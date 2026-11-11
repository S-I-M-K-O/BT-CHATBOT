package hermes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMistral is a scripted stand-in for api.mistral.ai. Each path has a queue
// of responses; every request body is recorded for assertions.
type fakeMistral struct {
	mu        sync.Mutex
	responses map[string][]fakeResp
	bodies    map[string][]string
	hits      map[string]int
}

type fakeResp struct {
	status int
	body   string
}

func newFakeMistral(t *testing.T) *fakeMistral {
	t.Helper()
	f := &fakeMistral{responses: map[string][]fakeResp{}, bodies: map[string][]string{}, hits: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization header = %q", got)
		}
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies[r.URL.Path] = append(f.bodies[r.URL.Path], string(b))
		f.hits[r.URL.Path]++
		q := f.responses[r.URL.Path]
		var resp fakeResp
		switch {
		case len(q) == 0:
			resp = fakeResp{http.StatusNotFound, `{"error":"unscripted ` + r.URL.Path + `"}`}
		case len(q) == 1:
			resp = q[0] // last response repeats
		default:
			resp, f.responses[r.URL.Path] = q[0], q[1:]
		}
		f.mu.Unlock()
		w.WriteHeader(resp.status)
		_, _ = w.Write([]byte(resp.body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MISTRAL_API_KEY", "test-key")
	t.Setenv("MISTRAL_BASE", srv.URL)
	t.Setenv("GEN_DIR", t.TempDir())
	// Reset package-level caches touched by these flows.
	forgetImageAgent(currentImageAgent())
	noteMistralResult(nil)
	t.Cleanup(func() { forgetImageAgent(currentImageAgent()); noteMistralResult(nil) })
	return f
}

func (f *fakeMistral) script(path string, rs ...fakeResp) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[path] = append(f.responses[path], rs...)
}

func currentImageAgent() string {
	imageAgent.Lock()
	defer imageAgent.Unlock()
	return imageAgent.id
}

func TestMistralToolLoopRunsToolAndStreamsAnswer(t *testing.T) {
	f := newFakeMistral(t)
	f.script("/v1/chat/completions",
		fakeResp{200, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"get_time","arguments":"{}"}}]}}]}`},
		fakeResp{200, `{"choices":[{"message":{"role":"assistant","content":"It is noon, Pilot."}}]}`},
	)
	rec := httptest.NewRecorder()
	msgs := []chatMessage{{Role: "system", Content: "sys"}, {Role: "user", Content: "what time is it"}}
	handleMistralTools(context.Background(), rec, defaultSettings(), msgs, "Europe/Berlin")

	evs := parseSSE(t, rec.Body.String())
	if got := joinDeltas(evs); got != "It is noon, Pilot." {
		t.Fatalf("streamed answer = %q", got)
	}
	if !evs[len(evs)-1].Done {
		t.Error("stream must end with done")
	}
	bodies := f.bodies["/v1/chat/completions"]
	if len(bodies) != 2 {
		t.Fatalf("expected 2 completion rounds, got %d", len(bodies))
	}
	// Round 2 must carry the tool result back to the model, in the user's zone.
	var second struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(bodies[1]), &second); err != nil {
		t.Fatal(err)
	}
	last := second.Messages[len(second.Messages)-1]
	if last["role"] != "tool" || last["tool_call_id"] != "c1" {
		t.Fatalf("last message of round 2 = %v, want tool result for c1", last)
	}
	if !strings.Contains(last["content"].(string), "Europe/Berlin") {
		t.Errorf("get_time result should use the browser time zone: %v", last["content"])
	}
}

func TestMistralToolLoopIsBounded(t *testing.T) {
	f := newFakeMistral(t)
	// The model keeps asking for tools forever; HERMES must stop and say so.
	f.script("/v1/chat/completions", fakeResp{200, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"x","type":"function","function":{"name":"get_time","arguments":"{}"}}]}}]}`})
	rec := httptest.NewRecorder()
	handleMistralTools(context.Background(), rec, defaultSettings(), []chatMessage{{Role: "user", Content: "time"}}, "")
	if n := f.hits["/v1/chat/completions"]; n != mistralToolRounds {
		t.Errorf("rounds = %d, want %d", n, mistralToolRounds)
	}
	evs := parseSSE(t, rec.Body.String())
	if evs[0].Error == "" || joinDeltas(evs) != phrasesEN.noData {
		t.Errorf("want error event + noData phrase, got %+v", evs)
	}
}

