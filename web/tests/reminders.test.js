"use strict";
// Run: node --test tests/js/
// Pure helpers of static/reminders.js (HermesReminders).
const test = require("node:test");
const assert = require("node:assert/strict");
const R = require("../static/reminders.js");

const trEn = R.makeTr(R.STRINGS, "en");
const trDe = R.makeTr(R.STRINGS, "de");

function dueEvent(o, data) {
  return Object.assign(
    {
      id: 42,
      type: "reminder.due",
      at: "2026-11-06T14:32:00Z",
      person: "p_ab12cd34ef56",
      data: Object.assign(
        {
          reminderId: "r_0123456789ab",
          kind: "reminder",
          text: "die Pizza rausnehmen",
          due: "2026-11-06T14:32:00Z",
          personName: "Anna",
          speak: "Anna, Erinnerung: die Pizza rausnehmen.",
          lang: "de",
          private: false,
          repeat: 0,
          chime: true,
          clientId: "c_3f9a0b1c2d3e",
        },
        data || {}
      ),
    },
    o || {}
  );
}

// ------------------------------------------------------------------ speak here

test("shouldSpeakHere: stored preference wins, default on for localhost only", () => {
  assert.equal(R.shouldSpeakHere(true, null), true);
  assert.equal(R.shouldSpeakHere(false, null), false);
  assert.equal(R.shouldSpeakHere(true, undefined), true);
  assert.equal(R.shouldSpeakHere(false, ""), false, "empty / junk falls back to the default");
  assert.equal(R.shouldSpeakHere(true, "junk"), true);
  assert.equal(R.shouldSpeakHere(true, "false"), false);
  assert.equal(R.shouldSpeakHere(true, false), false);
  assert.equal(R.shouldSpeakHere(true, "0"), false);
  assert.equal(R.shouldSpeakHere(false, "true"), true);
  assert.equal(R.shouldSpeakHere(false, true), true);
  assert.equal(R.shouldSpeakHere(false, "1"), true);
});

test("shouldSpeakHere agrees with hermes.js speakHerePref for stored strings", () => {
  const Hm = require("../static/hermes.js");
  for (const local of [true, false]) {
    for (const raw of [null, "true", "false", "1", "0", "", "x"]) {
      assert.equal(R.shouldSpeakHere(local, raw), Hm.speakHerePref(raw, local), JSON.stringify([local, raw]));
    }
  }
});

// ------------------------------------------------------------------ quiet hours

test("parseHHMM: valid and invalid clock strings", () => {
  assert.equal(R.parseHHMM("22:00"), 1320);
  assert.equal(R.parseHHMM("7:05"), 425);
  assert.equal(R.parseHHMM(" 00:00 "), 0);
  assert.equal(R.parseHHMM("23:59"), 1439);
  for (const bad of ["24:00", "12:60", "", null, undefined, "7", "07:5", "ab:cd", "07:00:00"]) {
    assert.equal(R.parseHHMM(bad), null, String(bad));
  }
});

test("inQuietHours: range across midnight (22:00–07:00), end exclusive", () => {
  const at = (h, m) => new Date(2026, 10, 6, h, m, 0);
  assert.equal(R.inQuietHours(at(21, 59), "22:00", "07:00"), false);
  assert.equal(R.inQuietHours(at(22, 0), "22:00", "07:00"), true);
  assert.equal(R.inQuietHours(at(23, 30), "22:00", "07:00"), true);
  assert.equal(R.inQuietHours(at(0, 0), "22:00", "07:00"), true);
  assert.equal(R.inQuietHours(at(6, 59), "22:00", "07:00"), true);
  assert.equal(R.inQuietHours(at(7, 0), "22:00", "07:00"), false);
  assert.equal(R.inQuietHours(at(12, 0), "22:00", "07:00"), false);
});

test("inQuietHours: range within a day, empty and invalid ranges", () => {
  const at = (h, m) => new Date(2026, 10, 6, h, m, 0);
  assert.equal(R.inQuietHours(at(13, 0), "12:30", "14:00"), true);
  assert.equal(R.inQuietHours(at(12, 29), "12:30", "14:00"), false);
  assert.equal(R.inQuietHours(at(14, 0), "12:30", "14:00"), false);
  assert.equal(R.inQuietHours(at(3, 0), "22:00", "22:00"), false, "start == end: no quiet hours");
  assert.equal(R.inQuietHours(at(3, 0), "junk", "07:00"), false);
  assert.equal(R.inQuietHours(new Date(NaN), "22:00", "07:00"), false);
  assert.equal(R.inQuietHours(at(23, 0).getTime(), "22:00", "07:00"), true, "ms timestamps work");
});

test("inQuietHours: honours the settings time zone", () => {
  // 21:30 UTC = 22:30 in Berlin (CET, November) and 16:30 in New York
  const t = Date.UTC(2026, 10, 6, 21, 30);
  assert.equal(R.inQuietHours(t, "22:00", "07:00", "Europe/Berlin"), true);
  assert.equal(R.inQuietHours(t, "22:00", "07:00", "America/New_York"), false);
  assert.equal(R.minutesOfDay(t, "UTC"), 21 * 60 + 30);
  // an unknown zone falls back to the browser's local time instead of throwing
  const local = new Date(t);
  assert.equal(R.minutesOfDay(t, "Not/AZone"), local.getHours() * 60 + local.getMinutes());
});

// ------------------------------------------------------------------ dedupe

