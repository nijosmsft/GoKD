package main

// composite_tools.go owns the lablink-backed composite MCP tools that
// combine a lablink agent operation with a local DbgEng call:
//
//   * setup_kernel_debug  (t4-1) - bcdedit on the node + reboot wait,
//                                  then optional local AttachKernel.
//   * pull_latest_minidump (t4-2) - find newest .dmp on the node, pull
//                                   via PullFile, optionally OpenDump
//                                   in the local engine and surface
//                                   bug_check + last_exception.
//
// Both tools are gated by srv.lablinkEnabled (CC-2). Registration lives
// in tools.go::registerTools next to the rest of the surface so the
// schema test enumerates everything from a single entry point.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nijosmsft/gokd"
	pb "github.com/nijosmsft/lablink/proto/agent"
)

// --- t4-1 setup_kernel_debug ------------------------------------------------

type setupKernelInput struct {
	Node           string `json:"node"           jsonschema:"lablink node name from nodes.json"`
	Host           string `json:"host"           jsonschema:"host IP the kernel debug transport binds to (operator side)"`
	Port           uint16 `json:"port"           jsonschema:"UDP port (1024-65535)"`
	Key            string `json:"key"            jsonschema:"KDNET key in 'A.B.C.D' form (four dotted groups, each 1-3 digits)"`
	ConfirmReboot  bool   `json:"confirm_reboot" jsonschema:"must be true; refuses otherwise"`
	AttachAfter    bool   `json:"attach_after,omitempty"  jsonschema:"if true and host is local to this gokd-mcp instance, attach the local engine to the kernel once the node returns"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"reboot-wait timeout in seconds (default 300)"`
}

type setupKernelOutput struct {
	Node                  string `json:"node"`
	BcdeditDbgsettings    string `json:"bcdedit_dbgsettings"`
	BcdeditDebugOn        bool   `json:"bcdedit_debug_on"`
	Rebooted              bool   `json:"rebooted"`
	NodeReadyAfterSeconds int    `json:"node_ready_after_seconds"`
	ConnectionString      string `json:"connection_string"`
	Attached              bool   `json:"attached,omitempty"`
	NextAction            string `json:"next_action"`
}

var kdnetKeyRe = regexp.MustCompile(`^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$`)

