package mcpserver

// Coverage declares how one vee CLI command is represented over MCP: either
// by one or more tools, or by an explicit exemption with the reason it stays
// CLI-only. cmd's TestMCPCoversCLI walks the cobra command tree and fails
// when a command has no entry here — so adding a CLI command forces a
// decision about its MCP surface, and neither side can drift silently.
type Coverage struct {
	// Tools are the MCP tool names covering this command.
	Tools []string
	// Exempt explains why no MCP tool exists. Set exactly one of Tools/Exempt
	// (a command with partial tool coverage lists Tools and explains the gap
	// in Note).
	Exempt string
	// Note documents intentional gaps between the CLI command and its tools.
	Note string
}

// CLICoverage maps every vee CLI command (by its space-joined command path,
// e.g. "gpu list") to its MCP surface. Subcommands inherit the parent's entry
// unless they have their own.
var CLICoverage = map[string]Coverage{
	"create": {Tools: []string{"vm_create", "template_list"}},
	"pull":   {Tools: []string{"image_pull", "image_catalog"}},
	"start":  {Tools: []string{"vm_start"}, Note: "--foreground serial streaming stays CLI-only; vm_logs covers post-hoc reads"},
	"stop":   {Tools: []string{"vm_stop"}},
	"list":   {Tools: []string{"vm_list"}},
	"status": {Tools: []string{"vm_status", "vm_check"}, Note: "--watch live cloud-init progress stays CLI-only; poll vm_status instead"},
	"ssh":    {Tools: []string{"vm_exec"}, Note: "the interactive shell stays CLI-only; vm_exec runs one-off guest commands via the guest agent"},
	"ssh-share": {
		Exempt: "long-lived host-SSH-agent vsock proxy tied to an interactive SSH session",
	},
	"tunnel": {Tools: []string{"vm_services"}, Note: "opening tunnels stays CLI-only (long-lived foreground proxy / GUI launch); vm_services reports how to reach each service"},
	"ports":  {Tools: []string{"vm_ports"}},
	"ip":     {Tools: []string{"vm_ip"}},
	"logs":   {Tools: []string{"vm_logs"}, Note: "--follow streaming stays CLI-only; the tool returns bounded tails"},
	"monitor": {
		Tools: []string{"vm_stats"},
		Note:  "the live TUI stays CLI-only; vm_stats returns one sample",
	},
	"qmp":        {Tools: []string{"vm_qmp"}, Note: "--stdin batching stays CLI-only; call the tool repeatedly instead"},
	"screenshot": {Tools: []string{"vm_screenshot"}, Note: "the CLI writes a PNG file; the tool returns the image inline (and saves it when path is given)"},
	"view":       {Tools: []string{"vm_display"}, Note: "launching GUI clients stays CLI-only; the tool returns connection details"},
	"config":     {Tools: []string{"vm_config_get"}, Note: "editing stays CLI-only (validated TUI form); recreate via vm_create for config changes"},
	"check":      {Tools: []string{"vm_check"}},
	"network":    {Tools: []string{"vm_network"}},
	"backup":     {Tools: []string{"vm_backup", "vm_backup_list"}, Note: "the interactive directory picker stays CLI-only; pass dirs explicitly"},
	"cp":         {Tools: []string{"vm_cp"}},
	"wait":       {Tools: []string{"vm_wait"}, Note: "vm_start also waits at boot; vm_wait gates on an authenticated SSH round-trip"},
	"autostart": {
		Tools: []string{"vm_autostart"},
	},
	"move":   {Tools: []string{"vm_move"}},
	"resize": {Tools: []string{"vm_resize"}},
	"delete": {Tools: []string{"vm_delete"}},
	"daemon": {Exempt: "host systemd service management (requires sudo); the daemon itself is not an agent task"},
	"dashboard": {
		Exempt: "long-lived web server; the same data is available via vm_list, vm_status, and vm_stats",
	},
	"gpu":        {Exempt: "parent command; see subcommand entries"},
	"gpu list":   {Tools: []string{"gpu_list"}},
	"gpu status": {Tools: []string{"gpu_status"}},
	"gpu bind":   {Exempt: "requires root and mutates host device drivers"},
	"gpu unbind": {Exempt: "requires root and mutates host device drivers"},
	"mirror":     {Exempt: "parent command; see subcommand entries"},
	"mirror start": {
		Tools: []string{"mirror_start"},
	},
	"mirror stop":     {Tools: []string{"mirror_stop"}},
	"mirror status":   {Tools: []string{"mirror_status"}},
	"mirror purge":    {Tools: []string{"mirror_purge"}},
	"runner":          {Exempt: "parent command; see subcommand entries"},
	"runner key":      {Tools: []string{"runner_key"}},
	"runner snapshot": {Tools: []string{"runner_snapshot"}},
	"version":         {Exempt: "the server reports its version in the MCP initialize handshake"},
	"mcp":             {Exempt: "is the MCP server"},
}
