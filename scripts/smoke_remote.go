//go:build manual

// smoke_remote.go: end-to-end smoke test for `gokd-mcp -remote NODE`.
// Spawns gokd-mcp.exe -remote <NODE>, runs `initialize` + `tools/list`,
// and (if a PID is supplied) also drives attach_process -> get_modules ->
// detach to exercise the actual debugger pipeline through the tunnel.
//
// Usage:
//
//	go run -tags manual scripts/smoke_remote.go RR1N4406-25
//	go run -tags manual scripts/smoke_remote.go RR1N4406-25 13612
//
// Requires the same lablink env vars that LabLinkServer uses (token file,
// TLS material). Inherits parent env.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: smoke_remote <NODE> [PID]")
		os.Exit(2)
	}
	node := os.Args[1]
	var pid int
	if len(os.Args) >= 3 {
		v, err := strconv.Atoi(os.Args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad PID %q: %v\n", os.Args[2], err)
			os.Exit(2)
		}
		pid = v
	}

	exe := `C:\git\gokd\bin\gokd-mcp.exe`
	cmd := exec.Command(exe, "-remote", node, "-log", `C:\git\gokd\gokd-mcp-remote.log`)
	cmd.Stderr = os.Stderr

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "smoke-remote", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	defer session.Close()

	fmt.Println("connected; listing tools...")
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ListTools: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("tools: %d\n", len(tools.Tools))
	for i, t := range tools.Tools {
		if i >= 5 {
			fmt.Printf("  ... +%d more\n", len(tools.Tools)-5)
			break
		}
		fmt.Printf("  - %s\n", t.Name)
	}

	if pid > 0 {
		fmt.Printf("attach_process pid=%d...\n", pid)
		mustCall(ctx, session, "attach_process", map[string]any{"pid": pid})
		fmt.Println("get_modules...")
		mods := mustCall(ctx, session, "get_modules", map[string]any{})
		fmt.Printf("get_modules returned %d bytes; first 200: %s\n", len(mods), trunc(mods, 200))
		fmt.Println("detach...")
		mustCall(ctx, session, "detach", map[string]any{})
	}

	fmt.Println("OK")
}

func mustCall(ctx context.Context, s *mcp.ClientSession, name string, args map[string]any) string {
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		fmt.Fprintf(os.Stderr, "CallTool %s: %v\n", name, err)
		os.Exit(1)
	}
	if res.IsError {
		b, _ := json.Marshal(res.Content)
		fmt.Fprintf(os.Stderr, "tool %s error: %s\n", name, b)
		os.Exit(1)
	}
	b, _ := json.Marshal(res.Content)
	return string(b)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
