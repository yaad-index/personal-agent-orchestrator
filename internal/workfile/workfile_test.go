package workfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tree makes a working directory with a note, a hidden .git and .claude, and a
// file outside it, and returns the working directory and the outside file.
func tree(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	wd := filepath.Join(base, "work")
	for _, d := range []string{"notes", ".git", ".claude", "sub/.git"} {
		require.NoError(t, os.MkdirAll(filepath.Join(wd, d), 0o755))
	}
	for p, body := range map[string]string{
		"notes/a.md":       "note a",
		"top.txt":          "top",
		".git/config":      "secret config",
		".claude/set.json": "settings",
		"sub/.git/HEAD":    "ref",
		"with space.md":    "spaced",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(wd, p), []byte(body), 0o644))
	}
	outside := filepath.Join(base, "outside.txt")
	require.NoError(t, os.WriteFile(outside, []byte("not yours"), 0o644))
	return wd, outside
}

func TestAFileInsideTheWorkingDirectoryIsRead(t *testing.T) {
	wd, _ := tree(t)
	for path, want := range map[string]string{"notes/a.md": "note a", "top.txt": "top", "./notes/../top.txt": "top", "with space.md": "spaced"} {
		f, err := Read(wd, path, 1<<20)
		require.NoError(t, err, path)
		assert.Equal(t, want, string(f.Data), path)
		assert.Equal(t, path, f.Path)
	}
	f, err := Read(wd, "notes/a.md", 1<<20)
	require.NoError(t, err)
	assert.Equal(t, "a.md", f.Name)
}

func TestEveryOtherPathGetsTheSameRefusal(t *testing.T) {
	wd, outside := tree(t)
	require.NoError(t, os.Symlink(outside, filepath.Join(wd, "link-out")))
	require.NoError(t, os.Symlink(filepath.Join(wd, ".git", "config"), filepath.Join(wd, "link-git")))
	require.NoError(t, os.Symlink(filepath.Dir(outside), filepath.Join(wd, "dir-out")))
	require.NoError(t, os.Symlink(filepath.Join(wd, "notes", "a.md"), filepath.Join(wd, "link-in")))
	for _, path := range []string{
		"missing.md", ".git/config", ".claude/set.json", "sub/.git/HEAD", "../outside.txt",
		"notes/../../outside.txt", outside, "link-out", "link-git", "dir-out/outside.txt", "notes", ".", "", "..",
	} {
		_, err := Read(wd, path, 1<<20)
		assert.ErrorIs(t, err, ErrNoFile, "%q", path)
		assert.Equal(t, ErrNoFile.Error(), err.Error(), "%q: one refusal, saying nothing more", path)
	}
	f, err := Read(wd, "link-in", 1<<20)
	require.NoError(t, err, "a link that stays inside is read")
	assert.Equal(t, "note a", string(f.Data))
}

func TestAFileLargerThanAllowedIsRefusedAsTooLarge(t *testing.T) {
	wd, _ := tree(t)
	require.NoError(t, os.WriteFile(filepath.Join(wd, "big.bin"), []byte(strings.Repeat("x", 101)), 0o644))
	_, err := Read(wd, "big.bin", 100)
	assert.True(t, errors.Is(err, ErrTooLarge))
	assert.Contains(t, err.Error(), "101 bytes, at most 100", "refused from its size, before it is read")
	f, err := Read(wd, "big.bin", 101)
	require.NoError(t, err)
	assert.Len(t, f.Data, 101)
}

func TestAWorkingDirectoryReachedThroughALinkStillConfines(t *testing.T) {
	wd, outside := tree(t)
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(wd, alias))
	f, err := Read(alias, "notes/a.md", 1<<20)
	require.NoError(t, err)
	assert.Equal(t, "note a", string(f.Data))
	_, err = Read(alias, "../"+filepath.Base(outside), 1<<20)
	assert.ErrorIs(t, err, ErrNoFile)
}

func TestAnAbsolutePathIsRefusedEvenWhereItWouldNameAFileInside(t *testing.T) {
	wd, _ := tree(t)
	_, err := Read(wd, "/notes/a.md", 1<<20)
	assert.ErrorIs(t, err, ErrNoFile, "a path is read relative to the working directory, and an absolute one is refused")
}

func TestAHiddenNameIsRefusedEvenWhenItLinksToSomethingReadable(t *testing.T) {
	base := t.TempDir()
	wd := filepath.Join(base, "work")
	require.NoError(t, os.MkdirAll(filepath.Join(wd, "store"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wd, "store", "config"), []byte("x"), 0o644))
	require.NoError(t, os.Symlink("store", filepath.Join(wd, ".git")))
	require.NoError(t, os.Symlink("store", filepath.Join(wd, ".claude")))
	for _, p := range []string{".git/config", ".claude/config"} {
		_, err := Read(wd, p, 1<<20)
		assert.ErrorIs(t, err, ErrNoFile, "%s: the name asked for is checked, not only where it leads", p)
	}
}

func TestResolveAloneConfinesAsReadDoes(t *testing.T) {
	wd, outside := tree(t)
	require.NoError(t, os.Symlink(outside, filepath.Join(wd, "link-out")))
	_, err := Resolve(wd, "link-out")
	assert.ErrorIs(t, err, ErrNoFile, "a link out is refused before anything is opened")
	_, err = Resolve(wd, "notes")
	assert.ErrorIs(t, err, ErrNoFile, "a directory is not a file")
	real, err := Resolve(wd, "notes/a.md")
	require.NoError(t, err)
	assert.Equal(t, "a.md", filepath.Base(real))
}
