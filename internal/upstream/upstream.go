// Package upstream is pao as an MCP client of an upstream MCP server (ADR-0024
// point 1). pao connects to each configured server once, with the server's own
// credential, and offers the tools the configuration names; an agent never sees
// the server or its credential.
//
// What the server says is not trusted: a tool's name, tags and risk come from the
// configuration, never from the server; its description is capped and marked as
// the server's; and a result larger than the cap is refused, not cut.
package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/gate"
)

// Limits on what an upstream server may hand an agent through pao.
const (
	// MaxDescription is the longest tool description passed on, in bytes.
	MaxDescription = 2048
	// MaxResult is the largest tool result passed on, as JSON, in bytes.
	MaxResult = 1 << 20
	// maxResponse caps any one HTTP response from the server.
	maxResponse = 4 << 20
	// DefaultCallTimeout bounds one call to the server.
	DefaultCallTimeout = 60 * time.Second
)

// ErrNotOffered is a tool the configuration does not name.
var ErrNotOffered = errors.New("not a tool pao offers from this server")

// ErrTooLarge is a result past MaxResult.
var ErrTooLarge = errors.New("the result is larger than pao passes on")

// Tool is one upstream tool as pao offers it.
type Tool struct {
	// Name is the pao-side name, "<upstream>__<tool>".
	Name string
	// Upstream and Remote are the server's name and the tool's name there.
	Upstream string
	Remote   string
	// Description is the server's description, capped and marked as the server's.
	Description string
	// InputSchema is the server's input schema for the tool.
	InputSchema any
	// Tags and Risk are the configuration's.
	Tags []string
	Risk gate.Risk
}

// Server is one upstream server. It is safe for concurrent use.
type Server struct {
	cfg     config.MCPUpstream
	client  *mcp.Client
	httpc   *http.Client
	timeout time.Duration

	mu      sync.Mutex
	session *mcp.ClientSession
}

// New returns the server for cfg; token is the bearer credential, empty for
// none. Nothing connects until it is used.
func New(cfg config.MCPUpstream, token string) *Server {
	home := originOf(cfg.URL)
	return &Server{
		cfg:    cfg,
		client: mcp.NewClient(&mcp.Implementation{Name: "pao", Version: "1"}, nil),
		httpc: &http.Client{
			Transport: &bearer{token: token, origin: home, next: capped{next: http.DefaultTransport, max: maxResponse}},
			// A redirect to another origin is not followed: neither the credential
			// nor what an agent sends the tool goes anywhere but the configured server.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if originOf(req.URL.String()) != home {
					return errOtherOrigin
				}
				if len(via) >= maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				return nil
			},
		},
		timeout: DefaultCallTimeout,
	}
}

// maxRedirects is how many redirects within the server's own origin are followed.
const maxRedirects = 10

// errOtherOrigin is a redirect away from the configured server.
var errOtherOrigin = errors.New("the server redirected to another origin, which pao does not follow")

// originOf is a URL's scheme, host and port, the port filled in where the
// scheme implies it, so two spellings of one origin compare equal. A URL that
// does not parse has no origin and matches nothing.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

// Name is the server's configured name.
func (s *Server) Name() string { return s.cfg.Name }

