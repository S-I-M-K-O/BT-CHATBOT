package hermes

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// calendar.go: the calendar voice features (SPEC 9 Feature 3) on top of the
// Google client in gcal.go.
//
//   - Fast path 70: queries ("Was steht morgen an?", "What's on tomorrow?",
//     "Was habe ich am Freitag?", "wann ist mein nächster Termin") and adds
//     ("Trag morgen um 15 Uhr Zahnarzt ein", "add dentist tomorrow at 3pm to
//     my calendar"). Router tools calendar_query and calendar_add.
//   - Owner rule: the connected account belongs to one person (or the
//     household). Other known people hear "In Annas Kalender: …" or are
//     refused (setting calendarOthers); adding to someone else's calendar is
//     always confirmed first.
//   - Replies are templates (no LLM); every title goes through safeTitle.
//     Calendar text reaches a prompt only through contextBlock (data
//     delimiters) and never leaves the machine.
//   - Briefing section 30, personal-context section 31, GET /api/calendar/events.

const (
	calQueryMax     = 8                   // events named in a spoken answer
	calBriefingMax  = 3                   // events named in the briefing
	calDefaultDur   = 60 * time.Minute    // a timed event without a duration
	calMaxDur       = 24 * time.Hour      // longest spoken duration accepted
	calMinDur       = 5 * time.Minute     // shortest
	calNextHorizon  = 62 * 24 * time.Hour // "wann ist mein nächster Termin"
	calDefaultRange = 7                   // days for "was steht an?" without a day
	calFetchTimeout = 8 * time.Second     // voice queries and adds
	calBriefTimeout = 4 * time.Second     // briefing section
	calCtxTimeout   = 1500 * time.Millisecond
	calCacheTTL     = 5 * time.Minute // personal-context cache of today's events
	calTitleMax     = 200             // titles HERMES creates
	calOfferTTL     = 2 * time.Minute
	calQueryMaxToks = 24
	calAddMaxRunes  = 400
)

// ---------------------------------------------------------------------------
// Owner rule

type calRelation int

const (
	calOwn   calRelation = iota // the speaker's own (or the household's) calendar
	calOther                    // a known person who is not the owner
)

// calendarRelation tells whose calendar it is from the speaker's point of
// view. Single-user mode and a household calendar are always "own"; an
// owner who no longer exists counts as household.
func calendarRelation(t *Turn) (calRelation, string) {
	owner := calendarOwner()
	if owner == "" || t.Speaker.ID == singleSpeakerID || t.Speaker.ID == owner || !personExists(owner) {
		return calOwn, ""
	}
	name, ok := personName(owner)
	if !ok || safeTitle(name) == "" {
		return calOwn, ""
	}
	return calOther, safeTitle(name)
}

// genitive: "Annas", "Thomas'"; EN "Anna's".
func genitive(name, lang string) string {
	if lang != "de" {
		return name + "'s"
	}
	lower := strings.ToLower(name)
	for _, suf := range []string{"s", "ß", "x", "z", "ce"} {
		if strings.HasSuffix(lower, suf) {
			return name + "'"
		}
	}
	return name + "s"
}

func calOthersRefuse(t *Turn) bool {
	return t.Settings != nil && t.Settings.CalendarOthers == "refuse"
}

func calRefuseOther(t *Turn, name string, write bool) Reply {
	if write {
		return Reply{Text: pick(t.Lang,
			"That is "+genitive(name, "en")+" calendar; only "+name+" can add to it.",
			"Das ist "+genitive(name, "de")+" Kalender; da trage ich nur für "+name+" etwas ein.")}
	}
	return Reply{Text: pick(t.Lang,
		"That is "+genitive(name, "en")+" calendar; I only read it to "+name+".",
		"Das ist "+genitive(name, "de")+" Kalender; den lese ich nur "+name+" vor.")}
}

// ---------------------------------------------------------------------------
// Errors spoken to the user

func calNotConnectedText(lang string) string {
	return pick(lang,
		"The calendar is not connected. Connect it in Settings under Calendar.",
		"Der Kalender ist nicht verbunden. Verbinde ihn in den Einstellungen unter Kalender.")
}

func calRelinkText(lang string) string {
	return pick(lang,
		"The calendar connection has expired. Please connect the calendar again in Settings under Calendar.",
		"Die Kalenderverbindung ist abgelaufen. Verbinde den Kalender bitte in den Einstellungen unter Kalender neu.")
}

func calErrorReply(t *Turn, err error) Reply {
	switch {
	case errors.Is(err, errCalendarNotConnected):
		return Reply{Text: calNotConnectedText(t.Lang)}
	case errors.Is(err, errCalendarRelink):
		return Reply{Text: calRelinkText(t.Lang), Error: true}
	case errors.Is(err, errCalendarNoClient):
		return Reply{Text: pick(t.Lang,
			"The Google access for the calendar is missing. Enter it in Settings under Calendar.",
			"Der Google-Zugang für den Kalender fehlt. Trag ihn in den Einstellungen unter Kalender ein."), Error: true}
	case errors.Is(err, errStoreReadOnly):
		return Reply{Text: pick(t.Lang,
			"The calendar data was written by a newer HERMES; I can't change it.",
			"Die Kalenderdaten stammen von einem neueren HERMES; ich kann sie nicht ändern."), Error: true}
	}
	log.Printf("calendar: %v", err)
	return Reply{Text: pick(t.Lang,
		"I can't reach the calendar right now. Please try again later.",
		"Ich erreiche den Kalender gerade nicht. Versuch es bitte später noch einmal."), Error: true}
}

// calFeatureOn: the calendar was set up at least partly (a Google client is
// configured, or a token exists). Without either, the fast paths stay out of
// the way and HERMES does not talk about a calendar the user never wanted.
func calFeatureOn() bool {
	if gcalConfigured() {
		return true
	}
	_, ok := gcalToken()
	return ok
}

// calReady: the calendar is connected and usable (no network call).
func calReady() error {
	st := gcalCurrentState()
	switch {
	case st.NeedsRelink:
		return errCalendarRelink
	case !st.Connected:
		return errCalendarNotConnected
	}
	return nil
}

// ---------------------------------------------------------------------------
// Speaking events

