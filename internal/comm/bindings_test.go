package comm

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// atLevel renders an entry as "<text>@<level>", and shows nothing above max.
func atLevel(text string, max int) func(int) (core.Message, bool) {
	return func(level int) (core.Message, bool) {
		if level > max {
			return core.Message{}, false
		}
		return core.Message{Sender: "a", Kind: core.MessageContent, Text: text + "@" + strconv.Itoa(level)}, true
	}
}

func entry(t *testing.T, l *Layer, render func(int) (core.Message, bool)) {
	t.Helper()
	require.NoError(t, l.SendEntry(context.Background(), "a", 1, render, nil))
}

func bind(to string, level int, prefix string) Binding {
	kind, name, _ := cut(to)
	return Binding{Agent: "a", To: Dest{Kind: kind, Name: name}, Level: level, Prefix: prefix}
}

func cut(s string) (kind, name string, ok bool) {
	for i := range s {
		if s[i] == ':' {
			return s[:i], s[i+1:], true
		}
	}
	return "", s, false
}

func TestABindingFollowsTheAgentAtItsOwnLevelBesideTheOrigin(t *testing.T) {
	l, _, chat, local := originSetup(t)
	l.SetBindings([]Binding{bind("private:alice", 3, "")})
	fromChat(t, l, inGroup("-5", "op", "hi"))
	entry(t, l, atLevel("x", 9))
	assert.Equal(t, map[string][]string{"-5": {"x@1"}, "op": {"x@3"}}, chat.take(), "the origin at the agent's level, the binding at its own")
	assert.Empty(t, local.texts())
}

func TestABindingThatIsAlsoTheOriginGetsOneCopyTheBindings(t *testing.T) {
	l, _, chat, _ := originSetup(t)
	l.SetBindings([]Binding{bind("private:alice", 2, "TA:")})
	fromChat(t, l, privately("hi"))
	entry(t, l, atLevel("x", 9))
	assert.Equal(t, map[string][]string{"op": {"TA: x@2"}}, chat.take())
}

func TestOutputWithNoOriginStillGoesWideBesideTheBindingsWithoutACopyTwice(t *testing.T) {
	l, _, chat, local := originSetup(t)
	l.SetBindings([]Binding{bind("group:team", 2, ""), bind("channel:local", 3, "")})
	entry(t, l, atLevel("x", 9))
	assert.Equal(t, map[string][]string{"-5": {"x@2"}}, chat.take(), "the group shows the binding's copy only")
	assert.Equal(t, []string{"x@3"}, local.texts(), "and so does the local channel")
}

func TestAnAgentWithNoBindingsIsRoutedAsBefore(t *testing.T) {
	l, _, chat, local := originSetup(t)
	l.SetBindings([]Binding{{Agent: "b", To: Dest{Kind: DestChannel, Name: "local"}, Level: 1}})
	fromChat(t, l, privately("hi"))
	entry(t, l, atLevel("x", 9))
	assert.Equal(t, map[string][]string{"op": {"x@1"}}, chat.take())
	assert.Empty(t, local.texts(), "another agent's binding is not this one's")
}

func TestALevelThatShowsNothingOfAnEntrySendsNothingThere(t *testing.T) {
	l, _, chat, _ := originSetup(t)
	l.SetBindings([]Binding{bind("private:alice", 3, "")})
	fromChat(t, l, inGroup("-5", "op", "hi"))
	entry(t, l, func(level int) (core.Message, bool) {
		if level != 3 {
			return core.Message{}, false
		}
		return core.Message{Sender: "a", Kind: core.MessageContent, Text: "detail"}, true
	})
	assert.Equal(t, map[string][]string{"op": {"detail"}}, chat.take(), "the origin's level shows nothing of it; the binding's does")
}

func TestARenderingIsStoredOnceWhereverItGoes(t *testing.T) {
	l, _, chat, local := originSetup(t)
	l.SetBindings([]Binding{bind("channel:local", 1, "")})
	fromChat(t, l, privately("hi"))
	asked := map[int]int{}
	entry(t, l, func(level int) (core.Message, bool) {
		asked[level]++
		return core.Message{Sender: "a", Kind: core.MessageContent, Text: "same"}, true
	})
	assert.Equal(t, map[int]int{1: 1}, asked, "one rendering for the origin and the binding at the same level and no prefix")
	assert.Equal(t, map[string][]string{"op": {"same"}}, chat.take())
	assert.Equal(t, []string{"same"}, local.texts())
	_, err := l.store.Resolve("a", "same")
	assert.NoError(t, err, "one stored copy, so a quote of it is not ambiguous")
}

