package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nijosmsft/gokd"
)

type srv struct{ sess gokd.Session }

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

func toolErr[Out any](err error) (*mcp.CallToolResult, Out, error) {
	var zero Out
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, zero, nil
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
	mcp.AddTool(server, &mcp.Tool{Name: "attach_process", Description: "Attach to a running user-mode process by process ID."}, s.attachProcess)
	mcp.AddTool(server, &mcp.Tool{Name: "create_process", Description: "Create a user-mode process under the debugger."}, s.createProcess)
	mcp.AddTool(server, &mcp.Tool{Name: "open_dump", Description: "Open a crash dump file as the current target."}, s.openDump)
	mcp.AddTool(server, &mcp.Tool{Name: "attach_kernel", Description: "Attach to a kernel target using a DbgEng connection string."}, s.attachKernel)
	mcp.AddTool(server, &mcp.Tool{Name: "detach", Description: "Detach from the current target."}, s.detach)
	mcp.AddTool(server, &mcp.Tool{Name: "connect_remote", Description: "Connect to a DbgEng dbgsrv process server."}, s.connectRemote)
	mcp.AddTool(server, &mcp.Tool{Name: "disconnect_remote", Description: "Disconnect from the current DbgEng process server."}, s.disconnectRemote)
	mcp.AddTool(server, &mcp.Tool{Name: "get_modules", Description: "List all modules loaded in the current target. Returns base address, size, and name for each module."}, s.getModules)
	mcp.AddTool(server, &mcp.Tool{Name: "get_threads", Description: "List threads in the current target."}, s.getThreads)
	mcp.AddTool(server, &mcp.Tool{Name: "set_thread", Description: "Set the current thread by system thread ID."}, s.setThread)
	mcp.AddTool(server, &mcp.Tool{Name: "get_registers", Description: "Read registers from the current thread; optionally filter by register name."}, s.getRegisters)
	mcp.AddTool(server, &mcp.Tool{Name: "set_register", Description: "Set a register value in the current thread."}, s.setRegister)
	mcp.AddTool(server, &mcp.Tool{Name: "get_stack", Description: "Return the current thread stack frames with symbol information."}, s.getStack)
	mcp.AddTool(server, &mcp.Tool{Name: "read_memory", Description: "Read virtual memory and return hex-encoded bytes."}, s.readMemory)
	mcp.AddTool(server, &mcp.Tool{Name: "write_memory", Description: "Write hex-encoded bytes to virtual memory."}, s.writeMemory)
	mcp.AddTool(server, &mcp.Tool{Name: "read_physical", Description: "Read physical memory and return hex-encoded bytes."}, s.readPhysical)
	mcp.AddTool(server, &mcp.Tool{Name: "disassemble", Description: "Disassemble instructions starting at an address."}, s.disassemble)
	mcp.AddTool(server, &mcp.Tool{Name: "name_to_addr", Description: "Resolve a symbol name to an address."}, s.nameToAddr)
	mcp.AddTool(server, &mcp.Tool{Name: "addr_to_name", Description: "Resolve an address to a symbol name and displacement."}, s.addrToName)
	mcp.AddTool(server, &mcp.Tool{Name: "get_type_fields", Description: "List fields for a type using DbgHelp symbol information."}, s.getTypeFields)
	mcp.AddTool(server, &mcp.Tool{Name: "get_type_size", Description: "Return the size of a type using DbgHelp symbol information."}, s.getTypeSize)
	mcp.AddTool(server, &mcp.Tool{Name: "add_breakpoint", Description: "Add a breakpoint by address or symbol expression."}, s.addBreakpoint)
	mcp.AddTool(server, &mcp.Tool{Name: "remove_breakpoint", Description: "Remove a breakpoint by ID."}, s.removeBreakpoint)
	mcp.AddTool(server, &mcp.Tool{Name: "enable_breakpoint", Description: "Enable or disable a breakpoint by ID."}, s.enableBreakpoint)
	mcp.AddTool(server, &mcp.Tool{Name: "list_breakpoints", Description: "List breakpoints in the current target."}, s.listBreakpoints)
	mcp.AddTool(server, &mcp.Tool{Name: "go_execution", Description: "Continue target execution until a stop event or timeout. Long running."}, s.goExecution)
	mcp.AddTool(server, &mcp.Tool{Name: "step_in", Description: "Step into one instruction or source line and return the stop event."}, s.stepIn)
	mcp.AddTool(server, &mcp.Tool{Name: "step_over", Description: "Step over one instruction or source line and return the stop event."}, s.stepOver)
	mcp.AddTool(server, &mcp.Tool{Name: "step_out", Description: "Step out of the current function and return the stop event."}, s.stepOut)
	mcp.AddTool(server, &mcp.Tool{Name: "break_in", Description: "Interrupt a running target or long-running go_execution call."}, s.breakIn)
	mcp.AddTool(server, &mcp.Tool{Name: "get_symbol_path", Description: "Get the current debugger symbol path."}, s.getSymbolPath)
	mcp.AddTool(server, &mcp.Tool{Name: "set_symbol_path", Description: "Set the debugger symbol path."}, s.setSymbolPath)
	mcp.AddTool(server, &mcp.Tool{Name: "execute_raw", Description: "WARNING: arbitrary dbgeng command. Use only when other tools are insufficient."}, s.executeRaw)

	// --- t1-4 symbol reload / status ---
	mcp.AddTool(server, &mcp.Tool{Name: "reload_symbols", Description: "Force a symbol reload via IDebugSymbols3::ReloadWide. Use when get_modules reports a deferred symbol_type or after changing set_symbol_path. spec is forwarded verbatim ('', '/f', '/f <module>'). May download from the symbol server — supply timeout_seconds (default 0 = no timeout) to bound the wait. Returns ok=true on success."}, s.reloadSymbols)
	mcp.AddTool(server, &mcp.Tool{Name: "sym_fix", Description: "Configure the symbol path to the standard Microsoft public-symbol-server cache layout (mirrors WinDbg .symfix). Pass an optional cache directory; empty uses a per-user default. Returns ok=true. Follow with reload_symbols to actually pull PDBs."}, s.symFix)

	// --- t1-1 evaluate ---
	mcp.AddTool(server, &mcp.Tool{Name: "evaluate", Description: "Evaluate a MASM or C++ expression via IDebugControl4::EvaluateWide. Use when you need a typed value from a symbol/arithmetic expression like 'nt!KiSystemServiceStart+0x40' or 'sizeof(_EPROCESS)'. Default syntax is MASM; switch with set_expression_syntax for C++ scope-resolution. May stall on PDB downloads — supply timeout_seconds. Returns type (string), u64 (integer slot), f64 (float slot), raw_hex (24-byte DEBUG_VALUE tail), and remainder (byte index where parsing stopped)."}, s.evaluate)
	mcp.AddTool(server, &mcp.Tool{Name: "get_radix", Description: "Return the current numeric radix used by evaluate and DbgEng formatting (typically 10 or 16)."}, s.getRadix)
	mcp.AddTool(server, &mcp.Tool{Name: "set_radix", Description: "Set the numeric radix used by evaluate and DbgEng formatting. Typical values: 10, 16."}, s.setRadix)
	mcp.AddTool(server, &mcp.Tool{Name: "get_expression_syntax", Description: "Return the current expression-parser syntax ('masm' or 'cpp')."}, s.getExpressionSyntax)
	mcp.AddTool(server, &mcp.Tool{Name: "set_expression_syntax", Description: "Switch the expression-parser syntax. Accepts 'masm' or 'cpp'. C++ is needed for scope-resolved names like MyClass::Method."}, s.setExpressionSyntax)
}

