// Package tui implements a lazygit-style terminal UI: a service list on the
// left, live combined logs of the selected service on the right, a
// full-screen modal to paste AWS credentials, and a panel to log into (or
// create) AWS SSO profiles discovered from the user's own ~/.aws/config.
package tui

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"scriptstui/internal/awscreds"
	"scriptstui/internal/clipboard"
	"scriptstui/internal/config"
	"scriptstui/internal/procman"
	"scriptstui/internal/ssologin"
	"scriptstui/internal/store"
)

const maxLogLines = 2000

// ssoDeviceCodePattern matches the "XXXX-XXXX" device code `aws sso login`
// prints on its own line, so we can splice it onto the verification URL it
// printed just above into one pasteable link. Shared with the desktop
// front end (see desktop/app.go) via ssologin.DeviceCodePattern.
var ssoDeviceCodePattern = ssologin.DeviceCodePattern

// logBoxChrome is what logBoxStyle costs around its contents: a border on
// each side plus one column of padding on each side.
const logBoxChrome = 4

// baseListWidth is the tunnel panel's width without the proyecto column.
const baseListWidth = 74
const titleFieldWidth = 34

// projectColumnWidth is how much the panel grows once tunnels start carrying
// a proyecto; it stays hidden while every proyecto is empty.
const projectColumnWidth = 12

// portFieldWidth reserves room for the "[NNNNN]" local-port field shown
// between a tunnel's title and its status, so the statuses stay aligned
// whether or not a service has a port.
const portFieldWidth = 7

// maxImportWarnings caps how many per-account problems an import prints
// before collapsing the rest into a count, so one broken account can't push
// the summary off the panel.
const maxImportWarnings = 5

// visibleWindow returns the [start,end) slice of a list of total items that
// fits in maxItems rows while keeping cursor inside it — importing a whole
// portal can easily produce more profiles than the terminal has rows.
func visibleWindow(cursor, total, maxItems int) (int, int) {
	if maxItems <= 0 || total <= maxItems {
		return 0, total
	}
	start := cursor - maxItems/2
	if start < 0 {
		start = 0
	}
	if start+maxItems > total {
		start = total - maxItems
	}
	return start, start + maxItems
}

type mode int

const (
	modeList mode = iota
	modeCredentials
	modeSSO
	modeAssignProfile
	modeNewProfile
	modeTunnelForm
	modeTunnelDelete
)

var (
	colorAccent  = lipgloss.Color("#7D56F4")
	colorRunning = lipgloss.Color("#3FB950")
	colorBusy    = lipgloss.Color("#D4A72C")
	colorFailed  = lipgloss.Color("#F85149")
	colorStopped = lipgloss.Color("#6E7681")
	colorMarked  = lipgloss.Color("#39C5CF")
	colorMuted   = lipgloss.Color("#767676")
	colorText    = lipgloss.Color("#E6E6E6")

	appStyle = lipgloss.NewStyle().Padding(0, 1)

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#F5F5F5")).
			Background(colorAccent).
			Padding(0, 2)

	listBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorAccent).
			Padding(0, 1)

	logBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#3A3A3A")).
			Padding(0, 1)

	logHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)

	cursorStyle   = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	itemStyle     = lipgloss.NewStyle().Foreground(colorText)
	itemDimStyle  = lipgloss.NewStyle().Foreground(colorMuted)
	selectedTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF"))
	checkedStyle  = lipgloss.NewStyle().Bold(true).Foreground(colorMarked)
	portStyle     = lipgloss.NewStyle().Foreground(colorMarked)

	helpStyle    = lipgloss.NewStyle().Foreground(colorMuted)
	helpKeyStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)

	labelStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#5FD7FF"))

	modalBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorAccent).
			Padding(1, 3)

	modalTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	modalHintStyle  = lipgloss.NewStyle().Foreground(colorMuted)
	modalErrStyle   = lipgloss.NewStyle().Bold(true).Foreground(colorFailed)
)

func statusColor(s procman.Status) lipgloss.Color {
	switch s {
	case procman.StatusRunning:
		return colorRunning
	case procman.StatusStarting, procman.StatusStopping:
		return colorBusy
	case procman.StatusFailed:
		return colorFailed
	default:
		return colorStopped
	}
}

func statusDot(s procman.Status) string {
	return lipgloss.NewStyle().Foreground(statusColor(s)).Render("●")
}

func padTrunc(s string, w int) string {
	r := []rune(s)
	if len(r) > w {
		if w <= 1 {
			return string(r[:w])
		}
		return string(r[:w-1]) + "…"
	}
	return s + strings.Repeat(" ", w-len(r))
}

// projectFieldWidth returns the width the proyecto column needs: zero until
// at least one tunnel has one, so a freshly seeded list looks exactly as it
// did before the column existed.
func (m Model) projectFieldWidth() int {
	for _, s := range m.services {
		if strings.TrimSpace(s.row.Proyecto) != "" {
			return projectColumnWidth
		}
	}
	return 0
}

// listWidth is the tunnel panel's width, grown to fit the proyecto column
// when it is in use.
func (m Model) listWidth() int {
	if w := m.projectFieldWidth(); w > 0 {
		return baseListWidth + w + 1
	}
	return baseListWidth
}

func helpEntry(key, desc string) string {
	return helpKeyStyle.Render(key) + " " + helpStyle.Render(desc)
}

// connectionStringFor returns the tunnel's stored connection string for the
// running OS plus the name of the column it came from. Anything that isn't
// Windows reads the mac column, since the POSIX form works there too.
func connectionStringFor(row store.Tunnel) (value, column string) {
	if runtime.GOOS == "windows" {
		return row.ConnStringWin, "windows"
	}
	return row.ConnStringMac, "mac"
}

// withProfile returns a copy of svc with every tunnel step set to run as
// the given AWS CLI profile (`aws ... --profile profile`). Passing "" clears
// it, falling back to ambient/pasted credentials.
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

type serviceState struct {
	cfg config.Service
	// row is the tunnels row this service was built from, kept so the edit
	// form can be prefilled and so the list can tell a disabled tunnel apart.
	row       store.Tunnel
	status    procman.Status
	logs      []string
	startedAt time.Time
}

type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

type credsCheckResultMsg struct {
	ok      bool
	err     error
	targets []string
}

type credsValidateResultMsg struct {
	ok      bool
	err     error
	creds   awscreds.Credentials
	targets []string
}

func checkAmbientCredsCmd(targets []string) tea.Cmd {
	return func() tea.Msg {
		err := awscreds.Validate(nil, 5*time.Second)
		return credsCheckResultMsg{ok: err == nil, err: err, targets: targets}
	}
}

func validatePastedCredsCmd(text string, targets []string) tea.Cmd {
	return func() tea.Msg {
		creds, err := awscreds.Parse(text)
		if err != nil {
			return credsValidateResultMsg{ok: false, err: err, targets: targets}
		}
		if err := awscreds.Validate(creds.Env(), 8*time.Second); err != nil {
			return credsValidateResultMsg{ok: false, err: err, creds: creds, targets: targets}
		}
		return credsValidateResultMsg{ok: true, creds: creds, targets: targets}
	}
}

// ssoRefreshMsg carries the profiles currently found in ~/.aws/config plus
// each one's login status, in a single message so the SSO panel and the
// profile-assignment picker never render half-updated state.
type ssoRefreshMsg struct {
	profiles []ssologin.Profile
	status   map[string]bool
	expiry   map[string]time.Time
	err      error
}

func refreshSSOCmd() tea.Cmd {
	return func() tea.Msg {
		profiles, err := ssologin.DiscoverProfiles()
		if err != nil {
			return ssoRefreshMsg{err: err}
		}
		// One `aws sts get-caller-identity` per profile, so once a whole
		// portal has been imported this has to fan out: sequentially it would
		// freeze the panel for a minute on every refresh.
		status := make(map[string]bool, len(profiles))
		expiry := make(map[string]time.Time, len(profiles))
		var mu sync.Mutex
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		for _, p := range profiles {
			wg.Add(1)
			go func(p ssologin.Profile) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				loggedIn := ssologin.CheckStatus(p.Name)
				expiresAt, hasExpiry := ssologin.SessionExpiry(p)
				mu.Lock()
				defer mu.Unlock()
				status[p.Name] = loggedIn
				if hasExpiry {
					expiry[p.Name] = expiresAt
				}
			}(p)
		}
		wg.Wait()
		return ssoRefreshMsg{profiles: profiles, status: status, expiry: expiry}
	}
}

// ssoImportMsg carries the outcome of importing every account and role the
// SSO user can reach into ~/.aws/config.
type ssoImportMsg struct {
	result ssologin.ImportResult
	err    error
}

// importAllProfilesCmd creates one profile per account+role pair the portal
// at startURL exposes to this user, reusing an SSO access token we already
// have so nobody has to approve a second browser prompt.
func importAllProfilesCmd(region, startURL, token string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		result, err := ssologin.ImportAllProfiles(ctx, region, startURL, token)
		return ssoImportMsg{result: result, err: err}
	}
}

type ssoLineMsg struct {
	line string
}

type ssoActionDoneMsg struct {
	err     error
	profile string
	action  string
}

// waitSSOOutput drains one line from an in-flight login/logout, or — once
// lines closes — reports its final result. Re-issue this after every
// ssoLineMsg to keep draining until ssoActionDoneMsg arrives.
func waitSSOOutput(lines <-chan string, done <-chan error, profile, action string) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-lines
		if ok {
			return ssoLineMsg{line: line}
		}
		return ssoActionDoneMsg{err: <-done, profile: profile, action: action}
	}
}

// --- New-profile wizard: register client, device auth, account/role pick ---

type npStep int

const (
	npStepForm npStep = iota
	npStepWaiting
	npStepAccounts
	npStepRoles
)

type newProfileWizard struct {
	step npStep

	urlInput    textinput.Model
	regionInput textinput.Model
	focus       int // 0 = url field, 1 = region field
	formErr     string

	region      string
	startURL    string
	accessToken string

	client  ssologin.OIDCClient
	device  ssologin.DeviceAuthorization
	ctx     context.Context
	cancel  context.CancelFunc
	waitErr string

	accounts       []ssologin.Account
	accountCursor  int
	accountsLoaded bool
	account        ssologin.Account

	roles       []ssologin.Role
	roleCursor  int
	rolesLoaded bool

	err string
}

