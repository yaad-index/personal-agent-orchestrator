// Package registry holds the set of agents a node knows about: their profiles
// and their lifecycle state. It is the lookup the router resolves targets
// against.
package registry

import (
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

type entry struct {
	profile core.Profile
	state   core.State
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
		return fmt.Errorf("agent %q already registered", p.Name)
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
