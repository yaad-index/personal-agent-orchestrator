package registry

import (
	"testing"
	"time"

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

func TestAddTempAndRemove(t *testing.T) {
	r := New()
	require.NoError(t, r.Add(core.Profile{Name: "static"}))
	created := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	require.NoError(t, r.AddTemp(core.Profile{Name: "tmp", Tags: []string{"x"}}, TempInfo{Created: created, Channel: "local", Recipe: "r"}))

	_, _, ok := r.Lookup("tmp")
	require.True(t, ok, "a temporary agent is a first-class registered agent")
	assert.Len(t, r.ByTag("x"), 1, "and is addressable by tag")

	require.NoError(t, r.Remove("tmp"))
	_, _, ok = r.Lookup("tmp")
	assert.False(t, ok)
	assert.Empty(t, r.ByTag("x"))
}

func TestAddTempRejectsAnyNameCollision(t *testing.T) {
	r := New()
	require.NoError(t, r.Add(core.Profile{Name: "static"}))
	require.NoError(t, r.AddTemp(core.Profile{Name: "tmp"}, TempInfo{}))
	assert.Error(t, r.AddTemp(core.Profile{Name: "static"}, TempInfo{}), "over a configured agent")
	assert.Error(t, r.AddTemp(core.Profile{Name: "tmp"}, TempInfo{}), "over another temporary agent")
	assert.Error(t, r.Add(core.Profile{Name: "tmp"}), "a configured agent over a temporary one")
}

func TestRemoveNeverRemovesAConfiguredAgent(t *testing.T) {
	r := New()
	require.NoError(t, r.Add(core.Profile{Name: "static"}))
	err := r.Remove("static")
	require.ErrorIs(t, err, ErrNotTemporary)
	_, _, ok := r.Lookup("static")
	assert.True(t, ok)

	assert.Error(t, r.Remove("missing"))
}

func TestTempsListsOnlyTemporaryAgentsByName(t *testing.T) {
	r := New()
	require.NoError(t, r.Add(core.Profile{Name: "static"}))
	require.NoError(t, r.AddTemp(core.Profile{Name: "b"}, TempInfo{Channel: "local"}))
	require.NoError(t, r.AddTemp(core.Profile{Name: "a"}, TempInfo{Recipe: "r"}))

	got := r.Temps()
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].Profile.Name)
	assert.Equal(t, "r", got[0].Info.Recipe)
	assert.Equal(t, "b", got[1].Profile.Name)
	assert.Equal(t, "local", got[1].Info.Channel)
}

func TestFlagUnusedNeedsAgeAndNoMessage(t *testing.T) {
	created := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	th := Thresholds{UnusedAfter: 10 * time.Minute, IdleAfter: 30 * time.Minute}
	r := New()
	require.NoError(t, r.AddTemp(core.Profile{Name: "t"}, TempInfo{Created: created}))
	v := r.Temps()[0]

	assert.Equal(t, FlagActive, v.Flag(created.Add(9*time.Minute), th), "young agent is not yet unused")
	assert.Equal(t, FlagUnused, v.Flag(created.Add(10*time.Minute), th))
	assert.Equal(t, FlagUnused, v.Flag(created.Add(5*time.Hour), th), "never given a message stays unused, never idle")
}

func TestFlagIdleOnlyAfterUse(t *testing.T) {
	created := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	th := Thresholds{UnusedAfter: 10 * time.Minute, IdleAfter: 30 * time.Minute}
	r := New()
	require.NoError(t, r.AddTemp(core.Profile{Name: "t"}, TempInfo{Created: created}))
	r.Touch("t", created.Add(5*time.Minute), true)
	v := r.Temps()[0]

	assert.Equal(t, FlagActive, v.Flag(created.Add(34*time.Minute), th))
	assert.Equal(t, FlagIdle, v.Flag(created.Add(35*time.Minute), th), "quiet for IdleAfter since the last activity")
	assert.Equal(t, FlagActive, v.Flag(created.Add(20*time.Minute), th), "a used agent past UnusedAfter is not unused")
}