func newProfileWizardForm(width int) (*newProfileWizard, tea.Cmd) {
	w := width - 24
	if w > 70 {
		w = 70
	}
	if w < 30 {
		w = 30
	}

	url := textinput.New()
	url.Placeholder = "https://d-90671b694f.awsapps.com/start"
	url.Prompt = "URL del portal:  "
	url.CharLimit = 200
	url.Width = w
	focusCmd := url.Focus()

	region := textinput.New()
	region.Placeholder = "us-east-1"
	region.Prompt = "Región SSO:     "
	region.CharLimit = 30
	region.Width = w

	return &newProfileWizard{urlInput: url, regionInput: region}, focusCmd
}

type npDeviceStartedMsg struct {
	client ssologin.OIDCClient
	device ssologin.DeviceAuthorization
	region string
	err    error
}

type npTokenMsg struct {
	token string
	err   error
}

type npAccountsMsg struct {
	accounts []ssologin.Account
	err      error
}

type npRolesMsg struct {
	roles []ssologin.Role
	err   error
}

type npWriteDoneMsg struct {
	profileName string
	err         error
}

func npStartDeviceAuthCmd(ctx context.Context, region, startURL string) tea.Cmd {
	return func() tea.Msg {
		client, err := ssologin.RegisterClient(ctx, region)
		if err != nil {
			return npDeviceStartedMsg{err: err}
		}
		device, err := ssologin.StartDeviceAuthorization(ctx, region, client, startURL)
		if err != nil {
			return npDeviceStartedMsg{err: err}
		}
		return npDeviceStartedMsg{client: client, device: device, region: region}
	}
}

func npPollTokenCmd(ctx context.Context, region string, client ssologin.OIDCClient, device ssologin.DeviceAuthorization) tea.Cmd {
	return func() tea.Msg {
		token, err := ssologin.PollForToken(ctx, region, client, device)
		return npTokenMsg{token: token, err: err}
	}
}

func npListAccountsCmd(ctx context.Context, region, token string) tea.Cmd {
	return func() tea.Msg {
		accounts, err := ssologin.ListAccounts(ctx, region, token)
		return npAccountsMsg{accounts: accounts, err: err}
	}
}

func npListRolesCmd(ctx context.Context, region, token, accountID string) tea.Cmd {
	return func() tea.Msg {
		roles, err := ssologin.ListAccountRoles(ctx, region, token, accountID)
		return npRolesMsg{roles: roles, err: err}
	}
}

func npWriteProfileCmd(name, startURL, region, accountID, roleName string) tea.Cmd {
	return func() tea.Msg {
		_, err := ssologin.WriteProfile(name, startURL, region, accountID, roleName, region)
		return npWriteDoneMsg{profileName: name, err: err}
	}
}

func newCredsTextarea(totalWidth int) (textarea.Model, tea.Cmd) {
	ta := textarea.New()
	ta.Placeholder = "export AWS_ACCESS_KEY_ID=...\nexport AWS_SECRET_ACCESS_KEY=...\nexport AWS_SESSION_TOKEN=..."
	ta.ShowLineNumbers = false
	w := totalWidth - 20
	if w > 64 {
		w = 64
	}
	if w < 30 {
		w = 30
	}
	ta.SetWidth(w)
	ta.SetHeight(6)
	cmd := ta.Focus()
	return ta, cmd
}

type Model struct {
	mgr      *procman.Manager
	services []*serviceState
	index    map[string]int
	cursor   int
	marked   map[string]bool
	// store is where the tunnels live; every create/edit/delete goes through
	// it and the list is rebuilt from it afterwards.
	store *store.Store
	// tunnelProfile maps service ID -> AWS CLI profile name, mirroring the
	// profile column so the header and the command view can read it cheaply.
	tunnelProfile map[string]string
	storeErr      string // last failure talking to the database, shown in the UI

	viewport viewport.Model
	ready    bool
	// commandBlock is the selected tunnel's command line, already wrapped and
	// height-budgeted by layout() so the view just prints it.
	commandBlock string
	// connBlock is the selected tunnel's stored connection string for this
	// OS, rendered separately from commandBlock so a short terminal truncates
	// the long aws command rather than the one line most worth copying.
	connBlock string
	// logPanelWidth is the log panel's outer width. Its contents are laid out
	// at viewport.Width, which is narrower by the box's borders and padding —
	// measuring at the outer width made every wrapped line come out one row
	// short of what the box actually drew.
	logPanelWidth int
	// logPrefix is everything the log panel draws above the logs — header,
	// command and connection line — already wrapped and trimmed to fit, so
	// the view just prints it.
	logPrefix  string
	width      int
	height     int
	listHeight int

	mode          mode
	credsInput    textarea.Model
	credsChecking bool
	credsHint     string
	credsError    string
	pendingStart  []string
	awsReady      bool

	discoveredProfiles []ssologin.Profile
	ssoStatus          map[string]bool
	ssoExpiry          map[string]time.Time
	ssoCursor          int
	ssoRunning         bool
	ssoAction          string
	ssoActiveProfile   string
	ssoOutput          []string
	ssoLines           <-chan string
	ssoDone            <-chan error
	ssoCancel          context.CancelFunc
	ssoRefreshErr      string
	ssoRefreshing      bool
	ssoImporting       bool
	ssoPendingURL      string

	assignTarget string
	assignCursor int

	// copyStatus replaces the log header's hint for a few seconds after 'y',
	// to confirm the command was copied (or say why it wasn't).
	copyStatus      string
	copyStatusUntil time.Time

	np *newProfileWizard
	tf *tunnelForm

	// deleteTarget is the id awaiting confirmation in modeTunnelDelete.
	deleteTarget string

	showHelp    bool
	showAllLogs bool

	quitting bool
}

// NewModel builds the UI over st, reading the tunnel list from it. A failure
// here is fatal for the caller: without the database there is nothing to show.
func NewModel(st *store.Store, mgr *procman.Manager) (Model, error) {
	m := Model{
		mgr:           mgr,
		store:         st,
		index:         make(map[string]int),
		marked:        make(map[string]bool),
		tunnelProfile: make(map[string]string),
		ssoStatus:     make(map[string]bool),
		ssoExpiry:     make(map[string]time.Time),
	}
	if err := m.reloadTunnels(); err != nil {
		return Model{}, err
	}
	return m, nil
}

