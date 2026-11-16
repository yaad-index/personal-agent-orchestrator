// Package builtin is the table of channel adapters compiled into this build
// (ADR-0020 point 5). Adding an adapter is one entry here and one package.
package builtin

import (
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/adapter"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/bot"
)

// Table returns the adapters this build contains.
func Table() adapter.Table {
	return adapter.Table{"bot": bot.New}
}
