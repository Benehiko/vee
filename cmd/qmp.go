package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/qemu"
	"github.com/Benehiko/vee/internal/vm"
)

var (
	qmpArgs    string
	qmpRaw     bool
	qmpStdin   bool
	qmpTimeout time.Duration
)

var qmpCmd = &cobra.Command{
	Use:   "qmp <name> [command]",
	Short: "Send QMP commands to a running VM",
	Long: `Send QMP (QEMU Machine Protocol) commands to a running VM's control socket.

vee already speaks QMP internally; this exposes it so you don't have to script
against the raw socket by hand. The JSON "return" payload of each command is
printed.

QEMU's QMP socket only accepts one client at a time, and the vee daemon holds
that connection for every running VM (to watch for shutdown events). So when
the daemon is running, commands are routed through it. If no daemon is running,
vee connects to the VM's QMP socket directly.

Examples:
  # Query run state
  vee qmp myvm query-status

  # A command that takes arguments (--args is a JSON object)
  vee qmp myvm human-monitor-command --args '{"command-line":"info registers"}'

  # Pipe a full QMP request object on stdin (execute + arguments)
  echo '{"execute":"query-block"}' | vee qmp myvm --stdin

  # Compact single-line output, e.g. to pipe into jq
  vee qmp myvm --raw query-status | jq .status`,
	Args:              cobra.RangeArgs(1, 2),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		entry, err := findVM(name)
		if err != nil {
			return err
		}
		if !entry.State.Running || entry.State.QMPSocket == "" {
			return fmt.Errorf("VM %q is not running or has no QMP socket", name)
		}

		requests, err := buildQMPRequests(cmd, args)
		if err != nil {
			return err
		}

		exec, cleanup, err := qmpExecutor(cmd, name, entry.State.QMPSocket)
		if err != nil {
			return err
		}
		defer cleanup()

		for _, req := range requests {
			raw, execErr := exec(req.Execute, req.Arguments)
			if execErr != nil {
				return execErr
			}
			printQMPReturn(raw)
		}
		return nil
	},
}

// qmpExecutor returns a function that executes a single QMP command, choosing
// the transport automatically: route through the daemon when it is running
// (because it owns the VM's one QMP connection), otherwise dial the QMP socket
// directly. The returned cleanup closes any direct connection.
func qmpExecutor(cmd *cobra.Command, name, socket string) (
	exec func(execute string, args map[string]any) (json.RawMessage, error),
	cleanup func(),
	err error,
) {
	mgr := vm.NewManager(prov)

	// Probe the daemon by running the first request through it. We can't cheaply
	// "check" without a request, so route every command through the daemon and
	// only fall back if the daemon is unreachable.
	daemonExec := func(execute string, args map[string]any) (json.RawMessage, error) {
		raw, reachable, dErr := mgr.QMPViaDaemon(cmd.Context(), name, execute, args)
		if !reachable {
			return nil, errDaemonUnreachable
		}
		return raw, dErr
	}

	// Try a lightweight command against the daemon to decide the transport up
	// front, so multi-command (--stdin) invocations don't split across
	// transports mid-run.
	if _, reachable, _ := mgr.QMPViaDaemon(cmd.Context(), name, "query-status", nil); reachable {
		return daemonExec, func() {}, nil
	}

	// No daemon — dial the QMP socket directly.
	client, dialErr := qemu.NewQMPClient(cmd.Context(), socket, qmpTimeout)
	if dialErr != nil {
		return nil, func() {}, dialErr
	}
	return client.Execute, func() { _ = client.Close() }, nil
}

var errDaemonUnreachable = fmt.Errorf("vee daemon became unreachable")

// qmpCLIRequest mirrors the on-the-wire QMP request shape for stdin parsing.
type qmpCLIRequest struct {
	Execute   string         `json:"execute"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// buildQMPRequests assembles the command(s) to run from the positional command
// argument (+ --args) or from stdin (--stdin). Stdin may contain one QMP
// request object per line, allowing several commands in one invocation.
func buildQMPRequests(cmd *cobra.Command, args []string) ([]qmpCLIRequest, error) {
	if qmpStdin {
		if len(args) > 1 {
			return nil, fmt.Errorf("cannot combine a positional command with --stdin")
		}
		return parseStdinRequests(cmd.InOrStdin())
	}

	if len(args) < 2 {
		return nil, fmt.Errorf("a QMP command is required (or use --stdin)")
	}

	req := qmpCLIRequest{Execute: args[1]}
	if strings.TrimSpace(qmpArgs) != "" {
		if err := json.Unmarshal([]byte(qmpArgs), &req.Arguments); err != nil {
			return nil, fmt.Errorf("parse --args as JSON object: %w", err)
		}
	}
	return []qmpCLIRequest{req}, nil
}

func parseStdinRequests(r io.Reader) ([]qmpCLIRequest, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	var reqs []qmpCLIRequest
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var req qmpCLIRequest
		if decErr := dec.Decode(&req); decErr != nil {
			if decErr == io.EOF {
				break
			}
			return nil, fmt.Errorf("parse stdin QMP request: %w", decErr)
		}
		if req.Execute == "" {
			return nil, fmt.Errorf("stdin QMP request missing \"execute\" field")
		}
		reqs = append(reqs, req)
	}
	if len(reqs) == 0 {
		return nil, fmt.Errorf("no QMP requests found on stdin")
	}
	return reqs, nil
}

// printQMPReturn writes the command's return payload to stdout. By default the
// JSON is pretty-printed; --raw emits it verbatim (compact, one line) so output
// can be piped into jq or another QMP tool.
func printQMPReturn(raw json.RawMessage) {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if qmpRaw {
		fmt.Println(string(raw))
		return
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		// Not indentable (shouldn't happen for valid QMP) — fall back to raw.
		fmt.Println(string(raw))
		return
	}
	fmt.Println(buf.String())
}

func init() {
	qmpCmd.Flags().StringVar(&qmpArgs, "args", "",
		"JSON object passed as the command's QMP arguments")
	qmpCmd.Flags().BoolVar(&qmpRaw, "raw", false,
		"Emit compact single-line JSON instead of pretty-printed output")
	qmpCmd.Flags().BoolVar(&qmpStdin, "stdin", false,
		"Read one or more QMP request objects from stdin instead of positional args")
	qmpCmd.Flags().DurationVar(&qmpTimeout, "timeout", 3*time.Second,
		"How long to wait for the QMP socket to accept a connection")
	rootCmd.AddCommand(qmpCmd)
}
