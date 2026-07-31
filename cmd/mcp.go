package cmd

import (
	"errors"
	"io"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/mcpserver"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run an MCP server over stdio (for coding agents)",
	Long: `Run a Model Context Protocol server over stdio, exposing vee's VM
lifecycle as tools a coding agent can call: list, create, start, stop,
delete, resolve IPs, run guest commands, and read logs.

Register it with an MCP-capable agent, e.g. Claude Code:

  claude mcp add vee -- vee mcp

Stdout carries the MCP protocol stream. Logs go to ~/.vee/logs/vee.log as
usual; --verbose additionally streams them to stderr, which is protocol-safe.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		v, _, _ := resolveVersion()
		err := mcpserver.Run(cmd.Context(), prov, v)
		// The client closing stdin is the normal way an MCP session ends.
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	},
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}
