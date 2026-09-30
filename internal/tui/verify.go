package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"scriptstui/internal/config"
	"scriptstui/internal/procman"
)

// maxParallelProbes caps how many tunnels a parallel run opens at once.
// Enabled tunnels never share a local port (the store enforces it), so the
// cap isn't about ports: it keeps a burst of StartSession calls and SSO token
// refreshes from tripping AWS throttling, which would read as false failures.
const maxParallelProbes = 4

// verifyState is where one tunnel stands in a verification run.
type verifyState int

const (
	verifyQueued verifyState = iota
	verifyChecking
	verifyOK
	verifyFailed
	verifySkipped
)

type verifyItem struct {
	id    string
	title string
	state verifyState
	// note is why it failed or was skipped, or how it was checked.
	note string
}

// verifyRun checks every enabled tunnel: a stopped one is opened just long
// enough to push a connection through it, a running one is tested on its port
// as it is, so verifying never takes a working tunnel down. It starts by
// asking whether to go one at a time (the default) or in parallel.
type verifyRun struct {
	choosing bool
	parallel bool // chosen mode; while choosing, the highlighted option
	showHelp bool // '?' explanation shown over the panel; the run keeps going

	items    []verifyItem
	disabled int // tunnels left out because they are turned off
	started  time.Time
	elapsed  time.Duration // set once the run is over
	ctx      context.Context
	cancel   context.CancelFunc
}

func (r *verifyRun) done() bool {
	if r.choosing {
		return false
	}
	return r.count(verifyQueued) == 0 && r.count(verifyChecking) == 0
}

func (r *verifyRun) count(state verifyState) int {
	n := 0
	for _, it := range r.items {
		if it.state == state {
			n++
		}
	}
	return n
}

type verifyResultMsg struct {
	run *verifyRun
	idx int
	err error
}

// probeCmd checks one tunnel; running says whether it is already up, in which
// case only its port is tested.
func probeCmd(run *verifyRun, idx int, mgr Backend, cfg config.Service, running bool) tea.Cmd {
	return func() tea.Msg {
		if running {
			port := 0
			for _, step := range cfg.Steps {
				if step.Kind == config.StepTunnel && step.LocalPort > 0 {
					port = step.LocalPort
					break
				}
			}
			return verifyResultMsg{run: run, idx: idx, err: procman.CheckPort(port, procman.RemotePort(cfg))}
		}
		return verifyResultMsg{run: run, idx: idx, err: mgr.Probe(run.ctx, cfg)}
	}
}

