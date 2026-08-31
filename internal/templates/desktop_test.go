package templates

import (
	"slices"
	"strings"
	"testing"

	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/vm"
)

func writeFilePaths(files []vm.CloudInitWriteFile) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	return paths
}

func TestDesktopRunCmdsProvisionGUIInBand(t *testing.T) {
	// The GUI must come up on the first boot: graphical.target is isolated as
	// the final runcmd, after GDM is enabled — otherwise the running system
	// stays on multi-user.target until a manual power-cycle.
	for _, distro := range []string{images.DistroFedora, images.DistroUbuntu} {
		t.Run(distro, func(t *testing.T) {
			runCmds, _ := desktopRunCmds(distro, "tester")
			if len(runCmds) == 0 {
				t.Fatal("no runcmds")
			}
			last := runCmds[len(runCmds)-1]
			if last != "systemctl --no-block isolate graphical.target" {
				t.Errorf("last runcmd = %q, want the graphical.target isolate", last)
			}

			// The first-boot banner must precede the long desktop install so
			// the VM window explains itself, and must be gone before GDM
			// takes over.
			if runCmds[0] != desktopTTYBanner {
				t.Errorf("first runcmd = %q, want the tty banner", runCmds[0])
			}
			if !slices.Contains(runCmds, "rm -f "+desktopIssuePath) {
				t.Error("runcmds never remove the first-boot issue banner")
			}

			installIdx, dconfIdx := -1, -1
			for i, cmd := range runCmds {
				if strings.Contains(cmd, "install") && installIdx == -1 {
					installIdx = i
				}
				if cmd == "dconf update" {
					dconfIdx = i
				}
				// The pre-seeded gdm3 custom.conf trips dpkg's conffile
				// prompt; without force-confold the noninteractive install
				// aborts (no GDM, no sshd).
				if strings.Contains(cmd, "apt-get install") && !strings.Contains(cmd, "-o Dpkg::Options::=--force-confold") {
					t.Errorf("apt-get install misses --force-confold: %q", cmd)
				}
			}
			if dconfIdx == -1 {
				t.Error("runcmds miss `dconf update`")
			}
			if installIdx == -1 || dconfIdx < installIdx {
				t.Errorf("`dconf update` at %d must run after the desktop install at %d (the dconf binary arrives with GNOME)", dconfIdx, installIdx)
			}
		})
	}
}

func TestDesktopWriteFilesDisableScreenLock(t *testing.T) {
	// An unattended autologin session must never lock: locked sessions show
	// up as LockedHint=yes and every screenshot is a black lock screen.
	for _, distro := range []string{images.DistroFedora, images.DistroUbuntu} {
		t.Run(distro, func(t *testing.T) {
			_, files := desktopRunCmds(distro, "tester")
			paths := writeFilePaths(files)
			for _, want := range []string{
				"/etc/dconf/profile/user",
				"/etc/dconf/db/local.d/00-vee-desktop",
				"/etc/dconf/db/local.d/locks/00-vee-desktop",
				desktopIssuePath,
			} {
				if !slices.Contains(paths, want) {
					t.Errorf("write_files miss %s (got %v)", want, paths)
				}
			}
			for _, f := range files {
				if f.Path != "/etc/dconf/db/local.d/00-vee-desktop" {
					continue
				}
				for _, key := range []string{"lock-enabled=false", "idle-delay=uint32 0", "disable-lock-screen=true"} {
					if !strings.Contains(f.Content, key) {
						t.Errorf("dconf keyfile misses %q", key)
					}
				}
			}
		})
	}
}

func TestDesktopWriteFilesGDMAutologin(t *testing.T) {
	tests := []struct {
		distro string
		path   string
	}{
		{images.DistroFedora, "/etc/gdm/custom.conf"},
		{images.DistroUbuntu, "/etc/gdm3/custom.conf"},
	}
	for _, tt := range tests {
		t.Run(tt.distro, func(t *testing.T) {
			_, files := desktopRunCmds(tt.distro, "tester")
			found := false
			for _, f := range files {
				if f.Path != tt.path {
					continue
				}
				found = true
				if !strings.Contains(f.Content, "AutomaticLogin=tester") {
					t.Errorf("%s misses autologin user: %q", tt.path, f.Content)
				}
			}
			if !found {
				t.Errorf("write_files miss %s", tt.path)
			}
		})
	}
}