// reloadTunnels rebuilds the list from the database, carrying over the status
// and logs of every tunnel that is still there — editing one tunnel must not
// wipe the output of another that is running.
func (m *Model) reloadTunnels() error {
	rows, err := m.store.List()
	if err != nil {
		return err
	}

	previous := make(map[string]*serviceState, len(m.services))
	for _, s := range m.services {
		previous[s.cfg.ID] = s
	}

	states := make([]*serviceState, len(rows))
	index := make(map[string]int, len(rows))
	profiles := make(map[string]string, len(rows))
	for i, r := range rows {
		st := &serviceState{cfg: r.Service(), row: r, status: procman.StatusStopped}
		if prev, ok := previous[r.ID]; ok {
			st.status, st.logs, st.startedAt = prev.status, prev.logs, prev.startedAt
		}
		states[i] = st
		index[r.ID] = i
		if r.Profile != "" {
			profiles[r.ID] = r.Profile
		}
	}

	m.services = states
	m.index = index
	m.tunnelProfile = profiles

	// A delete can leave the cursor past the end, and marks pointing at
	// tunnels that no longer exist.
	if m.cursor >= len(states) {
		m.cursor = len(states) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	for id := range m.marked {
		if _, ok := index[id]; !ok {
			delete(m.marked, id)
		}
	}
	return nil
}

func waitForEvent(events <-chan procman.Event) tea.Cmd {
	return func() tea.Msg {
		return <-events
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(waitForEvent(m.mgr.Events()), tick(), refreshSSOCmd())
}

// selected returns the highlighted tunnel, or nil when there are none left —
// the list starts empty on a fresh database and can be emptied by deleting.
func (m *Model) selected() *serviceState {
	if m.cursor < 0 || m.cursor >= len(m.services) {
		return nil
	}
	return m.services[m.cursor]
}

// targets returns the marked services' IDs, or just the highlighted one if
// nothing is marked.
func (m *Model) targets() []string {
	if len(m.marked) == 0 {
		sel := m.selected()
		if sel == nil {
			return nil
		}
		return []string{sel.cfg.ID}
	}
	ids := make([]string, 0, len(m.marked))
	for id := range m.marked {
		ids = append(ids, id)
	}
	return ids
}

// needsAmbientCredsCheck returns the ids among targets that have no AWS CLI
// profile assigned, i.e. they depend on ambient/pasted credentials.
func (m *Model) needsAmbientCredsCheck(ids []string) []string {
	var out []string
	for _, id := range ids {
		if m.tunnelProfile[id] == "" {
			out = append(out, id)
		}
	}
	return out
}

func (m *Model) startServices(ids []string) {
	for _, id := range ids {
		i, ok := m.index[id]
		if !ok {
			continue
		}
		s := m.services[i]
		if !s.row.Enabled {
			// A tunnel turned off has given up its port; starting it would
			// contradict what the list shows.
			continue
		}
		if s.status == procman.StatusRunning || s.status == procman.StatusStarting {
			continue
		}
		cfg := s.cfg
		if p := m.tunnelProfile[id]; p != "" {
			cfg = withProfile(cfg, p)
		}
		m.mgr.Start(cfg)
	}
}

func (m *Model) stopServices(ids []string) {
	for _, id := range ids {
		i, ok := m.index[id]
		if !ok {
			continue
		}
		s := m.services[i]
		if s.status == procman.StatusStopped {
			continue
		}
		m.mgr.Stop(id)
	}
}

func (m *Model) refreshViewportContent() {
	if !m.ready {
		return
	}
	s := m.selected()
	if s == nil {
		m.viewport.SetContent("")
		return
	}
	m.viewport.SetContent(strings.Join(s.logs, "\n"))
	m.viewport.GotoBottom()
}

// logHeader builds the line above the logs: which tunnel, its profile, and
// either the available shortcuts or the copy confirmation. It is wrapped to
// width here so layout() can measure the exact number of rows it will take —
// the hint is long enough to wrap on narrower terminals.
func (m Model) logHeader(width int) string {
	sel := m.selected()
	if sel == nil {
		return lipgloss.NewStyle().Width(width).Render(
			logHeaderStyle.Render("Sin túneles") + "  " +
				itemDimStyle.Render("(n para crear el primero)"))
	}

	profileInfo := "sin perfil asignado"
	if p := m.tunnelProfile[sel.cfg.ID]; p != "" {
		profileInfo = "perfil: " + p
	}

	hint := itemDimStyle.Render("(" + profileInfo + " · p perfil · y copiar comando · C copiar conexión)")
	if m.copyStatus != "" {
		style := lipgloss.NewStyle().Bold(true).Foreground(colorRunning)
		if strings.HasPrefix(m.copyStatus, "✗") {
			style = modalErrStyle
		}
		hint = style.Render(m.copyStatus)
	}

	line := logHeaderStyle.Render("Logs — "+sel.cfg.Title) + "  " + hint
	if m.storeErr != "" {
		line += "  " + modalErrStyle.Render(m.storeErr)
	}
	return lipgloss.NewStyle().Width(width).Render(line)
}

// commandFor returns the tunnel's command line(s) exactly as they will run —
// profile included, one per line, unwrapped — which is what gets copied.
func (m Model) commandFor(s *serviceState) string {
	profile := m.tunnelProfile[s.cfg.ID]
	lines := make([]string, len(s.cfg.Steps))
	for i, step := range s.cfg.Steps {
		if step.Kind == config.StepTunnel && profile != "" {
			step.Profile = profile
		}
		lines[i] = step.CommandLine()
	}
	return strings.Join(lines, "\n")
}

// commandView renders the highlighted tunnel's full `aws ssm start-session`
// line, broken before each flag the way it is written in internal/config, so
// it can be read at a glance and pasted into a terminal to run by hand. The
// assigned profile is spliced in, so what is shown is what will actually run.
func (m Model) commandView(width int) string {
	sel := m.selected()
	if sel == nil {
		return ""
	}
	profile := m.tunnelProfile[sel.cfg.ID]
	style := lipgloss.NewStyle().Foreground(colorMuted)

	var blocks []string
	for _, step := range sel.cfg.Steps {
		if step.Kind == config.StepTunnel && profile != "" {
			step.Profile = profile
		}
		blocks = append(blocks, style.Render(formatCommand(step.CommandLine(), width)))
	}
	return strings.Join(blocks, "\n")
}

// connectionView renders the stored connection string for this OS, or a hint
// pointing at the editor when the tunnel doesn't have one yet.
func (m Model) connectionView(width int) string {
	sel := m.selected()
	if sel == nil {
		return ""
	}
	conn, column := connectionStringFor(sel.row)
	label := "conexión (" + column + ")  "

	// Always one row: this line exists to identify the string, not to read it
	// off the panel — 'C' is what puts it on the clipboard — and letting a
	// long DSN wrap would eat three rows of logs.
	// Whatever is left after the label — never padded back up, since a line
	// wider than the panel wraps onto a second row and costs the box its
	// bottom border. On a panel too narrow even for the label, the label
	// itself is what gets cut.
	room := width - utf8.RuneCountInString(label)
	if room < 1 {
		return labelStyle.Render(padTrunc(label, width))
	}
	if conn == "" {
		return labelStyle.Render(label) + itemDimStyle.Render(padTrunc("sin definir · 'e' para agregarla", room))
	}
	return labelStyle.Render(label) + itemStyle.Render(padTrunc(conn, room))
}

// formatCommand lays a one-line command out as the multi-line, backslash-
// continued form: the program first, then one `--flag value` per line, each
// wrapped to width. Breaks land before a flag or at a space wherever possible,
// so what is shown still runs when pasted — the one exception being the
// --parameters JSON, which is a single token longer than any panel and has to
// be split mid-value to be shown in full.
func formatCommand(line string, width int) string {
	if width < 24 {
		width = 24
	}

	var lines []string
	parts := splitOnFlags(line)
	for i, part := range parts {
		if i > 0 {
			part = "  " + part // hanging indent under the program name
		}
		// -2 leaves room for the " \" continuation marker.
		chunks := wrapTokens(part, width-2)
		if i < len(parts)-1 {
			chunks[len(chunks)-1] += " \\"
		}
		lines = append(lines, chunks...)
	}
	return strings.Join(lines, "\n")
}

// splitOnFlags cuts a command line into the program plus one segment per
// `--flag value` pair. It only ever breaks before a `--` token, never inside
// a value.
func splitOnFlags(line string) []string {
	var out []string
	var cur []string
	for _, tok := range strings.Split(line, " ") {
		if strings.HasPrefix(tok, "--") && len(cur) > 0 {
			out = append(out, strings.Join(cur, " "))
			cur = nil
		}
		cur = append(cur, tok)
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, " "))
	}
	return out
}

// wrapTokens breaks s into visual lines of at most width, preferring the last
// space that fits so tokens stay whole, and cutting mid-token only when the
// token itself is wider than width.
func wrapTokens(s string, width int) []string {
	if width < 8 {
		width = 8
	}
	var out []string
	for len(s) > width {
		cut := strings.LastIndex(s[:width+1], " ")
		if cut <= 0 {
			cut = width
		}
		out = append(out, strings.TrimRight(s[:cut], " "))
		s = strings.TrimLeft(s[cut:], " ")
	}
	return append(out, s)
}

