package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // settingsLocation must work in the slim Docker image
	"unicode"
)

// skills.go is the contract every feature file codes against: the per-turn
// Turn, the replies tools give back, the registries feature files fill from
// init(), who may run what (access classes) and pending questions.

// ---------------------------------------------------------------------------
// Turn

type Speaker struct {
	ID     string  `json:"id"`   // "p_…" | "_single" (single-user mode) | "" (guest / uncertain)
	Name   string  `json:"name"` // display name, "" for guests
	Source string  `json:"source"`
	Score  float64 `json:"score,omitempty"` // only for voice matches
}

// Speaker sources (Section 3.1).
const (
	SourceVoice     = "voice"
	SourceManual    = "manual"
	SourceClaim     = "claim"
	SourceSticky    = "sticky"
	SourceSingle    = "single"
	SourceUncertain = "uncertain"
	SourceGuest     = "guest"
)

// singleSpeakerID is the speaker id in single-user mode (no people exist).
const singleSpeakerID = "_single"

func (s Speaker) Known() bool { return strings.HasPrefix(s.ID, "p_") }

// VoiceInput is a validated, L2-normalised voice embedding of the utterance.
type VoiceInput struct {
	Model, ORT string
	Emb        []float32
	Sec        float64
}

// Attachment is a page the browser extracted for "lies diesen Link vor".
type Attachment struct {
	Kind, URL, FinalURL, Title, Site, Text, Lang string
	Paywall                                      bool
	Instruction                                  string // user's typed request next to the URL (≤500 runes)
}

type Turn struct {
	Ctx           context.Context
	Req           *http.Request
	Settings      *Settings
	Lang          string         // "de" | "en" (voiceLang(s.PiperVoice))
	Loc           *time.Location // settings tz, else client tz, else env / Europe/Berlin
	Now           time.Time      // nowFunc().In(Loc)
	Query         string         // latest user message (sanitised)
	History       []chatMessage  // user/assistant turns incl. Query as last, after the speaker filter
	Tagged        []reqMessage   // same, with src/sp/att tags (router context and realtime follow-ups read these)
	Speaker       Speaker
	ClientID      string
	Spoken        bool
	Voice         *VoiceInput // nil if absent/invalid/short
	VoiceShort    bool        // a voice was sent but had < minSec voiced audio
	VoiceSec      float64     // voiced seconds the client reported (also when short)
	VoiceExpected bool        // client was capturing for this utterance
	Unlocked      bool        // request carries a valid hermes_unlock cookie (4.10)
	Attachment    *Attachment
	Tokens        []string // tokenize(Query)

	w            http.ResponseWriter
	out          *sseStream
	handledBy    string // "fast:<name>"|"tool:<name>"|"kb"|"realtime:<kind>"|"image"|"persona"|"offer"|"local:*"|"notice"
	toolName     string // tool behind a fast:/tool:/pending turn, for the {"tool"} event
	personalUsed bool   // persona turn injected a personal context section
	// replySuffix runs once in streamLocal, inside the final event before
	// "done", and only after an answer actually streamed (not after a model
	// error or when the client went away). A context provider uses it to
	// append to the reply (the docs offer); fn may set fields of ev.
	replySuffix []func(ev map[string]any)
	// untrustedHistory: a recent reply came from untrusted material
	// (recentUntrustedReply); personalContext then adds no personal section.
	untrustedHistory bool
	refused          bool   // the turn's reply is an access refusal
	access           Access // class the running tool was checked for (PushUndo records it)
	accessSet        bool   // access was set by checkAccess
}

// nowFunc is the clock for everything time-dependent; tests replace it.
var nowFunc = time.Now

// SSE returns the turn's event stream, creating it (and its headers) on first
// use so JSON error replies stay possible until something is streamed.
func (t *Turn) SSE() *sseStream {
	if t.out == nil {
		t.out = newSSEStream(t.w)
	}
	return t.out
}

// PersonalAllowed is t.Can(AccessRead), named for readability at call sites
// that decide whether personal data may appear in a reply.
func (t *Turn) PersonalAllowed() bool { return t.Can(AccessRead) }

// MemoryScope is where this speaker's memories live: their person id, the
// reserved "_owner" scope in single-user mode, else "" (nothing personal).
func (t *Turn) MemoryScope() string {
	switch {
	case t.Speaker.Known():
		return t.Speaker.ID
	case t.Speaker.ID == singleSpeakerID:
		return "_owner"
	}
	return ""
}

// Meta is the {"meta":…} payload: how the reply was produced (the message
// tag the client stores), who spoke, and whether the exchange is private.
func (t *Turn) Meta() map[string]any {
	src := t.handledBy
	if src == "" {
		src = "persona"
	}
	private := true
	switch {
	case src == "image" || src == "kb" || strings.HasPrefix(src, "realtime:"):
		private = false
	case src == "persona" && !t.personalUsed:
		private = false
	}
	return map[string]any{"src": src, "sp": t.Speaker.ID, "private": private}
}

