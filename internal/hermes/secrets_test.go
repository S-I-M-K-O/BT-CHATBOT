package hermes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fake credentials: distinctive so a leak shows up in any body.
const (
	fakeHAToken     = "eyJhbGciOiJIUzI1NiJ9.HA-TOKEN-LEAKCHECK-7731"
	fakeHAToken2    = "eyJhbGciOiJIUzI1NiJ9.HA-TOKEN-SECOND-9902"
	fakeClientID    = "1234-leakcheckclient.apps.googleusercontent.com"
	fakeClientSec   = "GOCSPX-LEAKCHECK-secret-5511"
	fakeEnvHAToken  = "ENV-HA-TOKEN-LEAKCHECK-4040"
	fakeEnvClientID = "env-leakcheck-id.apps.googleusercontent.com"
	fakeEnvSecret   = "GOCSPX-ENV-LEAKCHECK-6262"
	fakePIN         = "48151623"
)

// integEnv gives a test its own data dir and settings, no credential env
// vars, a cheap PIN hash and a clean unlock state.
func integEnv(t *testing.T) http.Handler {
	t.Helper()
	useTempData(t)
	useTempSettings(t)
	for _, k := range []string{"HA_URL", "HA_TOKEN", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET"} {
		t.Setenv(k, "")
	}
	old := pinHashIter
	pinHashIter = 10
	resetUnlockState := func() {
		clearUnlockSessions()
		unlockSessions.Lock()
		clear(unlockSessions.fails)
		unlockSessions.Unlock()
	}
	resetUnlockState()
	t.Cleanup(func() { pinHashIter = old; resetUnlockState() })
	return newMux()
}

// adminReq is a request from this machine (loopback Host and remote).
func adminReq(h http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Host = "localhost:8080"
	r.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func mustCode(t *testing.T, rec *httptest.ResponseRecorder, want int, what string) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("%s: status %d, want %d: %s", what, rec.Code, want, rec.Body)
	}
}

func integErrCode(rec *httptest.ResponseRecorder) string {
	var b struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	return b.Code
}

func integView(t *testing.T, rec *httptest.ResponseRecorder) integrationsView {
	t.Helper()
	var v integrationsView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("view: %v: %s", err, rec.Body)
	}
	return v
}

func assertNoLeak(t *testing.T, what, body string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s != "" && strings.Contains(body, s) {
			t.Errorf("%s leaks %q: %s", what, s, body)
		}
	}
}

func TestIntegrationsGetMasksSecrets(t *testing.T) {
	h := integEnv(t)
	mustCode(t, adminReq(h, "POST", "/api/integrations/ha", `{"url":"http://192.168.1.20:8123","token":"`+fakeHAToken+`"}`), 200, "ha")
	mustCode(t, adminReq(h, "POST", "/api/integrations/google", `{"clientId":"`+fakeClientID+`","clientSecret":"`+fakeClientSec+`"}`), 200, "google")
	rec := adminReq(h, "POST", "/api/integrations/lock", `{"pin":"`+fakePIN+`"}`)
	mustCode(t, rec, 200, "lock")
	hash := loadSecrets().LockPINHash
	secrets := []string{fakeHAToken, fakeClientID, fakeClientSec, fakePIN, hash}
	// Write responses are masked too.
	assertNoLeak(t, "lock response", rec.Body.String(), secrets...)

	rec = adminReq(h, "GET", "/api/integrations", "")
	mustCode(t, rec, 200, "get")
	assertNoLeak(t, "GET /api/integrations", rec.Body.String(), secrets...)
	v := integView(t, rec)
	if v.HA.URL != "http://192.168.1.20:8123" || !v.HA.URLValid || !v.HA.TokenSet || v.HA.FromEnv {
		t.Errorf("ha view: %+v", v.HA)
	}
	if !v.Google.ClientIDSet || !v.Google.ClientSecretSet || v.Google.FromEnv {
		t.Errorf("google view: %+v", v.Google)
	}
	if !v.Lock.PINSet || v.Lock.Enabled || !v.Admin {
		t.Errorf("lock view: %+v admin=%v", v.Lock, v.Admin)
	}
	// The exact spec keys are there.
	for _, k := range []string{`"tokenSet":true`, `"fromEnv":false`, `"clientIdSet":true`, `"clientSecretSet":true`, `"pinSet":true`} {
		if !strings.Contains(rec.Body.String(), k) {
			t.Errorf("GET lacks %s: %s", k, rec.Body)
		}
	}
	// A non-admin sees the same mask, with admin false.
	rec = doReq(h, "GET", "/api/integrations", "")
	mustCode(t, rec, 200, "non-admin get")
	assertNoLeak(t, "non-admin GET", rec.Body.String(), secrets...)
	if integView(t, rec).Admin {
		t.Error("non-admin request reported as admin")
	}
}

