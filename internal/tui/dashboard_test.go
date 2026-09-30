package tui

import (
	"testing"
	"time"

	"scriptstui/internal/procman"
)

func TestCompactDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		34 * time.Second:               "34s",
		12*time.Minute + 5*time.Second: "12m",
		65 * time.Minute:               "1h05m",
		51 * time.Hour:                 "2d3h",
	} {
		if got := compactDuration(d); got != want {
			t.Errorf("compactDuration(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestApplyHealth(t *testing.T) {
	started := time.Now().Add(-time.Minute)
	s := &serviceState{status: procman.StatusRunning, startedAt: started}
	m := Model{services: []*serviceState{s}, index: map[string]int{"a": 0}}
	health := func(since time.Time, ok bool, rtt time.Duration) procman.Event {
		return procman.Event{ServiceID: "a", Kind: procman.EventHealth, Since: since, Healthy: ok, RTT: rtt, Measured: rtt > 0, Text: "EOF"}
	}

	// A check of an earlier session of the same tunnel is ignored.
	m.applyHealth(0, health(started.Add(-time.Hour), false, 0))
	if s.health != healthUnknown {
		t.Fatal("un resultado de una sesión anterior cambió la salud")
	}

	m.applyHealth(0, health(started, false, 0))
	if s.health != healthBad || len(s.logs) != 1 {
		t.Fatalf("fallo: health=%v logs=%d", s.health, len(s.logs))
	}
	m.applyHealth(0, health(started, true, 40*time.Millisecond))
	if s.health != healthOK || !s.latencyKnown || len(s.logs) != 2 {
		t.Fatal("la recuperación no se registró")
	}
	if text, _ := statusCell(s); text != "activo · 1m · 40ms" {
		t.Fatalf("statusCell = %q", text)
	}
}

func TestFailedCountdown(t *testing.T) {
	s := &serviceState{status: procman.StatusFailed, statusSince: time.Now().Add(-5 * time.Second)}
	if text, _ := statusCell(s); text != "falló · se detiene en 25s" {
		t.Fatalf("statusCell = %q", text)
	}
}
