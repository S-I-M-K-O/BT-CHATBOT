package hermes

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"
)

// briefing.go is Feature 2, the morning briefing (SPEC 9.2): "Guten Morgen,
// Hermes" (first greeting per person and day), "Morgenbriefing" / "brief
// me" at any time, and the router tool morning_briefing. The text is a
// deterministic template built from the registerBriefing sections, so no
// language model ever sees it and nothing personal leaves the machine. The
// weather and news calls (realtime.go) go to Open-Meteo, the Tagesschau and
// Google News with the city and the language only.
//
// Sections (order, owner): greeting (here, built in, always first),
// weather 20 (here), calendar
// 30 (calendar.go), reminders 40 (here), lists 50 (lists.go's helper,
// registered here), news 60 (here, only with the briefingNews setting).

// briefingSectionTimeout bounds every section; a slow one is left out.
var briefingSectionTimeout = 4 * time.Second

// Greeting window for the automatic briefing: 04:00–11:59 local time.
const (
	briefingMorningFrom = 4
	briefingMorningTo   = 12
)

// briefingPersonalSections are left out for speakers without Read access
// even if the section forgot to check (defence in depth, 4.10).
var briefingPersonalSections = wordSet("calendar")

type briefingArgs struct {
	// Mode: "greeting" (first morning greeting today), "again" (a later
	// greeting: greet and offer the overview), anything else = explicit.
	Mode string `json:"mode,omitempty"`
}

type briefingPart struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// ---------------------------------------------------------------------------
// Matching

// briefingGreetings are whole-utterance morning greetings (after fillers).
var briefingGreetings = wordSet(
	"guten morgen", "schönen guten morgen", "einen schönen guten morgen", "einen guten morgen",
	"moin", "moin moin", "morgähn",
	"good morning", "a good morning", "very good morning",
)

// briefingBareGreetings count only together with "hermes" ("Morgen,
// Hermes"); a bare "Morgen." is usually an answer to "wann?".
var briefingBareGreetings = wordSet("morgen", "morning", "mornin")

// briefingGreetFillers may surround a greeting.
var briefingGreetFillers = wordSet(
	"hermes", "hallo", "hey", "hi", "hello", "na", "oh", "so", "ja", "und", "and",
	"dir", "auch", "you", "too", "to", "zusammen", "allerseits", "everyone", "everybody", "all",
	"mein", "my", "lieber", "liebe", "dear", "friend", "freund", "buddy",
)

// briefingRequests are explicit requests; they work at any time.
var briefingRequests = []string{
	"morgenbriefing", "morgen briefing", "morgens briefing", "tagesbriefing", "lagebericht",
	"tagesüberblick", "tagesueberblick", "überblick für heute", "überblick über den tag",
	"wie sieht mein tag aus", "wie sieht mein tag heute aus", "wie sieht der tag aus", "wie sieht der tag heute aus",
	"was erwartet mich heute",
	"brief me", "morning briefing", "daily briefing", "my briefing", "the briefing", "daily brief",
	"how does my day look", "how does today look", "what does my day look like", "what does today look like",
	"how is my day looking", "how s my day looking",
}

// briefingDayRequests ask what is on today. With a calendar they belong to
// the calendar query (order 70); without one, the briefing answers them.
var briefingDayRequests = []string{
	"was steht heute an", "was liegt heute an", "was gibt s heute", "was gibts heute", "was steht an heute",
	"what s on today", "what is on today", "what s on for today", "what is on for today",
	"what s up today", "what s happening today", "what do i have today", "what have i got today",
}

