// Package markdown reads the Markdown an agent writes (ADR-0050) into plain text
// and the formatted spans over it. A channel draws the spans in its own native
// form; a channel with no rich formatting shows the text alone, which carries no
// markup.
//
// The text keeps what a reader needs where a span cannot say it: a list item
// keeps its bullet or number, a table is laid out in columns, and a link whose
// address cannot be a span is shown as its text.
package markdown

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Style is how a span is drawn.
type Style int

const (
	Bold Style = iota + 1
	Italic
	Strike
	// Code is code inside a line.
	Code
	// Pre is a block of code; Lang names its language when the source did.
	Pre
	// Link is text that leads to URL.
	Link
	// Quote is a quoted block.
	Quote
)

func (s Style) String() string {
	switch s {
	case Bold:
		return "bold"
	case Italic:
		return "italic"
	case Strike:
		return "strike"
	case Code:
		return "code"
	case Pre:
		return "pre"
	case Link:
		return "link"
	case Quote:
		return "quote"
	}
	return fmt.Sprintf("Style(%d)", int(s))
}

// Span is a formatted run of a Doc's text, from rune Start up to rune End.
type Span struct {
	Style      Style
	Start, End int
	URL        string
	Lang       string
}

// Doc is rendered Markdown: the text a reader sees and the spans over it. Spans
// nest properly (one inside another or apart, never crossing), and none is
// empty. A Code or Pre span holds no other span, and no Quote is inside another.
type Doc struct {
	Text  string
	Spans []Span
}

var parser = goldmark.New(goldmark.WithExtensions(
	extension.Strikethrough, extension.Table, extension.TaskList,
)).Parser()

// Render reads src as Markdown (CommonMark, with strikethrough, tables and task
// lists).
func Render(src string) Doc {
	b := []byte(src)
	w := &writer{src: b}
	w.blocks(parser.Parse(text.NewReader(b)), "\n\n")
	return Doc{Text: strings.TrimRight(w.out.String(), "\n"), Spans: w.spans}.clip()
}

// Plain is the text of src with its markup gone: what a reader of the
// rendered message sees.
func Plain(src string) string { return Render(src).Text }

type writer struct {
	src    []byte
	out    strings.Builder
	n      int // runes written
	indent string
	spans  []Span
	quoted int // depth of quotes being written
}

// write appends s, indenting each new line to the current list depth.
func (w *writer) write(s string) {
	if w.indent != "" {
		s = strings.ReplaceAll(s, "\n", "\n"+w.indent)
	}
	w.out.WriteString(s)
	w.n += utf8.RuneCountInString(s)
}

// span records a span over what was written since start.
func (w *writer) span(st Style, start int, url, lang string) {
	if w.n > start {
		w.spans = append(w.spans, Span{Style: st, Start: start, End: w.n, URL: url, Lang: lang})
	}
}

// blocks writes each block child of n, sep between them.
func (w *writer) blocks(n ast.Node, sep string) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if c != n.FirstChild() {
			w.write(sep)
		}
		w.block(c)
	}
}

func (w *writer) block(n ast.Node) {
	switch n := n.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		w.inlines(n)
	case *ast.Heading:
		start := w.n
		w.inlines(n)
		w.span(Bold, start, "", "")
	case *ast.ThematicBreak:
		w.write("———")
	case *ast.FencedCodeBlock:
		w.pre(n, string(n.Language(w.src)))
	case *ast.CodeBlock:
		w.pre(n, "")
	case *ast.HTMLBlock:
		w.write(strings.TrimRight(w.lines(n), "\n"))
	case *ast.Blockquote:
		start := w.n
		w.quoted++
		w.blocks(n, "\n\n")
		w.quoted--
		if w.quoted == 0 {
			w.span(Quote, start, "", "")
		}
	case *ast.List:
		w.list(n)
	case *extast.Table:
		w.table(n)
	default:
		w.blocks(n, "\n\n")
	}
}

func (w *writer) lines(n ast.Node) string {
	var b strings.Builder
	l := n.Lines()
	for i := 0; i < l.Len(); i++ {
		seg := l.At(i)
		b.Write(seg.Value(w.src))
	}
	return b.String()
}

func (w *writer) pre(n ast.Node, lang string) {
	start := w.n
	w.write(strings.TrimRight(w.lines(n), "\n"))
	w.span(Pre, start, "", lang)
}

