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

type fakeIdentity struct {
	subjects []SubjectJSON
	bindings []BindingJSON
	err      error

	gotName, gotChannel, gotSender, gotSubject string
	gotBind                                    BindingJSON
}

func (f *fakeIdentity) AddSubject(_ context.Context, n string) error { f.gotName = n; return f.err }
func (f *fakeIdentity) RemoveSubject(_ context.Context, n string) error {
	f.gotName = n
	return f.err
}
func (f *fakeIdentity) Subjects(context.Context) ([]SubjectJSON, error) { return f.subjects, f.err }
func (f *fakeIdentity) Bind(_ context.Context, b BindingJSON) error     { f.gotBind = b; return f.err }
func (f *fakeIdentity) Revoke(_ context.Context, c, s string) error {
	f.gotChannel, f.gotSender = c, s
	return f.err
}
func (f *fakeIdentity) Bindings(_ context.Context, s string) ([]BindingJSON, error) {
	f.gotSubject = s
	return f.bindings, f.err
}

func identityClient(t *testing.T, f *fakeIdentity) *Client {
	t.Helper()
	_, ts := newServer(t, &fakeLayer{}, WithIdentity(f))
	return &Client{URL: ts.URL, Token: token}
}

func TestIdentityRoutesAreAbsentWithoutAnAPI(t *testing.T) {
	_, ts := newServer(t, &fakeLayer{})
	for _, p := range []string{"/v1/identity/subjects", "/v1/identity/bindings"} {
		resp := do(t, ts, "GET", p, "", bearer())
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, p)
	}
}

func TestIdentityRoutesNeedTheBearerToken(t *testing.T) {
	f := &fakeIdentity{}
	_, ts := newServer(t, &fakeLayer{}, WithIdentity(f))
	for _, c := range [][2]string{
		{"GET", "/v1/identity/subjects"}, {"POST", "/v1/identity/subjects"}, {"DELETE", "/v1/identity/subjects/a"},
		{"GET", "/v1/identity/bindings"}, {"POST", "/v1/identity/bindings"}, {"DELETE", "/v1/identity/bindings?channel=c&sender=s"},
	} {
		for _, auth := range []string{"", "Bearer wrong"} {
			resp := do(t, ts, c[0], c[1], `{"name":"a"}`, auth)
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, c[0]+" "+c[1]+" "+auth)
		}
	}
	assert.Equal(t, fakeIdentity{}, *f, "nothing reached the API")
}

func TestTheClientReachesEachIdentityOperation(t *testing.T) {
	f := &fakeIdentity{
		subjects: []SubjectJSON{{Name: "alice", Created: time.Unix(5, 0).UTC()}},
		bindings: []BindingJSON{{Channel: "chat", Sender: "1", Subject: "alice"}},
	}
	c := identityClient(t, f)
	ctx := context.Background()

	subs, err := c.Subjects(ctx)
	require.NoError(t, err)
	assert.Equal(t, f.subjects, subs)

	require.NoError(t, c.AddSubject(ctx, "bob"))
	assert.Equal(t, "bob", f.gotName)
	require.NoError(t, c.RemoveSubject(ctx, "a b"))
	assert.Equal(t, "a b", f.gotName)

	bs, err := c.Bindings(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, f.bindings, bs)
	assert.Equal(t, "alice", f.gotSubject)
	_, err = c.Bindings(ctx, "")
	require.NoError(t, err)
	assert.Empty(t, f.gotSubject)

	require.NoError(t, c.Bind(ctx, BindingJSON{Channel: "chat", Sender: "2", Subject: "bob"}))
	assert.Equal(t, BindingJSON{Channel: "chat", Sender: "2", Subject: "bob"}, f.gotBind)
	require.NoError(t, c.Revoke(ctx, "chat", "id/with slash&amp"))
	assert.Equal(t, [2]string{"chat", "id/with slash&amp"}, [2]string{f.gotChannel, f.gotSender}, "the sender id travels intact")
}

func TestAnEmptyListIsAnEmptyArray(t *testing.T) {
	c := identityClient(t, &fakeIdentity{})
	subs, err := c.Subjects(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, subs)
	assert.Empty(t, subs)
	bs, err := c.Bindings(context.Background(), "")
	require.NoError(t, err)
	assert.NotNil(t, bs)
	assert.Empty(t, bs)
}

func TestIdentityErrorsKeepTheirStatus(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		err  error
		want string
	}{{ErrNotFound, "404"}, {ErrConflict, "409"}, {ErrBadRequest, "400"}} {
		cl := identityClient(t, &fakeIdentity{err: c.err})
		for name, err := range map[string]error{
			"add":     cl.AddSubject(ctx, "x"),
			"remove":  cl.RemoveSubject(ctx, "x"),
			"bind":    cl.Bind(ctx, BindingJSON{}),
			"revoke":  cl.Revoke(ctx, "c", "s"),
			"binding": func() error { _, e := cl.Bindings(ctx, ""); return e }(),
			"subject": func() error { _, e := cl.Subjects(ctx); return e }(),
		} {
			require.Error(t, err, name)
			assert.Contains(t, err.Error(), c.want, name)
		}
	}
	cl := identityClient(t, &fakeIdentity{err: errors.New("boom")})
	assert.Error(t, cl.AddSubject(ctx, "x"))
}

func TestABadIdentityBodyIsRefused(t *testing.T) {
	f := &fakeIdentity{}
	_, ts := newServer(t, &fakeLayer{}, WithIdentity(f))
	for _, p := range []string{"/v1/identity/subjects", "/v1/identity/bindings"} {
		resp := do(t, ts, "POST", p, "{not json", bearer())
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, p)
	}
	assert.Equal(t, fakeIdentity{}, *f)
}
