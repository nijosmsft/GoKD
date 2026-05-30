package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nijosmsft/gokd"
)

// ring buffer capacities for events / output / raw-output continuation.
// These are intentionally small: ring buffers are the pull fallback for
// notification-aware clients; bulky output (target stdout) gets coalesced
// upstream by the drainer.
const (
	eventRingCap            = 32
	outputRingCap           = 256
	rawOverflowEntries      = 8
	rawOverflowMaxBytes     = 8 * 64 * 1024 // 8 × 64 KiB
	rawInlineCapBytes       = 16 * 1024     // 16 KiB inline before continuation
	rawContinuationMaxBytes = 16 * 1024     // 16 KiB per get_raw_output_continuation
)

// Pagination and size caps applied at the MCP boundary so a single tool
// call cannot blow up an LLM context. See design doc §5 (t2-4).
const (
	defaultModulesLimit = 100
	maxModulesLimit     = 500

	defaultStackFrames = 32
	maxStackFrames     = 256

	maxReadMemoryBytes = 4096

	maxDisassembleCount = 1024

	maxTypeFields = 256

	defaultRecentEventsLimit = 32
	maxRecentEventsLimit     = 32
	defaultRecentOutputLimit = 64
	maxRecentOutputLimit     = 256
)

type srv struct {
	sess      gokd.Session
	readonly  bool
	unsafeRaw bool

	// Ring buffers populated by the drainer; queried by get_recent_events
	// and get_recent_output for clients without notification subscriptions.
	eventRing  *ring[ringEvent]
	outputRing *ring[ringOutput]

	// rawOverflow stores bytes spilled past the inline cap from execute_raw,
	// keyed by continuation_token UUID. LRU evicts the oldest entry under
	// pressure.
	rawOverflow *lruCache

	// Last known target lifecycle marker, kept up to date by the drainer
	// as events flow through pushEvent.
	status *sessionStatus

	// Lablink composite-tool wiring (t4-1, t4-2). lablinkEnabled gates
	// registration of setup_kernel_debug / pull_latest_minidump in
	// registerTools, so they only appear in tools/list when -lablink-enabled
	// (or GOKD_MCP_LABLINK=1) is set. lablink is the shared, lazily-dialed
	// agent pool — nil when lablinkEnabled is false. Production code sets
	// both via newSrv → main.go; tests can set lablinkEnabled directly to
	// snapshot the composite tools' schemas without dialing a real pool.
	lablinkEnabled bool
	lablink        *lablinkClient
}

