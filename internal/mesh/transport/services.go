package transport

import (
	"context"
	"errors"
	"io"
	"slices"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/yaad-index/personal-agent-orchestrator/internal/mesh/meshpb"
)

// --- Master ---

type masterServer struct {
	pb.UnimplementedMasterServer
	n *Node
}

// Session is where a secondary reports to the master. The interceptor has already
// refused it unless this node is master. Each report goes to the sink (the
// directory) and is acknowledged.
func (s masterServer) Session(stream pb.Master_SessionServer) error {
	caller, _ := CallerFrom(stream.Context())
	gen := s.n.beginSession(caller)
	// What a node reported is only good while it is reporting: when its current
	// stream ends, whether the node left or was cut off, the master forgets it and
	// waits for the node's next full report. A stream that has been superseded by a
	// newer one from the same node (the node reconnected while this one was still
	// waiting to notice it was dead) does not: the newer stream's report is the truth.
	defer func() {
		if s.n.sink != nil && s.n.endSession(caller, gen) {
			s.n.sink.Drop(caller)
		}
	}()
	for {
		r, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if !s.n.isCurrentSession(caller, gen) {
			return status.Error(codes.Aborted, "superseded by a newer session from this node")
		}
		s.n.apply(caller, r)
		s.n.noteReport(caller, r.GetSeq())
		if err := stream.Send(&pb.Ack{Seq: r.GetSeq()}); err != nil {
			return err
		}
	}
}

// beginSession registers a new report stream from a node and returns its
// generation. A node has one current stream: the newest.
func (n *Node) beginSession(caller string) uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.gens[caller]++
	return n.gens[caller]
}

func (n *Node) isCurrentSession(caller string, gen uint64) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.gens[caller] == gen
}

// endSession ends a stream and reports whether it was still the node's current
// one, which is when the node's reports should be dropped with it.
func (n *Node) endSession(caller string, gen uint64) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.gens[caller] == gen
}

func (n *Node) noteReport(caller string, seq uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	s := n.sessions[caller]
	if s == nil {
		s = &session{}
		n.sessions[caller] = s
	}
	s.reports++
	s.lastSeq = seq
	s.last = time.Now()
}

// Reports returns how many reports the master has had from a node.
func (n *Node) Reports(from string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	if s := n.sessions[from]; s != nil {
		return s.reports
	}
	return 0
}

// --- Peer ---

type peerServer struct {
	pb.UnimplementedPeerServer
	n *Node
}

// Hello exchanges what each node believes and refuses a peer it shares no protocol
// minor version with.
func (s peerServer) Hello(ctx context.Context, req *pb.HelloRequest) (*pb.HelloReply, error) {
	caller, _ := CallerFrom(ctx)
	if !shareMinor(req.GetMinors()) {
		return nil, status.Errorf(codes.FailedPrecondition, "no protocol version in common: this node speaks %v, the caller %v", Minors, req.GetMinors())
	}
	s.n.observe(caller, req.GetEpoch(), req.GetMasterId())
	epoch, master := s.n.mem.View()
	return &pb.HelloReply{Role: s.n.selfRole(), Epoch: epoch, MasterId: master, Minors: Minors}, nil
}

func shareMinor(theirs []uint32) bool {
	for _, m := range theirs {
		if slices.Contains(Minors, m) {
			return true
		}
	}
	return false
}

// Liveness is the heartbeat stream: each side sends a beat every interval and
// takes in the other's.
func (s peerServer) Liveness(stream pb.Peer_LivenessServer) error {
	caller, _ := CallerFrom(stream.Context())
	return s.n.runLiveness(stream.Context(), caller, stream)
}

// MasterLost records an announcement that the master is lost.
func (s peerServer) MasterLost(ctx context.Context, req *pb.MasterLostNotice) (*pb.Result, error) {
	caller, _ := CallerFrom(ctx)
	s.n.mu.Lock()
	s.n.lost = append(s.n.lost, LostNotice{Epoch: req.GetEpoch(), MasterID: req.GetMasterId(), Announcer: caller, At: time.Now()})
	s.n.mu.Unlock()
	return &pb.Result{}, nil
}

// Probe says what this node believes about the master and whether it can see one.
func (s peerServer) Probe(context.Context, *pb.ProbeRequest) (*pb.ProbeReply, error) {
	epoch, master := s.n.mem.View()
	live := master == s.n.cfg.Self.NodeID || s.n.peerUp(master)
	return &pb.ProbeReply{Epoch: epoch, MasterId: master, HasLiveMaster: live}, nil
}