// Tools lists the configured tools the server has, in pao-side name order. A
// configured tool the server does not have, or offers with an input schema that
// is not a JSON object schema, is left out and named in missing.
func (s *Server) Tools(ctx context.Context) (tools []Tool, missing []string, err error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	cs, err := s.connect(ctx)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for t, err := range cs.Tools(ctx, nil) {
		if err != nil {
			s.drop(cs)
			return nil, nil, s.fail("list its tools", err)
		}
		conf, ok := s.cfg.Tools[t.Name]
		if !ok || seen[t.Name] || !objectSchema(t.InputSchema) {
			continue
		}
		seen[t.Name] = true
		tools = append(tools, Tool{
			Name: config.ToolName(s.cfg.Name, t.Name), Upstream: s.cfg.Name, Remote: t.Name,
			Description: s.describe(t.Description), InputSchema: t.InputSchema,
			Tags: conf.Tags, Risk: conf.Risk,
		})
	}
	for name := range s.cfg.Tools {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	sort.Strings(missing)
	return tools, missing, nil
}

// Call calls the server's tool remote with args. A call is made once: one that
// failed in transport may have run, so it is never repeated.
func (s *Server) Call(ctx context.Context, remote string, args any) (*mcp.CallToolResult, error) {
	if _, ok := s.cfg.Tools[remote]; !ok {
		return nil, fmt.Errorf("upstream %q, tool %q: %w", s.cfg.Name, remote, ErrNotOffered)
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	cs, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: remote, Arguments: args})
	if err != nil {
		s.drop(cs)
		return nil, s.fail("call "+remote, err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		return nil, s.fail("call "+remote, err)
	}
	if len(b) > MaxResult {
		return nil, fmt.Errorf("upstream %q, tool %q: %w (%d bytes, at most %d)", s.cfg.Name, remote, ErrTooLarge, len(b), MaxResult)
	}
	return res, nil
}

// Close ends the session, if there is one.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil {
		return nil
	}
	err := s.session.Close()
	s.session = nil
	return err
}

func (s *Server) connect(ctx context.Context) (*mcp.ClientSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		return s.session, nil
	}
	cs, err := s.client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: s.cfg.URL, HTTPClient: s.httpc, MaxRetries: -1}, nil)
	if err != nil {
		return nil, s.fail("connect", err)
	}
	s.session = cs
	return cs, nil
}

// drop forgets a session that failed, so the next use connects afresh.
func (s *Server) drop(cs *mcp.ClientSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == cs {
		_ = cs.Close()
		s.session = nil
	}
}

func (s *Server) fail(what string, err error) error {
	msg := err.Error()
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return fmt.Errorf("upstream %q could not %s: %s", s.cfg.Name, what, msg)
}

// objectSchema reports whether an input schema describes a JSON object, the only
// kind of input an MCP tool takes.
func objectSchema(schema any) bool {
	b, err := json.Marshal(schema)
	if err != nil {
		return false
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	return m["type"] == "object"
}

// Offered is every tool the configuration offers, as the configuration alone
// says: names, tags and risk, without anything only a server can say.
func Offered(cfg []config.MCPUpstream) []Tool {
	var tools []Tool
	for _, u := range cfg {
		for remote, t := range u.Tools {
			tools = append(tools, Tool{Name: config.ToolName(u.Name, remote), Upstream: u.Name, Remote: remote, Tags: t.Tags, Risk: t.Risk})
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

func (s *Server) describe(d string) string {
	if len(d) > MaxDescription {
		d = strings.ToValidUTF8(d[:MaxDescription], "") + "…"
	}
	return fmt.Sprintf("[from upstream %s] %s", s.cfg.Name, d)
}

// bearer presents the server's credential on every request.
type bearer struct {
	token string
	// origin is the configured server's: the only one the token is sent to.
	origin string
	next   http.RoundTripper
}

func (b *bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	if b.token == "" || b.origin == "" || originOf(r.URL.String()) != b.origin {
		return b.next.RoundTrip(r)
	}
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

// capped refuses a response body past max bytes.
type capped struct {
	next http.RoundTripper
	max  int64
}

func (c capped) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = &cappedBody{rc: resp.Body, left: c.max}
	return resp, nil
}

type cappedBody struct {
	rc   io.ReadCloser
	left int64
}

var errTooLargeResponse = errors.New("the server's response is larger than pao reads")

func (c *cappedBody) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errTooLargeResponse
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.rc.Read(p)
	c.left -= int64(n)
	return n, err
}

func (c *cappedBody) Close() error { return c.rc.Close() }
