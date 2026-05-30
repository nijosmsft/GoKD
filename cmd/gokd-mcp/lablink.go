package main

// Shared lablink plumbing used by both the -remote stdio proxy and the
// composite MCP tools (setup_kernel_debug, pull_latest_minidump). This
// file owns the lazy *agentclient.Pool plus all helpers that drive the
// agent gRPC interface (remoteRun, pushFile, pullFile, remoteHash).
//
// proxy.go keeps just the -remote-specific glue: deployToRemote,
// startRemoteEngine, the Forward shuttle. Composite tools call
// lablinkClient.get() to obtain a pool, then resolveNode + GetClient for
// the per-call agent client.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	llagent "github.com/nijosmsft/lablink/pkg/agentclient"
	llreg "github.com/nijosmsft/lablink/pkg/registry"
	llsec "github.com/nijosmsft/lablink/pkg/security"
	pb "github.com/nijosmsft/lablink/proto/agent"
)

// lablinkClient owns the process-wide lazy *agentclient.Pool plus the
// loaded *registry.Registry. Both the -remote proxy and the composite
// MCP tools share one instance via srv.lablink.
//
// The pool caches gRPC ClientConns per (address|serverName) inside
// agentclient internals, so reusing one pool across the lifetime of the
// gokd-mcp process avoids redialing on every tool call.
type lablinkClient struct {
	once      sync.Once
	pool      *llagent.Pool
	registry  *llreg.Registry
	nodesFile string
	initErr   error
	logf      func(string, ...any)
}

// newLablinkClient builds an empty client whose first get() call
// performs registry + token + transport resolution. logf is invoked for
// human-readable progress; pass a no-op when none is needed.
func newLablinkClient(logf func(string, ...any)) *lablinkClient {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &lablinkClient{logf: logf}
}

// get returns the lazily-initialised pool + registry. After the first
// call, errors are cached and replayed on subsequent calls so we do not
// re-read env vars per tool invocation.
func (l *lablinkClient) get() (*llagent.Pool, *llreg.Registry, error) {
	l.once.Do(func() {
		l.pool, l.registry, l.nodesFile, l.initErr = dialLablink(l.logf)
	})
	return l.pool, l.registry, l.initErr
}

// close shuts down all cached gRPC connections. Safe to call when get()
// was never invoked.
func (l *lablinkClient) close() {
	if l.pool != nil {
		l.pool.Close()
	}
}

// resolveNode looks up name in the registry, returning a clear error
// (mentioning the nodes file path) when it is missing.
func (l *lablinkClient) resolveNode(name string) (*llreg.Node, error) {
	if _, _, err := l.get(); err != nil {
		return nil, err
	}
	node, ok := l.registry.GetNode(name)
	if !ok {
		return nil, fmt.Errorf("node %q not found in %s", name, l.nodesFile)
	}
	return node, nil
}

// clientFor resolves a node and returns its NodeAgent gRPC client.
func (l *lablinkClient) clientFor(name string) (pb.NodeAgentClient, *llreg.Node, error) {
	node, err := l.resolveNode(name)
	if err != nil {
		return nil, nil, err
	}
	client, err := l.pool.GetClient(node.Address, node.TLSServerName)
	if err != nil {
		return nil, nil, fmt.Errorf("get agent client for %s: %w", name, err)
	}
	return client, node, nil
}

// dialLablink loads the lablink registry + TLS config + token from env
// and returns a connected pool. Pure factory — no per-node logic. The
// -remote path and the composite tools both call this through
// lablinkClient.get().
func dialLablink(logf func(string, ...any)) (*llagent.Pool, *llreg.Registry, string, error) {
	configDir := llsec.FirstPresentEnv("LABLINK_HOME", "DEVICE_INTERACTION_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, "", fmt.Errorf("home dir: %w", err)
		}
		configDir = filepath.Join(home, ".lablink")
	}

	nodesFile := llsec.FirstPresentEnv("LABLINK_NODES", "DEVICE_INTERACTION_NODES")
	if nodesFile == "" {
		nodesFile = filepath.Join(configDir, "nodes.json")
	}
	logf("registry: %s", nodesFile)

	reg := llreg.Load(nodesFile)

	token, src, err := llsec.ResolveToken(
		"", "",
		[]string{"LABLINK_AGENT_TOKEN", "DEVICE_AGENT_TOKEN"},
		[]string{"LABLINK_AGENT_TOKEN_FILE"},
	)
	if err != nil {
		return nil, nil, nodesFile, fmt.Errorf("token: %w", err)
	}
	if token == "" {
		return nil, nil, nodesFile, errors.New("missing shared auth token; set LABLINK_AGENT_TOKEN or LABLINK_AGENT_TOKEN_FILE")
	}
	logf("token source: %s", src)

	allowInsecure, err := llsec.AllowInsecure(false)
	if err != nil {
		return nil, nil, nodesFile, err
	}
	transport, err := llsec.ResolveClientTransport(
		llsec.FirstPresentEnv("LABLINK_TRANSPORT"),
		allowInsecure,
		llsec.FirstPresentEnv("LABLINK_TLS_CA", "LABLINK_TLS_CA_CERT"),
		llsec.FirstPresentEnv("LABLINK_TLS_CERT", "LABLINK_TLS_CLIENT_CERT"),
		llsec.FirstPresentEnv("LABLINK_TLS_KEY", "LABLINK_TLS_CLIENT_KEY"),
		llsec.FirstPresentEnv("LABLINK_TLS_SERVER_NAME"),
	)
	if err != nil {
		return nil, nil, nodesFile, err
	}
	return llagent.NewPool(token, transport), reg, nodesFile, nil
}

