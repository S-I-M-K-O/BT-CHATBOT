package hermes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// No GET endpoint ever shows a stored or env credential, whatever the voice
// language: status, settings, integrations and the unlock state.
func TestReadEndpointsNeverLeakSecrets(t *testing.T) {
	for _, voice := range []string{"BT7274", "JarvisDE"} {
		t.Run(voice, func(t *testing.T) {
			h := integEnv(t)
			t.Setenv("OLLAMA_BASE", "http://127.0.0.1:1") // nothing listens: fast failure
			t.Setenv("MISTRAL_API_KEY", "")
			s := loadSettings()
			s.PiperVoice = voice
			if err := saveSettings(s); err != nil {
				t.Fatal(err)
			}
			mustCode(t, adminReq(h, "POST", "/api/integrations/ha", `{"url":"http://192.168.1.20:8123","token":"`+fakeHAToken+`"}`), 200, "ha")
			mustCode(t, adminReq(h, "POST", "/api/integrations/google", `{"clientId":"`+fakeClientID+`","clientSecret":"`+fakeClientSec+`"}`), 200, "google")
			mustCode(t, adminReq(h, "POST", "/api/integrations/lock", `{"pin":"`+fakePIN+`"}`), 200, "lock")
			hash := loadSecrets().LockPINHash
			stored := []string{fakeHAToken, fakeClientID, fakeClientSec, fakePIN, hash}

			check := func(label string, secrets []string) {
				t.Helper()
				for _, p := range []string{"/api/status", "/api/settings", "/api/integrations", "/api/unlock", "/api/health"} {
					rec := adminReq(h, "GET", p, "")
					if rec.Code != http.StatusOK {
						t.Errorf("%s GET %s: %d", label, p, rec.Code)
					}
					assertNoLeak(t, label+" GET "+p, rec.Body.String(), secrets...)
					for k, vs := range rec.Header() {
						assertNoLeak(t, label+" header "+k, strings.Join(vs, " "), secrets...)
					}
				}
			}
			check("stored", stored)

			t.Setenv("HA_URL", "http://192.168.1.20:8123")
			t.Setenv("HA_TOKEN", fakeEnvHAToken)
			t.Setenv("GOOGLE_CLIENT_ID", fakeEnvClientID)
			t.Setenv("GOOGLE_CLIENT_SECRET", fakeEnvSecret)
			check("env", append(stored, fakeEnvHAToken, fakeEnvClientID, fakeEnvSecret))
		})
	}
}

// The scoped lists token never reaches the integrations API, not even GET.
func TestIntegrationsNotReachableWithListsToken(t *testing.T) {
	integEnv(t)
	tokenEnv(t, "master-token")
	t.Setenv("HERMES_LISTS_TOKEN", "lists-token")
	h := newMux()
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/integrations", ""},
		{"POST", "/api/integrations/ha", `{"url":""}`},
		{"POST", "/api/integrations/lock", `{"pin":"1234"}`},
	} {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		r.Host, r.RemoteAddr = "hermes.lan", "192.168.1.40:40000"
		r.AddCookie(&http.Cookie{Name: listsCookieName, Value: listsCookieToken("lists-token")})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with the lists token: %d", c.method, c.path, w.Code)
		}
	}
	if lockPINSet() {
		t.Error("lists token set a PIN")
	}
}

// OLLAMA_BASE may carry credentials (a reverse proxy with basic auth); the
// status error shown to every client must not repeat them.
func TestStatusOllamaErrorHidesCredentials(t *testing.T) {
	t.Setenv("OLLAMA_BASE", "http://hermes:pr0xy-LEAKCHECK@127.0.0.1:1")
	rec := httptest.NewRecorder()
	handleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if strings.Contains(rec.Body.String(), "pr0xy-LEAKCHECK") {
		t.Errorf("status leaks the Ollama credentials: %s", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "127.0.0.1:1") {
		t.Errorf("status should still name the host: %s", rec.Body)
	}
}