// NewMaster is a node announcing that it is master at an epoch. Only the node
// itself may say so: a belief that a third node is master comes through a
// heartbeat, not through this call.
func (s peerServer) NewMaster(ctx context.Context, req *pb.NewMasterNotice) (*pb.Result, error) {
	caller, _ := CallerFrom(ctx)
	if req.GetMasterId() != caller {
		return nil, status.Errorf(codes.PermissionDenied, "node %q cannot announce node %q as master", caller, req.GetMasterId())
	}
	s.n.observe(caller, req.GetEpoch(), req.GetMasterId())
	return &pb.Result{}, nil
}

// --- the liveness loop, on both ends ---

type beatStream interface {
	Send(*pb.Beat) error
	Recv() (*pb.Beat, error)
}

// runLiveness sends a beat every interval and takes in the peer's until the stream
// or the context ends. While it runs the peer counts as connected.
func (n *Node) runLiveness(ctx context.Context, peerID string, bs beatStream) error {
	n.mu.Lock()
	ps := n.peerLocked(peerID)
	ps.connected++
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		ps.connected--
		n.mu.Unlock()
	}()

	recvErr := make(chan error, 1)
	go func() {
		for {
			b, err := bs.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			n.mu.Lock()
			ps.lastBeat, ps.role, ps.epoch, ps.master = time.Now(), b.GetRole(), b.GetEpoch(), b.GetMasterId()
			n.mu.Unlock()
			n.observe(peerID, b.GetEpoch(), b.GetMasterId())
		}
	}()

	var seq uint64
	tick := time.NewTicker(n.beat)
	defer tick.Stop()
	send := func() error {
		seq++
		epoch, master := n.mem.View()
		return bs.Send(&pb.Beat{Seq: seq, Role: n.selfRole(), Epoch: epoch, MasterId: master})
	}
	if err := send(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-recvErr:
			if errors.Is(err, io.EOF) || status.Code(err) == codes.Canceled {
				return nil
			}
			return err
		case <-tick.C:
			if err := send(); err != nil {
				return err
			}
		}
	}
}

func (n *Node) peerLocked(id string) *peerStatus {
	ps := n.status[id]
	if ps == nil {
		ps = &peerStatus{}
		n.status[id] = ps
	}
	return ps
}

func (n *Node) peerUp(id string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	s, ok := n.status[id]
	return ok && s.connected > 0 && time.Since(s.lastBeat) < 3*n.beat
}

// keepLivenessOpen keeps the liveness stream to a higher-id node open, dialing
// again with backoff whenever it drops, until ctx is done.
func (n *Node) keepLivenessOpen(ctx context.Context, id string) {
	delay := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		if err := n.liveOnce(ctx, id); err != nil && ctx.Err() == nil {
			n.log.Warn("liveness stream to a peer ended", "peer", id, "error", err)
		}
		if time.Since(start) > 30*time.Second {
			delay = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < 15*time.Second {
			delay = delay * 8 / 5
		}
	}
}

func (n *Node) liveOnce(ctx context.Context, id string) error {
	conn, err := n.Dial(id)
	if err != nil {
		return err
	}
	client := pb.NewPeerClient(conn)

	epoch, master := n.mem.View()
	hctx, cancel := context.WithTimeout(ctx, DefaultCallTimeout)
	reply, err := client.Hello(hctx, &pb.HelloRequest{Role: n.selfRole(), Epoch: epoch, MasterId: master, Minors: Minors})
	cancel()
	if err != nil {
		return err
	}
	n.observe(id, reply.GetEpoch(), reply.GetMasterId())

	sctx, stop := context.WithCancel(ctx)
	defer stop()
	stream, err := client.Liveness(sctx)
	if err != nil {
		return err
	}
	return n.runLiveness(sctx, id, clientBeats{stream})
}

type clientBeats struct {
	grpc.BidiStreamingClient[pb.Beat, pb.Beat]
}

// Sink is where the master puts what secondaries report. The directory implements it.
type Sink interface {
	Replace(node string, agents []*pb.AgentInfo)
	Upsert(node string, a *pb.AgentInfo)
	Remove(node, name string)
	Drop(node string)
}

// SetSink sets where reports go. It must be called before Serve. Without one, the
// master acknowledges reports and keeps nothing.
func (n *Node) SetSink(s Sink) { n.sink = s }

// apply puts one report from a node into the sink.
func (n *Node) apply(caller string, r *pb.Report) {
	if n.sink == nil {
		return
	}
	switch b := r.GetBody().(type) {
	case *pb.Report_Agents:
		if b.Agents.GetFull() {
			n.sink.Replace(caller, b.Agents.GetAgents())
			return
		}
		for _, a := range b.Agents.GetAgents() {
			n.sink.Upsert(caller, a)
		}
	case *pb.Report_Event:
		switch c := b.Event.GetChange().(type) {
		case *pb.StateEvent_Changed:
			n.sink.Upsert(caller, c.Changed)
		case *pb.StateEvent_Removed:
			n.sink.Remove(caller, c.Removed)
		}
	}
}
