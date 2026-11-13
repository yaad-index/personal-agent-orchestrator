package harvest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
)

func write(t *testing.T, root, rel, content string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
	require.NoError(t, os.WriteFile(p, []byte(content), mode))
}

func newStore(t *testing.T, a access.Authorizer, opts ...Option) (*Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "harvest")
	return New(root, a, opts...), root
}

func workdir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "wd")
	require.NoError(t, os.Mkdir(d, 0o700))
	return d
}

func harvest(t *testing.T, s *Store, agent string, tags []string, wd string) Manifest {
	t.Helper()
	m, err := s.Harvest(context.Background(), agent, tags, wd, access.Operator)
	require.NoError(t, err)
	return m
}

func paths(m Manifest) []string {
	var out []string
	for _, f := range m.Files {
		out = append(out, f.Path)
	}
	return out
}

func TestHarvestCopiesTheWorkdirIncludingHiddenAndNested(t *testing.T) {
	wd := workdir(t)
	write(t, wd, "out.txt", "result", 0o600)
	write(t, wd, "deep/er/x.md", "x", 0o600)
	write(t, wd, ".git/HEAD", "ref: refs/heads/main", 0o600)
	write(t, wd, "run.sh", "#!/bin/sh", 0o700)
	s, _ := newStore(t, nil)

	m := harvest(t, s, "tmp-a", []string{"x"}, wd)

	assert.True(t, m.Complete)
	assert.Equal(t, []string{".git/HEAD", "deep/er/x.md", "out.txt", "run.sh"}, paths(m), "unpushed commits live in .git, so it is harvested too")
	assert.Equal(t, int64(len("ref: refs/heads/main")+1+len("result")+len("#!/bin/sh")), m.Bytes)
	got, err := s.Read(access.Operator, m.ID, "deep/er/x.md")
	require.NoError(t, err)
	assert.Equal(t, "x", string(got))
	assert.Equal(t, "operator", m.Requester)
	assert.Equal(t, "tmp-a", m.Agent)
}

func TestHarvestKeepsTheExecutableBit(t *testing.T) {
	wd := workdir(t)
	write(t, wd, "run.sh", "x", 0o755)
	write(t, wd, "data", "x", 0o644)
	s, root := newStore(t, nil)
	m := harvest(t, s, "a", nil, wd)

	sh, err := os.Stat(filepath.Join(root, m.ID, "files", "run.sh"))
	require.NoError(t, err)
	assert.NotZero(t, sh.Mode()&0o100)
	data, err := os.Stat(filepath.Join(root, m.ID, "files", "data"))
	require.NoError(t, err)
	assert.Zero(t, data.Mode()&0o177, "the store is private to the node's user")
}

func TestSymlinksAndSpecialFilesAreNeverFollowedOrCopied(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("do not copy"), 0o600))
	outsideDir := t.TempDir()
	write(t, outsideDir, "inner.txt", "inner secret", 0o600)

	wd := workdir(t)
	write(t, wd, "real.txt", "real", 0o600)
	require.NoError(t, os.Symlink(secret, filepath.Join(wd, "link-file")))
	require.NoError(t, os.Symlink(outsideDir, filepath.Join(wd, "link-dir")))
	require.NoError(t, syscall.Mkfifo(filepath.Join(wd, "pipe"), 0o600))
	s, root := newStore(t, nil)

	m := harvest(t, s, "a", nil, wd)

	assert.Equal(t, []string{"real.txt"}, paths(m))
	assert.True(t, m.Complete, "links are not outputs; they do not make the harvest incomplete")
	assert.Equal(t, 3, m.SkippedTotal)
	reasons := map[string]string{}
	for _, sk := range m.Skipped {
		reasons[sk.Path] = sk.Reason
	}
	assert.Equal(t, map[string]string{"link-file": "symlink", "link-dir": "symlink", "pipe": "not a regular file"}, reasons)

	var copied []string
	_ = filepath.WalkDir(filepath.Join(root, m.ID), func(p string, d os.DirEntry, _ error) error {
		if !d.IsDir() {
			copied = append(copied, filepath.Base(p))
		}
		return nil
	})
	assert.ElementsMatch(t, []string{"real.txt", "manifest.json"}, copied)
}

