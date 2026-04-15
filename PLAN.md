# GoKD — Project Plan

**GoKD** is a Go library for Windows debugging, providing a clean, idiomatic Go API over the
Windows DbgEng engine (`dbgeng.dll`) — the same engine that powers `kd.exe`, `cdb.exe`, and
`windbg.exe`. It targets user-mode processes, kernel-mode targets (via KDNET, serial, USB), and
offline crash dump analysis.

GoKD is a library only. A separate MCP server package will be built on top of it.

---

## Design Goals

- **No text parsing.** All operations use DbgEng's typed COM interfaces and DbgHelp's symbol APIs.
  Structured Go types are returned everywhere.
- **Stable under adversarial data.** Crash dumps and live targets may contain corrupt or malformed
  memory. The C++ shim isolates SEH exceptions; the Go layer never sees raw COM errors.
- **Single-process, no IPC.** The C++ shim compiles directly into the Go binary via CGo. No
  separate agent process, no gRPC, no serialization overhead.
- **DbgEng thread affinity respected.** A dedicated OS-locked goroutine owns all DbgEng calls.
  Commands from other goroutines are queued and executed on that thread.
- **Go-idiomatic API.** Sessions, targets, events, breakpoints — all expressed as Go interfaces
  and structs with channels for async event delivery.

---

## Architecture

```
┌─────────────────────────────────────────────────┐
│              Consumer (your app / MCP)          │
│         import "github.com/nijosmsft/gokd"      │
└────────────────────┬────────────────────────────┘
                     │
┌────────────────────▼────────────────────────────┐
│           pkg/  — Go public API                 │
│  Session · Target · Memory · Registers · Stack  │
│  Symbols · Types · Modules · Threads · Events   │
│  Breakpoints · Disassembly                      │
└────────────────────┬────────────────────────────┘
                     │  CGo (internal only)
┌────────────────────▼────────────────────────────┐
│       internal/dbgcgo/ — CGo bindings           │
│  Thin unsafe wrappers · OS-locked goroutine     │
│  CGo export callbacks for events/output         │
└────────────────────┬────────────────────────────┘
                     │  compiled in via CGo
┌────────────────────▼────────────────────────────┐
│         cshim/ — C++ DbgEng shim                │
│  Flat C API (no COM crosses the CGo boundary)   │
│  Dispatch thread + command queue                │
│  IDebugEventCallbacks implementation            │
│  DbgHelp for full type/symbol information       │
└────────────────────┬────────────────────────────┘
                     │
              dbgeng.dll · dbghelp.dll
```

### Why a C++ shim?

CGo cannot call C++ COM vtable methods directly. The shim translates every DbgEng COM interface
(`IDebugClient6`, `IDebugControl6`, `IDebugSymbols5`, `IDebugDataSpaces4`, `IDebugRegisters2`,
`IDebugSystemObjects4`, `IDebugAdvanced3`) into a flat C API. Only plain C types and opaque
`uint64_t` session handles cross the CGo boundary.

### DbgEng thread affinity

DbgEng requires all calls to originate from a single OS thread (the thread that calls
`WaitForEvent`). The shim manages a dedicated dispatch thread with a mutex-protected command
queue. The Go side has a matching OS-locked goroutine (`runtime.LockOSThread`) that posts
commands and waits for results. Events fire CGo callbacks back into Go channels.

---

## Repository Structure

