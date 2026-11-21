package main

import (
	"fmt"
	"io"
	"os"

	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/governor"
)

// GovernorCmd groups the governor's commands.
type GovernorCmd struct {
	Instructions GovernorInstructionsCmd `cmd:"" help:"Print the governor's instructions, for the instruction file in its working directory."`
}

// GovernorInstructionsCmd prints the governor's instructions for a node: the
// commands it may propose, under the node's prefix and narrower set.
type GovernorInstructionsCmd struct {
	Path string `arg:"" type:"existingfile" help:"Path to the orchestrator config file."`
}

// Run implements governor instructions.
func (c GovernorInstructionsCmd) Run() error { return c.run(os.Stdout) }

func (c GovernorInstructionsCmd) run(w io.Writer) error {
	cfg, err := config.Load(c.Path)
	if err != nil {
		return err
	}
	g := cfg.Orchestrator.Governor
	if g.Agent == "" {
		return fmt.Errorf("%s: no governor is configured: set governor.agent", c.Path)
	}
	prefix := g.Prefix
	if prefix == "" {
		prefix = comm.DefaultGovernorPrefix
	}
	_, err = io.WriteString(w, governor.Instructions(prefix, g.Propose))
	return err
}
