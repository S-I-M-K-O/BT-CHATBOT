package hermes

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// End-to-end language tests for reminders, timers and lists: every phrase
// goes through POST /api/chat with the Piper voice deciding the language
// (BT7274 → English, JarvisDE → German), a temp DATA_DIR, the fixed clock
// of remEnv (Friday 6 November 2026, 14:30 Berlin) and no reachable Ollama,
// so only the fast paths can answer.

// germanInEnglish are words that must never show up in an English reply.
var germanInEnglish = regexp.MustCompile(`(?i)\b(erinnerung|erinnere|uhr|einkaufsliste|liste|minuten|stunde|morgen|heute|abends|läuft|gesetzt|steht|auf der|und|nicht|ich|dich|wann)\b`)

// englishInGerman are words that must never show up in a German reply.
var englishInGerman = regexp.MustCompile(`(?i)\b(reminder|remind|tomorrow|today|minutes|shopping|list|the|is|set|left|cancelled|removed|added)\b`)

func voiceLangEnv(t *testing.T, voice string) http.Handler {
	t.Helper()
	remEnv(t)
	peopleCountProvider = nil // single-user mode: no speaker checks
	resetLastLists()
	t.Cleanup(resetLastLists)
	s := loadSettings()
	s.PiperVoice = voice
	s.Timezone = "Europe/Berlin"
	if err := saveSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := voiceLang(loadSettings().PiperVoice); (voice == "JarvisDE") != (got == "de") {
		t.Fatalf("voice %s maps to %q", voice, got)
	}
	return newMux()
}

// chatSay posts one utterance and returns the streamed reply text and events.
func chatSay(t *testing.T, h http.Handler, text string) (string, []map[string]any) {
	t.Helper()
	body := postChat(t, h, chatBody(testClient, text)).Body.String()
	reply := sseText(t, body)
	t.Logf("%q → %q", text, reply)
	return reply, rawEvents(t, body)
}

func pendingOfKind(kind string) []Reminder {
	var out []Reminder
	for _, r := range allReminders() {
		if r.Kind == kind && r.Status == "pending" {
			out = append(out, r)
		}
	}
	return out
}

func checkLang(t *testing.T, lang, phrase, reply string) {
	t.Helper()
	if strings.TrimSpace(reply) == "" {
		t.Errorf("%q: empty reply", phrase)
		return
	}
	bad := germanInEnglish
	if lang == "de" {
		bad = englishInGerman
	}
	if strings.Contains(reply, "for on ") || strings.Contains(reply, "für am ") {
		t.Errorf("%q: doubled preposition: %q", phrase, reply)
	}
	if m := bad.FindString(reply); m != "" {
		t.Errorf("%q: %s reply contains %q: %q", phrase, lang, m, reply)
	}
}

type remCase struct {
	phrase string
	kind   string
	due    time.Time
	text   string // stored text, "" = do not check
}

func runReminderCases(t *testing.T, voice, lang string, cases []remCase) {
	for _, c := range cases {
		t.Run(c.phrase, func(t *testing.T) {
			h := voiceLangEnv(t, voice)
			reply, evs := chatSay(t, h, c.phrase)
			checkLang(t, lang, c.phrase, reply)
			rs := allReminders()
			if len(rs) != 1 {
				t.Fatalf("%q: want one reminder, have %d (reply %q, events %v)", c.phrase, len(rs), reply, evs)
			}
			r := rs[0]
			if r.Kind != c.kind || !r.Due.Equal(c.due) {
				t.Errorf("%q: got %s due %s, want %s due %s (reply %q)", c.phrase, r.Kind, r.Due.In(berlin).Format("Mon 02 15:04"), c.kind, c.due.Format("Mon 02 15:04"), reply)
			}
			if c.text != "" && r.Text != c.text {
				t.Errorf("%q: text %q, want %q", c.phrase, r.Text, c.text)
			}
			if r.Lang != lang {
				t.Errorf("%q: stored lang %q, want %q", c.phrase, r.Lang, lang)
			}
			if card := hasEvent(evs, "card"); card == nil || (card["kind"] != "reminder" && card["kind"] != "timer") {
				t.Errorf("%q: no reminder card: %v", c.phrase, evs)
			}
			if sp := reminderSpeech(r, "", 0, nowFunc()); lang == "en" && germanInEnglish.MatchString(sp) {
				t.Errorf("%q: due speech is not English: %q", c.phrase, sp)
			} else if lang == "de" && englishInGerman.MatchString(sp) {
				t.Errorf("%q: due speech is not German: %q", c.phrase, sp)
			}
		})
	}
}

