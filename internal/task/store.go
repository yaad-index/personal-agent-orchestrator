package task

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"
)

// ErrNotFound is a task id the store does not hold.
var ErrNotFound = errors.New("task not found")

// The folders the store starts with. The set is open (ADR-0036 point 6): any
// folder named like these may be used, and is made when first written to.
const (
	// FolderOpen holds the tasks in play.
	FolderOpen = "open"
	// FolderArchive holds tasks that are over.
	FolderArchive = "archive"
	// FolderTemplates holds tasks to copy from.
	FolderTemplates = "templates"
	// FolderRecurring holds tasks that re-queue themselves.
	FolderRecurring = "recurring"
)

var folderPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ValidFolder reports whether name can be a folder: one lowercase word, so it
// can never name a path outside the store.
func ValidFolder(name string) bool { return folderPattern.MatchString(name) }

// Store keeps task files under Root, one folder per kind of task, one file per
// task named by its id.
type Store struct {
	Root string

	mu sync.Mutex
}

// Put writes t into folder, replacing any file of the same id in any folder,
// so a task is in exactly one folder. The write is whole or not at all.
func (s *Store) Put(folder string, t Task) error {
	if !ValidFolder(folder) {
		return fmt.Errorf("folder %q must be one lowercase word", folder)
	}
	if err := t.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, _, err := s.find(t.ID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	dir := filepath.Join(s.Root, folder)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := writeYAML(filepath.Join(dir, t.ID+".yaml"), t); err != nil {
		return err
	}
	if prev != "" && filepath.Dir(prev) != dir {
		return os.Remove(prev)
	}
	return nil
}

// Get returns the task of that id and the folder it is in.
func (s *Store) Get(id string) (Task, string, error) {
	if !ValidID(id) {
		return Task{}, "", ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path, folder, err := s.find(id)
	if err != nil {
		return Task{}, "", err
	}
	t, err := readTask(path)
	return t, folder, err
}

// Entry is a task and the folder it is in.
type Entry struct {
	Task   Task
	Folder string
}

// List returns every task in every folder, the newest first. A file that is
// not a valid task is skipped and reported in the error, beside the rest.
func (s *Store) List() ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	folders, err := os.ReadDir(s.Root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	var errs []error
	for _, f := range folders {
		if !f.IsDir() || !ValidFolder(f.Name()) {
			continue
		}
		files, err := filepath.Glob(filepath.Join(s.Root, f.Name(), "t-*.yaml"))
		if err != nil {
			return nil, err
		}
		for _, p := range files {
			t, err := readTask(p)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			out = append(out, Entry{Task: t, Folder: f.Name()})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Task.Created.Equal(out[j].Task.Created) {
			return out[i].Task.Created.After(out[j].Task.Created)
		}
		return out[i].Task.ID > out[j].Task.ID
	})
	return out, errors.Join(errs...)
}

// find returns the path and folder of the task of that id.
func (s *Store) find(id string) (string, string, error) {
	matches, err := filepath.Glob(filepath.Join(s.Root, "*", id+".yaml"))
	if err != nil {
		return "", "", err
	}
	var found []string
	for _, m := range matches {
		if ValidFolder(filepath.Base(filepath.Dir(m))) {
			found = append(found, m)
		}
	}
	switch len(found) {
	case 0:
		return "", "", ErrNotFound
	case 1:
		return found[0], filepath.Base(filepath.Dir(found[0])), nil
	default:
		return "", "", fmt.Errorf("task %s is in more than one folder: %s", id, strings.Join(found, ", "))
	}
}

func readTask(path string) (Task, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Task{}, err
	}
	var t Task
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil {
		return Task{}, fmt.Errorf("%s: %s", path, strings.TrimSpace(err.Error()))
	}
	if err := t.Validate(); err != nil {
		return Task{}, fmt.Errorf("%s: %w", path, err)
	}
	if want := strings.TrimSuffix(filepath.Base(path), ".yaml"); t.ID != want {
		return Task{}, fmt.Errorf("%s: holds task %s, not %s", path, t.ID, want)
	}
	return t, nil
}

// writeYAML stores v as YAML at path, mode 0600, through a temporary file
// renamed over it, so a reader sees the old file or the new one.
func writeYAML(path string, v any) error {
	b, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
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
	return os.Rename(tmp.Name(), path)
}
