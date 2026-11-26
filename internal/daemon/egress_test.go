package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/egress"
)

func testPlane(t *testing.T) (*egressPlane, config.Orchestrator) {
	t.Helper()
	state := t.TempDir()
	o := config.Orchestrator{StateDir: state, Secrets: []config.Secret{
		{Name: "token", Env: "X", Inject: &config.SecretInject{Hosts: []string{"api.acme.example"}, Env: "ACME_TOKEN"}},
		{Name: "file-key", File: filepath.Join(t.TempDir(), "key")},
	}}
	e := &egressPlane{
		drv: &egress.Driver{Dir: filepath.Join(state, "egress"), Secrets: core.SecretResolverFunc(func(string) ([]byte, error) {
			return []byte("the-real-value"), nil
		}), SystemBundle: writeRoots(t)},
		self:       "/opt/pao/bin/pao",
		mask:       nodeMasks(o),
		keep:       []string{filepath.Join(state, "gate.sock"), filepath.Join(state, "mcp.sock")},
		mcpSock:    filepath.Join(state, "mcp.sock"),
		runSecrets: filepath.Join(state, "run-secrets"),
		pending:    map[string]*core.InjectRule{},
	}
	e.setListen("127.0.0.1:7433")
	return e, o
}

// writeRoots is a stand-in for the system's root bundle.
func writeRoots(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "roots.pem")
	require.NoError(t, os.WriteFile(p, []byte("# no roots\n"), 0o600))
	return p
}

func TestAnAgentThatIsNotATaskAgentIsLaunchedAsBefore(t *testing.T) {
	e, _ := testPlane(t)
	cmd, env, err := e.wrap(core.Profile{Name: "standing"}, "claude --x", []string{"A=1"})
	require.NoError(t, err)
	assert.Equal(t, "claude --x", cmd)
	assert.Equal(t, []string{"A=1"}, env)
}

func TestATaskAgentRunsConfinedBehindItsOwnProxy(t *testing.T) {
	e, o := testPlane(t)
	require.NoError(t, e.register("one-1", injectRule(o, "token")))
	t.Cleanup(func() { _ = e.stop("one-1") })
	workdir := filepath.Join(o.StateDir, "temp", "one-1")
	cmd, env, err := e.wrap(core.Profile{Name: "one-1", Workdir: workdir, Egress: core.EgressProfile{Pass: []string{"api.model.example"}}}, "claude --x", []string{"A=1"})
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(cmd, "exec '/opt/pao/bin/pao' egress-exec --socket "), cmd)
	assert.True(t, strings.HasSuffix(cmd, " -- sh -c 'claude --x'"), cmd)
	for _, want := range []string{
		"--mask '" + o.StateDir + "'",
		"--keep '" + filepath.Join(o.StateDir, "gate.sock") + "'",
		"--keep '" + filepath.Join(o.StateDir, "mcp.sock") + "'",
		"--keep '" + workdir + "'",
		"--keep '" + filepath.Join(o.StateDir, "egress", "one-1") + "'",
		"--forward '127.0.0.1:7433=" + filepath.Join(o.StateDir, "mcp.sock") + "'",
	} {
		assert.Contains(t, cmd, want)
	}
	assert.NotContains(t, cmd, "--keep '"+filepath.Join(o.StateDir, "egress")+"' ", "only the agent's own proxy, not every agent's")

	vars := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	assert.Equal(t, "1", vars["A"])
	assert.Equal(t, "http://"+egress.BridgeAddr, vars["HTTPS_PROXY"])
	require.NotEmpty(t, vars["ACME_TOKEN"])
	assert.NotContains(t, strings.Join(env, "\n"), "the-real-value", "the agent is given a placeholder, never the value")

	require.NoError(t, e.stop("one-1"))
	_, err = os.Stat(filepath.Join(o.StateDir, "egress", "one-1"))
	assert.True(t, errors.Is(err, os.ErrNotExist), "its proxy is gone when its session ends")
	cmd, _, err = e.wrap(core.Profile{Name: "one-1"}, "claude --x", nil)
	require.NoError(t, err)
	assert.Equal(t, "claude --x", cmd, "a stopped agent is no longer registered")
}

func TestATaskAgentWaitsForTheMCPAddress(t *testing.T) {
	e, _ := testPlane(t)
	e.setListen("")
	require.NoError(t, e.register("one-1", nil))
	_, _, err := e.wrap(core.Profile{Name: "one-1"}, "claude", nil)
	assert.Error(t, err)
}

func TestWhereTheNodeCannotConfine(t *testing.T) {
	e, o := testPlane(t)
	e.why = errors.New("no user namespaces")

	err := e.register("one-1", injectRule(o, "token"))
	require.Error(t, err)
	assert.False(t, errors.Is(err, errUnconfined), "a secret to inject is refused, not run unconfined")
	assert.Contains(t, err.Error(), `"token"`)

	err = e.register("one-2", nil)
	assert.ErrorIs(t, err, errUnconfined, "a task-agent with nothing to inject runs, declared unconfined")
	cmd, _, err := e.wrap(core.Profile{Name: "one-2"}, "claude", nil)
	require.NoError(t, err)
	assert.Equal(t, "claude", cmd)
}

func TestAnInjectRuleIsTheSecretsOwnConfiguration(t *testing.T) {
	_, o := testPlane(t)
	r := injectRule(o, "token")
	require.NotNil(t, r)
	assert.Equal(t, core.InjectRule{Secret: "token", Hosts: []string{"api.acme.example"}, Env: "ACME_TOKEN"}, *r)
	assert.Nil(t, injectRule(o, "file-key"), "a secret without inject is given as a file")
	assert.Nil(t, injectRule(o, "nope"))
}

func TestTheNodesMasksHideItsStateAndTheHostsSockets(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/elsewhere")
	t.Setenv("DOCKER_HOST", "unix:///srv/docker.sock")
	t.Setenv("TMUX_TMPDIR", "/var/tmux")
	_, o := testPlane(t)
	m := nodeMasks(o)
	uid := strconv.Itoa(os.Getuid())
	for _, want := range []string{
		o.StateDir, o.Secrets[1].File,
		"/run/user/" + uid, "/run/user/elsewhere",
		"/var/tmux/tmux-" + uid,
		"/var/run/docker.sock", "/run/docker.sock", "/srv/docker.sock",
		"/tmp/agent.sock",
	} {
		assert.Contains(t, m, want)
	}
}
