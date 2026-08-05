// Package tui implements a lazygit-style terminal UI: a service list on the
// left, live combined logs of the selected service on the right, a
// full-screen modal to paste AWS credentials, and a panel to log into (or
// create) AWS SSO profiles discovered from the user's own ~/.aws/config.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"scriptstui/internal/awscreds"
	"scriptstui/internal/config"
	"scriptstui/internal/procman"
	"scriptstui/internal/ssologin"
)

const maxLogLines = 2000
const listWidth = 66
const titleFieldWidth = 34

type mode int

const (
	modeList mode = iota
	modeCredentials
	modeSSO
	modeAssignProfile
	modeNewProfile
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

func helpEntry(key, desc string) string {
	return helpKeyStyle.Render(key) + " " + helpStyle.Render(desc)
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
	cfg       config.Service
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
	err      error
}

func refreshSSOCmd() tea.Cmd {
	return func() tea.Msg {
		profiles, err := ssologin.DiscoverProfiles()
		if err != nil {
			return ssoRefreshMsg{err: err}
		}
		status := make(map[string]bool, len(profiles))
		for _, p := range profiles {
			status[p.Name] = ssologin.CheckStatus(p.Name)
		}
		return ssoRefreshMsg{profiles: profiles, status: status}
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
		err := ssologin.WriteProfile(name, startURL, region, accountID, roleName, region)
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
	mgr           *procman.Manager
	services      []*serviceState
	index         map[string]int
	cursor        int
	marked        map[string]bool
	tunnelProfile map[string]string // service ID -> AWS CLI profile name to use

	viewport   viewport.Model
	ready      bool
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
	ssoCursor          int
	ssoRunning         bool
	ssoAction          string
	ssoActiveProfile   string
	ssoOutput          []string
	ssoLines           <-chan string
	ssoDone            <-chan error
	ssoCancel          context.CancelFunc
	ssoRefreshErr      string

	assignTarget string
	assignCursor int

	np *newProfileWizard

	showHelp    bool
	showAllLogs bool

	quitting bool
}

func NewModel(services []config.Service, mgr *procman.Manager) Model {
	states := make([]*serviceState, len(services))
	index := make(map[string]int, len(services))
	for i, s := range services {
		states[i] = &serviceState{cfg: s, status: procman.StatusStopped}
		index[s.ID] = i
	}
	return Model{
		mgr:           mgr,
		services:      states,
		index:         index,
		marked:        make(map[string]bool),
		tunnelProfile: make(map[string]string),
		ssoStatus:     make(map[string]bool),
	}
}

func waitForEvent(events <-chan procman.Event) tea.Cmd {
	return func() tea.Msg {
		return <-events
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(waitForEvent(m.mgr.Events()), tick(), refreshSSOCmd())
}

func (m *Model) selected() *serviceState {
	return m.services[m.cursor]
}

// targets returns the marked services' IDs, or just the highlighted one if
// nothing is marked.
func (m *Model) targets() []string {
	if len(m.marked) == 0 {
		return []string{m.selected().cfg.ID}
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
	m.viewport.SetContent(strings.Join(s.logs, "\n"))
	m.viewport.GotoBottom()
}

func (m *Model) layout() {
	const headerHeight = 1
	const footerHeight = 1
	const borderHeight = 2    // top + bottom border, shared by both panels
	const logHeaderHeight = 1 // "Logs — ..." line drawn inside the log box

	bodyHeight := m.height - headerHeight - footerHeight
	if bodyHeight < 5 {
		bodyHeight = 5
	}
	logWidth := m.width - listWidth - 6
	if logWidth < 20 {
		logWidth = 20
	}

	m.listHeight = bodyHeight - borderHeight
	logViewportHeight := bodyHeight - borderHeight - logHeaderHeight

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
		default:
			return m.updateList(msg)
		}

	case ssoRefreshMsg:
		if msg.err != nil {
			m.ssoRefreshErr = msg.err.Error()
			return m, nil
		}
		m.ssoRefreshErr = ""
		m.discoveredProfiles = msg.profiles
		m.ssoStatus = msg.status
		return m, nil

	case ssoLineMsg:
		m.ssoOutput = append(m.ssoOutput, msg.line)
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
			m.refreshViewportContent()
		}
		return m, nil
	case "down", "j":
		if m.cursor < len(m.services)-1 {
			m.cursor++
			m.refreshViewportContent()
		}
		return m, nil
	case " ":
		id := m.selected().cfg.ID
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
		m.assignTarget = m.selected().cfg.ID
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
		return m, refreshSSOCmd()
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
	case "enter":
		if m.assignCursor == 0 {
			delete(m.tunnelProfile, m.assignTarget)
		} else {
			m.tunnelProfile[m.assignTarget] = m.discoveredProfiles[m.assignCursor-1].Name
		}
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
	default:
		return m.viewList()
	}
}

func (m Model) viewList() string {
	header := titleStyle.Render("scriptstui — gestor de túneles")

	var list strings.Builder
	for i, s := range m.services {
		cursorMark := "  "
		titleRendered := itemStyle.Render(padTrunc(s.cfg.Title, titleFieldWidth))
		if i == m.cursor {
			cursorMark = cursorStyle.Render("▶ ")
			titleRendered = selectedTitle.Render(padTrunc(s.cfg.Title, titleFieldWidth))
		}
		selMark := " "
		if m.marked[s.cfg.ID] {
			selMark = checkedStyle.Render("✓")
		}

		statusText := s.status.String()
		if s.status == procman.StatusRunning && !s.startedAt.IsZero() {
			statusText += " · " + time.Since(s.startedAt).Round(time.Second).String()
		}
		statusRendered := lipgloss.NewStyle().Foreground(statusColor(s.status)).Render(statusText)

		line := cursorMark + selMark + " " + statusDot(s.status) + " " + titleRendered + "  " + statusRendered
		list.WriteString(line)
		list.WriteString("\n")
	}
	listPanel := listBoxStyle.Width(listWidth).Height(m.listHeight).Render(list.String())

	sel := m.selected()
	profileInfo := "sin perfil asignado"
	if p := m.tunnelProfile[sel.cfg.ID]; p != "" {
		profileInfo = "perfil: " + p
	}
	logHeader := logHeaderStyle.Render("Logs — "+sel.cfg.Title) + "  " +
		itemDimStyle.Render("("+profileInfo+" · p para cambiar)")
	logPanel := logBoxStyle.Render(logHeader + "\n" + m.viewport.View())

	body := lipgloss.JoinHorizontal(lipgloss.Top, listPanel, logPanel)

	help := helpEntry("enter/s", "iniciar") + "  " +
		helpEntry("x", "detener") + "  " +
		helpEntry("v", "ver todos los logs") + "  " +
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
	b.WriteString(helpEntry("v", "ver los logs de los 3 túneles a la vez") + "\n")
	b.WriteString(helpEntry("pgup/pgdn", "scroll de los logs") + "\n")

	b.WriteString("\n")
	b.WriteString(itemDimStyle.Render("Credenciales AWS"))
	b.WriteString("\n")
	b.WriteString(helpEntry("a", "abrir el panel de cuentas AWS SSO (recomendado)") + "\n")
	b.WriteString(helpEntry("c", "pegar credenciales manualmente (respaldo)") + "\n")

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
		b.WriteString(modalHintStyle.Render("No hay perfiles SSO en ~/.aws/config.\nAbre el panel de cuentas ('a') y presiona 'n' para crear uno."))
		b.WriteString("\n")
	}
	for i, p := range m.discoveredProfiles {
		renderRow(i+1, p.Name, "cuenta "+p.AccountID+" · rol "+p.RoleName)
	}

	b.WriteString("\n")
	b.WriteString(helpEntry("↑/↓", "navegar") + "  " + helpEntry("enter", "elegir") + "  " + helpEntry("esc", "cancelar"))

	box := modalBoxStyle.Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m Model) viewSSO() string {
	header := titleStyle.Render("scriptstui — cuentas AWS SSO")

	var list strings.Builder
	if len(m.discoveredProfiles) == 0 {
		list.WriteString(itemDimStyle.Render("No hay perfiles SSO en ~/.aws/config todavía.\n"))
		list.WriteString(itemDimStyle.Render("Presiona 'n' para crear uno (aws configure sso).\n"))
		if m.ssoRefreshErr != "" {
			list.WriteString(modalErrStyle.Render("✗ " + m.ssoRefreshErr))
		}
	}
	for i, p := range m.discoveredProfiles {
		marker := "  "
		title := padTrunc(p.Name, titleFieldWidth)
		titleRendered := itemStyle.Render(title)
		if i == m.ssoCursor {
			marker = cursorStyle.Render("▶ ")
			titleRendered = selectedTitle.Render(title)
		}

		badge := itemDimStyle.Render("✗ no logueado")
		if m.ssoStatus[p.Name] {
			badge = checkedStyle.Render("✓ logueado")
		}

		list.WriteString(marker + titleRendered + " " + badge + "\n")
		detail := "    cuenta " + p.AccountID + " · rol " + p.RoleName
		list.WriteString(itemDimStyle.Render(detail) + "\n")
	}
	listPanel := listBoxStyle.Width(listWidth).Height(m.listHeight).Render(list.String())

	logWidth := m.width - listWidth - 6
	if logWidth < 20 {
		logWidth = 20
	}
	outHeader := logHeaderStyle.Render("Salida de aws sso")
	outBody := strings.Join(m.ssoOutput, "\n")
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
		b.WriteString("Elige la cuenta:\n\n")
		switch {
		case np.err != "":
			b.WriteString(modalErrStyle.Render("✗ " + np.err))
		case !np.accountsLoaded:
			b.WriteString(modalHintStyle.Render("Cargando cuentas..."))
		case len(np.accounts) == 0:
			b.WriteString(modalHintStyle.Render("No tienes cuentas asignadas en este portal."))
		}
		for i, a := range np.accounts {
			marker, title := "  ", itemStyle.Render(a.AccountName+" ("+a.AccountID+")")
			if i == np.accountCursor {
				marker, title = cursorStyle.Render("▶ "), selectedTitle.Render(a.AccountName+" ("+a.AccountID+")")
			}
			b.WriteString(marker + title + "\n")
		}
		b.WriteString("\n")
		b.WriteString(helpEntry("↑/↓", "navegar") + "  " + helpEntry("enter", "elegir") + "  " + helpEntry("esc", "cancelar"))

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