// pick returns de for German, en otherwise.
func pick(lang, en, de string) string {
	if lang == "de" {
		return de
	}
	return en
}

// settingsLocation is the household time zone: the setting, then HERMES_TZ,
// then TZ, then Europe/Berlin (always loadable thanks to time/tzdata).
func settingsLocation(s *Settings) *time.Location {
	var names []string
	if s != nil {
		names = append(names, s.Timezone)
	}
	names = append(names, os.Getenv("HERMES_TZ"), os.Getenv("TZ"), "Europe/Berlin")
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || name == "Local" {
			continue
		}
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.UTC
}

// ---------------------------------------------------------------------------
// Replies

type Reply struct {
	Text     string    // final text in t.Lang
	Card     *Card     // optional {"card":…} event before the text
	Error    bool      // send {"error":Text} first (UI styles as failed)
	Local    *LocalJob // instead of Text: stream an answer from the local model
	NoMemory bool      // reserved; tool/fast turns never trigger extraction anyway
}

type Card struct {
	Kind string `json:"kind"`
	Data any    `json:"data"`
}

// LocalJob streams a local-model answer (doc QA, link summary). It never has
// tools: the text it reads may be attacker-controlled.
type LocalJob struct {
	System    string        // job system prompt (persona prefix added when Persona)
	Messages  []chatMessage // user/assistant messages
	Persona   bool          // prepend effectiveSystemPrompt(s)
	Personal  bool          // append personal context (only if t.PersonalAllowed())
	MaxTokens int           // 0 => settings.MaxTokens
	// LangReminder sends replyLanguageReminder as the last message. Free
	// chat needs it: the model answers in the language of the question
	// otherwise. Doc and link jobs carry the language in their own prompt.
	LangReminder bool
}

// ---------------------------------------------------------------------------
// Access classes (Section 4.10)

type Access int

const (
	AccessOpen    Access = iota // timers, household reminders, lists, links, weather
	AccessRead                  // memory recall, calendar read, docs, own reminders
	AccessWrite                 // merk dir, vergiss, calendar add, personal reminders
	AccessExtract               // automatic memory extraction
)

func (a Access) String() string {
	switch a {
	case AccessOpen:
		return "open"
	case AccessRead:
		return "read"
	case AccessWrite:
		return "write"
	case AccessExtract:
		return "extract"
	}
	return fmt.Sprintf("access(%d)", int(a))
}

// Can applies the access matrix. Voice ID is not authentication: weakly
// attributed speakers (sticky, uncertain, guest) only get open tools, and a
// claim stops counting once the household turned the PIN lock on.
func (t *Turn) Can(a Access) bool {
	if a <= AccessOpen {
		return true
	}
	lock := t.Settings != nil && t.Settings.PersonalLock
	switch t.Speaker.Source {
	case SourceVoice, SourceSingle:
		return true
	case SourceManual:
		if t.Unlocked {
			return true
		}
		return a != AccessExtract && !lock
	case SourceClaim:
		return a != AccessExtract && !lock
	}
	return false // sticky, uncertain, guest, unknown
}

// toolAccess is the access class a tool needs for these args.
func toolAccess(t *Turn, tool *Tool, args json.RawMessage) Access {
	if tool.Access == nil {
		return AccessOpen
	}
	return tool.Access(t, args)
}

// checkAccess applies the central access check for running tool with args
// and remembers the class, so an undo entry the tool pushes needs the same.
func (t *Turn) checkAccess(tool *Tool, args json.RawMessage) bool {
	a := toolAccess(t, tool, args)
	if !t.Can(a) {
		return false
	}
	t.access, t.accessSet = a, true
	return true
}

// refuse is the standard reply when t.Can fails. An uncertain speaker is
// asked who is talking, and the original action resumes after the answer.
func refuse(t *Turn, tool string, args json.RawMessage) Reply {
	t.refused = true
	switch t.Speaker.Source {
	case SourceGuest, "":
		return Reply{Text: pick(t.Lang,
			"I only do that for people I know. Introduce yourself with \"I'm …\".",
			"Das mache ich nur für Personen, die ich kenne. Stell dich mit „Ich bin …“ vor.")}
	case SourceUncertain:
		q := pick(t.Lang,
			"Who is speaking? Say \"I'm …\" or pick yourself at the top.",
			"Wer spricht gerade? Sag „Ich bin …“ oder wähle dich oben aus.")
		return t.Ask(Pending{Kind: "slot", Slot: "which", Tool: tool, Args: args}, q)
	}
	return Reply{Text: pick(t.Lang,
		"I'm not sure who is speaking. Please say that again a little longer, or pick yourself at the top with the PIN.",
		"Ich bin mir nicht sicher, wer spricht. Sag den Satz bitte noch einmal etwas länger, oder wähle dich oben mit der PIN aus.")}
}

