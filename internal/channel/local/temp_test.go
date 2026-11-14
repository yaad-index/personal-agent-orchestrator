package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/harvest"
)

type fakeTemp struct {
	spawnName string
	spawnErr  error
	killRes   KillJSON
	killErr   error
	agents    []AgentJSON
	harvests  []harvest.Manifest
	harvest   harvest.Manifest
	file      []byte
	err       error

	gotSpawn [4]string
	gotNode  string
	gotKill  string
	gotID    string
	gotPath  string
}

func (f *fakeTemp) Spawn(_ context.Context, recipe, name, channel, node string) (string, error) {
	f.gotSpawn = [4]string{recipe, name, channel, node}
	return f.spawnName, f.spawnErr
}
func (f *fakeTemp) Kill(_ context.Context, name string) (KillJSON, error) {
	f.gotKill = name
	return f.killRes, f.killErr
}
func (f *fakeTemp) Agents() []AgentJSON { return f.agents }
func (f *fakeTemp) Harvests(_ context.Context, node string) ([]harvest.Manifest, error) {
	f.gotNode = node
	return f.harvests, f.err
}
func (f *fakeTemp) Harvest(_ context.Context, id string) (harvest.Manifest, error) {
	f.gotID = id
	return f.harvest, f.err
}
func (f *fakeTemp) HarvestFile(_ context.Context, id, path string) ([]byte, error) {
	f.gotID, f.gotPath = id, path
	return f.file, f.err
}
func (f *fakeTemp) DeleteHarvest(_ context.Context, id string) error { f.gotID = id; return f.err }

func tempServer(t *testing.T, f *fakeTemp) (*Client, string) {
	t.Helper()
	_, ts := newServer(t, &fakeLayer{}, WithTemp(f))
	return &Client{URL: ts.URL, Token: token}, ts.URL
}

func TestTempRoutesAreAbsentWithoutAnAPI(t *testing.T) {
	_, ts := newServer(t, &fakeLayer{})
	for _, p := range []string{"/v1/temp", "/v1/harvests"} {
		resp := do(t, ts, "GET", p, "", "Bearer "+token)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, p)
	}
}

func TestTempRoutesNeedTheBearerToken(t *testing.T) {
	_, ts := newServer(t, &fakeLayer{}, WithTemp(&fakeTemp{}))
	for _, c := range [][2]string{
		{"GET", "/v1/temp"}, {"POST", "/v1/temp"}, {"DELETE", "/v1/temp/a"},
		{"GET", "/v1/harvests"}, {"GET", "/v1/harvests/a"}, {"GET", "/v1/harvests/a/file?path=x"}, {"DELETE", "/v1/harvests/a"},
	} {
		for _, auth := range []string{"", "Bearer nope", token} {
			resp := do(t, ts, c[0], c[1], "{}", auth)
			_ = resp.Body.Close()
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "%v with %q", c, auth)
		}
	}
}

func TestSpawnPassesTheRequestAndReturnsTheName(t *testing.T) {
	f := &fakeTemp{spawnName: "tmp-a"}
	c, _ := tempServer(t, f)
	name, err := c.Spawn(context.Background(), "researcher", "tmp-a", "chat", "n-b")
	require.NoError(t, err)
	assert.Equal(t, "tmp-a", name)
	assert.Equal(t, [4]string{"researcher", "tmp-a", "chat", "n-b"}, f.gotSpawn)
}

func TestSpawnWithoutARecipeIsABadRequest(t *testing.T) {
	_, ts := newServer(t, &fakeLayer{}, WithTemp(&fakeTemp{}))
	for _, body := range []string{`{}`, `{"recipe":""}`, `not json`, ``} {
		resp := do(t, ts, "POST", "/v1/temp", body, "Bearer "+token)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, body)
	}
}

func TestErrorsBecomeStatusCodes(t *testing.T) {
	cases := []struct {
		err  error
		code int
	}{
		{ErrNotFound, 404}, {ErrConflict, 409}, {ErrBadRequest, 400}, {ErrForbidden, 403}, {ErrNotReady, 502},
		{errors.New("disk on fire"), 500},
		{fmt.Errorf("wrapped: %w", ErrNotFound), 404},
	}
	for _, c := range cases {
		f := &fakeTemp{spawnErr: c.err, killErr: c.err, err: c.err}
		cl, _ := tempServer(t, f)
		ctx := context.Background()
		_, err := cl.Spawn(ctx, "r", "", "", "")
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), fmt.Sprint(c.code)), "spawn %v: %v", c.err, err)
		_, err = cl.Kill(ctx, "a")
		assert.True(t, strings.HasPrefix(err.Error(), fmt.Sprint(c.code)), "kill %v: %v", c.err, err)
		_, err = cl.Harvest(ctx, "a")
		assert.True(t, strings.HasPrefix(err.Error(), fmt.Sprint(c.code)), "harvest %v: %v", c.err, err)
		_, err = cl.HarvestFile(ctx, "a", "p")
		assert.True(t, strings.HasPrefix(err.Error(), fmt.Sprint(c.code)), "file %v: %v", c.err, err)
		_, err = cl.Harvests(ctx, "")
		assert.True(t, strings.HasPrefix(err.Error(), fmt.Sprint(c.code)), "list %v: %v", c.err, err)
		err = cl.DeleteHarvest(ctx, "a")
		assert.True(t, strings.HasPrefix(err.Error(), fmt.Sprint(c.code)), "delete %v: %v", c.err, err)
	}
}

