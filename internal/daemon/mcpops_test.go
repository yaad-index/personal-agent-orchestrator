package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/harvest"
	"github.com/yaad-index/personal-agent-orchestrator/internal/recipe"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/temp"
	"github.com/yaad-index/personal-agent-orchestrator/internal/workdir"
)

type stubPlane struct{ killed []string }

func (p *stubPlane) Kill(_ context.Context, pr core.Profile) error {
	p.killed = append(p.killed, pr.Name)
	return nil
}

type stubChannels struct{}

func (stubChannels) HasChannel(string) bool    { return true }
func (stubChannels) Channels() []string        { return []string{"local"} }
func (stubChannels) Bind(string, string) error { return nil }
func (stubChannels) Unbind(string)             {}

// opsOverAManager builds the MCP operations over a real registry, workdir
// provisioner and harvest store, with the default (operator-only) seam.
func opsOverAManager(t *testing.T) (mcpOps, *stubPlane, *harvest.Store, string) {
	t.Helper()
	root := t.TempDir()
	reg := registry.New()
	wd := &workdir.Provisioner{Root: filepath.Join(root, "temp")}
	dir, err := wd.Provision(context.Background(), "tmp-a", workdir.Source{Files: map[string][]byte{"out.txt": []byte("x")}})
	require.NoError(t, err)
	require.NoError(t, reg.AddTemp(core.Profile{Name: "tmp-a", Workdir: dir}, registry.TempInfo{}))
	store := harvest.New(filepath.Join(root, "harvest"), nil)
	plane := &stubPlane{}
	mgr := &temp.Manager{
		Registry: reg, Plane: plane, Channels: stubChannels{}, Workdirs: wd, Harvests: store,
		Recipes: func() (*recipe.Set, error) { return recipe.LoadDir(filepath.Join(root, "recipes")) },
	}
	return mcpOps{reg: reg, mgr: mgr, store: store, recipes: mgr.Recipes}, plane, store, dir
}

func TestMCPOperationsAreMadeAsTheCallerNotAsTheOperator(t *testing.T) {
	ops, plane, store, dir := opsOverAManager(t)
	ctx := context.Background()
	agent := access.Agent("boss")

	_, err := ops.Kill(ctx, agent, "tmp-a")
	require.ErrorIs(t, err, temp.ErrNotFound, "an agent's kill is refused by the seam, and looks like no such agent")
	assert.Empty(t, plane.killed)
	assert.DirExists(t, dir)

	_, err = ops.Spawn(ctx, agent, "anything", "x", "")
	require.ErrorIs(t, err, temp.ErrNotFound)

	// The operator's kill makes a harvest; the agent cannot see or read it.
	res, err := ops.Kill(ctx, access.Operator, "tmp-a")
	require.NoError(t, err)
	assert.True(t, res.Complete)
	list, err := ops.Harvests(agent)
	require.NoError(t, err)
	assert.Empty(t, list)
	_, err = ops.Harvest(agent, res.HarvestID)
	require.ErrorIs(t, err, harvest.ErrNotFound)
	_, err = ops.HarvestFile(agent, res.HarvestID, "out.txt")
	require.ErrorIs(t, err, harvest.ErrNotFound)

	list, err = ops.Harvests(access.Operator)
	require.NoError(t, err)
	require.Len(t, list, 1)
	b, err := ops.HarvestFile(access.Operator, res.HarvestID, "out.txt")
	require.NoError(t, err)
	assert.Equal(t, "x", string(b))
	h, err := ops.Harvest(access.Operator, res.HarvestID)
	require.NoError(t, err)
	assert.Equal(t, "out.txt", h.Files[0].Path)
	_, err = store.Get(access.Operator, res.HarvestID)
	require.NoError(t, err)
	assert.Equal(t, "operator", h.Requester)
	require.NoError(t, os.RemoveAll(dir))
}
