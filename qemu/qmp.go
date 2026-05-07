package qemu

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

type QMPClient struct {
	conn    net.Conn
	scanner *bufio.Scanner
}

type qmpRequest struct {
	Execute   string         `json:"execute"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type qmpResponse struct {
	Return json.RawMessage `json:"return,omitempty"`
	Error  *qmpError       `json:"error,omitempty"`
}

type qmpError struct {
	Class string `json:"class"`
	Desc  string `json:"desc"`
}

// NewQMPClient dials the QMP Unix socket, retrying for up to timeout.
func NewQMPClient(socketPath string, timeout time.Duration) (*QMPClient, error) {
	deadline := time.Now().Add(timeout)
	var conn net.Conn
	var err error
	for time.Now().Before(deadline) {
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("QMP dial %s: %w", socketPath, err)
	}
	c := &QMPClient{conn: conn, scanner: bufio.NewScanner(conn)}
	// Consume the greeting banner.
	if !c.scanner.Scan() {
		_ = conn.Close()
		return nil, fmt.Errorf("QMP: no greeting received")
	}
	if err := c.Capabilities(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *QMPClient) execute(cmd string, args map[string]any) (json.RawMessage, error) {
	req := qmpRequest{Execute: cmd, Arguments: args}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if _, err := c.conn.Write(data); err != nil {
		return nil, err
	}
	if !c.scanner.Scan() {
		return nil, fmt.Errorf("QMP: connection closed")
	}
	var resp qmpResponse
	if err := json.Unmarshal(c.scanner.Bytes(), &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("QMP error %s: %s", resp.Error.Class, resp.Error.Desc)
	}
	return resp.Return, nil
}

func (c *QMPClient) Capabilities() error {
	_, err := c.execute("qmp_capabilities", nil)
	return err
}

func (c *QMPClient) SystemPowerdown() error {
	_, err := c.execute("system_powerdown", nil)
	return err
}

type StatusResult struct {
	Status string `json:"status"`
}

func (c *QMPClient) QueryStatus() (*StatusResult, error) {
	raw, err := c.execute("query-status", nil)
	if err != nil {
		return nil, err
	}
	var s StatusResult
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (c *QMPClient) Close() error {
	return c.conn.Close()
}
