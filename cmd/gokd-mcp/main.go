// Command gokd-mcp exposes GoKD debugging operations as MCP tools over stdio.
package main

/*
#cgo windows LDFLAGS: -static -static-libstdc++ -static-libgcc
*/
import "C"

import (
	"bufio"
	"context"
	"crypto/subtle"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nijosmsft/gokd"
)

type config struct {
	symbols   string
	logPath   string
	logLevel  string
	logJSON   bool
	listen    string
	remote    string
	authToken string
	readonly  bool
	unsafeRaw bool
}

func main() {
	cfg := parseFlags()

	logWriter, closeLog, err := setupLogWriter(cfg.logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "log setup failed: %v\n", err)
		os.Exit(1)
	}
	defer closeLog()

	level, err := parseLogLevel(cfg.logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	logger := buildLogger(logWriter, level, cfg.logJSON)

	if cfg.remote != "" {
		if err := runRemoteProxy(cfg, logWriter); err != nil {
			fmt.Fprintf(os.Stderr, "remote proxy failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	var newOpts []gokd.Option
	if cfg.symbols != "" {
		newOpts = append(newOpts, gokd.WithSymbolPath(cfg.symbols))
	} else {
		newOpts = append(newOpts, gokd.WithDefaultSymbols())
	}

	sess, err := gokd.New(newOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "New() failed: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		_ = sess.Detach()
		_ = sess.Close()
	}()

	state := newSrv(sess, cfg.readonly, cfg.unsafeRaw)
	drain := newDrainer(state, logger)
	drain.run(sess)

	makeServer := func() *mcp.Server {
		s := mcp.NewServer(&mcp.Implementation{Name: "gokd-mcp", Version: "0.1.0"}, &mcp.ServerOptions{
			Instructions: "Stateful MCP server for Windows DbgEng debugging through GoKD. Attach or open a target before inspection tools.",
			Logger:       logger,
		})
		registerTools(s, state)
		drain.addServer(s)
		return s
	}

	if cfg.listen != "" {
		if cfg.authToken == "" {
			fmt.Fprintln(logWriter, "[gokd-mcp] WARNING: -listen without -auth-token; any local process can connect.")
		}
		if err := runListen(cfg.listen, cfg.authToken, makeServer, logger, logWriter); err != nil {
			fmt.Fprintf(os.Stderr, "listen failed: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if cfg.authToken != "" && cfg.remote == "" {
		fmt.Fprintln(logWriter, "[gokd-mcp] note: -auth-token has no effect without -listen; stdio is inside the process boundary.")
	}

	if err := makeServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server failed: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.symbols, "symbols", "", "set symbol path at startup (default: Microsoft public symbols via WithDefaultSymbols)")
	flag.StringVar(&cfg.logPath, "log", envOr("GOKD_MCP_LOG", ""), "log MCP traffic and engine output to this file (env: GOKD_MCP_LOG)")
	flag.StringVar(&cfg.logLevel, "log-level", envOr("GOKD_MCP_LOG_LEVEL", "info"), "slog level: debug|info|warn|error (env: GOKD_MCP_LOG_LEVEL, default info). Engine output is logged at debug; events at info.")
	flag.BoolVar(&cfg.logJSON, "log-json", envBool("GOKD_MCP_LOG_JSON", false), "emit logs as JSON instead of text (env: GOKD_MCP_LOG_JSON)")
	flag.StringVar(&cfg.listen, "listen", "", "serve MCP over TCP instead of stdio, e.g. 127.0.0.1:8765 (one client at a time)")
	flag.StringVar(&cfg.remote, "remote", "", "run as a stdio proxy to a remote gokd-mcp on the named lablink node")
	flag.StringVar(&cfg.authToken, "auth-token", envOr("GOKD_MCP_AUTH_TOKEN", ""), "require -listen clients to present this token via 'AUTH <token>\\n' before MCP starts. Min 16 chars, no whitespace. Empty (default) disables auth.")
	flag.BoolVar(&cfg.readonly, "readonly", false, "reject any tool that can modify the target (writes, breakpoints, execution)")
	flag.BoolVar(&cfg.unsafeRaw, "unsafe-raw", false, "with --readonly, allow execute_raw but enforce a command denylist")
	flag.Parse()
	if cfg.authToken != "" {
		if strings.ContainsAny(cfg.authToken, "\r\n \t") {
			fmt.Fprintln(os.Stderr, "-auth-token must not contain whitespace")
			os.Exit(2)
		}
		if len(cfg.authToken) < 16 {
			fmt.Fprintln(os.Stderr, "-auth-token must be at least 16 chars")
			os.Exit(2)
		}
	}
	return cfg
}

// envOr returns the value of key from the environment if set and non-empty,
// otherwise def. Used to give every gokd-mcp flag a matching env-var
// equivalent so the binary can be configured without rewriting launch
// scripts (matches the existing GOKD_DBGENG_PATH convention in the shim).
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envBool parses a boolean env var, recognising 1/true/yes/on as true and
// 0/false/no/off as false. Any other value (including empty) returns def.
func envBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

// parseLogLevel converts the human-readable level string accepted by the
// -log-level flag into a slog.Level. The empty string maps to LevelInfo so
// callers that pass through unset env vars still get the documented
// default. Unknown values return a typed error suitable for stderr.
func parseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error", "err":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("invalid log level %q (want debug|info|warn|error)", s)
	}
}

// buildLogger constructs a *slog.Logger that writes to w at the given
// level, using either the text or JSON handler depending on asJSON.
func buildLogger(w io.Writer, level slog.Level, asJSON bool) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if asJSON {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h)
}

// runListen accepts TCP connections one at a time and serves each as a
// dedicated MCP session against the shared DbgEng session. DbgEng is
// inherently single-client, so concurrent connections are intentionally
// serialised: the next Accept does not happen until the active session
// closes.
//
// When authToken is non-empty, every accepted conn must complete a
// "AUTH <token>\n" -> "OK\n" / "DENIED\n" handshake within 5s before any
// MCP bytes are read. The handshake uses crypto/subtle for constant-time
// comparison and never echoes the presented token. See t3-6 for the wire
// format and rationale.
func runListen(addr, authToken string, makeServer func() *mcp.Server, logger *slog.Logger, logWriter io.Writer) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	defer listener.Close()
	fmt.Fprintf(logWriter, "[gokd-mcp] listening on %s (auth=%v)\n", listener.Addr(), authToken != "")

	for {
		conn, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("accept: %w", err)
		}
		peer := conn.RemoteAddr().String()
		if err := authenticateConn(conn, authToken); err != nil {
			if logger != nil {
				logger.Warn("auth failed",
					slog.String("peer", peer),
					slog.String("err", err.Error()))
			}
			fmt.Fprintf(logWriter, "[gokd-mcp] auth failed from %s: %v\n", peer, err)
			_ = conn.Close()
			continue
		}
		fmt.Fprintf(logWriter, "[gokd-mcp] client connected: %s\n", peer)
		serveConn(conn, makeServer(), logWriter)
		fmt.Fprintf(logWriter, "[gokd-mcp] client disconnected: %s\n", peer)
	}
}

