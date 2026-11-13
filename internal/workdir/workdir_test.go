package workdir

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newProv(t *testing.T) *Provisioner {
	t.Helper()
	return &Provisioner{Root: filepath.Join(t.TempDir(), "temp")}
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// makeRepo makes a git repository with one commit on main and one on a branch
// named "feature", and returns its path.
func makeRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git("init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("main"), 0o600))
	git("add", ".")
	git("commit", "-q", "-m", "one")
	git("checkout", "-q", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("feature"), 0o600))
	git("add", ".")
	git("commit", "-q", "-m", "two")
	git("checkout", "-q", "main")
	return dir
}

func TestProvisionFilesWritesThemIntoAnIsolatedDir(t *testing.T) {
	p := newProv(t)
	dir, err := p.Provision(context.Background(), "tmp-a", Source{Files: map[string][]byte{
		"README.md":      []byte("hello"),
		"src/deep/x.txt": []byte("x"),
	}})
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(p.Root, "tmp-a"), dir)
	got, err := os.ReadFile(filepath.Join(dir, "src", "deep", "x.txt"))
	require.NoError(t, err)
	assert.Equal(t, "x", string(got))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "an agent's directory is private to the node's user")
}

func TestTwoAgentsGetSeparateDirectories(t *testing.T) {
	p := newProv(t)
	a, err := p.Provision(context.Background(), "a", Source{Files: map[string][]byte{"f": []byte("A")}})
	require.NoError(t, err)
	b, err := p.Provision(context.Background(), "b", Source{Files: map[string][]byte{"f": []byte("B")}})
	require.NoError(t, err)

	assert.NotEqual(t, a, b)
	ga, _ := os.ReadFile(filepath.Join(a, "f"))
	gb, _ := os.ReadFile(filepath.Join(b, "f"))
	assert.Equal(t, "A", string(ga))
	assert.Equal(t, "B", string(gb))
}

func TestProvisionRefusesAnExistingDirAndLeavesItAlone(t *testing.T) {
	p := newProv(t)
	dir, err := p.Provision(context.Background(), "a", Source{Files: map[string][]byte{"keep": []byte("mine")}})
	require.NoError(t, err)

	_, err = p.Provision(context.Background(), "a", Source{Files: map[string][]byte{"other": []byte("x")}})
	require.ErrorIs(t, err, ErrExists)

	assert.Equal(t, []string{"keep"}, entries(t, dir), "the earlier agent's files are untouched, and the failed one's are not added")
	_, err = p.Provision(context.Background(), "a", Source{Files: map[string][]byte{"keep": []byte("x")}})
	require.ErrorIs(t, err, ErrExists)
}

func TestBadAgentNamesNeverTouchThePathOutsideRoot(t *testing.T) {
	p := newProv(t)
	outside := filepath.Join(filepath.Dir(p.Root), "victim")
	require.NoError(t, os.MkdirAll(outside, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "f"), []byte("x"), 0o600))

	for _, name := range []string{"", "../victim", "a/b", "/abs", "A", ".", "..", "a b"} {
		_, err := p.Provision(context.Background(), name, Source{Files: map[string][]byte{"f": []byte("x")}})
		require.ErrorIs(t, err, ErrBadSource, "provision %q", name)
		require.ErrorIs(t, p.Remove(name), ErrBadSource, "remove %q", name)
	}
	assert.Equal(t, []string{"f"}, entries(t, outside), "nothing outside the root was removed")
}

func TestUnsafeFilePathsAreRejectedAndLeaveNothingBehind(t *testing.T) {
	for _, bad := range []string{"", "/etc/x", "../x", "a/../../x", "a/../b", "./a", "a//b", "a/", `a\b`, ".", "..", "a\x00b"} {
		p := newProv(t)
		_, err := p.Provision(context.Background(), "a", Source{Files: map[string][]byte{"ok": []byte("1"), bad: []byte("x")}})
		require.ErrorIs(t, err, ErrBadSource, "path %q", bad)
		assert.Empty(t, entries(t, p.Root), "path %q: the half-made dir is removed", bad)
	}
}

func TestFileAndDirectoryAtOnePathFailCleanly(t *testing.T) {
	p := newProv(t)
	_, err := p.Provision(context.Background(), "a", Source{Files: map[string][]byte{"x": []byte("1"), "x/y": []byte("2")}})
	require.Error(t, err)
	assert.Empty(t, entries(t, p.Root))
}

func TestPushedFilesAreBounded(t *testing.T) {
	p := newProv(t)
	many := make(map[string][]byte)
	for i := 0; i <= MaxFiles; i++ {
		many[fmt.Sprintf("f%d", i)] = nil
	}
	_, err := p.Provision(context.Background(), "a", Source{Files: many})
	require.ErrorIs(t, err, ErrBadSource)

	_, err = p.Provision(context.Background(), "b", Source{Files: map[string][]byte{"big": make([]byte, MaxBytes+1)}})
	require.ErrorIs(t, err, ErrBadSource)

	_, err = p.Provision(context.Background(), "c", Source{Files: map[string][]byte{"edge": make([]byte, MaxBytes)}})
	require.NoError(t, err, "exactly the limit is allowed")
	assert.Equal(t, []string{"c"}, entries(t, p.Root), "the refused ones left nothing")
}

