# Security policy

## Reporting a vulnerability

Please report security issues privately by email to:

<!-- TODO(maintainer): replace this placeholder with a real reporting address
     (or enable GitHub Security Advisories on the repo and link to the
     "Report a vulnerability" page here). -->

**`security@<placeholder>`**

Please do **not** open a public GitHub issue for security reports.

When reporting, include:

- A description of the vulnerability and its impact.
- Steps to reproduce, or a minimal proof of concept.
- The version (commit SHA or release tag) you tested against.
- Which **transport mode** of `gokd-mcp` is affected, if applicable:
  - `stdio` (default) — local in-process MCP server.
  - `-listen ADDR` — TCP newline-delimited JSON-RPC server.
  - `-remote NODE` — proxy mode (lablink-resolved remote node).

We will acknowledge receipt within 5 business days and aim to provide a
remediation timeline within 15 business days.

## Supported versions

| Version | Status                                       |
| ------- | -------------------------------------------- |
| `main`  | Active development; security fixes accepted. |

<!-- TODO(maintainer): once v0.1.0 is tagged, add a row like:
     | `v0.x`  | Latest tagged release; security fixes backported. |
-->

## Threat model notes

GoKD is a debugger library and MCP server that ships sensitive
capabilities. Reviewers should be aware that:

- **Kernel debugging.** GoKD can attach to a running Windows kernel over
  KDNET. A misused or compromised session can read and modify kernel
  memory, registers, and breakpoints on the target.
- **Destructive operations exposed over MCP.** The `cmd/gokd-mcp` server
  exposes tools that write memory, set breakpoints, run the target,
  detach, and execute arbitrary debugger commands. A malicious MCP
  client (or anyone who can reach a `-listen` socket) effectively has
  full debugger control of whatever target the session is attached to.
- **No built-in authentication on `-listen`.** The TCP listen mode does
  not authenticate clients. Bind only to `127.0.0.1` or to interfaces
  you control, and treat the socket as equivalent to a debugger console.
- **Symbol-server traffic.** When DbgEng resolves symbols it may contact
  symbol servers configured by `_NT_SYMBOL_PATH`. Be aware of where
  symbol requests go.

Reports that exploit any of the above are in scope.
