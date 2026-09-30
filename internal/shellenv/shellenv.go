// Package shellenv gives a process launched outside a terminal — a macOS app
// opened from Finder, or the agent it spawns — the PATH a terminal would
// have. launchd starts apps with /usr/bin:/bin:/usr/sbin:/sbin, where neither
// `aws` nor `session-manager-plugin` is usually found.
package shellenv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// commonDirs are where the AWS CLI and its session plugin are usually
// installed on macOS, tried when asking the login shell doesn't help.
var commonDirs = []string{"/usr/local/bin", "/opt/homebrew/bin", "/usr/local/sessionmanagerplugin/bin"}

// EnsurePATH makes `aws` findable when it isn't: first by taking the PATH of
// the user's login shell, then by adding the usual install directories. It
// leaves a PATH that already finds `aws` alone.
func EnsurePATH() {
	if _, err := exec.LookPath("aws"); err == nil {
		return
	}
	dirs := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))
	if shellPath := loginShellPATH(); shellPath != "" {
		dirs = append(strings.Split(shellPath, string(os.PathListSeparator)), dirs...)
	}
	if home, err := os.UserHomeDir(); err == nil {
		commonDirs = append(commonDirs, filepath.Join(home, ".local", "bin"))
	}
	dirs = append(dirs, commonDirs...)

	seen := make(map[string]bool, len(dirs))
	var out []string
	for _, d := range dirs {
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	os.Setenv("PATH", strings.Join(out, string(os.PathListSeparator)))
}

func loginShellPATH() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// A marker keeps whatever the profile prints from being read as PATH.
	out, err := exec.CommandContext(ctx, shell, "-l", "-c", `printf '\n__PATH__%s' "$PATH"`).Output()
	if err != nil {
		return ""
	}
	i := strings.LastIndex(string(out), "__PATH__")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(string(out)[i+len("__PATH__"):])
}
