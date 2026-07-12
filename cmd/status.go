package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/qemu"
	"github.com/Benehiko/vee/internal/vm"
)

var (
	statusWatch    bool
	statusRunCheck bool
)

var statusCmd = &cobra.Command{
	Use:               "status <name>",
	Short:             "Show detailed status of a VM",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		entry, err := findVM(name)
		if err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

		printRow := func(k, v string) {
			_, _ = fmt.Fprintf(w, "%s\t%s\n", k, v)
		}

		printRow("name", entry.Config.Name)
		printRow("template", entry.Config.Template)
		printRow("memory", entry.Config.Memory)
		printRow("cpus", fmt.Sprintf("%d", entry.Config.CPUs))

		if entry.State.Running {
			status := "running"
			if entry.State.InstallState == vm.InstallStatePending {
				status = "installing"
			}
			printRow("status", status)
			printRow("pid", fmt.Sprintf("%d", entry.State.PID))
			if entry.State.StartedAt != nil && !entry.State.StartedAt.IsZero() {
				uptime := time.Since(*entry.State.StartedAt).Truncate(time.Second)
				printRow("uptime", uptime.String())
			}
			if entry.State.BootPhase != "" {
				printRow("phase", formatBootPhase(entry.State))
			}
			if entry.State.LastPanicLine != "" {
				printRow("panic", entry.State.LastPanicLine)
			}
			if entry.State.SPICEPort > 0 {
				printRow("spice", fmt.Sprintf(":%d", entry.State.SPICEPort))
			}
			if entry.State.SSHPort > 0 {
				printRow("ssh", fmt.Sprintf("127.0.0.1:%d", entry.State.SSHPort))
			}
		} else {
			printRow("status", "stopped")
			if entry.State.LastPanicLine != "" {
				printRow("last panic", entry.State.LastPanicLine)
			}
		}

		_ = w.Flush()

		// Cloud-init progress — shown when install is pending and VM is reachable via SSH.
		if entry.State.Running && entry.State.InstallState == vm.InstallStatePending {
			fmt.Println()
			if statusWatch {
				return watchCloudInitProgress(cmd.Context(), entry.Config, entry.State)
			}
			printCloudInitProgress(cmd.Context(), entry.Config, entry.State)
			return nil
		}

		// QGA section — only if VM is running and has a QGA socket.
		if entry.State.Running && entry.State.QGASocket != "" {
			fmt.Println()
			client, closeClient, qgaErr := openQGAClient(cmd.Context(), entry.State.QGASocket, 3*time.Second)
			if qgaErr != nil {
				fmt.Printf("guest-agent: unavailable (%v)\n", qgaErr)
			} else {
				defer closeClient()

				if pingErr := client.GuestPing(); pingErr != nil {
					fmt.Printf("guest-agent: not responding (%v)\n", pingErr)
				} else {
					fmt.Println("guest-agent: connected")
					fmt.Println()

					w2 := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
					_, _ = fmt.Fprintln(w2, "INTERFACE\tIP\tMAC")

					ifaces, ifErr := client.GuestNetworkGetInterfaces()
					if ifErr == nil {
						for _, iface := range ifaces {
							if iface.Name == "lo" {
								continue
							}
							var ips []string
							for _, addr := range iface.IPAddresses {
								if addr.IPAddressType == "ipv4" {
									ips = append(ips, fmt.Sprintf("%s/%d", addr.IPAddress, addr.Prefix))
								}
							}
							ipStr := strings.Join(ips, ", ")
							if ipStr == "" {
								ipStr = "-"
							}
							_, _ = fmt.Fprintf(w2, "%s\t%s\t%s\n", iface.Name, ipStr, iface.HardwareAddress)
						}
					}
					_ = w2.Flush()

					hostname, _, _, herr := client.RunCommand("/bin/hostname", nil)
					if herr == nil {
						fmt.Printf("\nhostname: %s", hostname)
					}

					osRelease, _, _, oerr := client.RunCommand("/bin/sh", []string{"-c", "grep PRETTY_NAME /etc/os-release | cut -d= -f2 | tr -d '\"'"})
					if oerr == nil {
						fmt.Printf("os:       %s", osRelease)
					}

					uptime, _, _, uerr := client.RunCommand("/usr/bin/uptime", []string{"-p"})
					if uerr == nil {
						fmt.Printf("uptime:   %s", uptime)
					}
				}
			}
		}

		// Health check section — run fresh if --check, otherwise show persisted results.
		if statusRunCheck && entry.State.Running {
			mgr := vm.NewManager(prov)
			if _, runErr := mgr.RunHealthCheck(cmd.Context(), name); runErr != nil {
				fmt.Printf("\nhealth check error: %v\n", runErr)
			} else {
				// Reload state so we display the freshly persisted results.
				if refreshed, refreshErr := findVM(name); refreshErr == nil {
					entry = refreshed
				}
			}
		}

		fmt.Println()
		if len(entry.State.PostInstallChecks) > 0 {
			if entry.State.PostInstallCheckedAt != nil {
				fmt.Printf("health checks (as of %s):\n\n",
					entry.State.PostInstallCheckedAt.Local().Format("2006-01-02 15:04:05"))
			} else {
				fmt.Println("health checks:")
				fmt.Println()
			}
			printHealthChecks(entry.State.PostInstallChecks)
		} else if entry.State.InstallState == vm.InstallStateReady {
			fmt.Println("health checks: not yet run — use 'vee check <name>'")
		}

		return nil
	},
}

