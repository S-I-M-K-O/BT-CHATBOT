package hermes

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// people.go: the people the household told HERMES about, their voiceprints,
// who is speaking right now (Section 3.1 resolution, 4.6 matching), the
// voice-driven fast paths ("ich bin Anna", "nein, ich bin Anna", "vergiss
// mich"), the People HTTP API (3.3), the deletion cascade trigger (4.8) and
// the expiry of unconfirmed voice-created people.
//
// Voiceprints never leave this file: no API response, log line or event
// carries an embedding or a centroid.

const (
	voiceModelID = "campplus-zh-en-adv-16k@aa3cfc16"
	voiceORT     = "1.30.0"
	voiceDim     = 192
)

const (
	maxConfirmedPeople   = 12 // UI people and kept voice people
	maxUnconfirmedPeople = 2  // voice-created, not yet kept (outside the 12)
	maxAnchors           = 3  // wizard anchors per enrolment
	maxAdaptive          = 7  // adaptive samples, FIFO
	maxNameRunes         = 40

	anchorMinSec       = 2.0  // wizard takes and voice anchors
	anchorConsistency  = 0.60 // cosine of a new take to the anchor centroid
	adaptBonus         = 0.10 // adapt only at threshold + 0.10 …
	adaptMargin        = 0.15 // … and a margin of 0.15 …
	adaptMinSec        = 2.0  // … on at least 2 s of voice …
	adaptEvery         = 10 * time.Minute
	lastSeenEvery      = 5 * time.Minute
	unconfirmedTTL     = 7 * 24 * time.Hour
	claimTTL           = 10 * time.Minute
	reassignWindow     = 5 * time.Minute
	maxNameCorrections = 2
	peopleSweepEvery   = time.Hour
)

// Sample kinds.
const (
	sampleAnchor      = "anchor"       // Settings wizard
	sampleVoiceAnchor = "voice-anchor" // "Ich bin Anna"; never promotes
	sampleAdaptive    = "adaptive"     // strong matches only
)

type peopleFile struct {
	Version int       `json:"version"` // 1
	People  []*Person `json:"people"`
}

type Person struct {
	ID               string      `json:"id"`   // "p_…"
	Name             string      `json:"name"` // 1–40 runes, letters/space/-/'
	CreatedAt        time.Time   `json:"createdAt"`
	LastSeen         time.Time   `json:"lastSeen,omitempty"`
	Provisional      bool        `json:"provisional"` // true until ≥3 wizard anchors
	Origin           string      `json:"origin"`      // "ui" | "voice"
	Confirmed        bool        `json:"confirmed"`   // ui: always; voice: after "Behalten" or the wizard
	ExpiresAt        *time.Time  `json:"expiresAt,omitempty"`
	PrivateReminders bool        `json:"privateReminders"`
	Voice            *Voiceprint `json:"voice,omitempty"`
}

type Voiceprint struct {
	Model     string        `json:"model"`
	ORT       string        `json:"ort"`
	Dim       int           `json:"dim"`
	Centroid  []float32     `json:"centroid"` // L2-normalised mean of Samples
	Samples   []VoiceSample `json:"samples"`
	UpdatedAt time.Time     `json:"updatedAt"`
}

type VoiceSample struct {
	ID   string    `json:"id"`  // "vp_…"
	Emb  []float32 `json:"emb"` // L2-normalised, len Dim
	Sec  float64   `json:"sec"`
	Kind string    `json:"kind"` // anchor | voice-anchor | adaptive
	At   time.Time `json:"at"`
}

var peopleStore = &fileStore[peopleFile]{
	Name:    "people.json",
	Version: 1,
	New:     func() *peopleFile { return &peopleFile{Version: 1} },
}

var (
	errPersonNotFound = errors.New("person not found")
	errNameExists     = errors.New("a person with this name exists")
	errPeopleLimit    = errors.New("people limit reached")
)

// ---------------------------------------------------------------------------
// Memory contract: memory.go sets these provider variables from its init().
// Every caller here is nil-safe, so People still works (with zero counts and
// a 503 on migration) when the memory feature is absent.

var (
	// memoryCountProvider: number of memories stored under a person id
	// (PersonView.memories, the delete dialog).
	memoryCountProvider func(personID string) int
	// ownerMemoryStatsProvider: memories still in the single-user scope
	// "_owner", in total and per kind (GET /api/people/migration).
	ownerMemoryStatsProvider func() (count int, byKind map[string]int)
	// migrateOwnerMemoriesProvider moves every "_owner" memory to `to` (a
	// person id, or "" for the household). With to=="", kinds health,
	// relationship and identity go to sensitiveTo instead (never household).
	migrateOwnerMemoriesProvider func(to, sensitiveTo string) (moved int, err error)
	// reassignRecentMemoriesProvider moves memories with source "auto" that
	// clientID created at or after since and that are stored under scope from
	// (the mistaken person, or "" from a guest turn) to `to` (SPEC 4.6).
	// undo moves them back.
	reassignRecentMemoriesProvider func(clientID, from, to string, since time.Time) (moved int, undo func(ctx context.Context) error, err error)
)

// ---------------------------------------------------------------------------
// Read helpers. They copy what they return, so no caller ever holds a pointer
// into the store.

// personInfo is a person without voice samples (cheap to copy).
type personInfo struct {
	ID, Name, Origin string
	CreatedAt        time.Time
	Confirmed        bool
	Provisional      bool
	PrivateReminders bool
	HasVoice         bool // a voiceprint of the current model
}

func infoOf(p *Person) personInfo {
	return personInfo{ID: p.ID, Name: p.Name, Origin: p.Origin, CreatedAt: p.CreatedAt, Confirmed: p.Confirmed,
		Provisional: p.Provisional, PrivateReminders: p.PrivateReminders, HasVoice: voiceUsable(p.Voice)}
}

func voiceUsable(v *Voiceprint) bool {
	return v != nil && v.Model == voiceModelID && len(v.Centroid) == voiceDim
}

func listPeople() []personInfo {
	var out []personInfo
	if err := peopleStore.View(func(st *peopleFile) error {
		for _, p := range st.People {
			out = append(out, infoOf(p))
		}
		return nil
	}); err != nil {
		log.Printf("people: %v", err)
	}
	return out
}

func findPersonInfo(id string) (personInfo, bool) {
	for _, p := range listPeople() {
		if p.ID == id {
			return p, true
		}
	}
	return personInfo{}, false
}

// findPersonByName matches case-insensitively (and umlaut-folded, so typed
// "Joerg" finds "Jörg").
func findPersonByName(name string) (personInfo, bool) {
	key := nameKey(name)
	for _, p := range listPeople() {
		if nameKey(p.Name) == key {
			return p, true
		}
	}
	return personInfo{}, false
}

func nameKey(name string) string { return foldName(strings.Join(strings.Fields(name), " ")) }

// firstPersonID is the person who gets the single-user memories that must
// never become household ones: the earliest confirmed person, else the
// earliest one. "" when nobody exists. Memory (W1-D) may use it to inject
// "_owner" memories only for that person until the migration is answered.
func firstPersonID() string {
	var best *personInfo
	for _, p := range listPeople() {
		p := p
		switch {
		case best == nil,
			p.Confirmed && !best.Confirmed,
			p.Confirmed == best.Confirmed && p.CreatedAt.Before(best.CreatedAt):
			best = &p
		}
	}
	if best == nil {
		return ""
	}
	return best.ID
}

// personPrivateReminders: reminders for this person default to private
// (Section 9.1, read by the reminders feature).
func personPrivateReminders(id string) bool {
	p, ok := findPersonInfo(id)
	return ok && p.PrivateReminders
}

func countPeople(st *peopleFile) (confirmed, unconfirmed int) {
	for _, p := range st.People {
		if p.Confirmed {
			confirmed++
		} else {
			unconfirmed++
		}
	}
	return
}

// ---------------------------------------------------------------------------
// Vectors

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}

// l2normalize returns a normalised copy, nil for a zero or non-finite vector.
func l2normalize(v []float32) []float32 {
	var n float64
	for _, x := range v {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil
		}
		n += f * f
	}
	n = math.Sqrt(n)
	if n == 0 {
		return nil
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / n)
	}
	return out
}

func meanNormalized(samples []VoiceSample, keep func(VoiceSample) bool) []float32 {
	sum := make([]float32, voiceDim)
	n := 0
	for _, s := range samples {
		if len(s.Emb) != voiceDim || (keep != nil && !keep(s)) {
			continue
		}
		for i, x := range s.Emb {
			sum[i] += x
		}
		n++
	}
	if n == 0 {
		return nil
	}
	return l2normalize(sum)
}

func recomputeCentroid(v *Voiceprint) { v.Centroid = meanNormalized(v.Samples, nil) }

