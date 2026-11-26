// Package backend is how a coding-agent harness is brought in to run agents
// (ADR-0011): a backend adapter is compiled in behind one contract, and the
// table of them is one explicit map (package builtin), as channel adapters are.
//
// The adapter decides what an agent loads when it starts (ADR-0025). By default
// an agent's context comes only from its profile and from pao; a profile opens
// a category of the node user's own environment by naming it, and a backend
// whose adapter cannot shut a category out is refused for a profile that does
// not open it.
package backend

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// Category is one part of the node user's environment a backend could load
// (ADR-0025 point 3).
type Category string

// The categories, in the order they are named to an operator.
const (
	// Instructions are user-level and ancestor instruction files.
	Instructions Category = "instructions"
	// MCP is user-scope MCP servers and account connectors.
	MCP Category = "mcp"
	// Skills are the user's skills and commands.
	Skills Category = "skills"
	// Settings are the user's settings files, with the hooks and plugins they
	// enable.
	Settings Category = "settings"
	// Memory is what the backend remembers across sessions on its own.
	Memory Category = "memory"
)

// Categories is every category.
var Categories = []Category{Instructions, MCP, Skills, Settings, Memory}

// Variables the node starts every agent with that an adapter's MCP
// configuration refers to: the address of the node's MCP endpoint and the
// agent's own token for it. The backend reads them when it starts, so the
// command itself carries no secret.
const (
	EnvMCPURL     = "PAO_MCP_URL"
	EnvAgentToken = "PAO_AGENT_TOKEN"
)

// Launch is what the node gives an adapter to start an agent.
type Launch struct {
	// MCPServer is the name the backend gives pao's MCP server, which the
	// gate's rules for pao's tools are written against.
	MCPServer string
	// Hook is the shell command the backend runs before each tool call: pao's
	// gate hook.
	Hook string
	// HookTimeout is how long the backend waits for the hook.
	HookTimeout time.Duration
	// Home is the node user's home directory.
	Home string
}

// Start is how an agent is started: the shell command, and the environment
// entries (KEY=value) it gets besides those the node gives every agent.
type Start struct {
	Command string
	Env     []string
}

// Adapter is one backend compiled into the build.
type Adapter interface {
	// Isolates lists the categories the adapter can shut out.
	Isolates() []Category
	// Owned returns the options the adapter owns that command sets itself.
	Owned(command string) []string
	// Command returns how to start the profile: its startup command with the
	// adapter's options added, and the environment the adapter sets. Together
	// they shut out every category the profile does not inherit and give the
	// agent pao's MCP configuration and gate hook.
	Command(p core.Profile, l Launch) (Start, error)
}

// Table maps a profile's backend name to its adapter.
type Table map[string]Adapter

// Prompt is a question a backend may stop at while it starts, before it is
// ready, and the keys that answer it.
type Prompt struct {
	// About names the question, for the log.
	About string
	// Pattern matches the pane while the question is shown.
	Pattern *regexp.Regexp
	// Keys are the terminal keys that answer it, sent in order.
	Keys []string
	// Tries is how many times the keys may be sent while it stays shown; zero
	// means once. Set it only where the pattern names the state the keys apply
	// in, so a second send can never give another answer.
	Tries int
}

// Prompter is an adapter whose backend may stop at a question on starting that
// pao answers: a backend's own first-run check that no one is at the terminal to
// answer (ADR-0033 point 6). It returns the questions to answer for p; for a
// provisioned profile, only those it keeps safe to answer, such as trusting a
// directory whose settings it shuts out.
type Prompter interface {
	Prompts(p core.Profile) []Prompt
}

// Prompts returns the start-up questions of p's backend to answer for p, none
// when its adapter has none.
func (t Table) Prompts(p core.Profile) []Prompt {
	if a, ok := t[p.Backend].(Prompter); ok {
		return a.Prompts(p)
	}
	return nil
}

// Egresser is an adapter whose backend must reach hosts of its own to run at
// all, such as its model's API. Behind the egress plane (ADR-0049) those hosts
// are passed for every profile on the backend, so an agent is never cut off
// from its own model; any other host a profile reaches it names itself.
type Egresser interface {
	PassHosts(p core.Profile) []string
}

// Pass returns the hosts p reaches untouched behind the egress plane: its
// backend's own, then the ones its profile names, each once.
func (t Table) Pass(p core.Profile) []string {
	var out []string
	if a, ok := t[p.Backend].(Egresser); ok {
		out = append(out, a.PassHosts(p)...)
	}
	for _, h := range p.Egress.Pass {
		if !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

// Inherits reports which categories p opens, and refuses a name that is not a
// category or is named twice.
func Inherits(p core.Profile) (map[Category]bool, error) {
	out := map[Category]bool{}
	for _, name := range p.Inherit {
		c := Category(name)
		if !slices.Contains(Categories, c) {
			return nil, fmt.Errorf("inherit: %q is not a category (have: %s)", name, names(Categories))
		}
		if out[c] {
			return nil, fmt.Errorf("inherit: %q is named twice", name)
		}
		out[c] = true
	}
	return out, nil
}

// Check reports whether p can be started as ADR-0025 requires: its inherit list
// names only categories, its command sets none of the options its backend's
// adapter owns, and the adapter can shut out every category p does not
// inherit. A backend with no adapter in this build can shut out nothing.
func (t Table) Check(p core.Profile) error {
	inherits, err := Inherits(p)
	if err != nil {
		return err
	}
	a, ok := t[p.Backend]
	var can []Category
	if ok {
		can = a.Isolates()
		if owned := a.Owned(p.Startup.Command); len(owned) > 0 {
			return fmt.Errorf("startup.command sets %s, which the %q backend adapter sets itself; remove %s", strings.Join(owned, ", "), p.Backend, pronoun(len(owned)))
		}
	}
	var cannot []Category
	for _, c := range Categories {
		if !inherits[c] && !slices.Contains(can, c) {
			cannot = append(cannot, c)
		}
	}
	if len(cannot) == 0 {
		return nil
	}
	why := fmt.Sprintf("the %q backend adapter cannot shut out", p.Backend)
	if !ok {
		why = fmt.Sprintf("backend %q has no adapter in this build, so pao can shut out nothing; it cannot shut out", p.Backend)
	}
	return fmt.Errorf("%s %s, which the profile does not inherit: add %s to inherit, or use a backend that can", why, names(cannot), pronoun(len(cannot)))
}

// Command returns how to start p: as its adapter says, or for a backend with no
// adapter with its own command, which Check allows only when p inherits every
// category. It checks p first, so a profile that changed since it was loaded is
// not started on a guarantee pao cannot keep.
func (t Table) Command(p core.Profile, l Launch) (Start, error) {
	if err := t.Check(p); err != nil {
		return Start{}, err
	}
	a, ok := t[p.Backend]
	if !ok {
		return Start{Command: p.Startup.Command}, nil
	}
	return a.Command(p, l)
}

// ErrNoHook is a launch with no gate hook to install.
var ErrNoHook = errors.New("no gate hook command to install")

func names(cs []Category) string {
	s := make([]string, len(cs))
	for i, c := range cs {
		s[i] = string(c)
	}
	return strings.Join(s, ", ")
}

func pronoun(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}
