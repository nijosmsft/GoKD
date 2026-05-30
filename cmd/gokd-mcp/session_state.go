package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nijosmsft/gokd"
)

// getSessionStateOutput is the structured snapshot returned by
// get_session_state — a single call that answers "what is the state of
// the world and what should I do next?" without 5-6 chained tool calls.
type getSessionStateOutput struct {
	Attached             bool              `json:"attached"`
	TargetKind           string            `json:"target_kind,omitempty"`
	TargetName           string            `json:"target_name,omitempty"`
	Status               string            `json:"status"`
	Radix                uint32            `json:"radix,omitempty"`
	ExpressionSyntax     string            `json:"expression_syntax,omitempty"`
	SymbolPath           string            `json:"symbol_path,omitempty"`
	Threads              int               `json:"threads,omitempty"`
	Modules              int               `json:"modules,omitempty"`
	Breakpoints          int               `json:"breakpoints"`
	LastEvent            *lastEventSummary `json:"last_event,omitempty"`
	PendingEvents        int               `json:"pending_events"`
	PendingOutput        int               `json:"pending_output"`
	RecommendedNextTools []string          `json:"recommended_next_tools,omitempty"`
	Caveats              []string          `json:"caveats,omitempty"`
}

type lastEventSummary struct {
	Seq         uint64    `json:"seq"`
	Kind        string    `json:"kind"`
	At          time.Time `json:"at"`
	Description string    `json:"description"`
	AddressHex  string    `json:"address_hex,omitempty"`
	ThreadID    uint32    `json:"thread_id,omitempty"`
}

// HRESULT codes we probe for to map an error from Threads()/Modules() into
// an attached/running/no-target status. We can't call
// IDebugControl::GetExecutionStatus directly (the gokd.Session interface
// doesn't expose it), so we use the cheapest read-only call we have and
// translate its error into the same three-way answer.
const (
	hrTargetRunning = 0x80004005 // E_FAIL — engine refuses query while running
	hrNoTarget      = 0x80070006 // E_HANDLE — no current target
	hrNoCurrentProc = 0x8000FFFF // E_UNEXPECTED — no current process
)

// probeStatus calls Threads() and turns the result into one of
// {"no_target","running","broken_in","unknown"}. It returns the threads
// slice (or nil) so the caller can re-use it without a second round-trip.
func probeStatus(sess gokd.Session) (status string, threads []*gokd.Thread, attached bool) {
	threads, err := sess.Threads()
	if err == nil {
		return "broken_in", threads, true
	}
	if errors.Is(err, gokd.ErrSessionClosed) {
		return "no_target", nil, false
	}
	var hr gokd.HRESULTError
	if errors.As(err, &hr) {
		code := uint32(int32(hr))
		switch code {
		case hrNoTarget, hrNoCurrentProc:
			return "no_target", nil, false
		case hrTargetRunning:
			return "running", nil, true
		}
	}
	return "unknown", nil, false
}

func (s *srv) getSessionState(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, getSessionStateOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[getSessionStateOutput]("get_session_state", err)
	}
	out := getSessionStateOutput{}

	status, threads, attached := probeStatus(s.sess)
	out.Status = status
	out.Attached = attached
	if threads != nil {
		out.Threads = len(threads)
	}

	if s.status != nil {
		kind, name, last := s.status.snapshot()
		out.TargetKind = kind
		out.TargetName = name
		// Drainer's lastKind wins when the probe says "broken_in" but the
		// drainer has more specific info (e.g. it saw a ProcessExited).
		if last == "exited" {
			out.Status = "exited"
		}
	}

	if attached {
		if mods, err := s.sess.Modules(); err == nil {
			out.Modules = len(mods)
		}
		if bps, err := s.sess.Breakpoints(); err == nil {
			out.Breakpoints = len(bps)
		}
		if r, err := s.sess.Radix(); err == nil {
			out.Radix = r
		}
		if syn, err := s.sess.ExpressionSyntax(); err == nil {
			out.ExpressionSyntax = expressionSyntaxString(syn)
		}
		if p, err := s.sess.SymbolPath(); err == nil {
			out.SymbolPath = p
		}
	}

	if s.eventRing != nil {
		out.PendingEvents = s.eventRing.Len()
		if last, ok := s.eventRing.Last(); ok {
			out.LastEvent = &lastEventSummary{
				Seq:         last.Seq,
				Kind:        last.Kind,
				At:          last.At,
				Description: last.Description,
				AddressHex:  last.AddressHex,
				ThreadID:    last.ThreadID,
			}
		}
	}
	if s.outputRing != nil {
		out.PendingOutput = s.outputRing.Len()
	}

	out.RecommendedNextTools = recommendNext(out)
	out.Caveats = sessionStateCaveats(out)

	return nil, out, nil
}

