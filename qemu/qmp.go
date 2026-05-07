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

// GuestPing sends a guest-ping command via the QEMU guest agent channel.
// Returns nil if the guest agent responds successfully.
func (c *QMPClient) GuestPing() error {
	_, err := c.execute("guest-ping", nil)
	return err
}

// QMPRawCounters holds cumulative I/O counters from a single QMP poll.
type QMPRawCounters struct {
	BalloonActual uint64
	DiskRdBytes   uint64
	DiskWrBytes   uint64
	NetRxBytes    uint64
	NetTxBytes    uint64
}

type balloonResp struct {
	Actual uint64 `json:"actual"`
}

type blockStatEntry struct {
	Stats struct {
		RdBytes uint64 `json:"rd_bytes"`
		WrBytes uint64 `json:"wr_bytes"`
	} `json:"stats"`
}

type netStatEntry struct {
	RxBytes uint64 `json:"rx-bytes"`
	TxBytes uint64 `json:"tx-bytes"`
}

// QueryRaw fetches balloon, block, and net stats and returns raw cumulative counters.
func (c *QMPClient) QueryRaw() (QMPRawCounters, error) {
	var out QMPRawCounters

	balloonRaw, err := c.execute("query-balloon", nil)
	if err == nil {
		var b balloonResp
		if json.Unmarshal(balloonRaw, &b) == nil {
			out.BalloonActual = b.Actual
		}
	}

	blockRaw, err := c.execute("query-blockstats", nil)
	if err == nil {
		var list []blockStatEntry
		if json.Unmarshal(blockRaw, &list) == nil {
			for _, e := range list {
				out.DiskRdBytes += e.Stats.RdBytes
				out.DiskWrBytes += e.Stats.WrBytes
			}
		}
	}

	netRaw, err := c.execute("query-net", nil)
	if err == nil {
		var list []netStatEntry
		if json.Unmarshal(netRaw, &list) == nil {
			for _, e := range list {
				out.NetRxBytes += e.RxBytes
				out.NetTxBytes += e.TxBytes
			}
		}
	}

	return out, nil
}
