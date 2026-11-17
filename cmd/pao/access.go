package main

import (
	"fmt"
	"io"
	"os"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
)

// AccessCmd groups the access-matrix commands (ADR-0005, ADR-0012).
type AccessCmd struct {
	Grants AccessGrantsCmd `cmd:"" help:"List the grants, of one subject or of all."`
	Grant  AccessGrantCmd  `cmd:"" help:"Let a subject reach an agent or a tool, one by name or all carrying a tag."`
	Revoke AccessRevokeCmd `cmd:"" help:"Take a grant away."`
}

// grantTarget is the resource half of a grant on the command line: exactly one
// agent, agent tag, tool or tool tag.
type grantTarget struct {
	Agent   string `help:"One agent, by name." xor:"target"`
	Tag     string `help:"Every agent carrying this tag." xor:"target"`
	Tool    string `help:"One tool on pao's MCP surface, by its pao-side name." xor:"target"`
	ToolTag string `name:"tool-tag" help:"Every such tool carrying this tag." xor:"target"`
}

func (t grantTarget) grant(subject string) (local.GrantJSON, error) {
	set := 0
	for _, v := range []string{t.Agent, t.Tag, t.Tool, t.ToolTag} {
		if v != "" {
			set++
		}
	}
	if set != 1 {
		return local.GrantJSON{}, fmt.Errorf("give exactly one of --agent, --tag, --tool and --tool-tag")
	}
	switch {
	case t.Tool != "" || t.ToolTag != "":
		return local.GrantJSON{Subject: subject, Kind: "tool", Name: t.Tool, Tag: t.ToolTag}, nil
	default:
		return local.GrantJSON{Subject: subject, Kind: "agent", Name: t.Agent, Tag: t.Tag}, nil
	}
}

func describeGrant(g local.GrantJSON) string {
	if g.Tag != "" {
		return fmt.Sprintf("%ss tagged %s", g.Kind, g.Tag)
	}
	return fmt.Sprintf("%s %s", g.Kind, g.Name)
}

// AccessGrantCmd adds a grant.
type AccessGrantCmd struct {
	NodeFlags
	Subject string `arg:"" help:"Who is granted: operator, or agent:<name>."`
	grantTarget
}

// Run implements access grant.
func (c AccessGrantCmd) Run() error { return c.run(os.Stdout) }

func (c AccessGrantCmd) run(w io.Writer) error {
	g, err := c.grant(c.Subject)
	if err != nil {
		return err
	}
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	ch, err := cl.AddGrant(ctx, g)
	if err != nil {
		return err
	}
	suffix := ""
	if !ch.Changed {
		suffix = " (already granted)"
	}
	_, err = fmt.Fprintf(w, "%s may reach %s%s\n", g.Subject, describeGrant(g), suffix)
	return err
}

// AccessRevokeCmd removes a grant.
type AccessRevokeCmd struct {
	NodeFlags
	Subject string `arg:"" help:"Whose grant: operator, or agent:<name>."`
	grantTarget
}

// Run implements access revoke.
func (c AccessRevokeCmd) Run() error { return c.run(os.Stdout) }

func (c AccessRevokeCmd) run(w io.Writer) error {
	g, err := c.grant(c.Subject)
	if err != nil {
		return err
	}
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	ch, err := cl.RemoveGrant(ctx, g)
	if err != nil {
		return err
	}
	suffix := ""
	if !ch.Changed {
		suffix = " (it was not granted)"
	}
	_, err = fmt.Fprintf(w, "%s no longer reaches %s%s\n", g.Subject, describeGrant(g), suffix)
	return err
}

// AccessGrantsCmd lists grants.
type AccessGrantsCmd struct {
	NodeFlags
	Subject string `help:"Only this subject's grants."`
}

// Run implements access grants.
func (c AccessGrantsCmd) Run() error { return c.run(os.Stdout) }

func (c AccessGrantsCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	gs, err := cl.Grants(ctx, c.Subject)
	if err != nil {
		return err
	}
	for _, g := range gs.Grants {
		if _, err := fmt.Fprintf(w, "%s\t%s\n", g.Subject, describeGrant(g)); err != nil {
			return err
		}
	}
	return nil
}
