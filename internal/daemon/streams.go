package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mirror"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/temp"
)

const (
	// maxHeld bounds what a paused view keeps. Past it the oldest are dropped and
	// the viewer is told how many on resume: the transcript on disk still has them,
	// and a backtrack shows them again.
	maxHeld = 1000
	// defaultBacktrack and maxBacktrack bound the window a backtrack re-renders.
	defaultBacktrack = 20
	maxBacktrack     = 200
)

// errViewer is a view control from someone other than the viewer the stream is for.
var errViewer = errors.New("the stream is for another viewer")

// view is what a node holds about how one agent's output is shown to a viewer
// (ADR-0008 point 3): the level it is rendered at, whose view it is, and what a
// pause is holding back. It is local state, reported to the master but never needed
// from it.
type view struct {
	level  int
	viewer string
	paused bool
	held   []core.Message
	// dropped counts messages a full pause let go of.
	dropped int
}

// views holds the view of each agent that has one.
type views struct {
	mu sync.Mutex
	m  map[string]*view
}

func newViews() *views { return &views{m: map[string]*view{}} }

// level is the level the agent's output is rendered at: its own if one was set,
// otherwise def.
func (v *views) level(name string, def int) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	if w, ok := v.m[name]; ok && w.level > 0 {
		return w.level
	}
	return def
}

// hold keeps m instead of sending it when the agent's view is paused.
func (v *views) hold(name string, m core.Message) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	w, ok := v.m[name]
	if !ok || !w.paused {
		return false
	}
	if len(w.held) >= maxHeld {
		w.held = w.held[1:]
		w.dropped++
	}
	w.held = append(w.held, m)
	return true
}

// info returns the level and viewer set for the agent, if any.
func (v *views) info(name string) (level int, viewer string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if w, ok := v.m[name]; ok {
		return w.level, w.viewer
	}
	return 0, ""
}

// drop forgets the agent's view.
func (v *views) drop(name string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.m, name)
}

// bindings is the layer's channel binding with the view dropped when a binding is
// let go, so a name spawned again does not inherit a level or a pause.
type bindings struct {
	*comm.Layer
	views *views
}

func (b bindings) Unbind(agent string) {
	b.Layer.Unbind(agent)
	b.views.drop(agent)
}

// streamControl is what binds an agent's output to a channel, sets how it is
// rendered, and carries a viewer's pause, resume and backtrack. The operator's
// local routes and the master's commands to a node both come here.
type streamControl struct {
	layer *comm.Layer
	reg   *registry.Registry
	views *views
	// send is how a held message finally goes out: as the agent's own output.
	send func(ctx context.Context, agent string, m core.Message) error
	// level is the node's default level.
	level int
}

func (c *streamControl) profile(name string) (core.Profile, error) {
	for _, a := range c.reg.All() {
		if a.Profile.Name == name {
			return a.Profile, nil
		}
	}
	return core.Profile{}, fmt.Errorf("%w: no agent %q on this node", temp.ErrNotFound, name)
}

// SetStream binds the agent's output to channel, rendered at level for viewer. An
// empty channel clears the binding and the view with it: the output goes to every
// channel again at the node's level, and what a pause held is let go.
func (c *streamControl) SetStream(agent, channel string, level int, viewer string) error {
	if _, err := c.profile(agent); err != nil {
		return err
	}
	if level < 0 {
		return fmt.Errorf("%w: level %d is below 0", temp.ErrBadRequest, level)
	}
	if channel == "" {
		c.layer.Unbind(agent)
		c.views.mu.Lock()
		delete(c.views.m, agent)
		c.views.mu.Unlock()
		return nil
	}
	if err := c.layer.Bind(agent, channel); err != nil {
		return fmt.Errorf("%w: %v", temp.ErrBadRequest, err)
	}
	c.views.mu.Lock()
	defer c.views.mu.Unlock()
	// A new binding is a new view: nothing paused carries over to it.
	c.views.m[agent] = &view{level: level, viewer: viewer}
	return nil
}

func (c *streamControl) check(agent, viewer string) (*view, error) {
	if _, err := c.profile(agent); err != nil {
		return nil, err
	}
	w, ok := c.views.m[agent]
	if !ok {
		w = &view{}
		c.views.m[agent] = w
	}
	if w.viewer != "" && viewer != w.viewer {
		return nil, fmt.Errorf("%w: %q's stream of %q is for %q", errViewer, agent, agent, w.viewer)
	}
	return w, nil
}

