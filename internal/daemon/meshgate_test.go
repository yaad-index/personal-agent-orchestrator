package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/yaad-index/personal-agent-orchestrator/internal/mesh/meshpb"
)

func TestTheMasterAnswersAQuestionAskedOnAnotherNode(t *testing.T) {
	_, a, b := meshWithRecipes(t)
	ctx := context.Background()

	res := askGate(t, b, "Bash", `{"command":"ls"}`)
	id := nextQuestion(t, b)
	assert.Equal(t, "n-b.q1", id, "a question carries the id of the node that asked it")
	select {
	case <-res:
		t.Fatal("the hook must stay blocked until the operator answers")
	case <-time.After(150 * time.Millisecond):
	}
	require.NoError(t, a.client.Decide(ctx, id, "allow", ""), "the operator answers at the master")
	d := decisionWithin(t, res)
	assert.True(t, d.Allow)
	assert.Equal(t, "operator", d.Via)

	res = askGate(t, b, "Bash", `{"command":"rm -rf /"}`)
	id = nextQuestion(t, b)
	require.NoError(t, a.client.Decide(ctx, id, "deny", "not on that box"))
	d = decisionWithin(t, res)
	assert.False(t, d.Allow)
	assert.Equal(t, "not on that box", d.Reason, "the reason travels with the denial")

	err := a.client.Decide(ctx, id, "allow", "")
	require.Error(t, err, "a question already answered")
	assert.Contains(t, err.Error(), "404", "the node's not-found keeps its meaning")
}

func TestAnAnswerToRememberIsRememberedByTheNodeThatAsked(t *testing.T) {
	_, a, b := meshWithRecipes(t)
	ctx := context.Background()

	res := askGate(t, b, "Bash", `{"command":"ls"}`)
	require.NoError(t, a.client.Decide(ctx, nextQuestion(t, b), "always", ""))
	assert.True(t, decisionWithin(t, res).Allow)

	d := decisionWithin(t, askGate(t, b, "Bash", `{"command":"ls"}`))
	assert.True(t, d.Allow, "the same action needs no question on that node now")
	assert.Equal(t, "rule", d.Via)
}

func TestTheMasterAnswersItsOwnQuestionsByEitherForm(t *testing.T) {
	_, a, _ := meshWithRecipes(t)
	ctx := context.Background()

	res := askGate(t, a, "Bash", `{"command":"ls"}`)
	id := nextQuestion(t, a)
	assert.Equal(t, "n-a.q1", id)
	require.NoError(t, a.client.Decide(ctx, id, "allow", ""))
	assert.True(t, decisionWithin(t, res).Allow)

	res = askGate(t, a, "Bash", `{"command":"pwd"}`)
	id = nextQuestion(t, a)
	assert.Equal(t, "n-a.q2", id)
	require.NoError(t, a.client.Decide(ctx, "q2", "deny", "bare"), "a bare id is the node's own")
	assert.False(t, decisionWithin(t, res).Allow)
}

func TestASecondaryAnswersOnlyItsOwnQuestions(t *testing.T) {
	_, a, b := meshWithRecipes(t)
	ctx := context.Background()

	res := askGate(t, a, "Bash", `{"command":"ls"}`)
	id := nextQuestion(t, a)
	err := b.client.Decide(ctx, id, "allow", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403", "only the master carries an answer to another node")
	select {
	case <-res:
		t.Fatal("the refused answer must not have decided the question")
	case <-time.After(150 * time.Millisecond):
	}

	err = a.client.Decide(ctx, "n-z.q1", "allow", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400", "no such node")
	require.NoError(t, a.client.Decide(ctx, id, "deny", ""))
	decisionWithin(t, res)
}

func TestANodeAnswersOnlyTheQuestionsItAskedAndOnlyAsTheMasterSaysThem(t *testing.T) {
	f, _, b := meshWithRecipes(t)
	ctx := context.Background()
	node := asMaster(t, f, "n-b")

	res := askGate(t, b, "Bash", `{"command":"ls"}`)
	id := nextQuestion(t, b)

	for _, c := range []struct {
		name string
		req  *pb.DecideRequest
		code codes.Code
	}{
		{"another node's id", &pb.DecideRequest{RequestId: "d1", QuestionId: "n-a.q1", Decision: pb.Decision_DECISION_ALLOW}, codes.NotFound},
		{"a bare id", &pb.DecideRequest{RequestId: "d2", QuestionId: "q1", Decision: pb.Decision_DECISION_ALLOW}, codes.NotFound},
		{"no decision", &pb.DecideRequest{RequestId: "d3", QuestionId: id}, codes.InvalidArgument},
		{"no id", &pb.DecideRequest{RequestId: "d4", Decision: pb.Decision_DECISION_ALLOW}, codes.InvalidArgument},
	} {
		_, err := node.Decide(ctx, c.req)
		assert.Equal(t, c.code, status.Code(err), c.name)
	}
	select {
	case <-res:
		t.Fatal("none of the refused answers may decide the question")
	case <-time.After(150 * time.Millisecond):
	}

	req := &pb.DecideRequest{RequestId: "d5", QuestionId: id, Decision: pb.Decision_DECISION_ALLOW}
	_, err := node.Decide(ctx, req)
	require.NoError(t, err)
	assert.True(t, decisionWithin(t, res).Allow)
	_, err = node.Decide(ctx, req)
	require.NoError(t, err, "a repeat of the same request gets the first result and does not run twice")
	_, err = node.Decide(ctx, &pb.DecideRequest{RequestId: "d6", QuestionId: id, Decision: pb.Decision_DECISION_ALLOW})
	assert.Equal(t, codes.NotFound, status.Code(err), "a new request for an answered question")
}

func TestANodeOutsideAMeshRefusesAQualifiedQuestionAsNotFound(t *testing.T) {
	n := startNode(t, map[string]string{"a": ""})
	err := n.client.Decide(context.Background(), "n-b.q1", "allow", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}
