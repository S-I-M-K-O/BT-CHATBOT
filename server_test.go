package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStatusEndpoint(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"models":[{"name":"qwen3.5:latest"},{"name":"llama3:8b"}]}`))
	}))
	defer ollama.Close()
	t.Setenv("OLLAMA_BASE", ollama.URL+"/")
	t.Setenv("PIPER_BIN", filepath.Join(t.TempDir(), "nope"))
	t.Setenv("MISTRAL_API_KEY", "")
	voiceDir := t.TempDir()
	os.WriteFile(filepath.Join(voiceDir, "voices.json"), []byte(`{"PDA":{"language":{"family":"de","code":"de_DE"}}}`), 0o644)
	t.Setenv("VOICE_DIR", voiceDir)

	rec := httptest.NewRecorder()
	newMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var st statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Ollama.OK || len(st.Ollama.Models) != 2 || st.Ollama.Models[0] != "qwen3.5:latest" {
		t.Errorf("ollama: %+v", st.Ollama)
	}
	if st.Piper.OK || st.Mistral.Enabled {
		t.Errorf("piper/mistral: %+v %+v", st.Piper, st.Mistral)
	}
	if len(st.Voices) != 4 || st.Voices[0].Key != "BT7274" || st.Voices[0].Name != "BT-7274" {
		t.Errorf("voices: %+v", st.Voices)
	}
	for _, v := range st.Voices {
		if v.Key == "PDA" && v.Lang != "de" {
			t.Errorf("voices.json language not applied: %+v", v)
		}
		if v.Key == "JarvisDE" && v.Lang != "de" {
			t.Errorf("JarvisDE must be German: %+v", v)
		}
	}
}

func TestStatusOllamaDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	t.Setenv("OLLAMA_BASE", srv.URL)
	rec := httptest.NewRecorder()
	start := time.Now()
	newMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if time.Since(start) > 3*time.Second {
		t.Error("status probe must be bounded by ~2s")
	}
	var st statusResponse
	json.Unmarshal(rec.Body.Bytes(), &st)
	if st.Ollama.OK || st.Ollama.Error == "" || st.Ollama.Models == nil {
		t.Errorf("expected ollama error and empty (non-null) model list: %s", rec.Body)
	}
}

func TestHealth(t *testing.T) {
	rec := httptest.NewRecorder()
	newMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"status":"ok"}` {
		t.Errorf("health: %d %s", rec.Code, rec.Body)
	}
}

func TestGenHandler(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GEN_DIR", dir)
	png := []byte("\x89PNG\r\n\x1a\n0000")
	os.WriteFile(filepath.Join(dir, "hermes_123.png"), png, 0o644)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("secret"), 0o644)

	rec := httptest.NewRecorder()
	handleGen(rec, httptest.NewRequest(http.MethodGet, "/gen/hermes_123.png", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Cache-Control") == "" {
		t.Errorf("valid image: %d %v", rec.Code, rec.Header())
	}

	for _, p := range []string{
		"/gen/secret.txt",
		"/gen/../main.go",
		"/gen/..%2fmain.go",
		"/gen/hermes_123.png/../../main.go",
		"/gen/hermes_999.png", // valid name, missing file
		"/gen/",
		"/gen/search_1.jpg",
	} {
		req := httptest.NewRequest(http.MethodGet, "/gen/x", nil)
		req.URL.Path = p
		rec := httptest.NewRecorder()
		handleGen(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, rec.Code)
		}
	}
}

func TestRecoverMiddleware(t *testing.T) {
	h := recoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status %d", rec.Code)
	}
}

func TestLogMiddlewareKeepsFlusher(t *testing.T) {
	var isFlusher bool
	h := logMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, isFlusher = w.(http.Flusher)
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if !isFlusher || rec.Code != http.StatusTeapot {
		t.Errorf("flusher %v code %d", isFlusher, rec.Code)
	}
}

func TestStaticSecurityAndCacheHeaders(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>x</title>"), 0o644)
	os.WriteFile(filepath.Join(dir, "app.js"), []byte("1"), 0o644)
	os.WriteFile(filepath.Join(dir, "favicon.svg"), []byte("<svg/>"), 0o644)
	t.Setenv("STATIC_DIR", dir)
	h := newMux()
	for path, wantCache := range map[string]string{
		"/":            "no-cache",
		"/app.js":      "no-cache",
		"/favicon.svg": "public, max-age=3600",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != 200 {
			t.Errorf("%s: status %d", path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != wantCache {
			t.Errorf("%s: Cache-Control %q, want %q", path, got, wantCache)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" ||
			!strings.Contains(rec.Header().Get("Content-Security-Policy"), "media-src 'self' blob:") ||
			rec.Header().Get("Referrer-Policy") == "" {
			t.Errorf("%s: missing security headers %v", path, rec.Header())
		}
	}
}

func TestCleanupGenDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GEN_DIR", dir)
	oldF := filepath.Join(dir, "search_1_0.jpg")
	newF := filepath.Join(dir, "search_2_0.jpg")
	os.WriteFile(oldF, []byte("x"), 0o644)
	os.WriteFile(newF, []byte("x"), 0o644)
	past := time.Now().Add(-genMaxAge - time.Hour)
	os.Chtimes(oldF, past, past)
	cleanupGenDir()
	if _, err := os.Stat(oldF); !os.IsNotExist(err) {
		t.Error("old file not removed")
	}
	if _, err := os.Stat(newF); err != nil {
		t.Error("fresh file removed")
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	os.WriteFile(".env", []byte("# comment\nexport HERMES_T1=\"quoted\"\nHERMES_T2='single'\nHERMES_T3=keep\nbadline\n"), 0o644)
	t.Setenv("HERMES_T3", "preset")
	os.Unsetenv("HERMES_T1")
	os.Unsetenv("HERMES_T2")
	t.Cleanup(func() { os.Unsetenv("HERMES_T1"); os.Unsetenv("HERMES_T2") })
	loadDotEnv()
	if os.Getenv("HERMES_T1") != "quoted" || os.Getenv("HERMES_T2") != "single" || os.Getenv("HERMES_T3") != "preset" {
		t.Errorf("got %q %q %q", os.Getenv("HERMES_T1"), os.Getenv("HERMES_T2"), os.Getenv("HERMES_T3"))
	}
}
