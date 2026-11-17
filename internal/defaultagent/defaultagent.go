// Package defaultagent holds each channel's default agent (ADR-0023 points 3 and
// 5): the agent that answers a chat inbound naming no agent. It is node state the
// operator changes while the node runs, kept in one versioned document in the state
// directory so it survives a restart.
//
// The binding is one fact seen from both ends: a channel has at most one default
// agent, and an agent is the default of at most one channel (ADR-0023 point 3).
// Making an agent the default of a channel therefore moves it off any channel it was
// the default of.
package defaultagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/yaad-index/personal-agent-orchestrator/internal/statefile"
)

// Document is the stored form: the bindings and a version that increases with each
// change.
type Document struct {
	Version  uint64            `json:"version"`
	Defaults map[string]string `json:"defaults"` // channel to agent
}

// Change is what a Set or Clear did.
type Change struct {
	// Previous is the agent the channel had before, empty if it had none.
	Previous string
	// Left is the channel the agent stopped being the default of, empty if none.
	Left string
}

// Store is safe for concurrent use.
type Store struct {
	path string

	mu  sync.RWMutex
	doc Document
}

// Open reads the document at path; a missing file is an empty store.
func Open(path string) (*Store, error) {
	s := &Store{path: path, doc: Document{Defaults: map[string]string{}}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var d Document
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	seen := map[string]string{}
	for ch, agent := range d.Defaults {
		if ch == "" || agent == "" {
			return nil, fmt.Errorf("%s: an entry has an empty channel or agent", path)
		}
		if other, dup := seen[agent]; dup {
			return nil, fmt.Errorf("%s: agent %q is the default of both %q and %q", path, agent, other, ch)
		}
		seen[agent] = ch
	}
	if d.Defaults == nil {
		d.Defaults = map[string]string{}
	}
	s.doc = d
	return s, nil
}

// Get returns the channel's default agent.
func (s *Store) Get(channel string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.doc.Defaults[channel]
	return a, ok
}

// Document returns a copy of the stored document.
func (s *Store) Document() Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Document{Version: s.doc.Version, Defaults: make(map[string]string, len(s.doc.Defaults))}
	for k, v := range s.doc.Defaults {
		out.Defaults[k] = v
	}
	return out
}

// Set makes agent the channel's default, moving it off any other channel. The file
// is written before the change takes effect, so a change that cannot be kept is
// not made.
func (s *Store) Set(channel, agent string) (Change, error) {
	if channel == "" || agent == "" {
		return Change{}, errors.New("a default names a channel and an agent")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.copyLocked()
	var c Change
	c.Previous = next.Defaults[channel]
	if c.Previous == agent {
		return c, nil
	}
	for ch, a := range next.Defaults {
		if a == agent {
			c.Left = ch
			delete(next.Defaults, ch)
		}
	}
	next.Defaults[channel] = agent
	return c, s.commitLocked(next)
}

// Clear removes the channel's default.
func (s *Store) Clear(channel string) (Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, ok := s.doc.Defaults[channel]
	if !ok {
		return Change{}, nil
	}
	next := s.copyLocked()
	delete(next.Defaults, channel)
	return Change{Previous: prev}, s.commitLocked(next)
}

func (s *Store) copyLocked() Document {
	next := Document{Version: s.doc.Version + 1, Defaults: make(map[string]string, len(s.doc.Defaults)+1)}
	for k, v := range s.doc.Defaults {
		next.Defaults[k] = v
	}
	return next
}

func (s *Store) commitLocked(next Document) error {
	if err := statefile.Write(s.path, next); err != nil {
		return err
	}
	s.doc = next
	return nil
}