// calDayPhrase speaks a day: "heute", "morgen (Samstag)", "am Montag, 9.
// November"; EN "today", "tomorrow (Saturday)", "on Monday, November 9".
func calDayPhrase(day, now time.Time, lang string) string {
	day = day.In(now.Location())
	switch calendarDaysBetween(now, day) {
	case 0:
		return pick(lang, "today", "heute")
	case 1:
		return pick(lang, "tomorrow", "morgen")
	case 2:
		if lang == "de" {
			return "übermorgen"
		}
	}
	if lang == "de" {
		s := fmt.Sprintf("am %s, %d. %s", weekdaysDE[day.Weekday()], day.Day(), monthsDE[day.Month()-1])
		if day.Year() != now.Year() {
			s += fmt.Sprintf(" %d", day.Year())
		}
		return s
	}
	s := fmt.Sprintf("on %s, %s %d", day.Weekday(), day.Month(), day.Day())
	if day.Year() != now.Year() {
		s += fmt.Sprintf(", %d", day.Year())
	}
	return s
}

// calTitle is an event title safe to speak.
func calTitle(ev CalendarEvent, lang string) string {
	title := safeTitle(ev.Title)
	if title == "" {
		title = pick(lang, "an untitled event", "ein Termin ohne Titel")
	}
	if ev.NeedsAction {
		title = pick(lang, "(invitation) ", "(Einladung) ") + title
	}
	return title
}

// calItem speaks one event. withDay adds the day for multi-day answers.
func calItem(ev CalendarEvent, now time.Time, lang string, withDay bool) string {
	title := calTitle(ev, lang)
	start := ev.Start.In(now.Location())
	if ev.AllDay {
		if withDay && !start.After(midnightOf(now)) && ev.End.After(now) {
			withDay = false // already running: "ganztägig Urlaub"
		}
		if withDay {
			return pick(lang, title+" "+calDayPhrase(start, now, lang)+" (all day)",
				calDayPhrase(start, now, lang)+" ganztägig "+title)
		}
		return pick(lang, title+" (all day)", "ganztägig "+title)
	}
	if withDay {
		return pick(lang, title+" "+formatWhen(start, now, lang), formatWhen(start, now, lang)+" "+title)
	}
	clock := formatClock(start, lang)
	if calendarDaysBetween(now, start) < 0 { // started before this day (multi-day event)
		since := strings.TrimPrefix(strings.TrimPrefix(calDayPhrase(start, now, lang), "am "), "on ")
		return pick(lang, title+" (since "+since+")", title+" (seit "+since+")")
	}
	return pick(lang, title+" at "+clock, "um "+clock+" "+title)
}

// calItems lists at most max events plus "und 3 weitere".
func calItems(evs []CalendarEvent, now time.Time, lang string, withDay bool, max int, join bool) string {
	var items []string
	for i, ev := range evs {
		if i == max {
			break
		}
		items = append(items, calItem(ev, now, lang, withDay))
	}
	more := len(evs) - len(items)
	if join && more == 0 {
		return joinAnd(items, lang)
	}
	s := strings.Join(items, ", ")
	if more > 0 {
		s += pick(lang, fmt.Sprintf(" and %d more", more), fmt.Sprintf(" und %d weitere", more))
	}
	return s
}

func calCount(n int, lang string) string {
	if n == 1 {
		return pick(lang, "one event", "einen Termin")
	}
	return pick(lang, fmt.Sprintf("%d events", n), fmt.Sprintf("%d Termine", n))
}

func capFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// calRangeLabel speaks a query range: a day phrase for one day, else
// "von Freitag bis Sonntag" / "from Friday to Sunday".
func calRangeLabel(from, to, now time.Time, lang string) (label string, oneDay bool) {
	loc := now.Location()
	from, to = from.In(loc), to.In(loc)
	last := to.Add(-time.Nanosecond)
	if calendarDaysBetween(from, last) == 0 {
		return calDayPhrase(from, now, lang), true
	}
	dayName := func(d time.Time) string {
		if calendarDaysBetween(now, d) == 0 {
			return pick(lang, "today", "heute")
		}
		if calendarDaysBetween(now, d) > 6 || calendarDaysBetween(now, d) < 0 {
			if lang == "de" {
				return fmt.Sprintf("%d. %s", d.Day(), monthsDE[d.Month()-1])
			}
			return fmt.Sprintf("%s %d", d.Month(), d.Day())
		}
		if lang == "de" {
			return weekdaysDE[d.Weekday()]
		}
		return d.Weekday().String()
	}
	return pick(lang, "from "+dayName(from)+" to "+dayName(last), "von "+dayName(from)+" bis "+dayName(last)), false
}

// calQueryText is the spoken answer to a calendar question (template).
func calQueryText(evs []CalendarEvent, from, to, now time.Time, lang string, rel calRelation, owner string) string {
	label, oneDay := calRangeLabel(from, to, now, lang)
	list := calItems(evs, now, lang, !oneDay, calQueryMax, false)
	if rel == calOther {
		if len(evs) == 0 {
			return pick(lang, "Nothing is in "+genitive(owner, "en")+" calendar "+label+".",
				"In "+genitive(owner, "de")+" Kalender ist "+label+" nichts eingetragen.")
		}
		return pick(lang, "In "+genitive(owner, "en")+" calendar "+label+": "+list+".",
			"In "+genitive(owner, "de")+" Kalender steht "+label+": "+list+".")
	}
	if len(evs) == 0 {
		return pick(lang, capFirst(label)+" there is nothing on your calendar.", capFirst(label)+" ist nichts eingetragen.")
	}
	return pick(lang, capFirst(label)+" you have "+calCount(len(evs), lang)+": "+list+".",
		capFirst(label)+" hast du "+calCount(len(evs), lang)+": "+list+".")
}

// calNextText answers "wann ist mein nächster Termin".
func calNextText(ev *CalendarEvent, now time.Time, lang string, rel calRelation, owner string) string {
	if ev == nil {
		if rel == calOther {
			return pick(lang, "There is nothing in "+genitive(owner, "en")+" calendar for the next weeks.",
				"In "+genitive(owner, "de")+" Kalender steht in den nächsten Wochen nichts.")
		}
		return pick(lang, "There is nothing on your calendar for the next weeks.", "In den nächsten Wochen ist nichts eingetragen.")
	}
	item := calItem(*ev, now, lang, true)
	if rel == calOther {
		return pick(lang, "The next event in "+genitive(owner, "en")+" calendar: "+item+".",
			"Der nächste Termin in "+genitive(owner, "de")+" Kalender: "+item+".")
	}
	return pick(lang, "Your next event: "+item+".", "Dein nächster Termin: "+item+".")
}

// calCardEvents prepares events for a card: titles cleaned, times in loc.
func calCardEvents(evs []CalendarEvent, loc *time.Location) []CalendarEvent {
	out := make([]CalendarEvent, 0, len(evs))
	for _, ev := range evs {
		ev.Title = safeTitle(ev.Title)
		ev.Location = safeTitle(ev.Location)
		ev.Start, ev.End = ev.Start.In(loc), ev.End.In(loc)
		out = append(out, ev)
	}
	return out
}