func TestOutputIsActivityButNotUse(t *testing.T) {
	created := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	th := Thresholds{UnusedAfter: 10 * time.Minute, IdleAfter: 30 * time.Minute}
	r := New()
	require.NoError(t, r.AddTemp(core.Profile{Name: "t"}, TempInfo{Created: created}))
	r.Touch("t", created.Add(time.Minute), false)
	v := r.Temps()[0]

	assert.False(t, v.Received, "output alone is not a message having reached the agent")
	assert.Equal(t, FlagUnused, v.Flag(created.Add(11*time.Minute), th))
}

func TestTouchNeverMovesActivityBackwards(t *testing.T) {
	created := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	r := New()
	require.NoError(t, r.AddTemp(core.Profile{Name: "t"}, TempInfo{Created: created}))
	r.Touch("t", created.Add(10*time.Minute), true)
	r.Touch("t", created.Add(2*time.Minute), true)
	assert.Equal(t, created.Add(10*time.Minute), r.Temps()[0].LastActivity)
}

func TestTouchOnRemovedOrUnknownAgentIsNoop(t *testing.T) {
	r := New()
	require.NoError(t, r.AddTemp(core.Profile{Name: "t"}, TempInfo{}))
	require.NoError(t, r.Remove("t"))
	assert.NotPanics(t, func() { r.Touch("t", time.Now(), true); r.Touch("nope", time.Now(), false) })
	_, _, ok := r.Lookup("t")
	assert.False(t, ok, "a late touch does not resurrect it")
}

func TestZeroThresholdsNeverFlag(t *testing.T) {
	created := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	r := New()
	require.NoError(t, r.AddTemp(core.Profile{Name: "t"}, TempInfo{Created: created}))
	v := r.Temps()[0]
	assert.Equal(t, FlagActive, v.Flag(created.Add(1000*time.Hour), Thresholds{}))
}

func TestTempOfFindsOnlyTemporaryAgents(t *testing.T) {
	r := New()
	require.NoError(t, r.Add(core.Profile{Name: "static"}))
	require.NoError(t, r.AddTemp(core.Profile{Name: "tmp", Tags: []string{"x"}}, TempInfo{Channel: "local", Recipe: "r"}))

	v, ok := r.TempOf("tmp")
	require.True(t, ok)
	assert.Equal(t, "local", v.Info.Channel)
	v.Profile.Tags[0] = "mutated"
	again, _ := r.TempOf("tmp")
	assert.Equal(t, []string{"x"}, again.Profile.Tags, "a snapshot does not alias the registry")

	_, ok = r.TempOf("static")
	assert.False(t, ok, "a configured agent is not a temporary one")
	_, ok = r.TempOf("missing")
	assert.False(t, ok)
}

func TestAllListsEveryAgentByNameWithTempDetail(t *testing.T) {
	r := New()
	require.NoError(t, r.Add(core.Profile{Name: "b", Tags: []string{"x"}}))
	require.NoError(t, r.AddTemp(core.Profile{Name: "a"}, TempInfo{Channel: "local", Recipe: "r"}))
	require.NoError(t, r.SetState("b", core.Running))

	got := r.All()
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].Profile.Name)
	require.NotNil(t, got[0].Temp)
	assert.Equal(t, "r", got[0].Temp.Info.Recipe)
	assert.Equal(t, "b", got[1].Profile.Name)
	assert.Nil(t, got[1].Temp, "a configured agent has no temporary detail")
	assert.Equal(t, core.Running, got[1].State)

	got[1].Profile.Tags[0] = "mutated"
	assert.Equal(t, []string{"x"}, r.All()[1].Profile.Tags, "a snapshot does not alias the registry")
	assert.Empty(t, New().All())
}
