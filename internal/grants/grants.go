// Package grants stores the access matrix's grants (ADR-0005, ADR-0012) as one
// versioned document in the state directory. The operator changes them while the
// node runs; they survive a restart, and a document that cannot be read as it was
// written stops the node instead of being guessed at.
package grants

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/statefile"
)

// Entry is a grant as stored.
type Entry struct {
	Subject string `json:"subject"`
	Kind    string `json:"kind"`
	Name    string `json:"name,omitempty"`
	Tag     string `json:"tag,omitempty"`
}

// Document is the stored form: the grants, and a version that increases with each
// change.
type Document struct {
	Version uint64  `json:"version"`
	Grants  []Entry `json:"grants"`
}

// EntryOf is a grant in its stored form.
func EntryOf(g access.Grant) Entry {
	return Entry{Subject: g.Subject.String(), Kind: string(g.Kind), Name: g.Name, Tag: g.Tag}
}

// Grant reads an entry back, refusing one that is not well formed.
func (e Entry) Grant() (access.Grant, error) {
	s, err := access.ParseSubject(e.Subject)
	if err != nil {
		return access.Grant{}, err
	}
	g := access.Grant{Subject: s, Kind: access.ResourceKind(e.Kind), Name: e.Name, Tag: e.Tag}
	return g, g.Validate()
}

// Store is safe for concurrent use. It implements access.Grants.
type Store struct {
	path  string
	found bool // the document existed when the store was opened

	mu      sync.RWMutex
	version uint64
	grants  []access.Grant
}

// Open reads the document at path; a missing file is an empty store.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var d Document
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	seen := map[access.Grant]bool{}
	for i, e := range d.Grants {
		g, err := e.Grant()
		if err != nil {
			return nil, fmt.Errorf("%s: grants[%d]: %w", path, i, err)
		}
		if seen[g] {
			return nil, fmt.Errorf("%s: grants[%d] repeats an earlier grant", path, i)
		}
		seen[g] = true
		s.grants = append(s.grants, g)
	}
	s.version = d.Version
	s.found = true
	return s, nil
}

// Found reports whether a document existed when the store was opened.
func (s *Store) Found() bool { return s.found }

// Seed stores gs as the first document, in one write. It refuses a store that was
// opened from a document or has changed since, so a seed is written at most once
// and never over grants the operator made.
func (s *Store) Seed(gs []access.Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.found || s.version != 0 {
		return errors.New("grants exist already: a seed is only written into an empty store")
	}
	next := make([]access.Grant, 0, len(gs))
	seen := map[access.Grant]bool{}
	for _, g := range gs {
		if err := g.Validate(); err != nil {
			return err
		}
		if !seen[g] {
			seen[g] = true
			next = append(next, g)
		}
	}
	return s.commitLocked(next)
}

// Of returns the subject's grants.
func (s *Store) Of(subject access.Subject) []access.Grant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []access.Grant
	for _, g := range s.grants {
		if g.Subject == subject {
			out = append(out, g)
		}
	}
	return out
}

// Document returns the grants in their stored form, ordered by subject, then
// kind, then name, then tag.
func (s *Store) Document() Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return document(s.version, s.grants)
}

func document(version uint64, gs []access.Grant) Document {
	d := Document{Version: version, Grants: make([]Entry, 0, len(gs))}
	for _, g := range gs {
		d.Grants = append(d.Grants, EntryOf(g))
	}
	sort.Slice(d.Grants, func(i, j int) bool {
		a, b := d.Grants[i], d.Grants[j]
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Tag < b.Tag
	})
	return d
}

// Add stores the grant. Adding one already held changes nothing and reports
// false. The file is written before the grant takes effect.
func (s *Store) Add(g access.Grant) (bool, error) {
	if err := g.Validate(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, have := range s.grants {
		if have == g {
			return false, nil
		}
	}
	next := append(append([]access.Grant(nil), s.grants...), g)
	return true, s.commitLocked(next)
}

// Remove drops the grant. Removing one not held changes nothing and reports
// false.
func (s *Store) Remove(g access.Grant) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make([]access.Grant, 0, len(s.grants))
	for _, have := range s.grants {
		if have != g {
			next = append(next, have)
		}
	}
	if len(next) == len(s.grants) {
		return false, nil
	}
	return true, s.commitLocked(next)
}

func (s *Store) commitLocked(next []access.Grant) error {
	if err := statefile.Write(s.path, document(s.version+1, next)); err != nil {
		return err
	}
	s.version++
	s.grants = next
	return nil
}
