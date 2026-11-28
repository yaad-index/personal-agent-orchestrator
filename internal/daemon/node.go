package daemon

import (
	"runtime/debug"
	"slices"
	"strings"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/temp"
)

// nodeInfo is what the node is and the agents it holds, for the management
// UI's node page.
func nodeInfo(node string, reg *registry.Registry) func() local.NodeJSON {
	build := buildRevision()
	return func() local.NodeJSON {
		out := local.NodeJSON{Node: node, Build: build, Agents: []local.NodeAgentJSON{}}
		for _, a := range reg.All() {
			tags := slices.Clone(a.Profile.Tags)
			if tags == nil {
				tags = []string{}
			}
			out.Agents = append(out.Agents, local.NodeAgentJSON{
				Name: a.Profile.Name, Tags: tags, Backend: a.Profile.Backend,
				State: a.State.String(), Temporary: a.Temp != nil,
			})
		}
		slices.SortFunc(out.Agents, func(x, y local.NodeAgentJSON) int { return strings.Compare(x.Name, y.Name) })
		return out
	}
}

// buildRevision is the commit this binary was built from, marked when the tree
// had changes, or "unknown" when the build did not record one.
func buildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	var rev, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if rev == "" {
		return "unknown"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if modified == "true" {
		rev += "-dirty"
	}
	return rev
}

// questionsOf is the approval questions waiting on the node, for the UI.
func questionsOf(p *gate.Pending) func() []local.QuestionJSON {
	return func() []local.QuestionJSON {
		out := []local.QuestionJSON{}
		for _, w := range p.List() {
			out = append(out, local.QuestionJSON{ID: w.ID, Agent: w.Agent, Tool: w.Tool, Text: w.Text, Asked: w.Asked})
		}
		return out
	}
}

// recipesOf is the recipes a temporary agent can be spawned from, for the UI.
func recipesOf(m *temp.Manager) func() ([]local.RecipeJSON, error) {
	return func() ([]local.RecipeJSON, error) {
		out := []local.RecipeJSON{}
		if m == nil || m.Recipes == nil {
			return out, nil
		}
		set, err := m.Recipes()
		if err != nil {
			return nil, err
		}
		for _, n := range set.Names() {
			r, _ := set.Get(n)
			tags := slices.Clone(r.Profile.Tags)
			if tags == nil {
				tags = []string{}
			}
			out = append(out, local.RecipeJSON{Name: n, Description: r.Description, Tags: tags})
		}
		return out, nil
	}
}

// channelsOf is the node's channels for the UI: the local channel and each
// configured one, by name and kind, with its groups and named topics by name. Each field is
// copied by name, so nothing else of a channel's configuration reaches it.
func channelsOf(providers []config.Provider, chans []config.Channel) []local.ChannelJSON {
	out := []local.ChannelJSON{{Name: local.Source, Kind: local.Source, Groups: []string{}}}
	for _, c := range chans {
		ch := local.ChannelJSON{Name: c.Name, Kind: config.KindOf(providers, c), Groups: []string{}}
		for _, g := range c.Groups {
			// a group's named topics are destinations of their own
			ch.Groups = append(ch.Groups, g.Names()...)
		}
		out = append(out, ch)
	}
	return out
}
