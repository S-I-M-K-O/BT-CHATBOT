"use strict";
// Run: node --test tests/js/
// static/checkins.js (HermesCheckins) and the pure helpers of
// static/integrations.js (HermesIntegrations).
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const C = require("../static/checkins.js");
const I = require("../static/integrations.js");
const R = require("../static/reminders.js");

const MIN = 60 * 1000;
const T0 = Date.parse("2026-11-09T09:00:00Z");

const active = { userState: "active", screenState: "unlocked" };
const idle = { userState: "idle", screenState: "unlocked" };
const locked = { userState: "idle", screenState: "locked" };

function ctx(o) {
  return Object.assign({ enabled: true, minutes: 60 }, o || {});
}

// ------------------------------------------------------------------ sitting streak

test("sitting: a streak of continuous activity reaches the threshold and nudges", () => {
  let s = C.sitObserve(C.sitInit(), active, T0);
  assert.equal(s.streakStart, T0);
  let d = C.sitDecide(s, ctx(), T0 + 59 * MIN);
  assert.equal(d.action, "none");
  assert.equal(d.reason, "sitting");
  assert.equal(d.until, T0 + 60 * MIN);
  d = C.sitDecide(d.state, ctx(), T0 + 60 * MIN);
  assert.equal(d.action, "nudge");
  assert.equal(d.minutes, 60);
});

test("sitting: the setting is off, or nobody is there → no nudge", () => {
  const s = C.sitObserve(C.sitInit(), active, T0);
  assert.equal(C.sitDecide(s, ctx({ enabled: false }), T0 + 90 * MIN).action, "none");
  assert.equal(C.sitDecide(C.sitInit(), ctx(), T0 + 90 * MIN).reason, "idle");
});

test("sitting: a short idle stretch (< 5 min) keeps the streak", () => {
  let s = C.sitObserve(C.sitInit(), active, T0);
  // idle reported at +30 min means idle since +29 min (60 s threshold)
  s = C.sitObserve(s, idle, T0 + 30 * MIN);
  assert.equal(s.idleSince, T0 + 29 * MIN);
  assert.equal(C.sitDecide(s, ctx(), T0 + 60 * MIN).reason, "idle"); // not while away
  s = C.sitObserve(s, active, T0 + 32 * MIN); // 3 min away
  assert.equal(s.streakStart, T0);
  assert.equal(C.sitDecide(s, ctx(), T0 + 60 * MIN).action, "nudge");
});

test("sitting: idle for ≥ 5 min resets the streak (on return and on a tick)", () => {
  let s = C.sitObserve(C.sitInit(), active, T0);
  s = C.sitObserve(s, idle, T0 + 30 * MIN); // idle since +29
  s = C.sitObserve(s, active, T0 + 34 * MIN); // 5 min away
  assert.equal(s.streakStart, T0 + 34 * MIN);
  assert.equal(C.sitDecide(s, ctx(), T0 + 60 * MIN).action, "none");
  assert.equal(C.sitDecide(s, ctx(), T0 + 94 * MIN).action, "nudge");

  let t = C.sitObserve(C.sitInit(), active, T0);
  t = C.sitObserve(t, idle, T0 + 10 * MIN);
  t = C.sitTick(t, T0 + 15 * MIN); // still idle, 6 min since +9
  assert.equal(t.streakStart, 0);
});

test("sitting: a locked screen ends the streak at once", () => {
  let s = C.sitObserve(C.sitInit(), active, T0);
  s = C.sitObserve(s, locked, T0 + 50 * MIN);
  assert.equal(s.streakStart, 0);
  assert.equal(s.locked, true);
  s = C.sitObserve(s, active, T0 + 51 * MIN);
  assert.equal(s.streakStart, T0 + 51 * MIN);
  assert.equal(C.sitDecide(s, ctx(), T0 + 61 * MIN).action, "none");
});

test("sitting: a long gap without ticks (the computer slept) restarts the streak", () => {
  let s = C.sitObserve(C.sitInit(), active, T0);
  s = C.sitTick(s, T0 + 20 * MIN);
  s = C.sitTick(s, T0 + 21 * MIN);
  s = C.sitTick(s, T0 + 80 * MIN); // nothing for 59 min
  assert.equal(s.streakStart, T0 + 80 * MIN);
});

test("sitting: a calendar event in progress suppresses, then retries 5 min after it ends", () => {
  let s = C.sitObserve(C.sitInit(), active, T0);
  const busyUntil = T0 + 75 * MIN;
  let d = C.sitDecide(s, ctx({ busyUntil }), T0 + 60 * MIN);
  assert.equal(d.action, "wait");
  assert.equal(d.reason, "busy");
  assert.equal(d.until, busyUntil + 5 * MIN);
  s = d.state;
  // the event ended (server no longer reports it), but the retry time holds
  d = C.sitDecide(s, ctx(), T0 + 76 * MIN);
  assert.equal(d.action, "wait");
  d = C.sitDecide(d.state, ctx(), T0 + 80 * MIN);
  assert.equal(d.action, "nudge");
  assert.equal(d.minutes, 80);
});

test("sitting: paused (voice), paused on this device, snoozed, quiet and speaking all wait", () => {
  const s = C.sitObserve(C.sitInit(), active, T0);
  const at = T0 + 61 * MIN;
  assert.equal(C.sitDecide(s, ctx({ pausedUntil: at + MIN }), at).reason, "paused");
  assert.equal(C.sitDecide(s, ctx({ localPausedUntil: at + MIN }), at).reason, "paused");
  assert.equal(C.sitDecide(s, ctx({ snoozeUntil: at + MIN }), at).reason, "snoozed");
  assert.equal(C.sitDecide(s, ctx({ quiet: true }), at).reason, "quiet");
  assert.equal(C.sitDecide(s, ctx({ speaking: true }), at).reason, "speaking");
  // expired pauses do not count
  assert.equal(C.sitDecide(s, ctx({ pausedUntil: at - MIN, snoozeUntil: at - 1 }), at).action, "nudge");
});

test("sitting: after a nudge a new streak starts and the line rotates", () => {
  let s = C.sitObserve(C.sitInit(), active, T0);
  const d = C.sitDecide(s, ctx({ minutes: 20 }), T0 + 20 * MIN);
  assert.equal(d.action, "nudge");
  s = C.sitNudged(d.state, T0 + 20 * MIN);
  assert.equal(s.streakStart, T0 + 20 * MIN);
  assert.equal(s.nudges, 1);
  assert.equal(C.sitDecide(s, ctx({ minutes: 20 }), T0 + 30 * MIN).action, "none");
  assert.equal(C.sitDecide(s, ctx({ minutes: 20 }), T0 + 40 * MIN).action, "nudge");
});

test("sitting: the minutes setting is clamped to 20..240", () => {
  assert.equal(C.clampMinutes(5), 20);
  assert.equal(C.clampMinutes(999), 240);
  assert.equal(C.clampMinutes("45"), 45);
  assert.equal(C.clampMinutes(undefined), 60);
  assert.equal(C.clampMinutes(NaN), 60);
  assert.equal(C.clampMinutes(""), 60);
  const s = C.sitObserve(C.sitInit(), active, T0);
  assert.equal(C.sitDecide(s, ctx({ minutes: 1 }), T0 + 19 * MIN).action, "none");
  assert.equal(C.sitDecide(s, ctx({ minutes: 1 }), T0 + 20 * MIN).action, "nudge");
});

test("sitting: pure functions never mutate their input", () => {
  const s0 = C.sitObserve(C.sitInit(), active, T0);
  const snap = JSON.stringify(s0);
  C.sitObserve(s0, locked, T0 + MIN);
  C.sitDecide(s0, ctx({ busyUntil: T0 + 90 * MIN }), T0 + 61 * MIN);
  C.sitNudged(s0, T0 + 61 * MIN);
  C.sitTick(s0, T0 + 200 * MIN);
  assert.equal(JSON.stringify(s0), snap);
});

// ------------------------------------------------------------------ quiet hours, lines, state

test("quiet hours across midnight in the settings time zone (via reminders.js)", () => {
  const s = { quietStart: "22:00", quietEnd: "07:00", timezone: "Europe/Berlin" };
  // Berlin is UTC+1 in November
  assert.equal(C.isQuiet(Date.parse("2026-11-09T20:59:00Z"), s), false); // 21:59
  assert.equal(C.isQuiet(Date.parse("2026-11-09T21:00:00Z"), s), true); // 22:00
  assert.equal(C.isQuiet(Date.parse("2026-11-09T23:30:00Z"), s), true); // 00:30
  assert.equal(C.isQuiet(Date.parse("2026-11-10T05:59:00Z"), s), true); // 06:59
  assert.equal(C.isQuiet(Date.parse("2026-11-10T06:00:00Z"), s), false); // 07:00 (end exclusive)
  // a same-day window and "no quiet hours"
  const day = { quietStart: "12:00", quietEnd: "13:00", timezone: "UTC" };
  assert.equal(C.isQuiet(Date.parse("2026-11-09T12:30:00Z"), day), true);
  assert.equal(C.isQuiet(Date.parse("2026-11-09T13:00:00Z"), day), false);
  assert.equal(C.isQuiet(Date.parse("2026-11-09T23:00:00Z"), { quietStart: "00:00", quietEnd: "00:00", timezone: "UTC" }), false);
  // defaults 22:00–07:00 when unset
  assert.equal(C.isQuiet(Date.parse("2026-11-09T23:00:00Z"), { timezone: "UTC" }), true);
  // same answer as reminders.js
  const at = Date.parse("2026-11-09T21:30:00Z");
  assert.equal(C.isQuiet(at, s), R.inQuietHours(new Date(at), "22:00", "07:00", "Europe/Berlin"));
});

