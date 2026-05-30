package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nijosmsft/gokd"
)

// Workflow caps. Per-tool defaults that bound output of the composite
// workflow tools (triage_crash, walk_stacks_all, inspect_struct, etc.)
// so a single call cannot blow up an LLM context.
const (
	defaultWalkStackFrames  = 32
	defaultWalkStackThreads = 64
	maxWalkStackThreads     = 256

	defaultInspectRecurse = 0
	maxInspectRecurse     = 3
	maxInspectFields      = 1024

	defaultReadStringBytes = 256
	maxReadStringBytes     = 4096

	defaultDumpMemoryWidth = 1
)

// ---- triage_crash ----

type triageCrashInput struct {
	MaxFrames int `json:"max_frames,omitempty" jsonschema:"frame cap (default 32, hard cap 256)"`
}

type triageCrashOutput struct {
	BugCheck       *bugCheckOutput      `json:"bug_check,omitempty"`
	Exception      *lastExceptionOutput `json:"exception,omitempty"`
	FaultingThread *Thread              `json:"faulting_thread,omitempty"`
	FaultingFrame  *Frame               `json:"faulting_frame,omitempty"`
	Stack          []Frame              `json:"stack,omitempty"`
	Registers      map[string]string    `json:"registers,omitempty"`
	NearbyCode     []Instruction        `json:"nearby_code,omitempty"`
	Modules        []Module             `json:"modules,omitempty"`
	Summary        string               `json:"summary"`
}

func (s *srv) triageCrash(ctx context.Context, _ *mcp.CallToolRequest, in triageCrashInput) (*mcp.CallToolResult, triageCrashOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[triageCrashOutput]("triage_crash", err)
	}

	out := triageCrashOutput{}
	parts := []string{}

	if bc, err := s.sess.BugCheck(); err == nil && bc != nil {
		args := make([]string, 0, len(bc.Args))
		for _, a := range bc.Args {
			args = append(args, hex64(a))
		}
		out.BugCheck = &bugCheckOutput{
			Found:       true,
			Code:        bc.Code,
			CodeHex:     fmt.Sprintf("0x%08x", bc.Code),
			Args:        args,
			Name:        bc.Name,
			Description: bc.Description,
		}
		parts = append(parts, fmt.Sprintf("Bug check 0x%x (%s)", bc.Code, bc.Name))
	}

	if ex, err := s.sess.LastException(); err == nil && ex != nil {
		params := make([]string, 0, ex.ParameterCount)
		for i := uint32(0); i < ex.ParameterCount; i++ {
			params = append(params, hex64(ex.Parameters[i]))
		}
		out.Exception = &lastExceptionOutput{
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
		}
		parts = append(parts, fmt.Sprintf("Exception 0x%08x at %s", ex.Code, hex64(ex.Address)))
	} else if errors.Is(err, gokd.ErrNotFound) {
		// No exception; carry on.
	} else if err != nil && out.BugCheck == nil {
		// We couldn't get either bug check or exception — surface the
		// underlying error to the caller.
		return toolErr[triageCrashOutput]("triage_crash", err)
	}

	if threads, err := s.sess.Threads(); err == nil && len(threads) > 0 {
		ft := formatThread(threads[0])
		out.FaultingThread = &ft
	}

	maxFrames := in.MaxFrames
	if maxFrames <= 0 {
		maxFrames = defaultStackFrames
	}
	if maxFrames > maxStackFrames {
		maxFrames = maxStackFrames
	}
	if frames, err := s.sess.Stack(); err == nil {
		if len(frames) > maxFrames {
			frames = frames[:maxFrames]
		}
		out.Stack = make([]Frame, len(frames))
		for i, f := range frames {
			out.Stack[i] = formatFrame(f)
		}
		if len(out.Stack) > 0 {
			ff := out.Stack[0]
			out.FaultingFrame = &ff
			if ff.Function != "" {
				parts = append(parts, fmt.Sprintf("at %s+0x%x", ff.Function, ff.Displacement))
			}
		}
	}

	if regs, err := s.sess.Registers(); err == nil && regs != nil {
		out.Registers = map[string]string{}
		for _, r := range regs.Registers {
			if r.Valid {
				out.Registers[r.Name] = hex64(r.Value)
			}
		}
	}

	if out.FaultingFrame != nil {
		if rip, perr := parseHexUint64(out.FaultingFrame.InstructionOffsetHex, "rip"); perr == nil {
			start := rip
			if rip >= 20 {
				start = rip - 20
			}
			if ins, err := s.sess.DisassembleRange(start, 11); err == nil {
				out.NearbyCode = make([]Instruction, len(ins))
				for i, inst := range ins {
					out.NearbyCode[i] = formatInstruction(inst)
				}
			}
		}
	}

	if out.FaultingFrame != nil && out.FaultingFrame.Module != "" {
		if mods, err := s.sess.Modules(); err == nil {
			for _, m := range mods {
				if m.Name == out.FaultingFrame.Module {
					out.Modules = append(out.Modules, formatModule(m))
					break
				}
			}
		}
	}

	if len(parts) == 0 {
		out.Summary = "No bug check or exception found. Inspect get_session_state and walk_stacks_all to diagnose the stop."
	} else {
		out.Summary = strings.Join(parts, ". ") + "."
	}
	return nil, out, nil
}

