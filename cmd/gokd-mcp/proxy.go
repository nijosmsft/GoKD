//go:build remote

package main

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
	"time"

	llagent "github.com/nijosmsft/lablink/pkg/agentclient"
	llreg "github.com/nijosmsft/lablink/pkg/registry"
	llsec "github.com/nijosmsft/lablink/pkg/security"
	pb "github.com/nijosmsft/lablink/proto/agent"
)

const (
	remoteListenPort = 8765
	remoteInstallDir = `C:\gokd`
	remoteLogPath    = `C:\gokd\gokd-mcp.log`
)

// runRemoteProxy turns this process into a stdio proxy whose other end is a
// gokd-mcp.exe running on a remote lablink node. The flow:
//
//  1. Resolve node + TLS + token from lablink env vars.
//  2. Sync local gokd-mcp.exe + DLLs to C:\gokd\ on the node (skip if hashes match).
//  3. Kill any pre-existing gokd-mcp.exe on the node, then spawn ours with -listen.
//  4. Open a single Forward gRPC stream to 127.0.0.1:8765 on the node.
//  5. io.Copy stdin (from Copilot) ↔ stream.
func runRemoteProxy(cfg config, logWriter io.Writer) error {
	logf := func(format string, args ...any) {
		fmt.Fprintf(logWriter, "[gokd-mcp:remote] "+format+"\n", args...)
	}

	pool, node, err := dialLablinkNode(cfg.remote, logf)
	if err != nil {
		return err
	}
	defer pool.Close()

	client, err := pool.GetClient(node.Address, node.TLSServerName)
	if err != nil {
		return fmt.Errorf("get agent client for %s: %w", cfg.remote, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := deployToRemote(ctx, client, logf); err != nil {
		return fmt.Errorf("deploy to %s: %w", cfg.remote, err)
	}

	if err := startRemoteEngine(ctx, client, logf); err != nil {
		return fmt.Errorf("start remote gokd-mcp on %s: %w", cfg.remote, err)
	}

	logf("opening forward tunnel to 127.0.0.1:%d on %s", remoteListenPort, cfg.remote)
	stream, err := client.Forward(ctx)
	if err != nil {
		return fmt.Errorf("open Forward stream: %w", err)
	}
	if err := stream.Send(&pb.ForwardChunk{TargetAddr: fmt.Sprintf("127.0.0.1:%d", remoteListenPort)}); err != nil {
		return fmt.Errorf("send forward header: %w", err)
	}

	return shuttleStdio(stream, logf)
}

// dialLablinkNode loads the lablink registry + TLS config + token from env,
// resolves the named node, and returns a connected pool.
func dialLablinkNode(name string, logf func(string, ...any)) (*llagent.Pool, *llreg.Node, error) {
	configDir := llsec.FirstPresentEnv("LABLINK_HOME", "DEVICE_INTERACTION_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, fmt.Errorf("home dir: %w", err)
		}
		configDir = filepath.Join(home, ".lablink")
	}

	nodesFile := llsec.FirstPresentEnv("LABLINK_NODES", "DEVICE_INTERACTION_NODES")
	if nodesFile == "" {
		nodesFile = filepath.Join(configDir, "nodes.json")
	}
	logf("registry: %s", nodesFile)

	reg := llreg.Load(nodesFile)
	node, ok := reg.GetNode(name)
	if !ok {
		return nil, nil, fmt.Errorf("node %q not found in %s", name, nodesFile)
	}
	logf("node %s -> %s", name, node.Address)

	token, src, err := llsec.ResolveToken(
		"", "",
		[]string{"LABLINK_AGENT_TOKEN", "DEVICE_AGENT_TOKEN"},
		[]string{"LABLINK_AGENT_TOKEN_FILE"},
	)
	if err != nil {
		return nil, nil, fmt.Errorf("token: %w", err)
	}
	if token == "" {
		return nil, nil, errors.New("missing shared auth token; set LABLINK_AGENT_TOKEN or LABLINK_AGENT_TOKEN_FILE")
	}
	logf("token source: %s", src)

	allowInsecure, err := llsec.AllowInsecure(false)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, err
	}
	return llagent.NewPool(token, transport), node, nil
}

type deployFile struct {
	localPath  string
	remotePath string
}

