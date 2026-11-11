package hermes

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Batch 2 end to end through POST /api/chat, in English (BT7274) and German
// (JarvisDE): the morning briefing with fake Open-Meteo and a fake Google
// Calendar, calendar questions and adds, and plain small talk with the
// briefing off. Nothing here leaves the machine.

type e2eLang struct {
	voice, lang               string
	greet, plain              string
	reminder, item            string
	todayEvent, tomorrowEvent string
	query, add                string
	wantGreet, wantWeather    string
	wantCal, wantReminder     string
	wantTomorrow, wantAdded   string
	addTitle                  string
	otherLang                 func(string) bool
}

var e2eLangs = []e2eLang{
	{
		voice: "BT7274", lang: "en",
		greet: "Good morning, Hermes", plain: "good morning",
		reminder: "take out the trash", item: "milk",
		todayEvent: "Team call", tomorrowEvent: "Haircut",
		query: "what's on tomorrow?", add: "add dentist tomorrow at 3pm to my calendar",
		wantGreet:    "Good morning. It is Friday, November 6, 7:30 AM.",
		wantWeather:  "In Berlin it is 6 degrees, overcast. Today up to 11 degrees, 40 percent chance of rain.",
		wantCal:      "Team call at 10",
		wantReminder: "take out the trash",
		wantTomorrow: "Tomorrow you have one event: Haircut at 9:00 AM.",
		wantAdded:    "Added: Dentist, tomorrow",
		addTitle:     "Dentist",
		otherLang:    germanInEnglish.MatchString,
	},
	{
		voice: "JarvisDE", lang: "de",
		greet: "Guten Morgen, Hermes", plain: "Guten Morgen",
		reminder: "Müll rausbringen", item: "Milch",
		todayEvent: "Team-Call", tomorrowEvent: "Friseur",
		query: "Was steht morgen an?", add: "Trag morgen um 15 Uhr Zahnarzt ein",
		wantGreet:    "Guten Morgen. Es ist Freitag, der 6. November, 7:30 Uhr.",
		wantWeather:  "In Berlin sind es 6 Grad, bedeckt. Heute bis 11 Grad, Regenrisiko 40 Prozent.",
		wantCal:      "um 10 Uhr Team-Call",
		wantReminder: "Müll rausbringen",
		wantTomorrow: "Morgen",
		wantAdded:    "Eingetragen: Zahnarzt, morgen",
		addTitle:     "Zahnarzt",
		otherLang:    englishInGerman.MatchString,
	},
}

// e2eEnv: the briefing harness (Friday 6 November 2026, 07:30 Berlin, fake
// Open-Meteo and news), single-user mode, the given voice, and a connected
// calendar on a fake Google server.
func e2eEnv(t *testing.T, voice string) (http.Handler, *fakeGoogle, *briefFakes) {
	t.Helper()
	_, f := briefEnv(t)
	peopleCountProvider = nil // single user: no speaker checks
	resetLastLists()
	t.Cleanup(resetLastLists)
	setBriefSettings(t, func(s *Settings) { s.PiperVoice = voice })
	t.Setenv("GOOGLE_CLIENT_ID", fgClientID)
	t.Setenv("GOOGLE_CLIENT_SECRET", fgSecret)
	t.Setenv("HERMES_TOKEN", "")
	resetCalendarState()
	t.Cleanup(resetCalendarState)
	fg := newFakeGoogle(t)
	connectFake(t, fg)
	return newMux(), fg, f
}

