package claudecode

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTranscriptDir(t *testing.T) {
	assert.Equal(t, "/h/.claude/projects/-home-alice", TranscriptDir("/h", "/home/alice"))
	assert.Equal(t, "/h/.claude/projects/-tmp-a-b-c1", TranscriptDir("/h", "/tmp/a.b_c1"),
		"dots and underscores become dashes, digits stay")
}
