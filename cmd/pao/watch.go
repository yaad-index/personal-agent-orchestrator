package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
)

// WatchCmd follows an agent's output on demand, on this node or, at the master, on
// another node, until it is interrupted.
type WatchCmd struct {
	NodeFlags
	Agent   string `arg:"" help:"Agent to watch; on this node or, at the master, on another."`
	Level   int    `default:"1" help:"Verbosity to show it at."`
	Entries int    `help:"How many recent entries to show first. Default: the node's window."`
}

// Run implements watch.
func (c WatchCmd) Run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return c.run(ctx, os.Stdout)
}

func (c WatchCmd) run(ctx context.Context, w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	live := false
	var werr error
	err = cl.Watch(ctx, c.Agent, c.Level, c.Entries, func(l local.WatchLineJSON) {
		if werr != nil {
			return
		}
		if !l.History && !live {
			live = true
			_, werr = fmt.Fprintf(w, "-- %s, live from here --\n", c.Agent)
		}
		if werr == nil {
			_, werr = fmt.Fprintf(w, "%s: %s\n", c.Agent, l.Text)
		}
	})
	if werr != nil {
		return werr
	}
	return err
}
