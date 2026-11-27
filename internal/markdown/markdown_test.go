package markdown

import (
	"fmt"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// shown lists a Doc's spans as "style:text", with the address or language
// after an "@" where there is one, in order.
func shown(d Doc) []string {
	r := []rune(d.Text)
	out := []string{}
	for _, s := range d.Spans {
		e := s.Style.String() + ":" + string(r[s.Start:s.End])
		if s.URL != "" {
			e += "@" + s.URL
		}
		if s.Lang != "" {
			e += "@" + s.Lang
		}
		out = append(out, e)
	}
	return out
}

func TestInlineFormattingBecomesSpansOverPlainText(t *testing.T) {
	for _, c := range []struct {
		src, text string
		spans     []string
	}{
		{"just words", "just words", []string{}},
		{"**bold** and *it* and _it2_", "bold and it and it2", []string{"bold:bold", "italic:it", "italic:it2"}},
		{"~~gone~~ `x := 1`", "gone x := 1", []string{"strike:gone", "code:x := 1"}},
		{"snake_case_name stays", "snake_case_name stays", []string{}},
		{`\*not bold\* &amp; &#65;`, "*not bold* & A", []string{}},
		{"`a\\*b`", `a\*b`, []string{`code:a\*b`}},
		{"[site](https://e.example/a?b=1)", "site", []string{"link:site@https://e.example/a?b=1"}},
		{"[mail](mailto:x@e.example)", "mail", []string{"link:mail@mailto:x@e.example"}},
		{"see [the note](notes/a.md)", "see the note", []string{}},
		{"[](https://e.example)", "https://e.example", []string{"link:https://e.example@https://e.example"}},
		{"<https://e.example>", "https://e.example", []string{}},
		{"![a cat](https://e.example/c.png)", "a cat", []string{"link:a cat@https://e.example/c.png"}},
		{"one\ntwo", "one\ntwo", []string{}},
		{"**ünï 😀** x", "ünï 😀 x", []string{"bold:ünï 😀"}},
	} {
		d := Render(c.src)
		assert.Equal(t, c.text, d.Text, c.src)
		assert.Equal(t, c.spans, shown(d), c.src)
	}
}

func TestBlocksKeepTheirShapeInText(t *testing.T) {
	for _, c := range []struct {
		src, text string
		spans     []string
	}{
		{"# Title\n\nbody", "Title\n\nbody", []string{"bold:Title"}},
		{"```go\nx := `a`\n\\*y\n```", "x := `a`\n\\*y", []string{"pre:x := `a`\n\\*y@go"}},
		{"    indented\n    code", "indented\ncode", []string{"pre:indented\ncode"}},
		{"- one\n- **two**", "• one\n• two", []string{"bold:two"}},
		{"3. three\n4. four", "3. three\n4. four", []string{}},
		{"- a\n  - b\n    more\n- c", "• a\n  • b\n    more\n• c", []string{}},
		{"- [x] done\n- [ ] todo", "• ☑ done\n• ☐ todo", []string{}},
		{"> said\n> > again\n\nafter", "said\n\nagain\n\nafter", []string{"quote:said\n\nagain"}},
		{"a\n\n---\n\nb", "a\n\n———\n\nb", []string{}},
		{"| a | long |\n|---|---|\n| **1** | 2 |", "a | long\n--+-----\n1 | 2", []string{"pre:a | long\n--+-----\n1 | 2"}},
	} {
		d := Render(c.src)
		assert.Equal(t, c.text, d.Text, c.src)
		assert.Equal(t, c.spans, shown(d), c.src)
	}
}

func TestFormattingAroundCodeIsCutAroundIt(t *testing.T) {
	d := Render("**run `make` now**")
	assert.Equal(t, "run make now", d.Text)
	assert.Equal(t, []string{"bold:run ", "code:make", "bold: now"}, shown(d), "a span never holds code")

	d = Render("[call `f`](https://e.example)")
	assert.Equal(t, []string{"link:call f@https://e.example"}, shown(d), "a link keeps its code as text")

	d = Render("> `q`")
	assert.Equal(t, []string{"quote:q", "code:q"}, shown(d), "a quote may hold code")
}

func TestPlainIsTheTextAReaderSees(t *testing.T) {
	assert.Equal(t, "Done: wrote notes/a.md", Plain("**Done:** wrote `notes/a.md`"))
}

// corpus is Markdown of the shapes an agent writes, malformed included.
var corpus = []string{
	"", "plain", "**unclosed", "`unclosed", "```\nunclosed fence", "*a **b** c*", "***both***",
	"**a `b` c `d` e**", "~~a **b `c` d** e~~", "> q **b `c`**\n> > n", "- **a** `b`\n  > q\n- [l](https://x.example)",
	"| a | b |\n|---|---|\n| `x` | [y](https://y.example) |", "[**b**](https://e.example) `c`", "<b>html</b> &lt;",
	"1. a\n\n2. b\n\n   ```\n   code\n   ```", "😀 **😀** `😀`", "line  \nbreak\\\nagain", "# h *i*\n## h2", "[a\\\n](https://x.example)",
}

func TestEveryDocKeepsItsPromises(t *testing.T) {
	for _, src := range corpus {
		d := Render(src)
		n := utf8.RuneCountInString(d.Text)
		for i, s := range d.Spans {
			require.True(t, 0 <= s.Start && s.Start < s.End && s.End <= n, "%q: span %d out of the text: %+v", src, i, s)
			for j, o := range d.Spans {
				if i == j {
					continue
				}
				in := s.Start >= o.Start && s.End <= o.End
				apart := s.End <= o.Start || o.End <= s.Start
				holds := o.Start >= s.Start && o.End <= s.End
				assert.True(t, in || apart || holds, "%q: spans cross: %+v %+v", src, s, o)
				if in && (o.Style == Code || o.Style == Pre) {
					assert.Failf(t, "span inside code", "%q: %+v inside %+v", src, s, o)
				}
				if in && s.Style == Quote && o.Style == Quote {
					assert.Failf(t, "quote inside quote", "%q", src)
				}
				if in && (s.Style == Code || s.Style == Pre) && o.Style != Quote && o.Style != Link {
					assert.Failf(t, "code inside a span", "%q: %+v inside %+v", src, s, o)
				}
			}
		}
	}
}

func TestRawTextIsNeverUnescaped(t *testing.T) {
	src := []byte(`a\*b &amp;`)
	raw := ast.NewRawTextSegment(text.NewSegment(0, len(src)))
	w := &writer{src: src}
	w.inline(raw)
	assert.Equal(t, `a\*b &amp;`, w.out.String(), "raw text is shown as it is")

	plain := ast.NewTextSegment(text.NewSegment(0, len(src)))
	w = &writer{src: src}
	w.inline(plain)
	assert.Equal(t, "a*b &", w.out.String())
}

func ExampleRender() {
	d := Render("**Saved** `inbox/a.md`")
	fmt.Printf("%q %v\n", d.Text, shown(d))
	// Output: "Saved inbox/a.md" [bold:Saved code:inbox/a.md]
}
