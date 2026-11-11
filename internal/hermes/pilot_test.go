package hermes

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// HERMES must not call the user "Pilot" in any built-in line.
var pilotWord = regexp.MustCompile(`(?i)\bpilot(en|in)?\b`)

func TestNoPilotInBuiltinLines(t *testing.T) {
	for _, voice := range []string{"BT7274", "JarvisDE"} {
		v := reflect.ValueOf(phrasesFor(voice))
		for i := 0; i < v.NumField(); i++ {
			if s := v.Field(i).String(); pilotWord.MatchString(s) {
				t.Errorf("%s phrase %s says Pilot: %q", voice, v.Type().Field(i).Name, s)
			}
		}
	}
	for _, q := range []string{"hello", "who are you", "how are you", "thanks", "bye"} {
		for _, lang := range []string{"en", "de"} {
			if a := localAnswer(q, lang); pilotWord.MatchString(a) {
				t.Errorf("localAnswer(%q, %s) says Pilot: %q", q, lang, a)
			}
		}
	}
	for _, voice := range []string{"BT7274", "JarvisDE"} {
		p := effectiveSystemPrompt(&Settings{PiperVoice: voice})
		if !strings.Contains(p, "'Pilot'") || !strings.Contains(strings.ToLower(p), "nie") && !strings.Contains(p, "Never") {
			t.Errorf("%s prompt does not forbid 'Pilot': %q", voice, p)
		}
	}
}

func TestOldDefaultPromptIsUpgraded(t *testing.T) {
	for _, old := range legacyEnglishDefaultPrompts {
		if got := effectiveSystemPrompt(&Settings{SystemPrompt: old, PiperVoice: "BT7274"}); got != englishDefaultPrompt+"\n\n"+englishReplyInstruction {
			t.Errorf("saved old default not upgraded: %q", got)
		}
	}
}

func TestNoPilotInPage(t *testing.T) {
	for _, f := range []string{"web/static/index.html", "web/static/app.js"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if m := pilotWord.FindString(string(b)); m != "" {
			t.Errorf("%s still contains %q", f, m)
		}
	}
}
