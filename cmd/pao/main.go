// Command pao is the node daemon and its operator CLI. It stays thin: parse
// flags, wire collaborators, run. Behaviour lives in internal/.
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/alecthomas/kong"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/adapter"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/builtin"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/egress/confine"
)

// CLI is the root command tree.
type CLI struct {
	Version  VersionCmd  `cmd:"" help:"Print the build version."`
	Config   ConfigCmd   `cmd:"" help:"Inspect pao configuration."`
	Run      RunCmd      `cmd:"" help:"Run the node daemon."`
	Chat     ChatCmd     `cmd:"" help:"Talk to agents through the node's local channel."`
	Message  MessageCmd  `cmd:"" help:"Send one message to an agent as the operator, without waiting for its answer."`
	Mesh     MeshCmd     `cmd:"" help:"This node's place in a mesh: its key and the nodes it trusts."`
	Recipes  RecipesCmd  `cmd:"" help:"Recipes that temporary agents are spawned from."`
	Spawn    SpawnCmd    `cmd:"" help:"Start a temporary agent from a recipe."`
	Kill     KillCmd     `cmd:"" help:"Tear a temporary agent down, harvesting its outputs first."`
	Agents   AgentsCmd   `cmd:"" help:"List the temporary agents and their status flags."`
	Stream   StreamCmd   `cmd:"" help:"Bind an agent's output to a channel, or clear the binding."`
	Bindings StandingCmd `cmd:"" help:"Standing bindings: which destinations follow an agent's output or stderr."`
	Watch    WatchCmd    `cmd:"" help:"Follow an agent's output on demand, here or on another node."`
	View     ViewCmd     `cmd:"" help:"Pause, resume or backtrack the view of an agent's output."`
	Agent    AgentCmd    `cmd:"" help:"Start, stop or restart a configured agent."`
	Governor GovernorCmd `cmd:"" help:"The governor, which turns a request in plain words into one command to commit."`
	Access   AccessCmd   `cmd:"" help:"The access matrix: which agents each subject may reach."`
	Identity IdentityCmd `cmd:"" help:"Human subjects and the sender ids bound to them."`
	Channel  ChannelCmd  `cmd:"" help:"The node's chat channels and their default agents."`
	Inbound  InboundCmd  `cmd:"" help:"Inbounds the node received but did not act on."`
	Harvests HarvestsCmd `cmd:"" help:"Outputs harvested from killed temporary agents."`
	Budget   BudgetCmd   `cmd:"" help:"Each account's budget as the running node last read it."`
	Tasks    TasksCmd    `cmd:"" help:"Tasks: the units of work pao dispatches to agents."`
	Hook     HookCmd     `cmd:"" help:"The backend's pre-tool-call hook: ask the node's gate about an action."`

	EgressExec EgressExecCmd `cmd:"" name:"egress-exec" hidden:"" help:"Run a command whose only way out is the egress proxy (the daemon's, not for typing)."`
}

// ConfigCmd groups the configuration commands.
type ConfigCmd struct {
	Check ConfigCheckCmd `cmd:"" help:"Validate the orchestrator config, every agent profile it references and every channel's adapter options."`
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
	specs := make([]adapter.Spec, 0, len(cfg.Orchestrator.Channels))
	for _, ch := range cfg.Orchestrator.Channels {
		specs = append(specs, adapter.Spec{ID: ch.ID, Name: ch.Name, Kind: ch.Kind, Options: ch.Options})
	}
	if err := builtin.Table().Check(specs); err != nil {
		return fmt.Errorf("%s: %w", c.Path, err)
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
	// pao re-runs itself as the setup step of a confined agent's namespaces
	// (ADR-0049); that step is not a command line to parse.
	if confine.IsInner() {
		os.Exit(confine.Inner())
	}
	ctx := kong.Parse(&CLI{},
		kong.Name("pao"),
		kong.Description("personal-agent-orchestrator: routes input to coding-agent harnesses."),
	)
	ctx.FatalIfErrorf(ctx.Run())
}
