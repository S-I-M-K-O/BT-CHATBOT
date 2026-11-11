/*
 * HERMES check-ins (W2-N, SPEC 5.5 "checkins.js" and 9.8).
 *
 * Part 1 (UMD, no DOM): pure helpers for the browser (window.HermesCheckins)
 * and the node tests (require("../../static/checkins.js")):
 *
 *   sitInit, sitObserve, sitTick, sitDecide, sitNudged   sitting streak state machine
 *   sittingLine, isQuiet, normalizeState, parseTime, clampMinutes,
 *   endOfDay, nudgePlan, STRINGS, LINES
 *
 * Part 2 (browser only, needs window.Hermes and window.HermesReminders):
 *   - sitting check-ins: an IdleDetector (threshold 60 s) feeds the streak
 *     machine; after `checkinSittingMin` minutes of continuous activity the
 *     leader tab of a "speak here" device says a rotating line. Suppressed
 *     while a calendar event is in progress (GET /api/checkins/state,
 *     polled every 5 min; retried 5 min after the event ends), while
 *     check-ins are paused, during quiet hours and while HERMES is talking
 *     or answering. Off by default (setting `checkinSitting`).
 *   - meeting check-ins arrive from the server as `reminder.due` with
 *     kind "meeting" and are delivered by reminders.js (speak / notify /
 *     notice / ack). This module only watches them (`reminders:due`) so a
 *     sitting nudge does not follow right after a meeting announcement.
 *   - window.HermesCheckins.ui: support / permission / enable for the
 *     Settings → Check-ins section (integrations.js).
 *
 * Quiet hours and the delivery ladder are reused from reminders.js
 * (HermesReminders.inQuietHours / deliveryPlan / shouldSpeakHere).
 */
