package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/adapter"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/identity"
	"github.com/yaad-index/personal-agent-orchestrator/internal/rejected"
)

// rejectRecorder is the layer's write-only view of the rejected-inbound record.
type rejectRecorder struct{ s *rejected.Store }

func (r rejectRecorder) Record(channel, sender, name, kind, reason, payload string) error {
	return r.s.Record(channel, sender, name, rejected.Kind(kind), reason, payload)
}

// wireChannels gives the layer what channel inbound needs and builds one adapter
// per provider that carries a channel, registering each of its channels with the
// layer (ADR-0058 points 1 and 2). It admits the channel ids to the ledger first,
// so an id is never reused (ADR-0021 point 2), and returns the adapters for the
// caller to run. A kind not compiled in, an unset credential, or a reused id stops
// the node here.
func wireChannels(cfg *config.Config, creds map[string]string, table adapter.Table, layer *comm.Layer, ids *identity.Store, rejects *rejected.Store) ([]adapter.Adapter, error) {
	layer.SetIdentity(ids, rejectRecorder{rejects})
	layer.SetCompositeWindow(cfg.Orchestrator.MessageWindow)
	if len(cfg.Orchestrator.Channels) == 0 {
		return nil, nil
	}
	chanSpecs := make([]adapter.Spec, 0, len(cfg.Orchestrator.Channels))
	chanIDs := make(map[string]int, len(cfg.Orchestrator.Channels))
	providerOf := make(map[string]string, len(cfg.Orchestrator.Channels))
	carried := map[string][]string{}
	for _, c := range cfg.Orchestrator.Channels {
		chanSpecs = append(chanSpecs, adapter.Spec{ID: c.ID, Name: c.Name})
		chanIDs[c.Name], providerOf[c.Name] = c.ID, c.Provider
		carried[c.Provider] = append(carried[c.Provider], c.Name)
	}
	var specs []adapter.Spec
	for _, p := range cfg.Orchestrator.Providers {
		if len(carried[p.Name]) == 0 {
			continue // a provider that carries no channel is not brought up
		}
		specs = append(specs, adapter.Spec{Name: p.Name, Kind: p.Kind, Credential: creds[p.Name], Options: p.Options, Channels: carried[p.Name]})
	}
	ledger, err := adapter.OpenLedger(filepath.Join(cfg.Orchestrator.StateDir, "channels.json"))
	if err != nil {
		return nil, err
	}
	if err := ledger.Admit(chanSpecs); err != nil {
		return nil, err
	}
	adapters, err := table.Build(specs, layer)
	if err != nil {
		return nil, err
	}
	layer.SetChannelIDs(chanIDs)
	layer.SetGroups(admittedGroups(cfg.Orchestrator.Channels))
	layer.SetScopes(channelScopes(cfg.Orchestrator.Channels))
	layer.SetProviders(providerOf)
	for _, a := range adapters {
		for _, c := range a.Channels() {
			layer.AddChannel(c)
		}
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
		out = append(out, comm.Binding{Agent: b.Agent, Stream: b.Stream, To: comm.Dest{Kind: d.Kind, Name: d.Name, Channel: d.Channel}, Level: int(b.Level), Prefix: b.Prefix})
	}
	return out
}

// rekeyIdentity moves the senders bound under a channel's name onto its
// provider's (ADR-0058 point 1), keeping a copy of the store from before beside
// it. A sender that would be bound to two subjects on one provider stops the
// node, naming both, with nothing changed.
func rekeyIdentity(ids interface {
	Rekey(map[string]string, string) (int, error)
}, o config.Orchestrator) error {
	to := make(map[string]string, len(o.Channels))
	for _, c := range o.Channels {
		to[c.Name] = c.Provider
	}
	backup := filepath.Join(o.StateDir, "identity.db.pre-provider-"+time.Now().UTC().Format("20060102T150405Z"))
	n, err := ids.Rekey(to, backup)
	if err != nil {
		return fmt.Errorf("binding senders by provider: %w", err)
	}
	if n > 0 {
		slog.Info("senders bound by channel are now bound by its provider; the store from before is kept", "moved", n, "copy", backup)
	}
	return nil
}

// warnUnknownOwners logs each channel whose owner is no subject the node knows.
// Such a channel acts on nothing until the subject is added, which fails safe
// but is easy to miss, as a misspelt owner would be.
func warnUnknownOwners(ids interface {
	Subjects() ([]identity.Subject, error)
}, chs []config.Channel) {
	subs, err := ids.Subjects()
	if err != nil {
		slog.Warn("could not list the subjects to check the channels' owners", "error", err)
		return
	}
	known := make(map[string]bool, len(subs))
	for _, s := range subs {
		known[s.Name] = true
	}
	for _, c := range chs {
		if !known[c.Owner] {
			slog.Warn("a channel's owner is no subject the node knows: the channel acts on nothing until it is added", "channel", c.Name, "owner", c.Owner)
		}
	}
}

// channelScopes is each channel's owner and one conversation.
func channelScopes(chs []config.Channel) map[string]comm.Scope {
	out := make(map[string]comm.Scope, len(chs))
	for _, c := range chs {
		s := comm.Scope{Owner: c.Owner}
		if group, topic, ok := c.TargetGroup(); ok {
			s.Conversation = group
			if topic != "" {
				s.Conversation = comm.TopicConversation(group, topic)
			}
		}
		out[c.Name] = s
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
			// A named topic is read by its group's readers (ADR-0038 point 10).
			for _, t := range g.Topics {
				out[c.Name][comm.TopicConversation(g.ID, t.ID)] = comm.Group{Name: t.Name, Policy: policy, Topic: true}
			}
		}
	}
	return out
}

// providerCredentials reads each provider's credential: from the variable it
// names, which is then taken out of the environment, or through the secret it
// names (ADR-0057 point 2), a variable's value as read at start or a file's,
// without the line end a file holds. A provider whose credential cannot be
// read is given none, and its adapter reports it as an unset credential.
func providerCredentials(o config.Orchestrator, secretValues map[string]string) map[string]string {
	read := readSecret(o, secretValues)
	creds := make(map[string]string, len(o.Providers))
	for _, p := range o.Providers {
		if p.CredentialEnv != "" {
			creds[p.Name] = os.Getenv(p.CredentialEnv)
			continue
		}
		b, err := read(p.CredentialSecret)
		if err != nil {
			slog.Warn("a provider's credential could not be read", "provider", p.Name, "error", err)
			continue
		}
		creds[p.Name] = strings.TrimRight(string(b), "\r\n")
	}
	for _, p := range o.Providers {
		if p.CredentialEnv != "" {
			_ = os.Unsetenv(p.CredentialEnv)
		}
	}
	return creds
}