func (s *srv) attachProcess(ctx context.Context, _ *mcp.CallToolRequest, in attachProcessInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput](err)
	}
	if err := s.sess.AttachProcess(in.PID, gokd.AttachDefault); err != nil {
		return toolErr[okOutput](err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) createProcess(ctx context.Context, _ *mcp.CallToolRequest, in createProcessInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput](err)
	}
	if strings.TrimSpace(in.CommandLine) == "" {
		return toolErr[okOutput](fmt.Errorf("command_line is required"))
	}
	if err := s.sess.CreateProcess(in.CommandLine, gokd.CreateOptions{Flags: 1, InitialBreak: in.InitialBreak}); err != nil {
		return toolErr[okOutput](err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) openDump(ctx context.Context, _ *mcp.CallToolRequest, in pathInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput](err)
	}
	if strings.TrimSpace(in.Path) == "" {
		return toolErr[okOutput](fmt.Errorf("path is required"))
	}
	if err := s.sess.OpenDump(in.Path); err != nil {
		return toolErr[okOutput](err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) attachKernel(ctx context.Context, _ *mcp.CallToolRequest, in attachKernelInput) (*mcp.CallToolResult, okOutput, error) {
	ctx, cancel, err := contextWithSeconds(ctx, in.TimeoutSeconds)
	if err != nil {
		return toolErr[okOutput](err)
	}
	defer cancel()
	opts := gokd.KernelDefault
	if in.Passive {
		opts = gokd.KernelPassive
	}
	if strings.TrimSpace(in.ConnectionString) == "" {
		return toolErr[okOutput](fmt.Errorf("connection_string is required"))
	}
	if err := s.sess.AttachKernel(ctx, in.ConnectionString, opts); err != nil {
		return toolErr[okOutput](err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) detach(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput](err)
	}
	if err := s.sess.Detach(); err != nil {
		return toolErr[okOutput](err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) connectRemote(ctx context.Context, _ *mcp.CallToolRequest, in connectionInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput](err)
	}
	if strings.TrimSpace(in.Connection) == "" {
		return toolErr[okOutput](fmt.Errorf("connection is required"))
	}
	if err := s.sess.ConnectRemote(in.Connection); err != nil {
		return toolErr[okOutput](err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) disconnectRemote(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput](err)
	}
	if err := s.sess.DisconnectRemote(); err != nil {
		return toolErr[okOutput](err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) getModules(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, modulesOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[modulesOutput](err)
	}
	mods, err := s.sess.Modules()
	if err != nil {
		return toolErr[modulesOutput](err)
	}
	out := make([]Module, len(mods))
	for i, m := range mods {
		out[i] = formatModule(m)
	}
	return nil, modulesOutput{Modules: out}, nil
}

func (s *srv) getThreads(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, threadsOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[threadsOutput](err)
	}
	threads, err := s.sess.Threads()
	if err != nil {
		return toolErr[threadsOutput](err)
	}
	out := make([]Thread, len(threads))
	for i, t := range threads {
		out[i] = formatThread(t)
	}
	return nil, threadsOutput{Threads: out}, nil
}

func (s *srv) setThread(ctx context.Context, _ *mcp.CallToolRequest, in setThreadInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput](err)
	}
	if err := s.sess.SetThread(in.SystemID); err != nil {
		return toolErr[okOutput](err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) getRegisters(ctx context.Context, _ *mcp.CallToolRequest, in getRegistersInput) (*mcp.CallToolResult, registersOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[registersOutput](err)
	}
	regs, err := s.sess.Registers()
	if err != nil {
		return toolErr[registersOutput](err)
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
		return toolErr[okOutput](err)
	}
	value, err := strconv.ParseUint(strings.TrimSpace(in.ValueHex), 0, 64)
	if err != nil {
		return toolErr[okOutput](fmt.Errorf("invalid value_hex: %w", err))
	}
	if err := s.sess.SetRegister(in.Name, value); err != nil {
		return toolErr[okOutput](err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) getStack(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, stackOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[stackOutput](err)
	}
	frames, err := s.sess.Stack()
	if err != nil {
		return toolErr[stackOutput](err)
	}
	out := make([]Frame, len(frames))
	for i, f := range frames {
		out[i] = formatFrame(f)
	}
	return nil, stackOutput{Frames: out}, nil
}

func (s *srv) readMemory(ctx context.Context, _ *mcp.CallToolRequest, in readMemoryInput) (*mcp.CallToolResult, hexOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[hexOutput](err)
	}
	addr, err := parseHexUint64(in.AddressHex, "address_hex")
	if err != nil {
		return toolErr[hexOutput](err)
	}
	data, err := s.sess.ReadMemory(addr, in.Length)
	if err != nil {
		return toolErr[hexOutput](err)
	}
	return nil, hexOutput{Hex: hex.EncodeToString(data)}, nil
}

