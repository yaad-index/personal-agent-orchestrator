package daemon

import (
	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mcpsrv"
)

// governorScoped is the node's authorizer with the governor bound to its role
// (ADR-0031 points 1 and 5).
//
//   - It may only read what matching a request needs: the agents and the
//     recipes. Every other action is refused it whatever its grants say: sending
//     to an agent, spawning, killing, lifecycle, watching output, reading a
//     harvest, and calling a proxied tool. So it cannot act on the node, or reach
//     an operation through pao's MCP surface in place of the operator's commit.
//   - A read is allowed only when the governor's own grants allow it and the
//     grants of the subject it serves do too, so a governor shared by the node's
//     operators shows no one more than they could see themselves. Serving no
//     request, it reads nothing.
//   - Its own proposal tool is the exception to both: it reads nothing, and it is
//     how the governor answers. It still needs the governor's own grant.
type governorScoped struct {
	inner    access.Authorizer
	governor string
	// requester is the access subject of whoever the governor is serving; false
	// when it serves no one.
	requester func() (access.Subject, bool)
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
	who, ok := g.requester()
	return ok && access.Allowed(g.inner, who, act, r)
}

// servedBy is the access subject of whoever the governor is serving, read from
// requester (comm.Layer.GovernorRequester). Only an operator's request reaches
// the governor, and an operator is the operator subject; one whose flag was taken
// away since is served nothing more. The local channel's sender is the operator
// by construction.
func servedBy(requester func() (subject, channel string, ok bool), isOperator func(string) bool) func() (access.Subject, bool) {
	return func() (access.Subject, bool) {
		subject, channel, ok := requester()
		if !ok || (channel != local.Source && !isOperator(subject)) {
			return access.Subject{}, false
		}
		return access.Operator, true
	}
}
