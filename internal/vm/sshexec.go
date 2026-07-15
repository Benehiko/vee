package vm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshExecClient is a minimal in-process SSH client used to drive a transient,
// vee-created build VM: run a command and fetch a file. It is intentionally small
// — one session per operation, no multiplexing.
//
// Host-key policy: this client only ever targets a VM that vee just created on a
// 127.0.0.1 port-forward and will destroy moments later. The guest has no
// persistent identity to pin (a recreated VM legitimately presents a new key), so
// InsecureIgnoreHostKey is acceptable here. It mirrors the CLI's posture, which
// scrubs known_hosts before every connect for the same reason. Do NOT lift this
// client for long-lived hosts without adding host-key pinning.
type sshExecClient struct {
	client *ssh.Client
}

// dialSSH connects to addr (host:port) as user, authenticating with the given
// PEM-encoded private key. It retries until sshd accepts or ctx/timeout expires,
// since a freshly booted guest may briefly refuse on the forwarded port.
func dialSSH(ctx context.Context, addr, user string, privKeyPEM []byte, timeout time.Duration) (*sshExecClient, error) {
	signer, err := ssh.ParsePrivateKey(privKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse ssh private key: %w", err)
	}

	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // G106: transient loopback build VM with no persistent identity; see type doc.
		Timeout:         10 * time.Second,
	}

	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		d := net.Dialer{Timeout: 10 * time.Second}
		conn, derr := d.DialContext(ctx, "tcp", addr)
		if derr != nil {
			lastErr = derr
			time.Sleep(2 * time.Second)
			continue
		}
		c, chans, reqs, herr := ssh.NewClientConn(conn, addr, cfg)
		if herr != nil {
			_ = conn.Close()
			lastErr = herr
			time.Sleep(2 * time.Second)
			continue
		}
		return &sshExecClient{client: ssh.NewClient(c, chans, reqs)}, nil
	}
	if lastErr == nil {
		lastErr = context.DeadlineExceeded
	}
	return nil, fmt.Errorf("ssh dial %s: %w", addr, lastErr)
}

// Close releases the underlying SSH connection.
func (c *sshExecClient) Close() error {
	if c.client == nil {
		return nil
	}
	return c.client.Close()
}

// Run executes cmd on the guest and waits for it to finish, returning captured
// stdout and stderr. A non-zero exit status is returned as an error with stderr
// attached. ctx cancellation closes the session, unblocking a hung command.
func (c *sshExecClient) Run(ctx context.Context, cmd string) (stdout, stderr []byte, err error) {
	session, err := c.client.NewSession()
	if err != nil {
		return nil, nil, fmt.Errorf("new ssh session: %w", err)
	}
	defer func() { _ = session.Close() }()

	var outBuf, errBuf bytes.Buffer
	session.Stdout = &outBuf
	session.Stderr = &errBuf

	done := make(chan error, 1)
	if startErr := session.Start(cmd); startErr != nil {
		return nil, nil, fmt.Errorf("start ssh command: %w", startErr)
	}
	go func() { done <- session.Wait() }()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		return outBuf.Bytes(), errBuf.Bytes(), ctx.Err()
	case werr := <-done:
		if werr != nil {
			return outBuf.Bytes(), errBuf.Bytes(), fmt.Errorf("ssh command %q failed: %w: %s", cmd, werr, errBuf.String())
		}
		return outBuf.Bytes(), errBuf.Bytes(), nil
	}
}

// FetchFile streams the remote file at path to w by running `cat` in a session.
// Used to pull a compiled binary out of the build VM without an SFTP dependency.
func (c *sshExecClient) FetchFile(ctx context.Context, path string, w io.Writer) error {
	session, err := c.client.NewSession()
	if err != nil {
		return fmt.Errorf("new ssh session: %w", err)
	}
	defer func() { _ = session.Close() }()

	var errBuf bytes.Buffer
	session.Stdout = w
	session.Stderr = &errBuf

	// cat the file in binary-safe form; LC_ALL/quoting kept simple since path is
	// an internally constructed absolute path, never user input.
	cmd := "cat " + shellQuote(path)

	done := make(chan error, 1)
	if startErr := session.Start(cmd); startErr != nil {
		return fmt.Errorf("start ssh fetch: %w", startErr)
	}
	go func() { done <- session.Wait() }()

	select {
	case <-ctx.Done():
		_ = session.Close()
		return ctx.Err()
	case werr := <-done:
		if werr != nil {
			return fmt.Errorf("fetch %s: %w: %s", path, werr, errBuf.String())
		}
		return nil
	}
}

// shellQuote single-quotes a string for safe use in a POSIX shell command.
func shellQuote(s string) string {
	// Wrap in single quotes, escaping any embedded single quote as '\''.
	var b bytes.Buffer
	b.WriteByte('\'')
	for _, r := range s {
		if r == '\'' {
			b.WriteString(`'\''`)
			continue
		}
		b.WriteRune(r)
	}
	b.WriteByte('\'')
	return b.String()
}

// readVeePrivateKey reads the vee-managed private key bytes from
// <home>/.vee/ssh/id_ed25519.
func readVeePrivateKey(privKeyPath string) ([]byte, error) {
	//nolint:gosec // G304: privKeyPath is the fixed vee-managed key path derived from home dir.
	data, err := os.ReadFile(privKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read vee ssh key %s: %w", privKeyPath, err)
	}
	return data, nil
}
