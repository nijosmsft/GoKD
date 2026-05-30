# GoKD

Go library for Windows kernel- and user-mode debugging, built on the Windows DbgEng engine
(`dbgeng.dll`) — the same engine that powers `kd.exe`, `cdb.exe`, and `windbg.exe`.

GoKD wraps DbgEng's COM interfaces and DbgHelp's symbol APIs in a flat C shim and exposes
them as an idiomatic Go API. No text parsing; structured Go types everywhere. Single
process, no IPC — the C++ shim is statically linked into your Go binary via CGo.

See [PLAN.md](PLAN.md) for the original design document and [CLAUDE.md](CLAUDE.md) for
contributor / agent guidance.

## Capabilities

| Target type | Status |
|---|---|
| User-mode process (attach / create-process / detach) | Working |
| Kernel-mode via KDNET / serial / USB | Working (KDNET verified) |
| Crash dump (`.dmp`, minidump) | Working |
| Remote debugging via `dbgsrv.exe` | Working |

Supported operations: memory read/write (virtual + physical), registers, stack walking
with symbols, threads, modules, type info (DbgHelp PDB walking), breakpoints, disassembly,
async event + output channels, `context.Context`-based cancellation.

## Prerequisites

| Requirement | Notes |
|---|---|
| Windows 10/11 x64 | Build host and debug target |
| Go 1.25+ | `go.mod` minimum; required by the MCP SDK |
| MSYS2 + MinGW-w64 | CGo on Windows needs a GCC toolchain |
| Windows SDK Debugging Tools | Provides `dbgeng.dll` with KDNET transports |
| `_NT_SYMBOL_PATH` (optional) | e.g. `srv*C:\symbols*https://msdl.microsoft.com/download/symbols` |

## Install the toolchain

```powershell
# 1. Install MSYS2 (provides MinGW-w64). ~1 GB, ~5 min.
winget install MSYS2.MSYS2 --accept-source-agreements --accept-package-agreements --silent

# 2. From the MSYS2 MinGW64 shell (C:\msys64\usr\bin\bash.exe -l), install the toolchain:
pacman -S --noconfirm --needed mingw-w64-x86_64-gcc make
```

Verify:

```powershell
C:\msys64\mingw64\bin\g++.exe --version   # expect g++ 16.x+
C:\msys64\usr\bin\make.exe --version      # expect GNU Make 4.x
```

Install the Windows SDK Debugging Tools if you haven't already — they ship with the
Windows SDK installer. By default they land at:

```
C:\Program Files (x86)\Windows Kits\10\Debuggers\x64\dbgeng.dll
```

## Build

```bash
# Open the MSYS2 MinGW64 shell (NOT the plain MSYS shell)
cd /c/path/to/gokd

# Step 1: build the C++ shim into a static library.
# IMPORTANT: leave WINDBG_SDK unset. MinGW ships its own dbgeng.h / dbghelp.h
# under /mingw64/include — and those are the ones we want. The Windows Kits
# headers use MSVC-only MIDL_INTERFACE macros and pull in minidumpapiset.h,
# which won't compile with g++.
unset WINDBG_SDK WINDOWS_SDK_INCLUDE
make -C cshim                              # produces cshim/lib/libgokd_shim.a

# Step 2: build / test the Go code (CGo links the static lib).
go build ./...
go test -v -run TestSessionCreateClose .   # smallest smoke test
```

A successful smoke test prints:

```
[gokd] loaded dbgeng.dll from: C:\Program Files (x86)\Windows Kits\10\Debuggers\x64\dbgeng.dll
--- PASS: TestSessionCreateClose
```

## Quick start

```go
package main

import (
    "fmt"
    "log"

    "github.com/nijosmsft/gokd"
)

func main() {
    sess, err := gokd.New(gokd.WithDefaultSymbols())
    if err != nil {
        log.Fatal(err)
    }
    defer sess.Close()

    if err := sess.AttachProcess(1234, gokd.AttachDefault); err != nil {
        log.Fatal(err)
    }
    defer sess.Detach()

    stack, _ := sess.Stack()
    for _, f := range stack {
        fmt.Printf("%s!%s+0x%x\n", f.Module, f.Function, f.Displacement)
    }
}
```

### Symbol path

The DbgEng symbol path drives every `lm`-style listing, stack unwind, and `?` expression.
`gokd.New` accepts functional options to configure it:

