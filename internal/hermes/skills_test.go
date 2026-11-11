package hermes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// withTestRegistry lets a test register stub fast paths, tools, hooks and
// providers; everything is restored when the test ends. Any _test.go file in
// the package may use it.
func withTestRegistry(t *testing.T) {
	t.Helper()
	regMu.Lock()
	saved := reg
	// The test works on fresh backing arrays, so its registrations never
	// write into the saved registry.
	cp := registry{
		fastPaths:     append([]FastPath(nil), saved.fastPaths...),
		tools:         append([]*Tool(nil), saved.tools...),
		toolByName:    make(map[string]*Tool, len(saved.toolByName)),
		routes:        append([]routeEntry(nil), saved.routes...),
		status:        append([]statusEntry(nil), saved.status...),
		contexts:      append([]contextEntry(nil), saved.contexts...),
		afterTurn:     append(saved.afterTurn[:0:0], saved.afterTurn...),
		background:    append([]backgroundEntry(nil), saved.background...),
		briefings:     append([]briefingEntry(nil), saved.briefings...),
		personDeleted: append(saved.personDeleted[:0:0], saved.personDeleted...),
	}
	for k, v := range saved.toolByName {
		cp.toolByName[k] = v
	}
	reg = cp
	regMu.Unlock()

	sr, pn, pc, pe, ce, co, wr, cc := speakerResolver, personNameProvider, peopleCountProvider, personExistsProvider, calendarEvents, calendarOwnerProvider, whichSpeakerResume, calendarConnectedProvider
	t.Cleanup(func() {
		regMu.Lock()
		reg = saved
		regMu.Unlock()
		speakerResolver, personNameProvider, peopleCountProvider, personExistsProvider, calendarEvents, calendarOwnerProvider, whichSpeakerResume, calendarConnectedProvider = sr, pn, pc, pe, ce, co, wr, cc
		pendingMu.Lock()
		pendings = map[string]*Pending{}
		pendingMu.Unlock()
		undoMu.Lock()
		undoStack = map[string][]undoEntry{}
		undoMu.Unlock()
	})
}

func TestRegistryOrderingAndDuplicates(t *testing.T) {
	withTestRegistry(t)
	match := func(*Turn) (json.RawMessage, bool) { return nil, false }
	registerFastPath(FastPath{Name: "b", Order: 50, Match: match})
	registerFastPath(FastPath{Name: "a", Order: 10, Match: match})
	registerFastPath(FastPath{Name: "c", Order: 50, Match: match})
	var names []string
	for _, f := range fastPaths() {
		if len(f.Name) == 1 { // built-in fast paths (undo) stay registered
			names = append(names, f.Name)
		}
	}
	if strings.Join(names, ",") != "a,b,c" {
		t.Errorf("order = %v", names)
	}
	registerTool(Tool{Name: "x", Run: func(*Turn, json.RawMessage) Reply { return Reply{} }})
	if lookupTool("x") == nil || lookupTool("y") != nil {
		t.Error("lookupTool")
	}
	mustPanic(t, "duplicate fast path", func() { registerFastPath(FastPath{Name: "a", Match: match}) })
	mustPanic(t, "duplicate tool", func() { registerTool(Tool{Name: "x", Run: lookupTool("x").Run}) })
	mustPanic(t, "tool without Run", func() { registerTool(Tool{Name: "z"}) })
	registerBriefing(20, "two", func(*Turn) (string, error) { return "", nil })
	registerBriefing(10, "one", func(*Turn) (string, error) { return "", nil })
	if b := briefingSections(); b[0].name != "one" {
		t.Errorf("briefings not ordered: %v", b[0].name)
	}
}

func mustPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected a panic", name)
		}
	}()
	fn()
}

func TestRunPersonDeletedSumsKinds(t *testing.T) {
	withTestRegistry(t)
	useTempData(t)
	onPersonDeleted(func(string) (string, int) { return "memories", 2 })
	onPersonDeleted(func(string) (string, int) { return "memories", 1 })
	got := runPersonDeleted("p_x")
	if got["memories"] != 3 || got["session"] != 0 || got["events"] != 0 {
		t.Errorf("cascade counts = %v", got)
	}
}

