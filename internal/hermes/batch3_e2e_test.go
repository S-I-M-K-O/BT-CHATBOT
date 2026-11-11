package hermes

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Batch 3 end to end through POST /api/chat, in English (BT7274) and German
// (JarvisDE): smart home commands, questions and refusals against a fake
// Home Assistant, a question about an uploaded document answered by a fake
// local model, and a link summary of an httptest page. Fake realtime
// services (Open-Meteo, news) must never be asked by any of these turns.

type b3Lang struct {
	voice, lang          string
	command, wantCommand string
	query, wantQuery     string
	unlock, wantRefusal  string
	docName, docText     string
	docQuestion          string
	docPrompt            string
	linkInstruction      string
	linkPrompt           string
	langMark             string // the fake model answers with this prefix
}

var b3Langs = []b3Lang{
	{
		voice: "BT7274", lang: "en",
		command: "turn on the living room light", wantCommand: "Turned on the living room light.",
		query: "is the kitchen light on?", wantQuery: "Küchenlicht is on.",
		unlock: "unlock the front door", wantRefusal: "For safety reasons I don't control doors, locks, gates or alarm systems.",
		docName: "Lease.txt", docText: englishLease,
		docQuestion:     "what does my lease say about pets?",
		docPrompt:       "Answer the question using only",
		linkInstruction: "summarise this in two sentences",
		linkPrompt:      "Summarise the following article",
		langMark:        "EN:",
	},
	{
		voice: "JarvisDE", lang: "de",
		command: "Mach das Licht im Wohnzimmer an", wantCommand: "Das Licht im Wohnzimmer ist an.",
		query: "Ist das Küchenlicht an?", wantQuery: "Küchenlicht ist an.",
		unlock: "Entriegle die Haustür", wantRefusal: "Türen, Schlösser, Tore und Alarmanlagen steuere ich aus Sicherheitsgründen nicht.",
		docName: "Mietvertrag.txt", docText: leaseText,
		docQuestion:     "Was steht in meinem Mietvertrag über Haustiere?",
		docPrompt:       "Beantworte die Frage ausschließlich",
		linkInstruction: "fasse das in zwei Sätzen zusammen",
		linkPrompt:      "Fasse den folgenden Artikel",
		langMark:        "DE:",
	},
}

// b3Ollama is a fake local model: it records every chat body and answers in
// the language the system prompt asks for, so the test can tell whether the
// prompt picked the right language.
type b3Ollama struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func newB3Ollama(t *testing.T) *b3Ollama {
	f := &b3Ollama{}
	fakeOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/api/tags":
			io.WriteString(w, `{"models":[{"name":"local-test:latest"}]}`)
			return
		case "/api/chat":
		default:
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()
		if body["stream"] != true {
			io.WriteString(w, `{"message":{"role":"assistant","content":"{}"}}`)
			return
		}
		sys := ""
		if msgs, _ := body["messages"].([]any); len(msgs) > 0 {
			if m, _ := msgs[0].(map[string]any); m != nil {
				sys, _ = m["content"].(string)
			}
		}
		reply := "?? no language"
		switch {
		case strings.Contains(sys, "Antworte auf Deutsch") || strings.Contains(sys, "Beantworte die Frage") || strings.Contains(sys, "Fasse den folgenden"):
			reply = "DE: Katzen und kleine Hunde sind erlaubt."
		case strings.Contains(sys, "Answer in English") || strings.Contains(sys, "Answer the question") || strings.Contains(sys, "Summarise the following"):
			reply = "EN: Cats are allowed."
		}
		enc, _ := json.Marshal(map[string]any{"message": map[string]string{"role": "assistant", "content": reply}, "done": false})
		w.Write(append(enc, '\n'))
		io.WriteString(w, `{"message":{"role":"assistant","content":""},"done":true}`+"\n")
	})
	return f
}

func (f *b3Ollama) streamed() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, b := range f.bodies {
		if b["stream"] == true {
			out = append(out, b)
		}
	}
	return out
}