func TestSourceMustBeExactlyOne(t *testing.T) {
	p := newProv(t)
	_, err := p.Provision(context.Background(), "a", Source{})
	require.ErrorIs(t, err, ErrBadSource)
	_, err = p.Provision(context.Background(), "a", Source{Git: &Git{URL: "https://example.com/x.git"}, Files: map[string][]byte{}})
	require.ErrorIs(t, err, ErrBadSource)
	_, err = p.Provision(context.Background(), "a", Source{Git: &Git{}})
	require.ErrorIs(t, err, ErrBadSource)

	dir, err := p.Provision(context.Background(), "a", Source{Files: map[string][]byte{}})
	require.NoError(t, err, "an empty but present file set is a valid, empty workdir")
	assert.Empty(t, entries(t, dir))
}

func TestRemoveDeletesTheDirAndIsIdempotent(t *testing.T) {
	p := newProv(t)
	dir, err := p.Provision(context.Background(), "a", Source{Files: map[string][]byte{"f": []byte("x")}})
	require.NoError(t, err)
	require.NoError(t, p.Remove("a"))
	_, err = os.Stat(dir)
	assert.True(t, os.IsNotExist(err))
	require.NoError(t, p.Remove("a"), "removing one already gone is not an error")
	_, err = p.Provision(context.Background(), "a", Source{Files: map[string][]byte{}})
	require.NoError(t, err, "the name is reusable after removal")
}

func TestGitCloneProvisionsTheRepo(t *testing.T) {
	repo := makeRepo(t)
	p := newProv(t)
	p.AllowLocal = true

	dir, err := p.Provision(context.Background(), "a", Source{Git: &Git{URL: repo}})
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "main", string(got))
	assert.NoFileExists(t, filepath.Join(dir, "b.txt"))
}

func TestGitCloneChecksOutTheRef(t *testing.T) {
	repo := makeRepo(t)
	p := newProv(t)
	p.AllowLocal = true

	dir, err := p.Provision(context.Background(), "a", Source{Git: &Git{URL: repo, Ref: "feature"}})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dir, "b.txt"))
}

func TestLocalGitSourcesAreRefusedByDefault(t *testing.T) {
	repo := makeRepo(t)
	p := newProv(t)
	for _, url := range []string{repo, "file://" + repo} {
		_, err := p.Provision(context.Background(), "a", Source{Git: &Git{URL: url}})
		require.ErrorIs(t, err, ErrBadSource, "url %q", url)
	}
	assert.Empty(t, entries(t, p.Root), "a refused source leaves no directory")
}

func TestGitOptionLookalikesAreRefused(t *testing.T) {
	p := newProv(t)
	p.AllowLocal = true
	_, err := p.Provision(context.Background(), "a", Source{Git: &Git{URL: "--upload-pack=touch /tmp/pwned"}})
	require.ErrorIs(t, err, ErrBadSource)
	_, err = p.Provision(context.Background(), "a", Source{Git: &Git{URL: "https://example.com/x.git", Ref: "--config=x"}})
	require.ErrorIs(t, err, ErrBadSource)
	assert.Empty(t, entries(t, p.Root))
}

func TestFailedCloneLeavesNothingAndHidesThePassword(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	p := newProv(t)
	_, err := p.Provision(context.Background(), "a", Source{Git: &Git{URL: "https://user:hunter2@127.0.0.1:1/x.git"}})
	require.Error(t, err)

	assert.NotContains(t, err.Error(), "hunter2", "a credential in the url must not reach logs or the operator")
	assert.Contains(t, err.Error(), "user@127.0.0.1", "the redacted location is still shown")
	assert.Empty(t, entries(t, p.Root))
}

func TestCancelledContextStopsTheCloneAndCleansUp(t *testing.T) {
	repo := makeRepo(t)
	p := newProv(t)
	p.AllowLocal = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.Provision(ctx, "a", Source{Git: &Git{URL: repo}})
	require.Error(t, err)
	assert.Empty(t, entries(t, p.Root))
}

func TestIsLocal(t *testing.T) {
	local := []string{"/srv/repo", "./repo", "../repo", "repo", "file:///srv/repo", "file:/srv/repo"}
	remote := []string{"https://example.com/x.git", "ssh://git@example.com/x.git", "git@example.com:x/y.git", "git://example.com/x.git"}
	for _, u := range local {
		assert.True(t, isLocal(u), u)
	}
	for _, u := range remote {
		assert.False(t, isLocal(u), u)
	}
}

func TestRedact(t *testing.T) {
	assert.Equal(t, "https://user@example.com/x.git", redact("https://user:pw@example.com/x.git"))
	assert.Equal(t, "https://example.com/x.git", redact("https://example.com/x.git"))
	assert.Equal(t, "git@example.com:x/y.git", redact("git@example.com:x/y.git"))
}