// Pause holds the agent's output back from the view. The agent keeps working and the
// transcript keeps growing.
func (c *streamControl) Pause(agent, viewer string) error {
	c.views.mu.Lock()
	defer c.views.mu.Unlock()
	w, err := c.check(agent, viewer)
	if err != nil {
		return err
	}
	w.paused = true
	return nil
}

// Resume sends what the pause held, in order, then lets live output through. Output
// that arrives while the held part is going out is held too and goes after it, so
// the order is the agent's.
func (c *streamControl) Resume(ctx context.Context, agent, viewer string) error {
	c.views.mu.Lock()
	w, err := c.check(agent, viewer)
	if err != nil {
		c.views.mu.Unlock()
		return err
	}
	if !w.paused {
		c.views.mu.Unlock()
		return nil
	}
	c.views.mu.Unlock()
	if d := c.takeDropped(agent); d > 0 {
		if err := c.notice(ctx, agent, fmt.Sprintf("%d earlier message(s) were not kept while the view was paused; a backtrack shows them again", d)); err != nil {
			return err
		}
	}
	for {
		c.views.mu.Lock()
		w := c.views.m[agent]
		if w == nil || len(w.held) == 0 {
			if w != nil {
				w.paused = false
			}
			c.views.mu.Unlock()
			return nil
		}
		batch := w.held
		w.held = nil
		c.views.mu.Unlock()
		for i, m := range batch {
			if err := c.send(ctx, agent, m); err != nil {
				c.putBack(agent, batch[i:])
				return err
			}
		}
	}
}

// putBack returns what a failed resume did not send to the front of what the view
// holds, so a retry sends it, in order, before anything that arrived since. The view
// stays paused.
func (c *streamControl) putBack(agent string, unsent []core.Message) {
	c.views.mu.Lock()
	defer c.views.mu.Unlock()
	w := c.views.m[agent]
	if w == nil {
		return
	}
	w.held = append(append([]core.Message(nil), unsent...), w.held...)
	if over := len(w.held) - maxHeld; over > 0 {
		w.held = w.held[over:]
		w.dropped += over
	}
}

func (c *streamControl) takeDropped(agent string) int {
	c.views.mu.Lock()
	defer c.views.mu.Unlock()
	if w := c.views.m[agent]; w != nil {
		d := w.dropped
		w.dropped = 0
		return d
	}
	return 0
}

// notice shows the viewer a message of pao's on the channel the agent is bound to,
// or on every channel when it is not bound.
func (c *streamControl) notice(ctx context.Context, agent, text string) error {
	m := core.Message{Sender: "pao", Text: text}
	if ch, ok := c.layer.Bound(agent); ok {
		_, err := c.layer.NotifyOn(ctx, ch, m)
		return err
	}
	_, err := c.layer.Notify(ctx, m)
	return err
}

// Backtrack re-renders the last entries of the agent's transcript that are visible
// at level, for the viewer alone. The level changes only what is shown: the
// transcript is read as it is on disk (ADR-0008 points 3 and 4).
func (c *streamControl) Backtrack(ctx context.Context, agent, viewer string, level, entries int) error {
	c.views.mu.Lock()
	_, err := c.check(agent, viewer)
	c.views.mu.Unlock()
	if err != nil {
		return err
	}
	p, _ := c.profile(agent)
	switch {
	case level < 1:
		return fmt.Errorf("%w: a backtrack names the level to show, at least 1", temp.ErrBadRequest)
	case entries < 0 || entries > maxBacktrack:
		return fmt.Errorf("%w: a backtrack shows at most %d entries", temp.ErrBadRequest, maxBacktrack)
	}
	if entries == 0 {
		entries = defaultBacktrack
	}
	rd, ok := readers[p.Transcript.Format]
	if !ok {
		return fmt.Errorf("%w: agent %q has no transcript format this node reads", temp.ErrBadRequest, agent)
	}
	all, err := mirror.Replay(expandPath(p.Transcript.Path), rd)
	if err != nil {
		return fmt.Errorf("%w: agent %q has no transcript to replay: %v", temp.ErrNotFound, agent, err)
	}
	levels := p.EffectiveLevels()
	var lines []string
	for _, e := range all {
		if text, ok := render(e, levels, level); ok {
			lines = append(lines, text)
		}
	}
	if len(lines) > entries {
		lines = lines[len(lines)-entries:]
	}
	text := fmt.Sprintf("%s, the last %d entr(ies) at level %d:\n%s", agent, len(lines), level, strings.Join(lines, "\n"))
	if len(lines) == 0 {
		text = fmt.Sprintf("%s: nothing to show at level %d yet", agent, level)
	}
	return c.notice(ctx, agent, text)
}