func calCard(from, to time.Time, evs []CalendarEvent, loc *time.Location, extra map[string]any) *Card {
	data := map[string]any{
		"from": from.In(loc).Format(time.RFC3339), "to": to.In(loc).Format(time.RFC3339),
		"events": calCardEvents(evs, loc),
	}
	for k, v := range extra {
		data[k] = v
	}
	return &Card{Kind: "calendar", Data: data}
}

// ---------------------------------------------------------------------------
// Fetching

// calFetch lists events with a timeout, dropping ones that already ended
// when the range starts in the past ("heute" means what is still ahead).
func calFetch(ctx context.Context, from, to, now time.Time, timeout time.Duration) ([]CalendarEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	evs, err := fetchCalendarEvents(cctx, from, to)
	if err != nil {
		return nil, err
	}
	if from.After(now) {
		return evs, nil
	}
	out := evs[:0:0]
	for _, ev := range evs {
		if ev.AllDay || ev.End.After(now) {
			out = append(out, ev)
		}
	}
	return out, nil
}

// Today's events for the personal context, cached so persona turns do not
// wait for Google on every message.
var calCache = struct {
	sync.Mutex
	day  string
	at   time.Time
	evs  []CalendarEvent
	fail time.Time // last failed fetch: back off for a minute
	gen  uint64    // bumped by every clear; a fetch from an older gen is not stored
}{}

func calendarCacheClear() {
	calCache.Lock()
	calCache.day, calCache.evs, calCache.at, calCache.fail = "", nil, time.Time{}, time.Time{}
	calCache.gen++
	calCache.Unlock()
}

func calTodayCached(ctx context.Context, now time.Time) ([]CalendarEvent, bool) {
	day := now.Format("2006-01-02") + "|" + now.Location().String()
	calCache.Lock()
	if calCache.day == day && now.Sub(calCache.at) < calCacheTTL && !calCache.at.After(now) {
		evs := append([]CalendarEvent(nil), calCache.evs...)
		calCache.Unlock()
		return evs, true
	}
	if !calCache.fail.IsZero() && now.Sub(calCache.fail) < time.Minute && !calCache.fail.After(now) {
		calCache.Unlock()
		return nil, false
	}
	gen := calCache.gen
	calCache.Unlock()
	start := midnightOf(now)
	evs, err := calFetch(ctx, start, start.AddDate(0, 0, 1), now, calCtxTimeout)
	calCache.Lock()
	defer calCache.Unlock()
	if calCache.gen != gen {
		// The calendar changed while Google answered (disconnect, re-link,
		// insert): this answer may be stale or another account's.
		return nil, false
	}
	if err != nil {
		calCache.fail = now
		return nil, false
	}
	calCache.day, calCache.at, calCache.evs, calCache.fail = day, now, evs, time.Time{}
	return append([]CalendarEvent(nil), evs...), true
}

// ---------------------------------------------------------------------------
// Matching: queries

var (
	// calNouns make an utterance about the calendar.
	calNouns = wordSet("termin", "termine", "terminen", "kalender", "terminkalender", "kalendar",
		"calendar", "appointment", "appointments", "meeting", "meetings", "schedule", "events", "event",
		"verabredung", "verabredungen", "besprechung", "besprechungen")
	// calQueryWords may appear in a calendar question besides the day words
	// and calNouns; anything else ("Zeitung", "gegessen") means it is not one.
	calQueryWords = wordSet(
		// German
		"was", "steht", "stehen", "an", "habe", "hab", "hast", "ich", "wir", "haben", "du", "für", "fuer",
		"noch", "so", "alles", "denn", "eigentlich", "mal", "bitte", "mir", "uns", "sag", "sage", "zeig", "zeige",
		"lies", "vor", "irgendwelche", "irgendwas", "welche", "in", "im", "meinem", "mein", "meine", "meinen",
		"unserem", "unser", "unsere", "ist", "sind", "gibt", "es", "auf", "dem", "programm", "nächster",
		"nächste", "nächsten", "naechster", "naechste", "wann", "der", "die", "das", "den", "eine", "einen", "ein",
		"heute", "morgen", "übermorgen", "los", "geplant", "drin", "eingetragen", "kommt", "als", "nächstes",
		"hermes", "kannst", "jetzt", "dann", "wie", "sieht", "aus", "da", "etwas", "überhaupt",
		// English
		"what", "whats", "s", "is", "on", "do", "does", "i", "have", "my", "the", "for", "got", "anything", "there",
		"are", "any", "scheduled", "planned", "coming", "up", "next", "when", "tell", "me", "show", "read", "please",
		"how", "look", "looks", "like", "plans", "today", "tomorrow", "a", "an", "can", "you", "we", "our",
		"am", "it", "this", "at",
	)
	calNextNouns = wordSet("termin", "appointment", "meeting", "event", "besprechung", "verabredung")
)

type calQueryArgs struct {
	WhenText string `json:"when_text,omitempty"`
	Next     bool   `json:"next,omitempty"`
	Fast     bool   `json:"fast,omitempty"`
}

// removeTokens drops the first occurrence of the token sequence sub.
func removeTokens(toks, sub []string) []string {
	if len(sub) == 0 {
		return toks
	}
	for i := 0; i+len(sub) <= len(toks); i++ {
		if phraseAt(toks, i, sub) {
			return append(append([]string(nil), toks[:i]...), toks[i+len(sub):]...)
		}
	}
	return toks
}

