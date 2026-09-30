package shellenv

import (
	"os/exec"
	"testing"
)

// An app opened from Finder starts with launchd's bare PATH; EnsurePATH has
// to find `aws` from there.
func TestEnsurePATHFromBarePath(t *testing.T) {
	if _, err := exec.LookPath("aws"); err != nil {
		t.Skip("aws no está instalado en esta máquina")
	}
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	if _, err := exec.LookPath("aws"); err == nil {
		t.Skip("aws está en el PATH mínimo; no hay nada que probar")
	}
	EnsurePATH()
	if _, err := exec.LookPath("aws"); err != nil {
		t.Fatalf("aws sigue sin encontrarse: %v", err)
	}
	if _, err := exec.LookPath("session-manager-plugin"); err != nil {
		t.Errorf("session-manager-plugin sin encontrarse: %v", err)
	}
}
