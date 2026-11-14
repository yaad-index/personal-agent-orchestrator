package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mcpsrv"
	"github.com/yaad-index/personal-agent-orchestrator/internal/msgstore"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
)

type sink struct {
	mu  sync.Mutex
	got map[string][]string
}

func (s *sink) Launch(context.Context, core.Profile) error { return nil }
func (s *sink) Deliver(_ context.Context, p core.Profile, m core.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.got == nil {
		s.got = map[string][]string{}
	}
	s.got[p.Name] = append(s.got[p.Name], m.Line())
	return nil
}

// routeTo lets one agent route to the agents carrying one tag.
type routeTo struct{ agent, tag string }

func (r routeTo) Allow(s access.Subject, a access.Action, res access.Resource) bool {
	if s.Kind == access.KindOperator {
		return true
	}
	if s.Kind != access.KindAgent || s.Name != r.agent || a != access.Route {
		return false
	}
	for _, t := range res.Tags {
		if t == r.tag {
			return true
		}
	}
	return false
}

func sendOps(t *testing.T, auth access.Authorizer) (mcpOps, *sink) {
	t.Helper()
	reg := registry.New()
	for name, tags := range map[string][]string{"a": {"team"}, "b": {"team", "secret"}, "c": {"other"}} {
		require.NoError(t, reg.Add(core.Profile{Name: name, Tags: tags}))
	}
	store, err := msgstore.Open(filepath.Join(t.TempDir(), "m.db"), 100)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	sk := &sink{}
	layer := comm.New(store, route.New(reg, sk, sk))
	return mcpOps{layer: layer, auth: auth}, sk
}

func TestAnOperatorMessageReachesEveryAddressedAgentAsFromTheOperator(t *testing.T) {
	ops, sk := sendOps(t, nil)
	got, err := ops.Send(context.Background(), access.Operator, true, "team", "hello")
	require.NoError(t, err)
	assert.Equal(t, []mcpsrv.Delivered{{Agent: "a", Launched: true}, {Agent: "b", Launched: true}}, got)
	assert.Equal(t, []string{"[operator]: hello"}, sk.got["a"])
	assert.Equal(t, []string{"[operator]: hello"}, sk.got["b"])
	assert.Empty(t, sk.got["c"])
}

func TestAnAgentsMessageCarriesItsOwnNameAndOnlyReachesWhatItMayAddress(t *testing.T) {
	ops, sk := sendOps(t, routeTo{agent: "a", tag: "team"})
	got, err := ops.Send(context.Background(), access.Agent("a"), true, "team", "psst")
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.Equal(t, []string{"[a]: psst"}, sk.got["b"], "from the agent that sent it, by its own name")

	_, err = ops.Send(context.Background(), access.Agent("a"), false, "c", "nope")
	require.ErrorIs(t, err, core.ErrNoMatch)
	assert.Empty(t, sk.got["c"])
}

func TestAnAgentCannotTellAnAgentItMayNotAddressFromOneThatDoesNotExist(t *testing.T) {
	ops, sk := sendOps(t, routeTo{agent: "a", tag: "team"})
	ctx := context.Background()
	_, denied := ops.Send(ctx, access.Agent("a"), false, "c", "x")
	_, missing := ops.Send(ctx, access.Agent("a"), false, "c-does-not-exist", "x")
	require.Error(t, denied)
	assert.Equal(t, "no match: agent \"c\"", normalize(denied.Error()), "the words are the router's own")
	assert.Equal(t, "no match: agent \"c-does-not-exist\"", normalize(missing.Error()))

	_, deniedTag := ops.Send(ctx, access.Agent("a"), true, "other", "x")
	_, missingTag := ops.Send(ctx, access.Agent("a"), true, "no-such-tag", "x")
	assert.Equal(t, "no match: tag \"other\"", normalize(deniedTag.Error()))
	assert.Equal(t, "no match: tag \"no-such-tag\"", normalize(missingTag.Error()))
	assert.Empty(t, sk.got)
}

func normalize(s string) string {
	// core.ErrNoMatch's own text is the prefix; compare only the shape after it.
	const prefix = "target matches no agent: "
	if len(s) > len(prefix) && s[:len(prefix)] == prefix {
		return "no match: " + s[len(prefix):]
	}
	return s
}

