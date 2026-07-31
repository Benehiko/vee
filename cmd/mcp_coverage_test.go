package cmd

import (
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/mcpserver"
)

// TestMCPCoversCLI is the CLI↔MCP drift guard. Every vee command must either
// map to MCP tools or carry an explicit exemption in mcpserver.CLICoverage —
// so adding a CLI command without deciding its MCP surface fails the build,
// and stale entries (renamed commands, deleted tools) fail it too.
func TestMCPCoversCLI(t *testing.T) {
	tools := map[string]bool{}
	for _, name := range mcpserver.ToolNames() {
		if tools[name] {
			t.Errorf("tool %q registered twice", name)
		}
		tools[name] = true
	}

	// Walk the real command tree.
	paths := map[string]bool{}
	var walk func(prefix string, cmds []*cobra.Command)
	walk = func(prefix string, cmds []*cobra.Command) {
		for _, c := range cmds {
			if c.Hidden || c.Name() == "help" {
				continue
			}
			path := c.Name()
			if prefix != "" {
				path = prefix + " " + c.Name()
			}
			paths[path] = true
			walk(path, c.Commands())
		}
	}
	walk("", rootCmd.Commands())

	// coverageFor resolves a command path to its entry, falling back to the
	// nearest ancestor (so "daemon install" inherits "daemon").
	coverageFor := func(path string) (mcpserver.Coverage, string, bool) {
		for p := path; p != ""; {
			if entry, ok := mcpserver.CLICoverage[p]; ok {
				return entry, p, true
			}
			idx := strings.LastIndex(p, " ")
			if idx < 0 {
				break
			}
			p = p[:idx]
		}
		return mcpserver.Coverage{}, "", false
	}

	referenced := map[string]bool{}
	for path := range paths {
		entry, _, ok := coverageFor(path)
		if !ok {
			t.Errorf("CLI command %q has no entry in mcpserver.CLICoverage — add MCP tool(s) for it, or an explicit exemption with the reason it stays CLI-only", path)
			continue
		}
		if len(entry.Tools) == 0 && entry.Exempt == "" {
			t.Errorf("CLICoverage entry for %q declares neither Tools nor Exempt", path)
		}
		if len(entry.Tools) > 0 && entry.Exempt != "" {
			t.Errorf("CLICoverage entry for %q declares both Tools and Exempt — pick one (use Note for partial-coverage caveats)", path)
		}
		for _, tool := range entry.Tools {
			referenced[tool] = true
			if !tools[tool] {
				t.Errorf("CLICoverage entry for %q references tool %q, which the MCP server does not register", path, tool)
			}
		}
	}

	// Stale keys: every declared path must exist as a real command.
	declared := make([]string, 0, len(mcpserver.CLICoverage))
	for path := range mcpserver.CLICoverage {
		declared = append(declared, path)
	}
	slices.Sort(declared)
	for _, path := range declared {
		if !paths[path] {
			t.Errorf("CLICoverage declares %q, but no such CLI command exists — remove or rename the entry", path)
		}
	}

	// Orphan tools: every registered tool must be tied to at least one CLI
	// command, so tool renames keep the map honest.
	for name := range tools {
		if !referenced[name] {
			t.Errorf("MCP tool %q is not referenced by any CLICoverage entry — add it to the command it covers", name)
		}
	}
}
