// Package mcpsrv exposes pao's operations as an MCP server (ADR-0016): a third
// surface onto pao, next to the channels and the operator HTTP API. It adds no
// behaviour of its own. Every tool is a thin call onto the operation that
// already exists, made as the subject the connection resolves to, so the access
// seam (and, for operations that need it, the approval gate) decides exactly as
// it does for the other surfaces. A tool call is a tool call, not a channel
// (ADR-0001 point 3).
//
// Every request over streamable HTTP carries its own bearer token and is
// resolved to a subject on its own. A client keeps a session, so pao can tell it
// when the tools on offer change. A session belongs to the subject that opened it
// and is refused to any other, so its identity cannot be borrowed.
package mcpsrv

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
	"github.com/yaad-index/personal-agent-orchestrator/internal/upstream"
)

// Resolver says which subject a bearer token identifies. A token it does not
// know is not a subject: the request is refused, never treated as anyone.
type Resolver interface {
	Resolve(token string) (access.Subject, bool)
}

// OperatorResolver resolves exactly one token, to the operator.
type OperatorResolver struct{ token []byte }

// NewOperatorResolver returns a Resolver for the operator's bearer token. An
// empty token resolves nothing.
func NewOperatorResolver(token string) OperatorResolver {
	return OperatorResolver{token: []byte(token)}
}

// Resolve implements Resolver.
func (r OperatorResolver) Resolve(token string) (access.Subject, bool) {
	if len(r.token) == 0 || subtle.ConstantTimeCompare([]byte(token), r.token) != 1 {
		return access.Subject{}, false
	}
	return access.Operator, true
}

// AgentTokens says which agent a live token belongs to.
type AgentTokens interface {
	Resolve(token string) (agent string, ok bool)
}

// AgentResolver resolves an agent's token to that agent as a subject, and only
// while Alive says the agent's session is still there: an identity tied to a
// session ends with it, even before anyone has revoked the token.
type AgentResolver struct {
	Tokens AgentTokens
	// Alive reports whether the agent's session exists. A nil Alive refuses
	// every token: liveness is part of the identity, not an option.
	Alive func(agent string) bool
}

// Resolve implements Resolver.
func (r AgentResolver) Resolve(token string) (access.Subject, bool) {
	if r.Tokens == nil || r.Alive == nil {
		return access.Subject{}, false
	}
	name, ok := r.Tokens.Resolve(token)
	if !ok || name == "" || !r.Alive(name) {
		return access.Subject{}, false
	}
	return access.Agent(name), true
}

// Chain tries each resolver in turn and returns the first subject found.
type Chain []Resolver

// Resolve implements Resolver.
func (c Chain) Resolve(token string) (access.Subject, bool) {
	for _, r := range c {
		if s, ok := r.Resolve(token); ok {
			return s, true
		}
	}
	return access.Subject{}, false
}

// Server is pao's MCP server.
type Server struct {
	resolver Resolver
	auth     access.Authorizer
	gate     Gate
	ops      Operations
	proxy    Proxy

	mu       sync.Mutex
	subjects map[access.Subject]*offer
}

// offer is the server one subject's sessions share, with the proxied tools on it
// now.
type offer struct {
	srv *mcp.Server

	mu      sync.Mutex
	proxied map[string]upstream.Tool
}

// Proxy is the upstream servers' tools pao offers as its own (ADR-0024).
type Proxy interface {
	// Tools lists the upstream tools on offer now.
	Tools(ctx context.Context) []upstream.Tool
	// Call calls the upstream tool of that pao-side name with args, once.
	Call(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error)
}

// WithProxy offers the proxy's tools, each to the subjects whose access reaches
// it. Without one, pao offers only its own tools.
func WithProxy(p Proxy) Option { return func(s *Server) { s.proxy = p } }

// Option tunes a Server.
type Option func(*Server)

// WithAuthorizer sets the access seam. The default is operator-only.
func WithAuthorizer(a access.Authorizer) Option { return func(s *Server) { s.auth = a } }

// Gate judges an action pao is about to take (ADR-0004).
type Gate interface {
	Decide(ctx context.Context, req gate.Request) gate.Decision
}

// WithGate sets the approval gate. Without one, every action asked for by an
// agent is denied.
func WithGate(g Gate) Option { return func(s *Server) { s.gate = g } }

// ErrDenied is an action the approval gate did not allow.
var ErrDenied = errors.New("denied")

// approve asks the gate about an action pao is about to take for who: when an
// agent calls a tool, pao is the actor, so pao's action is what is judged, here
// on pao's side, where no agent's setup can skip it. The operator's own call is
// not asked: the gate's questions go to the operator. A denial means pao does not
// act; the agent is told so.
func (s *Server) approve(ctx context.Context, who access.Subject, action string, input any) error {
	switch who.Kind {
	case access.KindOperator:
		return nil
	case access.KindAgent:
	default:
		return fmt.Errorf("%w: nobody asked", ErrDenied)
	}
	if s.gate == nil {
		return fmt.Errorf("%w: no approval gate is configured", ErrDenied)
	}
	b, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDenied, err)
	}
	d := s.gate.Decide(ctx, gate.Request{Agent: who.Name, Tool: action, Input: b})
	if !d.Allow {
		return fmt.Errorf("%w: %s", ErrDenied, d.Reason)
	}
	return nil
}