// briefingRequestFillers may surround an explicit request without making it
// something else ("Ich schreibe einen Lagebericht" is not a request).
var briefingRequestFillers = wordSet(
	"hermes", "bitte", "mal", "please", "kannst", "könntest", "du", "can", "could", "you", "would",
	"gib", "gibst", "mir", "give", "me", "den", "das", "die", "dem", "deinen", "meinen", "mein", "meine",
	"einen", "ein", "a", "an", "the", "my", "your", "ich", "i", "möchte", "hätte", "gern", "gerne", "will",
	"want", "wollen", "like", "d", "lies", "vor", "read", "out", "starte", "start", "go", "with", "mit",
	"jetzt", "now", "heute", "today", "nochmal", "noch", "einmal", "again", "kurz", "kurzen", "quick",
	"short", "schnell", "hey", "hallo", "hi", "ok", "okay", "also", "so", "und", "and", "for", "für",
	"zum", "fürs", "guten", "morgen", "good", "morning", "uns", "us", "was", "what", "wie", "how",
)

func onlyFillers(tokens []string, phrases []string, fillers map[string]bool) bool {
	if len(tokens) == 0 || len(tokens) > 14 {
		return false
	}
	used := make([]bool, len(tokens))
	found := false
	for _, ph := range phrases {
		p := strings.Fields(ph)
		for i := range tokens {
			if phraseAt(tokens, i, p) {
				for j := range p {
					used[i+j] = true
				}
				found = true
			}
		}
	}
	if !found {
		return false
	}
	for i, tok := range tokens {
		if !used[i] && !fillers[tok] {
			return false
		}
	}
	return true
}

// isMorningGreeting: the whole utterance is a morning greeting.
func isMorningGreeting(tokens []string) bool {
	if len(tokens) == 0 || len(tokens) > 8 {
		return false
	}
	hermes := false
	var core []string
	for _, tok := range tokens {
		if tok == "hermes" {
			hermes = true
		}
		if !briefingGreetFillers[tok] {
			core = append(core, tok)
		}
	}
	c := strings.Join(core, " ")
	if briefingGreetings[c] {
		return true
	}
	return hermes && briefingBareGreetings[c]
}

func isBriefingRequest(tokens []string) bool {
	if onlyFillers(tokens, briefingRequests, briefingRequestFillers) {
		return true
	}
	return !calendarConnected() && onlyFillers(tokens, briefingDayRequests, briefingRequestFillers)
}

// briefingKey is who "first greeting today" is tracked for: the person, the
// single user, or this device for everybody else.
func briefingKey(t *Turn) string {
	switch {
	case t.Speaker.Known() && t.Speaker.Source != SourceSticky:
		return t.Speaker.ID
	case t.Speaker.ID == singleSpeakerID:
		return singleSpeakerID
	case t.ClientID != "":
		return "c:" + t.ClientID
	}
	return ""
}

func briefingDay(now time.Time) string { return now.Format("2006-01-02") }

func inMorningWindow(now time.Time) bool {
	h := now.Hour()
	return h >= briefingMorningFrom && h < briefingMorningTo
}

func matchBriefing(t *Turn) (json.RawMessage, bool) {
	if isBriefingRequest(t.Tokens) {
		return json.RawMessage(`{"mode":"explicit"}`), true
	}
	if t.Settings == nil || !t.Settings.BriefingOnGreeting || !isMorningGreeting(t.Tokens) {
		return nil, false // plain greetings fall through to the small-talk answer
	}
	now := turnNow(t)
	if !inMorningWindow(now) {
		return nil, false
	}
	if k := briefingKey(t); k != "" && briefingSeen(k) == briefingDay(now) {
		return json.RawMessage(`{"mode":"again"}`), true
	}
	return json.RawMessage(`{"mode":"greeting"}`), true
}

// ---------------------------------------------------------------------------
// Running

