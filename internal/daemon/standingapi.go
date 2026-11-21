package daemon

import (
	"context"
	"fmt"
	"strconv"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh/directory"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
)

// standingAPI is the operator's control of the standing bindings
// (local.StandingAPI). An agent the operator may not reach is answered as one
// that does not exist (ADR-0012 point 4). Bindings are set on the node that holds
// the agent.
type standingAPI struct {
	auth access.Authorizer
	reg  *registry.Registry
	dir  *directory.Directory
	set  *bindingSet
}

// reach checks that agent is this node's and the operator may reach it.
func (a standingAPI) reach(agent string) error {
	e, remote, err := locateForOperator(a.auth, access.Watch, a.reg, a.dir, agent)
	if err != nil {
		return err
	}
	if remote {
		return translated{local.ErrBadRequest, fmt.Errorf("agent %q is on node %s: set its bindings there", agent, e.Node)}
	}
	return nil
}

func (a standingAPI) Bindings(_ context.Context, agent string) (local.StandingListJSON, error) {
	if agent != "" {
		if err := a.reach(agent); err != nil {
			return local.StandingListJSON{}, err
		}
	}
	var out local.StandingListJSON
	for _, b := range a.set.list(agent) {
		if agent == "" && a.reach(b.Agent) != nil {
			continue
		}
		out.Bindings = append(out.Bindings, standingJSON(b.Binding, b.From))
	}
	return out, nil
}

func (a standingAPI) Bind(_ context.Context, in local.StandingJSON) (local.StandingJSON, error) {
	if err := a.reach(in.Agent); err != nil {
		return local.StandingJSON{}, err
	}
	level, err := parseLevel(in.Level)
	if err != nil {
		return local.StandingJSON{}, translated{local.ErrBadRequest, err}
	}
	b, err := a.set.add(config.Binding{Agent: in.Agent, Stream: in.Stream, To: in.To, Level: level, Prefix: in.Prefix})
	if err != nil {
		return local.StandingJSON{}, translate(err)
	}
	return standingJSON(b, bindingFromRuntime), nil
}

func (a standingAPI) Unbind(_ context.Context, in local.StandingJSON) (local.StandingJSON, error) {
	if err := a.reach(in.Agent); err != nil {
		return local.StandingJSON{}, err
	}
	b, err := a.set.remove(in.Agent, in.Stream, in.To)
	if err != nil {
		return local.StandingJSON{}, translate(err)
	}
	return standingJSON(b, bindingFromRuntime), nil
}

// parseLevel reads a level as the config does: empty is the node's, else a
// number or "log".
func parseLevel(s string) (config.BindingLevel, error) {
	switch s {
	case "":
		return 0, nil
	case config.LevelLog:
		return config.BindingLevel(core.LevelLog), nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("level must be a number or %q", config.LevelLog)
	}
	return config.BindingLevel(n), nil
}

// levelText writes a level as parseLevel reads it.
func levelText(l config.BindingLevel) string {
	switch {
	case l == 0:
		return ""
	case int(l) == core.LevelLog:
		return config.LevelLog
	}
	return strconv.Itoa(int(l))
}

func standingJSON(b config.Binding, from string) local.StandingJSON {
	return local.StandingJSON{Agent: b.Agent, Stream: b.Stream, To: b.To, Level: levelText(b.Level), Prefix: b.Prefix, From: from}
}
