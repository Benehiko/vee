package media

import (
	"slices"
	"strings"
	"testing"

	"github.com/Benehiko/vee/internal/vm"
)

func TestPlan_HostDir(t *testing.T) {
	s := Source{
		Kind:      KindHostDir,
		GuestPath: "/media/photos",
		HostDir:   "/mnt/4TB/photos",
	}
	patch, prompts, err := s.Plan(Ubuntu, nil)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if len(prompts) != 0 {
		t.Fatalf("host-dir should not produce prompts, got %d", len(prompts))
	}
	if len(patch.VirtiofsMounts) != 1 {
		t.Fatalf("expected 1 virtiofs mount, got %d", len(patch.VirtiofsMounts))
	}
	m := patch.VirtiofsMounts[0]
	if m.SharedDir != "/mnt/4TB/photos" || m.Tag != "media-photos" {
		t.Errorf("unexpected mount: %+v", m)
	}
	if len(patch.RunCmds) < 2 {
		t.Errorf("expected mount runcmds, got %v", patch.RunCmds)
	}
	if !hasSubstring(patch.RunCmds, "mount -t virtiofs media-photos /media/photos") {
		t.Errorf("missing virtiofs mount cmd: %v", patch.RunCmds)
	}
}

func TestPlan_HostDir_ReadOnly(t *testing.T) {
	s := Source{
		Kind:      KindHostDir,
		GuestPath: "/media/ro",
		HostDir:   "/srv/ro",
		ReadOnly:  true,
	}
	patch, _, err := s.Plan(Ubuntu, nil)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if !hasSubstring(patch.RunCmds, "-o ro,defaults") {
		t.Errorf("expected ro mount opts: %v", patch.RunCmds)
	}
}

func TestPlan_NFS(t *testing.T) {
	s := Source{
		Kind:      KindNFS,
		GuestPath: "/media/movies",
		NFS: &NFSSource{
			Server: "truenas.lan",
			Export: "/mnt/Data/Movies",
		},
	}
	for _, distro := range []Distro{Ubuntu, Arch, Fedora} {
		t.Run(string(distro), func(t *testing.T) {
			patch, prompts, err := s.Plan(distro, nil)
			if err != nil {
				t.Fatalf("Plan returned error: %v", err)
			}
			if len(prompts) != 0 {
				t.Fatalf("nfs should not produce prompts, got %d", len(prompts))
			}
			wantPkg := "nfs-common"
			if distro != Ubuntu {
				wantPkg = "nfs-utils"
			}
			if !containsStr(patch.Packages, wantPkg) {
				t.Errorf("missing pkg %q in %v", wantPkg, patch.Packages)
			}
			if len(patch.WriteFiles) != 2 {
				t.Fatalf("expected mount + automount unit files, got %d", len(patch.WriteFiles))
			}
			var hasMount, hasAutomount bool
			for _, wf := range patch.WriteFiles {
				if strings.HasSuffix(wf.Path, "media-movies.mount") {
					hasMount = true
					if !strings.Contains(wf.Content, "What=truenas.lan:/mnt/Data/Movies") {
						t.Errorf("mount unit missing What: %s", wf.Content)
					}
					if !strings.Contains(wf.Content, "vers=4.2") {
						t.Errorf("mount unit missing default vers=4.2: %s", wf.Content)
					}
					// The network dependency belongs on the .mount unit.
					if !strings.Contains(wf.Content, "network-online.target") {
						t.Errorf("mount unit missing network-online dependency: %s", wf.Content)
					}
				}
				if strings.HasSuffix(wf.Path, "media-movies.automount") {
					hasAutomount = true
					// Regression: the automount point must NOT order after
					// network-online.target — automounts are Before=local-fs.target
					// while network-online.target is After=local-fs.target, so this
					// forms a cycle that systemd breaks by dropping the automount at
					// boot, leaving the share silently unmounted.
					if strings.Contains(wf.Content, "network-online.target") {
						t.Errorf("automount unit must not depend on network-online.target (ordering cycle): %s", wf.Content)
					}
				}
			}
			if !hasMount || !hasAutomount {
				t.Errorf("expected both .mount and .automount files, got %+v", patch.WriteFiles)
			}
			if !hasSubstring(patch.RunCmds, "systemctl enable --now media-movies.automount") {
				t.Errorf("missing enable cmd: %v", patch.RunCmds)
			}
		})
	}
}

