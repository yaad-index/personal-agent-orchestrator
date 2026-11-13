// Package harvest is the small shared store a temporary agent's outputs are
// written to when it is killed (ADR-0006 point 4). A harvest is a copy of the
// agent's workdir plus a manifest, kept until the operator deletes it: it is
// the place work survives teardown, so nothing expires on its own.
//
// Reading is access-scoped (ADR-0005): a slice is the harvest of one agent, and
// the Authorizer decides who may read it. A denied read looks exactly like a
// harvest that does not exist.
package harvest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
)

// Limits on one harvest. A workdir over them is harvested as far as it fits
// and the manifest says it is incomplete, so the caller can keep the workdir
// instead of destroying work that was not captured.
const (
	MaxFiles = 20000
	MaxBytes = 256 << 20
	// maxSkipped bounds how many skipped paths a manifest lists by name.
	maxSkipped = 200
)

// ErrNotFound is returned for a harvest that does not exist or that the
// requester may not read. The two are deliberately indistinguishable.
var ErrNotFound = errors.New("harvest not found")

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// File is one harvested file.
type File struct {
	// Path is slash-separated and relative to the harvested workdir.
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Skip is a path that was not copied, and why.
type Skip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Manifest describes one harvest.
type Manifest struct {
	ID    string   `json:"id"`
	Agent string   `json:"agent"`
	Tags  []string `json:"tags"`
	// Requester is who asked for the kill that caused the harvest.
	Requester string    `json:"requester"`
	Time      time.Time `json:"time"`
	// Complete is false when a limit stopped the copy: some regular files were
	// not captured, and the workdir must not be destroyed on this harvest alone.
	Complete bool   `json:"complete"`
	Files    []File `json:"files"`
	Bytes    int64  `json:"bytes"`
	// Skipped lists paths not copied (symbolic links, special files, files over
	// a limit), up to a bound; SkippedTotal counts all of them.
	Skipped      []Skip `json:"skipped,omitempty"`
	SkippedTotal int    `json:"skipped_total,omitempty"`
}

// Summary is the text a kill reports to whoever asked for it.
func (m Manifest) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "harvested %d file(s), %d bytes, from %q as %s", len(m.Files), m.Bytes, m.Agent, m.ID)
	const show = 20
	for i, f := range m.Files {
		if i == show {
			fmt.Fprintf(&b, "\n  ... and %d more", len(m.Files)-show)
			break
		}
		fmt.Fprintf(&b, "\n  %s (%d bytes)", f.Path, f.Size)
	}
	if !m.Complete {
		b.WriteString("\nINCOMPLETE: a limit stopped the copy, so the workdir was kept")
	}
	if m.SkippedTotal > 0 {
		fmt.Fprintf(&b, "\n%d path(s) not copied (links or special files)", m.SkippedTotal)
	}
	return b.String()
}

// Store is the harvest store rooted at one directory.
type Store struct {
	root     string
	auth     access.Authorizer
	now      func() time.Time
	maxFiles int
	maxBytes int64
}

// Option tunes a Store.
type Option func(*Store)

// WithClock replaces the clock used to stamp harvests.
func WithClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// WithLimits replaces MaxFiles and MaxBytes.
func WithLimits(files int, bytes int64) Option {
	return func(s *Store) { s.maxFiles, s.maxBytes = files, bytes }
}

