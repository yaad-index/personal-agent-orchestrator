package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/yaad-index/personal-agent-orchestrator/internal/mesh/meshpb"
)

// answered starts a master and a secondary with the agent bee, has bee answer once,
// and returns the id the secondary showed for that answer.
func answered(t *testing.T) (a, b *node, id string) {
	t.Helper()
	f := newMeshFixture(t, "n-a", "n-b")
	a = f.startWith(t, "n-a", map[string]string{"boss": ""}, nil)
	b = f.startWith(t, "n-b", map[string]string{"bee": ""}, nil)
	directoryHas(t, a, "n-b", 1)
	_, err := a.client.Post(context.Background(), "human", "first", "name", "bee")
	require.NoError(t, err)
	m := b.next(t)
	require.Equal(t, "echo: [human]: first", m.Text)
	return a, b, m.ID
}

func TestAnIdAMeshNodeShowsNamesItsNode(t *testing.T) {
	_, b, id := answered(t)
	assert.True(t, strings.HasPrefix(id, "n-b:m"), "an id on the stream says which node holds the message: %s", id)
	rep, err := b.client.Post(context.Background(), "human", "second", "name", "bee")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(rep.ID, "n-b:m"), "and so does the id of what was just posted: %s", rep.ID)
}

func TestTheMasterAnswersAMessageOfARemoteAgentByItsQualifiedId(t *testing.T) {
	a, b, id := answered(t)
	ctx := context.Background()

	rep, err := a.client.PostReply(ctx, "human", "yes, that one", "name", "bee", id, "")
	require.NoError(t, err)
	require.Len(t, rep.Results, 1)
	assert.Empty(t, rep.Results[0].Error, "the node resolved the reference in the agent's scope")
	assert.Equal(t, "echo: [human]: yes, that one", b.next(t).Text, "and the agent was given the reply")

	rep, err = a.client.PostReply(ctx, "human", "that one is gone", "name", "bee", "n-b:m99999", "")
	require.NoError(t, err)
	require.Len(t, rep.Results, 1)
	assert.Contains(t, rep.Results[0].Error, "not found", "forwarded, and the node did not find it: nothing is guessed")
}

func TestAReplyThatNamesNoNodeOrTheWrongNodeIsNotFoundAtTheMaster(t *testing.T) {
	a, _, id := answered(t)
	ctx := context.Background()
	bare := id[strings.Index(id, ":")+1:]

	for name, ref := range map[string]string{"bare id": bare, "this node's": "n-a:" + bare, "unknown node": "n-z:" + bare} {
		_, err := a.client.PostReply(ctx, "human", "x", "name", "bee", ref, "")
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "404", name)
	}
}

func TestAQuotedReplyToARemoteAgentIsResolvedOnItsNode(t *testing.T) {
	a, b, _ := answered(t)
	ctx := context.Background()

	rep, err := a.client.PostReply(ctx, "human", "about that", "name", "bee", "", "echo: [human]: first")
	require.NoError(t, err)
	require.Len(t, rep.Results, 1)
	assert.Empty(t, rep.Results[0].Error, "the node found the quoted text in the agent's scope")
	assert.Equal(t, "echo: [human]: about that", b.next(t).Text)

	rep, err = a.client.PostReply(ctx, "human", "about nothing", "name", "bee", "", "text the agent never saw")
	require.NoError(t, err)
	require.Len(t, rep.Results, 1)
	assert.NotEmpty(t, rep.Results[0].Error, "unresolved is reported, never guessed")
}

func TestANodeAnswersAReplyItsAgentWasNotGivenAsNotFound(t *testing.T) {
	f, _, _ := meshWithRecipes(t)
	ctx := context.Background()
	node := asMaster(t, f, "n-b")

	_, err := node.Deliver(ctx, &pb.DeliverRequest{RequestId: "d1", Agent: "bee", Sender: "human", Text: "re", ReplyRef: "m99999"})
	assert.Equal(t, codes.NotFound, status.Code(err))
	_, err = node.Deliver(ctx, &pb.DeliverRequest{RequestId: "d2", Agent: "bee", Sender: "human", Text: "re", ReplyQuote: "never seen"})
	assert.Equal(t, codes.NotFound, status.Code(err), "an unresolved quote is the same answer as an absent one")
	_, err = node.Deliver(ctx, &pb.DeliverRequest{RequestId: "d3", Agent: "bee", Sender: "human", Text: "plain"})
	require.NoError(t, err)

	for i, id := range []string{"d4", "d5"} {
		_, err = node.Deliver(ctx, &pb.DeliverRequest{RequestId: id, Agent: "bee", Sender: "human", Text: "twin message " + string(rune('a'+i))})
		require.NoError(t, err)
	}
	_, err = node.Deliver(ctx, &pb.DeliverRequest{RequestId: "d6", Agent: "bee", Sender: "human", Text: "re", ReplyQuote: "twin message"})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err), "a quote that matches several is reported, in the same status as one that matches none")
	assert.Contains(t, err.Error(), "more than one message matches", "and the message says which")
}

func TestTheMasterNeverResolvesAReplyToARemoteAgentAgainstItsOwnMessages(t *testing.T) {
	a, _, _ := answered(t)
	ctx := context.Background()
	// The master has a message of its own with its own id; naming it to the remote agent
	// must not be taken as a message that agent was given.
	_, err := a.client.Post(ctx, "human", "local note", "name", "boss")
	require.NoError(t, err)
	a.next(t)
	_, err = a.client.PostReply(ctx, "human", "x", "name", "bee", "n-a:m1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestQueryPostedIsNotBuiltYetAndSaysSo(t *testing.T) {
	f, _, _ := meshWithRecipes(t)
	node := asMaster(t, f, "n-b")
	_, err := node.QueryPosted(context.Background(), &pb.PostedQuery{Channel: "local", PlatformMessageId: "m1"})
	assert.Equal(t, codes.Unimplemented, status.Code(err))
}
