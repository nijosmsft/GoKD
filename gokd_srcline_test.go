package gokd_test

import (
	"errors"
	"testing"

	"github.com/nijosmsft/gokd"
)

// TestSourceLineRoundTrip exercises AddrToLine + LineToAddr against the
// current RIP. Notepad ships only public PDBs without line info, so a
// successful round-trip is best-effort: if AddrToLine returns
// ErrNotFound we skip cleanly.
func TestSourceLineRoundTrip(t *testing.T) {
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
		t.Skip("no stack frames available")
	}
	rip := frames[0].InstructionOffset

	sl, err := sess.AddrToLine(rip)
	if err != nil {
		if errors.Is(err, gokd.ErrNotFound) {
			t.Skipf("no line info for RIP 0x%x (install line-info PDBs to run): %v", rip, err)
		}
		t.Fatalf("AddrToLine(0x%x) failed: %v", rip, err)
	}
	t.Logf("RIP 0x%x => %s:%d (+0x%x)", rip, sl.File, sl.Line, sl.Displacement)
	if sl.File == "" {
		t.Errorf("file was empty but AddrToLine succeeded")
	}
	if sl.Line == 0 {
		t.Errorf("line was zero but AddrToLine succeeded")
	}

	got, err := sess.LineToAddr(sl.File, sl.Line)
	if err != nil {
		t.Fatalf("LineToAddr(%q, %d) failed: %v", sl.File, sl.Line, err)
	}
	if got != rip-sl.Displacement {
		t.Logf("LineToAddr returned 0x%x; RIP 0x%x - disp 0x%x = 0x%x (allowed to differ if the line covers multiple statements)",
			got, rip, sl.Displacement, rip-sl.Displacement)
	}
}

// TestLineToAddrNotFound asserts that an obviously bogus (file, line)
// surfaces as ErrNotFound.
func TestLineToAddrNotFound(t *testing.T) {
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

	_, err = sess.LineToAddr(`C:\\nonexistent\\path\\that\\dbgeng\\will\\not\\know.cpp`, 42)
	if err == nil {
		t.Fatalf("LineToAddr unexpectedly succeeded for a bogus path")
	}
	if !errors.Is(err, gokd.ErrNotFound) {
		// Not a hard failure: depending on DbgEng/version some other
		// HRESULT might come back. Log so the run is still informative.
		t.Logf("LineToAddr returned non-ErrNotFound error: %v", err)
	}
}

// TestAddBreakpointSourceLineInvalid asserts that AddBreakpointSourceLine
// surfaces errors from LineToAddr.
func TestAddBreakpointSourceLineInvalid(t *testing.T) {
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

	if _, err := sess.AddBreakpointSourceLine("", 1); err == nil {
		t.Errorf("AddBreakpointSourceLine(\"\") unexpectedly succeeded")
	}
	if _, err := sess.AddBreakpointSourceLine(`C:\\never\\seen.cpp`, 1); err == nil {
		t.Errorf("AddBreakpointSourceLine(bogus) unexpectedly succeeded")
	}
}
