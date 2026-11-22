package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
)

// ChannelCmd groups the commands about the node's chat channels.
type ChannelCmd struct {
	Default  ChannelDefaultCmd  `cmd:"" help:"Set or clear a channel's default agent: the one that answers a message naming no agent."`
	Defaults ChannelDefaultsCmd `cmd:"" help:"List each channel's default agent."`
}

// ChannelDefaultCmd sets or clears a channel's default agent.
type ChannelDefaultCmd struct {
	NodeFlags
	Channel string `arg:"" help:"Configured channel, or an admitted group or named topic as group:<name>."`
	Agent   string `arg:"" optional:"" help:"Agent to make its default. It stops being the default of any other channel, group or topic."`
	Clear   bool   `help:"Remove the channel's default agent."`
}

// Run implements channel default.
func (c ChannelDefaultCmd) Run() error { return c.run(os.Stdout) }

func (c ChannelDefaultCmd) run(w io.Writer) error {
	if c.Clear == (c.Agent != "") {
		return fmt.Errorf("name an agent to make it the default, or give --clear to remove the default")
	}
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	var ch local.DefaultChangeJSON
	if c.Clear {
		ch, err = cl.ClearDefault(ctx, c.Channel)
	} else {
		ch, err = cl.SetDefault(ctx, c.Channel, c.Agent)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, describeDefaultChange(ch))
	return err
}

func describeDefaultChange(ch local.DefaultChangeJSON) string {
	var b strings.Builder
	if ch.Agent == "" {
		fmt.Fprintf(&b, "%s: no default agent", ch.Channel)
	} else {
		fmt.Fprintf(&b, "%s: default agent %s", ch.Channel, ch.Agent)
	}
	if ch.Previous != "" && ch.Previous != ch.Agent {
		fmt.Fprintf(&b, " (was %s)", ch.Previous)
	}
	if ch.Left != "" {
		fmt.Fprintf(&b, "; %s is no longer the default of %s", ch.Agent, ch.Left)
	}
	return b.String()
}

// ChannelDefaultsCmd lists the channels' default agents.
type ChannelDefaultsCmd struct{ NodeFlags }

// Run implements channel defaults.
func (c ChannelDefaultsCmd) Run() error { return c.run(os.Stdout) }

func (c ChannelDefaultsCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	d, err := cl.Defaults(ctx)
	if err != nil {
		return err
	}
	for _, e := range d.Defaults {
		if _, err := fmt.Fprintf(w, "%s\t%s\n", e.Channel, e.Agent); err != nil {
			return err
		}
	}
	return nil
}
