# GoKD — Project Plan

**GoKD** is a Go library for Windows debugging, providing a clean, idiomatic Go API over the
Windows DbgEng engine (`dbgeng.dll`) — the same engine that powers `kd.exe`, `cdb.exe`, and
`windbg.exe`. It targets user-mode processes, kernel-mode targets (via KDNET, serial, USB), and
offline crash dump analysis.

GoKD is a library only. A separate MCP server package (`gokd-mcp`) will be built on top of it.

---

## Design Goals

- **No text parsing.** All operations use DbgEng's typed COM interfaces and DbgHelp's symbol APIs.
  Structured Go types are returned everywhere.
- **Stable under adversarial data.** Crash dumps and live targets may contain corrupt or malformed
  memory. The C++ shim wraps all DbgEng calls in SEH handlers; the Go layer never sees raw COM
  errors or access violations.
- **Single-process, no IPC.** The C++ shim compiles directly into the Go binary via CGo. No
  separate agent process, no gRPC, no serialization overhead on the hot path.
- **DbgEng thread affinity respected.** A dedicated OS-locked goroutine owns all DbgEng calls.
  Commands from other goroutines are queued and executed on that goroutine.
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
│  CGo //export callbacks for events/output       │
└────────────────────┬────────────────────────────┘
                     │  links against pre-built static lib
┌────────────────────▼────────────────────────────┐
│         cshim/ — C++ DbgEng shim                │
│  Flat C API  (no COM objects cross CGo boundary)│
│  Dispatch thread + command queue                │
│  IDebugEventCallbacks implementation            │
│  DbgHelp for full type / symbol information     │
└────────────────────┬────────────────────────────┘
                     │
              dbgeng.dll · dbghelp.dll
```

### Why a C++ shim?

CGo cannot call C++ COM vtable methods directly. The shim translates every DbgEng COM interface
(`IDebugClient6`, `IDebugControl6`, `IDebugSymbols5`, `IDebugDataSpaces4`, `IDebugRegisters2`,
`IDebugSystemObjects4`, `IDebugAdvanced3`) into a **flat C API**. Only plain C types and opaque
`uint64_t` session handles cross the CGo boundary.

---

## Build System

### Toolchain

CGo on Windows requires a GCC-based toolchain. We use **MinGW-w64** (via MSYS2):

```
pacman -S mingw-w64-x86_64-gcc
```

The C++ shim is compiled with `x86_64-w64-mingw32-g++` into a static library
(`libgokd_shim.a`). CGo links against this library.

### Directory layout for build artefacts

```
cshim/
├── gokd_shim.h          ← public C header (only file CGo includes)
├── gokd_shim.cpp        ← COM → C implementations
├── dispatch_thread.cpp  ← DbgEng dispatch thread
├── callbacks.cpp        ← IDebugEventCallbacks impl
├── Makefile             ← builds libgokd_shim.a
└── lib/
    └── libgokd_shim.a   ← output (git-ignored; built before go build)
```

### cshim/Makefile

```makefile
CXX      = x86_64-w64-mingw32-g++
CXXFLAGS = -std=c++17 -Wall \
           -I"$(WINDBG_SDK)/inc" \
           -I"$(WINDBG_SDK)/sdk/inc"
AR       = x86_64-w64-mingw32-ar

SRCS = gokd_shim.cpp dispatch_thread.cpp callbacks.cpp
OBJS = $(SRCS:.cpp=.o)

lib/libgokd_shim.a: $(OBJS)
	mkdir -p lib
	$(AR) rcs $@ $^

%.o: %.cpp gokd_shim.h
	$(CXX) $(CXXFLAGS) -c $< -o $@

clean:
	rm -f $(OBJS) lib/libgokd_shim.a
