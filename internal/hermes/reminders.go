package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// reminders.go: reminders and timers (SPEC 3.5 and Section 9, Feature 1).
//
//   - Store: data/reminders.json (0600, version 2), at most 500 pending;
//     fired, done and cancelled entries are pruned after 30 days. Version 2
//     added the optional repeat rule; version 1 files load as they are.
//   - Repeats: a reminder with a Rule is one series record. When an
//     occurrence is acknowledged ("Erledigt", "okay", POST done) or its
//     announcements end unanswered, the same record moves on to the next
//     occurrence (pending again, new Due). "Lösch die Erinnerung" / DELETE
//     stops the whole series; "skip" moves past the next occurrence.
//   - Voice: deterministic fast paths (set, list, cancel, "wie lange noch?",
//     acknowledge, snooze, "nein, abends" fix-up) and router tools
//     (set_reminder, set_timer, reminder_list, reminder_cancel). Times are
//     always parsed in Go (parseWhen) and spoken back as absolute times.
//   - Scheduler: one background worker sleeps until the next due time (woken
//     on every change, 30 s safety tick), then publishes a durable
//     "reminder.due" hub event. Reminders and timers are re-announced every
//     60 s (at most 5 times) until acknowledged.
//   - Access (4.10): setting is open (guests set household reminders);
//     listing one's own reminders needs Read; cancelling or snoozing a
//     personal reminder needs Write, so guests can cancel household timers only.
//
// The morning briefing (batch 2) lists open reminders through
// openReminders(personID) and reminderLine.

type remindersFile struct {
	Version   int         `json:"version"` // 2
	Reminders []*Reminder `json:"reminders"`
}

// remindersVersion is the current file format: 2 added Reminder.Rule.
const remindersVersion = 2

// UnmarshalJSON migrates older files on load: version 1 has no rules, so
// only the version moves on (it is written with the next change). A file
// from a newer HERMES keeps its version; the store opens it read-only.
func (f *remindersFile) UnmarshalJSON(b []byte) error {
	type plain remindersFile
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*f = remindersFile(p)
	if f.Version < remindersVersion {
		f.Version = remindersVersion
	}
	kept := f.Reminders[:0]
	for _, r := range f.Reminders {
		if r == nil {
			continue
		}
		if r.Rule != nil && r.Rule.validate() != nil {
			log.Printf("reminders: dropping an invalid repeat rule of %q", truncateRunes(r.ID, 40))
			r.Rule = nil
		} else if r.Rule != nil {
			r.Rule.normalize()
		}
		kept = append(kept, r)
	}
	f.Reminders = kept
	return nil
}

type Reminder struct {
	ID        string     `json:"id"`        // "r_…"
	Kind      string     `json:"kind"`      // "reminder" | "timer" | "meeting"
	Text      string     `json:"text"`      // may be "" for timers
	Due       time.Time  `json:"due"`       // absolute, stored UTC, rendered in settings tz
	Person    string     `json:"person"`    // "" = household / unknown
	Lang      string     `json:"lang"`      // "de" | "en"
	Status    string     `json:"status"`    // "pending" | "fired" | "done" | "cancelled"
	CreatedAt time.Time  `json:"createdAt"` //
	FiredAt   *time.Time `json:"firedAt,omitempty"`
	EventID   int64      `json:"eventId,omitempty"`   // hub event id when fired
	Source    string     `json:"source"`              // "voice" | "ui" | "calendar"
	Key       string     `json:"key,omitempty"`       // dedupe key for meeting check-ins: calEventID|startRFC3339
	DurationS int        `json:"durationS,omitempty"` // timers: original duration, for the spoken line
	Private   bool       `json:"private,omitempty"`   // speak only "Anna, du hast eine Erinnerung."
	ClientID  string     `json:"clientId,omitempty"`  // device that set it
	Repeats   int        `json:"repeats,omitempty"`   // re-announcements so far (max 5, every 60 s, until acked)
	Rule      *RecurRule `json:"rule,omitempty"`      // repeat rule (v2); Due is the current occurrence
}

const (
	reminderKindReminder = "reminder"
	reminderKindTimer    = "timer"
	reminderKindMeeting  = "meeting"

	reminderPending   = "pending"
	reminderFired     = "fired"
	reminderDone      = "done"
	reminderCancelled = "cancelled"
)

const (
	reminderMaxPending    = 500
	reminderKeep          = 30 * 24 * time.Hour
	reminderTextMax       = 200
	reminderRepeatEvery   = 60 * time.Second
	reminderMaxRepeats    = 5
	reminderAckWindow     = 5 * time.Minute
	reminderFixWindow     = 2 * time.Minute
	reminderSnoozeWindow  = 10 * time.Minute
	reminderSnoozeDefault = 10 * time.Minute
	reminderLateAfter     = 2 * time.Minute
	reminderBodyLimit     = 64 << 10
	reminderListMax       = 5
)

// reminderSafetyTick bounds every scheduler sleep (clock jumps, lost wakes).
var reminderSafetyTick = 30 * time.Second

var (
	errReminderNotFound = errors.New("unknown reminder")
	errReminderLimit    = errors.New("too many pending reminders")
	errReminderWhen     = errors.New("the time is in the past or more than 366 days ahead")
	errReminderNoRule   = errors.New("the reminder does not repeat")
)

var remindersStore = &fileStore[remindersFile]{
	Name:    "reminders.json",
	Version: remindersVersion,
	New:     func() *remindersFile { return &remindersFile{Version: remindersVersion} },
}

// ---------------------------------------------------------------------------
// Store

// reminderList returns copies of the reminders filter accepts (all for nil).
func reminderList(filter func(*Reminder) bool) []Reminder {
	var out []Reminder
	if err := remindersStore.View(func(st *remindersFile) error {
		for _, r := range st.Reminders {
			if filter == nil || filter(r) {
				out = append(out, copyReminder(r))
			}
		}
		return nil
	}); err != nil {
		log.Printf("reminders: %v", err)
	}
	sortReminders(out)
	return out
}

func copyReminder(r *Reminder) Reminder {
	c := *r
	if r.FiredAt != nil {
		f := *r.FiredAt
		c.FiredAt = &f
	}
	c.Rule = r.Rule.clone()
	return c
}

// householdLoc is the settings time zone, the fallback for rules without one.
func householdLoc() *time.Location { return settingsLocation(loadSettings()) }

// nextOccurrence is the occurrence after the current one: after the later of
// Due and now, so a late or snoozed occurrence never comes back and a series
// skips what it missed while HERMES was off.
func nextOccurrence(r *Reminder, now time.Time, loc *time.Location) (time.Time, bool) {
	if r.Rule == nil {
		return time.Time{}, false
	}
	after := r.Due
	if now.After(after) {
		after = now
	}
	return r.Rule.Next(after, loc)
}

// advanceOccurrence ends the current occurrence of a series: the record
// becomes pending at the next occurrence, or done when there is none. It
// keeps EventID (the caller acks it, or the scheduler does on the next fire)
// and reports whether a next occurrence exists.
func advanceOccurrence(r *Reminder, now time.Time, loc *time.Location) bool {
	next, ok := nextOccurrence(r, now, loc)
	if !ok {
		r.Status = reminderDone
		return false
	}
	r.Status, r.Due, r.FiredAt, r.Repeats = reminderPending, next.UTC(), nil, 0
	return true
}

func sortReminders(rs []Reminder) {
	sort.SliceStable(rs, func(i, j int) bool {
		if !rs[i].Due.Equal(rs[j].Due) {
			return rs[i].Due.Before(rs[j].Due)
		}
		return rs[i].CreatedAt.Before(rs[j].CreatedAt)
	})
}

func reminderGet(id string) (Reminder, bool) {
	rs := reminderList(func(r *Reminder) bool { return r.ID == id })
	if len(rs) == 0 {
		return Reminder{}, false
	}
	return rs[0], true
}

// reminderLastActive is when a reminder last changed state, for pruning.
func reminderLastActive(r *Reminder) time.Time {
	t := r.Due
	if r.CreatedAt.After(t) {
		t = r.CreatedAt
	}
	if r.FiredAt != nil && r.FiredAt.After(t) {
		t = *r.FiredAt
	}
	return t
}

// pruneReminders drops finished entries older than 30 days.
func pruneReminders(st *remindersFile, now time.Time) bool {
	kept := st.Reminders[:0]
	changed := false
	for _, r := range st.Reminders {
		if r.Status != reminderPending && now.Sub(reminderLastActive(r)) > reminderKeep {
			changed = true
			continue
		}
		kept = append(kept, r)
	}
	for i := len(kept); i < len(st.Reminders); i++ {
		st.Reminders[i] = nil
	}
	st.Reminders = kept
	return changed
}

// remindersChanged wakes the scheduler and tells open tabs to refresh.
func remindersChanged() {
	wakeReminders()
	hubPublish("reminders.changed", "", struct{}{})
}

// reminderAdd stores a new pending reminder. A person deleted meanwhile is
// refused (write-after-delete guard, 4.8).
func reminderAdd(r Reminder) (Reminder, error) {
	now := nowFunc()
	r.ID = newID("r")
	r.Status = reminderPending
	r.CreatedAt = now.UTC()
	r.Due = r.Due.UTC()
	r.FiredAt, r.EventID, r.Repeats = nil, 0, 0
	r.Text = cleanText(r.Text, reminderTextMax)
	if r.Kind == "" {
		r.Kind = reminderKindReminder
	}
	if r.Rule != nil {
		if r.Rule.validate() != nil {
			return Reminder{}, errRecurRule
		}
		r.Rule = r.Rule.clone() // the stored rule is never shared with the caller
		r.Rule.normalize()
	}
	err := remindersStore.Update(func(st *remindersFile) error {
		if r.Person != "" && !personExists(r.Person) {
			return errPersonGone
		}
		pruneReminders(st, now)
		pending := 0
		for _, x := range st.Reminders {
			if x.Status == reminderPending {
				pending++
			}
		}
		if pending >= reminderMaxPending {
			return errReminderLimit
		}
		c := r
		c.Rule = r.Rule.clone()
		st.Reminders = append(st.Reminders, &c)
		return nil
	})
	if err != nil {
		return Reminder{}, err
	}
	remindersChanged()
	return r, nil
}

// reminderModify changes one reminder under the store lock; fn returns
// errNoChange to leave it alone. It returns the state before and after.
func reminderModify(id string, fn func(r *Reminder) error) (before, after Reminder, err error) {
	changed := false
	err = remindersStore.Update(func(st *remindersFile) error {
		for _, r := range st.Reminders {
			if r.ID != id {
				continue
			}
			before = copyReminder(r)
			if err := fn(r); err != nil {
				*r = copyReminder(&before) // the state stays unchanged on any error
				after = before
				return err
			}
			after = copyReminder(r)
			changed = true
			return nil
		}
		return errReminderNotFound
	})
	if err == nil && changed {
		remindersChanged()
	}
	return before, after, err
}

// reminderRestore puts back an earlier state of a reminder (undo).
func reminderRestore(prev Reminder) error {
	_, _, err := reminderModify(prev.ID, func(r *Reminder) error {
		*r = copyReminder(&prev)
		return nil
	})
	return err
}

// settleReminder ends a reminder (done or cancelled): the hub event is
// acked so it is not replayed, and every tab stops chiming. "done" on a
// series completes only the current occurrence (the record moves on to the
// next one); "cancelled" stops the whole series.
func settleReminder(id, status string) (before, after Reminder, err error) {
	now, loc := nowFunc(), householdLoc()
	before, after, err = reminderModify(id, func(r *Reminder) error {
		if r.Status == status || (r.Rule != nil && r.Status == reminderCancelled) {
			return errNoChange
		}
		if status == reminderDone && r.Rule != nil && r.Status != reminderDone {
			advanceOccurrence(r, now, loc)
			r.EventID = 0 // acked below
			return nil
		}
		r.Status = status
		return nil
	})
	if err != nil {
		return before, after, err
	}
	stopAnnouncement(before)
	return before, after, nil
}

// stopAnnouncement acks the hub event of a reminder's last announcement and
// tells every tab to stop chiming when it was ringing.
func stopAnnouncement(before Reminder) {
	if before.EventID > 0 {
		hubAck([]int64{before.EventID})
	}
	if before.Status == reminderFired {
		hubPublish("reminder.acked", before.Person, map[string]string{"reminderId": before.ID})
	}
}

// skipOccurrence moves a series past its current occurrence ("skip next";
// "nur die nächste auslassen"). One-off reminders have nothing to skip.
func skipOccurrence(id string) (before, after Reminder, err error) {
	now, loc := nowFunc(), householdLoc()
	before, after, err = reminderModify(id, func(r *Reminder) error {
		switch {
		case r.Rule == nil:
			return errReminderNoRule
		case r.Status != reminderPending && r.Status != reminderFired:
			return errReminderNotFound
		}
		advanceOccurrence(r, now, loc)
		r.EventID = 0
		return nil
	})
	if err != nil {
		return before, after, err
	}
	stopAnnouncement(before)
	return before, after, nil
}

// rescheduleReminder makes a reminder pending again at due (snooze, fix-up,
// API edits) and stops the old announcement.
func rescheduleReminder(id string, due time.Time, durationS int) (before, after Reminder, err error) {
	before, after, err = reminderModify(id, func(r *Reminder) error {
		if r.Status == reminderCancelled {
			return errReminderNotFound
		}
		r.Status, r.Due, r.FiredAt, r.Repeats, r.EventID = reminderPending, due.UTC(), nil, 0, 0
		if durationS > 0 {
			r.DurationS = durationS
		}
		return nil
	})
	if err != nil {
		return before, after, err
	}
	stopAnnouncement(before)
	return before, after, nil
}

// clearReminderPerson is the person-deletion cascade (4.8): their reminders
// stay as household tasks and are no longer spoken with a name.
func clearReminderPerson(personID string) (string, int) {
	if personID == "" {
		return "reminders", 0
	}
	n := 0
	err := remindersStore.Update(func(st *remindersFile) error {
		for _, r := range st.Reminders {
			if r.Person == personID {
				r.Person = ""
				n++
			}
		}
		if n == 0 {
			return errNoChange
		}
		return nil
	})
	if err != nil {
		log.Printf("reminders: cascade for deleted person: %v", err)
		return "reminders", 0
	}
	if n > 0 {
		remindersChanged()
	}
	return "reminders", n
}

// openReminders lists the pending reminders of a person plus the household
// ones, soonest first. The morning briefing (batch 2) builds on it.
func openReminders(personID string) []Reminder {
	return reminderList(func(r *Reminder) bool {
		return r.Status == reminderPending && (r.Person == "" || (personID != "" && r.Person == personID))
	})
}

// ---------------------------------------------------------------------------
// Scheduler

var reminderWake = make(chan struct{}, 1)

