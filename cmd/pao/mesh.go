package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh"
)

// MeshCmd groups the commands on a node's place in a mesh.
type MeshCmd struct {
	Keygen MeshKeygenCmd `cmd:"" help:"Create this node's key and print the fingerprint to list in the mesh document."`
	Show   MeshShowCmd   `cmd:"" help:"Show this node's identity and the nodes the mesh document lists."`
	Status MeshStatusCmd `cmd:"" help:"Show a running node's view of the mesh: who is master, and which peers are up."`
}

// MeshKeygenCmd creates the node's key.
type MeshKeygenCmd struct {
	Config string `arg:"" type:"existingfile" help:"Path to the orchestrator config file."`
}

// Run implements mesh keygen.
func (c MeshKeygenCmd) Run() error { return c.run(os.Stdout) }

func (c MeshKeygenCmd) run(w io.Writer) error {
	cfg, err := config.LoadWithoutMesh(c.Config)
	if err != nil {
		return err
	}
	o := cfg.Orchestrator
	id, err := mesh.Generate(o.MeshKey, o.Node)
	if errors.Is(err, mesh.ErrKeyExists) {
		return fmt.Errorf("%w; a node's key is its identity in every other node's list, so it is not replaced here: move it aside first if you mean to rotate it", err)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "node:        %s\nkey:         %s\nfingerprint: %s\n\nList this node in the mesh document on every node with:\n  - id: %s\n    addr: <host:port other nodes dial>\n    fingerprint: %s\n",
		id.NodeID, o.MeshKey, id.Fingerprint, id.NodeID, id.Fingerprint)
	return err
}

// MeshShowCmd shows the node's identity and the mesh.
type MeshShowCmd struct {
	Config string `arg:"" type:"existingfile" help:"Path to the orchestrator config file."`
}

// Run implements mesh show.
func (c MeshShowCmd) Run() error { return c.run(os.Stdout) }

func (c MeshShowCmd) run(w io.Writer) error {
	cfg, err := config.Load(c.Config)
	if err != nil {
		return err
	}
	o := cfg.Orchestrator
	if cfg.Mesh == nil {
		_, err = fmt.Fprintf(w, "node %s is not part of a mesh (no mesh_file in the config)\n", o.Node)
		return err
	}
	id, err := mesh.Load(o.MeshKey, o.Node)
	if err != nil {
		return err
	}
	entry, _ := cfg.Mesh.Entry(o.Node)
	status := "matches the mesh document"
	if entry.Fingerprint != id.Fingerprint {
		status = "DOES NOT MATCH the mesh document (" + entry.Fingerprint + ")"
	}
	if _, err := fmt.Fprintf(w, "node:        %s\nlistens:     %s\nfingerprint: %s (%s)\n\nmesh:\n", o.Node, o.MeshListen, id.Fingerprint, status); err != nil {
		return err
	}
	for _, n := range cfg.Mesh.Nodes {
		mark := " "
		if n.ID == o.Node {
			mark = "*"
		}
		if _, err := fmt.Fprintf(w, " %s %-12s %-24s %s\n", mark, n.ID, n.Addr, n.Fingerprint); err != nil {
			return err
		}
	}
	return nil
}

// MeshStatusCmd shows a running node's view of the mesh.
type MeshStatusCmd struct{ NodeFlags }

// Run implements mesh status.
func (c MeshStatusCmd) Run() error { return c.run(os.Stdout) }

func (c MeshStatusCmd) run(w io.Writer) error {
	cl, err := c.client()
	if err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()
	st, err := cl.Mesh(ctx)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "node:   %s (%s)\nmaster: %s at epoch %d\n\n", st.Node, st.Role, st.Master, st.Epoch); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, p := range st.Peers {
		state := "DOWN"
		if p.Up {
			state = "up"
		}
		believes := "-"
		if p.Master != "" {
			believes = fmt.Sprintf("%s@%d", p.Master, p.Epoch)
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d agent(s)\t%s\n", p.ID, state, p.Role, believes, p.Agents, p.Addr); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(st.Conflicts) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "\nname conflicts (these agents are not addressable through the mesh):"); err != nil {
		return err
	}
	for _, c := range st.Conflicts {
		holder := "the master itself"
		if c.HeldBy != "" {
			holder = "node " + c.HeldBy
		}
		if _, err := fmt.Fprintf(w, "  %s on node %s: the name is held by %s\n", c.Name, c.Node, holder); err != nil {
			return err
		}
	}
	return nil
}
