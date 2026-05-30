//go:build manual

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPEndToEnd drives the gokd-mcp binary as a real MCP server, spawning
// a notepad target, attaching to it, exercising several inspection tools,
// and detaching. This is the closest we can get to "an LLM client used the
// debugger" without actually wiring up Copilot or Claude Desktop.
//
// Run with:
//
//	go test -tags manual -v -run TestMCPEndToEnd ./cmd/gokd-mcp/
func TestMCPEndToEnd(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "gokd-mcp-e2e", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(exe)}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
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
		t.Errorf("get_modules: expected ntdll in %d modules", len(mods.Modules))
	}
	t.Logf("get_modules: %d entries (ntdll present=%v)", len(mods.Modules), foundNtdll)

	var threads threadsOutput
	callTool(t, ctx, session, "get_threads", map[string]any{}, &threads)
	if len(threads.Threads) == 0 {
		t.Fatal("get_threads returned no threads")
	}
	t.Logf("get_threads: %d entries", len(threads.Threads))

	var regs registersOutput
	callTool(t, ctx, session, "get_registers", map[string]any{"names": []string{"rip", "rsp"}}, &regs)
	rip, ok := regs.Registers["rip"]
	if !ok {
		t.Fatalf("get_registers: rip missing, got keys=%v", keys(regs.Registers))
	}
	if rip == "0x0000000000000000" {
		t.Errorf("get_registers: rip is zero")
	}
	t.Logf("get_registers: rip=%s rsp=%s", rip, regs.Registers["rsp"])

	var stack stackOutput
	callTool(t, ctx, session, "get_stack", map[string]any{}, &stack)
	if len(stack.Frames) == 0 {
		t.Fatal("get_stack returned no frames")
	}
	t.Logf("get_stack: %d frames", len(stack.Frames))

	callTool(t, ctx, session, "detach", map[string]any{}, nil)
}

func findExe(t *testing.T) string {
	t.Helper()
	candidates := []string{
		filepath.Join(".", "bin", "gokd-mcp.exe"),
		filepath.Join("..", "..", "bin", "gokd-mcp.exe"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	t.Fatalf("gokd-mcp.exe not found in %v — run `go build -o bin/gokd-mcp.exe ./cmd/gokd-mcp` first", candidates)
	return ""
}

func callTool(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s: tool error: %s", name, textContent(res))
	}
	if out == nil {
		return
	}
	// On the client side, StructuredContent arrives as a generic Go value
	// from json.Unmarshal into `any` (typically map[string]any). Re-marshal
	// it and then decode into the typed output struct.
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("%s: re-marshal StructuredContent: %v", name, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%s: unmarshal: %v (raw=%s)", name, err, string(raw))
	}
}

func textContent(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		} else {
			fmt.Fprintf(&b, " [%T]", c)
		}
	}
	return b.String()
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