func (m *Model) layout() {
	const headerHeight = 1
	const footerHeight = 1
	const borderHeight = 2 // top + bottom border, shared by both panels

	bodyHeight := m.height - headerHeight - footerHeight
	if bodyHeight < 5 {
		bodyHeight = 5
	}
	// logPanelWidth is the box; logWidth is what fits inside it, once its two
	// borders and two columns of padding are taken out.
	m.logPanelWidth = m.width - m.listWidth() - 6
	if m.logPanelWidth < 24 {
		m.logPanelWidth = 24
	}
	logWidth := m.logPanelWidth - logBoxChrome
	if logWidth < 20 {
		logWidth = 20
	}

	m.listHeight = bodyHeight - borderHeight

	// The command block sits between the log header and the logs. Its height
	// depends on the selected tunnel and how far its line wraps, so measure it
	// rather than reserving a fixed number of rows — and cap it so a short
	// terminal still shows some logs instead of pushing the panel off screen.
	available := m.listHeight - lipgloss.Height(m.logHeader(logWidth))
	if available < 2 {
		available = 2
	}
	m.connBlock = m.connectionView(logWidth)
	connHeight := 0
	if m.connBlock != "" {
		connHeight = lipgloss.Height(m.connBlock)
	}

	// Trim the command first, since it is the tallest and least urgent part.
	m.commandBlock = m.commandView(logWidth)
	maxCommandHeight := available - 4 - connHeight // blank separator + 3 rows of logs
	if maxCommandHeight < 1 {
		maxCommandHeight = 1
	}
	if lines := strings.Split(m.commandBlock, "\n"); len(lines) > maxCommandHeight {
		m.commandBlock = strings.Join(append(lines[:maxCommandHeight-1], itemDimStyle.Render("  …")), "\n")
	}

	// Assemble everything above the logs and measure that, rather than adding
	// up its parts: the header, the command and the connection line all wrap,
	// and an arithmetic estimate drifts by a row. Anything past what the box
	// can hold is cut here — letting it overflow costs the panel its bottom
	// border, since lipgloss clips the excess.
	// Wrapped to the panel's real width before being measured: formatCommand
	// emits an over-long line whenever a single token (the --parameters JSON,
	// a long instance id) doesn't fit, and the box would re-wrap it into rows
	// this measurement never saw.
	m.logPrefix = lipgloss.NewStyle().Width(logWidth).Render(
		m.logHeader(logWidth) + "\n" + m.commandBlock + "\n" + m.connBlock)
	maxPrefix := m.listHeight - 2 // blank separator + one row of logs
	if maxPrefix < 1 {
		maxPrefix = 1
	}
	if lines := strings.Split(m.logPrefix, "\n"); len(lines) > maxPrefix {
		m.logPrefix = strings.Join(append(lines[:maxPrefix-1], itemDimStyle.Render("…")), "\n")
	}

	logViewportHeight := m.listHeight - lipgloss.Height(m.logPrefix) - 1
	if logViewportHeight < 1 {
		logViewportHeight = 1
	}

	if !m.ready {
		m.viewport = viewport.New(logWidth, logViewportHeight)
		m.ready = true
	} else {
		m.viewport.Width = logWidth
		m.viewport.Height = logViewportHeight
	}
	m.refreshViewportContent()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		return m, nil

	case tickMsg:
		if m.copyStatus != "" && time.Now().After(m.copyStatusUntil) {
			m.copyStatus = ""
			m.layout()
		}
		return m, tick()

	case tea.KeyMsg:
		switch m.mode {
		case modeCredentials:
			return m.updateCredentials(msg)
		case modeSSO:
			return m.updateSSO(msg)
		case modeAssignProfile:
			return m.updateAssignProfile(msg)
		case modeNewProfile:
			return m.updateNewProfile(msg)
		case modeTunnelForm:
			return m.updateTunnelForm(msg)
		case modeTunnelDelete:
			return m.updateTunnelDelete(msg)
		default:
			return m.updateList(msg)
		}

	case ssoRefreshMsg:
		m.ssoRefreshing = false
		if msg.err != nil {
			m.ssoRefreshErr = msg.err.Error()
			return m, nil
		}
		m.ssoRefreshErr = ""
		// A refresh can add, remove or reorder profiles, so remember which
		// one each cursor was pointing at (by name) and put it back there.
		// Keeping the raw index would silently move the selection onto a
		// different profile, or past the end of the list.
		ssoName, assignName := "", ""
		if m.ssoCursor < len(m.discoveredProfiles) {
			ssoName = m.discoveredProfiles[m.ssoCursor].Name
		}
		if m.assignCursor > 0 && m.assignCursor-1 < len(m.discoveredProfiles) {
			assignName = m.discoveredProfiles[m.assignCursor-1].Name
		}
		m.discoveredProfiles = msg.profiles
		m.ssoStatus = msg.status
		m.ssoExpiry = msg.expiry

		if i := profileIndex(msg.profiles, ssoName); i >= 0 {
			m.ssoCursor = i
		} else if m.ssoCursor >= len(msg.profiles) {
			m.ssoCursor = 0
		}
		// 0 is "ninguno" in the assignment picker, hence the +1 offset.
		if i := profileIndex(msg.profiles, assignName); i >= 0 {
			m.assignCursor = i + 1
		} else if m.assignCursor > len(msg.profiles) {
			m.assignCursor = 0
		}
		return m, nil

	case ssoLineMsg:
		trimmed := strings.TrimSpace(msg.line)
		switch {
		case strings.HasPrefix(trimmed, "https://"):
			m.ssoPendingURL = trimmed
			m.ssoOutput = append(m.ssoOutput, msg.line)
		case m.ssoPendingURL != "" && ssoDeviceCodePattern.MatchString(trimmed):
			m.ssoOutput = append(m.ssoOutput,
				msg.line,
				"",
				"Enlace completo (copiar y pegar en el navegador):",
				m.ssoPendingURL+"?user_code="+trimmed,
			)
			m.ssoPendingURL = ""
		default:
			m.ssoOutput = append(m.ssoOutput, msg.line)
		}
		if len(m.ssoOutput) > maxLogLines {
			m.ssoOutput = m.ssoOutput[len(m.ssoOutput)-maxLogLines:]
		}
		return m, waitSSOOutput(m.ssoLines, m.ssoDone, m.ssoActiveProfile, m.ssoAction)

	case ssoActionDoneMsg:
		m.ssoRunning = false
		m.ssoCancel = nil
		if msg.err != nil {
			m.ssoOutput = append(m.ssoOutput, "✗ "+msg.err.Error())
		} else {
			m.ssoOutput = append(m.ssoOutput, "✓ listo")
		}
		return m, refreshSSOCmd()

	case ssoImportMsg:
		m.ssoImporting = false
		if msg.err != nil {
			m.ssoOutput = append(m.ssoOutput, "✗ "+msg.err.Error())
			return m, refreshSSOCmd()
		}
		r := msg.result
		m.ssoOutput = append(m.ssoOutput, fmt.Sprintf(
			"✓ %d cuentas revisadas · %d perfiles nuevos · %d ya existían",
			r.Accounts, len(r.Written), len(r.Existing)))
		switch {
		case r.Accounts == 0:
			m.ssoOutput = append(m.ssoOutput, "Este portal no reporta ninguna cuenta asignada a tu usuario.")
		case len(r.Written) == 0 && len(r.Warnings) == 0:
			m.ssoOutput = append(m.ssoOutput, "Ya tenías un perfil por cada cuenta y rol de este portal.")
		}
		for i, w := range r.Warnings {
			if i == maxImportWarnings {
				m.ssoOutput = append(m.ssoOutput, fmt.Sprintf("  ! y %d avisos más", len(r.Warnings)-i))
				break
			}
			m.ssoOutput = append(m.ssoOutput, "  ! "+w)
		}
		m.ssoRefreshing = true
		return m, refreshSSOCmd()

	case npDeviceStartedMsg:
		if m.np == nil {
			return m, nil
		}
		if msg.err != nil {
			m.np.step = npStepForm
			m.np.formErr = msg.err.Error()
			return m, nil
		}
		m.np.client = msg.client
		m.np.device = msg.device
		m.np.step = npStepWaiting
		m.np.waitErr = ""
		ssologin.OpenBrowser(msg.device.VerificationURIComplete)
		return m, npPollTokenCmd(m.np.ctx, msg.region, msg.client, msg.device)

	case npTokenMsg:
		if m.np == nil {
			return m, nil
		}
		if msg.err != nil {
			m.np.waitErr = msg.err.Error()
			return m, nil
		}
		m.np.accessToken = msg.token
		m.np.step = npStepAccounts
		return m, npListAccountsCmd(m.np.ctx, m.np.region, msg.token)

	case npAccountsMsg:
		if m.np == nil {
			return m, nil
		}
		if msg.err != nil {
			m.np.err = msg.err.Error()
			return m, nil
		}
		m.np.err = ""
		m.np.accounts = msg.accounts
		m.np.accountCursor = 0
		m.np.accountsLoaded = true
		return m, nil

	case npRolesMsg:
		if m.np == nil {
			return m, nil
		}
		if msg.err != nil {
			m.np.err = msg.err.Error()
			return m, nil
		}
		m.np.err = ""
		m.np.roles = msg.roles
		m.np.roleCursor = 0
		m.np.rolesLoaded = true
		return m, nil

	case npWriteDoneMsg:
		if m.np != nil && m.np.cancel != nil {
			m.np.cancel()
		}
		m.np = nil
		if msg.err != nil {
			m.ssoOutput = []string{"✗ " + msg.err.Error()}
			m.mode = modeSSO
			return m, refreshSSOCmd()
		}
		// Profile written; log into it right away the standard way so the
		// AWS CLI caches a real session for it.
		m.mode = modeSSO
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		lines, done := ssologin.Login(ctx, msg.profileName)
		m.ssoCancel = cancel
		m.ssoRunning = true
		m.ssoAction = "login"
		m.ssoActiveProfile = msg.profileName
		m.ssoOutput = []string{
			"✓ perfil creado: " + msg.profileName,
			"Iniciando sesión (puede pedir aprobar el navegador una vez más)...",
		}
		m.ssoPendingURL = ""
		m.ssoLines, m.ssoDone = lines, done
		return m, tea.Batch(refreshSSOCmd(), waitSSOOutput(lines, done, msg.profileName, "login"))

	case credsCheckResultMsg:
		m.credsChecking = false
		if msg.ok {
			m.awsReady = true
			m.startServices(msg.targets)
			m.marked = map[string]bool{}
			return m, nil
		}
		m.credsHint = ""
		if msg.err != nil {
			line := strings.SplitN(msg.err.Error(), "\n", 2)[0]
			m.credsHint = line
		}
		m.pendingStart = msg.targets
		m.mode = modeCredentials
		m.credsError = ""
		var focusCmd tea.Cmd
		m.credsInput, focusCmd = newCredsTextarea(m.width)
		return m, focusCmd

	case credsValidateResultMsg:
		m.credsChecking = false
		if !msg.ok {
			if msg.err != nil {
				m.credsError = strings.SplitN(msg.err.Error(), "\n", 2)[0]
			} else {
				m.credsError = "credenciales inválidas"
			}
			return m, nil
		}
		m.mgr.SetAWSCredentials(msg.creds.Env())
		m.awsReady = true
		m.mode = modeList
		m.credsError = ""
		m.startServices(msg.targets)
		m.marked = map[string]bool{}
		return m, nil

	case procman.Event:
		i, ok := m.index[msg.ServiceID]
		if !ok {
			return m, waitForEvent(m.mgr.Events())
		}
		s := m.services[i]
		switch msg.Kind {
		case procman.EventLog:
			line := fmt.Sprintf("%s %s", labelStyle.Render("["+msg.Label+"]"), msg.Text)
			s.logs = append(s.logs, line)
			if len(s.logs) > maxLogLines {
				s.logs = s.logs[len(s.logs)-maxLogLines:]
			}
			if i == m.cursor {
				m.refreshViewportContent()
			}
		case procman.EventStatus:
			s.status = msg.Status
			if msg.Status == procman.StatusRunning {
				s.startedAt = time.Now()
			}
		}
		return m, waitForEvent(m.mgr.Events())
	}

	return m, nil
}

func (m Model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.showHelp {
		m.showHelp = false
		return m, nil
	}

	if m.showAllLogs {
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			m.mgr.StopAll()
			return m, tea.Quit
		case "v", "esc":
			m.showAllLogs = false
		}
		return m, nil
	}

	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		m.mgr.StopAll()
		return m, tea.Quit
	case "v":
		m.showAllLogs = true
		m.showHelp = false
		return m, nil
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.layout()
		}
		return m, nil
	case "down", "j":
		if m.cursor < len(m.services)-1 {
			m.cursor++
			m.layout()
		}
		return m, nil
	case "n":
		var focusCmd tea.Cmd
		m.tf, focusCmd = newTunnelForm(store.Tunnel{}, false, m.width)
		m.mode = modeTunnelForm
		return m, focusCmd
	case "e":
		sel := m.selected()
		if sel == nil {
			return m, nil
		}
		var focusCmd tea.Cmd
		m.tf, focusCmd = newTunnelForm(sel.row, true, m.width)
		m.mode = modeTunnelForm
		return m, focusCmd
	case "D":
		sel := m.selected()
		if sel == nil {
			return m, nil
		}
		m.deleteTarget = sel.cfg.ID
		m.mode = modeTunnelDelete
		return m, nil
	case " ":
		sel := m.selected()
		if sel == nil {
			return m, nil
		}
		id := sel.cfg.ID
		if m.marked[id] {
			delete(m.marked, id)
		} else {
			m.marked[id] = true
		}
		return m, nil
	case "c":
		m.pendingStart = nil
		m.mode = modeCredentials
		m.credsError = ""
		m.credsHint = ""
		var focusCmd tea.Cmd
		m.credsInput, focusCmd = newCredsTextarea(m.width)
		return m, focusCmd
	case "a":
		m.mode = modeSSO
		return m, refreshSSOCmd()
	case "p":
		sel := m.selected()
		if sel == nil {
			return m, nil
		}
		m.assignTarget = sel.cfg.ID
		m.assignCursor = 0
		if cur := m.tunnelProfile[m.assignTarget]; cur != "" {
			for i, p := range m.discoveredProfiles {
				if p.Name == cur {
					m.assignCursor = i + 1
					break
				}
			}
		}
		m.mode = modeAssignProfile
		return m, nil
	case "y":
		sel := m.selected()
		if sel == nil {
			return m, nil
		}
		// Copy the single-line form: it pastes cleanly into any shell,
		// unlike the wrapped, backslash-continued one on screen.
		if err := clipboard.Copy(m.commandFor(sel)); err != nil {
			m.copyStatus = "✗ no se pudo copiar: " + err.Error()
		} else {
			m.copyStatus = "✓ comando copiado al portapapeles"
		}
		m.copyStatusUntil = time.Now().Add(4 * time.Second)
		m.layout()
		return m, nil
	case "C":
		// Both panels share every screen row, so a mouse drag always picks up
		// the tunnel list too; this is the way to get the string out clean.
		sel := m.selected()
		if sel == nil {
			return m, nil
		}
		conn, column := connectionStringFor(sel.row)
		if conn == "" {
			m.copyStatus = "✗ sin cadena de conexión para " + column + " · 'e' para agregarla"
		} else if err := clipboard.Copy(conn); err != nil {
			m.copyStatus = "✗ no se pudo copiar: " + err.Error()
		} else {
			m.copyStatus = "✓ cadena de conexión copiada"
		}
		m.copyStatusUntil = time.Now().Add(4 * time.Second)
		m.layout()
		return m, nil
	case "?":
		m.showHelp = !m.showHelp
		return m, nil
	case "enter", "s":
		targets := m.targets()
		if m.awsReady || len(m.needsAmbientCredsCheck(targets)) == 0 {
			m.startServices(targets)
			m.marked = map[string]bool{}
			return m, nil
		}
		m.pendingStart = targets
		m.credsChecking = true
		return m, checkAmbientCredsCmd(targets)
	case "x", "d":
		m.stopServices(m.targets())
		m.marked = map[string]bool{}
		return m, nil
	default:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
}

