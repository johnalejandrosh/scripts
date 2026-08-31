package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"scriptstui/internal/config"
	"scriptstui/internal/prefs"
	"scriptstui/internal/procman"
	"scriptstui/internal/ssologin"
	"scriptstui/internal/store"
)

// App is the Wails-bound backend: a thin adapter over the same
// internal/config, internal/procman and internal/ssologin packages the
// terminal UI (internal/tui) uses, so both front ends share one source of
// truth for tunnels and AWS SSO accounts.
type App struct {
	ctx context.Context
	mgr *procman.Manager

	// store is the shared tunnels database at the project root — the same file
	// the terminal UI creates and edits, so both front ends list the same
	// tunnels and see the same profile assignments. Tunnels are created,
	// edited and deleted from the terminal UI; this one only reads them.
	store *store.Store

	npMu sync.Mutex
	np   *pendingNewProfile // in-flight "new SSO profile" wizard, one at a time
}

// pendingNewProfile carries state across the multiple frontend calls that
// make up the new-profile wizard (start -> pick account -> pick role ->
// write+login), mirroring internal/tui's newProfileWizard.
type pendingNewProfile struct {
	cancel      context.CancelFunc
	region      string
	startURL    string
	client      ssologin.OIDCClient
	device      ssologin.DeviceAuthorization
	accessToken string
}

func NewApp() (*App, error) {
	st, err := store.Open(store.DefaultPath())
	if err != nil {
		return nil, err
	}
	if _, err := st.Seed(config.Services(), prefs.LoadTunnelProfiles()); err != nil {
		st.Close()
		return nil, err
	}

	events := make(chan procman.Event, 4096)
	return &App{
		mgr:   procman.NewManager(events),
		store: st,
	}, nil
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go a.forwardTunnelEvents()
}

func (a *App) shutdown(ctx context.Context) {
	a.mgr.StopAll()
	if a.store != nil {
		a.store.Close()
	}
}

func (a *App) forwardTunnelEvents() {
	for ev := range a.mgr.Events() {
		kind := "log"
		if ev.Kind == procman.EventStatus {
			kind = "status"
		}
		runtime.EventsEmit(a.ctx, "tunnel:event", map[string]any{
			"serviceId": ev.ServiceID,
			"kind":      kind,
			"label":     ev.Label,
			"text":      ev.Text,
			"status":    ev.Status.String(),
		})
	}
}

// --- Tunnels ---------------------------------------------------------------

type ServiceView struct {
	ID       string `json:"id"`
	Proyecto string `json:"proyecto"`
	Title    string `json:"title"`
	Profile  string `json:"profile"`
	// LocalPort is the port the tunnel listens on locally, and the connection
	// strings are the ones stored alongside it for each OS.
	LocalPort               int    `json:"localPort"`
	ConnectionStringMac     string `json:"connectionStringMac"`
	ConnectionStringWindows string `json:"connectionStringWindows"`
	// Command is every step's full command line (one per line), with the
	// assigned profile already spliced in, so the UI can show it and the user
	// can copy it to run the tunnel by hand.
	Command string `json:"command"`
}

// ListServices returns every configured tunnel plus whatever AWS profile is
// currently assigned to it. Live status/logs arrive separately over the
// "tunnel:event" event as they happen.
func (a *App) ListServices() []ServiceView {
	rows, err := a.store.Enabled()
	if err != nil {
		return nil
	}
	out := make([]ServiceView, len(rows))
	for i, r := range rows {
		out[i] = ServiceView{
			ID:                      r.ID,
			Proyecto:                r.Proyecto,
			Title:                   r.Title,
			Profile:                 r.Profile,
			LocalPort:               r.LocalPort,
			ConnectionStringMac:     r.ConnStringMac,
			ConnectionStringWindows: r.ConnStringWin,
			Command:                 commandLines(r.Service(), r.Profile),
		}
	}
	return out
}

// AssignProfile sets (or, if profile is "", clears) the AWS CLI profile a
// tunnel's steps should run as, and persists the choice. Takes effect on the
// next start.
func (a *App) AssignProfile(serviceID, profile string) error {
	return a.store.SetProfile(serviceID, profile)
}

