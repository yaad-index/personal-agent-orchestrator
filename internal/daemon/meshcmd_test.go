package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	pb "github.com/yaad-index/personal-agent-orchestrator/internal/mesh/meshpb"
)

func TestTheOperatorStartsAndKillsATemporaryAgentOnANodeFromTheMaster(t *testing.T) {
	_, a, b := meshWithRecipes(t)
	ctx := context.Background()

	name, err := a.client.Spawn(ctx, "fake", "tmp-r", "", "n-b")
	require.NoError(t, err)
	assert.Equal(t, "tmp-r", name)
	assert.True(t, tmuxHasSession(t, b, "tmp-r"), "the agent runs on the node asked")
	assert.False(t, tmuxHasSession(t, a, "tmp-r"), "and not on the master")
	directoryHas(t, a, "n-b", 2)

	agents, err := a.client.Agents(ctx)
	require.NoError(t, err)
	var node string
	for _, ag := range agents {
		if ag.Name == "tmp-r" {
			node = ag.Node
		}
	}
	assert.Equal(t, "n-b", node, "the master's list says which node holds it")

	require.NoError(t, os.WriteFile(filepath.Join(b.stateDir, "temp", "tmp-r", "out.txt"), []byte("7"), 0o600))
	res, err := a.client.Kill(ctx, "tmp-r")
	require.NoError(t, err)
	assert.False(t, tmuxHasSession(t, b, "tmp-r"))
	assert.True(t, strings.HasPrefix(res.Harvest.ID, "n-b:"), "a harvest on another node is named by it: %s", res.Harvest.ID)
	assert.True(t, res.Harvest.Complete)
	assert.Equal(t, "operator", res.Harvest.Requester)
	directoryHas(t, a, "n-b", 1)

	// Harvests stay on their node; the operator at the master lists and reads them there.
	list, err := a.client.Harvests(ctx, "n-b")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, res.Harvest.ID, list[0].ID)
	local, err := a.client.Harvests(ctx, "")
	require.NoError(t, err)
	assert.Empty(t, local, "the master holds none of its own")

	m, err := a.client.Harvest(ctx, res.Harvest.ID)
	require.NoError(t, err)
	assert.Equal(t, "tmp-r", m.Agent)
	b1, err := a.client.HarvestFile(ctx, res.Harvest.ID, "out.txt")
	require.NoError(t, err)
	assert.Equal(t, "7", string(b1))

	require.NoError(t, a.client.DeleteHarvest(ctx, res.Harvest.ID))
	_, err = a.client.Harvest(ctx, res.Harvest.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestSpawningOnTheMasterItselfByNameIsTheLocalSpawn(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.startWith(t, "n-a", nil, map[string]string{"fake": goodRecipe("fake")})
	f.startWith(t, "n-b", nil, nil)
	name, err := a.client.Spawn(context.Background(), "fake", "tmp-m", "", "n-a")
	require.NoError(t, err)
	assert.True(t, tmuxHasSession(t, a, name))
}

func TestARemoteSpawnThatTheMeshCannotPlaceIsRefusedWithAReason(t *testing.T) {
	_, a, b := meshWithRecipes(t)
	ctx := context.Background()

	_, err := a.client.Spawn(ctx, "fake", "x", "", "n-zzz")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
	assert.Contains(t, err.Error(), "no node")

	_, err = a.client.Spawn(ctx, "no-such", "", "", "n-b")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404", "an unknown recipe on that node is a not found")

	_, err = a.client.Spawn(ctx, "fake", "bee", "", "n-b")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409", "a name the mesh holds is a conflict")

	// A secondary does not command other nodes.
	_, err = b.client.Spawn(ctx, "fake", "", "", "n-a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")

	// Killing a name nobody holds, and an agent that is not temporary.
	_, err = a.client.Kill(ctx, "ghost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
	_, err = a.client.Kill(ctx, "bee")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}

func TestANodeOutsideAMeshHasNoOtherNodeToSpawnOn(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	_, err := n.client.Spawn(context.Background(), "fake", "", "", "n-b")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}

func TestARemoteSpawnThatIsNotReadyReturnsItsNameAndCanBeKilled(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.startWith(t, "n-a", nil, nil)
	b := f.startWith(t, "n-b", nil, map[string]string{"stuck": tempRecipe("@@SCRIPT@@", "stuck", "NEVER-MATCHES", "claude-code")})
	directoryHas(t, a, "n-b", 0)
	ctx := context.Background()

	name, err := a.client.Spawn(ctx, "stuck", "tmp-s", "", "n-b")
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "502"), err.Error())
	assert.Equal(t, "tmp-s", name, "the name comes back so it can be inspected or killed")

	directoryHas(t, a, "n-b", 1)
	_, err = a.client.Kill(ctx, "tmp-s")
	require.NoError(t, err)
	_, ok := agentByName(t, b, "tmp-s")
	assert.False(t, ok)
}

