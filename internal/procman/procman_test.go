package procman

import (
	"testing"
	"time"

	"scriptstui/internal/config"
)

// crashing is a service whose only step dies right after starting.
func crashing(id string) config.Service {
	return config.Service{ID: id, Steps: []config.Step{{
		Kind: config.StepCommand, Label: "T", Command: "sh", Args: []string{"-c", "exit 1"},
		WaitAfterStartSeconds: 1,
	}}}
}

func nextStatus(t *testing.T, events <-chan Event, id string, within time.Duration) Status {
	t.Helper()
	timeout := time.After(within)
	for {
		select {
		case ev := <-events:
			if ev.ServiceID == id && ev.Kind == EventStatus {
				return ev.Status
			}
		case <-timeout:
			t.Fatalf("%s: no llegó otro estado en %s", id, within)
		}
	}
}

func TestFailedIsStoppedAfterDelay(t *testing.T) {
	events := make(chan Event, 256)
	m := NewManager(events)
	m.failedReset = 300 * time.Millisecond

	m.Start(crashing("a"))
	for _, want := range []Status{StatusStarting, StatusFailed, StatusStopped} {
		if got := nextStatus(t, events, "a", 5*time.Second); got != want {
			t.Fatalf("estado %v, esperaba %v", got, want)
		}
	}
}

func TestFailedResetSkipsRestartedService(t *testing.T) {
	events := make(chan Event, 256)
	m := NewManager(events)
	m.failedReset = time.Second

	m.Start(crashing("a"))
	nextStatus(t, events, "a", 5*time.Second) // iniciando
	nextStatus(t, events, "a", 5*time.Second) // falló
	time.Sleep(1600 * time.Millisecond)       // killAll's grace period

	// Restarted before the reset fires: the new session must not be
	// stopped by the old failure's timer.
	m.Start(config.Service{ID: "a", Steps: []config.Step{{
		Kind: config.StepCommand, Label: "T", Command: "sleep", Args: []string{"30"},
	}}})
	defer m.StopAll()
	nextStatus(t, events, "a", 5*time.Second) // iniciando
	if got := nextStatus(t, events, "a", 5*time.Second); got != StatusRunning {
		t.Fatalf("estado %v, esperaba activo", got)
	}
	select {
	case ev := <-events:
		if ev.Kind == EventStatus {
			t.Fatalf("el temporizador del fallo anterior cambió la sesión nueva a %v", ev.Status)
		}
	case <-time.After(1500 * time.Millisecond):
	}
}