func isAnchorKind(k string) bool { return k == sampleAnchor || k == sampleVoiceAnchor }

func countKind(v *Voiceprint, kind string) int {
	if v == nil {
		return 0
	}
	n := 0
	for _, s := range v.Samples {
		if s.Kind == kind {
			n++
		}
	}
	return n
}

// decodeVoiceEmb validates a voice payload the way /api/chat does (Section
// 3.1): current model, exactly 192 little-endian float32s, all finite, norm
// in [0.5, 2]; the result is renormalised. code is the API error code.
func decodeVoiceEmb(model, emb string) (vec []float32, code string, err error) {
	if model != voiceModelID {
		return nil, "model", fmt.Errorf("voice model %q is not %q", truncateRunes(model, 64), voiceModelID)
	}
	raw, err := base64.StdEncoding.DecodeString(emb)
	if err != nil || len(raw) != 4*voiceDim {
		return nil, "bad_voice", errors.New("emb must be base64 of 192 little-endian float32 values")
	}
	vec = make([]float32, voiceDim)
	var norm float64
	for i := range vec {
		f := math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return nil, "bad_voice", errors.New("emb contains a non-finite value")
		}
		vec[i] = f
		norm += float64(f) * float64(f)
	}
	if norm = math.Sqrt(norm); norm < 0.5 || norm > 2 {
		return nil, "bad_voice", errors.New("emb is not normalised")
	}
	return l2normalize(vec), "", nil
}

// ---------------------------------------------------------------------------
// Matching (Section 4.6)

type speakerScore struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

