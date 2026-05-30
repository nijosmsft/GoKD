//go:build manual

package main

import (
	"context"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPListenEndToEnd exercises the gokd-mcp -listen mode: start the
// binary as a TCP MCP server, connect a client over TCP, attach to notepad,
// list modules, detach.
//
// Run with:
//
//	go test -tags manual -v -run TestMCPListenEndToEnd ./cmd/gokd-mcp/
func TestMCPListenEndToEnd(t *testing.T) {
	exe := findExe(t)

	npCmd := exec.Command("notepad.exe")
	if err := npCmd.Start(); err != nil {
		t.Fatalf("start notepad: %v", err)
	}
	t.Cleanup(func() {
		_ = npCmd.Process.Kill()
		_ = npCmd.Wait()
	})
	time.Sleep(500 * time.Millisecond)
	pid := uint32(npCmd.Process.Pid)
	t.Logf("notepad pid=%d", pid)

	// Pick an ephemeral port: bind here, close immediately, hand to the child.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	listenAddr := probe.Addr().String()
	probe.Close()

	srvCmd := exec.Command(exe, "-listen", listenAddr)
	srvCmd.Stdout = testWriter{t}
	srvCmd.Stderr = testWriter{t}
	if err := srvCmd.Start(); err != nil {
		t.Fatalf("start gokd-mcp -listen: %v", err)
	}
	t.Cleanup(func() {
		_ = srvCmd.Process.Kill()
		_ = srvCmd.Wait()
	})

	// Wait for the listener to come up.
	var conn net.Conn
	for i := 0; i < 50; i++ {
		conn, err = net.DialTimeout("tcp", listenAddr, 500*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial %s: %v", listenAddr, err)
	}
	t.Logf("connected to %s", listenAddr)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "gokd-mcp-listen-e2e", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: conn, Writer: nopCloserWriter{conn}}, nil)
	if err != nil {
		t.Fatalf("MCP connect: %v", err)
	}
	defer session.Close()

	callTool(t, ctx, session, "attach_process", map[string]any{"pid": pid}, nil)

	var mods modulesOutput
	callTool(t, ctx, session, "get_modules", map[string]any{}, &mods)
	if len(mods.Modules) == 0 {
		t.Fatal("get_modules returned no modules")
	}
	foundNtdll := false
	for _, m := range mods.Modules {
		if strings.EqualFold(m.Name, "ntdll") {
			foundNtdll = true
			break
		}
	}
	if !foundNtdll {
		t.Errorf("expected ntdll among %d modules", len(mods.Modules))
	}
	t.Logf("get_modules: %d entries (ntdll=%v)", len(mods.Modules), foundNtdll)

	callTool(t, ctx, session, "detach", map[string]any{}, nil)
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("[gokd-mcp child] %s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

type nopCloserWriter struct{ net.Conn }

func (nopCloserWriter) Close() error { return nil }