```

`WINDBG_SDK` points to the Debugging Tools for Windows SDK directory
(e.g. `C:\Program Files (x86)\Windows Kits\10\Debuggers\x64`).

### CGo directives (internal/dbgcgo/dbgeng.go)

```go
/*
#cgo CFLAGS:  -I${SRCDIR}/../../cshim
#cgo LDFLAGS: -L${SRCDIR}/../../cshim/lib -lgokd_shim
#cgo LDFLAGS: -ldbgeng -ldbghelp -lole32 -luuid
#include "gokd_shim.h"
*/
import "C"
```

`dbgeng.lib`, `dbghelp.lib`, `ole32.lib`, `uuid.lib` are linked from the MinGW sysroot, which
includes import libraries for standard Windows DLLs.

### Build order

```
1. make -C cshim          # produces cshim/lib/libgokd_shim.a
2. go build ./...         # CGo links against libgokd_shim.a
```

A top-level `Makefile` or `build.bat` wraps both steps. `go generate` in
`internal/dbgcgo/dbgeng.go` triggers step 1:

```go
//go:generate make -C ../../cshim
```

---

## DbgEng Thread Affinity & Dispatch Mechanism

DbgEng has strict thread affinity: **all calls, including `WaitForEvent`, must be made from the
thread that called `DebugCreate`**. This is because DbgEng queues APCs to that thread and RPC
connections (for remote targets) are tied to it.

### The dispatch goroutine

`internal/dbgcgo` starts a single dedicated goroutine on session creation:

```go
func startDispatch(s *shimSession) {
    go func() {
        runtime.LockOSThread()   // pin this goroutine to its OS thread forever
        defer runtime.UnlockOSThread()

        // COM must be initialized on the DbgEng thread
        ole32CoInitializeEx(COINIT_MULTITHREADED)
        defer ole32CoUninitialize()

        s.handle = C.gokd_create_session()   // calls DebugCreate internally

        // Signal that the session is ready
        close(s.readyCh)

        // Dispatch loop
        for cmd := range s.cmdCh {
            cmd.result <- cmd.fn()
        }
    }()
    <-s.readyCh
}
```

### Command pattern

Every DbgEng operation is posted as a closure to `cmdCh` and the caller blocks on the result:

```go
type command struct {
    fn     func() error
    result chan error
}

func (s *shimSession) exec(fn func() error) error {
    cmd := command{fn: fn, result: make(chan error, 1)}
    s.cmdCh <- cmd
    return <-cmd.result
}
```

### Execution commands (Go / StepIn / StepOver / StepOut)

Execution commands call `WaitForEvent`, which blocks the dispatch thread until a stop event fires.
While the target is running, other commands queue up and are processed after the target stops.

```
cmd.fn = func() error {
    // 1. Set execution status (go / step)
    IDebugControl::SetExecutionStatus(DEBUG_STATUS_GO)

    // 2. Block on WaitForEvent — fires IDebugEventCallbacks synchronously on this thread
    hr = IDebugControl::WaitForEvent(DEBUG_WAIT_DEFAULT, INFINITE)

    // 3. Callbacks ran, stop event was captured — return to caller
    return hresultToError(hr)
}
```

The `IDebugEventCallbacks` implementation (in `callbacks.cpp`) captures the stop reason and
details into the session struct, then returns `DEBUG_STATUS_BREAK` to halt execution.

### Async break-in

`BreakIn()` calls `IDebugControl::SetInterrupt(DEBUG_INTERRUPT_ACTIVE)` which is documented as
safe to call from any thread. It does NOT go through the command queue.

---

## C Shim — Complete Header

```c
/* gokd_shim.h — the only C header included by CGo */

#pragma once
#include <stdint.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* ── Types ────────────────────────────────────────────────────── */

typedef uint64_t gokd_session_t;   /* 0 = invalid */

/* Stack frame */
typedef struct {
    uint64_t instruction_offset;
    uint64_t return_offset;
    uint64_t frame_offset;
    uint64_t stack_offset;
    char     module[256];
    char     function[512];
    uint64_t displacement;
    char     source_file[512];
    uint32_t source_line;
} gokd_frame_t;

/* Register */
typedef struct {
    char     name[64];
    uint64_t value;
    uint32_t type;      /* GOKD_REG_TYPE_* */
    uint8_t  valid;
} gokd_register_t;

/* Register value types (matches DEBUG_VALUE types) */
#define GOKD_REG_TYPE_INT8      0
#define GOKD_REG_TYPE_INT16     1
#define GOKD_REG_TYPE_INT32     2
#define GOKD_REG_TYPE_INT64     3
#define GOKD_REG_TYPE_FLOAT32   4
#define GOKD_REG_TYPE_FLOAT64   5
#define GOKD_REG_TYPE_FLOAT80   6
#define GOKD_REG_TYPE_VECTOR128 7

