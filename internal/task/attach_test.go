package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
)

// toolReach says which tools are proxied, and which agents reach each.
type toolReach map[string][]string

func (r toolReach) Proxied(tool string) bool { _, ok := r[tool]; return ok }
func (r toolReach) Reaches(agent, tool string) bool {
	for _, a := range r[tool] {
		if a == agent {
			return true
		}
	}
	return false
}

func TestATaskWithASecretRunsOnlyOnAOneOffStartedWithIt(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{{Name: "bee", Tags: []string{"r"}, Account: "sub", Running: true}}}
	o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r"}, Account: "sub"}}}
	d, s := oneOffDispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true}, o)
	n := newTask("r")
	n.Attachments.Secret = "report-api"
	tk, err := s.Create(context.Background(), access.Operator, n)
	require.NoError(t, err)
	passUntil(t, d, func() bool { return get(t, s, tk.ID).Assignee != "" })
	assert.Equal(t, "writer-a", get(t, s, tk.ID).Assignee, "the free running agent is passed over")
	spawned, _ := o.snapshot()
	assert.Equal(t, []string{"operator writer -> writer-a with report-api"}, spawned)
	assert.Empty(t, fleet.sent("bee"))
}

func TestAOneOffsBriefNamesTheFileItsSecretIsIn(t *testing.T) {
	fleet := &fakeFleet{}
	o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r"}, Account: "sub"}}}
	d, s := oneOffDispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true}, o)
	d.SecretPath = func(agent, secret string) (string, error) { return "/run-secrets/" + agent + "/" + secret, nil }
	n := newTask("r")
	n.Attachments.Secret = "report-api"
	tk, err := s.Create(context.Background(), access.Operator, n)
	require.NoError(t, err)
	passUntil(t, d, func() bool { return get(t, s, tk.ID).Assignee != "" })
	require.Eventually(t, func() bool { return len(fleet.sent("writer-a")) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Contains(t, fleet.sent("writer-a")[0], "the file /run-secrets/writer-a/report-api")
}

func TestAOneOffsBriefNamesThePlaceholderOfAnInjectedSecret(t *testing.T) {
	fleet := &fakeFleet{}
	o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r"}, Account: "sub"}}}
	d, s := oneOffDispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true}, o)
	d.SecretPath = func(agent, secret string) (string, error) { return "/run-secrets/" + agent + "/" + secret, nil }
	d.Injected = func(secret string) (string, []string, bool) {
		return "ACME_TOKEN", []string{"api.acme.example", "acme.example"}, secret == "report-api"
	}
	n := newTask("r")
	n.Attachments.Secret = "report-api"
	tk, err := s.Create(context.Background(), access.Operator, n)
	require.NoError(t, err)
	passUntil(t, d, func() bool { return get(t, s, tk.ID).Assignee != "" })
	require.Eventually(t, func() bool { return len(fleet.sent("writer-a")) == 1 }, 5*time.Second, 10*time.Millisecond)
	brief := fleet.sent("writer-a")[0]
	assert.Contains(t, brief, "Use $ACME_TOKEN as the credential in your requests to api.acme.example, acme.example")
	assert.NotContains(t, brief, "/run-secrets/", "an injected secret has no file to name")
	assert.NotContains(t, brief, "file-reading tool")
}

func TestASecretTaskWithNoRecipeWaitsAndSaysWhy(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{{Name: "bee", Tags: []string{"r"}, Account: "sub"}}}
	d, s := dispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true})
	n := newTask("r")
	n.Attachments.Secret = "report-api"
	tk, err := s.Create(context.Background(), access.Operator, n)
	require.NoError(t, err)
	d.Pass(context.Background())
	got := get(t, s, tk.ID)
	assert.Equal(t, Open, got.Status)
	assert.Equal(t, "waiting: it names a secret, so it runs only on an agent started for it", got.Log[len(got.Log)-1].Text)
}

func TestAnAttachedProxiedToolNarrowsTheAgentsAndGrantsNothing(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{
		{Name: "a-without", Tags: []string{"r"}, Account: "sub", Running: true},
		{Name: "b-with", Tags: []string{"r"}, Account: "sub"},
	}}
	d, s := dispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true})
	d.Tools = toolReach{"search__query": {"b-with"}}
	n := newTask("r")
	n.Attachments.Tools = []string{"search__query", "Read"}
	tk, err := s.Create(context.Background(), access.Operator, n)
	require.NoError(t, err)
	d.Pass(context.Background())
	assert.Equal(t, "b-with", get(t, s, tk.ID).Assignee, "only an agent whose own access reaches the proxied tool; Read is not proxied and narrows nothing")
}