func TestOverTheLimitIsIncompleteAndSaysSo(t *testing.T) {
	wd := workdir(t)
	for _, n := range []string{"a", "b", "c", "d"} {
		write(t, wd, n, "12345", 0o600)
	}
	s, _ := newStore(t, nil, WithLimits(100, 12))

	m := harvest(t, s, "a", nil, wd)

	assert.False(t, m.Complete)
	assert.Equal(t, []string{"a", "b"}, paths(m), "as much as fits, in order")
	assert.Equal(t, int64(10), m.Bytes)
	assert.Equal(t, []Skip{{Path: "c", Reason: "over limit"}, {Path: "d", Reason: "over limit"}}, m.Skipped)
	assert.Contains(t, m.Summary(), "INCOMPLETE")
}

func TestFileCountLimit(t *testing.T) {
	wd := workdir(t)
	for i := 0; i < 5; i++ {
		write(t, wd, fmt.Sprintf("f%d", i), "x", 0o600)
	}
	s, _ := newStore(t, nil, WithLimits(3, 1<<20))
	m := harvest(t, s, "a", nil, wd)
	assert.False(t, m.Complete)
	assert.Len(t, m.Files, 3)
}

func TestExactlyAtTheLimitIsComplete(t *testing.T) {
	wd := workdir(t)
	write(t, wd, "a", "12345", 0o600)
	write(t, wd, "b", "12345", 0o600)
	s, _ := newStore(t, nil, WithLimits(2, 10))
	m := harvest(t, s, "a", nil, wd)
	assert.True(t, m.Complete)
	assert.Len(t, m.Files, 2)
}

func TestSkippedPathsListedAreBoundedButCounted(t *testing.T) {
	wd := workdir(t)
	for i := 0; i < maxSkipped+10; i++ {
		require.NoError(t, os.Symlink("/nonexistent", filepath.Join(wd, fmt.Sprintf("l%03d", i))))
	}
	s, _ := newStore(t, nil)
	m := harvest(t, s, "a", nil, wd)
	assert.Len(t, m.Skipped, maxSkipped)
	assert.Equal(t, maxSkipped+10, m.SkippedTotal)
}

func TestAFailedHarvestIsNeverListedAndLeavesNoStaging(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read anything")
	}
	wd := workdir(t)
	write(t, wd, "a-ok", "x", 0o600)
	write(t, wd, "z-locked", "x", 0o000)
	s, root := newStore(t, nil)

	_, err := s.Harvest(context.Background(), "a", nil, wd, access.Operator)
	require.Error(t, err, "a file that cannot be read is an error, not a silent gap")

	list, err := s.List(access.Operator)
	require.NoError(t, err)
	assert.Empty(t, list)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, entries, "the staging directory is removed")
}

