package procman

import (
	"io"
	"net"
	"testing"
)

// listen starts a one-shot server on a free port that runs handle on the
// first connection it gets.
func listen(t *testing.T, handle func(net.Conn)) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		handle(conn)
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func TestCheckPortPostgresAnswers(t *testing.T) {
	port := listen(t, func(c net.Conn) {
		buf := make([]byte, len(postgresSSLRequest))
		if _, err := io.ReadFull(c, buf); err == nil {
			c.Write([]byte("N"))
		}
	})
	if err := CheckPort(port, 5432); err != nil {
		t.Fatalf("CheckPort = %v, want nil", err)
	}
}

func TestCheckPortRemoteDrops(t *testing.T) {
	// What a tunnel does when its remote host can't be reached: accept
	// locally, then hang up.
	port := listen(t, func(net.Conn) {})
	if err := CheckPort(port, 5432); err == nil {
		t.Fatal("CheckPort = nil, want an error for a dropped connection")
	}
}

func TestCheckPortNothingListening(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if err := CheckPort(port, 6379); err == nil {
		t.Fatal("CheckPort = nil, want an error with nothing listening")
	}
}