test("dedupeSpoken: first claim is fresh, second is not; bounded to the newest", () => {
  let r = R.dedupeSpoken([], "rem:r_a:0");
  assert.equal(r.fresh, true);
  assert.deepEqual(r.list, ["rem:r_a:0"]);
  r = R.dedupeSpoken(r.list, "rem:r_a:0");
  assert.equal(r.fresh, false);
  r = R.dedupeSpoken(r.list, "rem:r_a:1");
  assert.equal(r.fresh, true, "a repeat is a new announcement");
  let list = [];
  for (let i = 0; i < 60; i++) list = R.dedupeSpoken(list, "k" + i).list;
  assert.equal(list.length, 50);
  assert.equal(list[0], "k10");
  assert.equal(R.dedupeSpoken(list, "k5").fresh, true, "evicted keys count as new");
  assert.equal(R.dedupeSpoken("junk", "x").fresh, true, "non-array storage is ignored");
  assert.deepEqual(R.dedupeSpoken([{}, null, "a"], "b").list, ["a", "b"], "junk entries are dropped");
  assert.equal(R.dedupeSpoken(["a", "b", "c"], "d", 2).list.length, 2);
});

test("speechKey: per reminder and repeat, falls back to the event id", () => {
  const d = R.normalizeDue(dueEvent());
  const sec = Date.UTC(2026, 10, 6, 14, 32) / 1000;
  assert.equal(R.speechKey(d), "rem:r_0123456789ab:" + sec + ":0");
  assert.equal(R.speechKey(R.normalizeDue(dueEvent({}, { repeat: 3 }))), "rem:r_0123456789ab:" + sec + ":3");
  assert.notEqual(R.speechKey(R.normalizeDue(dueEvent({}, { due: "2026-11-06T14:42:00Z" }))), R.speechKey(d), "a snoozed firing is new");
  assert.equal(R.speechKey(R.normalizeDue(dueEvent({}, { due: "2026-11-06T15:32:00+01:00" }))), R.speechKey(d), "same instant, other offset");
  assert.equal(R.speechKey(R.normalizeDue(dueEvent({ id: 7 }, { reminderId: "" }))), "rem:e7:0");
  assert.equal(R.speechKey(null), "");
});

// ------------------------------------------------------------------ events

test("normalizeDue: validates, caps and keeps private text off", () => {
  const d = R.normalizeDue(dueEvent({ missed: true }));
  assert.equal(d.eventId, 42);
  assert.equal(d.reminderId, "r_0123456789ab");
  assert.equal(d.kind, "reminder");
  assert.equal(d.text, "die Pizza rausnehmen");
  assert.equal(d.personName, "Anna");
  assert.equal(d.missed, true);
  assert.equal(d.chime, true);

  assert.equal(R.normalizeDue(null), null);
  assert.equal(R.normalizeDue({ id: 1 }), null, "no data");
  assert.equal(R.normalizeDue(dueEvent({}, { text: "", speak: "" })), null, "nothing to say");
  assert.equal(R.normalizeDue(dueEvent({}, { kind: "evil" })).kind, "reminder");
  assert.equal(R.normalizeDue(dueEvent({}, { kind: "timer" })).kind, "timer");
  assert.equal(R.normalizeDue(dueEvent({}, { repeat: 99 })).repeat, R.REPEAT_MAX);
  assert.equal(R.normalizeDue(dueEvent({}, { repeat: -3 })).repeat, 0);
  assert.equal(R.normalizeDue(dueEvent({}, { repeat: "2" })).repeat, 0);
  assert.equal(R.normalizeDue(dueEvent({}, { reminderId: "r_<img>" })).reminderId, "", "junk ids are dropped");
  assert.equal(R.normalizeDue(dueEvent({ id: -5 })).eventId, 0);
  assert.equal(R.normalizeDue(dueEvent({}, { chime: false })).chime, false);
  assert.equal(R.normalizeDue(dueEvent({}, { text: "a\u0000b\u0007c" })).text, "abc", "control characters stripped");
  assert.equal(Array.from(R.normalizeDue(dueEvent({}, { text: "x".repeat(2000) })).text).length, 500);
  assert.equal(R.normalizeDue(dueEvent({}, { personName: { x: 1 } })).personName, "", "wrong types");

  const p = R.normalizeDue(dueEvent({}, { private: true, text: "Therapie", speak: "Anna, du hast eine Erinnerung." }));
  assert.equal(p.private, true);
  assert.equal(p.text, "", "a private event never keeps text");
  const p2 = R.normalizeDue(dueEvent({}, { private: true, text: "", speak: "" }));
  assert.ok(p2, "a private event without speak is still delivered (generic line)");
});

// ------------------------------------------------------------------ repeat / ack state

test("tracker: repeats announce once each, older repeats and replays are dropped", () => {
  const t = R.createTracker();
  const ev = (rep) => R.normalizeDue(dueEvent({}, { repeat: rep }));
  assert.equal(t.due(ev(0), 1000), true);
  assert.equal(t.due(ev(0), 1500), false, "replay of the same announcement");
  assert.equal(t.due(ev(1), 61000), true);
  assert.equal(t.due(ev(1), 61500), false);
  assert.equal(t.due(ev(0), 62000), false, "older repeat after a newer one");
  assert.equal(t.due(ev(3), 180000), true, "a skipped repeat number is fine");
  assert.equal(t.open(180000).length, 1);
  assert.equal(t.willRepeat("r_0123456789ab"), true);
  assert.equal(t.due(ev(5), 300000), true);
  assert.equal(t.willRepeat("r_0123456789ab"), false, "repeat 5 is the last");
});

test("tracker: an ack stops later repeats, also ones already on the wire", () => {
  const t = R.createTracker();
  const ev = (rep) => R.normalizeDue(dueEvent({}, { repeat: rep }));
  assert.equal(t.due(ev(0), 0), true);
  assert.equal(t.isAcked("r_0123456789ab"), false);
  assert.equal(t.ack("r_0123456789ab", 5000), true);
  assert.equal(t.ack("r_0123456789ab", 5001), false, "second ack is a no-op");
  assert.equal(t.isAcked("r_0123456789ab"), true);
  assert.equal(t.due(ev(1), 60000), false, "repeat after the ack");
  assert.equal(t.open(60000).length, 0);
  assert.equal(t.willRepeat("r_0123456789ab"), false);
  assert.equal(t.ack("r_unknown00000", 1), false, "ack of an unknown reminder");
  assert.equal(t.ack("", 1), false);
});

