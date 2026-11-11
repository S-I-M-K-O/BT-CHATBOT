package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The token every fake HA expects; it must never show up in a response,
// an SSE stream or a log line.
const fakeHATok = "ha-secret-token-3f9a0b1c2d3e4f5a6b7c8d9e"

type fakeHAReq struct {
	Method, Path, Auth string
	Body               map[string]any
}

// fakeHA is an httptest Home Assistant: /api/config, /api/states,
// /api/template (areas), /api/conversation/process and /api/services/*.
type fakeHA struct {
	srv     *httptest.Server
	mu      sync.Mutex
	reqs    []fakeHAReq
	process func(text, lang string) string // response JSON; nil => no_intent_match
	status  int                            // force this status on every request (0 = normal)
	delay   time.Duration
	states  string
	areas   string
}

const fakeHAStates = `[
 {"entity_id":"light.kueche","state":"on","attributes":{"friendly_name":"Küchenlicht"}},
 {"entity_id":"light.wz_decke","state":"on","attributes":{"friendly_name":"Deckenlampe"}},
 {"entity_id":"light.wz_steh","state":"off","attributes":{"friendly_name":"Stehlampe"}},
 {"entity_id":"climate.wohnzimmer","state":"heat","attributes":{"friendly_name":"Heizung Wohnzimmer","current_temperature":20.5,"temperature":21}},
 {"entity_id":"sensor.wz_temp","state":"21.5","attributes":{"friendly_name":"Temperatur Wohnzimmer","device_class":"temperature","unit_of_measurement":"°C"}},
 {"entity_id":"cover.rollladen_kueche","state":"open","attributes":{"friendly_name":"Rollladen Küche","device_class":"shutter"}},
 {"entity_id":"cover.einfahrt","state":"closed","attributes":{"friendly_name":"Einfahrt","device_class":"gate"}},
 {"entity_id":"lock.front","state":"locked","attributes":{"friendly_name":"Front Door"}},
 {"entity_id":"alarm_control_panel.home","state":"armed_away","attributes":{"friendly_name":"Alarmanlage"}},
 {"entity_id":"script.gute_nacht","state":"off","attributes":{"friendly_name":"Gute Nacht"}},
 {"entity_id":"automation.morgens","state":"on","attributes":{"friendly_name":"Morgenroutine"}},
 {"entity_id":"switch.kaffee","state":"off","attributes":{"friendly_name":"Kaffeemaschine"}},
 {"entity_id":"media_player.tv","state":"off","attributes":{"friendly_name":"Fernseher"}},
 {"entity_id":"vacuum.robi","state":"docked","attributes":{"friendly_name":"Robi"}},
 {"entity_id":"Bad ID!","state":"on","attributes":{"friendly_name":"broken"}}
]`

const fakeHAAreas = `[["light.kueche","Küche"],["light.wz_decke","Wohnzimmer"],["light.wz_steh","Wohnzimmer"],
 ["climate.wohnzimmer","Wohnzimmer"],["sensor.wz_temp","Wohnzimmer"],["cover.rollladen_kueche","Küche"],
 ["lock.front","Flur"],["switch.kaffee","Küche"],["media_player.tv","Wohnzimmer"],["cover.einfahrt","Außen"]]`

func newFakeHA(t *testing.T) *fakeHA {
	t.Helper()
	f := &fakeHA{states: fakeHAStates, areas: fakeHAAreas}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHA) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	f.mu.Lock()
	f.reqs = append(f.reqs, fakeHAReq{r.Method, r.URL.Path, r.Header.Get("Authorization"), body})
	status, delay, process, states, areas := f.status, f.delay, f.process, f.states, f.areas
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if status != 0 {
		w.WriteHeader(status)
		io.WriteString(w, `{"message":"upstream detail `+fakeHATok+`"}`)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+fakeHATok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/config":
		io.WriteString(w, `{"version":"2026.10.1","location_name":"Home"}`)
	case r.Method == "GET" && r.URL.Path == "/api/states":
		io.WriteString(w, states)
	case r.Method == "POST" && r.URL.Path == "/api/template":
		io.WriteString(w, areas)
	case r.Method == "POST" && r.URL.Path == "/api/conversation/process":
		text, _ := body["text"].(string)
		lang, _ := body["language"].(string)
		resp := `{"response":{"response_type":"error","data":{"code":"no_intent_match"},"speech":{"plain":{"speech":"Sorry"}}}}`
		if process != nil {
			resp = process(text, lang)
		}
		io.WriteString(w, resp)
	case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/api/services/"):
		io.WriteString(w, `[]`)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeHA) set(fn func(f *fakeHA)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

func (f *fakeHA) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if path == "" || r.Path == path || strings.HasSuffix(path, "*") && strings.HasPrefix(r.Path, strings.TrimSuffix(path, "*")) {
			n++
		}
	}
	return n
}

func (f *fakeHA) last(path string) *fakeHAReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.reqs) - 1; i >= 0; i-- {
		if f.reqs[i].Path == path {
			r := f.reqs[i]
			return &r
		}
	}
	return nil
}

func (f *fakeHA) reset() {
	f.mu.Lock()
	f.reqs = nil
	f.mu.Unlock()
}

func assistDone(speech string) string {
	return `{"response":{"response_type":"action_done","speech":{"plain":{"speech":"` + speech +
		`"}},"data":{"success":[{"id":"light.kueche","type":"entity"}]}}}`
}

// haEnv: temp data and settings, a private registry, a known Anna, no
// router, and a clean HA state before and after the test.
func haEnv(t *testing.T) {
	t.Helper()
	useTempData(t)
	useTempSettings(t)
	withTestRegistry(t)
	t.Setenv("OLLAMA_BASE", "http://127.0.0.1:1") // never reached
	t.Setenv("HA_URL", "")
	t.Setenv("HA_TOKEN", "")
	personExistsProvider = func(id string) bool { return id == "p_anna" }
	personNameProvider = func(id string) (string, bool) { return "Anna", id == "p_anna" }
	haReset()
	t.Cleanup(func() { haBG.Wait(); haReset() })
}

// haConnect points HA_URL/HA_TOKEN at the fake.
func haConnect(t *testing.T, f *fakeHA) {
	t.Helper()
	t.Setenv("HA_URL", f.srv.URL)
	t.Setenv("HA_TOKEN", fakeHATok)
	haReset()
}

// haWarm fills the entity cache and forgets the requests it took.
func haWarm(t *testing.T, f *fakeHA) {
	t.Helper()
	if _, err := haEntities(context.Background(), 0); err != nil {
		t.Fatalf("warm: %v", err)
	}
	f.reset()
}

func setHomeSettings(t *testing.T, control, queries string) {
	t.Helper()
	s := loadSettings()
	s.HomeControl, s.HomeQueries = control, queries
	if err := saveSettings(s); err != nil {
		t.Fatal(err)
	}
}

func homeTurn(text, lang string, sp Speaker) (*Turn, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	tagged := []reqMessage{{Role: "user", Content: text}}
	return &Turn{
		Ctx: context.Background(), Settings: loadSettings(), Lang: lang, Loc: time.UTC, Now: nowFunc(),
		Query: text, Tokens: tokenize(text), Speaker: sp, ClientID: testClient, Spoken: true,
		Tagged: tagged, History: plainMessages(tagged), w: rec,
	}, rec
}

