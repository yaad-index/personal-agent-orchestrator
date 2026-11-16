package identity

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time       { return c.t }
func (c *clock) tick(d time.Duration) { c.t = c.t.Add(d) }

func openClock(t *testing.T) (*Store, *clock) {
	t.Helper()
	s := open(t)
	c := &clock{t: time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)}
	s.now = c.now
	return s, c
}

func TestAnInvitationRegistersASenderToItsSubjectOnce(t *testing.T) {
	s, _ := openClock(t)
	require.NoError(t, s.AddSubject("alice"))
	secret, inv, err := s.Invite("alice", 0)
	require.NoError(t, err)
	assert.Equal(t, "alice", inv.Subject)

	got, ok := s.Register("chat", "1001", secret)
	require.True(t, ok)
	assert.Equal(t, "alice", got)
	bound, ok := s.Resolve("chat", "1001")
	assert.True(t, ok)
	assert.Equal(t, "alice", bound)

	_, ok = s.Register("chat", "2002", secret)
	assert.False(t, ok, "the invitation is single-use")
	_, ok = s.Resolve("chat", "2002")
	assert.False(t, ok)
}

func TestTheSecretIsLongAndTypedLoosely(t *testing.T) {
	s, _ := openClock(t)
	require.NoError(t, s.AddSubject("alice"))
	secret, _, err := s.Invite("alice", 0)
	require.NoError(t, err)
	assert.Len(t, strings.ReplaceAll(secret, "-", ""), 20, "96 bits, past the 80 the ADR asks for")

	loose := " " + strings.ToLower(strings.ReplaceAll(secret, "-", " ")) + " "
	_, ok := s.Register("chat", "1", loose)
	assert.True(t, ok, "case, spaces and dashes do not matter")
}

func TestAWrongSecretRegistersNobody(t *testing.T) {
	s, _ := openClock(t)
	require.NoError(t, s.AddSubject("alice"))
	_, _, err := s.Invite("alice", 0)
	require.NoError(t, err)
	for _, bad := range []string{"", "nope", "AAAA-AAAA-AAAA-AAAA-AAAA"} {
		_, ok := s.Register("chat", "1", bad)
		assert.False(t, ok, bad)
	}
	_, ok := s.Resolve("chat", "1")
	assert.False(t, ok)
}

func TestAnExpiredInvitationRegistersNobody(t *testing.T) {
	s, c := openClock(t)
	require.NoError(t, s.AddSubject("alice"))
	secret, _, err := s.Invite("alice", time.Minute)
	require.NoError(t, err)
	c.tick(time.Minute)
	_, ok := s.Register("chat", "1", secret)
	assert.False(t, ok)
	invs, err := s.Invitations()
	require.NoError(t, err)
	assert.Empty(t, invs)
}

func TestABoundPairPresentingAValidSecretDoesNotConsumeIt(t *testing.T) {
	s, _ := openClock(t)
	require.NoError(t, s.AddSubject("alice"))
	require.NoError(t, s.AddSubject("bob"))
	require.NoError(t, s.Bind("chat", "1", "alice"))
	secret, _, err := s.Invite("bob", 0)
	require.NoError(t, err)

	_, ok := s.Register("chat", "1", secret)
	assert.False(t, ok)
	got, _ := s.Resolve("chat", "1")
	assert.Equal(t, "alice", got, "still alice's")
	_, ok = s.Register("chat", "2", secret)
	assert.True(t, ok, "and the invitation is still there for its holder")
}

func TestAnInvitationIsForAKnownSubjectAndAGoneSubjectCannotBeRegistered(t *testing.T) {
	s, _ := openClock(t)
	_, _, err := s.Invite("ghost", 0)
	assert.ErrorIs(t, err, ErrNoSubject)

	require.NoError(t, s.AddSubject("alice"))
	secret, _, err := s.Invite("alice", 0)
	require.NoError(t, err)
	require.NoError(t, s.RemoveSubject("alice"))
	_, ok := s.Register("chat", "1", secret)
	assert.False(t, ok)
}

func TestAnIllFormedPairCannotRegister(t *testing.T) {
	s, _ := openClock(t)
	require.NoError(t, s.AddSubject("alice"))
	secret, _, err := s.Invite("alice", 0)
	require.NoError(t, err)
	for _, c := range [][2]string{{"", "1"}, {"chat", ""}, {"Bad!", "1"}} {
		_, ok := s.Register(c[0], c[1], secret)
		assert.False(t, ok)
	}
	_, ok := s.Register("chat", "1", secret)
	assert.True(t, ok, "the invitation was not spent on them")
}

func TestOnlyADigestIsKeptSoACopyOfTheStoreRegistersNobody(t *testing.T) {
	path := t.TempDir() + "/id.db"
	s, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s.AddSubject("alice"))
	secret, _, err := s.Invite("alice", 0)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), strings.ReplaceAll(secret, "-", ""))
	assert.NotContains(t, string(raw), secret)
}

