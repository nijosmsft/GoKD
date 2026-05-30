package main

import (
	"context"
	"strings"
	"testing"

	"github.com/nijosmsft/gokd"
)

// modulesStub embeds stubSession and overrides Modules() to return a
// caller-supplied slice, so pagination tests can exercise the boundaries
// without a live target.
type modulesStub struct {
	stubSession
	mods []*gokd.Module
}

func (m *modulesStub) Modules() ([]*gokd.Module, error) { return m.mods, nil }

func makeModules(n int, prefix string) []*gokd.Module {
	out := make([]*gokd.Module, n)
	for i := 0; i < n; i++ {
		out[i] = &gokd.Module{
			Name: prefix + "mod" + itoaPad(i, 4),
			Base: uint64(i + 1),
			Size: 4096,
		}
	}
	return out
}

func itoaPad(i, width int) string {
	s := ""
	if i == 0 {
		s = "0"
	}
	for v := i; v > 0; v /= 10 {
		s = string(rune('0'+v%10)) + s
	}
	for len(s) < width {
		s = "0" + s
	}
	return s
}

func TestGetModulesPaginationDefault(t *testing.T) {
	s := &srv{sess: &modulesStub{mods: makeModules(250, "nt!")}}
	_, out, err := s.getModules(context.Background(), nil, getModulesInput{})
	if err != nil {
		t.Fatalf("getModules: %v", err)
	}
	if out.Total != 250 {
		t.Errorf("total=%d want 250", out.Total)
	}
	if out.Returned != defaultModulesLimit {
		t.Errorf("returned=%d want %d", out.Returned, defaultModulesLimit)
	}
	if !out.Truncated {
		t.Errorf("expected truncated=true with 250 modules and default limit")
	}
	if out.NextOffset != defaultModulesLimit {
		t.Errorf("next_offset=%d want %d", out.NextOffset, defaultModulesLimit)
	}
}

func TestGetModulesPaginationOffset(t *testing.T) {
	s := &srv{sess: &modulesStub{mods: makeModules(50, "nt!")}}
	_, out, err := s.getModules(context.Background(), nil, getModulesInput{Offset: 40, Limit: 20})
	if err != nil {
		t.Fatalf("getModules: %v", err)
	}
	if out.Returned != 10 {
		t.Errorf("returned=%d want 10 (50 modules, offset 40, limit 20)", out.Returned)
	}
	if out.Truncated {
		t.Errorf("expected truncated=false at end of set")
	}
	if out.NextOffset != 0 {
		t.Errorf("next_offset=%d want 0 (terminal page)", out.NextOffset)
	}
}

func TestGetModulesPaginationLimitCap(t *testing.T) {
	s := &srv{sess: &modulesStub{mods: makeModules(1000, "nt!")}}
	_, out, err := s.getModules(context.Background(), nil, getModulesInput{Limit: 10000})
	if err != nil {
		t.Fatalf("getModules: %v", err)
	}
	if out.Returned != maxModulesLimit {
		t.Errorf("returned=%d want %d (limit should clamp to %d)", out.Returned, maxModulesLimit, maxModulesLimit)
	}
}

func TestGetModulesNameGlob(t *testing.T) {
	mods := []*gokd.Module{
		{Name: "ntdll", Base: 1},
		{Name: "ntoskrnl", Base: 2},
		{Name: "kernel32", Base: 3},
		{Name: "user32", Base: 4},
	}
	s := &srv{sess: &modulesStub{mods: mods}}
	_, out, err := s.getModules(context.Background(), nil, getModulesInput{NameGlob: "nt*"})
	if err != nil {
		t.Fatalf("getModules: %v", err)
	}
	if out.Total != 2 {
		t.Errorf("total=%d want 2 (nt* should match ntdll + ntoskrnl)", out.Total)
	}
	for _, m := range out.Modules {
		if !strings.HasPrefix(m.Name, "nt") {
			t.Errorf("unexpected module %q in nt* filter", m.Name)
		}
	}
}

func TestGetModulesInvalidGlob(t *testing.T) {
	s := &srv{sess: &modulesStub{mods: []*gokd.Module{{Name: "ntdll"}}}}
	res, _, err := s.getModules(context.Background(), nil, getModulesInput{NameGlob: "[invalid"})
	if err != nil {
		t.Fatalf("getModules: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected error result for invalid glob, got %+v", res)
	}
}

// stackStub overrides Stack() with a caller-supplied slice.
type stackStub struct {
	stubSession
	frames []*gokd.Frame
}

func (s *stackStub) Stack() ([]*gokd.Frame, error) { return s.frames, nil }

