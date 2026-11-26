package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/egress"
	"github.com/yaad-index/personal-agent-orchestrator/internal/egress/confine"
)

// egressPlane puts every task-agent behind the credential-injection plane
// where the node can confine it (ADR-0049): its pane runs confined, its only
// way out is its own proxy, and a secret configured for injection reaches it
// as a placeholder. Where the node cannot confine, a task with a secret to
// inject is refused and any other task-agent runs unconfined, and says so.
type egressPlane struct {
	drv  *egress.Driver
	self string
	// why is set when the node cannot confine.
	why error
	// mask and keep are the node's paths hidden from, and kept for, every
	// confined agent; listen is pao's MCP address, forwarded to mcpSock.
	mask, keep      []string
	listen, mcpSock string
	runSecrets      string

	// pass gives a profile's pass hosts: its backend's own and its own.
	pass func(core.Profile) []string

	mu      sync.Mutex
	pending map[string]*core.InjectRule
}

// setListen records the address pao's MCP is actually served on, which a
// confined agent's network forwards to mcpSock.
func (e *egressPlane) setListen(addr string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.listen = addr
}

// errUnconfined is returned by register for a task-agent that runs without the
// plane because the node cannot confine it.
var errUnconfined = errors.New("this node cannot confine a task-agent")

// newEgressPlane builds the plane. self is the pao binary a confined pane runs;
// empty means the node does not confine.
func newEgressPlane(o config.Orchestrator, self string, secrets core.SecretResolver, gateSock, mcpSock string, pass func(core.Profile) []string) *egressPlane {
	e := &egressPlane{
		pass:       pass,
		drv:        &egress.Driver{Dir: filepath.Join(o.StateDir, "egress"), Secrets: secrets},
		self:       self,
		mask:       nodeMasks(o),
		keep:       []string{gateSock, mcpSock},
		mcpSock:    mcpSock,
		runSecrets: filepath.Join(o.StateDir, "run-secrets"),
		pending:    map[string]*core.InjectRule{},
	}
	if self == "" {
		e.why = errors.New("no pao binary to confine with")
	} else if err := confine.Available(self); err != nil {
		e.why = err
	}
	return e
}

// nodeMasks are the host paths no confined agent sees: pao's own state, which
// holds the node's key, every agent's workdir and every agent's proxy; the
// files the operator keeps secrets in; and the host sockets that would run a
// command on the host or sign with the user's keys (ADR-0049 point 3).
func nodeMasks(o config.Orchestrator) []string {
	uid := strconv.Itoa(os.Getuid())
	m := []string{o.StateDir}
	for _, s := range o.Secrets {
		if s.File != "" {
			m = append(m, s.File)
		}
	}
	tmuxDir := os.Getenv("TMUX_TMPDIR")
	if tmuxDir == "" {
		tmuxDir = os.TempDir()
	}
	m = append(m,
		filepath.Join("/run/user", uid),
		filepath.Join(tmuxDir, "tmux-"+uid),
		"/var/run/docker.sock", "/run/docker.sock",
		"/run/containerd/containerd.sock",
	)
	for _, v := range []string{"XDG_RUNTIME_DIR", "SSH_AUTH_SOCK"} {
		if p := os.Getenv(v); p != "" {
			m = append(m, p)
		}
	}
	if h, ok := strings.CutPrefix(os.Getenv("DOCKER_HOST"), "unix://"); ok && h != "" {
		m = append(m, h)
	}
	return m
}

// register records agent as a task-agent before it starts, with the secret to
// inject into its calls, if any. Where the node cannot confine, a secret to
// inject is refused and anything else returns errUnconfined: the agent runs
// as before, and the caller says so.
func (e *egressPlane) register(agent string, inject *core.InjectRule) error {
	if e.why != nil {
		if inject != nil {
			return fmt.Errorf("task needs secret %q injected, and %v; it is refused rather than run unconfined", inject.Secret, e.why)
		}
		return fmt.Errorf("%w: %v", errUnconfined, e.why)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pending[agent] = inject
	return nil
}

// wrap returns the pane command and environment for p: unchanged for an agent
// that is not a registered task-agent, confined behind its own proxy for one
// that is.
func (e *egressPlane) wrap(p core.Profile, command string, env []string) (string, []string, error) {
	e.mu.Lock()
	inject, ok := e.pending[p.Name]
	listen := e.listen
	e.mu.Unlock()
	if !ok {
		return command, env, nil
	}
	if listen == "" {
		return "", nil, errors.New("egress: pao's MCP address is not known yet")
	}
	pol := core.EgressPolicy{Pass: e.pass(p)}
	if inject != nil {
		pol.Inject = []core.InjectRule{*inject}
	}
	eg, err := e.drv.Open(context.Background(), p.Name, pol)
	if err != nil {
		return "", nil, err
	}
	spec := confine.Spec{
		Socket:   eg.Socket,
		Mask:     e.mask,
		Keep:     append(append([]string{}, e.keep...), filepath.Dir(eg.Socket), p.Workdir, filepath.Join(e.runSecrets, p.Name)),
		Forwards: []confine.Forward{{Addr: listen, Socket: e.mcpSock}},
	}
	return confine.PaneCommand(e.self, spec, command), append(env, eg.Env...), nil
}

// stop closes the agent's egress when its session ends.
func (e *egressPlane) stop(agent string) error {
	e.mu.Lock()
	delete(e.pending, agent)
	e.mu.Unlock()
	return e.drv.Close(agent)
}

// sweep removes what a previous run of the node left in the egress state.
func (e *egressPlane) sweep() error {
	return os.RemoveAll(e.drv.Dir)
}

// injectRule is the rule for a secret the operator configured for injection,
// or nil for a secret given as a file.
func injectRule(o config.Orchestrator, secret string) *core.InjectRule {
	s, ok := o.Secret(secret)
	if !ok || s.Inject == nil {
		return nil
	}
	return &core.InjectRule{Secret: secret, Hosts: s.Inject.Hosts, Env: s.Inject.Env}
}
