package main

import (
	"bytes"
	"context"
	"fmt"
	"text/template"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// promptDef describes one canned prompt that gokd-mcp exposes via
// MCP prompts/get. bodyTpl is rendered as a text/template with the
// caller-supplied arguments wrapped in {{ .Args.field }}.
type promptDef struct {
	name        string
	description string
	arguments   []*mcp.PromptArgument
	bodyTpl     string
}

var prompts = []promptDef{
	{
		name:        "triage-dump",
		description: "Step-by-step triage of an unfamiliar crash dump.",
		arguments: []*mcp.PromptArgument{
			{Name: "dump_path", Required: true, Description: "Absolute path to the .dmp file on the debugger host."},
		},
		bodyTpl: `You are debugging a Windows crash dump.

1. Call open_dump with path="{{ .Args.dump_path }}".
2. Call summarise_target to see the high-level state.
3. Call triage_crash to fetch the bug check / exception, faulting thread, stack, registers, and nearby disassembly in one shot.
4. If the bug check or exception code is unfamiliar, call evaluate with "@$exr" or look it up. If you need to inspect a pointer, use inspect_struct or read_string.
5. Summarise the root cause for the user in 3-5 sentences and quote the faulting symbol verbatim.`,
	},
	{
		name:        "attach-and-orient",
		description: "Attach to a live user-mode process and gather orientation.",
		arguments: []*mcp.PromptArgument{
			{Name: "pid", Required: true, Description: "Decimal PID."},
		},
		bodyTpl: `1. Call attach_process with pid={{ .Args.pid }}.
2. Call get_session_state. If status is "running", call break_in then get_session_state again.
3. Call get_modules with name_glob="*.exe" to find the main executable.
4. Call get_threads, then set_thread to thread 0.
5. Call get_stack to see what the main thread is doing.
6. Report what the process appears to be doing.`,
	},
	{
		name:        "find-who-wrote",
		description: "Find which thread wrote to a given address.",
		arguments: []*mcp.PromptArgument{
			{Name: "address", Required: true, Description: "Hex address being written, e.g. 0x7ff600401000."},
		},
		bodyTpl: `1. Call get_session_state to confirm the target is broken in.
2. Call add_data_breakpoint with address={{ .Args.address }}, access="write", length=8.
3. Call go_execution. When it returns, call last_exception (or inspect the StopEvent in get_recent_events) to identify the firing breakpoint.
4. Call get_stack and get_registers on the firing thread.
5. Report the writing call site.`,
	},
	{
		name:        "why-blocked",
		description: "Diagnose why a thread is blocked.",
		arguments: []*mcp.PromptArgument{
			{Name: "thread_id", Required: false, Description: "System thread ID. Defaults to the current thread."},
		},
		bodyTpl: `1. Call get_session_state.
2. {{ if .Args.thread_id }}Call set_thread with id={{ .Args.thread_id }}.{{ end }}
3. Call get_stack with max_frames=64.
4. Look for KiSwapContext, NtWait..., RtlEnterCritical..., or syscalls.
5. If a critical section appears, use inspect_struct on the RTL_CRITICAL_SECTION pointer to find the owner thread.
6. Report the wait reason and the owning thread, if any.`,
	},
	{
		name:        "inspect-object",
		description: "Inspect a typed object in memory and follow one pointer level.",
		arguments: []*mcp.PromptArgument{
			{Name: "type_name", Required: true, Description: "Type name, e.g. nt!_EPROCESS."},
			{Name: "address", Required: true, Description: "Hex address of the instance."},
		},
		bodyTpl: `1. Call get_type_size with type="{{ .Args.type_name }}" to sanity-check.
2. Call inspect_struct with type_name="{{ .Args.type_name }}", address="{{ .Args.address }}", recurse=1.
3. Highlight any string-like fields (UNICODE_STRING, char*) and call read_string on them.
4. Summarise the object state.`,
	},
	{
		name:        "kernel-attach-kdnet",
		description: "Attach to a Windows kernel target over KDNET and orient.",
		arguments: []*mcp.PromptArgument{
			{Name: "port", Required: true, Description: "KDNET UDP port, e.g. 50000."},
			{Name: "key", Required: true, Description: "KDNET key, e.g. 1.2.3.4."},
		},
		bodyTpl: `1. Call attach_kernel with connection_string="net:port={{ .Args.port }},key={{ .Args.key }}" and timeout_seconds=120.
   Do NOT include "target=..." in the connection string — that segment is for kdsrv-style routing and prevents the listener from starting.
2. After it returns, call break_in then get_session_state.
3. Call summarise_target.
4. Report what kind of kernel target is connected.`,
	},
}

// registerPrompts attaches every entry in `prompts` to the server.
// Templates are parsed once at registration time; render-time failures
// surface as ordinary GetPrompt errors.
func registerPrompts(server *mcp.Server) {
	for _, p := range prompts {
		pd := p
		tpl, err := template.New(pd.name).Parse(pd.bodyTpl)
		if err != nil {
			// A parse error here is a programmer mistake; surface it
			// loudly by re-parsing at handler time so the test
			// catches it without crashing the binary.
			server.AddPrompt(&mcp.Prompt{
				Name: pd.name, Description: pd.description, Arguments: pd.arguments,
			}, func(_ context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
				return nil, fmt.Errorf("prompt %q has invalid template: %v", pd.name, err)
			})
			continue
		}
		server.AddPrompt(&mcp.Prompt{
			Name: pd.name, Description: pd.description, Arguments: pd.arguments,
		}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			data := map[string]any{"Args": req.Params.Arguments}
			var buf bytes.Buffer
			if err := tpl.Execute(&buf, data); err != nil {
				return nil, fmt.Errorf("rendering prompt %q: %w", pd.name, err)
			}
			return &mcp.GetPromptResult{
				Description: pd.description,
				Messages: []*mcp.PromptMessage{{
					Role:    "user",
					Content: &mcp.TextContent{Text: buf.String()},
				}},
			}, nil
		})
	}
}
