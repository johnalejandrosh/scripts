// Package prefs stores the small bits of state the user chooses in the UI and
// expects to still be there next time: right now, which AWS CLI profile each
// tunnel runs as. It lives in a plain JSON file the user can read or edit by
// hand; nothing secret goes in it, only profile names that already exist in
// ~/.aws/config.
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

// SaveTunnelProfiles persists the assignments, creating the directory if
// needed. It writes to a temp file and renames, so an interrupted write can't
// leave a half-written file behind.
func SaveTunnelProfiles(assignments map[string]string) error {
	path, err := Path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(file{TunnelProfiles: assignments}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, fileName+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
