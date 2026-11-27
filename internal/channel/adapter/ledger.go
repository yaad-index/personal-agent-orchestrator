package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// Ledger remembers every channel id this node has served and the name it served
// under, so an id is never reused (ADR-0021 point 2): a removed channel's id stays
// retired, and a configuration that gives it to another channel, or gives a served
// name another id, is refused before anything starts.
type Ledger struct {
	path string
	ids  map[int]string
}

type ledgerFile struct {
	Channels map[string]string `json:"channels"`
}

// OpenLedger reads the ledger at path; a missing file is an empty ledger.
func OpenLedger(path string) (*Ledger, error) {
	l := &Ledger{path: path, ids: map[int]string{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	var f ledgerFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for k, name := range f.Channels {
		id, err := strconv.Atoi(k)
		if err != nil || id < 1 || name == "" {
			return nil, fmt.Errorf("%s: bad entry %q", path, k)
		}
		l.ids[id] = name
	}
	return l, nil
}

// Check reports whether Admit would admit the channels, writing nothing.
func (l *Ledger) Check(specs []Spec) error {
	for _, s := range specs {
		if prev, ok := l.ids[s.ID]; ok && prev != s.Name {
			return fmt.Errorf("channel id %d is channel %q, not %q: an id is never reused and a channel keeps its name", s.ID, prev, s.Name)
		}
		if id, ok := l.IDOf(s.Name); ok && id != s.ID {
			return fmt.Errorf("channel %q was id %d: a channel keeps its id", s.Name, id)
		}
	}
	return nil
}

// IDOf returns the id the named channel was served under, if it was.
func (l *Ledger) IDOf(name string) (int, bool) {
	for id, n := range l.ids {
		if n == name {
			return id, true
		}
	}
	return 0, false
}

// MaxID returns the highest id served, 0 when none was.
func (l *Ledger) MaxID() int {
	m := 0
	for id := range l.ids {
		m = max(m, id)
	}
	return m
}

// Admit checks the channels against what has been served and, if they are all
// consistent, records them. Nothing is written when any is refused.
func (l *Ledger) Admit(specs []Spec) error {
	if err := l.Check(specs); err != nil {
		return err
	}
	next := map[int]string{}
	for id, n := range l.ids {
		next[id] = n
	}
	for _, s := range specs {
		next[s.ID] = s.Name
	}
	if len(next) == len(l.ids) {
		return nil
	}
	f := ledgerFile{Channels: map[string]string{}}
	ids := make([]int, 0, len(next))
	for id := range next {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		f.Channels[strconv.Itoa(id)] = next[id]
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".channels-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), l.path); err != nil {
		return err
	}
	l.ids = next
	return nil
}
