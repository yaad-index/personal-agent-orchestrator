// Package msgstore keeps pao's rolling map of recent messages (ADR-0010): each
// message gets an id, is stored for the agents it concerns, and can be found
// again by id or by a quoted snippet of its text. Only the most recent window
// of messages is kept; an older one falls out of the map, and a reference to it
// is then unresolved rather than guessed.
package msgstore

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

var (
	// ErrNotFound means no message with that id is in the window.
	ErrNotFound = errors.New("message not found")
	// ErrUnresolved means a quote matched no message in the window.
	ErrUnresolved = errors.New("reference not resolved: no message matches")
	// ErrAmbiguous means a quote matched more than one message.
	ErrAmbiguous = errors.New("reference not resolved: more than one message matches")
)

var bucket = []byte("messages")

// record is what is stored: the message and the agents it was delivered to or
// came from. An agent can only quote what it was shown, so a quote is resolved
// within one agent's scope.
type record struct {
	Scopes  []string     `json:"scopes"`
	Message core.Message `json:"message"`
}

// Store is safe for concurrent use.
type Store struct {
	db     *bolt.DB
	window int
	now    func() time.Time
}

// Open opens (creating if needed) the store at path, keeping the most recent
// window messages.
func Open(path string, window int) (*Store, error) {
	if window < 1 {
		return nil, errors.New("window must be at least 1")
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucket)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, window: window, now: time.Now}, nil
}

// Close closes the store.
func (s *Store) Close() error { return s.db.Close() }

// Add assigns m an id (and a time, if it has none), stores it for the given
// agents, drops whatever the window no longer holds, and returns the stored
// message.
func (s *Store) Add(m core.Message, scopes ...string) (core.Message, error) {
	if m.Kind == "" {
		m.Kind = core.MessageContent
	}
	if m.Time.IsZero() {
		m.Time = s.now().UTC()
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		seq, err := b.NextSequence()
		if err != nil {
			return err
		}
		m.ID = formatID(seq)
		val, err := json.Marshal(record{Scopes: scopes, Message: m})
		if err != nil {
			return err
		}
		if err := b.Put(key(seq), val); err != nil {
			return err
		}
		return prune(b, s.window)
	})
	return m, err
}

// prune deletes everything but the newest window records. Keys are big-endian
// sequence numbers, so the cursor walks oldest first.
func prune(b *bolt.Bucket, window int) error {
	c := b.Cursor()
	oldestKept, _ := c.Last()
	for i := 1; i < window && oldestKept != nil; i++ {
		oldestKept, _ = c.Prev()
	}
	if oldestKept == nil {
		return nil // fewer than window records
	}
	oldestKept = append([]byte(nil), oldestKept...)
	for k, _ := c.First(); k != nil && string(k) < string(oldestKept); k, _ = c.First() {
		if err := c.Delete(); err != nil {
			return err
		}
	}
	return nil
}

// Get returns the message with the given id.
func (s *Store) Get(id string) (core.Message, error) {
	var rec record
	err := s.db.View(func(tx *bolt.Tx) error {
		return load(tx.Bucket(bucket), id, &rec)
	})
	return rec.Message, err
}

// GetScoped returns the message with the given id only if it is in agent's
// scope: an agent can fetch what it was shown or wrote, not another agent's
// messages. Out of scope looks exactly like not found.
func (s *Store) GetScoped(agent, id string) (core.Message, error) {
	var rec record
	err := s.db.View(func(tx *bolt.Tx) error {
		return load(tx.Bucket(bucket), id, &rec)
	})
	if err != nil {
		return core.Message{}, err
	}
	if !inScope(rec.Scopes, agent) {
		return core.Message{}, ErrNotFound
	}
	return rec.Message, nil
}