func (m Model) updateCredentials(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.pendingStart = nil
		m.credsError = ""
		m.credsChecking = false
		return m, nil
	case "ctrl+d":
		if m.credsChecking {
			return m, nil
		}
		text := m.credsInput.Value()
		if strings.TrimSpace(text) == "" {
			m.credsError = "pega las credenciales primero"
			return m, nil
		}
		m.credsChecking = true
		m.credsError = ""
		return m, validatePastedCredsCmd(text, m.pendingStart)
	default:
		var cmd tea.Cmd
		m.credsInput, cmd = m.credsInput.Update(msg)
		return m, cmd
	}
}

func (m Model) updateSSO(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "a":
		m.mode = modeList
		return m, nil
	case "ctrl+c":
		if m.ssoCancel != nil {
			m.ssoCancel()
		}
		m.quitting = true
		m.mgr.StopAll()
		return m, tea.Quit
	case "up", "k":
		if m.ssoCursor > 0 {
			m.ssoCursor--
		}
		return m, nil
	case "down", "j":
		if m.ssoCursor < len(m.discoveredProfiles)-1 {
			m.ssoCursor++
		}
		return m, nil
	case "r":
		m.ssoRefreshing = true
		return m, refreshSSOCmd()
	case "i":
		if m.ssoRunning || m.ssoImporting {
			return m, nil
		}
		if len(m.discoveredProfiles) == 0 {
			m.ssoOutput = []string{"Todavía no hay ningún portal configurado: usa 'n' para crear el primer perfil."}
			return m, nil
		}
		// The cursor's profile only tells us which portal to walk; the token
		// comes from the CLI's own cache, which is per start URL, not per
		// profile — so any logged-in profile of that portal works.
		p := m.discoveredProfiles[m.ssoCursor]
		token, region, ok := ssologin.CachedToken(p)
		if !ok {
			m.ssoOutput = []string{
				"Para traer todas las cuentas, primero inicia sesión (enter) en un perfil de este portal.",
			}
			return m, nil
		}
		m.ssoImporting = true
		m.ssoOutput = []string{"Buscando todas las cuentas y roles de " + p.StartURL + "..."}
		return m, importAllProfilesCmd(region, p.StartURL, token)
	case "n":
		var focusCmd tea.Cmd
		m.np, focusCmd = newProfileWizardForm(m.width)
		m.mode = modeNewProfile
		return m, focusCmd
	case "enter", "l":
		if m.ssoRunning || len(m.discoveredProfiles) == 0 {
			return m, nil
		}
		p := m.discoveredProfiles[m.ssoCursor]
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		lines, done := ssologin.Login(ctx, p.Name)
		m.ssoCancel = cancel
		m.ssoRunning = true
		m.ssoAction = "login"
		m.ssoActiveProfile = p.Name
		m.ssoOutput = nil
		m.ssoPendingURL = ""
		m.ssoLines, m.ssoDone = lines, done
		return m, waitSSOOutput(lines, done, p.Name, "login")
	case "x", "L":
		if m.ssoRunning {
			return m, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		lines, done := ssologin.Logout(ctx)
		m.ssoCancel = cancel
		m.ssoRunning = true
		m.ssoAction = "logout"
		m.ssoActiveProfile = ""
		m.ssoOutput = nil
		m.ssoLines, m.ssoDone = lines, done
		return m, waitSSOOutput(lines, done, "", "logout")
	}
	return m, nil
}

func (m Model) updateAssignProfile(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	total := len(m.discoveredProfiles) + 1 // +1 for "ninguno"
	switch msg.String() {
	case "esc":
		m.mode = modeList
		return m, nil
	case "up", "k":
		if m.assignCursor > 0 {
			m.assignCursor--
		}
		return m, nil
	case "down", "j":
		if m.assignCursor < total-1 {
			m.assignCursor++
		}
		return m, nil
	case "r":
		m.ssoRefreshing = true
		return m, refreshSSOCmd()
	case "enter":
		profile := ""
		if m.assignCursor > 0 {
			profile = m.discoveredProfiles[m.assignCursor-1].Name
		}
		m.storeErr = ""
		if err := m.store.SetProfile(m.assignTarget, profile); err != nil {
			m.storeErr = "no se pudo guardar el perfil: " + err.Error()
		} else if err := m.reloadTunnels(); err != nil {
			m.storeErr = "no se pudo recargar: " + err.Error()
		}
		m.layout()
		m.mode = modeList
		return m, nil
	}
	return m, nil
}

func (m Model) updateNewProfile(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.np == nil {
		return m, nil
	}

	cancelWizard := func(m Model) (tea.Model, tea.Cmd) {
		if m.np.cancel != nil {
			m.np.cancel()
		}
		m.np = nil
		m.mode = modeSSO
		return m, refreshSSOCmd()
	}

	if msg.String() == "ctrl+c" {
		if m.np.cancel != nil {
			m.np.cancel()
		}
		m.quitting = true
		m.mgr.StopAll()
		return m, tea.Quit
	}

	switch m.np.step {
	case npStepForm:
		switch msg.String() {
		case "esc":
			m.np = nil
			m.mode = modeSSO
			return m, nil
		case "tab", "down", "shift+tab", "up":
			m.np.urlInput.Blur()
			m.np.regionInput.Blur()
			m.np.focus = (m.np.focus + 1) % 2 // only two fields, so either key just toggles
			var cmd tea.Cmd
			if m.np.focus == 0 {
				cmd = m.np.urlInput.Focus()
			} else {
				cmd = m.np.regionInput.Focus()
			}
			return m, cmd
		case "enter":
			if m.np.focus == 0 {
				m.np.urlInput.Blur()
				m.np.focus = 1
				return m, m.np.regionInput.Focus()
			}
			url := strings.TrimSpace(m.np.urlInput.Value())
			region := strings.TrimSpace(m.np.regionInput.Value())
			if !strings.HasPrefix(url, "https://") {
				m.np.formErr = "la URL debe empezar con https:// (ej. https://d-90671b694f.awsapps.com/start)"
				return m, nil
			}
			if region == "" {
				m.np.formErr = "escribe una región, ej. us-east-1"
				return m, nil
			}
			m.np.formErr = ""
			m.np.startURL = url
			m.np.region = region
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			m.np.ctx, m.np.cancel = ctx, cancel
			m.np.step = npStepWaiting
			m.np.waitErr = ""
			return m, npStartDeviceAuthCmd(ctx, region, url)
		default:
			var cmd tea.Cmd
			if m.np.focus == 0 {
				m.np.urlInput, cmd = m.np.urlInput.Update(msg)
			} else {
				m.np.regionInput, cmd = m.np.regionInput.Update(msg)
			}
			return m, cmd
		}

	case npStepWaiting:
		if msg.String() == "esc" {
			return cancelWizard(m)
		}
		return m, nil

	case npStepAccounts:
		switch msg.String() {
		case "esc":
			return cancelWizard(m)
		case "up", "k":
			if m.np.accountCursor > 0 {
				m.np.accountCursor--
			}
			return m, nil
		case "down", "j":
			if m.np.accountCursor < len(m.np.accounts)-1 {
				m.np.accountCursor++
			}
			return m, nil
		case "i":
			if !m.np.accountsLoaded || len(m.np.accounts) == 0 {
				return m, nil
			}
			// The wizard's job is done: it got us a token for the portal, and
			// the import needs nothing else, so hand off to the SSO panel.
			region, startURL, token := m.np.region, m.np.startURL, m.np.accessToken
			if m.np.cancel != nil {
				m.np.cancel()
			}
			m.np = nil
			m.mode = modeSSO
			m.ssoImporting = true
			m.ssoOutput = []string{"Creando un perfil por cada cuenta y rol de " + startURL + "..."}
			return m, importAllProfilesCmd(region, startURL, token)
		case "enter":
			if len(m.np.accounts) == 0 {
				return m, nil
			}
			m.np.account = m.np.accounts[m.np.accountCursor]
			m.np.step = npStepRoles
			m.np.roles = nil
			m.np.err = ""
			return m, npListRolesCmd(m.np.ctx, m.np.region, m.np.accessToken, m.np.account.AccountID)
		}
		return m, nil

	case npStepRoles:
		switch msg.String() {
		case "esc":
			return cancelWizard(m)
		case "up", "k":
			if m.np.roleCursor > 0 {
				m.np.roleCursor--
			}
			return m, nil
		case "down", "j":
			if m.np.roleCursor < len(m.np.roles)-1 {
				m.np.roleCursor++
			}
			return m, nil
		case "enter":
			if len(m.np.roles) == 0 {
				return m, nil
			}
			role := m.np.roles[m.np.roleCursor]
			name := ssologin.ProfileName(m.np.account.AccountID, role.RoleName)
			return m, npWriteProfileCmd(name, m.np.startURL, m.np.region, m.np.account.AccountID, role.RoleName)
		}
		return m, nil
	}

	return m, nil
}

func (m Model) View() string {
	if m.quitting {
		return "Deteniendo servicios...\n"
	}
	if !m.ready {
		return "Cargando..."
	}
	if m.mode == modeList && m.showHelp {
		return m.viewHelp()
	}
	if m.mode == modeList && m.showAllLogs {
		return m.viewAllLogs()
	}
	switch m.mode {
	case modeCredentials:
		return m.viewCredentials()
	case modeSSO:
		return m.viewSSO()
	case modeAssignProfile:
		return m.viewAssignProfile()
	case modeNewProfile:
		return m.viewNewProfile()
	case modeTunnelForm:
		return m.viewTunnelForm()
	case modeTunnelDelete:
		return m.viewTunnelDelete()
	default:
		return m.viewList()
	}
}

