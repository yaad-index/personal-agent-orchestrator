package local

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeLogs struct {
	calls []string
	err   error
}

func (f *fakeLogs) PauseLog(agent string, pause bool) error {
	word := "resume"
	if pause {
		word = "pause"
	}
	f.calls = append(f.calls, agent+" "+word)
	return f.err
}

func TestTheOperatorPausesAndResumesALogChannel(t *testing.T) {
	f := &fakeLogs{}
	_, ts := newServer(t, &fakeLayer{}, WithLogs(f))
	c := &Client{URL: ts.URL, Token: token}
	ctx := context.Background()
	require.NoError(t, c.PauseLog(ctx, "coder", true))
	require.NoError(t, c.PauseLog(ctx, "coder", false))
	assert.Equal(t, []string{"coder pause", "coder resume"}, f.calls)

	assert.Equal(t, http.StatusUnauthorized, send(t, "PUT", ts.URL+"/v1/agents/coder/log", `{"paused":true}`, nil, nil))
	assert.Equal(t, http.StatusBadRequest, do(t, ts, "PUT", "/v1/agents/coder/log", `not json`, bearer()).StatusCode)
	assert.Len(t, f.calls, 2, "neither reached it")

	f.err = errors.New(`agent "coder" has no log channel`)
	err := c.PauseLog(ctx, "coder", true)
	assert.ErrorContains(t, err, "404")
	assert.ErrorContains(t, err, "has no log channel")
}
