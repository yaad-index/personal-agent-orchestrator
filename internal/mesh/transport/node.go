package transport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh"
	pb "github.com/yaad-index/personal-agent-orchestrator/internal/mesh/meshpb"
)

// Minors are the protocol minor versions within pao.mesh.v1 this node speaks.
var Minors = []uint32{0}

const (
	// epochHeader carries the master epoch the caller believes in, on every call.
	epochHeader = "pao-epoch"
	// DefaultBeatEvery is how often a node sends a heartbeat to each peer.
	DefaultBeatEvery = 2 * time.Second
	// DefaultCallTimeout bounds a call whose caller set no deadline of its own.
	DefaultCallTimeout = 10 * time.Second
)

const (
	nodeService   = "/pao.mesh.v1.Node/"
	masterService = "/pao.mesh.v1.Master/"
	peerService   = "/pao.mesh.v1.Peer/"
)

// Config says how a node takes part in the mesh.
type Config struct {
	// Self is this node's identity. Its key must be the one the document lists for
	// it: a node that is not what the others trust would be refused by every peer,
	// and says so here instead.
	Self *mesh.Identity
	// Doc is the mesh document.
	Doc *mesh.Document
	// StateDir holds the node's persisted epoch.
	StateDir string
	// Listen is the address the node accepts other nodes on. Ignored if Listener is set.
	Listen string
	// Listener, if set, is used in place of listening on Listen.
	Listener net.Listener
	// BeatEvery is the heartbeat interval; DefaultBeatEvery if zero.
	BeatEvery time.Duration
	// Logger receives the node's log lines; the default logger if nil.
	Logger *slog.Logger
}

// Node is one pao node's side of the mesh transport.
type Node struct {
	cfg   Config
	peers *mesh.Peers
	mem   *Membership
	log   *slog.Logger
	beat  time.Duration

	mu       sync.Mutex
	status   map[string]*peerStatus
	conns    map[string]*grpc.ClientConn
	nodeSrv  pb.NodeServer
	sink     Sink
	lost     []LostNotice
	sessions map[string]*session
	gens     map[string]uint64 // the newest report stream from each node
	cache    *idempotency
	addr     net.Addr
}

type peerStatus struct {
	lastBeat time.Time
	role     pb.Role
	epoch    uint64
	master   string
	// connected counts the liveness streams open with the peer.
	connected int
}

type session struct {
	reports int
	lastSeq uint64
	last    time.Time
}

// LostNotice is a master-lost announcement a node received.
type LostNotice struct {
	Epoch     uint64
	MasterID  string
	Announcer string
	At        time.Time
}

// New returns a node. It refuses a key that is not the one the document lists for
// this node.
func New(cfg Config) (*Node, error) {
	entry, ok := cfg.Doc.Entry(cfg.Self.NodeID)
	if !ok {
		return nil, fmt.Errorf("node %q is not in the mesh document", cfg.Self.NodeID)
	}
	if entry.Fingerprint != cfg.Self.Fingerprint {
		return nil, fmt.Errorf("this node's key (%s) is not the one the mesh document lists for %q (%s): every other node would refuse it", cfg.Self.Fingerprint, cfg.Self.NodeID, entry.Fingerprint)
	}
	mem, err := LoadMembership(statePath(cfg.StateDir), cfg.Self.NodeID, cfg.Doc.Master)
	if err != nil {
		return nil, err
	}
	n := &Node{
		cfg: cfg, peers: cfg.Doc.Peers(), mem: mem, log: cfg.Logger, beat: cfg.BeatEvery,
		status: make(map[string]*peerStatus), conns: make(map[string]*grpc.ClientConn),
		nodeSrv: pb.UnimplementedNodeServer{}, sessions: make(map[string]*session), gens: make(map[string]uint64),
		cache: newIdempotency(1024, 10*time.Minute),
	}
	if n.log == nil {
		n.log = slog.Default()
	}
	if n.beat <= 0 {
		n.beat = DefaultBeatEvery
	}
	return n, nil
}

func statePath(dir string) string {
	if dir == "" {
		return ""
	}
	return dir + "/mesh.state"
}

// SetNodeServer sets what answers the master's commands on this node. It must be
// called before Serve. Until it is, every command is unimplemented, but is still
// refused to anyone but the master.
func (n *Node) SetNodeServer(s pb.NodeServer) { n.nodeSrv = s }

// Membership returns the node's belief about who is master.
func (n *Node) Membership() *Membership { return n.mem }

// Addr is the address the node is serving on, once Serve has started.
func (n *Node) Addr() net.Addr {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.addr
}