// ---------------------------------------------------------------------------
// Registries. Feature files register only from init(); tests may register
// stubs and restore the registries afterwards (see skills_test.go).

// FastPath: deterministic, no network, < 1 ms. Match returns the args for
// Tool, whose Run executes it.
type FastPath struct {
	Name  string
	Order int // lower runs first (table in Section 4.3)
	Match func(t *Turn) (args json.RawMessage, ok bool)
	Tool  string
}

// Tool is an executable action, optionally exposed to the LLM router.
type Tool struct {
	Name        string
	Description string                                     // English, for the router
	Params      func(t *Turn) json.RawMessage              // JSON schema; nil => not offered to the router
	Gate        []string                                   // phrases that send an utterance to the router (9.0)
	GateFunc    func(t *Turn) bool                         // optional extra gate
	Enabled     func(t *Turn) bool                         // nil => always
	Access      func(t *Turn, args json.RawMessage) Access // nil => AccessOpen
	SideEffect  bool                                       // changes state outside the conversation
	Imperative  []string                                   // a router result runs directly only with one of these cues
	Offer       func(t *Turn, args json.RawMessage) string // "Soll ich …?" for the confirm path
	Run         func(t *Turn, args json.RawMessage) Reply
}

func (tool *Tool) enabled(t *Turn) bool { return tool.Enabled == nil || tool.Enabled(t) }

type routeEntry struct {
	pattern string
	h       http.HandlerFunc
}

type statusEntry struct {
	name string
	fn   func(ctx context.Context) any
}

type contextEntry struct {
	order int
	name  string
	fn    func(t *Turn) string
}

type backgroundEntry struct {
	name string
	fn   func(ctx context.Context)
}

type briefingEntry struct {
	order int
	name  string
	fn    func(t *Turn) (string, error)
}

// registry is one struct so tests can snapshot and restore it whole.
type registry struct {
	fastPaths     []FastPath
	tools         []*Tool
	toolByName    map[string]*Tool
	routes        []routeEntry
	status        []statusEntry
	contexts      []contextEntry
	afterTurn     []func(t *Turn, reply string)
	background    []backgroundEntry
	briefings     []briefingEntry
	personDeleted []func(personID string) (kind string, n int)
}

var (
	regMu sync.RWMutex
	reg   = registry{toolByName: map[string]*Tool{}}
)

// Registration mistakes are programming errors in init(): fail loudly at
// start-up instead of silently dropping a feature.
func regPanic(format string, args ...any) { panic("register: " + fmt.Sprintf(format, args...)) }

func registerFastPath(f FastPath) {
	if f.Name == "" || f.Match == nil {
		regPanic("fast path needs Name and Match")
	}
	regMu.Lock()
	defer regMu.Unlock()
	for _, x := range reg.fastPaths {
		if x.Name == f.Name {
			regPanic("duplicate fast path %q", f.Name)
		}
	}
	reg.fastPaths = append(reg.fastPaths, f)
	sort.SliceStable(reg.fastPaths, func(i, j int) bool { return reg.fastPaths[i].Order < reg.fastPaths[j].Order })
}

func registerTool(tool Tool) {
	if tool.Name == "" || tool.Run == nil {
		regPanic("tool needs Name and Run")
	}
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := reg.toolByName[tool.Name]; dup {
		regPanic("duplicate tool %q", tool.Name)
	}
	p := &tool
	reg.tools = append(reg.tools, p)
	reg.toolByName[tool.Name] = p
}

// registerRoute adds a handler that newMux mounts behind the full middleware
// chain. Use Go 1.22 method patterns ("GET /api/people").
func registerRoute(pattern string, h http.HandlerFunc) {
	if pattern == "" || h == nil {
		regPanic("route needs a pattern and a handler")
	}
	regMu.Lock()
	defer regMu.Unlock()
	reg.routes = append(reg.routes, routeEntry{pattern, h})
}

// registerStatus adds a "features.<name>" entry to GET /api/status.
func registerStatus(name string, fn func(ctx context.Context) any) {
	if name == "" || fn == nil {
		regPanic("status needs a name and a func")
	}
	regMu.Lock()
	defer regMu.Unlock()
	for _, x := range reg.status {
		if x.name == name {
			regPanic("duplicate status %q", name)
		}
	}
	reg.status = append(reg.status, statusEntry{name, fn})
}

