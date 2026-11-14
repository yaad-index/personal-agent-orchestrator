package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh/directory"
	pb "github.com/yaad-index/personal-agent-orchestrator/internal/mesh/meshpb"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh/transport"
	"github.com/yaad-index/personal-agent-orchestrator/internal/msgstore"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
)

// meshRemote is the router's view of agents on other nodes: the master's directory,
// and the node call that hands one a message.
type meshRemote struct {
	dir  *directory.Directory
	node *transport.Node
}

func profileOf(e directory.Entry) core.Profile {
	return core.Profile{Name: e.Agent.GetName(), Tags: e.Agent.GetTags(), Node: e.Node}
}

func (m meshRemote) Lookup(name string) (core.Profile, bool) {
	e, ok := m.dir.Lookup(name)
	if !ok {
		return core.Profile{}, false
	}
	return profileOf(e), true
}

func (m meshRemote) ByTag(tag string) []core.Profile {
	var out []core.Profile
	for _, e := range m.dir.ByTag(tag) {
		out = append(out, profileOf(e))
	}
	return out
}

// Deliver hands the message to the agent on its node, where it is stored and
// delivered exactly as a message to a local agent is (ADR-0017 point 4).
func (m meshRemote) Deliver(ctx context.Context, p core.Profile, msg core.Message) (bool, error) {
	conn, err := m.node.Dial(p.Node)
	if err != nil {
		return false, err
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return false, err
	}
	reply, err := pb.NewNodeClient(conn).Deliver(ctx, &pb.DeliverRequest{
		RequestId: hex.EncodeToString(id[:]), Agent: p.Name, Sender: msg.Sender, Text: msg.Text,
		ReplyRef: msg.Reply, ReplyQuote: msg.ReplyQuote,
	})
	if err != nil {
		return false, errors.New(status.Convert(err).Message())
	}
	return reply.GetLaunched(), nil
}

func (s meshNodeServer) Deliver(ctx context.Context, req *pb.DeliverRequest) (*pb.DeliverResult, error) {
	d, err := s.layer.Receive(ctx, comm.Inbound{
		Source: "mesh", Sender: req.GetSender(), Text: req.GetText(), Kind: core.MessageContent,
		Reply: req.GetReplyRef(), ReplyQuote: req.GetReplyQuote(),
		Target: core.Target{Kind: core.ByName, Value: req.GetAgent()},
	})
	switch {
	case errors.Is(err, core.ErrNoMatch):
		return nil, status.Errorf(codes.NotFound, "no agent %q on this node", req.GetAgent())
	case errors.Is(err, msgstore.ErrNotFound), errors.Is(err, msgstore.ErrUnresolved), errors.Is(err, msgstore.ErrAmbiguous):
		// A reply the agent was not given is the same answer as one that does not
		// exist, and one that matches several is reported, not picked (ADR-0010
		// point 3, ADR-0017 point 10); the message says which.
		return nil, status.Error(codes.NotFound, err.Error())
	case err != nil:
		return nil, status.Error(codes.Internal, err.Error())
	case len(d.Results) != 1:
		return nil, status.Errorf(codes.Internal, "delivered to %d agents, want 1", len(d.Results))
	case d.Results[0].Err != nil:
		return nil, status.Error(codes.Unavailable, d.Results[0].Err.Error())
	}
	return &pb.DeliverResult{Launched: d.Results[0].Launched}, nil
}

// registrySource is what this node reports to the master: its own agents, with the
// stream bindings they have.
type registrySource struct {
	reg        *registry.Registry
	thresholds registry.Thresholds
	layer      *comm.Layer
	views      *views
}

func (s registrySource) Snapshot() []*pb.AgentInfo {
	now := time.Now()
	var out []*pb.AgentInfo
	for _, a := range s.reg.All() {
		info := &pb.AgentInfo{Name: a.Profile.Name, Tags: a.Profile.Tags, State: pb.AgentState_AGENT_STATE_STOPPED}
		if a.State == core.Running {
			info.State = pb.AgentState_AGENT_STATE_RUNNING
		}
		if a.Temp != nil {
			info.Temporary = true
			info.Recipe = a.Temp.Info.Recipe
			info.Channel = a.Temp.Info.Channel
			info.Flag = string(a.Temp.Flag(now, s.thresholds))
		}
		if ch, ok := s.layer.Bound(a.Profile.Name); ok {
			level, viewer := s.views.info(a.Profile.Name)
			info.Bindings = []*pb.StreamBinding{{Channel: ch, Level: int32(level), Viewer: viewer}}
		}
		out = append(out, info)
	}
	return out
}

// meshStatusWith is the node's view of the mesh in the shape the local channel
// serves, with what the master's directory holds for each peer.
func meshStatusWith(n *transport.Node, dir *directory.Directory) func() local.MeshJSON {
	return func() local.MeshJSON {
		st := n.Status()
		out := local.MeshJSON{
			Node: st.Node, Role: st.Role, Epoch: st.Epoch, Master: st.Master,
			Peers: []local.MeshPeerJSON{}, Conflicts: []local.MeshConflictJSON{},
		}
		for _, p := range st.Peers {
			out.Peers = append(out.Peers, local.MeshPeerJSON{
				ID: p.ID, Addr: p.Addr, Up: p.Up, LastBeat: p.LastBeat, Role: p.Role, Epoch: p.Epoch, Master: p.Master,
				Agents: dir.Count(p.ID),
			})
		}
		for _, c := range dir.Conflicts() {
			out.Conflicts = append(out.Conflicts, local.MeshConflictJSON{Name: c.Name, Node: c.Node, HeldBy: c.HeldBy})
		}
		return out
	}
}