// New returns a Store under root. A nil Authorizer means operator-only.
func New(root string, a access.Authorizer, opts ...Option) *Store {
	s := &Store{root: root, auth: a, now: time.Now, maxFiles: MaxFiles, maxBytes: MaxBytes}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Harvest copies the files under workdir into a new harvest of agent and
// returns its manifest. The copy is staged and renamed into place, so a
// harvest that fails or is interrupted is never listed. Symbolic links and
// special files are not followed or copied. The directory is best read once
// the agent has stopped writing.
func (s *Store) Harvest(ctx context.Context, agent string, tags []string, workdir string, requester access.Subject) (Manifest, error) {
	if !idPattern.MatchString(agent) {
		return Manifest{}, fmt.Errorf("harvest: bad agent name %q", agent)
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return Manifest{}, err
	}
	now := s.now().UTC()
	id, err := newID(agent, now)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{
		ID: id, Agent: agent, Tags: slices.Clone(tags), Requester: requesterName(requester),
		Time: now, Complete: true, Files: []File{},
	}

	stage := filepath.Join(s.root, ".partial-"+id)
	if err := os.Mkdir(stage, 0o700); err != nil {
		return Manifest{}, err
	}
	fail := func(err error) (Manifest, error) {
		_ = os.RemoveAll(stage)
		return Manifest{}, err
	}
	files := filepath.Join(stage, "files")
	if err := os.Mkdir(files, 0o700); err != nil {
		return fail(err)
	}
	if err := s.copyTree(ctx, workdir, files, &m); err != nil {
		return fail(err)
	}
	if err := writeManifest(filepath.Join(stage, "manifest.json"), m); err != nil {
		return fail(err)
	}
	if err := os.Rename(stage, filepath.Join(s.root, id)); err != nil {
		return fail(err)
	}
	return m, nil
}

func requesterName(s access.Subject) string {
	switch s.Kind {
	case access.KindOperator:
		return "operator"
	case access.KindAgent:
		return "agent:" + s.Name
	default:
		return "unknown"
	}
}

func newID(agent string, now time.Time) (string, error) {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return agent + "-" + now.Format("20060102t150405z") + "-" + hex.EncodeToString(b[:]), nil
}

func (s *Store) copyTree(ctx context.Context, src, dst string, m *Manifest) error {
	skip := func(rel, reason string) {
		m.SkippedTotal++
		if len(m.Skipped) < maxSkipped {
			m.Skipped = append(m.Skipped, Skip{Path: rel, Reason: reason})
		}
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		slash := filepath.ToSlash(rel)
		target := filepath.Join(dst, rel)

		switch {
		case d.IsDir():
			return os.Mkdir(target, 0o700)
		case d.Type()&fs.ModeSymlink != 0:
			skip(slash, "symlink")
			return nil
		case !d.Type().IsRegular():
			skip(slash, "not a regular file")
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if len(m.Files) >= s.maxFiles || m.Bytes+info.Size() > s.maxBytes {
			m.Complete = false
			skip(slash, "over limit")
			return nil
		}
		n, err := copyFile(p, target, info)
		if err != nil {
			return fmt.Errorf("harvest %s: %w", slash, err)
		}
		m.Files = append(m.Files, File{Path: slash, Size: n})
		m.Bytes += n
		return nil
	})
}

// copyFile copies at most the size the file had when it was listed.
func copyFile(src, dst string, info fs.FileInfo) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()

	mode := fs.FileMode(0o600)
	if info.Mode()&0o100 != 0 {
		mode = 0o700
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(in, info.Size()))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}

func writeManifest(path string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (s *Store) load(id string) (Manifest, error) {
	if !idPattern.MatchString(id) {
		return Manifest{}, ErrNotFound
	}
	data, err := os.ReadFile(filepath.Join(s.root, id, "manifest.json"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Manifest{}, ErrNotFound
		}
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("harvest %s: manifest: %w", id, err)
	}
	if m.ID != id {
		return Manifest{}, fmt.Errorf("harvest %s: manifest names %q", id, m.ID)
	}
	return m, nil
}

func (s *Store) allowed(who access.Subject, m Manifest) bool {
	return access.Allowed(s.auth, who, access.ReadHarvest, access.Resource{Agent: m.Agent, Tags: m.Tags})
}

// Get returns a harvest's manifest if who may read it.
func (s *Store) Get(who access.Subject, id string) (Manifest, error) {
	m, err := s.load(id)
	if err != nil {
		return Manifest{}, err
	}
	if !s.allowed(who, m) {
		return Manifest{}, ErrNotFound
	}
	return m, nil
}

// List returns the manifests who may read, newest first. A harvest who may
// not read is left out, as if it were not there.
func (s *Store) List(who access.Subject) ([]Manifest, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Manifest
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		m, err := s.load(e.Name())
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if s.allowed(who, m) {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.After(out[j].Time)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Read returns one harvested file if who may read the harvest. Only a path the
// manifest lists can be read, so a path cannot reach outside the harvest.
func (s *Store) Read(who access.Subject, id, path string) ([]byte, error) {
	m, err := s.Get(who, id)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(m.Files, func(f File) bool { return f.Path == path }) {
		return nil, ErrNotFound
	}
	return os.ReadFile(filepath.Join(s.root, id, "files", filepath.FromSlash(path)))
}

// Delete removes a harvest. Only the operator may: a harvest is the record that
// work survived, and deleting it is as deliberate as the kill that made it.
func (s *Store) Delete(who access.Subject, id string) error {
	if who.Kind != access.KindOperator {
		return ErrNotFound
	}
	if _, err := s.load(id); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.root, id))
}
