package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"scriptstui/internal/procman"
	"scriptstui/internal/ssologin"
)

// maxParallelProbes caps a parallel verification, as in the terminal UI:
// enabled tunnels never share a local port, so the cap only keeps a burst of
// StartSession calls from tripping AWS throttling.
const maxParallelProbes = 4

// VerifyItem is one tunnel's line in a verification.
type VerifyItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// State is queued, checking, ok, failed or skipped.
	State string `json:"state"`
	Note  string `json:"note"`
}

// VerifyUpdate is the whole run as the page draws it, sent on every change.
type VerifyUpdate struct {
	Items     []VerifyItem `json:"items"`
	Parallel  bool         `json:"parallel"`
	Done      bool         `json:"done"`
	ElapsedMs int64        `json:"elapsedMs"`
	Disabled  int          `json:"disabled"`
}

// StartVerify checks every enabled tunnel — one at a time, or up to
// maxParallelProbes at once — and reports progress as "verify:update"
// events. A stopped tunnel is opened briefly and closed again; a running one
// is only tested on its port, so nothing that works gets interrupted.
func (a *App) StartVerify(parallel bool) VerifyUpdate {
	a.CancelVerify()
	ctx, cancel := context.WithCancel(context.Background())
	a.verifyMu.Lock()
	a.verifyCancel = cancel
	a.verifyMu.Unlock()

	all, _ := a.store.List()
	rows, _ := a.store.Enabled()
	run := &VerifyUpdate{Parallel: parallel, Disabled: len(all) - len(rows)}
	for _, r := range rows {
		run.Items = append(run.Items, VerifyItem{ID: r.ID, Title: r.Title, State: "queued", Note: "en cola"})
	}

	var mu sync.Mutex
	started := time.Now()
	snapshot := func() VerifyUpdate {
		out := *run
		out.Items = append([]VerifyItem(nil), run.Items...)
		out.ElapsedMs = time.Since(started).Milliseconds()
		return out
	}
	emit := func() { runtime.EventsEmit(a.ctx, "verify:update", snapshot()) }

	go func() {
		limit := 1
		if parallel {
			limit = maxParallelProbes
		}
		sem := make(chan struct{}, limit)
		var wg sync.WaitGroup
		for i, r := range rows {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
			}
			if ctx.Err() != nil {
				break
			}

			a.tunnelsMu.Lock()
			status := a.live(r.ID).Status
			a.tunnelsMu.Unlock()
			mu.Lock()
			item := &run.Items[i]
			switch status {
			case procman.StatusStarting.String(), procman.StatusStopping.String():
				item.State, item.Note = "skipped", "omitido: está "+status
				mu.Unlock()
				<-sem
				emit()
				continue
			}
			running := status == procman.StatusRunning.String()
			item.State = "checking"
			item.Note = "abriendo túnel de prueba..."
			if running {
				item.Note = "activo, probando su puerto..."
			}
			mu.Unlock()
			emit()

			svc := r.Service()
			if r.Profile != "" {
				svc = withProfile(svc, r.Profile)
			}
			wg.Add(1)
			go func(i int, id string) {
				defer wg.Done()
				defer func() { <-sem }()
				var err error
				if running {
					err = procman.CheckPort(r.LocalPort, r.RemotePort)
				} else {
					err = a.mgr.Probe(ctx, svc)
				}
				if ctx.Err() != nil {
					return
				}
				mu.Lock()
				item := &run.Items[i]
				if err != nil {
					item.State, item.Note = "failed", strings.SplitN(err.Error(), "\n", 2)[0]
				} else {
					item.State, item.Note = "ok", "conexión ok"
				}
				mu.Unlock()
				a.markVerified(id, err == nil)
				emit()
			}(i, r.ID)
		}
		wg.Wait()
		if ctx.Err() != nil {
			return
		}
		mu.Lock()
		run.Done = true
		mu.Unlock()
		emit()
		cancel()
	}()

	mu.Lock()
	defer mu.Unlock()
	return snapshot()
}

// CancelVerify stops a verification in progress; tunnels it had opened for
// the test are closed.
func (a *App) CancelVerify() {
	a.verifyMu.Lock()
	defer a.verifyMu.Unlock()
	if a.verifyCancel != nil {
		a.verifyCancel()
		a.verifyCancel = nil
	}
}

// markVerified leaves the result on the tunnel's row, where it fades out.
func (a *App) markVerified(id string, ok bool) {
	a.tunnelsMu.Lock()
	t := a.live(id)
	t.VerifiedAt, t.VerifiedOK = time.Now().Format(time.RFC3339), ok
	live := *t
	a.tunnelsMu.Unlock()
	runtime.EventsEmit(a.ctx, "tunnel:event", map[string]any{"serviceId": id, "live": live})
}

// SSOSummary is the state of the SSO sessions the enabled tunnels use.
type SSOSummary struct {
	Expired int `json:"expired"`
	// SoonestSeconds is how long until the next one expires; valid when
	// Known.
	SoonestSeconds int64 `json:"soonestSeconds"`
	Known          bool  `json:"known"`
}

// GetSSOSummary reads the AWS CLI's token cache — no network — for the
// profiles assigned to enabled tunnels.
func (a *App) GetSSOSummary() SSOSummary {
	rows, err := a.store.Enabled()
	if err != nil {
		return SSOSummary{}
	}
	used := make(map[string]bool)
	for _, r := range rows {
		if r.Profile != "" {
			used[r.Profile] = true
		}
	}
	profiles, err := ssologin.DiscoverProfiles()
	if err != nil {
		return SSOSummary{}
	}
	var out SSOSummary
	now := time.Now()
	for _, p := range profiles {
		if !used[p.Name] {
			continue
		}
		at, ok := ssologin.SessionExpiry(p)
		if !ok {
			continue
		}
		left := at.Sub(now)
		if left <= 0 {
			out.Expired++
			continue
		}
		if s := int64(left.Seconds()); !out.Known || s < out.SoonestSeconds {
			out.SoonestSeconds, out.Known = s, true
		}
	}
	return out
}
