package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
)

// Handler dispatches a Request and produces a Response. Returning a non-nil
// error short-circuits to an error response; otherwise the response is sent
// verbatim.
type Handler func(ctx context.Context, req Request) (Response, error)

// Server accepts connections on a unix socket and dispatches requests.
type Server struct {
	path    string
	ln      net.Listener
	handler Handler
}

// NewServer creates the socket file and starts listening. The socket is 0600.
// If a stale socket exists at path and no daemon is alive, it's removed
// first; otherwise an error is returned.
func NewServer(path string, handler Handler) (*Server, error) {
	if err := EnsureSocketDir(path); err != nil {
		return nil, err
	}
	if err := removeIfStale(path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod %s: %w", path, err)
	}
	return &Server{path: path, ln: ln, handler: handler}, nil
}

// Serve blocks accepting connections until ctx is cancelled or Close is
// called. Either path removes the socket file before returning so a future
// daemon instance doesn't trip the stale-socket check.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = s.ln.Close()
	}()
	defer os.Remove(s.path)
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(ctx, conn)
	}
}

// Close removes the socket file. Safe to call multiple times.
func (s *Server) Close() error {
	err := s.ln.Close()
	_ = os.Remove(s.path)
	return err
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		var req Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			writeJSON(conn, Response{OK: false, Error: "bad request: " + err.Error()})
			return
		}
		resp, err := s.handler(ctx, req)
		if err != nil {
			resp = Response{OK: false, Error: err.Error()}
		}
		writeJSON(conn, resp)
		return
	}
}

func writeJSON(w net.Conn, v any) {
	b, _ := json.Marshal(v)
	b = append(b, '\n')
	_, _ = w.Write(b)
}

// removeIfStale: if path exists but no one is listening, unlink it. If
// someone IS listening, return an error.
func removeIfStale(path string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	conn, err := net.Dial("unix", path)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("claude-watch daemon already running (socket %s is live)", path)
	}
	return os.Remove(path)
}
