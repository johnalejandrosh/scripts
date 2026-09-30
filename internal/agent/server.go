package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"scriptstui/internal/procman"
)

const (
	// logReplay is how much of each tunnel's log a front end gets when it
	// connects, so it shows what happened before it opened.
	logReplay = 500
	// clientQueue is how many messages may wait for a slow front end before
	// the newest are dropped, as procman does with its own events.
	clientQueue = 4096
	// defaultLinger is how long the agent waits after the last front end
	// leaves before stopping the tunnels: long enough to survive a front end
	// being restarted, short enough that quitting still stops them.
	defaultLinger = 5 * time.Second
	// startupWait is how long a freshly spawned agent waits for its first
	// front end before giving up.
	startupWait = 20 * time.Second
)

// tunnelState is what the agent remembers of one tunnel for front ends that
// connect later.
type tunnelState struct {
	status procman.Event  // last EventStatus
	health *procman.Event // last EventHealth of the current session
	logs   []procman.Event
}

type server struct {
	mgr    *procman.Manager
	linger time.Duration

	mu      sync.Mutex
	clients map[*serverConn]bool
	tunnels map[string]*tunnelState
	order   []string // tunnel IDs in the order they first appeared
	idle    *time.Timer
	done    chan struct{}
	once    sync.Once
}

type serverConn struct {
	conn net.Conn
	out  chan message

	// mu guards probes and closed: a probe or a stop can finish after its
	// front end has gone, and must not send on the closed channel.
	mu     sync.Mutex
	probes map[int]context.CancelFunc
	closed bool
}

// Options tune Serve; the zero value is what the real agent uses.
type Options struct {
	// Linger overrides how long the agent outlives its last front end.
	Linger time.Duration
}

// Serve runs the agent on socketPath until the last front end has left, then
// stops every tunnel and returns. If another agent already answers on the
// socket it returns nil straight away: two front ends that spawn at the same
// moment end up sharing the first one.
func Serve(socketPath string, opts Options) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return err
	}

	// The lock makes check-then-listen atomic between agents racing to start.
	lock, err := os.OpenFile(socketPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	if c, err := net.DialTimeout("unix", socketPath, time.Second); err == nil {
		c.Close()
		syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		return nil
	}
	os.Remove(socketPath) // left behind by an agent that died
	ln, err := net.Listen("unix", socketPath)
	syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err != nil {
		return err
	}
	// The socket carries pasted AWS credentials: only this user may connect.
	os.Chmod(socketPath, 0o600)
	defer os.Remove(socketPath)

	linger := opts.Linger
	if linger == 0 {
		linger = defaultLinger
	}
	events := make(chan procman.Event, 4096)
	s := &server{
		mgr:     procman.NewManager(events),
		linger:  linger,
		clients: make(map[*serverConn]bool),
		tunnels: make(map[string]*tunnelState),
		done:    make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.mgr.StartHealthMonitor(ctx)
	go s.pump(events)

	s.mu.Lock()
	s.idle = time.AfterFunc(startupWait, s.finish)
	s.mu.Unlock()

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(c)
		}
	}()

	<-s.done
	ln.Close()
	s.mgr.StopAll()
	return nil
}

func (s *server) finish() {
	s.once.Do(func() { close(s.done) })
}

// pump records every event from the tunnels and forwards it to every front
// end. Both happen under s.mu, so a front end that connects mid-stream gets
// its snapshot and then exactly the events after it.
func (s *server) pump(events <-chan procman.Event) {
	for ev := range events {
		s.mu.Lock()
		s.record(ev)
		for c := range s.clients {
			c.send(message{Event: &ev})
		}
		s.mu.Unlock()
	}
}

func (s *server) record(ev procman.Event) {
	st, ok := s.tunnels[ev.ServiceID]
	if !ok {
		st = &tunnelState{}
		s.tunnels[ev.ServiceID] = st
		s.order = append(s.order, ev.ServiceID)
	}
	switch ev.Kind {
	case procman.EventStatus:
		st.status = ev
		st.health = nil // a new status is a new session, or none
	case procman.EventHealth:
		st.health = &ev
	case procman.EventLog:
		st.logs = append(st.logs, ev)
		if len(st.logs) > logReplay {
			st.logs = st.logs[len(st.logs)-logReplay:]
		}
	}
}