func TestMistralRejectedKeyIsReported(t *testing.T) {
	f := newFakeMistral(t)
	f.script("/v1/chat/completions", fakeResp{401, `{"message":"Unauthorized"}`})
	rec := httptest.NewRecorder()
	handleMistralTools(context.Background(), rec, defaultSettings(), []chatMessage{{Role: "user", Content: "weather in Paris"}}, "")
	evs := parseSSE(t, rec.Body.String())
	if evs[0].Error == "" {
		t.Error("expected an error event first")
	}
	if got := joinDeltas(evs); got != phrasesEN.satelliteKeyRejected {
		t.Errorf("reply = %q, want the key-rejected phrase", got)
	}
	if mistralAuthError() == "" {
		t.Error("status should now report the rejected key")
	}
	// The German voice gets the German phrase.
	rec = httptest.NewRecorder()
	de := defaultSettings()
	de.PiperVoice = "JarvisDE"
	handleMistralTools(context.Background(), rec, de, []chatMessage{{Role: "user", Content: "Wetter"}}, "")
	if got := joinDeltas(parseSSE(t, rec.Body.String())); got != phrasesDE.satelliteKeyRejected {
		t.Errorf("German reply = %q", got)
	}
}

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")

func TestMistralImageGenerationFlow(t *testing.T) {
	f := newFakeMistral(t)
	f.script("/v1/agents", fakeResp{200, `{"id":"ag_1"}`})
	f.script("/v1/conversations", fakeResp{200, `{"outputs":[{"type":"message.output","content":[{"type":"text","text":"Titan rendered."},{"type":"tool_file","file_id":"file_9"}]}]}`})
	f.script("/v1/files/file_9/content", fakeResp{200, string(pngBytes)})

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		handleMistralImage(context.Background(), rec, defaultSettings(), "draw a titan")
		evs := parseSSE(t, rec.Body.String())
		if evs[0].Intent != "image" {
			t.Fatalf("first event should announce the image intent: %+v", evs[0])
		}
		var img string
		for _, e := range evs {
			if e.Image != "" {
				img = e.Image
			}
		}
		if !genNamePattern.MatchString(strings.TrimPrefix(img, "/gen/")) {
			t.Fatalf("image url %q does not match the servable pattern", img)
		}
		fi, err := os.Stat(filepath.Join(genDir(), strings.TrimPrefix(img, "/gen/")))
		if err != nil {
			t.Fatalf("generated image not stored: %v", err)
		}
		// what someone asked to see is personal: owner-only like data files
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("generated image mode %v, want 0600", fi.Mode().Perm())
		}
		if got := joinDeltas(evs); !strings.Contains(got, "Titan rendered.") {
			t.Errorf("caption = %q", got)
		}
	}
	if n := f.hits["/v1/agents"]; n != 1 {
		t.Errorf("image agent should be created once and cached, created %d times", n)
	}
}

func TestMistralImageAgentResetOnlyWhenGone(t *testing.T) {
	f := newFakeMistral(t)
	f.script("/v1/agents", fakeResp{200, `{"id":"ag_1"}`})
	f.script("/v1/conversations", fakeResp{429, `{"message":"rate limited"}`})
	handleMistralImage(context.Background(), httptest.NewRecorder(), defaultSettings(), "draw a cat")
	if currentImageAgent() != "ag_1" {
		t.Fatal("a rate limit must not throw away the cached agent")
	}

	f.mu.Lock()
	f.responses["/v1/conversations"] = []fakeResp{{404, `{"message":"agent not found"}`}}
	f.mu.Unlock()
	rec := httptest.NewRecorder()
	handleMistralImage(context.Background(), rec, defaultSettings(), "draw a cat")
	if currentImageAgent() != "" {
		t.Error("a 404 means the agent is gone; it should be forgotten")
	}
	if got := joinDeltas(parseSSE(t, rec.Body.String())); got != phrasesEN.imageSynthesis {
		t.Errorf("reply = %q", got)
	}
}

