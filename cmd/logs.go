package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/backend"
	"github.com/Benehiko/vee/internal/journal"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/internal/vzhelper"
)

var (
	logsFollow  bool
	logsJournal bool
	logsKernel  bool
	logsLines   int
)

var logsCmd = &cobra.Command{
	Use:               "logs <name>",
	Short:             "Stream logs for a VM",
	ValidArgsFunction: completeVMNames,
	Long: `Stream logs for a VM.

By default, shows the VM process log — QEMU's qemu.log, or vz-helper.log for a
guest on Virtualization.framework. Use --journal to stream
the guest's systemd journal forwarded by systemd-journal-upload.
(vz Linux guests also write their boot console to serial.log in the VM
directory.)

Examples:
  vee logs myvm                   — QEMU process log
  vee logs myvm -f                — follow QEMU log
  vee logs myvm --journal         — guest systemd journal
  vee logs myvm --journal -f      — follow guest journal
  vee logs myvm --journal --kernel — kernel messages only`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		if logsJournal || logsKernel {
			return runJournalLogs(cmd, name)
		}
		return runProcessLog(cmd, name)
	},
}

func runProcessLog(cmd *cobra.Command, name string) error {
	// Each backend writes its VM process log under a different name: QEMU's
	// stdout/stderr goes to qemu.log, the Virtualization.framework helper's to
	// vz-helper.log.
	logName := "qemu.log"
	mgr := vm.NewManager(prov)
	if state, err := mgr.LoadState(name); err == nil && state.BackendName() == backend.VZ {
		logName = vzhelper.LogFileName
	}
	logPath := filepath.Join(prov.Config().StoragePath, name, logName)

	f, err := os.Open(logPath) //nolint:gosec // logPath is derived from vee-managed storage path and VM name.
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no log found for VM %q (has it been started?)", name)
		}
		return err
	}
	defer func() { _ = f.Close() }()

	if _, err := io.Copy(os.Stdout, f); err != nil {
		return err
	}

	if !logsFollow {
		return nil
	}

	for {
		select {
		case <-cmd.Context().Done():
			return nil
		case <-time.After(250 * time.Millisecond):
			if _, err := io.Copy(os.Stdout, f); err != nil {
				return err
			}
		}
	}
}

func runJournalLogs(cmd *cobra.Command, name string) error {
	dir := filepath.Join(prov.Config().StoragePath, name, "journal")
	return journal.Tail(cmd.Context(), dir, journal.TailOptions{
		KernelOnly: logsKernel,
		Follow:     logsFollow,
		Lines:      logsLines,
	})
}

func init() {
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "Follow log output (like tail -f)")
	logsCmd.Flags().BoolVar(&logsJournal, "journal", false, "Stream guest systemd journal (requires gaming-arch VM with journal-upload)")
	logsCmd.Flags().BoolVar(&logsKernel, "kernel", false, "Filter to kernel messages only (implies --journal)")
	logsCmd.Flags().IntVar(&logsLines, "lines", 0, "Show last N journal lines (0 = all; only applies with --journal)")
}
