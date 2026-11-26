package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/task"
)

const injectedValue = "sk-TEST-INJECTED-VALUE-7731"

// injectNode starts a node with one recipe, a task-agent from which takes
// tasks tagged team, and a secret configured for injection.
func injectNode(t *testing.T, recipe string, optFns ...func(*Options)) *node {
	t.Helper()
	t.Setenv("PAO_TEST_INJECT_SECRET", injectedValue)
	no := false
	return startNodeWith(t, map[string]string{"a": ""}, func(dir, script string, cfg *config.Config) {
		rdir := filepath.Join(dir, "recipes")
		require.NoError(t, os.MkdirAll(rdir, 0o700))
		body := strings.Replace(recipe, "  backend: claude-code\n", "  backend: claude-code\n  account: local\n", 1)
		body = strings.Replace(body, "  tags: [temp]\n", "  tags: [temp, team]\n", 1)
		require.NoError(t, os.WriteFile(filepath.Join(rdir, "fake.yaml"), []byte(strings.ReplaceAll(body, "@@SCRIPT@@", script)), 0o600))
		cfg.Orchestrator.RecipesDir = rdir
		cfg.Orchestrator.Accounts = []config.Account{{Name: "local", Metered: &no}}
		cfg.Orchestrator.Secrets = []config.Secret{{Name: "acme", Env: "PAO_TEST_INJECT_SECRET",
			Inject: &config.SecretInject{Hosts: []string{"api.acme.example"}, Env: "ACME_TOKEN"}}}
		for i := range cfg.Profiles {
			cfg.Profiles[i].Account = "local"
		}
	}, append([]func(*Options){func(o *Options) { o.DispatchEvery = 50 * time.Millisecond }}, optFns...)...)
}

func TestATaskWithASecretToInjectIsRefusedWhereTheNodeCannotConfine(t *testing.T) {
	n := injectNode(t, goodRecipe("fake"))
	ctx := context.Background()
	created, err := n.client.CreateTask(ctx, local.NewTaskJSON{Title: "x", Description: "y", Done: "z", Tags: []string{"team"}, Attachments: task.Attachments{Secret: "acme"}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, err := n.client.Task(ctx, created.ID)
		return err == nil && len(got.Log) > 0 && strings.Contains(got.Log[len(got.Log)-1].Text, `needs secret "acme" injected`)
	}, 15*time.Second, 50*time.Millisecond)
	got, err := n.client.Task(ctx, created.ID)
	require.NoError(t, err)
	assert.Empty(t, got.Assignee)
	assert.Contains(t, got.Log[len(got.Log)-1].Text, "refused rather than run unconfined")
	left, _ := os.ReadDir(filepath.Join(n.stateDir, "run-secrets"))
	assert.Empty(t, left, "an injected secret is never written out as a file")
}

// confinedAgent checks, from inside its pane, what a confined task-agent can
// and cannot reach, and reports it as its first transcript line.
const confinedAgent = `mcp=$(curl -s -o /dev/null -w '%{http_code}' -m 3 -X POST "$PAO_MCP_URL")
[ -S "$PAO_GATE_SOCKET" ] && gate=yes || gate=no
state_dir=$(dirname "$PAO_GATE_SOCKET")
[ -e "$state_dir/grants.json" ] && state=visible || state=hidden
case "$ACME_TOKEN" in pao-placeholder-*) ph=yes ;; *) ph=no ;; esac
curl -s --noproxy '*' -o /dev/null -m 3 https://1.1.1.1 && direct=open || direct=closed
proxy=$(curl -s -o /dev/null -w '%{http_connect}' -m 3 https://unlisted.acme.example)
[ -f README.md ] && readme=yes || readme=no
echo 'READY>'
printf '{"type":"assistant","uuid":"c","message":{"role":"assistant","content":[{"type":"text","text":"checks mcp=%s gate=%s state=%s ph=%s direct=%s proxy=%s readme=%s uid=%s"}]}}\n' "$mcp" "$gate" "$state" "$ph" "$direct" "$proxy" "$readme" "$(id -u)" >> "$TRANSCRIPT"
while IFS= read -r line; do :; done
`

