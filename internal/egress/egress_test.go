package egress_test

import (
	"log/slog"
	"testing"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/egress"
	"github.com/yaad-index/personal-agent-orchestrator/internal/egress/egresstest"
)

func TestContract(t *testing.T) {
	egresstest.Run(t, func(_ *testing.T, f egresstest.Fixture) core.Connectivity {
		return &egress.Driver{
			Dir:           f.Dir,
			Secrets:       f.Secrets,
			Log:           slog.New(slog.NewTextHandler(f.Log, &slog.HandlerOptions{Level: slog.LevelDebug})),
			Dial:          f.Dial,
			UpstreamRoots: f.UpstreamRoots,
			SystemBundle:  f.SystemBundle,
		}
	})
}
