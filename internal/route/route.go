// Package route resolves an addressed input to its agents and delivers it
// (ADR-0003). An input names an agent or a tag; a tag fans out to every agent
// carrying it. A resolved agent that is running receives the input; one that is
// not is launched first.
package route

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
)

// Result is the outcome for one resolved agent.
type Result struct {
	Agent    string
	Launched bool
	Err      error
}

// Remote is the agents on other nodes of a mesh, as the master knows them, and the
// way to hand one a message. The router asks it after its own registry.
type Remote interface {
	// Lookup returns the remote agent a name addresses.
	Lookup(name string) (core.Profile, bool)
	// ByTag returns the remote agents carrying a tag.
	ByTag(tag string) []core.Profile
	// Deliver hands the agent a message on its node and reports whether it had to be
	// started to receive it.
	Deliver(ctx context.Context, p core.Profile, m core.Message) (launched bool, err error)
}

// Router routes inputs against a registry. Launching and delivering are
// collaborators so the terminal driver stays swappable (ADR-0002).
type Router struct {
	reg       *registry.Registry
	launcher  core.Launcher
	deliverer core.Deliverer

	now func() time.Time

	remote Remote

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// Option tunes a Router.
type Option func(*Router)

// WithClock replaces the clock used to stamp agent activity.
func WithClock(now func() time.Time) Option { return func(r *Router) { r.now = now } }

// SetRemote lets the router address agents on other nodes. Call it before the
// router is used.
func (r *Router) SetRemote(remote Remote) { r.remote = remote }

// New returns a Router.
func New(reg *registry.Registry, l core.Launcher, d core.Deliverer, opts ...Option) *Router {
	r := &Router{reg: reg, launcher: l, deliverer: d, now: time.Now, locks: make(map[string]*sync.Mutex)}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Route delivers m to every agent the target resolves to and returns one
// Result per agent. A failure for one agent does not stop the others; the
// returned error is non-nil only when the target itself cannot be resolved.
func (r *Router) Route(ctx context.Context, t core.Target, m core.Message) ([]Result, error) {
	profiles, err := r.Resolve(t)
	if err != nil {
		return nil, err
	}
	return r.Deliver(ctx, profiles, m), nil
}

// Deliver delivers m to exactly the given agents, launching any that are not
// running, and returns one Result per agent. A failure for one does not stop
// the others.
func (r *Router) Deliver(ctx context.Context, profiles []core.Profile, m core.Message) []Result {
	out := make([]Result, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, r.deliver(ctx, p, m))
	}
	return out
}

// NoMatch is the error for a target that addresses no agent. Whoever filters
// the agents a target resolves to uses it when nothing is left, so that an
// agent the caller may not address and one that does not exist are reported
// in exactly the same words.
func NoMatch(t core.Target) error {
	if t.Kind == core.ByTag {
		return fmt.Errorf("%w: tag %q", core.ErrNoMatch, t.Value)
	}
	return fmt.Errorf("%w: agent %q", core.ErrNoMatch, t.Value)
}

// Resolve returns the agents a target addresses, without delivering anything.
func (r *Router) Resolve(t core.Target) ([]core.Profile, error) {
	if t.Value == "" {
		return nil, core.ErrNoTarget
	}
	switch t.Kind {
	case core.ByName:
		if p, _, ok := r.reg.Lookup(t.Value); ok {
			return []core.Profile{p}, nil
		}
		if r.remote != nil {
			if p, ok := r.remote.Lookup(t.Value); ok {
				return []core.Profile{p}, nil
			}
		}
		return nil, NoMatch(t)
	case core.ByTag:
		ps := r.reg.ByTag(t.Value)
		if r.remote != nil {
			ps = append(ps, r.remote.ByTag(t.Value)...)
			sort.Slice(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
		}
		if len(ps) == 0 {
			return nil, NoMatch(t)
		}
		return ps, nil
	default:
		return nil, core.ErrNoTarget
	}
}

func (r *Router) deliver(ctx context.Context, p core.Profile, m core.Message) Result {
	res := Result{Agent: p.Name}
	if p.Node != "" {
		// An agent on another node starts itself when the message reaches it; there
		// is no launch lock to take here and no activity of ours to note.
		if r.remote == nil {
			res.Err = fmt.Errorf("deliver to %q on node %q: no way to reach other nodes", p.Name, p.Node)
			return res
		}
		launched, err := r.remote.Deliver(ctx, p, m)
		res.Launched = launched
		if err != nil {
			res.Err = fmt.Errorf("deliver to %q on node %q: %w", p.Name, p.Node, err)
		}
		return res
	}

	launched, err := r.ensureRunning(ctx, p)
	if err != nil {
		res.Err = err
		return res
	}
	res.Launched = launched

	if err := r.deliverer.Deliver(ctx, p, m); err != nil {
		res.Err = fmt.Errorf("deliver to %q: %w", p.Name, err)
		return res
	}
	r.reg.Touch(p.Name, r.now(), true)
	return res
}

// Ensure launches the agent if it is not running and reports whether it did. It
// is the router's launch path without a message, for a caller that brings an
// agent up on its own (spawning a temporary agent).
func (r *Router) Ensure(ctx context.Context, p core.Profile) (bool, error) {
	return r.ensureRunning(ctx, p)
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
