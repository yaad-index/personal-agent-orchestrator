package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/rejected"
)

func TestANodeServesAnEmptyRejectedRecordToTheOperator(t *testing.T) {
	n := startNode(t, map[string]string{"bee": ""})
	got, err := n.client.Rejected(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

func TestTheRejectedAPIPassesEveryFieldOfAnEntry(t *testing.T) {
	s, err := rejected.Open(filepath.Join(t.TempDir(), "r.db"), rejected.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.Record("chat", "9", "", rejected.Message, "no subject", "hello"))
	time.Sleep(5 * time.Millisecond)
	require.NoError(t, s.Record("chat", "9", "", rejected.Message, "no subject", "hello again"))
	require.NoError(t, s.Record("chat", "8", "", rejected.Registration, "bad secret", "S3CRET"))

	got, err := rejectedAPI{s}.Rejected(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2)
	byS := map[string]local.RejectedJSON{got[0].Sender: got[0], got[1].Sender: got[1]}
	nine := byS["9"]
	assert.Equal(t, [5]any{"chat", "message", "no subject", 2, "hello again"}, [5]any{nine.Channel, nine.Kind, nine.Reason, nine.Count, nine.Payload})
	assert.True(t, nine.First.Before(nine.Last), "first seen is the first record, last seen the second")
	assert.Equal(t, "registration", byS["8"].Kind)
	assert.Empty(t, byS["8"].Payload)
}
