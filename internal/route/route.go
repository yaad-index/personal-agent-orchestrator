// Package route resolves an addressed input to its agents and delivers it
// (ADR-0003). An input names an agent or a tag; a tag fans out to every agent
// carrying it. A resolved agent that is running receives the input; one that is
// not is launched first.
package route

import (
	"context"
	"fmt"
	"sync"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
)

// Result is the outcome for one resolved agent.
type Result struct {
	Agent    string
	Launched bool
	Err      error
}

// Router routes inputs against a registry. Launching and delivering are
// collaborators so the terminal driver stays swappable (ADR-0002).
type Router struct {
	reg       *registry.Registry
	launcher  core.Launcher
	deliverer core.Deliverer

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// New returns a Router.
func New(reg *registry.Registry, l core.Launcher, d core.Deliverer) *Router {
	return &Router{reg: reg, launcher: l, deliverer: d, locks: make(map[string]*sync.Mutex)}
}

// Route delivers m to every agent the target resolves to and returns one
// Result per agent. A failure for one agent does not stop the others; the
// returned error is non-nil only when the target itself cannot be resolved.
func (r *Router) Route(ctx context.Context, t core.Target, m core.Message) ([]Result, error) {
	profiles, err := r.Resolve(t)
	if err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, r.deliver(ctx, p, m))
	}
	return out, nil
}

// Resolve returns the agents a target addresses, without delivering anything.
func (r *Router) Resolve(t core.Target) ([]core.Profile, error) {
	if t.Value == "" {
		return nil, core.ErrNoTarget
	}
	switch t.Kind {
	case core.ByName:
		p, _, ok := r.reg.Lookup(t.Value)
		if !ok {
			return nil, fmt.Errorf("%w: agent %q", core.ErrNoMatch, t.Value)
		}
		return []core.Profile{p}, nil
	case core.ByTag:
		ps := r.reg.ByTag(t.Value)
		if len(ps) == 0 {
			return nil, fmt.Errorf("%w: tag %q", core.ErrNoMatch, t.Value)
		}
		return ps, nil
	default:
		return nil, core.ErrNoTarget
	}
}

func (r *Router) deliver(ctx context.Context, p core.Profile, m core.Message) Result {
	res := Result{Agent: p.Name}

	launched, err := r.ensureRunning(ctx, p)
	if err != nil {
		res.Err = err
		return res
	}
	res.Launched = launched

	if err := r.deliverer.Deliver(ctx, p, m); err != nil {
		res.Err = fmt.Errorf("deliver to %q: %w", p.Name, err)
	}
	return res
}

// ensureRunning launches the agent if it is not running. It serializes per
// agent, so two inputs racing for a stopped agent start it once.
func (r *Router) ensureRunning(ctx context.Context, p core.Profile) (bool, error) {
	lock := r.lockFor(p.Name)
	lock.Lock()
	defer lock.Unlock()

	if _, state, _ := r.reg.Lookup(p.Name); state == core.Running {
		return false, nil
	}
	if err := r.launcher.Launch(ctx, p); err != nil {
		return false, fmt.Errorf("launch %q: %w", p.Name, err)
	}
	if err := r.reg.SetState(p.Name, core.Running); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Router) lockFor(name string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.locks[name]
	if !ok {
		l = &sync.Mutex{}
		r.locks[name] = l
	}
	return l
}
