// Command gokd is a small interactive CLI for the GoKD DbgEng wrapper.
package main

/*
#cgo windows LDFLAGS: -static -static-libstdc++ -static-libgcc
*/
import "C"

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/nijosmsft/gokd"
)

type cliConfig struct {
	pid     int
	exec    string
	kernel  string
	dump    string
	remote  string
	symbols string
	command string
	timeout time.Duration
}

var (
	stdoutMu       sync.Mutex
	commandTimeout time.Duration
	cleanupOnce    sync.Once
)

func main() {
	cfg := parseFlags()
	commandTimeout = cfg.timeout

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

	cleanup := func() {
		cleanupOnce.Do(func() {
			_ = sess.Detach()
			_ = sess.Close()
		})
	}
	defer cleanup()

	startAsyncDrainers(sess)
	installSignalHandler(sess, cleanup)

	if cfg.remote != "" {
		if err := sess.ConnectRemote(cfg.remote); err != nil {
			fmt.Fprintf(os.Stderr, "ConnectRemote failed: %v\n", err)
			os.Exit(1)
		}
	}
	if err := attachInitialTarget(sess, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	if cfg.command != "" {
		if err := runCommandString(sess, cfg.command); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		return
	}
	runREPL(sess)
}

func parseFlags() cliConfig {
	var cfg cliConfig
	flag.IntVar(&cfg.pid, "pid", 0, "attach to running process")
	flag.StringVar(&cfg.exec, "exec", "", "create process and attach")
	flag.StringVar(&cfg.kernel, "kernel", "", "kernel attach connection string")
	flag.StringVar(&cfg.dump, "dump", "", "open crash dump")
	flag.StringVar(&cfg.remote, "remote", "", "connect to dbgsrv")
	flag.StringVar(&cfg.symbols, "symbols", "", "set symbol path (default: Microsoft public symbols via WithDefaultSymbols)")
	flag.StringVar(&cfg.command, "c", "", "run semicolon-separated commands and quit")
	flag.DurationVar(&cfg.timeout, "timeout", 0, "timeout for AttachKernel and execution")
	flag.Parse()
	return cfg
}

func attachInitialTarget(s gokd.Session, cfg cliConfig) error {
	switch {
	case cfg.pid != 0:
		if cfg.pid < 0 {
			return fmt.Errorf("invalid pid %d", cfg.pid)
		}
		if err := s.AttachProcess(uint32(cfg.pid), gokd.AttachDefault); err != nil {
			return fmt.Errorf("AttachProcess(%d) failed: %w", cfg.pid, err)
		}
		printf("Attached to pid %d\n", cfg.pid)
	case cfg.exec != "":
		if err := s.CreateProcess(cfg.exec, gokd.CreateOptions{Flags: 0x00000001, InitialBreak: true}); err != nil {
			return fmt.Errorf("CreateProcess failed: %w", err)
		}
		printf("Created and attached: %s\n", cfg.exec)
	case cfg.kernel != "":
		ctx, cancel := contextForCommand()
		defer cancel()
		if err := s.AttachKernel(ctx, cfg.kernel, gokd.KernelDefault); err != nil {
			return fmt.Errorf("AttachKernel failed: %w", err)
		}
		printf("Kernel attached.\n")
	case cfg.dump != "":
		if err := s.OpenDump(cfg.dump); err != nil {
			return fmt.Errorf("OpenDump failed: %w", err)
		}
		printf("Opened dump: %s\n", cfg.dump)
	}
	return nil
}

func contextForCommand() (context.Context, context.CancelFunc) {
	if commandTimeout > 0 {
		return context.WithTimeout(context.Background(), commandTimeout)
	}
	return context.WithCancel(context.Background())
}

func installSignalHandler(s gokd.Session, cleanup func()) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		var last time.Time
		for sig := range ch {
			if sig == syscall.SIGTERM || time.Since(last) <= 2*time.Second {
				printf("\nexiting\n")
				cleanup()
				os.Exit(130)
			}
			last = time.Now()
			printf("\nbreak in\n")
			_ = s.BreakIn()
		}
	}()
}

func parseUint32Arg(s string) (uint32, error) {
	v, err := strconv.ParseUint(s, 0, 32)
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}
