package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nijosmsft/gokd"
)

// ringEvent is one entry in srv.eventRing. Kind is a string discriminator
// so JSON clients can pick out specific event types without parsing the
// raw payload. The payload carries the per-event fields formatted the same
// way as the rest of the MCP layer (no internal types leak through).
type ringEvent struct {
	Seq         uint64    `json:"seq"`
	At          time.Time `json:"at"`
	Kind        string    `json:"kind"`
	Description string    `json:"description"`
	AddressHex  string    `json:"address_hex,omitempty"`
	ThreadID    uint32    `json:"thread_id,omitempty"`
	Payload     any       `json:"payload,omitempty"`
}

// ringOutput is one entry in srv.outputRing. A single dbgeng output line
// (already line-buffered by the gokd Session) becomes one ringOutput.
type ringOutput struct {
	Seq  uint64    `json:"seq"`
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// sessionStatus tracks the last known target lifecycle marker. The drainer
// updates this on Process* / module / break events; get_session_state
// reads it.
type sessionStatus struct {
	mu         sync.Mutex
	targetKind string // "user", "kernel", "dump", "remote", ""
	targetName string // free-form, e.g. "notepad.exe (pid 1234)"
	lastKind   string // "attached", "detached", "broken_in", "running", "exited"
}

func (s *sessionStatus) snapshot() (kind, name, last string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.targetKind, s.targetName, s.lastKind
}

func (s *sessionStatus) set(kind, name, last string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind != "" {
		s.targetKind = kind
	}
	if name != "" {
		s.targetName = name
	}
	if last != "" {
		s.lastKind = last
	}
}

// pushEvent converts a gokd.Event into a ringEvent and stores it in
// s.eventRing. Returns the stored entry (with its assigned Seq) so
// callers can broadcast it.
func (s *srv) pushEvent(ev gokd.Event) ringEvent {
	if s == nil || s.eventRing == nil {
		return ringEvent{}
	}
	template := ringEvent{At: time.Now().UTC()}
	switch e := ev.(type) {
	case gokd.BreakpointEvent:
		template.Kind = "breakpoint"
		template.Description = fmt.Sprintf("breakpoint %d at %s", e.ID, hex64(e.Address))
		template.AddressHex = hex64(e.Address)
		if e.Thread != nil {
			template.ThreadID = e.Thread.SystemID
		}
		if s.status != nil {
			s.status.set("", "", "broken_in")
		}
	case gokd.ExceptionEvent:
		template.Kind = "exception"
		chance := "second"
		if e.FirstChance {
			chance = "first"
		}
		template.Description = fmt.Sprintf("exception 0x%08x at %s %s-chance", e.Code, hex64(e.Address), chance)
		template.AddressHex = hex64(e.Address)
		if e.Thread != nil {
			template.ThreadID = e.Thread.SystemID
		}
		if s.status != nil {
			s.status.set("", "", "broken_in")
		}
	case gokd.ProcessCreatedEvent:
		template.Kind = "process_created"
		template.Description = fmt.Sprintf("process created %s", e.ImageName)
		template.AddressHex = hex64(e.BaseOffset)
		if s.status != nil {
			s.status.set("", e.ImageName, "attached")
		}
	case gokd.ProcessExitedEvent:
		template.Kind = "process_exited"
		template.Description = fmt.Sprintf("process exited code=%d", e.ExitCode)
		if s.status != nil {
			s.status.set("", "", "exited")
		}
	case gokd.ModuleLoadedEvent:
		template.Kind = "module_loaded"
		if e.Module != nil {
			template.Description = fmt.Sprintf("module loaded %s base=%s size=0x%x", e.Module.Name, hex64(e.Module.Base), e.Module.Size)
			template.AddressHex = hex64(e.Module.Base)
		}
	case gokd.ModuleUnloadedEvent:
		template.Kind = "module_unloaded"
		template.Description = fmt.Sprintf("module unloaded %s base=%s", e.ImageBaseName, hex64(e.BaseOffset))
		template.AddressHex = hex64(e.BaseOffset)
	case gokd.ThreadCreatedEvent:
		template.Kind = "thread_created"
		if e.Thread != nil {
			template.Description = fmt.Sprintf("thread created sysid=%d start=%s", e.Thread.SystemID, hex64(e.Thread.StartOffset))
			template.ThreadID = e.Thread.SystemID
		}
	case gokd.ThreadExitedEvent:
		template.Kind = "thread_exited"
		template.Description = fmt.Sprintf("thread exited sysid=%d code=%d", e.SystemID, e.ExitCode)
		template.ThreadID = e.SystemID
	default:
		template.Kind = fmt.Sprintf("%T", ev)
		template.Description = template.Kind
	}
	var stored ringEvent
	s.eventRing.PushWith(func(seq uint64) ringEvent {
		template.Seq = seq
		stored = template
		return template
	})
	return stored
}

// pushOutput stores a single output line in s.outputRing and returns the
// stored entry.
func (s *srv) pushOutput(line string) ringOutput {
	if s == nil || s.outputRing == nil {
		return ringOutput{}
	}
	var stored ringOutput
	s.outputRing.PushWith(func(seq uint64) ringOutput {
		stored = ringOutput{Seq: seq, At: time.Now().UTC(), Text: line}
		return stored
	})
	return stored
}

// drainer pumps the Session's event and output channels into the srv's
// ring buffers and (optionally) broadcasts them as MCP logging
// notifications to every connected ServerSession on every registered
// *mcp.Server. The set of servers is mutable: the listen loop calls
// d.addServer(...) for every new connection.
type drainer struct {
	s      *srv
	logger *log.Logger

	mu      sync.Mutex
	servers []*mcp.Server

	outBatch outputBatcher
	prevLast string
}

// outputBatcher coalesces output lines that arrive within a short window
// into a single notification, to keep noisy targets from saturating the
// notification channel. The buffer flushes when:
//   - the running byte count reaches outputCap, OR
//   - the window timer fires (outputWindow after the first buffered line).
type outputBatcher struct {
	mu      sync.Mutex
	buf     []ringOutput
	bytes   int
	timer   *time.Timer
	pending bool
}

const (
	outputWindow = 100 * time.Millisecond
	outputCap    = 4096
)

func newDrainer(s *srv, logger *log.Logger) *drainer {
	return &drainer{s: s, logger: logger}
}

// addServer registers an *mcp.Server with the drainer. Broadcasts after
// this call fan out to every ServerSession owned by sv, in addition to
// any previously registered servers.
func (d *drainer) addServer(sv *mcp.Server) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = append(d.servers, sv)
}

