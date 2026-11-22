package bot

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// inTopic is a message written in topic thread of a group split into topics.
func inTopic(id, msg, from, chat int64, thread int, text string) update {
	u := grp(id, msg, from, chat, text)
	u.Message.Thread, u.Message.Topic = thread, true
	return u
}

// threaded is a message the service gives a thread id without marking it as
// written in a topic, such as a reply inside a group without topics.
func threaded(u update, thread int) update {
	u.Message.Thread = thread
	return u
}

// sends is what was sent: the chat, the thread named ("" for none) and the text.
func (s *server) sends() [][3]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][3]string
	for _, m := range s.sent {
		out = append(out, [3]string{m["chat_id"], m["message_thread_id"], m["text"]})
	}
	return out
}

func TestATopicMessageIsItsTopicsConversationAndItsAnswerGoesIntoTheTopic(t *testing.T) {
	s, h := newServer(t), &handler{res: comm.ChannelResult{Reply: "pao says"}}
	s.queue(inTopic(1, 1, 900, -500, 42, "/pao help"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(s.sends()) == 1 }, "the answer")
	in := h.inbound()
	require.Len(t, in, 1)
	assert.True(t, in[0].Group)
	assert.Equal(t, "-500/42", in[0].Conversation)
	assert.Equal(t, "-500:1", in[0].Native, "a message's own id is the chat's, topic or not")
	assert.Equal(t, [][3]string{{"-500", "42", "pao says"}}, s.sends())
}

// The invariants: what is not a topic's is handled exactly as before topics.
func TestWhatIsNotATopicsIsTheConversationItWasBefore(t *testing.T) {
	s, h := newServer(t), &handler{res: comm.ChannelResult{Reply: "ok"}}
	general := grp(2, 2, 900, -500, "general") // the general topic: no thread, no mark
	reply := threaded(grp(3, 3, 900, -600, "a reply"), 7)
	reply.Message.ReplyTo = &struct {
		ID int64 `json:"message_id"`
	}{ID: 7}
	private := threaded(priv(4, 4, 900, "private"), 9)
	private.Message.Topic = true // a private chat has no topics, whatever it says
	s.queue(grp(1, 1, 900, -700, "plain group"), general, reply, private)
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(s.sends()) == 4 }, "four answers")
	in := h.inbound()
	require.Len(t, in, 4)
	assert.Equal(t, "-700", in[0].Conversation, "a group without topics")
	assert.Equal(t, "-500", in[1].Conversation, "the general topic is the group's own conversation")
	assert.Equal(t, "-600", in[2].Conversation, "a thread id without the topic mark makes no topic")
	assert.Equal(t, "-600:7", in[2].ReplyNative, "and the reply is still read")
	assert.False(t, in[3].Group, "a private chat")
	assert.Empty(t, in[3].Conversation)
	assert.Equal(t, [][3]string{{"-700", "", "ok"}, {"-500", "", "ok"}, {"-600", "", "ok"}, {"900", "", "ok"}}, s.sends(),
		"each answered where it was asked, with no thread named")
}

func TestOutputIsSentIntoATopicOnlyWhereTheLayerNamesOne(t *testing.T) {
	s, h := newServer(t), &handler{recipients: []string{"-500/42", "-500", "900", "-500/x"}}
	b, _ := start(t, s, h, nil)
	err := b.Send(context.Background(), core.Message{Sender: "pao", Text: "hello"})
	require.Error(t, err, "a destination that names no topic by its id")
	assert.Contains(t, err.Error(), `"-500/x"`)
	assert.Equal(t, [][3]string{{"-500", "42", "[pao]: hello"}, {"-500", "", "[pao]: hello"}, {"900", "", "[pao]: hello"}}, s.sends())
}

func TestAStickyAddressIsPerTopic(t *testing.T) {
	s, h := newServer(t), &handler{res: comm.ChannelResult{Delivery: &comm.Delivery{}}}
	s.queue(inTopic(1, 1, 900, -500, 42, "/pao agent bee one"), inTopic(2, 2, 900, -500, 42, "two"),
		inTopic(3, 3, 900, -500, 43, "three"), grp(4, 4, 900, -500, "four"), priv(5, 5, 900, "five"))
	start(t, s, h, nil)
	waitFor(t, func() bool { return len(h.inbound()) == 5 }, "five")
	in := h.inbound()
	assert.Equal(t, core.Target{Kind: core.ByName, Value: "bee"}, in[1].Target, "kept in the topic it was made in")
	assert.Zero(t, in[2].Target, "not in another topic")
	assert.Zero(t, in[3].Target, "nor in the group's own conversation")
	assert.Zero(t, in[4].Target, "nor privately")
}

func TestAPressInATopicIsThatTopicsAndOnlyThere(t *testing.T) {
	s, h := newServer(t), &handler{}
	start(t, s, h, nil)
	topic := press(1, "cb1", 42, -500, "supergroup", 101, "/pao allow q1")
	topic.Callback.Message.Thread, topic.Callback.Message.Topic = 7, true
	plain := press(2, "cb2", 42, -600, "supergroup", 102, "/pao allow q1")
	plain.Callback.Message.Thread = 7 // a thread id without the topic mark
	s.queue(topic, plain)
	waitFor(t, func() bool { return len(s.snapshot(&s.answers)) == 2 }, "both answered")
	in := h.inbound()
	require.Len(t, in, 2)
	assert.Equal(t, "-500/7", in[0].Conversation)
	assert.Equal(t, native(-500, 101), in[0].Pressed, "the pressed message is named by its chat")
	assert.Equal(t, "-600", in[1].Conversation)
}

func TestAConversationsDefaultSpeaksUnprefixedThereAndNamedElsewhere(t *testing.T) {
	s := newServer(t)
	h := &handler{recipients: []string{"-500/42", "-500"}, defaults: map[string]string{"chat/-500/42": "rules", "chat": "other"}}
	b, _ := start(t, s, h, nil)
	require.NoError(t, b.Send(context.Background(), core.Message{Sender: "rules", Text: "answer"}))
	assert.Equal(t, [][3]string{{"-500", "42", "answer"}, {"-500", "", "[rules]: answer"}}, s.sends())
}

func TestATopicsAddressReadsBackAsTheLayerWroteIt(t *testing.T) {
	for _, a := range []addr{{chat: -500}, {chat: -500, topic: 42}, {chat: 900}} {
		got, err := parseAddr(a.String())
		require.NoError(t, err)
		assert.Equal(t, a, got)
	}
	for _, bad := range []string{"x", "-500/x", "-500/0", "-500/-1", "/42"} {
		_, err := parseAddr(bad)
		assert.Error(t, err, bad)
	}
}
