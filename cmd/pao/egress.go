package main

import (
	"errors"
	"os"

	"github.com/yaad-index/personal-agent-orchestrator/internal/egress/confine"
)

// EgressExecCmd runs a command whose only way out is the egress proxy
// (ADR-0049). The node daemon makes it a task-agent's pane command; it is not
// meant to be typed.
type EgressExecCmd struct {
	Socket  string   `required:"" help:"The egress proxy's socket."`
	Mask    []string `sep:"none" help:"A host path the command does not see; repeat for more."`
	Forward []string `sep:"none" help:"A loopback address inside carried to a host socket, as addr=socket; repeat for more."`
	Command []string `arg:"" passthrough:"" help:"The command and its arguments."`
}

// Run implements egress-exec. It exits with the command's status.
func (c EgressExecCmd) Run() error {
	command := c.Command
	if len(command) > 0 && command[0] == "--" {
		command = command[1:]
	}
	if len(command) == 0 {
		return errors.New("egress-exec: no command")
	}
	spec := confine.Spec{Socket: c.Socket, Mask: c.Mask, Command: command}
	for _, f := range c.Forward {
		fw, err := confine.ParseForward(f)
		if err != nil {
			return err
		}
		spec.Forwards = append(spec.Forwards, fw)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	code, err := confine.Run(self, spec)
	if err != nil {
		return err
	}
	os.Exit(code)
	return nil
}
