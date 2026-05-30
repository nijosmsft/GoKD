package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var updateGolden = flag.Bool("update-golden", false, "regenerate the testdata golden files")

// toolEntry is the per-tool snapshot persisted to the golden file. We
// inline name + description + annotations (clients depend on those) but
// hash the input/output schemas so the golden stays readable; any
// schema drift still trips the test.
type toolEntry struct {
	Name             string            `json:"name"`
	Description      string            `json:"description"`
	InputSchemaHash  string            `json:"input_schema_sha256,omitempty"`
	OutputSchemaHash string            `json:"output_schema_sha256,omitempty"`
	Annotations      *toolAnnotations  `json:"annotations,omitempty"`
}

type toolAnnotations struct {
	ReadOnlyHint    bool  `json:"read_only_hint,omitempty"`
	DestructiveHint *bool `json:"destructive_hint,omitempty"`
	IdempotentHint  bool  `json:"idempotent_hint,omitempty"`
	OpenWorldHint   *bool `json:"open_world_hint,omitempty"`
}

type toolsSnapshot struct {
	Tools []toolEntry `json:"tools"`
}

type promptArgEntry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type promptEntry struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Arguments   []promptArgEntry `json:"arguments,omitempty"`
}

type promptsSnapshot struct {
	Prompts []promptEntry `json:"prompts"`
}

// snapshotTools enumerates registered tools via an in-memory MCP client
// so we observe exactly what a remote client would see.
func snapshotTools(t *testing.T, server *mcp.Server) toolsSnapshot {
	t.Helper()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "schema-test-client", Version: "test"}, nil)
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

	entries := make([]toolEntry, 0, len(res.Tools))
	for _, tt := range res.Tools {
		e := toolEntry{Name: tt.Name, Description: tt.Description}
		if tt.InputSchema != nil {
			e.InputSchemaHash = hashJSON(tt.InputSchema)
		}
		if tt.OutputSchema != nil {
			e.OutputSchemaHash = hashJSON(tt.OutputSchema)
		}
		if tt.Annotations != nil {
			e.Annotations = &toolAnnotations{
				DestructiveHint: tt.Annotations.DestructiveHint,
				ReadOnlyHint:    tt.Annotations.ReadOnlyHint,
				IdempotentHint:  tt.Annotations.IdempotentHint,
				OpenWorldHint:   tt.Annotations.OpenWorldHint,
			}
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return toolsSnapshot{Tools: entries}
}

func snapshotPrompts(t *testing.T, server *mcp.Server) promptsSnapshot {
	t.Helper()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "schema-test-client", Version: "test"}, nil)
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

	res, err := cliSess.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}

	entries := make([]promptEntry, 0, len(res.Prompts))
	for _, p := range res.Prompts {
		e := promptEntry{Name: p.Name, Description: p.Description}
		for _, a := range p.Arguments {
			e.Arguments = append(e.Arguments, promptArgEntry{
				Name:        a.Name,
				Description: a.Description,
				Required:    a.Required,
			})
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return promptsSnapshot{Prompts: entries}
}

func hashJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("json-error:%v", err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func goldenCompare(t *testing.T, name, path string, got []byte) {
	t.Helper()
	if *updateGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v (run go test -update-golden to seed)", path, err)
	}
	if string(want) != string(got) {
		t.Errorf("%s drift. Run go test -update-golden if intentional.\nfirst diff:\n%s", name, firstDiff(string(want), string(got)))
	}
}

func firstDiff(a, b string) string {
	min := len(a)
	if len(b) < min {
		min = len(b)
	}
	for i := 0; i < min; i++ {
		if a[i] != b[i] {
			lo := i - 40
			if lo < 0 {
				lo = 0
			}
			ahi := i + 80
			if ahi > len(a) {
				ahi = len(a)
			}
			bhi := i + 80
			if bhi > len(b) {
				bhi = len(b)
			}
			return fmt.Sprintf("at byte %d:\nwant: %q\n got: %q", i, a[lo:ahi], b[lo:bhi])
		}
	}
	if len(a) != len(b) {
		return fmt.Sprintf("length differs: want=%d got=%d", len(a), len(b))
	}
	return "(identical)"
}

func newTestServer(t *testing.T) *mcp.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "gokd-mcp", Version: "test"}, nil)
	s := newSrv(&stubSession{}, false, false)
	// Register the composite tools too so the schema golden / description
	// style tests cover them. The lablinkClient is intentionally left nil:
	// the handlers reject calls with a clear error in that state, but the
	// schemas are visible to ListTools.
	s.lablinkEnabled = true
	registerTools(server, s)
	return server
}

