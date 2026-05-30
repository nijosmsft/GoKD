package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nijosmsft/gokd"
)

type srv struct {
	sess      gokd.Session
	readonly  bool
	unsafeRaw bool
}

type okOutput struct {
	OK bool `json:"ok"`
}

type attachProcessInput struct {
	PID uint32 `json:"pid" jsonschema:"process ID to attach to"`
}

type createProcessInput struct {
	CommandLine  string `json:"command_line" jsonschema:"command line to create under the debugger"`
	InitialBreak bool   `json:"initial_break" jsonschema:"whether to request an initial debugger break"`
}

type pathInput struct {
	Path string `json:"path" jsonschema:"filesystem path"`
}

type attachKernelInput struct {
	ConnectionString string `json:"connection_string" jsonschema:"DbgEng kernel connection string, for example net:port=50000,key=1.2.3.4"`
	TimeoutSeconds   int    `json:"timeout_seconds,omitempty" jsonschema:"timeout in seconds; 0 means no timeout"`
	Passive          bool   `json:"passive" jsonschema:"use passive attach without an initial active break-in"`
}

type connectionInput struct {
	Connection string `json:"connection" jsonschema:"DbgEng remote process-server connection string"`
}

type modulesOutput struct {
	Modules []Module `json:"modules"`
}
type threadsOutput struct {
	Threads []Thread `json:"threads"`
}

type setThreadInput struct {
	SystemID uint32 `json:"system_id" jsonschema:"system thread ID to make current"`
}

type getRegistersInput struct {
	Names []string `json:"names,omitempty" jsonschema:"optional register names; empty returns all registers"`
}

type registersOutput struct {
	Registers map[string]string `json:"registers"`
}

type setRegisterInput struct {
	Name     string `json:"name" jsonschema:"register name"`
	ValueHex string `json:"value_hex" jsonschema:"new register value, parsed with base 0 (for example 0x1234)"`
}

type stackOutput struct {
	Frames []Frame `json:"frames"`
}

type readMemoryInput struct {
	AddressHex string `json:"address_hex" jsonschema:"virtual address to read, parsed with base 0"`
	Length     uint64 `json:"length" jsonschema:"number of bytes to read"`
}

type hexOutput struct {
	Hex string `json:"hex"`
}

type writeMemoryInput struct {
	AddressHex string `json:"address_hex" jsonschema:"virtual address to write, parsed with base 0"`
	Hex        string `json:"hex" jsonschema:"hex-encoded bytes to write"`
}

type writeMemoryOutput struct {
	OK           bool `json:"ok"`
	BytesWritten int  `json:"bytes_written"`
}

type readPhysicalInput struct {
	AddressHex string `json:"address_hex" jsonschema:"physical address to read, parsed with base 0"`
	Length     uint64 `json:"length" jsonschema:"number of bytes to read"`
}

type disassembleInput struct {
	AddressHex string `json:"address_hex" jsonschema:"address to disassemble, parsed with base 0"`
	Count      int    `json:"count,omitempty" jsonschema:"number of instructions; defaults to 8"`
}

type disassembleOutput struct {
	Instructions []Instruction `json:"instructions"`
}

type nameInput struct {
	Name string `json:"name" jsonschema:"symbol name"`
}

type nameToAddrOutput struct {
	AddressHex string `json:"address_hex"`
}

type addrInput struct {
	AddressHex string `json:"address_hex" jsonschema:"address parsed with base 0"`
}

type addrToNameOutput struct {
	Name         string `json:"name"`
	Displacement uint64 `json:"displacement"`
}

type typeInput struct {
	Module   string `json:"module" jsonschema:"module name containing the type"`
	TypeName string `json:"type_name" jsonschema:"type name to inspect"`
}

type fieldsOutput struct {
	Fields []Field `json:"fields"`
}
type typeSizeOutput struct {
	Size uint64 `json:"size"`
}

type addBreakpointInput struct {
	AddressHex string `json:"address_hex,omitempty" jsonschema:"breakpoint address, parsed with base 0"`
	Symbol     string `json:"symbol,omitempty" jsonschema:"breakpoint symbol expression"`
}

type addBreakpointOutput struct {
	ID         uint32 `json:"id"`
	AddressHex string `json:"address_hex"`
}

type breakpointIDInput struct {
	ID uint32 `json:"id" jsonschema:"breakpoint ID"`
}

type enableBreakpointInput struct {
	ID      uint32 `json:"id" jsonschema:"breakpoint ID"`
	Enabled bool   `json:"enabled" jsonschema:"whether the breakpoint should be enabled"`
}

type breakpointsOutput struct {
	Breakpoints []Breakpoint `json:"breakpoints"`
}

type executionInput struct {
	TimeoutSeconds int `json:"timeout_seconds,omitempty" jsonschema:"timeout in seconds; 0 means no timeout"`
}

type stopOutput struct {
	Stop StopEvent `json:"stop"`
}

type symbolPathInput struct {
	Path string `json:"path" jsonschema:"new symbol path"`
}

type symbolPathOutput struct {
	Path string `json:"path"`
}

type executeRawInput struct {
	Command string `json:"command" jsonschema:"raw DbgEng command to execute"`
}

type executeRawOutput struct {
	Output string `json:"output"`
}

// --- t1-4 symbol reload / status ---

type reloadSymbolsInput struct {
	Spec           string `json:"spec,omitempty" jsonschema:"reload spec forwarded verbatim to ReloadWide (for example \"/f\" or \"/f nt\"); empty reloads anything stale"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"timeout in seconds; 0 means no timeout"`
}

type symFixInput struct {
	Cache string `json:"cache,omitempty" jsonschema:"optional local cache directory; empty uses a per-user default"`
}

// --- t1-1 evaluate ---

type evaluateInput struct {
	Expr           string `json:"expr" jsonschema:"expression to evaluate in the current syntax (MASM by default)"`
	DesiredType    string `json:"desired_type,omitempty" jsonschema:"requested value kind (invalid|int8|int16|int32|int64|float32|float64|float80|float82|float128|vector64|vector128); empty/invalid means 'natural'"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"timeout in seconds; 0 means no timeout"`
}

type evaluateOutput struct {
	Type      string  `json:"type"`
	U64       uint64  `json:"u64"`
	F64       float64 `json:"f64"`
	RawHex    string  `json:"raw_hex"`
	Remainder uint32  `json:"remainder"`
}

type radixOutput struct {
	Radix uint32 `json:"radix"`
}

type setRadixInput struct {
	Radix uint32 `json:"radix" jsonschema:"new radix (typically 10 or 16)"`
}

type expressionSyntaxOutput struct {
	Syntax string `json:"syntax"`
}

type setExpressionSyntaxInput struct {
	Syntax string `json:"syntax" jsonschema:"expression syntax to switch to (masm|cpp)"`
}

// --- t1-3 source lines ---

type addrToLineInput struct {
	AddressHex string `json:"address_hex" jsonschema:"instruction address as a hex string (with or without 0x prefix)"`
}

type addrToLineOutput struct {
	File         string `json:"file"`
	Line         uint32 `json:"line"`
	Displacement uint64 `json:"displacement"`
}

type lineToAddrInput struct {
	File string `json:"file" jsonschema:"absolute source path as stored in the PDB"`
	Line uint32 `json:"line" jsonschema:"1-based source line"`
}

type lineToAddrOutput struct {
	AddressHex string `json:"address_hex"`
}