// matchCalendarQuery is fast path 70 (read): an explicit calendar question.
// Every word must be calendar-question vocabulary or part of the day phrase,
// so "Was steht heute in der Zeitung?" never matches.
func matchCalendarQuery(t *Turn) (json.RawMessage, bool) {
	if !calFeatureOn() {
		return nil, false
	}
	q := stripCommand(t.Query)
	toks := tokenize(q)
	if len(toks) == 0 || len(toks) > calQueryMaxToks {
		return nil, false
	}
	// "wann ist mein nächster Termin" / "what's my next appointment"
	if (hasAnyPhrase(toks, []string{"nächster", "naechster", "nächste", "next"})) && hasAnyWord(toks, calNextNouns) &&
		(hasAnyWord(toks, wordSet("wann", "was", "when", "what", "whats", "welcher")) || isQuestion(t)) {
		for _, w := range toks {
			if !calQueryWords[w] && !calNouns[w] && w != "welcher" {
				return nil, false
			}
		}
		b, _ := json.Marshal(calQueryArgs{Next: true, Fast: true})
		return b, true
	}
	from, _, label, dayOK := parseDayRange(q, turnNow(t), turnLoc(t))
	rest := toks
	if dayOK {
		rest = removeTokens(toks, tokenize(label))
	}
	// Cues are read on the whole utterance: a day label may swallow a word
	// of them ("What's on tomorrow" gives the label "on tomorrow").
	noun := hasAnyWord(toks, calNouns)
	steht := hasCue(toks, "was steht … an") || hasCue(toks, "was stehen … an") || hasCue(toks, "was steht … auf dem programm") || hasCue(toks, "was ist … los")
	whatsOn := hasAnyPhrase(toks, []string{"what s on", "whats on", "what is on", "what s happening", "what is happening"})
	haveDay := dayOK && hasAnyPhrase(toks, []string{"was habe ich", "was hab ich", "was haben wir", "what do i have", "what have i got", "what do we have", "habe ich was", "hab ich was", "do i have anything"})
	switch {
	case noun:
	case steht:
	case whatsOn && dayOK:
	case haveDay:
	default:
		return nil, false
	}
	// "Ich hab morgen um 8 einen Zahnarzttermin" is a statement, not a
	// question: every remaining word must be question vocabulary.
	for _, w := range rest {
		if !calQueryWords[w] && !calNouns[w] {
			return nil, false
		}
	}
	// A bare noun needs a question shape ("Kalender" alone is no request).
	if noun && !steht && !whatsOn && !haveDay && !isQuestion(t) &&
		!hasAnyWord(rest, wordSet("zeig", "zeige", "lies", "show", "read", "tell", "sag", "sage")) {
		return nil, false
	}
	if dayOK && from.Before(midnightOf(turnNow(t))) {
		return nil, false // "was hatte ich gestern": no past read-outs by voice
	}
	a := calQueryArgs{Fast: true}
	if dayOK {
		a.WhenText = label
	}
	b, _ := json.Marshal(a)
	return b, true
}

// calQueryRange resolves the range of a query; the default is the next
// seven days.
func calQueryRange(t *Turn, a calQueryArgs) (from, to time.Time) {
	now, loc := turnNow(t), turnLoc(t)
	if f, tt, _, ok := parseDayRange(a.WhenText, now, loc); ok && tt.After(now) {
		return f, tt
	}
	if !a.Fast {
		if f, tt, _, ok := parseDayRange(stripCommand(t.Query), now, loc); ok && tt.After(now) {
			return f, tt
		}
	}
	start := midnightOf(now)
	return start, start.AddDate(0, 0, calDefaultRange)
}

func runCalendarQuery(t *Turn, raw json.RawMessage) Reply {
	var a calQueryArgs
	_ = json.Unmarshal(raw, &a)
	if err := calReady(); err != nil {
		return calErrorReply(t, err)
	}
	rel, owner := calendarRelation(t)
	if rel == calOther && calOthersRefuse(t) {
		return calRefuseOther(t, owner, false)
	}
	now, loc := turnNow(t), turnLoc(t)
	if !a.Next && !a.Fast {
		a.Next = hasAnyWord(tokenize(t.Query), wordSet("nächster", "naechster", "next")) && hasAnyWord(tokenize(t.Query), calNextNouns)
	}
	if a.Next {
		evs, err := calFetch(t.Ctx, now, now.Add(calNextHorizon), now, calFetchTimeout)
		if err != nil {
			return calErrorReply(t, err)
		}
		var next *CalendarEvent
		for i := range evs {
			if evs[i].Start.After(now) || (evs[i].AllDay && !evs[i].Start.Before(midnightOf(now).AddDate(0, 0, 1))) {
				next = &evs[i]
				break
			}
		}
		var shown []CalendarEvent
		if next != nil {
			shown = []CalendarEvent{*next}
		}
		return Reply{Text: calNextText(next, now, t.Lang, rel, owner), Card: calCard(now, now.Add(calNextHorizon), shown, loc, nil)}
	}
	from, to := calQueryRange(t, a)
	evs, err := calFetch(t.Ctx, from, to, now, calFetchTimeout)
	if err != nil {
		return calErrorReply(t, err)
	}
	return Reply{Text: calQueryText(evs, from, to, now, t.Lang, rel, owner), Card: calCard(from, to, evs, loc, nil)}
}

// ---------------------------------------------------------------------------
// Matching: adds

type calAddArgs struct {
	Title       string `json:"title,omitempty"`
	WhenText    string `json:"when_text,omitempty"`
	DurationMin int    `json:"duration_min,omitempty"`
	AllDay      bool   `json:"all_day,omitempty"`
	Fast        bool   `json:"fast,omitempty"` // title and time come from the utterance
}

