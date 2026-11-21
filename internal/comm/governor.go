package comm

import (
	"context"
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
	ReplyNoGovernor = "this node has no governor"
)

// governorUsage is the answer to a request with nothing in it.
func governorUsage(prefix string) string {
	return "usage: " + prefix + " <what you want done, in your own words>"
}

// SetGovernorReset gives the layer how to end the governor's session, so the next
// request starts it on a fresh context (ADR-0031 point 6). Call it before the
// layer starts receiving.
func (l *Layer) SetGovernorReset(reset func(context.Context) error) { l.governorReset = reset }

// toGovernor turns subject's request to the governor into the message it is
// sent: to the governor by name, the request as its text. reply is what to answer
// instead when it does not go. A request from a different subject than the one
// before it first ends the governor's session (serve).
func (l *Layer) toGovernor(ctx context.Context, text, subject string) (target core.Target, request, reply string) {
	request, _ = l.governorRequest(text)
	switch {
	case l.governor == "":
		return core.Target{}, "", ReplyNoGovernor
	case request == "":
		return core.Target{}, "", governorUsage(l.governorPrefix)
	}
	if err := l.serve(ctx, subject); err != nil {
		return core.Target{}, "", "the governor could not be started on a fresh context: " + err.Error()
	}
	return core.Target{Kind: core.ByName, Value: l.governor}, request, ""
}

// governorTargets is where what the governor says goes: back to the conversation
// the request that started its current turn came from (ADR-0034 point 4), under
// the narrowest-reader rule like any reply, and nowhere else. With no request in
// hand it goes nowhere.
func (l *Layer) governorTargets(agent string) []target {
	l.mu.RLock()
	t, ok := l.origins[agent]
	l.mu.RUnlock()
	if !ok || t.requester == "" {
		return nil
	}
	return []target{{kind: toOrigin, at: t.at, agent: agent}}
}

// serve makes the governor's context subject's (ADR-0031 point 6): when the
// subject differs from the one the governor last served, or it has served no one
// since the node started, its session is ended first, and the request then starts
// it afresh. One requester's request, and what was read for it, is so never in
// the context another's is answered in. A reset that fails leaves the subject
// unchanged, so the next request tries again.
func (l *Layer) serve(ctx context.Context, subject string) error {
	l.governorMu.Lock()
	defer l.governorMu.Unlock()
	// A sender is never empty, so the empty subject a node starts with is no one.
	if l.governorSubject == subject {
		return nil
	}
	if l.governorReset != nil {
		if err := l.governorReset(ctx); err != nil {
			return err
		}
	}
	l.governorSubject = subject
	return nil
}

// GovernorRequester is the subject the governor is serving, and the channel the
// request came from: who sent the message that started its current turn. ok is
// false when the node has no governor or the governor is serving no request,
// such as before its first or in a turn another agent started.
func (l *Layer) GovernorRequester() (subject, channel string, ok bool) {
	if l.governor == "" {
		return "", "", false
	}
	l.mu.RLock()
	t, has := l.origins[l.governor]
	l.mu.RUnlock()
	if !has || t.requester == "" {
		return "", "", false
	}
	return t.requester, t.at.channel, true
}