func (s *srv) writeMemory(ctx context.Context, _ *mcp.CallToolRequest, in writeMemoryInput) (*mcp.CallToolResult, writeMemoryOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[writeMemoryOutput](err)
	}
	addr, err := parseHexUint64(in.AddressHex, "address_hex")
	if err != nil {
		return toolErr[writeMemoryOutput](err)
	}
	data, err := hex.DecodeString(strings.TrimSpace(in.Hex))
	if err != nil {
		return toolErr[writeMemoryOutput](fmt.Errorf("invalid hex: %w", err))
	}
	if err := s.sess.WriteMemory(addr, data); err != nil {
		return toolErr[writeMemoryOutput](err)
	}
	return nil, writeMemoryOutput{OK: true, BytesWritten: len(data)}, nil
}

func (s *srv) readPhysical(ctx context.Context, _ *mcp.CallToolRequest, in readPhysicalInput) (*mcp.CallToolResult, hexOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[hexOutput](err)
	}
	addr, err := parseHexUint64(in.AddressHex, "address_hex")
	if err != nil {
		return toolErr[hexOutput](err)
	}
	data, err := s.sess.ReadPhysical(addr, in.Length)
	if err != nil {
		return toolErr[hexOutput](err)
	}
	return nil, hexOutput{Hex: hex.EncodeToString(data)}, nil
}

