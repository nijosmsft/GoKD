# GoKD

Go library for Windows kernel- and user-mode debugging, built on the Windows DbgEng engine
(`dbgeng.dll`) — the same engine that powers `kd.exe`, `cdb.exe`, and `windbg.exe`.

GoKD wraps DbgEng's COM interfaces and DbgHelp's symbol APIs in a flat C shim and exposes
them as an idiomatic Go API. No text parsing; structured Go types everywhere. Single
process, no IPC — the C++ shim is statically linked into your Go binary via CGo.

It also ships an **MCP server** (`cmd/gokd-mcp/`) so AI clients (Copilot CLI,
Claude Desktop, Cursor, etc.) can drive a real DbgEng session through 33
structured tools — locally, over TCP, or proxied to a remote lab machine.

See [PLAN.md](PLAN.md) for the original design document and [CLAUDE.md](CLAUDE.md) for
contributor / agent guidance.

---

## Install (TL;DR)

You have two choices:

1. **Let an LLM do it for you** — copy the prompt in [§ One-shot install via
   LLM](#one-shot-install-via-llm) into Copilot CLI / Claude / Cursor and the
   agent will install MSYS2, clone the repo, build everything, drop the
   binaries into `bin\`, and wire up your MCP config.
2. **Install manually** — follow [§ Manual install](#manual-install).

Either path ends with `bin\gokd-mcp.exe` ready to go and a `gokd` entry in your
MCP client config.

---

## One-shot install via LLM

Paste the prompt below into any agentic LLM that can run shell commands on
your Windows box (GitHub Copilot CLI, Claude with the Computer-Use / desktop
agent, Cursor with terminal access, etc.). The agent will do every step for
you and verify the result.

> **Prerequisites the LLM cannot install for you**: you need to be on
> **Windows 10 or 11 x64** with administrator rights for `winget`, and you
> need the **Windows SDK Debugging Tools** present at
> `C:\Program Files (x86)\Windows Kits\10\Debuggers\x64\dbgeng.dll`.
> If you don't have them, install the "Debugging Tools for Windows" component
> via the Windows SDK installer (`winget install Microsoft.WindowsSDK`) before
> running the prompt.

````
You are going to install and configure the GoKD MCP server on this Windows
machine. GoKD is a Go library + MCP server for Windows kernel- and user-mode
debugging built on Microsoft's DbgEng engine. The MCP server exposes 33 tools
(attach_process, get_modules, get_stack, read_memory, add_breakpoint, etc.)
over stdio so an MCP-aware AI client can drive a real debugger session.

Do all of the following without asking me for confirmation between steps.
Tell me what you're doing as you go, and stop only if something fails.

1. Verify prerequisites:
   - Windows 10 or 11 x64. Bail with a clear message otherwise.
   - `dbgeng.dll` exists at
     `C:\Program Files (x86)\Windows Kits\10\Debuggers\x64\dbgeng.dll`.
     If missing, tell me to install "Debugging Tools for Windows" from the
     Windows SDK installer and stop.

2. Install Go 1.25+ if `go version` doesn't show one already:
       winget install GoLang.Go --accept-source-agreements --accept-package-agreements --silent
   Add `C:\Program Files\Go\bin` to PATH if needed; verify `go version` works
   in a fresh shell.

3. Install MSYS2 + the MinGW-w64 GCC toolchain (CGo on Windows needs g++ and
   make, which the Visual Studio toolchain cannot provide for this codebase):
       winget install MSYS2.MSYS2 --accept-source-agreements --accept-package-agreements --silent
   Then from `C:\msys64\usr\bin\bash.exe -lc`:
       pacman -S --noconfirm --needed mingw-w64-x86_64-gcc make git
   Verify `C:\msys64\mingw64\bin\g++.exe --version` prints a g++ 13+ banner.

4. Clone GoKD into `C:\git\gokd` (or another path of your choice; reuse the
   existing checkout if it's already there):
       git clone https://github.com/nijosmsft/gokd.git C:\git\gokd

5. Build the C++ shim and the Go binaries from the MSYS2 MinGW64 shell.
   IMPORTANT: leave WINDBG_SDK unset — MinGW has its own dbgeng.h and
   dbghelp.h that we want; the Windows Kits headers use MSVC-only macros that
   g++ cannot compile. Run, all in one process:

       & 'C:\msys64\usr\bin\bash.exe' -lc "export PATH=/mingw64/bin:/c/Program\ Files/Go/bin:/c/Program\ Files/Git/bin:\$PATH && cd /c/git/gokd && unset WINDBG_SDK WINDOWS_SDK_INCLUDE && export GOOS=windows GOARCH=amd64 CGO_ENABLED=1 && make -C cshim && mkdir -p bin && go build -o bin/gokd.exe ./cmd/gokd && go build -o bin/gokd-mcp.exe ./cmd/gokd-mcp"

   The first build takes ~2 minutes. Confirm `C:\git\gokd\bin\gokd-mcp.exe`
   exists and is ~15 MB.

6. Smoke-test the engine. From an ordinary PowerShell (not MSYS2):
       C:\git\gokd\bin\gokd-mcp.exe -help
   Expect a usage banner that mentions `-attach`, `-create`, `-dump`,
   `-listen`, `-remote`, `-symbols`, `-log`.

7. Register the MCP server in the user's MCP client. Detect which clients are
   present and add a `gokd` entry to each found config file. The entry is:

       {
         "command": "C:\\git\\gokd\\bin\\gokd-mcp.exe",
         "args": ["-log", "C:\\git\\gokd\\gokd-mcp.log"]
       }

   Common config locations on Windows (create the file if missing, otherwise
   merge under the existing `mcpServers` object):
     - Copilot CLI:    %USERPROFILE%\.copilot\mcp-config.json
     - Claude Desktop: %APPDATA%\Claude\claude_desktop_config.json
     - Cursor:         %USERPROFILE%\.cursor\mcp.json

   Preserve any existing entries; do not overwrite them.

8. Tell me:
   - Which clients you updated and the path you wrote to.
   - That I need to restart the MCP client for the new server to load.
   - That after restart I can verify with a tool call like:
       attach_process pid=<some notepad pid>
       get_modules
       detach
   - The MCP server log is at C:\git\gokd\gokd-mcp.log.

If any step fails, dump the full output of the failing command and stop —
don't try to fix builds by editing source files in this repo without my
permission.
````

After the agent finishes, restart your MCP client and you should see the
`gokd` server with 33 tools available.

---

## Manual install

### Prerequisites

| Requirement | Notes |
|---|---|
| Windows 10/11 x64 | Build host and debug target |
| Go 1.25+ | `go.mod` minimum; required by the MCP SDK |
| MSYS2 + MinGW-w64 | CGo on Windows needs a GCC toolchain |
| Windows SDK Debugging Tools | Provides `dbgeng.dll` with KDNET transports |
| `_NT_SYMBOL_PATH` (optional) | e.g. `srv*C:\symbols*https://msdl.microsoft.com/download/symbols` |

### Toolchain

```powershell
# 1. Install Go (~5 min, ~400 MB).
winget install GoLang.Go --accept-source-agreements --accept-package-agreements --silent

# 2. Install MSYS2 (~5 min, ~1 GB).
winget install MSYS2.MSYS2 --accept-source-agreements --accept-package-agreements --silent

# 3. From the MSYS2 MinGW64 shell (C:\msys64\usr\bin\bash.exe -l):
pacman -S --noconfirm --needed mingw-w64-x86_64-gcc make git
```

Verify:

```powershell
go version                                # expect go1.25+
C:\msys64\mingw64\bin\g++.exe --version   # expect g++ 13.x+
C:\msys64\usr\bin\make.exe --version      # expect GNU Make 4.x
```

Install the Windows SDK Debugging Tools if you haven't already — they ship with the
Windows SDK installer. By default they land at:

```
C:\Program Files (x86)\Windows Kits\10\Debuggers\x64\dbgeng.dll
```

### Clone + build

```bash
git clone https://github.com/nijosmsft/gokd.git C:\git\gokd

# Open the MSYS2 MinGW64 shell (NOT the plain MSYS shell)
cd /c/git/gokd

# Step 1: build the C++ shim into a static library.
# IMPORTANT: leave WINDBG_SDK unset. MinGW ships its own dbgeng.h / dbghelp.h
# under /mingw64/include — and those are the ones we want. The Windows Kits
# headers use MSVC-only MIDL_INTERFACE macros and pull in minidumpapiset.h,
# which won't compile with g++.
unset WINDBG_SDK WINDOWS_SDK_INCLUDE
make -C cshim                              # produces cshim/lib/libgokd_shim.a

# Step 2: build / test the Go code (CGo links the static lib).
go build ./...
go build -o bin/gokd.exe     ./cmd/gokd
go build -o bin/gokd-mcp.exe ./cmd/gokd-mcp
go test -v -run TestSessionCreateClose .   # smallest smoke test
```

A successful smoke test prints:

```
[gokd] loaded dbgeng.dll from: C:\Program Files (x86)\Windows Kits\10\Debuggers\x64\dbgeng.dll
--- PASS: TestSessionCreateClose
```

### Register with your MCP client

Add a `gokd` entry to your client's config:

| Client | Config path |
|---|---|
| Copilot CLI | `%USERPROFILE%\.copilot\mcp-config.json` |
| Claude Desktop | `%APPDATA%\Claude\claude_desktop_config.json` |
| Cursor | `%USERPROFILE%\.cursor\mcp.json` |

```json
{
  "mcpServers": {
    "gokd": {
      "command": "C:\\git\\gokd\\bin\\gokd-mcp.exe",
      "args": ["-log", "C:\\git\\gokd\\gokd-mcp.log"]
    }
  }
}
```

Restart the client. The 33 GoKD tools should now be available.

---

## Capabilities

| Target type | Status |
|---|---|
| User-mode process (attach / create-process / detach) | Working |
| Kernel-mode via KDNET / serial / USB | Working (KDNET verified) |
| Crash dump (`.dmp`, minidump) | Working |
| Remote debugging via `dbgsrv.exe` | Working |
| Remote debugging via lablink agent (`-remote NODE`) | Working |

Supported operations: memory read/write (virtual + physical), registers, stack walking
with symbols, threads, modules, type info (DbgHelp PDB walking), breakpoints, disassembly,
async event + output channels, `context.Context`-based cancellation.

## Quick start (library)

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

GoKD ships a stateful MCP server at `cmd/gokd-mcp/`. It exposes the public
`gokd.Session` API as **33 tools** that an MCP host can call to attach/open a
target and then inspect modules, threads, registers, stack frames, memory,
symbols, types, breakpoints, execution control, and raw DbgEng commands.

If you followed the [install](#install-tldr) section above, the server is
already built and registered. This section covers the tool catalogue, the
non-default transport modes (`-listen`, `-remote`), and operational details.

### Tool catalogue (33 tools)

| Category | Tools |
|---|---|
| Target lifecycle | `attach_process`, `create_process`, `open_dump`, `attach_kernel`, `detach`, `connect_remote`, `disconnect_remote` |
| Modules & symbols | `get_modules`, `get_type_fields`, `get_type_size`, `addr_to_name`, `name_to_addr` |
| Threads & registers | `get_threads`, `set_thread`, `get_registers`, `set_register`, `get_stack` |
| Memory | `read_memory`, `write_memory`, `read_physical` |
| Breakpoints | `add_breakpoint`, `remove_breakpoint`, `enable_breakpoint`, `list_breakpoints` |
| Execution control | `go_execution`, `step_in`, `step_over`, `step_out`, `break_in` |
| Code inspection | `disassemble` |
| Engine | `get_symbol_path`, `set_symbol_path`, `execute_raw` (raw DbgEng escape hatch) |

The server uses stdout for JSON-RPC only. Engine output and optional MCP
logging go to stderr, or to the path passed with `-log`.

Pass `-symbols PATH` to override the default symbol path (Microsoft public
symbols + per-user cache via `WithDefaultSymbols`).

A self-contained end-to-end test exists at `cmd/gokd-mcp/e2e_test.go` (under
the `manual` build tag) that spawns the server, attaches to a real
`notepad.exe`, and walks through `get_modules`, `get_threads`,
`get_registers`, `get_stack`, and `detach`:

```bash
go test -tags manual -v -run TestMCPEndToEnd ./cmd/gokd-mcp/
```

### Transport modes

`gokd-mcp` speaks the same 33 tools regardless of how it's invoked. The
flags decide where the engine actually runs:

| Flag | Engine runs on | Use when |
|---|---|---|
| (default) | This box, stdio | Copilot / Claude Desktop, default config |
| `-listen ADDR` | This box, TCP | You want to attach a client manually, or be the target of a `-remote` tunnel |
| `-remote NODE` | Remote lablink node, stdio proxy | You want to debug something a remote node can see — kernel KDNET, a target the local box can't reach, or simply to keep DbgEng off the operator's workstation |

#### `-listen` mode

Serves MCP over TCP instead of stdio. One client at a time (DbgEng is
single-session by design).

```powershell
bin\gokd-mcp.exe -listen 127.0.0.1:8765 -log gokd-mcp.log
```

Then any MCP client that speaks newline-delimited JSON-RPC over a socket can
connect (`net.Dial` + `mcp.IOTransport` in Go, equivalent in any other
language). `cmd/gokd-mcp/listen_test.go` has a complete worked example
(attaches to a freshly-spawned notepad, lists modules, detaches; ~1.5 s).

#### `-remote NODE` mode

Spawns no DbgEng locally. Instead:

1. Resolves `NODE` against the same `~/.lablink/nodes.json` registry that
   `LabLinkServer` uses.
2. Deploys `gokd-mcp.exe` (and any DLLs sitting next to it) to `C:\gokd\` on
   the node, skipping files whose SHA256 already matches.
3. Kills any stale `gokd-mcp.exe` on the node, then starts a fresh one with
   `-listen 127.0.0.1:8765`.
4. Opens a bidirectional `Forward` gRPC stream to that port via the node's
   `LabLinkAgent`.
5. Byte-shuttles stdin ↔ stream ↔ stdout. The Copilot host sees a normal stdio
   MCP server; the engine runs on the remote node.

Required env vars (same set as `LabLinkServer`):

| Variable | Notes |
|---|---|
| `LABLINK_AGENT_TOKEN_FILE` or `LABLINK_AGENT_TOKEN` | Shared auth token |
| `LABLINK_TRANSPORT` | `mtls` (default in current lab) |
| `LABLINK_TLS_CA`, `LABLINK_TLS_CERT`, `LABLINK_TLS_KEY` | mTLS material |
| `LABLINK_NODES` or `LABLINK_HOME` | Registry path; falls back to `~/.lablink/nodes.json` |

Example Copilot config that gets you a single `gokd` MCP server backed by a
remote node:

```json
{
  "mcpServers": {
    "gokd": {
      "command": "C:\\git\\gokd\\bin\\gokd-mcp.exe",
      "args": ["-remote", "RR1N4406-25", "-log", "C:\\git\\gokd\\gokd-mcp-remote.log"],
      "env": {
        "LABLINK_AGENT_TOKEN_FILE": "C:\\Users\\you\\.lablink\\agent.token",
        "LABLINK_TRANSPORT": "mtls",
        "LABLINK_TLS_CA":   "C:\\Users\\you\\.lablink\\pki\\ca-bundle\\ca.crt",
        "LABLINK_TLS_CERT": "C:\\Users\\you\\.lablink\\pki\\clients\\default\\client.crt",
        "LABLINK_TLS_KEY":  "C:\\Users\\you\\.lablink\\pki\\clients\\default\\client.key"
      }
    }
  }
}
```

The remote node must already be running `LabLinkAgent` ≥ the version that
ships the `Forward` RPC (from `github.com/nijosmsft/lablink` head, May 2026
or later). Older agents will appear to deploy successfully but the engine
will never come up — the proxy will time out with
`remote engine did not come up within 15s`.

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
go.mod                   Module: github.com/nijosmsft/gokd, go 1.25

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
cmd/gokd-mcp/            MCP server (33 tools) — stdio, -listen, -remote
scripts/                 Manual smoke drivers (manual build tag)

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
