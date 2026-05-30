package main

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// listToolNames runs a registerTools-populated server through an in-memory
// transport and returns the set of registered tool names. It is the test
// equivalent of "tools/list" from a client's perspective.
func listToolNames(t *testing.T, s *srv) map[string]bool {
	t.Helper()
	ctx := context.Background()

	server := mcp.NewServer(&mcp.Implementation{Name: "gokd-mcp-test", Version: "test"}, nil)
	registerTools(server, s)

	client := mcp.NewClient(&mcp.Implementation{Name: "gokd-mcp-test-client", Version: "test"}, nil)
	st, ct := mcp.NewInMemoryTransports()
	srvSess, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	defer srvSess.Close()
	cliSess, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer cliSess.Close()

	res, err := cliSess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := map[string]bool{}
	for _, tt := range res.Tools {
		got[tt.Name] = true
	}
	return got
}

func TestReadonlyDropsMutatingTools(t *testing.T) {
	s := &srv{sess: &stubSession{}, readonly: true}
	got := listToolNames(t, s)

	for name := range mutatingTools {
		if got[name] {
			t.Errorf("readonly mode still registered mutating tool %q", name)
		}
	}
	if got["execute_raw"] {
		t.Errorf("readonly mode (no --unsafe-raw) still registered execute_raw")
	}
	for _, name := range []string{
		"get_modules", "get_threads", "get_stack", "get_registers",
		"read_memory", "read_physical", "disassemble",
		"name_to_addr", "addr_to_name", "evaluate",
		"list_breakpoints", "last_exception", "bug_check",
	} {
		if !got[name] {
			t.Errorf("readonly mode dropped read-only tool %q", name)
		}
	}
}

func TestReadonlyUnsafeRawRegistersExecuteRaw(t *testing.T) {
	s := &srv{sess: &stubSession{}, readonly: true, unsafeRaw: true}
	got := listToolNames(t, s)

	if !got["execute_raw"] {
		t.Errorf("--readonly --unsafe-raw should register execute_raw")
	}
	for name := range mutatingTools {
		if got[name] {
			t.Errorf("readonly mode still registered mutating tool %q", name)
		}
	}
}

func TestDefaultRegistersEverything(t *testing.T) {
	s := &srv{sess: &stubSession{}}
	got := listToolNames(t, s)

	for name := range mutatingTools {
		if !got[name] {
			t.Errorf("default mode missing mutating tool %q", name)
		}
	}
	if !got["execute_raw"] {
		t.Errorf("default mode missing execute_raw")
	}
}

func TestRawDenylist(t *testing.T) {
	cases := []struct {
		cmd  string
		deny bool
	}{
		// Lifetime / process control
		{"q", true}, {"qq", true}, {"qd", true},
		{".kill", true}, {".restart", true},
		{".create notepad.exe", true}, {".attach 0n1234", true},
		{".detach", true}, {".shell cmd", true},

		// Filesystem / module side effects
		{".dump /ma c:\\foo.dmp", true},
		{".writemem c:\\foo 0 100", true},
		{".load foo", true}, {".loadby foo nt", true},
		{".unload foo", true}, {".logopen c:\\log.txt", true},

		// Memory writes
		{"e 0 90", true}, {"eb 0 90", true}, {"ed 0 12345", true},
		{"ew 0 1234", true}, {"eq 0 1234", true},
		{"ep 0 1234", true}, {"ea 0 hello", true},
		{"f 0 L4 90", true},

		// Execution
		{"g", true}, {"gh", true}, {"gu", true},
		{"p", true}, {"pa nt!foo", true},
		{"t", true}, {"ta nt!foo", true}, {"tb", true},
		{"wt", true},

		// Breakpoints
		{"bp nt!foo", true}, {"bm nt!foo*", true}, {"bu nt!foo", true},
		{"ba r 4 nt!g", true}, {"bc *", true}, {"bd *", true}, {"be *", true},

		// Should NOT be denied
		{"k", false}, {"kb", false}, {"kn 50", false},
		{"dt nt!_PEB", false},
		{"r", false}, {"r eax", false},
		{"!process 0 0", false},
		{"!analyze -v", false},
		{"lm", false}, {"x nt!*", false},
		{"u nt!foo", false},
		{"d 0", false}, {"db 0", false}, {"dq 0", false},
		{"bl", false}, // list breakpoints is read-only
		{".sympath", false}, {".reload", false},
		{"!handle", false},
		{"version", false},
		{"", false},
	}

	for _, c := range cases {
		got := denyRawCommand(c.cmd)
		if got != c.deny {
			t.Errorf("denyRawCommand(%q) = %v, want %v", c.cmd, got, c.deny)
		}
	}
}

func TestFirstToken(t *testing.T) {
	cases := []struct{ in, want string }{
		{"q", "q"},
		{"  q  ", "q"},
		{".dump /ma c:\\foo.dmp", ".dump"},
		{"eb\t0 90", "eb"},
		{"", ""},
	}
	for _, c := range cases {
		if got := firstToken(c.in); got != c.want {
			t.Errorf("firstToken(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
