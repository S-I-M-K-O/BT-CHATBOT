package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func fakeOllamaServer(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("OLLAMA_BASE", srv.URL)
}

func TestOllamaChatOnceSendsSharedOptions(t *testing.T) {
	var got map[string]any
	fakeOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"message":{"content":"","tool_calls":[{"function":{"name":"set_reminder","arguments":{"when_text":"in 20 Minuten"}}}]}}`))
	})
	s := defaultSettings()
	s.NumCtx, s.KeepAlive = 4096, "30m"
	res, err := ollamaChatOnce(context.Background(), s, ollamaChatRequest{
		Model: "m", Messages: []chatMessage{{Role: "user", Content: "hi"}},
		Tools: []any{map[string]any{"type": "function"}}, Format: "json", NumPredict: 256,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["stream"] != false || got["think"] != false || got["keep_alive"] != "30m" || got["format"] != "json" || got["tools"] == nil {
		t.Errorf("body %v", got)
	}
	opts := got["options"].(map[string]any)
	if opts["num_ctx"] != float64(4096) || opts["num_predict"] != float64(256) || opts["temperature"] != float64(0) {
		t.Errorf("options %v", opts)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Function.Name != "set_reminder" ||
		string(res.ToolCalls[0].Function.Arguments) != `{"when_text":"in 20 Minuten"}` {
		t.Errorf("tool calls %+v", res.ToolCalls)
	}
}

func TestOllamaChatOnceOmitsEmptyParts(t *testing.T) {
	var got map[string]any
	fakeOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"message":{"content":"ok"}}`))
	})
	s := defaultSettings()
	s.KeepAlive, s.NumCtx = "", 0
	res, err := ollamaChatOnce(context.Background(), s, ollamaChatRequest{Model: "m"})
	if err != nil || res.Content != "ok" {
		t.Fatalf("%v %+v", err, res)
	}
	for _, k := range []string{"keep_alive", "tools", "format"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s sent: %v", k, got)
		}
	}
	if _, ok := got["options"].(map[string]any)["num_ctx"]; ok {
		t.Error("num_ctx sent without a setting")
	}
}

func TestOllamaErrorsAndEmbed(t *testing.T) {
	fakeOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/embed" {
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			if _, ok := b["options"]; ok {
				t.Error("embed call sent chat options")
			}
			w.Write([]byte(`{"embeddings":[[0.1,0.2],[0.3,0.4]]}`))
			return
		}
		http.Error(w, `{"error":"model not found"}`, http.StatusNotFound)
	})
	s := defaultSettings()
	if _, err := ollamaChatOnce(context.Background(), s, ollamaChatRequest{Model: "x"}); !errors.Is(err, errOllamaStatus) {
		t.Errorf("err = %v", err)
	}
	v, err := ollamaEmbed(context.Background(), s, "e", []string{"a", "b"})
	if err != nil || len(v) != 2 || v[1][1] != 0.4 {
		t.Errorf("%v %v", v, err)
	}
	if _, err := ollamaEmbed(context.Background(), s, "e", []string{"only one"}); err == nil {
		t.Error("count mismatch accepted")
	}
	if _, err := ollamaEmbed(context.Background(), s, "", []string{"a"}); err == nil {
		t.Error("empty model accepted")
	}
}

func TestWarmLoadsModelsWithSharedOptionsOncePerMinute(t *testing.T) {
	useTempSettings(t)
	resetOllamaCaches()
	t.Cleanup(resetOllamaCaches)
	var mu sync.Mutex
	var bodies []map[string]any
	var paths []string
	fakeOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		bodies, paths = append(bodies, b), append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/api/embed" {
			w.Write([]byte(`{"embeddings":[[0.1,0.2]]}`))
			return
		}
		w.Write([]byte(`{"message":{"content":""},"done":true}`))
	})
	s := defaultSettings()
	s.LocalModel, s.RouterModel, s.EmbedModel, s.KeepAlive, s.NumCtx = "qwen3.5", "qwen3.5:latest", "embeddinggemma", "30m", 6144
	saveSettings(s)
	done := make(chan struct{}, 2)
	old := warmDone
	warmDone = func() { done <- struct{}{} }
	t.Cleanup(func() { warmDone = old })

	h := newMux()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/warm", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("warm: %d", rec.Code)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("warm-up did not finish")
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/warm", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"warming":false`) {
		t.Errorf("second warm within a minute: %d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	// qwen3.5 and qwen3.5:latest are one model: one chat warm-up plus embed.
	if strings.Join(paths, ",") != "/api/chat,/api/embed" {
		t.Fatalf("paths %v", paths)
	}
	for _, b := range bodies {
		if b["keep_alive"] != "30m" {
			t.Errorf("keep_alive missing: %v", b)
		}
	}
	opts, _ := bodies[0]["options"].(map[string]any)
	if opts["num_ctx"] != float64(6144) || bodies[0]["model"] != "qwen3.5" {
		t.Errorf("chat warm body %v", bodies[0])
	}
}

func TestOllamaModelWarmCachesPS(t *testing.T) {
	resetOllamaCaches()
	t.Cleanup(resetOllamaCaches)
	var mu sync.Mutex
	hits := 0
	fakeOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Write([]byte(`{"models":[{"name":"qwen3.5:latest","model":"qwen3.5:latest"}]}`))
	})
	if !ollamaModelWarm(context.Background(), "qwen3.5") || ollamaModelWarm(context.Background(), "llama3") {
		t.Error("warm detection")
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Errorf("/api/ps not cached: %d hits", hits)
	}
}
