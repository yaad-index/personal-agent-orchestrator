package main

import (
	"fmt"
	"io"
	"os"
)

// LogCmd pauses and resumes an agent's log channel (ADR-0058 point 7).
type LogCmd struct {
	Pause  LogPauseCmd  `cmd:"" help:"Stop sending an agent's output to its log channel; what it says meanwhile is not sent later."`
	Resume LogResumeCmd `cmd:"" help:"Send an agent's output to its log channel again."`
}

// LogPauseCmd pauses an agent's log channel.
type LogPauseCmd struct {
	NodeFlags
	Agent string `arg:"" help:"Agent whose log channel to pause."`
}

// Run implements log pause.
func (c LogPauseCmd) Run() error { return pauseLog(c.NodeFlags, os.Stdout, c.Agent, true) }

// LogResumeCmd resumes an agent's log channel.
type LogResumeCmd struct {
	NodeFlags
	Agent string `arg:"" help:"Agent whose log channel to resume."`
}

// Run implements log resume.
func (c LogResumeCmd) Run() error { return pauseLog(c.NodeFlags, os.Stdout, c.Agent, false) }

func pauseLog(f NodeFlags, w io.Writer, agent string, pause bool) error {
	cl, err := f.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	if err := cl.PauseLog(ctx, agent, pause); err != nil {
		return err
	}
	word := "resumed"
	if pause {
		word = "paused"
	}
	_, err = fmt.Fprintf(w, "%s's log channel is %s\n", agent, word)
	return err
}
