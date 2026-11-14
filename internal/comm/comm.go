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
	"strings"
	"sync"

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
	// Allow, when set, narrows the agents Target resolves to: an agent it
	// returns false for is left out, and if none is left the target matches
	// no agent, in the same words as one that does not exist. It is how access
	// bounds routing (ADR-0012 point 4); orchestrator-side only.
	Allow func(core.Profile) bool
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
	Deliver(ctx context.Context, profiles []core.Profile, m core.Message) []route.Result
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

	mu    sync.RWMutex
	bound map[string]string // agent name to the channel its output goes to

	// self is this node's id in a mesh, empty outside one; known says whether an id
	// is a node of the mesh.
	self  string
	known func(string) bool
}

// New returns a Layer.
func New(store *msgstore.Store, router Router) *Layer {
	return &Layer{store: store, router: router, pre: make(map[string]Preprocessor), bound: make(map[string]string)}
}

// SetNode makes the layer part of a mesh. Message ids shown to a human are then
// qualified by the node (Qualify), and a reply may name a message by a qualified id.
// Call it before the layer starts receiving.
func (l *Layer) SetNode(self string, known func(string) bool) {
	l.self, l.known = self, known
}

// Qualify names a message id by this node, so an id shown to a human or logged
// anywhere in the mesh says which node holds the message (ADR-0017 point 10).
// Outside a mesh it is the id unchanged.
func (l *Layer) Qualify(id string) string {
	if l.self == "" || id == "" {
		return id
	}
	return l.self + ":" + id
}

// splitID reads a message id a human named: unqualified, or "<node>:<id>". An id
// with a colon whose node is neither this one nor in the mesh is not a message of
// any node; ok is false for it.
func (l *Layer) splitID(id string) (node, rest string, ok bool) {
	n, r, found := strings.Cut(id, ":")
	switch {
	case !found:
		return "", id, true
	case l.self != "" && (n == l.self || (l.known != nil && l.known(n))):
		return n, r, true
	}
	return "", id, false
}

// Preprocess registers the preprocessor for a source kind. Call it before the
// layer starts receiving.
func (l *Layer) Preprocess(source string, p Preprocessor) { l.pre[source] = p }

// AddChannel registers a human front. Call it before the layer starts sending.
func (l *Layer) AddChannel(c Channel) { l.channels = append(l.channels, c) }

// HasChannel reports whether a channel of that name is registered.
func (l *Layer) HasChannel(name string) bool {
	for _, c := range l.channels {
		if c.Name() == name {
			return true
		}
	}
	return false
}

// Channels returns the names of the registered channels, in the order added.
func (l *Layer) Channels() []string {
	out := make([]string, 0, len(l.channels))
	for _, c := range l.channels {
		out = append(out, c.Name())
	}
	return out
}

// Bind sends what the agent says to the named channel only, instead of to every
// channel (ADR-0006 point 1: a temporary agent has its own stream on a channel
// named at launch). Binding an agent again replaces its binding.
func (l *Layer) Bind(agent, channel string) error {
	if !l.HasChannel(channel) {
		return fmt.Errorf("no channel %q", channel)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.bound[agent] = channel
	return nil
}

// Bound returns the channel an agent's output is bound to, if it is bound.
func (l *Layer) Bound(agent string) (string, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	ch, ok := l.bound[agent]
	return ch, ok
}

// Unbind returns the agent's output to every channel.
func (l *Layer) Unbind(agent string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.bound, agent)
}

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
	if in.Allow != nil {
		kept := make([]core.Profile, 0, len(targets))
		for _, t := range targets {
			if in.Allow(t) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			return Delivery{}, route.NoMatch(in.Target)
		}
		targets = kept
	}
	scopes := make([]string, len(targets))
	for i, t := range targets {
		scopes[i] = t.Name
	}

	reply, forwards, err := l.resolveReply(in, targets)
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

	if len(forwards) == 0 {
		return Delivery{Message: msg, Results: l.router.Deliver(ctx, targets, msg)}, nil
	}
	// An agent on another node is given the reference its own node resolves, not the
	// one stored here.
	var results []route.Result
	for _, t := range targets {
		m := msg
		if f, ok := forwards[t.Name]; ok {
			m.Reply, m.ReplyQuote = f.ref, f.quote
		}
		results = append(results, l.router.Deliver(ctx, []core.Profile{t}, m)...)
	}
	return Delivery{Message: msg, Results: results}, nil
}

// forward is the reply reference sent to an agent on another node, in that node's
// own terms: the id of a message it holds, or a snippet for it to resolve.
type forward struct{ ref, quote string }

