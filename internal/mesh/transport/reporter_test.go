package transport

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/yaad-index/personal-agent-orchestrator/internal/mesh/meshpb"
)

// recSink records what a master is told.
type recSink struct {
	mu     sync.Mutex
	events []string
	agents map[string]map[string]*pb.AgentInfo
}

func newRecSink() *recSink { return &recSink{agents: map[string]map[string]*pb.AgentInfo{}} }

func (s *recSink) log(e string) { s.events = append(s.events, e) }

func (s *recSink) Replace(node string, as []*pb.AgentInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log("replace:" + node)
	s.agents[node] = map[string]*pb.AgentInfo{}
	for _, a := range as {
		s.agents[node][a.GetName()] = a
	}
}
func (s *recSink) Upsert(node string, a *pb.AgentInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log("upsert:" + node + ":" + a.GetName())
	if s.agents[node] == nil {
		s.agents[node] = map[string]*pb.AgentInfo{}
	}
	s.agents[node][a.GetName()] = a
}
func (s *recSink) Remove(node, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log("remove:" + node + ":" + name)
	delete(s.agents[node], name)
}
func (s *recSink) Drop(node string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log("drop:" + node)
	delete(s.agents, node)
}

func (s *recSink) names(node string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for n := range s.agents[node] {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (s *recSink) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

func (s *recSink) get(node, name string) *pb.AgentInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agents[node][name]
}

// fakeSource is a node's list of agents, changeable by the test.
type fakeSource struct {
	mu sync.Mutex
	as map[string]*pb.AgentInfo
}

func (f *fakeSource) set(as ...*pb.AgentInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.as = map[string]*pb.AgentInfo{}
	for _, a := range as {
		f.as[a.GetName()] = a
	}
}

func (f *fakeSource) Snapshot() []*pb.AgentInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*pb.AgentInfo
	for _, a := range f.as {
		out = append(out, a)
	}
	return out
}

func info(name string, tags ...string) *pb.AgentInfo {
	return &pb.AgentInfo{Name: name, Tags: tags, State: pb.AgentState_AGENT_STATE_RUNNING}
}

func runReporter(t *testing.T, n *Node, src Source) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); n.RunReporter(ctx, src, 30*time.Millisecond) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel
}