test("tracker: reopen after a failed ack lets repeats through again", () => {
  const t = R.createTracker();
  const ev = (rep) => R.normalizeDue(dueEvent({}, { repeat: rep }));
  t.due(ev(0), 0);
  t.ack("r_0123456789ab", 10);
  assert.equal(t.reopen("r_0123456789ab"), true);
  assert.equal(t.isAcked("r_0123456789ab"), false);
  assert.equal(t.due(ev(0), 20), false, "still no duplicate of an announced repeat");
  assert.equal(t.due(ev(1), 60000), true);
  assert.equal(t.reopen("r_0123456789ab"), false, "not acked: nothing to reopen");
});

test("tracker: a snoozed reminder fires again; a late ack of the old firing does not block it", () => {
  const t = R.createTracker();
  const first = R.normalizeDue(dueEvent({}, { repeat: 0 }));
  t.due(first, 0);
  t.ack("r_0123456789ab", 1000); // "Schlummern" pressed
  // the old firing's reminder.acked arrives late: still the old cycle
  assert.equal(t.ack("r_0123456789ab", 1200), false);
  const again = R.normalizeDue(dueEvent({ id: 99 }, { repeat: 0, due: "2026-11-06T14:42:00Z" }));
  assert.equal(t.due(again, 600000), true, "new due time = new firing");
  assert.equal(t.isAcked("r_0123456789ab"), false);
  assert.equal(t.due(first, 600100), false, "a replay of the old firing stays acked");
});

test("tracker: meetings never repeat; events without id always pass; old state expires", () => {
  const t = R.createTracker({ ttlMs: 1000 });
  const m = R.normalizeDue(dueEvent({}, { kind: "meeting", reminderId: "r_meeting00001" }));
  assert.equal(t.due(m, 0), true);
  assert.equal(t.willRepeat("r_meeting00001"), false);
  const anon = R.normalizeDue(dueEvent({}, { reminderId: "" }));
  assert.equal(t.due(anon, 0), true);
  assert.equal(t.due(anon, 1), true, "untracked: the event-id dedupe handles it");
  assert.equal(t.size(), 1);
  t.open(5000);
  assert.equal(t.size(), 0, "garbage-collected after the ttl");
});

// ------------------------------------------------------------------ delivery ladder

const BASE = { claimed: true, fresh: true, speakHere: true, hidden: false, notifyGranted: false, gesture: true, quiet: false, missedSpoken: 0 };

test("deliveryPlan: speak-here device chimes and speaks and acks the hub event", () => {
  const d = R.normalizeDue(dueEvent());
  assert.deepEqual(R.deliveryPlan(d, BASE), { notice: true, speak: true, chime: true, defer: false, notify: false, ack: true });
  const noChime = R.normalizeDue(dueEvent({}, { chime: false }));
  assert.equal(R.deliveryPlan(noChime, BASE).chime, false);
});

test("deliveryPlan: not claimed or stale does nothing", () => {
  const d = R.normalizeDue(dueEvent());
  for (const ctx of [Object.assign({}, BASE, { claimed: false }), Object.assign({}, BASE, { fresh: false }), null]) {
    assert.deepEqual(R.deliveryPlan(d, ctx), { notice: false, speak: false, chime: false, defer: false, notify: false, ack: false });
  }
  assert.equal(R.deliveryPlan(null, BASE).notice, false);
});

test("deliveryPlan: speak here off gives a silent notice; hidden + no speech keeps the event durable", () => {
  const d = R.normalizeDue(dueEvent());
  const off = R.deliveryPlan(d, Object.assign({}, BASE, { speakHere: false }));
  assert.equal(off.notice, true);
  assert.equal(off.speak, false);
  assert.equal(off.chime, false);
  assert.equal(off.ack, true, "visible tab: the notice is seen");
  const hidden = R.deliveryPlan(d, Object.assign({}, BASE, { speakHere: false, hidden: true }));
  assert.equal(hidden.ack, false, "nobody notices it here: leave it for a speaking device");
  const hiddenNotify = R.deliveryPlan(d, Object.assign({}, BASE, { speakHere: false, hidden: true, notifyGranted: true }));
  assert.equal(hiddenNotify.notify, true);
  assert.equal(hiddenNotify.ack, true);
});

test("deliveryPlan: hidden tab with permission also notifies; visible tab does not", () => {
  const d = R.normalizeDue(dueEvent());
  assert.equal(R.deliveryPlan(d, Object.assign({}, BASE, { hidden: true, notifyGranted: true })).notify, true);
  assert.equal(R.deliveryPlan(d, Object.assign({}, BASE, { hidden: true, notifyGranted: false })).notify, false);
  assert.equal(R.deliveryPlan(d, Object.assign({}, BASE, { hidden: false, notifyGranted: true })).notify, false);
  assert.equal(R.deliveryPlan(d, Object.assign({}, BASE, { hidden: true, notifyGranted: true })).speak, true, "speech too");
});

test("deliveryPlan: audio locked defers speech to the first gesture", () => {
  const d = R.normalizeDue(dueEvent({ missed: true }));
  const p = R.deliveryPlan(d, Object.assign({}, BASE, { gesture: false }));
  assert.equal(p.speak, false);
  assert.equal(p.chime, false);
  assert.equal(p.defer, true);
  assert.equal(p.notice, true);
  assert.equal(p.ack, true);
});

