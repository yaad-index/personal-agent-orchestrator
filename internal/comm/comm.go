// Package comm is the communication layer (ADR-0009): the one component that
// handles every source. Inbound, it preprocesses a source's input, normalizes
// it to pao's message, stores it and routes it to the agents it addresses.
// Outbound, it is the single conduit by which anything pao says reaches the
// human. A channel is the human-facing subset of sources (ADR-0009 point 1); a
// source kind never reaches an agent (ADR-0001, ADR-0009 point 3).
package comm

import (
	"context"
	"errors"
	"fmt"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/msgstore"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
)

// Inbound is what a source hands the layer, before normalization.
type Inbound struct {
	// Source is the source kind (a chat front, a peer, a reminder, a system
	// event). It selects the preprocessor and is otherwise orchestrator-side
	// only: it never reaches the agent.
	Source string
	Sender string
	Text   string
	Kind   core.MessageKind
	// Reply is the pao id of the message this one answers, when the source can
	// say so by id (ADR-0010 point 3: id-first from the human).
	Reply string
	// ReplyQuote is the fallback: a snippet of the message being answered.
	ReplyQuote  string
	Attachments []core.Attachment
	Target      core.Target
}

// Preprocessor does the source-specific work that must happen before routing,
// such as turning a voice note into text or expanding a reminder into a message
// (ADR-0009 point 4).
type Preprocessor interface {
	Preprocess(ctx context.Context, in Inbound) (Inbound, error)
}

// Router is the routing the layer hands normalized messages to.
type Router interface {
	Resolve(t core.Target) ([]core.Profile, error)
	Route(ctx context.Context, t core.Target, m core.Message) ([]route.Result, error)
}

// Channel is a human front: the human to pao line (ADR-0001, ADR-0009).
type Channel interface {
	Name() string
	// Send hands a message to the human. Whether the platform then delivers it
	// is the platform's affair (ADR-0009 point 6).
	Send(ctx context.Context, m core.Message) error
}

// Layer is safe for concurrent use once configured.
type Layer struct {
	store    *msgstore.Store
	router   Router
	pre      map[string]Preprocessor
	channels []Channel
}

// New returns a Layer.
func New(store *msgstore.Store, router Router) *Layer {
	return &Layer{store: store, router: router, pre: make(map[string]Preprocessor)}
}

// Preprocess registers the preprocessor for a source kind. Call it before the
// layer starts receiving.
func (l *Layer) Preprocess(source string, p Preprocessor) { l.pre[source] = p }

// AddChannel registers a human front. Call it before the layer starts sending.
func (l *Layer) AddChannel(c Channel) { l.channels = append(l.channels, c) }

// Delivery is the outcome of one inbound message.
type Delivery struct {
	Message core.Message
	Results []route.Result
}

// Receive normalizes in, stores it for the agents it addresses and routes it.
// The message is stored before it is delivered, so a quick reply that quotes it
// already finds it. A target that matches no agent is an error and nothing is
// stored.
func (l *Layer) Receive(ctx context.Context, in Inbound) (Delivery, error) {
	if p, ok := l.pre[in.Source]; ok {
		source := in.Source
		var err error
		if in, err = p.Preprocess(ctx, in); err != nil {
			return Delivery{}, fmt.Errorf("preprocess %q: %w", source, err)
		}
	}

	targets, err := l.router.Resolve(in.Target)
	if err != nil {
		return Delivery{}, err
	}
	scopes := make([]string, len(targets))
	for i, t := range targets {
		scopes[i] = t.Name
	}

	reply, err := l.resolveReply(in, scopes)
	if err != nil {
		return Delivery{}, err
	}

	msg, err := l.store.Add(core.Message{
		Sender:      in.Sender,
		Kind:        in.Kind,
		Text:        in.Text,
		Reply:       reply,
		Attachments: in.Attachments,
	}, scopes...)
	if err != nil {
		return Delivery{}, err
	}

	results, err := l.router.Route(ctx, in.Target, msg)
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{Message: msg, Results: results}, nil
}

// resolveReply turns a reply reference into a message id: an id must be in
// scope for every addressed agent; a quote is resolved within the one addressed
// agent's scope, and a quote with several addressed agents is refused, not
// guessed.
func (l *Layer) resolveReply(in Inbound, scopes []string) (string, error) {
	switch {
	case in.Reply != "":
		// The referenced message must be in the scope of every addressed agent.
		// Out of scope and absent are the same answer (not found), so a
		// reference cannot be used to probe for messages an agent never saw.
		for _, agent := range scopes {
			if _, err := l.store.GetScoped(agent, in.Reply); err != nil {
				return "", fmt.Errorf("reply %q: %w", in.Reply, err)
			}
		}
		return in.Reply, nil
	case in.ReplyQuote != "":
		if len(scopes) != 1 {
			return "", errors.New("a quoted reply needs exactly one addressed agent")
		}
		m, err := l.store.Resolve(scopes[0], in.ReplyQuote)
		if err != nil {
			return "", err
		}
		return m.ID, nil
	}
	return "", nil
}

// Send is the single conduit to the human: it stores m in from's scope (so the
// agent can quote or fetch its own output) and hands it to every channel. A
// channel that fails does not stop the others; the errors are joined.
func (l *Layer) Send(ctx context.Context, from string, m core.Message) (core.Message, error) {
	stored, err := l.store.Add(m, from)
	if err != nil {
		return core.Message{}, err
	}
	var errs []error
	for _, c := range l.channels {
		if err := c.Send(ctx, stored); err != nil {
			errs = append(errs, fmt.Errorf("channel %q: %w", c.Name(), err))
		}
	}
	return stored, errors.Join(errs...)
}

// ResolveQuote resolves a quote the agent used to reference a message. An
// unresolved or ambiguous quote is reported to the agent, never guessed.
func (l *Layer) ResolveQuote(agent, quote string) (core.Message, error) {
	return l.store.Resolve(agent, quote)
}

// Fetch returns a message by id, only if it is in the agent's scope.
func (l *Layer) Fetch(agent, id string) (core.Message, error) {
	return l.store.GetScoped(agent, id)
}

// React records the human's reaction on a message id. The human sees every
// message, so it is not limited to an agent's scope; an agent reacts through
// ReactAs.
func (l *Layer) React(id, by, emoji string) error { return l.store.React(id, by, emoji) }

// ReactAs records an agent's reaction, only on a message in the agent's scope.
// A message outside it is reported as not found.
func (l *Layer) ReactAs(agent, id, emoji string) error { return l.store.ReactAs(agent, id, emoji) }
