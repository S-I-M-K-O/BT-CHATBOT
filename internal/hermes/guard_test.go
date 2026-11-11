package hermes

import (
	"crypto/tls"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// httptest.NewRequest uses Host "example.com" and a non-loopback RemoteAddr;
// allow it for the whole package so handler tests exercise the real
// middleware stack. The token tests below switch the access checks back on.
func TestMain(m *testing.M) {
	// Tests run in internal/hermes; HERMES itself runs from the repo root
	// (web/static, voices/). Switch there so relative paths mean the same.
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		panic(err)
	}
	os.Setenv("ALLOWED_HOSTS", "example.com")
	os.Setenv("HERMES_INSECURE_NO_TOKEN", "1")
	os.Unsetenv("HERMES_TOKEN")
	os.Unsetenv("HERMES_BEHIND_LOOPBACK")
	// No test may ever touch the real data/ directory; tests that need a
	// store still set their own DATA_DIR with t.Setenv.
	dir, err := os.MkdirTemp("", "hermes-test-data-")
	if err != nil {
		panic(err)
	}
	os.Setenv("DATA_DIR", dir)
	// Nor the real settings file: tests that never call useTempSettings
	// read (and must never write) defaults from a throwaway path.
	os.Setenv("SETTINGS_FILE", dir+"/settings.json")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
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

// tokenEnv turns the access checks on the way a real deployment has them.
func tokenEnv(t *testing.T, token string) {
	t.Helper()
	t.Setenv("HERMES_INSECURE_NO_TOKEN", "")
	t.Setenv("HERMES_BEHIND_LOOPBACK", "")
	t.Setenv("HERMES_TOKEN", token)
	t.Setenv("ALLOWED_HOSTS", "hermes.lan")
	t.Setenv("SETTINGS_FILE", t.TempDir()+"/settings.json")
}

type tokenReq struct {
	method, path, host, remote string
	bearer, cookie             string
	body                       string
}

func (q tokenReq) do(h http.Handler) *httptest.ResponseRecorder {
	method := q.method
	if method == "" {
		method = http.MethodGet
	}
	r := httptest.NewRequest(method, q.path, strings.NewReader(q.body))
	r.Host = q.host
	r.RemoteAddr = q.remote
	if q.bearer != "" {
		r.Header.Set("Authorization", "Bearer "+q.bearer)
	}
	if q.cookie != "" {
		r.AddCookie(&http.Cookie{Name: tokenCookieName, Value: q.cookie})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// The audit scenario: with ADDR=0.0.0.0 a LAN client used curl (no Origin) to
// overwrite the system prompt. That must now need the token.
func TestTokenGuardBlocksLANWithoutToken(t *testing.T) {
	tokenEnv(t, "s3cret-token")
	h := newMux()
	evil := `{"systemPrompt":"EVIL"}`
	lan := "192.168.1.77:51000"

	for name, q := range map[string]tokenReq{
		"IP host":           {method: http.MethodPost, path: "/api/settings", host: "192.168.1.50:8080", remote: lan, body: evil},
		"ALLOWED_HOSTS":     {method: http.MethodPost, path: "/api/settings", host: "hermes.lan:8080", remote: lan, body: evil},
		"spoofed localhost": {method: http.MethodPost, path: "/api/settings", host: "localhost:8080", remote: lan, body: evil},
		"wrong bearer":      {method: http.MethodPost, path: "/api/settings", host: "192.168.1.50:8080", remote: lan, body: evil, bearer: "nope"},
		"wrong cookie":      {method: http.MethodPost, path: "/api/settings", host: "192.168.1.50:8080", remote: lan, body: evil, cookie: "nope"},
		"raw token cookie":  {method: http.MethodPost, path: "/api/settings", host: "192.168.1.50:8080", remote: lan, body: evil, cookie: "s3cret-token"},
		"local proxy":       {method: http.MethodPost, path: "/api/settings", host: "hermes.lan", remote: "127.0.0.1:4000", body: evil},
		"static page":       {path: "/", host: "192.168.1.50:8080", remote: lan},
		"generated image":   {path: "/gen/hermes_1.png", host: "192.168.1.50:8080", remote: lan},
	} {
		if w := q.do(h); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", name, w.Code)
		}
	}
	if p := loadSettings().SystemPrompt; p == "EVIL" {
		t.Fatal("unauthenticated LAN request rewrote the system prompt")
	}
}

func TestTokenGuardAllowsTokenAndLoopback(t *testing.T) {
	tokenEnv(t, "s3cret-token")
	h := newMux()
	lan := "192.168.1.77:51000"
	for name, q := range map[string]tokenReq{
		"bearer":         {path: "/api/settings", host: "192.168.1.50:8080", remote: lan, bearer: "s3cret-token"},
		"cookie":         {path: "/api/settings", host: "hermes.lan:8080", remote: lan, cookie: cookieToken("s3cret-token")},
		"localhost":      {path: "/api/settings", host: "localhost:8080", remote: "127.0.0.1:5000"},
		"127.0.0.1":      {path: "/api/settings", host: "127.0.0.1:8080", remote: "127.0.0.1:5000"},
		"::1":            {path: "/api/settings", host: "[::1]:8080", remote: "[::1]:5000"},
		"bearer, POST":   {method: http.MethodPost, path: "/api/settings", host: "192.168.1.50:8080", remote: lan, bearer: "s3cret-token", body: `{"pitch":1}`},
		"health (local)": {path: "/api/health", host: "localhost:8080", remote: "127.0.0.1:5000"},
	} {
		if w := q.do(h); w.Code != http.StatusOK {
			t.Errorf("%s: got %d, want 200 (%s)", name, w.Code, w.Body)
		}
	}

	// Docker: the published port is loopback-only, connections come from the
	// bridge gateway.
	docker := tokenReq{path: "/api/settings", host: "localhost:8080", remote: "172.17.0.1:40000"}
	if w := docker.do(h); w.Code != http.StatusUnauthorized {
		t.Errorf("bridge gateway without HERMES_BEHIND_LOOPBACK: got %d, want 401", w.Code)
	}
	t.Setenv("HERMES_BEHIND_LOOPBACK", "1")
	fakeGateway(t, "010011AC") // 172.17.0.1
	if w := docker.do(h); w.Code != http.StatusOK {
		t.Errorf("bridge gateway with HERMES_BEHIND_LOOPBACK: got %d, want 200", w.Code)
	}
	// Another container on the network (or a LAN host routing to the
	// bridge) sending Host: localhost is not this machine.
	other := docker
	other.remote = "172.17.0.5:40000"
	if w := other.do(h); w.Code != http.StatusUnauthorized {
		t.Errorf("non-gateway peer with HERMES_BEHIND_LOOPBACK: got %d, want 401", w.Code)
	}
	t.Setenv("HERMES_TRUSTED_PEER", "10.0.0.9, 172.17.0.5")
	if w := other.do(h); w.Code != http.StatusOK {
		t.Errorf("HERMES_TRUSTED_PEER: got %d, want 200", w.Code)
	}
	t.Setenv("HERMES_TRUSTED_PEER", "")
	// Without a readable routing table only HERMES_TRUSTED_PEER helps.
	fakeGateway(t, "")
	if w := docker.do(h); w.Code != http.StatusUnauthorized {
		t.Errorf("unknown gateway: got %d, want 401", w.Code)
	}
	fakeGateway(t, "010011AC")
	// ...but a non-loopback Host still needs the token there.
	docker.host = "192.168.1.50:8080"
	if w := docker.do(h); w.Code != http.StatusUnauthorized {
		t.Errorf("LAN host behind loopback flag: got %d, want 401", w.Code)
	}
}

func TestTokenGuardFailsClosedWithoutToken(t *testing.T) {
	tokenEnv(t, "")
	h := newMux()
	q := tokenReq{method: http.MethodPost, path: "/api/settings", host: "hermes.lan:8080", remote: "192.168.1.77:51000", body: `{"pitch":1}`, bearer: "anything"}
	if w := q.do(h); w.Code != http.StatusForbidden {
		t.Errorf("LAN request with no HERMES_TOKEN configured: got %d, want 403", w.Code)
	}
	local := tokenReq{path: "/api/settings", host: "localhost:8080", remote: "127.0.0.1:5000"}
	if w := local.do(h); w.Code != http.StatusOK {
		t.Errorf("loopback must stay token-free: got %d", w.Code)
	}
}

func TestTokenLogin(t *testing.T) {
	tokenEnv(t, "s3cret-token")
	h := newMux()
	lan := "192.168.1.77:51000"

	w := tokenReq{path: "/?view=eye&token=s3cret-token", host: "192.168.1.50:8080", remote: lan}.do(h)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/?view=eye" {
		t.Fatalf("login: got %d Location %q", w.Code, w.Header().Get("Location"))
	}
	var c *http.Cookie
	for _, ck := range w.Result().Cookies() {
		if ck.Name == tokenCookieName {
			c = ck
		}
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Value != cookieToken("s3cret-token") {
		t.Fatalf("login cookie: %+v", c)
	}
	if w := (tokenReq{path: "/api/settings", host: "192.168.1.50:8080", remote: lan, cookie: c.Value}).do(h); w.Code != http.StatusOK {
		t.Errorf("request with login cookie: got %d", w.Code)
	}

	// A wrong token sets nothing.
	w = tokenReq{path: "/?token=wrong", host: "192.168.1.50:8080", remote: lan}.do(h)
	if w.Code != http.StatusUnauthorized || len(w.Result().Cookies()) != 0 {
		t.Errorf("bad login: got %d cookies %v", w.Code, w.Result().Cookies())
	}

	// The redirect must stay on this site even for a "//host" path.
	r := httptest.NewRequest(http.MethodGet, "/?token=s3cret-token", nil)
	r.URL.Path = "//evil.example/x"
	r.Host, r.RemoteAddr = "192.168.1.50:8080", lan
	r.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if loc := rec.Header().Get("Location"); loc != "/evil.example/x" {
		t.Errorf("redirect target %q must be a local path", loc)
	}
	if cs := rec.Result().Cookies(); len(cs) != 1 || !cs[0].Secure {
		t.Errorf("cookie over TLS must be Secure: %v", cs)
	}
}

// The login cookie must round-trip any token: net/http drops bytes that are
// not valid in a cookie value, which used to leave a mangled cookie and a
// login that silently never worked.
func TestTokenLoginOddCharacters(t *testing.T) {
	lan := "192.168.1.77:51000"
	for _, tok := range []string{"plain", "a b", "a;b", `a"b`, `a\b`, "pässwort", "a,b=c"} {
		tokenEnv(t, tok)
		h := newMux()
		w := tokenReq{path: "/eye/?token=" + url.QueryEscape(tok), host: "192.168.1.50:8080", remote: lan}.do(h)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/eye/" {
			t.Fatalf("%q: login got %d Location %q", tok, w.Code, w.Header().Get("Location"))
		}
		// Replay exactly what the browser would send back.
		r := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
		r.Host, r.RemoteAddr = "192.168.1.50:8080", lan
		r.Header.Set("Cookie", strings.SplitN(w.Header().Get("Set-Cookie"), ";", 2)[0])
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Errorf("%q: request with login cookie got %d", tok, rec.Code)
		}
	}
}

