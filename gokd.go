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

// ErrNotFound is returned when a lookup that the underlying DbgEng call
// could not satisfy — e.g. source-line info that is missing for an
// address, an unresolved (file, line) pair, or a memory search that
// found no match. Use errors.Is(err, ErrNotFound) to distinguish a
// benign "no result" from a hard failure.
var ErrNotFound = dbgcgo.ErrNotFound

// HRESULTError is the canonical error type returned by all DbgEng-backed
// operations. Use errors.As(err, &h) to inspect the raw HRESULT.
type HRESULTError = dbgcgo.HRESULTError

// Session is the top-level handle to a debug session.
// All methods are safe to call from any goroutine.
type Session interface {
	// Target attachment
	AttachProcess(pid uint32, opts AttachOptions) error
	CreateProcess(cmd string, opts CreateOptions) error
	AttachKernel(ctx context.Context, connectStr string, opts KernelOptions) error
	OpenDump(path string) error
	// WriteDump snapshots the current target to a .dmp file. path must be
	// an absolute path. WriteDumpFileWide is synchronous and uncancellable
	// mid-call — full kernel dumps can take minutes. ctx is honoured at
	// dispatch-thread granularity (the call will not return early once
	// DbgEng has begun writing).
	WriteDump(ctx context.Context, path string, opts WriteDumpOptions) error
	Detach() error

	// Remote debugging (connect to dbgsrv.exe process server)
	ConnectRemote(connection string) error // "tcp:server=host,port=5005"
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
	// ReloadSymbols forwards spec to IDebugSymbols3::ReloadWide. Pass an
	// empty string to reload anything currently stale, "/f" to force a
	// full reload, or "/f <module>" to force-reload a single module. The
	// call may download from the symbol server and block for a long time;
	// pass a cancellable context to bound the wait.
	ReloadSymbols(ctx context.Context, spec string) error
	// SymFix configures the symbol path to the standard public-symbol-server
	// cache layout (srv*<cache>*https://msdl.microsoft.com/download/symbols),
	// matching WinDbg's .symfix. If cache is empty, a per-user default is
	// used.
	SymFix(cache string) error

	// Types
	TypeSize(module, typeName string) (uint64, error)
	TypeFields(module, typeName string) ([]*Field, error)

	// Breakpoints
	AddBreakpoint(addr uint64) (*Breakpoint, error)
	AddBreakpointSym(symbol string) (*Breakpoint, error)
	RemoveBreakpoint(id uint32) error
	EnableBreakpoint(id uint32, enabled bool) error
	Breakpoints() ([]*Breakpoint, error)

	// Data + conditional breakpoints (t1-5). AddDataBreakpoint installs
	// a hardware ("break-on-access") breakpoint at addr covering size
	// bytes (size must be 1, 2, 4, or 8). access is any combination of
	// BreakpointAccessRead/Write/Execute/IO. Only four data breakpoints
	// may be enabled concurrently on x64 hardware; the fifth will fail
	// at the next Go().
	//
	// ConfigureBreakpoint applies non-positional fields (pass count,
	// thread filter, WinDbg command) to an existing breakpoint without
	// recreating it. See BreakpointOptions for sentinel semantics.
	//
	// BreakpointCommand returns the configured WinDbg command, or ""
	// if none has been set.
	AddDataBreakpoint(addr uint64, size uint32, access BreakpointAccess) (*Breakpoint, error)
	ConfigureBreakpoint(id uint32, opts BreakpointOptions) error
	BreakpointCommand(id uint32) (string, error)

	// Disassembly
	Disassemble(addr uint64) (*Instruction, error)
	DisassembleRange(addr uint64, count int) ([]*Instruction, error)

	// Evaluate parses an expression in the current expression syntax
	// (MASM by default) and returns the typed result. desired may be
	// ValueInvalid to ask for the engine's natural type. The remainder
	// is the number of wide characters left unconsumed at the end of
	// expr; 0 means the parser consumed the entire input. Symbol
	// resolution may stall on PDB downloads — use ctx to bound the wait.
	//
	// Expressions like MyClass::Method require ExpressionSyntaxCPP;
	// the default MASM parser will reject them with E_INVALIDARG.
	Evaluate(ctx context.Context, expr string, desired ValueKind) (Value, uint32, error)
	Radix() (uint32, error)
	SetRadix(r uint32) error
	ExpressionSyntax() (ExpressionSyntax, error)
	SetExpressionSyntax(syn ExpressionSyntax) error

	// Source lines (t1-3). AddrToLine maps an instruction address to a
	// (file, line, displacement) tuple; LineToAddr does the reverse.
	// AddBreakpointSourceLine resolves (file, line) to an address and
	// installs a code breakpoint there. All three return ErrNotFound
	// when DbgEng has no line info loaded for the location.
	AddrToLine(address uint64) (SourceLine, error)
	LineToAddr(file string, line uint32) (uint64, error)
	AddBreakpointSourceLine(file string, line uint32) (*Breakpoint, error)

	// Memory search / translate / query (t1-6). SearchMemory returns
	// (0, ErrNotFound) when the pattern is not present in the scanned
	// range. VirtualToPhysical only works on kernel-mode sessions —
	// user-mode targets fail with an HRESULT (commonly E_NOTIMPL).
	SearchMemory(start, length uint64, pattern []byte, granularity uint32) (uint64, error)
	VirtualToPhysical(va uint64) (uint64, error)
	QueryRegion(va uint64) (MemoryRegion, error)

	// Async streams
	Events() <-chan Event
	Output() <-chan string

	// Escape hatch
	Execute(cmd string) (string, error)

	// Last event / bugcheck (t1-8). LastException surfaces the most
	// recent DEBUG_EVENT_EXCEPTION record reported by DbgEng, with the
	// raw EXCEPTION_RECORD fields (code, address, parameters) and a
	// human-readable description. Returns (nil, ErrNotFound) when the
	// last event was not an exception (e.g. an attach breakpoint or
	// process-exit notification).
	//
	// BugCheck reads the kernel bug-check record via
	// IDebugControl4::ReadBugCheckData; user-mode sessions return
	// (nil, ErrNotFound). The returned struct carries the raw bugcheck
	// Code and Args plus a best-effort Name + Description from the
	// embedded common-codes table (empty strings for unknown codes).
	LastException() (*LastException, error)
	BugCheck() (*BugCheck, error)

	// Recursive type walker (t1-7). DumpType resolves typeName in
	// module's symbol namespace and reads addr as that type, recursing
	// into struct fields up to opts.MaxDepth levels deep. Set
	// FollowPtrs to dereference non-NULL pointer fields one extra
	// level (cycle detection guarantees termination). Special-case
	// decoders fill TypeValue.Decoded for _UNICODE_STRING, _LIST_ENTRY,
	// GUID, and _LARGE_INTEGER.
	DumpType(ctx context.Context, module, typeName string, addr uint64, opts DumpTypeOptions) (*TypeValue, error)

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
	Flags        uint32
	InitialBreak bool
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

// LastException is the rich exception record returned by Session.LastException.
// Distinct from ExceptionInfo (used by StopEvent) because it carries the
// full EXCEPTION_RECORD64 surface — parameters, nested-record pointer,
// process / thread ID, and the DbgEng-supplied textual description.
type LastException = dbgcgo.LastException

// ExceptionMaxParameters is the Windows EXCEPTION_MAXIMUM_PARAMETERS
// constant — re-exported so callers iterating Parameters don't need to
// import the internal dbgcgo package.
const ExceptionMaxParameters = dbgcgo.ExceptionMaxParameters

// BugCheck wraps the raw kernel bugcheck record with a best-effort
// human-readable name and description. Name and Description are filled
// in by Session.BugCheck via the embedded common-codes table; both are
// empty strings for codes not in the table.
type BugCheck struct {
	Code        uint32
	Args        [4]uint64
	Name        string
	Description string
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
	Name       string
	ImageName  string
	Base       uint64
	Size       uint32
	Timestamp  uint32
	Checksum   uint32
	SymbolType SymbolType
}

// SymbolType describes the kind of symbol information loaded for a module,
// mirroring DEBUG_SYMTYPE_* in dbgeng.h. It is surfaced through
// [Module.SymbolType] so callers can tell PDB-loaded modules apart from
// export-only or deferred ones (the common "no symbols loaded yet" state).
type SymbolType = dbgcgo.SymbolType

const (
	SymbolTypeNone     = dbgcgo.SymbolTypeNone
	SymbolTypeCOFF     = dbgcgo.SymbolTypeCOFF
	SymbolTypeCodeView = dbgcgo.SymbolTypeCodeView
	SymbolTypePDB      = dbgcgo.SymbolTypePDB
	SymbolTypeExport   = dbgcgo.SymbolTypeExport
	SymbolTypeDeferred = dbgcgo.SymbolTypeDeferred
	SymbolTypeSym      = dbgcgo.SymbolTypeSym
	SymbolTypeDIA      = dbgcgo.SymbolTypeDIA
)

// SymbolTypeString returns a stable lower-case name for a SymbolType.
// Returns "unknown(N)" for values outside the documented range.
func SymbolTypeString(t SymbolType) string {
	switch t {
	case SymbolTypeNone:
		return "none"
	case SymbolTypeCOFF:
		return "coff"
	case SymbolTypeCodeView:
		return "codeview"
	case SymbolTypePDB:
		return "pdb"
	case SymbolTypeExport:
		return "export"
	case SymbolTypeDeferred:
		return "deferred"
	case SymbolTypeSym:
		return "sym"
	case SymbolTypeDIA:
		return "dia"
	default:
		return fmt.Sprintf("unknown(%d)", uint32(t))
	}
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
	ID            uint32
	Address       uint64
	Expression    string
	Enabled       bool
	Type          BreakpointType
	Size          uint32
	Access        BreakpointAccess
	PassCount     uint32
	CurrentPass   uint32
	MatchThreadID uint32
	Command       string
}

// BreakpointType and BreakpointAccess re-export the underlying
// dbgcgo enums so callers can configure data breakpoints without
// importing the internal package.
type (
	BreakpointType   = dbgcgo.BreakpointType
	BreakpointAccess = dbgcgo.BreakpointAccess
)

const (
	BreakpointTypeCode      = dbgcgo.BreakpointTypeCode
	BreakpointTypeData      = dbgcgo.BreakpointTypeData
	BreakpointAccessRead    = dbgcgo.BreakpointAccessRead
	BreakpointAccessWrite   = dbgcgo.BreakpointAccessWrite
	BreakpointAccessExecute = dbgcgo.BreakpointAccessExecute
	BreakpointAccessIO      = dbgcgo.BreakpointAccessIO
)

// BreakpointMatchThreadAny is the wildcard thread filter — the value
// to pass to BreakpointOptions.MatchThreadID to leave the filter alone.
const BreakpointMatchThreadAny = dbgcgo.BreakpointMatchThreadAny

// BreakpointOptions describes non-positional breakpoint configuration.
// All fields are optional: zero / empty means "leave existing alone".
//
//   - PassCount     0  → leave existing pass count alone. Pass 1 to "fire
//     every hit" explicitly.
//   - MatchThreadID 0xFFFFFFFF (BreakpointMatchThreadAny) → leave existing
//     thread filter alone.
//   - Command       "" → leave existing command alone. Use ClearCommand
//     to clear it.
//   - ClearCommand  if true, the breakpoint command is unconditionally
//     cleared (overrides Command).
type BreakpointOptions struct {
	PassCount     uint32
	MatchThreadID uint32
	Command       string
	ClearCommand  bool
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

// ── Expression evaluation (t1-1) ──────────────────────────────────────

// ValueKind mirrors dbgcgo.ValueKind / DEBUG_VALUE_* in dbgeng.h.
type ValueKind = dbgcgo.ValueKind

const (
	ValueInvalid   = dbgcgo.ValueInvalid
	ValueInt8      = dbgcgo.ValueInt8
	ValueInt16     = dbgcgo.ValueInt16
	ValueInt32     = dbgcgo.ValueInt32
	ValueInt64     = dbgcgo.ValueInt64
	ValueFloat32   = dbgcgo.ValueFloat32
	ValueFloat64   = dbgcgo.ValueFloat64
	ValueFloat80   = dbgcgo.ValueFloat80
	ValueFloat82   = dbgcgo.ValueFloat82
	ValueFloat128  = dbgcgo.ValueFloat128
	ValueVector64  = dbgcgo.ValueVector64
	ValueVector128 = dbgcgo.ValueVector128
)

// ValueKindString returns a stable lower-case name for v.
func ValueKindString(v ValueKind) string {
	switch v {
	case ValueInvalid:
		return "invalid"
	case ValueInt8:
		return "int8"
	case ValueInt16:
		return "int16"
	case ValueInt32:
		return "int32"
	case ValueInt64:
		return "int64"
	case ValueFloat32:
		return "float32"
	case ValueFloat64:
		return "float64"
	case ValueFloat80:
		return "float80"
	case ValueFloat82:
		return "float82"
	case ValueFloat128:
		return "float128"
	case ValueVector64:
		return "vector64"
	case ValueVector128:
		return "vector128"
	default:
		return fmt.Sprintf("unknown(%d)", uint32(v))
	}
}

// ParseValueKind is the inverse of ValueKindString. Returns ValueInvalid
// and false for unknown names. Case-insensitive.
func ParseValueKind(name string) (ValueKind, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "invalid":
		return ValueInvalid, true
	case "int8":
		return ValueInt8, true
	case "int16":
		return ValueInt16, true
	case "int32":
		return ValueInt32, true
	case "int64":
		return ValueInt64, true
	case "float32":
		return ValueFloat32, true
	case "float64":
		return ValueFloat64, true
	case "float80":
		return ValueFloat80, true
	case "float82":
		return ValueFloat82, true
	case "float128":
		return ValueFloat128, true
	case "vector64":
		return ValueVector64, true
	case "vector128":
		return ValueVector128, true
	}
	return ValueInvalid, false
}

// Value mirrors dbgcgo.Value. See [Session.Evaluate].
type Value = dbgcgo.Value

// ExpressionSyntax identifies the DbgEng expression parser to use.
type ExpressionSyntax uint32

const (
	ExpressionSyntaxMASM ExpressionSyntax = 0
	ExpressionSyntaxCPP  ExpressionSyntax = 1
)

func (e ExpressionSyntax) String() string {
	switch e {
	case ExpressionSyntaxMASM:
		return "MASM"
	case ExpressionSyntaxCPP:
		return "C++"
	default:
		return fmt.Sprintf("ExpressionSyntax(%d)", uint32(e))
	}
}

// SourceLine is the (file, line, displacement) triple returned by
// AddrToLine.
type SourceLine = dbgcgo.SourceLine

// Memory-region types (t1-6) mirror the WinNT MEMORY_BASIC_INFORMATION64
// fields. The numeric constants below are the most common values; see
// winnt.h for the full list.
type (
	MemoryState   = dbgcgo.MemoryState
	MemoryProtect = dbgcgo.MemoryProtect
	MemoryType    = dbgcgo.MemoryType
	MemoryRegion  = dbgcgo.MemoryRegion
)

// Dump-format types (t1-2) re-exported from the internal layer.
type (
	DumpKind        = dbgcgo.DumpKind
	DumpFormatFlags = dbgcgo.DumpFormatFlags
)

const (
	DumpSmall   = dbgcgo.DumpSmall
	DumpDefault = dbgcgo.DumpDefault
	DumpFull    = dbgcgo.DumpFull

	DumpFmtDefault                     = dbgcgo.DumpFmtDefault
	DumpFmtUserSmallFullMemory         = dbgcgo.DumpFmtUserSmallFullMemory
	DumpFmtUserSmallHandleData         = dbgcgo.DumpFmtUserSmallHandleData
	DumpFmtUserSmallUnloadedModules    = dbgcgo.DumpFmtUserSmallUnloadedModules
	DumpFmtUserSmallIndirectMemory     = dbgcgo.DumpFmtUserSmallIndirectMemory
	DumpFmtUserSmallDataSegments       = dbgcgo.DumpFmtUserSmallDataSegments
	DumpFmtUserSmallFilterMemory       = dbgcgo.DumpFmtUserSmallFilterMemory
	DumpFmtUserSmallFilterPaths        = dbgcgo.DumpFmtUserSmallFilterPaths
	DumpFmtUserSmallProcessThreadData  = dbgcgo.DumpFmtUserSmallProcessThreadData
	DumpFmtUserSmallPrivateReadWrite   = dbgcgo.DumpFmtUserSmallPrivateReadWrite
	DumpFmtUserSmallNoOptionalData     = dbgcgo.DumpFmtUserSmallNoOptionalData
	DumpFmtUserSmallFullMemoryInfo     = dbgcgo.DumpFmtUserSmallFullMemoryInfo
	DumpFmtUserSmallThreadInfo         = dbgcgo.DumpFmtUserSmallThreadInfo
	DumpFmtUserSmallCodeSegments       = dbgcgo.DumpFmtUserSmallCodeSegments
	DumpFmtUserSmallNoAuxiliaryState   = dbgcgo.DumpFmtUserSmallNoAuxiliaryState
	DumpFmtUserSmallFullAuxiliaryState = dbgcgo.DumpFmtUserSmallFullAuxiliaryState
)

// WriteDumpOptions configures Session.WriteDump.
type WriteDumpOptions struct {
	// Kind selects the dump format. Zero means DumpDefault.
	Kind DumpKind
	// Flags is a bitmask of DEBUG_FORMAT_USER_SMALL_* values forwarded
	// verbatim to WriteDumpFileWide.
	Flags DumpFormatFlags
	// Comment is recorded inside the dump; may be empty.
	Comment string
}

const (
	MemCommit  MemoryState = 0x1000
	MemReserve MemoryState = 0x2000
	MemFree    MemoryState = 0x10000

	MemPrivate MemoryType = 0x20000
	MemMapped  MemoryType = 0x40000
	MemImage   MemoryType = 0x1000000

	PageNoAccess         MemoryProtect = 0x01
	PageReadOnly         MemoryProtect = 0x02
	PageReadWrite        MemoryProtect = 0x04
	PageWriteCopy        MemoryProtect = 0x08
	PageExecute          MemoryProtect = 0x10
	PageExecuteRead      MemoryProtect = 0x20
	PageExecuteReadWrite MemoryProtect = 0x40
	PageExecuteWriteCopy MemoryProtect = 0x80
	PageGuard            MemoryProtect = 0x100
	PageNoCache          MemoryProtect = 0x200
	PageWriteCombine     MemoryProtect = 0x400
)

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
type ThreadCreatedEvent struct{ Thread *Thread }
type ThreadExitedEvent struct {
	SystemID uint32
	ExitCode uint32
}
type ProcessCreatedEvent struct {
	ImageName  string
	BaseOffset uint64
	ModuleSize uint32
}
type ProcessExitedEvent struct{ ExitCode uint32 }
type ModuleLoadedEvent struct{ Module *Module }
type ModuleUnloadedEvent struct {
	ImageBaseName string
	BaseOffset    uint64
}

// SessionStatus describes a DbgEng session-lifecycle transition delivered
// via SessionStatusEvent. Values mirror dbgeng.h DEBUG_SESSION_*.
type SessionStatus uint32

const (
	SessionStatusActive             SessionStatus = 0
	SessionStatusEndActiveTerminate SessionStatus = 1
	SessionStatusEndActiveDetach    SessionStatus = 2
	SessionStatusEndPassive         SessionStatus = 3
	SessionStatusEnd                SessionStatus = 4
	SessionStatusReboot             SessionStatus = 5
	SessionStatusHibernate          SessionStatus = 6
	SessionStatusFailure            SessionStatus = 7
)

func (s SessionStatus) String() string {
	switch s {
	case SessionStatusActive:
		return "Active"
	case SessionStatusEndActiveTerminate:
		return "EndActiveTerminate"
	case SessionStatusEndActiveDetach:
		return "EndActiveDetach"
	case SessionStatusEndPassive:
		return "EndPassive"
	case SessionStatusEnd:
		return "End"
	case SessionStatusReboot:
		return "Reboot"
	case SessionStatusHibernate:
		return "Hibernate"
	case SessionStatusFailure:
		return "Failure"
	default:
		return fmt.Sprintf("SessionStatus(%d)", uint32(s))
	}
}

type SessionStatusEvent struct{ Status SessionStatus }

func (BreakpointEvent) isEvent()     {}
func (ExceptionEvent) isEvent()      {}
func (ThreadCreatedEvent) isEvent()  {}
func (ThreadExitedEvent) isEvent()   {}
func (ProcessCreatedEvent) isEvent() {}
func (ProcessExitedEvent) isEvent()  {}
func (ModuleLoadedEvent) isEvent()   {}
func (ModuleUnloadedEvent) isEvent() {}
func (SessionStatusEvent) isEvent()  {}

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
	return s.inner.CreateProcess(cmd, opts.Flags, opts.InitialBreak)
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

func (s *session) WriteDump(ctx context.Context, path string, opts WriteDumpOptions) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("gokd: path is required")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("gokd: WriteDump requires an absolute path (DbgEng resolves relative paths against its own CWD): %q", path)
	}
	kind := opts.Kind
	if kind == 0 {
		kind = DumpDefault
	}
	return s.runWithCancel(ctx, func() error {
		return s.inner.WriteDump(path, kind, opts.Flags, opts.Comment)
	})
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

