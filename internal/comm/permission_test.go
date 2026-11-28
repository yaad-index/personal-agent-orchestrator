package comm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// permissionSetup is a layer with three channels on the provider tg: alice's
// private chat, alice's group team (-5) and bob's private bobdm. alice is an
// operator, as sender op; bob is not, as sender pb. Agent a is alice's and b
// is bob's; neither has a stderr binding.
func permissionSetup(t *testing.T) (*Layer, *fakeIdentity, map[string]*convChannel) {
	t.Helper()
	l, r, id, _ := chanSetup(t)
	r.agents["b"] = []string{"b"}
	r.agents["c"] = []string{"c"} // of no user
	r.users = map[string]string{"a": "alice", "b": "bob"}
	id.bound = map[string]string{"tg\x00op": "alice", "tg\x00pb": "bob"}
	id.permission = map[string]string{}
	l.SetProviders(map[string]string{"chat": "tg", "team": "tg", "bobdm": "tg"})
	l.SetScopes(map[string]Scope{"chat": {Owner: "alice"}, "team": {Owner: "alice", Conversation: "-5"}, "bobdm": {Owner: "bob"}})
	l.SetGroups(map[string]map[string]Group{"team": {"-5": {Name: "team", Policy: PolicyOperators}}})
	l.SetDefaults(mapDefaults{"chat": "a", "bobdm": "b"})
	chs := map[string]*convChannel{}
	for _, n := range []string{"chat", "team", "bobdm"} {
		chs[n] = &convChannel{name: n, l: l, got: map[string][]string{}}
		l.AddChannel(chs[n])
	}
	return l, id, chs
}

func taken(chs map[string]*convChannel) map[string]map[string][]string {
	out := map[string]map[string][]string{}
	for n, c := range chs {
		if got := c.take(); len(got) > 0 {
			out[n] = got
		}
	}
	return out
}

func TestAnAgentsQuestionGoesToItsUsersPrivateConversationOnceByDefault(t *testing.T) {
	l, _, chs := permissionSetup(t)
	_, err := l.NotifyAgent(context.Background(), "a", question)
	require.NoError(t, err)
	assert.Equal(t, map[string]map[string][]string{"chat": {"op": {"allow?"}}}, taken(chs),
		"alice's private conversation, once though two of her channels are on tg; not her group, not bob")
}

func TestAnAgentsQuestionGoesToTheChannelItsUserSet(t *testing.T) {
	l, id, chs := permissionSetup(t)
	id.permission["alice"] = "team"
	_, err := l.NotifyAgent(context.Background(), "a", question)
	require.NoError(t, err)
	assert.Equal(t, map[string]map[string][]string{"team": {"-5": {"allow?"}}}, taken(chs), "her group, and not privately")

	id.permission["alice"] = "chat"
	_, err = l.NotifyAgent(context.Background(), "a", question)
	require.NoError(t, err)
	assert.Equal(t, map[string]map[string][]string{"chat": {"op": {"allow?"}}}, taken(chs))
}

func TestAPermissionChannelThatIsNotTheUsersOwnIsNotUsed(t *testing.T) {
	l, id, chs := permissionSetup(t)
	for _, ch := range []string{"bobdm", "gone"} {
		id.permission["alice"] = ch
		_, err := l.NotifyAgent(context.Background(), "a", question)
		require.NoError(t, err)
		assert.Equal(t, map[string]map[string][]string{"chat": {"op": {"allow?"}}}, taken(chs), "%s: her private conversation instead", ch)
	}
}

func TestAQuestionAboutAnAgentOfANonOperatorStillReachesTheOperator(t *testing.T) {
	l, _, chs := permissionSetup(t)
	_, err := l.NotifyAgent(context.Background(), "b", question)
	require.NoError(t, err)
	assert.Equal(t, map[string]map[string][]string{"chat": {"op": {"allow?"}}}, taken(chs),
		"the operator, who answers it, privately and once; never bob, who may not answer it")
}
