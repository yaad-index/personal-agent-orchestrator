package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"sync"

	"github.com/yaad-index/personal-agent-orchestrator/internal/statefile"
)

// savedStream is one agent's stream binding as the node keeps it across a restart.
type savedStream struct {
	Channel string `json:"channel"`
	Level   int    `json:"level,omitempty"`
	Viewer  string `json:"viewer,omitempty"`
}

// savedStreams keeps the stream bindings set through the stream control, so they
// outlive a restart of the node, wherever they were set from (ADR-0026 point 8).
// A pause is not kept: it is a viewer's moment, not a binding.
type savedStreams struct {
	path string
	mu   sync.Mutex
	m    map[string]savedStream
}

// loadSavedStreams reads the saved bindings at path. A missing file is none.
func loadSavedStreams(path string) (*savedStreams, error) {
	s := &savedStreams{path: path, m: map[string]savedStream{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stream bindings: %w", err)
	}
	if err := json.Unmarshal(b, &s.m); err != nil {
		return nil, fmt.Errorf("stream bindings: %s: %w", path, err)
	}
	if s.m == nil {
		s.m = map[string]savedStream{}
	}
	return s, nil
}

// set keeps the agent's binding. On an error nothing is changed, on disk or here.
func (s *savedStreams) set(agent string, b savedStream) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := maps.Clone(s.m)
	next[agent] = b
	if err := statefile.Write(s.path, next); err != nil {
		return fmt.Errorf("keep the stream binding: %w", err)
	}
	s.m = next
	return nil
}

// forget drops the agent's binding. On an error nothing is changed.
func (s *savedStreams) forget(agent string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[agent]; !ok {
		return nil
	}
	next := maps.Clone(s.m)
	delete(next, agent)
	if err := statefile.Write(s.path, next); err != nil {
		return fmt.Errorf("drop the stream binding: %w", err)
	}
	s.m = next
	return nil
}

func (s *savedStreams) all() map[string]savedStream {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.m)
}

// restore applies the saved bindings at start. One whose agent or channel this node
// no longer has is dropped, and said so in the log.
func (c *streamControl) restore() {
	for agent, b := range c.saved.all() {
		_, err := c.profile(agent)
		switch {
		case err != nil:
			slog.Info("stream binding dropped: no such agent on this node", "agent", agent, "channel", b.Channel)
		case !c.layer.HasChannel(b.Channel):
			slog.Info("stream binding dropped: no such channel", "agent", agent, "channel", b.Channel)
		default:
			c.apply(agent, b)
			continue
		}
		if err := c.saved.forget(agent); err != nil {
			slog.Warn("stream binding could not be dropped", "agent", agent, "error", err)
		}
	}
}
