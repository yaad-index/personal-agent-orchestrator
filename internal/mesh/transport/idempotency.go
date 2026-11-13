package transport

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// idempotency makes a state-changing call run once however often it is sent. A
// call is identified by who made it, which call it is, and its request id (field 1
// of the request, named request_id). A repeat returns the first result; a repeat
// that arrives while the first is still running waits for it. A first attempt that
// failed in a way a retry could fix (the caller gave up, or the node was
// unreachable) is not remembered, so the retry does run.
type idempotency struct {
	max int
	ttl time.Duration

	mu      sync.Mutex
	entries map[idemKey]*idemEntry
	order   []idemKey
	now     func() time.Time
}

type idemKey struct{ caller, method, id string }

type idemEntry struct {
	done chan struct{}
	resp proto.Message
	err  error
	at   time.Time
}

func newIdempotency(max int, ttl time.Duration) *idempotency {
	return &idempotency{max: max, ttl: ttl, entries: make(map[idemKey]*idemEntry), now: time.Now}
}

// requestID returns the request id a request carries, if it has one.
func requestID(req any) string {
	m, ok := req.(proto.Message)
	if !ok {
		return ""
	}
	f := m.ProtoReflect().Descriptor().Fields().ByName("request_id")
	if f == nil || f.Kind() != protoreflect.StringKind || f.Number() != 1 {
		return ""
	}
	return m.ProtoReflect().Get(f).String()
}

func (c *idempotency) do(ctx context.Context, caller, method string, req any, handler grpc.UnaryHandler) (any, error) {
	id := requestID(req)
	if id == "" {
		return handler(ctx, req)
	}
	key := idemKey{caller, method, id}

	c.mu.Lock()
	c.expireLocked()
	if e, ok := c.entries[key]; ok {
		c.mu.Unlock()
		select {
		case <-e.done:
			return e.resp, e.err
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	}
	e := &idemEntry{done: make(chan struct{}), at: c.now()}
	c.entries[key] = e
	c.order = append(c.order, key)
	c.evictLocked()
	c.mu.Unlock()

	resp, err := handler(ctx, req)
	m, _ := resp.(proto.Message)
	e.resp, e.err = m, err
	if retryable(err) {
		c.mu.Lock()
		if c.entries[key] == e {
			delete(c.entries, key)
		}
		c.mu.Unlock()
	}
	close(e.done)
	if m == nil {
		return resp, err
	}
	return m, err
}

// retryable reports whether a failed first attempt should be run again.
func retryable(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Aborted, codes.ResourceExhausted:
		return true
	}
	return false
}

func (c *idempotency) expireLocked() {
	cut := c.now().Add(-c.ttl)
	for len(c.order) > 0 {
		k := c.order[0]
		e, ok := c.entries[k]
		if ok && e.at.After(cut) {
			return
		}
		if ok {
			delete(c.entries, k)
		}
		c.order = c.order[1:]
	}
}

func (c *idempotency) evictLocked() {
	for len(c.entries) > c.max && len(c.order) > 0 {
		k := c.order[0]
		delete(c.entries, k)
		c.order = c.order[1:]
	}
}
