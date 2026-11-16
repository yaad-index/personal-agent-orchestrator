package comm

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// The composite message id (ADR-0021) names a message on a channel as
// "<channel-id>-<native-id>": the channel's numeric id, then the platform's own id
// for the message, opaque and kept as given. It sits beside the pao id, which stays
// canonical; the layer keeps a two-way map between the two over a bounded window,
// and the adapter that owns the channel strips the prefix to act back on the
// original message.

// DefaultCompositeWindow is how many composites the map keeps when none is set.
const DefaultCompositeWindow = 500

// NativeSender is a channel that reports the platform's id for a message it sent,
// so a reply or reaction to that message can be found again.
type NativeSender interface {
	Channel
	// SendNative sends m and returns the platform's own id for each copy it sent
	// (one per recipient), none when the platform gave none.
	SendNative(ctx context.Context, m core.Message) (natives []string, err error)
}

// FormatComposite builds "<channel-id>-<native-id>".
func FormatComposite(channelID int, native string) string {
	return strconv.Itoa(channelID) + "-" + native
}

// ParseComposite splits a composite on its first "-". The channel id before it is
// all digits and positive; the native id after it is non-empty and opaque, so it
// may itself contain "-" or ":". Anything else is not a composite.
func ParseComposite(s string) (channelID int, native string, ok bool) {
	head, native, found := strings.Cut(s, "-")
	if !found || native == "" || head == "" || head[0] == '0' {
		return 0, "", false
	}
	for _, r := range head {
		if r < '0' || r > '9' {
			return 0, "", false
		}
	}
	n, err := strconv.Atoi(head)
	if err != nil {
		return 0, "", false
	}
	return n, native, true
}

// compositeMap is the two-way, bounded map between composite and pao id. Several
// composites can name one pao message, as when pao sends it to several channels;
// each resolves to it, and the id's own composite is the latest. The oldest
// composite falls out first.
type compositeMap struct {
	mu     sync.Mutex
	window int
	ids    map[string]string // composite -> pao id
	back   map[string]string // pao id -> composite
	order  []string          // composites, oldest first
}

func newCompositeMap(window int) *compositeMap {
	return &compositeMap{window: window, ids: map[string]string{}, back: map[string]string{}}
}

func (c *compositeMap) put(composite, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.ids[composite]; !ok {
		c.order = append(c.order, composite)
	}
	c.ids[composite], c.back[id] = id, composite
	for len(c.order) > c.window {
		oldest := c.order[0]
		c.order = c.order[1:]
		if id := c.ids[oldest]; c.back[id] == oldest {
			delete(c.back, id)
		}
		delete(c.ids, oldest)
	}
}

func (c *compositeMap) id(composite string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.ids[composite]
	return id, ok
}

func (c *compositeMap) composite(id string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.back[id]
	return s, ok
}

// SetChannelIDs gives the layer each channel's numeric id by name (ADR-0021
// points 1 and 2). Call it before the layer starts receiving.
func (l *Layer) SetChannelIDs(ids map[string]int) {
	l.channelIDs = make(map[string]int, len(ids))
	for n, id := range ids {
		l.channelIDs[n] = id
	}
}

// SetCompositeWindow sets how many composites the map keeps. Call it before the
// layer starts receiving; a value below 1 leaves the default.
func (l *Layer) SetCompositeWindow(n int) {
	if n >= 1 {
		l.composites = newCompositeMap(n)
	}
}

// noteComposite records the composite of a message a channel sent or received. A
// channel with no id, or a message with no native id, is not recorded.
func (l *Layer) noteComposite(channel, native, id string) {
	n, ok := l.channelIDs[channel]
	if !ok || native == "" || id == "" {
		return
	}
	l.composites.put(FormatComposite(n, native), id)
}

// CompositeOf returns the composite of a pao message id, qualified by this node in
// a mesh, when the message is on a channel and still in the window.
func (l *Layer) CompositeOf(id string) (string, bool) {
	c, ok := l.composites.composite(id)
	if !ok {
		return "", false
	}
	return l.Qualify(c), true
}

// splitComposite reads the node qualifier off a composite, if there is one. The
// part before the first ":" is a node only if it is this node or one of the mesh;
// otherwise the whole string is unqualified, because a native id may itself hold a
// ":". node is empty when unqualified.
func (l *Layer) splitComposite(s string) (node, rest string) {
	n, r, found := strings.Cut(s, ":")
	if found && l.self != "" && (n == l.self || (l.known != nil && l.known(n))) {
		return n, r
	}
	return "", s
}

// ResolveComposite returns the pao id of the message a composite names. A
// composite outside the window, one naming a channel that is not configured, one
// held by another node, and a string that is no composite all give the same answer:
// not found (ADR-0021 point 6).
func (l *Layer) ResolveComposite(s string) (string, bool) {
	node, rest := l.splitComposite(s)
	if node != "" && node != l.self {
		return "", false
	}
	n, native, ok := ParseComposite(rest)
	if !ok {
		return "", false
	}
	return l.composites.id(FormatComposite(n, native))
}

// NativeOf returns the platform's own id from a composite, only if the composite
// carries the named channel's id: a channel acts only on composites of its own
// (ADR-0021 point 4).
func (l *Layer) NativeOf(channel, s string) (string, bool) {
	node, rest := l.splitComposite(s)
	if node != "" && node != l.self {
		return "", false
	}
	n, native, ok := ParseComposite(rest)
	if !ok {
		return "", false
	}
	if id, has := l.channelIDs[channel]; !has || id != n {
		return "", false
	}
	return native, true
}

// deliver hands m to a channel and, when the channel reports the platform's id for
// it, notes the composite.
func (l *Layer) deliver(ctx context.Context, c Channel, m core.Message) error {
	if ns, ok := c.(NativeSender); ok {
		natives, err := ns.SendNative(ctx, m)
		for _, native := range natives {
			l.noteComposite(c.Name(), native, m.ID)
		}
		return err
	}
	return c.Send(ctx, m)
}
