package main

import (
	"context"
	"net"
	"strings"
	"testing"
)

func TestSetupKernelDebugValidation(t *testing.T) {
	ctx := context.Background()
	s := &srv{sess: &stubSession{}}

	cases := []struct {
		name   string
		in     setupKernelInput
		wantIn string // substring expected in the structured error message
	}{
		{"missing confirm", setupKernelInput{Node: "n", Host: "1.2.3.4", Port: 50000, Key: "1.2.3.4"}, "confirm_reboot"},
		{"missing node", setupKernelInput{ConfirmReboot: true, Host: "1.2.3.4", Port: 50000, Key: "1.2.3.4"}, "node is required"},
		{"missing host", setupKernelInput{ConfirmReboot: true, Node: "n", Port: 50000, Key: "1.2.3.4"}, "host is required"},
		{"missing key", setupKernelInput{ConfirmReboot: true, Node: "n", Host: "1.2.3.4", Port: 50000}, "key is required"},
		{"low port", setupKernelInput{ConfirmReboot: true, Node: "n", Host: "1.2.3.4", Port: 80, Key: "1.2.3.4"}, "port must be in 1024..65535"},
		{"bad key", setupKernelInput{ConfirmReboot: true, Node: "n", Host: "1.2.3.4", Port: 50000, Key: "notdotted"}, "must be four dotted decimal"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, _, err := s.setupKernelDebug(ctx, nil, c.in)
			if err != nil {
				t.Fatalf("unexpected go error: %v", err)
			}
			if res == nil || !res.IsError {
				t.Fatalf("expected IsError result, got %+v", res)
			}
			joined := ""
			for _, c := range res.Content {
				if t, ok := c.(interface{ MarshalJSON() ([]byte, error) }); ok {
					b, _ := t.MarshalJSON()
					joined += string(b)
				}
			}
			if !strings.Contains(joined, c.wantIn) {
				t.Errorf("error message %q does not contain %q", joined, c.wantIn)
			}
		})
	}
}

func TestSetupKernelDebugLablinkUninitialised(t *testing.T) {
	// Valid inputs but s.lablink == nil → should refuse with a friendly error.
	s := &srv{sess: &stubSession{}}
	res, _, err := s.setupKernelDebug(context.Background(), nil, setupKernelInput{
		Node: "n", Host: "127.0.0.1", Port: 50000, Key: "1.2.3.4", ConfirmReboot: true,
	})
	if err != nil {
		t.Fatalf("go error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError")
	}
}

func TestPullLatestMinidumpValidation(t *testing.T) {
	s := &srv{sess: &stubSession{}}
	cases := []struct {
		name string
		in   pullLatestDumpInput
		want string
	}{
		{"missing node", pullLatestDumpInput{}, "node is required"},
		{"bad source", pullLatestDumpInput{Node: "n", Source: "weird"}, "source must be"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, _, err := s.pullLatestMinidump(context.Background(), nil, c.in)
			if err != nil {
				t.Fatalf("go error: %v", err)
			}
			if res == nil || !res.IsError {
				t.Fatalf("expected IsError")
			}
		})
	}
}

func TestPullLatestMinidumpLablinkUninitialised(t *testing.T) {
	s := &srv{sess: &stubSession{}}
	res, _, err := s.pullLatestMinidump(context.Background(), nil, pullLatestDumpInput{Node: "n"})
	if err != nil {
		t.Fatalf("go error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError")
	}
}

func TestParseDumpListing(t *testing.T) {
	path, size, ts, err := parseDumpListing(`C:\Windows\Minidump\051324-9234-01.dmp|524288|2024-05-13T12:34:56.7890000Z`)
	if err != nil {
		t.Fatalf("parseDumpListing: %v", err)
	}
	if path != `C:\Windows\Minidump\051324-9234-01.dmp` {
		t.Errorf("path=%q", path)
	}
	if size != 524288 {
		t.Errorf("size=%d", size)
	}
	if ts != "2024-05-13T12:34:56.7890000Z" {
		t.Errorf("ts=%q", ts)
	}

	if _, _, _, err := parseDumpListing("nope"); err == nil {
		t.Error("expected error on missing pipes")
	}
	if _, _, _, err := parseDumpListing("|0|x"); err == nil {
		t.Error("expected error on empty path")
	}
	if _, _, _, err := parseDumpListing("p|notanumber|x"); err == nil {
		t.Error("expected error on bad size")
	}
}

func TestIsLocalHost(t *testing.T) {
	for _, h := range []string{"", "localhost", "LOCALHOST", "127.0.0.1", "127.10.20.30", "::1"} {
		if !isLocalHost(h) {
			t.Errorf("isLocalHost(%q) want true", h)
		}
	}
	for _, h := range []string{"8.8.8.8", "example.com", "not-an-ip"} {
		if isLocalHost(h) {
			t.Errorf("isLocalHost(%q) want false", h)
		}
	}
	// Best-effort: at least one local interface IP, when present, should
	// match. We pick the first non-loopback v4 address discovered.
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return
	}
	for _, a := range addrs {
		v, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if v.IP.To4() == nil || v.IP.IsLoopback() {
			continue
		}
		if !isLocalHost(v.IP.String()) {
			t.Errorf("isLocalHost(%s) want true for local interface", v.IP)
		}
		break
	}
}

func TestIsTransientReboot(t *testing.T) {
	for _, err := range []error{
		context.DeadlineExceeded,
		context.Canceled,
		errorString("EOF"),
		errorString("rpc error: code = Unavailable desc = ..."),
		errorString("read tcp: connection reset by peer"),
		errorString("transport is closing"),
	} {
		if !isTransientReboot(err) {
			t.Errorf("isTransientReboot(%v) want true", err)
		}
	}
	if isTransientReboot(nil) {
		t.Error("isTransientReboot(nil) want false")
	}
	if isTransientReboot(errorString("permission denied")) {
		t.Error("non-transient error misclassified")
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
