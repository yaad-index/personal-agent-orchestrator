package daemon

import (
	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mcpsrv"
)

// governorScoped is the node's authorizer with the governor bound to its role.
//
//   - It may only read what matching a request needs: the agents and the
//     recipes. Every other action is refused it whatever its grants say: sending
//     to an agent, spawning, killing, lifecycle, watching output, reading a
//     harvest, and calling a proxied tool. So it cannot act on the node, or reach
//     an operation through pao's MCP surface in place of the operator's commit.
//   - A read is checked against the governor's own grants alone, whoever it is
//     serving: it is told what exists, not what one operator may use. Serving no
//     request, it reads nothing.
//   - Its own proposal tool is the exception to both: it reads nothing, and it is
//     how the governor answers. It still needs the governor's own grant.
type governorScoped struct {
	inner    access.Authorizer
	governor string
	// serving says whether the governor is serving a request now.
	serving func() bool
}

// governorReads are the actions matching needs: the governor's only ones.
var governorReads = map[access.Action]bool{access.List: true, access.ListRecipes: true}

// Allow implements access.Authorizer.
func (g governorScoped) Allow(s access.Subject, act access.Action, r access.Resource) bool {
	if !access.Allowed(g.inner, s, act, r) {
		return false
	}
	if s.Kind != access.KindAgent || s.Name != g.governor {
		return true
	}
	if act == access.CallTool && r.Kind == access.ResourceTool && r.Name == mcpsrv.ToolPropose {
		return true
	}
	if !governorReads[act] {
		return false
	}
	return g.serving()
}

// servesAnOperator says whether the governor is serving an operator's request
// now, read from requester (comm.Layer.GovernorRequester). Only an operator's
// request reaches the governor; one whose flag was taken away since is served
// nothing more. The local channel's sender is the operator by construction.
func servesAnOperator(requester func() (subject, channel string, ok bool), isOperator func(string) bool) func() bool {
	return func() bool {
		subject, channel, ok := requester()
		return ok && (channel == local.Source || isOperator(subject))
	}
}

// governorChannels lists the node's channels to the governor: the local channel
// and each configured one, by name and kind, with its groups by name and member
// policy. Each field is copied by name, so a credential, the variable that holds
// one, an adapter option or a group's platform id never reaches it. Serving no
// request, it lists nothing.
func governorChannels(providers []config.Provider, chans []config.Channel, serving func() bool) func() []mcpsrv.ChannelInfo {
	all := []mcpsrv.ChannelInfo{{Name: local.Source, Kind: local.Source}}
	for _, c := range chans {
		info := mcpsrv.ChannelInfo{Name: c.Name, Kind: config.KindOf(providers, c)}
		for _, g := range c.Groups {
			info.Groups = append(info.Groups, mcpsrv.GroupInfo{Name: g.Name, Members: g.Members})
		}
		all = append(all, info)
	}
	return func() []mcpsrv.ChannelInfo {
		if !serving() {
			return nil
		}
		return all
	}
}