// newSrv constructs an srv with all Tier 2 plumbing wired up. Tests that
// only need the read paths can construct &srv{sess: ...} directly; this
// helper exists so production code in main.go has a single call site.
func newSrv(sess gokd.Session, readonly, unsafeRaw bool) *srv {
	return &srv{
		sess:        sess,
		readonly:    readonly,
		unsafeRaw:   unsafeRaw,
		eventRing:   newRing[ringEvent](eventRingCap),
		outputRing:  newRing[ringOutput](outputRingCap),
		rawOverflow: newLRUCache(rawOverflowEntries, rawOverflowMaxBytes),
		status:      &sessionStatus{},
	}
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

type getModulesInput struct {
	NameGlob string `json:"name_glob,omitempty" jsonschema:"optional filepath.Match glob applied to module Name (e.g. 'nt!*' or '*.dll'); empty matches every module"`
	Offset   int    `json:"offset,omitempty" jsonschema:"0-based index into the filtered set"`
	Limit    int    `json:"limit,omitempty" jsonschema:"max items to return (default 100, max 500)"`
}

type modulesOutput struct {
	Modules    []Module `json:"modules"`
	Total      int      `json:"total"`
	Returned   int      `json:"returned"`
	NextOffset int      `json:"next_offset,omitempty"`
	Truncated  bool     `json:"truncated,omitempty"`
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

type getStackInput struct {
	MaxFrames int `json:"max_frames,omitempty" jsonschema:"max frames to return (default 32, hard cap 256)"`
}

type stackOutput struct {
	Frames    []Frame `json:"frames"`
	Returned  int     `json:"returned"`
	Truncated bool    `json:"truncated,omitempty"`
}

type readMemoryInput struct {
	AddressHex string `json:"address_hex" jsonschema:"virtual address to read, parsed with base 0"`
	Length     uint64 `json:"length" jsonschema:"number of bytes to read (hard cap 4096)"`
}

type hexOutput struct {
	Hex       string `json:"hex"`
	Length    int    `json:"length,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
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
	Length     uint64 `json:"length" jsonschema:"number of bytes to read (hard cap 4096)"`
}

type disassembleInput struct {
	AddressHex string `json:"address_hex" jsonschema:"address to disassemble, parsed with base 0"`
	Count      int    `json:"count,omitempty" jsonschema:"number of instructions; defaults to 8, hard cap 1024"`
}

type disassembleOutput struct {
	Instructions []Instruction `json:"instructions"`
	Returned     int           `json:"returned,omitempty"`
	Truncated    bool          `json:"truncated,omitempty"`
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
	Fields    []Field `json:"fields"`
	Returned  int     `json:"returned,omitempty"`
	Truncated bool    `json:"truncated,omitempty"`
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
	Output            string `json:"output"`
	Bytes             int    `json:"bytes,omitempty"`
	Truncated         bool   `json:"truncated,omitempty"`
	ContinuationToken string `json:"continuation_token,omitempty"`
}

type getRawContinuationInput struct {
	Token  string `json:"token" jsonschema:"continuation_token returned by an earlier execute_raw call"`
	Offset int    `json:"offset,omitempty" jsonschema:"0-based byte offset into the overflow buffer"`
	Limit  int    `json:"limit,omitempty" jsonschema:"max bytes to return (default 16384, max 16384)"`
}

type getRawContinuationOutput struct {
	Output     string `json:"output"`
	NextOffset int    `json:"next_offset,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	Expired    bool   `json:"expired,omitempty"`
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
	addToolMaybe(s, server, &mcp.Tool{Name: "attach_process", Description: "Attaches the engine to a running Windows user-mode process by PID. Requires no prior target. After this returns, call get_session_state to discover the new state, then get_threads and get_modules."}, s.attachProcess)
	addToolMaybe(s, server, &mcp.Tool{Name: "create_process", Description: "Creates and attaches to a new Windows user-mode process from a command line. Useful for capturing startup. After this returns, the target is initially broken at the loader; call go_execution to let it run or get_stack to inspect."}, s.createProcess)
	addToolMaybe(s, server, &mcp.Tool{Name: "open_dump", Description: "Opens a Windows crash dump file as a passive target. Read-only: execution and breakpoints are inert. After this returns, call summarise_target for a high-level view or triage_crash for fault analysis."}, s.openDump)
	addToolMaybe(s, server, &mcp.Tool{Name: "attach_kernel", Description: "Attaches to a Windows kernel target over KDNET. Connection string is e.g. 'net:port=50000,key=W.X.Y.Z'. Long-running: pass timeout_seconds. Do NOT include target=... — that segment is for kdsrv routing and prevents the listener from starting. After this returns, call break_in then get_session_state."}, s.attachKernel)
	addToolMaybe(s, server, &mcp.Tool{Name: "detach", Description: "Detaches the engine from the current target without terminating it. The MCP session stays alive; you can attach to a different target next. Call get_session_state after to confirm no_target."}, s.detach)
	addToolMaybe(s, server, &mcp.Tool{Name: "connect_remote", Description: "Connects to a remote dbgsrv process server. Provide a connection string like 'tcp:port=12345,server=HOST'. After this returns, use the normal attach tools (attach_process, create_process) as if local."}, s.connectRemote)
	addToolMaybe(s, server, &mcp.Tool{Name: "disconnect_remote", Description: "Disconnects from the dbgsrv process server. Releases the remote-side handles; the engine reverts to local-only mode. Any active target attached over the process server must be detached first."}, s.disconnectRemote)

	// ---- Orientation and state ----
	addToolMaybe(s, server, &mcp.Tool{Name: "get_session_state", Description: "Returns a one-shot snapshot of session state: attached/running/broken, target kind, current thread, last event, pending ring sizes, and a recommended_next_tools array tailored to the current state. Call this first whenever you are uncertain what to do next."}, s.getSessionState)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_recent_events", Description: "Returns events recorded by the drainer (breakpoint, exception, module loaded, process exited, etc.) since since_token. Paged via since_token + limit (default 32, max 32). Use this when your client does not subscribe to gokd/event notifications."}, s.getRecentEvents)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_recent_output", Description: "Returns engine output lines recorded by the drainer since since_token. Paged via since_token + limit (default 64, max 256). Same fallback role as get_recent_events for clients without gokd/output notifications."}, s.getRecentOutput)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_modules", Description: "Lists loaded modules. Requires a target. Supports name_glob filter (e.g. 'nt!*'), offset, and limit (default 100, max 500). Use the returned next_offset for pagination."}, s.getModules)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_threads", Description: "Lists all threads in the target with engine IDs and system IDs. Requires a target. Use set_thread to switch the engine's current thread before reading registers or stack."}, s.getThreads)
	addToolMaybe(s, server, &mcp.Tool{Name: "set_thread", Description: "Switches the engine's current thread by system thread ID. Affects what get_stack and get_registers return. Requires the target to be broken in."}, s.setThread)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_registers", Description: "Returns all named registers for the current thread (or a filtered subset by name). Requires the target to be broken in. Use set_thread first if you need a different thread's registers."}, s.getRegisters)
	addToolMaybe(s, server, &mcp.Tool{Name: "set_register", Description: "Sets a register value on the current thread. Requires the target to be broken in. Mutates target state — use only when steering execution; otherwise read with get_registers."}, s.setRegister)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_stack", Description: "Returns the call stack of the current thread. Requires the target to be broken in. Default cap 32 frames; pass max_frames up to 256. Truncated indicates the unwinder produced more frames than were returned."}, s.getStack)
	addToolMaybe(s, server, &mcp.Tool{Name: "walk_stacks_all", Description: "Returns call stacks for every thread (paged). Requires the target to be broken in. Caps: max_threads default 64 (max 256), max_frames default 32 (max 256). Best-effort restores current thread. Use this when triaging a hang."}, s.walkStacksAll)
	addToolMaybe(s, server, &mcp.Tool{Name: "read_memory", Description: "Reads up to 4096 bytes of virtual memory at address. Requires a target. Returns hex-encoded bytes. Call repeatedly with address+length to read larger ranges."}, s.readMemory)
	addToolMaybe(s, server, &mcp.Tool{Name: "write_memory", Description: "Writes hex-encoded bytes to virtual memory at address. Requires the target to be broken in. Mutates target memory — use only when you intend to alter target state."}, s.writeMemory)
	addToolMaybe(s, server, &mcp.Tool{Name: "read_physical", Description: "Reads up to 4096 bytes of physical memory. Kernel targets only. Returns hex-encoded bytes. User-mode targets fail with HRESULT 0x80004001 (E_NOTIMPL)."}, s.readPhysical)
	addToolMaybe(s, server, &mcp.Tool{Name: "read_string", Description: "Reads a NUL-terminated string at address. encoding is 'ansi'|'utf16le'|'auto' (default auto). Default cap 256 bytes, max 4096. truncated=true when no NUL was found within the budget."}, s.readString)
	addToolMaybe(s, server, &mcp.Tool{Name: "dump_memory", Description: "Returns a kd-style hexdump (16 bytes per row, address + grouped hex + ASCII gutter) of virtual or physical memory. width is 1/2/4/8 bytes per group (default 1). Hard cap 4096 bytes. Set physical=true for kernel-mode physical reads."}, s.dumpMemory)
	addToolMaybe(s, server, &mcp.Tool{Name: "disassemble", Description: "Disassembles up to count instructions starting at address. Default 8, max 1024. Requires symbols for best results (function names appear inline). truncated indicates the cap was applied."}, s.disassemble)
	addToolMaybe(s, server, &mcp.Tool{Name: "name_to_addr", Description: "Resolves a symbol name (e.g. 'notepad!WinMain') to a virtual address. Returns NOT_FOUND if unresolved; try reload_symbols or sym_fix first to load matching PDBs."}, s.nameToAddr)
	addToolMaybe(s, server, &mcp.Tool{Name: "addr_to_name", Description: "Resolves an address to the nearest symbol and displacement (e.g. {symbol:'kernel32!CreateFileW', displacement:0x10}). Requires loaded symbols; otherwise returns the raw address."}, s.addrToName)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_type_fields", Description: "Returns the field layout (name, offset, type) of a type using DbgHelp (e.g. 'nt!_EPROCESS'). Use inspect_struct to get the actual field values at an address. Hard cap 256 fields."}, s.getTypeFields)
	addToolMaybe(s, server, &mcp.Tool{Name: "inspect_struct", Description: "Returns a structured field-by-field dump of an instance of a type at address. type_name must be qualified as 'module!type'. Optional recurse follows nested struct types up to depth 3. Total fields hard-capped at 1024 across all levels."}, s.inspectStruct)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_type_size", Description: "Returns the size in bytes of a type (e.g. 'nt!_EPROCESS'). Requires DbgHelp symbol information for the type. Useful for offset arithmetic and sanity-checks before inspect_struct."}, s.getTypeSize)
	addToolMaybe(s, server, &mcp.Tool{Name: "add_breakpoint", Description: "Sets a code breakpoint at an address or symbol expression (e.g. 'nt!NtCreateFile'). Returns the assigned breakpoint ID. Use list_breakpoints to inspect, remove_breakpoint to delete."}, s.addBreakpoint)
	addToolMaybe(s, server, &mcp.Tool{Name: "remove_breakpoint", Description: "Deletes a breakpoint by ID. Returns ok=true. Use list_breakpoints first to discover IDs; enable_breakpoint with enabled=false is a non-destructive alternative."}, s.removeBreakpoint)
	addToolMaybe(s, server, &mcp.Tool{Name: "enable_breakpoint", Description: "Enables or disables a breakpoint by ID without removing it. Disabled breakpoints stay registered and keep their hit count; re-enable with enabled=true."}, s.enableBreakpoint)
	addToolMaybe(s, server, &mcp.Tool{Name: "list_breakpoints", Description: "Lists every active breakpoint with ID, address, hit count, enabled flag, thread filter, and attached command. Requires a target. Use add_breakpoint to install new ones."}, s.listBreakpoints)
	addToolMaybe(s, server, &mcp.Tool{Name: "go_execution", Description: "Resumes the target and blocks until it next breaks or timeout_seconds elapses (default 0 = wait forever). Cancellable via break_in. After return, inspect the StopEvent or call get_session_state."}, s.goExecution)
	addToolMaybe(s, server, &mcp.Tool{Name: "step_in", Description: "Steps a single instruction or source line, stepping into calls. Requires the target to be broken in. Returns the StopEvent. Use step_over to skip call instructions."}, s.stepIn)
	addToolMaybe(s, server, &mcp.Tool{Name: "step_over", Description: "Steps a single instruction or source line, stepping over calls. Requires the target to be broken in. Returns the StopEvent. Use step_in to descend into calls."}, s.stepOver)
	addToolMaybe(s, server, &mcp.Tool{Name: "step_out", Description: "Runs the target until the current function returns. Requires the target to be broken in. Returns the StopEvent at the return site. Useful for backing out of helpers."}, s.stepOut)
	addToolMaybe(s, server, &mcp.Tool{Name: "break_in", Description: "Interrupts a running target and forces it to break. Thread-safe; can be called concurrently with go_execution to cancel a long-running resume. After return, call get_session_state to confirm broken_in."}, s.breakIn)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_symbol_path", Description: "Returns the engine's current symbol search path (sympath). Standard form: 'srv*C:\\sym*https://msdl.microsoft.com/download/symbols'. Use set_symbol_path to change."}, s.getSymbolPath)
	addToolMaybe(s, server, &mcp.Tool{Name: "set_symbol_path", Description: "Sets the engine's symbol search path. Standard form: 'srv*C:\\sym*https://msdl.microsoft.com/download/symbols'. After this, call reload_symbols to make it effective."}, s.setSymbolPath)
	if !s.readonly || s.unsafeRaw {
		execAnnot := &mcp.ToolAnnotations{}
		dt := true
		execAnnot.DestructiveHint = &dt
		ow := true
		execAnnot.OpenWorldHint = &ow
		mcp.AddTool(server, &mcp.Tool{Name: "execute_raw", Description: "Executes an arbitrary kd command (e.g. '!process 0 0'). WARNING: prefer typed tools when available. Output is capped at 16 KiB inline; if the command produced more, the response sets truncated=true and continuation_token, which the caller passes to get_raw_output_continuation. Disabled in --readonly mode unless --unsafe-raw is also set.", Annotations: execAnnot}, s.executeRaw)
	}
	addToolMaybe(s, server, &mcp.Tool{Name: "get_raw_output_continuation", Description: "Returns the remainder of an execute_raw response that exceeded the 16 KiB inline cap. offset (default 0) and limit (default 16384, max 16384) control the byte window. Returns expired=true once the LRU evicts the entry (8 entries / 512 KiB total)."}, s.getRawContinuation)

	// --- t1-4 symbol reload / status ---
	addToolMaybe(s, server, &mcp.Tool{Name: "reload_symbols", Description: "Forces the engine to reload symbols for one module or all modules via IDebugSymbols3::ReloadWide. spec is forwarded verbatim ('', '/f', '/f <module>'). May download from the symbol server — pass timeout_seconds (default 0 = no timeout) to bound the wait. Use after set_symbol_path."}, s.reloadSymbols)
	addToolMaybe(s, server, &mcp.Tool{Name: "sym_fix", Description: "Installs the standard Microsoft public symbol server into the sympath (mirrors WinDbg .symfix). Pass an optional cache directory; empty uses a per-user default. Convenience for fresh sessions. Follow with reload_symbols to actually pull PDBs."}, s.symFix)

	// --- t1-1 evaluate ---
	addToolMaybe(s, server, &mcp.Tool{Name: "evaluate", Description: "Evaluates a MASM or C++ expression in the engine's current context (e.g. 'nt!KiSystemServiceStart+0x40', 'sizeof(_EPROCESS)'). Affected by set_radix and set_expression_syntax. May stall on PDB downloads — pass timeout_seconds. Returns type, u64, f64, raw_hex, and remainder."}, s.evaluate)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_radix", Description: "Returns the engine's current numeric radix (typically 10 or 16). Affects how evaluate parses literals and how DbgEng formats numbers in output. Most workflows want 16."}, s.getRadix)
	addToolMaybe(s, server, &mcp.Tool{Name: "set_radix", Description: "Sets the engine's numeric radix to 10 or 16. Most workflows want 16. Mutates engine state — affects subsequent evaluate calls and DbgEng formatting."}, s.setRadix)
	addToolMaybe(s, server, &mcp.Tool{Name: "get_expression_syntax", Description: "Returns the current expression syntax ('masm' or 'cpp'). Affects evaluate, breakpoint addresses, and dt-style commands. C++ is needed for scope-resolved names like MyClass::Method."}, s.getExpressionSyntax)
	addToolMaybe(s, server, &mcp.Tool{Name: "set_expression_syntax", Description: "Switches the expression syntax to 'masm' (DbgEng default) or 'cpp'. Mutates engine state. C++ is needed for scope-resolved names like MyClass::Method; otherwise prefer MASM."}, s.setExpressionSyntax)

	// --- t1-3 source lines ---
	addToolMaybe(s, server, &mcp.Tool{Name: "addr_to_line", Description: "Returns the source file, line, and displacement containing address via IDebugSymbols3::GetLineByOffsetWide. Requires private (line-info) PDBs. Errors with HRESULT 0x80000002 (E_NOTFOUND) if no line info is loaded — install matching PDBs or run reload_symbols."}, s.addrToLine)
	addToolMaybe(s, server, &mcp.Tool{Name: "line_to_addr", Description: "Returns the instruction address corresponding to a source file and line via IDebugSymbols3::GetOffsetByLineWide. The file path must match the canonical absolute path the PDB was built with; partial matches fail with E_NOTFOUND."}, s.lineToAddr)
	addToolMaybe(s, server, &mcp.Tool{Name: "add_breakpoint_source_line", Description: "Resolves a source file and line to an address and installs a code breakpoint there. Requires line-info PDBs for the target binary; otherwise fails with E_NOTFOUND. Returns the assigned breakpoint ID."}, s.addBreakpointSourceLine)

	// --- t1-6 memory search / translate / query ---
	addToolMaybe(s, server, &mcp.Tool{Name: "search_memory", Description: "Searches [start_hex, start_hex+length) for pattern_hex via IDebugDataSpaces4::SearchVirtual. granularity must be 1, 4, or 8 (default 1). Returns {found:false, match_hex:''} when absent so callers can loop. Keep length small (<=4 KB) — SearchVirtual is slow on large ranges."}, s.searchMemory)
	addToolMaybe(s, server, &mcp.Tool{Name: "virtual_to_physical", Description: "Translates a virtual address to a physical address via IDebugDataSpaces4::VirtualToPhysical. Kernel-mode sessions only; user-mode targets fail with HRESULT 0x80004001 (E_NOTIMPL) or similar."}, s.virtualToPhysical)
	addToolMaybe(s, server, &mcp.Tool{Name: "query_region", Description: "Returns the MEMORY_BASIC_INFORMATION64 record covering va_hex via IDebugDataSpaces4::QueryVirtual. Fields use raw Windows numerics: state (MEM_COMMIT=0x1000, MEM_RESERVE=0x2000, MEM_FREE=0x10000), type (MEM_PRIVATE=0x20000, MEM_MAPPED=0x40000, MEM_IMAGE=0x1000000), protect (PAGE_* flags)."}, s.queryRegion)

	// --- t1-2 write dump ---
	addToolMaybe(s, server, &mcp.Tool{Name: "write_dump", Description: "Snapshots the current target to a .dmp file via IDebugClient5::WriteDumpFileWide. path must be absolute. kind is 'small'|'default'|'full' (default 'default'). flags is the raw DEBUG_FORMAT_USER_SMALL_* bitmask. Synchronous and uncancellable mid-call; default timeout_seconds is 600."}, s.writeDump)

	// --- t1-5 data + conditional breakpoints ---
	addToolMaybe(s, server, &mcp.Tool{Name: "add_data_breakpoint", Description: "Installs a hardware ('break-on-access') data breakpoint at address_hex covering size bytes. size must be 1/2/4/8. access is any non-empty subset of ['read','write','execute','io']. x64 supports at most four enabled data breakpoints concurrently — the fifth fails at the next go_execution."}, s.addDataBreakpoint)
	addToolMaybe(s, server, &mcp.Tool{Name: "configure_breakpoint", Description: "Applies non-positional configuration (pass count, thread filter, WinDbg command) to an existing breakpoint without recreating it. Each field is independently optional: pass_count=0 leaves alone; omit match_thread_id to leave alone or pass 0xFFFFFFFF for 'any thread'; omit command to leave alone."}, s.configureBreakpoint)
	addToolMaybe(s, server, &mcp.Tool{Name: "breakpoint_command", Description: "Returns the WinDbg command string attached to a breakpoint by ID (empty if none was set via configure_breakpoint). Use to audit conditional breakpoints before resuming execution."}, s.breakpointCommand)

	// --- t1-8 last event / bugcheck ---
	addToolMaybe(s, server, &mcp.Tool{Name: "last_exception", Description: "Returns the most recent DEBUG_EVENT_EXCEPTION record reported by DbgEng (the .lastevent / .exr surface). Returns {found:false} when the last event was not an exception — e.g. an attach breakpoint or a process-exit notification. parameters carries raw EXCEPTION_RECORD.ExceptionInformation values."}, s.lastException)
	addToolMaybe(s, server, &mcp.Tool{Name: "bug_check", Description: "Reads the kernel bugcheck record via IDebugControl4::ReadBugCheckData. Kernel-mode sessions only — user-mode targets return {found:false}. name and description are best-effort lookups for ~20 common codes; unknown codes still surface the raw code and four args."}, s.bugCheck)

	// ---- Workflow composites ----
	addToolMaybe(s, server, &mcp.Tool{Name: "triage_crash", Description: "One-shot crash triage: returns bug check (kernel) and/or last exception, faulting thread/frame, stack (capped by max_frames, default 32), all registers, ~11 instructions of nearby disassembly around the faulting RIP, the module containing the fault, and a human-readable summary. Call this first on any unfamiliar dump or freshly broken-in target."}, s.triageCrash)
	addToolMaybe(s, server, &mcp.Tool{Name: "summarise_target", Description: "Returns a narrative summary of the target: full get_session_state, the first 20 modules, up to 32 threads, and a one-liner preview of any last exception or bug check. Use this immediately after open_dump or attach_kernel to orient."}, s.summariseTarget)


	// --- t1-7 dump type ---
	addToolMaybe(s, server, &mcp.Tool{Name: "dump_type", Description: "Walks a typed object recursively (the 'dt -r' surface). Resolves type in module's symbol namespace, reads address_hex as that type, and recurses into struct fields up to max_depth levels (default 3). follow_ptrs dereferences non-NULL pointer fields one extra level with cycle detection. Special decoders surface _UNICODE_STRING (string), _LIST_ENTRY, GUID, _LARGE_INTEGER."}, s.dumpType)

	// --- t4-1 / t4-2 lablink-backed composite tools ---
	// Both gated by -lablink-enabled (env GOKD_MCP_LABLINK). Default off so
	// installations without a lablink registry do not advertise tools that
	// cannot work. setup_kernel_debug is destructive (mutatingTools) so it
	// is additionally suppressed under --readonly via addToolMaybe.
	if s.lablinkEnabled {
		addToolMaybe(s, server, &mcp.Tool{Name: "setup_kernel_debug", Description: "Configures a remote lablink node for KDNET kernel debugging and reboots it. Runs 'bcdedit /dbgsettings net hostip:HOST port:PORT key:KEY' and 'bcdedit /debug on' on the node, reboots it, then waits up to timeout_seconds (default 300) for it to come back. REQUIRES confirm_reboot=true. Returns a connection_string suitable for attach_kernel. If attach_after=true and host is local to this gokd-mcp, the local engine is attached automatically."}, s.setupKernelDebug)
		addToolMaybe(s, server, &mcp.Tool{Name: "pull_latest_minidump", Description: "Fetches the most recent crash dump from a lablink node, copies it locally, and optionally opens it in the local engine. source defaults to 'minidump' (C:\\Windows\\Minidump\\*.dmp); set 'crashdumps' for C:\\Windows\\LiveKernelReports\\*.dmp. Returns found=false when no dump exists. Refuses files larger than max_bytes (default 1 GiB). When open_locally=true, also surfaces bug_check and last_exception in the summary."}, s.pullLatestMinidump)
	}

	registerPrompts(server)
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