(function (root, factory) {
  var R = null;
  if (typeof module === "object" && module.exports) {
    try {
      R = require("./reminders.js");
    } catch (e) {
      R = null;
    }
  } else if (root && root.HermesReminders) {
    R = root.HermesReminders;
  }
  var api = factory(R);
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root && typeof root === "object") root.HermesCheckins = api;
})(typeof window !== "undefined" ? window : typeof globalThis !== "undefined" ? globalThis : this, function (R) {
  "use strict";

  var MINUTE = 60 * 1000;
  var IDLE_THRESHOLD_MS = 60 * 1000; // IdleDetector threshold (its minimum)
  var RESET_IDLE_MS = 5 * MINUTE; // an idle stretch this long ends the streak
  var BUSY_RETRY_MS = 5 * MINUTE; // retry this long after a calendar event ends
  var SNOOZE_MS = 15 * MINUTE; // "Später" on a nudge
  var MEETING_HOLD_MS = 15 * MINUTE; // no sitting nudge right after a meeting check-in
  var MIN_DEFAULT = 60;
  var MIN_LOW = 20;
  var MIN_HIGH = 240;

  var STRINGS = {
    en: {
      notice: "{line}",
      later: "Later",
      laterTitle: "Remind me again in 15 minutes",
      noMoreToday: "Not today",
      noMoreTodayTitle: "No more sitting check-ins on this device today",
      eyeBanner: "CHECK-IN",
      snoozed: "Okay, I will ask again in 15 minutes.",
      offToday: "Okay, no more sitting check-ins on this device today.",
    },
    de: {
      notice: "{line}",
      later: "Später",
      laterTitle: "In 15 Minuten noch einmal fragen",
      noMoreToday: "Heute nicht mehr",
      noMoreTodayTitle: "Heute keine Sitz-Hinweise mehr auf diesem Gerät",
      eyeBanner: "CHECK-IN",
      snoozed: "Okay, ich frage in 15 Minuten noch einmal.",
      offToday: "Okay, heute keine Sitz-Hinweise mehr auf diesem Gerät.",
    },
  };

  // Rotating sitting lines ({n} = minutes). Never addresses the user as "Pilot".
  var LINES = {
    de: [
      "Du sitzt seit {n} Minuten. Zeit, kurz aufzustehen und dich zu strecken.",
      "Seit {n} Minuten am Stück dabei. Steh kurz auf und hol dir ein Glas Wasser.",
      "{n} Minuten ohne Pause. Ein paar Schritte und einmal die Schultern kreisen tun gut.",
      "Kleine Pause? Du bist seit {n} Minuten aktiv. Steh auf und schau kurz aus dem Fenster.",
    ],
    en: [
      "You have been sitting for {n} minutes. Time to stand up and stretch.",
      "{n} minutes in a row. Stand up for a moment and get a glass of water.",
      "{n} minutes without a break. A few steps and a shoulder roll will do you good.",
      "Short break? You have been at it for {n} minutes. Stand up and look out of the window for a bit.",
    ],
  };

  // ------------------------------------------------------------ small utils

  function format(str, vars) {
    str = String(str == null ? "" : str);
    if (!vars) return str;
    return str.replace(/\{(\w+)\}/g, function (all, k) {
      return Object.prototype.hasOwnProperty.call(vars, k) && vars[k] != null ? String(vars[k]) : all;
    });
  }

  function makeTr(dict, langFn) {
    if (R && typeof R.makeTr === "function") return R.makeTr(dict, langFn);
    return function (key, vars) {
      var lang = (typeof langFn === "function" ? langFn() : langFn) === "de" ? "de" : "en";
      var t = dict[lang] || {};
      var s = Object.prototype.hasOwnProperty.call(t, key) ? t[key] : Object.prototype.hasOwnProperty.call(dict.en, key) ? dict.en[key] : key;
      return format(s, vars);
    };
  }

  function num(v) {
    return typeof v === "number" && isFinite(v) ? v : 0;
  }

  /* Sitting minutes setting: an integer clamped to 20..240, default 60. */
  function clampMinutes(v) {
    var n = typeof v === "string" && v.trim() !== "" ? Number(v) : v;
    if (typeof n !== "number" || !isFinite(n)) return MIN_DEFAULT;
    n = Math.round(n);
    return Math.max(MIN_LOW, Math.min(MIN_HIGH, n));
  }

  /* RFC3339 (or "") → ms since epoch; 0 when empty or invalid. */
  function parseTime(s) {
    if (typeof s !== "string" || !s || s.length > 64) return 0;
    var t = Date.parse(s);
    return isFinite(t) ? t : 0;
  }

  /*
   * GET /api/checkins/state → {busyUntil, pausedUntil} in ms (0 = none).
   * Values far in the future are capped at 2 days (a broken server must
   * not silence the nudges forever on this tab).
   */
  function normalizeState(json, now) {
    var out = { busyUntil: 0, pausedUntil: 0 };
    if (!json || typeof json !== "object") return out;
    var cap = num(now) + 2 * 24 * 60 * MINUTE;
    out.busyUntil = Math.min(parseTime(json.busyUntil), cap);
    out.pausedUntil = Math.min(parseTime(json.pausedUntil), cap);
    return out;
  }

  /* End of the local day of `now` (ms): the next local midnight. */
  function endOfDay(now) {
    var d = new Date(num(now));
    d.setHours(24, 0, 0, 0);
    return d.getTime();
  }

  /*
   * Quiet hours of the settings (defaults 22:00–07:00, settings time zone),
   * via HermesReminders.inQuietHours so both modules agree.
   */
  function isQuiet(now, s) {
    s = s || {};
    var start = s.quietStart == null ? "22:00" : s.quietStart;
    var end = s.quietEnd == null ? "07:00" : s.quietEnd;
    if (!R || typeof R.inQuietHours !== "function") return false;
    return R.inQuietHours(new Date(num(now)), start, end, s.timezone || "");
  }

  /* The rotating nudge line for `n` minutes in `lang` ("de"/"en"). */
  function sittingLine(n, lang, index) {
    var list = LINES[lang === "de" ? "de" : "en"];
    var i = Math.abs(Math.floor(num(index))) % list.length;
    var mins = Math.max(1, Math.floor(num(n)));
    return format(list[i], { n: mins });
  }

  // ------------------------------------------------------------ sitting streak

  /*
   * The streak state machine. Pure: every function returns a new state and
   * takes the time (ms) as a parameter, so tests run on fake timestamps.
   *
   *   streakStart  start of the current continuous-activity streak (0 = none)
   *   idleSince    when the current idle stretch began (0 = active)
   *   active       the user is active right now
   *   locked       the screen is locked
   *   lastAt       the last observation or tick (gap detection: sleep)
   *   retryAt      no nudge before this time (busy: event end + 5 min)
   *   nudges       nudges given so far (rotates the line)
   */
  function sitInit() {
    return { streakStart: 0, idleSince: 0, active: false, locked: false, lastAt: 0, retryAt: 0, nudges: 0, lastNudgeAt: 0 };
  }

  function copy(st) {
    var s = sitInit();
    if (st && typeof st === "object") {
      for (var k in s) if (Object.prototype.hasOwnProperty.call(st, k)) s[k] = st[k];
    }
    return s;
  }

  function endStreak(s) {
    s.streakStart = 0;
    s.retryAt = 0;
    return s;
  }

  /* An idle stretch of ≥ 5 min ends the streak; remember the latest time seen. */
  function advance(s, now) {
    if (s.idleSince && now - s.idleSince >= RESET_IDLE_MS && s.streakStart) endStreak(s);
    s.lastAt = Math.max(s.lastAt, now);
    return s;
  }

  /*
   * The heartbeat (every 30 s in the browser): like advance(), plus a gap
   * of ≥ 5 min since the last heartbeat or observation (the computer
   * slept, the tab was frozen) counts as a break and restarts the streak.
   * Observations and decisions do not do this, because the detector only
   * reports changes: a long stretch between two of them is normal.
   */
  function sitTick(st, now) {
    var s = copy(st);
    if (s.lastAt && now - s.lastAt >= RESET_IDLE_MS && s.streakStart) {
      if (s.active && !s.locked) {
        s.streakStart = now;
        s.retryAt = 0;
      } else endStreak(s);
    }
    return advance(s, now);
  }

  /*
   * An IdleDetector reading {userState: "active"|"idle", screenState:
   * "locked"|"unlocked"} at `now`. The detector reports "idle" only after
   * the threshold (60 s), so the idle stretch began that much earlier.
   */
  function sitObserve(st, obs, now, opts) {
    var threshold = opts && opts.idleThresholdMs > 0 ? opts.idleThresholdMs : IDLE_THRESHOLD_MS;
    var s = advance(copy(st), now);
    obs = obs || {};
    if (obs.screenState === "locked") {
      s.locked = true;
      s.active = false;
      s.idleSince = 0;
      endStreak(s);
      return s;
    }
    s.locked = false;
    if (obs.userState === "idle") {
      if (!s.idleSince) s.idleSince = Math.max(0, now - threshold);
      s.active = false;
      if (now - s.idleSince >= RESET_IDLE_MS && s.streakStart) endStreak(s);
      return s;
    }
    // active
    if (s.idleSince && now - s.idleSince >= RESET_IDLE_MS) endStreak(s);
    s.idleSince = 0;
    s.active = true;
    if (!s.streakStart) {
      s.streakStart = now;
      s.retryAt = 0;
    }
    return s;
  }

  /* Whole minutes of the current streak. */
  function sitMinutes(st, now) {
    return st && st.streakStart ? Math.max(0, Math.floor((now - st.streakStart) / MINUTE)) : 0;
  }

  /*
   * Should we nudge now? ctx:
   *   enabled            settings.checkinSitting (and this device runs a detector)
   *   minutes            settings.checkinSittingMin
   *   quiet              inside quiet hours
   *   busyUntil          end of the calendar event in progress (ms, 0 = none)
   *   pausedUntil        server pause (voice: "keine Check-ins mehr heute")
   *   localPausedUntil   "Heute nicht mehr" on this device
   *   snoozeUntil        "Später"
   *   speaking           HERMES talks or a reply is in progress
   * Returns {action: "none"|"wait"|"nudge", reason, until, minutes, state}.
   * `state` carries retryAt when a calendar event postponed the nudge.
   */
  function sitDecide(st, ctx, now) {
    ctx = ctx || {};
    var s = advance(copy(st), now);
    var out = function (action, reason, until) {
      return { action: action, reason: reason, until: until || 0, minutes: sitMinutes(s, now), state: s };
    };
    if (!ctx.enabled) return out("none", "off");
    if (!s.streakStart || !s.active || s.locked) return out("none", "idle");
    var dueAt = s.streakStart + clampMinutes(ctx.minutes) * MINUTE;
    if (now < dueAt) return out("none", "sitting", dueAt);
    if (num(ctx.busyUntil) > now) {
      // in a meeting: retry 5 min after it ends
      s.retryAt = Math.max(s.retryAt, num(ctx.busyUntil) + BUSY_RETRY_MS);
      return out("wait", "busy", s.retryAt);
    }
    if (s.retryAt && now < s.retryAt) return out("wait", "busy", s.retryAt);
    if (num(ctx.pausedUntil) > now) return out("wait", "paused", num(ctx.pausedUntil));
    if (num(ctx.localPausedUntil) > now) return out("wait", "paused", num(ctx.localPausedUntil));
    if (num(ctx.snoozeUntil) > now) return out("wait", "snoozed", num(ctx.snoozeUntil));
    if (ctx.quiet) return out("wait", "quiet");
    if (ctx.speaking) return out("wait", "speaking");
    return out("nudge", "due");
  }

  /* A nudge went out (or another tab took it): start a new streak now. */
  function sitNudged(st, now) {
    var s = copy(st);
    s.streakStart = s.active && !s.locked ? now : 0;
    s.retryAt = 0;
    s.nudges = (s.nudges || 0) + 1;
    s.lastNudgeAt = now;
    s.lastAt = Math.max(s.lastAt, now);
    return s;
  }

  /*
   * What to do with a nudge on this tab, through the reminders delivery
   * ladder (a "checkin" is not an explicit kind there, so quiet hours would
   * mute it too). Missed nudges are never queued for a later gesture: a
   * stale "stand up" is worse than none.
   */
  function nudgePlan(ctx) {
    ctx = ctx || {};
    var plan;
    if (R && typeof R.deliveryPlan === "function") {
      plan = R.deliveryPlan({ kind: "checkin", eventId: 0, chime: true, missed: false }, {
        claimed: !!ctx.claimed,
        fresh: true,
        speakHere: !!ctx.speakHere,
        hidden: !!ctx.hidden,
        notifyGranted: !!ctx.notifyGranted,
        gesture: !!ctx.gesture,
        quiet: !!ctx.quiet,
        missedSpoken: 0,
      });
    } else {
      plan = { notice: !!ctx.claimed, speak: !!(ctx.claimed && ctx.speakHere && ctx.gesture && !ctx.quiet), chime: false, defer: false, notify: false };
      plan.chime = plan.speak;
      plan.notify = !!(ctx.claimed && ctx.hidden && ctx.notifyGranted);
    }
    return { notice: !!plan.notice, speak: !!plan.speak, chime: !!plan.chime, notify: !!plan.notify };
  }

  return {
    MINUTE: MINUTE,
    IDLE_THRESHOLD_MS: IDLE_THRESHOLD_MS,
    RESET_IDLE_MS: RESET_IDLE_MS,
    BUSY_RETRY_MS: BUSY_RETRY_MS,
    SNOOZE_MS: SNOOZE_MS,
    MEETING_HOLD_MS: MEETING_HOLD_MS,
    STRINGS: STRINGS,
    LINES: LINES,
    format: format,
    makeTr: makeTr,
    clampMinutes: clampMinutes,
    parseTime: parseTime,
    normalizeState: normalizeState,
    endOfDay: endOfDay,
    isQuiet: isQuiet,
    sittingLine: sittingLine,
    sitInit: sitInit,
    sitTick: sitTick,
    sitObserve: sitObserve,
    sitMinutes: sitMinutes,
    sitDecide: sitDecide,
    sitNudged: sitNudged,
    nudgePlan: nudgePlan,
  };
});