var (
	// calAddCues are the explicit add commands of the fast path.
	calAddCues = []string{
		"in den kalender", "in meinen kalender", "in unseren kalender", "in den terminkalender", "zum kalender",
		"im kalender eintragen", "neuer termin", "neuen termin", "neue termin", "einen termin an",
		"termin anlegen", "termin eintragen", "mach einen termin", "mache einen termin", "trag … termin",
		// English adds name the verb, so "what's on my calendar" stays a question.
		"add … to my calendar", "add … to the calendar", "add … to our calendar", "add … to calendar",
		"put … on my calendar", "put … on the calendar", "put … in my calendar", "put … into my calendar",
		"put … onto my calendar", "put … on our calendar", "enter … in my calendar", "enter … into my calendar",
		"schedule a", "schedule an", "create an event",
		"create a calendar event", "create an appointment", "make an appointment", "new event", "new appointment",
		"add an event", "add an appointment",
	}
	// calImperatives: a router call for calendar_add runs directly only with
	// one of these (SPEC 9.0); otherwise HERMES offers first.
	calImperatives = []string{"trag … ein", "trage … ein", "eintragen", "leg … an", "lege … an", "neuer termin",
		"neuen termin", "mach einen termin", "mache einen termin", "add … to", "add … calendar", "put … on", "put … calendar",
		"schedule a", "schedule an", "create an event", "create a calendar event", "book"}
	calGate = []string{"termin", "termine", "kalender", "eintragen", "calendar", "appointment", "appointments",
		"my schedule", "what s on", "whats on", "what is on"}

	reCalTragEin = regexp.MustCompile(`(?i)^(?:trag|trage)\s+.+\s+ein$`)
	reCalAllDay  = regexp.MustCompile(`(?i)(?:^|[\s,;:!?.]+)(?:ganztägig|ganztags|den\s+ganzen\s+tag|all[\s-]day|the\s+whole\s+day)(?:[\s,;:!?.]+|$)`)
	// "für 2 Stunden", "for 30 minutes", "für eine halbe Stunde", "for an
	// hour and a half": cut out before the title is read.
	reCalDuration = regexp.MustCompile(`(?i)(?:^|[\s,;:]+)(?:für|fuer|for)\s+(?:(?:eine|ein|einer|an|a|one)\s+)?(?:(halbe\s+stunde|half\s+(?:an\s+)?hour|anderthalb\s+stunden|eineinhalb\s+stunden|dreiviertel\s+stunde|viertelstunde|quarter\s+of\s+an\s+hour)|(\d+(?:[.,]\d+)?|[\p{L}]+)?\s*(stunden?|std\.?|minuten?|min\.?|hours?|hrs?|h|minutes?|mins?)(?:\s+(?:und\s+)?(?:eine\s+halbe|a\s+half|and\s+a\s+half|halb))?)(?:\s+lang)?(?:[\s,;:.!?]+|$)`)
	reCalStrip    = stripAlt(
		`(?:hey\s+)?hermes`, `bitte`, `please`, `mal`, `doch`, `noch`, `danke`,
		`(?:kannst|könntest|würdest)\s+du(?:\s+(?:mir|uns))?`, `(?:can|could|would)\s+you`,
		`(?:trag|trage)(?:\s+(?:mir|uns))?`, `eintragen`, `einen\s+termin\s+an`,
		`(?:in|zu)\s+(?:den|meinen|unseren|meinem|unserem)\s+(?:termin)?kalender`, `im\s+kalender`, `zum\s+kalender`, `in\s+den\s+kalender`,
		`(?:leg|lege|mach|mache|erstell|erstelle)\s+(?:mir\s+|uns\s+)?(?:einen\s+)?(?:neuen\s+)?termin(?:\s+an)?`,
		`(?:ein\s+)?(?:neuer|neuen|neue)\s+termin`, `termin\s+(?:anlegen|eintragen)`,
		`(?:add|put|schedule|create|make|book|enter)(?:\s+(?:me|us))?`, `(?:a|an)\s+(?:new\s+)?(?:calendar\s+)?(?:event|appointment)(?:\s+for)?`,
		`new\s+(?:event|appointment)`, `(?:to|on|in|into|onto)\s+(?:my|the|our)\s+calendar`, `to\s+calendar`,
		`ganztägig`, `ganztags`, `den\s+ganzen\s+tag`, `all[\s-]day`, `the\s+whole\s+day`,
	)
	// Articles and prepositions before the title; whole words only, so
	// "dentist" keeps its "den".
	reCalTitleLead = regexp.MustCompile(`(?i)^(?:(?:der|die|das|den|dem|einen|eine|ein|the|a|an|für|fuer|for)(?:\s+|$)|[:\-–]\s*)+`)
	// A title that only says "appointment" is no title.
	calEmptyTitles = wordSet("termin", "einen termin", "ein termin", "event", "an event", "appointment", "an appointment", "kalender", "calendar")
	reCalTitleTail = regexp.MustCompile(`(?i)(?:\s+(?:ein|an|eintragen|for|für|on|in|at|um|am))+$`)
)

// calDurationOf reads "für 2 Stunden" / "for 30 minutes"; 0 when absent.
func calDurationOf(text string) (time.Duration, string) {
	m := reCalDuration.FindStringSubmatchIndex(text)
	if m == nil {
		return 0, text
	}
	sub := func(i int) string {
		if m[2*i] < 0 {
			return ""
		}
		return strings.ToLower(text[m[2*i]:m[2*i+1]])
	}
	whole := strings.ToLower(text[m[0]:m[1]])
	var d time.Duration
	switch fixed := sub(1); {
	case strings.HasPrefix(fixed, "halbe") || strings.HasPrefix(fixed, "half"):
		d = 30 * time.Minute
	case strings.HasPrefix(fixed, "anderthalb") || strings.HasPrefix(fixed, "eineinhalb"):
		d = 90 * time.Minute
	case strings.HasPrefix(fixed, "dreiviertel"):
		d = 45 * time.Minute
	case strings.HasPrefix(fixed, "viertel") || strings.HasPrefix(fixed, "quarter"):
		d = 15 * time.Minute
	default:
		n := 1.0
		if num := sub(2); num != "" {
			v, ok := whenNumber(num)
			if !ok {
				if f, err := strconv.ParseFloat(strings.ReplaceAll(num, ",", "."), 64); err == nil {
					v, ok = f, true
				}
			}
			if !ok {
				return 0, text
			}
			n = v
		}
		unit := sub(3)
		if strings.HasPrefix(unit, "min") {
			d = time.Duration(n * float64(time.Minute))
		} else {
			d = time.Duration(n * float64(time.Hour))
			if strings.Contains(whole, "halb") || strings.Contains(whole, "half") {
				d += 30 * time.Minute
			}
		}
	}
	if d < calMinDur || d > calMaxDur {
		return 0, text
	}
	return d, strings.TrimSpace(text[:m[0]] + " " + text[m[1]:])
}

// calTitleFrom turns what is left of an add command into the title.
func calTitleFrom(rest string) string {
	s := " " + rest + " "
	for i := 0; i < 8; i++ {
		n := reCalStrip.ReplaceAllString(s, " ")
		if n == s {
			break
		}
		s = n
	}
	s = strings.Join(strings.Fields(s), " ")
	for i := 0; i < 3; i++ {
		s = strings.Trim(s, " ,;:.!?-–—")
		s = reCalTitleLead.ReplaceAllString(s, "")
		s = reCalTitleTail.ReplaceAllString(s, "")
	}
	s = strings.Trim(s, " ,;:.!?-–—\"'„“”")
	if calEmptyTitles[strings.ToLower(s)] {
		return ""
	}
	return capFirst(cleanText(s, calTitleMax))
}

// matchCalendarAdd is fast path 70 (write): an explicit add command.
func matchCalendarAdd(t *Turn) (json.RawMessage, bool) {
	if !calFeatureOn() {
		return nil, false
	}
	q := stripCommand(t.Query)
	if q == "" || utf8.RuneCountInString(q) > calAddMaxRunes {
		return nil, false
	}
	toks := tokenize(q)
	core := strings.TrimRight(q, " .!?")
	tragEin := reCalTragEin.MatchString(core)
	if !tragEin && !hasAnyCue(toks, calAddCues) {
		return nil, false
	}
	// "Soll ich einen neuen Termin machen?" / questions are not commands.
	if strings.HasSuffix(strings.TrimSpace(t.Query), "?") && !hasAnyCue(toks, []string{"kannst du", "könntest du", "can you", "could you", "would you"}) {
		return nil, false
	}
	a := calAddArgs{Fast: true}
	text := q
	if d, rest := calDurationOf(text); d > 0 {
		a.DurationMin = int(d / time.Minute)
		text = rest
	}
	if reCalAllDay.MatchString(text) {
		a.AllDay = true
	}
	a.WhenText = text
	rest := text
	if w, ok := calParseWhen(text, turnNow(t), turnLoc(t)); ok {
		rest = w.Rest
	}
	a.Title = calTitleFrom(rest)
	b, _ := json.Marshal(a)
	return b, true
}