var (
	haAnna  = Speaker{ID: "p_anna", Name: "Anna", Source: SourceVoice}
	haGuest = Speaker{Source: SourceGuest}
)

// sayHome runs one utterance through the pending answer and the fast paths.
// It returns the reply text ("" when no fast path took it), the events and
// the turn.
func sayHome(t *testing.T, text, lang string, sp Speaker) (string, []map[string]any, *Turn) {
	t.Helper()
	tr, rec := homeTurn(text, lang, sp)
	if !runFastPaths(tr) {
		return "", nil, tr
	}
	evs := rawEvents(t, rec.Body.String())
	var b strings.Builder
	for _, ev := range evs {
		if d, ok := ev["delta"].(string); ok {
			b.WriteString(d)
		}
	}
	return b.String(), evs, tr
}

// captureLog collects log output for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	var mu sync.Mutex
	prev := log.Writer()
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}))
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// ---------------------------------------------------------------------------
// Assist path and fallback, German and English

func TestHomeAssistDE(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	haWarm(t, f)
	f.set(func(f *fakeHA) {
		f.process = func(text, lang string) string { return assistDone("Licht ausgeschaltet") }
	})
	text, evs, tr := sayHome(t, "Hermes, schalte bitte das Licht in der Küche aus", "de", haAnna)
	if text != "Licht ausgeschaltet" {
		t.Fatalf("reply %q", text)
	}
	req := f.last("/api/conversation/process")
	if req == nil || req.Body["text"] != "Schalte das Licht in der Küche aus" || req.Body["language"] != "de" {
		t.Fatalf("assist request %+v", req)
	}
	if req.Auth != "Bearer "+fakeHATok {
		t.Errorf("auth header %q", req.Auth)
	}
	if _, ok := req.Body["conversation_id"]; ok {
		t.Error("no conversation id may be sent")
	}
	if c := hasEvent(evs, "card"); c == nil || c["kind"] != "home" {
		t.Errorf("card %v", evs)
	}
	if tl := hasEvent(evs, "tool"); tl == nil || tl["name"] != "home_control" {
		t.Errorf("tool event %v", evs)
	}
	if tr.handledBy != "fast:home_control" {
		t.Errorf("handledBy %q", tr.handledBy)
	}
	if n := f.count("/api/services/*"); n != 0 {
		t.Errorf("services called %d times after action_done", n)
	}
}

func TestHomeAssistEnglishVoice(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	haWarm(t, f)
	f.set(func(f *fakeHA) {
		f.process = func(text, lang string) string {
			if lang != "en" {
				return `{"response":{"response_type":"error","data":{"code":"no_intent_match"}}}`
			}
			return assistDone("Turned on the lights")
		}
	})
	text, _, _ := sayHome(t, "Turn on the living room lights please", "en", haAnna)
	if text != "Turned on the lights" {
		t.Fatalf("reply %q", text)
	}
	if req := f.last("/api/conversation/process"); req.Body["text"] != "Turn on the living room lights" || req.Body["language"] != "en" {
		t.Errorf("assist request %+v", req.Body)
	}
}

// An English sentence about a German HA (area "Wohnzimmer"): Assist does
// not match, the room synonym resolves the lights, the fallback calls the
// whitelisted service and the reply is English.
func TestHomeFallbackEnglishToGermanAreas(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	haWarm(t, f)
	text, _, _ := sayHome(t, "turn on the living room light", "en", haAnna)
	if text != "Turned on Deckenlampe and Stehlampe." {
		t.Fatalf("reply %q", text)
	}
	req := f.last("/api/services/light/turn_on")
	if req == nil {
		t.Fatal("light.turn_on not called")
	}
	ids, _ := req.Body["entity_id"].([]any)
	if len(ids) != 2 || ids[0] != "light.wz_decke" || ids[1] != "light.wz_steh" {
		t.Errorf("entity_id %v", req.Body)
	}
	if f.count("/api/conversation/process") != 1 {
		t.Error("Assist must be tried first")
	}
}

func TestHomeFallbackClimateDEandEN(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	haWarm(t, f)
	text, _, _ := sayHome(t, "Schalte die Heizung im Wohnzimmer auf 19 Grad", "de", haAnna)
	if text != "Heizung Wohnzimmer auf 19 Grad gestellt." {
		t.Fatalf("DE reply %q", text)
	}
	req := f.last("/api/services/climate/set_temperature")
	if req == nil || req.Body["temperature"] != float64(19) {
		t.Fatalf("set_temperature %+v", req)
	}
	// English, no room: the only thermostat is meant.
	text, _, _ = sayHome(t, "set the heating to 21.5 degrees", "en", haAnna)
	if text != "Set Heizung Wohnzimmer to 21.5 degrees." {
		t.Fatalf("EN reply %q", text)
	}
	if req := f.last("/api/services/climate/set_temperature"); req.Body["temperature"] != 21.5 {
		t.Errorf("temperature %v", req.Body)
	}
	// Out of range: nothing is called.
	before := f.count("/api/services/*")
	text, _, _ = sayHome(t, "Stell die Heizung im Wohnzimmer auf 45 Grad", "de", haAnna)
	if !strings.Contains(text, "zwischen 5 und 30 Grad") || f.count("/api/services/*") != before {
		t.Errorf("range: %q, services %d→%d", text, before, f.count("/api/services/*"))
	}
}

func TestHomeFallbackCoverAndDim(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	haWarm(t, f)
	if text, _, _ := sayHome(t, "Mach den Rollladen in der Küche zu", "de", haAnna); text != "Rollladen Küche geschlossen." {
		t.Errorf("cover: %q", text)
	}
	if f.last("/api/services/cover/close_cover") == nil {
		t.Error("close_cover not called")
	}
	if text, _, _ := sayHome(t, "Dimm die Stehlampe auf 40 Prozent", "de", haAnna); text != "Stehlampe auf 40 Prozent gedimmt." {
		t.Errorf("dim: %q", text)
	}
	if req := f.last("/api/services/light/turn_on"); req == nil || req.Body["brightness_pct"] != float64(40) {
		t.Errorf("brightness %+v", req)
	}
	if text, _, _ := sayHome(t, "switch off the coffee machine Kaffeemaschine", "en", haAnna); text != "Turned off Kaffeemaschine." {
		t.Errorf("switch: %q", text)
	}
	// "Dimm die Stehlampe" without a value (fast path, Assist did not match)
	// and a confirmed router offer without a value: the light is switched on
	// and the reply says so, never "auf NaN Prozent".
	if text, _, _ := sayHome(t, "Dimm die Stehlampe", "de", haAnna); text != "Stehlampe eingeschaltet." {
		t.Errorf("dim without value: %q", text)
	}
	for _, c := range []struct{ args, lang, want string }{
		{`{"target":"Stehlampe","action":"set"}`, "de", "Stehlampe eingeschaltet."},
		{`{"target":"Stehlampe","action":"dim"}`, "en", "Turned on Stehlampe."},
	} {
		tr, _ := homeTurn("ja", c.lang, haAnna)
		if r := runHomeControl(tr, json.RawMessage(c.args)); r.Text != c.want {
			t.Errorf("%s (%s): %q", c.args, c.lang, r.Text)
		}
	}
}

