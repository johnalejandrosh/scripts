package main

import (
	"context"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"scriptstui/internal/agent"
	"scriptstui/internal/config"
	"scriptstui/internal/prefs"
	"scriptstui/internal/procman"
	"scriptstui/internal/store"
	"scriptstui/internal/tui"
)

func main() {
	// The same binary is the shared agent both UIs spawn and talk to.
	if len(os.Args) > 1 && os.Args[1] == agent.Arg {
		os.Exit(agent.Main(os.Args[2:]))
	}

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

	// Tunnels live in the agent, so the desktop app sees and drives the same
	// ones. Without it this UI still works, just on tunnels of its own.
	var backend tui.Backend
	if client, err := agent.Connect(dbPath); err == nil {
		backend = client
	} else {
		fmt.Fprintln(os.Stderr, "aviso:", err, "— los túneles no se compartirán con la interfaz gráfica")
		mgr := procman.NewManager(make(chan procman.Event, 4096))
		mgr.StartHealthMonitor(context.Background())
		backend = mgr
	}
	model, err := tui.NewModel(st, backend)
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
