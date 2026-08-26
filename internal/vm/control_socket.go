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
	"strings"
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
	// NotOwned marks the daemon answering "I don't own this VM's QMP
	// connection" — the socket may be free, so callers can dial it directly
	// instead of failing.
	NotOwned bool `json:"not_owned,omitempty"`
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
func (m *Manager) dispatchControl(ctx context.Context, req controlRequest) controlResponse {
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
				return controlResponse{
					Error:    fmt.Sprintf("daemon does not own a QMP connection for VM %q", req.VM),
					NotOwned: true,
				}
			}
			return controlResponse{Error: err.Error()}
		}
		return controlResponse{Return: raw}
	case "tunnel.list":
		statuses, err := m.TunnelStatuses()
		if err != nil {
			return controlResponse{Error: err.Error()}
		}
		raw, err := json.Marshal(statuses)
		if err != nil {
			return controlResponse{Error: fmt.Sprintf("marshal tunnel statuses: %v", err)}
		}
		return controlResponse{Return: raw}
	case "tunnel.reload":
		// The CLI writes the registry directly, then asks the daemon to
		// reconcile immediately rather than waiting out the poll interval —
		// so `--background` prints a URL that already works.
		m.reconcileTunnels(ctx)
		statuses, err := m.TunnelStatuses()
		if err != nil {
			return controlResponse{Error: err.Error()}
		}
		raw, err := json.Marshal(statuses)
		if err != nil {
			return controlResponse{Error: fmt.Sprintf("marshal tunnel statuses: %v", err)}
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
	// Surface not-owned as the sentinel so callers fall back to a direct
	// dial: the daemon answered, but the VM's QMP socket is not held by it.
	// The string match covers daemons from before the not_owned field that
	// are still running with the old error-only reply.
	if resp.NotOwned || strings.Contains(resp.Error, "does not own a QMP connection") {
		return nil, true, errQMPNotOwned
	}
	if resp.Error != "" {
		return nil, true, fmt.Errorf("%s", resp.Error)
	}
	return resp.Return, true, nil
}

// TunnelsViaDaemon asks the daemon for the live state of every registered
// background tunnel. The bool reports whether a daemon answered *and*
// understands the tunnel ops: false means none is running, or the one that is
// predates background tunnels. Either way the caller falls back to reporting
// the registry alone (registered, but nothing serving them).
func (m *Manager) TunnelsViaDaemon(ctx context.Context, reload bool) ([]TunnelStatus, bool, error) {
	op := "tunnel.list"
	if reload {
		op = "tunnel.reload"
	}
	raw, reachable, err := m.controlCall(ctx, controlRequest{Op: op})
	if err != nil {
		// A daemon from before background tunnels rejects the op outright.
		// Treat it as "no capable daemon" rather than a hard error, so the
		// CLI degrades to the registry view and tells the user to restart
		// the daemon instead of failing with a protocol error.
		if strings.Contains(err.Error(), "unknown op") {
			return nil, false, ErrDaemonTooOld
		}
		return nil, reachable, err
	}
	var statuses []TunnelStatus
	if err := json.Unmarshal(raw, &statuses); err != nil {
		return nil, true, fmt.Errorf("parse tunnel statuses: %w", err)
	}
	return statuses, true, nil
}

// ErrDaemonTooOld reports that a daemon is running but predates the background
// tunnel control ops. Callers treat it like "no daemon" for fallback purposes
// while telling the user to restart the daemon rather than start one.
var ErrDaemonTooOld = errors.New("the running vee daemon predates background tunnels; restart it to pick them up")

// controlCall performs one request/response round trip on the daemon control
// socket. Shared by the tunnel ops; QMPViaDaemon keeps its own copy because it
// additionally interprets the not-owned sentinel.
func (m *Manager) controlCall(ctx context.Context, req controlRequest) (json.RawMessage, bool, error) {
	var dialer net.Dialer
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := dialer.DialContext(dctx, "unix", m.DaemonSocketPath())
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

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
