package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"scriptstui/internal/store"
)

// desktopAppPath is where `wails build` leaves the desktop app, relative to
// the project root that holds the tunnels database.
const desktopAppPath = "desktop/build/bin/scriptstui-desktop.app"

// openDesktopApp opens the desktop app and returns the line to show about
// it. Both UIs connect to the same agent, so it opens on the same tunnels.
func openDesktopApp() string {
	if runtime.GOOS != "darwin" {
		return "✗ la interfaz gráfica solo está disponible en macOS"
	}
	app := filepath.Join(filepath.Dir(store.DefaultPath()), desktopAppPath)
	if _, err := os.Stat(app); err != nil {
		return "✗ la interfaz gráfica no está compilada: cd desktop && wails build"
	}
	// `open` focuses the app when it is already running instead of
	// starting a second copy.
	if err := exec.Command("open", app).Run(); err != nil {
		return "✗ no se pudo abrir la interfaz gráfica: " + err.Error()
	}
	return "✓ abriendo la interfaz gráfica (comparte estos mismos túneles)"
}