func TestIntegrationsErrorsNeverEchoInput(t *testing.T) {
	h := integEnv(t)
	cases := []struct{ path, body, code string }{
		{"/api/integrations/ha", `{"url":"http://user:PW-LEAKCHECK@ha.local:8123","token":"x"}`, "bad_url"},
		{"/api/integrations/ha", `{"url":"http://ha.local:8123/?t=PW-LEAKCHECK"}`, "bad_url"},
		{"/api/integrations/ha", `{"url":"http://ha.local:8123","token":"PW-LEAKCHECK with space"}`, "bad_token"},
		{"/api/integrations/ha", `{"url":"http://ha.local:8123","token":"PW-LEAKCHECK\r\nX-Evil: 1"}`, "bad_token"},
		{"/api/integrations/ha", `{"token":"PW-LEAKCHECK"}`, "url_required"},
		{"/api/integrations/ha", `{"token":"PW-LEAKCHECK"`, "bad_request"},
		{"/api/integrations/google", `{"clientId":"PW-LEAKCHECK id","clientSecret":"PW-LEAKCHECK"}`, "bad_client"},
		{"/api/integrations/google", `{"clientSecretJson":"{\"web\":{\"client_secret\":\"PW-LEAKCHECK\"}}"}`, "not_desktop"},
		{"/api/integrations/google", `{"clientSecretJson":"PW-LEAKCHECK"}`, "bad_json"},
		{"/api/integrations/lock", `{"pin":"PW-LEAKCHECK"}`, "bad_pin"},
	}
	for _, c := range cases {
		rec := adminReq(h, "POST", c.path, c.body)
		if rec.Code < 400 || integErrCode(rec) != c.code {
			t.Errorf("%s %s: %d %s, want code %s", c.path, c.body, rec.Code, rec.Body, c.code)
		}
		assertNoLeak(t, c.path+" error", rec.Body.String(), "PW-LEAKCHECK")
	}
	if s := loadSecrets(); s.HAURL != "" || s.HAToken != "" || s.GoogleClientID != "" || s.LockPINHash != "" {
		t.Errorf("a refused request stored something: %+v", s)
	}
}