func (s *srv) getModules(ctx context.Context, _ *mcp.CallToolRequest, in getModulesInput) (*mcp.CallToolResult, modulesOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[modulesOutput]("get_modules", err)
	}
	mods, err := s.sess.Modules()
	if err != nil {
		return toolErr[modulesOutput]("get_modules", err)
	}
	glob := strings.TrimSpace(in.NameGlob)
	if glob != "" {
		if _, err := filepath.Match(glob, ""); err != nil {
			return toolErr[modulesOutput]("get_modules",
				fmt.Errorf("invalid name_glob %q: %w", glob, err))
		}
	}
	filtered := make([]Module, 0, len(mods))
	for _, m := range mods {
		formatted := formatModule(m)
		if glob != "" {
			match, _ := filepath.Match(glob, formatted.Name)
			if !match {
				continue
			}
		}
		filtered = append(filtered, formatted)
	}
	total := len(filtered)
	limit := in.Limit
	if limit <= 0 {
		limit = defaultModulesLimit
	}
	if limit > maxModulesLimit {
		limit = maxModulesLimit
	}
	offset := in.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := filtered[offset:end]
	out := modulesOutput{
		Modules:  page,
		Total:    total,
		Returned: len(page),
	}
	if end < total {
		out.NextOffset = end
		out.Truncated = true
	}
	return nil, out, nil
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

