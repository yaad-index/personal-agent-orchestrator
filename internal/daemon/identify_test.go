package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/adapter"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/builtin"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestACodeIsUsedOnceForTheTargetItWasSentTo(t *testing.T) {
	clk := &clock{time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)}
	c := newIdentifyCodes(clk.now)
	at := where{"tg", "private", "alice"}
	code, exp, err := c.issue("dm", at)
	require.NoError(t, err)
	assert.Regexp(t, `^[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}$`, code)
	assert.Equal(t, clk.t.Add(identifyTTL), exp)

	assert.ErrorContains(t, c.redeem("dm", at, ""), "needs its target identified first")
	assert.ErrorContains(t, c.redeem("other", at, code), "no code is waiting")
	assert.ErrorContains(t, c.redeem("dm", where{"tg", "-5", "alice"}, code), "sent to another target")
	assert.ErrorContains(t, c.redeem("dm", where{"tg", "private", "bob"}, code), "another target, owner or provider")

	typed := " " + string(code[:4]) + string(code[5:]) + " "
	require.NoError(t, c.redeem("dm", at, typed), "typed without its dash, with spaces, is the code")
	assert.ErrorContains(t, c.redeem("dm", at, code), "no code is waiting", "used once")
}

func TestACodeExpiresAndTooManyWrongOnesEndIt(t *testing.T) {
	clk := &clock{time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)}
	c := newIdentifyCodes(clk.now)
	at := where{"tg", "private", "alice"}

	code, _, err := c.issue("dm", at)
	require.NoError(t, err)
	clk.t = clk.t.Add(identifyTTL + time.Second)
	assert.ErrorContains(t, c.redeem("dm", at, code), "expired")
	assert.ErrorContains(t, c.redeem("dm", at, code), "no code is waiting", "an expired code is gone")

	code, _, err = c.issue("dm", at)
	require.NoError(t, err)
	for i := 1; i < identifyTries; i++ {
		assert.ErrorContains(t, c.redeem("dm", at, "AAAA-AAAA"), "not the code", "wrong guess %d", i)
	}
	assert.ErrorContains(t, c.redeem("dm", at, "AAAA-AAAA"), "too many were wrong")
	assert.ErrorContains(t, c.redeem("dm", at, code), "no code is waiting", "the right code no longer works")

	first, _, err := c.issue("dm", at)
	require.NoError(t, err)
	second, _, err := c.issue("dm", at)
	require.NoError(t, err)
	if first != second {
		assert.Error(t, c.redeem("dm", at, first), "a new code replaces the one before")
	}
	require.NoError(t, c.redeem("dm", at, second))
}

func TestANewChannelOrOneMovedNeedsItsCodeAndNothingIsWrittenWithout(t *testing.T) {
	pao, mf := channelNode(t, true)
	a := channelAPI(t, pao, nil)
	ctx := context.Background()

	err := a.Put(ctx, uiChannel(2, "ui", "free1"))
	require.ErrorIs(t, err, local.ErrBadRequest)
	assert.Contains(t, err.Error(), "needs its target identified first")
	_, statErr := os.Stat(mf)
	assert.True(t, os.IsNotExist(statErr), "nothing was written")

	require.NoError(t, putIdentified(ctx, a, uiChannel(2, "ui", "free1")))

	moved := uiChannel(2, "ui", "free1")
	moved.Owner = "bob"
	assert.ErrorContains(t, a.Put(ctx, moved), "needs its target identified first", "another owner")
	moved = uiChannel(2, "ui", "free2")
	assert.ErrorContains(t, a.Put(ctx, moved), "needs its target identified first", "another provider")

	before, err := os.ReadFile(mf)
	require.NoError(t, err)
	code, _, err := a.codes.issue("ui", where{"free1", "private", "alice"})
	require.NoError(t, err)
	moved = uiChannel(2, "ui", "free1")
	moved.Owner, moved.Code = "bob", code
	assert.ErrorContains(t, a.Put(ctx, moved), "sent to another target", "a code for where it was is not one for where it goes")
	after, err := os.ReadFile(mf)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestAChannelLeftWhereItIsNeedsNoCode(t *testing.T) {
	pao, _ := channelNode(t, true)
	a := channelAPI(t, pao, nil)
	ctx := context.Background()
	group := local.ChannelConfigJSON{ID: 2, Name: "team", Provider: "free1", Owner: "alice", Target: "-5",
		Groups: []local.GroupConfigJSON{{ID: "-5", Name: "team", Members: config.MembersOperators}}}
	require.NoError(t, putIdentified(ctx, a, group))
	group.Groups[0].Name = "renamed"
	require.NoError(t, a.Put(ctx, group), "the group's name is not where the channel is")
}

func TestAWrongCodeIsNotCountedForADefinitionThatWouldNotLoad(t *testing.T) {
	pao, _ := channelNode(t, true)
	a := channelAPI(t, pao, nil)
	ctx := context.Background()
	code, _, err := a.codes.issue("ui", where{"nope", "private", "alice"})
	require.NoError(t, err)
	bad := uiChannel(2, "ui", "nope")
	bad.Code = code
	assert.ErrorContains(t, a.Put(ctx, bad), `provider "nope" is not configured`)
	require.NoError(t, a.codes.redeem("ui", where{"nope", "private", "alice"}, code), "the code was not spent on it")
}

// identifyNode is a channel API whose provider free1 is a running bot on svc,
// and alice is bound on it as sender 4242.
func identifyNode(t *testing.T, svc *service) *channelConfigAPI {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(svc.handler))
	t.Cleanup(ts.Close)
	pao, _ := channelNode(t, true)
	a := channelAPI(t, pao, nil)
	as, err := builtin.Table().Build([]adapter.Spec{{Name: "free1", Kind: "bot", Credential: "tok-1", Options: map[string]any{"endpoint": ts.URL + "/bot{token}/{method}"}}}, nopHandler{})
	require.NoError(t, err)
	a.providers = map[string]adapter.Adapter{"free1": as[0]}
	a.senders = func(provider, subject string) ([]string, error) {
		if provider == "free1" && subject == "alice" {
			return []string{"4242"}, nil
		}
		return nil, nil
	}
	return a
}

