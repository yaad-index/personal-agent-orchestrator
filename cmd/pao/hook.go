package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/backend/claudecode"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
)

// Environment the control plane gives every agent it starts, which the hook
// reads to find the gate and say who is asking.
const (
	envAgent      = "PAO_AGENT"
	envGateSocket = "PAO_GATE_SOCKET"
)

// HookCmd is the backend's pre-tool-call hook. It reads the hook payload on
// standard input, asks the node's gate, and prints the backend's decision. It
// fails closed: anything that goes wrong is a denial.
type HookCmd struct{}

// Run implements hook.
func (HookCmd) Run() error {
	return runHook(context.Background(), os.Stdin, os.Stdout, os.Getenv(envAgent), os.Getenv(envGateSocket))
}

func runHook(ctx context.Context, stdin io.Reader, stdout io.Writer, agent, socket string) error {
	deny := func(reason string) error {
		_, err := stdout.Write(append(claudecode.HookOutput(false, reason), '\n'))
		return err
	}
	if agent == "" || socket == "" {
		return deny("this agent was not started by pao, so there is no gate to ask")
	}
	in, err := claudecode.ParseHookInput(stdin)
	if err != nil {
		return deny("pao could not read the action: " + err.Error())
	}
	d, _ := gate.Ask(ctx, socket, gate.Request{Agent: agent, Tool: in.Tool, Input: in.Input})
	reason := d.Reason
	if !d.Allow {
		reason += ". Do not retry the same action; ask the operator if it is needed."
	}
	_, err = stdout.Write(append(claudecode.HookOutput(d.Allow, reason), '\n'))
	return err
}

// HookSettingsCmd prints the backend settings JSON that installs the hook, for
// a profile's startup command (for example: claude --settings "$(pao
// hook-settings)").
type HookSettingsCmd struct {
	Command string        `help:"Hook command the backend runs." default:"pao hook"`
	Timeout time.Duration `help:"How long the backend waits for the hook; it must outlast the operator's time to answer. Ignored when --config is given." default:"10m"`
	Config  string        `help:"Orchestrator config to read the gate's ask timeout from; the hook timeout is then that plus a margin, so the backend never gives up before the gate does." type:"existingfile"`
}

// hookMargin is how much longer than the gate's ask timeout the backend waits
// for the hook, so the gate's own denial arrives before the backend's.
const hookMargin = time.Minute

// Run implements hook-settings.
func (c HookSettingsCmd) Run() error {
	timeout, err := c.timeout()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, claudecode.HookSettings(c.Command, int(timeout.Seconds())))
	return err
}

func (c HookSettingsCmd) timeout() (time.Duration, error) {
	if c.Config == "" {
		return c.Timeout, nil
	}
	cfg, err := config.Load(c.Config)
	if err != nil {
		return 0, err
	}
	return cfg.Orchestrator.Gate.AskTimeout + hookMargin, nil
}
