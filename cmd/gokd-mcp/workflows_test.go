package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/nijosmsft/gokd"
	"github.com/nijosmsft/gokd/internal/dbgcgo"
)

// workflowStub overrides the subset of gokd.Session methods that the
// workflow composites call. Tests construct one of these with canned data
// and feed it to *srv as if it were a live target.
type workflowStub struct {
	stubSession

	mu sync.Mutex

	threads     []*gokd.Thread
	threadsErr  error
	stackByTID  map[uint32][]*gokd.Frame
	currentTID  uint32
	setThreadErr error

	registers    *gokd.RegisterSet
	registersErr error

	modules []*gokd.Module

	bugCheck    *gokd.BugCheck
	bugCheckErr error

	lastException    *gokd.LastException
	lastExceptionErr error

	memReads  map[uint64][]byte
	physReads map[uint64][]byte

	disasm map[uint64][]*gokd.Instruction

	typeFields map[string][]*gokd.Field
	typeSize   map[string]uint64
}

func (w *workflowStub) Threads() ([]*gokd.Thread, error) {
	if w.threadsErr != nil {
		return nil, w.threadsErr
	}
	return w.threads, nil
}

func (w *workflowStub) Stack() ([]*gokd.Frame, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if frames, ok := w.stackByTID[w.currentTID]; ok {
		return frames, nil
	}
	return nil, nil
}

func (w *workflowStub) SetThread(tid uint32) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.setThreadErr != nil {
		return w.setThreadErr
	}
	w.currentTID = tid
	return nil
}

func (w *workflowStub) Registers() (*gokd.RegisterSet, error) {
	if w.registersErr != nil {
		return nil, w.registersErr
	}
	return w.registers, nil
}

func (w *workflowStub) Modules() ([]*gokd.Module, error) { return w.modules, nil }

func (w *workflowStub) BugCheck() (*gokd.BugCheck, error) {
	return w.bugCheck, w.bugCheckErr
}

func (w *workflowStub) LastException() (*gokd.LastException, error) {
	return w.lastException, w.lastExceptionErr
}

func (w *workflowStub) ReadMemory(addr uint64, n uint64) ([]byte, error) {
	if data, ok := w.memReads[addr]; ok {
		if uint64(len(data)) > n {
			return data[:n], nil
		}
		return data, nil
	}
	return make([]byte, n), nil
}

func (w *workflowStub) ReadPhysical(addr uint64, n uint64) ([]byte, error) {
	if data, ok := w.physReads[addr]; ok {
		if uint64(len(data)) > n {
			return data[:n], nil
		}
		return data, nil
	}
	return make([]byte, n), nil
}

func (w *workflowStub) DisassembleRange(addr uint64, count int) ([]*gokd.Instruction, error) {
	if ins, ok := w.disasm[addr]; ok {
		return ins, nil
	}
	return nil, nil
}

func (w *workflowStub) TypeFields(module, typeName string) ([]*gokd.Field, error) {
	key := module + "!" + typeName
	if f, ok := w.typeFields[key]; ok {
		return f, nil
	}
	return nil, nil
}

func (w *workflowStub) TypeSize(module, typeName string) (uint64, error) {
	key := module + "!" + typeName
	if sz, ok := w.typeSize[key]; ok {
		return sz, nil
	}
	return 0, nil
}

// ---- triage_crash ----

