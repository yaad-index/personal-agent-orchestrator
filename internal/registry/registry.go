// Package registry holds the set of agents a node knows about: their profiles
// and their lifecycle state. It is the lookup the router resolves targets
// against.
package registry

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// ErrDuplicate is returned when a name is already registered.
var ErrDuplicate = errors.New("agent already registered")

// ErrNotTemporary is returned when a temporary-agent operation names an agent
// that is not one. Agents declared in configuration are never removed.
var ErrNotTemporary = errors.New("agent is not a temporary agent")

// TempInfo is what pao tracks about a temporary agent beyond its profile
// (ADR-0006 point 3).
type TempInfo struct {
	// Created is when the agent was spawned.
	Created time.Time
	// Channel names the channel the agent's stream is bound to at launch.
	Channel string
	// Recipe names the recipe the agent was spawned from, if any.
	Recipe string
}

type entry struct {
	profile core.Profile
	state   core.State

	temp *TempInfo
	// lastActive is the latest time a message reached the agent or the agent
	// produced output; received is whether a message has ever reached it.
	lastActive time.Time
	received   bool
}

// Registry is safe for concurrent use.
type Registry struct {
	mu     sync.RWMutex
	agents map[string]*entry
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{agents: make(map[string]*entry)}
}

// Add registers a profile in the Stopped state. A name may be registered once.
func (r *Registry) Add(p core.Profile) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.agents[p.Name]; ok {
		return fmt.Errorf("%w: %q", ErrDuplicate, p.Name)
	}
	p.Tags = slices.Clone(p.Tags)
	r.agents[p.Name] = &entry{profile: p}
	return nil
}

// Lookup returns the profile and state of the named agent.
func (r *Registry) Lookup(name string) (core.Profile, core.State, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.agents[name]
	if !ok {
		return core.Profile{}, core.Stopped, false
	}
	return e.profile, e.state, true
}

// ByTag returns every profile carrying tag, ordered by name.
func (r *Registry) ByTag(tag string) []core.Profile {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []core.Profile
	for _, e := range r.agents {
		if e.profile.HasTag(tag) {
			out = append(out, e.profile)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SetState records the lifecycle state of the named agent.
func (r *Registry) SetState(name string, s core.State) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.agents[name]
	if !ok {
		return fmt.Errorf("agent %q not registered", name)
	}
	e.state = s
	return nil
}

// AddTemp registers a temporary agent in the Stopped state. Its name must not
// collide with any registered agent.
func (r *Registry) AddTemp(p core.Profile, info TempInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.agents[p.Name]; ok {
		return fmt.Errorf("%w: %q", ErrDuplicate, p.Name)
	}
	p.Tags = slices.Clone(p.Tags)
	r.agents[p.Name] = &entry{profile: p, temp: &info, lastActive: info.Created}
	return nil
}

// Remove unregisters a temporary agent. It is the registry's half of an
// explicit kill (ADR-0006 point 3): nothing calls it on a timer. An agent
// declared in configuration is never removed.
func (r *Registry) Remove(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.agents[name]
	if !ok {
		return fmt.Errorf("agent %q not registered", name)
	}
	if e.temp == nil {
		return fmt.Errorf("remove %q: %w", name, ErrNotTemporary)
	}
	delete(r.agents, name)
	return nil
}

// Touch records activity on the named agent at time at: a message delivered to
// it (inbound) or output it produced. Touching an agent that is not
// registered, such as one just removed, is a no-op.
func (r *Registry) Touch(name string, at time.Time, inbound bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.agents[name]
	if !ok {
		return
	}
	if at.After(e.lastActive) {
		e.lastActive = at
	}
	if inbound {
		e.received = true
	}
}

// Thresholds say how long an agent may go without activity before it is
// flagged. They only flag; nothing is ever killed on them (ADR-0006 point 3).
type Thresholds struct {
	// UnusedAfter is how long after spawning an agent that never received a
	// message is flagged unused.
	UnusedAfter time.Duration
	// IdleAfter is how long an agent that has been used may be quiet before it
	// is flagged idle.
	IdleAfter time.Duration
}

// DefaultThresholds are the flag thresholds when none are configured.
var DefaultThresholds = Thresholds{UnusedAfter: 10 * time.Minute, IdleAfter: 30 * time.Minute}

// Flag is the visible status of a temporary agent.
type Flag string

const (
	// FlagActive means the agent is within its thresholds.
	FlagActive Flag = "active"
	// FlagIdle means a used agent has been quiet for IdleAfter.
	FlagIdle Flag = "idle"
	// FlagUnused means an agent never given a message has existed UnusedAfter.
	FlagUnused Flag = "unused"
)

// TempView is a snapshot of one temporary agent.
type TempView struct {
	Profile      core.Profile
	State        core.State
	Info         TempInfo
	LastActivity time.Time
	// Received is whether any message has ever been delivered to the agent.
	Received bool
}

// Flag computes the agent's status flag at now.
func (v TempView) Flag(now time.Time, th Thresholds) Flag {
	if !v.Received {
		if th.UnusedAfter > 0 && now.Sub(v.Info.Created) >= th.UnusedAfter {
			return FlagUnused
		}
		return FlagActive
	}
	if th.IdleAfter > 0 && now.Sub(v.LastActivity) >= th.IdleAfter {
		return FlagIdle
	}
	return FlagActive
}

// TempOf returns a snapshot of the named temporary agent. ok is false for an
// agent that is unknown or declared in configuration.
func (r *Registry) TempOf(name string) (TempView, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.agents[name]
	if !ok || e.temp == nil {
		return TempView{}, false
	}
	p := e.profile
	p.Tags = slices.Clone(p.Tags)
	return TempView{Profile: p, State: e.state, Info: *e.temp, LastActivity: e.lastActive, Received: e.received}, true
}

// Temps returns a snapshot of every temporary agent, ordered by name.
func (r *Registry) Temps() []TempView {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []TempView
	for _, e := range r.agents {
		if e.temp == nil {
			continue
		}
		p := e.profile
		p.Tags = slices.Clone(p.Tags)
		out = append(out, TempView{Profile: p, State: e.state, Info: *e.temp, LastActivity: e.lastActive, Received: e.received})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Profile.Name < out[j].Profile.Name })
	return out
}
