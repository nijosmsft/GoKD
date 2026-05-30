// REPL and async stream handling for the gokd CLI.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/nijosmsft/gokd"
)

type commandFunc func(gokd.Session, []string) error

var commands = map[string]commandFunc{
	"attach": cmdAttach,
	"detach": cmdDetach,
	"q":      cmdQuit,
	"quit":   cmdQuit,
	"exit":   cmdQuit,
	"?":      cmdHelp,
	"help":   cmdHelp,
	"bp":     cmdBP,
	"bl":     cmdBL,
	"bc":     cmdBC,
	"bd":     cmdBD,
	"be":     cmdBE,
	"g":      cmdGo,
	"t":      cmdStepIn,
	"p":      cmdStepOver,
	"gu":     cmdStepOut,
	"bi":     cmdBreakIn,
	"k":      cmdStack,
	"r":      cmdRegs,
	"dq":     cmdDQ,
	"dd":     cmdDD,
	"db":     cmdDB,
	"dt":     cmdDT,
	"lm":     cmdLM,
	"u":      cmdU,
	"th":     cmdThreads,
	"st":     cmdSetThread,
	"sym":    cmdSym,
	"n2a":    cmdN2A,
	"a2n":    cmdA2N,
}

var errQuit = fmt.Errorf("quit")

func runREPL(s gokd.Session) {
	sc := bufio.NewScanner(os.Stdin)
	for {
		printf("> ")
		if !sc.Scan() {
			return
		}
		if err := runLine(s, sc.Text()); err != nil {
			if err == errQuit {
				return
			}
			printf("error: %v\n", err)
		}
	}
}

func runCommandString(s gokd.Session, text string) error {
	for _, part := range strings.Split(text, ";") {
		if err := runLine(s, part); err != nil {
			if err == errQuit {
				return nil
			}
			return err
		}
	}
	return nil
}

func runLine(s gokd.Session, line string) error {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	if strings.HasPrefix(line, "!") {
		out, err := s.Execute(strings.TrimSpace(line[1:]))
		if out != "" {
			printf("%s", out)
			if !strings.HasSuffix(out, "\n") {
				printf("\n")
			}
		}
		return err
	}
	fields := strings.Fields(line)
	name := strings.ToLower(fields[0])
	fn, ok := commands[name]
	if !ok {
		printf("Unknown command '%s'. Type ? for help.\n", fields[0])
		return nil
	}
	return fn(s, fields[1:])
}

func startAsyncDrainers(s gokd.Session) {
	go func() {
		for out := range s.Output() {
			printf("%s", out)
		}
	}()
	go func() {
		for ev := range s.Events() {
			printf("[event] %s\n", formatEvent(ev))
		}
	}()
}

func printf(format string, args ...any) {
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	fmt.Printf(format, args...)
}

func formatEvent(ev gokd.Event) string {
	switch e := ev.(type) {
	case gokd.BreakpointEvent:
		return fmt.Sprintf("breakpoint %d at 0x%016x thread=%d", e.ID, e.Address, eventThreadID(e.Thread))
	case gokd.ExceptionEvent:
		chance := "second"
		if e.FirstChance {
			chance = "first"
		}
		return fmt.Sprintf("exception 0x%08x at 0x%016x %s-chance thread=%d", e.Code, e.Address, chance, eventThreadID(e.Thread))
	case gokd.ProcessCreatedEvent:
		return fmt.Sprintf("process created %s base=0x%016x size=0x%x", e.ImageName, e.BaseOffset, e.ModuleSize)
	case gokd.ProcessExitedEvent:
		return fmt.Sprintf("process exited code=%d", e.ExitCode)
	case gokd.ModuleLoadedEvent:
		if e.Module == nil {
			return "module loaded"
		}
		return fmt.Sprintf("module loaded %s base=0x%016x size=0x%x", e.Module.Name, e.Module.Base, e.Module.Size)
	case gokd.ModuleUnloadedEvent:
		return fmt.Sprintf("module unloaded %s base=0x%016x", e.ImageBaseName, e.BaseOffset)
	case gokd.ThreadCreatedEvent:
		if e.Thread == nil {
			return "thread created"
		}
		return fmt.Sprintf("thread created sysid=%d start=0x%016x", e.Thread.SystemID, e.Thread.StartOffset)
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
