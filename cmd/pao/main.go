// Command pao is the node daemon and its operator CLI. It stays thin: parse
// flags, wire collaborators, run. Behaviour lives in internal/.
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/alecthomas/kong"
)

// CLI is the root command tree.
type CLI struct {
	Version VersionCmd `cmd:"" help:"Print the build version."`
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