func (d *drainer) run(sess gokd.Session) {
	go func() {
		for ev := range sess.Events() {
			entry := d.s.pushEvent(ev)
			if d.logger != nil {
				d.logger.Printf("[event] %s", entry.Description)
			}
			d.broadcast(context.Background(), &mcp.LoggingMessageParams{
				Logger: "gokd/event",
				Level:  "info",
				Data:   entry,
			})
			d.maybeStateChanged(context.Background())
		}
	}()
	go func() {
		for out := range sess.Output() {
			entry := d.s.pushOutput(out)
			if d.logger != nil {
				d.logger.Print(entry.Text)
			}
			d.queueOutput(entry)
		}
	}()
}

// broadcast fires a logging notification to every ServerSession on every
// registered server. Errors (closed sessions, slow clients, no subscriber)
// are intentionally swallowed: a debugger drainer must not block on a
// misbehaving client.
func (d *drainer) broadcast(ctx context.Context, params *mcp.LoggingMessageParams) {
	d.mu.Lock()
	servers := append([]*mcp.Server(nil), d.servers...)
	d.mu.Unlock()
	for _, sv := range servers {
		for ss := range sv.Sessions() {
			_ = ss.Log(ctx, params)
		}
	}
}

// queueOutput appends one ringOutput to the in-flight batch and arms (or
// re-arms) the flush timer. If appending pushes the batch over outputCap,
// flush immediately.
func (d *drainer) queueOutput(entry ringOutput) {
	d.outBatch.mu.Lock()
	d.outBatch.buf = append(d.outBatch.buf, entry)
	d.outBatch.bytes += len(entry.Text)
	overflow := d.outBatch.bytes >= outputCap
	if !d.outBatch.pending {
		d.outBatch.pending = true
		d.outBatch.timer = time.AfterFunc(outputWindow, func() {
			d.flushOutput()
		})
	}
	d.outBatch.mu.Unlock()
	if overflow {
		d.flushOutput()
	}
}

// flushOutput drains the current batch into one notification. Safe to
// call concurrently from the cap-exceeded path and from the timer.
func (d *drainer) flushOutput() {
	d.outBatch.mu.Lock()
	if len(d.outBatch.buf) == 0 {
		d.outBatch.pending = false
		d.outBatch.mu.Unlock()
		return
	}
	batch := d.outBatch.buf
	d.outBatch.buf = nil
	d.outBatch.bytes = 0
	d.outBatch.pending = false
	if d.outBatch.timer != nil {
		d.outBatch.timer.Stop()
		d.outBatch.timer = nil
	}
	d.outBatch.mu.Unlock()

	d.broadcast(context.Background(), &mcp.LoggingMessageParams{
		Logger: "gokd/output",
		Level:  "debug",
		Data:   batch,
	})
}

// stateChangedEntry is the payload of a gokd/state_changed notification.
type stateChangedEntry struct {
	Kind       string    `json:"kind"`
	At         time.Time `json:"at"`
	TargetKind string    `json:"target_kind,omitempty"`
	TargetName string    `json:"target_name,omitempty"`
}

// maybeStateChanged emits a synthetic notification when sessionStatus.lastKind
// transitions to a new value. Tracked locally on the drainer so we don't
// need to mutate srv state on every event.
func (d *drainer) maybeStateChanged(ctx context.Context) {
	if d.s == nil || d.s.status == nil {
		return
	}
	kind, name, last := d.s.status.snapshot()
	if last == "" || last == d.prevLast {
		return
	}
	d.prevLast = last
	d.broadcast(ctx, &mcp.LoggingMessageParams{
		Logger: "gokd/state_changed",
		Level:  "info",
		Data: stateChangedEntry{
			Kind:       last,
			At:         time.Now().UTC(),
			TargetKind: kind,
			TargetName: name,
		},
	})
}
