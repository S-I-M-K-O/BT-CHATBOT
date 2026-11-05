package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useTempSettings points SETTINGS_FILE at a fresh temp file for one test.
func useTempSettings(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "nested", "settings.json")
	t.Setenv("SETTINGS_FILE", p)
	return p
}

func TestDefaultSettingsAreNormalized(t *testing.T) {
	d := defaultSettings()
	n := defaultSettings()
	n.normalize()
	if *d != *n {
		t.Errorf("defaults change under normalize:\n%+v\n%+v", d, n)
	}
	if d.Rate != 1.0 || !d.AutoScroll || d.VoiceSpeed != 1.0 || d.VoiceFx != "titan" || !d.StreamSpeech || d.Pitch != 0.6 {
		t.Errorf("unexpected defaults: %+v", d)
	}
}

func TestSettingsNormalize(t *testing.T) {
	s := &Settings{
		Temperature: 9, MaxTokens: 1, Pitch: -1, Rate: -0.2, Volume: 3,
		VoiceMode: "laser", EyeDesign: "../../etc", PiperVoice: "Evil",
		VoiceSpeed: 7, VoiceFx: "chorus", LocalModel: "   ",
		SystemPrompt: strings.Repeat("x", maxSystemPromptLen+50),
	}
	s.normalize()
	want := Settings{
		Temperature: 2, MaxTokens: 16, Pitch: 0, Rate: 1.0, Volume: 1,
		VoiceMode: "piper", EyeDesign: "classic", PiperVoice: "BT7274",
		VoiceSpeed: 2, VoiceFx: "titan", LocalModel: defaultLocalModel,
		SystemPrompt: strings.Repeat("x", maxSystemPromptLen),
	}
	if *s != want {
		t.Errorf("normalize:\n got %+v\nwant %+v", *s, want)
	}

	ok := &Settings{Rate: 2.5, VoiceSpeed: 0.7, VoiceFx: "radio", EyeDesign: "cortana", PiperVoice: "JarvisDE", VoiceMode: "browser", MaxTokens: 9000}
	ok.normalize()
	if ok.Rate != 2.5 || ok.VoiceSpeed != 0.7 || ok.VoiceFx != "radio" || ok.EyeDesign != "cortana" || ok.PiperVoice != "JarvisDE" || ok.VoiceMode != "browser" || ok.MaxTokens != 8192 {
		t.Errorf("valid values must survive normalize: %+v", ok)
	}
	for _, d := range []string{"classic", "cyclops", "iris", "cortana"} {
		s := &Settings{EyeDesign: d}
		s.normalize()
		if s.EyeDesign != d {
			t.Errorf("eye design %q rejected", d)
		}
	}
	// designs that were removed fall back to BT-7274 instead of a blank eye
	for _, d := range []string{"scorch", "radar", "mantis"} {
		s := &Settings{EyeDesign: d}
		s.normalize()
		if s.EyeDesign != "classic" {
			t.Errorf("removed design %q kept as %q", d, s.EyeDesign)
		}
	}
}

func TestLoadSettingsMigratesLegacyFile(t *testing.T) {
	p := useTempSettings(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	// An old settings file: negative rate, no autoScroll/voiceSpeed/voiceFx.
	legacy := `{"systemPrompt":"custom","temperature":0.5,"maxTokens":512,"rate":-0.2,"pitch":0.6,"volume":1,"speak":true,"call":false,"piperVoice":"PDA","eyeDesign":"iris"}`
	if err := os.WriteFile(p, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	s := loadSettings()
	if s.Rate != 1.0 {
		t.Errorf("rate not migrated: %v", s.Rate)
	}
	if !s.AutoScroll || !s.StreamSpeech || s.VoiceSpeed != 1.0 || s.VoiceFx != "titan" {
		t.Errorf("new fields must default: %+v", s)
	}
	if s.SystemPrompt != "custom" || s.MaxTokens != 512 || s.Call || s.PiperVoice != "PDA" || s.EyeDesign != "iris" {
		t.Errorf("stored values lost: %+v", s)
	}
}

func TestLoadSettingsCorruptFileFallsBack(t *testing.T) {
	p := useTempSettings(t)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("{not json"), 0o644)
	if s := loadSettings(); *s != *defaultSettings() {
		t.Errorf("expected defaults for corrupt file, got %+v", s)
	}
}

func TestLoadSettingsLegacyPathFallback(t *testing.T) {
	t.Setenv("SETTINGS_FILE", "")
	dir := t.TempDir()
	old := legacySettingsPath
	legacySettingsPath = filepath.Join(dir, "settings.json")
	t.Cleanup(func() { legacySettingsPath = old })

	oldDefault := defaultSettingsPath
	defaultSettingsPath = filepath.Join(dir, "data", "settings.json") // does not exist
	t.Cleanup(func() { defaultSettingsPath = oldDefault })
	os.WriteFile(legacySettingsPath, []byte(`{"eyeDesign":"iris"}`), 0o644)
	if s := loadSettings(); s.EyeDesign != "iris" {
		t.Errorf("legacy settings not read: %+v", s)
	}
}

func TestSettingsPathDefault(t *testing.T) {
	t.Setenv("SETTINGS_FILE", "")
	if got := settingsPath(); got != filepath.Join("data", "settings.json") {
		t.Errorf("settingsPath() = %q", got)
	}
}

func TestSettingsHandlerRoundTrip(t *testing.T) {
	p := useTempSettings(t)
	h := newMux()

	// GET with no file returns defaults.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status %d", rec.Code)
	}
	var got Settings
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got != *defaultSettings() {
		t.Errorf("GET defaults mismatch: %+v", got)
	}

	// Partial POST keeps defaults for missing fields and clamps the rest.
	body := `{"temperature":5,"eyeDesign":"cyclops","voiceSpeed":1.25,"autoScroll":false,"rate":-1}`
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status %d: %s", rec.Code, rec.Body)
	}
	got = Settings{}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Temperature != 2 || got.EyeDesign != "cyclops" || got.VoiceSpeed != 1.25 || got.AutoScroll || got.Rate != 1 {
		t.Errorf("POST response: %+v", got)
	}
	if got.MaxTokens != 1024 || !got.Speak || got.Pitch != 0.6 || got.VoiceFx != "titan" || !got.StreamSpeech {
		t.Errorf("missing fields must keep defaults: %+v", got)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("settings file not written (nested dir must be created): %v", err)
	}

	// GET returns what was saved.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	var again Settings
	json.Unmarshal(rec.Body.Bytes(), &again)
	if again != got {
		t.Errorf("round trip mismatch:\n%+v\n%+v", again, got)
	}
}

func TestSettingsHandlerErrors(t *testing.T) {
	useTempSettings(t)
	h := newMux()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader("{bad")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON: status %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/settings", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE: status %d", rec.Code)
	}
}
