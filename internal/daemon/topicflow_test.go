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

// TestAChannelThatIsATopicServesThatTopicAlone runs ADR-0038 on a real node,
// on a channel that is one named topic of a group with its own default agent:
// plain text in that topic reaches its agent and the answer goes into the topic,
// unprefixed. The group's general topic, another topic, a group reply carrying a
// thread id and the private chat are not the channel's: none reaches an agent,
// and nothing is said in any of them.
func TestAChannelThatIsATopicServesThatTopicAlone(t *testing.T) {
	svc := &service{}
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_TEST_BOT_TOKEN", "tok-1")
	const person, group = 4242, -700

	n := startNodeWith(t, map[string]string{"bee": "", "wasp": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Providers = []config.Provider{botProvider("chat", "PAO_TEST_BOT_TOKEN", ts.URL)}
		cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Provider: "chat", Owner: "alice", Target: strconv.Itoa(group) + "/42",
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

	svc.push(groupUpdate(3, 21, person, group, "in general"))
	svc.push(topicUpdate(4, 22, person, group, 77, "another topic"))
	reply := groupUpdate(5, 23, person, group, "a reply with a thread")
	reply["message"].(map[string]any)["message_thread_id"] = 42
	svc.push(reply)
	svc.push(chatUpdate(6, 24, person, "in private"))

	svc.push(topicUpdate(7, 25, person, group, 42, "in the topic"))
	out := lineWith(t, svc, group, "in the topic")
	assert.Equal(t, "echo: [alice]: in the topic", out, "the topic's own default answers, unprefixed")
	assert.Equal(t, "42", thread(svc.sentWith(group, "in the topic")), "into the topic it was asked in")

	for _, text := range []string{"in general", "another topic", "a reply with a thread"} {
		assert.Nil(t, svc.sentWith(group, text), text)
	}
	assert.Nil(t, svc.sentWith(person, "in private"))
	rej, err := n.client.Rejected(ctx)
	require.NoError(t, err)
	require.Len(t, rej, 1, "one sender, recorded once with its latest")
	assert.Equal(t, "no channel is this conversation: private", rej[0].Reason, "the provider carries no channel there")
	assert.Equal(t, 5, rej[0].Count, "the registration is recorded, and each of the four after it")
}
