package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const maxBody = 1 << 20

// Server serves a Gate over HTTP on a Unix socket: the backend's pre-tool-call
// hook asks, and is blocked until the gate answers (ADR-0004 point 1).
type Server struct {
	Gate *Gate
}

// Handler returns the gate's HTTP API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/decide", s.decide)
	return mux
}

func (s *Server) decide(w http.ResponseWriter, r *http.Request) {
	var req Request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Agent == "" || req.Tool == "" {
		// A request the gate cannot read is denied, never waved through.
		writeDecision(w, http.StatusBadRequest, Decision{Via: "closed", Reason: "the request to the gate was malformed"})
		return
	}
	d := s.Gate.Decide(r.Context(), req)
	writeDecision(w, http.StatusOK, d)
}

func writeDecision(w http.ResponseWriter, status int, d Decision) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(d)
}

// Listen creates the gate socket at path: its directory private, any stale
// socket from a crashed run removed, the socket itself owner-only. Anyone who
// can open it can ask the gate, so access is the file's.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

// Ask puts the request to the gate at socket and returns its decision. It
// fails closed: when the gate cannot be reached or answers nonsense, the
// returned decision is a denial that says why (and err is set too, so the
// caller can log it).
func Ask(ctx context.Context, socket string, req Request) (Decision, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return closed("the request could not be encoded"), err
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		},
		DisableKeepAlives: true,
	}}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://gate/v1/decide", bytes.NewReader(body))
	if err != nil {
		return closed("the request could not be built"), err
	}
	resp, err := client.Do(hreq)
	if err != nil {
		return closed("the pao gate could not be reached"), err
	}
	defer func() { _ = resp.Body.Close() }()

	var wd Decision
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&wd); err != nil {
		return closed("the pao gate answered unreadably"), err
	}
	if resp.StatusCode != http.StatusOK {
		return Decision{Via: "closed", Reason: wd.Reason}, errors.New("the gate refused the request")
	}
	return wd, nil
}
