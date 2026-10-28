package tui

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"scriptstui/internal/procman"
	"scriptstui/internal/ssologin"
	"scriptstui/internal/traffic"
)

// --- Animation clock -------------------------------------------------------

// animInterval drives everything that moves on the main panel: the spinner
// on a tunnel that is starting or stopping and the heartbeat in the header.
const animInterval = 120 * time.Millisecond

type animMsg struct{}

func animTick() tea.Cmd {
	return tea.Tick(animInterval, func(time.Time) tea.Msg { return animMsg{} })
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinner is the current spinner frame, in the busy color.
func (m Model) spinner() string {
	frame := spinnerFrames[m.frame%len(spinnerFrames)]
	return lipgloss.NewStyle().Foreground(colorBusy).Render(frame)
}

// --- Health monitor --------------------------------------------------------

// The checks themselves run in the backend (procman's health monitor), once
// for every front end; this side only shows what they report.

type healthState int

const (
	healthUnknown healthState = iota
	healthOK
	healthBad
)

// resetHealth forgets what the monitor knew, for a tunnel that just stopped
// or restarted.
func (s *serviceState) resetHealth() {
	s.health, s.latency, s.latencyKnown = healthUnknown, 0, false
}

// applyHealth records a health check of the tunnel at index i, logging the
// moments it stops and starts answering again.
func (m *Model) applyHealth(i int, ev procman.Event) {
	s := m.services[i]
	if s.status != procman.StatusRunning || !s.startedAt.Equal(ev.Since) {
		return // a check of an earlier session
	}
	was := s.health
	if !ev.Healthy {
		s.health, s.latencyKnown = healthBad, false
		if was != healthBad {
			m.appendLog(i, "SALUD", "sin respuesta: "+ev.Text)
		}
		return
	}
	s.health = healthOK
	s.latency, s.latencyKnown = ev.RTT, ev.Measured
	if was == healthBad {
		m.appendLog(i, "SALUD", "responde de nuevo")
	}
}

// appendLog adds a line of the UI's own to a tunnel's log, formatted like the
// ones procman sends.
func (m *Model) appendLog(i int, label, text string) {
	s := m.services[i]
	s.logs = append(s.logs, fmt.Sprintf("%s %s", labelStyle.Render("["+label+"]"), text))
	if len(s.logs) > maxLogLines {
		s.logs = s.logs[len(s.logs)-maxLogLines:]
	}
	if i == m.cursor {
		m.refreshViewportContent()
	}
}

// --- SSO expiry ------------------------------------------------------------

// ssoExpiryInterval re-reads the AWS CLI's token cache, which is cheap (no
// network), so a login done from another terminal shows up within a minute.
const ssoExpiryInterval = time.Minute

type ssoExpiryTickMsg struct{}

type ssoExpiryMsg struct {
	expiry map[string]time.Time
}

func ssoExpiryTick() tea.Cmd {
	return tea.Tick(ssoExpiryInterval, func(time.Time) tea.Msg { return ssoExpiryTickMsg{} })
}

func readSSOExpiryCmd(profiles []ssologin.Profile) tea.Cmd {
	return func() tea.Msg {
		expiry := make(map[string]time.Time, len(profiles))
		for _, p := range profiles {
			if at, ok := ssologin.SessionExpiry(p); ok {
				expiry[p.Name] = at
			}
		}
		return ssoExpiryMsg{expiry: expiry}
	}
}

// ssoHeader summarizes the SSO sessions the enabled tunnels depend on: how
// many already expired, or how long until the next one does.
func (m Model) ssoHeader() string {
	used := make(map[string]bool)
	for _, s := range m.services {
		if p := m.tunnelProfile[s.cfg.ID]; p != "" && s.row.Enabled {
			used[p] = true
		}
	}
	now := time.Now()
	expired := 0
	var soonest time.Duration
	soonestName := ""
	for name := range used {
		at, ok := m.ssoExpiry[name]
		if !ok {
			continue
		}
		left := at.Sub(now)
		if left <= 0 {
			expired++
			continue
		}
		if soonestName == "" || left < soonest {
			soonest, soonestName = left, name
		}
	}

	switch {
	case expired == 1:
		return modalErrStyle.Render("✗ 1 sesión SSO vencida · a para entrar")
	case expired > 1:
		return modalErrStyle.Render(fmt.Sprintf("✗ %d sesiones SSO vencidas · a para entrar", expired))
	case soonestName == "":
		return ""
	}
	text := "SSO vence en " + compactDuration(soonest)
	switch {
	case soonest < 10*time.Minute:
		return modalErrStyle.Render("⚠ " + text)
	case soonest < 30*time.Minute:
		return lipgloss.NewStyle().Bold(true).Foreground(colorBusy).Render("⚠ " + text)
	default:
		return itemDimStyle.Render(text)
	}
}

// --- Info panel: signal wave, activity and clock ---------------------------

// signalLevel sums up the running tunnels for the wave: its color and shape.
type signalLevel int

const (
	signalIdle signalLevel = iota // nothing running: a flat line
	signalOK                      // every running tunnel answers
	signalBusy                    // one is starting or stopping
	signalBad                     // one stopped answering or failed
)

// activity counts what the info panel reports and picks the wave's level.
func (m Model) activity() (level signalLevel, running, bad int) {
	busy := 0
	for _, s := range m.services {
		switch s.status {
		case procman.StatusRunning:
			running++
			if s.health == healthBad {
				bad++
			}
		case procman.StatusFailed:
			bad++
		case procman.StatusStarting, procman.StatusStopping:
			busy++
		}
	}
	switch {
	case running == 0 && busy == 0:
		return signalIdle, running, bad
	case bad > 0:
		return signalBad, running, bad
	case busy > 0:
		return signalBusy, running, bad
	}
	return signalOK, running, bad
}

func (l signalLevel) color() lipgloss.Color {
	switch l {
	case signalOK:
		return colorRunning
	case signalBusy:
		return colorBusy
	case signalBad:
		return colorFailed
	}
	return colorStopped
}

// braille dot bits, indexed [column][row] within one 2x4 braille cell.
var brailleBits = [2][4]rune{{0x01, 0x02, 0x04, 0x40}, {0x08, 0x10, 0x20, 0x80}}

// signalWave draws a sine wave travelling right to left across width columns
// and rows lines, in braille so it is four times finer than a row of text.
// frame moves it; level shapes it — a clean wave when all is well, one with
// interference when a tunnel is failing, and a flat line with nothing up.
func signalWave(width, rows, frame int, level signalLevel) []string {
	if width < 1 || rows < 1 {
		return nil
	}
	w, h := width*2, rows*4
	cells := make([][]rune, rows)
	for r := range cells {
		cells[r] = make([]rune, width)
	}
	set := func(x, y int) {
		if y < 0 {
			y = 0
		}
		if y >= h {
			y = h - 1
		}
		cells[y/4][x/2] |= brailleBits[x%2][y%4]
	}

	mid := float64(h-1) / 2
	amp := mid * 0.85
	phase := float64(frame) * 0.35
	const cycles = 3.0

	prev := -1
	for x := 0; x < w; x++ {
		t := 2 * math.Pi * cycles * float64(x) / float64(w)
		v := 0.0
		switch level {
		case signalOK, signalBusy:
			v = math.Sin(t + phase)
		case signalBad:
			// A noisy, clipped signal: something is getting lost.
			v = 0.6*math.Sin(t+phase) + 0.4*math.Sin(3.7*t-2*phase)
		}
		y := int(math.Round(mid - amp*v))
		// Fill the gap to the previous column so steep parts stay joined.
		if prev >= 0 {
			lo, hi := prev, y
			if lo > hi {
				lo, hi = hi, lo
			}
			for yy := lo; yy <= hi; yy++ {
				set(x, yy)
			}
		} else {
			set(x, y)
		}
		prev = y
	}

	style := lipgloss.NewStyle().Foreground(level.color())
	out := make([]string, rows)
	for r := range cells {
		var b strings.Builder
		for _, c := range cells[r] {
			b.WriteRune(0x2800 + c)
		}
		out[r] = style.Render(b.String())
	}
	return out
}

var (
	weekdaysES = []string{"dom", "lun", "mar", "mié", "jue", "vie", "sáb"}
	monthsES   = []string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sep", "oct", "nov", "dic"}
)

// clock is the date and time, in Spanish.
func clock(t time.Time) string {
	return fmt.Sprintf("%s %d %s · %s",
		weekdaysES[t.Weekday()], t.Day(), monthsES[t.Month()-1], t.Format("15:04:05"))
}

// waveRows is how tall the wave is drawn: smaller on a short terminal, so
// the tunnel list keeps its room.
func (m Model) waveRows() int {
	switch {
	case m.height >= 32:
		return 3
	case m.height >= 24:
		return 2
	}
	return 1
}

// infoRows is the info panel's content height: title, wave, activity line
// and the SSO/clock line.
func (m Model) infoRows() int {
	return m.waveRows() + 3
}

// infoPanel is the box under the tunnel list: the app's title, the signal
// wave, how many tunnels are up, the SSO notice and the date and time.
func (m Model) infoPanel(width int) string {
	inner := width - 2 // listBoxStyle's padding
	level, running, bad := m.activity()

	var lines []string
	lines = append(lines, titleStyle.Render("scriptstui — gestor de túneles"))
	lines = append(lines, signalWave(inner, m.waveRows(), m.frame, level)...)

	var activity string
	if level == signalIdle {
		activity = itemDimStyle.Render("sin túneles activos")
		if bad > 0 {
			activity += "  " + modalErrStyle.Render(fmt.Sprintf("· %d fallido", bad))
		}
	} else {
		label := fmt.Sprintf("♥ %d activo", running)
		if running != 1 {
			label += "s"
		}
		if bad > 0 {
			label += fmt.Sprintf(" · %d sin respuesta", bad)
		}
		activity = lipgloss.NewStyle().Bold(true).Foreground(level.color()).Render(label)
	}
	lines = append(lines, activity)

	now := labelStyle.Render(clock(time.Now()))
	bottom := now
	if sso := m.ssoHeader(); sso != "" {
		if gap := inner - lipgloss.Width(sso) - lipgloss.Width(now); gap >= 2 {
			bottom = sso + strings.Repeat(" ", gap) + now
		}
	}
	lines = append(lines, bottom)

	return infoBoxStyle.Width(width).Height(m.infoRows()).Render(strings.Join(lines, "\n"))
}

// --- Verify marks on the list ----------------------------------------------

// verifyMarkTTL is how long a Verificar result stays next to its tunnel,
// fading as it ages so an old result doesn't read like a fresh one.
const verifyMarkTTL = 5 * time.Minute

var (
	verifyOKFade   = []lipgloss.Color{"#3FB950", "#2F8A3E", "#25592D"}
	verifyFailFade = []lipgloss.Color{"#F85149", "#B23A35", "#6E2A27"}
)

// verifyMark renders the tunnel's last Verificar result, faded by age, or a
// blank of the same width once it is older than verifyMarkTTL.
func verifyMark(s *serviceState) string {
	if s.verifiedAt.IsZero() {
		return " "
	}
	age := time.Since(s.verifiedAt)
	if age > verifyMarkTTL {
		return " "
	}
	step := int(age * time.Duration(len(verifyOKFade)) / verifyMarkTTL)
	if step >= len(verifyOKFade) {
		step = len(verifyOKFade) - 1
	}
	if s.verifiedOK {
		return lipgloss.NewStyle().Foreground(verifyOKFade[step]).Render("✓")
	}
	return lipgloss.NewStyle().Bold(true).Foreground(verifyFailFade[step]).Render("✗")
}

// --- Status column ---------------------------------------------------------

// statusFieldWidth is what is left of the list row for the status once the
// other columns are laid out.
const statusFieldWidth = 30

// compactDuration writes a duration in at most two units — 34s, 12m, 1h05m,
// 2d3h — so the status column stays narrow however long a tunnel runs.
func compactDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// statusCell renders a running tunnel's status: uptime plus the monitor's
// latency, or an amber warning once it stops answering.
func statusCell(s *serviceState) (string, lipgloss.Color) {
	text := s.status.String()
	if s.status == procman.StatusFailed && !s.statusSince.IsZero() {
		// procman stops a failed tunnel on its own; say when.
		left := procman.FailedResetDelay - time.Since(s.statusSince)
		if left < 0 {
			left = 0
		}
		return fmt.Sprintf("%s · se detiene en %ds", text, int(left.Seconds()+0.5)), statusColor(s.status)
	}
	if s.status != procman.StatusRunning || s.startedAt.IsZero() {
		return text, statusColor(s.status)
	}
	text += " · " + compactDuration(time.Since(s.startedAt))
	switch {
	case s.health == healthBad:
		return text + " · sin respuesta", colorBusy
	case s.health == healthOK && s.latencyKnown:
		return text + " · " + formatLatency(s.latency), colorRunning
	}
	return text, colorRunning
}

func formatLatency(d time.Duration) string {
	if d < time.Millisecond {
		return "<1ms"
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

// fitStatus trims a status to the column so a long one can't wrap the row.
func fitStatus(s string) string {
	if utf8.RuneCountInString(s) <= statusFieldWidth {
		return s
	}
	return padTrunc(s, statusFieldWidth)
}

// --- traffic: rate and total of each running tunnel ------------------------

// trafficInterval is how often the running tunnels' traffic is sampled.
const trafficInterval = 2 * time.Second

// trafficFieldWidth fits "↓999K/s ↑999K/s · 999M".
const trafficFieldWidth = 23

type trafficTickMsg struct{}

// trafficMsg carries one sample, keyed by service id. nil means nothing was
// measured this time (no tunnel running, or nettop failed).
type trafficMsg struct {
	stats map[string]traffic.Stats
}

func trafficTick() tea.Cmd {
	return tea.Tick(trafficInterval, func(time.Time) tea.Msg { return trafficTickMsg{} })
}

// sampleTrafficCmd measures every running tunnel's port. Only one sample is
// in flight at a time: the next tick is scheduled once its result arrives.
func (m Model) sampleTrafficCmd() tea.Cmd {
	ports := make(map[int]string)
	for _, s := range m.services {
		if s.status != procman.StatusRunning {
			continue
		}
		if port, err := strconv.Atoi(s.cfg.LocalPort()); err == nil {
			ports[port] = s.cfg.ID
		}
	}
	sampler := m.traffic
	return func() tea.Msg {
		if len(ports) == 0 || sampler == nil {
			return trafficMsg{}
		}
		list := make([]int, 0, len(ports))
		for port := range ports {
			list = append(list, port)
		}
		byPort, err := sampler.Sample(context.Background(), list)
		if err != nil {
			return trafficMsg{}
		}
		stats := make(map[string]traffic.Stats, len(byPort))
		for port, st := range byPort {
			stats[ports[port]] = st
		}
		return trafficMsg{stats: stats}
	}
}

func (m *Model) applyTraffic(msg trafficMsg) {
	for _, s := range m.services {
		st, ok := msg.stats[s.cfg.ID]
		s.traffic, s.trafficKnown = st, ok
	}
}

// trafficCell is the column next to the port: download and upload rate of
// the tunnel right now, and everything it moved since it started. Blank for
// a tunnel that isn't running.
func trafficCell(s *serviceState) string {
	if s.status != procman.StatusRunning || !s.trafficKnown {
		return strings.Repeat(" ", trafficFieldWidth)
	}
	st := s.traffic
	in, out := "…", "…"
	if st.HasRate {
		in, out = traffic.FormatBytes(st.RateIn)+"/s", traffic.FormatBytes(st.RateOut)+"/s"
	}
	text := fmt.Sprintf("↓%-6s ↑%-6s · %s", in, out, traffic.FormatBytes(float64(st.TotalIn+st.TotalOut)))
	style := itemDimStyle
	if st.HasRate && st.RateIn+st.RateOut >= 1000 {
		style = portStyle // moving data right now: same color as the port
	}
	return style.Render(padTrunc(text, trafficFieldWidth))
}
