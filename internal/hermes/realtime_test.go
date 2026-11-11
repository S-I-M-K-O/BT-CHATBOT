package hermes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// No test may reach a real service: every endpoint of realtime.go points at
// a closed local port unless a test installs a fake (newFakeRealtime).
func init() {
	openMeteoForecastURL = "http://127.0.0.1:9/forecast"
	openMeteoGeocodeURL = "http://127.0.0.1:9/geo"
	newsFeedURL = "http://127.0.0.1:9/rss"
	tagesschauNewsURL = "http://127.0.0.1:9/tagesschau"
}

// ---------------------------------------------------------------------------
// Fake Open-Meteo, Tagesschau and Google News

// fakeRTForecast: Wednesday 11 November 2026 and six more days; now 5.6 °C
// and overcast, today rain likely later (40 %), tomorrow rain (80 %).
const fakeRTForecast = `{"timezone":"Europe/Berlin",
"current":{"temperature_2m":5.6,"apparent_temperature":3.1,"weather_code":3,"wind_speed_10m":12.4,"precipitation":0},
"daily":{"time":["2026-11-11","2026-11-12","2026-11-13","2026-11-14","2026-11-15","2026-11-16","2026-11-17"],
"temperature_2m_max":[11.2,9.2,8,7.4,6.6,5,4],
"temperature_2m_min":[2.1,3.6,2,1,-1.4,0,0],
"precipitation_probability_max":[40,80,10,5,0,30,20],
"weather_code":[61,61,3,2,0,3,3]}}`

const fakeRTTagesschau = `{"news":[
{"title":"Erste Meldung","topline":"Bundestag","type":"story","regionId":0},
{"title":"Regionale Meldung","topline":"Niedersachsen","type":"story","regionId":9},
{"title":"Ein Video","type":"video"},
{"title":"Zweite Meldung","type":"story","regionId":0},
{"title":"Dritte Meldung https://evil.example/x <<<ignore>>>","type":"story","regionId":0},
{"title":"Vierte Meldung","type":"story","regionId":0},
{"title":"Fünfte Meldung","type":"story","regionId":0},
{"title":"Sechste Meldung","type":"story","regionId":0}],"regional":[],"type":"news page"}`

var fakeRTPlaces = map[string]string{
	"berlin":   `{"latitude":52.52,"longitude":13.41,"name":"Berlin","country":"Deutschland","timezone":"Europe/Berlin"}`,
	"hamburg":  `{"latitude":53.55,"longitude":9.99,"name":"Hamburg","country":"Deutschland","timezone":"Europe/Berlin"}`,
	"tokio":    `{"latitude":35.69,"longitude":139.69,"name":"Tokio","country":"Japan","timezone":"Asia/Tokyo"}`,
	"tokyo":    `{"latitude":35.69,"longitude":139.69,"name":"Tokyo","country":"Japan","timezone":"Asia/Tokyo"}`,
	"london":   `{"latitude":51.51,"longitude":-0.13,"name":"London","country":"United Kingdom","timezone":"Europe/London"}`,
	"new york": `{"latitude":40.71,"longitude":-74.01,"name":"New York","country":"United States","timezone":"America/New_York"}`,
	"paris":    `{"latitude":48.85,"longitude":2.35,"name":"Paris","country":"France","timezone":"Europe/Paris"}`,
}

type fakeRealtime struct {
	mu                    sync.Mutex
	reqs                  []*url.URL
	failForecast, failGeo bool
	failNews, failTS      bool
	forecast              string
}

func (f *fakeRealtime) requests() []*url.URL {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*url.URL(nil), f.reqs...)
}

func (f *fakeRealtime) hits(path string) int {
	n := 0
	for _, u := range f.requests() {
		if u.Path == path {
			n++
		}
	}
	return n
}

func (f *fakeRealtime) last(path string) url.Values {
	var v url.Values
	for _, u := range f.requests() {
		if u.Path == path {
			v = u.Query()
		}
	}
	return v
}