func (c *serverConn) send(msg message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	select {
	case c.out <- msg:
	default:
	}
}

func (s *server) handle(nc net.Conn) {
	c := &serverConn{conn: nc, out: make(chan message, clientQueue), probes: make(map[int]context.CancelFunc)}
	defer nc.Close()

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		enc := json.NewEncoder(nc)
		for msg := range c.out {
			if enc.Encode(msg) != nil {
				nc.Close()
				for range c.out {
				}
				return
			}
		}
	}()

	scanner := bufio.NewScanner(nc)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	registered := false
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		if req.Op == opHello {
			if registered {
				continue
			}
			if req.Version != ProtocolVersion {
				c.send(message{Re: req.ID, Version: ProtocolVersion,
					Err: fmt.Sprintf("el agente corre la versión %d del protocolo y esta interfaz la %d", ProtocolVersion, req.Version)})
				break
			}
			s.register(c, req.ID)
			registered = true
			continue
		}
		if registered {
			s.serve(c, req)
		}
	}

	if registered {
		s.unregister(c)
	}
	c.mu.Lock()
	for _, cancel := range c.probes {
		cancel()
	}
	c.closed = true
	close(c.out)
	c.mu.Unlock()
	<-writerDone
}

// register answers the hello and queues the snapshot — each tunnel's status,
// health and recent logs — before any live event can reach this front end.
func (s *server) register(c *serverConn, id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.send(message{Re: id, Version: ProtocolVersion})
	for _, tid := range s.order {
		st := s.tunnels[tid]
		for i := range st.logs {
			c.send(message{Event: &st.logs[i]})
		}
		if st.status.Kind == procman.EventStatus && st.status.ServiceID != "" {
			ev := st.status
			c.send(message{Event: &ev})
		}
		if st.health != nil {
			c.send(message{Event: st.health})
		}
	}
	s.clients[c] = true
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
}

func (s *server) unregister(c *serverConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clients, c)
	if len(s.clients) == 0 && s.idle == nil {
		s.idle = time.AfterFunc(s.linger, func() {
			s.mu.Lock()
			empty := len(s.clients) == 0
			s.mu.Unlock()
			if empty {
				s.finish()
			}
		})
	}
}

func (s *server) serve(c *serverConn, req request) {
	switch req.Op {
	case opStart:
		if req.Service != nil {
			s.mgr.Start(*req.Service)
		}
		c.send(message{Re: req.ID})
	case opStop:
		// Stop waits out the kill grace period; don't hold up this front
		// end's other requests behind it.
		go func() {
			s.mgr.Stop(req.ServiceID)
			c.send(message{Re: req.ID})
		}()
	case opCreds:
		s.mgr.SetAWSCredentials(req.Env)
		c.send(message{Re: req.ID})
	case opProbe:
		if req.Service == nil {
			c.send(message{Re: req.ID, Err: "falta el túnel"})
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		c.mu.Lock()
		c.probes[req.ID] = cancel
		c.mu.Unlock()
		go func() {
			err := s.mgr.Probe(ctx, *req.Service)
			c.mu.Lock()
			delete(c.probes, req.ID)
			c.mu.Unlock()
			cancel()
			msg := message{Re: req.ID}
			if err != nil {
				msg.Err = err.Error()
			}
			c.send(msg)
		}()
	case opCancel:
		c.mu.Lock()
		if cancel, ok := c.probes[req.Re]; ok {
			cancel()
		}
		c.mu.Unlock()
	case opQuit:
		// Tell the other front ends to close, stop every tunnel, then answer
		// — so the caller knows nothing is left running — and end the agent.
		s.mu.Lock()
		for other := range s.clients {
			if other != c {
				other.send(message{Event: &procman.Event{Kind: procman.EventQuit}})
			}
		}
		s.mu.Unlock()
		go func() {
			s.mgr.StopAll()
			c.send(message{Re: req.ID})
			s.finish()
		}()
	case opChanged:
		s.mu.Lock()
		for other := range s.clients {
			if other != c {
				other.send(message{Event: &procman.Event{Kind: procman.EventReload}})
			}
		}
		s.mu.Unlock()
		c.send(message{Re: req.ID})
	default:
		c.send(message{Re: req.ID, Err: errors.New("operación desconocida: " + req.Op).Error()})
	}
}
