// Command gokd-mcp exposes GoKD debugging operations as MCP tools over stdio.
package main

/*
#cgo windows LDFLAGS: -static -static-libstdc++ -static-libgcc
*/
import "C"

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nijosmsft/gokd"
)

type config struct {
	symbols   string
	logPath   string
	listen    string
	remote    string
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

	if cfg.remote != "" {
		if err := runRemoteProxy(cfg, logWriter); err != nil {
			fmt.Fprintf(os.Stderr, "remote proxy failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	engineLog := log.New(logWriter, "[engine] ", 0)

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
	drain := newDrainer(state, engineLog)
	drain.run(sess)

	makeServer := func() *mcp.Server {
		s := mcp.NewServer(&mcp.Implementation{Name: "gokd-mcp", Version: "0.1.0"}, &mcp.ServerOptions{
			Instructions: "Stateful MCP server for Windows DbgEng debugging through GoKD. Attach or open a target before inspection tools.",
			Logger:       slog.New(slog.NewTextHandler(logWriter, nil)),
		})
		registerTools(s, state)
		drain.addServer(s)
		return s
	}

	if cfg.listen != "" {
		if err := runListen(cfg.listen, makeServer, logWriter); err != nil {
			fmt.Fprintf(os.Stderr, "listen failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := makeServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server failed: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.symbols, "symbols", "", "set symbol path at startup (default: Microsoft public symbols via WithDefaultSymbols)")
	flag.StringVar(&cfg.logPath, "log", "", "log MCP traffic and engine output to this file")
	flag.StringVar(&cfg.listen, "listen", "", "serve MCP over TCP instead of stdio, e.g. 127.0.0.1:8765 (one client at a time)")
	flag.StringVar(&cfg.remote, "remote", "", "run as a stdio proxy to a remote gokd-mcp on the named lablink node")
	flag.BoolVar(&cfg.readonly, "readonly", false, "reject any tool that can modify the target (writes, breakpoints, execution)")
	flag.BoolVar(&cfg.unsafeRaw, "unsafe-raw", false, "with --readonly, allow execute_raw but enforce a command denylist")
	flag.Parse()
	return cfg
}

// runListen accepts TCP connections one at a time and serves each as a
// dedicated MCP session against the shared DbgEng session. DbgEng is
// inherently single-client, so concurrent connections are intentionally
// serialised: the next Accept does not happen until the active session
// closes.
func runListen(addr string, makeServer func() *mcp.Server, logWriter io.Writer) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	defer listener.Close()
	fmt.Fprintf(logWriter, "[gokd-mcp] listening on %s\n", listener.Addr())

	for {
		conn, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("accept: %w", err)
		}
		fmt.Fprintf(logWriter, "[gokd-mcp] client connected: %s\n", conn.RemoteAddr())
		serveConn(conn, makeServer(), logWriter)
		fmt.Fprintf(logWriter, "[gokd-mcp] client disconnected: %s\n", conn.RemoteAddr())
	}
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
