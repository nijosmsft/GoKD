//go:build manual

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPSmoke(t *testing.T) {
	exe := filepath.Join(".", "bin", "gokd-mcp.exe")
	if _, err := os.Stat(exe); err != nil {
		exe = filepath.Join("..", "..", "bin", "gokd-mcp.exe")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "gokd-mcp-smoke", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(exe)}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) < 25 {
		t.Fatalf("got %d tools, want at least 25", len(res.Tools))
	}

	want := map[string]bool{"get_modules": false, "attach_process": false, "add_breakpoint": false}
	for _, tool := range res.Tools {
		if tool.Description == "" {
			t.Fatalf("tool %q has empty description", tool.Name)
		}
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("tool %q not registered", name)
		}
	}
	t.Logf("registered %d tools", len(res.Tools))
}