func TestIntegrationsEnvOverridePrecedence(t *testing.T) {
	h := integEnv(t)
	mustCode(t, adminReq(h, "POST", "/api/integrations/ha", `{"url":"http://192.168.1.20:8123","token":"`+fakeHAToken+`"}`), 200, "ha")
	mustCode(t, adminReq(h, "POST", "/api/integrations/google", `{"clientId":"`+fakeClientID+`","clientSecret":"`+fakeClientSec+`"}`), 200, "google")

	// Nothing in env: stored values.
	if haURL() != "http://192.168.1.20:8123" || haToken() != fakeHAToken {
		t.Fatalf("stored: %q %q", haURL(), haToken())
	}

	// HA_URL for the same host (different spelling): the stored token is bound to it.
	t.Setenv("HA_URL", "HTTP://192.168.1.20:8123/")
	if haToken() != fakeHAToken {
		t.Error("same-host HA_URL must keep the stored token")
	}
	// HA_URL for another host: the stored token is never sent there.
	t.Setenv("HA_URL", "http://10.0.0.5:8123")
	if haURL() != "http://10.0.0.5:8123" || haToken() != "" {
		t.Errorf("other-host HA_URL: %q %q", haURL(), haToken())
	}
	v := integView(t, adminReq(h, "GET", "/api/integrations", ""))
	if v.HA.URL != "http://10.0.0.5:8123" || v.HA.TokenSet || !v.HA.FromEnv || !v.HA.URLFromEnv || v.HA.TokenFromEnv {
		t.Errorf("view with HA_URL: %+v", v.HA)
	}
	// With HA_URL set, the UI may bind a token to exactly that URL ...
	rec := adminReq(h, "POST", "/api/integrations/ha", `{"token":"`+fakeHAToken2+`"}`)
	mustCode(t, rec, 200, "token for env URL")
	if haToken() != fakeHAToken2 || loadSecrets().HAURL != "http://10.0.0.5:8123" {
		t.Errorf("token not bound to the env URL: %+v", loadSecrets().HAURL)
	}
	// ... or name it explicitly, but never another one.
	mustCode(t, adminReq(h, "POST", "/api/integrations/ha", `{"url":"http://10.0.0.5:8123/","token":"`+fakeHAToken2+`"}`), 200, "same URL as env")
	rec = adminReq(h, "POST", "/api/integrations/ha", `{"url":"http://192.168.1.99:8123","token":"`+fakeHAToken+`"}`)
	if rec.Code != http.StatusConflict || integErrCode(rec) != "env_override" {
		t.Errorf("URL other than HA_URL: %d %s", rec.Code, rec.Body)
	}
	// HA_TOKEN beats everything stored.
	t.Setenv("HA_TOKEN", fakeEnvHAToken)
	if haToken() != fakeEnvHAToken {
		t.Error("HA_TOKEN must win")
	}
	rec = adminReq(h, "GET", "/api/integrations", "")
	assertNoLeak(t, "GET with env", rec.Body.String(), fakeEnvHAToken, fakeHAToken, fakeHAToken2)
	if v := integView(t, rec); !v.HA.TokenSet || !v.HA.TokenFromEnv {
		t.Errorf("view with HA_TOKEN: %+v", v.HA)
	}
	// HA_TOKEN alone (no HA_URL) also wins over the stored token.
	t.Setenv("HA_URL", "")
	if haURL() != "http://10.0.0.5:8123" || haToken() != fakeEnvHAToken {
		t.Errorf("HA_TOKEN only: %q %q", haURL(), haToken())
	}

	// Google: each field separately.
	t.Setenv("GOOGLE_CLIENT_ID", fakeEnvClientID)
	if id, sec := googleClient(); id != fakeEnvClientID || sec != fakeClientSec {
		t.Errorf("GOOGLE_CLIENT_ID only: %q %q", id, sec)
	}
	t.Setenv("GOOGLE_CLIENT_SECRET", fakeEnvSecret)
	if id, sec := googleClient(); id != fakeEnvClientID || sec != fakeEnvSecret {
		t.Errorf("both env: %q %q", id, sec)
	}
	rec = adminReq(h, "GET", "/api/integrations", "")
	assertNoLeak(t, "GET with google env", rec.Body.String(), fakeEnvClientID, fakeEnvSecret, fakeClientID, fakeClientSec)
	if v := integView(t, rec); !v.Google.FromEnv || !v.Google.ClientIDFromEnv || !v.Google.ClientSecretFromEnv || !v.Google.ClientIDSet {
		t.Errorf("google env view: %+v", v.Google)
	}
	// Whitespace-only env values do not count.
	t.Setenv("GOOGLE_CLIENT_ID", "   ")
	t.Setenv("GOOGLE_CLIENT_SECRET", "")
	if id, sec := googleClient(); id != fakeClientID || sec != fakeClientSec {
		t.Errorf("blank env must fall back: %q %q", id, sec)
	}
}

func TestIntegrationsEnvURLNeverShowsCredentials(t *testing.T) {
	h := integEnv(t)
	t.Setenv("HA_URL", "http://admin:ENVPW-LEAKCHECK@ha.local:8123/path?x=ENVQ-LEAKCHECK")
	rec := adminReq(h, "GET", "/api/integrations", "")
	assertNoLeak(t, "GET with env userinfo", rec.Body.String(), "ENVPW-LEAKCHECK", "ENVQ-LEAKCHECK", "admin:")
	if v := integView(t, rec); v.HA.URLValid || v.HA.URL != "http://ha.local:8123" {
		t.Errorf("invalid env URL view: %+v", v.HA)
	}
}

