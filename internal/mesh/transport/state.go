// Package transport is how pao nodes talk to each other (ADR-0017): gRPC over
// the mutually authenticated TLS of package mesh. This package is the core of it:
// who is master and at what epoch, the three services and the interceptors that
// decide who may call them, the liveness stream between every pair of nodes, and
// the dialer. The calls that do the work of the mesh (directory, commands,
// streams, failover) are built on it.
package transport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Supersedes reports whether the master named by (epochA, masterA) takes
// precedence over (epochB, masterB). Masters are ordered by epoch, the higher
// winning, and at equal epochs by node id, the lower winning. The rule is
// arbitrary; what matters is that it is a fixed total order, so two masters that
// meet after a partition always agree which of them yields (ADR-0017 point 12).
func Supersedes(epochA uint64, masterA string, epochB uint64, masterB string) bool {
	if epochA != epochB {
		return epochA > epochB
	}
	return masterA < masterB
}

// Membership is what a node believes about who is master. It is safe for
// concurrent use and persists every change, so a restart does not forget an epoch
// the mesh has moved to.
type Membership struct {
	self string
	path string

	mu     sync.RWMutex
	epoch  uint64
	master string
}

type persisted struct {
	Epoch  uint64 `json:"epoch"`
	Master string `json:"master"`
}

// LoadMembership reads the persisted belief at path, or starts from initial (the
// mesh document's starting master, at epoch 0) if there is none.
func LoadMembership(path, self, initialMaster string) (*Membership, error) {
	m := &Membership{self: self, path: path, master: initialMaster}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return m, nil
	case err != nil:
		return nil, err
	}
	var p persisted
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if p.Master == "" {
		return nil, fmt.Errorf("%s: no master recorded", path)
	}
	m.epoch, m.master = p.Epoch, p.Master
	return m, nil
}

// View returns the current epoch and master.
func (m *Membership) View() (epoch uint64, master string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.epoch, m.master
}

// IsMaster reports whether this node believes it is master.
func (m *Membership) IsMaster() bool {
	_, master := m.View()
	return master == m.self
}

// Observe takes in a belief heard from another node. It is adopted only if it
// supersedes the one held, and then persisted; a node that was master and adopts a
// master that is not itself has yielded. It returns whether the belief changed.
func (m *Membership) Observe(epoch uint64, master string) (changed bool, err error) {
	if master == "" {
		return false, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !Supersedes(epoch, master, m.epoch, m.master) {
		return false, nil
	}
	if err := m.persist(epoch, master); err != nil {
		return false, err
	}
	m.epoch, m.master = epoch, master
	return true, nil
}

// persist writes the belief with a temporary file and a rename, so the file is
// always a whole record.
func (m *Membership) persist(epoch uint64, master string) error {
	if m.path == "" {
		return nil
	}
	b, err := json.Marshal(persisted{Epoch: epoch, Master: master})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.path), ".state-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	return os.Rename(tmp.Name(), m.path)
}