// setupKernelDebug rewires a remote node's boot configuration for KDNET
// and reboots it. See design § 4.3 for the full algorithm.
func (s *srv) setupKernelDebug(ctx context.Context, _ *mcp.CallToolRequest, in setupKernelInput) (*mcp.CallToolResult, setupKernelOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[setupKernelOutput]("setup_kernel_debug", err)
	}
	if !in.ConfirmReboot {
		return toolErr[setupKernelOutput]("setup_kernel_debug",
			fmt.Errorf("set confirm_reboot=true to acknowledge this tool reboots the node"))
	}
	if strings.TrimSpace(in.Node) == "" {
		return toolErr[setupKernelOutput]("setup_kernel_debug", fmt.Errorf("node is required"))
	}
	if strings.TrimSpace(in.Host) == "" {
		return toolErr[setupKernelOutput]("setup_kernel_debug", fmt.Errorf("host is required"))
	}
	if strings.TrimSpace(in.Key) == "" {
		return toolErr[setupKernelOutput]("setup_kernel_debug", fmt.Errorf("key is required"))
	}
	if in.Port < 1024 {
		return toolErr[setupKernelOutput]("setup_kernel_debug",
			fmt.Errorf("port must be in 1024..65535 (got %d)", in.Port))
	}
	if !kdnetKeyRe.MatchString(in.Key) {
		return toolErr[setupKernelOutput]("setup_kernel_debug",
			fmt.Errorf("key %q must be four dotted decimal groups (e.g. '1.2.3.4')", in.Key))
	}
	if s.lablink == nil {
		return toolErr[setupKernelOutput]("setup_kernel_debug",
			fmt.Errorf("lablink integration not initialised; pass -lablink-enabled"))
	}

	timeout := in.TimeoutSeconds
	if timeout <= 0 {
		timeout = 300
	}

	out := setupKernelOutput{Node: in.Node}

	client, node, err := s.lablink.clientFor(in.Node)
	if err != nil {
		return toolErr[setupKernelOutput]("setup_kernel_debug", lablinkErr("resolve node", err))
	}

	// Step 3: bcdedit /dbgsettings ...
	cmd := fmt.Sprintf(`bcdedit /dbgsettings net hostip:%s port:%d key:%s`, in.Host, in.Port, in.Key)
	stdout, code, _, err := remoteRunWithTimeout(ctx, client, cmd, 15)
	if err != nil {
		return toolErr[setupKernelOutput]("setup_kernel_debug",
			lablinkErr("execute bcdedit /dbgsettings", err))
	}
	if code != 0 {
		return toolErr[setupKernelOutput]("setup_kernel_debug",
			fmt.Errorf("bcdedit /dbgsettings exit=%d: %s", code, strings.TrimSpace(stdout)))
	}
	out.BcdeditDbgsettings = strings.TrimSpace(stdout)

	// Step 4: bcdedit /debug on
	stdout, code, _, err = remoteRunWithTimeout(ctx, client, `bcdedit /debug on`, 15)
	if err != nil {
		return toolErr[setupKernelOutput]("setup_kernel_debug",
			lablinkErr("execute bcdedit /debug on", err))
	}
	if code != 0 {
		return toolErr[setupKernelOutput]("setup_kernel_debug",
			fmt.Errorf("bcdedit /debug on exit=%d: %s", code, strings.TrimSpace(stdout)))
	}
	out.BcdeditDebugOn = true

	// Step 5: fire-and-forget reboot. The agent itself may go away
	// mid-stream when shutdown closes services, so we tolerate io.EOF /
	// transport errors here.
	_, _, _, rerr := remoteRunWithTimeout(ctx, client, `shutdown /r /t 5`, 5)
	if rerr != nil && !isTransientReboot(rerr) {
		return toolErr[setupKernelOutput]("setup_kernel_debug",
			lablinkErr("execute shutdown", rerr))
	}
	out.Rebooted = true
	rebootStart := time.Now()

	// Step 6: wait for the node to come back.
	if err := waitForReboot(ctx, s.lablink, node.Address, node.TLSServerName, timeout); err != nil {
		return toolErr[setupKernelOutput]("setup_kernel_debug",
			lablinkErr("wait for reboot", err))
	}
	out.NodeReadyAfterSeconds = int(time.Since(rebootStart).Round(time.Second).Seconds())

	// Step 7: connection string, NO target= segment (CLAUDE.md).
	out.ConnectionString = fmt.Sprintf("net:port=%d,key=%s", in.Port, in.Key)

	// Step 8: optionally attach the local engine.
	if in.AttachAfter {
		switch {
		case s.sess == nil:
			out.NextAction = "attach_after ignored: -remote mode has no local engine; call attach_kernel with connection_string=" + out.ConnectionString
		case !isLocalHost(in.Host):
			out.NextAction = "attach_after ignored: host " + in.Host + " is not local to this gokd-mcp; call attach_kernel from a host that owns that IP, with connection_string=" + out.ConnectionString
		default:
			attachCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
			err := s.sess.AttachKernel(attachCtx, out.ConnectionString, gokd.KernelDefault)
			cancel()
			if err != nil {
				return toolErr[setupKernelOutput]("setup_kernel_debug", err)
			}
			out.Attached = true
		}
	}
	if out.NextAction == "" {
		if out.Attached {
			out.NextAction = "kernel attached; call get_threads or get_stack"
		} else {
			out.NextAction = "call attach_kernel with connection_string=" + out.ConnectionString
		}
	}
	return nil, out, nil
}