```
GoKD/
├── cshim/
│   ├── gokd_shim.h           # Public C API — the only header CGo includes
│   ├── gokd_shim.cpp         # COM → flat C implementation
│   ├── dispatch_thread.cpp   # Dedicated DbgEng dispatch thread + command queue
│   └── callbacks.cpp         # IDebugEventCallbacks + IDebugOutputCallbacks impl
│
├── internal/
│   └── dbgcgo/
│       ├── dbgeng.go         # CGo bindings (unsafe, not public)
│       └── callbacks.go      # //export functions called back from C
│
├── pkg/
│   ├── session/              # Session interface + engine implementation
│   ├── target/
│   │   ├── umode.go          # User-mode process attach/detach/create
│   │   ├── kmode.go          # Kernel-mode (KDNET, serial, USB, EXDI)
│   │   └── dump.go           # Crash dump / minidump
│   ├── memory/               # Virtual + physical memory read/write
│   ├── registers/            # Register access; arch-specific sets (x64, ARM64)
│   ├── symbols/              # Symbol resolution (name↔address, symbol path)
│   ├── types/                # PDB type system (fields, offsets, inheritance)
│   ├── stack/                # Stack walk, frame info, source line mapping
│   ├── modules/              # Module enumeration, load/unload events
│   ├── threads/              # Thread enumeration, context switching
│   ├── breakpoints/          # SW breakpoints, HW breakpoints, data breakpoints
│   ├── events/               # Event types + channel-based async dispatch
│   └── disasm/               # Disassembly via DbgEng
│
├── cmd/
│   └── gokd/                 # Interactive REPL / CLI (development + demo tool)
│
├── examples/                 # Usage examples
├── PLAN.md                   # This file
├── README.md
├── go.mod
└── LICENSE
```

---

## C Shim API

All COM state lives inside the shim. Callers hold an opaque `uint64_t` session handle.

```c
/* ── Session lifecycle ─────────────────────────────────────────── */
gokd_session_t  gokd_create_session(void);
void            gokd_destroy_session(gokd_session_t s);

/* ── Attach modes ──────────────────────────────────────────────── */
int  gokd_attach_process(gokd_session_t, uint32_t pid, uint32_t flags);
int  gokd_create_process(gokd_session_t, const wchar_t *cmd, uint32_t flags);
int  gokd_attach_kernel(gokd_session_t, const wchar_t *options);
     // options: L"net:port=50000,key=..." | L"com:port=\\\\.\\COM1,baud=115200"
int  gokd_open_dump(gokd_session_t, const wchar_t *path);
int  gokd_detach(gokd_session_t);

/* ── Execution control ─────────────────────────────────────────── */
int  gokd_go(gokd_session_t);
int  gokd_step_in(gokd_session_t);
int  gokd_step_over(gokd_session_t);
int  gokd_step_out(gokd_session_t);
int  gokd_break_in(gokd_session_t);   /* async break-in */

/* ── Memory ────────────────────────────────────────────────────── */
int  gokd_read_virtual(gokd_session_t, uint64_t addr,
                        void *buf, size_t len, size_t *out_read);
int  gokd_write_virtual(gokd_session_t, uint64_t addr,
                         const void *buf, size_t len);
int  gokd_read_physical(gokd_session_t, uint64_t addr,
                         void *buf, size_t len, size_t *out_read);

/* ── Registers ─────────────────────────────────────────────────── */
int  gokd_get_registers(gokd_session_t, gokd_register_t *out,
                         uint32_t *count);
int  gokd_set_register(gokd_session_t, const char *name, uint64_t value);

/* ── Stack ─────────────────────────────────────────────────────── */
int  gokd_get_stack(gokd_session_t, gokd_frame_t *out,
                    uint32_t max, uint32_t *count);

/* ── Symbols & types (DbgEng + DbgHelp) ───────────────────────── */
int  gokd_name_to_addr(gokd_session_t, const char *name, uint64_t *addr);
int  gokd_addr_to_name(gokd_session_t, uint64_t addr,
                        char *name, size_t len, uint64_t *displacement);
int  gokd_get_type_size(gokd_session_t, const char *module,
                         const char *type_name, uint64_t *size);
int  gokd_get_field_offset(gokd_session_t, const char *module,
                            const char *type_name, const char *field,
                            uint32_t *offset);
int  gokd_get_type_fields(gokd_session_t, const char *module,
                           const char *type_name,
                           gokd_field_t *out, uint32_t max, uint32_t *count);

/* ── Modules ───────────────────────────────────────────────────── */
int  gokd_get_modules(gokd_session_t, gokd_module_t *out,
                       uint32_t max, uint32_t *count);

/* ── Threads ───────────────────────────────────────────────────── */
int  gokd_get_threads(gokd_session_t, gokd_thread_t *out,
                       uint32_t max, uint32_t *count);
int  gokd_set_current_thread(gokd_session_t, uint32_t sys_tid);

/* ── Breakpoints ───────────────────────────────────────────────── */
int  gokd_add_breakpoint(gokd_session_t, uint64_t addr, uint32_t *out_id);
int  gokd_add_breakpoint_sym(gokd_session_t, const char *symbol,
                              uint32_t *out_id);
int  gokd_remove_breakpoint(gokd_session_t, uint32_t id);
int  gokd_list_breakpoints(gokd_session_t, gokd_bp_t *out,
                            uint32_t max, uint32_t *count);

/* ── Disassembly ───────────────────────────────────────────────── */
int  gokd_disassemble(gokd_session_t, uint64_t addr,
                       char *out, size_t len, uint64_t *next_addr);

/* ── Symbol path ───────────────────────────────────────────────── */
int  gokd_set_symbol_path(gokd_session_t, const char *path);
int  gokd_get_symbol_path(gokd_session_t, char *out, size_t len);

/* ── Callbacks (CGo exports called from C) ─────────────────────── */
typedef void (*gokd_event_fn)(gokd_session_t, int event_type,
                               const void *event_data, void *ctx);
typedef void (*gokd_output_fn)(uint32_t mask, const char *text, void *ctx);

void gokd_set_event_callback(gokd_session_t, gokd_event_fn, void *ctx);
void gokd_set_output_callback(gokd_session_t, gokd_output_fn, void *ctx);

/* ── Escape hatch ──────────────────────────────────────────────── */
int  gokd_execute(gokd_session_t, const char *cmd,
                   char *out, size_t out_len);
```

