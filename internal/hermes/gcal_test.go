package hermes

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fake Google: token, revoke and Calendar API endpoints. Never the real one.

const (
	fgClientID = "fake-client.apps.googleusercontent.com"
	fgSecret   = "fake-secret-GCALTEST"
)

type fakeGoogle struct {
	t   *testing.T
	srv *httptest.Server

	mu            sync.Mutex
	codes         map[string]string // code -> PKCE challenge
	redirects     map[string]string // code -> redirect_uri at auth time
	scope         string            // granted scope
	noRefresh     bool              // answer the code exchange without a refresh token
	refreshErr    string            // e.g. "invalid_grant"
	access        string            // the currently valid access token
	accessN       int
	exchanges     int
	refreshes     int
	revoked       []string
	events        []map[string]any // returned by list (one page each pageSize)
	pageSize      int
	listQueries   []url.Values
	inserted      []map[string]any
	deleted       []string
	unauthorized  int // answer this many API calls with 401
	listStatus    int // non-zero: list answers with this status
	lastTokenForm url.Values
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	fg := &fakeGoogle{t: t, codes: map[string]string{}, redirects: map[string]string{},
		scope: googleCalendarScope, access: "at-0"}
	fg.srv = httptest.NewServer(http.HandlerFunc(fg.serve))
	t.Cleanup(fg.srv.Close)
	oldAuth, oldTok, oldRev, oldAPI := googleAuthURL, googleTokenURL, googleRevokeURL, googleCalendarAPI
	googleAuthURL = "https://accounts.google.com/o/oauth2/v2/auth" // only parsed, never fetched
	googleTokenURL = fg.srv.URL + "/token"
	googleRevokeURL = fg.srv.URL + "/revoke"
	googleCalendarAPI = fg.srv.URL + "/calendar/v3"
	t.Cleanup(func() {
		googleAuthURL, googleTokenURL, googleRevokeURL, googleCalendarAPI = oldAuth, oldTok, oldRev, oldAPI
	})
	return fg
}

// issueCode is what Google does when the user consents: a code bound to the
// PKCE challenge and redirect URI of the auth URL.
func (fg *fakeGoogle) issueCode(authURL string) (state, code string) {
	fg.t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		fg.t.Fatal(err)
	}
	q := u.Query()
	fg.mu.Lock()
	defer fg.mu.Unlock()
	code = "code-" + q.Get("state")[:8]
	fg.codes[code] = q.Get("code_challenge")
	fg.redirects[code] = q.Get("redirect_uri")
	return q.Get("state"), code
}

func (fg *fakeGoogle) tokenErr(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": "SECRET-DESCRIPTION"})
}

