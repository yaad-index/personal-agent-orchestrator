// Package workfile reads a file from an agent's working directory for a human
// who asked for it (ADR-0052). Only a regular file inside the working
// directory can be read: the path is resolved through every symbolic link
// before it is checked, and nothing inside .git or .claude is ever read.
package workfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoFile is every refusal of a path, whether the file is missing or not one
// that may be read, so a caller can't tell the two apart (ADR-0052 point 3).
var ErrNoFile = errors.New("no such file")

// ErrTooLarge is a file that may be read but is larger than asked for.
var ErrTooLarge = errors.New("file too large")

// hidden are the directories inside a working directory that are never read.
var hidden = []string{".git", ".claude"}

// File is a file read from a working directory.
type File struct {
	// Name is the file's base name.
	Name string
	// Path is the path as asked for, relative to the working directory.
	Path string
	Data []byte
}

// Resolve checks path against workdir and returns the file's real location. It
// does not open the file.
func Resolve(workdir, path string) (string, error) {
	if path == "" || filepath.IsAbs(path) {
		return "", ErrNoFile
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || hiddenIn(clean) {
		return "", ErrNoFile
	}
	root, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		return "", ErrNoFile
	}
	real, err := filepath.EvalSymlinks(filepath.Join(root, clean))
	if err != nil {
		return "", ErrNoFile
	}
	if !inside(root, real) {
		return "", ErrNoFile
	}
	info, err := os.Lstat(real)
	if err != nil || !info.Mode().IsRegular() {
		return "", ErrNoFile
	}
	return real, nil
}

// Read reads the file at path in workdir, refusing one larger than max bytes.
func Read(workdir, path string, max int64) (File, error) {
	real, err := Resolve(workdir, path)
	if err != nil {
		return File{}, err
	}
	f, err := openNoFollow(real)
	if err != nil {
		return File{}, ErrNoFile
	}
	defer func() { _ = f.Close() }()
	// What was opened is checked again, so a directory swapped for a link
	// between the check and the open cannot lead outside.
	root, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		return File{}, ErrNoFile
	}
	if opened, ok := openedPath(f); ok && !inside(root, opened) {
		return File{}, ErrNoFile
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return File{}, ErrNoFile
	}
	if info.Size() > max {
		return File{}, fmt.Errorf("%w: %d bytes, at most %d", ErrTooLarge, info.Size(), max)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return File{}, ErrNoFile
	}
	if int64(len(data)) > max {
		return File{}, fmt.Errorf("%w: more than %d bytes", ErrTooLarge, max)
	}
	return File{Name: filepath.Base(real), Path: path, Data: data}, nil
}

// inside reports whether real is root or below it, and in nothing hidden.
func inside(root, real string) bool {
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	return !hiddenIn(rel)
}

// hiddenIn reports whether a relative path passes through a hidden directory.
func hiddenIn(rel string) bool {
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		for _, h := range hidden {
			if part == h {
				return true
			}
		}
	}
	return false
}