// b3Env: temp data and settings, single-user mode, the given voice, a fake
// HA (connected and warm), a fake local model, fake realtime services, a
// public stand-in page for links, and documents indexed at once.
func b3Env(t *testing.T, voice string) (http.Handler, *fakeHA, *b3Ollama, *fakeRealtime, *httptest.Server) {
	t.Helper()
	docsEnv(t, false)
	haReset()
	t.Cleanup(func() { haBG.Wait(); haReset() })
	t.Setenv("HA_URL", "")
	t.Setenv("HA_TOKEN", "")
	t.Setenv("HERMES_TOKEN", "")
	t.Setenv("HERMES_BLOCK_CIDRS", "")
	peopleCountProvider = nil // single user: no speaker checks

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<!doctype html><html><head><title>Cats in rented flats</title></head><body><article>`+
			`<p>A new court ruling says landlords may no longer forbid cats in rented flats across the board.</p>`+
			`<p>Small pets such as hamsters were already allowed; dogs still need the landlord's consent.</p>`+
			`</article></body></html>`)
	}))
	t.Cleanup(page.Close)
	oldCheck, oldAddrs := linkDialCheck, linkInterfaceAddrs
	pageAddr := strings.TrimPrefix(page.URL, "http://")
	linkDialCheck = func(address string) error {
		if address == pageAddr {
			return nil // stands in for a public web site
		}
		return checkLinkDial(address)
	}
	linkInterfaceAddrs = func() ([]net.Addr, error) { return nil, nil }
	resetLinkIfaceCache()
	t.Cleanup(func() {
		linkDialCheck, linkInterfaceAddrs = oldCheck, oldAddrs
		resetLinkIfaceCache()
	})

	o := newB3Ollama(t)
	fm := newFakeRealtime(t)

	s := loadSettings()
	s.PiperVoice = voice
	s.MaxTokens = 2000
	if err := saveSettings(s); err != nil {
		t.Fatal(err)
	}
	f := newFakeHA(t)
	haConnect(t, f)
	haWarm(t, f)
	f.set(func(f *fakeHA) {
		f.process = func(text, lang string) string {
			speech := "Turned on the living room light."
			if lang == "de" {
				speech = "Das Licht im Wohnzimmer ist an."
			}
			return `{"response":{"response_type":"action_done","speech":{"plain":{"speech":"` + speech +
				`"}},"data":{"success":[{"id":"light.wz_decke","type":"entity"},{"id":"light.wz_steh","type":"entity"}]}}}`
		}
	})
	return newMux(), f, o, fm, page
}

func fmtAny(v any) string { s, _ := v.(string); return s }

func b3Say(t *testing.T, h http.Handler, body string) (string, []map[string]any, string) {
	t.Helper()
	raw := postChat(t, h, body).Body.String()
	return sseText(t, raw), rawEvents(t, raw), raw
}

func b3Plain(text string) string {
	b, _ := json.Marshal(map[string]any{"clientId": testClient, "messages": []map[string]any{{"role": "user", "content": text}}})
	return string(b)
}

