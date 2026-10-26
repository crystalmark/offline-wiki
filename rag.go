package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kiwix talks to a running kiwix-serve.
type Kiwix struct {
	Base   string // e.g. http://127.0.0.1:8471
	Client *http.Client
}

type SearchHit struct {
	Title   string
	Link    string // path on kiwix-serve, e.g. /content/<book>/A/Foo
	Snippet string
	Rank    int
}

type rssDoc struct {
	Channel struct {
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
		} `xml:"item"`
	} `xml:"channel"`
}

func (k *Kiwix) Search(ctx context.Context, book, pattern string, n int) ([]SearchHit, error) {
	q := url.Values{}
	q.Set("books.name", book)
	q.Set("pattern", pattern)
	q.Set("format", "xml")
	q.Set("pageLength", fmt.Sprint(n))
	req, _ := http.NewRequestWithContext(ctx, "GET", k.Base+"/search?"+q.Encode(), nil)
	resp, err := k.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		// kiwix-serve returns 404 for "no results" in some versions.
		if resp.StatusCode == 404 {
			return nil, nil
		}
		return nil, fmt.Errorf("kiwix search %s: HTTP %d", book, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var doc rssDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("kiwix search %s: %w", book, err)
	}
	var hits []SearchHit
	for i, it := range doc.Channel.Items {
		link := strings.TrimSpace(it.Link)
		if u, err := url.Parse(link); err == nil && u.IsAbs() {
			link = u.RequestURI()
		}
		hits = append(hits, SearchHit{
			Title:   cleanTitle(html.UnescapeString(it.Title)),
			Link:    link,
			Snippet: strings.TrimSpace(html.UnescapeString(reTag.ReplaceAllString(html.UnescapeString(it.Description), ""))),
			Rank:    i,
		})
	}
	return hits, nil
}

type suggestion struct {
	Value string `json:"value"`
	Kind  string `json:"kind"`
	Path  string `json:"path"`
}

// Suggest returns articles whose titles match term (kiwix-serve's title
// autocomplete), which finds the obvious article far more reliably than
// full-text search across millions of pages.
func (k *Kiwix) Suggest(ctx context.Context, book, term string, n int) ([]SearchHit, error) {
	q := url.Values{}
	q.Set("content", book)
	q.Set("term", term)
	q.Set("count", fmt.Sprint(n))
	req, _ := http.NewRequestWithContext(ctx, "GET", k.Base+"/suggest?"+q.Encode(), nil)
	resp, err := k.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, nil
	}
	var list []suggestion
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&list); err != nil {
		return nil, nil
	}
	var hits []SearchHit
	for _, s := range list {
		if s.Kind != "path" || s.Path == "" {
			continue
		}
		segs := strings.Split(s.Path, "/")
		for i := range segs {
			segs[i] = url.PathEscape(segs[i])
		}
		hits = append(hits, SearchHit{Title: cleanTitle(html.UnescapeString(s.Value)), Link: "/content/" + book + "/" + strings.Join(segs, "/"), Rank: len(hits)})
	}
	return hits, nil
}

var reWS = regexp.MustCompile(`\s+`)

// cleanTitle collapses line breaks and drops site-name suffixes that
// crawled websites add to page titles ("Shingles\n - NHS").
func cleanTitle(t string) string {
	t = strings.TrimSpace(reWS.ReplaceAllString(t, " "))
	for _, suf := range []string{" - Guidance - GOV.UK", " - GOV.UK", " - NHS", " | NHS", " - MedlinePlus"} {
		t = strings.TrimSuffix(t, suf)
	}
	return t
}

var reMetaRefresh = regexp.MustCompile(`(?is)<meta[^>]+http-equiv=["']?refresh["']?[^>]+content=["']?\d+;\s*url=['"]?([^'">]+)`)

