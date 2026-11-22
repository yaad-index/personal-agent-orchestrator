package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/defaultagent"
)

// defaultsFile is the stored document of the channels' default agents, in the
// state directory.
const defaultsFile = "channel-defaults.json"

// defaultsAPI is the operator's control of the channels' default agents
// (local.DefaultsAPI). A default can be set only on a configured channel that
// receives chat, or on an admitted group or named topic as group:<name>, and only
// to an agent the node can route to by name.
type defaultsAPI struct {
	store    *defaultagent.Store
	channels map[string]bool
	resolve  func(core.Target) ([]core.Profile, error)
}

func newDefaultsAPI(store *defaultagent.Store, chs []config.Channel, resolve func(core.Target) ([]core.Profile, error)) defaultsAPI {
	names := make(map[string]bool, len(chs))
	for _, c := range chs {
		names[c.Name] = true
		// An admitted group and a named topic may have a default of their own,
		// under their destination name (ADR-0038 point 5).
		for _, g := range c.Groups {
			for _, n := range g.Names() {
				names[comm.DefaultKey(n)] = true
			}
		}
	}
	for ch, agent := range store.Document().Defaults {
		if !names[ch] {
			slog.Warn("a stored default agent names a channel that is not configured; it is kept and has no effect", "channel", ch, "agent", agent)
		}
	}
	return defaultsAPI{store: store, channels: names, resolve: resolve}
}

func (a defaultsAPI) Defaults(context.Context) (local.DefaultsJSON, error) {
	d := a.store.Document()
	out := local.DefaultsJSON{Version: d.Version, Defaults: make([]local.DefaultJSON, 0, len(d.Defaults))}
	for ch, agent := range d.Defaults {
		out.Defaults = append(out.Defaults, local.DefaultJSON{Channel: ch, Agent: agent})
	}
	sort.Slice(out.Defaults, func(i, j int) bool { return out.Defaults[i].Channel < out.Defaults[j].Channel })
	return out, nil
}

func (a defaultsAPI) SetDefault(_ context.Context, channel, agent string) (local.DefaultChangeJSON, error) {
	if err := a.chatChannel(channel); err != nil {
		return local.DefaultChangeJSON{}, err
	}
	ps, err := a.resolve(core.Target{Kind: core.ByName, Value: agent})
	if err != nil || len(ps) != 1 || ps[0].Name != agent {
		return local.DefaultChangeJSON{}, translated{local.ErrNotFound, fmt.Errorf("no agent %q", agent)}
	}
	c, err := a.store.Set(channel, agent)
	if err != nil {
		return local.DefaultChangeJSON{}, err
	}
	return local.DefaultChangeJSON{Channel: channel, Agent: agent, Previous: c.Previous, Left: c.Left}, nil
}

func (a defaultsAPI) ClearDefault(_ context.Context, channel string) (local.DefaultChangeJSON, error) {
	if err := a.chatChannel(channel); err != nil {
		return local.DefaultChangeJSON{}, err
	}
	c, err := a.store.Clear(channel)
	if err != nil {
		return local.DefaultChangeJSON{}, err
	}
	return local.DefaultChangeJSON{Channel: channel, Previous: c.Previous}, nil
}

func (a defaultsAPI) chatChannel(channel string) error {
	if !a.channels[channel] {
		return translated{local.ErrNotFound, fmt.Errorf("no chat channel or group %q: a default agent is set on a configured channel, or on an admitted group or named topic as group:<name>", channel)}
	}
	return nil
}
