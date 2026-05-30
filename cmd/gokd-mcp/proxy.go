package main

// proxy.go owns -remote stdio<->Forward proxying.
//
// Listener-side lifecycle (t4-3) — instead of Start-Process + zombie
// hunting, we ask the agent to launch gokd-mcp.exe as a tracked detached
// Execute. The agent returns a job_id; we keep it on remoteProxy and:
//
//   * cancel it on teardown (defer),
//   * watch it for early termination via WatchJobs and bail out fast,
//   * cancel any sibling jobs left over by previous proxy sessions.
//
// Auth-token (t3-6 client side) — the listener is spawned with
// -auth-token <random hex>. The proxy sends "AUTH <token>\n" as the
// first data frame after the Forward TargetAddr header, and reads
// "OK\n" before shuttling.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	llreg "github.com/nijosmsft/lablink/pkg/registry"
	pb "github.com/nijosmsft/lablink/proto/agent"
)

const (
	remoteListenPort = 8765
	remoteInstallDir = `C:\gokd`
	remoteLogPath    = `C:\gokd\gokd-mcp.log`

	// listenerJobMarker is a substring of the Job.Command field for any
	// listener spawned by t4-3. ListJobs/CancelStaleListeners scan for it.
	listenerJobMarker = `gokd-mcp.exe -listen 127.0.0.1:8765`
)

// remoteProxy carries the resources spanning one -remote session: the
// lablink client, the resolved node, the auth token used to spawn the
// listener, and the job_id returned by the agent. teardown() is wired
// via defer in runRemoteProxy so a panic, ctx cancel, or stream error
// still cancels the remote job.
type remoteProxy struct {
	ll        *lablinkClient
	client    pb.NodeAgentClient
	node      *llreg.Node
	authToken string
	jobID     string
	logf      func(string, ...any)
}

// runRemoteProxy turns this process into a stdio proxy whose other end
// is a gokd-mcp.exe running on a remote lablink node. The flow is:
//
//  1. Resolve node + TLS + token from lablink env vars (lablinkClient).
//  2. Generate a per-session auth token for the remote listener.
//  3. Sync local gokd-mcp.exe + DLLs to C:\gokd\ on the node (skip if hashed equal).
//  4. Cancel any stale listener jobs from previous proxy sessions.
//  5. Detached Execute "gokd-mcp.exe -listen ... -auth-token ..." -> tracked job_id.
//  6. Probe the listener (with AUTH) until it accepts, or fail fast on early job exit.
//  7. Background goroutine watches WatchJobs; if the listener job terminates,
//     cancel the proxy ctx so the Forward stream tears down.
//  8. Open one Forward gRPC stream, do AUTH handshake inline, byte-shuttle stdio.
func runRemoteProxy(cfg config, logWriter io.Writer) error {
	logf := func(format string, args ...any) {
		fmt.Fprintf(logWriter, "[gokd-mcp:remote] "+format+"\n", args...)
	}

	ll := newLablinkClient(logf)
	defer ll.close()

	if _, _, err := ll.get(); err != nil {
		return err
	}
	client, node, err := ll.clientFor(cfg.remote)
	if err != nil {
		return err
	}
	logf("node %s -> %s", cfg.remote, node.Address)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	authToken, err := newAuthToken()
	if err != nil {
		return fmt.Errorf("generate auth token: %w", err)
	}

	p := &remoteProxy{
		ll: ll, client: client, node: node,
		authToken: authToken, logf: logf,
	}

	if err := deployToRemote(ctx, client, logf); err != nil {
		return fmt.Errorf("deploy to %s: %w", cfg.remote, err)
	}

	jobID, err := startRemoteListener(ctx, client, authToken, logf)
	p.jobID = jobID
	defer p.teardown(context.Background())
	if err != nil {
		return fmt.Errorf("start remote gokd-mcp on %s: %w", cfg.remote, err)
	}

	// Watch for the listener job dying so we don't sit forever on a dead
	// Forward stream. The watcher cancels the proxy ctx on terminal status.
	go p.watchListener(ctx, cancel)

	logf("opening forward tunnel to 127.0.0.1:%d on %s", remoteListenPort, cfg.remote)
	stream, err := client.Forward(ctx)
	if err != nil {
		return fmt.Errorf("open Forward stream: %w", err)
	}
	if err := stream.Send(&pb.ForwardChunk{TargetAddr: fmt.Sprintf("127.0.0.1:%d", remoteListenPort)}); err != nil {
		return fmt.Errorf("send forward header: %w", err)
	}
	if err := authenticateRemoteStream(stream, authToken); err != nil {
		return fmt.Errorf("auth remote listener: %w", err)
	}

	return shuttleStdio(stream, logf)
}