/* Module */
typedef struct {
    char     name[256];         /* short name, no path */
    char     image_name[512];   /* full image path */
    uint64_t base;
    uint32_t size;
    uint32_t timestamp;
    uint32_t checksum;
} gokd_module_t;

/* Thread */
typedef struct {
    uint32_t system_id;
    uint64_t handle;
    uint64_t data_offset;
    uint64_t start_offset;
} gokd_thread_t;

/* Breakpoint */
typedef struct {
    uint32_t id;
    uint64_t offset;
    char     expression[512];
    uint32_t flags;             /* DEBUG_BREAKPOINT_* */
    uint32_t enabled;
} gokd_bp_t;

/* Type field */
typedef struct {
    char     name[256];
    uint32_t offset;            /* byte offset within parent struct */
    uint64_t size;              /* size in bytes */
    char     type_name[256];
} gokd_field_t;

/* ── Event types ──────────────────────────────────────────────── */

#define GOKD_EVENT_BREAKPOINT      1
#define GOKD_EVENT_EXCEPTION       2
#define GOKD_EVENT_THREAD_CREATED  3
#define GOKD_EVENT_THREAD_EXITED   4
#define GOKD_EVENT_PROC_CREATED    5
#define GOKD_EVENT_PROC_EXITED     6
#define GOKD_EVENT_MOD_LOADED      7
#define GOKD_EVENT_MOD_UNLOADED    8
#define GOKD_EVENT_STOP            9   /* WaitForEvent returned (step done) */

/* Stop reasons (inside GOKD_EVENT_STOP) */
#define GOKD_STOP_BREAKPOINT    1
#define GOKD_STOP_STEP          2
#define GOKD_STOP_EXCEPTION     3
#define GOKD_STOP_PROC_EXIT     4

/* Event data structs — passed as void* to the event callback */

typedef struct {
    uint32_t bp_id;
    uint64_t address;
    uint32_t thread_sys_id;
} gokd_ev_breakpoint_t;

typedef struct {
    uint32_t code;
    uint64_t address;
    uint32_t first_chance;
    uint32_t thread_sys_id;
} gokd_ev_exception_t;

typedef struct {
    uint32_t sys_id;
    uint64_t handle;
    uint64_t data_offset;
    uint64_t start_offset;
} gokd_ev_thread_created_t;

typedef struct {
    uint32_t sys_id;
    uint32_t exit_code;
} gokd_ev_thread_exited_t;

typedef struct {
    uint64_t base_offset;
    uint32_t module_size;
    char     module_name[256];
    char     image_name[512];
} gokd_ev_proc_created_t;

typedef struct {
    uint32_t exit_code;
} gokd_ev_proc_exited_t;

typedef struct {
    uint64_t base_offset;
    uint32_t module_size;
    char     module_name[256];
    char     image_name[512];
} gokd_ev_mod_loaded_t;

typedef struct {
    uint64_t base_offset;
    char     image_base_name[256];
} gokd_ev_mod_unloaded_t;

typedef struct {
    uint32_t reason;            /* GOKD_STOP_* */
    uint64_t address;
    uint32_t thread_sys_id;
} gokd_ev_stop_t;

/* ── Callbacks ────────────────────────────────────────────────── */

/* Event callback: fired from the dispatch thread during WaitForEvent */
typedef void (*gokd_event_fn)(gokd_session_t s, int event_type,
                               const void *event_data, void *ctx);

/* Output callback: fired from the dispatch thread */
typedef void (*gokd_output_fn)(uint32_t mask, const char *text, void *ctx);

/* ── Error convention ─────────────────────────────────────────── */
/*
 * All functions that return int32_t return an HRESULT.
 * S_OK (0)   = success.
 * Negative   = failure (standard HRESULT error code).
 * Use SUCCEEDED(hr) / FAILED(hr) macros to test.
 *
 * gokd_create_session() returns 0 on failure.
 * gokd_get_last_error() returns the HRESULT from the most recent failed call
 * on this session (thread-local within the dispatch thread).
 */

/* ── String encoding ──────────────────────────────────────────── */
/*
 * All char* parameters and buffers are UTF-8.
 * The shim converts to/from UTF-16 (wchar_t*) internally before calling DbgEng.
 * Callers never deal with wchar_t.
 */

/* ── Session lifecycle ────────────────────────────────────────── */

gokd_session_t  gokd_create_session(void);
void            gokd_destroy_session(gokd_session_t s);
int32_t         gokd_get_last_error(gokd_session_t s);