func TestMistralImageWithoutFileOrBadBytes(t *testing.T) {
	f := newFakeMistral(t)
	f.script("/v1/agents", fakeResp{200, `{"id":"ag_1"}`})
	f.script("/v1/conversations",
		fakeResp{200, `{"outputs":[{"type":"message.output","content":"I cannot draw that."}]}`},
		fakeResp{200, `{"outputs":[{"type":"message.output","content":[{"type":"tool_file","file_id":"f2"}]}]}`},
	)
	f.script("/v1/files/f2/content", fakeResp{200, "<html>not an image</html>"})

	rec := httptest.NewRecorder()
	handleMistralImage(context.Background(), rec, defaultSettings(), "draw x")
	if got := joinDeltas(parseSSE(t, rec.Body.String())); got != phrasesEN.imageEmpty {
		t.Errorf("no file: reply = %q", got)
	}
	rec = httptest.NewRecorder()
	handleMistralImage(context.Background(), rec, defaultSettings(), "draw x")
	if got := joinDeltas(parseSSE(t, rec.Body.String())); got != phrasesEN.imageRetrieve {
		t.Errorf("non-image bytes: reply = %q", got)
	}
	if files, _ := os.ReadDir(genDir()); len(files) != 0 {
		t.Errorf("nothing should be stored for a non-image download, got %d files", len(files))
	}
}

func TestGetTimeToolZones(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	var out map[string]string
	_ = json.Unmarshal([]byte(getTimeTool(now, "", "Europe/Berlin")), &out)
	if out["time"] != "13:00" || out["timezone"] != "Europe/Berlin" || out["utc_offset"] != "+01:00" {
		t.Errorf("client zone: %v", out)
	}
	out = nil
	_ = json.Unmarshal([]byte(getTimeTool(now, "Asia/Tokyo", "Europe/Berlin")), &out)
	if out["time"] != "21:00" || out["timezone"] != "Asia/Tokyo" {
		t.Errorf("requested zone should win: %v", out)
	}
	out = nil
	_ = json.Unmarshal([]byte(getTimeTool(now, "Mars/Olympus", "UTC")), &out)
	if out["error"] == "" || out["time"] != "12:00" {
		t.Errorf("unknown zone should fall back with an error note: %v", out)
	}
}

