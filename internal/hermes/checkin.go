package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// checkin.go is the server side of check-ins (SPEC 9.1 "Meeting check-ins",
// 9.8, 5.5):
//   - a background worker that polls the connected calendar every 5 minutes
//     (and on checkinWake) and turns upcoming meetings into pending
//     reminders of kind "meeting". The reminder scheduler (reminders.go)
//     announces them as durable "reminder.due" hub events: "In 10 Minuten:
//     Team-Call." / "Gleich: …";
//   - the voice pause "keine Check-ins mehr heute" / "pause check-ins for an
//     hour" (fast path 15), which also silences the browser's sitting
//     nudges;
//   - GET /api/checkins/state for checkins.js (busy while a meeting runs,
//     paused, quiet hours) and two small POSTs for its buttons.
//
// Meetings are announced once, at most 3 per rolling 30 minutes (more are
// merged into one line), and, per the spec, regardless of quiet hours: the
// user's own appointment is not a nudge. Quiet hours are reported to the
// browser for the sitting nudges. Calendar titles go through safeTitle and
// are only ever spoken; nothing here reaches a language model or Mistral.

var (
	// checkinPoll is how often the calendar is polled.
	checkinPoll = 5 * time.Minute
	// checkinFetchTimeout bounds one calendar fetch.
	checkinFetchTimeout = 20 * time.Second
)

const (
	checkinLookBack     = 12 * time.Hour   // finds meetings in progress (busy state)
	checkinLookAhead    = 70 * time.Minute // max lead (60) + 10: sees every meeting we may have planned
	checkinPlanSlack    = 10 * time.Minute // plan meetings starting within lead + 10 min
	checkinCapWindow    = 30 * time.Minute
	checkinCapCount     = 3
	checkinCacheMaxAge  = 12 * time.Minute // older cache => busy state unknown
	checkinNotNowWindow = 2 * time.Minute  // "nicht jetzt" right after a check-in
	checkinNotNowPause  = time.Hour
	checkinMaxPause     = 24 * time.Hour
	checkinBodyLimit    = 4 << 10
	checkinSource       = "calendar"
)

var checkinMu sync.Mutex // one planning pass at a time (dedupe by key)

var checkinWakeCh = make(chan struct{}, 1)

// checkinWake makes the worker poll the calendar now (calendar connected or
// changed, settings changed, pause lifted).
func checkinWake() {
	select {
	case checkinWakeCh <- struct{}{}:
	default:
	}
}

// checkinCache is the last successful calendar fetch, for the busy state.
var checkinCache = struct {
	sync.Mutex
	events []CalendarEvent
	at     time.Time
	err    string
}{}

// lastNudge is when a browser last spoke a sitting check-in (POST
// /api/checkins/nudged), for "nicht jetzt".
var lastNudge = struct {
	sync.Mutex
	at time.Time
}{}

// ---------------------------------------------------------------------------
// Worker

