// Package agent keeps the tunnels in one background process that every front
// end talks to, so the terminal UI and the desktop app see and drive the same
// tunnels: one started from either shows up as running in the other, and a
// stop from either stops it for both.
//
// The first front end to open spawns the agent (its own binary, run with the
// "agent" argument) and each one connects over a Unix socket speaking
// newline-delimited JSON. The agent stops every tunnel and exits once the last
// front end has left, or at once when a front end quits everything (q in the
// terminal UI), which also closes the others.
package agent

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"

	"scriptstui/internal/config"
	"scriptstui/internal/procman"
)

// ProtocolVersion is bumped whenever the messages below change, so a front
// end built from another version refuses to talk to a running agent instead
// of misreading it.
const ProtocolVersion = 2

// Arg is the command-line argument that turns a front end's binary into the
// agent.
const Arg = "agent"

// request is what a front end sends.
type request struct {
	ID int    `json:"id"`
	Op string `json:"op"`

	Version   int             `json:"version,omitempty"`   // hello
	Service   *config.Service `json:"service,omitempty"`   // start, probe
	ServiceID string          `json:"serviceId,omitempty"` // stop
	Env       []string        `json:"env,omitempty"`       // creds
	Re        int             `json:"re,omitempty"`        // cancel: the probe's request ID
}

const (
	opHello   = "hello"
	opStart   = "start"
	opStop    = "stop"
	opCreds   = "creds"
	opProbe   = "probe"
	opCancel  = "cancel"
	opChanged = "changed"
	opQuit    = "quit"
)

// message is what the agent sends: a reply to a request (Re set) or an event.
type message struct {
	Re      int            `json:"re,omitempty"`
	Err     string         `json:"err,omitempty"`
	Version int            `json:"version,omitempty"`
	Event   *procman.Event `json:"event,omitempty"`
}

// SocketPath is where the agent for the tunnels database at dbPath listens.
// It is keyed by the database so two checkouts never share an agent.
func SocketPath(dbPath string) string {
	if abs, err := filepath.Abs(dbPath); err == nil {
		dbPath = abs
	}
	sum := sha1.Sum([]byte(dbPath))
	return filepath.Join(runtimeDir(), "agent-"+hex.EncodeToString(sum[:4])+".sock")
}

func runtimeDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "scriptstui")
}

// LogPath is where a spawned agent writes its own output.
func LogPath() string {
	return filepath.Join(runtimeDir(), "agent.log")
}
