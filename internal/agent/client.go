package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"scriptstui/internal/config"
	"scriptstui/internal/procman"
)

// replyTimeout bounds how long a front end waits on anything but a probe.
const replyTimeout = 10 * time.Second

// Client is a front end's connection to the agent. It offers the same calls
// as a procman.Manager, so a front end drives shared tunnels exactly as it
// used to drive its own.
type Client struct {
	conn   net.Conn
	events chan procman.Event

	writeMu sync.Mutex
	enc     *json.Encoder

	mu      sync.Mutex
	nextID  int
	pending map[int]chan message
	closed  bool
}

// Connect joins the agent for the tunnels database at dbPath, spawning it
// from this same binary when none is running yet.
func Connect(dbPath string) (*Client, error) {
	socket := SocketPath(dbPath)
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		if err := spawn(socket); err != nil {
			return nil, fmt.Errorf("no se pudo iniciar el agente: %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			conn, err = net.DialTimeout("unix", socket, time.Second)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("el agente no respondió (ver %s): %v", LogPath(), err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	return Dial(conn)
}

// spawn starts `<this binary> agent <socket>` detached from this process, so
// it outlives the front end that launched it while another one is open.
func spawn(socket string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(runtimeDir(), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cmd := exec.Command(exe, Arg, socket)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// Dial speaks the protocol over an already open connection to the agent.
func Dial(conn net.Conn) (*Client, error) {
	c := &Client{
		conn: conn,
		// Large enough to hold the snapshot — every tunnel's recent logs —
		// while the front end is still starting up.
		events:  make(chan procman.Event, 16384),
		enc:     json.NewEncoder(conn),
		pending: make(map[int]chan message),
	}
	go c.read()

	reply, err := c.call(context.Background(), request{Op: opHello, Version: ProtocolVersion}, replyTimeout)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if reply.Version != ProtocolVersion {
		conn.Close()
		return nil, fmt.Errorf("el agente corre otra versión (%d); cierra las otras interfaces y vuelve a abrir", reply.Version)
	}
	return c, nil
}

func (c *Client) read() {
	scanner := bufio.NewScanner(c.conn)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var msg message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		if msg.Event != nil {
			c.events <- *msg.Event
			continue
		}
		c.mu.Lock()
		ch := c.pending[msg.Re]
		delete(c.pending, msg.Re)
		c.mu.Unlock()
		if ch != nil {
			ch <- msg
		}
	}

	c.mu.Lock()
	wasClosed := c.closed
	c.closed = true
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if !wasClosed {
		c.events <- procman.Event{Kind: procman.EventBackendLost, Text: "se perdió la conexión con el agente de túneles"}
	}
}

var errClosed = errors.New("sin conexión con el agente de túneles")

// call sends req and waits for its reply, for ctx or for timeout (zero means
// no timeout).
func (c *Client) call(ctx context.Context, req request, timeout time.Duration) (message, error) {
	id, ch, err := c.send(req)
	if err != nil {
		return message{}, err
	}
	return c.await(ctx, id, ch, timeout)
}

// send registers req under a fresh ID and writes it.
func (c *Client) send(req request) (int, chan message, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, nil, errClosed
	}
	c.nextID++
	req.ID = c.nextID
	ch := make(chan message, 1)
	c.pending[req.ID] = ch
	c.mu.Unlock()

	if err := c.write(req); err != nil {
		c.forget(req.ID)
		return 0, nil, err
	}
	return req.ID, ch, nil
}

func (c *Client) await(ctx context.Context, id int, ch chan message, timeout time.Duration) (message, error) {
	var expired <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		expired = t.C
	}
	select {
	case msg, ok := <-ch:
		if !ok {
			return message{}, errClosed
		}
		if msg.Err != "" {
			return msg, errors.New(msg.Err)
		}
		return msg, nil
	case <-ctx.Done():
		c.forget(id)
		return message{}, ctx.Err()
	case <-expired:
		c.forget(id)
		return message{}, errors.New("el agente de túneles no respondió")
	}
}

func (c *Client) forget(id int) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) write(req request) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.enc.Encode(req)
}

// Events carries every tunnel's logs, status and health — first a snapshot of
// how things stand, then live — plus EventReload and EventBackendLost.
func (c *Client) Events() <-chan procman.Event { return c.events }

// Start asks the agent to start cfg; its progress arrives as events.
func (c *Client) Start(cfg config.Service) {
	c.fire(request{Op: opStart, Service: &cfg})
}

// Stop asks the agent to stop the tunnel id, for every front end.
func (c *Client) Stop(id string) {
	c.fire(request{Op: opStop, ServiceID: id})
}

// SetAWSCredentials hands pasted credentials to the agent, where every
// tunnel without a profile will use them — whichever front end starts it.
func (c *Client) SetAWSCredentials(env []string) {
	c.fire(request{Op: opCreds, Env: env})
}

// NotifyChanged tells the other front ends the tunnels database changed.
func (c *Client) NotifyChanged() {
	c.fire(request{Op: opChanged})
}

// fire sends a request whose reply nobody waits for; a failure surfaces as
// EventBackendLost when the connection drops.
func (c *Client) fire(req request) {
	go c.call(context.Background(), req, replyTimeout)
}

// Probe asks the agent to open cfg's tunnel briefly and check it answers,
// as procman.Manager.Probe does. Cancelling ctx cancels it in the agent too.
func (c *Client) Probe(ctx context.Context, cfg config.Service) error {
	id, ch, err := c.send(request{Op: opProbe, Service: &cfg})
	if err != nil {
		return err
	}
	_, err = c.await(ctx, id, ch, 0)
	if ctx.Err() != nil {
		c.write(request{Op: opCancel, Re: id})
	}
	return err
}

// quitTimeout covers stopping every tunnel, which waits out each one's kill
// grace period.
const quitTimeout = 15 * time.Second

// QuitAll stops every tunnel, closes the other front ends and ends the agent,
// returning once the tunnels are down.
func (c *Client) QuitAll() {
	c.call(context.Background(), request{Op: opQuit}, quitTimeout)
	c.Shutdown()
}

// Shutdown leaves the agent. The tunnels keep running while another front
// end is connected; after the last one leaves the agent stops them.
func (c *Client) Shutdown() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.conn.Close()
}