// newAuthToken returns 32 random bytes hex-encoded. Result is 64 chars,
// shell-safe, contains no whitespace, and satisfies the -auth-token
// minimum-16-char validator in parseFlags.
func newAuthToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// teardown cancels the listener job. Called via defer so it runs on any
// exit path: clean shutdown, error from startup, or panic. Uses a fresh
// 10s context so teardown survives the caller's cancelled ctx.
func (p *remoteProxy) teardown(parent context.Context) {
	if p.jobID == "" {
		return
	}
	tctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	p.logf("cancelling listener job %s", p.jobID)
	if _, err := p.client.CancelJob(tctx, &pb.CancelJobRequest{JobId: p.jobID, Force: true}); err != nil {
		p.logf("cancel job failed: %v", err)
	}
}

// watchListener subscribes to the agent's WatchJobs stream and watches
// for our listener job entering a terminal status (EXITED / CANCELED /
// ORPHANED). When it does, we log the tail of its captured output and
// cancel the proxy ctx so the Forward stream errors out cleanly.
//
// The goroutine returns silently when the parent ctx is cancelled.
func (p *remoteProxy) watchListener(ctx context.Context, cancelProxy context.CancelFunc) {
	stream, err := p.client.WatchJobs(ctx, &pb.WatchJobsRequest{})
	if err != nil {
		p.logf("watch jobs: %v", err)
		return
	}
	for {
		ev, rerr := stream.Recv()
		if rerr != nil {
			return
		}
		if ev.Job == nil || ev.Job.JobId != p.jobID {
			continue
		}
		if isTerminalStatus(ev.Job.Status) {
			tail := fetchJobTail(ctx, p.client, p.jobID, 100)
			p.logf("listener job %s terminated: status=%v exit=%d output=%s",
				p.jobID, ev.Job.Status, ev.Job.ExitCode, tail)
			cancelProxy()
			return
		}
	}
}

// deployFile is one (local, remote) pair fed to deployToRemote.
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
		remoteHashVal, _ := remoteHash(ctx, client, f.remotePath)
		if remoteHashVal == localHash {
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

// startRemoteListener spawns gokd-mcp -listen on the node as a tracked
// detached Execute job, kills any pre-existing listener jobs first, and
// waits for the new one to accept connections. Returns the job_id so the
// caller can cancel it on teardown and watch it for early death.
func startRemoteListener(ctx context.Context, client pb.NodeAgentClient, authToken string, logf func(string, ...any)) (string, error) {
	cancelStaleListeners(ctx, client, logf)

	// Build the command. We invoke gokd-mcp.exe directly (no
	// Start-Process); the agent's Detach branch already gives us
	// OS-level detachment via the JobManager.
	cmd := fmt.Sprintf(`& '%s\gokd-mcp.exe' -listen 127.0.0.1:%d -log '%s' -auth-token %s`,
		remoteInstallDir, remoteListenPort, remoteLogPath, authToken)
	logf("starting remote gokd-mcp listener (tracked job)")

	stream, err := client.Execute(ctx, &pb.ExecuteRequest{
		Command:        cmd,
		Shell:          "powershell",
		WorkingDir:     remoteInstallDir,
		Detach:         true,
		TimeoutSeconds: 0,
	})
	if err != nil {
		return "", fmt.Errorf("execute detach: %w", err)
	}
	var jobID string
	for {
		msg, rerr := stream.Recv()
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return jobID, fmt.Errorf("execute recv: %w", rerr)
		}
		if msg.JobId != "" {
			jobID = msg.JobId
		}
		if msg.Done {
			break
		}
	}
	if jobID == "" {
		return "", errors.New("agent did not return a job_id for detached gokd-mcp")
	}
	logf("listener job %s started", jobID)

	// Wait for the listener to accept. We probe via Forward + AUTH so we
	// also validate the new auth token round-trips; a successful probe
	// rules out both transport and auth failures. Bail out early if the
	// job dies before accepting (e.g. -auth-token rejected by parseFlags
	// on an older binary).
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := probeForwardAuth(ctx, client, authToken); err == nil {
			logf("remote engine ready")
			return jobID, nil
		}
		if job, gerr := client.GetJob(ctx, &pb.GetJobRequest{JobId: jobID}); gerr == nil &&
			job.Job != nil && isTerminalStatus(job.Job.Status) {
			tail := fetchJobTail(ctx, client, jobID, 100)
			return jobID, fmt.Errorf("listener job %s terminated before accepting: status=%v exit=%d output=%s",
				jobID, job.Job.Status, job.Job.ExitCode, tail)
		}
		time.Sleep(250 * time.Millisecond)
	}
	return jobID, errors.New("remote engine did not come up within 15s")
}

