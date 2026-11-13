package mirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// lineReader turns each line into one message entry; the line BAD is an error.
type lineReader struct{}

func (lineReader) Parse(line []byte) ([]core.Entry, error) {
	if string(line) == "BAD" {
		return nil, errors.New("bad line")
	}
	return []core.Entry{{Kind: core.KindMessage, Text: string(line)}}, nil
}

type sink struct {
	mu   sync.Mutex
	got  []string
	errs []error
}

func (s *sink) emit(e core.Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, e.Text)
}

func (s *sink) onError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, err)
}

func (s *sink) texts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}

func (s *sink) errCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.errs)
}

// follow runs Follow in the background and returns the sink and a stop func
// that cancels it and checks it returned cleanly.
func follow(t *testing.T, path string, start Start) (*sink, func()) {
	t.Helper()
	s := &sink{}
	m := &Mirror{Reader: lineReader{}, Poll: 5 * time.Millisecond, OnError: s.onError}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Follow(ctx, path, start, s.emit) }()
	return s, func() {
		cancel()
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("Follow did not return after cancel")
		}
	}
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(text)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

func eventually(t *testing.T, s *sink, want ...string) {
	t.Helper()
	require.Eventually(t, func() bool {
		return strings.Join(s.texts(), "|") == strings.Join(want, "|")
	}, 3*time.Second, 5*time.Millisecond, "got %v, want %v", s.texts(), want)
}

func TestFollowDeliversAppendedLinesInOrder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	appendTo(t, p, "one\n")
	s, stop := follow(t, p, FromStart)
	defer stop()
	eventually(t, s, "one")

	appendTo(t, p, "two\nthree\n")
	eventually(t, s, "one", "two", "three")
}

func TestFollowFromEndSkipsExistingContent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	appendTo(t, p, "old\n")
	s, stop := follow(t, p, FromEnd)
	defer stop()
	time.Sleep(50 * time.Millisecond) // let Follow take its first look at the file

	appendTo(t, p, "new\n")
	eventually(t, s, "new")
}

func TestFollowWaitsForAFileThatDoesNotExistYet(t *testing.T) {
	p := filepath.Join(t.TempDir(), "later.jsonl")
	s, stop := follow(t, p, FromEnd)
	defer stop()

	time.Sleep(30 * time.Millisecond)
	appendTo(t, p, "hello\n")
	eventually(t, s, "hello")
}

func TestFollowNeverReadsAHalfWrittenLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	s, stop := follow(t, p, FromStart)
	defer stop()

	appendTo(t, p, "par")
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, s.texts(), "no newline yet, so no line")
	assert.Zero(t, s.errCount())

	appendTo(t, p, "tial\n")
	eventually(t, s, "partial")
}

func TestFollowSkipsAnUnparseableLineAndContinues(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	s, stop := follow(t, p, FromStart)
	defer stop()

	appendTo(t, p, "a\nBAD\nb\n")
	eventually(t, s, "a", "b")
	assert.Equal(t, 1, s.errCount())
}

func TestFollowRereadsAFileThatShrank(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	appendTo(t, p, "first line\nsecond line\n")
	s, stop := follow(t, p, FromStart)
	defer stop()
	eventually(t, s, "first line", "second line")

	require.NoError(t, os.WriteFile(p, []byte("new\n"), 0o600))
	eventually(t, s, "first line", "second line", "new")
}

func TestFollowDirectoryMovesToANewerTranscript(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.jsonl")
	appendTo(t, a, "from a\n")
	require.NoError(t, os.Chtimes(a, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)))

	s, stop := follow(t, dir, FromEnd)
	defer stop()
	time.Sleep(30 * time.Millisecond)

	b := filepath.Join(dir, "b.jsonl")
	appendTo(t, b, "from b 1\nfrom b 2\n")
	eventually(t, s, "from b 1", "from b 2")

	appendTo(t, a, "late a\n") // a is no longer the live transcript once b exists
	appendTo(t, b, "from b 3\n")
	eventually(t, s, "from b 1", "from b 2", "from b 3")
}

func TestLocate(t *testing.T) {
	dir := t.TempDir()
	_, err := Locate(dir)
	assert.ErrorIs(t, err, os.ErrNotExist, "an empty directory has no transcript")

	old := filepath.Join(dir, "old.jsonl")
	appendTo(t, old, "x\n")
	require.NoError(t, os.Chtimes(old, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)))
	appendTo(t, filepath.Join(dir, "new.jsonl"), "y\n")
	appendTo(t, filepath.Join(dir, "notes.txt"), "z\n")

	got, err := Locate(dir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "new.jsonl"), got)

	got, err = Locate(old)
	require.NoError(t, err)
	assert.Equal(t, old, got, "a file is its own transcript")

	_, err = Locate(filepath.Join(dir, "missing.jsonl"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestReplayReadsEverythingAndIsLoudOnAFormatChange(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	appendTo(t, p, "a\n\nb\nunfinished")

	got, err := Replay(p, lineReader{})
	require.NoError(t, err)
	var texts []string
	for _, e := range got {
		texts = append(texts, e.Text)
	}
	assert.Equal(t, []string{"a", "b"}, texts, "blank lines and the unfinished tail are not entries")

	appendTo(t, p, "\nBAD\n")
	_, err = Replay(p, lineReader{})
	assert.ErrorContains(t, err, "line 5")
}

func TestFilter(t *testing.T) {
	es := []core.Entry{
		{Kind: core.KindMessage, Text: "m"},
		{Kind: core.KindToolCall, Text: "c"},
		{Kind: core.KindReasoning, Text: "r"},
		{Kind: core.KindMessage, Text: "m2"},
	}
	l := core.DefaultLevels()

	texts := func(n int) []string {
		var out []string
		for _, e := range Filter(es, l, n) {
			out = append(out, e.Text)
		}
		return out
	}
	assert.Equal(t, []string{"m", "m2"}, texts(1))
	assert.Equal(t, []string{"m", "c", "m2"}, texts(2))
	assert.Equal(t, []string{"m", "c", "r", "m2"}, texts(3), "a higher level re-renders the same entries with more shown")
}
