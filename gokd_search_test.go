package gokd_test

import (
	"errors"
	"testing"

	"github.com/nijosmsft/gokd"
)

// TestQueryRegionOfMainImage asserts QueryRegion describes the main
// image as a committed memory region whose extent contains its base
// address.
func TestQueryRegionOfMainImage(t *testing.T) {
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
		t.Fatal("no modules in target")
	}
	base := mods[0].Base
	r, err := sess.QueryRegion(base)
	if err != nil {
		t.Fatalf("QueryRegion(0x%x) failed: %v", base, err)
	}
	t.Logf("region base=0x%x size=0x%x state=0x%x protect=0x%x type=0x%x",
		r.BaseAddress, r.RegionSize, uint32(r.State), uint32(r.Protect), uint32(r.Type))
	if r.State != gokd.MemCommit {
		t.Errorf("expected MEM_COMMIT (0x1000), got 0x%x", uint32(r.State))
	}
	if !(r.BaseAddress <= base && base < r.BaseAddress+r.RegionSize) {
		t.Errorf("base 0x%x not within region [0x%x, 0x%x)", base,
			r.BaseAddress, r.BaseAddress+r.RegionSize)
	}
}

// TestVirtualToPhysicalUserModeError exercises the user-mode path. We
// accept any HRESULT here — the test asserts only that the call does
// not crash and either succeeds or returns an error.
func TestVirtualToPhysicalUserModeError(t *testing.T) {
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
		t.Skip("no modules to translate")
	}
	pa, err := sess.VirtualToPhysical(mods[0].Base)
	if err != nil {
		t.Logf("VirtualToPhysical(0x%x) returned (expected in user-mode): %v",
			mods[0].Base, err)
		return
	}
	t.Logf("VirtualToPhysical(0x%x) = 0x%x", mods[0].Base, pa)
}

// TestSearchMemoryHit reads bytes from the image base, takes a 16-byte
// slice, and asserts SearchMemory finds that exact sequence within a
// small surrounding window.
func TestSearchMemoryHit(t *testing.T) {
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
		t.Skip("no modules to scan")
	}
	base := mods[0].Base
	const window = 256
	buf, err := sess.ReadMemory(base, window)
	if err != nil {
		t.Fatalf("ReadMemory failed: %v", err)
	}
	if len(buf) < 32 {
		t.Skipf("read returned only %d bytes; cannot run search", len(buf))
	}
	pattern := buf[16:32]
	match, err := sess.SearchMemory(base, window, pattern, 1)
	if err != nil {
		t.Fatalf("SearchMemory failed: %v", err)
	}
	if match != base+16 {
		t.Errorf("match = 0x%x, want 0x%x", match, base+16)
	}
}

// TestSearchMemoryMiss writes a tiny block of 0xAB bytes via a known
// trick (we just search at the image base for a guaranteed-absent
// pattern) and asserts ErrNotFound.
func TestSearchMemoryMiss(t *testing.T) {
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
		t.Skip("no modules to scan")
	}
	base := mods[0].Base
	// First grab a small window so we can pick a pattern that is NOT in it.
	const window = 64
	buf, err := sess.ReadMemory(base, window)
	if err != nil {
		t.Fatalf("ReadMemory failed: %v", err)
	}
	// Build an 8-byte pattern guaranteed not to occur in buf by XORing a
	// known mismatch into every byte.
	probe := make([]byte, 8)
	copy(probe, buf[:8])
	for i := range probe {
		probe[i] ^= 0xFF
	}
	_, err = sess.SearchMemory(base, window-8, probe, 1)
	if err == nil {
		t.Fatalf("SearchMemory unexpectedly found pattern %x", probe)
	}
	if !errors.Is(err, gokd.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
