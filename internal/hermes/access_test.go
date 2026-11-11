package hermes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The access matrix of Section 4.10, cell by cell.
func TestAccessMatrixPerSource(t *testing.T) {
	type cell struct{ open, read, write, extract bool }
	all, openOnly := cell{true, true, true, true}, cell{true, false, false, false}
	cases := []struct {
		name     string
		source   string
		lock     bool
		unlocked bool
		want     cell
	}{
		{"voice", SourceVoice, false, false, all},
		{"voice, lock on", SourceVoice, true, false, all},
		{"single", SourceSingle, false, false, all},
		{"manual, lock off", SourceManual, false, false, cell{true, true, true, false}},
		{"manual, PIN-unlocked", SourceManual, true, true, all},
		{"manual, lock on, no cookie", SourceManual, true, false, openOnly},
		{"claim, lock off", SourceClaim, false, false, cell{true, true, true, false}},
		{"claim, lock on", SourceClaim, true, false, openOnly},
		{"sticky", SourceSticky, false, false, openOnly},
		{"uncertain", SourceUncertain, false, false, openOnly},
		{"guest", SourceGuest, false, false, openOnly},
		{"guest, unlocked", SourceGuest, true, true, openOnly},
	}
	for _, c := range cases {
		s := defaultSettings()
		s.PersonalLock = c.lock
		tr := &Turn{Settings: s, Unlocked: c.unlocked, Speaker: Speaker{ID: "p_anna", Source: c.source}}
		got := cell{tr.Can(AccessOpen), tr.Can(AccessRead), tr.Can(AccessWrite), tr.Can(AccessExtract)}
		if got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
		if tr.PersonalAllowed() != c.want.read {
			t.Errorf("%s: PersonalAllowed", c.name)
		}
	}
}

// stubAccessTools registers one fast-path tool per access class.
func stubAccessTools(t *testing.T) map[Access]*int {
	t.Helper()
	runs := map[Access]*int{}
	for _, a := range []Access{AccessOpen, AccessRead, AccessWrite} {
		a := a
		n := new(int)
		runs[a] = n
		name := "stub_" + a.String()
		registerTool(Tool{Name: name, Access: func(*Turn, json.RawMessage) Access { return a },
			Run: func(*Turn, json.RawMessage) Reply { *n++; return Reply{Text: "ran " + name} }})
		registerFastPath(FastPath{Name: name, Order: 40, Tool: name, Match: func(t *Turn) (json.RawMessage, bool) {
			return nil, hasPhrase(t.Tokens, "stub"+a.String())
		}})
	}
	return runs
}

// Central enforcement: whatever the source, Run is only called when allowed,
// and the refusal fits the source.
func TestAccessEnforcedCentrallyPerSource(t *testing.T) {
	cases := []struct {
		source  string
		allowed map[Access]bool
		refusal string
	}{
		{SourceVoice, map[Access]bool{AccessOpen: true, AccessRead: true, AccessWrite: true}, ""},
		{SourceSticky, map[Access]bool{AccessOpen: true}, "not sure who is speaking"},
		{SourceGuest, map[Access]bool{AccessOpen: true}, "only do that for people I know"},
		{SourceUncertain, map[Access]bool{AccessOpen: true}, "Who is speaking?"},
	}
	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			chatEnv(t)
			useTempData(t)
			t.Setenv("OLLAMA_BASE", "http://127.0.0.1:1")
			withTestRegistry(t)
			runs := stubAccessTools(t)
			peopleCountProvider = func() int { return 1 }
			speakerResolver = func(*Turn, *VoiceInput, string, string) Speaker {
				if c.source == SourceVoice || c.source == SourceSticky {
					return Speaker{ID: "p_anna", Name: "Anna", Source: c.source}
				}
				return Speaker{Source: c.source}
			}
			for _, a := range []Access{AccessOpen, AccessRead, AccessWrite} {
				body := postChat(t, newMux(), chatBody(testClient, "stub"+a.String())).Body.String()
				ran := *runs[a] == 1
				if ran != c.allowed[a] {
					t.Errorf("%s ran=%v want %v: %s", a, ran, c.allowed[a], body)
				}
				*runs[a] = 0
				if !c.allowed[a] && !strings.Contains(joinDeltas(parseSSE(t, body)), c.refusal) {
					t.Errorf("%s: refusal %q missing: %s", a, c.refusal, body)
				}
				if !c.allowed[a] && hasEvent(rawEvents(t, body), "tool") != nil {
					t.Errorf("%s: a refusal must not flash a tool action", a)
				}
			}
		})
	}
}