func TestBatch2EndToEndEnglishAndGerman(t *testing.T) {
	for _, c := range e2eLangs {
		t.Run(c.voice, func(t *testing.T) {
			h, fg, f := e2eEnv(t, c.voice)
			addTestReminder(t, Reminder{Text: c.reminder, Due: at(6, 18, 0), Lang: c.lang})
			if _, err := addItems(listsShoppingID, []string{c.item}, ""); err != nil {
				t.Fatal(err)
			}
			fg.set(func(fg *fakeGoogle) {
				fg.events = []map[string]any{fgTimed("a", c.todayEvent, at(6, 10, 0), time.Hour)}
			})

			// 1. The morning greeting gives the briefing in the voice's language.
			text, evs := chatSay(t, h, c.greet)
			if meta := hasEvent(evs, "meta"); meta["src"] != "fast:briefing" {
				t.Fatalf("greeting meta %v: %q", meta, text)
			}
			for _, want := range []string{c.wantGreet, c.wantWeather, c.wantCal, c.wantReminder, c.item} {
				if !strings.Contains(text, want) {
					t.Errorf("briefing lacks %q:\n%s", want, text)
				}
			}
			card := hasEvent(evs, "card")
			if card == nil || card["kind"] != "briefing" {
				t.Fatalf("no briefing card: %v", evs)
			}
			if got := sectionNames(briefingCard(t, remResult{events: evs})); got != "greeting,weather,calendar,reminders,lists" {
				t.Errorf("sections %s", got)
			}
			// Names and titles are the user's own data; check the rest.
			plain := text
			for _, own := range []string{c.todayEvent, c.reminder, c.item, "Berlin"} {
				plain = strings.ReplaceAll(plain, own, "")
			}
			if c.otherLang(plain) {
				t.Errorf("words of the other language in the %s briefing: %s", c.lang, text)
			}
			if f.lastGeoLang.Load() != c.lang {
				t.Errorf("geocode language %v", f.lastGeoLang.Load())
			}

			// 2. "What's on tomorrow?" reads tomorrow from the calendar.
			fg.set(func(fg *fakeGoogle) {
				fg.events = []map[string]any{fgTimed("b", c.tomorrowEvent, at(7, 9, 0), time.Hour)}
			})
			text, evs = chatSay(t, h, c.query)
			if meta := hasEvent(evs, "meta"); meta["src"] != "fast:calendar_query" {
				t.Fatalf("query meta %v: %q", meta, text)
			}
			if !strings.Contains(text, c.wantTomorrow) || !strings.Contains(text, c.tomorrowEvent) {
				t.Errorf("tomorrow reply %q", text)
			}
			if c.lang == "de" && !strings.Contains(text, "9 Uhr") {
				t.Errorf("German time missing: %q", text)
			}
			if c.otherLang(strings.ReplaceAll(text, c.tomorrowEvent, "")) {
				t.Errorf("words of the other language: %q", text)
			}
			fg.get(func(fg *fakeGoogle) {
				q := fg.listQueries[len(fg.listQueries)-1]
				if q.Get("timeMin") != "2026-11-07T00:00:00+01:00" || q.Get("timeMax") != "2026-11-08T00:00:00+01:00" {
					t.Errorf("tomorrow range %s – %s", q.Get("timeMin"), q.Get("timeMax"))
				}
			})

			// 3. Adding an event inserts it at the right time on Google.
			text, evs = chatSay(t, h, c.add)
			if meta := hasEvent(evs, "meta"); meta["src"] != "fast:calendar_add" {
				t.Fatalf("add meta %v: %q", meta, text)
			}
			title, start, end := insertedSpan(t, fg.lastInsert(t))
			if title != c.addTitle || start != "2026-11-07T15:00:00+01:00" || end != "2026-11-07T16:00:00+01:00" {
				t.Errorf("inserted %q %s – %s", title, start, end)
			}
			if !strings.HasPrefix(text, c.wantAdded) {
				t.Errorf("add reply %q", text)
			}
			if c.otherLang(strings.ReplaceAll(text, c.addTitle, "")) {
				t.Errorf("words of the other language: %q", text)
			}
			if card := hasEvent(evs, "card"); card == nil || card["kind"] != "calendar" {
				t.Errorf("add card %v", evs)
			} else if d, _ := card["data"].(map[string]any); d["added"] != true {
				t.Errorf("add card data %v", card["data"])
			}
		})
	}
}

func TestBatch2GreetingSmallTalkWithBriefingOff(t *testing.T) {
	for _, c := range e2eLangs {
		t.Run(c.voice, func(t *testing.T) {
			h, fg, _ := e2eEnv(t, c.voice)
			setBriefSettings(t, func(s *Settings) { s.BriefingOnGreeting = false })
			for _, text := range []string{c.plain, c.greet} {
				reply, evs := chatSay(t, h, text)
				meta := hasEvent(evs, "meta")
				if src, _ := meta["src"].(string); strings.HasPrefix(src, "fast:") {
					t.Errorf("%q taken by %s with the briefing off: %q", text, src, reply)
				}
				if strings.TrimSpace(reply) == "" {
					t.Errorf("%q: no small-talk answer", text)
				}
				if want := localAnswer(text, c.lang); want != "" && reply != want {
					t.Errorf("%q: reply %q, want the local answer %q", text, reply, want)
				}
				if c.otherLang(reply) {
					t.Errorf("%q: words of the other language in %q", text, reply)
				}
				if strings.Contains(reply, "Berlin") || strings.Contains(reply, "Grad") || strings.Contains(reply, "degrees") {
					t.Errorf("%q: a briefing leaked into small talk: %q", text, reply)
				}
			}
			if lists, _, _ := fg.counts(); lists != 0 {
				t.Errorf("small talk read the calendar %d times", lists)
			}
		})
	}
}