// ---- summarise_target ----

type summariseTargetOutput struct {
	State            getSessionStateOutput `json:"state"`
	TopModules       []Module              `json:"top_modules,omitempty"`
	Threads          []Thread              `json:"threads,omitempty"`
	ExceptionPreview *lastExceptionOutput  `json:"exception_preview,omitempty"`
	BugCheckPreview  *bugCheckOutput       `json:"bug_check_preview,omitempty"`
	Summary          string                `json:"summary"`
}

func (s *srv) summariseTarget(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, summariseTargetOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[summariseTargetOutput]("summarise_target", err)
	}
	out := summariseTargetOutput{}
	_, stateOut, _ := s.getSessionState(ctx, nil, struct{}{})
	out.State = stateOut

	if mods, err := s.sess.Modules(); err == nil {
		n := len(mods)
		if n > 20 {
			n = 20
		}
		out.TopModules = make([]Module, n)
		for i := 0; i < n; i++ {
			out.TopModules[i] = formatModule(mods[i])
		}
	}
	if threads, err := s.sess.Threads(); err == nil {
		n := len(threads)
		if n > 32 {
			n = 32
		}
		out.Threads = make([]Thread, n)
		for i := 0; i < n; i++ {
			out.Threads[i] = formatThread(threads[i])
		}
	}
	if ex, err := s.sess.LastException(); err == nil && ex != nil {
		out.ExceptionPreview = &lastExceptionOutput{
			Found:      true,
			Code:       ex.Code,
			CodeHex:    fmt.Sprintf("0x%08x", ex.Code),
			AddressHex: hex64(ex.Address),
		}
	}
	if bc, err := s.sess.BugCheck(); err == nil && bc != nil {
		out.BugCheckPreview = &bugCheckOutput{
			Found:       true,
			Code:        bc.Code,
			CodeHex:     fmt.Sprintf("0x%08x", bc.Code),
			Name:        bc.Name,
			Description: bc.Description,
		}
	}

	parts := []string{}
	if out.State.TargetKind != "" {
		parts = append(parts, fmt.Sprintf("%s target", out.State.TargetKind))
	}
	if out.State.TargetName != "" {
		parts = append(parts, out.State.TargetName)
	}
	parts = append(parts, fmt.Sprintf("status=%s", out.State.Status))
	parts = append(parts, fmt.Sprintf("%d modules, %d threads", out.State.Modules, out.State.Threads))
	if out.BugCheckPreview != nil {
		parts = append(parts, fmt.Sprintf("bug check 0x%x", out.BugCheckPreview.Code))
	}
	if out.ExceptionPreview != nil {
		parts = append(parts, fmt.Sprintf("last exception 0x%08x at %s", out.ExceptionPreview.Code, out.ExceptionPreview.AddressHex))
	}
	out.Summary = strings.Join(parts, ", ") + "."
	return nil, out, nil
}