func (s *srv) getStack(ctx context.Context, _ *mcp.CallToolRequest, in getStackInput) (*mcp.CallToolResult, stackOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[stackOutput]("get_stack", err)
	}
	frames, err := s.sess.Stack()
	if err != nil {
		return toolErr[stackOutput]("get_stack", err)
	}
	limit := in.MaxFrames
	if limit <= 0 {
		limit = defaultStackFrames
	}
	if limit > maxStackFrames {
		limit = maxStackFrames
	}
	truncated := false
	if len(frames) > limit {
		frames = frames[:limit]
		truncated = true
	}
	out := make([]Frame, len(frames))
	for i, f := range frames {
		out[i] = formatFrame(f)
	}
	return nil, stackOutput{Frames: out, Returned: len(out), Truncated: truncated}, nil
}

func (s *srv) readMemory(ctx context.Context, _ *mcp.CallToolRequest, in readMemoryInput) (*mcp.CallToolResult, hexOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[hexOutput]("read_memory", err)
	}
	addr, err := parseHexUint64(in.AddressHex, "address_hex")
	if err != nil {
		return toolErr[hexOutput]("read_memory", err)
	}
	length := in.Length
	truncated := false
	if length > maxReadMemoryBytes {
		length = maxReadMemoryBytes
		truncated = true
	}
	data, err := s.sess.ReadMemory(addr, length)
	if err != nil {
		return toolErr[hexOutput]("read_memory", err)
	}
	return nil, hexOutput{Hex: hex.EncodeToString(data), Length: len(data), Truncated: truncated}, nil
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
	length := in.Length
	truncated := false
	if length > maxReadMemoryBytes {
		length = maxReadMemoryBytes
		truncated = true
	}
	data, err := s.sess.ReadPhysical(addr, length)
	if err != nil {
		return toolErr[hexOutput]("read_physical", err)
	}
	return nil, hexOutput{Hex: hex.EncodeToString(data), Length: len(data), Truncated: truncated}, nil
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
	requested := count
	if count > maxDisassembleCount {
		count = maxDisassembleCount
	}
	ins, err := s.sess.DisassembleRange(addr, count)
	if err != nil {
		return toolErr[disassembleOutput]("disassemble", err)
	}
	out := make([]Instruction, len(ins))
	for i, inst := range ins {
		out[i] = formatInstruction(inst)
	}
	return nil, disassembleOutput{
		Instructions: out,
		Returned:     len(out),
		Truncated:    requested > maxDisassembleCount,
	}, nil
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
	truncated := false
	if len(fields) > maxTypeFields {
		fields = fields[:maxTypeFields]
		truncated = true
	}
	out := make([]Field, len(fields))
	for i, f := range fields {
		out[i] = formatField(f)
	}
	return nil, fieldsOutput{Fields: out, Returned: len(out), Truncated: truncated}, nil
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
	return nil, s.packExecuteRawOutput(out), nil
}