// calParseWhen reads the event time. A day without a clock is an all-day
// event; "heute" alone (whose 9:00 default may have passed) is today.
func calParseWhen(text string, now time.Time, loc *time.Location) (WhenResult, bool) {
	if strings.TrimSpace(text) == "" {
		return WhenResult{}, false
	}
	// A confirmed router plan carries its absolute start (RFC 3339).
	if at, err := time.Parse(time.RFC3339, strings.TrimSpace(text)); err == nil {
		at = at.In(loc)
		if at.Before(midnightOf(now.In(loc))) || at.Sub(now) >= whenMaxAhead {
			return WhenResult{}, false
		}
		return WhenResult{At: at, HasClock: true, HasDay: true}, true
	}
	if w, ok := parseWhen(text, now, loc); ok {
		return w, true
	}
	start := midnightOf(now.In(loc))
	if w, ok := parseWhen(text, start.Add(-time.Second), loc); ok && !w.HasClock && w.Duration == 0 &&
		calendarDaysBetween(start, w.At) == 0 {
		w.At = start
		w.DefaultedClock = true
		return w, true
	}
	return WhenResult{}, false
}

// calAddWhen resolves the time of an add from the args, the title and (for
// the router) the user's own words.
func calAddWhen(t *Turn, a *calAddArgs) (WhenResult, bool) {
	now, loc := turnNow(t), turnLoc(t)
	if w, ok := calParseWhen(a.WhenText, now, loc); ok {
		return w, true
	}
	if a.Fast {
		return WhenResult{}, false
	}
	if w, ok := calParseWhen(a.Title, now, loc); ok {
		a.Title = calTitleFrom(w.Rest)
		return w, true
	}
	if t.Query != "" {
		q := stripCommand(t.Query)
		if d, rest := calDurationOf(q); d > 0 {
			q = rest
		}
		if w, ok := calParseWhen(q, now, loc); ok {
			if a.Title == "" {
				a.Title = calTitleFrom(w.Rest)
			}
			return w, true
		}
	}
	return WhenResult{}, false
}

// calAddPlan is a fully resolved add.
type calAddPlan struct {
	Title  string
	Start  time.Time
	Dur    time.Duration
	AllDay bool
}

// calResolveAdd turns args into a plan; missing names the missing slot.
func calResolveAdd(t *Turn, a calAddArgs) (plan calAddPlan, missing string) {
	if !a.Fast {
		a.Title = cleanText(a.Title, calTitleMax)
		if a.DurationMin <= 0 {
			if d, rest := calDurationOf(a.Title); d > 0 {
				a.DurationMin, a.Title = int(d/time.Minute), rest
			}
		}
	}
	w, ok := calAddWhen(t, &a)
	title := a.Title
	if !a.Fast {
		if w2, ok2 := calParseWhen(title, turnNow(t), turnLoc(t)); ok2 {
			title = w2.Rest
		}
		title = calTitleFrom(title)
	}
	if !ok {
		return calAddPlan{Title: title}, "when"
	}
	if title == "" {
		return calAddPlan{Start: w.At}, "title"
	}
	plan = calAddPlan{Title: title, Start: w.At}
	switch {
	case a.AllDay:
		plan.AllDay = true
		plan.Start = midnightOf(w.At)
	case !w.HasClock && w.Duration == 0:
		plan.AllDay = true // "morgen", "am Freitag": all day (SPEC 9.3)
		plan.Start = midnightOf(w.At)
	default:
		plan.Dur = calDefaultDur
		if a.DurationMin > 0 {
			if d := time.Duration(a.DurationMin) * time.Minute; d >= calMinDur && d <= calMaxDur {
				plan.Dur = d
			}
		}
	}
	return plan, ""
}

// calWhenText speaks a planned start: "morgen (Samstag) um 15 Uhr" or
// "morgen (Samstag), ganztägig".
func calWhenText(p calAddPlan, now time.Time, lang string) string {
	if p.AllDay {
		day := calDayPhrase(p.Start, now, lang)
		switch calendarDaysBetween(now, p.Start) {
		case 1:
			if lang == "de" {
				day = "morgen (" + weekdaysDE[p.Start.Weekday()] + ")"
			} else {
				day = "tomorrow (" + p.Start.Weekday().String() + ")"
			}
		}
		return day + pick(lang, ", all day", ", ganztägig")
	}
	return formatWhen(p.Start, now, lang)
}

// Router offers to a non-owner: the "yes" that answers the offer must not
// be asked again, so the offer remembers the args it was made for.
var calOffers = struct {
	sync.Mutex
	m map[string]calOfferNote
}{m: map[string]calOfferNote{}}

type calOfferNote struct {
	sum [32]byte
	at  time.Time
}

func noteCalOffer(clientID string, args json.RawMessage) {
	if clientID == "" {
		return
	}
	now := nowFunc()
	calOffers.Lock()
	defer calOffers.Unlock()
	for k, n := range calOffers.m {
		if now.Sub(n.at) > calOfferTTL {
			delete(calOffers.m, k)
		}
	}
	calOffers.m[clientID] = calOfferNote{sum: sha256.Sum256(args), at: now}
}

func takeCalOffer(clientID string, args json.RawMessage) bool {
	if clientID == "" {
		return false
	}
	calOffers.Lock()
	defer calOffers.Unlock()
	n, ok := calOffers.m[clientID]
	if !ok || n.sum != sha256.Sum256(args) || nowFunc().Sub(n.at) > calOfferTTL {
		return false
	}
	delete(calOffers.m, clientID)
	return true
}

func calSlotQuestion(t *Turn, slot string, a calAddArgs) Reply {
	if slot == "when" {
		a.WhenText = ""
	}
	args, _ := json.Marshal(a)
	q := pick(t.Lang, "When is the event?", "Wann ist der Termin?")
	if slot == "title" {
		q = pick(t.Lang, "What should the event be called?", "Wie soll der Termin heißen?")
	}
	return t.Ask(Pending{Kind: "slot", Slot: slot, Tool: "calendar_add", Args: args,
		Resume: func(t *Turn, p *Pending) (Reply, bool) {
			if p.Slot == "when" {
				if _, ok := calParseWhen(t.Query, turnNow(t), turnLoc(t)); !ok {
					return Reply{}, false
				}
				next := mergeArg(p.Args, "when_text", t.Query)
				if d, _ := calDurationOf(t.Query); d > 0 {
					next = mergeArg(next, "duration_min", int(d/time.Minute))
				}
				if reCalAllDay.MatchString(t.Query) {
					next = mergeArg(next, "all_day", true)
				}
				return runToolChecked(t, p.Tool, next), true
			}
			title := calTitleFrom(t.Query)
			if title == "" || len(t.Tokens) > 12 || isQuestion(t) {
				return Reply{}, false
			}
			return runToolChecked(t, p.Tool, mergeArg(p.Args, "title", title)), true
		}}, q)
}

