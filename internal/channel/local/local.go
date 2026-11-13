// Package local is pao's generic local channel: a human front served over
// HTTP on the loopback interface. The human posts messages and reactions and
// reads what pao sends as a server-sent event stream. It names no chat
// platform, so it is both the first channel and the test double for others
// (ADR-0009). It is always token-gated: anyone who can reach the port is
// otherwise the human.
package local

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
	"github.com/yaad-index/personal-agent-orchestrator/internal/msgstore"
)

const (
	// Source is the source kind the channel stamps on inbound messages.
	Source = "local"

	maxBody       = 1 << 20
	subscriberBuf = 64
	defaultSender = "human"
)

// Layer is the part of the communication layer the channel uses.
type Layer interface {
	Receive(ctx context.Context, in comm.Inbound) (comm.Delivery, error)
	React(id, by, emoji string) error
}

// Decider receives the operator's answers to the gate's questions.
type Decider interface {
	Answer(id string, ans gate.Answer) error
}

// Server is the local channel. It implements comm.Channel for the outbound
// direction and serves the inbound HTTP API.
type Server struct {
	layer     Layer
	decider   Decider
	token     []byte
	heartbeat time.Duration

	mu   sync.Mutex
	subs map[chan core.Message]struct{}
}

var _ comm.Channel = (*Server)(nil)

// Option configures a Server.
type Option func(*Server)

// WithHeartbeat sets how often an idle stream gets a comment line, so a quiet
// connection is not mistaken for a dead one. Zero disables it.
func WithHeartbeat(d time.Duration) Option { return func(s *Server) { s.heartbeat = d } }

// WithDecider lets the operator answer the gate's questions over this channel.
func WithDecider(d Decider) Option { return func(s *Server) { s.decider = d } }

// New returns a Server. An empty token is refused: the channel has no
// unauthenticated mode.
func New(layer Layer, token string, opts ...Option) (*Server, error) {
	if token == "" {
		return nil, errors.New("local channel needs a token")
	}
	s := &Server{
		layer:     layer,
		token:     []byte(token),
		heartbeat: 15 * time.Second,
		subs:      make(map[chan core.Message]struct{}),
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Name implements comm.Channel.
func (s *Server) Name() string { return "local" }

// Send implements comm.Channel: it hands m to every connected stream. A stream
// that has fallen too far behind is dropped rather than allowed to block the
// conduit; its client reconnects.
func (s *Server) Send(_ context.Context, m core.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- m:
		default:
			delete(s.subs, ch)
			close(ch)
		}
	}
	return nil
}

// Handler returns the HTTP API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", s.auth(s.postMessage))
	mux.HandleFunc("POST /v1/reactions", s.auth(s.postReaction))
	mux.HandleFunc("POST /v1/decisions", s.auth(s.postDecision))
	mux.HandleFunc("GET /v1/stream", s.auth(s.stream))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got, hasScheme := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !hasScheme || subtle.ConstantTimeCompare([]byte(got), s.token) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

type targetJSON struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type postMessageRequest struct {
	Sender     string     `json:"sender"`
	Text       string     `json:"text"`
	Target     targetJSON `json:"target"`
	Reply      string     `json:"reply"`
	ReplyQuote string     `json:"reply_quote"`
}

type resultJSON struct {
	Agent    string `json:"agent"`
	Launched bool   `json:"launched"`
	Error    string `json:"error,omitempty"`
}

type deliveryJSON struct {
	ID      string       `json:"id"`
	Results []resultJSON `json:"results"`
}