test("deliveryPlan: quiet hours never mute reminders, timers or meetings, but mute other kinds", () => {
  for (const kind of ["reminder", "timer", "meeting"]) {
    const d = R.normalizeDue(dueEvent({}, { kind }));
    assert.equal(R.deliveryPlan(d, Object.assign({}, BASE, { quiet: true })).speak, true, kind);
  }
  const other = Object.assign(R.normalizeDue(dueEvent()), { kind: "checkin" });
  const p = R.deliveryPlan(other, Object.assign({}, BASE, { quiet: true }));
  assert.equal(p.speak, false);
  assert.equal(p.notice, true);
  assert.equal(R.deliveryPlan(other, BASE).speak, true, "outside quiet hours it speaks");
});

test("deliveryPlan: a burst of missed events speaks only the first few", () => {
  const d = R.normalizeDue(dueEvent({ missed: true }));
  assert.equal(R.deliveryPlan(d, Object.assign({}, BASE, { missedSpoken: R.MISSED_SPEAK_MAX - 1 })).speak, true);
  const p = R.deliveryPlan(d, Object.assign({}, BASE, { missedSpoken: R.MISSED_SPEAK_MAX }));
  assert.equal(p.speak, false);
  assert.equal(p.notice, true);
  const live = R.normalizeDue(dueEvent());
  assert.equal(R.deliveryPlan(live, Object.assign({}, BASE, { missedSpoken: 99 })).speak, true, "live events always speak");
});

test("deliveryPlan: event without durable id is never acked", () => {
  const d = R.normalizeDue(dueEvent({ id: undefined }));
  assert.equal(R.deliveryPlan(d, BASE).ack, false);
});

// ------------------------------------------------------------------ texts

test("spokenText / noticeText / notificationBody: normal, missed, timer (DE/EN)", () => {
  const d = R.normalizeDue(dueEvent());
  assert.equal(R.spokenText(d, trDe), "Anna, Erinnerung: die Pizza rausnehmen.");
  assert.equal(R.noticeText(d, trDe), "die Pizza rausnehmen");
  assert.equal(R.notificationBody(d, trDe), "die Pizza rausnehmen");
  const m = R.normalizeDue(dueEvent({ missed: true }));
  assert.equal(R.spokenText(m, trDe), "Verpasst: Anna, Erinnerung: die Pizza rausnehmen.");
  assert.equal(R.noticeText(m, trEn), "Missed: die Pizza rausnehmen");
  const timer = R.normalizeDue(dueEvent({}, { kind: "timer", text: "", speak: "Your 5 minute timer is done." }));
  assert.equal(R.noticeText(timer, trEn), "Your 5 minute timer is done.");
  assert.equal(R.spokenText(timer, trEn), "Your 5 minute timer is done.");
});

test("private reminders: generic texts everywhere, the name only where allowed", () => {
  const p = R.normalizeDue(dueEvent({}, { private: true, text: "Therapie", speak: "Anna, du hast eine Erinnerung." }));
  assert.equal(R.spokenText(p, trDe), "Anna, du hast eine Erinnerung.");
  assert.equal(R.noticeText(p, trDe), "Erinnerung für Anna – „Anzeigen“ antippen");
  assert.equal(R.noticeText(p, trEn), "Reminder for Anna – tap “Show” to see it");
  assert.equal(R.notificationBody(p, trDe), "Du hast eine Erinnerung.", "lock screens get no name and no text");
  for (const s of [R.spokenText(p, trDe), R.noticeText(p, trDe), R.notificationBody(p, trEn)]) {
    assert.ok(!s.includes("Therapie"), s);
  }
  const anon = R.normalizeDue(dueEvent({}, { private: true, speak: "", personName: "" }));
  assert.equal(R.spokenText(anon, trEn), "You have a reminder.");
  assert.equal(R.noticeText(anon, trDe), "Du hast eine Erinnerung – „Anzeigen“ antippen");
});

test("awayText: one, many, private", () => {
  assert.equal(R.awayText([], trDe), "");
  assert.equal(R.awayText([R.normalizeDue(dueEvent())], trDe), "Während du weg warst: die Pizza rausnehmen");
  assert.equal(R.awayText([R.normalizeDue(dueEvent()), R.normalizeDue(dueEvent())], trEn), "While you were away: 2 reminders");
  const p = R.normalizeDue(dueEvent({}, { private: true }));
  assert.equal(R.awayText([p], trEn), "While you were away: You have a reminder.");
});

test("STRINGS: every key exists in both languages; never calls the user Pilot", () => {
  const en = Object.keys(R.STRINGS.en).sort();
  const de = Object.keys(R.STRINGS.de).sort();
  assert.deepEqual(de, en);
  for (const l of ["en", "de"]) {
    for (const [k, v] of Object.entries(R.STRINGS[l])) {
      assert.equal(typeof v, "string", l + "." + k);
      assert.ok(v.length > 0, l + "." + k);
      assert.ok(!/pilot/i.test(v), l + "." + k);
    }
  }
  // memories are never called "Erinnerung" elsewhere; here the word is right
  assert.equal(R.STRINGS.de.tab, "Erinnerungen");
});

// ------------------------------------------------------------------ hub tab

test("validateAdd: text and when required and capped", () => {
  assert.deepEqual(R.validateAdd("  Pizza  ", " in 20 Minuten "), { ok: true, text: "Pizza", when: "in 20 Minuten" });
  assert.equal(R.validateAdd("", "in 5 min").key, "errText");
  assert.equal(R.validateAdd("   ", "in 5 min").key, "errText");
  assert.equal(R.validateAdd("Pizza", "").key, "errWhenEmpty");
  const long = R.validateAdd("x".repeat(R.TEXT_MAX + 1), "in 5 min");
  assert.equal(long.key, "errTextLong");
  assert.deepEqual(long.vars, { n: R.TEXT_MAX });
  assert.equal(R.validateAdd("ä".repeat(R.TEXT_MAX), "in 5 min").ok, true, "counted in characters, not bytes");
  assert.equal(R.validateAdd("Pizza", "x".repeat(R.WHEN_MAX + 1)).key, "errWhen");
  assert.equal(R.validateAdd(null, undefined).key, "errText");
});

