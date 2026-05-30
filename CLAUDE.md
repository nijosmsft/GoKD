# CLAUDE.md

Guidance for Claude Code when working in this repository.

## Project

**GoKD** — Go library for Windows kernel- and user-mode debugging, built on the Windows
DbgEng engine (`dbgeng.dll`). Same engine that powers `kd.exe`, `cdb.exe`, `windbg.exe`.

Module path: `github.com/nijosmsft/gokd` (Go 1.22). MIT licensed. Single-process, no IPC —
the C++ shim is statically linked into the Go binary via CGo.

`PLAN.md` is the original design document; `README.md` is a stub. **PLAN.md is aspirational
in places — trust the actual code over PLAN.md when they disagree** (e.g. PLAN.md describes
a `pkg/` directory tree that does not exist; the public API lives in `gokd.go` at the repo
root).

## Architecture (three layers)

```
gokd.go (top-level, public Session interface)
  ↓
internal/dbgcgo/  (CGo bindings, dispatch goroutine, OS-thread lock)
  ↓
cshim/  (C++ shim → flat C API; wraps IDebugClient5/Control4/Symbols3/etc.)
  ↓
dbgeng.dll + dbghelp.dll (loaded dynamically — see below)
```

### Why a C++ shim
CGo cannot call C++ COM vtable methods directly. The shim translates every DbgEng COM
interface into a flat C API (`gokd_*` functions taking `uint64_t` session handles). Only
plain C types cross the CGo boundary — no COM, no `wchar_t`, no `IUnknown*`.

### DbgEng thread affinity
DbgEng requires **all calls (including `WaitForEvent`) to come from the thread that called
`DebugCreate`**. `internal/dbgcgo.NewSession()` starts a goroutine, pins it with
`runtime.LockOSThread()`, calls `gokd_create_session()` there, then runs a dispatch loop
that executes commands sequentially. Public API methods post closures to `cmdCh` and block
on the result.

### Cancellation
Long-running `WaitForEvent` calls (kernel attach, `Go()`) are cancelled via
`gokd_cancel_wait()` which sets a `volatile int` flag the dispatch thread polls. Public API
wires this to `context.Context` — see `session.execWithCancel` in `gokd.go`.

### Dynamic dbgeng.dll loading
**Critical**: the shim does NOT link `dbgeng.lib` statically. It calls `LoadLibraryA` at
runtime with an explicit path to the Windows SDK Debugging Tools version of `dbgeng.dll`.
Search order: `GOKD_DBGENG_PATH` env var → `C:\Program Files (x86)\Windows Kits\10\Debuggers\x64\dbgeng.dll`
→ same dir as exe → standard DLL search. **The system `dbgeng.dll` lacks KDNET transport
DLLs**, so kernel debugging only works against the SDK copy. See
`cshim/dispatch_thread.cpp` `load_dbgeng()`.

## Layout

```
gokd.go                       Public Session interface + impl (top-level package "gokd")
gokd_test.go                  User-mode tests (attaches to notepad.exe)
gokd_kernel_test.go           Live KDNET kernel attach test
gokd_diag_test.go             CreateProcess diagnostics
go.mod                        Module: github.com/nijosmsft/gokd, go 1.22

internal/dbgcgo/
  dbgeng.go                   CGo wrappers + dispatch goroutine + Session struct
  callbacks.go                //export Go functions invoked from C event/output callbacks

cshim/
  gokd_shim.h                 Public C header — the ONLY file CGo includes
  gokd_internal.h             Internal C++ shared header (gokd_session struct, helpers)
  gokd_shim.cpp               (~1225 lines) COM → C implementations
  dispatch_thread.cpp         (~250 lines) Dynamic dbgeng.dll load, UTF-8/16 conversion
  callbacks.cpp               (~330 lines) IDebugEventCallbacksWide + IDebugOutputCallbacksWide
  Makefile                    Builds lib/libgokd_shim.a with MinGW-w64
  test_dbgeng*.cpp            Standalone C++ scratch tests (compile separately, not in lib)
  test_kernel*.cpp            Standalone kernel-attach C++ probes (see commit 039f87c)
  test_which_dll.cpp          Diagnostic: which dbgeng.dll got loaded

cmd/gokd/                     Reference interactive debugger (REPL); stdlib-only
  main.go                     Flag parsing, session setup, signal handler
  repl.go                     REPL loop, async output/event drainers
  commands.go                 Command handlers (bp, g, k, r, lm, dt, u, ...)
  format.go                   Hexdump, stack and register formatting
  parse.go                    parseAddr (hex/sym/0x/0d), parseCount (L-prefix)

PLAN.md, README.md, LICENSE
```

