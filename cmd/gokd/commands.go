// Command handlers for the gokd interactive debugger.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/nijosmsft/gokd"
)

func cmdAttach(s gokd.Session, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: attach <pid>")
	}
	pid, err := parseUint32Arg(args[0])
	if err != nil {
		return err
	}
	if err := s.AttachProcess(pid, gokd.AttachDefault); err != nil {
		return err
	}
	printf("Attached to pid %d\n", pid)
	return nil
}

func cmdDetach(s gokd.Session, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: detach")
	}
	return s.Detach()
}

func cmdQuit(gokd.Session, []string) error { return errQuit }

func cmdHelp(_ gokd.Session, args []string) error {
	if len(args) == 0 {
		printf("Commands:\n")
		names := sortedCommandNames()
		var maxLen int
		for _, n := range names {
			if len(n) > maxLen {
				maxLen = len(n)
			}
		}
		for _, n := range names {
			printf("  %-*s  %s\n", maxLen, n, commands[n].Short)
		}
		printf("  %-*s  %s\n", maxLen, "!<cmd>", "execute raw dbgeng command")
		printf("\nType 'help <command>' for details and examples.\n")
		return nil
	}
	name := strings.ToLower(args[0])
	spec, ok := commands[name]
	if !ok {
		return fmt.Errorf("unknown command: %s", name)
	}
	printf("%s -- %s\n", name, spec.Short)
	if spec.Long != "" {
		printf("\n%s\n", spec.Long)
	}
	if len(spec.Examples) > 0 {
		printf("\nExamples:\n")
		for _, ex := range spec.Examples {
			printf("  %s\n", ex)
		}
	}
	return nil
}

func cmdBP(s gokd.Session, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: bp <addr-or-symbol>")
	}
	if looksHex(args[0]) {
		addr, err := parseAddr(s, args[0])
		if err != nil {
			return err
		}
		bp, err := s.AddBreakpoint(addr)
		if err != nil {
			return err
		}
		printf("Breakpoint %d at 0x%016x\n", bp.ID, addr)
		return nil
	}
	bp, err := s.AddBreakpointSym(args[0])
	if err != nil {
		return err
	}
	addr := bp.Address
	if addr == 0 {
		if resolved, err := s.NameToAddr(args[0]); err == nil {
			addr = resolved
		}
	}
	printf("Breakpoint %d at 0x%016x\n", bp.ID, addr)
	return nil
}

func cmdBL(s gokd.Session, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: bl")
	}
	bps, err := s.Breakpoints()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(outWriter, 0, 0, 2, ' ', 0)
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	fmt.Fprintln(tw, "id\ten\taddr\texpr")
	for _, bp := range bps {
		en := "n"
		if bp.Enabled {
			en = "y"
		}
		fmt.Fprintf(tw, "%d\t%s\t%016x\t%s\n", bp.ID, en, bp.Address, bp.Expression)
	}
	return tw.Flush()
}

func breakpointID(args []string, usage string) (uint32, error) {
	if len(args) != 1 {
		return 0, fmt.Errorf("usage: %s", usage)
	}
	id, err := strconv.ParseUint(args[0], 10, 32)
	return uint32(id), err
}

func cmdBC(s gokd.Session, args []string) error {
	id, err := breakpointID(args, "bc <id>")
	if err != nil {
		return err
	}
	return s.RemoveBreakpoint(id)
}
func cmdBD(s gokd.Session, args []string) error {
	id, err := breakpointID(args, "bd <id>")
	if err != nil {
		return err
	}
	return s.EnableBreakpoint(id, false)
}
func cmdBE(s gokd.Session, args []string) error {
	id, err := breakpointID(args, "be <id>")
	if err != nil {
		return err
	}
	return s.EnableBreakpoint(id, true)
}

func runExec(s gokd.Session, args []string, fn func(context.Context) (*gokd.StopEvent, error), usage string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: %s", usage)
	}
	ctx, cancel := contextForCommand()
	defer cancel()
	ev, err := fn(ctx)
	if err != nil {
		return err
	}
	printStopEvent(ev)
	return nil
}

func cmdGo(s gokd.Session, args []string) error       { return runExec(s, args, s.Go, "g") }
func cmdStepIn(s gokd.Session, args []string) error   { return runExec(s, args, s.StepIn, "t") }
func cmdStepOver(s gokd.Session, args []string) error { return runExec(s, args, s.StepOver, "p") }
func cmdStepOut(s gokd.Session, args []string) error  { return runExec(s, args, s.StepOut, "gu") }