func TestTriageCrashUserModeException(t *testing.T) {
	w := &workflowStub{
		threads: []*gokd.Thread{{SystemID: 100}, {SystemID: 200}},
		stackByTID: map[uint32][]*gokd.Frame{
			100: {
				{InstructionOffset: 0x1000, Module: "notepad", Function: "FaultFn", Displacement: 0x5},
				{InstructionOffset: 0x2000, Module: "kernel32", Function: "X", Displacement: 0},
			},
		},
		currentTID: 100,
		registers: &gokd.RegisterSet{
			Registers: []gokd.Register{
				{Name: "rip", Value: 0x1000, Valid: true},
				{Name: "rax", Value: 0xdead, Valid: true},
				{Name: "broken", Value: 0, Valid: false},
			},
		},
		modules: []*gokd.Module{
			{Name: "notepad", Base: 0x400000, Size: 0x1000},
			{Name: "kernel32", Base: 0x500000, Size: 0x2000},
		},
		lastException: &gokd.LastException{
			Code:           0xC0000005,
			Address:        0x1234,
			ParameterCount: 2,
			Parameters:     [dbgcgo.ExceptionMaxParameters]uint64{1, 0xdeadbeef},
			FirstChance:    false,
			Description:    "Access violation",
		},
		disasm: map[uint64][]*gokd.Instruction{
			0xfec: {{Address: 0x1000, Text: "mov eax, [rbx]", Size: 2, Bytes: []byte{0x8b, 0x03}}},
		},
	}
	s := &srv{sess: w}
	_, out, err := s.triageCrash(context.Background(), nil, triageCrashInput{})
	if err != nil {
		t.Fatalf("triageCrash: %v", err)
	}
	if out.Exception == nil || out.Exception.Code != 0xC0000005 {
		t.Errorf("exception missing or wrong: %+v", out.Exception)
	}
	if out.FaultingFrame == nil || out.FaultingFrame.Function != "FaultFn" {
		t.Errorf("faulting frame missing or wrong: %+v", out.FaultingFrame)
	}
	if len(out.Stack) != 2 {
		t.Errorf("stack len=%d want 2", len(out.Stack))
	}
	if out.Registers["rip"] == "" {
		t.Errorf("registers missing rip: %+v", out.Registers)
	}
	if _, present := out.Registers["broken"]; present {
		t.Errorf("invalid register should not be emitted")
	}
	if len(out.NearbyCode) != 1 {
		t.Errorf("nearby code len=%d want 1 (from canned disasm)", len(out.NearbyCode))
	}
	if len(out.Modules) != 1 || out.Modules[0].Name != "notepad" {
		t.Errorf("modules should only include faulting module: %+v", out.Modules)
	}
	if !strings.Contains(out.Summary, "Exception 0xc0000005") {
		t.Errorf("summary missing exception text: %q", out.Summary)
	}
}

func TestTriageCrashBugCheck(t *testing.T) {
	w := &workflowStub{
		bugCheck: &gokd.BugCheck{
			Code: 0x7E,
			Args: [4]uint64{1, 2, 3, 4},
			Name: "SYSTEM_THREAD_EXCEPTION_NOT_HANDLED",
		},
		threads:    []*gokd.Thread{{SystemID: 100}},
		currentTID: 100,
		stackByTID: map[uint32][]*gokd.Frame{100: {{InstructionOffset: 0x9000}}},
	}
	s := &srv{sess: w}
	_, out, err := s.triageCrash(context.Background(), nil, triageCrashInput{})
	if err != nil {
		t.Fatalf("triageCrash: %v", err)
	}
	if out.BugCheck == nil || out.BugCheck.Code != 0x7E {
		t.Errorf("bug check missing: %+v", out.BugCheck)
	}
	if !strings.Contains(out.Summary, "Bug check") {
		t.Errorf("summary missing bug check: %q", out.Summary)
	}
}

func TestTriageCrashNoFault(t *testing.T) {
	w := &workflowStub{
		lastExceptionErr: gokd.ErrNotFound,
	}
	s := &srv{sess: w}
	_, out, err := s.triageCrash(context.Background(), nil, triageCrashInput{})
	if err != nil {
		t.Fatalf("triageCrash: %v", err)
	}
	if !strings.Contains(out.Summary, "No bug check") {
		t.Errorf("expected 'no bug check' fallback: %q", out.Summary)
	}
}

// ---- summarise_target ----

func TestSummariseTarget(t *testing.T) {
	w := &workflowStub{
		modules: makeModulesW(30, "mod"),
		threads: makeThreadsW(40),
		lastException: &gokd.LastException{
			Code:    0xC0000005,
			Address: 0x9000,
		},
	}
	s := newTestSrv(w)
	_, out, err := s.summariseTarget(context.Background(), nil, struct{}{})
	if err != nil {
		t.Fatalf("summariseTarget: %v", err)
	}
	if len(out.TopModules) != 20 {
		t.Errorf("top_modules len=%d want 20", len(out.TopModules))
	}
	if len(out.Threads) != 32 {
		t.Errorf("threads len=%d want 32", len(out.Threads))
	}
	if out.ExceptionPreview == nil {
		t.Errorf("exception preview missing")
	}
	if !strings.Contains(out.Summary, "modules") {
		t.Errorf("summary should mention modules: %q", out.Summary)
	}
}

// ---- walk_stacks_all ----

func TestWalkStacksAllRestoresThread(t *testing.T) {
	w := &workflowStub{
		threads: []*gokd.Thread{{SystemID: 10}, {SystemID: 20}, {SystemID: 30}},
		stackByTID: map[uint32][]*gokd.Frame{
			10: {{InstructionOffset: 0x1000}},
			20: {{InstructionOffset: 0x2000}, {InstructionOffset: 0x2008}},
			30: {{InstructionOffset: 0x3000}},
		},
		currentTID: 99, // sentinel
	}
	s := &srv{sess: w}
	_, out, err := s.walkStacksAll(context.Background(), nil, walkStacksAllInput{})
	if err != nil {
		t.Fatalf("walkStacksAll: %v", err)
	}
	if out.Returned != 3 {
		t.Errorf("returned=%d want 3", out.Returned)
	}
	if out.Items[1].Thread.SystemID != 20 || len(out.Items[1].Frames) != 2 {
		t.Errorf("thread 20 stack wrong: %+v", out.Items[1])
	}
	if w.currentTID != 10 {
		t.Errorf("currentTID=%d, walkStacksAll should restore to origTID (the first thread, 10)", w.currentTID)
	}
}