func TestTokenMatches(t *testing.T) {
	if !tokenMatches("abc", "abc") || tokenMatches("abc", "abd") || tokenMatches("", "") || tokenMatches("abc", "") || tokenMatches("ab", "abc") {
		t.Error("tokenMatches")
	}
}

func TestLanAccessProblem(t *testing.T) {
	cases := []struct {
		addr, hosts, token, behind, insecure string
		want                                 string // substring; "" = no message
	}{
		{"", "", "", "", "", ""},
		{"127.0.0.1", "localhost", "", "", "", ""},
		{"0.0.0.0", "", "", "1", "", ""}, // docker-compose default
		{"0.0.0.0", "", "", "", "", "ERROR"},
		{"127.0.0.1", "hermes.lan", "", "", "", "hermes.lan"},
		{"0.0.0.0", "hermes.lan", "", "1", "", "HERMES_TOKEN is empty"},
		{"0.0.0.0", "hermes.lan", "tok", "", "", ""},
		{"0.0.0.0", "", "", "", "1", "WARNING"},
	}
	for _, c := range cases {
		t.Setenv("ADDR", c.addr)
		t.Setenv("ALLOWED_HOSTS", c.hosts)
		t.Setenv("HERMES_TOKEN", c.token)
		t.Setenv("HERMES_BEHIND_LOOPBACK", c.behind)
		t.Setenv("HERMES_INSECURE_NO_TOKEN", c.insecure)
		got := lanAccessProblem()
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%+v: got %q, want %q", c, got, c.want)
		}
	}
}