func checkinWorker(ctx context.Context) {
	for {
		checkinPass(ctx, nowFunc())
		timer := time.NewTimer(checkinPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-checkinWakeCh:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// checkinPass fetches the calendar and plans the meeting reminders. Without
// a calendar or with meeting check-ins off, pending ones are withdrawn.
func checkinPass(ctx context.Context, now time.Time) {
	checkinMu.Lock()
	defer checkinMu.Unlock()
	s := loadSettings()
	if !s.CheckinMeetings || !calendarConnected() {
		checkinCache.Lock()
		checkinCache.events, checkinCache.at, checkinCache.err = nil, time.Time{}, ""
		checkinCache.Unlock()
		withdrawMeetingReminders(func(Reminder) bool { return true })
		return
	}
	fctx, cancel := context.WithTimeout(ctx, checkinFetchTimeout)
	evs, err := fetchCalendarEvents(fctx, now.Add(-checkinLookBack), now.Add(checkinLookAhead))
	cancel()
	if err != nil {
		// Keep what is planned: a flaky network must not drop check-ins.
		log.Printf("checkins: calendar: %v", err)
		checkinCache.Lock()
		checkinCache.err = err.Error()
		checkinCache.Unlock()
		return
	}
	checkinCache.Lock()
	checkinCache.events = append([]CalendarEvent(nil), evs...)
	checkinCache.at, checkinCache.err = now, ""
	checkinCache.Unlock()
	planMeetings(s, evs, now)
}

// meetingKey is the dedupe key "calEventID|startRFC3339" (2.3); the reminder
// speech reads the start time back from it.
func meetingKey(e CalendarEvent) string {
	return strings.ReplaceAll(e.ID, "|", "_") + "|" + e.Start.UTC().Format(time.RFC3339)
}

func meetingStart(r Reminder) (time.Time, bool) {
	i := strings.LastIndex(r.Key, "|")
	if i < 0 {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, r.Key[i+1:])
	return t, err == nil
}

func isCalendarMeeting(r *Reminder) bool {
	return r.Kind == reminderKindMeeting && r.Source == checkinSource
}

// meetingText is the reminder text: the title, plus the merged overflow line
// when more meetings than the cap fall into its half hour. The whole text
// stays within safeTitle's 80 runes so the speech is never cut.
func meetingText(title string, overflow int, lang string) string {
	if overflow <= 0 {
		return title
	}
	var suffix string
	switch {
	case overflow == 1:
		suffix = pick(lang, "Plus one more event in the next half hour", "Außerdem ein weiterer Termin in der nächsten halben Stunde")
	default:
		suffix = fmt.Sprintf(pick(lang, "Plus %d more events in the next half hour", "Außerdem %d weitere Termine in der nächsten halben Stunde"), overflow)
	}
	room := 80 - len([]rune(suffix)) - 2
	return strings.TrimSpace(truncateRunes(title, room)) + ". " + suffix
}

// withdrawMeetingReminders removes pending calendar meeting reminders that
// match and returns how many. They are deleted rather than cancelled: a
// withdrawn check-in (event gone, check-ins paused or switched off) must be
// planned again when the reason goes away, while a check-in the user
// cancelled by voice or in the UI keeps its "cancelled" status and stays
// silent. Pending ones were never announced, so there is no hub event to ack.
func withdrawMeetingReminders(match func(Reminder) bool) int {
	n := 0
	err := remindersStore.Update(func(st *remindersFile) error {
		kept := st.Reminders[:0]
		for _, r := range st.Reminders {
			if isCalendarMeeting(r) && r.Status == reminderPending && match(copyReminder(r)) {
				n++
				continue
			}
			kept = append(kept, r)
		}
		for i := len(kept); i < len(st.Reminders); i++ {
			st.Reminders[i] = nil
		}
		st.Reminders = kept
		if n == 0 {
			return errNoChange
		}
		return nil
	})
	if err != nil && !errors.Is(err, errNoChange) {
		log.Printf("checkins: withdraw: %v", err)
		return 0
	}
	if n > 0 {
		remindersChanged()
	}
	return n
}

type meetingCand struct {
	key   string
	start time.Time
	due   time.Time
	title string
}

type meetingSlot struct {
	at      time.Time
	id      string
	pending bool
}

// planMeetings turns the fetched events into meeting reminders. It is
// idempotent: every pass recomputes the plan from the calendar, so moved,
// renamed or deleted events and a changed lead time are picked up.
func planMeetings(s *Settings, evs []CalendarEvent, now time.Time) {
	lead := time.Duration(s.CheckinMeetingLead) * time.Minute
	horizon := now.Add(lead + checkinPlanSlack)
	fetchEnd := now.Add(checkinLookAhead)
	paused := checkinsPausedUntil()
	lang := voiceLang(s.PiperVoice)
	owner := calendarOwner()
	if owner != "" && !personExists(owner) {
		owner = ""
	}

	valid := map[string]meetingCand{}
	var cands []meetingCand
	for _, e := range evs {
		// All-day entries and unanswered invitations are not check-ins.
		if e.AllDay || e.NeedsAction || !e.Start.After(now) || e.Start.After(fetchEnd) {
			continue
		}
		title := safeTitle(e.Title)
		if title == "" {
			title = pick(lang, "Appointment", "Termin")
		}
		due := e.Start.Add(-lead)
		if due.Before(now) {
			due = now // added at short notice: announce at once
		}
		c := meetingCand{key: meetingKey(e), start: e.Start, due: due, title: title}
		if _, dup := valid[c.key]; dup {
			continue
		}
		valid[c.key] = c
		cands = append(cands, c)
	}

	// Withdraw what no longer applies: the event disappeared, moved, was
	// declined, or check-ins are paused over its announcement time.
	withdrawMeetingReminders(func(r Reminder) bool {
		if !paused.IsZero() && r.Due.Before(paused) {
			return true
		}
		start, ok := meetingStart(r)
		if !ok {
			return false
		}
		_, still := valid[r.Key]
		return !still && start.After(now) && !start.After(fetchEnd)
	})

	existing := reminderList(func(r *Reminder) bool { return isCalendarMeeting(r) })
	byKey := map[string]Reminder{}
	var slots []meetingSlot
	for _, r := range existing {
		if prev, ok := byKey[r.Key]; !ok || prev.Status == reminderCancelled {
			byKey[r.Key] = r
		}
		switch r.Status {
		case reminderPending:
			slots = append(slots, meetingSlot{at: r.Due, id: r.ID, pending: true})
		case reminderFired, reminderDone:
			at := r.Due
			if r.FiredAt != nil {
				at = *r.FiredAt
			}
			slots = append(slots, meetingSlot{at: at, id: r.ID})
		}
	}

	sort.SliceStable(cands, func(i, j int) bool {
		if !cands[i].due.Equal(cands[j].due) {
			return cands[i].due.Before(cands[j].due)
		}
		if !cands[i].start.Equal(cands[j].start) {
			return cands[i].start.Before(cands[j].start)
		}
		return cands[i].key < cands[j].key
	})

	overflow := map[string]int{}
	for _, c := range cands {
		if r, ok := byKey[c.key]; ok {
			// Already planned (or cancelled by the user: leave it alone).
			// A changed lead time moves a not-yet-due announcement.
			if r.Status == reminderPending && r.Due.After(now) && absDuration(r.Due.Sub(c.due)) > 30*time.Second {
				if _, _, err := rescheduleReminder(r.ID, c.due, 0); err != nil {
					log.Printf("checkins: move %s: %v", r.ID, err)
				}
			}
			continue
		}
		if c.start.After(horizon) || (!paused.IsZero() && c.due.Before(paused)) {
			continue
		}
		var near []meetingSlot
		for _, sl := range slots {
			if absDuration(sl.at.Sub(c.due)) < checkinCapWindow {
				near = append(near, sl)
			}
		}
		if len(near) >= checkinCapCount {
			// Over the cap: fold into the closest earlier pending
			// announcement of that half hour.
			var target *meetingSlot
			for i := range near {
				sl := &near[i]
				if !sl.pending {
					continue
				}
				if target == nil || (!sl.at.After(c.due) && (target.at.After(c.due) || sl.at.After(target.at))) {
					target = sl
				}
			}
			if target == nil {
				continue // the half hour was already announced in full
			}
			overflow[target.id]++
			continue
		}
		r, err := reminderAdd(Reminder{
			Kind: reminderKindMeeting, Text: c.title, Due: c.due, Person: owner, Lang: lang,
			Source: checkinSource, Key: c.key,
		})
		if err != nil {
			log.Printf("checkins: plan meeting: %v", err)
			continue
		}
		byKey[c.key] = r
		slots = append(slots, meetingSlot{at: r.Due, id: r.ID, pending: true})
	}

	// Texts follow the calendar (renames) and carry the merged overflow line.
	for _, r := range reminderList(func(r *Reminder) bool { return isCalendarMeeting(r) && r.Status == reminderPending }) {
		c, ok := valid[r.Key]
		if !ok {
			continue
		}
		want := meetingText(c.title, overflow[r.ID], lang)
		if r.Text == want && r.Lang == lang {
			continue
		}
		if _, _, err := reminderModify(r.ID, func(x *Reminder) error {
			if x.Status != reminderPending {
				return errNoChange
			}
			x.Text, x.Lang = cleanText(want, reminderTextMax), lang
			return nil
		}); err != nil {
			log.Printf("checkins: update %s: %v", r.ID, err)
		}
	}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// ---------------------------------------------------------------------------
// State for the browser (busy, paused, quiet hours)

// checkinBusyUntil is the end of the meeting in progress, zero when none or
// when the calendar state is unknown.
func checkinBusyUntil(now time.Time) time.Time {
	checkinCache.Lock()
	defer checkinCache.Unlock()
	if checkinCache.at.IsZero() || now.Sub(checkinCache.at) > checkinCacheMaxAge || !calendarConnected() {
		return time.Time{}
	}
	var until time.Time
	for _, e := range checkinCache.events {
		if e.AllDay || e.NeedsAction {
			continue
		}
		if !e.Start.After(now) && e.End.After(now) && e.End.After(until) {
			until = e.End
		}
	}
	return until
}

// parseHHMM reads "22:00" as minutes after midnight.
func parseHHMM(s string) (int, bool) {
	if !clockPattern.MatchString(s) {
		return 0, false
	}
	h, _ := strconv.Atoi(s[:2])
	m, _ := strconv.Atoi(s[3:])
	return h*60 + m, true
}

// inQuietHours reports whether now (household time) lies in the quiet
// window [quietStart, quietEnd), which may wrap midnight. Equal start and
// end means no quiet hours.
func inQuietHours(s *Settings, now time.Time) bool {
	if s == nil {
		return false
	}
	start, ok1 := parseHHMM(s.QuietStart)
	end, ok2 := parseHHMM(s.QuietEnd)
	if !ok1 || !ok2 || start == end {
		return false
	}
	now = now.In(settingsLocation(s))
	m := now.Hour()*60 + now.Minute()
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

func rfc3339OrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func checkinStateView() map[string]any {
	s := loadSettings()
	now := nowFunc()
	return map[string]any{
		"busyUntil":   rfc3339OrEmpty(checkinBusyUntil(now)),
		"pausedUntil": rfc3339OrEmpty(checkinsPausedUntil()),
		"quiet":       inQuietHours(s, now),
	}
}

func handleCheckinState(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, checkinStateView())
}

// POST /api/checkins/pause {"minutes":60} | {"today":true} | {"resume":true}
func handleCheckinPause(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Minutes int  `json:"minutes"`
		Today   bool `json:"today"`
		Resume  bool `json:"resume"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, checkinBodyLimit)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSONErrorCode(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large")
		} else {
			writeJSONErrorCode(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		}
		return
	}
	s := loadSettings()
	now := nowFunc().In(settingsLocation(s))
	var until time.Time
	switch {
	case body.Resume:
	case body.Today:
		until = midnightOf(now).AddDate(0, 0, 1)
	case body.Minutes >= 1 && body.Minutes <= int(checkinMaxPause/time.Minute):
		until = now.Add(time.Duration(body.Minutes) * time.Minute)
	default:
		writeJSONErrorCode(w, http.StatusBadRequest, "minutes", "minutes must be 1..1440, or send today or resume")
		return
	}
	if err := applyCheckinPause(until); err != nil {
		writeCheckinError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, checkinStateView())
}

// POST /api/checkins/nudged: a browser just spoke a sitting check-in.
func handleCheckinNudged(w http.ResponseWriter, _ *http.Request) {
	lastNudge.Lock()
	lastNudge.at = nowFunc()
	lastNudge.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func writeCheckinError(w http.ResponseWriter, err error) {
	if errors.Is(err, errStoreReadOnly) {
		writeJSONErrorCode(w, http.StatusServiceUnavailable, "read_only", err.Error())
		return
	}
	writeJSONErrorCode(w, http.StatusInternalServerError, "internal", "could not save the check-in pause")
}

// applyCheckinPause stores the pause (zero = resume), withdraws meeting
// announcements inside it and tells open tabs.
func applyCheckinPause(until time.Time) error {
	if err := setCheckinsPausedUntil(until); err != nil {
		return err
	}
	if !until.IsZero() {
		checkinMu.Lock()
		withdrawMeetingReminders(func(r Reminder) bool { return r.Due.Before(until) })
		checkinMu.Unlock()
	}
	hubPublish("checkins.changed", "", map[string]string{"pausedUntil": rfc3339OrEmpty(until)})
	checkinWake()
	return nil
}

// ---------------------------------------------------------------------------
// Voice: "keine Check-ins mehr heute", "pause check-ins for an hour",
// "Check-ins wieder an", and "nicht jetzt" right after a check-in.

type checkinPauseArgs struct {
	Resume  bool   `json:"resume,omitempty"`
	Minutes int    `json:"minutes,omitempty"` // 0 = until the end of today
	Until   string `json:"until,omitempty"`   // RFC 3339, from "bis 15 Uhr"
}

var (
	checkinPauseWords = []string{
		"pausiere", "pausieren", "pausier", "pause", "pausiert", "stopp", "stoppe", "stop", "keine", "kein",
		"no", "aus", "ausschalten", "off", "deaktiviere", "deaktivieren", "disable", "mute", "silence",
		"nicht mehr", "hold",
	}
	checkinResumeWords = []string{
		"wieder an", "wieder einschalten", "wieder aktivieren", "aktiviere", "aktivieren", "fortsetzen",
		"setze fort", "setz fort", "resume", "unpause", "back on", "turn on", "enable", "einschalten",
	}
	// After commandCore drops "jetzt"/"now": "nicht jetzt", "nein, jetzt
	// nicht", "not now", "not right now".
	checkinNotNow = wordSet("nicht", "nein nicht", "nicht gerade", "gerade nicht", "not", "no not", "not right")
	checkinNumber = map[string]int{
		"ein": 1, "eine": 1, "einer": 1, "einen": 1, "an": 1, "a": 1, "one": 1,
		"zwei": 2, "two": 2, "drei": 3, "three": 3, "vier": 4, "four": 4, "fünf": 5, "five": 5,
		"sechs": 6, "six": 6, "zehn": 10, "ten": 10, "zwanzig": 20, "twenty": 20, "dreißig": 30, "thirty": 30,
	}
)

// checkinTokens joins "check ins" / "check in" into one token so STT
// spellings ("Check-ins", "Checkins", "check ins") look the same.
func checkinTokens(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for i := 0; i < len(tokens); i++ {
		if tokens[i] == "check" && i+1 < len(tokens) && (tokens[i+1] == "ins" || tokens[i+1] == "in") {
			out = append(out, "check"+tokens[i+1])
			i++
			continue
		}
		out = append(out, tokens[i])
	}
	return out
}

// pauseDuration reads "für eine Stunde", "for 30 minutes", "half an hour".
func pauseDuration(tokens []string) time.Duration {
	for i, tok := range tokens {
		var unit time.Duration
		switch tok {
		case "stunde", "stunden", "std", "hour", "hours":
			unit = time.Hour
		case "minute", "minuten", "min", "minutes":
			unit = time.Minute
		default:
			continue
		}
		if unit == time.Hour && i > 0 && (tokens[i-1] == "halbe" || tokens[i-1] == "halben" ||
			(i > 1 && tokens[i-2] == "half")) {
			return 30 * time.Minute
		}
		if i == 0 {
			return unit
		}
		prev := tokens[i-1]
		if n, err := strconv.Atoi(prev); err == nil && n > 0 && n <= 1440 {
			return time.Duration(n) * unit
		}
		if n, ok := checkinNumber[prev]; ok {
			return time.Duration(n) * unit
		}
		return unit
	}
	return 0
}

func lastCheckinAt() time.Time {
	lastNudge.Lock()
	at := lastNudge.at
	lastNudge.Unlock()
	for _, r := range reminderList(func(r *Reminder) bool {
		return isCalendarMeeting(r) && r.FiredAt != nil
	}) {
		if r.FiredAt.After(at) {
			at = *r.FiredAt
		}
	}
	return at
}

func matchCheckinPause(t *Turn) (json.RawMessage, bool) {
	toks := checkinTokens(t.Tokens)
	if len(toks) == 0 || len(toks) > 14 {
		return nil, false
	}
	if checkinNotNow[commandCore(toks)] {
		at := lastCheckinAt()
		if at.IsZero() || nowFunc().Sub(at) > checkinNotNowWindow || nowFunc().Before(at) {
			return nil, false
		}
		return json.RawMessage(fmt.Sprintf(`{"minutes":%d}`, int(checkinNotNowPause/time.Minute))), true
	}
	// "Wie aktiviere ich Check-ins?" is a question, not a command.
	if !hasAnyWord(toks, wordSet("checkins", "checkin")) || hasQuestionWord(toks) {
		return nil, false
	}
	var a checkinPauseArgs
	switch {
	case hasAnyPhrase(toks, checkinResumeWords):
		a.Resume = true
	case hasAnyPhrase(toks, checkinPauseWords):
		if d := pauseDuration(toks); d > 0 {
			a.Minutes = int(d / time.Minute)
		} else if hasAnyWord(toks, wordSet("bis", "until", "till")) && !hasAnyWord(toks, wordSet("morgen", "tomorrow", "heute", "today")) {
			now := turnNow(t)
			if w, ok := parseWhen(t.Query, now, turnLoc(t)); ok && w.HasClock && w.At.Sub(now) <= checkinMaxPause {
				a.Until = w.At.UTC().Format(time.RFC3339)
			}
		}
	default:
		return nil, false
	}
	b, _ := json.Marshal(a)
	return b, true
}

func runCheckinPause(t *Turn, raw json.RawMessage) Reply {
	var a checkinPauseArgs
	_ = json.Unmarshal(raw, &a)
	now := turnNow(t)
	prev := checkinsPausedUntil()
	var until time.Time
	switch {
	case a.Resume:
	case a.Until != "":
		if u, err := time.Parse(time.RFC3339, a.Until); err == nil && u.After(now) {
			until = u
		}
	case a.Minutes > 0:
		m := a.Minutes
		if m > int(checkinMaxPause/time.Minute) {
			m = int(checkinMaxPause / time.Minute)
		}
		until = now.Add(time.Duration(m) * time.Minute)
	}
	endOfDay := midnightOf(now).AddDate(0, 0, 1)
	if !a.Resume && until.IsZero() {
		until = endOfDay
	}
	if err := applyCheckinPause(until); err != nil {
		log.Printf("checkins: pause: %v", err)
		return Reply{Error: true, Text: pick(t.Lang, "I could not pause the check-ins.", "Ich konnte die Check-ins nicht pausieren.")}
	}
	t.PushUndo(pick(t.Lang, "check-in pause", "Check-in-Pause"), func(context.Context) error {
		return applyCheckinPause(prev)
	})
	switch {
	case a.Resume:
		return Reply{Text: pick(t.Lang, "Okay, check-ins are back on.", "Okay, die Check-ins sind wieder an.")}
	case until.Equal(endOfDay):
		return Reply{Text: pick(t.Lang, "Okay, no more check-ins today.", "Okay, heute keine Check-ins mehr.")}
	}
	return Reply{Text: pick(t.Lang, "Okay, no check-ins until ", "Okay, keine Check-ins bis ") + formatWhen(until, now, t.Lang) + "."}
}

// ---------------------------------------------------------------------------
// Status and registration

func checkinStatus(context.Context) any {
	s := loadSettings()
	checkinCache.Lock()
	lastPoll, lastErr := checkinCache.at, checkinCache.err
	checkinCache.Unlock()
	return map[string]any{
		"meetings":    s.CheckinMeetings,
		"connected":   calendarConnected(),
		"lead":        s.CheckinMeetingLead,
		"pausedUntil": rfc3339OrEmpty(checkinsPausedUntil()),
		"lastPoll":    rfc3339OrEmpty(lastPoll),
		"ok":          lastErr == "",
	}
}

func init() {
	registerTool(Tool{Name: "checkins_pause", SideEffect: true, Run: runCheckinPause})
	registerFastPath(FastPath{Name: "checkins_pause", Order: 15, Tool: "checkins_pause", Match: matchCheckinPause})
	registerBackground("checkins", checkinWorker)
	// A new Google client means the calendar must be relinked: re-check at
	// once so stale check-ins are withdrawn. gcal.go also wakes the worker
	// on every calendar.changed (connect, disconnect, relink, add, delete).
	onGoogleClientChange(checkinWake)
	registerStatus("checkins", checkinStatus)
	registerRoute("GET /api/checkins/state", handleCheckinState)
	registerRoute("POST /api/checkins/pause", handleCheckinPause)
	registerRoute("POST /api/checkins/nudged", handleCheckinNudged)
}
