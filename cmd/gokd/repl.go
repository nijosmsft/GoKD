// REPL and async stream handling for the gokd CLI.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chzyer/readline"
	"github.com/nijosmsft/gokd"
)

type commandFunc func(gokd.Session, []string) error

type commandSpec struct {
	Fn       commandFunc
	Short    string
	Long     string
	Examples []string
}

var commands = map[string]commandSpec{
	"attach": {Fn: cmdAttach, Short: "Attach to a running process",
		Long:     "Attach DbgEng to an existing process by PID.",
		Examples: []string{"attach 4321"}},
	"detach": {Fn: cmdDetach, Short: "Detach from the current target",
		Long: "Detach DbgEng from the current target, leaving it running."},
	"bp": {Fn: cmdBP, Short: "Set a breakpoint",
		Long:     "Set a software breakpoint at an address or symbol.",
		Examples: []string{"bp kernel32!CreateFileW", "bp 0x7ff800001234"}},
	"bl": {Fn: cmdBL, Short: "List breakpoints",
		Long: "List all configured breakpoints with id, enabled flag, address and expression."},
	"bc": {Fn: cmdBC, Short: "Clear (remove) a breakpoint",
		Long: "Remove the breakpoint with the given numeric id (see bl).", Examples: []string{"bc 0"}},
	"bd": {Fn: cmdBD, Short: "Disable a breakpoint",
		Long: "Disable the breakpoint with the given id without removing it.", Examples: []string{"bd 0"}},
	"be": {Fn: cmdBE, Short: "Enable a breakpoint",
		Long: "Enable a previously disabled breakpoint.", Examples: []string{"be 0"}},
	"g": {Fn: cmdGo, Short: "Go (resume execution)",
		Long: "Resume target execution until a breakpoint, exception, or other stop event. Ctrl+C breaks in."},
	"t": {Fn: cmdStepIn, Short: "Step in",
		Long: "Execute a single instruction, stepping into calls."},
	"p": {Fn: cmdStepOver, Short: "Step over",
		Long: "Execute a single instruction, stepping over calls."},
	"gu": {Fn: cmdStepOut, Short: "Step out",
		Long: "Run until the current function returns."},
	"bi": {Fn: cmdBreakIn, Short: "Break into the target",
		Long: "Asynchronously request a break-in (equivalent to Ctrl+Break in WinDbg)."},
	"k": {Fn: cmdStack, Short: "Print stack trace",
		Long: "Print the current thread's stack trace."},
	"r": {Fn: cmdRegs, Short: "Show registers",
		Long:     "Print register values. With arguments, show only the named registers.",
		Examples: []string{"r", "r rax rsp rip"}},
	"dq": {Fn: cmdDQ, Short: "Dump memory as qwords",
		Long: "Read and display memory as 8-byte values.", Examples: []string{"dq 0x7ff800000000", "dq @rsp L20"}},
	"dd": {Fn: cmdDD, Short: "Dump memory as dwords",
		Long: "Read and display memory as 4-byte values.", Examples: []string{"dd 0x7ff800000000 L10"}},
	"db": {Fn: cmdDB, Short: "Dump memory as bytes",
		Long: "Read and display memory as bytes plus ASCII.", Examples: []string{"db 0x7ff800000000 L40"}},
	"dt": {Fn: cmdDT, Short: "Show type fields",
		Long: "Look up a structure via DbgHelp and print its fields with offsets.",
		Examples: []string{"dt nt!_EPROCESS", "dt ntdll!_PEB"}},
	"lm": {Fn: cmdLM, Short: "List loaded modules",
		Long: "List currently loaded modules with base address and size."},
	"u": {Fn: cmdU, Short: "Disassemble instructions",
		Long: "Disassemble starting at an address. Default count is 8 instructions.",
		Examples: []string{"u kernel32!CreateFileW", "u 0x7ff800001234 L10"}},
	"th": {Fn: cmdThreads, Short: "List threads",
		Long: "List threads in the target with system TID, handle, TEB, and start address."},
	"st": {Fn: cmdSetThread, Short: "Set current thread",
		Long: "Switch the active thread by system TID.", Examples: []string{"st 1234"}},
	"sym": {Fn: cmdSym, Short: "Get or set the symbol path",
		Long:     "With no arguments, print the current symbol path. With arguments, set it.",
		Examples: []string{"sym", `sym srv*C:\symbols*https://msdl.microsoft.com/download/symbols`}},
	"n2a": {Fn: cmdN2A, Short: "Resolve symbol name to address",
		Long: "Resolve a symbol name to its virtual address.", Examples: []string{"n2a kernel32!CreateFileW"}},
	"a2n": {Fn: cmdA2N, Short: "Resolve address to nearest symbol",
		Long: "Resolve an address to the nearest symbol plus displacement.", Examples: []string{"a2n 0x7ff800001234"}},
	"q":    {Fn: cmdQuit, Short: "Quit the debugger"},
	"quit": {Fn: cmdQuit, Short: "Quit the debugger"},
	"exit": {Fn: cmdQuit, Short: "Quit the debugger"},
}