func TestInvitationsListWithoutSecretsAndCanBeRevoked(t *testing.T) {
	s, _ := openClock(t)
	require.NoError(t, s.AddSubject("alice"))
	secret, inv, err := s.Invite("alice", 0)
	require.NoError(t, err)
	invs, err := s.Invitations()
	require.NoError(t, err)
	require.Len(t, invs, 1)
	assert.Equal(t, inv, invs[0])
	assert.NotContains(t, fmt.Sprint(invs[0]), strings.ReplaceAll(secret, "-", ""))

	require.NoError(t, s.RevokeInvitation(inv.ID))
	assert.ErrorIs(t, s.RevokeInvitation(inv.ID), ErrNoInvitation)
	_, ok := s.Register("chat", "1", secret)
	assert.False(t, ok)
}

func TestExpiredInvitationsAreSweptWhenAnotherIsIssued(t *testing.T) {
	s, c := openClock(t)
	require.NoError(t, s.AddSubject("alice"))
	_, _, err := s.Invite("alice", time.Minute)
	require.NoError(t, err)
	c.tick(2 * time.Minute)
	_, _, err = s.Invite("alice", time.Hour)
	require.NoError(t, err)
	n := 0
	require.NoError(t, s.db.View(func(tx *bolt.Tx) error { n = tx.Bucket(invites).Stats().KeyN; return nil }))
	assert.Equal(t, 1, n)
}

func TestFiveFailuresInAWindowSilenceAPairEvenForAValidSecret(t *testing.T) {
	s, c := openClock(t)
	require.NoError(t, s.AddSubject("alice"))
	secret, _, err := s.Invite("alice", time.Hour)
	require.NoError(t, err)
	for range DefaultFailures {
		_, ok := s.Register("chat", "1", "wrong")
		require.False(t, ok)
	}
	_, ok := s.Register("chat", "1", secret)
	assert.False(t, ok, "past the limit even the right secret fails")
	_, ok = s.Register("chat", "2", secret)
	assert.True(t, ok, "the limit is per pair: another sender is not held by it")

	secret2, _, err := s.Invite("alice", time.Hour)
	require.NoError(t, err)
	c.tick(DefaultWindow + time.Second)
	_, ok = s.Register("chat", "1", secret2)
	assert.True(t, ok, "and it lifts when the window passes")
}

func TestASuppressedAttemptsDoNotExtendTheirOwnSilence(t *testing.T) {
	s, c := openClock(t)
	require.NoError(t, s.AddSubject("alice"))
	for range DefaultFailures {
		s.Register("chat", "1", "wrong")
	}
	c.tick(DefaultWindow - time.Minute)
	for range 20 {
		s.Register("chat", "1", "wrong")
	}
	c.tick(2 * time.Minute)
	secret, _, err := s.Invite("alice", time.Hour)
	require.NoError(t, err)
	_, ok := s.Register("chat", "1", secret)
	assert.False(t, ok, "failures keep counting inside the window")
	c.tick(DefaultWindow + time.Second)
	_, ok = s.Register("chat", "1", secret)
	assert.True(t, ok)
}

func TestEveryOutcomeDoesTheSameComparisons(t *testing.T) {
	s, c := openClock(t)
	require.NoError(t, s.AddSubject("alice"))
	require.NoError(t, s.AddSubject("bob"))
	require.NoError(t, s.Bind("chat", "bound", "bob"))
	secret, _, err := s.Invite("alice", time.Hour)
	require.NoError(t, err)
	expiring, _, err := s.Invite("alice", time.Second)
	require.NoError(t, err)
	c.tick(time.Minute)
	for range DefaultFailures {
		s.Register("chat", "silenced", "wrong")
	}

	cost := func(channel, sender, sec string) int64 {
		before := s.compares.Load()
		s.Register(channel, sender, sec)
		return s.compares.Load() - before
	}
	want := cost("chat", "stranger", "wrong")
	assert.EqualValues(t, 3, want, "each of the two outstanding invitations and the dummy")
	assert.Equal(t, want, cost("chat", "stranger2", ""), "an empty secret")
	assert.Equal(t, want, cost("chat", "stranger3", expiring), "an expired one")
	assert.Equal(t, want, cost("chat", "silenced", secret), "a silenced pair with the right secret")
	assert.Equal(t, want, cost("chat", "bound", secret), "a bound pair with the right secret")
	assert.Equal(t, want, cost("", "x", secret), "an ill-formed pair")
	assert.Equal(t, want, cost("chat", "fresh", secret), "and the one that succeeds")
}

func TestTheFailureCountersStayBounded(t *testing.T) {
	s, _ := openClock(t)
	for range 50 {
		s.Register("chat", "1", "wrong")
	}
	assert.LessOrEqual(t, len(s.lim.failures["chat\x001"]), DefaultFailures, "one pair keeps at most the limit")

	s.lim.track = 3
	for i := range 10 {
		s.Register("chat", fmt.Sprint("n", i), "wrong")
	}
	assert.LessOrEqual(t, len(s.lim.failures), 3, "and the pairs tracked are capped")
}
