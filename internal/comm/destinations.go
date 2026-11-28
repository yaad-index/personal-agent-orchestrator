package comm

import (
	"log/slog"
	"sort"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// Destinations returns the conversations on a channel that m is shown in, by the
// platform's id for each: the private conversation of each operator bound on the
// channel (whose id is the operator's sender id), and the channel's admitted
// groups.
//
// An agent's output goes where its route says (routed). Anything else is shown as
// wide says. Either way it is shown only in the channel's one conversation
// (inScope).
func (l *Layer) Destinations(channel string, m core.Message) []string {
	if l.identity == nil {
		return nil
	}
	var out []string
	if r, ok := l.routes.Load(m.ID); ok {
		out = l.routed(channel, m, r.(outRoute))
	} else {
		out = l.wide(channel, m)
	}
	out = l.inScope(channel, m, out)
	if len(out) == 0 {
		l.warnUnshown(channel, m)
	}
	return out
}

// wide returns every conversation on a channel that may show m.
//
// A conversation gets m only if every one of its readers may see it (ADR-0028
// point 5). A private conversation's one reader is its operator; an admitted
// group's readers are its members under its policy. A gate question goes to the
// private conversations only, as before any group was admitted (ADR-0029 point 4).
// Anything else goes to the channel's admitted groups that may show it, or, where
// there is none, to the private conversations.
func (l *Layer) wide(channel string, m core.Message) []string {
	if isQuestion(m) {
		return l.privately(channel, m)
	}
	var groups []string
	for id, g := range l.groups[channel] {
		// A named topic is a conversation for replies and bindings, not one of
		// the channel's conversations as a whole (ADR-0038 point 10).
		if id != "" && !g.Topic && l.readersMay(g.Policy, m) {
			groups = append(groups, id)
		}
	}
	sort.Strings(groups)
	if len(groups) == 0 {
		return l.privately(channel, m)
	}
	return groups
}

// privately returns the private conversation of each operator bound on the
// channel, if an operator may see m.
func (l *Layer) privately(channel string, m core.Message) []string {
	if !l.readersMay(PolicyOperators, m) {
		return nil
	}
	return l.identity.Operators(channel)
}

// isQuestion reports whether m is a gate question: pao's own message offering
// actions (ADR-0027). It is the one message that cannot wait.
func isQuestion(m core.Message) bool {
	return m.Kind == core.MessageSystemEvent && len(m.Actions) > 0
}

// readersMay reports whether every reader a policy admits may see m. A policy the
// layer does not know shows nothing.
func (l *Layer) readersMay(policy string, m core.Message) bool {
	switch policy {
	case PolicyOperators:
		return l.operatorMay(m)
	}
	return false
}

// operatorMay reports whether a subject routed as the operator may see m. pao's
// own messages are the operator's, and a gate question is the operator's to
// answer (ADR-0023 point 7). An agent's output is the operator's to see only if
// the operator may reach the agent (ADR-0012); an agent pao cannot name is not.
func (l *Layer) operatorMay(m core.Message) bool {
	if m.Kind == core.MessageSystemEvent {
		return true
	}
	if l.operatorAllow == nil {
		return true
	}
	ps, err := l.router.Resolve(core.Target{Kind: core.ByName, Value: m.Sender})
	if err != nil || len(ps) == 0 {
		return false
	}
	for _, p := range ps {
		if !l.operatorAllow(p) {
			return false
		}
	}
	return true
}

// warnUnshown logs, once per channel and sender, that a message was shown in no
// conversation, so output held back by access is seen in the log rather than
// lost without a trace.
func (l *Layer) warnUnshown(channel string, m core.Message) {
	if _, seen := l.unshown.LoadOrStore(channel+"\x00"+m.Sender, true); seen {
		return
	}
	slog.Warn("channel output shown in no conversation: no reader may see it, or no one is bound", "channel", channel, "sender", m.Sender)
}