// Short voice while voice ID expected and people exist: uncertain, asked who
// is speaking, and nothing personal is read out.
func TestShortVoiceIsUncertainAndAskedWho(t *testing.T) {
	chatEnv(t)
	useTempData(t)
	t.Setenv("OLLAMA_BASE", "http://127.0.0.1:1")
	withTestRegistry(t)
	runs := stubAccessTools(t)
	peopleCountProvider = func() int { return 1 } // no resolver: built-in rules
	b, _ := json.Marshal(map[string]any{
		"clientId":      testClient,
		"messages":      []map[string]string{{"role": "user", "content": "stubread Was weißt du über mich?"}},
		"voiceExpected": true,
		"voice":         map[string]any{"short": true, "sec": 0.6},
		"speaker":       map[string]any{"id": "p_anna", "source": "sticky"},
	})
	body := postChat(t, newMux(), string(b)).Body.String()
	evs := rawEvents(t, body)
	if sp := hasEvent(evs, "speaker"); sp == nil || sp["source"] != SourceUncertain {
		t.Errorf("speaker = %v", sp)
	}
	if p := hasEvent(evs, "pending"); p == nil || p["kind"] != "slot" {
		t.Errorf("pending = %v", p)
	}
	if *runs[AccessRead] != 0 || !strings.Contains(joinDeltas(parseSSE(t, body)), "Who is speaking?") {
		t.Errorf("uncertain read: runs %d body %s", *runs[AccessRead], body)
	}
}

// setTestPIN stores a PIN with a cheap hash.
func setTestPIN(t *testing.T, pin string) {
	t.Helper()
	old := pinHashIter
	pinHashIter = 10
	t.Cleanup(func() { pinHashIter = old; clearUnlockSessions() })
	h, err := hashLockPIN(pin)
	if err != nil {
		t.Fatal(err)
	}
	if err := updateSecrets(func(s *secretsFile) error { s.LockPINHash = h; return nil }); err != nil {
		t.Fatal(err)
	}
}

func doReq(h http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.10:5555"
	r.Host = "localhost:8080"
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func unlockCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == unlockCookieName {
			return c
		}
	}
	return nil
}

