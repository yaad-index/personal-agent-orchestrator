package comm

import (
	"strings"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// DefaultGovernorPrefix is the first word of a request to the governor (ADR-0030)
// where the node names no other. Like /pao it is pao's: what follows it goes to
// the governor alone, never to another agent.
const DefaultGovernorPrefix = "/gpao"

// SetGovernor names the node's governor, and the prefix of a request to it; empty
// agent names none, and an empty prefix is the default. Call it before the layer
// starts receiving.
func (l *Layer) SetGovernor(agent, prefix string) {
	if prefix == "" {
		prefix = DefaultGovernorPrefix
	}
	l.governor, l.governorPrefix = agent, prefix
}

// Governor is the node's governor, by name; empty when it has none.
func (l *Layer) Governor() string { return l.governor }

// governorRequest reports whether text is a request to the governor, and returns
// the request: the text after the prefix, which may also carry the bot's own name
// after an "@", as a chat client writes it in a group.
func (l *Layer) governorRequest(text string) (string, bool) {
	prefix := l.governorPrefix
	if prefix == "" {
		prefix = DefaultGovernorPrefix
	}
	text = strings.TrimSpace(text)
	head, rest := text, ""
	if i := strings.IndexAny(text, " \t\n"); i >= 0 {
		head, rest = text[:i], text[i+1:]
	}
	if head != prefix && !strings.HasPrefix(head, prefix+"@") {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// Replies to a request to the governor that does not reach it.
const (
	ReplyNoGovernor   = "this node has no governor"
	ReplyGovernorHeld = "the governor is serving another operator on this node"
)

// governorUsage is the answer to a request with nothing in it.
func governorUsage(prefix string) string {
	return "usage: " + prefix + " <what you want done, in your own words>"
}

// toGovernor turns subject's request to the governor into the message it is
// sent: to the governor by name, the request as its text. reply is what to answer
// instead when it does not go. The governor serves one subject while the node
// runs (governorHolder): a request from any other is refused.
func (l *Layer) toGovernor(text, subject string) (target core.Target, request, reply string) {
	request, _ = l.governorRequest(text)
	switch {
	case l.governor == "":
		return core.Target{}, "", ReplyNoGovernor
	case request == "":
		return core.Target{}, "", governorUsage(l.governorPrefix)
	case !l.holdGovernor(subject):
		return core.Target{}, "", ReplyGovernorHeld
	}
	return core.Target{Kind: core.ByName, Value: l.governor}, request, ""
}

// governorTargets is where what the governor says goes: to the subject who sent
// the request that started its current turn, alone and privately, wherever the
// request was sent. With no such turn it goes nowhere.
func (l *Layer) governorTargets(agent string) []target {
	l.mu.RLock()
	t, ok := l.origins[agent]
	l.mu.RUnlock()
	if !ok || t.requester == "" {
		return nil
	}
	return l.subjectTargets(t.at.channel, t.at.conversation == "", t.requester, agent)
}

// holdGovernor reports whether the governor serves subject: it does when no one
// holds it yet, and subject then holds it, or when subject already does.
func (l *Layer) holdGovernor(subject string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.governorHolder == "" {
		l.governorHolder = subject
	}
	return l.governorHolder == subject
}