// Serve accepts other nodes and keeps a liveness stream open to every node with a
// higher id, until ctx is done.
func (n *Node) Serve(ctx context.Context) error {
	ln := n.cfg.Listener
	if ln == nil {
		var err error
		if ln, err = net.Listen("tcp", n.cfg.Listen); err != nil {
			return err
		}
	}
	n.mu.Lock()
	n.addr = ln.Addr()
	n.mu.Unlock()

	srv := grpc.NewServer(
		grpc.Creds(mesh.ServerCredentials(n.cfg.Self, n.peers)),
		grpc.ChainUnaryInterceptor(n.unaryInterceptor),
		grpc.ChainStreamInterceptor(n.streamInterceptor),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 15 * time.Second, Timeout: 5 * time.Second}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 5 * time.Second, PermitWithoutStream: true}),
		grpc.MaxRecvMsgSize(4<<20),
	)
	pb.RegisterNodeServer(srv, n.nodeServer())
	pb.RegisterMasterServer(srv, masterServer{n: n})
	pb.RegisterPeerServer(srv, peerServer{n: n})

	var wg sync.WaitGroup
	for _, entry := range n.cfg.Doc.Nodes {
		if entry.ID > n.cfg.Self.NodeID {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				n.keepLivenessOpen(ctx, id)
			}(entry.ID)
		}
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	var err error
	select {
	case err = <-serveErr:
	case <-ctx.Done():
	}
	srv.Stop()
	wg.Wait()
	n.closeConns()
	if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return err
	}
	return nil
}

func (n *Node) nodeServer() pb.NodeServer { return n.nodeSrv }

// --- who may call what ---

type callerKey struct{}

// CallerFrom returns the node a call came from, as the interceptors established it.
func CallerFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(callerKey{}).(string)
	return id, ok
}

// authorize decides whether the caller may make this call and returns who it is.
// Identity is the node whose verified key the connection was made with. A command
// to a node (service Node) is accepted only from the node this node believes is
// master, at the epoch it believes in; the master's own service (Master) only
// answers when this node is master; the peer calls are open to any listed node.
func (n *Node) authorize(ctx context.Context, method string) (string, error) {
	caller, ok := mesh.CallerNodeID(ctx, n.peers)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "the caller is not a node of this mesh")
	}
	epoch, master := n.mem.View()
	switch {
	case hasPrefix(method, nodeService):
		if master == n.cfg.Self.NodeID {
			return "", status.Error(codes.FailedPrecondition, "this node is the master and is not commanded")
		}
		if caller != master {
			return "", status.Errorf(codes.PermissionDenied, "node %q is not the master", caller)
		}
		got, err := callerEpoch(ctx)
		if err != nil {
			return "", err
		}
		switch {
		case got < epoch:
			return "", status.Errorf(codes.FailedPrecondition, "stale epoch %d: this node is at %d", got, epoch)
		case got > epoch:
			return "", status.Errorf(codes.FailedPrecondition, "epoch %d is not one this node knows (it is at %d): announce the new master first", got, epoch)
		}
	case hasPrefix(method, masterService):
		if master != n.cfg.Self.NodeID {
			return "", status.Errorf(codes.FailedPrecondition, "this node is not the master (node %q is)", master)
		}
		if caller == n.cfg.Self.NodeID {
			return "", status.Error(codes.PermissionDenied, "a node does not report to itself")
		}
	case hasPrefix(method, peerService):
	default:
		return "", status.Errorf(codes.Unimplemented, "unknown service %s", method)
	}
	return caller, nil
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

func callerEpoch(ctx context.Context) (uint64, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get(epochHeader)
	if len(vals) != 1 {
		return 0, status.Errorf(codes.InvalidArgument, "a command carries exactly one %s", epochHeader)
	}
	e, err := strconv.ParseUint(vals[0], 10, 64)
	if err != nil {
		return 0, status.Errorf(codes.InvalidArgument, "%s %q is not an epoch", epochHeader, vals[0])
	}
	return e, nil
}

func (n *Node) unaryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	caller, err := n.authorize(ctx, info.FullMethod)
	if err != nil {
		return nil, err
	}
	// Every call carries a deadline, so a caller that set none is not served and a
	// hung handler cannot be waited on forever.
	if _, ok := ctx.Deadline(); !ok {
		return nil, status.Error(codes.InvalidArgument, "a call between nodes carries a deadline")
	}
	ctx = context.WithValue(ctx, callerKey{}, caller)
	if hasPrefix(info.FullMethod, nodeService) {
		return n.cache.do(ctx, caller, info.FullMethod, req, handler)
	}
	return handler(ctx, req)
}

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w wrappedStream) Context() context.Context { return w.ctx }

