package gokd_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nijosmsft/gokd"
)

// TestModuleSymbolType verifies that every module reported by Modules()
// has a SymbolType in the documented [0..7] range and that the string
// formatter returns a stable lower-case name.
func TestModuleSymbolType(t *testing.T) {
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

	seen := map[gokd.SymbolType]int{}
	for _, m := range mods {
		st := m.SymbolType
		if uint32(st) > 7 {
			t.Errorf("module %s has SymbolType %d outside documented [0..7]", m.Name, uint32(st))
		}
		seen[st]++
		name := gokd.SymbolTypeString(st)
		if name == "" || strings.HasPrefix(name, "unknown(") {
			t.Errorf("SymbolTypeString(%d) returned %q for module %s", uint32(st), name, m.Name)
		}
	}
	for st, n := range seen {
		t.Logf("  %d module(s) have symbol_type=%s", n, gokd.SymbolTypeString(st))
	}
}

// TestReloadNoop calls ReloadSymbols with an empty spec, which should be
// a near-instant no-op when nothing is stale, and returns nil.
func TestReloadNoop(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := sess.ReloadSymbols(ctx, ""); err != nil {
		t.Fatalf("ReloadSymbols(\"\") returned: %v", err)
	}
}

// TestSymFixSetsPath calls SymFix with an explicit cache directory and
// confirms the resulting symbol path begins with the expected
// "srv*<dir>*https://..." form.
func TestSymFixSetsPath(t *testing.T) {
	sess, err := gokd.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer sess.Close()

	cache := t.TempDir()
	if err := sess.SymFix(cache); err != nil {
		t.Fatalf("SymFix(%q) failed: %v", cache, err)
	}
	path, err := sess.SymbolPath()
	if err != nil {
		t.Fatalf("SymbolPath() failed: %v", err)
	}
	want := "srv*" + cache + "*https://msdl.microsoft.com/download/symbols"
	if !strings.Contains(path, want) {
		t.Errorf("SymbolPath = %q, want it to contain %q", path, want)
	}
}
