package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/control"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/harvest"
	pb "github.com/yaad-index/personal-agent-orchestrator/internal/mesh/meshpb"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
	"github.com/yaad-index/personal-agent-orchestrator/internal/temp"
	"github.com/yaad-index/personal-agent-orchestrator/internal/workdir"
)

// maxHarvestFileRPC bounds one harvested file sent over the mesh in a single call.
const maxHarvestFileRPC = 3 << 20

// follower stops following an agent's transcript.
type follower interface{ Stop(name string) }

// meshNodeServer answers the master's commands on this node (ADR-0017). The
// interceptors have already refused anyone but the master, and the master has
// already authorized the request for whoever it acts for, so none of these decide
// that again; they do the work the same way the node's own surfaces do.
type meshNodeServer struct {
	pb.UnimplementedNodeServer
	layer   *comm.Layer
	router  *route.Router
	plane   *control.Plane
	reg     *registry.Registry
	follow  follower
	mgr     *temp.Manager
	harvest *harvest.Store
	streams *streamControl
}

func (s meshNodeServer) agent(name string) (core.Profile, registry.AgentView, error) {
	for _, a := range s.reg.All() {
		if a.Profile.Name == name {
			return a.Profile, a, nil
		}
	}
	return core.Profile{}, registry.AgentView{}, status.Errorf(codes.NotFound, "no agent %q on this node", name)
}

func (s meshNodeServer) Launch(ctx context.Context, req *pb.AgentRef) (*pb.Result, error) {
	p, _, err := s.agent(req.GetAgent())
	if err != nil {
		return nil, err
	}
	if _, err := s.router.Ensure(ctx, p); err != nil {
		return nil, grpcErr(err)
	}
	return &pb.Result{}, nil
}

func (s meshNodeServer) Kill(ctx context.Context, req *pb.AgentRef) (*pb.Result, error) {
	p, v, err := s.agent(req.GetAgent())
	if err != nil {
		return nil, err
	}
	if v.Temp != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%q is a temporary agent: kill it with KillTemp, which harvests its outputs first", p.Name)
	}
	if err := s.stop(ctx, p); err != nil {
		return nil, err
	}
	return &pb.Result{}, nil
}

func (s meshNodeServer) Restart(ctx context.Context, req *pb.AgentRef) (*pb.Result, error) {
	p, v, err := s.agent(req.GetAgent())
	if err != nil {
		return nil, err
	}
	if v.Temp != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%q is a temporary agent: killing it destroys its workdir, so it is not restarted", p.Name)
	}
	if err := s.stop(ctx, p); err != nil {
		return nil, err
	}
	if _, err := s.router.Ensure(ctx, p); err != nil {
		return nil, grpcErr(err)
	}
	return &pb.Result{}, nil
}

// stop ends an agent's session and forgets that it is running.
func (s meshNodeServer) stop(ctx context.Context, p core.Profile) error {
	if err := s.plane.Kill(ctx, p); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	if err := s.reg.SetState(p.Name, core.Stopped); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	s.follow.Stop(p.Name)
	return nil
}

func (s meshNodeServer) Spawn(ctx context.Context, req *pb.SpawnRequest) (*pb.SpawnResult, error) {
	who, err := requesterSubject(req.GetRequester())
	if err != nil {
		return nil, err
	}
	p, err := s.mgr.Spawn(ctx, temp.SpawnRequest{
		Recipe: req.GetRecipe(), Name: req.GetName(), Channel: req.GetChannel(), Requester: who, Authorized: true,
	})
	if _, notReady := control.AsReadinessError(err); notReady {
		return &pb.SpawnResult{Name: p.Name, NotReady: err.Error()}, nil
	}
	if err != nil {
		return nil, grpcErr(err)
	}
	return &pb.SpawnResult{Name: p.Name}, nil
}

func (s meshNodeServer) KillTemp(ctx context.Context, req *pb.AgentRef) (*pb.KillResult, error) {
	who, err := requesterSubject(req.GetRequester())
	if err != nil {
		return nil, err
	}
	res, err := s.mgr.Kill(ctx, temp.KillRequest{Name: req.GetAgent(), Requester: who, Authorized: true, NoReport: true})
	if err != nil {
		return nil, grpcErr(err)
	}
	m := res.Harvest
	summary := m.Summary()
	if res.WorkdirKept {
		summary += "\nthe workdir was kept: the harvest was incomplete"
	}
	return &pb.KillResult{
		HarvestId: m.ID, Complete: m.Complete, WorkdirKept: res.WorkdirKept,
		Files: int64(len(m.Files)), Bytes: m.Bytes, Summary: summary,
	}, nil
}

