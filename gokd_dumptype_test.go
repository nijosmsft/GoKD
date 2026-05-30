package gokd_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nijosmsft/gokd"
)

// findUnicodeString walks a TypeValue tree and returns the first node
// whose TypeName resolves to _UNICODE_STRING and whose Decoded slot is
// a string. Returns nil if none found.
func findUnicodeString(tv *gokd.TypeValue) *gokd.TypeValue {
	if tv == nil {
		return nil
	}
	name := strings.TrimSpace(strings.TrimPrefix(tv.TypeName, "_"))
	if strings.EqualFold(name, "UNICODE_STRING") {
		if _, ok := tv.Decoded.(string); ok {
			return tv
		}
	}
	for _, c := range tv.Children {
		if hit := findUnicodeString(c); hit != nil {
			return hit
		}
	}
	return nil
}

func TestDumpType_Notepad_PEB(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	peb, _, err := sess.Evaluate(ctx, "@$peb", gokd.ValueInt64)
	if err != nil {
		t.Skipf("Evaluate(@$peb) failed: %v", err)
	}
	pebAddr := peb.U64
	if pebAddr == 0 {
		t.Skip("@$peb is 0 — cannot walk")
	}

	tv, err := sess.DumpType(ctx, "ntdll", "_PEB", pebAddr, gokd.DumpTypeOptions{MaxDepth: 2})
	if err != nil {
		t.Skipf("DumpType(_PEB) failed (public PDBs may not include _PEB): %v", err)
	}
	if tv == nil {
		t.Fatal("DumpType returned nil tree")
	}
	if len(tv.Children) == 0 {
		t.Skip("_PEB resolved with no children — public PDBs probably lack struct layout")
	}
	if tv.Children[0].TypeName == "" {
		t.Errorf("first child has empty TypeName")
	}
}

func TestDumpType_UnicodeStringDecoder(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	peb, _, err := sess.Evaluate(ctx, "@$peb", gokd.ValueInt64)
	if err != nil {
		t.Skipf("Evaluate(@$peb) failed: %v", err)
	}
	pebAddr := peb.U64
	if pebAddr == 0 {
		t.Skip("@$peb is 0")
	}

	tv, err := sess.DumpType(ctx, "ntdll", "_PEB", pebAddr, gokd.DumpTypeOptions{MaxDepth: 4, FollowPtrs: true})
	if err != nil {
		t.Skipf("DumpType failed: %v", err)
	}
	hit := findUnicodeString(tv)
	if hit == nil {
		t.Skip("no _UNICODE_STRING with decoded value found in PEB subtree (probably public PDBs)")
	}
	if s, ok := hit.Decoded.(string); !ok {
		t.Errorf("expected Decoded to be string, got %T", hit.Decoded)
	} else {
		t.Logf("decoded UNICODE_STRING at %s: %q", hit.TypeName, s)
	}
}

func TestDumpType_DepthLimit(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	peb, _, err := sess.Evaluate(ctx, "@$peb", gokd.ValueInt64)
	if err != nil {
		t.Skipf("Evaluate(@$peb) failed: %v", err)
	}
	pebAddr := peb.U64
	if pebAddr == 0 {
		t.Skip("@$peb is 0")
	}

	// MaxDepth=-1 is normalised to 0 inside DumpType (header only).
	tv, err := sess.DumpType(ctx, "ntdll", "_PEB", pebAddr, gokd.DumpTypeOptions{MaxDepth: -1})
	if err != nil {
		t.Skipf("DumpType failed: %v", err)
	}
	if tv == nil {
		t.Fatal("DumpType returned nil")
	}
	if len(tv.Children) != 0 {
		t.Errorf("expected 0 children at depth 0, got %d", len(tv.Children))
	}
}

func TestDumpType_BadType(t *testing.T) {
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

	_, err = sess.DumpType(ctx, "ntdll", "_THIS_TYPE_DOES_NOT_EXIST_xyzzy", 0x1000, gokd.DumpTypeOptions{MaxDepth: 1})
	if err == nil {
		t.Errorf("expected error for unknown type, got nil")
	}
}
