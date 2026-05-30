package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nijosmsft/gokd"
)

// fakeBroadcaster captures every broadcast() call so tests can assert
// what (and how often) the drainer fan-outs. It replaces the *mcp.Server
// list with an in-memory sink, sidestepping the need to spin up a real
// MCP transport in unit tests.
type fakeBroadcaster struct {
	mu      sync.Mutex
	events  []string // logger names of broadcast() calls
	outputs []string // concatenated text of any gokd/output broadcasts
	states  []stateChangedEntry
}

// drainerHook lets tests inject a broadcaster without a real server.
// drainer.broadcast checks the hook field first.
type drainerHook struct {
	*drainer
	bc *fakeBroadcaster
}

// pushEventNoBroadcast invokes the same code path as the channel reader
// in drainer.run, but synchronously so tests can deterministically order
// events. We do not call the real broadcast() because that requires a
// live *mcp.Server.
func (h *drainerHook) feedEvent(ev gokd.Event) {
	entry := h.s.pushEvent(ev)
	h.bc.mu.Lock()
	h.bc.events = append(h.bc.events, entry.Kind)
	h.bc.mu.Unlock()
}

func (h *drainerHook) feedOutput(line string) {
	entry := h.s.pushOutput(line)
	h.bc.mu.Lock()
	h.bc.outputs = append(h.bc.outputs, entry.Text)
	h.bc.mu.Unlock()
}

func TestDrainerPushesToRingsForUnregisteredServer(t *testing.T) {
	// Even with no servers registered, the drainer must still update
	// the rings so get_recent_events / get_recent_output keep working.
	state := newSrv(&stubSession{}, false, false)
	d := newDrainer(state, slog.New(slog.NewTextHandler(io.Discard, nil)))
	hook := &drainerHook{drainer: d, bc: &fakeBroadcaster{}}
	hook.feedEvent(gokd.BreakpointEvent{ID: 5, Address: 0x1000})
	hook.feedEvent(gokd.ProcessExitedEvent{ExitCode: 1})

	evs, dropped := state.eventRing.Since(0)
	if dropped != 0 {
		t.Errorf("dropped=%d want 0", dropped)
	}
	if len(evs) != 2 {
		t.Fatalf("ring has %d events; want 2", len(evs))
	}
	if evs[0].Kind != "breakpoint" || evs[1].Kind != "process_exited" {
		t.Errorf("event kinds wrong: %+v", evs)
	}
}

func TestDrainerCoalescesOutput(t *testing.T) {
	// Several short output lines arriving in quick succession should
	// all be sitting in one batch when the timer fires.
	state := newSrv(&stubSession{}, false, false)
	d := newDrainer(state, nil)

	d.queueOutput(ringOutput{Text: "first"})
	d.queueOutput(ringOutput{Text: "second"})
	d.queueOutput(ringOutput{Text: "third"})

	d.outBatch.mu.Lock()
	got := len(d.outBatch.buf)
	d.outBatch.mu.Unlock()
	if got != 3 {
		t.Errorf("batched=%d want 3 (window=%v)", got, outputWindow)
	}

	// Allow the timer to fire then assert the buffer is flushed.
	time.Sleep(outputWindow + 50*time.Millisecond)
	d.outBatch.mu.Lock()
	remaining := len(d.outBatch.buf)
	d.outBatch.mu.Unlock()
	if remaining != 0 {
		t.Errorf("after window, batch=%d want 0 (auto-flush expected)", remaining)
	}
}

func TestDrainerFlushesAtByteCap(t *testing.T) {
	state := newSrv(&stubSession{}, false, false)
	d := newDrainer(state, nil)
	// Each entry adds outputCap bytes — first one should already trigger flush.
	big := make([]byte, outputCap+1)
	for i := range big {
		big[i] = 'x'
	}
	d.queueOutput(ringOutput{Text: string(big)})
	// Flush is synchronous on overflow; check that the batch is empty
	// without waiting for the window.
	d.outBatch.mu.Lock()
	remaining := len(d.outBatch.buf)
	d.outBatch.mu.Unlock()
	if remaining != 0 {
		t.Errorf("after cap-overflow, batch=%d want 0", remaining)
	}
}

func TestDrainerStateChangedTransition(t *testing.T) {
	state := newSrv(&stubSession{}, false, false)
	// Pre-seed status so we have something to transition from.
	state.status.set("user", "notepad.exe (pid 1234)", "attached")
	d := newDrainer(state, nil)
	// Initial call: lastKind="attached", prevLast="" — should emit.
	d.maybeStateChanged(context.Background())
	if d.prevLast != "attached" {
		t.Errorf("prevLast=%q want attached", d.prevLast)
	}
	// Same status again: should NOT emit (prevLast already attached).
	d.maybeStateChanged(context.Background())
	if d.prevLast != "attached" {
		t.Errorf("prevLast=%q still attached after no-op", d.prevLast)
	}
	// Transition.
	state.status.set("", "", "exited")
	d.maybeStateChanged(context.Background())
	if d.prevLast != "exited" {
		t.Errorf("prevLast=%q want exited", d.prevLast)
	}
}