// formatBootPhase renders the BootPhase row, including the dwell time in the
// current phase when PhaseStartedAt is populated.
func formatBootPhase(state *vm.VMState) string {
	if state.PhaseStartedAt == nil || state.PhaseStartedAt.IsZero() {
		return state.BootPhase
	}
	dwell := time.Since(*state.PhaseStartedAt).Truncate(time.Second)
	return fmt.Sprintf("%s — %s", state.BootPhase, dwell)
}

// cloudInitStatus mirrors the relevant fields of /run/cloud-init/status.json.
type cloudInitStatus struct {
	V1 struct {
		Modules map[string]struct {
			Errors []string `json:"errors"`
			Start  *float64 `json:"start"`
			End    *float64 `json:"end"`
		} `json:"stage-module-results"`
		Stage  string   `json:"stage"`
		Errors []string `json:"errors"`
	} `json:"v1"`
}

func sshRunQuiet(ctx context.Context, cfg *vm.VMConfig, state *vm.VMState, command string) (string, error) {
	ip, err := resolveVMIP(ctx, cfg, state)
	if err != nil {
		return "", err
	}
	home, _ := os.UserHomeDir()
	identity := home + "/.vee/ssh/id_ed25519"

	user := cfg.SSHUser
	if user == "" && cfg.CloudInit != nil {
		user = cfg.CloudInit.DefaultUser
	}
	if user == "" {
		user = "root"
	}

	args := []string{
		"-i", identity,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=3",
		"-o", "LogLevel=ERROR",
		user + "@" + ip,
		command,
	}
	//nolint:gosec,noctx // ssh subprocess from vetted VM config; short guest-status query, no ctx in this call chain.
	out, err := exec.Command("ssh", args...).Output()
	return strings.TrimSpace(string(out)), err
}

func resolveVMIP(ctx context.Context, cfg *vm.VMConfig, state *vm.VMState) (string, error) {
	if cfg.NIC.MAC != "" {
		if ip, err := vm.ResolveIPFromMAC(cfg.NIC.MAC); err == nil {
			return ip, nil
		}
	}
	if state.QGASocket != "" {
		return vm.ResolveIPFromQGA(ctx, state.QGASocket)
	}
	return "", fmt.Errorf("cannot resolve VM IP")
}

func fetchCloudInitStatus(ctx context.Context, cfg *vm.VMConfig, state *vm.VMState) (*cloudInitStatus, error) {
	// Prefer the guest agent — it works before networking is up and before
	// cloud-init has installed any host SSH keys. Fall back to SSH for guests
	// without QGA (GuestAgent=false in the VM config).
	if state.QGASocket != "" {
		if s, err := fetchCloudInitStatusViaQGA(ctx, state); err == nil {
			return s, nil
		}
	}
	out, err := sshRunQuiet(ctx, cfg, state, "cat /run/cloud-init/status.json 2>/dev/null || echo '{}'")
	if err != nil {
		return nil, err
	}
	var s cloudInitStatus
	if jsonErr := json.Unmarshal([]byte(out), &s); jsonErr != nil {
		return nil, jsonErr
	}
	return &s, nil
}

func fetchCloudInitStatusViaQGA(ctx context.Context, state *vm.VMState) (*cloudInitStatus, error) {
	client, err := qemu.NewQGAClient(ctx, state.QGASocket, 3*time.Second)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	out, _, _, err := client.RunCommand("/bin/cat", []string{"/run/cloud-init/status.json"})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		out = "{}"
	}
	var s cloudInitStatus
	if jsonErr := json.Unmarshal([]byte(out), &s); jsonErr != nil {
		return nil, jsonErr
	}
	return &s, nil
}

func printCloudInitProgress(ctx context.Context, cfg *vm.VMConfig, state *vm.VMState) {
	s, err := fetchCloudInitStatus(ctx, cfg, state)
	if err != nil {
		fmt.Printf("cloud-init: unreachable (%v)\n", err)
		fmt.Println("  VM may still be booting — check the SPICE console")
		return
	}

	stage := s.V1.Stage
	if stage == "" {
		stage = "booting"
	}
	fmt.Printf("cloud-init stage: %s\n", stage)

	done, total := 0, 0
	for _, m := range s.V1.Modules {
		total++
		if m.End != nil {
			done++
		}
	}
	if total > 0 {
		width := 40
		filled := width * done / total
		bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
		fmt.Printf("modules: [%s] %d/%d\n", bar, done, total)
	}
	if len(s.V1.Errors) > 0 {
		fmt.Printf("errors: %s\n", strings.Join(s.V1.Errors, ", "))
	}
}

func watchCloudInitProgress(ctx context.Context, cfg *vm.VMConfig, state *vm.VMState) error {
	fmt.Println("watching cloud-init progress (Ctrl+C to stop)…")
	fmt.Println()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		// Clear previous output with ANSI escape (4 lines up).
		fmt.Print("\033[4A\033[J")
		printCloudInitProgress(ctx, cfg, state)

		// Check if done.
		out, err := sshRunQuiet(ctx, cfg, state, "cloud-init status 2>/dev/null")
		if err == nil && strings.Contains(out, "done") {
			fmt.Println("\ncloud-init: complete")
			return nil
		}

		<-ticker.C
	}
}

func init() {
	statusCmd.Flags().BoolVarP(&statusWatch, "watch", "w", false, "Watch cloud-init progress live (during install)")
	statusCmd.Flags().BoolVar(&statusRunCheck, "check", false, "Run health checks now and show fresh results")
	rootCmd.AddCommand(statusCmd)
}