func makeFrames(n int) []*gokd.Frame {
	out := make([]*gokd.Frame, n)
	for i := 0; i < n; i++ {
		out[i] = &gokd.Frame{InstructionOffset: uint64(i)}
	}
	return out
}

func TestGetStackDefaultCap(t *testing.T) {
	s := &srv{sess: &stackStub{frames: makeFrames(100)}}
	_, out, err := s.getStack(context.Background(), nil, getStackInput{})
	if err != nil {
		t.Fatalf("getStack: %v", err)
	}
	if out.Returned != defaultStackFrames {
		t.Errorf("returned=%d want %d", out.Returned, defaultStackFrames)
	}
	if !out.Truncated {
		t.Errorf("expected truncated=true with 100 frames and default cap")
	}
}

func TestGetStackHardCap(t *testing.T) {
	s := &srv{sess: &stackStub{frames: makeFrames(500)}}
	_, out, err := s.getStack(context.Background(), nil, getStackInput{MaxFrames: 1000})
	if err != nil {
		t.Fatalf("getStack: %v", err)
	}
	if out.Returned != maxStackFrames {
		t.Errorf("returned=%d want %d (should clamp to %d)", out.Returned, maxStackFrames, maxStackFrames)
	}
}

func TestGetStackExact(t *testing.T) {
	s := &srv{sess: &stackStub{frames: makeFrames(8)}}
	_, out, err := s.getStack(context.Background(), nil, getStackInput{MaxFrames: 16})
	if err != nil {
		t.Fatalf("getStack: %v", err)
	}
	if out.Returned != 8 {
		t.Errorf("returned=%d want 8", out.Returned)
	}
	if out.Truncated {
		t.Errorf("expected truncated=false when frames < max")
	}
}

// memoryStub returns caller-supplied bytes from ReadMemory/ReadPhysical.
type memoryStub struct {
	stubSession
	data []byte
}

func (m *memoryStub) ReadMemory(addr uint64, n uint64) ([]byte, error) {
	if n > uint64(len(m.data)) {
		n = uint64(len(m.data))
	}
	return m.data[:n], nil
}
func (m *memoryStub) ReadPhysical(addr uint64, n uint64) ([]byte, error) {
	return m.ReadMemory(addr, n)
}

func TestReadMemoryCap(t *testing.T) {
	big := make([]byte, 1_000_000)
	s := &srv{sess: &memoryStub{data: big}}
	_, out, err := s.readMemory(context.Background(), nil, readMemoryInput{AddressHex: "0x1000", Length: 1_000_000})
	if err != nil {
		t.Fatalf("readMemory: %v", err)
	}
	if !out.Truncated {
		t.Errorf("expected truncated=true on oversized read")
	}
	if out.Length != maxReadMemoryBytes {
		t.Errorf("length=%d want %d", out.Length, maxReadMemoryBytes)
	}
	if len(out.Hex) != 2*maxReadMemoryBytes {
		t.Errorf("hex length=%d want %d", len(out.Hex), 2*maxReadMemoryBytes)
	}
}

func TestReadPhysicalCap(t *testing.T) {
	big := make([]byte, 1_000_000)
	s := &srv{sess: &memoryStub{data: big}}
	_, out, err := s.readPhysical(context.Background(), nil, readPhysicalInput{AddressHex: "0x1000", Length: 1_000_000})
	if err != nil {
		t.Fatalf("readPhysical: %v", err)
	}
	if !out.Truncated {
		t.Errorf("expected truncated=true on oversized read")
	}
}

// disasmStub returns caller-supplied instructions from DisassembleRange.
type disasmStub struct {
	stubSession
	insns []*gokd.Instruction
}

func (d *disasmStub) DisassembleRange(addr uint64, count int) ([]*gokd.Instruction, error) {
	if count > len(d.insns) {
		count = len(d.insns)
	}
	return d.insns[:count], nil
}

func TestDisassembleCap(t *testing.T) {
	insns := make([]*gokd.Instruction, maxDisassembleCount+10)
	for i := range insns {
		insns[i] = &gokd.Instruction{Address: uint64(i)}
	}
	s := &srv{sess: &disasmStub{insns: insns}}
	_, out, err := s.disassemble(context.Background(), nil, disassembleInput{AddressHex: "0x1000", Count: 5000})
	if err != nil {
		t.Fatalf("disassemble: %v", err)
	}
	if out.Returned != maxDisassembleCount {
		t.Errorf("returned=%d want %d", out.Returned, maxDisassembleCount)
	}
	if !out.Truncated {
		t.Errorf("expected truncated=true when count exceeds cap")
	}
}