func TestUnlockCookieGatesPersonalRoutes(t *testing.T) {
	useTempData(t)
	useTempSettings(t)
	withTestRegistry(t)
	registerRoute("GET /api/stubmemories", requireUnlock(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	h := newMux()

	// Without a PIN the lock cannot be switched on.
	s := loadSettings()
	s.PersonalLock = true
	saveSettings(s)
	if rec := doReq(h, http.MethodPost, "/api/unlock", `{"pin":"1234"}`); rec.Code != http.StatusConflict {
		t.Errorf("unlock without PIN: %d", rec.Code)
	}

	setTestPIN(t, "1234")
	if rec := doReq(h, http.MethodGet, "/api/stubmemories", ""); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"locked"`) {
		t.Fatalf("locked route: %d %s", rec.Code, rec.Body)
	}
	if rec := doReq(h, http.MethodPost, "/api/unlock", `{"pin":"9999"}`); rec.Code != http.StatusForbidden || unlockCookie(rec) != nil {
		t.Errorf("wrong PIN: %d", rec.Code)
	}
	rec := doReq(h, http.MethodPost, "/api/unlock", `{"pin":"1234"}`)
	c := unlockCookie(rec)
	if rec.Code != http.StatusOK || c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || len(c.Value) != 64 {
		t.Fatalf("unlock: %d %+v", rec.Code, c)
	}
	if rec := doReq(h, http.MethodGet, "/api/stubmemories", "", c); rec.Code != http.StatusOK {
		t.Errorf("unlocked route: %d", rec.Code)
	}
	if rec := doReq(h, http.MethodGet, "/api/unlock", "", c); !strings.Contains(rec.Body.String(), `"unlocked":true`) {
		t.Errorf("status: %s", rec.Body)
	}
	// A forged cookie is not a session.
	if rec := doReq(h, http.MethodGet, "/api/stubmemories", "", &http.Cookie{Name: unlockCookieName, Value: strings.Repeat("a", 64)}); rec.Code != http.StatusForbidden {
		t.Errorf("forged cookie: %d", rec.Code)
	}
	// DELETE locks again.
	doReq(h, http.MethodDelete, "/api/unlock", "", c)
	if rec := doReq(h, http.MethodGet, "/api/stubmemories", "", c); rec.Code != http.StatusForbidden {
		t.Errorf("after lock: %d", rec.Code)
	}
}

func TestUnlockLockoutAfterFiveWrongPINs(t *testing.T) {
	useTempData(t)
	useTempSettings(t)
	setTestPIN(t, "1234")
	unlockSessions.Lock()
	clear(unlockSessions.fails)
	unlockSessions.Unlock()
	t.Cleanup(func() {
		unlockSessions.Lock()
		clear(unlockSessions.fails)
		unlockSessions.Unlock()
	})
	h := newMux()
	for i := 0; i < unlockMaxFails; i++ {
		if rec := doReq(h, http.MethodPost, "/api/unlock", `{"pin":"0000"}`); rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	rec := doReq(h, http.MethodPost, "/api/unlock", `{"pin":"1234"}`)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || unlockCookie(rec) != nil {
		t.Errorf("right PIN during lockout: %d", rec.Code)
	}
	// Another address is not locked out.
	r := httptest.NewRequest(http.MethodPost, "/api/unlock", strings.NewReader(`{"pin":"1234"}`))
	r.RemoteAddr, r.Host = "192.0.2.99:1", "localhost:8080"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("other address: %d", w.Code)
	}
}

// Manual speaker choice only counts after a PIN unlock when the lock is on.
func TestManualSpeakerNeedsUnlockWithLock(t *testing.T) {
	useTempData(t)
	useTempSettings(t)
	setTestPIN(t, "1234")
	s := loadSettings()
	s.PersonalLock = true
	saveSettings(s)
	r := httptest.NewRequest(http.MethodPost, "/api/chat", nil)
	tr := &Turn{Settings: loadSettings(), Unlocked: unlockedRequest(r), Speaker: Speaker{ID: "p_anna", Source: SourceManual}}
	if tr.Can(AccessRead) {
		t.Error("manual without cookie may read with the lock on")
	}
}

func TestPersonalLockSettingNeedsAdminAndUnlock(t *testing.T) {
	useTempData(t)
	useTempSettings(t)
	tokenEnv(t, "secret-token")
	h := newMux()
	post := func(remote, host string, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(body))
		r.RemoteAddr, r.Host = remote, host
		r.Header.Set("Authorization", "Bearer secret-token")
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	setTestPIN(t, "1234")
	// A LAN client with the master token is not an admin request.
	if rec := post("192.0.2.5:1", "192.168.1.10:8080", `{"personalLock":true}`); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "local_only") {
		t.Errorf("LAN lock change: %d %s", rec.Code, rec.Body)
	}
	// Local but not unlocked.
	if rec := post("127.0.0.1:1", "localhost:8080", `{"personalLock":true}`); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "locked") {
		t.Errorf("not unlocked: %d %s", rec.Code, rec.Body)
	}
	unlock := httptest.NewRequest(http.MethodPost, "/api/unlock", strings.NewReader(`{"pin":"1234"}`))
	unlock.RemoteAddr, unlock.Host = "127.0.0.1:1", "localhost:8080"
	uw := httptest.NewRecorder()
	h.ServeHTTP(uw, unlock)
	c := unlockCookie(uw)
	if c == nil {
		t.Fatalf("unlock failed: %d %s", uw.Code, uw.Body)
	}
	if rec := post("127.0.0.1:1", "localhost:8080", `{"personalLock":true}`, c); rec.Code != http.StatusOK || !loadSettings().PersonalLock {
		t.Errorf("admin + unlocked: %d %s", rec.Code, rec.Body)
	}
	// Other settings still save from the LAN while the lock stays as is.
	if rec := post("192.0.2.5:1", "192.168.1.10:8080", `{"homeCity":"Hamburg"}`); rec.Code != http.StatusOK || loadSettings().HomeCity != "Hamburg" || !loadSettings().PersonalLock {
		t.Errorf("unrelated save: %d %s", rec.Code, rec.Body)
	}
}

// Undo is keyed by device and speaker id, and a sticky or (lock on) claimed
// speaker carries the person's id without their Write rights. Undoing a
// Write action is itself a write, and its label may quote personal text, so
// it needs the class of the action it reverts.
func TestUndoNeedsTheAccessOfTheUndoneAction(t *testing.T) {
	chatEnv(t)
	useTempData(t)
	t.Setenv("OLLAMA_BASE", "http://127.0.0.1:1")
	withTestRegistry(t)
	reverted := map[Access]int{}
	for _, a := range []Access{AccessOpen, AccessWrite} {
		a := a
		name := "stubundo_" + a.String()
		registerTool(Tool{Name: name, Access: func(*Turn, json.RawMessage) Access { return a },
			Run: func(t *Turn, _ json.RawMessage) Reply {
				t.PushUndo("secret "+a.String(), func(context.Context) error { reverted[a]++; return nil })
				return Reply{Text: "done " + a.String()}
			}})
		registerFastPath(FastPath{Name: name, Order: 40, Tool: name, Match: func(t *Turn) (json.RawMessage, bool) {
			return nil, hasPhrase(t.Tokens, "do"+a.String())
		}})
	}
	peopleCountProvider = func() int { return 1 }
	source := SourceVoice
	speakerResolver = func(*Turn, *VoiceInput, string, string) Speaker {
		return Speaker{ID: "p_anna", Name: "Anna", Source: source}
	}
	h := newMux()

	// Anna (by voice) writes; then a sticky "Anna" says "undo".
	postChat(t, h, chatBody(testClient, "dowrite"))
	source = SourceSticky
	body := postChat(t, h, chatBody(testClient, "undo")).Body.String()
	if reverted[AccessWrite] != 0 {
		t.Fatalf("a sticky speaker undid a Write action: %s", body)
	}
	if strings.Contains(body, "secret") {
		t.Errorf("the refusal must not read out the undo label: %s", body)
	}
	if !strings.Contains(joinDeltas(parseSSE(t, body)), "not sure who is speaking") {
		t.Errorf("want the sticky refusal: %s", body)
	}
	// The voice-recognised Anna still can.
	source = SourceVoice
	postChat(t, h, chatBody(testClient, "undo"))
	if reverted[AccessWrite] != 1 {
		t.Errorf("Anna by voice must undo her own write, reverted=%v", reverted)
	}
	// An open action stays undoable for the sticky speaker.
	postChat(t, h, chatBody(testClient, "doopen"))
	source = SourceSticky
	postChat(t, h, chatBody(testClient, "undo"))
	if reverted[AccessOpen] != 1 {
		t.Errorf("sticky must still undo an open action, reverted=%v", reverted)
	}
}

// A tool run that bypassed the central check (a feature's own pending
// resume) records an undo that needs Write: fail closed.
func TestUndoWithoutCentralCheckNeedsWrite(t *testing.T) {
	withTestRegistry(t)
	tr := &Turn{ClientID: testClient, Speaker: Speaker{ID: "p_anna", Source: SourceVoice}}
	tr.PushUndo("x", func(context.Context) error { return nil })
	if a := undoAccess(testClient, "p_anna"); a != AccessWrite {
		t.Errorf("unchecked undo needs %s, want write", a)
	}
	if a := undoAccess(testClient, "p_ben"); a != AccessOpen {
		t.Errorf("nothing to undo must be open, got %s", a)
	}
}