// runCalendarAdd is the tool entry (fast path, router, slot answers).
// Router args are model output, so a "confirmed" flag can never come from
// them: only calConfirmResume passes confirmed=true.
func runCalendarAdd(t *Turn, raw json.RawMessage) Reply { return calAdd(t, raw, false) }

func calAdd(t *Turn, raw json.RawMessage, confirmed bool) Reply {
	var a calAddArgs
	_ = json.Unmarshal(raw, &a)
	if err := calReady(); err != nil {
		return calErrorReply(t, err)
	}
	rel, owner := calendarRelation(t)
	if rel == calOther && calOthersRefuse(t) {
		return calRefuseOther(t, owner, true)
	}
	plan, missing := calResolveAdd(t, a)
	if missing != "" {
		if missing == "when" {
			a.Title = plan.Title
		}
		return calSlotQuestion(t, missing, a)
	}
	now, loc := turnNow(t), turnLoc(t)
	when := calWhenText(plan, now, t.Lang)
	title := safeTitle(plan.Title)
	if rel == calOther && !confirmed && !takeCalOffer(t.ClientID, raw) {
		// Adding to someone else's calendar is always confirmed first.
		next := calAddArgs{Title: plan.Title, WhenText: a.WhenText, DurationMin: int(plan.Dur / time.Minute),
			AllDay: plan.AllDay, Fast: true}
		// The absolute start: "in 20 Minuten" must not move while the
		// question is open.
		next.WhenText = plan.Start.Format(time.RFC3339)
		args, _ := json.Marshal(next)
		return t.Ask(Pending{Kind: "confirm", Tool: "calendar_add", Args: args, Resume: calConfirmResume},
			pick(t.Lang, "Should I add "+title+" "+when+" to "+genitive(owner, "en")+" calendar?",
				"Soll ich "+title+" "+when+" in "+genitive(owner, "de")+" Kalender eintragen?"))
	}
	ctx, cancel := context.WithTimeout(t.Ctx, calFetchTimeout)
	defer cancel()
	ev, err := gcalInsert(ctx, plan.Title, plan.Start, plan.Dur, plan.AllDay, loc)
	if err != nil {
		return calErrorReply(t, err)
	}
	id := ev.ID
	label := pick(t.Lang, "event “"+title+"”", "Termin „"+title+"“")
	t.PushUndo(label, func(ctx context.Context) error { return gcalDelete(ctx, id) })
	text := pick(t.Lang, "Added: "+title+", "+when+".", "Eingetragen: "+title+", "+when+".")
	if rel == calOther {
		text = pick(t.Lang, "Added to "+genitive(owner, "en")+" calendar: "+title+", "+when+".",
			"In "+genitive(owner, "de")+" Kalender eingetragen: "+title+", "+when+".")
	}
	start := plan.Start
	end := start.Add(plan.Dur)
	if plan.AllDay {
		end = start.AddDate(0, 0, 1)
	}
	return Reply{Text: text, Card: calCard(start, end, []CalendarEvent{ev}, loc, map[string]any{"added": true})}
}

// calConfirmResume answers "Soll ich das in Annas Kalender eintragen?". A
// "yes" runs the add with the central access check, already confirmed.
func calConfirmResume(t *Turn, p *Pending) (Reply, bool) {
	switch yesNo(t.Tokens) {
	case 1:
		tool := lookupTool(p.Tool)
		if tool == nil || !tool.enabled(t) {
			return Reply{Text: pick(t.Lang, "That is not available right now.", "Das geht gerade nicht."), Error: true}, true
		}
		t.toolName = tool.Name
		// the central check records the class, so the undo calAdd pushes
		// needs the same access (skills.go undoClass)
		if !t.checkAccess(tool, p.Args) {
			return refuse(t, tool.Name, p.Args), true
		}
		return calAdd(t, p.Args, true), true
	case -1:
		return Reply{Text: pick(t.Lang, "Okay, I won't.", "Okay, mache ich nicht.")}, true
	}
	return Reply{}, false
}

// calOffer is the router's "Soll ich …?" for calendar_add without a cue.
func calOffer(t *Turn, raw json.RawMessage) string {
	var a calAddArgs
	_ = json.Unmarshal(raw, &a)
	rel, owner := calendarRelation(t)
	if rel == calOther && calOthersRefuse(t) {
		// No question that a "yes" could only answer with a refusal.
		return calRefuseOther(t, owner, true).Text
	}
	if rel == calOther {
		noteCalOffer(t.ClientID, raw)
	}
	plan, missing := calResolveAdd(t, a)
	now := turnNow(t)
	title := safeTitle(plan.Title)
	if title == "" {
		title = pick(t.Lang, "the event", "den Termin")
	}
	when := ""
	if missing != "when" {
		when = " " + calWhenText(plan, now, t.Lang)
	}
	if rel == calOther {
		return pick(t.Lang, "Should I add "+title+when+" to "+genitive(owner, "en")+" calendar?",
			"Soll ich "+title+when+" in "+genitive(owner, "de")+" Kalender eintragen?")
	}
	return pick(t.Lang, "Should I add "+title+when+" to your calendar?",
		"Soll ich "+title+when+" in deinen Kalender eintragen?")
}

// ---------------------------------------------------------------------------
// Briefing (section 30) and personal context (section 31)

// calendarBriefing: "Heute hast du 2 Termine: um 10 Uhr Zahnarzt und um
// 15:30 Uhr Team-Call." Omitted when not connected or not allowed.
func calendarBriefing(t *Turn) (string, error) {
	if !t.Can(AccessRead) {
		return "", nil
	}
	switch err := calReady(); {
	case errors.Is(err, errCalendarRelink):
		return calRelinkText(t.Lang), nil
	case err != nil:
		return "", nil
	}
	rel, owner := calendarRelation(t)
	if rel == calOther && calOthersRefuse(t) {
		return "", nil
	}
	now := turnNow(t)
	start := midnightOf(now)
	evs, err := calFetch(t.Ctx, start, start.AddDate(0, 0, 1), now, calBriefTimeout)
	if err != nil {
		return "", err
	}
	if len(evs) == 0 {
		if rel == calOther {
			return pick(t.Lang, "Nothing in "+genitive(owner, "en")+" calendar today.", "In "+genitive(owner, "de")+" Kalender steht heute nichts."), nil
		}
		return pick(t.Lang, "No events today.", "Heute keine Termine."), nil
	}
	list := calItems(evs, now, t.Lang, false, calBriefingMax, true)
	if rel == calOther {
		return pick(t.Lang, "In "+genitive(owner, "en")+" calendar today: "+list+".",
			"In "+genitive(owner, "de")+" Kalender steht heute: "+list+"."), nil
	}
	return pick(t.Lang, "Today you have "+calCount(len(evs), t.Lang)+": "+list+".",
		"Heute hast du "+calCount(len(evs), t.Lang)+": "+list+"."), nil
}

