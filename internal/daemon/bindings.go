package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"sync"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/identity"
	"github.com/yaad-index/personal-agent-orchestrator/internal/statefile"
)

// Where a standing binding was set.
const (
	bindingFromConfig  = "config"
	bindingFromRuntime = "runtime"
)

// listedBinding is a standing binding and where it was set.
type listedBinding struct {
	config.Binding
	From string
}

// bindingSet is the node's standing bindings (ADR-0029 points 3 and 8): those of
// the config, and those set while the node runs, from chat or the operator
// surface. The ones set while it runs are kept in a state file, so they outlive a
// restart. A config binding is not removed or set twice from outside the config.
type bindingSet struct {
	path   string
	layer  *comm.Layer
	known  config.BindingKnown
	config []config.Binding
	// subject reports whether a subject exists, for a private destination.
	subject func(name string) bool

	mu      sync.Mutex
	runtime []config.Binding
}

// loadBindingSet reads the bindings set at runtime from path, a missing file being
// none, and gives the layer every binding. A saved binding that no longer checks,
// such as one whose agent or group the config dropped, is dropped and said so in
// the log.
func loadBindingSet(path string, cfg *config.Config, layer *comm.Layer, subject func(string) bool, configured func(string) bool) (*bindingSet, error) {
	s := &bindingSet{path: path, layer: layer, config: cfg.Orchestrator.Bindings, subject: subject, known: knownFor(cfg, configured)}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("standing bindings: %w", err)
	default:
		var saved []config.Binding
		if err := json.Unmarshal(b, &saved); err != nil {
			return nil, fmt.Errorf("standing bindings: %s: %w", path, err)
		}
		for _, sb := range saved {
			if err := s.check(&sb); err != nil {
				slog.Info("standing binding dropped", "agent", sb.Agent, "stream", sb.Stream, "destination", sb.To, "why", err.Error())
				continue
			}
			s.runtime = append(s.runtime, sb)
		}
		if len(s.runtime) != len(saved) {
			if err := statefile.Write(path, s.runtime); err != nil {
				slog.Warn("standing bindings could not be rewritten", "error", err)
			}
		}
	}
	s.apply()
	return s, nil
}

// knownFor is what a binding set at runtime is checked against: the configured
// agents, channels and admitted groups, as the config's own bindings are.
func knownFor(cfg *config.Config, configured func(string) bool) config.BindingKnown {
	k := config.BindingKnown{
		// an agent added from the management UI after start is known too
		Agent:    configured,
		Groups:   map[string]bool{},
		Channels: map[string]bool{},
		Level:    cfg.Orchestrator.Level,
		Governor: cfg.Orchestrator.Governor.Agent,
	}
	for _, c := range cfg.Orchestrator.Channels {
		k.Channels[c.Name] = true
		for _, g := range c.Groups {
			for _, n := range g.Names() {
				k.Groups[n] = true
			}
		}
	}
	return k
}

// check validates a binding set at runtime: as the config checks one, and its
// private destination's subject must exist.
func (s *bindingSet) check(b *config.Binding) error {
	if err := config.CheckBinding(b, s.known); err != nil {
		return err
	}
	if d, _ := config.ParseDestination(b.To); d.Kind == config.DestPrivate && !s.subject(d.Name) {
		return fmt.Errorf("no subject is named %q", d.Name)
	}
	return nil
}

// add sets a binding. It is refused if it does not check, or if the agent is
// already bound to the destination on that stream, by the config or at runtime.
// On an error nothing is changed, on disk or in the layer.
func (s *bindingSet) add(b config.Binding) (config.Binding, error) {
	if err := s.check(&b); err != nil {
		return config.Binding{}, translated{local.ErrBadRequest, err}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.ContainsFunc(s.config, func(c config.Binding) bool { return c.Key() == b.Key() }) {
		return config.Binding{}, translated{local.ErrConflict, fmt.Errorf("agent %q is already bound to %s by the config", b.Agent, b.To)}
	}
	if slices.ContainsFunc(s.runtime, func(r config.Binding) bool { return r.Key() == b.Key() }) {
		return config.Binding{}, translated{local.ErrConflict, fmt.Errorf("agent %q is already bound to %s", b.Agent, b.To)}
	}
	next := append(slices.Clone(s.runtime), b)
	if err := statefile.Write(s.path, next); err != nil {
		return config.Binding{}, fmt.Errorf("keep the standing binding: %w", err)
	}
	s.runtime = next
	s.applyLocked()
	return b, nil
}

// remove drops a binding set at runtime. A config binding is refused: it is the
// config's to change. On an error nothing is changed.
func (s *bindingSet) remove(agent, stream, to string) (config.Binding, error) {
	if stream == "" {
		stream = config.StreamStdout
	}
	d, err := config.ParseDestination(to)
	if err != nil {
		return config.Binding{}, translated{local.ErrBadRequest, err}
	}
	key := config.Binding{Agent: agent, Stream: stream, To: d.String()}.Key()
	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.ContainsFunc(s.config, func(c config.Binding) bool { return c.Key() == key }) {
		return config.Binding{}, translated{local.ErrConflict, fmt.Errorf("agent %q is bound to %s by the config: change it there", agent, d)}
	}
	i := slices.IndexFunc(s.runtime, func(r config.Binding) bool { return r.Key() == key })
	if i < 0 {
		return config.Binding{}, translated{local.ErrNotFound, fmt.Errorf("agent %q has no %s binding to %s", agent, stream, d)}
	}
	gone := s.runtime[i]
	next := slices.Delete(slices.Clone(s.runtime), i, i+1)
	if err := statefile.Write(s.path, next); err != nil {
		return config.Binding{}, fmt.Errorf("drop the standing binding: %w", err)
	}
	s.runtime = next
	s.applyLocked()
	return gone, nil
}

// list returns the standing bindings, of one agent when agent is not empty: the
// config's first, then those set at runtime, each in the order it was set.
func (s *bindingSet) list(agent string) []listedBinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []listedBinding
	for _, set := range []struct {
		bs   []config.Binding
		from string
	}{{s.config, bindingFromConfig}, {s.runtime, bindingFromRuntime}} {
		for _, b := range set.bs {
			if agent == "" || b.Agent == agent {
				out = append(out, listedBinding{Binding: b, From: set.from})
			}
		}
	}
	return out
}

func (s *bindingSet) apply() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyLocked()
}

func (s *bindingSet) applyLocked() {
	s.layer.SetBindings(standingBindings(append(slices.Clone(s.config), s.runtime...)))
}

// subjectExists reports whether the identity store has a subject of that name.
func subjectExists(ids *identity.Store) func(string) bool {
	return func(name string) bool {
		subjects, err := ids.Subjects()
		if err != nil {
			return false
		}
		return slices.ContainsFunc(subjects, func(s identity.Subject) bool { return s.Name == name })
	}
}