// runWithCancel is the generic counterpart to execWithCancel for operations
// that do not produce a StopEvent. It wires ctx cancellation to
// CancelWait so long-blocking shim calls (symbol downloads, dump writes,
// recursive memory reads) can be interrupted from the caller's side.
// If ctx is nil, fn runs without cancellation support.
func (s *session) runWithCancel(ctx context.Context, fn func() error) error {
	if ctx == nil {
		return fn()
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			s.inner.CancelWait()
		case <-done:
		}
	}()
	err := fn()
	close(done)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
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
			Name:       m.Name,
			ImageName:  m.ImageName,
			Base:       m.Base,
			Size:       m.Size,
			Timestamp:  m.Timestamp,
			Checksum:   m.Checksum,
			SymbolType: m.SymbolType,
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

// ReloadSymbols forwards spec to IDebugSymbols3::ReloadWide. See the
// [Session] interface documentation for accepted spec values. Cancellation
// via ctx interrupts long-running symbol downloads between dispatch turns.
func (s *session) ReloadSymbols(ctx context.Context, spec string) error {
	return s.runWithCancel(ctx, func() error { return s.inner.ReloadSymbols(spec) })
}

// SymFix mirrors WinDbg's .symfix and installs the standard
// public-symbol-server cache path. When cache is empty, a per-user default
// at "<UserCacheDir>/gokd/symbols" is used (falling back to "<TempDir>/gokd/symbols").
func (s *session) SymFix(cache string) error {
	if cache == "" {
		cacheDir, err := os.UserCacheDir()
		if err != nil || cacheDir == "" {
			cacheDir = os.TempDir()
		}
		cache = filepath.Join(cacheDir, "gokd", "symbols")
	}
	return s.SetSymbolPath(fmt.Sprintf("srv*%s*https://msdl.microsoft.com/download/symbols", cache))
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
	return &Breakpoint{ID: id, Address: addr, Enabled: true, Type: BreakpointTypeCode}, nil
}