type addBreakpointSourceLineInput struct {
	File string `json:"file" jsonschema:"absolute source path as stored in the PDB"`
	Line uint32 `json:"line" jsonschema:"1-based source line"`
}

// --- t1-6 memory search / translate / query ---

type searchMemoryInput struct {
	StartHex    string `json:"start_hex" jsonschema:"start virtual address (hex, with or without 0x)"`
	Length      uint64 `json:"length" jsonschema:"byte length to scan; keep small (<=4 KB) for performance"`
	PatternHex  string `json:"pattern_hex" jsonschema:"byte pattern as contiguous hex (\"deadbeef\") or space-separated bytes (\"de ad be ef\")"`
	Granularity uint32 `json:"granularity,omitempty" jsonschema:"stride DbgEng uses; must be 1, 4, or 8 (default 1)"`
}

type searchMemoryOutput struct {
	Found    bool   `json:"found"`
	MatchHex string `json:"match_hex"`
}

type virtualToPhysicalInput struct {
	VAHex string `json:"va_hex" jsonschema:"virtual address (hex). Kernel-mode sessions only."`
}

type virtualToPhysicalOutput struct {
	PAHex string `json:"pa_hex"`
}

type queryRegionInput struct {
	VAHex string `json:"va_hex" jsonschema:"virtual address (hex) to look up"`
}

type queryRegionOutput struct {
	BaseAddressHex       string `json:"base_address_hex"`
	AllocationBaseHex    string `json:"allocation_base_hex"`
	AllocationProtectHex string `json:"allocation_protect_hex"`
	RegionSize           uint64 `json:"region_size"`
	StateHex             string `json:"state_hex"`
	ProtectHex           string `json:"protect_hex"`
	TypeHex              string `json:"type_hex"`
}

// --- t1-5 data + conditional breakpoints ---

type addDataBreakpointInput struct {
	AddressHex string   `json:"address_hex" jsonschema:"watched virtual address (hex)"`
	Size       uint32   `json:"size" jsonschema:"watched region size in bytes; must be 1, 2, 4, or 8"`
	Access     []string `json:"access" jsonschema:"access types that trip the breakpoint; any combination of 'read', 'write', 'execute', 'io'"`
}

type configureBreakpointInput struct {
	ID            uint32  `json:"id" jsonschema:"breakpoint ID returned by add_breakpoint / add_data_breakpoint"`
	PassCount     uint32  `json:"pass_count,omitempty" jsonschema:"hit count before the BP fires; 0 leaves existing value alone"`
	MatchThreadID *uint32 `json:"match_thread_id,omitempty" jsonschema:"system thread ID filter; omit to leave existing value alone, or pass 0xFFFFFFFF for 'any thread'"`
	Command       *string `json:"command,omitempty" jsonschema:"WinDbg command to execute on hit; omit to leave alone, empty string to clear"`
}

type breakpointCommandInput struct {
	ID uint32 `json:"id" jsonschema:"breakpoint ID"`
}

type breakpointCommandOutput struct {
	Command string `json:"command"`
}

// --- t1-2 write dump ---

type writeDumpInput struct {
	Path           string `json:"path" jsonschema:"absolute output path for the .dmp file"`
	Kind           string `json:"kind,omitempty" jsonschema:"dump kind: 'small' | 'default' | 'full' (default 'default')"`
	Flags          uint32 `json:"flags,omitempty" jsonschema:"DEBUG_FORMAT_USER_SMALL_* bitmask forwarded raw"`
	Comment        string `json:"comment,omitempty" jsonschema:"optional comment recorded inside the dump"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"timeout in seconds; 0 means no timeout. Default 600 if omitted."`
}

// --- t1-8 last event / bugcheck ---

type lastExceptionOutput struct {
	Found           bool     `json:"found"`
	Code            uint32   `json:"code,omitempty"`
	CodeHex         string   `json:"code_hex,omitempty"`
	Flags           uint32   `json:"flags,omitempty"`
	AddressHex      string   `json:"address_hex,omitempty"`
	NestedRecordHex string   `json:"nested_record_hex,omitempty"`
	ParameterCount  uint32   `json:"parameter_count,omitempty"`
	Parameters      []string `json:"parameters,omitempty"`
	FirstChance     bool     `json:"first_chance,omitempty"`
	ProcessID       uint32   `json:"process_id,omitempty"`
	ThreadID        uint32   `json:"thread_id,omitempty"`
	Description     string   `json:"description,omitempty"`
}

type bugCheckOutput struct {
	Found       bool     `json:"found"`
	Code        uint32   `json:"code,omitempty"`
	CodeHex     string   `json:"code_hex,omitempty"`
	Args        []string `json:"args,omitempty"`
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
}

// --- t1-7 dump type ---

type dumpTypeInput struct {
	Module         string `json:"module" jsonschema:"module name whose symbols define the type (e.g. 'ntdll')"`
	Type           string `json:"type" jsonschema:"type name (e.g. '_PEB' or 'MY_STRUCT')"`
	AddressHex     string `json:"address_hex" jsonschema:"virtual address at which to read the typed value"`
	MaxDepth       int    `json:"max_depth,omitempty" jsonschema:"recursion depth; 0 = header only (default 3)"`
	FollowPtrs     bool   `json:"follow_ptrs,omitempty" jsonschema:"if true, follow non-NULL pointer fields one extra level (cycle-detected)"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"timeout in seconds; 0 means no timeout"`
}

type typeValueOutput struct {
	Name       string `json:"name,omitempty"`
	TypeName   string `json:"type_name,omitempty"`
	AddressHex string `json:"address_hex"`
	Size       uint32 `json:"size"`
	RawHex     string `json:"raw_hex,omitempty"`
	Error      string `json:"error,omitempty"`
	Decoded    any    `json:"decoded,omitempty"`
	// Children is typed as []any to break the recursive cycle that the
	// jsonschema-go reflector cannot encode. Each element is in fact a
	// *typeValueOutput; the JSON shape is unchanged.
	Children []any `json:"children,omitempty"`
}

// toolErr returns an MCP error result populated with a structured envelope.
// op identifies the calling tool ("attach_process", "read_memory", ...) so
// the envelope can include canonical follow-up tools.
//
// The envelope is encoded as a second TextContent block on the returned
// CallToolResult: the SDK's ToolHandlerFor wrapper overwrites
// StructuredContent with the marshaled zero Out value, so we cannot ship
// the envelope through StructuredContent without changing every Out type.
// Clients that read Content will see [message, json]; non-structured
// clients still get the human-readable message in the first block.
func toolErr[Out any](op string, err error) (*mcp.CallToolResult, Out, error) {
	var zero Out
	env := wrapErr(op, err)
	return &mcp.CallToolResult{
		IsError: true,
		Content: envelopeContent(env),
	}, zero, nil
}

func checkContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func contextWithSeconds(parent context.Context, seconds int) (context.Context, context.CancelFunc, error) {
	if seconds < 0 {
		return nil, nil, fmt.Errorf("timeout_seconds must be >= 0")
	}
	if seconds == 0 {
		return parent, func() {}, nil
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(seconds)*time.Second)
	return ctx, cancel, nil
}