test("validateSnooze: integer minutes 1..240", () => {
  assert.equal(R.validateSnooze(10), 10);
  assert.equal(R.validateSnooze("5"), 5);
  assert.equal(R.validateSnooze(240), 240);
  for (const bad of [0, -1, 241, 2.5, NaN, Infinity, "x", null, undefined, {}]) {
    assert.equal(R.validateSnooze(bad), 0, String(bad));
  }
  assert.deepEqual(R.SNOOZE_CHOICES, [5, 10, 30]);
});

test("visibleReminders: fired first, then pending, each by due; others hidden", () => {
  const list = [
    { id: "r_000000000001", status: "pending", due: "2026-11-06T18:00:00Z" },
    { id: "r_000000000002", status: "done", due: "2026-11-06T10:00:00Z" },
    { id: "r_000000000003", status: "fired", due: "2026-11-06T14:00:00Z" },
    { id: "r_000000000004", status: "pending", due: "2026-11-06T15:00:00Z" },
    { id: "r_000000000005", status: "cancelled", due: "2026-11-06T15:00:00Z" },
    { id: "r_000000000006", status: "fired", due: "2026-11-06T13:00:00Z" },
    { id: "r_000000000007", status: "pending", due: "junk" },
    { id: "<b>", status: "pending", due: "2026-11-06T15:00:00Z" },
    null,
    "x",
  ];
  const g = R.visibleReminders(list);
  assert.deepEqual(
    g.fired.map((r) => r.id),
    ["r_000000000006", "r_000000000003"]
  );
  assert.deepEqual(
    g.pending.map((r) => r.id),
    ["r_000000000004", "r_000000000001", "r_000000000007"],
    "unparseable due sorts last"
  );
  assert.deepEqual(R.visibleReminders(null), { fired: [], pending: [] });
});

test("errorKey: maps server codes and statuses", () => {
  const e = (status, code, message) => Object.assign(new Error(message || "x"), { status, code: code || "" });
  assert.equal(R.errorKey(e(400, "when")), "errWhen");
  assert.equal(R.errorKey(e(403, "locked")), "errLocked");
  assert.equal(R.errorKey(e(409, "limit")), "errLimit");
  assert.equal(R.errorKey(e(404)), "errGone");
  assert.equal(R.errorKey(e(503, "", "data file was written by a newer HERMES; refusing to overwrite")), "errReadOnly");
  assert.equal(R.errorKey(e(503, "", "reminders not configured")), "errOffline");
  assert.equal(R.errorKey(e(405)), "errOffline", "older server without the route");
  assert.equal(R.errorKey(Object.assign(new Error("Failed to fetch"), {})), "errOffline", "network error");
  assert.equal(R.errorKey(e(500)), "errGeneric");
  assert.equal(R.errorKey(null), "errGeneric");
});

test("cleanStr: strips controls, trims, caps by code point", () => {
  assert.equal(R.cleanStr("  a\tb\nc\u0001 ", 10), "a\tb\nc");
  assert.equal(R.cleanStr("😀😀😀", 2), "😀😀");
  assert.equal(R.cleanStr(5, 10), "");
});

// ------------------------------------------------------------------ browser part (stub window)

const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const STATIC = path.join(__dirname, "..", "static");

/*
 * Load hermes.js + reminders.js into a stub window. The api records what
 * the module asked app.js to do; fetchJSON answers from `routes`.
 */
function loadBrowser(opts) {
  opts = opts || {};
  const calls = [];
  const storage = new Map(Object.entries(opts.storage || {}));
  const win = {
    console,
    setTimeout,
    clearTimeout,
    Promise,
    Date,
    Intl,
    JSON,
    Math,
    Object,
    Array,
    String,
    Number,
    RegExp,
    Error,
    isSecureContext: opts.secure !== false,
    location: { host: opts.host || "localhost:8080" },
    navigator: { userAgent: "node" },
    localStorage: {
      getItem: (k) => (storage.has(k) ? storage.get(k) : null),
      setItem: (k, v) => storage.set(k, String(v)),
      removeItem: (k) => storage.delete(k),
    },
    document: { visibilityState: opts.hidden ? "hidden" : "visible", documentElement: { lang: "de" } },
  };
  if (opts.notification) {
    const N = function (title, o) {
      calls.push(["notify", title, o.body]);
      this.close = () => {};
    };
    N.permission = opts.notification;
    win.Notification = N;
  }
  win.window = win;
  vm.createContext(win);
  vm.runInContext(fs.readFileSync(path.join(STATIC, "hermes.js"), "utf8"), win, { filename: "hermes.js" });
  vm.runInContext(fs.readFileSync(path.join(STATIC, "reminders.js"), "utf8"), win, { filename: "reminders.js" });
  const notices = [];
  const routes = opts.routes || {};
  let gesture = opts.gesture !== false;
  win.Hermes.api = {
    lang: () => opts.lang || "de",
    settings: () => Object.assign({ quietStart: "22:00", quietEnd: "07:00", timezone: "" }, opts.settings || {}),
    gestureSeen: () => gesture,
    isLocalhost: () => /^localhost/.test(win.location.host),
    isSpeaking: () => false,
    chime: () => {
      calls.push(["chime"]);
      return Promise.resolve(true);
    },
    say: (text, o) => {
      calls.push(["say", text, !!(o && o.force)]);
      return Promise.resolve();
    },
    flashEye: (s, b) => calls.push(["eye", s, b]),
    toast: (msg, o) => {
      calls.push(["toast", msg, o && o.key]);
      return { close: () => calls.push(["toastClose", o && o.key]) };
    },
    stopSpeaking: () => calls.push(["stopSpeaking"]),
    addNotice: (text, o) => {
      const n = { text, kind: o.kind, actions: o.actions || [], ephemeral: !!o.ephemeral, removed: false };
      notices.push(n);
      return {
        update: (t, a) => {
          if (t != null) n.text = t;
          if (Array.isArray(a)) n.actions = a;
        },
        remove: () => (n.removed = true),
      };
    },
    fetchJSON: (url, o) => {
      const method = (o && o.method) || "GET";
      calls.push(["fetch", method, url, o && o.body]);
      const h = routes[method + " " + url];
      if (!h) return Promise.resolve({});
      try {
        return Promise.resolve(h(o && o.body));
      } catch (e) {
        return Promise.reject(e);
      }
    },
  };
  win.Hermes.emit("ready");
  return {
    win,
    calls,
    notices,
    storage,
    H: win.Hermes,
    setGesture: (v) => (gesture = v),
  };
}

