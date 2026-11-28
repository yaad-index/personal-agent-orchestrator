// Package identity holds who the humans are and which external sender ids are
// theirs (ADR-0019 points 3 to 5). A human is a subject (ADR-0012); a binding
// ties one (channel, per-sender id) pair to exactly one subject. The pair is
// created at registration, and the operator can revoke it. An id with no binding
// resolves to nobody.
package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	// ErrNoSubject means no subject has that name.
	ErrNoSubject = errors.New("no such subject")
	// ErrSubjectExists means a subject already has that name.
	ErrSubjectExists = errors.New("subject already exists")
	// ErrBound means the (channel, sender) pair is already bound to a subject.
	ErrBound = errors.New("sender already bound")
	// ErrNoBinding means the (channel, sender) pair is not bound.
	ErrNoBinding = errors.New("no such binding")
	// ErrInvalid means a name, channel or sender id is not acceptable.
	ErrInvalid = errors.New("invalid value")
)

var (
	subjects = []byte("subjects") // subject name -> record
	bindings = []byte("bindings") // channel NUL sender -> subject name

	nameRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	channelRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
)

// MaxSenderLen bounds a per-sender id. It is opaque to pao: the platform's own
// authenticated value, kept exactly as given.
const MaxSenderLen = 256

// Binding is one (channel, sender) pair and the subject it resolves to.
type Binding struct {
	Channel string
	Sender  string
	Subject string
}

// Subject is a human known to pao.
type Subject struct {
	Name    string
	Created time.Time
	// Operator marks a human acting as the operator (ADR-0022). Only the operator
	// surface sets it.
	Operator bool
}

// record is what is stored for a subject.
type record struct {
	Created  time.Time `json:"created"`
	Operator bool      `json:"operator,omitempty"`
}

func (r record) bytes() []byte {
	b, _ := json.Marshal(r)
	return b
}

func parseRecord(v []byte) record {
	var r record
	if json.Unmarshal(v, &r) != nil {
		return record{}
	}
	return r
}

// Store is safe for concurrent use.
type Store struct {
	db  *bolt.DB
	now func() time.Time
	lim *limiter
	// compares counts the digest comparisons registration makes, so a test can
	// hold every outcome to the same work.
	compares atomic.Int64
}

// Open opens (creating if needed) the store at path.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{subjects, bindings, invites} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, now: time.Now, lim: &limiter{failures: map[string][]time.Time{}, max: DefaultFailures, window: DefaultWindow, track: maxTracked}}, nil
}

// Close closes the store.
func (s *Store) Close() error { return s.db.Close() }

func checkName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("%w: subject name %q", ErrInvalid, name)
	}
	return nil
}

func bindingKey(channel, sender string) ([]byte, error) {
	if !channelRE.MatchString(channel) {
		return nil, fmt.Errorf("%w: channel %q", ErrInvalid, channel)
	}
	if sender == "" || len(sender) > MaxSenderLen || strings.ContainsRune(sender, 0) {
		return nil, fmt.Errorf("%w: sender id", ErrInvalid)
	}
	return []byte(channel + "\x00" + sender), nil
}

// AddSubject creates a subject.
func (s *Store) AddSubject(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(subjects)
		if b.Get([]byte(name)) != nil {
			return ErrSubjectExists
		}
		return b.Put([]byte(name), record{Created: s.now().UTC()}.bytes())
	})
}

// Subjects lists every subject, by name.
func (s *Store) Subjects() ([]Subject, error) {
	var out []Subject
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(subjects).ForEach(func(k, v []byte) error {
			r := parseRecord(v)
			out = append(out, Subject{Name: string(k), Created: r.Created, Operator: r.Operator})
			return nil
		})
	})
	return out, err
}

// RemoveSubject deletes a subject and every binding that resolves to it.
func (s *Store) RemoveSubject(name string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		sb := tx.Bucket(subjects)
		if sb.Get([]byte(name)) == nil {
			return ErrNoSubject
		}
		bb := tx.Bucket(bindings)
		var drop [][]byte
		if err := bb.ForEach(func(k, v []byte) error {
			if string(v) == name {
				drop = append(drop, append([]byte(nil), k...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, k := range drop {
			if err := bb.Delete(k); err != nil {
				return err
			}
		}
		return sb.Delete([]byte(name))
	})
}

// Bind ties (channel, sender) to the subject. A pair that is already bound,
// even to the same subject, is refused: moving an id is a revoke and a bind, so
// it is always the operator's visible act.
func (s *Store) Bind(channel, sender, subject string) error {
	key, err := bindingKey(channel, sender)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(subjects).Get([]byte(subject)) == nil {
			return ErrNoSubject
		}
		bb := tx.Bucket(bindings)
		if bb.Get(key) != nil {
			return ErrBound
		}
		return bb.Put(key, []byte(subject))
	})
}

// Revoke removes a binding; the pair falls back to unbound.
func (s *Store) Revoke(channel, sender string) error {
	key, err := bindingKey(channel, sender)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bb := tx.Bucket(bindings)
		if bb.Get(key) == nil {
			return ErrNoBinding
		}
		return bb.Delete(key)
	})
}

// Resolve returns the subject a (channel, sender) pair is bound to. An unbound
// pair, and one that is not even well-formed, resolve to nobody and the same
// answer: ok is false.
func (s *Store) Resolve(channel, sender string) (subject string, ok bool) {
	key, err := bindingKey(channel, sender)
	if err != nil {
		return "", false
	}
	_ = s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(bindings).Get(key); v != nil {
			subject, ok = string(v), true
		}
		return nil
	})
	return subject, ok
}