func TestCancelledContextAbortsCleanly(t *testing.T) {
	wd := workdir(t)
	write(t, wd, "a", "x", 0o600)
	s, root := newStore(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.Harvest(ctx, "a", nil, wd, access.Operator)
	require.Error(t, err)
	entries, _ := os.ReadDir(root)
	assert.Empty(t, entries)
}

func TestHarvestRefusesABadAgentName(t *testing.T) {
	s, root := newStore(t, nil)
	for _, name := range []string{"", "../x", "a/b", "A"} {
		_, err := s.Harvest(context.Background(), name, nil, workdir(t), access.Operator)
		require.Error(t, err, name)
	}
	_, err := os.Stat(root)
	assert.True(t, os.IsNotExist(err), "nothing was created")
}

func TestHarvestOfAMissingWorkdirFails(t *testing.T) {
	s, _ := newStore(t, nil)
	_, err := s.Harvest(context.Background(), "a", nil, filepath.Join(t.TempDir(), "nope"), access.Operator)
	require.Error(t, err, "a workdir that is gone is an error, so a caller never believes it was harvested")
}

func TestTwoHarvestsOfOneAgentGetDistinctIDs(t *testing.T) {
	wd := workdir(t)
	write(t, wd, "a", "x", 0o600)
	fixed := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	s, _ := newStore(t, nil, WithClock(func() time.Time { return fixed }))
	a, b := harvest(t, s, "t", nil, wd), harvest(t, s, "t", nil, wd)
	assert.NotEqual(t, a.ID, b.ID)
	assert.True(t, strings.HasPrefix(a.ID, "t-20261113t120000z-"))
}

func TestTagsAreCopiedNotShared(t *testing.T) {
	wd := workdir(t)
	tags := []string{"x"}
	s, _ := newStore(t, nil)
	m := harvest(t, s, "a", tags, wd)
	tags[0] = "mutated"
	assert.Equal(t, []string{"x"}, m.Tags)
}

func TestRequesterIsRecorded(t *testing.T) {
	wd := workdir(t)
	s, _ := newStore(t, nil)
	m, err := s.Harvest(context.Background(), "a", nil, wd, access.Agent("boss"))
	require.NoError(t, err)
	assert.Equal(t, "agent:boss", m.Requester)
	m, err = s.Harvest(context.Background(), "a", nil, wd, access.Subject{})
	require.NoError(t, err)
	assert.Equal(t, "unknown", m.Requester)
}

// tagReader allows reading harvests whose tags include scope, for one agent.
type tagReader struct{ agent, scope string }

func (r tagReader) Allow(s access.Subject, a access.Action, res access.Resource) bool {
	if s.Kind == access.KindOperator {
		return true
	}
	if s.Kind != access.KindAgent || s.Name != r.agent || a != access.ReadHarvest {
		return false
	}
	for _, t := range res.Tags {
		if t == r.scope {
			return true
		}
	}
	return false
}

func TestDefaultIsOperatorOnly(t *testing.T) {
	wd := workdir(t)
	write(t, wd, "f", "x", 0o600)
	s, _ := newStore(t, nil)
	m := harvest(t, s, "a", []string{"x"}, wd)

	_, err := s.Get(access.Agent("peer"), m.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.Read(access.Agent("peer"), m.ID, "f")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.Get(access.Subject{}, m.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	list, err := s.List(access.Agent("peer"))
	require.NoError(t, err)
	assert.Empty(t, list)

	_, err = s.Get(access.Operator, m.ID)
	assert.NoError(t, err)
}

func TestReadsAreScopedBySlice(t *testing.T) {
	wd := workdir(t)
	write(t, wd, "f", "x", 0o600)
	s, _ := newStore(t, tagReader{agent: "reader", scope: "team"})
	in := harvest(t, s, "in", []string{"team"}, wd)
	out := harvest(t, s, "out", []string{"other"}, wd)

	list, err := s.List(access.Agent("reader"))
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, in.ID, list[0].ID)

	_, err = s.Read(access.Agent("reader"), in.ID, "f")
	assert.NoError(t, err)
	_, err = s.Read(access.Agent("reader"), out.ID, "f")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.Read(access.Agent("stranger"), in.ID, "f")
	assert.ErrorIs(t, err, ErrNotFound)

	all, err := s.List(access.Operator)
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

func TestADeniedReadIsIndistinguishableFromAMissingOne(t *testing.T) {
	wd := workdir(t)
	write(t, wd, "f", "x", 0o600)
	s, _ := newStore(t, nil)
	m := harvest(t, s, "a", nil, wd)
	peer := access.Agent("peer")

	_, denied := s.Get(peer, m.ID)
	_, missing := s.Get(peer, "a-20200101t000000z-000000")
	assert.Equal(t, missing, denied)
	_, deniedRead := s.Read(peer, m.ID, "f")
	_, missingRead := s.Read(peer, "a-20200101t000000z-000000", "f")
	assert.Equal(t, missingRead, deniedRead)
	_, missingPath := s.Read(access.Operator, m.ID, "nope")
	assert.Equal(t, missing, missingPath, "an unlisted path is also just not found")
}

func TestReadCannotReachOutsideTheHarvest(t *testing.T) {
	wd := workdir(t)
	write(t, wd, "f", "x", 0o600)
	s, root := newStore(t, nil)
	m := harvest(t, s, "a", nil, wd)
	write(t, filepath.Dir(root), "outside.txt", "secret", 0o600)

	for _, p := range []string{"../manifest.json", "manifest.json", "../../outside.txt", "/etc/passwd", "f/../f", ""} {
		_, err := s.Read(access.Operator, m.ID, p)
		assert.ErrorIs(t, err, ErrNotFound, "path %q", p)
	}
	// A manifest outside the store that names itself by a traversal id: only
	// the id check stands between Get and it.
	planted := filepath.Join(filepath.Dir(root), "planted")
	require.NoError(t, os.MkdirAll(planted, 0o700))
	write(t, planted, "manifest.json", `{"id":"../planted","agent":"a","files":[]}`, 0o600)

	for _, id := range []string{"../a", "..", ".", "", "a/b", ".partial-x", "../planted"} {
		_, err := s.Get(access.Operator, id)
		assert.ErrorIs(t, err, ErrNotFound, "id %q", id)
	}
}

func TestListIsNewestFirstAndIgnoresStaging(t *testing.T) {
	wd := workdir(t)
	write(t, wd, "f", "x", 0o600)
	clock := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	s, root := newStore(t, nil, WithClock(func() time.Time { clock = clock.Add(time.Minute); return clock }))
	first := harvest(t, s, "a", nil, wd)
	second := harvest(t, s, "b", nil, wd)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".partial-crashed", "files"), 0o700))

	list, err := s.List(access.Operator)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, second.ID, list[0].ID)
	assert.Equal(t, first.ID, list[1].ID)
}

