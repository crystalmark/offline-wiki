package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

type ChatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type LLM struct {
	Base    string // e.g. http://127.0.0.1:8472
	Client  *http.Client
	NoThink bool // ask a hybrid model to answer without thinking first
}

type chatReq struct {
	Model       string         `json:"model"`
	Messages    []ChatMsg      `json:"messages"`
	Temperature float64        `json:"temperature"`
	MaxTokens   int            `json:"max_tokens"`
	Stream      bool           `json:"stream"`
	TemplateKw  map[string]any `json:"chat_template_kwargs,omitempty"`
}

func (l *LLM) post(ctx context.Context, r chatReq) (*http.Response, error) {
	if l.NoThink {
		r.TemplateKw = map[string]any{"enable_thinking": false}
	}
	b, _ := json.Marshal(r)
	req, _ := http.NewRequestWithContext(ctx, "POST", l.Base+"/v1/chat/completions", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
		resp.Body.Close()
		return nil, fmt.Errorf("model server: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp, nil
}

// Complete returns a whole (non-streamed) reply.
func (l *LLM) Complete(ctx context.Context, msgs []ChatMsg, maxTokens int) (string, error) {
	resp, err := l.post(ctx, chatReq{Model: "local", Messages: msgs, Temperature: 0, MaxTokens: maxTokens})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("model server returned no choices")
	}
	return stripThink(out.Choices[0].Message.Content), nil
}

// Stream sends reply text to emit as it is generated. For thinking models
// the reasoning is hidden (onThink is called while it happens), whether the
// server reports it separately or inline before a closing </think>.
func (l *LLM) Stream(ctx context.Context, msgs []ChatMsg, maxTokens int, thinking bool, onThink func(), emit func(string)) error {
	resp, err := l.post(ctx, chatReq{Model: "local", Messages: msgs, Temperature: 0.3, MaxTokens: maxTokens, Stream: true})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	tf := &thinkFilter{}
	held := thinking // until we know how the server reports reasoning
	var hold strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					Reasoning string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil || len(ev.Choices) == 0 {
			continue
		}
		d := ev.Choices[0].Delta
		if d.Reasoning != "" {
			if onThink != nil {
				onThink()
			}
			if held { // reasoning comes separately: content is the answer
				held = false
				if t := tf.Feed(hold.String()); t != "" {
					emit(t)
				}
				hold.Reset()
			}
		}
		if held {
			// Reasoning may be inline, ending with </think> (the opening tag
			// is part of the prompt): hold text back until it ends.
			hold.WriteString(d.Content)
			if onThink != nil && hold.Len() > 0 {
				onThink()
			}
			if i := strings.Index(hold.String(), "</think>"); i >= 0 {
				rest := hold.String()[i+len("</think>"):]
				held = false
				hold.Reset()
				if t := tf.Feed(rest); t != "" {
					emit(t)
				}
			}
			continue
		}
		if t := tf.Feed(d.Content); t != "" {
			emit(t)
		}
	}
	if held && hold.Len() > 0 { // never saw a </think>: it was all answer
		if t := tf.Feed(hold.String()); t != "" {
			emit(t)
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

var reThink = regexp.MustCompile(`(?s)<think>.*?</think>`)

func stripThink(s string) string {
	s = reThink.ReplaceAllString(s, "")
	if i := strings.Index(s, "</think>"); i >= 0 {
		s = s[i+len("</think>"):]
	}
	return strings.TrimSpace(s)
}

// thinkFilter hides <think>…</think> sections from a token stream.
type thinkFilter struct {
	buf     string
	inThink bool
	started bool
}

func (f *thinkFilter) Feed(s string) string {
	f.buf += s
	var out strings.Builder
	for {
		if f.inThink {
			i := strings.Index(f.buf, "</think>")
			if i < 0 {
				// keep only a possible partial closing tag
				if len(f.buf) > 8 {
					f.buf = f.buf[len(f.buf)-8:]
				}
				return out.String()
			}
			f.buf = f.buf[i+len("</think>"):]
			f.inThink = false
			continue
		}
		i := strings.Index(f.buf, "<think>")
		if i >= 0 {
			out.WriteString(f.buf[:i])
			f.buf = f.buf[i+len("<think>"):]
			f.inThink = true
			continue
		}
		// hold back a possible partial "<think>"
		keep := 0
		for k := 1; k < len("<think>") && k <= len(f.buf); k++ {
			if strings.HasSuffix(f.buf, "<think>"[:k]) {
				keep = k
			}
		}
		emit := f.buf[:len(f.buf)-keep]
		f.buf = f.buf[len(f.buf)-keep:]
		if !f.started {
			emit = strings.TrimLeft(emit, "\n ")
			if emit != "" {
				f.started = true
			}
		}
		out.WriteString(emit)
		return out.String()
	}
}

const queryPrompt = `You write search queries for an offline encyclopedia search engine (keyword based, like a library catalogue).
Given the conversation and the latest question, reply with 2 to 4 short keyword searches, one per line.
Rules: each search is 1 to 4 words. No numbering, no quotes, no explanations. Broad topic words find more than long exact phrases. Use the specific names, places, things and technical terms the answer would be filed under. Resolve words like "it" or "that" from the conversation. Prefer the encyclopedia article title the answer is most likely in as the first line. Include both the everyday wording and the technical term where they differ (for example "broken leg" and "leg fracture", "heart attack" and "myocardial infarction").`

// MakeQueries asks the model for search queries; falls back to the question.
func MakeQueries(ctx context.Context, l *LLM, history []ChatMsg, question string, thinking bool) []string {
	fallback := []string{question}
	if l == nil {
		return fallback
	}
	var convo strings.Builder
	for _, m := range lastN(history, 4) {
		content := m.Content
		if len(content) > 600 {
			content = content[:600] + "…"
		}
		fmt.Fprintf(&convo, "%s: %s\n", strings.ToUpper(m.Role[:1])+m.Role[1:], content)
	}
	user := "Latest question: " + question
	if convo.Len() > 0 {
		user = "Conversation so far:\n" + convo.String() + "\n" + user
	}
	budget := 80
	if thinking {
		budget = 2500 // a thinking model reasons first
	}
	out, err := l.Complete(ctx, []ChatMsg{{Role: "system", Content: queryPrompt}, {Role: "user", Content: user}}, budget)
	if err != nil {
		return fallback
	}
	var qs []string
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "-*•0123456789.) "))
		line = strings.Trim(line, "\"'`")
		if line == "" || len(line) > 120 || seen[strings.ToLower(line)] {
			continue
		}
		seen[strings.ToLower(line)] = true
		qs = append(qs, line)
		if len(qs) == 4 {
			break
		}
	}
	if len(qs) == 0 {
		return fallback
	}
	// Also search the question itself: full-text search copes with it, and it
	// guards against a poor rewrite.
	if !seen[strings.ToLower(question)] {
		qs = append(qs, question)
	}
	return qs
}

