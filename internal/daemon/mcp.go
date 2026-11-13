package daemon

import (
	"context"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/harvest"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mcpsrv"
	"github.com/yaad-index/personal-agent-orchestrator/internal/recipe"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/temp"
)

// mcpOps is the node's operations as the MCP server sees them: the registry
// and the recipes, read as they are when a tool is called.
//
// Spawning, killing and reading harvests go to the same Manager and harvest
// store as the operator HTTP surface, as the subject the connection resolved
// to, so the access seam and the approval gate decide here exactly as there.
type mcpOps struct {
	reg        *registry.Registry
	thresholds registry.Thresholds
	recipes    func() (*recipe.Set, error)
	mgr        *temp.Manager
	store      *harvest.Store
}

func (o mcpOps) Agents() []mcpsrv.AgentInfo {
	now := time.Now()
	var out []mcpsrv.AgentInfo
	for _, a := range o.reg.All() {
		info := mcpsrv.AgentInfo{
			Name: a.Profile.Name, Tags: a.Profile.Tags, Backend: a.Profile.Backend, State: a.State.String(),
		}
		if info.Tags == nil {
			info.Tags = []string{}
		}
		if a.Temp != nil {
			info.Temporary = true
			info.Recipe = a.Temp.Info.Recipe
			info.Channel = a.Temp.Info.Channel
			info.Flag = string(a.Temp.Flag(now, o.thresholds))
		}
		out = append(out, info)
	}
	return out
}

func (o mcpOps) Recipes() ([]mcpsrv.RecipeInfo, error) {
	set, err := o.recipes()
	if err != nil {
		return nil, err
	}
	var out []mcpsrv.RecipeInfo
	for _, n := range set.Names() {
		r, _ := set.Get(n)
		out = append(out, mcpsrv.RecipeInfo{Name: n, Description: r.Description})
	}
	return out, nil
}

func (o mcpOps) Spawn(ctx context.Context, who access.Subject, rec, name, channel string) (string, error) {
	p, err := o.mgr.Spawn(ctx, temp.SpawnRequest{Recipe: rec, Name: name, Channel: channel, Requester: who})
	return p.Name, err
}

func (o mcpOps) Kill(ctx context.Context, who access.Subject, name string) (mcpsrv.KillInfo, error) {
	res, err := o.mgr.Kill(ctx, temp.KillRequest{Name: name, Requester: who})
	if err != nil {
		return mcpsrv.KillInfo{}, err
	}
	m := res.Harvest
	summary := m.Summary()
	if res.WorkdirKept {
		summary += "\nthe workdir was kept: the harvest was incomplete"
	}
	return mcpsrv.KillInfo{
		HarvestID: m.ID, Complete: m.Complete, WorkdirKept: res.WorkdirKept,
		Files: len(m.Files), Bytes: m.Bytes, Summary: summary,
	}, nil
}

func harvestInfo(m harvest.Manifest, withFiles bool) mcpsrv.HarvestInfo {
	h := mcpsrv.HarvestInfo{
		ID: m.ID, Agent: m.Agent, Tags: m.Tags, Requester: m.Requester,
		Time: m.Time.Format(time.RFC3339), Complete: m.Complete, Bytes: m.Bytes,
	}
	if h.Tags == nil {
		h.Tags = []string{}
	}
	if withFiles {
		for _, f := range m.Files {
			h.Files = append(h.Files, mcpsrv.FileInfo{Path: f.Path, Size: f.Size})
		}
	}
	return h
}

func (o mcpOps) Harvests(who access.Subject) ([]mcpsrv.HarvestInfo, error) {
	ms, err := o.store.List(who)
	if err != nil {
		return nil, err
	}
	var out []mcpsrv.HarvestInfo
	for _, m := range ms {
		out = append(out, harvestInfo(m, false))
	}
	return out, nil
}

func (o mcpOps) Harvest(who access.Subject, id string) (mcpsrv.HarvestInfo, error) {
	m, err := o.store.Get(who, id)
	if err != nil {
		return mcpsrv.HarvestInfo{}, err
	}
	return harvestInfo(m, true), nil
}

func (o mcpOps) HarvestFile(who access.Subject, id, path string) ([]byte, error) {
	return o.store.Read(who, id, path)
}
