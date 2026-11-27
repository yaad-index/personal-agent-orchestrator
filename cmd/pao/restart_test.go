package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
)

// A restart asked of a running node starts it again in the same process, with
// the credentials it started with, though the node took them out of its own
// environment when it started.
func TestARestartedNodeIsTheSameProcessAndStillHasItsToken(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs pao")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "pao")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	require.NoError(t, err, string(out))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "profiles"), 0o755))
	conf := filepath.Join(dir, "pao.yaml")
	require.NoError(t, os.WriteFile(conf, []byte(fmt.Sprintf(
		"node: n1\nprofiles_dir: profiles\nstate_dir: %s\ntoken_env: PAO_RESTART_TEST_TOKEN\nlisten: %s\ntmux_socket: pao-restart-%d\n",
		filepath.Join(dir, "state"), addr, time.Now().UnixNano())), 0o600))

	const token = "restart-test-token"
	cmd := exec.Command(bin, "run", conf)
	cmd.Env = append(os.Environ(), "PAO_RESTART_TEST_TOKEN="+token)
	logs := &lockedLog{}
	cmd.Stdout, cmd.Stderr = logs, logs
	require.NoError(t, cmd.Start())
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
	})

	client := &local.Client{URL: "http://" + addr, Token: token}
	serving := func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := client.Node(ctx)
		return err == nil
	}
	require.Eventually(t, serving, 20*time.Second, 50*time.Millisecond, "the node did not start: %s", logs)

	require.NoError(t, client.RestartNode(context.Background()))
	require.Eventually(t, func() bool { return strings.Contains(logs.String(), "pao restarting") }, 10*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool { return strings.Count(logs.String(), "pao serving") == 2 }, 20*time.Second, 50*time.Millisecond, "it did not start again: %s", logs)
	require.Eventually(t, serving, 10*time.Second, 50*time.Millisecond, "it answers with the token it started with")

	select {
	case err := <-exited:
		t.Fatalf("the process ended (%v) instead of starting again in place: %s", err, logs)
	default:
	}
	assert.Equal(t, http.StatusUnauthorized, func() int {
		req, _ := http.NewRequest("GET", "http://"+addr+"/v1/node", nil)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}(), "still authenticated as before")
}

// lockedLog collects a process's output, read while it is written.
type lockedLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