test("sitting lines: German and English, rotating, with the minutes, never 'Pilot'", () => {
  assert.equal(C.sittingLine(60, "de", 0), "Du sitzt seit 60 Minuten. Zeit, kurz aufzustehen und dich zu strecken.");
  assert.equal(C.sittingLine(60, "en", 0), "You have been sitting for 60 minutes. Time to stand up and stretch.");
  assert.notEqual(C.sittingLine(60, "en", 1), C.sittingLine(60, "en", 0));
  assert.equal(C.sittingLine(60, "en", C.LINES.en.length), C.sittingLine(60, "en", 0));
  assert.equal(C.sittingLine(60, "fr", 0), C.sittingLine(60, "en", 0));
  assert.equal(C.LINES.de.length, C.LINES.en.length);
  for (const l of ["de", "en"]) {
    C.LINES[l].forEach((line, i) => {
      const out = C.sittingLine(75, l, i);
      assert.match(out, /75/);
      assert.doesNotMatch(out, /\{n\}|pilot/i);
    });
  }
});

test("check-in state from the server: RFC3339 → ms, junk → 0, capped at 2 days", () => {
  const now = T0;
  assert.deepEqual(C.normalizeState({ busyUntil: "2026-11-09T10:00:00Z", pausedUntil: "" }, now), { busyUntil: Date.parse("2026-11-09T10:00:00Z"), pausedUntil: 0 });
  assert.deepEqual(C.normalizeState(null, now), { busyUntil: 0, pausedUntil: 0 });
  assert.deepEqual(C.normalizeState({ busyUntil: 5, pausedUntil: "soon" }, now), { busyUntil: 0, pausedUntil: 0 });
  assert.equal(C.normalizeState({ pausedUntil: "2099-01-01T00:00:00Z" }, now).pausedUntil, now + 2 * 24 * 60 * MIN);
});

test("nudge delivery reuses the reminders ladder: claim, speak here, quiet, hidden", () => {
  assert.deepEqual(C.nudgePlan({ claimed: false, speakHere: true, gesture: true }), { notice: false, speak: false, chime: false, notify: false });
  assert.deepEqual(C.nudgePlan({ claimed: true, speakHere: true, gesture: true }), { notice: true, speak: true, chime: true, notify: false });
  // a LAN device without speak-here: notice only
  assert.deepEqual(C.nudgePlan({ claimed: true, speakHere: false, gesture: true }), { notice: true, speak: false, chime: false, notify: false });
  // quiet hours mute a check-in (not an explicit kind)
  assert.equal(C.nudgePlan({ claimed: true, speakHere: true, gesture: true, quiet: true }).speak, false);
  // no gesture yet: never queued for later
  assert.equal(C.nudgePlan({ claimed: true, speakHere: true, gesture: false }).speak, false);
  assert.equal(C.nudgePlan({ claimed: true, speakHere: true, gesture: true, hidden: true, notifyGranted: true }).notify, true);
});

test("endOfDay is the next local midnight", () => {
  const now = new Date(2026, 10, 9, 15, 30).getTime();
  assert.equal(C.endOfDay(now), new Date(2026, 10, 10, 0, 0).getTime());
});

test("strings: both modules have the same keys in DE and EN and never say 'Pilot'", () => {
  for (const mod of [C, I]) {
    assert.deepEqual(Object.keys(mod.STRINGS.de).sort(), Object.keys(mod.STRINGS.en).sort());
    for (const l of ["de", "en"]) {
      for (const [k, v] of Object.entries(mod.STRINGS[l])) assert.doesNotMatch(v, /pilot/i, l + "." + k);
    }
  }
});

// ------------------------------------------------------------------ integrations helpers

test("maskSecret hides credentials; long ones keep a short tail", () => {
  assert.equal(I.maskSecret(""), "");
  assert.equal(I.maskSecret(null), "");
  assert.equal(I.maskSecret("short-secret"), "••••••••"); // 12 chars, keep=0
  assert.equal(I.maskSecret("short", 4), "••••••••"); // too short for a tail
  assert.equal(I.maskSecret("1234567890-abc.apps.googleusercontent.com", 4), "••••••••.com");
  assert.equal(I.maskSecret("GOCSPX-abcdefghijklmnop", 9), "••••••••mnop"); // keep capped at 4
  assert.doesNotMatch(I.maskSecret("GOCSPX-abcdefghijklmnop", 4), /GOCSPX/);
});

test("credentialState: env beats stored; set / unset", () => {
  assert.equal(I.credentialState(true, true), "env");
  assert.equal(I.credentialState(false, true), "env");
  assert.equal(I.credentialState(true, false), "set");
  assert.equal(I.credentialState(false, false), "unset");
});