func TestEnglishVoiceReminderPhrases(t *testing.T) {
	runReminderCases(t, "BT7274", "en", []remCase{
		{"remind me in 20 min to take the pizza out", reminderKindReminder, at(6, 14, 50), "take the pizza out"},
		{"remind me in 20 mins to take the pizza out", reminderKindReminder, at(6, 14, 50), "take the pizza out"},
		{"Remind me in 20 minutes to take the pizza out", reminderKindReminder, at(6, 14, 50), "take the pizza out"},
		{"set a timer for 10 minutes", reminderKindTimer, at(6, 14, 40), ""},
		{"10 minute timer", reminderKindTimer, at(6, 14, 40), ""},
		{"remind me in half an hour to call mom", reminderKindReminder, at(6, 15, 0), "call mom"},
		{"remind me tomorrow at 8am to water the plants", reminderKindReminder, at(7, 8, 0), "water the plants"},
		{"remind me tonight to lock the door", reminderKindReminder, at(6, 19, 0), "lock the door"},
		{"remind me at 5pm to feed the cat", reminderKindReminder, at(6, 17, 0), "feed the cat"},
		{"remind me next Monday to pay the rent", reminderKindReminder, at(9, 9, 0), "pay the rent"},
	})
}

func TestGermanVoiceReminderPhrases(t *testing.T) {
	runReminderCases(t, "JarvisDE", "de", []remCase{
		{"Erinnere mich in 20 Minuten daran, die Pizza rauszuholen", reminderKindReminder, at(6, 14, 50), ""},
		{"Stell einen Timer auf 10 Minuten", reminderKindTimer, at(6, 14, 40), ""},
		{"10 Minuten Timer", reminderKindTimer, at(6, 14, 40), ""},
		{"Erinnere mich in einer halben Stunde, Mama anzurufen", reminderKindReminder, at(6, 15, 0), ""},
		{"Erinnere mich morgen um 8 Uhr an die Pflanzen", reminderKindReminder, at(7, 8, 0), ""},
		{"Erinnere mich heute Abend an die Haustür", reminderKindReminder, at(6, 19, 0), ""},
		{"Erinnere mich um 17 Uhr, die Katze zu füttern", reminderKindReminder, at(6, 17, 0), ""},
		{"Erinnere mich nächsten Montag an die Miete", reminderKindReminder, at(9, 9, 0), ""},
	})
}

// timerFlow: set a timer, ask how long is left, cancel it.
func timerFlow(t *testing.T, voice, lang, set, left, cancel string, leftWant *regexp.Regexp) {
	h := voiceLangEnv(t, voice)
	reply, _ := chatSay(t, h, set)
	checkLang(t, lang, set, reply)
	if n := len(pendingOfKind(reminderKindTimer)); n != 1 {
		t.Fatalf("%q: %d pending timers (reply %q)", set, n, reply)
	}
	reply, _ = chatSay(t, h, left)
	checkLang(t, lang, left, reply)
	if !leftWant.MatchString(reply) {
		t.Errorf("%q: reply %q does not say the time left", left, reply)
	}
	reply, _ = chatSay(t, h, cancel)
	checkLang(t, lang, cancel, reply)
	if n := len(pendingOfKind(reminderKindTimer)); n != 0 {
		t.Errorf("%q: timer still pending (reply %q)", cancel, reply)
	}
}