// packExecuteRawOutput splits a possibly oversized command output into an
// inline 16 KiB head plus an LRU-stored continuation tail; the caller pulls
// the tail via get_raw_output_continuation.
func (s *srv) packExecuteRawOutput(out string) executeRawOutput {
	resp := executeRawOutput{Bytes: len(out)}
	if len(out) <= rawInlineCapBytes {
		resp.Output = out
		return resp
	}
	resp.Output = out[:rawInlineCapBytes]
	resp.Truncated = true
	if s.rawOverflow == nil {
		// No backing store -- still surface the truncation flag so the
		// caller knows bytes were dropped.
		return resp
	}
	tail := []byte(out[rawInlineCapBytes:])
	token, err := newContinuationToken()
	if err != nil {
		return resp
	}
	s.rawOverflow.Put(token, tail)
	resp.ContinuationToken = token
	return resp
}

func newContinuationToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *srv) getRawContinuation(ctx context.Context, _ *mcp.CallToolRequest, in getRawContinuationInput) (*mcp.CallToolResult, getRawContinuationOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[getRawContinuationOutput]("get_raw_output_continuation", err)
	}
	if strings.TrimSpace(in.Token) == "" {
		return toolErr[getRawContinuationOutput]("get_raw_output_continuation",
			fmt.Errorf("token is required"))
	}
	if s.rawOverflow == nil {
		return nil, getRawContinuationOutput{Expired: true}, nil
	}
	data, ok := s.rawOverflow.Get(in.Token)
	if !ok {
		return nil, getRawContinuationOutput{Expired: true}, nil
	}
	offset := in.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > len(data) {
		offset = len(data)
	}
	limit := in.Limit
	if limit <= 0 || limit > rawContinuationMaxBytes {
		limit = rawContinuationMaxBytes
	}
	end := offset + limit
	if end > len(data) {
		end = len(data)
	}
	chunk := data[offset:end]
	out := getRawContinuationOutput{Output: string(chunk)}
	if end < len(data) {
		out.NextOffset = end
		out.Truncated = true
	}
	return nil, out, nil
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
