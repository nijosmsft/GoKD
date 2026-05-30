package gokd_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/nijosmsft/gokd"
)

// TestAddDataBreakpointRoundTrip verifies that a hardware breakpoint
// installed against a well-known kernel32 export shows up in
// Breakpoints() with the size and access fields reflected back.
func TestAddDataBreakpointRoundTrip(t *testing.T) {
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

	addr, err := sess.NameToAddr("kernel32!UnhandledExceptionFilter")
	if err != nil {
		t.Skipf("symbol not available: %v", err)
	}

	access := gokd.BreakpointAccessWrite
	bp, err := sess.AddDataBreakpoint(addr, 4, access)
	if err != nil {
		t.Fatalf("AddDataBreakpoint failed: %v", err)
	}
	defer func() { _ = sess.RemoveBreakpoint(bp.ID) }()

	if bp.Type != gokd.BreakpointTypeData {
		t.Errorf("expected BreakpointTypeData, got %v", bp.Type)
	}
	if bp.Size != 4 {
		t.Errorf("expected size 4, got %d", bp.Size)
	}
	if bp.Access != access {
		t.Errorf("expected access %v, got %v", access, bp.Access)
	}

	bps, err := sess.Breakpoints()
	if err != nil {
		t.Fatalf("Breakpoints() failed: %v", err)
	}

	var found *gokd.Breakpoint
	for _, b := range bps {
		if b.ID == bp.ID {
			found = b
			break
		}
	}
	if found == nil {
		t.Fatalf("BP %d missing from Breakpoints()", bp.ID)
	}
	if found.Type != gokd.BreakpointTypeData {
		t.Errorf("listed BP type = %v, want data", found.Type)
	}
	if found.Size != 4 {
		t.Errorf("listed BP size = %d, want 4", found.Size)
	}
	if found.Access != access {
		t.Errorf("listed BP access = %v, want %v", found.Access, access)
	}
	if found.MatchThreadID != gokd.BreakpointMatchThreadAny {
		t.Errorf("listed BP match_thread = 0x%x, want BreakpointMatchThreadAny",
			found.MatchThreadID)
	}
	if !found.Enabled {
		t.Error("listed BP should be enabled by default")
	}
}

// TestConfigureBreakpoint exercises pass-count + WinDbg command round-trip
// against a plain code breakpoint.
func TestConfigureBreakpoint(t *testing.T) {
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

	bp, err := sess.AddBreakpointSym("kernel32!CreateFileW")
	if err != nil {
		t.Skipf("symbol not available: %v", err)
	}
	defer func() { _ = sess.RemoveBreakpoint(bp.ID) }()

	const wantCmd = "r rax;g"
	if err := sess.ConfigureBreakpoint(bp.ID, gokd.BreakpointOptions{
		PassCount: 5,
		Command:   wantCmd,
	}); err != nil {
		t.Fatalf("ConfigureBreakpoint failed: %v", err)
	}

	gotCmd, err := sess.BreakpointCommand(bp.ID)
	if err != nil {
		t.Fatalf("BreakpointCommand failed: %v", err)
	}
	if !strings.EqualFold(gotCmd, wantCmd) {
		t.Errorf("BreakpointCommand = %q, want %q", gotCmd, wantCmd)
	}

	bps, err := sess.Breakpoints()
	if err != nil {
		t.Fatalf("Breakpoints() failed: %v", err)
	}
	var found *gokd.Breakpoint
	for _, b := range bps {
		if b.ID == bp.ID {
			found = b
			break
		}
	}
	if found == nil {
		t.Fatalf("BP %d missing", bp.ID)
	}
	if found.PassCount != 5 {
		t.Errorf("PassCount = %d, want 5", found.PassCount)
	}
	// CurrentPass should be the remaining hits before fire. DbgEng
	// initialises it to PassCount after SetPassCount.
	if found.CurrentPass != 5 {
		t.Logf("CurrentPass = %d (informational; DbgEng may diverge)", found.CurrentPass)
	}
	if !strings.EqualFold(found.Command, wantCmd) {
		t.Errorf("listed Command = %q, want %q", found.Command, wantCmd)
	}

	// Now clear the command and confirm.
	if err := sess.ConfigureBreakpoint(bp.ID, gokd.BreakpointOptions{
		ClearCommand: true,
	}); err != nil {
		t.Fatalf("ConfigureBreakpoint(clear) failed: %v", err)
	}
	gotCmd, err = sess.BreakpointCommand(bp.ID)
	if err != nil {
		t.Fatalf("BreakpointCommand(after clear) failed: %v", err)
	}
	if gotCmd != "" {
		t.Errorf("after clear, command = %q, want \"\"", gotCmd)
	}
}

// TestInvalidDataSize asserts the Go-layer validation rejects sizes
// other than {1, 2, 4, 8} with E_INVALIDARG before reaching the shim.
func TestInvalidDataSize(t *testing.T) {
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
	if err != nil || len(mods) == 0 {
		t.Skipf("no modules: %v", err)
	}
	addr := mods[0].Base

	_, err = sess.AddDataBreakpoint(addr, 3, gokd.BreakpointAccessWrite)
	if err == nil {
		t.Fatal("expected error for size=3, got nil")
	}
	var hr gokd.HRESULTError
	if !errors.As(err, &hr) {
		t.Fatalf("expected HRESULTError, got %T: %v", err, err)
	}
	const wantHR int32 = int32(0x80070057 - 0x100000000) // E_INVALIDARG
	if hr.HRESULT() != wantHR {
		t.Errorf("HRESULT = 0x%08x, want 0x80070057", uint32(hr.HRESULT()))
	}

	// Empty access mask is also rejected.
	_, err = sess.AddDataBreakpoint(addr, 4, 0)
	if err == nil {
		t.Error("expected error for empty access, got nil")
	}
}