func (s *session) AddBreakpointSym(symbol string) (*Breakpoint, error) {
	id, err := s.inner.AddBreakpointSym(symbol)
	if err != nil {
		return nil, err
	}
	return &Breakpoint{ID: id, Expression: symbol, Enabled: true, Type: BreakpointTypeCode}, nil
}

// AddDataBreakpoint installs a hardware "break-on-access" breakpoint at
// addr. size must be one of {1, 2, 4, 8} and access must contain at
// least one of BreakpointAccessRead/Write/Execute/IO; both are validated
// at the Go layer to keep DbgEng's errors readable.
func (s *session) AddDataBreakpoint(addr uint64, size uint32, access BreakpointAccess) (*Breakpoint, error) {
	switch size {
	case 1, 2, 4, 8:
	default:
		return nil, dbgcgo.HRESULTError(int32(0x80070057 - 0x100000000)) // E_INVALIDARG
	}
	if access == 0 {
		return nil, dbgcgo.HRESULTError(int32(0x80070057 - 0x100000000))
	}
	id, err := s.inner.AddDataBreakpoint(addr, size, access)
	if err != nil {
		return nil, err
	}
	return &Breakpoint{
		ID:            id,
		Address:       addr,
		Enabled:       true,
		Type:          BreakpointTypeData,
		Size:          size,
		Access:        access,
		MatchThreadID: BreakpointMatchThreadAny,
	}, nil
}