func registerTools(server *mcp.Server, s *srv) {
	addToolMaybe(s, server, &mcp.Tool{Name: "attach_process", Description: "Attach to a running user-mode process by process ID."}, s.attachProcess)
	addToolMaybe(s, server, &mcp.Tool{Name: "create_process", Description: "Create a user-mode process under the debugger."}, s.createProcess)
	addToolMaybe(s, server, &mcp.Tool{Name: "open_dump", Description: "Open a crash dump file as the current target."}, s.openDump)
	addToolMaybe(s, server, &mcp.Tool{Name: "attach_kernel", Description: "Attach to a kernel target using a DbgEng connection string."}, s.attachKernel)
	addToolMaybe(s, server, &mcp.Tool{Name: "detach", Description: "Detach from the current target."}, s.detach)
	addToolMaybe(s, server, &mcp.Tool{Name: "connect_remote", Description: "Connect to a DbgEng dbgsrv process server."}, s.connectRemote)
	addToolMaybe(s, server, &mcp.Tool{Name: "disconnect_remote", Description: "Disconnect from the current DbgEng process server."}, s.disconnectRemote)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_modules", Description: "List all modules loaded in the current target. Returns base address, size, and name for each module."}, s.getModules)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_threads", Description: "List threads in the current target."}, s.getThreads)
	addToolMaybe(s, server, &mcp.Tool{Name: "set_thread", Description: "Set the current thread by system thread ID."}, s.setThread)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_registers", Description: "Read registers from the current thread; optionally filter by register name."}, s.getRegisters)
	addToolMaybe(s, server, &mcp.Tool{Name: "set_register", Description: "Set a register value in the current thread."}, s.setRegister)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_stack", Description: "Return the current thread stack frames with symbol information."}, s.getStack)
	addToolMaybe(s, server, &mcp.Tool{Name: "read_memory", Description: "Read virtual memory and return hex-encoded bytes."}, s.readMemory)
	addToolMaybe(s, server, &mcp.Tool{Name: "write_memory", Description: "Write hex-encoded bytes to virtual memory."}, s.writeMemory)
	addToolMaybe(s, server, &mcp.Tool{Name: "read_physical", Description: "Read physical memory and return hex-encoded bytes."}, s.readPhysical)
	addToolMaybe(s, server, &mcp.Tool{Name: "disassemble", Description: "Disassemble instructions starting at an address."}, s.disassemble)
	addToolMaybe(s, server, &mcp.Tool{Name: "name_to_addr", Description: "Resolve a symbol name to an address."}, s.nameToAddr)
	addToolMaybe(s, server, &mcp.Tool{Name: "addr_to_name", Description: "Resolve an address to a symbol name and displacement."}, s.addrToName)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_type_fields", Description: "List fields for a type using DbgHelp symbol information."}, s.getTypeFields)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_type_size", Description: "Return the size of a type using DbgHelp symbol information."}, s.getTypeSize)
	addToolMaybe(s, server, &mcp.Tool{Name: "add_breakpoint", Description: "Add a breakpoint by address or symbol expression."}, s.addBreakpoint)
	addToolMaybe(s, server, &mcp.Tool{Name: "remove_breakpoint", Description: "Remove a breakpoint by ID."}, s.removeBreakpoint)
	addToolMaybe(s, server, &mcp.Tool{Name: "enable_breakpoint", Description: "Enable or disable a breakpoint by ID."}, s.enableBreakpoint)
	addToolMaybe(s, server, &mcp.Tool{Name: "list_breakpoints", Description: "List breakpoints in the current target."}, s.listBreakpoints)
	addToolMaybe(s, server, &mcp.Tool{Name: "go_execution", Description: "Continue target execution until a stop event or timeout. Long running."}, s.goExecution)
	addToolMaybe(s, server, &mcp.Tool{Name: "step_in", Description: "Step into one instruction or source line and return the stop event."}, s.stepIn)
	addToolMaybe(s, server, &mcp.Tool{Name: "step_over", Description: "Step over one instruction or source line and return the stop event."}, s.stepOver)
	addToolMaybe(s, server, &mcp.Tool{Name: "step_out", Description: "Step out of the current function and return the stop event."}, s.stepOut)
	addToolMaybe(s, server, &mcp.Tool{Name: "break_in", Description: "Interrupt a running target or long-running go_execution call."}, s.breakIn)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_symbol_path", Description: "Get the current debugger symbol path."}, s.getSymbolPath)
	addToolMaybe(s, server, &mcp.Tool{Name: "set_symbol_path", Description: "Set the debugger symbol path."}, s.setSymbolPath)
	if !s.readonly || s.unsafeRaw {
		execAnnot := &mcp.ToolAnnotations{}
		dt := true
		execAnnot.DestructiveHint = &dt
		ow := true
		execAnnot.OpenWorldHint = &ow
		mcp.AddTool(server, &mcp.Tool{Name: "execute_raw", Description: "WARNING: arbitrary dbgeng command. Use only when other tools are insufficient.", Annotations: execAnnot}, s.executeRaw)
	}

	// --- t1-4 symbol reload / status ---
	addToolMaybe(s, server, &mcp.Tool{Name: "reload_symbols", Description: "Force a symbol reload via IDebugSymbols3::ReloadWide. Use when get_modules reports a deferred symbol_type or after changing set_symbol_path. spec is forwarded verbatim ('', '/f', '/f <module>'). May download from the symbol server — supply timeout_seconds (default 0 = no timeout) to bound the wait. Returns ok=true on success."}, s.reloadSymbols)
	addToolMaybe(s, server, &mcp.Tool{Name: "sym_fix", Description: "Configure the symbol path to the standard Microsoft public-symbol-server cache layout (mirrors WinDbg .symfix). Pass an optional cache directory; empty uses a per-user default. Returns ok=true. Follow with reload_symbols to actually pull PDBs."}, s.symFix)

	// --- t1-1 evaluate ---
	addToolMaybe(s, server, &mcp.Tool{Name: "evaluate", Description: "Evaluate a MASM or C++ expression via IDebugControl4::EvaluateWide. Use when you need a typed value from a symbol/arithmetic expression like 'nt!KiSystemServiceStart+0x40' or 'sizeof(_EPROCESS)'. Default syntax is MASM; switch with set_expression_syntax for C++ scope-resolution. May stall on PDB downloads — supply timeout_seconds. Returns type (string), u64 (integer slot), f64 (float slot), raw_hex (24-byte DEBUG_VALUE tail), and remainder (byte index where parsing stopped)."}, s.evaluate)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_radix", Description: "Return the current numeric radix used by evaluate and DbgEng formatting (typically 10 or 16)."}, s.getRadix)
	addToolMaybe(s, server, &mcp.Tool{Name: "set_radix", Description: "Set the numeric radix used by evaluate and DbgEng formatting. Typical values: 10, 16."}, s.setRadix)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_expression_syntax", Description: "Return the current expression-parser syntax ('masm' or 'cpp')."}, s.getExpressionSyntax)
	addToolMaybe(s, server, &mcp.Tool{Name: "set_expression_syntax", Description: "Switch the expression-parser syntax. Accepts 'masm' or 'cpp'. C++ is needed for scope-resolved names like MyClass::Method."}, s.setExpressionSyntax)

	// --- t1-3 source lines ---
	addToolMaybe(s, server, &mcp.Tool{Name: "addr_to_line", Description: "Map an instruction address to its source (file, line, displacement) via IDebugSymbols3::GetLineByOffsetWide. Returns an error containing 'HRESULT 0x80000002' (E_NOTFOUND) when no line info is loaded for the address — install matching PDBs or run reload_symbols first."}, s.addrToLine)
	addToolMaybe(s, server, &mcp.Tool{Name: "line_to_addr", Description: "Map a (file, line) pair to an instruction address via IDebugSymbols3::GetOffsetByLineWide. The file path must be the canonical absolute path the PDB was built with; partial matches fail with E_NOTFOUND."}, s.lineToAddr)
	addToolMaybe(s, server, &mcp.Tool{Name: "add_breakpoint_source_line", Description: "Resolve a (file, line) pair to an address and install a code breakpoint there. Requires line-info PDBs for the target binary; otherwise fails with E_NOTFOUND."}, s.addBreakpointSourceLine)

	// --- t1-6 memory search / translate / query ---
	addToolMaybe(s, server, &mcp.Tool{Name: "search_memory", Description: "Scan [start_hex, start_hex+length) for pattern_hex via IDebugDataSpaces4::SearchVirtual. granularity must be 1, 4, or 8 (defaults to 1). Returns {found:false, match_hex:\"\"} when the pattern is not present so callers can loop without exception handling. Keep length small (<= 4 KB) — SearchVirtual is slow on large ranges."}, s.searchMemory)
	addToolMaybe(s, server, &mcp.Tool{Name: "virtual_to_physical", Description: "Translate a virtual address to a physical address via IDebugDataSpaces4::VirtualToPhysical. Kernel-mode sessions only; user-mode targets fail with E_NOTIMPL or similar."}, s.virtualToPhysical)
	addToolMaybe(s, server, &mcp.Tool{Name: "query_region", Description: "Return the MEMORY_BASIC_INFORMATION64 record covering va_hex via IDebugDataSpaces4::QueryVirtual. Fields use raw Windows numerics: state (MEM_COMMIT=0x1000, MEM_RESERVE=0x2000, MEM_FREE=0x10000), type (MEM_PRIVATE=0x20000, MEM_MAPPED=0x40000, MEM_IMAGE=0x1000000), protect (PAGE_* flags)."}, s.queryRegion)

	// --- t1-2 write dump ---
	addToolMaybe(s, server, &mcp.Tool{Name: "write_dump", Description: "Snapshot the current target to a .dmp file via IDebugClient5::WriteDumpFileWide. path must be absolute. kind is 'small' (1024), 'default' (1025), or 'full' (1026) — defaults to 'default'. flags is the raw DEBUG_FORMAT_USER_SMALL_* bitmask. Synchronous and uncancellable mid-call; default timeout_seconds is 600. Use to capture state for offline analysis."}, s.writeDump)

	// --- t1-5 data + conditional breakpoints ---
	addToolMaybe(s, server, &mcp.Tool{Name: "add_data_breakpoint", Description: "Install a hardware ('break-on-access') breakpoint at address_hex covering size bytes. size must be 1, 2, 4, or 8. access is any non-empty subset of ['read', 'write', 'execute', 'io']. x64 hardware supports at most four enabled data breakpoints concurrently — the fifth will fail at the next go_execution call."}, s.addDataBreakpoint)
	addToolMaybe(s, server, &mcp.Tool{Name: "configure_breakpoint", Description: "Apply non-positional configuration (pass count, thread filter, WinDbg command) to an existing breakpoint without recreating it. Each field is independently optional: pass_count=0 leaves existing alone; omit match_thread_id to leave alone or pass 0xFFFFFFFF for 'any thread'; omit command to leave alone, pass empty string to clear."}, s.configureBreakpoint)
	addToolMaybe(s, server, &mcp.Tool{Name: "breakpoint_command", Description: "Return the WinDbg command string attached to a breakpoint (empty if none)."}, s.breakpointCommand)

	// --- t1-8 last event / bugcheck ---
	addToolMaybe(s, server, &mcp.Tool{Name: "last_exception", Description: "Return the most recent DEBUG_EVENT_EXCEPTION record reported by DbgEng (the .lastevent / .exr surface). Returns {found:false} when the last event was not an exception — e.g. an attach breakpoint or a process-exit notification. parameters carries raw EXCEPTION_RECORD.ExceptionInformation values (e.g. for access violations [0] is the read/write/execute flag and [1] is the faulting VA)."}, s.lastException)
	addToolMaybe(s, server, &mcp.Tool{Name: "bug_check", Description: "Read the kernel bugcheck record via IDebugControl4::ReadBugCheckData. Kernel-mode sessions only — user-mode targets return {found:false}. name and description are best-effort lookups for ~20 common codes; unknown codes still surface the raw code and four args."}, s.bugCheck)

	// --- t1-7 dump type ---
	addToolMaybe(s, server, &mcp.Tool{Name: "dump_type", Description: "Walk a typed object recursively (the 'dt -r' surface). Resolves type in module's symbol namespace, reads address_hex as that type, and recurses into struct fields up to max_depth levels (default 3). follow_ptrs dereferences non-NULL pointer fields one extra level with cycle detection. Special decoders surface human-readable values for _UNICODE_STRING (string), _LIST_ENTRY (Flink/Blink), GUID, and _LARGE_INTEGER. Requires PDBs that carry the requested type — public-symbol-only PDBs typically do NOT include OS structures like _PEB."}, s.dumpType)
}

