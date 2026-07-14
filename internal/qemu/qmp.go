package qemu

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// executeTimeout bounds how long an individual QMP command waits for QEMU
// to respond. QEMU is normally instantaneous on these synchronous commands,
// so a short timeout is fine and prevents a wedged or paused VM from
// hanging `vee stop` (and the daemon's shutdown path) indefinitely.
const executeTimeout = 5 * time.Second

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

// QMPEvent is the minimal shape of an asynchronous QMP event. The event
// listener (see ReadEvent) decodes events with this struct so callers can
// dispatch on Event without parsing the full per-event Data schema.
type QMPEvent struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// ShutdownEventData is the payload of QMP's SHUTDOWN event. The boolean
// Guest is true when the guest OS initiated the shutdown (e.g. `poweroff`
// inside the VM), false when QEMU was asked from the outside (QMP
// system_powerdown, SIGTERM, host kill, …).
type ShutdownEventData struct {
	Guest  bool   `json:"guest"`
	Reason string `json:"reason,omitempty"`
}

// NewQMPClient dials the QMP Unix socket, retrying for up to timeout.
func NewQMPClient(ctx context.Context, socketPath string, timeout time.Duration) (*QMPClient, error) {
	deadline := time.Now().Add(timeout)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	var dialer net.Dialer
	var conn net.Conn
	var err error
	for time.Now().Before(deadline) {
		conn, err = dialer.DialContext(ctx, controlNetwork(), socketPath)
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("QMP dial %s: %w", socketPath, err)
	}
	c := &QMPClient{conn: conn, scanner: bufio.NewScanner(conn)}
	// Consume the greeting banner under the same deadline as a regular
	// command so a wedged QEMU cannot hang the dialer.
	_ = conn.SetReadDeadline(time.Now().Add(executeTimeout))
	if !c.scanner.Scan() {
		_ = conn.Close()
		return nil, fmt.Errorf("QMP: no greeting received: %w", c.scanner.Err())
	}
	_ = conn.SetReadDeadline(time.Time{})
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
	// Bound both the write and the read so a stalled QEMU cannot block
	// the caller indefinitely. Clear the deadline before returning so
	// subsequent calls on the same client get a fresh window.
	if err := c.conn.SetDeadline(time.Now().Add(executeTimeout)); err != nil {
		return nil, fmt.Errorf("QMP set deadline: %w", err)
	}
	defer func() { _ = c.conn.SetDeadline(time.Time{}) }()
	if _, err := c.conn.Write(data); err != nil {
		return nil, fmt.Errorf("QMP write %s: %w", cmd, err)
	}
	if !c.scanner.Scan() {
		if scanErr := c.scanner.Err(); scanErr != nil {
			return nil, fmt.Errorf("QMP %s: %w", cmd, scanErr)
		}
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

// NewQMPEventListener dials a dedicated QMP socket connection used purely
// for reading asynchronous events. It does not share a connection with
// QMPClient so command responses and events never interleave on the same
// scanner. Caller must call Close on the returned QMPClient when done; the
// connection is also closed automatically when QEMU exits.
func NewQMPEventListener(ctx context.Context, socketPath string, timeout time.Duration) (*QMPClient, error) {
	return NewQMPClient(ctx, socketPath, timeout)
}

// ReadEvent blocks until the next QMP event arrives on the connection.
// Command responses received on this connection are skipped silently.
// Returns io.EOF (wrapped) when the connection closes, which happens when
// QEMU exits.
func (c *QMPClient) ReadEvent() (*QMPEvent, error) {
	for {
		if !c.scanner.Scan() {
			if err := c.scanner.Err(); err != nil {
				return nil, fmt.Errorf("QMP read: %w", err)
			}
			return nil, fmt.Errorf("QMP: connection closed")
		}
		line := c.scanner.Bytes()
		var ev QMPEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			// Not a valid JSON object — skip.
			continue
		}
		if ev.Event == "" {
			// Command response or greeting; ignore.
			continue
		}
		return &ev, nil
	}
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
	// CPUNs is total vCPU wall-clock nanoseconds across all vCPUs (query-vcpu-time).
	// Zero if the guest does not support the command.
	CPUNs       uint64
	DiskRdBytes uint64
	DiskWrBytes uint64
	NetRxBytes  uint64
	NetTxBytes  uint64
}

type vcpuTimeEntry struct {
	// wall-clock time this vCPU has been scheduled, in nanoseconds
	WallNs uint64 `json:"wall-time-ns"`
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

// QueryRaw fetches balloon, block, net, and vCPU-time stats and returns raw cumulative counters.
func (c *QMPClient) QueryRaw() (QMPRawCounters, error) {
	var out QMPRawCounters

	balloonRaw, err := c.execute("query-balloon", nil)
	if err == nil {
		var b balloonResp
		if json.Unmarshal(balloonRaw, &b) == nil {
			out.BalloonActual = b.Actual
		}
	}

	vcpuRaw, err := c.execute("query-vcpu-time", nil)
	if err == nil {
		var vcpus []vcpuTimeEntry
		if json.Unmarshal(vcpuRaw, &vcpus) == nil {
			for _, v := range vcpus {
				out.CPUNs += v.WallNs
			}
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
