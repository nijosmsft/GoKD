package dbgcgo

/*
#include "gokd_shim.h"

// Forward declarations for the Go callback trampolines.
// Note: CGo exports do not support const qualifiers, so we use non-const here.
extern void goEventCallback(gokd_session_t s, int event_type,
                             void *event_data, void *ctx);
extern void goOutputCallback(uint32_t mask, char *text, void *ctx);
*/
import "C"

import (
	"sync"
	"unsafe"
)

// ── Event types (mirrors GOKD_EVENT_* constants) ──────────────────────

const (
	EventBreakpoint    = C.GOKD_EVENT_BREAKPOINT
	EventException     = C.GOKD_EVENT_EXCEPTION
	EventThreadCreated = C.GOKD_EVENT_THREAD_CREATED
	EventThreadExited  = C.GOKD_EVENT_THREAD_EXITED
	EventProcCreated   = C.GOKD_EVENT_PROC_CREATED
	EventProcExited    = C.GOKD_EVENT_PROC_EXITED
	EventModLoaded     = C.GOKD_EVENT_MOD_LOADED
	EventModUnloaded   = C.GOKD_EVENT_MOD_UNLOADED
)

// ── Event data types ──────────────────────────────────────────────────

type EvBreakpoint struct {
	BPID        uint32
	Address     uint64
	ThreadSysID uint32
}

type EvException struct {
	Code        uint32
	Address     uint64
	FirstChance uint32
	ThreadSysID uint32
}

type EvThreadCreated struct {
	SysID       uint32
	Handle      uint64
	DataOffset  uint64
	StartOffset uint64
}

type EvThreadExited struct {
	SysID    uint32
	ExitCode uint32
}

type EvProcCreated struct {
	BaseOffset uint64
	ModuleSize uint32
	ModuleName string
	ImageName  string
}

type EvProcExited struct {
	ExitCode uint32
}

type EvModLoaded struct {
	BaseOffset uint64
	ModuleSize uint32
	ModuleName string
	ImageName  string
}

type EvModUnloaded struct {
	BaseOffset    uint64
	ImageBaseName string
}

// Event is a tagged union delivered on the event channel.
type Event struct {
	Type int
	Data interface{} // one of Ev* types above
}

// ── Registry: map session handles to Go channels ──────────────────────

var (
	cbMu       sync.RWMutex
	eventChans = make(map[C.gokd_session_t]chan Event)
	outputChans = make(map[C.gokd_session_t]chan string)
)

// RegisterCallbacks installs the Go callback trampolines on the session
// and returns channels for events and output. Must be called from the
// dispatch goroutine (inside s.exec).
func (s *Session) RegisterCallbacks(eventBuf, outputBuf int) (<-chan Event, <-chan string) {
	evCh := make(chan Event, eventBuf)
	outCh := make(chan string, outputBuf)

	cbMu.Lock()
	eventChans[s.handle] = evCh
	outputChans[s.handle] = outCh
	cbMu.Unlock()

	s.exec(func() {
		C.gokd_set_event_callback(s.handle,
			C.gokd_event_fn(C.goEventCallback), nil)
		C.gokd_set_output_callback(s.handle,
			C.gokd_output_fn(C.goOutputCallback), nil)
	})

	return evCh, outCh
}

// UnregisterCallbacks removes the channels and clears the C callbacks.
func (s *Session) UnregisterCallbacks() {
	s.exec(func() {
		C.gokd_set_event_callback(s.handle, nil, nil)
		C.gokd_set_output_callback(s.handle, nil, nil)
	})

	cbMu.Lock()
	if ch, ok := eventChans[s.handle]; ok {
		close(ch)
		delete(eventChans, s.handle)
	}
	if ch, ok := outputChans[s.handle]; ok {
		close(ch)
		delete(outputChans, s.handle)
	}
	cbMu.Unlock()
}

// ── CGo export trampolines ────────────────────────────────────────────
// These are called from C (on the dispatch thread) and must not block.

//export goEventCallback
func goEventCallback(s C.gokd_session_t, eventType C.int,
	eventData unsafe.Pointer, ctx unsafe.Pointer) {

	cbMu.RLock()
	ch, ok := eventChans[s]
	cbMu.RUnlock()
	if !ok || ch == nil {
		return
	}

	var ev Event
	ev.Type = int(eventType)

	switch int(eventType) {
	case EventBreakpoint:
		d := (*C.gokd_ev_breakpoint_t)(eventData)
		ev.Data = EvBreakpoint{
			BPID:        uint32(d.bp_id),
			Address:     uint64(d.address),
			ThreadSysID: uint32(d.thread_sys_id),
		}
	case EventException:
		d := (*C.gokd_ev_exception_t)(eventData)
		ev.Data = EvException{
			Code:        uint32(d.code),
			Address:     uint64(d.address),
			FirstChance: uint32(d.first_chance),
			ThreadSysID: uint32(d.thread_sys_id),
		}
	case EventThreadCreated:
		d := (*C.gokd_ev_thread_created_t)(eventData)
		ev.Data = EvThreadCreated{
			SysID:       uint32(d.sys_id),
			Handle:      uint64(d.handle),
			DataOffset:  uint64(d.data_offset),
			StartOffset: uint64(d.start_offset),
		}
	case EventThreadExited:
		d := (*C.gokd_ev_thread_exited_t)(eventData)
		ev.Data = EvThreadExited{
			SysID:    uint32(d.sys_id),
			ExitCode: uint32(d.exit_code),
		}
	case EventProcCreated:
		d := (*C.gokd_ev_proc_created_t)(eventData)
		ev.Data = EvProcCreated{
			BaseOffset: uint64(d.base_offset),
			ModuleSize: uint32(d.module_size),
			ModuleName: C.GoString(&d.module_name[0]),
			ImageName:  C.GoString(&d.image_name[0]),
		}
	case EventProcExited:
		d := (*C.gokd_ev_proc_exited_t)(eventData)
		ev.Data = EvProcExited{ExitCode: uint32(d.exit_code)}
	case EventModLoaded:
		d := (*C.gokd_ev_mod_loaded_t)(eventData)
		ev.Data = EvModLoaded{
			BaseOffset: uint64(d.base_offset),
			ModuleSize: uint32(d.module_size),
			ModuleName: C.GoString(&d.module_name[0]),
			ImageName:  C.GoString(&d.image_name[0]),
		}
	case EventModUnloaded:
		d := (*C.gokd_ev_mod_unloaded_t)(eventData)
		ev.Data = EvModUnloaded{
			BaseOffset:    uint64(d.base_offset),
			ImageBaseName: C.GoString(&d.image_base_name[0]),
		}
	default:
		return
	}

	// Non-blocking send: drop if channel is full.
	select {
	case ch <- ev:
	default:
	}
}

//export goOutputCallback
func goOutputCallback(mask C.uint32_t, text *C.char, ctx unsafe.Pointer) {
	// The session handle is not passed directly to the output callback.
	// We broadcast to all sessions. For multi-session support this
	// would need refinement, but single-session is the common case.
	cbMu.RLock()
	defer cbMu.RUnlock()
	goText := C.GoString(text)
	for _, ch := range outputChans {
		select {
		case ch <- goText:
		default:
		}
	}
}
