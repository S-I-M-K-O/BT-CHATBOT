package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// memory.go: durable facts about the people of the household (SPEC 2.3,
// 3.4, 9.B). Facts are stored per person ("p_…"), for the household ("") or,
// before anyone is enrolled, in the reserved single-user scope "_owner".
//
// Ways in:  explicit "merk dir …" (fast path / router tool), automatic
//           extraction after persona turns (background queue), the panel API.
// Ways out: the personal context section (local model only, never a cloud),
//           "was weißt du über mich", the panel API and export.
// Ways to delete: "vergiss …", the panel, person deletion (cascade), expiry.

// ---------------------------------------------------------------------------
// Data model

type memoriesFile struct {
	Version  int       `json:"version"` // 1
	Memories []*Memory `json:"memories"`
}

type Memory struct {
	ID        string    `json:"id"`                 // "m_…"
	Person    string    `json:"person"`             // person id; "" = household; "_owner" = single-user mode
	Text      string    `json:"text"`               // ≤300 runes, third person main clause
	Kind      string    `json:"kind"`               // identity|preference|relationship|date|plan|health|home|other
	Source    string    `json:"source"`             // "explicit" | "auto" | "manual"
	ClientID  string    `json:"clientId,omitempty"` // creating browser
	Lang      string    `json:"lang"`               // "de" | "en"
	Pinned    bool      `json:"pinned"`             // always injected
	Expires   string    `json:"expires,omitempty"`  // "YYYY-MM-DD"; excluded and purged from that day on
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	LastUsed  time.Time `json:"lastUsed,omitempty"`
	Uses      int       `json:"uses"`
}

type vectorsFile struct {
	Version int               `json:"version"` // 1
	Model   string            `json:"model"`   // embed model used; mismatch => re-embed in background
	Dim     int               `json:"dim"`
	Vectors map[string]string `json:"vectors"` // memory id -> base64 little-endian float32
}

const (
	ownerScope         = "_owner" // single-user mode (no people yet)
	memoryMaxRunes     = 300
	memoryMaxTotal     = 2000
	memoryMaxPerScope  = 500
	memoryContextChars = 2000 // budget of the personal-context memory sections
	memoryContextAll   = 15   // up to this many, inject everything
	memoryContextTop   = 8
	memoryContextPins  = 5
	memoryRecallMax    = 8
)

var memoryKinds = wordSet("identity", "preference", "relationship", "date", "plan", "health", "home", "other")

// Kinds that never land in the household scope automatically: they are
// about one person even when the model says otherwise.
var memoryPersonalKinds = wordSet("health", "relationship", "identity")

var (
	errMemoryLimit    = errors.New("memory limit reached")
	errMemoryNotFound = errors.New("memory not found")
	errPersonGone     = errors.New("person no longer exists")
)

var memoriesStore = &fileStore[memoriesFile]{
	Name: "memories.json", Version: 1,
	New: func() *memoriesFile { return &memoriesFile{Version: 1} },
}

var vectorsStore = &fileStore[vectorsFile]{
	Name: "memory_vectors.json", Version: 1,
	New: func() *vectorsFile { return &vectorsFile{Version: 1, Vectors: map[string]string{}} },
}

// memoryFirstPersonProvider names the first-created person (W1-C). Until the
// migration dialog moved the "_owner" memories, they are shown to that person
// only (SPEC 9.B, 5.4). nil or "" => "_owner" memories reach nobody but the
// single-user scope itself. people.go sets it in its init().
var memoryFirstPersonProvider func() string

func memoryFirstPerson() string {
	if memoryFirstPersonProvider == nil {
		return ""
	}
	return memoryFirstPersonProvider()
}

// ---------------------------------------------------------------------------
// Store access. Callers get copies; the store keeps the only pointers.

