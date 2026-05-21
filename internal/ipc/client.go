package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// Send opens a one-shot connection to the daemon socket, sends req, and
// returns the parsed response. Returns an error if the daemon isn't reachable.
func Send(path string, req Request, timeout time.Duration) (Response, error) {
	var zero Response
	conn, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return zero, fmt.Errorf("connect to daemon at %s: %w", path, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	b, err := json.Marshal(req)
	if err != nil {
		return zero, err
	}
	b = append(b, '\n')
	if _, err := conn.Write(b); err != nil {
		return zero, fmt.Errorf("write: %w", err)
	}

	br := bufio.NewReaderSize(conn, 1<<16)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return zero, fmt.Errorf("read: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return zero, fmt.Errorf("decode: %w (raw: %s)", err, string(line))
	}
	return resp, nil
}

// IsDaemonRunning reports whether something is listening on the socket.
func IsDaemonRunning(path string) bool {
	conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
