package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh"
	pb "github.com/yaad-index/personal-agent-orchestrator/internal/mesh/meshpb"
)

// asMaster dials a node of the mesh as the master does: with the master's key, at
// the mesh epoch 0 the nodes start from, with a deadline on every call.
func asMaster(t *testing.T, f *meshFixture, to string) pb.NodeClient {
	t.Helper()
	conn, err := grpc.NewClient(f.lns[to].Addr().String(),
		grpc.WithTransportCredentials(mesh.ClientCredentials(f.ids["n-a"], f.doc.Peers(), to)),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, inv grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return inv(metadata.AppendToOutgoingContext(ctx, "pao-epoch", "0"), method, req, reply, cc, opts...)
		}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewNodeClient(conn)
}

// meshWithRecipes starts a master and a secondary that hosts the agent bee and has a
// recipe.
func meshWithRecipes(t *testing.T) (f *meshFixture, a, b *node) {
	t.Helper()
	f = newMeshFixture(t, "n-a", "n-b")
	a = f.startWith(t, "n-a", nil, nil)
	b = f.startWith(t, "n-b", map[string]string{"bee": ""}, map[string]string{"fake": goodRecipe("fake")})
	directoryHas(t, a, "n-b", 1)
	return f, a, b
}

func TestTheMasterSpawnsATemporaryAgentOnANodeAndItIsInTheDirectory(t *testing.T) {
	f, a, b := meshWithRecipes(t)
	ctx := context.Background()
	node := asMaster(t, f, "n-b")

	res, err := node.Spawn(ctx, &pb.SpawnRequest{RequestId: "s1", Recipe: "fake", Name: "tmp-x"})
	require.NoError(t, err)
	assert.Equal(t, "tmp-x", res.GetName())

	directoryHas(t, a, "n-b", 2)
	cs := mcpSession(t, a, token)
	out := mcpCall[struct {
		Agents []struct {
			Name      string `json:"name"`
			Node      string `json:"node"`
			Temporary bool   `json:"temporary"`
			Recipe    string `json:"recipe"`
		} `json:"agents"`
	}](t, cs, "list_agents", nil)
	var found bool
	for _, ag := range out.Agents {
		if ag.Name == "tmp-x" {
			found = true
			assert.Equal(t, "n-b", ag.Node)
			assert.True(t, ag.Temporary)
			assert.Equal(t, "fake", ag.Recipe)
		}
	}
	require.True(t, found, "the master learns of the temporary agent from the node's report")

	// And a message to it through the master reaches it.
	_, err = a.client.Post(ctx, "human", "hello temp", "name", "tmp-x")
	require.NoError(t, err)
	assert.Equal(t, "echo: [human]: hello temp", b.next(t).Text)
}

func TestARepeatedSpawnCommandSpawnsOnce(t *testing.T) {
	f, _, b := meshWithRecipes(t)
	ctx := context.Background()
	node := asMaster(t, f, "n-b")

	first, err := node.Spawn(ctx, &pb.SpawnRequest{RequestId: "once", Recipe: "fake"})
	require.NoError(t, err)
	again, err := node.Spawn(ctx, &pb.SpawnRequest{RequestId: "once", Recipe: "fake"})
	require.NoError(t, err)
	assert.Equal(t, first.GetName(), again.GetName(), "a retry with the same request id is the same spawn, with no second agent")
	agents, err := b.client.Agents(ctx)
	require.NoError(t, err)
	assert.Len(t, agents, 1)
}