// Fetch returns an article's HTML and its final link. Kiwix stores
// Wikipedia redirects (e.g. "Leg fracture" -> "Human leg#Fracture") as small
// pages with a meta refresh, which are followed here.
func (k *Kiwix) Fetch(ctx context.Context, link string, limit int64) (string, string, error) {
	for hop := 0; ; hop++ {
		req, _ := http.NewRequestWithContext(ctx, "GET", k.Base+link, nil)
		resp, err := k.Client.Do(req)
		if err != nil {
			return "", link, err
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
		resp.Body.Close()
		if err != nil {
			return "", link, err
		}
		if resp.StatusCode != 200 {
			return "", link, fmt.Errorf("fetch %s: HTTP %d", link, resp.StatusCode)
		}
		if resp.Request != nil && resp.Request.URL != nil {
			link = resp.Request.URL.RequestURI() // after any HTTP redirect
		}
		body := string(b)
		m := reMetaRefresh.FindStringSubmatch(body)
		if m == nil || len(body) > 4000 || hop >= 3 {
			return body, link, nil
		}
		base, _ := url.Parse(link)
		next, err := url.Parse(strings.TrimSpace(html.UnescapeString(m[1])))
		if err != nil {
			return body, link, nil
		}
		target := base.ResolveReference(next)
		target.Fragment = ""
		link = target.RequestURI()
	}
}

// Doc is a fetched article with its chunks.
type Doc struct {
	Title  string
	Link   string
	Book   string // label, e.g. Wikipedia
	Prior  float64
	Chunks []Chunk
}

// Source is what the UI shows for each numbered reference.
type Source struct {
	N     int    `json:"n"`
	Title string `json:"title"`
	Book  string `json:"book"`
	Link  string `json:"link"`
}

// Retrieval is the result of searching and ranking for one question.
type Retrieval struct {
	Sources []Source
	Context string // numbered source text for the prompt
}

type candidate struct {
	hit   SearchHit
	zim   LocalZim
	score float64
}

// Retrieve searches every book for every query, fetches the best articles,
// and selects the most relevant passages up to budget characters.
func Retrieve(ctx context.Context, k *Kiwix, zims []LocalZim, question string, queries []string, budget int, status func(string), rr *Reranker) (*Retrieval, error) {
	type job struct {
		zim   LocalZim
		query string
	}
	var jobs []job
	for _, q := range queries {
		for _, z := range zims {
			jobs = append(jobs, job{z, q})
		}
	}

	var mu sync.Mutex
	cands := map[string]*candidate{}
	var firstErr error
	sem := make(chan struct{}, 12)
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			n := j.zim.Book.PerQ
			if n <= 0 {
				n = 3
			}
			hits, err := k.Search(ctx, j.zim.Stem(), j.query, n)
			titles, _ := k.Suggest(ctx, j.zim.Stem(), j.query, 3)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && len(titles) == 0 {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			w := j.zim.Book.Weight
			if w == 0 {
				w = 1
			}
			add := func(h SearchHit, score float64) {
				c := cands[h.Link]
				if c == nil {
					c = &candidate{hit: h, zim: j.zim}
					cands[h.Link] = c
				}
				c.score += score
			}
			for _, h := range hits {
				add(h, w/float64(1+h.Rank))
			}
			// A title match is strong evidence: an exact title most of all.
			for _, h := range titles {
				sc := 1.5 * w / float64(1+h.Rank)
				if strings.EqualFold(h.Title, j.query) {
					sc = 3 * w
				}
				add(h, sc)
			}
		}(j)
	}
	wg.Wait()
	if len(cands) == 0 {
		if firstErr != nil {
			return nil, firstErr
		}
		return &Retrieval{}, nil
	}

	// Pick the strongest candidates, respecting each book's cap.
	list := make([]*candidate, 0, len(cands))
	for _, c := range cands {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].score > list[j].score })
	perBook := map[string]int{}
	var chosen []*candidate
	for _, c := range list {
		limit := c.zim.Book.MaxDocs
		if limit <= 0 {
			limit = 3
		}
		if perBook[c.zim.Book.Prefix] >= limit {
			continue
		}
		perBook[c.zim.Book.Prefix]++
		chosen = append(chosen, c)
		if len(chosen) >= 16 {
			break
		}
	}

	if status != nil {
		names := make([]string, 0, len(chosen))
		for _, c := range chosen {
			names = append(names, c.hit.Title)
		}
		status("Reading " + strings.Join(names, ", "))
	}

	docs := make([]*Doc, len(chosen))
	for i, c := range chosen {
		wg.Add(1)
		go func(i int, c *candidate) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			body, final, err := k.Fetch(ctx, c.hit.Link, 3<<20)
			if err != nil {
				return
			}
			title := c.hit.Title
			if final != c.hit.Link || title == "" {
				if t := cleanTitle(htmlTitle(body)); t != "" {
					title = t // the article a redirect led to
				}
			}
			d := &Doc{Title: title, Link: final, Book: c.zim.Book.Label, Prior: c.score}
			d.Chunks = chunkText(i, htmlToText(body))
			docs[i] = d
		}(i, c)
	}
	wg.Wait()

	if rr != nil && status != nil {
		status("Picking the most relevant passages…")
	}
	return selectPassages(ctx, docs, question, queries, budget, rr), nil
}

