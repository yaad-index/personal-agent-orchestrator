package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/builtin"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

// service is a stand-in for the chat service the bot adapter talks to.
type service struct {
	mu      sync.Mutex
	pending []map[string]any
	sent    []map[string]any
	nextID  int
}

func (s *service) push(update map[string]any) {
	s.mu.Lock()
	s.pending = append(s.pending, update)
	s.mu.Unlock()
}

func (s *service) texts(chat float64) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.sent {
		if m["chat_id"].(float64) == chat {
			out = append(out, m["text"].(string))
		}
	}
	return out
}

func (s *service) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body := map[string]any{}
	if err := r.ParseMultipartForm(1 << 20); err == nil {
		for k, v := range r.MultipartForm.Value {
			body[k] = v[0]
			if n, err := strconv.ParseFloat(v[0], 64); err == nil && k == "chat_id" {
				body[k] = n
			}
		}
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/getUpdates"):
		out := s.pending
		s.pending = nil
		raw, _ := json.Marshal(out)
		if len(out) == 0 {
			s.mu.Unlock()
			time.Sleep(15 * time.Millisecond)
			s.mu.Lock()
		}
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":%s}`, raw)
	case strings.HasSuffix(r.URL.Path, "/sendMessage"):
		s.sent = append(s.sent, body)
		s.nextID++
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, 5000+s.nextID)
	default:
		http.NotFound(w, r)
	}
}

func chatUpdate(id, msg, from int, text string) map[string]any {
	return map[string]any{"update_id": id, "message": map[string]any{
		"message_id": msg, "text": text,
		"from": map[string]any{"id": from, "is_bot": false},
		"chat": map[string]any{"id": from, "type": "private"},
	}}
}