// fieldsStub returns caller-supplied fields from TypeFields.
type fieldsStub struct {
	stubSession
	fields []*gokd.Field
}

func (f *fieldsStub) TypeFields(module, typeName string) ([]*gokd.Field, error) {
	return f.fields, nil
}

func TestGetTypeFieldsCap(t *testing.T) {
	fields := make([]*gokd.Field, maxTypeFields+5)
	for i := range fields {
		fields[i] = &gokd.Field{Name: "f"}
	}
	s := &srv{sess: &fieldsStub{fields: fields}}
	_, out, err := s.getTypeFields(context.Background(), nil, typeInput{Module: "nt", TypeName: "_FOO"})
	if err != nil {
		t.Fatalf("getTypeFields: %v", err)
	}
	if out.Returned != maxTypeFields {
		t.Errorf("returned=%d want %d", out.Returned, maxTypeFields)
	}
	if !out.Truncated {
		t.Errorf("expected truncated=true")
	}
}

// execStub returns caller-supplied output from Execute.
type execStub struct {
	stubSession
	out string
}

func (e *execStub) Execute(cmd string) (string, error) { return e.out, nil }

func TestExecuteRawInlineSmall(t *testing.T) {
	s := newSrv(&execStub{out: "small output"}, false, false)
	_, out, err := s.executeRaw(context.Background(), nil, executeRawInput{Command: "k"})
	if err != nil {
		t.Fatalf("executeRaw: %v", err)
	}
	if out.Output != "small output" {
		t.Errorf("output=%q want %q", out.Output, "small output")
	}
	if out.Truncated {
		t.Errorf("expected truncated=false for small output")
	}
	if out.ContinuationToken != "" {
		t.Errorf("unexpected continuation token %q", out.ContinuationToken)
	}
}

func TestExecuteRawContinuation(t *testing.T) {
	big := strings.Repeat("A", rawInlineCapBytes+1234)
	s := newSrv(&execStub{out: big}, false, false)
	_, out, err := s.executeRaw(context.Background(), nil, executeRawInput{Command: "lm"})
	if err != nil {
		t.Fatalf("executeRaw: %v", err)
	}
	if !out.Truncated {
		t.Errorf("expected truncated=true on oversized output")
	}
	if out.ContinuationToken == "" {
		t.Errorf("expected non-empty continuation_token")
	}
	if len(out.Output) != rawInlineCapBytes {
		t.Errorf("output length=%d want %d", len(out.Output), rawInlineCapBytes)
	}
	if out.Bytes != len(big) {
		t.Errorf("bytes=%d want %d", out.Bytes, len(big))
	}

	// Fetch continuation.
	_, cont, err := s.getRawContinuation(context.Background(), nil, getRawContinuationInput{Token: out.ContinuationToken})
	if err != nil {
		t.Fatalf("getRawContinuation: %v", err)
	}
	if cont.Expired {
		t.Errorf("expected continuation to be live, got expired=true")
	}
	if len(cont.Output) != 1234 {
		t.Errorf("continuation output length=%d want 1234", len(cont.Output))
	}
	if cont.Truncated {
		t.Errorf("expected continuation not truncated (fits in single fetch)")
	}
}

func TestGetRawContinuationExpiredToken(t *testing.T) {
	s := newSrv(&execStub{}, false, false)
	_, out, err := s.getRawContinuation(context.Background(), nil, getRawContinuationInput{Token: "deadbeef"})
	if err != nil {
		t.Fatalf("getRawContinuation: %v", err)
	}
	if !out.Expired {
		t.Errorf("expected expired=true for unknown token")
	}
}

func TestGetRawContinuationOffsetLimit(t *testing.T) {
	tail := strings.Repeat("B", rawContinuationMaxBytes*2)
	s := newSrv(&execStub{out: strings.Repeat("A", rawInlineCapBytes) + tail}, false, false)
	_, raw, _ := s.executeRaw(context.Background(), nil, executeRawInput{Command: "x"})
	token := raw.ContinuationToken
	if token == "" {
		t.Fatalf("expected continuation token")
	}
	_, cont, _ := s.getRawContinuation(context.Background(), nil, getRawContinuationInput{Token: token, Limit: 100})
	if len(cont.Output) != 100 {
		t.Errorf("output len=%d want 100", len(cont.Output))
	}
	if cont.NextOffset != 100 || !cont.Truncated {
		t.Errorf("next_offset=%d truncated=%v want (100, true)", cont.NextOffset, cont.Truncated)
	}
}
