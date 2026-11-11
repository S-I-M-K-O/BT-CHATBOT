package hermes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// useTempConfig points HERMES_CONFIG at a temp path (no file yet) next to a
// temp settings file.
func useTempConfig(t *testing.T) string {
	t.Helper()
	sp := useTempSettings(t)
	p := filepath.Join(filepath.Dir(filepath.Dir(sp)), "conf", "hermes.conf")
	t.Setenv("HERMES_CONFIG", p)
	return p
}

func writeConf(t *testing.T, p, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	// Bump mtime so the stat cache always sees the rewrite.
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	future := fi.ModTime().Add(time.Duration(len(text)+1) * time.Millisecond)
	os.Chtimes(p, future, future)
}

func errCodes(c *configResult) map[string]string {
	m := map[string]string{}
	for _, e := range c.errors {
		m[e.Key+"@"+strconv.Itoa(e.Line)] = e.Code
	}
	return m
}

func TestConfigFieldsCoverSettings(t *testing.T) {
	seen := map[string]bool{}
	secs := map[string]bool{}
	for _, s := range configSections {
		secs[s] = true
	}
	for _, f := range configFields {
		if _, ok := settingsFieldIndex[f.Key]; !ok {
			t.Errorf("configFields has unknown key %q", f.Key)
		}
		if seen[f.Key] {
			t.Errorf("duplicate configFields key %q", f.Key)
		}
		if !secs[f.Section] {
			t.Errorf("%s: unknown section %q", f.Key, f.Section)
		}
		if f.DE == "" || f.EN == "" {
			t.Errorf("%s: needs a DE and EN comment", f.Key)
		}
		seen[f.Key] = true
	}
	for k := range settingsFieldIndex {
		if !seen[k] && configForbiddenFields[k] == "" {
			t.Errorf("Settings field %q missing from configFields", k)
		}
	}
}

func TestUILangSetting(t *testing.T) {
	if defaultSettings().UILang != "auto" {
		t.Fatal("uiLang must default to auto")
	}
	for in, want := range map[string]string{"de": "de", "en": "en", "auto": "auto", "": "auto", "fr": "auto", "DE": "auto"} {
		s := defaultSettings()
		s.UILang = in
		s.normalize()
		if s.UILang != want {
			t.Errorf("uiLang %q -> %q, want %q", in, s.UILang, want)
		}
	}
}

func TestParseConfigEdgeCases(t *testing.T) {
	text := "\ufeff# full comment\r\n" + // 1
		"[Oberfläche / Interface]\n" + // 2
		"uiLang = de   # inline comment\n" + // 3
		"\n" + // 4
		"  ; also a comment\n" + // 5
		"homeCity = \"Bad # Homburg\"\n" + // 6
		"SPEAK = false\n" + // 7  (case-insensitive key)
		"systemPrompt = Line 1\\nLine 2\n" + // 8
		"voice = Anna#1\n" + // 9  (# without whitespace is data)
		"bogusKey = 1\n" + // 10
		"temperature = 9\n" + // 11 (clamped -> invalid)
		"maxTokens = lots\n" + // 12
		"haToken = abc\n" + // 13
		"personalLock = true\n" + // 14
		"just text\n" + // 15
		"quietEnd = \"06:30\n" + // 16 (missing quote)
		"temperature = 0,5\n" + // 17 (comma decimal)
		"eyeDesign = iris\n" + // 18
		"eyeDesign = cortana\n" + // 19 duplicate, last wins
		"[broken\n" + // 20
		"calendarOthers = \"refuse\" trailing\n" + // 21
		"routerModel =\n" + // 22 (empty string ok)
		"HERMES_TOKEN = x\n" + // 23
		"numCtx = 0\n" // 24 (0 -> default: invalid)
	c := parseConfig(text)
	v := c.values
	if v.UILang != "de" || v.HomeCity != "Bad # Homburg" || v.Speak || v.SystemPrompt != "Line 1\nLine 2" ||
		v.Voice != "Anna#1" || v.Temperature != 0.5 || v.EyeDesign != "cortana" || v.RouterModel != "" {
		t.Errorf("values: %+v", v)
	}
	wantManaged := []string{"eyeDesign", "homeCity", "routerModel", "speak", "systemPrompt", "temperature", "uiLang", "voice"}
	if got := c.managedKeys(); !reflect.DeepEqual(got, wantManaged) {
		t.Errorf("managed %v, want %v", got, wantManaged)
	}
	wantErr := map[string]string{
		"bogusKey@10":       "unknown_key",
		"temperature@11":    "invalid_value",
		"maxTokens@12":      "invalid_value",
		"haToken@13":        "secret",
		"personalLock@14":   "not_allowed",
		"@15":               "syntax",
		"quietEnd@16":       "syntax",
		"eyeDesign@19":      "duplicate",
		"@20":               "syntax",
		"calendarOthers@21": "syntax",
		"HERMES_TOKEN@23":   "secret",
		"numCtx@24":         "invalid_value",
	}
	if got := errCodes(c); !reflect.DeepEqual(got, wantErr) {
		t.Errorf("errors:\n got %v\nwant %v\n%+v", got, wantErr, c.errors)
	}
	for _, e := range c.errors {
		if e.Msg == "" {
			t.Errorf("empty message: %+v", e)
		}
		if e.Code == "secret" && !strings.Contains(e.Msg, ".env") {
			t.Errorf("secret message should point to .env: %q", e.Msg)
		}
	}
}