// authenticateConn implements the t3-6 line-prefix handshake. When token
// is empty it accepts the conn unconditionally (legacy behaviour). When
// set, it reads up to one line within a 5s deadline, requires the form
// "AUTH <token>\n" with a constant-time byte-compare against token, and
// either writes "OK\n" + clears the deadline, or writes "DENIED\n" and
// returns an error so the caller can close.
//
// The handshake is intentionally NOT MCP-spec: the spec has no session
// auth at initialize-time today. A line-prefix in front of the JSON-RPC
// stream is dropped trivially when an MCP-native mechanism appears.
func authenticateConn(conn net.Conn, token string) error {
	if token == "" {
		return nil
	}
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return fmt.Errorf("set deadline: %w", err)
	}
	br := bufio.NewReaderSize(conn, 1024)
	line, err := br.ReadString('\n')
	if err != nil {
		_, _ = io.WriteString(conn, "DENIED\n")
		return fmt.Errorf("read auth line: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	const prefix = "AUTH "
	if !strings.HasPrefix(line, prefix) {
		_, _ = io.WriteString(conn, "DENIED\n")
		return fmt.Errorf("missing AUTH prefix")
	}
	presented := []byte(line[len(prefix):])
	expected := []byte(token)
	if len(presented) != len(expected) ||
		subtle.ConstantTimeCompare(presented, expected) != 1 {
		_, _ = io.WriteString(conn, "DENIED\n")
		return fmt.Errorf("token mismatch")
	}
	if _, err := io.WriteString(conn, "OK\n"); err != nil {
		return fmt.Errorf("write OK: %w", err)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear deadline: %w", err)
	}
	// We require AUTH and the MCP first-byte to arrive in separate writes
	// so the bufio reader holds nothing once handshake completes. Clients
	// that pipeline (write "AUTH X\n{json...}" in a single packet) get
	// rejected. The proxy in proxy.go does the right thing.
	if br.Buffered() > 0 {
		return fmt.Errorf("unexpected bytes after AUTH line")
	}
	return nil
}

func serveConn(conn net.Conn, server *mcp.Server, logWriter io.Writer) {
	defer conn.Close()
	transport := &mcp.IOTransport{Reader: conn, Writer: nopCloser{Writer: conn}}
	if err := server.Run(context.Background(), transport); err != nil {
		fmt.Fprintf(logWriter, "[gokd-mcp] session ended: %v\n", err)
	}
}

// nopCloser turns an io.Writer into an io.WriteCloser whose Close is a no-op.
// The MCP IOTransport expects a WriteCloser; closing it here would also close
// the underlying TCP read side via conn.Close() in serveConn.
type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

func setupLogWriter(path string) (io.Writer, func(), error) {
	if path == "" {
		return os.Stderr, func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		return nil, nil, err
	}
	return io.MultiWriter(os.Stderr, f), func() { _ = f.Close() }, nil
}
