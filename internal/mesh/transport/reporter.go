package transport

import (
	"context"
	"errors"
	"io"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/yaad-index/personal-agent-orchestrator/internal/mesh/meshpb"
)

// Source is what a node reports to the master: its own agents as they are now.
type Source interface {
	Snapshot() []*pb.AgentInfo
}

// RunReporter keeps a Session open to the master and keeps it told what this node
// hosts, until ctx is done: the node's whole list when the stream opens, then each
// change as it is noticed. A node that is itself master reports to no one. When
// the node's belief about who is master changes, the session moves to the new one;
// when a session drops it is opened again with backoff and begins again with the
// whole list, which is what lets the master forget a node it can no longer hear.
// Changes are found by looking at the source every interval.
func (n *Node) RunReporter(ctx context.Context, src Source, every time.Duration) {
	if every <= 0 {
		every = time.Second
	}
	delay := time.Second
	for ctx.Err() == nil {
		if n.mem.IsMaster() {
			if !sleep(ctx, every) {
				return
			}
			continue
		}
		start := time.Now()
		if err := n.reportOnce(ctx, src, every); err != nil && ctx.Err() == nil {
			n.log.Warn("report session to the master ended", "error", err)
		}
		if time.Since(start) > 30*time.Second {
			delay = time.Second
		}
		if !sleep(ctx, delay) {
			return
		}
		if delay < 15*time.Second {
			delay = delay * 8 / 5
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (n *Node) reportOnce(ctx context.Context, src Source, every time.Duration) error {
	_, master := n.mem.View()
	if master == n.cfg.Self.NodeID {
		return nil
	}
	conn, err := n.Dial(master)
	if err != nil {
		return err
	}
	sctx, stop := context.WithCancel(ctx)
	defer stop()
	stream, err := pb.NewMasterClient(conn).Session(sctx)
	if err != nil {
		return err
	}

	var seq uint64
	send := func(r *pb.Report) error {
		seq++
		r.Seq = seq
		return stream.Send(r)
	}
	last := index(src.Snapshot())
	if err := send(&pb.Report{Body: &pb.Report_Agents{Agents: &pb.AgentsReport{Full: true, Agents: values(last)}}}); err != nil {
		return err
	}

	recvErr := make(chan error, 1)
	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
				recvErr <- err
				return
			}
		}
	}()

	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = stream.CloseSend()
			return nil
		case err := <-recvErr:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case <-tick.C:
		}
		if _, now := n.mem.View(); now != master {
			_ = stream.CloseSend()
			return nil // the master changed: begin again with the new one
		}
		next := index(src.Snapshot())
		for name, a := range next {
			if prev, ok := last[name]; !ok || !proto.Equal(prev, a) {
				if err := send(&pb.Report{Body: &pb.Report_Event{Event: &pb.StateEvent{Change: &pb.StateEvent_Changed{Changed: a}}}}); err != nil {
					return err
				}
			}
		}
		for name := range last {
			if _, ok := next[name]; !ok {
				if err := send(&pb.Report{Body: &pb.Report_Event{Event: &pb.StateEvent{Change: &pb.StateEvent_Removed{Removed: name}}}}); err != nil {
					return err
				}
			}
		}
		last = next
	}
}

func index(as []*pb.AgentInfo) map[string]*pb.AgentInfo {
	m := make(map[string]*pb.AgentInfo, len(as))
	for _, a := range as {
		if a.GetName() != "" {
			m[a.GetName()] = a
		}
	}
	return m
}

func values(m map[string]*pb.AgentInfo) []*pb.AgentInfo {
	out := make([]*pb.AgentInfo, 0, len(m))
	for _, a := range m {
		out = append(out, a)
	}
	return out
}