## Build

```bash
# Inside MSYS2 MinGW64 shell (CGo requires MinGW-w64 toolchain on Windows).
# CRITICAL: leave WINDBG_SDK unset. Use MinGW's own dbgeng.h / dbghelp.h
# (under /mingw64/include) — they ship __CRT_UUID_DECL annotations that make
# __uuidof work with g++. The Windows Kits headers under
# C:\Program Files (x86)\Windows Kits\10\Debuggers\inc use MSVC-only
# MIDL_INTERFACE macros and drag in minidumpapiset.h — will NOT compile.
unset WINDBG_SDK WINDOWS_SDK_INCLUDE
make -C cshim                 # produces cshim/lib/libgokd_shim.a
go build ./...                # CGo links against the static lib
go test ./...                 # runs all tests (some need a live target)
```

CGo directives in `internal/dbgcgo/dbgeng.go`:
```go
#cgo CFLAGS:  -I${SRCDIR}/../../cshim
#cgo LDFLAGS: -L${SRCDIR}/../../cshim/lib -lgokd_shim
#cgo LDFLAGS: -ldbghelp -lole32 -luuid -lstdc++
```
Note: `dbgeng` is NOT in LDFLAGS — it is loaded dynamically by the shim.

`cshim/lib/`, `*.o`, `*.a`, `*.exe`, `*.dll` are git-ignored.

## Deploying built binaries to another machine

CGo statically links `libgokd_shim.a` into the Go binary, but it still depends on:

- SDK Debugging Tools: `dbgeng.dll`, `dbghelp.dll`, `dbgcore.dll`, `symsrv.dll`, `symsrv.yes`
- MinGW runtime: `libstdc++-6.dll`, `libgcc_s_seh-1.dll`, `libwinpthread-1.dll`

Drop all eight files next to the `.exe` and set `GOKD_DBGENG_PATH` to point at the
bundled `dbgeng.dll`. The system `dbgeng.dll` lacks KDNET transport — use the SDK
copy if you need kernel debugging.

## KDNET connection-string gotcha

DbgEng's `net:` connection string accepts a `target=...` parameter — but it is the
**VM host machine name** for indirect `kdsrv`-style routing, NOT the target's IP. Putting
an IP there silently prevents DbgEng from opening the UDP listener; `AttachKernel`
returns `S_OK` and the subsequent `WaitForEvent` hangs forever. The correct form for
direct kernel attach is just:

```
net:port=N,key=W.X.Y.Z
```

## Kernel attach: deterministic break-in

`AttachKernel(ctx, connStr, opts KernelOptions)` takes a `KernelOptions` struct.
`gokd.KernelDefault` (`InitialBreakIn: true`) is the recommended setting for
programmatic use: after the transport opens, the shim calls
`SetInterrupt(DEBUG_INTERRUPT_ACTIVE)` so the engine pushes a break packet to
the target as soon as the link handshakes. Without this, the wait sits forever
on an idle KDNET target (the same way `kd.exe` waits passively until you hit
Ctrl+Break). `gokd.KernelPassive` (`InitialBreakIn: false`) preserves the old
passive behaviour and is appropriate when attaching to a target that is
already broken into the debugger.

