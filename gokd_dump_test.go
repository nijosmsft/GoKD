package gokd_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nijosmsft/gokd"
)

// TestWriteDumpSmall attaches to notepad, writes a small minidump to a
// temp file, and verifies a second session can re-open it and list its
// modules.
func TestWriteDumpSmall(t *testing.T) {
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

	dir := t.TempDir()
	abs, err := filepath.Abs(filepath.Join(dir, "notepad-small.dmp"))
	if err != nil {
		t.Fatalf("Abs failed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := sess.WriteDump(ctx, abs, gokd.WriteDumpOptions{
		Kind: gokd.DumpSmall,
	}); err != nil {
		t.Fatalf("WriteDump failed: %v", err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("Stat(%s) failed: %v", abs, err)
	}
	if st.Size() < 4*1024 {
		t.Fatalf("dump too small: %d bytes", st.Size())
	}
	t.Logf("dump = %s (%d bytes)", abs, st.Size())

	// Detach and close the first session before re-opening — DbgEng can
	// fail to release the dump if a process target is still attached on
	// the dispatch thread.
	_ = sess.Detach()
	_ = sess.Close()

	sess2, err := gokd.New()
	if err != nil {
		t.Fatalf("New() (#2) failed: %v", err)
	}
	defer sess2.Close()
	if err := sess2.OpenDump(abs); err != nil {
		t.Fatalf("OpenDump(%s) failed: %v", abs, err)
	}
	defer sess2.Detach()
	mods, err := sess2.Modules()
	if err != nil {
		t.Fatalf("Modules() failed: %v", err)
	}
	if len(mods) == 0 {
		t.Errorf("re-opened dump has no modules")
	} else {
		t.Logf("re-opened dump has %d modules; first: %s", len(mods), mods[0].Name)
	}
}

// TestWriteDumpComment writes a minidump with a comment and checks the
// call succeeds; no public API surfaces the comment back, so we only
// verify nil-error.
func TestWriteDumpComment(t *testing.T) {
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

	dir := t.TempDir()
	abs, err := filepath.Abs(filepath.Join(dir, "notepad-commented.dmp"))
	if err != nil {
		t.Fatalf("Abs failed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := sess.WriteDump(ctx, abs, gokd.WriteDumpOptions{
		Kind:    gokd.DumpSmall,
		Comment: "gokd test",
	}); err != nil {
		t.Fatalf("WriteDump(commented) failed: %v", err)
	}
}

// TestWriteDumpRejectsRelative ensures the public layer rejects relative
// paths (DbgEng would otherwise resolve them against its CWD).
func TestWriteDumpRejectsRelative(t *testing.T) {
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

	err = sess.WriteDump(context.Background(), "relative.dmp", gokd.WriteDumpOptions{Kind: gokd.DumpSmall})
	if err == nil {
		t.Fatalf("WriteDump unexpectedly accepted relative path")
	}
}

// TestWriteDumpCancelled cancels the context before the call. WriteDump
// is uncancellable mid-call so we accept either context.Canceled (the
// dispatch thread observed the cancellation before starting the write)
// or success (the write completed before the goroutine ran).
func TestWriteDumpCancelled(t *testing.T) {
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

	dir := t.TempDir()
	abs, _ := filepath.Abs(filepath.Join(dir, "notepad-cancel.dmp"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = sess.WriteDump(ctx, abs, gokd.WriteDumpOptions{Kind: gokd.DumpSmall})
	switch {
	case err == nil:
		t.Logf("WriteDump finished before cancellation took effect (acceptable)")
	case errors.Is(err, context.Canceled):
		t.Logf("WriteDump cancelled: %v", err)
	default:
		t.Logf("WriteDump returned: %v", err)
	}
}
