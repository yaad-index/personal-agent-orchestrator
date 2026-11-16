package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
)

func TestTheOperatorManagesSubjectsAndBindingsOverTheLocalChannel(t *testing.T) {
	n := startNode(t, map[string]string{"bee": ""})
	ctx := context.Background()
	c := n.client

	require.NoError(t, c.AddSubject(ctx, "alice"))
	assert.ErrorContains(t, c.AddSubject(ctx, "alice"), "409")
	assert.ErrorContains(t, c.AddSubject(ctx, "Not Valid"), "400")

	require.NoError(t, c.Bind(ctx, local.BindingJSON{Channel: "chat", Sender: "1001", Subject: "alice"}))
	assert.ErrorContains(t, c.Bind(ctx, local.BindingJSON{Channel: "chat", Sender: "1001", Subject: "alice"}), "409")
	assert.ErrorContains(t, c.Bind(ctx, local.BindingJSON{Channel: "chat", Sender: "1", Subject: "ghost"}), "404")

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
