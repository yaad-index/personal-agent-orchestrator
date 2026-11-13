package daemon

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mesh"
)

type meshFixture struct {
	ids  map[string]*mesh.Identity
	keys map[string]string
	lns  map[string]net.Listener
	doc  *mesh.Document
}

func newMeshFixture(t *testing.T, names ...string) *meshFixture {
	t.Helper()
	f := &meshFixture{ids: map[string]*mesh.Identity{}, keys: map[string]string{}, lns: map[string]net.Listener{}, doc: &mesh.Document{Master: names[0]}}
	for _, n := range names {
		key := filepath.Join(t.TempDir(), "mesh.key")
		id, err := mesh.Generate(key, n)
		require.NoError(t, err)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		f.ids[n], f.keys[n], f.lns[n] = id, key, ln
		f.doc.Nodes = append(f.doc.Nodes, mesh.NodeEntry{ID: n, Addr: ln.Addr().String(), Fingerprint: id.Fingerprint})
	}
	require.NoError(t, f.doc.Validate())
	t.Cleanup(func() {
		for _, ln := range f.lns {
			_ = ln.Close()
		}
	})
	return f
}

func (f *meshFixture) start(t *testing.T, name string) *node {
	t.Helper()
	return startNodeWith(t, nil, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Node = name
		cfg.Orchestrator.MeshKey = f.keys[name]
		cfg.Orchestrator.MeshListen = f.lns[name].Addr().String()
		cfg.Mesh = f.doc
	}, func(o *Options) { o.MeshListener = f.lns[name] })
}

func TestTwoNodesFindEachOtherAndAgreeWhoIsMaster(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.start(t, "n-a")
	b := f.start(t, "n-b")
	ctx := context.Background()

	require.Eventually(t, func() bool {
		sa, errA := a.client.Mesh(ctx)
		sb, errB := b.client.Mesh(ctx)
		return errA == nil && errB == nil && len(sa.Peers) == 1 && sa.Peers[0].Up && len(sb.Peers) == 1 && sb.Peers[0].Up
	}, 8*time.Second, 50*time.Millisecond, "each node sees the other up")

	sa, err := a.client.Mesh(ctx)
	require.NoError(t, err)
	assert.Equal(t, "n-a", sa.Node)
	assert.Equal(t, "master", sa.Role)
	assert.Equal(t, "n-a", sa.Master)
	assert.Equal(t, "secondary", sa.Peers[0].Role)
	sb, err := b.client.Mesh(ctx)
	require.NoError(t, err)
	assert.Equal(t, "secondary", sb.Role)
	assert.Equal(t, "n-a", sb.Master, "the secondary agrees who the master is")
	assert.Equal(t, "n-b", sb.Node)
}

func TestANodeThatIsNotInAMeshHasNoMeshRoute(t *testing.T) {
	n := startNode(t, map[string]string{"a": ""})
	_, err := n.client.Mesh(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestTheMeshRouteNeedsTheToken(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	a := f.start(t, "n-a")
	_, err := (&local.Client{URL: a.url, Token: "wrong"}).Mesh(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

func bareConfig(t *testing.T, node string) *config.Config {
	t.Helper()
	return &config.Config{Orchestrator: config.Orchestrator{
		Node: node, StateDir: filepath.Join(t.TempDir(), "state"), TmuxSocket: "pao-mesh-test",
		MessageWindow: 100, Level: 1, Gate: config.Gate{AutoAllow: []string{"read"}, AskTimeout: time.Second},
	}}
}

func runFor(t *testing.T, cfg *config.Config) error {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return Run(ctx, cfg, token, Options{Listener: ln})
}

func TestANodeWithoutItsKeyDoesNotStartAndSaysHowToMakeOne(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	cfg := bareConfig(t, "n-a")
	cfg.Orchestrator.MeshKey = filepath.Join(t.TempDir(), "missing.key")
	cfg.Orchestrator.MeshListen = "127.0.0.1:0"
	cfg.Mesh = f.doc

	err := runFor(t, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pao mesh keygen")
}

func TestANodeWhoseKeyIsNotTheOneListedDoesNotStart(t *testing.T) {
	f := newMeshFixture(t, "n-a", "n-b")
	// n-a's key file holds n-b's key: the document lists a different key for n-a.
	cfg := bareConfig(t, "n-a")
	cfg.Orchestrator.MeshKey = f.keys["n-b"]
	cfg.Orchestrator.MeshListen = "127.0.0.1:0"
	cfg.Mesh = f.doc

	err := runFor(t, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not the one the mesh document lists")
}