func init() {
	helpSpec := commandSpec{
		Fn:       cmdHelp,
		Short:    "Show help",
		Long:     "With no arguments, list all commands. With a command name, show its details and examples.",
		Examples: []string{"help", "help bp"},
	}
	commands["help"] = helpSpec
	commands["?"] = commandSpec{Fn: cmdHelp, Short: "Alias for help"}
	commands["h"] = commandSpec{Fn: cmdHelp, Short: "Alias for help"}
}

// quitNames lists command names that close the session; these are not stored
// as the "last command" for repeat-on-empty-Enter.
var quitNames = map[string]bool{"q": true, "quit": true, "exit": true}

var errQuit = fmt.Errorf("quit")

var (
	outWriter io.Writer = os.Stdout
	lastLine  string
)

func runREPL(s gokd.Session) {
	histPath := historyFile()
	rl, err := readline.NewEx(&readline.Config{
		Prompt:            "gokd> ",
		HistoryFile:       histPath,
		HistoryLimit:      1000,
		InterruptPrompt:   "^C",
		EOFPrompt:         "exit",
		HistorySearchFold: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "readline init failed: %v; falling back to plain input\n", err)
		runREPLFallback(s)
		return
	}
	defer rl.Close()

	stdoutMu.Lock()
	outWriter = rl.Stdout()
	stdoutMu.Unlock()
	defer func() {
		stdoutMu.Lock()
		outWriter = os.Stdout
		stdoutMu.Unlock()
	}()

	for {
		line, err := rl.Readline()
		if err != nil {
			if errors.Is(err, readline.ErrInterrupt) {
				printf("(press Ctrl+D or type 'quit' to exit)\n")
				continue
			}
			if errors.Is(err, io.EOF) {
				return
			}
			printf("readline error: %v\n", err)
			return
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if lastLine == "" {
				continue
			}
			trimmed = lastLine
			printf("(repeat) %s\n", trimmed)
		}
		if rerr := runLine(s, trimmed); rerr != nil {
			if rerr == errQuit {
				_ = rl.SaveHistory(trimmed)
				return
			}
			printf("error: %v\n", rerr)
		}
		if !isQuitCommand(trimmed) {
			lastLine = trimmed
		}
		_ = rl.SaveHistory(trimmed)
	}
}

func runREPLFallback(s gokd.Session) {
	r := readline.NewCancelableStdin(os.Stdin)
	defer r.Close()
	buf := make([]byte, 0, 256)
	tmp := make([]byte, 256)
	printf("gokd> ")
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for {
				i := -1
				for j, b := range buf {
					if b == '\n' {
						i = j
						break
					}
				}
				if i < 0 {
					break
				}
				line := strings.TrimRight(string(buf[:i]), "\r")
				buf = buf[i+1:]
				trimmed := strings.TrimSpace(line)
				if trimmed == "" && lastLine != "" {
					trimmed = lastLine
				}
				if trimmed != "" {
					if rerr := runLine(s, trimmed); rerr != nil {
						if rerr == errQuit {
							return
						}
						printf("error: %v\n", rerr)
					}
					if !isQuitCommand(trimmed) {
						lastLine = trimmed
					}
				}
				printf("gokd> ")
			}
		}
		if err != nil {
			return
		}
	}
}

func isQuitCommand(line string) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	return quitNames[strings.ToLower(fields[0])]
}

func historyFile() string {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "gokd")
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return ""
	}
	return filepath.Join(dir, "history")
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
	spec, ok := commands[name]
	if !ok {
		printf("Unknown command '%s'. Type ? for help.\n", fields[0])
		return nil
	}
	return spec.Fn(s, fields[1:])
}

// sortedCommandNames returns all command names sorted alphabetically.
func sortedCommandNames() []string {
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
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
	fmt.Fprintf(outWriter, format, args...)
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
