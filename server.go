package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed ui/index.html
var indexHTML []byte

type askReq struct {
	Question string    `json:"question"`
	History  []ChatMsg `json:"history"`
}

type event struct {
	Type    string   `json:"type"`
	Text    string   `json:"text,omitempty"`
	Sources []Source `json:"sources,omitempty"`
}

func isLocal(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// localPost wraps actions only the computer running Offline Wiki may take
// (not phones using it over the network with --lan).
func localPost(f func(w http.ResponseWriter)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST only", 405)
			return
		}
		if !isLocal(r) {
			http.Error(w, "Only the computer the drive is plugged into can do this.", 403)
			return
		}
		f(w)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func serveUI(a *App, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(indexHTML)
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		us := a.upd.state()
		a.mu.Lock()
		root := a.Root
		type bookInfo struct {
			Label string `json:"label"`
			Date  string `json:"date"`
			GB    string `json:"gb"`
			Home  string `json:"home"`
		}
		var books []bookInfo
		for _, z := range a.Zims {
			books = append(books, bookInfo{z.Book.Label, z.Date, fmt.Sprintf("%.1f", float64(z.Size)/1e9), "/content/" + z.Stem()})
		}
		model, modelID := "", ""
		if a.Model != nil {
			model = a.Model.ID + " · " + strings.TrimSuffix(a.Model.File, ".gguf")
			modelID = a.Model.ID
		}
		type modelInfo struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		}
		var models []modelInfo
		for _, m := range availableModels(root, a.Cfg) {
			l := m.Label
			if l == "" {
				l = m.ID
			}
			models = append(models, modelInfo{m.ID, l})
		}
		st := map[string]any{
			"kiwix_ready": a.kiwixOK, "kiwix_error": a.kiwixErr,
			"llm_ready": a.llmOK, "llm_error": a.llmErr, "reranker_ready": a.rrOK, "query_model_ready": a.qOK,
			"model": model, "model_id": modelID, "models": models, "kiwix_port": a.KiwixPort, "books": books,
			"last_update":     lastUpdateSummary(root),
			"drive_read_only": isReadOnly(root),
			"drive_note":      a.driveNote,
			"updating":        us.Phase == "running",
			"local":           isLocal(r),
			"log_dir":         runtimeDir(),
		}
		a.mu.Unlock()
		writeJSON(w, st)
	})
	mux.HandleFunc("/api/ask", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST only", 405)
			return
		}
		var req askReq
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Question) == "" {
			http.Error(w, "bad request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-store")
		fl, _ := w.(http.Flusher)
		var wmu sync.Mutex
		send := func(e event) {
			wmu.Lock()
			defer wmu.Unlock()
			b, _ := json.Marshal(e)
			w.Write(append(b, '\n'))
			if fl != nil {
				fl.Flush()
			}
		}
		a.answer(r.Context(), req, send)
	})
	mux.HandleFunc("/api/update", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, a.upd.state()) })
	mux.HandleFunc("/api/update/check", localPost(func(w http.ResponseWriter) { a.updateCheck(); w.Write([]byte("ok")) }))
	mux.HandleFunc("/api/update/start", localPost(func(w http.ResponseWriter) { a.updateStart(); w.Write([]byte("ok")) }))
	mux.HandleFunc("/api/update/cancel", localPost(func(w http.ResponseWriter) { a.updateCancel(); w.Write([]byte("ok")) }))
	mux.HandleFunc("/api/model", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST only", 405)
			return
		}
		if !isLocal(r) {
			http.Error(w, "Only the computer the drive is plugged into can do this.", 403)
			return
		}
		if err := a.switchModel(r.URL.Query().Get("id")); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/api/shutdown", localPost(func(w http.ResponseWriter) {
		w.Write([]byte("ok"))
		go func() {
			time.Sleep(300 * time.Millisecond)
			a.Stop()
			os.Exit(0)
		}()
	}))
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	return srv.ListenAndServe()
}