// registerContext adds a personal-context section for persona prompts.
func registerContext(order int, name string, fn func(t *Turn) string) {
	if fn == nil {
		regPanic("context %q needs a func", name)
	}
	regMu.Lock()
	defer regMu.Unlock()
	reg.contexts = append(reg.contexts, contextEntry{order, name, fn})
	sort.SliceStable(reg.contexts, func(i, j int) bool { return reg.contexts[i].order < reg.contexts[j].order })
}

// registerAfterTurn runs fn after every completed reply (memory extraction
// hooks in here and filters on t.handledBy itself).
func registerAfterTurn(fn func(t *Turn, reply string)) {
	if fn == nil {
		regPanic("after-turn hook needs a func")
	}
	regMu.Lock()
	defer regMu.Unlock()
	reg.afterTurn = append(reg.afterTurn, fn)
}

// registerBackground adds a worker that main() starts; tests never start it.
func registerBackground(name string, fn func(ctx context.Context)) {
	if fn == nil {
		regPanic("background %q needs a func", name)
	}
	regMu.Lock()
	defer regMu.Unlock()
	reg.background = append(reg.background, backgroundEntry{name, fn})
}

// registerBriefing adds a morning-briefing section.
func registerBriefing(order int, name string, fn func(t *Turn) (string, error)) {
	if fn == nil {
		regPanic("briefing %q needs a func", name)
	}
	regMu.Lock()
	defer regMu.Unlock()
	reg.briefings = append(reg.briefings, briefingEntry{order, name, fn})
	sort.SliceStable(reg.briefings, func(i, j int) bool { return reg.briefings[i].order < reg.briefings[j].order })
}

// onPersonDeleted adds a cascade hook; kind names what it deleted
// ("memories", "docs", …) and n how many.
func onPersonDeleted(fn func(personID string) (kind string, n int)) {
	if fn == nil {
		regPanic("person-deleted hook needs a func")
	}
	regMu.Lock()
	defer regMu.Unlock()
	reg.personDeleted = append(reg.personDeleted, fn)
}

// Registry readers return copies so callers never hold regMu.

func fastPaths() []FastPath {
	regMu.RLock()
	defer regMu.RUnlock()
	return append([]FastPath(nil), reg.fastPaths...)
}

func registeredTools() []*Tool {
	regMu.RLock()
	defer regMu.RUnlock()
	return append([]*Tool(nil), reg.tools...)
}

func lookupTool(name string) *Tool {
	regMu.RLock()
	defer regMu.RUnlock()
	return reg.toolByName[name]
}

func registeredRoutes() []routeEntry {
	regMu.RLock()
	defer regMu.RUnlock()
	return append([]routeEntry(nil), reg.routes...)
}

func contextSections() []contextEntry {
	regMu.RLock()
	defer regMu.RUnlock()
	return append([]contextEntry(nil), reg.contexts...)
}

func briefingSections() []briefingEntry {
	regMu.RLock()
	defer regMu.RUnlock()
	return append([]briefingEntry(nil), reg.briefings...)
}

// runAfterTurn calls every after-turn hook; a panicking hook must not take
// the finished reply down with it.
func runAfterTurn(t *Turn, reply string) {
	regMu.RLock()
	hooks := make([]func(*Turn, string), len(reg.afterTurn))
	copy(hooks, reg.afterTurn)
	regMu.RUnlock()
	for _, fn := range hooks {
		func() {
			defer func() {
				if p := recover(); p != nil {
					log.Printf("after-turn hook panic: %v", p)
				}
			}()
			fn(t, reply)
		}()
	}
}

// startBackground starts every registered worker; they stop with ctx.
func startBackground(ctx context.Context) {
	regMu.RLock()
	workers := append([]backgroundEntry(nil), reg.background...)
	regMu.RUnlock()
	for _, b := range workers {
		go b.fn(ctx)
	}
}

// runPersonDeleted runs the cascade hooks and sums the counts per kind.
func runPersonDeleted(personID string) map[string]int {
	regMu.RLock()
	hooks := make([]func(string) (string, int), len(reg.personDeleted))
	copy(hooks, reg.personDeleted)
	regMu.RUnlock()
	out := map[string]int{}
	for _, fn := range hooks {
		if kind, n := fn(personID); kind != "" {
			out[kind] += n
		}
	}
	return out
}

// statusTimeout bounds each feature status probe.
const statusTimeout = 1500 * time.Millisecond