/* ── Attach modes ─────────────────────────────────────────────── */

int32_t  gokd_attach_process(gokd_session_t s, uint32_t pid, uint32_t flags);
         /* flags: 0 = default, DEBUG_ATTACH_NONINVASIVE, etc. */

int32_t  gokd_create_process(gokd_session_t s, const char *cmd, uint32_t flags);
         /* flags: DEBUG_PROCESS, DEBUG_ONLY_THIS_PROCESS, etc. */

int32_t  gokd_attach_kernel(gokd_session_t s, const char *options);
         /* options: "net:port=50000,key=..." | "com:port=\\\\.\\COM1,baud=115200" */

int32_t  gokd_open_dump(gokd_session_t s, const char *path);

int32_t  gokd_detach(gokd_session_t s);

/* ── Execution control ────────────────────────────────────────── */

int32_t  gokd_go(gokd_session_t s);
int32_t  gokd_step_in(gokd_session_t s);
int32_t  gokd_step_over(gokd_session_t s);
int32_t  gokd_step_out(gokd_session_t s);
int32_t  gokd_break_in(gokd_session_t s);    /* safe to call from any thread */

/* ── Memory ───────────────────────────────────────────────────── */

int32_t  gokd_read_virtual(gokd_session_t s, uint64_t addr,
                            void *buf, size_t len, size_t *out_read);
int32_t  gokd_write_virtual(gokd_session_t s, uint64_t addr,
                             const void *buf, size_t len);
int32_t  gokd_read_physical(gokd_session_t s, uint64_t addr,
                             void *buf, size_t len, size_t *out_read);

/* ── Registers ────────────────────────────────────────────────── */

int32_t  gokd_get_registers(gokd_session_t s,
                             gokd_register_t *out, uint32_t *count);
         /* call with out=NULL to get count first */

int32_t  gokd_set_register(gokd_session_t s,
                            const char *name, uint64_t value);

/* ── Stack ────────────────────────────────────────────────────── */

int32_t  gokd_get_stack(gokd_session_t s,
                         gokd_frame_t *out, uint32_t max, uint32_t *count);

/* ── Symbols ──────────────────────────────────────────────────── */

int32_t  gokd_name_to_addr(gokd_session_t s,
                            const char *name, uint64_t *addr);
int32_t  gokd_addr_to_name(gokd_session_t s, uint64_t addr,
                            char *name, size_t name_len,
                            uint64_t *displacement);

int32_t  gokd_set_symbol_path(gokd_session_t s, const char *path);
int32_t  gokd_get_symbol_path(gokd_session_t s, char *out, size_t len);

/* ── Types (DbgHelp) ──────────────────────────────────────────── */

int32_t  gokd_get_type_size(gokd_session_t s,
                             const char *module, const char *type_name,
                             uint64_t *size);
int32_t  gokd_get_field_offset(gokd_session_t s,
                                const char *module, const char *type_name,
                                const char *field, uint32_t *offset);
int32_t  gokd_get_type_fields(gokd_session_t s,
                               const char *module, const char *type_name,
                               gokd_field_t *out, uint32_t max,
                               uint32_t *count);
         /* call with out=NULL to get count first */

/* ── Modules ──────────────────────────────────────────────────── */

int32_t  gokd_get_modules(gokd_session_t s,
                           gokd_module_t *out, uint32_t max, uint32_t *count);

/* ── Threads ──────────────────────────────────────────────────── */

int32_t  gokd_get_threads(gokd_session_t s,
                           gokd_thread_t *out, uint32_t max, uint32_t *count);
int32_t  gokd_set_current_thread(gokd_session_t s, uint32_t sys_tid);

/* ── Breakpoints ──────────────────────────────────────────────── */

int32_t  gokd_add_breakpoint(gokd_session_t s,
                              uint64_t addr, uint32_t *out_id);
int32_t  gokd_add_breakpoint_sym(gokd_session_t s,
                                  const char *symbol, uint32_t *out_id);
int32_t  gokd_remove_breakpoint(gokd_session_t s, uint32_t id);
int32_t  gokd_enable_breakpoint(gokd_session_t s, uint32_t id, int enable);
int32_t  gokd_list_breakpoints(gokd_session_t s,
                                gokd_bp_t *out, uint32_t max, uint32_t *count);