func (w *writer) list(l *ast.List) {
	sep := "\n"
	if !l.IsTight {
		sep = "\n\n"
	}
	i := 0
	for c := l.FirstChild(); c != nil; c = c.NextSibling() {
		if c != l.FirstChild() {
			w.write(sep)
		}
		marker := "• "
		if l.IsOrdered() {
			marker = fmt.Sprintf("%d. ", l.Start+i)
		}
		w.write(marker)
		outer := w.indent
		w.indent += strings.Repeat(" ", utf8.RuneCountInString(marker))
		w.blocks(c, sep)
		w.indent = outer
		i++
	}
}

// table lays the table out in columns of plain text, as a block of code, so the
// columns line up in a fixed-width face.
func (w *writer) table(t *extast.Table) {
	var rows [][]string
	for r := t.FirstChild(); r != nil; r = r.NextSibling() {
		var row []string
		for c := r.FirstChild(); c != nil; c = c.NextSibling() {
			row = append(row, Plain(w.inlineSource(c)))
		}
		rows = append(rows, row)
	}
	var width []int
	for _, row := range rows {
		for i, cell := range row {
			for len(width) <= i {
				width = append(width, 0)
			}
			width[i] = max(width[i], utf8.RuneCountInString(cell))
		}
	}
	var b strings.Builder
	for ri, row := range rows {
		if ri > 0 {
			b.WriteString("\n")
		}
		for i, cell := range row {
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(cell)
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", width[i]-utf8.RuneCountInString(cell)))
			}
		}
		if ri == 0 && len(rows) > 1 {
			b.WriteString("\n")
			for i, wd := range width {
				if i > 0 {
					b.WriteString("-+-")
				}
				b.WriteString(strings.Repeat("-", max(wd, 1)))
			}
		}
	}
	start := w.n
	w.write(b.String())
	w.span(Pre, start, "", "")
}

// inlineSource is the source text a table cell spans, to render on its own.
func (w *writer) inlineSource(n ast.Node) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch c := c.(type) {
		case *ast.Text:
			b.Write(c.Segment.Value(w.src))
			return ast.WalkSkipChildren, nil
		case *ast.CodeSpan:
			b.WriteString("`")
			for t := c.FirstChild(); t != nil; t = t.NextSibling() {
				if tx, ok := t.(*ast.Text); ok {
					b.Write(tx.Segment.Value(w.src))
				}
			}
			b.WriteString("`")
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

func (w *writer) inlines(n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		w.inline(c)
	}
}

func (w *writer) inline(n ast.Node) {
	switch n := n.(type) {
	case *ast.Text:
		v := n.Segment.Value(w.src)
		if !n.IsRaw() {
			v = unescape(v)
		}
		w.write(string(v))
		if n.SoftLineBreak() || n.HardLineBreak() {
			w.write("\n")
		}
	case *ast.String:
		v := n.Value
		if !n.IsRaw() && !n.IsCode() {
			v = unescape(v)
		}
		w.write(string(v))
	case *ast.CodeSpan:
		start := w.n
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch t := c.(type) {
			case *ast.Text:
				w.write(string(t.Segment.Value(w.src)))
			case *ast.String:
				w.write(string(t.Value))
			}
		}
		w.span(Code, start, "", "")
	case *ast.Emphasis:
		st := Italic
		if n.Level >= 2 {
			st = Bold
		}
		start := w.n
		w.inlines(n)
		w.span(st, start, "", "")
	case *extast.Strikethrough:
		start := w.n
		w.inlines(n)
		w.span(Strike, start, "", "")
	case *ast.Link:
		start := w.n
		w.inlines(n)
		dest := string(unescape(n.Destination))
		if w.n == start {
			w.write(dest)
		}
		if linkable(dest) {
			w.span(Link, start, dest, "")
		}
	case *ast.AutoLink:
		w.write(string(n.Label(w.src)))
	case *ast.Image:
		start := w.n
		w.inlines(n)
		dest := string(unescape(n.Destination))
		if w.n == start {
			w.write(dest)
		}
		if linkable(dest) {
			w.span(Link, start, dest, "")
		}
	case *ast.RawHTML:
		for i := 0; i < n.Segments.Len(); i++ {
			seg := n.Segments.At(i)
			w.write(string(seg.Value(w.src)))
		}
	case *extast.TaskCheckBox:
		if n.IsChecked {
			w.write("☑ ")
		} else {
			w.write("☐ ")
		}
	default:
		w.inlines(n)
	}
}