func (s *srv) disassemble(ctx context.Context, _ *mcp.CallToolRequest, in disassembleInput) (*mcp.CallToolResult, disassembleOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[disassembleOutput](err)
	}
	addr, err := parseHexUint64(in.AddressHex, "address_hex")
	if err != nil {
		return toolErr[disassembleOutput](err)
	}
	count := in.Count
	if count == 0 {
		count = 8
	}
	if count < 0 {
		return toolErr[disassembleOutput](fmt.Errorf("count must be >= 0"))
	}
	ins, err := s.sess.DisassembleRange(addr, count)
	if err != nil {
		return toolErr[disassembleOutput](err)
	}
	out := make([]Instruction, len(ins))
	for i, inst := range ins {
		out[i] = formatInstruction(inst)
	}
	return nil, disassembleOutput{Instructions: out}, nil
}

func (s *srv) nameToAddr(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, nameToAddrOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[nameToAddrOutput](err)
	}
	addr, err := s.sess.NameToAddr(in.Name)
	if err != nil {
		return toolErr[nameToAddrOutput](err)
	}
	return nil, nameToAddrOutput{AddressHex: hex64(addr)}, nil
}

func (s *srv) addrToName(ctx context.Context, _ *mcp.CallToolRequest, in addrInput) (*mcp.CallToolResult, addrToNameOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[addrToNameOutput](err)
	}
	addr, err := parseHexUint64(in.AddressHex, "address_hex")
	if err != nil {
		return toolErr[addrToNameOutput](err)
	}
	name, disp, err := s.sess.AddrToName(addr)
	if err != nil {
		return toolErr[addrToNameOutput](err)
	}
	return nil, addrToNameOutput{Name: name, Displacement: disp}, nil
}

func (s *srv) getTypeFields(ctx context.Context, _ *mcp.CallToolRequest, in typeInput) (*mcp.CallToolResult, fieldsOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[fieldsOutput](err)
	}
	fields, err := s.sess.TypeFields(in.Module, in.TypeName)
	if err != nil {
		return toolErr[fieldsOutput](err)
	}
	out := make([]Field, len(fields))
	for i, f := range fields {
		out[i] = formatField(f)
	}
	return nil, fieldsOutput{Fields: out}, nil
}

func (s *srv) getTypeSize(ctx context.Context, _ *mcp.CallToolRequest, in typeInput) (*mcp.CallToolResult, typeSizeOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[typeSizeOutput](err)
	}
	size, err := s.sess.TypeSize(in.Module, in.TypeName)
	if err != nil {
		return toolErr[typeSizeOutput](err)
	}
	return nil, typeSizeOutput{Size: size}, nil
}

