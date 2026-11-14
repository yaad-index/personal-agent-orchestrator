package main

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sse(w http.ResponseWriter, events string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(": watching\n\n" + events))
}

func TestWatchAsksForTheLevelAndEntriesAndPrintsThePastThenLive(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		sse(w, "event: line\ndata: {\"text\":\"before\",\"history\":true}\n\n"+
			"event: line\ndata: {\"text\":\"now\"}\n\n"+
			"event: end\ndata: {\"error\":\"the master epoch changed\"}\n\n")
	})
	var out bytes.Buffer
	err := WatchCmd{NodeFlags: s.flags(t), Agent: "bee", Level: 2, Entries: 5}.run(context.Background(), &out)
	assert.Equal(t, "GET /v1/watch/bee?entries=5&level=2", s.path)
	assert.Equal(t, "Bearer tok", s.auth)
	assert.Equal(t, "bee: before\n-- bee, live from here --\nbee: now\n", out.String())
	require.Error(t, err, "the node ended the watch")
	assert.Contains(t, err.Error(), "the master epoch changed")
}

func TestWatchEndsWithoutErrorWhenTheOperatorInterruptsIt(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, "event: line\ndata: {\"text\":\"x\",\"history\":true}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var out bytes.Buffer
	go func() { done <- WatchCmd{NodeFlags: s.flags(t), Agent: "bee", Level: 1}.run(ctx, &out) }()
	time.Sleep(300 * time.Millisecond)
	stop()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the watch did not stop")
	}
}

func TestAWatchRefusalIsReportedWithItsStatus(t *testing.T) {
	s := newStubNode(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 404, map[string]string{"error": "no agent"})
	})
	err := WatchCmd{NodeFlags: s.flags(t), Agent: "ghost", Level: 1}.run(context.Background(), &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}