var codeIn = regexp.MustCompile(`[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}`)

func TestIdentifySendsACodeToTheOwnersPrivateConversationAndItDefinesTheChannel(t *testing.T) {
	svc := &service{}
	a := identifyNode(t, svc)
	ctx := context.Background()

	got, err := a.Identify(ctx, uiChannel(0, "ui", "free1"))
	require.NoError(t, err)
	assert.Equal(t, 1, got.Sent)
	text := lineWith(t, svc, 4242, `channel "ui"`)
	code := codeIn.FindString(text)
	require.NotEmpty(t, code, text)
	assert.Contains(t, text, "If nobody asked for it, ignore this message.")

	c := uiChannel(0, "ui", "free1")
	c.Code = code
	require.NoError(t, a.Put(ctx, c))
	l, err := a.List(ctx)
	require.NoError(t, err)
	assert.True(t, len(l.Channels) == 2 && l.Channels[1].Name == "ui", "defined")
	assert.Error(t, a.Put(ctx, local.ChannelConfigJSON{Name: "ui2", Provider: "free1", Owner: "alice", Target: "private", Code: code}), "the code was for ui, and is spent")
}

func TestIdentifySendsToTheGroupOrTopicThatIsTheTarget(t *testing.T) {
	svc := &service{}
	a := identifyNode(t, svc)
	team := local.ChannelConfigJSON{Name: "team", Provider: "free1", Owner: "alice", Target: "-700/42",
		Groups: []local.GroupConfigJSON{{ID: "-700", Name: "team", Members: config.MembersOperators, Topics: []local.TopicConfigJSON{{ID: "42", Name: "work"}}}}}
	_, err := a.Identify(context.Background(), team)
	require.NoError(t, err)
	lineWith(t, svc, -700, `channel "team"`)
	m := svc.sentWith(-700, `channel "team"`)
	require.NotNil(t, m)
	assert.Equal(t, "42", m["message_thread_id"], "in the topic")
}

func TestIdentifyRefusesWhatCannotBeSentOrWouldNotBeWritten(t *testing.T) {
	svc := &service{}
	a := identifyNode(t, svc)
	ctx := context.Background()

	_, err := a.Identify(ctx, local.ChannelConfigJSON{Name: "ui", Provider: "free1", Owner: "bob", Target: "private"})
	require.ErrorIs(t, err, local.ErrConflict)
	assert.Contains(t, err.Error(), "bob is not registered on provider \"free1\"")

	_, err = a.Identify(ctx, uiChannel(0, "ui", "free2"))
	require.ErrorIs(t, err, local.ErrConflict)
	assert.Contains(t, err.Error(), `provider "free2" is not running`)

	_, err = a.Identify(ctx, uiChannel(0, "mine", "free1"))
	require.ErrorIs(t, err, local.ErrConflict, "a channel of pao.yaml")

	_, err = a.Identify(ctx, uiChannel(0, "ui", "nope"))
	require.ErrorIs(t, err, local.ErrBadRequest, "a definition that would not load")
	assert.Empty(t, svc.texts(4242), "and nothing was sent for any of them")
}

type refusing struct{ adapter.Adapter }

func (refusing) Announce(context.Context, string, string) error { return errors.New("bot was blocked") }

func TestACodeThatCouldNotBeSentCannotBeEntered(t *testing.T) {
	svc := &service{}
	a := identifyNode(t, svc)
	a.providers["free1"] = refusing{a.providers["free1"]}
	_, err := a.Identify(context.Background(), uiChannel(0, "ui", "free1"))
	require.ErrorIs(t, err, local.ErrConflict)
	assert.Contains(t, err.Error(), "bot was blocked")
	assert.ErrorContains(t, a.codes.redeem("ui", where{"free1", "private", "alice"}, "AAAA-AAAA"), "no code is waiting")
}

type nopHandler struct{}

func (nopHandler) ReceiveFromChannel(context.Context, comm.ChannelInbound) (comm.ChannelResult, error) {
	return comm.ChannelResult{}, nil
}
func (nopHandler) Destinations(string, core.Message) []string   { return nil }
func (nopHandler) NativeOf(string, string) (string, bool)       { return "", false }
func (nopHandler) DefaultAgent(string) (string, bool)           { return "", false }
func (nopHandler) DefaultAgentIn(string, string) (string, bool) { return "", false }