func (fg *fakeGoogle) serve(w http.ResponseWriter, r *http.Request) {
	fg.mu.Lock()
	defer fg.mu.Unlock()
	switch {
	case r.URL.Path == "/token":
		_ = r.ParseForm()
		fg.lastTokenForm = r.PostForm
		if r.PostForm.Get("client_id") != fgClientID || r.PostForm.Get("client_secret") != fgSecret {
			fg.tokenErr(w, 401, "invalid_client")
			return
		}
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			fg.exchanges++
			code := r.PostForm.Get("code")
			challenge, ok := fg.codes[code]
			delete(fg.codes, code)
			if !ok || pkceChallenge(r.PostForm.Get("code_verifier")) != challenge ||
				r.PostForm.Get("redirect_uri") != fg.redirects[code] {
				fg.tokenErr(w, 400, "invalid_grant")
				return
			}
			fg.accessN++
			fg.access = "at-" + string(rune('0'+fg.accessN))
			resp := map[string]any{"access_token": fg.access, "expires_in": 3599, "scope": fg.scope, "token_type": "Bearer"}
			if !fg.noRefresh {
				resp["refresh_token"] = "rt-1"
			}
			json.NewEncoder(w).Encode(resp)
		case "refresh_token":
			fg.refreshes++
			if fg.refreshErr != "" {
				fg.tokenErr(w, 400, fg.refreshErr)
				return
			}
			if r.PostForm.Get("refresh_token") != "rt-1" {
				fg.tokenErr(w, 400, "invalid_grant")
				return
			}
			fg.accessN++
			fg.access = "at-" + string(rune('0'+fg.accessN))
			json.NewEncoder(w).Encode(map[string]any{"access_token": fg.access, "expires_in": 3599, "scope": fg.scope})
		default:
			fg.tokenErr(w, 400, "unsupported_grant_type")
		}
	case r.URL.Path == "/revoke":
		_ = r.ParseForm()
		fg.revoked = append(fg.revoked, r.PostForm.Get("token"))
	case strings.HasPrefix(r.URL.Path, "/calendar/v3/calendars/primary/events"):
		if fg.unauthorized > 0 {
			fg.unauthorized--
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+fg.access {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/calendar/v3/calendars/primary/events")
		switch {
		case r.Method == http.MethodGet && rest == "":
			fg.listQueries = append(fg.listQueries, r.URL.Query())
			if fg.listStatus != 0 {
				w.WriteHeader(fg.listStatus)
				return
			}
			items, next := fg.events, ""
			if fg.pageSize > 0 {
				start := 0
				if pt := r.URL.Query().Get("pageToken"); pt != "" {
					start = int(pt[0] - '0')
				}
				end := start + fg.pageSize
				if end < len(items) {
					next = string(rune('0' + end))
				} else {
					end = len(items)
				}
				items = items[start:end]
			}
			json.NewEncoder(w).Encode(map[string]any{"items": items, "nextPageToken": next})
		case r.Method == http.MethodPost && rest == "":
			var ev map[string]any
			_ = json.NewDecoder(r.Body).Decode(&ev)
			fg.inserted = append(fg.inserted, ev)
			ev["id"] = "ev" + string(rune('0'+len(fg.inserted)))
			ev["status"] = "confirmed"
			json.NewEncoder(w).Encode(ev)
		case r.Method == http.MethodDelete && strings.HasPrefix(rest, "/"):
			fg.deleted = append(fg.deleted, strings.TrimPrefix(rest, "/"))
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (fg *fakeGoogle) set(fn func(fg *fakeGoogle)) {
	fg.mu.Lock()
	defer fg.mu.Unlock()
	fn(fg)
}

func (fg *fakeGoogle) get(fn func(fg *fakeGoogle)) { fg.set(fn) }

// gcalEnv: the reminders harness (temp data and settings, Anna and Ben,
// Friday 6 November 2026 14:30 Berlin), a Google client in env, a fake
// Google, and clean calendar state.
func gcalEnv(t *testing.T) *fakeGoogle {
	t.Helper()
	remEnv(t)
	t.Setenv("GOOGLE_CLIENT_ID", fgClientID)
	t.Setenv("GOOGLE_CLIENT_SECRET", fgSecret)
	t.Setenv("HERMES_TOKEN", "")
	resetCalendarState()
	t.Cleanup(resetCalendarState)
	return newFakeGoogle(t)
}

func resetCalendarState() {
	gcalStates.Lock()
	gcalStates.m = map[string]gcalPending{}
	gcalStates.Unlock()
	gcalTok.Lock()
	gcalTok.loaded, gcalTok.tok, gcalTok.path, gcalTok.readOnly = false, nil, "", false
	gcalTok.Unlock()
	calendarCacheClear()
	calOffers.Lock()
	calOffers.m = map[string]calOfferNote{}
	calOffers.Unlock()
}

// connectFake stores a token as if the consent flow had run.
func connectFake(t *testing.T, fg *fakeGoogle) {
	t.Helper()
	fg.set(func(fg *fakeGoogle) { fg.access = "at-0" })
	if err := gcalSaveToken(googleToken{RefreshToken: "rt-1", AccessToken: "at-0", Expiry: nowFunc().Add(time.Hour),
		Scope: googleCalendarScope, ClientID: fgClientID}); err != nil {
		t.Fatal(err)
	}
}

func calReq(h http.Handler, method, path, body string, mod func(r *http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Host = "localhost:8080"
	r.Header.Set("Content-Type", "application/json")
	if mod != nil {
		mod(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func calStatusOf(t *testing.T) map[string]any {
	t.Helper()
	b, _ := json.Marshal(gcalStatus())
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// ---------------------------------------------------------------------------
// PKCE and states

func TestPKCEChallengeRFC7636(t *testing.T) {
	// RFC 7636 Appendix B.
	if got := pkceChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("challenge %q", got)
	}
	v, err := randURLToken(32)
	if err != nil || len(v) != 43 {
		t.Fatalf("verifier %q %v", v, err)
	}
	sum := sha256.Sum256([]byte(v))
	if pkceChallenge(v) != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatal("S256 mismatch")
	}
}

func TestGcalAuthURLAndStates(t *testing.T) {
	gcalEnv(t)
	u, err := gcalAuthStart("http://localhost:8080/api/calendar/callback", "")
	if err != nil {
		t.Fatal(err)
	}
	pu, _ := url.Parse(u)
	q := pu.Query()
	for k, want := range map[string]string{
		"client_id": fgClientID, "redirect_uri": "http://localhost:8080/api/calendar/callback",
		"response_type": "code", "scope": googleCalendarScope, "code_challenge_method": "S256",
		"access_type": "offline", "prompt": "consent",
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	if len(q.Get("state")) != 43 || len(q.Get("code_challenge")) != 43 || strings.Contains(u, fgSecret) {
		t.Fatalf("auth url %s", u)
	}
	// Single use.
	if _, ok := gcalTakeState(q.Get("state")); !ok {
		t.Fatal("state unknown")
	}
	if _, ok := gcalTakeState(q.Get("state")); ok {
		t.Fatal("state reused")
	}
	// At most 5 pending; the oldest goes.
	clock := time.Now()
	old := nowFunc
	nowFunc = func() time.Time { return clock }
	t.Cleanup(func() { nowFunc = old })
	var states []string
	for i := 0; i < 7; i++ {
		clock = clock.Add(time.Second)
		u, _ := gcalAuthStart("http://localhost:8080/api/calendar/callback", "")
		pu, _ := url.Parse(u)
		states = append(states, pu.Query().Get("state"))
	}
	gcalStates.Lock()
	n := len(gcalStates.m)
	gcalStates.Unlock()
	if n != gcalMaxStates {
		t.Fatalf("%d pending states", n)
	}
	if _, ok := gcalTakeState(states[0]); ok {
		t.Fatal("oldest state not evicted")
	}
	if _, ok := gcalTakeState(states[6]); !ok {
		t.Fatal("newest state missing")
	}
	// Expiry after 10 minutes.
	clock = clock.Add(gcalStateTTL + time.Second)
	if _, ok := gcalTakeState(states[5]); ok {
		t.Fatal("expired state accepted")
	}
}

// ---------------------------------------------------------------------------
// Connect, callback, disconnect

func TestCalendarConnectCallbackDisconnect(t *testing.T) {
	fg := gcalEnv(t)
	h := newMux()
	sub := subscribe(t, false)

	rec := calReq(h, "POST", "/api/calendar/connect", `{"owner":"p_anna"}`, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") })
	mustCode(t, rec, 200, "connect")
	var cr struct {
		AuthURL string `json:"authUrl"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &cr)
	if !strings.HasPrefix(cr.AuthURL, "https://accounts.google.com/") {
		t.Fatalf("authUrl %q", cr.AuthURL)
	}
	state, code := fg.issueCode(cr.AuthURL)

	// The callback must arrive with a loopback Host.
	rec = calReq(h, "GET", "/api/calendar/callback?state="+state+"&code="+code, "", func(r *http.Request) {
		r.Host = "hermes.lan:8080"
		r.RemoteAddr = "192.168.1.20:5000"
	})
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		t.Fatalf("LAN-host callback: %d", rec.Code)
	}

	rec = calReq(h, "GET", "/api/calendar/callback?state="+url.QueryEscape(state)+"&code="+code, "", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/?calendar=connected" {
		t.Fatalf("callback %d %q", rec.Code, rec.Header().Get("Location"))
	}
	fi, err := os.Stat(dataPath(gcalTokenFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file %v %v", fi, err)
	}
	b, _ := os.ReadFile(dataPath(gcalTokenFile))
	if !strings.Contains(string(b), "rt-1") || !strings.Contains(string(b), fgClientID) {
		t.Fatalf("token file %s", b)
	}
	st := calStatusOf(t)
	if st["connected"] != true || st["configured"] != true || st["owner"] != "p_anna" || st["ownerName"] != "Anna" || st["needsRelink"] != false {
		t.Fatalf("status %v", st)
	}
	// GET status does not leak tokens or the client.
	rec = calReq(h, "GET", "/api/calendar/status", "", nil)
	for _, s := range []string{"rt-1", "at-1", fgSecret, fgClientID} {
		if strings.Contains(rec.Body.String(), s) {
			t.Fatalf("status leaks %q: %s", s, rec.Body)
		}
	}
	if got := drainTypes(sub); !containsStr(got, "calendar.changed") {
		t.Fatalf("events %v", got)
	}

	// The state is single-use.
	rec = calReq(h, "GET", "/api/calendar/callback?state="+url.QueryEscape(state)+"&code="+code, "", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/?calendar=error&reason=state" {
		t.Fatalf("replayed state: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// Disconnect: non-admin refused, admin revokes and deletes.
	rec = calReq(h, "POST", "/api/calendar/disconnect", "", func(r *http.Request) { r.RemoteAddr = "192.168.1.20:5000"; r.Host = "192.168.1.2:8080" })
	if rec.Code == 200 {
		t.Fatal("LAN disconnect allowed")
	}
	rec = calReq(h, "POST", "/api/calendar/disconnect", "", nil)
	mustCode(t, rec, 200, "disconnect")
	if _, err := os.Stat(dataPath(gcalTokenFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token file still there: %v", err)
	}
	fg.get(func(fg *fakeGoogle) {
		if len(fg.revoked) != 1 || fg.revoked[0] != "rt-1" {
			t.Errorf("revoked %v", fg.revoked)
		}
	})
	if st := calStatusOf(t); st["connected"] != false {
		t.Fatalf("status after disconnect %v", st)
	}
	if got := drainTypes(sub); !containsStr(got, "calendar.changed") {
		t.Fatalf("events after disconnect %v", got)
	}
}

func TestCalendarConnectGuards(t *testing.T) {
	gcalEnv(t)
	h := newMux()
	// GET never creates a state (an <img> could trigger it).
	if rec := calReq(h, "GET", "/api/calendar/connect", "", nil); rec.Code == 200 {
		t.Fatal("GET connect allowed")
	}
	rec := calReq(h, "POST", "/api/calendar/connect", `{}`, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	mustCode(t, rec, 403, "cross-site")
	// A LAN client (with the master token, non-loopback Host) is no admin.
	t.Setenv("HERMES_TOKEN", "master-token-123")
	rec = calReq(h, "POST", "/api/calendar/connect", `{}`, func(r *http.Request) {
		r.RemoteAddr = "192.168.1.20:5000"
		r.Host = "192.168.1.2:8080"
		r.Header.Set("Authorization", "Bearer master-token-123")
	})
	if rec.Code != 403 || integErrCode(rec) != "local_only" {
		t.Fatalf("LAN connect: %d %s", rec.Code, rec.Body)
	}
	// Docker LAN layout: the Mac's browser (loopback Host, master token,
	// remote address of the Docker bridge) is an admin.
	rec = calReq(h, "POST", "/api/calendar/connect", `{}`, func(r *http.Request) {
		r.RemoteAddr = "172.17.0.1:5000"
		r.Host = "localhost:8080"
		r.Header.Set("Authorization", "Bearer master-token-123")
	})
	mustCode(t, rec, 200, "docker admin connect")
	// Unknown owner.
	rec = calReq(h, "POST", "/api/calendar/connect", `{"owner":"p_nobody"}`, nil)
	mustCode(t, rec, 400, "unknown owner")
	// No client configured.
	t.Setenv("GOOGLE_CLIENT_ID", "")
	rec = calReq(h, "POST", "/api/calendar/connect", `{}`, nil)
	if rec.Code != 503 || integErrCode(rec) != "no_client" {
		t.Fatalf("no client: %d %s", rec.Code, rec.Body)
	}
}

func TestLoopbackRedirect(t *testing.T) {
	for host, want := range map[string]string{
		"localhost:8080": "http://localhost:8080/api/calendar/callback",
		"127.0.0.1:8080": "http://127.0.0.1:8080/api/calendar/callback",
		"[::1]:8080":     "http://[::1]:8080/api/calendar/callback",
		"192.168.1.2":    "",
		"evil.com:8080":  "",
	} {
		got, ok := loopbackRedirect(host)
		if got != want || ok != (want != "") {
			t.Errorf("%s: %q %v", host, got, ok)
		}
	}
}

func TestCalendarCallbackScopeAndErrors(t *testing.T) {
	fg := gcalEnv(t)
	h := newMux()
	start := func() (string, string) {
		u, err := gcalAuthStart("http://localhost:8080/api/calendar/callback", "")
		if err != nil {
			t.Fatal(err)
		}
		return fg.issueCode(u)
	}
	loc := func(rec *httptest.ResponseRecorder) string { return rec.Header().Get("Location") }

	// Read-only scope granted: refused, nothing stored.
	fg.set(func(fg *fakeGoogle) { fg.scope = "https://www.googleapis.com/auth/calendar.readonly" })
	state, code := start()
	if rec := calReq(h, "GET", "/api/calendar/callback?state="+state+"&code="+code, "", nil); loc(rec) != "/?calendar=error&reason=scope" {
		t.Fatalf("scope: %q", loc(rec))
	}
	if _, ok := gcalToken(); ok {
		t.Fatal("token stored despite missing scope")
	}
	fg.set(func(fg *fakeGoogle) { fg.scope = "openid " + googleCalendarScope })

	// The user pressed "cancel".
	state, _ = start()
	if rec := calReq(h, "GET", "/api/calendar/callback?state="+state+"&error=access_denied", "", nil); loc(rec) != "/?calendar=error&reason=denied" {
		t.Fatalf("denied: %q", loc(rec))
	}
	// A wrong code (the verifier would not match).
	state, _ = start()
	rec := calReq(h, "GET", "/api/calendar/callback?state="+state+"&code=forged", "", nil)
	if loc(rec) != "/?calendar=error&reason=exchange" || strings.Contains(rec.Body.String(), "SECRET-DESCRIPTION") {
		t.Fatalf("forged code: %q", loc(rec))
	}
	// No refresh token.
	fg.set(func(fg *fakeGoogle) { fg.noRefresh = true })
	state, code = start()
	if rec := calReq(h, "GET", "/api/calendar/callback?state="+state+"&code="+code, "", nil); loc(rec) != "/?calendar=error&reason=refresh" {
		t.Fatalf("no refresh: %q", loc(rec))
	}
	fg.set(func(fg *fakeGoogle) { fg.noRefresh = false })
	// Expired state.
	state, code = start()
	clock := nowFunc()
	old := nowFunc
	nowFunc = func() time.Time { return clock.Add(gcalStateTTL + time.Minute) }
	rec = calReq(h, "GET", "/api/calendar/callback?state="+state+"&code="+code, "", nil)
	nowFunc = old
	if loc(rec) != "/?calendar=error&reason=state" {
		t.Fatalf("expired: %q", loc(rec))
	}
	// Finally a good one.
	state, code = start()
	if rec := calReq(h, "GET", "/api/calendar/callback?state="+state+"&code="+code, "", nil); loc(rec) != "/?calendar=connected" {
		t.Fatalf("good: %q", loc(rec))
	}
	if tok, ok := gcalToken(); !ok || tok.Scope != "openid "+googleCalendarScope {
		t.Fatalf("token %+v", tok)
	}
}

// ---------------------------------------------------------------------------
// Token refresh and relink

func TestGcalRefreshOnExpiryAnd401(t *testing.T) {
	fg := gcalEnv(t)
	connectFake(t, fg)
	// Expired access token: one refresh, then the list.
	tok, _ := gcalToken()
	tok.Expiry = nowFunc().Add(-time.Minute)
	if err := gcalSaveToken(tok); err != nil {
		t.Fatal(err)
	}
	if _, err := gcalList(context.Background(), at(7, 0, 0), at(8, 0, 0), berlin); err != nil {
		t.Fatal(err)
	}
	fg.get(func(fg *fakeGoogle) {
		if fg.refreshes != 1 || fg.lastTokenForm.Get("refresh_token") != "rt-1" {
			t.Errorf("refreshes %d form %v", fg.refreshes, fg.lastTokenForm)
		}
	})
	tok, _ = gcalToken()
	if tok.AccessToken != "at-1" || !tok.Expiry.After(nowFunc()) || tok.RefreshToken != "rt-1" {
		t.Fatalf("refreshed token %+v", tok)
	}
	// A 401 although the token looks valid: refresh once and retry.
	fg.set(func(fg *fakeGoogle) { fg.unauthorized = 1 })
	if _, err := gcalList(context.Background(), at(7, 0, 0), at(8, 0, 0), berlin); err != nil {
		t.Fatal(err)
	}
	fg.get(func(fg *fakeGoogle) {
		if fg.refreshes != 2 {
			t.Errorf("refreshes after 401: %d", fg.refreshes)
		}
	})
}

func TestGcalInvalidGrantNeedsRelink(t *testing.T) {
	fg := gcalEnv(t)
	connectFake(t, fg)
	sub := subscribe(t, false)
	tok, _ := gcalToken()
	tok.Expiry = nowFunc().Add(-time.Minute)
	_ = gcalSaveToken(tok)
	fg.set(func(fg *fakeGoogle) { fg.refreshErr = "invalid_grant" })
	_, err := gcalList(context.Background(), at(7, 0, 0), at(8, 0, 0), berlin)
	if !errors.Is(err, errCalendarRelink) {
		t.Fatalf("err %v", err)
	}
	if st := calStatusOf(t); st["needsRelink"] != true || st["connected"] != false {
		t.Fatalf("status %v", st)
	}
	if got := drainTypes(sub); !containsStr(got, "calendar.changed") {
		t.Fatalf("events %v", got)
	}
	// No further calls to Google while it needs a relink.
	_, err = gcalList(context.Background(), at(7, 0, 0), at(8, 0, 0), berlin)
	fg.get(func(fg *fakeGoogle) {
		if fg.refreshes != 1 || !errors.Is(err, errCalendarRelink) {
			t.Errorf("refreshes %d err %v", fg.refreshes, err)
		}
	})
	// The spoken answers say so, in both languages.
	if res := remSay(t, "Was steht morgen an?", remSingle); !strings.Contains(res.text, "Kalenderverbindung ist abgelaufen") {
		t.Fatalf("DE relink text %q", res.text)
	}
	if res := remSay(t, "What's on tomorrow?", remSingle, "en"); !strings.Contains(res.text, "calendar connection has expired") {
		t.Fatalf("EN relink text %q", res.text)
	}
}

func TestGcalClientChangeRequiresRelink(t *testing.T) {
	fg := gcalEnv(t)
	connectFake(t, fg)
	sub := subscribe(t, false)
	// Only the secret rotated: the refresh token stays valid.
	gcalClientChanged()
	if st := calStatusOf(t); st["connected"] != true {
		t.Fatalf("secret rotation broke the link: %v", st)
	}
	// A new client id: relink, pending consent flows dropped.
	if _, err := gcalAuthStart("http://localhost:8080/api/calendar/callback", ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_CLIENT_ID", "other-client.apps.googleusercontent.com")
	gcalClientChanged()
	if st := calStatusOf(t); st["needsRelink"] != true || st["connected"] != false {
		t.Fatalf("status %v", st)
	}
	gcalStates.Lock()
	n := len(gcalStates.m)
	gcalStates.Unlock()
	if n != 0 {
		t.Fatalf("%d pending states kept", n)
	}
	if got := drainTypes(sub); !containsStr(got, "calendar.changed") {
		t.Fatalf("events %v", got)
	}
	// The hook is wired to the integrations API.
	connectFake(t, fg)
	t.Setenv("GOOGLE_CLIENT_ID", "")
	t.Setenv("GOOGLE_CLIENT_SECRET", "")
	_ = updateSecrets(func(s *secretsFile) error { s.GoogleClientID, s.GoogleClientSecret = fgClientID, fgSecret; return nil })
	if st := calStatusOf(t); st["connected"] != true {
		t.Fatalf("status before API change %v", st)
	}
	rec := calReq(newMux(), "POST", "/api/integrations/google", `{"clientId":"third.apps.googleusercontent.com","clientSecret":"x-secret-3"}`, nil)
	mustCode(t, rec, 200, "integrations google")
	if st := calStatusOf(t); st["needsRelink"] != true {
		t.Fatalf("API client change did not require a relink: %v", st)
	}
	// An env change (no hook) is caught on the next use.
	connectFake(t, fg)
	if _, err := gcalAccessToken(context.Background(), false); !errors.Is(err, errCalendarRelink) {
		t.Fatalf("mismatch not detected: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Calendar API

func TestGcalListConvertsEvents(t *testing.T) {
	fg := gcalEnv(t)
	connectFake(t, fg)
	fg.set(func(fg *fakeGoogle) {
		fg.pageSize = 2
		fg.events = []map[string]any{
			{"id": "a", "summary": "Urlaub", "start": map[string]any{"date": "2026-11-07"}, "end": map[string]any{"date": "2026-11-09"}},
			{"id": "b", "summary": "Zahnarzt", "location": "Praxis\nMitte", "start": map[string]any{"dateTime": "2026-11-07T10:00:00+01:00"}, "end": map[string]any{"dateTime": "2026-11-07T11:00:00+01:00"}},
			{"id": "c", "summary": "Abgesagt", "status": "cancelled", "start": map[string]any{"dateTime": "2026-11-07T12:00:00+01:00"}, "end": map[string]any{"dateTime": "2026-11-07T13:00:00+01:00"}},
			{"id": "d", "summary": "Team-Call", "start": map[string]any{"dateTime": "2026-11-07T14:30:00Z"}, "end": map[string]any{"dateTime": "2026-11-07T15:00:00Z"},
				"attendees": []map[string]any{{"email": "x@y", "responseStatus": "accepted"}, {"self": true, "responseStatus": "needsAction"}}},
			{"id": "e", "summary": "Kaputt", "start": map[string]any{}},
		}
	})
	evs, err := gcalList(context.Background(), at(7, 0, 0), at(8, 0, 0), berlin)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 {
		t.Fatalf("events %+v", evs)
	}
	u := evs[0]
	if u.Title != "Urlaub" || !u.AllDay || !u.Start.Equal(at(7, 0, 0)) || !u.End.Equal(at(9, 0, 0)) {
		t.Errorf("all-day (exclusive end) %+v", u)
	}
	if z := evs[1]; z.Title != "Zahnarzt" || z.Location != "Praxis Mitte" || !z.Start.Equal(at(7, 10, 0)) || z.AllDay {
		t.Errorf("timed %+v", z)
	}
	if c := evs[2]; !c.NeedsAction || !c.Start.Equal(at(7, 15, 30)) || c.Start.Location() != berlin {
		t.Errorf("invite %+v", c)
	}
	fg.get(func(fg *fakeGoogle) {
		if len(fg.listQueries) != 3 { // 5 items, 2 per page
			t.Fatalf("pages %d", len(fg.listQueries))
		}
		q := fg.listQueries[0]
		for k, want := range map[string]string{"singleEvents": "true", "orderBy": "startTime", "maxResults": "100",
			"timeZone": "Europe/Berlin", "timeMin": "2026-11-07T00:00:00+01:00", "timeMax": "2026-11-08T00:00:00+01:00"} {
			if q.Get(k) != want {
				t.Errorf("%s=%q want %q", k, q.Get(k), want)
			}
		}
	})
	// Range checks.
	if _, err := gcalList(context.Background(), at(8, 0, 0), at(7, 0, 0), berlin); !errors.Is(err, errCalendarRange) {
		t.Errorf("reversed range: %v", err)
	}
	if _, err := gcalList(context.Background(), at(1, 0, 0), at(1, 0, 0).AddDate(0, 0, 63), berlin); !errors.Is(err, errCalendarRange) {
		t.Errorf("63 days: %v", err)
	}
	// Not connected.
	_, _, _ = gcalDeleteToken()
	if _, err := fetchCalendarEvents(context.Background(), at(7, 0, 0), at(8, 0, 0)); !errors.Is(err, errCalendarNotConnected) {
		t.Errorf("not connected: %v", err)
	}
}

func TestGcalInsertAndDelete(t *testing.T) {
	fg := gcalEnv(t)
	connectFake(t, fg)
	ev, err := gcalInsert(context.Background(), "Zahnarzt", at(7, 15, 0), 90*time.Minute, false, berlin)
	if err != nil || ev.ID != "ev1" || !ev.Start.Equal(at(7, 15, 0)) || !ev.End.Equal(at(7, 16, 30)) {
		t.Fatalf("insert %+v %v", ev, err)
	}
	ev2, err := gcalInsert(context.Background(), "Urlaub", at(13, 9, 0), 0, true, berlin)
	if err != nil || !ev2.AllDay || !ev2.Start.Equal(at(13, 0, 0)) || !ev2.End.Equal(at(14, 0, 0)) {
		t.Fatalf("all-day insert %+v %v", ev2, err)
	}
	fg.get(func(fg *fakeGoogle) {
		timed, _ := json.Marshal(fg.inserted[0])
		if !strings.Contains(string(timed), `"dateTime":"2026-11-07T15:00:00+01:00"`) || !strings.Contains(string(timed), `"timeZone":"Europe/Berlin"`) ||
			!strings.Contains(string(timed), `"dateTime":"2026-11-07T16:30:00+01:00"`) {
			t.Errorf("timed body %s", timed)
		}
		allDay, _ := json.Marshal(fg.inserted[1])
		if !strings.Contains(string(allDay), `"start":{"date":"2026-11-13"}`) || !strings.Contains(string(allDay), `"end":{"date":"2026-11-14"}`) {
			t.Errorf("all-day body %s", allDay)
		}
	})
	if err := gcalDelete(context.Background(), "ev1"); err != nil {
		t.Fatal(err)
	}
	fg.get(func(fg *fakeGoogle) {
		if len(fg.deleted) != 1 || fg.deleted[0] != "ev1" {
			t.Errorf("deleted %v", fg.deleted)
		}
	})
}

// ---------------------------------------------------------------------------
// GET /api/calendar/events

func TestCalendarEventsAPI(t *testing.T) {
	fg := gcalEnv(t)
	h := newMux()
	path := "/api/calendar/events?from=" + url.QueryEscape("2026-11-07T00:00:00+01:00") + "&to=" + url.QueryEscape("2026-11-08T00:00:00+01:00")
	if rec := calReq(h, "GET", path, "", nil); rec.Code != 503 || integErrCode(rec) != "not_connected" {
		t.Fatalf("not connected: %d %s", rec.Code, rec.Body)
	}
	connectFake(t, fg)
	fg.set(func(fg *fakeGoogle) {
		fg.events = []map[string]any{{"id": "b", "summary": "Zahnarzt", "start": map[string]any{"dateTime": "2026-11-07T09:00:00Z"}, "end": map[string]any{"dateTime": "2026-11-07T10:00:00Z"}}}
	})
	rec := calReq(h, "GET", path, "", nil)
	mustCode(t, rec, 200, "events")
	var out struct {
		Events []map[string]any `json:"events"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Events) != 1 || out.Events[0]["title"] != "Zahnarzt" || out.Events[0]["start"] != "2026-11-07T10:00:00+01:00" || out.Events[0]["allDay"] != false {
		t.Fatalf("events %s", rec.Body)
	}
	long := "/api/calendar/events?from=" + url.QueryEscape("2026-11-01T00:00:00+01:00") + "&to=" + url.QueryEscape("2027-01-05T00:00:00+01:00")
	if rec := calReq(h, "GET", long, "", nil); rec.Code != 400 {
		t.Fatalf("63-day range: %d", rec.Code)
	}
	if rec := calReq(h, "GET", "/api/calendar/events?from=x&to=y", "", nil); rec.Code != 400 {
		t.Fatalf("bad times: %d", rec.Code)
	}
	// With the personal lock on, the unlock cookie is required.
	setTestPIN(t, "1234")
	s := loadSettings()
	s.PersonalLock = true
	if err := saveSettings(s); err != nil {
		t.Fatal(err)
	}
	if rec := calReq(h, "GET", path, "", nil); rec.Code != 403 || integErrCode(rec) != "locked" {
		t.Fatalf("locked: %d %s", rec.Code, rec.Body)
	}
	unlock := calReq(h, "POST", "/api/unlock", `{"pin":"1234"}`, nil)
	c := unlockCookie(unlock)
	if c == nil {
		t.Fatalf("no unlock cookie: %d %s", unlock.Code, unlock.Body)
	}
	if rec := calReq(h, "GET", path, "", func(r *http.Request) { r.AddCookie(c) }); rec.Code != 200 {
		t.Fatalf("unlocked: %d %s", rec.Code, rec.Body)
	}
}

func TestCalendarEventsRefusesListsToken(t *testing.T) {
	fg := gcalEnv(t)
	connectFake(t, fg)
	t.Setenv("HERMES_TOKEN", "master-token-123")
	t.Setenv("HERMES_LISTS_TOKEN", "lists-token-456")
	t.Setenv("HERMES_INSECURE_NO_TOKEN", "")
	h := newMux()
	path := "/api/calendar/events?from=" + url.QueryEscape("2026-11-07T00:00:00+01:00") + "&to=" + url.QueryEscape("2026-11-08T00:00:00+01:00")
	rec := calReq(h, "GET", path, "", func(r *http.Request) {
		r.RemoteAddr = "192.168.1.20:5000"
		r.Host = "192.168.1.2:8080"
		r.Header.Set("Authorization", "Bearer lists-token-456")
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("lists token read the calendar: %d %s", rec.Code, rec.Body)
	}
	body, _ := io.ReadAll(rec.Body)
	if strings.Contains(string(body), "Zahnarzt") {
		t.Fatal("calendar data leaked")
	}
}

// ---------------------------------------------------------------------------
// Status provider and person deletion

func TestCalendarStatusProviderAndOwnerDeletion(t *testing.T) {
	fg := gcalEnv(t)
	connectFake(t, fg)
	_ = updateSecrets(func(s *secretsFile) error { s.CalendarOwner = "p_anna"; return nil })
	st := collectStatus(context.Background())
	cal, ok := st["calendar"].(map[string]any)
	if !ok || cal["connected"] != true || cal["owner"] != "p_anna" {
		t.Fatalf("status %v", st["calendar"])
	}
	if kind, n := calendarOwnerDeleted("p_ben"); n != 0 || kind != "" {
		t.Fatalf("other person: %s %d", kind, n)
	}
	if kind, n := calendarOwnerDeleted("p_anna"); kind != "calendar" || n != 1 {
		t.Fatalf("owner: %s %d", kind, n)
	}
	if _, ok := gcalToken(); ok || loadSecrets().CalendarOwner != "" {
		t.Fatal("owner deletion kept the calendar")
	}
	fg.get(func(fg *fakeGoogle) {
		if len(fg.revoked) != 1 {
			t.Errorf("revoked %v", fg.revoked)
		}
	})
}

// A refresh that is in flight while the user disconnects (or the owner is
// deleted) must not write the token file back: the calendar would silently
// be connected again with a token the user asked HERMES to forget.
func TestGcalRefreshDuringDisconnectDoesNotResurrectToken(t *testing.T) {
	fg := gcalEnv(t)
	connectFake(t, fg)
	tok, _ := gcalToken()
	tok.Expiry = nowFunc().Add(-time.Minute)
	if err := gcalSaveToken(tok); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"at-late","expires_in":3599,"scope":"`+googleCalendarScope+`"}`)
	}))
	defer slow.Close()
	googleTokenURL = slow.URL + "/token"
	done := make(chan error, 1)
	go func() {
		_, err := gcalAccessToken(context.Background(), false)
		done <- err
	}()
	<-started
	if err := gcalDisconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	if _, ok := gcalToken(); ok {
		t.Fatal("token is back after disconnect")
	}
	if _, err := os.Stat(dataPath(gcalTokenFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token file exists after disconnect: %v", err)
	}
	// Reload from disk too (a restart).
	gcalTok.Lock()
	gcalTok.loaded = false
	gcalTok.Unlock()
	if st := calStatusOf(t); st["connected"] != false {
		t.Fatalf("status after disconnect: %v", st)
	}
}
