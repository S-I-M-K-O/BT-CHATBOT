package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

var settingsMu sync.Mutex

type Settings struct {
	SystemPrompt string  `json:"systemPrompt"`
	Temperature  float64 `json:"temperature"`
	MaxTokens    int     `json:"maxTokens"`
	Voice        string  `json:"voice"`
	VoiceMode    string  `json:"voiceMode"`
	Pitch        float64 `json:"pitch"`
	Rate         float64 `json:"rate"`
	Volume       float64 `json:"volume"`
	Speak        bool    `json:"speak"`
	Call         bool    `json:"call"`
	LocalModel   string  `json:"localModel"`
	EyeDesign    string  `json:"eyeDesign"`
	PiperVoice   string  `json:"piperVoice"`
	AutoScroll   bool    `json:"autoScroll"`
	VoiceSpeed   float64 `json:"voiceSpeed"`
	VoiceFx      string  `json:"voiceFx"`
	StreamSpeech bool    `json:"streamSpeech"`
}

const (
	defaultLocalModel  = "qwen3.5:latest"
	defaultPiperVoice  = "BT7274"
	maxSystemPromptLen = 8000
	maxSettingStrLen   = 200
)

var eyeDesigns = wordSet("classic", "cyclops", "iris", "cortana")
var voiceFxModes = wordSet("none", "titan", "radio")

func defaultSettings() *Settings {
	return &Settings{
		SystemPrompt: englishDefaultPrompt,
		Temperature:  0.7,
		MaxTokens:    1024,
		Voice:        "",
		VoiceMode:    "piper",
		Pitch:        0.6,
		Rate:         1.0,
		Volume:       1.0,
		Speak:        true,
		Call:         true,
		LocalModel:   defaultLocalModel,
		EyeDesign:    "classic",
		PiperVoice:   defaultPiperVoice,
		AutoScroll:   true,
		VoiceSpeed:   1.0,
		VoiceFx:      "titan",
		StreamSpeech: true,
	}
}

func finiteOr(v, fallback float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return fallback
	}
	return v
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// normalize clamps and defaults every field so the rest of the server (and
// the frontend) can trust the values. It also migrates legacy values.
func (s *Settings) normalize() {
	d := defaultSettings()
	s.Temperature = clamp(finiteOr(s.Temperature, d.Temperature), 0, 2)
	s.MaxTokens = clampInt(s.MaxTokens, 16, 8192)
	s.Pitch = clamp(finiteOr(s.Pitch, d.Pitch), 0, 2)
	// Older versions stored a negative "rate" (-0.2); the browser speech rate
	// is a multiplier, so anything <= 0 becomes the neutral 1.0.
	if s.Rate <= 0 || math.IsNaN(s.Rate) {
		s.Rate = 1.0
	}
	s.Rate = clamp(finiteOr(s.Rate, 1.0), 0.1, 3)
	s.Volume = clamp(finiteOr(s.Volume, d.Volume), 0, 1)
	if s.VoiceSpeed == 0 || math.IsNaN(s.VoiceSpeed) {
		s.VoiceSpeed = 1.0
	}
	s.VoiceSpeed = clamp(finiteOr(s.VoiceSpeed, 1.0), 0.5, 2)
	if s.VoiceMode != "piper" && s.VoiceMode != "browser" {
		s.VoiceMode = d.VoiceMode
	}
	if !voiceFxModes[s.VoiceFx] {
		s.VoiceFx = d.VoiceFx
	}
	if !eyeDesigns[s.EyeDesign] {
		s.EyeDesign = d.EyeDesign
	}
	if _, ok := piperVoices[s.PiperVoice]; !ok {
		s.PiperVoice = d.PiperVoice
	}
	s.LocalModel = strings.TrimSpace(truncateRunes(s.LocalModel, maxSettingStrLen))
	if s.LocalModel == "" {
		s.LocalModel = d.LocalModel
	}
	s.Voice = truncateRunes(s.Voice, maxSettingStrLen)
	s.SystemPrompt = truncateRunes(s.SystemPrompt, maxSystemPromptLen)
}

// settingsPath is where settings are written. SETTINGS_FILE wins; the default
// lives in the data dir next to generated images.
func settingsPath() string {
	return envOr("SETTINGS_FILE", defaultSettingsPath)
}

var defaultSettingsPath = filepath.Join("data", "settings.json")

// legacySettingsPath is the pre-data-dir location, read (never written) when
// SETTINGS_FILE is unset and the new file does not exist yet.
var legacySettingsPath = "settings.json"

func loadSettings() *Settings {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	s := defaultSettings()
	path := settingsPath()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && os.Getenv("SETTINGS_FILE") == "" {
		b, err = os.ReadFile(legacySettingsPath)
	}
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("settings read error: %v", err)
		}
		return s
	}
	if err := json.Unmarshal(b, s); err != nil {
		log.Printf("settings parse error: %v", err)
		s = defaultSettings()
	}
	s.normalize()
	return s
}

func saveSettings(s *Settings) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	path := settingsPath()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "settings_*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		writeJSON(w, http.StatusOK, loadSettings())
	case http.MethodPost, http.MethodPut:
		handleSaveSettings(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, PUT")
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	// Decode on top of the defaults so fields missing from the request keep
	// their default values instead of becoming zero.
	s := defaultSettings()
	if err := json.NewDecoder(r.Body).Decode(s); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad request")
		return
	}
	s.normalize()
	if err := saveSettings(s); err != nil {
		log.Printf("settings save error: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "could not save settings")
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
