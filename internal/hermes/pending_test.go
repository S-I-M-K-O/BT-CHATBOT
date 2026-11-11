package hermes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testClient = "c_0000000000a1"

func chatBody(clientID, text string) string {
	b, _ := json.Marshal(map[string]any{
		"clientId": clientID,
		"messages": []map[string]string{{"role": "user", "content": text}},
	})
	return string(b)
}

// rawEvents returns every SSE payload as a generic map.
func rawEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m); err != nil {
			t.Fatalf("bad SSE payload %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func hasEvent(evs []map[string]any, key string) map[string]any {
	for _, e := range evs {
		if v, ok := e[key]; ok {
			m, _ := v.(map[string]any)
			if m == nil {
				m = map[string]any{}
			}
			return m
		}
	}
	return nil
}

func TestPendingAskTakeAndExpiry(t *testing.T) {
	withTestRegistry(t)
	now := time.Now()
	old := nowFunc
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = old })

	rec := httptest.NewRecorder()
	tr := &Turn{w: rec, ClientID: testClient, Speaker: Speaker{ID: "p_anna", Source: SourceVoice}}
	r := tr.Ask(Pending{Kind: "slot", Slot: "when", Tool: "x"}, "When?")
	if r.Text != "When?" {
		t.Errorf("reply %q", r.Text)
	}
	ev := hasEvent(rawEvents(t, rec.Body.String()), "pending")
	if ev == nil || ev["kind"] != "slot" || ev["expiresIn"] != float64(60) {
		t.Errorf("pending event = %v", ev)
	}
	p := takePending(testClient)
	if p == nil || p.Speaker != "p_anna" || p.Prompt != "When?" || !p.Expires.Equal(now.Add(pendingTTL)) {
		t.Fatalf("takePending = %+v", p)
	}
	if takePending(testClient) != nil {
		t.Error("takePending must remove the intent")
	}

	tr.Ask(Pending{Kind: "confirm", Tool: "x"}, "Sure?")
	nowFunc = func() time.Time { return now.Add(pendingTTL) }
	if takePending(testClient) != nil {
		t.Error("expired intent must not be returned")
	}

	// Without a client id nothing can be followed up: no event, nothing stored.
	rec = httptest.NewRecorder()
	(&Turn{w: rec}).Ask(Pending{Kind: "confirm"}, "Sure?")
	if rec.Body.Len() != 0 || len(pendings) != 0 {
		t.Errorf("ask without client id: body %q pendings %v", rec.Body.String(), pendings)
	}
}

func TestPendingOtherVoiceCannotAnswer(t *testing.T) {
	withTestRegistry(t)
	ran := false
	registerTool(Tool{Name: "stub_do", Run: func(*Turn, json.RawMessage) Reply { ran = true; return Reply{Text: "done"} }})
	anna := &Turn{w: httptest.NewRecorder(), ClientID: testClient, Speaker: Speaker{ID: "p_anna", Source: SourceVoice}}
	anna.Ask(Pending{Kind: "confirm", Tool: "stub_do"}, "Sure?")

	ben := &Turn{w: httptest.NewRecorder(), ClientID: testClient, Speaker: Speaker{ID: "p_ben", Source: SourceVoice},
		Settings: defaultSettings(), Lang: "en", Tokens: tokenize("ja")}
	if runPendingAnswer(ben) || ran {
		t.Fatal("another recognised voice answered Anna's question")
	}
	anna2 := &Turn{w: httptest.NewRecorder(), ClientID: testClient, Speaker: Speaker{ID: "p_anna", Source: SourceVoice},
		Settings: defaultSettings(), Lang: "en", Tokens: tokenize("ja")}
	if !runPendingAnswer(anna2) || !ran {
		t.Error("Anna's question must still be open for her")
	}
}

