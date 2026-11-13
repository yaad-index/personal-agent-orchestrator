package mesh

import (
	"context"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// ServerCredentials are the gRPC transport credentials for a node serving other
// nodes: the TLS of ServerConfig.
func ServerCredentials(id *Identity, peers *Peers) credentials.TransportCredentials {
	return credentials.NewTLS(ServerConfig(id, peers))
}

// ClientCredentials are the gRPC transport credentials for a node dialing the
// node named want: the TLS of ClientConfig.
func ClientCredentials(id *Identity, peers *Peers, want string) credentials.TransportCredentials {
	return credentials.NewTLS(ClientConfig(id, peers, want))
}

// CallerNodeID returns the id of the node making the gRPC call ctx belongs to,
// from the key its connection was verified with. A call that did not come over
// the mesh's TLS, or whose peer is not on the list, has no caller: an
// interceptor that finds none refuses the call.
func CallerNodeID(ctx context.Context, peers *Peers) (string, bool) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", false
	}
	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", false
	}
	return PeerNodeID(info.State, peers)
}
