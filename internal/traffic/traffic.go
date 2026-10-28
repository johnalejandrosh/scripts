// Package traffic measures how much each running tunnel is moving. A tunnel
// is a session-manager-plugin listening on the local port; what it sends to
// and receives from AWS (its non-loopback connections) is the bandwidth the
// tunnel really uses. macOS reports that per connection through nettop, and
// lsof says which plugin process listens on which port.
package traffic

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Stats is one tunnel's traffic, keyed by its local port in Sample's result.
type Stats struct {
	// RateIn/RateOut are bytes per second over the last sampling interval;
	// HasRate is false on the first sample of a session, which has nothing
	// to compare against.
	RateIn, RateOut float64
	HasRate         bool
	// TotalIn/TotalOut are the bytes moved since the session started.
	TotalIn, TotalOut uint64
}

type bytesInOut struct{ in, out uint64 }

// Sampler keeps what the previous Sample saw, to turn nettop's per-connection
// byte counters into rates and running totals. Safe for concurrent use.
type Sampler struct {
	mu     sync.Mutex
	at     time.Time
	pid    map[int]int                   // port -> plugin pid of the session being tracked
	flows  map[int]map[string]bytesInOut // pid -> connection -> counters last seen
	totals map[int]bytesInOut            // port -> bytes since the session started
}

func NewSampler() *Sampler {
	return &Sampler{
		pid:    make(map[int]int),
		flows:  make(map[int]map[string]bytesInOut),
		totals: make(map[int]bytesInOut),
	}
}

// Sample measures every port in ports that has a tunnel listening on it.
// Ports with nothing listening are left out of the result.
func (s *Sampler) Sample(ctx context.Context, ports []int) (map[int]Stats, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	lsofOut, err := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-a", "-c", "session-m", "-F", "pn").Output()
	if err != nil && len(lsofOut) == 0 {
		// lsof exits 1 when nothing matches: no tunnel is listening.
		return map[int]Stats{}, nil
	}
	listeners := parseListeners(string(lsofOut))

	nettopOut, err := exec.CommandContext(ctx, "nettop", "-L", "1", "-x", "-n", "-J", "bytes_in,bytes_out").Output()
	if err != nil {
		return nil, fmt.Errorf("nettop: %w", err)
	}
	flows := parseNettop(string(nettopOut))

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(s.at).Seconds()
	s.at = now

	out := make(map[int]Stats, len(ports))
	seenPIDs := make(map[int]bool)
	for _, port := range ports {
		pid, ok := listeners[port]
		if !ok {
			delete(s.pid, port)
			delete(s.totals, port)
			continue
		}
		seenPIDs[pid] = true

		// A new plugin process on the port is a new session: start over.
		continuing := s.pid[port] == pid
		if !continuing {
			s.pid[port] = pid
			s.totals[port] = bytesInOut{}
			delete(s.flows, pid)
		}

		prev := s.flows[pid]
		cur := flows[pid]
		var delta bytesInOut
		for key, c := range cur {
			p, known := prev[key]
			if !known || c.in < p.in || c.out < p.out {
				p = bytesInOut{} // a connection opened since the last sample
			}
			delta.in += c.in - p.in
			delta.out += c.out - p.out
		}
		s.flows[pid] = cur

		total := s.totals[port]
		total.in += delta.in
		total.out += delta.out
		s.totals[port] = total

		st := Stats{TotalIn: total.in, TotalOut: total.out}
		if continuing && elapsed > 0 {
			st.HasRate = true
			st.RateIn = float64(delta.in) / elapsed
			st.RateOut = float64(delta.out) / elapsed
		}
		out[port] = st
	}
	for pid := range s.flows {
		if !seenPIDs[pid] {
			delete(s.flows, pid)
		}
	}
	return out, nil
}

// parseListeners reads `lsof -F pn` output into port -> pid.
func parseListeners(out string) map[int]int {
	ports := make(map[int]int)
	pid := 0
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			i := strings.LastIndex(line, ":")
			if port, err := strconv.Atoi(line[i+1:]); err == nil && pid > 0 {
				ports[port] = pid
			}
		}
	}
	return ports
}

// parseNettop reads `nettop -x -J bytes_in,bytes_out` CSV into
// pid -> connection -> counters, keeping only the connections that leave the
// machine. Process rows look like "name.pid,in,out,", connection rows like
// "tcp4 10.0.0.2:5366<->3.4.5.6:443,in,out," right below their process.
func parseNettop(out string) map[int]map[string]bytesInOut {
	flows := make(map[int]map[string]bytesInOut)
	pid := 0
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(line, ",")
		if len(fields) < 3 || fields[0] == "" {
			continue
		}
		name := fields[0]
		if !strings.HasPrefix(name, "tcp") && !strings.HasPrefix(name, "udp") {
			pid = 0
			if i := strings.LastIndex(name, "."); i >= 0 {
				pid, _ = strconv.Atoi(name[i+1:])
			}
			continue
		}
		if pid == 0 || isLocal(name) {
			continue
		}
		in, errIn := strconv.ParseUint(fields[1], 10, 64)
		outBytes, errOut := strconv.ParseUint(fields[2], 10, 64)
		if errIn != nil || errOut != nil {
			continue
		}
		if flows[pid] == nil {
			flows[pid] = make(map[string]bytesInOut)
		}
		flows[pid][name] = bytesInOut{in: in, out: outBytes}
	}
	return flows
}

// isLocal is true for listening sockets and loopback connections: the
// client's side of the tunnel, which would count the same bytes twice.
func isLocal(conn string) bool {
	_, peers, ok := strings.Cut(conn, " ")
	if !ok {
		return true
	}
	_, remote, ok := strings.Cut(peers, "<->")
	if !ok || remote == "*:*" || remote == "*.*" {
		return true
	}
	return strings.HasPrefix(remote, "127.") || strings.HasPrefix(remote, "::1") ||
		strings.HasPrefix(remote, "[::1]") || strings.HasPrefix(remote, "localhost")
}

// FormatBytes writes n compactly in at most 4 characters plus unit:
// 512B, 1.2K, 34K, 1.2M, 120M, 3.4G.
func FormatBytes(n float64) string {
	units := []string{"B", "K", "M", "G", "T"}
	i := 0
	for n >= 1000 && i < len(units)-1 {
		n /= 1000
		i++
	}
	switch {
	case i == 0:
		return fmt.Sprintf("%.0f%s", n, units[i])
	case n < 10:
		return fmt.Sprintf("%.1f%s", n, units[i])
	default:
		return fmt.Sprintf("%.0f%s", n, units[i])
	}
}