func TestATaskAgentRunsConfinedBehindTheEgressPlane(t *testing.T) {
	if why := kernelForbidsUserNamespaces(); why != "" {
		t.Skipf("unprivileged user namespaces are off here: %s", why)
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	bin := filepath.Join(t.TempDir(), "pao")
	build := exec.Command("go", "build", "-o", bin, "github.com/yaad-index/personal-agent-orchestrator/cmd/pao")
	build.Stderr = os.Stderr
	require.NoError(t, build.Run())

	dir := t.TempDir()
	script := filepath.Join(dir, "confined.sh")
	require.NoError(t, os.WriteFile(script, []byte(confinedAgent), 0o700))
	recipe := strings.Replace(goodRecipe("fake"), "sh @@SCRIPT@@", "sh "+script, 1)
	n := injectNode(t, recipe, func(o *Options) { o.Self = bin })
	require.FileExists(t, filepath.Join(n.stateDir, grantsFile), "the file the agent must not see is there to hide")
	ctx := context.Background()
	created, err := n.client.CreateTask(ctx, local.NewTaskJSON{Title: "x", Description: "y", Done: "z", Tags: []string{"team"}, Attachments: task.Attachments{Secret: "acme"}})
	require.NoError(t, err)
	var got local.TaskJSON
	require.Eventually(t, func() bool {
		got, err = n.client.Task(ctx, created.ID)
		return err == nil && got.Assignee != ""
	}, 20*time.Second, 50*time.Millisecond)
	one := got.Assignee

	m := n.next(t)
	for ; m.Sender != one || !strings.Contains(m.Text, "checks "); m = n.next(t) {
	}
	fields := map[string]string{}
	for _, f := range strings.Fields(strings.TrimPrefix(m.Text, "checks ")) {
		k, v, _ := strings.Cut(f, "=")
		fields[k] = v
	}
	// An unauthenticated call is answered by pao's MCP itself: neither no
	// answer (000) nor the proxy's refusal (403).
	assert.Equal(t, "401", fields["mcp"], "pao's MCP answers through the forward")
	assert.Equal(t, "yes", fields["gate"], "the gate socket is kept")
	assert.Equal(t, "hidden", fields["state"], "pao's state is masked")
	assert.Equal(t, "yes", fields["ph"], "the agent holds a placeholder")
	assert.Equal(t, "closed", fields["direct"], "nothing leaves but through the proxy")
	assert.Equal(t, "403", fields["proxy"], "the bridge reaches this run's own proxy, which refuses an unlisted host")
	assert.Equal(t, "yes", fields["readme"], "its own workdir is kept")
	assert.Equal(t, strconv.Itoa(os.Getuid()), fields["uid"], "it runs as the node user")
	assert.NotContains(t, paneEnv(t, n, one), injectedValue, "never the value")
	assert.DirExists(t, filepath.Join(n.stateDir, "egress", one))

	_, err = n.client.CancelTask(ctx, created.ID, "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, err = n.client.Task(ctx, created.ID)
		return err == nil && got.Removed
	}, 15*time.Second, 50*time.Millisecond)
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(n.stateDir, "egress", one))
		return os.IsNotExist(err)
	}, 5*time.Second, 50*time.Millisecond, "its proxy is closed with it")
}

func kernelForbidsUserNamespaces() string {
	for file, bad := range map[string]string{
		"/proc/sys/kernel/apparmor_restrict_unprivileged_userns": "1",
		"/proc/sys/kernel/unprivileged_userns_clone":             "0",
		"/proc/sys/user/max_user_namespaces":                     "0",
	} {
		if b, err := os.ReadFile(file); err == nil && strings.TrimSpace(string(b)) == bad {
			return fmt.Sprintf("%s is %s", file, bad)
		}
	}
	return ""
}