func runBriefing(t *Turn, raw json.RawMessage) Reply {
	var a briefingArgs
	_ = json.Unmarshal(raw, &a)
	now := turnNow(t)
	if a.Mode == "again" {
		q := briefingSalutation(t, now) + " " + pick(t.Lang,
			"Shall I give you the overview again?",
			"Soll ich dir den Überblick nochmal geben?")
		t.Ask(Pending{Kind: "confirm", Tool: "morning_briefing", Args: json.RawMessage(`{"mode":"explicit"}`),
			Resume: briefingConfirmResume}, q)
		return Reply{Text: q}
	}
	parts := composeBriefing(t)
	if k := briefingKey(t); k != "" {
		if err := markBriefingSeen(k, briefingDay(now)); err != nil {
			log.Printf("briefing: mark seen: %v", err)
		}
	}
	texts := make([]string, 0, len(parts))
	news := false
	for _, p := range parts {
		texts = append(texts, p.Text)
		if p.Name == "news" {
			news = true
		}
	}
	text := strings.Join(texts, " ")
	if news {
		// The question is already the last sentence of the news section.
		t.Ask(Pending{Kind: "confirm", Tool: "briefing_news_more", Resume: briefingConfirmResume},
			pick(t.Lang, "More headlines?", "Mehr Schlagzeilen?"))
	}
	return Reply{Text: text, Card: &Card{Kind: "briefing", Data: map[string]any{"sections": parts}}}
}

// briefingConfirmResume answers "Soll ich …?" / "Mehr Schlagzeilen?": yes
// runs the tool, no is acknowledged briefly, anything else is a new request.
func briefingConfirmResume(t *Turn, p *Pending) (Reply, bool) {
	switch yesNo(t.Tokens) {
	case 1:
		return runToolChecked(t, p.Tool, p.Args), true
	case -1:
		return Reply{Text: pick(t.Lang, "Alright.", "Alles klar.")}, true
	}
	return Reply{}, false
}

