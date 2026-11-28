package identity

import (
	"os"
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

func TestASubjectIsNotAnOperatorUntilTheOperatorFlagsIt(t *testing.T) {
	s := open(t)
	require.NoError(t, s.AddSubject("alice"))
	require.NoError(t, s.AddSubject("bob"))
	assert.False(t, s.IsOperator("alice"), "created unflagged")

	require.NoError(t, s.SetOperator("alice", true))
	assert.True(t, s.IsOperator("alice"))
	assert.False(t, s.IsOperator("bob"), "and nobody else is")
	subs, err := s.Subjects()
	require.NoError(t, err)
	flags := map[string]bool{}
	for _, sub := range subs {
		flags[sub.Name] = sub.Operator
		assert.False(t, sub.Created.IsZero(), "setting the flag keeps the creation time")
	}
	assert.Equal(t, map[string]bool{"alice": true, "bob": false}, flags)

	require.NoError(t, s.SetOperator("alice", false))
	assert.False(t, s.IsOperator("alice"), "cleared, it takes effect at once")
}

func TestTheOperatorFlagNeedsAnExistingSubjectAndGoesWhenTheSubjectDoes(t *testing.T) {
	s := open(t)
	assert.ErrorIs(t, s.SetOperator("ghost", true), ErrNoSubject)
	assert.False(t, s.IsOperator("ghost"))

	require.NoError(t, s.AddSubject("alice"))
	require.NoError(t, s.SetOperator("alice", true))
	require.NoError(t, s.RemoveSubject("alice"))
	assert.False(t, s.IsOperator("alice"))
	require.NoError(t, s.AddSubject("alice"))
	assert.False(t, s.IsOperator("alice"), "a subject made again under the name is not flagged")
}

func TestRegistrationNeverFlagsAnOperator(t *testing.T) {
	s := open(t)
	require.NoError(t, s.AddSubject("alice"))
	secret, _, err := s.Invite("alice", 0)
	require.NoError(t, err)
	_, ok := s.Register("chat", "1", secret)
	require.True(t, ok)
	assert.False(t, s.IsOperator("alice"))
}

func TestTheFlagSurvivesReopening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "id.db")
	s, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s.AddSubject("alice"))
	require.NoError(t, s.SetOperator("alice", true))
	require.NoError(t, s.Close())
	s, err = Open(path)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	assert.True(t, s.IsOperator("alice"))
}

func TestOperatorsAreTheFlaggedSubjectsSendersOnThatChannelOnly(t *testing.T) {
	s := open(t)
	require.NoError(t, s.AddSubject("alice"))
	require.NoError(t, s.AddSubject("bob"))
	require.NoError(t, s.SetOperator("alice", true))
	require.NoError(t, s.Bind("chat", "1", "alice"))
	require.NoError(t, s.Bind("chat", "2", "bob"))
	require.NoError(t, s.Bind("other", "3", "alice"))

	assert.Equal(t, []string{"1"}, s.Operators("chat"), "bob is bound but not flagged, and other is another channel")
	assert.Equal(t, []string{"3"}, s.Operators("other"))
	assert.Empty(t, s.Operators("none"))

	require.NoError(t, s.SetOperator("alice", false))
	assert.Empty(t, s.Operators("chat"), "cleared, it is read afresh")
}

func rekeySetup(t *testing.T) (*Store, string) {
	t.Helper()
	s := open(t)
	for _, n := range []string{"alice", "bob"} {
		require.NoError(t, s.AddSubject(n))
	}
	require.NoError(t, s.Bind("chat", "100", "alice"))
	require.NoError(t, s.Bind("team", "100", "alice"))
	require.NoError(t, s.Bind("team", "200", "bob"))
	require.NoError(t, s.Bind("elsewhere", "300", "bob"))
	return s, filepath.Join(t.TempDir(), "backup.db")
}

func TestRekeyMovesAChannelsSendersOntoItsProviderAndKeepsACopy(t *testing.T) {
	s, backup := rekeySetup(t)
	n, err := s.Rekey(map[string]string{"chat": "tg", "team": "tg", "same": "same"}, backup)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	for sender, want := range map[string]string{"100": "alice", "200": "bob"} {
		got, ok := s.Resolve("tg", sender)
		assert.True(t, ok, sender)
		assert.Equal(t, want, got, sender)
	}
	_, ok := s.Resolve("chat", "100")
	assert.False(t, ok, "moved, not copied")
	got, ok := s.Resolve("elsewhere", "300")
	assert.True(t, ok, "a name not mapped is left alone")
	assert.Equal(t, "bob", got)

	old, err := Open(backup)
	require.NoError(t, err)
	defer func() { _ = old.Close() }()
	got, ok = old.Resolve("chat", "100")
	assert.True(t, ok, "the copy holds the store from before")
	assert.Equal(t, "alice", got)

	n, err = s.Rekey(map[string]string{"chat": "tg", "team": "tg"}, filepath.Join(t.TempDir(), "again.db"))
	require.NoError(t, err)
	assert.Zero(t, n, "run again, nothing moves")
	_, err = os.Stat(filepath.Join(filepath.Dir(backup), "again.db"))
	assert.True(t, os.IsNotExist(err), "and with nothing to move, no copy is written")
}

func TestRekeyThatWouldBindOneSenderTwiceChangesNothing(t *testing.T) {
	s, backup := rekeySetup(t)
	require.NoError(t, s.Bind("chat", "200", "alice")) // bob on team, alice on chat
	_, err := s.Rekey(map[string]string{"chat": "tg", "team": "tg"}, backup)
	require.ErrorIs(t, err, ErrRekeyConflict)
	assert.Contains(t, err.Error(), `"200"`)
	assert.Contains(t, err.Error(), "alice")
	assert.Contains(t, err.Error(), "bob")
	got, ok := s.Resolve("chat", "200")
	assert.True(t, ok, "nothing moved")
	assert.Equal(t, "alice", got)
	_, ok = s.Resolve("tg", "100")
	assert.False(t, ok)

	require.NoError(t, s.Bind("tg", "100", "bob"))
	require.NoError(t, s.Revoke("chat", "200"))
	_, err = s.Rekey(map[string]string{"chat": "tg"}, backup)
	require.ErrorIs(t, err, ErrRekeyConflict, "one already bound otherwise on the provider")
}

func TestRekeyNeverOverwritesACopy(t *testing.T) {
	s, backup := rekeySetup(t)
	require.NoError(t, os.WriteFile(backup, []byte("an earlier copy"), 0o600))
	_, err := s.Rekey(map[string]string{"chat": "tg"}, backup)
	require.Error(t, err)
	b, rerr := os.ReadFile(backup)
	require.NoError(t, rerr)
	assert.Equal(t, "an earlier copy", string(b))
	_, ok := s.Resolve("chat", "100")
	assert.True(t, ok, "and nothing moved")
}

func TestRekeyOntoANameThatIsItselfMovedChangesNothing(t *testing.T) {
	s, backup := rekeySetup(t)
	// channel chat is carried on team, and channel team on tg: a second run
	// would take what landed on team on to tg
	_, err := s.Rekey(map[string]string{"chat": "team", "team": "tg"}, backup)
	require.ErrorIs(t, err, ErrRekeyChain)
	assert.Contains(t, err.Error(), "chat moves onto team, which itself moves onto tg")
	for name, sender := range map[string]string{"chat": "100", "team": "200"} {
		_, ok := s.Resolve(name, sender)
		assert.True(t, ok, "nothing moved: %s", name)
	}
	_, err = os.Stat(backup)
	assert.True(t, os.IsNotExist(err), "and no copy was written")
}
