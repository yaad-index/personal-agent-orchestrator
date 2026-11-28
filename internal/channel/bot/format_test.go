package bot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/markdown"
)

// ents reads the formatting a send carried.
func ents(t *testing.T, body map[string]string) []models.MessageEntity {
	t.Helper()
	if body["entities"] == "" {
		return nil
	}
	var out []models.MessageEntity
	require.NoError(t, json.Unmarshal([]byte(body["entities"]), &out))
	return out
}

// covered is the text an entity covers, counted in UTF-16 as the service counts.
func covered(text string, e models.MessageEntity) string {
	u := utf16.Encode([]rune(text))
	return string(utf16.Decode(u[e.Offset : e.Offset+e.Length]))
}

func sendOne(t *testing.T, h *handler, m core.Message, opts map[string]any) (*server, []map[string]string) {
	t.Helper()
	s := newServer(t)
	b, err := newBot(spec(s, opts), h)
	require.NoError(t, err)
	require.NoError(t, b.For("chat").Send(context.Background(), m))
	return s, s.snapshot(&s.sent)
}

func TestAnAgentsMarkdownIsSentAsTextWithItsFormatting(t *testing.T) {
	h := &handler{recipients: []string{"900"}}
	_, sent := sendOne(t, h, core.Message{Sender: "vault", Text: "Saved **two** notes in `inbox/`, see [docs](https://e.example).", Format: core.FormatMarkdown}, nil)
	require.Len(t, sent, 1)
	text := sent[0]["text"]
	assert.Equal(t, "[vault]: Saved two notes in inbox/, see docs.", text, "the sender's name is plain and the markup is gone")
	es := ents(t, sent[0])
	require.Len(t, es, 3)
	assert.Equal(t, models.MessageEntityTypeBold, es[0].Type)
	assert.Equal(t, "two", covered(text, es[0]))
	assert.Equal(t, models.MessageEntityTypeCode, es[1].Type)
	assert.Equal(t, "inbox/", covered(text, es[1]))
	assert.Equal(t, models.MessageEntityTypeTextLink, es[2].Type)
	assert.Equal(t, "docs", covered(text, es[2]))
	assert.Equal(t, "https://e.example", es[2].URL)
	assert.Empty(t, sent[0]["parse_mode"], "no markup language: nothing to escape")
}

func TestTheDefaultAgentsMarkdownHasNoNameBeforeIt(t *testing.T) {
	h := &handler{recipients: []string{"900"}, defaults: map[string]string{"chat": "vault"}}
	_, sent := sendOne(t, h, core.Message{Sender: "vault", Text: "*hi* 😀 **there**", Format: core.FormatMarkdown}, nil)
	require.Len(t, sent, 1)
	text := sent[0]["text"]
	assert.Equal(t, "hi 😀 there", text)
	es := ents(t, sent[0])
	require.Len(t, es, 2)
	assert.Equal(t, "hi", covered(text, es[0]))
	assert.Equal(t, 6, es[1].Offset, "offsets count UTF-16 units: the emoji is two")
	assert.Equal(t, "there", covered(text, es[1]))
}

func TestPlainTextIsNeverReadAsMarkdown(t *testing.T) {
	for _, m := range []core.Message{
		{Sender: "pao", Kind: core.MessageSystemEvent, Text: `agent "a" wants to run Write: {"content":"**x** ` + "`y`" + `"}`},
		{Sender: "human", Text: "my **stars** and `ticks`"},
		{Sender: "vault", Text: "tool Write: {\"file_path\":\"a_b_c.md\"}"},
	} {
		_, sent := sendOne(t, &handler{recipients: []string{"900"}}, m, nil)
		require.Len(t, sent, 1)
		assert.Equal(t, m.Shown(), sent[0]["text"], "shown exactly as written")
		assert.Empty(t, sent[0]["entities"])
	}
}

func TestALabelStaysPlainBeforeMarkdown(t *testing.T) {
	_, sent := sendOne(t, &handler{recipients: []string{"900"}}, core.Message{Sender: "vault", Label: "*v*", Text: "**b**", Format: core.FormatMarkdown}, nil)
	require.Len(t, sent, 1)
	assert.Equal(t, "*v* b", sent[0]["text"])
	es := ents(t, sent[0])
	require.Len(t, es, 1)
	assert.Equal(t, "b", covered(sent[0]["text"], es[0]))
}

