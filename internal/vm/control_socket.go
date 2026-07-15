package vm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
)

// DaemonSocketPath returns the path to the daemon's control socket. The daemon
// listens here; the CLI connects here to route QMP commands to the daemon,
// which owns the VMs' single QMP connections.
func (m *Manager) DaemonSocketPath() string {
	// StoragePath is ~/.vee/vms; the control socket lives one level up in ~/.vee
	// so it is not mistaken for a per-VM artifact.
	return filepath.Join(filepath.Dir(m.storagePath()), "daemon.sock")
}

// controlRequest is a single line-delimited JSON request on the control socket.
type controlRequest struct {
	Op        string         `json:"op"`
	VM        string         `json:"vm,omitempty"`
	Execute   string         `json:"execute,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// controlResponse is the reply to a controlRequest. Exactly one of Return or
// Error is populated on success/failure respectively.
type controlResponse struct {
	Return json.RawMessage `json:"return,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// serveControlSocket listens on the daemon control socket and serves requests
// until ctx is cancelled. It is best-effort: a listener error is logged and the
// daemon continues (QMP routing is unavailable, but VM supervision is not
// affected).
func (m *Manager) serveControlSocket(ctx context.Context) {
	log := m.provider.Logger()
	sockPath := m.DaemonSocketPath()

	// Remove a stale socket from a previous daemon that did not clean up.
	if err := os.Remove(sockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Debug("control socket: could not remove stale socket",
			zap.String("path", sockPath), zap.Error(err))
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "unix", sockPath)
	if err != nil {
		log.Warn("control socket: listen failed; `vee qmp` routing unavailable",
			zap.String("path", sockPath), zap.Error(err))
		return
	}
	// Owner-only: the socket exposes QMP command execution against local VMs.
	if err := os.Chmod(sockPath, 0o600); err != nil {
		log.Debug("control socket: chmod failed", zap.Error(err))
	}
	log.Info("control socket listening", zap.String("path", sockPath))

	// Close the listener when ctx is cancelled so Accept unblocks and the
	// socket file is removed.
	go func() {
		<-ctx.Done()
		_ = ln.Close()
		_ = os.Remove(sockPath)
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return // shutting down
			}
			log.Debug("control socket: accept error", zap.Error(err))
			continue
		}
		go m.handleControlConn(ctx, conn)
	}
}

func (m *Manager) handleControlConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	if !scanner.Scan() {
		return
	}

	var req controlRequest
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		writeControlResponse(conn, controlResponse{Error: fmt.Sprintf("parse request: %v", err)})
		return
	}

	resp := m.dispatchControl(ctx, req)
	writeControlResponse(conn, resp)
}

// dispatchControl routes a control request to its handler.
func (m *Manager) dispatchControl(_ context.Context, req controlRequest) controlResponse {
	switch req.Op {
	case "qmp":
		if req.VM == "" || req.Execute == "" {
			return controlResponse{Error: "qmp request requires vm and execute fields"}
		}
		// The owner may still be registering if the daemon just started and is
		// adopting running VMs; wait briefly before giving up.
		m.waitOwner(req.VM, 3*time.Second)
		raw, err := m.ExecuteQMP(req.VM, req.Execute, req.Arguments)
		if err != nil {
			if IsErrQMPNotOwned(err) {
				return controlResponse{Error: fmt.Sprintf("daemon does not own a QMP connection for VM %q", req.VM)}
			}
			return controlResponse{Error: err.Error()}
		}
		return controlResponse{Return: raw}
	case "ping":
		return controlResponse{Return: json.RawMessage(`"pong"`)}
	default:
		return controlResponse{Error: fmt.Sprintf("unknown op %q", req.Op)}
	}
}

func writeControlResponse(conn net.Conn, resp controlResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		data, _ = json.Marshal(controlResponse{Error: "marshal response failed"})
	}
	data = append(data, '\n')
	_, _ = conn.Write(data)
}

// QMPViaDaemon connects to the daemon control socket and asks the daemon to run
// a QMP command on the VM it owns. Returns the raw "return" payload. The bool
// reports whether the daemon was reachable at all: false means no daemon is
// listening (caller may fall back to a direct QMP dial), true means the daemon
// answered — err then reflects the command result.
func (m *Manager) QMPViaDaemon(ctx context.Context, vmName, execute string, args map[string]any) (json.RawMessage, bool, error) {
	sockPath := m.DaemonSocketPath()

	var dialer net.Dialer
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := dialer.DialContext(dctx, "unix", sockPath)
	if err != nil {
		// No daemon listening.
		return nil, false, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	req := controlRequest{Op: "qmp", VM: vmName, Execute: execute, Arguments: args}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, true, err
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return nil, true, fmt.Errorf("write control request: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	if !scanner.Scan() {
		if scanErr := scanner.Err(); scanErr != nil {
			return nil, true, fmt.Errorf("read control response: %w", scanErr)
		}
		return nil, true, fmt.Errorf("daemon closed control connection without responding")
	}
	var resp controlResponse
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return nil, true, fmt.Errorf("parse control response: %w", err)
	}
	if resp.Error != "" {
		return nil, true, fmt.Errorf("%s", resp.Error)
	}
	return resp.Return, true, nil
}
