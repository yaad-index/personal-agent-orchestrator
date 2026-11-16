// Package identity holds who the humans are and which external sender ids are
// theirs (ADR-0019 points 3 to 5). A human is a subject (ADR-0012); a binding
// ties one (channel, per-sender id) pair to exactly one subject. The pair is
// created at registration, and the operator can revoke it. An id with no binding
// resolves to nobody.
package identity

import (
	"errors"
	"fmt"
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
		return b.Put([]byte(name), []byte(s.now().UTC().Format(time.RFC3339Nano)))
	})
}

// Subjects lists every subject, by name.
func (s *Store) Subjects() ([]Subject, error) {
	var out []Subject
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(subjects).ForEach(func(k, v []byte) error {
			t, _ := time.Parse(time.RFC3339Nano, string(v))
			out = append(out, Subject{Name: string(k), Created: t})
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