func (n *Node) streamInterceptor(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	caller, err := n.authorize(ss.Context(), info.FullMethod)
	if err != nil {
		return err
	}
	return handler(srv, wrappedStream{ss, context.WithValue(ss.Context(), callerKey{}, caller)})
}

// --- dialing ---

// Dial returns the connection to a peer, made if there is none yet. Every call on
// it carries this node's epoch, and a deadline if the caller set none.
func (n *Node) Dial(id string) (*grpc.ClientConn, error) {
	entry, ok := n.cfg.Doc.Entry(id)
	if !ok {
		return nil, fmt.Errorf("no node %q in the mesh", id)
	}
	if id == n.cfg.Self.NodeID {
		return nil, errors.New("a node does not dial itself")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if c, ok := n.conns[id]; ok {
		return c, nil
	}
	c, err := grpc.NewClient(entry.Addr,
		grpc.WithTransportCredentials(mesh.ClientCredentials(n.cfg.Self, n.peers, id)),
		grpc.WithChainUnaryInterceptor(n.clientInterceptor),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 10 * time.Second, Timeout: 5 * time.Second, PermitWithoutStream: true}),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoffConfig(),
			MinConnectTimeout: 3 * time.Second,
		}),
	)
	if err != nil {
		return nil, err
	}
	n.conns[id] = c
	return c, nil
}

func (n *Node) clientInterceptor(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultCallTimeout)
		defer cancel()
	}
	epoch, _ := n.mem.View()
	ctx = metadata.AppendToOutgoingContext(ctx, epochHeader, strconv.FormatUint(epoch, 10))
	return invoker(ctx, method, req, reply, cc, opts...)
}

func (n *Node) closeConns() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, c := range n.conns {
		_ = c.Close()
		delete(n.conns, id)
	}
}

// --- status ---

// PeerStatus is what a node knows about one peer.
type PeerStatus struct {
	ID       string
	Addr     string
	Up       bool
	LastBeat time.Time
	Role     string
	Epoch    uint64
	Master   string
}

// Status is a node's view of the mesh.
type Status struct {
	Node   string
	Role   string
	Epoch  uint64
	Master string
	Peers  []PeerStatus
}

// Status returns the node's role and what it knows of each peer. A peer is up if a
// heartbeat arrived within three beat intervals.
func (n *Node) Status() Status {
	epoch, master := n.mem.View()
	role := "secondary"
	if master == n.cfg.Self.NodeID {
		role = "master"
	}
	st := Status{Node: n.cfg.Self.NodeID, Role: role, Epoch: epoch, Master: master}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, e := range n.cfg.Doc.Nodes {
		if e.ID == n.cfg.Self.NodeID {
			continue
		}
		p := PeerStatus{ID: e.ID, Addr: e.Addr, Role: "unknown"}
		if s, ok := n.status[e.ID]; ok {
			p.LastBeat, p.Epoch, p.Master = s.lastBeat, s.epoch, s.master
			p.Up = s.connected > 0 && time.Since(s.lastBeat) < 3*n.beat
			p.Role = roleName(s.role)
		}
		st.Peers = append(st.Peers, p)
	}
	sort.Slice(st.Peers, func(i, j int) bool { return st.Peers[i].ID < st.Peers[j].ID })
	return st
}

// LostNotices returns the master-lost announcements this node has received.
func (n *Node) LostNotices() []LostNotice {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]LostNotice(nil), n.lost...)
}

func roleName(r pb.Role) string {
	switch r {
	case pb.Role_ROLE_MASTER:
		return "master"
	case pb.Role_ROLE_SECONDARY:
		return "secondary"
	default:
		return "unknown"
	}
}

func (n *Node) selfRole() pb.Role {
	if n.mem.IsMaster() {
		return pb.Role_ROLE_MASTER
	}
	return pb.Role_ROLE_SECONDARY
}

// observe takes in a belief about the master heard from a peer. A node that was
// master and hears of one that supersedes it has yielded.
func (n *Node) observe(from string, epoch uint64, master string) {
	if _, ok := n.cfg.Doc.Entry(master); !ok {
		return // a master that is not a node of the mesh is not a belief
	}
	was := n.mem.IsMaster()
	changed, err := n.mem.Observe(epoch, master)
	if err != nil {
		n.log.Error("could not record the master", "error", err)
		return
	}
	if changed {
		n.log.Info("master changed", "master", master, "epoch", epoch, "heard_from", from, "yielded", was && !n.mem.IsMaster())
	}
}