// commandLines renders svc's steps as pasteable command lines, one per line,
// as they would run with profile assigned.
func commandLines(svc config.Service, profile string) string {
	lines := make([]string, len(svc.Steps))
	for i, step := range svc.Steps {
		if step.Kind == config.StepTunnel && profile != "" {
			step.Profile = profile
		}
		lines[i] = step.CommandLine()
	}
	return strings.Join(lines, "\n")
}

func withProfile(svc config.Service, profile string) config.Service {
	steps := make([]config.Step, len(svc.Steps))
	copy(steps, svc.Steps)
	for i := range steps {
		if steps[i].Kind == config.StepTunnel {
			steps[i].Profile = profile
		}
	}
	svc.Steps = steps
	return svc
}

// StartService launches serviceID's steps, using whatever profile is
// currently assigned to it (falling back to ambient/pasted credentials).
func (a *App) StartService(serviceID string) error {
	row, err := a.store.Get(serviceID)
	if err != nil {
		return fmt.Errorf("túnel desconocido: %s", serviceID)
	}
	if !row.Enabled {
		return fmt.Errorf("el túnel %s está desactivado", serviceID)
	}
	svc := row.Service()
	if row.Profile != "" {
		svc = withProfile(svc, row.Profile)
	}
	a.mgr.Start(svc)
	return nil
}

func (a *App) StopService(serviceID string) {
	a.mgr.Stop(serviceID)
}

// --- AWS SSO accounts --------------------------------------------------------

type SSOProfileView struct {
	Name      string `json:"name"`
	AccountID string `json:"accountId"`
	RoleName  string `json:"roleName"`
	LoggedIn  bool   `json:"loggedIn"`
	ExpiresAt string `json:"expiresAt"` // RFC3339, empty if unknown
}

// ListSSOProfiles discovers every SSO profile in ~/.aws/config plus its live
// login status and, if logged in, when that cached session expires.
func (a *App) ListSSOProfiles() ([]SSOProfileView, error) {
	profiles, err := ssologin.DiscoverProfiles()
	if err != nil {
		return nil, err
	}
	// CheckStatus shells out to `aws sts get-caller-identity` per profile, so
	// fan out: a user who imported a whole portal can have dozens of them.
	out := make([]SSOProfileView, len(profiles))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, p := range profiles {
		wg.Add(1)
		go func(i int, p ssologin.Profile) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			v := SSOProfileView{Name: p.Name, AccountID: p.AccountID, RoleName: p.RoleName}
			v.LoggedIn = ssologin.CheckStatus(p.Name)
			if t, ok := ssologin.SessionExpiry(p); ok {
				v.ExpiresAt = t.Format(time.RFC3339)
			}
			out[i] = v
		}(i, p)
	}
	wg.Wait()
	return out, nil
}

// LoginSSO runs `aws sso login --profile profileName` in the background,
// streaming its output as "sso:line" events and finishing with "sso:done".
func (a *App) LoginSSO(profileName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	lines, done := ssologin.Login(ctx, profileName)
	go a.streamSSO(cancel, lines, done, profileName, "login")
}

// LogoutSSO runs `aws sso logout`, which the AWS CLI applies globally (there
// is no per-profile logout).
func (a *App) LogoutSSO() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	lines, done := ssologin.Logout(ctx)
	go a.streamSSO(cancel, lines, done, "", "logout")
}

func (a *App) streamSSO(cancel context.CancelFunc, lines <-chan string, done <-chan error, profile, action string) {
	defer cancel()
	pendingURL := ""
	for line := range lines {
		trimmed := strings.TrimSpace(line)
		runtime.EventsEmit(a.ctx, "sso:line", map[string]any{"profile": profile, "action": action, "line": line})
		if strings.HasPrefix(trimmed, "https://") {
			pendingURL = trimmed
		} else if pendingURL != "" && ssologin.DeviceCodePattern.MatchString(trimmed) {
			runtime.EventsEmit(a.ctx, "sso:line", map[string]any{
				"profile": profile, "action": action,
				"line": "Enlace completo (copiar y pegar en el navegador): " + pendingURL + "?user_code=" + trimmed,
			})
			pendingURL = ""
		}
	}
	err := <-done
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	runtime.EventsEmit(a.ctx, "sso:done", map[string]any{"profile": profile, "action": action, "error": msg})
}

