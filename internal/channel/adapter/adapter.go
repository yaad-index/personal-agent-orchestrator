// Package adapter is how a chat platform is brought in as a channel (ADR-0019
// point 1, ADR-0020 points 5 and 6). An adapter is compiled in behind one
// contract; the node configuration only selects and configures the adapters this
// build contains, and the table of them is one explicit map in this package, so
// what a build can talk to is readable in one place (package builtin).
package adapter

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
)

// Spec is one configured channel, with its credential resolved.
type Spec struct {
	// ID is the channel's numeric id (ADR-0021 point 1).
	ID int
	// Name is the channel's name, the namespace of its bindings.
	Name string
	// Kind is the adapter kind.
	Kind string
	// Credential is the value of the channel's credential variable. It is never
	// logged or put in an error.
	Credential string
	// Options are the adapter's own settings.
	Options map[string]any
}

// Handler is what the layer offers an adapter.
type Handler interface {
	// ReceiveFromChannel hands the layer one inbound message or registration
	// attempt and returns what to do next.
	ReceiveFromChannel(ctx context.Context, in comm.ChannelInbound) (comm.ChannelResult, error)
	// Recipients returns the sender ids on the channel that pao's own output goes
	// to: those bound to an operator-flagged subject.
	Recipients(channel string) []string
	// NativeOf recovers the platform's own message id from a composite id, only if
	// it is this channel's own (ADR-0021 point 4).
	NativeOf(channel, composite string) (native string, ok bool)
}

// Adapter is a channel the node brings up: the channel contract of ADR-0009 and the
// loop that receives from the platform. An adapter that reports the platform's id
// for what it sends also implements comm.NativeSender.
type Adapter interface {
	comm.Channel
	// Run receives from the platform until ctx is done, handing each inbound to
	// the Handler.
	Run(ctx context.Context) error
}

// Factory builds an adapter for a spec.
type Factory func(spec Spec, h Handler) (Adapter, error)

// Table maps an adapter kind to its factory.
type Table map[string]Factory

// Kinds lists the kinds in the table, sorted.
func (t Table) Kinds() []string {
	out := make([]string, 0, len(t))
	for k := range t {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Build brings up one adapter per spec. A kind that is not compiled in is an
// error, and so is a spec with an empty credential, so a misconfigured channel
// stops the node loudly instead of being skipped (ADR-0020 point 6).
func (t Table) Build(specs []Spec, h Handler) ([]Adapter, error) {
	out := make([]Adapter, 0, len(specs))
	for _, s := range specs {
		f, ok := t[s.Kind]
		if !ok {
			return nil, fmt.Errorf("channel %q: kind %q is not compiled into this build (have: %s)", s.Name, s.Kind, strings.Join(t.Kinds(), ", "))
		}
		if s.Credential == "" {
			return nil, fmt.Errorf("channel %q: its credential variable is empty or unset", s.Name)
		}
		a, err := f(s, h)
		if err != nil {
			return nil, fmt.Errorf("channel %q: %w", s.Name, err)
		}
		if a.Name() != s.Name {
			return nil, fmt.Errorf("channel %q: the adapter named itself %q", s.Name, a.Name())
		}
		out = append(out, a)
	}
	return out, nil
}
