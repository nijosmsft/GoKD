package main

import (
	"context"

	"github.com/nijosmsft/gokd"
)

// stubSession satisfies gokd.Session with zero-value returns. It exists so
// the MCP server can be exercised in unit tests without a live DbgEng
// session — registerTools, schema generation, and readonly filtering can
// all be validated against this stub.
//
// Methods that return pointers return nil; methods that return slices or
// strings return their zero value; methods that return error return nil
// so the call appears to succeed. Individual tests that need a particular
// behaviour should embed *stubSession in a wrapper type that overrides
// just the methods of interest.
type stubSession struct{}

var _ gokd.Session = (*stubSession)(nil)

func (*stubSession) AttachProcess(pid uint32, opts gokd.AttachOptions) error { return nil }
func (*stubSession) CreateProcess(cmd string, opts gokd.CreateOptions) error { return nil }
func (*stubSession) AttachKernel(ctx context.Context, connectStr string, opts gokd.KernelOptions) error {
	return nil
}
func (*stubSession) OpenDump(path string) error                                       { return nil }
func (*stubSession) WriteDump(ctx context.Context, path string, opts gokd.WriteDumpOptions) error { return nil }
func (*stubSession) Detach() error                                                    { return nil }
func (*stubSession) ConnectRemote(connection string) error                            { return nil }
func (*stubSession) DisconnectRemote() error                                          { return nil }
func (*stubSession) Go(ctx context.Context) (*gokd.StopEvent, error)                  { return nil, nil }
func (*stubSession) StepIn(ctx context.Context) (*gokd.StopEvent, error)              { return nil, nil }
func (*stubSession) StepOver(ctx context.Context) (*gokd.StopEvent, error)            { return nil, nil }
func (*stubSession) StepOut(ctx context.Context) (*gokd.StopEvent, error)             { return nil, nil }
func (*stubSession) BreakIn() error                                                   { return nil }
func (*stubSession) ReadMemory(addr uint64, n uint64) ([]byte, error)                 { return nil, nil }
func (*stubSession) WriteMemory(addr uint64, data []byte) error                       { return nil }
func (*stubSession) ReadPhysical(addr uint64, n uint64) ([]byte, error)               { return nil, nil }
func (*stubSession) Registers() (*gokd.RegisterSet, error)                            { return nil, nil }
func (*stubSession) SetRegister(name string, value uint64) error                      { return nil }
func (*stubSession) Stack() ([]*gokd.Frame, error)                                    { return nil, nil }
func (*stubSession) Threads() ([]*gokd.Thread, error)                                 { return nil, nil }
func (*stubSession) SetThread(sysTID uint32) error                                    { return nil }
func (*stubSession) Modules() ([]*gokd.Module, error)                                 { return nil, nil }
func (*stubSession) NameToAddr(name string) (uint64, error)                           { return 0, nil }
func (*stubSession) AddrToName(addr uint64) (string, uint64, error)                   { return "", 0, nil }
func (*stubSession) SetSymbolPath(path string) error                                  { return nil }
func (*stubSession) SymbolPath() (string, error)                                      { return "", nil }
func (*stubSession) ReloadSymbols(ctx context.Context, spec string) error             { return nil }
func (*stubSession) SymFix(cache string) error                                        { return nil }
func (*stubSession) TypeSize(module, typeName string) (uint64, error)                 { return 0, nil }
func (*stubSession) TypeFields(module, typeName string) ([]*gokd.Field, error)        { return nil, nil }
func (*stubSession) AddBreakpoint(addr uint64) (*gokd.Breakpoint, error)              { return nil, nil }
func (*stubSession) AddBreakpointSym(symbol string) (*gokd.Breakpoint, error)         { return nil, nil }
func (*stubSession) RemoveBreakpoint(id uint32) error                                 { return nil }
func (*stubSession) EnableBreakpoint(id uint32, enabled bool) error                   { return nil }
func (*stubSession) Breakpoints() ([]*gokd.Breakpoint, error)                         { return nil, nil }
func (*stubSession) AddDataBreakpoint(addr uint64, size uint32, access gokd.BreakpointAccess) (*gokd.Breakpoint, error) {
	return nil, nil
}
func (*stubSession) ConfigureBreakpoint(id uint32, opts gokd.BreakpointOptions) error { return nil }
func (*stubSession) BreakpointCommand(id uint32) (string, error)                      { return "", nil }
func (*stubSession) Disassemble(addr uint64) (*gokd.Instruction, error)               { return nil, nil }
func (*stubSession) DisassembleRange(addr uint64, count int) ([]*gokd.Instruction, error) {
	return nil, nil
}
func (*stubSession) Evaluate(ctx context.Context, expr string, desired gokd.ValueKind) (gokd.Value, uint32, error) {
	return gokd.Value{}, 0, nil
}
func (*stubSession) Radix() (uint32, error)                                  { return 0, nil }
func (*stubSession) SetRadix(r uint32) error                                 { return nil }
func (*stubSession) ExpressionSyntax() (gokd.ExpressionSyntax, error)        { return 0, nil }
func (*stubSession) SetExpressionSyntax(syn gokd.ExpressionSyntax) error     { return nil }
func (*stubSession) AddrToLine(address uint64) (gokd.SourceLine, error)      { return gokd.SourceLine{}, nil }
func (*stubSession) LineToAddr(file string, line uint32) (uint64, error)     { return 0, nil }
func (*stubSession) AddBreakpointSourceLine(file string, line uint32) (*gokd.Breakpoint, error) {
	return nil, nil
}
func (*stubSession) SearchMemory(start, length uint64, pattern []byte, granularity uint32) (uint64, error) {
	return 0, nil
}
func (*stubSession) VirtualToPhysical(va uint64) (uint64, error)             { return 0, nil }
func (*stubSession) QueryRegion(va uint64) (gokd.MemoryRegion, error)        { return gokd.MemoryRegion{}, nil }
func (*stubSession) Events() <-chan gokd.Event                               { return nil }
func (*stubSession) Output() <-chan string                                   { return nil }
func (*stubSession) Execute(cmd string) (string, error)                      { return "", nil }
func (*stubSession) LastException() (*gokd.LastException, error)             { return nil, nil }
func (*stubSession) BugCheck() (*gokd.BugCheck, error)                       { return nil, nil }
func (*stubSession) DumpType(ctx context.Context, module, typeName string, addr uint64, opts gokd.DumpTypeOptions) (*gokd.TypeValue, error) {
	return nil, nil
}
func (*stubSession) Close() error { return nil }
