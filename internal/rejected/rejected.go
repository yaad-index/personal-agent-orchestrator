// Package rejected is the record of inbounds pao did not act on (ADR-0019 point
// 7, ADR-0020 points 7 and 8): a message from a sender that resolves to no
// subject, one outside the sender's access, and a registration attempt.
//
// It holds one entry per (channel, sender), coalesced, so a flood from one
// sender costs one entry; it is capped, so a flood of distinct senders costs a
// bounded amount and keeps the most recent; and entries age out. It is inert by
// construction: nothing here imports routing or the communication layer, and
// the only way out is the operator's listing.
package rejected

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

// Kind says what was rejected.
type Kind string

const (
	// Message is an inbound message pao did not act on.
	Message Kind = "message"
	// Registration is an attempt at the registration entry point. Its payload
	// carries the secret the sender presented, so it is never kept.
	Registration Kind = "registration"
)

// Defaults for Options (ADR-0020 point 7).
const (
	DefaultMaxEntries = 1000
	DefaultMaxPayload = 4 << 10
	DefaultRetention  = 30 * 24 * time.Hour
)

// ErrInvalid means a record was refused: an unknown kind, or no channel or sender.
var ErrInvalid = errors.New("invalid record")

// Options bound the record. A zero field takes its default.
type Options struct {
	MaxEntries int
	MaxPayload int
	Retention  time.Duration
}

// Entry is what is known of one (channel, sender).
type Entry struct {
	Channel string `json:"channel"`
	Sender  string `json:"sender"`
	// Kind and Reason are those of the most recent rejection.
	Kind   Kind      `json:"kind"`
	Reason string    `json:"reason"`
	First  time.Time `json:"first"`
	Last   time.Time `json:"last"`
	Count  int       `json:"count"`
	// Payload is the most recent raw payload, truncated, and empty for a
	// registration attempt.
	Payload string `json:"payload"`
}

var bucket = []byte("rejected")

// Store is safe for concurrent use.
type Store struct {
	db  *bolt.DB
	opt Options
	now func() time.Time
}

// Open opens (creating if needed) the record at path.
func Open(path string, opt Options) (*Store, error) {
	if opt.MaxEntries <= 0 {
		opt.MaxEntries = DefaultMaxEntries
	}
	if opt.MaxPayload <= 0 {
		opt.MaxPayload = DefaultMaxPayload
	}
	if opt.Retention <= 0 {
		opt.Retention = DefaultRetention
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
	return &Store{db: db, opt: opt, now: time.Now}, nil
}

// Close closes the record.
func (s *Store) Close() error { return s.db.Close() }

func key(channel, sender string) []byte { return []byte(channel + "\x00" + sender) }

// truncate cuts p to at most n bytes without splitting a character.
func truncate(p string, n int) string {
	if len(p) <= n {
		return p
	}
	p = p[:n]
	for len(p) > 0 && !utf8.ValidString(p) {
		p = p[:len(p)-1]
	}
	return p
}

// Record notes a rejection. A repeat from the same (channel, sender) updates
// its entry. A new sender at the cap displaces the entry last seen longest ago.
// A registration attempt's payload is discarded here, so the secret it carried
// cannot be stored whatever the caller passes.
func (s *Store) Record(channel, sender string, kind Kind, reason, payload string) error {
	if channel == "" || sender == "" || strings.ContainsRune(channel, 0) {
		return fmt.Errorf("%w: channel and sender are required", ErrInvalid)
	}
	switch kind {
	case Message:
		payload = truncate(payload, s.opt.MaxPayload)
	case Registration:
		payload = ""
	default:
		return fmt.Errorf("%w: kind %q", ErrInvalid, kind)
	}
	now := s.now().UTC()
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		if err := s.prune(b, now); err != nil {
			return err
		}
		k := key(channel, sender)
		e := Entry{Channel: channel, Sender: sender, First: now}
		if v := b.Get(k); v != nil {
			if err := json.Unmarshal(v, &e); err != nil {
				return err
			}
		} else if err := s.makeRoom(b); err != nil {
			return err
		}
		e.Kind, e.Reason, e.Last, e.Payload = kind, reason, now, payload
		e.Count++
		raw, err := json.Marshal(e)
		if err != nil {
			return err
		}
		return b.Put(k, raw)
	})
}

// prune drops the entries last seen before the retention period.
func (s *Store) prune(b *bolt.Bucket, now time.Time) error {
	var drop [][]byte
	err := b.ForEach(func(k, v []byte) error {
		var e Entry
		if err := json.Unmarshal(v, &e); err != nil {
			return err
		}
		if now.Sub(e.Last) > s.opt.Retention {
			drop = append(drop, append([]byte(nil), k...))
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, k := range drop {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// makeRoom drops the entry last seen longest ago while the record is full.
func (s *Store) makeRoom(b *bolt.Bucket) error {
	for {
		var oldest []byte
		var at time.Time
		n := 0
		err := b.ForEach(func(k, v []byte) error {
			n++
			var e Entry
			if err := json.Unmarshal(v, &e); err != nil {
				return err
			}
			if oldest == nil || e.Last.Before(at) {
				oldest, at = append(oldest[:0], k...), e.Last
			}
			return nil
		})
		if err != nil {
			return err
		}
		if n < s.opt.MaxEntries {
			return nil
		}
		if err := b.Delete(oldest); err != nil {
			return err
		}
	}
}

// List returns the entries, the most recently seen first.
func (s *Store) List() ([]Entry, error) {
	var out []Entry
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucket).ForEach(func(_, v []byte) error {
			var e Entry
			if err := json.Unmarshal(v, &e); err != nil {
				return err
			}
			if s.now().UTC().Sub(e.Last) <= s.opt.Retention {
				out = append(out, e)
			}
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	return out, err
}
