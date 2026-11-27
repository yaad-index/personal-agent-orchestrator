package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// adoption is what pao found running when it started: the agents it took back
// and the sessions it has no record of (ADR-0051 point 2).
type adoption struct {
	// adopted are the configured agents taken back as they were.
	adopted []string
	// tokenless are adopted agents whose token could not be restored: their calls
	// to pao are refused until they are restarted.
	tokenless []string
	// unknown are sessions of agents pao has no record of, left running.
	unknown []string
}

// adopter takes back the configured agents whose sessions outlived the last
// run of pao, without starting them again.
type adopter struct {
	sessions func(ctx context.Context) ([]string, error)
	tokens   interface{ Restore(agent, token string) }
	files    agentTokenFiles
	follow   interface{ start(p core.Profile) }
	running  func(agent string) error
}

func (a adopter) adopt(ctx context.Context, profiles []core.Profile) (adoption, error) {
	live, err := a.sessions(ctx)
	if err != nil {
		return adoption{}, err
	}
	var out adoption
	for _, name := range live {
		i := slices.IndexFunc(profiles, func(p core.Profile) bool { return p.Name == name })
		if i < 0 {
			out.unknown = append(out.unknown, name)
			continue
		}
		p := profiles[i]
		if tok, err := a.files.read(name); err == nil {
			a.tokens.Restore(name, tok)
		} else {
			out.tokenless = append(out.tokenless, name)
		}
		a.follow.start(p)
		if err := a.running(name); err != nil {
			return out, fmt.Errorf("adopt %q: %w", name, err)
		}
		out.adopted = append(out.adopted, name)
	}
	slices.Sort(out.adopted)
	slices.Sort(out.tokenless)
	slices.Sort(out.unknown)
	return out, nil
}

// keep is the agents whose token files stay: the adopted agents that hold one.
func (o adoption) keep() map[string]bool {
	keep := map[string]bool{}
	for _, a := range o.adopted {
		if !slices.Contains(o.tokenless, a) {
			keep[a] = true
		}
	}
	return keep
}

// notice is what the operator is told about the start, or "" when nothing is
// worth telling: an agent taken back with all it needs is the normal case.
func (o adoption) notice(socket string) string {
	var parts []string
	if len(o.tokenless) > 0 {
		parts = append(parts, fmt.Sprintf("Taken back after the restart without its token, so its calls to pao are refused until it is restarted: %s.", strings.Join(o.tokenless, ", ")))
	}
	if len(o.unknown) > 0 {
		parts = append(parts, fmt.Sprintf("Running sessions pao has no record of, left as they are (a temporary agent from before the restart is one): %s. End one with: tmux -L %s kill-session -t <pao-name>", strings.Join(o.unknown, ", "), socket))
	}
	return strings.Join(parts, "\n")
}

func (o adoption) log() {
	if len(o.adopted) > 0 {
		slog.Info("took back agents still running from before", "agents", o.adopted)
	}
	if len(o.tokenless) > 0 {
		slog.Warn("agents taken back without their token", "agents", o.tokenless)
	}
	if len(o.unknown) > 0 {
		slog.Warn("running sessions pao has no record of, left running", "agents", o.unknown)
	}
}