// deployToRemote pushes gokd-mcp.exe plus the DbgEng bundle (DLLs sitting
// next to the exe) to C:\gokd\ on the node, skipping any file whose remote
// SHA256 already matches.
func deployToRemote(ctx context.Context, client pb.NodeAgentClient, logf func(string, ...any)) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve self: %w", err)
	}
	dir := filepath.Dir(exe)

	candidates := []string{
		"gokd-mcp.exe",
		"dbgeng.dll", "dbghelp.dll", "dbgcore.dll", "symsrv.dll", "symsrv.yes",
		"libstdc++-6.dll", "libgcc_s_seh-1.dll", "libwinpthread-1.dll",
	}
	var files []deployFile
	for _, name := range candidates {
		local := filepath.Join(dir, name)
		if _, err := os.Stat(local); err != nil {
			continue
		}
		files = append(files, deployFile{
			localPath:  local,
			remotePath: remoteInstallDir + `\` + name,
		})
	}

	if _, _, _, err := remoteRun(ctx, client,
		fmt.Sprintf(`New-Item -ItemType Directory -Path '%s' -Force | Out-Null`, remoteInstallDir),
		15); err != nil {
		return fmt.Errorf("mkdir %s: %w", remoteInstallDir, err)
	}

	for _, f := range files {
		localHash, err := sha256File(f.localPath)
		if err != nil {
			return fmt.Errorf("hash %s: %w", f.localPath, err)
		}
		remoteHash, _ := remoteHash(ctx, client, f.remotePath)
		if remoteHash == localHash {
			logf("skip %s (hash match)", filepath.Base(f.localPath))
			continue
		}
		logf("push %s -> %s", filepath.Base(f.localPath), f.remotePath)
		if err := pushFile(ctx, client, f.localPath, f.remotePath); err != nil {
			return fmt.Errorf("push %s: %w", filepath.Base(f.localPath), err)
		}
	}
	return nil
}

// startRemoteEngine kills any stale gokd-mcp.exe and starts a fresh one with
// -listen on the conventional port. Idempotent.
func startRemoteEngine(ctx context.Context, client pb.NodeAgentClient, logf func(string, ...any)) error {
	logf("killing any stale gokd-mcp.exe on node")
	_, _, _, _ = remoteRun(ctx, client,
		`Get-Process -Name gokd-mcp -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue`,
		10)

	cmd := fmt.Sprintf(`Start-Process -FilePath '%s\gokd-mcp.exe' -ArgumentList '-listen','127.0.0.1:%d','-log','%s' -WorkingDirectory '%s' -WindowStyle Hidden`,
		remoteInstallDir, remoteListenPort, remoteLogPath, remoteInstallDir)
	logf("starting remote gokd-mcp")
	if _, _, _, err := remoteRun(ctx, client, cmd, 15); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := probeForward(ctx, client); err == nil {
			logf("remote engine ready")
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return errors.New("remote engine did not come up within 15s")
}

func probeForward(ctx context.Context, client pb.NodeAgentClient) error {
	stream, err := client.Forward(ctx)
	if err != nil {
		return err
	}
	if err := stream.Send(&pb.ForwardChunk{TargetAddr: fmt.Sprintf("127.0.0.1:%d", remoteListenPort)}); err != nil {
		return err
	}
	if err := stream.Send(&pb.ForwardChunk{Close: true}); err != nil {
		return err
	}
	_ = stream.CloseSend()
	for {
		_, rerr := stream.Recv()
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return nil
			}
			return rerr
		}
	}
}

// shuttleStdio bridges the local stdin/stdout with the Forward stream. We
// run two goroutines: stdin->stream and stream->stdout. Either side closing
// terminates the proxy.
func shuttleStdio(stream pb.NodeAgent_ForwardClient, logf func(string, ...any)) error {
	var wg sync.WaitGroup
	wg.Add(2)
	errCh := make(chan error, 2)

	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, rerr := os.Stdin.Read(buf)
			if n > 0 {
				if serr := stream.Send(&pb.ForwardChunk{Data: append([]byte(nil), buf[:n]...)}); serr != nil {
					errCh <- fmt.Errorf("send: %w", serr)
					return
				}
			}
			if rerr != nil {
				if !errors.Is(rerr, io.EOF) {
					errCh <- fmt.Errorf("stdin read: %w", rerr)
				}
				_ = stream.Send(&pb.ForwardChunk{Close: true})
				_ = stream.CloseSend()
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		for {
			msg, rerr := stream.Recv()
			if rerr != nil {
				if !errors.Is(rerr, io.EOF) {
					errCh <- fmt.Errorf("recv: %w", rerr)
				}
				return
			}
			if len(msg.Data) > 0 {
				if _, werr := os.Stdout.Write(msg.Data); werr != nil {
					errCh <- fmt.Errorf("stdout write: %w", werr)
					return
				}
			}
			if msg.Close {
				return
			}
		}
	}()

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			logf("shuttle: %v", err)
			return err
		}
	}
	return nil
}

// --- agent helpers -----------------------------------------------------------

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

func remoteHash(ctx context.Context, client pb.NodeAgentClient, path string) (string, error) {
	cmd := fmt.Sprintf(`if (Test-Path '%s') { (Get-FileHash -Algorithm SHA256 '%s').Hash.ToLower() }`, path, path)
	out, code, _, err := remoteRun(ctx, client, cmd, 30)
	if err != nil || code != 0 {
		return "", fmt.Errorf("remote hash exit=%d: %w", code, err)
	}
	return strings.TrimSpace(out), nil
}

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
		// Send a frame if we have bytes OR we still need to mark the end.
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