/* ── Disassembly ──────────────────────────────────────────────── */

int32_t  gokd_disassemble(gokd_session_t s, uint64_t addr,
                           char *out, size_t len, uint64_t *next_addr);

/* ── Callbacks ────────────────────────────────────────────────── */

void  gokd_set_event_callback(gokd_session_t s, gokd_event_fn cb, void *ctx);
void  gokd_set_output_callback(gokd_session_t s, gokd_output_fn cb, void *ctx);

/* ── Escape hatch ─────────────────────────────────────────────── */

int32_t  gokd_execute(gokd_session_t s, const char *cmd,
                       char *out, size_t out_len);

#ifdef __cplusplus
}
#endif
```

---

## Go Module

```
// go.mod
module github.com/nijosmsft/gokd

go 1.22
```

---

## Go Public API — Complete Types

```go
package gokd

import "context"

// ── Session ───────────────────────────────────────────────────────────────

// Session is the top-level handle to a debug session.
// All methods are safe to call from any goroutine.
type Session interface {
    // Target attachment
    AttachProcess(pid uint32, opts AttachOptions) error
    CreateProcess(cmd string, opts CreateOptions) error
    AttachKernel(connectStr string) error   // "net:port=50000,key=..."
    OpenDump(path string) error
    Detach() error

    // Execution — block until a stop event occurs or ctx is cancelled
    Go(ctx context.Context) (*StopEvent, error)
    StepIn(ctx context.Context) (*StopEvent, error)
    StepOver(ctx context.Context) (*StopEvent, error)
    StepOut(ctx context.Context) (*StopEvent, error)
    BreakIn() error   // async; does not wait for stop

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
    AddrToName(addr uint64) (string, uint64, error)  // name, displacement, err

    // Symbol path
    SetSymbolPath(path string) error
    SymbolPath() (string, error)

    // Types (PDB-aware, via DbgHelp)
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

    // Async streams (buffered channels; drop events if consumer is slow)
    Events() <-chan Event    // breakpoint, exception, thread/process/module lifecycle
    Output() <-chan string   // raw debugger output lines

    // Escape hatch — use sparingly; output is unstructured text
    Execute(cmd string) (string, error)

    Close() error
}

// New creates a new debug session.
// Starts the dispatch goroutine and initialises DbgEng on its thread.
func New() (Session, error)

// ── Attach / Create options ───────────────────────────────────────────────

type AttachOptions struct {
    // DEBUG_ATTACH_* flags. 0 = default (invasive attach).
    // Use AttachNonInvasive for read-only inspection.
    Flags uint32
}

var (
    AttachDefault     = AttachOptions{Flags: 0}
    AttachNonInvasive = AttachOptions{Flags: 0x00000001} // DEBUG_ATTACH_NONINVASIVE
    AttachExisting    = AttachOptions{Flags: 0x00000002} // DEBUG_ATTACH_EXISTING
)

type CreateOptions struct {
    // DEBUG_PROCESS / DEBUG_ONLY_THIS_PROCESS / etc.
    Flags        uint32
    InitialBreak bool   // break at process entry point
}

// ── Stop event ────────────────────────────────────────────────────────────

type StopReason int

const (
    StopBreakpoint  StopReason = iota + 1
    StopStep
    StopException
    StopProcessExit
)

func (r StopReason) String() string {
    switch r {
    case StopBreakpoint:  return "Breakpoint"
    case StopStep:        return "Step"
    case StopException:   return "Exception"
    case StopProcessExit: return "ProcessExit"
    default:              return "Unknown"
    }
}

type StopEvent struct {
    Reason    StopReason
    Address   uint64
    Thread    *Thread
    Exception *ExceptionInfo // non-nil when Reason == StopException
}

type ExceptionInfo struct {
    Code        uint32
    Address     uint64
    FirstChance bool
}

// ── Core types ────────────────────────────────────────────────────────────

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
    Name      string // short name, no path
    ImageName string // full image path
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
    RegisterInt8   RegisterType = iota
    RegisterInt16
    RegisterInt32
    RegisterInt64
    RegisterFloat32
    RegisterFloat64
    RegisterFloat80
    RegisterVector128
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
    Expression string // symbol or address expression
    Enabled    bool
}

type Field struct {
    Name     string
    Offset   uint32  // byte offset within parent struct
    Size     uint64
    TypeName string
}

