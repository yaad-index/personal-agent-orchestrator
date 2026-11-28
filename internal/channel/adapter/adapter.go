// Package adapter is how a chat platform is brought in as a channel (ADR-0019
// point 1, ADR-0020 points 5 and 6). An adapter is compiled in behind one
// contract; the node configuration only selects and configures the adapters this
// build contains, and the table of them is one explicit map in this package, so
// what a build can talk to is readable in one place (package builtin).
package adapter

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// Spec is one configured provider, with its credential resolved, and the
// channels it carries (ADR-0058 points 1 and 2). The ledger of channel ids
// reads a Spec as one channel, by its ID and Name.
type Spec struct {
	// ID is a channel's numeric id (ADR-0021 point 1), as the ledger reads it.
	ID int
	// Name is the provider's name, or a channel's as the ledger reads it.
	Name string
	// Kind is the adapter kind.
	Kind string
	// Credential is the value of the provider's credential. It is never logged
	// or put in an error.
	Credential string
	// Options are the adapter's own settings.
	Options map[string]any
	// Channels are the channels the provider carries, by name.
	Channels []string
}

// Handler is what the layer offers an adapter.
type Handler interface {
	// ReceiveFromChannel hands the layer one inbound message or registration
	// attempt and returns what to do next.
	ReceiveFromChannel(ctx context.Context, in comm.ChannelInbound) (comm.ChannelResult, error)
	// Destinations returns the conversations on the channel that a message is
	// shown in, by the platform's id for each: operators' private conversations
	// and admitted groups, each only if every reader there may see it (ADR-0028
	// points 5 and 6).
	Destinations(channel string, m core.Message) []string
	// NativeOf recovers the platform's own message id from a composite id, only if
	// it is this channel's own (ADR-0021 point 4).
	NativeOf(channel, composite string) (native string, ok bool)
	// DefaultAgent returns the channel's default agent, whose output the adapter
	// shows without a name prefix (ADR-0023 point 5).
	DefaultAgent(channel string) (agent string, ok bool)
	// DefaultAgentIn returns the default agent of one conversation on the
	// channel: its own, if it has one, and otherwise the channel's (ADR-0038
	// point 5). Its output in that conversation carries no name prefix.
	DefaultAgentIn(channel, conversation string) (agent string, ok bool)
}

// Adapter is a provider the node brings up: one connection to the platform,
// with the loop that receives from it, carrying the provider's channels
// (ADR-0058 point 1). Each channel is the channel contract of ADR-0009; one
// that reports the platform's id for what it sends also implements
// comm.NativeSender.
type Adapter interface {
	// Run receives from the platform until ctx is done, handing each inbound to
	// the Handler, which places it in one of the provider's channels.
	Run(ctx context.Context) error
	// Channels are the provider's channels, one for each name the spec gave.
	Channels() []comm.Channel
}

// Factory builds an adapter for a spec.
type Factory func(spec Spec, h Handler) (Adapter, error)

// Kind is one adapter compiled into the build.
type Kind struct {
	// New builds the adapter.
	New Factory
	// Check validates a channel's options without a credential and without the
	// network, so a configuration can be checked before the node runs. New applies
	// the same validation.
	Check func(options map[string]any) error
}

// Table maps an adapter kind to the adapter.
type Table map[string]Kind

// Kinds lists the kinds in the table, sorted.
func (t Table) Kinds() []string {
	out := make([]string, 0, len(t))
	for k := range t {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Check validates each provider's kind and options, one spec per provider
// (ADR-0058 point 1), and reports every one that fails, not only the first. It
// does not look at credentials.
func (t Table) Check(specs []Spec) error {
	var errs []error
	for _, s := range specs {
		k, ok := t[s.Kind]
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("provider %w", t.notCompiled(s)))
		case k.Check == nil:
			errs = append(errs, fmt.Errorf("provider %q: kind %q has no option check", s.Name, s.Kind))
		default:
			if err := k.Check(s.Options); err != nil {
				errs = append(errs, fmt.Errorf("provider %q: %w", s.Name, err))
			}
		}
	}
	return errors.Join(errs...)
}

func (t Table) notCompiled(s Spec) error {
	return fmt.Errorf("%q: kind %q is not compiled into this build (have: %s)", s.Name, s.Kind, strings.Join(t.Kinds(), ", "))
}

// Build brings up one adapter per provider spec. A kind that is not compiled in
// is an error, and so is a spec with an empty credential, so a misconfigured
// provider stops the node loudly instead of being skipped (ADR-0020 point 6).
func (t Table) Build(specs []Spec, h Handler) ([]Adapter, error) {
	out := make([]Adapter, 0, len(specs))
	for _, s := range specs {
		k, ok := t[s.Kind]
		if !ok {
			return nil, fmt.Errorf("provider %w", t.notCompiled(s))
		}
		if s.Credential == "" {
			return nil, fmt.Errorf("provider %q: its credential is empty or unset", s.Name)
		}
		a, err := k.New(s, h)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", s.Name, err)
		}
		var names []string
		for _, c := range a.Channels() {
			names = append(names, c.Name())
		}
		if !slices.Equal(names, s.Channels) {
			return nil, fmt.Errorf("provider %q: the adapter carries channels %q, not %q", s.Name, names, s.Channels)
		}
		out = append(out, a)
	}
	return out, nil
}

// CheckConfig checks every configured provider's kind and options against the
// table, as starting the node would. A provider is defined in the orchestrator
// config at path only (ADR-0058 point 1), so an error names that file.
func (t Table) CheckConfig(path string, o config.Orchestrator) error {
	specs := make([]Spec, 0, len(o.Providers))
	for _, p := range o.Providers {
		specs = append(specs, Spec{Name: p.Name, Kind: p.Kind, Options: p.Options})
	}
	if err := t.Check(specs); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
