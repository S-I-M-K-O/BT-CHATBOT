package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// httptest.NewRequest uses Host "example.com"; allow it for the whole package
// so handler tests exercise the real middleware stack.
func TestMain(m *testing.M) {
	os.Setenv("ALLOWED_HOSTS", "example.com")
	os.Exit(m.Run())
}

func TestHostAllowed(t *testing.T) {
	t.Setenv("ALLOWED_HOSTS", "hermes.lan, Pi.Local")
	cases := map[string]bool{
		"localhost:8080":          true,
		"127.0.0.1:8080":          true,
		"[::1]:8080":              true,
		"192.168.1.20:8080":       true,
		"app.localhost":           true,
		"hermes.lan:8080":         true,
		"pi.local":                true,
		"evil.example":            false,
		"rebind.attacker.io:8080": false,
		"":                        true, // HTTP/1.0 without Host: nothing to rebind
	}
	for host, want := range cases {
		if got := hostAllowed(host); got != want {
			t.Errorf("hostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestOriginGuard(t *testing.T) {
	h := newMux()
	do := func(method, host, origin, site string) int {
		r := httptest.NewRequest(method, "/api/settings", strings.NewReader(`{"pitch":1}`))
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	t.Setenv("SETTINGS_FILE", t.TempDir()+"/settings.json")

	if c := do(http.MethodPost, "localhost:8080", "http://localhost:8080", "same-origin"); c != http.StatusOK {
		t.Errorf("same-origin POST: got %d, want 200", c)
	}
	if c := do(http.MethodPost, "localhost:8080", "", ""); c != http.StatusOK {
		t.Errorf("POST without Origin (curl): got %d, want 200", c)
	}
	if c := do(http.MethodPost, "localhost:8080", "https://evil.example", "cross-site"); c != http.StatusForbidden {
		t.Errorf("cross-site POST: got %d, want 403", c)
	}
	if c := do(http.MethodPost, "localhost:8080", "https://evil.example", ""); c != http.StatusForbidden {
		t.Errorf("cross-origin POST without fetch metadata: got %d, want 403", c)
	}
	if c := do(http.MethodPost, "localhost:8080", "null", ""); c != http.StatusForbidden {
		t.Errorf("opaque-origin POST: got %d, want 403", c)
	}
	if c := do(http.MethodGet, "rebind.attacker.io", "", ""); c != http.StatusForbidden {
		t.Errorf("DNS-rebinding GET: got %d, want 403", c)
	}
	if c := do(http.MethodGet, "localhost:8080", "https://evil.example", "cross-site"); c != http.StatusOK {
		t.Errorf("cross-site GET (e.g. <img>) should not be blocked: got %d", c)
	}
}
