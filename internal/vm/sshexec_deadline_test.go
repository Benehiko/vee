package vm

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestDialSSHHonoursTimeoutOnSilentListener covers a user-mode NAT port
// forward, which accepts TCP before the guest's sshd exists and then never
// sends a banner: dialSSH must give up at its timeout, not block forever.
func TestDialSSHHonoursTimeoutOnSilentListener(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// The accept loop owns every connection it holds open (never writing), and
	// closes them itself once the listener closes; the test waits for that.
	done := make(chan struct{})
	go func() {
		defer close(done)
		var held []net.Conn
		defer func() {
			for _, c := range held {
				_ = c.Close()
			}
		}()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			held = append(held, c)
		}
	}()
	defer func() {
		_ = ln.Close()
		<-done
	}()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err = dialSSH(t.Context(), ln.Addr().String(), "vee", pem.EncodeToMemory(block), 1500*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("dial to a silent listener succeeded")
	}
	// Timeout + one retry backoff (2s) is the most it may overshoot.
	if elapsed > 5*time.Second {
		t.Fatalf("dialSSH took %s against a 1.5s timeout; the handshake is unbounded", elapsed)
	}
}

// TestRunHonoursContextWhenChannelOpenStalls covers a guest that completes
// the SSH handshake and then goes down before confirming the session channel
// (Windows rebooting under a live sshd).
func TestRunHonoursContextWhenChannelOpenStalls(t *testing.T) {
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	srvCfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil },
	}
	srvCfg.AddHostKey(hostSigner)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		sc, chans, reqs, err := ssh.NewServerConn(conn, srvCfg)
		if err != nil {
			return
		}
		defer func() { _ = sc.Close() }()
		go ssh.DiscardRequests(reqs)
		for range chans { // never accept or reject: the open stalls
		}
	}()

	_, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(clientPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := dialSSH(t.Context(), ln.Addr().String(), "vee", pem.EncodeToMemory(block), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	start := time.Now()
	_, _, err = c.Run(ctx, "ver")
	if err == nil {
		t.Fatal("Run succeeded against a server that never opens channels")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("Run took %s against a 1s context; the session open is unbounded", elapsed)
	}
}
