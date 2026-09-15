# Claude Code skill: driving vee VMs

The repository ships a [Claude Code](https://docs.anthropic.com/en/docs/claude-code)
skill at [`.claude/skills/vee-vm/SKILL.md`](../.claude/skills/vee-vm/SKILL.md).
A skill is a Markdown file with a short frontmatter (`name`, `description`)
followed by instructions. Claude Code reads the description to decide when the
skill applies and loads the instructions into the conversation when it does.

The `vee-vm` skill teaches Claude how to use the `vee` CLI as a test bench:
pick or create a VM, wait for SSH, copy a tree in, run commands and Go tests
inside Linux and Windows guests, take screenshots, and tear down. It also
records the gotchas that cost the most time in practice: `vee wait` returning
before cloud-init finishes, `sudo reboot` wedging QEMU guests, backslash
stripping on Windows `vee ssh` arguments, and how to run a probe as a standard
Windows user from an all-privileges SSH session.

## Skill or MCP server?

Both work, and they compose.

- The [MCP server](mcp.md) (`vee mcp`) exposes vee as typed tools with JSON
  results. It suits agents that should never parse terminal output.
- The skill is prose. It teaches the agent workflow and pitfalls, and it drives
  the plain CLI through the agent's shell tool. It needs no registration beyond
  placing the file where Claude Code looks.

Register the MCP server for structured tool calls, add the skill for the
know-how, or use either alone.

## Using the skill inside this repository

Nothing to install. Claude Code discovers `.claude/skills/*/SKILL.md` in the
project it runs in, so a session started in a vee checkout already has
`vee-vm` available. Type `/vee-vm` to invoke it directly, or describe a task
that needs a VM ("reproduce this test failure on Ubuntu") and Claude picks the
skill up from its description.

## Using the skill globally

Claude Code also loads skills from `~/.claude/skills/<name>/SKILL.md`. A skill
placed there is available in every project on the machine, which is where the
`vee-vm` skill earns its keep: the point is to test some other repository's
code inside a VM.

Copy the skill directory:

```sh
mkdir -p ~/.claude/skills
cp -R /path/to/vee/.claude/skills/vee-vm ~/.claude/skills/vee-vm
```

Or symlink it so `git pull` in the vee checkout updates the global copy:

```sh
mkdir -p ~/.claude/skills
ln -s /path/to/vee/.claude/skills/vee-vm ~/.claude/skills/vee-vm
```

Start a new Claude Code session afterwards; skills are read at startup. Check
the result by typing `/` in the prompt and looking for `vee-vm` in the list,
or ask Claude which skills it has.

To remove the skill, delete `~/.claude/skills/vee-vm`.

## Requirements

The skill assumes:

- `vee` installed (see [prerequisites.md](prerequisites.md)) with the daemon
  running, and `~/.vee/bin` on `PATH` or the full path used.
- An Apple Silicon macOS host. Most of the skill applies to Linux hosts too,
  but the guest-architecture notes, the SPICE caveat, and the Docker Desktop
  raw-socket workaround for the Windows unattend build are macOS-specific.
- Claude Code with Bash access, since the skill drives the CLI through shell
  commands.

## Editing the skill

Keep the frontmatter `name` equal to the directory name. The `description`
is what Claude matches against, so state the trigger conditions there, not
only what the skill does. Instructions that only hold on one machine (a
broken driver, a personal permission rule) belong in a user-level skill or in
Claude's memory, not in this file.
