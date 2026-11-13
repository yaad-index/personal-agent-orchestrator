package transport

import (
	"context"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
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

const beat = 40 * time.Millisecond

type cluster struct {
	t     *testing.T
	ids   map[string]*mesh.Identity
	doc   *mesh.Document
	lns   map[string]net.Listener
	nodes map[string]*Node
	stop  map[string]context.CancelFunc
	dirs  map[string]string
	done  map[string]chan struct{}
}

// newCluster makes identities, listeners and a mesh document for the named nodes,
// with the first as the starting master. No node is serving yet.
func newCluster(t *testing.T, names ...string) *cluster {
	t.Helper()
	c := &cluster{
		t: t, ids: map[string]*mesh.Identity{}, lns: map[string]net.Listener{}, nodes: map[string]*Node{},
		stop: map[string]context.CancelFunc{}, dirs: map[string]string{}, done: map[string]chan struct{}{},
		doc: &mesh.Document{Master: names[0]},
	}
	for _, n := range names {
		id, err := mesh.Generate(filepath.Join(t.TempDir(), "mesh.key"), n)
		require.NoError(t, err)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		c.ids[n], c.lns[n], c.dirs[n] = id, ln, t.TempDir()
		c.doc.Nodes = append(c.doc.Nodes, mesh.NodeEntry{ID: n, Addr: ln.Addr().String(), Fingerprint: id.Fingerprint})
	}
	require.NoError(t, c.doc.Validate())
	t.Cleanup(c.stopAll)
	return c
}

func (c *cluster) node(name string, srv pb.NodeServer) *Node {
	c.t.Helper()
	n, err := New(Config{Self: c.ids[name], Doc: c.doc, StateDir: c.dirs[name], Listener: c.lns[name], BeatEvery: beat})
	require.NoError(c.t, err)
	if srv != nil {
		n.SetNodeServer(srv)
	}
	c.nodes[name] = n
	return n
}

func (c *cluster) start(name string) {
	c.t.Helper()
	n := c.nodes[name]
	ctx, cancel := context.WithCancel(context.Background())
	c.stop[name] = cancel
	done := make(chan struct{})
	c.done[name] = done
	go func() {
		defer close(done)
		_ = n.Serve(ctx)
	}()
	require.Eventually(c.t, func() bool { return n.Addr() != nil }, 3*time.Second, 5*time.Millisecond)
}

func (c *cluster) halt(name string) {
	if cancel, ok := c.stop[name]; ok {
		cancel()
		<-c.done[name]
		delete(c.stop, name)
	}
}

func (c *cluster) stopAll() {
	for name := range c.stop {
		c.halt(name)
	}
}

func up(n *Node, peer string) bool {
	for _, p := range n.Status().Peers {
		if p.ID == peer {
			return p.Up
		}
	}
	return false
}

func withDeadline() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*time.Second)
}

// --- liveness ---

func TestNodesSeeEachOthersLivenessAndRole(t *testing.T) {
	c := newCluster(t, "a", "b")
	a, b := c.node("a", nil), c.node("b", nil)
	c.start("a")
	c.start("b")

	require.Eventually(t, func() bool { return up(a, "b") && up(b, "a") }, 5*time.Second, 10*time.Millisecond, "the lower id dials the higher; both ends then see the other")
	sa := a.Status()
	assert.Equal(t, "master", sa.Role)
	assert.Equal(t, "a", sa.Master)
	assert.Equal(t, uint64(0), sa.Epoch)
	require.Len(t, sa.Peers, 1)
	assert.Equal(t, "secondary", sa.Peers[0].Role, "a learned b's role from its heartbeat")
	assert.Equal(t, "a", sa.Peers[0].Master)
	assert.Equal(t, "secondary", b.Status().Role)
}

func TestAPeerThatStopsIsSeenToBeDown(t *testing.T) {
	c := newCluster(t, "a", "b")
	a := c.node("a", nil)
	c.node("b", nil)
	c.start("a")
	c.start("b")
	require.Eventually(t, func() bool { return up(a, "b") }, 5*time.Second, 10*time.Millisecond)

	c.halt("b")
	require.Eventually(t, func() bool { return !up(a, "b") }, 5*time.Second, 10*time.Millisecond, "no heartbeat for three intervals, and no stream")
}

