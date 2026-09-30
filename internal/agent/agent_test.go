package agent

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"scriptstui/internal/config"
	"scriptstui/internal/procman"
)

// startAgent runs an agent on a fresh socket and returns its path plus a
// channel closed when Serve returns. Unix socket paths are short on macOS,
// so it lives under /tmp rather than t.TempDir().
func startAgent(t *testing.T, linger time.Duration) (string, <-chan struct{}) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "a.sock")

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := Serve(socket, Options{Linger: linger}); err != nil {
			t.Error(err)
		}
	}()
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("unix", socket); err == nil {
			c.Close()
			return socket, done
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("el agente no abrió el socket")
	return "", nil
}

func connect(t *testing.T, socket string) *Client {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Dial(conn)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// waitFor reads c's events until one matches, or fails after a while.
func waitFor(t *testing.T, c *Client, what string, match func(procman.Event) bool) procman.Event {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-c.Events():
			if match(ev) {
				return ev
			}
		case <-timeout:
			t.Fatalf("no llegó: %s", what)
		}
	}
}

func status(id string, s procman.Status) func(procman.Event) bool {
	return func(ev procman.Event) bool {
		return ev.ServiceID == id && ev.Kind == procman.EventStatus && ev.Status == s
	}
}

// fakeTunnel stands in for an SSM session: it prints a line and stays up.
func fakeTunnel(id string) config.Service {
	return config.Service{ID: id, Title: id, Steps: []config.Step{{
		Kind: config.StepCommand, Label: "T", Command: "sh", Args: []string{"-c", "echo listo; sleep 30"},
	}}}
}

func TestSharedAcrossFrontEnds(t *testing.T) {
	socket, _ := startAgent(t, time.Minute)
	tui := connect(t, socket)
	defer tui.Shutdown()
	gui := connect(t, socket)
	defer gui.Shutdown()

	// Started from one front end, the other sees it running.
	tui.Start(fakeTunnel("db"))
	running := waitFor(t, gui, "activo en la otra interfaz", status("db", procman.StatusRunning))
	if running.Since.IsZero() {
		t.Fatal("el estado no trae desde cuándo")
	}
	waitFor(t, gui, "log del túnel", func(ev procman.Event) bool {
		return ev.ServiceID == "db" && ev.Kind == procman.EventLog && ev.Text == "listo"
	})

	// A front end that opens later gets the same state and uptime.
	late := connect(t, socket)
	defer late.Shutdown()
	waitFor(t, late, "log en la foto inicial", func(ev procman.Event) bool {
		return ev.ServiceID == "db" && ev.Kind == procman.EventLog && ev.Text == "listo"
	})
	snap := waitFor(t, late, "estado en la foto inicial", status("db", procman.StatusRunning))
	if !snap.Since.Equal(running.Since) {
		t.Fatalf("tiempo encendido distinto: %v vs %v", snap.Since, running.Since)
	}

	// Stopped from another front end, it stops for everyone.
	gui.Stop("db")
	waitFor(t, tui, "detenido en la interfaz que lo inició", status("db", procman.StatusStopped))

	// A database change reaches the others, not the one that made it.
	tui.NotifyChanged()
	waitFor(t, gui, "aviso de recarga", func(ev procman.Event) bool { return ev.Kind == procman.EventReload })
	select {
	case ev := <-tui.Events():
		if ev.Kind == procman.EventReload {
			t.Fatal("quien hizo el cambio no debía recibir el aviso")
		}
	case <-time.After(200 * time.Millisecond):
	}
}

func TestStopsTunnelsAfterLastFrontEnd(t *testing.T) {
	socket, done := startAgent(t, 200*time.Millisecond)
	a := connect(t, socket)
	b := connect(t, socket)
	a.Start(fakeTunnel("db"))
	waitFor(t, b, "activo", status("db", procman.StatusRunning))

	// One front end leaving keeps the tunnel up for the other.
	a.Shutdown()
	time.Sleep(500 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("el agente se cerró con una interfaz todavía conectada")
	default:
	}

	// The last one leaving stops everything and ends the agent.
	b.Shutdown()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("el agente siguió vivo sin interfaces")
	}
}

func TestRejectsOtherProtocolVersion(t *testing.T) {
	socket, _ := startAgent(t, time.Minute)
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	json.NewEncoder(conn).Encode(request{ID: 1, Op: opHello, Version: ProtocolVersion + 1})
	var msg message
	if err := json.NewDecoder(conn).Decode(&msg); err != nil {
		t.Fatal(err)
	}
	if msg.Err == "" {
		t.Fatal("aceptó una interfaz de otra versión")
	}
}

func TestQuitAllStopsEverythingAndClosesOthers(t *testing.T) {
	socket, done := startAgent(t, time.Minute)
	tui := connect(t, socket)
	gui := connect(t, socket)
	tui.Start(fakeTunnel("db"))
	waitFor(t, gui, "activo", status("db", procman.StatusRunning))

	tui.QuitAll()
	waitFor(t, gui, "aviso de cerrar", func(ev procman.Event) bool { return ev.Kind == procman.EventQuit })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("el agente siguió vivo tras salir de todo")
	}
	// The fake tunnel's shell is gone with it.
	if out, _ := exec.Command("pgrep", "-f", "echo listo; sleep 30").Output(); len(out) > 0 {
		t.Fatalf("quedó el proceso del túnel: %s", out)
	}
}