test("normalizeIntegrations keeps booleans only (no secrets ever)", () => {
  const n = I.normalizeIntegrations({
    ha: { url: "http://192.168.1.20:8123", tokenSet: true, token: "ha-secret" },
    google: { clientIdSet: true, clientSecretSet: "yes", fromEnv: false, clientIdFromEnv: false, clientSecretFromEnv: false, clientSecret: "x" },
    lock: { pinSet: true, enabled: true, pin: "1234", pinHash: "pbkdf2-sha256$1$a$b" },
    admin: true,
  });
  assert.deepEqual(n, {
    available: true,
    admin: true,
    google: { clientIdSet: true, clientSecretSet: false, fromEnv: false, clientIdFromEnv: false, clientSecretFromEnv: false },
    lock: { pinSet: true, enabled: true },
  });
  assert.doesNotMatch(JSON.stringify(n), /secret"|1234|pbkdf2|ha-secret|192\.168/);
  assert.equal(I.normalizeIntegrations(null).available, false);
  assert.equal(I.normalizeIntegrations(null).admin, false);
  assert.equal(I.normalizeIntegrations([]).available, false);
  // a LAN device: viewing only
  assert.equal(I.normalizeIntegrations({ admin: false }).admin, false);
});

test("normalizeIntegrations: each env variable fixes only its own field", () => {
  const g = (o) => I.normalizeIntegrations({ google: Object.assign({ clientIdSet: true, clientSecretSet: true }, o), admin: true }).google;
  assert.deepEqual(g({ fromEnv: true, clientIdFromEnv: true, clientSecretFromEnv: false }), {
    clientIdSet: true,
    clientSecretSet: true,
    fromEnv: true,
    clientIdFromEnv: true,
    clientSecretFromEnv: false,
  });
  assert.equal(I.credentialState(true, g({ fromEnv: true, clientIdFromEnv: true, clientSecretFromEnv: false }).clientSecretFromEnv), "set");
  // an older server sends only fromEnv: both fields count as env
  const old = g({ fromEnv: true });
  assert.equal(old.clientIdFromEnv && old.clientSecretFromEnv, true);
});

test("calendar status and state", () => {
  assert.equal(I.calendarState(null), "unavailable");
  const st = (o) => I.normalizeCalendarStatus(Object.assign({ configured: true, connected: false, needsRelink: false, owner: "", ownerName: "", error: "" }, o));
  assert.equal(I.calendarState(st({ configured: false })), "no_client");
  assert.equal(I.calendarState(st({})), "disconnected");
  assert.equal(I.calendarState(st({ connected: true })), "connected");
  assert.equal(I.calendarState(st({ connected: true, needsRelink: true })), "relink");
  assert.equal(I.calendarState(st({ error: "token" })), "error");
  const n = st({ owner: "p_ab12cd34ef56", ownerName: "Anna\u0007" });
  assert.equal(n.owner, "p_ab12cd34ef56");
  assert.equal(n.ownerName, "Anna");
  assert.equal(st({ owner: "../etc" }).owner, "");
});

test("parseClientSecretJson: Desktop client ok, web client and junk refused", () => {
  const ok = JSON.stringify({ installed: { client_id: "123-abc.apps.googleusercontent.com", client_secret: "GOCSPX-xyz", redirect_uris: ["http://localhost"] } });
  assert.deepEqual(I.parseClientSecretJson(ok), { ok: true, clientId: "123-abc.apps.googleusercontent.com", clientSecret: "GOCSPX-xyz" });
  assert.equal(I.parseClientSecretJson(JSON.stringify({ web: { client_id: "a", client_secret: "b" } })).key, "errJsonWeb");
  assert.equal(I.parseClientSecretJson("{not json").key, "errJson");
  assert.equal(I.parseClientSecretJson("[]").key, "errJson");
  assert.equal(I.parseClientSecretJson(JSON.stringify({ installed: { client_id: "a b", client_secret: "x" } })).key, "errJson");
  assert.equal(I.parseClientSecretJson("").key, "errJson");
  assert.equal(I.parseClientSecretJson(" ".repeat(I.JSON_MAX + 1)).key, "errJsonLarge");
});

test("googleBody sends only typed fields and validates them", () => {
  assert.deepEqual(I.googleBody(" id.apps ", ""), { ok: true, body: { clientId: "id.apps" } });
  assert.deepEqual(I.googleBody("", "sec"), { ok: true, body: { clientSecret: "sec" } });
  assert.deepEqual(I.googleBody("a", "b"), { ok: true, body: { clientId: "a", clientSecret: "b" } });
  assert.equal(I.googleBody("", " ").key, "clientNothing");
  assert.equal(I.googleBody("a b", "").key, "errIdFormat");
  assert.equal(I.googleBody("", "x".repeat(513)).key, "errSecretFormat");
  assert.deepEqual(I.googleBody("", "x".repeat(512)).ok, true);
  // the server takes visible ASCII only (it goes into a form body / header)
  assert.equal(I.googleBody("idé", "").key, "errIdFormat");
  assert.equal(I.googleBody("", "a\u0000b").key, "errSecretFormat");
});

test("PIN: 4–12 digits; set, change and remove requests", () => {
  assert.equal(I.validatePin("1234"), true);
  assert.equal(I.validatePin("123456789012"), true);
  assert.equal(I.validatePin("123"), false);
  assert.equal(I.validatePin("1234567890123"), false);
  assert.equal(I.validatePin("12a4"), false);
  assert.equal(I.validatePin(1234), false);

  assert.deepEqual(I.pinRequest("set", { pin: "1234", confirm: "1234" }), { ok: true, body: { pin: "1234" } });
  assert.equal(I.pinRequest("set", { pin: "12", confirm: "12" }).key, "errPinFormat");
  assert.equal(I.pinRequest("set", { pin: "1234", confirm: "1235" }).key, "errPinMismatch");
  assert.deepEqual(I.pinRequest("change", { oldPin: "1111", pin: "2222", confirm: "2222" }), { ok: true, body: { pin: "2222", oldPin: "1111" } });
  assert.equal(I.pinRequest("change", { pin: "2222", confirm: "2222" }).key, "errPinOld");
  assert.equal(I.pinRequest("change", { oldPin: "1111", pin: "1111", confirm: "1111" }).key, "errPinSame");
  assert.deepEqual(I.pinRequest("remove", { oldPin: "1111" }), { ok: true, body: { pin: "", oldPin: "1111" } });
  assert.equal(I.pinRequest("remove", {}).field, "old");
});

test("safeAuthUrl only allows Google's sign-in host", () => {
  assert.equal(I.safeAuthUrl("https://accounts.google.com/o/oauth2/v2/auth?client_id=x&code_challenge=y"), true);
  assert.equal(I.safeAuthUrl("https://accounts.google.com.evil.example/"), false);
  assert.equal(I.safeAuthUrl("http://accounts.google.com/o/oauth2"), false);
  assert.equal(I.safeAuthUrl("javascript:alert(1)"), false);
  assert.equal(I.safeAuthUrl("https://accounts.google.com/x@evil.example/"), false);
  assert.equal(I.safeAuthUrl("https://accounts.google.com/a\\b"), false);
  assert.equal(I.safeAuthUrl(null), false);
});

test("ownerOptions: household first, valid people, unknown owner kept", () => {
  const tr = I.makeTr(I.STRINGS, "de");
  const opts = I.ownerOptions(
    [
      { id: "p_aaaaaaaaaaaa", name: "Anna" },
      { id: "bad id", name: "Evil" },
      { id: "p_bbbbbbbbbbbb", name: "" },
      { id: "p_aaaaaaaaaaaa", name: "Dup" },
    ],
    "p_cccccccccccc",
    tr
  );
  assert.deepEqual(opts, [
    { value: "", label: "Haushalt / nur ich" },
    { value: "p_aaaaaaaaaaaa", label: "Anna" },
    { value: "p_cccccccccccc", label: "Unbekannte Person" },
  ]);
  assert.equal(I.ownerOptions(null, "", I.makeTr(I.STRINGS, "en"))[0].label, "Household / just me");
});

test("calendarReturn reads and strips the OAuth result from the URL", () => {
  assert.deepEqual(I.calendarReturn("?calendar=connected"), { kind: "connected", reason: "", search: "" });
  assert.deepEqual(I.calendarReturn("?token=abc&calendar=error&reason=access_denied"), { kind: "error", reason: "access_denied", search: "?token=abc" });
  assert.equal(I.calendarReturn("?calendar=error&reason=%3Cscript%3E").reason, "unknown");
  assert.equal(I.calendarReturn("?calendar=error").reason, "unknown");
  assert.deepEqual(I.calendarReturn("?lang=de"), { kind: "", reason: "", search: "?lang=de" });
  assert.equal(I.calendarReturn("?calendar=bogus").kind, "");
  assert.equal(I.calendarReturn("?calendar=%E0%A4%A").kind, "");
});

test("errorKey maps server codes to texts", () => {
  assert.equal(I.errorKey({ status: 403, code: "local_only" }), "errLocalOnly");
  assert.equal(I.errorKey({ status: 403, code: "locked" }), "errLocked");
  assert.equal(I.errorKey({ status: 503, code: "no_client" }), "errNoClient");
  assert.equal(I.errorKey({ status: 429 }), "errTooMany");
  assert.equal(I.errorKey({ status: 404 }), "errUnavailable");
  assert.equal(I.errorKey({ status: 503, message: "data file was written by a newer HERMES" }), "errReadOnly");
  // saveSettings errors carry no code, only the server text
  assert.equal(I.errorKey({ status: 403, message: "only from this machine (http://localhost:8080)" }), "errLocalOnly");
  assert.equal(I.errorKey({ status: 403, message: "unlock with the PIN first" }), "errLocked");
  assert.equal(I.errorKey({ status: 500 }), "errGeneric");
  assert.equal(I.errorKey(null), "errGeneric");
  // the secrets.go codes
  const codes = {
    bad_pin: "errPinFormat",
    old_pin_required: "errPinOld",
    wrong_pin: "errPinWrong",
    lockout: "errTooMany",
    bad_client: "errBadClient",
    bad_json: "errJson",
    not_desktop: "errJsonWeb",
    too_large: "errTooLarge",
    conflict: "errConflict",
    read_only: "errReadOnly",
  };
  for (const [code, key] of Object.entries(codes)) {
    assert.equal(I.errorKey({ status: 400, code }), key, code);
    assert.ok(I.STRINGS.de[key] && I.STRINGS.en[key], key);
  }
  // an unknown code with a known status still maps
  assert.equal(I.errorKey({ status: 429, code: "whatever" }), "errTooMany");
});

test("formatUntil: time today, weekday + time otherwise, DE and EN", () => {
  const now = Date.parse("2026-11-09T09:00:00Z"); // Monday
  assert.equal(I.formatUntil(Date.parse("2026-11-09T17:00:00Z"), now, "de", "Europe/Berlin"), "18:00");
  assert.equal(I.formatUntil(Date.parse("2026-11-09T17:00:00Z"), now, "en", "Europe/Berlin"), "18:00");
  assert.match(I.formatUntil(Date.parse("2026-11-10T06:00:00Z"), now, "de", "Europe/Berlin"), /^Di\.? 07:00$/);
  assert.match(I.formatUntil(Date.parse("2026-11-10T06:00:00Z"), now, "en", "Europe/Berlin"), /^Tue 07:00$/);
  assert.equal(I.formatUntil(0, now, "de", ""), "");
  // an invalid zone falls back to local time instead of throwing
  assert.match(I.formatUntil(now + 60 * MIN, now, "en", "Not/AZone"), /\d\d:\d\d/);
});

// ------------------------------------------------------------------ browser runtime (stub window)

const STATIC = path.join(__dirname, "..", "static");

/* A fake IdleDetector the test drives. */
function makeIdleDetector(perm) {
  const made = [];
  function IdleDetector() {
    this.userState = "active";
    this.screenState = "unlocked";
    this._l = [];
    made.push(this);
  }
  IdleDetector.prototype.addEventListener = function (t, fn) {
    if (t === "change") this._l.push(fn);
  };
  IdleDetector.prototype.start = function () {
    return Promise.resolve();
  };
  IdleDetector.prototype.set = function (u, s) {
    this.userState = u;
    this.screenState = s || "unlocked";
    this._l.forEach((fn) => fn());
  };
  IdleDetector.requestPermission = () => Promise.resolve(perm.value);
  IdleDetector.made = made;
  return IdleDetector;
}

function loadBrowser(opts) {
  opts = opts || {};
  const calls = [];
  const storage = new Map(Object.entries(opts.storage || {}));
  const perm = { value: opts.permission || "granted" };
  const win = {
    console,
    setTimeout,
    clearTimeout,
    setInterval: () => 1, // the test drives time itself
    clearInterval: () => {},
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
    AbortController,
    isSecureContext: opts.secure !== false,
    location: { host: opts.host || "localhost:8080" },
    navigator: {
      userAgent: "node",
      permissions: { query: () => Promise.resolve({ state: perm.value === "granted" ? "granted" : "prompt" }) },
    },
    localStorage: {
      getItem: (k) => (storage.has(k) ? storage.get(k) : null),
      setItem: (k, v) => storage.set(k, String(v)),
      removeItem: (k) => storage.delete(k),
    },
    document: { visibilityState: "visible", documentElement: { lang: "de" } },
  };
  if (opts.idle !== false) win.IdleDetector = makeIdleDetector(perm);
  win.window = win;
  vm.createContext(win);
  for (const f of ["hermes.js", "reminders.js", "checkins.js"]) {
    vm.runInContext(fs.readFileSync(path.join(STATIC, f), "utf8"), win, { filename: f });
  }
  const notices = [];
  const routes = opts.routes || {};
  let speaking = false;
  const sets = Object.assign({ checkinSitting: true, checkinSittingMin: 60, quietStart: "00:00", quietEnd: "00:00", timezone: "UTC" }, opts.settings || {});
  win.Hermes.api = {
    lang: () => opts.lang || "de",
    settings: () => Object.assign({}, sets),
    gestureSeen: () => true,
    isLocalhost: () => true,
    isSpeaking: () => speaking,
    chime: () => {
      calls.push(["chime"]);
      return Promise.resolve(true);
    },
    say: (text, o) => {
      calls.push(["say", text, !!(o && o.force)]);
      return Promise.resolve();
    },
    flashEye: () => {},
    toast: (msg) => calls.push(["toast", msg]),
    addNotice: (text, o) => {
      const n = { text, kind: o.kind, actions: o.actions || [], ephemeral: !!o.ephemeral };
      notices.push(n);
      return { update: (t, a) => Array.isArray(a) && (n.actions = a), remove: () => {} };
    },
    fetchJSON: (url, o) => {
      const method = (o && o.method) || "GET";
      calls.push(["fetch", method, url]);
      const h = routes[method + " " + url];
      if (!h) return Promise.reject(Object.assign(new Error("not found"), { status: 404 }));
      return Promise.resolve(h());
    },
  };
  win.Hermes.emit("ready");
  return {
    win,
    calls,
    notices,
    storage,
    sets,
    H: win.Hermes,
    ui: win.HermesCheckins.ui,
    setSpeaking: (v) => (speaking = v),
  };
}

const settle = () => new Promise((r) => setTimeout(r, 20));
const says = (b) => b.calls.filter((c) => c[0] === "say");

test("browser: with the permission granted the detector starts on ready and nudges in German", async () => {
  const b = loadBrowser({ lang: "de" });
  await settle();
  assert.equal(b.ui.state().running, true);
  const now = Date.now();
  b.ui._observe("active", "unlocked", now);
  b.ui._tick(now + 59 * MIN);
  await settle();
  assert.equal(says(b).length, 0);
  b.ui._tick(now + 60 * MIN);
  await settle();
  assert.deepEqual(says(b), [["say", "Du sitzt seit 60 Minuten. Zeit, kurz aufzustehen und dich zu strecken.", true]]);
  assert.equal(b.calls.findIndex((c) => c[0] === "chime") < b.calls.findIndex((c) => c[0] === "say"), true);
  assert.equal(b.notices.length, 1);
  assert.equal(b.notices[0].kind, "checkin");
  assert.equal(b.notices[0].ephemeral, true);
  assert.deepEqual(
    Array.from(b.notices[0].actions, (a) => a.label),
    ["Später", "Heute nicht mehr"]
  );
});

test("browser: an English voice gets English nudges", async () => {
  const b = loadBrowser({ lang: "en" });
  await settle();
  const now = Date.now();
  b.ui._observe("active", "unlocked", now);
  b.ui._tick(now + 60 * MIN);
  await settle();
  assert.deepEqual(says(b), [["say", "You have been sitting for 60 minutes. Time to stand up and stretch.", true]]);
  assert.deepEqual(
    Array.from(b.notices[0].actions, (a) => a.label),
    ["Later", "Not today"]
  );
});

test("browser: a calendar event in progress (from /api/checkins/state) suppresses the nudge", async () => {
  const busy = new Date(Date.now() + 2 * 60 * MIN).toISOString();
  const b = loadBrowser({ routes: { "GET /api/checkins/state": () => ({ busyUntil: busy, pausedUntil: "" }) } });
  await settle();
  assert.ok(b.calls.some((c) => c[2] === "/api/checkins/state"));
  const now = Date.now();
  b.ui._observe("active", "unlocked", now);
  b.ui._tick(now + 61 * MIN);
  await settle();
  assert.equal(says(b).length, 0);
  assert.equal(b.notices.length, 0);
});

test("browser: paused by voice and while HERMES is speaking → no nudge", async () => {
  const paused = new Date(Date.now() + 3 * 60 * MIN).toISOString();
  const b = loadBrowser({ routes: { "GET /api/checkins/state": () => ({ busyUntil: "", pausedUntil: paused }) } });
  await settle();
  const now = Date.now();
  b.ui._observe("active", "unlocked", now);
  b.ui._tick(now + 90 * MIN);
  await settle();
  assert.equal(says(b).length, 0);

  const c = loadBrowser();
  await settle();
  c.setSpeaking(true);
  c.ui._observe("active", "unlocked", now);
  c.ui._tick(now + 61 * MIN);
  await settle();
  assert.equal(says(c).length, 0);
  c.setSpeaking(false);
  c.ui._tick(now + 62 * MIN);
  await settle();
  assert.equal(says(c).length, 1);
});

test("browser: a meeting check-in delivered by reminders.js holds the sitting nudge", async () => {
  const b = loadBrowser();
  await settle();
  const now = Date.now();
  // sitting for 70 minutes already (the detector started a streak at load)
  b.ui._run.st = C.sitObserve(C.sitInit(), active, now - 70 * MIN);
  b.H.emit("hub:reminder.due", {
    id: 7,
    type: "reminder.due",
    data: { reminderId: "r_meeting00001", kind: "meeting", text: "Team-Call", due: new Date(now).toISOString(), speak: "In 10 Minuten: Team-Call.", lang: "de", repeat: 0, chime: true },
  });
  await settle();
  // reminders.js spoke the meeting once; the sitting nudge waits
  assert.deepEqual(says(b), [["say", "In 10 Minuten: Team-Call.", true]]);
  b.ui._tick(now + MIN);
  await settle();
  assert.equal(says(b).length, 1);
  b.ui._tick(now + 16 * MIN);
  await settle();
  assert.equal(says(b).length, 2);
});

test("browser: 'Heute nicht mehr' pauses this device until midnight; resume clears it", async () => {
  const b = loadBrowser();
  await settle();
  const now = Date.now();
  b.ui._observe("active", "unlocked", now);
  b.ui._tick(now + 60 * MIN);
  await settle();
  b.notices[0].actions[1].fn();
  assert.ok(Number(b.storage.get("hermes-checkins-sit-off")) > now);
  b.ui._tick(now + 125 * MIN);
  await settle();
  // still paused unless the day is over
  const expected = C.endOfDay(Date.now()) > now + 125 * MIN ? 1 : 2;
  assert.equal(says(b).length, expected);
  b.ui.resume();
  assert.equal(b.ui.state().localPausedUntil, 0);
});

test("browser: without permission the detector does not start; enable() asks and starts", async () => {
  const b = loadBrowser({ permission: "prompt" });
  await settle();
  assert.equal(b.ui.state().running, false);
  assert.equal(b.ui.support(), "ok");
  b.win.IdleDetector.requestPermission = () => Promise.resolve("granted");
  assert.equal(await b.ui.enable(), "granted");
  assert.equal(b.ui.state().running, true);

  const d = loadBrowser({ permission: "denied" });
  await settle();
  assert.equal(await d.ui.enable(), "denied");
  assert.equal(d.ui.state().running, false);
  assert.equal(d.ui.state().permission, "denied");
});

test("browser: unsupported browser or insecure context reports so and never starts", async () => {
  const b = loadBrowser({ idle: false });
  await settle();
  assert.equal(b.ui.support(), "unsupported");
  assert.equal(await b.ui.enable(), "unsupported");
  const c = loadBrowser({ secure: false });
  await settle();
  assert.equal(c.ui.support(), "insecure");
  assert.equal(c.ui.state().running, false);
});

test("browser: switching the setting off stops the detector", async () => {
  const b = loadBrowser();
  await settle();
  assert.equal(b.ui.state().running, true);
  b.sets.checkinSitting = false;
  b.H.emit("settings", {});
  await settle();
  assert.equal(b.ui.state().running, false);
  const now = Date.now();
  b.ui._observe("active", "unlocked", now);
  b.ui._tick(now + 120 * MIN);
  await settle();
  assert.equal(says(b).length, 0);
});

test("browser: a LAN device (speak here off) shows a silent notice only", async () => {
  const b = loadBrowser({ storage: { "hermes-speak-reminders": "false" } });
  await settle();
  const now = Date.now();
  b.ui._observe("active", "unlocked", now);
  b.ui._tick(now + 60 * MIN);
  await settle();
  assert.equal(says(b).length, 0);
  assert.equal(b.notices.length, 1);
});

test("browser: the detector's change events drive the streak (lock resets)", async () => {
  const b = loadBrowser();
  await settle();
  const det = b.win.IdleDetector.made[0];
  det.set("active");
  assert.equal(b.ui.state().active, true);
  det.set("idle", "locked");
  assert.equal(b.ui._run.st.streakStart, 0);
});

test("browser: an English meeting check-in is spoken by reminders.js in English and holds the nudge", async () => {
  const b = loadBrowser({ lang: "en" });
  await settle();
  const now = Date.now();
  b.ui._run.st = C.sitObserve(C.sitInit(), active, now - 70 * MIN);
  b.H.emit("hub:reminder.due", {
    id: 8,
    type: "reminder.due",
    data: { reminderId: "r_meeting00002", kind: "meeting", text: "Team call", due: new Date(now).toISOString(), speak: "In 10 minutes: Team call.", lang: "en", repeat: 0, chime: true },
  });
  await settle();
  assert.deepEqual(says(b), [["say", "In 10 minutes: Team call.", true]]);
  assert.equal(b.notices[0].kind, "meeting");
  b.ui._tick(now + 5 * MIN);
  await settle();
  assert.equal(says(b).length, 1);
  b.ui._tick(now + 16 * MIN);
  await settle();
  assert.equal(says(b)[1][1], "You have been sitting for 86 minutes. Time to stand up and stretch.");
});

test("browser: quiet hours (settings time zone) suppress the nudge", async () => {
  const d = new Date();
  const hh = (h) => String((h + 24) % 24).padStart(2, "0") + ":00";
  // quiet from the last full UTC hour to two hours later: now is inside
  const b = loadBrowser({ settings: { quietStart: hh(d.getUTCHours()), quietEnd: hh(d.getUTCHours() + 2), timezone: "UTC" } });
  await settle();
  const now = Date.now();
  b.ui._observe("active", "unlocked", now);
  b.ui._tick(now + 60 * MIN);
  await settle();
  assert.equal(says(b).length, 0);
  assert.equal(b.notices.length, 0);
});

test("browser: 'Später' snoozes the nudge for 15 minutes", async () => {
  const b = loadBrowser({ lang: "en" });
  await settle();
  const now = Date.now();
  b.ui._observe("active", "unlocked", now);
  b.ui._tick(now + 60 * MIN);
  await settle();
  assert.equal(says(b).length, 1);
  b.notices[0].actions[0].fn();
  assert.ok(b.calls.some((c) => c[0] === "toast" && /15 minutes/.test(c[1])));
  // the streak restarted at the nudge; even a due streak waits for the snooze
  b.ui._run.st = C.sitObserve(C.sitInit(), active, now - 90 * MIN);
  b.ui._tick(Date.now() + 10 * MIN);
  await settle();
  assert.equal(says(b).length, 1);
  b.ui._tick(Date.now() + 16 * MIN);
  await settle();
  assert.equal(says(b).length, 2);
});

test("browser: the heartbeat treats a long silent gap (sleep) as a break", async () => {
  const b = loadBrowser();
  await settle();
  const now = Date.now();
  // active for 2 hours, but nothing was seen for the last 20 minutes
  const st = C.sitObserve(C.sitInit(), active, now - 120 * MIN);
  st.lastAt = now - 20 * MIN;
  b.ui._run.st = st;
  b.ui._heartbeat();
  await settle();
  assert.equal(says(b).length, 0);
  assert.ok(b.ui._run.st.streakStart >= now);
  // a regular heartbeat (no gap) on a due streak nudges
  const due = C.sitObserve(C.sitInit(), active, Date.now() - 61 * MIN);
  due.lastAt = Date.now() - 30 * 1000;
  b.ui._run.st = due;
  b.ui._heartbeat();
  await settle();
  assert.equal(says(b).length, 1);
});

// ------------------------------------------------------------------ integration pass

test("calendar callback reasons map to DE and EN texts", () => {
  for (const r of ["denied", "state", "exchange", "scope", "refresh", "store", "owner"]) {
    const k = I.reasonKey(r);
    assert.equal(k, "reason_" + r);
    assert.ok(I.STRINGS.en[k] && I.STRINGS.de[k] && I.STRINGS.en[k] !== I.STRINGS.de[k], r);
  }
  assert.equal(I.reasonKey("<script>"), "reason_unknown");
  assert.equal(I.reasonKey(""), "reason_unknown");
});

test("calendarAdded formats the added event in DE and EN", () => {
  const card = {
    kind: "calendar",
    data: { added: true, events: [{ id: "e1", title: "Zahnarzt", start: "2026-11-10T15:00:00+01:00", end: "2026-11-10T16:00:00+01:00", allDay: false }] },
  };
  assert.deepEqual(I.calendarAdded(card, "de"), { title: "Zahnarzt", when: "Di., 10.11., 15:00" });
  assert.deepEqual(I.calendarAdded(card, "en"), { title: "Zahnarzt", when: "Tue Nov 10, 15:00" });
  const allDay = { kind: "calendar", data: { added: true, events: [{ title: "Urlaub", start: "2026-11-12T00:00:00+01:00", allDay: true }] } };
  assert.equal(I.calendarAdded(allDay, "de").when, "Do., 12.11. (ganztägig)");
  // queries and junk are not adds
  assert.equal(I.calendarAdded({ kind: "calendar", data: { events: card.data.events } }, "de"), null);
  assert.equal(I.calendarAdded({ kind: "list", data: card.data }, "de"), null);
  assert.equal(I.calendarAdded({ kind: "calendar", data: { added: true, events: [{ title: "x", start: 5 }] } }, "en"), null);
  assert.equal(I.calendarAdded(null, "en"), null);
});

// ------------------------------------------------------------------ smart home settings (helpers)

test("normalizeHaUrl accepts http(s)://host[:port] only, like secrets.go", () => {
  assert.equal(I.normalizeHaUrl("http://192.168.1.20:8123"), "http://192.168.1.20:8123");
  assert.equal(I.normalizeHaUrl("  HTTP://HomeAssistant.Local:8123/ "), "http://homeassistant.local:8123");
  assert.equal(I.normalizeHaUrl("https://ha.example.org"), "https://ha.example.org");
  assert.equal(I.normalizeHaUrl("http://[fd00::12]:8123"), "http://[fd00::12]:8123");
  for (const bad of [
    "",
    "192.168.1.20:8123", // no scheme
    "ftp://ha:21",
    "http://ha:8123/api", // path
    "http://ha:8123?x=1",
    "http://ha:8123#frag",
    "http://user:pw@ha:8123", // credentials
    "http://ha:0",
    "http://ha:65536",
    "http://ha:08123", // leading zero
    "http://ha:",
    "http://ha:abc",
    "http://h a",
    "http://ha%2f:8123",
    "javascript:alert(1)",
    "http://" + "a".repeat(600),
    null,
    42,
  ]) {
    assert.equal(I.normalizeHaUrl(bad), "", String(bad));
  }
});

test("normalizeHA reads only the masked view (no token ever) and the env flags", () => {
  const h = I.normalizeHA({ ha: { url: "http://192.168.1.20:8123", urlValid: true, tokenSet: true, token: "eyJ-secret", fromEnv: false, urlFromEnv: false, tokenFromEnv: false } });
  assert.deepEqual(h, { url: "http://192.168.1.20:8123", urlValid: true, tokenSet: true, fromEnv: false, urlFromEnv: false, tokenFromEnv: false });
  assert.doesNotMatch(JSON.stringify(h), /eyJ/);
  // an older server sends only fromEnv
  const old = I.normalizeHA({ ha: { url: "http://ha:8123", tokenSet: true, fromEnv: true } });
  assert.equal(old.urlFromEnv, true);
  assert.equal(old.tokenFromEnv, true);
  // only the token from the environment
  const tok = I.normalizeHA({ ha: { url: "", tokenSet: true, fromEnv: true, urlFromEnv: false, tokenFromEnv: true } });
  assert.equal(tok.urlFromEnv, false);
  assert.equal(tok.tokenFromEnv, true);
  // an unusable env address is not put into the form and is flagged
  const bad = I.normalizeHA({ ha: { url: "http://ha:8123/x", urlValid: false, tokenSet: false, fromEnv: true, urlFromEnv: true } });
  assert.equal(bad.url, "");
  assert.equal(bad.urlValid, false);
  assert.deepEqual(I.normalizeHA(null), { url: "", urlValid: true, tokenSet: false, fromEnv: false, urlFromEnv: false, tokenFromEnv: false });
  assert.equal(I.normalizeHA({ ha: { tokenSet: "yes" } }).tokenSet, false);
  // the old normalizeIntegrations still never carries the HA part
  assert.equal(I.normalizeIntegrations({ ha: { url: "http://ha:8123" }, admin: true }).ha, undefined);
});

test("haRequest: address and token are bound; a new address without a token clears it", () => {
  const none = I.normalizeHA(null);
  const stored = I.normalizeHA({ ha: { url: "http://192.168.1.20:8123", tokenSet: true } });
  // first setup: both
  assert.deepEqual(I.haRequest({ url: "http://192.168.1.20:8123/", token: " tok-123 " }, none), {
    ok: true,
    action: "save",
    body: { url: "http://192.168.1.20:8123", token: "tok-123" },
    clearsToken: false,
  });
  // address only on a fresh setup: no token to lose
  assert.equal(I.haRequest({ url: "http://ha:8123", token: "" }, none).clearsToken, false);
  // a token without any address
  assert.deepEqual(I.haRequest({ url: "", token: "tok" }, none), { ok: false, key: "errHaUrlFirst", field: "url" });
  // unchanged address + new token → token only (bound by the server to the stored address)
  assert.deepEqual(I.haRequest({ url: "http://192.168.1.20:8123", token: "new" }, stored).body, { token: "new" });
  // changed address, no token → clearsToken (the UI asks)
  const moved = I.haRequest({ url: "http://192.168.1.30:8123", token: "" }, stored);
  assert.equal(moved.ok, true);
  assert.deepEqual(moved.body, { url: "http://192.168.1.30:8123" });
  assert.equal(moved.clearsToken, true);
  // changed address with a token → both, nothing lost
  const both = I.haRequest({ url: "http://192.168.1.30:8123", token: "t2" }, stored);
  assert.deepEqual(both.body, { url: "http://192.168.1.30:8123", token: "t2" });
  assert.equal(both.clearsToken, false);
  // nothing changed
  assert.equal(I.haRequest({ url: "http://192.168.1.20:8123", token: "" }, stored).key, "haNothing");
  // emptied address → clear both
  assert.deepEqual(I.haRequest({ url: "  ", token: "" }, stored), { ok: true, action: "clear", body: { url: "" }, clearsToken: true });
  assert.equal(I.haRequest({ url: "", token: "x" }, stored).key, "errHaUrlFirst");
  // validation
  assert.deepEqual(I.haRequest({ url: "http://ha:8123/api", token: "" }, none), { ok: false, key: "errHaUrl", field: "url" });
  assert.equal(I.haRequest({ url: "http://ha:8123", token: "has space" }, none).key, "errHaToken");
  assert.equal(I.haRequest({ url: "http://ha:8123", token: "x".repeat(2049) }, none).key, "errHaToken");
  assert.equal(I.haRequest({ url: "http://ha:8123", token: "x".repeat(2048) }, none).ok, true);
});

test("haRequest: values from the environment are never sent", () => {
  const envUrl = I.normalizeHA({ ha: { url: "http://ha:8123", tokenSet: false, fromEnv: true, urlFromEnv: true, tokenFromEnv: false } });
  // the address field is ignored (and disabled in the UI); the token binds to the env address
  assert.deepEqual(I.haRequest({ url: "http://other:8123", token: "t" }, envUrl).body, { token: "t" });
  assert.equal(I.haRequest({ url: "", token: "" }, envUrl).key, "haNothing");
  const envTok = I.normalizeHA({ ha: { url: "http://ha:8123", tokenSet: true, fromEnv: true, urlFromEnv: false, tokenFromEnv: true } });
  const r = I.haRequest({ url: "http://ha2:8123", token: "typed" }, envTok);
  assert.deepEqual(r.body, { url: "http://ha2:8123" });
  assert.equal(r.clearsToken, false); // the env token is not stored, so nothing is lost
  const envBoth = I.normalizeHA({ ha: { url: "http://ha:8123", tokenSet: true, fromEnv: true } });
  assert.equal(I.haRequest({ url: "http://x:1", token: "t" }, envBoth).ok, false);
});

test("normalizeHomeStatus reads the SPEC and the batch-3 shapes; only generic codes", () => {
  // SPEC 3.8
  assert.deepEqual(I.normalizeHomeStatus({ configured: true, ok: true, version: "2026.11.1", code: "ok", entities: 42 }), {
    configured: true,
    connected: true,
    version: "2026.11.1",
    entities: 42,
    code: "",
    error: "",
  });
  assert.equal(I.normalizeHomeStatus({ configured: true, ok: false, code: "auth" }).code, "auth");
  // batch-3 contract
  const c = I.normalizeHomeStatus({ configured: true, connected: false, error: "unreachable" });
  assert.equal(c.code, "unreachable");
  assert.equal(c.error, "");
  assert.equal(I.normalizeHomeStatus({ configured: true, connected: true, entities: 3 }).connected, true);
  // other error text: cleaned and capped, never markup-special in effect (textContent only)
  const e = I.normalizeHomeStatus({ configured: true, connected: false, error: "weird\u0000 " + "x".repeat(400) });
  assert.equal(e.code, "");
  assert.ok(Array.from(e.error).length <= 160);
  assert.doesNotMatch(e.error, /\u0000/);
  // junk
  assert.equal(I.normalizeHomeStatus(null), null);
  assert.equal(I.normalizeHomeStatus([]), null);
  const j = I.normalizeHomeStatus({ configured: "yes", ok: "true", version: "<b>1</b>", entities: -3, code: "rm -rf" });
  assert.deepEqual(j, { configured: false, connected: false, version: "", entities: -1, code: "", error: "" });
  assert.equal(I.normalizeHomeStatus({ ok: true, entities: 1e9 }).entities, 100000);
});

test("homeState / homeStateText cover every case with DE and EN texts", () => {
  const ha = (url, tokenSet) => I.normalizeHA({ ha: { url, tokenSet } });
  const st = (o) => I.normalizeHomeStatus(o);
  assert.equal(I.homeState(null, null, true), "unavailable");
  assert.equal(I.homeState(null, null, false), "unavailable");
  assert.equal(I.homeState(null, ha("", false), false), "unconfigured");
  assert.equal(I.homeState(st({ configured: false }), ha("", false), false), "unconfigured");
  assert.equal(I.homeState(st({ configured: false }), ha("http://ha:8123", false), false), "no_token");
  assert.equal(I.homeState(st({ configured: true, ok: true }), ha("http://ha:8123", true), false), "connected");
  for (const code of ["unreachable", "auth", "timeout", "bad_response"]) {
    assert.equal(I.homeState(st({ configured: true, ok: false, code }), ha("http://ha:8123", true), false), code);
  }
  assert.equal(I.homeState(st({ configured: true, ok: false }), ha("http://ha:8123", true), false), "error");
  // the status endpoint failed, but HA is configured
  assert.equal(I.homeState(null, ha("http://ha:8123", true), false), "error");
  for (const s of ["connected", "unconfigured", "no_token", "unreachable", "auth", "timeout", "bad_response", "unavailable", "error"]) {
    const t = I.homeStateText(s, st({ configured: true, ok: s === "connected", version: "2026.11" }));
    assert.ok(I.STRINGS.de[t.key] && I.STRINGS.en[t.key], s);
  }
  assert.deepEqual(I.homeStateText("connected", st({ ok: true, version: "2026.11" })), { key: "haConnectedVersion", vars: { version: "2026.11" } });
  assert.deepEqual(I.homeStateText("error", st({ configured: true, error: "boom" })), { key: "haError", vars: { msg: "boom" } });
  assert.deepEqual(I.homeStateText("error", null), { key: "haErrorPlain" });
});

test("normalizeHomeEntities counts blocked devices, sorts and caps", () => {
  const n = I.normalizeHomeEntities({
    entities: [
      { id: "lock.front", name: "Haustür", area: "Flur", domain: "lock", state: "locked", blocked: true },
      { id: "light.kitchen", name: "Küche", area: "Küche", domain: "light", state: "on", blocked: false },
      { id: "light.a", name: "", area: "", domain: "light" }, // name falls back to the id
      null,
      "junk",
      { id: "", name: "" },
      { id: "x.y", name: "<img src=x onerror=alert(1)>", domain: "Bad Domain!" },
    ],
  });
  assert.equal(n.count, 4);
  assert.equal(n.blocked, 1);
  assert.deepEqual(
    n.items.map((i) => i.name),
    ["<img src=x onerror=alert(1)>", "Küche", "light.a", "Haustür"]
  );
  assert.equal(n.items[3].blocked, true);
  assert.equal(n.items[0].domain, ""); // invalid domain dropped
  assert.equal(I.normalizeHomeEntities(null), null);
  assert.equal(I.normalizeHomeEntities({ entities: "x" }), null);
  const many = I.normalizeHomeEntities({ entities: Array.from({ length: 500 }, (_, i) => ({ id: "light.l" + i, name: "L" + i })) });
  assert.equal(many.count, 500);
  assert.equal(many.items.length, 300);
});

test("haLanguageOptions: reply language, de, en, and a stored other code", () => {
  const tr = I.makeTr(I.STRINGS, "en");
  assert.deepEqual(
    I.haLanguageOptions("", tr).map((o) => o.value),
    ["", "de", "en"]
  );
  assert.equal(I.haLanguageOptions("", tr)[0].label, I.STRINGS.en.haLangAuto);
  assert.deepEqual(
    I.haLanguageOptions("de-CH", tr).map((o) => o.value),
    ["", "de", "en", "de-CH"]
  );
  assert.equal(I.haLanguageOptions("<x>", tr).length, 3);
  assert.equal(I.makeTr(I.STRINGS, "de")("haLangAuto"), I.STRINGS.de.haLangAuto);
});

test("errorKey maps the HA integration codes", () => {
  const codes = { bad_url: "errHaUrl", bad_token: "errHaToken", url_required: "errHaUrlFirst", env_override: "errEnvOverride" };
  for (const [code, key] of Object.entries(codes)) {
    assert.equal(I.errorKey({ status: 400, code }), key, code);
    assert.ok(I.STRINGS.de[key] && I.STRINGS.en[key] && I.STRINGS.de[key] !== I.STRINGS.en[key], key);
  }
});

test("smart home setup help names the real HA steps in DE and EN", () => {
  assert.match(I.STRINGS.en.haSetup2, /Security/);
  assert.match(I.STRINGS.en.haSetup2, /Long-lived access tokens/);
  assert.match(I.STRINGS.de.haSetup2, /Sicherheit/);
  assert.match(I.STRINGS.en.haSetup3, /Expose/);
  assert.match(I.STRINGS.de.haSetup3, /Sprachassistenten/);
  assert.match(I.STRINGS.de.haSetup1, /ohne Admin-Rechte/);
});

// ------------------------------------------------------------------ smart home section (fake DOM)

/* Just enough DOM for integrations.js: elements, text, events, [data-setting] queries. */
function makeDom() {
  let active = null;
  class Node {
    constructor(tag) {
      this.tagName = tag ? tag.toUpperCase() : "#text";
      this.childNodes = [];
      this.parentNode = null;
      this.attrs = {};
      this.listeners = {};
      this.hidden = false;
      this.disabled = false;
      this.checked = false;
      this._value = "";
      this._text = "";
      this.className = "";
      this.placeholder = "";
      const self = this;
      this.classList = {
        add: (...c) => (self.className = (self.className + " " + c.join(" ")).trim()),
        contains: (c) => self.className.split(/\s+/).includes(c),
      };
    }
    get firstChild() {
      return this.childNodes[0] || null;
    }
    get options() {
      return this.childNodes.filter((c) => c.tagName === "OPTION");
    }
    get type() {
      return this.attrs.type || "";
    }
    get value() {
      if (this.tagName === "OPTION") return this.attrs.value != null ? this.attrs.value : this._value;
      return this._value;
    }
    set value(v) {
      if (this.tagName === "SELECT") {
        const o = this.options.find((x) => x.value === String(v));
        this._value = o ? o.value : this.options[0] ? this.options[0].value : "";
      } else if (this.tagName === "OPTION") this.attrs.value = String(v);
      else this._value = String(v);
    }
    get textContent() {
      if (this.tagName === "#text") return this._text;
      return this._text + this.childNodes.map((c) => c.textContent).join("");
    }
    set textContent(t) {
      this.childNodes = [];
      this._text = String(t);
    }
    get innerHTML() {
      throw new Error("innerHTML must not be used");
    }
    set innerHTML(v) {
      throw new Error("innerHTML must not be used");
    }
    appendChild(c) {
      if (c.parentNode) c.parentNode.removeChild(c);
      c.parentNode = this;
      this.childNodes.push(c);
      if (this.tagName === "SELECT" && this.options.length === 1) this._value = c.value;
      return c;
    }
    removeChild(c) {
      this.childNodes = this.childNodes.filter((x) => x !== c);
      c.parentNode = null;
      return c;
    }
    setAttribute(k, v) {
      this.attrs[k] = String(v);
      if (k === "id") this.id = String(v);
    }
    getAttribute(k) {
      return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null;
    }
    addEventListener(t, fn) {
      (this.listeners[t] = this.listeners[t] || []).push(fn);
    }
    dispatchEvent(ev) {
      ev.target = ev.target || this;
      let stop = false;
      ev.stopPropagation = () => (stop = true);
      ev.preventDefault = () => {};
      for (let n = this; n && !stop; n = n.parentNode) (n.listeners[ev.type] || []).forEach((fn) => fn(ev));
      return true;
    }
    focus() {
      active = this;
    }
    click() {
      if (this.disabled) return;
      this.dispatchEvent({ type: "click" });
      if (this.attrs.type === "submit") {
        for (let n = this.parentNode; n; n = n.parentNode) {
          if (n.tagName === "FORM") {
            n.dispatchEvent({ type: "submit" });
            break;
          }
        }
      }
    }
    all() {
      return this.childNodes.flatMap((c) => [c, ...c.all()]);
    }
    querySelectorAll(sel) {
      const m = /^\[([\w-]+)\]$/.exec(sel);
      return m ? this.all().filter((n) => n.getAttribute(m[1]) != null) : [];
    }
    find(pred) {
      return this.all().find(pred) || null;
    }
  }
  const document = {
    createElement: (t) => new Node(t),
    createTextNode: (t) => {
      const n = new Node("");
      n._text = String(t);
      return n;
    },
    addEventListener: () => {},
    get activeElement() {
      return active;
    },
    visibilityState: "visible",
    documentElement: { lang: "de" },
  };
  return { document, Node };
}

/* Objects from the vm realm have other prototypes; compare their JSON. */
const plain = (o) => JSON.parse(JSON.stringify(o));

function loadHomeSection(opts) {
  opts = opts || {};
  const { document } = makeDom();
  const calls = [];
  const toasts = [];
  const confirms = [];
  // lib.js defaults: device control and queries for known people only
  const sets = Object.assign({ homeControl: "known", homeQueries: "known", haLanguage: "" }, opts.settings || {});
  const state = {
    ha: Object.assign({ url: "", urlValid: true, tokenSet: false, fromEnv: false, urlFromEnv: false, tokenFromEnv: false }, opts.ha || {}),
    admin: opts.admin !== false,
    status: opts.status || { configured: false, ok: false, code: "", entities: 0 },
  };
  const routes = Object.assign(
    {
      "GET /api/integrations": () => ({ ha: state.ha, google: {}, lock: {}, admin: state.admin }),
      "GET /api/home/status": () => state.status,
      "POST /api/integrations/ha": (body) => {
        if (body.url !== undefined) {
          state.ha.url = body.url;
          if (body.token === undefined) state.ha.tokenSet = false;
        }
        if (body.token !== undefined) state.ha.tokenSet = !!body.token;
        if (body.url === "") state.ha.tokenSet = false;
        return { ha: state.ha, google: {}, lock: {}, admin: state.admin };
      },
    },
    opts.routes || {}
  );
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
    Event: function (type) {
      this.type = type;
    },
    location: { host: "localhost:8080", search: "", pathname: "/", hash: "" },
    navigator: { userAgent: "node" },
    document,
    confirm: (msg) => {
      confirms.push(msg);
      return opts.confirm !== false;
    },
  };
  win.window = win;
  vm.createContext(win);
  vm.runInContext(fs.readFileSync(path.join(STATIC, "hermes.js"), "utf8"), win, { filename: "hermes.js" });
  win.Hermes.api = {
    lang: () => opts.lang || "de",
    settings: () => Object.assign({}, sets),
    toast: (msg, o) => toasts.push([msg, o && o.type]),
    fetchJSON: (url, o) => {
      const method = (o && o.method) || "GET";
      calls.push([method, url, o && o.body]);
      const h = routes[method + " " + url];
      if (!h) return Promise.reject(Object.assign(new Error("not found"), { status: 404 }));
      try {
        const r = h(o && o.body);
        return r && typeof r.then === "function" ? r : Promise.resolve(r);
      } catch (e) {
        return Promise.reject(e);
      }
    },
  };
  vm.runInContext(fs.readFileSync(path.join(STATIC, "integrations.js"), "utf8"), win, { filename: "integrations.js" });
  const sec = win.Hermes.sections().find((s) => s.id === "home");
  const root = document.createElement("div");
  sec.mount(root);
  const byId = (id) => root.find((n) => n.id === id);
  const button = (label) => root.find((n) => n.tagName === "BUTTON" && n.textContent === label);
  const msg = () => root.find((n) => n.className && n.className.split(" ").includes("int-msg"));
  const status = () => root.find((n) => n.className === "int-status");
  return { win, root, sec, calls, toasts, confirms, state, sets, byId, button, msg, status, S: I.STRINGS[opts.lang === "en" ? "en" : "de"] };
}