func TestHomeAssistLanguageSetting(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	s := loadSettings()
	s.HALanguage = "de"
	if err := saveSettings(s); err != nil {
		t.Fatal(err)
	}
	haWarm(t, f)
	f.set(func(f *fakeHA) {
		f.process = func(text, lang string) string { return assistDone("Licht eingeschaltet") }
	})
	// English voice, German HA: Assist gets "de"; the reply stays English.
	text, _, _ := sayHome(t, "turn on the Stehlampe", "en", haAnna)
	if req := f.last("/api/conversation/process"); req.Body["language"] != "de" {
		t.Errorf("language %v", req.Body["language"])
	}
	if text != "Turned on Stehlampe." {
		t.Errorf("reply %q", text)
	}
}

// ---------------------------------------------------------------------------
// Pre-classification: refused before anything is sent

func TestHomeRefusesBlockedBeforeAnyRequest(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	haWarm(t, f)
	f.set(func(f *fakeHA) {
		f.process = func(string, string) string { return assistDone("done") }
	})
	cases := []struct{ text, lang string }{
		{"Schließ die Haustür auf", "de"},
		{"Öffne das Garagentor", "de"},
		{"Mach die Alarmanlage aus", "de"},
		{"Schalte Gute Nacht ein", "de"},        // a script, named
		{"Öffne die Einfahrt", "de"},            // a gate cover, no keyword
		{"Schalte die Morgenroutine aus", "de"}, // an automation
		{"unlock the front door", "en"},         // keyword
		{"open the garage door", "en"},          // keyword
		{"disarm the alarm system", "en"},       // keyword (a bare "alarm" is a reminder alarm)
		{"Entriegle die Haustür", "de"},         // lock verb, no switching verb
		{"lock the front door", "en"},
		{"switch on Front Door please", "en"},          // a lock, named
		{"Hermes, öffne bitte die Terrassentür", "de"}, // compound
	}
	for _, c := range cases {
		text, _, tr := sayHome(t, c.text, c.lang, haAnna)
		want := pick(c.lang, "For safety reasons I don't control doors, locks, gates or alarm systems.",
			"Türen, Schlösser, Tore und Alarmanlagen steuere ich aus Sicherheitsgründen nicht.")
		if text != want {
			t.Errorf("%q: reply %q", c.text, text)
		}
		if !tr.refused {
			t.Errorf("%q: not marked as refused", c.text)
		}
	}
	if n := f.count(""); n != 0 {
		t.Errorf("HA got %d requests for refused commands: %+v", n, f.reqs)
	}
	// Also when the cache is cold: keywords need no entity list.
	haReset()
	f.reset()
	if text, _, _ := sayHome(t, "Öffne das Garagentor", "de", haAnna); !strings.Contains(text, "Sicherheitsgründen") {
		t.Errorf("cold: %q", text)
	}
	haBG.Wait()
	if n := f.count(""); n != 0 {
		t.Errorf("cold cache: %d requests", n)
	}
	// An unsupported domain (vacuum) is not sent either.
	haWarm(t, f)
	if text, _, _ := sayHome(t, "Schalte Robi ein", "de", haAnna); !strings.Contains(text, "Dieses Gerät steuere ich nicht") {
		t.Errorf("vacuum: %q", text)
	}
	if n := f.count("/api/conversation/process") + f.count("/api/services/*"); n != 0 {
		t.Errorf("vacuum sent %d", n)
	}
	// Nor when the same sentence also names an allowed device: Assist would
	// get the whole sentence and switch the vacuum too (DE and EN).
	for _, c := range []struct{ text, lang, want string }{
		{"Schalte das Küchenlicht und Robi ein", "de", "Dieses Gerät steuere ich nicht"},
		{"Turn on the Stehlampe and Robi", "en", "I don't control that device"},
	} {
		f.reset()
		text, _, tr := sayHome(t, c.text, c.lang, haAnna)
		if !strings.Contains(text, c.want) || !tr.refused {
			t.Errorf("%q: reply %q refused=%v", c.text, text, tr.refused)
		}
		if n := f.count("/api/conversation/process") + f.count("/api/services/*"); n != 0 {
			t.Errorf("%q: mixed command sent %d requests to HA", c.text, n)
		}
	}
}

func TestHomeServiceWhitelist(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	cl, _, err := haClientFor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	bad := []struct {
		domain, service string
		data            map[string]any
	}{
		{"lock", "unlock", map[string]any{"entity_id": []string{"lock.front"}}},
		{"alarm_control_panel", "alarm_disarm", map[string]any{"entity_id": []string{"alarm_control_panel.home"}}},
		{"script", "turn_on", map[string]any{"entity_id": []string{"script.gute_nacht"}}},
		{"automation", "trigger", map[string]any{"entity_id": []string{"automation.morgens"}}},
		{"light", "toggle", map[string]any{"entity_id": []string{"light.kueche"}}},
		{"light", "turn_on", map[string]any{"entity_id": []string{"lock.front"}}},                    // wrong domain
		{"light", "turn_on", map[string]any{"entity_id": []string{"light.kueche"}, "flash": "long"}}, // unknown key
		{"light", "turn_on", map[string]any{"entity_id": []string{"light.kueche"}, "brightness_pct": 400.0}},
		{"climate", "set_temperature", map[string]any{"entity_id": []string{"climate.wohnzimmer"}, "temperature": 35.0}},
		{"climate", "set_temperature", map[string]any{"entity_id": []string{"climate.wohnzimmer"}}},
		{"climate", "set_hvac_mode", map[string]any{"entity_id": []string{"climate.wohnzimmer"}, "hvac_mode": "cool"}},
		{"homeassistant", "restart", map[string]any{}},
		{"light", "turn_on", map[string]any{}},
	}
	for _, b := range bad {
		if err := cl.callService(ctx, b.domain, b.service, b.data); haCode(err) != "not_allowed" {
			t.Errorf("%s.%s %v: %v", b.domain, b.service, b.data, err)
		}
	}
	// Paths outside the list are refused before any request.
	for _, p := range []string{"/api/", "/api/events", "/api/config/core/check_config", "/api/services/lock/unlock", "/api/states/../config", "/api/hassio/x"} {
		if err := cl.do(ctx, http.MethodPost, p, map[string]any{}, nil, haBodyMax); haCode(err) != "not_allowed" {
			t.Errorf("POST %s: %v", p, err)
		}
	}
	if err := cl.do(ctx, http.MethodDelete, "/api/states", nil, nil, haBodyMax); haCode(err) != "not_allowed" {
		t.Errorf("DELETE: %v", err)
	}
	if n := f.count(""); n != 0 {
		t.Fatalf("%d requests reached HA", n)
	}
	if err := cl.callService(ctx, "climate", "set_temperature", map[string]any{"entity_id": []string{"climate.wohnzimmer"}, "temperature": 21.0}); err != nil {
		t.Errorf("allowed call: %v", err)
	}
	// A garage cover never gets a service call, even from structured args.
	haWarm(t, f)
	tr, _ := homeTurn("ja", "de", haAnna)
	r := runHomeControl(tr, json.RawMessage(`{"target":"Einfahrt","action":"open"}`))
	if !strings.Contains(r.Text, "Sicherheitsgründen") || f.count("/api/services/*") != 0 {
		t.Errorf("gate via args: %q", r.Text)
	}
}

