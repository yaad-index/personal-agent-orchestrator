package comm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

func proposal(cmd string) core.Message {
	return core.Message{Sender: "pao", Text: cmd, Actions: []core.Action{{Label: cmd, Command: cmd}}}
}

func propose(t *testing.T, l *Layer, cmd string) core.Message {
	t.Helper()
	m, err := l.Propose(context.Background(), "a", proposal(cmd))
	require.NoError(t, err)
	return m
}

func TestAProposalGoesBackWhereTheTurnBegan(t *testing.T) {
	l, _, chat, local := originSetup(t)
	fromChat(t, l, privately("bind it"))
	propose(t, l, "/pao agents")
	assert.Equal(t, map[string][]string{"op": {"/pao agents"}}, chat.take())

	fromChat(t, l, inGroup("-5", "op", "bind it"))
	propose(t, l, "/pao view a pause")
	assert.Equal(t, map[string][]string{"-5": {"/pao view a pause"}}, chat.take(), "asked in the group, proposed in the group, and not privately")
	assert.Empty(t, local.texts())
}

func TestAProposalInATurnFromTheLocalChannelGoesThere(t *testing.T) {
	l, _, chat, local := originSetup(t)
	_, err := l.ReceiveAsOperator(context.Background(), Inbound{Source: "local", Sender: "alice", Text: "hi", Target: byName("a")})
	require.NoError(t, err)
	propose(t, l, "/pao agents")
	assert.Equal(t, []string{"/pao agents"}, local.texts())
	assert.Empty(t, chat.take())
}

func TestAProposalIsInNoAgentsScopeAndCarriesItsButton(t *testing.T) {
	l, _, _, local := originSetup(t)
	_, err := l.ReceiveAsOperator(context.Background(), Inbound{Source: "local", Sender: "alice", Text: "hi", Target: byName("a")})
	require.NoError(t, err)
	m := propose(t, l, "/pao agents")
	assert.Equal(t, core.MessageSystemEvent, m.Kind)
	_, err = l.Fetch("a", m.ID)
	assert.Error(t, err, "the agent cannot fetch what was shown to the operator")
	require.Len(t, local.got, 1)
	assert.Equal(t, []core.Action{{Label: "/pao agents", Command: "/pao agents"}}, local.got[0].Actions)
}

func TestWithNoOneToProposeToNothingIsSent(t *testing.T) {
	l, _, chat, local := originSetup(t)
	_, err := l.Propose(context.Background(), "a", proposal("/pao agents"))
	require.Error(t, err, "no turn at all")

	_, err = l.ReceiveAsOperator(context.Background(), Inbound{Source: "mcp", Sender: "b", Text: "hi", Target: byName("a")})
	require.NoError(t, err)
	_, err = l.Propose(context.Background(), "a", proposal("/pao agents"))
	require.Error(t, err, "a turn another agent started")
	assert.Contains(t, err.Error(), "no one to propose to")

	_, err = l.ReceiveAsOperator(context.Background(), Inbound{Source: "chat", Sender: "bob", Conversation: "plain", Text: "hi", Target: byName("a")})
	require.NoError(t, err)
	_, err = l.Propose(context.Background(), "a", proposal("/pao agents"))
	require.Error(t, err, "bob is not an operator, so his conversation is not shown it")
	assert.Contains(t, err.Error(), "bob")
	assert.Empty(t, chat.take())
	assert.Empty(t, local.texts())
}

func TestAFailedDeliveryPutsBackTheRequesterWithTheOrigin(t *testing.T) {
	l, r, chat, _ := originSetup(t)
	fromChat(t, l, privately("first"))
	r.fail = map[string]error{"a": errors.New("down")}
	_, _ = l.ReceiveAsOperator(context.Background(), Inbound{Source: "chat", Sender: "bob", Conversation: "plain", Text: "hi", Target: byName("a")})
	r.fail = nil
	propose(t, l, "/pao agents")
	assert.Equal(t, map[string][]string{"op": {"/pao agents"}}, chat.take(), "bob's message never reached the agent, so alice's turn stands")
}

// pressChannel names each message it sends by a fresh platform id and records
// which messages it was asked to close.
type pressChannel struct {
	name   string
	mu     sync.Mutex
	n      int
	sent   map[string]string // platform id -> text
	closed []string
}

func (c *pressChannel) Name() string                             { return c.name }
func (c *pressChannel) Send(context.Context, core.Message) error { return nil }
func (c *pressChannel) SendNative(_ context.Context, m core.Message) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	id := fmt.Sprintf("m%d", c.n)
	c.sent[id] = m.Text
	return []string{id}, nil
}
func (c *pressChannel) CloseActions(_ context.Context, id, outcome string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = append(c.closed, id+" "+outcome)
	return nil
}
func (c *pressChannel) closedNow() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.closed...)
}
func (c *pressChannel) idOf(text string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, t := range c.sent {
		if t == text {
			return id
		}
	}
	return ""
}

func pressSetup(t *testing.T) (*Layer, *pressChannel) {
	t.Helper()
	l, _, _, _ := chanSetup(t)
	l.SetCommands(&cmdOps{})
	ch := &pressChannel{name: "chat", sent: map[string]string{}}
	l.AddChannel(ch)
	l.SetChannelIDs(map[string]int{"chat": 1})
	fromChat(t, l, privately("pause it"))
	return l, ch
}

func press(t *testing.T, l *Layer, sender, pressed string) {
	t.Helper()
	_, err := l.ReceiveFromChannel(context.Background(), ChannelInbound{Channel: "chat", Sender: sender, Text: "/pao view a pause", Press: true, Pressed: pressed})
	require.NoError(t, err)
}

func TestAPressedProposalsButtonIsClosedOnce(t *testing.T) {
	l, ch := pressSetup(t)
	m := propose(t, l, "/pao view a pause")
	native := ch.idOf("/pao view a pause")
	require.NotEmpty(t, native)

	press(t, l, "op", native)
	require.Eventually(t, func() bool { return len(ch.closedNow()) == 1 }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{m.ID + " " + OutcomePressed}, ch.closedNow())

	press(t, l, "op", native)
	time.Sleep(50 * time.Millisecond)
	assert.Len(t, ch.closedNow(), 1, "closed once")
}

func TestAPressOnAnythingButAnOpenProposalClosesNothing(t *testing.T) {
	l, ch := pressSetup(t)
	q, err := l.Notify(context.Background(), core.Message{Sender: "pao", Text: "may I?", Actions: []core.Action{{Label: "ALLOW", Command: "/pao allow q1"}}})
	require.NoError(t, err)
	require.NotEmpty(t, q.ID)
	propose(t, l, "/pao view a pause")
	native := ch.idOf("/pao view a pause")

	press(t, l, "op", ch.idOf("may I?"))
	press(t, l, "op", "")
	press(t, l, "op", "unknown")
	press(t, l, "plain", native)
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, ch.closedNow(), "a gate question is the gate's to close, and only the operator's press counts")
}

func TestOnlyTheNewestProposalsAreKeptForClosing(t *testing.T) {
	var s proposalSet
	for i := range maxProposals + 1 {
		s.add(fmt.Sprintf("p%d", i))
	}
	assert.False(t, s.take("p0"), "the oldest is forgotten past the limit")
	assert.True(t, s.take("p1"))
	assert.True(t, s.take(fmt.Sprintf("p%d", maxProposals)))
}