* `gokd.WithSymbolPath(path)` — set an explicit path (e.g. an internal `\\symsrv\share`).
* `gokd.WithDefaultSymbols()` — install the Microsoft public symbol server plus a
  per-user cache (under `os.UserCacheDir()\gokd\symbols`) **only if** DbgEng's path is
  empty after session creation. So a user's `_NT_SYMBOL_PATH` always wins.
* No option — leave whatever DbgEng picked up at startup untouched (typically the
  `_NT_SYMBOL_PATH` env var, or empty).

The bundled `cmd/gokd` REPL and `cmd/gokd-mcp` server both default to
`WithDefaultSymbols()`; pass `-symbols PATH` to override.

## Interactive CLI

A small reference debugger ships with the library at `cmd/gokd/`. Build it the same
way as the library, then point it at any target:

```bash
go build -o bin/gokd.exe ./cmd/gokd

bin\gokd.exe -pid 1234                              # attach to a running process
bin\gokd.exe -exec "C:\Windows\notepad.exe"         # spawn and attach with initial break
bin\gokd.exe -dump C:\dumps\bsod.dmp                # open a crash dump
bin\gokd.exe -kernel net:port=50000,key=1.2.3.4     # KDNET kernel attach
bin\gokd.exe -pid 1234 -c "lm;k;r rip rsp;detach"   # non-interactive script
```

With no target flag, `gokd.exe` drops into an unattached REPL. Inside the REPL:

```
> ?
Commands:
  attach <pid>        attach to process
  bp <addr|symbol>    set breakpoint
  g | t | p | gu      go / step in / step over / step out
  k                   stack
  r [regs...]         registers
  dq|dd|db <addr> [count]
  lm                  modules
  u <addr> [count]    disassemble
  dt <module>!<type>  type fields
  !<cmd>              raw dbgeng command (escape hatch)
  q                   quit
```

`Ctrl+C` during execution issues a break-in; press it twice within two seconds to exit.

## MCP server

GoKD also ships a stateful MCP server at `cmd/gokd-mcp/`. It exposes the public
`gokd.Session` API as tools over stdio, so an MCP host can attach/open a target and then
inspect modules, threads, registers, stack frames, memory, symbols, types, breakpoints,
execution control, and raw DbgEng commands.

Build it from the MSYS2 MinGW64 shell the same way as the CLI:

```bash
unset WINDBG_SDK WINDOWS_SDK_INCLUDE
go build -o bin/gokd-mcp.exe ./cmd/gokd-mcp
```

Example MCP configuration for Claude Desktop or Copilot CLI:

```json
{
  "mcpServers": {
    "gokd": {
      "command": "C:\\git\\gokd\\bin\\gokd-mcp.exe",
      "args": ["-symbols", "srv*C:\\symbols*https://msdl.microsoft.com/download/symbols"]
    }
  }
}
```

The server uses stdout for JSON-RPC only. Engine output and optional MCP logging go to
stderr, or to the path passed with `-log`.

## Running tests

`go test ./...` runs the user-mode tests against a freshly-spawned `notepad.exe`.

- `TestSessionCreateClose` — no target needed.
- `TestAttachDetach`, `TestModules`, `TestRegisters`, `TestStack`, `TestReadMemory`,
  `TestThreads`, `TestSymbolResolution` — all attach to notepad. Work on any
  desktop Windows. **Will fail on Windows Server Core / a server with no interactive
  session** because notepad needs a window station to start.
- `TestBreakpointAndGo` — same caveat (uses `CreateProcess` to launch notepad under the
  debugger).
- `TestKernelAttachKDNET` — has a hardcoded KDNET connection string; will fail unless
  that exact target is up and responding.

## Deploying gokd binaries to another machine

A built `gokd` consumer is a self-contained `.exe` that depends on:

