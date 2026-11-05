package main

import (
	"strings"
	"testing"
)

func TestLocalAnswerEnglish(t *testing.T) {
	cases := map[string]string{
		"hello":             "Hello",
		"Hi pilot":          "Hello",
		"hey!":              "Hello",
		"good morning":      "Hello",
		"who are you?":      "I am HERMES",
		"what is your name": "I am HERMES",
		"how are you":       "nominal",
		"hi, how are you?":  "nominal",
		"thanks":            "welcome",
		"thank you so much": "welcome",
		"bye":               "Goodbye",
		"good night":        "Goodbye",
		"see you later":     "Goodbye",
	}
	for q, want := range cases {
		got := localAnswer(q, "en")
		if !strings.Contains(got, want) {
			t.Errorf("localAnswer(%q, en) = %q, want it to contain %q", q, got, want)
		}
	}
}

func TestLocalAnswerGerman(t *testing.T) {
	cases := map[string]string{
		"hallo":              "Hallo. HERMES",
		"Servus":             "Hallo",
		"moin":               "Hallo",
		"Guten Morgen":       "Hallo",
		"guten Abend Hermes": "Hallo",
		"Wer bist du?":       "Ich bin HERMES",
		"Wie geht's?":        "nominal",
		"wie geht es dir":    "nominal",
		"Danke":              "Gern geschehen",
		"vielen Dank!":       "Gern geschehen",
		"Tschüss":            "Auf Wiedersehen",
		"gute Nacht":         "Auf Wiedersehen",
		"hello":              "Hallo", // the voice language decides the reply language
	}
	for q, want := range cases {
		got := localAnswer(q, "de")
		if !strings.Contains(got, want) {
			t.Errorf("localAnswer(%q, de) = %q, want it to contain %q", q, got, want)
		}
	}
}

func TestLocalAnswerPassesThrough(t *testing.T) {
	for _, q := range []string{
		"",
		"what is the weather like",
		"find a picture of a cat",
		"random gibberish",
		"hello, what is the weather in Berlin tomorrow?",
		"this hike was a real hill to climb",    // "hi" inside words
		"tell me about thanksgiving traditions", // "thank" inside a word
		"explain the byelorussian history",      // "bye" inside a word
		"what are you doing tomorrow",           // not identity
		"thanks, now explain quantum computing in detail please",
		"no thanks",
		"Hallo, kannst du mir die Relativitätstheorie erklären?",
	} {
		if got := localAnswer(q, "en"); got != "" {
			t.Errorf("localAnswer(%q) = %q, want empty", q, got)
		}
	}
}

// TestLocalAnswerDoesNotSwallowFollowUps: in voice use people say "thanks"
// and the next question in one breath; the question must reach routing.
func TestLocalAnswerDoesNotSwallowFollowUps(t *testing.T) {
	for _, q := range []string{
		"Thanks, what time is it?",
		"Danke, wie spaet ist es?",
		"Thanks! And the news?",
		"thanks, play some music",
		"thanks whats the weather",
		"hi weather",
		"hello, draw a cat",
		"I can see you",
		"What does your name mean?",
		"How are you so smart?",
		"Danke, zeig mir Bilder von Katzen",
	} {
		if got := localAnswer(q, "en"); got != "" {
			t.Errorf("localAnswer(%q) = %q, want empty", q, got)
		}
	}
	for q, want := range map[string]string{
		"thanks a lot, Hermes":  "welcome",
		"ok bye then":           "Goodbye",
		"hello there":           "Hello",
		"what's your name?":     "I am HERMES",
		"hey hermes, thank you": "welcome",
	} {
		if got := localAnswer(q, "en"); !strings.Contains(got, want) {
			t.Errorf("localAnswer(%q) = %q, want it to contain %q", q, got, want)
		}
	}
}
