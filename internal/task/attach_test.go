package task

import (
	"context"
	"testing"

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
	assert.Empty(t, fleet.delivered["bee"])
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