type TypedValue struct {
    TypeName string
    Address  uint64
    Fields   map[string]interface{}
}

type Instruction struct {
    Address uint64
    Text    string  // disassembly text, e.g. "mov rax,rcx"
    Size    uint32  // byte size of instruction
    Bytes   []byte
}

// ── Events ────────────────────────────────────────────────────────────────

// Event is delivered on the channel returned by Session.Events().
type Event interface{ isEvent() }

type BreakpointEvent struct {
    ID       uint32
    Address  uint64
    Thread   *Thread
}

type ExceptionEvent struct {
    Code        uint32
    Address     uint64
    FirstChance bool
    Thread      *Thread
}

type ThreadCreatedEvent struct{ Thread *Thread }
type ThreadExitedEvent  struct{ SystemID uint32; ExitCode uint32 }

type ProcessCreatedEvent struct {
    ImageName   string
    BaseOffset  uint64
    ModuleSize  uint32
}

type ProcessExitedEvent  struct{ ExitCode uint32 }
type ModuleLoadedEvent   struct{ Module *Module }
type ModuleUnloadedEvent struct{ ImageBaseName string; BaseOffset uint64 }

func (BreakpointEvent)    isEvent() {}
func (ExceptionEvent)     isEvent() {}
func (ThreadCreatedEvent) isEvent() {}
func (ThreadExitedEvent)  isEvent() {}
func (ProcessCreatedEvent)isEvent() {}
func (ProcessExitedEvent) isEvent() {}
func (ModuleLoadedEvent)  isEvent() {}
func (ModuleUnloadedEvent)isEvent() {}
```

---

## Repository Structure

```
GoKD/
├── cshim/
│   ├── gokd_shim.h           # Public C API — only file CGo includes
│   ├── gokd_shim.cpp         # COM → C implementations
│   ├── dispatch_thread.cpp   # DbgEng dispatch thread + command queue
│   ├── callbacks.cpp         # IDebugEventCallbacks + IDebugOutputCallbacks
│   ├── Makefile
│   └── lib/                  # .gitignored; built artefacts
│       └── libgokd_shim.a
│
├── internal/
│   └── dbgcgo/
│       ├── dbgeng.go         # CGo bindings (unsafe, not exported)
│       └── callbacks.go      # //export Go functions called from C
│
├── pkg/
│   ├── session/              # Session interface + engine impl
│   ├── target/
│   │   ├── umode.go          # User-mode process
│   │   ├── kmode.go          # Kernel-mode (KDNET / serial / USB / EXDI)
│   │   └── dump.go           # Crash dump / minidump
│   ├── memory/               # Virtual + physical read/write
│   ├── registers/            # Register access + arch register sets (x64, ARM64)
│   ├── symbols/              # Symbol resolution, symbol path management
│   ├── types/                # PDB type system: field walking, offset resolution
│   ├── stack/                # Stack walk, frame info, source line mapping
│   ├── modules/              # Module enumeration, load/unload tracking
│   ├── threads/              # Thread enumeration + context switching
│   ├── breakpoints/          # SW / HW breakpoints, data breakpoints
│   ├── events/               # Event channel fan-out from single CGo callback
│   └── disasm/               # Disassembly
│
├── cmd/
│   └── gokd/                 # Interactive REPL / CLI
│
├── examples/
│   ├── attach/               # Attach to process, set breakpoint, print stack
│   └── dump/                 # Open crash dump, print stop reason + stack
│
├── go.mod
├── go.sum
├── PLAN.md
├── README.md
└── LICENSE
```

---

## Phased Delivery

### Phase 1 — C++ Shim
**Goal:** The shim compiles cleanly and can attach to a live process, read virtual memory, and
detach. No Go code yet.

Deliverables:
- `cshim/gokd_shim.h` — as specified above (complete)
- `cshim/dispatch_thread.cpp` — COM init, `DebugCreate`, command queue loop
- `cshim/callbacks.cpp` — `IDebugEventCallbacks` implementation
- `cshim/gokd_shim.cpp` — all function bodies
- `cshim/Makefile` builds `lib/libgokd_shim.a` cleanly with MinGW-w64

Validation: a standalone C test program (`cshim/test_attach.c`) that calls
`gokd_create_session`, `gokd_attach_process`, `gokd_read_virtual`, `gokd_detach`,
`gokd_destroy_session` and prints results.

### Phase 2 — CGo Bindings
**Goal:** Attach, set a breakpoint, `Go()`, receive the breakpoint event — entirely from Go.

Deliverables:
- `internal/dbgcgo/dbgeng.go` — CGo wrappers for every shim function
- `internal/dbgcgo/callbacks.go` — `//export GoEventCallback` and `//export GoOutputCallback`
  routing events to a per-session Go channel
