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
	Version VersionCmd `cmd:"" help:"Print the build version."`
	Config  ConfigCmd  `cmd:"" help:"Inspect pao configuration."`
	Run     RunCmd     `cmd:"" help:"Run the node daemon."`
	Chat    ChatCmd    `cmd:"" help:"Talk to agents through the node's local channel."`
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
