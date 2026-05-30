package main

import (
	"context"
	"errors"
	"io"
	"testing"

	pb "github.com/nijosmsft/lablink/proto/agent"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// fakeForwardStream implements pb.NodeAgent_ForwardClient for unit tests
// of authenticateRemoteStream / readAuthResp. Send is recorded; Recv
// returns from a pre-loaded queue then io.EOF.
type fakeForwardStream struct {
	grpc.ClientStream
	sent []*pb.ForwardChunk
	recv []*pb.ForwardChunk
	idx  int
}

func (f *fakeForwardStream) Send(c *pb.ForwardChunk) error {
	f.sent = append(f.sent, c)
	return nil
}

func (f *fakeForwardStream) Recv() (*pb.ForwardChunk, error) {
	if f.idx >= len(f.recv) {
		return nil, io.EOF
	}
	c := f.recv[f.idx]
	f.idx++
	return c, nil
}

func (f *fakeForwardStream) CloseSend() error             { return nil }
func (f *fakeForwardStream) Header() (metadata.MD, error) { return nil, nil }
func (f *fakeForwardStream) Trailer() metadata.MD         { return nil }
func (f *fakeForwardStream) Context() context.Context     { return context.Background() }
func (f *fakeForwardStream) SendMsg(m any) error          { return nil }
func (f *fakeForwardStream) RecvMsg(m any) error          { return nil }

func TestReadAuthRespOK(t *testing.T) {
	s := &fakeForwardStream{recv: []*pb.ForwardChunk{
		{Data: []byte("OK\n")},
	}}
	if err := readAuthResp(s); err != nil {
		t.Fatalf("readAuthResp OK: %v", err)
	}
}

func TestReadAuthRespDenied(t *testing.T) {
	s := &fakeForwardStream{recv: []*pb.ForwardChunk{
		{Data: []byte("DENIED\n")},
	}}
	err := readAuthResp(s)
	if err == nil {
		t.Fatal("expected denial error")
	}
}

func TestReadAuthRespSplitFrames(t *testing.T) {
	s := &fakeForwardStream{recv: []*pb.ForwardChunk{
		{Data: []byte("O")},
		{Data: []byte("K\n")},
	}}
	if err := readAuthResp(s); err != nil {
		t.Fatalf("readAuthResp split: %v", err)
	}
}

func TestReadAuthRespEarlyClose(t *testing.T) {
	s := &fakeForwardStream{recv: []*pb.ForwardChunk{
		{Close: true},
	}}
	err := readAuthResp(s)
	if err == nil {
		t.Fatal("expected error on early close")
	}
}

func TestAuthenticateRemoteStreamEmptyToken(t *testing.T) {
	s := &fakeForwardStream{}
	if err := authenticateRemoteStream(s, ""); err != nil {
		t.Fatalf("empty token should be no-op: %v", err)
	}
	if len(s.sent) != 0 {
		t.Errorf("empty token must not send: %v", s.sent)
	}
}

func TestAuthenticateRemoteStreamSendsAndRecvs(t *testing.T) {
	s := &fakeForwardStream{recv: []*pb.ForwardChunk{
		{Data: []byte("OK\n")},
	}}
	if err := authenticateRemoteStream(s, "ABCDEFGHIJKLMNOP1234"); err != nil {
		t.Fatalf("authenticateRemoteStream: %v", err)
	}
	if len(s.sent) != 1 {
		t.Fatalf("sent %d frames want 1", len(s.sent))
	}
	got := string(s.sent[0].Data)
	want := "AUTH ABCDEFGHIJKLMNOP1234\n"
	if got != want {
		t.Errorf("AUTH frame=%q want %q", got, want)
	}
}

func TestIsTerminalStatus(t *testing.T) {
	cases := map[pb.JobStatus]bool{
		pb.JobStatus_JOB_STATUS_UNSPECIFIED: false,
		pb.JobStatus_JOB_STATUS_RUNNING:     false,
		pb.JobStatus_JOB_STATUS_EXITED:      true,
		pb.JobStatus_JOB_STATUS_CANCELED:    true,
		pb.JobStatus_JOB_STATUS_ORPHANED:    true,
	}
	for s, want := range cases {
		if got := isTerminalStatus(s); got != want {
			t.Errorf("isTerminalStatus(%v)=%v want %v", s, got, want)
		}
	}
}

func TestNewAuthToken(t *testing.T) {
	a, err := newAuthToken()
	if err != nil {
		t.Fatalf("newAuthToken: %v", err)
	}
	b, err := newAuthToken()
	if err != nil {
		t.Fatalf("newAuthToken: %v", err)
	}
	if a == b {
		t.Errorf("two tokens collided: %s", a)
	}
	if len(a) != 64 { // 32 bytes hex
		t.Errorf("token length=%d want 64", len(a))
	}
	for _, r := range a {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("non-hex char %q in token %q", r, a)
		}
	}
}

// silence unused-import on test-only setups
var _ = errors.New
