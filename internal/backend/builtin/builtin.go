// Package builtin is the table of backend adapters compiled into this build
// (ADR-0011, ADR-0025). Adding an adapter is one entry here and one package.
package builtin

import (
	"github.com/yaad-index/personal-agent-orchestrator/internal/backend"
	"github.com/yaad-index/personal-agent-orchestrator/internal/backend/claudecode"
)

// Table returns the adapters this build contains.
func Table() backend.Table {
	return backend.Table{claudecode.Name: claudecode.Adapter{}}
}