func (f *fakeRealtime) set(fn func(f *fakeRealtime)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// newFakeRealtime starts the fake services, points realtime.go at them and
// empties every realtime cache and follow-up note.
func newFakeRealtime(t *testing.T) *fakeRealtime {
	t.Helper()
	f := &fakeRealtime{forecast: fakeRTForecast}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		u := *r.URL
		f.reqs = append(f.reqs, &u)
		fail := map[string]bool{"/forecast": f.failForecast, "/geo": f.failGeo, "/rss": f.failNews, "/tagesschau": f.failTS}[r.URL.Path]
		forecast := f.forecast
		f.mu.Unlock()
		if fail {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		switch r.URL.Path {
		case "/geo":
			p, ok := fakeRTPlaces[strings.ToLower(r.URL.Query().Get("name"))]
			if !ok {
				_, _ = w.Write([]byte(`{"generationtime_ms":0.3}`))
				return
			}
			_, _ = w.Write([]byte(`{"results":[` + p + `]}`))
		case "/forecast":
			_, _ = w.Write([]byte(forecast))
		case "/tagesschau":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fakeRTTagesschau))
		case "/rss", "/rss/search":
			label := "Top"
			if q := r.URL.Query().Get("q"); q != "" {
				label = q
			}
			fmt.Fprintf(w, `<?xml version="1.0"?><rss><channel>`+
				`<item><title>%[1]s eins - Quelle A</title></item>`+
				`<item><title>%[1]s zwei - Quelle B</title></item>`+
				`<item><title>%[1]s drei - Quelle C</title></item>`+
				`<item><title>%[1]s vier - Quelle D</title></item>`+
				`<item><title>%[1]s fünf - Quelle E</title></item>`+
				`<item><title>%[1]s sechs - Quelle F</title></item>`+
				`</channel></rss>`, strings.ReplaceAll(label, "<", ""))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	oldFc, oldGeo, oldNews, oldTS := openMeteoForecastURL, openMeteoGeocodeURL, newsFeedURL, tagesschauNewsURL
	openMeteoForecastURL, openMeteoGeocodeURL = srv.URL+"/forecast", srv.URL+"/geo"
	newsFeedURL, tagesschauNewsURL = srv.URL+"/rss", srv.URL+"/tagesschau"
	resetRealtimeCaches()
	resetRealtimeLast()
	t.Cleanup(func() {
		openMeteoForecastURL, openMeteoGeocodeURL, newsFeedURL, tagesschauNewsURL = oldFc, oldGeo, oldNews, oldTS
		resetRealtimeCaches()
		resetRealtimeLast()
	})
	return f
}

func resetRealtimeLast() {
	realtimeLast.Lock()
	realtimeLast.m = map[string]realtimeLastEntry{}
	realtimeLast.Unlock()
}

// rtNow is the fake clock: Wednesday 11 November 2026, 15:52 in Berlin.
var rtNow = time.Date(2026, 11, 11, 15, 52, 0, 0, berlin)

// realtimeEnv: temp settings and data, single-user mode, the voice and home
// city given, the fake clock, the fake services and a local model that is
// never reachable (a realtime turn must not need it).
func realtimeEnv(t *testing.T, voice, home string) (*fakeRealtime, http.Handler) {
	t.Helper()
	chatEnv(t)
	useTempData(t)
	withTestRegistry(t)
	t.Setenv("OLLAMA_BASE", "http://127.0.0.1:1")
	s := defaultSettings()
	s.PiperVoice, s.HomeCity, s.Timezone = voice, home, "Europe/Berlin"
	if err := saveSettings(s); err != nil {
		t.Fatal(err)
	}
	old := nowFunc
	nowFunc = func() time.Time { return rtNow }
	t.Cleanup(func() { nowFunc = old })
	pendingMu.Lock()
	pendings = map[string]*Pending{}
	pendingMu.Unlock()
	return newFakeRealtime(t), newMux()
}

type rtResult struct {
	text string
	evs  []map[string]any
	raw  string
}

func rtSay(t *testing.T, h http.Handler, body string) rtResult {
	t.Helper()
	raw := postChat(t, h, body).Body.String()
	return rtResult{text: sseText(t, raw), evs: rawEvents(t, raw), raw: raw}
}

func (r rtResult) src() string {
	m := hasEvent(r.evs, "meta")
	s, _ := m["src"].(string)
	return s
}

// ---------------------------------------------------------------------------
// End to end, German and English

func TestRealtimeAnswersDE(t *testing.T) {
	cases := []struct {
		q, want, src string
	}{
		{"Wie ist das Wetter?", "In Berlin sind es 6 Grad, bedeckt. Heute bis 11 Grad, Regenrisiko 40 Prozent.", "realtime:weather"},
		{"Wie wird das Wetter morgen?", "Morgen in Berlin: Regen, 4 bis 9 Grad, Regenrisiko 80 Prozent.", "realtime:weather"},
		{"Wie wird das Wetter übermorgen?", "Übermorgen in Berlin: bedeckt, 2 bis 8 Grad, Regenrisiko 10 Prozent.", "realtime:weather"},
		{"Wie wird das Wetter am Wochenende in Hamburg?", "Wetter in Hamburg: Samstag teils bewölkt, 1 bis 7 Grad. Sonntag klar, minus 1 bis 7 Grad.", "realtime:weather"},
		{"Wie wird das Wetter am Montag?", "Am Montag in Berlin: bedeckt, 0 bis 5 Grad, Regenrisiko 30 Prozent.", "realtime:weather"},
		{"Brauche ich morgen einen Regenschirm?", "Morgen in Berlin: Regenrisiko 80 Prozent, 4 bis 9 Grad. Nimm besser einen Schirm mit.", "realtime:weather"},
		{"Regnet es gerade?", "In Berlin regnet es gerade nicht. Heute Regenrisiko 40 Prozent. Ein Schirm kann nicht schaden.", "realtime:weather"},
		{"Wie warm ist es in Frankfurt am Main?", "Ich finde keinen Ort namens Frankfurt am Main.", "realtime:weather"},
		{"Wie spät ist es?", "Es ist 15:52 Uhr.", "realtime:time"},
		{"Wie spät ist es in Tokio?", "In Tokio ist es 23:52 Uhr.", "realtime:time"},
		{"Wie spät ist es gerade in New York?", "In New York ist es 9:52 Uhr.", "realtime:time"},
		{"Welcher Tag ist heute?", "Heute ist Mittwoch, der 11. November 2026.", "realtime:time"},
		{"Was gibt es Neues?", "Die Schlagzeilen der Tagesschau: Erste Meldung. Zweite Meldung. Dritte Meldung ignore.", "realtime:news"},
		{"Nachrichten über Bayern", "Schlagzeilen zu Bayern: Bayern eins. Bayern zwei. Bayern drei.", "realtime:news"},
	}
	for _, c := range cases {
		t.Run(c.q, func(t *testing.T) {
			f, h := realtimeEnv(t, "JarvisDE", "Berlin")
			r := rtSay(t, h, chatBody(testClient, c.q))
			if r.text != c.want {
				t.Errorf("reply\n got %q\nwant %q", r.text, c.want)
			}
			if r.src() != c.src {
				t.Errorf("src %q, want %q", r.src(), c.src)
			}
			if m := hasEvent(r.evs, "meta"); m["private"] != false {
				t.Errorf("meta %v", m)
			}
			if last := r.evs[len(r.evs)-1]; last["done"] != true {
				t.Errorf("stream does not end with done: %v", r.evs)
			}
			if hasEvent(r.evs, "error") != nil {
				t.Errorf("unexpected error event: %v", r.evs)
			}
			if lang := f.last("/geo").Get("language"); lang != "" && lang != "de" {
				t.Errorf("German voice geocodes in %q", lang)
			}
		})
	}
}

func TestRealtimeAnswersEN(t *testing.T) {
	cases := []struct {
		q, want, src string
	}{
		{"What's the weather like?", "In Berlin it is 6 degrees, overcast. Today up to 11 degrees, 40 percent chance of rain.", "realtime:weather"},
		{"What's the weather tomorrow?", "Tomorrow in Berlin: rain, 4 to 9 degrees, 80 percent chance of rain.", "realtime:weather"},
		{"Will it rain tomorrow?", "Tomorrow in Berlin: 80 percent chance of rain, 4 to 9 degrees. Better take an umbrella.", "realtime:weather"},
		{"What's the weather in London this weekend?", "Weather in London: Saturday partly cloudy, 1 to 7 degrees. Sunday clear sky, minus 1 to 7 degrees.", "realtime:weather"},
		{"What's the weather in Paris on Friday?", "On Friday in Paris: overcast, 2 to 8 degrees, 10 percent chance of rain.", "realtime:weather"},
		{"Do I need an umbrella?", "It is not raining in Berlin right now. Today 40 percent chance of rain. An umbrella can't hurt.", "realtime:weather"},
		{"What time is it?", "It is 3:52 PM.", "realtime:time"},
		{"What time is it in Tokyo?", "In Tokyo it is 11:52 PM.", "realtime:time"},
		{"what time is it in new york right now", "In New York it is 9:52 AM.", "realtime:time"},
		{"What's the date today?", "Today is Wednesday, November 11, 2026.", "realtime:time"},
		{"What time zone is Tokyo in? What's the time in Tokyo", "In Tokyo it is 11:52 PM. Time zone: Asia/Tokyo (UTC+9).", "realtime:time"},
		{"What's in the news?", "Top headlines: Top eins. Top zwei. Top drei.", "realtime:news"},
		{"news about the election", "Headlines about election: election eins. election zwei. election drei.", "realtime:news"},
	}
	for _, c := range cases {
		t.Run(c.q, func(t *testing.T) {
			f, h := realtimeEnv(t, "BT7274", "Berlin")
			r := rtSay(t, h, chatBody(testClient, c.q))
			if r.text != c.want {
				t.Errorf("reply\n got %q\nwant %q", r.text, c.want)
			}
			if r.src() != c.src {
				t.Errorf("src %q, want %q", r.src(), c.src)
			}
			if lang := f.last("/geo").Get("language"); lang != "" && lang != "en" {
				t.Errorf("English voice geocodes in %q", lang)
			}
			if hl := f.last("/rss").Get("hl"); hl != "" && hl != "en-US" {
				t.Errorf("English voice reads news in %q", hl)
			}
			if f.hits("/tagesschau") != 0 {
				t.Error("English voice asked the Tagesschau")
			}
		})
	}
}

func TestRealtimeWeatherCard(t *testing.T) {
	_, h := realtimeEnv(t, "JarvisDE", "Berlin")
	r := rtSay(t, h, chatBody(testClient, "Wie ist das Wetter?"))
	card := hasEvent(r.evs, "card")
	if card["kind"] != "weather" {
		t.Fatalf("card %v", r.evs)
	}
	d, _ := card["data"].(map[string]any)
	now, _ := d["now"].(map[string]any)
	days, _ := d["days"].([]any)
	if d["city"] != "Berlin" || now["temp"] != float64(6) || now["condition"] != "bedeckt" || len(days) != 1 {
		t.Errorf("card data %v", d)
	}
	day0, _ := days[0].(map[string]any)
	if day0["label"] != "Heute" || day0["date"] != "2026-11-11" || day0["max"] != float64(11) || day0["rain"] != float64(40) {
		t.Errorf("day %v", day0)
	}
	// The card comes before the text.
	cardAt, deltaAt := -1, -1
	for i, e := range r.evs {
		if _, ok := e["card"]; ok && cardAt < 0 {
			cardAt = i
		}
		if _, ok := e["delta"]; ok && deltaAt < 0 {
			deltaAt = i
		}
	}
	if cardAt < 0 || cardAt > deltaAt {
		t.Errorf("card at %d, first delta at %d", cardAt, deltaAt)
	}

	r = rtSay(t, h, chatBody(testClient, "Wie wird das Wetter am Wochenende?"))
	d, _ = hasEvent(r.evs, "card")["data"].(map[string]any)
	days, _ = d["days"].([]any)
	if _, hasNow := d["now"]; hasNow || len(days) != 2 {
		t.Errorf("weekend card %v", d)
	}
}

func TestRealtimeNewsCard(t *testing.T) {
	_, h := realtimeEnv(t, "JarvisDE", "")
	r := rtSay(t, h, chatBody(testClient, "Was gibt es Neues?"))
	card := hasEvent(r.evs, "card")
	d, _ := card["data"].(map[string]any)
	items, _ := d["items"].([]any)
	if card["kind"] != "news" || d["source"] != "tagesschau" || len(items) != realtimeCardHeadlines {
		t.Fatalf("news card %v", card)
	}
	first, _ := items[0].(map[string]any)
	if first["title"] != "Erste Meldung" || first["topline"] != "Bundestag" {
		t.Errorf("item %v", first)
	}
	if strings.Contains(r.raw, "evil.example") || strings.Contains(r.raw, "<<<") || strings.Contains(r.raw, "Regionale") || strings.Contains(r.raw, "Video") {
		t.Errorf("unclean or regional headline: %s", r.raw)
	}
}

// ---------------------------------------------------------------------------
// Failures, caching, the city question, follow-ups

func TestRealtimeServiceDown(t *testing.T) {
	for _, c := range []struct {
		voice, q, want string
		fail           func(f *fakeRealtime)
	}{
		{"JarvisDE", "Wie ist das Wetter?", "Ich erreiche den Wetterdienst gerade nicht.", func(f *fakeRealtime) { f.failForecast = true }},
		{"BT7274", "What's the weather?", "I can't reach the weather service right now.", func(f *fakeRealtime) { f.failGeo = true }},
		{"BT7274", "What time is it in Tokyo?", "I can't reach the place lookup right now.", func(f *fakeRealtime) { f.failGeo = true }},
		{"JarvisDE", "Was gibt es Neues?", "Ich erreiche den Nachrichtendienst gerade nicht.", func(f *fakeRealtime) { f.failNews, f.failTS = true, true }},
		{"BT7274", "What's in the news?", "I can't reach the news service right now.", func(f *fakeRealtime) { f.failNews = true }},
	} {
		t.Run(c.q, func(t *testing.T) {
			f, h := realtimeEnv(t, c.voice, "Berlin")
			f.set(c.fail)
			r := rtSay(t, h, chatBody(testClient, c.q))
			if r.text != c.want || hasEvent(r.evs, "error") == nil {
				t.Errorf("got %q (%v)", r.text, r.evs)
			}
			if r.evs[len(r.evs)-1]["done"] != true || hasEvent(r.evs, "meta") == nil {
				t.Errorf("no meta/done after the error: %v", r.evs)
			}
		})
	}
}

func TestRealtimeTagesschauFallsBackToGoogleNews(t *testing.T) {
	f, h := realtimeEnv(t, "JarvisDE", "")
	f.set(func(f *fakeRealtime) { f.failTS = true })
	r := rtSay(t, h, chatBody(testClient, "Was gibt es Neues?"))
	if r.text != "Die aktuellen Schlagzeilen: Top eins. Top zwei. Top drei." {
		t.Errorf("fallback %q", r.text)
	}
	if hl := f.last("/rss").Get("hl"); hl != "de-DE" {
		t.Errorf("fallback language %q", hl)
	}
}

func TestRealtimeCaches(t *testing.T) {
	f, h := realtimeEnv(t, "JarvisDE", "Berlin")
	for i := 0; i < 3; i++ {
		rtSay(t, h, chatBody(testClient, "Wie ist das Wetter?"))
		rtSay(t, h, chatBody(testClient, "Was gibt es Neues?"))
	}
	if n := f.hits("/forecast"); n != 1 {
		t.Errorf("forecast fetched %d times, want 1 (10 min cache)", n)
	}
	if n := f.hits("/geo"); n != 1 {
		t.Errorf("geocode fetched %d times, want 1", n)
	}
	if n := f.hits("/tagesschau"); n != 1 {
		t.Errorf("news fetched %d times, want 1 (15 min cache)", n)
	}
	// Failures are not cached.
	resetRealtimeCaches()
	f.set(func(f *fakeRealtime) { f.failForecast = true })
	rtSay(t, h, chatBody(testClient, "Wie ist das Wetter?"))
	f.set(func(f *fakeRealtime) { f.failForecast = false })
	if r := rtSay(t, h, chatBody(testClient, "Wie ist das Wetter?")); !strings.HasPrefix(r.text, "In Berlin sind es") {
		t.Errorf("after a failure: %q", r.text)
	}
}

func TestRealtimeAsksForTheCity(t *testing.T) {
	for _, c := range []struct {
		voice, q, ask, answer, want string
	}{
		{"JarvisDE", "Wie wird das Wetter morgen?", "Für welchen Ort?", "Hamburg", "Morgen in Hamburg: Regen, 4 bis 9 Grad, Regenrisiko 80 Prozent."},
		{"BT7274", "What's the weather tomorrow?", "For which city?", "in London please", "Tomorrow in London: rain, 4 to 9 degrees, 80 percent chance of rain."},
	} {
		t.Run(c.voice, func(t *testing.T) {
			f, h := realtimeEnv(t, c.voice, "")
			r := rtSay(t, h, chatBody(testClient, c.q))
			if !strings.HasPrefix(r.text, c.ask) || hasEvent(r.evs, "pending") == nil {
				t.Fatalf("no city question: %q %v", r.text, r.evs)
			}
			if f.hits("/geo")+f.hits("/forecast") != 0 {
				t.Error("fetched weather without a city")
			}
			r = rtSay(t, h, chatBody(testClient, c.answer))
			if r.text != c.want {
				t.Errorf("after the answer: %q", r.text)
			}
			if tl := hasEvent(r.evs, "tool"); tl["name"] != "realtime" {
				t.Errorf("tool event %v", r.evs)
			}
		})
	}
}

func TestRealtimeFollowUps(t *testing.T) {
	_, h := realtimeEnv(t, "JarvisDE", "Berlin")
	first := rtSay(t, h, chatBody(testClient, "Wie ist das Wetter in Hamburg?"))
	if !strings.HasPrefix(first.text, "In Hamburg sind es") {
		t.Fatalf("first %q", first.text)
	}
	prev := []map[string]any{
		msg("user", "Wie ist das Wetter in Hamburg?", "realtime:weather", singleSpeakerID, false),
		msg("assistant", first.text, "realtime:weather", singleSpeakerID, false),
	}
	follow := func(q string) rtResult {
		return rtSay(t, h, taggedBody(testClient, append(append([]map[string]any(nil), prev...), msg("user", q, "", "", false))...))
	}
	if r := follow("Und morgen?"); r.text != "Morgen in Hamburg: Regen, 4 bis 9 Grad, Regenrisiko 80 Prozent." || r.src() != "realtime:weather" {
		t.Errorf("und morgen: %q (%s)", r.text, r.src())
	}
	if r := follow("Und in Tokio?"); !strings.HasPrefix(r.text, "Morgen in Tokio:") {
		t.Errorf("und in Tokio keeps the day of the last answer: %q", r.text)
	}
	// Not a follow-up: no realtime reply right before.
	prev[1]["src"] = "persona"
	if r := follow("Und morgen?"); r.src() == "realtime:weather" {
		t.Errorf("follow-up without a realtime answer: %q", r.text)
	}

	// English time follow-up.
	_, h = realtimeEnv(t, "BT7274", "Berlin")
	first = rtSay(t, h, chatBody(testClient, "What time is it in Tokyo?"))
	prev = []map[string]any{
		msg("user", "What time is it in Tokyo?", "realtime:time", singleSpeakerID, false),
		msg("assistant", first.text, "realtime:time", singleSpeakerID, false),
	}
	if r := follow("And in London?"); r.text != "In London it is 2:52 PM." {
		t.Errorf("and in London: %q", r.text)
	}
	if r := follow("and what about you"); r.src() == "realtime:time" {
		t.Errorf("a non-place follow-up was taken as one: %q", r.text)
	}
}

func TestImageGenerationIsDeclinedLocally(t *testing.T) {
	f, h := realtimeEnv(t, "BT7274", "Berlin")
	r := rtSay(t, h, chatBody(testClient, "draw a titan"))
	if !strings.Contains(r.text, "can't draw pictures") || r.src() != "kb" {
		t.Errorf("draw: %q (%s)", r.text, r.src())
	}
	if strings.Contains(r.raw, `"image"`) || len(f.requests()) != 0 {
		t.Errorf("generation request reached a service: %s", r.raw)
	}
	_, h = realtimeEnv(t, "JarvisDE", "Berlin")
	if r := rtSay(t, h, chatBody(testClient, "Male mir ein Bild von einem Leuchtturm")); !strings.HasPrefix(r.text, "Bilder malen kann ich nicht") {
		t.Errorf("male: %q", r.text)
	}
}

// ---------------------------------------------------------------------------
// Parsing

func TestParseRealtimeKinds(t *testing.T) {
	for q, want := range map[string]string{
		"Wie ist das Wetter?":               "weather",
		"Regnet es morgen in Köln?":         "weather",
		"Brauche ich eine Jacke?":           "weather",
		"what's the forecast":               "weather",
		"Wie spät ist es?":                  "time",
		"What day is it?":                   "time",
		"Welches Jahr haben wir?":           "time",
		"Was gibt's Neues?":                 "news",
		"latest headlines":                  "news",
		"Nachrichten aus Bayern":            "news",
		"Gibt es Nachrichten über Gewitter": "news",
	} {
		if !wantsRealtime(q) {
			t.Errorf("%q: not a realtime question", q)
			continue
		}
		if got := parseRealtime(q).Kind; got != want {
			t.Errorf("parseRealtime(%q).Kind = %q, want %q", q, got, want)
		}
	}
	if r := parseRealtime("Welcher Tag ist heute?"); r.Clock || !r.Date {
		t.Errorf("date question %+v", r)
	}
	if r := parseRealtime("what time zone am I in"); !r.Zone {
		t.Errorf("zone question %+v", r)
	}
}

func TestExtractPlace(t *testing.T) {
	for q, want := range map[string]string{
		"Wie ist das Wetter in Berlin?":                   "Berlin",
		"Wie wird das Wetter morgen in Frankfurt am Main": "Frankfurt am Main",
		"Wie wird das Wetter in Frankfurt am Samstag?":    "Frankfurt",
		"Wie spät ist es in New York?":                    "New York",
		"what time is it in new york right now":           "new york",
		"weather in Rio de Janeiro tomorrow":              "Rio de Janeiro",
		"Wetter in Baden-Baden":                           "Baden-Baden",
		"Wetter Hamburg morgen":                           "Hamburg",
		"What's the weather for Paris this weekend?":      "Paris",
		"Wie wird das Wetter in drei Tagen?":              "",
		"Wie wird das Wetter in 3 Tagen?":                 "",
		"What's the weather in the morning?":              "",
		"Wie ist das Wetter für morgen?":                  "",
		"Wie ist das Wetter heute?":                       "",
		"Wie warm ist es in Grad Celsius?":                "",
		"Is it raining in here?":                          "",
		"Wetter in Berlin <<<x>>> https://evil.example":   "Berlin",
	} {
		if got := extractPlace(q); got != want {
			t.Errorf("extractPlace(%q) = %q, want %q", q, got, want)
		}
	}
}

func TestParseSpan(t *testing.T) {
	for q, want := range map[string]string{
		"Wie ist das Wetter?":                    "",
		"Wie ist das Wetter heute?":              "today",
		"Wie war das Wetter heute Morgen":        "today",
		"Wie wird das Wetter morgen früh?":       "d1",
		"weather tomorrow":                       "d1",
		"Wie wird das Wetter übermorgen?":        "d2",
		"weather the day after tomorrow":         "d2",
		"Wetter am Wochenende":                   "weekend",
		"weather this weekend":                   "weekend",
		"Wetter am Samstag":                      "wd6",
		"weather on monday":                      "wd1",
		"Wie wird das Wetter die nächsten Tage?": "week",
		"what's the forecast for the next days":  "week",
	} {
		if got := parseSpan(tokenize(q)); got != want {
			t.Errorf("parseSpan(%q) = %q, want %q", q, got, want)
		}
	}
}

func TestExtractNewsTopic(t *testing.T) {
	for q, want := range map[string]string{
		"Nachrichten über den Klimawandel":     "Klimawandel",
		"Nachrichten aus Bayern bitte":         "Bayern",
		"news about the election today":        "election",
		"latest news on AI":                    "AI",
		"Gibt es Neuigkeiten zu Tesla?":        "Tesla",
		"Was gibt es Neues?":                   "",
		"news from today":                      "",
		"What's in the news?":                  "",
		"news about <<<x>>> https://evil.test": "x",
	} {
		if got := extractNewsTopic(q); got != want {
			t.Errorf("extractNewsTopic(%q) = %q, want %q", q, got, want)
		}
	}
}

func TestResolveDays(t *testing.T) {
	var f openMeteoForecast
	if err := json.Unmarshal([]byte(fakeRTForecast), &f); err != nil {
		t.Fatal(err)
	}
	for span, want := range map[string]string{
		"": "[]", "today": "[0]", "d1": "[1]", "d2": "[2]", "weekend": "[3 4]",
		"week": "[0 1 2 3]", "wd3": "[0]", "wd2": "[6]", "wd1": "[5]",
	} {
		got, ok := resolveDays(span, f, rtNow)
		if !ok || fmt.Sprint(append([]int{}, got...)) != want {
			t.Errorf("resolveDays(%q) = %v %v, want %s", span, got, ok, want)
		}
	}
	// Saturday and Sunday themselves.
	f.Daily.Time[0] = "2026-11-14"
	if got, _ := resolveDays("weekend", f, rtNow); fmt.Sprint(got) != "[0 1]" {
		t.Errorf("weekend on Saturday %v", got)
	}
	f.Daily.Time[0] = "2026-11-15"
	if got, _ := resolveDays("weekend", f, rtNow); fmt.Sprint(got) != "[0]" {
		t.Errorf("weekend on Sunday %v", got)
	}
	// A short forecast does not reach Sunday.
	f.Daily.Time[0] = "2026-11-11"
	f.Daily.Max, f.Daily.Min = f.Daily.Max[:2], f.Daily.Min[:2]
	if _, ok := resolveDays("wd0", f, rtNow); ok {
		t.Error("a day beyond the forecast must not resolve")
	}
}

func TestUTCOffsetAndLongDate(t *testing.T) {
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	india, _ := time.LoadLocation("Asia/Kolkata")
	ny, _ := time.LoadLocation("America/St_Johns")
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{rtNow.In(tokyo), "UTC+9"}, {rtNow.In(india), "UTC+5:30"}, {rtNow.In(ny), "UTC-3:30"}, {rtNow.UTC(), "UTC"},
	} {
		if got := utcOffset(c.t); got != c.want {
			t.Errorf("utcOffset(%v) = %q, want %q", c.t, got, c.want)
		}
	}
	if got := longDate(rtNow, "de"); got != "Mittwoch, der 11. November 2026" {
		t.Errorf("longDate de %q", got)
	}
	if got := longDate(rtNow, "en"); got != "Wednesday, November 11, 2026" {
		t.Errorf("longDate en %q", got)
	}
}

