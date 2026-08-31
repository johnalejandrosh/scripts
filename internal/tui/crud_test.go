package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"scriptstui/internal/config"
	"scriptstui/internal/procman"
	"scriptstui/internal/store"
)

func key(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func send(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(Model)
}

func TestTunnelCRUDFromTUI(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Seed(config.Services(), nil); err != nil {
		t.Fatal(err)
	}

	mgr := procman.NewManager(make(chan procman.Event, 16))
	m, err := NewModel(st, mgr)
	if err != nil {
		t.Fatal(err)
	}
	m = send(t, m, tea.WindowSizeMsg{Width: 160, Height: 40})
	if len(m.services) != 6 {
		t.Fatalf("cargados %d túneles, esperaba 6", len(m.services))
	}

	// --- CREATE ---------------------------------------------------------
	m = send(t, m, key("n"))
	if m.mode != modeTunnelForm || m.tf == nil || m.tf.editing {
		t.Fatal("'n' no abrió el formulario de alta")
	}
	m.tf.inputs[tfID].SetValue("mi-tunel")
	m.tf.inputs[tfProyecto].SetValue("CNE")
	m.tf.inputs[tfTitle].SetValue("Túnel de prueba")
	m.tf.inputs[tfTarget].SetValue("i-0abc123")
	m.tf.inputs[tfHost].SetValue("db.us-east-1.rds.amazonaws.com")
	m.tf.inputs[tfRemotePort].SetValue("5432")
	m.tf.inputs[tfConnMac].SetValue("psql postgres://u@localhost:5499/db")
	m.tf.inputs[tfConnWin].SetValue("psql postgresql://u@127.0.0.1:5499/db")

	// puerto local vacío -> el formulario se queda abierto con el error
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.mode != modeTunnelForm || m.tf.err == "" {
		t.Fatal("esperaba que rechazara el puerto local vacío")
	}
	t.Logf("validación puerto vacío -> %s", m.tf.err)

	// puerto ya usado por el túnel sembrado en 5437
	m.tf.inputs[tfLocalPort].SetValue("5437")
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.mode != modeTunnelForm || !strings.Contains(m.tf.err, "5437") {
		t.Fatalf("esperaba choque de puerto, err=%q", m.tf.err)
	}
	t.Logf("validación puerto duplicado -> %s", m.tf.err)

	m.tf.inputs[tfLocalPort].SetValue("5499")
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.mode != modeList {
		t.Fatalf("no guardó: %s", m.tf.err)
	}
	if len(m.services) != 7 {
		t.Fatalf("quedaron %d túneles, esperaba 7", len(m.services))
	}
	i, ok := m.index["mi-tunel"]
	if !ok || m.cursor != i {
		t.Fatal("el cursor no quedó sobre el túnel recién creado")
	}
	created := m.services[i]
	if got := created.cfg.LocalPort(); got != "5499" {
		t.Errorf("LocalPort() = %q, esperaba 5499", got)
	}
	if !strings.Contains(created.cfg.Steps[0].CommandLine(), `"localPortNumber":["5499"]`) {
		t.Errorf("el comando no refleja el puerto: %s", created.cfg.Steps[0].CommandLine())
	}
	t.Logf("creado -> %s", created.cfg.Steps[0].CommandLine())

	// la columna proyecto aparece sola en cuanto se usa
	if m.projectFieldWidth() == 0 {
		t.Error("esperaba que la columna proyecto se activara")
	}

	// --- READ (persistencia real) ---------------------------------------
	row, err := st.Get("mi-tunel")
	if err != nil {
		t.Fatal(err)
	}
	if row.Proyecto != "CNE" || row.ConnStringMac == "" || row.ConnStringWin == "" {
		t.Errorf("no persistieron proyecto/connection strings: %+v", row)
	}

	// --- UPDATE ---------------------------------------------------------
	m = send(t, m, key("e"))
	if m.mode != modeTunnelForm || !m.tf.editing {
		t.Fatal("'e' no abrió el formulario de edición")
	}
	if m.tf.inputs[tfTitle].Value() != "Túnel de prueba" {
		t.Errorf("el formulario no se precargó: %q", m.tf.inputs[tfTitle].Value())
	}
	if m.tf.focus == tfID {
		t.Error("el id no debería recibir foco al editar")
	}
	m.tf.inputs[tfTitle].SetValue("Túnel renombrado")
	m.tf.inputs[tfLocalPort].SetValue("5500")
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.mode != modeList {
		t.Fatalf("no actualizó: %s", m.tf.err)
	}
	row, _ = st.Get("mi-tunel")
	if row.Title != "Túnel renombrado" || row.LocalPort != 5500 {
		t.Errorf("update no persistió: %+v", row)
	}

	// desactivar libera el puerto y bloquea el arranque
	m = send(t, m, key("e"))
	m.tf.focus = tfEnabled
	m = send(t, m, key(" "))
	if m.tf.enabled {
		t.Error("espacio no desactivó el túnel")
	}
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.mode != modeList {
		t.Fatalf("no guardó el desactivado: %s", m.tf.err)
	}
	if m.services[m.index["mi-tunel"]].row.Enabled {
		t.Error("el túnel debería quedar desactivado")
	}

	// --- DELETE ---------------------------------------------------------
	m = send(t, m, key("D"))
	if m.mode != modeTunnelDelete || m.deleteTarget != "mi-tunel" {
		t.Fatal("'D' no abrió la confirmación de borrado")
	}
	m = send(t, m, key("n")) // cancelar
	if m.mode != modeList || len(m.services) != 7 {
		t.Fatal("cancelar el borrado no debió eliminar nada")
	}
	m = send(t, m, key("D"))
	m = send(t, m, key("s")) // confirmar
	if m.mode != modeList {
		t.Fatal("no volvió a la lista tras borrar")
	}
	if len(m.services) != 6 {
		t.Fatalf("quedaron %d túneles, esperaba 6", len(m.services))
	}
	if _, err := st.Get("mi-tunel"); err != store.ErrNotFound {
		t.Errorf("el túnel sigue en la base: %v", err)
	}
	if m.storeErr != "" {
		t.Errorf("storeErr = %q", m.storeErr)
	}

	// la vista sigue renderizando sin panics tras cada operación
	if out := m.View(); !strings.Contains(out, "gestor de túneles") {
		t.Error("la lista no renderizó")
	}
}

// TestEmptyDatabase covers the state the list can now reach: no tunnels at all.
func TestEmptyDatabase(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	m, err := NewModel(st, procman.NewManager(make(chan procman.Event, 16)))
	if err != nil {
		t.Fatal(err)
	}
	m = send(t, m, tea.WindowSizeMsg{Width: 160, Height: 40})

	if m.selected() != nil {
		t.Error("selected() debería ser nil sin túneles")
	}
	// ninguna de estas debe entrar en pánico con la lista vacía
	for _, k := range []string{"e", "D", "p", "y", " ", "s", "x"} {
		m = send(t, m, key(k))
	}
	out := m.View()
	if !strings.Contains(out, "No hay túneles") {
		t.Errorf("esperaba el mensaje de lista vacía, obtuve:\n%s", out)
	}
}

// TestCopyConnectionString covers the 'C' key: the tunnel list and the log
// panel share every screen row, so a mouse drag can never select the
// connection string on its own — the keyboard has to be able to.
func TestCopyConnectionString(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Seed(config.Services(), nil); err != nil {
		t.Fatal(err)
	}

	m, err := NewModel(st, procman.NewManager(make(chan procman.Event, 16)))
	if err != nil {
		t.Fatal(err)
	}
	m = send(t, m, tea.WindowSizeMsg{Width: 160, Height: 40})

	// Sin cadena definida: avisa y señala el editor, en vez de copiar vacío.
	m = send(t, m, key("C"))
	if !strings.Contains(m.copyStatus, "sin cadena de conexión") {
		t.Errorf("copyStatus = %q", m.copyStatus)
	}
	if !strings.Contains(m.View(), "sin definir") {
		t.Error("el panel debería avisar que no hay cadena definida")
	}

	// Con cadena definida: se muestra y se copia.
	const conn = "psql postgres://usuario@localhost:5437/balu"
	row, _ := st.Get("db-balu")
	row.ConnStringMac, row.ConnStringWin = conn, "windows-conn"
	if err := st.Update(row); err != nil {
		t.Fatal(err)
	}
	if err := m.reloadTunnels(); err != nil {
		t.Fatal(err)
	}
	m.cursor = m.index["db-balu"]
	m.layout()

	if !strings.Contains(m.View(), "conexión (") {
		t.Error("el panel no muestra la etiqueta de conexión")
	}
	if !strings.Contains(m.View(), "postgres://usuario@localhost:5437/balu") {
		t.Errorf("el panel no muestra la cadena:\n%s", m.View())
	}

	m = send(t, m, key("C"))
	// Copy() can legitimately fail on a machine with no clipboard; what must
	// never happen is a silent no-op or a claim that an empty string was copied.
	if strings.Contains(m.copyStatus, "sin cadena") || m.copyStatus == "" {
		t.Errorf("copyStatus = %q", m.copyStatus)
	}
	t.Logf("copyStatus = %q", m.copyStatus)

	// El comando aws sigue teniendo su propia tecla.
	m = send(t, m, key("y"))
	t.Logf("y -> %q", m.copyStatus)
}

// TestLogPanelAlignment guards the log panel's height math. Its contents are
// laid out at the viewport's width but drawn inside a box that is narrower by
// its borders and padding, so measuring at the wrong width made wrapped lines
// come out short and pushed the bottom border off the panel.
func TestLogPanelAlignment(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Seed(config.Services(), nil); err != nil {
		t.Fatal(err)
	}

	long := "psql 'host=localhost port=5439 user=admin dbname=balu_prod sslmode=require options=--search_path=public'"
	for _, conn := range []string{"", long} {
		rows, _ := st.List()
		for _, r := range rows {
			r.Proyecto = "BALU" // widens the list panel too
			r.ConnStringMac, r.ConnStringWin = conn, conn
			if err := st.Update(r); err != nil {
				t.Fatal(err)
			}
		}

		m, err := NewModel(st, procman.NewManager(make(chan procman.Event, 16)))
		if err != nil {
			t.Fatal(err)
		}
		for _, size := range []tea.WindowSizeMsg{
			{Width: 100, Height: 20}, {Width: 120, Height: 24},
			{Width: 150, Height: 26}, {Width: 200, Height: 40},
			{Width: 90, Height: 14},
		} {
			m = send(t, m, size)
			for _, cursor := range []int{0, len(m.services) - 1} {
				m.cursor = cursor
				m.layout()

				var bottoms []string
				for _, line := range strings.Split(m.View(), "\n") {
					if strings.Contains(line, "╰") {
						bottoms = append(bottoms, line)
					}
				}
				if len(bottoms) != 1 {
					t.Fatalf("conn=%q %dx%d cursor=%d: %d filas con borde inferior, esperaba 1",
						conn, size.Width, size.Height, cursor, len(bottoms))
				}
				if n := strings.Count(bottoms[0], "╰"); n != 2 {
					t.Errorf("conn=%q %dx%d cursor=%d: los dos paneles no cierran en la misma fila (%d bordes)\n%s",
						conn, size.Width, size.Height, cursor, n, bottoms[0])
				}
			}
		}
	}
}