// wakeReminders makes the scheduler recompute its next wake-up.
func wakeReminders() {
	select {
	case reminderWake <- struct{}{}:
	default:
	}
}

// reminderScheduler is the background worker (registerBackground): sleep
// until the next due time or a change, fire, repeat.
func reminderScheduler(ctx context.Context) {
	for {
		next := reminderPass(nowFunc())
		wait := reminderSafetyTick
		if !next.IsZero() {
			if d := next.Sub(nowFunc()); d < wait {
				wait = d
			}
		}
		if wait < 10*time.Millisecond {
			wait = 10 * time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-reminderWake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// reminderLastAnnounced is when the newest announcement went out.
func reminderLastAnnounced(r *Reminder) time.Time {
	if r.FiredAt == nil {
		return time.Time{}
	}
	return r.FiredAt.Add(time.Duration(r.Repeats) * reminderRepeatEvery)
}

// reminderRepeats reports whether this kind is re-announced until acked.
func reminderRepeats(r *Reminder) bool {
	return r.Kind == reminderKindReminder || r.Kind == reminderKindTimer
}

// reminderPass fires every due reminder and re-announces unacknowledged
// ones; it returns when the next pass is needed (zero: nothing scheduled).
// Overdue reminders after a restart fire at once.
func reminderPass(now time.Time) time.Time {
	loc := householdLoc()
	var next time.Time
	soon := func(t time.Time) {
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	announced := false
	err := remindersStore.Update(func(st *remindersFile) error {
		changed := pruneReminders(st, now)
		for _, r := range st.Reminders {
			switch r.Status {
			case reminderPending:
				if r.Due.After(now) {
					soon(r.Due)
					continue
				}
				if r.EventID > 0 {
					// The previous occurrence of a series was never acknowledged:
					// its event stayed for replay until now.
					hubAck([]int64{r.EventID})
				}
				fired := now.UTC()
				r.Status, r.FiredAt, r.Repeats = reminderFired, &fired, 0
				r.EventID = announceReminder(r, 0, now)
				changed, announced = true, true
				switch {
				case reminderRepeats(r):
					soon(fired.Add(reminderRepeatEvery))
				case r.Rule != nil:
					soon(fired.Add(reminderAckWindow))
				}
			case reminderFired:
				if r.FiredAt == nil {
					continue
				}
				if reminderRepeats(r) && r.Repeats < reminderMaxRepeats {
					nextAt := reminderLastAnnounced(r).Add(reminderRepeatEvery)
					switch {
					case nextAt.After(now):
						soon(nextAt)
					case now.Sub(nextAt) > 2*reminderRepeatEvery:
						// HERMES was not running: the durable event is replayed
						// on the next connect; no burst of stale repeats.
						r.Repeats = reminderMaxRepeats
						changed = true
					default:
						old := r.EventID
						r.Repeats++
						r.EventID = announceReminder(r, r.Repeats, now)
						if old > 0 && old != r.EventID {
							hubAck([]int64{old})
						}
						changed, announced = true, true
						if r.Repeats < reminderMaxRepeats {
							soon(reminderLastAnnounced(r).Add(reminderRepeatEvery))
						}
					}
				}
				if r.Rule != nil && (!reminderRepeats(r) || r.Repeats >= reminderMaxRepeats) {
					// The announcements of this occurrence are over and nobody
					// answered: once "okay" can no longer reach it, the series
					// moves on. The unacked event stays for replay ("missed").
					endAt := reminderLastAnnounced(r).Add(reminderAckWindow)
					if endAt.After(now) {
						soon(endAt)
						continue
					}
					if advanceOccurrence(r, now, loc) {
						soon(r.Due)
					}
					changed, announced = true, true
				}
			}
		}
		if !changed {
			return errNoChange
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errStoreReadOnly) {
			return time.Time{} // the store already logged it; nothing can fire
		}
		log.Printf("reminders: scheduler: %v", err)
		return now.Add(5 * time.Second)
	}
	if announced {
		hubPublish("reminders.changed", "", struct{}{})
	}
	return next
}

// announceReminder publishes the durable "reminder.due" event and returns
// its id. Private reminders carry no text on the wire (3.2).
func announceReminder(r *Reminder, repeat int, now time.Time) int64 {
	name, _ := personName(r.Person)
	text := r.Text
	if r.Private {
		text = ""
	}
	data := map[string]any{
		"reminderId": r.ID,
		"kind":       r.Kind,
		"text":       text,
		"due":        r.Due.UTC().Format(time.RFC3339),
		"personName": name,
		"speak":      reminderSpeech(*r, name, repeat, now),
		"lang":       reminderLang(r),
		"private":    r.Private,
		"repeat":     repeat,
		"chime":      true,
		"clientId":   r.ClientID,
		"late":       repeat == 0 && now.Sub(r.Due) > reminderLateAfter,
		"recurring":  r.Rule != nil,
	}
	if r.Rule != nil && !r.Private {
		data["ruleText"] = r.Rule.Describe(reminderLang(r))
	}
	return hubPublishDurable("reminder.due", r.Person, data)
}

func reminderLang(r *Reminder) string {
	if r.Lang == "de" || r.Lang == "en" {
		return r.Lang
	}
	return voiceLang(loadSettings().PiperVoice)
}

// speechText makes stored text safe to speak: one line, no controls, no
// URLs, capped.
func speechText(s string) string {
	s = strings.Join(strings.Fields(cleanText(s, reminderTextMax)), " ")
	s = urlPattern.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// sentence ends s with a full stop unless it already ends a sentence.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ".") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "?") {
		return s
	}
	return s + "."
}

// reminderSpeech is the sentence a tab speaks when the reminder is due.
func reminderSpeech(r Reminder, name string, repeat int, now time.Time) string {
	lang := reminderLang(&r)
	name = safeTitle(name)
	text := speechText(r.Text)
	var s string
	switch r.Kind {
	case reminderKindTimer:
		dur := ""
		if r.DurationS > 0 {
			dur = formatDuration(time.Duration(r.DurationS)*time.Second, lang)
		}
		switch {
		case lang == "de" && dur != "":
			s = "dein Timer über " + dur + " ist abgelaufen"
		case lang == "de":
			s = "dein Timer ist abgelaufen"
		case dur != "":
			s = "your timer for " + dur + " is done"
		default:
			s = "your timer is done"
		}
		if text != "" && !r.Private {
			s += ": " + text
		}
		if name != "" {
			s = name + ", " + s
		} else {
			s = strings.ToUpper(s[:1]) + s[1:]
		}
		s = sentence(s)
	case reminderKindMeeting:
		title := safeTitle(r.Text)
		mins := 0
		if parts := strings.SplitN(r.Key, "|", 2); len(parts) == 2 {
			if start, err := time.Parse(time.RFC3339, parts[1]); err == nil {
				mins = int(math.Ceil(start.Sub(now).Minutes()))
			}
		}
		if mins < 2 {
			s = sentence(pick(lang, "Coming up: ", "Gleich: ") + title)
		} else {
			s = sentence(fmt.Sprintf(pick(lang, "In %d minutes: ", "In %d Minuten: "), mins) + title)
		}
	default:
		switch {
		case r.Private && name != "":
			s = name + pick(lang, ", you have a reminder.", ", du hast eine Erinnerung.")
		case r.Private:
			s = pick(lang, "You have a reminder.", "Du hast eine Erinnerung.")
		case text == "" && name != "":
			s = name + pick(lang, ", your reminder is due.", ", deine Erinnerung ist fällig.")
		case text == "":
			s = pick(lang, "Your reminder is due.", "Deine Erinnerung ist fällig.")
		case name != "":
			s = sentence(name + pick(lang, ", reminder: ", ", Erinnerung: ") + text)
		default:
			s = sentence(pick(lang, "Reminder: ", "Erinnerung: ") + text)
		}
	}
	if repeat > 0 {
		s = pick(lang, "Again: ", "Noch einmal: ") + s
	}
	return s
}

// ---------------------------------------------------------------------------
// Per-device memory for the fix-up ("nein, abends") and snooze paths

type reminderSetNote struct {
	id string
	at time.Time
}

var reminderSets = struct {
	sync.Mutex
	m map[string]reminderSetNote
}{m: map[string]reminderSetNote{}}

func noteReminderSet(clientID, id string) {
	if clientID == "" {
		return
	}
	now := nowFunc()
	reminderSets.Lock()
	defer reminderSets.Unlock()
	for k, v := range reminderSets.m {
		if now.Sub(v.at) > reminderFixWindow {
			delete(reminderSets.m, k)
		}
	}
	reminderSets.m[clientID] = reminderSetNote{id: id, at: now}
}

// lastReminderSet is the reminder this device set within the last 2 minutes.
func lastReminderSet(clientID string) (string, bool) {
	reminderSets.Lock()
	defer reminderSets.Unlock()
	n, ok := reminderSets.m[clientID]
	if !ok || nowFunc().Sub(n.at) > reminderFixWindow {
		return "", false
	}
	return n.id, true
}

// ---------------------------------------------------------------------------
// Turn helpers

func turnLoc(t *Turn) *time.Location {
	if t.Loc != nil {
		return t.Loc
	}
	return settingsLocation(t.Settings)
}

func turnNow(t *Turn) time.Time {
	now := t.Now
	if now.IsZero() {
		now = nowFunc()
	}
	return now.In(turnLoc(t))
}

// reminderOwner is who a voice-set reminder belongs to: any attributed
// person (sticky included, 4.10: sticky attributes open actions), else the
// household.
func reminderOwner(t *Turn) string {
	if t.Speaker.Known() && personExists(t.Speaker.ID) {
		return t.Speaker.ID
	}
	return ""
}

// visibleReminders are what this speaker may see or touch: household ones,
// plus their own when the access class allows. Meeting check-ins carry
// calendar titles, and calendar data needs Read (4.10) even when the
// calendar belongs to the household (Person ""), so they follow the access
// class too.
func visibleReminders(t *Turn, a Access, filter func(*Reminder) bool) []Reminder {
	allowed := t.Can(a)
	own := ""
	if t.Speaker.Known() && allowed {
		own = t.Speaker.ID
	}
	return reminderList(func(r *Reminder) bool {
		if r.Person != "" && r.Person != own {
			return false
		}
		if r.Kind == reminderKindMeeting && !allowed {
			return false
		}
		return filter == nil || filter(r)
	})
}

// ---------------------------------------------------------------------------
// Matching

var (
	setCues = []string{
		"erinner mich", "erinnere mich", "erinner uns", "erinnere uns", "erinnert mich", "erinnerst du mich",
		"remind me", "remind us", "weck mich", "wecke mich", "weck uns", "wecke uns", "wake me", "wake us",
	}
	politeErinnern = []string{"kannst du mich", "könntest du mich", "würdest du mich", "kannst du uns",
		"könntest du uns", "bitte mich", "du sollst mich", "sollst du mich"}
	timerNouns    = wordSet("timer", "countdown", "stoppuhr", "eieruhr", "kurzzeitwecker")
	alarmNouns    = wordSet("wecker", "alarm")
	reminderNouns = wordSet("erinnerung", "reminder")
	createVerbs   = wordSet("stell", "stelle", "stellen", "setz", "setze", "setzen", "start", "starte", "starten",
		"mach", "mache", "machen", "set", "create", "make", "add", "leg", "lege", "erstell", "erstelle", "erstellen")
	privateWords = wordSet("privat", "leise", "private", "privately", "quietly", "silently")
	wakeWords    = wordSet("weck", "wecke", "wake")

	// "vergiss/forget" + a reminder noun cancels; memory_forget (memory.go,
	// order 31) skips utterances that name a reminder noun (matchForget).
	cancelVerbs = []string{"vergiss", "vergesse", "vergessen", "forget", "lösch", "lösche", "löschen", "entferne", "entfernen", "entfern", "streich", "streiche",
		"streichen", "cancel", "delete", "remove", "stopp", "stop", "stoppe", "stoppen", "beende", "beenden",
		"abbrechen", "brich … ab", "mach … aus", "schalte … aus", "schalt … aus", "timer aus", "wecker aus", "alarm aus",
		"turn off", "switch off", "clear",
		"brauche … nicht mehr", "don t need",
		"überspring", "überspringe", "überspringen", "skip", "auslassen", "lass … aus", "lass … ausfallen"}
	// Scope of a cancel request on a series.
	occurrenceCues = []string{"nur die nächste", "nur die nächsten", "nur diese", "nur dieses mal", "nur diesmal", "nur heute",
		"nur morgen", "nur einmal", "diesmal", "dieses mal", "die nächste", "nur die eine", "überspring", "überspringe",
		"überspringen", "auslassen", "lass … aus", "ausfallen", "this time", "this one", "just the next", "only the next",
		"the next one", "next one", "just once", "just today", "only today", "skip", "only this"}
	seriesCues = []string{"serie", "die ganze", "ganze serie", "komplett", "für immer", "dauerhaft", "ganz löschen",
		"wiederkehrende", "series", "whole", "for good", "forever", "entirely", "completely",
		"all of them", "every time", "repeating", "recurring", "altogether", "alles", "alle", "all"}
	// "serie"/"series": "lösch die ganze Serie" said on its own reached the
	// persona model, which claimed a deletion that never ran.
	cancelNouns = wordSet("erinnerung", "erinnerungen", "timer", "wecker", "alarm", "countdown", "reminder",
		"reminders", "timers", "alarms", "countdowns", "serie", "serien", "series")
	listCues = []string{"welche erinnerungen", "welche timer", "was für erinnerungen", "meine erinnerungen",
		"habe ich erinnerungen", "hab ich erinnerungen", "habe ich noch erinnerungen", "hab ich noch erinnerungen",
		"gibt es erinnerungen", "erinnerungen anzeigen", "zeig … erinnerungen", "liste … erinnerungen",
		"läuft … timer", "laufen … timer", "laufende timer", "aktive timer", "welche wecker",
		"what reminders", "which reminders", "my reminders", "any reminders", "list … reminders",
		"show … reminders", "what timers", "which timers", "any timers", "timers running", "timer running",
		"is there a timer", "upcoming reminders", "anstehende erinnerungen"}
	remainingCues = []string{"wie lange noch", "wie lange läuft", "wie lang noch", "wie viel zeit", "wieviel zeit",
		"wann klingelt", "wann ist der timer", "wann läuft … ab", "wann ist … abgelaufen",
		"how long left", "how much time", "how long until", "how long is left", "time left", "how long on",
		"when does the timer", "when will the timer", "how long does the timer"}
	ackUtterances = wordSet("okay", "ok", "danke", "danke schön", "dankeschön", "stopp", "stop", "erledigt",
		"ist erledigt", "got it", "thanks", "thank you", "done", "alles klar", "verstanden", "schon gut", "aus",
		"ruhe", "ok danke", "okay danke", "ok thanks", "okay thanks")
	fixLeads    = wordSet("nein", "no", "nee", "nö", "ne", "doch", "lieber", "sorry", "nope", "besser", "actually", "rather")
	snoozeWords = wordSet("schlummern", "schlummer", "snooze", "schlummerfunktion")
)

// reminderQuery is the command without wake word and polite fillers.
func reminderQuery(t *Turn) (string, []string) {
	q := stripCommand(t.Query)
	return q, tokenize(q)
}

// hasSetVerb: "erinnere mich", "remind me", "weck mich", or the polite
// "kannst du mich … erinnern" (matched on the full utterance, because
// stripCommand drops the leading "kannst du").
func hasSetVerb(t *Turn, toks []string) bool {
	return hasAnyCue(toks, setCues) || (hasPhrase(t.Tokens, "erinnern") && hasAnyCue(t.Tokens, politeErinnern))
}

func isCancelRequest(tokens []string) bool {
	return hasAnyWord(tokens, cancelNouns) && hasAnyCue(tokens, cancelVerbs)
}

func isListRequest(tokens []string) bool {
	return len(tokens) <= 12 && hasAnyCue(tokens, listCues)
}

func isRemainingRequest(tokens []string) bool {
	if !hasAnyCue(tokens, remainingCues) {
		return false
	}
	if hasAnyWord(tokens, timerNouns) || hasAnyWord(tokens, reminderNouns) || hasAnyWord(tokens, alarmNouns) {
		return true
	}
	// A bare "wie lange noch?" only right after the question words.
	return len(strings.Fields(commandCore(tokens))) <= 4
}

// reminderArgs are the set_reminder / set_timer arguments (router schema
// plus fast-path fields).
type reminderArgs struct {
	WhenText     string `json:"when_text,omitempty"`
	WhenISO      string `json:"when_iso,omitempty"`
	DurationText string `json:"duration_text,omitempty"` // set_timer
	Text         string `json:"text,omitempty"`
	Label        string `json:"label,omitempty"` // set_timer
	Kind         string `json:"kind,omitempty"`  // "reminder" | "timer"
	Private      bool   `json:"private,omitempty"`
	Recurring    bool   `json:"recurring,omitempty"` // repeat words were heard ("jeden", "every", "immer wenn")
	Fast         bool   `json:"fast,omitempty"`      // text was already derived from the utterance
	Wake         bool   `json:"wake,omitempty"`      // "weck mich": default text "aufstehen"
}

// matchReminderSet is fast path 40: a reminder or timer verb, with or
// without a time (no time: slot question).
func matchReminderSet(t *Turn) (json.RawMessage, bool) {
	q, toks := reminderQuery(t)
	if len(toks) == 0 || len(toks) > 60 {
		return nil, false
	}
	verb := hasSetVerb(t, toks)
	if !verb && (isCancelRequest(toks) || isListRequest(toks) || isRemainingRequest(toks)) {
		return nil, false
	}
	// "disarm the alarm", "Alarmanlage unscharf schalten": a security verb
	// means the alarm system, which home_control refuses; not an alarm clock.
	if !verb && hasAnyWord(haFold(q), haBlockedVerbs) {
		return nil, false
	}
	timer := hasAnyWord(toks, timerNouns)
	alarm := hasAnyWord(toks, alarmNouns)
	noun := timer || alarm || hasAnyWord(toks, reminderNouns)
	if !verb && !noun {
		return nil, false
	}
	w, ok := parseWhen(q, turnNow(t), turnLoc(t))
	rec, recOK := parseRecurring(q, turnNow(t), turnLoc(t))
	if recOK {
		ok = true
	}
	if !verb {
		// A bare noun ("Timer", "Erinnerung") needs a time, a creation verb or
		// a very short utterance: "Ich hab einen Timer gekauft" is no command.
		if !ok && !hasAnyWord(toks, createVerbs) && len(strings.Fields(commandCore(toks))) > 3 {
			return nil, false
		}
		if isQuestion(t) && !ok {
			return nil, false
		}
	}
	a := reminderArgs{WhenText: q, Fast: true, Kind: reminderKindReminder}
	wake := hasAnyWord(toks, wakeWords) || alarm
	remindVerb := hasAnyCue(toks, []string{"erinner", "erinnere", "erinnert", "erinnerst", "erinnern", "remind"})
	switch {
	case timer && !remindVerb: // "erinnere mich, den Timer zu stoppen" is a reminder
		a.Kind = reminderKindTimer
	case alarm && ok && !recOK && w.Duration > 0:
		a.Kind = reminderKindTimer
	}
	if recOK && a.Kind == reminderKindTimer {
		a.Kind = reminderKindReminder // "jeden Tag um 7 ein Timer" is a repeating reminder
	}
	a.Wake = wake && a.Kind == reminderKindReminder
	a.Private = hasAnyWord(toks, privateWords)
	a.Recurring = recOK || whenRecurring(q)
	rest := q
	switch {
	case recOK:
		rest = rec.Rest
	case ok:
		rest = w.Rest
	}
	a.Text = reminderTextFrom(rest, t.Lang, a.Kind == reminderKindTimer, !remindVerb)
	if !ok && a.Wake {
		// "Stell einen Wecker auf 7", "set an alarm for 6:30": an alarm's
		// bare number is its clock time, not the task.
		if m := reAlarmClock.FindStringSubmatch(a.Text); m != nil {
			clock := m[1]
			if m[2] != "" {
				clock += ":" + m[2]
			}
			// "wecker" keeps the wake-up reading (7 is 7:00, not 19:00).
			when := "wecker um " + clock + " uhr"
			if _, good := parseWhen(when, turnNow(t), turnLoc(t)); good {
				a.WhenText, a.Text = when, ""
			}
		}
	}
	b, _ := json.Marshal(a)
	return b, true
}

// reAlarmClock is what is left of an alarm request with a bare clock time.
var reAlarmClock = regexp.MustCompile(`(?i)^(?:(?:auf|um|für|fuer|for|at)\s+)?(\d{1,2})(?:[:.](\d{2}))?$`)

// stripAlt builds a pattern that removes whole phrases wherever they are.
func stripAlt(phrases ...string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(?:^|[\s,;:!?.]+)(?:` + strings.Join(phrases, "|") + `)(?:[\s,;:!?.]+|$)`)
}

var (
	// reStripVerbs removes the command verbs and fillers of a reminder
	// sentence; reminderTextFrom keeps the task.
	reStripVerbs = stripAlt(
		`(?:hey\s+)?hermes`, `bitte`, `please`, `mal`, `doch`, `danke`,
		`(?:kannst|könntest|würdest)\s+du\s+(?:mich|uns)`, `(?:can|could|would)\s+you`, `du\s+sollst\s+mich`,
		`erinnere?\s+(?:mich|uns)`, `erinnert\s+mich`, `erinnerst\s+du\s+mich`, `(?:mich|uns)\s+erinnern`, `erinnern`,
		`wecke?\s+(?:mich|uns)(?:\s+auf)?`, `wake\s+(?:me|us)(?:\s+up)?`, `remind\s+(?:me|us)`,
		`(?:stell|stelle|setz|setze|starte?|mach|mache|leg|lege|erstelle?)\s+(?:mir\s+|uns\s+)?(?:einen\s+|ein\s+|eine\s+|den\s+|die\s+|das\s+)?(?:neuen\s+|neue\s+)?(?:timer|wecker|erinnerung|alarm|countdown|kurzzeitwecker|eieruhr)`,
		`(?:set|start|create|make|add)\s+(?:me\s+|us\s+)?(?:a\s+|an\s+|the\s+)?(?:new\s+)?(?:timer|reminder|alarm|countdown)`,
		`privat`, `leise`, `private`, `privately`, `quietly`, `silently`,
	)
	// reStripNouns removes the command nouns too, when the noun itself was
	// the trigger ("Timer 10 Minuten für die Nudeln", "Erinnerung: Wäsche").
	reStripNouns = stripAlt(
		`(?:einen|ein|eine|den|die|das|a|an|the)\s+(?:neuen\s+|neue\s+|new\s+)?(?:timer|wecker|erinnerung|alarm|countdown|reminder)`,
		`neue\s+erinnerung`, `new\s+reminder`,
		`timer`, `countdown`, `wecker`, `erinnerung`, `reminder`, `alarm`, `kurzzeitwecker`, `eieruhr`, `stoppuhr`,
		`setzen`, `stellen`, `starten`, `stellst`, `auf\s*$`,
	)
)

var (
	reTimerLead = regexp.MustCompile(`(?i)^(?:für|fuer|for|auf|über|ueber|of)\s+`)
	// "Erinnerung für den Zahnarzt", "a reminder for the dentist": the
	// preposition belongs to the command noun, not to the task.
	reNounLead = regexp.MustCompile(`(?i)^(?:für|fuer|for)\s+`)
	// A preposition left alone once the time was cut out ("set a reminder
	// for 5 pm", "stell einen Wecker für 7 Uhr") is no task.
	reLonePrep = regexp.MustCompile(`(?i)^(?:für|fuer|for|auf|um|at|on|am|bis|by|gegen|around|zu|to|in|ab|from)$`)
	// "(kannst du) mich an die Tabletten (erinnern)": the object pronoun left
	// at the start once the verb is gone.
	reObjLead = regexp.MustCompile(`(?i)^\s*(?:mich|uns|me|us)(?:\s+|$)`)
)

// reminderTextFrom turns what is left of the sentence after the time was
// cut out into the reminder text. nouns also strips "Timer", "Erinnerung"
// and friends: right when the noun was the command, wrong in "erinnere
// mich, den Timer zu stoppen".
func reminderTextFrom(rest, lang string, timer, nouns bool) string {
	s := " " + rest + " "
	for i := 0; i < 8; i++ {
		n := reStripVerbs.ReplaceAllString(s, " ")
		if nouns {
			n = reStripNouns.ReplaceAllString(n, " ")
		}
		if n == s {
			break
		}
		s = n
	}
	s = reObjLead.ReplaceAllString(strings.TrimSpace(s), "")
	s = tidyReminderText(s, lang)
	switch {
	case timer:
		s = strings.TrimSpace(reTimerLead.ReplaceAllString(s, ""))
		s = tidyReminderText(s, lang)
	case nouns:
		s = strings.TrimSpace(reNounLead.ReplaceAllString(s, ""))
		s = tidyReminderText(s, lang)
	}
	if reLonePrep.MatchString(s) {
		s = ""
	}
	return cleanText(s, reminderTextMax)
}

// ---------------------------------------------------------------------------
// set_reminder / set_timer

func reminderWhenQuestion(t *Turn, kind string) string {
	if kind == reminderKindTimer {
		return pick(t.Lang, "How long should the timer run?", "Wie lange soll der Timer laufen?")
	}
	return pick(t.Lang, "When should I remind you?", "Wann soll ich dich erinnern?")
}

// askReminderWhen is the slot question for a reminder without a time; the
// next utterance with a time completes it.
func askReminderWhen(t *Turn, tool string, a reminderArgs) Reply {
	a.WhenText, a.WhenISO, a.DurationText = "", "", ""
	args, _ := json.Marshal(a)
	return t.Ask(Pending{Kind: "slot", Slot: "when", Tool: tool, Args: args,
		Resume: func(t *Turn, p *Pending) (Reply, bool) {
			if _, ok := parseWhen(t.Query, turnNow(t), turnLoc(t)); !ok {
				if _, rok := parseRecurring(t.Query, turnNow(t), turnLoc(t)); !rok {
					return Reply{}, false
				}
			}
			return runToolChecked(t, p.Tool, mergeArg(p.Args, "when_text", t.Query)), true
		}}, reminderWhenQuestion(t, a.Kind))
}

// parseISOWhen reads the router's fallback ISO time (RFC 3339, or a local
// "2006-01-02T15:04" in loc).
func parseISOWhen(s string, loc *time.Location) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.In(loc), true
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func runSetTimer(t *Turn, raw json.RawMessage) Reply {
	var a reminderArgs
	_ = json.Unmarshal(raw, &a)
	a.Kind = reminderKindTimer
	if a.WhenText == "" {
		a.WhenText = a.DurationText
	}
	if a.Text == "" {
		a.Text = a.Label
	}
	b, _ := json.Marshal(a)
	return runSetReminder(t, b)
}

func runSetReminder(t *Turn, raw json.RawMessage) Reply {
	var a reminderArgs
	_ = json.Unmarshal(raw, &a)
	now, loc := turnNow(t), turnLoc(t)
	if a.Kind != reminderKindTimer {
		a.Kind = reminderKindReminder
	}
	text := strings.TrimSpace(a.Text)
	if a.Kind == reminderKindReminder {
		if rec, ok := recurringFrom(t, &a, now, loc); ok {
			return setRecurring(t, a, rec, now)
		}
	}
	w, ok := parseWhen(a.WhenText, now, loc)
	if !ok && !a.Fast && text != "" {
		// The router put the time into the text.
		if w2, ok2 := parseWhen(text, now, loc); ok2 {
			w, ok, text = w2, true, w2.Rest
		}
	}
	if !ok && !a.Fast && t.Query != "" {
		// Go reads the user's own words before trusting a model's ISO time.
		if w2, ok2 := parseWhen(stripCommand(t.Query), now, loc); ok2 {
			w, ok = w2, true
			if text == "" {
				text = reminderTextFrom(w2.Rest, t.Lang, a.Kind == reminderKindTimer, a.Kind == reminderKindTimer || !hasSetVerb(t, tokenize(stripCommand(t.Query))))
			}
		}
	}
	if !ok {
		if at, good := parseISOWhen(a.WhenISO, loc); good && at.After(now) && at.Sub(now) < whenMaxAhead {
			w, ok = WhenResult{At: at, HasClock: true, HasDay: true}, true
		}
	}
	a.Text = text
	if !ok {
		return askReminderWhen(t, "set_reminder", a)
	}
	if !a.Fast {
		if w2, ok2 := parseWhen(text, now, loc); ok2 {
			text = w2.Rest // a time left in the router's text
		}
		text = reminderTextFrom(text, t.Lang, a.Kind == reminderKindTimer, a.Kind == reminderKindTimer)
	}
	if text == "" && a.Wake && a.Kind == reminderKindReminder {
		text = pick(t.Lang, "wake up", "aufstehen")
	}
	r := Reminder{
		Kind: a.Kind, Text: text, Due: w.At, Person: reminderOwner(t), Lang: t.Lang,
		Source: "voice", ClientID: t.ClientID, Private: a.Private,
	}
	if r.Person != "" && personPrivateReminders(r.Person) {
		r.Private = true
	}
	if r.Kind == reminderKindTimer {
		d := w.Duration
		if d <= 0 {
			d = w.At.Sub(now)
		}
		r.DurationS = int(d.Round(time.Second) / time.Second)
	}
	saved, err := reminderAdd(r)
	if err != nil {
		return reminderErrorReply(t, err)
	}
	noteReminderSet(t.ClientID, saved.ID)
	id := saved.ID
	t.PushUndo(reminderLabel(saved, now, t.Lang), func(context.Context) error {
		_, _, err := settleReminder(id, reminderCancelled)
		return err
	})
	return Reply{Text: setReply(t, saved, now, a.Recurring), Card: reminderCard(saved, now, t.Lang)}
}

// recurringFrom reads a repeat rule from the time words: the fast path's
// sentence, else the router's when_text, a time the router left in the
// text, and finally the user's own words. It sets a.Text when the text is
// derived here.
func recurringFrom(t *Turn, a *reminderArgs, now time.Time, loc *time.Location) (RecurResult, bool) {
	if rec, ok := parseRecurring(a.WhenText, now, loc); ok {
		if !a.Fast {
			a.Text = strings.TrimSpace(a.Text)
			if r2, ok2 := parseRecurring(a.Text, now, loc); ok2 {
				a.Text = r2.Rest
			}
		}
		return rec, true
	}
	if a.Fast {
		return RecurResult{}, false
	}
	if rec, ok := parseRecurring(a.Text, now, loc); ok {
		a.Text = rec.Rest
		return rec, true
	}
	if q := stripCommand(t.Query); q != "" && strings.TrimSpace(a.WhenText) == "" {
		// The router dropped the time words: Go reads the user's sentence.
		if rec, ok := parseRecurring(q, now, loc); ok {
			if strings.TrimSpace(a.Text) == "" {
				a.Text = reminderTextFrom(rec.Rest, t.Lang, false, !hasSetVerb(t, tokenize(q)))
				a.Fast = true
			}
			return rec, true
		}
	}
	return RecurResult{}, false
}

// setRecurring stores a repeating reminder and confirms the rule and the
// first absolute time.
func setRecurring(t *Turn, a reminderArgs, rec RecurResult, now time.Time) Reply {
	text := a.Text
	if !a.Fast {
		text = reminderTextFrom(text, t.Lang, false, false)
	}
	if text == "" && a.Wake {
		text = pick(t.Lang, "wake up", "aufstehen")
	}
	rule := rec.Rule
	r := Reminder{
		Kind: reminderKindReminder, Text: text, Due: rec.First, Person: reminderOwner(t), Lang: t.Lang,
		Source: "voice", ClientID: t.ClientID, Private: a.Private, Rule: &rule,
	}
	if r.Person != "" && personPrivateReminders(r.Person) {
		r.Private = true
	}
	saved, err := reminderAdd(r)
	if err != nil {
		return reminderErrorReply(t, err)
	}
	noteReminderSet(t.ClientID, saved.ID)
	id := saved.ID
	t.PushUndo(reminderLabel(saved, now, t.Lang), func(context.Context) error {
		_, _, err := settleReminder(id, reminderCancelled)
		return err
	})
	return Reply{Text: setReply(t, saved, now, false), Card: reminderCard(saved, now, t.Lang)}
}

// setReply confirms with the absolute time, so a misheard or assumed time
// is heard at once.
func setReply(t *Turn, r Reminder, now time.Time, recurring bool) string {
	when := formatWhen(r.Due, now, t.Lang)
	text := speechText(r.Text)
	var s string
	switch {
	case r.Rule != nil:
		// "Wiederkehrende Erinnerung gesetzt, jeden Montag um 8 Uhr: den
		// Müll. Das erste Mal am Montag, 9. November, um 8 Uhr."
		rule := r.Rule.Describe(t.Lang)
		if text == "" || r.Private {
			s = pick(t.Lang, "Repeating reminder set, "+rule+".", "Wiederkehrende Erinnerung gesetzt, "+rule+".")
		} else {
			s = sentence(pick(t.Lang, "Repeating reminder set, "+rule+": "+text, "Wiederkehrende Erinnerung gesetzt, "+rule+": "+text))
		}
		s += pick(t.Lang, " The first one is "+when+".", " Das erste Mal "+when+".")
		recurring = false
	case r.Kind == reminderKindTimer:
		dur := formatDuration(time.Duration(r.DurationS)*time.Second, t.Lang)
		s = pick(t.Lang, "Timer set for "+dur+". I'll let you know "+when+".",
			"Timer läuft: "+dur+". Ich melde mich "+when+".")
	case text == "" || r.Private:
		s = pick(t.Lang, "Reminder set for "+bareWhen(when)+".", "Erinnerung gesetzt für "+bareWhen(when)+".")
	default:
		s = sentence(pick(t.Lang, "Reminder set for "+bareWhen(when)+": "+text, "Erinnerung gesetzt für "+bareWhen(when)+": "+text))
	}
	if recurring {
		s += pick(t.Lang, " I didn't understand how it should repeat, so this one rings once.",
			" Wie sie sich wiederholen soll, habe ich nicht verstanden, deshalb kommt sie einmal.")
	}
	if _, _, speakHere := hubSubscribers(); speakHere == 0 {
		s += pick(t.Lang, " Note: I can only announce it while HERMES is open in a browser tab.",
			" Achtung: Ansagen gibt es nur, solange HERMES in einem Browser-Tab offen ist.")
	}
	return s
}

// bareWhen drops the leading preposition of a formatWhen date ("on Monday,
// November 9, at 9:00 AM" / "am Montag, 9. November, um 9 Uhr") where the
// sentence already has one: "Reminder set for Monday, …", not "for on Monday".
func bareWhen(when string) string {
	for _, p := range []string{"on ", "am "} {
		if strings.HasPrefix(when, p) {
			return when[len(p):]
		}
	}
	return when
}

func reminderLabel(r Reminder, now time.Time, lang string) string {
	if r.Kind == reminderKindTimer {
		return pick(lang, "the timer for "+formatDuration(time.Duration(r.DurationS)*time.Second, lang),
			"Timer über "+formatDuration(time.Duration(r.DurationS)*time.Second, lang))
	}
	if t := safeTitle(r.Text); t != "" && !r.Private {
		return pick(lang, "reminder “"+t+"”", "Erinnerung „"+t+"“")
	}
	return pick(lang, "the reminder for ", "Erinnerung für ") + bareWhen(formatWhen(r.Due, now, lang))
}

// ReminderView is a reminder as the API and cards show it. A series carries
// its structured "rule" (from Reminder) plus the spoken "ruleText".
type ReminderView struct {
	Reminder
	PersonName string `json:"personName"`
	DueText    string `json:"dueText"`
	RuleText   string `json:"ruleText,omitempty"`
	Locked     bool   `json:"locked,omitempty"` // personal lock on: text withheld
}

func reminderView(r Reminder, now time.Time, lang string) ReminderView {
	name, _ := personName(r.Person)
	v := ReminderView{Reminder: r, PersonName: name, DueText: formatWhen(r.Due, now, lang)}
	if r.Rule != nil {
		v.RuleText = r.Rule.Describe(lang)
	}
	return v
}

func reminderCard(r Reminder, now time.Time, lang string) *Card {
	kind := "reminder"
	if r.Kind == reminderKindTimer {
		kind = "timer"
	}
	text := r.Text
	if r.Private {
		text = ""
	}
	data := map[string]any{
		"id": r.ID, "text": text, "due": r.Due.UTC().Format(time.RFC3339), "dueText": formatWhen(r.Due, now, lang),
		"kind": r.Kind, "private": r.Private,
	}
	if r.Rule != nil {
		data["rule"] = r.Rule.clone()
		data["ruleText"] = r.Rule.Describe(lang)
	}
	return &Card{Kind: kind, Data: data}
}

func reminderErrorReply(t *Turn, err error) Reply {
	switch {
	case errors.Is(err, errReminderLimit):
		return Reply{Error: true, Text: pick(t.Lang, "I already have too many reminders. Please delete a few.",
			"Ich habe schon zu viele Erinnerungen. Lösch bitte ein paar.")}
	case errors.Is(err, errStoreReadOnly):
		return Reply{Error: true, Text: pick(t.Lang,
			"I can't change reminders right now: the data file is from a newer HERMES.",
			"Ich kann Erinnerungen gerade nicht ändern: Die Datendatei stammt von einer neueren HERMES-Version.")}
	case errors.Is(err, errPersonGone):
		return Reply{Error: true, Text: pick(t.Lang, "That person no longer exists.", "Diese Person gibt es nicht mehr.")}
	case errors.Is(err, errReminderNotFound):
		return Reply{Error: true, Text: pick(t.Lang, "That reminder no longer exists.", "Diese Erinnerung gibt es nicht mehr.")}
	}
	log.Printf("reminders: %v", err)
	return Reply{Error: true, Text: pick(t.Lang, "I couldn't save that reminder.", "Das konnte ich nicht speichern.")}
}

// ---------------------------------------------------------------------------
// Acknowledge (fast path 8)

type reminderIDs struct {
	IDs []string `json:"ids"`
}

// ringingReminders are fired, unacknowledged reminders announced within the
// last 5 minutes.
func ringingReminders(now time.Time) []Reminder {
	return reminderList(func(r *Reminder) bool {
		if r.Status != reminderFired || r.FiredAt == nil {
			return false
		}
		last := reminderLastAnnounced(r)
		return !last.After(now.Add(time.Second)) && now.Sub(last) <= reminderAckWindow
	})
}

func matchReminderAck(t *Turn) (json.RawMessage, bool) {
	if !ackUtterances[commandCore(t.Tokens)] {
		return nil, false
	}
	ringing := ringingReminders(turnNow(t))
	if len(ringing) == 0 {
		return nil, false
	}
	var a reminderIDs
	for _, r := range ringing {
		a.IDs = append(a.IDs, r.ID)
	}
	b, _ := json.Marshal(a)
	return b, true
}

// runReminderAck marks the ringing reminders done. Acknowledging only stops
// an announcement that everyone in the room hears, so it is open to all.
func runReminderAck(t *Turn, raw json.RawMessage) Reply {
	var a reminderIDs
	_ = json.Unmarshal(raw, &a)
	for _, id := range a.IDs {
		if _, _, err := settleReminder(id, reminderDone); err != nil && !errors.Is(err, errReminderNotFound) {
			return reminderErrorReply(t, err)
		}
	}
	return Reply{Text: "Okay."}
}

// ---------------------------------------------------------------------------
// Fix-up (fast path 11): "nein, abends", "nein, um 19 Uhr", "no, at 7 pm"

type reminderFixArgs struct {
	ID         string `json:"id"`
	Correction string `json:"correction"`
}

func matchReminderFix(t *Turn) (json.RawMessage, bool) {
	q, toks := reminderQuery(t)
	if len(toks) < 2 || len(toks) > 7 {
		return nil, false
	}
	lead := fixLeads[toks[0]] || hasPhrase(toks[:min(3, len(toks))], "ich meinte") || hasPhrase(toks[:min(3, len(toks))], "i meant")
	if !lead {
		return nil, false
	}
	id, ok := lastReminderSet(t.ClientID)
	if !ok {
		return nil, false
	}
	if r, found := reminderGet(id); !found || r.Status != reminderPending || otherPersons(t, &r) {
		return nil, false
	}
	_, timeOK := parseWhen(q, turnNow(t), turnLoc(t))
	_, partOK := whenDayPart(q)
	if !timeOK && !partOK {
		return nil, false
	}
	b, _ := json.Marshal(reminderFixArgs{ID: id, Correction: q})
	return b, true
}

// fixedDue applies a correction to the original due time: a new clock time
// keeps the day, a day part moves the same hour ("nein, abends": 7 -> 19).
func fixedDue(orig time.Time, correction string, now time.Time, loc *time.Location) (time.Time, bool) {
	orig = orig.In(loc)
	at := func(day time.Time, h, m int) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, loc)
	}
	w, ok := parseWhen(correction, now, loc)
	part, partOK := whenDayPart(correction)
	switch {
	case ok && w.Duration > 0:
		return w.At, true
	case ok && w.HasDay && (w.HasClock || partOK):
		return w.At, true
	case ok && w.HasDay:
		return at(w.At, orig.Hour(), orig.Minute()), true // "nein, übermorgen": same time
	case ok && w.HasClock && !partOK:
		if c := at(orig, w.At.Hour(), w.At.Minute()); c.After(now) {
			return c, true
		}
		return w.At, true
	case partOK:
		h := orig.Hour()
		if ok && w.HasClock {
			h = w.At.Hour()
		}
		if nh, applied := applyPart(h%12, part); applied && h != 0 {
			if part == partMorning || part == partForenoon {
				nh = h % 12
			}
			h = nh
		}
		if part == partNoon && orig.Hour() == 9 && orig.Minute() == 0 {
			h = 12
		}
		c := at(orig, h, orig.Minute())
		if ok && w.HasClock {
			c = at(orig, h, w.At.Minute())
		}
		if c.Equal(orig) || (orig.Hour() == 9 && orig.Minute() == 0 && !(ok && w.HasClock)) {
			// The original time was only assumed (09:00): take the day part's hour.
			c = at(orig, partHour[part], 0)
		}
		if !c.After(now) {
			return time.Time{}, false
		}
		return c, true
	}
	return time.Time{}, false
}

func runReminderFix(t *Turn, raw json.RawMessage) Reply {
	var a reminderFixArgs
	_ = json.Unmarshal(raw, &a)
	r, ok := reminderGet(a.ID)
	if !ok || r.Status != reminderPending {
		return Reply{Text: pick(t.Lang, "That reminder no longer exists.", "Diese Erinnerung gibt es nicht mehr.")}
	}
	if !ownsReminder(t, r) {
		return notYourReminder(t)
	}
	now, loc := turnNow(t), turnLoc(t)
	due, ok := fixedDue(r.Due, a.Correction, now, loc)
	if !ok || due.Sub(now) >= whenMaxAhead {
		return Reply{Text: pick(t.Lang, "That time is already over. When should I remind you?",
			"Diese Zeit ist schon vorbei. Wann soll ich dich erinnern?")}
	}
	if r.Rule != nil {
		return fixRecurring(t, r, due, now, loc)
	}
	durS := 0
	if r.Kind == reminderKindTimer {
		durS = int(due.Sub(now).Round(time.Second) / time.Second)
	}
	before, after, err := rescheduleReminder(r.ID, due, durS)
	if err != nil {
		return reminderErrorReply(t, err)
	}
	t.PushUndo(reminderLabel(after, now, t.Lang), func(context.Context) error { return reminderRestore(before) })
	return Reply{
		Text: pick(t.Lang, "Okay, changed to "+bareWhen(formatWhen(due, now, t.Lang))+".", "Okay, geändert auf "+bareWhen(formatWhen(due, now, t.Lang))+"."),
		Card: reminderCard(after, now, t.Lang),
	}
}

// fixRecurring applies a correction to a series just set: the new clock time
// becomes the rule's time ("nein, abends": every Monday at 19:00), and the
// series starts again from its first occurrence after now.
func fixRecurring(t *Turn, r Reminder, due time.Time, now time.Time, loc *time.Location) Reply {
	rule := *r.Rule.clone()
	local := due.In(rule.location(loc))
	rule.Hour, rule.Minute, rule.Start = local.Hour(), local.Minute(), ""
	first, ok := rule.begin(now, time.Time{}, loc)
	if !ok {
		return reminderErrorReply(t, errReminderNotFound)
	}
	before, after, err := reminderModify(r.ID, func(x *Reminder) error {
		if x.Status != reminderPending || x.Rule == nil {
			return errReminderNotFound
		}
		c := rule
		x.Rule, x.Due = &c, first.UTC()
		return nil
	})
	if err != nil {
		return reminderErrorReply(t, err)
	}
	t.PushUndo(reminderLabel(after, now, t.Lang), func(context.Context) error { return reminderRestore(before) })
	return Reply{
		Text: pick(t.Lang, "Okay, changed to "+rule.Describe(t.Lang)+". The first one is "+formatWhen(first, now, t.Lang)+".",
			"Okay, geändert auf "+rule.Describe(t.Lang)+". Das erste Mal "+formatWhen(first, now, t.Lang)+"."),
		Card: reminderCard(after, now, t.Lang),
	}
}

// ---------------------------------------------------------------------------
// Snooze (fast path 12)

type reminderSnoozeArgs struct {
	ID      string `json:"id"`
	Seconds int    `json:"seconds"`
}

// ownsReminder: a household reminder belongs to everyone, a personal one
// only to its person (4.10: snooze and fix-up candidates are the speaker's
// own reminders plus household ones).
func ownsReminder(t *Turn, r Reminder) bool {
	return r.Person == "" || (t.Speaker.Known() && t.Speaker.ID == r.Person)
}

// otherPersons reports a reminder of someone other than a known speaker:
// never a candidate for that speaker. For unknown speakers it stays one, so
// the access check answers them (refusal, or "who is speaking?").
func otherPersons(t *Turn, r *Reminder) bool {
	return t.Speaker.Known() && !ownsReminder(t, *r)
}

func notYourReminder(t *Turn) Reply {
	return Reply{Text: pick(t.Lang, "That is someone else's reminder.", "Das ist die Erinnerung von jemand anderem.")}
}

// snoozeTarget is the most recently fired reminder on this device, else the
// latest one fired within 10 minutes (acknowledged or not); never another
// person's reminder for a known speaker.
func snoozeTarget(t *Turn, now time.Time) (Reminder, bool) {
	clientID := t.ClientID
	cands := reminderList(func(r *Reminder) bool {
		return (r.Status == reminderFired || r.Status == reminderDone) && r.FiredAt != nil && !otherPersons(t, r) &&
			now.Sub(*r.FiredAt) <= reminderSnoozeWindow+time.Duration(r.Repeats)*reminderRepeatEvery && !r.FiredAt.After(now)
	})
	var best *Reminder
	for i := range cands {
		c := &cands[i]
		switch {
		case best == nil:
			best = c
		case (c.ClientID == clientID) != (best.ClientID == clientID):
			if c.ClientID == clientID {
				best = c
			}
		case c.FiredAt.After(*best.FiredAt):
			best = c
		}
	}
	if best == nil {
		return Reminder{}, false
	}
	return *best, true
}

func matchReminderSnooze(t *Turn) (json.RawMessage, bool) {
	q, toks := reminderQuery(t)
	if len(toks) == 0 || len(toks) > 14 {
		return nil, false
	}
	now := turnNow(t)
	w, ok := parseWhen(q, now, turnLoc(t))
	again := hasAnyPhrase(toks, []string{"nochmal", "noch mal", "noch einmal", "again", "erneut"})
	later := hasAnyPhrase(toks, []string{"später", "later"})
	switch {
	case hasAnyWord(toks, snoozeWords):
	case again && (hasAnyCue(toks, setCues) || ok || later): // "später nochmal", "erinner mich in 5 Minuten nochmal"
	case later && len(toks) <= 3: // "später", "bitte später"
	case toks[0] == "noch" && ok && w.Duration > 0 && len(toks) <= 4: // "noch 5 Minuten"
	default:
		return nil, false
	}
	target, found := snoozeTarget(t, now)
	if !found {
		return nil, false
	}
	d := reminderSnoozeDefault
	if ok && w.At.After(now) {
		d = w.At.Sub(now)
	}
	b, _ := json.Marshal(reminderSnoozeArgs{ID: target.ID, Seconds: int(d.Round(time.Second) / time.Second)})
	return b, true
}

// personalTargetAccess: touching someone's personal reminder is a write;
// household ones are open (4.10).
func personalTargetAccess(t *Turn, raw json.RawMessage) Access {
	var a struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &a)
	if r, ok := reminderGet(a.ID); ok && r.Person != "" {
		return AccessWrite
	}
	return AccessOpen
}

func runReminderSnooze(t *Turn, raw json.RawMessage) Reply {
	var a reminderSnoozeArgs
	_ = json.Unmarshal(raw, &a)
	now := turnNow(t)
	if a.Seconds <= 0 {
		a.Seconds = int(reminderSnoozeDefault / time.Second)
	}
	if time.Duration(a.Seconds)*time.Second >= whenMaxAhead {
		return Reply{Text: pick(t.Lang, "That is too far ahead.", "Das ist zu weit in der Zukunft.")}
	}
	r, ok := reminderGet(a.ID)
	if !ok || r.Status == reminderCancelled {
		return Reply{Text: pick(t.Lang, "That reminder no longer exists.", "Diese Erinnerung gibt es nicht mehr.")}
	}
	if !ownsReminder(t, r) {
		return notYourReminder(t) // e.g. an uncertain speaker who then named someone else
	}
	due := now.Add(time.Duration(a.Seconds) * time.Second)
	before, after, err := rescheduleReminder(r.ID, due, 0)
	if err != nil {
		return reminderErrorReply(t, err)
	}
	t.PushUndo(reminderLabel(after, now, t.Lang), func(context.Context) error {
		return reminderRestore(undoState(before))
	})
	return Reply{
		Text: pick(t.Lang, "Okay, I'll remind you again "+formatWhen(due, now, t.Lang)+".",
			"Okay, ich erinnere dich "+formatWhen(due, now, t.Lang)+" noch einmal."),
		Card: reminderCard(after, now, t.Lang),
	}
}

// ---------------------------------------------------------------------------
// List, remaining time, cancel (fast path 41 and router tools)

type reminderListArgs struct {
	Timers bool `json:"timers,omitempty"`
}

func matchReminderList(t *Turn) (json.RawMessage, bool) {
	_, toks := reminderQuery(t)
	if !isListRequest(toks) || isCancelRequest(toks) || isRemainingRequest(toks) {
		return nil, false
	}
	a := reminderListArgs{Timers: (hasAnyWord(toks, timerNouns) || hasPhrase(toks, "timers")) && !hasAnyWord(toks, wordSet("erinnerungen", "reminders"))}
	b, _ := json.Marshal(a)
	return b, true
}

// reminderLine is one spoken list entry ("heute um 16 Uhr: Zahnarzt
// anrufen"); private reminders keep their text to themselves.
func reminderLine(r Reminder, now time.Time, lang string) string {
	when := formatWhen(r.Due, now, lang)
	if r.Rule != nil {
		// "jeden Montag um 8 Uhr, als Nächstes am Montag, 9. November: den Müll"
		when = r.Rule.Describe(lang) + pick(lang, ", next ", ", als Nächstes ") + formatDay(r.Due, now, lang)
	}
	switch {
	case r.Kind == reminderKindTimer:
		dur := formatDuration(time.Duration(r.DurationS)*time.Second, lang)
		return pick(lang, "a timer for "+dur+", "+when, "ein Timer über "+dur+", "+when)
	case r.Kind == reminderKindMeeting:
		return when + ": " + safeTitle(r.Text)
	case r.Private:
		return when + pick(lang, ": a private reminder", ": eine private Erinnerung")
	case speechText(r.Text) == "":
		return when + pick(lang, ": a reminder", ": eine Erinnerung")
	}
	return when + ": " + speechText(r.Text)
}

func runReminderList(t *Turn, raw json.RawMessage) Reply {
	var a reminderListArgs
	_ = json.Unmarshal(raw, &a)
	if !t.Can(AccessRead) && t.Speaker.Source == SourceUncertain {
		return refuse(t, "reminder_list", raw)
	}
	now := turnNow(t)
	rs := visibleReminders(t, AccessRead, func(r *Reminder) bool {
		return r.Status == reminderPending && (!a.Timers || r.Kind == reminderKindTimer)
	})
	householdOnly := !t.Can(AccessRead) && peopleCount() > 0
	if len(rs) == 0 {
		switch {
		case a.Timers:
			return Reply{Text: pick(t.Lang, "No timer is running.", "Gerade läuft kein Timer.")}
		case householdOnly:
			return Reply{Text: pick(t.Lang, "There are no upcoming household reminders.", "Für den Haushalt stehen keine Erinnerungen an.")}
		}
		return Reply{Text: pick(t.Lang, "You have no upcoming reminders.", "Du hast keine anstehenden Erinnerungen.")}
	}
	var lines []string
	views := make([]ReminderView, 0, len(rs))
	for i, r := range rs {
		v := reminderView(r, now, t.Lang)
		if r.Private {
			v.Text = "" // like reminderCard: the card never carries private text
		}
		views = append(views, v)
		if i < reminderListMax {
			lines = append(lines, reminderLine(r, now, t.Lang))
		}
	}
	n := len(rs)
	var head string
	switch {
	case a.Timers && n == 1:
		head = pick(t.Lang, "One timer is running: ", "Es läuft ein Timer: ")
	case a.Timers:
		head = fmt.Sprintf(pick(t.Lang, "%d timers are running: ", "Es laufen %d Timer: "), n)
	case householdOnly && n == 1:
		head = pick(t.Lang, "For the household there is one reminder: ", "Für den Haushalt gibt es eine Erinnerung: ")
	case householdOnly:
		head = fmt.Sprintf(pick(t.Lang, "For the household there are %d reminders: ", "Für den Haushalt gibt es %d Erinnerungen: "), n)
	case n == 1:
		head = pick(t.Lang, "You have one reminder: ", "Du hast eine Erinnerung: ")
	default:
		head = fmt.Sprintf(pick(t.Lang, "You have %d reminders: ", "Du hast %d Erinnerungen: "), n)
	}
	text := head + strings.Join(lines, "; ") + "."
	if n > reminderListMax {
		text += fmt.Sprintf(pick(t.Lang, " And %d more.", " Und %d weitere."), n-reminderListMax)
	}
	return Reply{Text: text, Card: &Card{Kind: "reminder", Data: map[string]any{"reminders": views}}}
}

func matchReminderRemaining(t *Turn) (json.RawMessage, bool) {
	_, toks := reminderQuery(t)
	if !isRemainingRequest(toks) || isCancelRequest(toks) {
		return nil, false
	}
	reminders := hasAnyWord(toks, reminderNouns) || hasAnyWord(toks, wordSet("erinnerungen", "reminders"))
	pending := reminderList(func(r *Reminder) bool {
		return r.Status == reminderPending && (reminders || r.Kind == reminderKindTimer)
	})
	if len(pending) == 0 && !hasAnyWord(toks, timerNouns) && !reminders {
		return nil, false // "wie lange noch bis Weihnachten?" is not ours
	}
	b, _ := json.Marshal(reminderListArgs{Timers: !reminders})
	return b, true
}

// formatRemaining speaks a countdown: seconds below a minute, whole minutes
// from 2 minutes on.
func formatRemaining(d time.Duration, lang string) string {
	switch {
	case d < time.Minute:
		return formatDuration(d.Round(time.Second), lang)
	case d < 2*time.Minute:
		return formatDuration(d.Round(10*time.Second), lang)
	}
	return formatDuration(d.Round(time.Minute), lang)
}

func runReminderRemaining(t *Turn, raw json.RawMessage) Reply {
	var a reminderListArgs
	_ = json.Unmarshal(raw, &a)
	now := turnNow(t)
	rs := visibleReminders(t, AccessRead, func(r *Reminder) bool {
		return r.Status == reminderPending && (!a.Timers || r.Kind == reminderKindTimer)
	})
	if len(rs) == 0 {
		if a.Timers {
			return Reply{Text: pick(t.Lang, "No timer is running.", "Gerade läuft kein Timer.")}
		}
		return Reply{Text: pick(t.Lang, "You have no upcoming reminders.", "Du hast keine anstehenden Erinnerungen.")}
	}
	r := rs[0]
	left := formatRemaining(r.Due.Sub(now), t.Lang)
	clock := formatWhen(r.Due, now, t.Lang)
	var s string
	if r.Kind == reminderKindTimer {
		s = pick(t.Lang, left+" left. The timer rings "+clock+".", "Noch "+left+". Der Timer klingelt "+clock+".")
	} else {
		s = pick(t.Lang, left+" left until your next reminder, "+clock+".", "Noch "+left+" bis zur nächsten Erinnerung, "+clock+".")
	}
	if more := len(rs) - 1; more == 1 {
		s += pick(t.Lang, " One more after that.", " Danach kommt noch eine.")
	} else if more > 1 {
		s += fmt.Sprintf(pick(t.Lang, " %d more after that.", " Danach kommen noch %d."), more)
	}
	return Reply{Text: s}
}

type reminderCancelArgs struct {
	Query string   `json:"query,omitempty"`
	Timer bool     `json:"timer,omitempty"`
	All   bool     `json:"all,omitempty"`
	ID    string   `json:"id,omitempty"`    // a series named in the "which scope?" question
	IDs   []string `json:"ids,omitempty"`   // "alle"/"both" after the "which one?" question
	Scope string   `json:"scope,omitempty"` // series only: "occurrence" | "series" ("" = ask)
}

const (
	scopeOccurrence = "occurrence"
	scopeSeries     = "series"
)

// cancelScope reads whether a cancel request means one occurrence ("nur die
// nächste", "skip") or the whole series ("die ganze Serie", "for good").
func cancelScope(toks []string) string {
	switch {
	case hasAnyCue(toks, occurrenceCues):
		return scopeOccurrence
	case hasAnyCue(toks, seriesCues):
		return scopeSeries
	}
	return ""
}

// reCancelWords strips the command from a cancel request, leaving what to
// match ("lösch die Erinnerung an die Pizza" -> "Pizza").
var reCancelWords = regexp.MustCompile(`(?i)(?:^|[\s,;:!?.]+)(?:` + strings.Join([]string{
	`lösch(?:e|en)?`, `entfern(?:e|en)?`, `streich(?:e|en)?`, `cancel`, `delete`, `remove`, `stopp(?:e|en)?`, `stop`,
	`beende(?:n)?`, `abbrechen`, `brich`, `ab`, `aus`, `mach(?:e)?`, `schalt(?:e)?`, `turn`, `switch`, `off`, `clear`,
	`vergiss`, `vergesse`, `vergessen`, `forget`, `dem`, `mit`,
	`überspring(?:e|en)?`, `skip`, `auslassen`, `lass`, `ausfallen`, `nur`, `only`, `just`, `nächsten?`, `next`,
	`diese[nsm]?`, `diesmal`, `this`, `mal`, `time`, `ganzen?`, `serien?`, `series`, `whole`, `komplett`, `completely`,
	`dauerhaft`, `immer`, `forever`, `good`, `wiederkehrenden?`, `repeating`, `recurring`, `one`, `einmal`,
	`brauche`, `nicht`, `mehr`, `don\s*'?\s*t`, `need`, `bitte`, `please`, `hermes`, `alle`, `all`, `meine`, `my`,
	`die`, `den`, `das`, `der`, `the`, `an`, `für`, `for`, `about`, `von`, `zum`, `zur`, `of`,
	`erinnerung(?:en)?`, `timer`, `timers`, `wecker`, `alarm(?:s)?`, `countdown(?:s)?`, `reminder(?:s)?`,
}, "|") + `)(?:[\s,;:!?.]+|$)`)

func matchReminderCancel(t *Turn) (json.RawMessage, bool) {
	q, toks := reminderQuery(t)
	if !isCancelRequest(toks) || len(toks) > 20 || hasSetVerb(t, toks) {
		return nil, false // "erinnere mich, den Timer zu stoppen" sets a reminder
	}
	scope := cancelScope(toks)
	b, _ := json.Marshal(reminderCancelArgs{
		Query: cancelQuery(q),
		Timer: hasAnyWord(toks, timerNouns) || hasPhrase(toks, "timers"),
		All:   (hasPhrase(toks, "alle") || hasPhrase(toks, "all")) && scope != scopeOccurrence && hasAnyWord(toks, pluralCancelNouns),
		Scope: scope,
	})
	return b, true
}

func cancelQuery(q string) string {
	s := " " + q + " "
	for i := 0; i < 10; i++ {
		n := reCancelWords.ReplaceAllString(s, " ")
		if n == s {
			break
		}
		s = n
	}
	return strings.TrimSpace(tidyReminderText(s, ""))
}

// pluralCancelNouns make "alle"/"all" mean every reminder ("lösch alle
// Erinnerungen"), not "the whole series" ("die Müll-Erinnerung, alle").
var pluralCancelNouns = wordSet("erinnerungen", "reminders", "timers", "timer", "wecker", "alarms", "countdowns")

// cancelExclude are folded words that never identify a reminder.
var cancelExclude = wordSet("erinnerung", "erinnerungen", "timer", "reminder", "reminders", "wecker", "alarm", "serie", "serien", "series")

func runReminderCancel(t *Turn, raw json.RawMessage) Reply {
	var a reminderCancelArgs
	_ = json.Unmarshal(raw, &a)
	now := turnNow(t)
	write := t.Can(AccessWrite)
	active := func(r *Reminder) bool {
		if r.Status != reminderPending && r.Status != reminderFired {
			return false
		}
		if a.Timer && r.Kind != reminderKindTimer {
			return false
		}
		// Without write access only household timers (4.10).
		return write || (r.Person == "" && r.Kind == reminderKindTimer)
	}
	cands := visibleReminders(t, AccessWrite, active)
	query := strings.TrimSpace(a.Query)

	var targets []Reminder
	switch {
	case a.ID != "":
		for _, r := range cands {
			if r.ID == a.ID {
				targets = []Reminder{r}
			}
		}
	case len(a.IDs) > 0:
		want := map[string]bool{}
		for _, id := range a.IDs {
			want[id] = true
		}
		for _, r := range cands {
			if want[r.ID] {
				targets = append(targets, r)
			}
		}
		a.All = true // the user chose "all of them": no series question
	case a.All:
		targets = cands
	case query != "" && len(lexTokens(query, cancelExclude)) > 0:
		best, second := -1.0, -1.0
		var pick1 Reminder
		for _, r := range cands {
			sc := lexScoreExcl(query, r.Text, cancelExclude)
			if sc > best {
				second, best, pick1 = best, sc, r
			} else if sc > second {
				second = sc
			}
		}
		if best > 0 && best-second >= 0.05 {
			targets = []Reminder{pick1}
		} else if best > 0 {
			return cancelWhich(t, cands, a, now)
		}
	default:
		if a.Scope == scopeSeries {
			// "lösch die ganze Serie" with no name: only series qualify.
			var series []Reminder
			for _, r := range cands {
				if r.Rule != nil {
					series = append(series, r)
				}
			}
			cands = series
		}
		var ringing []Reminder
		for _, r := range cands {
			if r.Status == reminderFired {
				ringing = append(ringing, r)
			}
		}
		switch {
		case len(ringing) > 0:
			targets = ringing
		case len(cands) == 1:
			targets = cands
		case len(cands) > 1:
			return cancelWhich(t, cands, a, now)
		}
	}
	if len(targets) == 0 {
		if !write && t.Speaker.Source != SourceSingle {
			// Personal reminders exist only for known speakers: ask or refuse.
			if len(reminderList(func(r *Reminder) bool { return r.Person != "" && r.Status == reminderPending })) > 0 {
				return refuse(t, "reminder_cancel", raw)
			}
		}
		if a.Timer {
			return Reply{Text: pick(t.Lang, "No timer is running.", "Gerade läuft kein Timer.")}
		}
		return Reply{Text: pick(t.Lang, "I couldn't find a matching reminder.", "Ich habe keine passende Erinnerung gefunden.")}
	}
	if len(targets) == 1 && targets[0].Rule != nil && !a.All {
		return cancelSeriesOrOccurrence(t, targets[0], a, now)
	}
	var befores []Reminder
	for _, r := range targets {
		status := reminderCancelled
		if r.Status == reminderFired {
			status = reminderDone // stopping a ringing one is an acknowledgement
		}
		before, _, err := settleReminder(r.ID, status)
		if err != nil && !errors.Is(err, errReminderNotFound) {
			return reminderErrorReply(t, err)
		}
		if err == nil {
			before.EventID = 0
			befores = append(befores, before)
		}
	}
	if len(befores) == 0 {
		return Reply{Text: pick(t.Lang, "I couldn't find a matching reminder.", "Ich habe keine passende Erinnerung gefunden.")}
	}
	label := reminderLabel(befores[0], now, t.Lang)
	if len(befores) > 1 {
		label = fmt.Sprintf(pick(t.Lang, "%d reminders", "%d Erinnerungen"), len(befores))
	}
	t.PushUndo(label, func(context.Context) error {
		for _, b := range befores {
			if err := reminderRestore(undoState(b)); err != nil {
				return err
			}
		}
		return nil
	})
	first := befores[0]
	switch {
	case len(befores) > 1:
		return Reply{Text: fmt.Sprintf(pick(t.Lang, "Okay, I cancelled %d reminders.", "Okay, ich habe %d Erinnerungen gelöscht."), len(befores))}
	case first.Kind == reminderKindTimer:
		return Reply{Text: pick(t.Lang, "Okay, timer stopped.", "Okay, Timer gestoppt.")}
	case first.Private || speechText(first.Text) == "":
		return Reply{Text: pick(t.Lang, "Okay, I cancelled the reminder for ", "Okay, ich habe die Erinnerung für ") +
			bareWhen(formatWhen(first.Due, now, t.Lang)) + pick(t.Lang, ".", " gelöscht.")}
	}
	return Reply{Text: sentence(pick(t.Lang, "Okay, cancelled: ", "Okay, gelöscht: ") + speechText(first.Text))}
}

// undoState is what an undo puts back for a reminder that was ringing: it
// never rings again; a series continues with its next occurrence.
func undoState(b Reminder) Reminder {
	b.EventID = 0 // the old announcement was acked
	if b.Status == reminderFired {
		if b.Rule != nil {
			advanceOccurrence(&b, nowFunc(), householdLoc())
		} else {
			b.Status = reminderDone
		}
	}
	return b
}

// cancelSeriesOrOccurrence handles a cancel request that hit one series:
// skip the current occurrence, stop the series, or ask which.
func cancelSeriesOrOccurrence(t *Turn, r Reminder, a reminderCancelArgs, now time.Time) Reply {
	rule := r.Rule.Describe(t.Lang)
	switch a.Scope {
	case scopeOccurrence:
		before, after, err := skipOccurrence(r.ID)
		if err != nil {
			return reminderErrorReply(t, err)
		}
		t.PushUndo(reminderLabel(before, now, t.Lang), func(context.Context) error { return reminderRestore(undoState(before)) })
		skipped := pick(t.Lang, "Okay, I'll skip the one ", "Okay, ich lasse die Erinnerung ") + formatDay(before.Due, now, t.Lang)
		if after.Status != reminderPending {
			return Reply{Text: skipped + pick(t.Lang, ". That was the last one.", " aus. Das war die letzte.")}
		}
		return Reply{
			Text: skipped + pick(t.Lang, ". The next one is "+formatWhen(after.Due, now, t.Lang)+".",
				" aus. Das nächste Mal "+formatWhen(after.Due, now, t.Lang)+"."),
			Card: reminderCard(after, now, t.Lang),
		}
	case scopeSeries:
		before, _, err := settleReminder(r.ID, reminderCancelled)
		if err != nil {
			return reminderErrorReply(t, err)
		}
		t.PushUndo(reminderLabel(before, now, t.Lang), func(context.Context) error { return reminderRestore(undoState(before)) })
		if text := speechText(before.Text); text != "" && !before.Private {
			return Reply{Text: sentence(pick(t.Lang, "Okay, cancelled: "+text+", "+rule, "Okay, gelöscht: "+text+", "+rule))}
		}
		return Reply{Text: sentence(pick(t.Lang, "Okay, I cancelled the series: "+rule, "Okay, ich habe die Serie gelöscht: "+rule))}
	}
	a.ID, a.Scope = r.ID, ""
	args, _ := json.Marshal(a)
	q := pick(t.Lang,
		"Just the next one, "+formatDay(r.Due, now, t.Lang)+", or the whole series ("+rule+")?",
		"Nur die nächste, "+formatDay(r.Due, now, t.Lang)+", oder die ganze Serie ("+rule+")?")
	return t.Ask(Pending{Kind: "slot", Slot: "scope", Tool: "reminder_cancel", Args: args,
		Resume: func(t *Turn, p *Pending) (Reply, bool) {
			_, toks := reminderQuery(t)
			scope := scopeAnswer(toks)
			if scope == "" {
				if yesNo(t.Tokens) == -1 || hasAnyCue(toks, []string{"keine", "keins", "nichts", "neither", "none", "nothing", "abbrechen"}) {
					return Reply{Text: pick(t.Lang, "Okay, I'll leave it.", "Okay, ich lasse sie.")}, true
				}
				return Reply{}, false
			}
			return runToolChecked(t, p.Tool, mergeArg(p.Args, "scope", scope)), true
		}}, q)
}

// scopeAnswer reads the answer to "nur die nächste oder die ganze Serie?".
func scopeAnswer(toks []string) string {
	if len(toks) == 0 || len(toks) > 8 {
		return ""
	}
	if s := cancelScope(toks); s != "" {
		return s
	}
	switch {
	case hasAnyCue(toks, []string{"nächste", "nächsten", "erste", "einmal", "eine", "next", "first", "once", "one", "this"}):
		return scopeOccurrence
	case hasAnyCue(toks, []string{"ganze", "ganzen", "beides", "everything", "both", "it all"}):
		return scopeSeries
	}
	return ""
}

// cancelWhich asks which of several matching reminders to cancel and keeps
// the question pending: "den ersten", "the second", "alle"/"both" answer it.
// Two identical timers have no text to tell them apart, so a hint like
// "cancel the reminder about …" could never single one out.
func cancelWhich(t *Turn, cands []Reminder, a reminderCancelArgs, now time.Time) Reply {
	if len(cands) > cancelWhichMax {
		cands = cands[:cancelWhichMax]
	}
	var names []string
	ids := make([]string, len(cands))
	for i, r := range cands {
		ids[i] = r.ID
		names = append(names, fmt.Sprintf(pick(t.Lang, "number %d: ", "Nummer %d: "), i+1)+reminderLine(r, now, t.Lang))
	}
	a.Query, a.All = "", false
	q := pick(t.Lang,
		"Which one do you mean? "+strings.Join(names, "; ")+". Say the number, “the first” or “all”.",
		"Welche meinst du? "+strings.Join(names, "; ")+". Sag die Nummer, „die erste“ oder „alle“.")
	return t.Ask(Pending{Kind: "slot", Slot: "which", Tool: "reminder_cancel",
		Resume: func(t *Turn, p *Pending) (Reply, bool) {
			_, toks := reminderQuery(t)
			if len(toks) == 0 || len(toks) > 8 {
				return Reply{}, false
			}
			if yesNo(t.Tokens) == -1 || hasAnyCue(toks, []string{"keine", "keinen", "keins", "nichts", "neither", "none", "nothing", "abbrechen"}) {
				return Reply{Text: pick(t.Lang, "Okay, I'll leave them.", "Okay, ich lasse sie.")}, true
			}
			b := a
			switch i := ordinalAnswer(toks, len(ids)); {
			case i >= 0:
				b.ID = ids[i]
			case hasAnyCue(toks, []string{"alle", "beide", "beiden", "all", "both", "all of them", "everything", "alles"}):
				b.IDs = ids
			default:
				return Reply{}, false
			}
			args, _ := json.Marshal(b)
			return runToolChecked(t, p.Tool, args), true
		}}, q)
}

const cancelWhichMax = 3

var (
	// ordinalWords map an answer word to a list index (-1: the last one).
	ordinalWords = map[string]int{
		"erste": 0, "ersten": 0, "erster": 0, "erstes": 0, "erstens": 0, "first": 0, "1": 0, "1st": 0, "eins": 0,
		"zweite": 1, "zweiten": 1, "zweiter": 1, "zweites": 1, "zweitens": 1, "second": 1, "2": 1, "2nd": 1, "zwei": 1, "two": 1,
		"dritte": 2, "dritten": 2, "dritter": 2, "drittes": 2, "drittens": 2, "third": 2, "3": 2, "3rd": 2, "drei": 2, "three": 2,
		"letzte": -1, "letzten": -1, "letzter": -1, "letztes": -1, "last": -1,
	}
)

// ordinalAnswer reads "den ersten" / "the second one" / "die letzte" as an
// index below n, or -1.
func ordinalAnswer(toks []string, n int) int {
	for _, w := range toks {
		i, ok := ordinalWords[w]
		if !ok {
			continue
		}
		if i < 0 {
			i = n - 1
		}
		if i < n {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Personal context (order 30) and status

// reminderContext lists today's pending reminders of the speaker and the
// household for the local persona model (data block, 4.7).
func reminderContext(t *Turn) string {
	if !t.Can(AccessRead) {
		return ""
	}
	now := turnNow(t)
	end := midnightOf(now).AddDate(0, 0, 1)
	rs := visibleReminders(t, AccessRead, func(r *Reminder) bool {
		return r.Status == reminderPending && r.Due.Before(end)
	})
	var items []string
	for i, r := range rs {
		if i == reminderListMax {
			break
		}
		clock := r.Due.In(now.Location()).Format("15:04")
		text := safeTitle(r.Text)
		switch {
		case r.Private:
			// Never in a prompt: the persona might read it out to the room.
			text = pick(t.Lang, "private reminder", "private Erinnerung")
		case r.Kind == reminderKindTimer:
			text = pick(t.Lang, "Timer (", "Timer (") + formatDuration(time.Duration(r.DurationS)*time.Second, t.Lang) + ")"
			if s := safeTitle(r.Text); s != "" {
				text += ": " + s
			}
		case text == "":
			text = pick(t.Lang, "reminder", "Erinnerung")
		}
		if r.Rule != nil {
			text += " (" + r.Rule.Describe(t.Lang) + ")"
		}
		items = append(items, clock+" "+text)
	}
	return contextBlock(t.Lang, "Upcoming reminders today", "Anstehende Erinnerungen heute", items)
}

func reminderStatus(context.Context) any {
	rs := reminderList(func(r *Reminder) bool { return r.Status == reminderPending })
	next := ""
	if len(rs) > 0 {
		next = rs[0].Due.UTC().Format(time.RFC3339)
	}
	return map[string]any{"pending": len(rs), "next": next}
}

// ---------------------------------------------------------------------------
// HTTP API (SPEC 3.5)

func writeReminderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errStoreReadOnly):
		writeJSONErrorCode(w, http.StatusServiceUnavailable, "read_only", err.Error())
	case errors.Is(err, errReminderNotFound):
		writeJSONErrorCode(w, http.StatusNotFound, "not_found", "unknown reminder")
	case errors.Is(err, errReminderLimit):
		writeJSONErrorCode(w, http.StatusConflict, "limit", "too many pending reminders")
	case errors.Is(err, errPersonGone):
		writeJSONErrorCode(w, http.StatusBadRequest, "person", "unknown person")
	case errors.Is(err, errReminderWhen):
		writeJSONErrorCode(w, http.StatusBadRequest, "when", err.Error())
	case errors.Is(err, errRecurRule):
		writeJSONErrorCode(w, http.StatusBadRequest, "rule", err.Error())
	case errors.Is(err, errReminderNoRule):
		writeJSONErrorCode(w, http.StatusConflict, "not_recurring", err.Error())
	default:
		log.Printf("reminders api: %v", err)
		writeJSONErrorCode(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

func decodeReminderBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, reminderBodyLimit)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
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

// apiClock is the household clock and language for API answers.
func apiClock() (now time.Time, loc *time.Location, lang string) {
	s := loadSettings()
	loc = settingsLocation(s)
	return nowFunc().In(loc), loc, voiceLang(s.PiperVoice)
}

// apiDue reads "due" (RFC 3339) or "when" (spoken form) and checks the range.
func apiDue(due, when string, now time.Time, loc *time.Location) (time.Time, time.Duration, error) {
	var at time.Time
	var dur time.Duration
	switch {
	case strings.TrimSpace(due) != "":
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(due))
		if err != nil {
			return time.Time{}, 0, errReminderWhen
		}
		at = t
	case strings.TrimSpace(when) != "":
		w, ok := parseWhen(truncateRunes(when, 300), now, loc)
		if !ok {
			return time.Time{}, 0, errReminderWhen
		}
		at, dur = w.At, w.Duration
	default:
		return time.Time{}, 0, errReminderWhen
	}
	if !at.After(now) || at.Sub(now) >= whenMaxAhead {
		return time.Time{}, 0, errReminderWhen
	}
	return at, dur, nil
}

// apiView is a reminder as the API returns it. Meeting check-ins carry
// calendar titles; with the personal lock on they are calendar data like
// GET /api/calendar/events and need the unlock cookie (4.10), so a locked
// request sees only the kind ("Termin").
//
// A person's reminders are personal data too: while locked the list shows
// them without text (Locked:true, the UI shows "Private reminder"), and a
// single GET or a change to one needs the unlock cookie (lockedReminder).
func apiView(r *http.Request, x Reminder, now time.Time, lang string) ReminderView {
	locked := false
	if (x.Kind == reminderKindMeeting || x.Person != "") && loadSettings().PersonalLock && !unlockedRequest(r) {
		x.Text = ""
		locked = x.Person != ""
	}
	v := reminderView(x, now, lang)
	if locked {
		v.Locked = true
		if x.Private {
			v.RuleText = ""
		}
	}
	return v
}

// lockedReminder writes 403 code "locked" and reports true when the personal
// lock is on, the request is not unlocked and the reminder belongs to a
// person. Household reminders stay open (4.10). allowRinging lets a fired
// (ringing) reminder be acknowledged or snoozed at the device it rings on.
func lockedReminder(w http.ResponseWriter, r *http.Request, id string, allowRinging bool) bool {
	x, ok := reminderGet(id)
	if !ok || x.Person == "" || (allowRinging && x.Status == reminderFired) {
		return false
	}
	if !loadSettings().PersonalLock || unlockedRequest(r) {
		return false
	}
	writeJSONErrorCode(w, http.StatusForbidden, "locked", "personal data is locked; unlock with the PIN")
	return true
}

func handleRemindersList(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	if status != "pending" && status != "all" {
		writeJSONErrorCode(w, http.StatusBadRequest, "status", "status must be pending or all")
		return
	}
	now, _, lang := apiClock()
	rs := reminderList(func(x *Reminder) bool { return status == "all" || x.Status == reminderPending })
	views := make([]ReminderView, 0, len(rs))
	for _, x := range rs {
		views = append(views, apiView(r, x, now, lang))
	}
	writeJSON(w, http.StatusOK, map[string]any{"reminders": views})
}

func handleReminderGet(w http.ResponseWriter, r *http.Request) {
	x, ok := reminderGet(r.PathValue("id"))
	if !ok {
		writeReminderError(w, errReminderNotFound)
		return
	}
	if lockedReminder(w, r, x.ID, false) {
		return
	}
	now, _, lang := apiClock()
	writeJSON(w, http.StatusOK, apiView(r, x, now, lang))
}

type reminderBody struct {
	Text   *string         `json:"text"`
	Due    string          `json:"due"`
	When   string          `json:"when"`
	Person string          `json:"person"`
	Kind   string          `json:"kind"`
	Rule   json.RawMessage `json:"rule"`   // RecurRule fields; null removes the rule (PATCH)
	Repeat string          `json:"repeat"` // shorthand: none|daily|weekdays|weekly|monthly|yearly
}

// ruleInput is a rule as POST/PATCH accept it: fields left out are taken
// from the reminder's due time.
type ruleInput struct {
	Freq     string `json:"freq"`
	Interval int    `json:"interval"`
	Weekdays []int  `json:"weekdays"`
	MonthDay *int   `json:"monthDay"`
	Month    *int   `json:"month"`
	Hour     *int   `json:"hour"`
	Minute   *int   `json:"minute"`
	TZ       string `json:"tz"`
	Start    string `json:"start"`
}

var repeatShorthands = wordSet("none", "daily", "weekdays", "weekly", "monthly", "yearly")

// apiRule builds the rule a request asks for. set reports whether the
// request touches the rule at all; rule is nil to remove it ("rule": null or
// "repeat": "none"). ref is the time the derived fields come from.
func apiRule(raw json.RawMessage, repeat string, ref time.Time, loc *time.Location) (rule *RecurRule, set bool, err error) {
	repeat = strings.ToLower(strings.TrimSpace(repeat))
	if len(raw) == 0 && repeat == "" {
		return nil, false, nil
	}
	if repeat != "" && !repeatShorthands[repeat] {
		return nil, true, fmt.Errorf("%w: repeat must be none, daily, weekdays, weekly, monthly or yearly", errRecurRule)
	}
	var in ruleInput
	switch {
	case len(raw) > 0 && string(raw) != "null":
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, true, fmt.Errorf("%w: %v", errRecurRule, err)
		}
		if len(in.Weekdays) > 7 {
			return nil, true, fmt.Errorf("%w: weekly needs 1..7 weekdays", errRecurRule)
		}
	case len(raw) > 0 || repeat == "none":
		return nil, true, nil
	case repeat == "weekdays":
		in = ruleInput{Freq: recurWeekly, Weekdays: []int{1, 2, 3, 4, 5}}
	default:
		in = ruleInput{Freq: repeat}
	}
	ref = ref.In(loc)
	if in.TZ != "" {
		if len(in.TZ) > 64 || !reTZName.MatchString(in.TZ) || recurLocation(in.TZ) == nil {
			return nil, true, fmt.Errorf("%w: unknown time zone", errRecurRule)
		}
		ref = ref.In(recurLocation(in.TZ))
	} else if name := loc.String(); name != "Local" {
		in.TZ = name
	}
	or := func(p *int, def int) int {
		if p != nil {
			return *p
		}
		return def
	}
	r := RecurRule{Freq: in.Freq, Interval: in.Interval, Weekdays: in.Weekdays, TZ: in.TZ, Start: in.Start,
		Hour: or(in.Hour, ref.Hour()), Minute: or(in.Minute, ref.Minute())}
	switch in.Freq {
	case recurWeekly:
		if len(r.Weekdays) == 0 {
			r.Weekdays = []int{int(ref.Weekday())}
		}
	case recurMonthly:
		r.MonthDay = or(in.MonthDay, ref.Day())
	case recurYearly:
		r.MonthDay, r.Month = or(in.MonthDay, ref.Day()), or(in.Month, int(ref.Month()))
	}
	if err := r.validate(); err != nil {
		return nil, true, err
	}
	r.normalize()
	return &r, true, nil
}

// startRule anchors an API rule and returns its first occurrence: from ref
// on (an explicit due time), else from now; within the 366-day window.
func startRule(rule *RecurRule, ref, now time.Time, loc *time.Location) (time.Time, error) {
	first, ok := rule.begin(now, ref, loc)
	if !ok || !first.After(now) || first.Sub(now) >= whenMaxAhead {
		return time.Time{}, errReminderWhen
	}
	return first, nil
}

func handleReminderCreate(w http.ResponseWriter, r *http.Request) {
	var b reminderBody
	if !decodeReminderBody(w, r, &b) {
		return
	}
	now, loc, lang := apiClock()
	kind := b.Kind
	if kind == "" {
		kind = reminderKindReminder
	}
	if kind != reminderKindReminder && kind != reminderKindTimer {
		writeJSONErrorCode(w, http.StatusBadRequest, "kind", "kind must be reminder or timer")
		return
	}
	text := ""
	if b.Text != nil {
		text = strings.Join(strings.Fields(cleanText(*b.Text, reminderTextMax+1)), " ")
	}
	if len([]rune(text)) > reminderTextMax {
		writeJSONErrorCode(w, http.StatusBadRequest, "text", "text is longer than 200 characters")
		return
	}
	if text == "" && kind == reminderKindReminder {
		writeJSONErrorCode(w, http.StatusBadRequest, "text", "text is empty")
		return
	}
	person := b.Person
	if person == "household" {
		person = ""
	}
	if person != "" && (!strings.HasPrefix(person, "p_") || !personExists(person)) {
		writeJSONErrorCode(w, http.StatusBadRequest, "person", "unknown person")
		return
	}
	var rec RecurResult
	recOK := false
	if strings.TrimSpace(b.Due) == "" && strings.TrimSpace(b.When) != "" {
		rec, recOK = parseRecurring(truncateRunes(b.When, 300), now, loc) // "jeden Montag um 8"
	}
	var due time.Time
	var dur time.Duration
	var err error
	ruleOnly := false
	switch {
	case recOK:
		due = rec.First
	case strings.TrimSpace(b.Due) == "" && strings.TrimSpace(b.When) == "" && len(b.Rule) > 0 && string(b.Rule) != "null":
		due, ruleOnly = now, true // a complete rule needs no due time
	default:
		if due, dur, err = apiDue(b.Due, b.When, now, loc); err != nil {
			writeReminderError(w, err)
			return
		}
	}
	rule, ruleSet, err := apiRule(b.Rule, b.Repeat, due, loc)
	if err != nil {
		writeReminderError(w, err)
		return
	}
	if !ruleSet && recOK {
		rule = &rec.Rule
	}
	if rule != nil {
		if kind != reminderKindReminder {
			writeJSONErrorCode(w, http.StatusBadRequest, "rule", "only reminders repeat")
			return
		}
		ref := due // an explicit due time is the first occurrence (or bounds it)
		if recOK || ruleOnly {
			ref = time.Time{}
		}
		if due, err = startRule(rule, ref, now, loc); err != nil {
			writeReminderError(w, err)
			return
		}
	}
	x := Reminder{Kind: kind, Text: text, Due: due, Person: person, Lang: lang, Source: "ui", Rule: rule}
	if person != "" && personPrivateReminders(person) {
		x.Private = true
	}
	if kind == reminderKindTimer {
		if dur <= 0 {
			dur = due.Sub(now)
		}
		x.DurationS = int(dur.Round(time.Second) / time.Second)
	}
	saved, err := reminderAdd(x)
	if err != nil {
		writeReminderError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, reminderView(saved, now, lang))
}

func handleReminderPatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if lockedReminder(w, r, id, false) {
		return
	}
	var b reminderBody
	if !decodeReminderBody(w, r, &b) {
		return
	}
	now, loc, lang := apiClock()
	cur, ok := reminderGet(id)
	if !ok {
		writeReminderError(w, errReminderNotFound)
		return
	}
	var text string
	if b.Text != nil {
		text = strings.Join(strings.Fields(cleanText(*b.Text, reminderTextMax+1)), " ")
		if len([]rune(text)) > reminderTextMax {
			writeJSONErrorCode(w, http.StatusBadRequest, "text", "text is longer than 200 characters")
			return
		}
		if text == "" && cur.Kind == reminderKindReminder {
			writeJSONErrorCode(w, http.StatusBadRequest, "text", "text is empty")
			return
		}
	}
	if len(b.Rule) > 0 || b.Repeat != "" {
		if cur.Status == reminderCancelled {
			writeJSONErrorCode(w, http.StatusConflict, "cancelled", "the reminder was cancelled")
			return
		}
		if cur.Kind != reminderKindReminder {
			writeJSONErrorCode(w, http.StatusBadRequest, "rule", "only reminders repeat")
			return
		}
		ref, explicit := cur.Due, false
		if b.Due != "" || b.When != "" {
			due, _, err := apiDue(b.Due, b.When, now, loc)
			if err != nil {
				writeReminderError(w, err)
				return
			}
			ref, explicit = due, true
		}
		rule, _, err := apiRule(b.Rule, b.Repeat, ref, loc)
		if err != nil {
			writeReminderError(w, err)
			return
		}
		due := ref
		if rule != nil {
			from := time.Time{}
			if explicit {
				from = ref
			}
			if due, err = startRule(rule, from, now, loc); err != nil {
				writeReminderError(w, err)
				return
			}
		}
		if err := setReminderRule(id, rule, due); err != nil {
			writeReminderError(w, err)
			return
		}
		b.Due, b.When = "", "" // applied together with the rule
	}
	if b.Due != "" || b.When != "" {
		if cur.Status == reminderCancelled {
			writeJSONErrorCode(w, http.StatusConflict, "cancelled", "the reminder was cancelled")
			return
		}
		due, dur, err := apiDue(b.Due, b.When, now, loc)
		if err != nil {
			writeReminderError(w, err)
			return
		}
		durS := 0
		if cur.Kind == reminderKindTimer {
			if dur <= 0 {
				dur = due.Sub(now)
			}
			durS = int(dur.Round(time.Second) / time.Second)
		}
		if _, _, err := rescheduleReminder(id, due, durS); err != nil {
			writeReminderError(w, err)
			return
		}
	}
	if b.Text != nil {
		if _, _, err := reminderModify(id, func(x *Reminder) error {
			if x.Text == text {
				return errNoChange
			}
			x.Text = text
			return nil
		}); err != nil {
			writeReminderError(w, err)
			return
		}
	}
	cur, ok = reminderGet(id)
	if !ok {
		writeReminderError(w, errReminderNotFound)
		return
	}
	writeJSON(w, http.StatusOK, apiView(r, cur, now, lang))
}

// setReminderRule replaces (or with nil removes) the rule of a reminder;
// due becomes its current occurrence and any announcement stops.
func setReminderRule(id string, rule *RecurRule, due time.Time) error {
	before, _, err := reminderModify(id, func(x *Reminder) error {
		if x.Status == reminderCancelled {
			return errReminderNotFound
		}
		x.Rule = rule.clone()
		if !x.Due.Equal(due.UTC()) || x.Status != reminderPending {
			if rule == nil && x.Status != reminderPending && x.Due.Equal(due.UTC()) {
				return nil // no longer repeating; this occurrence is over
			}
			x.Status, x.Due, x.FiredAt, x.Repeats, x.EventID = reminderPending, due.UTC(), nil, 0, 0
		}
		return nil
	})
	if err != nil {
		return err
	}
	if before.Status == reminderFired && (rule != nil || !before.Due.Equal(due.UTC())) {
		stopAnnouncement(before)
	}
	return nil
}

// handleReminderDelete cancels a reminder; on a series it stops the whole
// series, or with ?scope=occurrence skips only the current occurrence.
func handleReminderDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if lockedReminder(w, r, id, false) {
		return
	}
	switch r.URL.Query().Get("scope") {
	case "", scopeSeries:
	case scopeOccurrence:
		if _, _, err := skipOccurrence(id); err != nil {
			writeReminderError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"skipped": 1})
		return
	default:
		writeJSONErrorCode(w, http.StatusBadRequest, "scope", "scope must be occurrence or series")
		return
	}
	if _, _, err := settleReminder(id, reminderCancelled); err != nil {
		writeReminderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"cancelled": 1})
}

