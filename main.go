package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"scriptstui/internal/config"
	"scriptstui/internal/procman"
	"scriptstui/internal/tui"
)

func main() {
	events := make(chan procman.Event, 4096)
	mgr := procman.NewManager(events)
	model := tui.NewModel(config.Services(), mgr)

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