// ConfigureBreakpoint applies non-positional fields (pass count, thread
// filter, command) to an existing breakpoint. See BreakpointOptions for
// per-field "leave alone" sentinel semantics.
func (s *session) ConfigureBreakpoint(id uint32, opts BreakpointOptions) error {
	matchTID := opts.MatchThreadID
	if matchTID == 0 {
		matchTID = BreakpointMatchThreadAny
	}
	var cmd *string
	switch {
	case opts.ClearCommand:
		empty := ""
		cmd = &empty
	case opts.Command != "":
		c := opts.Command
		cmd = &c
	}
	return s.inner.ConfigureBreakpoint(id, opts.PassCount, matchTID, cmd)
}

func (s *session) BreakpointCommand(id uint32) (string, error) {
	return s.inner.BreakpointCommand(id)
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
		cmd, _ := s.inner.BreakpointCommand(b.ID)
		out[i] = &Breakpoint{
			ID:            b.ID,
			Address:       b.Offset,
			Expression:    b.Expression,
			Enabled:       b.Enabled,
			Type:          b.Type,
			Size:          b.Size,
			Access:        b.Access,
			PassCount:     b.PassCount,
			CurrentPass:   b.CurrentPass,
			MatchThreadID: b.MatchThreadID,
			Command:       cmd,
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

// Evaluate implements [Session.Evaluate].
func (s *session) Evaluate(ctx context.Context, expr string, desired ValueKind) (Value, uint32, error) {
	var v Value
	var rem uint32
	err := s.runWithCancel(ctx, func() error {
		var e error
		v, rem, e = s.inner.Evaluate(expr, desired)
		return e
	})
	return v, rem, err
}

func (s *session) Radix() (uint32, error) {
	return s.inner.Radix()
}

func (s *session) SetRadix(r uint32) error {
	return s.inner.SetRadix(r)
}

func (s *session) ExpressionSyntax() (ExpressionSyntax, error) {
	idx, err := s.inner.ExpressionSyntax()
	if err != nil {
		return 0, err
	}
	return ExpressionSyntax(idx), nil
}

func (s *session) SetExpressionSyntax(syn ExpressionSyntax) error {
	var name string
	switch syn {
	case ExpressionSyntaxMASM:
		name = "MASM"
	case ExpressionSyntaxCPP:
		name = "C++"
	default:
		return fmt.Errorf("gokd: unknown ExpressionSyntax(%d)", uint32(syn))
	}
	return s.inner.SetExpressionSyntax(name)
}

// --- Source lines (t1-3) ---

func (s *session) AddrToLine(address uint64) (SourceLine, error) {
	return s.inner.AddrToLine(address)
}

func (s *session) LineToAddr(file string, line uint32) (uint64, error) {
	if file == "" {
		return 0, fmt.Errorf("gokd: file is required")
	}
	return s.inner.LineToAddr(file, line)
}

func (s *session) AddBreakpointSourceLine(file string, line uint32) (*Breakpoint, error) {
	addr, err := s.LineToAddr(file, line)
	if err != nil {
		return nil, err
	}
	return s.AddBreakpoint(addr)
}

// --- Memory search / translate / query (t1-6) ---

func (s *session) SearchMemory(start, length uint64, pattern []byte, granularity uint32) (uint64, error) {
	return s.inner.SearchMemory(start, length, pattern, granularity)
}

func (s *session) VirtualToPhysical(va uint64) (uint64, error) {
	return s.inner.VirtualToPhysical(va)
}

func (s *session) QueryRegion(va uint64) (MemoryRegion, error) {
	return s.inner.QueryRegion(va)
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
			case dbgcgo.EvSessionStatus:
				ev = SessionStatusEvent{Status: SessionStatus(d.Status)}
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

// LastException returns the most recent DEBUG_EVENT_EXCEPTION record
// from DbgEng. Returns (nil, ErrNotFound) when the last event was not
// an exception (e.g. an attach breakpoint or a process-exit event).
func (s *session) LastException() (*LastException, error) {
	return s.inner.GetLastException()
}

// BugCheck reads the kernel bugcheck record and decorates it with a
// best-effort name + description from the embedded common-codes table.
// User-mode sessions and kernel sessions with no recorded bugcheck
// return (nil, ErrNotFound).
func (s *session) BugCheck() (*BugCheck, error) {
	bc, err := s.inner.GetBugCheck()
	if err != nil {
		return nil, err
	}
	name, desc := LookupBugCheckName(bc.Code)
	return &BugCheck{
		Code:        bc.Code,
		Args:        bc.Args,
		Name:        name,
		Description: desc,
	}, nil
}

func (s *session) Close() error {
	s.closeOnce.Do(func() {
		s.inner.UnregisterCallbacks()
		s.inner.Close()
	})
	return nil
}
