package main

import (
	"context"
	"testing"
	"time"

	"github.com/nijosmsft/gokd"
	"github.com/nijosmsft/gokd/internal/dbgcgo"
)

// stateStub lets a test control which error Threads() returns so
// probeStatus() can be exercised across the no_target / running /
// broken_in branches.
type stateStub struct {
	stubSession
	threadsErr error
	threads    []*gokd.Thread
	modules    []*gokd.Module
	bps        []*gokd.Breakpoint
	radix      uint32
	syntax     gokd.ExpressionSyntax
	sympath    string
}

func (s *stateStub) Threads() ([]*gokd.Thread, error) {
	if s.threadsErr != nil {
		return nil, s.threadsErr
	}
	return s.threads, nil
}
func (s *stateStub) Modules() ([]*gokd.Module, error)        { return s.modules, nil }
func (s *stateStub) Breakpoints() ([]*gokd.Breakpoint, error) { return s.bps, nil }
func (s *stateStub) Radix() (uint32, error)                  { return s.radix, nil }
func (s *stateStub) ExpressionSyntax() (gokd.ExpressionSyntax, error) {
	return s.syntax, nil
}
func (s *stateStub) SymbolPath() (string, error) { return s.sympath, nil }

func hresult(code uint32) error {
	return dbgcgo.HRESULTError(int32(code))
}

func TestGetSessionStateNoTarget(t *testing.T) {
	stub := &stateStub{threadsErr: hresult(hrNoTarget)}
	s := newSrv(stub, false, false)
	_, out, err := s.getSessionState(context.Background(), nil, struct{}{})
	if err != nil {
		t.Fatalf("getSessionState: %v", err)
	}
	if out.Attached {
		t.Errorf("attached=true, expected false")
	}
	if out.Status != "no_target" {
		t.Errorf("status=%q want no_target", out.Status)
	}
	if len(out.RecommendedNextTools) == 0 || out.RecommendedNextTools[0] != "attach_process" {
		t.Errorf("recommended_next_tools=%v want [attach_process, ...]", out.RecommendedNextTools)
	}
}

func TestGetSessionStateRunning(t *testing.T) {
	stub := &stateStub{threadsErr: hresult(hrTargetRunning)}
	s := newSrv(stub, false, false)
	_, out, err := s.getSessionState(context.Background(), nil, struct{}{})
	if err != nil {
		t.Fatalf("getSessionState: %v", err)
	}
	if !out.Attached {
		t.Errorf("attached=false, expected true")
	}
	if out.Status != "running" {
		t.Errorf("status=%q want running", out.Status)
	}
	if len(out.RecommendedNextTools) == 0 || out.RecommendedNextTools[0] != "break_in" {
		t.Errorf("first recommended=%q want break_in", firstTokenIfSet(out.RecommendedNextTools))
	}
}

func TestGetSessionStateBrokenIn(t *testing.T) {
	stub := &stateStub{
		threads: []*gokd.Thread{{SystemID: 1}, {SystemID: 2}},
		modules: []*gokd.Module{{Name: "ntdll"}},
		bps:     []*gokd.Breakpoint{{ID: 0}, {ID: 1}, {ID: 2}},
		radix:   16,
		syntax:  gokd.ExpressionSyntaxMASM,
		sympath: "srv*c:\\sym*https://msdl",
	}
	s := newSrv(stub, false, false)
	_, out, err := s.getSessionState(context.Background(), nil, struct{}{})
	if err != nil {
		t.Fatalf("getSessionState: %v", err)
	}
	if !out.Attached || out.Status != "broken_in" {
		t.Errorf("attached=%v status=%q want true/broken_in", out.Attached, out.Status)
	}
	if out.Threads != 2 || out.Modules != 1 || out.Breakpoints != 3 {
		t.Errorf("counts threads=%d modules=%d bps=%d want 2/1/3",
			out.Threads, out.Modules, out.Breakpoints)
	}
	if out.Radix != 16 || out.ExpressionSyntax != "masm" {
		t.Errorf("radix=%d syntax=%q", out.Radix, out.ExpressionSyntax)
	}
	if out.SymbolPath == "" {
		t.Errorf("symbol_path missing")
	}
	if len(out.RecommendedNextTools) == 0 {
		t.Errorf("no recommendations for broken_in status")
	}
}

