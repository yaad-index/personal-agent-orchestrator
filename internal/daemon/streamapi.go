package daemon

import (
	"context"
	"fmt"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh/directory"
)

// streamAPI is the operator's stream and view control (local.StreamAPI): an agent of
// this node is handled here, an agent of another node by a command to that node,
// found by name in the master's directory.
type streamAPI struct {
	streams *streamControl
	cmds    *meshCommands
	dir     *directory.Directory
}

// remote returns the entry of an agent held on another node. ok is false for an
// agent of this node, and err is set for a name nobody holds.
func (a streamAPI) remote(name string) (e directory.Entry, ok bool, err error) {
	if _, perr := a.streams.profile(name); perr == nil {
		return directory.Entry{}, false, nil
	}
	if a.dir != nil {
		if e, found := a.dir.Lookup(name); found {
			return e, true, nil
		}
	}
	return directory.Entry{}, false, translated{local.ErrNotFound, fmt.Errorf("no agent %q in the mesh", name)}
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
