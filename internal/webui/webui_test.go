package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const token = "operator-token"

func init() { failDelay = 0 }

func do(t *testing.T, h http.Handler, method, path, body string, header map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range header {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

var ui = map[string]string{Header: "1"}

func signIn(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	w := do(t, h, "POST", "/ui/api/login", `{"token":"`+token+`"}`, ui)
	require.Equal(t, http.StatusNoContent, w.Code)
	cs := w.Result().Cookies()
	require.Len(t, cs, 1)
	return cs[0]
}

func TestThePageIsServedWithAPolicyThatAllowsOnlyPao(t *testing.T) {
	h := Handler(NewSessions(), token)
	w := do(t, h, "GET", "/ui/", "", nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `<div id="app">`)
	csp := w.Header().Get("Content-Security-Policy")
	for _, d := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "connect-src 'self'", "frame-ancestors 'none'", "base-uri 'none'"} {
		assert.Contains(t, csp, d)
	}
	assert.NotContains(t, csp, "unsafe")
	assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.NotContains(t, w.Body.String(), "<script>", "no inline script for the policy to block")

	w = do(t, h, "GET", "/ui", "", nil)
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "/ui/", w.Header().Get("Location"))
}

func TestTheBuiltFilesAreServedAndCachedForGood(t *testing.T) {
	h := Handler(NewSessions(), token)
	page := do(t, h, "GET", "/ui/", "", nil).Body.String()
	i := strings.Index(page, "/ui/assets/")
	require.GreaterOrEqual(t, i, 0, "the page names its script")
	asset := page[i:]
	asset = asset[:strings.IndexAny(asset, `"'`)]
	w := do(t, h, "GET", asset, "", nil)
	require.Equal(t, http.StatusOK, w.Code, asset)
	assert.Contains(t, w.Header().Get("Cache-Control"), "immutable")
	assert.NotEmpty(t, w.Header().Get("Content-Security-Policy"))
	assert.Equal(t, http.StatusNotFound, do(t, h, "GET", "/ui/assets/nope.js", "", nil).Code)
}

func TestSigningInNeedsTheTokenAndTheUIsOwnHeader(t *testing.T) {
	s := NewSessions()
	h := Handler(s, token)
	assert.Equal(t, http.StatusForbidden, do(t, h, "POST", "/ui/api/login", `{"token":"`+token+`"}`, nil).Code, "not from the UI's page")
	w := do(t, h, "POST", "/ui/api/login", `{"token":"wrong"}`, ui)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Empty(t, w.Result().Cookies())
	assert.Equal(t, http.StatusBadRequest, do(t, h, "POST", "/ui/api/login", `not json`, ui).Code)

	c := signIn(t, h)
	assert.Equal(t, CookieName, c.Name)
	assert.True(t, c.HttpOnly, "no script can read it")
	assert.Equal(t, http.SameSiteStrictMode, c.SameSite, "no other site's request carries it")
	assert.Equal(t, "/", c.Path)
	assert.Len(t, c.Value, 64)
	assert.Equal(t, int(SessionTTL/time.Second), c.MaxAge)

	assert.Equal(t, http.StatusNoContent, do(t, h, "GET", "/ui/api/session", "", nil, c).Code)
	assert.Equal(t, http.StatusUnauthorized, do(t, h, "GET", "/ui/api/session", "", nil).Code)
	assert.Equal(t, http.StatusUnauthorized, do(t, h, "GET", "/ui/api/session", "", nil, &http.Cookie{Name: CookieName, Value: strings.Repeat("0", 64)}).Code)
}

func TestSigningOutEndsTheSession(t *testing.T) {
	s := NewSessions()
	h := Handler(s, token)
	c := signIn(t, h)
	assert.Equal(t, http.StatusForbidden, do(t, h, "POST", "/ui/api/logout", "", nil, c).Code, "a page elsewhere cannot sign you out")
	w := do(t, h, "POST", "/ui/api/logout", "", ui, c)
	require.Equal(t, http.StatusNoContent, w.Code)
	require.Len(t, w.Result().Cookies(), 1)
	assert.Negative(t, w.Result().Cookies()[0].MaxAge, "the cookie is cleared")
	assert.Equal(t, http.StatusUnauthorized, do(t, h, "GET", "/ui/api/session", "", nil, c).Code, "and the session is gone on the node")
}

func TestASessionEndsAfterItsTime(t *testing.T) {
	s := NewSessions()
	now := time.Now()
	s.now = func() time.Time { return now }
	c := signIn(t, Handler(s, token))
	r := httptest.NewRequest("GET", "/v1/node", nil)
	r.AddCookie(c)
	assert.True(t, s.Authorized(r))
	now = now.Add(SessionTTL)
	assert.False(t, s.Authorized(r))
	assert.Empty(t, s.ids, "an expired session is forgotten")
}

func TestOnlyTheUIsOwnPageMayChangeSomething(t *testing.T) {
	r := func(header map[string]string) *http.Request {
		req := httptest.NewRequest("POST", "http://127.0.0.1:7744/v1/messages", nil)
		for k, v := range header {
			req.Header.Set(k, v)
		}
		return req
	}
	assert.True(t, FromUI(r(map[string]string{Header: "1"})))
	assert.True(t, FromUI(r(map[string]string{Header: "1", "Origin": "http://127.0.0.1:7744"})))
	assert.False(t, FromUI(r(nil)), "no header")
	assert.False(t, FromUI(r(map[string]string{Header: "yes"})))
	assert.False(t, FromUI(r(map[string]string{Header: "1", "Origin": "http://evil.example"})), "another site's page")
	assert.False(t, FromUI(r(map[string]string{Header: "1", "Origin": "http://127.0.0.1:7745"})), "another port is another origin")
}

func TestNoTokenMeansNoSignIn(t *testing.T) {
	h := Handler(NewSessions(), "")
	assert.Equal(t, http.StatusUnauthorized, do(t, h, "POST", "/ui/api/login", `{"token":""}`, ui).Code)
}
