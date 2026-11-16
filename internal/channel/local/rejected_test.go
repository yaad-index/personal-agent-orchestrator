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

type fakeRejected struct {
	out []RejectedJSON
	err error
}

func (f fakeRejected) Rejected(context.Context) ([]RejectedJSON, error) { return f.out, f.err }

func TestTheRejectedRouteIsAbsentWithoutAnAPI(t *testing.T) {
	_, ts := newServer(t, &fakeLayer{})
	resp := do(t, ts, "GET", "/v1/inbound/rejected", "", bearer())
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestTheRejectedRouteNeedsTheBearerTokenAndOffersNoWrite(t *testing.T) {
	_, ts := newServer(t, &fakeLayer{}, WithRejected(fakeRejected{out: []RejectedJSON{{Channel: "c", Sender: "s"}}}))
	for _, auth := range []string{"", "Bearer wrong"} {
		resp := do(t, ts, "GET", "/v1/inbound/rejected", "", auth)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, auth)
	}
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		resp := do(t, ts, m, "/v1/inbound/rejected", "{}", bearer())
		assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, m)
	}
}

func TestTheClientListsTheRejectedInbounds(t *testing.T) {
	want := []RejectedJSON{{Channel: "chat", Sender: "1", Kind: "message", Reason: "no subject", First: time.Unix(1, 0).UTC(), Last: time.Unix(2, 0).UTC(), Count: 3, Payload: "hi"}}
	_, ts := newServer(t, &fakeLayer{}, WithRejected(fakeRejected{out: want}))
	got, err := (&Client{URL: ts.URL, Token: token}).Rejected(context.Background())
	require.NoError(t, err)
	assert.Equal(t, want, got)

	_, ts = newServer(t, &fakeLayer{}, WithRejected(fakeRejected{}))
	got, err = (&Client{URL: ts.URL, Token: token}).Rejected(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, got, "empty is an array")
	assert.Empty(t, got)
}

func TestAFailingRecordIsAnError(t *testing.T) {
	_, ts := newServer(t, &fakeLayer{}, WithRejected(fakeRejected{err: errors.New("boom")}))
	_, err := (&Client{URL: ts.URL, Token: token}).Rejected(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}