| DLL | Source | Why |
|---|---|---|
| `dbgeng.dll` | Windows SDK Debugging Tools | Loaded dynamically by the shim |
| `dbghelp.dll` | Windows SDK Debugging Tools | PDB / symbol walking |
| `dbgcore.dll` | Windows SDK Debugging Tools | dbgeng transitive |
| `symsrv.dll` + `symsrv.yes` | Windows SDK Debugging Tools | Symbol server |
| `libstdc++-6.dll` | MSYS2 MinGW (`C:\msys64\mingw64\bin\`) | C++ runtime |
| `libgcc_s_seh-1.dll` | Same | libstdc++ dep |
| `libwinpthread-1.dll` | Same | libstdc++ dep |

Place all of them next to the `.exe`. The shim will find `dbgeng.dll` via its search
order (`GOKD_DBGENG_PATH` env var → standard SDK paths → exe directory). For
predictability set the env var:

```powershell
$env:GOKD_DBGENG_PATH = "C:\path\to\dbgeng.dll"
```

The **system** `dbgeng.dll` lacks KDNET transport support; you must ship the SDK
Debugging Tools version for kernel debugging to work.

## Kernel debug — connection-string caveat

DbgEng accepts:

```
net:port=N,key=W.X.Y.Z
```

Do **not** include `target=...` in the connection string. The `target=` parameter is
documented as the "VM host machine name" for indirect debugging through `kdsrv`. Putting
an IP there silently prevents DbgEng from opening the UDP listener, and the wait will
time out even though `AttachKernel` returns `S_OK`. The target IP (which the host
connects *to*) is implicit — once the host listens on `port=N`, the target's KDNET stack
sends probes to whatever address it was given via `bcdedit /dbgsettings net hostip:...`
on the target.

Configure the target with:

```cmd
bcdedit /dbgsettings net hostip:<host-ip> port:50000 key:1.2.3.4
bcdedit /debug on
shutdown /r /t 0
```

Then from the host side, attach with:

```go
ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
defer cancel()
err := sess.AttachKernel(ctx, "net:port=50000,key=1.2.3.4", gokd.KernelDefault)
```

`gokd.KernelDefault` (the recommended option set for programmatic use)
requests an active break-in immediately after the transport opens, so the
first break is deterministic. Pass `gokd.KernelPassive` instead if you want
kd.exe-style "wait for the target to talk first" behaviour — useful when
attaching to a target that's already broken into the debugger.

If the handshake completes but break-in still stalls (KDNET says
`Connected to target` and the active interrupt was issued but nothing
further), the target kernel is reachable but not honouring NMI break —
usually VBS / HVCI or a virtual-NIC quirk. Cause a real break-in (a
workload bug check, a manual `Ctrl+ScrLk` on the target console, etc.) to
engage the debugger.

## Repository layout

```
gokd.go                  Public Session interface + impl
gokd_test.go             User-mode tests (notepad target)
gokd_kernel_test.go      KDNET attach test
gokd_diag_test.go        CreateProcess diagnostics
go.mod                   Module: github.com/nijosmsft/gokd, go 1.22

internal/dbgcgo/
  dbgeng.go              CGo wrappers + dispatch goroutine
  callbacks.go           //export Go funcs invoked from C event callbacks

cshim/
  gokd_shim.h            Public C header (only file CGo includes)
  gokd_internal.h        Internal C++ shared header
  gokd_shim.cpp          COM → C implementations
  dispatch_thread.cpp    Dynamic dbgeng.dll load, UTF-8/16 helpers
  callbacks.cpp          IDebugEventCallbacksWide + IDebugOutputCallbacksWide
  Makefile               Builds lib/libgokd_shim.a (MinGW-w64)

cmd/gokd/                Reference interactive debugger (REPL)

PLAN.md, CLAUDE.md, README.md, LICENSE
```

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `dbghelp.h:...: minidumpapiset.h: No such file or directory` | You set `WINDBG_SDK` and are pulling Windows Kits headers. Unset it — use MinGW's headers. |
| `expected primary-expression before '__typeof'` near `__uuidof(...)` | Same root cause: Windows Kits `dbgeng.h` has no `__CRT_UUID_DECL`. Use MinGW's `dbgeng.h`. |
| `gokd_create_session failed` | SDK `dbgeng.dll` not found. Install Windows SDK Debugging Tools, or set `GOKD_DBGENG_PATH`. |
| `AttachKernel` returns `S_OK` but `WaitForEvent` hangs | Either `target=...` is in your connection string (remove it), or the active break-in didn't reach the target. Confirm with `KernelDefault`; if still stalled, see "Kernel debug" above (likely VBS / HVCI on the target). |
| `TestBreakpointAndGo` hangs on a server VM | Notepad can't launch without an interactive desktop. Run user-mode tests on a desktop host instead. |
| Build link errors about missing `dbgeng_*` symbols | You added `-ldbgeng` to LDFLAGS. Don't — the shim loads `dbgeng.dll` dynamically. |

## License

MIT. See [LICENSE](LICENSE).
