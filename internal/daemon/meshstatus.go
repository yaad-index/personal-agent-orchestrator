package daemon

import (
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh/transport"
)

// meshStatus is the node's view of the mesh in the shape the local channel serves.
func meshStatus(n *transport.Node) func() local.MeshJSON {
	return func() local.MeshJSON {
		st := n.Status()
		out := local.MeshJSON{Node: st.Node, Role: st.Role, Epoch: st.Epoch, Master: st.Master, Peers: []local.MeshPeerJSON{}}
		for _, p := range st.Peers {
			out.Peers = append(out.Peers, local.MeshPeerJSON{
				ID: p.ID, Addr: p.Addr, Up: p.Up, LastBeat: p.LastBeat, Role: p.Role, Epoch: p.Epoch, Master: p.Master,
			})
		}
		return out
	}
}
