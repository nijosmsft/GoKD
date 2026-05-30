package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{"", slog.LevelInfo, false},
		{"info", slog.LevelInfo, false},
		{"INFO", slog.LevelInfo, false},
		{"  debug ", slog.LevelDebug, false},
		{"warn", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"err", slog.LevelError, false},
		{"bogus", slog.LevelInfo, true},
	}
	for _, c := range cases {
		got, err := parseLogLevel(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseLogLevel(%q) wantErr=%v got err=%v", c.in, c.wantErr, err)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("parseLogLevel(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

func TestBuildLoggerLevelFilter(t *testing.T) {
	var buf bytes.Buffer
	logger := buildLogger(&buf, slog.LevelWarn, false)
	logger.Debug("d-msg")
	logger.Info("i-msg")
	logger.Warn("w-msg")
	logger.Error("e-msg")
	out := buf.String()
	if strings.Contains(out, "d-msg") {
		t.Errorf("debug should be suppressed at LevelWarn: %q", out)
	}
	if strings.Contains(out, "i-msg") {
		t.Errorf("info should be suppressed at LevelWarn: %q", out)
	}
	if !strings.Contains(out, "w-msg") {
		t.Errorf("warn should be present at LevelWarn: %q", out)
	}
	if !strings.Contains(out, "e-msg") {
		t.Errorf("error should be present at LevelWarn: %q", out)
	}
}

func TestBuildLoggerJSON(t *testing.T) {
	var buf bytes.Buffer
	logger := buildLogger(&buf, slog.LevelInfo, true)
	logger.Info("hello", slog.String("k", "v"))
	line := strings.TrimRight(buf.String(), "\n")
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("expected JSON output, got %q: %v", line, err)
	}
	if m["msg"] != "hello" {
		t.Errorf("msg=%v want hello", m["msg"])
	}
	if m["k"] != "v" {
		t.Errorf("k=%v want v", m["k"])
	}
}

func TestEnvOrEnvBool(t *testing.T) {
	t.Setenv("GOKD_MCP_TEST_S", "set")
	if got := envOr("GOKD_MCP_TEST_S", "def"); got != "set" {
		t.Errorf("envOr set: got %q", got)
	}
	if got := envOr("GOKD_MCP_TEST_MISSING", "def"); got != "def" {
		t.Errorf("envOr missing: got %q", got)
	}
	t.Setenv("GOKD_MCP_TEST_B", "true")
	if !envBool("GOKD_MCP_TEST_B", false) {
		t.Errorf("envBool true")
	}
	t.Setenv("GOKD_MCP_TEST_B", "off")
	if envBool("GOKD_MCP_TEST_B", true) {
		t.Errorf("envBool off")
	}
	t.Setenv("GOKD_MCP_TEST_B", "garbage")
	if !envBool("GOKD_MCP_TEST_B", true) {
		t.Errorf("envBool garbage falls through to default")
	}
}