func (s *srv) attachProcess(ctx context.Context, _ *mcp.CallToolRequest, in attachProcessInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput]("attach_process", err)
	}
	if err := s.sess.AttachProcess(in.PID, gokd.AttachDefault); err != nil {
		return toolErr[okOutput]("attach_process", err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) createProcess(ctx context.Context, _ *mcp.CallToolRequest, in createProcessInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput]("create_process", err)
	}
	if strings.TrimSpace(in.CommandLine) == "" {
		return toolErr[okOutput]("create_process", fmt.Errorf("command_line is required"))
	}
	if err := s.sess.CreateProcess(in.CommandLine, gokd.CreateOptions{Flags: 1, InitialBreak: in.InitialBreak}); err != nil {
		return toolErr[okOutput]("create_process", err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) openDump(ctx context.Context, _ *mcp.CallToolRequest, in pathInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput]("open_dump", err)
	}
	if strings.TrimSpace(in.Path) == "" {
		return toolErr[okOutput]("open_dump", fmt.Errorf("path is required"))
	}
	if err := s.sess.OpenDump(in.Path); err != nil {
		return toolErr[okOutput]("open_dump", err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) attachKernel(ctx context.Context, _ *mcp.CallToolRequest, in attachKernelInput) (*mcp.CallToolResult, okOutput, error) {
	ctx, cancel, err := contextWithSeconds(ctx, in.TimeoutSeconds)
	if err != nil {
		return toolErr[okOutput]("attach_kernel", err)
	}
	defer cancel()
	opts := gokd.KernelDefault
	if in.Passive {
		opts = gokd.KernelPassive
	}
	if strings.TrimSpace(in.ConnectionString) == "" {
		return toolErr[okOutput]("attach_kernel", fmt.Errorf("connection_string is required"))
	}
	if err := s.sess.AttachKernel(ctx, in.ConnectionString, opts); err != nil {
		return toolErr[okOutput]("attach_kernel", err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) detach(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput]("detach", err)
	}
	if err := s.sess.Detach(); err != nil {
		return toolErr[okOutput]("detach", err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) connectRemote(ctx context.Context, _ *mcp.CallToolRequest, in connectionInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput]("connect_remote", err)
	}
	if strings.TrimSpace(in.Connection) == "" {
		return toolErr[okOutput]("connect_remote", fmt.Errorf("connection is required"))
	}
	if err := s.sess.ConnectRemote(in.Connection); err != nil {
		return toolErr[okOutput]("connect_remote", err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) disconnectRemote(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput]("disconnect_remote", err)
	}
	if err := s.sess.DisconnectRemote(); err != nil {
		return toolErr[okOutput]("disconnect_remote", err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) getModules(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, modulesOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[modulesOutput]("get_modules", err)
	}
	mods, err := s.sess.Modules()
	if err != nil {
		return toolErr[modulesOutput]("get_modules", err)
	}
	out := make([]Module, len(mods))
	for i, m := range mods {
		out[i] = formatModule(m)
	}
	return nil, modulesOutput{Modules: out}, nil
}

func (s *srv) getThreads(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, threadsOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[threadsOutput]("get_threads", err)
	}
	threads, err := s.sess.Threads()
	if err != nil {
		return toolErr[threadsOutput]("get_threads", err)
	}
	out := make([]Thread, len(threads))
	for i, t := range threads {
		out[i] = formatThread(t)
	}
	return nil, threadsOutput{Threads: out}, nil
}

func (s *srv) setThread(ctx context.Context, _ *mcp.CallToolRequest, in setThreadInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput]("set_thread", err)
	}
	if err := s.sess.SetThread(in.SystemID); err != nil {
		return toolErr[okOutput]("set_thread", err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) getRegisters(ctx context.Context, _ *mcp.CallToolRequest, in getRegistersInput) (*mcp.CallToolResult, registersOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[registersOutput]("get_registers", err)
	}
	regs, err := s.sess.Registers()
	if err != nil {
		return toolErr[registersOutput]("get_registers", err)
	}
	out := map[string]string{}
	if len(in.Names) == 0 {
		for _, r := range regs.Registers {
			if r.Valid {
				out[r.Name] = hex64(r.Value)
			}
		}
		return nil, registersOutput{Registers: out}, nil
	}
	for _, name := range in.Names {
		r, ok := regs.ByName[name]
		if !ok {
			r, ok = regs.ByName[strings.ToLower(name)]
		}
		if ok && r.Valid {
			out[r.Name] = hex64(r.Value)
		}
	}
	return nil, registersOutput{Registers: out}, nil
}