func TestEnglishVoiceTimerLeftAndCancel(t *testing.T) {
	timerFlow(t, "BT7274", "en", "set a timer for 10 minutes", "how long is left?", "cancel the timer", regexp.MustCompile(`(?i)10 minutes`))
}

func TestGermanVoiceTimerLeftAndCancel(t *testing.T) {
	timerFlow(t, "JarvisDE", "de", "Stell einen Timer auf 10 Minuten", "Wie lange noch?", "Lösche den Timer", regexp.MustCompile(`(?i)10 Minuten`))
}

func shoppingOpen(t *testing.T) []string {
	t.Helper()
	return openOf(t, "shopping")
}

// listFlow: add twice, read, remove.
func listFlow(t *testing.T, voice, lang string, add1, add2, read, remove string, item1, item2 string) {
	h := voiceLangEnv(t, voice)
	reply, _ := chatSay(t, h, add1)
	checkLang(t, lang, add1, reply)
	if got := shoppingOpen(t); len(got) != 1 || !strings.EqualFold(got[0], item1) {
		t.Fatalf("%q: shopping list %v (reply %q)", add1, got, reply)
	}
	reply, _ = chatSay(t, h, add2)
	checkLang(t, lang, add2, reply)
	if got := shoppingOpen(t); len(got) != 2 {
		t.Fatalf("%q: shopping list %v (reply %q)", add2, got, reply)
	}
	reply, _ = chatSay(t, h, read)
	checkLang(t, lang, read, reply)
	if !strings.Contains(strings.ToLower(reply), strings.ToLower(item1)) || !strings.Contains(strings.ToLower(reply), strings.ToLower(item2)) {
		t.Errorf("%q: reply %q does not read both items", read, reply)
	}
	reply, _ = chatSay(t, h, remove)
	checkLang(t, lang, remove, reply)
	if got := shoppingOpen(t); len(got) != 1 || !strings.EqualFold(got[0], item2) {
		t.Errorf("%q: shopping list %v (reply %q)", remove, got, reply)
	}
}

func TestEnglishVoiceShoppingList(t *testing.T) {
	listFlow(t, "BT7274", "en", "add milk to the shopping list", "put eggs on my shopping list", "what's on my shopping list?", "remove milk from the shopping list", "milk", "eggs")
}

func TestGermanVoiceShoppingList(t *testing.T) {
	listFlow(t, "JarvisDE", "de", "Setz Milch auf die Einkaufsliste", "Pack Eier auf meine Einkaufsliste", "Was steht auf der Einkaufsliste?", "Nimm Milch von der Einkaufsliste", "Milch", "Eier")
}

// "vergiss/forget" + a reminder noun cancels the reminder instead of going
// to memory_forget or being read as "set a timer".
func TestForgetReminderCancels(t *testing.T) {
	for _, c := range []struct{ q, lang, kind string }{
		{"Vergiss die Erinnerung an die Pizza", "de", reminderKindReminder},
		{"vergiss das mit dem Timer", "de", reminderKindTimer},
		{"Vergiss den Timer", "de", reminderKindTimer},
		{"forget about the pizza reminder", "en", reminderKindReminder},
		{"forget the timer", "en", reminderKindTimer},
	} {
		t.Run(c.q, func(t *testing.T) {
			remEnv(t)
			peopleCountProvider = nil
			addTestReminder(t, Reminder{Text: "Pizza holen", Due: at(6, 15, 0)})
			addTestReminder(t, Reminder{Kind: reminderKindTimer, DurationS: 600, Due: at(6, 14, 40)})
			r := remSay(t, c.q, remAnna, c.lang)
			if !r.handled || len(pendingOfKind(c.kind)) != 0 || len(allReminders()) != 2 {
				t.Errorf("%q → %q; pending %s: %d", c.q, r.text, c.kind, len(pendingOfKind(c.kind)))
			}
			checkLang(t, c.lang, c.q, r.text)
		})
	}
}
