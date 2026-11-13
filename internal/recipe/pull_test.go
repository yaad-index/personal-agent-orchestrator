package recipe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeRepo makes a git repository holding files on main, and one more commit
// on a branch "next" that adds extra, and returns its path.
func makeRepo(t *testing.T, files map[string]string, extra map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git("init", "-q", "-b", "main")
	put := func(m map[string]string) {
		for name, content := range m {
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
		}
		git("add", ".")
		git("commit", "-q", "-m", "c")
	}
	put(files)
	if extra != nil {
		git("checkout", "-q", "-b", "next")
		put(extra)
		git("checkout", "-q", "main")
	}
	return dir
}

func named(name string) string {
	return strings.Replace(validRecipe, "name: researcher", "name: "+name, 1)
}

func pull(t *testing.T, dir, url string, mod func(*PullOptions)) ([]string, error) {
	t.Helper()
	o := PullOptions{Dir: dir, URL: url, AllowLocal: true}
	if mod != nil {
		mod(&o)
	}
	return Pull(context.Background(), o)
}

func TestPullInstallsASetAndItsRecipesLoad(t *testing.T) {
	repo := makeRepo(t, map[string]string{"researcher.yaml": validRecipe, "writer.yaml": named("writer"), "README.md": "docs"}, nil)
	dir := filepath.Join(t.TempDir(), "recipes")

	names, err := pull(t, dir, repo, func(o *PullOptions) { o.Set = "team" })
	require.NoError(t, err)

	assert.Equal(t, []string{"researcher", "writer"}, names)
	assert.FileExists(t, filepath.Join(dir, "team", "researcher.yaml"))
	s, err := LoadDir(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"researcher", "writer"}, s.Names())
	assert.NoDirExists(t, filepath.Join(dir, ".pull", "team"), "the clone made aside is gone")
}

func TestPullChecksOutTheRef(t *testing.T) {
	repo := makeRepo(t, map[string]string{"researcher.yaml": validRecipe}, map[string]string{"writer.yaml": named("writer")})
	dir := t.TempDir()
	names, err := pull(t, dir, repo, func(o *PullOptions) { o.Set = "team"; o.Ref = "next" })
	require.NoError(t, err)
	assert.Equal(t, []string{"researcher", "writer"}, names)
}

func TestPullRefusesAnInstalledSetUnlessReplacing(t *testing.T) {
	repo := makeRepo(t, map[string]string{"researcher.yaml": validRecipe}, nil)
	dir := t.TempDir()
	_, err := pull(t, dir, repo, func(o *PullOptions) { o.Set = "team" })
	require.NoError(t, err)

	_, err = pull(t, dir, repo, func(o *PullOptions) { o.Set = "team" })
	require.ErrorIs(t, err, ErrSetExists)
}

func TestReplaceSwapsTheSetInAndItsOwnNamesDoNotConflict(t *testing.T) {
	v1 := makeRepo(t, map[string]string{"researcher.yaml": validRecipe}, nil)
	v2 := makeRepo(t, map[string]string{"researcher.yaml": strings.Replace(validRecipe, "reads and reports", "version two", 1), "writer.yaml": named("writer")}, nil)
	dir := t.TempDir()
	_, err := pull(t, dir, v1, func(o *PullOptions) { o.Set = "team" })
	require.NoError(t, err)

	names, err := pull(t, dir, v2, func(o *PullOptions) { o.Set = "team"; o.Replace = true })
	require.NoError(t, err)

	assert.Equal(t, []string{"researcher", "writer"}, names)
	s, err := LoadDir(dir)
	require.NoError(t, err)
	r, _ := s.Get("researcher")
	assert.Equal(t, "version two", r.Description)
}

func TestAFailedReplaceLeavesTheOldSetAsItWas(t *testing.T) {
	good := makeRepo(t, map[string]string{"researcher.yaml": validRecipe}, nil)
	broken := makeRepo(t, map[string]string{"researcher.yaml": "name: researcher\n"}, nil)
	dir := t.TempDir()
	_, err := pull(t, dir, good, func(o *PullOptions) { o.Set = "team" })
	require.NoError(t, err)

	_, err = pull(t, dir, broken, func(o *PullOptions) { o.Set = "team"; o.Replace = true })
	require.Error(t, err)

	s, err := LoadDir(dir)
	require.NoError(t, err, "the installed set still loads")
	assert.Equal(t, []string{"researcher"}, s.Names())
	r, _ := s.Get("researcher")
	assert.Equal(t, "reads and reports", r.Description)
	assert.NoDirExists(t, filepath.Join(dir, ".pull", "team"))
}