func TestIntegrationsSecretsFile0600(t *testing.T) {
	h := integEnv(t)
	mustCode(t, adminReq(h, "POST", "/api/integrations/ha", `{"url":"http://ha.local:8123","token":"`+fakeHAToken+`"}`), 200, "ha")
	mustCode(t, adminReq(h, "PUT", "/api/integrations/google", `{"clientId":"`+fakeClientID+`","clientSecret":"`+fakeClientSec+`"}`), 200, "google")
	mustCode(t, adminReq(h, "POST", "/api/integrations/lock", `{"pin":"`+fakePIN+`"}`), 200, "lock")
	p := dataPath("secrets.json")
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("secrets.json mode %v, want 0600", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), fakePIN) {
		t.Error("the PIN is stored in clear")
	}
	if !strings.Contains(string(b), `"lockPinHash":"pbkdf2-sha256$`) {
		t.Errorf("PIN hash not in the spec format: %s", b)
	}
	// No temp files left next to it.
	ents, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
	// The readable settings never get a credential.
	if sb, err := os.ReadFile(os.Getenv("SETTINGS_FILE")); err == nil {
		assertNoLeak(t, "settings.json", string(sb), fakeHAToken, fakeClientSec, fakeClientID)
	}
}

func TestIntegrationsHAURLBindsToken(t *testing.T) {
	h := integEnv(t)
	set := func(body string) integrationsView {
		t.Helper()
		rec := adminReq(h, "POST", "/api/integrations/ha", body)
		mustCode(t, rec, 200, body)
		return integView(t, rec)
	}
	v := set(`{"url":"http://192.168.1.20:8123/","token":"` + fakeHAToken + `"}`)
	if v.HA.URL != "http://192.168.1.20:8123" || !v.HA.TokenSet {
		t.Fatalf("set: %+v", v.HA)
	}
	// The same URL in another spelling keeps the token.
	if v = set(`{"url":"HTTP://192.168.1.20:8123"}`); !v.HA.TokenSet || haToken() != fakeHAToken {
		t.Errorf("same URL dropped the token: %+v", v.HA)
	}
	// A new URL without a token clears the old token.
	if v = set(`{"url":"http://192.168.1.21:8123"}`); v.HA.TokenSet || haToken() != "" || loadSecrets().HAToken != "" {
		t.Errorf("URL change kept the token: %+v", v.HA)
	}
	if haURL() != "http://192.168.1.21:8123" {
		t.Errorf("url = %q", haURL())
	}
	// A token alone binds to the stored URL.
	if v = set(`{"token":"` + fakeHAToken2 + `"}`); !v.HA.TokenSet || haToken() != fakeHAToken2 {
		t.Errorf("token only: %+v", v.HA)
	}
	// URL and token together.
	if v = set(`{"url":"https://ha.example.org","token":"` + fakeHAToken + `"}`); v.HA.URL != "https://ha.example.org" || haToken() != fakeHAToken {
		t.Errorf("both: %+v", v.HA)
	}
	// An explicit empty token clears just the token.
	if v = set(`{"token":""}`); v.HA.TokenSet || v.HA.URL != "https://ha.example.org" {
		t.Errorf("clear token: %+v", v.HA)
	}
	// {"url":""} clears both, even with a token in the request.
	set(`{"url":"http://ha.local:8123","token":"` + fakeHAToken + `"}`)
	if v = set(`{"url":"","token":"` + fakeHAToken + `"}`); v.HA.URL != "" || v.HA.TokenSet {
		t.Errorf("clear: %+v", v.HA)
	}
	if s := loadSecrets(); s.HAURL != "" || s.HAToken != "" {
		t.Errorf("stored after clear: %+v", s)
	}
	// The change hook runs on changes only.
	runs := 0
	haChangeHooks = append(haChangeHooks, func() { runs++ })
	t.Cleanup(func() { haChangeHooks = haChangeHooks[:len(haChangeHooks)-1] })
	set(`{"url":"http://ha.local:8123","token":"` + fakeHAToken + `"}`)
	set(`{"url":"http://ha.local:8123","token":"` + fakeHAToken + `"}`)
	if runs != 1 {
		t.Errorf("hook ran %d times, want 1", runs)
	}
}

