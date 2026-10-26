package main

import (
	"html"
	"regexp"
	"strings"
)

var (
	reDropBlocks = regexp.MustCompile(`(?is)<(script|style|noscript|svg|math|button)\b[^>]*>.*?</(script|style|noscript|svg|math|button)>`)
	reSup        = regexp.MustCompile(`(?is)<sup\b[^>]*>.*?</sup>`)
	reComment    = regexp.MustCompile(`(?s)<!--.*?-->`)
	reHeadOpen   = regexp.MustCompile(`(?i)<h([1-6])\b[^>]*>`)
	reHeadClose  = regexp.MustCompile(`(?i)</h[1-6]>`)
	reCell       = regexp.MustCompile(`(?i)</t[dh]>`)
	reBlock      = regexp.MustCompile(`(?i)</?(p|div|li|ul|ol|tr|table|tbody|thead|section|details|summary|dd|dt|dl|br|blockquote|figure|figcaption|pre|article|header|footer|caption)\b[^>]*>`)
	reTag        = regexp.MustCompile(`(?s)<[^>]*>`)
	reBracketRef = regexp.MustCompile(`\[(?:\d+|[a-z]|edit|citation needed|note \d+)\]`)
	reSpaces     = regexp.MustCompile(`[ \t\x{00a0}]+`)
	reTitle      = regexp.MustCompile(`(?is)<title>(.*?)</title>`)
	reH2Text     = regexp.MustCompile(`(?is)<h2\b[^>]*>(.*?)</h2>`)
)

// Sections after which an article is mostly citations and links.
var tailSections = map[string]bool{
	"references": true, "notes": true, "external links": true, "further reading": true,
	"see also": true, "bibliography": true, "sources": true, "citations": true,
	"footnotes": true, "notes and references": true, "works cited": true,
}

// htmlTitle returns the <title> of a page.
func htmlTitle(doc string) string {
	m := reTitle.FindStringSubmatch(doc)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(reTag.ReplaceAllString(m[1], "")))
}

// htmlToText turns an article page into plain text, with "## " heading lines.
func htmlToText(doc string) string {
	// Cut everything from the first reference-style section onwards.
	for _, loc := range reH2Text.FindAllStringSubmatchIndex(doc, -1) {
		heading := strings.ToLower(strings.TrimSpace(html.UnescapeString(reTag.ReplaceAllString(doc[loc[2]:loc[3]], ""))))
		if tailSections[heading] {
			doc = doc[:loc[0]]
			break
		}
	}
	if i := strings.Index(strings.ToLower(doc), "<body"); i >= 0 {
		doc = doc[i:]
	}
	doc = reComment.ReplaceAllString(doc, "")
	doc = reDropBlocks.ReplaceAllString(doc, "")
	doc = reSup.ReplaceAllString(doc, "")
	doc = reHeadOpen.ReplaceAllString(doc, "\n## ")
	doc = reHeadClose.ReplaceAllString(doc, "\n")
	doc = reCell.ReplaceAllString(doc, " | ")
	doc = reBlock.ReplaceAllString(doc, "\n")
	doc = reTag.ReplaceAllString(doc, "")
	doc = html.UnescapeString(doc)
	doc = reBracketRef.ReplaceAllString(doc, "")

	var out []string
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(reSpaces.ReplaceAllString(line, " "))
		line = strings.Trim(line, "| ")
		if line == "" || line == "##" {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// Chunk is a passage of an article.
type Chunk struct {
	Doc     int    // index into the document list
	Pos     int    // order within the document
	Section string // heading it sits under
	Text    string
	Score   float64
}

const chunkTarget = 900

// chunkText splits article text into passages of roughly chunkTarget chars,
// keeping track of the section heading for each.
func chunkText(doc int, text string) []Chunk {
	var chunks []Chunk
	section := "Introduction"
	var buf strings.Builder
	flush := func() {
		t := strings.TrimSpace(buf.String())
		if len(t) > 40 {
			chunks = append(chunks, Chunk{Doc: doc, Pos: len(chunks), Section: section, Text: t})
		}
		buf.Reset()
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "## ") {
			flush()
			section = strings.TrimSpace(line[3:])
			continue
		}
		for len(line) > chunkTarget*2 {
			cut := sentenceCut(line, chunkTarget)
			if buf.Len() > 0 {
				flush()
			}
			buf.WriteString(line[:cut])
			flush()
			line = strings.TrimSpace(line[cut:])
		}
		if buf.Len() > 0 && buf.Len()+len(line) > chunkTarget {
			flush()
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(line)
	}
	flush()
	return chunks
}

// sentenceCut finds a sentence boundary near n.
func sentenceCut(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	for i := n; i < len(s) && i < n+400; i++ {
		if (s[i] == '.' || s[i] == '?' || s[i] == '!') && i+1 < len(s) && s[i+1] == ' ' {
			return i + 1
		}
	}
	for i := n; i > n/2; i-- {
		if s[i] == ' ' {
			return i
		}
	}
	return n
}

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a about above after again against all am an and any are as at be because been before being below between both but by can could did do does doing down during each few for from further had has have having he her here hers herself him himself his how i if in into is it its itself just me more most my myself no nor not now of off on once only or other our ours ourselves out over own same she should so some such than that the their theirs them themselves then there these they this those through to too under until up very was we were what when where which while who whom why will with would you your yours yourself yourselves tell explain describe give show please know use used using make get one also like much many way best`) {
		stopwords[w] = true
	}
}

var reWord = regexp.MustCompile(`[\p{L}\p{N}]+`)

// terms lower-cases, drops stopwords and does very light stemming.
func terms(s string) []string {
	var out []string
	for _, w := range reWord.FindAllString(strings.ToLower(s), -1) {
		if stopwords[w] || (len(w) < 2 && !isDigit(w)) {
			continue
		}
		out = append(out, stem(w))
	}
	return out
}

func isDigit(s string) bool { return s != "" && s[0] >= '0' && s[0] <= '9' }

func stem(w string) string {
	switch {
	case len(w) > 5 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 4 && strings.HasSuffix(w, "es") && (strings.HasSuffix(w, "ches") || strings.HasSuffix(w, "shes") || strings.HasSuffix(w, "xes") || strings.HasSuffix(w, "sses")):
		return w[:len(w)-2]
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is"):
		return w[:len(w)-1]
	}
	return w
}
