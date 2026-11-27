// Package webui serves pao's management UI (ADR-0047) at /ui on the node's
// own listener, which is on localhost by default (ADR-0005 point 4). The
// page is a static build embedded in the binary; it talks to the node's /v1
// API with the session cookie it gets by signing in with the operator token.
//
// The page loads nothing from anywhere but pao: its content security policy
// allows only pao's own scripts, styles and API, and no page may frame it.
package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed all:dist
var dist embed.FS

// Prefix is where the UI is served.
const Prefix = "/ui/"

// Header marks a call as the UI's own. A page on another site cannot add it to
// a request it makes the browser send, so pao requires it on every call the
// session cookie authorises that changes something.
const Header = "X-Pao-UI"

// CookieName is the session cookie.
const CookieName = "pao_ui"

// failDelay is how long a failed sign-in waits before it is answered.
var failDelay = time.Second

// SessionTTL is how long a sign-in lasts.
const SessionTTL = 12 * time.Hour

// policy is the content security policy of every page and file the UI serves.
const policy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// Sessions are the signed-in UI sessions, held in memory: a node that
// restarts signs everyone out.
type Sessions struct {
	mu  sync.Mutex
	ids map[string]time.Time
	now func() time.Time
}

// NewSessions returns an empty set of sessions.
func NewSessions() *Sessions {
	return &Sessions{ids: map[string]time.Time{}, now: time.Now}
}

// start opens a session and returns its id.
func (s *Sessions) start() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, exp := range s.ids {
		if !now.Before(exp) {
			delete(s.ids, k)
		}
	}
	s.ids[id] = now.Add(SessionTTL)
	return id, nil
}

func (s *Sessions) end(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ids, id)
}

// valid reports whether id is a session that has not ended or expired.
func (s *Sessions) valid(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.ids[id]
	if !ok {
		return false
	}
	if !s.now().Before(exp) {
		delete(s.ids, id)
		return false
	}
	return true
}

// Authorized reports whether r carries a live session cookie. It is how the
// node's API takes a call from the UI as the operator's; whether the call may
// change something is the API's to check, by Header and origin (Mutating).
func (s *Sessions) Authorized(r *http.Request) bool {
	c, err := r.Cookie(CookieName)
	return err == nil && s.valid(c.Value)
}

// FromUI reports whether a request that the session cookie authorises may
// change something: it carries Header, and an Origin, if it names one, that
// is the node's own. A page on another site can make a browser send the
// cookie, but it cannot add the header, and its Origin is its own.
func FromUI(r *http.Request) bool {
	if r.Header.Get(Header) != "1" {
		return false
	}
	o := r.Header.Get("Origin")
	return o == "" || o == "http://"+r.Host || o == "https://"+r.Host
}

// Handler serves the UI and its sign-in at Prefix. token is the node's
// operator token.
func Handler(sessions *Sessions, token string) http.Handler {
	files, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	static := http.StripPrefix(strings.TrimSuffix(Prefix, "/"), http.FileServer(http.FS(files)))
	want := []byte(token)

	mux := http.NewServeMux()
	mux.HandleFunc("POST "+Prefix+"api/login", func(w http.ResponseWriter, r *http.Request) {
		if !FromUI(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "the body must be {\"token\": ...}")
			return
		}
		if len(want) == 0 || subtle.ConstantTimeCompare([]byte(body.Token), want) != 1 {
			// a guess costs time; the token is 256 bits all the same
			time.Sleep(failDelay)
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		id, err := sessions.start()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not start a session")
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: CookieName, Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
			MaxAge: int(SessionTTL / time.Second), Secure: r.TLS != nil,
		})
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST "+Prefix+"api/logout", func(w http.ResponseWriter, r *http.Request) {
		if !FromUI(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if c, err := r.Cookie(CookieName); err == nil {
			sessions.end(c.Value)
		}
		http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET "+Prefix+"api/session", func(w http.ResponseWriter, r *http.Request) {
		if !sessions.Authorized(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET "+Prefix+"assets/", func(w http.ResponseWriter, r *http.Request) {
		// file names carry their content's hash, so a file never changes
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		static.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET "+Prefix+"{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		static.ServeHTTP(w, r)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", policy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		if r.URL.Path == strings.TrimSuffix(Prefix, "/") {
			http.Redirect(w, r, Prefix, http.StatusFound)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
