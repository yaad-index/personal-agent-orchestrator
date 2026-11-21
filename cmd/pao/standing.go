package main

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
)

// StandingCmd groups the standing-binding commands (ADR-0029 points 3 and 8).
type StandingCmd struct {
	List   StandingListCmd   `cmd:"" help:"List the standing bindings, of one agent or of all, and where each was set."`
	Add    StandingAddCmd    `cmd:"" help:"Make a destination follow an agent's output or its stderr."`
	Remove StandingRemoveCmd `cmd:"" help:"Remove a standing binding set while the node runs."`
}

// StandingListCmd lists the standing bindings.
type StandingListCmd struct {
	NodeFlags
	Agent string `arg:"" optional:"" help:"Only this agent's bindings."`
}

// Run implements bindings list.
func (c StandingListCmd) Run() error { return c.run(os.Stdout) }

func (c StandingListCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	out, err := cl.StandingBindings(ctx, c.Agent)
	if err != nil {
		return err
	}
	if len(out.Bindings) == 0 {
		_, err = fmt.Fprintln(w, "no standing bindings")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "AGENT\tSTREAM\tDESTINATION\tLEVEL\tPREFIX\tSET IN"); err != nil {
		return err
	}
	for _, b := range out.Bindings {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", b.Agent, b.Stream, b.To, dash(b.Level), dash(b.Prefix), b.From); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// StandingAddCmd sets a standing binding.
type StandingAddCmd struct {
	NodeFlags
	Agent  string `arg:"" help:"Agent followed, on this node."`
	To     string `arg:"" help:"Destination: group:<name>, private:<subject>[@<channel>] or channel:local."`
	Stream string `help:"Stream followed: stdout (the agent's output) or stderr (its gate questions and notices)." default:"stdout" enum:"stdout,stderr"`
	Level  string `help:"Level the output is shown at, a number or log. Default: the node's level. stdout only."`
	Prefix string `help:"Shown before each message in place of the agent's name, at most 10 characters. stdout only."`
}

// Run implements bindings add.
func (c StandingAddCmd) Run() error { return c.run(os.Stdout) }

func (c StandingAddCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	b, err := cl.BindStanding(ctx, local.StandingJSON{Agent: c.Agent, Stream: c.Stream, To: c.To, Level: c.Level, Prefix: c.Prefix})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s follows %s's %s%s\n", b.To, b.Agent, b.Stream, levelSuffix(b.Level))
	return err
}

func levelSuffix(level string) string {
	if level == "" {
		return ""
	}
	return " at level " + level
}

// StandingRemoveCmd removes a standing binding set while the node runs.
type StandingRemoveCmd struct {
	NodeFlags
	Agent  string `arg:"" help:"Agent followed."`
	To     string `arg:"" help:"Destination of the binding."`
	Stream string `help:"Stream of the binding." default:"stdout" enum:"stdout,stderr"`
}

// Run implements bindings remove.
func (c StandingRemoveCmd) Run() error { return c.run(os.Stdout) }

func (c StandingRemoveCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	b, err := cl.UnbindStanding(ctx, local.StandingJSON{Agent: c.Agent, Stream: c.Stream, To: c.To})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s no longer follows %s's %s\n", b.To, b.Agent, b.Stream)
	return err
}