test("home section: registered as 'home' (order 50), DE and EN titles, all three settings bound", async () => {
  const h = loadHomeSection({ lang: "de" });
  await settle();
  assert.equal(h.sec.order, 50);
  assert.deepEqual(plain(h.sec.title), { en: "Smart Home", de: "Smart Home" });
  const bound = h.root.querySelectorAll("[data-setting]").map((n) => n.getAttribute("data-setting"));
  assert.deepEqual(bound.sort(), ["haLanguage", "homeControl", "homeQueries"]);
  const ctl = h.root.find((n) => n.getAttribute("data-setting") === "homeControl");
  // the default ("known") is listed first and labelled as the default
  assert.match(ctl.options[0].textContent, /\(default\)|\(Standard\)/);
  assert.deepEqual(ctl.options.map((o) => o.value), ["known", "everyone", "off"]);
  assert.equal(ctl.value, "known", "device control defaults to known people only");
  assert.equal(ctl.options[2].textContent, I.STRINGS.de.hcOff);
  const q = h.root.find((n) => n.getAttribute("data-setting") === "homeQueries");
  assert.equal(q.value, "known");
  assert.equal(h.status().textContent, I.STRINGS.de.haUnconfigured);
  assert.ok(h.root.textContent.includes(I.STRINGS.de.haSetup2));
  assert.ok(h.root.textContent.includes(I.STRINGS.de.haBindHint));
  const en = loadHomeSection({ lang: "en" });
  await settle();
  assert.equal(en.status().textContent, I.STRINGS.en.haUnconfigured);
  assert.ok(en.root.textContent.includes("Long-lived access tokens"));
  assert.ok(!/pilot/i.test(en.root.textContent + h.root.textContent));
});