// stubConfirmTools registers a command that asks for confirmation and the
// tool that runs on "yes".
func stubConfirmTools(t *testing.T, access Access) *int {
	t.Helper()
	runs := new(int)
	registerTool(Tool{
		Name:       "stub_do",
		SideEffect: true,
		Access:     func(*Turn, json.RawMessage) Access { return access },
		Run: func(t *Turn, args json.RawMessage) Reply {
			*runs++
			return Reply{Text: "Stub done " + string(args) + ".", Card: &Card{Kind: "stub", Data: map[string]int{"n": *runs}}}
		},
	})
	registerTool(Tool{Name: "stub_ask", Run: func(t *Turn, _ json.RawMessage) Reply {
		return t.Ask(Pending{Kind: "confirm", Tool: "stub_do", Args: json.RawMessage(`{"a":1}`)}, "Shall I do the stub?")
	}})
	registerFastPath(FastPath{Name: "stub_ask", Order: 40, Tool: "stub_ask", Match: func(t *Turn) (json.RawMessage, bool) {
		return nil, hasPhrase(t.Tokens, "stubcmd")
	}})
	return runs
}

func TestPendingConfirmFlowThroughChat(t *testing.T) {
	chatEnv(t)
	useTempData(t)
	t.Setenv("OLLAMA_BASE", "http://127.0.0.1:1") // persona chat must not be needed
	withTestRegistry(t)
	runs := stubConfirmTools(t, AccessOpen)
	h := newMux()

	evs := rawEvents(t, postChat(t, h, chatBody(testClient, "Hermes stubcmd bitte")).Body.String())
	if p := hasEvent(evs, "pending"); p == nil || p["kind"] != "confirm" {
		t.Fatalf("no pending event: %v", evs)
	}
	if tool := hasEvent(evs, "tool"); tool == nil || tool["name"] != "stub_ask" {
		t.Errorf("tool event = %v", tool)
	}

	// "ja" runs the stored tool with the stored args, card and tool events first.
	body := postChat(t, h, chatBody(testClient, "ja")).Body.String()
	evs = rawEvents(t, body)
	if *runs != 1 || joinDeltas(parseSSE(t, body)) != `Stub done {"a":1}.` {
		t.Fatalf("yes did not run the tool: runs=%d body=%s", *runs, body)
	}
	if hasEvent(evs, "card") == nil || hasEvent(evs, "tool")["name"] != "stub_do" {
		t.Errorf("card/tool events missing: %v", evs)
	}

	// "nein" declines.
	postChat(t, h, chatBody(testClient, "stubcmd"))
	if got := joinDeltas(parseSSE(t, postChat(t, h, chatBody(testClient, "nein")).Body.String())); got != "Okay, I won't." {
		t.Errorf("no: %q", got)
	}
	if *runs != 1 {
		t.Error("no must not run the tool")
	}

	// An unrelated utterance clears the question and is handled normally.
	postChat(t, h, chatBody(testClient, "stubcmd"))
	if got := joinDeltas(parseSSE(t, postChat(t, h, chatBody(testClient, "hello")).Body.String())); !strings.Contains(got, "HERMES online") {
		t.Errorf("unrelated utterance must reach the normal pipeline, got %q", got)
	}
	if takePending(testClient) != nil {
		t.Error("pending must be cleared after a non-answer")
	}

	// Another client cannot answer this client's question.
	postChat(t, h, chatBody(testClient, "stubcmd"))
	postChat(t, h, chatBody("c_0000000000b2", "ja"))
	if *runs != 1 {
		t.Error("another client answered the question")
	}
}

func TestFastPathAccessIsCheckedCentrally(t *testing.T) {
	chatEnv(t)
	useTempData(t)
	t.Setenv("OLLAMA_BASE", "http://127.0.0.1:1")
	withTestRegistry(t)
	peopleCountProvider = func() int { return 2 } // people exist, no resolver: guest
	ran := false
	registerTool(Tool{Name: "stub_write", Access: func(*Turn, json.RawMessage) Access { return AccessWrite },
		Run: func(*Turn, json.RawMessage) Reply { ran = true; return Reply{Text: "written"} }})
	registerFastPath(FastPath{Name: "stub_write", Order: 30, Tool: "stub_write", Match: func(t *Turn) (json.RawMessage, bool) {
		return nil, hasPhrase(t.Tokens, "stubwrite")
	}})
	body := postChat(t, newMux(), chatBody(testClient, "stubwrite")).Body.String()
	if ran {
		t.Fatal("a guest ran a write tool")
	}
	if !strings.Contains(joinDeltas(parseSSE(t, body)), "only do that for people I know") {
		t.Errorf("want the guest refusal, got %s", body)
	}
	if sp := hasEvent(rawEvents(t, body), "speaker"); sp == nil || sp["source"] != SourceGuest {
		t.Errorf("speaker event = %v", sp)
	}
}