- Dispatch goroutine and command queue in `internal/dbgcgo/`

Validation: `go test ./internal/dbgcgo/` integration test that attaches to a known process.

### Phase 3 — Go Public API
**Goal:** All `pkg/` packages implemented behind the `Session` interface.

Deliverables:
- All packages under `pkg/` with full implementations
- `pkg/session` wraps the CGo layer; implements `Session`
- `pkg/events` fan-out: single CGo callback → typed event channel
- `pkg/types` full PDB field walking via `gokd_get_type_fields`

Validation: integration tests against a live process and a crash dump file.

### Phase 4 — CLI / REPL
**Goal:** A usable interactive debugger proving the full API end-to-end.

```
$ gokd
> attach 4521
Attached to notepad.exe (pid 4521)
> bp kernel32!CreateFileW
Breakpoint 0 at kernel32!CreateFileW (0x00007ff8a1b34c20)
> g
[bp 0] kernel32!CreateFileW
> k
 #  Address              Symbol
 0  00007ff8a1b34c20  kernel32!CreateFileW
 1  00007ff69a12c344  notepad!CFile::Open+0x54
> r rax rbx rip
rax=0000000000000000  rbx=000001a34f2c0000  rip=00007ff8a1b34c20
> dt ntdll!_TEB  fs:[0]
+0x000  NtTib   : _NT_TIB
...
> q
```

Commands: `attach`, `detach`, `bp`, `bc`, `bd`, `be`, `bl`, `g`, `t`, `p`, `gu`, `k`, `r`,
`dq`, `dd`, `db`, `dt`, `lm`, `u`, `q`

### Phase 4b — Kernel Mode
- `pkg/target/kmode.go` — `AttachKernel()` with KDNET / serial / USB options
- Kernel helpers: process list, thread list returning Go structs (no text parsing)

Validation: live kernel debug session over KDNET to a VM.

### Phase 4c — Crash Dumps
- `pkg/target/dump.go` — `OpenDump()` for full memory dumps and minidumps

Validation: open a real BSOD `.dmp` file and print stop reason, faulting thread stack.

### Phase 5 — MCP Server (separate repository)
- `github.com/nijosmsft/gokd-mcp`
- Built on top of the `Session` interface
- Each MCP tool maps 1:1 to a `Session` method; returns structured JSON

---

## Notes on DbgHelp and Remote Debugging

DbgShell documented a known limitation: DbgEng's type API is insufficient for deep struct
introspection, so DbgShell added DbgHelp — which has no remote equivalent.

GoKD avoids this problem by design:

- DbgHelp is called **inside the C++ shim**, which always runs on the same machine as the
  debugging session. For local debugging this is trivially true. For kernel debugging over KDNET,
  the shim runs on the host (KD) machine, which has local access to symbols via `_NT_SYMBOL_PATH`.
- The Go layer receives resolved type data (field names, offsets, sizes) — it never calls DbgHelp.
- For a fully remote scenario (shim on machine A, Go consumer on machine B), the shim can be
  wrapped in a transport layer without any change to the Go public API.

---

## Prerequisites

| Requirement | Notes |
|---|---|
| Windows 10/11 x64 | Build host and debug target |
| Go 1.22+ | `go.mod` minimum version |
| MSYS2 + MinGW-w64 | `pacman -S mingw-w64-x86_64-gcc` — required for CGo |
| Debugging Tools for Windows | Provides `dbgeng.dll`, `dbghelp.dll`, `dbgeng.h`, `dbghelp.h` |
| WinDbg (optional) | For manual verification of results |
| `_NT_SYMBOL_PATH` | Set to e.g. `srv*C:\symbols*https://msdl.microsoft.com/download/symbols` |

`WINDBG_SDK` environment variable must point to the Debugging Tools directory, e.g.:
```
set WINDBG_SDK=C:\Program Files (x86)\Windows Kits\10\Debuggers\x64
```

---

## License

MIT