// collectStatus runs every status provider concurrently, each with its own
// deadline, so one hung integration cannot stall the status pills.
func collectStatus(ctx context.Context) map[string]any {
	regMu.RLock()
	entries := append([]statusEntry(nil), reg.status...)
	regMu.RUnlock()
	if len(entries) == 0 {
		return nil
	}
	type result struct {
		name string
		v    any
	}
	ch := make(chan result, len(entries))
	for _, e := range entries {
		go func(e statusEntry) {
			cctx, cancel := context.WithTimeout(ctx, statusTimeout)
			defer cancel()
			done := make(chan any, 1)
			go func() {
				defer func() {
					if p := recover(); p != nil {
						done <- map[string]any{"ok": false, "error": "internal"}
					}
				}()
				done <- e.fn(cctx)
			}()
			select {
			case v := <-done:
				ch <- result{e.name, v}
			case <-cctx.Done():
				ch <- result{e.name, map[string]any{"ok": false, "error": "timeout"}}
			}
		}(e)
	}
	out := make(map[string]any, len(entries))
	for range entries {
		r := <-ch
		out[r.name] = r.v
	}
	return out
}

// ---------------------------------------------------------------------------
// Providers set by feature init()s (people.go, gcal.go). Call the nil-safe
// wrappers below, never the variables directly.

var (
	speakerResolver       func(t *Turn, v *VoiceInput, claimedID, claimedSource string) Speaker  // W1-C
	personNameProvider    func(id string) (string, bool)                                         // W1-C
	peopleCountProvider   func() int                                                             // W1-C
	personExistsProvider  func(id string) bool                                                   // W1-C
	calendarEvents        func(ctx context.Context, from, to time.Time) ([]CalendarEvent, error) // W2-I; nil => not connected
	calendarOwnerProvider func() string                                                          // W2-I
	// calendarConnectedProvider reports whether the calendar is linked and
	// usable right now (gcal.go: a token that needs no relink). nil =>
	// connected whenever calendarEvents is set (test stubs).
	calendarConnectedProvider func() bool // W2-I
	// whichSpeakerResume answers the "Wer spricht gerade?" question (slot
	// "which") that refuse() asks an uncertain speaker: set a claim for the
	// named person and re-run p.Tool. W1-C; nil => the question is dropped.
	whichSpeakerResume func(t *Turn, p *Pending) (Reply, bool)
)

var errCalendarNotConnected = errors.New("calendar not connected")

// resolveSpeaker applies the Section 3.1 rules. Without the people feature
// there is nobody to recognise: single-user mode.
func resolveSpeaker(t *Turn, v *VoiceInput, claimedID, claimedSource string) Speaker {
	if speakerResolver != nil {
		return speakerResolver(t, v, claimedID, claimedSource)
	}
	if peopleCount() == 0 {
		return Speaker{ID: singleSpeakerID, Source: SourceSingle}
	}
	if t.VoiceExpected && t.VoiceShort {
		return Speaker{Source: SourceUncertain}
	}
	return Speaker{Source: SourceGuest}
}

func personName(id string) (string, bool) {
	if personNameProvider == nil {
		return "", false
	}
	return personNameProvider(id)
}

func peopleCount() int {
	if peopleCountProvider == nil {
		return 0
	}
	return peopleCountProvider()
}

// personExists guards writes for people deleted while a job was queued.
// Household ("") and single-user scopes always exist.
func personExists(id string) bool {
	if id == "" || id == "_owner" || id == singleSpeakerID {
		return true
	}
	if personExistsProvider == nil {
		return false
	}
	return personExistsProvider(id)
}

// calendarConnected: a calendar is linked and usable. gcal.go always sets
// calendarEvents, so the provider decides whether a token is in place.
func calendarConnected() bool {
	if calendarEvents == nil {
		return false
	}
	if p := calendarConnectedProvider; p != nil {
		return p()
	}
	return true
}

func fetchCalendarEvents(ctx context.Context, from, to time.Time) ([]CalendarEvent, error) {
	fn := calendarEvents
	if fn == nil {
		return nil, errCalendarNotConnected
	}
	return fn(ctx, from, to)
}

func calendarOwner() string {
	if calendarOwnerProvider == nil {
		return ""
	}
	return calendarOwnerProvider()
}

type CalendarEvent struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Location    string    `json:"location,omitempty"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	AllDay      bool      `json:"allDay"`
	NeedsAction bool      `json:"needsAction,omitempty"`
}

// ---------------------------------------------------------------------------
// safeTitle

var urlPattern = regexp.MustCompile(`(?i)\b(?:https?://|www\.)\S+`)

// safeTitle makes stored or third-party text safe to speak and to place
// between prompt data delimiters: no URLs, controls, newlines or "<<<"/">>>",
// at most 80 runes.
func safeTitle(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s)
	s = urlPattern.ReplaceAllString(s, " ")
	for strings.Contains(s, "<<<") || strings.Contains(s, ">>>") {
		s = strings.ReplaceAll(strings.ReplaceAll(s, "<<<", ""), ">>>", "")
	}
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(truncateRunes(s, 80))
}