func TestRealtimeTimeAcrossTheDateLine(t *testing.T) {
	_, h := realtimeEnv(t, "JarvisDE", "")
	nowFunc = func() time.Time { return time.Date(2026, 11, 11, 22, 30, 0, 0, berlin) }
	if r := rtSay(t, h, chatBody(testClient, "Wie spät ist es in Tokio?")); r.text != "In Tokio ist es 6:30 Uhr. Dort ist schon Donnerstag." {
		t.Errorf("tokio %q", r.text)
	}
	nowFunc = func() time.Time { return time.Date(2026, 11, 11, 3, 0, 0, 0, berlin) }
	if r := rtSay(t, h, chatBody(testClient, "Wie spät ist es in New York?")); r.text != "In New York ist es 21 Uhr. Dort ist noch Dienstag." {
		t.Errorf("new york %q", r.text)
	}
}

// ---------------------------------------------------------------------------
// Geocoding helpers (moved from the old cloud client)

// Open-Meteo returns nothing for "City, Country"; the lookup must retry with
// the bare name and use the qualifier to pick the right match.
func TestGeocodeQualifiedCity(t *testing.T) {
	var mu sync.Mutex
	var names []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		mu.Lock()
		names = append(names, name)
		mu.Unlock()
		if name != "Frankfurt" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[
			{"latitude":50.11,"longitude":8.68,"name":"Frankfurt am Main","country":"Germany","country_code":"DE","admin1":"Hesse","timezone":"Europe/Berlin"},
			{"latitude":52.34,"longitude":14.55,"name":"Frankfurt (Oder)","country":"Germany","country_code":"DE","admin1":"Brandenburg","timezone":"Europe/Berlin"}]}`))
	}))
	defer srv.Close()
	old := openMeteoGeocodeURL
	openMeteoGeocodeURL = srv.URL
	defer func() { openMeteoGeocodeURL = old }()

	r, err := geocodeCity(context.Background(), "Frankfurt (Oder), Germany", "en")
	if err != nil {
		t.Fatal(err)
	}
	if r.Latitude != 52.34 || r.Name != "Frankfurt (Oder)" || r.Timezone != "Europe/Berlin" {
		t.Errorf("got %+v, want Frankfurt (Oder)", r)
	}
	if len(names) != 2 || names[1] != "Frankfurt" {
		t.Errorf("lookups = %q, want a retry with the bare name", names)
	}
	if r, err := geocodeCity(context.Background(), "Frankfurt, Germany", "en"); err != nil || r.Latitude != 50.11 {
		t.Errorf("an unspecific qualifier should keep the top match, got %v %v", r, err)
	}
	if _, err := geocodeCity(context.Background(), "Atlantis, Ocean", "en"); err == nil {
		t.Error("unknown city should still be an error")
	}
}

func TestBestGeocodeMatchShortCodes(t *testing.T) {
	results := []geocodeResult{
		{Name: "Halle", Country: "Sweden", CountryCode: "SE"},
		{Name: "Halle", Country: "Germany", CountryCode: "DE"},
	}
	if got := bestGeocodeMatch(results, []string{"DE"}); got.Country != "Germany" {
		t.Errorf("DE picked %s, want Germany", got.Country)
	}
}

func TestRealtimeResponsesAreCapped(t *testing.T) {
	f := newFakeRealtime(t)
	_ = f
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[` + strings.Repeat(" ", geocodeMaxBytes) + `]}`))
	}))
	defer big.Close()
	openMeteoGeocodeURL = big.URL
	if _, err := geocodeCity(context.Background(), "Berlin", "de"); err == nil {
		t.Error("an oversized geocode answer must be refused")
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	old := realtimeTimeout
	realtimeTimeout = 100 * time.Millisecond
	defer func() { realtimeTimeout = old }()
	openMeteoGeocodeURL = slow.URL
	start := time.Now()
	if _, err := geocodeCity(context.Background(), "Berlin", "de"); err == nil || time.Since(start) > time.Second {
		t.Errorf("slow geocoder: %v after %v", err, time.Since(start))
	}
}