func TestAnAgentThatIsNotReadyStillReturnsItsName(t *testing.T) {
	f := &fakeTemp{spawnName: "tmp-a", spawnErr: fmt.Errorf("%w: pane text", ErrNotReady)}
	c, _ := tempServer(t, f)
	name, err := c.Spawn(context.Background(), "r", "tmp-a", "", "")
	require.Error(t, err)
	assert.Equal(t, "tmp-a", name)
	assert.True(t, strings.HasPrefix(err.Error(), "502"))
	assert.Contains(t, err.Error(), "pane text")
}

func TestKillAndListAndHarvestRoundTrip(t *testing.T) {
	when := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	f := &fakeTemp{
		killRes:  KillJSON{Harvest: harvest.Manifest{ID: "a-1", Agent: "a", Complete: true}, WorkdirKept: true},
		agents:   []AgentJSON{{Name: "a", Recipe: "r", Channel: "local", State: "running", Flag: "idle", Created: when, LastActivity: when}},
		harvests: []harvest.Manifest{{ID: "a-1", Agent: "a"}},
		harvest:  harvest.Manifest{ID: "a-1", Agent: "a", Files: []harvest.File{{Path: "x", Size: 3}}},
		file:     []byte("raw\x00bytes"),
	}
	c, _ := tempServer(t, f)
	ctx := context.Background()

	res, err := c.Kill(ctx, "a")
	require.NoError(t, err)
	assert.Equal(t, "a", f.gotKill)
	assert.Equal(t, "a-1", res.Harvest.ID)
	assert.True(t, res.WorkdirKept)

	agents, err := c.Agents(ctx)
	require.NoError(t, err)
	assert.Equal(t, f.agents, agents)

	hs, err := c.Harvests(ctx, "n-b")
	require.NoError(t, err)
	require.Len(t, hs, 1)

	m, err := c.Harvest(ctx, "a-1")
	require.NoError(t, err)
	assert.Equal(t, "x", m.Files[0].Path)

	b, err := c.HarvestFile(ctx, "a-1", "dir/with space/x.txt")
	require.NoError(t, err)
	assert.Equal(t, "raw\x00bytes", string(b))
	assert.Equal(t, "dir/with space/x.txt", f.gotPath, "the path survives the query string")

	require.NoError(t, c.DeleteHarvest(ctx, "a-1"))
	assert.Equal(t, "a-1", f.gotID)
}

func TestEmptyListsAreArraysNotNull(t *testing.T) {
	_, ts := newServer(t, &fakeLayer{}, WithTemp(&fakeTemp{}))
	for _, p := range []string{"/v1/temp", "/v1/harvests"} {
		resp := do(t, ts, "GET", p, "", "Bearer "+token)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, "[]", strings.TrimSpace(string(body)), p)
	}
}

func TestAHarvestedFileIsServedAsOpaqueBytes(t *testing.T) {
	f := &fakeTemp{file: []byte("<script>alert(1)</script>")}
	_, ts := newServer(t, &fakeLayer{}, WithTemp(f))
	resp := do(t, ts, "GET", "/v1/harvests/a/file?path=x.html", "", "Bearer "+token)
	_ = resp.Body.Close()
	assert.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"), "agent-written content is never served as a page")
	assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
}

func TestAMountedHandlerIsServedAndAuthenticatesItself(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("mounted:" + r.Header.Get("Authorization")))
	})
	_, ts := newServer(t, &fakeLayer{}, WithMount("/mcp", h))

	resp := do(t, ts, "GET", "/mcp", "", "Bearer any-token-at-all")
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the server's own token check does not stand in front of a mount: it must decide for itself")
	assert.Equal(t, "mounted:Bearer any-token-at-all", string(body))

	resp = do(t, ts, "GET", "/elsewhere", "", "Bearer "+token)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