test("home section: first setup sends address + token, never shows the token, then tests", async () => {
  const h = loadHomeSection({
    lang: "en",
    routes: { "POST /api/home/test": () => ({ configured: true, ok: true, version: "2026.11.1", code: "ok", entities: 12 }) },
  });
  await settle();
  h.byId("int_ha_url").value = "http://192.168.1.20:8123/";
  h.byId("int_ha_token").value = "eyJ.secret.token";
  h.button(I.STRINGS.en.haSave).click();
  await settle();
  const post = h.calls.find((c) => c[0] === "POST" && c[1] === "/api/integrations/ha");
  assert.deepEqual(plain(post[2]), { url: "http://192.168.1.20:8123", token: "eyJ.secret.token" });
  assert.equal(h.byId("int_ha_token").value, ""); // cleared after saving
  assert.ok(!h.root.textContent.includes("eyJ"));
  assert.equal(h.byId("int_ha_token").placeholder, I.STRINGS.en.haTokenPlaceholderSet);
  assert.equal(h.byId("int_ha_url").value, "http://192.168.1.20:8123");
  // the admin test ran right after saving
  assert.ok(h.calls.some((c) => c[0] === "POST" && c[1] === "/api/home/test"));
  assert.equal(h.status().textContent, "Connected – Home Assistant 2026.11.1.");
  assert.ok(h.root.textContent.includes("12 devices are available to HERMES."));
  assert.ok(h.toasts.some((t) => t[0] === I.STRINGS.en.haSaved));
});

