package identity

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "id.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestABoundSenderResolvesToItsSubjectAndAnUnboundOneToNobody(t *testing.T) {
	s := open(t)
	require.NoError(t, s.AddSubject("alice"))
	require.NoError(t, s.Bind("chat", "1001", "alice"))

	got, ok := s.Resolve("chat", "1001")
	assert.True(t, ok)
	assert.Equal(t, "alice", got)

	_, ok = s.Resolve("chat", "1002")
	assert.False(t, ok, "another sender on the same channel is unbound")
	_, ok = s.Resolve("other", "1001")
	assert.False(t, ok, "the same id on another channel is a different pair")
}

func TestAnIllFormedPairResolvesLikeAnUnboundOne(t *testing.T) {
	s := open(t)
	for _, c := range [][2]string{{"", "1"}, {"chat", ""}, {"Chat!", "1"}, {"chat", "a\x00b"}, {"chat", strings.Repeat("x", MaxSenderLen+1)}} {
		got, ok := s.Resolve(c[0], c[1])
		assert.False(t, ok)
		assert.Empty(t, got)
	}
}

func TestOneSubjectIsReachableOnManyChannelsAndOneChannelCarriesManySubjects(t *testing.T) {
	s := open(t)
	require.NoError(t, s.AddSubject("alice"))
	require.NoError(t, s.AddSubject("bob"))
	require.NoError(t, s.Bind("chat", "1", "alice"))
	require.NoError(t, s.Bind("other", "a", "alice"))
	require.NoError(t, s.Bind("chat", "2", "bob"))

	for _, c := range []struct{ ch, id, want string }{{"chat", "1", "alice"}, {"other", "a", "alice"}, {"chat", "2", "bob"}} {
		got, ok := s.Resolve(c.ch, c.id)
		assert.True(t, ok)
		assert.Equal(t, c.want, got)
	}
	bs, err := s.Bindings("alice")
	require.NoError(t, err)
	assert.Len(t, bs, 2)
}

func TestAPairBindsOnceEvenToTheSameSubject(t *testing.T) {
	s := open(t)
	require.NoError(t, s.AddSubject("alice"))
	require.NoError(t, s.AddSubject("bob"))
	require.NoError(t, s.Bind("chat", "1", "alice"))
	assert.ErrorIs(t, s.Bind("chat", "1", "alice"), ErrBound)
	assert.ErrorIs(t, s.Bind("chat", "1", "bob"), ErrBound)
	got, _ := s.Resolve("chat", "1")
	assert.Equal(t, "alice", got)
}

func TestBindingNeedsAKnownSubject(t *testing.T) {
	s := open(t)
	assert.ErrorIs(t, s.Bind("chat", "1", "ghost"), ErrNoSubject)
	_, ok := s.Resolve("chat", "1")
	assert.False(t, ok)
}

func TestRevokingABindingReturnsThePairToUnboundAndLetsItBeBoundAgain(t *testing.T) {
	s := open(t)
	require.NoError(t, s.AddSubject("alice"))
	require.NoError(t, s.AddSubject("bob"))
	require.NoError(t, s.Bind("chat", "1", "alice"))
	require.NoError(t, s.Revoke("chat", "1"))
	_, ok := s.Resolve("chat", "1")
	assert.False(t, ok)
	assert.ErrorIs(t, s.Revoke("chat", "1"), ErrNoBinding)
	require.NoError(t, s.Bind("chat", "1", "bob"))
	got, _ := s.Resolve("chat", "1")
	assert.Equal(t, "bob", got)
}

func TestRemovingASubjectRemovesItsBindingsAndOnlyThose(t *testing.T) {
	s := open(t)
	require.NoError(t, s.AddSubject("alice"))
	require.NoError(t, s.AddSubject("bob"))
	require.NoError(t, s.Bind("chat", "1", "alice"))
	require.NoError(t, s.Bind("other", "a", "alice"))
	require.NoError(t, s.Bind("chat", "2", "bob"))

	require.NoError(t, s.RemoveSubject("alice"))
	_, ok := s.Resolve("chat", "1")
	assert.False(t, ok)
	_, ok = s.Resolve("other", "a")
	assert.False(t, ok)
	got, ok := s.Resolve("chat", "2")
	assert.True(t, ok)
	assert.Equal(t, "bob", got)
	assert.ErrorIs(t, s.RemoveSubject("alice"), ErrNoSubject)
}

func TestSubjectNamesAreValidatedAndUnique(t *testing.T) {
	s := open(t)
	for _, n := range []string{"", "Alice", "-x", "a b", strings.Repeat("a", 33)} {
		assert.ErrorIs(t, s.AddSubject(n), ErrInvalid, n)
	}
	require.NoError(t, s.AddSubject("alice"))
	assert.ErrorIs(t, s.AddSubject("alice"), ErrSubjectExists)
	subs, err := s.Subjects()
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Equal(t, "alice", subs[0].Name)
	assert.False(t, subs[0].Created.IsZero())
}

func TestBindingsSurviveReopening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "id.db")
	s, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s.AddSubject("alice"))
	require.NoError(t, s.Bind("chat", "1", "alice"))
	require.NoError(t, s.Close())

	s, err = Open(path)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	got, ok := s.Resolve("chat", "1")
	assert.True(t, ok)
	assert.Equal(t, "alice", got)
}

func TestAnIllFormedPairCannotBeBoundOrRevoked(t *testing.T) {
	s := open(t)
	require.NoError(t, s.AddSubject("alice"))
	for _, c := range [][2]string{{"", "1"}, {"chat", ""}, {"Chat!", "1"}, {"chat", "a\x00b"}, {"chat", strings.Repeat("x", MaxSenderLen+1)}} {
		assert.ErrorIs(t, s.Bind(c[0], c[1], "alice"), ErrInvalid)
		assert.ErrorIs(t, s.Revoke(c[0], c[1]), ErrInvalid)
	}
	require.NoError(t, s.Bind("chat", strings.Repeat("x", MaxSenderLen), "alice"))
	bs, err := s.Bindings("")
	require.NoError(t, err)
	assert.Len(t, bs, 1)
}