// startVerify opens the panel on the how-to-run question, keeping the last
// choice highlighted when it is a repeat.
func (m Model) startVerify() (tea.Model, tea.Cmd) {
	parallel := false
	if m.verify != nil {
		parallel = m.verify.parallel
		m.verify.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.verify = &verifyRun{choosing: true, parallel: parallel, ctx: ctx, cancel: cancel}
	m.mode = modeVerify
	return m, nil
}

// beginVerify fills the run with the tunnels as they are right now and
// launches the first checks.
func (m *Model) beginVerify() tea.Cmd {
	run := m.verify
	run.choosing = false
	run.started = time.Now()
	for _, s := range m.services {
		if !s.row.Enabled {
			run.disabled++
			continue
		}
		run.items = append(run.items, verifyItem{id: s.cfg.ID, title: s.cfg.Title})
	}
	return m.advanceVerify()
}

// advanceVerify launches queued checks until the run's limit is in flight —
// one at a time, or maxParallelProbes in parallel — marking the tunnels that
// can't be checked right now as skipped.
func (m *Model) advanceVerify() tea.Cmd {
	run := m.verify
	limit := 1
	if run.parallel {
		limit = maxParallelProbes
	}
	inFlight := run.count(verifyChecking)

	var cmds []tea.Cmd
	for idx := range run.items {
		if inFlight >= limit {
			break
		}
		it := &run.items[idx]
		if it.state != verifyQueued {
			continue
		}
		i, ok := m.index[it.id]
		if !ok {
			it.state, it.note = verifySkipped, "ya no existe"
			continue
		}
		s := m.services[i]
		switch s.status {
		case procman.StatusStarting, procman.StatusStopping:
			it.state, it.note = verifySkipped, "omitido: está "+s.status.String()
			continue
		}
		cfg := s.cfg
		if p := m.tunnelProfile[it.id]; p != "" {
			cfg = withProfile(cfg, p)
		}
		running := s.status == procman.StatusRunning
		it.state = verifyChecking
		if running {
			it.note = "activo, probando su puerto..."
		} else {
			it.note = "abriendo túnel de prueba..."
		}
		inFlight++
		cmds = append(cmds, probeCmd(run, idx, m.mgr, cfg, running))
	}

	if run.done() {
		run.elapsed = time.Since(run.started)
		run.cancel()
	}
	return tea.Batch(cmds...)
}

func (m Model) handleVerifyResult(msg verifyResultMsg) (tea.Model, tea.Cmd) {
	// A result from a run that was closed or restarted has nowhere to go.
	if m.verify == nil || msg.run != m.verify || msg.idx >= len(m.verify.items) {
		return m, nil
	}
	it := &m.verify.items[msg.idx]
	if it.state != verifyChecking {
		return m, nil
	}
	if msg.err != nil {
		it.state, it.note = verifyFailed, strings.SplitN(msg.err.Error(), "\n", 2)[0]
	} else {
		it.state, it.note = verifyOK, "conexión ok"
	}
	// Leave the result on the list too, where it fades out on its own.
	if i, ok := m.index[it.id]; ok {
		m.services[i].verifiedAt, m.services[i].verifiedOK = time.Now(), msg.err == nil
	}
	return m, m.advanceVerify()
}

func (m Model) updateVerify(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	run := m.verify
	switch msg.String() {
	case "ctrl+c":
		m.quitting = true
		m.mgr.QuitAll()
		return m, tea.Quit
	case "?":
		run.showHelp = !run.showHelp
		return m, nil
	}
	if run.showHelp {
		// Any key closes the explanation, the same as the main help.
		run.showHelp = false
		return m, nil
	}
	switch msg.String() {
	case "esc", "q":
		// Leaving mid-run cancels it; whatever was being opened is torn down.
		run.cancel()
		m.mode = modeList
		m.layout()
		return m, nil
	}

	if run.choosing {
		switch msg.String() {
		case "up", "k", "down", "j", "tab":
			run.parallel = !run.parallel
		case "1":
			run.parallel = false
			return m, m.beginVerify()
		case "2":
			run.parallel = true
			return m, m.beginVerify()
		case "enter":
			return m, m.beginVerify()
		}
		return m, nil
	}

	if (msg.String() == "t" || msg.String() == "r") && run.done() {
		return m.startVerify()
	}
	return m, nil
}

func (m Model) viewVerify() string {
	run := m.verify
	width := m.width - 16
	if width > 100 {
		width = 100
	}
	if width < 40 {
		width = 40
	}
	if run.showHelp {
		return m.viewVerifyHelp(width)
	}

	var b strings.Builder
	b.WriteString(modalTitleStyle.Render("Verificar túneles") + "  " + newBadge())
	b.WriteString("\n")
	b.WriteString(modalHintStyle.Render("Los detenidos se abren un momento y se vuelven a cerrar;"))
	b.WriteString("\n")
	b.WriteString(modalHintStyle.Render("los que ya están activos se prueban sin reiniciarlos."))
	b.WriteString("\n\n")

	if run.choosing {
		b.WriteString(itemStyle.Render("¿Cómo quieres probarlos?"))
		b.WriteString("\n\n")
		options := []struct {
			key, label, hint string
			parallel         bool
		}{
			{"1", "En cola (uno a uno)", "por defecto · más lento, una sesión a la vez", false},
			{"2", "En paralelo", fmt.Sprintf("más rápido · hasta %d túneles a la vez", maxParallelProbes), true},
		}
		for _, o := range options {
			mark, label := "  ", itemStyle.Render(padTrunc(o.label, 20))
			if o.parallel == run.parallel {
				mark, label = cursorStyle.Render("▶ "), selectedTitle.Render(padTrunc(o.label, 20))
			}
			b.WriteString(mark + helpKeyStyle.Render(o.key) + " " + label + "  " + itemDimStyle.Render(o.hint) + "\n")
		}
		b.WriteString("\n")
		b.WriteString(helpEntry("↑/↓", "elegir") + "  " + helpEntry("enter", "empezar") + "  " +
			helpEntry("?", "¿qué es esto?") + "  " + helpEntry("esc", "cancelar"))
		box := modalBoxStyle.Width(width + 6).Render(b.String())
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
	}

	if len(run.items) == 0 {
		b.WriteString(itemDimStyle.Render("No hay túneles activados para verificar."))
		b.WriteString("\n")
	}

	const titleWidth = 38
	noteWidth := width - titleWidth - 3
	// Keep the first one still being checked on screen when the list
	// outgrows the box.
	focus := len(run.items) - 1
	for i, it := range run.items {
		if it.state == verifyChecking || it.state == verifyQueued {
			focus = i
			break
		}
	}
	start, end := visibleWindow(focus, len(run.items), m.height-16)
	if start > 0 {
		b.WriteString(itemDimStyle.Render(fmt.Sprintf("  ↑ %d más", start)) + "\n")
	}
	for _, it := range run.items[start:end] {
		mark, style := "·", itemDimStyle
		note := "en cola"
		switch it.state {
		case verifyChecking:
			mark, style, note = spinnerFrames[m.frame%len(spinnerFrames)], lipgloss.NewStyle().Foreground(colorBusy), it.note
		case verifyOK:
			mark, style, note = "✓", lipgloss.NewStyle().Foreground(colorRunning), it.note
		case verifyFailed:
			mark, style, note = "✗", modalErrStyle, it.note
		case verifySkipped:
			mark, note = "-", it.note
		}
		b.WriteString(style.Render(mark) + " " + itemStyle.Render(padTrunc(it.title, titleWidth)) + " " +
			style.Render(padTrunc(note, noteWidth)) + "\n")
	}
	if end < len(run.items) {
		b.WriteString(itemDimStyle.Render(fmt.Sprintf("  ↓ %d más", len(run.items)-end)) + "\n")
	}
	b.WriteString("\n")

	modeLabel := "en cola"
	if run.parallel {
		modeLabel = "en paralelo"
	}
	if !run.done() {
		finished := len(run.items) - run.count(verifyQueued) - run.count(verifyChecking)
		b.WriteString(lipgloss.NewStyle().Foreground(colorBusy).Render(fmt.Sprintf(
			"Verificando %s · %d de %d listos · %d en curso",
			modeLabel, finished, len(run.items), run.count(verifyChecking))))
		b.WriteString("\n\n")
		b.WriteString(helpEntry("?", "ayuda") + "  " + helpEntry("esc", "cancelar"))
	} else {
		failed := run.count(verifyFailed)
		summary := fmt.Sprintf("%d ok · %d fallaron", run.count(verifyOK), failed)
		if n := run.count(verifySkipped); n > 0 {
			summary += fmt.Sprintf(" · %d omitidos", n)
		}
		if run.disabled > 0 {
			summary += fmt.Sprintf(" · %d desactivados sin probar", run.disabled)
		}
		summary += fmt.Sprintf(" · %s en %s", modeLabel, run.elapsed.Round(100*time.Millisecond))
		if failed == 0 {
			b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorRunning).Render("✓ Todos los túneles conectan"))
			b.WriteString("\n" + itemDimStyle.Render(summary))
		} else {
			b.WriteString(modalErrStyle.Render("Fallaron la conexión:"))
			b.WriteString("\n")
			for _, it := range run.items {
				if it.state == verifyFailed {
					b.WriteString(modalErrStyle.Render("  ✗ ") + itemStyle.Render(it.title) + "\n")
				}
			}
			b.WriteString("\n" + itemDimStyle.Render(summary+" · el detalle queda en los logs de cada túnel"))
		}
		b.WriteString("\n\n")
		b.WriteString(helpEntry("t", "verificar de nuevo") + "  " + helpEntry("?", "ayuda") + "  " + helpEntry("esc", "cerrar"))
	}

	box := modalBoxStyle.Width(width + 6).Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// viewVerifyHelp explains the panel in plain words, for someone who just