// --- New SSO profile wizard --------------------------------------------------

type DeviceAuthView struct {
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
}

// StartNewSSOProfile registers this app as an OIDC client, starts a device
// authorization for startURL, opens it in the browser, and begins polling
// for approval in the background. Once approved, the account list arrives
// over the "sso:new:accounts" event ("sso:new:error" on failure).
func (a *App) StartNewSSOProfile(startURL, region string) (DeviceAuthView, error) {
	a.npMu.Lock()
	if a.np != nil && a.np.cancel != nil {
		a.np.cancel()
	}
	a.npMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())

	client, err := ssologin.RegisterClient(ctx, region)
	if err != nil {
		cancel()
		return DeviceAuthView{}, err
	}
	device, err := ssologin.StartDeviceAuthorization(ctx, region, client, startURL)
	if err != nil {
		cancel()
		return DeviceAuthView{}, err
	}

	a.npMu.Lock()
	a.np = &pendingNewProfile{cancel: cancel, region: region, startURL: startURL, client: client, device: device}
	a.npMu.Unlock()

	ssologin.OpenBrowser(device.VerificationURIComplete)
	go a.pollNewProfileToken(ctx, region, client, device)

	return DeviceAuthView{
		UserCode:                device.UserCode,
		VerificationURI:         device.VerificationURI,
		VerificationURIComplete: device.VerificationURIComplete,
	}, nil
}

func (a *App) pollNewProfileToken(ctx context.Context, region string, client ssologin.OIDCClient, device ssologin.DeviceAuthorization) {
	token, err := ssologin.PollForToken(ctx, region, client, device)
	if err != nil {
		runtime.EventsEmit(a.ctx, "sso:new:error", err.Error())
		return
	}

	a.npMu.Lock()
	if a.np != nil {
		a.np.accessToken = token
	}
	a.npMu.Unlock()

	accounts, err := ssologin.ListAccounts(ctx, region, token)
	if err != nil {
		runtime.EventsEmit(a.ctx, "sso:new:error", err.Error())
		return
	}
	runtime.EventsEmit(a.ctx, "sso:new:accounts", accounts)
}

// ListSSORoles lists the roles the just-authorized user can assume into
// accountID. Only valid after "sso:new:accounts" has fired for the current
// wizard session.
func (a *App) ListSSORoles(accountID string) ([]ssologin.Role, error) {
	a.npMu.Lock()
	np := a.np
	a.npMu.Unlock()
	if np == nil || np.accessToken == "" {
		return nil, errors.New("no hay una sesión de alta de cuenta en curso")
	}
	return ssologin.ListAccountRoles(context.Background(), np.region, np.accessToken, accountID)
}

// FinishNewSSOProfile writes the [profile ...] block to ~/.aws/config for
// accountID+roleName and immediately logs into it (streamed like any other
// LoginSSO call, under action "login"). Ends the wizard session either way.
func (a *App) FinishNewSSOProfile(accountID, roleName, cliRegion string) (string, error) {
	a.npMu.Lock()
	np := a.np
	a.np = nil
	a.npMu.Unlock()
	if np == nil {
		return "", errors.New("no hay una sesión de alta de cuenta en curso")
	}
	defer np.cancel()

	name := ssologin.ProfileName(accountID, roleName)
	if _, err := ssologin.WriteProfile(name, np.startURL, np.region, accountID, roleName, cliRegion); err != nil {
		return "", err
	}

	a.LoginSSO(name)
	return name, nil
}

// CancelNewSSOProfile aborts an in-flight wizard session, if any.
func (a *App) CancelNewSSOProfile() {
	a.npMu.Lock()
	if a.np != nil && a.np.cancel != nil {
		a.np.cancel()
	}
	a.np = nil
	a.npMu.Unlock()
}
