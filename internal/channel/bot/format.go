package bot

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/markdown"
)

// piece is one message as the service is sent it: plain text and the ranges of
// it that are formatted. The service is given the ranges rather than a markup
// language, so nothing in the text needs escaping and no text can fail to parse
// (ADR-0050 point 5).
type piece struct {
	text string
	ents []models.MessageEntity
}

// compose is a message as a conversation shows it: its line (the sender's name
// before it where line puts one) and, for an agent's Markdown, the formatting
// of its words. Only the words are read as Markdown, never the name before them
// or a plain message (ADR-0050 point 2).
func (b *Bot) compose(m core.Message, conversation string) markdown.Doc {
	line := b.line(m, conversation)
	if m.Format != core.FormatMarkdown {
		return markdown.Doc{Text: line}
	}
	prefix, ok := strings.CutSuffix(line, m.Text)
	if !ok {
		return markdown.Doc{Text: line}
	}
	d := markdown.Render(m.Text)
	shift := utf8.RuneCountInString(prefix)
	spans := make([]markdown.Span, len(d.Spans))
	for i, s := range d.Spans {
		s.Start += shift
		s.End += shift
		spans[i] = s
	}
	return markdown.Doc{Text: prefix + d.Text, Spans: spans}
}

// pieces cuts a composed message into pieces of at most limit characters, as
// split does, and gives each piece the part of every span that falls in it: a
// span that crosses a cut is closed at the cut and opened again after it.
func pieces(d markdown.Doc, limit int) []piece {
	r := []rune(d.Text)
	var out []piece
	for _, c := range cuts(r, limit) {
		p := piece{text: string(r[c[0]:c[1]])}
		var spans []markdown.Span
		for _, s := range d.Spans {
			s.Start, s.End = max(s.Start, c[0])-c[0], min(s.End, c[1])-c[0]
			if s.Start < s.End {
				spans = append(spans, s)
			}
		}
		p.ents = entities(r[c[0]:c[1]], spans)
		out = append(out, p)
	}
	return out
}

// entities are spans over text as the service counts them: offsets and lengths
// in UTF-16 code units.
func entities(text []rune, spans []markdown.Span) []models.MessageEntity {
	if len(spans) == 0 {
		return nil
	}
	at := make([]int, len(text)+1)
	for i, c := range text {
		at[i+1] = at[i] + utf16.RuneLen(c)
	}
	out := make([]models.MessageEntity, 0, len(spans))
	for _, s := range spans {
		e := models.MessageEntity{Offset: at[s.Start], Length: at[s.End] - at[s.Start]}
		switch s.Style {
		case markdown.Bold:
			e.Type = models.MessageEntityTypeBold
		case markdown.Italic:
			e.Type = models.MessageEntityTypeItalic
		case markdown.Strike:
			e.Type = models.MessageEntityTypeStrikethrough
		case markdown.Code:
			e.Type = models.MessageEntityTypeCode
		case markdown.Pre:
			e.Type, e.Language = models.MessageEntityTypePre, s.Lang
		case markdown.Link:
			e.Type, e.URL = models.MessageEntityTypeTextLink, s.URL
		case markdown.Quote:
			e.Type = models.MessageEntityTypeBlockquote
		default:
			continue
		}
		out = append(out, e)
	}
	return out
}

// within is ents cut to the first n UTF-16 units of their text.
func within(ents []models.MessageEntity, n int) []models.MessageEntity {
	var out []models.MessageEntity
	for _, e := range ents {
		if e.Offset >= n {
			continue
		}
		e.Length = min(e.Length, n-e.Offset)
		out = append(out, e)
	}
	return out
}

// units is how many UTF-16 code units the service counts s as.
func units(s string) int {
	n := 0
	for _, c := range s {
		n += utf16.RuneLen(c)
	}
	return n
}
