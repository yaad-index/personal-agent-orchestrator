package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
)

// NodeFlags say how to reach a running node's local channel.
type NodeFlags struct {
	URL      string `help:"Base URL of the node's local channel." default:"http://127.0.0.1:7744" env:"PAO_URL"`
	TokenEnv string `name:"token-env" help:"Environment variable holding the channel token." default:"PAO_TOKEN"`
}

func (f NodeFlags) client() (*local.Client, error) {
	token := os.Getenv(f.TokenEnv)
	if token == "" {
		return nil, fmt.Errorf("environment variable %s is empty", f.TokenEnv)
	}
	return &local.Client{URL: f.URL, Token: token}, nil
}

func opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Minute)
}

// SpawnCmd starts a temporary agent from a recipe.
type SpawnCmd struct {
	NodeFlags
	Recipe  string `arg:"" help:"Recipe to spawn from (see 'pao recipes list')."`
	Name    string `help:"Name for the agent. Default: generated from the recipe."`
	Channel string `help:"Channel its stream is bound to. Default: the node's only channel."`
}

// Run implements spawn.
func (c SpawnCmd) Run() error { return c.run(os.Stdout) }

func (c SpawnCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	name, err := cl.Spawn(ctx, c.Recipe, c.Name, c.Channel)
	if name != "" {
		// Even when it did not become ready the agent exists: say its name, so
		// it can be inspected or killed.
		if _, werr := fmt.Fprintln(w, name); werr != nil && err == nil {
			err = werr
		}
	}
	return err
}

// KillCmd tears a temporary agent down, harvesting its outputs first.
type KillCmd struct {
	NodeFlags
	Name string `arg:"" help:"Temporary agent to kill."`
}

// Run implements kill.
func (c KillCmd) Run() error { return c.run(os.Stdout) }

func (c KillCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	res, err := cl.Kill(ctx, c.Name)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, res.Harvest.Summary())
	if err == nil && res.WorkdirKept {
		_, err = fmt.Fprintln(w, "the workdir was kept: the harvest was incomplete")
	}
	return err
}

// AgentsCmd lists the temporary agents and their status flags.
type AgentsCmd struct {
	NodeFlags
}

// Run implements agents.
func (c AgentsCmd) Run() error { return c.run(os.Stdout) }

func (c AgentsCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	agents, err := cl.Agents(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, a := range agents {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.Name, a.Flag, a.State, a.Recipe, a.Channel); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// HarvestsCmd groups the commands on harvested outputs.
type HarvestsCmd struct {
	List HarvestsListCmd `cmd:"" help:"List harvests."`
	Show HarvestsShowCmd `cmd:"" help:"Show one harvest and its files."`
	Cat  HarvestsCatCmd  `cmd:"" help:"Print one harvested file."`
	Rm   HarvestsRmCmd   `cmd:"" help:"Delete a harvest."`
}

// HarvestsListCmd lists harvests, newest first.
type HarvestsListCmd struct{ NodeFlags }

// Run implements harvests list.
func (c HarvestsListCmd) Run() error { return c.run(os.Stdout) }

func (c HarvestsListCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	hs, err := cl.Harvests(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, h := range hs {
		state := "complete"
		if !h.Complete {
			state = "INCOMPLETE"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%d file(s)\t%s\t%s\n", h.ID, len(h.Files), state, h.Requester); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// HarvestsShowCmd shows one harvest.
type HarvestsShowCmd struct {
	NodeFlags
	ID string `arg:"" help:"Harvest id."`
}

// Run implements harvests show.
func (c HarvestsShowCmd) Run() error { return c.run(os.Stdout) }

func (c HarvestsShowCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	m, err := cl.Harvest(ctx, c.ID)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\nrequested by %s at %s\n", m.Summary(), m.Requester, m.Time.Format(time.RFC3339))
	return err
}

// HarvestsCatCmd prints one harvested file to standard output.
type HarvestsCatCmd struct {
	NodeFlags
	ID   string `arg:"" help:"Harvest id."`
	Path string `arg:"" help:"File path inside the harvest, as 'harvests show' lists it."`
}

// Run implements harvests cat.
func (c HarvestsCatCmd) Run() error { return c.run(os.Stdout) }

func (c HarvestsCatCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	b, err := cl.HarvestFile(ctx, c.ID, c.Path)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// HarvestsRmCmd deletes a harvest.
type HarvestsRmCmd struct {
	NodeFlags
	ID string `arg:"" help:"Harvest id."`
}

// Run implements harvests rm.
func (c HarvestsRmCmd) Run() error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	return cl.DeleteHarvest(ctx, c.ID)
}
