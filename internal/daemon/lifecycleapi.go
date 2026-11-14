package daemon

import (
	"context"

	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh/directory"
)

// lifecycleAPI is the operator's start, stop and restart of a configured agent
// (local.LifecycleAPI): an agent of this node is handled here, an agent of another
// node by a command to that node, found by name in the master's directory.
type lifecycleAPI struct {
	life lifecycle
	cmds *meshCommands
	dir  *directory.Directory
}

func (a lifecycleAPI) run(ctx context.Context, name, action string, local func(context.Context, string) error) error {
	e, remote, err := locateAgent(a.life.reg, a.dir, name)
	if err != nil {
		return err
	}
	if remote {
		return a.cmds.Lifecycle(ctx, e.Node, name, action)
	}
	return translate(local(ctx, name))
}

func (a lifecycleAPI) Start(ctx context.Context, name string) error {
	return a.run(ctx, name, "start", a.life.Start)
}

func (a lifecycleAPI) Stop(ctx context.Context, name string) error {
	return a.run(ctx, name, "stop", a.life.Stop)
}

func (a lifecycleAPI) Restart(ctx context.Context, name string) error {
	return a.run(ctx, name, "restart", a.life.Restart)
}