// New returns a Server over the given operations.
func New(ops Operations, r Resolver, opts ...Option) *Server {
	s := &Server{resolver: r, ops: ops, subjects: map[access.Subject]*offer{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// offerFor returns the server of who's sessions, made the first time who
// connects, with pao's own tools on it.
func (s *Server) offerFor(who access.Subject) *offer {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.subjects[who]
	if !ok {
		srv := mcp.NewServer(&mcp.Implementation{Name: "pao", Version: "0"}, nil)
		s.addTools(srv)
		o = &offer{srv: srv, proxied: map[string]upstream.Tool{}}
		s.subjects[who] = o
	}
	return o
}

// sync brings the proxied tools on who's server in line with the access as it
// stands: a tool out of reach is not on it at all, so asking for one is answered
// exactly as asking for a tool that does not exist (ADR-0024 point 4). A change
// tells who's open sessions that the list changed.
func (s *Server) sync(ctx context.Context, who access.Subject, o *offer) {
	want := map[string]upstream.Tool{}
	if s.proxy != nil {
		for _, t := range s.proxy.Tools(ctx) {
			if access.Allowed(s.auth, who, access.CallTool, access.ToolResource(t.Name, t.Tags)) {
				want[t.Name] = t
			}
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	var gone []string
	for name := range o.proxied {
		if _, ok := want[name]; !ok {
			gone = append(gone, name)
		}
	}
	if len(gone) > 0 {
		o.srv.RemoveTools(gone...)
		for _, name := range gone {
			delete(o.proxied, name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(want)) {
		t := want[name]
		if have, ok := o.proxied[name]; ok && reflect.DeepEqual(have, t) {
			continue
		}
		o.srv.AddTool(&mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}, s.proxied(t))
		o.proxied[name] = t
	}
}

// Refresh brings every connected subject's tools in line with the access as it
// stands, so a session open now hears of a tool it was given since it listed
// them. It is called when grants change; a revoked tool needs no Refresh to be
// refused, since every request and every call checks again.
func (s *Server) Refresh(ctx context.Context) {
	s.mu.Lock()
	subjects := maps.Clone(s.subjects)
	s.mu.Unlock()
	for who, o := range subjects {
		s.sync(ctx, who, o)
	}
}

// proxied calls an upstream tool for the caller: the caller's access reaches it,
// checked again at the call; the gate judges pao's call; then pao makes it.
func (s *Server) proxied(t upstream.Tool) mcp.ToolHandler {
	name := t.Name
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		who := subject(req)
		// A grant revoked since the tool was offered is refused now, as a tool
		// that does not exist, in the server's own words.
		if !access.Allowed(s.auth, who, access.CallTool, access.ToolResource(t.Name, t.Tags)) {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("unknown tool %q", name)}
		}
		args := req.Params.Arguments
		if err := s.approve(ctx, who, ProxiedAction(name), args); err != nil {
			return toolError(err), nil
		}
		res, err := s.proxy.Call(ctx, name, args)
		if err != nil {
			return toolError(err), nil
		}
		return res, nil
	}
}

func toolError(err error) *mcp.CallToolResult {
	res := &mcp.CallToolResult{}
	res.SetError(err)
	return res
}

// Handler returns the HTTP handler to mount at the MCP endpoint. It refuses a
// request without a token that resolves to a subject before the protocol sees
// it.
func (s *Server) Handler() http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return s.offerFor(requester(r)).srv },
		&mcp.StreamableHTTPOptions{JSONResponse: true})
	// Before the protocol sees a request, the subject's tools are brought in
	// line with its access as it stands, so what a request lists or calls is
	// never older than the request.
	synced := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := requester(r)
		s.sync(r.Context(), who, s.offerFor(who))
		h.ServeHTTP(w, r)
	})
	return auth.RequireBearerToken(s.verify, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(synced)
}

// subjectKey is the TokenInfo.Extra key that carries the resolved subject.
const subjectKey = "pao.subject"

func (s *Server) verify(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	sub, ok := s.resolver.Resolve(token)
	if !ok || sub.Kind == 0 {
		return nil, auth.ErrInvalidToken
	}
	return &auth.TokenInfo{
		UserID:     subjectName(sub),
		Expiration: time.Now().Add(time.Hour),
		Extra:      map[string]any{subjectKey: sub},
	}, nil
}

func subjectName(s access.Subject) string {
	if s.Kind == access.KindOperator {
		return "operator"
	}
	return "agent:" + s.Name
}

// requester returns who an HTTP request is made as: the subject its token
// resolved to, or nobody.
func requester(r *http.Request) access.Subject {
	info := auth.TokenInfoFromContext(r.Context())
	if info == nil {
		return access.Subject{}
	}
	sub, _ := info.Extra[subjectKey].(access.Subject)
	return sub
}

// subject returns who a tool call is made as. A call that carries no resolved
// subject is nobody, which every authorizer denies.
func subject(req *mcp.CallToolRequest) access.Subject {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		return access.Subject{}
	}
	sub, _ := req.Extra.TokenInfo.Extra[subjectKey].(access.Subject)
	return sub
}