// Bindings lists every binding, optionally only those of one subject.
func (s *Store) Bindings(subject string) ([]Binding, error) {
	var out []Binding
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bindings).ForEach(func(k, v []byte) error {
			if subject != "" && string(v) != subject {
				return nil
			}
			ch, snd, _ := strings.Cut(string(k), "\x00")
			out = append(out, Binding{Channel: ch, Sender: snd, Subject: string(v)})
			return nil
		})
	})
	return out, err
}

// SetOperator flags a subject as a human acting as the operator, or clears the
// flag (ADR-0022 points 1 and 2). It is the only way the flag changes, and it is
// reached from the operator's surface alone.
func (s *Store) SetOperator(name string, on bool) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(subjects)
		v := b.Get([]byte(name))
		if v == nil {
			return ErrNoSubject
		}
		r := parseRecord(v)
		r.Operator = on
		return b.Put([]byte(name), r.bytes())
	})
}

// IsOperator reports whether the subject exists and is flagged operator. It is
// read afresh for each inbound (ADR-0022 point 5); a subject that does not exist
// is not an operator.
func (s *Store) IsOperator(name string) bool {
	op := false
	_ = s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(subjects).Get([]byte(name)); v != nil {
			op = parseRecord(v).Operator
		}
		return nil
	})
	return op
}

// Senders returns the sender ids bound on a channel to the subject, read afresh.
func (s *Store) Senders(subject, channel string) []string {
	var out []string
	_ = s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bindings).ForEach(func(k, v []byte) error {
			if ch, snd, _ := strings.Cut(string(k), "\x00"); ch == channel && string(v) == subject {
				out = append(out, snd)
			}
			return nil
		})
	})
	return out
}

// Operators returns the sender ids bound on a channel to an operator-flagged
// subject, read afresh (ADR-0022).
func (s *Store) Operators(channel string) []string {
	var out []string
	_ = s.db.View(func(tx *bolt.Tx) error {
		sb := tx.Bucket(subjects)
		return tx.Bucket(bindings).ForEach(func(k, v []byte) error {
			ch, snd, _ := strings.Cut(string(k), "\x00")
			if ch != channel {
				return nil
			}
			if parseRecord(sb.Get(v)).Operator {
				out = append(out, snd)
			}
			return nil
		})
	})
	return out
}

// ErrRekeyConflict means a re-key would bind one sender to two subjects in the
// same namespace.
var ErrRekeyConflict = errors.New("re-keying would bind one sender to two subjects")

// ErrRekeyChain means a name a re-key moves bindings onto is itself moved: the
// one name would mean both, and a re-key run again would move bindings twice.
var ErrRekeyChain = errors.New("re-keying onto a name that is itself re-keyed")

// Rekey moves every binding made under a name in to onto the name it maps to,
// in one transaction: a sender's id belongs to the platform connection, so a
// channel's bindings become its provider's (ADR-0058 point 1). Nothing is
// changed if a sender would end up bound to two subjects under one name; the
// error names both. A name that maps to itself, or that no binding uses, is
// left alone, so Rekey run again changes nothing. A name moved onto may not be
// moved itself, or a second run would move its bindings on again.
//
// Before it changes anything, it writes a copy of the whole store to backup,
// which must not exist yet, so a re-key can be undone by hand. It returns the
// number of bindings moved; with none to move, it writes no copy.
func (s *Store) Rekey(to map[string]string, backup string) (int, error) {
	for from, onto := range to {
		if next, ok := to[onto]; ok && next != onto && onto != from {
			return 0, fmt.Errorf("%w: %s moves onto %s, which itself moves onto %s", ErrRekeyChain, from, onto, next)
		}
	}
	type move struct{ from, onto, v []byte }
	var moves []move
	err := s.db.View(func(tx *bolt.Tx) error {
		bb := tx.Bucket(bindings)
		landing := map[string]string{}
		return bb.ForEach(func(k, v []byte) error {
			channel, sender, ok := strings.Cut(string(k), "\x00")
			target, mapped := to[channel]
			if !ok || !mapped || target == channel {
				return nil
			}
			onto, err := bindingKey(target, sender)
			if err != nil {
				return err
			}
			if have := bb.Get(onto); have != nil && string(have) != string(v) {
				return fmt.Errorf("%w: sender %q on %s is %s, and on %s it is %s", ErrRekeyConflict, sender, target, have, channel, v)
			}
			if was, seen := landing[string(onto)]; seen && was != string(v) {
				return fmt.Errorf("%w: sender %q would be both %s and %s on %s", ErrRekeyConflict, sender, was, v, target)
			}
			landing[string(onto)] = string(v)
			moves = append(moves, move{append([]byte(nil), k...), onto, append([]byte(nil), v...)})
			return nil
		})
	})
	if err != nil || len(moves) == 0 {
		return 0, err
	}
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("could not keep a copy before re-keying: %s is already there", backup)
	}
	if err := s.db.View(func(tx *bolt.Tx) error { return tx.CopyFile(backup, 0o600) }); err != nil {
		return 0, fmt.Errorf("could not keep a copy before re-keying: %w", err)
	}
	return len(moves), s.db.Update(func(tx *bolt.Tx) error {
		bb := tx.Bucket(bindings)
		for _, m := range moves {
			if err := bb.Delete(m.from); err != nil {
				return err
			}
		}
		for _, m := range moves {
			if err := bb.Put(m.onto, m.v); err != nil {
				return err
			}
		}
		return nil
	})
}
