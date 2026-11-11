package hermes

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Harness: the reminders environment (Friday 6 November 2026, 14:30 Berlin)
// with a stub calendar provider owned by Anna. No Google service is used.

type stubCalendar struct {
	mu     sync.Mutex
	events []CalendarEvent
	err    error
	calls  atomic.Int32
	from   time.Time
	to     time.Time
}

func (c *stubCalendar) set(evs ...CalendarEvent) {
	c.mu.Lock()
	c.events = evs
	c.mu.Unlock()
}

func (c *stubCalendar) fail(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
}

func (c *stubCalendar) fetch(_ context.Context, from, to time.Time) ([]CalendarEvent, error) {
	c.calls.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.from, c.to = from, to
	if c.err != nil {
		return nil, c.err
	}
	return append([]CalendarEvent(nil), c.events...), nil
}

func checkinEnv(t *testing.T, voice string) (*remClock, *stubCalendar) {
	t.Helper()
	clock := remEnv(t)
	setBriefSettings(t, func(s *Settings) { s.PiperVoice = voice })
	cal := &stubCalendar{}
	calendarEvents = cal.fetch
	calendarConnectedProvider = nil // the stub stands for a linked calendar
	calendarOwnerProvider = func() string { return "p_anna" }
	lastNudge.Lock()
	lastNudge.at = time.Time{}
	lastNudge.Unlock()
	t.Cleanup(func() {
		checkinCache.Lock()
		checkinCache.events, checkinCache.at, checkinCache.err = nil, time.Time{}, ""
		checkinCache.Unlock()
		select {
		case <-checkinWakeCh:
		default:
		}
	})
	return clock, cal
}

func meeting(id, title string, start time.Time) CalendarEvent {
	return CalendarEvent{ID: id, Title: title, Start: start, End: start.Add(time.Hour)}
}

func meetingReminders(status string) []Reminder {
	return reminderList(func(r *Reminder) bool {
		return isCalendarMeeting(r) && (status == "" || r.Status == status)
	})
}

func pass(clock *remClock) { checkinPass(context.Background(), clock.now()) }