// wants to know whether their tunnels work and what to do when one doesn't.
func (m Model) viewVerifyHelp(width int) string {
	section := func(title string) string { return "\n" + labelStyle.Render(title) + "\n" }
	text := func(s string) string { return itemStyle.Render(s) + "\n" }
	dim := func(s string) string { return itemDimStyle.Render(s) + "\n" }

	var b strings.Builder
	b.WriteString(modalTitleStyle.Render("¿Qué hace Verificar túneles?") + "  " + newBadge() + "\n")
	b.WriteString(section("Para qué sirve"))
	b.WriteString(text("Revisa si cada túnel logra llegar de verdad a su base de datos o servicio,"))
	b.WriteString(text("para que sepas cuáles funcionan antes de necesitarlos."))

	b.WriteString(section("Qué hace con tus túneles"))
	b.WriteString(text("• Si está detenido: lo abre unos segundos, prueba la conexión y lo cierra."))
	b.WriteString(text("• Si ya está activo: solo prueba la conexión. No lo reinicia ni lo corta."))
	b.WriteString(dim("  Los desactivados no se prueban."))

	b.WriteString(section("En cola o en paralelo"))
	b.WriteString(text("• En cola: prueba un túnel a la vez. Es más lento, pero es lo más seguro."))
	b.WriteString(text(fmt.Sprintf("• En paralelo: prueba hasta %d a la vez, así termina mucho antes.", maxParallelProbes)))
	b.WriteString(dim("  No pasan de 4 a la vez para que AWS no bloquee tantas conexiones juntas."))

	b.WriteString(section("Cómo leer el resultado"))
	b.WriteString(lipgloss.NewStyle().Foreground(colorRunning).Render("✓") + itemStyle.Render(" conecta bien") + "\n")
	b.WriteString(modalErrStyle.Render("✗") + itemStyle.Render(" no pudo conectar; al lado dice por qué") + "\n")
	b.WriteString(lipgloss.NewStyle().Foreground(colorBusy).Render(spinnerFrames[0]) + itemStyle.Render(" probándose ahora") + "\n")
	b.WriteString(itemDimStyle.Render("·") + itemStyle.Render(" esperando su turno") + "\n")
	b.WriteString(itemDimStyle.Render("-") + itemStyle.Render(" omitido (se estaba iniciando o deteniendo)") + "\n")

	b.WriteString(section("Si uno falla"))
	b.WriteString(text("• Mensajes con \"token\", \"expired\" o \"SSO\": tu sesión de AWS venció."))
	b.WriteString(text("  Sal con esc, pulsa a y vuelve a iniciar sesión en esa cuenta."))
	b.WriteString(text("• \"is not connected\" o \"TargetNotConnected\": el perfil es de otra cuenta"))
	b.WriteString(text("  o la instancia está apagada. Revisa el perfil del túnel con p."))
	b.WriteString(text("• \"cerró la conexión\" o \"no respondió\": la instancia no llega al host;"))
	b.WriteString(text("  revisa el host y el puerto remoto del túnel con e."))
	b.WriteString(text("• \"puerto ... en uso\": otro programa ocupa ese puerto local."))
	b.WriteString(dim("  El detalle completo queda en los logs del túnel, marcado [VERIFICAR]."))

	b.WriteString("\n" + helpEntry("(cualquier tecla)", "volver al panel"))

	box := modalBoxStyle.Width(width + 6).Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}