func memoryList(filter func(*Memory) bool) []Memory {
	var out []Memory
	err := memoriesStore.View(func(st *memoriesFile) error {
		for _, m := range st.Memories {
			if filter == nil || filter(m) {
				out = append(out, *m)
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("memory: read failed: %v", err)
	}
	return out
}

func memoryGet(id string) (Memory, bool) {
	ms := memoryList(func(m *Memory) bool { return m.ID == id })
	if len(ms) == 0 {
		return Memory{}, false
	}
	return ms[0], true
}

// memoryCount is the number of memories in one scope (PersonView.memories).
func memoryCount(person string) int {
	return len(memoryList(func(m *Memory) bool { return m.Person == person }))
}

// checkCaps must run under the store lock with the state about to grow.
func checkCaps(st *memoriesFile, scope string, adding int) error {
	if len(st.Memories)+adding > memoryMaxTotal {
		return errMemoryLimit
	}
	n := 0
	for _, m := range st.Memories {
		if m.Person == scope {
			n++
		}
	}
	if n+adding > memoryMaxPerScope {
		return errMemoryLimit
	}
	return nil
}

// memoryInsert stores a new memory. The person is re-checked under the store
// lock (write-after-delete guard, SPEC 4.8): a job queued for a deleted person
// never resurrects their data.
func memoryInsert(m Memory) (Memory, error) {
	now := nowFunc().UTC()
	if m.ID == "" {
		m.ID = newID("m")
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = m.CreatedAt
	}
	m.Text = cleanText(m.Text, memoryMaxRunes)
	if !memoryKinds[m.Kind] {
		m.Kind = "other"
	}
	if m.Lang != "de" {
		m.Lang = "en"
	}
	err := memoriesStore.Update(func(st *memoriesFile) error {
		if !personExists(m.Person) {
			return errPersonGone
		}
		for _, x := range st.Memories {
			if x.ID == m.ID {
				return fmt.Errorf("duplicate memory id %s", m.ID)
			}
		}
		if err := checkCaps(st, m.Person, 1); err != nil {
			return err
		}
		cp := m
		st.Memories = append(st.Memories, &cp)
		return nil
	})
	return m, err
}

// memoryModify applies fn to the stored memory with id and returns the old
// and new versions. fn returning errNoChange leaves the file untouched.
func memoryModify(id string, fn func(m *Memory) error) (old, updated Memory, err error) {
	err = memoriesStore.Update(func(st *memoriesFile) error {
		for _, m := range st.Memories {
			if m.ID != id {
				continue
			}
			before := *m
			cp := *m
			if err := fn(&cp); err != nil {
				return err
			}
			if cp.Person != before.Person {
				if !personExists(cp.Person) {
					return errPersonGone
				}
				if err := checkCaps(st, cp.Person, 1); err != nil {
					return err
				}
			}
			*m = cp
			old, updated = before, cp
			return nil
		}
		return errMemoryNotFound
	})
	return old, updated, err
}

// memoryRestore puts back an earlier version of a memory (undo): replaced in
// place when it still exists, re-created otherwise.
func memoryRestore(m Memory) error {
	return memoriesStore.Update(func(st *memoriesFile) error {
		if !personExists(m.Person) {
			return errPersonGone
		}
		for i, x := range st.Memories {
			if x.ID == m.ID {
				cp := m
				st.Memories[i] = &cp
				return nil
			}
		}
		if err := checkCaps(st, m.Person, 1); err != nil {
			return err
		}
		cp := m
		st.Memories = append(st.Memories, &cp)
		return nil
	})
}

// memoryDeleteWhere removes every memory matching fn (and its vector).
func memoryDeleteWhere(fn func(*Memory) bool) ([]Memory, error) {
	var gone []Memory
	err := memoriesStore.Update(func(st *memoriesFile) error {
		keep := st.Memories[:0:0]
		for _, m := range st.Memories {
			if fn(m) {
				gone = append(gone, *m)
			} else {
				keep = append(keep, m)
			}
		}
		if len(gone) == 0 {
			return errNoChange
		}
		st.Memories = keep
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(gone) > 0 {
		ids := make([]string, len(gone))
		for i, m := range gone {
			ids[i] = m.ID
		}
		vectorsDelete(ids)
		forgetUsage(ids)
	}
	return gone, nil
}

func memoryDelete(id string) (Memory, error) {
	gone, err := memoryDeleteWhere(func(m *Memory) bool { return m.ID == id })
	if err != nil {
		return Memory{}, err
	}
	if len(gone) == 0 {
		return Memory{}, errMemoryNotFound
	}
	return gone[0], nil
}

// sortMemories: pinned first, then most recently updated.
func sortMemories(ms []Memory) {
	sort.SliceStable(ms, func(i, j int) bool {
		if ms[i].Pinned != ms[j].Pinned {
			return ms[i].Pinned
		}
		return ms[i].UpdatedAt.After(ms[j].UpdatedAt)
	})
}

// memoryToday is the household date used for expiry ("YYYY-MM-DD").
func memoryToday(s *Settings) string {
	return nowFunc().In(settingsLocation(s)).Format("2006-01-02")
}

// memoryExpired: "expires" is the first day the fact no longer applies.
func memoryExpired(m *Memory, today string) bool {
	return m.Expires != "" && m.Expires <= today
}

// validExpires accepts "" or a real date after today.
func validExpires(v, today string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", true
	}
	if _, err := time.Parse("2006-01-02", v); err != nil || v <= today {
		return "", false
	}
	return v, true
}

// ---------------------------------------------------------------------------
// Vectors (derived data: losing them only costs a re-embed)

// vectorsFor returns the decoded vectors made with model.
func vectorsFor(model string) map[string][]float32 {
	out := map[string][]float32{}
	if model == "" {
		return out
	}
	_ = vectorsStore.View(func(st *vectorsFile) error {
		if st.Model != model {
			return nil
		}
		for id, enc := range st.Vectors {
			if v, err := decodeF32(enc); err == nil && len(v) > 0 && (st.Dim == 0 || len(v) == st.Dim) {
				out[id] = v
			}
		}
		return nil
	})
	return out
}

// vectorsSet stores vectors made with model. A different model than the one
// on file invalidates every older vector (they are not comparable).
func vectorsSet(model string, vecs map[string][]float32) {
	if model == "" || len(vecs) == 0 {
		return
	}
	err := vectorsStore.Update(func(st *vectorsFile) error {
		dim := 0
		for _, v := range vecs {
			dim = len(v)
			break
		}
		if st.Model != model || st.Dim != dim || st.Vectors == nil {
			st.Model, st.Dim, st.Vectors = model, dim, map[string]string{}
		}
		for id, v := range vecs {
			if len(v) == dim {
				st.Vectors[id] = encodeF32(v)
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("memory: saving vectors failed: %v", err)
	}
}

func vectorsDelete(ids []string) {
	if len(ids) == 0 {
		return
	}
	err := vectorsStore.Update(func(st *vectorsFile) error {
		n := 0
		for _, id := range ids {
			if _, ok := st.Vectors[id]; ok {
				delete(st.Vectors, id)
				n++
			}
		}
		if n == 0 {
			return errNoChange
		}
		return nil
	})
	if err != nil {
		log.Printf("memory: deleting vectors failed: %v", err)
	}
}

// vectorsRaw / vectorsRestore let an undo put the exact old vector back.
func vectorsRaw(id string) (model, enc string) {
	_ = vectorsStore.View(func(st *vectorsFile) error {
		model, enc = st.Model, st.Vectors[id]
		return nil
	})
	return model, enc
}

func vectorsRestore(id, model, enc string) {
	if enc == "" {
		vectorsDelete([]string{id})
		return
	}
	_ = vectorsStore.Update(func(st *vectorsFile) error {
		if st.Model != model {
			return errNoChange // the model changed meanwhile; the worker re-embeds
		}
		st.Vectors[id] = enc
		return nil
	})
}

// embedMemoryTexts embeds stored texts (doc role) and saves the vectors.
// Errors only mean "lexical until the worker backfills".
func embedMemoryTexts(ctx context.Context, s *Settings, ms []Memory) {
	if len(ms) == 0 || embedModelFor(s) == "" {
		return
	}
	texts := make([]string, len(ms))
	for i, m := range ms {
		texts[i] = m.Text
	}
	vecs, model, err := embedTexts(ctx, s, embedDoc, texts)
	if err != nil {
		if !errors.Is(err, errEmbedModelMissing) && !errors.Is(err, errEmbedDisabled) {
			log.Printf("memory: embedding failed (lexical fallback): %v", err)
		}
		return
	}
	set := map[string][]float32{}
	for i, v := range vecs {
		if v != nil {
			set[ms[i].ID] = v
		}
	}
	vectorsSet(model, set)
}

// memoryInteractiveEmbed bounds embedding inside a user-facing request.
const memoryInteractiveEmbed = 3 * time.Second

func embedMemoriesNow(parent context.Context, s *Settings, ms ...Memory) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), memoryInteractiveEmbed)
	defer cancel()
	embedMemoryTexts(ctx, s, ms)
}

// ---------------------------------------------------------------------------
// Usage counters: recorded in memory on every injection and flushed to disk
// by the background worker, so a persona turn never waits for a file write.

var memUsage = struct {
	sync.Mutex
	m map[string]memUse
}{m: map[string]memUse{}}

type memUse struct {
	n    int
	last time.Time
}

func noteUsage(ids []string) {
	if len(ids) == 0 {
		return
	}
	now := nowFunc().UTC()
	memUsage.Lock()
	for _, id := range ids {
		u := memUsage.m[id]
		u.n++
		u.last = now
		memUsage.m[id] = u
	}
	memUsage.Unlock()
}

func forgetUsage(ids []string) {
	memUsage.Lock()
	for _, id := range ids {
		delete(memUsage.m, id)
	}
	memUsage.Unlock()
}

// flushUsage writes the collected counters into memories.json.
func flushUsage() {
	memUsage.Lock()
	pending := memUsage.m
	memUsage.m = map[string]memUse{}
	memUsage.Unlock()
	if len(pending) == 0 {
		return
	}
	err := memoriesStore.Update(func(st *memoriesFile) error {
		changed := false
		for _, m := range st.Memories {
			if u, ok := pending[m.ID]; ok {
				m.Uses += u.n
				if u.last.After(m.LastUsed) {
					m.LastUsed = u.last
				}
				changed = true
			}
		}
		if !changed {
			return errNoChange
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStoreReadOnly) {
		log.Printf("memory: saving usage failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Text helpers

var (
	deLangWords = wordSet("ich", "und", "der", "die", "das", "ist", "nicht", "mein", "meine", "meinen", "habe",
		"hat", "ein", "eine", "einen", "mit", "für", "wir", "unser", "unsere", "auf", "zu", "im", "sind", "mag",
		"heißt", "bin", "dass", "den", "dem", "von", "auch", "aber", "kein", "keine", "keinen", "er", "sie", "es",
		"wie", "wo", "wann", "morgen", "heute", "gerne", "gern", "sehr", "schon", "noch", "mich", "mir", "du", "dich")
	enLangWords = wordSet("i", "and", "the", "is", "not", "my", "have", "has", "a", "with", "for", "we", "our",
		"on", "to", "are", "like", "that", "of", "from", "but", "no", "it", "you", "your", "me", "he", "she",
		"how", "where", "when", "tomorrow", "today", "very", "don", "doesn", "this", "be", "will", "at", "her", "his")
)

// detectLang votes DE vs EN by function words (umlauts and ß count for
// German); a tie keeps fallback. Facts are written in the language the user
// spoke, not the voice language (SPEC 9.B).
func detectLang(text, fallback string) string {
	de, en := 0, 0
	for _, w := range tokenize(text) {
		if deLangWords[w] {
			de++
		}
		if enLangWords[w] {
			en++
		}
	}
	if strings.ContainsAny(strings.ToLower(text), "äöüß") {
		de++
	}
	switch {
	case de > en:
		return "de"
	case en > de:
		return "en"
	}
	if fallback == "de" {
		return "de"
	}
	return "en"
}

var negationWords = wordSet("nicht", "nie", "niemals", "nichts", "no", "not", "never", "nothing")
var contractedNot = regexp.MustCompile(`(?i)n['’]t\b`)

// negationParity is 1 for a negated statement, 0 otherwise (double
// negations cancel). "mag keinen Koriander" vs "mag doch Koriander" differ.
func negationParity(text string) int {
	n := len(contractedNot.FindAllString(text, -1))
	for _, w := range tokenize(contractedNot.ReplaceAllString(text, " ")) {
		if negationWords[w] || strings.HasPrefix(w, "kein") {
			n++
		}
	}
	return n % 2
}

// nameExclusions are the folded forms of a person's name (and possessive)
// that every fact about them contains; they say nothing about the content.
func nameExclusions(names ...string) map[string]bool {
	ex := map[string]bool{}
	for _, n := range names {
		for _, w := range tokenize(n) {
			f := lexFold(w)
			ex[f] = true
			ex[f+"s"] = true
		}
	}
	return ex
}

// guardTokens are the content tokens of the memory-poisoning guard (SPEC
// 8.3): words of at least four letters, numbers and capitalised words of any
// length, minus stopwords; folded and stemmed.
func guardTokens(s string, exclude map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		f := lexFold(w)
		if lexStopwords[f] || exclude[f] {
			continue
		}
		first := []rune(w)[0]
		if isNumberToken(f) || len([]rune(f)) >= 4 || unicode.IsUpper(first) {
			out[lexStem(f)] = true
		}
	}
	return out
}

// overlapGuard keeps a fact only if it shares ≥2 content tokens, or ≥30 % of
// its own content tokens (at least one), with what the user actually said.
// It stops the model from storing things nobody said.
func overlapGuard(fact, said string, exclude map[string]bool) bool {
	f := guardTokens(fact, exclude)
	if len(f) == 0 {
		return false
	}
	m := guardTokens(said, exclude)
	shared := 0
	for w := range f {
		if m[w] {
			shared++
		}
	}
	return shared >= 2 || (shared >= 1 && shared*10 >= 3*len(f))
}

// memSafe cleans stored text for a prompt data block: no controls, no
// newlines, no delimiter look-alikes.
func memSafe(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	for strings.Contains(s, "<<<") || strings.Contains(s, ">>>") {
		s = strings.ReplaceAll(strings.ReplaceAll(s, "<<<", ""), ">>>", "")
	}
	return truncateRunes(strings.Join(strings.Fields(s), " "), memoryMaxRunes)
}

func endSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	switch s[len(s)-1] {
	case '.', '!', '?', '"', ')':
		return s
	}
	if strings.HasSuffix(s, "“") || strings.HasSuffix(s, "”") || strings.HasSuffix(s, "‘") {
		return s
	}
	return s + "."
}

// genericUserNames are how facts refer to someone without a known name.
var genericUserNames = []string{"Der Nutzer", "Die Nutzerin", "Der Sprecher", "Die Sprecherin", "The user", "The speaker"}

// speakerLabel is the name facts use for the current speaker.
func speakerLabel(t *Turn, lang string) string {
	if t.Speaker.Known() {
		if n := t.Speaker.Name; n != "" {
			return n
		}
		if n, ok := personName(t.Speaker.ID); ok && n != "" {
			return n
		}
	}
	return pick(lang, "The user", "Der Nutzer")
}

// scopeLabel is the name facts in scope use ("" for the household).
func scopeLabel(scope, lang string) string {
	if strings.HasPrefix(scope, "p_") {
		if n, ok := personName(scope); ok && n != "" {
			return n
		}
	}
	return pick(lang, "The user", "Der Nutzer")
}

// --- third person -> second person (recall, read-backs) ---

var deVerb2nd = map[string]string{
	"ist": "bist", "hat": "hast", "mag": "magst", "kann": "kannst", "will": "willst", "muss": "musst",
	"darf": "darfst", "soll": "sollst", "weiß": "weißt", "isst": "isst", "fährt": "fährst", "läuft": "läufst",
	"spricht": "sprichst", "trägt": "trägst", "schläft": "schläfst", "liest": "liest", "sieht": "siehst",
	"gibt": "gibst", "nimmt": "nimmst", "hält": "hältst", "trifft": "triffst", "vergisst": "vergisst",
	"war": "warst", "hatte": "hattest", "wird": "wirst", "möchte": "möchtest", "mochte": "mochtest",
	"wollte": "wolltest", "konnte": "konntest", "musste": "musstest", "sollte": "solltest", "tanzt": "tanzt",
	"heißt": "heißt", "sitzt": "sitzt", "reist": "reist",
}

var dePossessive = map[string]string{
	"ihr": "dein", "ihre": "deine", "ihren": "deinen", "ihrem": "deinem", "ihrer": "deiner", "ihres": "deines",
	"sein": "dein", "seine": "deine", "seinen": "deinen", "seinem": "deinem", "seiner": "deiner", "seines": "deines",
	"sich": "dich",
}

// Feminine (or plural-looking) nouns take "Deine" after "Annas …".
var deFeminine = wordSet("schwester", "mutter", "tochter", "frau", "ehefrau", "freundin", "oma", "großmutter",
	"tante", "nichte", "cousine", "katze", "familie", "adresse", "nummer", "telefonnummer", "arbeit", "firma",
	"wohnung", "stadt", "mama", "chefin", "kollegin", "partnerin", "enkelin", "schwiegermutter", "lieblingsfarbe",
	"lieblingsband", "allergie", "schule", "klasse", "praxis", "ärztin", "größe", "schuhgröße", "kinder", "eltern",
	"geschwister", "großeltern", "enkel", "freunde", "hobbys")

var deMasculineE = wordSet("name", "junge", "käse", "hase", "affe", "löwe", "kunde", "neffe", "gedanke",
	"glaube", "wille", "buchstabe", "kollege", "experte", "kaffee", "tee", "spitzname", "nachname", "vorname")

func deArticleFor(noun string) string {
	n := strings.ToLower(noun)
	if deFeminine[n] {
		return "Deine"
	}
	if deMasculineE[n] {
		return "Dein"
	}
	for _, suf := range []string{"in", "ung", "heit", "keit", "schaft", "ion", "tät", "e"} {
		if strings.HasSuffix(n, suf) && len([]rune(n)) > len([]rune(suf))+2 {
			return "Deine"
		}
	}
	return "Dein"
}

func deConj2nd(verb string) (string, bool) {
	v := strings.ToLower(verb)
	if x, ok := deVerb2nd[v]; ok {
		return x, true
	}
	if !strings.HasSuffix(v, "t") || len([]rune(v)) < 3 {
		return "", false
	}
	for _, suf := range []string{"ßt", "st", "zt", "xt", "tzt"} {
		if strings.HasSuffix(v, suf) {
			return v, true
		}
	}
	if strings.HasSuffix(v, "et") && len([]rune(v)) > 4 { // arbeitet -> arbeitest
		return strings.TrimSuffix(v, "t") + "st", true
	}
	return strings.TrimSuffix(v, "t") + "st", true
}

var enVerb2nd = map[string]string{
	"is": "are", "has": "have", "was": "were", "does": "do", "doesn't": "don't", "doesn’t": "don’t",
	"isn't": "aren't", "isn’t": "aren’t", "wasn't": "weren't", "wasn’t": "weren’t", "hasn't": "haven't",
	"hasn’t": "haven’t", "can": "can", "will": "will", "would": "would", "should": "should", "must": "must",
	"can't": "can't", "won't": "won't", "might": "might", "may": "may",
}

var enPronoun2nd = map[string]string{
	"her": "your", "his": "your", "she": "you", "he": "you", "herself": "yourself", "himself": "yourself",
	"hers": "yours",
}

func enConj2nd(verb string) (string, bool) {
	v := strings.ToLower(verb)
	if x, ok := enVerb2nd[v]; ok {
		return x, true
	}
	if !isASCIIWord(v) {
		return "", false
	}
	switch {
	case strings.HasSuffix(v, "ies") && len(v) > 4:
		return strings.TrimSuffix(v, "ies") + "y", true
	case strings.HasSuffix(v, "ches"), strings.HasSuffix(v, "shes"), strings.HasSuffix(v, "sses"),
		strings.HasSuffix(v, "xes"), strings.HasSuffix(v, "zes"), strings.HasSuffix(v, "oes"):
		return strings.TrimSuffix(v, "es"), true
	case strings.HasSuffix(v, "ss"):
		return "", false
	case strings.HasSuffix(v, "s") && len(v) > 2:
		return strings.TrimSuffix(v, "s"), true
	case strings.HasSuffix(v, "ed"), v == "said", v == "went", v == "got", v == "had", v == "made", v == "met":
		return v, true // past tense is the same in every person
	}
	return "", false
}

func isASCIIWord(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII || !unicode.IsLetter(r) {
			return false
		}
	}
	return s != ""
}

// splitWord returns the first word of s (up to a space) and the remainder.
func splitWord(s string) (string, string) {
	s = strings.TrimLeft(s, " ")
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i], s[i:]
	}
	return s, ""
}

func replaceWords(s string, table map[string]string) string {
	words := strings.Split(s, " ")
	for i, w := range words {
		core := strings.TrimRight(w, ".,;:!?")
		tail := w[len(core):]
		if x, ok := table[strings.ToLower(core)]; ok {
			words[i] = x + tail
		}
	}
	return strings.Join(words, " ")
}

// toSecondPerson turns a stored third-person fact about name into a sentence
// addressed to that person ("Anna mag keinen Koriander." -> "Du magst keinen
// Koriander."). Only the sentence start is rewritten; anything it does not
// understand comes back unchanged.
func toSecondPerson(text, name, lang string) string {
	text = strings.TrimSpace(text)
	subjects := append([]string{}, genericUserNames...)
	if name != "" {
		subjects = append([]string{name}, subjects...)
	}
	for _, subj := range subjects {
		if len(text) <= len(subj) || !strings.EqualFold(text[:len(subj)], subj) {
			continue
		}
		rest := text[len(subj):]
		de := lang == "de"
		switch {
		case de && strings.HasPrefix(rest, "s "): // "Annas Hund heißt Bello."
			noun, after := splitWord(rest[2:])
			if noun == "" {
				continue
			}
			art := deArticleFor(strings.TrimRight(noun, ".,;:!?"))
			if v, _ := splitWord(after); strings.HasSuffix(v, "en") && v != "" && unicode.IsLower([]rune(v)[0]) {
				art = "Deine" // plural verb: "Annas Kinder heißen …"
			}
			return art + " " + noun + after
		case !de && (strings.HasPrefix(rest, "'s ") || strings.HasPrefix(rest, "’s ")):
			return "Your " + strings.TrimLeft(rest[strings.Index(rest, " ")+1:], " ")
		case strings.HasPrefix(rest, " "):
			verb, after := splitWord(rest)
			core := strings.TrimRight(verb, ".,;:!?")
			punct := verb[len(core):]
			if de {
				if v, ok := deConj2nd(core); ok {
					return "Du " + v + punct + replaceWords(after, dePossessive)
				}
			} else if v, ok := enConj2nd(core); ok {
				return "You " + v + punct + replaceWords(after, enPronoun2nd)
			}
		}
		return text
	}
	return text
}

// ---------------------------------------------------------------------------
// Who sees which scopes

// personalScopes are the scopes this speaker's own memories live in: their
// person (or "_owner" in single-user mode), plus "_owner" for the first
// person until the migration dialog was answered.
func personalScopes(t *Turn) []string {
	scope := t.MemoryScope()
	if scope == "" {
		return nil
	}
	out := []string{scope}
	if scope != ownerScope && t.Speaker.Known() && memoryFirstPerson() == t.Speaker.ID {
		out = append(out, ownerScope)
	}
	return out
}

func householdVisible(t *Turn) bool {
	return t.Can(AccessRead) || (t.Settings != nil && t.Settings.GuestSeesHousehold)
}

func inScopes(p string, scopes []string) bool {
	for _, s := range scopes {
		if s == p {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Similarity: duplicates and contradictions

const (
	dupCosine           = 0.90
	dupJaccard          = 0.7
	contradictionCosine = 0.85
	contradictionJacc   = 0.6
)

// similarMemory finds a stored memory in scope that the new fact duplicates
// (same polarity, very similar) or contradicts (opposite negation polarity,
// similar). vec may be nil (lexical only).
func similarMemory(scope, fact string, vec []float32, vecs map[string][]float32, skip map[string]bool) (Memory, bool, bool) {
	exclude := nameExclusions(append([]string{scopeLabel(scope, "de")}, genericUserNames...)...)
	pol := negationParity(fact)
	var best Memory
	bestScore, found, contradicts := 0.0, false, false
	today := memoryToday(loadSettings())
	for _, m := range memoryList(func(m *Memory) bool { return m.Person == scope && !skip[m.ID] }) {
		if memoryExpired(&m, today) {
			continue
		}
		cos := 0.0
		if v := vecs[m.ID]; vec != nil && v != nil {
			cos = cosine32(vec, v)
		}
		jac := lexJaccard(fact, m.Text, exclude)
		opposite := negationParity(m.Text) != pol
		var hit bool
		if opposite {
			hit = cos >= contradictionCosine || jac >= contradictionJacc
		} else {
			hit = cos >= dupCosine || jac >= dupJaccard
		}
		if !hit {
			continue
		}
		score := max(cos, jac)
		if !found || score > bestScore {
			best, bestScore, found, contradicts = m, score, true, opposite
		}
	}
	return best, found, contradicts
}

// ---------------------------------------------------------------------------
// Explicit "merk dir" (fast path 30, router tool "remember")

// memoryRewriteTimeout bounds the rewrite call of an explicit save.
var memoryRewriteTimeout = 2 * time.Second

func extractModelFor(s *Settings) string {
	for _, m := range []string{s.ExtractModel, s.RouterModel, s.LocalModel} {
		if strings.TrimSpace(m) != "" {
			return strings.TrimSpace(m)
		}
	}
	return defaultLocalModel
}

var rewriteSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"fact":     map[string]any{"type": "string"},
		"readback": map[string]any{"type": "string"},
		"kind":     map[string]any{"type": "string", "enum": []string{"identity", "preference", "relationship", "date", "plan", "health", "home", "other"}},
	},
	"required": []string{"fact", "readback", "kind"},
}

// rewriteFact turns what the speaker asked to remember into a third-person
// fact and a second-person read-back (SPEC 9.B). On any failure it quotes
// the statement verbatim instead of guessing.
func rewriteFact(t *Turn, statement, name, lang string) (fact, readback, kind string) {
	s := t.Settings
	fallback := func() (string, string, string) {
		q := truncateRunes(statement, memoryMaxRunes-40)
		if lang == "de" {
			return fmt.Sprintf("%s hat gesagt: „%s“", name, q), fmt.Sprintf("Du hast gesagt: „%s“", q), "other"
		}
		return fmt.Sprintf("%s said: “%s”", name, q), fmt.Sprintf("You said: “%s”", q), "other"
	}
	sys, examples := rewritePrompt(name, lang)
	ctx, cancel := context.WithTimeout(t.Ctx, memoryRewriteTimeout)
	defer cancel()
	res, err := ollamaChatOnce(ctx, s, ollamaChatRequest{
		Model:       extractModelFor(s),
		Messages:    append(append([]chatMessage{{Role: "system", Content: sys}}, examples...), chatMessage{Role: "user", Content: statement}),
		Format:      rewriteSchema,
		Temperature: 0,
		NumPredict:  200,
	})
	if err != nil {
		log.Printf("memory: rewrite failed, quoting instead: %v", err)
		return fallback()
	}
	var out struct {
		Fact     string `json:"fact"`
		Readback string `json:"readback"`
		Kind     string `json:"kind"`
	}
	if err := decodeFirstJSON(res.Content, &out); err != nil {
		return fallback()
	}
	fact = cleanText(strings.Join(strings.Fields(out.Fact), " "), memoryMaxRunes)
	readback = cleanText(strings.Join(strings.Fields(out.Readback), " "), memoryMaxRunes)
	if fact == "" || !overlapGuard(fact, statement, nameExclusions(append([]string{name}, genericUserNames...)...)) ||
		detectLang(fact, lang) != lang || !keepsRelations(fact, statement) {
		return fallback()
	}
	// The read-back is derived from the stored fact whenever the grammar
	// allows: the model's own read-back sometimes adds a clause ("Du wohnst
	// in Berlin, dein Bruder wohnt in Berlin.").
	if rb := toSecondPerson(fact, name, lang); rb != fact {
		readback = rb
	} else if readback == "" || detectLang(readback, lang) != lang || !keepsRelations(readback, statement) {
		readback = fact
	}
	kind = out.Kind
	if !memoryKinds[kind] {
		kind = "other"
	}
	return endSentence(fact), endSentence(readback), kind
}

// rewritePrompt is the rewrite instruction in the language of the statement,
// with two worked examples. A German-only prompt made the model translate
// English statements into German (and the overlap guard then fell back to a
// quote), and without examples it moved facts about a relative onto the
// speaker ("my sister lives in Hamburg" -> "Anna wohnt in Hamburg").
func rewritePrompt(name, lang string) (string, []chatMessage) {
	type ex struct{ said, fact, readback, kind string }
	var sys string
	var exs []ex
	if lang == "de" {
		poss := name + "s Onkel"
		if isGenericUserName(name) {
			poss = "Der Onkel " + map[bool]string{true: "der Nutzerin", false: "des Nutzers"}[strings.HasPrefix(name, "Die ")]
		}
		sys = "Formuliere um, was der Sprecher gesagt hat. fact: ein vollständiger Hauptsatz in der dritten Person über " + name +
			", auf Deutsch. readback: derselbe Satz in der zweiten Person („du“/„dein“), auf Deutsch. " +
			"Ein Nebensatz wird zum Hauptsatz (Verb an zweiter Stelle). Behalte, um wen es geht. Nichts hinzufügen.\n" +
			"kind: identity, preference, relationship, date, plan, health, home oder other. Antworte nur mit JSON."
		exs = []ex{
			// verb-final inputs: "merk dir, dass …" leaves a subordinate clause
			{"mein Onkel Schach spielt", poss + " spielt Schach.", "Dein Onkel spielt Schach.", "relationship"},
			{"ich allergisch gegen Nüsse bin", name + " ist allergisch gegen Nüsse.", "Du bist allergisch gegen Nüsse.", "health"},
		}
	} else {
		sys = "Rewrite what the speaker said. fact: one complete sentence in the third person about " + name +
			", in English. readback: the same sentence in the second person (\"you\"/\"your\"), in English. " +
			"Keep who it is about. Add nothing.\n" +
			"kind: identity, preference, relationship, date, plan, health, home or other. Reply with JSON only."
		exs = []ex{
			{"my uncle plays chess", name + "'s uncle plays chess.", "Your uncle plays chess.", "relationship"},
			{"I am allergic to nuts", name + " is allergic to nuts.", "You are allergic to nuts.", "health"},
		}
	}
	var msgs []chatMessage
	for _, e := range exs {
		b, _ := json.Marshal(map[string]string{"fact": e.fact, "readback": e.readback, "kind": e.kind})
		msgs = append(msgs, chatMessage{Role: "user", Content: e.said}, chatMessage{Role: "assistant", Content: string(b)})
	}
	return sys, msgs
}

func isGenericUserName(name string) bool {
	for _, g := range genericUserNames {
		if g == name {
			return true
		}
	}
	return false
}

// relationWords name someone other than the speaker. A fact about "my
// sister" must still be about the sister, not the speaker.
var relationWords = wordSet(
	"mother", "mom", "mum", "father", "dad", "sister", "brother", "son", "daughter", "wife", "husband",
	"partner", "girlfriend", "boyfriend", "friend", "uncle", "aunt", "cousin", "grandma", "grandpa",
	"grandmother", "grandfather", "colleague", "boss", "neighbour", "neighbor", "child", "children", "kids",
	"mutter", "mama", "vater", "papa", "schwester", "bruder", "sohn", "tochter", "frau", "mann", "freundin",
	"freund", "onkel", "tante", "cousine", "oma", "opa", "kollege", "kollegin", "chef", "chefin", "nachbar",
	"nachbarin", "kind", "kinder")

// keepsRelations reports whether every relative the statement names is still
// named in the rewrite.
func keepsRelations(rewrite, said string) bool {
	have := map[string]bool{}
	for _, w := range tokenize(rewrite) {
		have[w] = true
	}
	for _, w := range tokenize(said) {
		if relationWords[w] && !have[w] {
			return false
		}
	}
	return true
}

// decodeFirstJSON decodes the first JSON object in s (models sometimes wrap
// it in prose or code fences when no format is enforced).
func decodeFirstJSON(s string, v any) error {
	i := strings.IndexByte(s, '{')
	if i < 0 {
		return errors.New("no JSON object")
	}
	return json.NewDecoder(strings.NewReader(s[i:])).Decode(v)
}

type rememberArgs struct {
	Fact      string `json:"fact"`
	Item      string `json:"item"`  // slot answer ("Was soll ich mir merken?")
	Scope     string `json:"scope"` // router: "speaker" | "household"
	Household bool   `json:"household"`
	Bare      bool   `json:"bare"` // "merk dir das": use the previous user message
}

var (
	cmdLead = regexp.MustCompile(`(?i)^(?:\s|[,.!:;-])*(?:hey|hallo|hi|ok|okay|hermes|bitte|mal|please|kannst du|könntest du|würdest du|can you|could you|would you)(?:(?:\s|[,.!:;-])+|$)`)
	cmdTail = regexp.MustCompile(`(?i)(?:(?:\s|[,.!:;-])+(?:hermes|bitte|please|danke|thanks))+(?:\s|[.!])*$`)

	rememberRe = regexp.MustCompile(`(?is)^(?:merke?|merk's|notiere?)\s+dir(?:\s+(?:bitte|mal|doch))*(?:(?:\s|[,.:;!-])+(.*)|\s*)$`)
	// English needs "that"/":"/a pronoun: "remember to …" is a reminder and
	// "remember when …" a question.
	rememberEnRe = regexp.MustCompile(`(?is)^(?:remember|don['’]?t\s+forget|do\s+not\s+forget)(?:\s*:\s*(.*)|\s*,?\s+(that\b.*|this|it|my\b.*|our\b.*|i\b.*|we\b.*)|\s*)$`)

	scopeLead    = regexp.MustCompile(`(?i)^(?:für\s+(?:alle|uns\s+alle|den\s+haushalt|das\s+haus)|fürs\s+haus|for\s+(?:everyone|everybody|all\s+of\s+us|the\s+(?:whole\s+)?household))(?:(?:\s|[,.:;!-])+|$)`)
	thatLead     = regexp.MustCompile(`(?i)^(?:dass|daß|that)(?:(?:\s|[,:;-])+|$)`)
	fillerLead   = regexp.MustCompile(`(?i)^(?:bitte|mal|doch|please)(?:(?:\s|[,:;-])+|$)`)
	sepLead      = regexp.MustCompile(`^(?:\s|[,.:;!-])+`)
	barePronouns = wordSet("das", "dies", "es", "this", "that", "it", "bitte", "please", "mal", "auch", "too", "also")
)

// stripCommand removes greetings, the wake word and polite fillers around a
// command so the matchers see the command itself.
func stripCommand(q string) string {
	q = strings.TrimSpace(q)
	for i := 0; i < 6; i++ {
		n := cmdLead.ReplaceAllString(q, "")
		if n == q {
			break
		}
		q = n
	}
	q = cmdTail.ReplaceAllString(q, "")
	return strings.TrimSpace(q)
}

// parseRememberRest strips scope phrases, "dass/that" and fillers from what
// follows the trigger.
func parseRememberRest(rest string) (string, bool) {
	household := false
	for i := 0; i < 5; i++ {
		before := rest
		rest = sepLead.ReplaceAllString(rest, "")
		rest = fillerLead.ReplaceAllString(rest, "")
		if scopeLead.MatchString(rest) {
			rest = scopeLead.ReplaceAllString(rest, "")
			household = true
		}
		rest = thatLead.ReplaceAllString(rest, "")
		if rest == before {
			break
		}
	}
	rest = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(rest), ".!"))
	return rest, household
}

func isBarePronoun(rest string) bool {
	toks := tokenize(rest)
	if len(toks) == 0 {
		return false
	}
	for _, w := range toks {
		if !barePronouns[w] {
			return false
		}
	}
	return true
}

func matchRemember(t *Turn) (json.RawMessage, bool) {
	q := stripCommand(t.Query)
	var rest string
	if m := rememberRe.FindStringSubmatch(q); m != nil {
		rest = m[1]
	} else if m := rememberEnRe.FindStringSubmatch(q); m != nil && !strings.HasSuffix(q, "?") {
		rest = m[1] + m[2]
	} else {
		return nil, false
	}
	rest, household := parseRememberRest(rest)
	args := rememberArgs{Fact: rest, Household: household}
	if isBarePronoun(rest) {
		args = rememberArgs{Bare: true, Household: household}
	}
	b, _ := json.Marshal(args)
	return b, true
}

// previousUserMessage is this speaker's previous typed/spoken message (the
// history is already filtered to the speaker); link markers are skipped.
func previousUserMessage(t *Turn) string {
	for i := len(t.Tagged) - 2; i >= 0; i-- {
		m := t.Tagged[i]
		if m.Role == "user" && !m.Att {
			return m.Content
		}
	}
	if len(t.Tagged) == 0 {
		for i := len(t.History) - 2; i >= 0; i-- {
			if t.History[i].Role == "user" {
				return t.History[i].Content
			}
		}
	}
	return ""
}

func memoryDisabledReply(t *Turn) Reply {
	return Reply{Text: pick(t.Lang,
		"My memory is switched off. You can turn it on in Settings.",
		"Mein Gedächtnis ist ausgeschaltet. Du kannst es in den Einstellungen einschalten.")}
}

func guestMemoryRefusal(t *Turn) Reply {
	if t.Speaker.Source == SourceGuest || t.Speaker.Source == "" {
		return Reply{Text: pick(t.Lang,
			"I only remember things for people I know. Introduce yourself with “I'm …”.",
			"Ich merke mir etwas nur für Personen, die ich kenne. Stell dich mit „Ich bin …“ vor.")}
	}
	return refuse(t, "memory_remember", nil)
}

// memoryWriteAccess: guests get the dedicated "Ich merke mir etwas nur …"
// line from Run (which re-checks), everyone else goes through the central
// check (uncertain speakers are asked who is talking).
func memoryWriteAccess(t *Turn, _ json.RawMessage) Access {
	if t.Speaker.Source == SourceGuest {
		return AccessOpen
	}
	return AccessWrite
}

func askWhatToRemember(t *Turn, household bool) Reply {
	args, _ := json.Marshal(rememberArgs{Household: household})
	return t.Ask(Pending{Kind: "slot", Slot: "item", Tool: "memory_remember", Args: args,
		Resume: func(t *Turn, p *Pending) (Reply, bool) {
			if len(t.Tokens) == 0 || isQuestion(t) || len(t.Tokens) > 60 {
				return Reply{}, false
			}
			return runToolChecked(t, p.Tool, mergeArg(p.Args, "fact", t.Query)), true
		}},
		pick(t.Lang, "What should I remember?", "Was soll ich mir merken?"))
}

func runRemember(t *Turn, raw json.RawMessage) Reply {
	if !t.Settings.MemoryEnabled {
		return memoryDisabledReply(t)
	}
	if !t.Can(AccessWrite) {
		return guestMemoryRefusal(t)
	}
	var a rememberArgs
	_ = json.Unmarshal(raw, &a)
	household := a.Household || strings.EqualFold(a.Scope, "household")
	statement := strings.TrimSpace(a.Fact)
	if statement == "" {
		statement = strings.TrimSpace(a.Item)
	}
	if statement != "" && !a.Bare {
		var hh bool
		statement, hh = parseRememberRest(statement)
		household = household || hh
		if isBarePronoun(statement) {
			a.Bare = true
		}
	}
	if a.Bare {
		statement = strings.TrimSpace(strings.TrimRight(previousUserMessage(t), ".!"))
	}
	if len([]rune(statement)) < 3 {
		return askWhatToRemember(t, household)
	}
	statement = cleanText(statement, 600)

	scope := t.MemoryScope()
	if household {
		scope = ""
	} else if scope == "" {
		return guestMemoryRefusal(t)
	}
	lang := detectLang(statement, t.Lang)
	name := speakerLabel(t, lang)
	fact, readback, kind := rewriteFact(t, statement, name, lang)
	if household {
		readback = fact // nothing to address: "Die Katze heißt Mochi."
	}
	saved, replaced, err := saveFact(t.Ctx, t.Settings, Memory{
		Person: scope, Text: fact, Kind: kind, Source: "explicit", ClientID: t.ClientID, Lang: lang,
	}, nil)
	if err != nil {
		return memoryErrorReply(t, err)
	}
	label := pick(t.Lang, "the note “%s”", "Eintrag „%s“")
	if replaced != nil {
		old := *replaced
		oldModel, oldVec := vectorsRaw(old.ID)
		t.PushUndo(fmt.Sprintf(label, truncateRunes(saved.Text, 60)), func(context.Context) error {
			if err := memoryRestore(old); err != nil {
				return err
			}
			vectorsRestore(old.ID, oldModel, oldVec)
			return nil
		})
	} else {
		id := saved.ID
		t.PushUndo(fmt.Sprintf(label, truncateRunes(saved.Text, 60)), func(context.Context) error {
			_, err := memoryDelete(id)
			return err
		})
	}
	publishMemorySaved(scope, t.ClientID, saved.ID)
	return Reply{
		Text: pick(t.Lang, "Noted: ", "Gemerkt: ") + readback,
		Card: &Card{Kind: "memory", Data: map[string]any{"id": saved.ID, "text": saved.Text, "person": saved.Person}},
	}
}

func memoryErrorReply(t *Turn, err error) Reply {
	switch {
	case errors.Is(err, errMemoryLimit):
		return Reply{Text: pick(t.Lang,
			"My memory is full. Please delete a few entries in the Memory tab.",
			"Mein Gedächtnis ist voll. Lösch bitte ein paar Einträge im Gedächtnis-Tab."), Error: true}
	case errors.Is(err, errStoreReadOnly):
		return Reply{Text: pick(t.Lang,
			"I can't change my memory right now: the data file is from a newer HERMES.",
			"Ich kann mein Gedächtnis gerade nicht ändern: Die Datendatei stammt von einer neueren HERMES-Version."), Error: true}
	case errors.Is(err, errPersonGone):
		return Reply{Text: pick(t.Lang, "That person no longer exists.", "Diese Person gibt es nicht mehr."), Error: true}
	}
	log.Printf("memory: %v", err)
	return Reply{Text: pick(t.Lang, "I couldn't save that.", "Das konnte ich mir nicht merken."), Error: true}
}

// saveFact stores m, replacing a near-duplicate or a contradicted memory in
// the same scope (SPEC 9.B "Contradiction"). It returns the saved memory and,
// when one was replaced, its previous version. skip excludes ids from the
// similarity search (e.g. ones just saved in the same batch).
func saveFact(ctx context.Context, s *Settings, m Memory, skip map[string]bool) (Memory, *Memory, error) {
	var vec []float32
	var model string
	if embedModelFor(s) != "" {
		ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), memoryInteractiveEmbed)
		v, mdl, err := embedOne(ectx, s, embedDoc, m.Text)
		cancel()
		if err == nil {
			vec, model = v, mdl
		}
	}
	vecs := vectorsFor(model)
	if old, ok, _ := similarMemory(m.Person, m.Text, vec, vecs, skip); ok {
		before, after, err := memoryModify(old.ID, func(x *Memory) error {
			x.Text, x.Lang, x.UpdatedAt = m.Text, m.Lang, nowFunc().UTC()
			if m.Kind != "other" {
				x.Kind = m.Kind
			}
			if m.Source == "explicit" {
				x.Source, x.ClientID = m.Source, m.ClientID
			}
			if m.Expires != "" {
				x.Expires = m.Expires
			}
			return nil
		})
		if err == nil {
			if vec != nil {
				vectorsSet(model, map[string][]float32{after.ID: vec})
			} else {
				vectorsDelete([]string{after.ID})
			}
			return after, &before, nil
		}
		if !errors.Is(err, errMemoryNotFound) {
			return Memory{}, nil, err
		}
	}
	saved, err := memoryInsert(m)
	if err != nil {
		return Memory{}, nil, err
	}
	if vec != nil {
		vectorsSet(model, map[string][]float32{saved.ID: vec})
	}
	return saved, nil, nil
}

// publishMemorySaved tells the device that made the request (ids only, no
// text: the UI fetches texts through the lockable API).
func publishMemorySaved(scope, clientID string, ids ...string) {
	if len(ids) == 0 {
		return
	}
	hubPublish("memory.saved", scope, map[string]any{
		"person": scope, "count": len(ids), "ids": ids, "clientId": clientID,
	})
}

// ---------------------------------------------------------------------------
// "Vergiss …" (fast path 31, router tool "forget")

type forgetArgs struct {
	Mode  string `json:"mode"` // "last" | "all" | "all_confirmed" | "query" | "id"
	Query string `json:"query"`
	ID    string `json:"id"`
}

var (
	forgetAllRe   = regexp.MustCompile(`(?i)^(?:vergiss|forget)\s+(?:bitte\s+|please\s+)?(?:alles|everything)\s+(?:über|ueber|about|von|zu)\s+(?:mich|me)\s*[.!]*$`)
	forgetLastRe  = regexp.MustCompile(`(?i)^(?:vergiss|forget)\s+(?:das|that)(?:\s+(?:wieder|bitte|please|again|lieber|doch))*\s*[.!]*$`)
	forgetQueryRe = regexp.MustCompile(`(?is)^(?:vergiss|forget)(?:\s+(?:bitte|please))?(?:\s*,?\s*(?:dass|daß|that)|\s+das\s+mit|\s+(?:about|alles\s+(?:über|zu)|was\s+du\s+(?:über|zu)\s+\S+\s+weißt\s*,?))\s+(.+?)\s*[.!]*$`)
)

func matchForget(t *Turn) (json.RawMessage, bool) {
	q := stripCommand(t.Query)
	// "vergiss die Erinnerung / den Timer", "forget the reminder" cancel a
	// reminder (reminders.go reminder_cancel), they are not memories
	if isCancelRequest(t.Tokens) {
		return nil, false
	}
	var a forgetArgs
	switch {
	case forgetAllRe.MatchString(q):
		a.Mode = "all"
	case forgetLastRe.MatchString(q):
		a.Mode = "last"
	default:
		m := forgetQueryRe.FindStringSubmatch(q)
		if m == nil {
			return nil, false
		}
		rest := strings.TrimSpace(m[1])
		if isBarePronoun(rest) {
			a.Mode = "last"
		} else {
			a.Mode, a.Query = "query", rest
		}
	}
	b, _ := json.Marshal(a)
	return b, true
}

// forgettable are the memories this speaker may delete by voice: their own
// scopes plus the household.
func forgettable(t *Turn) []Memory {
	scopes := append(personalScopes(t), "")
	today := memoryToday(t.Settings)
	ms := memoryList(func(m *Memory) bool { return inScopes(m.Person, scopes) && !memoryExpired(m, today) })
	sortMemories(ms)
	return ms
}

func runForget(t *Turn, raw json.RawMessage) Reply {
	if !t.Settings.MemoryEnabled {
		return memoryDisabledReply(t)
	}
	if !t.Can(AccessWrite) || t.MemoryScope() == "" {
		return refuse(t, "memory_forget", raw)
	}
	var a forgetArgs
	_ = json.Unmarshal(raw, &a)
	if a.Mode == "" {
		a.Mode = "query"
		if strings.TrimSpace(a.Query) == "" || isBarePronoun(a.Query) {
			a.Mode = "last"
		}
	}
	nothing := Reply{Text: pick(t.Lang, "I haven't noted anything about you yet.", "Ich habe mir über dich noch nichts gemerkt.")}
	switch a.Mode {
	case "last":
		since := nowFunc().Add(-10 * time.Minute)
		own := personalScopes(t)
		var newest *Memory
		for _, m := range forgettable(t) {
			if m.ClientID == t.ClientID && t.ClientID != "" && (inScopes(m.Person, own) || m.Person == "") &&
				m.Source != "manual" && m.CreatedAt.After(since) && (newest == nil || m.CreatedAt.After(newest.CreatedAt)) {
				m := m
				newest = &m
			}
		}
		if newest != nil {
			return forgetOne(t, *newest)
		}
		ms := memoryList(func(m *Memory) bool { return inScopes(m.Person, own) })
		if len(ms) == 0 {
			return nothing
		}
		sort.SliceStable(ms, func(i, j int) bool { return ms[i].CreatedAt.After(ms[j].CreatedAt) })
		return askForgetWhich(t, ms[0])
	case "all":
		n := len(memoryList(func(m *Memory) bool { return m.Person == t.MemoryScope() }))
		if n == 0 {
			return nothing
		}
		args, _ := json.Marshal(forgetArgs{Mode: "all_confirmed"})
		return t.Ask(Pending{Kind: "confirm", Tool: "memory_forget", Args: args},
			pick(t.Lang, fmt.Sprintf("Should I forget all %d entries about you?", n),
				fmt.Sprintf("Soll ich alle %d Einträge über dich vergessen?", n)))
	case "all_confirmed":
		scope := t.MemoryScope()
		gone, err := memoryDeleteWhere(func(m *Memory) bool { return m.Person == scope })
		if err != nil {
			return memoryErrorReply(t, err)
		}
		if len(gone) == 0 {
			return nothing
		}
		restore := append([]Memory(nil), gone...)
		s := t.Settings
		t.PushUndo(pick(t.Lang, fmt.Sprintf("forgetting %d entries", len(gone)), fmt.Sprintf("Vergessen von %d Einträgen", len(gone))),
			func(ctx context.Context) error {
				var first error
				for _, m := range restore {
					if err := memoryRestore(m); err != nil && first == nil {
						first = err
					}
				}
				embedMemoriesNow(ctx, s, restore...)
				return first
			})
		return Reply{Text: pick(t.Lang, fmt.Sprintf("Done. I forgot %d entries about you.", len(gone)),
			fmt.Sprintf("Erledigt. Ich habe %d Einträge über dich vergessen.", len(gone)))}
	case "id":
		for _, m := range forgettable(t) {
			if m.ID == a.ID {
				return forgetOne(t, m)
			}
		}
		return Reply{Text: pick(t.Lang, "That entry no longer exists.", "Diesen Eintrag gibt es nicht mehr.")}
	}
	return forgetByQuery(t, a.Query)
}

func askForgetWhich(t *Turn, m Memory) Reply {
	args, _ := json.Marshal(forgetArgs{Mode: "id", ID: m.ID})
	text := toSecondPerson(m.Text, scopeLabel(m.Person, m.Lang), m.Lang)
	return t.Ask(Pending{Kind: "confirm", Tool: "memory_forget", Args: args},
		pick(t.Lang, fmt.Sprintf("Do you mean: “%s”?", text), fmt.Sprintf("Meinst du: ‚%s‘?", text)))
}

func forgetOne(t *Turn, m Memory) Reply {
	model, enc := vectorsRaw(m.ID)
	if _, err := memoryDelete(m.ID); err != nil {
		if errors.Is(err, errMemoryNotFound) {
			return Reply{Text: pick(t.Lang, "That entry no longer exists.", "Diesen Eintrag gibt es nicht mehr.")}
		}
		return memoryErrorReply(t, err)
	}
	text := toSecondPerson(m.Text, scopeLabel(m.Person, m.Lang), m.Lang)
	t.PushUndo(pick(t.Lang, fmt.Sprintf("forgetting “%s”", truncateRunes(text, 60)), fmt.Sprintf("Vergessen von „%s“", truncateRunes(text, 60))),
		func(context.Context) error {
			if err := memoryRestore(m); err != nil {
				return err
			}
			vectorsRestore(m.ID, model, enc)
			return nil
		})
	return Reply{Text: pick(t.Lang, "Forgotten: ", "Vergessen: ") + text,
		Card: &Card{Kind: "memory", Data: map[string]any{"id": m.ID, "deleted": true, "person": m.Person}}}
}

type scoredMemory struct {
	m     Memory
	score float64
}

// rankMemories scores candidates against a query by embedding cosine when
// every candidate has a vector, else lexically. embedded reports which.
func rankMemories(ctx context.Context, s *Settings, query string, cands []Memory, timeout time.Duration) (out []scoredMemory, embedded bool) {
	if len(cands) == 0 {
		return nil, false
	}
	if embedModelFor(s) != "" && !embedMissing(embedModelFor(s)) {
		ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		qv, model, err := embedOne(ectx, s, embedQuery, query)
		cancel()
		if err == nil && qv != nil {
			vecs := vectorsFor(model)
			all := true
			for _, m := range cands {
				if vecs[m.ID] == nil {
					all = false
					break
				}
			}
			if all {
				for _, m := range cands {
					out = append(out, scoredMemory{m, cosine32(qv, vecs[m.ID])})
				}
				embedded = true
			}
		}
	}
	if !embedded {
		out = out[:0]
		for _, m := range cands {
			ex := nameExclusions(append([]string{scopeLabel(m.Person, m.Lang)}, genericUserNames...)...)
			out = append(out, scoredMemory{m, lexScoreExcl(query, m.Text, ex)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].m.UpdatedAt.After(out[j].m.UpdatedAt)
	})
	return out, embedded
}

func forgetByQuery(t *Turn, query string) Reply {
	cands := forgettable(t)
	none := Reply{Text: pick(t.Lang, "I have nothing stored about that.", "Dazu habe ich nichts gespeichert.")}
	if len(cands) == 0 || strings.TrimSpace(query) == "" {
		return none
	}
	ranked, embedded := rankMemories(t.Ctx, t.Settings, query, cands, memoryInteractiveEmbed)
	sure, maybe := 0.5, 0.3
	if embedded {
		sure, maybe = 0.75, 0.5
	}
	top := ranked[0]
	second := 0.0
	if len(ranked) > 1 {
		second = ranked[1].score
	}
	switch {
	case top.score >= sure && top.score-second >= 0.05:
		return forgetOne(t, top.m)
	case top.score >= maybe:
		return askForgetWhich(t, top.m)
	}
	return none
}

// ---------------------------------------------------------------------------
// "Was weißt du über mich" (fast path 32)

var recallPhrases = []string{
	"was weißt du über mich", "was weisst du über mich", "was weißt du von mir", "was weisst du von mir",
	"was hast du dir über mich gemerkt", "was hast du dir gemerkt", "was hast du über mich gespeichert",
	"what do you know about me", "what do you remember about me", "what have you remembered about me",
	"what have you saved about me", "what do you know of me",
}

var recallFillers = wordSet("hermes", "hey", "eigentlich", "alles", "so", "denn", "bitte", "mal", "please",
	"actually", "all", "else", "noch", "schon", "already")

func matchRecall(t *Turn) (json.RawMessage, bool) {
	var toks []string
	for _, w := range t.Tokens {
		if !recallFillers[w] {
			toks = append(toks, w)
		}
	}
	if len(toks) == 0 || len(toks) > 10 {
		return nil, false
	}
	for _, p := range recallPhrases {
		if hasPhrase(toks, p) && len(toks) <= len(strings.Fields(p))+2 {
			return json.RawMessage(`{}`), true
		}
	}
	return nil, false
}

func runRecall(t *Turn, _ json.RawMessage) Reply {
	if !t.Settings.MemoryEnabled {
		return memoryDisabledReply(t)
	}
	if !t.Can(AccessRead) {
		return refuse(t, "memory_recall", nil)
	}
	today := memoryToday(t.Settings)
	scopes := personalScopes(t)
	own := memoryList(func(m *Memory) bool { return inScopes(m.Person, scopes) && !memoryExpired(m, today) })
	house := memoryList(func(m *Memory) bool { return m.Person == "" && !memoryExpired(m, today) })
	sortMemories(own)
	sortMemories(house)
	if len(own) == 0 && len(house) == 0 {
		return Reply{Text: pick(t.Lang, "I haven't noted anything about you yet.", "Ich habe mir über dich noch nichts gemerkt.")}
	}
	var mine, shared []string
	var used []string
	for _, m := range own {
		if len(mine) >= memoryRecallMax {
			break
		}
		mine = append(mine, endSentence(toSecondPerson(memSafe(m.Text), scopeLabel(m.Person, m.Lang), m.Lang)))
		used = append(used, m.ID)
	}
	for _, m := range house {
		if len(mine)+len(shared) >= memoryRecallMax {
			break
		}
		shared = append(shared, endSentence(memSafe(m.Text)))
		used = append(used, m.ID)
	}
	noteUsage(used)
	var b strings.Builder
	if len(mine) > 0 {
		b.WriteString(pick(t.Lang, "Here's what I know about you: ", "Ich weiß über dich: "))
		b.WriteString(strings.Join(mine, " "))
	} else {
		b.WriteString(pick(t.Lang, "I haven't noted anything about you yet.", "Über dich habe ich mir noch nichts gemerkt."))
	}
	if len(shared) > 0 {
		b.WriteString(pick(t.Lang, " For the household: ", " Für den Haushalt: "))
		b.WriteString(strings.Join(shared, " "))
	}
	if more := len(own) + len(house) - len(mine) - len(shared); more > 0 {
		b.WriteString(pick(t.Lang, fmt.Sprintf(" And %d more in the Memory tab.", more),
			fmt.Sprintf(" Und %d weitere Einträge im Gedächtnis-Tab.", more)))
	}
	return Reply{Text: b.String()}
}

// ---------------------------------------------------------------------------
// Personal context section (order 20; local model only)

// memoryContextEmbedTimeout bounds the query embedding of a persona turn.
var memoryContextEmbedTimeout = 1500 * time.Millisecond

func memoryContext(t *Turn) string {
	s := t.Settings
	if s == nil || !s.MemoryEnabled {
		return ""
	}
	var scopes []string
	if t.Can(AccessRead) {
		scopes = personalScopes(t)
	}
	house := householdVisible(t)
	if len(scopes) == 0 && !house {
		return ""
	}
	today := memoryToday(s)
	cands := memoryList(func(m *Memory) bool {
		return (inScopes(m.Person, scopes) || (house && m.Person == "")) && !memoryExpired(m, today)
	})
	if len(cands) == 0 {
		return ""
	}
	sortMemories(cands)
	chosen := cands
	if len(cands) > memoryContextAll {
		var pins, rest []Memory
		for _, m := range cands {
			if m.Pinned && len(pins) < memoryContextPins {
				pins = append(pins, m)
			} else {
				rest = append(rest, m)
			}
		}
		ranked, _ := rankMemories(t.Ctx, s, t.Query, rest, memoryContextEmbedTimeout)
		chosen = pins
		for i := 0; i < len(ranked) && i < memoryContextTop; i++ {
			chosen = append(chosen, ranked[i].m)
		}
	}
	de := t.Lang == "de"
	name := pick(t.Lang, "the user", "den Nutzer")
	if t.Speaker.Known() {
		name = speakerLabel(t, t.Lang)
	}
	var personal, household []string
	var used []string
	budget := memoryContextChars
	for _, m := range chosen {
		line := "- " + memSafe(m.Text)
		if len(line)+1 > budget {
			break
		}
		budget -= len(line) + 1
		if m.Person == "" {
			household = append(household, line)
		} else {
			personal = append(personal, line)
		}
		used = append(used, m.ID)
	}
	noteUsage(used)
	var b strings.Builder
	if len(personal) > 0 {
		if de {
			fmt.Fprintf(&b, "<<<DATEN: Gedächtnis über %s (nur verwenden, wenn es zur Frage passt; nicht ungefragt erwähnen)>>>\n", memSafe(name))
		} else {
			fmt.Fprintf(&b, "<<<DATA: Memory about %s (use only when it fits the question; do not bring it up unprompted)>>>\n", memSafe(name))
		}
		b.WriteString(strings.Join(personal, "\n"))
		b.WriteString(pick(t.Lang, "\n<<<END>>>", "\n<<<ENDE>>>"))
	}
	if len(household) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(pick(t.Lang, "<<<DATA: Household>>>\n", "<<<DATEN: Haushalt>>>\n"))
		b.WriteString(strings.Join(household, "\n"))
		b.WriteString(pick(t.Lang, "\n<<<END>>>", "\n<<<ENDE>>>"))
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Automatic extraction (after persona turns, background queue)

type extractJob struct {
	Scope    string // memory scope of the speaker ("p_…" or "_owner")
	Name     string // how facts name the speaker
	ClientID string
	Text     string // the user's message only, never the reply (SPEC 8.3)
	Lang     string // language of the message
	Enqueued time.Time
}

const memoryQueueSize = 16

var (
	memoryQueue = make(chan extractJob, memoryQueueSize)
	// Worker timing (SPEC 9.B "Scheduling"); tests shorten them.
	memoryExtractDelay   = 3 * time.Second
	memoryIdleHold       = 1500 * time.Millisecond
	memoryIdlePoll       = 100 * time.Millisecond
	memoryExtractTimeout = 60 * time.Second
	memoryMaintainEvery  = time.Hour
	// memoryJobHook observes finished jobs (tests).
	memoryJobHook func(job extractJob, saved []Memory, err error)
)

// skipSources: replies built from untrusted or private material; a fact
// "stated" right after them may have been planted there (SPEC 8.3).
func extractionSkipSource(src string) bool {
	switch {
	case src == "local:link", src == "local:docs", src == "fast:briefing", src == "realtime:news":
		return true
	case strings.HasPrefix(src, "tool:calendar"), strings.HasPrefix(src, "fast:calendar"):
		return true
	}
	return false
}

var firstPersonMarkers = wordSet("ich", "mein", "meine", "meinen", "meinem", "meiner", "meines", "mir", "mich",
	"wir", "unser", "unsere", "unseren", "unserem", "unserer", "uns", "i", "my", "me", "mine", "we", "our", "us", "im")

// extractionWanted applies every skip rule of SPEC 9.B / 8.3.
func extractionWanted(t *Turn) bool {
	s := t.Settings
	if s == nil || !s.MemoryEnabled || !s.MemoryAuto {
		return false
	}
	if t.handledBy != "persona" || t.Attachment != nil || !t.Can(AccessExtract) || t.MemoryScope() == "" {
		return false
	}
	if len(t.Tokens) < 3 {
		return false
	}
	if isQuestion(t) {
		first := false
		for _, w := range t.Tokens {
			if firstPersonMarkers[w] {
				first = true
				break
			}
		}
		if !first {
			return false
		}
	}
	if n := len(t.Tagged); n > 0 && t.Tagged[n-1].Att {
		return false
	}
	return !recentUntrustedReply(t)
}

// recentUntrustedReply reports whether one of the last two assistant turns
// was built from untrusted or private material (extractionSkipSource). Such
// a turn blocks extraction, and the persona answer that follows gets no
// personal context, so instructions planted in a page, a document or a
// calendar invite never share a prompt with the household's memories
// (SPEC 8.3).
func recentUntrustedReply(t *Turn) bool {
	seen := 0
	for i := len(t.Tagged) - 2; i >= 0 && seen < 2; i-- {
		if t.Tagged[i].Role != "assistant" {
			continue
		}
		seen++
		if extractionSkipSource(t.Tagged[i].Src) {
			return true
		}
	}
	return false
}

// memoryAfterTurn queues an extraction job; a full queue drops the job (the
// next persona turn gets another chance).
func memoryAfterTurn(t *Turn, _ string) {
	if !extractionWanted(t) {
		return
	}
	lang := detectLang(t.Query, t.Lang)
	job := extractJob{
		Scope: t.MemoryScope(), Name: speakerLabel(t, lang), ClientID: t.ClientID,
		Text: truncateRunes(t.Query, 2000), Lang: lang, Enqueued: time.Now(),
	}
	select {
	case memoryQueue <- job:
	default:
		log.Printf("memory: extraction queue full; dropped one job")
	}
}

// waitChatIdle returns once no chat turn has run for hold, or ctx ends. Doc
// indexing shares this gate so background model work never slows a reply.
func waitChatIdle(ctx context.Context, hold time.Duration) error {
	var idleSince time.Time
	tick := time.NewTicker(memoryIdlePoll)
	defer tick.Stop()
	for {
		if chatInFlight.Load() == 0 {
			if idleSince.IsZero() {
				idleSince = time.Now()
			}
			if time.Since(idleSince) >= hold {
				return nil
			}
		} else {
			idleSince = time.Time{}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// memoryWorker is the single background goroutine: extraction jobs, the
// daily purge, usage counters and vector backfill. main() starts it.
func memoryWorker(ctx context.Context) {
	maint := time.NewTicker(memoryMaintainEvery)
	defer maint.Stop()
	lastPurge := ""
	maintain := func() {
		if waitChatIdle(ctx, memoryIdleHold) != nil {
			return
		}
		s := loadSettings()
		if today := memoryToday(s); today != lastPurge {
			if n, err := purgeExpiredMemories(s); err == nil {
				lastPurge = today
				if n > 0 {
					log.Printf("memory: purged %d expired entries", n)
				}
			}
		}
		flushUsage()
		backfillVectors(ctx, s)
	}
	maintain()
	for {
		select {
		case <-ctx.Done():
			flushUsage()
			return
		case <-maint.C:
			maintain()
		case job := <-memoryQueue:
			if wait := memoryExtractDelay - time.Since(job.Enqueued); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			if waitChatIdle(ctx, memoryIdleHold) != nil {
				return
			}
			jctx, cancel := context.WithTimeout(ctx, memoryExtractTimeout)
			saved, err := processExtraction(jctx, job)
			cancel()
			if err != nil && !errors.Is(err, errPersonGone) {
				log.Printf("memory: extraction failed: %v", err)
			}
			if fn := memoryJobHook; fn != nil {
				fn(job, saved, err)
			}
		}
	}
}

// purgeExpiredMemories deletes memories whose expiry day has come.
func purgeExpiredMemories(s *Settings) (int, error) {
	today := memoryToday(s)
	gone, err := memoryDeleteWhere(func(m *Memory) bool { return memoryExpired(m, today) })
	if err != nil {
		return 0, err
	}
	// Vectors of memories that no longer exist (crash between the two files).
	ids := map[string]bool{}
	for _, m := range memoryList(nil) {
		ids[m.ID] = true
	}
	_ = vectorsStore.Update(func(st *vectorsFile) error {
		n := 0
		for id := range st.Vectors {
			if !ids[id] {
				delete(st.Vectors, id)
				n++
			}
		}
		if n == 0 {
			return errNoChange
		}
		return nil
	})
	return len(gone), nil
}

// backfillVectors embeds memories without a vector for the current model
// (new memories saved while Ollama was down, or after a model change).
func backfillVectors(ctx context.Context, s *Settings) {
	model := embedModelFor(s)
	if model == "" || embedMissing(model) {
		return
	}
	have := vectorsFor(model)
	var todo []Memory
	for _, m := range memoryList(nil) {
		if have[m.ID] == nil {
			todo = append(todo, m)
		}
	}
	for len(todo) > 0 && ctx.Err() == nil {
		if waitChatIdle(ctx, memoryIdleHold) != nil {
			return
		}
		n := min(len(todo), embedMaxBatch)
		before := len(vectorsFor(model))
		embedMemoryTexts(ctx, s, todo[:n])
		if len(vectorsFor(model)) == before {
			return // embedding failed; try again next maintenance round
		}
		todo = todo[n:]
	}
}

// The extraction prompt is v2 from design/local-llm/memory_bench_v2.py, with
// the "exchange" wording changed to the speaker's message alone (the reply is
// never sent, SPEC 8.3), plus the language and "replaces" lines.
const extractSystemTemplate = `You maintain the long-term memory of HERMES, a home voice assistant. Today is %[1]s.
Given ONE message from the speaker and the memories already stored, output NEW durable facts worth remembering for future conversations.
Store ONLY facts the SPEAKER stated about themselves, their family/household, pets, home, preferences, important dates (birthdays, anniversaries), health/allergies, work, or concrete future plans.
NEVER store: greetings, small talk, questions the speaker asked, general knowledge, things HERMES said, momentary states (hungry, tired, bored), one-off actions already handled (timers, lists, light control), opinions about trivia, or anything already in the stored memories (also not reworded).
Write each fact as one short third-person sentence in %[2]s using the speaker's name if known (e.g. %[3]s). about="household" only for facts about the shared home (pets, wifi, appliances), otherwise "speaker".
For plans set "expires" to the day after the plan date; otherwise "".
If there is nothing durable, return {"facts": []}.
Write facts in %[2]s (the language of the speaker's message), even when the stored memories are in another language.
If a fact corrects one of the existing memories, set replaces to its id.`

var extractSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"facts": map[string]any{
			"type": "array", "maxItems": 3,
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"fact":     map[string]any{"type": "string"},
					"about":    map[string]any{"type": "string", "enum": []string{"speaker", "household"}},
					"kind":     map[string]any{"type": "string", "enum": []string{"identity", "preference", "relationship", "date", "plan", "health", "home", "other"}},
					"expires":  map[string]any{"type": "string", "description": "ISO date when the fact stops being relevant, or empty"},
					"replaces": map[string]any{"type": "string", "description": "id of an existing memory this fact corrects, or empty"},
				},
				"required": []string{"fact", "about", "kind", "expires"},
			},
		},
	},
	"required": []string{"facts"},
}

type extractedFact struct {
	Fact     string `json:"fact"`
	About    string `json:"about"`
	Kind     string `json:"kind"`
	Expires  string `json:"expires"`
	Replaces string `json:"replaces"`
}

// humanNow: "Friday, 6 November 2026, 14:30 (Europe/Berlin, UTC+01:00)".
func humanNow(now time.Time, loc *time.Location) string {
	n := now.In(loc)
	return n.Format("Monday, 2 January 2006, 15:04") + " (" + loc.String() + ", UTC" + n.Format("-07:00") + ")"
}

// extractionMessages builds the request: the speaker's message plus the
// stored memories for dedupe, never HERMES' reply.
func extractionMessages(s *Settings, job extractJob, existing []Memory) []chatMessage {
	// The example and a closing reminder are in the message's language: with
	// a German example and German stored memories, the model wrote English
	// messages' facts in German (and the overlap guard then dropped them).
	lang, example, reminder := "English", `"Anna is allergic to nuts."`, "Write the facts in English."
	if job.Lang == "de" {
		lang, example, reminder = "German", `"Anna ist allergisch gegen Nüsse."`, "Schreibe die Fakten auf Deutsch."
	}
	sys := fmt.Sprintf(extractSystemTemplate, humanNow(nowFunc(), settingsLocation(s)), lang, example)
	var b strings.Builder
	b.WriteString("Existing memories (id: text):\n")
	if len(existing) == 0 {
		b.WriteString("(none)\n")
	}
	for _, m := range existing {
		fmt.Fprintf(&b, "%s: %s\n", m.ID, memSafe(m.Text))
	}
	fmt.Fprintf(&b, "\nSpeaker: %s\nSpeaker said: %s\n\n%s", memSafe(job.Name), strings.TrimSpace(job.Text), reminder)
	return []chatMessage{{Role: "system", Content: sys}, {Role: "user", Content: b.String()}}
}

// callExtractor runs the extraction request. MLX builds answer 501 to
// "format": retry once without it and parse the first JSON object.
func callExtractor(ctx context.Context, s *Settings, msgs []chatMessage) ([]extractedFact, error) {
	req := ollamaChatRequest{Model: extractModelFor(s), Messages: msgs, Format: extractSchema, Temperature: 0, NumPredict: 300}
	res, err := ollamaChatOnce(ctx, s, req)
	if err != nil && errors.Is(err, errOllamaStatus) && strings.Contains(err.Error(), "/api/chat 501") {
		req.Format = nil
		req.Messages = append([]chatMessage(nil), msgs...)
		req.Messages[0].Content += "\nAnswer with JSON only, exactly in the shape {\"facts\": [{\"fact\": \"…\", \"about\": \"speaker|household\", \"kind\": \"…\", \"expires\": \"\", \"replaces\": \"\"}]}."
		res, err = ollamaChatOnce(ctx, s, req)
	}
	if err != nil {
		return nil, err
	}
	var out struct {
		Facts []extractedFact `json:"facts"`
	}
	if err := decodeFirstJSON(res.Content, &out); err != nil {
		return nil, fmt.Errorf("extractor answer is not JSON: %w", err)
	}
	return out.Facts, nil
}

// extractionContext: the speaker's own and household memories the model
// sees for dedupe and "replaces" (the most relevant 30).
func extractionContext(job extractJob, s *Settings) []Memory {
	today := memoryToday(s)
	ms := memoryList(func(m *Memory) bool { return (m.Person == job.Scope || m.Person == "") && !memoryExpired(m, today) })
	if len(ms) <= 30 {
		sortMemories(ms)
		return ms
	}
	sort.SliceStable(ms, func(i, j int) bool {
		a, b := lexScore(job.Text, ms[i].Text), lexScore(job.Text, ms[j].Text)
		if a != b {
			return a > b
		}
		return ms[i].UpdatedAt.After(ms[j].UpdatedAt)
	})
	return ms[:30]
}

// targetScope decides where an extracted fact lives (SPEC 9.B).
func targetScope(jobScope string, f extractedFact) string {
	if f.About != "household" || memoryPersonalKinds[f.Kind] {
		return jobScope
	}
	if jobScope == ownerScope && f.Kind != "home" {
		return ownerScope // single-user: only home facts become household
	}
	return ""
}

// processExtraction runs one job end to end and returns what it saved.
func processExtraction(ctx context.Context, job extractJob) ([]Memory, error) {
	s := loadSettings()
	if !s.MemoryEnabled || !s.MemoryAuto {
		return nil, nil
	}
	if !personExists(job.Scope) {
		log.Printf("memory: person gone; dropped an extraction job")
		return nil, errPersonGone
	}
	existing := extractionContext(job, s)
	facts, err := callExtractor(ctx, s, extractionMessages(s, job, existing))
	if err != nil {
		return nil, err
	}
	visible := map[string]Memory{}
	for _, m := range existing {
		visible[m.ID] = m
	}
	exclude := nameExclusions(append([]string{job.Name}, genericUserNames...)...)
	today := memoryToday(s)
	var saved []Memory
	skip := map[string]bool{}
	for i, f := range facts {
		if i >= 3 {
			break
		}
		f.Fact = cleanText(strings.Join(strings.Fields(f.Fact), " "), 1000)
		if f.Fact == "" || len([]rune(f.Fact)) > memoryMaxRunes {
			continue
		}
		if !memoryKinds[f.Kind] {
			f.Kind = "other"
		}
		if f.Kind == "health" && !s.MemoryAutoHealth {
			continue
		}
		if !overlapGuard(f.Fact, job.Text, exclude) {
			log.Printf("memory: dropped an extracted fact that the message does not support")
			continue
		}
		if detectLang(f.Fact, job.Lang) != job.Lang {
			log.Printf("memory: dropped an extracted fact in the wrong language")
			continue
		}
		expires, ok := validExpires(f.Expires, today)
		if !ok {
			expires = ""
		}
		scope := targetScope(job.Scope, f)
		text := endSentence(f.Fact)
		// replaces only within the fact's own scope: a personal fact never
		// overwrites a household entry (or the other way round).
		if old, ok := visible[strings.TrimSpace(f.Replaces)]; ok && !skip[old.ID] && old.Person == scope {
			_, after, err := memoryModify(old.ID, func(x *Memory) error {
				x.Text, x.Lang, x.UpdatedAt = text, job.Lang, nowFunc().UTC()
				x.Kind = f.Kind
				x.Expires = expires
				return nil
			})
			if err == nil {
				embedMemoryTexts(ctx, s, []Memory{after})
				saved = append(saved, after)
				skip[after.ID] = true
				continue
			}
			if !errors.Is(err, errMemoryNotFound) {
				return saved, err
			}
		}
		m, _, err := saveFact(ctx, s, Memory{
			Person: scope, Text: text, Kind: f.Kind, Source: "auto", ClientID: job.ClientID,
			Lang: job.Lang, Expires: expires,
		}, skip)
		if err != nil {
			if errors.Is(err, errPersonGone) {
				log.Printf("memory: person gone; extracted facts dropped")
				return saved, err
			}
			if errors.Is(err, errMemoryLimit) {
				log.Printf("memory: scope full; extracted fact dropped")
				continue
			}
			return saved, err
		}
		saved = append(saved, m)
		skip[m.ID] = true
	}
	byScope := map[string][]string{}
	var order []string
	for _, m := range saved {
		if _, ok := byScope[m.Person]; !ok {
			order = append(order, m.Person)
		}
		byScope[m.Person] = append(byScope[m.Person], m.ID)
	}
	for _, sc := range order {
		publishMemorySaved(sc, job.ClientID, byScope[sc]...)
	}
	return saved, nil
}

// Note: saveFact's similarity check doubles as the dedupe of SPEC 9.B: a
// same-polarity near-duplicate (cosine ≥ 0.90 / Jaccard ≥ 0.7) is refreshed
// instead of added twice, and an opposite-polarity match (≥ 0.85 / 0.6)
// replaces the older memory.

// ---------------------------------------------------------------------------
// Person deletion, migration and correction helpers (used by people.go)

// deletePersonMemories is the cascade hook (SPEC 4.8): their memories,
// vectors and every corrupt copy that may still hold them.
func deletePersonMemories(personID string) (string, int) {
	if personID == "" {
		return "memories", 0
	}
	gone, err := memoryDeleteWhere(func(m *Memory) bool { return m.Person == personID })
	if err != nil {
		log.Printf("memory: cascade delete failed: %v", err)
	}
	removeCorruptCopies(dataPath(memoriesStore.Name))
	removeCorruptCopies(dataPath(vectorsStore.Name))
	return "memories", len(gone)
}

// ownerMemoryStats reports the "_owner" memories still waiting for the
// migration dialog (GET /api/people/migration).
func ownerMemoryStats() (n int, byKind map[string]int) {
	byKind = map[string]int{}
	for _, m := range memoryList(func(m *Memory) bool { return m.Person == ownerScope }) {
		n++
		byKind[m.Kind]++
	}
	return n, byKind
}

// migrateOwnerMemories moves every "_owner" memory to person to, or with
// to == "household" to the household, except health, relationship and
// identity, which go to firstPerson (POST /api/people/migration).
func migrateOwnerMemories(to, firstPerson string) (int, error) {
	if to != "household" && !strings.HasPrefix(to, "p_") {
		return 0, fmt.Errorf("invalid migration target %q", to)
	}
	moved := 0
	err := memoriesStore.Update(func(st *memoriesFile) error {
		if to != "household" && !personExists(to) {
			return errPersonGone
		}
		if to == "household" && (!strings.HasPrefix(firstPerson, "p_") || !personExists(firstPerson)) {
			return errPersonGone
		}
		now := nowFunc().UTC()
		for _, m := range st.Memories {
			if m.Person != ownerScope {
				continue
			}
			switch {
			case to != "household":
				m.Person = to
			case memoryPersonalKinds[m.Kind]:
				m.Person = firstPerson
			default:
				m.Person = ""
			}
			m.UpdatedAt = now
			moved++
		}
		if moved == 0 {
			return errNoChange
		}
		return nil
	})
	return moved, err
}

// reassignRecentAutoMemories moves auto-extracted memories that this client
// created within the window under the mistaken person from to person to
// (speaker correction, SPEC 4.6). It returns the moved ids;
// moveMemories(ids, from) undoes it. from must be a person: guest turns never
// extract (4.10), so recent "" auto memories are household facts of a known
// speaker and never belong to the corrected person.
func reassignRecentAutoMemories(clientID, from, to string, within time.Duration) ([]string, error) {
	if clientID == "" || from == to || !strings.HasPrefix(from, "p_") {
		return nil, nil
	}
	since := nowFunc().Add(-within)
	var ids []string
	err := memoriesStore.Update(func(st *memoriesFile) error {
		if !personExists(to) {
			return errPersonGone
		}
		for _, m := range st.Memories {
			if m.Source == "auto" && m.ClientID == clientID && m.Person == from && m.CreatedAt.After(since) {
				m.Person = to
				ids = append(ids, m.ID)
			}
		}
		if len(ids) == 0 {
			return errNoChange
		}
		return nil
	})
	return ids, err
}

// moveMemories puts the given memories into scope (undo of a reassignment).
func moveMemories(ids []string, scope string) error {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	return memoriesStore.Update(func(st *memoriesFile) error {
		if !personExists(scope) {
			return errPersonGone
		}
		n := 0
		for _, m := range st.Memories {
			if want[m.ID] {
				m.Person = scope
				n++
			}
		}
		if n == 0 {
			return errNoChange
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// HTTP API (SPEC 3.4). Every endpoint sits behind requireUnlock.

const memoryBodyLimit = 64 << 10

func writeMemoryStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errStoreReadOnly):
		writeJSONErrorCode(w, http.StatusServiceUnavailable, "read_only", err.Error())
	case errors.Is(err, errMemoryNotFound):
		writeJSONErrorCode(w, http.StatusNotFound, "not_found", "unknown memory")
	case errors.Is(err, errMemoryLimit):
		writeJSONErrorCode(w, http.StatusConflict, "limit", "memory limit reached")
	case errors.Is(err, errPersonGone):
		writeJSONErrorCode(w, http.StatusBadRequest, "person", "unknown person")
	default:
		log.Printf("memory api: %v", err)
		writeJSONErrorCode(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

// apiScope maps the API's person parameter to a scope: "household" -> "".
func apiScope(p string) string {
	if p == "household" {
		return ""
	}
	return p
}

func validScope(scope string) bool {
	return scope == "" || scope == ownerScope || (strings.HasPrefix(scope, "p_") && personExists(scope))
}

func handleMemoriesList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var filter func(*Memory) bool
	switch {
	case q.Get("ids") != "":
		want := map[string]bool{}
		for i, id := range strings.Split(q.Get("ids"), ",") {
			if i >= 200 {
				break
			}
			want[strings.TrimSpace(id)] = true
		}
		filter = func(m *Memory) bool { return want[m.ID] }
	case q.Get("person") != "" && q.Get("person") != "all":
		scope := apiScope(q.Get("person"))
		filter = func(m *Memory) bool { return m.Person == scope }
	}
	ms := memoryList(filter)
	if ms == nil {
		ms = []Memory{}
	}
	sortMemories(ms)
	writeJSON(w, http.StatusOK, map[string]any{"memories": ms})
}

type memoryPatch struct {
	Text    *string `json:"text"`
	Person  *string `json:"person"`
	Kind    *string `json:"kind"`
	Pinned  *bool   `json:"pinned"`
	Expires *string `json:"expires"`
}

func decodeMemoryBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, memoryBodyLimit)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSONErrorCode(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large")
		} else {
			writeJSONErrorCode(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		}
		return false
	}
	return true
}

// checkPatch validates the fields of a create or update request.
func checkPatch(w http.ResponseWriter, p *memoryPatch, today string) (text, scope, expires string, ok bool) {
	if p.Text != nil {
		text = cleanText(strings.Join(strings.Fields(*p.Text), " "), memoryMaxRunes+1)
		if text == "" {
			writeJSONErrorCode(w, http.StatusBadRequest, "text", "text is empty")
			return "", "", "", false
		}
		if len([]rune(text)) > memoryMaxRunes {
			writeJSONErrorCode(w, http.StatusBadRequest, "text", "text is longer than 300 characters")
			return "", "", "", false
		}
	}
	if p.Person != nil {
		scope = apiScope(*p.Person)
		if !validScope(scope) {
			writeJSONErrorCode(w, http.StatusBadRequest, "person", "unknown person")
			return "", "", "", false
		}
	}
	if p.Kind != nil && *p.Kind != "" && !memoryKinds[*p.Kind] {
		writeJSONErrorCode(w, http.StatusBadRequest, "kind", "unknown kind")
		return "", "", "", false
	}
	if p.Expires != nil {
		var good bool
		if expires, good = validExpires(*p.Expires, today); !good {
			writeJSONErrorCode(w, http.StatusBadRequest, "expires", "expires must be a future date (YYYY-MM-DD)")
			return "", "", "", false
		}
	}
	return text, scope, expires, true
}

func handleMemoryCreate(w http.ResponseWriter, r *http.Request) {
	var p memoryPatch
	if !decodeMemoryBody(w, r, &p) {
		return
	}
	if p.Text == nil {
		writeJSONErrorCode(w, http.StatusBadRequest, "text", "text is empty")
		return
	}
	s := loadSettings()
	text, scope, expires, ok := checkPatch(w, &p, memoryToday(s))
	if !ok {
		return
	}
	kind := "other"
	if p.Kind != nil && *p.Kind != "" {
		kind = *p.Kind
	}
	m, err := memoryInsert(Memory{
		Person: scope, Text: text, Kind: kind, Source: "manual", Lang: detectLang(text, voiceLang(s.PiperVoice)),
		Pinned: p.Pinned != nil && *p.Pinned, Expires: expires,
	})
	if err != nil {
		writeMemoryStoreError(w, err)
		return
	}
	embedMemoriesNow(r.Context(), s, m)
	writeJSON(w, http.StatusCreated, m)
}

func handleMemoryPatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p memoryPatch
	if !decodeMemoryBody(w, r, &p) {
		return
	}
	s := loadSettings()
	text, scope, expires, ok := checkPatch(w, &p, memoryToday(s))
	if !ok {
		return
	}
	old, m, err := memoryModify(id, func(m *Memory) error {
		changed := false
		if p.Text != nil && text != m.Text {
			m.Text, m.Lang, changed = text, detectLang(text, m.Lang), true
		}
		if p.Person != nil && scope != m.Person {
			m.Person, changed = scope, true
		}
		if p.Kind != nil && *p.Kind != "" && *p.Kind != m.Kind {
			m.Kind, changed = *p.Kind, true
		}
		if p.Pinned != nil && *p.Pinned != m.Pinned {
			m.Pinned, changed = *p.Pinned, true
		}
		if p.Expires != nil && expires != m.Expires {
			m.Expires, changed = expires, true
		}
		if !changed {
			return errNoChange
		}
		m.UpdatedAt = nowFunc().UTC()
		return nil
	})
	if err != nil {
		writeMemoryStoreError(w, err)
		return
	}
	if m.ID == "" { // nothing changed
		cur, found := memoryGet(id)
		if !found {
			writeMemoryStoreError(w, errMemoryNotFound)
			return
		}
		writeJSON(w, http.StatusOK, cur)
		return
	}
	if m.Text != old.Text {
		vectorsDelete([]string{m.ID})
		embedMemoriesNow(r.Context(), s, m)
	}
	writeJSON(w, http.StatusOK, m)
}

func handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	if _, err := memoryDelete(r.PathValue("id")); err != nil {
		writeMemoryStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": 1})
}

func handleMemoriesBulkDelete(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("confirm") != "1" {
		writeJSONErrorCode(w, http.StatusBadRequest, "confirm", "bulk delete needs confirm=1")
		return
	}
	if !q.Has("person") || q.Get("person") == "all" {
		writeJSONErrorCode(w, http.StatusBadRequest, "person", "person (an id or household) is required")
		return
	}
	scope := apiScope(q.Get("person"))
	gone, err := memoryDeleteWhere(func(m *Memory) bool { return m.Person == scope })
	if err != nil {
		writeMemoryStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": len(gone)})
}

func handleMemoriesExport(w http.ResponseWriter, r *http.Request) {
	ms := memoryList(nil)
	if ms == nil {
		ms = []Memory{}
	}
	sortMemories(ms)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{"version": 1, "memories": ms}); err != nil {
		writeMemoryStoreError(w, err)
		return
	}
	name := "hermes-memories-" + memoryToday(loadSettings()) + ".json"
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

// memoryStatus is features.memory in /api/status. It never embeds: embedOk
// needs the last embed call to have worked (embedOK) AND Ollama to answer
// /api/tags with the model listed, from the probe handleStatus just made
// (ollamaModelAvailable reuses it), so a stopped Ollama shows as not ok.
func memoryStatus(ctx context.Context) any {
	s := loadSettings()
	model := embedModelFor(s)
	return map[string]any{
		"enabled":    s.MemoryEnabled,
		"auto":       s.MemoryAuto,
		"count":      len(memoryList(nil)),
		"embedModel": model,
		"embedOk":    embedOK(s) && ollamaModelAvailable(ctx, model),
	}
}

// ---------------------------------------------------------------------------
// Registration

var rememberImperatives = []string{"merk dir", "merke dir", "notier dir", "speicher", "speichere", "remember", "note that", "don t forget"}

func init() {
	memoryEnabled := func(t *Turn) bool { return t.Settings != nil && t.Settings.MemoryEnabled }

	// Providers People (people.go) reads; see the memory contract there.
	memoryCountProvider = memoryCount
	ownerMemoryStatsProvider = ownerMemoryStats
	migrateOwnerMemoriesProvider = func(to, sensitiveTo string) (int, error) {
		if to == "" {
			to = "household"
		}
		return migrateOwnerMemories(to, sensitiveTo)
	}
	reassignRecentMemoriesProvider = func(clientID, from, to string, since time.Time) (int, func(context.Context) error, error) {
		ids, err := reassignRecentAutoMemories(clientID, from, to, nowFunc().Sub(since))
		if err != nil || len(ids) == 0 {
			return 0, nil, err
		}
		return len(ids), func(context.Context) error { return moveMemories(ids, from) }, nil
	}

	// Fast-path tools (deterministic; never offered to the router).
	registerTool(Tool{Name: "memory_remember", SideEffect: true, Access: memoryWriteAccess, Run: runRemember})
	registerTool(Tool{Name: "memory_forget", SideEffect: true,
		Access: func(*Turn, json.RawMessage) Access { return AccessWrite }, Run: runForget})
	registerTool(Tool{Name: "memory_recall",
		Access: func(*Turn, json.RawMessage) Access { return AccessRead }, Run: runRecall})
	registerFastPath(FastPath{Name: "memory_remember", Order: 30, Tool: "memory_remember", Match: matchRemember})
	registerFastPath(FastPath{Name: "memory_forget", Order: 31, Tool: "memory_forget", Match: matchForget})
	registerFastPath(FastPath{Name: "memory_recall", Order: 32, Tool: "memory_recall", Match: matchRecall})

	// Router tools (SPEC 9.B): phrasings the fast paths do not catch.
	registerTool(Tool{
		Name:        "remember",
		Description: "Store a durable fact the user explicitly asks HERMES to remember (about themselves or, with scope household, about the shared home).",
		Params: func(*Turn) json.RawMessage {
			return json.RawMessage(`{"type":"object","properties":{"fact":{"type":"string","description":"the statement to remember, in the user's words and language"},"scope":{"type":"string","enum":["speaker","household"]}},"required":["fact"]}`)
		},
		Gate:       []string{"merk dir", "merke dir", "merken", "remember", "notier dir", "speicher dir"},
		Enabled:    memoryEnabled,
		Access:     memoryWriteAccess,
		SideEffect: true,
		Imperative: rememberImperatives,
		Offer: func(t *Turn, args json.RawMessage) string {
			var a rememberArgs
			_ = json.Unmarshal(args, &a)
			fact := safeTitle(a.Fact)
			return pick(t.Lang, "Should I remember: "+fact+"?", "Soll ich mir merken: "+fact+"?")
		},
		Run: func(t *Turn, args json.RawMessage) Reply {
			var a rememberArgs
			_ = json.Unmarshal(args, &a)
			// The router copies the fact; it must come from what was said.
			if a.Fact != "" && !overlapGuard(a.Fact, t.Query, nil) {
				if m, ok := matchRemember(t); ok {
					return runRemember(t, m)
				}
				a.Fact = t.Query
			}
			b, _ := json.Marshal(a)
			return runRemember(t, b)
		},
	})
	registerTool(Tool{
		Name:        "forget",
		Description: "Delete a stored memory the user asks HERMES to forget.",
		Params: func(*Turn) json.RawMessage {
			return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"what to forget, in the user's words"}},"required":["query"]}`)
		},
		Gate:       []string{"vergiss", "forget"},
		Enabled:    memoryEnabled,
		Access:     func(*Turn, json.RawMessage) Access { return AccessWrite },
		SideEffect: true,
		Imperative: []string{"vergiss", "forget"},
		Offer: func(t *Turn, args json.RawMessage) string {
			var a forgetArgs
			_ = json.Unmarshal(args, &a)
			q := safeTitle(a.Query)
			return pick(t.Lang, "Should I forget what I noted about "+q+"?", "Soll ich vergessen, was ich mir zu "+q+" gemerkt habe?")
		},
		Run: func(t *Turn, args json.RawMessage) Reply {
			var a forgetArgs
			_ = json.Unmarshal(args, &a)
			b, _ := json.Marshal(forgetArgs{Mode: "query", Query: a.Query})
			if strings.TrimSpace(a.Query) == "" || isBarePronoun(a.Query) {
				b, _ = json.Marshal(forgetArgs{Mode: "last"})
			}
			return runForget(t, b)
		},
	})

	registerContext(20, "memory", memoryContext)
	registerAfterTurn(memoryAfterTurn)
	registerBackground("memory", memoryWorker)
	onPersonDeleted(deletePersonMemories)
	registerStatus("memory", memoryStatus)

	registerRoute("GET /api/memories", requireUnlock(handleMemoriesList))
	registerRoute("POST /api/memories", requireUnlock(handleMemoryCreate))
	registerRoute("DELETE /api/memories", requireUnlock(handleMemoriesBulkDelete))
	registerRoute("GET /api/memories/export", requireUnlock(handleMemoriesExport))
	registerRoute("PATCH /api/memories/{id}", requireUnlock(handleMemoryPatch))
	registerRoute("DELETE /api/memories/{id}", requireUnlock(handleMemoryDelete))
}
