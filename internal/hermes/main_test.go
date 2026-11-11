package hermes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalAnswer(t *testing.T) {
	known := []string{"hello", "hi pilot", "who are you", "how are you", "thanks", "bye"}
	for _, q := range known {
		if got := localAnswer(q, "en"); got == "" {
			t.Errorf("localAnswer(%q): expected a canned reply, got empty", q)
		}
	}
	unknown := []string{"what is the weather like", "find a picture of a cat", "random gibberish"}
	for _, q := range unknown {
		if got := localAnswer(q, "en"); got != "" {
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
	got := effectiveSystemPrompt(custom)
	if !strings.HasPrefix(got, custom.SystemPrompt) || !strings.Contains(got, germanReplyInstruction) {
		t.Errorf("expected custom prompt kept plus German reply instruction, got %q", got)
	}
	if again := effectiveSystemPrompt(&Settings{PiperVoice: "JarvisDE", SystemPrompt: got}); again != got {
		t.Error("German reply instruction must not be appended twice")
	}
	customEN := defaultSettings()
	customEN.SystemPrompt = "You are my personal robot."
	if got := effectiveSystemPrompt(customEN); got != customEN.SystemPrompt+"\n\n"+englishReplyInstruction {
		t.Errorf("custom prompt + English voice: want the English reply instruction, got %q", got)
	}
}

// Switching voices must switch the reply language too, or the voice reads
// the other language with a heavy accent (German persona saved, English
// voice picked, and vice versa).
func TestEffectiveSystemPromptFollowsVoiceAfterSwitch(t *testing.T) {
	if got := effectiveSystemPrompt(&Settings{PiperVoice: "BT7274", SystemPrompt: germanDefaultPrompt}); got != englishDefaultPrompt+"\n\n"+englishReplyInstruction {
		t.Errorf("saved German default + English voice: %q", got)
	}
	if got := effectiveSystemPrompt(&Settings{PiperVoice: "JarvisDE", SystemPrompt: englishDefaultPrompt}); got != germanDefaultPrompt+"\n\n"+germanReplyInstruction {
		t.Errorf("saved English default + German voice: %q", got)
	}
	// a custom prompt saved together with the German instruction, then an
	// English voice: the German instruction goes, the English one comes
	saved := "Du bist mein Roboter.\n\n" + germanReplyInstruction
	got := effectiveSystemPrompt(&Settings{PiperVoice: "BT7274", SystemPrompt: saved})
	if strings.Contains(got, germanReplyInstruction) || !strings.HasSuffix(got, englishReplyInstruction) || !strings.HasPrefix(got, "Du bist mein Roboter.") {
		t.Errorf("custom + switch to English: %q", got)
	}
	back := effectiveSystemPrompt(&Settings{PiperVoice: "JarvisDE", SystemPrompt: got})
	if strings.Contains(back, englishReplyInstruction) || !strings.HasSuffix(back, germanReplyInstruction) {
		t.Errorf("custom + switch back to German: %q", back)
	}
	// the default persona saved with an instruction is still the default
	if got := effectiveSystemPrompt(&Settings{PiperVoice: "BT7274", SystemPrompt: germanDefaultPrompt + "\n\n" + germanReplyInstruction}); got != englishDefaultPrompt+"\n\n"+englishReplyInstruction {
		t.Errorf("German default + instruction + English voice: %q", got)
	}
}

// The default personas say nothing about the language, so the reply
// instruction must be appended to them too: an English question under a
// German voice otherwise gets an English answer.
func TestDefaultPromptCarriesReplyLanguage(t *testing.T) {
	if got := effectiveSystemPrompt(&Settings{PiperVoice: "JarvisDE"}); !strings.HasSuffix(got, germanReplyInstruction) {
		t.Errorf("German voice, empty prompt: %q", got)
	}
	if got := effectiveSystemPrompt(&Settings{PiperVoice: "BT7274", SystemPrompt: englishDefaultPrompt}); !strings.HasSuffix(got, englishReplyInstruction) {
		t.Errorf("English voice, default prompt: %q", got)
	}
	if replyLanguageReminder(&Settings{PiperVoice: "JarvisDE"}) == replyLanguageReminder(&Settings{PiperVoice: "BT7274"}) {
		t.Error("reply reminder does not follow the voice")
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

func TestParseDotEnvValue(t *testing.T) {
	cases := map[string]string{
		"":                   "",
		" # paste your key":  "", // empty value followed by a comment
		"\t#c":               "",
		"#c":                 "",
		"abc # c":            "abc",
		"abc\t# c":           "abc",
		"a#b":                "a#b", // no whitespace before '#': part of the value
		" abc ":              "abc",
		`"x # y" # c`:        "x # y",
		`'#quoted'`:          "#quoted",
		`"unterminated # c`:  `"unterminated # c`,
		"http://h:1/#frag":   "http://h:1/#frag",
		"sk-123 #note #more": "sk-123",
	}
	for in, want := range cases {
		if got := parseDotEnvValue(in); got != want {
			t.Errorf("parseDotEnvValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListenAddr(t *testing.T) {
	cases := map[string]string{
		"":          "127.0.0.1:8080",
		"0.0.0.0":   "0.0.0.0:8080",
		"::":        "[::]:8080",
		"::1":       "[::1]:8080",
		"[::1]":     "[::1]:8080",
		"localhost": "localhost:8080",
	}
	t.Setenv("PORT", "")
	for in, want := range cases {
		t.Setenv("ADDR", in)
		if got := listenAddr(); got != want {
			t.Errorf("ADDR=%q: %q, want %q", in, got, want)
		}
	}
}

// README "directories 0700": data/gen was created 0755 (E2E finding), and an
// existing folder keeps being tightened.
func TestEnsureGenDirIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gen")
	t.Setenv("GEN_DIR", dir)
	if err := ensureGenDir(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureGenDir(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("gen dir mode %v err %v", st.Mode().Perm(), err)
	}
}
