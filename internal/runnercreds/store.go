package runnercreds

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
)

// decodeBase64 decodes the base64 stream produced by `base64 -w0` on the guest,
// tolerating any trailing newline the SSH channel appends.
func decodeBase64(b []byte) ([]byte, error) {
	return base64.StdEncoding.DecodeString(string(bytes.TrimSpace(b)))
}

// remoteRunnerDir is where the actions-runner software lives inside the VM.
const remoteRunnerDir = "/opt/actions-runner"

// snapshotsDir holds per-runner encrypted credential snapshots. It lives
// outside the VM storage dir on purpose: `vee create --reinstall` deletes the
// VM dir, and the snapshot must survive that so the recreated runner can
// restore its identity.
func snapshotsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, ".vee", "runner-creds")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create snapshots dir: %w", err)
	}
	return dir, nil
}

// SSHRunner runs a command on the guest and returns its combined output. It is
// satisfied by a small adapter over the existing ssh plumbing so this package
// stays free of the backup package and its DB dependency.
type SSHRunner interface {
	// Output runs the remote command and returns stdout. An error wraps any
	// non-zero exit along with stderr for diagnostics.
	Output(ctx context.Context, remoteCmd string) ([]byte, error)
}

// SnapshotPath returns the on-disk path of the encrypted snapshot for a runner,
// keyed by VM name under ~/.vee/runner-creds.
func SnapshotPath(name string) (string, error) {
	dir, err := snapshotsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".age"), nil
}

// Has reports whether an encrypted snapshot exists for the named runner.
func Has(name string) bool {
	path, err := SnapshotPath(name)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// Snapshot pulls the runner credential files off a running VM and writes an
// age-encrypted tar to ~/.vee/runner-creds/<name>.age. It returns os.ErrNotExist
// (wrapped) if the runner has not registered yet (no .credentials), so callers
// can poll.
func Snapshot(ctx context.Context, ssh SSHRunner, id *age.X25519Identity, name string) error {
	// Tar the cred files on the guest and stream them out base64-encoded so the
	// bytes survive the SSH text channel intact. --ignore-failed-read would mask
	// a missing .credentials, so probe it explicitly first.
	probe := fmt.Sprintf("test -f %s/.credentials", remoteRunnerDir)
	if _, err := ssh.Output(ctx, probe); err != nil {
		return fmt.Errorf("runner not yet registered (no .credentials): %w", os.ErrNotExist)
	}

	// Only tar files that exist — .credentials_rsaparams is present for some
	// registration flows and absent for others.
	tarCmd := fmt.Sprintf(
		"cd %s && files=''; for f in %s; do [ -f \"$f\" ] && files=\"$files $f\"; done; "+
			"sudo tar -cf - $files | base64 -w0",
		remoteRunnerDir, strings.Join(CredFiles, " "))

	out, err := ssh.Output(ctx, tarCmd)
	if err != nil {
		return fmt.Errorf("tar runner creds: %w", err)
	}

	archive, err := decodeBase64(out)
	if err != nil {
		return fmt.Errorf("decode cred archive: %w", err)
	}
	if len(archive) == 0 {
		return fmt.Errorf("empty cred archive from guest")
	}

	recipient := id.Recipient()
	var enc bytes.Buffer
	w, err := age.Encrypt(&enc, recipient)
	if err != nil {
		return fmt.Errorf("age encrypt init: %w", err)
	}
	if _, err := w.Write(archive); err != nil {
		return fmt.Errorf("age encrypt write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("age encrypt close: %w", err)
	}

	dst, err := SnapshotPath(name)
	if err != nil {
		return err
	}
	tmp, err := writeSecret(dst, enc.Bytes())
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install cred snapshot: %w", err)
	}
	return nil
}

// RestoredFile is a single runner credential file ready for cloud-init
// injection. Path is relative to /opt/actions-runner.
type RestoredFile struct {
	RelPath string
	Content []byte
	Mode    int64
}

// Restore decrypts the snapshot for the named runner and returns its files. It
// returns os.ErrNotExist (wrapped) when no snapshot exists, so callers can fall
// back to token registration.
func Restore(id *age.X25519Identity, name string) ([]RestoredFile, error) {
	path, err := SnapshotPath(name)
	if err != nil {
		return nil, err
	}
	//nolint:gosec // path is ~/.vee/runner-creds/<name>.age from SnapshotPath, built from os.UserHomeDir, not user input.
	enc, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no runner snapshot at %s: %w", path, os.ErrNotExist)
		}
		return nil, fmt.Errorf("read snapshot: %w", err)
	}

	r, err := age.Decrypt(bytes.NewReader(enc), id)
	if err != nil {
		return nil, fmt.Errorf("age decrypt %s (wrong identity?): %w", path, err)
	}
	archive, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read decrypted archive: %w", err)
	}

	var files []RestoredFile
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar entry: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read tar file %s: %w", hdr.Name, err)
		}
		files = append(files, RestoredFile{
			RelPath: filepath.Clean(hdr.Name),
			Content: content,
			Mode:    hdr.Mode,
		})
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("snapshot %s decrypted to no files", path)
	}
	return files, nil
}

// SnapshotWithRetry polls Snapshot until the runner has registered or the
// context/deadline elapses. interval is the poll period. It exists because
// config.sh runs asynchronously via cloud-init after vee create returns, so
// .credentials may not exist for the first few seconds.
func SnapshotWithRetry(ctx context.Context, ssh SSHRunner, id *age.X25519Identity, name string, attempts int, interval time.Duration) error {
	var lastErr error
	for range attempts {
		err := Snapshot(ctx, ssh, id, name)
		if err == nil {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("snapshot did not succeed after %d attempts: %w", attempts, lastErr)
}

// sshExecRunner adapts an *exec.Cmd-based ssh invocation to SSHRunner. It is the
// default production implementation; tests inject their own SSHRunner.
type sshExecRunner struct {
	user     string
	host     string
	port     int
	identity string
}

// NewSSHRunner builds an SSHRunner that shells out to the system ssh client,
// mirroring the connection style used elsewhere in vee (BatchMode, no host-key
// prompt — the host is a local VM on a deterministic port).
func NewSSHRunner(user, host string, port int, identity string) SSHRunner {
	return &sshExecRunner{user: user, host: host, port: port, identity: identity}
}

func (r *sshExecRunner) Output(ctx context.Context, remoteCmd string) ([]byte, error) {
	args := []string{"-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes"}
	if r.port != 0 && r.port != 22 {
		args = append(args, "-p", fmt.Sprintf("%d", r.port))
	}
	if r.identity != "" {
		args = append(args, "-i", r.identity)
	}
	dest := r.host
	if r.user != "" {
		dest = r.user + "@" + r.host
	}
	args = append(args, dest, remoteCmd)

	cmd := exec.CommandContext(ctx, "ssh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ssh: %w (stderr: %s)", err, bytes.TrimSpace(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}