// Resolve finds the one message in agent's scope whose text contains quote,
// compared with runs of whitespace collapsed. No match is ErrUnresolved and
// more than one is ErrAmbiguous: the caller tells the agent, who re-quotes,
// and nothing is guessed.
func (s *Store) Resolve(agent, quote string) (core.Message, error) {
	q := collapse(quote)
	if q == "" {
		return core.Message{}, ErrUnresolved
	}
	var found []core.Message
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucket).ForEach(func(_, v []byte) error {
			var rec record
			if err := json.Unmarshal(v, &rec); err != nil {
				return err
			}
			if !inScope(rec.Scopes, agent) {
				return nil
			}
			if strings.Contains(collapse(rec.Message.Text), q) {
				found = append(found, rec.Message)
			}
			return nil
		})
	})
	if err != nil {
		return core.Message{}, err
	}
	switch len(found) {
	case 0:
		return core.Message{}, ErrUnresolved
	case 1:
		return found[0], nil
	default:
		return core.Message{}, ErrAmbiguous
	}
}

// React records that by reacted to message id with emoji, whoever owns the
// message. It is for the human and for pao, which see every message; an agent
// reacts through ReactAs, which keeps it inside its own scope. Reacting twice
// with the same emoji is the same reaction.
func (s *Store) React(id, by, emoji string) error {
	return s.updateReactions("", id, func(rs []core.Reaction) []core.Reaction {
		return addReaction(rs, core.Reaction{By: by, Emoji: emoji})
	})
}

// ReactAs is React for an agent: the message must be in the agent's scope, and
// out of scope looks exactly like not found, so an agent cannot tell a message
// it was never shown from one that does not exist.
func (s *Store) ReactAs(agent, id, emoji string) error {
	return s.updateReactions(agent, id, func(rs []core.Reaction) []core.Reaction {
		return addReaction(rs, core.Reaction{By: agent, Emoji: emoji})
	})
}

// UnreactAs removes the agent's own reaction, under the same scope rule.
func (s *Store) UnreactAs(agent, id, emoji string) error {
	return s.updateReactions(agent, id, func(rs []core.Reaction) []core.Reaction {
		return dropReaction(rs, core.Reaction{By: agent, Emoji: emoji})
	})
}

// Unreact removes by's reaction with emoji from message id, if present. Like
// React it is for the human and pao.
func (s *Store) Unreact(id, by, emoji string) error {
	return s.updateReactions("", id, func(rs []core.Reaction) []core.Reaction {
		return dropReaction(rs, core.Reaction{By: by, Emoji: emoji})
	})
}

func addReaction(rs []core.Reaction, r core.Reaction) []core.Reaction {
	for _, have := range rs {
		if have == r {
			return rs
		}
	}
	return append(rs, r)
}

func dropReaction(rs []core.Reaction, r core.Reaction) []core.Reaction {
	out := rs[:0:0]
	for _, have := range rs {
		if have != r {
			out = append(out, have)
		}
	}
	return out
}

// updateReactions applies f to the message's reactions. A non-empty agent
// restricts it to messages in that agent's scope.
func (s *Store) updateReactions(agent, id string, f func([]core.Reaction) []core.Reaction) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		var rec record
		if err := load(b, id, &rec); err != nil {
			return err
		}
		if agent != "" && !inScope(rec.Scopes, agent) {
			return ErrNotFound
		}
		rec.Message.Reactions = f(rec.Message.Reactions)
		val, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		seq, _ := parseID(id)
		return b.Put(key(seq), val)
	})
}

func load(b *bolt.Bucket, id string, into *record) error {
	seq, ok := parseID(id)
	if !ok {
		return ErrNotFound
	}
	v := b.Get(key(seq))
	if v == nil {
		return ErrNotFound
	}
	return json.Unmarshal(v, into)
}

func inScope(scopes []string, agent string) bool {
	for _, s := range scopes {
		if s == agent {
			return true
		}
	}
	return false
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func key(seq uint64) []byte {
	k := make([]byte, 8)
	binary.BigEndian.PutUint64(k, seq)
	return k
}

func formatID(seq uint64) string { return fmt.Sprintf("m%d", seq) }

func parseID(id string) (uint64, bool) {
	var seq uint64
	if len(id) < 2 || id[0] != 'm' {
		return 0, false
	}
	if _, err := fmt.Sscanf(id[1:], "%d", &seq); err != nil || formatID(seq) != id {
		return 0, false
	}
	return seq, true
}
