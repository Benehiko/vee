package backup

import (
	"bufio"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// SSHConn holds the parameters needed to run commands on a guest.
type SSHConn struct {
	User     string
	Host     string
	Port     int
	Identity string
	// Password is only used transiently during first-connect key injection.
	// It is never persisted.
	Password string
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

func pathToEntry(p string) *DirEntry {
	name := p
	if slash := strings.LastIndex(p, "/"); slash >= 0 {
		name = p[slash+1:]
	}
	return &DirEntry{
		Path:  p,
		Name:  name,
		Depth: strings.Count(p, "/"),
	}
}

const findCmd = "find ~ -mindepth 1 -type d ! -path '*/proc/*' ! -path '*/sys/*' ! -path '*/.git/*' 2>/dev/null | sort"

// EnumerateHome lists all directories under the guest home dir via SSH find.
// Returns a flat list sorted by path.
func EnumerateHome(conn SSHConn) ([]*DirEntry, error) {
	c := exec.Command("ssh", conn.args(findCmd)...)
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
		entries = append(entries, pathToEntry(line))
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})
	return entries, nil
}

// EnumerateHomeStream runs find over SSH and sends entries line-by-line to ch.
// Closes ch when done. Send errors to errCh (buffered 1). Caller owns both channels.
func EnumerateHomeStream(conn SSHConn, ch chan<- *DirEntry, errCh chan<- error) {
	c := exec.Command("ssh", conn.args(findCmd)...)
	stdout, err := c.StdoutPipe()
	if err != nil {
		errCh <- fmt.Errorf("stdout pipe: %w", err)
		close(ch)
		return
	}
	if err := c.Start(); err != nil {
		errCh <- fmt.Errorf("ssh start: %w", err)
		close(ch)
		return
	}
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			ch <- pathToEntry(line)
		}
	}
	_ = c.Wait()
	close(ch)
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
