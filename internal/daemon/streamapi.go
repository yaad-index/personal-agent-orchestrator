package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh/directory"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
)

// streamAPI is the operator's stream and view control (local.StreamAPI): an agent of
// this node is handled here, an agent of another node by a command to that node,
// found by name in the master's directory.
type streamAPI struct {
	streams *streamControl
	cmds    *meshCommands
	dir     *directory.Directory
	watcher *watcher
}

// locateAgent finds who holds an agent: this node (remote false), or another node of
// the mesh, by the master's directory. A name nobody holds is a not found, never a
// fall back to this node.
func locateAgent(reg *registry.Registry, dir *directory.Directory, name string) (e directory.Entry, remote bool, err error) {
	if _, _, ok := reg.Lookup(name); ok {
		return directory.Entry{}, false, nil
	}
	if dir != nil {
		if e, found := dir.Lookup(name); found {
			return e, true, nil
		}
	}
	where := ""
	if dir != nil {
		where = " in the mesh"
	}
	return directory.Entry{}, false, translated{local.ErrNotFound, fmt.Errorf("no agent %q%s", name, where)}
}

func (a streamAPI) remote(name string) (directory.Entry, bool, error) {
	return locateAgent(a.streams.reg, a.dir, name)
}

func (a streamAPI) SetStream(ctx context.Context, agent, channel string, level int) error {
	e, isRemote, err := a.remote(agent)
	if err != nil {
		return err
	}
	if isRemote {
		return a.cmds.SetStream(ctx, e.Node, agent, channel, level)
	}
	return translate(a.streams.SetStream(agent, channel, level, operatorViewer))
}

func (a streamAPI) View(ctx context.Context, agent, action string, level, entries int) error {
	e, isRemote, err := a.remote(agent)
	if err != nil {
		return err
	}
	if isRemote {
		return a.cmds.View(ctx, e.Node, agent, action, level, entries)
	}
	switch action {
	case "pause":
		return translate(a.streams.Pause(agent, operatorViewer))
	case "resume":
		return translate(a.streams.Resume(ctx, agent, operatorViewer))
	case "backtrack":
		return translate(a.streams.Backtrack(ctx, agent, operatorViewer, level, entries))
	}
	return translated{local.ErrBadRequest, fmt.Errorf("a view action is pause, resume or backtrack, not %q", action)}
}

// Watch shows the operator an agent's output on demand (ADR-0018): an agent of this
// node from its transcript, an agent of another node by a watch on that node.
func (a streamAPI) Watch(ctx context.Context, agent string, level, entries int, opened func(), emit func(local.WatchLineJSON) error) error {
	e, isRemote, err := a.remote(agent)
	if err != nil {
		return err
	}
	if isRemote {
		return a.cmds.Watch(ctx, e.Node, agent, level, entries, opened, emit)
	}
	return translate(a.watcher.watch(ctx, access.Operator, agent, level, entries, opened, func(l watchLine) error {
		out := local.WatchLineJSON{Text: l.text, History: l.history}
		if !l.time.IsZero() {
			out.Time = l.time.Format(time.RFC3339Nano)
		}
		return emit(out)
	}))
}