func TestGetWeatherToolWithFakeOpenMeteo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/geo":
			if r.URL.Query().Get("name") != "Berlin" {
				_, _ = w.Write([]byte(`{"results":[]}`))
				return
			}
			if r.URL.Query().Get("language") != "de" {
				t.Errorf("German voice should geocode in German: %v", r.URL.Query())
			}
			_, _ = w.Write([]byte(`{"results":[{"latitude":52.52,"longitude":13.41,"name":"Berlin","country":"Deutschland"}]}`))
		case "/forecast":
			_, _ = w.Write([]byte(`{"current":{"temperature_2m":7.5,"relative_humidity_2m":81,"apparent_temperature":4.9,"weather_code":61,"wind_speed_10m":14}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldGeo, oldFc := openMeteoGeocodeURL, openMeteoForecastURL
	openMeteoGeocodeURL, openMeteoForecastURL = srv.URL+"/geo", srv.URL+"/forecast"
	defer func() { openMeteoGeocodeURL, openMeteoForecastURL = oldGeo, oldFc }()

	var out map[string]any
	if err := json.Unmarshal([]byte(getWeatherTool(context.Background(), "Berlin", "de")), &out); err != nil {
		t.Fatal(err)
	}
	if out["city"] != "Berlin, Deutschland" || out["temperature_c"] != 7.5 || out["condition"] != "rain" {
		t.Errorf("weather = %v", out)
	}
	if got := getWeatherTool(context.Background(), "", "en"); !strings.Contains(got, "error") {
		t.Errorf("empty city should be an error, got %s", got)
	}
	if got := getWeatherTool(context.Background(), "Atlantis", "en"); !strings.Contains(got, "error") {
		t.Errorf("unknown city should be an error, got %s", got)
	}
}

// Reasoning models (magistral-*) return content as a chunk array; the tool
// loop must read the text chunks instead of failing to decode the reply.
func TestMistralToolLoopAcceptsChunkedContent(t *testing.T) {
	f := newFakeMistral(t)
	f.script("/v1/chat/completions",
		fakeResp{200, `{"choices":[{"message":{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"need the time"}]}],"tool_calls":[{"id":"c1","type":"function","function":{"name":"get_time","arguments":"{}"}}]}}]}`},
		fakeResp{200, `{"choices":[{"message":{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"hmm"}]},{"type":"text","text":"It is noon, "},{"type":"text","text":"Pilot."}]}}]}`},
	)
	rec := httptest.NewRecorder()
	handleMistralTools(context.Background(), rec, defaultSettings(), []chatMessage{{Role: "user", Content: "what time is it"}}, "UTC")
	if got := joinDeltas(parseSSE(t, rec.Body.String())); got != "It is noon, Pilot." {
		t.Fatalf("streamed answer = %q", got)
	}
	// Round 2 echoes the assistant's chunked content back unchanged.
	var second struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(f.bodies["/v1/chat/completions"][1]), &second); err != nil {
		t.Fatal(err)
	}
	if _, ok := second.Messages[1]["content"].([]any); !ok {
		t.Errorf("assistant content should be echoed as chunks, got %v", second.Messages[1]["content"])
	}
}

func TestMistralText(t *testing.T) {
	cases := map[string]string{
		`"plain"`: "plain",
		`null`:    "",
		``:        "",
		`[{"type":"thinking","thinking":[]},{"type":"text","text":"a"},{"type":"text","text":"b"}]`: "ab",
	}
	for in, want := range cases {
		if got := mistralText(json.RawMessage(in)); got != want {
			t.Errorf("mistralText(%s) = %q, want %q", in, got, want)
		}
	}
}

// Only the system prompt, the last exchange and the question go to the
// Mistral cloud, never the whole local conversation.
func TestMistralToolsSendsTrimmedHistory(t *testing.T) {
	f := newFakeMistral(t)
	f.script("/v1/chat/completions", fakeResp{200, `{"choices":[{"message":{"role":"assistant","content":"Sunny, Pilot."}}]}`})
	msgs := []chatMessage{{Role: "system", Content: "sys"}}
	for i := 0; i < 10; i++ {
		msgs = append(msgs, chatMessage{Role: "user", Content: "private question"}, chatMessage{Role: "assistant", Content: "private answer"})
	}
	msgs = append(msgs,
		chatMessage{Role: "user", Content: "weather in Paris?"},
		chatMessage{Role: "assistant", Content: "Cloudy."},
		chatMessage{Role: "user", Content: "and tomorrow?"},
	)
	handleMistralTools(context.Background(), httptest.NewRecorder(), defaultSettings(), msgs, "")
	var body struct {
		Messages []chatMessage `json:"messages"`
	}
	if err := json.Unmarshal([]byte(f.bodies["/v1/chat/completions"][0]), &body); err != nil {
		t.Fatal(err)
	}
	want := []chatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "weather in Paris?"},
		{Role: "assistant", Content: "Cloudy."},
		{Role: "user", Content: "and tomorrow?"},
	}
	if len(body.Messages) != len(want) {
		t.Fatalf("sent %d messages, want %d: %+v", len(body.Messages), len(want), body.Messages)
	}
	for i := range want {
		if body.Messages[i] != want[i] {
			t.Errorf("message %d = %+v, want %+v", i, body.Messages[i], want[i])
		}
	}
}

// Open-Meteo returns nothing for "City, Country"; the tool must retry with
// the bare name and use the qualifier to pick the right match.
func TestGeocodeQualifiedCity(t *testing.T) {
	var names []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		names = append(names, name)
		if name != "Frankfurt" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[
			{"latitude":50.11,"longitude":8.68,"name":"Frankfurt am Main","country":"Germany","country_code":"DE","admin1":"Hesse"},
			{"latitude":52.34,"longitude":14.55,"name":"Frankfurt (Oder)","country":"Germany","country_code":"DE","admin1":"Brandenburg"}]}`))
	}))
	defer srv.Close()
	old := openMeteoGeocodeURL
	openMeteoGeocodeURL = srv.URL
	defer func() { openMeteoGeocodeURL = old }()

	lat, _, name, err := geocodeCity(context.Background(), "Frankfurt (Oder), Germany", "en")
	if err != nil {
		t.Fatal(err)
	}
	if lat != 52.34 || name != "Frankfurt (Oder), Germany" {
		t.Errorf("got %v %q, want Frankfurt (Oder)", lat, name)
	}
	if len(names) != 2 || names[1] != "Frankfurt" {
		t.Errorf("lookups = %q, want a retry with the bare name", names)
	}

	names = nil
	if lat, _, _, err := geocodeCity(context.Background(), "Frankfurt, Germany", "en"); err != nil || lat != 50.11 {
		t.Errorf("an unspecific qualifier should keep the top match, got %v %v", lat, err)
	}
	if _, _, _, err := geocodeCity(context.Background(), "Atlantis, Ocean", "en"); err == nil {
		t.Error("unknown city should still be an error")
	}
}

