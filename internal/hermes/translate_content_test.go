package hermes

import "testing"

func TestContentLangChange(t *testing.T) {
	de, en := &Settings{PiperVoice: "JarvisDE"}, &Settings{PiperVoice: "BT7274"}
	if got := contentLangChange(de, en); got != "en" {
		t.Errorf("voice de→en: %q", got)
	}
	if got := contentLangChange(en, de); got != "de" {
		t.Errorf("voice en→de: %q", got)
	}
	if got := contentLangChange(en, &Settings{PiperVoice: "PDA"}); got != "" {
		t.Errorf("same language: %q", got)
	}
	if got := contentLangChange(&Settings{PiperVoice: "BT7274", UILang: "en"}, &Settings{PiperVoice: "BT7274", UILang: "de"}); got != "de" {
		t.Errorf("ui en→de: %q", got)
	}
}

func TestTranslationStaysLocal(t *testing.T) {
	cases := []struct {
		base, model string
		ok          bool
	}{
		{"http://localhost:11434", "qwen3.5:latest", true},
		{"http://127.0.0.1:11434", "qwen3.5:latest", true},
		{"http://host.docker.internal:11434", "qwen3.5:4b", true},
		{"http://192.168.1.20:11434", "llama3.1:8b", true},
		{"http://ollama:11434", "llama3.1:8b", true},
		{"http://mac-mini.local:11434", "llama3.1:8b", true},
		{"https://ollama.com", "qwen3.5:latest", false},
		{"http://8.8.8.8:11434", "qwen3.5:latest", false},
		{"https://api.example.com", "qwen3.5:latest", false},
		{"http://localhost:11434", "gpt-oss:120b-cloud", false},
		{"http://localhost:11434", "qwen3-coder:480b:cloud", false},
		{"ftp://localhost", "qwen3.5:latest", false},
		{"", "qwen3.5:latest", false},
	}
	for _, c := range cases {
		if got := translationLocal(c.base, c.model); got != c.ok {
			t.Errorf("translationLocal(%q, %q) = %v, want %v", c.base, c.model, got, c.ok)
		}
	}
}

func TestReminderNeedsTranslation(t *testing.T) {
	cases := []struct {
		name string
		r    Reminder
		lang string
		want bool
	}{
		{"open, other language", Reminder{Kind: reminderKindReminder, Status: reminderPending, Lang: "de", Text: "Wasser ausstellen"}, "en", true},
		{"already in language", Reminder{Kind: reminderKindReminder, Status: reminderPending, Lang: "en", Text: "turn off the water"}, "en", false},
		{"language tag right, text clearly in the other language", Reminder{Kind: reminderKindReminder, Status: reminderPending, Lang: "en", Text: "ich muss die Wäsche waschen und das Auto holen"}, "en", true},
		{"timer without text", Reminder{Kind: reminderKindTimer, Status: reminderPending, Lang: "de"}, "en", true},
		{"fired, still announcing", Reminder{Kind: reminderKindReminder, Status: reminderFired, Lang: "de", Text: "Pizza"}, "en", true},
		{"done", Reminder{Kind: reminderKindReminder, Status: reminderDone, Lang: "de", Text: "Pizza"}, "en", false},
		{"cancelled", Reminder{Kind: reminderKindReminder, Status: reminderCancelled, Lang: "de", Text: "Pizza"}, "en", false},
		{"calendar meeting is somebody else's text", Reminder{Kind: reminderKindMeeting, Status: reminderPending, Lang: "de", Text: "Team-Call"}, "en", false},
	}
	for _, c := range cases {
		if got := reminderNeedsTranslation(&c.r, c.lang); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
