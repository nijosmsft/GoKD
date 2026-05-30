package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nijosmsft/gokd"
)

// MCPError is the structured error envelope returned to MCP clients on
// tool failures. It complements the existing text Content with an
// actionable code, a translated HRESULT (when known), a hint pointing at
// remediation, and a list of recommended follow-up tools.
type MCPError struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	HRESULT   string            `json:"hresult,omitempty"`
	Hint      string            `json:"hint,omitempty"`
	NextTools []string          `json:"next_tools,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
}

// hresultMap translates the most common DbgEng HRESULTs into structured
// codes + actionable hints. Codes outside this map fall back to
// {Code:"HRESULT", HRESULT:"0x..."} so the LLM still sees the raw code.
var hresultMap = map[uint32]struct {
	Code string
	Hint string
}{
	0x80004005: {"TARGET_RUNNING", "Call break_in first or wait for go_execution to return."},
	0x80070006: {"NO_TARGET", "Call attach_process/create_process/open_dump/attach_kernel first."},
	0x80070057: {"INVALID_ARG", "Check the argument; for symbols call name_to_addr first to verify the symbol resolves."},
	0x80070002: {"FILE_NOT_FOUND", "File not found. Verify the path on the debugger host machine."},
	0x800703E5: {"CANCELLED", "Operation cancelled by break_in or context cancellation."},
	0x80004001: {"NOT_IMPL", "Operation not supported on the current target type (e.g. attach_kernel ops on a dump)."},
	0x8000FFFF: {"NO_CURRENT_PROC", "Engine has no current process; call go_execution once after attach to settle the state."},
	0x80000002: {"NOT_FOUND", "No result. Check spelling; try wildcards or a module! prefix."},
}

// opHints is a per-tool table of canonical follow-up tools that the LLM
// should consider after an error. Used to populate MCPError.NextTools.
var opHints = map[string][]string{
	"attach_process":              {"get_session_state", "get_threads", "get_modules"},
	"create_process":              {"get_session_state", "go_execution"},
	"open_dump":                   {"summarise_target", "last_exception", "get_stack"},
	"attach_kernel":               {"get_session_state", "go_execution"},
	"detach":                      {"get_session_state"},
	"connect_remote":              {"attach_process", "attach_kernel"},
	"disconnect_remote":           {"get_session_state"},
	"go_execution":                {"break_in", "get_recent_events"},
	"step_in":                     {"break_in", "get_stack"},
	"step_over":                   {"break_in", "get_stack"},
	"step_out":                    {"break_in", "get_stack"},
	"break_in":                    {"get_session_state"},
	"read_memory":                 {"query_region", "virtual_to_physical"},
	"read_physical":               {"query_region", "virtual_to_physical"},
	"write_memory":                {"query_region"},
	"read_string":                 {"read_memory", "query_region"},
	"dump_memory":                 {"read_memory", "query_region"},
	"name_to_addr":                {"reload_symbols", "sym_fix"},
	"addr_to_name":                {"reload_symbols", "get_modules"},
	"addr_to_line":                {"reload_symbols", "sym_fix"},
	"line_to_addr":                {"reload_symbols", "sym_fix"},
	"add_breakpoint":              {"name_to_addr", "sym_fix"},
	"add_breakpoint_source_line":  {"line_to_addr", "reload_symbols"},
	"add_data_breakpoint":         {"query_region"},
	"configure_breakpoint":        {"list_breakpoints"},
	"breakpoint_command":          {"list_breakpoints"},
	"enable_breakpoint":           {"list_breakpoints"},
	"remove_breakpoint":           {"list_breakpoints"},
	"list_breakpoints":            {"get_session_state"},
	"get_modules":                 {"get_session_state", "reload_symbols"},
	"get_threads":                 {"get_session_state"},
	"set_thread":                  {"get_threads"},
	"get_registers":               {"set_thread"},
	"set_register":                {"get_registers"},
	"get_stack":                   {"get_threads", "break_in"},
	"walk_stacks_all":             {"break_in", "get_threads"},
	"disassemble":                 {"name_to_addr", "addr_to_name"},
	"evaluate":                    {"set_expression_syntax", "reload_symbols"},
	"get_type_fields":             {"reload_symbols", "sym_fix"},
	"get_type_size":               {"reload_symbols", "sym_fix"},
	"dump_type":                   {"get_type_fields", "reload_symbols"},
	"inspect_struct":              {"get_type_fields", "reload_symbols"},
	"reload_symbols":              {"sym_fix", "get_symbol_path"},
	"sym_fix":                     {"reload_symbols"},
	"set_symbol_path":             {"reload_symbols"},
	"search_memory":               {"query_region"},
	"virtual_to_physical":         {"query_region"},
	"query_region":                {"get_modules"},
	"write_dump":                  {"get_session_state"},
	"last_exception":              {"get_stack", "get_registers"},
	"bug_check":                   {"triage_crash", "summarise_target"},
	"triage_crash":                {"last_exception", "get_stack", "get_registers"},
	"summarise_target":            {"get_session_state", "triage_crash"},
	"execute_raw":                 {"get_raw_output_continuation"},
	"get_raw_output_continuation": {"execute_raw"},
	"get_recent_events":           {"get_session_state"},
	"get_recent_output":           {"get_session_state"},
}

// wrapErr converts an arbitrary error from a gokd Session call into a
// structured MCPError. op identifies the calling tool (e.g. "attach_process")
// and is used to look up canonical follow-up tools in opHints.
func wrapErr(op string, err error) *MCPError {
	if err == nil {
		return nil
	}
	e := &MCPError{Message: fmt.Sprintf("%s: %v", op, err)}

	switch {
	case errors.Is(err, gokd.ErrSessionClosed):
		e.Code = "SESSION_CLOSED"
		e.Hint = "Reconnect the MCP client to start a fresh gokd session."
	case errors.Is(err, gokd.ErrTimeout):
		e.Code = "TIMEOUT"
		e.HRESULT = "0x00000001"
		e.Hint = "The operation timed out. Increase timeout_seconds or call break_in."
	case errors.Is(err, gokd.ErrNotFound):
		e.Code = "NOT_FOUND"
		e.HRESULT = "0x80000002"
		e.Hint = "No result. Check the input value; try wildcards or module! prefix."
	default:
		var hr gokd.HRESULTError
		if errors.As(err, &hr) {
			code := uint32(int32(hr))
			e.HRESULT = fmt.Sprintf("0x%08X", code)
			if m, ok := hresultMap[code]; ok {
				e.Code = m.Code
				e.Hint = m.Hint
			} else {
				e.Code = "HRESULT"
			}
		} else {
			e.Code = "INTERNAL"
		}
	}

	if next, ok := opHints[op]; ok {
		e.NextTools = next
	}
	return e
}

// envelopeContent renders an MCPError as a pair of TextContent blocks: the
// first is the human-readable message (what most LLMs show by default),
// the second is the full envelope encoded as JSON for tool-using clients.
//
// We embed the JSON in a second Content block because the MCP SDK's
// ToolHandlerFor wrapper unconditionally overwrites CallToolResult.
// StructuredContent with the marshaled Out value (see go-sdk
// server.go:384), making it impossible to ship structured error data via
// StructuredContent without changing every Out type.
func envelopeContent(e *MCPError) []mcp.Content {
	jsonBytes, _ := json.Marshal(e)
	return []mcp.Content{
		&mcp.TextContent{Text: e.Message},
		&mcp.TextContent{Text: string(jsonBytes)},
	}
}
