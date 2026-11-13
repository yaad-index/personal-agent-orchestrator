// Package daemon wires pao's node together: the registry, the control plane
// over tmux, the router, the message store, the communication layer, the local
// channel, and a mirror that follows each running agent's transcript and sends
// what the agent says to the human (ADR-0001, ADR-0007, ADR-0008, ADR-0009).
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/backend/claudecode"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/control"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mirror"
	"github.com/yaad-index/personal-agent-orchestrator/internal/msgstore"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/route"
	"github.com/yaad-index/personal-agent-orchestrator/internal/tmux"
)

// readers maps a profile's transcript format to its reader. A backend joins
// pao by adding its reader here (ADR-0011).
var readers = map[string]core.TranscriptReader{
	claudecode.Format: claudecode.Reader{},
}

// Options tune a Run; the zero value is the production configuration.
type Options struct {
	// Listener, when set, replaces listening on the configured address.
	Listener net.Listener
	// MirrorPoll is how often transcripts are checked for growth.
	MirrorPoll time.Duration
	// PollInterval is how often startup checks for readiness.
	PollInterval time.Duration
}

// Run serves the node until ctx is done. token is the local channel's bearer
// token.
func Run(ctx context.Context, cfg *config.Config, token string, opts Options) error {
	for _, p := range cfg.Profiles {
		if _, ok := readers[p.Transcript.Format]; !ok {
			return fmt.Errorf("agent %q: unknown transcript format %q", p.Name, p.Transcript.Format)
		}
	}

	reg := registry.New()
	for _, p := range cfg.Profiles {
		if err := reg.Add(p); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(cfg.Orchestrator.StateDir, 0o700); err != nil {
		return err
	}
	store, err := msgstore.Open(filepath.Join(cfg.Orchestrator.StateDir, "messages.db"), cfg.Orchestrator.MessageWindow)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	var ctlOpts []control.Option
	if opts.PollInterval > 0 {
		ctlOpts = append(ctlOpts, control.WithPollInterval(opts.PollInterval))
	}
	gateSock := filepath.Join(cfg.Orchestrator.StateDir, "gate.sock")
	ctlOpts = append(ctlOpts, control.WithEnv(func(agent string) []string {
		return []string{"PAO_AGENT=" + agent, "PAO_GATE_SOCKET=" + gateSock}
	}))
	plane := control.New(tmux.New(tmux.WithSocket(cfg.Orchestrator.TmuxSocket)), ctlOpts...)

	hook := &launchHook{plane: plane}
	router := route.New(reg, hook, plane)
	layer := comm.New(store, router)
	hook.layer = layer
	hook.follow = &followers{
		ctx:   ctx,
		layer: layer,
		level: cfg.Orchestrator.Level,
		poll:  opts.MirrorPoll,
		seen:  make(map[string]bool),
	}

	policy, err := gate.LoadPolicy(filepath.Join(cfg.Orchestrator.StateDir, "policy.json"))
	if err != nil {
		return err
	}
	pending := gate.NewPending(func(ctx context.Context, text string) error {
		_, err := layer.Notify(ctx, core.Message{Sender: "pao", Text: text})
		return err
	})
	autoAllow := make(map[gate.Risk]bool)
	for _, r := range cfg.Orchestrator.Gate.AutoAllow {
		autoAllow[gate.Risk(r)] = true
	}
	gt := &gate.Gate{Policy: policy, Asker: pending, AutoAllow: autoAllow, AskTimeout: cfg.Orchestrator.Gate.AskTimeout}
	gateLn, err := gate.Listen(gateSock)
	if err != nil {
		return err
	}
	gateSrv := &http.Server{Handler: (&gate.Server{Gate: gt}).Handler(), ReadHeaderTimeout: 10 * time.Second}

	srv, err := local.New(layer, token, local.WithDecider(pending))
	if err != nil {
		return err
	}
	layer.AddChannel(srv)

	ln := opts.Listener
	if ln == nil {
		if ln, err = net.Listen("tcp", cfg.Orchestrator.Listen); err != nil {
			return err
		}
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	serveErr := make(chan error, 2)
	go func() { serveErr <- httpSrv.Serve(ln) }()
	go func() { serveErr <- gateSrv.Serve(gateLn) }()
	slog.Info("pao serving", "node", cfg.Orchestrator.Node, "listen", ln.Addr().String(), "agents", len(cfg.Profiles))

	select {
	case err := <-serveErr:
		_ = gateSrv.Close()
		_ = httpSrv.Close()
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Close rather than Shutdown for the gate: its requests wait on the
	// operator, and a question nobody will answer now must not hold the node up.
	_ = gateSrv.Close()
	err = httpSrv.Shutdown(shutdown)
	if errors.Is(err, context.DeadlineExceeded) {
		// A client still holds a stream open; that is not a failure to stop.
		_ = httpSrv.Close()
		err = nil
	}
	hook.follow.wait()
	return err
}

// launchHook is the Launcher the router uses: it launches through the control
// plane, starts following the agent's transcript once it is up, and tells the
// operator, and only the operator, when an agent does not become ready.
type launchHook struct {
	plane  *control.Plane
	layer  *comm.Layer
	follow *followers
}

func (h *launchHook) Launch(ctx context.Context, p core.Profile) error {
	// Follow before launching: the transcript a fresh agent writes then arrives
	// whole (a file that appears after following began is read from its start),
	// instead of losing whatever the agent wrote before the follower caught up.
	h.follow.start(p)
	err := h.plane.Launch(ctx, p)
	if err == nil {
		return nil
	}
	if re, ok := control.AsReadinessError(err); ok {
		text := fmt.Sprintf("agent %q is not ready after %s. Its terminal pane:\n%s", p.Name, re.Timeout, re.Pane())
		if _, nerr := h.layer.Notify(ctx, core.Message{Sender: "pao", Text: text}); nerr != nil {
			slog.Error("could not show the operator the pane", "agent", p.Name, "error", nerr)
		}
	}
	return err
}

// followers runs one mirror per agent, started the first time the agent is
// up, and sends what the agent says to the human.
type followers struct {
	ctx   context.Context
	layer *comm.Layer
	level int
	poll  time.Duration

	mu   sync.Mutex
	seen map[string]bool
	wg   sync.WaitGroup
}

func (f *followers) start(p core.Profile) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen[p.Name] {
		return
	}
	f.seen[p.Name] = true

	m := &mirror.Mirror{
		Reader:  readers[p.Transcript.Format],
		Poll:    f.poll,
		OnError: func(err error) { slog.Warn("transcript line skipped", "agent", p.Name, "error", err) },
	}
	levels := p.EffectiveLevels()
	path := expandPath(p.Transcript.Path)

	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		err := m.Follow(f.ctx, path, mirror.FromEnd, func(e core.Entry) {
			text, ok := render(e, levels, f.level)
			if !ok {
				return
			}
			if _, err := f.layer.Send(f.ctx, p.Name, core.Message{Sender: p.Name, Kind: core.MessageContent, Text: text, Time: e.Time}); err != nil {
				slog.Warn("could not send agent output", "agent", p.Name, "error", err)
			}
		})
		if err != nil {
			slog.Error("transcript follower stopped", "agent", p.Name, "error", err)
		}
	}()
}

func (f *followers) wait() { f.wg.Wait() }

// expandPath resolves a leading ~/ and environment variables in a profile path.
func expandPath(p string) string {
	p = os.ExpandEnv(p)
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return p
}
