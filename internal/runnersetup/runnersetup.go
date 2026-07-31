// Package runnersetup assembles the github-runner template's registration
// data: credential-snapshot restore, the registration token, and the runner's
// GitHub SSH key. It is the single source of truth shared by `vee create` and
// the MCP server's vm_create, so the two surfaces cannot drift apart.
package runnersetup

import (
	"fmt"

	"github.com/Benehiko/vee/internal/runnercreds"
	"github.com/Benehiko/vee/internal/runnerssh"
	"github.com/Benehiko/vee/internal/templates"
	"github.com/Benehiko/vee/internal/vm/build"
)

// DefaultLabels are applied when the caller supplies none.
var DefaultLabels = []string{"self-hosted", "linux", "kvm"}

// Result carries the prepared registration data plus what the caller should
// surface to the user.
type Result struct {
	Extras *build.RunnerExtras
	// PubKey is the runner's GitHub SSH public key in authorized_keys form;
	// KeyCreated reports whether it was newly generated (callers surface the
	// key to the user then, so it can be added to GitHub).
	PubKey     string
	KeyCreated bool
	// RestoredFiles is the number of credential files restored from a host
	// snapshot. 0 means a fresh registration using the token.
	RestoredFiles int
}

// Prepare assembles build.RunnerExtras for a github-runner create.
//
// If a credential snapshot exists for name (from `vee runner snapshot` or a
// previous create), it is restored and no token is needed. Otherwise tokenFn
// is called once and must return a fresh GitHub registration token — the CLI
// prompts the terminal, the MCP server returns a tool parameter.
//
// perInstanceKey selects a per-runner SSH key named after the VM instead of
// the shared global runner key.
func Prepare(name, url string, labels []string, perInstanceKey bool, tokenFn func() (string, error)) (*Result, error) {
	if url == "" {
		return nil, fmt.Errorf("a repo or org URL is required for the github-runner template")
	}
	if len(labels) == 0 {
		labels = DefaultLabels
	}

	// A host snapshot lets a recreated runner rejoin GitHub as the same
	// runner: restore it instead of fetching a fresh registration token.
	var restored []templates.RunnerCredFile
	if runnercreds.Has(name) {
		id, err := runnercreds.LoadOrCreateIdentity()
		if err != nil {
			return nil, fmt.Errorf("load age identity: %w", err)
		}
		files, err := runnercreds.Restore(id, name)
		if err != nil {
			return nil, fmt.Errorf("restore runner creds for %q: %w", name, err)
		}
		for _, f := range files {
			restored = append(restored, templates.RunnerCredFile{
				RelPath: f.RelPath, Content: f.Content, Mode: f.Mode,
			})
		}
	}

	var token string
	if len(restored) == 0 {
		t, err := tokenFn()
		if err != nil {
			return nil, err
		}
		token = t
	}

	extras := &build.RunnerExtras{
		URL:           url,
		Token:         token,
		Labels:        labels,
		RestoredCreds: restored,
	}

	// SSH key for GitHub access: the shared global key by default, or a
	// per-instance key (scope it to one repo via a read-only deploy key).
	keyName := ""
	if perInstanceKey {
		keyName = name
	}
	id, err := runnercreds.LoadOrCreateIdentity()
	if err != nil {
		return nil, fmt.Errorf("load age identity: %w", err)
	}
	pub, createdKey, err := runnerssh.EnsureKey(id, keyName)
	if err != nil {
		return nil, fmt.Errorf("ensure runner ssh key: %w", err)
	}
	priv, err := runnerssh.LoadPrivateKey(id, keyName)
	if err != nil {
		return nil, fmt.Errorf("load runner ssh key: %w", err)
	}
	extras.SSHPrivKey = priv

	return &Result{
		Extras:        extras,
		PubKey:        pub,
		KeyCreated:    createdKey,
		RestoredFiles: len(restored),
	}, nil
}
