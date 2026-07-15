package qemu

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// QMPOwner owns a single, long-lived QMP monitor connection and multiplexes it
// between two concerns that would otherwise fight over the socket:
//
//   - synchronous command execution (query-status, system_powerdown, arbitrary
//     verbs from `vee qmp`), and
//   - asynchronous event delivery (SHUTDOWN, etc.) to a callback.
//
// QEMU's QMP socket (`-qmp ...,server,nowait`) accepts exactly one connected
// client at a time. Before QMPOwner, the daemon's shutdown watcher held that
// one connection for the VM's whole lifetime, so nothing else — not even the
// daemon itself — could issue a command. QMPOwner is the single client: it runs
// one reader goroutine that classifies each incoming line as either an event
// (has an "event" field) or a command response, and hands command responses to
// the goroutine currently waiting in Execute.
//
// Commands are serialized: QMP is request/response with no IDs by default, so
// at most one command may be in flight at a time. Execute takes cmdMu for the
// duration of a single command exchange.
type QMPOwner struct {
	conn    net.Conn
	scanner *bufio.Scanner

	// cmdMu serializes Execute calls so only one command is ever in flight.
	cmdMu sync.Mutex
	// respCh carries the reader goroutine's decoded command responses to the
	// Execute call currently holding cmdMu. Buffered by one so the reader never
	// blocks handing off a response.
	respCh chan qmpResponse

	// onEvent, if non-nil, is invoked for every asynchronous QMP event. It runs
	// on the reader goroutine, so it must not call Execute (that would deadlock
	// on the reader) and should return promptly.
	onEvent func(QMPEvent)

	closeOnce sync.Once
	done      chan struct{}
	// readErr holds the terminal error that ended the reader loop (connection
	// closed, QEMU exited). Guarded by errMu.
	errMu   sync.Mutex
	readErr error
}

// NewQMPOwner dials the QMP socket, negotiates capabilities, and starts the
// reader goroutine. onEvent may be nil if the caller only issues commands.
// The owner runs until Close is called or the connection drops (QEMU exit).
func NewQMPOwner(ctx context.Context, socketPath string, timeout time.Duration, onEvent func(QMPEvent)) (*QMPOwner, error) {
	deadline := time.Now().Add(timeout)
	dctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	var dialer net.Dialer
	var conn net.Conn
	var err error
	for time.Now().Before(deadline) {
		conn, err = dialer.DialContext(dctx, controlNetwork(), socketPath)
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("QMP dial %s: %w", socketPath, err)
	}

	o := &QMPOwner{
		conn:    conn,
		scanner: bufio.NewScanner(conn),
		respCh:  make(chan qmpResponse, 1),
		onEvent: onEvent,
		done:    make(chan struct{}),
	}
	// Allow long QMP lines (query-block on a many-disk VM can be large).
	o.scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	// Consume the greeting banner under the dial deadline so a wedged QEMU
	// cannot hang startup.
	_ = conn.SetReadDeadline(deadline)
	if !o.scanner.Scan() {
		_ = conn.Close()
		return nil, fmt.Errorf("QMP: no greeting received: %w", o.scanner.Err())
	}
	_ = conn.SetReadDeadline(time.Time{})

	// Negotiate capabilities synchronously before the reader goroutine starts,
	// so the handshake response is not mistaken for an event.
	if err := o.handshake(); err != nil {
		_ = conn.Close()
		return nil, err
	}

	go o.readLoop()
	return o, nil
}

// handshake sends qmp_capabilities and reads its single response inline, before
// the reader goroutine owns the scanner.
func (o *QMPOwner) handshake() error {
	if err := o.writeRequest(qmpRequest{Execute: "qmp_capabilities"}); err != nil {
		return err
	}
	_ = o.conn.SetReadDeadline(time.Now().Add(executeTimeout))
	defer func() { _ = o.conn.SetReadDeadline(time.Time{}) }()
	for {
		if !o.scanner.Scan() {
			if scanErr := o.scanner.Err(); scanErr != nil {
				return fmt.Errorf("QMP capabilities: %w", scanErr)
			}
			return fmt.Errorf("QMP: connection closed during handshake")
		}
		var resp qmpResponse
		if err := json.Unmarshal(o.scanner.Bytes(), &resp); err != nil {
			// A stray event could theoretically arrive; skip non-responses.
			var probe QMPEvent
			if json.Unmarshal(o.scanner.Bytes(), &probe) == nil && probe.Event != "" {
				continue
			}
			return err
		}
		if resp.Error != nil {
			return fmt.Errorf("QMP error %s: %s", resp.Error.Class, resp.Error.Desc)
		}
		return nil
	}
}

func (o *QMPOwner) writeRequest(req qmpRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := o.conn.Write(data); err != nil {
		return fmt.Errorf("QMP write %s: %w", req.Execute, err)
	}
	return nil
}

// readLoop is the single reader of the connection. It classifies each line as
// an event (dispatched to onEvent) or a command response (handed to respCh).
func (o *QMPOwner) readLoop() {
	defer close(o.done)
	for {
		if !o.scanner.Scan() {
			err := o.scanner.Err()
			if err == nil {
				err = fmt.Errorf("QMP: connection closed")
			}
			o.setReadErr(err)
			return
		}
		line := o.scanner.Bytes()

		var ev QMPEvent
		if json.Unmarshal(line, &ev) == nil && ev.Event != "" {
			if o.onEvent != nil {
				o.onEvent(ev)
			}
			continue
		}

		var resp qmpResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			// Unparseable, non-event line — ignore.
			continue
		}
		// Deliver to a waiting Execute. If nobody is waiting (unsolicited
		// response, which QMP should not produce), drop it rather than block.
		select {
		case o.respCh <- resp:
		default:
		}
	}
}

func (o *QMPOwner) setReadErr(err error) {
	o.errMu.Lock()
	if o.readErr == nil {
		o.readErr = err
	}
	o.errMu.Unlock()
}

// Execute runs a single QMP command and returns its raw "return" payload. It is
// safe for concurrent callers; commands are serialized internally.
func (o *QMPOwner) Execute(cmd string, args map[string]any) (json.RawMessage, error) {
	o.cmdMu.Lock()
	defer o.cmdMu.Unlock()

	// Drain any stale response left in the channel from a prior aborted call so
	// it cannot be mistaken for this command's reply.
	select {
	case <-o.respCh:
	default:
	}

	if err := o.writeRequest(qmpRequest{Execute: cmd, Arguments: args}); err != nil {
		return nil, err
	}

	select {
	case resp := <-o.respCh:
		if resp.Error != nil {
			return nil, fmt.Errorf("QMP error %s: %s", resp.Error.Class, resp.Error.Desc)
		}
		return resp.Return, nil
	case <-o.done:
		return nil, o.err()
	case <-time.After(executeTimeout):
		return nil, fmt.Errorf("QMP %s: timed out after %s", cmd, executeTimeout)
	}
}

func (o *QMPOwner) err() error {
	o.errMu.Lock()
	defer o.errMu.Unlock()
	if o.readErr != nil {
		return o.readErr
	}
	return fmt.Errorf("QMP: connection closed")
}

// Done returns a channel closed when the connection drops (QEMU exit) or Close
// is called. Callers watching for VM exit can select on it.
func (o *QMPOwner) Done() <-chan struct{} { return o.done }

// Close tears down the connection and stops the reader goroutine.
func (o *QMPOwner) Close() error {
	var err error
	o.closeOnce.Do(func() {
		err = o.conn.Close()
	})
	return err
}
