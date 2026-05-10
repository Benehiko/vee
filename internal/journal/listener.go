// Package journal manages the host-side systemd-journal-remote listener that
// receives journal entries pushed by gaming VMs via systemd-journal-upload.
package journal

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

const defaultPort = 19532

// Listener runs systemd-journal-remote in HTTP receive mode, writing journal
// files to dir/<name>/ one file per incoming connection.
type Listener struct {
	vmName string
	dir    string // base output dir, e.g. ~/.vee/vms/<name>/journal
	port   int
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

// NewListener creates a Listener for the given VM. dir is the journal output
// directory (created if absent). port 0 uses the standard port 19532.
func NewListener(vmName, dir string, port int) *Listener {
	if port == 0 {
		port = defaultPort
	}
	return &Listener{vmName: vmName, dir: dir, port: port}
}

// Start launches systemd-journal-remote in the background.
// It binds to 0.0.0.0:<port> so it is reachable from bridge-networked guests.
// Returns an error if systemd-journal-remote is not found or fails to start.
func (l *Listener) Start(ctx context.Context) error {
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return fmt.Errorf("journal dir: %w", err)
	}

	bin, err := exec.LookPath("systemd-journal-remote")
	if err != nil {
		return fmt.Errorf("systemd-journal-remote not found: %w", err)
	}

	listenCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel

	// --listen-http=<addr>:<port> puts it in HTTP receive mode.
	// --output=<dir> writes one journal file per sender.
	// --split-mode=host writes per-sender files named by the sending hostname.
	l.cmd = exec.CommandContext(listenCtx, bin,
		"--listen-http=0.0.0.0:"+strconv.Itoa(l.port),
		"--output="+l.dir,
		"--split-mode=host",
	)
	l.cmd.Stdout = os.Stdout
	l.cmd.Stderr = os.Stderr

	if err := l.cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start systemd-journal-remote: %w", err)
	}

	return nil
}

// Stop terminates the listener.
func (l *Listener) Stop() {
	if l.cancel != nil {
		l.cancel()
	}
	if l.cmd != nil && l.cmd.Process != nil {
		_ = l.cmd.Wait()
	}
}

// Port returns the port the listener is bound to.
func (l *Listener) Port() int { return l.port }

// JournalFiles returns the list of journal files written to the output dir.
func (l *Listener) JournalFiles() ([]string, error) {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".journal" {
			files = append(files, filepath.Join(l.dir, e.Name()))
		}
	}
	return files, nil
}

// FreePort finds a free TCP port on all interfaces starting from defaultPort.
// Returns defaultPort if it's available, otherwise searches upward.
func FreePort() (int, error) {
	for port := defaultPort; port < defaultPort+100; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
		if err == nil {
			_ = ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free port found near %d", defaultPort)
}