const settle = () => new Promise((r) => setTimeout(r, 20));

test("browser: a due reminder chimes, speaks with force, adds a notice and acks the hub event", async () => {
  const b = loadBrowser();
  b.H.emit("hub:reminder.due", dueEvent());
  await settle();
  const kinds = b.calls.map((c) => c[0]);
  assert.ok(kinds.indexOf("chime") >= 0 && kinds.indexOf("chime") < kinds.indexOf("say"), "chime before speech");
  assert.deepEqual(b.calls.find((c) => c[0] === "say"), ["say", "Anna, Erinnerung: die Pizza rausnehmen.", true]);
  assert.deepEqual(b.calls.find((c) => c[0] === "eye"), ["eye", "speaking", "ERINNERUNG"]);
  const ack = b.calls.find((c) => c[0] === "fetch" && c[2] === "/api/events/ack");
  assert.deepEqual(JSON.parse(JSON.stringify(ack[3])), { ids: [42] });
  assert.equal(b.notices.length, 1);
  assert.equal(b.notices[0].text, "die Pizza rausnehmen");
  assert.equal(b.notices[0].kind, "reminder");
  assert.deepEqual(JSON.parse(JSON.stringify(b.notices[0].actions.map((a) => a.label))), ["Erledigt", "Schlummern 10 Min."]);
});

test("browser: a repeat refreshes the notice; reminder.acked stops further repeats", async () => {
  const b = loadBrowser();
  b.H.emit("hub:reminder.due", dueEvent());
  await settle();
  b.H.emit("hub:reminder.due", dueEvent({ id: 43 }, { repeat: 1, speak: "Noch einmal: Anna, Erinnerung: die Pizza rausnehmen." }));
  await settle();
  assert.equal(b.notices.length, 1, "no second notice");
  assert.equal(b.calls.filter((c) => c[0] === "say").length, 2);
  b.H.emit("hub:reminder.acked", { type: "reminder.acked", data: { reminderId: "r_0123456789ab" } });
  await settle();
  assert.equal(b.notices[0].actions.length, 0, "buttons gone");
  assert.equal(b.notices[0].text, "die Pizza rausnehmen · erledigt");
  b.H.emit("hub:reminder.due", dueEvent({ id: 44 }, { repeat: 2 }));
  await settle();
  assert.equal(b.calls.filter((c) => c[0] === "say").length, 2, "acked: not spoken again");
});

test("browser: the Erledigt button posts done; a failed request reopens the notice", async () => {
  let fail = false;
  const b = loadBrowser({
    routes: {
      "POST /api/reminders/r_0123456789ab/done": () => {
        if (fail) throw Object.assign(new Error("boom"), { status: 500, code: "" });
        return { ok: true };
      },
    },
  });
  b.H.emit("hub:reminder.due", dueEvent());
  await settle();
  fail = true;
  b.notices[0].actions.find((a) => a.label === "Erledigt").fn();
  await settle();
  assert.ok(b.calls.some((c) => c[0] === "fetch" && c[1] === "POST" && c[2] === "/api/reminders/r_0123456789ab/done"));
  assert.equal(b.notices[0].actions.length, 2, "buttons back after the failure");
  assert.equal(b.notices[0].text, "die Pizza rausnehmen");
  b.H.emit("hub:reminder.due", dueEvent({ id: 43 }, { repeat: 1 }));
  await settle();
  assert.equal(b.calls.filter((c) => c[0] === "say").length, 2, "repeats come through again");
  fail = false;
  b.notices[0].actions.find((a) => a.label === "Erledigt").fn();
  await settle();
  assert.equal(b.notices[0].actions.length, 0);
});

test("browser: snooze posts minutes and the reminder can fire again later", async () => {
  const b = loadBrowser({ routes: { "POST /api/reminders/r_0123456789ab/snooze": () => ({ id: "r_0123456789ab", status: "pending" }) } });
  b.H.emit("hub:reminder.due", dueEvent());
  await settle();
  b.notices[0].actions.find((a) => /Schlummern/.test(a.label)).fn();
  await settle();
  const call = b.calls.find((c) => c[2] === "/api/reminders/r_0123456789ab/snooze");
  assert.deepEqual(JSON.parse(JSON.stringify(call[3])), { minutes: 10 });
  assert.match(b.notices[0].text, /in 10 Min\. noch einmal/);
  // late ack of the old firing, then the snoozed reminder fires
  b.H.emit("hub:reminder.acked", { type: "reminder.acked", data: { reminderId: "r_0123456789ab" } });
  b.H.emit("hub:reminder.due", dueEvent({ id: 60 }, { due: "2026-11-06T14:42:00Z" }));
  await settle();
  assert.equal(b.calls.filter((c) => c[0] === "say").length, 2);
  assert.equal(b.notices.length, 2, "a new notice for the new firing");
});

