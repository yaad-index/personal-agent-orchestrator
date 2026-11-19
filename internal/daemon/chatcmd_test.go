package daemon

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/command"
	"github.com/yaad-index/personal-agent-orchestrator/internal/mcpsrv"
)

type agentList []mcpsrv.AgentInfo

func (l agentList) Agents() []mcpsrv.AgentInfo { return l }

// listOnly lets the operator list the agents named, and nothing else.
type listOnly map[string]bool

func (m listOnly) Allow(s access.Subject, a access.Action, r access.Resource) bool {
	return s == access.Operator && a == access.List && r.Kind == access.ResourceAgent && m[r.Name]
}

// /pao agents lists what the operator may see under the access matrix; an agent
// out of reach is left out, as if it did not exist (ADR-0012 point 4).
func TestChatAgentsListsOnlyWhatTheOperatorMaySee(t *testing.T) {
	c := chatCommands{
		agents: agentList{
			{Name: "bee", Tags: []string{"ops"}, State: "running"},
			{Name: "hidden", Tags: []string{"secret"}, State: "running"},
			{Name: "wasp", Tags: []string{}, State: "stopped", Node: "n2"},
		},
		auth: listOnly{"bee": true, "wasp": true},
	}
	got, err := c.Agents(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []command.Agent{
		{Name: "bee", Tags: []string{"ops"}, State: "running"},
		{Name: "wasp", Tags: []string{}, State: "stopped"},
	}, got)

	c.auth = nil
	got, err = c.Agents(context.Background())
	require.NoError(t, err)
	assert.Len(t, got, 3, "no authorizer is the operator-only fallback, which lets the operator see all")
}
