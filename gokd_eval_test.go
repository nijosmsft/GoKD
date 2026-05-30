package gokd_test

import (
	"context"
	"testing"
	"time"

	"github.com/nijosmsft/gokd"
)

// TestEvaluateNumericLiteral parses 0x1234 as an int64.
func TestEvaluateNumericLiteral(t *testing.T) {
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
	v, rem, err := sess.Evaluate(ctx, "0x1234", gokd.ValueInt64)
	if err != nil {
		t.Fatalf("Evaluate(0x1234) failed: %v", err)
	}
	if v.U64 != 0x1234 {
		t.Errorf("U64 = 0x%x, want 0x1234", v.U64)
	}
	if rem != 0 {
		t.Errorf("remainder = %d, want 0", rem)
	}
}

// TestEvaluateSymbol resolves an ntdll export through the expression
// evaluator. Skips cleanly if the symbol cannot be resolved (no symbols).
func TestEvaluateSymbol(t *testing.T) {
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
	v, _, err := sess.Evaluate(ctx, "ntdll!NtClose", gokd.ValueInvalid)
	if err != nil {
		t.Skipf("Evaluate(ntdll!NtClose) failed (symbol not available): %v", err)
	}
	if v.Type != gokd.ValueInt64 {
		t.Errorf("Type = %s, want int64", gokd.ValueKindString(v.Type))
	}
	if v.U64 == 0 {
		t.Errorf("expected non-zero address for ntdll!NtClose, got 0")
	}
}

// TestRadixRoundTrip ensures SetRadix/Radix preserve the value, and
// restores the default 10 afterwards.
func TestRadixRoundTrip(t *testing.T) {
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

	original, err := sess.Radix()
	if err != nil {
		t.Fatalf("Radix() failed: %v", err)
	}
	defer sess.SetRadix(original)

	if err := sess.SetRadix(16); err != nil {
		t.Fatalf("SetRadix(16) failed: %v", err)
	}
	r, err := sess.Radix()
	if err != nil {
		t.Fatalf("Radix() failed: %v", err)
	}
	if r != 16 {
		t.Errorf("Radix() = %d, want 16", r)
	}
}

// TestExpressionSyntaxRoundTrip switches to C++ and back to MASM.
func TestExpressionSyntaxRoundTrip(t *testing.T) {
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

	original, err := sess.ExpressionSyntax()
	if err != nil {
		t.Fatalf("ExpressionSyntax() failed: %v", err)
	}
	defer sess.SetExpressionSyntax(original)

	if err := sess.SetExpressionSyntax(gokd.ExpressionSyntaxCPP); err != nil {
		t.Fatalf("SetExpressionSyntax(C++) failed: %v", err)
	}
	got, err := sess.ExpressionSyntax()
	if err != nil {
		t.Fatalf("ExpressionSyntax() after switch failed: %v", err)
	}
	if got != gokd.ExpressionSyntaxCPP {
		t.Errorf("after SetExpressionSyntax(C++), got %s", got)
	}

	if err := sess.SetExpressionSyntax(gokd.ExpressionSyntaxMASM); err != nil {
		t.Fatalf("SetExpressionSyntax(MASM) failed: %v", err)
	}
	got, err = sess.ExpressionSyntax()
	if err != nil {
		t.Fatalf("ExpressionSyntax() after MASM switch failed: %v", err)
	}
	if got != gokd.ExpressionSyntaxMASM {
		t.Errorf("after SetExpressionSyntax(MASM), got %s", got)
	}
}

// TestEvaluateRemainder expects a non-zero remainder when trailing tokens
// remain after the parsed expression.
func TestEvaluateRemainder(t *testing.T) {
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
	v, rem, err := sess.Evaluate(ctx, "1+2 trailing", gokd.ValueInt64)
	if err != nil {
		t.Fatalf("Evaluate(1+2 trailing) failed: %v", err)
	}
	if v.U64 != 3 {
		t.Errorf("U64 = 0x%x, want 3", v.U64)
	}
	if rem == 0 {
		t.Errorf("expected non-zero remainder, got 0")
	} else {
		t.Logf("remainder=%d", rem)
	}
}