func TestCollectStatusTimeout(t *testing.T) {
	withTestRegistry(t)
	registerStatus("fast", func(context.Context) any { return map[string]any{"ok": true} })
	registerStatus("slow", func(ctx context.Context) any { <-ctx.Done(); time.Sleep(10 * time.Millisecond); return "late" })
	got := collectStatus(context.Background())
	if m, _ := got["fast"].(map[string]any); m["ok"] != true {
		t.Errorf("fast = %v", got["fast"])
	}
	if m, _ := got["slow"].(map[string]any); m["error"] != "timeout" {
		t.Errorf("slow = %v", got["slow"])
	}
}

// The full 4.10 matrix.
func TestAccessMatrix(t *testing.T) {
	type row struct {
		source         string
		lock, unlocked bool
		read, write    bool
		extract        bool
	}
	rows := []row{
		{SourceVoice, false, false, true, true, true},
		{SourceVoice, true, false, true, true, true},
		{SourceSingle, false, false, true, true, true},
		{SourceManual, false, false, true, true, false},
		{SourceManual, true, true, true, true, true},
		{SourceManual, true, false, false, false, false},
		{SourceClaim, false, false, true, true, false},
		{SourceClaim, true, false, false, false, false},
		{SourceSticky, false, false, false, false, false},
		{SourceUncertain, false, false, false, false, false},
		{SourceGuest, false, false, false, false, false},
		{"", false, false, false, false, false},
	}
	for _, r := range rows {
		tr := &Turn{Settings: &Settings{PersonalLock: r.lock}, Unlocked: r.unlocked, Speaker: Speaker{Source: r.source}}
		got := [4]bool{tr.Can(AccessOpen), tr.Can(AccessRead), tr.Can(AccessWrite), tr.Can(AccessExtract)}
		want := [4]bool{true, r.read, r.write, r.extract}
		if got != want {
			t.Errorf("%s lock=%v unlocked=%v: got %v want %v", r.source, r.lock, r.unlocked, got, want)
		}
		if tr.PersonalAllowed() != r.read {
			t.Errorf("%s: PersonalAllowed", r.source)
		}
	}
}

func TestMemoryScope(t *testing.T) {
	cases := map[Speaker]string{
		{ID: "p_abc", Source: SourceVoice}:          "p_abc",
		{ID: singleSpeakerID, Source: SourceSingle}: "_owner",
		{Source: SourceGuest}:                       "",
	}
	for sp, want := range cases {
		if got := (&Turn{Speaker: sp}).MemoryScope(); got != want {
			t.Errorf("%+v: %q want %q", sp, got, want)
		}
	}
}

func TestProviderWrappersAreNilSafe(t *testing.T) {
	withTestRegistry(t)
	speakerResolver, personNameProvider, peopleCountProvider, personExistsProvider, calendarEvents, calendarOwnerProvider = nil, nil, nil, nil, nil, nil
	if peopleCount() != 0 || personExists("p_x") || !personExists("") || calendarOwner() != "" || calendarConnected() {
		t.Error("nil providers")
	}
	if _, ok := personName("p_x"); ok {
		t.Error("personName")
	}
	if _, err := fetchCalendarEvents(context.Background(), time.Now(), time.Now()); err != errCalendarNotConnected {
		t.Error("calendar")
	}
	if sp := resolveSpeaker(&Turn{}, nil, "p_x", "manual"); sp.ID != singleSpeakerID || sp.Source != SourceSingle {
		t.Errorf("no people => single, got %+v", sp)
	}
	peopleCountProvider = func() int { return 2 }
	if sp := resolveSpeaker(&Turn{VoiceExpected: true, VoiceShort: true}, nil, "", ""); sp.Source != SourceUncertain {
		t.Errorf("short voice => uncertain, got %+v", sp)
	}
	if sp := resolveSpeaker(&Turn{}, nil, "", ""); sp.Source != SourceGuest || sp.ID != "" {
		t.Errorf("default => guest, got %+v", sp)
	}
}