func (s *srv) addBreakpoint(ctx context.Context, _ *mcp.CallToolRequest, in addBreakpointInput) (*mcp.CallToolResult, addBreakpointOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[addBreakpointOutput](err)
	}
	hasAddr := strings.TrimSpace(in.AddressHex) != ""
	hasSym := strings.TrimSpace(in.Symbol) != ""
	if hasAddr == hasSym {
		return toolErr[addBreakpointOutput](fmt.Errorf("set exactly one of address_hex or symbol"))
	}
	var (
		bp   *gokd.Breakpoint
		err  error
		addr uint64
	)
	if hasAddr {
		addr, err = parseHexUint64(in.AddressHex, "address_hex")
		if err != nil {
			return toolErr[addBreakpointOutput](err)
		}
		bp, err = s.sess.AddBreakpoint(addr)
	} else {
		bp, err = s.sess.AddBreakpointSym(in.Symbol)
	}
	if err != nil {
		return toolErr[addBreakpointOutput](err)
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
		return toolErr[okOutput](err)
	}
	if err := s.sess.RemoveBreakpoint(in.ID); err != nil {
		return toolErr[okOutput](err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) enableBreakpoint(ctx context.Context, _ *mcp.CallToolRequest, in enableBreakpointInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput](err)
	}
	if err := s.sess.EnableBreakpoint(in.ID, in.Enabled); err != nil {
		return toolErr[okOutput](err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) listBreakpoints(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, breakpointsOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[breakpointsOutput](err)
	}
	bps, err := s.sess.Breakpoints()
	if err != nil {
		return toolErr[breakpointsOutput](err)
	}
	out := make([]Breakpoint, len(bps))
	for i, bp := range bps {
		out[i] = formatBreakpoint(bp)
	}
	return nil, breakpointsOutput{Breakpoints: out}, nil
}

func (s *srv) runExecution(ctx context.Context, in executionInput, fn func(context.Context) (*gokd.StopEvent, error)) (*mcp.CallToolResult, stopOutput, error) {
	ctx, cancel, err := contextWithSeconds(ctx, in.TimeoutSeconds)
	if err != nil {
		return toolErr[stopOutput](err)
	}
	defer cancel()
	ev, err := fn(ctx)
	if err != nil {
		return toolErr[stopOutput](err)
	}
	return nil, stopOutput{Stop: formatStopEvent(ev)}, nil
}

func (s *srv) goExecution(ctx context.Context, _ *mcp.CallToolRequest, in executionInput) (*mcp.CallToolResult, stopOutput, error) {
	return s.runExecution(ctx, in, s.sess.Go)
}

func (s *srv) stepIn(ctx context.Context, _ *mcp.CallToolRequest, in executionInput) (*mcp.CallToolResult, stopOutput, error) {
	return s.runExecution(ctx, in, s.sess.StepIn)
}

func (s *srv) stepOver(ctx context.Context, _ *mcp.CallToolRequest, in executionInput) (*mcp.CallToolResult, stopOutput, error) {
	return s.runExecution(ctx, in, s.sess.StepOver)
}

func (s *srv) stepOut(ctx context.Context, _ *mcp.CallToolRequest, in executionInput) (*mcp.CallToolResult, stopOutput, error) {
	return s.runExecution(ctx, in, s.sess.StepOut)
}

func (s *srv) breakIn(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput](err)
	}
	if err := s.sess.BreakIn(); err != nil {
		return toolErr[okOutput](err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) getSymbolPath(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, symbolPathOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[symbolPathOutput](err)
	}
	path, err := s.sess.SymbolPath()
	if err != nil {
		return toolErr[symbolPathOutput](err)
	}
	return nil, symbolPathOutput{Path: path}, nil
}