test("home section: a new address without a token asks first and says the token was deleted", async () => {
  const h = loadHomeSection({ lang: "de", ha: { url: "http://192.168.1.20:8123", tokenSet: true }, status: { configured: true, ok: true, entities: 3 } });
  await settle();
  assert.equal(h.byId("int_ha_url").value, "http://192.168.1.20:8123");
  h.byId("int_ha_url").value = "http://192.168.1.30:8123";
  h.button(I.STRINGS.de.haSave).click();
  await settle();
  assert.deepEqual(h.confirms, [I.STRINGS.de.haUrlChangeConfirm]);
  const post = h.calls.find((c) => c[0] === "POST" && c[1] === "/api/integrations/ha");
  assert.deepEqual(plain(post[2]), { url: "http://192.168.1.30:8123" });
  assert.ok(h.toasts.some((t) => t[0] === I.STRINGS.de.haSavedNoToken));
  assert.equal(h.status().textContent, I.STRINGS.de.haNoToken);

  // declined: nothing is sent
  const d = loadHomeSection({ lang: "en", confirm: false, ha: { url: "http://192.168.1.20:8123", tokenSet: true } });
  await settle();
  d.byId("int_ha_url").value = "http://192.168.1.30:8123";
  d.button(I.STRINGS.en.haSave).click();
  await settle();
  assert.equal(d.calls.filter((c) => c[1] === "/api/integrations/ha").length, 0);
});

