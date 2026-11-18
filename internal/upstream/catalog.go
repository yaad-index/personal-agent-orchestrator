package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

// Catalog is the tools of every upstream server pao reaches. Each server's tools
// are listed from it once, on first need, and kept; a server that could not be
// listed offers nothing and is tried again on a later need, at most once per
// retry interval, so a server that is down does not hold up every listing. It is
// safe for concurrent use.
type Catalog struct {
	entries []*entry
	byName  map[string]offered
	// ListTimeout bounds listing one server's tools; Retry is how long after a
	// failed listing a server is left alone. Zero means the default.
	ListTimeout time.Duration
	Retry       time.Duration
	now         func() time.Time
}

// Defaults for a Catalog.
const (
	DefaultListTimeout = 10 * time.Second
	DefaultRetry       = 30 * time.Second
)

type offered struct {
	entry  *entry
	remote string
}

type entry struct {
	srv *Server

	mu      sync.Mutex
	listed  bool
	tools   []Tool
	lastTry time.Time
}

// NewCatalog returns the catalog of servers. Nothing is listed until it is used.
func NewCatalog(servers ...*Server) *Catalog {
	c := &Catalog{byName: map[string]offered{}, now: time.Now}
	for _, s := range servers {
		e := &entry{srv: s}
		c.entries = append(c.entries, e)
		for remote := range s.cfg.Tools {
			c.byName[config.ToolName(s.cfg.Name, remote)] = offered{entry: e, remote: remote}
		}
	}
	return c
}

// Tools lists the tools on offer from every server that could be listed, server
// by server in configuration order.
func (c *Catalog) Tools(ctx context.Context) []Tool {
	lists := make([][]Tool, len(c.entries))
	var wg sync.WaitGroup
	for i, e := range c.entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lists[i] = c.list(ctx, e)
		}()
	}
	wg.Wait()
	var out []Tool
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

func (c *Catalog) list(ctx context.Context, e *entry) []Tool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.listed {
		return e.tools
	}
	retry := c.Retry
	if retry == 0 {
		retry = DefaultRetry
	}
	if !e.lastTry.IsZero() && c.now().Sub(e.lastTry) < retry {
		return nil
	}
	e.lastTry = c.now()
	timeout := c.ListTimeout
	if timeout == 0 {
		timeout = DefaultListTimeout
	}
	// The listing serves every later caller, so one caller giving up does not
	// cut it short; the timeout bounds it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	tools, missing, err := e.srv.Tools(ctx)
	if err != nil {
		slog.Warn("an upstream server's tools could not be listed; it offers none until it can be", "upstream", e.srv.Name(), "error", err)
		return nil
	}
	if len(missing) > 0 {
		slog.Warn("configured upstream tools are not offered: the server does not have them, or not with an object input schema", "upstream", e.srv.Name(), "tools", missing)
	}
	e.listed, e.tools = true, tools
	return tools
}

// Call calls the tool of that pao-side name with args, once.
func (c *Catalog) Call(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	o, ok := c.byName[name]
	if !ok {
		return nil, fmt.Errorf("tool %q: %w", name, ErrNotOffered)
	}
	var a any
	if len(args) > 0 {
		a = args
	}
	return o.entry.srv.Call(ctx, o.remote, a)
}

// Close ends every server's session.
func (c *Catalog) Close() error {
	var first error
	for _, e := range c.entries {
		if err := e.srv.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
