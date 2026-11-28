package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"go.yaml.in/yaml/v3"
)

// Managed is the content of the file pao manages (ADR-0055). It holds only the
// settings the management UI sets, in the same form as the orchestrator config;
// any other setting in it is a load error, not ignored.
type Managed struct {
	Channels []Channel `yaml:"channels,omitempty"`
}

// ErrNotManaged is returned for a write when the orchestrator config names no
// managed file: without one, pao changes none of these settings (ADR-0055
// point 5).
var ErrNotManaged = errors.New("the configuration names no managed file; add managed: <file> to let pao keep these settings")

// readManaged resolves o.Managed against the config's directory and reads it.
// A managed file not written yet holds nothing. override, when not nil, is
// used in place of the file.
func readManaged(path string, o *Orchestrator, override *Managed) (Managed, error) {
	if o.Managed == "" {
		if override != nil {
			return Managed{}, ErrNotManaged
		}
		return Managed{}, nil
	}
	if !filepath.IsAbs(o.Managed) {
		o.Managed = filepath.Join(filepath.Dir(path), o.Managed)
	}
	if same, err := sameFile(path, o.Managed); err != nil || same {
		if err == nil {
			err = errors.New("it is this file")
		}
		return Managed{}, fmt.Errorf("%s: managed: %s: %w", path, o.Managed, err)
	}
	if override != nil {
		return *override, nil
	}
	var m Managed
	if err := decodeFile(o.Managed, &m); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Managed{}, nil
		}
		return Managed{}, err
	}
	return m, nil
}

// sameFile reports whether a and b are one file. A b that does not exist yet
// is not a.
func sameFile(a, b string) (bool, error) {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true, nil
	}
	sa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	sb, err := os.Stat(b)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return os.SameFile(sa, sb), nil
}

// ManagedPath is the managed file, resolved; empty when the configuration
// names none.
func (o Orchestrator) ManagedPath() string { return o.Managed }

// ManagedChannel reports whether the managed file defines the named channel,
// as opposed to the orchestrator config.
func (o Orchestrator) ManagedChannel(name string) bool { return o.managedChannels[name] }

// writeMu keeps one write of a managed file at a time within the process.
var writeMu sync.Mutex

// WriteManaged replaces the managed file of the configuration at path with m,
// only if the configuration loads with it (ADR-0055 point 4): it is checked as
// loading checks it, and by each check given, such as the channel adapters'
// own, which this package does not know; then it is written whole, so the file
// is never left half written and never holds what would not load.
func WriteManaged(path string, m Managed, checks ...func(*Config) error) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	cfg, err := checkManaged(path, m, checks)
	if err != nil {
		return err
	}
	b, err := encodeManaged(m)
	if err != nil {
		return err
	}
	return writeWhole(cfg.Orchestrator.Managed, b)
}

// CheckManaged is WriteManaged without the write: whether the configuration
// with m as its managed file loads and passes checks.
func CheckManaged(path string, m Managed, checks ...func(*Config) error) error {
	_, err := checkManaged(path, m, checks)
	return err
}

func checkManaged(path string, m Managed, checks []func(*Config) error) (*Config, error) {
	cfg, err := load(path, true, &m)
	if err != nil {
		return nil, err
	}
	for _, check := range checks {
		if err := check(cfg); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// managedHeader opens every managed file, so whoever reads it knows not to
// edit it by hand while pao runs.
const managedHeader = "# Written by pao (ADR-0055). Change these settings in pao's management UI;\n# an edit made here while pao runs may be overwritten.\n"

func encodeManaged(m Managed) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(managedHeader)
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// writeWhole writes b to path by way of a file beside it, renamed over it once
// complete and on disk.
func writeWhole(path string, b []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
