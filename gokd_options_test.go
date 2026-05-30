package gokd_test

import (
	"strings"
	"testing"

	"github.com/nijosmsft/gokd"
)

func TestDefaultSymbolPath(t *testing.T) {
	p := gokd.DefaultSymbolPath()
	if !strings.HasPrefix(p, "srv*") {
		t.Errorf("DefaultSymbolPath: expected prefix srv*, got %q", p)
	}
	if !strings.Contains(p, "msdl.microsoft.com") {
		t.Errorf("DefaultSymbolPath: expected msdl.microsoft.com server, got %q", p)
	}
	if !strings.Contains(p, "gokd") {
		t.Errorf("DefaultSymbolPath: expected per-user gokd cache dir, got %q", p)
	}
}

func TestWithSymbolPath(t *testing.T) {
	const want = "srv*C:\\sym-test*https://msdl.microsoft.com/download/symbols"

	sess, err := gokd.New(gokd.WithSymbolPath(want))
	if err != nil {
		t.Fatalf("New(WithSymbolPath) failed: %v", err)
	}
	defer sess.Close()

	got, err := sess.SymbolPath()
	if err != nil {
		t.Fatalf("SymbolPath() failed: %v", err)
	}
	if got != want {
		t.Errorf("SymbolPath: got %q, want %q", got, want)
	}
}

func TestWithDefaultSymbolsOnEmpty(t *testing.T) {
	// DbgEng seeds from _NT_SYMBOL_PATH at session create. Clear it so the
	// empty-path branch of WithDefaultSymbols runs.
	t.Setenv("_NT_SYMBOL_PATH", "")

	sess, err := gokd.New(gokd.WithDefaultSymbols())
	if err != nil {
		t.Fatalf("New(WithDefaultSymbols) failed: %v", err)
	}
	defer sess.Close()

	got, err := sess.SymbolPath()
	if err != nil {
		t.Fatalf("SymbolPath() failed: %v", err)
	}
	if !strings.Contains(got, "msdl.microsoft.com") {
		t.Errorf("SymbolPath: expected default Microsoft public path when env is empty, got %q", got)
	}
}

func TestWithSymbolPathOverridesDefault(t *testing.T) {
	const explicit = "srv*C:\\sym-explicit*https://example.invalid/symbols"

	sess, err := gokd.New(gokd.WithDefaultSymbols(), gokd.WithSymbolPath(explicit))
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer sess.Close()

	got, err := sess.SymbolPath()
	if err != nil {
		t.Fatalf("SymbolPath() failed: %v", err)
	}
	if got != explicit {
		t.Errorf("SymbolPath: explicit path should win, got %q, want %q", got, explicit)
	}
}