func TestASecondaryReportsItsAgentsInFullThenOnlyChanges(t *testing.T) {
	c := newCluster(t, "a", "b")
	a := c.node("a", nil)
	sink := newRecSink()
	a.SetSink(sink)
	b := c.node("b", nil)
	c.start("a")
	c.start("b")

	src := &fakeSource{}
	src.set(info("x", "team"), info("y"))
	runReporter(t, b, src)

	require.Eventually(t, func() bool { return len(sink.names("b")) == 2 }, 5*time.Second, 10*time.Millisecond, "the full list arrives when the stream opens")
	assert.Equal(t, []string{"replace:b"}, sink.seen(), "one full report, nothing else yet")

	// A change, an addition and a removal arrive as three deltas, not as a new full list.
	changed := info("x", "team", "ops")
	changed.Flag = "idle"
	src.set(changed, info("z"))
	require.Eventually(t, func() bool {
		n := sink.names("b")
		return len(n) == 2 && n[0] == "x" && n[1] == "z"
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "idle", sink.get("b", "x").GetFlag())
	events := sink.seen()
	assert.Equal(t, "replace:b", events[0])
	assert.ElementsMatch(t, []string{"upsert:b:x", "upsert:b:z", "remove:b:y"}, events[1:], "only what changed, once each")
	assert.Equal(t, 4, a.Reports("b"), "four reports in all: the full list and three deltas")
}

func TestNothingIsSentWhileNothingChanges(t *testing.T) {
	c := newCluster(t, "a", "b")
	a := c.node("a", nil)
	sink := newRecSink()
	a.SetSink(sink)
	b := c.node("b", nil)
	c.start("a")
	c.start("b")
	src := &fakeSource{}
	src.set(info("x"))
	runReporter(t, b, src)
	require.Eventually(t, func() bool { return a.Reports("b") == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, 1, a.Reports("b"), "ten intervals passed with nothing to say")
}

func TestTheMasterForgetsASecondaryWhenItStopsReporting(t *testing.T) {
	c := newCluster(t, "a", "b")
	a := c.node("a", nil)
	sink := newRecSink()
	a.SetSink(sink)
	b := c.node("b", nil)
	c.start("a")
	c.start("b")
	src := &fakeSource{}
	src.set(info("x"))
	stop := runReporter(t, b, src)
	require.Eventually(t, func() bool { return len(sink.names("b")) == 1 }, 5*time.Second, 10*time.Millisecond)

	stop()
	require.Eventually(t, func() bool { return len(sink.names("b")) == 0 }, 5*time.Second, 10*time.Millisecond, "the stream ended, so what it reported is dropped")
	assert.Contains(t, sink.seen(), "drop:b")
}

func TestASecondaryReportsInFullAgainWhenTheMasterReturns(t *testing.T) {
	c := newCluster(t, "a", "b")
	a := c.node("a", nil)
	sink := newRecSink()
	a.SetSink(sink)
	b := c.node("b", nil)
	c.start("a")
	c.start("b")
	src := &fakeSource{}
	src.set(info("x"), info("y"))
	runReporter(t, b, src)
	require.Eventually(t, func() bool { return len(sink.names("b")) == 2 }, 5*time.Second, 10*time.Millisecond)

	// The master restarts (a new process, with an empty directory).
	addr := c.lns["a"].Addr().String()
	c.halt("a")
	sink2 := newRecSink()
	ln, err := listenOn(addr)
	require.NoError(t, err)
	c.lns["a"] = ln
	a2 := c.node("a", nil)
	a2.SetSink(sink2)
	c.start("a")

	require.Eventually(t, func() bool { return len(sink2.names("b")) == 2 }, 20*time.Second, 25*time.Millisecond, "the new master is told everything, not just changes")
	assert.Equal(t, "replace:b", sink2.seen()[0])
}

func TestAnAgentIsReportedToTheNewMasterWhenTheMasterChanges(t *testing.T) {
	c := newCluster(t, "a", "b", "c")
	a := c.node("a", nil)
	sinkA := newRecSink()
	a.SetSink(sinkA)
	b := c.node("b", nil)
	cn := c.node("c", nil)
	sinkC := newRecSink()
	cn.SetSink(sinkC)
	c.start("a")
	c.start("b")
	c.start("c")
	require.Eventually(t, func() bool { return up(b, "a") && up(b, "c") }, 5*time.Second, 10*time.Millisecond)

	src := &fakeSource{}
	src.set(info("x"))
	runReporter(t, b, src)
	require.Eventually(t, func() bool { return len(sinkA.names("b")) == 1 }, 5*time.Second, 10*time.Millisecond)

	// c is promoted (its own record first, then the announcement).
	_, err := cn.Membership().Observe(1, "c")
	require.NoError(t, err)
	connCB, err := cn.Dial("b")
	require.NoError(t, err)
	ctx, cancel := withDeadline()
	defer cancel()
	_, err = pb.NewPeerClient(connCB).NewMaster(ctx, &pb.NewMasterNotice{Epoch: 1, MasterId: "c"})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(sinkC.names("b")) == 1 }, 15*time.Second, 25*time.Millisecond, "b moved its session to the new master and reported in full")
	require.Eventually(t, func() bool { return len(sinkA.names("b")) == 0 }, 5*time.Second, 10*time.Millisecond, "and a, no longer master, dropped it")
}