func (s *srv) setRegister(ctx context.Context, _ *mcp.CallToolRequest, in setRegisterInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput]("set_register", err)
	}
	value, err := strconv.ParseUint(strings.TrimSpace(in.ValueHex), 0, 64)
	if err != nil {
		return toolErr[okOutput]("set_register", fmt.Errorf("invalid value_hex: %w", err))
	}
	if err := s.sess.SetRegister(in.Name, value); err != nil {
		return toolErr[okOutput]("set_register", err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) getStack(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, stackOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[stackOutput]("get_stack", err)
	}
	frames, err := s.sess.Stack()
	if err != nil {
		return toolErr[stackOutput]("get_stack", err)
	}
	out := make([]Frame, len(frames))
	for i, f := range frames {
		out[i] = formatFrame(f)
	}
	return nil, stackOutput{Frames: out}, nil
}

func (s *srv) readMemory(ctx context.Context, _ *mcp.CallToolRequest, in readMemoryInput) (*mcp.CallToolResult, hexOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[hexOutput]("read_memory", err)
	}
	addr, err := parseHexUint64(in.AddressHex, "address_hex")
	if err != nil {
		return toolErr[hexOutput]("read_memory", err)
	}
	data, err := s.sess.ReadMemory(addr, in.Length)
	if err != nil {
		return toolErr[hexOutput]("read_memory", err)
	}
	return nil, hexOutput{Hex: hex.EncodeToString(data)}, nil
}

func (s *srv) writeMemory(ctx context.Context, _ *mcp.CallToolRequest, in writeMemoryInput) (*mcp.CallToolResult, writeMemoryOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[writeMemoryOutput]("write_memory", err)
	}
	addr, err := parseHexUint64(in.AddressHex, "address_hex")
	if err != nil {
		return toolErr[writeMemoryOutput]("write_memory", err)
	}
	data, err := hex.DecodeString(strings.TrimSpace(in.Hex))
	if err != nil {
		return toolErr[writeMemoryOutput]("write_memory", fmt.Errorf("invalid hex: %w", err))
	}
	if err := s.sess.WriteMemory(addr, data); err != nil {
		return toolErr[writeMemoryOutput]("write_memory", err)
	}
	return nil, writeMemoryOutput{OK: true, BytesWritten: len(data)}, nil
}

func (s *srv) readPhysical(ctx context.Context, _ *mcp.CallToolRequest, in readPhysicalInput) (*mcp.CallToolResult, hexOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[hexOutput]("read_physical", err)
	}
	addr, err := parseHexUint64(in.AddressHex, "address_hex")
	if err != nil {
		return toolErr[hexOutput]("read_physical", err)
	}
	data, err := s.sess.ReadPhysical(addr, in.Length)
	if err != nil {
		return toolErr[hexOutput]("read_physical", err)
	}
	return nil, hexOutput{Hex: hex.EncodeToString(data)}, nil
}

func (s *srv) disassemble(ctx context.Context, _ *mcp.CallToolRequest, in disassembleInput) (*mcp.CallToolResult, disassembleOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[disassembleOutput]("disassemble", err)
	}
	addr, err := parseHexUint64(in.AddressHex, "address_hex")
	if err != nil {
		return toolErr[disassembleOutput]("disassemble", err)
	}
	count := in.Count
	if count == 0 {
		count = 8
	}
	if count < 0 {
		return toolErr[disassembleOutput]("disassemble", fmt.Errorf("count must be >= 0"))
	}
	ins, err := s.sess.DisassembleRange(addr, count)
	if err != nil {
		return toolErr[disassembleOutput]("disassemble", err)
	}
	out := make([]Instruction, len(ins))
	for i, inst := range ins {
		out[i] = formatInstruction(inst)
	}
	return nil, disassembleOutput{Instructions: out}, nil
}

func (s *srv) nameToAddr(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, nameToAddrOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[nameToAddrOutput]("name_to_addr", err)
	}
	addr, err := s.sess.NameToAddr(in.Name)
	if err != nil {
		return toolErr[nameToAddrOutput]("name_to_addr", err)
	}
	return nil, nameToAddrOutput{AddressHex: hex64(addr)}, nil
}

func (s *srv) addrToName(ctx context.Context, _ *mcp.CallToolRequest, in addrInput) (*mcp.CallToolResult, addrToNameOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[addrToNameOutput]("addr_to_name", err)
	}
	addr, err := parseHexUint64(in.AddressHex, "address_hex")
	if err != nil {
		return toolErr[addrToNameOutput]("addr_to_name", err)
	}
	name, disp, err := s.sess.AddrToName(addr)
	if err != nil {
		return toolErr[addrToNameOutput]("addr_to_name", err)
	}
	return nil, addrToNameOutput{Name: name, Displacement: disp}, nil
}

func (s *srv) getTypeFields(ctx context.Context, _ *mcp.CallToolRequest, in typeInput) (*mcp.CallToolResult, fieldsOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[fieldsOutput]("get_type_fields", err)
	}
	fields, err := s.sess.TypeFields(in.Module, in.TypeName)
	if err != nil {
		return toolErr[fieldsOutput]("get_type_fields", err)
	}
	out := make([]Field, len(fields))
	for i, f := range fields {
		out[i] = formatField(f)
	}
	return nil, fieldsOutput{Fields: out}, nil
}