func (s *Server) postMessage(w http.ResponseWriter, r *http.Request) {
	var req postMessageRequest
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	kind, ok := parseKind(req.Target.Kind)
	if !ok {
		writeError(w, http.StatusBadRequest, `target.kind must be "name" or "tag"`)
		return
	}
	sender := req.Sender
	if sender == "" {
		sender = defaultSender
	}

	d, err := s.layer.Receive(r.Context(), comm.Inbound{
		Source:     Source,
		Sender:     sender,
		Text:       req.Text,
		Kind:       core.MessageContent,
		Reply:      req.Reply,
		ReplyQuote: req.ReplyQuote,
		Target:     core.Target{Kind: kind, Value: req.Target.Value},
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	out := deliveryJSON{ID: d.Message.ID, Results: []resultJSON{}}
	for _, res := range d.Results {
		rj := resultJSON{Agent: res.Agent, Launched: res.Launched}
		if res.Err != nil {
			rj.Error = res.Err.Error()
		}
		out.Results = append(out.Results, rj)
	}
	writeJSON(w, http.StatusOK, out)
}

type reactionRequest struct {
	ID    string `json:"id"`
	By    string `json:"by"`
	Emoji string `json:"emoji"`
}

func (s *Server) postReaction(w http.ResponseWriter, r *http.Request) {
	var req reactionRequest
	if !decode(w, r, &req) {
		return
	}
	if req.ID == "" || req.Emoji == "" {
		writeError(w, http.StatusBadRequest, "id and emoji are required")
		return
	}
	by := req.By
	if by == "" {
		by = defaultSender
	}
	if err := s.layer.React(req.ID, by, req.Emoji); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": req.ID})
}

type decisionRequest struct {
	ID       string `json:"id"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// postDecision is the channel's answer button: allow, always (allow and
// remember) or deny a question the gate put to the operator.
func (s *Server) postDecision(w http.ResponseWriter, r *http.Request) {
	var req decisionRequest
	if !decode(w, r, &req) {
		return
	}
	if s.decider == nil {
		writeError(w, http.StatusNotFound, "no gate is configured on this node")
		return
	}
	var ans gate.Answer
	switch req.Decision {
	case "allow":
		ans = gate.Answer{Allow: true}
	case "always":
		ans = gate.Answer{Allow: true, Remember: true}
	case "deny":
		ans = gate.Answer{Reason: req.Reason}
	default:
		writeError(w, http.StatusBadRequest, `decision must be "allow", "always" or "deny"`)
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := s.decider.Answer(req.ID, ans); err != nil {
		if errors.Is(err, gate.ErrNoSuchQuestion) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": req.ID, "decision": req.Decision})
}

// MessageJSON is a message as it appears on the stream.
type MessageJSON struct {
	ID     string    `json:"id"`
	Time   time.Time `json:"time"`
	Sender string    `json:"sender"`
	Kind   string    `json:"kind"`
	Text   string    `json:"text"`
	Reply  string    `json:"reply,omitempty"`
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	ch := make(chan core.Message, subscriberBuf)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if _, still := s.subs[ch]; still {
			delete(s.subs, ch)
			close(ch)
		}
		s.mu.Unlock()
	}()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, ": connected\n\n")
	fl.Flush()

	var beat <-chan time.Time
	if s.heartbeat > 0 {
		t := time.NewTicker(s.heartbeat)
		defer t.Stop()
		beat = t.C
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
		case m, open := <-ch:
			if !open {
				return // dropped as too slow; the client reconnects
			}
			b, err := json.Marshal(MessageJSON{ID: m.ID, Time: m.Time, Sender: m.Sender, Kind: string(m.Kind), Text: m.Text, Reply: m.Reply})
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "id: %s\nevent: message\ndata: %s\n\n", m.ID, b)
			fl.Flush()
		}
	}
}

// fail answers with the error's status. An unexpected error is logged and the
// client gets a generic message, not the internals.
func (s *Server) fail(w http.ResponseWriter, err error) {
	status := statusFor(err)
	if status == http.StatusInternalServerError {
		slog.Error("local channel request failed", "error", err)
		writeError(w, status, "internal error")
		return
	}
	writeError(w, status, err.Error())
}

func parseKind(s string) (core.TargetKind, bool) {
	switch s {
	case "name":
		return core.ByName, true
	case "tag":
		return core.ByTag, true
	}
	return 0, false
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, core.ErrNoTarget):
		return http.StatusBadRequest
	case errors.Is(err, core.ErrNoMatch), errors.Is(err, msgstore.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, msgstore.ErrUnresolved), errors.Is(err, msgstore.ErrAmbiguous):
		return http.StatusUnprocessableEntity
	}
	return http.StatusInternalServerError
}

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