func TestToolsSchemaGolden(t *testing.T) {
	server := newTestServer(t)
	snap := snapshotTools(t, server)
	got, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = append(got, '\n')
	goldenCompare(t, "tools_schema", filepath.Join("testdata", "tools_schema.golden.json"), got)
}

func TestPromptsSchemaGolden(t *testing.T) {
	server := newTestServer(t)
	snap := snapshotPrompts(t, server)
	got, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = append(got, '\n')
	goldenCompare(t, "prompts_schema", filepath.Join("testdata", "prompts_schema.golden.json"), got)
}

// Action verbs every tool description must start with. Kept in one
// place so the t2-7 style guide is enforced consistently.
var actionVerbs = []string{
	"Attaches", "Creates", "Opens", "Detaches", "Connects", "Disconnects",
	"Returns", "Reads", "Writes", "Resolves", "Lists", "Switches",
	"Searches", "Translates", "Walks", "Steps", "Resumes", "Interrupts",
	"Executes", "Disassembles", "Forces", "Sets", "Installs", "Snapshots",
	"Applies", "Enables", "Deletes", "Configures", "Evaluates",
	"Pretty-prints", "Dumps", "Runs", "Maps", "Fetches", "Gets",
	// workflow tools
	"One-shot",
}

func TestToolDescriptionsAreSubstantive(t *testing.T) {
	server := newTestServer(t)
	snap := snapshotTools(t, server)
	for _, e := range snap.Tools {
		if len(e.Description) < 80 {
			t.Errorf("%s: description too short (%d chars): %q", e.Name, len(e.Description), e.Description)
		}
		startsOK := false
		for _, v := range actionVerbs {
			if strings.HasPrefix(e.Description, v) {
				startsOK = true
				break
			}
		}
		if !startsOK {
			t.Errorf("%s: description does not start with an action verb: %q", e.Name, headLine(e.Description))
		}
	}
}

func TestPromptsRegistered(t *testing.T) {
	server := newTestServer(t)
	snap := snapshotPrompts(t, server)
	want := []string{
		"triage-dump", "attach-and-orient", "find-who-wrote",
		"why-blocked", "inspect-object", "kernel-attach-kdnet",
	}
	got := map[string]bool{}
	for _, p := range snap.Prompts {
		got[p.Name] = true
	}
	for _, n := range want {
		if !got[n] {
			t.Errorf("prompt %q not registered", n)
		}
	}
}

func TestParseHexUint64Roundtrip(t *testing.T) {
	cases := []struct {
		in      string
		want    uint64
		wantErr bool
	}{
		{"0x0", 0, false},
		{"0xFFFFFFFFFFFFFFFF", ^uint64(0), false},
		{"0", 0, false},
		{"0xdeadbeef", 0xdeadbeef, false},
		{"0x10000000000000000", 0, true},
		{"", 0, true},
		{"0xZZ", 0, true},
	}
	for _, c := range cases {
		got, err := parseHexUint64(c.in, "addr")
		if (err != nil) != c.wantErr {
			t.Errorf("parseHexUint64(%q) wantErr=%v got err=%v", c.in, c.wantErr, err)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("parseHexUint64(%q)=0x%x want 0x%x", c.in, got, c.want)
		}
	}
}

func headLine(s string) string {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return s[:i+1]
	}
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}