func TestGetSessionStateExceptionRecommendsTriage(t *testing.T) {
	stub := &stateStub{
		threads: []*gokd.Thread{{SystemID: 1}},
		modules: []*gokd.Module{{Name: "ntdll"}},
	}
	s := newSrv(stub, false, false)
	// Inject an exception event so the recommendations branch fires.
	s.pushEvent(gokd.ExceptionEvent{Code: 0xC0000005, Address: 0x1000, FirstChance: true})
	_, out, _ := s.getSessionState(context.Background(), nil, struct{}{})
	if out.LastEvent == nil || out.LastEvent.Kind != "exception" {
		t.Fatalf("last_event missing or not exception: %+v", out.LastEvent)
	}
	gotTriage := false
	for _, tool := range out.RecommendedNextTools {
		if tool == "triage_crash" {
			gotTriage = true
		}
	}
	if !gotTriage {
		t.Errorf("expected triage_crash in recommendations, got %v", out.RecommendedNextTools)
	}
}

func TestGetSessionStateExitedFromStatus(t *testing.T) {
	stub := &stateStub{threads: []*gokd.Thread{{SystemID: 1}}}
	s := newSrv(stub, false, false)
	s.status.set("user", "notepad.exe", "exited")
	_, out, _ := s.getSessionState(context.Background(), nil, struct{}{})
	if out.Status != "exited" {
		t.Errorf("status=%q want exited (drainer wins over probe)", out.Status)
	}
}

// --- get_recent_events / get_recent_output ---

func TestGetRecentEventsEmpty(t *testing.T) {
	s := newSrv(&stubSession{}, false, false)
	_, out, _ := s.getRecentEvents(context.Background(), nil, getRecentEventsInput{})
	if len(out.Items) != 0 {
		t.Errorf("len=%d want 0", len(out.Items))
	}
	if out.NextToken != 0 {
		t.Errorf("next_token=%d want 0", out.NextToken)
	}
}

func TestGetRecentEventsPagination(t *testing.T) {
	s := newSrv(&stubSession{}, false, false)
	for i := 0; i < 10; i++ {
		s.pushEvent(gokd.BreakpointEvent{ID: uint32(i), Address: uint64(i)})
	}
	_, out, _ := s.getRecentEvents(context.Background(), nil, getRecentEventsInput{Limit: 4})
	if len(out.Items) != 4 {
		t.Errorf("len=%d want 4", len(out.Items))
	}
	if !out.Truncated {
		t.Errorf("expected truncated=true with limit 4 < 10 events")
	}
	if out.NextToken != out.Items[3].Seq {
		t.Errorf("next_token=%d want %d", out.NextToken, out.Items[3].Seq)
	}
	// Resume from next_token; should return next 4 events.
	_, out2, _ := s.getRecentEvents(context.Background(), nil, getRecentEventsInput{SinceToken: out.NextToken, Limit: 4})
	if len(out2.Items) != 4 || out2.Items[0].Seq != out.NextToken+1 {
		t.Errorf("second page: got %d items starting at seq %d, want 4 items starting at %d",
			len(out2.Items), out2.Items[0].Seq, out.NextToken+1)
	}
}

func TestGetRecentEventsDropped(t *testing.T) {
	s := newSrv(&stubSession{}, false, false)
	// Push more events than the ring can hold to force drops.
	for i := 0; i < eventRingCap+8; i++ {
		s.pushEvent(gokd.BreakpointEvent{ID: uint32(i)})
	}
	_, out, _ := s.getRecentEvents(context.Background(), nil, getRecentEventsInput{Limit: eventRingCap})
	if out.Dropped != 8 {
		t.Errorf("dropped=%d want 8 (%d pushes vs cap %d)", out.Dropped, eventRingCap+8, eventRingCap)
	}
}

func TestGetRecentOutputBasic(t *testing.T) {
	s := newSrv(&stubSession{}, false, false)
	s.pushOutput("line one")
	s.pushOutput("line two")
	_, out, _ := s.getRecentOutput(context.Background(), nil, getRecentOutputInput{})
	if len(out.Items) != 2 {
		t.Errorf("len=%d want 2", len(out.Items))
	}
	if out.Items[0].Text != "line one" {
		t.Errorf("items[0].text=%q want %q", out.Items[0].Text, "line one")
	}
	if !out.Items[0].At.Before(time.Now().Add(time.Minute)) {
		t.Errorf("items[0].at looks bogus: %v", out.Items[0].At)
	}
}