func TestParseConfigValues(t *testing.T) {
	c := parseConfig(strings.Join([]string{
		`homeCity = "Frankfurt am Main"`,
		`piperVoice = JarvisDE`,
		`voiceSpeed = 1.5`,
		`checkinMeetingLead = 15`,
		`quietStart = 23:00`,
		`timezone = Europe/Berlin`,
		`keepAlive = -1`,
		`systemPrompt = "Sag \"Hallo\"\\n"`,
		`cloudSpeech = off`,
	}, "\n"))
	if len(c.errors) != 0 {
		t.Fatalf("unexpected errors: %+v", c.errors)
	}
	v := c.values
	if v.HomeCity != "Frankfurt am Main" || v.PiperVoice != "JarvisDE" || v.VoiceSpeed != 1.5 || v.CheckinMeetingLead != 15 ||
		v.QuietStart != "23:00" || v.Timezone != "Europe/Berlin" || v.KeepAlive != "-1" || v.SystemPrompt != "Sag \"Hallo\"\\n" || v.CloudSpeech {
		t.Errorf("values: %+v", v)
	}
	bad := parseConfig("homeCity = \"  Bonn\"\npiperVoice = Nobody\nquietStart = 25:00\ntimezone = Mars/Base\nvoiceFx = chorus\nuiLang = fr\n")
	if len(bad.errors) != 6 || len(bad.managed) != 0 {
		t.Errorf("invalid values must be reported and ignored: %+v %v", bad.errors, bad.managed)
	}
}

func TestConfigSecretKeys(t *testing.T) {
	for _, k := range []string{"haToken", "haUrl", "googleClientSecret", "googleClientId", "lockPin", "pin", "HERMES_TOKEN", "weather_api_key", "password", "smtpPassword"} {
		c := parseConfig(k + " = x\n")
		if len(c.errors) != 1 || c.errors[0].Code != "secret" || len(c.managed) != 0 {
			t.Errorf("%s not rejected as secret: %+v", k, c.errors)
		}
	}
	for _, k := range []string{"piperVoice", "speak", "keepAlive"} {
		if isConfigSecretKey(k) {
			t.Errorf("%s flagged as secret", k)
		}
	}
}

