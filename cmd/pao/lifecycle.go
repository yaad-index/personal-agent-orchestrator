package main

import (
	"fmt"
	"io"
	"os"
)

// AgentCmd groups the commands that bring a configured agent up and down.
type AgentCmd struct {
	Start   AgentStartCmd   `cmd:"" help:"Start a configured agent now, without waiting for a message."`
	Stop    AgentStopCmd    `cmd:"" help:"Stop a configured agent; its next message starts it again."`
	Restart AgentRestartCmd `cmd:"" help:"Stop a configured agent and start it again."`
}

// AgentStartCmd starts an agent.
type AgentStartCmd struct {
	NodeFlags
	Name string `arg:"" help:"Agent to start; on this node or, at the master, on another."`
}

// Run implements agent start.
func (c AgentStartCmd) Run() error { return agentAction(c.NodeFlags, os.Stdout, c.Name, "start") }

// AgentStopCmd stops an agent.
type AgentStopCmd struct {
	NodeFlags
	Name string `arg:"" help:"Agent to stop; on this node or, at the master, on another."`
}

// Run implements agent stop.
func (c AgentStopCmd) Run() error { return agentAction(c.NodeFlags, os.Stdout, c.Name, "stop") }

// AgentRestartCmd restarts an agent.
type AgentRestartCmd struct {
	NodeFlags
	Name string `arg:"" help:"Agent to restart; on this node or, at the master, on another."`
}

// Run implements agent restart.
func (c AgentRestartCmd) Run() error { return agentAction(c.NodeFlags, os.Stdout, c.Name, "restart") }

func agentAction(f NodeFlags, w io.Writer, name, action string) error {
	cl, err := f.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	if err := cl.AgentAction(ctx, name, action); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s: %s done\n", name, action)
	return err
}