// waitReady waits (up to five minutes) for services that are still starting,
// e.g. a large model being read from a USB drive, or a restart for an update.
func (a *App) waitReady(ctx context.Context, send func(event)) {
	for waited := 0; waited < 600 && ctx.Err() == nil; waited++ {
		a.mu.Lock()
		kiwixLoading := !a.kiwixOK && a.kiwixErr == ""
		llmLoading := a.Model != nil && !a.llmOK && a.llmErr == ""
		a.mu.Unlock()
		if !kiwixLoading && !llmLoading {
			return
		}
		if waited%10 == 0 {
			what := "the language model"
			if kiwixLoading {
				what = "the library"
			}
			if a.upd.state().Phase == "running" {
				what += " (restarting for the update)"
			}
			send(event{Type: "status", Text: fmt.Sprintf("Waiting for %s… (%ds)", what, waited/2)})
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (a *App) servicesUp() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.kiwixOK
}

func (a *App) answer(ctx context.Context, req askReq, send func(event)) {
	for attempt := 0; ; attempt++ {
		a.waitReady(ctx, send)
		err := a.answerOnce(ctx, req, send)
		// If Offline Wiki restarted underneath us (an update switching the
		// drive), wait for it and try again once.
		if err == nil || attempt > 0 || a.servicesUp() || ctx.Err() != nil {
			if err != nil {
				send(event{Type: "error", Text: err.Error()})
			}
			return
		}
		send(event{Type: "status", Text: "Offline Wiki is restarting - trying again shortly…"})
		time.Sleep(time.Second)
	}
}

// answerOnce answers one question; it returns an error only for failures
// worth retrying after a restart.
func (a *App) answerOnce(ctx context.Context, req askReq, send func(event)) error {
	a.mu.Lock()
	kiwixOK, llmOK := a.kiwixOK, a.llmOK
	kiwixErr, llmErr := a.kiwixErr, a.llmErr
	zims := append([]LocalZim(nil), a.Zims...)
	model := a.Model
	kport := a.KiwixPort
	qOK, qport := a.qOK, a.QueryPort
	var rr *Reranker
	if a.rrOK {
		rr = &Reranker{Base: fmt.Sprintf("http://127.0.0.1:%d", a.RerankPort), Client: &http.Client{Timeout: 2 * time.Minute}}
	}
	a.mu.Unlock()
	if !kiwixOK {
		return fmt.Errorf("The library isn't running. %s", kiwixErr)
	}
	k := &Kiwix{Base: fmt.Sprintf("http://127.0.0.1:%d", kport), Client: &http.Client{Timeout: 60 * time.Second}}
	var llm *LLM
	if llmOK {
		llm = &LLM{Base: fmt.Sprintf("http://127.0.0.1:%d", a.LLMPort), Client: &http.Client{}, NoThink: model != nil && model.NoThink}
	}

	q := strings.TrimSpace(req.Question)
	send(event{Type: "status", Text: "Working out what to look up…"})
	thinking := model != nil && model.Thinking
	qllm, qThinking := llm, thinking
	if qOK && llm != nil {
		qllm, qThinking = &LLM{Base: fmt.Sprintf("http://127.0.0.1:%d", qport), Client: &http.Client{}}, false
	}
	queries := MakeQueries(ctx, qllm, req.History, q, qThinking)
	send(event{Type: "status", Text: "Searching: " + strings.Join(queries, " · ")})

	budget := 12000
	if model != nil && model.ContextChars > 0 {
		budget = model.ContextChars
	}
	status := func(s string) { send(event{Type: "status", Text: s}) }
	ret, err := Retrieve(ctx, k, zims, q, queries, budget, status, rr)
	if err != nil {
		return fmt.Errorf("Search failed: %v", err)
	}
	if len(ret.Sources) == 0 {
		// Nothing matched: widen the search to the question's key words.
		if broad := broadQueries(q); len(broad) > 0 {
			send(event{Type: "status", Text: "Nothing yet - widening the search: " + strings.Join(broad, " · ")})
			ret, err = Retrieve(ctx, k, zims, q, broad, budget, status, rr)
			if err != nil {
				return fmt.Errorf("Search failed: %v", err)
			}
		}
	}
	if len(ret.Sources) == 0 {
		send(event{Type: "token", Text: "I couldn't find anything in the library for that. Try different words, or a more specific name or term."})
		send(event{Type: "done"})
		return nil
	}
	send(event{Type: "sources", Sources: ret.Sources})

	if llm == nil {
		why := "No language model is running"
		if llmErr != "" {
			why = "The language model isn't available (" + llmErr + ")"
		}
		send(event{Type: "token", Text: why + ", so here are the most relevant passages from the library:\n\n" + excerpt(ret.Context, 4000)})
		send(event{Type: "done"})
		return nil
	}
	send(event{Type: "status", Text: "Writing the answer…"})
	msgs := BuildAnswerMessages(req.History, q, ret.Context)
	started := false
	maxTokens := 1500
	if thinking {
		maxTokens = 8000 // room to reason before answering
	}
	thinkNoted := false
	onThink := func() {
		if !thinkNoted {
			thinkNoted = true
			send(event{Type: "status", Text: "Thinking it through before answering…"})
		}
	}
	err = llm.Stream(ctx, msgs, maxTokens, thinking, onThink, func(t string) { started = true; send(event{Type: "token", Text: t}) })
	if err != nil {
		if started {
			send(event{Type: "error", Text: "The model stopped with an error: " + err.Error()})
			return nil
		}
		return fmt.Errorf("The model stopped with an error: %v", err)
	}
	send(event{Type: "done"})
	return nil
}

func excerpt(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// broadQueries builds short fallback searches from the question's longest
// key words and adjacent pairs of them.
func broadQueries(q string) []string {
	var words []string
	seen := map[string]bool{}
	for _, w := range reWord.FindAllString(strings.ToLower(q), -1) {
		if stopwords[w] || len(w) < 3 || seen[w] {
			continue
		}
		seen[w] = true
		words = append(words, w)
	}
	var out []string
	for i := 0; i+1 < len(words) && len(out) < 3; i++ {
		out = append(out, words[i]+" "+words[i+1])
	}
	sorted := append([]string(nil), words...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	for _, w := range sorted {
		if len(out) >= 6 {
			break
		}
		out = append(out, w)
	}
	return out
}