func (m Model) viewList() string {
	header := titleStyle.Render("scriptstui — gestor de túneles")

	projectWidth := m.projectFieldWidth()

	var list strings.Builder
	if len(m.services) == 0 {
		list.WriteString(itemDimStyle.Render("  No hay túneles todavía."))
		list.WriteString("\n\n")
		list.WriteString("  " + helpEntry("n", "crear el primero"))
		list.WriteString("\n")
	}
	for i, s := range m.services {
		cursorMark := "  "
		titleStyleFor := itemStyle
		if !s.row.Enabled {
			titleStyleFor = itemDimStyle
		}
		titleRendered := titleStyleFor.Render(padTrunc(s.cfg.Title, titleFieldWidth))
		if i == m.cursor {
			cursorMark = cursorStyle.Render("▶ ")
			titleRendered = selectedTitle.Render(padTrunc(s.cfg.Title, titleFieldWidth))
		}
		selMark := " "
		if m.marked[s.cfg.ID] {
			selMark = checkedStyle.Render("✓")
		}

		projectRendered := ""
		if projectWidth > 0 {
			projectRendered = itemDimStyle.Render(padTrunc(s.row.Proyecto, projectWidth)) + " "
		}

		portText := ""
		if port := s.cfg.LocalPort(); port != "" {
			portText = "[" + port + "]"
		}
		portRendered := portStyle.Render(padTrunc(portText, portFieldWidth))

		// A disabled tunnel can't be started, so it reports that instead of a
		// run status that would never change.
		statusText := s.status.String()
		statusFg := statusColor(s.status)
		dot := statusDot(s.status)
		if !s.row.Enabled {
			statusText = "desactivado"
			statusFg = colorStopped
			dot = lipgloss.NewStyle().Foreground(colorStopped).Render("○")
			portRendered = itemDimStyle.Render(padTrunc(portText, portFieldWidth))
		} else if s.status == procman.StatusRunning && !s.startedAt.IsZero() {
			statusText += " · " + time.Since(s.startedAt).Round(time.Second).String()
		}
		statusRendered := lipgloss.NewStyle().Foreground(statusFg).Render(statusText)

		line := cursorMark + selMark + " " + dot + " " + projectRendered + titleRendered + "  " + portRendered + " " + statusRendered
		list.WriteString(line)
		list.WriteString("\n")
	}
	listPanel := listBoxStyle.Width(m.listWidth()).Height(m.listHeight).Render(list.String())

	logPanel := logBoxStyle.
		Width(m.logPanelWidth).
		Height(m.listHeight).
		MaxHeight(m.listHeight + 2). // + top and bottom border
		Render(m.logPrefix + "\n\n" + m.viewport.View())

	body := lipgloss.JoinHorizontal(lipgloss.Top, listPanel, logPanel)

	help := helpEntry("enter/s", "iniciar") + "  " +
		helpEntry("x", "detener") + "  " +
		helpEntry("n", "nuevo") + "  " +
		helpEntry("e", "editar") + "  " +
		helpEntry("D", "eliminar") + "  " +
		helpEntry("y", "comando") + "  " +
		helpEntry("C", "conexión") + "  " +
		helpEntry("a", "cuentas AWS") + "  " +
		helpEntry("?", "ayuda") + "  " +
		helpEntry("q", "salir")

	return appStyle.Render(lipgloss.JoinVertical(lipgloss.Left, header, body, help))
}

// viewAllLogs shows every tunnel's log tail stacked full-width, for
// monitoring all of them at once instead of one at a time.
func (m Model) viewAllLogs() string {
	header := titleStyle.Render("scriptstui — logs de todos los túneles")

	panelWidth := m.width - 6
	if panelWidth < 20 {
		panelWidth = 20
	}
	bodyHeight := m.listHeight + 2 // total vertical budget the single-panel layout had
	perPanel := bodyHeight / len(m.services)
	contentHeight := perPanel - 3 // border(2) + own header line(1)
	if contentHeight < 1 {
		contentHeight = 1
	}

	var panels []string
	for _, s := range m.services {
		profileInfo := "sin perfil"
		if p := m.tunnelProfile[s.cfg.ID]; p != "" {
			profileInfo = "perfil: " + p
		}
		head := logHeaderStyle.Render(statusDot(s.status)+" "+s.cfg.Title) + "  " +
			itemDimStyle.Render("("+s.status.String()+" · "+profileInfo+")")

		lines := s.logs
		if len(lines) > contentHeight {
			lines = lines[len(lines)-contentHeight:]
		}
		panels = append(panels, logBoxStyle.Width(panelWidth).Height(contentHeight).Render(head+"\n"+strings.Join(lines, "\n")))
	}
	body := lipgloss.JoinVertical(lipgloss.Left, panels...)

	help := helpEntry("v", "volver a un túnel") + "  " + helpEntry("q", "salir")

	return appStyle.Render(lipgloss.JoinVertical(lipgloss.Left, header, body, help))
}

