package hermes

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers

// peopleEnv isolates a people test: temp data and settings, restored
// registries and providers, no claims, and a controllable clock.
func peopleEnv(t *testing.T) *time.Time {
	t.Helper()
	withTestRegistry(t)
	useTempData(t)
	useTempSettings(t)
	t.Setenv("OLLAMA_BASE", "http://127.0.0.1:1") // no persona chat in these tests
	mc, os2, mo, rr := memoryCountProvider, ownerMemoryStatsProvider, migrateOwnerMemoriesProvider, reassignRecentMemoriesProvider
	memoryCountProvider, ownerMemoryStatsProvider, migrateOwnerMemoriesProvider, reassignRecentMemoriesProvider = nil, nil, nil, nil
	clock := time.Date(2026, 11, 6, 14, 30, 0, 0, time.UTC)
	oldNow := nowFunc
	nowFunc = func() time.Time { return clock }
	claimsMu.Lock()
	claims = map[string]speakerClaimState{}
	claimsMu.Unlock()
	t.Cleanup(func() {
		nowFunc = oldNow
		memoryCountProvider, ownerMemoryStatsProvider, migrateOwnerMemoriesProvider, reassignRecentMemoriesProvider = mc, os2, mo, rr
		claimsMu.Lock()
		claims = map[string]speakerClaimState{}
		claimsMu.Unlock()
	})
	return &clock
}

// vec builds a unit vector from axis weights; axes are orthonormal, so the
// cosine of vec({0: a, 9: b}) with axis 0 is a/√(a²+b²).
func vec(w map[int]float64) []float32 {
	v := make([]float32, voiceDim)
	for i, x := range w {
		v[i] = float32(x)
	}
	return l2normalize(v)
}

var noiseAxis atomic.Int64

// near returns a unit vector with cosine c to axis i. The rest goes to a
// fresh "noise" axis (100–191) on every call, so two voices from near() are
// as different as their cosines to i allow, even after adaptive samples
// moved a centroid.
func near(i int, c float64) []float32 {
	axis := 100 + int(noiseAxis.Add(1)%92)
	return vec(map[int]float64{i: c, axis: math.Sqrt(1 - c*c)})
}

func voiceIn(emb []float32, sec float64) *VoiceInput {
	return &VoiceInput{Model: voiceModelID, ORT: voiceORT, Emb: emb, Sec: sec}
}

func encEmb(v []float32) string {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return base64.StdEncoding.EncodeToString(b)
}

