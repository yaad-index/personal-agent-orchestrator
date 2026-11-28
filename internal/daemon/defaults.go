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
// to an agent the node can route to by name. The default is the channel's agent,
// so it must belong to the channel's owner (ADR-0058 point 9); the store moves
// an agent off any channel it was the default of, so it has one (point 8).
type defaultsAPI struct {
	store    *defaultagent.Store
	channels map[string]bool
	// owners holds each place a default can be set to the owner of its channel,
	// and chanOf to the channel's name.
	owners, chanOf map[string]string
	resolve        func(core.Target) ([]core.Profile, error)
	// logs returns the agents' log channels, by channel name; nil for none.
	logs func() map[string]string
}

func newDefaultsAPI(store *defaultagent.Store, chs []config.Channel, resolve func(core.Target) ([]core.Profile, error)) defaultsAPI {
	a := defaultsAPI{store: store, channels: map[string]bool{}, owners: map[string]string{}, chanOf: map[string]string{}, resolve: resolve}
	for _, c := range chs {
		keys := []string{c.Name}
		// An admitted group and a named topic may have a default of their own,
		// under their destination name (ADR-0038 point 5).
		for _, g := range c.Groups {
			for _, n := range g.Names() {
				keys = append(keys, comm.DefaultKey(n))
			}
		}
		for _, k := range keys {
			a.channels[k], a.owners[k], a.chanOf[k] = true, c.Owner, c.Name
		}
	}
	for ch, agent := range store.Document().Defaults {
		switch {
		case !a.channels[ch]:
			slog.Warn("a stored default agent names a channel that is not configured; it is kept and has no effect", "channel", ch, "agent", agent)
		case a.notOwners(ch, agent) != nil:
			slog.Warn("a stored default agent does not belong to its channel's owner; it is kept and has no effect", "channel", ch, "agent", agent, "reason", a.notOwners(ch, agent))
		}
	}
	return a
}

// notOwners says why agent may not be the agent of the channel at key, or nil
// if it may: it must belong to the channel's owner.
func (a defaultsAPI) notOwners(key, agent string) error {
	if a.logs != nil {
		if of, ok := a.logs()[a.chanOf[key]]; ok {
			return translated{local.ErrBadRequest, fmt.Errorf("channel %q is agent %q's log channel, which takes no input, so it has no agent", a.chanOf[key], of)}
		}
	}
	ps, err := a.resolve(core.Target{Kind: core.ByName, Value: agent})
	if err != nil || len(ps) != 1 || ps[0].Name != agent {
		return translated{local.ErrNotFound, fmt.Errorf("no agent %q", agent)}
	}
	switch user := ps[0].User; {
	case user == "":
		return translated{local.ErrBadRequest, fmt.Errorf("agent %q belongs to no user: name one with user in its profile", agent)}
	case user != a.owners[key]:
		return translated{local.ErrBadRequest, fmt.Errorf("channel %q is %s's and agent %q is %s's: a channel's agent belongs to its owner", a.chanOf[key], a.owners[key], agent, user)}
	}
	return nil
}

// Get is the channel's default agent as the layer routes by it: one that does
// not belong to the channel's owner has no effect.
func (a defaultsAPI) Get(key string) (string, bool) {
	agent, ok := a.store.Get(key)
	if !ok || a.notOwners(key, agent) != nil {
		return "", false
	}
	return agent, true
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
	if err := a.notOwners(channel, agent); err != nil {
		return local.DefaultChangeJSON{}, err
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