func TestMistralContextStartsOnUserTurn(t *testing.T) {
	got := mistralContext([]chatMessage{
		{Role: "system", Content: "sys"},
		{Role: "assistant", Content: "Hello, Pilot."},
		{Role: "user", Content: "weather?"},
	})
	if len(got) != 2 || got[1].Role != "user" {
		t.Errorf("context = %+v, want system + user", got)
	}
}

func TestBestGeocodeMatchShortCodes(t *testing.T) {
	results := []geocodeResult{
		{Name: "Halle", Country: "Sweden", CountryCode: "SE"},
		{Name: "Halle", Country: "Germany", CountryCode: "DE"},
	}
	if got := bestGeocodeMatch(results, []string{"DE"}); got.Country != "Germany" {
		t.Errorf("DE picked %s, want Germany", got.Country)
	}
}

// Through /api/chat the image handler reuses the turn's stream: one set of
// headers, the speaker event first, and meta right before done.
func TestChatImageFlowSharesTurnStream(t *testing.T) {
	useTempSettings(t)
	useTempData(t)
	withTestRegistry(t)
	f := newFakeMistral(t)
	f.script("/v1/agents", fakeResp{200, `{"id":"ag_1"}`})
	f.script("/v1/conversations", fakeResp{200, `{"outputs":[{"type":"message.output","content":[{"type":"text","text":"Titan rendered."},{"type":"tool_file","file_id":"file_9"}]}]}`})
	f.script("/v1/files/file_9/content", fakeResp{200, string(pngBytes)})
	peopleCountProvider = func() int { return 1 }

	rec := postChat(t, newMux(), chatBody(testClient, "draw a titan"))
	evs := rawEvents(t, rec.Body.String())
	if len(evs) < 4 || evs[0]["speaker"] == nil || evs[1]["intent"] != "image" {
		t.Fatalf("events %v", evs)
	}
	n := len(evs)
	meta, _ := evs[n-2]["meta"].(map[string]any)
	if meta["src"] != "image" || evs[n-1]["done"] != true {
		t.Errorf("tail %v", evs[n-2:])
	}
	dones := 0
	for _, e := range evs {
		if e["done"] == true {
			dones++
		}
	}
	if dones != 1 {
		t.Errorf("%d done events", dones)
	}
	// The image prompt is the current query only.
	for _, b := range f.bodies["/v1/conversations"] {
		if !strings.Contains(b, "draw a titan") || strings.Contains(b, "p_") {
			t.Errorf("conversation body %s", b)
		}
	}
}

// A model answer with dozens of tool calls (say, induced by a headline in a
// news result) must not fan out into dozens of outgoing requests: only the
// first mistralMaxToolCalls run, the rest get an error result so the
// conversation stays valid.
func TestMistralToolCallsPerRoundAreCapped(t *testing.T) {
	f := newFakeMistral(t)
	var geo sync.Mutex
	geoHits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		geo.Lock()
		geoHits++
		geo.Unlock()
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()
	old := openMeteoGeocodeURL
	openMeteoGeocodeURL = srv.URL
	t.Cleanup(func() { openMeteoGeocodeURL = old })

	var calls []string
	for i := 0; i < 30; i++ {
		calls = append(calls, `{"id":"c`+strings.Repeat("x", i)+`","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Berlin\"}"}}`)
	}
	f.script("/v1/chat/completions",
		fakeResp{200, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[` + strings.Join(calls, ",") + `]}}]}`},
		fakeResp{200, `{"choices":[{"message":{"role":"assistant","content":"Done."}}]}`},
	)
	rec := httptest.NewRecorder()
	msgs := []chatMessage{{Role: "system", Content: "sys"}, {Role: "user", Content: "weather in Berlin"}}
	handleMistralTools(context.Background(), rec, defaultSettings(), msgs, "")
	geo.Lock()
	hits := geoHits
	geo.Unlock()
	if hits > mistralMaxToolCalls {
		t.Errorf("%d geocode requests for one round, want at most %d", hits, mistralMaxToolCalls)
	}
	bodies := f.bodies["/v1/chat/completions"]
	if len(bodies) != 2 {
		t.Fatalf("rounds = %d", len(bodies))
	}
	var second struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(bodies[1]), &second); err != nil {
		t.Fatal(err)
	}
	tools := 0
	for _, m := range second.Messages {
		if m["role"] == "tool" {
			tools++
		}
	}
	if tools != 30 {
		t.Errorf("every tool call needs a result, got %d of 30", tools)
	}
	if got := joinDeltas(parseSSE(t, rec.Body.String())); got != "Done." {
		t.Errorf("answer = %q", got)
	}
}