---

## Go Public API

```go
// Session is the top-level handle to a debug session.
type Session interface {
    // Target attachment
    AttachProcess(pid uint32, opts AttachOptions) error
    CreateProcess(cmd string, opts CreateOptions) error
    AttachKernel(connectStr string) error   // "net:port=50000,key=..."
    OpenDump(path string) error
    Detach() error

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
    ReadTyped(addr uint64, module, typeName string) (*TypedValue, error)

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
    AddrToName(addr uint64) (string, uint64, error)  // name, displacement

    // Types (PDB-aware, via DbgHelp)
    TypeSize(module, typeName string) (uint64, error)
    TypeFields(module, typeName string) ([]*Field, error)

    // Breakpoints
    AddBreakpoint(addr uint64) (*Breakpoint, error)
    AddBreakpointSym(symbol string) (*Breakpoint, error)
    RemoveBreakpoint(id uint32) error
    Breakpoints() ([]*Breakpoint, error)

    // Disassembly
    Disassemble(addr uint64) (*Instruction, error)
    DisassembleRange(addr uint64, count int) ([]*Instruction, error)

    // Symbol path
    SetSymbolPath(path string) error
    SymbolPath() (string, error)

    // Async streams
    Events() <-chan Event    // breakpoint, exception, thread/process/module lifecycle
    Output() <-chan string   // raw debugger output

    // Escape hatch (avoid where possible)
    Execute(cmd string) (string, error)

    Close() error
}

// New creates a new debug session.
func New() (Session, error)
```

---

## Key Types

```go
type StopEvent struct {
    Reason     StopReason   // Breakpoint, Step, Exception, ProcessExit
    Address    uint64
    Thread     *Thread
    Exception  *ExceptionInfo  // non-nil when Reason == Exception
}

type Frame struct {
    InstructionOffset uint64
    ReturnOffset      uint64
    FrameOffset       uint64
    StackOffset       uint64
    Module            string
    Function          string
    Displacement      uint32
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
}

type Thread struct {
    SystemID    uint32
    Handle      uint64
    DataOffset  uint64
    StartOffset uint64
}

type Breakpoint struct {
    ID      uint32
    Address uint64
    Symbol  string
    Enabled bool
}

type Field struct {
    Name   string
    Offset uint32
    Size   uint64
    Type   string
}

type TypedValue struct {
    TypeName string
    Address  uint64
    Fields   map[string]interface{}
}

// Event types delivered on Session.Events()
type Event interface{ eventTag() }

type BreakpointEvent  struct { ID uint32; Address uint64; Thread *Thread }
type ExceptionEvent   struct { Code uint32; Address uint64; FirstChance bool }
type ThreadCreated    struct { Thread *Thread }
type ThreadExited     struct { SystemID uint32; ExitCode uint32 }
type ProcessCreated   struct { ImageName string; BaseOffset uint64 }
type ProcessExited    struct { ExitCode uint32 }
type ModuleLoaded     struct { Module *Module }
type ModuleUnloaded   struct { ImageBaseName string; BaseOffset uint64 }
```