func (m Model) viewHelp() string {
	var b strings.Builder
	b.WriteString(modalTitleStyle.Render("Atajos de teclado"))
	b.WriteString("\n\n")

	b.WriteString(itemDimStyle.Render("Túneles"))
	b.WriteString("\n")
	b.WriteString(helpEntry("↑/↓", "navegar") + "\n")
	b.WriteString(helpEntry("enter/s", "iniciar el resaltado (o los marcados con espacio)") + "\n")
	b.WriteString(helpEntry("x", "detener el resaltado (o los marcados)") + "\n")
	b.WriteString(helpEntry("espacio", "marcar varios túneles para iniciarlos/detenerlos juntos") + "\n")
	b.WriteString(helpEntry("p", "elegir el perfil AWS del túnel resaltado") + "\n")
	b.WriteString(helpEntry("y", "copiar al portapapeles el comando aws ssm del túnel resaltado") + "\n")
	b.WriteString(helpEntry("C", "copiar la cadena de conexión del túnel resaltado (mac o windows según el SO)") + "\n")
	b.WriteString(helpEntry("v", "ver los logs de los 3 túneles a la vez") + "\n")
	b.WriteString(helpEntry("pgup/pgdn", "scroll de los logs") + "\n")

	b.WriteString("\n")
	b.WriteString(itemDimStyle.Render("Administrar túneles (se guardan en " + store.FileName + ")"))
	b.WriteString("\n")
	b.WriteString(helpEntry("n", "crear un túnel nuevo") + "\n")
	b.WriteString(helpEntry("e", "editar el túnel resaltado") + "\n")
	b.WriteString(helpEntry("D", "eliminar el túnel resaltado (pide confirmación)") + "\n")
	b.WriteString(helpEntry("ctrl+s", "guardar, dentro del formulario") + "\n")

	b.WriteString("\n")
	b.WriteString(itemDimStyle.Render("Credenciales AWS"))
	b.WriteString("\n")
	b.WriteString(helpEntry("a", "abrir el panel de cuentas AWS SSO (recomendado)") + "\n")
	b.WriteString(helpEntry("c", "pegar credenciales manualmente (respaldo)") + "\n")
	b.WriteString(helpEntry("r", "recargar los perfiles de ~/.aws/config (en 'a' y en 'p')") + "\n")
	b.WriteString(helpEntry("i", "en 'a': crear un perfil por cada cuenta y rol del portal") + "\n")

	b.WriteString("\n")
	b.WriteString(helpEntry("q", "salir") + "  " + helpEntry("(cualquier tecla)", "cerrar esta ayuda"))

	box := modalBoxStyle.Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m Model) viewCredentials() string {
	var b strings.Builder
	b.WriteString(modalTitleStyle.Render("🔐 Credenciales AWS"))
	b.WriteString("\n\n")
	b.WriteString("No se detectaron credenciales válidas. Pega el bloque que genera AWS\n")
	b.WriteString("(access key id, secret access key y session token) y confirma.\n")
	b.WriteString("No se guarda en disco, solo vive mientras esta ventana esté abierta.\n\n")
	b.WriteString(m.credsInput.View())
	b.WriteString("\n\n")

	switch {
	case m.credsChecking:
		b.WriteString(modalHintStyle.Render("Validando con AWS..."))
	case m.credsError != "":
		b.WriteString(modalErrStyle.Render("✗ " + m.credsError))
	case m.credsHint != "":
		b.WriteString(modalHintStyle.Render(m.credsHint))
	default:
		b.WriteString(modalHintStyle.Render(" "))
	}
	b.WriteString("\n\n")
	b.WriteString(helpEntry("ctrl+d", "confirmar") + "  " + helpEntry("esc", "cancelar"))

	box := modalBoxStyle.Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m Model) viewAssignProfile() string {
	sel := m.services[m.index[m.assignTarget]]

	var b strings.Builder
	b.WriteString(modalTitleStyle.Render("Perfil AWS para: " + sel.cfg.Title))
	b.WriteString("\n\n")

	renderRow := func(idx int, title, detail string) {
		marker := "  "
		titleRendered := itemStyle.Render(title)
		if idx == m.assignCursor {
			marker = cursorStyle.Render("▶ ")
			titleRendered = selectedTitle.Render(title)
		}
		b.WriteString(marker + titleRendered + "\n")
		if detail != "" {
			b.WriteString(itemDimStyle.Render("    "+detail) + "\n")
		}
	}

	renderRow(0, "ninguno", "usar credenciales pegadas ('c') o las ambientales")
	if len(m.discoveredProfiles) == 0 {
		b.WriteString("\n")
		b.WriteString(modalHintStyle.Render("No hay perfiles SSO en ~/.aws/config.\nPresiona 'r' para recargarlos, o abre el panel de cuentas ('a') y presiona 'n' para crear uno."))
		b.WriteString("\n")
	}
	// Two rows per profile inside a centered modal: title, blank, "ninguno",
	// blank, status, help and the box chrome take the rest of the height.
	maxProfiles := (m.height - 14) / 2
	if maxProfiles < 2 {
		maxProfiles = 2
	}
	cursor := m.assignCursor - 1 // "ninguno" is row 0, profiles start at 1
	if cursor < 0 {
		cursor = 0
	}
	start, end := visibleWindow(cursor, len(m.discoveredProfiles), maxProfiles)
	if start > 0 {
		b.WriteString(itemDimStyle.Render(fmt.Sprintf("  ↑ %d perfiles más arriba", start)) + "\n")
	}
	for i := start; i < end; i++ {
		p := m.discoveredProfiles[i]
		renderRow(i+1, p.Name, "cuenta "+p.AccountID+" · rol "+p.RoleName)
	}
	if end < len(m.discoveredProfiles) {
		b.WriteString(itemDimStyle.Render(fmt.Sprintf("  ↓ %d perfiles más abajo", len(m.discoveredProfiles)-end)) + "\n")
	}

	b.WriteString("\n")
	if m.ssoRefreshing {
		b.WriteString(modalHintStyle.Render("recargando perfiles...") + "\n")
	} else if m.ssoRefreshErr != "" {
		b.WriteString(modalErrStyle.Render("✗ "+m.ssoRefreshErr) + "\n")
	}
	b.WriteString(helpEntry("↑/↓", "navegar") + "  " + helpEntry("enter", "elegir") + "  " +
		helpEntry("r", "recargar perfiles") + "  " + helpEntry("esc", "cancelar"))

	box := modalBoxStyle.Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// profileIndex returns where name sits in profiles, or -1 when it is absent
// (including for the empty name), so callers can re-anchor a cursor after the
// list is reloaded.
func profileIndex(profiles []ssologin.Profile, name string) int {
	if name == "" {
		return -1
	}
	for i, p := range profiles {
		if p.Name == name {
			return i
		}
	}
	return -1
}

// ssoExpiryLabel renders how much longer a logged-in SSO session has left,
// e.g. " · vence en 3h 42m", based on the AWS CLI's own cached token
// expiry. Empty if we have no cached expiry to show.
func ssoExpiryLabel(expiresAt time.Time) string {
	if expiresAt.IsZero() {
		return ""
	}
	remaining := time.Until(expiresAt)
	if remaining <= 0 {
		return " · vence en breve"
	}
	remaining = remaining.Round(time.Minute)
	h := remaining / time.Hour
	mins := (remaining % time.Hour) / time.Minute
	if h > 0 {
		return fmt.Sprintf(" · vence en %dh %dm", h, mins)
	}
	return fmt.Sprintf(" · vence en %dm", mins)
}

func (m Model) viewSSO() string {
	header := titleStyle.Render("scriptstui — cuentas AWS SSO")

	var list strings.Builder
	if len(m.discoveredProfiles) == 0 {
		list.WriteString(itemDimStyle.Render("No hay perfiles SSO en ~/.aws/config todavía.\n"))
		list.WriteString(itemDimStyle.Render("Presiona 'n' para crear uno (aws configure sso).\n"))
		list.WriteString(itemDimStyle.Render("Desde ahí puedes traer de una vez todas tus cuentas del portal.\n"))
		if m.ssoRefreshErr != "" {
			list.WriteString(modalErrStyle.Render("✗ " + m.ssoRefreshErr))
		}
	}
	// Two rows per profile, plus one row for each "N más" marker.
	maxProfiles := (m.listHeight - 2) / 2
	if maxProfiles < 1 {
		maxProfiles = 1
	}
	start, end := visibleWindow(m.ssoCursor, len(m.discoveredProfiles), maxProfiles)
	if start > 0 {
		list.WriteString(itemDimStyle.Render(fmt.Sprintf("  ↑ %d perfiles más arriba", start)) + "\n")
	}
	for i := start; i < end; i++ {
		p := m.discoveredProfiles[i]
		marker := "  "
		title := padTrunc(p.Name, titleFieldWidth)
		titleRendered := itemStyle.Render(title)
		if i == m.ssoCursor {
			marker = cursorStyle.Render("▶ ")
			titleRendered = selectedTitle.Render(title)
		}

		badge := itemDimStyle.Render("✗ no logueado")
		if m.ssoStatus[p.Name] {
			badge = checkedStyle.Render("✓ logueado" + ssoExpiryLabel(m.ssoExpiry[p.Name]))
		}

		list.WriteString(marker + titleRendered + " " + badge + "\n")
		detail := "    cuenta " + p.AccountID + " · rol " + p.RoleName
		list.WriteString(itemDimStyle.Render(detail) + "\n")
	}
	if end < len(m.discoveredProfiles) {
		list.WriteString(itemDimStyle.Render(fmt.Sprintf("  ↓ %d perfiles más abajo", len(m.discoveredProfiles)-end)) + "\n")
	}
	listPanel := listBoxStyle.Width(baseListWidth).Height(m.listHeight).Render(list.String())

	logWidth := m.width - baseListWidth - 6
	if logWidth < 20 {
		logWidth = 20
	}
	outHeader := logHeaderStyle.Render("Salida de aws sso")
	outLines := m.ssoOutput
	if room := m.listHeight - 3; room > 0 && len(outLines) > room {
		outLines = outLines[len(outLines)-room:]
	}
	outBody := strings.Join(outLines, "\n")
	if m.ssoImporting {
		if outBody != "" {
			outBody += "\n"
		}
		outBody += modalHintStyle.Render("importando cuentas del portal...")
	}
	if m.ssoRefreshing {
		if outBody != "" {
			outBody += "\n"
		}
		outBody += modalHintStyle.Render("recargando perfiles...")
	}
	if m.ssoRunning {
		if outBody != "" {
			outBody += "\n"
		}
		outBody += modalHintStyle.Render("ejecutando " + m.ssoAction + "...")
	}
	logPanel := logBoxStyle.Width(logWidth).Height(m.listHeight).Render(outHeader + "\n" + outBody)

	body := lipgloss.JoinHorizontal(lipgloss.Top, listPanel, logPanel)

	help := helpEntry("↑/↓", "navegar") + "  " +
		helpEntry("enter/l", "iniciar sesión") + "  " +
		helpEntry("i", "traer todas las cuentas") + "  " +
		helpEntry("n", "nuevo perfil") + "  " +
		helpEntry("r", "refrescar") + "  " +
		helpEntry("x", "cerrar sesión (todas)") + "  " +
		helpEntry("esc/a", "volver")

	return appStyle.Render(lipgloss.JoinVertical(lipgloss.Left, header, body, help))
}

func (m Model) viewNewProfile() string {
	np := m.np
	var b strings.Builder
	b.WriteString(modalTitleStyle.Render("➕ Nueva cuenta AWS SSO"))
	b.WriteString("\n\n")

	switch np.step {
	case npStepForm:
		b.WriteString("Los mismos datos que verías en 'aws configure sso':\n\n")
		b.WriteString(np.urlInput.View())
		b.WriteString("\n")
		b.WriteString(np.regionInput.View())
		b.WriteString("\n\n")
		if np.formErr != "" {
			b.WriteString(modalErrStyle.Render("✗ " + np.formErr))
		} else {
			b.WriteString(modalHintStyle.Render("Ejemplo: URL https://d-90671b694f.awsapps.com/start · Región us-east-1"))
		}
		b.WriteString("\n\n")
		b.WriteString(helpEntry("tab", "cambiar campo") + "  " + helpEntry("enter", "siguiente/continuar") + "  " + helpEntry("esc", "cancelar"))

	case npStepWaiting:
		b.WriteString("Abrí tu navegador para que apruebes el acceso.\n")
		b.WriteString("Si no se abrió solo, entra a esta URL:\n\n")
		b.WriteString(itemStyle.Render(np.device.VerificationURIComplete))
		b.WriteString("\n\n")
		b.WriteString("Código: " + checkedStyle.Render(np.device.UserCode))
		b.WriteString("\n\n")
		if np.waitErr != "" {
			b.WriteString(modalErrStyle.Render("✗ " + np.waitErr))
		} else {
			b.WriteString(modalHintStyle.Render("Esperando que apruebes en el navegador..."))
		}
		b.WriteString("\n\n")
		b.WriteString(helpEntry("esc", "cancelar"))

	case npStepAccounts:
		b.WriteString("Elige la cuenta, o presiona 'i' para traer todas:\n\n")
		switch {
		case np.err != "":
			b.WriteString(modalErrStyle.Render("✗ " + np.err))
		case !np.accountsLoaded:
			b.WriteString(modalHintStyle.Render("Cargando cuentas..."))
		case len(np.accounts) == 0:
			b.WriteString(modalHintStyle.Render("No tienes cuentas asignadas en este portal."))
		}
		maxAccounts := m.height - 15
		if maxAccounts < 3 {
			maxAccounts = 3
		}
		start, end := visibleWindow(np.accountCursor, len(np.accounts), maxAccounts)
		if start > 0 {
			b.WriteString(itemDimStyle.Render(fmt.Sprintf("  ↑ %d cuentas más arriba", start)) + "\n")
		}
		for i := start; i < end; i++ {
			a := np.accounts[i]
			label := a.AccountName + " (" + a.AccountID + ")"
			marker, title := "  ", itemStyle.Render(label)
			if i == np.accountCursor {
				marker, title = cursorStyle.Render("▶ "), selectedTitle.Render(label)
			}
			b.WriteString(marker + title + "\n")
		}
		if end < len(np.accounts) {
			b.WriteString(itemDimStyle.Render(fmt.Sprintf("  ↓ %d cuentas más abajo", len(np.accounts)-end)) + "\n")
		}
		b.WriteString("\n")
		b.WriteString(helpEntry("↑/↓", "navegar") + "  " + helpEntry("enter", "elegir") + "  " +
			helpEntry("i", "traer todas") + "  " + helpEntry("esc", "cancelar"))

	case npStepRoles:
		b.WriteString("Elige el rol en " + np.account.AccountName + ":\n\n")
		switch {
		case np.err != "":
			b.WriteString(modalErrStyle.Render("✗ " + np.err))
		case !np.rolesLoaded:
			b.WriteString(modalHintStyle.Render("Cargando roles..."))
		case len(np.roles) == 0:
			b.WriteString(modalHintStyle.Render("No tienes roles asignados en esta cuenta."))
		}
		for i, r := range np.roles {
			marker, title := "  ", itemStyle.Render(r.RoleName)
			if i == np.roleCursor {
				marker, title = cursorStyle.Render("▶ "), selectedTitle.Render(r.RoleName)
			}
			b.WriteString(marker + title + "\n")
		}
		b.WriteString("\n")
		b.WriteString(helpEntry("↑/↓", "navegar") + "  " + helpEntry("enter", "elegir") + "  " + helpEntry("esc", "cancelar"))
	}

	box := modalBoxStyle.Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// --- Tunnel CRUD form: one modal shared by "new" and "edit" ---------------

// The fields of the tunnel form, in tab order. tfEnabled is a toggle rather
// than a text input, so it has no entry in tunnelForm.inputs.
const (
	tfID = iota
	tfProyecto
	tfTitle
	tfTarget
	tfHost
	tfRemotePort
	tfLocalPort
	tfRegion
	tfDocument
	tfConnMac
	tfConnWin
	tfInputCount // number of text inputs
	tfEnabled    = tfInputCount
	tfFieldCount = tfInputCount + 1
)

var tunnelFormLabels = [tfInputCount]string{
	tfID:         "id",
	tfProyecto:   "proyecto",
	tfTitle:      "nombre",
	tfTarget:     "target (EC2)",
	tfHost:       "host remoto",
	tfRemotePort: "puerto remoto",
	tfLocalPort:  "puerto local",
	tfRegion:     "región",
	tfDocument:   "documento SSM",
	tfConnMac:    "conn. string mac",
	tfConnWin:    "conn. string win",
}

var tunnelFormPlaceholders = [tfInputCount]string{
	tfID:         "db-balu",
	tfProyecto:   "BALU",
	tfTitle:      "Túnel DB BALU PROD",
	tfTarget:     "i-074e88b2ee9d1d67f",
	tfHost:       "mi-db.cluster-abc.us-east-1.rds.amazonaws.com",
	tfRemotePort: "5432",
	tfLocalPort:  "5437",
	tfRegion:     "us-east-1",
	tfDocument:   config.SSMPortForwardDocument,
	tfConnMac:    "psql postgres://usuario@localhost:5437/mi_db",
	tfConnWin:    "psql postgresql://usuario@127.0.0.1:5437/mi_db",
}

type tunnelForm struct {
	inputs [tfInputCount]textinput.Model
	focus  int
	// editing is false for a new tunnel. When true the id is fixed: it is the
	// key procman and the marks are stored under, so renaming it would have to
	// be a delete plus an insert rather than an update.
	editing bool
	enabled bool
	err     string
}

// labelPrompt pads every label to the same width so the input boxes line up.
// The padding counts runes, not bytes: "región" is one rune shorter than its
// byte length and would otherwise sit a column off from the rest.
func labelPrompt(label string) string {
	const w = 18
	n := utf8.RuneCountInString(label)
	if n >= w {
		return label + ": "
	}
	return label + ":" + strings.Repeat(" ", w-n) + " "
}

// newTunnelForm builds the form, prefilled from row when editing.
func newTunnelForm(row store.Tunnel, editing bool, width int) (*tunnelForm, tea.Cmd) {
	w := width - 34
	if w > 60 {
		w = 60
	}
	if w < 24 {
		w = 24
	}

	f := &tunnelForm{editing: editing, enabled: true}
	values := [tfInputCount]string{}
	if editing {
		f.enabled = row.Enabled
		values = [tfInputCount]string{
			tfID:         row.ID,
			tfProyecto:   row.Proyecto,
			tfTitle:      row.Title,
			tfTarget:     row.Target,
			tfHost:       row.Host,
			tfRemotePort: strconv.Itoa(row.RemotePort),
			tfLocalPort:  strconv.Itoa(row.LocalPort),
			tfRegion:     row.Region,
			tfDocument:   row.DocumentName,
			tfConnMac:    row.ConnStringMac,
			tfConnWin:    row.ConnStringWin,
		}
	} else {
		// Sensible defaults so a new tunnel only needs the parts that differ.
		values[tfRegion] = "us-east-1"
		values[tfDocument] = config.SSMPortForwardDocument
	}

	for i := 0; i < tfInputCount; i++ {
		in := textinput.New()
		in.Prompt = labelPrompt(tunnelFormLabels[i])
		in.Placeholder = tunnelFormPlaceholders[i]
		in.CharLimit = 300
		in.Width = w
		in.SetValue(values[i])
		f.inputs[i] = in
	}

	f.focus = f.firstFocusable()
	return f, f.inputs[f.focus].Focus()
}

// firstFocusable skips the id when editing, since it can't be changed there.
func (f *tunnelForm) firstFocusable() int {
	if f.editing {
		return tfProyecto
	}
	return tfID
}

// focusable reports whether a field can take the cursor.
func (f *tunnelForm) focusable(i int) bool {
	return !(f.editing && i == tfID)
}

// move walks the focus by delta, wrapping around and skipping fixed fields.
func (f *tunnelForm) move(delta int) tea.Cmd {
	for i := 0; i < tfInputCount; i++ {
		f.inputs[i].Blur()
	}
	next := f.focus
	for {
		next = (next + delta + tfFieldCount) % tfFieldCount
		if f.focusable(next) {
			break
		}
	}
	f.focus = next
	if f.focus < tfInputCount {
		return f.inputs[f.focus].Focus()
	}
	return nil
}

// tunnel assembles the row the form describes, reporting the first field the
// user has to fix.
func (f *tunnelForm) tunnel(sortOrder int) (store.Tunnel, error) {
	value := func(i int) string { return strings.TrimSpace(f.inputs[i].Value()) }

	remote, err := strconv.Atoi(value(tfRemotePort))
	if err != nil {
		return store.Tunnel{}, errors.New("el puerto remoto debe ser un número (ej. 5432)")
	}
	local, err := strconv.Atoi(value(tfLocalPort))
	if err != nil {
		return store.Tunnel{}, errors.New("el puerto local debe ser un número (ej. 5437)")
	}

	t := store.Tunnel{
		ID:            value(tfID),
		Proyecto:      value(tfProyecto),
		Title:         value(tfTitle),
		Target:        value(tfTarget),
		Host:          value(tfHost),
		RemotePort:    remote,
		LocalPort:     local,
		Region:        value(tfRegion),
		DocumentName:  value(tfDocument),
		ConnStringMac: value(tfConnMac),
		ConnStringWin: value(tfConnWin),
		Enabled:       f.enabled,
		SortOrder:     sortOrder,
	}
	return t, t.Validate()
}

func (m Model) updateTunnelForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.tf == nil {
		m.mode = modeList
		return m, nil
	}

	save := func(m Model) (tea.Model, tea.Cmd) {
		// A new tunnel goes to the end of the list; an edited one keeps the
		// place (and the profile) it already had.
		sortOrder, profile := 0, ""
		if m.tf.editing {
			if i, ok := m.index[strings.TrimSpace(m.tf.inputs[tfID].Value())]; ok {
				sortOrder = m.services[i].row.SortOrder
				profile = m.services[i].row.Profile
			}
		}
		row, err := m.tf.tunnel(sortOrder)
		if err != nil {
			m.tf.err = err.Error()
			return m, nil
		}
		row.Profile = profile

		if m.tf.editing {
			err = m.store.Update(row)
		} else {
			err = m.store.Create(row)
		}
		if err != nil {
			m.tf.err = err.Error()
			return m, nil
		}
		if err := m.reloadTunnels(); err != nil {
			m.tf.err = err.Error()
			return m, nil
		}
		// Land the cursor on the tunnel that was just saved.
		if i, ok := m.index[row.ID]; ok {
			m.cursor = i
		}
		m.tf = nil
		m.mode = modeList
		m.storeErr = ""
		m.layout()
		return m, nil
	}

	switch msg.String() {
	case "esc":
		m.tf = nil
		m.mode = modeList
		return m, nil
	case "ctrl+c":
		m.quitting = true
		m.mgr.StopAll()
		return m, tea.Quit
	case "tab", "down":
		return m, m.tf.move(1)
	case "shift+tab", "up":
		return m, m.tf.move(-1)
	case "ctrl+s":
		return save(m)
	case " ":
		if m.tf.focus == tfEnabled {
			m.tf.enabled = !m.tf.enabled
			return m, nil
		}
	case "enter":
		// Enter walks the form and saves from the last field, matching the
		// SSO wizard; ctrl+s saves from anywhere.
		if m.tf.focus == tfEnabled {
			return save(m)
		}
		return m, m.tf.move(1)
	}

	if m.tf.focus < tfInputCount {
		var cmd tea.Cmd
		m.tf.inputs[m.tf.focus], cmd = m.tf.inputs[m.tf.focus].Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) updateTunnelDelete(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "s", "enter":
		if err := m.store.Delete(m.deleteTarget); err != nil {
			m.storeErr = "no se pudo eliminar: " + err.Error()
		} else if err := m.reloadTunnels(); err != nil {
			m.storeErr = "no se pudo recargar: " + err.Error()
		} else {
			m.storeErr = ""
		}
		m.deleteTarget = ""
		m.mode = modeList
		m.layout()
		return m, nil
	case "n", "esc", "q":
		m.deleteTarget = ""
		m.mode = modeList
		return m, nil
	}
	return m, nil
}

func (m Model) viewTunnelForm() string {
	f := m.tf
	title := "➕ Nuevo túnel"
	if f.editing {
		title = "✎ Editar túnel"
	}

	var b strings.Builder
	b.WriteString(modalTitleStyle.Render(title))
	b.WriteString("\n\n")

	for i := 0; i < tfInputCount; i++ {
		if f.editing && i == tfID {
			// Shown for context, but fixed: it keys the row, the marks and
			// the running process.
			b.WriteString("  " + itemDimStyle.Render(labelPrompt(tunnelFormLabels[i])+f.inputs[i].Value()+"  (no editable)"))
			b.WriteString("\n")
			continue
		}
		b.WriteString("  " + f.inputs[i].View())
		b.WriteString("\n")
	}

	check := "[ ]"
	if f.enabled {
		check = "[x]"
	}
	enabledLine := labelPrompt("activo") + check + " se muestra en la lista y reserva su puerto"
	if f.focus == tfEnabled {
		b.WriteString(cursorStyle.Render("▶ ") + selectedTitle.Render(enabledLine))
	} else {
		b.WriteString("  " + itemStyle.Render(enabledLine))
	}
	b.WriteString("\n\n")

	if f.err != "" {
		b.WriteString(modalErrStyle.Render("✗ " + f.err))
	} else {
		b.WriteString(modalHintStyle.Render("El comando aws ssm se arma solo con estos datos."))
	}
	b.WriteString("\n\n")
	b.WriteString(helpEntry("tab/↑↓", "cambiar campo") + "  " +
		helpEntry("espacio", "activar/desactivar") + "  " +
		helpEntry("ctrl+s", "guardar") + "  " +
		helpEntry("esc", "cancelar"))

	box := modalBoxStyle.Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m Model) viewTunnelDelete() string {
	var b strings.Builder
	b.WriteString(modalTitleStyle.Render("Eliminar túnel"))
	b.WriteString("\n\n")

	title := m.deleteTarget
	if i, ok := m.index[m.deleteTarget]; ok {
		title = m.services[i].cfg.Title
	}
	b.WriteString("Se va a borrar de la base de datos:\n\n")
	b.WriteString("  " + selectedTitle.Render(title) + "\n")
	b.WriteString("  " + itemDimStyle.Render("id: "+m.deleteTarget) + "\n\n")
	b.WriteString(modalHintStyle.Render("No se puede deshacer. Para conservarlo sin que aparezca,\nedítalo con 'e' y desmárcalo como activo."))
	b.WriteString("\n\n")
	b.WriteString(helpEntry("s/y", "eliminar") + "  " + helpEntry("n/esc", "cancelar"))

	box := modalBoxStyle.Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}