func TestAProxiedToolNoAgentReachesKeepsTheTaskWaitingAndOffOneOffs(t *testing.T) {
	fleet := &fakeFleet{candidates: []Candidate{{Name: "bee", Tags: []string{"r"}, Account: "sub"}}}
	o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r"}, Account: "sub"}}}
	d, s := oneOffDispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true}, o)
	d.Tools = toolReach{"search__query": nil}
	n := newTask("r")
	n.Attachments.Tools = []string{"search__query"}
	tk, err := s.Create(context.Background(), access.Operator, n)
	require.NoError(t, err)
	d.Pass(context.Background())
	got := get(t, s, tk.ID)
	assert.Equal(t, Open, got.Status)
	assert.Contains(t, got.Log[len(got.Log)-1].Text, "a new agent could not reach the attached tool search__query")
	spawned, _ := o.snapshot()
	assert.Empty(t, spawned)
}

func TestATaskWithASecretAndAProxiedToolIsRefusedAtCreate(t *testing.T) {
	s, _ := service(t, grants{"operator": {tagGrant("operator", "r")}})
	s.Proxied = func(tool string) bool { return tool == "search__query" }
	n := newTask("r")
	n.Attachments.Secret = "report-api"
	n.Attachments.Tools = []string{"Read", "search__query"}
	_, err := s.Create(context.Background(), access.Operator, n)
	require.ErrorIs(t, err, ErrBadRequest)
	assert.Contains(t, err.Error(), "cannot reach the proxied tool search__query")

	n.Attachments.Tools = []string{"Read"}
	_, err = s.Create(context.Background(), access.Operator, n)
	assert.NoError(t, err, "a secret with tools pao does not proxy is fine")
	n.Attachments.Secret, n.Attachments.Tools = "", []string{"search__query"}
	_, err = s.Create(context.Background(), access.Operator, n)
	assert.NoError(t, err, "and so is a proxied tool without a secret")
}

func secretOneOff(t *testing.T, o *fakeOneOffs) (*Dispatcher, *Service, Task, *[]string) {
	t.Helper()
	fleet := &fakeFleet{}
	d, s := oneOffDispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true}, o)
	var warned []string
	d.Warn = func(text string) { warned = append(warned, text) }
	n := newTask("r")
	n.Attachments.Secret = "report-api"
	tk, err := s.Create(context.Background(), access.Operator, n)
	require.NoError(t, err)
	passUntil(t, d, func() bool { return get(t, s, tk.ID).Assignee != "" })
	return d, s, get(t, s, tk.ID), &warned
}

func TestASecretOneOffsTranscriptIsRecordedAndRemovedWithIt(t *testing.T) {
	o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r"}, Account: "sub"}}}
	d, s, tk, warned := secretOneOff(t, o)
	assert.Equal(t, "/transcripts/writer-a", tk.Transcript, "where its backend keeps the transcript is in the task")
	_, err := s.Report(context.Background(), access.Agent("writer-a"), tk.ID, Report{Status: Done})
	require.NoError(t, err)
	passUntil(t, d, func() bool { return get(t, s, tk.ID).Removed })
	o.mu.Lock()
	forgot := append([]string(nil), o.forgot...)
	o.mu.Unlock()
	assert.Equal(t, []string{"writer-a /transcripts/writer-a"}, forgot)
	got := get(t, s, tk.ID)
	assert.Equal(t, "one-off writer-a removed; its outputs are in harvest h-1; its transcript was removed", got.Log[len(got.Log)-1].Text)
	assert.Empty(t, *warned)
}

func TestATranscriptThatCannotBeRemovedIsLoggedAndToldToTheOperator(t *testing.T) {
	o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r"}, Account: "sub"}}, forgetErr: errors.New("no transcript at /x")}
	d, s, tk, warned := secretOneOff(t, o)
	_, err := s.Cancel(context.Background(), access.Operator, tk.ID, "")
	require.NoError(t, err)
	passUntil(t, d, func() bool { return get(t, s, tk.ID).Removed })
	got := get(t, s, tk.ID)
	assert.Contains(t, got.Log[len(got.Log)-1].Text, "its transcript could NOT be removed: no transcript at /x")
	require.Len(t, *warned, 1)
	assert.Contains(t, (*warned)[0], "could not be removed: no transcript at /x. The secret's value may still be in it.")
}

func TestAOneOffWithoutASecretKeepsItsTranscript(t *testing.T) {
	fleet := &fakeFleet{}
	o := &fakeOneOffs{recipes: []Recipe{{Name: "writer", Tags: []string{"r"}, Account: "sub"}}}
	d, s := oneOffDispatcher(t, opGrants("r"), fleet, headroomOf{"sub": true}, o)
	tk := create(t, s, access.Operator, "r")
	passUntil(t, d, func() bool { return get(t, s, tk.ID).Assignee != "" })
	assert.Empty(t, get(t, s, tk.ID).Transcript, "recorded only for a run with a secret")
	_, err := s.Report(context.Background(), access.Agent("writer-a"), tk.ID, Report{Status: Done})
	require.NoError(t, err)
	passUntil(t, d, func() bool { return get(t, s, tk.ID).Removed })
	o.mu.Lock()
	defer o.mu.Unlock()
	assert.Empty(t, o.forgot)
}
