package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/harvest"
)

// stubNode serves canned answers on the temporary-agent routes and records the
// last request and its token.
type stubNode struct {
	srv   *httptest.Server
	path  string
	body  string
	auth  string
	reply func(w http.ResponseWriter, r *http.Request)
}

func newStubNode(t *testing.T, reply func(w http.ResponseWriter, r *http.Request)) *stubNode {
	t.Helper()
	s := &stubNode{reply: reply}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.path = r.Method + " " + r.URL.RequestURI()
		s.auth = r.Header.Get("Authorization")
		var b bytes.Buffer
		_, _ = b.ReadFrom(r.Body)
		s.body = b.String()
		s.reply(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stubNode) flags(t *testing.T) NodeFlags {
	t.Helper()
	t.Setenv("PAO_TEST_TOKEN", "tok")
	return NodeFlags{URL: s.srv.URL, TokenEnv: "PAO_TEST_TOKEN"}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestSpawnPrintsTheName(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 201, local.SpawnJSON{Name: "tmp-a"}) })
	var out bytes.Buffer
	require.NoError(t, SpawnCmd{NodeFlags: s.flags(t), Recipe: "r", Name: "tmp-a", Channel: "local"}.run(&out))
	assert.Equal(t, "tmp-a\n", out.String())
	assert.Equal(t, "POST /v1/temp", s.path)
	assert.Equal(t, "Bearer tok", s.auth)
	assert.JSONEq(t, `{"recipe":"r","name":"tmp-a","channel":"local"}`, s.body)
}

func TestSpawnThatIsNotReadyStillPrintsTheNameAndFails(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 502, map[string]string{"error": "not ready", "name": "tmp-a"})
	})
	var out bytes.Buffer
	err := SpawnCmd{NodeFlags: s.flags(t), Recipe: "r"}.run(&out)
	require.Error(t, err)
	assert.ErrorContains(t, err, "not ready")
	assert.Equal(t, "tmp-a\n", out.String(), "the name is how the operator finds it to inspect or kill")
}

func TestSpawnFailureWithoutAnAgentPrintsNothing(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 404, map[string]string{"error": "no recipe"})
	})
	var out bytes.Buffer
	require.Error(t, SpawnCmd{NodeFlags: s.flags(t), Recipe: "r"}.run(&out))
	assert.Empty(t, out.String())
}

func TestKillPrintsWhatWasHarvested(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, local.KillJSON{Harvest: harvest.Manifest{ID: "a-1", Agent: "a", Complete: false, Files: []harvest.File{{Path: "x", Size: 1}}, Bytes: 1}, WorkdirKept: true})
	})
	var out bytes.Buffer
	require.NoError(t, KillCmd{NodeFlags: s.flags(t), Name: "a"}.run(&out))
	assert.Equal(t, "DELETE /v1/temp/a", s.path)
	assert.Contains(t, out.String(), "a-1")
	assert.Contains(t, out.String(), "INCOMPLETE")
	assert.Contains(t, out.String(), "workdir was kept")
}

func TestAgentsListsNameFlagStateRecipeChannel(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, []local.AgentJSON{{Name: "a", Recipe: "r", Channel: "local", State: "running", Flag: "idle", Created: time.Now()}})
	})
	var out bytes.Buffer
	require.NoError(t, AgentsCmd{NodeFlags: s.flags(t)}.run(&out))
	assert.Regexp(t, `a\s+idle\s+running\s+r\s+local`, out.String())
}

