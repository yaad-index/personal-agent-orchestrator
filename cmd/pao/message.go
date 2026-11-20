package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// MessageCmd sends one message to an agent as the operator and returns without
// waiting for an answer. The agent's output goes where its stream is bound.
type MessageCmd struct {
	NodeFlags
	Agent string `arg:"" help:"Agent to send to; on this node or, at the master, on another."`
	Text  string `arg:"" help:"The message. A /pao command is run as one and its answer printed."`
}

// Run implements message.
func (c MessageCmd) Run() error { return c.run(os.Stdout) }

func (c MessageCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	rep, err := cl.Post(ctx, "", c.Text, "name", c.Agent)
	if err != nil {
		return err
	}
	if rep.Reply != "" {
		if _, err := fmt.Fprintln(w, rep.Reply); err != nil {
			return err
		}
	}
	if rep.ID != "" {
		if _, err := fmt.Fprintln(w, rep.ID); err != nil {
			return err
		}
	}
	var failed []string
	for _, r := range rep.Results {
		if r.Error != "" {
			failed = append(failed, r.Agent+": "+r.Error)
		}
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}