func TestFormattingThatCrossesACutIsReopenedInTheNextPiece(t *testing.T) {
	long := "**" + strings.TrimSpace(strings.Repeat("word ", 30)) + "**\n\n```go\n" + strings.Repeat("x := 1\n", 20) + "```"
	_, sent := sendOne(t, &handler{recipients: []string{"900"}, defaults: map[string]string{"chat": "vault"}},
		core.Message{Sender: "vault", Text: long, Format: core.FormatMarkdown}, map[string]any{"message_limit": 100})
	require.Greater(t, len(sent), 2)
	var bold, pre int
	for i, p := range sent {
		text := p["text"]
		assert.LessOrEqual(t, len([]rune(text)), 100, "piece %d within the limit", i)
		for _, e := range ents(t, p) {
			require.GreaterOrEqual(t, e.Offset, 0)
			require.LessOrEqual(t, e.Offset+e.Length, len(utf16.Encode([]rune(text))), "piece %d: entity inside its own text", i)
			assert.NotEmpty(t, strings.TrimSpace(covered(text, e)))
			switch e.Type {
			case models.MessageEntityTypeBold:
				bold++
				assert.NotContains(t, covered(text, e), "x :=")
			case models.MessageEntityTypePre:
				pre++
				assert.Equal(t, "go", e.Language)
				assert.NotContains(t, covered(text, e), "word")
			}
		}
	}
	assert.GreaterOrEqual(t, bold, 2, "the bold run spans a cut and is drawn on both sides")
	assert.GreaterOrEqual(t, pre, 2, "so is the code block")
}

func TestARefusedFormattedMessageIsSentAgainAsPlainText(t *testing.T) {
	s := newServer(t)
	s.refuseEntities = true
	b, err := newBot(spec(s, nil), &handler{recipients: []string{"900"}})
	require.NoError(t, err)
	require.NoError(t, b.For("chat").Send(context.Background(), core.Message{Sender: "vault", Text: "**x** y", Format: core.FormatMarkdown}), "the message still goes out")
	require.Len(t, s.snapshot(&s.refused), 1)
	sent := s.snapshot(&s.sent)
	require.Len(t, sent, 1)
	assert.Equal(t, "[vault]: x y", sent[0]["text"], "the same text, without its formatting")
	assert.Empty(t, sent[0]["entities"])
}

func TestAPlainSendThatFailsIsNotSentTwice(t *testing.T) {
	s := newServer(t)
	s.mode, s.describe = "okfalse", "Bad Request: chat not found"
	b, err := newBot(spec(s, nil), &handler{recipients: []string{"900"}})
	require.NoError(t, err)
	assert.Error(t, b.For("chat").Send(context.Background(), core.Message{Sender: "pao", Text: "**x**"}))
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Equal(t, 1, s.attempts, "a plain message that fails is not retried")
}

func TestClosingAFormattedMessageKeepsItsFormatting(t *testing.T) {
	s := newServer(t)
	b, err := newBot(spec(s, nil), &handler{recipients: []string{"900"}})
	require.NoError(t, err)
	require.NoError(t, b.For("chat").Send(context.Background(), core.Message{ID: "m1", Sender: "vault", Text: "pick **one**", Format: core.FormatMarkdown, Actions: gateActions}))
	require.NoError(t, b.CloseActions(context.Background(), "m1", "allowed"))
	edits := s.snapshot(&s.edits)
	require.Len(t, edits, 1)
	assert.Equal(t, "[vault]: pick one\n\n— allowed", edits[0]["text"])
	es := ents(t, edits[0])
	require.Len(t, es, 1)
	assert.Equal(t, "one", covered(edits[0]["text"], es[0]))
}

func TestFormattingIsCutWithAClosedTextThatWasShortened(t *testing.T) {
	es := []models.MessageEntity{{Type: models.MessageEntityTypeBold, Offset: 0, Length: 10}, {Type: models.MessageEntityTypeCode, Offset: 12, Length: 3}}
	assert.Equal(t, []models.MessageEntity{{Type: models.MessageEntityTypeBold, Offset: 0, Length: 8}}, within(es, 8))
}

func TestPiecesOfPlainTextMatchSplit(t *testing.T) {
	text := strings.Repeat("ab cd\nef ", 40)
	var got []string
	for _, p := range pieces(markdown.Doc{Text: text}, 37) {
		got = append(got, p.text)
		assert.Nil(t, p.ents)
	}
	assert.Equal(t, split(text, 37), got)
}