// composeBriefing runs every registered section in parallel, each on its own
// copy of the turn with a bounded context, and returns the greeting plus the
// non-empty texts in section order. Errors and timeouts only drop that
// section.
func composeBriefing(t *Turn) []briefingPart {
	sections := briefingSections()
	results := make([]string, len(sections))
	parent := t.Ctx
	if parent == nil {
		parent = context.Background()
	}
	var wg sync.WaitGroup
	for i, sec := range sections {
		if briefingPersonalSections[sec.name] && !t.Can(AccessRead) {
			continue
		}
		wg.Add(1)
		go func(i int, sec briefingEntry) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(parent, briefingSectionTimeout)
			defer cancel()
			tc := *t // sections read the turn; each gets its own context
			tc.Ctx = ctx
			tc.out = nil
			done := make(chan string, 1)
			go func() {
				defer func() {
					if p := recover(); p != nil {
						log.Printf("briefing: section %s panicked: %v", sec.name, p)
						done <- ""
					}
				}()
				s, err := sec.fn(&tc)
				if err != nil {
					log.Printf("briefing: section %s: %v", sec.name, err)
					s = ""
				}
				done <- s
			}()
			select {
			case s := <-done:
				results[i] = strings.TrimSpace(s)
			case <-ctx.Done():
				log.Printf("briefing: section %s timed out", sec.name)
			}
		}(i, sec)
	}
	wg.Wait()
	// The greeting is always first and needs no I/O. It is built in rather
	// than registered so registry order 10 stays free for other sections.
	g, _ := briefingGreeting(t)
	out := []briefingPart{{Name: "greeting", Text: g}}
	for i, sec := range sections {
		if results[i] != "" {
			out = append(out, briefingPart{Name: sec.name, Text: results[i]})
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Section 10: greeting, date and time

// briefingSpeakerName is the name to greet with: only for a speaker HERMES
// may treat as that person (a sticky or claimed-under-lock name is not
// spoken back).
func briefingSpeakerName(t *Turn) string {
	if t.Speaker.Known() && t.Can(AccessRead) {
		return safeTitle(t.Speaker.Name)
	}
	return ""
}

func briefingSalutation(t *Turn, now time.Time) string {
	var s string
	switch h := now.Hour(); {
	case h >= briefingMorningFrom && h < briefingMorningTo:
		s = pick(t.Lang, "Good morning", "Guten Morgen")
	case h >= 12 && h < 18:
		s = pick(t.Lang, "Good afternoon", "Guten Tag")
	default:
		s = pick(t.Lang, "Good evening", "Guten Abend")
	}
	if name := briefingSpeakerName(t); name != "" {
		s += ", " + name
	}
	return s + "."
}

func briefingGreeting(t *Turn) (string, error) {
	now := turnNow(t)
	clock := formatClock(now, t.Lang)
	var date string
	if t.Lang == "de" {
		date = fmt.Sprintf("Es ist %s, der %d. %s, %s.", weekdaysDE[now.Weekday()], now.Day(), monthsDE[now.Month()-1], clock)
	} else {
		date = fmt.Sprintf("It is %s, %s %d, %s.", now.Weekday(), now.Month(), now.Day(), clock)
	}
	return briefingSalutation(t, now) + " " + date, nil
}

// ---------------------------------------------------------------------------
// Section 20: weather for the home city (Open-Meteo, no key, fail soft)

func briefingWeather(t *Turn) (string, error) {
	city := ""
	if t.Settings != nil {
		city = strings.TrimSpace(t.Settings.HomeCity)
	}
	if city == "" {
		if cityHintShown() {
			return "", nil
		}
		if err := setCityHintShown(); err != nil {
			log.Printf("briefing: city hint: %v", err)
		}
		return pick(t.Lang,
			"For the weather, enter your home city in the settings.",
			"Für das Wetter trage in den Einstellungen deinen Wohnort ein."), nil
	}
	place, err := cachedGeocode(t.Ctx, city, t.Lang)
	if err != nil {
		return "", fmt.Errorf("geocode: %w", err)
	}
	f, err := fetchForecast(t.Ctx, place)
	if err != nil {
		return "", fmt.Errorf("forecast: %w", err)
	}
	return weatherSentence(t.Lang, place.name, f), nil
}

// degrees speaks a temperature: "6 Grad", "minus 2 Grad", "1 degree".
func degrees(v float64, lang string) string {
	n := int(math.Round(v)) // int(-0.0) is 0, so no "minus 0"
	if lang == "de" {
		if n < 0 {
			return fmt.Sprintf("minus %d Grad", -n)
		}
		return fmt.Sprintf("%d Grad", n)
	}
	unit := "degrees"
	if n == 1 || n == -1 {
		unit = "degree"
	}
	if n < 0 {
		return fmt.Sprintf("minus %d %s", -n, unit)
	}
	return fmt.Sprintf("%d %s", n, unit)
}

// wmoConditionDE is the German wording of a WMO weather code ("" unknown).
func wmoConditionDE(code int) string {
	switch code {
	case 0:
		return "klar"
	case 1:
		return "überwiegend klar"
	case 2:
		return "teils bewölkt"
	case 3:
		return "bedeckt"
	case 45, 48:
		return "neblig"
	case 51, 53, 55:
		return "Nieselregen"
	case 56, 57:
		return "gefrierender Nieselregen"
	case 61, 63, 65:
		return "Regen"
	case 66, 67:
		return "gefrierender Regen"
	case 71, 73, 75:
		return "Schnee"
	case 77:
		return "Schneegriesel"
	case 80, 81, 82:
		return "Regenschauer"
	case 85, 86:
		return "Schneeschauer"
	case 95:
		return "Gewitter"
	case 96, 99:
		return "Gewitter mit Hagel"
	}
	return ""
}

// ---------------------------------------------------------------------------
// Section 40: reminders today (household for everyone, own with Read)

func briefingReminders(t *Turn) (string, error) {
	now := turnNow(t)
	end := midnightOf(now).AddDate(0, 0, 1)
	rs := visibleReminders(t, AccessRead, func(r *Reminder) bool {
		// Meetings are the calendar section's job.
		return r.Status == reminderPending && r.Kind != reminderKindMeeting && r.Due.Before(end)
	})
	if len(rs) == 0 {
		return "", nil
	}
	line := reminderLine(rs[0], now, t.Lang)
	line = strings.TrimPrefix(line, pick(t.Lang, "today ", "heute "))
	if len(rs) == 1 {
		return sentence(pick(t.Lang, "One reminder today, ", "Heute eine Erinnerung, ") + line), nil
	}
	return sentence(fmt.Sprintf(pick(t.Lang, "%d reminders today, the next ", "Heute %d Erinnerungen, die nächste "), len(rs)) + line), nil
}

// ---------------------------------------------------------------------------
// Section 60: one news headline (only with briefingNews), then "Mehr
// Schlagzeilen?"

// briefingHeadlines fetches the top headlines in lang (realtime.go: the
// Tagesschau for German, Google News otherwise; nothing personal is sent).
func briefingHeadlines(ctx context.Context, lang string) ([]string, error) {
	hs, _, err := fetchHeadlines(ctx, "", lang)
	if err != nil {
		return nil, fmt.Errorf("news: %w", err)
	}
	out := make([]string, 0, len(hs))
	for _, h := range hs {
		out = append(out, h.Title)
	}
	return out, nil
}

func briefingNews(t *Turn) (string, error) {
	if t.Settings == nil || !t.Settings.BriefingNews {
		return "", nil
	}
	hs, err := briefingHeadlines(t.Ctx, t.Lang)
	if err != nil || len(hs) == 0 {
		return "", err
	}
	return sentence(pick(t.Lang, "Top headline: ", "Schlagzeile: ")+hs[0]) + " " +
		pick(t.Lang, "More headlines?", "Mehr Schlagzeilen?"), nil
}

// runNewsMore reads the next three headlines ("ja" to "Mehr Schlagzeilen?").
func runNewsMore(t *Turn, _ json.RawMessage) Reply {
	ctx, cancel := context.WithTimeout(t.Ctx, briefingSectionTimeout)
	defer cancel()
	hs, err := briefingHeadlines(ctx, t.Lang)
	if err != nil || len(hs) < 2 {
		if err != nil {
			log.Printf("briefing: %v", err)
		}
		return Reply{Text: pick(t.Lang, "I could not get more headlines right now.",
			"Weitere Schlagzeilen bekomme ich gerade nicht.")}
	}
	hs = hs[1:]
	if len(hs) > 3 {
		hs = hs[:3]
	}
	for i := range hs {
		hs[i] = sentence(hs[i])
	}
	return Reply{Text: pick(t.Lang, "More headlines: ", "Weitere Schlagzeilen: ") + strings.Join(hs, " ")}
}

// ---------------------------------------------------------------------------
// Registration

func init() {
	registerTool(Tool{
		Name:        "morning_briefing",
		Description: "Give the user their daily briefing: date, weather, appointments, reminders and lists for today.",
		Params: func(*Turn) json.RawMessage {
			return json.RawMessage(`{"type":"object","properties":{}}`)
		},
		Gate: []string{"morgenbriefing", "morgen briefing", "lagebericht", "tagesüberblick", "briefing"},
		Run:  runBriefing,
	})
	registerTool(Tool{Name: "briefing_news_more", Run: runNewsMore})
	registerFastPath(FastPath{Name: "briefing", Order: 60, Tool: "morning_briefing", Match: matchBriefing})
	registerStatus("briefing", briefingStatus)

	registerBriefing(20, "weather", briefingWeather)
	registerBriefing(40, "reminders", briefingReminders)
	registerBriefing(50, "lists", listsBriefing)
	registerBriefing(60, "news", briefingNews)
}

// briefingStatus is the /api/status entry: the switches and the sections in
// order (no personal data, no network call).
func briefingStatus(context.Context) any {
	s := loadSettings()
	names := []string{"greeting"}
	for _, sec := range briefingSections() {
		names = append(names, sec.name)
	}
	return map[string]any{
		"onGreeting":  s.BriefingOnGreeting,
		"news":        s.BriefingNews,
		"homeCity":    strings.TrimSpace(s.HomeCity) != "",
		"calendar":    calendarConnected(),
		"sections":    names,
		"morningFrom": briefingMorningFrom,
		"morningTo":   briefingMorningTo,
	}
}
