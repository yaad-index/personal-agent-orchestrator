package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/harvest"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh/directory"
	pb "github.com/yaad-index/personal-agent-orchestrator/internal/mesh/meshpb"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh/transport"
)

// The deadlines of the commands the master sends. A spawn waits for the agent to
// become ready on the other node, so it gets longer than a call that only changes
// state there.
const (
	spawnCallTimeout = 2 * time.Minute
	killCallTimeout  = time.Minute
	readCallTimeout  = 30 * time.Second
)

// meshCommands is the master's side of the command plane: it sends the operator's
// requests to the node that holds the agent or the harvest. The operator's token has
// already authorized each one at the local channel, so the node is told to act
// without deciding it again, and is told who asked for the record.
type meshCommands struct {
	self string
	node *transport.Node
	dir  *directory.Directory
	// holds says whether this node, the master, has an agent of the name.
	holds func(name string) bool
}

func newRequestID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// requireMaster refuses a command from a node that is not the master: the commands
// are the master's alone, and a secondary's dial would be refused by the node.
func (c meshCommands) requireMaster() error {
	if !c.node.Membership().IsMaster() {
		return fmt.Errorf("%w: this node is not the master, so it does not command other nodes", local.ErrForbidden)
	}
	return nil
}

// fromStatus turns what a node answered into the error the local channel maps to a
// status code, keeping the node's own words.
func fromStatus(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	var kind error
	switch st.Code() {
	case codes.NotFound:
		kind = local.ErrNotFound
	case codes.InvalidArgument:
		kind = local.ErrBadRequest
	case codes.AlreadyExists:
		kind = local.ErrConflict
	case codes.PermissionDenied:
		kind = local.ErrForbidden
	default:
		return errors.New(st.Message())
	}
	return translated{kind, errors.New(st.Message())}
}

// Spawn asks a node for a temporary agent. When the agent was created but is not
// ready, the name comes back with the error, as for a local spawn.
func (c meshCommands) Spawn(ctx context.Context, nodeID, recipe, name, channel string) (string, error) {
	if err := c.requireMaster(); err != nil {
		return "", err
	}
	if name != "" && (c.dir.Has(name) || c.holds(name)) {
		return "", translated{local.ErrConflict, fmt.Errorf("the name %q is already held by an agent in the mesh", name)}
	}
	conn, err := c.node.Dial(nodeID)
	if err != nil {
		return "", translated{local.ErrBadRequest, err}
	}
	id, err := newRequestID()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, spawnCallTimeout)
	defer cancel()
	res, err := pb.NewNodeClient(conn).Spawn(ctx, &pb.SpawnRequest{
		RequestId: id, Recipe: recipe, Name: name, Channel: channel, Requester: "operator",
	})
	if err != nil {
		return "", fromStatus(err)
	}
	if res.GetNotReady() != "" {
		return res.GetName(), translated{local.ErrNotReady, errors.New(res.GetNotReady())}
	}
	return res.GetName(), nil
}

// Kill tears down a temporary agent on the node that holds it, found by name.
func (c meshCommands) Kill(ctx context.Context, e directory.Entry) (local.KillJSON, error) {
	if err := c.requireMaster(); err != nil {
		return local.KillJSON{}, err
	}
	if !e.Agent.GetTemporary() {
		return local.KillJSON{}, translated{local.ErrBadRequest, fmt.Errorf("%q is not a temporary agent", e.Agent.GetName())}
	}
	conn, err := c.node.Dial(e.Node)
	if err != nil {
		return local.KillJSON{}, err
	}
	id, err := newRequestID()
	if err != nil {
		return local.KillJSON{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, killCallTimeout)
	defer cancel()
	res, err := pb.NewNodeClient(conn).KillTemp(ctx, &pb.AgentRef{RequestId: id, Agent: e.Agent.GetName(), Requester: "operator"})
	if err != nil {
		return local.KillJSON{}, fromStatus(err)
	}
	return local.KillJSON{
		Harvest: harvest.Manifest{
			ID: qualify(e.Node, res.GetHarvestId()), Agent: e.Agent.GetName(), Tags: e.Agent.GetTags(),
			Requester: "operator", Complete: res.GetComplete(), Bytes: res.GetBytes(),
		},
		WorkdirKept: res.GetWorkdirKept(),
	}, nil
}

// qualify names a harvest by the node that holds it. A node id has no colon.
func qualify(node, id string) string { return node + ":" + id }

// split reads a harvest id: a node-qualified one names the node it is on, an
// unqualified one is this node's. A prefix that is not a node of the mesh is part
// of a local id.
func (c meshCommands) split(id string) (node, rest string) {
	if n, r, ok := strings.Cut(id, ":"); ok {
		if c.node.Knows(n) {
			return n, r
		}
	}
	return "", id
}

func (c meshCommands) client(nodeID string) (pb.NodeClient, error) {
	if err := c.requireMaster(); err != nil {
		return nil, err
	}
	conn, err := c.node.Dial(nodeID)
	if err != nil {
		return nil, translated{local.ErrBadRequest, err}
	}
	return pb.NewNodeClient(conn), nil
}

func (c meshCommands) Harvests(ctx context.Context, nodeID string) ([]harvest.Manifest, error) {
	cl, err := c.client(nodeID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, readCallTimeout)
	defer cancel()
	res, err := cl.ListHarvests(ctx, &pb.ListHarvestsRequest{})
	if err != nil {
		return nil, fromStatus(err)
	}
	var out []harvest.Manifest
	for _, m := range res.GetHarvests() {
		out = append(out, manifestFrom(nodeID, m))
	}
	return out, nil
}

func (c meshCommands) Harvest(ctx context.Context, nodeID, id string) (harvest.Manifest, error) {
	cl, err := c.client(nodeID)
	if err != nil {
		return harvest.Manifest{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, readCallTimeout)
	defer cancel()
	m, err := cl.GetHarvest(ctx, &pb.HarvestRef{Id: id})
	if err != nil {
		return harvest.Manifest{}, fromStatus(err)
	}
	return manifestFrom(nodeID, m), nil
}

func (c meshCommands) HarvestFile(ctx context.Context, nodeID, id, path string) ([]byte, error) {
	cl, err := c.client(nodeID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, readCallTimeout)
	defer cancel()
	f, err := cl.ReadHarvestFile(ctx, &pb.HarvestFileRef{Id: id, Path: path})
	if err != nil {
		return nil, fromStatus(err)
	}
	return f.GetContent(), nil
}

func (c meshCommands) DeleteHarvest(ctx context.Context, nodeID, id string) error {
	cl, err := c.client(nodeID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, readCallTimeout)
	defer cancel()
	_, err = cl.DeleteHarvest(ctx, &pb.HarvestRef{Id: id})
	return fromStatus(err)
}

func manifestFrom(node string, m *pb.HarvestManifest) harvest.Manifest {
	out := harvest.Manifest{
		ID: qualify(node, m.GetId()), Agent: m.GetAgent(), Tags: m.GetTags(), Requester: m.GetRequester(),
		Complete: m.GetComplete(), Bytes: m.GetBytes(), SkippedTotal: int(m.GetSkippedTotal()),
	}
	if t, err := time.Parse(time.RFC3339, m.GetTime()); err == nil {
		out.Time = t
	}
	for _, f := range m.GetFiles() {
		out.Files = append(out.Files, harvest.File{Path: f.GetPath(), Size: f.GetSize()})
	}
	return out
}