// ---------------------------------------------------------------------------
// Who may control and query

func TestHomePermissions(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	haWarm(t, f)
	guestRefusal := "Das mache ich nur für Personen, die ich kenne. Stell dich mit „Ich bin …“ vor."

	// Default: only known people may control or ask; a guest is refused
	// without any request to HA.
	if text, _, _ := sayHome(t, "Mach das Licht in der Küche aus", "de", haGuest); text != guestRefusal {
		t.Errorf("default + guest: %q", text)
	}
	if n := f.count("/api/services/*"); n != 0 {
		t.Errorf("default refused guest control sent %d service calls", n)
	}
	setHomeSettings(t, "everyone", "known")
	if text, _, _ := sayHome(t, "Mach das Licht in der Küche aus", "de", haGuest); text != "Küchenlicht ausgeschaltet." {
		t.Errorf("everyone + guest: %q", text)
	}
	f.reset()
	if text, _, _ := sayHome(t, "Ist das Licht in der Küche an?", "de", haGuest); text != guestRefusal {
		t.Errorf("queries known + guest: %q", text)
	}
	if text, _, _ := sayHome(t, "Ist die Haustür zu?", "de", haGuest); text != guestRefusal {
		t.Errorf("door query + guest: %q", text)
	}
	if text, _, _ := sayHome(t, "Ist die Haustür zu?", "de", haAnna); !strings.Contains(text, "keine Auskunft") {
		t.Errorf("door query + Anna: %q", text)
	}
	if n := f.count(""); n != 0 {
		t.Errorf("refused queries sent %d requests", n)
	}

	setHomeSettings(t, "known", "everyone")
	if text, _, _ := sayHome(t, "Mach das Licht in der Küche aus", "de", haGuest); text != guestRefusal {
		t.Errorf("known + guest: %q", text)
	}
	if text, _, _ := sayHome(t, "turn off the kitchen light", "en", Speaker{ID: "p_anna", Source: SourceSticky}); !strings.Contains(text, "not sure who is speaking") {
		t.Errorf("known + sticky (EN): %q", text)
	}
	if n := f.count(""); n != 0 {
		t.Errorf("refused control sent %d requests", n)
	}
	if text, _, _ := sayHome(t, "Is the kitchen light on?", "en", haGuest); text != "Küchenlicht is on." {
		t.Errorf("queries everyone + guest: %q", text)
	}
	if text, _, _ := sayHome(t, "Mach das Licht in der Küche aus", "de", haAnna); text != "Küchenlicht ausgeschaltet." {
		t.Errorf("known + Anna: %q", text)
	}

	// Uncertain speaker: asked who is speaking; nothing runs.
	f.reset()
	text, evs, _ := sayHome(t, "Mach das Licht in der Küche an", "de", Speaker{Source: SourceUncertain})
	if !strings.HasPrefix(text, "Wer spricht gerade?") || hasEvent(evs, "pending") == nil || f.count("") != 0 {
		t.Errorf("uncertain: %q %v", text, evs)
	}

	// off: no control at all, an honest hint, queries still work.
	setHomeSettings(t, "off", "everyone")
	f.reset()
	if text, _, _ := sayHome(t, "Mach das Licht in der Küche aus", "de", haAnna); text != "Die Gerätesteuerung ist in den Einstellungen ausgeschaltet." {
		t.Errorf("off: %q", text)
	}
	if text, _, _ := sayHome(t, "turn off the kitchen light", "en", haAnna); text != "Device control is turned off in the settings." {
		t.Errorf("off EN: %q", text)
	}
	if f.count("/api/conversation/process")+f.count("/api/services/*") != 0 {
		t.Error("off sent commands")
	}
	tr, _ := homeTurn("x", "de", haAnna)
	if lookupTool("home_control").enabled(tr) {
		t.Error("home_control enabled with homeControl=off")
	}
	if !lookupTool("home_query").enabled(tr) {
		t.Error("home_query must stay enabled")
	}
	if text, _, _ := sayHome(t, "Wie warm ist es im Wohnzimmer?", "de", haGuest); text != "Im Wohnzimmer sind es 21,5 Grad." {
		t.Errorf("query with control off: %q", text)
	}
}

func TestHomeAccessClasses(t *testing.T) {
	haEnv(t)
	tr, _ := homeTurn("x", "de", haAnna)
	tr.Settings.HomeControl, tr.Settings.HomeQueries = "everyone", "known"
	if homeControlAccess(tr, json.RawMessage(`{"action":"on"}`)) != AccessOpen ||
		homeControlAccess(tr, json.RawMessage(`{"action":"query"}`)) != AccessRead ||
		homeQueryAccess(tr, nil) != AccessRead {
		t.Error("everyone/known")
	}
	tr.Settings.HomeControl, tr.Settings.HomeQueries = "known", "everyone"
	if homeControlAccess(tr, json.RawMessage(`{"action":"off"}`)) != AccessWrite || homeQueryAccess(tr, nil) != AccessOpen {
		t.Error("known/everyone")
	}
	tr.Settings = nil
	if homeControlAccess(tr, nil) != AccessWrite || homeQueryAccess(tr, nil) != AccessRead {
		t.Error("nil settings must fail closed")
	}
}

// ---------------------------------------------------------------------------
// Queries

func TestHomeQueriesDEandEN(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	haWarm(t, f)
	cases := []struct{ text, lang, want string }{
		{"Wie warm ist es im Wohnzimmer?", "de", "Im Wohnzimmer sind es 21,5 Grad."},
		{"How warm is it in the living room?", "en", "It's 21.5 degrees in Wohnzimmer."},
		{"Ist das Licht in der Küche an?", "de", "Küchenlicht ist an."},
		{"Is the kitchen light on?", "en", "Küchenlicht is on."},
		{"Ist die Stehlampe an?", "de", "Stehlampe ist aus."},
		{"Ist im Wohnzimmer Licht an?", "de", "Im Wohnzimmer ist an: Deckenlampe."},
		{"Are the lights on in the living room?", "en", "On in Wohnzimmer: Deckenlampe."},
		{"Ist der Rollladen in der Küche zu?", "de", "Rollladen Küche ist offen."},
		{"Ist die Heizung an?", "de", "Heizung Wohnzimmer ist an, eingestellt auf 21 Grad."},
	}
	for _, c := range cases {
		text, evs, tr := sayHome(t, c.text, c.lang, haAnna)
		if text != c.want {
			t.Errorf("%q: %q, want %q", c.text, text, c.want)
			continue
		}
		if tr.handledBy != "fast:home_query" || hasEvent(evs, "card") == nil {
			t.Errorf("%q: handled by %q", c.text, tr.handledBy)
		}
	}
	if n := f.count("/api/conversation/process") + f.count("/api/services/*"); n != 0 {
		t.Errorf("queries sent %d commands", n)
	}
	// Not smart-home questions: left to the rest of the pipeline.
	for _, q := range []string{"Wie warm ist es?", "How warm is it in Berlin?", "Was ist eine Tür?", "Ist in der Küche noch Kaffee?",
		"Wie stelle ich die Heizung auf 21 Grad?", "Was macht die Alarmanlage?"} {
		if fp, _ := firstHomeMatch(q, "de"); strings.HasPrefix(fp, "home_") {
			t.Errorf("%q matched %s", q, fp)
		}
	}
}

