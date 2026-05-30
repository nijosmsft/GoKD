package main

import (
	"fmt"
	"log"
	"sync"
	"time"

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
// ring buffers. It runs in its own goroutine for the life of the session.
// Tier 2 t2-6 will extend it to broadcast notifications; for now it is
// a strict superset of the previous startAsyncDrainers behaviour.
type drainer struct {
	s      *srv
	logger *log.Logger
}

func newDrainer(s *srv, logger *log.Logger) *drainer {
	return &drainer{s: s, logger: logger}
}

func (d *drainer) run(sess gokd.Session) {
	go func() {
		for ev := range sess.Events() {
			entry := d.s.pushEvent(ev)
			if d.logger != nil {
				d.logger.Printf("[event] %s", entry.Description)
			}
		}
	}()
	go func() {
		for out := range sess.Output() {
			entry := d.s.pushOutput(out)
			if d.logger != nil {
				d.logger.Print(entry.Text)
			}
		}
	}()
}