test("browser: private reminder notice is ephemeral and generic; Anzeigen fetches the text", async () => {
  const b = loadBrowser({ routes: { "GET /api/reminders/r_0123456789ab": () => ({ id: "r_0123456789ab", text: "Therapie" }) } });
  b.H.emit("hub:reminder.due", dueEvent({}, { private: true, text: "", speak: "Anna, du hast eine Erinnerung." }));
  await settle();
  assert.equal(b.notices[0].ephemeral, true, "never stored in the conversation");
  assert.equal(b.notices[0].kind, "lock");
  assert.ok(!b.notices[0].text.includes("Therapie"));
  assert.deepEqual(b.calls.find((c) => c[0] === "say"), ["say", "Anna, du hast eine Erinnerung.", true]);
  b.notices[0].actions.find((a) => a.label === "Anzeigen").fn();
  await settle();
  assert.equal(b.notices[0].text, "Therapie");
  assert.ok(!b.notices[0].actions.some((a) => a.label === "Anzeigen"), "show button gone once revealed");
});

test("browser: LAN device without a stored choice shows a silent notice", async () => {
  const b = loadBrowser({ host: "192.168.1.20:8080", secure: false });
  b.H.emit("hub:reminder.due", dueEvent());
  await settle();
  assert.equal(b.notices.length, 1);
  assert.ok(!b.calls.some((c) => c[0] === "say" || c[0] === "chime"));
});

test("browser: audio locked defers speech to the first gesture with a banner", async () => {
  const b = loadBrowser({ gesture: false });
  b.H.emit("hub:reminder.due", dueEvent({ missed: true }));
  await settle();
  assert.ok(!b.calls.some((c) => c[0] === "say"));
  const banner = b.calls.find((c) => c[0] === "toast" && c[2] === "rem-away");
  assert.equal(banner[1], "Während du weg warst: Verpasst: die Pizza rausnehmen".replace("Verpasst: ", ""));
  b.setGesture(true);
  b.H.emit("gesture");
  await settle();
  await settle();
  assert.deepEqual(b.calls.find((c) => c[0] === "say"), ["say", "Verpasst: Anna, Erinnerung: die Pizza rausnehmen.", true]);
});

test("browser: hidden tab with permission shows a system notification (private: generic)", async () => {
  const b = loadBrowser({ hidden: true, notification: "granted" });
  b.H.emit("hub:reminder.due", dueEvent({}, { private: true, speak: "Anna, du hast eine Erinnerung." }));
  await settle();
  assert.deepEqual(b.calls.find((c) => c[0] === "notify"), ["notify", "HERMES", "Du hast eine Erinnerung."]);
});

test("browser: a stop while a reminder speaks marks it done", async () => {
  const b = loadBrowser({ routes: { "POST /api/reminders/r_0123456789ab/done": () => ({ ok: true }) } });
  let release;
  b.H.api.say = (text) => {
    b.calls.push(["say", text]);
    return new Promise((r) => (release = r));
  };
  b.H.emit("hub:reminder.due", dueEvent());
  await settle();
  b.H.emit("stop", { spoken: true });
  await settle();
  assert.ok(b.calls.some((c) => c[2] === "/api/reminders/r_0123456789ab/done"));
  assert.ok(b.calls.some((c) => c[0] === "stopSpeaking"));
  release();
});

test("browser: a reminder claimed by another tab (localStorage dedupe) is not spoken twice", async () => {
  const b = loadBrowser({ storage: { "hermes-spoken-ids": JSON.stringify(["rem:r_0123456789ab:" + Date.UTC(2026, 10, 6, 14, 32) / 1000 + ":0"]) } });
  b.H.events.start(); // no EventSource in the stub: start() only marks started
  b.H.emit("hub:reminder.due", dueEvent());
  await settle();
  assert.ok(!b.calls.some((c) => c[0] === "say"));
  assert.equal(b.notices.length, 0);
});

// ------------------------------------------------------------------ repeating reminders

const weeklyRow = {
  id: "r_0123456789ab",
  kind: "reminder",
  status: "pending",
  text: "den Müll",
  due: "2026-11-09T07:00:00Z",
  dueText: "am Montag, 9. November, um 8 Uhr",
  rule: { freq: "weekly", interval: 1, weekdays: [1], hour: 8, minute: 0, tz: "Europe/Berlin", start: "2026-11-09" },
  ruleText: "jeden Montag um 8 Uhr",
};

test("validateAdd / addBody: repeat choice is passed on, 'none' and junk are dropped", () => {
  const v = R.validateAdd("Müll", "am Montag um 8", "weekly");
  assert.equal(v.repeat, "weekly");
  assert.deepEqual(R.addBody(v), { text: "Müll", when: "am Montag um 8", repeat: "weekly" });
  for (const r of ["none", "hourly", "", null, undefined, 5, "__proto__"]) {
    const x = R.validateAdd("Müll", "morgen", r);
    assert.equal(x.ok, true);
    assert.equal("repeat" in x, false, String(r));
    assert.deepEqual(R.addBody(x), { text: "Müll", when: "morgen" });
  }
  assert.equal(R.addBody(R.validateAdd("", "morgen", "daily")), null);
  assert.deepEqual(R.REPEAT_CHOICES, ["none", "daily", "weekdays", "weekly", "monthly", "yearly"]);
});

test("isRecurring: only rows with a known rule", () => {
  assert.equal(R.isRecurring(weeklyRow), true);
  assert.equal(R.isRecurring({ id: "r_1", rule: null }), false);
  assert.equal(R.isRecurring({ id: "r_1" }), false);
  assert.equal(R.isRecurring({ rule: { freq: "hourly" } }), false);
  assert.equal(R.isRecurring({ rule: ["weekly"] }), false);
  assert.equal(R.isRecurring(null), false);
});

