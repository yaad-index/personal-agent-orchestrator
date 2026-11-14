package local

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// WatchLineJSON is one line of a watch: an entry of an agent's output, rendered.
type WatchLineJSON struct {
	// Time is RFC 3339, empty when the entry has none.
	Time string `json:"time,omitempty"`
	Text string `json:"text"`
	// History is true for the recent past sent first, false for output that arrived
	// after the watch began.
	History bool `json:"history,omitempty"`
}

// WatchAPI is the operator's on-demand view of an agent's output (ADR-0018), on
// this node's agents and, at the master, on agents of other nodes. The server calls
// it for the operator, the only caller the bearer token identifies.
type WatchAPI interface {
	// Watch calls opened once the watch is admitted, then emit for each line, until
	// ctx is done (a nil return) or the watch cannot go on. An error before opened
	// is why it was never admitted.
	Watch(ctx context.Context, agent string, level, entries int, opened func(), emit func(WatchLineJSON) error) error
}

// WithWatch serves the watch route.
func WithWatch(a WatchAPI) Option { return func(s *Server) { s.watch = a } }

func (s *Server) registerWatch(mux *http.ServeMux) {
	if s.watch == nil {
		return
	}
	mux.HandleFunc("GET /v1/watch/{agent}", s.auth(s.watchAgent))
}

// watchAgent streams the agent's output as server-sent events until the client goes
// away, which is how the operator stops a watch. The lines go to this request only:
// they are not sent to the channel's other streams and are kept in no agent's scope.
func (s *Server) watchAgent(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	q := r.URL.Query()
	level, entries := 0, 0
	for name, dst := range map[string]*int{"level": &level, "entries": &entries} {
		if v := q.Get(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				writeError(w, http.StatusBadRequest, name+" is a number")
				return
			}
			*dst = n
		}
	}

	var (
		mu   sync.Mutex
		done = make(chan struct{})
	)
	defer close(done)
	write := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprintf(w, format, args...)
		fl.Flush()
	}
	opened := func() {
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		mu.Lock()
		w.WriteHeader(http.StatusOK)
		mu.Unlock()
		write(": watching\n\n")
		if s.heartbeat > 0 {
			go func() {
				t := time.NewTicker(s.heartbeat)
				defer t.Stop()
				for {
					select {
					case <-done:
						return
					case <-t.C:
						write(": keepalive\n\n")
					}
				}
			}()
		}
	}
	started := false
	err := s.watch.Watch(r.Context(), r.PathValue("agent"), level, entries, func() { started = true; opened() }, func(l WatchLineJSON) error {
		b, err := json.Marshal(l)
		if err != nil {
			return err
		}
		write("event: line\ndata: %s\n\n", b)
		return r.Context().Err()
	})
	switch {
	case err == nil || r.Context().Err() != nil:
	case !started:
		writeError(w, tempStatus(err), err.Error())
	default:
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		write("event: end\ndata: %s\n\n", b)
	}
}

// Watch calls fn for each line of the agent's output, the recent past first, until
// ctx is done (a nil return) or the server ends the watch, which it reports as an
// error with the server's words.
func (c *Client) Watch(ctx context.Context, agent string, level, entries int, fn func(WatchLineJSON)) error {
	q := url.Values{}
	if level > 0 {
		q.Set("level", strconv.Itoa(level))
	}
	if entries > 0 {
		q.Set("entries", strconv.Itoa(entries))
	}
	u := c.URL + "/v1/watch/" + url.PathEscape(agent)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.http().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		if e, ok := strings.CutPrefix(line, "event: "); ok {
			event = e
			continue
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		switch event {
		case "line":
			var l WatchLineJSON
			if json.Unmarshal([]byte(data), &l) == nil {
				fn(l)
			}
		case "end":
			var e struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal([]byte(data), &e)
			if e.Error == "" {
				e.Error = "the watch ended"
			}
			return errors.New(e.Error)
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}
