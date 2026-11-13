package claudecode

import (
	"path/filepath"
	"strings"
)

// TranscriptDir is the directory Claude Code writes the transcripts of sessions
// started in cwd: <home>/.claude/projects/<cwd with every character that is not
// a letter or digit replaced by '-'>. A profile may give this directory as its
// transcript path; the live transcript is the newest .jsonl file in it. The
// contract test pins this layout.
func TranscriptDir(home, cwd string) string {
	enc := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '-'
	}, cwd)
	return filepath.Join(home, ".claude", "projects", enc)
}
