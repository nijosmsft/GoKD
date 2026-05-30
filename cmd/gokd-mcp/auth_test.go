package main

import (
	"io"
	"net"
	"strings"
	"sync"
	"testing"
)

// pipeConn returns a pair of net.Conn backed by net.Pipe. It is the
// simplest in-process loopback for exercising authenticateConn without
// binding a real TCP socket.
func pipeConn(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	return a, b
}

func TestAuthenticateConnDisabled(t *testing.T) {
	srv, _ := pipeConn(t)
	// Empty token: handshake is a no-op, no bytes consumed.
	if err := authenticateConn(srv, ""); err != nil {
		t.Fatalf("authenticateConn empty token: %v", err)
	}
}

func TestAuthenticateConnAccepts(t *testing.T) {
	srv, cli := pipeConn(t)
	token := "ABCDEFGHIJKLMNOP1234"

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.WriteString(cli, "AUTH "+token+"\n")
		// Read the OK\n reply so the server's write doesn't block.
		buf := make([]byte, 16)
		_, _ = cli.Read(buf)
	}()

	if err := authenticateConn(srv, token); err != nil {
		t.Fatalf("authenticateConn: %v", err)
	}
	wg.Wait()
}

func TestAuthenticateConnRejectsWrongToken(t *testing.T) {
	srv, cli := pipeConn(t)
	var wg sync.WaitGroup
	wg.Add(1)
	var reply []byte
	go func() {
		defer wg.Done()
		_, _ = io.WriteString(cli, "AUTH WRONGTOKEN12345\n")
		reply = make([]byte, 32)
		n, _ := cli.Read(reply)
		reply = reply[:n]
	}()

	err := authenticateConn(srv, "ABCDEFGHIJKLMNOP1234")
	if err == nil {
		t.Fatal("expected error on wrong token")
	}
	wg.Wait()
	if !strings.HasPrefix(string(reply), "DENIED") {
		t.Errorf("client got %q, want DENIED prefix", reply)
	}
}

func TestAuthenticateConnRejectsMissingPrefix(t *testing.T) {
	srv, cli := pipeConn(t)
	var wg sync.WaitGroup
	wg.Add(1)
	var reply []byte
	go func() {
		defer wg.Done()
		_, _ = io.WriteString(cli, `{"jsonrpc":"2.0","method":"initialize"}`+"\n")
		reply = make([]byte, 32)
		n, _ := cli.Read(reply)
		reply = reply[:n]
	}()

	err := authenticateConn(srv, "ABCDEFGHIJKLMNOP1234")
	if err == nil {
		t.Fatal("expected error on missing AUTH prefix")
	}
	wg.Wait()
	if !strings.HasPrefix(string(reply), "DENIED") {
		t.Errorf("client got %q, want DENIED prefix", reply)
	}
}

func TestAuthenticateConnDeadline(t *testing.T) {
	srv, cli := pipeConn(t)
	// Client closes immediately without sending: server's ReadString
	// returns io.ErrClosedPipe so authenticateConn fails fast. (The
	// internal 5s deadline isn't hit because the read errors first.)
	_ = cli.Close()
	err := authenticateConn(srv, "ABCDEFGHIJKLMNOP1234")
	if err == nil {
		t.Fatal("expected error when client closes before sending")
	}
}

func TestAuthenticateConnRejectsPipelinedBytes(t *testing.T) {
	srv, cli := pipeConn(t)
	token := "ABCDEFGHIJKLMNOP1234"
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Send AUTH line and a JSON-RPC frame in one write.
		_, _ = io.WriteString(cli, "AUTH "+token+"\n"+`{"jsonrpc":"2.0"}`+"\n")
		buf := make([]byte, 16)
		_, _ = cli.Read(buf)
	}()
	err := authenticateConn(srv, token)
	if err == nil {
		t.Fatal("expected rejection of pipelined bytes")
	}
	wg.Wait()
}