// addPerson stores a person directly; kinds lists one sample per entry, all
// with embedding emb.
func addPerson(t *testing.T, name string, confirmed bool, emb []float32, kinds ...string) string {
	t.Helper()
	now := nowFunc()
	p := &Person{ID: newID("p"), Name: name, CreatedAt: now, Origin: "ui", Confirmed: confirmed, Provisional: true}
	if !confirmed {
		p.Origin = "voice"
		exp := now.Add(unconfirmedTTL)
		p.ExpiresAt = &exp
	}
	if len(kinds) > 0 {
		p.Voice = &Voiceprint{Model: voiceModelID, ORT: voiceORT, Dim: voiceDim, UpdatedAt: now}
		for _, k := range kinds {
			p.Voice.Samples = append(p.Voice.Samples, VoiceSample{ID: newID("vp"), Emb: emb, Sec: 3, Kind: k, At: now.Add(-time.Hour)})
		}
		recomputeCentroid(p.Voice)
		p.Provisional = countKind(p.Voice, sampleAnchor) < maxAnchors
	}
	if err := peopleStore.Update(func(st *peopleFile) error {
		st.People = append(st.People, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func getPerson(t *testing.T, id string) *Person {
	t.Helper()
	var out *Person
	_ = peopleStore.View(func(st *peopleFile) error {
		for _, p := range st.People {
			if p.ID == id {
				cp := *p
				if p.Voice != nil {
					v := *p.Voice
					v.Samples = append([]VoiceSample(nil), p.Voice.Samples...)
					cp.Voice = &v
				}
				out = &cp
			}
		}
		return nil
	})
	return out
}

func sampleCount(t *testing.T, id string) int {
	t.Helper()
	p := getPerson(t, id)
	if p == nil || p.Voice == nil {
		return 0
	}
	return len(p.Voice.Samples)
}

type turnOpt struct {
	typed    bool // default spoken
	short    bool
	expected bool
	claimID  string
	claimSrc string
	unlocked bool
	lang     string
	settings func(*Settings)
}

func newPeopleTurn(query string, v *VoiceInput, o turnOpt) (*Turn, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	s := loadSettings()
	if o.settings != nil {
		o.settings(s)
	}
	lang := o.lang
	if lang == "" {
		lang = "de"
	}
	tr := &Turn{w: rec, Ctx: context.Background(), Settings: s, Lang: lang, Loc: time.UTC, Now: nowFunc(),
		Query: query, Tokens: tokenize(query), ClientID: testClient, Spoken: !o.typed, Voice: v,
		VoiceShort: o.short, VoiceExpected: o.expected || o.short || v != nil, Unlocked: o.unlocked}
	if o.short {
		tr.VoiceSec = 0.6
	}
	tr.Speaker = resolveSpeaker(tr, v, o.claimID, o.claimSrc)
	return tr, rec
}

// say runs one utterance through the pending answer and the fast paths.
func say(t *testing.T, query string, v *VoiceInput, o turnOpt) (handled bool, text string, tr *Turn, events []map[string]any) {
	t.Helper()
	tr, rec := newPeopleTurn(query, v, o)
	handled = runFastPaths(tr)
	body := rec.Body.String()
	return handled, joinDeltas(parseSSE(t, body)), tr, rawEvents(t, body)
}

func peopleCountStored(t *testing.T) int {
	t.Helper()
	return len(listPeople())
}

// ---------------------------------------------------------------------------
// Matching (4.6)

func TestMatchSpeakerThresholdMarginSingleAndModel(t *testing.T) {
	peopleEnv(t)
	anna := &Person{ID: "p_anna", Name: "Anna", Voice: &Voiceprint{Model: voiceModelID, Dim: voiceDim, Centroid: vec(map[int]float64{0: 1})}}
	ben := &Person{ID: "p_ben", Name: "Ben", Voice: &Voiceprint{Model: voiceModelID, Dim: voiceDim, Centroid: vec(map[int]float64{1: 1})}}
	old := &Person{ID: "p_old", Name: "Old", Voice: &Voiceprint{Model: "ecapa@old", Dim: voiceDim, Centroid: vec(map[int]float64{2: 1})}}
	all := []*Person{anna, ben, old}

	best, score, second := matchSpeaker(voiceIn(near(0, 0.7), 2), all, 0.6, 0.05)
	if best != anna || math.Abs(score-0.7) > 1e-4 || math.Abs(second) > 1e-4 {
		t.Errorf("clear match: best=%v score=%.3f second=%.3f", best, score, second)
	}
	if best, score, _ = matchSpeaker(voiceIn(near(0, 0.55), 2), all, 0.6, 0.05); best != nil || math.Abs(score-0.55) > 1e-4 {
		t.Errorf("below threshold must reject: %v %.3f", best, score)
	}
	// 0.66 vs 0.63: above the threshold but inside the margin.
	both := vec(map[int]float64{0: 0.66, 1: 0.63, 99: math.Sqrt(1 - 0.66*0.66 - 0.63*0.63)})
	if best, score, second = matchSpeaker(voiceIn(both, 2), all, 0.6, 0.05); best != nil || score-second > 0.05 {
		t.Errorf("margin must reject: %v %.3f %.3f", best, score, second)
	}
	if best, _, second = matchSpeaker(voiceIn(both, 2), []*Person{anna}, 0.6, 0.05); best != anna || second != 0 {
		t.Errorf("single candidate: second must be 0, got %v %.3f", best, second)
	}
	// A print of another model is never a candidate, however close.
	if best, score, _ = matchSpeaker(voiceIn(near(2, 0.95), 2), all, 0.6, 0.05); best != nil || score > 0.01 {
		t.Errorf("model mismatch must be ignored: %v %.3f", best, score)
	}
	if best, score, second = matchSpeaker(voiceIn(near(0, 0.9), 2), []*Person{old}, 0.6, 0.05); best != nil || score != 0 || second != 0 {
		t.Error("no candidates must give nil, 0, 0")
	}
}

func TestDecodeVoiceEmbValidation(t *testing.T) {
	good := vec(map[int]float64{0: 1, 1: 1})
	if v, _, err := decodeVoiceEmb(voiceModelID, encEmb(good)); err != nil || math.Abs(cosine(v, good)-1) > 1e-6 {
		t.Fatalf("good emb: %v", err)
	}
	if _, code, _ := decodeVoiceEmb("other@1", encEmb(good)); code != "model" {
		t.Errorf("model mismatch code = %q", code)
	}
	if _, code, _ := decodeVoiceEmb(voiceModelID, encEmb(good[:100])); code != "bad_voice" {
		t.Error("short emb accepted")
	}
	nan := append([]float32(nil), good...)
	nan[3] = float32(math.NaN())
	if _, _, err := decodeVoiceEmb(voiceModelID, encEmb(nan)); err == nil {
		t.Error("NaN accepted")
	}
	big := make([]float32, voiceDim)
	big[0] = 3
	if _, _, err := decodeVoiceEmb(voiceModelID, encEmb(big)); err == nil {
		t.Error("norm 3 accepted")
	}
	if _, _, err := decodeVoiceEmb(voiceModelID, "!!"); err == nil {
		t.Error("bad base64 accepted")
	}
}

func TestAdaptiveUpdateRulesAndCaps(t *testing.T) {
	clock := peopleEnv(t)
	anna := addPerson(t, "Anna", true, vec(map[int]float64{0: 1}), sampleAnchor, sampleAnchor, sampleAnchor)

	// Strong (≥ th+0.10), clear (margin ≥ 0.15), long (≥ 2 s): one adaptive sample.
	tr, _ := newPeopleTurn("hallo", voiceIn(near(0, 0.8), 2.5), turnOpt{})
	if tr.Speaker.ID != anna || tr.Speaker.Source != SourceVoice {
		t.Fatalf("speaker = %+v", tr.Speaker)
	}
	if n := sampleCount(t, anna); n != 4 {
		t.Fatalf("samples after a strong match = %d, want 4", n)
	}
	if p := getPerson(t, anna); !p.LastSeen.Equal(*clock) {
		t.Errorf("LastSeen = %v", p.LastSeen)
	}
	// Within 10 minutes: no second sample.
	*clock = clock.Add(5 * time.Minute)
	newPeopleTurn("hallo", voiceIn(near(0, 0.8), 2.5), turnOpt{})
	if n := sampleCount(t, anna); n != 4 {
		t.Errorf("adapted again within 10 min: %d", n)
	}
	// Accepted but not strong enough, or too short: no sample.
	*clock = clock.Add(11 * time.Minute)
	newPeopleTurn("hallo", voiceIn(near(0, 0.65), 2.5), turnOpt{})
	newPeopleTurn("hallo", voiceIn(near(0, 0.9), 1.5), turnOpt{})
	if n := sampleCount(t, anna); n != 4 {
		t.Errorf("weak or short match adapted: %d", n)
	}
	// Adapt switched off: nothing.
	newPeopleTurn("hallo", voiceIn(near(0, 0.9), 3), turnOpt{settings: func(s *Settings) { s.VoiceIDAdapt = false }})
	if n := sampleCount(t, anna); n != 4 {
		t.Errorf("adapted with VoiceIDAdapt off: %d", n)
	}
	// FIFO cap: at most 7 adaptive samples, all anchors kept.
	for i := 0; i < 12; i++ {
		*clock = clock.Add(11 * time.Minute)
		newPeopleTurn("hallo", voiceIn(near(0, 0.85), 3), turnOpt{})
	}
	p := getPerson(t, anna)
	if a, ad := countKind(p.Voice, sampleAnchor), countKind(p.Voice, sampleAdaptive); a != 3 || ad != maxAdaptive {
		t.Errorf("anchors=%d adaptive=%d", a, ad)
	}
	if len(p.Voice.Centroid) != voiceDim {
		t.Error("centroid not recomputed")
	}

	// A provisional voice person never gets promoted by adaptive samples.
	clara := addPerson(t, "Clara", false, vec(map[int]float64{5: 1}), sampleVoiceAnchor)
	for i := 0; i < 4; i++ {
		*clock = clock.Add(11 * time.Minute)
		newPeopleTurn("hallo", voiceIn(near(5, 0.9), 3), turnOpt{})
	}
	if c := getPerson(t, clara); !c.Provisional || c.Confirmed || countKind(c.Voice, sampleAdaptive) != 4 {
		t.Errorf("clara: provisional=%v confirmed=%v adaptive=%d", c.Provisional, c.Confirmed, countKind(c.Voice, sampleAdaptive))
	}
}

// ---------------------------------------------------------------------------
// Speaker resolution (3.1)

func TestResolveSpeakerRules(t *testing.T) {
	clock := peopleEnv(t)
	if tr, _ := newPeopleTurn("hi", nil, turnOpt{}); tr.Speaker.ID != singleSpeakerID || tr.Speaker.Source != SourceSingle {
		t.Fatalf("no people: %+v", tr.Speaker)
	}
	anna := addPerson(t, "Anna", true, vec(map[int]float64{0: 1}), sampleAnchor, sampleAnchor, sampleAnchor)
	ben := addPerson(t, "Ben", true, nil)

	check := func(name string, v *VoiceInput, o turnOpt, id, source string) {
		t.Helper()
		tr, _ := newPeopleTurn("hi", v, o)
		if tr.Speaker.ID != id || tr.Speaker.Source != source {
			t.Errorf("%s: got %+v, want %s/%s", name, tr.Speaker, id, source)
		}
	}
	check("valid voice", voiceIn(near(0, 0.75), 2), turnOpt{}, anna, SourceVoice)
	check("rejected voice beats sticky", voiceIn(near(0, 0.3), 2), turnOpt{claimID: anna, claimSrc: SourceSticky}, "", SourceGuest)
	check("rejected voice beats manual", voiceIn(near(0, 0.3), 2), turnOpt{claimID: ben, claimSrc: SourceManual}, "", SourceGuest)
	check("unlocked manual beats rejected voice", voiceIn(near(0, 0.3), 2), turnOpt{claimID: ben, claimSrc: SourceManual, unlocked: true}, ben, SourceManual)
	check("manual without voice", nil, turnOpt{claimID: ben, claimSrc: SourceManual}, ben, SourceManual)
	check("manual with lock, not unlocked", nil, turnOpt{claimID: ben, claimSrc: SourceManual, settings: func(s *Settings) { s.PersonalLock = true }}, "", SourceGuest)
	check("unknown manual id", nil, turnOpt{claimID: "p_000000000000", claimSrc: SourceManual}, "", SourceGuest)
	check("sticky on a short voice", nil, turnOpt{short: true, claimID: anna, claimSrc: SourceSticky}, anna, SourceSticky)
	check("sticky without voice ID", nil, turnOpt{claimID: anna, claimSrc: SourceSticky}, anna, SourceSticky)
	check("sticky ignored when a voice was expected but none came", nil, turnOpt{expected: true, claimID: anna, claimSrc: SourceSticky}, "", SourceGuest)
	check("short voice, nothing else", nil, turnOpt{short: true}, "", SourceUncertain)
	check("nothing", nil, turnOpt{}, "", SourceGuest)
	if tr, _ := newPeopleTurn("hi", voiceIn(near(0, 0.3), 2), turnOpt{}); math.Abs(tr.Speaker.Score-0.3) > 0.02 {
		t.Errorf("guest score = %v", tr.Speaker.Score)
	}

	// Claims: win over the chip and sticky, expire after 10 minutes, and are
	// dropped by a voice that says someone else or nobody.
	setClaim(testClient, ben)
	check("claim", nil, turnOpt{claimID: anna, claimSrc: SourceManual}, ben, SourceClaim)
	check("valid voice beats a claim", voiceIn(near(0, 0.8), 2), turnOpt{}, anna, SourceVoice)
	if activeClaim(testClient).person != "" {
		t.Error("a voice accepted as someone else must clear the claim")
	}
	setClaim(testClient, ben)
	check("rejected voice beats a claim", voiceIn(near(0, 0.2), 2), turnOpt{}, "", SourceGuest)
	if activeClaim(testClient).person != "" {
		t.Error("a rejected voice must clear the claim")
	}
	setClaim(testClient, ben)
	*clock = clock.Add(claimTTL + time.Second)
	check("expired claim", nil, turnOpt{}, "", SourceGuest)
}

// One person without an enrolled voice: nothing can tell a visitor apart, so
// that person is the speaker (like single-user mode). A second person, an
// enrolled voice or the personal lock end it.
func TestSoleUnenrolledPersonIsTheSpeaker(t *testing.T) {
	peopleEnv(t)
	solo := addPerson(t, "Szymon", true, nil)
	check := func(name string, o turnOpt, id, source string) {
		t.Helper()
		tr, _ := newPeopleTurn("hi", nil, o)
		if tr.Speaker.ID != id || tr.Speaker.Source != source {
			t.Errorf("%s: got %+v, want %s/%s", name, tr.Speaker, id, source)
		}
		if id != "" && (!tr.Can(AccessRead) || !tr.Can(AccessWrite)) {
			t.Errorf("%s: the sole person must be able to read and write", name)
		}
	}
	check("sole person", turnOpt{}, solo, SourceSingle)
	check("sole person, voice expected", turnOpt{expected: true}, solo, SourceSingle)
	check("personal lock", turnOpt{settings: func(s *Settings) { s.PersonalLock = true }}, "", SourceGuest)
	addPerson(t, "Ben", true, nil)
	check("two people", turnOpt{}, "", SourceGuest)
}

// ---------------------------------------------------------------------------
// Voice enrolment (4.6)

func TestEnrollByVoiceOnFreshInstall(t *testing.T) {
	clock := peopleEnv(t)
	anchor := vec(map[int]float64{0: 1})

	handled, text, tr, evs := say(t, "Hermes, ich bin Anna", voiceIn(anchor, 2.5), turnOpt{})
	if !handled || text != "Soll ich dich als Anna speichern?" {
		t.Fatalf("question: handled=%v text=%q", handled, text)
	}
	if tr.Speaker.Source != SourceSingle {
		t.Errorf("fresh install speaker = %+v", tr.Speaker)
	}
	if p := hasEvent(evs, "pending"); p == nil || p["kind"] != "confirm" {
		t.Errorf("pending event = %v", p)
	}
	if c := hasEvent(evs, "card"); c == nil || c["kind"] != "person" || c["data"].(map[string]any)["pending"] != true {
		t.Errorf("card = %v", c)
	}
	if peopleCountStored(t) != 0 {
		t.Fatal("nobody may be created before the confirmation")
	}

	handled, text, _, evs = say(t, "ja", nil, turnOpt{})
	if !handled || !strings.HasPrefix(text, "Freut mich, Anna. Ich habe ein vorläufiges Stimmprofil angelegt.") {
		t.Fatalf("create: handled=%v text=%q", handled, text)
	}
	people := listPeople()
	if len(people) != 1 {
		t.Fatalf("people = %v", people)
	}
	p := getPerson(t, people[0].ID)
	if p.Origin != "voice" || !p.Provisional || p.Confirmed || p.ExpiresAt == nil || !p.ExpiresAt.Equal(clock.Add(7*24*time.Hour)) {
		t.Errorf("voice person = %+v", p)
	}
	if p.Voice == nil || len(p.Voice.Samples) != 1 || p.Voice.Samples[0].Kind != sampleVoiceAnchor {
		t.Errorf("voice = %+v", p.Voice)
	}
	c := hasEvent(evs, "card")
	if d, _ := c["data"].(map[string]any); d["confirmable"] != true || d["id"] != p.ID {
		t.Errorf("confirm card = %v", c)
	}
	// The file holds a voiceprint: 0600.
	st, err := os.Stat(filepath.Join(dataDir(), "people.json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("people.json mode = %v, %v", st, err)
	}
	// Anna's voice is recognised from now on.
	if tr, _ := newPeopleTurn("hi", voiceIn(near(0, 0.9), 2), turnOpt{}); tr.Speaker.ID != p.ID || tr.Speaker.Source != SourceVoice {
		t.Errorf("after enrolment: %+v", tr.Speaker)
	}
}

func TestEnrollShortVoiceUsesNextUtteranceAsAnchor(t *testing.T) {
	peopleEnv(t)
	if _, text, _, _ := say(t, "ich bin Anna", nil, turnOpt{short: true}); text != "Soll ich dich als Anna speichern?" {
		t.Fatalf("question = %q", text)
	}
	_, text, _, evs := say(t, "ja genau", nil, turnOpt{short: true})
	if !strings.HasPrefix(text, "Ich habe deine Stimme noch nicht lange genug gehört.") {
		t.Fatalf("anchor question = %q", text)
	}
	if p := hasEvent(evs, "pending"); p == nil || p["kind"] != "voice" {
		t.Errorf("pending = %v", p)
	}
	// Still short: one retry.
	if _, text, _, _ = say(t, "ich koche heute", nil, turnOpt{short: true}); !strings.HasPrefix(text, "Das war noch etwas kurz.") {
		t.Fatalf("retry = %q", text)
	}
	// The next long utterance is the anchor, never a command.
	handled, text, _, _ := say(t, "merk dir dass ich heute einen langen Spaziergang mache", voiceIn(vec(map[int]float64{3: 1}), 3.1), turnOpt{})
	if !handled || !strings.HasPrefix(text, "Freut mich, Anna.") {
		t.Fatalf("anchor: %v %q", handled, text)
	}
	if peopleCountStored(t) != 1 {
		t.Error("person not created")
	}
}

func TestEnrollAnchorGivesUpAfterOneRetry(t *testing.T) {
	peopleEnv(t)
	say(t, "ich bin Anna", nil, turnOpt{short: true})
	say(t, "ja", nil, turnOpt{short: true})
	say(t, "hm", nil, turnOpt{short: true})
	_, text, _, _ := say(t, "ja ok", nil, turnOpt{short: true})
	if text != "Nimm deine Stimme lieber unter Einstellungen → Personen auf." {
		t.Errorf("give up = %q", text)
	}
	if peopleCountStored(t) != 0 {
		t.Error("created without an anchor")
	}
	// Typed input never answers the voice question.
	say(t, "ich bin Anna", nil, turnOpt{short: true})
	say(t, "ja", nil, turnOpt{short: true})
	if handled, _, _, _ := say(t, "ein getippter Satz", nil, turnOpt{typed: true}); handled {
		t.Error("typed text consumed as a voice anchor")
	}
}

func TestEnrollNameCorrectionAndNo(t *testing.T) {
	peopleEnv(t)
	v := voiceIn(vec(map[int]float64{0: 1}), 2.5)
	say(t, "ich bin Jorg", v, turnOpt{})
	if _, text, _, _ := say(t, "Nein, ich heiße Jörg", nil, turnOpt{}); text != "Soll ich dich als Jörg speichern?" {
		t.Fatalf("corrected = %q", text)
	}
	if _, text, _, _ := say(t, "No, my name is George", nil, turnOpt{}); text != "Soll ich dich als George speichern?" {
		t.Fatalf("second correction = %q", text)
	}
	if _, text, _, _ := say(t, "nein, ich heiße Georg", nil, turnOpt{}); !strings.Contains(text, "Einstellungen → Personen") {
		t.Errorf("third correction = %q", text)
	}
	say(t, "ich bin Jorg", v, turnOpt{})
	if _, text, _, _ := say(t, "nein", nil, turnOpt{}); text != "Okay, ich lege niemanden an." {
		t.Errorf("no = %q", text)
	}
	if peopleCountStored(t) != 0 {
		t.Error("someone was created")
	}
	// The corrected name is the one stored.
	say(t, "ich bin Jorg", v, turnOpt{})
	say(t, "nein, ich heiße Jörg", nil, turnOpt{})
	say(t, "ja", nil, turnOpt{})
	if p, ok := findPersonByName("Jörg"); !ok || p.Name != "Jörg" {
		t.Errorf("stored = %+v", listPeople())
	}
}

func TestEnrollTypedGivesHintOnly(t *testing.T) {
	peopleEnv(t)
	handled, text, _, _ := say(t, "Ich bin Anna", nil, turnOpt{typed: true})
	if !handled || !strings.HasPrefix(text, "Stell dich bitte per Sprache vor") {
		t.Fatalf("typed: %v %q", handled, text)
	}
	_, text, _, _ = say(t, "I'm Anna", nil, turnOpt{typed: true, lang: "en"})
	if !strings.HasPrefix(text, "Please introduce yourself by voice") {
		t.Errorf("typed EN = %q", text)
	}
	addPerson(t, "Anna", true, nil)
	if _, text, _, _ = say(t, "ich bin anna", nil, turnOpt{typed: true}); text != "Wähle dich oben im Sprecher-Menü aus." {
		t.Errorf("typed existing = %q", text)
	}
	if peopleCountStored(t) != 1 || activeClaim(testClient).person != "" {
		t.Error("typed intro changed state")
	}
}

func TestEnrollStoplistAndForms(t *testing.T) {
	peopleEnv(t)
	for _, q := range []string{
		"ich bin müde", "Hermes, ich bin müde", "Ich bin Lehrer", "ich bin Lehrerin", "ich bin der Chef",
		"I'm tired", "I'm Jack's dad", "ich bin 42", "ich bin so müde heute", "Ich bin traurig",
		"I am a teacher", "ich bin zurück", "ich bin gleich da", "ich bin Berliner", "Ich bin Papa",
		"Bin ich Anna?", "ich bin nicht sicher", "das war lecker", "nein danke", "this is great",
	} {
		if handled, text, _, _ := say(t, q, voiceIn(vec(map[int]float64{0: 1}), 2.5), turnOpt{}); handled {
			t.Errorf("%q matched a speaker fast path: %q", q, text)
		}
	}
	if peopleCountStored(t) != 0 {
		t.Error("a person was created")
	}
	for q, want := range map[string]string{
		"Hermes, ich bin Anna-Lena": "Anna-Lena",
		"my name is george":         "George",
		"Ich heiße Jörg, bitte":     "Jörg",
		"hey hermes this is Maria":  "Maria",
		"I am Mary Jane":            "Mary Jane",
		"mein Name ist McDonald":    "McDonald",
	} {
		u, ok := parseSpeakerUtterance(q)
		if !ok || u.Kind != "intro" || u.Name != want {
			t.Errorf("%q: %+v %v, want %q", q, u, ok, want)
		}
	}
}

func TestEnrollExistingPersonClaimAndAntiPoisoning(t *testing.T) {
	peopleEnv(t)
	anna := addPerson(t, "Anna", false, vec(map[int]float64{0: 1}), sampleVoiceAnchor)
	ben := addPerson(t, "Ben", true, nil)

	// An impostor (valid voice, matches nobody) repeats "ich bin Anna".
	for i := 0; i < 5; i++ {
		_, text, tr, _ := say(t, "Hermes, ich bin Anna", voiceIn(vec(map[int]float64{7: 1}), 3), turnOpt{})
		if tr.Speaker.Source != SourceGuest || !strings.HasPrefix(text, "Ich bin mir nicht sicher, ob du Anna bist.") {
			t.Fatalf("impostor %d: %+v %q", i, tr.Speaker, text)
		}
	}
	if p := getPerson(t, anna); len(p.Voice.Samples) != 1 || !p.Provisional || p.Confirmed {
		t.Errorf("impostor changed Anna: samples=%d provisional=%v", len(p.Voice.Samples), p.Provisional)
	}
	clearClaim(testClient)

	// Existing person without a voiceprint: claim, no sample.
	_, text, _, _ := say(t, "ich bin Ben", nil, turnOpt{})
	if !strings.Contains(text, "ob du Ben bist") || activeClaim(testClient).person != ben {
		t.Fatalf("claim: %q %+v", text, activeClaim(testClient))
	}
	if getPerson(t, ben).Voice != nil {
		t.Error("a sample was added to Ben")
	}
	if tr, _ := newPeopleTurn("hi", nil, turnOpt{}); tr.Speaker.ID != ben || tr.Speaker.Source != SourceClaim {
		t.Errorf("next turn = %+v", tr.Speaker)
	}
	// With the PIN lock the reply says personal things need the PIN.
	_, text, _, _ = say(t, "ich bin Ben", nil, turnOpt{settings: func(s *Settings) { s.PersonalLock = true }})
	if !strings.HasSuffix(text, "Persönliches lese ich dir erst vor, wenn du dich oben mit der PIN auswählst.") {
		t.Errorf("lock text = %q", text)
	}
	clearClaim(testClient)

	// Recognised as Anna.
	if _, text, _, _ = say(t, "ich bin Anna", voiceIn(near(0, 0.9), 2), turnOpt{}); text != "Ich erkenne dich, Anna." {
		t.Errorf("recognised = %q", text)
	}
	// Recognised as Anna but claims Ben: no claim.
	if _, text, _, _ = say(t, "ich bin Ben", voiceIn(near(0, 0.9), 2), turnOpt{}); !strings.HasPrefix(text, "Für mich klingst du wie Anna.") {
		t.Errorf("sounds like = %q", text)
	}
	if activeClaim(testClient).person != "" {
		t.Error("claim set although the voice says Anna")
	}
	// Recognised as Anna and introduces a new name: nothing created.
	if _, text, _, _ = say(t, "ich bin Clara", voiceIn(near(0, 0.9), 2), turnOpt{}); !strings.HasPrefix(text, "Für mich klingst du wie Anna.") {
		t.Errorf("new name by a known voice = %q", text)
	}
	if peopleCountStored(t) != 2 {
		t.Error("person created for a known voice")
	}
}

func TestEnrollCapForVoicePeople(t *testing.T) {
	peopleEnv(t)
	addPerson(t, "Anna", false, vec(map[int]float64{0: 1}), sampleVoiceAnchor)
	addPerson(t, "Ben", false, vec(map[int]float64{1: 1}), sampleVoiceAnchor)
	_, text, _, _ := say(t, "ich bin Clara", voiceIn(vec(map[int]float64{2: 1}), 3), turnOpt{})
	if text != "Ich kann gerade keine weitere Person anlegen. Schau unter Einstellungen → Personen." {
		t.Errorf("cap = %q", text)
	}
	if peopleCountStored(t) != 2 {
		t.Error("third voice person created")
	}
}

func TestExpiryPurgesUnconfirmedVoicePeople(t *testing.T) {
	clock := peopleEnv(t)
	var deleted []string
	onPersonDeleted(func(id string) (string, int) { deleted = append(deleted, id); return "memories", 1 })
	clara := addPerson(t, "Clara", false, vec(map[int]float64{0: 1}), sampleVoiceAnchor)
	anna := addPerson(t, "Anna", true, nil)
	corrupt := filepath.Join(dataDir(), "people.json.corrupt-1")
	if err := os.WriteFile(corrupt, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(6 * 24 * time.Hour)
	if n := purgeExpiredPeople(); n != 0 {
		t.Fatalf("purged early: %d", n)
	}
	*clock = clock.Add(24*time.Hour + time.Minute)
	if n := purgeExpiredPeople(); n != 1 {
		t.Fatalf("purged %d, want 1", n)
	}
	if getPerson(t, clara) != nil || getPerson(t, anna) == nil {
		t.Error("wrong person purged")
	}
	if len(deleted) != 1 || deleted[0] != clara {
		t.Errorf("cascade hooks = %v", deleted)
	}
	if _, err := os.Stat(corrupt); !os.IsNotExist(err) {
		t.Error("corrupt copy survived the purge")
	}
}

// ---------------------------------------------------------------------------
// Corrections, "vergiss mich", "Wer spricht gerade?"

func TestSpeakerCorrectClaimsAndReassigns(t *testing.T) {
	clock := peopleEnv(t)
	anna := addPerson(t, "Anna", true, vec(map[int]float64{0: 1}), sampleAnchor, sampleAnchor, sampleAnchor)
	ben := addPerson(t, "Ben", true, vec(map[int]float64{1: 1}), sampleAnchor, sampleAnchor, sampleAnchor)
	var gotClient, gotFrom, gotTo string
	var gotSince time.Time
	undone := false
	reassignRecentMemoriesProvider = func(clientID, from, to string, since time.Time) (int, func(context.Context) error, error) {
		gotClient, gotFrom, gotTo, gotSince = clientID, from, to, since
		return 2, func(context.Context) error { undone = true; return nil }, nil
	}

	// Anna was taken for Ben.
	_, text, _, _ := say(t, "Nein, ich bin Anna", voiceIn(near(1, 0.7), 1.2), turnOpt{})
	if text != "Okay, Anna. Ich habe dich verwechselt. Ich habe 2 Einträge von eben zu dir verschoben." {
		t.Fatalf("correction = %q", text)
	}
	if gotClient != testClient || gotFrom != ben || gotTo != anna || !gotSince.Equal(clock.Add(-5*time.Minute)) {
		t.Errorf("reassign args: %q %q %q %v", gotClient, gotFrom, gotTo, gotSince)
	}
	if activeClaim(testClient).person != anna {
		t.Error("no claim for Anna")
	}
	if sampleCount(t, anna) != 3 || sampleCount(t, ben) != 3 {
		t.Error("a correction touched a voiceprint")
	}
	label, fn, ok := popUndo(testClient, anna)
	if !ok || fn(context.Background()) != nil || !undone || label == "" {
		t.Error("undo for the moved entries missing")
	}

	// "das war Anna" needs a known name; "das war lecker" stays chat.
	clearClaim(testClient)
	if handled, text, _, _ := say(t, "das war Anna", nil, turnOpt{}); !handled || !strings.HasPrefix(text, "Okay, Anna.") {
		t.Errorf("das war Anna: %v %q", handled, text)
	}
	// EN.
	if _, text, _, _ := say(t, "that's not me, I'm Anna", nil, turnOpt{lang: "en"}); !strings.HasPrefix(text, "Okay, Anna. Sorry for the mix-up.") {
		t.Errorf("EN = %q", text)
	}

	// With the lock on a spoken claim moves nothing.
	called := false
	reassignRecentMemoriesProvider = func(string, string, string, time.Time) (int, func(context.Context) error, error) {
		called = true
		return 0, nil, nil
	}
	say(t, "nein, ich bin Anna", nil, turnOpt{settings: func(s *Settings) { s.PersonalLock = true }})
	if called {
		t.Error("reassigned with the lock on")
	}

	// "ich bin nicht Ben": no claim, sticky Ben ignored afterwards.
	_, text, tr, _ := say(t, "ich bin nicht Ben", nil, turnOpt{claimID: ben, claimSrc: SourceSticky})
	if text != "Okay, dann weiß ich gerade nicht, wer spricht." || tr.Speaker.Known() {
		t.Errorf("deny: %q %+v", text, tr.Speaker)
	}
	if tr, _ := newPeopleTurn("hi", nil, turnOpt{claimID: ben, claimSrc: SourceSticky}); tr.Speaker.Source != SourceGuest {
		t.Errorf("sticky after a denial = %+v", tr.Speaker)
	}

	// Unknown name: handled like a new introduction.
	if _, text, _, _ := say(t, "nein, ich bin Clara", voiceIn(vec(map[int]float64{9: 1}), 3), turnOpt{}); text != "Soll ich dich als Clara speichern?" {
		t.Errorf("unknown name = %q", text)
	}
}

func TestForgetMeOnlyPointsToSettings(t *testing.T) {
	peopleEnv(t)
	addPerson(t, "Anna", true, nil)
	if handled, text, _, _ := say(t, "Hermes, vergiss mich", nil, turnOpt{}); !handled || text != "Das geht nur in den Einstellungen unter Personen." {
		t.Errorf("DE: %v %q", handled, text)
	}
	if _, text, _, _ := say(t, "delete my voice", nil, turnOpt{lang: "en"}); text != "You can do that in Settings → People." {
		t.Errorf("EN: %q", text)
	}
	// "vergiss das" is not about the person: forget_me must not match it, so
	// it reaches the memory feature's forget fast path (when registered).
	ftr, _ := newPeopleTurn("vergiss das", nil, turnOpt{})
	for _, f := range fastPaths() {
		if f.Name == "forget_me" {
			if _, ok := f.Match(ftr); ok {
				t.Error("forget_me matched \"vergiss das\"")
			}
		}
	}
	if handled, _, tr, _ := say(t, "vergiss das", nil, turnOpt{}); handled && tr.handledBy != "fast:memory_forget" {
		t.Errorf("vergiss das must be left to memory_forget, handled by %q", tr.handledBy)
	}
	if peopleCountStored(t) != 1 {
		t.Error("forget me deleted someone")
	}
}

func TestWhichSpeakerQuestionResumes(t *testing.T) {
	peopleEnv(t)
	anna := addPerson(t, "Anna", true, vec(map[int]float64{0: 1}), sampleAnchor, sampleAnchor, sampleAnchor)
	addPerson(t, "Ben", true, vec(map[int]float64{1: 1}), sampleAnchor, sampleAnchor, sampleAnchor)
	var ranAs []string
	registerTool(Tool{Name: "stub_read", Access: func(*Turn, json.RawMessage) Access { return AccessRead },
		Run: func(t *Turn, _ json.RawMessage) Reply {
			ranAs = append(ranAs, t.Speaker.ID)
			return Reply{Text: "secret"}
		}})
	ask := func() {
		t.Helper()
		tr, _ := newPeopleTurn("was weißt du", nil, turnOpt{short: true})
		if r := runToolChecked(tr, "stub_read", nil); !strings.HasPrefix(r.Text, "Wer spricht gerade?") {
			t.Fatalf("question = %q", r.Text)
		}
	}

	// Still unrecognised (short voice): the answer sets a claim and re-runs.
	ask()
	if _, text, _, _ := say(t, "ich bin Anna", nil, turnOpt{short: true}); text != "secret" || len(ranAs) != 1 || ranAs[0] != anna {
		t.Fatalf("short answer: %q %v", text, ranAs)
	}
	clearClaim(testClient)

	// Recognised now: the bare name answers and the tool runs as the voice.
	ask()
	handled, text, tr, _ := say(t, "Anna", voiceIn(near(0, 0.8), 2), turnOpt{})
	if !handled || text != "secret" || len(ranAs) != 2 || tr.Speaker.Source != SourceVoice {
		t.Fatalf("voice answer: %v %q %v %+v", handled, text, ranAs, tr.Speaker)
	}

	// Recognised as Ben, says Anna: nothing runs.
	ask()
	if _, text, _, _ = say(t, "ich bin Anna", voiceIn(near(1, 0.8), 2), turnOpt{}); !strings.HasPrefix(text, "Für mich klingst du wie Ben.") || len(ranAs) != 2 {
		t.Errorf("other voice: %q %v", text, ranAs)
	}

	// With the lock on a claim reads nothing.
	ask()
	_, text, _, _ = say(t, "Anna", nil, turnOpt{short: true, settings: func(s *Settings) { s.PersonalLock = true }})
	if len(ranAs) != 2 || !strings.HasPrefix(text, "Ich bin mir nicht sicher, wer spricht.") {
		t.Errorf("lock: %q %v", text, ranAs)
	}

	// An unknown name is not an answer: normal pipeline (the intro path).
	clearClaim(testClient)
	ask()
	if _, text, _, _ = say(t, "ich bin Clara", nil, turnOpt{short: true}); text != "Soll ich dich als Clara speichern?" || len(ranAs) != 2 {
		t.Errorf("unknown: %q %v", text, ranAs)
	}
}

// ---------------------------------------------------------------------------
// HTTP API (3.3)

func doJSON(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	h.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m, rec.Body.String()
}

func voiceprintBody(emb []float32, sec float64) string {
	b, _ := json.Marshal(map[string]any{"model": voiceModelID, "ort": voiceORT, "emb": encEmb(emb), "sec": sec, "kind": "anchor"})
	return string(b)
}

func TestPeopleAPICrudAndLimits(t *testing.T) {
	peopleEnv(t)
	h := newMux()
	code, m, _ := doJSON(t, h, "GET", "/api/people", "")
	if code != 200 || len(m["people"].([]any)) != 0 || m["voice"].(map[string]any)["model"] != voiceModelID {
		t.Fatalf("empty list: %d %v", code, m)
	}
	code, m, _ = doJSON(t, h, "POST", "/api/people", `{"name":"  anna  "}`)
	if code != 201 || m["name"] != "anna" || m["origin"] != "ui" || m["confirmed"] != true || m["provisional"] != true {
		t.Fatalf("create: %d %v", code, m)
	}
	id := m["id"].(string)
	if code, m, _ = doJSON(t, h, "POST", "/api/people", `{"name":"ANNA"}`); code != 409 || m["code"] != "exists" {
		t.Errorf("duplicate: %d %v", code, m)
	}
	for _, bad := range []string{`{"name":"R2D2"}`, `{"name":""}`, `{"name":"<b>x</b>"}`, `{"name":"` + strings.Repeat("a", 41) + `"}`, `not json`} {
		if code, _, _ = doJSON(t, h, "POST", "/api/people", bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if code, m, _ = doJSON(t, h, "PATCH", "/api/people/"+id, `{"name":"Anna Lena","privateReminders":true}`); code != 200 || m["name"] != "Anna Lena" || m["privateReminders"] != true {
		t.Errorf("patch: %d %v", code, m)
	}
	if !personPrivateReminders(id) {
		t.Error("privateReminders not stored")
	}
	doJSON(t, h, "POST", "/api/people", `{"name":"Ben"}`)
	if code, m, _ = doJSON(t, h, "PATCH", "/api/people/"+id, `{"name":"ben"}`); code != 409 {
		t.Errorf("rename onto Ben: %d %v", code, m)
	}
	if code, _, _ = doJSON(t, h, "PATCH", "/api/people/p_000000000000", `{"name":"X"}`); code != 404 {
		t.Errorf("unknown id: %d", code)
	}
	if code, _, _ = doJSON(t, h, "DELETE", "/api/people/..%2Fsecrets", ""); code != 404 {
		t.Errorf("bad id: %d", code)
	}
	// 12 confirmed people at most; unconfirmed voice people do not count.
	addPerson(t, "Vera", false, vec(map[int]float64{0: 1}), sampleVoiceAnchor)
	for i := 0; i < 10; i++ {
		if code, m, _ = doJSON(t, h, "POST", "/api/people", `{"name":"Person `+string(rune('a'+i))+`"}`); code != 201 {
			t.Fatalf("person %d: %d %v", i, code, m)
		}
	}
	if code, m, _ = doJSON(t, h, "POST", "/api/people", `{"name":"Zoe"}`); code != 409 || m["code"] != "limit" {
		t.Errorf("limit: %d %v", code, m)
	}
	if personName, ok := personName(id); !ok || personName != "Anna Lena" || peopleCount() != 13 || !personExists(id) || personExists("p_000000000000") {
		t.Error("providers disagree with the store")
	}
}

func TestVoiceprintEnrolmentValidation(t *testing.T) {
	peopleEnv(t)
	h := newMux()
	_, m, _ := doJSON(t, h, "POST", "/api/people", `{"name":"Anna"}`)
	id := m["id"].(string)
	path := "/api/people/" + id + "/voiceprints"
	base := vec(map[int]float64{0: 1})

	bad, _ := json.Marshal(map[string]any{"model": "x@1", "emb": encEmb(base), "sec": 3})
	if code, m, _ := doJSON(t, h, "POST", path, string(bad)); code != 422 || m["code"] != "model" {
		t.Errorf("model: %d %v", code, m)
	}
	if code, m, _ := doJSON(t, h, "POST", path, voiceprintBody(base, 1.5)); code != 422 || m["code"] != "short" {
		t.Errorf("short: %d %v", code, m)
	}
	if code, m, _ := doJSON(t, h, "POST", path, `{"model":"`+voiceModelID+`","emb":"AAAA","sec":3}`); code != 400 {
		t.Errorf("bad emb: %d %v", code, m)
	}
	code, m, _ := doJSON(t, h, "POST", path, voiceprintBody(base, 3))
	if code != 200 || m["samples"] != 1.0 || m["provisional"] != true {
		t.Fatalf("first take: %d %v", code, m)
	}
	// A take that does not match the first one.
	if code, m, _ = doJSON(t, h, "POST", path, voiceprintBody(vec(map[int]float64{1: 1}), 3)); code != 422 || m["code"] != "inconsistent" {
		t.Errorf("inconsistent: %d %v", code, m)
	}
	if sampleCount(t, id) != 1 {
		t.Error("inconsistent take stored")
	}
	doJSON(t, h, "POST", path, voiceprintBody(near(0, 0.9), 3))
	code, m, _ = doJSON(t, h, "POST", path, voiceprintBody(near(0, 0.85), 3))
	if code != 200 || m["samples"] != 3.0 || m["provisional"] != false {
		t.Fatalf("third take: %d %v", code, m)
	}
	if code, m, _ = doJSON(t, h, "POST", path, voiceprintBody(base, 3)); code != 409 || m["code"] != "limit" {
		t.Errorf("fourth anchor: %d %v", code, m)
	}
	if code, _, _ = doJSON(t, h, "POST", "/api/people/p_000000000000/voiceprints", voiceprintBody(base, 3)); code != 404 {
		t.Errorf("unknown person: %d", code)
	}

	// Embeddings never leave the server.
	_, _, body := doJSON(t, h, "GET", "/api/people", "")
	for _, bad := range []string{"centroid", `"emb"`, encEmb(base)[:16], "voice-anchor", `"samples":[`} {
		if strings.Contains(body, bad) {
			t.Errorf("GET /api/people leaks %q: %s", bad, body)
		}
	}
	if pv, _ := json.Marshal(viewOf(getPerson(t, id))); strings.Contains(string(pv), "centroid") || strings.Contains(string(pv), "emb") {
		t.Errorf("PersonView leaks: %s", pv)
	}

	// Remove the voice, keep the person.
	if os.WriteFile(filepath.Join(dataDir(), "people.json.corrupt-9"), []byte("x"), 0o600) != nil {
		t.Fatal("write corrupt copy")
	}
	if code, m, _ = doJSON(t, h, "DELETE", path, ""); code != 200 || m["samples"] != 0.0 {
		t.Errorf("delete voice: %d %v", code, m)
	}
	if p := getPerson(t, id); p == nil || p.Voice != nil || !p.Provisional {
		t.Errorf("after voice delete: %+v", p)
	}
	if matches, _ := filepath.Glob(filepath.Join(dataDir(), "people.json.corrupt-*")); len(matches) != 0 {
		t.Error("corrupt copies kept after deleting the voice")
	}
}

func TestPeopleDeleteCascadesAndConfirm(t *testing.T) {
	peopleEnv(t)
	h := newMux()
	onPersonDeleted(func(string) (string, int) { return "memories", 3 })
	onPersonDeleted(func(string) (string, int) { return "docs", 1 })
	memoryCountProvider = func(id string) int { return 7 }
	_, m, _ := doJSON(t, h, "POST", "/api/people", `{"name":"Anna"}`)
	id := m["id"].(string)
	if m["memories"] != 7.0 {
		t.Errorf("memory count = %v", m["memories"])
	}
	setClaim(testClient, id)
	for _, f := range []string{"people.json.corrupt-1", "memories.json.corrupt-1"} {
		_ = os.WriteFile(filepath.Join(dataDir(), f), []byte("x"), 0o600)
	}
	code, m, _ := doJSON(t, h, "DELETE", "/api/people/"+id, "")
	d, _ := m["deleted"].(map[string]any)
	if code != 200 || d["person"] != 1.0 || d["memories"] != 3.0 || d["docs"] != 1.0 {
		t.Fatalf("delete: %d %v", code, m)
	}
	if matches, _ := filepath.Glob(filepath.Join(dataDir(), "*.corrupt-*")); len(matches) != 0 {
		t.Errorf("corrupt copies left: %v", matches)
	}
	if activeClaim(testClient).person != "" {
		t.Error("claim survived the deletion")
	}
	if code, _, _ = doJSON(t, h, "DELETE", "/api/people/"+id, ""); code != 404 {
		t.Errorf("second delete: %d", code)
	}

	// Behalten / Entfernen.
	clara := addPerson(t, "Clara", false, vec(map[int]float64{0: 1}), sampleVoiceAnchor)
	if code, m, _ = doJSON(t, h, "POST", "/api/people/"+clara+"/confirm", `{}`); code != 400 {
		t.Errorf("confirm without keep: %d", code)
	}
	if code, m, _ = doJSON(t, h, "POST", "/api/people/"+clara+"/confirm", `{"keep":true}`); code != 200 || m["confirmed"] != true || m["expiresAt"] != nil {
		t.Errorf("keep: %d %v", code, m)
	}
	if code, m, _ = doJSON(t, h, "POST", "/api/people/"+clara+"/confirm", `{"keep":false}`); code != 409 {
		t.Errorf("remove a kept person: %d %v", code, m)
	}
	dora := addPerson(t, "Dora", false, vec(map[int]float64{1: 1}), sampleVoiceAnchor)
	if code, m, _ = doJSON(t, h, "POST", "/api/people/"+dora+"/confirm", `{"keep":false}`); code != 200 || getPerson(t, dora) != nil {
		t.Errorf("remove: %d %v", code, m)
	}
}

func TestVoiceIdentifyNeverAdapts(t *testing.T) {
	peopleEnv(t)
	h := newMux()
	anna := addPerson(t, "Anna", true, vec(map[int]float64{0: 1}), sampleAnchor, sampleAnchor, sampleAnchor)
	addPerson(t, "Ben", true, vec(map[int]float64{1: 1}), sampleAnchor, sampleAnchor, sampleAnchor)
	body, _ := json.Marshal(map[string]any{"voice": map[string]any{"model": voiceModelID, "emb": encEmb(near(0, 0.9)), "sec": 3}})
	code, m, raw := doJSON(t, h, "POST", "/api/voiceid/identify", string(body))
	sp, _ := m["speaker"].(map[string]any)
	if code != 200 || sp["id"] != anna || sp["source"] != SourceVoice || len(m["scores"].([]any)) != 2 {
		t.Fatalf("identify: %d %s", code, raw)
	}
	if sampleCount(t, anna) != 3 || !getPerson(t, anna).LastSeen.IsZero() {
		t.Error("identify adapted or recorded a match")
	}
	body, _ = json.Marshal(map[string]any{"voice": map[string]any{"model": voiceModelID, "emb": encEmb(near(0, 0.3)), "sec": 3}})
	if _, m, _ = doJSON(t, h, "POST", "/api/voiceid/identify", string(body)); m["speaker"].(map[string]any)["source"] != SourceGuest {
		t.Errorf("guest: %v", m)
	}
	if code, _, _ = doJSON(t, h, "POST", "/api/voiceid/identify", `{}`); code != 400 {
		t.Errorf("no voice: %d", code)
	}
}

func TestPeopleAPILockReadOnlyAndMigration(t *testing.T) {
	peopleEnv(t)
	h := newMux()

	// Migration needs the memory feature and a person.
	if code, _, _ := doJSON(t, h, "POST", "/api/people/migration", `{"to":"household"}`); code != 503 {
		t.Errorf("no memory feature: %d", code)
	}
	var gotTo, gotSensitive string
	migrateOwnerMemoriesProvider = func(to, sensitiveTo string) (int, error) { gotTo, gotSensitive = to, sensitiveTo; return 4, nil }
	ownerMemoryStatsProvider = func() (int, map[string]int) { return 4, map[string]int{"preference": 3, "health": 1} }
	if code, _, _ := doJSON(t, h, "POST", "/api/people/migration", `{"to":"household"}`); code != 409 {
		t.Errorf("no people: %d", code)
	}
	if _, m, _ := doJSON(t, h, "GET", "/api/people/migration", ""); m["pending"] != false || m["count"] != 4.0 {
		t.Errorf("migration without people: %v", m)
	}
	first := addPerson(t, "Anna", true, nil)
	addPerson(t, "Ben", true, nil)
	if _, m, _ := doJSON(t, h, "GET", "/api/people/migration", ""); m["pending"] != true || m["byKind"].(map[string]any)["health"] != 1.0 {
		t.Errorf("migration status: %v", m)
	}
	if code, m, _ := doJSON(t, h, "POST", "/api/people/migration", `{"to":"household"}`); code != 200 || m["moved"] != 4.0 || gotTo != "" || gotSensitive != first {
		t.Errorf("household: %d %v to=%q sensitive=%q", code, m, gotTo, gotSensitive)
	}
	if code, _, _ := doJSON(t, h, "POST", "/api/people/migration", `{"to":"`+first+`"}`); code != 200 || gotTo != first || gotSensitive != first {
		t.Errorf("to person: %d %q %q", code, gotTo, gotSensitive)
	}
	for _, bad := range []string{`{"to":"p_000000000000"}`, `{"to":""}`, `{"to":"_owner"}`} {
		if code, _, _ := doJSON(t, h, "POST", "/api/people/migration", bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}

	// Personal lock: mutations need the unlock, the list stays open.
	_ = os.MkdirAll(filepath.Dir(settingsPath()), 0o700)
	if err := os.WriteFile(settingsPath(), []byte(`{"personalLock":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, m, _ := doJSON(t, h, "POST", "/api/people", `{"name":"Eve"}`); code != 403 || m["code"] != "locked" {
		t.Errorf("locked create: %d %v", code, m)
	}
	if code, _, _ := doJSON(t, h, "POST", "/api/voiceid/identify", `{}`); code != 403 {
		t.Errorf("locked identify: %d", code)
	}
	if code, _, _ := doJSON(t, h, "GET", "/api/people", ""); code != 200 {
		t.Errorf("locked list: %d", code)
	}
	_ = os.Remove(settingsPath())

	// A people.json from a newer HERMES: read-only, 503.
	useTempData(t)
	if err := os.WriteFile(filepath.Join(dataDir(), "people.json"), []byte(`{"version":2,"people":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, m, _ := doJSON(t, h, "POST", "/api/people", `{"name":"Eve"}`); code != 503 || m["code"] != "read_only" {
		t.Errorf("read-only: %d %v", code, m)
	}
}

func TestVoiceIDStatus(t *testing.T) {
	peopleEnv(t)
	static := t.TempDir()
	t.Setenv("STATIC_DIR", static)
	addPerson(t, "Anna", true, vec(map[int]float64{0: 1}), sampleAnchor)
	addPerson(t, "Ben", true, nil)
	st := voiceIDStatus(context.Background()).(map[string]any)
	if st["assets"] != false || st["people"] != 2 || st["enrolled"] != 1 || st["model"] != voiceModelID || st["enabled"] != true {
		t.Errorf("status = %v", st)
	}
	for _, f := range voiceIDAssets {
		p := filepath.Join(static, filepath.FromSlash(f))
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		_ = os.WriteFile(p, []byte("x"), 0o600)
	}
	if st = voiceIDStatus(context.Background()).(map[string]any); st["assets"] != true {
		t.Errorf("assets present: %v", st)
	}
}

// A correction from a turn without a known speaker (guest, no voice) has
// nothing of its own to move: guests never extract (4.10), so every recent
// "" auto memory is a household fact of someone else and must stay put.
func TestSpeakerCorrectFromGuestMovesNothing(t *testing.T) {
	peopleEnv(t)
	addPerson(t, "Anna", true, vec(map[int]float64{0: 1}), sampleAnchor, sampleAnchor, sampleAnchor)
	called := false
	reassignRecentMemoriesProvider = func(string, string, string, time.Time) (int, func(context.Context) error, error) {
		called = true
		return 1, nil, nil
	}
	_, text, tr, _ := say(t, "das war Anna", nil, turnOpt{})
	if !strings.HasPrefix(text, "Okay, Anna.") || tr.Speaker.Source != SourceClaim {
		t.Fatalf("correction = %q %+v", text, tr.Speaker)
	}
	if called || strings.Contains(text, "verschoben") {
		t.Errorf("a guest correction reassigned memories: %q", text)
	}
}

// Claims of other devices expire but were only removed when that same device
// asked again; a long-running server kept every client id forever.
func TestClaimsPruneExpiredEntries(t *testing.T) {
	clock := peopleEnv(t)
	for i := 0; i < 50; i++ {
		setClaim(fmt.Sprintf("c_%012x", i), "p_000000000001")
	}
	*clock = clock.Add(claimTTL + time.Second)
	setDenial("c_ffffffffffff", "p_000000000001")
	claimsMu.Lock()
	n := len(claims)
	claimsMu.Unlock()
	if n != 1 {
		t.Errorf("claims after expiry = %d, want 1", n)
	}
}

// A person kept between the expiry check and the delete (a "Behalten" or a
// wizard take racing the sweep) must survive: the delete re-checks inside
// the same store update.
func TestDeletePersonIfRechecksInsideTheUpdate(t *testing.T) {
	clock := peopleEnv(t)
	clara := addPerson(t, "Clara", false, vec(map[int]float64{0: 1}), sampleVoiceAnchor)
	*clock = clock.Add(8 * 24 * time.Hour)
	// The sweep saw Clara expired; then she is kept.
	if err := peopleStore.Update(func(st *peopleFile) error {
		for _, p := range st.People {
			if p.ID == clara {
				p.Confirmed = true
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := deletePersonIf(clara, func(p *Person) bool { return !p.Confirmed }); !errors.Is(err, errPersonKept) {
		t.Fatalf("err = %v, want errPersonKept", err)
	}
	if getPerson(t, clara) == nil {
		t.Fatal("a kept person was deleted")
	}
	if n := purgeExpiredPeople(); n != 0 {
		t.Errorf("purged %d kept people", n)
	}
}