func (s *srv) setSymbolPath(ctx context.Context, _ *mcp.CallToolRequest, in symbolPathInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput](err)
	}
	if err := s.sess.SetSymbolPath(in.Path); err != nil {
		return toolErr[okOutput](err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) executeRaw(ctx context.Context, _ *mcp.CallToolRequest, in executeRawInput) (*mcp.CallToolResult, executeRawOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[executeRawOutput](err)
	}
	out, err := s.sess.Execute(in.Command)
	if err != nil {
		return toolErr[executeRawOutput](err)
	}
	return nil, executeRawOutput{Output: out}, nil
}

// --- t1-4 symbol reload / status ---

func (s *srv) reloadSymbols(ctx context.Context, _ *mcp.CallToolRequest, in reloadSymbolsInput) (*mcp.CallToolResult, okOutput, error) {
	ctx, cancel, err := contextWithSeconds(ctx, in.TimeoutSeconds)
	if err != nil {
		return toolErr[okOutput](err)
	}
	defer cancel()
	if err := s.sess.ReloadSymbols(ctx, in.Spec); err != nil {
		return toolErr[okOutput](err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) symFix(ctx context.Context, _ *mcp.CallToolRequest, in symFixInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput](err)
	}
	if err := s.sess.SymFix(in.Cache); err != nil {
		return toolErr[okOutput](err)
	}
	return nil, okOutput{OK: true}, nil
}

// --- t1-1 evaluate ---

func (s *srv) evaluate(ctx context.Context, _ *mcp.CallToolRequest, in evaluateInput) (*mcp.CallToolResult, evaluateOutput, error) {
	if strings.TrimSpace(in.Expr) == "" {
		return toolErr[evaluateOutput](fmt.Errorf("expr is required"))
	}
	kind, ok := gokd.ParseValueKind(in.DesiredType)
	if !ok {
		return toolErr[evaluateOutput](fmt.Errorf("invalid desired_type: %q", in.DesiredType))
	}
	ctx, cancel, err := contextWithSeconds(ctx, in.TimeoutSeconds)
	if err != nil {
		return toolErr[evaluateOutput](err)
	}
	defer cancel()
	v, rem, err := s.sess.Evaluate(ctx, in.Expr, kind)
	if err != nil {
		return toolErr[evaluateOutput](err)
	}
	tname, u64, f64, rawHex := formatValue(v)
	return nil, evaluateOutput{Type: tname, U64: u64, F64: f64, RawHex: rawHex, Remainder: rem}, nil
}

func (s *srv) getRadix(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, radixOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[radixOutput](err)
	}
	r, err := s.sess.Radix()
	if err != nil {
		return toolErr[radixOutput](err)
	}
	return nil, radixOutput{Radix: r}, nil
}

func (s *srv) setRadix(ctx context.Context, _ *mcp.CallToolRequest, in setRadixInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput](err)
	}
	if err := s.sess.SetRadix(in.Radix); err != nil {
		return toolErr[okOutput](err)
	}
	return nil, okOutput{OK: true}, nil
}

func (s *srv) getExpressionSyntax(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, expressionSyntaxOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[expressionSyntaxOutput](err)
	}
	syn, err := s.sess.ExpressionSyntax()
	if err != nil {
		return toolErr[expressionSyntaxOutput](err)
	}
	name := "masm"
	if syn == gokd.ExpressionSyntaxCPP {
		name = "cpp"
	}
	return nil, expressionSyntaxOutput{Syntax: name}, nil
}

func (s *srv) setExpressionSyntax(ctx context.Context, _ *mcp.CallToolRequest, in setExpressionSyntaxInput) (*mcp.CallToolResult, okOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[okOutput](err)
	}
	var syn gokd.ExpressionSyntax
	switch strings.ToLower(strings.TrimSpace(in.Syntax)) {
	case "masm", "":
		syn = gokd.ExpressionSyntaxMASM
	case "cpp", "c++":
		syn = gokd.ExpressionSyntaxCPP
	default:
		return toolErr[okOutput](fmt.Errorf("invalid syntax: %q (want 'masm' or 'cpp')", in.Syntax))
	}
	if err := s.sess.SetExpressionSyntax(syn); err != nil {
		return toolErr[okOutput](err)
	}
	return nil, okOutput{OK: true}, nil
}
