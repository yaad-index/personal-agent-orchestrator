package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

func TestAddAndLookup(t *testing.T) {
	r := New()
	require.NoError(t, r.Add(core.Profile{Name: "a", Tags: []string{"x"}}))

	p, st, ok := r.Lookup("a")
	require.True(t, ok)
	assert.Equal(t, "a", p.Name)
	assert.Equal(t, core.Stopped, st)

	_, _, ok = r.Lookup("missing")
	assert.False(t, ok)
}

func TestAddRejectsDuplicate(t *testing.T) {
	r := New()
	require.NoError(t, r.Add(core.Profile{Name: "a"}))
	assert.Error(t, r.Add(core.Profile{Name: "a"}))
}

func TestAddCopiesTags(t *testing.T) {
	r := New()
	tags := []string{"x"}
	require.NoError(t, r.Add(core.Profile{Name: "a", Tags: tags}))
	tags[0] = "mutated"

	p, _, _ := r.Lookup("a")
	assert.Equal(t, []string{"x"}, p.Tags)
}

func TestByTagOrderedByName(t *testing.T) {
	r := New()
	require.NoError(t, r.Add(core.Profile{Name: "b", Tags: []string{"x"}}))
	require.NoError(t, r.Add(core.Profile{Name: "a", Tags: []string{"x", "y"}}))
	require.NoError(t, r.Add(core.Profile{Name: "c", Tags: []string{"y"}}))

	var names []string
	for _, p := range r.ByTag("x") {
		names = append(names, p.Name)
	}
	assert.Equal(t, []string{"a", "b"}, names)
	assert.Empty(t, r.ByTag("none"))
}

func TestSetState(t *testing.T) {
	r := New()
	require.NoError(t, r.Add(core.Profile{Name: "a"}))
	require.NoError(t, r.SetState("a", core.Running))

	_, st, _ := r.Lookup("a")
	assert.Equal(t, core.Running, st)
	assert.Error(t, r.SetState("missing", core.Running))
}