func TestListOnAnEmptyOrMissingStoreIsEmpty(t *testing.T) {
	s, _ := newStore(t, nil)
	list, err := s.List(access.Operator)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestDeleteIsOperatorOnly(t *testing.T) {
	wd := workdir(t)
	write(t, wd, "f", "x", 0o600)
	s, root := newStore(t, tagReader{agent: "reader", scope: "team"})
	m := harvest(t, s, "a", []string{"team"}, wd)

	require.ErrorIs(t, s.Delete(access.Agent("reader"), m.ID), ErrNotFound, "an agent that can read cannot delete")
	assert.DirExists(t, filepath.Join(root, m.ID))

	require.NoError(t, s.Delete(access.Operator, m.ID))
	assert.NoDirExists(t, filepath.Join(root, m.ID))
	assert.ErrorIs(t, s.Delete(access.Operator, m.ID), ErrNotFound)
}

func TestDeleteWithATraversalIDRemovesNothingElse(t *testing.T) {
	s, root := newStore(t, nil)
	require.NoError(t, os.MkdirAll(root, 0o700))
	victim := filepath.Join(filepath.Dir(root), "victim")
	require.NoError(t, os.MkdirAll(victim, 0o700))

	assert.ErrorIs(t, s.Delete(access.Operator, "../victim"), ErrNotFound)
	assert.DirExists(t, victim)
}

func TestSummary(t *testing.T) {
	m := Manifest{ID: "a-1", Agent: "a", Complete: true, Bytes: 7}
	for i := 0; i < 25; i++ {
		m.Files = append(m.Files, File{Path: fmt.Sprintf("f%02d", i), Size: 1})
	}
	got := m.Summary()
	assert.Contains(t, got, `harvested 25 file(s), 7 bytes, from "a" as a-1`)
	assert.Contains(t, got, "f19 (1 bytes)")
	assert.NotContains(t, got, "f20")
	assert.Contains(t, got, "... and 5 more")
	assert.NotContains(t, got, "INCOMPLETE")
}
