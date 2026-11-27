package bot

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/command"
)

var noteFile = comm.ChannelResult{Reply: "notes/a.md from vault", File: &command.File{Name: "a.md", Data: []byte("# A\nbody")}}

func TestAFileAskedForPrivatelyIsSentThereAsADocument(t *testing.T) {
	s, h := newServer(t), &handler{res: noteFile}
	start(t, s, h, nil)
	s.queue(priv(1, 1, 42, "/pao file vault notes/a.md"))
	waitFor(t, func() bool { return len(s.snapshot(&s.documents)) == 1 }, "the file is sent")
	d := s.snapshot(&s.documents)[0]
	assert.Equal(t, "42", d["chat_id"])
	assert.Equal(t, "a.md", d["filename"])
	assert.Equal(t, "# A\nbody", d["data"])
	assert.Equal(t, "notes/a.md from vault", d["caption"])
	assert.Empty(t, s.snapshot(&s.sent), "the caption is the answer: no separate message")
}

func TestAFileAskedForInAGroupGoesToTheSendersPrivateChatAlone(t *testing.T) {
	s, h := newServer(t), &handler{res: noteFile}
	start(t, s, h, nil)
	s.queue(grp(1, 1, 900, -500, "/pao file vault notes/a.md"))
	waitFor(t, func() bool { return len(s.snapshot(&s.documents)) == 1 }, "the file is sent")
	assert.Equal(t, "900", s.snapshot(&s.documents)[0]["chat_id"], "to the sender, not the group")
	assert.Empty(t, s.snapshot(&s.sent), "nothing is said in the group")
}

func TestAFileThatCannotReachTheSenderIsSaidInTheGroupWithoutItsName(t *testing.T) {
	s, h := newServer(t), &handler{res: noteFile}
	s.failSend[900] = true
	start(t, s, h, nil)
	s.queue(grp(1, 1, 900, -500, "/pao file vault notes/a.md"))
	waitFor(t, func() bool { return len(s.snapshot(&s.sent)) == 1 }, "the group is told")
	m := s.snapshot(&s.sent)[0]
	assert.Equal(t, "-500", m["chat_id"])
	assert.Equal(t, fileNotSentPrivately, m["text"])
	assert.NotContains(t, m["text"], "a.md")
	assert.Empty(t, s.snapshot(&s.documents))
}

func TestAPressForAFileSendsItToThePressersPrivateChat(t *testing.T) {
	s, h := newServer(t), &handler{res: noteFile}
	start(t, s, h, nil)
	s.queue(press(1, "cb1", 42, -500, "supergroup", 101, "/pao file vault notes/a.md"))
	waitFor(t, func() bool { return len(s.snapshot(&s.answers)) == 1 }, "the press is answered")
	docs := s.snapshot(&s.documents)
	require.Len(t, docs, 1)
	assert.Equal(t, "42", docs[0]["chat_id"])
	assert.Equal(t, pressAnsweredPrivately, s.snapshot(&s.answers)[0]["text"])
	assert.Empty(t, s.snapshot(&s.sent))
}

func TestAFileTooLargeForTheChannelIsRefusedWithoutUploading(t *testing.T) {
	big := comm.ChannelResult{Reply: "big", File: &command.File{Name: "big.bin", Data: []byte(strings.Repeat("x", maxDocument+1))}}
	s, h := newServer(t), &handler{res: big}
	start(t, s, h, nil)
	s.queue(priv(1, 1, 42, "/pao file vault big.bin"))
	waitFor(t, func() bool { return len(s.snapshot(&s.sent)) == 1 }, "the sender is told")
	assert.Contains(t, s.snapshot(&s.sent)[0]["text"], "larger than this channel carries")
	assert.Empty(t, s.snapshot(&s.documents))
}