test("home section: invalid input is refused locally; server errors use the DE/EN texts", async () => {
  const h = loadHomeSection({
    lang: "en",
    routes: { "POST /api/integrations/ha": () => Promise.reject(Object.assign(new Error("x"), { status: 409, code: "env_override" })) },
  });
  await settle();
  h.byId("int_ha_url").value = "http://ha:8123/api";
  h.button(I.STRINGS.en.haSave).click();
  await settle();
  assert.equal(h.msg().textContent, I.STRINGS.en.errHaUrl);
  assert.equal(h.calls.filter((c) => c[1] === "/api/integrations/ha").length, 0);
  h.byId("int_ha_url").value = "http://ha:8123";
  h.byId("int_ha_token").value = "abc";
  h.button(I.STRINGS.en.haSave).click();
  await settle();
  assert.equal(h.msg().textContent, I.STRINGS.en.errEnvOverride);
});

test("home section: a LAN device views only; the test button reads the cached status", async () => {
  const h = loadHomeSection({
    lang: "de",
    admin: false,
    ha: { url: "http://192.168.1.20:8123", tokenSet: true },
    status: { configured: true, ok: false, code: "auth" },
  });
  await settle();
  assert.equal(h.byId("int_ha_url").disabled, true);
  assert.equal(h.byId("int_ha_token").disabled, true);
  assert.equal(h.button(I.STRINGS.de.haSave).disabled, true);
  assert.equal(h.button(I.STRINGS.de.haClear).hidden, true);
  assert.equal(h.root.find((n) => n.textContent === I.STRINGS.de.haAdminOnly).hidden, false);
  assert.equal(h.status().textContent, I.STRINGS.de.haAuth);
  h.calls.length = 0;
  h.button(I.STRINGS.de.haTest).click();
  await settle();
  assert.deepEqual(h.calls.map((c) => c[0] + " " + c[1]).slice(0, 1), ["GET /api/home/status"]);
  assert.ok(!h.calls.some((c) => c[1] === "/api/home/test"));
});