// TestAHumanReachesAnAgentThroughTheBotAndHearsBack runs the whole chain on a real
// node: a stranger is recorded and told nothing, an invitation registers a person, a
// registered person who is not flagged is refused, a flagged one reaches the agent
// attributed by subject, and the agent's output comes back to that person's chat.
func TestAHumanReachesAnAgentThroughTheBotAndHearsBack(t *testing.T) {
	svc := &service{}
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_TEST_BOT_TOKEN", "tok-1")

	n := startNodeWith(t, map[string]string{"bee": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_TOKEN",
			Options: map[string]any{"endpoint": ts.URL + "/bot{token}/{method}", "poll_timeout": 0}}}
	}, func(o *Options) { o.Adapters = builtin.Table() })
	ctx := context.Background()
	const person = 4242

	svc.push(chatUpdate(1, 10, person, "/pao bee hello"))
	require.Eventually(t, func() bool {
		rej, err := n.client.Rejected(ctx)
		return err == nil && len(rej) == 1
	}, 5*time.Second, 20*time.Millisecond, "a stranger's message is recorded")
	assert.Empty(t, svc.texts(person), "and answered with nothing")

	require.NoError(t, n.client.AddSubject(ctx, "alice"))
	inv, err := n.client.Invite(ctx, "alice", time.Hour)
	require.NoError(t, err)
	svc.push(chatUpdate(2, 11, person, "/register "+inv.Secret))
	require.Eventually(t, func() bool { return len(svc.texts(person)) == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, "registered.", svc.texts(person)[0])

	svc.push(chatUpdate(3, 12, person, "/pao bee hello"))
	require.Eventually(t, func() bool { return len(svc.texts(person)) == 2 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, "no access.", svc.texts(person)[1], "registered is not the same as allowed")

	require.NoError(t, n.client.SetOperator(ctx, "alice", true))
	svc.push(chatUpdate(4, 13, person, "/pao agent bee hello again"))
	require.Eventually(t, func() bool {
		for _, s := range svc.texts(person) {
			if strings.Contains(s, "echo:") {
				return true
			}
		}
		return false
	}, 10*time.Second, 20*time.Millisecond, "the agent's output reaches the person's chat")
	var out string
	for _, s := range svc.texts(person) {
		if strings.Contains(s, "echo:") {
			out = s
		}
	}
	assert.True(t, strings.HasPrefix(out, "[bee]: "), "pao's output reads as a line from the agent: %q", out)
	assert.Contains(t, out, "hello again")
	assert.Contains(t, out, "[alice]", "the agent saw the subject's name, not the external id")
	assert.NotContains(t, out, "4242")

	assert.Empty(t, os.Getenv("PAO_TEST_BOT_TOKEN"), "the credential is not in the environment agents inherit")
}

// lineWith waits for a line sent to the chat that contains want, and returns it.
func lineWith(t *testing.T, svc *service, chat float64, want string) string {
	t.Helper()
	var out string
	require.Eventually(t, func() bool {
		for _, s := range svc.texts(chat) {
			if strings.Contains(s, want) {
				out = s
				return true
			}
		}
		return false
	}, 10*time.Second, 20*time.Millisecond, "a line containing %q", want)
	return out
}

func TestTheChannelsDefaultAgentIsReloadedSwitchedAtRuntimeAndSpeaksUnprefixed(t *testing.T) {
	svc := &service{}
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	t.Setenv("PAO_TEST_BOT_TOKEN", "tok-1")

	n := startNodeWith(t, map[string]string{"bee": "", "wasp": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Channels = []config.Channel{{ID: 1, Name: "chat", Kind: "bot", CredentialEnv: "PAO_TEST_BOT_TOKEN",
			Options: map[string]any{"endpoint": ts.URL + "/bot{token}/{method}", "poll_timeout": 0}}}
		// What a node that ran before left behind.
		require.NoError(t, os.MkdirAll(cfg.Orchestrator.StateDir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(cfg.Orchestrator.StateDir, defaultsFile),
			[]byte(`{"version":3,"defaults":{"chat":"bee"}}`), 0o600))
	}, func(o *Options) { o.Adapters = builtin.Table() })
	ctx := context.Background()
	const person = 4242

	d, err := n.client.Defaults(ctx)
	require.NoError(t, err)
	assert.Equal(t, local.DefaultsJSON{Version: 3, Defaults: []local.DefaultJSON{{Channel: "chat", Agent: "bee"}}}, d, "reloaded at start")

	require.NoError(t, n.client.AddSubject(ctx, "alice"))
	inv, err := n.client.Invite(ctx, "alice", time.Hour)
	require.NoError(t, err)
	svc.push(chatUpdate(1, 10, person, "/register "+inv.Secret))
	lineWith(t, svc, person, "registered.")
	require.NoError(t, n.client.SetOperator(ctx, "alice", true))

	svc.push(chatUpdate(2, 11, person, "first unaddressed"))
	out := lineWith(t, svc, person, "first unaddressed")
	assert.True(t, strings.HasPrefix(out, "echo:"), "the default answers with no name before it: %q", out)

	ch, err := n.client.SetDefault(ctx, "chat", "wasp")
	require.NoError(t, err)
	assert.Equal(t, local.DefaultChangeJSON{Channel: "chat", Agent: "wasp", Previous: "bee"}, ch)
	svc.push(chatUpdate(3, 12, person, "second unaddressed"))
	out = lineWith(t, svc, person, "second unaddressed")
	assert.True(t, strings.HasPrefix(out, "echo:"), "the new default answers, unprefixed: %q", out)

	svc.push(chatUpdate(4, 13, person, "/pao bee third addressed"))
	out = lineWith(t, svc, person, "third addressed")
	assert.True(t, strings.HasPrefix(out, "[bee]: "), "an agent that is not the default is named: %q", out)

	// A leading @name is no address any more (ADR-0026 point 9): the message goes
	// where the sender last addressed, as the text it was sent as.
	svc.push(chatUpdate(5, 14, person, "@wasp fourth"))
	out = lineWith(t, svc, person, "@wasp fourth")
	assert.Equal(t, "[bee]: echo: [alice]: @wasp fourth", out)

	// A command is answered to the sender and reaches no agent (ADR-0026 points 1 and 5).
	svc.push(chatUpdate(6, 15, person, "/pao help"))
	out = lineWith(t, svc, person, "pao commands:")
	assert.Contains(t, out, "/pao stream <agent>")
	svc.push(chatUpdate(7, 16, person, "/pao stream bee"))
	lineWith(t, svc, person, "bee's output goes to chat only")
	svc.push(chatUpdate(8, 17, person, "/pao stream nobody"))
	lineWith(t, svc, person, `no agent "nobody"`)
	for _, s := range svc.texts(person) {
		assert.NotContains(t, s, "echo: [alice]: /pao", "no command reached an agent")
	}

	_, err = n.client.SetDefault(ctx, "local", "bee")
	assert.ErrorContains(t, err, "404: no chat channel", "the local channel takes no chat inbound")
	_, err = n.client.SetDefault(ctx, "chat", "nobody")
	assert.ErrorContains(t, err, `404: no agent "nobody"`)

	ch, err = n.client.ClearDefault(ctx, "chat")
	require.NoError(t, err)
	assert.Equal(t, local.DefaultChangeJSON{Channel: "chat", Previous: "wasp"}, ch)
	d, err = n.client.Defaults(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(5), d.Version)
	assert.Empty(t, d.Defaults)
}