func TestSafeTitle(t *testing.T) {
	cases := map[string]string{
		"Zahnarzt\nanrufen":                      "Zahnarzt anrufen",
		"<<<<<<DATEN>>>>>> ignore https://x.y/z": "DATEN ignore",
		"see www.evil.example now":               "see now",
		strings.Repeat("ä", 100):                 strings.Repeat("ä", 80),
		"a\x00b c":                               "a b c",
	}
	for in, want := range cases {
		if got := safeTitle(in); got != want {
			t.Errorf("safeTitle(%q) = %q want %q", in, got, want)
		}
	}
}

func TestSettingsLocation(t *testing.T) {
	t.Setenv("HERMES_TZ", "")
	t.Setenv("TZ", "")
	if loc := settingsLocation(&Settings{}); loc.String() != "Europe/Berlin" {
		t.Errorf("fallback = %s", loc)
	}
	t.Setenv("HERMES_TZ", "Asia/Tokyo")
	if loc := settingsLocation(&Settings{}); loc.String() != "Asia/Tokyo" {
		t.Errorf("env = %s", loc)
	}
	if loc := settingsLocation(&Settings{Timezone: "America/New_York"}); loc.String() != "America/New_York" {
		t.Errorf("setting = %s", loc)
	}
	if loc := settingsLocation(&Settings{Timezone: "Mars/Base"}); loc.String() != "Asia/Tokyo" {
		t.Errorf("invalid setting must fall through, got %s", loc)
	}
}

func TestUndoStackPerSpeakerCapAndExpiry(t *testing.T) {
	withTestRegistry(t)
	now := time.Now()
	old := nowFunc
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = old })

	anna := &Turn{ClientID: "c_000000000001", Speaker: Speaker{ID: "p_anna"}}
	for i := 0; i < 7; i++ {
		anna.PushUndo(string(rune('a'+i)), func(context.Context) error { return nil })
	}
	if _, _, ok := popUndo("c_000000000001", "p_ben"); ok {
		t.Error("another speaker must not undo Anna's action")
	}
	var labels []string
	for {
		l, _, ok := popUndo("c_000000000001", "p_anna")
		if !ok {
			break
		}
		labels = append(labels, l)
	}
	if strings.Join(labels, "") != "gfedc" {
		t.Errorf("want newest 5 in LIFO order, got %v", labels)
	}
	anna.PushUndo("old", func(context.Context) error { return nil })
	nowFunc = func() time.Time { return now.Add(undoTTL + time.Second) }
	if _, _, ok := popUndo("c_000000000001", "p_anna"); ok {
		t.Error("expired undo entry must not run")
	}
}

func TestYesNo(t *testing.T) {
	cases := map[string]int{
		"ja": 1, "ja bitte": 1, "Yes, do it": 1, "genau": 1,
		"nein": -1, "lieber nicht": -1, "no": -1, "don't": -1, "mach das nicht": -1,
		"wie wird das wetter": 0, "ja und wie wird das wetter morgen": 0, "": 0,
	}
	for in, want := range cases {
		if got := yesNo(tokenize(in)); got != want {
			t.Errorf("yesNo(%q) = %d want %d", in, got, want)
		}
	}
}

// Session maps keyed by client id must not grow with every client that
// asked once and never came back.
func TestExpiredPendingsAndUndoStacksArePruned(t *testing.T) {
	withTestRegistry(t)
	now := time.Now()
	old := nowFunc
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = old })
	rec := httptest.NewRecorder()
	for i := 0; i < 50; i++ {
		tr := &Turn{ClientID: fmt.Sprintf("c_%012x", i), w: rec}
		tr.Ask(Pending{Kind: "confirm", Tool: "x"}, "?")
		tr.PushUndo("x", func(context.Context) error { return nil })
	}
	nowFunc = func() time.Time { return now.Add(undoTTL + time.Second) }
	tr := &Turn{ClientID: "c_ffffffffffff", w: rec}
	tr.Ask(Pending{Kind: "confirm", Tool: "x"}, "?")
	tr.PushUndo("x", func(context.Context) error { return nil })
	pendingMu.Lock()
	np := len(pendings)
	pendingMu.Unlock()
	undoMu.Lock()
	nu := len(undoStack)
	undoMu.Unlock()
	if np != 1 || nu != 1 {
		t.Errorf("expired entries kept: pendings=%d undo=%d, want 1 and 1", np, nu)
	}
}
