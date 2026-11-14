package main

import (
	"fmt"
	"io"
	"os"
)

// StreamCmd binds an agent's output to a channel, or clears the binding.
type StreamCmd struct {
	NodeFlags
	Agent   string `arg:"" help:"Agent whose output to bind; on this node or, at the master, on another."`
	Channel string `help:"Channel to send its output to, as the agent's node names it."`
	Clear   bool   `help:"Send its output to every channel again."`
	Level   int    `help:"Verbosity to render it at. Default: the node's level."`
}

// Run implements stream.
func (c StreamCmd) Run() error { return c.run(os.Stdout) }

func (c StreamCmd) run(w io.Writer) error {
	if c.Clear == (c.Channel != "") {
		return fmt.Errorf("give --channel to bind the output, or --clear to unbind it")
	}
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	if err := cl.SetStream(ctx, c.Agent, c.Channel, c.Level); err != nil {
		return err
	}
	if c.Clear {
		_, err = fmt.Fprintf(w, "%s: output goes to every channel\n", c.Agent)
	} else {
		_, err = fmt.Fprintf(w, "%s: output goes to %s\n", c.Agent, c.Channel)
	}
	return err
}

// ViewCmd groups the controls of how an agent's output is shown.
type ViewCmd struct {
	Pause     ViewPauseCmd     `cmd:"" help:"Hold an agent's output back from the view."`
	Resume    ViewResumeCmd    `cmd:"" help:"Show what a pause held, then live output."`
	Backtrack ViewBacktrackCmd `cmd:"" help:"Show an agent's recent past again, at a higher level."`
}

// ViewPauseCmd pauses the view.
type ViewPauseCmd struct {
	NodeFlags
	Agent string `arg:"" help:"Agent whose output to hold back."`
}

// Run implements view pause.
func (c ViewPauseCmd) Run() error { return c.run(os.Stdout) }

func (c ViewPauseCmd) run(w io.Writer) error { return doView(c.NodeFlags, w, c.Agent, "pause", 0, 0) }

// ViewResumeCmd resumes the view.
type ViewResumeCmd struct {
	NodeFlags
	Agent string `arg:"" help:"Agent whose output to show again."`
}

// Run implements view resume.
func (c ViewResumeCmd) Run() error { return c.run(os.Stdout) }

func (c ViewResumeCmd) run(w io.Writer) error {
	return doView(c.NodeFlags, w, c.Agent, "resume", 0, 0)
}

// ViewBacktrackCmd re-renders the recent past.
type ViewBacktrackCmd struct {
	NodeFlags
	Agent   string `arg:"" help:"Agent whose past to show."`
	Level   int    `required:"" help:"Verbosity to show it at."`
	Entries int    `help:"How many entries. Default: the node's window."`
}

// Run implements view backtrack.
func (c ViewBacktrackCmd) Run() error { return c.run(os.Stdout) }

func (c ViewBacktrackCmd) run(w io.Writer) error {
	return doView(c.NodeFlags, w, c.Agent, "backtrack", c.Level, c.Entries)
}

func doView(f NodeFlags, w io.Writer, agent, action string, level, entries int) error {
	cl, err := f.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	if err := cl.View(ctx, agent, action, level, entries); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s: %s done\n", agent, action)
	return err
}