// Reranker scores passages against the question with a cross-encoder model
// (served by llamafile --rerank). It understands meaning, so "broken leg"
// doesn't match a passage about a broken-down car.
type Reranker struct {
	Base   string
	Client *http.Client
}

func (r *Reranker) Scores(ctx context.Context, query string, docs []string) ([]float64, error) {
	out := make([]float64, len(docs))
	for start := 0; start < len(docs); start += 16 {
		end := start + 16
		if end > len(docs) {
			end = len(docs)
		}
		body, _ := json.Marshal(map[string]any{"query": query, "documents": docs[start:end]})
		req, _ := http.NewRequestWithContext(ctx, "POST", r.Base+"/v1/rerank", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := r.Client.Do(req)
		if err != nil {
			return nil, err
		}
		var res struct {
			Results []struct {
				Index int     `json:"index"`
				Score float64 `json:"relevance_score"`
			} `json:"results"`
		}
		err = json.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 200 || len(res.Results) != end-start {
			return nil, fmt.Errorf("reranker: HTTP %d, %d results", resp.StatusCode, len(res.Results))
		}
		for _, x := range res.Results {
			if x.Index >= 0 && x.Index < end-start {
				out[start+x.Index] = x.Score
			}
		}
	}
	return out, nil
}

const (
	rerankCandidates = 96   // passages sent to the reranker
	rerankPerDoc     = 6    // at most this many from one article (plus its opening)
	rerankFloor      = -5.0 // below this a passage is almost never relevant
	rerankBand       = 6.0  // ...or this far below the best passage
)

// selectPassages ranks every chunk (BM25 keyword scoring, then the reranker
// when available), drops weak ones and builds the numbered context.
func selectPassages(ctx context.Context, docs []*Doc, question string, queries []string, budget int, rr *Reranker) *Retrieval {
	qTerms := map[string]float64{}
	for _, t := range terms(question) {
		qTerms[t] = 1.0
	}
	for _, q := range queries {
		for _, t := range terms(q) {
			if qTerms[t] < 0.7 {
				qTerms[t] = 0.7
			}
		}
	}

	var all []*Chunk
	for _, d := range docs {
		if d == nil {
			continue
		}
		for i := range d.Chunks {
			all = append(all, &d.Chunks[i])
		}
	}
	if len(all) == 0 {
		return &Retrieval{}
	}

	tf := make([]map[string]int, len(all))
	df := map[string]int{}
	totalLen := 0
	for i, c := range all {
		m := map[string]int{}
		ts := terms(c.Section + " " + c.Text)
		for _, t := range ts {
			m[t]++
		}
		for t := range m {
			df[t]++
		}
		tf[i] = m
		totalLen += len(ts)
	}
	avgLen := float64(totalLen) / float64(len(all))
	N := float64(len(all))
	const k1, b = 1.4, 0.75

	maxPrior := 0.0
	for _, d := range docs {
		if d != nil && d.Prior > maxPrior {
			maxPrior = d.Prior
		}
	}
	for i, c := range all {
		d := docs[c.Doc]
		dl := 0
		for _, n := range tf[i] {
			dl += n
		}
		s := 0.0
		for t, w := range qTerms {
			f := float64(tf[i][t])
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (N-float64(df[t])+0.5)/(float64(df[t])+0.5))
			s += w * idf * f * (k1 + 1) / (f + k1*(1-b+b*float64(dl)/avgLen))
		}
		// Prefer passages from articles the search engine ranked highly,
		// lead passages, and articles whose title matches the question.
		s *= 1 + 0.6*d.Prior/maxPrior
		if c.Pos == 0 {
			s = s*1.25 + 0.5
		}
		for _, t := range terms(d.Title) {
			if qTerms[t] > 0 {
				s += 0.6
			}
		}
		c.Score = s
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Score > all[j].Score })

	var ranked []*Chunk
	reranked := false
	if rr != nil {
		// Give every article a fair hearing: its opening plus its best few
		// passages, so one long page can't crowd the others out.
		var cand []*Chunk
		perDoc := map[int]int{}
		for _, c := range all {
			if c.Pos == 0 || perDoc[c.Doc] < rerankPerDoc {
				cand = append(cand, c)
				perDoc[c.Doc]++
			}
			if len(cand) >= rerankCandidates {
				break
			}
		}
		texts := make([]string, len(cand))
		for i, c := range cand {
			d := docs[c.Doc]
			t := d.Title
			if c.Section != "Introduction" {
				t += " - " + c.Section
			}
			text := c.Text
			if len(text) > 1500 {
				text = text[:1500]
			}
			texts[i] = t + "\n" + text
		}
		rq := question
		if len(queries) > 0 && !strings.EqualFold(queries[0], question) {
			rq += " (" + queries[0] + ")"
		}
		if scores, err := rr.Scores(ctx, rq, texts); err == nil {
			for i, c := range cand {
				c.Score = scores[i]
			}
			sort.SliceStable(cand, func(i, j int) bool { return cand[i].Score > cand[j].Score })
			floor := math.Max(rerankFloor, cand[0].Score-rerankBand)
			for i, c := range cand {
				if i < 2 || c.Score >= floor {
					ranked = append(ranked, c)
				}
			}
			reranked = true
		}
		logRetrieval(question, rq, cand, docs, reranked)
	}
	if !reranked {
		// Keyword scores only: drop passages far weaker than the best one.
		floor := all[0].Score * 0.2
		for _, c := range all {
			if c.Score > 0 && c.Score >= floor {
				ranked = append(ranked, c)
			}
		}
	}

	picked := map[int][]*Chunk{}
	used := 0
	seen := map[string]bool{} // WikiMed repeats Wikipedia's medical articles
	for _, c := range ranked {
		if len(picked[c.Doc]) >= 4 || seen[c.Text] {
			continue
		}
		seen[c.Text] = true
		if used+len(c.Text) > budget && used > 0 {
			continue
		}
		picked[c.Doc] = append(picked[c.Doc], c)
		used += len(c.Text) + 60
		if used >= budget {
			break
		}
	}

	// Number sources by best passage score, keep passages in article order.
	type ds struct {
		doc  int
		best float64
	}
	var order []ds
	for di, cs := range picked {
		best := math.Inf(-1)
		for _, c := range cs {
			if c.Score > best {
				best = c.Score
			}
		}
		order = append(order, ds{di, best})
	}
	sort.Slice(order, func(i, j int) bool { return order[i].best > order[j].best })

	r := &Retrieval{}
	var sb strings.Builder
	for n, o := range order {
		d := docs[o.doc]
		cs := picked[o.doc]
		sort.Slice(cs, func(i, j int) bool { return cs[i].Pos < cs[j].Pos })
		fmt.Fprintf(&sb, "[%d] %s (%s)\n", n+1, d.Title, d.Book)
		for i, c := range cs {
			if i > 0 {
				sb.WriteString("…\n")
			}
			if c.Section != "Introduction" {
				fmt.Fprintf(&sb, "(%s) ", c.Section)
			}
			sb.WriteString(c.Text)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
		r.Sources = append(r.Sources, Source{N: n + 1, Title: d.Title, Book: d.Book, Link: d.Link})
	}
	r.Context = sb.String()
	return r
}

// logRetrieval records the reranker's scores for the last question, to help
// tune the thresholds (runtime folder, retrieval.log).
func logRetrieval(question, rq string, cand []*Chunk, docs []*Doc, ok bool) {
	w := logFile("retrieval.log")
	fmt.Fprintf(w, "Question: %s\nReranker query: %s (ok=%v)\n", question, rq, ok)
	floor := rerankFloor
	if len(cand) > 0 {
		floor = math.Max(rerankFloor, cand[0].Score-rerankBand)
	}
	for i, c := range cand {
		mark := " "
		if i < 2 || c.Score >= floor {
			mark = "+"
		}
		fmt.Fprintf(w, "%s %7.2f  %s [%s] %.70q\n", mark, c.Score, docs[c.Doc].Title, docs[c.Doc].Book, c.Text)
	}
	if c, ok := w.(io.Closer); ok {
		c.Close()
	}
}

// waitHTTP polls url until it returns 200 or the timeout passes.
func waitHTTP(ctx context.Context, u string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	c := &http.Client{Timeout: 3 * time.Second}
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		resp, err := c.Get(u)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return true
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}
