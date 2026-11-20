package comm

import (
	"context"
	"errors"
	"fmt"

	"github.com/yaad-index/personal-agent-orchestrator/internal/command"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// The kinds of rejection a Recorder is told, matching the record's own.
const (
	RecordMessage      = "message"
	RecordRegistration = "registration"
)

// The only words an unbound or unprivileged sender is ever sent. They are fixed:
// they say nothing of who exists, what is bound, or why (ADR-0019 point 5,
// ADR-0020 point 3).
const (
	ReplyRegistered = "registered."
	ReplyRefused    = "not accepted."
	ReplyNoAccess   = "no access."
)

// ReplyNotACommand answers an operator's press whose payload is not a pao command.
const ReplyNotACommand = "that button is not a pao command."

// ErrRecord wraps a failure to keep the record of a rejected inbound. It is for the
// adapter to log; the sender, who may be a stranger, is told nothing of it.
var ErrRecord = errors.New("could not record a rejected inbound")

// ErrNoIdentity means the layer was not given an identity store, so a channel
// inbound has no way to be attributed and none is accepted.
var ErrNoIdentity = errors.New("no identity store: channel inbound is not accepted")

// Identity is what the layer asks about a channel sender (ADR-0019 points 3 to 5,
// ADR-0020, ADR-0022).
type Identity interface {
	// Resolve returns the subject a (channel, sender) pair is bound to.
	Resolve(channel, sender string) (subject string, ok bool)
	// Register completes a registration if secret is a valid invitation for a free
	// pair.
	Register(channel, sender, secret string) (subject string, ok bool)
	// IsOperator reports whether the subject is flagged operator, read afresh.
	IsOperator(subject string) bool
	// Operators returns the sender ids on the channel that resolve to an
	// operator-flagged subject, read afresh.
	Operators(channel string) []string
}

// Recorder keeps what the layer did not act on (ADR-0019 point 7). It is write-only
// from here: nothing the layer holds reads the record back.
type Recorder interface {
	Record(channel, sender, kind, reason, payload string) error
}

// ChannelInbound is what a channel adapter hands the layer for one incoming
// message (ADR-0020 point 5).
type ChannelInbound struct {
	// Channel is the configured channel's name, the namespace of its bindings.
	Channel string
	// Sender is the platform's authenticated per-sender id (ADR-0019 point 2),
	// never a value read from the message text.
	Sender string
	// Text is the message text, or, for a registration, the secret presented.
	Text string
	// Registration marks the registration entry point: Text is a secret and not
	// a message.
	Registration bool
	// Native is the platform's own id for this message. The layer forms the
	// composite from it and the channel's id (ADR-0021 point 5).
	Native string
	// ReplyNative is the platform's own id for the message this one answers, when
	// the platform says so. It is resolved through the composite map; one that
	// does not resolve falls back to ReplyQuote.
	ReplyNative string
	// Target says which agents the message is for.
	Target core.Target
	// Reply and ReplyQuote reference the message this one answers, as for Inbound.
	Reply      string
	ReplyQuote string
	// Raw is the platform payload, kept only in the record of a rejection.
	Raw string
	// Press marks a button press (ADR-0027 point 4): Text is the command the
	// button carries, and Sender the platform's authenticated sender of the press.
	// A press is only ever a command; one that is not is refused and never routed.
	Press bool
	// Group marks a message from a group conversation, and Conversation is the
	// platform's id for that conversation. A group the channel does not admit is
	// denied whoever the sender is (ADR-0019 point 6, ADR-0028 point 1): the layer
	// records it and routes nothing, replies with nothing, and never reads it as a
	// registration. In an admitted group, a sender the group's member policy admits
	// is served as in a private conversation; anyone else is recorded and nothing
	// is said in the group (ADR-0028 point 2).
	Group        bool
	Conversation string
	// Join marks the platform's report that Sender joined the group conversation.
	// A join the group's member policy does not admit is recorded (ADR-0028
	// point 5); nothing is routed or answered either way.
	Join bool
	// MovedTo marks the platform's report that the group conversation moved to a
	// new id: Conversation is the old id and MovedTo the new one. It is recorded so
	// the operator can list the new id; the new id is not admitted by it.
	MovedTo string
}

// PolicyOperators is the member policy of a group that admits only subjects
// routed as the operator (ADR-0028 point 2).
const PolicyOperators = "operators"

// SetGroups gives the layer the group conversations each channel admits, by
// channel name and then the platform's conversation id, each with its member
// policy (ADR-0028 points 1 and 2). A group not given is refused. Call it before
// the layer starts receiving.
func (l *Layer) SetGroups(groups map[string]map[string]string) { l.groups = groups }

// groupPolicy returns the member policy of an admitted group.
func (l *Layer) groupPolicy(channel, conversation string) (string, bool) {
	p, ok := l.groups[channel][conversation]
	return p, ok && conversation != ""
}

// admits reports whether a group's member policy lets subject act in it. A policy
// it does not know admits no one.
func (l *Layer) admits(policy, subject string) bool {
	switch policy {
	case PolicyOperators:
		return l.identity.IsOperator(subject)
	}
	return false
}

// ChannelResult is what the adapter does next. Reply is the text to send the
// sender, empty to send nothing; Delivery is set when the message was routed.
// Addressed is set when the message was a command naming an agent or a tag: it is
// the sender's explicit address, which an adapter may keep as the sticky one.
type ChannelResult struct {
	Reply     string
	Delivery  *Delivery
	Addressed *core.Target
}

// Defaults is each channel's default agent (ADR-0023 point 5).
type Defaults interface {
	Get(channel string) (agent string, ok bool)
}

// SetDefaults gives the layer the channels' default agents. Call it before the
// layer starts receiving.
func (l *Layer) SetDefaults(d Defaults) { l.defaults = d }

// DefaultAgent returns the channel's default agent: the one that answers an inbound
// naming no agent, and whose output on that channel carries no name prefix
// (ADR-0023 point 5).
func (l *Layer) DefaultAgent(channel string) (string, bool) {
	if l.defaults == nil {
		return "", false
	}
	return l.defaults.Get(channel)
}

// SetOperatorAccess bounds what the operator's own messages may reach: an agent
// allow returns false for is left out of the operator's routing as if it did not
// exist (ADR-0012 points 2 and 4). Unset, the operator's routing is not narrowed.
// Call it before the layer starts receiving.
func (l *Layer) SetOperatorAccess(allow func(core.Profile) bool) { l.operatorAllow = allow }

// ReceiveAsOperator is Receive for a message from the operator, at the local
// channel or from an operator-flagged subject on a chat channel, bounded by what
// the operator may reach.
func (l *Layer) ReceiveAsOperator(ctx context.Context, in Inbound) (Delivery, error) {
	in.Allow = l.operatorAllow
	return l.Receive(ctx, in)
}

// SetIdentity gives the layer the identity store and the record of rejections that
// channel inbound needs. Call it before the layer starts receiving.
func (l *Layer) SetIdentity(id Identity, rec Recorder) { l.identity, l.recorder = id, rec }

func (l *Layer) reject(in ChannelInbound, kind, reason, payload string) error {
	if l.recorder == nil {
		return nil
	}
	if err := l.recorder.Record(in.Channel, in.Sender, kind, reason, payload); err != nil {
		return fmt.Errorf("%w: %w", ErrRecord, err)
	}
	return nil
}

// ReceiveFromChannel attributes one inbound message from a channel adapter and
// acts on it.
//
// A registration attempt gets one of two fixed replies, and no other reply says
// whether a sender is bound. A message from a sender with no binding is recorded
// and answered with nothing. A message from a bound subject that is not flagged
// operator is recorded as outside access and answered with a fixed refusal; it is
// never routed. A message from a subject flagged operator is routed as the
// operator, attributed under the subject's own name (ADR-0022 points 3 and 4). The
// flag is read here, per message (ADR-0022 point 5).
//
// The message goes to the agent the adapter's target names (an explicit address,
// or the sender's sticky one); failing that, to the author of the message it
// replies to; failing that, to the channel's default agent (ADR-0023 point 4).
//
// An error from recording is returned for the adapter to log; it is never text to
// send the sender.
func (l *Layer) ReceiveFromChannel(ctx context.Context, in ChannelInbound) (ChannelResult, error) {
	if l.identity == nil {
		return ChannelResult{}, ErrNoIdentity
	}

	if in.Group {
		return l.receiveFromGroup(ctx, in)
	}

	if in.Registration {
		if _, ok := l.identity.Register(in.Channel, in.Sender, in.Text); ok {
			return ChannelResult{Reply: ReplyRegistered}, l.reject(in, RecordRegistration, "registered", "")
		}
		return ChannelResult{Reply: ReplyRefused}, l.reject(in, RecordRegistration, "not accepted", "")
	}

	subject, ok := l.identity.Resolve(in.Channel, in.Sender)
	if !ok {
		return ChannelResult{}, l.reject(in, RecordMessage, "no subject", in.Raw)
	}
	if !l.identity.IsOperator(subject) {
		return ChannelResult{Reply: ReplyNoAccess}, l.reject(in, RecordMessage, "outside access", in.Raw)
	}
	return l.receiveAsOperator(ctx, in, subject)
}

// receiveFromGroup is ReceiveFromChannel for a message from a group conversation.
// Nothing refused here is answered in the group, where everyone in it would read
// the answer (ADR-0028 point 2).
func (l *Layer) receiveFromGroup(ctx context.Context, in ChannelInbound) (ChannelResult, error) {
	if in.MovedTo != "" {
		return ChannelResult{}, l.reject(in, RecordMessage, "conversation moved", "from "+in.Conversation+" to "+in.MovedTo)
	}
	// A secret said in a group is read by everyone in it: it is refused and not
	// kept, whether or not the group is admitted (ADR-0028 point 3).
	if in.Registration {
		return ChannelResult{}, l.reject(in, RecordRegistration, "from a group", "")
	}
	policy, ok := l.groupPolicy(in.Channel, in.Conversation)
	if !ok {
		if in.Join {
			return ChannelResult{}, nil
		}
		// The record names the conversation, so the operator can admit it by
		// the id the platform gives it (ADR-0028 point 1).
		reason := "group"
		if in.Conversation != "" {
			reason += " " + in.Conversation
		}
		return ChannelResult{}, l.reject(in, RecordMessage, reason, in.Raw)
	}
	subject, bound := l.identity.Resolve(in.Channel, in.Sender)
	admitted := bound && l.admits(policy, subject)
	switch {
	case in.Join && admitted:
		return ChannelResult{}, nil
	case in.Join:
		return ChannelResult{}, l.reject(in, RecordMessage, "joined outside policy", "conversation "+in.Conversation)
	case !bound:
		return ChannelResult{}, l.reject(in, RecordMessage, "no subject", in.Raw)
	case !admitted:
		return ChannelResult{}, l.reject(in, RecordMessage, "outside access", in.Raw)
	}
	return l.receiveAsOperator(ctx, in, subject)
}

// receiveAsOperator acts on an inbound from a subject routed as the operator, in a
// private conversation or an admitted group alike (ADR-0028 point 4).
func (l *Layer) receiveAsOperator(ctx context.Context, in ChannelInbound, subject string) (ChannelResult, error) {
	if in.Press && !command.Is(in.Text) {
		return ChannelResult{Reply: ReplyNotACommand}, l.reject(in, RecordMessage, "press not a command", in.Raw)
	}

	// A /pao command is pao's, whatever the target the adapter brings (ADR-0026
	// point 1). One that addresses an agent or a tag routes its message there.
	var addressed *core.Target
	if command.Is(in.Text) {
		res := command.Run(ctx, l.commandOps(), command.Caller{Channel: in.Channel, Operator: true}, in.Text)
		if res.Address == nil {
			return ChannelResult{Reply: res.Reply}, nil
		}
		addressed = res.Address
		in.Target, in.Text = *res.Address, res.Text
	}

	reply := in.Reply
	if reply == "" && in.ReplyNative != "" {
		if n, ok := l.channelIDs[in.Channel]; ok {
			reply, _ = l.composites.id(FormatComposite(n, in.ReplyNative))
		}
	}
	target := in.Target
	if target.Kind == 0 && reply != "" {
		// A reply to an agent's message with no address goes to that agent.
		if m, err := l.store.Get(reply); err == nil {
			if _, err := l.router.Resolve(core.Target{Kind: core.ByName, Value: m.Sender}); err == nil {
				target = core.Target{Kind: core.ByName, Value: m.Sender}
			}
		}
	}
	if target.Kind == 0 {
		if agent, ok := l.DefaultAgent(in.Channel); ok {
			target = core.Target{Kind: core.ByName, Value: agent}
		}
	}
	d, err := l.ReceiveAsOperator(ctx, Inbound{
		Source:     in.Channel,
		Sender:     subject,
		Text:       in.Text,
		Target:     target,
		Reply:      reply,
		ReplyQuote: in.ReplyQuote,
	})
	if err != nil {
		return ChannelResult{}, err
	}
	l.noteComposite(in.Channel, in.Native, d.Message.ID)
	return ChannelResult{Delivery: &d, Addressed: addressed}, nil
}

// SetCommands gives the layer the operations pao's chat commands run (ADR-0026
// point 2). Unset, a steering command is answered that it is not available. Call
// it before the layer starts receiving.
func (l *Layer) SetCommands(ops command.Ops) { l.commands = ops }

func (l *Layer) commandOps() command.Ops {
	if l.commands == nil {
		return noCommands{}
	}
	return l.commands
}

// errNoCommands is the answer to a steering command on a layer given no operations.
var errNoCommands = errors.New("this command is not available on this node")

type noCommands struct{}

func (noCommands) SetStream(context.Context, string, string, int) error { return errNoCommands }
func (noCommands) View(context.Context, string, string) error           { return errNoCommands }
func (noCommands) Decide(context.Context, string, string, string) error { return errNoCommands }
func (noCommands) Agents(context.Context) ([]command.Agent, error)      { return nil, errNoCommands }

// CommandAsOperator runs a /pao command from the operator at a channel that
// routes as the operator without an identity binding, such as the node's local
// channel. A command that addresses an agent or a tag is routed as the operator's
// message; any other command's answer is returned as reply, for the sender alone.
// ok is false when in.Text is not a command.
func (l *Layer) CommandAsOperator(ctx context.Context, in Inbound) (reply string, d *Delivery, ok bool, err error) {
	if !command.Is(in.Text) {
		return "", nil, false, nil
	}
	res := command.Run(ctx, l.commandOps(), command.Caller{Channel: in.Source, Operator: true}, in.Text)
	if res.Address == nil {
		return res.Reply, nil, true, nil
	}
	in.Target, in.Text = *res.Address, res.Text
	got, err := l.ReceiveAsOperator(ctx, in)
	if err != nil {
		return "", nil, true, err
	}
	return "", &got, true, nil
}