func (s *srv) getTypeSize(ctx context.Context, _ *mcp.CallToolRequest, in typeInput) (*mcp.CallToolResult, typeSizeOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[typeSizeOutput]("get_type_size", err)
	}
	size, err := s.sess.TypeSize(in.Module, in.TypeName)
	if err != nil {
		return toolErr[typeSizeOutput]("get_type_size", err)
	}
	return nil, typeSizeOutput{Size: size}, nil
}

func (s *srv) addBreakpoint(ctx context.Context, _ *mcp.CallToolRequest, in addBreakpointInput) (*mcp.CallToolResult, addBreakpointOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[addBreakpointOutput]("add_breakpoint", err)
	}
	hasAddr := strings.TrimSpace(in.AddressHex) != ""
	hasSym := strings.TrimSpace(in.Symbol) != ""
	if hasAddr == hasSym {
		return toolErr[addBreakpointOutput]("add_breakpoint", fmt.Errorf("set exactly one of address_hex or symbol"))
	}
	var (
		bp   *gokd.Breakpoint
		err  error
		addr uint64
	)
	if hasAddr {
		addr, err = parseHexUint64(in.AddressHex, "address_hex")
		if err != nil {
			return toolErr[addBreakpointOutput]("add_breakpoint", err)
		}
		bp, err = s.sess.AddBreakpoint(addr)
	} else {
		bp, err = s.sess.AddBreakpointSym(in.Symbol)
	}
	if err != nil {
		return toolErr[addBreakpointOutput]("add_breakpoint", err)
	}
	resultAddr := bp.Address
	if resultAddr == 0 && hasSym {
		if resolved, err := s.sess.NameToAddr(in.Symbol); err == nil {
			resultAddr = resolved
		}
	}
	return nil, addBreakpointOutput{ID: bp.ID, AddressHex: hex64(resultAddr)}, nil
}

func (s *srv) removeBreakpoint(ctx context.Context, _ *mcp.CallToolRequest, in breakpointIDInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput]("remove_breakpoint", err)
	}
	if err := s.sess.RemoveBreakpoint(in.ID); err != nil {
		return toolErr[okOutput]("remove_breakpoint", err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) enableBreakpoint(ctx context.Context, _ *mcp.CallToolRequest, in enableBreakpointInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput]("enable_breakpoint", err)
	}
	if err := s.sess.EnableBreakpoint(in.ID, in.Enabled); err != nil {
		return toolErr[okOutput]("enable_breakpoint", err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) listBreakpoints(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, breakpointsOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[breakpointsOutput]("list_breakpoints", err)
	}
	bps, err := s.sess.Breakpoints()
	if err != nil {
		return toolErr[breakpointsOutput]("list_breakpoints", err)
	}
	out := make([]Breakpoint, len(bps))
	for i, bp := range bps {
		out[i] = formatBreakpoint(bp)
	}
	return nil, breakpointsOutput{Breakpoints: out}, nil
}

func (s *srv) runExecution(ctx context.Context, op string, in executionInput, fn func(context.Context) (*gokd.StopEvent, error)) (*mcp.CallToolResult, stopOutput, error) {
	ctx, cancel, err := contextWithSeconds(ctx, in.TimeoutSeconds)
	if err != nil {
		return toolErr[stopOutput](op, err)
	}
	defer cancel()
	ev, err := fn(ctx)
	if err != nil {
		return toolErr[stopOutput](op, err)
	}
	return nil, stopOutput{Stop: formatStopEvent(ev)}, nil
}

func (s *srv) goExecution(ctx context.Context, _ *mcp.CallToolRequest, in executionInput) (*mcp.CallToolResult, stopOutput, error) {
	return s.runExecution(ctx, "go", in, s.sess.Go)
}

func (s *srv) stepIn(ctx context.Context, _ *mcp.CallToolRequest, in executionInput) (*mcp.CallToolResult, stopOutput, error) {
	return s.runExecution(ctx, "step_in", in, s.sess.StepIn)
}

func (s *srv) stepOver(ctx context.Context, _ *mcp.CallToolRequest, in executionInput) (*mcp.CallToolResult, stopOutput, error) {
	return s.runExecution(ctx, "step_over", in, s.sess.StepOver)
}

func (s *srv) stepOut(ctx context.Context, _ *mcp.CallToolRequest, in executionInput) (*mcp.CallToolResult, stopOutput, error) {
	return s.runExecution(ctx, "step_out", in, s.sess.StepOut)
}

func (s *srv) breakIn(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput]("break_in", err)
	}
	if err := s.sess.BreakIn(); err != nil {
		return toolErr[okOutput]("break_in", err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) getSymbolPath(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, symbolPathOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[symbolPathOutput]("get_symbol_path", err)
	}
	path, err := s.sess.SymbolPath()
	if err != nil {
		return toolErr[symbolPathOutput]("get_symbol_path", err)
	}
	return nil, symbolPathOutput{Path: path}, nil
}

func (s *srv) setSymbolPath(ctx context.Context, _ *mcp.CallToolRequest, in symbolPathInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput]("set_symbol_path", err)
	}
	if err := s.sess.SetSymbolPath(in.Path); err != nil {
		return toolErr[okOutput]("set_symbol_path", err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) executeRaw(ctx context.Context, _ *mcp.CallToolRequest, in executeRawInput) (*mcp.CallToolResult, executeRawOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[executeRawOutput]("execute_raw", err)
	}
	if s.readonly && denyRawCommand(in.Command) {
		return toolErr[executeRawOutput]("execute_raw",
			fmt.Errorf("command %q rejected by readonly denylist", firstToken(in.Command)))
	}
	out, err := s.sess.Execute(in.Command)
	if err != nil {
		return toolErr[executeRawOutput]("execute_raw", err)
	}
	return nil, executeRawOutput{Output: out}, nil
}

// --- t1-4 symbol reload / status ---

func (s *srv) reloadSymbols(ctx context.Context, _ *mcp.CallToolRequest, in reloadSymbolsInput) (*mcp.CallToolResult, okOutput, error) {
	ctx, cancel, err := contextWithSeconds(ctx, in.TimeoutSeconds)
	if err != nil {
		return toolErr[okOutput]("reload_symbols", err)
	}
	defer cancel()
	if err := s.sess.ReloadSymbols(ctx, in.Spec); err != nil {
		return toolErr[okOutput]("reload_symbols", err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) symFix(ctx context.Context, _ *mcp.CallToolRequest, in symFixInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput]("sym_fix", err)
	}
	if err := s.sess.SymFix(in.Cache); err != nil {
		return toolErr[okOutput]("sym_fix", err)
	}
	return nil, okOutput{OK: true}, nil
}

// --- t1-1 evaluate ---

func (s *srv) evaluate(ctx context.Context, _ *mcp.CallToolRequest, in evaluateInput) (*mcp.CallToolResult, evaluateOutput, error) {
	if strings.TrimSpace(in.Expr) == "" {
		return toolErr[evaluateOutput]("evaluate", fmt.Errorf("expr is required"))
	}
	kind, ok := gokd.ParseValueKind(in.DesiredType)
	if !ok {
		return toolErr[evaluateOutput]("evaluate", fmt.Errorf("invalid desired_type: %q", in.DesiredType))
	}
	ctx, cancel, err := contextWithSeconds(ctx, in.TimeoutSeconds)
	if err != nil {
		return toolErr[evaluateOutput]("evaluate", err)
	}
	defer cancel()
	v, rem, err := s.sess.Evaluate(ctx, in.Expr, kind)
	if err != nil {
		return toolErr[evaluateOutput]("evaluate", err)
	}
	tname, u64, f64, rawHex := formatValue(v)
	return nil, evaluateOutput{Type: tname, U64: u64, F64: f64, RawHex: rawHex, Remainder: rem}, nil
}