func TestWalkStacksAllThreadCapAndFrameCap(t *testing.T) {
	threads := make([]*gokd.Thread, 80)
	stacks := map[uint32][]*gokd.Frame{}
	for i := range threads {
		threads[i] = &gokd.Thread{SystemID: uint32(i + 1)}
		f := make([]*gokd.Frame, 50)
		for j := range f {
			f[j] = &gokd.Frame{InstructionOffset: uint64(0x1000 + j)}
		}
		stacks[uint32(i+1)] = f
	}
	w := &workflowStub{threads: threads, stackByTID: stacks, currentTID: 1}
	s := &srv{sess: w}
	_, out, err := s.walkStacksAll(context.Background(), nil, walkStacksAllInput{MaxFrames: 5, MaxThreads: 10})
	if err != nil {
		t.Fatalf("walkStacksAll: %v", err)
	}
	if out.TotalThreads != 80 || out.Returned != 10 || !out.Truncated {
		t.Errorf("expected total=80 returned=10 truncated=true got %+v", out)
	}
	if len(out.Items[0].Frames) != 5 || !out.Items[0].Truncated {
		t.Errorf("first item should have 5 frames and truncated=true: %+v", out.Items[0])
	}
}

// ---- inspect_struct ----

func TestInspectStructFlat(t *testing.T) {
	w := &workflowStub{
		typeFields: map[string][]*gokd.Field{
			"nt!_FOO": {
				{Name: "Header", Offset: 0, TypeName: "uint32"},
				{Name: "Next", Offset: 8, TypeName: "_FOO*"},
			},
		},
		typeSize: map[string]uint64{"nt!_FOO": 16},
	}
	s := &srv{sess: w}
	_, out, err := s.inspectStruct(context.Background(), nil, inspectStructInput{TypeName: "nt!_FOO", Address: "0x1000"})
	if err != nil {
		t.Fatalf("inspectStruct: %v", err)
	}
	if out.SizeBytes != 16 || len(out.Fields) != 2 {
		t.Errorf("got SizeBytes=%d Fields=%d", out.SizeBytes, len(out.Fields))
	}
	if out.Fields[0].Children != nil {
		t.Errorf("recurse=0 should not produce children: %+v", out.Fields[0])
	}
}

func TestInspectStructRecursive(t *testing.T) {
	w := &workflowStub{
		typeFields: map[string][]*gokd.Field{
			"nt!_OUTER": {{Name: "Inner", Offset: 0, TypeName: "_INNER"}},
			"nt!_INNER": {{Name: "Value", Offset: 0, TypeName: "uint32"}},
		},
	}
	s := &srv{sess: w}
	_, out, err := s.inspectStruct(context.Background(), nil, inspectStructInput{
		TypeName: "nt!_OUTER", Address: "0x1000", Recurse: 1,
	})
	if err != nil {
		t.Fatalf("inspectStruct: %v", err)
	}
	if len(out.Fields) != 1 || len(out.Fields[0].Children) != 1 {
		t.Errorf("expected nested child: %+v", out.Fields)
	}
}

func TestInspectStructRequiresQualifiedName(t *testing.T) {
	s := &srv{sess: &workflowStub{}}
	res, _, err := s.inspectStruct(context.Background(), nil, inspectStructInput{TypeName: "_FOO", Address: "0x1000"})
	if err != nil {
		t.Fatalf("inspectStruct unexpected go-level error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected res.IsError=true; res=%+v", res)
	}
}

// ---- read_string ----

func TestReadStringASCII(t *testing.T) {
	data := append([]byte("hello"), 0)
	w := &workflowStub{memReads: map[uint64][]byte{0x1000: data}}
	s := &srv{sess: w}
	_, out, err := s.readString(context.Background(), nil, readStringInput{Address: "0x1000"})
	if err != nil {
		t.Fatalf("readString: %v", err)
	}
	if out.Encoding != "ansi" || out.Value != "hello" {
		t.Errorf("got encoding=%q value=%q", out.Encoding, out.Value)
	}
	if !out.Auto {
		t.Errorf("expected auto=true")
	}
}

