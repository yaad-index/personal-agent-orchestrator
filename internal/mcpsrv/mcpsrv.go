// Package mcpsrv exposes pao's operations as an MCP server (ADR-0016): a third
// surface onto pao, next to the channels and the operator HTTP API. It adds no
// behaviour of its own. Every tool is a thin call onto the operation that
// already exists, made as the subject the connection resolves to, so the access
// seam (and, for operations that need it, the approval gate) decides exactly as
// it does for the other surfaces. A tool call is a tool call, not a channel
// (ADR-0001 point 3).
//
// The server is stateless over streamable HTTP: every request carries its own
// bearer token and is resolved to a subject on its own, so there is no session
// whose identity could be borrowed.
package mcpsrv

import (
	"context"
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
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
	srv      *mcp.Server
	resolver Resolver
	auth     access.Authorizer
	ops      Operations
}

// Option tunes a Server.
type Option func(*Server)

// WithAuthorizer sets the access seam. The default is operator-only.
func WithAuthorizer(a access.Authorizer) Option { return func(s *Server) { s.auth = a } }

// New returns a Server over the given operations.
func New(ops Operations, r Resolver, opts ...Option) *Server {
	s := &Server{resolver: r, ops: ops}
	for _, o := range opts {
		o(s)
	}
	s.srv = mcp.NewServer(&mcp.Implementation{Name: "pao", Version: "0"}, nil)
	s.addTools()
	return s
}

// Handler returns the HTTP handler to mount at the MCP endpoint. It refuses a
// request without a token that resolves to a subject before the protocol sees
// it.
func (s *Server) Handler() http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return auth.RequireBearerToken(s.verify, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(h)
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

// subject returns who a tool call is made as. A call that carries no resolved
// subject is nobody, which every authorizer denies.
func subject(req *mcp.CallToolRequest) access.Subject {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		return access.Subject{}
	}
	sub, _ := req.Extra.TokenInfo.Extra[subjectKey].(access.Subject)
	return sub
}
