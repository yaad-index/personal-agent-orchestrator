package transport

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/yaad-index/personal-agent-orchestrator/internal/mesh/meshpb"
)

// watchNode answers a watch with one entry and then holds the stream open until the
// call ends, and records that it ended and why.
type watchNode struct {
	pb.UnimplementedNodeServer
	ended chan error
}

func newWatchNode() *watchNode { return &watchNode{ended: make(chan error, 4)} }

func (w *watchNode) Watch(req *pb.WatchRequest, stream pb.Node_WatchServer) error {
	if err := stream.Send(&pb.WatchEntry{Text: "first " + req.GetAgent()}); err != nil {
		return err
	}
	<-stream.Context().Done()
	w.ended <- stream.Context().Err()
	return stream.Context().Err()
}

func openWatch(t *testing.T, ctx context.Context, from *Node, to string) pb.Node_WatchClient {
	t.Helper()
	st, err := nodeClient(t, from, to).Watch(ctx, &pb.WatchRequest{Agent: "bee", Level: 1})
	require.NoError(t, err)
	e, err := st.Recv()
	require.NoError(t, err)
	require.Equal(t, "first bee", e.GetText())
	return st
}

func awaitEnded(t *testing.T, w *watchNode, why string) {
	t.Helper()
	select {
	case <-w.ended:
	case <-time.After(3 * time.Second):
		t.Fatalf("the node kept the watch after %s", why)
	}
}

func TestAWatchEndsWhenTheNodesEpochMoves(t *testing.T) {
	c := newCluster(t, "a", "b")
	c.node("a", nil)
	w := newWatchNode()
	b := c.node("b", w)
	c.start("a")
	c.start("b")

	// The context is bounded only so a stream that wrongly stays open fails the test
	// instead of hanging it; the other tests in this file open watches with none.
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	st := openWatch(t, ctx, c.nodes["a"], "b")

	changed, err := b.Membership().Observe(1, "a")
	require.NoError(t, err)
	require.True(t, changed)

	_, err = st.Recv()
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err), "the stream says the epoch moved, it is not a plain cancel")
	assert.Contains(t, err.Error(), "epoch changed")
	awaitEnded(t, w, "its epoch moved")
}

func TestAWatchSurvivesABeliefThatDoesNotChange(t *testing.T) {
	c := newCluster(t, "a", "b")
	c.node("a", nil)
	w := newWatchNode()
	b := c.node("b", w)
	c.start("a")
	c.start("b")
	st := openWatch(t, context.Background(), c.nodes["a"], "b")

	changed, err := b.Membership().Observe(0, "a")
	require.NoError(t, err)
	require.False(t, changed, "the same belief again is no change")
	select {
	case <-w.ended:
		t.Fatal("the watch ended though the epoch did not move")
	case <-time.After(300 * time.Millisecond):
	}
	_ = st
}

func TestAWatchEndsWhenTheMasterStopsItOrTheConnectionDrops(t *testing.T) {
	c := newCluster(t, "a", "b")
	c.node("a", nil)
	w := newWatchNode()
	c.node("b", w)
	c.start("a")
	c.start("b")

	ctx, stop := context.WithCancel(context.Background())
	openWatch(t, ctx, c.nodes["a"], "b")
	stop()
	awaitEnded(t, w, "the master stopped it")

	openWatch(t, context.Background(), c.nodes["a"], "b")
	c.halt("a")
	awaitEnded(t, w, "the master's connection dropped")
}

func TestAWatchIsOnlyTheMastersAndAtTheEpochTheNodeBelievesIn(t *testing.T) {
	c := newCluster(t, "a", "b", "c")
	c.node("a", nil)
	b := c.node("b", nil)
	c.node("c", newWatchNode())
	c.start("a")
	c.start("b")
	c.start("c")

	ctx, cancel := withDeadline()
	defer cancel()
	recv := func(ctx context.Context, cl pb.NodeClient) error {
		st, err := cl.Watch(ctx, &pb.WatchRequest{Agent: "bee", Level: 1})
		require.NoError(t, err)
		_, err = st.Recv()
		return err
	}

	err := recv(ctx, nodeClient(t, b, "c"))
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "a secondary cannot watch through another")

	err = recv(ctx, pb.NewNodeClient(bareConn(t, c, "a", "c")))
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "a watch names its epoch like a command")

	err = recv(withEpoch(ctx, 7), pb.NewNodeClient(bareConn(t, c, "a", "c")))
	assert.Equal(t, codes.FailedPrecondition, status.Code(err), "an epoch the node does not know is refused")

	err = recv(withEpoch(ctx, 0), pb.NewNodeClient(bareConn(t, c, "a", "c")))
	require.NoError(t, err, "the master at the node's epoch is served")
}

func TestAMasterThatIsNotTheMasterAnymoreCannotOpenAWatch(t *testing.T) {
	c := newCluster(t, "a", "b")
	a, b := c.node("a", nil), c.node("b", newWatchNode())
	c.start("a")
	c.start("b")
	ctx, cancel := withDeadline()
	defer cancel()

	_, err := b.Membership().Observe(1, "b")
	require.NoError(t, err)
	st, err := nodeClient(t, a, "b").Watch(ctx, &pb.WatchRequest{Agent: "bee", Level: 1})
	require.NoError(t, err)
	_, err = st.Recv()
	require.Error(t, err)
	assert.Contains(t, []codes.Code{codes.PermissionDenied, codes.FailedPrecondition}, status.Code(err))
}