// ---------------------------------------------------------------------------
// Pending intents (Section 4.9)

type Pending struct {
	Kind    string          // "confirm" | "slot" | "voice"
	Tool    string          // tool to run on completion
	Args    json.RawMessage // args collected so far
	Slot    string          // for "slot": "when" | "item" | "title" | "which"
	Prompt  string          // the question that was asked
	Speaker string          // speaker id when it was created
	Expires time.Time       // now + 60 s
	// Resume handles the answer; ok=false means "not an answer": the pending
	// intent is dropped and the normal pipeline runs. nil uses the default
	// handling for confirm and slot:when|item|title.
	Resume func(t *Turn, p *Pending) (Reply, bool)
}

const pendingTTL = 60 * time.Second

var (
	pendingMu sync.Mutex
	pendings  = map[string]*Pending{} // by client id; newest wins
)

// Ask stores p for this client and returns the question as the reply. The
// {"pending"} event tells the UI to keep the mic open for the answer. A turn
// without a client id cannot be followed up, so it only asks.
func (t *Turn) Ask(p Pending, question string) Reply {
	if t.ClientID != "" {
		p.Prompt = question
		p.Speaker = t.Speaker.ID
		if p.Expires.IsZero() {
			p.Expires = nowFunc().Add(pendingTTL)
		}
		pendingMu.Lock()
		// Expired questions of clients that never came back would pile up
		// (any request can bring a fresh client id): drop them here.
		now := nowFunc()
		for k, old := range pendings {
			if !now.Before(old.Expires) {
				delete(pendings, k)
			}
		}
		pendings[t.ClientID] = &p
		pendingMu.Unlock()
		secs := int(p.Expires.Sub(nowFunc()).Round(time.Second) / time.Second)
		t.SSE().event(map[string]any{"pending": map[string]any{"kind": p.Kind, "expiresIn": secs}})
	}
	return Reply{Text: question}
}

// takePending removes and returns the client's pending intent, nil when there
// is none or it expired.
func takePending(clientID string) *Pending {
	if clientID == "" {
		return nil
	}
	pendingMu.Lock()
	defer pendingMu.Unlock()
	p := pendings[clientID]
	delete(pendings, clientID)
	if p == nil || !nowFunc().Before(p.Expires) {
		return nil
	}
	return p
}

// restorePending puts back a pending intent that belonged to someone else
// unless a newer one was stored meanwhile.
func restorePending(clientID string, p *Pending) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	if _, exists := pendings[clientID]; !exists {
		pendings[clientID] = p
	}
}

func purgePendingPerson(personID string) int {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	n := 0
	for k, p := range pendings {
		if p.Speaker == personID {
			delete(pendings, k)
			n++
		}
	}
	return n
}

var (
	yesPhrases = []string{"ja", "jo", "jap", "genau", "passt", "gerne", "gern", "mach das", "klar",
		"yes", "yeah", "yep", "sure", "do it", "correct", "ok", "okay"}
	noPhrases = []string{"nein", "nö", "noe", "nee", "lieber nicht", "mach das nicht", "no", "nope", "don t", "dont"}
)

// yesNo classifies a short answer to a confirm question: 1 yes, -1 no, 0 not
// an answer. Longer utterances are new requests, not answers.
func yesNo(tokens []string) int {
	if len(tokens) == 0 || len(tokens) > 5 {
		return 0
	}
	if hasAnyPhrase(tokens, noPhrases) {
		return -1
	}
	if hasAnyPhrase(tokens, yesPhrases) {
		return 1
	}
	return 0
}

func isQuestion(t *Turn) bool {
	return strings.HasSuffix(strings.TrimSpace(t.Query), "?") || hasQuestionWord(t.Tokens)
}

// mergeArg sets key=value in a JSON object, starting a new one if needed.
func mergeArg(args json.RawMessage, key string, value any) json.RawMessage {
	m := map[string]any{}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &m)
	}
	m[key] = value
	b, _ := json.Marshal(m)
	return b
}

// defaultResume handles the generic pending kinds. slot:which is delegated to
// the people feature (whichSpeakerResume); "voice" pendings always carry
// their own Resume.
func defaultResume(t *Turn, p *Pending) (Reply, bool) {
	switch {
	case p.Kind == "confirm":
		switch yesNo(t.Tokens) {
		case 1:
			return runToolChecked(t, p.Tool, p.Args), true
		case -1:
			return Reply{Text: pick(t.Lang, "Okay, I won't.", "Okay, mache ich nicht.")}, true
		}
	case p.Kind == "slot" && p.Slot == "when":
		if _, ok := parseWhen(t.Query, t.Now, t.Loc); ok {
			return runToolChecked(t, p.Tool, mergeArg(p.Args, "when_text", t.Query)), true
		}
	case p.Kind == "slot" && p.Slot == "which":
		if fn := whichSpeakerResume; fn != nil {
			return fn(t, p)
		}
	case p.Kind == "slot" && (p.Slot == "item" || p.Slot == "title"):
		if len(t.Tokens) > 0 && len(t.Tokens) <= 8 && !isQuestion(t) {
			return runToolChecked(t, p.Tool, mergeArg(p.Args, p.Slot, t.Query)), true
		}
	}
	return Reply{}, false
}