func TestAMasterReportsToNoOne(t *testing.T) {
	c := newCluster(t, "a", "b")
	a := c.node("a", nil)
	sinkB := newRecSink()
	c.node("b", nil).SetSink(sinkB)
	c.start("a")
	c.start("b")
	src := &fakeSource{}
	src.set(info("local-agent"))
	runReporter(t, a, src)
	time.Sleep(300 * time.Millisecond)
	assert.Empty(t, sinkB.seen(), "the master's own agents are not reported to a secondary")
	assert.Equal(t, 0, c.nodes["b"].Reports("a"))
}

func TestAPartialReportAddsOrChangesAndDoesNotReplace(t *testing.T) {
	c := newCluster(t, "a", "b")
	a := c.node("a", nil)
	sink := newRecSink()
	a.SetSink(sink)
	c.node("b", nil)
	c.start("a")
	c.start("b")

	conn, err := c.nodes["b"].Dial("a")
	require.NoError(t, err)
	ctx, cancel := withDeadline()
	defer cancel()
	stream, err := pb.NewMasterClient(conn).Session(ctx)
	require.NoError(t, err)
	send := func(r *pb.Report) {
		require.NoError(t, stream.Send(r))
		_, err := stream.Recv()
		require.NoError(t, err)
	}
	send(&pb.Report{Seq: 1, Body: &pb.Report_Agents{Agents: &pb.AgentsReport{Full: true, Agents: []*pb.AgentInfo{info("x")}}}})
	send(&pb.Report{Seq: 2, Body: &pb.Report_Agents{Agents: &pb.AgentsReport{Full: false, Agents: []*pb.AgentInfo{info("y")}}}})

	assert.Equal(t, []string{"x", "y"}, sink.names("b"), "a partial report adds y without forgetting x")
	assert.Equal(t, []string{"replace:b", "upsert:b:y"}, sink.seen())
	_ = stream.CloseSend()
}

func TestASupersededSessionCannotDropOrOverwriteTheNewOnesReport(t *testing.T) {
	c := newCluster(t, "a", "b")
	a := c.node("a", nil)
	sink := newRecSink()
	a.SetSink(sink)
	c.node("b", nil)
	c.start("a")
	c.start("b")

	conn, err := c.nodes["b"].Dial("a")
	require.NoError(t, err)
	open := func() (pb.Master_SessionClient, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		s, err := pb.NewMasterClient(conn).Session(ctx)
		require.NoError(t, err)
		return s, cancel
	}
	report := func(s pb.Master_SessionClient, seq uint64, full bool, names ...string) error {
		var as []*pb.AgentInfo
		for _, n := range names {
			as = append(as, info(n))
		}
		if err := s.Send(&pb.Report{Seq: seq, Body: &pb.Report_Agents{Agents: &pb.AgentsReport{Full: full, Agents: as}}}); err != nil {
			return err
		}
		_, err := s.Recv()
		return err
	}

	// The node's old stream is still open on the master (a half-open connection the
	// master has not noticed is dead) when the node reconnects and reports in full.
	old, cancelOld := open()
	require.NoError(t, report(old, 1, true, "x"))
	fresh, cancelFresh := open()
	defer cancelFresh()
	require.NoError(t, report(fresh, 1, true, "x", "y"))
	require.Equal(t, []string{"x", "y"}, sink.names("b"))

	// A late report on the old stream, still open, is refused rather than applied over
	// the new one.
	err = report(old, 2, true, "stale")
	require.Error(t, err)
	assert.Equal(t, codes.Aborted, status.Code(err))
	assert.Equal(t, []string{"x", "y"}, sink.names("b"), "a superseded stream cannot overwrite the current report")

	// The old stream finally ends. It must not take the new stream's agents with it.
	cancelOld()
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, []string{"x", "y"}, sink.names("b"), "the superseded stream ending did not drop the new stream's report")
	assert.NotContains(t, sink.seen(), "drop:b")

	// The current stream ending still drops, as it should.
	cancelFresh()
	require.Eventually(t, func() bool { return len(sink.names("b")) == 0 }, 5*time.Second, 10*time.Millisecond)
	assert.Contains(t, sink.seen(), "drop:b")
}
