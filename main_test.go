package main

import (
	"strings"
	"testing"
)

func TestLocalAnswer(t *testing.T) {
	known := []string{"hello", "hi pilot", "who are you", "how are you", "thanks", "bye"}
	for _, q := range known {
		if got := localAnswer(q); got == "" {
			t.Errorf("localAnswer(%q): expected a canned reply, got empty", q)
		}
	}
	unknown := []string{"what is the weather like", "find a picture of a cat", "random gibberish"}
	for _, q := range unknown {
		if got := localAnswer(q); got != "" {
			t.Errorf("localAnswer(%q): expected empty, got %q", q, got)
		}
	}
}

func TestClassifyImageRequest(t *testing.T) {
	cases := map[string]string{
		"draw a cat":                         "generate",
		"generate an image of a dog":         "generate",
		"paint me a sunset":                  "generate",
		"find a picture of the eiffel tower": "search",
		"show me a photo of a red car":       "search",
		"look up images of the moon":         "search",
		"what is the weather in paris":       "",
		"what time is it":                    "",
		"zeichne einen Drachen":              "generate",
		"generiere ein bild von einem Hund":  "generate",
		"male einen Sonnenuntergang":         "generate",
		"zeig mir ein Bild von einem Löwen":  "search",
		"suche ein Foto von einem Auto":      "search",
		"bilder von Katzen":                  "search",
	}
	for q, want := range cases {
		if got := classifyImageRequest(q); got != want {
			t.Errorf("classifyImageRequest(%q): got %q, want %q", q, got, want)
		}
	}
}

func TestWantsRealtime(t *testing.T) {
	if !wantsRealtime("what is the weather in Tokyo") {
		t.Error("expected weather to match realtime")
	}
	if !wantsRealtime("what time is it") {
		t.Error("expected time to match realtime")
	}
	if !wantsRealtime("any news about technology") {
		t.Error("expected news to match realtime")
	}
	if wantsRealtime("hello there") {
		t.Error("expected plain greeting to NOT match realtime")
	}
	if wantsRealtime("what can you do today") {
		t.Error("expected 'today' to not trigger realtime")
	}
	if !wantsRealtime("Wie ist das Wetter in Berlin?") {
		t.Error("expected German weather to match realtime")
	}
	if !wantsRealtime("Wie viel Uhr ist es?") {
		t.Error("expected German time to match realtime")
	}
	if wantsRealtime("Wie geht es dir heute?") {
		t.Error("expected 'heute' to not trigger realtime")
	}
}

func TestExtractImageSubject(t *testing.T) {
	cases := map[string]string{
		"find a picture of the eiffel tower": "eiffel tower",
		"show me a photo of a red car":       "red car",
		"search for pictures of the moon":    "moon",
		"zeig mir ein Bild von einem Löwen":  "löwen",
		"suche ein Foto von einem Auto":      "auto",
		"zeig mir bilder von dem Eiffelturm": "eiffelturm",
		"finde mir ein Bild von einem Hund":  "hund",
		"zeig mir ein Foto von einem Berg":   "berg",
	}
	for in, want := range cases {
		if got := extractImageSubject(in); got != want {
			t.Errorf("extractImageSubject(%q): got %q, want %q", in, got, want)
		}
	}
}

func TestWmoCondition(t *testing.T) {
	if got := wmoCondition(0); got != "clear sky" {
		t.Errorf("wmoCondition(0): got %q", got)
	}
	if got := wmoCondition(95); got != "thunderstorm" {
		t.Errorf("wmoCondition(95): got %q", got)
	}
	if got := wmoCondition(999); got != "unknown conditions" {
		t.Errorf("wmoCondition(999): got %q", got)
	}
}

func TestEffectiveSystemPrompt(t *testing.T) {
	en := defaultSettings()
	if got := effectiveSystemPrompt(en); !strings.Contains(got, "You are HERMES") {
		t.Error("expected English persona for English voice")
	}
	de := defaultSettings()
	de.PiperVoice = "JarvisDE"
	if got := effectiveSystemPrompt(de); !strings.Contains(got, "Du bist HERMES") {
		t.Error("expected German persona for German voice")
	}
	custom := defaultSettings()
	custom.PiperVoice = "JarvisDE"
	custom.SystemPrompt = "You are my personal robot."
	if got := effectiveSystemPrompt(custom); got != custom.SystemPrompt {
		t.Error("expected hand-written custom prompt to be respected")
	}
}

func TestPhrasesFor(t *testing.T) {
	if phrasesFor("BT7274").searchEmpty != phrasesEN.searchEmpty {
		t.Error("expected English phrases for English voice")
	}
	if phrasesFor("JarvisDE").searchEmpty != phrasesDE.searchEmpty {
		t.Error("expected German phrases for German voice")
	}
}