func TestNormalizeHAURL(t *testing.T) {
	good := map[string]string{
		"http://192.168.1.20:8123":        "http://192.168.1.20:8123",
		"http://192.168.1.20:8123/":       "http://192.168.1.20:8123",
		" https://HA.Example.org ":        "https://ha.example.org",
		"http://homeassistant.local:8123": "http://homeassistant.local:8123",
		"http://[fd00::1]:8123":           "http://[fd00::1]:8123",
	}
	for in, want := range good {
		if got, err := normalizeHAURL(in); err != nil || got != want {
			t.Errorf("%q → %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "ha.local:8123", "ftp://ha.local", "http://", "http://:8123",
		"http://ha.local:8123/api", "http://ha.local:8123?x=1", "http://ha.local:8123/?",
		"http://ha.local#x", "http://u:p@ha.local", "http://ha.local:0", "http://ha.local:99999",
		"http://ha.local:", "http://ha local", "javascript:alert(1)", "http://ha.local:08123",
		"http://" + strings.Repeat("a", 600) + ".local",
	} {
		if got, err := normalizeHAURL(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}

func TestIntegrationsGoogleClient(t *testing.T) {
	h := integEnv(t)
	post := func(body string) *httptest.ResponseRecorder {
		return adminReq(h, "POST", "/api/integrations/google", body)
	}
	mustCode(t, post(`{"clientId":"`+fakeClientID+`","clientSecret":"`+fakeClientSec+`"}`), 200, "set")
	if id, sec := googleClient(); id != fakeClientID || sec != fakeClientSec {
		t.Fatalf("stored: %q %q", id, sec)
	}
	// A new client id alone drops the old secret.
	mustCode(t, post(`{"clientId":"other.apps.googleusercontent.com"}`), 200, "id only")
	if id, sec := googleClient(); id != "other.apps.googleusercontent.com" || sec != "" {
		t.Errorf("id change: %q %q", id, sec)
	}
	// The secret alone keeps the id.
	mustCode(t, post(`{"clientSecret":"`+fakeClientSec+`"}`), 200, "secret only")
	if id, sec := googleClient(); id != "other.apps.googleusercontent.com" || sec != fakeClientSec {
		t.Errorf("secret only: %q %q", id, sec)
	}
	// The downloaded client_secret JSON of a Desktop client.
	file := `{"installed":{"client_id":"` + fakeClientID + `","project_id":"hermes","auth_uri":"https://accounts.google.com/o/oauth2/auth","client_secret":"` + fakeClientSec + `","redirect_uris":["http://localhost"]}}`
	body, _ := json.Marshal(map[string]string{"clientSecretJson": file})
	rec := post(string(body))
	mustCode(t, rec, 200, "json")
	assertNoLeak(t, "json response", rec.Body.String(), fakeClientID, fakeClientSec)
	if id, sec := googleClient(); id != fakeClientID || sec != fakeClientSec {
		t.Errorf("json: %q %q", id, sec)
	}
	// Mixed forms are ambiguous.
	if rec := post(`{"clientSecretJson":"{}","clientId":"x"}`); rec.Code != 400 {
		t.Errorf("mixed: %d", rec.Code)
	}
	// JSON without the secret.
	if rec := post(`{"clientSecretJson":"{\"installed\":{\"client_id\":\"x\"}}"}`); integErrCode(rec) != "bad_client" {
		t.Errorf("json without secret: %d %s", rec.Code, rec.Body)
	}
	// Empty strings clear.
	mustCode(t, post(`{"clientId":"","clientSecret":""}`), 200, "clear")
	if id, sec := googleClient(); id != "" || sec != "" {
		t.Errorf("clear: %q %q", id, sec)
	}
	mustCode(t, post(`{"clientId":"`+fakeClientID+`","clientSecret":"`+fakeClientSec+`"}`), 200, "set again")
	mustCode(t, post(`{"clientSecretJson":""}`), 200, "clear via json")
	if s := loadSecrets(); s.GoogleClientID != "" || s.GoogleClientSecret != "" {
		t.Errorf("clear via json: %+v", s)
	}
	if rec := post(`{}`); rec.Code != 400 {
		t.Errorf("empty body: %d", rec.Code)
	}
}

func TestIntegrationsWritesAreAdminOnly(t *testing.T) {
	h := integEnv(t)
	writes := []struct{ path, body string }{
		{"/api/integrations/ha", `{"url":"http://ha.local:8123","token":"` + fakeHAToken + `"}`},
		{"/api/integrations/google", `{"clientId":"` + fakeClientID + `","clientSecret":"` + fakeClientSec + `"}`},
		{"/api/integrations/lock", `{"pin":"` + fakePIN + `"}`},
	}
	// Token-less LAN mode (TestMain): the request passes tokenGuard but is
	// not from this machine.
	for _, wr := range writes {
		for _, m := range []string{"POST", "PUT"} {
			rec := doReq(h, m, wr.path, wr.body)
			if rec.Code != http.StatusForbidden || integErrCode(rec) != "local_only" {
				t.Errorf("%s %s from LAN: %d %s", m, wr.path, rec.Code, rec.Body)
			}
			assertNoLeak(t, "refusal", rec.Body.String(), fakeHAToken, fakeClientSec, fakePIN)
		}
	}
	if s := loadSecrets(); s != (secretsFile{Version: 1}) {
		t.Errorf("non-admin write stored something: %+v", s)
	}

	// Docker layout: loopback Host through the bridge needs the master token.
	tokenEnv(t, "master-token")
	h = newMux()
	bridged := func(bearer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/integrations/ha", strings.NewReader(writes[0].body))
		r.Host, r.RemoteAddr = "localhost:8080", "172.17.0.1:40000"
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if rec := bridged(""); rec.Code != http.StatusUnauthorized {
		t.Errorf("bridge without token: %d", rec.Code)
	}
	mustCode(t, bridged("master-token"), 200, "bridge with master token")
	// A LAN Host never counts as admin, even with the master token.
	r := httptest.NewRequest("POST", "/api/integrations/ha", strings.NewReader(`{"url":""}`))
	r.Host, r.RemoteAddr = "hermes.lan", "192.168.1.30:40000"
	r.Header.Set("Authorization", "Bearer master-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || integErrCode(w) != "local_only" || haToken() == "" {
		t.Errorf("LAN host with token: %d %s", w.Code, w.Body)
	}
	// GET works for any token holder on the LAN, masked.
	r = httptest.NewRequest("GET", "/api/integrations", nil)
	r.Host, r.RemoteAddr = "hermes.lan", "192.168.1.30:40000"
	r.Header.Set("Authorization", "Bearer master-token")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	mustCode(t, w, 200, "LAN GET")
	assertNoLeak(t, "LAN GET", w.Body.String(), fakeHAToken)
}

func TestIntegrationsBodyCapsAndMethods(t *testing.T) {
	h := integEnv(t)
	big := `{"url":"http://ha.local:8123","token":"` + strings.Repeat("a", 5<<10) + `"}`
	if rec := adminReq(h, "POST", "/api/integrations/ha", big); rec.Code != http.StatusRequestEntityTooLarge || integErrCode(rec) != "too_large" {
		t.Errorf("ha 5 KB: %d %s", rec.Code, rec.Body)
	}
	if rec := adminReq(h, "POST", "/api/integrations/lock", `{"pin":"1234","oldPin":"`+strings.Repeat("1", 5<<10)+`"}`); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("lock 5 KB: %d", rec.Code)
	}
	gbig, _ := json.Marshal(map[string]string{"clientSecretJson": strings.Repeat("x", 17<<10)})
	if rec := adminReq(h, "POST", "/api/integrations/google", string(gbig)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("google 17 KB: %d", rec.Code)
	}
	// A real-sized JSON file (a few KB) fits the Google cap.
	pad := strings.Repeat("x", 6<<10)
	file := `{"installed":{"client_id":"` + fakeClientID + `","client_secret":"` + fakeClientSec + `","note":"` + pad + `"}}`
	body, _ := json.Marshal(map[string]string{"clientSecretJson": file})
	mustCode(t, adminReq(h, "POST", "/api/integrations/google", string(body)), 200, "google 6 KB")
	// Trailing junk after the object.
	if rec := adminReq(h, "POST", "/api/integrations/ha", `{"url":""} {"url":"http://evil:1"}`); rec.Code != 400 {
		t.Errorf("trailing data: %d", rec.Code)
	}
	if rec := adminReq(h, "DELETE", "/api/integrations/ha", ""); rec.Code < 400 {
		t.Errorf("DELETE: %d", rec.Code)
	}
	if rec := adminReq(h, "GET", "/api/integrations/lock", ""); rec.Code < 400 {
		t.Errorf("GET lock: %d", rec.Code)
	}
	if rec := adminReq(h, "POST", "/api/integrations", "{}"); rec.Code < 400 {
		t.Errorf("POST root: %d", rec.Code)
	}
}

func TestIntegrationsLockPIN(t *testing.T) {
	h := integEnv(t)
	lock := func(body string) *httptest.ResponseRecorder {
		return adminReq(h, "POST", "/api/integrations/lock", body)
	}
	unlock := func(pin string) *http.Cookie {
		rec := adminReq(h, "POST", "/api/unlock", `{"pin":"`+pin+`"}`)
		return unlockCookie(rec)
	}
	unlocked := func(c *http.Cookie) bool {
		return strings.Contains(adminReq(h, "GET", "/api/unlock", "", c).Body.String(), `"unlocked":true`)
	}

	for _, bad := range []string{"123", "1234567890123", "12a4", "12 34", "١٢٣٤", "-1234"} {
		body, _ := json.Marshal(map[string]string{"pin": bad})
		if rec := lock(string(body)); rec.Code != 400 || integErrCode(rec) != "bad_pin" {
			t.Errorf("pin %q: %d %s", bad, rec.Code, rec.Body)
		}
	}
	if rec := lock(`{}`); rec.Code != 400 {
		t.Errorf("no pin field: %d", rec.Code)
	}

	// Set (no PIN yet: no oldPin needed).
	rec := lock(`{"pin":"1234"}`)
	mustCode(t, rec, 200, "set")
	if !integView(t, rec).Lock.PINSet || !checkLockPIN("1234") {
		t.Fatal("PIN not set")
	}
	c1 := unlock("1234")
	if c1 == nil || !unlocked(c1) {
		t.Fatal("unlock with the new PIN failed")
	}
	// Changing needs the current PIN.
	if rec := lock(`{"pin":"5678"}`); rec.Code != 400 || integErrCode(rec) != "old_pin_required" {
		t.Errorf("change without oldPin: %d %s", rec.Code, rec.Body)
	}
	if rec := lock(`{"pin":"5678","oldPin":"0000"}`); rec.Code != 403 || integErrCode(rec) != "wrong_pin" {
		t.Errorf("change with wrong oldPin: %d %s", rec.Code, rec.Body)
	}
	if !checkLockPIN("1234") || !unlocked(c1) {
		t.Error("a refused change altered the PIN or the sessions")
	}
	// Change: every session ends, the old PIN stops working.
	mustCode(t, lock(`{"pin":"5678","oldPin":"1234"}`), 200, "change")
	if unlocked(c1) {
		t.Error("PIN change must lock every device")
	}
	if checkLockPIN("1234") || !checkLockPIN("5678") {
		t.Error("PIN not changed")
	}
	if unlock("1234") != nil {
		t.Error("old PIN still unlocks")
	}
	c2 := unlock("5678")
	if c2 == nil {
		t.Fatal("new PIN does not unlock")
	}
	// Setting the same PIN again still re-locks (new salt, new hash).
	before := loadSecrets().LockPINHash
	mustCode(t, lock(`{"pin":"5678","oldPin":"5678"}`), 200, "same pin")
	if unlocked(c2) || loadSecrets().LockPINHash == before {
		t.Error("re-setting the PIN must re-hash and lock again")
	}

	// Remove: needs the PIN, turns personalLock off, locks everyone.
	s := loadSettings()
	s.PersonalLock = true
	if err := saveSettings(s); err != nil {
		t.Fatal(err)
	}
	c3 := unlock("5678")
	if rec := lock(`{"pin":"","oldPin":"1111"}`); rec.Code != 403 {
		t.Errorf("remove with wrong PIN: %d", rec.Code)
	}
	if !lockPINSet() || !loadSettings().PersonalLock {
		t.Fatal("wrong PIN removed the lock")
	}
	rec = lock(`{"pin":"","oldPin":"5678"}`)
	mustCode(t, rec, 200, "remove")
	v := integView(t, rec)
	if v.Lock.PINSet || v.Lock.Enabled || lockPINSet() || loadSettings().PersonalLock {
		t.Errorf("remove: %+v", v.Lock)
	}
	if unlocked(c3) {
		t.Error("PIN removal must end the sessions")
	}
	// Removing again is a harmless no-op.
	mustCode(t, lock(`{"pin":""}`), 200, "remove twice")
}

func TestIntegrationsLockSharesUnlockLockout(t *testing.T) {
	h := integEnv(t)
	mustCode(t, adminReq(h, "POST", "/api/integrations/lock", `{"pin":"2468"}`), 200, "set")
	for i := 0; i < unlockMaxFails; i++ {
		if rec := adminReq(h, "POST", "/api/integrations/lock", `{"pin":"1357","oldPin":"0000"}`); rec.Code != 403 {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	rec := adminReq(h, "POST", "/api/integrations/lock", `{"pin":"1357","oldPin":"2468"}`)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || integErrCode(rec) != "lockout" {
		t.Errorf("after 5 wrong PINs: %d %s", rec.Code, rec.Body)
	}
	if !checkLockPIN("2468") {
		t.Error("PIN changed during lockout")
	}
	// /api/unlock from the same address is locked out too.
	if rec := adminReq(h, "POST", "/api/unlock", `{"pin":"2468"}`); rec.Code != http.StatusTooManyRequests {
		t.Errorf("unlock during lockout: %d", rec.Code)
	}
}

func TestIntegrationsHintsFollowVoiceLanguage(t *testing.T) {
	for _, c := range []struct{ voice, want, notWant string }{
		{"BT7274", "Only directly on the Mac", "Nur direkt"},
		{"JarvisDE", "Nur direkt am Mac", "Only directly"},
	} {
		t.Run(c.voice, func(t *testing.T) {
			h := integEnv(t)
			s := loadSettings()
			s.PiperVoice = c.voice
			if err := saveSettings(s); err != nil {
				t.Fatal(err)
			}
			rec := doReq(h, "POST", "/api/integrations/ha", `{"url":""}`)
			if !strings.Contains(rec.Body.String(), c.want) || strings.Contains(rec.Body.String(), c.notWant) {
				t.Errorf("local_only hint: %s", rec.Body)
			}
			rec = adminReq(h, "POST", "/api/integrations/lock", `{"pin":"12"}`)
			var b map[string]string
			_ = json.Unmarshal(rec.Body.Bytes(), &b)
			wantPIN := "The PIN must be 4 to 12 digits."
			if c.voice == "JarvisDE" {
				wantPIN = "Die PIN muss aus 4 bis 12 Ziffern bestehen."
			}
			if b["hint"] != wantPIN || b["code"] != "bad_pin" || b["error"] == "" {
				t.Errorf("bad_pin body: %v", b)
			}
		})
	}
	// Every code has both languages.
	for code, h := range integrationsHints {
		if h[0] == "" || h[1] == "" || h[0] == h[1] {
			t.Errorf("hint %s incomplete: %v", code, h)
		}
	}
}

func TestIntegrationsPublishesChangeEvent(t *testing.T) {
	h := integEnv(t)
	sub, err := hubSubscribe(false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer hubUnsubscribe(sub)
	mustCode(t, adminReq(h, "POST", "/api/integrations/ha", `{"url":"http://ha.local:8123","token":"`+fakeHAToken+`"}`), 200, "ha")
	select {
	case ev := <-sub.Events():
		if ev.Type != "integrations.changed" || string(ev.Data) != `{"what":"ha"}` {
			t.Errorf("event: %s %s", ev.Type, ev.Data)
		}
		b, _ := json.Marshal(ev)
		assertNoLeak(t, "event", string(b), fakeHAToken)
	default:
		t.Error("no integrations.changed event")
	}
	// A no-op write publishes nothing.
	mustCode(t, adminReq(h, "POST", "/api/integrations/ha", `{"url":"http://ha.local:8123"}`), 200, "same")
	select {
	case ev := <-sub.Events():
		t.Errorf("unexpected event %s", ev.Type)
	default:
	}
}

// The hints are interface text: a pinned uiLang wins over the voice language
// (a German voice with an English interface gets English hints).
func TestIntegrationsHintsFollowUILang(t *testing.T) {
	for _, c := range []struct{ voice, ui, want string }{
		{"JarvisDE", "en", "Only directly on the Mac (http://localhost:8080)."},
		{"BT7274", "de", "Nur direkt am Mac (http://localhost:8080)."},
		{"JarvisDE", "auto", "Nur direkt am Mac (http://localhost:8080)."},
		{"BT7274", "auto", "Only directly on the Mac (http://localhost:8080)."},
	} {
		h := integEnv(t)
		s := loadSettings()
		s.PiperVoice, s.UILang = c.voice, c.ui
		if err := saveSettings(s); err != nil {
			t.Fatal(err)
		}
		var b map[string]string
		_ = json.Unmarshal(doReq(h, "POST", "/api/integrations/ha", `{"url":""}`).Body.Bytes(), &b)
		if b["hint"] != c.want {
			t.Errorf("voice %s ui %s: hint %q, want %q", c.voice, c.ui, b["hint"], c.want)
		}
	}
}
