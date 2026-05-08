package qemu

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// QGAClient communicates with the QEMU guest agent over a Unix socket.
// Unlike QMP, QGA requires no capabilities handshake.
type QGAClient struct {
	conn net.Conn
	rd   *bufio.Reader
}

type qgaRequest struct {
	Execute   string         `json:"execute"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type qgaResponse struct {
	Return json.RawMessage `json:"return,omitempty"`
	Error  *qmpError       `json:"error,omitempty"`
}

// NewQGAClient dials the QGA Unix socket, retrying until timeout.
func NewQGAClient(socketPath string, timeout time.Duration) (*QGAClient, error) {
	deadline := time.Now().Add(timeout)
	var conn net.Conn
	var err error
	for time.Now().Before(deadline) {
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			return &QGAClient{conn: conn, rd: bufio.NewReader(conn)}, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, fmt.Errorf("QGA dial %s: %w", socketPath, err)
}

func (c *QGAClient) execute(cmd string, args map[string]any) (json.RawMessage, error) {
	req := qgaRequest{Execute: cmd, Arguments: args}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if _, err := c.conn.Write(data); err != nil {
		return nil, err
	}

	// Read one line response.
	line, err := c.rd.ReadBytes('\n')
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("QGA connection closed (VM may have stopped)")
		}
		return nil, fmt.Errorf("QGA read: %w", err)
	}
	buf := line

	var resp qgaResponse
	if err := json.Unmarshal(buf, &resp); err != nil {
		return nil, fmt.Errorf("QGA unmarshal: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("QGA error %s: %s", resp.Error.Class, resp.Error.Desc)
	}
	return resp.Return, nil
}

func (c *QGAClient) Close() error {
	return c.conn.Close()
}

// GuestIPAddress is an IP address entry from the guest agent.
type GuestIPAddress struct {
	IPAddress     string `json:"ip-address"`
	IPAddressType string `json:"ip-address-type"`
	Prefix        int    `json:"prefix"`
}

// GuestNetworkInterface is a network interface reported by the guest agent.
type GuestNetworkInterface struct {
	Name            string           `json:"name"`
	HardwareAddress string           `json:"hardware-address"`
	IPAddresses     []GuestIPAddress `json:"ip-addresses"`
}

// GuestNetworkGetInterfaces returns all network interfaces visible inside the guest.
func (c *QGAClient) GuestNetworkGetInterfaces() ([]GuestNetworkInterface, error) {
	raw, err := c.execute("guest-network-get-interfaces", nil)
	if err != nil {
		return nil, err
	}
	var ifaces []GuestNetworkInterface
	if err := json.Unmarshal(raw, &ifaces); err != nil {
		return nil, fmt.Errorf("QGA parse interfaces: %w", err)
	}
	return ifaces, nil
}

// GuestExec runs a command inside the guest and returns its PID.
func (c *QGAClient) GuestExec(path string, args []string, captureOutput bool) (int, error) {
	arguments := map[string]any{
		"path":           path,
		"capture-output": captureOutput,
	}
	if len(args) > 0 {
		arguments["arg"] = args
	}
	raw, err := c.execute("guest-exec", arguments)
	if err != nil {
		return 0, err
	}
	var result struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return 0, fmt.Errorf("QGA parse exec: %w", err)
	}
	return result.PID, nil
}

// GuestExecStatus holds the result of a completed guest-exec call.
type GuestExecStatus struct {
	Exited   bool
	ExitCode int
	OutData  []byte
	ErrData  []byte
}

type guestExecStatusRaw struct {
	Exited   bool   `json:"exited"`
	ExitCode int    `json:"exitcode"`
	OutData  string `json:"out-data"`
	ErrData  string `json:"err-data"`
}

// GuestExecStatus polls the status of a guest-exec PID.
func (c *QGAClient) GuestExecStatus(pid int) (GuestExecStatus, error) {
	raw, err := c.execute("guest-exec-status", map[string]any{"pid": pid})
	if err != nil {
		return GuestExecStatus{}, err
	}
	var r guestExecStatusRaw
	if err := json.Unmarshal(raw, &r); err != nil {
		return GuestExecStatus{}, fmt.Errorf("QGA parse exec-status: %w", err)
	}
	s := GuestExecStatus{
		Exited:   r.Exited,
		ExitCode: r.ExitCode,
	}
	if r.OutData != "" {
		s.OutData, _ = base64.StdEncoding.DecodeString(r.OutData)
	}
	if r.ErrData != "" {
		s.ErrData, _ = base64.StdEncoding.DecodeString(r.ErrData)
	}
	return s, nil
}

// RunCommand executes a command inside the guest and waits for it to finish.
// Returns stdout, stderr, exit code, and any transport error.
func (c *QGAClient) RunCommand(path string, args []string) (stdout string, stderr string, exitCode int, err error) {
	pid, err := c.GuestExec(path, args, true)
	if err != nil {
		return "", "", 0, err
	}
	for {
		status, err := c.GuestExecStatus(pid)
		if err != nil {
			return "", "", 0, err
		}
		if status.Exited {
			return string(status.OutData), string(status.ErrData), status.ExitCode, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
}