func TestAPeerNeverStartedIsDownNotUnknownToTheList(t *testing.T) {
	c := newCluster(t, "a", "b")
	a := c.node("a", nil)
	c.start("a")
	st := a.Status()
	require.Len(t, st.Peers, 1)
	assert.False(t, st.Peers[0].Up)
	assert.Equal(t, "unknown", st.Peers[0].Role)
	assert.Equal(t, c.doc.Nodes[1].Addr, st.Peers[0].Addr)
}

// --- who may call what ---

func nodeClient(t *testing.T, from *Node, to string) pb.NodeClient {
	t.Helper()
	conn, err := from.Dial(to)
	require.NoError(t, err)
	return pb.NewNodeClient(conn)
}

func TestOnlyTheMasterMayCommandANode(t *testing.T) {
	c := newCluster(t, "a", "b", "c")
	c.node("a", nil)
	b, cc := c.node("b", nil), c.node("c", nil)
	_ = b
	c.start("a")
	c.start("b")
	c.start("c")

	ctx, cancel := withDeadline()
	defer cancel()
	// The master (a) reaches the service: nothing implements it yet.
	_, err := nodeClient(t, c.nodes["a"], "c").Launch(ctx, &pb.AgentRef{RequestId: "r1", Agent: "x"})
	assert.Equal(t, codes.Unimplemented, status.Code(err), "the master's command got past the interceptors")

	// A secondary (b) is refused.
	_, err = nodeClient(t, b, "c").Launch(ctx, &pb.AgentRef{RequestId: "r2", Agent: "x"})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "a secondary cannot command anyone")
	assert.Contains(t, err.Error(), `"b" is not the master`)

	// Nor can a node command the master.
	_, err = nodeClient(t, cc, "a").Launch(ctx, &pb.AgentRef{RequestId: "r3", Agent: "x"})
	assert.Equal(t, codes.FailedPrecondition, status.Code(err), "the master is not commanded")
}

func TestACommandWithoutADeadlineIsRefused(t *testing.T) {
	c := newCluster(t, "a", "b")
	a := c.node("a", nil)
	c.node("b", nil)
	c.start("a")
	c.start("b")
	_ = a
	conn := bareConn(t, c, "a", "b")

	// The dialer's own interceptor adds a default deadline, so to send a call with
	// none the call goes around it on a bare connection.
	err := conn.Invoke(withEpoch(context.Background(), 0), "/pao.mesh.v1.Node/Launch", &pb.AgentRef{RequestId: "r1", Agent: "x"}, &pb.Result{}, grpc.StaticMethod())
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "deadline")
}

