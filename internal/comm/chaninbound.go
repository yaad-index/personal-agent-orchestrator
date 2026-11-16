package comm

import (
	"context"
	"errors"
	"fmt"

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
}

// ChannelResult is what the adapter does next. Reply is the fixed text to send
// the sender, empty to send nothing; Delivery is set when the message was routed.
type ChannelResult struct {
	Reply    string
	Delivery *Delivery
}

// SetIdentity gives the layer the identity store and the record of rejections that
// channel inbound needs. Call it before the layer starts receiving.
func (l *Layer) SetIdentity(id Identity, rec Recorder) { l.identity, l.recorder = id, rec }

func (l *Layer) reject(in ChannelInbound, kind, reason, payload string) error {
	if l.recorder == nil {
		return nil
	}
	if err := l.recorder.Record(in.Channel, in.Sender, kind, reason, payload); err != nil {
		return fmt.Errorf("record of a rejected inbound: %w", err)
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
// An error from recording is returned for the adapter to log; it is never text to
// send the sender.
func (l *Layer) ReceiveFromChannel(ctx context.Context, in ChannelInbound) (ChannelResult, error) {
	if l.identity == nil {
		return ChannelResult{}, ErrNoIdentity
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

	reply := in.Reply
	if reply == "" && in.ReplyNative != "" {
		if n, ok := l.channelIDs[in.Channel]; ok {
			reply, _ = l.composites.id(FormatComposite(n, in.ReplyNative))
		}
	}
	d, err := l.Receive(ctx, Inbound{
		Source:     in.Channel,
		Sender:     subject,
		Text:       in.Text,
		Target:     in.Target,
		Reply:      reply,
		ReplyQuote: in.ReplyQuote,
	})
	if err != nil {
		return ChannelResult{}, err
	}
	l.noteComposite(in.Channel, in.Native, d.Message.ID)
	return ChannelResult{Delivery: &d}, nil
}
