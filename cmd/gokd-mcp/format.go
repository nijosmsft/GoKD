package main

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/nijosmsft/gokd"
)

func hex64(v uint64) string { return fmt.Sprintf("0x%016x", v) }

func parseHexUint64(text, field string) (uint64, error) {
	t := strings.TrimSpace(text)
	if t == "" {
		return 0, fmt.Errorf("%s is required", field)
	}
	return strconv.ParseUint(t, 0, 64)
}

// parseHexBytes parses a hex pattern accepting either a contiguous run of
// hex digits ("deadbeef") or whitespace-separated bytes ("de ad be ef").
// Returns an error if the field is empty, the input contains non-hex
// characters, or the digit count is odd.
func parseHexBytes(text, field string) ([]byte, error) {
	t := strings.TrimSpace(text)
	if t == "" {
		return nil, fmt.Errorf("%s is required", field)
	}
	// strip whitespace
	var sb strings.Builder
	for _, r := range t {
		switch r {
		case ' ', '\t', '\n', '\r':
			continue
		}
		sb.WriteRune(r)
	}
	// strip optional 0x/0X prefix
	s := sb.String()
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		s = s[2:]
	}
	if len(s) == 0 {
		return nil, fmt.Errorf("%s is required", field)
	}
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("%s must contain an even number of hex digits", field)
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	return b, nil
}

func registerTypeString(t gokd.RegisterType) string {
	switch t {
	case gokd.RegisterInt8:
		return "int8"
	case gokd.RegisterInt16:
		return "int16"
	case gokd.RegisterInt32:
		return "int32"
	case gokd.RegisterInt64:
		return "int64"
	case gokd.RegisterFloat32:
		return "float32"
	case gokd.RegisterFloat64:
		return "float64"
	case gokd.RegisterFloat80:
		return "float80"
	case gokd.RegisterVector128:
		return "vector128"
	default:
		return fmt.Sprintf("unknown(%d)", int(t))
	}
}

type Module struct {
	Name       string `json:"name"`
	ImageName  string `json:"image_name,omitempty"`
	BaseHex    string `json:"base_hex"`
	Size       uint32 `json:"size"`
	Timestamp  uint32 `json:"timestamp"`
	Checksum   uint32 `json:"checksum"`
	SymbolType string `json:"symbol_type"`
}

type Thread struct {
	SystemID       uint32 `json:"system_id"`
	HandleHex      string `json:"handle_hex"`
	DataOffsetHex  string `json:"data_offset_hex"`
	StartOffsetHex string `json:"start_offset_hex"`
}

type Frame struct {
	InstructionOffsetHex string `json:"instruction_offset_hex"`
	ReturnOffsetHex      string `json:"return_offset_hex"`
	FrameOffsetHex       string `json:"frame_offset_hex"`
	StackOffsetHex       string `json:"stack_offset_hex"`
	Module               string `json:"module,omitempty"`
	Function             string `json:"function,omitempty"`
	Displacement         uint64 `json:"displacement"`
	SourceFile           string `json:"source_file,omitempty"`
	SourceLine           uint32 `json:"source_line,omitempty"`
}

type Register struct {
	Name     string `json:"name"`
	ValueHex string `json:"value_hex"`
	Type     string `json:"type"`
	Valid    bool   `json:"valid"`
}

type Instruction struct {
	AddressHex string `json:"address_hex"`
	Text       string `json:"text"`
	Size       uint32 `json:"size"`
	BytesHex   string `json:"bytes_hex,omitempty"`
}

type Field struct {
	Name     string `json:"name"`
	Offset   uint32 `json:"offset"`
	Size     uint64 `json:"size"`
	TypeName string `json:"type_name"`
}

type Breakpoint struct {
	ID         uint32 `json:"id"`
	AddressHex string `json:"address_hex"`
	Expression string `json:"expression,omitempty"`
	Enabled    bool   `json:"enabled"`
}

type ExceptionInfo struct {
	Code        uint32 `json:"code"`
	AddressHex  string `json:"address_hex"`
	FirstChance bool   `json:"first_chance"`
}

type StopEvent struct {
	Reason     string         `json:"reason,omitempty"`
	AddressHex string         `json:"address_hex,omitempty"`
	Thread     *Thread        `json:"thread,omitempty"`
	Exception  *ExceptionInfo `json:"exception,omitempty"`
}

func formatModule(m *gokd.Module) Module {
	return Module{Name: m.Name, ImageName: m.ImageName, BaseHex: hex64(m.Base), Size: m.Size, Timestamp: m.Timestamp, Checksum: m.Checksum, SymbolType: gokd.SymbolTypeString(m.SymbolType)}
}

func formatThread(t *gokd.Thread) Thread {
	if t == nil {
		return Thread{}
	}
	return Thread{SystemID: t.SystemID, HandleHex: hex64(t.Handle), DataOffsetHex: hex64(t.DataOffset), StartOffsetHex: hex64(t.StartOffset)}
}

func formatFrame(f *gokd.Frame) Frame {
	return Frame{
		InstructionOffsetHex: hex64(f.InstructionOffset),
		ReturnOffsetHex:      hex64(f.ReturnOffset),
		FrameOffsetHex:       hex64(f.FrameOffset),
		StackOffsetHex:       hex64(f.StackOffset),
		Module:               f.Module,
		Function:             f.Function,
		Displacement:         f.Displacement,
		SourceFile:           f.SourceFile,
		SourceLine:           f.SourceLine,
	}
}

func formatRegister(r gokd.Register) Register {
	return Register{Name: r.Name, ValueHex: hex64(r.Value), Type: registerTypeString(r.Type), Valid: r.Valid}
}

func formatInstruction(in *gokd.Instruction) Instruction {
	return Instruction{AddressHex: hex64(in.Address), Text: in.Text, Size: in.Size, BytesHex: hex.EncodeToString(in.Bytes)}
}

func formatField(f *gokd.Field) Field {
	return Field{Name: f.Name, Offset: f.Offset, Size: f.Size, TypeName: f.TypeName}
}

func formatBreakpoint(bp *gokd.Breakpoint) Breakpoint {
	return Breakpoint{ID: bp.ID, AddressHex: hex64(bp.Address), Expression: bp.Expression, Enabled: bp.Enabled}
}

func formatStopEvent(ev *gokd.StopEvent) StopEvent {
	if ev == nil {
		return StopEvent{}
	}
	out := StopEvent{Reason: ev.Reason.String(), AddressHex: hex64(ev.Address)}
	if ev.Thread != nil {
		thread := formatThread(ev.Thread)
		out.Thread = &thread
	}
	if ev.Exception != nil {
		out.Exception = &ExceptionInfo{Code: ev.Exception.Code, AddressHex: hex64(ev.Exception.Address), FirstChance: ev.Exception.FirstChance}
	}
	return out
}

// formatValue picks the right payload slot for a Value and returns a
// (type-name, u64, f64, raw-hex) tuple. raw is always returned as a hex
// string for full fidelity, even when u64/f64 carries the canonical value.
func formatValue(v gokd.Value) (string, uint64, float64, string) {
	return gokd.ValueKindString(v.Type), v.U64, v.F64, hex.EncodeToString(v.Raw[:])
}