// runToolChecked runs a registered tool after the central access check.
func runToolChecked(t *Turn, name string, args json.RawMessage) Reply {
	tool := lookupTool(name)
	if tool == nil || !tool.enabled(t) {
		log.Printf("pending: tool %q is not available", name)
		return Reply{Text: pick(t.Lang, "That is not available right now.", "Das geht gerade nicht."), Error: true}
	}
	t.toolName = tool.Name
	if !t.checkAccess(tool, args) {
		return refuse(t, tool.Name, args)
	}
	return tool.Run(t, args)
}

// runPendingAnswer is the order-1 fast path: answer an open question for this
// client. A different recognised voice cannot answer someone else's confirm;
// their question stays open for them.
func runPendingAnswer(t *Turn) bool {
	p := takePending(t.ClientID)
	if p == nil {
		return false
	}
	if t.Speaker.Source == SourceVoice && p.Speaker != t.Speaker.ID {
		restorePending(t.ClientID, p)
		return false
	}
	resume := p.Resume
	if resume == nil {
		resume = defaultResume
	}
	before := t.handledBy
	r, ok := resume(t, p)
	if !ok {
		return false
	}
	// a resumed tool that tagged its turn "local:*" (docs, link) keeps that
	// tag: it marks the reply as local-only for memory extraction
	if t.handledBy == before || !strings.HasPrefix(t.handledBy, "local:") {
		t.handledBy = "fast:pending_answer"
		if p.Tool != "" {
			t.handledBy = "tool:" + p.Tool
		}
	}
	writeReply(t, r)
	return true
}

// runFastPaths runs the pending answer, then the first matching registered
// fast path. Access is checked here, so tools cannot forget it.
func runFastPaths(t *Turn) bool {
	if runPendingAnswer(t) {
		return true
	}
	for _, f := range fastPaths() {
		args, ok := f.Match(t)
		if !ok {
			continue
		}
		tool := lookupTool(f.Tool)
		if tool == nil || !tool.enabled(t) {
			continue
		}
		t.handledBy = "fast:" + f.Name
		t.toolName = tool.Name
		if !t.checkAccess(tool, args) {
			writeReply(t, refuse(t, tool.Name, args))
			return true
		}
		writeReply(t, tool.Run(t, args))
		return true
	}
	return false
}

// writeReply streams a tool or fast-path reply.
func writeReply(t *Turn, r Reply) {
	sse := t.SSE()
	if r.Card != nil {
		sse.event(map[string]any{"card": r.Card})
	}
	if t.toolName != "" && !t.refused && (strings.HasPrefix(t.handledBy, "fast:") || strings.HasPrefix(t.handledBy, "tool:")) {
		sse.event(map[string]any{"tool": map[string]any{"name": t.toolName, "ok": !r.Error}})
		if !r.Error {
			noteLastTool(t.ClientID, t.toolName)
		}
	}
	switch {
	case r.Local != nil:
		stop := sse.watchBusy()
		reply, ok := streamLocal(t, *r.Local)
		stop()
		if ok {
			runAfterTurn(t, reply)
		}
	case r.Error:
		sse.fail(r.Text)
	default:
		sse.reply(r.Text)
		runAfterTurn(t, r.Text)
	}
}

// ---------------------------------------------------------------------------
// Undo stack: per device and speaker, so the next speaker on the same device
// cannot undo the previous speaker's action.

const (
	undoMax = 5
	undoTTL = 10 * time.Minute
)

type undoEntry struct {
	label   string
	fn      func(ctx context.Context) error
	at      time.Time
	speaker string
	access  Access // undoing needs the class the action itself needed
}

var (
	undoMu    sync.Mutex
	undoStack = map[string][]undoEntry{}
)

func undoKey(clientID, speakerID string) string { return clientID + "|" + speakerID }

