// Package gokd provides a Go library for Windows debugging via the
// DbgEng engine (dbgeng.dll) — the same engine that powers kd.exe,
// cdb.exe, and windbg.exe.
//
// All operations return structured Go types. No text parsing is involved.
//
// Usage:
//
//	sess, err := gokd.New(gokd.WithDefaultSymbols())
//	if err != nil { log.Fatal(err) }
//	defer sess.Close()
//
//	err = sess.AttachProcess(1234, gokd.AttachDefault)
//	stack, _ := sess.Stack()
//	for _, f := range stack {
//	    fmt.Printf("%s!%s+0x%x\n", f.Module, f.Function, f.Displacement)
//	}
package gokd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nijosmsft/gokd/internal/dbgcgo"
)

// ErrSessionClosed is returned by methods invoked on a Session after
// Close has been called. Re-exported from the internal dbgcgo package.
var ErrSessionClosed = dbgcgo.ErrSessionClosed

// ErrTimeout is returned when an internal WaitForEvent call hits its
// overall timeout (DbgEng's S_FALSE). Use errors.Is(err, ErrTimeout)
// to distinguish a benign wait timeout from a hard failure.
var ErrTimeout = dbgcgo.ErrTimeout

// Session is the top-level handle to a debug session.
// All methods are safe to call from any goroutine.
type Session interface {
	// Target attachment
	AttachProcess(pid uint32, opts AttachOptions) error
	CreateProcess(cmd string, opts CreateOptions) error
	AttachKernel(ctx context.Context, connectStr string, opts KernelOptions) error
	OpenDump(path string) error
	Detach() error

	// Remote debugging (connect to dbgsrv.exe process server)
	ConnectRemote(connection string) error   // "tcp:server=host,port=5005"
	DisconnectRemote() error

	// Execution
	Go(ctx context.Context) (*StopEvent, error)
	StepIn(ctx context.Context) (*StopEvent, error)
	StepOver(ctx context.Context) (*StopEvent, error)
	StepOut(ctx context.Context) (*StopEvent, error)
	BreakIn() error

	// Memory
	ReadMemory(addr uint64, n uint64) ([]byte, error)
	WriteMemory(addr uint64, data []byte) error
	ReadPhysical(addr uint64, n uint64) ([]byte, error)

	// Registers
	Registers() (*RegisterSet, error)
	SetRegister(name string, value uint64) error

	// Stack
	Stack() ([]*Frame, error)

	// Threads
	Threads() ([]*Thread, error)
	SetThread(sysTID uint32) error

	// Modules
	Modules() ([]*Module, error)

	// Symbols
	NameToAddr(name string) (uint64, error)
	AddrToName(addr uint64) (string, uint64, error)

	// Symbol path
	SetSymbolPath(path string) error
	SymbolPath() (string, error)

	// Types
	TypeSize(module, typeName string) (uint64, error)
	TypeFields(module, typeName string) ([]*Field, error)

	// Breakpoints
	AddBreakpoint(addr uint64) (*Breakpoint, error)
	AddBreakpointSym(symbol string) (*Breakpoint, error)
	RemoveBreakpoint(id uint32) error
	EnableBreakpoint(id uint32, enabled bool) error
	Breakpoints() ([]*Breakpoint, error)

	// Disassembly
	Disassemble(addr uint64) (*Instruction, error)
	DisassembleRange(addr uint64, count int) ([]*Instruction, error)

	// Async streams
	Events() <-chan Event
	Output() <-chan string

	// Escape hatch
	Execute(cmd string) (string, error)

	Close() error
}

// New creates a new debug session.
//
// Options can be supplied to configure session-wide settings such as the
// symbol path; see [WithSymbolPath] and [WithDefaultSymbols].
func New(opts ...Option) (Session, error) {
	var o sessionOptions
	for _, opt := range opts {
		opt(&o)
	}
	inner, err := dbgcgo.NewSession()
	if err != nil {
		return nil, err
	}
	s := &session{inner: inner}
	s.eventCh, s.outputCh = inner.RegisterCallbacks(256, 256)

	if o.symbolPathSet {
		if err := s.SetSymbolPath(o.symbolPath); err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("set symbol path: %w", err)
		}
	} else if o.useDefaultSymbols {
		current, err := s.SymbolPath()
		if err == nil && strings.TrimSpace(current) == "" {
			if err := s.SetSymbolPath(DefaultSymbolPath()); err != nil {
				_ = s.Close()
				return nil, fmt.Errorf("set default symbol path: %w", err)
			}
		}
	}
	return s, nil
}