func (s meshNodeServer) ListHarvests(context.Context, *pb.ListHarvestsRequest) (*pb.ListHarvestsReply, error) {
	ms, err := s.harvest.List(access.Operator)
	if err != nil {
		return nil, grpcErr(err)
	}
	out := &pb.ListHarvestsReply{}
	for _, m := range ms {
		out.Harvests = append(out.Harvests, manifestPB(m, false))
	}
	return out, nil
}

func (s meshNodeServer) GetHarvest(_ context.Context, req *pb.HarvestRef) (*pb.HarvestManifest, error) {
	m, err := s.harvest.Get(access.Operator, req.GetId())
	if err != nil {
		return nil, grpcErr(err)
	}
	return manifestPB(m, true), nil
}

func (s meshNodeServer) ReadHarvestFile(_ context.Context, req *pb.HarvestFileRef) (*pb.HarvestFile, error) {
	b, err := s.harvest.Read(access.Operator, req.GetId(), req.GetPath())
	if err != nil {
		return nil, grpcErr(err)
	}
	if len(b) > maxHarvestFileRPC {
		return nil, status.Errorf(codes.FailedPrecondition, "%s is %d bytes, more than the %d one call between nodes carries: read it on its node", req.GetPath(), len(b), maxHarvestFileRPC)
	}
	return &pb.HarvestFile{Content: b}, nil
}

func (s meshNodeServer) DeleteHarvest(_ context.Context, req *pb.HarvestRef) (*pb.Result, error) {
	if err := s.harvest.Delete(access.Operator, req.GetId()); err != nil {
		return nil, grpcErr(err)
	}
	return &pb.Result{}, nil
}

func manifestPB(m harvest.Manifest, withFiles bool) *pb.HarvestManifest {
	out := &pb.HarvestManifest{
		Id: m.ID, Agent: m.Agent, Tags: m.Tags, Requester: m.Requester, Time: m.Time.Format(time.RFC3339),
		Complete: m.Complete, Bytes: m.Bytes, SkippedTotal: int64(m.SkippedTotal),
	}
	if withFiles {
		for _, f := range m.Files {
			out.Files = append(out.Files, &pb.HarvestFileInfo{Path: f.Path, Size: f.Size})
		}
	}
	return out
}

// requesterSubject reads who the master acted for. An empty name is the operator.
func requesterSubject(s string) (access.Subject, error) {
	switch {
	case s == "" || s == "operator":
		return access.Operator, nil
	case strings.HasPrefix(s, "agent:") && len(s) > len("agent:"):
		return access.Agent(strings.TrimPrefix(s, "agent:")), nil
	}
	return access.Subject{}, status.Errorf(codes.InvalidArgument, "requester %q is neither \"operator\" nor \"agent:<name>\"", s)
}

// grpcErr gives a domain error the status a caller across the mesh can act on.
func grpcErr(err error) error {
	if _, ok := status.FromError(err); ok && status.Code(err) != codes.Unknown {
		return err
	}
	switch {
	case errors.Is(err, temp.ErrNotFound), errors.Is(err, temp.ErrNoRecipe), errors.Is(err, harvest.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, temp.ErrBadRequest), errors.Is(err, workdir.ErrBadSource):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, registry.ErrDuplicate), errors.Is(err, workdir.ErrExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, temp.ErrDenied):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, errViewer):
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	if _, notReady := control.AsReadinessError(err); notReady {
		return status.Error(codes.FailedPrecondition, fmt.Sprintf("%v", err))
	}
	return status.Error(codes.Internal, err.Error())
}

func (s meshNodeServer) SetStream(_ context.Context, req *pb.StreamRequest) (*pb.Result, error) {
	if err := s.streams.SetStream(req.GetAgent(), req.GetChannel(), int(req.GetLevel()), req.GetViewer()); err != nil {
		return nil, grpcErr(err)
	}
	return &pb.Result{}, nil
}

func (s meshNodeServer) View(ctx context.Context, req *pb.ViewRequest) (*pb.Result, error) {
	var err error
	switch req.GetAction() {
	case pb.ViewAction_VIEW_ACTION_PAUSE:
		err = s.streams.Pause(req.GetAgent(), req.GetViewer())
	case pb.ViewAction_VIEW_ACTION_RESUME:
		err = s.streams.Resume(ctx, req.GetAgent(), req.GetViewer())
	case pb.ViewAction_VIEW_ACTION_BACKTRACK:
		err = s.streams.Backtrack(ctx, req.GetAgent(), req.GetViewer(), int(req.GetLevel()), int(req.GetEntries()))
	default:
		return nil, status.Error(codes.InvalidArgument, "a view control is pause, resume or backtrack")
	}
	if err != nil {
		return nil, grpcErr(err)
	}
	return &pb.Result{}, nil
}
