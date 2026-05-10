package backup

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// SSHConn holds the parameters needed to run commands on a guest.
type SSHConn struct {
	User     string
	Host     string
	Port     int
	Identity string
}

func (c SSHConn) args(remote string) []string {
	var args []string
	args = append(args, "-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes")
	if c.Port != 22 {
		args = append(args, "-p", fmt.Sprintf("%d", c.Port))
	}
	if c.Identity != "" {
		args = append(args, "-i", c.Identity)
	}
	dest := c.Host
	if c.User != "" {
		dest = c.User + "@" + c.Host
	}
	return append(args, dest, remote)
}

// DirEntry is a node in the guest filesystem tree.
type DirEntry struct {
	Path     string
	Name     string
	Depth    int
	Children []*DirEntry
}

// EnumerateHome lists all directories under the guest home dir via SSH find.
// Returns a flat list sorted by path.
func EnumerateHome(conn SSHConn) ([]*DirEntry, error) {
	cmd := "find ~ -mindepth 1 -type d ! -path '*/proc/*' ! -path '*/sys/*' ! -path '*/.git/*' 2>/dev/null | sort"
	c := exec.Command("ssh", conn.args(cmd)...)
	var stderr strings.Builder
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("enumerate guest dirs: %w\nstderr: %s", err, stderr.String())
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var entries []*DirEntry
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		entries = append(entries, &DirEntry{
			Path:  line,
			Name:  filepath.Base(line),
			Depth: strings.Count(line, "/"),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})
	return entries, nil
}

// RunRsync executes rsync for a single guest path into dest over SSH.
// Progress output is written to stdout via the exec.Cmd.
func RunRsync(conn SSHConn, guestPath, localDest string) error {
	sshCmd := "ssh -o StrictHostKeyChecking=no -o BatchMode=yes"
	if conn.Port != 22 {
		sshCmd += fmt.Sprintf(" -p %d", conn.Port)
	}
	if conn.Identity != "" {
		sshCmd += " -i " + conn.Identity
	}

	src := conn.Host
	if conn.User != "" {
		src = conn.User + "@" + conn.Host
	}

	rsync, err := exec.LookPath("rsync")
	if err != nil {
		return fmt.Errorf("rsync not found: %w", err)
	}

	// Trailing slash on source → copy contents into dest, not a nested subdir.
	remoteSrc := src + ":" + strings.TrimRight(guestPath, "/") + "/"

	args := []string{
		"-az", "--info=progress2", "--human-readable",
		"--relative",
		"-e", sshCmd,
		remoteSrc,
		localDest + "/",
	}

	cmd := exec.Command(rsync, args...)
	cmd.Stdout = nil // caller sets these
	cmd.Stderr = nil
	return cmd.Run()
}

// RunRsyncCmd returns a prepared *exec.Cmd for a single rsync transfer.
// The caller sets Stdout/Stderr before calling Run().
func RunRsyncCmd(conn SSHConn, guestPath, localDest string) (*exec.Cmd, error) {
	sshCmd := "ssh -o StrictHostKeyChecking=no -o BatchMode=yes"
	if conn.Port != 22 {
		sshCmd += fmt.Sprintf(" -p %d", conn.Port)
	}
	if conn.Identity != "" {
		sshCmd += " -i " + conn.Identity
	}

	src := conn.Host
	if conn.User != "" {
		src = conn.User + "@" + conn.Host
	}

	rsync, err := exec.LookPath("rsync")
	if err != nil {
		return nil, fmt.Errorf("rsync not found: %w", err)
	}

	remoteSrc := src + ":" + strings.TrimRight(guestPath, "/") + "/"

	args := []string{
		"-az", "--info=progress2", "--human-readable",
		"-e", sshCmd,
		remoteSrc,
		localDest + "/",
	}

	return exec.Command(rsync, args...), nil
}