func TestAGroupBindingFollowsTheGroupByName(t *testing.T) {
	l, _, chat, _ := originSetup(t)
	l.SetBindings([]Binding{bind("group:team", 2, "")})
	fromChat(t, l, privately("hi"))
	entry(t, l, atLevel("x", 9))
	assert.Equal(t, map[string][]string{"op": {"x@1"}, "-5": {"x@2"}}, chat.take())

	l.SetGroups(map[string]map[string]Group{"chat": {"-7": {Name: "other", Policy: PolicyOperators}}})
	entry(t, l, atLevel("y", 9))
	assert.Equal(t, map[string][]string{"op": {"y@1"}}, chat.take(), "a group no longer admitted names no conversation")
}

func TestAPrivateBindingOnSeveralChannelsNeedsTheChannelNamed(t *testing.T) {
	l, _, chat, _ := originSetup(t)
	chat2 := &convChannel{name: "chat2", l: l, got: map[string][]string{}}
	l.AddChannel(chat2)
	l.identity.(*fakeIdentity).bound["chat2\x00op9"] = "alice"
	fromChat(t, l, inGroup("-5", "op", "hi"))

	l.SetBindings([]Binding{bind("private:alice", 2, "")})
	entry(t, l, atLevel("x", 9))
	assert.Equal(t, map[string][]string{"-5": {"x@1"}}, chat.take(), "ambiguous: no conversation")
	assert.Empty(t, chat2.take())

	l.SetBindings([]Binding{{Agent: "a", To: Dest{Kind: DestPrivate, Name: "alice", Channel: "chat2"}, Level: 2}})
	entry(t, l, atLevel("y", 9))
	assert.Equal(t, map[string][]string{"-5": {"y@1"}}, chat.take())
	assert.Equal(t, map[string][]string{"op9": {"y@2"}}, chat2.take())
}

func TestABindingIsHeldBackWhereAReaderMayNotSeeTheAgent(t *testing.T) {
	l, r, chat, _ := originSetup(t)
	r.agents["b"] = []string{"b"}
	l.SetBindings([]Binding{bind("group:team", 2, ""), bind("private:alice", 2, "")})
	l.SetOperatorAccess(func(p core.Profile) bool { return p.Name != "a" })
	entry(t, l, atLevel("x", 9))
	assert.Empty(t, chat.take())
}

func TestAPrivateBindingToASubjectWhoIsNotAnOperatorShowsNothing(t *testing.T) {
	l, _, chat, _ := originSetup(t)
	l.SetBindings([]Binding{bind("private:bob", 2, "")})
	entry(t, l, atLevel("x", 9))
	assert.Equal(t, map[string][]string{"-5": {"x@1"}}, chat.take(), "bob's conversation is not shown it")
}

func TestAPausedViewerHoldsTheDefaultStreamButNotTheBindings(t *testing.T) {
	l, _, chat, local := originSetup(t)
	l.SetBindings([]Binding{bind("channel:local", 1, "")})
	fromChat(t, l, privately("hi"))
	var held []core.Message
	require.NoError(t, l.SendEntry(context.Background(), "a", 1, atLevel("x", 9), func(m core.Message) bool { held = append(held, m); return true }))
	assert.Empty(t, chat.take(), "the default stream's copy waits")
	assert.Equal(t, []string{"x@1"}, local.texts(), "the binding's does not")
	require.Len(t, held, 1)

	_, err := l.Send(context.Background(), "a", held[0])
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"op": {"x@1"}}, chat.take(), "let go, it goes to the default stream only")
	assert.Equal(t, []string{"x@1"}, local.texts(), "not to the binding again")
}

func TestShownPutsTheLabelInPlaceOfTheName(t *testing.T) {
	m := core.Message{Sender: "a", Text: "hi"}
	assert.Equal(t, "[a]: hi", m.Shown())
	m.Label = "TA:"
	assert.Equal(t, "TA: hi", m.Shown())
	assert.Equal(t, "[a]: hi", m.Line(), "an agent reads the name, never the label")
}

func TestABindingToAChannelThatIsNotRegisteredSendsNothingAndFailsNothing(t *testing.T) {
	l, _, chat, local := originSetup(t)
	l.SetBindings([]Binding{bind("channel:gone", 2, ""), bind("channel:local", 2, "")})
	fromChat(t, l, privately("hi"))
	entry(t, l, atLevel("x", 9))
	assert.Equal(t, map[string][]string{"op": {"x@1"}}, chat.take())
	assert.Equal(t, []string{"x@2"}, local.texts())
}