---

## Phased Delivery

### Phase 1 — C++ Shim
- `cshim/gokd_shim.h` — complete C API definition
- `cshim/dispatch_thread.cpp` — DbgEng OS thread + command queue
- `cshim/callbacks.cpp` — `IDebugEventCallbacks` + `IDebugOutputCallbacks`
- `cshim/gokd_shim.cpp` — all COM → C implementations
- Builds with MSVC or MinGW (CGo-compatible toolchain)
- **Validates:** attach to a live process, read memory, detach

### Phase 2 — CGo Bindings
- `internal/dbgcgo/` — thin unsafe Go wrappers over every shim function
- OS-locked goroutine managing the dispatch loop
- CGo `//export` callbacks routing C events into Go channels
- **Validates:** Go test: attach, set breakpoint, `Go()`, receive breakpoint event

### Phase 3 — Go Public API (`pkg/`)
- All packages under `pkg/` implemented
- `pkg/session` wraps CGo layer behind the `Session` interface
- `pkg/types` implements full PDB field/offset walking
- `pkg/events` channel fan-out from single CGo callback
- **Validates:** integration test against a real process and a crash dump

### Phase 4 — CLI / REPL (`cmd/gokd`)
- Interactive REPL proving the full API end-to-end
- Commands: `attach`, `detach`, `bp`, `g`, `t`, `p`, `k`, `r`, `dq`, `dt`, `lm`, `q`
- **Validates:** full user-mode debug session interactively

### Phase 4b — Kernel Mode
- `pkg/target/kmode.go` — `AttachKernel()` with KDNET/serial/USB options
- Kernel-specific helpers: `!process`, `!thread` equivalents returning Go structs
- **Validates:** live kernel debug session over KDNET

### Phase 4c — Crash Dumps
- `pkg/target/dump.go` — `OpenDump()` for full memory dumps and minidumps
- **Validates:** open and inspect a real BSOD dump

### Phase 5 — MCP Server (separate repository)
- Built on top of the `Session` interface
- `github.com/nijosmsft/gokd-mcp` (separate package, separate repo)

---

## Notes on DbgHelp and Remote Debugging

DbgShell documented a known limitation: DbgEng's symbol/type API is insufficient for deep type
introspection, so DbgShell uses DbgHelp — which has no remote variant.

GoKD addresses this differently:

- The C++ shim runs **on the same machine as the target** (for local debugging, this is the same
  machine as the Go process; for kernel debugging, the shim is on the host KD machine).
- DbgHelp calls happen **inside the shim**, which always has local access to symbols.
- The Go layer receives resolved type data — it never calls DbgHelp directly.
- For fully remote scenarios (shim on machine A, Go consumer on machine B), the shim can be
  wrapped in a lightweight transport layer without changing the Go API.

---

## Prerequisites

- Windows 10/11 x64 (build host and target)
- Go 1.22+
- MinGW-w64 (GCC toolchain for CGo on Windows) **or** MSVC with CGo support
- Windows SDK (for `dbgeng.h`, `dbghelp.h`)
- WinDbg / Debugging Tools for Windows (provides `dbgeng.dll`, `dbghelp.dll`)
- `_NT_SYMBOL_PATH` set to a symbol server (e.g. `srv*c:\symbols*https://msdl.microsoft.com/download/symbols`)

---

## License

MIT