// Option configures a new debug session created by [New].
type Option func(*sessionOptions)

type sessionOptions struct {
	symbolPath        string
	symbolPathSet     bool
	useDefaultSymbols bool
}

// WithSymbolPath sets the DbgEng symbol path immediately after the session
// is created. Use this when you have an explicit symbol path that should
// always win, regardless of the environment.
func WithSymbolPath(path string) Option {
	return func(o *sessionOptions) {
		o.symbolPath = path
		o.symbolPathSet = true
	}
}

// WithDefaultSymbols installs a sensible symbol path if and only if no path
// is already configured by DbgEng (e.g. via the _NT_SYMBOL_PATH environment
// variable). The default is the Microsoft public symbol server combined with
// a per-user local cache — see [DefaultSymbolPath].
//
// Has no effect if [WithSymbolPath] is also supplied: the explicit path wins.
func WithDefaultSymbols() Option {
	return func(o *sessionOptions) {
		o.useDefaultSymbols = true
	}
}

// DefaultSymbolPath returns the symbol path that [WithDefaultSymbols] would
// install when the engine has no path configured: the Microsoft public symbol
// server plus a per-user local cache at
// "<UserCacheDir>\gokd\symbols" (or "<TempDir>\gokd\symbols" as a fallback).
func DefaultSymbolPath() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil || cacheDir == "" {
		cacheDir = os.TempDir()
	}
	symCache := filepath.Join(cacheDir, "gokd", "symbols")
	return fmt.Sprintf("srv*%s*https://msdl.microsoft.com/download/symbols", symCache)
}

// ── Options ───────────────────────────────────────────────────────────

type AttachOptions struct {
	Flags uint32
}

var (
	AttachDefault     = AttachOptions{Flags: 0}
	AttachNonInvasive = AttachOptions{Flags: 0x00000001}
	AttachExisting    = AttachOptions{Flags: 0x00000002}
)

type CreateOptions struct {
	Flags         uint32
	InitialBreak  bool
}

// KernelOptions controls kernel-target attach behaviour.
//
// InitialBreakIn requests an active break-in immediately after the transport
// opens, so the engine pushes a break packet to the target as soon as the
// connection handshakes. This makes the first break deterministic — without
// it, kernel attaches can sit in WaitForEvent forever on an idle target
// (the same way `kd.exe` waits passively until you press Ctrl+Break).
//
// Use KernelDefault for the recommended programmatic behaviour, or
// KernelPassive for kd.exe-style "wait for the target to talk first".
type KernelOptions struct {
	InitialBreakIn bool
}

var (
	KernelDefault = KernelOptions{InitialBreakIn: true}
	KernelPassive = KernelOptions{InitialBreakIn: false}
)

// Kernel-attach flag bits forwarded to the C shim. Keep in sync with
// GOKD_KERNEL_* in cshim/gokd_shim.h.
const (
	kernelFlagInitialBreakIn uint32 = 0x00000001
)

func (o KernelOptions) flags() uint32 {
	var f uint32
	if o.InitialBreakIn {
		f |= kernelFlagInitialBreakIn
	}
	return f
}

// ── Types ─────────────────────────────────────────────────────────────

type StopReason int

const (
	StopBreakpoint  StopReason = 1
	StopStep        StopReason = 2
	StopException   StopReason = 3
	StopProcessExit StopReason = 4
)

func (r StopReason) String() string {
	switch r {
	case StopBreakpoint:
		return "Breakpoint"
	case StopStep:
		return "Step"
	case StopException:
		return "Exception"
	case StopProcessExit:
		return "ProcessExit"
	default:
		return fmt.Sprintf("Unknown(%d)", int(r))
	}
}

type StopEvent struct {
	Reason    StopReason
	Address   uint64
	Thread    *Thread
	Exception *ExceptionInfo
}

type ExceptionInfo struct {
	Code        uint32
	Address     uint64
	FirstChance bool
}

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

type Module struct {
	Name      string
	ImageName string
	Base      uint64
	Size      uint32
	Timestamp uint32
	Checksum  uint32
}

type Thread struct {
	SystemID    uint32
	Handle      uint64
	DataOffset  uint64
	StartOffset uint64
}

type RegisterType int