// bareConn dials a node with the mesh's TLS but without the dialer's interceptor, so
// a test can send a command with exactly the epoch (or none) it chooses.
func bareConn(t *testing.T, c *cluster, from, to string) *grpc.ClientConn {
	t.Helper()
	var addr string
	for _, n := range c.doc.Nodes {
		if n.ID == to {
			addr = n.Addr
		}
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(mesh.ClientCredentials(c.ids[from], c.doc.Peers(), to)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func withEpoch(ctx context.Context, e uint64) context.Context {
	return metadata.AppendToOutgoingContext(ctx, epochHeader, itoa(e))
}

func itoa(e uint64) string {
	if e == 0 {
		return "0"
	}
	var b []byte
	for e > 0 {
		b = append([]byte{byte('0' + e%10)}, b...)
		e /= 10
	}
	return string(b)
}

func TestACommandAtTheWrongEpochOrWithNoneIsRefused(t *testing.T) {
	c := newCluster(t, "a", "b")
	c.node("a", nil)
	c.node("b", nil)
	c.start("a")
	c.start("b")
	conn := bareConn(t, c, "a", "b")
	ctx, cancel := withDeadline()
	defer cancel()

	// b is at epoch 0; a command at epoch 7 names an epoch it does not know.
	err := conn.Invoke(withEpoch(ctx, 7), "/pao.mesh.v1.Node/Launch", &pb.AgentRef{RequestId: "r1", Agent: "x"}, &pb.Result{})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Contains(t, err.Error(), "not one this node knows")

	// A command with no epoch at all is malformed.
	err = conn.Invoke(ctx, "/pao.mesh.v1.Node/Launch", &pb.AgentRef{RequestId: "r2", Agent: "x"}, &pb.Result{})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "pao-epoch")
}

func TestACommandFromAnOlderEpochIsStale(t *testing.T) {
	c := newCluster(t, "a", "b")
	c.node("a", nil)
	b := c.node("b", nil)
	c.start("a")
	c.start("b")
	// b holds epoch 4 under master a (a was promoted again at a later epoch); a
	// command carrying the old epoch 0 is stale.
	_, err := b.Membership().Observe(4, "a")
	require.NoError(t, err)
	conn := bareConn(t, c, "a", "b")
	ctx, cancel := withDeadline()
	defer cancel()

	err = conn.Invoke(withEpoch(ctx, 0), "/pao.mesh.v1.Node/Launch", &pb.AgentRef{RequestId: "r1", Agent: "x"}, &pb.Result{})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Contains(t, err.Error(), "stale epoch 0")
}

func TestAStaleMasterIsRefusedAfterANewOneIsAnnounced(t *testing.T) {
	c := newCluster(t, "a", "b")
	a, b := c.node("a", nil), c.node("b", nil)
	c.start("a")
	c.start("b")
	require.Eventually(t, func() bool { return up(a, "b") && up(b, "a") }, 5*time.Second, 10*time.Millisecond)

	// b becomes master at epoch 1 (its own record first, as a promotion does) and
	// announces it; a, which was master, yields.
	ctx, cancel := withDeadline()
	defer cancel()
	_, err := b.Membership().Observe(1, "b")
	require.NoError(t, err)
	connBA, err := b.Dial("a")
	require.NoError(t, err)
	_, err = pb.NewPeerClient(connBA).NewMaster(ctx, &pb.NewMasterNotice{Epoch: 1, MasterId: "b"})
	require.NoError(t, err)
	assert.False(t, a.Membership().IsMaster(), "a heard of a master that supersedes it and yielded")
	e, m := a.Membership().View()
	assert.Equal(t, uint64(1), e)
	assert.Equal(t, "b", m)

	// a's command to b is now refused: a is not master, and says so as the caller sees it.
	_, err = nodeClient(t, a, "b").Launch(ctx, &pb.AgentRef{RequestId: "r1", Agent: "x"})
	require.Error(t, err)
	assert.Contains(t, []codes.Code{codes.PermissionDenied, codes.FailedPrecondition}, status.Code(err))

	// b, master now, commands a: accepted past the interceptors.
	_, err = nodeClient(t, b, "a").Launch(ctx, &pb.AgentRef{RequestId: "r2", Agent: "x"})
	assert.Equal(t, codes.Unimplemented, status.Code(err))
}

func TestOnlyANodeCanAnnounceItselfAsMaster(t *testing.T) {
	c := newCluster(t, "a", "b", "c")
	a := c.node("a", nil)
	b := c.node("b", nil)
	c.node("c", nil)
	c.start("a")
	c.start("b")
	c.start("c")

	ctx, cancel := withDeadline()
	defer cancel()
	conn, err := b.Dial("a")
	require.NoError(t, err)
	_, err = pb.NewPeerClient(conn).NewMaster(ctx, &pb.NewMasterNotice{Epoch: 5, MasterId: "c"})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "b cannot make c master")
	e, m := a.Membership().View()
	assert.Equal(t, uint64(0), e)
	assert.Equal(t, "a", m, "nothing changed")
}

func TestTheMasterAnswersSessionsAndASecondaryDoesNot(t *testing.T) {
	c := newCluster(t, "a", "b", "c")
	a := c.node("a", nil)
	b := c.node("b", nil)
	c.node("c", nil)
	c.start("a")
	c.start("b")
	c.start("c")

	conn, err := b.Dial("a")
	require.NoError(t, err)
	ctx, cancel := withDeadline()
	defer cancel()
	stream, err := pb.NewMasterClient(conn).Session(ctx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&pb.Report{Seq: 1, Body: &pb.Report_Agents{Agents: &pb.AgentsReport{Full: true}}}))
	ack, err := stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, uint64(1), ack.GetSeq())
	require.NoError(t, stream.Send(&pb.Report{Seq: 2}))
	ack, err = stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, uint64(2), ack.GetSeq())
	assert.Equal(t, 2, a.Reports("b"))
	_ = stream.CloseSend()

	// c reporting to b, which is not the master, is refused.
	connCB, err := c.nodes["c"].Dial("b")
	require.NoError(t, err)
	ctx2, cancel2 := withDeadline()
	defer cancel2()
	s2, err := pb.NewMasterClient(connCB).Session(ctx2)
	require.NoError(t, err)
	_, err = s2.Recv()
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
}

// --- Hello, Probe, MasterLost ---

