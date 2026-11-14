package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/yaad-index/personal-agent-orchestrator/internal/control"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
	"github.com/yaad-index/personal-agent-orchestrator/internal/temp"
)

// errTemporaryAgent is a lifecycle command on a temporary agent that only the
// temporary-agent commands may run: killing one destroys its workdir.
var errTemporaryAgent = errors.New("a temporary agent")

// temporaryAgentError is that refusal with its own words.
type temporaryAgentError string

func (e temporaryAgentError) Error() string      { return string(e) }
func (temporaryAgentError) Is(target error) bool { return target == errTemporaryAgent }

// lifecycle starts, stops and restarts a configured agent of this node. The
// operator's local routes and the master's commands to a node both come here.
type lifecycle struct {
	router *route.Router
	plane  *control.Plane
	reg    *registry.Registry
	follow follower
}

func (l lifecycle) find(name string) (core.Profile, registry.AgentView, error) {
	for _, a := range l.reg.All() {
		if a.Profile.Name == name {
			return a.Profile, a, nil
		}
	}
	return core.Profile{}, registry.AgentView{}, fmt.Errorf("%w: no agent %q on this node", temp.ErrNotFound, name)
}

// Start brings the agent up if it is not running. An agent that does not become
// ready is returned with a readiness error and stays, as for a first message.
func (l lifecycle) Start(ctx context.Context, name string) error {
	p, _, err := l.find(name)
	if err != nil {
		return err
	}
	_, err = l.router.Ensure(ctx, p)
	return err
}

// Stop ends the agent's session and marks it stopped. It keeps its place and starts
// again with the next message.
func (l lifecycle) Stop(ctx context.Context, name string) error {
	p, v, err := l.find(name)
	if err != nil {
		return err
	}
	if v.Temp != nil {
		return temporaryAgentError(fmt.Sprintf("%q is a temporary agent: kill it with `pao kill` (KillTemp between nodes), which harvests its outputs first", p.Name))
	}
	return l.stop(ctx, p)
}

// Restart stops the agent and starts it again.
func (l lifecycle) Restart(ctx context.Context, name string) error {
	p, v, err := l.find(name)
	if err != nil {
		return err
	}
	if v.Temp != nil {
		return temporaryAgentError(fmt.Sprintf("%q is a temporary agent: killing it destroys its workdir, so it is not restarted", p.Name))
	}
	if err := l.stop(ctx, p); err != nil {
		return err
	}
	_, err = l.router.Ensure(ctx, p)
	return err
}

func (l lifecycle) stop(ctx context.Context, p core.Profile) error {
	if err := l.plane.Kill(ctx, p); err != nil {
		return err
	}
	if err := l.reg.SetState(p.Name, core.Stopped); err != nil {
		return err
	}
	l.follow.Stop(p.Name)
	return nil
}