// firstHomeMatch returns the first fast path of any feature that takes the
// utterance.
func firstHomeMatch(text, lang string) (string, json.RawMessage) {
	tr, _ := homeTurn(text, lang, haAnna)
	for _, f := range fastPaths() {
		if args, ok := f.Match(tr); ok {
			if tool := lookupTool(f.Tool); tool != nil && tool.enabled(tr) {
				return f.Name, args
			}
		}
	}
	return "", nil
}

// ---------------------------------------------------------------------------
// Unconfigured, negatives and the router

func TestHomeUnconfigured(t *testing.T) {
	haEnv(t)
	if text, _, _ := sayHome(t, "Mach das Licht an", "de", haGuest); text != haSetupText("de") {
		t.Errorf("DE hint: %q", text)
	}
	if text, _, _ := sayHome(t, "Turn on the lights", "en", haGuest); text != haSetupText("en") {
		t.Errorf("EN hint: %q", text)
	}
	for _, q := range []string{"Erzähl mir was über Licht", "Tell me something about light", "Öffne das Garagentor", "Ist das Licht an?"} {
		if fp, _ := firstHomeMatch(q, "de"); strings.HasPrefix(fp, "home_") {
			t.Errorf("%q matched %s", q, fp)
		}
	}
	tr, _ := homeTurn("Mach das Licht an", "de", haAnna)
	for _, name := range []string{"home_control", "home_query"} {
		if lookupTool(name).enabled(tr) {
			t.Errorf("%s enabled without HA", name)
		}
	}
	if haGateFunc(tr) {
		t.Error("gate open without HA")
	}
	// A URL with a path is not a usable config.
	t.Setenv("HA_URL", "http://192.0.2.1:8123/api")
	t.Setenv("HA_TOKEN", fakeHATok)
	if haConfigured() {
		t.Error("URL with a path accepted")
	}
}

func TestHomeNegativesConfigured(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	haWarm(t, f)
	for _, q := range []string{
		"Erzähl mir was über Licht", "Mir ist kalt", "Ich bin in der Küche", "Was steht heute in der Zeitung?",
		"Ich muss einen Brief schreiben", "I forgot to buy milk yesterday", "Soll ich mir ein neues Handy kaufen?",
		"Open the pod bay doors", // keyword, but a question-free command: refused, never chat
	} {
		fp, _ := firstHomeMatch(q, "de")
		if q == "Open the pod bay doors" {
			if fp != "home_control" {
				t.Errorf("%q: %q", q, fp)
			}
			continue
		}
		if strings.HasPrefix(fp, "home_") {
			t.Errorf("%q matched %s", q, fp)
		}
	}
}

func TestHomeRouterOfferThenConfirm(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	haWarm(t, f)
	rf := newRouterFake(t)
	rf.reply = toolCall("home_control", `{"target":"Wohnzimmer","action":"set","value":22}`)
	s := loadSettings()
	s.RouterEnabled = true
	if err := saveSettings(s); err != nil {
		t.Fatal(err)
	}
	tr, rec := homeTurn("Mir ist kalt im Wohnzimmer", "de", haAnna)
	if runFastPaths(tr) {
		t.Fatal("a statement must not take a fast path")
	}
	if !gateMatches(tr) {
		t.Fatal("area names open the router gate")
	}
	if !runRouter(tr) {
		t.Fatal("router did not handle the call")
	}
	text := joinRaw(t, rec.Body.String())
	if text != "Soll ich die Heizung im Wohnzimmer auf 22 Grad stellen?" || tr.handledBy != "offer" {
		t.Fatalf("offer %q (%s)", text, tr.handledBy)
	}
	if n := f.count("/api/conversation/process") + f.count("/api/services/*"); n != 0 {
		t.Fatalf("offer changed something: %d", n)
	}
	// The enum lists allowed names only.
	tools := rf.lastRouter(t)["tools"].([]any)
	var enum []any
	for _, x := range tools {
		fn := x.(map[string]any)["function"].(map[string]any)
		if fn["name"] == "home_control" {
			enum = fn["parameters"].(map[string]any)["properties"].(map[string]any)["target"].(map[string]any)["enum"].([]any)
		}
	}
	joined := ""
	for _, e := range enum {
		joined += e.(string) + "|"
	}
	for _, want := range []string{"Wohnzimmer|", "Küche|", "Stehlampe|", "Heizung Wohnzimmer|"} {
		if !strings.Contains(joined, want) {
			t.Errorf("enum misses %s: %s", want, joined)
		}
	}
	for _, bad := range []string{"Front Door", "Alarmanlage", "Gute Nacht", "Morgenroutine", "Einfahrt", "Robi", "Temperatur Wohnzimmer"} {
		if strings.Contains(joined, bad) {
			t.Errorf("enum lists %s: %s", bad, joined)
		}
	}
	// "ja" runs the whitelisted service with the offered args.
	text, _, _ = sayHome(t, "ja", "de", haAnna)
	if text != "Heizung Wohnzimmer auf 22 Grad gestellt." {
		t.Fatalf("confirm: %q", text)
	}
	if req := f.last("/api/services/climate/set_temperature"); req == nil || req.Body["temperature"] != float64(22) {
		t.Errorf("set_temperature %+v", req)
	}
	if f.count("/api/conversation/process") != 0 {
		t.Error("a bare 'ja' must never reach Assist")
	}
	// With an imperative the router result runs directly (English).
	rf.reply = toolCall("home_control", `{"target":"Stehlampe","action":"on"}`)
	f.set(func(f *fakeHA) { f.process = func(string, string) string { return assistDone("Turned on the light") } })
	tr, rec = homeTurn("could you turn on the Stehlampe and also something else entirely", "en", haAnna)
	tr.Tokens = tokenize(tr.Query)
	if !runRouter(tr) {
		t.Fatal("router")
	}
	if got := joinRaw(t, rec.Body.String()); got != "Turned on the light" {
		t.Errorf("direct: %q", got)
	}
}

func joinRaw(t *testing.T, body string) string {
	t.Helper()
	var b strings.Builder
	for _, ev := range rawEvents(t, body) {
		if d, ok := ev["delta"].(string); ok {
			b.WriteString(d)
		}
	}
	return b.String()
}

