package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Registration is by an operator-issued, single-use invitation (ADR-0020 points
// 1 to 4). The operator issues one for a subject and hands the secret to the
// human out of band; the human presents it at the registration entry point and
// that sender is bound to the subject.

var invites = []byte("invites") // sha256(secret) -> invitation

const (
	// DefaultInviteTTL is how long an invitation lasts when none is asked for.
	DefaultInviteTTL = 15 * time.Minute
	// DefaultFailures is how many failed attempts a sender may make in a window.
	DefaultFailures = 5
	// DefaultWindow is that window.
	DefaultWindow = 15 * time.Minute
	// secretBytes is 96 bits of randomness (ADR-0020 point 1 asks for at least 80).
	secretBytes = 12
	// maxTracked bounds the failure counters held in memory.
	maxTracked = 10000
)

var (
	// ErrNoInvitation means no outstanding invitation has that id.
	ErrNoInvitation = errors.New("no such invitation")

	secretEnc = base32.StdEncoding.WithPadding(base32.NoPadding)
	// dummy is compared against when nothing else is, so a comparison always runs.
	dummy = sha256.Sum256([]byte("pao-registration-dummy"))
)

// Invitation is an outstanding invitation as the operator sees it. The secret
// is not part of it: only a digest is kept.
type Invitation struct {
	ID      string    `json:"id"`
	Subject string    `json:"subject"`
	Expires time.Time `json:"expires"`
}

// limiter counts failed registration attempts per (channel, sender) in memory.
// A restart forgets them; the secret's size is what makes guessing infeasible,
// and the limit only keeps an attempt from being cheap.
type limiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
	max      int
	window   time.Duration
	track    int // pairs tracked at most
}

func (l *limiter) suppressed(pair string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(pair, now)) >= l.max
}

func (l *limiter) fail(pair string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.failures) >= l.track {
		for p := range l.failures {
			if len(l.recent(p, now)) == 0 {
				delete(l.failures, p)
			}
		}
		if len(l.failures) >= l.track {
			l.failures = map[string][]time.Time{}
		}
	}
	l.failures[pair] = append(l.recent(pair, now), now)
	if n := len(l.failures[pair]); n > l.max {
		l.failures[pair] = l.failures[pair][n-l.max:]
	}
}

// recent returns the pair's failures inside the window, dropping older ones.
func (l *limiter) recent(pair string, now time.Time) []time.Time {
	var keep []time.Time
	for _, t := range l.failures[pair] {
		if now.Sub(t) <= l.window {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(l.failures, pair)
	} else {
		l.failures[pair] = keep
	}
	return keep
}

func normalize(secret string) string {
	secret = strings.ToUpper(strings.TrimSpace(secret))
	return strings.NewReplacer("-", "", " ", "").Replace(secret)
}

func digest(secret string) [32]byte { return sha256.Sum256([]byte(normalize(secret))) }

func inviteID(d []byte) string { return hex.EncodeToString(d[:4]) }

// Invite issues a single-use invitation for an existing subject and returns its
// secret, shown once. ttl <= 0 means DefaultInviteTTL.
func (s *Store) Invite(subject string, ttl time.Duration) (secret string, inv Invitation, err error) {
	if ttl <= 0 {
		ttl = DefaultInviteTTL
	}
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", Invitation{}, err
	}
	enc := secretEnc.EncodeToString(raw)
	var groups []string
	for i := 0; i < len(enc); i += 4 {
		groups = append(groups, enc[i:min(i+4, len(enc))])
	}
	secret = strings.Join(groups, "-")
	d := digest(secret)
	inv = Invitation{ID: inviteID(d[:]), Subject: subject, Expires: s.now().UTC().Add(ttl)}
	err = s.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(subjects).Get([]byte(subject)) == nil {
			return ErrNoSubject
		}
		b := tx.Bucket(invites)
		if err := dropExpired(b, s.now()); err != nil {
			return err
		}
		v, err := json.Marshal(inv)
		if err != nil {
			return err
		}
		return b.Put(d[:], v)
	})
	if err != nil {
		return "", Invitation{}, err
	}
	return secret, inv, nil
}

func dropExpired(b *bolt.Bucket, now time.Time) error {
	var drop [][]byte
	if err := b.ForEach(func(k, v []byte) error {
		var inv Invitation
		if json.Unmarshal(v, &inv) != nil || !now.Before(inv.Expires) {
			drop = append(drop, append([]byte(nil), k...))
		}
		return nil
	}); err != nil {
		return err
	}
	for _, k := range drop {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// Invitations lists the outstanding invitations, never their secrets.
func (s *Store) Invitations() ([]Invitation, error) {
	var out []Invitation
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(invites).ForEach(func(_, v []byte) error {
			var inv Invitation
			if err := json.Unmarshal(v, &inv); err != nil {
				return err
			}
			if s.now().Before(inv.Expires) {
				out = append(out, inv)
			}
			return nil
		})
	})
	return out, err
}

// RevokeInvitation removes an outstanding invitation by id.
func (s *Store) RevokeInvitation(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(invites)
		var hit []byte
		if err := b.ForEach(func(k, _ []byte) error {
			if inviteID(k) == id {
				hit = append([]byte(nil), k...)
			}
			return nil
		}); err != nil {
			return err
		}
		if hit == nil {
			return ErrNoInvitation
		}
		return b.Delete(hit)
	})
}

// Register completes a registration if secret is a valid outstanding invitation
// and the (channel, sender) pair is free and not rate limited, binding the pair
// to the invited subject and consuming the invitation. It reports only whether it
// did. Every attempt, whatever it finds, does the same comparison of the secret's
// digest against every outstanding invitation and a fixed dummy, so no outcome is
// cheaper than another (ADR-0020 points 3 and 4). A failed attempt counts against
// the pair; an attempt past the limit still runs the comparison and cannot
// succeed.
func (s *Store) Register(channel, sender, secret string) (subject string, ok bool) {
	key, err := bindingKey(channel, sender)
	now := s.now()
	pair := string(key)
	suppressed := s.lim.suppressed(pair, now)
	d := digest(secret)

	_ = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(invites)
		_ = subtle.ConstantTimeCompare(d[:], dummy[:]) // the comparison that always runs
		s.compares.Add(1)
		match := 0
		var hit []byte
		var inv Invitation
		_ = b.ForEach(func(k, v []byte) error {
			s.compares.Add(1)
			if subtle.ConstantTimeCompare(d[:], k) == 1 {
				match = 1
				hit = append([]byte(nil), k...)
				_ = json.Unmarshal(v, &inv)
			}
			return nil
		})
		free := tx.Bucket(bindings).Get(key) == nil
		known := tx.Bucket(subjects).Get([]byte(inv.Subject)) != nil
		live := now.Before(inv.Expires)
		if err == nil && match == 1 && free && known && live && !suppressed {
			if tx.Bucket(bindings).Put(key, []byte(inv.Subject)) == nil && b.Delete(hit) == nil {
				subject, ok = inv.Subject, true
			}
		}
		return nil
	})
	if !ok {
		s.lim.fail(pair, now)
	}
	return subject, ok
}