// ---- walk_stacks_all ----

type walkStacksAllInput struct {
	MaxFrames        int  `json:"max_frames,omitempty" jsonschema:"per-thread frame cap (default 32, hard cap 256)"`
	MaxThreads       int  `json:"max_threads,omitempty" jsonschema:"thread cap (default 64, hard cap 256)"`
	IncludeRegisters bool `json:"include_registers,omitempty" jsonschema:"if true, include registers per thread (expensive)"`
}

type threadStack struct {
	Thread    Thread            `json:"thread"`
	Frames    []Frame           `json:"frames"`
	Truncated bool              `json:"truncated,omitempty"`
	Registers map[string]string `json:"registers,omitempty"`
	Caveat    string            `json:"caveat,omitempty"`
}

type walkStacksAllOutput struct {
	Items        []threadStack `json:"items"`
	Returned     int           `json:"returned"`
	TotalThreads int           `json:"total_threads"`
	Truncated    bool          `json:"truncated,omitempty"`
}

func (s *srv) walkStacksAll(ctx context.Context, _ *mcp.CallToolRequest, in walkStacksAllInput) (*mcp.CallToolResult, walkStacksAllOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[walkStacksAllOutput]("walk_stacks_all", err)
	}
	threads, err := s.sess.Threads()
	if err != nil {
		return toolErr[walkStacksAllOutput]("walk_stacks_all", err)
	}

	maxFrames := in.MaxFrames
	if maxFrames <= 0 {
		maxFrames = defaultWalkStackFrames
	}
	if maxFrames > maxStackFrames {
		maxFrames = maxStackFrames
	}
	maxThreads := in.MaxThreads
	if maxThreads <= 0 {
		maxThreads = defaultWalkStackThreads
	}
	if maxThreads > maxWalkStackThreads {
		maxThreads = maxWalkStackThreads
	}

	total := len(threads)
	truncated := false
	if total > maxThreads {
		threads = threads[:maxThreads]
		truncated = true
	}

	// Save current thread so we can restore it after switching.
	var origTID uint32
	if len(threads) > 0 {
		origTID = threads[0].SystemID
	}

	out := walkStacksAllOutput{TotalThreads: total, Truncated: truncated}
	for _, th := range threads {
		entry := threadStack{Thread: formatThread(th)}
		if err := s.sess.SetThread(th.SystemID); err != nil {
			entry.Caveat = fmt.Sprintf("set_thread failed: %v", err)
			out.Items = append(out.Items, entry)
			continue
		}
		frames, ferr := s.sess.Stack()
		if ferr != nil {
			entry.Caveat = fmt.Sprintf("Stack() failed: %v", ferr)
		} else {
			if len(frames) > maxFrames {
				frames = frames[:maxFrames]
				entry.Truncated = true
			}
			entry.Frames = make([]Frame, len(frames))
			for i, f := range frames {
				entry.Frames[i] = formatFrame(f)
			}
		}
		if in.IncludeRegisters {
			if regs, rerr := s.sess.Registers(); rerr == nil && regs != nil {
				entry.Registers = map[string]string{}
				for _, r := range regs.Registers {
					if r.Valid {
						entry.Registers[r.Name] = hex64(r.Value)
					}
				}
			}
		}
		out.Items = append(out.Items, entry)
	}
	// Best-effort restore.
	if origTID != 0 {
		_ = s.sess.SetThread(origTID)
	}
	out.Returned = len(out.Items)
	return nil, out, nil
}

// ---- inspect_struct ----