func TestABrokenOrEmptyRepoInstallsNothing(t *testing.T) {
	cases := map[string]map[string]string{
		"broken recipe": {"researcher.yaml": validRecipe, "bad.yaml": "name: bad\n"},
		"no recipes":    {"README.md": "x"},
		"name vs file":  {"other.yaml": validRecipe},
	}
	for name, files := range cases {
		repo := makeRepo(t, files, nil)
		dir := t.TempDir()
		_, err := pull(t, dir, repo, func(o *PullOptions) { o.Set = "team" })
		require.Error(t, err, name)
		assert.NoDirExists(t, filepath.Join(dir, "team"), name)
		s, err := LoadDir(dir)
		require.NoError(t, err, name)
		assert.Empty(t, s.Names(), name)
	}
}

func TestAPulledSetCannotReuseAnInstalledRecipeName(t *testing.T) {
	a := makeRepo(t, map[string]string{"researcher.yaml": validRecipe}, nil)
	b := makeRepo(t, map[string]string{"researcher.yaml": validRecipe, "writer.yaml": named("writer")}, nil)
	dir := t.TempDir()
	_, err := pull(t, dir, a, func(o *PullOptions) { o.Set = "one" })
	require.NoError(t, err)

	_, err = pull(t, dir, b, func(o *PullOptions) { o.Set = "two" })
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"researcher" is already defined`)
	assert.NoDirExists(t, filepath.Join(dir, "two"))
	_, err = LoadDir(dir)
	assert.NoError(t, err, "the installed recipes still load: no ambiguity was let in")
}

func TestAPulledSetCannotReuseALooseRecipeName(t *testing.T) {
	dir := t.TempDir()
	writeRecipe(t, dir, "researcher", validRecipe)
	repo := makeRepo(t, map[string]string{"researcher.yaml": validRecipe}, nil)

	_, err := pull(t, dir, repo, func(o *PullOptions) { o.Set = "team" })
	require.Error(t, err)
	assert.NoDirExists(t, filepath.Join(dir, "team"))
}

func TestPullRefusesALocalRepoByDefault(t *testing.T) {
	repo := makeRepo(t, map[string]string{"researcher.yaml": validRecipe}, nil)
	dir := t.TempDir()
	_, err := pull(t, dir, repo, func(o *PullOptions) { o.Set = "team"; o.AllowLocal = false })
	require.Error(t, err)
	assert.NoDirExists(t, filepath.Join(dir, "team"))
}

func TestPullNeedsAUsableSetName(t *testing.T) {
	repo := makeRepo(t, map[string]string{"researcher.yaml": validRecipe}, nil)
	dir := t.TempDir()
	for _, set := range []string{"../x", "A", "a/b"} {
		_, err := pull(t, dir, repo, func(o *PullOptions) { o.Set = set })
		require.Error(t, err, set)
	}
	_, err := pull(t, dir, "https://example.com/.git", func(o *PullOptions) { o.AllowLocal = false })
	require.Error(t, err, "a url that yields no name asks for one")
	entries, _ := os.ReadDir(dir)
	assert.Empty(t, entries, "nothing was created")
}

func TestPullDerivesTheSetNameFromTheURL(t *testing.T) {
	repo := makeRepo(t, map[string]string{"researcher.yaml": validRecipe}, nil)
	parent := t.TempDir()
	url := filepath.Join(parent, "My Recipes.git")
	require.NoError(t, os.Rename(repo, url))
	dir := t.TempDir()

	_, err := pull(t, dir, url, nil)
	require.NoError(t, err)
	assert.DirExists(t, filepath.Join(dir, "my-recipes"))
}

func TestDefaultSetName(t *testing.T) {
	assert.Equal(t, "my-recipes", DefaultSetName("https://example.com/x/My-Recipes.git"))
	assert.Equal(t, "y", DefaultSetName("git@example.com:x/y.git"))
	assert.Equal(t, "recipes", DefaultSetName("/srv/Recipes/"))
	assert.Equal(t, "a-b", DefaultSetName("https://example.com/a b"))
	assert.Equal(t, "", DefaultSetName("https://example.com/.git"))
}