const (
	RegisterInt8      RegisterType = 0
	RegisterInt16     RegisterType = 1
	RegisterInt32     RegisterType = 2
	RegisterInt64     RegisterType = 3
	RegisterFloat32   RegisterType = 4
	RegisterFloat64   RegisterType = 5
	RegisterFloat80   RegisterType = 6
	RegisterVector128 RegisterType = 7
)

type Register struct {
	Name  string
	Value uint64
	Type  RegisterType
	Valid bool
}

type RegisterSet struct {
	Registers []Register
	ByName    map[string]*Register
}

type Breakpoint struct {
	ID         uint32
	Address    uint64
	Expression string
	Enabled    bool
}

type Field struct {
	Name     string
	Offset   uint32
	Size     uint64
	TypeName string
}

type Instruction struct {
	Address uint64
	Text    string
	Size    uint32
	Bytes   []byte
}

// Event types delivered on Session.Events().
type Event interface{ isEvent() }

type BreakpointEvent struct {
	ID      uint32
	Address uint64
	Thread  *Thread
}
type ExceptionEvent struct {
	Code        uint32
	Address     uint64
	FirstChance bool
	Thread      *Thread
}
type ThreadCreatedEvent  struct{ Thread *Thread }
type ThreadExitedEvent   struct{ SystemID uint32; ExitCode uint32 }
type ProcessCreatedEvent struct{ ImageName string; BaseOffset uint64; ModuleSize uint32 }
type ProcessExitedEvent  struct{ ExitCode uint32 }
type ModuleLoadedEvent   struct{ Module *Module }
type ModuleUnloadedEvent struct{ ImageBaseName string; BaseOffset uint64 }

func (BreakpointEvent)    isEvent() {}
func (ExceptionEvent)     isEvent() {}
func (ThreadCreatedEvent) isEvent() {}
func (ThreadExitedEvent)  isEvent() {}
func (ProcessCreatedEvent) isEvent() {}
func (ProcessExitedEvent) isEvent() {}
func (ModuleLoadedEvent)  isEvent() {}
func (ModuleUnloadedEvent) isEvent() {}

// ── session implementation ────────────────────────────────────────────

type session struct {
	inner     *dbgcgo.Session
	eventCh   <-chan dbgcgo.Event
	outputCh  <-chan string
	closeOnce sync.Once
}

func (s *session) AttachProcess(pid uint32, opts AttachOptions) error {
	return s.inner.AttachProcess(pid, opts.Flags)
}

func (s *session) CreateProcess(cmd string, opts CreateOptions) error {
	return s.inner.CreateProcess(cmd, opts.Flags)
}

func (s *session) AttachKernel(ctx context.Context, connectStr string, opts KernelOptions) error {
	// Monitor context cancellation in a separate goroutine.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			s.inner.CancelWait()
		case <-done:
		}
	}()
	err := s.inner.AttachKernel(connectStr, opts.flags())
	close(done)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (s *session) OpenDump(path string) error {
	return s.inner.OpenDump(path)
}

func (s *session) ConnectRemote(connection string) error {
	return s.inner.ConnectRemote(connection)
}

func (s *session) DisconnectRemote() error {
	return s.inner.DisconnectRemote()
}

func (s *session) Detach() error {
	return s.inner.Detach()
}

func (s *session) makeStopEvent(cev dbgcgo.StopEvent) *StopEvent {
	ev := &StopEvent{
		Reason:  StopReason(cev.Reason),
		Address: cev.Address,
		Thread:  &Thread{SystemID: cev.ThreadSystemID},
	}
	if ev.Reason == StopException {
		ev.Exception = &ExceptionInfo{
			Code:        cev.ExceptionCode,
			Address:     cev.Address,
			FirstChance: cev.ExceptionFirstChance != 0,
		}
	}
	return ev
}

// execWithCancel runs an execution command with context cancellation support.
func (s *session) execWithCancel(ctx context.Context, fn func() (dbgcgo.StopEvent, error)) (*StopEvent, error) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			s.inner.CancelWait()
		case <-done:
		}
	}()
	cev, err := fn()
	close(done)
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return s.makeStopEvent(cev), nil
}

func (s *session) Go(ctx context.Context) (*StopEvent, error) {
	return s.execWithCancel(ctx, s.inner.Go)
}

func (s *session) StepIn(ctx context.Context) (*StopEvent, error) {
	return s.execWithCancel(ctx, s.inner.StepIn)
}

