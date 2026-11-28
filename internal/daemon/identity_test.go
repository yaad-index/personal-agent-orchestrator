package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

func TestTheOperatorManagesSubjectsAndBindingsOverTheLocalChannel(t *testing.T) {
	n := startNodeWith(t, map[string]string{"bee": ""}, func(_, _ string, cfg *config.Config) {
		cfg.Orchestrator.Providers = []config.Provider{{Name: "chat", Kind: "bot", CredentialEnv: "UNUSED"}}
	})
	ctx := context.Background()
	c := n.client

	require.NoError(t, c.AddSubject(ctx, "alice"))
	assert.ErrorContains(t, c.AddSubject(ctx, "alice"), "409")
	assert.ErrorContains(t, c.AddSubject(ctx, "Not Valid"), "400")

	require.NoError(t, c.Bind(ctx, local.BindingJSON{Channel: "chat", Sender: "1001", Subject: "alice"}))
	assert.ErrorContains(t, c.Bind(ctx, local.BindingJSON{Channel: "chat", Sender: "1001", Subject: "alice"}), "409")
	assert.ErrorContains(t, c.Bind(ctx, local.BindingJSON{Channel: "chat", Sender: "1", Subject: "ghost"}), "404")
	err := c.Bind(ctx, local.BindingJSON{Channel: "other", Sender: "1002", Subject: "alice"})
	assert.ErrorContains(t, err, "400", "a binding under a name that is no provider would never be read")
	assert.ErrorContains(t, err, `no provider "other"`)

	bs, err := c.Bindings(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, []local.BindingJSON{{Channel: "chat", Sender: "1001", Subject: "alice"}}, bs)

	require.NoError(t, c.Revoke(ctx, "chat", "1001"))
	assert.ErrorContains(t, c.Revoke(ctx, "chat", "1001"), "404")

	require.NoError(t, c.Bind(ctx, local.BindingJSON{Channel: "chat", Sender: "1001", Subject: "alice"}))
	require.NoError(t, c.RemoveSubject(ctx, "alice"))
	bs, err = c.Bindings(ctx, "")
	require.NoError(t, err)
	assert.Empty(t, bs, "removing a subject takes its bindings")
	assert.ErrorContains(t, c.RemoveSubject(ctx, "alice"), "404")

	require.NoError(t, c.AddSubject(ctx, "bob"))
	require.NoError(t, c.SetOperator(ctx, "bob", true))
	subs, err := c.Subjects(ctx)
	require.NoError(t, err)
	flags := map[string]bool{}
	for _, s := range subs {
		flags[s.Name] = s.Operator
	}
	assert.Equal(t, map[string]bool{"bob": true}, flags)
	assert.ErrorContains(t, c.SetOperator(ctx, "ghost", true), "404")
	require.NoError(t, c.SetOperator(ctx, "bob", false))

	inv, err := c.Invite(ctx, "bob", time.Hour)
	require.NoError(t, err)
	assert.NotEmpty(t, inv.Secret)
	invs, err := c.Invitations(ctx)
	require.NoError(t, err)
	require.Len(t, invs, 1)
	assert.Empty(t, invs[0].Secret, "the list never carries a secret")
	_, err = c.Invite(ctx, "ghost", 0)
	assert.ErrorContains(t, err, "404")
	require.NoError(t, c.RevokeInvitation(ctx, inv.ID))
	assert.ErrorContains(t, c.RevokeInvitation(ctx, inv.ID), "404")
}
