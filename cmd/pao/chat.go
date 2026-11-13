package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
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