// scoreSpeakers scores v against every person with a voiceprint of the
// current model, best first.
func scoreSpeakers(v *VoiceInput, people []*Person) []speakerScore {
	if v == nil || len(v.Emb) != voiceDim {
		return nil
	}
	var out []speakerScore
	for _, p := range people {
		if !voiceUsable(p.Voice) {
			continue
		}
		out = append(out, speakerScore{ID: p.ID, Name: p.Name, Score: cosine(v.Emb, p.Voice.Centroid)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// matchSpeaker scores v against the candidates (people with a voiceprint of
// the current model) and returns the accepted person, the best cosine and
// the runner-up's (0 with a single candidate). best is nil when the match is
// rejected (score < th or score − second < margin) or nobody can be matched;
// score and second are reported either way.
func matchSpeaker(v *VoiceInput, people []*Person, th, margin float64) (best *Person, score float64, second float64) {
	scores := scoreSpeakers(v, people)
	if len(scores) == 0 {
		return nil, 0, 0
	}
	score = scores[0].Score
	if len(scores) > 1 {
		second = scores[1].Score
	}
	if !acceptMatch(score, second, th, margin) {
		return nil, score, second
	}
	for _, p := range people {
		if p.ID == scores[0].ID {
			return p, score, second
		}
	}
	return nil, score, second
}

func acceptMatch(score, second, th, margin float64) bool {
	return score >= th && score-second >= margin
}

// voiceCandidates is a copy of the people whose voiceprint can be matched
// (centroids only, no samples).
func voiceCandidates() []*Person {
	var out []*Person
	_ = peopleStore.View(func(st *peopleFile) error {
		for _, p := range st.People {
			cp := &Person{ID: p.ID, Name: p.Name}
			if voiceUsable(p.Voice) {
				cp.Voice = &Voiceprint{Model: p.Voice.Model, Dim: p.Voice.Dim, Centroid: append([]float32(nil), p.Voice.Centroid...)}
			}
			out = append(out, cp)
		}
		return nil
	})
	return out
}

// noteVoiceMatch records an accepted match: LastSeen (at most one write per
// 5 min) and, for a very strong match, an adaptive sample. Adaptive samples
// come only from here (anti-poisoning, Section 8.4).
func noteVoiceMatch(id string, v *VoiceInput, score, second float64, s *Settings) {
	now := nowFunc()
	err := peopleStore.Update(func(st *peopleFile) error {
		var p *Person
		for _, x := range st.People {
			if x.ID == id {
				p = x
			}
		}
		if p == nil {
			return errNoChange
		}
		changed := false
		if now.Sub(p.LastSeen) >= lastSeenEvery {
			p.LastSeen = now
			changed = true
		}
		if s.VoiceIDAdapt && voiceUsable(p.Voice) && score >= s.VoiceIDThreshold+adaptBonus &&
			score-second >= adaptMargin && v.Sec >= adaptMinSec && adaptDue(p.Voice, now) {
			appendAdaptive(p.Voice, v, now)
			changed = true
		}
		if !changed {
			return errNoChange
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStoreReadOnly) {
		log.Printf("people: recording a voice match failed: %v", err)
	}
}

func adaptDue(v *Voiceprint, now time.Time) bool {
	var last time.Time
	for _, s := range v.Samples {
		if s.Kind == sampleAdaptive && s.At.After(last) {
			last = s.At
		}
	}
	return last.IsZero() || now.Sub(last) >= adaptEvery
}

func appendAdaptive(v *Voiceprint, in *VoiceInput, now time.Time) {
	v.Samples = append(v.Samples, VoiceSample{ID: newID("vp"), Emb: append([]float32(nil), in.Emb...),
		Sec: math.Round(in.Sec*100) / 100, Kind: sampleAdaptive, At: now})
	if n := countKind(v, sampleAdaptive); n > maxAdaptive {
		drop := n - maxAdaptive
		kept := v.Samples[:0]
		for _, s := range v.Samples {
			if s.Kind == sampleAdaptive && drop > 0 {
				drop-- // oldest first: samples are appended in time order
				continue
			}
			kept = append(kept, s)
		}
		v.Samples = kept
	}
	recomputeCentroid(v)
	v.UpdatedAt = now
}

// ---------------------------------------------------------------------------
// Claims: "this device is Anna for the next 10 minutes", set by a correction
// or by "ich bin Anna" for an existing person. A denial ("ich bin nicht Ben")
// stops sticky and claims for Ben on that device.

type speakerClaimState struct {
	person string // claimed person id ("" for a denial only)
	deny   string // person id this device said it is not
	until  time.Time
}

var (
	claimsMu sync.Mutex
	claims   = map[string]speakerClaimState{} // by client id
)

func setClaim(clientID, personID string) {
	storeClaim(clientID, speakerClaimState{person: personID})
}

func setDenial(clientID, personID string) {
	storeClaim(clientID, speakerClaimState{deny: personID})
}

// storeClaim records c for clientID for claimTTL and drops every expired
// entry, so claims of devices that never come back do not pile up.
func storeClaim(clientID string, c speakerClaimState) {
	if clientID == "" {
		return
	}
	now := nowFunc()
	c.until = now.Add(claimTTL)
	claimsMu.Lock()
	defer claimsMu.Unlock()
	for k, old := range claims {
		if !now.Before(old.until) {
			delete(claims, k)
		}
	}
	claims[clientID] = c
}

func clearClaim(clientID string) {
	claimsMu.Lock()
	defer claimsMu.Unlock()
	delete(claims, clientID)
}

func activeClaim(clientID string) speakerClaimState {
	if clientID == "" {
		return speakerClaimState{}
	}
	claimsMu.Lock()
	defer claimsMu.Unlock()
	c, ok := claims[clientID]
	if !ok {
		return speakerClaimState{}
	}
	if !nowFunc().Before(c.until) {
		delete(claims, clientID)
		return speakerClaimState{}
	}
	return c
}

func purgeClaimsPerson(personID string) int {
	claimsMu.Lock()
	defer claimsMu.Unlock()
	n := 0
	for k, c := range claims {
		if c.person == personID || c.deny == personID {
			delete(claims, k)
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Speaker resolution (Section 3.1). The first matching rule wins.

func peopleSettings(t *Turn) *Settings {
	if t != nil && t.Settings != nil {
		return t.Settings
	}
	return loadSettings()
}

var debugVoiceID = os.Getenv("HERMES_DEBUG_VOICEID") == "1"

func resolvePeopleSpeaker(t *Turn, v *VoiceInput, claimedID, claimedSource string) Speaker {
	// 1: nobody exists: single-user mode. (peopleCount is this file's
	// provider in production; tests may stub it.)
	if peopleCount() == 0 {
		return Speaker{ID: singleSpeakerID, Source: SourceSingle}
	}
	people := voiceCandidates()
	s := peopleSettings(t)
	byID := map[string]*Person{}
	for _, p := range people {
		byID[p.ID] = p
	}
	named := func(id, source string) Speaker { return Speaker{ID: id, Name: byID[id].Name, Source: source} }
	claimedOK := claimedID != "" && byID[claimedID] != nil

	// 2: a valid voice and enrolled people: the voice decides.
	if v != nil && s.VoiceIDEnabled && len(v.Emb) == voiceDim && (v.Model == "" || v.Model == voiceModelID) && v.Sec >= s.VoiceIDMinSec {
		if scores := scoreSpeakers(v, people); len(scores) > 0 {
			score, second, secondName := scores[0].Score, 0.0, "-"
			if len(scores) > 1 {
				second, secondName = scores[1].Score, scores[1].Name
			}
			ok := acceptMatch(score, second, s.VoiceIDThreshold, s.VoiceIDMargin)
			if debugVoiceID {
				verdict := "reject"
				if ok {
					verdict = "accept"
				}
				log.Printf("voiceid: best=%s %.3f second=%s %.3f sec=%.1f → %s", scores[0].Name, score, secondName, second, v.Sec, verdict)
			}
			if ok {
				id := scores[0].ID
				if c := activeClaim(t.ClientID); c.person != "" && c.person != id {
					clearClaim(t.ClientID) // the voice says someone else
				}
				noteVoiceMatch(id, v, score, second, s)
				sp := named(id, SourceVoice)
				sp.Score = roundScore(score)
				return sp
			}
			if c := activeClaim(t.ClientID); c.person != "" {
				clearClaim(t.ClientID)
			}
			// A rejected valid voice is final: guest. Only a PIN-unlocked
			// manual choice overrides it.
			if claimedSource == SourceManual && claimedOK && t.Unlocked {
				return named(claimedID, SourceManual)
			}
			return Speaker{Source: SourceGuest, Score: roundScore(score)}
		}
	}
	// 3: a claim on this device.
	c := activeClaim(t.ClientID)
	if c.person != "" && byID[c.person] != nil {
		return named(c.person, SourceClaim)
	}
	// 4: the chip (only after a PIN unlock while the lock is on).
	if claimedSource == SourceManual && claimedOK && (!s.PersonalLock || t.Unlocked) {
		return named(claimedID, SourceManual)
	}
	// 5: the last voice match on this device, only without a usable voice.
	if claimedSource == SourceSticky && claimedOK && c.deny != claimedID && (t.VoiceShort || !t.VoiceExpected) {
		return named(claimedID, SourceSticky)
	}
	// 6: voice expected but too short.
	if t.VoiceExpected && t.VoiceShort {
		return Speaker{Source: SourceUncertain}
	}
	return Speaker{Source: SourceGuest}
}

func roundScore(f float64) float64 { return math.Round(f*1000) / 1000 }

// ---------------------------------------------------------------------------
// Utterance parsing for the voice fast paths. Matchers work on whole words of
// the whole utterance (after "hermes" and fillers are stripped); anything
// longer is a normal chat message.

// speakerWords splits s into words, keeping inner hyphens and apostrophes
// ("Anna-Lena", "O'Brien", "I'm", "Jack's"). Case is kept.
func speakerWords(s string) []string {
	s = strings.NewReplacer("’", "'", "‘", "'", "`", "'").Replace(s)
	raw := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '\''
	})
	out := raw[:0]
	for _, w := range raw {
		if w = strings.Trim(w, "-'"); w != "" {
			out = append(out, w)
		}
	}
	return out
}

var (
	speakerLeadFillers  = wordSet("hermes", "hey", "hi", "hallo", "hello", "ok", "okay", "also", "so", "äh", "ähm", "ehm", "em", "um", "uh", "uhm", "und", "and", "bitte", "please", "well", "na", "nun", "oh", "ja", "yes")
	speakerTrailFillers = wordSet("hermes", "bitte", "please", "danke", "thanks")
	speakerNoWords      = wordSet("nein", "no", "nee", "ne", "nö", "nope")
)

// stripSpeakerFillers removes leading and trailing fillers; it returns the
// words unchanged in case and the lower-cased copy.
func stripSpeakerFillers(words []string) (orig, low []string) {
	i, j := 0, len(words)
	for i < j && speakerLeadFillers[strings.ToLower(words[i])] {
		i++
	}
	for j > i && speakerTrailFillers[strings.ToLower(words[j-1])] {
		j--
	}
	orig = words[i:j]
	low = make([]string, len(orig))
	for k, w := range orig {
		low[k] = strings.ToLower(w)
	}
	return orig, low
}

// Self-introduction forms ("ich bin X", "I'm X", …).
var introForms = [][]string{
	{"ich", "bin"}, {"mein", "name", "ist"}, {"ich", "heiße"}, {"ich", "heisse"},
	{"i'm"}, {"i", "am"}, {"my", "name", "is"}, {"this", "is"},
}

func hasPrefixWords(low []string, form []string) bool {
	if len(low) < len(form) {
		return false
	}
	for i, f := range form {
		if low[i] != f {
			return false
		}
	}
	return true
}

// introName parses "<intro form> <name>" exactly. rest are the words after
// the form (original case).
func introName(orig, low []string) (string, bool) {
	for _, f := range introForms {
		if hasPrefixWords(low, f) {
			return parseName(orig[len(f):])
		}
	}
	return "", false
}

// parseName accepts 1–2 name tokens: letters (with inner - or '), at least 2
// letters each, no digits, no article, no stop word, no possessive.
func parseName(words []string) (string, bool) {
	if len(words) == 0 || len(words) > 2 {
		return "", false
	}
	parts := make([]string, 0, len(words))
	for _, w := range words {
		if !nameToken(w) {
			return "", false
		}
		parts = append(parts, titleName(w))
	}
	name := strings.Join(parts, " ")
	if utf8.RuneCountInString(name) > maxNameRunes {
		return "", false
	}
	return name, true
}

func nameToken(w string) bool {
	lw := strings.ToLower(w)
	if strings.HasSuffix(lw, "'s") || strings.HasPrefix(lw, "'") {
		return false // "Jack's dad"
	}
	letters := 0
	for _, r := range w {
		switch {
		case unicode.IsLetter(r):
			letters++
		case r == '-' || r == '\'':
		default:
			return false // digits and everything else
		}
	}
	return letters >= 2 && !nameStopWord(w)
}

// titleName title-cases a token per part ("anna-lena" → "Anna-Lena") but
// keeps mixed case the speaker typed ("McDonald").
func titleName(w string) string {
	if !isLowerWord(w) && strings.ToUpper(w) != w {
		return w
	}
	var b strings.Builder
	start := true
	for _, r := range strings.ToLower(w) {
		if start {
			b.WriteRune(unicode.ToTitle(r))
		} else {
			b.WriteRune(r)
		}
		start = r == '-' || r == '\''
	}
	return b.String()
}

// speakerUtterance is what a speaker-related utterance says.
type speakerUtterance struct {
	Kind      string `json:"kind"`                // "intro" | "correct" | "deny"
	Name      string `json:"name,omitempty"`      // the name the speaker gives
	Not       string `json:"not,omitempty"`       // the name the speaker says they are not
	NeedKnown bool   `json:"needKnown,omitempty"` // only a known person may be meant ("das war Anna")
}

func isQuestionText(q string) bool { return strings.HasSuffix(strings.TrimSpace(q), "?") }

// parseSpeakerUtterance recognises the self-introduction (4.3 order 21) and
// correction (order 20) forms. ok=false for everything else.
func parseSpeakerUtterance(query string) (speakerUtterance, bool) {
	if isQuestionText(query) {
		return speakerUtterance{}, false
	}
	orig, low := stripSpeakerFillers(speakerWords(query))
	if len(low) == 0 || len(low) > 9 {
		return speakerUtterance{}, false
	}
	// "nein, ich bin Anna" / "no, I'm Anna" / "nein, Anna"
	if speakerNoWords[low[0]] && len(low) > 1 {
		o, l := orig[1:], low[1:]
		if n, ok := introName(o, l); ok {
			return speakerUtterance{Kind: "correct", Name: n}, true
		}
		if n, ok := parseName(o); ok {
			return speakerUtterance{Kind: "correct", Name: n, NeedKnown: true}, true
		}
		return speakerUtterance{}, false
	}
	// "das war Anna" / "that was Anna"
	for _, f := range [][]string{{"das", "war"}, {"that", "was"}, {"it", "was"}} {
		if hasPrefixWords(low, f) {
			if n, ok := parseName(orig[len(f):]); ok {
				return speakerUtterance{Kind: "correct", Name: n, NeedKnown: true}, true
			}
			return speakerUtterance{}, false
		}
	}
	// "that's not me, I'm Anna" / "das bin ich nicht, ich bin Anna"
	for _, f := range [][]string{{"that's", "not", "me"}, {"that", "is", "not", "me"}, {"das", "bin", "ich", "nicht"}, {"das", "war", "ich", "nicht"}} {
		if hasPrefixWords(low, f) {
			if n, ok := introName(orig[len(f):], low[len(f):]); ok {
				return speakerUtterance{Kind: "correct", Name: n}, true
			}
			return speakerUtterance{}, false
		}
	}
	// "ich bin nicht Ben (, sondern Anna | , ich bin Anna)" / "I'm not Ben (, I'm Anna)"
	for _, f := range [][]string{{"ich", "bin", "nicht"}, {"i'm", "not"}, {"i", "am", "not"}} {
		if !hasPrefixWords(low, f) {
			continue
		}
		o, l := orig[len(f):], low[len(f):]
		for k := 1; k <= 2 && k <= len(o); k++ {
			not, ok := parseName(o[:k])
			if !ok {
				continue
			}
			ro, rl := o[k:], l[k:]
			if len(rl) == 0 {
				return speakerUtterance{Kind: "deny", Not: not, NeedKnown: true}, true
			}
			if rl[0] == "sondern" || rl[0] == "but" {
				ro, rl = ro[1:], rl[1:]
				if n, ok := parseName(ro); ok {
					return speakerUtterance{Kind: "correct", Name: n, Not: not}, true
				}
			}
			if n, ok := introName(ro, rl); ok {
				return speakerUtterance{Kind: "correct", Name: n, Not: not}, true
			}
		}
		return speakerUtterance{}, false
	}
	if n, ok := introName(orig, low); ok {
		return speakerUtterance{Kind: "intro", Name: n}, true
	}
	return speakerUtterance{}, false
}

// answerName reads a name from an answer to "Wer spricht gerade?": an intro
// or correction form, or a bare name ("Anna").
func answerName(query string) (string, bool) {
	if u, ok := parseSpeakerUtterance(query); ok && u.Name != "" {
		return u.Name, true
	}
	orig, low := stripSpeakerFillers(speakerWords(query))
	if len(low) > 0 && speakerNoWords[low[0]] {
		orig = orig[1:]
	}
	return parseName(orig)
}

var forgetMeForms = [][]string{
	{"vergiss", "mich"}, {"vergiss", "mich", "bitte"}, {"lösch", "meine", "stimme"}, {"lösche", "meine", "stimme"},
	{"lösch", "mein", "stimmprofil"}, {"lösche", "mein", "stimmprofil"}, {"loesch", "meine", "stimme"},
	{"forget", "me"}, {"delete", "my", "voice"}, {"delete", "my", "voiceprint"}, {"delete", "my", "voice", "profile"},
	{"remove", "my", "voice"},
}

func isForgetMe(query string) bool {
	_, low := stripSpeakerFillers(speakerWords(query))
	for _, f := range forgetMeForms {
		if len(low) == len(f) && hasPrefixWords(low, f) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Fast paths and their tools

// voiceDevice: this utterance came from a device that captures voice for
// recognition (a voice arrived, or one was expected).
func voiceDevice(t *Turn) bool { return t.Voice != nil || t.VoiceShort || t.VoiceExpected }

func anchorVoice(t *Turn) *VoiceInput {
	if v := t.Voice; v != nil && len(v.Emb) == voiceDim && v.Sec >= anchorMinSec && (v.Model == "" || v.Model == voiceModelID) {
		cp := *v
		cp.Emb = append([]float32(nil), v.Emb...)
		return &cp
	}
	return nil
}

func lockOn(t *Turn) bool { return peopleSettings(t).PersonalLock }

func init() {
	speakerResolver = resolvePeopleSpeaker
	personNameProvider = func(id string) (string, bool) {
		p, ok := findPersonInfo(id)
		return p.Name, ok
	}
	peopleCountProvider = func() int { return len(listPeople()) }
	memoryFirstPersonProvider = firstPersonID
	personExistsProvider = func(id string) bool { _, ok := findPersonInfo(id); return ok }
	whichSpeakerResume = resumeWhichSpeaker

	registerTool(Tool{Name: "enroll_voice", Run: runEnrollVoice})
	registerTool(Tool{Name: "speaker_correct", Run: runSpeakerCorrect})
	registerTool(Tool{Name: "forget_me", Run: runForgetMe})
	registerTool(Tool{Name: "speaker_which", Run: runSpeakerWhich})

	registerFastPath(FastPath{Name: "speaker_which", Order: 2, Tool: "speaker_which", Match: matchSpeakerWhich})
	registerFastPath(FastPath{Name: "speaker_correct", Order: 20, Tool: "speaker_correct", Match: matchSpeakerCorrect})
	registerFastPath(FastPath{Name: "enroll_voice", Order: 21, Tool: "enroll_voice", Match: matchEnrollVoice})
	registerFastPath(FastPath{Name: "forget_me", Order: 29, Tool: "forget_me", Match: func(t *Turn) (json.RawMessage, bool) {
		return nil, isForgetMe(t.Query)
	}})

	registerRoute("GET /api/people", handlePeopleList)
	registerRoute("POST /api/people", requireUnlock(handlePeopleCreate))
	registerRoute("PATCH /api/people/{id}", requireUnlock(handlePeopleUpdate))
	registerRoute("DELETE /api/people/{id}", requireUnlock(handlePeopleDelete))
	registerRoute("POST /api/people/{id}/voiceprints", requireUnlock(handleVoiceprintAdd))
	registerRoute("DELETE /api/people/{id}/voiceprints", requireUnlock(handleVoiceprintDelete))
	registerRoute("POST /api/people/{id}/confirm", requireUnlock(handlePeopleConfirm))
	registerRoute("POST /api/voiceid/identify", requireUnlock(handleVoiceIdentify))
	registerRoute("GET /api/people/migration", requireUnlock(handleMigrationGet))
	registerRoute("POST /api/people/migration", requireUnlock(handleMigrationPost))

	registerStatus("voiceId", voiceIDStatus)
	registerBackground("people-expiry", runPeopleExpiry)
}

func matchEnrollVoice(t *Turn) (json.RawMessage, bool) {
	u, ok := parseSpeakerUtterance(t.Query)
	if !ok || u.Kind != "intro" {
		return nil, false
	}
	b, _ := json.Marshal(u)
	return b, true
}

func matchSpeakerCorrect(t *Turn) (json.RawMessage, bool) {
	u, ok := parseSpeakerUtterance(t.Query)
	if !ok || u.Kind == "intro" {
		return nil, false
	}
	if u.NeedKnown {
		// "das war X", "nein, X", "ich bin nicht X" only mean a person HERMES
		// knows; "das war lecker" and "ich bin nicht sicher" stay chat.
		key := u.Name
		if u.Kind == "deny" {
			key = u.Not
		}
		if _, known := findPersonByName(key); !known {
			return nil, false
		}
	}
	b, _ := json.Marshal(u)
	return b, true
}

func decodeUtterance(args json.RawMessage) speakerUtterance {
	var u speakerUtterance
	_ = json.Unmarshal(args, &u)
	return u
}

func runForgetMe(t *Turn, _ json.RawMessage) Reply {
	return Reply{Text: pick(t.Lang, "You can do that in Settings → People.", "Das geht nur in den Einstellungen unter Personen.")}
}

// runEnrollVoice handles "ich bin Anna" deterministically (Section 4.6); the
// turn never reaches persona chat.
func runEnrollVoice(t *Turn, args json.RawMessage) Reply {
	u := decodeUtterance(args)
	if u.Name == "" {
		return Reply{Text: pick(t.Lang, "I didn't catch the name.", "Den Namen habe ich nicht verstanden.")}
	}
	p, exists := findPersonByName(u.Name)
	if !t.Spoken {
		if exists {
			return Reply{Text: pick(t.Lang, "Pick yourself in the speaker menu at the top.", "Wähle dich oben im Sprecher-Menü aus.")}
		}
		return Reply{Text: pick(t.Lang,
			"Please introduce yourself by voice (\"Hermes, I'm "+u.Name+"\"), or add yourself in Settings → People.",
			"Stell dich bitte per Sprache vor („Hermes, ich bin "+u.Name+"“), oder lege dich unter Einstellungen → Personen an.")}
	}
	if exists {
		return introExisting(t, p)
	}
	return introNewName(t, u.Name, 0)
}

// introExisting: "ich bin Anna" for a person HERMES knows. Never adds a
// sample; at most a claim.
func introExisting(t *Turn, p personInfo) Reply {
	if t.Speaker.Source == SourceVoice {
		if t.Speaker.ID == p.ID {
			return Reply{Text: pick(t.Lang, "I recognise you, "+p.Name+".", "Ich erkenne dich, "+p.Name+".")}
		}
		return soundsLike(t, t.Speaker.Name, p.Name)
	}
	setClaim(t.ClientID, p.ID)
	text := pick(t.Lang,
		"I'm not sure you are "+p.Name+". I'll treat you as "+p.Name+" anyway. For better recognition, record your voice in Settings → People.",
		"Ich bin mir nicht sicher, ob du "+p.Name+" bist. Ich behandle dich jetzt trotzdem als "+p.Name+". Für bessere Erkennung nimm deine Stimme unter Einstellungen → Personen auf.")
	if lockOn(t) {
		text += pick(t.Lang,
			" I'll only read out personal things once you pick yourself at the top with the PIN.",
			" Persönliches lese ich dir erst vor, wenn du dich oben mit der PIN auswählst.")
	}
	return Reply{Text: text}
}

func soundsLike(t *Turn, heard, claimed string) Reply {
	return Reply{Text: pick(t.Lang,
		"You sound like "+heard+" to me. If I'm wrong, say \"No, I'm "+claimed+"\".",
		"Für mich klingst du wie "+heard+". Wenn ich mich irre, sag „Nein, ich bin "+claimed+"“.")}
}

func capReply(t *Turn) Reply {
	return Reply{Text: pick(t.Lang,
		"I can't add another person right now. Have a look at Settings → People.",
		"Ich kann gerade keine weitere Person anlegen. Schau unter Einstellungen → Personen.")}
}

// voicePeopleRoom reports whether one more unconfirmed voice person fits.
func voicePeopleRoom() bool {
	ok := false
	_ = peopleStore.View(func(st *peopleFile) error {
		c, u := countPeople(st)
		ok = u < maxUnconfirmedPeople && c < maxConfirmedPeople
		return nil
	})
	return ok
}

// introNewName starts (or, after a correction, restarts) the two-step
// creation: confirm the spelled name, then make sure there is an anchor.
func introNewName(t *Turn, name string, corrections int) Reply {
	if t.Speaker.Source == SourceVoice && t.Speaker.Known() {
		return soundsLike(t, t.Speaker.Name, name)
	}
	if !voicePeopleRoom() {
		return capReply(t)
	}
	if !voiceDevice(t) {
		return Reply{Text: pick(t.Lang,
			"I can't record voices on this device. Add yourself in Settings → People.",
			"Auf diesem Gerät kann ich keine Stimmen aufnehmen. Lege dich unter Einstellungen → Personen an.")}
	}
	return askEnrollConfirm(t, name, corrections, anchorVoice(t))
}

func askEnrollConfirm(t *Turn, name string, corrections int, voice *VoiceInput) Reply {
	q := pick(t.Lang, "Should I save you as "+name+"?", "Soll ich dich als "+name+" speichern?")
	p := Pending{
		Kind: "confirm",
		Tool: "enroll_voice",
		Resume: func(t *Turn, _ *Pending) (Reply, bool) {
			return resumeEnrollConfirm(t, name, corrections, voice)
		},
	}
	r := t.Ask(p, q)
	r.Card = &Card{Kind: "person", Data: map[string]any{"name": name, "pending": true}}
	return r
}

func resumeEnrollConfirm(t *Turn, name string, corrections int, voice *VoiceInput) (Reply, bool) {
	orig, low := stripSpeakerFillers(speakerWords(t.Query))
	// "Nein, ich heiße Jörg" / "No, my name is George": ask again.
	if len(low) > 1 && speakerNoWords[low[0]] {
		corrected, ok := introName(orig[1:], low[1:])
		if !ok {
			corrected, ok = parseName(orig[1:])
		}
		if ok {
			if corrections >= maxNameCorrections {
				return Reply{Text: pick(t.Lang,
					"Let's do that in Settings → People instead.",
					"Lass uns das lieber unter Einstellungen → Personen machen.")}, true
			}
			if _, exists := findPersonByName(corrected); exists {
				return Reply{Text: pick(t.Lang,
					corrected+" already exists. If that's you, say \"I'm "+corrected+"\".",
					corrected+" gibt es schon. Wenn du das bist, sag „Ich bin "+corrected+"“.")}, true
			}
			if v := anchorVoice(t); v != nil {
				voice = v
			}
			return askEnrollConfirm(t, corrected, corrections+1, voice), true
		}
	}
	switch yesNo(t.Tokens) {
	case 1:
		if v := anchorVoice(t); v != nil && (t.Speaker.Source != SourceVoice || !t.Speaker.Known()) {
			voice = v
		}
		if t.Speaker.Source == SourceVoice && t.Speaker.Known() {
			return soundsLike(t, t.Speaker.Name, name), true
		}
		if voice != nil {
			return createVoicePerson(t, name, voice), true
		}
		return askEnrollAnchor(t, name, 0), true
	case -1:
		return Reply{Text: pick(t.Lang, "Okay, I won't add anyone.", "Okay, ich lege niemanden an.")}, true
	}
	return Reply{}, false
}

func askEnrollAnchor(t *Turn, name string, retries int) Reply {
	q := pick(t.Lang,
		"I haven't heard your voice long enough yet. Tell me in a sentence or two what you're up to today.",
		"Ich habe deine Stimme noch nicht lange genug gehört. Erzähl mir in ein, zwei Sätzen, was du heute vorhast.")
	if retries > 0 {
		q = pick(t.Lang,
			"That was a bit short. Please tell me a little more, in two or three sentences.",
			"Das war noch etwas kurz. Erzähl mir bitte noch ein bisschen mehr, in zwei, drei Sätzen.")
	}
	return t.Ask(Pending{
		Kind: "voice",
		Tool: "enroll_voice",
		Resume: func(t *Turn, _ *Pending) (Reply, bool) {
			return resumeEnrollAnchor(t, name, retries)
		},
	}, q)
}

// resumeEnrollAnchor consumes the next spoken utterance as the anchor; it is
// never dispatched as a command. Typed input is not an answer.
func resumeEnrollAnchor(t *Turn, name string, retries int) (Reply, bool) {
	if !t.Spoken {
		return Reply{}, false
	}
	if t.Speaker.Source == SourceVoice && t.Speaker.Known() {
		return soundsLike(t, t.Speaker.Name, name), true
	}
	if v := anchorVoice(t); v != nil {
		return createVoicePerson(t, name, v), true
	}
	if retries == 0 {
		return askEnrollAnchor(t, name, 1), true
	}
	return Reply{Text: pick(t.Lang,
		"Better record your voice in Settings → People.",
		"Nimm deine Stimme lieber unter Einstellungen → Personen auf.")}, true
}

// createVoicePerson creates the provisional, unconfirmed voice person with
// its single voice-anchor sample.
func createVoicePerson(t *Turn, name string, v *VoiceInput) Reply {
	emb := l2normalize(v.Emb)
	if len(emb) != voiceDim {
		return Reply{Text: pick(t.Lang, "That didn't work.", "Das hat nicht geklappt."), Error: true}
	}
	now := nowFunc()
	exp := now.Add(unconfirmedTTL)
	p := &Person{
		ID: newID("p"), Name: name, CreatedAt: now, Provisional: true, Origin: "voice", ExpiresAt: &exp,
		Voice: &Voiceprint{Model: voiceModelID, ORT: voiceORTOf(v), Dim: voiceDim, UpdatedAt: now,
			Samples: []VoiceSample{{ID: newID("vp"), Emb: emb, Sec: math.Round(v.Sec*100) / 100, Kind: sampleVoiceAnchor, At: now}}},
	}
	recomputeCentroid(p.Voice)
	err := peopleStore.Update(func(st *peopleFile) error {
		c, u := countPeople(st)
		if u >= maxUnconfirmedPeople || c >= maxConfirmedPeople {
			return errPeopleLimit
		}
		for _, x := range st.People {
			if nameKey(x.Name) == nameKey(name) {
				return errNameExists
			}
		}
		st.People = append(st.People, p)
		return nil
	})
	switch {
	case errors.Is(err, errPeopleLimit):
		return capReply(t)
	case errors.Is(err, errNameExists):
		return Reply{Text: pick(t.Lang, name+" already exists.", name+" gibt es schon.")}
	case err != nil:
		log.Printf("people: creating a voice person failed: %v", err)
		return Reply{Text: pick(t.Lang, "I couldn't save that right now.", "Das konnte ich gerade nicht speichern."), Error: true}
	}
	setClaim(t.ClientID, p.ID)
	hubPublish("people.changed", "", map[string]any{})
	return Reply{
		Text: pick(t.Lang,
			"Nice to meet you, "+name+". I've created a provisional voice profile. For reliable recognition, record three more sentences in Settings → People.",
			"Freut mich, "+name+". Ich habe ein vorläufiges Stimmprofil angelegt. Für sichere Erkennung nimm unter Einstellungen → Personen noch drei Sätze auf."),
		Card: &Card{Kind: "person", Data: map[string]any{"id": p.ID, "name": name, "confirmable": true}},
	}
}

func voiceORTOf(v *VoiceInput) string {
	if o := cleanText(v.ORT, 16); o != "" {
		return o
	}
	return voiceORT
}

// runSpeakerCorrect handles "nein, ich bin Anna", "das war Anna" and "ich bin
// nicht Ben" (Section 4.6). It never touches a voiceprint.
func runSpeakerCorrect(t *Turn, args json.RawMessage) Reply {
	u := decodeUtterance(args)
	if u.Kind == "deny" {
		if p, ok := findPersonByName(u.Not); ok {
			setDenial(t.ClientID, p.ID)
		}
		t.Speaker = Speaker{Source: SourceGuest}
		return Reply{Text: pick(t.Lang, "Okay, then I don't know who is speaking right now.", "Okay, dann weiß ich gerade nicht, wer spricht.")}
	}
	p, known := findPersonByName(u.Name)
	if !known {
		if !t.Spoken {
			return runEnrollVoice(t, mustJSON(speakerUtterance{Kind: "intro", Name: u.Name}))
		}
		return introNewName(t, u.Name, 0)
	}
	mistaken := t.Speaker
	setClaim(t.ClientID, p.ID)
	t.Speaker = Speaker{ID: p.ID, Name: p.Name, Source: SourceClaim}
	text := pick(t.Lang, "Okay, "+p.Name+". Sorry for the mix-up.", "Okay, "+p.Name+". Ich habe dich verwechselt.")
	// Reassign what the extractor just stored for the wrong speaker. With the
	// PIN lock on a spoken claim is not trusted, so nothing moves. Only a
	// known mistaken person has anything of their own to move: guest turns
	// never extract, so recent household auto memories stay household.
	if fn := reassignRecentMemoriesProvider; fn != nil && t.ClientID != "" && !lockOn(t) && mistaken.Known() && mistaken.ID != p.ID {
		n, undo, err := fn(t.ClientID, mistaken.ID, p.ID, nowFunc().Add(-reassignWindow))
		switch {
		case err != nil:
			log.Printf("people: moving recent memories failed: %v", err)
		case n > 0:
			if n == 1 {
				text += pick(t.Lang, " I moved one entry from just now to you.", " Ich habe einen Eintrag von eben zu dir verschoben.")
			} else {
				text += pick(t.Lang, fmt.Sprintf(" I moved %d entries from just now to you.", n), fmt.Sprintf(" Ich habe %d Einträge von eben zu dir verschoben.", n))
			}
			if undo != nil {
				t.PushUndo(pick(t.Lang, "entries moved", "Einträge verschoben"), undo)
			}
		}
	}
	return Reply{Text: text}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// ---------------------------------------------------------------------------
// "Wer spricht gerade?" (slot:which, asked by refuse() for an uncertain
// speaker).
//
// Decision on who may answer: the question is asked of a speaker HERMES could
// not identify, so it is stored with speaker "". runPendingAnswer (skills.go)
// lets every unrecognised speaker on that device answer it through
// whichSpeakerResume. A speaker whose voice IS now recognised is skipped by
// runPendingAnswer (the question is put back), so the order-2 fast path
// speaker_which picks that case up: a recognised voice that names itself
// ("ich bin Anna", "Anna") answers the open question and the original tool
// re-runs as that voice. A recognised voice naming someone else gets the
// "Für mich klingst du wie …" reply and nothing runs. The question only ever
// re-runs the tool on the device that asked it and within its 60 s.

// resumeWhichSpeaker answers the question for an unrecognised speaker.
func resumeWhichSpeaker(t *Turn, p *Pending) (Reply, bool) {
	name, ok := answerName(t.Query)
	if !ok {
		return Reply{}, false
	}
	person, known := findPersonByName(name)
	if !known {
		return Reply{}, false // "ich bin Clara" for a new name: the enrol fast path takes it
	}
	return answerWhich(t, p, person), true
}

func answerWhich(t *Turn, p *Pending, person personInfo) Reply {
	switch {
	case t.Speaker.Source == SourceVoice && t.Speaker.ID == person.ID:
		// Recognised as the named person: re-run as them.
	case t.Speaker.Source == SourceVoice:
		return soundsLike(t, t.Speaker.Name, person.Name)
	case t.Voice != nil && t.Speaker.Source == SourceGuest:
		// A valid voice that matched nobody is final (3.1 rule 2): the
		// claim helps later turns without a voice, not this one.
		return introExisting(t, person)
	default:
		setClaim(t.ClientID, person.ID)
		t.Speaker = Speaker{ID: person.ID, Name: person.Name, Source: SourceClaim}
	}
	if p == nil || p.Tool == "" {
		return Reply{Text: pick(t.Lang, "Okay, "+person.Name+".", "Okay, "+person.Name+".")}
	}
	return runToolChecked(t, p.Tool, p.Args)
}

// peekWhichPending returns the open slot:which question of this device,
// without taking it.
func peekWhichPending(clientID string) *Pending {
	if clientID == "" {
		return nil
	}
	pendingMu.Lock()
	defer pendingMu.Unlock()
	p := pendings[clientID]
	if p == nil || p.Kind != "slot" || p.Slot != "which" || p.Speaker != "" || !nowFunc().Before(p.Expires) {
		return nil
	}
	return p
}

func matchSpeakerWhich(t *Turn) (json.RawMessage, bool) {
	if t.Speaker.Source != SourceVoice || peekWhichPending(t.ClientID) == nil {
		return nil, false
	}
	name, ok := answerName(t.Query)
	if !ok {
		return nil, false
	}
	if _, known := findPersonByName(name); !known {
		return nil, false
	}
	return mustJSON(map[string]string{"name": name}), true
}

func runSpeakerWhich(t *Turn, args json.RawMessage) Reply {
	var a struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(args, &a)
	person, known := findPersonByName(a.Name)
	p := takePending(t.ClientID)
	if p != nil && (p.Kind != "slot" || p.Slot != "which") {
		restorePending(t.ClientID, p)
		p = nil
	}
	if !known {
		return Reply{Text: pick(t.Lang, "I don't know that name.", "Den Namen kenne ich nicht.")}
	}
	if p != nil && p.Tool != "" {
		t.toolName = p.Tool
	}
	return answerWhich(t, p, person)
}

// ---------------------------------------------------------------------------
// Deletion cascade (Section 4.8)

// deletePerson removes the person (and their voiceprints) first, so queued
// writers re-checking personExists drop their work, then runs every
// onPersonDeleted hook, deletes corrupt copies that may still hold the
// voiceprint, and announces the change.
func deletePerson(id string) (map[string]int, error) {
	return deletePersonIf(id, nil)
}

// errPersonKept: deletePersonIf found the person no longer deletable (kept
// or confirmed since the caller looked).
var errPersonKept = errors.New("person was kept")

// deletePersonIf deletes the person only when ok (if set) still approves of
// them inside the same store update, so a check made earlier cannot race a
// "Behalten" or a wizard take that confirms the person in between.
func deletePersonIf(id string, ok func(p *Person) bool) (map[string]int, error) {
	err := peopleStore.Update(func(st *peopleFile) error {
		for i, p := range st.People {
			if p.ID == id {
				if ok != nil && !ok(p) {
					return errPersonKept
				}
				st.People = append(st.People[:i], st.People[i+1:]...)
				return nil
			}
		}
		return errPersonNotFound
	})
	if err != nil {
		return nil, err
	}
	purgeClaimsPerson(id)
	counts := runPersonDeleted(id)
	removeCorruptCopies(dataPath(peopleStore.Name))
	removeCorruptCopies(dataPath("memories.json"))
	hubPublish("people.changed", "", map[string]any{})
	counts["person"] = 1
	return counts, nil
}

// purgeExpiredPeople deletes unconfirmed voice people whose 7 days are over.
func purgeExpiredPeople() int {
	now := nowFunc()
	var expired []string
	_ = peopleStore.View(func(st *peopleFile) error {
		for _, p := range st.People {
			if !p.Confirmed && p.ExpiresAt != nil && !now.Before(*p.ExpiresAt) {
				expired = append(expired, p.ID)
			}
		}
		return nil
	})
	n := 0
	stillExpired := func(p *Person) bool {
		return !p.Confirmed && p.ExpiresAt != nil && !now.Before(*p.ExpiresAt)
	}
	for _, id := range expired {
		if _, err := deletePersonIf(id, stillExpired); err == nil {
			n++
		} else if !errors.Is(err, errPersonNotFound) && !errors.Is(err, errPersonKept) {
			log.Printf("people: expiring %s failed: %v", id, err)
		}
	}
	if n > 0 {
		log.Printf("people: removed %d unconfirmed voice profile(s) after 7 days", n)
	}
	return n
}

// runPeopleExpiry is the background worker (started by main, never in tests).
func runPeopleExpiry(ctx context.Context) {
	purgeExpiredPeople()
	tick := time.NewTicker(peopleSweepEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			purgeExpiredPeople()
		}
	}
}

// ---------------------------------------------------------------------------
// HTTP API (Section 3.3)

// PersonView is the only shape a person leaves the server in: no samples,
// embeddings or centroids.
type PersonView struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	CreatedAt        time.Time  `json:"createdAt"`
	LastSeen         *time.Time `json:"lastSeen"`
	Provisional      bool       `json:"provisional"`
	Origin           string     `json:"origin"`
	Confirmed        bool       `json:"confirmed"`
	ExpiresAt        *time.Time `json:"expiresAt"`
	Samples          int        `json:"samples"`
	VoiceModelOk     bool       `json:"voiceModelOk"`
	Memories         int        `json:"memories"`
	PrivateReminders bool       `json:"privateReminders"`
}

func viewOf(p *Person) PersonView {
	v := PersonView{ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt, Provisional: p.Provisional, Origin: p.Origin,
		Confirmed: p.Confirmed, PrivateReminders: p.PrivateReminders, VoiceModelOk: voiceUsable(p.Voice)}
	if !p.LastSeen.IsZero() {
		ls := p.LastSeen
		v.LastSeen = &ls
	}
	if p.ExpiresAt != nil {
		e := *p.ExpiresAt
		v.ExpiresAt = &e
	}
	if p.Voice != nil {
		v.Samples = len(p.Voice.Samples)
	}
	return v
}

func withMemoryCounts(views []PersonView) []PersonView {
	if fn := memoryCountProvider; fn != nil {
		for i := range views {
			views[i].Memories = fn(views[i].ID)
		}
	}
	return views
}

var personIDPattern = regexp.MustCompile(`^p_[0-9a-f]{12}$`)

// peopleStoreError maps store errors to the API error shapes.
func peopleStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errStoreReadOnly):
		writeJSONErrorCode(w, http.StatusServiceUnavailable, "read_only", "people.json was written by a newer HERMES (read-only); refusing to overwrite")
	case errors.Is(err, errPersonNotFound):
		writeJSONErrorCode(w, http.StatusNotFound, "not_found", "unknown person")
	case errors.Is(err, errNameExists):
		writeJSONErrorCode(w, http.StatusConflict, "exists", "a person with this name exists")
	case errors.Is(err, errPeopleLimit):
		writeJSONErrorCode(w, http.StatusConflict, "limit", "people limit reached")
	default:
		log.Printf("people: %v", err)
		writeJSONErrorCode(w, http.StatusInternalServerError, "internal", "could not save people")
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSONErrorCode(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large")
			return false
		}
		writeJSONErrorCode(w, http.StatusBadRequest, "bad_request", "bad request")
		return false
	}
	return true
}

func pathPersonID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !personIDPattern.MatchString(id) {
		writeJSONErrorCode(w, http.StatusNotFound, "not_found", "unknown person")
		return "", false
	}
	return id, true
}

// validPersonName cleans a name from the UI: 1–40 runes of letters, spaces,
// hyphens and apostrophes.
func validPersonName(raw string) (string, bool) {
	name := strings.Join(strings.Fields(cleanText(strings.ReplaceAll(raw, "’", "'"), 200)), " ")
	n := utf8.RuneCountInString(name)
	if n == 0 || n > maxNameRunes {
		return "", false
	}
	letters := 0
	for _, r := range name {
		switch {
		case unicode.IsLetter(r) || unicode.Is(unicode.Mn, r):
			letters++
		case r == ' ' || r == '-' || r == '\'':
		default:
			return "", false
		}
	}
	return name, letters > 0
}

func handlePeopleList(w http.ResponseWriter, r *http.Request) {
	var views []PersonView
	if err := peopleStore.View(func(st *peopleFile) error {
		for _, p := range st.People {
			views = append(views, viewOf(p))
		}
		return nil
	}); err != nil {
		peopleStoreError(w, err)
		return
	}
	if views == nil {
		views = []PersonView{}
	}
	s := loadSettings()
	writeJSON(w, http.StatusOK, map[string]any{
		"people": withMemoryCounts(views),
		"voice": map[string]any{"model": voiceModelID, "ort": voiceORT, "dim": voiceDim,
			"threshold": s.VoiceIDThreshold, "margin": s.VoiceIDMargin, "minSec": s.VoiceIDMinSec},
	})
}

func handlePeopleCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	name, ok := validPersonName(body.Name)
	if !ok {
		writeJSONErrorCode(w, http.StatusBadRequest, "name", "name must be 1–40 letters, spaces, hyphens or apostrophes")
		return
	}
	now := nowFunc()
	p := &Person{ID: newID("p"), Name: name, CreatedAt: now, Provisional: true, Origin: "ui", Confirmed: true}
	err := peopleStore.Update(func(st *peopleFile) error {
		for _, x := range st.People {
			if nameKey(x.Name) == nameKey(name) {
				return errNameExists
			}
		}
		if c, _ := countPeople(st); c >= maxConfirmedPeople {
			return errPeopleLimit
		}
		st.People = append(st.People, p)
		return nil
	})
	if err != nil {
		peopleStoreError(w, err)
		return
	}
	hubPublish("people.changed", "", map[string]any{})
	writeJSON(w, http.StatusCreated, withMemoryCounts([]PersonView{viewOf(p)})[0])
}

func handlePeopleUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathPersonID(w, r)
	if !ok {
		return
	}
	var body struct {
		Name             *string `json:"name"`
		PrivateReminders *bool   `json:"privateReminders"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	var name string
	if body.Name != nil {
		if name, ok = validPersonName(*body.Name); !ok {
			writeJSONErrorCode(w, http.StatusBadRequest, "name", "name must be 1–40 letters, spaces, hyphens or apostrophes")
			return
		}
	}
	var view PersonView
	err := peopleStore.Update(func(st *peopleFile) error {
		var p *Person
		for _, x := range st.People {
			if x.ID == id {
				p = x
			} else if body.Name != nil && nameKey(x.Name) == nameKey(name) {
				return errNameExists
			}
		}
		if p == nil {
			return errPersonNotFound
		}
		if body.Name != nil {
			p.Name = name
		}
		if body.PrivateReminders != nil {
			p.PrivateReminders = *body.PrivateReminders
		}
		view = viewOf(p)
		return nil
	})
	if err != nil {
		peopleStoreError(w, err)
		return
	}
	hubPublish("people.changed", "", map[string]any{})
	writeJSON(w, http.StatusOK, withMemoryCounts([]PersonView{view})[0])
}

func handlePeopleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathPersonID(w, r)
	if !ok {
		return
	}
	counts, err := deletePerson(id)
	if err != nil {
		peopleStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deletedCounts(counts)})
}

// deletedCounts always reports person, memories and docs, plus whatever
// else the cascade hooks counted.
func deletedCounts(counts map[string]int) map[string]int {
	out := map[string]int{"person": 0, "memories": 0, "docs": 0}
	for k, v := range counts {
		out[k] = v
	}
	return out
}

func handleVoiceprintAdd(w http.ResponseWriter, r *http.Request) {
	id, ok := pathPersonID(w, r)
	if !ok {
		return
	}
	var body struct {
		Model string  `json:"model"`
		ORT   string  `json:"ort"`
		Emb   string  `json:"emb"`
		Sec   float64 `json:"sec"`
		Kind  string  `json:"kind"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Kind != "" && body.Kind != sampleAnchor {
		writeJSONErrorCode(w, http.StatusBadRequest, "kind", "only anchor samples can be added")
		return
	}
	emb, code, err := decodeVoiceEmb(body.Model, body.Emb)
	if err != nil {
		status := http.StatusBadRequest
		if code == "model" {
			status = http.StatusUnprocessableEntity
		}
		writeJSONErrorCode(w, status, code, err.Error())
		return
	}
	sec := finiteOr(body.Sec, 0)
	if sec < anchorMinSec {
		writeJSONErrorCode(w, http.StatusUnprocessableEntity, "short", "an enrolment take needs at least 2 seconds of voice")
		return
	}
	ort := cleanText(body.ORT, 16)
	if ort == "" {
		ort = voiceORT
	}
	type result struct {
		Samples     int     `json:"samples"`
		Provisional bool    `json:"provisional"`
		Consistency float64 `json:"consistency"`
		Warning     string  `json:"warning"`
	}
	var res result
	var inconsistent float64 = -1
	now := nowFunc()
	err = peopleStore.Update(func(st *peopleFile) error {
		var p *Person
		for _, x := range st.People {
			if x.ID == id {
				p = x
			}
		}
		if p == nil {
			return errPersonNotFound
		}
		v := p.Voice
		if !voiceUsable(v) {
			v = &Voiceprint{Model: voiceModelID, Dim: voiceDim} // a print of an old model is useless now
		}
		if countKind(v, sampleAnchor) >= maxAnchors {
			return errPeopleLimit
		}
		res.Consistency = 1
		if anchors := meanNormalized(v.Samples, func(s VoiceSample) bool { return isAnchorKind(s.Kind) }); anchors != nil {
			res.Consistency = roundScore(cosine(emb, anchors))
			if res.Consistency < anchorConsistency {
				inconsistent = res.Consistency
				return errNoChange
			}
		}
		cp := *v
		cp.Samples = append(append([]VoiceSample(nil), v.Samples...),
			VoiceSample{ID: newID("vp"), Emb: emb, Sec: math.Round(sec*100) / 100, Kind: sampleAnchor, At: now})
		cp.ORT, cp.UpdatedAt = ort, now
		if countKind(&cp, sampleAnchor) >= maxAnchors {
			// Promotion: three wizard anchors. The single voice anchor has
			// done its job and leaves room for the 7 adaptive samples.
			kept := cp.Samples[:0]
			for _, s := range cp.Samples {
				if s.Kind != sampleVoiceAnchor {
					kept = append(kept, s)
				}
			}
			cp.Samples = kept
			p.Provisional = false
			if !p.Confirmed {
				// A wizard enrolment counts as "Behalten" when there is room.
				if c, _ := countPeople(st); c < maxConfirmedPeople {
					p.Confirmed, p.ExpiresAt = true, nil
				}
			}
		}
		recomputeCentroid(&cp)
		p.Voice = &cp
		res.Samples, res.Provisional = len(cp.Samples), p.Provisional
		return nil
	})
	if err != nil {
		peopleStoreError(w, err)
		return
	}
	if inconsistent >= 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":       "this take does not sound like the earlier ones; please record it again in the same room with the same microphone",
			"code":        "inconsistent",
			"consistency": inconsistent,
		})
		return
	}
	if res.Consistency < anchorConsistency+0.10 {
		res.Warning = "takes differ somewhat; keep the same room and microphone"
	}
	hubPublish("people.changed", "", map[string]any{})
	writeJSON(w, http.StatusOK, res)
}

func handleVoiceprintDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathPersonID(w, r)
	if !ok {
		return
	}
	err := peopleStore.Update(func(st *peopleFile) error {
		for _, p := range st.People {
			if p.ID == id {
				p.Voice = nil
				p.Provisional = true
				return nil
			}
		}
		return errPersonNotFound
	})
	if err != nil {
		peopleStoreError(w, err)
		return
	}
	removeCorruptCopies(dataPath(peopleStore.Name)) // may still hold the old print
	hubPublish("people.changed", "", map[string]any{})
	writeJSON(w, http.StatusOK, map[string]int{"samples": 0})
}

func handlePeopleConfirm(w http.ResponseWriter, r *http.Request) {
	id, ok := pathPersonID(w, r)
	if !ok {
		return
	}
	var body struct {
		Keep *bool `json:"keep"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Keep == nil {
		writeJSONErrorCode(w, http.StatusBadRequest, "bad_request", "keep is required")
		return
	}
	if !*body.Keep {
		// "Entfernen" only removes a person nobody kept yet; a stale toast
		// must never delete a confirmed person.
		info, found := findPersonInfo(id)
		if !found {
			peopleStoreError(w, errPersonNotFound)
			return
		}
		if info.Confirmed {
			writeJSONErrorCode(w, http.StatusConflict, "confirmed", "this person was already kept; delete them in Settings → People")
			return
		}
		counts, err := deletePersonIf(id, func(p *Person) bool { return !p.Confirmed })
		if errors.Is(err, errPersonKept) {
			writeJSONErrorCode(w, http.StatusConflict, "confirmed", "this person was already kept; delete them in Settings → People")
			return
		}
		if err != nil {
			peopleStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": deletedCounts(counts)})
		return
	}
	var view PersonView
	err := peopleStore.Update(func(st *peopleFile) error {
		var p *Person
		for _, x := range st.People {
			if x.ID == id {
				p = x
			}
		}
		if p == nil {
			return errPersonNotFound
		}
		if p.Confirmed {
			view = viewOf(p)
			return errNoChange
		}
		if c, _ := countPeople(st); c >= maxConfirmedPeople {
			return errPeopleLimit
		}
		p.Confirmed, p.ExpiresAt = true, nil
		view = viewOf(p)
		return nil
	})
	if err != nil {
		peopleStoreError(w, err)
		return
	}
	hubPublish("people.changed", "", map[string]any{})
	writeJSON(w, http.StatusOK, withMemoryCounts([]PersonView{view})[0])
}

// handleVoiceIdentify scores a voice against everyone (diagnostics for the
// "Erkennung testen" button). It never adapts or records anything.
func handleVoiceIdentify(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Voice *voicePayload `json:"voice"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Voice == nil {
		writeJSONErrorCode(w, http.StatusBadRequest, "bad_voice", "voice is required")
		return
	}
	emb, code, err := decodeVoiceEmb(body.Voice.Model, body.Voice.Emb)
	if err != nil {
		status := http.StatusBadRequest
		if code == "model" {
			status = http.StatusUnprocessableEntity
		}
		writeJSONErrorCode(w, status, code, err.Error())
		return
	}
	s := loadSettings()
	sec := finiteOr(body.Voice.Sec, 0)
	if sec < s.VoiceIDMinSec {
		writeJSONErrorCode(w, http.StatusUnprocessableEntity, "short", "not enough voiced audio")
		return
	}
	scores := scoreSpeakers(&VoiceInput{Model: voiceModelID, Emb: emb, Sec: sec}, voiceCandidates())
	sp := Speaker{Source: SourceGuest}
	if len(scores) > 0 {
		second := 0.0
		if len(scores) > 1 {
			second = scores[1].Score
		}
		sp.Score = roundScore(scores[0].Score)
		if acceptMatch(scores[0].Score, second, s.VoiceIDThreshold, s.VoiceIDMargin) {
			sp = Speaker{ID: scores[0].ID, Name: scores[0].Name, Source: SourceVoice, Score: roundScore(scores[0].Score)}
		}
	}
	for i := range scores {
		scores[i].Score = roundScore(scores[i].Score)
	}
	if scores == nil {
		scores = []speakerScore{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"speaker": sp, "scores": scores})
}

func handleMigrationGet(w http.ResponseWriter, r *http.Request) {
	count, byKind := 0, map[string]int{}
	if fn := ownerMemoryStatsProvider; fn != nil {
		if n, k := fn(); n > 0 {
			count = n
			if k != nil {
				byKind = k
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": count > 0 && len(listPeople()) > 0, "count": count, "byKind": byKind})
}

func handleMigrationPost(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To string `json:"to"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	fn := migrateOwnerMemoriesProvider
	if fn == nil {
		writeJSONErrorCode(w, http.StatusServiceUnavailable, "unavailable", "memory is not available")
		return
	}
	first := firstPersonID()
	if first == "" {
		writeJSONErrorCode(w, http.StatusConflict, "no_people", "add a person first")
		return
	}
	to, sensitiveTo := body.To, ""
	switch {
	case to == "household":
		to, sensitiveTo = "", first
	case personIDPattern.MatchString(to) && personExists(to):
		sensitiveTo = to
	default:
		writeJSONErrorCode(w, http.StatusBadRequest, "bad_request", "to must be a person id or \"household\"")
		return
	}
	moved, err := fn(to, sensitiveTo)
	if err != nil {
		if errors.Is(err, errStoreReadOnly) {
			writeJSONErrorCode(w, http.StatusServiceUnavailable, "read_only", err.Error())
			return
		}
		log.Printf("people: memory migration failed: %v", err)
		writeJSONErrorCode(w, http.StatusInternalServerError, "internal", "migration failed")
		return
	}
	hubPublish("people.changed", "", map[string]any{})
	writeJSON(w, http.StatusOK, map[string]int{"moved": moved})
}

// ---------------------------------------------------------------------------
// Status (Section 3.12)

// voiceIDAssets are the four files the browser needs for voice ID.
var voiceIDAssets = []string{
	"vendor/ort-1.30.0/ort.wasm.min.js",
	"vendor/ort-1.30.0/ort-wasm-simd-threaded.mjs",
	"vendor/ort-1.30.0/ort-wasm-simd-threaded.wasm",
	"vendor/models/campplus-zh-en-adv-16k.onnx",
}

func voiceIDAssetsPresent() bool {
	dir := envOr("STATIC_DIR", "web/static")
	for _, f := range voiceIDAssets {
		st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil || !st.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func voiceIDStatus(ctx context.Context) any {
	people := listPeople()
	enrolled := 0
	for _, p := range people {
		if p.HasVoice {
			enrolled++
		}
	}
	return map[string]any{
		"assets":   voiceIDAssetsPresent(),
		"enabled":  loadSettings().VoiceIDEnabled,
		"people":   len(people),
		"enrolled": enrolled,
		"model":    voiceModelID,
	}
}