// PushUndo records how to revert the action this turn just did. label is
// spoken back ("Rückgängig gemacht: {label}."). Undoing needs the access
// class the action was checked for: a sticky speaker shares the person's id
// but must not revert (or hear the label of) that person's Write action.
func (t *Turn) PushUndo(label string, fn func(ctx context.Context) error) {
	if fn == nil {
		return
	}
	k := undoKey(t.ClientID, t.Speaker.ID)
	undoMu.Lock()
	defer undoMu.Unlock()
	// Drop stacks whose newest entry expired (devices that never undo).
	now := nowFunc()
	for key, st := range undoStack {
		if len(st) == 0 || now.Sub(st[len(st)-1].at) > undoTTL {
			delete(undoStack, key)
		}
	}
	s := append(undoStack[k], undoEntry{label: label, fn: fn, at: now, speaker: t.Speaker.ID, access: t.undoClass()})
	if len(s) > undoMax {
		s = s[len(s)-undoMax:]
	}
	undoStack[k] = s
}

// popUndo returns the newest unexpired undo entry for this device and
// speaker, or ok=false when there is nothing to undo.
func popUndo(clientID, speakerID string) (label string, fn func(ctx context.Context) error, ok bool) {
	k := undoKey(clientID, speakerID)
	undoMu.Lock()
	defer undoMu.Unlock()
	s := undoStack[k]
	for len(s) > 0 {
		e := s[len(s)-1]
		s = s[:len(s)-1]
		if nowFunc().Sub(e.at) <= undoTTL {
			undoStack[k] = s
			return e.label, e.fn, true
		}
	}
	delete(undoStack, k)
	return "", nil, false
}

// undoClass is the access an undo of this turn's action needs. A tool run
// that bypassed checkAccess fails closed (Write).
func (t *Turn) undoClass() Access {
	if !t.accessSet {
		return AccessWrite
	}
	return t.access
}

// undoAccess is the class needed to undo the newest unexpired entry of this
// device and speaker (open when there is none: "nothing to undo").
func undoAccess(clientID, speakerID string) Access {
	undoMu.Lock()
	defer undoMu.Unlock()
	s := undoStack[undoKey(clientID, speakerID)]
	for i := len(s) - 1; i >= 0; i-- {
		if nowFunc().Sub(s[i].at) <= undoTTL {
			return s[i].access
		}
	}
	return AccessOpen
}

func purgeUndoPerson(personID string) int {
	undoMu.Lock()
	defer undoMu.Unlock()
	n := 0
	for k, s := range undoStack {
		if len(s) > 0 && s[0].speaker == personID {
			n += len(s)
			delete(undoStack, k)
		}
	}
	return n
}

// The undo fast path (order 10): the whole utterance, after "hermes" and
// fillers, is one of these.
var undoUtterances = wordSet(
	"rückgängig", "mach das rückgängig", "mach es rückgängig", "mach rückgängig",
	"das rückgängig machen", "rückgängig machen", "nimm das zurück", "nimm das wieder zurück",
	"undo", "undo that", "undo it", "undo the last thing",
)

// commandFillers may surround a short command without changing it.
var commandFillers = wordSet("hermes", "bitte", "mal", "please", "kannst", "du", "can", "you", "jetzt", "now", "doch")

// commandCore strips fillers ("hermes", "bitte", "mal", "please", "kannst
// du", "can you") from a tokenised short command.
func commandCore(tokens []string) string {
	out := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		if !commandFillers[tok] {
			out = append(out, tok)
		}
	}
	return strings.Join(out, " ")
}

func undoTool(t *Turn, _ json.RawMessage) Reply {
	label, fn, ok := popUndo(t.ClientID, t.Speaker.ID)
	if !ok {
		return Reply{Text: pick(t.Lang, "There is nothing to undo.", "Da gibt es nichts rückgängig zu machen.")}
	}
	ctx, cancel := context.WithTimeout(t.Ctx, 15*time.Second)
	defer cancel()
	if err := fn(ctx); err != nil {
		log.Printf("undo failed: %v", err) // the label may quote personal text
		return Reply{Error: true, Text: pick(t.Lang, "I could not undo that: ", "Das konnte ich nicht rückgängig machen: ") + label + "."}
	}
	return Reply{Text: pick(t.Lang, "Undone: ", "Rückgängig gemacht: ") + label + ".", Card: &Card{Kind: "undo", Data: map[string]string{"label": label}}}
}

// The session state above and pending hub events must not outlive a deleted
// person (Section 4.8).
func init() {
	registerTool(Tool{Name: "undo", Run: undoTool, Access: func(t *Turn, _ json.RawMessage) Access {
		return undoAccess(t.ClientID, t.Speaker.ID)
	}})
	registerFastPath(FastPath{Name: "undo", Order: 10, Tool: "undo", Match: func(t *Turn) (json.RawMessage, bool) {
		return nil, t.ClientID != "" && undoUtterances[commandCore(t.Tokens)]
	}})
	onPersonDeleted(func(id string) (string, int) {
		return "session", purgePendingPerson(id) + purgeUndoPerson(id)
	})
	onPersonDeleted(func(id string) (string, int) { return "events", hubPurgePerson(id) })
}
