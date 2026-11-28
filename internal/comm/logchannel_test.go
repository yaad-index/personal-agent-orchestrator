package comm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// logEntry is agent a's transcript entry, shown as text at every level.
func logEntry(t *testing.T, l *Layer, text string) {
	t.Helper()
	require.NoError(t, l.SendEntry(context.Background(), "a", 1, func(int) (core.Message, bool) {
		return core.Message{Sender: "a", Kind: core.MessageContent, Text: text}, true
	}, nil))
}

// logSetup is permissionSetup with alice's group team as agent a's log channel.
func logSetup(t *testing.T) (*Layer, *fakeIdentity, map[string]*convChannel, *fakeRecorder) {
	t.Helper()
	l, id, chs := permissionSetup(t)
	l.SetLogChannels(func() map[string]string { return map[string]string{"team": "a"} })
	return l, id, chs, l.recorder.(*fakeRecorder)
}

func TestALogChannelIsShownAllOfItsAgentsOutputAndWhatItIsSent(t *testing.T) {
	l, _, chs, _ := logSetup(t)
	require.NotNil(t, receive(t, l, ChannelInbound{Channel: "chat", Sender: "op", Text: "hi", Target: byName("a"), Raw: "hi"}).Delivery)
	render := func(level int) (core.Message, bool) {
		text := "the answer"
		if level == core.LevelLog {
			text = "the answer, with every tool call before it"
		}
		return core.Message{Sender: "a", Kind: core.MessageContent, Text: text}, true
	}
	require.NoError(t, l.SendEntry(context.Background(), "a", 1, render, nil))
	got := taken(chs)
	assert.Equal(t, []string{"hi", "the answer, with every tool call before it"}, got["team"]["-5"], "what alice sent, and the answer at the log level")
	assert.Equal(t, []string{"the answer"}, got["chat"]["op"], "the interaction channel at the agent's own level")
}

func TestALogChannelTakesNoInputFromAnyone(t *testing.T) {
	l, _, chs, rc := logSetup(t)
	for _, in := range []ChannelInbound{
		{Channel: "team", Sender: "op", Group: true, Conversation: "-5", Text: "the owner, in her log channel", Raw: "the owner, in her log channel", Target: byName("a")},
		{Channel: "team", Sender: "op", Group: true, Conversation: "-5", Text: "/pao log a pause", Raw: "/pao log a pause", Press: true},
	} {
		assert.Equal(t, ChannelResult{}, receive(t, l, in), in.Text)
	}
	assert.Equal(t, []rec{
		{"team", "op", RecordMessage, "a log channel takes no input", "the owner, in her log channel"},
		{"team", "op", RecordMessage, "a log channel takes no input", "/pao log a pause"},
	}, rc.got)
	logEntry(t, l, "still logged")
	assert.Equal(t, []string{"still logged"}, taken(chs)["team"]["-5"], "and the command typed there did nothing")
}

func TestAPausedLogChannelIsShownNothingUntilResumed(t *testing.T) {
	l, _, chs, _ := logSetup(t)
	require.NoError(t, l.PauseLog("a", true))
	logEntry(t, l, "while paused")
	assert.NotContains(t, taken(chs), "team")
	require.NoError(t, l.PauseLog("a", false))
	logEntry(t, l, "after")
	assert.Equal(t, []string{"after"}, taken(chs)["team"]["-5"], "what was said while paused is not sent later")
	assert.ErrorContains(t, l.PauseLog("b", true), `agent "b" has no log channel`)
}

func TestALogChannelHasNoAgentAndPaoAsksNothingThere(t *testing.T) {
	l, id, chs, _ := logSetup(t)
	l.SetDefaults(mapDefaults{"chat": "a", "team": "a", "bobdm": "b"})
	_, ok := l.DefaultAgentIn("team", "-5")
	assert.False(t, ok, "a default stored for it has no effect")

	id.permission["alice"] = "team"
	_, err := l.NotifyAgent(context.Background(), "a", question)
	require.NoError(t, err)
	got := taken(chs)
	assert.Equal(t, map[string][]string{"op": {"allow?"}}, got["chat"], "a log channel set as her permission channel is not used: a press there would not be taken")
	assert.NotContains(t, got, "team")

	l.SetLogChannels(func() map[string]string { return map[string]string{"chat": "a"} })
	_, err = l.NotifyOperators(context.Background(), question)
	require.NoError(t, err)
	got = taken(chs)
	assert.NotContains(t, got, "chat", "nor is any private conversation reached through one")
	assert.Equal(t, map[string][]string{"op": {"allow?"}}, got["team"])
}

func TestAQuestionAboutAnAgentOfNoUserIsNotPutThroughALogChannel(t *testing.T) {
	l, _, chs, _ := logSetup(t)
	l.SetLogChannels(func() map[string]string { return map[string]string{"chat": "a"} })
	_, err := l.NotifyAgent(context.Background(), "c", question)
	require.NoError(t, err)
	got := taken(chs)
	assert.NotContains(t, got, "chat", "a press on a message shown by way of a log channel would be placed there, and not taken")
	assert.Equal(t, map[string][]string{"op": {"allow?"}}, got["team"], "the operator's private conversation, through another of her channels")
}
