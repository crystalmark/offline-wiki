package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const hypoArticle = `<html><head><title>Hypothermia</title></head><body>
<h1>Hypothermia</h1>
<p><b>Hypothermia</b> is defined as a body core temperature below 35.0 °C (95.0 °F) in humans.<sup class="reference">[1]</sup> Symptoms depend on the temperature.</p>
<details><summary><h2>Signs and symptoms</h2></summary>
<p>In mild hypothermia there is shivering and mental confusion. In moderate hypothermia shivering stops and confusion increases.</p>
</details>
<h2>Treatment</h2>
<p>Treatment of mild hypothermia involves warm drinks, warm clothing, and voluntary physical activity. In those with moderate hypothermia, heating blankets and warmed intravenous fluids are recommended.</p>
<table class="infobox"><tr><th>Specialty</th><td>Critical care medicine</td></tr></table>
<h2>References</h2>
<ol><li>Some citation text that should be removed</li></ol>
<script>var x = 1;</script>
</body></html>`

const frostArticle = `<html><head><title>Frostbite</title></head><body>
<p>Frostbite is skin damage from freezing. It usually affects the fingers and toes.</p>
</body></html>`

func fakeKiwix(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search":
			book := r.URL.Query().Get("books.name")
			w.Header().Set("Content-Type", "application/rss+xml")
			if book == "wikipedia_en_all_maxi_2026-08" {
				fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>Search</title>
<item><title>Hypothermia</title><link>/content/wikipedia_en_all_maxi_2026-08/Hypothermia</link><description>...&lt;b&gt;hypothermia&lt;/b&gt;...</description></item>
<item><title>Frostbite</title><link>/content/wikipedia_en_all_maxi_2026-08/Frostbite</link><description>cold</description></item>
</channel></rss>`)
				return
			}
			fmt.Fprint(w, `<?xml version="1.0"?><rss><channel></channel></rss>`)
		case r.URL.Path == "/suggest":
			if r.URL.Query().Get("content") == "wikipedia_en_all_maxi_2026-08" && strings.Contains(strings.ToLower(r.URL.Query().Get("term")), "hypothermia") {
				fmt.Fprint(w, `[{"value":"Hypothermia","label":"x","kind":"path","path":"Hypothermia"},{"value":"hypothermia ","label":"containing","kind":"pattern"}]`)
				return
			}
			fmt.Fprint(w, `[]`)
		case strings.HasSuffix(r.URL.Path, "/Cold_injury"):
			fmt.Fprint(w, `<html><head><title>Cold injury</title><meta http-equiv="refresh" content="0;URL='./Hypothermia#Treatment'" /></head><body><a href="./Hypothermia#Treatment">Cold injury</a></body></html>`)
		case strings.HasSuffix(r.URL.Path, "/Hypothermia"):
			fmt.Fprint(w, hypoArticle)
		case strings.HasSuffix(r.URL.Path, "/Frostbite"):
			fmt.Fprint(w, frostArticle)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestHTMLToText(t *testing.T) {
	txt := htmlToText(hypoArticle)
	for _, want := range []string{"below 35.0 °C", "## Signs and symptoms", "## Treatment", "warm drinks", "Specialty | Critical care medicine"} {
		if !strings.Contains(txt, want) {
			t.Errorf("missing %q in:\n%s", want, txt)
		}
	}
	for _, bad := range []string{"citation text", "var x", "[1]", "<"} {
		if strings.Contains(txt, bad) {
			t.Errorf("unexpected %q in:\n%s", bad, txt)
		}
	}
}

func TestRetrieve(t *testing.T) {
	srv := fakeKiwix(t)
	defer srv.Close()
	k := &Kiwix{Base: srv.URL, Client: srv.Client()}
	zims := []LocalZim{
		{Book: Book{Prefix: "wikipedia_en_all_maxi", Label: "Wikipedia", Weight: 1, PerQ: 5, MaxDocs: 5}, File: "wikipedia_en_all_maxi_2026-08.zim"},
		{Book: Book{Prefix: "wikivoyage_en_all_maxi", Label: "Wikivoyage", Weight: 0.6}, File: "wikivoyage_en_all_maxi_2026-09.zim"},
	}
	r, err := Retrieve(context.Background(), k, zims, "How is hypothermia treated?", []string{"hypothermia treatment"}, 5000, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Sources) == 0 || r.Sources[0].Title != "Hypothermia" {
		t.Fatalf("expected Hypothermia first, got %+v", r.Sources)
	}
	if !strings.Contains(r.Context, "[1] Hypothermia (Wikipedia)") || !strings.Contains(r.Context, "warm drinks") {
		t.Fatalf("context missing treatment passage:\n%s", r.Context)
	}
}

func TestThinkFilter(t *testing.T) {
	f := &thinkFilter{}
	var out strings.Builder
	for _, tok := range []string{"<thi", "nk>planning", " stuff</th", "ink>\n\nThe ", "answer <b> is 4."} {
		out.WriteString(f.Feed(tok))
	}
	out.WriteString(f.buf)
	if got := out.String(); got != "The answer <b> is 4." {
		t.Fatalf("got %q", got)
	}
}

func TestAnswerFlow(t *testing.T) {
	kiwix := fakeKiwix(t)
	defer kiwix.Close()
	var sawContext bool
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatReq
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &req)
		if !req.Stream {
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "1. Hypothermia\n2. hypothermia treatment"}}}})
			return
		}
		if strings.Contains(req.Messages[0].Content, "warm drinks") {
			sawContext = true
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, tok := range []string{"Warm ", "drinks and dry clothing [1]."} {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": tok}}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer llm.Close()

	a := &App{Root: t.TempDir(), kiwixOK: true, llmOK: true, Model: &Model{ID: "small", ContextChars: 6000}}
	a.Zims = []LocalZim{{Book: Book{Prefix: "wikipedia_en_all_maxi", Label: "Wikipedia", Weight: 1, PerQ: 5, MaxDocs: 5}, File: "wikipedia_en_all_maxi_2026-08.zim"}}
	fmt.Sscanf(strings.TrimPrefix(kiwix.URL, "http://127.0.0.1:"), "%d", &a.KiwixPort)
	fmt.Sscanf(strings.TrimPrefix(llm.URL, "http://127.0.0.1:"), "%d", &a.LLMPort)

	var events []event
	a.answer(context.Background(), askReq{Question: "How do you treat hypothermia?"}, func(e event) { events = append(events, e) })

	var text string
	var gotSources bool
	for _, e := range events {
		switch e.Type {
		case "token":
			text += e.Text
		case "sources":
			gotSources = len(e.Sources) > 0
		case "error":
			t.Fatalf("error event: %s", e.Text)
		}
	}
	if !gotSources || !sawContext || text != "Warm drinks and dry clothing [1]." {
		t.Fatalf("sources=%v context=%v text=%q", gotSources, sawContext, text)
	}
}

func TestLocalZimsPicksNewest(t *testing.T) {
	t.Setenv("OFFLINE_WIKI_RUNTIME", t.TempDir())
	root := t.TempDir()
	if err := cmdInit(root); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"wikipedia_en_all_maxi_2026-02.zim", "wikipedia_en_all_maxi_2026-08.zim", "wikipedia_en_all_maxi_2026-09.zim.dl.part", "ifixit_en_all_2025-12.zim"} {
		if err := writeBytes(root+"/library/"+f, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	z := localZims(root, defaultConfig())
	if len(z) != 2 || z[0].File != "wikipedia_en_all_maxi_2026-08.zim" || z[1].Book.Label != "iFixit" {
		t.Fatalf("got %+v", z)
	}
}

func TestSuggestFindsTitle(t *testing.T) {
	srv := fakeKiwix(t)
	defer srv.Close()
	k := &Kiwix{Base: srv.URL, Client: srv.Client()}
	hits, err := k.Suggest(context.Background(), "wikipedia_en_all_maxi_2026-08", "Hypothermia", 3)
	if err != nil || len(hits) != 1 || hits[0].Link != "/content/wikipedia_en_all_maxi_2026-08/Hypothermia" {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
}

func TestAnswerWaitsForLoadingModel(t *testing.T) {
	kiwix := fakeKiwix(t)
	defer kiwix.Close()
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatReq
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &req)
		if !req.Stream {
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "Hypothermia"}}}})
			return
		}
		b, _ = json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": "ok [1]"}}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
	}))
	defer llm.Close()
	a := &App{Root: t.TempDir(), kiwixOK: true, Model: &Model{ID: "large", ContextChars: 6000}}
	a.Zims = []LocalZim{{Book: Book{Prefix: "wikipedia_en_all_maxi", Label: "Wikipedia", Weight: 1, PerQ: 5, MaxDocs: 5}, File: "wikipedia_en_all_maxi_2026-08.zim"}}
	fmt.Sscanf(strings.TrimPrefix(kiwix.URL, "http://127.0.0.1:"), "%d", &a.KiwixPort)
	fmt.Sscanf(strings.TrimPrefix(llm.URL, "http://127.0.0.1:"), "%d", &a.LLMPort)
	go func() { time.Sleep(1200 * time.Millisecond); a.setLLM(true, "") }()

	var text string
	var waited bool
	a.answer(context.Background(), askReq{Question: "hypothermia"}, func(e event) {
		if e.Type == "status" && strings.Contains(e.Text, "Waiting for the language model") {
			waited = true
		}
		if e.Type == "token" {
			text += e.Text
		}
	})
	if !waited || text != "ok [1]" {
		t.Fatalf("waited=%v text=%q", waited, text)
	}
}

func TestRemoveOldZimsNow(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"wikipedia_en_all_maxi_2026-02.zim", "wikipedia_en_all_maxi_2026-08.zim", "ifixit_en_all_2025-12.zim"} {
		if err := writeBytes(filepath.Join(root, "library", f), []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	removeOldZimsNow(root, defaultConfig(), &buf)
	if _, err := os.Stat(filepath.Join(root, "library", "wikipedia_en_all_maxi_2026-02.zim")); !os.IsNotExist(err) {
		t.Fatal("superseded file was kept")
	}
	for _, f := range []string{"wikipedia_en_all_maxi_2026-08.zim", "ifixit_en_all_2025-12.zim"} {
		if _, err := os.Stat(filepath.Join(root, "library", f)); err != nil {
			t.Fatalf("%s was removed", f)
		}
	}
}

func TestRuntimeFilesStayOffDrive(t *testing.T) {
	rt := t.TempDir()
	t.Setenv("OFFLINE_WIKI_RUNTIME", rt)
	fmt.Fprintln(logFile("llm.log"), "hello")
	writePid()
	defer removePid()
	unlock, err := lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for _, f := range []string{"llm.log", "run.pid", "update.lock"} {
		if _, err := os.Stat(filepath.Join(rt, f)); err != nil {
			t.Errorf("%s not in runtime folder", f)
		}
	}
	if runningPid() != 0 || lockHolder() != 0 {
		t.Error("own process should not count as another instance")
	}
}

func TestLineWriterMergesProgress(t *testing.T) {
	u := &updater{}
	w := &lineWriter{u: u}
	fmt.Fprint(w, "Wikipedia: downloading x\n  Wikipedia: 1.0% of 128 GB, 40 MB/s\n")
	fmt.Fprint(w, "  Wikipedia: 2.0% of 128 GB, 40 MB/s\nDone\n")
	got := u.state().Lines
	if len(got) != 3 || got[1] != "Wikipedia: 2.0% of 128 GB, 40 MB/s" || got[2] != "Done" {
		t.Fatalf("got %q", got)
	}
}

// A question in flight when Offline Wiki restarts (an update switching the
// drive) should wait for the restart and try again, not fail.
func TestAnswerRetriesAfterRestart(t *testing.T) {
	kiwix := fakeKiwix(t)
	defer kiwix.Close()
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatReq
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &req)
		if !req.Stream {
			time.Sleep(300 * time.Millisecond) // the restart begins meanwhile
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "Hypothermia"}}}})
			return
		}
		b, _ = json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": "ok [1]"}}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
	}))
	defer llm.Close()

	a := &App{Root: t.TempDir(), kiwixOK: true, llmOK: true, Model: &Model{ID: "large", ContextChars: 6000}}
	a.Zims = []LocalZim{{Book: Book{Prefix: "wikipedia_en_all_maxi", Label: "Wikipedia", Weight: 1, PerQ: 5, MaxDocs: 5}, File: "wikipedia_en_all_maxi_2026-08.zim"}}
	a.KiwixPort = 1 // nothing listening: the library is down during the restart
	var realPort int
	fmt.Sscanf(strings.TrimPrefix(kiwix.URL, "http://127.0.0.1:"), "%d", &realPort)
	fmt.Sscanf(strings.TrimPrefix(llm.URL, "http://127.0.0.1:"), "%d", &a.LLMPort)
	go func() {
		time.Sleep(100 * time.Millisecond)
		a.setKiwix(false, "") // restart begins
		time.Sleep(1200 * time.Millisecond)
		a.mu.Lock()
		a.KiwixPort, a.kiwixOK = realPort, true // restart done
		a.mu.Unlock()
	}()

	var text string
	var errs []string
	a.answer(context.Background(), askReq{Question: "hypothermia"}, func(e event) {
		switch e.Type {
		case "token":
			text += e.Text
		case "error":
			errs = append(errs, e.Text)
		}
	})
	if len(errs) > 0 || text != "ok [1]" {
		t.Fatalf("errors=%q text=%q", errs, text)
	}
}

func TestLetterSuffixedZimNames(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"nhs.uk_en_medicines_2026-09.zim", "nhs.uk_en_medicines_2026-09b.zim"} {
		if err := writeBytes(filepath.Join(root, "library", f), []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{Books: []Book{{Prefix: "nhs.uk_en_medicines", Label: "NHS medicines", Enabled: true}}}
	z := localZims(root, cfg)
	if len(z) != 1 || z[0].File != "nhs.uk_en_medicines_2026-09b.zim" || z[0].Stem() != "nhs.uk_en_medicines_2026-09b" {
		t.Fatalf("got %+v", z)
	}
	if old := oldZims(root, cfg.Books[0], z[0].File); len(old) != 1 || old[0] != "nhs.uk_en_medicines_2026-09.zim" {
		t.Fatalf("old = %v", old)
	}
}

func TestOldConfigGainsNewLibraries(t *testing.T) {
	root := t.TempDir()
	old := `{"books":[{"prefix":"wikipedia_en_all_maxi","dir":"wikipedia","label":"Wikipedia","weight":1,"per_query_results":6,"max_docs":8,"enabled":true},
	{"prefix":"gutenberg_en_all","dir":"gutenberg","label":"Project Gutenberg","enabled":false}],
	"models":[{"id":"small","file":"x.gguf"}]}`
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Book{}
	for _, b := range cfg.Books {
		got[b.Prefix] = b
	}
	if !got["medlineplus.gov_en_all"].Enabled || !got["diy.stackexchange.com_en_all"].Enabled {
		t.Error("new libraries were not added")
	}
	if got["gutenberg_en_all"].Enabled {
		t.Error("a library switched off in config.json was switched back on")
	}
	if len(cfg.Books) != len(defaultConfig().Books) {
		t.Errorf("duplicate or missing books: %d vs %d", len(cfg.Books), len(defaultConfig().Books))
	}
	if len(cfg.Models) != len(defaultConfig().Models) || cfg.Models[0].File != "x.gguf" || cfg.Models[0].Label != "Fast (small model)" || cfg.Models[0].MinRAMGB != 0 {
		t.Errorf("models: %+v", cfg.Models)
	}
	if cfg.Port != 8470 || cfg.Reranker.File == "" || cfg.UpdateIntervalDays != 7 {
		t.Errorf("defaults not applied: %+v", cfg)
	}
}

func TestLocalLibraryIsLoadedButNotDownloaded(t *testing.T) {
	t.Setenv("OFFLINE_WIKI_RUNTIME", t.TempDir())
	root := t.TempDir()
	if err := writeBytes(filepath.Join(root, "library", "nhs.uk_en_health-a-z_2026-10.zim"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Books: []Book{{Prefix: "nhs.uk_en_health-a-z", Label: "NHS Health A–Z", Enabled: true, Local: true}}}
	if z := localZims(root, cfg); len(z) != 1 {
		t.Fatalf("local library not loaded: %+v", z)
	}
	// A dry run must not try to fetch it from the (unreachable) mirror.
	cfg.ZimMirror = "http://127.0.0.1:1/"
	var buf bytes.Buffer
	uout = &buf
	defer func() { uout = os.Stdout }()
	res, err := runUpdate(context.Background(), root, cfg, updateOpts{DryRun: true, Only: map[string]bool{"books": true}})
	if err != nil || !res.OK || len(res.Plan) != 0 {
		t.Fatalf("res=%+v err=%v out=%s", res, err, buf.String())
	}
}

func TestRerankerDropsOffTopicPassages(t *testing.T) {
	t.Setenv("OFFLINE_WIKI_RUNTIME", t.TempDir())
	srv := fakeKiwix(t)
	defer srv.Close()
	// A fake reranker: hypothermia passages relevant, everything else not.
	var gotQuery string
	rrs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		gotQuery = req.Query
		type res struct {
			Index int     `json:"index"`
			Score float64 `json:"relevance_score"`
		}
		var out []res
		for i, d := range req.Documents {
			s := -11.0
			if strings.Contains(strings.ToLower(d), "hypothermia") {
				s = -1
			}
			out = append(out, res{i, s})
		}
		json.NewEncoder(w).Encode(map[string]any{"results": out})
	}))
	defer rrs.Close()
	k := &Kiwix{Base: srv.URL, Client: srv.Client()}
	rr := &Reranker{Base: rrs.URL, Client: rrs.Client()}
	zims := []LocalZim{{Book: Book{Prefix: "wikipedia_en_all_maxi", Label: "Wikipedia", Weight: 1, PerQ: 5, MaxDocs: 5}, File: "wikipedia_en_all_maxi_2026-08.zim"}}
	r, err := Retrieve(context.Background(), k, zims, "How is it treated?", []string{"hypothermia treatment"}, 5000, nil, rr)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range r.Sources {
		if s.Title == "Frostbite" {
			t.Fatalf("off-topic article kept: %+v", r.Sources)
		}
	}
	if len(r.Sources) == 0 || !strings.Contains(r.Context, "warm drinks") {
		t.Fatalf("relevant passage lost: %+v\n%s", r.Sources, r.Context)
	}
	if gotQuery != "How is it treated? (hypothermia treatment)" {
		t.Fatalf("reranker query %q should include the search terms", gotQuery)
	}
}

func streamServer(t *testing.T, deltas []map[string]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, d := range deltas {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": d}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func TestThinkingStreamHidesReasoning(t *testing.T) {
	cases := map[string][]map[string]string{
		"inline":   {{"content": "Let me think"}, {"content": " about legs.</thi"}, {"content": "nk>\n\nSplint it [1]."}},
		"separate": {{"reasoning_content": "Let me think"}, {"reasoning_content": " about legs."}, {"content": "Splint it [1]."}},
		"none":     {{"content": "Splint it [1]."}},
	}
	for name, deltas := range cases {
		srv := streamServer(t, deltas)
		l := &LLM{Base: srv.URL, Client: srv.Client()}
		var out string
		thought := false
		err := l.Stream(context.Background(), nil, 100, true, func() { thought = true }, func(s string) { out += s })
		srv.Close()
		if err != nil || out != "Splint it [1]." {
			t.Errorf("%s: out=%q err=%v", name, out, err)
		}
		if name != "none" && !thought {
			t.Errorf("%s: thinking not reported", name)
		}
	}
}

func TestFetchFollowsKiwixRedirects(t *testing.T) {
	srv := fakeKiwix(t)
	defer srv.Close()
	k := &Kiwix{Base: srv.URL, Client: srv.Client()}
	body, final, err := k.Fetch(context.Background(), "/content/wikipedia_en_all_maxi_2026-08/Cold_injury", 1<<20)
	if err != nil || final != "/content/wikipedia_en_all_maxi_2026-08/Hypothermia" || !strings.Contains(body, "warm drinks") {
		t.Fatalf("final=%q err=%v body=%.80q", final, err, body)
	}
}

func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		"Shingles\n - NHS": "Shingles",
		"The Highway Code - Rules for pedestrians (1 to 35) - Guidance - GOV.UK": "The Highway Code - Rules for pedestrians (1 to 35)",
		"  Human   leg ": "Human leg",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// With a thinking main model, search terms come from the small query model.
func TestQueriesUseSmallModelBesideThinkingModel(t *testing.T) {
	kiwix := fakeKiwix(t)
	defer kiwix.Close()
	var mainQueryCalls, smallCalls int
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatReq
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &req)
		if !req.Stream {
			mainQueryCalls++
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "Hypothermia"}}}})
			return
		}
		b, _ = json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": "ok [1]"}}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
	}))
	defer main.Close()
	small := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		smallCalls++
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "Hypothermia"}}}})
	}))
	defer small.Close()
	a := &App{Root: t.TempDir(), kiwixOK: true, llmOK: true, qOK: true, Model: &Model{ID: "thinking", Thinking: true, ContextChars: 6000}}
	a.Zims = []LocalZim{{Book: Book{Prefix: "wikipedia_en_all_maxi", Label: "Wikipedia", Weight: 1, PerQ: 5, MaxDocs: 5}, File: "wikipedia_en_all_maxi_2026-08.zim"}}
	fmt.Sscanf(strings.TrimPrefix(kiwix.URL, "http://127.0.0.1:"), "%d", &a.KiwixPort)
	fmt.Sscanf(strings.TrimPrefix(main.URL, "http://127.0.0.1:"), "%d", &a.LLMPort)
	fmt.Sscanf(strings.TrimPrefix(small.URL, "http://127.0.0.1:"), "%d", &a.QueryPort)
	var text string
	a.answer(context.Background(), askReq{Question: "hypothermia"}, func(e event) {
		if e.Type == "token" {
			text += e.Text
		}
	})
	if smallCalls != 1 || mainQueryCalls != 0 || text != "ok [1]" {
		t.Fatalf("small=%d mainQuery=%d text=%q", smallCalls, mainQueryCalls, text)
	}
}

func TestChooseModelPicksLargestThatFits(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Models: []Model{
		{ID: "small", File: "s.gguf", MinRAMGB: 0},
		{ID: "medium", File: "m.gguf", MinRAMGB: 16},
		{ID: "thinking", File: "t.gguf", MinRAMGB: 30, Thinking: true},
		{ID: "huge", File: "h.gguf", MinRAMGB: 100000},
	}}
	for _, f := range []string{"s.gguf", "m.gguf", "t.gguf", "h.gguf"} {
		writeBytes(filepath.Join(root, "models", f), []byte("x"))
	}
	if m := chooseModel(root, cfg, ""); m == nil || m.ID != "medium" {
		t.Fatalf("default: got %+v, want medium (largest non-thinking that fits)", m)
	}
	if m := chooseModel(root, cfg, "thinking"); m == nil || m.ID != "thinking" {
		t.Fatalf("named: got %+v", m)
	}
	if m := chooseModel(root, cfg, "missing"); m != nil {
		t.Fatalf("missing model should give nil, got %+v", m)
	}
}

func TestNoThinkRequestsSwitchThinkingOff(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "ok"}}}})
	}))
	defer srv.Close()
	l := &LLM{Base: srv.URL, Client: srv.Client(), NoThink: true}
	if _, err := l.Complete(context.Background(), []ChatMsg{{Role: "user", Content: "hi"}}, 10); err != nil {
		t.Fatal(err)
	}
	kw, _ := got["chat_template_kwargs"].(map[string]any)
	if kw == nil || kw["enable_thinking"] != false {
		t.Fatalf("request: %v", got)
	}
}