func TestARemoteSpawnCannotTakeANameTheMasterHolds(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.startWith(t, "n-a", map[string]string{"boss": ""}, nil)
	b := f.startWith(t, "n-b", nil, map[string]string{"fake": goodRecipe("fake")})
	directoryHas(t, a, "n-b", 0)

	_, err := a.client.Spawn(context.Background(), "fake", "boss", "", "n-b")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409")
	agents, err := b.client.Agents(context.Background())
	require.NoError(t, err)
	assert.Empty(t, agents, "refused before anything was started on the node")
}

func TestWhatANodeAnswersKeepsItsMeaningAndWordsAtTheMaster(t *testing.T) {
	for _, c := range []struct {
		code codes.Code
		kind error
	}{
		{codes.NotFound, local.ErrNotFound},
		{codes.InvalidArgument, local.ErrBadRequest},
		{codes.AlreadyExists, local.ErrConflict},
		{codes.PermissionDenied, local.ErrForbidden},
		{codes.FailedPrecondition, local.ErrConflict},
	} {
		err := fromStatus(status.Error(c.code, "the node's words"))
		require.ErrorIs(t, err, c.kind, c.code.String())
		assert.Equal(t, "the node's words", err.Error())
	}
	plain := fromStatus(status.Error(codes.Unavailable, "down"))
	for _, k := range []error{local.ErrNotFound, local.ErrBadRequest, local.ErrConflict, local.ErrForbidden} {
		assert.NotErrorIs(t, plain, k, "a node that cannot be reached is none of the four")
	}
	assert.NoError(t, fromStatus(nil))
}

func TestARemoteSpawnCannotTakeANameAnAgentOnAThirdNodeHolds(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b", "n-c")
	a := f.startWith(t, "n-a", nil, nil)
	f.startWith(t, "n-b", map[string]string{"bee": ""}, nil)
	c := f.startWith(t, "n-c", nil, map[string]string{"fake": goodRecipe("fake")})
	directoryHas(t, a, "n-b", 1)

	_, err := a.client.Spawn(context.Background(), "fake", "bee", "", "n-c")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409")
	agents, err := c.client.Agents(context.Background())
	require.NoError(t, err)
	assert.Empty(t, agents, "refused at the master, before the node was asked")
}

func TestAnAuthorizedCommandIsNotDecidedAgainAndTheKillIsNotReportedOnTheNode(t *testing.T) {
	f, _, b := meshWithRecipes(t)
	ctx := context.Background()
	node := asMaster(t, f, "n-b")

	// The master authorized this for the agent; the node's own seam (which refuses
	// every agent) is not asked again.
	_, err := node.Spawn(ctx, &pb.SpawnRequest{RequestId: "s", Recipe: "fake", Name: "tmp-y", Requester: "agent:boss"})
	require.NoError(t, err)
	time.Sleep(300 * time.Millisecond)
	for len(b.events) > 0 {
		<-b.events
	}
	res, err := node.KillTemp(ctx, &pb.AgentRef{RequestId: "k", Agent: "tmp-y", Requester: "operator"})
	require.NoError(t, err)
	assert.NotEmpty(t, res.GetHarvestId())

	select {
	case m := <-b.events:
		t.Fatalf("the node reported the harvest on its own channel, which the master answers for: %q", m.Text)
	case <-time.After(time.Second):
	}
}

func TestListingTheHarvestsOfANodeThatIsNotInTheMeshIsRefused(t *testing.T) {
	_, a, b := meshWithRecipes(t)
	ctx := context.Background()

	_, err := a.client.Harvests(ctx, "n-zzz")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
	assert.Contains(t, err.Error(), "no node")

	own, err := a.client.Harvests(ctx, "n-a")
	require.NoError(t, err, "naming this node is its own list")
	assert.Empty(t, own)
	_, err = b.client.Harvests(ctx, "n-zzz")
	require.Error(t, err)

	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	_, err = n.client.Harvests(ctx, "n-b")
	require.Error(t, err, "a node outside a mesh has no other node")
	assert.Contains(t, err.Error(), "400")
}