The implementation lives at the bottom of `gokd_attach_kernel` in
`cshim/gokd_shim.cpp` (flag `GOKD_KERNEL_INITIAL_BREAK_IN` in `gokd_shim.h`).
The flag bit value is duplicated in `gokd.go` as `kernelFlagInitialBreakIn`;
keep them in sync.

## Implementation status

The README still says "early development" but the code is well past that. Recent commits
indicate working:

- User-mode attach / create-process / detach (`gokd_test.go`)
- Kernel attach over KDNET (`gokd_kernel_test.go`, commit `0d950bf`)
- Crash dump open (`OpenDump`)
- Remote debugging via dbgsrv (`ConnectRemote` / `DisconnectRemote`)
- Memory read/write (virtual + physical), registers, stack, threads, modules, symbols,
  type info (via DbgHelp), breakpoints, disassembly
- Async event + output channels delivered from CGo callbacks
- Context-based cancellation for `Go/StepIn/StepOver/StepOut` and `AttachKernel`

`PLAN.md` Phases 1–4c are largely implemented in the flat layout. The interactive
CLI/REPL lives at `cmd/gokd/` (build: `go build -o bin/gokd.exe ./cmd/gokd`).
Phase 5 (`gokd-mcp` separate repo) is not yet present.

## Conventions

- **All errors are HRESULTs.** The shim returns `int32_t`; CGo wraps with `hresult()` →
  `fmt.Errorf("HRESULT 0x%08x", uint32(hr))`. Don't redefine error wrapping.
- **All strings across the CGo boundary are UTF-8.** The shim converts to/from UTF-16
  internally (`utf8_to_wide`, `wide_to_utf8`). Never pass `wchar_t*` through the API.
- **Count-then-fetch pattern.** Functions returning variable-length arrays (registers,
  modules, threads, type fields, breakpoints) accept `out=NULL` to query the count first.
- **Public types live in `gokd.go`.** The `internal/dbgcgo` types are mirrors — when adding
  a feature, update both layers and the translation in `session.*` methods.
- **DbgEng interfaces in use**: `IDebugClient5`, `IDebugControl4`, `IDebugDataSpaces4`,
  `IDebugSymbols3`, `IDebugRegisters2`, `IDebugSystemObjects4`, `IDebugAdvanced3`. Use the
  `Wide` variants of callbacks (`IDebugEventCallbacksWide`, `IDebugOutputCallbacksWide`).
- **Commits**: Project uses standard messages. No `Signed-off-by` requirement here (that
  is a sibling-repo convention from `C:\git\CLAUDE.md` — does NOT apply to gokd).

## Running tests

`go test ./...` runs user-mode tests against `notepad.exe`. The kernel test
(`TestKernelAttachKDNET`) has a hardcoded KDNET connection string and requires a live
target VM in debug mode — expect it to fail in isolation. Run individual tests with
`go test -run TestAttachDetach -v .`.

Set `GOKD_DBGENG_PATH` if the SDK Debugging Tools are installed in a non-standard location.

## When modifying

- Adding a shim function: header in `cshim/gokd_shim.h` → impl in `cshim/gokd_shim.cpp` →
  CGo wrapper in `internal/dbgcgo/dbgeng.go` → public method on `Session` in `gokd.go`.
- Adding an event type: update `gokd_ev_*_t` in shim header, `IDebugEventCallbacksWide`
  impl in `callbacks.cpp`, `Ev*` Go struct in `internal/dbgcgo/callbacks.go`, public
  `*Event` type in `gokd.go`, and the switch in `(*session).Events()`.
- All shim functions assume they run on the dispatch thread — never call them from a Go
  goroutine without going through `Session.exec()`. Exceptions: `gokd_break_in` and
  `gokd_cancel_wait` are explicitly thread-safe.
