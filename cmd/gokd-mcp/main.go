// Command gokd-mcp exposes GoKD debugging operations as MCP tools over stdio.
package main

/*
#cgo windows LDFLAGS: -static -static-libstdc++ -static-libgcc
*/
import "C"

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nijosmsft/gokd"
)

type config struct {
	symbols string
	logPath string
}

func main() {
	cfg := parseFlags()

	logWriter, closeLog, err := setupLogWriter(cfg.logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "log setup failed: %v\n", err)
		os.Exit(1)
	}
	defer closeLog()

	engineLog := log.New(logWriter, "[engine] ", 0)

	var newOpts []gokd.Option
	if cfg.symbols != "" {
		newOpts = append(newOpts, gokd.WithSymbolPath(cfg.symbols))
	} else {
		newOpts = append(newOpts, gokd.WithDefaultSymbols())
	}

	sess, err := gokd.New(newOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "New() failed: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		_ = sess.Detach()
		_ = sess.Close()
	}()

	startAsyncDrainers(sess, engineLog)

	server := mcp.NewServer(&mcp.Implementation{Name: "gokd-mcp", Version: "0.1.0"}, &mcp.ServerOptions{
		Instructions: "Stateful MCP server for Windows DbgEng debugging through GoKD. Attach or open a target before inspection tools.",
		Logger:       slog.New(slog.NewTextHandler(logWriter, nil)),
	})
	registerTools(server, &srv{sess: sess})

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server failed: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.symbols, "symbols", "", "set symbol path at startup (default: Microsoft public symbols via WithDefaultSymbols)")
	flag.StringVar(&cfg.logPath, "log", "", "log MCP traffic and engine output to this file")
	flag.Parse()
	return cfg
}

func setupLogWriter(path string) (io.Writer, func(), error) {
	if path == "" {
		return os.Stderr, func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		return nil, nil, err
	}
	return io.MultiWriter(os.Stderr, f), func() { _ = f.Close() }, nil
}

func startAsyncDrainers(sess gokd.Session, logger *log.Logger) {
	go func() {
		for out := range sess.Output() {
			logger.Print(out)
		}
	}()
	go func() {
		for ev := range sess.Events() {
			logger.Printf("[event] %s", formatEvent(ev))
		}
	}()
}

func formatEvent(ev gokd.Event) string {
	switch e := ev.(type) {
	case gokd.BreakpointEvent:
		return fmt.Sprintf("breakpoint %d at %s thread=%d", e.ID, hex64(e.Address), eventThreadID(e.Thread))
	case gokd.ExceptionEvent:
		chance := "second"
		if e.FirstChance {
			chance = "first"
		}
		return fmt.Sprintf("exception 0x%08x at %s %s-chance thread=%d", e.Code, hex64(e.Address), chance, eventThreadID(e.Thread))
	case gokd.ProcessCreatedEvent:
		return fmt.Sprintf("process created %s base=%s size=0x%x", e.ImageName, hex64(e.BaseOffset), e.ModuleSize)
	case gokd.ProcessExitedEvent:
		return fmt.Sprintf("process exited code=%d", e.ExitCode)
	case gokd.ModuleLoadedEvent:
		if e.Module == nil {
			return "module loaded"
		}
		return fmt.Sprintf("module loaded %s base=%s size=0x%x", e.Module.Name, hex64(e.Module.Base), e.Module.Size)
	case gokd.ModuleUnloadedEvent:
		return fmt.Sprintf("module unloaded %s base=%s", e.ImageBaseName, hex64(e.BaseOffset))
	case gokd.ThreadCreatedEvent:
		if e.Thread == nil {
			return "thread created"
		}
		return fmt.Sprintf("thread created sysid=%d start=%s", e.Thread.SystemID, hex64(e.Thread.StartOffset))
	case gokd.ThreadExitedEvent:
		return fmt.Sprintf("thread exited sysid=%d code=%d", e.SystemID, e.ExitCode)
	default:
		return fmt.Sprintf("%T", ev)
	}
}

func eventThreadID(t *gokd.Thread) uint32 {
	if t == nil {
		return 0
	}
	return t.SystemID
}