func (s *session) StepOver(ctx context.Context) (*StopEvent, error) {
	return s.execWithCancel(ctx, s.inner.StepOver)
}

func (s *session) StepOut(ctx context.Context) (*StopEvent, error) {
	return s.execWithCancel(ctx, s.inner.StepOut)
}

func (s *session) BreakIn() error {
	return s.inner.BreakIn()
}

func (s *session) ReadMemory(addr uint64, n uint64) ([]byte, error) {
	return s.inner.ReadVirtual(addr, n)
}

func (s *session) WriteMemory(addr uint64, data []byte) error {
	return s.inner.WriteVirtual(addr, data)
}

func (s *session) ReadPhysical(addr uint64, n uint64) ([]byte, error) {
	return s.inner.ReadPhysical(addr, n)
}

func (s *session) Registers() (*RegisterSet, error) {
	regs, err := s.inner.GetRegisters()
	if err != nil {
		return nil, err
	}
	rs := &RegisterSet{
		Registers: make([]Register, len(regs)),
		ByName:    make(map[string]*Register, len(regs)),
	}
	for i, r := range regs {
		rs.Registers[i] = Register{
			Name:  r.Name,
			Value: r.Value,
			Type:  RegisterType(r.Type),
			Valid: r.Valid,
		}
		rs.ByName[r.Name] = &rs.Registers[i]
	}
	return rs, nil
}

func (s *session) SetRegister(name string, value uint64) error {
	return s.inner.SetRegister(name, value)
}

func (s *session) Stack() ([]*Frame, error) {
	frames, err := s.inner.GetStack(256)
	if err != nil {
		return nil, err
	}
	out := make([]*Frame, len(frames))
	for i, f := range frames {
		out[i] = &Frame{
			InstructionOffset: f.InstructionOffset,
			ReturnOffset:      f.ReturnOffset,
			FrameOffset:       f.FrameOffset,
			StackOffset:       f.StackOffset,
			Module:            f.Module,
			Function:          f.Function,
			Displacement:      f.Displacement,
			SourceFile:        f.SourceFile,
			SourceLine:        f.SourceLine,
		}
	}
	return out, nil
}

func (s *session) Threads() ([]*Thread, error) {
	threads, err := s.inner.GetThreads()
	if err != nil {
		return nil, err
	}
	out := make([]*Thread, len(threads))
	for i, t := range threads {
		out[i] = &Thread{
			SystemID:    t.SystemID,
			Handle:      t.Handle,
			DataOffset:  t.DataOffset,
			StartOffset: t.StartOffset,
		}
	}
	return out, nil
}

func (s *session) SetThread(sysTID uint32) error {
	return s.inner.SetCurrentThread(sysTID)
}

func (s *session) Modules() ([]*Module, error) {
	if s.inner.IsClosed() {
		return nil, ErrSessionClosed
	}
	mods, err := s.inner.GetModules()
	if err != nil {
		return nil, err
	}
	out := make([]*Module, len(mods))
	for i, m := range mods {
		out[i] = &Module{
			Name:      m.Name,
			ImageName: m.ImageName,
			Base:      m.Base,
			Size:      m.Size,
			Timestamp: m.Timestamp,
			Checksum:  m.Checksum,
		}
	}
	return out, nil
}

func (s *session) NameToAddr(name string) (uint64, error) {
	return s.inner.NameToAddr(name)
}

func (s *session) AddrToName(addr uint64) (string, uint64, error) {
	return s.inner.AddrToName(addr)
}

func (s *session) SetSymbolPath(path string) error {
	return s.inner.SetSymbolPath(path)
}

func (s *session) SymbolPath() (string, error) {
	return s.inner.GetSymbolPath()
}

func (s *session) TypeSize(module, typeName string) (uint64, error) {
	return s.inner.GetTypeSize(module, typeName)
}

func (s *session) TypeFields(module, typeName string) ([]*Field, error) {
	fields, err := s.inner.GetTypeFields(module, typeName)
	if err != nil {
		return nil, err
	}
	out := make([]*Field, len(fields))
	for i, f := range fields {
		out[i] = &Field{
			Name:     f.Name,
			Offset:   f.Offset,
			Size:     f.Size,
			TypeName: f.TypeName,
		}
	}
	return out, nil
}