func TestPlan_NFS_ReadOnly(t *testing.T) {
	s := Source{
		Kind:      KindNFS,
		GuestPath: "/media/ro",
		ReadOnly:  true,
		NFS: &NFSSource{
			Server: "nas.lan",
			Export: "/export/ro",
		},
	}
	patch, _, err := s.Plan(Ubuntu, nil)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	var found bool
	for _, wf := range patch.WriteFiles {
		if strings.HasSuffix(wf.Path, ".mount") {
			if strings.Contains(wf.Content, ",ro") || strings.Contains(wf.Content, "ro,") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("ro option missing from mount unit")
	}
}

func TestPlan_SMB_RequiresPassword(t *testing.T) {
	s := Source{
		Kind:      KindSMB,
		GuestPath: "/media/music",
		SMB: &SMBSource{
			Server:   "nas.lan",
			Share:    "Music",
			Username: "alice",
		},
	}
	_, prompts, err := s.Plan(Ubuntu, nil)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if len(prompts) != 1 {
		t.Fatalf("expected 1 prompt without secret, got %d", len(prompts))
	}
	if prompts[0].Key != "smb-password:nas.lan/Music" {
		t.Errorf("unexpected prompt key: %q", prompts[0].Key)
	}
	if !prompts[0].Secret {
		t.Errorf("password prompt should be Secret=true")
	}
}

func TestPlan_SMB_WithSecret(t *testing.T) {
	s := Source{
		Kind:      KindSMB,
		GuestPath: "/media/music",
		SMB: &SMBSource{
			Server:   "nas.lan",
			Share:    "Music",
			Username: "alice",
			Domain:   "WORKGROUP",
		},
	}
	patch, prompts, err := s.Plan(Ubuntu, map[string]string{
		"smb-password:nas.lan/Music": "s3cret",
	})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if len(prompts) != 0 {
		t.Fatalf("expected no prompts when secret supplied, got %d", len(prompts))
	}
	if !containsStr(patch.Packages, "cifs-utils") {
		t.Errorf("missing cifs-utils pkg: %v", patch.Packages)
	}
	var credsFile *vm.CloudInitWriteFile
	for i := range patch.WriteFiles {
		wf := &patch.WriteFiles[i]
		if strings.HasPrefix(wf.Path, "/etc/cifs-credentials") {
			credsFile = wf
		}
	}
	if credsFile == nil {
		t.Fatalf("missing cifs credentials file")
	}
	if credsFile.Permissions != "0600" {
		t.Errorf("creds file must be 0600, got %q", credsFile.Permissions)
	}
	if !strings.Contains(credsFile.Content, "username=alice") ||
		!strings.Contains(credsFile.Content, "password=s3cret") ||
		!strings.Contains(credsFile.Content, "domain=WORKGROUP") {
		t.Errorf("creds content incomplete: %q", credsFile.Content)
	}
}

func TestPlan_Block(t *testing.T) {
	s := Source{
		Kind:      KindBlock,
		GuestPath: "/media/scratch",
		Block: &BlockSource{
			DevPath: "/dev/disk/by-id/ata-Samsung_SSD_870_QVO_4TB_S5STNF0R807890W",
			FSType:  "ext4",
		},
	}
	patch, prompts, err := s.Plan(Ubuntu, nil)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if len(prompts) != 0 {
		t.Fatalf("block should not produce prompts")
	}
	if len(patch.Disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(patch.Disks))
	}
	d := patch.Disks[0]
	if !d.Passthrough {
		t.Errorf("expected Passthrough=true")
	}
	if d.Serial == "" {
		t.Errorf("expected auto-derived Serial")
	}
	if len(d.Serial) > 20 {
		t.Errorf("Serial exceeds 20 chars: %q", d.Serial)
	}
	if !hasSubstring(patch.RunCmds, "/dev/disk/by-id/virtio-"+d.Serial) {
		t.Errorf("expected guest device path in runcmds: %v", patch.RunCmds)
	}
}

