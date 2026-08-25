// Package clipboard copies text to the system clipboard by piping it to the
// platform's own clipboard tool. Shelling out keeps this dependency-free, and
// the commands involved (pbcopy, wl-copy, xclip, xsel) ship with, or are
// standard on, their platforms.
package clipboard

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// candidates lists the clipboard commands to try, in order, for this platform.
func candidates() [][]string {
	switch runtime.GOOS {
	case "darwin":
		return [][]string{{"pbcopy"}}
	case "windows":
		return [][]string{{"clip"}}
	default:
		return [][]string{
			{"wl-copy"},
			{"xclip", "-selection", "clipboard"},
			{"xsel", "--clipboard", "--input"},
		}
	}
}

// Copy puts text on the system clipboard. It reports an error when no
// clipboard tool is available, so the caller can tell the user to select the
// text by hand instead of appearing to have copied something.
func Copy(text string) error {
	var missing []string
	for _, argv := range candidates() {
		path, err := exec.LookPath(argv[0])
		if err != nil {
			missing = append(missing, argv[0])
			continue
		}
		cmd := exec.Command(path, argv[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s falló: %w: %s", argv[0], err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if len(missing) == 0 {
		return errors.New("esta plataforma no tiene portapapeles")
	}
	return fmt.Errorf("no se encontró herramienta de portapapeles (%s)", strings.Join(missing, ", "))
}
