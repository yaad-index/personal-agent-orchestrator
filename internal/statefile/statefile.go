// Package statefile writes a node's small state documents in the state directory:
// whole, or not at all.
package statefile

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Write stores v as indented JSON at path, mode 0600. It writes a temporary file
// beside path, syncs it and renames it over path, so a reader sees the old document
// or the new one, never part of one. On any error path is left as it was.
func Write(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
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
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
