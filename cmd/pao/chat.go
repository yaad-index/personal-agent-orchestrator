package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/daemon"
)

// RunCmd runs the node daemon.
type RunCmd struct {
	Path string `arg:"" type:"existingfile" help:"Path to the orchestrator config file."`
}

// Run implements run.
func (c RunCmd) Run() error {
	cfg, err := config.Load(c.Path)
	if err != nil {
		return err
	}
	token := os.Getenv(cfg.Orchestrator.TokenEnv)
	if token == "" {
		return fmt.Errorf("environment variable %s (token_env) is empty: the local channel has no unauthenticated mode", cfg.Orchestrator.TokenEnv)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return daemon.Run(ctx, cfg, token, daemon.Options{})
}

// ChatCmd is a minimal client for the local channel: each line typed is sent
// to the target agent, and everything pao sends comes back as `[sender]: text`.
type ChatCmd struct {
	URL      string `help:"Base URL of the node's local channel." default:"http://127.0.0.1:7744" env:"PAO_URL"`
	TokenEnv string `name:"token-env" help:"Environment variable holding the channel token." default:"PAO_TOKEN"`
	To       string `help:"Agent name to send to." xor:"target"`
	Tag      string `help:"Tag to send to (every agent carrying it)." xor:"target"`
	Sender   string `help:"Sender name shown to the agent." default:"human"`
}

// Run implements chat.
func (c ChatCmd) Run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return c.run(ctx, os.Stdin, os.Stdout, os.Stderr)
}

func (c ChatCmd) run(ctx context.Context, in io.Reader, out, errw io.Writer) error {
	kind, value := "name", c.To
	if c.Tag != "" {
		kind, value = "tag", c.Tag
	}
	if value == "" {
		return fmt.Errorf("give --to <agent> or --tag <tag>")
	}
	token := os.Getenv(c.TokenEnv)
	if token == "" {
		return fmt.Errorf("environment variable %s is empty", c.TokenEnv)
	}
	client := &local.Client{URL: c.URL, Token: token}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	streamDone := make(chan error, 1)
	go func() {
		streamDone <- client.Stream(ctx, func(m local.MessageJSON) {
			_, _ = fmt.Fprintf(out, "[%s]: %s\n", m.Sender, m.Text)
		})
	}()

	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(in)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-streamDone:
			if err != nil {
				return fmt.Errorf("stream: %w", err)
			}
			return nil
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			if decision, id, reason, ok := parseDecision(line); ok {
				if err := client.Decide(ctx, id, decision, reason); err != nil {
					_, _ = fmt.Fprintf(errw, "answer failed: %v\n", err)
				}
				continue
			}
			rep, err := client.Post(ctx, c.Sender, line, kind, value)
			if err != nil {
				_, _ = fmt.Fprintf(errw, "send failed: %v\n", err)
				continue
			}
			for _, r := range rep.Results {
				if r.Error != "" {
					_, _ = fmt.Fprintf(errw, "%s: %s\n", r.Agent, r.Error)
				}
			}
		}
	}
}

// questionID is the id of a gate question: "q3", or "s2.q3" when a node in a mesh
// asked it.
var questionID = regexp.MustCompile(`^([a-z0-9][a-z0-9_-]*\.)?q[0-9]+$`)

// parseDecision recognizes the operator's answers to the gate's questions:
// "/allow q1", "/always q1" and "/deny q1 [reason]". Any other line, including
// one that merely starts with a slash, is a message to the agent.
func parseDecision(line string) (decision, id, reason string, ok bool) {
	f := strings.Fields(line)
	if len(f) < 2 || !questionID.MatchString(f[1]) {
		return "", "", "", false
	}
	switch f[0] {
	case "/allow", "/always":
		if len(f) != 2 {
			return "", "", "", false
		}
		return strings.TrimPrefix(f[0], "/"), f[1], "", true
	case "/deny":
		return "deny", f[1], strings.Join(f[2:], " "), true
	}
	return "", "", "", false
}