func (s *srv) getRadix(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, radixOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[radixOutput]("get_radix", err)
	}
	r, err := s.sess.Radix()
	if err != nil {
		return toolErr[radixOutput]("get_radix", err)
	}
	return nil, radixOutput{Radix: r}, nil
}

func (s *srv) setRadix(ctx context.Context, _ *mcp.CallToolRequest, in setRadixInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput]("set_radix", err)
	}
	if err := s.sess.SetRadix(in.Radix); err != nil {
		return toolErr[okOutput]("set_radix", err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) getExpressionSyntax(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, expressionSyntaxOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[expressionSyntaxOutput]("get_expression_syntax", err)
	}
	syn, err := s.sess.ExpressionSyntax()
	if err != nil {
		return toolErr[expressionSyntaxOutput]("get_expression_syntax", err)
	}
	name := "masm"
	if syn == gokd.ExpressionSyntaxCPP {
		name = "cpp"
	}
	return nil, expressionSyntaxOutput{Syntax: name}, nil
}

func (s *srv) setExpressionSyntax(ctx context.Context, _ *mcp.CallToolRequest, in setExpressionSyntaxInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput]("set_expression_syntax", err)
	}
	var syn gokd.ExpressionSyntax
	switch strings.ToLower(strings.TrimSpace(in.Syntax)) {
	case "masm", "":
		syn = gokd.ExpressionSyntaxMASM
	case "cpp", "c++":
		syn = gokd.ExpressionSyntaxCPP
	default:
		return toolErr[okOutput]("set_expression_syntax", fmt.Errorf("invalid syntax: %q (want 'masm' or 'cpp')", in.Syntax))
	}
	if err := s.sess.SetExpressionSyntax(syn); err != nil {
		return toolErr[okOutput]("set_expression_syntax", err)
	}
	return nil, okOutput{OK: true}, nil
}

// --- t1-3 source lines ---

func (s *srv) addrToLine(ctx context.Context, _ *mcp.CallToolRequest, in addrToLineInput) (*mcp.CallToolResult, addrToLineOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[addrToLineOutput]("addr_to_line", err)
	}
	addr, err := parseHexUint64(in.AddressHex, "address_hex")
	if err != nil {
		return toolErr[addrToLineOutput]("addr_to_line", err)
	}
	sl, err := s.sess.AddrToLine(addr)
	if err != nil {
		return toolErr[addrToLineOutput]("addr_to_line", err)
	}
	return nil, addrToLineOutput{File: sl.File, Line: sl.Line, Displacement: sl.Displacement}, nil
}

func (s *srv) lineToAddr(ctx context.Context, _ *mcp.CallToolRequest, in lineToAddrInput) (*mcp.CallToolResult, lineToAddrOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[lineToAddrOutput]("line_to_addr", err)
	}
	if strings.TrimSpace(in.File) == "" {
		return toolErr[lineToAddrOutput]("line_to_addr", fmt.Errorf("file is required"))
	}
	if in.Line == 0 {
		return toolErr[lineToAddrOutput]("line_to_addr", fmt.Errorf("line must be >= 1"))
	}
	addr, err := s.sess.LineToAddr(in.File, in.Line)
	if err != nil {
		return toolErr[lineToAddrOutput]("line_to_addr", err)
	}
	return nil, lineToAddrOutput{AddressHex: hex64(addr)}, nil
}

func (s *srv) addBreakpointSourceLine(ctx context.Context, _ *mcp.CallToolRequest, in addBreakpointSourceLineInput) (*mcp.CallToolResult, addBreakpointOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[addBreakpointOutput]("add_breakpoint_source_line", err)
	}
	if strings.TrimSpace(in.File) == "" {
		return toolErr[addBreakpointOutput]("add_breakpoint_source_line", fmt.Errorf("file is required"))
	}
	if in.Line == 0 {
		return toolErr[addBreakpointOutput]("add_breakpoint_source_line", fmt.Errorf("line must be >= 1"))
	}
	bp, err := s.sess.AddBreakpointSourceLine(in.File, in.Line)
	if err != nil {
		return toolErr[addBreakpointOutput]("add_breakpoint_source_line", err)
	}
	addr := uint64(0)
	if bp != nil {
		addr = bp.Address
	}
	return nil, addBreakpointOutput{ID: bp.ID, AddressHex: hex64(addr)}, nil
}

// --- t1-6 memory search / translate / query ---

func (s *srv) searchMemory(ctx context.Context, _ *mcp.CallToolRequest, in searchMemoryInput) (*mcp.CallToolResult, searchMemoryOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[searchMemoryOutput]("search_memory", err)
	}
	start, err := parseHexUint64(in.StartHex, "start_hex")
	if err != nil {
		return toolErr[searchMemoryOutput]("search_memory", err)
	}
	if in.Length == 0 {
		return toolErr[searchMemoryOutput]("search_memory", fmt.Errorf("length must be > 0"))
	}
	pattern, err := parseHexBytes(in.PatternHex, "pattern_hex")
	if err != nil {
		return toolErr[searchMemoryOutput]("search_memory", err)
	}
	gran := in.Granularity
	if gran == 0 {
		gran = 1
	}
	match, err := s.sess.SearchMemory(start, in.Length, pattern, gran)
	if err != nil {
		if errors.Is(err, gokd.ErrNotFound) {
			return nil, searchMemoryOutput{Found: false, MatchHex: ""}, nil
		}
		return toolErr[searchMemoryOutput]("search_memory", err)
	}
	return nil, searchMemoryOutput{Found: true, MatchHex: hex64(match)}, nil
}

func (s *srv) virtualToPhysical(ctx context.Context, _ *mcp.CallToolRequest, in virtualToPhysicalInput) (*mcp.CallToolResult, virtualToPhysicalOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[virtualToPhysicalOutput]("virtual_to_physical", err)
	}
	va, err := parseHexUint64(in.VAHex, "va_hex")
	if err != nil {
		return toolErr[virtualToPhysicalOutput]("virtual_to_physical", err)
	}
	pa, err := s.sess.VirtualToPhysical(va)
	if err != nil {
		return toolErr[virtualToPhysicalOutput]("virtual_to_physical", err)
	}
	return nil, virtualToPhysicalOutput{PAHex: hex64(pa)}, nil
}

func (s *srv) queryRegion(ctx context.Context, _ *mcp.CallToolRequest, in queryRegionInput) (*mcp.CallToolResult, queryRegionOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[queryRegionOutput]("query_region", err)
	}
	va, err := parseHexUint64(in.VAHex, "va_hex")
	if err != nil {
		return toolErr[queryRegionOutput]("query_region", err)
	}
	r, err := s.sess.QueryRegion(va)
	if err != nil {
		return toolErr[queryRegionOutput]("query_region", err)
	}
	return nil, queryRegionOutput{
		BaseAddressHex:       hex64(r.BaseAddress),
		AllocationBaseHex:    hex64(r.AllocationBase),
		AllocationProtectHex: fmt.Sprintf("0x%x", uint32(r.AllocationProtect)),
		RegionSize:           r.RegionSize,
		StateHex:             fmt.Sprintf("0x%x", uint32(r.State)),
		ProtectHex:           fmt.Sprintf("0x%x", uint32(r.Protect)),
		TypeHex:              fmt.Sprintf("0x%x", uint32(r.Type)),
	}, nil
}

