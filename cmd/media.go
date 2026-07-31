package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/Benehiko/vee/internal/media"
)

// collectMediaSecrets walks every Source, calls Plan to discover required
// prompts, and reads each value from the terminal (masking for Secret prompts).
//
// Returns a map keyed by PendingPrompt.Key suitable for re-invoking Plan.
// Secrets are not persisted anywhere by this function — the caller passes them
// straight to template/build, which writes them into the cloud-init cidata ISO
// (consumed on first boot, then discarded).
func collectMediaSecrets(sources []media.Source) (map[string]string, error) {
	secrets := map[string]string{}
	stdin := bufio.NewReader(os.Stdin)
	for _, src := range sources {
		_, prompts, err := src.Plan(media.Ubuntu, secrets)
		if err != nil {
			return nil, fmt.Errorf("plan %s: %w", src.Kind, err)
		}
		for _, pp := range prompts {
			if _, done := secrets[pp.Key]; done {
				continue
			}
			fmt.Fprintf(os.Stderr, "%s: ", pp.Prompt)
			if pp.Secret {
				//nolint:gosec // os.Stdin.Fd() is a small OS file descriptor; no overflow.
				pw, readErr := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(os.Stderr)
				if readErr != nil {
					return nil, readErr
				}
				secrets[pp.Key] = string(pw)
				continue
			}
			line, readErr := stdin.ReadString('\n')
			if readErr != nil {
				return nil, readErr
			}
			secrets[pp.Key] = strings.TrimRight(line, "\r\n")
		}
	}
	return secrets, nil
}
