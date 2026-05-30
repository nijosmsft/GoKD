package main

import (
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mutatingTools lists tools that can alter target or debugger state. When
// the server is started with --readonly, registerTools skips these so they
// do not appear in tools/list at all (cleaner than runtime rejection).
//
// open_dump and attach_kernel are included because they acquire a target —
// even though the target itself is then read-only — and a hosted readonly
// instance should not silently switch contexts on the operator.
var mutatingTools = map[string]bool{
	"attach_process":             true,
	"create_process":             true,
	"attach_kernel":              true,
	"open_dump":                  true,
	"detach":                     true,
	"connect_remote":             true,
	"disconnect_remote":          true,
	"set_thread":                 true,
	"set_register":               true,
	"write_memory":               true,
	"go_execution":               true,
	"step_in":                    true,
	"step_over":                  true,
	"step_out":                   true,
	"break_in":                   true,
	"add_breakpoint":             true,
	"add_breakpoint_source_line": true,
	"add_data_breakpoint":        true,
	"configure_breakpoint":       true,
	"breakpoint_command":         true,
	"enable_breakpoint":          true,
	"remove_breakpoint":          true,
	"set_radix":                  true,
	"set_expression_syntax":      true,
	"set_symbol_path":            true,
	"reload_symbols":             true,
	"sym_fix":                    true,
	"write_dump":                 true,
	"setup_kernel_debug":         true,
}

// addToolMaybe registers a tool unless the server is in readonly mode and
// the tool is on the mutating denylist. Also auto-populates Tool.Annotations
// with ReadOnlyHint or DestructiveHint based on the denylist so MCP clients
// can render appropriate risk badges. Replaces direct mcp.AddTool calls in
// registerTools.
func addToolMaybe[In, Out any](s *srv, server *mcp.Server, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	if s.readonly && mutatingTools[t.Name] {
		return
	}
	if t.Annotations == nil {
		t.Annotations = annotationsFor(t.Name)
	}
	mcp.AddTool(server, t, h)
}

// annotationsFor returns a default ToolAnnotations for a tool name, derived
// from mutatingTools. Callers may override by setting Tool.Annotations
// before calling addToolMaybe (e.g. execute_raw, which is destructive but
// also opens-world).
func annotationsFor(name string) *mcp.ToolAnnotations {
	destructive := mutatingTools[name]
	a := &mcp.ToolAnnotations{}
	if destructive {
		t := true
		a.DestructiveHint = &t
	} else {
		a.ReadOnlyHint = true
	}
	return a
}

// rawCommandDeny is the regex list applied to execute_raw input in
// --readonly --unsafe-raw mode. Each pattern matches the first whitespace-
// delimited token, case-insensitive, and covers the worst offenders that
// would otherwise sneak past --readonly.
var rawCommandDeny = []*regexp.Regexp{
	// Lifetime / process control
	regexp.MustCompile(`(?i)^\s*q\b`),       // q, qq, qd
	regexp.MustCompile(`(?i)^\s*qq\b`),      // explicit
	regexp.MustCompile(`(?i)^\s*qd\b`),      // quit-detach
	regexp.MustCompile(`(?i)^\s*\.kill\b`),  // .kill
	regexp.MustCompile(`(?i)^\s*\.restart\b`),
	regexp.MustCompile(`(?i)^\s*\.create\b`),
	regexp.MustCompile(`(?i)^\s*\.attach\b`),
	regexp.MustCompile(`(?i)^\s*\.detach\b`),
	regexp.MustCompile(`(?i)^\s*\.shell\b`),

	// Filesystem / module side effects
	regexp.MustCompile(`(?i)^\s*\.dump\b`),
	regexp.MustCompile(`(?i)^\s*\.writemem\b`),
	regexp.MustCompile(`(?i)^\s*\.load(?:by)?\b`),
	regexp.MustCompile(`(?i)^\s*\.unload\b`),
	regexp.MustCompile(`(?i)^\s*\.logopen\b`),

	// Memory writes — eb / ed / ew / eq / ep / ea + bare e
	regexp.MustCompile(`(?i)^\s*e[bdwqpa]?\b`),
	regexp.MustCompile(`(?i)^\s*f\b`),  // f (fill)

	// Execution — g, gh, gu, p, pa, t, ta, wt, tb
	regexp.MustCompile(`(?i)^\s*g[hu]?\b`),
	regexp.MustCompile(`(?i)^\s*p[a]?\b`),
	regexp.MustCompile(`(?i)^\s*t[ab]?\b`),
	regexp.MustCompile(`(?i)^\s*wt\b`),

	// Breakpoints (bp/bm/bu/ba/bc/bd/be — bl is read-only, intentionally allowed)
	regexp.MustCompile(`(?i)^\s*b[pmuacde]\b`),
}

// denyRawCommand returns true if cmd matches any pattern in rawCommandDeny.
func denyRawCommand(cmd string) bool {
	for _, re := range rawCommandDeny {
		if re.MatchString(cmd) {
			return true
		}
	}
	return false
}

// firstToken returns the first whitespace-delimited token of cmd, used in
// error messages so the LLM gets a precise hint about what was rejected.
func firstToken(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if i := strings.IndexAny(cmd, " \t"); i >= 0 {
		return cmd[:i]
	}
	return cmd
}
