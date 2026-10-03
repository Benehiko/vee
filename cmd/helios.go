package cmd

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/vm"
)

var (
	heliosWait        bool
	heliosWaitTimeout time.Duration
	heliosVerifyTime  time.Duration
)

var heliosCmd = &cobra.Command{
	Use:   "helios",
	Short: "Inspect and verify WinBoat Helios GPU guests",
	Long: `Commands for Windows guests created with --gpu-mode=helios, which run the
WinBoat Helios vGPU driver (D3D11/D3D12, Vulkan, OpenGL over virtio-gpu +
Venus). Helios is pre-release; see docs/windows-helios.md.`,
}

var heliosStatusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Show the guest's Helios driver provisioning state",
	Long: `Reads %ProgramData%\Helios\provisioning-status.json in the guest:
not-started, waiting, test-signing-restart-required, driver-restart-required,
finished or failed. The guest reboots itself through the restart-required
states (vee's VeeHeliosWatch task drives them).

With --wait, polls until the state is finished or failed — retrying through
the reboots — and exits non-zero on failed or timeout. Use it to gate test
runs on a freshly created VM:

  vee start wintest && vee helios status wintest --wait && vee helios verify wintest`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		mgr := vm.NewManager(prov)
		var s vm.HeliosStatus
		var err error
		if heliosWait {
			s, err = mgr.WaitHelios(cmd.Context(), name, heliosWaitTimeout, func(s vm.HeliosStatus) {
				fmt.Printf("%s  %s\n", time.Now().Format("15:04:05"), s.Status)
			})
			if err != nil {
				return err
			}
		} else {
			s, err = mgr.ReadHeliosStatus(cmd.Context(), name)
			if err != nil {
				return err
			}
			fmt.Println(s.Status)
		}
		if s.Message != "" {
			fmt.Println(s.Message)
		}
		if s.Status == "failed" {
			return fmt.Errorf("helios provisioning failed on %q (guest logs: C:\\vee-helios.log, C:\\ProgramData\\vee\\helios\\setup.log)", name)
		}
		return nil
	},
}

var heliosVerifyCmd = &cobra.Command{
	Use:   "verify <name>",
	Short: "Run Helios' D3D11/D3D12/Vulkan/OpenGL/OpenCL smoke tests in the guest",
	Long: `Runs Helios' own Verify-Helios.ps1 -RunSmokeTests: it checks the installed
driver files and registrations, then creates Vulkan instances, D3D11 and D3D12
devices and WGL contexts in x64 and x86 processes, clears/reads back textures,
and runs an OpenCL kernel.

The probes refuse to run in session 0 (where SSH lives), so vee runs them as
an interactive scheduled task in the guest's auto-logged-on desktop session
and relays the output. Exits non-zero if any probe fails. A pass does not
establish presentation correctness or full conformance.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		log, err := vm.NewManager(prov).VerifyHelios(cmd.Context(), name, heliosVerifyTime)
		if log != "" {
			fmt.Print(log)
		}
		if err != nil {
			if errors.Is(err, vm.ErrHeliosVerifyFailed) {
				fmt.Fprintln(os.Stderr, "Helios smoke tests FAILED")
			}
			return err
		}
		fmt.Println("Helios smoke tests passed")
		return nil
	},
}

func init() {
	heliosStatusCmd.Flags().BoolVar(&heliosWait, "wait", false, "Poll until provisioning is finished or failed (retries through guest reboots)")
	heliosStatusCmd.Flags().DurationVar(&heliosWaitTimeout, "timeout", 30*time.Minute, "Give up waiting after this long (with --wait)")
	heliosVerifyCmd.Flags().DurationVar(&heliosVerifyTime, "timeout", 15*time.Minute, "Give up on the smoke tests after this long (each probe can take 1-2 minutes)")
	heliosCmd.AddCommand(heliosStatusCmd, heliosVerifyCmd)
	rootCmd.AddCommand(heliosCmd)
}