/* ====================================================================== */
/* Browser runtime                                                         */
/* ====================================================================== */
(function () {
  "use strict";
  if (typeof window === "undefined" || typeof document === "undefined") return;
  var H = window.Hermes;
  var C = window.HermesCheckins;
  if (!H || !C) return; // hermes.js missing: the page works without check-ins

  var TICK_MS = 30 * 1000;
  var POLL_MS = 5 * 60 * 1000;
  var LOCAL_OFF_KEY = "hermes-checkins-sit-off"; // "Heute nicht mehr" (ms), device-local

  function api() {
    return H.api || {};
  }
  function lang() {
    try {
      return api().lang && api().lang() === "de" ? "de" : "en";
    } catch (e) {
      return "en";
    }
  }
  var tr = typeof H.strings === "function" ? H.strings(C.STRINGS) : C.makeTr(C.STRINGS, lang);

  function settings() {
    try {
      return (api().settings && api().settings()) || {};
    } catch (e) {
      return {};
    }
  }
  function nowMs() {
    return Date.now();
  }
  function storeGet(k) {
    try {
      return window.localStorage ? window.localStorage.getItem(k) : null;
    } catch (e) {
      return null;
    }
  }
  function storeSet(k, v) {
    try {
      if (window.localStorage) window.localStorage.setItem(k, v);
    } catch (e) {
      /* blocked storage: lasts for this page only */
    }
  }
  function toast(msg, o) {
    try {
      if (typeof api().toast === "function") return api().toast(msg, o || {});
    } catch (e) {
      /* ignore */
    }
    return null;
  }
  function fetchJSON(url, o) {
    if (typeof api().fetchJSON === "function") return api().fetchJSON(url, o);
    return Promise.reject(Object.assign(new Error("not ready"), { status: 0 }));
  }
  function speakHere() {
    try {
      if (H.events && typeof H.events.speakHere === "function") return !!H.events.speakHere();
    } catch (e) {
      /* fall through */
    }
    var R = window.HermesReminders;
    var local = /^(localhost|127\.|\[::1\])/.test(location.host);
    return R ? R.shouldSpeakHere(local, storeGet("hermes-speak-reminders")) : local;
  }
  function gestureSeen() {
    try {
      return typeof api().gestureSeen === "function" ? !!api().gestureSeen() : true;
    } catch (e) {
      return true;
    }
  }
  function notifyGranted() {
    return !!window.isSecureContext && typeof window.Notification === "function" && window.Notification.permission === "granted";
  }
  function isSpeaking() {
    try {
      return replyBusy || (typeof api().isSpeaking === "function" && !!api().isSpeaking());
    } catch (e) {
      return replyBusy;
    }
  }

  // ---------------------------------------------------------------- state

  var run = {
    detector: null,
    abort: null,
    starting: false,
    tickTimer: 0,
    pollTimer: 0,
    st: C.sitInit(),
    server: { busyUntil: 0, pausedUntil: 0 },
    holdUntil: 0, // after a meeting check-in
    snoozeUntil: 0,
    localOffMem: 0,
    error: "",
    permission: "unknown", // "granted" | "denied" | "prompt" | "unknown"
  };
  var replyBusy = false;
  var listeners = [];

  H.on("reply:start", function () {
    replyBusy = true;
  });
  H.on("reply:done", function () {
    replyBusy = false;
  });

  function changed() {
    var snap = ui.state();
    listeners.slice().forEach(function (fn) {
      try {
        fn(snap);
      } catch (e) {
        /* ignore */
      }
    });
    H.emit("checkins:state", snap);
  }

  function localOffUntil() {
    var v = Number(storeGet(LOCAL_OFF_KEY));
    return Math.max(isFinite(v) ? v : 0, run.localOffMem);
  }

  // ---------------------------------------------------------------- IdleDetector

  function supported() {
    return !!window.isSecureContext && typeof window.IdleDetector === "function";
  }

  function queryPermission() {
    if (!supported()) return Promise.resolve("unsupported");
    var perms = navigator.permissions;
    if (!perms || typeof perms.query !== "function") return Promise.resolve(run.permission === "granted" ? "granted" : "prompt");
    return perms
      .query({ name: "idle-detection" })
      .then(function (p) {
        run.permission = p && p.state ? p.state : "prompt";
        return run.permission;
      })
      .catch(function () {
        return "prompt";
      });
  }

  /* The live settings (app.js reflects unsaved form changes too). */
  function wanted() {
    return !!settings().checkinSitting;
  }

  function start() {
    if (run.detector || run.starting || !supported()) return Promise.resolve(!!run.detector);
    run.starting = true;
    var det;
    var ctrl = typeof window.AbortController === "function" ? new window.AbortController() : null;
    try {
      det = new window.IdleDetector();
    } catch (e) {
      run.starting = false;
      run.error = "start";
      changed();
      return Promise.resolve(false);
    }
    det.addEventListener("change", function () {
      observe(det.userState, det.screenState, 0, true);
    });
    var o = { threshold: C.IDLE_THRESHOLD_MS };
    if (ctrl) o.signal = ctrl.signal;
    return Promise.resolve()
      .then(function () {
        return det.start(o);
      })
      .then(function () {
        run.starting = false;
        if (!wanted()) {
          // switched off while starting
          if (ctrl) ctrl.abort();
          return false;
        }
        run.detector = det;
        run.abort = ctrl;
        run.error = "";
        run.permission = "granted";
        run.st = C.sitInit();
        if (det.userState) observe(det.userState, det.screenState, 0, true);
        run.tickTimer = setInterval(function () {
          heartbeat();
        }, TICK_MS);
        pollState();
        run.pollTimer = setInterval(pollState, POLL_MS);
        changed();
        return true;
      })
      .catch(function (err) {
        run.starting = false;
        if (ctrl) ctrl.abort();
        run.error = err && err.name === "NotAllowedError" ? "denied" : "start";
        if (run.error === "denied") run.permission = "denied";
        changed();
        return false;
      });
  }

  function stop() {
    if (run.abort) {
      try {
        run.abort.abort();
      } catch (e) {
        /* ignore */
      }
    }
    run.detector = null;
    run.abort = null;
    if (run.tickTimer) clearInterval(run.tickTimer);
    if (run.pollTimer) clearInterval(run.pollTimer);
    run.tickTimer = 0;
    run.pollTimer = 0;
    run.st = C.sitInit();
    changed();
  }

  /* Start or stop the detector to match the setting (and the permission). */
  function sync() {
    if (!wanted() || !supported()) {
      if (run.detector) stop();
      return Promise.resolve(false);
    }
    if (run.detector || run.starting) return Promise.resolve(true);
    // only start without a gesture when the permission is already granted
    return queryPermission().then(function (p) {
      if (p === "granted" && wanted()) return start();
      changed();
      return false;
    });
  }

  /*
   * Ask for the idle-detection permission and start. MUST be called from a
   * user gesture (the Check-ins settings toggle). Resolves to "granted",
   * "denied", "unsupported" or "error".
   */
  function enable() {
    if (!supported()) return Promise.resolve("unsupported");
    var ask;
    try {
      ask = typeof window.IdleDetector.requestPermission === "function" ? window.IdleDetector.requestPermission() : Promise.resolve("granted");
    } catch (e) {
      return Promise.resolve("error");
    }
    return Promise.resolve(ask)
      .then(function (p) {
        run.permission = p === "granted" ? "granted" : p === "denied" ? "denied" : "prompt";
        if (p !== "granted") {
          changed();
          return p === "denied" ? "denied" : "error";
        }
        // the toggle changed the live settings already (app.js reads the
        // form on every change), so start() sees checkinSitting on
        return start().then(function (ok) {
          return ok ? "granted" : "error";
        });
      })
      .catch(function () {
        changed();
        return "error";
      });
  }

  // ---------------------------------------------------------------- streak

  /*
   * A detector reading. `live` readings come from the real detector at the
   * real time, so they also run the sleep-gap check of the heartbeat.
   */
  function observe(userState, screenState, at, live) {
    var now = at || nowMs();
    if (live) run.st = C.sitTick(run.st, now);
    run.st = C.sitObserve(run.st, { userState: userState, screenState: screenState }, now);
    tick(now);
  }

  /* Every 30 s while running (and when the tab becomes visible). */
  function heartbeat() {
    var now = nowMs();
    run.st = C.sitTick(run.st, now);
    return tick(now);
  }

  function context(now) {
    var s = settings();
    return {
      enabled: !!run.detector && wanted(),
      minutes: s.checkinSittingMin,
      quiet: C.isQuiet(now, s),
      busyUntil: run.server.busyUntil,
      pausedUntil: run.server.pausedUntil,
      localPausedUntil: localOffUntil(),
      // a meeting check-in just went out: hold like a snooze (no extra 5 min)
      snoozeUntil: Math.max(run.snoozeUntil, run.holdUntil),
      speaking: isSpeaking(),
    };
  }

  function tick(at) {
    var now = at || nowMs();
    var d = C.sitDecide(run.st, context(now), now);
    run.st = d.state;
    if (d.action === "nudge") nudge(d.minutes, now);
    return d;
  }

  function pollState() {
    return fetchJSON("/api/checkins/state")
      .then(function (j) {
        run.server = C.normalizeState(j, nowMs());
      })
      .catch(function () {
        // older server or a hiccup: no busy / pause information
        run.server = { busyUntil: 0, pausedUntil: 0 };
      });
  }

  // ---------------------------------------------------------------- delivery

  function claim(key) {
    try {
      if (H.events && typeof H.events.claim === "function") return !!H.events.claim(key);
    } catch (e) {
      /* fall through */
    }
    return true;
  }

  function nudge(minutes, now) {
    var key = "checkin:sit:" + Math.floor(run.st.streakStart / 60000);
    var claimed = claim(key);
    var index = run.st.nudges;
    run.st = C.sitNudged(run.st, now);
    var plan = C.nudgePlan({
      claimed: claimed,
      speakHere: speakHere(),
      hidden: document.visibilityState === "hidden",
      notifyGranted: notifyGranted(),
      gesture: gestureSeen(),
      quiet: false, // checked by sitDecide already
    });
    var line = C.sittingLine(minutes, lang(), index);
    if (plan.notice) showNotice(line);
    if (plan.notify) showSystemNotification(line);
    if (plan.speak) speak(line, plan.chime);
    H.emit("checkins:nudge", { minutes: minutes, spoken: plan.speak, claimed: claimed });
    changed();
  }

  function showNotice(line) {
    if (typeof api().addNotice !== "function") return;
    var handle = null;
    var actions = [
      {
        label: tr("later"),
        title: tr("laterTitle"),
        fn: function () {
          run.snoozeUntil = nowMs() + C.SNOOZE_MS;
          if (handle) handle.update(null, []);
          toast(tr("snoozed"), { type: "success", key: "checkin-sit" });
        },
      },
      {
        label: tr("noMoreToday"),
        title: tr("noMoreTodayTitle"),
        fn: function () {
          var until = C.endOfDay(nowMs());
          run.localOffMem = until;
          storeSet(LOCAL_OFF_KEY, String(until));
          if (handle) handle.update(null, []);
          toast(tr("offToday"), { type: "success", key: "checkin-sit" });
          changed();
        },
      },
    ];
    try {
      // a nudge is not worth keeping in the stored conversation
      handle = api().addNotice(line, { kind: "checkin", actions: actions, ephemeral: true });
    } catch (e) {
      handle = null;
    }
  }

  function speak(line, withChime) {
    var a = api();
    try {
      if (typeof a.flashEye === "function") a.flashEye("speaking", tr("eyeBanner"), 4000);
    } catch (e) {
      /* ignore */
    }
    var c = withChime && typeof a.chime === "function" ? a.chime() : null;
    Promise.resolve(c)
      .catch(function () {})
      .then(function () {
        if (typeof a.say === "function" && !isSpeaking()) return a.say(line, { force: true });
        return null;
      })
      .catch(function () {
        /* speech failed: the notice is there */
      });
  }

  function showSystemNotification(line) {
    try {
      var n = new window.Notification("HERMES", { body: line, tag: "hermes-checkin-sit", silent: speakHere() });
      n.onclick = function () {
        try {
          window.focus();
        } catch (e) {
          /* ignore */
        }
        n.close();
      };
    } catch (e) {
      /* e.g. Android Chrome needs a service worker */
    }
  }

  // ---------------------------------------------------------------- wiring

  // meeting check-ins are delivered by reminders.js; hold the sitting nudge
  // so the user does not hear "stand up" right after "In 10 Minuten: …"
  H.on("reminders:due", function (d) {
    if (!d || d.kind !== "meeting") return;
    run.holdUntil = nowMs() + C.MEETING_HOLD_MS;
    if (run.detector) pollState();
  });
  H.on("hub:calendar.changed", function () {
    if (run.detector) pollState();
  });
  H.on("settings", function () {
    sync();
  });
  H.on("ready", function () {
    sync();
  });
  H.on("visible", function () {
    if (run.detector) heartbeat();
  });

  var ui = {
    /* "unsupported" | "insecure" | "ok" */
    support: function () {
      if (!window.isSecureContext) return "insecure";
      return typeof window.IdleDetector === "function" ? "ok" : "unsupported";
    },
    queryPermission: queryPermission,
    enable: enable,
    sync: sync,
    /* A snapshot for the settings section. */
    state: function () {
      var now = nowMs();
      return {
        support: ui.support(),
        permission: run.permission,
        running: !!run.detector,
        error: run.error,
        minutes: C.sitMinutes(run.st, now),
        active: !!run.st.active,
        pausedUntil: run.server.pausedUntil > now ? run.server.pausedUntil : 0,
        localPausedUntil: localOffUntil() > now ? localOffUntil() : 0,
        busyUntil: run.server.busyUntil > now ? run.server.busyUntil : 0,
      };
    },
    /* Clear "Heute nicht mehr" on this device. */
    resume: function () {
      run.localOffMem = 0;
      run.snoozeUntil = 0;
      storeSet(LOCAL_OFF_KEY, "0");
      changed();
    },
    refresh: pollState,
    onChange: function (fn) {
      if (typeof fn === "function") listeners.push(fn);
      return function () {
        var i = listeners.indexOf(fn);
        if (i >= 0) listeners.splice(i, 1);
      };
    },
    // test hooks (fake clock): drive the machine without a real detector
    _observe: observe,
    _tick: tick,
    _heartbeat: heartbeat,
    _run: run,
  };
  C.ui = ui;
})();
