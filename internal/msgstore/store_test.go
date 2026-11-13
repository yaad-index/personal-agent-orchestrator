package msgstore

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

func open(t *testing.T, window int) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "msgs.db"), window)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func add(t *testing.T, s *Store, text string, scopes ...string) core.Message {
	t.Helper()
	m, err := s.Add(core.Message{Sender: "h", Text: text}, scopes...)
	require.NoError(t, err)
	return m
}

func TestAddAssignsIdTimeAndKind(t *testing.T) {
	s := open(t, 10)
	fixed := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return fixed }

	a := add(t, s, "one", "x")
	b := add(t, s, "two", "x")

	assert.NotEmpty(t, a.ID)
	assert.NotEqual(t, a.ID, b.ID)
	assert.Equal(t, fixed, a.Time)
	assert.Equal(t, core.MessageContent, a.Kind)

	got, err := s.Get(a.ID)
	require.NoError(t, err)
	assert.Equal(t, a, got)
}

func TestAddKeepsAnExplicitTimeAndKind(t *testing.T) {
	s := open(t, 10)
	when := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

	m, err := s.Add(core.Message{Text: "e", Time: when, Kind: core.MessageSystemEvent}, "x")
	require.NoError(t, err)
	assert.Equal(t, when, m.Time)
	assert.Equal(t, core.MessageSystemEvent, m.Kind)
}

func TestGetUnknownId(t *testing.T) {
	s := open(t, 10)
	for _, id := range []string{"m999", "", "x1", "m", "m-1", "m01", "m1junk"} {
		_, err := s.Get(id)
		assert.ErrorIs(t, err, ErrNotFound, "id %q", id)
	}
}

func TestWindowKeepsOnlyTheNewest(t *testing.T) {
	s := open(t, 3)
	var ids []string
	for i := range 7 {
		ids = append(ids, add(t, s, fmt.Sprintf("msg %d", i), "x").ID)
	}

	for _, id := range ids[:4] {
		_, err := s.Get(id)
		assert.ErrorIs(t, err, ErrNotFound, "%s should have left the window", id)
	}
	for _, id := range ids[4:] {
		_, err := s.Get(id)
		assert.NoError(t, err, "%s should still be in the window", id)
	}
}

func TestWindowExactlyFullKeepsEverything(t *testing.T) {
	s := open(t, 3)
	var ids []string
	for i := range 3 {
		ids = append(ids, add(t, s, fmt.Sprintf("m%d", i), "x").ID)
	}
	for _, id := range ids {
		_, err := s.Get(id)
		assert.NoError(t, err)
	}
}

func TestIdsAreNotReusedAfterTheWindowMoves(t *testing.T) {
	s := open(t, 1)
	a := add(t, s, "a", "x")
	b := add(t, s, "b", "x")
	assert.NotEqual(t, a.ID, b.ID)
	_, err := s.Get(a.ID)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestResolveUniqueQuote(t *testing.T) {
	s := open(t, 10)
	add(t, s, "please review the deploy plan", "x")
	want := add(t, s, "lunch at noon?", "x")

	got, err := s.Resolve("x", "lunch at noon")
	require.NoError(t, err)
	assert.Equal(t, want.ID, got.ID)
}

func TestResolveIgnoresWhitespaceRuns(t *testing.T) {
	s := open(t, 10)
	want := add(t, s, "two  words\nacross lines", "x")

	got, err := s.Resolve("x", "words across")
	require.NoError(t, err)
	assert.Equal(t, want.ID, got.ID)
}

func TestResolveNoMatchIsUnresolved(t *testing.T) {
	s := open(t, 10)
	add(t, s, "hello", "x")

	_, err := s.Resolve("x", "goodbye")
	assert.ErrorIs(t, err, ErrUnresolved)
	_, err = s.Resolve("x", "   ")
	assert.ErrorIs(t, err, ErrUnresolved, "an empty quote never matches everything")
}

func TestResolveAmbiguousIsNotGuessed(t *testing.T) {
	s := open(t, 10)
	add(t, s, "ok", "x")
	add(t, s, "ok, thanks", "x")

	_, err := s.Resolve("x", "ok")
	assert.ErrorIs(t, err, ErrAmbiguous)

	got, err := s.Resolve("x", "thanks")
	require.NoError(t, err)
	assert.Equal(t, "ok, thanks", got.Text, "a longer quote disambiguates")
}

func TestResolveIsScopedToTheAgent(t *testing.T) {
	s := open(t, 10)
	add(t, s, "secret plan", "a")
	add(t, s, "secret plan", "b")
	both := add(t, s, "shared note", "a", "b")

	got, err := s.Resolve("a", "secret plan")
	require.NoError(t, err, "b's copy must not make a's quote ambiguous")
	assert.Equal(t, "secret plan", got.Text)

	_, err = s.Resolve("c", "secret plan")
	assert.ErrorIs(t, err, ErrUnresolved, "an agent cannot quote what it was never shown")

	for _, agent := range []string{"a", "b"} {
		got, err := s.Resolve(agent, "shared note")
		require.NoError(t, err)
		assert.Equal(t, both.ID, got.ID)
	}
}

func TestResolveSkipsMessagesThatLeftTheWindow(t *testing.T) {
	s := open(t, 2)
	add(t, s, "ancient words", "x")
	add(t, s, "filler one", "x")
	add(t, s, "filler two", "x")

	_, err := s.Resolve("x", "ancient")
	assert.ErrorIs(t, err, ErrUnresolved)
}

func TestReactions(t *testing.T) {
	s := open(t, 10)
	m := add(t, s, "hi", "x")

	require.NoError(t, s.React(m.ID, "h", "👍"))
	require.NoError(t, s.React(m.ID, "h", "👍"), "the same reaction twice is one reaction")
	require.NoError(t, s.React(m.ID, "k", "👍"))
	got, err := s.Get(m.ID)
	require.NoError(t, err)
	assert.Equal(t, []core.Reaction{{By: "h", Emoji: "👍"}, {By: "k", Emoji: "👍"}}, got.Reactions)

	require.NoError(t, s.Unreact(m.ID, "h", "👍"))
	got, _ = s.Get(m.ID)
	assert.Equal(t, []core.Reaction{{By: "k", Emoji: "👍"}}, got.Reactions)

	assert.ErrorIs(t, s.React("m999", "h", "👍"), ErrNotFound)
	assert.ErrorIs(t, s.Unreact("m999", "h", "👍"), ErrNotFound)
}

func TestSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "msgs.db")
	s, err := Open(path, 10)
	require.NoError(t, err)
	a, err := s.Add(core.Message{Text: "persist me"}, "x")
	require.NoError(t, err)
	require.NoError(t, s.Close())

	s, err = Open(path, 10)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	got, err := s.Get(a.ID)
	require.NoError(t, err)
	assert.Equal(t, "persist me", got.Text)
	b, err := s.Add(core.Message{Text: "after"}, "x")
	require.NoError(t, err)
	assert.NotEqual(t, a.ID, b.ID, "ids keep counting across a restart")
}

func TestOpenRejectsABadWindow(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "m.db"), 0)
	assert.Error(t, err)
}

func TestGetScoped(t *testing.T) {
	s := open(t, 10)
	m := add(t, s, "for a only", "a")

	got, err := s.GetScoped("a", m.ID)
	require.NoError(t, err)
	assert.Equal(t, "for a only", got.Text)

	_, err = s.GetScoped("b", m.ID)
	assert.ErrorIs(t, err, ErrNotFound, "out of scope looks like not found")
	_, err = s.GetScoped("a", "m999")
	assert.ErrorIs(t, err, ErrNotFound)
}