// remoteRunWithTimeout wraps remoteRun with a derived context whose
// deadline matches the per-call gRPC timeout. We need this so the agent
// side does not outlive the operator's overall ctx cancellation.
func remoteRunWithTimeout(parent context.Context, client pb.NodeAgentClient, command string, timeoutSec int) (string, int, int, error) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	return remoteRun(ctx, client, command, timeoutSec)
}

// isTransientReboot returns true when err looks like the kind of failure
// expected during a reboot: io.EOF, gRPC Unavailable, deadline exceeded,
// or a context error. Used to ignore the reboot Execute's error so we
// can move on to polling.
func isTransientReboot(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	msg := err.Error()
	for _, s := range []string{"EOF", "Unavailable", "connection reset", "transport is closing"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// waitForReboot implements design § 4.3 step 6: poll GetInfo through two
// phases (going down, then coming back up). pool.ResetConnection runs once
// at the transition so the cached gRPC connection redials cleanly.
func waitForReboot(ctx context.Context, ll *lablinkClient, address, serverName string, totalTimeoutSec int) error {
	pool, _, err := ll.get()
	if err != nil {
		return err
	}
	overall, cancel := context.WithTimeout(ctx, time.Duration(totalTimeoutSec)*time.Second)
	defer cancel()

	// Phase A: wait for GetInfo to start failing. Up to 120s.
	wentDown := false
	downDeadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(downDeadline) {
		if err := overall.Err(); err != nil {
			return err
		}
		cli, gerr := pool.GetClient(address, serverName)
		if gerr != nil {
			wentDown = true
			break
		}
		probeCtx, probeCancel := context.WithTimeout(overall, 2*time.Second)
		_, infoErr := cli.GetInfo(probeCtx, &pb.GetInfoRequest{})
		probeCancel()
		if infoErr != nil {
			wentDown = true
			break
		}
		select {
		case <-overall.Done():
			return overall.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if !wentDown {
		return fmt.Errorf("node did not appear to reboot within 120s")
	}

	// Drop the cached conn at the transition.
	pool.ResetConnection(address)

	// Phase B: wait for GetInfo to succeed again.
	for {
		if err := overall.Err(); err != nil {
			return err
		}
		cli, gerr := pool.GetClient(address, serverName)
		if gerr == nil {
			probeCtx, probeCancel := context.WithTimeout(overall, 5*time.Second)
			_, infoErr := cli.GetInfo(probeCtx, &pb.GetInfoRequest{})
			probeCancel()
			if infoErr == nil {
				return nil
			}
		}
		select {
		case <-overall.Done():
			return fmt.Errorf("node did not come back within %ds", totalTimeoutSec)
		case <-time.After(5 * time.Second):
		}
	}
}

// isLocalHost returns true if host refers to this machine: 127.0.0.0/8,
// ::1, "localhost", "" or an IP bound to a local interface. Used by
// setup_kernel_debug to decide whether AttachAfter can run safely.
func isLocalHost(host string) bool {
	h := strings.TrimSpace(strings.ToLower(host))
	switch h {
	case "", "localhost":
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		var local net.IP
		switch v := a.(type) {
		case *net.IPNet:
			local = v.IP
		case *net.IPAddr:
			local = v.IP
		}
		if local != nil && local.Equal(ip) {
			return true
		}
	}
	return false
}

// --- t4-2 pull_latest_minidump ----------------------------------------------

type pullLatestDumpInput struct {
	Node          string `json:"node"          jsonschema:"lablink node name from nodes.json"`
	Source        string `json:"source,omitempty" jsonschema:"'minidump' (default, C:\\Windows\\Minidump\\*.dmp) or 'crashdumps' (C:\\Windows\\LiveKernelReports\\*.dmp)"`
	OpenLocally   bool   `json:"open_locally,omitempty" jsonschema:"if true, after pulling, call OpenDump on the local engine and return a summary"`
	LocalPathHint string `json:"local_path_hint,omitempty" jsonschema:"local directory to save the file (default %TEMP%/gokd-dumps/<node>/)"`
	MaxBytes      int64  `json:"max_bytes,omitempty" jsonschema:"refuse to pull if remote file exceeds this size (default 1<<30 = 1 GiB)"`
}

type pullLatestDumpOutput struct {
	Found          bool                   `json:"found"`
	RemotePath     string                 `json:"remote_path,omitempty"`
	RemoteModified string                 `json:"remote_modified,omitempty"`
	LocalPath      string                 `json:"local_path,omitempty"`
	SizeBytes      int64                  `json:"size_bytes,omitempty"`
	Opened         bool                   `json:"opened,omitempty"`
	Summary        pullLatestDumpSummary  `json:"summary,omitempty"`
	Note           string                 `json:"note,omitempty"`
}

type pullLatestDumpSummary struct {
	BugCheck      *bugCheckOutput      `json:"bug_check,omitempty"`
	LastException *lastExceptionOutput `json:"last_exception,omitempty"`
}

// pullLatestMinidump enumerates the newest .dmp on a remote node, pulls
// it over PullFile, and (when requested + a local engine is present)
// opens it and surfaces bug-check + last-exception.
func (s *srv) pullLatestMinidump(ctx context.Context, _ *mcp.CallToolRequest, in pullLatestDumpInput) (*mcp.CallToolResult, pullLatestDumpOutput, error) {
	if err := checkContext(ctx); err != nil {
		return toolErr[pullLatestDumpOutput]("pull_latest_minidump", err)
	}
	if strings.TrimSpace(in.Node) == "" {
		return toolErr[pullLatestDumpOutput]("pull_latest_minidump", fmt.Errorf("node is required"))
	}
	source := strings.ToLower(strings.TrimSpace(in.Source))
	switch source {
	case "", "minidump":
		source = "minidump"
	case "crashdumps":
	default:
		return toolErr[pullLatestDumpOutput]("pull_latest_minidump",
			fmt.Errorf("source must be 'minidump' or 'crashdumps' (got %q)", in.Source))
	}
	maxBytes := in.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 1 << 30
	}
	if s.lablink == nil {
		return toolErr[pullLatestDumpOutput]("pull_latest_minidump",
			fmt.Errorf("lablink integration not initialised; pass -lablink-enabled"))
	}

	client, _, err := s.lablink.clientFor(in.Node)
	if err != nil {
		return toolErr[pullLatestDumpOutput]("pull_latest_minidump", lablinkErr("resolve node", err))
	}

	// Step 3: find latest dump.
	var psCmd string
	switch source {
	case "minidump":
		psCmd = `$f = Get-ChildItem 'C:\Windows\Minidump' -Filter '*.dmp' -File -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending | Select-Object -First 1; if ($f) { '{0}|{1}|{2}' -f $f.FullName, $f.Length, $f.LastWriteTimeUtc.ToString('o') }`
	case "crashdumps":
		psCmd = `$f = Get-ChildItem 'C:\Windows\LiveKernelReports' -Recurse -Filter '*.dmp' -File -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending | Select-Object -First 1; if ($f) { '{0}|{1}|{2}' -f $f.FullName, $f.Length, $f.LastWriteTimeUtc.ToString('o') }`
	}
	stdout, code, _, err := remoteRunWithTimeout(ctx, client, psCmd, 30)
	if err != nil {
		return toolErr[pullLatestDumpOutput]("pull_latest_minidump",
			lablinkErr("enumerate dumps", err))
	}
	if code != 0 {
		return toolErr[pullLatestDumpOutput]("pull_latest_minidump",
			fmt.Errorf("enumerate dumps exit=%d: %s", code, strings.TrimSpace(stdout)))
	}
	line := strings.TrimSpace(stdout)
	if line == "" {
		return nil, pullLatestDumpOutput{Found: false}, nil
	}
	remotePath, sizeBytes, remoteMod, err := parseDumpListing(line)
	if err != nil {
		return toolErr[pullLatestDumpOutput]("pull_latest_minidump",
			fmt.Errorf("parse listing %q: %w", line, err))
	}

	// Step 4: size guard.
	if sizeBytes > maxBytes {
		return toolErr[pullLatestDumpOutput]("pull_latest_minidump",
			fmt.Errorf("remote dump too large (%d bytes); raise max_bytes to fetch", sizeBytes))
	}

	// Step 5: compute local path.
	var dir string
	if h := strings.TrimSpace(in.LocalPathHint); h != "" {
		dir = h
	} else {
		dir = filepath.Join(os.TempDir(), "gokd-dumps", in.Node)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return toolErr[pullLatestDumpOutput]("pull_latest_minidump",
			fmt.Errorf("create %s: %w", dir, err))
	}
	local := filepath.Join(dir, filepath.Base(remotePath))

	// Step 6: pull.
	written, perr := pullFile(ctx, client, remotePath, local)
	if perr != nil {
		return toolErr[pullLatestDumpOutput]("pull_latest_minidump",
			lablinkErr("pull file", perr))
	}

	// Step 7: populate output.
	out := pullLatestDumpOutput{
		Found:          true,
		RemotePath:     remotePath,
		RemoteModified: remoteMod,
		LocalPath:      local,
		SizeBytes:      written,
	}

	// Step 8: optional local OpenDump + summary.
	if in.OpenLocally {
		if s.sess == nil {
			out.Note = "pulled file; -remote mode has no local engine, so open_locally was ignored"
			return nil, out, nil
		}
		if err := s.sess.OpenDump(local); err != nil {
			return toolErr[pullLatestDumpOutput]("pull_latest_minidump", err)
		}
		out.Opened = true
		// Populate the summary best-effort: an error from BugCheck or
		// LastException must not blow up the whole tool, since the dump
		// has already been pulled and opened successfully.
		if bc, bcErr := s.sess.BugCheck(); bcErr == nil && bc != nil {
			args := make([]string, 0, len(bc.Args))
			for _, a := range bc.Args {
				args = append(args, hex64(a))
			}
			out.Summary.BugCheck = &bugCheckOutput{
				Found:       true,
				Code:        bc.Code,
				CodeHex:     fmt.Sprintf("0x%08x", bc.Code),
				Args:        args,
				Name:        bc.Name,
				Description: bc.Description,
			}
		}
		if ex, exErr := s.sess.LastException(); exErr == nil && ex != nil {
			params := make([]string, 0, ex.ParameterCount)
			for i := uint32(0); i < ex.ParameterCount; i++ {
				params = append(params, hex64(ex.Parameters[i]))
			}
			out.Summary.LastException = &lastExceptionOutput{
				Found:           true,
				Code:            ex.Code,
				CodeHex:         fmt.Sprintf("0x%08x", ex.Code),
				Flags:           ex.Flags,
				AddressHex:      hex64(ex.Address),
				NestedRecordHex: hex64(ex.NestedRecord),
				ParameterCount:  ex.ParameterCount,
				Parameters:      params,
				FirstChance:     ex.FirstChance,
				ProcessID:       ex.ProcessID,
				ThreadID:        ex.ThreadID,
				Description:     ex.Description,
			}
		}
	}
	return nil, out, nil
}

// parseDumpListing parses a "path|size|RFC3339" line produced by the
// PowerShell one-liner in pullLatestMinidump.
func parseDumpListing(line string) (string, int64, string, error) {
	parts := strings.SplitN(line, "|", 3)
	if len(parts) != 3 {
		return "", 0, "", fmt.Errorf("expected 3 pipe-delimited fields, got %d", len(parts))
	}
	path := strings.TrimSpace(parts[0])
	if path == "" {
		return "", 0, "", fmt.Errorf("empty path")
	}
	size, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil {
		return "", 0, "", fmt.Errorf("size: %w", err)
	}
	mod := strings.TrimSpace(parts[2])
	return path, size, mod, nil
}