func cmdBreakIn(s gokd.Session, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: bi")
	}
	return s.BreakIn()
}

func cmdStack(s gokd.Session, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: k")
	}
	frames, err := s.Stack()
	if err != nil {
		return err
	}
	printStack(frames)
	return nil
}

func cmdRegs(s gokd.Session, args []string) error {
	regs, err := s.Registers()
	if err != nil {
		return err
	}
	printRegisters(regs, args)
	return nil
}

func cmdDQ(s gokd.Session, args []string) error { return dumpMemory(s, args, 8, 16) }
func cmdDD(s gokd.Session, args []string) error { return dumpMemory(s, args, 4, 16) }
func cmdDB(s gokd.Session, args []string) error { return dumpMemory(s, args, 1, 128) }

func dumpMemory(s gokd.Session, args []string, width, defaultCount int) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: d%c <addr> [count]", "xbdq"[width/4+1])
	}
	addr, err := parseAddr(s, args[0])
	if err != nil {
		return err
	}
	count := uint64(defaultCount)
	if len(args) == 2 {
		count, err = parseCount(args[1])
		if err != nil {
			return err
		}
	}
	data, err := s.ReadMemory(addr, count*uint64(width))
	if err != nil {
		return err
	}
	printHexdump(addr, data, width)
	return nil
}

func cmdDT(s gokd.Session, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: dt <module>!<type>")
	}
	mod, typ, ok := strings.Cut(args[0], "!")
	if !ok || mod == "" || typ == "" {
		return fmt.Errorf("usage: dt <module>!<type>")
	}
	fields, err := s.TypeFields(mod, typ)
	if err != nil {
		return err
	}
	for _, f := range fields {
		printf("+0x%03x  %s  : %s  (size %d)\n", f.Offset, f.Name, f.TypeName, f.Size)
	}
	return nil
}

func cmdLM(s gokd.Session, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: lm")
	}
	mods, err := s.Modules()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(outWriter, 0, 0, 2, ' ', 0)
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	fmt.Fprintln(tw, "base\tsize\tname")
	for _, m := range mods {
		fmt.Fprintf(tw, "%016x\t%08x\t%s\n", m.Base, m.Size, m.Name)
	}
	return tw.Flush()
}

func cmdU(s gokd.Session, args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: u <addr> [count]")
	}
	addr, err := parseAddr(s, args[0])
	if err != nil {
		return err
	}
	count := uint64(8)
	if len(args) == 2 {
		count, err = parseCount(args[1])
		if err != nil {
			return err
		}
	}
	ins, err := s.DisassembleRange(addr, int(count))
	if err != nil {
		return err
	}
	for _, in := range ins {
		printf("%016x  %s\n", in.Address, in.Text)
	}
	return nil
}

func cmdThreads(s gokd.Session, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: th")
	}
	ths, err := s.Threads()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(outWriter, 0, 0, 2, ' ', 0)
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	fmt.Fprintln(tw, "sysid\thandle\tteb\tstart")
	for _, th := range ths {
		fmt.Fprintf(tw, "%d\t%016x\t%016x\t%016x\n", th.SystemID, th.Handle, th.DataOffset, th.StartOffset)
	}
	return tw.Flush()
}

func cmdSetThread(s gokd.Session, args []string) error {
	tid, err := breakpointID(args, "st <sysTID>")
	if err != nil {
		return err
	}
	return s.SetThread(tid)
}

func cmdSym(s gokd.Session, args []string) error {
	if len(args) == 0 {
		p, err := s.SymbolPath()
		if err != nil {
			return err
		}
		printf("%s\n", p)
		return nil
	}
	return s.SetSymbolPath(strings.Join(args, " "))
}

func cmdN2A(s gokd.Session, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: n2a <name>")
	}
	addr, err := s.NameToAddr(args[0])
	if err != nil {
		return err
	}
	printf("%s = 0x%016x\n", args[0], addr)
	return nil
}

func cmdA2N(s gokd.Session, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: a2n <addr>")
	}
	addr, err := parseAddr(s, args[0])
	if err != nil {
		return err
	}
	name, disp, err := s.AddrToName(addr)
	if err != nil {
		return err
	}
	printf("0x%016x = %s+0x%x\n", addr, name, disp)
	return nil
}
