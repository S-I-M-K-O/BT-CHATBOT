package main

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
		if _, err := os.Stat(filepath.Join(genDir(), strings.TrimPrefix(img, "/gen/"))); err != nil {
			t.Fatalf("generated image not stored: %v", err)
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