// handleReminderSkip moves a series past its current occurrence.
func handleReminderSkip(w http.ResponseWriter, r *http.Request) {
	if lockedReminder(w, r, r.PathValue("id"), false) {
		return
	}
	if _, _, err := skipOccurrence(r.PathValue("id")); err != nil {
		writeReminderError(w, err)
		return
	}
	now, _, lang := apiClock()
	x, ok := reminderGet(r.PathValue("id"))
	if !ok {
		writeReminderError(w, errReminderNotFound)
		return
	}
	writeJSON(w, http.StatusOK, apiView(r, x, now, lang))
}

// handleReminderDone acknowledges a reminder. On a series it completes only
// the current occurrence; a body {"due": RFC3339} names the occurrence the
// client saw, so a stale "Erledigt" (a replayed notice of an earlier
// occurrence) does not complete the next one.
func handleReminderDone(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if lockedReminder(w, r, id, true) {
		return
	}
	var b struct {
		Due string `json:"due"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, reminderBodyLimit)
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil && !errors.Is(err, io.EOF) {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSONErrorCode(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large")
		} else {
			writeJSONErrorCode(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		}
		return
	}
	if seen := strings.TrimSpace(b.Due); seen != "" {
		at, err := time.Parse(time.RFC3339, seen)
		if err != nil {
			writeJSONErrorCode(w, http.StatusBadRequest, "due", "due must be RFC 3339")
			return
		}
		cur, ok := reminderGet(id)
		if !ok {
			writeReminderError(w, errReminderNotFound)
			return
		}
		if cur.Rule != nil && !cur.Due.Equal(at) {
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "stale": true})
			return
		}
	}
	if _, _, err := settleReminder(id, reminderDone); err != nil {
		writeReminderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func handleReminderSnooze(w http.ResponseWriter, r *http.Request) {
	if lockedReminder(w, r, r.PathValue("id"), true) {
		return
	}
	var b struct {
		Minutes int `json:"minutes"`
	}
	if !decodeReminderBody(w, r, &b) {
		return
	}
	if b.Minutes < 1 || b.Minutes > 240 {
		writeJSONErrorCode(w, http.StatusBadRequest, "minutes", "minutes must be 1..240")
		return
	}
	now, _, lang := apiClock()
	if _, _, err := rescheduleReminder(r.PathValue("id"), now.Add(time.Duration(b.Minutes)*time.Minute), 0); err != nil {
		writeReminderError(w, err)
		return
	}
	x, ok := reminderGet(r.PathValue("id"))
	if !ok {
		writeReminderError(w, errReminderNotFound)
		return
	}
	writeJSON(w, http.StatusOK, apiView(r, x, now, lang))
}

// ---------------------------------------------------------------------------
// Registration

var setReminderImperatives = []string{"erinner mich", "erinnere mich", "erinnere uns", "erinner uns",
	"stell timer", "stell einen timer", "stelle einen timer", "stell den timer", "stell einen wecker",
	"weck mich", "wecke mich", "remind me", "remind us", "set a timer", "set timer", "set an alarm", "wake me",
	"starte einen timer", "start a timer"}

var cancelImperatives = []string{"lösch", "lösche", "entferne", "streich", "streiche", "cancel", "delete", "remove",
	"stopp", "stop", "brich … ab", "abbrechen", "beende"}

func init() {
	registerTool(Tool{
		Name:        "set_reminder",
		Description: "Set a reminder (or a timer) for the user at a time they name; also repeating ones (every Monday, daily, monthly).",
		Params: func(*Turn) json.RawMessage {
			return json.RawMessage(`{"type":"object","properties":{` +
				`"when_text":{"type":"string","description":"the time expression copied verbatim from the user's message, including repeat words, e.g. 'in 20 Minuten', 'tomorrow at 8' or 'jeden Montag um 8'"},` +
				`"text":{"type":"string","description":"what to remind about, in the user's words and language; empty for a plain timer"},` +
				`"when_iso":{"type":"string","description":"optional: the time as ISO 8601 if you are sure"}},` +
				`"required":["when_text"]}`)
		},
		Gate:       []string{"erinner mich", "erinnere mich", "erinnerung", "timer", "wecker", "remind me", "reminder", "alarm", "wecken", "countdown"},
		SideEffect: true,
		Imperative: setReminderImperatives,
		Offer: func(t *Turn, args json.RawMessage) string {
			var a reminderArgs
			_ = json.Unmarshal(args, &a)
			now := turnNow(t)
			w, ok := parseWhen(a.WhenText, now, turnLoc(t))
			text := safeTitle(reminderTextFrom(a.Text, t.Lang, false, false))
			if rec, rok := parseRecurring(a.WhenText, now, turnLoc(t)); rok {
				rule := rec.Rule.Describe(t.Lang)
				if text != "" {
					return pick(t.Lang, "Should I remind you "+rule+" about "+text+"?", "Soll ich dich "+rule+" an "+text+" erinnern?")
				}
				return pick(t.Lang, "Should I set a repeating reminder, "+rule+"?", "Soll ich eine wiederkehrende Erinnerung stellen, "+rule+"?")
			}
			switch {
			case ok && text != "":
				return pick(t.Lang, "Should I remind you "+formatWhen(w.At, now, t.Lang)+" about "+text+"?",
					"Soll ich dich "+formatWhen(w.At, now, t.Lang)+" an "+text+" erinnern?")
			case ok:
				return pick(t.Lang, "Should I set a reminder for "+bareWhen(formatWhen(w.At, now, t.Lang))+"?",
					"Soll ich eine Erinnerung für "+bareWhen(formatWhen(w.At, now, t.Lang))+" stellen?")
			case text != "":
				return pick(t.Lang, "Should I remind you about "+text+"?", "Soll ich dich an "+text+" erinnern?")
			}
			return pick(t.Lang, "Should I set a reminder?", "Soll ich eine Erinnerung stellen?")
		},
		Run: runSetReminder,
	})
	registerTool(Tool{
		Name:        "set_timer",
		Description: "Start a countdown timer for a duration the user names.",
		Params: func(*Turn) json.RawMessage {
			return json.RawMessage(`{"type":"object","properties":{` +
				`"duration_text":{"type":"string","description":"the duration copied verbatim from the user's message, e.g. '10 Minuten'"},` +
				`"label":{"type":"string","description":"optional: what the timer is for, in the user's words"}},` +
				`"required":["duration_text"]}`)
		},
		Gate:       []string{"timer", "countdown", "stoppuhr", "eieruhr"},
		SideEffect: true,
		Imperative: setReminderImperatives,
		Offer: func(t *Turn, args json.RawMessage) string {
			var a reminderArgs
			_ = json.Unmarshal(args, &a)
			now := turnNow(t)
			if w, ok := parseWhen(a.DurationText, now, turnLoc(t)); ok && w.Duration > 0 {
				d := formatDuration(w.Duration, t.Lang)
				return pick(t.Lang, "Should I start a timer for "+d+"?", "Soll ich einen Timer über "+d+" stellen?")
			}
			return pick(t.Lang, "Should I start a timer?", "Soll ich einen Timer stellen?")
		},
		Run: runSetTimer,
	})
	registerTool(Tool{
		Name:        "reminder_list",
		Description: "List the user's upcoming reminders and running timers.",
		Params: func(*Turn) json.RawMessage {
			return json.RawMessage(`{"type":"object","properties":{"timers":{"type":"boolean","description":"true to list only timers"}}}`)
		},
		Gate: []string{"welche erinnerungen", "meine erinnerungen", "welche timer", "my reminders", "which reminders", "what reminders", "which timers"},
		Run:  runReminderList,
	})
	registerTool(Tool{
		Name:        "reminder_cancel",
		Description: "Cancel one of the user's reminders or timers.",
		Params: func(*Turn) json.RawMessage {
			return json.RawMessage(`{"type":"object","properties":{` +
				`"query":{"type":"string","description":"which reminder, in the user's words (e.g. 'Pizza'); empty for the only timer"},` +
				`"timer":{"type":"boolean","description":"true when the user talks about a timer"},` +
				`"scope":{"type":"string","enum":["occurrence","series"],"description":"repeating reminders only: 'occurrence' to skip just the next one, 'series' to stop it for good; leave out if the user did not say"}}}`)
		},
		Gate:       []string{"lösch … erinnerung", "lösche … erinnerung", "lösch … timer", "cancel … reminder", "cancel … timer", "stopp … timer", "stop … timer", "delete … reminder"},
		SideEffect: true,
		Imperative: cancelImperatives,
		Offer: func(t *Turn, args json.RawMessage) string {
			var a reminderCancelArgs
			_ = json.Unmarshal(args, &a)
			if q := safeTitle(a.Query); q != "" {
				return pick(t.Lang, "Should I cancel the reminder about "+q+"?", "Soll ich die Erinnerung an "+q+" löschen?")
			}
			return pick(t.Lang, "Should I cancel it?", "Soll ich sie löschen?")
		},
		Run: runReminderCancel,
	})
	// Fast-path-only tools.
	registerTool(Tool{Name: "reminder_ack", Run: runReminderAck})
	registerTool(Tool{Name: "reminder_fix", SideEffect: true, Access: personalTargetAccess, Run: runReminderFix})
	registerTool(Tool{Name: "reminder_snooze", SideEffect: true, Access: personalTargetAccess, Run: runReminderSnooze})
	registerTool(Tool{Name: "reminder_remaining", Run: runReminderRemaining})

	registerFastPath(FastPath{Name: "reminder_ack", Order: 8, Tool: "reminder_ack", Match: matchReminderAck})
	registerFastPath(FastPath{Name: "reminder_fix", Order: 11, Tool: "reminder_fix", Match: matchReminderFix})
	registerFastPath(FastPath{Name: "snooze", Order: 12, Tool: "reminder_snooze", Match: matchReminderSnooze})
	registerFastPath(FastPath{Name: "reminder_set", Order: 40, Tool: "set_reminder", Match: matchReminderSet})
	registerFastPath(FastPath{Name: "reminder_list", Order: 41, Tool: "reminder_list", Match: matchReminderList})
	registerFastPath(FastPath{Name: "reminder_remaining", Order: 41, Tool: "reminder_remaining", Match: matchReminderRemaining})
	registerFastPath(FastPath{Name: "reminder_cancel", Order: 41, Tool: "reminder_cancel", Match: matchReminderCancel})

	registerContext(30, "reminders", reminderContext)
	registerStatus("reminders", reminderStatus)
	registerBackground("reminders", reminderScheduler)
	onPersonDeleted(clearReminderPerson)

	registerRoute("GET /api/reminders", handleRemindersList)
	registerRoute("POST /api/reminders", handleReminderCreate)
	registerRoute("GET /api/reminders/{id}", handleReminderGet)
	registerRoute("PATCH /api/reminders/{id}", handleReminderPatch)
	registerRoute("DELETE /api/reminders/{id}", handleReminderDelete)
	registerRoute("POST /api/reminders/{id}/snooze", handleReminderSnooze)
	registerRoute("POST /api/reminders/{id}/done", handleReminderDone)
	registerRoute("POST /api/reminders/{id}/skip", handleReminderSkip)
}