const answerPrompt = `You are an offline reference assistant. The internet is not available; your only knowledge source is the numbered extracts below, taken from offline copies of Wikipedia and other reference works.

Answer the user's question using the extracts. Cite them inline with their numbers, like [1] or [2][3], after the statements they support.
- Every factual statement must carry a citation. If the extracts do not contain the answer, or only part of it, say so plainly, say what they do cover, and suggest a better question or article to look up.
- Only if it would really help, you may add a short final section headed "Not from the library (unverified):" with general knowledge. Never mix uncited knowledge into the cited answer.
- For medical, safety, survival, electrical or mechanical questions, be precise, keep quantities and warnings from the extracts, and say when a professional or emergency service is needed if they can be reached.
- The search may also return extracts that share words with the question but are about something else (a broken-down car is not a broken leg). Ignore those completely; never mention them.
- Some extracts are question-and-answer pages (marked "Q&A"). The details in those questions describe someone else's situation, not the user's: use the answers, but don't assume the user's circumstances match.
- Be direct and concise. Use short paragraphs or a list when steps are involved. British English.

Extracts:
%s`

func BuildAnswerMessages(history []ChatMsg, question, context string) []ChatMsg {
	msgs := []ChatMsg{{Role: "system", Content: fmt.Sprintf(answerPrompt, context)}}
	for _, m := range lastN(history, 6) {
		c := m.Content
		if len(c) > 2000 {
			c = c[:2000] + "…"
		}
		msgs = append(msgs, ChatMsg{Role: m.Role, Content: c})
	}
	msgs = append(msgs, ChatMsg{Role: "user", Content: question})
	return msgs
}

func lastN(h []ChatMsg, n int) []ChatMsg {
	var clean []ChatMsg
	for _, m := range h {
		if (m.Role == "user" || m.Role == "assistant") && strings.TrimSpace(m.Content) != "" {
			clean = append(clean, m)
		}
	}
	if len(clean) > n {
		clean = clean[len(clean)-n:]
	}
	return clean
}
