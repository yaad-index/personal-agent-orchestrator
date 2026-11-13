package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
)

// directoryHas waits until the master's directory holds n agents of a peer.
func directoryHas(t *testing.T, master *node, peer string, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		st, err := master.client.Mesh(context.Background())
		if err != nil {
			return false
		}
		for _, p := range st.Peers {
			if p.ID == peer {
				return p.Agents == n
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond, "the master's directory holds %d agent(s) of %s", n, peer)
}

func TestAMessageToAnAgentOnAnotherNodeReachesItThroughTheMaster(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.startWith(t, "n-a", nil, nil)
	b := f.startWith(t, "n-b", map[string]string{"bee": ""}, nil)
	directoryHas(t, a, "n-b", 1)

	// The human talks only to the master, which addresses an agent it does not host.
	rep, err := a.client.Post(context.Background(), "human", "hello bee", "name", "bee")
	require.NoError(t, err)
	require.Len(t, rep.Results, 1)
	assert.Equal(t, "bee", rep.Results[0].Agent)
	assert.True(t, rep.Results[0].Launched, "the node started its agent to receive it")
	assert.Empty(t, rep.Results[0].Error)

	// The agent read one line from the human, on its own node. (Where its output is
	// shown is a later step: for now it goes to the channel of the node it runs on.)
	got := b.next(t)
	assert.Equal(t, "bee", got.Sender)
	assert.Equal(t, "echo: [human]: hello bee", got.Text)

	// A second message finds it already running.
	rep, err = a.client.Post(context.Background(), "human", "again", "name", "bee")
	require.NoError(t, err)
	assert.False(t, rep.Results[0].Launched)
	assert.Equal(t, "echo: [human]: again", b.next(t).Text)
}

func TestATagFansOutAcrossNodes(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.startWith(t, "n-a", map[string]string{"alpha": ""}, nil)
	b := f.startWith(t, "n-b", map[string]string{"bee": ""}, nil)
	directoryHas(t, a, "n-b", 1)

	rep, err := a.client.Post(context.Background(), "human", "all hands", "tag", "team")
	require.NoError(t, err)
	var agents []string
	for _, r := range rep.Results {
		agents = append(agents, r.Agent)
		assert.Empty(t, r.Error, r.Agent)
	}
	assert.Equal(t, []string{"alpha", "bee"}, agents, "a local agent and a remote one, in name order")
	assert.Equal(t, "echo: [human]: all hands", a.next(t).Text)
	assert.Equal(t, "echo: [human]: all hands", b.next(t).Text)
}

func TestAnAgentThatLeavesTheMeshIsNoLongerAddressable(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.startWith(t, "n-a", nil, nil)
	b := f.startWith(t, "n-b", map[string]string{"bee": ""}, nil)
	directoryHas(t, a, "n-b", 1)

	b.cancel() // the node stops
	directoryHas(t, a, "n-b", 0)
	_, err := a.client.Post(context.Background(), "human", "anyone?", "name", "bee")
	require.Error(t, err, "the master does not route to an agent it can no longer hear about")
	assert.Contains(t, err.Error(), "no agent")
}

func TestTwoAgentsWithOneNameAreAConflictAndTheMastersOwnWins(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.startWith(t, "n-a", map[string]string{"dup": ""}, nil)
	b := f.startWith(t, "n-b", map[string]string{"dup": "", "only": ""}, nil)

	require.Eventually(t, func() bool {
		st, err := a.client.Mesh(context.Background())
		return err == nil && len(st.Conflicts) == 1
	}, 10*time.Second, 50*time.Millisecond)
	st, err := a.client.Mesh(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []local.MeshConflictJSON{{Name: "dup", Node: "n-b", HeldBy: ""}}, st.Conflicts)
	assert.Equal(t, 1, st.Peers[0].Agents, "only the agent that is in the directory counts")

	rep, err := a.client.Post(context.Background(), "human", "who", "name", "dup")
	require.NoError(t, err)
	assert.Equal(t, "echo: [human]: who", a.next(t).Text, "the name goes to the master's own agent")
	assert.Empty(t, rep.Results[0].Error)
	select {
	case m := <-b.events:
		t.Fatalf("the conflicting agent was reached: %+v", m)
	case <-time.After(300 * time.Millisecond):
	}

	rep, err = a.client.Post(context.Background(), "human", "to only", "name", "only")
	require.NoError(t, err)
	assert.Equal(t, "echo: [human]: to only", b.next(t).Text, "the other agent on that node is unaffected")
}

func TestATemporaryAgentCannotTakeANameAnotherNodeHolds(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.startWith(t, "n-a", nil, map[string]string{"fake": goodRecipe("fake")})
	f.startWith(t, "n-b", map[string]string{"bee": ""}, nil)
	directoryHas(t, a, "n-b", 1)

	_, err := a.client.Spawn(context.Background(), "fake", "bee", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
	assert.Contains(t, err.Error(), "held by an agent on another node")

	name, err := a.client.Spawn(context.Background(), "fake", "tmp-ok", "")
	require.NoError(t, err)
	assert.Equal(t, "tmp-ok", name)
}

func TestTheMasterListsAgentsOnOtherNodesOverMCP(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.startWith(t, "n-a", nil, nil)
	f.startWith(t, "n-b", map[string]string{"bee": ""}, nil)
	directoryHas(t, a, "n-b", 1)

	cs := mcpSession(t, a, token)
	out := mcpCall[struct {
		Agents []struct {
			Name string   `json:"name"`
			Node string   `json:"node"`
			Tags []string `json:"tags"`
		} `json:"agents"`
	}](t, cs, "list_agents", nil)
	require.Len(t, out.Agents, 1)
	assert.Equal(t, "bee", out.Agents[0].Name)
	assert.Equal(t, "n-b", out.Agents[0].Node, "an agent on another node says which")
	assert.Equal(t, []string{"team"}, out.Agents[0].Tags)
}