// --- agent helpers shared by proxy.go and composite tools -------------------

// remoteRun executes a single shell command on the node and returns its
// captured stdout, exit code, agent-reported PID, and any transport error.
// Returns stdout even on stream error so callers can include partial
// output in diagnostic messages.
func remoteRun(ctx context.Context, client pb.NodeAgentClient, command string, timeoutSec int) (string, int, int, error) {
	stream, err := client.Execute(ctx, &pb.ExecuteRequest{
		Command:        command,
		Shell:          "powershell",
		TimeoutSeconds: int32(timeoutSec),
	})
	if err != nil {
		return "", -1, 0, err
	}
	var stdout strings.Builder
	var exitCode int
	var pid int
	for {
		msg, rerr := stream.Recv()
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return stdout.String(), exitCode, pid, rerr
		}
		if len(msg.Data) > 0 {
			stdout.Write(msg.Data)
		}
		if msg.Pid != 0 {
			pid = int(msg.Pid)
		}
		if msg.Done {
			exitCode = int(msg.ExitCode)
		}
	}
	return stdout.String(), exitCode, pid, nil
}

// remoteHash returns the lowercase SHA256 of a remote file, or "" if the
// file does not exist. Returns an error only for transport / exit-code
// failures, never for "missing".
func remoteHash(ctx context.Context, client pb.NodeAgentClient, path string) (string, error) {
	cmd := fmt.Sprintf(`if (Test-Path '%s') { (Get-FileHash -Algorithm SHA256 '%s').Hash.ToLower() }`, path, path)
	out, code, _, err := remoteRun(ctx, client, cmd, 30)
	if err != nil || code != 0 {
		return "", fmt.Errorf("remote hash exit=%d: %w", code, err)
	}
	return strings.TrimSpace(out), nil
}

// sha256File hashes a local file. Used by deployToRemote for skip-if-equal.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// pushFile streams a local file to a remote path via the agent's PushFile
// RPC. Marks the final frame with IsLast=true; the first frame carries
// FileSize so the agent can preallocate.
func pushFile(ctx context.Context, client pb.NodeAgentClient, local, remote string) error {
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}

	stream, err := client.PushFile(ctx)
	if err != nil {
		return err
	}

	const chunk = 64 * 1024
	buf := make([]byte, chunk)
	first := true
	sentLast := false
	for !sentLast {
		n, rerr := f.Read(buf)
		if n > 0 || rerr != nil {
			req := &pb.PushFileRequest{}
			if n > 0 {
				req.Chunk = append([]byte(nil), buf[:n]...)
			}
			if first {
				req.RemotePath = remote
				req.FileSize = info.Size()
				first = false
			}
			if rerr != nil {
				if !errors.Is(rerr, io.EOF) {
					return rerr
				}
				req.IsLast = true
				sentLast = true
			}
			if serr := stream.Send(req); serr != nil {
				return serr
			}
		}
	}
	if _, err := stream.CloseAndRecv(); err != nil {
		return err
	}
	return nil
}

// pullFile streams a remote file to a local path via the agent's PullFile
// RPC. The first frame carries TotalSize; we return the number of bytes
// actually written so callers can sanity-check against it.
func pullFile(ctx context.Context, client pb.NodeAgentClient, remotePath, localPath string) (int64, error) {
	stream, err := client.PullFile(ctx, &pb.PullFileRequest{RemotePath: remotePath})
	if err != nil {
		return 0, err
	}
	f, err := os.Create(localPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var written int64
	for {
		msg, rerr := stream.Recv()
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return written, nil
			}
			return written, rerr
		}
		if len(msg.Chunk) > 0 {
			n, werr := f.Write(msg.Chunk)
			if werr != nil {
				return written, werr
			}
			written += int64(n)
		}
	}
}

// lablinkErr is the canonical prefix wrapper for errors that surface out
// of the lablink agent transport. Composite tools use this so the LLM can
// distinguish "agent / network problem" from "DbgEng / debuggee problem".
//
//	out, code, _, err := remoteRun(ctx, client, "bcdedit /debug on", 15)
//	if err != nil {
//	    return toolErr[...](lablinkErr("execute bcdedit /debug on", err))
//	}
func lablinkErr(stage string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("lablink %s: %w", stage, err)
}