// recommendNext returns the canonical follow-up tools for the current
// status; the LLM uses this list to plan the next call without having to
// re-derive it from a description.
func recommendNext(s getSessionStateOutput) []string {
	switch s.Status {
	case "no_target":
		return []string{"attach_process", "create_process", "open_dump", "attach_kernel"}
	case "running":
		return []string{"break_in", "get_recent_events", "get_recent_output"}
	case "exited":
		return []string{"detach", "open_dump"}
	case "broken_in":
		if s.LastEvent != nil && s.LastEvent.Kind == "exception" {
			return []string{"last_exception", "get_stack", "get_registers", "triage_crash"}
		}
		return []string{"get_stack", "get_registers", "get_modules", "disassemble"}
	}
	return nil
}

// sessionStateCaveats lists known limitations of the snapshot so the LLM
// does not over-trust missing fields.
func sessionStateCaveats(s getSessionStateOutput) []string {
	var c []string
	if s.Attached && s.Status == "unknown" {
		c = append(c, "status probe returned an unexpected HRESULT; consider break_in")
	}
	return c
}

func expressionSyntaxString(syn gokd.ExpressionSyntax) string {
	switch syn {
	case gokd.ExpressionSyntaxMASM:
		return "masm"
	case gokd.ExpressionSyntaxCPP:
		return "cpp"
	default:
		return fmt.Sprintf("unknown(%d)", int(syn))
	}
}

// ---- get_recent_events / get_recent_output (pull fallback for t2-6) ----

type getRecentEventsInput struct {
	SinceToken uint64 `json:"since_token,omitempty" jsonschema:"last seq the client has already seen (0 = start of the ring)"`
	Limit      int    `json:"limit,omitempty" jsonschema:"max items to return (default 32, max 32)"`
}

type getRecentEventsOutput struct {
	Items     []ringEvent `json:"items"`
	NextToken uint64      `json:"next_token"`
	Dropped   int         `json:"dropped,omitempty"`
	Truncated bool        `json:"truncated,omitempty"`
}

func (s *srv) getRecentEvents(ctx context.Context, _ *mcp.CallToolRequest, in getRecentEventsInput) (*mcp.CallToolResult, getRecentEventsOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[getRecentEventsOutput]("get_recent_events", err)
	}
	out := getRecentEventsOutput{NextToken: in.SinceToken}
	if s.eventRing == nil {
		return nil, out, nil
	}
	items, dropped := s.eventRing.Since(in.SinceToken)
	limit := in.Limit
	if limit <= 0 || limit > maxRecentEventsLimit {
		limit = defaultRecentEventsLimit
	}
	if len(items) > limit {
		items = items[:limit]
		out.Truncated = true
	}
	out.Items = items
	out.Dropped = dropped
	if len(items) > 0 {
		out.NextToken = items[len(items)-1].Seq
	}
	return nil, out, nil
}

type getRecentOutputInput struct {
	SinceToken uint64 `json:"since_token,omitempty" jsonschema:"last seq the client has already seen (0 = start of the ring)"`
	Limit      int    `json:"limit,omitempty" jsonschema:"max items to return (default 64, max 256)"`
}

type getRecentOutputOutput struct {
	Items     []ringOutput `json:"items"`
	NextToken uint64       `json:"next_token"`
	Dropped   int          `json:"dropped,omitempty"`
	Truncated bool         `json:"truncated,omitempty"`
}

func (s *srv) getRecentOutput(ctx context.Context, _ *mcp.CallToolRequest, in getRecentOutputInput) (*mcp.CallToolResult, getRecentOutputOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[getRecentOutputOutput]("get_recent_output", err)
	}
	out := getRecentOutputOutput{NextToken: in.SinceToken}
	if s.outputRing == nil {
		return nil, out, nil
	}
	items, dropped := s.outputRing.Since(in.SinceToken)
	limit := in.Limit
	if limit <= 0 {
		limit = defaultRecentOutputLimit
	}
	if limit > maxRecentOutputLimit {
		limit = maxRecentOutputLimit
	}
	if len(items) > limit {
		items = items[:limit]
		out.Truncated = true
	}
	out.Items = items
	out.Dropped = dropped
	if len(items) > 0 {
		out.NextToken = items[len(items)-1].Seq
	}
	return nil, out, nil
}

// firstTokenIfSet is a tiny helper used by tests to assert that the
// recommended_next_tools list always starts with the named tool.
func firstTokenIfSet(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return strings.TrimSpace(s[0])
}
