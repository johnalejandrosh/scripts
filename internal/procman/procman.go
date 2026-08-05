// Package procman starts, supervises and stops the multi-step service
// definitions from internal/config, streaming their combined logs and
// status changes as Events for a UI (or anything else) to consume.
package procman

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"scriptstui/internal/config"
)

type Status int

const (
	StatusStopped Status = iota
	StatusStarting
	StatusRunning
	StatusStopping
	StatusFailed
)

func (s Status) String() string {
	switch s {
	case StatusStopped:
		return "detenido"
	case StatusStarting:
		return "iniciando"
	case StatusRunning:
		return "activo"
	case StatusStopping:
		return "deteniendo"
	case StatusFailed:
		return "falló"
	default:
		return "?"
	}
}

type EventKind int

const (
	EventLog EventKind = iota
	EventStatus
)

// Event is sent for every log line produced by a step, and every time a
// service's overall status changes.
type Event struct {
	ServiceID string
	Kind      EventKind
	Label     string
	Text      string
	Status    Status
}

type runningService struct {
	mu       sync.Mutex
	cfg      config.Service
	cmds     []*exec.Cmd
	stopping bool
}

type Manager struct {
	events chan Event

	mu       sync.Mutex
	services map[string]*runningService

	credMu sync.RWMutex
	awsEnv []string // AWS_* overrides for tunnel steps; nil means "use ambient env"
}

func NewManager(events chan Event) *Manager {
	return &Manager{
		events:   events,
		services: make(map[string]*runningService),
	}
}

func (m *Manager) Events() <-chan Event {
	return m.events
}

// SetAWSCredentials makes every subsequent tunnel step use these credentials
// (as env vars) instead of whatever is ambient. Nothing is written to disk;
// it only lives for the lifetime of this Manager.
func (m *Manager) SetAWSCredentials(env []string) {
	m.credMu.Lock()
	m.awsEnv = env
	m.credMu.Unlock()
}

func (m *Manager) awsCredEnv() []string {
	m.credMu.RLock()
	defer m.credMu.RUnlock()
	return append([]string(nil), m.awsEnv...)
}

func (m *Manager) emit(ev Event) {
	select {
	case m.events <- ev:
	default:
		// Drop rather than block the caller if the UI has fallen far behind.
	}
}

func (m *Manager) emitLog(id, label, text string) {
	m.emit(Event{ServiceID: id, Kind: EventLog, Label: label, Text: text})
}

func (m *Manager) setStatus(id string, s Status) {
	m.emit(Event{ServiceID: id, Kind: EventStatus, Status: s})
}

// Start launches every step of cfg in order, same semantics as the original
// bash scripts: each step gets a fixed grace period to prove it didn't die
// immediately before the next one starts.
func (m *Manager) Start(cfg config.Service) {
	m.mu.Lock()
	if existing := m.services[cfg.ID]; existing != nil && !existing.stopping {
		existing.mu.Lock()
		busy := len(existing.cmds) > 0
		existing.mu.Unlock()
		if busy {
			m.mu.Unlock()
			return
		}
	}
	rs := &runningService{cfg: cfg}
	m.services[cfg.ID] = rs
	m.mu.Unlock()

	m.setStatus(cfg.ID, StatusStarting)
	go m.run(rs)
}

