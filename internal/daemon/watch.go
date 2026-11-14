package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mirror"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/temp"
)

// maxWatches bounds how many watches (ADR-0018) one node serves at once. A watch
// holds a follower of the agent's transcript open for as long as the operator looks,
// so the bound keeps a master that opens watches without closing them from costing
// the node more than a few readers.
const maxWatches = 8

// errTooManyWatches is a watch refused because the node is serving its bound.
var errTooManyWatches = errors.New("this node is serving as many watches as it allows")

// watchLine is one entry of a watch: its rendered text, and whether it is of the
// recent past sent first or arrived after the watch began.
type watchLine struct {
	time    time.Time
	text    string
	history bool
}

// watcher serves the operator's watch of an agent of this node: the agent's rendered
// output at a level the operator names, the recent past first and then as it arrives.
// It is a read of the transcript on disk and shares nothing with the agent's own
// channel binding, so a pause of that view does not hold it back (ADR-0018 point 7).
type watcher struct {
	reg *registry.Registry
	// auth decides who may watch what; nil is OperatorOnly, so an unset one is closed.
	auth  access.Authorizer
	poll  time.Duration
	slots chan struct{}
}

func newWatcher(reg *registry.Registry, poll time.Duration) *watcher {
	return &watcher{reg: reg, poll: poll, slots: make(chan struct{}, maxWatches)}
}

// watch, for who, calls admitted once the watch is accepted, then sends the lines of the agent's output to send until ctx is done, which is
// not an error, or send fails. An agent this node does not hold is not found, in the
// words of an absent one.
func (w *watcher) watch(ctx context.Context, who access.Subject, agent string, level, entries int, admitted func(), send func(watchLine) error) error {
	var p core.Profile
	found := false
	for _, a := range w.reg.All() {
		if a.Profile.Name == agent {
			p, found = a.Profile, true
			break
		}
	}
	// An agent the caller may not watch is answered as one that is not there, so a
	// watch cannot be used to learn which agents exist (ADR-0018 point 6).
	if found && !access.Allowed(w.auth, who, access.Watch, access.Resource{Agent: p.Name, Tags: p.Tags}) {
		found = false
	}
	if !found {
		return fmt.Errorf("%w: no agent %q on this node", temp.ErrNotFound, agent)
	}
	switch {
	case level < 1:
		return fmt.Errorf("%w: a watch names the level to show, at least 1", temp.ErrBadRequest)
	case entries < 0 || entries > maxBacktrack:
		return fmt.Errorf("%w: a watch shows at most %d entries first", temp.ErrBadRequest, maxBacktrack)
	}
	if entries == 0 {
		entries = defaultBacktrack
	}
	rd, ok := readers[p.Transcript.Format]
	if !ok {
		return fmt.Errorf("%w: agent %q has no transcript format this node reads", temp.ErrBadRequest, agent)
	}
	select {
	case w.slots <- struct{}{}:
		defer func() { <-w.slots }()
	default:
		return errTooManyWatches
	}
	admitted()

	path := expandPath(p.Transcript.Path)
	levels := p.EffectiveLevels()

	// The recent past, then follow from the start of the same transcript and skip
	// what the past already held, so nothing written between the two reads is lost or
	// shown twice. A transcript that is not there yet has no past: it is followed from
	// its first line once it appears.
	var skip int
	past, err := mirror.Replay(path, rd)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Errorf("agent %q has no transcript to replay: %w", agent, err)
	default:
		skip = len(past)
		var lines []watchLine
		for _, e := range past {
			if text, ok := render(e, levels, level); ok {
				lines = append(lines, watchLine{time: e.Time, text: text, history: true})
			}
		}
		if len(lines) > entries {
			lines = lines[len(lines)-entries:]
		}
		for _, l := range lines {
			if err := send(l); err != nil {
				return err
			}
		}
	}

	var sendErr error
	m := &mirror.Mirror{Reader: rd, Poll: w.poll}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	followErr := m.Follow(ctx, path, mirror.FromStart, func(e core.Entry) {
		if skip > 0 {
			skip--
			return
		}
		if sendErr != nil {
			return
		}
		text, ok := render(e, levels, level)
		if !ok {
			return
		}
		if sendErr = send(watchLine{time: e.Time, text: text}); sendErr != nil {
			cancel()
		}
	})
	if sendErr != nil {
		return sendErr
	}
	return followErr
}