func (s *session) AddBreakpoint(addr uint64) (*Breakpoint, error) {
	id, err := s.inner.AddBreakpoint(addr)
	if err != nil {
		return nil, err
	}
	return &Breakpoint{ID: id, Address: addr, Enabled: true}, nil
}

func (s *session) AddBreakpointSym(symbol string) (*Breakpoint, error) {
	id, err := s.inner.AddBreakpointSym(symbol)
	if err != nil {
		return nil, err
	}
	return &Breakpoint{ID: id, Expression: symbol, Enabled: true}, nil
}

func (s *session) RemoveBreakpoint(id uint32) error {
	return s.inner.RemoveBreakpoint(id)
}

func (s *session) EnableBreakpoint(id uint32, enabled bool) error {
	return s.inner.EnableBreakpoint(id, enabled)
}

func (s *session) Breakpoints() ([]*Breakpoint, error) {
	bps, err := s.inner.ListBreakpoints()
	if err != nil {
		return nil, err
	}
	out := make([]*Breakpoint, len(bps))
	for i, b := range bps {
		out[i] = &Breakpoint{
			ID:         b.ID,
			Address:    b.Offset,
			Expression: b.Expression,
			Enabled:    b.Enabled,
		}
	}
	return out, nil
}

func (s *session) Disassemble(addr uint64) (*Instruction, error) {
	text, nextAddr, err := s.inner.Disassemble(addr)
	if err != nil {
		return nil, err
	}
	return &Instruction{
		Address: addr,
		Text:    text,
		Size:    uint32(nextAddr - addr),
	}, nil
}

func (s *session) DisassembleRange(addr uint64, count int) ([]*Instruction, error) {
	out := make([]*Instruction, 0, count)
	cur := addr
	for i := 0; i < count; i++ {
		text, next, err := s.inner.Disassemble(cur)
		if err != nil {
			break
		}
		out = append(out, &Instruction{
			Address: cur,
			Text:    text,
			Size:    uint32(next - cur),
		})
		cur = next
	}
	return out, nil
}

func (s *session) Events() <-chan Event {
	// Translate internal events to public types on a bridge goroutine.
	pubCh := make(chan Event, 256)
	go func() {
		defer close(pubCh)
		for iev := range s.eventCh {
			var ev Event
			switch d := iev.Data.(type) {
			case dbgcgo.EvBreakpoint:
				ev = BreakpointEvent{
					ID:      d.BPID,
					Address: d.Address,
					Thread:  &Thread{SystemID: d.ThreadSysID},
				}
			case dbgcgo.EvException:
				ev = ExceptionEvent{
					Code:        d.Code,
					Address:     d.Address,
					FirstChance: d.FirstChance != 0,
					Thread:      &Thread{SystemID: d.ThreadSysID},
				}
			case dbgcgo.EvThreadCreated:
				ev = ThreadCreatedEvent{Thread: &Thread{
					SystemID:    d.SysID,
					Handle:      d.Handle,
					DataOffset:  d.DataOffset,
					StartOffset: d.StartOffset,
				}}
			case dbgcgo.EvThreadExited:
				ev = ThreadExitedEvent{SystemID: d.SysID, ExitCode: d.ExitCode}
			case dbgcgo.EvProcCreated:
				ev = ProcessCreatedEvent{
					ImageName:  d.ImageName,
					BaseOffset: d.BaseOffset,
					ModuleSize: d.ModuleSize,
				}
			case dbgcgo.EvProcExited:
				ev = ProcessExitedEvent{ExitCode: d.ExitCode}
			case dbgcgo.EvModLoaded:
				ev = ModuleLoadedEvent{Module: &Module{
					Name:      d.ModuleName,
					ImageName: d.ImageName,
					Base:      d.BaseOffset,
					Size:      d.ModuleSize,
				}}
			case dbgcgo.EvModUnloaded:
				ev = ModuleUnloadedEvent{
					ImageBaseName: d.ImageBaseName,
					BaseOffset:    d.BaseOffset,
				}
			default:
				continue
			}
			pubCh <- ev
		}
	}()
	return pubCh
}

func (s *session) Output() <-chan string {
	return s.outputCh
}

func (s *session) Execute(cmd string) (string, error) {
	return s.inner.Execute(cmd)
}

func (s *session) Close() error {
	s.closeOnce.Do(func() {
		s.inner.UnregisterCallbacks()
		s.inner.Close()
	})
	return nil
}