// unescape resolves backslash escapes and character references, as a Markdown
// reader shows them.
func unescape(b []byte) []byte {
	return util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(b)))
}

// linkable reports whether an address can be a link span: an absolute web or
// mail address. A relative path, such as a note's, is shown as its text.
func linkable(dest string) bool {
	u, err := url.Parse(dest)
	if err != nil || u.Host == "" && u.Scheme != "mailto" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return true
	case "mailto":
		return u.Opaque != ""
	}
	return false
}

// clip keeps the spans to the text and to the rules a Doc promises: nothing
// past the end and nothing empty; no Code or Pre inside a Link (the link is
// kept); and no span holds Code or Pre except a Quote, so a bold, italic or
// struck run around code is cut into the runs on either side of it.
func (d Doc) clip() Doc {
	n := utf8.RuneCountInString(d.Text)
	isCode := func(s Span) bool { return s.Style == Code || s.Style == Pre }
	inside := func(a, b Span) bool { return a.Start >= b.Start && a.End <= b.End }

	var spans []Span
	for _, s := range d.Spans {
		s.End = min(s.End, n)
		if s.Start < s.End {
			spans = append(spans, s)
		}
	}
	var codes, links []Span
	for _, s := range spans {
		switch {
		case isCode(s):
			codes = append(codes, s)
		case s.Style == Link:
			links = append(links, s)
		}
	}
	var out []Span
	for _, s := range spans {
		switch {
		case isCode(s):
			if !slices.ContainsFunc(links, func(l Span) bool { return inside(s, l) }) {
				out = append(out, s)
			}
		case s.Style == Quote || s.Style == Link:
			out = append(out, s)
		default:
			out = append(out, cut(s, codes, links)...)
		}
	}
	slices.SortStableFunc(out, func(a, b Span) int {
		if a.Start != b.Start {
			return a.Start - b.Start
		}
		if a.End != b.End {
			return b.End - a.End
		}
		return rank(a.Style) - rank(b.Style)
	})
	d.Spans = out
	return d
}

// rank orders spans over the same run outermost first: a quote holds a link,
// a link holds formatting, and code holds nothing.
func rank(s Style) int {
	switch s {
	case Quote:
		return 0
	case Link:
		return 1
	case Code, Pre:
		return 3
	}
	return 2
}

// cut is s with the code spans it holds cut out of it. A code span inside a
// link is not cut out, since the link keeps it as text.
func cut(s Span, codes, links []Span) []Span {
	parts := []Span{s}
	for _, c := range codes {
		if slices.ContainsFunc(links, func(l Span) bool { return c.Start >= l.Start && c.End <= l.End }) {
			continue
		}
		var next []Span
		for _, p := range parts {
			if c.End <= p.Start || c.Start >= p.End {
				next = append(next, p)
				continue
			}
			if c.Start > p.Start {
				l := p
				l.End = c.Start
				next = append(next, l)
			}
			if c.End < p.End {
				r := p
				r.Start = c.End
				next = append(next, r)
			}
		}
		parts = next
	}
	return parts
}

// Paths are the relative addresses src links to, in the order they first
// appear, each once: the links ADR-0050 shows only as their text, which may
// name a file beside the agent (ADR-0052 point 4). A fragment or query is left
// off and an escaped character is read back.
func Paths(src string) []string {
	b := []byte(src)
	var out []string
	seen := map[string]bool{}
	_ = ast.Walk(parser.Parse(text.NewReader(b)), func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var dest []byte
		switch n := n.(type) {
		case *ast.Link:
			dest = n.Destination
		case *ast.Image:
			dest = n.Destination
		default:
			return ast.WalkContinue, nil
		}
		d := string(unescape(dest))
		if u, err := url.Parse(d); err != nil || u.Scheme != "" || u.Host != "" {
			return ast.WalkContinue, nil
		} else if p, err := url.PathUnescape(u.EscapedPath()); err == nil {
			d = p
		} else {
			d = u.Path
		}
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
		return ast.WalkContinue, nil
	})
	return out
}
