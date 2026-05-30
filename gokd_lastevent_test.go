package gokd_test

import (
	"errors"
	"testing"

	"github.com/nijosmsft/gokd"
)

// TestLastExceptionAfterAttach attaches to notepad and immediately
// queries LastException. The attach-implicit event is usually
// DEBUG_EVENT_BREAKPOINT (or a synthetic create-process event), so the
// expected outcome is *either* a populated record *or* ErrNotFound; we
// just assert the call is well-formed and doesn't panic.
func TestLastExceptionAfterAttach(t *testing.T) {
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

	ex, err := sess.LastException()
	switch {
	case err == nil:
		if ex == nil {
			t.Fatal("LastException returned (nil, nil)")
		}
		if ex.ParameterCount > gokd.ExceptionMaxParameters {
			t.Errorf("ParameterCount=%d > max %d", ex.ParameterCount, gokd.ExceptionMaxParameters)
		}
		t.Logf("code=0x%08x address=0x%x first_chance=%v params=%d desc=%q",
			ex.Code, ex.Address, ex.FirstChance, ex.ParameterCount, ex.Description)
	case errors.Is(err, gokd.ErrNotFound):
		t.Log("LastException returned ErrNotFound (last event was not an exception — expected)")
	default:
		t.Fatalf("LastException returned unexpected error: %v", err)
	}
}

// TestBugCheckUserMode confirms that calling BugCheck on a user-mode
// session returns ErrNotFound rather than a raw HRESULT.
func TestBugCheckUserMode(t *testing.T) {
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

	bc, err := sess.BugCheck()
	if !errors.Is(err, gokd.ErrNotFound) {
		t.Fatalf("BugCheck() in user mode: got err=%v bc=%+v, want ErrNotFound", err, bc)
	}
	if bc != nil {
		t.Errorf("BugCheck() returned non-nil result with ErrNotFound: %+v", bc)
	}
}

// TestLookupBugCheckName verifies the embedded common-codes table
// surfaces a friendly name for well-known codes and returns empty
// strings for unknown ones.
func TestLookupBugCheckName(t *testing.T) {
	if name, _ := gokd.LookupBugCheckName(0x1A); name != "MEMORY_MANAGEMENT" {
		t.Errorf("LookupBugCheckName(0x1A) name = %q, want MEMORY_MANAGEMENT", name)
	}
	if name, _ := gokd.LookupBugCheckName(0xD1); name != "DRIVER_IRQL_NOT_LESS_OR_EQUAL" {
		t.Errorf("LookupBugCheckName(0xD1) name = %q, want DRIVER_IRQL_NOT_LESS_OR_EQUAL", name)
	}
	if name, desc := gokd.LookupBugCheckName(0xDEADBEEF); name != "" || desc != "" {
		t.Errorf("LookupBugCheckName(0xDEADBEEF) = (%q, %q), want both empty", name, desc)
	}
}

// A real kernel-dump-based BugCheck test is out of scope for Tier 1
// because it requires fixtures (a .dmp file from a crashed kernel).
// We exercise the user-mode error path and the name-lookup helper here.
