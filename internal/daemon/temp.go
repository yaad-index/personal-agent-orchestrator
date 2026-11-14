package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/backend/claudecode"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/control"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/harvest"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh/directory"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/temp"
	"github.com/yaad-index/personal-agent-orchestrator/internal/workdir"
)

// locators maps a transcript format to where its backend writes the
// transcripts of sessions started in a directory (ADR-0011). A backend joins
// pao here, next to its reader.
var locators = map[string]func(home, workdir string) string{
	claudecode.Format: claudecode.TranscriptDir,
}

// locate is the recipe.Locator for this node.
func locate(format, wd string) (string, bool) {
	f, ok := locators[format]
	if !ok {
		return "", false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	return f(home, wd), true
}

// checkProfile refuses a profile this node has no reader for.
func checkProfile(p core.Profile) error {
	if _, ok := readers[p.Transcript.Format]; !ok {
		return fmt.Errorf("unknown transcript format %q", p.Transcript.Format)
	}
	return nil
}

// tempAPI is the operator's view of the Manager and the harvest store, in the
// shape the local channel serves. The channel's bearer token identifies the
// operator, so every call is made as the operator.
type tempAPI struct {
	mgr   *temp.Manager
	store *harvest.Store
	// cmds sends the commands to other nodes; nil when this node is not in a mesh.
	cmds *meshCommands
	// dir is the master's directory of the mesh's agents; nil outside a mesh.
	dir *directory.Directory
}

// translated carries a domain error under the status-bearing sentinel the
// channel maps to a code, keeping the domain error's own words.
type translated struct {
	kind error
	err  error
}

func (t translated) Error() string        { return t.err.Error() }
func (t translated) Is(target error) bool { return target == t.kind }
func (t translated) Unwrap() error        { return t.err }

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, temp.ErrNotFound), errors.Is(err, temp.ErrNoRecipe), errors.Is(err, harvest.ErrNotFound):
		return translated{local.ErrNotFound, err}
	case errors.Is(err, temp.ErrBadRequest), errors.Is(err, workdir.ErrBadSource):
		return translated{local.ErrBadRequest, err}
	case errors.Is(err, registry.ErrDuplicate), errors.Is(err, workdir.ErrExists):
		return translated{local.ErrConflict, err}
	case errors.Is(err, temp.ErrDenied):
		return translated{local.ErrForbidden, err}
	}
	if _, notReady := control.AsReadinessError(err); notReady {
		return translated{local.ErrNotReady, err}
	}
	return err
}

func (a tempAPI) Spawn(ctx context.Context, recipe, name, channel, node string) (string, error) {
	if node != "" {
		if a.cmds == nil {
			return "", translated{local.ErrBadRequest, errors.New("this node is not in a mesh, so it has no other node to start an agent on")}
		}
		switch {
		case a.cmds.node.Knows(node):
			return a.cmds.Spawn(ctx, node, recipe, name, channel)
		case node != a.cmds.self:
			return "", translated{local.ErrBadRequest, fmt.Errorf("no node %q in the mesh", node)}
		}
	}
	p, err := a.mgr.Spawn(ctx, temp.SpawnRequest{Recipe: recipe, Name: name, Channel: channel, Requester: access.Operator})
	return p.Name, translate(err)
}

func (a tempAPI) Kill(ctx context.Context, name string) (local.KillJSON, error) {
	if a.dir != nil {
		// A name this node holds is its own; one held elsewhere is killed there.
		if !a.holds(name) {
			if e, ok := a.dir.Lookup(name); ok {
				return a.cmds.Kill(ctx, e)
			}
		}
	}
	res, err := a.mgr.Kill(ctx, temp.KillRequest{Name: name, Requester: access.Operator})
	if err != nil {
		return local.KillJSON{}, translate(err)
	}
	return local.KillJSON{Harvest: res.Harvest, WorkdirKept: res.WorkdirKept}, nil
}

func (a tempAPI) holds(name string) bool {
	for _, s := range a.mgr.List() {
		if s.Name == name {
			return true
		}
	}
	return false
}

func (a tempAPI) Agents() []local.AgentJSON {
	var out []local.AgentJSON
	for _, s := range a.mgr.List() {
		out = append(out, local.AgentJSON{
			Name: s.Name, Recipe: s.Recipe, Channel: s.Channel, State: s.State.String(),
			Flag: string(s.Flag), Created: s.Created, LastActivity: s.LastActivity,
		})
	}
	if a.dir != nil {
		for _, e := range a.dir.All() {
			if !e.Agent.GetTemporary() {
				continue
			}
			out = append(out, local.AgentJSON{
				Name: e.Agent.GetName(), Recipe: e.Agent.GetRecipe(), Channel: e.Agent.GetChannel(),
				State: strings.ToLower(strings.TrimPrefix(e.Agent.GetState().String(), "AGENT_STATE_")), Flag: e.Agent.GetFlag(), Node: e.Node,
			})
		}
	}
	return out
}

// remote says whether a harvest id names one on another node.
func (a tempAPI) remote(id string) (node, rest string, ok bool) {
	if a.cmds == nil {
		return "", id, false
	}
	node, rest = a.cmds.split(id)
	return node, rest, node != ""
}

func (a tempAPI) Harvests(ctx context.Context, node string) ([]harvest.Manifest, error) {
	if node != "" {
		switch {
		case a.cmds == nil:
			return nil, translated{local.ErrBadRequest, errors.New("this node is not in a mesh, so it has no other node to list the harvests of")}
		case a.cmds.node.Knows(node):
			return a.cmds.Harvests(ctx, node)
		case node != a.cmds.self:
			return nil, translated{local.ErrBadRequest, fmt.Errorf("no node %q in the mesh", node)}
		}
	}
	ms, err := a.store.List(access.Operator)
	return ms, translate(err)
}

func (a tempAPI) Harvest(ctx context.Context, id string) (harvest.Manifest, error) {
	if node, rest, ok := a.remote(id); ok {
		return a.cmds.Harvest(ctx, node, rest)
	}
	m, err := a.store.Get(access.Operator, id)
	return m, translate(err)
}

func (a tempAPI) HarvestFile(ctx context.Context, id, path string) ([]byte, error) {
	if node, rest, ok := a.remote(id); ok {
		return a.cmds.HarvestFile(ctx, node, rest, path)
	}
	b, err := a.store.Read(access.Operator, id, path)
	return b, translate(err)
}

func (a tempAPI) DeleteHarvest(ctx context.Context, id string) error {
	if node, rest, ok := a.remote(id); ok {
		return a.cmds.DeleteHarvest(ctx, node, rest)
	}
	return translate(a.store.Delete(access.Operator, id))
}