test("home section: values from .env are read-only; the test falls back when /api/home/test is missing", async () => {
  const h = loadHomeSection({
    lang: "en",
    ha: { url: "http://ha.local:8123", tokenSet: true, fromEnv: true, urlFromEnv: true, tokenFromEnv: true },
    status: { configured: true, connected: true, entities: 2 },
    routes: {
      "GET /api/home/entities": () => ({
        entities: [
          { id: "light.k", name: "Kitchen", area: "Kitchen", domain: "light", blocked: false },
          { id: "lock.f", name: "<b>Front door</b>", area: "Hall", domain: "lock", blocked: true },
        ],
      }),
    },
  });
  await settle();
  assert.equal(h.byId("int_ha_url").disabled, true);
  assert.equal(h.byId("int_ha_token").disabled, true);
  assert.equal(h.button(I.STRINGS.en.haClear).hidden, true);
  assert.equal(h.root.find((n) => n.textContent === I.STRINGS.en.haEnvHint).hidden, false);
  assert.ok(h.root.textContent.includes("(" + I.STRINGS.en.credEnv + ")"));
  assert.ok(h.root.textContent.includes("2 devices are available to HERMES, 1 blocked"));
  // names are text, the blocked one last and flagged
  const items = h.root.find((n) => n.tagName === "UL").childNodes;
  assert.equal(items[1].childNodes[0].textContent, "<b>Front door</b>");
  assert.equal(items[1].className, "blocked");
  h.calls.length = 0;
  h.button(I.STRINGS.en.haTest).click();
  await settle();
  const seq = h.calls.map((c) => c[0] + " " + c[1]);
  assert.deepEqual(seq.slice(0, 2), ["POST /api/home/test", "GET /api/home/status"]);
  assert.ok(h.toasts.some((t) => t[0] === I.STRINGS.en.haTestOk));
});

test("home section: without /api/home on the server the status says so; clearing asks and sends url:\"\"", async () => {
  const h = loadHomeSection({ lang: "de", ha: { url: "http://ha:8123", tokenSet: true }, routes: { "GET /api/home/status": undefined } });
  await settle();
  assert.equal(h.status().textContent, I.STRINGS.de.haUnavailable);
  assert.equal(h.button(I.STRINGS.de.haTest).disabled, true);
  h.button(I.STRINGS.de.haClear).click();
  await settle();
  assert.deepEqual(h.confirms, [I.STRINGS.de.haClearConfirm]);
  assert.deepEqual(plain(h.calls.find((c) => c[1] === "/api/integrations/ha")[2]), { url: "" });
  assert.equal(h.byId("int_ha_url").value, "");
  assert.equal(h.status().textContent, I.STRINGS.de.haUnavailable);
});

test("home section: 'off' shows the hint; a stored HA language other than de/en stays selected", async () => {
  const h = loadHomeSection({ lang: "en", settings: { homeControl: "off", haLanguage: "de-CH" } });
  await settle();
  assert.equal(h.root.find((n) => n.textContent === I.STRINGS.en.homeOffHint).hidden, false);
  const sel = h.root.find((n) => n.getAttribute("data-setting") === "haLanguage");
  assert.equal(sel.value, "de-CH");
  const ctl = h.root.find((n) => n.getAttribute("data-setting") === "homeControl");
  ctl.value = "known";
  ctl.dispatchEvent({ type: "change" });
  assert.equal(h.root.find((n) => n.textContent === I.STRINGS.en.homeOffHint).hidden, true);
});