// cancelStaleListeners enumerates currently-running jobs and cancels any
// whose command matches a previous gokd-mcp -listen invocation. Also
// runs a belt-and-braces Stop-Process for pre-t4-3 untracked listeners.
//
// Per design § 6.3.4 we deliberately do NOT try to reuse a stale listener
// — its auth token was generated by a previous proxy session and is now
// unknown. Cancel-and-respawn is simpler and ~1s in the noise of
// deployToRemote's hash check.
func cancelStaleListeners(ctx context.Context, client pb.NodeAgentClient, logf func(string, ...any)) {
	resp, err := client.ListJobs(ctx, &pb.ListJobsRequest{
		StatusFilter: pb.JobStatus_JOB_STATUS_RUNNING,
		Limit:        50,
	})
	if err != nil {
		logf("list jobs: %v", err)
	} else {
		for _, j := range resp.Jobs {
			if !strings.Contains(j.Command, listenerJobMarker) {
				continue
			}
			logf("cancelling stale listener job %s", j.JobId)
			_, _ = client.CancelJob(ctx, &pb.CancelJobRequest{JobId: j.JobId, Force: true})
		}
	}
	// Belt-and-braces for pre-t4-3 untracked instances (Start-Process
	// orphans). Ignore errors.
	_, _, _, _ = remoteRun(ctx, client,
		`Get-Process -Name gokd-mcp -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue`,
		10)
}

// fetchJobTail retrieves the most recent N lines of stdout+stderr for a
// job. Used in error messages so a "listener died" failure shows what
// actually went wrong on the node.
func fetchJobTail(ctx context.Context, client pb.NodeAgentClient, jobID string, lines int32) string {
	resp, err := client.GetJobOutput(ctx, &pb.GetJobOutputRequest{
		JobId:     jobID,
		Stream:    pb.GetJobOutputRequest_BOTH,
		TailLines: lines,
		MaxBytes:  64 * 1024,
	})
	if err != nil {
		return fmt.Sprintf("(no output: %v)", err)
	}
	out := strings.TrimRight(string(resp.Stdout), "\r\n")
	if len(resp.Stderr) > 0 {
		if out != "" {
			out += "\n"
		}
		out += "--stderr--\n" + strings.TrimRight(string(resp.Stderr), "\r\n")
	}
	if resp.Truncated {
		out += "\n(truncated)"
	}
	return out
}

// isTerminalStatus reports whether the JobStatus indicates the job is no
// longer running. Used by watchListener and the startup probe loop.
func isTerminalStatus(s pb.JobStatus) bool {
	switch s {
	case pb.JobStatus_JOB_STATUS_EXITED,
		pb.JobStatus_JOB_STATUS_CANCELED,
		pb.JobStatus_JOB_STATUS_ORPHANED:
		return true
	}
	return false
}

// probeForwardAuth opens a Forward stream, performs the AUTH handshake,
// and immediately half-closes — verifying the listener is accepting
// connections AND that the auth token round-trips. Used by startRemoteListener
// to wait for readiness without spamming "auth failed" warnings on the
// listener side (which a token-less probe would).
func probeForwardAuth(ctx context.Context, client pb.NodeAgentClient, authToken string) error {
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	stream, err := client.Forward(pctx)
	if err != nil {
		return err
	}
	if err := stream.Send(&pb.ForwardChunk{TargetAddr: fmt.Sprintf("127.0.0.1:%d", remoteListenPort)}); err != nil {
		return err
	}
	if authToken != "" {
		if err := stream.Send(&pb.ForwardChunk{Data: []byte("AUTH " + authToken + "\n")}); err != nil {
			return err
		}
		if err := readAuthResp(stream); err != nil {
			return err
		}
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

// authenticateRemoteStream sends AUTH on an open Forward stream and
// waits for OK\n. Returns a typed error on DENIED or EOF so the caller
// can include the result in shutdown logging.
func authenticateRemoteStream(stream pb.NodeAgent_ForwardClient, authToken string) error {
	if authToken == "" {
		return nil
	}
	if err := stream.Send(&pb.ForwardChunk{Data: []byte("AUTH " + authToken + "\n")}); err != nil {
		return fmt.Errorf("send AUTH: %w", err)
	}
	return readAuthResp(stream)
}

// readAuthResp reads ForwardChunk{Data:...} frames from the stream until
// it has seen a complete OK\n or DENIED\n line, then returns. Anything
// other than OK is reported as an error. The first complete line MUST
// fit in the first response frame — the gokd-mcp server side writes the
// response in a single Write so this is safe in practice.
func readAuthResp(stream pb.NodeAgent_ForwardClient) error {
	var buf bytes.Buffer
	for {
		msg, err := stream.Recv()
		if err != nil {
			return fmt.Errorf("recv auth resp: %w", err)
		}
		if len(msg.Data) > 0 {
			buf.Write(msg.Data)
		}
		if msg.Close {
			return errors.New("remote closed before AUTH response")
		}
		if idx := bytes.IndexByte(buf.Bytes(), '\n'); idx >= 0 {
			line := strings.TrimRight(buf.String()[:idx+1], "\r\n")
			switch line {
			case "OK":
				return nil
			case "DENIED":
				return errors.New("remote gokd-mcp denied AUTH")
			default:
				return fmt.Errorf("unexpected AUTH response %q", line)
			}
		}
	}
}

// shuttleStdio bridges the local stdin/stdout with the Forward stream.
// Runs two goroutines: stdin->stream and stream->stdout. Either side
// closing terminates the proxy.
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
