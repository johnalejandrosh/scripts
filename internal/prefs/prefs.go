// Package prefs reads the JSON file that used to hold which AWS CLI profile
// each tunnel runs as. That assignment now lives in the profile column of the
// tunnels table (see internal/store), so this package is only the migration
// path: it is read once, to carry the old choices into a fresh database, and
// nothing writes to it any more. The file itself is left in place as a
// backup, and never held anything secret — only profile names that already
// exist in ~/.aws/config.
package prefs

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	appDir   = "scriptstui"
	fileName = "tunnel-profiles.json"
)

// file is the on-disk shape. Keyed by config.Service.ID.
type file struct {
	TunnelProfiles map[string]string `json:"tunnelProfiles"`
}

// Path returns the preferences file location: $XDG_CONFIG_HOME/scriptstui/
// tunnel-profiles.json, falling back to ~/.config/scriptstui/.
func Path() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, appDir, fileName), nil
}

// LoadTunnelProfiles reads the saved service ID -> AWS profile assignments.
// A missing or unreadable file is not an error: the user simply hasn't chosen
// anything yet, so it returns an empty (but usable) map.
func LoadTunnelProfiles() map[string]string {
	out := make(map[string]string)

	path, err := Path()
	if err != nil {
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return out
	}
	for id, profile := range f.TunnelProfiles {
		if profile != "" {
			out[id] = profile
		}
	}
	return out
}
