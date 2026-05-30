# Contributing to GoKD

Thanks for your interest in GoKD! This document covers what you need to know
to build, modify, test, and submit changes.

## Build prerequisites

GoKD is a Windows-only library that wraps the Microsoft DbgEng debugger
engine. You will need:

- Windows 10/11 x64.
- [MSYS2](https://www.msys2.org/) with the MinGW-w64 toolchain
  (`mingw-w64-x86_64-gcc`, `make`). All build commands run inside the
  **MSYS2 MinGW64** shell.
- Go 1.25 or newer.
- The Windows SDK **Debugging Tools** component (provides `dbgeng.dll`,
  `dbghelp.dll`, KDNET transport DLLs, etc.).

See the "Manual install" section of [`README.md`](./README.md) for full
setup, environment variables, and verification steps.

### The `WINDBG_SDK` rule

**Critical:** before building the C++ shim, leave `WINDBG_SDK` and
`WINDOWS_SDK_INCLUDE` unset. The MinGW-w64 headers under
`/mingw64/include` ship the `__CRT_UUID_DECL` annotations g++ needs for
`__uuidof`. The Windows Kits headers under
`C:\Program Files (x86)\Windows Kits\10\Debuggers\inc` use MSVC-only
`MIDL_INTERFACE` macros and will NOT compile under g++.

```bash
unset WINDBG_SDK WINDOWS_SDK_INCLUDE
make -C cshim
go build ./...
```

## Architecture

GoKD is a three-layer stack:

```
gokd.go (top-level, public Session interface)
  ↓
internal/dbgcgo/  (CGo bindings, dispatch goroutine, OS-thread lock)
  ↓
cshim/  (C++ shim → flat C API; wraps IDebugClient5 / Control4 / Symbols3 / …)
  ↓
dbgeng.dll + dbghelp.dll (loaded dynamically at runtime)
```

See [`CLAUDE.md`](./CLAUDE.md) for the full spec including the DbgEng
thread-affinity rules, cancellation model, and dynamic-DLL-load search
order.

## Adding a new shim function

GoKD has a strict recipe for surfacing new DbgEng functionality. Touch
each layer in order:

1. **Declare the flat C entry point** in `cshim/gokd_shim.h`.
2. **Implement it** in `cshim/gokd_shim.cpp`. Use the existing
   `gokd_session*` handle pattern; convert UTF-16↔UTF-8 with the helpers
   in `cshim/gokd_internal.h`. Return `int32_t` HRESULTs only.
3. **Add a CGo wrapper** in `internal/dbgcgo/dbgeng.go`. Post the call
   through `Session.exec()` so it lands on the dispatch thread.
4. **Expose a public method on `Session`** in `gokd.go` at the repo
   root, including a Go-doc comment and any wrapper types needed.
5. **Wire it as an MCP tool** in `cmd/gokd-mcp/tools.go` and add the
   format helper in `cmd/gokd-mcp/format.go` if it returns structured
   data.

Public types live in `gokd.go`. Types in `internal/dbgcgo` are mirrors —
keep both in sync. See the "Conventions" section of `CLAUDE.md`.

## Tests

- **User-mode tests** run locally. They attach to `notepad.exe` and need
  no special setup.

  ```bash
  go test -v -timeout 5m -run "TestSessionCreateClose|TestAttachDetach|TestModules" .
  ```

- **Kernel tests** are gated on the `KDNET_CONN` environment variable
  and require a live KDNET target VM in debug mode. They skip
  automatically when the variable is unset. **Do not** add new tests
  that require kernel debugging on the local box — the operator's
  workstation should never run the KDNET transport directly. Kernel
  debugging is exercised via `gokd-mcp -remote NODE` against a lab
  machine.

- **MCP server tests** are gated behind the `manual` build tag:

  ```bash
  go test -tags manual -v -timeout 5m -run TestMCPEndToEnd ./cmd/gokd-mcp/
  ```

## PR checklist

Before opening a pull request, please confirm:

- [ ] `make -C cshim` builds cleanly.
- [ ] `go vet ./...` is clean.
- [ ] `gofmt -l .` is empty.
- [ ] User-mode tests pass (`go test -run "TestSessionCreateClose|TestAttachDetach|TestModules" .`).
- [ ] Documentation (`README.md`, `CLAUDE.md`, MCP tool docs) is updated
      to match new behaviour.
- [ ] No `dbgeng.dll` or other SDK redistributables were committed.

## Commit messages

Use short imperative subject lines. Group related changes per commit.

When AI assistance (Copilot, Claude, etc.) materially contributed to a
change, include a `Co-authored-by:` trailer at the end of the commit
message:

```
ci, release: add release workflow on tag push

Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>
```

See GitHub's documentation on
[creating a commit with multiple authors](https://docs.github.com/en/pull-requests/committing-changes-to-your-project/creating-and-editing-commits/creating-a-commit-with-multiple-authors)
for the exact trailer format.

There is no `Signed-off-by` requirement in this repository.
