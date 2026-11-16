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
	invitations []InvitationJSON
	gotTTL      time.Duration
	gotID       string
	gotOp       bool

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

func (f *fakeIdentity) SetOperator(_ context.Context, n string, on bool) error {
	f.gotName, f.gotOp = n, on
	return f.err
}
func (f *fakeIdentity) Invite(_ context.Context, subject string, ttl time.Duration) (InvitationJSON, error) {
	f.gotSubject, f.gotTTL = subject, ttl
	return InvitationJSON{ID: "ab12", Subject: subject, Secret: "S-E-C"}, f.err
}
func (f *fakeIdentity) Invitations(context.Context) ([]InvitationJSON, error) {
	return f.invitations, f.err
}
func (f *fakeIdentity) RevokeInvitation(_ context.Context, id string) error {
	f.gotID = id
	return f.err
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

func TestTheClientIssuesListsAndRevokesInvitations(t *testing.T) {
	f := &fakeIdentity{invitations: []InvitationJSON{{ID: "ab12", Subject: "alice", Expires: time.Unix(9, 0).UTC()}}}
	c := identityClient(t, f)
	ctx := context.Background()

	inv, err := c.Invite(ctx, "alice", 90*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "S-E-C", inv.Secret, "the secret comes back once, with the issue")
	assert.Equal(t, "alice", f.gotSubject)
	assert.Equal(t, 90*time.Second, f.gotTTL)

	invs, err := c.Invitations(ctx)
	require.NoError(t, err)
	assert.Equal(t, f.invitations, invs)
	assert.Empty(t, invs[0].Secret)

	require.NoError(t, c.RevokeInvitation(ctx, "ab12"))
	assert.Equal(t, "ab12", f.gotID)

	empty := identityClient(t, &fakeIdentity{})
	invs, err = empty.Invitations(ctx)
	require.NoError(t, err)
	assert.NotNil(t, invs)
}

func TestInvitationRoutesNeedTheTokenAndARefusedOneKeepsItsStatus(t *testing.T) {
	f := &fakeIdentity{}
	_, ts := newServer(t, &fakeLayer{}, WithIdentity(f))
	for _, c := range [][2]string{{"POST", "/v1/identity/invitations"}, {"GET", "/v1/identity/invitations"}, {"DELETE", "/v1/identity/invitations/x"}} {
		resp := do(t, ts, c[0], c[1], `{"subject":"a"}`, "")
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, c[0])
	}
	assert.Empty(t, f.gotSubject)
	resp := do(t, ts, "POST", "/v1/identity/invitations", `{"subject":"a","ttl_seconds":-1}`, bearer())
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "a negative lifetime is refused")
	resp = do(t, ts, "POST", "/v1/identity/invitations", `{nope`, bearer())
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	cl := identityClient(t, &fakeIdentity{err: ErrNotFound})
	_, err := cl.Invite(context.Background(), "ghost", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestTheClientFlagsAndClearsAnOperator(t *testing.T) {
	f := &fakeIdentity{}
	c := identityClient(t, f)
	require.NoError(t, c.SetOperator(context.Background(), "a b", true))
	assert.Equal(t, "a b", f.gotName)
	assert.True(t, f.gotOp)
	require.NoError(t, c.SetOperator(context.Background(), "a b", false))
	assert.False(t, f.gotOp)

	_, ts := newServer(t, &fakeLayer{}, WithIdentity(f))
	assert.Equal(t, http.StatusUnauthorized, do(t, ts, "PUT", "/v1/identity/subjects/a/operator", `{"operator":true}`, "").StatusCode)
	assert.Equal(t, http.StatusBadRequest, do(t, ts, "PUT", "/v1/identity/subjects/a/operator", `{nope`, bearer()).StatusCode)

	cl := identityClient(t, &fakeIdentity{err: ErrNotFound})
	err := cl.SetOperator(context.Background(), "ghost", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}
