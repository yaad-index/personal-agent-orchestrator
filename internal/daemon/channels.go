package daemon

import (
	"path/filepath"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/adapter"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/identity"
	"github.com/yaad-index/personal-agent-orchestrator/internal/rejected"
)

// rejectRecorder is the layer's write-only view of the rejected-inbound record.
type rejectRecorder struct{ s *rejected.Store }

func (r rejectRecorder) Record(channel, sender, kind, reason, payload string) error {
	return r.s.Record(channel, sender, rejected.Kind(kind), reason, payload)
}

// wireChannels gives the layer what channel inbound needs and builds the
// configured adapters, registering each as a channel. It admits the channel ids to
// the ledger first, so an id is never reused (ADR-0021 point 2), and returns the
// adapters for the caller to run. A kind not compiled in, an unset credential, or a
// reused id stops the node here.
func wireChannels(cfg *config.Config, creds map[string]string, table adapter.Table, layer *comm.Layer, ids *identity.Store, rejects *rejected.Store) ([]adapter.Adapter, error) {
	layer.SetIdentity(ids, rejectRecorder{rejects})
	layer.SetCompositeWindow(cfg.Orchestrator.MessageWindow)
	layer.SetBindings(standingBindings(cfg.Orchestrator.Bindings))
	if len(cfg.Orchestrator.Channels) == 0 {
		return nil, nil
	}
	specs := make([]adapter.Spec, 0, len(cfg.Orchestrator.Channels))
	chanIDs := make(map[string]int, len(cfg.Orchestrator.Channels))
	for _, c := range cfg.Orchestrator.Channels {
		specs = append(specs, adapter.Spec{ID: c.ID, Name: c.Name, Kind: c.Kind, Credential: creds[c.Name], Options: c.Options})
		chanIDs[c.Name] = c.ID
	}
	ledger, err := adapter.OpenLedger(filepath.Join(cfg.Orchestrator.StateDir, "channels.json"))
	if err != nil {
		return nil, err
	}
	if err := ledger.Admit(specs); err != nil {
		return nil, err
	}
	adapters, err := table.Build(specs, layer)
	if err != nil {
		return nil, err
	}
	layer.SetChannelIDs(chanIDs)
	layer.SetGroups(admittedGroups(cfg.Orchestrator.Channels))
	for _, a := range adapters {
		layer.AddChannel(a)
	}
	return adapters, nil
}

// standingBindings is the configured standing bindings as the layer routes them
// (ADR-0029 point 3). Loading the config checked every destination's name.
func standingBindings(bs []config.Binding) []comm.Binding {
	out := make([]comm.Binding, 0, len(bs))
	for _, b := range bs {
		d, err := config.ParseDestination(b.To)
		if err != nil {
			continue
		}
		out = append(out, comm.Binding{Agent: b.Agent, To: comm.Dest{Kind: d.Kind, Name: d.Name, Channel: d.Channel}, Level: b.Level, Prefix: b.Prefix})
	}
	return out
}

// admittedGroups is each channel's admitted group conversations with their member
// policy (ADR-0028 points 1 and 2). A policy the layer has no name for is left
// out, so the group is refused rather than served under a guess.
func admittedGroups(chs []config.Channel) map[string]map[string]comm.Group {
	out := map[string]map[string]comm.Group{}
	for _, c := range chs {
		for _, g := range c.Groups {
			var policy string
			switch g.Members {
			case config.MembersOperators:
				policy = comm.PolicyOperators
			default:
				continue
			}
			if out[c.Name] == nil {
				out[c.Name] = map[string]comm.Group{}
			}
			out[c.Name][g.ID] = comm.Group{Name: g.Name, Policy: policy}
		}
	}
	return out
}
