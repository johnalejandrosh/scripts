package procman

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"scriptstui/internal/config"
)

// ProbeLabel prefixes the log lines a verification writes into the tunnel's
// own log, so they read apart from a normal start.
const ProbeLabel = "VERIFICAR"

const (
	// probeReadyTimeout is how long a session gets to open its local port —
	// an expired SSO token or a wrong instance id fails well before this.
	probeReadyTimeout = 30 * time.Second
	// probeReplyTimeout is how long the remote end gets to answer once a
	// connection has gone through the tunnel.
	probeReplyTimeout = 8 * time.Second
)

// postgresSSLRequest is the 8-byte message a Postgres (or Redshift) client
// opens with to ask for TLS. The server always answers it with one byte, 'S'
// or 'N', whatever its SSL settings — so it proves the remote database is
// really reachable without logging in.
var postgresSSLRequest = []byte{0, 0, 0, 8, 4, 210, 22, 47}

// Probe opens cfg's tunnel on its own, pushes one connection through it and
// tears it down again, reporting why it failed or nil if the remote end is
// reachable. It is meant for a tunnel that isn't running: its local port has
// to be free. The session's output goes to the tunnel's log under ProbeLabel.
func (m *Manager) Probe(ctx context.Context, cfg config.Service) error {
	step, ok := tunnelStep(cfg)
	if !ok {
		return errors.New("no tiene un paso de túnel")
	}
	if portInUse(step.LocalPort) {
		return fmt.Errorf("el puerto local %d ya está en uso por otro proceso", step.LocalPort)
	}

	var extraEnv []string
	if step.Profile == "" {
		extraEnv = m.awsCredEnv()
	}
	cmd := buildCmd(step, extraEnv)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("no se pudo iniciar: %v", err)
	}

	var (
		mu    sync.Mutex
		lines []string
	)
	ready := make(chan struct{})
	var readyOnce sync.Once
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			m.emitLog(cfg.ID, ProbeLabel, line)
			mu.Lock()
			lines = append(lines, line)
			mu.Unlock()
			if strings.Contains(line, "Waiting for connections") {
				readyOnce.Do(func() { close(ready) })
			}
		}
	}()

	var exitErr error
	exited := make(chan struct{})
	go func() {
		exitErr = cmd.Wait()
		pw.Close()
		close(exited)
	}()
	defer stopProbe(cmd, exited)

	output := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}

	select {
	case <-ready:
	case <-exited:
		<-scanned
		if last := lastLine(output()); last != "" {
			return errors.New(last)
		}
		return fmt.Errorf("la sesión terminó: %v", exitErr)
	case <-time.After(probeReadyTimeout):
		return fmt.Errorf("la sesión no abrió el puerto %d en %s", step.LocalPort, probeReadyTimeout)
	case <-ctx.Done():
		return ctx.Err()
	}

	checkErr := CheckPort(step.LocalPort, remotePort(step))
	// The plugin reports a remote end it couldn't reach on its own output,
	// sometimes before the connection is closed on us — so trust it over a
	// quiet socket.
	for _, line := range output() {
		if strings.Contains(strings.ToLower(line), "failed") {
			return errors.New(strings.TrimSpace(line))
		}
	}
	return checkErr
}

// CheckPort connects to a tunnel's local port and waits for proof that the
// connection reached the remote host. A Postgres/Redshift port is asked for
// its SSL answer, which it always gives; anything else passes unless the
// tunnel drops the connection. It works on a tunnel that is already running.
func CheckPort(localPort, remotePort int) error {
	_, _, err := CheckPortLatency(localPort, remotePort)
	return err
}

// CheckPortLatency is CheckPort that also reports the round trip to the
// remote end. measured is false for ports that aren't asked anything — their
// check only ends when the wait runs out, so the time says nothing.
func CheckPortLatency(localPort, remotePort int) (rtt time.Duration, measured bool, err error) {
	addr := fmt.Sprintf("127.0.0.1:%d", localPort)
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return 0, false, fmt.Errorf("no se pudo conectar a %s: %v", addr, err)
	}
	defer conn.Close()

	greeting := answersProbe(remotePort)
	if greeting {
		if _, err := conn.Write(postgresSSLRequest); err != nil {
			return 0, false, fmt.Errorf("el túnel cortó la conexión: %v", err)
		}
	}

	_ = conn.SetReadDeadline(time.Now().Add(probeReplyTimeout))
	_, err = conn.Read(make([]byte, 1))
	var netErr net.Error
	switch {
	case err == nil:
		return time.Since(start), greeting, nil
	case errors.As(err, &netErr) && netErr.Timeout():
		if greeting {
			return 0, false, fmt.Errorf("la base de datos no respondió en %s", probeReplyTimeout)
		}
		// Most servers wait for the client to speak first; a connection
		// the tunnel kept open is as much as can be asked of them.
		return 0, false, nil
	default:
		return 0, false, fmt.Errorf("el host remoto cerró la conexión (%v)", err)
	}
}

// answersProbe reports whether the service behind remotePort replies to
// postgresSSLRequest: Postgres (5432) and Redshift (5439) do.
func answersProbe(remotePort int) bool {
	return remotePort == 5432 || remotePort == 5439
}

// RemotePort returns the port on the remote host that cfg's tunnel forwards
// to, or 0 when it has no port-forwarding step.
func RemotePort(cfg config.Service) int {
	step, ok := tunnelStep(cfg)
	if !ok {
		return 0
	}
	return remotePort(step)
}

func remotePort(step config.Step) int {
	params, ok := config.TunnelParamsOf(step)
	if !ok {
		return 0
	}
	return params.RemotePort
}

func tunnelStep(cfg config.Service) (config.Step, bool) {
	for _, step := range cfg.Steps {
		if step.Kind == config.StepTunnel && step.LocalPort > 0 {
			return step, true
		}
	}
	return config.Step{}, false
}

func portInUse(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return true
	}
	l.Close()
	return false
}

// stopProbe ends the probe's session the same way killAll ends a tunnel,
// but returns as soon as it has exited instead of always waiting out the
// grace period — a verification of every tunnel would otherwise crawl.
func stopProbe(cmd *exec.Cmd, exited <-chan struct{}) {
	signalGroup(cmd, syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(1500 * time.Millisecond):
		signalGroup(cmd, syscall.SIGKILL)
		<-exited
	}
}

func lastLine(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}
