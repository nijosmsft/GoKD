package gokd_test

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/nijosmsft/gokd"
)

// launchNotepad starts notepad.exe and returns its PID and a cleanup func.
func launchNotepad(t *testing.T) (uint32, func()) {
	t.Helper()
	cmd := exec.Command("notepad.exe")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start notepad.exe: %v", err)
	}
	// Give it a moment to initialise.
	time.Sleep(500 * time.Millisecond)
	pid := uint32(cmd.Process.Pid)
	return pid, func() {
		cmd.Process.Kill()
		cmd.Wait()
	}
}

func TestSessionCreateClose(t *testing.T) {
	sess, err := gokd.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}
}

func TestAttachDetach(t *testing.T) {
	pid, cleanup := launchNotepad(t)
	defer cleanup()

	sess, err := gokd.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer sess.Close()

	t.Logf("Attaching to notepad.exe pid=%d", pid)
	if err := sess.AttachProcess(pid, gokd.AttachDefault); err != nil {
		t.Fatalf("AttachProcess(%d) failed: %v", pid, err)
	}

	if err := sess.Detach(); err != nil {
		t.Fatalf("Detach() failed: %v", err)
	}
	t.Log("Attach/detach succeeded")
}

func TestModules(t *testing.T) {
	pid, cleanup := launchNotepad(t)
	defer cleanup()

	sess, err := gokd.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer sess.Close()

	if err := sess.AttachProcess(pid, gokd.AttachDefault); err != nil {
		t.Fatalf("AttachProcess failed: %v", err)
	}
	defer sess.Detach()

	mods, err := sess.Modules()
	if err != nil {
		t.Fatalf("Modules() failed: %v", err)
	}
	if len(mods) == 0 {
		t.Fatal("expected at least one module, got 0")
	}

	foundNtdll := false
	for _, m := range mods {
		t.Logf("  module: %-20s base=0x%x size=0x%x", m.Name, m.Base, m.Size)
		if m.Name == "ntdll" {
			foundNtdll = true
		}
	}
	if !foundNtdll {
		t.Error("expected to find ntdll module")
	}
}

func TestRegisters(t *testing.T) {
	pid, cleanup := launchNotepad(t)
	defer cleanup()

	sess, err := gokd.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer sess.Close()

	if err := sess.AttachProcess(pid, gokd.AttachDefault); err != nil {
		t.Fatalf("AttachProcess failed: %v", err)
	}
	defer sess.Detach()

	regs, err := sess.Registers()
	if err != nil {
		t.Fatalf("Registers() failed: %v", err)
	}
	if len(regs.Registers) == 0 {
		t.Fatal("expected at least one register, got 0")
	}

	// Check for rip on x64.
	rip, ok := regs.ByName["rip"]
	if !ok {
		t.Log("rip not found (may not be x64), printing available registers:")
		for _, r := range regs.Registers {
			t.Logf("  %s = 0x%x", r.Name, r.Value)
		}
	} else {
		t.Logf("rip = 0x%x", rip.Value)
		if rip.Value == 0 {
			t.Error("rip is 0, expected a valid instruction pointer")
		}
	}
}

func TestStack(t *testing.T) {
	pid, cleanup := launchNotepad(t)
	defer cleanup()

	sess, err := gokd.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer sess.Close()

	if err := sess.AttachProcess(pid, gokd.AttachDefault); err != nil {
		t.Fatalf("AttachProcess failed: %v", err)
	}
	defer sess.Detach()

	frames, err := sess.Stack()
	if err != nil {
		t.Fatalf("Stack() failed: %v", err)
	}
	if len(frames) == 0 {
		t.Fatal("expected at least one stack frame, got 0")
	}

	for i, f := range frames {
		sym := fmt.Sprintf("%s!%s+0x%x", f.Module, f.Function, f.Displacement)
		t.Logf("  #%d  0x%016x  %s", i, f.InstructionOffset, sym)
	}
}