func TestPlan_Block_NoFSType_SkipsMount(t *testing.T) {
	s := Source{
		Kind:      KindBlock,
		GuestPath: "/media/raw",
		Block: &BlockSource{
			DevPath: "/dev/disk/by-id/nvme-WD_BLACK",
		},
	}
	patch, _, err := s.Plan(Ubuntu, nil)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if len(patch.RunCmds) != 0 {
		t.Errorf("no FSType → no mount runcmds, got %v", patch.RunCmds)
	}
}

func TestPlan_USB_VendorProduct(t *testing.T) {
	s := Source{
		Kind:      KindUSB,
		GuestPath: "/media/usb",
		USB: &USBSource{
			VendorID:  "0951",
			ProductID: "1666",
		},
	}
	patch, _, err := s.Plan(Ubuntu, nil)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if len(patch.ExtraDevices) != 1 {
		t.Fatalf("expected 1 extra device, got %d", len(patch.ExtraDevices))
	}
	if patch.ExtraDevices[0] != "usb-host,vendorid=0x0951,productid=0x1666" {
		t.Errorf("unexpected device line: %q", patch.ExtraDevices[0])
	}
}

func TestPlan_USB_BusAddr(t *testing.T) {
	s := Source{
		Kind:      KindUSB,
		GuestPath: "/media/usb",
		USB: &USBSource{
			HostBus:  "1",
			HostAddr: "5",
		},
	}
	patch, _, err := s.Plan(Ubuntu, nil)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if patch.ExtraDevices[0] != "usb-host,hostbus=1,hostaddr=5" {
		t.Errorf("unexpected device line: %q", patch.ExtraDevices[0])
	}
}

func TestPlan_Errors(t *testing.T) {
	cases := []struct {
		name string
		src  Source
	}{
		{"missing guest path", Source{Kind: KindHostDir, HostDir: "/x"}},
		{"relative guest path", Source{Kind: KindHostDir, GuestPath: "rel", HostDir: "/x"}},
		{"unknown kind", Source{Kind: "bogus", GuestPath: "/x"}},
		{"host-dir no HostDir", Source{Kind: KindHostDir, GuestPath: "/x"}},
		{"nfs no server", Source{Kind: KindNFS, GuestPath: "/x", NFS: &NFSSource{Export: "/y"}}},
		{"smb no share", Source{Kind: KindSMB, GuestPath: "/x", SMB: &SMBSource{Server: "n"}}},
		{"block no DevPath", Source{Kind: KindBlock, GuestPath: "/x", Block: &BlockSource{}}},
		{"usb no ids", Source{Kind: KindUSB, GuestPath: "/x", USB: &USBSource{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := c.src.Plan(Ubuntu, nil)
			if err == nil {
				t.Errorf("expected error for %s", c.name)
			}
		})
	}
}

func TestPatch_Merge(t *testing.T) {
	a := Patch{
		Packages: []string{"a"},
		RunCmds:  []string{"cmd1"},
	}
	a.Merge(Patch{
		Packages: []string{"b"},
		RunCmds:  []string{"cmd2"},
	})
	if !containsStr(a.Packages, "a") || !containsStr(a.Packages, "b") {
		t.Errorf("merge lost packages: %v", a.Packages)
	}
	if len(a.RunCmds) != 2 {
		t.Errorf("merge lost runcmds: %v", a.RunCmds)
	}
}

func hasSubstring(cmds []string, sub string) bool {
	for _, c := range cmds {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func containsStr(ss []string, want string) bool {
	return slices.Contains(ss, want)
}
