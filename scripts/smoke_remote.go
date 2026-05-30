//go:build manual && remote

// smoke_remote.go: quick interactive smoke test for `gokd-mcp -remote NODE`.
// Spawns gokd-mcp.exe -remote <NODE>, runs `initialize` then `tools/list`,
// prints results, and exits.
//
// Usage:
//   go run -tags manual scripts/smoke_remote.go RR1N4406-25
//
// Requires the same lablink env vars that LabLinkServer uses (token file,
// TLS material). Inherits parent env.

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: smoke_remote <NODE>")
		os.Exit(2)
	}
	node := os.Args[1]

	exe := `C:\git\gokd\bin\gokd-mcp.exe`
	cmd := exec.Command(exe, "-remote", node, "-log", `C:\git\gokd\gokd-mcp-remote.log`)
	cmd.Stderr = os.Stderr

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
	fmt.Println("OK")
}