func TestHarvestsCommands(t *testing.T) {
	m := harvest.Manifest{ID: "a-1", Agent: "a", Complete: true, Requester: "operator", Time: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC), Files: []harvest.File{{Path: "dir/x.txt", Size: 2}}}
	s := newStubNode(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "DELETE":
			w.WriteHeader(204)
		case r.URL.Path == "/v1/harvests" && r.Method == "GET":
			writeJSON(w, 200, []harvest.Manifest{m, {ID: "b-2", Complete: false}})
		case r.URL.Path == "/v1/harvests/a-1":
			writeJSON(w, 200, m)
		case r.URL.Path == "/v1/harvests/a-1/file":
			_, _ = w.Write([]byte("file body"))
		}
	})
	f := s.flags(t)

	var out bytes.Buffer
	require.NoError(t, HarvestsListCmd{NodeFlags: f}.run(&out))
	assert.Regexp(t, `a-1\s+1 file\(s\)\s+complete\s+operator`, out.String())
	assert.Regexp(t, `b-2\s+0 file\(s\)\s+INCOMPLETE`, out.String())

	out.Reset()
	require.NoError(t, HarvestsShowCmd{NodeFlags: f, ID: "a-1"}.run(&out))
	assert.Contains(t, out.String(), "dir/x.txt")
	assert.Contains(t, out.String(), "2026-11-13T12:00:00Z")

	out.Reset()
	require.NoError(t, HarvestsCatCmd{NodeFlags: f, ID: "a-1", Path: "dir/x.txt"}.run(&out))
	assert.Equal(t, "file body", out.String(), "the file's bytes, nothing added")
	assert.Equal(t, "GET /v1/harvests/a-1/file?path=dir%2Fx.txt", s.path)

	require.NoError(t, HarvestsRmCmd{NodeFlags: f, ID: "a-1"}.Run())
	assert.Equal(t, "DELETE /v1/harvests/a-1", s.path)
}

func TestNodeCommandsNeedTheToken(t *testing.T) {
	t.Setenv("PAO_TEST_TOKEN", "")
	f := NodeFlags{URL: "http://127.0.0.1:1", TokenEnv: "PAO_TEST_TOKEN"}
	var out bytes.Buffer
	for name, err := range map[string]error{
		"spawn":   SpawnCmd{NodeFlags: f, Recipe: "r"}.run(&out),
		"kill":    KillCmd{NodeFlags: f, Name: "a"}.run(&out),
		"agents":  AgentsCmd{NodeFlags: f}.run(&out),
		"harvest": HarvestsListCmd{NodeFlags: f}.run(&out),
	} {
		require.Error(t, err, name)
		assert.ErrorContains(t, err, "PAO_TEST_TOKEN", name)
	}
}

func TestKillEscapesTheNameInThePath(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 404, map[string]string{"error": "no"}) })
	var out bytes.Buffer
	_ = KillCmd{NodeFlags: s.flags(t), Name: "../x y"}.run(&out)
	assert.Equal(t, "DELETE /v1/temp/..%2Fx%20y", s.path, "a name cannot add path segments")
}

func TestMeshStatusShowsTheMasterAndEachPeer(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, local.MeshJSON{
			Node: "n-b", Role: "secondary", Epoch: 2, Master: "n-a",
			Peers: []local.MeshPeerJSON{
				{ID: "n-a", Addr: "10.0.0.1:7745", Up: true, Role: "master", Epoch: 2, Master: "n-a", Agents: 3},
				{ID: "n-c", Addr: "10.0.0.3:7745", Up: false, Role: "unknown"},
			},
			Conflicts: []local.MeshConflictJSON{{Name: "dup", Node: "n-c", HeldBy: "n-a"}, {Name: "boss", Node: "n-a"}},
		})
	})
	var out bytes.Buffer
	require.NoError(t, MeshStatusCmd{NodeFlags: s.flags(t)}.run(&out))
	assert.Equal(t, "GET /v1/mesh", s.path)
	assert.Contains(t, out.String(), "node:   n-b (secondary)")
	assert.Contains(t, out.String(), "master: n-a at epoch 2")
	assert.Regexp(t, `n-a\s+up\s+master\s+n-a@2\s+3 agent\(s\)\s+10\.0\.0\.1:7745`, out.String())
	assert.Regexp(t, `n-c\s+DOWN\s+unknown\s+-\s+0 agent\(s\)\s+10\.0\.0\.3:7745`, out.String(), "a peer never heard from is DOWN with no belief")
	assert.Contains(t, out.String(), "name conflicts")
	assert.Contains(t, out.String(), "dup on node n-c: the name is held by node n-a")
	assert.Contains(t, out.String(), "boss on node n-a: the name is held by the master itself")
}

func TestMeshStatusOnANodeNotInAMeshIsAnError(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 404, map[string]string{"error": "Not Found"})
	})
	var out bytes.Buffer
	require.Error(t, MeshStatusCmd{NodeFlags: s.flags(t)}.run(&out))
}