test("describeRule: matches the server's wording (DE/EN)", () => {
  const cases = [
    [{ freq: "weekly", interval: 1, weekdays: [1], hour: 8, minute: 0 }, "jeden Montag um 8 Uhr", "every Monday at 8:00 AM"],
    [{ freq: "daily", interval: 1, hour: 22, minute: 0 }, "jeden Tag um 22 Uhr", "every day at 10:00 PM"],
    [{ freq: "daily", interval: 3, hour: 7, minute: 30 }, "alle 3 Tage um 7:30 Uhr", "every 3 days at 7:30 AM"],
    [{ freq: "weekly", interval: 1, weekdays: [1, 2, 3, 4, 5], hour: 9, minute: 0 }, "jeden Werktag um 9 Uhr", "every weekday at 9:00 AM"],
    [{ freq: "weekly", interval: 1, weekdays: [1, 4], hour: 18, minute: 0 }, "jeden Montag und Donnerstag um 18 Uhr", "every Monday and Thursday at 6:00 PM"],
    [{ freq: "monthly", interval: 1, monthDay: 1, hour: 9, minute: 0 }, "jeden Monat am 1. um 9 Uhr", "on the 1st of every month at 9:00 AM"],
    [{ freq: "monthly", interval: 1, monthDay: 31, hour: 9, minute: 0 }, "jeden Monat am Monatsletzten um 9 Uhr", "on the last day of every month at 9:00 AM"],
    [{ freq: "yearly", interval: 1, month: 12, monthDay: 24, hour: 0, minute: 0 }, "jedes Jahr am 24. Dezember um 0 Uhr", "every year on December 24 at 12:00 AM"],
  ];
  for (const [rule, de, en] of cases) {
    assert.equal(R.describeRule(rule, "de"), de);
    assert.equal(R.describeRule(rule, "en"), en);
  }
  for (const bad of [null, {}, { freq: "weekly", weekdays: [] }, { freq: "monthly" }, { freq: "yearly", month: 13, monthDay: 1 }, "daily"]) {
    assert.equal(R.describeRule(bad, "de"), "", JSON.stringify(bad));
  }
});

test("ruleText: server text first (cleaned, capped), local fallback, none for one-offs", () => {
  assert.equal(R.ruleText(weeklyRow, "en"), "jeden Montag um 8 Uhr", "the server renders it in the UI language");
  assert.equal(R.ruleText(Object.assign({}, weeklyRow, { ruleText: "" }), "en"), "every Monday at 8:00 AM");
  assert.equal(R.ruleText(Object.assign({}, weeklyRow, { ruleText: "a\u0007b" }), "de"), "ab");
  assert.equal(R.ruleText(Object.assign({}, weeklyRow, { ruleText: "x".repeat(500) }), "de").length, 160);
  assert.equal(R.ruleText({ id: "r_1", ruleText: "jeden Tag" }, "de"), "", "no rule: not repeating");
});

test("rowActions: one-off vs series, ringing vs waiting, meetings", () => {
  const one = { id: "r_1", kind: "reminder" };
  assert.deepEqual(R.rowActions(one, false), ["snooze:5", "snooze:10", "snooze:30", "cancel"]);
  assert.deepEqual(R.rowActions(one, true), ["done", "snooze:5", "snooze:10", "snooze:30", "cancel"]);
  assert.deepEqual(R.rowActions(weeklyRow, false), ["skip", "stop"]);
  assert.deepEqual(R.rowActions(weeklyRow, true), ["done", "snooze:5", "snooze:10", "snooze:30", "stop"]);
  assert.deepEqual(R.rowActions({ id: "r_2", kind: "meeting" }, true), ["done", "cancel"]);
});

test("errorKey: rule and not_recurring codes", () => {
  assert.equal(R.errorKey({ status: 400, code: "rule" }), "errRule");
  assert.equal(R.errorKey({ status: 409, code: "not_recurring" }), "errNotRecurring");
  assert.equal(R.errorKey({ status: 409, code: "limit" }), "errLimit");
});

test("normalizeDue: recurring flag and rule text; never for private events", () => {
  const d = R.normalizeDue(dueEvent({}, { recurring: true, ruleText: "jeden Montag um 8 Uhr" }));
  assert.equal(d.recurring, true);
  assert.equal(d.ruleText, "jeden Montag um 8 Uhr");
  const p = R.normalizeDue(dueEvent({}, { private: true, text: "", speak: "Anna, du hast eine Erinnerung.", recurring: true, ruleText: "jeden Tag" }));
  assert.equal(p.ruleText, "");
  assert.equal(R.normalizeDue(dueEvent({}, { recurring: "yes" })).recurring, false);
});

test("browser: a series notice shows the rule; Erledigt names the occurrence; the next occurrence is a new notice", async () => {
  const b = loadBrowser({ routes: { "POST /api/reminders/r_0123456789ab/done": () => ({ ok: true }) } });
  b.H.emit("hub:reminder.due", dueEvent({}, { recurring: true, ruleText: "jeden Montag um 8 Uhr", due: "2026-11-09T07:00:00Z" }));
  await settle();
  assert.equal(b.notices[0].text, "die Pizza rausnehmen · Wiederholt sich jeden Montag um 8 Uhr");
  b.notices[0].actions.find((a) => a.label === "Erledigt").fn();
  await settle();
  const call = b.calls.find((c) => c[2] === "/api/reminders/r_0123456789ab/done");
  assert.deepEqual(JSON.parse(JSON.stringify(call[3])), { due: "2026-11-09T07:00:00Z" });
  // a week later: same id, new due
  b.H.emit("hub:reminder.due", dueEvent({ id: 77 }, { recurring: true, ruleText: "jeden Montag um 8 Uhr", due: "2026-11-16T07:00:00Z" }));
  await settle();
  assert.equal(b.notices.length, 2, "a new notice for the next occurrence");
  assert.equal(b.calls.filter((c) => c[0] === "say").length, 2);
  assert.match(b.notices[0].text, /erledigt$/, "the old notice stays settled");
});