func TestBatch3EndToEndEnglishAndGerman(t *testing.T) {
	for _, c := range b3Langs {
		t.Run(c.voice, func(t *testing.T) {
			if voiceLang(c.voice) != c.lang {
				t.Fatalf("voice %s is not %s", c.voice, c.lang)
			}
			h, f, o, fm, page := b3Env(t, c.voice)

			// 1. A device command goes to Assist in the voice's language.
			text, evs, raw := b3Say(t, h, b3Plain(c.command))
			if text != c.wantCommand {
				t.Fatalf("command reply %q (%s)", text, raw)
			}
			req := f.last("/api/conversation/process")
			if req == nil || req.Body["language"] != c.lang || !strings.EqualFold(fmtAny(req.Body["text"]), c.command) {
				t.Fatalf("assist request %+v", req)
			}
			if m := hasEvent(evs, "meta"); m["src"] != "fast:home_control" {
				t.Errorf("command meta %v", m)
			}
			if card := hasEvent(evs, "card"); card == nil || card["kind"] != "home" {
				t.Errorf("command card %v", evs)
			}
			if strings.Contains(raw, fakeHATok) {
				t.Error("token in the stream")
			}

			// 2. A device question is answered from the states.
			f.reset()
			text, evs, raw = b3Say(t, h, b3Plain(c.query))
			if text != c.wantQuery {
				t.Errorf("query reply %q (%s)", text, raw)
			}
			if m := hasEvent(evs, "meta"); m["src"] != "fast:home_query" {
				t.Errorf("query meta %v", m)
			}
			if n := f.count("/api/conversation/process") + f.count("/api/services/*"); n != 0 {
				t.Errorf("a question sent %d commands", n)
			}

			// 3. Unlocking the front door is refused without any HA request.
			f.reset()
			text, _, raw = b3Say(t, h, b3Plain(c.unlock))
			if text != c.wantRefusal {
				t.Errorf("unlock reply %q (%s)", text, raw)
			}
			if n := f.count(""); n != 0 {
				t.Errorf("unlock: HA got %d requests", n)
			}

			// 4. Upload a .txt through /api/docs and ask about it.
			up, _ := json.Marshal(map[string]any{"name": c.docName, "mime": "text/plain", "text": c.docText, "owner": ""})
			rec := docReq(h, http.MethodPost, "/api/docs", string(up))
			if rec.Code != http.StatusCreated {
				t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
			}
			waitDocsIdle(t)
			before := len(o.streamed())
			text, evs, raw = b3Say(t, h, b3Plain(c.docQuestion))
			if m := hasEvent(evs, "meta"); m["src"] != "local:docs" {
				t.Fatalf("docs meta %v (%s)", m, raw)
			}
			if !strings.HasPrefix(text, c.langMark) {
				t.Errorf("docs reply %q is not in %s", text, c.lang)
			}
			bodies := o.streamed()
			if len(bodies) != before+1 {
				t.Fatalf("local model calls %d", len(bodies)-before)
			}
			sys, user := messagesOf(bodies[len(bodies)-1])
			if !strings.Contains(sys, c.docPrompt) || !strings.Contains(sys, c.docName) || user != c.docQuestion {
				t.Errorf("docs prompt %q / %q", sys, user)
			}
			if _, has := bodies[len(bodies)-1]["tools"]; has {
				t.Error("docs answer offered tools")
			}

			// 5. A link: the real fetch endpoint on a stand-in public page,
			// then the chat turn with the attachment links.js would build.
			rec = fetchReq(h, page.URL+"/article")
			if rec.Code != http.StatusOK {
				t.Fatalf("link fetch %d %s", rec.Code, rec.Body.String())
			}
			got := decodeLink(t, rec)
			fallback, _ := got["fallbackText"].(string)
			if got["title"] != "Cats in rented flats" || !strings.Contains(fallback, "court ruling") {
				t.Fatalf("fetched %v", got)
			}
			att := map[string]any{
				"kind": "link", "url": page.URL + "/article", "finalUrl": got["finalUrl"],
				"title": got["title"], "site": "example", "text": fallback, "paywall": false,
				"instruction": c.linkInstruction,
			}
			b, _ := json.Marshal(map[string]any{
				"clientId":   testClient,
				"messages":   []map[string]any{{"role": "user", "content": "[Link: 127.0.0.1]", "att": true}},
				"attachment": att,
			})
			before = len(o.streamed())
			text, evs, raw = b3Say(t, h, string(b))
			if m := hasEvent(evs, "meta"); m["src"] != "local:link" {
				t.Fatalf("link meta %v (%s)", m, raw)
			}
			if card := hasEvent(evs, "card"); card == nil || card["kind"] != "link" {
				t.Errorf("link card %v", evs)
			}
			if !strings.HasPrefix(text, c.langMark) {
				t.Errorf("link reply %q is not in %s", text, c.lang)
			}
			bodies = o.streamed()
			if len(bodies) != before+1 {
				t.Fatalf("local model calls %d", len(bodies)-before)
			}
			sys, user = messagesOf(bodies[len(bodies)-1])
			if !strings.Contains(sys, c.linkPrompt) || !strings.Contains(sys, "court ruling") {
				t.Errorf("link prompt %q", sys)
			}
			if user != c.linkInstruction {
				t.Errorf("link instruction %q, want %q", user, c.linkInstruction)
			}

			// Nothing of this asked a realtime service.
			if n := len(fm.requests()); n != 0 {
				t.Errorf("realtime services asked %d times", n)
			}
			time.Sleep(10 * time.Millisecond) // let after-turn work settle before cleanup
		})
	}
}