type inspectStructInput struct {
	TypeName string `json:"type_name" jsonschema:"type name with module prefix (e.g. 'nt!_EPROCESS' or 'notepad!FOO')"`
	Address  string `json:"address" jsonschema:"hex address of the instance"`
	Recurse  int    `json:"recurse,omitempty" jsonschema:"pointer-follow depth (default 0, max 3)"`
}

type inspectStructOutput struct {
	TypeName  string         `json:"type_name"`
	Address   string         `json:"address"`
	SizeBytes uint64         `json:"size_bytes"`
	Fields    []inspectField `json:"fields"`
	Truncated bool           `json:"truncated,omitempty"`
}

type inspectField struct {
	Name     string `json:"name"`
	Offset   uint32 `json:"offset"`
	TypeName string `json:"type_name"`
	Children []any  `json:"children,omitempty"`
}

func (s *srv) inspectStruct(ctx context.Context, _ *mcp.CallToolRequest, in inspectStructInput) (*mcp.CallToolResult, inspectStructOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[inspectStructOutput]("inspect_struct", err)
	}
	addr, err := parseHexUint64(in.Address, "address")
	if err != nil {
		return toolErr[inspectStructOutput]("inspect_struct", err)
	}
	module, typeName, err := splitTypeName(in.TypeName)
	if err != nil {
		return toolErr[inspectStructOutput]("inspect_struct", err)
	}
	recurse := in.Recurse
	if recurse < 0 {
		recurse = 0
	}
	if recurse > maxInspectRecurse {
		recurse = maxInspectRecurse
	}

	size, _ := s.sess.TypeSize(module, typeName)
	out := inspectStructOutput{
		TypeName:  in.TypeName,
		Address:   hex64(addr),
		SizeBytes: size,
	}
	budget := maxInspectFields
	fields, truncated, err := s.walkInspectFields(module, typeName, recurse, &budget)
	if err != nil {
		return toolErr[inspectStructOutput]("inspect_struct", err)
	}
	out.Fields = fields
	out.Truncated = truncated
	return nil, out, nil
}

// walkInspectFields recursively descends struct fields up to remainingDepth
// pointer-follow levels. budget bounds the total field count across all
// recursion levels so a deeply nested type cannot exceed maxInspectFields.
func (s *srv) walkInspectFields(module, typeName string, remainingDepth int, budget *int) ([]inspectField, bool, error) {
	if *budget <= 0 {
		return nil, true, nil
	}
	raw, err := s.sess.TypeFields(module, typeName)
	if err != nil {
		return nil, false, err
	}
	if len(raw) > *budget {
		raw = raw[:*budget]
	}
	out := make([]inspectField, 0, len(raw))
	truncated := false
	for _, f := range raw {
		if *budget <= 0 {
			truncated = true
			break
		}
		*budget--
		entry := inspectField{Name: f.Name, Offset: f.Offset, TypeName: f.TypeName}
		// Recurse into nested struct types only when we have depth budget
		// left and the type does not look like a pointer or scalar.
		if remainingDepth > 0 && shouldRecurseType(f.TypeName) {
			childModule, childType := module, strings.TrimPrefix(f.TypeName, "struct ")
			children, ct, err := s.walkInspectFields(childModule, childType, remainingDepth-1, budget)
			if err == nil {
				if len(children) > 0 {
					anyChildren := make([]any, len(children))
					for i := range children {
						anyChildren[i] = children[i]
					}
					entry.Children = anyChildren
				}
				if ct {
					truncated = true
				}
			}
		}
		out = append(out, entry)
	}
	return out, truncated, nil
}

func shouldRecurseType(t string) bool {
	t = strings.TrimSpace(t)
	if t == "" {
		return false
	}
	if strings.HasSuffix(t, "*") {
		return false
	}
	switch t {
	case "void", "int", "long", "short", "char", "bool",
		"unsigned int", "unsigned long", "unsigned short", "unsigned char",
		"int8", "int16", "int32", "int64",
		"uint8", "uint16", "uint32", "uint64",
		"float", "double":
		return false
	}
	return strings.HasPrefix(t, "_") || strings.HasPrefix(t, "struct ")
}