func TestHomeOfferTexts(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	haWarm(t, f)
	tr, _ := homeTurn("x", "en", haAnna)
	cases := map[string]string{
		`{"target":"Wohnzimmer","action":"set","value":21}`:     "Should I set the heating in Wohnzimmer to 21 degrees?",
		`{"target":"Stehlampe","action":"off"}`:                 "Should I turn off Stehlampe?",
		`{"target":"Küche","action":"close","device":"blinds"}`: "Should I close the blinds in Küche?",
		`{"target":"Stehlampe","action":"dim","value":"30"}`:    "Should I dim Stehlampe to 30 percent?",
	}
	for args, want := range cases {
		if got := homeOffer(tr, json.RawMessage(args)); got != want {
			t.Errorf("%s: %q", args, got)
		}
	}
	tr.Lang = "de"
	if got := homeOffer(tr, json.RawMessage(`{"target":"Küche","action":"on","device":"light"}`)); got != "Soll ich das Licht in der Küche einschalten?" {
		t.Errorf("DE: %q", got)
	}
}

// ---------------------------------------------------------------------------
// Failures: unreachable, auth, timeout

func TestHomeFailureReplies(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	haWarm(t, f)
	logs := captureLog(t)

	f.set(func(f *fakeHA) { f.status = http.StatusUnauthorized })
	text, evs, _ := sayHome(t, "Mach das Licht in der Küche an", "de", haAnna)
	if text != "Der Home-Assistant-Zugang ist ungültig. Bitte das Token in den Einstellungen erneuern." || hasEvent(evs, "error") == nil {
		t.Errorf("401: %q", text)
	}
	text, _, _ = sayHome(t, "turn on the kitchen light", "en", haAnna)
	if text != "The Home Assistant access is invalid. Please renew the token in the settings." {
		t.Errorf("401 EN: %q", text)
	}

	old := haTimeout
	haTimeout = 150 * time.Millisecond
	t.Cleanup(func() { haTimeout = old })
	f.set(func(f *fakeHA) { f.status, f.delay = 0, 3*time.Second })
	start := time.Now()
	text, _, _ = sayHome(t, "Mach das Licht in der Küche an", "de", haAnna)
	if text != "Home Assistant hat zu lange gebraucht. Versuch es gleich noch einmal." {
		t.Errorf("timeout: %q", text)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("timeout took %s", time.Since(start))
	}
	f.set(func(f *fakeHA) { f.delay = 0 })

	f.srv.Close()
	text, _, _ = sayHome(t, "Mach das Licht in der Küche an", "de", haAnna)
	if text != "Home Assistant antwortet gerade nicht." {
		t.Errorf("unreachable: %q", text)
	}
	text, _, _ = sayHome(t, "Turn on the kitchen light", "en", haAnna)
	if text != "Home Assistant is not responding right now." {
		t.Errorf("unreachable EN: %q", text)
	}
	if strings.Contains(logs.String(), fakeHATok) || strings.Contains(logs.String(), "upstream detail") {
		t.Errorf("log leaks: %s", logs.String())
	}
}

func TestHomeStatusCodes(t *testing.T) {
	haEnv(t)
	if st := haStatusDetached(context.Background(), false); st.Configured || st.Code != "not_configured" {
		t.Errorf("unconfigured %+v", st)
	}
	f := newFakeHA(t)
	haConnect(t, f)
	st := haStatusDetached(context.Background(), false)
	if !st.OK || st.Code != "ok" || st.Version != "2026.10.1" || st.Entities != 13 || st.Blocked != 5 {
		t.Errorf("ok %+v", st)
	}
	n := f.count("")
	for i := 0; i < 3; i++ {
		haStatusDetached(context.Background(), false)
	}
	if f.count("") != n {
		t.Errorf("status not cached: %d → %d", n, f.count(""))
	}
	if st := collectStatus(context.Background())["home"].(haStatusView); !st.OK {
		t.Errorf("provider %+v", st)
	}
	// A config change drops the cache (secrets.go runs the hook).
	runSecretHooks(&haChangeHooks)
	haStatusDetached(context.Background(), false)
	if f.count("") == n {
		t.Error("config change did not drop the status cache")
	}

	f.set(func(f *fakeHA) { f.status = http.StatusForbidden })
	if st := haStatusDetached(context.Background(), true); st.OK || st.Code != "auth" {
		t.Errorf("403 %+v", st)
	}
	f.set(func(f *fakeHA) { f.status = http.StatusInternalServerError })
	if st := haStatusDetached(context.Background(), true); st.Code != "bad_response" {
		t.Errorf("500 %+v", st)
	}
	f.set(func(f *fakeHA) { f.status = 0; f.states = `{"not":"a list"}` })
	if st := haStatusDetached(context.Background(), true); st.Code != "bad_response" {
		t.Errorf("bad json %+v", st)
	}
	old := haTimeout
	haTimeout = 100 * time.Millisecond
	t.Cleanup(func() { haTimeout = old })
	f.set(func(f *fakeHA) { f.states = fakeHAStates; f.delay = 2 * time.Second })
	if st := haStatusDetached(context.Background(), true); st.Code != "timeout" {
		t.Errorf("slow %+v", st)
	}
	f.set(func(f *fakeHA) { f.delay = 0 })
	f.srv.Close()
	if st := haStatusDetached(context.Background(), true); st.Code != "unreachable" {
		t.Errorf("down %+v", st)
	}
}

// Status polls that arrive while a probe is still running (a slow HA, several
// tabs, the status page) share that probe instead of each sending their own
// requests to Home Assistant.
func TestHomeStatusConcurrentPollsShareOneProbe(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	f.set(func(f *fakeHA) { f.delay = 150 * time.Millisecond })
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if st := haStatusDetached(context.Background(), false); !st.OK {
				t.Errorf("status %+v", st)
			}
		}()
	}
	wg.Wait()
	if n := f.count("/api/config"); n != 1 {
		t.Errorf("%d concurrent status polls sent %d /api/config requests, want 1", 8, n)
	}
}

// ---------------------------------------------------------------------------
// Pinning: only the configured host:port, no redirects, no proxy

func TestHomeClientPinning(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	other := newFakeHA(t) // must never see a request
	haConnect(t, f)
	cl, _, err := haClientFor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cl.tr.Proxy != nil {
		t.Error("the HA transport must not use a proxy")
	}
	otherAddr := strings.TrimPrefix(other.srv.URL, "http://")
	if _, err := cl.dial(context.Background(), "tcp", otherAddr); err == nil {
		t.Error("dial to another port succeeded")
	}
	if err := cl.control("tcp", otherAddr, nil); err == nil {
		t.Error("control allowed another port")
	}
	if err := cl.control("tcp", "10.0.0.1:"+strings.Split(otherAddr, ":")[1], nil); err == nil {
		t.Error("control allowed another host")
	}
	// A request whose URL names another host is refused at dial time.
	req, _ := http.NewRequest(http.MethodGet, other.srv.URL+"/api/states", nil)
	if resp, err := cl.http.Do(req); err == nil {
		resp.Body.Close()
		t.Error("the pinned client reached another server")
	}

	// Redirects are never followed, and the token never travels with one.
	f.set(func(fk *fakeHA) {
		fk.states = "" // unused
	})
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.srv.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redir.Close)
	t.Setenv("HA_URL", redir.URL)
	haReset()
	if _, err := haEntities(context.Background(), 0); haCode(err) != "bad_response" {
		t.Errorf("redirect: %v", err)
	}
	tr, _ := homeTurn("Mach das Licht in der Küche an", "de", haAnna)
	_ = runHomeControl(tr, json.RawMessage(`{"target":"Küche","action":"on"}`))
	if n := other.count(""); n != 0 {
		t.Fatalf("other server got %d requests", n)
	}

	// A config change drops the client: the next one pins the new URL.
	t.Setenv("HA_URL", f.srv.URL)
	runSecretHooks(&haChangeHooks)
	cl2, _, err := haClientFor(context.Background())
	if err != nil || cl2 == cl || cl2.base != strings.ToLower(f.srv.URL) {
		t.Errorf("client not rebuilt: %v %v", cl2, err)
	}
	if strings.Contains(cl2.String(), fakeHATok) {
		t.Error("String leaks the token")
	}
}

