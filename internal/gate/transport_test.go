package gate

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func serve(t *testing.T, g *Gate) string {
	t.Helper()
	// Unix socket paths are short-limited; keep it directly under the temp dir.
	sock := filepath.Join(t.TempDir(), "g.sock")
	ln, err := Listen(sock)
	require.NoError(t, err)
	srv := &http.Server{Handler: (&Server{Gate: g}).Handler(), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func TestAskRoundTripsAllowAndDeny(t *testing.T) {
	g, _ := newGate(t, "", nil, &fakeAsker{ans: Answer{Allow: false, Reason: "nope"}})
	sock := serve(t, g)

	d, err := Ask(context.Background(), sock, req("Read", `{"file_path":"/x"}`))
	require.NoError(t, err)
	assert.True(t, d.Allow)
	assert.Equal(t, "rule", d.Via)

	d, err = Ask(context.Background(), sock, req("Bash", `{"command":"rm -rf /"}`))
	require.NoError(t, err)
	assert.False(t, d.Allow)
	assert.Equal(t, "nope", d.Reason)
	assert.Equal(t, "operator", d.Via)
}

func TestAskBlocksWhileTheOperatorDecides(t *testing.T) {
	asker := &fakeAsker{block: true}
	g, _ := newGate(t, "", nil, asker)
	g.AskTimeout = 2 * time.Second
	sock := serve(t, g)

	done := make(chan Decision, 1)
	go func() {
		d, _ := Ask(context.Background(), sock, req("Bash", `{"command":"x"}`))
		done <- d
	}()
	select {
	case <-done:
		t.Fatal("the hook must stay blocked until the gate decides")
	case <-time.After(150 * time.Millisecond):
	}
	assert.Len(t, asker.asked(), 1)
}

func TestAskFailsClosedWhenTheGateIsDown(t *testing.T) {
	d, err := Ask(context.Background(), filepath.Join(t.TempDir(), "absent.sock"), req("Read", `{}`))
	require.Error(t, err)
	assert.False(t, d.Allow, "an unreachable gate is a denial, even for a read")
	assert.Equal(t, "closed", d.Via)
	assert.Contains(t, d.Reason, "could not be reached")
}

func TestAskFailsClosedOnAnUnreadableAnswer(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "g.sock")
	ln, err := Listen(sock)
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	d, err := Ask(context.Background(), sock, req("Read", `{}`))
	require.Error(t, err)
	assert.False(t, d.Allow)
}

func TestAskFailsClosedOnARefusal(t *testing.T) {
	g, _ := newGate(t, "", nil, nil)
	sock := serve(t, g)

	d, err := Ask(context.Background(), sock, Request{Agent: "", Tool: "Read", Input: json.RawMessage(`{}`)})
	require.Error(t, err)
	assert.False(t, d.Allow, "a request with no agent is refused, not waved through")
}

func TestAskHonoursItsContext(t *testing.T) {
	g, _ := newGate(t, "", nil, &fakeAsker{block: true})
	g.AskTimeout = 5 * time.Second
	sock := serve(t, g)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	d, err := Ask(ctx, sock, req("Bash", `{}`))
	require.Error(t, err)
	assert.False(t, d.Allow)
}

func TestListenSocketIsOwnerOnlyAndReplacesAStaleOne(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "sub", "g.sock")

	ln, err := Listen(sock)
	require.NoError(t, err)
	fi, err := os.Stat(sock)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	di, err := os.Stat(filepath.Dir(sock))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), di.Mode().Perm())

	// A crashed run leaves the socket file behind; the next start replaces it.
	require.NoError(t, ln.(*net.UnixListener).Close())
	_ = os.Remove(sock)
	stale, err := net.Listen("unix", sock)
	require.NoError(t, err)
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, stale.Close())

	ln2, err := Listen(sock)
	require.NoError(t, err, "a stale socket file must not stop the gate from starting")
	require.NoError(t, ln2.Close())
}

func TestListenRefusesToReplaceARegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "g.sock")
	require.NoError(t, os.WriteFile(path, []byte("precious"), 0o600))

	_, err := Listen(path)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "not a socket"))
	b, _ := os.ReadFile(path)
	assert.Equal(t, "precious", string(b))
}