func TestReadStringUTF16LE(t *testing.T) {
	// "Hello\0" in UTF-16LE
	data := []byte{'H', 0, 'e', 0, 'l', 0, 'l', 0, 'o', 0, 0, 0}
	w := &workflowStub{memReads: map[uint64][]byte{0x1000: data}}
	s := &srv{sess: w}
	_, out, err := s.readString(context.Background(), nil, readStringInput{Address: "0x1000"})
	if err != nil {
		t.Fatalf("readString: %v", err)
	}
	if out.Encoding != "utf16le" {
		t.Errorf("auto-detect failed, got encoding=%q", out.Encoding)
	}
	if out.Value != "Hello" {
		t.Errorf("decoded value=%q want %q", out.Value, "Hello")
	}
}

func TestReadStringTruncated(t *testing.T) {
	// 8 bytes ASCII no NUL
	data := []byte("ABCDEFGH")
	w := &workflowStub{memReads: map[uint64][]byte{0x1000: data}}
	s := &srv{sess: w}
	_, out, err := s.readString(context.Background(), nil, readStringInput{Address: "0x1000", Encoding: "ansi", MaxBytes: 8})
	if err != nil {
		t.Fatalf("readString: %v", err)
	}
	if !out.Truncated {
		t.Errorf("expected truncated=true with no NUL")
	}
	if out.Value != "ABCDEFGH" {
		t.Errorf("got value=%q", out.Value)
	}
}

func TestReadStringRejectsBadEncoding(t *testing.T) {
	w := &workflowStub{memReads: map[uint64][]byte{0x1000: []byte("hi")}}
	s := &srv{sess: w}
	res, _, err := s.readString(context.Background(), nil, readStringInput{Address: "0x1000", Encoding: "ebcdic"})
	if err != nil {
		t.Fatalf("readString unexpected go-level error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected res.IsError=true for bad encoding")
	}
}

// ---- dump_memory ----

func TestDumpMemory(t *testing.T) {
	data := []byte{0x00, 0x01, 0x02, 0x03, 0x41, 0x42, 0x43, 0x44, 0x10, 0x20, 0x30, 0x40, 0x7E, 0x7F, 0x80, 0xFF, 0xDE, 0xAD}
	w := &workflowStub{memReads: map[uint64][]byte{0x1000: data}}
	s := &srv{sess: w}
	_, out, err := s.dumpMemory(context.Background(), nil, dumpMemoryInput{Address: "0x1000", Length: 18, Width: 1})
	if err != nil {
		t.Fatalf("dumpMemory: %v", err)
	}
	if out.Bytes != 18 || len(out.Rows) != 2 {
		t.Errorf("got bytes=%d rows=%d", out.Bytes, len(out.Rows))
	}
	if !strings.Contains(out.Rows[0].ASCII, "ABCD") {
		t.Errorf("row 0 ASCII gutter wrong: %q", out.Rows[0].ASCII)
	}
}

func TestDumpMemoryRejectsBadWidth(t *testing.T) {
	w := &workflowStub{memReads: map[uint64][]byte{0x1000: []byte{0, 1, 2}}}
	s := &srv{sess: w}
	res, _, err := s.dumpMemory(context.Background(), nil, dumpMemoryInput{Address: "0x1000", Length: 3, Width: 3})
	if err != nil {
		t.Fatalf("dumpMemory unexpected go-level error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected res.IsError=true for bad width")
	}
}

func TestDumpMemoryCapsLength(t *testing.T) {
	big := make([]byte, 8192)
	for i := range big {
		big[i] = byte(i)
	}
	w := &workflowStub{memReads: map[uint64][]byte{0x1000: big}}
	s := &srv{sess: w}
	_, out, err := s.dumpMemory(context.Background(), nil, dumpMemoryInput{Address: "0x1000", Length: 8192, Width: 4})
	if err != nil {
		t.Fatalf("dumpMemory: %v", err)
	}
	if out.Bytes != maxReadMemoryBytes {
		t.Errorf("bytes=%d want capped at %d", out.Bytes, maxReadMemoryBytes)
	}
	if !out.Truncated {
		t.Errorf("expected truncated=true")
	}
}

// ---- helpers ----

func makeModulesW(n int, prefix string) []*gokd.Module {
	out := make([]*gokd.Module, n)
	for i := 0; i < n; i++ {
		out[i] = &gokd.Module{Name: prefix + fmt.Sprintf("%04d", i), Base: uint64(i + 1), Size: 4096}
	}
	return out
}

func makeThreadsW(n int) []*gokd.Thread {
	out := make([]*gokd.Thread, n)
	for i := 0; i < n; i++ {
		out[i] = &gokd.Thread{SystemID: uint32(i + 1)}
	}
	return out
}

func newTestSrv(sess gokd.Session) *srv {
	return newSrv(sess, false, false)
}