func (m *Manager) run(rs *runningService) {
	id := rs.cfg.ID

	for _, step := range rs.cfg.Steps {
		var extraEnv []string
		if step.Kind == config.StepTunnel && step.Profile == "" {
			extraEnv = m.awsCredEnv()
		}
		cmd := buildCmd(step, extraEnv)

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			m.emitLog(id, step.Label, "error creando stdout pipe: "+err.Error())
			m.failAndStop(rs, id)
			return
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			m.emitLog(id, step.Label, "error creando stderr pipe: "+err.Error())
			m.failAndStop(rs, id)
			return
		}

		if err := cmd.Start(); err != nil {
			m.emitLog(id, step.Label, "no se pudo iniciar: "+err.Error())
			m.failAndStop(rs, id)
			return
		}

		rs.mu.Lock()
		rs.cmds = append(rs.cmds, cmd)
		rs.mu.Unlock()

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); m.scan(id, step.Label, stdout) }()
		go func() { defer wg.Done(); m.scan(id, step.Label, stderr) }()

		exited := make(chan error, 1)
		go func() {
			wg.Wait()
			exited <- cmd.Wait()
		}()

		if step.WaitAfterStartSeconds > 0 {
			select {
			case err := <-exited:
				m.emitLog(id, step.Label, fmt.Sprintf("el proceso terminó antes de tiempo: %v", err))
				m.failAndStop(rs, id)
				return
			case <-time.After(time.Duration(step.WaitAfterStartSeconds) * time.Second):
			}
		}

		go m.watchExit(rs, id, step.Label, exited)
	}

	m.setStatus(id, StatusRunning)
}

func (m *Manager) watchExit(rs *runningService, id, label string, exited <-chan error) {
	err := <-exited

	rs.mu.Lock()
	stopping := rs.stopping
	rs.mu.Unlock()
	if stopping {
		return
	}

	if err != nil {
		m.emitLog(id, label, fmt.Sprintf("proceso terminado inesperadamente: %v", err))
	} else {
		m.emitLog(id, label, "proceso terminado inesperadamente")
	}
	m.failAndStop(rs, id)
}

func (m *Manager) failAndStop(rs *runningService, id string) {
	m.setStatus(id, StatusFailed)
	m.killAll(rs)
}

// Stop terminates every process belonging to the service, if any.
func (m *Manager) Stop(id string) {
	m.mu.Lock()
	rs := m.services[id]
	m.mu.Unlock()
	if rs == nil {
		return
	}
	m.setStatus(id, StatusStopping)
	m.killAll(rs)
	m.setStatus(id, StatusStopped)
}

// StopAll synchronously stops every service that is currently running. Meant
// to be called once, right before the program exits, so no aws/npm processes
// are left behind.
func (m *Manager) StopAll() {
	m.mu.Lock()
	all := make([]*runningService, 0, len(m.services))
	for _, rs := range m.services {
		all = append(all, rs)
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, rs := range all {
		rs := rs
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.killAll(rs)
		}()
	}
	wg.Wait()
}

func (m *Manager) killAll(rs *runningService) {
	rs.mu.Lock()
	rs.stopping = true
	cmds := append([]*exec.Cmd(nil), rs.cmds...)
	rs.mu.Unlock()

	for _, cmd := range cmds {
		signalGroup(cmd, syscall.SIGTERM)
	}
	time.Sleep(1500 * time.Millisecond)
	for _, cmd := range cmds {
		signalGroup(cmd, syscall.SIGKILL)
	}
}

func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Signal(sig)
		return
	}
	_ = syscall.Kill(-pgid, sig)
}

func (m *Manager) scan(id, label string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		m.emitLog(id, label, scanner.Text())
	}
}

func buildCmd(step config.Step, extraEnv []string) *exec.Cmd {
	args := step.Args
	if step.Profile != "" {
		args = make([]string, len(step.Args)+2)
		copy(args, step.Args)
		args[len(step.Args)] = "--profile"
		args[len(step.Args)+1] = step.Profile
	}

	cmd := exec.Command(step.Command, args...)
	if step.Dir != "" {
		cmd.Dir = os.ExpandEnv(step.Dir)
	}

	switch {
	case step.Profile != "":
		// An explicit --profile is set: strip any pasted static creds from
		// the environment so they can't shadow the SSO profile's own creds.
		cmd.Env = withoutAWSStaticCreds(os.Environ())
	case len(extraEnv) > 0:
		cmd.Env = append(os.Environ(), extraEnv...)
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

func withoutAWSStaticCreds(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "AWS_ACCESS_KEY_ID=") ||
			strings.HasPrefix(kv, "AWS_SECRET_ACCESS_KEY=") ||
			strings.HasPrefix(kv, "AWS_SESSION_TOKEN=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
