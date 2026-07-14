//go:build !linux

package ssh

import (
	"context"
	"fmt"
	"os"
)

// AF_VSOCK is a Linux-only address family. On other hosts (macOS, Windows) the
// SSH-agent-over-vsock proxy is unavailable; guests must fall back to user-mode
// port forwarding. This stub mirrors the Linux Proxy API so the rest of the
// tree builds, and every operation reports the feature is unsupported.
//
// Callers are additionally guarded by platform.SupportsVsock(), which returns
// false here, so NewProxy should not be reached in normal operation.

// Proxy is an unsupported stub on non-Linux hosts.
type Proxy struct {
	agentSock string
}

var errVsockUnsupported = fmt.Errorf("SSH-agent sharing over AF_VSOCK is only supported on Linux hosts")

// NewProxy reports that vsock SSH-agent sharing is unsupported on this host.
func NewProxy(agentSock string) (*Proxy, error) {
	if agentSock == "" {
		agentSock = os.Getenv("SSH_AUTH_SOCK")
	}
	return &Proxy{agentSock: agentSock}, errVsockUnsupported
}

// Listen is unsupported on non-Linux hosts.
func (p *Proxy) Listen() error { return errVsockUnsupported }

// Serve is unsupported on non-Linux hosts.
func (p *Proxy) Serve() error { return errVsockUnsupported }

// ServeContext is unsupported on non-Linux hosts.
func (p *Proxy) ServeContext(_ context.Context) error { return errVsockUnsupported }

// Close is a no-op on non-Linux hosts.
func (p *Proxy) Close() error { return nil }