func TestANarrowedFanOutDeliversOnlyToWhatIsAllowed(t *testing.T) {
	// b is in team and secret; the agent may address team, so both a and b: a tag
	// scope grants the whole group. An agent allowed only a different tag gets none.
	ops, sk := sendOps(t, routeTo{agent: "a", tag: "secret"})
	got, err := ops.Send(context.Background(), access.Agent("a"), true, "team", "x")
	require.NoError(t, err)
	require.Len(t, got, 1, "of the two agents the tag names, only the one in the scope the agent may address")
	assert.Equal(t, "b", got[0].Agent)
	assert.Empty(t, sk.got["a"])
	assert.Equal(t, []string{"[a]: x"}, sk.got["b"])
}

func TestTheDefaultSeamLetsNoAgentAddressAnyone(t *testing.T) {
	ops, sk := sendOps(t, nil)
	_, err := ops.Send(context.Background(), access.Agent("a"), false, "b", "x")
	require.ErrorIs(t, err, core.ErrNoMatch)
	_, err = ops.Send(context.Background(), access.Subject{}, false, "b", "x")
	require.ErrorIs(t, err, core.ErrNoMatch, "nobody least of all")
	assert.Empty(t, sk.got)
}

func TestTheOperatorOverMCPReachesATemporaryAgentWithTheOperatorsName(t *testing.T) {
	n := startTempNode(t, map[string]string{"fake": goodRecipe("fake")}, config.Temp{})
	ctx := context.Background()
	_, err := n.client.Spawn(ctx, "fake", "tmp-a", "", "")
	require.NoError(t, err)
	cs := mcpSession(t, n, token)

	out := mcpCall[struct {
		Delivered []mcpsrv.Delivered `json:"delivered"`
	}](t, cs, "send_message", map[string]any{"name": "tmp-a", "text": "from mcp"})
	require.Len(t, out.Delivered, 1)
	assert.Equal(t, "tmp-a", out.Delivered[0].Agent)
	assert.Equal(t, "echo: [operator]: from mcp", n.next(t).Text, "the agent read one line, from the operator")

	out = mcpCall[struct {
		Delivered []mcpsrv.Delivered `json:"delivered"`
	}](t, cs, "send_message", map[string]any{"tag": "temp", "text": "by tag"})
	require.Len(t, out.Delivered, 1)
	assert.Equal(t, "echo: [operator]: by tag", n.next(t).Text)
}

func TestReservedNamesAreNotAgentNames(t *testing.T) {
	for _, name := range core.ReservedNames {
		err := config.ValidateProfile(core.Profile{Name: name, Backend: "b", Startup: core.Startup{Command: "c", ReadyPattern: "r", ReadyTimeout: 1}, Transcript: core.Transcript{Format: "f", Path: "p"}})
		require.Error(t, err, name)
	}
}

func TestSendersAreNamedFromTheSubjectAndNobodyHasNone(t *testing.T) {
	n, ok := senderName(access.Operator)
	assert.True(t, ok)
	assert.Equal(t, "operator", n)
	n, ok = senderName(access.Agent("a"))
	assert.True(t, ok)
	assert.Equal(t, "a", n)
	for _, nobody := range []access.Subject{{}, {Kind: access.KindAgent}, {Name: "operator"}} {
		_, ok = senderName(nobody)
		assert.False(t, ok, "%+v", nobody)
	}
}

func TestNobodyCannotSendEvenIfTheSeamWouldLetThemAddress(t *testing.T) {
	ops, sk := sendOps(t, everyoneRoutes{})
	_, err := ops.Send(context.Background(), access.Subject{}, false, "a", "x")
	require.ErrorIs(t, err, core.ErrNoMatch, "the invariant holds in the sender logic itself, not only because the default seam refuses nobody")
	assert.Empty(t, sk.got)
}

// everyoneRoutes is a seam that allows anything, to show it is not what stops a
// nameless sender.
type everyoneRoutes struct{}

func (everyoneRoutes) Allow(access.Subject, access.Action, access.Resource) bool { return true }