func TestHelloRefusesAPeerWithNoVersionInCommon(t *testing.T) {
	c := newCluster(t, "a", "b")
	a := c.node("a", nil)
	c.node("b", nil)
	c.start("a")
	c.start("b")
	conn, err := a.Dial("b")
	require.NoError(t, err)
	ctx, cancel := withDeadline()
	defer cancel()

	reply, err := pb.NewPeerClient(conn).Hello(ctx, &pb.HelloRequest{Minors: []uint32{0, 9}})
	require.NoError(t, err)
	assert.Equal(t, Minors, reply.GetMinors())
	assert.Equal(t, "a", reply.GetMasterId())

	_, err = pb.NewPeerClient(conn).Hello(ctx, &pb.HelloRequest{Minors: []uint32{9}})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	_, err = pb.NewPeerClient(conn).Hello(ctx, &pb.HelloRequest{})
	assert.Equal(t, codes.FailedPrecondition, status.Code(err), "a peer that offers no version shares none")
}

func TestProbeSaysWhatThePeerBelievesAboutTheMaster(t *testing.T) {
	c := newCluster(t, "a", "b", "c")
	a := c.node("a", nil)
	b := c.node("b", nil)
	c.node("c", nil)
	c.start("a")
	c.start("b")
	c.start("c")
	require.Eventually(t, func() bool { return up(b, "a") }, 5*time.Second, 10*time.Millisecond)

	probe := func(from *Node, to string) *pb.ProbeReply {
		conn, err := from.Dial(to)
		require.NoError(t, err)
		ctx, cancel := withDeadline()
		defer cancel()
		r, err := pb.NewPeerClient(conn).Probe(ctx, &pb.ProbeRequest{})
		require.NoError(t, err)
		return r
	}
	r := probe(a, "b")
	assert.Equal(t, "a", r.GetMasterId())
	assert.True(t, r.GetHasLiveMaster(), "b has a live connection to the master")
	r = probe(b, "a")
	assert.True(t, r.GetHasLiveMaster(), "the master reports itself live")

	c.halt("a")
	require.Eventually(t, func() bool { return !up(b, "a") }, 5*time.Second, 10*time.Millisecond)
	r = probe(c.nodes["c"], "b")
	assert.Equal(t, "a", r.GetMasterId())
	assert.False(t, r.GetHasLiveMaster(), "b still believes in a, but cannot see it")
}

func TestAMasterLostAnnouncementIsRecordedWithItsAnnouncer(t *testing.T) {
	c := newCluster(t, "a", "b")
	c.node("a", nil)
	b := c.node("b", nil)
	c.start("a")
	c.start("b")
	conn, err := c.nodes["a"].Dial("b")
	require.NoError(t, err)
	ctx, cancel := withDeadline()
	defer cancel()
	_, err = pb.NewPeerClient(conn).MasterLost(ctx, &pb.MasterLostNotice{Epoch: 2, MasterId: "x", Announcer: "someone-else"})
	require.NoError(t, err)
	got := b.LostNotices()
	require.Len(t, got, 1)
	assert.Equal(t, "a", got[0].Announcer, "the announcer is the verified caller, not the name in the message")
	assert.Equal(t, uint64(2), got[0].Epoch)
}

// --- beliefs spread, and persist ---

func TestABeliefInAHeartbeatSpreadsAndIsPersisted(t *testing.T) {
	c := newCluster(t, "a", "b", "c")
	a, b := c.node("a", nil), c.node("b", nil)
	c.node("c", nil)
	c.start("a")
	c.start("b")
	c.start("c")
	require.Eventually(t, func() bool { return up(a, "b") && up(b, "c") }, 5*time.Second, 10*time.Millisecond)

	// c promotes itself at epoch 3 and tells only b.
	cNode := c.nodes["c"]
	changed, err := cNode.Membership().Observe(3, "c")
	require.NoError(t, err)
	require.True(t, changed)
	connCB, err := cNode.Dial("b")
	require.NoError(t, err)
	ctx, cancel := withDeadline()
	defer cancel()
	_, err = pb.NewPeerClient(connCB).NewMaster(ctx, &pb.NewMasterNotice{Epoch: 3, MasterId: "c"})
	require.NoError(t, err)

	// a never heard from c, but its heartbeats with b carry the belief.
	require.Eventually(t, func() bool {
		e, m := a.Membership().View()
		return e == 3 && m == "c"
	}, 5*time.Second, 10*time.Millisecond, "a learned of the new master from b's heartbeat")
	assert.False(t, a.Membership().IsMaster(), "a yielded without being told directly")

	// And it survives a restart of a.
	c.halt("a")
	again, err := New(Config{Self: c.ids["a"], Doc: c.doc, StateDir: c.dirs["a"], Listener: c.lns["a"], BeatEvery: beat})
	require.NoError(t, err)
	e, m := again.Membership().View()
	assert.Equal(t, uint64(3), e)
	assert.Equal(t, "c", m)
}

