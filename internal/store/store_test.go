package store

import (
	"path/filepath"
	"strings"
	"testing"

	"scriptstui/internal/config"
)

func TestSeedAndCRUD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	n, err := s.Seed(config.Services(), map[string]string{"db-balu": "mi-perfil"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Fatalf("sembrados %d, esperaba 6", n)
	}
	if again, err := s.Seed(config.Services(), nil); err != nil || again != 0 {
		t.Fatalf("re-seed = %d, %v; esperaba 0, nil", again, err)
	}

	list, _ := s.List()
	for _, tn := range list {
		svc := tn.Service()
		t.Logf("%-18s proy=%-8q port=%-5d profile=%-10q cmd=%s",
			tn.ID, tn.Proyecto, tn.LocalPort, tn.Profile, svc.Steps[0].CommandLine())
		if svc.LocalPort() != itoa(tn.LocalPort) {
			t.Errorf("%s: LocalPort() = %q", tn.ID, svc.LocalPort())
		}
	}

	// puerto duplicado
	dup := list[0]
	dup.ID = "otro"
	if err := s.Create(dup); err == nil {
		t.Error("esperaba error por puerto duplicado")
	} else {
		t.Logf("puerto duplicado -> %v", err)
	}
	// id duplicado
	same := list[0]
	same.LocalPort = 9999
	if err := s.Create(same); err == nil {
		t.Error("esperaba error por id duplicado")
	} else {
		t.Logf("id duplicado -> %v", err)
	}

	// update + connection strings + proyecto
	up := list[0]
	up.Proyecto = "BALU"
	up.ConnStringMac = "psql postgres://u@localhost:5437/db"
	up.ConnStringWin = "psql postgresql://u@127.0.0.1:5437/db"
	if err := s.Update(up); err != nil {
		t.Fatal(err)
	}
	back, _ := s.List()
	if back[0].Proyecto != "BALU" || back[0].ConnStringMac == "" || back[0].ConnStringWin == "" {
		t.Errorf("update no persistió: %+v", back[0])
	}

	if err := s.SetProfile(up.ID, "otro-perfil"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(up.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(up.ID); err != ErrNotFound {
		t.Errorf("segundo delete = %v, esperaba ErrNotFound", err)
	}
	fin, _ := s.List()
	if len(fin) != 5 {
		t.Errorf("quedaron %d, esperaba 5", len(fin))
	}
}

func itoa(i int) string {
	if i == 0 {
		return ""
	}
	b := []byte{}
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestGeneratedCommandStaysInSync covers the connection-string columns when
// they hold the tunnel's own SSM command: an edit has to carry them along,
// or 'C' hands someone a command that brings up the wrong port. Anything the
// user typed themselves must survive the same edit untouched.
func TestGeneratedCommandStaysInSync(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Seed(config.Services(), map[string]string{"db-balu": "perfil-viejo"}); err != nil {
		t.Fatal(err)
	}

	row, _ := s.Get("db-balu")
	row.ConnStringMac = row.CommandLine() // el comando SSM, como lo guardamos
	row.ConnStringWin = "cadena escrita a mano que nadie debe tocar"
	if err := s.Update(row); err != nil {
		t.Fatal(err)
	}

	// Cambiar el puerto arrastra el comando generado.
	row, _ = s.Get("db-balu")
	row.LocalPort = 5999
	if err := s.Update(row); err != nil {
		t.Fatal(err)
	}
	row, _ = s.Get("db-balu")
	if !strings.Contains(row.ConnStringMac, `"localPortNumber":["5999"]`) {
		t.Errorf("el comando guardado quedó viejo: %s", row.ConnStringMac)
	}
	if row.ConnStringMac != row.CommandLine() {
		t.Error("lo guardado no coincide con lo que la UI copiaría")
	}
	if row.ConnStringWin != "cadena escrita a mano que nadie debe tocar" {
		t.Errorf("se pisó una cadena escrita a mano: %q", row.ConnStringWin)
	}

	// Reasignar el perfil también.
	if err := s.SetProfile("db-balu", "perfil-nuevo"); err != nil {
		t.Fatal(err)
	}
	row, _ = s.Get("db-balu")
	if !strings.Contains(row.ConnStringMac, "--profile perfil-nuevo") {
		t.Errorf("el perfil no se propagó: %s", row.ConnStringMac)
	}
	if row.ConnStringWin != "cadena escrita a mano que nadie debe tocar" {
		t.Error("SetProfile pisó la cadena escrita a mano")
	}

	// Quitar el perfil deja el comando sin --profile, no con uno colgando.
	if err := s.SetProfile("db-balu", ""); err != nil {
		t.Fatal(err)
	}
	row, _ = s.Get("db-balu")
	if strings.Contains(row.ConnStringMac, "--profile") {
		t.Errorf("quedó un --profile huérfano: %s", row.ConnStringMac)
	}
}
