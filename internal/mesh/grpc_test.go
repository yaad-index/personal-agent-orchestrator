package mesh

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	pb "github.com/yaad-index/personal-agent-orchestrator/internal/mesh/meshpb"
)

// peerServer answers Hello by saying who it saw the caller to be.
type peerServer struct {
	pb.UnimplementedPeerServer
	peers *Peers
}

func (s peerServer) Hello(ctx context.Context, _ *pb.HelloRequest) (*pb.HelloReply, error) {
	who, ok := CallerNodeID(ctx, s.peers)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "no caller")
	}
	return &pb.HelloReply{MasterId: who}, nil
}

func serveGRPC(t *testing.T, id *Identity, peers *Peers) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer(grpc.Creds(ServerCredentials(id, peers)))
	pb.RegisterPeerServer(srv, peerServer{peers: peers})
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	return ln.Addr().String()
}

func dialGRPC(t *testing.T, addr string, id *Identity, peers *Peers, want string) pb.PeerClient {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(ClientCredentials(id, peers, want)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewPeerClient(conn)
}

func hello(c pb.PeerClient) (*pb.HelloReply, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return c.Hello(ctx, &pb.HelloRequest{})
}

func TestACallOverTheMeshKnowsItsCallerFromTheVerifiedKey(t *testing.T) {
	ids, peers := newMesh(t, "a", "b")
	addr := serveGRPC(t, ids["b"], peers)

	reply, err := hello(dialGRPC(t, addr, ids["a"], peers, "b"))
	require.NoError(t, err)
	assert.Equal(t, "a", reply.MasterId, "the server saw the caller as node a, by its key")
}

func TestACallFromAnUnlistedNodeNeverReachesTheService(t *testing.T) {
	ids, peers := newMesh(t, "a", "b")
	strangers, _ := newMesh(t, "stranger")
	addr := serveGRPC(t, ids["b"], peers)

	_, err := hello(dialGRPC(t, addr, strangers["stranger"], peers, "b"))
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
}

func TestACallToTheWrongNodeIsRefusedByTheCaller(t *testing.T) {
	ids, peers := newMesh(t, "a", "b", "c")
	addr := serveGRPC(t, ids["c"], peers)

	_, err := hello(dialGRPC(t, addr, ids["a"], peers, "b"))
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
}

func TestACallWithoutTheMeshsTLSHasNoCaller(t *testing.T) {
	_, peers := newMesh(t, "a")
	_, ok := CallerNodeID(context.Background(), peers)
	assert.False(t, ok)

	ids, peers2 := newMesh(t, "a", "b")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer() // no transport security
	pb.RegisterPeerServer(srv, peerServer{peers: peers2})
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = hello(pb.NewPeerClient(conn))
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err), "a plaintext caller is nobody, even to a service that would have answered")
	_ = ids
}