func TestReadMemory(t *testing.T) {
	pid, cleanup := launchNotepad(t)
	defer cleanup()

	sess, err := gokd.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer sess.Close()

	if err := sess.AttachProcess(pid, gokd.AttachDefault); err != nil {
		t.Fatalf("AttachProcess failed: %v", err)
	}
	defer sess.Detach()

	// Read the first 2 bytes of ntdll — should be "MZ".
	mods, _ := sess.Modules()
	var ntdllBase uint64
	for _, m := range mods {
		if m.Name == "ntdll" {
			ntdllBase = m.Base
			break
		}
	}
	if ntdllBase == 0 {
		t.Fatal("ntdll not found")
	}

	data, err := sess.ReadMemory(ntdllBase, 2)
	if err != nil {
		t.Fatalf("ReadMemory failed: %v", err)
	}
	if len(data) < 2 || data[0] != 'M' || data[1] != 'Z' {
		t.Errorf("expected MZ header, got %v", data)
	} else {
		t.Logf("ntdll @ 0x%x starts with MZ — correct", ntdllBase)
	}
}

func TestThreads(t *testing.T) {
	pid, cleanup := launchNotepad(t)
	defer cleanup()

	sess, err := gokd.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer sess.Close()

	if err := sess.AttachProcess(pid, gokd.AttachDefault); err != nil {
		t.Fatalf("AttachProcess failed: %v", err)
	}
	defer sess.Detach()

	threads, err := sess.Threads()
	if err != nil {
		t.Fatalf("Threads() failed: %v", err)
	}
	if len(threads) == 0 {
		t.Fatal("expected at least one thread, got 0")
	}
	for _, th := range threads {
		t.Logf("  thread sys_id=%d handle=0x%x teb=0x%x",
			th.SystemID, th.Handle, th.DataOffset)
	}
}

func TestSymbolResolution(t *testing.T) {
	pid, cleanup := launchNotepad(t)
	defer cleanup()

	sess, err := gokd.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer sess.Close()

	if err := sess.AttachProcess(pid, gokd.AttachDefault); err != nil {
		t.Fatalf("AttachProcess failed: %v", err)
	}
	defer sess.Detach()

	addr, err := sess.NameToAddr("ntdll!RtlUserThreadStart")
	if err != nil {
		t.Fatalf("NameToAddr failed: %v", err)
	}
	if addr == 0 {
		t.Fatal("expected non-zero address for ntdll!RtlUserThreadStart")
	}
	t.Logf("ntdll!RtlUserThreadStart = 0x%x", addr)

	name, disp, err := sess.AddrToName(addr)
	if err != nil {
		t.Fatalf("AddrToName failed: %v", err)
	}
	t.Logf("0x%x -> %s+0x%x", addr, name, disp)
}

func TestBreakpointAndGo(t *testing.T) {
	sess, err := gokd.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer sess.Close()

	// Create a process under the debugger so we have full control.
	if err := sess.CreateProcess("notepad.exe", gokd.CreateOptions{
		Flags: 0x00000001, // DEBUG_ONLY_THIS_PROCESS
	}); err != nil {
		t.Fatalf("CreateProcess failed: %v", err)
	}
	defer sess.Detach()

	// Set a breakpoint on kernel32!CreateFileW.
	bp, err := sess.AddBreakpointSym("kernel32!CreateFileW")
	if err != nil {
		t.Logf("AddBreakpointSym failed (symbol not available): %v", err)
		t.Skip("skipping breakpoint test — symbol not available")
	}
	t.Logf("Breakpoint %d set on kernel32!CreateFileW", bp.ID)

	// List breakpoints.
	bps, err := sess.Breakpoints()
	if err != nil {
		t.Fatalf("Breakpoints() failed: %v", err)
	}
	if len(bps) == 0 {
		t.Fatal("expected at least one breakpoint")
	}
	t.Logf("  %d breakpoint(s) set", len(bps))

	// Resume briefly — the breakpoint may not hit since notepad
	// doesn't call CreateFileW immediately. We just verify Go()
	// works without error and can be interrupted by BreakIn().
	go func() {
		time.Sleep(2 * time.Second)
		sess.BreakIn()
	}()

	stopEv, err := sess.Go(context.Background())
	if err != nil {
		t.Logf("Go() returned: %v (expected — BreakIn interrupted)", err)
	} else {
		t.Logf("Stopped: reason=%s addr=0x%x thread=%d",
			stopEv.Reason, stopEv.Address, stopEv.Thread.SystemID)
	}
}
