package daemon

import (
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/mcpsrv"
	"github.com/yaad-index/personal-agent-orchestrator/internal/recipe"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
)

// mcpOps is the node's operations as the MCP server sees them: the registry
// and the recipes, read as they are when a tool is called.
type mcpOps struct {
	reg        *registry.Registry
	thresholds registry.Thresholds
	recipes    func() (*recipe.Set, error)
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