func TestConfigPrecedenceAndSave(t *testing.T) {
	p := useTempConfig(t)
	s := defaultSettings()
	s.PiperVoice, s.EyeDesign, s.HomeCity = "PDA", "iris", "Bonn"
	if err := saveSettings(s); err != nil {
		t.Fatal(err)
	}
	writeConf(t, p, "piperVoice = JarvisDE\nuiLang = en\n")
	got := loadSettings()
	if got.PiperVoice != "JarvisDE" || got.UILang != "en" || got.EyeDesign != "iris" || got.HomeCity != "Bonn" {
		t.Fatalf("precedence: %+v", got)
	}

	// UI save of a managed key keeps the file's value; others change.
	h := newMux()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(`{"piperVoice":"PDA","uiLang":"de","eyeDesign":"cortana"}`)))
	if rec.Code != 200 {
		t.Fatalf("POST %d %s", rec.Code, rec.Body)
	}
	var resp struct {
		Settings
		Managed []string `json:"managed"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.PiperVoice != "JarvisDE" || resp.UILang != "en" || resp.EyeDesign != "cortana" {
		t.Errorf("POST response: %+v", resp.Settings)
	}
	if !reflect.DeepEqual(resp.Managed, []string{"piperVoice", "uiLang"}) {
		t.Errorf("POST managed: %v", resp.Managed)
	}
	// settings.json holds the file's value for managed keys.
	raw, _ := os.ReadFile(settingsPath())
	var onDisk map[string]any
	json.Unmarshal(raw, &onDisk)
	if onDisk["piperVoice"] != "JarvisDE" || onDisk["uiLang"] != "en" || onDisk["eyeDesign"] != "cortana" {
		t.Errorf("settings.json: %v", onDisk)
	}
	// Direct saveSettings is guarded too.
	s = loadSettings()
	s.PiperVoice = "PDA"
	saveSettings(s)
	if s.PiperVoice != "JarvisDE" || loadSettings().PiperVoice != "JarvisDE" {
		t.Error("saveSettings persisted a managed key against the file")
	}

	// GET includes managed.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	resp.Managed = nil
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if !reflect.DeepEqual(resp.Managed, []string{"piperVoice", "uiLang"}) {
		t.Errorf("GET managed: %v (%s)", resp.Managed, rec.Body)
	}

	// Removing the file frees the keys; the last effective values stay.
	os.Remove(p)
	got = loadSettings()
	if got.PiperVoice != "JarvisDE" || got.EyeDesign != "cortana" {
		t.Errorf("after removing hermes.conf: %+v", got)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if !strings.Contains(rec.Body.String(), `"managed":[]`) {
		t.Errorf("managed should be an empty list: %s", rec.Body)
	}
}

func TestConfigManagedPersonalLockCannotBeSet(t *testing.T) {
	p := useTempConfig(t)
	writeConf(t, p, "personalLock = true\n")
	if loadSettings().PersonalLock {
		t.Error("personalLock must not be settable from hermes.conf")
	}
}

func TestConfigExportRoundTrip(t *testing.T) {
	s := defaultSettings()
	s.SystemPrompt = "Zeile 1\nZeile \"2\" # kein Kommentar\\n \t"
	s.HomeCity = "Köln"
	s.UILang = "de"
	s.Temperature = 0.35
	s.Speak = false
	s.Timezone = "Europe/Berlin"
	s.HALanguage = "de"
	s.normalize()
	out := configExport(s)
	for _, sec := range configSections {
		if !strings.Contains(out, "["+sec+"]") {
			t.Errorf("export misses section %q", sec)
		}
	}
	if strings.Contains(out, "personalLock") {
		t.Error("export must not contain personalLock")
	}
	c := parseConfig(out)
	if len(c.errors) != 0 {
		t.Fatalf("export does not parse cleanly: %+v", c.errors)
	}
	back := *s
	c.overlay(&back)
	if back != *s {
		t.Errorf("round trip:\n got %+v\nwant %+v", back, *s)
	}
	if len(c.managed) != len(configFields) {
		t.Errorf("export sets %d keys, want %d", len(c.managed), len(configFields))
	}
	// Defaults too.
	d := defaultSettings()
	c = parseConfig(configExport(d))
	back = Settings{}
	c.overlay(&back)
	back.PersonalLock = d.PersonalLock
	if len(c.errors) != 0 || back != *d {
		t.Errorf("default round trip: %+v\n%+v", c.errors, back)
	}
}

func TestConfigHTTP(t *testing.T) {
	p := useTempConfig(t)
	h := newMux()

	get := func() configView {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		if rec.Code != 200 {
			t.Fatalf("GET /api/config %d", rec.Code)
		}
		var v configView
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rec.Body.String(), `"errors":[`) || !strings.Contains(rec.Body.String(), `"managed":[`) {
			t.Errorf("lists must never be null: %s", rec.Body)
		}
		return v
	}
	v := get()
	if v.Exists || v.Path != p || v.Mtime != "" || len(v.Managed) != 0 {
		t.Errorf("no file: %+v", v)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config/export", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" ||
		rec.Header().Get("Content-Disposition") != `attachment; filename="hermes.conf"` || !strings.Contains(rec.Body.String(), "uiLang = \"auto\"") {
		t.Errorf("export: %d %v\n%s", rec.Code, rec.Header(), rec.Body)
	}

	// Not admin: 403.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/config/write", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("non-admin write: %d", rec.Code)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("non-admin write created the file")
	}
	// Admin: created 0600 (it holds homeCity and the system prompt, like the
	// 0600 settings.json), returns the view.
	w := adminReq(h, http.MethodPost, "/api/config/write", "")
	if w.Code != 200 {
		t.Fatalf("admin write: %d %s", w.Code, w.Body)
	}
	var wv configView
	json.Unmarshal(w.Body.Bytes(), &wv)
	if !wv.Exists || wv.Mtime == "" || len(wv.Managed) != 0 || len(wv.Errors) != 0 {
		t.Errorf("write view: %+v", wv)
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode: %v %v", fi, err)
	}
	if _, err := time.Parse(time.RFC3339, wv.Mtime); err != nil {
		t.Errorf("mtime %q: %v", wv.Mtime, err)
	}
	// The created file is the export with every value commented out:
	// uncommenting it gives back the export exactly.
	created, _ := os.ReadFile(p)
	if !strings.Contains(string(created), "\n# uiLang = \"auto\"\n") {
		t.Errorf("template not commented:\n%s", created)
	}
	if c := parseConfig(string(created)); len(c.managedKeys()) != 0 || len(c.errors) != 0 {
		t.Errorf("template parses to %v %v", c.managedKeys(), c.errors)
	}
	// Second write: 409, file untouched.
	before, _ := os.ReadFile(p)
	writeConf(t, p, string(before)+"\nhomeCity = Hamburg\n")
	w = adminReq(h, http.MethodPost, "/api/config/write", "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"exists"`) {
		t.Errorf("second write: %d %s", w.Code, w.Body)
	}
	after, _ := os.ReadFile(p)
	if !strings.HasSuffix(string(after), "homeCity = Hamburg\n") {
		t.Error("409 must not touch the file")
	}
	if loadSettings().HomeCity != "Hamburg" {
		t.Error("edited file not applied")
	}
	// Errors are reported with line numbers.
	writeConf(t, p, "speak = maybe\n")
	v = get()
	if len(v.Errors) != 1 || v.Errors[0].Line != 1 || v.Errors[0].Key != "speak" {
		t.Errorf("errors: %+v", v.Errors)
	}
	// No leftover temp files.
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Errorf("leftover files: %v", ents)
	}
}

