package rejected

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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

func open(t *testing.T, opt Options) (*Store, *clock) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "r.db"), opt)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	c := &clock{t: time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)}
	s.now = c.now
	return s, c
}

func TestARejectedMessageIsRecordedInFull(t *testing.T) {
	s, c := open(t, Options{})
	require.NoError(t, s.Record("chat", "1001", Message, "no subject", "hello there"))
	es, err := s.List()
	require.NoError(t, err)
	require.Len(t, es, 1)
	assert.Equal(t, Entry{Channel: "chat", Sender: "1001", Kind: Message, Reason: "no subject", First: c.t, Last: c.t, Count: 1, Payload: "hello there"}, es[0])
}

func TestARepeatFromOneSenderUpdatesItsEntryInsteadOfAddingOne(t *testing.T) {
	s, c := open(t, Options{})
	require.NoError(t, s.Record("chat", "1", Message, "no subject", "first"))
	first := c.t
	c.tick(time.Minute)
	require.NoError(t, s.Record("chat", "1", Message, "outside access", "second"))
	es, err := s.List()
	require.NoError(t, err)
	require.Len(t, es, 1)
	assert.Equal(t, 2, es[0].Count)
	assert.Equal(t, first, es[0].First)
	assert.Equal(t, c.t, es[0].Last)
	assert.Equal(t, "outside access", es[0].Reason)
	assert.Equal(t, "second", es[0].Payload)
}

func TestTheSameSenderIdOnAnotherChannelIsAnotherEntry(t *testing.T) {
	s, _ := open(t, Options{})
	require.NoError(t, s.Record("a", "1", Message, "r", "x"))
	require.NoError(t, s.Record("b", "1", Message, "r", "x"))
	es, err := s.List()
	require.NoError(t, err)
	assert.Len(t, es, 2)
}

func TestARegistrationAttemptNeverKeepsWhatItCarried(t *testing.T) {
	s, _ := open(t, Options{})
	require.NoError(t, s.Record("chat", "1", Registration, "bad secret", "SECRET-ABC-123"))
	es, err := s.List()
	require.NoError(t, err)
	require.Len(t, es, 1)
	assert.Equal(t, Registration, es[0].Kind)
	assert.Empty(t, es[0].Payload)
	raw, err := os.ReadFile(s.db.Path())
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "SECRET-ABC-123", "not on disk either")
}

func TestAPayloadIsTruncatedWithoutSplittingACharacter(t *testing.T) {
	s, _ := open(t, Options{MaxPayload: 10})
	require.NoError(t, s.Record("chat", "1", Message, "r", strings.Repeat("a", 50)))
	require.NoError(t, s.Record("chat", "2", Message, "r", strings.Repeat("é", 20)))
	require.NoError(t, s.Record("chat", "3", Message, "r", "a"+strings.Repeat("é", 20)))
	es, err := s.List()
	require.NoError(t, err)
	got := map[string]string{}
	for _, e := range es {
		got[e.Sender] = e.Payload
	}
	assert.Equal(t, strings.Repeat("a", 10), got["1"])
	assert.Equal(t, strings.Repeat("é", 5), got["2"], "10 bytes hold five two-byte characters")
	assert.Equal(t, "a"+strings.Repeat("é", 4), got["3"], "a cut inside a character drops the whole character")
}

func TestAFloodOfDistinctSendersIsCappedAndKeepsTheMostRecent(t *testing.T) {
	s, c := open(t, Options{MaxEntries: 3})
	for i := 1; i <= 6; i++ {
		c.tick(time.Second)
		require.NoError(t, s.Record("chat", fmt.Sprint(i), Message, "r", ""))
	}
	es, err := s.List()
	require.NoError(t, err)
	var got []string
	for _, e := range es {
		got = append(got, e.Sender)
	}
	assert.Equal(t, []string{"6", "5", "4"}, got, "newest first, the oldest were displaced")
}

func TestARepeatAtTheCapDisplacesNobody(t *testing.T) {
	s, c := open(t, Options{MaxEntries: 2})
	require.NoError(t, s.Record("chat", "1", Message, "r", ""))
	c.tick(time.Second)
	require.NoError(t, s.Record("chat", "2", Message, "r", ""))
	c.tick(time.Second)
	require.NoError(t, s.Record("chat", "1", Message, "r", ""))
	es, err := s.List()
	require.NoError(t, err)
	assert.Len(t, es, 2)
}

func TestEntriesAgeOutAfterTheRetentionPeriod(t *testing.T) {
	s, c := open(t, Options{Retention: time.Hour})
	require.NoError(t, s.Record("chat", "old", Message, "r", ""))
	c.tick(30 * time.Minute)
	require.NoError(t, s.Record("chat", "mid", Message, "r", ""))
	c.tick(31 * time.Minute)
	es, err := s.List()
	require.NoError(t, err)
	require.Len(t, es, 1, "listing hides what has aged out before anything prunes it")
	assert.Equal(t, "mid", es[0].Sender)

	require.NoError(t, s.Record("chat", "new", Message, "r", ""))
	c.tick(time.Second)
	n := 0
	require.NoError(t, s.db.View(func(tx *bolt.Tx) error {
		n = tx.Bucket(bucket).Stats().KeyN
		return nil
	}))
	assert.Equal(t, 2, n, "and recording prunes it from disk")
}

func TestARecordWithoutAChannelSenderOrKnownKindIsRefused(t *testing.T) {
	s, _ := open(t, Options{})
	for _, c := range []struct {
		ch, snd string
		k       Kind
	}{{"", "1", Message}, {"chat", "", Message}, {"chat", "1", "other"}, {"a\x00b", "1", Message}} {
		assert.ErrorIs(t, s.Record(c.ch, c.snd, c.k, "r", "p"), ErrInvalid)
	}
	es, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, es)
}

func TestTheRecordSurvivesReopening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.db")
	s, err := Open(path, Options{})
	require.NoError(t, err)
	require.NoError(t, s.Record("chat", "1", Message, "r", "p"))
	require.NoError(t, s.Close())
	s, err = Open(path, Options{})
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	es, err := s.List()
	require.NoError(t, err)
	assert.Len(t, es, 1)
}

// The record is inert (ADR-0020 point 8): nothing in it can reach routing, the
// layer, or an agent, so what it holds cannot become agent input by any path.
func TestThePackageImportsNothingThatRoutesOrDelivers(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		require.NoError(t, err)
		for _, imp := range af.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range []string{"/internal/comm", "/internal/route", "/internal/registry", "/internal/channel", "/internal/daemon", "/internal/control", "/internal/mcpsrv", "/internal/tmux"} {
				assert.False(t, strings.HasSuffix(p, bad), "%s imports %s", f, p)
			}
		}
	}
}
