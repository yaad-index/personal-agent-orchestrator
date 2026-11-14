// Command pao is the node daemon and its operator CLI. It stays thin: parse
// flags, wire collaborators, run. Behaviour lives in internal/.
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/alecthomas/kong"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

// CLI is the root command tree.
type CLI struct {
	Version      VersionCmd      `cmd:"" help:"Print the build version."`
	Config       ConfigCmd       `cmd:"" help:"Inspect pao configuration."`
	Run          RunCmd          `cmd:"" help:"Run the node daemon."`
	Chat         ChatCmd         `cmd:"" help:"Talk to agents through the node's local channel."`
	Mesh         MeshCmd         `cmd:"" help:"This node's place in a mesh: its key and the nodes it trusts."`
	Recipes      RecipesCmd      `cmd:"" help:"Recipes that temporary agents are spawned from."`
	Spawn        SpawnCmd        `cmd:"" help:"Start a temporary agent from a recipe."`
	Kill         KillCmd         `cmd:"" help:"Tear a temporary agent down, harvesting its outputs first."`
	Agents       AgentsCmd       `cmd:"" help:"List the temporary agents and their status flags."`
	Stream       StreamCmd       `cmd:"" help:"Bind an agent's output to a channel, or clear the binding."`
	View         ViewCmd         `cmd:"" help:"Pause, resume or backtrack the view of an agent's output."`
	Harvests     HarvestsCmd     `cmd:"" help:"Outputs harvested from killed temporary agents."`
	Hook         HookCmd         `cmd:"" help:"The backend's pre-tool-call hook: ask the node's gate about an action."`
	HookSettings HookSettingsCmd `cmd:"" name:"hook-settings" help:"Print the backend settings JSON that installs the hook."`
	McpConfig    McpConfigCmd    `cmd:"" name:"mcp-config" help:"Print the backend MCP configuration that points an agent at its node."`
}

// ConfigCmd groups the configuration commands.
type ConfigCmd struct {
	Check ConfigCheckCmd `cmd:"" help:"Validate the orchestrator config and every agent profile it references."`
}

// ConfigCheckCmd loads and validates a config without starting anything.
type ConfigCheckCmd struct {
	Path string `arg:"" type:"existingfile" help:"Path to the orchestrator config file."`
}

// Run implements config check.
func (c ConfigCheckCmd) Run() error {
	cfg, err := config.Load(c.Path)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(os.Stdout, "ok: node %q, %d agent profile(s)\n", cfg.Orchestrator.Node, len(cfg.Profiles))
	return err
}

// VersionCmd prints the module version recorded in the build.
type VersionCmd struct{}

// Run implements the version command.
func (VersionCmd) Run() error {
	_, err := fmt.Fprintln(os.Stdout, buildVersion())
	return err
}

func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "devel"
}

func main() {
	ctx := kong.Parse(&CLI{},
		kong.Name("pao"),
		kong.Description("personal-agent-orchestrator: routes input to coding-agent harnesses."),
	)
	ctx.FatalIfErrorf(ctx.Run())
}