func TestABeliefNamingAnUnlistedMasterIsIgnored(t *testing.T) {
	c := newCluster(t, "a", "b")
	a := c.node("a", nil)
	c.node("b", nil)
	c.start("a")
	c.start("b")
	conn, err := c.nodes["b"].Dial("a")
	require.NoError(t, err)
	ctx, cancel := withDeadline()
	defer cancel()
	_, err = pb.NewPeerClient(conn).Hello(ctx, &pb.HelloRequest{Epoch: 99, MasterId: "ghost", Minors: Minors})
	require.NoError(t, err)
	e, m := a.Membership().View()
	assert.Equal(t, uint64(0), e)
	assert.Equal(t, "a", m)
}

func TestANodeWithTheWrongKeyRefusesToStart(t *testing.T) {
	c := newCluster(t, "a", "b")
	other, err := mesh.Generate(filepath.Join(t.TempDir(), "other.key"), "a")
	require.NoError(t, err)
	_, err = New(Config{Self: other, Doc: c.doc, StateDir: t.TempDir(), Listener: c.lns["a"]})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not the one the mesh document lists")

	stranger, err := mesh.Generate(filepath.Join(t.TempDir(), "s.key"), "zed")
	require.NoError(t, err)
	_, err = New(Config{Self: stranger, Doc: c.doc, StateDir: t.TempDir()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in the mesh document")
}

func TestNodesDoNotDialThemselvesOrStrangers(t *testing.T) {
	c := newCluster(t, "a", "b")
	a := c.node("a", nil)
	_, err := a.Dial("a")
	assert.Error(t, err)
	_, err = a.Dial("nobody")
	assert.Error(t, err)
}

// --- idempotency and deadlines ---

// countingNode counts how often each command runs.
type countingNode struct {
	pb.UnimplementedNodeServer
	launches atomic.Int32
	fail     atomic.Bool
	gate     chan struct{}
	sawDL    atomic.Bool
}

func (s *countingNode) Launch(ctx context.Context, r *pb.AgentRef) (*pb.Result, error) {
	s.launches.Add(1)
	if _, ok := ctx.Deadline(); ok {
		s.sawDL.Store(true)
	}
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.fail.Load() {
		return nil, status.Error(codes.Unavailable, "try again")
	}
	return &pb.Result{}, nil
}

func (s *countingNode) Kill(context.Context, *pb.AgentRef) (*pb.Result, error) {
	return nil, status.Error(codes.NotFound, "no such agent")
}

func TestARepeatedCommandRunsOnce(t *testing.T) {
	c := newCluster(t, "a", "b")
	a := c.node("a", nil)
	srv := &countingNode{}
	c.node("b", srv)
	c.start("a")
	c.start("b")
	client := nodeClient(t, a, "b")
	ctx, cancel := withDeadline()
	defer cancel()

	for i := 0; i < 3; i++ {
		_, err := client.Launch(ctx, &pb.AgentRef{RequestId: "same", Agent: "x"})
		require.NoError(t, err)
	}
	assert.EqualValues(t, 1, srv.launches.Load(), "a retry with the same request id does not run the command again")
	_, err := client.Launch(ctx, &pb.AgentRef{RequestId: "other", Agent: "x"})
	require.NoError(t, err)
	assert.EqualValues(t, 2, srv.launches.Load(), "a different request id is a different command")
	assert.True(t, srv.sawDL.Load(), "the handler runs under the caller's deadline")
}

func TestADefiniteFailureIsRememberedAndATransientOneIsNot(t *testing.T) {
	c := newCluster(t, "a", "b")
	a := c.node("a", nil)
	srv := &countingNode{}
	c.node("b", srv)
	c.start("a")
	c.start("b")
	client := nodeClient(t, a, "b")
	ctx, cancel := withDeadline()
	defer cancel()

	for i := 0; i < 2; i++ {
		_, err := client.Kill(ctx, &pb.AgentRef{RequestId: "k", Agent: "x"})
		assert.Equal(t, codes.NotFound, status.Code(err))
	}

	srv.fail.Store(true)
	_, err := client.Launch(ctx, &pb.AgentRef{RequestId: "t", Agent: "x"})
	assert.Equal(t, codes.Unavailable, status.Code(err))
	srv.fail.Store(false)
	_, err = client.Launch(ctx, &pb.AgentRef{RequestId: "t", Agent: "x"})
	require.NoError(t, err, "the retry of a call that failed transiently does run")
	assert.EqualValues(t, 2, srv.launches.Load())
}

func TestAnInFlightRepeatWaitsForTheFirst(t *testing.T) {
	c := newCluster(t, "a", "b")
	a := c.node("a", nil)
	srv := &countingNode{gate: make(chan struct{})}
	c.node("b", srv)
	c.start("a")
	c.start("b")
	client := nodeClient(t, a, "b")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := withDeadline()
			defer cancel()
			_, errs[i] = client.Launch(ctx, &pb.AgentRef{RequestId: "slow", Agent: "x"})
		}()
	}
	require.Eventually(t, func() bool { return srv.launches.Load() >= 1 }, 3*time.Second, 5*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	assert.EqualValues(t, 1, srv.launches.Load(), "the second request did not start a second run")
	close(srv.gate)
	wg.Wait()
	assert.NoError(t, errs[0])
	assert.NoError(t, errs[1])
	assert.EqualValues(t, 1, srv.launches.Load())
}

func TestTheDialerGivesACallWithNoDeadlineADefaultOne(t *testing.T) {
	c := newCluster(t, "a", "b")
	a := c.node("a", nil)
	srv := &countingNode{}
	c.node("b", srv)
	c.start("a")
	c.start("b")
	_, err := nodeClient(t, a, "b").Launch(context.Background(), &pb.AgentRef{RequestId: "nodl", Agent: "x"})
	require.NoError(t, err, "a caller that set no deadline is not refused: the dialer sets one")
	assert.True(t, srv.sawDL.Load())
}

func TestTheIdempotencyCacheForgetsOldEntriesAndIsBounded(t *testing.T) {
	cache := newIdempotency(2, time.Minute)
	now := time.Now()
	cache.now = func() time.Time { return now }
	calls := 0
	h := func(context.Context, any) (any, error) { calls++; return &pb.Result{}, nil }
	run := func(id string) {
		_, err := cache.do(context.Background(), "a", "/m", &pb.AgentRef{RequestId: id}, h)
		require.NoError(t, err)
	}
	run("1")
	run("2")
	run("3") // evicts "1"
	run("1")
	assert.Equal(t, 4, calls, "an entry evicted for size runs again")
	run("3")
	assert.Equal(t, 4, calls, "a recent one does not")
	now = now.Add(2 * time.Minute)
	run("3")
	assert.Equal(t, 5, calls, "an expired one runs again")
}

func TestARequestWithoutAnIDIsNotCached(t *testing.T) {
	cache := newIdempotency(10, time.Minute)
	calls := 0
	h := func(context.Context, any) (any, error) { calls++; return &pb.Result{}, nil }
	for i := 0; i < 3; i++ {
		_, err := cache.do(context.Background(), "a", "/m", &pb.ConfigBundle{Version: 1}, h)
		require.NoError(t, err)
		_, err = cache.do(context.Background(), "a", "/m", &pb.AgentRef{}, h)
		require.NoError(t, err)
	}
	assert.Equal(t, 6, calls)
}

func TestTheDialingSideReconnectsWhenAPeerComesBack(t *testing.T) {
	c := newCluster(t, "a", "b")
	a := c.node("a", nil)
	c.node("b", nil)
	c.start("a")
	c.start("b")
	require.Eventually(t, func() bool { return up(a, "b") }, 5*time.Second, 10*time.Millisecond)

	addr := c.lns["b"].Addr().String()
	c.halt("b")
	require.Eventually(t, func() bool { return !up(a, "b") }, 5*time.Second, 10*time.Millisecond)

	// b restarts on the same address, as a restarted node does.
	ln, err := listenOn(addr)
	require.NoError(t, err)
	c.lns["b"] = ln
	c.node("b", nil)
	c.start("b")
	require.Eventually(t, func() bool { return up(a, "b") }, 15*time.Second, 20*time.Millisecond, "a dials b again with backoff and the peer is up")
}

// listenOn listens on an address that was just released, retrying while it is
// still held.
func listenOn(addr string) (net.Listener, error) {
	var ln net.Listener
	var err error
	for i := 0; i < 50; i++ {
		if ln, err = net.Listen("tcp", addr); err == nil {
			return ln, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil, err
}
