package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"scriptstui/internal/config"
	"scriptstui/internal/procman"
	"scriptstui/internal/store"
)

// The commands the checks return are never run here (send drops them), so no
// real session is opened: this only covers how many get launched at once.
func TestVerifyAsksThenRespectsMode(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Seed(config.Services(), nil); err != nil {
		t.Fatal(err)
	}
	m, err := NewModel(st, procman.NewManager(make(chan procman.Event, 16)))
	if err != nil {
		t.Fatal(err)
	}
	m = send(t, m, tea.WindowSizeMsg{Width: 160, Height: 40})

	m = send(t, m, key("t"))
	if m.mode != modeVerify || !m.verify.choosing || m.verify.parallel {
		t.Fatal("'V' debía preguntar el modo, con 'en cola' por defecto")
	}
	// '?' explains the panel; the next key only closes it, it must not
	// also pick an option.
	m = send(t, m, key("?"))
	if !m.verify.showHelp || !strings.Contains(m.View(), "En cola o en paralelo") {
		t.Fatal("'?' no mostró la ayuda del panel")
	}
	m = send(t, m, key("2"))
	if m.verify.showHelp || !m.verify.choosing || m.verify.parallel {
		t.Fatal("cerrar la ayuda no debía elegir un modo")
	}

	m = send(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.verify.parallel || m.verify.count(verifyChecking) != 1 {
		t.Fatalf("en cola: %d en curso, esperaba 1", m.verify.count(verifyChecking))
	}

	// Answering the one in flight moves the queue on to the next.
	first := m.verify
	m = send(t, m, verifyResultMsg{run: first, idx: 0})
	if first.items[0].state != verifyOK || first.items[1].state != verifyChecking {
		t.Fatal("el resultado no avanzó la cola al siguiente túnel")
	}

	m = send(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = send(t, m, key("t"))
	m = send(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = send(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.verify.parallel || m.verify.count(verifyChecking) != maxParallelProbes {
		t.Fatalf("en paralelo: %d en curso, esperaba %d", m.verify.count(verifyChecking), maxParallelProbes)
	}

	// A late result from the cancelled run must not touch the new one.
	m = send(t, m, verifyResultMsg{run: first, idx: 2})
	if m.verify.items[2].state != verifyChecking {
		t.Fatal("un resultado de una corrida vieja alteró la actual")
	}
}
