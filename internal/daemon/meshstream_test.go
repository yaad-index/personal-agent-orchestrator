package daemon

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/yaad-index/personal-agent-orchestrator/internal/mesh/meshpb"
)

func appendLine(t *testing.T, n *node, agent, line string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(n.dir, "transcripts", agent+".jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(line + "\n")
	require.NoError(t, err)
}

func quiet(t *testing.T, n *node, d time.Duration, why string) {
	t.Helper()
	select {
	case m := <-n.events:
		t.Fatalf("%s: got %q", why, m.Text)
	case <-time.After(d):
	}
}

func TestTheOperatorSetsTheLevelAndPausesTheViewOfAnAgentOnThisNode(t *testing.T) {
	n := startNode(t, map[string]string{"bee": ""})
	ctx := context.Background()
	_, err := n.client.Post(ctx, "human", "wake up", "name", "bee")
	require.NoError(t, err)
	assert.Equal(t, "echo: [human]: wake up", n.next(t).Text)

	appendLine(t, n, "bee", `{"type":"assistant","uuid":"tc1","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`)
	quiet(t, n, 400*time.Millisecond, "a tool call is level 2, the node shows level 1")

	require.NoError(t, n.client.SetStream(ctx, "bee", "local", 2))
	appendLine(t, n, "bee", `{"type":"assistant","uuid":"tc2","message":{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"pwd"}}]}}`)
	assert.Contains(t, n.next(t).Text, "tool Bash", "at level 2 the tool call is shown")

	require.NoError(t, n.client.View(ctx, "bee", "pause", 0, 0))
	_, err = n.client.Post(ctx, "human", "while paused", "name", "bee")
	require.NoError(t, err)
	quiet(t, n, 400*time.Millisecond, "paused")
	require.NoError(t, n.client.View(ctx, "bee", "resume", 0, 0))
	assert.Equal(t, "echo: [human]: while paused", n.next(t).Text, "what the pause held arrives on resume")

	require.NoError(t, n.client.View(ctx, "bee", "backtrack", 2, 5))
	bt := n.next(t)
	assert.Equal(t, "pao", bt.Sender)
	assert.Contains(t, bt.Text, "tool Bash")
	assert.Contains(t, bt.Text, "echo: [human]: wake up")
}

func TestTheOperatorsStreamAndViewRequestsAreRefusedWithAReason(t *testing.T) {
	n := startNode(t, map[string]string{"bee": ""})
	ctx := context.Background()
	for _, c := range []struct {
		name string
		err  error
		code string
	}{
		{"no agent", n.client.SetStream(ctx, "ghost", "local", 1), "404"},
		{"no channel", n.client.SetStream(ctx, "bee", "nowhere", 1), "400"},
		{"no level", n.client.View(ctx, "bee", "backtrack", 0, 0), "400"},
		{"no such agent view", n.client.View(ctx, "ghost", "pause", 0, 0), "404"},
		{"no such action", n.client.View(ctx, "bee", "rewind", 0, 0), "400"},
	} {
		require.Error(t, c.err, c.name)
		assert.Contains(t, c.err.Error(), c.code, c.name)
	}
}

func TestTheStreamRoutesNeedTheToken(t *testing.T) {
	n := startNode(t, map[string]string{"bee": ""})
	for _, c := range []struct{ method, path string }{{"PUT", "/v1/stream/bee"}, {"POST", "/v1/view/bee"}} {
		for _, auth := range []string{"", "Bearer wrong", token} {
			req, err := http.NewRequest(c.method, n.url+c.path, strings.NewReader("{}"))
			require.NoError(t, err)
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			_ = resp.Body.Close()
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "%s %s with %q", c.method, c.path, auth)
		}
	}
}

func TestTheMasterSetsTheViewOfAnAgentOnAnotherNode(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.startWith(t, "n-a", nil, nil)
	b := f.startWith(t, "n-b", map[string]string{"bee": ""}, nil)
	directoryHas(t, a, "n-b", 1)
	ctx := context.Background()

	_, err := a.client.Post(ctx, "human", "hi", "name", "bee")
	require.NoError(t, err)
	assert.Equal(t, "echo: [human]: hi", b.next(t).Text, "its output is on its own node's channel")

	require.NoError(t, a.client.SetStream(ctx, "bee", "local", 2), "the master names it by agent, not by node")
	appendLine(t, b, "bee", `{"type":"assistant","uuid":"tc1","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`)
	assert.Contains(t, b.next(t).Text, "tool Bash", "the node renders at the level the master set")

	require.NoError(t, a.client.View(ctx, "bee", "pause", 0, 0))
	_, err = a.client.Post(ctx, "human", "held", "name", "bee")
	require.NoError(t, err)
	quiet(t, b, 400*time.Millisecond, "paused on its node")
	require.NoError(t, a.client.View(ctx, "bee", "resume", 0, 0))
	assert.Equal(t, "echo: [human]: held", b.next(t).Text)

	require.NoError(t, a.client.View(ctx, "bee", "backtrack", 2, 3))
	assert.Contains(t, b.next(t).Text, "bee, the last")

	err = a.client.SetStream(ctx, "bee", "nowhere", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400", "the node's refusal keeps its meaning")
	err = a.client.SetStream(ctx, "ghost", "local", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
	err = b.client.SetStream(ctx, "bee", "", 0)
	require.NoError(t, err, "an agent of its own node is controlled there, with no master")
}

func TestANodeKeepsAViewForItsViewerAndTheMasterIsTheOneItNames(t *testing.T) {
	f, _, _ := meshWithRecipes(t)
	ctx := context.Background()
	node := asMaster(t, f, "n-b")

	_, err := node.SetStream(ctx, &pb.StreamRequest{RequestId: "s1", Agent: "bee", Channel: "local", Level: 2, Viewer: "alice"})
	require.NoError(t, err)
	_, err = node.View(ctx, &pb.ViewRequest{RequestId: "v1", Viewer: "bob", Agent: "bee", Action: pb.ViewAction_VIEW_ACTION_PAUSE})
	assert.Equal(t, codes.FailedPrecondition, status.Code(err), "bob cannot pause alice's view")
	_, err = node.View(ctx, &pb.ViewRequest{RequestId: "v2", Viewer: "alice", Agent: "bee", Action: pb.ViewAction_VIEW_ACTION_PAUSE})
	require.NoError(t, err)
	_, err = node.View(ctx, &pb.ViewRequest{RequestId: "v3", Viewer: "alice", Agent: "bee", Action: pb.ViewAction_VIEW_ACTION_RESUME})
	require.NoError(t, err)
	_, err = node.View(ctx, &pb.ViewRequest{RequestId: "v4", Viewer: "alice", Agent: "bee"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "an action is required")

	_, err = node.SetStream(ctx, &pb.StreamRequest{RequestId: "s2", Agent: "ghost", Channel: "local"})
	assert.Equal(t, codes.NotFound, status.Code(err))
	_, err = node.SetStream(ctx, &pb.StreamRequest{RequestId: "s3", Agent: "bee", Channel: "nowhere"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestAViewRequestForAnAgentNoNodeHoldsIsNotFound(t *testing.T) {
	_, _, b := meshWithRecipes(t)
	err := b.client.View(context.Background(), "nobody", "pause", 0, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}
