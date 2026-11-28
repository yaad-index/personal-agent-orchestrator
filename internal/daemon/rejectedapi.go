package daemon

import (
	"context"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/rejected"
)

// rejectedAPI is the operator's reading of the rejected-inbound record
// (local.RejectedAPI). It only lists.
type rejectedAPI struct{ s *rejected.Store }

func (a rejectedAPI) Rejected(context.Context) ([]local.RejectedJSON, error) {
	es, err := a.s.List()
	out := make([]local.RejectedJSON, 0, len(es))
	for _, e := range es {
		out = append(out, local.RejectedJSON{Channel: e.Channel, Sender: e.Sender, Name: e.Name, Kind: string(e.Kind), Reason: e.Reason,
			First: e.First, Last: e.Last, Count: e.Count, Payload: e.Payload})
	}
	return out, err
}