func TestRegisteredRoutesAreMounted(t *testing.T) {
	withTestRegistry(t)
	registerRoute("GET /api/stubroute", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	rec := httptest.NewRecorder()
	newMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stubroute", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Errorf("route not mounted: %d %s", rec.Code, rec.Body)
	}
	// Security headers still apply (the route sits behind the chain).
	if rec.Header().Get("X-Content-Type-Options") == "" {
		t.Error("registered route bypasses securityHeaders")
	}
}

// Without people or features a turn sends what v1 sent plus the meta event,
// which comes right before done.
func TestEmptyRegistrySendsOnlyMetaExtra(t *testing.T) {
	chatEnv(t)
	useTempData(t)
	withTestRegistry(t)
	body := postChat(t, newMux(), chatBody(testClient, "hello")).Body.String()
	evs := rawEvents(t, body)
	for _, e := range evs {
		for k := range e {
			if k != "delta" && k != "done" && k != "meta" {
				t.Errorf("unexpected event %q without features: %s", k, body)
			}
		}
	}
	n := len(evs)
	if n < 2 || evs[n-1]["done"] != true || evs[n-2]["meta"] == nil {
		t.Fatalf("meta must come right before done: %s", body)
	}
	meta := evs[n-2]["meta"].(map[string]any)
	if meta["src"] != "kb" || meta["sp"] != singleSpeakerID || meta["private"] != false {
		t.Errorf("meta = %v", meta)
	}
}

func TestUncertainSpeakerIsAskedWhoThenToolResumes(t *testing.T) {
	withTestRegistry(t)
	ran := 0
	registerTool(Tool{Name: "stub_read", Access: func(*Turn, json.RawMessage) Access { return AccessRead },
		Run: func(*Turn, json.RawMessage) Reply { ran++; return Reply{Text: "secret"} }})
	rec := httptest.NewRecorder()
	tr := &Turn{w: rec, ClientID: testClient, Lang: "de", Settings: defaultSettings(), Speaker: Speaker{Source: SourceUncertain}}
	r := runToolChecked(tr, "stub_read", json.RawMessage(`{"q":1}`))
	if ran != 0 || !strings.HasPrefix(r.Text, "Wer spricht gerade?") {
		t.Fatalf("uncertain speaker: ran=%d reply=%q", ran, r.Text)
	}
	if p := hasEvent(rawEvents(t, rec.Body.String()), "pending"); p == nil || p["kind"] != "slot" {
		t.Errorf("pending event = %v", p)
	}

	// Without the people feature the answer is not understood: dropped.
	ans := &Turn{w: httptest.NewRecorder(), ClientID: testClient, Lang: "de", Settings: defaultSettings(),
		Speaker: Speaker{Source: SourceUncertain}, Query: "Anna", Tokens: tokenize("Anna")}
	if runPendingAnswer(ans) {
		t.Error("slot:which without a resolver must not be answered")
	}

	// With it, the original tool and args run for the named person.
	tr.Ask(Pending{Kind: "slot", Slot: "which", Tool: "stub_read", Args: json.RawMessage(`{"q":1}`)}, "Wer?")
	whichSpeakerResume = func(t *Turn, p *Pending) (Reply, bool) {
		t.Speaker = Speaker{ID: "p_anna", Name: "Anna", Source: SourceClaim}
		return runToolChecked(t, p.Tool, p.Args), true
	}
	if !runPendingAnswer(ans) || ran != 1 {
		t.Errorf("slot:which resume: ran=%d", ran)
	}
}