// splitTypeName accepts "module!type" and returns the parts. Returns an
// error if the type is missing the module prefix.
func splitTypeName(qual string) (module, typeName string, err error) {
	qual = strings.TrimSpace(qual)
	if i := strings.Index(qual, "!"); i > 0 {
		return qual[:i], qual[i+1:], nil
	}
	return "", "", fmt.Errorf("type_name must be qualified as 'module!type', got %q", qual)
}

// ---- read_string ----

type readStringInput struct {
	Address  string `json:"address" jsonschema:"hex address of the first byte"`
	Encoding string `json:"encoding,omitempty" jsonschema:"'ansi' | 'utf16le' | 'auto' (default auto)"`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"byte budget (default 256, max 4096)"`
}

type readStringOutput struct {
	Encoding  string `json:"encoding"`
	Value     string `json:"value"`
	Bytes     int    `json:"bytes"`
	Truncated bool   `json:"truncated,omitempty"`
	Auto      bool   `json:"auto,omitempty"`
}

func (s *srv) readString(ctx context.Context, _ *mcp.CallToolRequest, in readStringInput) (*mcp.CallToolResult, readStringOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[readStringOutput]("read_string", err)
	}
	addr, err := parseHexUint64(in.Address, "address")
	if err != nil {
		return toolErr[readStringOutput]("read_string", err)
	}
	max := in.MaxBytes
	if max <= 0 {
		max = defaultReadStringBytes
	}
	if max > maxReadStringBytes {
		max = maxReadStringBytes
	}
	data, err := s.sess.ReadMemory(addr, uint64(max))
	if err != nil {
		return toolErr[readStringOutput]("read_string", err)
	}
	enc := strings.ToLower(strings.TrimSpace(in.Encoding))
	auto := false
	if enc == "" || enc == "auto" {
		enc = detectStringEncoding(data)
		auto = true
	}
	out := readStringOutput{Encoding: enc, Auto: auto}
	switch enc {
	case "utf16le":
		out.Value, out.Bytes, out.Truncated = decodeUTF16LE(data)
	case "ansi":
		out.Value, out.Bytes, out.Truncated = decodeANSI(data)
	default:
		return toolErr[readStringOutput]("read_string", fmt.Errorf("unsupported encoding %q (want 'ansi' or 'utf16le' or 'auto')", enc))
	}
	return nil, out, nil
}

func detectStringEncoding(data []byte) string {
	n := len(data)
	if n >= 4 {
		zeros, printable := 0, 0
		probe := n
		if probe > 16 {
			probe = 16
		}
		pairs := probe / 2
		// Look at the first 16 bytes pair-wise (UTF-16LE: ASCII char
		// in even byte, NUL in odd byte). Require near-total dominance
		// of NULs in odd positions — short ASCII strings often end in
		// a single NUL terminator which would otherwise trip a 50%
		// threshold.
		for i := 0; i+1 < probe; i += 2 {
			if data[i+1] == 0x00 {
				zeros++
			}
			if data[i] >= 0x20 && data[i] < 0x7f {
				printable++
			}
		}
		if pairs >= 2 && zeros >= pairs && printable >= pairs/2 {
			return "utf16le"
		}
	}
	asciiPrintable := 0
	for _, b := range data {
		if b == 0x00 {
			break
		}
		if b >= 0x20 && b < 0x7f {
			asciiPrintable++
		}
	}
	if asciiPrintable*5 >= len(data)*4 {
		return "ansi"
	}
	return "utf16le"
}

func decodeUTF16LE(data []byte) (string, int, bool) {
	u16 := make([]uint16, 0, len(data)/2)
	consumed := 0
	for i := 0; i+1 < len(data); i += 2 {
		c := uint16(data[i]) | uint16(data[i+1])<<8
		consumed = i + 2
		if c == 0 {
			return string(utf16.Decode(u16)), consumed, false
		}
		u16 = append(u16, c)
	}
	return string(utf16.Decode(u16)), consumed, true
}

func decodeANSI(data []byte) (string, int, bool) {
	for i, b := range data {
		if b == 0 {
			return string(data[:i]), i + 1, false
		}
	}
	return string(data), len(data), true
}

// ---- dump_memory ----

type dumpMemoryInput struct {
	Address  string `json:"address" jsonschema:"hex address of the first byte"`
	Length   int    `json:"length" jsonschema:"bytes to read (hard cap 4096)"`
	Physical bool   `json:"physical,omitempty" jsonschema:"if true, read physical memory (kernel targets only)"`
	Width    int    `json:"width,omitempty" jsonschema:"bytes per group (1,2,4,8; default 1)"`
}

type dumpRow struct {
	Address  string `json:"address"`
	HexBytes string `json:"hex_bytes"`
	ASCII    string `json:"ascii"`
}

type dumpMemoryOutput struct {
	Rows      []dumpRow `json:"rows"`
	Bytes     int       `json:"bytes"`
	Truncated bool      `json:"truncated,omitempty"`
}

func (s *srv) dumpMemory(ctx context.Context, _ *mcp.CallToolRequest, in dumpMemoryInput) (*mcp.CallToolResult, dumpMemoryOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[dumpMemoryOutput]("dump_memory", err)
	}
	addr, err := parseHexUint64(in.Address, "address")
	if err != nil {
		return toolErr[dumpMemoryOutput]("dump_memory", err)
	}
	if in.Length <= 0 {
		return toolErr[dumpMemoryOutput]("dump_memory", fmt.Errorf("length must be > 0"))
	}
	length := in.Length
	truncated := false
	if length > maxReadMemoryBytes {
		length = maxReadMemoryBytes
		truncated = true
	}
	width := in.Width
	if width == 0 {
		width = defaultDumpMemoryWidth
	}
	switch width {
	case 1, 2, 4, 8:
	default:
		return toolErr[dumpMemoryOutput]("dump_memory", fmt.Errorf("width must be 1, 2, 4, or 8 (got %d)", width))
	}

	var data []byte
	if in.Physical {
		data, err = s.sess.ReadPhysical(addr, uint64(length))
	} else {
		data, err = s.sess.ReadMemory(addr, uint64(length))
	}
	if err != nil {
		return toolErr[dumpMemoryOutput]("dump_memory", err)
	}

	rows := []dumpRow{}
	const bytesPerRow = 16
	for i := 0; i < len(data); i += bytesPerRow {
		end := i + bytesPerRow
		if end > len(data) {
			end = len(data)
		}
		row := data[i:end]
		rows = append(rows, dumpRow{
			Address:  hex64(addr + uint64(i)),
			HexBytes: formatHexRow(row, width),
			ASCII:    formatASCIIRow(row),
		})
	}
	return nil, dumpMemoryOutput{Rows: rows, Bytes: len(data), Truncated: truncated}, nil
}

func formatHexRow(row []byte, width int) string {
	var sb strings.Builder
	for i := 0; i < len(row); i += width {
		end := i + width
		if end > len(row) {
			end = len(row)
		}
		// little-endian group: byte i is least significant; print high
		// byte first for human-readable hex.
		group := row[i:end]
		reversed := make([]byte, len(group))
		for j := range group {
			reversed[len(group)-1-j] = group[j]
		}
		if sb.Len() > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(hex.EncodeToString(reversed))
	}
	return sb.String()
}

func formatASCIIRow(row []byte) string {
	var sb strings.Builder
	for _, b := range row {
		if b >= 0x20 && b < 0x7f {
			sb.WriteByte(b)
		} else {
			sb.WriteByte('.')
		}
	}
	return sb.String()
}