// calendarContext gives the persona model today's remaining events of the
// speaker's own calendar, between data delimiters.
func calendarContext(t *Turn) string {
	if !t.Can(AccessRead) || calReady() != nil {
		return ""
	}
	if rel, _ := calendarRelation(t); rel != calOwn {
		return ""
	}
	ctx := t.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	now := turnNow(t)
	evs, ok := calTodayCached(ctx, now)
	if !ok {
		return ""
	}
	var items []string
	for _, ev := range evs {
		if !ev.AllDay && !ev.End.After(now) {
			continue
		}
		title := safeTitle(ev.Title)
		if title == "" {
			title = pick(t.Lang, "untitled event", "Termin ohne Titel")
		}
		if ev.AllDay {
			items = append(items, pick(t.Lang, "all day ", "ganztägig ")+title)
		} else {
			items = append(items, ev.Start.In(now.Location()).Format("15:04")+" "+title)
		}
		if len(items) == calQueryMax {
			break
		}
	}
	return contextBlock(t.Lang, "Calendar today", "Kalender heute", items)
}

// ---------------------------------------------------------------------------
// GET /api/calendar/events?from&to

func handleCalendarEvents(w http.ResponseWriter, r *http.Request) {
	loc := settingsLocation(loadSettings())
	parse := func(name string) (time.Time, bool) {
		v := strings.TrimSpace(r.URL.Query().Get(name))
		if v == "" || len(v) > 64 {
			return time.Time{}, false
		}
		t, err := time.Parse(time.RFC3339, v)
		return t, err == nil
	}
	from, ok1 := parse("from")
	to, ok2 := parse("to")
	if !ok1 || !ok2 {
		writeJSONErrorCode(w, http.StatusBadRequest, "range", "from and to must be RFC 3339 times")
		return
	}
	if !to.After(from) || to.Sub(from) > gcalMaxRange+time.Hour {
		writeJSONErrorCode(w, http.StatusBadRequest, "range", errCalendarRange.Error())
		return
	}
	if err := calReady(); err != nil {
		if errors.Is(err, errCalendarRelink) {
			writeJSONErrorCode(w, http.StatusServiceUnavailable, "relink", "calendar connection expired; connect it again")
		} else {
			writeJSONErrorCode(w, http.StatusServiceUnavailable, "not_connected", "calendar not connected")
		}
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	evs, err := gcalList(ctx, from, to, loc)
	switch {
	case errors.Is(err, errCalendarRelink):
		writeJSONErrorCode(w, http.StatusServiceUnavailable, "relink", "calendar connection expired; connect it again")
		return
	case errors.Is(err, errCalendarNotConnected):
		writeJSONErrorCode(w, http.StatusServiceUnavailable, "not_connected", "calendar not connected")
		return
	case err != nil:
		writeJSONErrorCode(w, http.StatusBadGateway, "upstream", "calendar service did not answer")
		return
	}
	out := make([]CalendarEvent, 0, len(evs))
	for _, ev := range evs {
		ev.Start, ev.End = ev.Start.In(loc), ev.End.In(loc)
		out = append(out, ev)
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

// ---------------------------------------------------------------------------
// Registration

// calGateFunc opens the router for calendar words, compounds included
// ("Zahnarzttermin", "Familienkalender"), while a calendar is linked.
func calGateFunc(t *Turn) bool {
	st := gcalCurrentState()
	if !st.Connected && !st.NeedsRelink {
		return false
	}
	if hasAnyCue(t.Tokens, calGate) {
		return true
	}
	for _, w := range t.Tokens {
		for _, suf := range []string{"termin", "termine", "terminen", "kalender"} {
			if strings.HasSuffix(w, suf) {
				return true
			}
		}
	}
	return false
}

func init() {
	registerTool(Tool{
		Name:        "calendar_query",
		Description: "Read the user's calendar: what is on a day or in a week, or the next appointment.",
		Params: func(*Turn) json.RawMessage {
			return json.RawMessage(`{"type":"object","properties":{` +
				`"when_text":{"type":"string","description":"the day or period copied verbatim from the user's message, e.g. 'morgen', 'am Freitag', 'this week'; empty for the coming days"},` +
				`"next":{"type":"boolean","description":"true when the user asks for the next appointment"}}}`)
		},
		GateFunc: calGateFunc,
		Access:   func(*Turn, json.RawMessage) Access { return AccessRead },
		Run:      runCalendarQuery,
	})
	registerTool(Tool{
		Name:        "calendar_add",
		Description: "Add an event to the user's calendar.",
		Params: func(*Turn) json.RawMessage {
			return json.RawMessage(`{"type":"object","properties":{` +
				`"title":{"type":"string","description":"what the event is, in the user's words and language, without the time"},` +
				`"when_text":{"type":"string","description":"the date and time copied verbatim from the user's message, e.g. 'morgen um 15 Uhr'"},` +
				`"duration_min":{"type":"integer","description":"optional: length in minutes if the user said it"},` +
				`"all_day":{"type":"boolean","description":"optional: true for an all-day event"}},` +
				`"required":["title","when_text"]}`)
		},
		GateFunc:   calGateFunc,
		Access:     func(*Turn, json.RawMessage) Access { return AccessWrite },
		SideEffect: true,
		Imperative: calImperatives,
		Offer:      calOffer,
		Run:        runCalendarAdd,
	})
	// The query matcher accepts question vocabulary only, so it never takes
	// an add command; it runs first so "what's on my calendar" is a question.
	registerFastPath(FastPath{Name: "calendar_query", Order: 70, Tool: "calendar_query", Match: matchCalendarQuery})
	registerFastPath(FastPath{Name: "calendar_add", Order: 71, Tool: "calendar_add", Match: matchCalendarAdd})

	registerBriefing(30, "calendar", calendarBriefing)
	registerContext(31, "calendar", calendarContext)
	registerRoute("GET /api/calendar/events", requireUnlock(handleCalendarEvents))
}