func TestIsAdminRequestAndRequireUnlock(t *testing.T) {
	t.Setenv("HERMES_TOKEN", "s3cret")
	local := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	local.Host, local.RemoteAddr = "localhost:8080", "127.0.0.1:5000"
	if !isAdminRequest(local) {
		t.Error("local request must be admin")
	}
	lan := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	lan.Host, lan.RemoteAddr = "192.168.1.20:8080", "192.168.1.30:5000"
	lan.Header.Set("Authorization", "Bearer s3cret")
	if isAdminRequest(lan) {
		t.Error("LAN Host must not be admin, even with the token")
	}
	bridged := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	bridged.Host, bridged.RemoteAddr = "localhost:8080", "172.17.0.1:5000"
	if isAdminRequest(bridged) {
		t.Error("loopback Host from a bridge without the token must not be admin")
	}
	bridged.Header.Set("Authorization", "Bearer s3cret")
	if !isAdminRequest(bridged) {
		t.Error("loopback Host plus master token must be admin")
	}

	useTempSettings(t)
	h := requireUnlock(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	rec := httptest.NewRecorder()
	h(rec, local)
	if rec.Code != http.StatusNoContent {
		t.Errorf("lock off: status %d", rec.Code)
	}
	s := defaultSettings()
	s.PersonalLock = true
	if err := saveSettings(s); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h(rec, local)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"locked"`) {
		t.Errorf("lock on without cookie: %d %s", rec.Code, rec.Body)
	}
}

func listsReq(h http.Handler, method, target, bearer string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r.Host, r.RemoteAddr = "192.168.1.10:8080", "192.168.1.50:4000"
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestListsTokenScope(t *testing.T) {
	tokenEnv(t, "master-token")
	t.Setenv("HERMES_LISTS_TOKEN", "lists-token")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "lists.html"), []byte("<!doctype html>"), 0o644)
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html>"), 0o644)
	t.Setenv("STATIC_DIR", dir)
	withTestRegistry(t)
	registerRoute("GET /api/lists", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"listsOnly": listsScopeOnly(r)})
	})
	h := newMux()

	// The lists token in the URL sets the scoped cookie, only on list paths.
	rec := listsReq(h, http.MethodGet, "/lists.html?token=lists-token", "")
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == listsCookieName {
			cookie = c
		}
		if c.Name == tokenCookieName {
			t.Error("lists token set the master cookie")
		}
	}
	if rec.Code != http.StatusSeeOther || cookie == nil || !cookie.HttpOnly || cookie.Value == "lists-token" {
		t.Fatalf("lists login: %d %+v", rec.Code, cookie)
	}
	if rec := listsReq(h, http.MethodGet, "/?token=lists-token", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("lists token on the main page: %d", rec.Code)
	}

	allowed := []string{"/lists.html", "/api/lists"}
	for _, p := range allowed {
		if rec := listsReq(h, http.MethodGet, p, "", cookie); rec.Code != http.StatusOK {
			t.Errorf("cookie %s: %d", p, rec.Code)
		}
	}
	if rec := listsReq(h, http.MethodGet, "/api/lists", "", cookie); !strings.Contains(rec.Body.String(), `"listsOnly":true`) {
		t.Errorf("lists request not tagged: %s", rec.Body)
	}
	if rec := listsReq(h, http.MethodGet, "/api/lists", "lists-token"); rec.Code != http.StatusOK {
		t.Errorf("bearer lists token: %d", rec.Code)
	}
	for _, p := range []string{"/", "/index.html", "/api/settings", "/api/memories", "/api/chat", "/api/events", "/api/status", "/api/unlock"} {
		if rec := listsReq(h, http.MethodGet, p, "", cookie); rec.Code != http.StatusUnauthorized {
			t.Errorf("lists cookie opened %s: %d", p, rec.Code)
		}
		if rec := listsReq(h, http.MethodGet, p, "lists-token"); rec.Code != http.StatusUnauthorized {
			t.Errorf("lists bearer opened %s: %d", p, rec.Code)
		}
	}
	if rec := listsReq(h, http.MethodPost, "/api/settings", "", cookie); rec.Code != http.StatusUnauthorized {
		t.Errorf("lists cookie POST settings: %d", rec.Code)
	}
	// The master token still opens everything and is not lists-only.
	if rec := listsReq(h, http.MethodGet, "/api/lists", "master-token"); !strings.Contains(rec.Body.String(), `"listsOnly":false`) {
		t.Errorf("master token: %d %s", rec.Code, rec.Body)
	}
	// The lists cookie is not an admin credential, even with a loopback Host.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host, r.RemoteAddr = "localhost:8080", "192.168.1.50:4000"
	r.AddCookie(cookie)
	r.Header.Set("Authorization", "Bearer lists-token")
	if isAdminRequest(r) {
		t.Error("lists token made an admin request")
	}
	if !listsPathAllowed(httptest.NewRequest(http.MethodGet, "/api/events?scope=lists", nil)) ||
		listsPathAllowed(httptest.NewRequest(http.MethodGet, "/api/lists/../memories", nil)) ||
		listsPathAllowed(httptest.NewRequest(http.MethodPost, "/lists.html", nil)) {
		t.Error("listsPathAllowed")
	}
}

func TestStartupGuard(t *testing.T) {
	for _, c := range []struct {
		loopback, lists, hosts string
		fatal                  bool
	}{
		{"1", "", "", false},
		{"1", "", "localhost,127.0.0.1", false},
		{"1", "lists-token", "", true},
		{"1", "", "hermes.lan", true},
		{"0", "lists-token", "hermes.lan", false},
		{"", "lists-token", "", false},
	} {
		t.Setenv("HERMES_BEHIND_LOOPBACK", c.loopback)
		t.Setenv("HERMES_LISTS_TOKEN", c.lists)
		t.Setenv("ALLOWED_HOSTS", c.hosts)
		err := startupProblem()
		if (err != nil) != c.fatal {
			t.Errorf("%+v: %v", c, err)
		}
		if err != nil && !strings.Contains(err.Error(), "docker-compose.lan.yml") {
			t.Errorf("message: %v", err)
		}
	}
}

// The ?token= login redirect must stay a same-site path: a decoded "\" would
// make "/\evil.example" (browsers read it as "//evil.example") and a decoded
// "?" would change the query.
func TestTokenLoginRedirectKeepsEscapes(t *testing.T) {
	tokenEnv(t, "s3cret-token")
	h := newMux()
	for target, want := range map[string]string{
		"/%5Cevil.example?token=s3cret-token":      "/%5Cevil.example",
		"/%5C%5Cevil.example/x?token=s3cret-token": "/%5C%5Cevil.example/x",
		"/docs/a%3Fb?view=eye&token=s3cret-token":  "/docs/a%3Fb?view=eye",
		"/eye/?token=s3cret-token":                 "/eye/",
		"/a/../b?token=s3cret-token":               "/b",
	} {
		w := tokenReq{path: target, host: "192.168.1.50:8080", remote: "192.168.1.77:51000"}.do(h)
		if loc := w.Header().Get("Location"); w.Code != http.StatusSeeOther || loc != want {
			t.Errorf("%s: got %d Location %q, want %q", target, w.Code, loc, want)
		}
	}
}

// A request path is attacker-chosen text: an encoded newline must not start
// a forged line in the access log.
func TestLogMiddlewareNoLogInjection(t *testing.T) {
	var buf strings.Builder
	old, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(old); log.SetFlags(oldFlags) }()
	h := logMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x%0AGET%20/api/unlock%20200%201ms%0D", nil))
	out := strings.TrimSuffix(buf.String(), "\n")
	if strings.ContainsAny(out, "\r\n") {
		t.Errorf("log line split by the request path: %q", buf.String())
	}
}

// fakeGateway points the default-gateway lookup at a routing table whose
// default route goes via hexGW (little-endian, as in /proc/net/route); ""
// leaves no table at all.
func fakeGateway(t *testing.T, hexGW string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "route")
	if hexGW != "" {
		table := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
			"eth0\t00000000\t" + hexGW + "\t0003\t0\t0\t0\t00000000\t0\t0\t0\n" +
			"eth0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n"
		if err := os.WriteFile(path, []byte(table), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := routeFile
	routeFile = path
	gatewayCache.Lock()
	gatewayCache.until = time.Time{}
	gatewayCache.Unlock()
	t.Cleanup(func() {
		routeFile = old
		gatewayCache.Lock()
		gatewayCache.until = time.Time{}
		gatewayCache.Unlock()
	})
}

func TestReadDefaultGateway(t *testing.T) {
	fakeGateway(t, "010013AC")
	if gw := defaultGateway(); gw == nil || gw.String() != "172.19.0.1" {
		t.Errorf("gateway = %v, want 172.19.0.1", gw)
	}
}