func TestSpawnErrorsHaveStatusesTheMasterCanActOn(t *testing.T) {
	f, _, _ := meshWithRecipes(t)
	ctx := context.Background()
	node := asMaster(t, f, "n-b")

	_, err := node.Spawn(ctx, &pb.SpawnRequest{RequestId: "e1", Recipe: "no-such"})
	assert.Equal(t, codes.NotFound, status.Code(err))
	_, err = node.Spawn(ctx, &pb.SpawnRequest{RequestId: "e2", Recipe: "fake", Name: "Bad Name"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = node.Spawn(ctx, &pb.SpawnRequest{RequestId: "e3", Recipe: "fake", Name: "bee"})
	assert.Equal(t, codes.AlreadyExists, status.Code(err), "bee is a configured agent on that node")
	_, err = node.Spawn(ctx, &pb.SpawnRequest{RequestId: "e4", Recipe: "fake", Requester: "someone"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestKillingATemporaryAgentOnANodeHarvestsItThereAndTheMasterReadsIt(t *testing.T) {
	f, a, b := meshWithRecipes(t)
	ctx := context.Background()
	node := asMaster(t, f, "n-b")
	_, err := node.Spawn(ctx, &pb.SpawnRequest{RequestId: "s", Recipe: "fake", Name: "tmp-x"})
	require.NoError(t, err)
	workdir := filepath.Join(b.stateDir, "temp", "tmp-x")
	require.NoError(t, os.WriteFile(filepath.Join(workdir, "result.txt"), []byte("42"), 0o600))

	res, err := node.KillTemp(ctx, &pb.AgentRef{RequestId: "k", Agent: "tmp-x", Requester: "agent:boss"})
	require.NoError(t, err)
	assert.True(t, res.GetComplete())
	assert.False(t, res.GetWorkdirKept())
	assert.GreaterOrEqual(t, res.GetFiles(), int64(2))
	assert.Contains(t, res.GetSummary(), res.GetHarvestId())
	assert.NoDirExists(t, workdir, "destroyed on its own node, after the harvest")
	directoryHas(t, a, "n-b", 1)

	// The harvest stays on the node that made it; the master lists, reads, deletes it there.
	list, err := node.ListHarvests(ctx, &pb.ListHarvestsRequest{})
	require.NoError(t, err)
	require.Len(t, list.GetHarvests(), 1)
	assert.Equal(t, res.GetHarvestId(), list.GetHarvests()[0].GetId())
	assert.Equal(t, "agent:boss", list.GetHarvests()[0].GetRequester(), "the harvest records the agent the master acted for")
	m, err := node.GetHarvest(ctx, &pb.HarvestRef{Id: res.GetHarvestId()})
	require.NoError(t, err)
	assert.Equal(t, []string{"temp"}, m.GetTags())
	var paths []string
	for _, fi := range m.GetFiles() {
		paths = append(paths, fi.GetPath())
	}
	assert.Contains(t, paths, "result.txt")
	file, err := node.ReadHarvestFile(ctx, &pb.HarvestFileRef{Id: res.GetHarvestId(), Path: "result.txt"})
	require.NoError(t, err)
	assert.Equal(t, "42", string(file.GetContent()))

	_, err = node.DeleteHarvest(ctx, &pb.HarvestRef{Id: res.GetHarvestId()})
	require.NoError(t, err)
	_, err = node.GetHarvest(ctx, &pb.HarvestRef{Id: res.GetHarvestId()})
	assert.Equal(t, codes.NotFound, status.Code(err))

	// The kill posted nothing to that node's own operator: the master has the result.
	select {
	case m := <-b.events:
		t.Fatalf("the node reported a master-commanded kill to its own channel: %+v", m)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestAHarvestFileTooBigForOneCallIsRefusedWithTheWayOut(t *testing.T) {
	f, _, b := meshWithRecipes(t)
	ctx := context.Background()
	node := asMaster(t, f, "n-b")
	_, err := node.Spawn(ctx, &pb.SpawnRequest{RequestId: "s", Recipe: "fake", Name: "tmp-x"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(b.stateDir, "temp", "tmp-x", "big.bin"), make([]byte, maxHarvestFileRPC+1), 0o600))
	res, err := node.KillTemp(ctx, &pb.AgentRef{RequestId: "k", Agent: "tmp-x"})
	require.NoError(t, err)

	_, err = node.ReadHarvestFile(ctx, &pb.HarvestFileRef{Id: res.GetHarvestId(), Path: "big.bin"})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Contains(t, err.Error(), "read it on its node")
}

func TestTheMasterLaunchesKillsAndRestartsAConfiguredAgentOnANode(t *testing.T) {
	f, a, b := meshWithRecipes(t)
	ctx := context.Background()
	node := asMaster(t, f, "n-b")

	_, err := node.Launch(ctx, &pb.AgentRef{RequestId: "l1", Agent: "bee"})
	require.NoError(t, err)
	assert.True(t, tmuxHasSession(t, b, "bee"))

	_, err = node.Restart(ctx, &pb.AgentRef{RequestId: "r1", Agent: "bee"})
	require.NoError(t, err)
	assert.True(t, tmuxHasSession(t, b, "bee"), "restarted: killed and launched again")

	_, err = node.Kill(ctx, &pb.AgentRef{RequestId: "k1", Agent: "bee"})
	require.NoError(t, err)
	assert.False(t, tmuxHasSession(t, b, "bee"))
	// A stopped agent is started again by the next message through the master.
	rep, err := a.client.Post(ctx, "human", "wake", "name", "bee")
	require.NoError(t, err)
	assert.True(t, rep.Results[0].Launched)
	assert.Equal(t, "echo: [human]: wake", b.next(t).Text)
}

func TestACommandOnAnUnknownOrTemporaryAgentIsRefusedWithAReason(t *testing.T) {
	f, _, _ := meshWithRecipes(t)
	ctx := context.Background()
	node := asMaster(t, f, "n-b")

	for name, call := range map[string]func() error{
		"launch": func() error { _, err := node.Launch(ctx, &pb.AgentRef{RequestId: "a", Agent: "ghost"}); return err },
		"kill":   func() error { _, err := node.Kill(ctx, &pb.AgentRef{RequestId: "b", Agent: "ghost"}); return err },
		"temp":   func() error { _, err := node.KillTemp(ctx, &pb.AgentRef{RequestId: "c", Agent: "ghost"}); return err },
		"conf":   func() error { _, err := node.KillTemp(ctx, &pb.AgentRef{RequestId: "d", Agent: "bee"}); return err },
	} {
		err := call()
		require.Error(t, err, name)
		assert.Equal(t, codes.NotFound, status.Code(err), name)
	}

	_, err := node.Spawn(ctx, &pb.SpawnRequest{RequestId: "s", Recipe: "fake", Name: "tmp-x"})
	require.NoError(t, err)
	_, err = node.Kill(ctx, &pb.AgentRef{RequestId: "k", Agent: "tmp-x"})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Contains(t, err.Error(), "KillTemp", "a temporary agent is killed only the way that harvests it")
	_, err = node.Restart(ctx, &pb.AgentRef{RequestId: "r", Agent: "tmp-x"})
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	_, err = node.Launch(ctx, &pb.AgentRef{RequestId: "l", Agent: "tmp-x"})
	assert.Equal(t, codes.FailedPrecondition, status.Code(err), "a temporary agent is started by spawning it")
}