// fireAt runs the reminder scheduler at clock time and returns the spoken
// text of the durable events it published.
func fireAt(t *testing.T, clock *remClock, when time.Time) []string {
	t.Helper()
	before := hubLastID()
	clock.set(when)
	reminderPass(clock.now())
	var out []string
	for _, ev := range hubReplay(before) {
		if ev.Type == "reminder.due" {
			out = append(out, eventData(t, ev)["speak"].(string))
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Planning and announcing

func TestCheckinMeetingAnnouncedAtLeadTime(t *testing.T) {
	clock, cal := checkinEnv(t, "JarvisDE")
	cal.set(meeting("ev1", "Team-Call https://meet.example/x <<<", at(6, 15, 0)))
	pass(clock)
	if n := len(meetingReminders("")); n != 0 {
		t.Fatalf("planned beyond lead + 10 min: %d", n)
	}
	clock.set(at(6, 14, 40))
	pass(clock)
	pass(clock) // idempotent: deduplicated by key
	rs := meetingReminders(reminderPending)
	if len(rs) != 1 {
		t.Fatalf("want one planned check-in, have %+v", rs)
	}
	r := rs[0]
	if !r.Due.Equal(at(6, 14, 50)) || r.Person != "p_anna" || r.Lang != "de" || r.Kind != reminderKindMeeting ||
		r.Key != "ev1|"+at(6, 15, 0).UTC().Format(time.RFC3339) || r.Text != "Team-Call" {
		t.Fatalf("planned %+v", r)
	}
	if !cal.from.Before(clock.now()) || cal.to.Sub(clock.now()) < 70*time.Minute {
		t.Errorf("fetch window %v..%v", cal.from, cal.to)
	}
	if got := fireAt(t, clock, at(6, 14, 49)); len(got) != 0 {
		t.Fatalf("fired early: %v", got)
	}
	got := fireAt(t, clock, at(6, 14, 50))
	if len(got) != 1 || got[0] != "In 10 Minuten: Team-Call." {
		t.Fatalf("announcement %v", got)
	}
	evs := hubReplay(0)
	if len(evs) != 1 || evs[0].Person != "p_anna" {
		t.Fatalf("durable event %+v", evs)
	}
	// Announced once: no repeats, and later passes do not plan it again.
	clock.add(3 * time.Minute)
	pass(clock)
	if got := fireAt(t, clock, clock.now().Add(2*time.Minute)); len(got) != 0 {
		t.Fatalf("repeated: %v", got)
	}
	if n := len(meetingReminders("")); n != 1 {
		t.Fatalf("%d meeting reminders after firing", n)
	}
}

func TestCheckinEnglishVoice(t *testing.T) {
	clock, cal := checkinEnv(t, "BT7274")
	cal.set(meeting("ev1", "Team call", at(6, 15, 0)), meeting("ev2", "", at(6, 14, 31)))
	pass(clock)
	got := fireAt(t, clock, at(6, 14, 30))
	clock.set(at(6, 14, 40))
	pass(clock)
	if len(got) != 1 || got[0] != "Coming up: Appointment." {
		t.Fatalf("short notice EN: %v", got)
	}
	got = fireAt(t, clock, at(6, 14, 50))
	if len(got) != 1 || got[0] != "In 10 minutes: Team call." {
		t.Fatalf("EN announcement: %v", got)
	}
	for _, r := range meetingReminders("") {
		if r.Lang != "en" {
			t.Errorf("lang %q", r.Lang)
		}
	}
}

func TestCheckinShortNoticeAndLongLead(t *testing.T) {
	clock, cal := checkinEnv(t, "JarvisDE")
	// Added at 14:30 for 14:35: due at once.
	cal.set(meeting("soon", "Kurzfristig", at(6, 14, 35)), meeting("now", "Sofort", at(6, 14, 31)))
	pass(clock)
	got := fireAt(t, clock, at(6, 14, 30))
	if len(got) != 2 || !containsStr(got, "In 5 Minuten: Kurzfristig.") || !containsStr(got, "Gleich: Sofort.") {
		t.Fatalf("short notice: %v", got)
	}

	// Lead 45: a 15:30 meeting is planned once it is within lead + 10 min.
	setBriefSettings(t, func(s *Settings) { s.CheckinMeetingLead = 45 })
	cal.set(meeting("late", "Abteilungsrunde", at(6, 15, 30)))
	pass(clock)
	if rs := meetingReminders(reminderPending); len(rs) != 0 {
		t.Fatalf("planned too early: %+v", rs)
	}
	clock.set(at(6, 14, 35))
	pass(clock)
	rs := meetingReminders(reminderPending)
	if len(rs) != 1 || !rs[0].Due.Equal(at(6, 14, 45)) {
		t.Fatalf("lead 45: %+v", rs)
	}
	if got := fireAt(t, clock, at(6, 14, 45)); len(got) != 1 || got[0] != "In 45 Minuten: Abteilungsrunde." {
		t.Fatalf("lead 45 announcement: %v", got)
	}
}

func TestCheckinLeadChangeMovesPlannedAnnouncement(t *testing.T) {
	clock, cal := checkinEnv(t, "JarvisDE")
	cal.set(meeting("ev1", "Team-Call", at(6, 15, 0)))
	pass(clock)
	setBriefSettings(t, func(s *Settings) { s.CheckinMeetingLead = 20 })
	pass(clock)
	rs := meetingReminders(reminderPending)
	if len(rs) != 1 || !rs[0].Due.Equal(at(6, 14, 40)) {
		t.Fatalf("lead change: %+v", rs)
	}
}

func TestCheckinIgnoresAllDayInvitesAndPast(t *testing.T) {
	clock, cal := checkinEnv(t, "JarvisDE")
	allDay := meeting("ad", "Urlaub", at(6, 0, 0))
	allDay.AllDay = true
	invite := meeting("inv", "Einladung", at(6, 15, 0))
	invite.NeedsAction = true
	cal.set(allDay, invite, meeting("past", "Vorbei", at(6, 14, 0)), meeting("far", "Später", at(6, 18, 0)))
	pass(clock)
	if rs := meetingReminders(""); len(rs) != 0 {
		t.Fatalf("planned %+v", rs)
	}
}

func TestCheckinCapThreePerHalfHour(t *testing.T) {
	clock, cal := checkinEnv(t, "JarvisDE")
	cal.set(
		meeting("a", "Eins", at(6, 15, 0)), meeting("b", "Zwei", at(6, 15, 2)), meeting("c", "Drei", at(6, 15, 4)),
		meeting("d", "Vier", at(6, 15, 6)), meeting("e", "Fünf", at(6, 15, 8)),
	)
	clock.set(at(6, 14, 50))
	pass(clock)
	pass(clock)
	rs := meetingReminders(reminderPending)
	if len(rs) != 3 {
		t.Fatalf("want 3 announcements, have %d: %+v", len(rs), rs)
	}
	if rs[2].Text != "Drei. Außerdem 2 weitere Termine in der nächsten halben Stunde" || rs[0].Text != "Eins" {
		t.Fatalf("texts %q / %q", rs[0].Text, rs[2].Text)
	}
	got := fireAt(t, clock, at(6, 14, 54))
	if len(got) == 3 && (got[0] != "In 6 Minuten: Eins." || got[1] != "In 8 Minuten: Zwei.") {
		t.Fatalf("first two %v", got)
	}
	if len(got) != 3 || got[2] != "In 10 Minuten: Drei. Außerdem 2 weitere Termine in der nächsten halben Stunde." {
		t.Fatalf("announcements %v", got)
	}
	// The overflow line stays within the spoken title limit.
	if long := meetingText(strings.Repeat("x", 80), 5, "en"); len([]rune(long)) > 80 || !strings.HasSuffix(long, "Plus 5 more events in the next half hour") {
		t.Errorf("long overflow text %q", long)
	}
	if one := meetingText("Call", 1, "en"); one != "Call. Plus one more event in the next half hour" {
		t.Errorf("one overflow %q", one)
	}
}

func TestCheckinFollowsCalendarChanges(t *testing.T) {
	clock, cal := checkinEnv(t, "JarvisDE")
	ev := meeting("ev1", "Team-Call", at(6, 15, 0))
	cal.set(ev, meeting("ev2", "Weg", at(6, 15, 5)))
	clock.set(at(6, 14, 45))
	pass(clock)
	if n := len(meetingReminders(reminderPending)); n != 2 {
		t.Fatalf("%d planned", n)
	}
	// ev2 deleted, ev1 renamed: withdrawn and retitled.
	ev.Title = "Team-Call (neu)"
	cal.set(ev)
	pass(clock)
	rs := meetingReminders("")
	if len(rs) != 1 || rs[0].Text != "Team-Call (neu)" || rs[0].Status != reminderPending {
		t.Fatalf("after change %+v", rs)
	}
	// ev1 moved to 15:03: the old one goes, a new one is planned.
	ev.Start = at(6, 15, 3)
	cal.set(ev)
	pass(clock)
	rs = meetingReminders("")
	if len(rs) != 1 || !rs[0].Due.Equal(at(6, 14, 53)) {
		t.Fatalf("after move %+v", rs)
	}
	// The user cancels the check-in: it stays cancelled.
	if _, _, err := settleReminder(rs[0].ID, reminderCancelled); err != nil {
		t.Fatal(err)
	}
	pass(clock)
	if rs := meetingReminders(reminderPending); len(rs) != 0 {
		t.Fatalf("user-cancelled check-in came back: %+v", rs)
	}
}

func TestCheckinCalendarDownOrDisconnected(t *testing.T) {
	clock, cal := checkinEnv(t, "JarvisDE")
	cal.set(meeting("ev1", "Team-Call", at(6, 15, 0)))
	clock.set(at(6, 14, 40))
	pass(clock)
	// A failing fetch keeps the plan.
	cal.fail(errors.New("network down"))
	pass(clock)
	if n := len(meetingReminders(reminderPending)); n != 1 {
		t.Fatalf("plan dropped on a fetch error: %d", n)
	}
	if st := checkinStatus(context.Background()).(map[string]any); st["ok"] != false || st["connected"] != true {
		t.Errorf("status %v", st)
	}
	// Meeting check-ins switched off: withdrawn.
	cal.fail(nil)
	setBriefSettings(t, func(s *Settings) { s.CheckinMeetings = false })
	pass(clock)
	if n := len(meetingReminders("")); n != 0 {
		t.Fatalf("still planned with check-ins off: %d", n)
	}
	// Back on, then the calendar is disconnected (nil provider): no panic, withdrawn.
	setBriefSettings(t, func(s *Settings) { s.CheckinMeetings = true })
	pass(clock)
	if n := len(meetingReminders(reminderPending)); n != 1 {
		t.Fatalf("not replanned: %d", n)
	}
	calendarEvents = nil
	calls := cal.calls.Load()
	pass(clock)
	if n := len(meetingReminders("")); n != 0 || cal.calls.Load() != calls {
		t.Fatalf("disconnected: %d planned, fetched=%v", n, cal.calls.Load() != calls)
	}
	if busy := checkinBusyUntil(clock.now()); !busy.IsZero() {
		t.Errorf("busy without calendar: %v", busy)
	}
}

// ---------------------------------------------------------------------------
// Pause

func TestCheckinPauseByVoiceGerman(t *testing.T) {
	clock, cal := checkinEnv(t, "JarvisDE")
	cal.set(meeting("ev1", "Team-Call", at(6, 15, 0)))
	pass(clock)
	explicit := addTestReminder(t, Reminder{Text: "Tee", Due: at(6, 14, 55)})

	r := remSay(t, "Hermes, keine Check-ins mehr heute", remAnna)
	if !r.handled || r.text != "Okay, heute keine Check-ins mehr." {
		t.Fatalf("pause reply %+v", r.text)
	}
	if until := checkinsPausedUntil(); !until.Equal(at(7, 0, 0)) {
		t.Fatalf("paused until %v", until)
	}
	if n := len(meetingReminders("")); n != 0 {
		t.Fatalf("meeting check-in not withdrawn: %d", n)
	}
	pass(clock)
	if n := len(meetingReminders("")); n != 0 {
		t.Fatalf("planned while paused: %d", n)
	}
	// Explicit reminders still fire.
	if got := fireAt(t, clock, at(6, 14, 55)); len(got) != 1 || !strings.Contains(got[0], "Tee") {
		t.Fatalf("explicit reminder during the pause: %v", got)
	}
	if x, _ := reminderGet(explicit.ID); x.Status != reminderFired {
		t.Fatalf("explicit %+v", x)
	}
	if st := checkinStateView(); st["pausedUntil"] != at(7, 0, 0).UTC().Format(time.RFC3339) {
		t.Errorf("state %v", st)
	}

	// Questions are not commands; resume replans.
	if r := remSay(t, "Wie aktiviere ich die Check-ins?", remAnna); r.handled && r.turn.toolName == "checkins_pause" {
		t.Fatalf("question handled as a command: %q", r.text)
	}
	if r := remSay(t, "Check-ins wieder an", remAnna); r.text != "Okay, die Check-ins sind wieder an." {
		t.Fatalf("resume reply %q", r.text)
	}
	if !checkinsPausedUntil().IsZero() {
		t.Fatal("still paused")
	}
	pass(clock)
	if n := len(meetingReminders(reminderPending)); n != 1 {
		t.Fatalf("not replanned after resume: %d", n)
	}
}

func TestCheckinPauseByVoiceEnglishAndUndo(t *testing.T) {
	clock, _ := checkinEnv(t, "BT7274")
	r := remSay(t, "pause check-ins for an hour", remAnna, "en")
	if !r.handled || !strings.HasPrefix(r.text, "Okay, no check-ins until ") || !strings.Contains(r.text, "3:30") {
		t.Fatalf("EN pause %q", r.text)
	}
	if until := checkinsPausedUntil(); !until.Equal(clock.now().Add(time.Hour)) {
		t.Fatalf("until %v", until)
	}
	if r := remSay(t, "pause the check ins for 30 minutes", remAnna, "en"); !strings.Contains(r.text, "3:00") {
		t.Fatalf("30 min pause %q", r.text)
	}
	if r := remSay(t, "no more check-ins today", remAnna, "en"); r.text != "Okay, no more check-ins today." {
		t.Fatalf("today pause %q", r.text)
	}
	if r := remSay(t, "resume check-ins", remAnna, "en"); r.text != "Okay, check-ins are back on." {
		t.Fatalf("resume %q", r.text)
	}
	if r := remSay(t, "Pausiere die Check-ins für eine Stunde", remAnna); !strings.HasPrefix(r.text, "Okay, keine Check-ins bis ") {
		t.Fatalf("DE hour pause %q", r.text)
	}
	runUndo(t, remAnna)
	if !checkinsPausedUntil().IsZero() {
		t.Fatal("undo did not lift the pause")
	}
}

func TestCheckinNotNowOnlyRightAfterACheckin(t *testing.T) {
	clock, cal := checkinEnv(t, "JarvisDE")
	if r := remSay(t, "nicht jetzt", remAnna); r.handled && r.turn.toolName == "checkins_pause" {
		t.Fatal("'nicht jetzt' without a check-in paused")
	}
	cal.set(meeting("ev1", "Team-Call", at(6, 14, 40)))
	pass(clock)
	fireAt(t, clock, at(6, 14, 30))
	clock.add(time.Minute)
	if r := remSay(t, "Nicht jetzt", remAnna); r.turn.toolName != "checkins_pause" || !strings.HasPrefix(r.text, "Okay, keine Check-ins bis ") {
		t.Fatalf("'nicht jetzt' after a check-in: %q", r.text)
	}
	if until := checkinsPausedUntil(); !until.Equal(clock.now().Add(time.Hour)) {
		t.Fatalf("until %v", until)
	}
	// A sitting nudge from the browser counts too; English; window 2 min.
	if err := applyCheckinPause(time.Time{}); err != nil {
		t.Fatal(err)
	}
	clock.add(10 * time.Minute)
	if r := remSay(t, "not now", remAnna, "en"); r.turn.toolName == "checkins_pause" {
		t.Fatal("'not now' long after the check-in paused")
	}
	h := newMux()
	if code, _, _ := doJSON(t, h, "POST", "/api/checkins/nudged", `{}`); code != 200 {
		t.Fatalf("nudged %d", code)
	}
	if r := remSay(t, "not right now", remAnna, "en"); r.turn.toolName != "checkins_pause" {
		t.Fatalf("'not right now' after a nudge: %q", r.text)
	}
}

// ---------------------------------------------------------------------------
// Browser state and API

func TestCheckinBusyAndQuietHours(t *testing.T) {
	clock, cal := checkinEnv(t, "JarvisDE")
	invite := meeting("inv", "Einladung", at(6, 14, 0))
	invite.NeedsAction = true
	cal.set(meeting("run", "Läuft", at(6, 14, 0)), invite)
	pass(clock)
	if busy := checkinBusyUntil(clock.now()); !busy.Equal(at(6, 15, 0)) {
		t.Fatalf("busy until %v", busy)
	}
	if busy := checkinBusyUntil(clock.now().Add(time.Hour)); !busy.IsZero() {
		t.Fatalf("stale cache still busy: %v", busy)
	}
	s := defaultSettings()
	s.Timezone = "Europe/Berlin"
	for _, c := range []struct {
		start, end string
		now        time.Time
		want       bool
	}{
		{"22:00", "07:00", at(6, 23, 0), true}, {"22:00", "07:00", at(6, 6, 59), true},
		{"22:00", "07:00", at(6, 7, 0), false}, {"22:00", "07:00", at(6, 14, 30), false},
		{"12:00", "14:00", at(6, 13, 0), true}, {"12:00", "14:00", at(6, 14, 0), false},
		{"08:00", "08:00", at(6, 8, 0), false}, {"bad", "07:00", at(6, 3, 0), false},
	} {
		s.QuietStart, s.QuietEnd = c.start, c.end
		if got := inQuietHours(s, c.now); got != c.want {
			t.Errorf("quiet %s-%s at %v = %v", c.start, c.end, c.now, got)
		}
	}
}

func TestCheckinAPI(t *testing.T) {
	clock, cal := checkinEnv(t, "JarvisDE")
	cal.set(meeting("run", "Läuft", at(6, 14, 0)))
	pass(clock)
	h := newMux()
	code, m, raw := doJSON(t, h, "GET", "/api/checkins/state", "")
	if code != 200 || m["busyUntil"] != at(6, 15, 0).UTC().Format(time.RFC3339) || m["pausedUntil"] != "" || m["quiet"] != false {
		t.Fatalf("state %d %s", code, raw)
	}
	code, m, raw = doJSON(t, h, "POST", "/api/checkins/pause", `{"minutes":30}`)
	if code != 200 || m["pausedUntil"] != at(6, 15, 0).UTC().Format(time.RFC3339) {
		t.Fatalf("pause 30: %d %s", code, raw)
	}
	if code, m, _ = doJSON(t, h, "POST", "/api/checkins/pause", `{"today":true}`); code != 200 || m["pausedUntil"] != at(7, 0, 0).UTC().Format(time.RFC3339) {
		t.Fatalf("pause today: %d %v", code, m)
	}
	if code, m, _ = doJSON(t, h, "POST", "/api/checkins/pause", `{"resume":true}`); code != 200 || m["pausedUntil"] != "" {
		t.Fatalf("resume: %d %v", code, m)
	}
	for _, bad := range []string{`{"minutes":0}`, `{"minutes":5000}`, `{}`, `nope`} {
		if code, _, _ := doJSON(t, h, "POST", "/api/checkins/pause", bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if code, _, _ := doJSON(t, h, "POST", "/api/checkins/pause", `{"minutes":1,"x":"`+strings.Repeat("a", 5000)+`"}`); code != 413 {
		t.Errorf("body cap: %d", code)
	}
	if st := checkinStatus(context.Background()).(map[string]any); st["meetings"] != true || st["lead"] != 10 || st["ok"] != true {
		t.Errorf("status %v", st)
	}
}

func TestCheckinWorkerWakesAndStops(t *testing.T) {
	_, cal := checkinEnv(t, "JarvisDE")
	old := checkinPoll
	checkinPoll = time.Hour
	t.Cleanup(func() { checkinPoll = old })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { checkinWorker(ctx); close(done) }()
	waitFor := func(n int32) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for cal.calls.Load() < n {
			if time.Now().After(deadline) {
				t.Fatalf("calendar polled %d times, want %d", cal.calls.Load(), n)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitFor(1) // first pass at start
	checkinWake()
	waitFor(2)
	runSecretHooks(&googleChangeHooks) // a new Google client re-checks at once
	waitFor(3)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
}
