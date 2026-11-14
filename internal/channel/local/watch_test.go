package local

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeWatch struct {
	// before is returned without admitting the watch.
	before error
	// lines are emitted once admitted, then end is returned.
	lines []WatchLineJSON
	end   error
	// hold keeps an admitted watch open until the request ends.
	hold bool
	got  chan [3]any
}

func (f *fakeWatch) Watch(ctx context.Context, agent string, level, entries int, opened func(), emit func(WatchLineJSON) error) error {
	if f.got != nil {
		f.got <- [3]any{agent, level, entries}
	}
	if f.before != nil {
		return f.before
	}
	opened()
	for _, l := range f.lines {
		if err := emit(l); err != nil {
			return err
		}
	}
	if f.hold {
		<-ctx.Done()
		return nil
	}
	return f.end
}

func watchClient(ts string) *Client { return &Client{URL: ts, Token: token} }

func TestAWatchEndedByTheNodeAfterItStartedIsReportedToTheClient(t *testing.T) {
	_, ts := newServer(t, &fakeLayer{}, WithWatch(&fakeWatch{
		lines: []WatchLineJSON{{Text: "a", History: true}, {Text: "b"}},
		end:   errors.New("the master epoch changed"),
	}))
	var got []WatchLineJSON
	err := watchClient(ts.URL).Watch(context.Background(), "bee", 2, 3, func(l WatchLineJSON) { got = append(got, l) })
	assert.Equal(t, []WatchLineJSON{{Text: "a", History: true}, {Text: "b"}}, got)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the master epoch changed", "a watch that ends is told why, not left to look like a quiet agent")
}

func TestAWatchRefusedBeforeItStartedIsAStatusWithItsMeaning(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{ErrNotFound, "404"}, {ErrBadRequest, "400"}, {ErrBusy, "429"},
	} {
		_, ts := newServer(t, &fakeLayer{}, WithWatch(&fakeWatch{before: c.err}))
		err := watchClient(ts.URL).Watch(context.Background(), "bee", 1, 0, func(WatchLineJSON) {})
		require.Error(t, err)
		assert.Contains(t, err.Error(), c.want)
	}
}

func TestAWatchPassesItsAgentLevelAndEntriesAndStopsWhenTheClientDoes(t *testing.T) {
	f := &fakeWatch{hold: true, lines: []WatchLineJSON{{Text: "x"}}, got: make(chan [3]any, 1)}
	_, ts := newServer(t, &fakeLayer{}, WithWatch(f))
	ctx, stop := context.WithCancel(context.Background())
	seen := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- watchClient(ts.URL).Watch(ctx, "bee", 2, 7, func(WatchLineJSON) { close(seen) })
	}()
	assert.Equal(t, [3]any{"bee", 2, 7}, <-f.got)
	select {
	case <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("no line")
	}
	stop()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the client did not stop")
	}
}

func TestTheWatchRouteIsAbsentWithoutAWatchAPIAndNeedsTheToken(t *testing.T) {
	_, ts := newServer(t, &fakeLayer{})
	resp := do(t, ts, "GET", "/v1/watch/bee", "", "Bearer "+token)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	_, ts = newServer(t, &fakeLayer{}, WithWatch(&fakeWatch{}))
	assert.Equal(t, http.StatusUnauthorized, do(t, ts, "GET", "/v1/watch/bee", "", "").StatusCode)
	assert.Equal(t, http.StatusBadRequest, do(t, ts, "GET", "/v1/watch/bee?level=x", "", "Bearer "+token).StatusCode, "a level that is not a number")
}