func TestHomeConfigChangeDropsCacheViaSecrets(t *testing.T) {
	haEnv(t)
	a, b := newFakeHA(t), newFakeHA(t)
	b.set(func(f *fakeHA) {
		f.states = `[{"entity_id":"light.b","state":"on","attributes":{"friendly_name":"Nur B"}}]`
		f.areas = `[]`
	})
	store := func(url string) {
		if err := updateSecrets(func(s *secretsFile) error {
			s.HAURL, s.HAToken = url, fakeHATok
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		runSecretHooks(&haChangeHooks)
	}
	store(a.srv.URL)
	c, err := haEntities(context.Background(), haCacheTTL)
	if err != nil || len(c.ents) != 14 { // 13 listed + the vacuum kept for refusals
		t.Fatalf("A: %v", err)
	}
	store(b.srv.URL)
	if haCacheNow(haStaleMatch) != nil {
		t.Error("cache of the old host survived the change")
	}
	c, err = haEntities(context.Background(), haCacheTTL)
	if err != nil || len(c.ents) != 1 || c.ents[0].Name != "Nur B" {
		t.Fatalf("B: %v %+v", err, c)
	}
	// Token only per use: clearing it makes the next call "not configured".
	if err := updateSecrets(func(s *secretsFile) error { s.HAToken = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	if haConfigured() {
		t.Error("configured without a token")
	}
}

// ---------------------------------------------------------------------------
// HTTP routes: never the token

func TestHomeRoutes(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	logs := captureLog(t)
	h := newMux()
	get := func(method, path string, local bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if local {
			r.Host, r.RemoteAddr = "localhost:8080", "127.0.0.1:5000"
		} else {
			r.Host, r.RemoteAddr = "localhost:8080", "192.168.1.50:5000"
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	t.Setenv("HERMES_INSECURE_NO_TOKEN", "1") // let LAN requests through the token guard
	rec := get("GET", "/api/home/status", true)
	var st haStatusView
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &st) != nil || !st.OK || !st.Connected || st.Entities != 13 || st.Blocked != 5 {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	rec = get("GET", "/api/home/entities", true)
	var ents struct{ Entities []haEntityView }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &ents) != nil || len(ents.Entities) != 13 {
		t.Fatalf("entities %d %s", rec.Code, rec.Body)
	}
	for _, e := range ents.Entities {
		blocked := e.Domain == "lock" || e.Domain == "alarm_control_panel" || e.Domain == "script" || e.Domain == "automation" || e.ID == "cover.einfahrt"
		if e.Blocked != blocked || (blocked && e.State != "") {
			t.Errorf("entity %+v", e)
		}
		if e.ID == "vacuum.robi" {
			t.Error("other domains are not listed")
		}
	}
	if rec := get("POST", "/api/home/test", false); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "local_only") {
		t.Errorf("test from LAN: %d %s", rec.Code, rec.Body)
	}
	n := f.count("")
	if rec := get("POST", "/api/home/test", true); rec.Code != 200 || f.count("") == n {
		t.Errorf("test local: %d, requests %d→%d", rec.Code, n, f.count(""))
	}
	// Failure bodies are generic too.
	f.set(func(f *fakeHA) { f.status = http.StatusUnauthorized })
	haReset()
	rec = get("GET", "/api/home/entities", true)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"code":"auth"`) {
		t.Errorf("entities 401: %d %s", rec.Code, rec.Body)
	}
	all := rec.Body.String()
	for _, p := range []string{"/api/home/status", "/api/home/entities", "/api/status"} {
		all += get("GET", p, true).Body.String()
	}
	all += get("POST", "/api/home/test", true).Body.String()
	if strings.Contains(all, fakeHATok) || strings.Contains(all, "upstream detail") || strings.Contains(logs.String(), fakeHATok) {
		t.Errorf("token or upstream body leaked: %s\nlogs: %s", all, logs.String())
	}

	// Personal lock + homeQueries=known: the entity list needs the unlock cookie.
	f.set(func(f *fakeHA) { f.status = 0 })
	s := loadSettings()
	s.PersonalLock = true
	if err := saveSettings(s); err != nil {
		t.Fatal(err)
	}
	if rec := get("GET", "/api/home/entities", true); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "locked") {
		t.Errorf("locked: %d %s", rec.Code, rec.Body)
	}
	setHomeSettings(t, "everyone", "everyone")
	if rec := get("GET", "/api/home/entities", true); rec.Code != 200 {
		t.Errorf("queries everyone: %d", rec.Code)
	}
	// Unconfigured.
	t.Setenv("HA_URL", "")
	haReset()
	if rec := get("GET", "/api/home/entities", true); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured: %d", rec.Code)
	}
	if rec := get("GET", "/api/home/status", true); !strings.Contains(rec.Body.String(), `"configured":false`) {
		t.Errorf("unconfigured status: %s", rec.Body)
	}
}

// The whole chat path with an English voice: the reply, the SSE stream and
// the meta tag (never Mistral) — and the token is nowhere.
func TestHomeChatEndToEndEnglishVoice(t *testing.T) {
	haEnv(t)
	chatEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	haWarm(t, f)
	s := loadSettings()
	s.PiperVoice = "BT7274"
	if err := saveSettings(s); err != nil {
		t.Fatal(err)
	}
	if voiceLang("BT7274") != "en" {
		t.Skip("BT7274 is not an English voice in this build")
	}
	rec := postChat(t, newMux(), `{"messages":[{"role":"user","content":"Is the kitchen light on?"}],"clientId":"`+testClient+`"}`)
	body := rec.Body.String()
	if !strings.Contains(body, "on.") || strings.Contains(body, fakeHATok) {
		t.Fatalf("body %s", body)
	}
	if !strings.Contains(body, `"src":"fast:home_query"`) {
		t.Errorf("meta: %s", body)
	}
}

// ---------------------------------------------------------------------------
// Helpers

func TestHomeStripWake(t *testing.T) {
	cases := map[string]string{
		"Hermes, mach bitte mal das Licht in der Küche an":        "Mach das Licht in der Küche an",
		"Hey Hermes schalte die Heizung auf 19 Grad":              "Schalte die Heizung auf 19 Grad",
		"Kannst du bitte das Licht anmachen?":                     "Das Licht anmachen",
		"could you please turn on the living room lights, Hermes": "Turn on the living room lights",
		"turn off the kitchen light please.":                      "Turn off the kitchen light",
		"Hermes":                                                  "",
	}
	for in, want := range cases {
		if got := haStripWake(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestHomeActionParsing(t *testing.T) {
	cases := []struct {
		text, action string
		value        float64
	}{
		{"Stell die Heizung auf 21 Grad", "set", 21},
		{"set the heating to 21.5 degrees", "set", 21.5},
		{"Heizung auf 19,5°", "set", 19.5},
		{"Dimm das Licht auf 30 Prozent", "dim", 30},
		{"dim the lamp to 40%", "dim", 40},
		{"Mach das Licht aus", "off", -1},
		{"turn the light off", "off", -1},
		{"Schalte das Licht ein", "on", -1},
		{"Mach die Rollläden zu", "close", -1},
		{"Fahr die Rollos hoch", "open", -1},
		{"open the blinds", "open", -1},
	}
	for _, c := range cases {
		a, v := haActionOf(haFold(c.text), c.text)
		if a != c.action || (c.value >= 0 && v != c.value) || (c.value < 0 && v == v) {
			t.Errorf("%q: %s %v", c.text, a, v)
		}
	}
}

func TestHomeClassify(t *testing.T) {
	cases := map[[2]string]haClass{
		{"lock", ""}: haBlocked, {"alarm_control_panel", ""}: haBlocked, {"script", ""}: haBlocked,
		{"automation", ""}: haBlocked, {"cover", "garage"}: haBlocked, {"cover", "gate"}: haBlocked,
		{"cover", "door"}: haBlocked, {"cover", "shutter"}: haControl, {"cover", ""}: haControl,
		{"light", ""}: haControl, {"switch", ""}: haControl, {"climate", ""}: haControl,
		{"scene", ""}: haControl, {"media_player", ""}: haControl, {"sensor", "temperature"}: haRead,
		{"binary_sensor", "door"}: haBlocked, {"binary_sensor", "window"}: haRead, {"vacuum", ""}: haOther,
		{"camera", ""}: haOther,
	}
	for in, want := range cases {
		if got := haClassify(in[0], in[1]); got != want {
			t.Errorf("%v: %v, want %v", in, got, want)
		}
	}
}

// Concurrent turns, status polls and config changes (run with -race).
func TestHomeConcurrent(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch i % 4 {
			case 0:
				_, _ = haEntities(context.Background(), 0)
			case 1:
				haStatusDetached(context.Background(), i%2 == 0)
			case 2:
				haReset()
			default:
				tr, _ := homeTurn("Ist das Licht in der Küche an?", "de", haAnna)
				runHomeQuery(tr, json.RawMessage(`{}`))
			}
		}(i)
	}
	wg.Wait()
}

// After a command the states are marked stale: the next question reads
// fresh states, while name matching neither refetches nor kicks a
// background refresh.
func TestHomeStatesStaleAfterCommand(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	haWarm(t, f)
	if text, _, _ := sayHome(t, "turn off the kitchen light", "en", haAnna); text != "Turned off Küchenlicht." {
		t.Fatalf("control: %q", text)
	}
	f.set(func(f *fakeHA) {
		f.states = strings.Replace(fakeHAStates, `"light.kueche","state":"on"`, `"light.kueche","state":"off"`, 1)
	})
	f.reset()
	if _, ok := matchHomeControl(func() *Turn { tr, _ := homeTurn("Mach das Küchenlicht an", "de", haAnna); return tr }()); !ok {
		t.Error("match after a command")
	}
	haBG.Wait()
	if n := f.count(""); n != 0 {
		t.Errorf("matching refetched: %d requests", n)
	}
	if text, _, _ := sayHome(t, "Is the kitchen light on?", "en", haAnna); text != "Küchenlicht is off." {
		t.Errorf("query after command: %q", text)
	}
	if f.count("/api/states") != 1 {
		t.Errorf("states fetched %d times", f.count("/api/states"))
	}
}

// E2E c-2e / c-2d: the verbless "Küchenlicht und Robi ein" skipped the
// mixed-device check (persona chat "switched" both, or the router switched
// only the light), and "disarm the alarm" became a reminder question.
func TestHomeVerblessAndSecurityVerbs(t *testing.T) {
	haEnv(t)
	f := newFakeHA(t)
	haConnect(t, f)
	haWarm(t, f)
	f.set(func(f *fakeHA) {
		f.process = func(string, string) string { return assistDone("done") }
	})
	for _, c := range []struct{ text, lang, want string }{
		{"Küchenlicht und Robi ein", "de", "Dieses Gerät steuere ich nicht"},
		{"Robi aus", "de", "Dieses Gerät steuere ich nicht"},
		{"disarm the alarm", "en", "For safety reasons I don't control"},
		{"Alarmanlage unscharf", "de", "aus Sicherheitsgründen nicht"},
	} {
		f.reset()
		text, _, tr := sayHome(t, c.text, c.lang, haAnna)
		if !strings.Contains(text, c.want) || !tr.refused {
			t.Errorf("%q: reply %q refused=%v", c.text, text, tr.refused)
		}
		if n := f.count("/api/conversation/process") + f.count("/api/services/*"); n != 0 {
			t.Errorf("%q: sent %d requests to HA", c.text, n)
		}
	}
	if n := len(allReminders()); n != 0 {
		t.Errorf("a security command created %d reminders", n)
	}
	// the allowed short form still switches
	f.reset()
	if text, _, tr := sayHome(t, "Küchenlicht aus", "de", haAnna); text == "" || tr.refused || f.count("/api/conversation/process")+f.count("/api/services/*") == 0 {
		t.Errorf("Küchenlicht aus: %q refused=%v", text, tr.refused)
	}
	// not a command: no device, or no trailing particle
	for _, q := range []string{"Ich gehe jetzt aus", "Das Küchenlicht ist schön"} {
		tr, _ := homeTurn(q, "de", haAnna)
		if _, ok := matchHomeControl(tr); ok {
			t.Errorf("%q matched home_control", q)
		}
	}
	// the router path re-checks the user's sentence: one allowed target
	// from the model must not hide the vacuum the user also named
	tr, _ := homeTurn("Küche hell machen und Robi starten", "de", haAnna)
	if r, refused := haRefuseSentence(tr); !refused || !strings.Contains(r.Text, "Dieses Gerät steuere ich nicht") {
		t.Errorf("router re-check: %q %v", r.Text, refused)
	}
	tr, _ = homeTurn("Mach es in der Küche heller", "de", haAnna)
	if _, refused := haRefuseSentence(tr); refused {
		t.Error("router re-check refused an allowed sentence")
	}
}

// Live run: without Home Assistant, "Licht aus" reached persona chat, which
// answered "Licht ist ausgeschaltet." The verbless short form now gets the
// setup hint like "Schalte das Licht aus".
func TestHomeSetupCatchesVerblessCommand(t *testing.T) {
	haEnv(t)
	for _, q := range []string{"Licht aus", "Küchenlicht und Robi ein", "Schalte das Licht aus"} {
		text, _, _ := sayHome(t, q, "de", haAnna)
		if !strings.Contains(text, "Smart Home ist nicht eingerichtet") {
			t.Errorf("%q: %q", q, text)
		}
	}
	if text, _, _ := sayHome(t, "Ich gehe jetzt aus", "de", haAnna); strings.Contains(text, "Smart Home") {
		t.Errorf("no device noun: %q", text)
	}
}
