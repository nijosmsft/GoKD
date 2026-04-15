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
	"fmt"
	"runtime"
	"sync"
	"unsafe"
)

// Session wraps a gokd_session_t handle and the dispatch goroutine.
type Session struct {
	handle C.gokd_session_t
	cmdCh  chan command
	done   chan struct{}
	once   sync.Once
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
func (s *Session) Close() {
	s.once.Do(func() {
		close(s.cmdCh)
		<-s.done
	})
}

// exec runs fn on the dispatch thread and blocks until it completes.
func (s *Session) exec(fn func()) {
	cmd := command{fn: fn, result: make(chan struct{})}
	s.cmdCh <- cmd
	<-cmd.result
}

// hresult converts a C int32_t HRESULT to a Go error (nil if S_OK).
func hresult(hr C.int32_t) error {
	if hr >= 0 {
		return nil
	}
	return fmt.Errorf("HRESULT 0x%08x", uint32(hr))
}

// ── Attach ────────────────────────────────────────────────────────────

func (s *Session) AttachProcess(pid uint32, flags uint32) error {
	var hr C.int32_t
	s.exec(func() {
		hr = C.gokd_attach_process(s.handle, C.uint32_t(pid), C.uint32_t(flags))
	})
	return hresult(hr)
}

func (s *Session) CreateProcess(cmd string, flags uint32) error {
	var hr C.int32_t
	s.exec(func() {
		ccmd := C.CString(cmd)
		defer C.free(unsafe.Pointer(ccmd))
		hr = C.gokd_create_process(s.handle, ccmd, C.uint32_t(flags))
	})
	return hresult(hr)
}

func (s *Session) AttachKernel(options string) error {
	var hr C.int32_t
	s.exec(func() {
		copts := C.CString(options)
		defer C.free(unsafe.Pointer(copts))
		hr = C.gokd_attach_kernel(s.handle, copts)
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

// ── Execution ─────────────────────────────────────────────────────────

// StopEvent mirrors gokd_stop_event_t.
type StopEvent struct {
	Reason              uint32
	Address             uint64
	ThreadSystemID      uint32
	ExceptionCode       uint32
	ExceptionFirstChance uint32
}

func stopEventFromC(cev C.gokd_stop_event_t) StopEvent {
	return StopEvent{
		Reason:              uint32(cev.reason),
		Address:             uint64(cev.address),
		ThreadSystemID:      uint32(cev.thread_sys_id),
		ExceptionCode:       uint32(cev.exception_code),
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

// Module mirrors gokd_module_t.
type Module struct {
	Name      string
	ImageName string
	Base      uint64
	Size      uint32
	Timestamp uint32
	Checksum  uint32
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
			Name:      C.GoString(&cmods[i].name[0]),
			ImageName: C.GoString(&cmods[i].image_name[0]),
			Base:      uint64(cmods[i].base),
			Size:      uint32(cmods[i].size),
			Timestamp: uint32(cmods[i].timestamp),
			Checksum:  uint32(cmods[i].checksum),
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
