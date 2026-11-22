package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/builtin"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

// topicUpdate is a message written in topic thread of a group split into topics.
func topicUpdate(id, msg, from, chat, thread int, text string) map[string]any {
	u := groupUpdate(id, msg, from, chat, text)
	m := u["message"].(map[string]any)
	m["message_thread_id"], m["is_topic_message"] = thread, true
	return u
}

// thread is the topic a sent message named, "" for none.
func thread(m map[string]any) any {
	if m == nil {
		return "no such message"
	}
	if v, ok := m["message_thread_id"]; ok {
		return v
	}
	return ""
}

// TestATopicHasItsOwnAgentAndEverythingElseIsAsBefore runs ADR-0038 on a real
// node: a group split into topics, one topic named and given its own default
// agent. Plain text in that topic reaches its agent and the answer goes into the
// topic, unprefixed; the group's general topic, a topic that isn't named, a group
// reply carrying a thread id and the private chat all keep the channel's default
// and are answered where they were asked, with no topic named except a topic's.
func TestATopicHasItsOwnAgentAndEverythingElseIsAsBefore(t *testing.T) {
	svc := &service{}
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_TEST_BOT_TOKEN", "tok-1")
	const person, group = 4242, -700

	n := startNodeWith(t, map[string]string{"bee": "", "wasp": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_TOKEN",
			Options: map[string]any{"endpoint": ts.URL + "/bot{token}/{method}", "poll_timeout": 0},
			Groups: []config.Group{{ID: strconv.Itoa(group), Name: "team", Members: config.MembersOperators,
				Topics: []config.Topic{{ID: "42", Name: "games"}}}}}}
	}, func(o *Options) { o.Adapters = builtin.Table() })
	ctx := context.Background()

	require.NoError(t, n.client.AddSubject(ctx, "alice"))
	inv, err := n.client.Invite(ctx, "alice", time.Hour)
	require.NoError(t, err)
	svc.push(chatUpdate(1, 10, person, "/register "+inv.Secret))
	lineWith(t, svc, person, "registered.")
	require.NoError(t, n.client.SetOperator(ctx, "alice", true))

	_, err = n.client.SetDefault(ctx, "chat", "bee")
	require.NoError(t, err)
	ch, err := n.client.SetDefault(ctx, "group:games", "wasp")
	require.NoError(t, err)
	assert.Equal(t, local.DefaultChangeJSON{Channel: "group:games", Agent: "wasp"}, ch, "a named topic takes a default by its destination name")
	_, err = n.client.SetDefault(ctx, "group:nowhere", "wasp")
	assert.ErrorContains(t, err, "404: no chat channel or group")

	svc.push(topicUpdate(2, 20, person, group, 42, "in the topic"))
	out := lineWith(t, svc, group, "in the topic")
	assert.Equal(t, "echo: [alice]: in the topic", out, "the topic's own default answers, unprefixed")
	assert.Equal(t, "42", thread(svc.sentWith(group, "in the topic")), "into the topic it was asked in")

	svc.push(groupUpdate(3, 21, person, group, "in general"))
	out = lineWith(t, svc, group, "in general")
	// Unprefixed in the group's own conversation means bee answered: wasp is not
	// that conversation's default, so its output there would be named.
	assert.Equal(t, "echo: [alice]: in general", out, "the general topic has the channel's default")
	assert.Equal(t, "", thread(svc.sentWith(group, "in general")), "sent with no topic named")

	svc.push(topicUpdate(4, 22, person, group, 77, "unnamed topic"))
	assert.Equal(t, "echo: [alice]: unnamed topic", lineWith(t, svc, group, "unnamed topic"), "with the group's default, here the channel's")
	assert.Equal(t, "77", thread(svc.sentWith(group, "unnamed topic")), "a topic that isn't named is answered in it too")

	reply := groupUpdate(5, 23, person, group, "a reply with a thread")
	reply["message"].(map[string]any)["message_thread_id"] = 21
	svc.push(reply)
	lineWith(t, svc, group, "a reply with a thread")
	assert.Equal(t, "", thread(svc.sentWith(group, "a reply with a thread")), "a thread id without the topic mark makes no topic")

	svc.push(chatUpdate(6, 24, person, "in private"))
	out = lineWith(t, svc, person, "in private")
	assert.Equal(t, "echo: [alice]: in private", out, "the private chat keeps the channel's default")
	assert.Equal(t, "", thread(svc.sentWith(person, "in private")))
	assert.Nil(t, svc.sentWith(group, "in private"))
}
