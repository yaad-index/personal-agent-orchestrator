// Package mirror follows a backend's transcript and turns it into entries
// (ADR-0008). It reads the transcript file the backend itself writes, never the
// terminal, so a turn cannot be lost the way a scrape can lose one. The level
// only filters what is shown; everything stays on disk.
package mirror

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// Start says where following begins on the first transcript it finds.
type Start int

const (
	// FromEnd skips what is already in the file: live view only.
	FromEnd Start = iota
	// FromStart delivers the whole file first, then follows.
	FromStart
)

// Mirror follows transcripts with one backend reader.
type Mirror struct {
	// Reader parses the backend's transcript lines.
	Reader core.TranscriptReader
	// Poll is how often the file is checked for growth. Zero means 250ms.
	Poll time.Duration
	// OnError receives a line the reader could not parse. The line is skipped
	// and following continues. May be nil.
	OnError func(error)
}

// Locate returns the live transcript file for path. A file is itself. A
// directory's live transcript is its most recently written .jsonl file: a new
// session writes a new file, and the newest one is the one being written.
func Locate(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return path, nil
	}
	matches, err := filepath.Glob(filepath.Join(path, "*.jsonl"))
	if err != nil {
		return "", err
	}
	type cand struct {
		path string
		mod  time.Time
	}
	var cands []cand
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil || fi.IsDir() {
			continue
		}
		cands = append(cands, cand{m, fi.ModTime()})
	}
	if len(cands) == 0 {
		return "", fs.ErrNotExist
	}
	sort.Slice(cands, func(i, j int) bool {
		if !cands[i].mod.Equal(cands[j].mod) {
			return cands[i].mod.After(cands[j].mod)
		}
		return cands[i].path > cands[j].path
	})
	return cands[0].path, nil
}

// Follow delivers entries to emit as the transcript at path grows, until ctx is
// done (it then returns nil). The transcript need not exist yet. A line is only
// parsed once its newline has been written, so a record the backend is still
// writing is never half-read. If the file shrinks it is read again from the
// start, and if path is a directory and a newer transcript appears, following
// moves to it from its start.
func (m *Mirror) Follow(ctx context.Context, path string, start Start, emit func(core.Entry)) error {
	poll := m.Poll
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	var (
		cur     string
		off     int64
		pending []byte
		// existed: a transcript was already there when following began. Only
		// that one is skipped under FromEnd; a file that appears later was
		// written entirely after following began and is read whole.
		existed bool
		ticked  bool
	)
	for {
		file, err := Locate(path)
		switch {
		case err == nil:
			if !ticked {
				existed = true
			}
			if file != cur {
				first := cur == ""
				cur, pending = file, nil
				off = 0
				if first && existed && start == FromEnd {
					if fi, serr := os.Stat(file); serr == nil {
						off = fi.Size()
					}
				}
			}
			var rerr error
			off, pending, rerr = m.readNew(file, off, pending, emit)
			if rerr != nil {
				return rerr
			}
		case errors.Is(err, fs.ErrNotExist):
			// Not written yet; keep waiting.
		default:
			return err
		}

		ticked = true
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(poll):
		}
	}
}

// readNew reads whatever was appended to file since off, emits each complete
// line's entries, and returns the new offset and the unfinished tail.
func (m *Mirror) readNew(file string, off int64, pending []byte, emit func(core.Entry)) (int64, []byte, error) {
	f, err := os.Open(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return off, pending, nil
		}
		return off, pending, err
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return off, pending, err
	}
	if fi.Size() < off {
		off, pending = 0, nil // truncated or replaced: start over
	}
	if fi.Size() == off {
		return off, pending, nil
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return off, pending, err
	}
	chunk, err := io.ReadAll(io.LimitReader(f, fi.Size()-off))
	if err != nil {
		return off, pending, err
	}
	off += int64(len(chunk))

	buf := append(pending, chunk...)
	start := 0
	for i, b := range buf {
		if b != '\n' {
			continue
		}
		m.parseLine(buf[start:i], emit)
		start = i + 1
	}
	return off, append([]byte(nil), buf[start:]...), nil
}

func (m *Mirror) parseLine(line []byte, emit func(core.Entry)) {
	if len(line) == 0 {
		return
	}
	entries, err := m.Reader.Parse(line)
	if err != nil {
		if m.OnError != nil {
			m.OnError(err)
		}
		return
	}
	for _, e := range entries {
		emit(e)
	}
}

// Replay reads the whole transcript at path and returns its entries in order.
// A directory is resolved to its live transcript; a final line with no newline
// yet is still being written and is left out. A line the reader cannot
// parse is an error here, because replay is where a format change should be
// loud.
func Replay(path string, r core.TranscriptReader) ([]core.Entry, error) {
	file, err := Locate(path)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var out []core.Entry
	start, n := 0, 0
	for i, c := range b {
		if c != '\n' {
			continue
		}
		n++
		raw := b[start:i]
		start = i + 1
		if len(raw) == 0 {
			continue
		}
		es, err := r.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w", file, n, err)
		}
		out = append(out, es...)
	}
	// Whatever follows the last newline is a record still being written, the
	// same rule Follow applies, so it is not read.
	return out, nil
}

// Filter returns the entries visible at level n under levels, in order.
func Filter(entries []core.Entry, l core.Levels, n int) []core.Entry {
	var out []core.Entry
	for _, e := range entries {
		if l.Visible(e.Kind, n) {
			out = append(out, e)
		}
	}
	return out
}
