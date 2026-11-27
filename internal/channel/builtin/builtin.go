// Package builtin is the table of channel adapters compiled into this build
// (ADR-0020 point 5). Adding an adapter is one entry here and one package.
package builtin

import (
	"fmt"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/adapter"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/bot"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

// Table returns the adapters this build contains.
func Table() adapter.Table {
	return adapter.Table{"bot": {New: bot.New, Check: bot.CheckOptions}}
}

// CheckChannels checks every configured channel's kind and options against the
// adapters this build contains, as starting the node would. An error names the
// file that defines the channel: the orchestrator config at path, or the file
// pao manages beside it (ADR-0055).
func CheckChannels(path string, o config.Orchestrator) error {
	var own, managed []adapter.Spec
	for _, ch := range o.Channels {
		s := adapter.Spec{ID: ch.ID, Name: ch.Name, Kind: ch.Kind, Options: ch.Options}
		if o.ManagedChannel(ch.Name) {
			managed = append(managed, s)
		} else {
			own = append(own, s)
		}
	}
	if err := Table().Check(own); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := Table().Check(managed); err != nil {
		return fmt.Errorf("%s: %w", o.ManagedPath(), err)
	}
	return nil
}
