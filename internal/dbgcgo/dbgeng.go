// Package dbgcgo provides low-level CGo bindings to the GoKD C++ shim.
// This package is internal — consumers should use the public pkg/ API.
//
// All DbgEng operations must run on a single OS thread (the thread that
// called DebugCreate). This package provides a dispatch mechanism:
// a goroutine pinned with runtime.LockOSThread that creates the session
// and processes all commands sequentially.

package dbgcgo

/*
#cgo CFLAGS:  -I${SRCDIR}/../../cshim
#cgo LDFLAGS: -L${SRCDIR}/../../cshim/lib -lgokd_shim
#cgo LDFLAGS: -ldbghelp -lole32 -luuid -lstdc++

#include "gokd_shim.h"
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"
)

// ErrSessionClosed is returned when an operation is attempted on a
// Session whose Close has already completed (or is in progress).
var ErrSessionClosed = errors.New("gokd: session is closed")

// Session wraps a gokd_session_t handle and the dispatch goroutine.
type Session struct {
	handle C.gokd_session_t
	cmdCh  chan command
	done   chan struct{}
	once   sync.Once
	closed atomic.Bool
}

type command struct {
	fn     func()
	result chan struct{}
}

// NewSession creates a new debug session. It starts the dispatch goroutine,
// initialises COM, and calls DebugCreate on the pinned OS thread.
func NewSession() (*Session, error) {
	s := &Session{
		cmdCh: make(chan command, 64),
		done:  make(chan struct{}),
	}

	readyCh := make(chan error, 1)

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		handle := C.gokd_create_session()
		if handle == 0 {
			readyCh <- fmt.Errorf("gokd_create_session failed")
			return
		}
		s.handle = handle
		readyCh <- nil

		// Dispatch loop: execute commands sequentially on this thread.
		for cmd := range s.cmdCh {
			cmd.fn()
			close(cmd.result)
		}

		C.gokd_destroy_session(s.handle)
		close(s.done)
	}()

	if err := <-readyCh; err != nil {
		return nil, err
	}
	return s, nil
}

// Close shuts down the dispatch goroutine and destroys the session.
// Safe to call multiple times; subsequent calls are no-ops.
func (s *Session) Close() {
	s.once.Do(func() {
		// Mark closed BEFORE closing cmdCh so any concurrent exec
		// observes the flag and returns ErrSessionClosed instead of
		// panicking on a send to a closed channel.
		s.closed.Store(true)
		close(s.cmdCh)
		<-s.done
	})
}

// IsClosed reports whether Close has been called.
func (s *Session) IsClosed() bool { return s.closed.Load() }

// exec runs fn on the dispatch thread and blocks until it completes.
// Returns ErrSessionClosed if the session has been closed.
// Recovers from the rare race where the session is closed between the
// closed-check and the send on cmdCh.
func (s *Session) exec(fn func()) error {
	if s.closed.Load() {
		return ErrSessionClosed
	}
	cmd := command{fn: fn, result: make(chan struct{})}
	sent := func() (ok bool) {
		defer func() {
			if recover() != nil {
				ok = false
			}
		}()
		s.cmdCh <- cmd
		return true
	}()
	if !sent {
		return ErrSessionClosed
	}
	<-cmd.result
	return nil
}

// HRESULTError wraps a non-success HRESULT value. It implements the
// error interface; the formatted form is "HRESULT 0x%08x". Callers can
// distinguish well-known sentinels via errors.Is — see ErrTimeout.
type HRESULTError int32

// Error implements the error interface.
func (h HRESULTError) Error() string {
	return fmt.Sprintf("HRESULT 0x%08x", uint32(h))
}

// HRESULT returns the raw 32-bit HRESULT value.
func (h HRESULTError) HRESULT() int32 { return int32(h) }

// ErrTimeout indicates a WaitForEvent overall-timeout. Internally the
// shim returns S_FALSE (0x00000001) for this case; without an explicit
// sentinel the old hresult() silently returned nil and the dispatch
// thread looked like it had completed successfully — every downstream
// call then failed with E_UNEXPECTED.
var ErrTimeout = HRESULTError(0x1)

// hresult converts a C int32_t HRESULT to a Go error.
//
//	S_OK (0)      → nil
//	S_FALSE (1)   → ErrTimeout
//	any other hr  → HRESULTError(hr) (covers both other "success"-coded
//	                informational HRESULTs and hard failures).
func hresult(hr C.int32_t) error {
	switch hr {
	case 0:
		return nil
	case 1:
		return ErrTimeout
	default:
		return HRESULTError(hr)
	}
}

// ── Attach ────────────────────────────────────────────────────────────

func (s *Session) AttachProcess(pid uint32, flags uint32) error {
	var hr C.int32_t
	s.exec(func() {
		hr = C.gokd_attach_process(s.handle, C.uint32_t(pid), C.uint32_t(flags))
	})
	return hresult(hr)
}

func (s *Session) CreateProcess(cmd string, flags uint32, initialBreak bool) error {
	var hr C.int32_t
	ib := C.int(0)
	if initialBreak {
		ib = 1
	}
	s.exec(func() {
		ccmd := C.CString(cmd)
		defer C.free(unsafe.Pointer(ccmd))
		hr = C.gokd_create_process(s.handle, ccmd, C.uint32_t(flags), ib)
	})
	return hresult(hr)
}

func (s *Session) AttachKernel(options string, flags uint32) error {
	var hr C.int32_t
	s.exec(func() {
		copts := C.CString(options)
		defer C.free(unsafe.Pointer(copts))
		hr = C.gokd_attach_kernel(s.handle, copts, C.uint32_t(flags))
	})
	return hresult(hr)
}

func (s *Session) OpenDump(path string) error {
	var hr C.int32_t
	s.exec(func() {
		cpath := C.CString(path)
		defer C.free(unsafe.Pointer(cpath))
		hr = C.gokd_open_dump(s.handle, cpath)
	})
	return hresult(hr)
}

func (s *Session) Detach() error {
	var hr C.int32_t
	s.exec(func() {
		hr = C.gokd_detach(s.handle)
	})
	return hresult(hr)
}

// ── Remote debugging ──────────────────────────────────────────────

func (s *Session) ConnectRemote(connection string) error {
	var hr C.int32_t
	s.exec(func() {
		cconn := C.CString(connection)
		defer C.free(unsafe.Pointer(cconn))
		hr = C.gokd_connect_remote(s.handle, cconn)
	})
	return hresult(hr)
}

func (s *Session) DisconnectRemote() error {
	var hr C.int32_t
	s.exec(func() {
		hr = C.gokd_disconnect_remote(s.handle)
	})
	return hresult(hr)
}

// CancelWait requests cancellation of a pending WaitForEvent.
// Safe to call from any goroutine.
func (s *Session) CancelWait() {
	C.gokd_cancel_wait(s.handle)
}

// ── Execution ─────────────────────────────────────────────────────────

// StopEvent mirrors gokd_stop_event_t.
type StopEvent struct {
	Reason               uint32
	Address              uint64
	ThreadSystemID       uint32
	ExceptionCode        uint32
	ExceptionFirstChance uint32
}

func stopEventFromC(cev C.gokd_stop_event_t) StopEvent {
	return StopEvent{
		Reason:               uint32(cev.reason),
		Address:              uint64(cev.address),
		ThreadSystemID:       uint32(cev.thread_sys_id),
		ExceptionCode:        uint32(cev.exception_code),
		ExceptionFirstChance: uint32(cev.exception_first_chance),
	}
}

func (s *Session) Go() (StopEvent, error) {
	var hr C.int32_t
	var cev C.gokd_stop_event_t
	s.exec(func() {
		hr = C.gokd_go(s.handle, &cev)
	})
	return stopEventFromC(cev), hresult(hr)
}

func (s *Session) StepIn() (StopEvent, error) {
	var hr C.int32_t
	var cev C.gokd_stop_event_t
	s.exec(func() {
		hr = C.gokd_step_in(s.handle, &cev)
	})
	return stopEventFromC(cev), hresult(hr)
}

func (s *Session) StepOver() (StopEvent, error) {
	var hr C.int32_t
	var cev C.gokd_stop_event_t
	s.exec(func() {
		hr = C.gokd_step_over(s.handle, &cev)
	})
	return stopEventFromC(cev), hresult(hr)
}

func (s *Session) StepOut() (StopEvent, error) {
	var hr C.int32_t
	var cev C.gokd_stop_event_t
	s.exec(func() {
		hr = C.gokd_step_out(s.handle, &cev)
	})
	return stopEventFromC(cev), hresult(hr)
}

// BreakIn is safe to call from any goroutine — it does NOT go through
// the dispatch queue because SetInterrupt is thread-safe.
func (s *Session) BreakIn() error {
	hr := C.gokd_break_in(s.handle)
	return hresult(hr)
}

// ── Memory ────────────────────────────────────────────────────────────

func (s *Session) ReadVirtual(addr uint64, n uint64) ([]byte, error) {
	buf := make([]byte, n)
	var bytesRead C.size_t
	var hr C.int32_t
	s.exec(func() {
		hr = C.gokd_read_virtual(s.handle, C.uint64_t(addr),
			unsafe.Pointer(&buf[0]), C.size_t(n), &bytesRead)
	})
	return buf[:int(bytesRead)], hresult(hr)
}

func (s *Session) WriteVirtual(addr uint64, data []byte) error {
	var hr C.int32_t
	s.exec(func() {
		hr = C.gokd_write_virtual(s.handle, C.uint64_t(addr),
			unsafe.Pointer(&data[0]), C.size_t(len(data)))
	})
	return hresult(hr)
}

func (s *Session) ReadPhysical(addr uint64, n uint64) ([]byte, error) {
	buf := make([]byte, n)
	var bytesRead C.size_t
	var hr C.int32_t
	s.exec(func() {
		hr = C.gokd_read_physical(s.handle, C.uint64_t(addr),
			unsafe.Pointer(&buf[0]), C.size_t(n), &bytesRead)
	})
	return buf[:int(bytesRead)], hresult(hr)
}

// ── Registers ─────────────────────────────────────────────────────────

// Register mirrors gokd_register_t.
type Register struct {
	Name  string
	Value uint64
	Type  uint32
	Valid bool
}

func (s *Session) GetRegisters() ([]Register, error) {
	// First call to get count.
	var count C.uint32_t
	var hr C.int32_t
	s.exec(func() {
		hr = C.gokd_get_registers(s.handle, nil, &count)
	})
	if err := hresult(hr); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}

	cregs := make([]C.gokd_register_t, int(count))
	s.exec(func() {
		hr = C.gokd_get_registers(s.handle, &cregs[0], &count)
	})
	if err := hresult(hr); err != nil {
		return nil, err
	}

	regs := make([]Register, int(count))
	for i := 0; i < int(count); i++ {
		regs[i] = Register{
			Name:  C.GoString(&cregs[i].name[0]),
			Value: uint64(cregs[i].value),
			Type:  uint32(cregs[i]._type),
			Valid: cregs[i].valid != 0,
		}
	}
	return regs, nil
}

func (s *Session) SetRegister(name string, value uint64) error {
	var hr C.int32_t
	s.exec(func() {
		cname := C.CString(name)
		defer C.free(unsafe.Pointer(cname))
		hr = C.gokd_set_register(s.handle, cname, C.uint64_t(value))
	})
	return hresult(hr)
}

// ── Stack ─────────────────────────────────────────────────────────────

// Frame mirrors gokd_frame_t.
type Frame struct {
	InstructionOffset uint64
	ReturnOffset      uint64
	FrameOffset       uint64
	StackOffset       uint64
	Module            string
	Function          string
	Displacement      uint64
	SourceFile        string
	SourceLine        uint32
}

func (s *Session) GetStack(maxFrames uint32) ([]Frame, error) {
	cframes := make([]C.gokd_frame_t, maxFrames)
	var count C.uint32_t
	var hr C.int32_t
	s.exec(func() {
		hr = C.gokd_get_stack(s.handle, &cframes[0],
			C.uint32_t(maxFrames), &count)
	})
	if err := hresult(hr); err != nil {
		return nil, err
	}

	frames := make([]Frame, int(count))
	for i := 0; i < int(count); i++ {
		frames[i] = Frame{
			InstructionOffset: uint64(cframes[i].instruction_offset),
			ReturnOffset:      uint64(cframes[i].return_offset),
			FrameOffset:       uint64(cframes[i].frame_offset),
			StackOffset:       uint64(cframes[i].stack_offset),
			Module:            C.GoString(&cframes[i].module[0]),
			Function:          C.GoString(&cframes[i].function[0]),
			Displacement:      uint64(cframes[i].displacement),
			SourceFile:        C.GoString(&cframes[i].source_file[0]),
			SourceLine:        uint32(cframes[i].source_line),
		}
	}
	return frames, nil
}

// ── Symbols ───────────────────────────────────────────────────────────

func (s *Session) NameToAddr(name string) (uint64, error) {
	var addr C.uint64_t
	var hr C.int32_t
	s.exec(func() {
		cname := C.CString(name)
		defer C.free(unsafe.Pointer(cname))
		hr = C.gokd_name_to_addr(s.handle, cname, &addr)
	})
	return uint64(addr), hresult(hr)
}

func (s *Session) AddrToName(addr uint64) (string, uint64, error) {
	var displacement C.uint64_t
	var hr C.int32_t
	nameBuf := make([]byte, 1024)
	s.exec(func() {
		hr = C.gokd_addr_to_name(s.handle, C.uint64_t(addr),
			(*C.char)(unsafe.Pointer(&nameBuf[0])),
			C.size_t(len(nameBuf)), &displacement)
	})
	name := C.GoString((*C.char)(unsafe.Pointer(&nameBuf[0])))
	return name, uint64(displacement), hresult(hr)
}

func (s *Session) SetSymbolPath(path string) error {
	var hr C.int32_t
	s.exec(func() {
		cpath := C.CString(path)
		defer C.free(unsafe.Pointer(cpath))
		hr = C.gokd_set_symbol_path(s.handle, cpath)
	})
	return hresult(hr)
}

func (s *Session) GetSymbolPath() (string, error) {
	buf := make([]byte, 2048)
	var hr C.int32_t
	s.exec(func() {
		hr = C.gokd_get_symbol_path(s.handle,
			(*C.char)(unsafe.Pointer(&buf[0])), C.size_t(len(buf)))
	})
	return C.GoString((*C.char)(unsafe.Pointer(&buf[0]))), hresult(hr)
}

// ReloadSymbols forwards spec to IDebugSymbols3::ReloadWide. spec may be
// empty (reload anything stale), "/f" (force a full reload), or
// "/f module" (force a single module reload). May download from the
// symbol server — wrap in execWithCancel from the Go-public layer.
func (s *Session) ReloadSymbols(spec string) error {
	var hr C.int32_t
	s.exec(func() {
		cspec := C.CString(spec)
		defer C.free(unsafe.Pointer(cspec))
		hr = C.gokd_reload_symbols(s.handle, cspec)
	})
	return hresult(hr)
}

// ── Types ─────────────────────────────────────────────────────────────

func (s *Session) GetTypeSize(module, typeName string) (uint64, error) {
	var size C.uint64_t
	var hr C.int32_t
	s.exec(func() {
		cmod := C.CString(module)
		ctype := C.CString(typeName)
		defer C.free(unsafe.Pointer(cmod))
		defer C.free(unsafe.Pointer(ctype))
		hr = C.gokd_get_type_size(s.handle, cmod, ctype, &size)
	})
	return uint64(size), hresult(hr)
}

func (s *Session) GetFieldOffset(module, typeName, field string) (uint32, error) {
	var offset C.uint32_t
	var hr C.int32_t
	s.exec(func() {
		cmod := C.CString(module)
		ctype := C.CString(typeName)
		cfield := C.CString(field)
		defer C.free(unsafe.Pointer(cmod))
		defer C.free(unsafe.Pointer(ctype))
		defer C.free(unsafe.Pointer(cfield))
		hr = C.gokd_get_field_offset(s.handle, cmod, ctype, cfield, &offset)
	})
	return uint32(offset), hresult(hr)
}

// Field mirrors gokd_field_t.
type Field struct {
	Name     string
	Offset   uint32
	Size     uint64
	TypeName string
}

func (s *Session) GetTypeFields(module, typeName string) ([]Field, error) {
	// Get count first.
	var count C.uint32_t
	var hr C.int32_t
	s.exec(func() {
		cmod := C.CString(module)
		ctype := C.CString(typeName)
		defer C.free(unsafe.Pointer(cmod))
		defer C.free(unsafe.Pointer(ctype))
		hr = C.gokd_get_type_fields(s.handle, cmod, ctype, nil, 0, &count)
	})
	if err := hresult(hr); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}

	cfields := make([]C.gokd_field_t, int(count))
	s.exec(func() {
		cmod := C.CString(module)
		ctype := C.CString(typeName)
		defer C.free(unsafe.Pointer(cmod))
		defer C.free(unsafe.Pointer(ctype))
		hr = C.gokd_get_type_fields(s.handle, cmod, ctype,
			&cfields[0], count, &count)
	})
	if err := hresult(hr); err != nil {
		return nil, err
	}

	fields := make([]Field, int(count))
	for i := 0; i < int(count); i++ {
		fields[i] = Field{
			Name:     C.GoString(&cfields[i].name[0]),
			Offset:   uint32(cfields[i].offset),
			Size:     uint64(cfields[i].size),
			TypeName: C.GoString(&cfields[i].type_name[0]),
		}
	}
	return fields, nil
}

// ── Modules ───────────────────────────────────────────────────────────

// SymbolType mirrors DEBUG_SYMTYPE_* in dbgeng.h, as carried by
// DEBUG_MODULE_PARAMETERS::SymbolType.
type SymbolType uint32

const (
	SymbolTypeNone     SymbolType = 0
	SymbolTypeCOFF     SymbolType = 1
	SymbolTypeCodeView SymbolType = 2
	SymbolTypePDB      SymbolType = 3
	SymbolTypeExport   SymbolType = 4
	SymbolTypeDeferred SymbolType = 5
	SymbolTypeSym      SymbolType = 6
	SymbolTypeDIA      SymbolType = 7
)

// Module mirrors gokd_module_t.
type Module struct {
	Name       string
	ImageName  string
	Base       uint64
	Size       uint32
	Timestamp  uint32
	Checksum   uint32
	SymbolType SymbolType
}

func (s *Session) GetModules() ([]Module, error) {
	var count C.uint32_t
	var hr C.int32_t
	s.exec(func() {
		hr = C.gokd_get_modules(s.handle, nil, 0, &count)
	})
	if err := hresult(hr); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}

	cmods := make([]C.gokd_module_t, int(count))
	s.exec(func() {
		hr = C.gokd_get_modules(s.handle, &cmods[0], count, &count)
	})
	if err := hresult(hr); err != nil {
		return nil, err
	}

	mods := make([]Module, int(count))
	for i := 0; i < int(count); i++ {
		mods[i] = Module{
			Name:       C.GoString(&cmods[i].name[0]),
			ImageName:  C.GoString(&cmods[i].image_name[0]),
			Base:       uint64(cmods[i].base),
			Size:       uint32(cmods[i].size),
			Timestamp:  uint32(cmods[i].timestamp),
			Checksum:   uint32(cmods[i].checksum),
			SymbolType: SymbolType(cmods[i].symbol_type),
		}
	}
	return mods, nil
}

// ── Threads ───────────────────────────────────────────────────────────

// Thread mirrors gokd_thread_t.
type Thread struct {
	SystemID    uint32
	Handle      uint64
	DataOffset  uint64
	StartOffset uint64
}

func (s *Session) GetThreads() ([]Thread, error) {
	var count C.uint32_t
	var hr C.int32_t
	s.exec(func() {
		hr = C.gokd_get_threads(s.handle, nil, 0, &count)
	})
	if err := hresult(hr); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}

	cthreads := make([]C.gokd_thread_t, int(count))
	s.exec(func() {
		hr = C.gokd_get_threads(s.handle, &cthreads[0], count, &count)
	})
	if err := hresult(hr); err != nil {
		return nil, err
	}

	threads := make([]Thread, int(count))
	for i := 0; i < int(count); i++ {
		threads[i] = Thread{
			SystemID:    uint32(cthreads[i].system_id),
			Handle:      uint64(cthreads[i].handle),
			DataOffset:  uint64(cthreads[i].data_offset),
			StartOffset: uint64(cthreads[i].start_offset),
		}
	}
	return threads, nil
}

func (s *Session) SetCurrentThread(sysTID uint32) error {
	var hr C.int32_t
	s.exec(func() {
		hr = C.gokd_set_current_thread(s.handle, C.uint32_t(sysTID))
	})
	return hresult(hr)
}

// ── Breakpoints ───────────────────────────────────────────────────────

// Breakpoint mirrors gokd_bp_t.
type Breakpoint struct {
	ID         uint32
	Offset     uint64
	Expression string
	Flags      uint32
	Enabled    bool
}

func (s *Session) AddBreakpoint(addr uint64) (uint32, error) {
	var id C.uint32_t
	var hr C.int32_t
	s.exec(func() {
		hr = C.gokd_add_breakpoint(s.handle, C.uint64_t(addr), &id)
	})
	return uint32(id), hresult(hr)
}

func (s *Session) AddBreakpointSym(symbol string) (uint32, error) {
	var id C.uint32_t
	var hr C.int32_t
	s.exec(func() {
		csym := C.CString(symbol)
		defer C.free(unsafe.Pointer(csym))
		hr = C.gokd_add_breakpoint_sym(s.handle, csym, &id)
	})
	return uint32(id), hresult(hr)
}

func (s *Session) RemoveBreakpoint(id uint32) error {
	var hr C.int32_t
	s.exec(func() {
		hr = C.gokd_remove_breakpoint(s.handle, C.uint32_t(id))
	})
	return hresult(hr)
}

func (s *Session) EnableBreakpoint(id uint32, enabled bool) error {
	var hr C.int32_t
	en := C.int(0)
	if enabled {
		en = 1
	}
	s.exec(func() {
		hr = C.gokd_enable_breakpoint(s.handle, C.uint32_t(id), en)
	})
	return hresult(hr)
}

func (s *Session) ListBreakpoints() ([]Breakpoint, error) {
	var count C.uint32_t
	var hr C.int32_t
	s.exec(func() {
		hr = C.gokd_list_breakpoints(s.handle, nil, 0, &count)
	})
	if err := hresult(hr); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}

	cbps := make([]C.gokd_bp_t, int(count))
	s.exec(func() {
		hr = C.gokd_list_breakpoints(s.handle, &cbps[0], count, &count)
	})
	if err := hresult(hr); err != nil {
		return nil, err
	}

	bps := make([]Breakpoint, int(count))
	for i := 0; i < int(count); i++ {
		bps[i] = Breakpoint{
			ID:         uint32(cbps[i].id),
			Offset:     uint64(cbps[i].offset),
			Expression: C.GoString(&cbps[i].expression[0]),
			Flags:      uint32(cbps[i].flags),
			Enabled:    cbps[i].enabled != 0,
		}
	}
	return bps, nil
}

// ── Disassembly ───────────────────────────────────────────────────────

func (s *Session) Disassemble(addr uint64) (string, uint64, error) {
	buf := make([]byte, 1024)
	var nextAddr C.uint64_t
	var hr C.int32_t
	s.exec(func() {
		hr = C.gokd_disassemble(s.handle, C.uint64_t(addr),
			(*C.char)(unsafe.Pointer(&buf[0])), C.size_t(len(buf)),
			&nextAddr)
	})
	text := C.GoString((*C.char)(unsafe.Pointer(&buf[0])))
	return text, uint64(nextAddr), hresult(hr)
}

// ── Expression evaluation (t1-1) ──────────────────────────────────────

// ValueKind mirrors DEBUG_VALUE_* in dbgeng.h.
type ValueKind uint32

const (
	ValueInvalid   ValueKind = 0
	ValueInt8      ValueKind = 1
	ValueInt16     ValueKind = 2
	ValueInt32     ValueKind = 3
	ValueInt64     ValueKind = 4
	ValueFloat32   ValueKind = 5
	ValueFloat64   ValueKind = 6
	ValueFloat80   ValueKind = 7
	ValueFloat82   ValueKind = 8
	ValueFloat128  ValueKind = 9
	ValueVector64  ValueKind = 10
	ValueVector128 ValueKind = 11
)

// Value mirrors gokd_value_t. Type indicates which payload slot is
// authoritative; Raw always carries the full 24-byte DEBUG_VALUE union
// for callers that need float-80/82/128 or vector-64/128 fidelity.
type Value struct {
	Type ValueKind
	U64  uint64
	F64  float64
	Raw  [24]byte
}

// Evaluate parses an expression in the current syntax and returns the
// computed value. desired may be ValueInvalid for the engine's "natural"
// type. The second return is the byte index into expr where the parser
// stopped (0 means the whole expression was consumed).
func (s *Session) Evaluate(expr string, desired ValueKind) (Value, uint32, error) {
	var v Value
	var rem C.uint32_t
	var hr C.int32_t
	s.exec(func() {
		cexpr := C.CString(expr)
		defer C.free(unsafe.Pointer(cexpr))
		var cv C.gokd_value_t
		hr = C.gokd_evaluate(s.handle, cexpr, C.uint32_t(desired), &cv, &rem)
		if hr == 0 {
			v.Type = ValueKind(cv._type)
			v.U64 = uint64(cv.u64)
			v.F64 = float64(cv.f64)
			for i := 0; i < 24; i++ {
				v.Raw[i] = byte(cv.raw[i])
			}
		}
	})
	return v, uint32(rem), hresult(hr)
}

// Radix returns the current numeric radix (10 or 16 typically).
func (s *Session) Radix() (uint32, error) {
	var r C.uint32_t
	var hr C.int32_t
	s.exec(func() { hr = C.gokd_get_radix(s.handle, &r) })
	return uint32(r), hresult(hr)
}

// SetRadix sets the numeric radix used by Evaluate and DbgEng's
// formatting helpers.
func (s *Session) SetRadix(radix uint32) error {
	var hr C.int32_t
	s.exec(func() { hr = C.gokd_set_radix(s.handle, C.uint32_t(radix)) })
	return hresult(hr)
}

// ExpressionSyntax returns the current expression-syntax index
// (DEBUG_EXPR_MASM = 0, DEBUG_EXPR_CPLUSPLUS = 1).
func (s *Session) ExpressionSyntax() (uint32, error) {
	var idx C.uint32_t
	var hr C.int32_t
	s.exec(func() { hr = C.gokd_get_expression_syntax(s.handle, &idx) })
	return uint32(idx), hresult(hr)
}

// SetExpressionSyntax switches the expression syntax by name.
// name must be "MASM" or "C++".
func (s *Session) SetExpressionSyntax(name string) error {
	var hr C.int32_t
	s.exec(func() {
		cname := C.CString(name)
		defer C.free(unsafe.Pointer(cname))
		hr = C.gokd_set_expression_syntax(s.handle, cname)
	})
	return hresult(hr)
}

// ── Escape hatch ──────────────────────────────────────────────────────

func (s *Session) Execute(cmd string) (string, error) {
	buf := make([]byte, 8192)
	var hr C.int32_t
	s.exec(func() {
		ccmd := C.CString(cmd)
		defer C.free(unsafe.Pointer(ccmd))
		hr = C.gokd_execute(s.handle, ccmd,
			(*C.char)(unsafe.Pointer(&buf[0])), C.size_t(len(buf)))
	})
	text := C.GoString((*C.char)(unsafe.Pointer(&buf[0])))
	return text, hresult(hr)
}