func (s *srv) writeDump(ctx context.Context, _ *mcp.CallToolRequest, in writeDumpInput) (*mcp.CallToolResult, okOutput, error) {
	if strings.TrimSpace(in.Path) == "" {
		return toolErr[okOutput]("write_dump", fmt.Errorf("path is required"))
	}
	var kind gokd.DumpKind
	switch strings.ToLower(strings.TrimSpace(in.Kind)) {
	case "", "default":
		kind = gokd.DumpDefault
	case "small":
		kind = gokd.DumpSmall
	case "full":
		kind = gokd.DumpFull
	default:
		return toolErr[okOutput]("write_dump", fmt.Errorf("invalid kind: %q (want 'small', 'default', or 'full')", in.Kind))
	}
	timeout := in.TimeoutSeconds
	if timeout == 0 {
		timeout = 600
	}
	ctx, cancel, err := contextWithSeconds(ctx, timeout)
	if err != nil {
		return toolErr[okOutput]("write_dump", err)
	}
	defer cancel()
	if err := s.sess.WriteDump(ctx, in.Path, gokd.WriteDumpOptions{
		Kind:    kind,
		Flags:   gokd.DumpFormatFlags(in.Flags),
		Comment: in.Comment,
	}); err != nil {
		return toolErr[okOutput]("write_dump", err)
	}
	return nil, okOutput{OK: true}, nil
}

// --- t1-5 data + conditional breakpoints ---

func (s *srv) addDataBreakpoint(ctx context.Context, _ *mcp.CallToolRequest, in addDataBreakpointInput) (*mcp.CallToolResult, addBreakpointOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[addBreakpointOutput]("add_data_breakpoint", err)
	}
	addr, err := parseHexUint64(in.AddressHex, "address_hex")
	if err != nil {
		return toolErr[addBreakpointOutput]("add_data_breakpoint", err)
	}
	if len(in.Access) == 0 {
		return toolErr[addBreakpointOutput]("add_data_breakpoint", fmt.Errorf("access must contain at least one of 'read', 'write', 'execute', 'io'"))
	}
	mask, err := parseBreakpointAccess(in.Access)
	if err != nil {
		return toolErr[addBreakpointOutput]("add_data_breakpoint", err)
	}
	bp, err := s.sess.AddDataBreakpoint(addr, in.Size, mask)
	if err != nil {
		return toolErr[addBreakpointOutput]("add_data_breakpoint", err)
	}
	return nil, addBreakpointOutput{ID: bp.ID, AddressHex: hex64(bp.Address)}, nil
}

func (s *srv) configureBreakpoint(ctx context.Context, _ *mcp.CallToolRequest, in configureBreakpointInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput]("configure_breakpoint", err)
	}
	opts := gokd.BreakpointOptions{
		PassCount:     in.PassCount,
		MatchThreadID: gokd.BreakpointMatchThreadAny,
	}
	if in.MatchThreadID != nil {
		opts.MatchThreadID = *in.MatchThreadID
	}
	if in.Command != nil {
		if *in.Command == "" {
			opts.ClearCommand = true
		} else {
			opts.Command = *in.Command
		}
	}
	if err := s.sess.ConfigureBreakpoint(in.ID, opts); err != nil {
		return toolErr[okOutput]("configure_breakpoint", err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) breakpointCommand(ctx context.Context, _ *mcp.CallToolRequest, in breakpointCommandInput) (*mcp.CallToolResult, breakpointCommandOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[breakpointCommandOutput]("breakpoint_command", err)
	}
	cmd, err := s.sess.BreakpointCommand(in.ID)
	if err != nil {
		return toolErr[breakpointCommandOutput]("breakpoint_command", err)
	}
	return nil, breakpointCommandOutput{Command: cmd}, nil
}

// --- t1-8 last event / bugcheck ---

func (s *srv) lastException(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, lastExceptionOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[lastExceptionOutput]("last_exception", err)
	}
	ex, err := s.sess.LastException()
	if err != nil {
		if errors.Is(err, gokd.ErrNotFound) {
			return nil, lastExceptionOutput{Found: false}, nil
		}
		return toolErr[lastExceptionOutput]("last_exception", err)
	}
	params := make([]string, 0, ex.ParameterCount)
	for i := uint32(0); i < ex.ParameterCount; i++ {
		params = append(params, hex64(ex.Parameters[i]))
	}
	return nil, lastExceptionOutput{
		Found:           true,
		Code:            ex.Code,
		CodeHex:         fmt.Sprintf("0x%08x", ex.Code),
		Flags:           ex.Flags,
		AddressHex:      hex64(ex.Address),
		NestedRecordHex: hex64(ex.NestedRecord),
		ParameterCount:  ex.ParameterCount,
		Parameters:      params,
		FirstChance:     ex.FirstChance,
		ProcessID:       ex.ProcessID,
		ThreadID:        ex.ThreadID,
		Description:     ex.Description,
	}, nil
}

func (s *srv) bugCheck(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, bugCheckOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[bugCheckOutput]("bug_check", err)
	}
	bc, err := s.sess.BugCheck()
	if err != nil {
		if errors.Is(err, gokd.ErrNotFound) {
			return nil, bugCheckOutput{Found: false}, nil
		}
		return toolErr[bugCheckOutput]("bug_check", err)
	}
	args := make([]string, 0, len(bc.Args))
	for _, a := range bc.Args {
		args = append(args, hex64(a))
	}
	return nil, bugCheckOutput{
		Found:       true,
		Code:        bc.Code,
		CodeHex:     fmt.Sprintf("0x%08x", bc.Code),
		Args:        args,
		Name:        bc.Name,
		Description: bc.Description,
	}, nil
}

// --- t1-7 dump type ---

func (s *srv) dumpType(ctx context.Context, _ *mcp.CallToolRequest, in dumpTypeInput) (*mcp.CallToolResult, typeValueOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[typeValueOutput]("dump_type", err)
	}
	if strings.TrimSpace(in.Module) == "" {
		return toolErr[typeValueOutput]("dump_type", fmt.Errorf("module is required"))
	}
	if strings.TrimSpace(in.Type) == "" {
		return toolErr[typeValueOutput]("dump_type", fmt.Errorf("type is required"))
	}
	addr, err := parseHexUint64(in.AddressHex, "address_hex")
	if err != nil {
		return toolErr[typeValueOutput]("dump_type", err)
	}
	ctx2, cancel, err := contextWithSeconds(ctx, in.TimeoutSeconds)
	if err != nil {
		return toolErr[typeValueOutput]("dump_type", err)
	}
	defer cancel()
	tv, err := s.sess.DumpType(ctx2, in.Module, in.Type, addr, gokd.DumpTypeOptions{
		MaxDepth:   in.MaxDepth,
		FollowPtrs: in.FollowPtrs,
	})
	if err != nil {
		return toolErr[typeValueOutput]("dump_type", err)
	}
	return nil, *formatTypeValue(tv), nil
}
