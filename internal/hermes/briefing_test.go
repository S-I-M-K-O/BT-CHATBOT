package hermes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Harness: the reminders environment (temp data, Berlin, Anna and Ben, fake
// clock) at Friday 6 November 2026, 07:30, plus fake Open-Meteo, Tagesschau
// and Google News servers. Nothing here reaches a real service.

type briefFakes struct {
	geoHits, forecastHits, newsHits atomic.Int32
	tagesschauHits                  atomic.Int32
	forecastFail                    atomic.Bool
	forecastDelay                   time.Duration
	lastGeoLang, lastNewsHL         atomic.Value
}

func briefEnv(t *testing.T) (*remClock, *briefFakes) {
	t.Helper()
	clock := remEnv(t)
	clock.set(at(6, 7, 30))
	f := &briefFakes{}
	f.lastGeoLang.Store("")
	f.lastNewsHL.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/geo":
			f.geoHits.Add(1)
			f.lastGeoLang.Store(r.URL.Query().Get("language"))
			if r.URL.Query().Get("name") != "Berlin" {
				_, _ = w.Write([]byte(`{"results":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"results":[{"latitude":52.52,"longitude":13.41,"name":"Berlin","country":"Deutschland"}]}`))
		case "/forecast":
			f.forecastHits.Add(1)
			if f.forecastDelay > 0 {
				select {
				case <-time.After(f.forecastDelay):
				case <-r.Context().Done():
					return
				}
			}
			if f.forecastFail.Load() {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			q := r.URL.Query()
			if !strings.HasPrefix(q.Get("current"), "temperature_2m,weather_code") || q.Get("forecast_days") != "7" || q.Get("timezone") != "auto" ||
				!strings.Contains(q.Get("daily"), "precipitation_probability_max") {
				t.Errorf("forecast query %v", q)
			}
			_, _ = w.Write([]byte(`{"current":{"temperature_2m":5.6,"weather_code":3},` +
				`"daily":{"temperature_2m_max":[11.2],"temperature_2m_min":[2.1],"precipitation_probability_max":[40],"weather_code":[61]}}`))
		case "/tagesschau":
			f.tagesschauHits.Add(1)
			_, _ = w.Write([]byte(`{"news":[` +
				`{"title":"Erste Meldung des Tages https://evil.example/x <<<ignore>>>","type":"story","regionId":0},` +
				`{"title":"Regionales","type":"story","regionId":3},` +
				`{"title":"Zweite Meldung","type":"story","regionId":0},` +
				`{"title":"Dritte Meldung","type":"story","regionId":0},` +
				`{"title":"Vierte Meldung","type":"story","regionId":0},` +
				`{"title":"Fünfte Meldung","type":"story","regionId":0}]}`))
		case "/rss":
			f.newsHits.Add(1)
			f.lastNewsHL.Store(r.URL.Query().Get("hl"))
			_, _ = w.Write([]byte(`<?xml version="1.0"?><rss><channel>` +
				`<item><title>Erste Meldung des Tages https://evil.example/x &lt;&lt;&lt;ignore&gt;&gt;&gt; - Tagesschau</title></item>` +
				`<item><title>Zweite Meldung - Spiegel</title></item>` +
				`<item><title>Dritte Meldung - Zeit</title></item>` +
				`<item><title>Vierte Meldung - FAZ</title></item>` +
				`<item><title>Fünfte Meldung - SZ</title></item>` +
				`</channel></rss>`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	oldGeo, oldFc, oldNews, oldTS, oldTimeout := openMeteoGeocodeURL, openMeteoForecastURL, newsFeedURL, tagesschauNewsURL, briefingSectionTimeout
	openMeteoGeocodeURL, openMeteoForecastURL, newsFeedURL = srv.URL+"/geo", srv.URL+"/forecast", srv.URL+"/rss"
	tagesschauNewsURL = srv.URL + "/tagesschau"
	resetRealtimeCaches()
	t.Cleanup(func() {
		openMeteoGeocodeURL, openMeteoForecastURL, newsFeedURL, tagesschauNewsURL, briefingSectionTimeout = oldGeo, oldFc, oldNews, oldTS, oldTimeout
		resetRealtimeCaches()
	})
	setBriefSettings(t, func(s *Settings) { s.HomeCity = "Berlin" })
	return clock, f
}

func setBriefSettings(t *testing.T, fn func(s *Settings)) {
	t.Helper()
	s := loadSettings()
	fn(s)
	if err := saveSettings(s); err != nil {
		t.Fatal(err)
	}
}

func briefingCard(t *testing.T, r remResult) []briefingPart {
	t.Helper()
	card := hasEvent(r.events, "card")
	if card == nil || card["kind"] != "briefing" {
		t.Fatalf("no briefing card in %v", r.events)
	}
	raw, _ := json.Marshal(card["data"])
	var d struct {
		Sections []briefingPart `json:"sections"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d.Sections
}

func sectionNames(ps []briefingPart) string {
	var n []string
	for _, p := range ps {
		n = append(n, p.Name)
	}
	return strings.Join(n, ",")
}

func briefMatch(t *testing.T, text string, sp Speaker, lang string) (string, bool) {
	t.Helper()
	tr, _ := remTurn(text, sp, lang)
	raw, ok := matchBriefing(tr)
	return string(raw), ok
}

// ---------------------------------------------------------------------------
// Matching

func TestBriefingRegistryOrder(t *testing.T) {
	var names []string
	for _, s := range briefingSections() {
		names = append(names, fmt.Sprintf("%d:%s", s.order, s.name))
	}
	got := strings.Join(names, " ")
	if strings.Contains(got, "greeting") {
		t.Errorf("the greeting is built in, not registered: %s", got)
	}
	for _, want := range []string{"20:weather", "40:reminders", "50:lists", "60:news"} {
		if !strings.Contains(got, want) {
			t.Errorf("briefing registry %q lacks %s", got, want)
		}
	}
}

func TestBriefingGreetingMatches(t *testing.T) {
	briefEnv(t)
	yes := []struct{ text, lang string }{
		{"Guten Morgen", "de"}, {"Guten Morgen, Hermes!", "de"}, {"Hallo Hermes, guten Morgen", "de"},
		{"Moin", "de"}, {"Morgen, Hermes", "de"}, {"Schönen guten Morgen", "de"},
		{"Good morning", "en"}, {"Good morning, Hermes", "en"}, {"Morning, Hermes", "en"}, {"good morning to you too", "en"},
	}
	for _, c := range yes {
		resetBriefingSeen(t)
		if raw, ok := briefMatch(t, c.text, remAnna, c.lang); !ok || !strings.Contains(raw, "greeting") {
			t.Errorf("%q: want greeting, got %s %v", c.text, raw, ok)
		}
	}
	no := []string{
		"Morgen.", "Morning.", "Morgen um 8", "Guten Morgen, wie wird das Wetter?", "Ich muss einen Brief schreiben",
		"Was steht heute in der Zeitung?", "good morning america is my favourite show", "Guten Tag",
		"Ich schreibe einen Lagebericht für die Arbeit",
	}
	for _, text := range no {
		if raw, ok := briefMatch(t, text, remAnna, "de"); ok {
			t.Errorf("%q must not trigger the briefing, got %s", text, raw)
		}
	}
}

func resetBriefingSeen(t *testing.T) {
	t.Helper()
	if err := stateStore.Update(func(st *stateFile) error {
		st.BriefingSeen = map[string]string{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBriefingExplicitRequestsAnyTime(t *testing.T) {
	clock, _ := briefEnv(t)
	clock.set(at(6, 16, 0))
	for _, c := range []struct{ text, lang string }{
		{"Morgenbriefing", "de"}, {"Morgen-Briefing bitte", "de"}, {"Hermes, gib mir den Lagebericht", "de"},
		{"Tagesüberblick", "de"}, {"Wie sieht mein Tag aus?", "de"},
		{"brief me", "en"}, {"morning briefing", "en"}, {"daily briefing please", "en"}, {"give me my briefing", "en"},
		{"Hermes, what does my day look like?", "en"},
	} {
		if raw, ok := briefMatch(t, c.text, remAnna, c.lang); !ok || !strings.Contains(raw, "explicit") {
			t.Errorf("%q: want explicit, got %s %v", c.text, raw, ok)
		}
	}
	// Outside 04:00–11:59 a greeting is small talk.
	if _, ok := briefMatch(t, "Guten Morgen", remAnna, "de"); ok {
		t.Error("greeting at 16:00 triggered the briefing")
	}
	clock.set(at(6, 3, 59))
	if _, ok := briefMatch(t, "Good morning", remAnna, "en"); ok {
		t.Error("greeting at 03:59 triggered the briefing")
	}
	clock.set(at(6, 4, 0))
	if _, ok := briefMatch(t, "Good morning", remAnna, "en"); !ok {
		t.Error("greeting at 04:00 did not trigger")
	}
}

func TestBriefingDayRequestDefersToCalendar(t *testing.T) {
	briefEnv(t)
	phrases := []struct{ text, lang string }{{"Was steht heute an?", "de"}, {"what's on today", "en"}}
	// Nothing set up: the day question is the briefing.
	for _, c := range phrases {
		if _, ok := briefMatch(t, c.text, remAnna, c.lang); !ok {
			t.Errorf("%q without a calendar should give the briefing", c.text)
		}
	}
	// A Google client but no link yet: still the briefing, not "not connected".
	t.Setenv("GOOGLE_CLIENT_ID", fgClientID)
	t.Setenv("GOOGLE_CLIENT_SECRET", fgSecret)
	resetCalendarState()
	t.Cleanup(resetCalendarState)
	fg := newFakeGoogle(t)
	for _, c := range phrases {
		if _, ok := briefMatch(t, c.text, remAnna, c.lang); !ok {
			t.Errorf("%q with an unlinked calendar should give the briefing", c.text)
		}
	}
	// Linked: the briefing steps aside and the calendar day query answers.
	connectFake(t, fg)
	for _, c := range phrases {
		if _, ok := briefMatch(t, c.text, remAnna, c.lang); ok {
			t.Errorf("%q with a calendar belongs to the calendar query", c.text)
		}
		r := remSay(t, c.text, remAnna, c.lang)
		if card := hasEvent(r.events, "card"); !r.handled || card == nil || card["kind"] != "calendar" {
			t.Errorf("%q with a calendar: handled=%v card=%v text=%q", c.text, r.handled, card, r.text)
		}
	}
	// Explicit briefing requests still win with a calendar.
	if _, ok := briefMatch(t, "Morgenbriefing", remAnna, "de"); !ok {
		t.Error("explicit briefing request deferred to the calendar")
	}
}

func TestBriefingDisabledLeavesSmallTalk(t *testing.T) {
	briefEnv(t)
	setBriefSettings(t, func(s *Settings) { s.BriefingOnGreeting = false })
	for _, c := range []struct{ text, lang string }{{"Guten Morgen", "de"}, {"Good morning, Hermes", "en"}} {
		r := remSay(t, c.text, remAnna, c.lang)
		if r.handled {
			t.Errorf("%q handled by a fast path with the briefing off: %q", c.text, r.text)
		}
		if localAnswer(c.text, c.lang) == "" {
			t.Errorf("%q: the local small-talk answer is gone", c.text)
		}
	}
	// Explicit requests still work.
	if r := remSay(t, "Morgenbriefing", remAnna); !r.handled || !strings.Contains(r.text, "Guten Morgen, Anna.") {
		t.Errorf("explicit request with greeting off: %+v", r.text)
	}
}

// ---------------------------------------------------------------------------
// Composition

func TestBriefingFullGerman(t *testing.T) {
	_, f := briefEnv(t)
	setBriefSettings(t, func(s *Settings) { s.BriefingNews = true })
	addTestReminder(t, Reminder{Text: "Zahnarzt anrufen", Due: at(6, 16, 0), Person: "p_anna"})
	addTestReminder(t, Reminder{Text: "Müll rausbringen", Due: at(6, 19, 0)})
	addTestReminder(t, Reminder{Text: "Morgen erst", Due: at(7, 9, 0)})
	if _, err := addItems(listsShoppingID, []string{"Milch", "Brot"}, ""); err != nil {
		t.Fatal(err)
	}

	r := remSay(t, "Guten Morgen, Hermes", remAnna)
	if !r.handled {
		t.Fatal("not handled")
	}
	for _, want := range []string{
		"Guten Morgen, Anna. Es ist Freitag, der 6. November, 7:30 Uhr.",
		"In Berlin sind es 6 Grad, bedeckt. Heute bis 11 Grad, Regenrisiko 40 Prozent.",
		"Heute 2 Erinnerungen, die nächste um 16 Uhr: Zahnarzt anrufen.",
		"Milch",
		"Schlagzeile: Erste Meldung des Tages",
		"Mehr Schlagzeilen?",
	} {
		if !strings.Contains(r.text, want) {
			t.Errorf("briefing lacks %q:\n%s", want, r.text)
		}
	}
	if strings.Contains(r.text, "evil.example") || strings.Contains(r.text, "<<<") || strings.Contains(r.text, "Tagesschau") {
		t.Errorf("headline not cleaned: %s", r.text)
	}
	if strings.Contains(r.text, "Morgen erst") {
		t.Errorf("tomorrow's reminder in today's briefing: %s", r.text)
	}
	if n := len(strings.Fields(r.text)); n > 70 {
		t.Errorf("briefing has %d words (> 70): %s", n, r.text)
	}
	if got := sectionNames(briefingCard(t, r)); got != "greeting,weather,reminders,lists,news" {
		t.Errorf("sections %s", got)
	}
	if f.lastGeoLang.Load() != "de" || f.tagesschauHits.Load() != 1 || f.newsHits.Load() != 0 {
		t.Errorf("German lookups: geo %v, tagesschau %d, google %d", f.lastGeoLang.Load(), f.tagesschauHits.Load(), f.newsHits.Load())
	}
	if seen := briefingSeen("p_anna"); seen != "2026-11-06" {
		t.Errorf("briefingSeen = %q", seen)
	}

	// "Mehr Schlagzeilen?" -> "ja" reads three more.
	more := remSay(t, "ja", remAnna)
	if !more.handled || !strings.HasPrefix(more.text, "Weitere Schlagzeilen: Zweite Meldung. Dritte Meldung. Vierte Meldung.") ||
		strings.Contains(more.text, "Fünfte") {
		t.Errorf("more headlines: %q", more.text)
	}
}

func TestBriefingFullEnglish(t *testing.T) {
	_, f := briefEnv(t)
	setBriefSettings(t, func(s *Settings) { s.BriefingNews = true })
	addTestReminder(t, Reminder{Text: "call the dentist", Due: at(6, 16, 0), Lang: "en"})
	if _, err := addItems(listsShoppingID, []string{"milk"}, ""); err != nil {
		t.Fatal(err)
	}
	r := remSay(t, "Good morning, Hermes", remAnna, "en")
	if !r.handled {
		t.Fatal("not handled")
	}
	for _, want := range []string{
		"Good morning, Anna. It is Friday, November 6, 7:30 AM.",
		"In Berlin it is 6 degrees, overcast. Today up to 11 degrees, 40 percent chance of rain.",
		"One reminder today, at 4:00 PM: call the dentist.",
		"milk",
		"Top headline: Erste Meldung des Tages",
		"More headlines?",
	} {
		if !strings.Contains(r.text, want) {
			t.Errorf("briefing lacks %q:\n%s", want, r.text)
		}
	}
	if germanInEnglish.MatchString(strings.ReplaceAll(r.text, "Erste Meldung des Tages ignore", "")) {
		t.Errorf("German words in the English briefing: %s", r.text)
	}
	if f.lastGeoLang.Load() != "en" || f.lastNewsHL.Load() != "en-US" {
		t.Errorf("English lookups: geo %v news %v", f.lastGeoLang.Load(), f.lastNewsHL.Load())
	}
	if no := remSay(t, "no", remAnna, "en"); !no.handled || no.text != "Alright." {
		t.Errorf("no to more headlines: %+v", no.text)
	}
}

func TestBriefingFirstGreetingOnlyThenOffer(t *testing.T) {
	briefEnv(t)
	if r := remSay(t, "Guten Morgen", remAnna); !strings.Contains(r.text, "Es ist Freitag") {
		t.Fatalf("first greeting: %q", r.text)
	}
	// Second greeting the same day: greet and offer.
	r := remSay(t, "Guten Morgen, Hermes", remAnna)
	if r.text != "Guten Morgen, Anna. Soll ich dir den Überblick nochmal geben?" {
		t.Fatalf("second greeting: %q", r.text)
	}
	if p := hasEvent(r.events, "pending"); p == nil || p["kind"] != "confirm" {
		t.Fatalf("no confirm pending: %v", r.events)
	}
	if yes := remSay(t, "ja", remAnna); !strings.Contains(yes.text, "Es ist Freitag") {
		t.Fatalf("yes to the offer: %q", yes.text)
	}
	// Ben has not been greeted yet.
	if r := remSay(t, "Moin", remBen); !strings.Contains(r.text, "Guten Morgen, Ben. Es ist Freitag") {
		t.Fatalf("Ben's first greeting: %q", r.text)
	}
	// English offer, and "no".
	en := remSay(t, "Good morning", remBen, "en")
	if en.text != "Good morning, Ben. Shall I give you the overview again?" {
		t.Fatalf("English offer: %q", en.text)
	}
	if no := remSay(t, "nein", remBen); no.text != "Alles klar." {
		t.Fatalf("no: %q", no.text)
	}
	// Explicit requests ignore the per-day rule.
	if r := remSay(t, "brief me", remBen, "en"); !strings.Contains(r.text, "It is Friday") {
		t.Fatalf("explicit after greeting: %q", r.text)
	}
	// The next morning it is new again.
	resetReminderSession()
	clockSetDay(t, 7)
	if r := remSay(t, "Guten Morgen", remAnna); !strings.Contains(r.text, "Es ist Samstag, der 7. November") {
		t.Fatalf("next day: %q", r.text)
	}
}

func clockSetDay(t *testing.T, day int) {
	t.Helper()
	old := nowFunc
	fixed := at(day, 7, 45)
	nowFunc = func() time.Time { return fixed }
	t.Cleanup(func() { nowFunc = old })
}

func TestBriefingGuestGetsNoPersonalSections(t *testing.T) {
	briefEnv(t)
	registerBriefing(30, "calendar", func(t *Turn) (string, error) {
		return "Heute hast du 2 Termine, um 10 Uhr Zahnarzt.", nil
	})
	addTestReminder(t, Reminder{Text: "Annas Geheimnis", Due: at(6, 16, 0), Person: "p_anna"})
	addTestReminder(t, Reminder{Text: "Müll rausbringen", Due: at(6, 19, 0)})

	guest := remSay(t, "Morgenbriefing", remGuest)
	if strings.Contains(guest.text, "Termine") || strings.Contains(guest.text, "Geheimnis") || strings.Contains(guest.text, "Anna") {
		t.Errorf("guest briefing leaks personal data: %s", guest.text)
	}
	if !strings.Contains(guest.text, "Guten Morgen. Es ist Freitag") || !strings.Contains(guest.text, "Müll rausbringen") {
		t.Errorf("guest briefing lacks household parts: %s", guest.text)
	}
	if got := sectionNames(briefingCard(t, guest)); strings.Contains(got, "calendar") {
		t.Errorf("guest sections %s", got)
	}
	// A sticky (unverified) Anna is not greeted by name either.
	if sticky := remSay(t, "Morgenbriefing", remSticky); strings.Contains(sticky.text, "Anna") || strings.Contains(sticky.text, "Termine") {
		t.Errorf("sticky speaker briefing: %s", sticky.text)
	}
	anna := remSay(t, "Morgenbriefing", remAnna)
	if !strings.Contains(anna.text, "Heute hast du 2 Termine") || !strings.Contains(anna.text, "Heute 2 Erinnerungen") {
		t.Errorf("Anna's briefing: %s", anna.text)
	}
	if got := sectionNames(briefingCard(t, anna)); !strings.HasPrefix(got, "greeting,weather,calendar,reminders") {
		t.Errorf("Anna's sections %s", got)
	}
}

func TestBriefingCityHintOnce(t *testing.T) {
	_, f := briefEnv(t)
	setBriefSettings(t, func(s *Settings) { s.HomeCity = "" })
	first := remSay(t, "Morgenbriefing", remAnna)
	if !strings.Contains(first.text, "Für das Wetter trage in den Einstellungen deinen Wohnort ein.") {
		t.Fatalf("no city hint: %s", first.text)
	}
	second := remSay(t, "brief me", remAnna, "en")
	if strings.Contains(second.text, "weather") || strings.Contains(second.text, "Wetter") {
		t.Fatalf("city hint repeated: %s", second.text)
	}
	if f.geoHits.Load() != 0 || f.forecastHits.Load() != 0 {
		t.Errorf("weather fetched without a city")
	}
	// English wording of the hint.
	if err := stateStore.Update(func(st *stateFile) error { st.CityHintShown = false; return nil }); err != nil {
		t.Fatal(err)
	}
	if en := remSay(t, "brief me", remAnna, "en"); !strings.Contains(en.text, "For the weather, enter your home city in the settings.") {
		t.Errorf("English hint: %s", en.text)
	}
}

func TestBriefingWeatherFailsSoftAndGeocodeIsCached(t *testing.T) {
	_, f := briefEnv(t)
	f.forecastFail.Store(true)
	r := remSay(t, "Morgenbriefing", remAnna)
	if !strings.Contains(r.text, "Es ist Freitag") || strings.Contains(r.text, "Grad") {
		t.Fatalf("failed weather: %s", r.text)
	}
	f.forecastFail.Store(false)
	remSay(t, "Morgenbriefing", remAnna)
	remSay(t, "Morgenbriefing", remAnna)
	if f.geoHits.Load() != 1 {
		t.Errorf("geocode called %d times, want 1 (cached)", f.geoHits.Load())
	}
	// Unknown city: omitted, no error text.
	setBriefSettings(t, func(s *Settings) { s.HomeCity = "Atlantis" })
	if r := remSay(t, "Morgenbriefing", remAnna); strings.Contains(r.text, "Atlantis") || !strings.Contains(r.text, "Es ist") {
		t.Errorf("unknown city: %s", r.text)
	}
}

func TestBriefingSectionTimeout(t *testing.T) {
	_, f := briefEnv(t)
	briefingSectionTimeout = 150 * time.Millisecond
	f.forecastDelay = 2 * time.Second
	registerBriefing(35, "hang", func(t *Turn) (string, error) {
		time.Sleep(time.Second) // ignores its context on purpose
		return "too late", nil
	})
	registerBriefing(36, "panics", func(t *Turn) (string, error) { panic("boom") })
	registerBriefing(37, "fails", func(t *Turn) (string, error) { return "partial", fmt.Errorf("nope") })
	start := time.Now()
	r := remSay(t, "Morgenbriefing", remAnna)
	if d := time.Since(start); d > time.Second {
		t.Errorf("briefing took %v despite section timeouts", d)
	}
	if strings.Contains(r.text, "too late") || strings.Contains(r.text, "Grad") || strings.Contains(r.text, "partial") {
		t.Errorf("slow or failed sections included: %s", r.text)
	}
	if !strings.Contains(r.text, "Guten Morgen, Anna.") {
		t.Errorf("greeting missing: %s", r.text)
	}
}

func TestBriefingRouterTool(t *testing.T) {
	briefEnv(t)
	tool := lookupTool("morning_briefing")
	if tool == nil || tool.SideEffect {
		t.Fatalf("tool %+v", tool)
	}
	for _, g := range tool.Gate {
		if g == "brief" || g == "morgen" || g == "morning" {
			t.Errorf("banned gate word %q", g)
		}
	}
	tr, _ := remTurn("irgendwas", remAnna, "de")
	if r := runToolChecked(tr, "morning_briefing", json.RawMessage(`{}`)); !strings.Contains(r.Text, "Es ist Freitag") || r.Card == nil {
		t.Errorf("router run: %+v", r)
	}
}

func TestBriefingChatEndToEndVoices(t *testing.T) {
	for _, c := range []struct{ voice, text, want string }{
		{"JarvisDE", "Guten Morgen, Hermes", "Guten Morgen. Es ist Freitag, der 6. November, 7:30 Uhr."},
		{"BT7274", "Good morning, Hermes", "Good morning. It is Friday, November 6, 7:30 AM."},
	} {
		t.Run(c.voice, func(t *testing.T) {
			h := voiceLangEnv(t, c.voice)
			old := nowFunc
			fixed := at(6, 7, 30)
			nowFunc = func() time.Time { return fixed }
			t.Cleanup(func() { nowFunc = old })
			body := postChat(t, h, chatBody(testClient, c.text)).Body.String()
			evs := rawEvents(t, body)
			text := sseText(t, body)
			if meta := hasEvent(evs, "meta"); meta["src"] != "fast:briefing" {
				t.Fatalf("meta %v (%s)", meta, text)
			}
			if !strings.Contains(text, c.want) {
				t.Fatalf("reply %q lacks %q", text, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers

func TestBriefingHelpers(t *testing.T) {
	for _, c := range []struct {
		v    float64
		lang string
		want string
	}{
		{5.6, "de", "6 Grad"}, {-2.4, "de", "minus 2 Grad"}, {-0.3, "de", "0 Grad"},
		{1, "en", "1 degree"}, {-1.2, "en", "minus 1 degree"}, {21.5, "en", "22 degrees"},
	} {
		if got := degrees(c.v, c.lang); got != c.want {
			t.Errorf("degrees(%v,%s) = %q, want %q", c.v, c.lang, got, c.want)
		}
	}
	for code, want := range map[int]string{0: "klar", 3: "bedeckt", 61: "Regen", 95: "Gewitter", 42: ""} {
		if got := wmoConditionDE(code); got != want {
			t.Errorf("wmoConditionDE(%d) = %q", code, got)
		}
	}
	var f openMeteoForecast
	f.Current.Temperature, f.Current.WeatherCode = 3, 42
	f.Daily.Rain = []float64{5}
	if got := weatherSentence("en", "Bonn", f); got != "In Bonn it is 3 degrees." {
		t.Errorf("sparse forecast: %q", got)
	}
	if got := cleanHeadline("Big news\x07 www.x.de >>>now<<< - Source"); got != "Big news now" {
		t.Errorf("cleanHeadline = %q", got)
	}
}