// resolveReply turns a reply reference into the message id stored with the
// message, and for an agent on another node the reference that node resolves. An id
// must be in scope for every addressed agent; a quote is resolved within the one
// addressed agent's scope, and a quote with several addressed agents is refused,
// not guessed. A message of another node is never in the scope of an agent here,
// and an agent there can only be answered with a message its node holds, so a
// reference that does not fit is not found, in the same words as an absent one.
func (l *Layer) resolveReply(in Inbound, targets []core.Profile) (string, map[string]forward, error) {
	forwards := map[string]forward{}
	switch {
	case in.Reply != "":
		node, id, ok := l.splitID(in.Reply)
		if !ok {
			return "", nil, fmt.Errorf("reply %q: %w", in.Reply, msgstore.ErrNotFound)
		}
		stored := in.Reply
		for _, t := range targets {
			switch {
			case t.Node != "":
				if node != t.Node {
					return "", nil, fmt.Errorf("reply %q: %w", in.Reply, msgstore.ErrNotFound)
				}
				forwards[t.Name] = forward{ref: id}
			default:
				if node != "" && node != l.self {
					return "", nil, fmt.Errorf("reply %q: %w", in.Reply, msgstore.ErrNotFound)
				}
				// Out of scope and absent are the same answer (not found), so a
				// reference cannot be used to probe for messages an agent never saw.
				if _, err := l.store.GetScoped(t.Name, id); err != nil {
					return "", nil, fmt.Errorf("reply %q: %w", in.Reply, err)
				}
				stored = id
			}
		}
		return stored, forwards, nil
	case in.ReplyQuote != "":
		if len(targets) != 1 {
			return "", nil, errors.New("a quoted reply needs exactly one addressed agent")
		}
		if t := targets[0]; t.Node != "" {
			forwards[t.Name] = forward{quote: in.ReplyQuote}
			return "", forwards, nil
		}
		m, err := l.store.Resolve(targets[0].Name, in.ReplyQuote)
		if err != nil {
			return "", nil, err
		}
		return m.ID, nil, nil
	}
	return "", nil, nil
}

// Send is the single conduit to the human: it stores m in from's scope (so the
// agent can quote or fetch its own output) and hands it to every channel. A
// channel that fails does not stop the others; the errors are joined.
func (l *Layer) Send(ctx context.Context, from string, m core.Message) (core.Message, error) {
	stored, err := l.store.Add(m, from)
	if err != nil {
		return core.Message{}, err
	}
	l.mu.RLock()
	only, bound := l.bound[from]
	l.mu.RUnlock()
	if bound {
		return stored, l.sendTo(ctx, only, stored)
	}
	return stored, l.fanOut(ctx, stored)
}

// sendTo hands m to the one channel named. A channel that has gone is an error,
// never a silent fall back to every channel: the binding said where it goes.
func (l *Layer) sendTo(ctx context.Context, name string, m core.Message) error {
	for _, c := range l.channels {
		if c.Name() == name {
			if err := c.Send(ctx, m); err != nil {
				return fmt.Errorf("channel %q: %w", name, err)
			}
			return nil
		}
	}
	return fmt.Errorf("channel %q is not registered", name)
}

// Notify sends pao's own message to the human (an alert, a raw pane). Unlike
// Send it is stored in no agent's scope, so an agent can neither quote nor fetch
// it: what is for the operator alone (ADR-0007 point 2) never reaches an agent
// through the message map.
func (l *Layer) Notify(ctx context.Context, m core.Message) (core.Message, error) {
	m.Kind = core.MessageSystemEvent
	stored, err := l.store.Add(m)
	if err != nil {
		return core.Message{}, err
	}
	return stored, l.fanOut(ctx, stored)
}

// NotifyOn is Notify to the one channel named, for what is meant for the operator
// on that channel alone (a replay of an agent's past output for the viewer who
// asked for it). A channel that is not registered is an error, never a fall back to
// every channel.
func (l *Layer) NotifyOn(ctx context.Context, channel string, m core.Message) (core.Message, error) {
	if !l.HasChannel(channel) {
		return core.Message{}, fmt.Errorf("no channel %q", channel)
	}
	m.Kind = core.MessageSystemEvent
	stored, err := l.store.Add(m)
	if err != nil {
		return core.Message{}, err
	}
	return stored, l.sendTo(ctx, channel, stored)
}

func (l *Layer) fanOut(ctx context.Context, m core.Message) error {
	var errs []error
	for _, c := range l.channels {
		if err := c.Send(ctx, m); err != nil {
			errs = append(errs, fmt.Errorf("channel %q: %w", c.Name(), err))
		}
	}
	return errors.Join(errs...)
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
func (l *Layer) React(id, by, emoji string) error {
	node, rest, ok := l.splitID(id)
	if !ok || (node != "" && node != l.self) {
		return fmt.Errorf("reaction to %q: %w", id, msgstore.ErrNotFound)
	}
	return l.store.React(rest, by, emoji)
}

// ReactAs records an agent's reaction, only on a message in the agent's scope.
// A message outside it is reported as not found.
func (l *Layer) ReactAs(agent, id, emoji string) error { return l.store.ReactAs(agent, id, emoji) }