func TestConfigPathDocker(t *testing.T) {
	sp := useTempSettings(t)
	t.Setenv("HERMES_CONFIG", "")
	if got := configPath(); got != filepath.Join(filepath.Dir(sp), "hermes.conf") {
		t.Errorf("default path %q", got)
	}
	t.Setenv("SETTINGS_FILE", "")
	if got := configPath(); got != filepath.Join("data", "hermes.conf") {
		t.Errorf("default path without SETTINGS_FILE %q", got)
	}
	t.Setenv("SETTINGS_FILE", "/app/data/settings.json")
	if got := configPath(); got != "/app/data/hermes.conf" {
		t.Errorf("docker path %q", got)
	}
	dir := t.TempDir()
	t.Setenv("HERMES_CONFIG", filepath.Join(dir, "custom.conf"))
	t.Setenv("SETTINGS_FILE", filepath.Join(dir, "settings.json"))
	writeConf(t, filepath.Join(dir, "custom.conf"), "uiLang = en\n")
	if loadSettings().UILang != "en" {
		t.Error("HERMES_CONFIG not used")
	}
}

func TestConfigWatcherPublishesOncePerChange(t *testing.T) {
	p := useTempConfig(t)
	sub, err := hubSubscribe(false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer hubUnsubscribe(sub)

	tick := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runConfigWatcher(ctx, tick); close(done) }()
	defer func() { cancel(); <-done }()

	count := func() int {
		n := 0
		for {
			select {
			case ev := <-sub.Events():
				if ev.Type != "settings.changed" {
					continue
				}
				if ev.ID != 0 || string(ev.Data) != `{"source":"config"}` {
					t.Errorf("event: %+v %s", ev, ev.Data)
				}
				n++
			case <-time.After(50 * time.Millisecond):
				return n
			}
		}
	}
	step := func() { tick <- time.Now(); tick <- time.Now(); tick <- time.Now() }

	step()
	if n := count(); n != 0 {
		t.Errorf("no change published %d", n)
	}
	writeConf(t, p, "uiLang = de\n") // created
	step()
	if n := count(); n != 1 {
		t.Errorf("create published %d", n)
	}
	if loadSettings().UILang != "de" {
		t.Error("not re-applied")
	}
	writeConf(t, p, "uiLang = en\nspeak = \n") // changed, partly invalid
	step()
	if n := count(); n != 1 {
		t.Errorf("change published %d", n)
	}
	if loadSettings().UILang != "en" {
		t.Error("change not re-applied")
	}
	os.Remove(p) // deleted
	step()
	if n := count(); n != 1 {
		t.Errorf("delete published %d", n)
	}
	if loadSettings().UILang != "auto" {
		t.Error("deleted file still applied")
	}
}

// Error messages echo the offending value or key, but only a short prefix:
// an over-long system prompt or a stray long line must not turn GET
// /api/config and the startup log line into a copy of the whole file.
func TestConfigErrorsStayShort(t *testing.T) {
	long := strings.Repeat("x", maxSystemPromptLen+500)
	c := parseConfig("systemPrompt = " + long + "\n" + strings.Repeat("k", 5000) + " = 1\nspeak = " + long + "\n")
	if len(c.errors) != 3 {
		t.Fatalf("errors: %d", len(c.errors))
	}
	for _, e := range c.errors {
		if len(e.Msg) > 400 || len(e.Key) > 200 {
			t.Errorf("line %d: key %d bytes, message %d bytes", e.Line, len(e.Key), len(e.Msg))
		}
	}
}
