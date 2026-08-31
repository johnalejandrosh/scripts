package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"scriptstui/internal/config"
	"scriptstui/internal/prefs"
	"scriptstui/internal/procman"
	"scriptstui/internal/store"
	"scriptstui/internal/tui"
)

func main() {
	dbPath := store.DefaultPath()
	st, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer st.Close()

	// First run against an empty database: bring over the tunnels that used to
	// be literals in internal/config, plus whatever profiles the old JSON
	// prefs file had assigned. A database with rows is left untouched.
	if n, err := st.Seed(config.Services(), prefs.LoadTunnelProfiles()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	} else if n > 0 {
		fmt.Fprintf(os.Stderr, "Se crearon %d túneles en %s\n", n, dbPath)
	}

	events := make(chan procman.Event, 4096)
	mgr := procman.NewManager(events)
	model, err := tui.NewModel(st, mgr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
