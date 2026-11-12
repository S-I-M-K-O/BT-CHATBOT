/*
 * HERMES reminders UI (W2-N, SPEC 5.5 "reminders.js").
 *
 * Part 1 (UMD, no DOM): pure helpers for the browser (window.HermesReminders)
 * and the node tests (require("../../static/reminders.js")):
 *
 *   shouldSpeakHere, parseHHMM, minutesOfDay, inQuietHours, dedupeSpoken,
 *   speechKey, normalizeDue, createTracker (repeat / ack state),
 *   deliveryPlan, noticeText, spokenText, notificationBody, awayText,
 *   validateAdd, validateSnooze, visibleReminders, errorKey, STRINGS,
 *   isRecurring, ruleText, describeRule, addBody, rowActions (repeating
 *   reminders: one series record with a rule; "Erledigt" completes one
 *   occurrence, "Nächste auslassen" skips one, "Serie beenden" stops it)
 *
 * Part 2 (browser only, needs window.Hermes from hermes.js):
 *   - the delivery ladder for `hub:reminder.due` (and `hub:checkin.meeting`):
 *     chime + speech on a "speak here" device, a chat notice with
 *     "Erledigt" / "Schlummern" buttons (private: text only on request and
 *     never stored), a system notification when the tab is hidden, and
 *     speech deferred to the first gesture while audio is still locked;
 *   - `hub:reminder.acked` stops repeats on every tab;
 *   - the hub tab "Erinnerungen" / "Reminders" (list, add, cancel, snooze,
 *     done) and the Settings section "Erinnerungen auf diesem Gerät".
 *
 * Reminder texts come from users (and calendar titles from third parties):
 * they are only ever set with textContent.
 */
(function (root, factory) {
  var api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root && typeof root === "object") root.HermesReminders = api;
})(typeof window !== "undefined" ? window : typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  var SPEAK_HERE_KEY = "hermes-speak-reminders"; // same key as hermes.js
  var SPOKEN_MAX = 50;
  var TEXT_MAX = 300; // reminder text cap in the add form
  var WHEN_MAX = 100;
  var SPEAK_MAX = 500;
  var REPEAT_MAX = 5; // SPEC 9.1: re-announced up to 5 times
  var KINDS = ["reminder", "timer", "meeting"];
  // kinds the user (or their calendar) asked for: quiet hours never mute them
  var EXPLICIT_KINDS = KINDS;
  var SNOOZE_CHOICES = [5, 10, 30];
  // the add form's repeat select; the server derives the rule from the due time
  var REPEAT_CHOICES = ["none", "daily", "weekdays", "weekly", "monthly", "yearly"];
  var RULE_FREQS = ["daily", "weekly", "monthly", "yearly"];
  var RULE_TEXT_MAX = 160;
  var MISSED_SPEAK_MAX = 3; // missed announcements spoken per burst; the rest are notices

  var STRINGS = {
    en: {
      tab: "Reminders",
      section: "Reminders on this device",
      eyeBanner: "REMINDER",
      missed: "Missed: {text}",
      privateNotice: "Reminder for {name} – tap “Show” to see it",
      privateNoticeAnon: "You have a reminder – tap “Show” to see it",
      privateBody: "You have a reminder.",
      privateBodyName: "{name}, you have a reminder.",
      timerDone: "Timer done",
      meeting: "Appointment",
      show: "Show",
      done: "Done",
      snooze: "Snooze",
      snoozeN: "{n} min",
      snoozeTitle: "Remind me again in {n} minutes",
      acked: "{text} · done",
      ackedSnooze: "{text} · again in {n} min",
      snoozed: "Okay, I will remind you again in {n} minutes.",
      awayOne: "While you were away: {text}",
      awayMany: "While you were away: {n} reminders",
      awayAction: "Play",
      revealFailed: "Could not load the reminder text.",
      ackFailed: "Could not reach HERMES. The reminder is still open.",
      // hub tab
      add: "Add",
      addText: "What?",
      addTextPlaceholder: "e.g. take the pizza out",
      addWhen: "When?",
      addWhenPlaceholder: "e.g. in 20 minutes, tomorrow at 9",
      added: "Reminder set: {when}",
      cancel: "Cancel",
      cancelTitle: "Cancel this reminder",
      cancelled: "Reminder cancelled.",
      due: "Due now",
      upcoming: "Upcoming",
      empty: "No reminders. Say “Hermes, remind me in 20 minutes to …” or add one here.",
      loading: "Loading …",
      reload: "Reload",
      household: "Household",
      privateText: "Private reminder",
      kind_reminder: "Reminder",
      kind_timer: "Timer",
      kind_meeting: "Appointment",
      errText: "Please enter what I should remind you of.",
      errTextLong: "That text is too long (max {n} characters).",
      errWhenEmpty: "Please enter when, e.g. “in 20 minutes”.",
      errWhen: "I did not understand that time. Try “in 20 minutes” or “tomorrow at 9”. It must be in the future and within a year.",
      errLocked: "Locked. Unlock with the household PIN to see personal reminders.",
      errReadOnly: "The reminders file was written by a newer HERMES; changes are blocked.",
      errLimit: "Too many reminders. Cancel some first.",
      errGone: "That reminder no longer exists.",
      errOffline: "Reminders are not available (server unreachable or too old).",
      errGeneric: "That did not work: {msg}",
      // settings section
      speakHere: "Announce reminders on this device",
      speakHereHint: "Plays a chime and speaks due reminders and timers here. On by default only on this computer (localhost).",
      leaderYes: "This tab announces reminders for this browser.",
      leaderOther: "Another HERMES tab in this browser announces reminders.",
      leaderNone: "Not connected to HERMES events yet. Reminders arrive once the page is connected.",
      speakOffNote: "Reminders show up here as notices only, without sound.",
      notifications: "System notifications",
      notifyHint: "When this tab is in the background, a due reminder also shows a system notification.",
      notifyGranted: "Allowed.",
      notifyDenied: "Blocked in the browser settings.",
      notifyAsk: "Allow notifications",
      notifyUnavailable: "Not available here (needs localhost or HTTPS).",
      memorySaver:
        "Chrome may put background tabs to sleep and then miss reminders. Add localhost under chrome://settings/performance to “Always keep these sites active”. See docs/reminders-and-lists.md in the HERMES repository.",
      quiet: "Quiet hours: {from}–{to}. Reminders and timers you set yourself and calendar check-ins are still announced; other nudges stay silent.",
      test: "Test",
      testLine: "This is how reminders sound on this device.",
      openTab: "Open reminders",
      cardSet: "Reminder set: {when}",
      cardTimer: "Timer runs until {when}",
      open: "Open",
      // repeating reminders
      repeat: "Repeat",
      repeat_none: "Once",
      repeat_daily: "Every day",
      repeat_weekdays: "Every weekday (Mon–Fri)",
      repeat_weekly: "Every week",
      repeat_monthly: "Every month",
      repeat_yearly: "Every year",
      repeats: "Repeats {rule}",
      nextTime: "next {when}",
      addedRepeat: "Repeating reminder set: {rule}. First one: {when}",
      cardRepeat: "Repeating reminder set: {rule}",
      skipNext: "Skip next",
      skipTitle: "Skip only the next one; the series continues",
      skipped: "Skipped. Next one: {when}",
      stopSeries: "Stop series",
      stopTitle: "Stop this repeating reminder for good",
      stopped: "Repeating reminder stopped.",
      errRule: "That repeat setting was not accepted.",
      errNotRecurring: "That reminder does not repeat.",
    },
    de: {
      tab: "Erinnerungen",
      section: "Erinnerungen auf diesem Gerät",
      eyeBanner: "ERINNERUNG",
      missed: "Verpasst: {text}",
      privateNotice: "Erinnerung für {name} – „Anzeigen“ antippen",
      privateNoticeAnon: "Du hast eine Erinnerung – „Anzeigen“ antippen",
      privateBody: "Du hast eine Erinnerung.",
      privateBodyName: "{name}, du hast eine Erinnerung.",
      timerDone: "Timer abgelaufen",
      meeting: "Termin",
      show: "Anzeigen",
      done: "Erledigt",
      snooze: "Schlummern",
      snoozeN: "{n} Min.",
      snoozeTitle: "In {n} Minuten noch einmal erinnern",
      acked: "{text} · erledigt",
      ackedSnooze: "{text} · in {n} Min. noch einmal",
      snoozed: "Okay, ich erinnere dich in {n} Minuten noch einmal.",
      awayOne: "Während du weg warst: {text}",
      awayMany: "Während du weg warst: {n} Erinnerungen",
      awayAction: "Abspielen",
      revealFailed: "Der Erinnerungstext konnte nicht geladen werden.",
      ackFailed: "HERMES ist nicht erreichbar. Die Erinnerung bleibt offen.",
      add: "Hinzufügen",
      addText: "Woran?",
      addTextPlaceholder: "z. B. die Pizza rausnehmen",
      addWhen: "Wann?",
      addWhenPlaceholder: "z. B. in 20 Minuten, morgen um 9",
      added: "Erinnerung gesetzt: {when}",
      cancel: "Löschen",
      cancelTitle: "Diese Erinnerung löschen",
      cancelled: "Erinnerung gelöscht.",
      due: "Jetzt fällig",
      upcoming: "Anstehend",
      empty: "Keine Erinnerungen. Sag „Hermes, erinnere mich in 20 Minuten an …“ oder lege hier eine an.",
      loading: "Lädt …",
      reload: "Neu laden",
      household: "Haushalt",
      privateText: "Private Erinnerung",
      kind_reminder: "Erinnerung",
      kind_timer: "Timer",
      kind_meeting: "Termin",
      errText: "Bitte gib ein, woran ich dich erinnern soll.",
      errTextLong: "Der Text ist zu lang (höchstens {n} Zeichen).",
      errWhenEmpty: "Bitte gib an, wann – z. B. „in 20 Minuten“.",
      errWhen: "Diese Zeitangabe habe ich nicht verstanden. Versuch „in 20 Minuten“ oder „morgen um 9“. Sie muss in der Zukunft und innerhalb eines Jahres liegen.",
      errLocked: "Gesperrt. Entsperre mit der Haushalts-PIN, um persönliche Erinnerungen zu sehen.",
      errReadOnly: "Die Erinnerungsdatei stammt von einem neueren HERMES; Änderungen sind gesperrt.",
      errLimit: "Zu viele Erinnerungen. Lösche zuerst einige.",
      errGone: "Diese Erinnerung gibt es nicht mehr.",
      errOffline: "Erinnerungen sind nicht verfügbar (Server nicht erreichbar oder zu alt).",
      errGeneric: "Das hat nicht geklappt: {msg}",
      speakHere: "Erinnerungen auf diesem Gerät ansagen",
      speakHereHint: "Spielt einen Hinweiston und spricht fällige Erinnerungen und Timer hier. Standardmäßig nur auf diesem Computer (localhost) an.",
      leaderYes: "Dieser Tab sagt die Erinnerungen für diesen Browser an.",
      leaderOther: "Ein anderer HERMES-Tab in diesem Browser sagt die Erinnerungen an.",
      leaderNone: "Noch nicht mit den HERMES-Ereignissen verbunden. Erinnerungen kommen an, sobald die Seite verbunden ist.",
      speakOffNote: "Erinnerungen erscheinen hier nur als Hinweis, ohne Ton.",
      notifications: "Systembenachrichtigungen",
      notifyHint: "Ist dieser Tab im Hintergrund, zeigt eine fällige Erinnerung zusätzlich eine Systembenachrichtigung.",
      notifyGranted: "Erlaubt.",
      notifyDenied: "In den Browser-Einstellungen blockiert.",
      notifyAsk: "Benachrichtigungen erlauben",
      notifyUnavailable: "Hier nicht verfügbar (braucht localhost oder HTTPS).",
      memorySaver:
        "Chrome legt Hintergrund-Tabs eventuell schlafen und verpasst dann Erinnerungen. Füge localhost unter chrome://settings/performance zu ‚Diese Websites immer aktiv lassen‘ hinzu. Siehe docs/reminders-and-lists.md im HERMES-Repository.",
      quiet: "Ruhezeit: {from}–{to}. Erinnerungen und Timer, die du selbst stellst, und Termin-Hinweise werden trotzdem angesagt; andere Hinweise bleiben stumm.",
      test: "Testen",
      testLine: "So klingen Erinnerungen auf diesem Gerät.",
      openTab: "Erinnerungen öffnen",
      cardSet: "Erinnerung gesetzt: {when}",
      cardTimer: "Timer läuft bis {when}",
      open: "Öffnen",
      repeat: "Wiederholen",
      repeat_none: "Einmalig",
      repeat_daily: "Jeden Tag",
      repeat_weekdays: "Jeden Werktag (Mo–Fr)",
      repeat_weekly: "Jede Woche",
      repeat_monthly: "Jeden Monat",
      repeat_yearly: "Jedes Jahr",
      repeats: "Wiederholt sich {rule}",
      nextTime: "als Nächstes {when}",
      addedRepeat: "Wiederkehrende Erinnerung gesetzt: {rule}. Das erste Mal: {when}",
      cardRepeat: "Wiederkehrende Erinnerung gesetzt: {rule}",
      skipNext: "Nächste auslassen",
      skipTitle: "Nur die nächste auslassen, die Serie läuft weiter",
      skipped: "Ausgelassen. Das nächste Mal: {when}",
      stopSeries: "Serie beenden",
      stopTitle: "Diese wiederkehrende Erinnerung ganz beenden",
      stopped: "Wiederkehrende Erinnerung beendet.",
      errRule: "Diese Wiederholung wurde nicht angenommen.",
      errNotRecurring: "Diese Erinnerung wiederholt sich nicht.",
    },
  };

  // ------------------------------------------------------------ small utils

  function format(str, vars) {
    str = String(str == null ? "" : str);
    if (!vars) return str;
    return str.replace(/\{(\w+)\}/g, function (all, k) {
      return Object.prototype.hasOwnProperty.call(vars, k) && vars[k] != null ? String(vars[k]) : all;
    });
  }

  /* tr(key, vars) over STRINGS for a language getter (fallback when Hermes.strings is missing). */
  function makeTr(dict, langFn) {
    return function (key, vars) {
      var lang = (typeof langFn === "function" ? langFn() : langFn) === "de" ? "de" : "en";
      var t = dict[lang] || {};
      var s = Object.prototype.hasOwnProperty.call(t, key) ? t[key] : Object.prototype.hasOwnProperty.call(dict.en, key) ? dict.en[key] : key;
      return format(s, vars);
    };
  }

  /* Clean a server string: string only, no C0 controls except \n\t, capped in code points. */
  function cleanStr(v, max) {
    if (typeof v !== "string") return "";
    // eslint-disable-next-line no-control-regex
    var s = v.replace(/[\u0000-\u0008\u000b-\u001f\u007f]/g, "").trim();
    var cps = Array.from(s);
    return cps.length > max ? cps.slice(0, max).join("") : s;
  }

  function runeLen(s) {
    return Array.from(String(s || "")).length;
  }

  // ------------------------------------------------------------ speak here

  /*
   * Should this device announce reminders? `pref` is the stored value of
   * localStorage "hermes-speak-reminders" ("true"/"false", booleans, or
   * null when never set). Unset means: on for localhost, off on the LAN
   * (SPEC 8.9). Matches hermes.js speakHerePref.
   */
  function shouldSpeakHere(isLocalhost, pref) {
    if (pref === true || pref === "true" || pref === "1") return true;
    if (pref === false || pref === "false" || pref === "0") return false;
    return !!isLocalhost;
  }

  // ------------------------------------------------------------ quiet hours

  /* "HH:MM" → minutes since midnight, or null when invalid. */
  function parseHHMM(s) {
    var m = /^\s*(\d{1,2}):(\d{2})\s*$/.exec(String(s == null ? "" : s));
    if (!m) return null;
    var h = +m[1];
    var min = +m[2];
    if (h > 23 || min > 59) return null;
    return h * 60 + min;
  }

  /*
   * Minutes since midnight of `now` (Date or ms) in time zone `tz` (IANA;
   * "" or invalid → the browser's local time). null for an invalid time.
   */
  function minutesOfDay(now, tz) {
    var d = now instanceof Date ? now : new Date(typeof now === "number" ? now : NaN);
    if (!isFinite(d.getTime())) return null;
    if (tz && typeof Intl !== "undefined" && Intl.DateTimeFormat) {
      try {
        var parts = new Intl.DateTimeFormat("en-GB", { timeZone: tz, hour: "2-digit", minute: "2-digit", hourCycle: "h23" }).formatToParts(d);
        var h = NaN;
        var mi = NaN;
        for (var i = 0; i < parts.length; i++) {
          if (parts[i].type === "hour") h = +parts[i].value;
          else if (parts[i].type === "minute") mi = +parts[i].value;
        }
        if (isFinite(h) && isFinite(mi)) return (h % 24) * 60 + mi;
      } catch (e) {
        /* unknown zone: local time */
      }
    }
    return d.getHours() * 60 + d.getMinutes();
  }

  /*
   * Is `now` inside the quiet hours [start, end)? Both are "HH:MM"; a range
   * with start > end runs across midnight (22:00–07:00). start == end or an
   * invalid value means no quiet hours.
   */
  function inQuietHours(now, start, end, tz) {
    var s = parseHHMM(start);
    var e = parseHHMM(end);
    if (s == null || e == null || s === e) return false;
    var m = minutesOfDay(now, tz);
    if (m == null) return false;
    if (s < e) return m >= s && m < e;
    return m >= s || m < e;
  }

  // ------------------------------------------------------------ dedupe

  /*
   * Remember `key` in a bounded list (localStorage "hermes-spoken-ids").
   * Returns {list, fresh}; fresh is false when the key was already there.
   */
  function dedupeSpoken(list, key, max) {
    max = max > 0 ? max : SPOKEN_MAX;
    var out = Array.isArray(list)
      ? list.filter(function (x) {
          return typeof x === "string" || typeof x === "number";
        })
      : [];
    var k = String(key);
    for (var i = 0; i < out.length; i++) {
      if (String(out[i]) === k) return { list: out, fresh: false };
    }
    out.push(k);
    if (out.length > max) out = out.slice(out.length - max);
    return { list: out, fresh: true };
  }

  /*
   * Speech dedupe key of a due event: one announcement per reminder, firing
   * (due time: a snoozed reminder fires again under the same id) and repeat.
   */
  function speechKey(due) {
    if (!due) return "";
    if (due.reminderId) {
      var t = Date.parse(due.due);
      return "rem:" + due.reminderId + ":" + (isFinite(t) ? Math.floor(t / 1000) : "") + ":" + due.repeat;
    }
    return due.eventId ? "rem:e" + due.eventId + ":" + due.repeat : "";
  }

  // ------------------------------------------------------------ events

  var REMINDER_ID = /^r_[A-Za-z0-9]{1,40}$/;

  /*
   * Validate a `reminder.due` hub event (SPEC 3.2) into a plain object, or
   * null when it is unusable. Every field is type-checked and capped.
   */
  function normalizeDue(ev) {
    if (!ev || typeof ev !== "object") return null;
    var d = ev.data && typeof ev.data === "object" && !Array.isArray(ev.data) ? ev.data : null;
    if (!d) return null;
    var rid = typeof d.reminderId === "string" && REMINDER_ID.test(d.reminderId) ? d.reminderId : "";
    var kind = KINDS.indexOf(d.kind) >= 0 ? d.kind : "reminder";
    var repeat = typeof d.repeat === "number" && isFinite(d.repeat) ? Math.max(0, Math.min(REPEAT_MAX, Math.floor(d.repeat))) : 0;
    var out = {
      eventId: typeof ev.id === "number" && ev.id > 0 && isFinite(ev.id) ? ev.id : 0,
      reminderId: rid,
      kind: kind,
      text: cleanStr(d.text, SPEAK_MAX),
      speak: cleanStr(d.speak, SPEAK_MAX),
      personName: cleanStr(d.personName, 40),
      due: typeof d.due === "string" ? d.due.slice(0, 40) : "",
      lang: d.lang === "de" ? "de" : d.lang === "en" ? "en" : "",
      private: d.private === true,
      repeat: repeat,
      chime: d.chime !== false,
      clientId: typeof d.clientId === "string" ? d.clientId.slice(0, 40) : "",
      missed: ev.missed === true,
      recurring: d.recurring === true,
      noSpeaker: d.noSpeaker === true,
      ruleText: cleanStr(d.ruleText, RULE_TEXT_MAX),
    };
    // a private event never carries its text (SPEC 3.2); drop it if it does
    if (out.private) {
      out.text = "";
      out.ruleText = "";
    }
    if (!out.speak && !out.text && !out.private) return null;
    return out;
  }

  // ------------------------------------------------------------ repeat / ack state

  /*
   * Per-tab state of fired reminders. The server re-announces an un-acked
   * reminder every 60 s (repeat 1..5); an ack on any device publishes
   * `reminder.acked`. The tracker makes sure that
   *   - a reminder acked here is not announced again even when a repeat was
   *     already on the wire,
   *   - an older or equal repeat (replay after a reconnect) is not announced
   *     twice,
   *   - a snoozed reminder (same id, new due time) fires again, even when a
   *     late `reminder.acked` of the old firing arrives after the snooze,
   *   - "stop" knows which reminder is open.
   * State is kept per firing cycle (reminderId + due time). Times are
   * injected (ms) so tests need no clock.
   */
  function createTracker(opts) {
    opts = opts || {};
    var ttl = opts.ttlMs > 0 ? opts.ttlMs : 60 * 60 * 1000;
    var items = {}; // "id|due" → {id, cycle, repeat, firedAt, lastAt, acked, kind}
    var cur = {}; // reminderId → newest cycle key

    function gc(now) {
      for (var k in items) {
        if (!Object.prototype.hasOwnProperty.call(items, k) || now - items[k].lastAt <= ttl) continue;
        if (cur[items[k].id] === k) delete cur[items[k].id];
        delete items[k];
      }
    }
    function current(id) {
      var k = id ? cur[id] : "";
      return k ? items[k] || null : null;
    }

    return {
      /*
       * A due event arrived. Returns true when it should be announced,
       * false when it is stale (acked here, or a repeat we already had).
       */
      due: function (due, now) {
        if (!due) return false;
        gc(now);
        if (!due.reminderId) return true; // cannot track: announce once (dedupe by event id elsewhere)
        var key = due.reminderId + "|" + (due.due || "");
        var it = items[key];
        if (it) {
          if (it.acked) return false;
          if (due.repeat <= it.repeat) return false;
          it.repeat = due.repeat;
          it.lastAt = now;
          return true;
        }
        items[key] = { id: due.reminderId, cycle: key, repeat: due.repeat, firedAt: now, lastAt: now, acked: false, kind: due.kind };
        cur[due.reminderId] = key;
        return true;
      },
      /* Mark the current firing acknowledged (done, snoozed, cancelled or acked elsewhere). */
      ack: function (id, now) {
        var it = current(id);
        if (!it || it.acked) return false;
        it.acked = true;
        if (now) it.lastAt = now;
        return true;
      },
      /* Undo a local ack (the server did not take it): repeats come through again. */
      reopen: function (id) {
        var it = current(id);
        if (!it || !it.acked) return false;
        it.acked = false;
        return true;
      },
      isAcked: function (id) {
        var it = current(id);
        return !!(it && it.acked);
      },
      /* Fired, un-acked reminders, newest first. */
      open: function (now) {
        gc(now);
        var out = [];
        for (var k in items) if (Object.prototype.hasOwnProperty.call(items, k) && !items[k].acked) out.push(items[k]);
        out.sort(function (a, b) {
          return b.lastAt - a.lastAt;
        });
        return out;
      },
      /* Can the reminder still repeat (the server repeats only reminders and timers)? */
      willRepeat: function (id) {
        var it = current(id);
        return !!it && !it.acked && it.kind !== "meeting" && it.repeat < REPEAT_MAX;
      },
      size: function () {
        return Object.keys(items).length;
      },
    };
  }

  // ------------------------------------------------------------ delivery ladder

  /*
   * Decide what to do with one due event on this tab (SPEC 5.5).
   *   ctx.claimed        this tab won the claim for the event (leader / first tab)
   *   ctx.fresh          the tracker says it is new (not acked, not a stale repeat)
   *   ctx.speakHere      the device's "speak here" switch
   *   ctx.hidden         document.hidden
   *   ctx.notifyGranted  Notification.permission === "granted" in a secure context
   *   ctx.gesture        audio unlocked (a user gesture was seen)
   *   ctx.quiet          inside quiet hours
   *   ctx.missedSpoken   missed events already spoken in this burst
   * Returns {notice, speak, chime, defer, notify, ack} (all booleans).
   *   notice  add (repeat 0) or refresh (repeat > 0) the chat notice
   *   speak   chime/speak now; defer: queue the speech for the first gesture
   *   notify  show a system notification
   *   ack     ack the durable hub event (it was delivered audibly or visibly)
   */
  function deliveryPlan(due, ctx) {
    var plan = { notice: false, speak: false, chime: false, defer: false, notify: false, ack: false };
    if (!due || !ctx || !ctx.claimed || !ctx.fresh) return plan;
    plan.notice = true;
    // no device in the household speaks reminders: the tab that claimed it does
    var audible = !!ctx.speakHere || due.noSpeaker === true;
    // quiet hours never mute what the user (or their calendar) asked for
    if (ctx.quiet && EXPLICIT_KINDS.indexOf(due.kind) < 0) audible = false;
    if (due.missed && (ctx.missedSpoken || 0) >= MISSED_SPEAK_MAX) audible = false;
    if (audible) {
      if (ctx.gesture) {
        plan.speak = true;
        plan.chime = due.chime !== false;
      } else {
        plan.defer = true;
      }
    }
    plan.notify = !!(ctx.hidden && ctx.notifyGranted);
    // ack the hub event only when someone could actually notice it here;
    // otherwise keep it durable so a speaking device replays it later
    plan.ack = !!due.eventId && (plan.speak || plan.defer || plan.notify || !ctx.hidden);
    return plan;
  }

  /* The line spoken for an event: the server's sentence, "Verpasst:" when missed. */
  function spokenText(due, tr) {
    if (!due) return "";
    var s = due.speak || due.text;
    if (!s && due.private) s = due.personName ? tr("privateBodyName", { name: due.personName }) : tr("privateBody");
    if (!s) return "";
    return due.missed ? tr("missed", { text: s }) : s;
  }

  /* The chat notice text. Private: a generic line, the text comes on request. */
  function noticeText(due, tr) {
    if (!due) return "";
    var s;
    if (due.private) {
      s = due.personName ? tr("privateNotice", { name: due.personName }) : tr("privateNoticeAnon");
    } else {
      s = due.text || due.speak || (due.kind === "timer" ? tr("timerDone") : "");
    }
    return due.missed ? tr("missed", { text: s }) : s;
  }

  /* System notification body. Private: generic (lock screens are public). */
  function notificationBody(due, tr) {
    if (!due) return "";
    if (due.private) return tr("privateBody");
    var s = due.text || due.speak || (due.kind === "timer" ? tr("timerDone") : "");
    return due.missed ? tr("missed", { text: s }) : s;
  }

  /* Banner text for speech waiting on the first gesture. */
  function awayText(list, tr) {
    var n = Array.isArray(list) ? list.length : 0;
    if (!n) return "";
    if (n === 1) return tr("awayOne", { text: list[0].private ? tr("privateBody") : list[0].text || list[0].speak || tr("timerDone") });
    return tr("awayMany", { n: n });
  }

  // ------------------------------------------------------------ hub tab

  /* Validate the add form; returns {ok, key, vars, text, when[, repeat]}. */
  function validateAdd(text, when, repeat) {
    var t = cleanStr(String(text == null ? "" : text), TEXT_MAX + 1);
    var w = cleanStr(String(when == null ? "" : when), WHEN_MAX + 1);
    if (!t) return { ok: false, key: "errText" };
    if (runeLen(t) > TEXT_MAX) return { ok: false, key: "errTextLong", vars: { n: TEXT_MAX } };
    if (!w) return { ok: false, key: "errWhenEmpty" };
    if (runeLen(w) > WHEN_MAX) return { ok: false, key: "errWhen" };
    var out = { ok: true, text: t, when: w };
    // "none" (and anything unknown) leaves the repeat to the words in "when"
    if (typeof repeat === "string" && repeat !== "none" && REPEAT_CHOICES.indexOf(repeat) >= 0) out.repeat = repeat;
    return out;
  }

  /* The POST /api/reminders body for a validated add form. */
  function addBody(v) {
    if (!v || !v.ok) return null;
    var b = { text: v.text, when: v.when };
    if (v.repeat) b.repeat = v.repeat;
    return b;
  }

  /* Does this server row (ReminderView) repeat? */
  function isRecurring(r) {
    return !!(r && r.rule && typeof r.rule === "object" && !Array.isArray(r.rule) && RULE_FREQS.indexOf(r.rule.freq) >= 0);
  }

  function ordinalEN(n) {
    var t = n % 100;
    var suf = t >= 11 && t <= 13 ? "th" : n % 10 === 1 ? "st" : n % 10 === 2 ? "nd" : n % 10 === 3 ? "rd" : "th";
    return n + suf;
  }

  function pad2(n) {
    return (n < 10 ? "0" : "") + n;
  }

  var WEEKDAYS = {
    en: ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"],
    de: ["Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"],
  };
  var MONTHS = {
    en: ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"],
    de: ["Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"],
  };

  function intIn(v, lo, hi) {
    return typeof v === "number" && isFinite(v) && Math.floor(v) === v && v >= lo && v <= hi;
  }

  /*
   * Local summary of a structured rule, for rows without the server's
   * ruleText (normally the server sends it in the UI language). "" when the
   * rule is unusable.
   */
  function describeRule(rule, lang) {
    if (!rule || typeof rule !== "object" || RULE_FREQS.indexOf(rule.freq) < 0) return "";
    var de = lang === "de";
    var h = intIn(rule.hour, 0, 23) ? rule.hour : 0;
    var mi = intIn(rule.minute, 0, 59) ? rule.minute : 0;
    var clock = de ? (mi ? h + ":" + pad2(mi) : String(h)) + " Uhr" : ((h + 11) % 12) + 1 + ":" + pad2(mi) + (h < 12 ? " AM" : " PM");
    var n = intIn(rule.interval, 1, 366) ? rule.interval : 1;
    var s;
    if (rule.freq === "daily") {
      s = n === 1 ? (de ? "jeden Tag" : "every day") : de ? "alle " + n + " Tage" : "every " + n + " days";
    } else if (rule.freq === "weekly") {
      var days = (Array.isArray(rule.weekdays) ? rule.weekdays : []).filter(function (d) {
        return intIn(d, 0, 6);
      });
      if (!days.length) return "";
      var names = days.map(function (d) {
        return WEEKDAYS[de ? "de" : "en"][d];
      });
      var list = names.length > 1 ? names.slice(0, -1).join(", ") + (de ? " und " : " and ") + names[names.length - 1] : names[0];
      if (days.join() === "1,2,3,4,5" && n === 1) s = de ? "jeden Werktag" : "every weekday";
      else if (n === 1) s = (de ? "jeden " : "every ") + list;
      else s = de ? "alle " + n + " Wochen am " + list : "every " + n + " weeks on " + list;
    } else if (rule.freq === "monthly") {
      if (!intIn(rule.monthDay, 1, 31)) return "";
      var d = rule.monthDay;
      var day = d >= 31 ? (de ? "am Monatsletzten" : "on the last day") : de ? "am " + d + "." : "on the " + ordinalEN(d);
      s = de ? (n === 1 ? "jeden Monat " : "alle " + n + " Monate ") + day : n === 1 ? day + " of every month" : "every " + n + " months " + day;
    } else {
      if (!intIn(rule.month, 1, 12) || !intIn(rule.monthDay, 1, 31)) return "";
      var date = de ? "am " + rule.monthDay + ". " + MONTHS.de[rule.month - 1] : "on " + MONTHS.en[rule.month - 1] + " " + rule.monthDay;
      s = (n === 1 ? (de ? "jedes Jahr " : "every year ") : de ? "alle " + n + " Jahre " : "every " + n + " years ") + date;
    }
    return s + (de ? " um " : " at ") + clock;
  }

  /* The repeat summary of a row: the server's ruleText, else a local one. */
  function ruleText(r, lang) {
    if (!isRecurring(r)) return "";
    return cleanStr(r.ruleText, RULE_TEXT_MAX) || describeRule(r.rule, lang);
  }

  /*
   * The buttons of a row in the Reminders tab, as action keys:
   *   "done", "snooze:<min>", "cancel" (one-off), "skip", "stop" (series).
   * A ringing occurrence is completed with "done" (the series moves on); a
   * waiting series offers "skip" (only the next one) and "stop".
   */
  function rowActions(r, fired) {
    var kind = r && KINDS.indexOf(r.kind) >= 0 ? r.kind : "reminder";
    var rec = isRecurring(r);
    var out = [];
    if (fired) out.push("done");
    if (kind !== "meeting" && (fired || !rec)) {
      SNOOZE_CHOICES.forEach(function (n) {
        out.push("snooze:" + n);
      });
    }
    if (rec) {
      if (!fired) out.push("skip");
      out.push("stop");
    } else {
      out.push("cancel");
    }
    return out;
  }

  /* Snooze minutes: an integer 1..240 (SPEC 3.5), else 0. */
  function validateSnooze(n) {
    var v = typeof n === "string" ? Number(n) : n;
    if (typeof v !== "number" || !isFinite(v) || Math.floor(v) !== v || v < 1 || v > 240) return 0;
    return v;
  }

  function dueMs(r) {
    var t = Date.parse(r && r.due);
    return isFinite(t) ? t : Infinity;
  }

  /*
   * The tab shows fired (un-acked) reminders first, then pending ones, each
   * by due time. done and cancelled are hidden. Malformed rows are dropped.
   */
  function visibleReminders(list) {
    if (!Array.isArray(list)) return { fired: [], pending: [] };
    var fired = [];
    var pending = [];
    list.forEach(function (r) {
      if (!r || typeof r !== "object" || typeof r.id !== "string" || !REMINDER_ID.test(r.id)) return;
      if (r.status === "fired") fired.push(r);
      else if (r.status === "pending") pending.push(r);
    });
    var by = function (a, b) {
      return dueMs(a) - dueMs(b);
    };
    fired.sort(by);
    pending.sort(by);
    return { fired: fired, pending: pending };
  }

  /* Map a fetchJSON error to a STRINGS key. */
  function errorKey(err) {
    if (!err) return "errGeneric";
    var code = err.code || "";
    var st = err.status || 0;
    if (code === "when") return "errWhen";
    if (code === "rule") return "errRule";
    if (code === "not_recurring") return "errNotRecurring";
    if (code === "locked" || st === 403) return "errLocked";
    if (code === "limit" || st === 409) return "errLimit";
    if (st === 404) return "errGone";
    if (st === 503 && /read-?only|newer/i.test(String(err.message || ""))) return "errReadOnly";
    if (st === 503 || st === 405 || st === 0) return "errOffline";
    return "errGeneric";
  }

  return {
    SPEAK_HERE_KEY: SPEAK_HERE_KEY,
    TEXT_MAX: TEXT_MAX,
    WHEN_MAX: WHEN_MAX,
    REPEAT_MAX: REPEAT_MAX,
    KINDS: KINDS,
    SNOOZE_CHOICES: SNOOZE_CHOICES,
    REPEAT_CHOICES: REPEAT_CHOICES,
    MISSED_SPEAK_MAX: MISSED_SPEAK_MAX,
    STRINGS: STRINGS,
    format: format,
    makeTr: makeTr,
    cleanStr: cleanStr,
    shouldSpeakHere: shouldSpeakHere,
    parseHHMM: parseHHMM,
    minutesOfDay: minutesOfDay,
    inQuietHours: inQuietHours,
    dedupeSpoken: dedupeSpoken,
    speechKey: speechKey,
    normalizeDue: normalizeDue,
    createTracker: createTracker,
    deliveryPlan: deliveryPlan,
    spokenText: spokenText,
    noticeText: noticeText,
    notificationBody: notificationBody,
    awayText: awayText,
    validateAdd: validateAdd,
    addBody: addBody,
    isRecurring: isRecurring,
    describeRule: describeRule,
    ruleText: ruleText,
    rowActions: rowActions,
    validateSnooze: validateSnooze,
    visibleReminders: visibleReminders,
    errorKey: errorKey,
  };
});

/* ====================================================================== */
/* Browser UI                                                              */
/* ====================================================================== */
(function () {
  "use strict";
  if (typeof window === "undefined" || typeof document === "undefined") return;
  var H = window.Hermes;
  var R = window.HermesReminders;
  if (!H || !R) return; // hermes.js missing: the page works without reminders

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
  var tr = typeof H.strings === "function" ? H.strings(R.STRINGS) : R.makeTr(R.STRINGS, lang);

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
  function isLocalhost() {
    try {
      if (typeof api().isLocalhost === "function") return !!api().isLocalhost();
    } catch (e) {
      /* fall through */
    }
    return /^(localhost|127\.|\[::1\])/.test(location.host);
  }
  function isSecure() {
    return !!window.isSecureContext;
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
      /* blocked storage: the switch lasts for this page only */
    }
  }
  var speakOverride = null; // when storage is blocked
  function speakHere() {
    if (speakOverride !== null) return speakOverride;
    return R.shouldSpeakHere(isLocalhost(), storeGet(R.SPEAK_HERE_KEY));
  }
  function setSpeakHere(on) {
    speakOverride = null;
    storeSet(R.SPEAK_HERE_KEY, on ? "true" : "false");
    if (storeGet(R.SPEAK_HERE_KEY) !== (on ? "true" : "false")) speakOverride = !!on;
    // the server counts speak-here subscribers (delivery check): reconnect
    try {
      if (H.events && typeof H.events.reconnect === "function") H.events.reconnect();
    } catch (e) {
      /* ignore */
    }
  }
  function gestureSeen() {
    try {
      return typeof api().gestureSeen === "function" ? !!api().gestureSeen() : true;
    } catch (e) {
      return true;
    }
  }
  function notifyGranted() {
    return isSecure() && typeof window.Notification === "function" && window.Notification.permission === "granted";
  }
  function inQuiet() {
    var s = settings();
    return R.inQuietHours(new Date(), s.quietStart == null ? "22:00" : s.quietStart, s.quietEnd == null ? "07:00" : s.quietEnd, s.timezone || "");
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
  function errText(err) {
    var key = R.errorKey(err);
    return key === "errGeneric" ? tr(key, { msg: (err && err.message) || "?" }) : tr(key);
  }

  // ---------------------------------------------------------------- DOM helpers

  function el(tag, attrs, kids) {
    var e = document.createElement(tag);
    if (attrs) {
      for (var k in attrs) {
        if (!Object.prototype.hasOwnProperty.call(attrs, k)) continue;
        var v = attrs[k];
        if (v == null || v === false) continue;
        if (k === "class") e.className = v;
        else if (k === "text") e.textContent = v;
        else if (k.slice(0, 2) === "on" && typeof v === "function") e.addEventListener(k.slice(2), v);
        else if (k === "hidden") e.hidden = !!v;
        else if (k === "disabled") e.disabled = !!v;
        else if (k === "checked") e.checked = !!v;
        else if (k === "value") e.value = v;
        else e.setAttribute(k, v === true ? "" : v);
      }
    }
    if (kids != null) {
      (Array.isArray(kids) ? kids : [kids]).forEach(function (c) {
        if (c == null || c === false) return;
        e.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
      });
    }
    return e;
  }
  function clear(node) {
    while (node && node.firstChild) node.removeChild(node.firstChild);
  }

  // ---------------------------------------------------------------- delivery

  var tracker = R.createTracker();
  var notices = {}; // reminderId → {handle, due, revealed}
  var deferred = []; // dues waiting for the first gesture
  var awayToast = null;
  var speaking = null; // reminderId being announced right now
  var chain = Promise.resolve(); // announcements run one after another
  var missedSpoken = 0;
  var missedAt = 0; // the burst of missed announcements resets after 10 min
  var replyBusy = false;

  H.on("reply:start", function () {
    replyBusy = true;
  });
  H.on("reply:done", function () {
    replyBusy = false;
  });

  function claim(due, ev) {
    var key = R.speechKey(due) || (ev && ev.id ? "rem:e" + ev.id : "");
    if (!key) return false;
    try {
      if (H.events && typeof H.events.claim === "function") return !!H.events.claim(key);
    } catch (e) {
      /* fall through */
    }
    return true;
  }

  function ackHubEvent(id) {
    if (!(id > 0)) return;
    fetchJSON("/api/events/ack", { method: "POST", body: { ids: [id] } }).catch(function () {
      /* replay will bring it back; the tracker drops the duplicate */
    });
  }

  function onDue(ev) {
    var due = R.normalizeDue(ev);
    if (!due) return;
    var fresh = tracker.due(due, nowMs());
    var claimed = fresh && claim(due, ev);
    var plan = R.deliveryPlan(due, {
      claimed: claimed,
      fresh: fresh,
      speakHere: speakHere(),
      hidden: document.visibilityState === "hidden",
      notifyGranted: notifyGranted(),
      gesture: gestureSeen(),
      quiet: inQuiet(),
      missedSpoken: nowMs() - missedAt > 10 * 60 * 1000 ? 0 : missedSpoken,
    });
    if (plan.notice) showNotice(due);
    if (plan.notify) showSystemNotification(due);
    if (plan.speak) announce(due);
    if (plan.defer) deferSpeech(due);
    if (plan.ack) ackHubEvent(due.eventId);
    H.emit("reminders:due", { reminderId: due.reminderId, kind: due.kind, repeat: due.repeat });
    if (view && view.visible) loadList();
  }

  function noticeActions(due) {
    if (!due.reminderId || tracker.isAcked(due.reminderId)) return [];
    var acts = [];
    var n = notices[due.reminderId];
    if (due.private && !(n && n.revealed)) {
      acts.push({
        label: tr("show"),
        fn: function () {
          reveal(due.reminderId);
        },
      });
    }
    acts.push({
      label: tr("done"),
      title: tr("done"),
      fn: function () {
        markDone(due.reminderId, due.due);
      },
    });
    if (due.kind !== "meeting") {
      acts.push({
        label: tr("snooze") + " " + tr("snoozeN", { n: 10 }),
        title: tr("snoozeTitle", { n: 10 }),
        fn: function () {
          snooze(due.reminderId, 10);
        },
      });
    }
    return acts;
  }

  function showNotice(due) {
    if (typeof api().addNotice !== "function") return;
    var text = R.noticeText(due, tr);
    if (due.ruleText) text += " · " + tr("repeats", { rule: due.ruleText });
    var existing = due.reminderId ? notices[due.reminderId] : null;
    // the next occurrence of a series (same id, new due time) is a new notice
    if (existing && existing.due && existing.due.due !== due.due) existing = null;
    if (existing && existing.handle) {
      // a repeat: refresh the notice instead of stacking a new one
      existing.due = due;
      existing.settled = false;
      if (!existing.revealed) existing.text = text;
      try {
        existing.handle.update(existing.text, noticeActions(due));
      } catch (e) {
        /* ignore */
      }
      return;
    }
    var kind = due.private ? "lock" : due.kind === "timer" ? "timer" : due.kind === "meeting" ? "meeting" : "reminder";
    var rec = { handle: null, due: due, revealed: false, text: text };
    if (due.reminderId) notices[due.reminderId] = rec;
    try {
      // private notices are ephemeral: the revealed text must never reach
      // the stored conversation, so the whole notice stays in memory only
      rec.handle = api().addNotice(text, { kind: kind, actions: noticeActions(due), ephemeral: !!due.private });
    } catch (e) {
      rec.handle = null;
    }
  }

  function settleNotice(id, snoozeMin) {
    var n = notices[id];
    if (!n || !n.handle || n.settled) return;
    n.settled = true;
    try {
      // keep the shown text (it may be the revealed private one), mark it done, drop the buttons
      n.handle.update(snoozeMin ? tr("ackedSnooze", { text: n.text, n: snoozeMin }) : tr("acked", { text: n.text }), []);
    } catch (e) {
      /* ignore */
    }
  }

  function reveal(id, retried) {
    var n = notices[id];
    if (!n) return;
    fetchJSON("/api/reminders/" + encodeURIComponent(id))
      .then(function (r) {
        var view = r && r.reminder && typeof r.reminder === "object" ? r.reminder : r;
        var text = R.cleanStr(view && view.text, 500);
        if (!text) throw new Error("empty");
        n.revealed = true;
        n.text = n.due.missed ? tr("missed", { text: text }) : text;
        if (n.handle) n.handle.update(n.text, noticeActions(n.due));
      })
      .catch(function (err) {
        // Personal lock: ask for the PIN once, then try again.
        if (!retried && err && err.code === "locked" && typeof api().requestUnlock === "function") {
          api()
            .requestUnlock()
            .then(function (ok) {
              if (ok) reveal(id, true);
            });
          return;
        }
        toast(err && err.status ? errText(err) : tr("revealFailed"), { type: "warn", key: "rem-reveal" });
      });
  }

  function acked(id, snoozeMin) {
    if (!id) return;
    tracker.ack(id, nowMs());
    settleNotice(id, snoozeMin);
    deferred = deferred.filter(function (d) {
      return d.reminderId !== id;
    });
    updateAwayToast();
    if (speaking === id && typeof api().stopSpeaking === "function") {
      try {
        api().stopSpeaking();
      } catch (e) {
        /* ignore */
      }
    }
  }

  /*
   * Acknowledge a reminder. `due` names the occurrence that was shown, so a
   * stale button of an earlier occurrence never completes the next one.
   */
  function markDone(id, due) {
    if (!id) return Promise.resolve(false);
    acked(id); // optimistic: no repeat races the request
    var body = typeof due === "string" && due ? { due: due } : {};
    return fetchJSON("/api/reminders/" + encodeURIComponent(id) + "/done", { method: "POST", body: body })
      .then(function () {
        if (view && view.visible) loadList();
        return true;
      })
      .catch(function (err) {
        if (err && err.status === 404) return true; // gone already
        tracker.reopen(id); // still open on the server: let repeats through again
        restoreNotice(id);
        toast(err && err.status ? errText(err) : tr("ackFailed"), { type: "warn", key: "rem-ack" });
        return false;
      });
  }

  function snooze(id, minutes) {
    var m = R.validateSnooze(minutes);
    if (!id || !m) return Promise.resolve(false);
    acked(id, m);
    return fetchJSON("/api/reminders/" + encodeURIComponent(id) + "/snooze", { method: "POST", body: { minutes: m } })
      .then(function () {
        // the next firing has a new due time: the tracker sees a new cycle
        delete notices[id];
        toast(tr("snoozed", { n: m }), { type: "success", key: "rem-snooze" });
        if (view && view.visible) loadList();
        return true;
      })
      .catch(function (err) {
        if (err && err.status !== 404) {
          tracker.reopen(id);
          restoreNotice(id);
        }
        toast(errText(err), { type: "warn", key: "rem-snooze" });
        return false;
      });
  }

  function restoreNotice(id) {
    var n = notices[id];
    if (!n || !n.handle) return;
    // the tracker was reopened first, so the buttons come back
    n.settled = false;
    try {
      n.handle.update(n.text, noticeActions(n.due));
    } catch (e) {
      /* ignore */
    }
  }

  /* Wait (bounded) until HERMES is not talking or answering, so a reminder does not cut a reply. */
  function waitQuiet(maxMs) {
    var until = nowMs() + maxMs;
    return new Promise(function (resolve) {
      (function poll() {
        var busy = false;
        try {
          busy = replyBusy || (typeof api().isSpeaking === "function" && api().isSpeaking());
        } catch (e) {
          busy = false;
        }
        if (!busy || nowMs() >= until) resolve();
        else setTimeout(poll, 250);
      })();
    });
  }

  function announce(due) {
    if (due.missed) {
      if (nowMs() - missedAt > 10 * 60 * 1000) missedSpoken = 0;
      missedSpoken++;
      missedAt = nowMs();
    }
    chain = chain
      .then(function () {
        return waitQuiet(20000);
      })
      .then(function () {
        if (due.reminderId && tracker.isAcked(due.reminderId)) return null;
        var a = api();
        try {
          if (typeof a.flashEye === "function") a.flashEye("speaking", tr("eyeBanner"), 4000);
        } catch (e) {
          /* ignore */
        }
        var c = due.chime !== false && typeof a.chime === "function" ? a.chime() : null;
        return Promise.resolve(c)
          .catch(function () {})
          .then(function () {
            if (due.reminderId && tracker.isAcked(due.reminderId)) return null;
            if (typeof a.say !== "function") return null;
            speaking = due.reminderId || null;
            return a.say(R.spokenText(due, tr), { force: true });
          });
      })
      .catch(function () {
        /* speech failed: the notice is there */
      })
      .then(function () {
        speaking = null;
      });
  }

  function deferSpeech(due) {
    deferred = deferred.filter(function (d) {
      return !(due.reminderId && d.reminderId === due.reminderId);
    });
    deferred.push(due);
    if (deferred.length > 10) deferred = deferred.slice(-10);
    updateAwayToast();
  }

  function updateAwayToast() {
    var text = R.awayText(deferred, tr);
    if (!text) {
      if (awayToast && awayToast.close) awayToast.close(true);
      awayToast = null;
      return;
    }
    awayToast = toast(text, {
      type: "info",
      key: "rem-away",
      ms: 0,
      action: { label: tr("awayAction"), fn: playDeferred },
    });
  }

  function playDeferred() {
    var list = deferred;
    deferred = [];
    updateAwayToast();
    list.forEach(function (d) {
      if (d.reminderId && tracker.isAcked(d.reminderId)) return;
      announce(d);
    });
  }

  // speech waits for the first user gesture (browsers keep audio locked until then)
  H.on("gesture", function () {
    if (deferred.length) setTimeout(playDeferred, 0);
  });

  function showSystemNotification(due) {
    if (!notifyGranted()) return;
    try {
      var n = new window.Notification("HERMES", {
        body: R.notificationBody(due, tr),
        tag: "hermes-" + (due.reminderId || "e" + due.eventId),
        renotify: due.repeat > 0,
        silent: speakHere(), // the chime plays already
      });
      n.onclick = function () {
        try {
          window.focus();
        } catch (e) {
          /* ignore */
        }
        n.close();
      };
    } catch (e) {
      /* e.g. Android Chrome needs a service worker: notice and speech still work */
    }
  }

  function onAcked(ev) {
    var d = ev && ev.data;
    var id = d && typeof d.reminderId === "string" ? d.reminderId : "";
    if (!/^r_[A-Za-z0-9]{1,40}$/.test(id)) return;
    acked(id);
    if (view && view.visible) loadList();
  }

  /*
   * "stopp" while a reminder is being spoken acknowledges it (SPEC 5.5).
   * app.js emits "stop" from every user stop (spoken "stopp", barge-in, the
   * stop button and Esc) before it cuts the speech off.
   */
  H.on("stop", function () {
    var id = speaking;
    var n = id ? notices[id] : null;
    if (id) markDone(id, n && n.due ? n.due.due : "");
  });

  H.on("hub:reminder.due", onDue);
  // meetings arrive as reminder.due; checkin.meeting is reserved (SPEC 3.2)
  H.on("hub:checkin.meeting", onDue);
  H.on("hub:reminder.acked", onAcked);
  H.on("hub:reminders.changed", function () {
    if (view && view.visible) loadList();
  });
  // a chat reply that set, moved or snoozed a reminder carries a card
  // {kind:"reminder"|"timer", data:{id, dueText, …}}; the list card
  // ({kind:"reminder", data:{reminders:[…]}}) only refreshes the tab
  H.on("card", function (c) {
    if (!c || (c.kind !== "reminder" && c.kind !== "timer") || !c.data || typeof c.data !== "object") return;
    if (view && view.visible) loadList();
    var d = c.data;
    if (typeof d.id !== "string" || !/^r_[A-Za-z0-9]{1,40}$/.test(d.id) || typeof d.dueText !== "string" || !d.dueText) return;
    var when = d.dueText.replace(/[\u0000-\u001f\u007f]/g, " ").slice(0, 120);
    var rule = R.ruleText(d, lang());
    var msg = rule ? tr("addedRepeat", { rule: rule, when: when }) : tr(c.kind === "timer" ? "cardTimer" : "cardSet", { when: when });
    toast(msg, {
      ms: 5000,
      key: "reminder-card",
      action:
        typeof api().openTab === "function"
          ? {
              label: tr("open"),
              fn: function () {
                api().openTab("reminders");
              },
            }
          : undefined,
    });
  });

  // ---------------------------------------------------------------- hub tab

  var ICON_TAB =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="13" r="7.5"/><path d="M12 9v4l2.5 2"/><path d="M4.5 4.5 7 2.5M19.5 4.5 17 2.5"/></svg>';

  var view = null; // {root, visible, list, loaded, error, busy, seq}

  function loadList() {
    if (!view) return Promise.resolve();
    var seq = ++view.seq;
    view.busy = true;
    if (!view.loaded) render();
    return fetchJSON("/api/reminders?status=all")
      .then(function (r) {
        if (seq !== view.seq) return;
        view.list = r && Array.isArray(r.reminders) ? r.reminders.slice(0, 1000) : [];
        view.loaded = true;
        view.error = "";
      })
      .catch(function (err) {
        if (seq !== view.seq) return;
        view.error = errText(err);
        view.loaded = true;
      })
      .then(function () {
        if (seq !== view.seq) return;
        view.busy = false;
        render();
      });
  }

  function personLabel(r) {
    var name = R.cleanStr(r.personName, 40);
    if (name) return name;
    return r.person ? "" : tr("household");
  }

  function actionButton(key, r) {
    var due = R.cleanStr(r.due, 40);
    if (key === "done") {
      return el("button", {
        type: "button",
        class: "small",
        text: tr("done"),
        onclick: function () {
          markDone(r.id, due);
        },
      });
    }
    if (key.indexOf("snooze:") === 0) {
      var n = R.validateSnooze(key.slice(7));
      return el("button", {
        type: "button",
        class: "small ghost rem-snooze",
        text: tr("snoozeN", { n: n }),
        title: tr("snoozeTitle", { n: n }),
        "aria-label": tr("snoozeTitle", { n: n }),
        onclick: function () {
          snooze(r.id, n);
        },
      });
    }
    if (key === "skip") {
      return el("button", {
        type: "button",
        class: "small ghost rem-skip",
        text: tr("skipNext"),
        title: tr("skipTitle"),
        onclick: function (e) {
          skipNext(r.id, e.currentTarget);
        },
      });
    }
    var stop = key === "stop";
    return el("button", {
      type: "button",
      class: "small ghost danger rem-cancel",
      text: tr(stop ? "stopSeries" : "cancel"),
      title: tr(stop ? "stopTitle" : "cancelTitle"),
      onclick: function (e) {
        cancelReminder(r.id, e.currentTarget, stop);
      },
    });
  }

  function rowFor(r, fired) {
    var kind = R.KINDS.indexOf(r.kind) >= 0 ? r.kind : "reminder";
    var text = R.cleanStr(r.text, 500) || (r.private || r.locked ? tr("privateText") : tr("kind_" + kind));
    var due = R.cleanStr(r.dueText, 80) || R.cleanStr(r.due, 40);
    var rule = R.ruleText(r, lang());
    var meta = [tr("kind_" + kind), rule ? tr("nextTime", { when: due }) : due, personLabel(r)].filter(Boolean).join(" · ");
    var actions = el(
      "div",
      { class: "rem-actions" },
      R.rowActions(r, fired).map(function (key) {
        return actionButton(key, r);
      })
    );
    var main = [el("div", { class: "rem-text", text: text })];
    if (rule) main.push(el("div", { class: "rem-rule", text: "↻ " + rule, title: tr("repeats", { rule: rule }) }));
    main.push(el("div", { class: "rem-meta", text: meta }));
    return el("li", { class: "rem-item" + (fired ? " fired" : "") + (r.private ? " private" : "") + (rule ? " recurring" : ""), "data-kind": kind }, [
      el("div", { class: "rem-main" }, main),
      actions,
    ]);
  }

  function skipNext(id, btn) {
    if (btn) btn.disabled = true;
    fetchJSON("/api/reminders/" + encodeURIComponent(id) + "/skip", { method: "POST", body: {} })
      .then(function (r) {
        acked(id);
        var when = R.cleanStr(r && r.dueText, 80) || R.cleanStr(r && r.due, 40);
        toast(tr("skipped", { when: when }), { type: "success", key: "rem-skip" });
      })
      .catch(function (err) {
        toast(errText(err), { type: "warn", key: "rem-skip" });
      })
      .then(function () {
        if (btn) btn.disabled = false;
        loadList();
      });
  }

  function cancelReminder(id, btn, series) {
    if (btn) btn.disabled = true;
    fetchJSON("/api/reminders/" + encodeURIComponent(id), { method: "DELETE" })
      .then(function () {
        acked(id);
        toast(tr(series ? "stopped" : "cancelled"), { type: "success", key: "rem-cancel" });
      })
      .catch(function (err) {
        toast(errText(err), { type: "warn", key: "rem-cancel" });
      })
      .then(function () {
        if (btn) btn.disabled = false;
        loadList();
      });
  }

  function addForm() {
    var text = el("input", { type: "text", class: "rem-in-text", maxlength: String(R.TEXT_MAX), placeholder: tr("addTextPlaceholder"), "aria-label": tr("addText"), autocomplete: "off" });
    var when = el("input", { type: "text", class: "rem-in-when", maxlength: String(R.WHEN_MAX), placeholder: tr("addWhenPlaceholder"), "aria-label": tr("addWhen"), autocomplete: "off" });
    var repeat = el(
      "select",
      { class: "rem-in-repeat", "aria-label": tr("repeat") },
      R.REPEAT_CHOICES.map(function (k) {
        return el("option", { value: k, text: tr("repeat_" + k) });
      })
    );
    var msg = el("p", { class: "rem-form-msg", role: "status", hidden: true });
    var btn = el("button", { type: "submit", class: "small", text: tr("add") });
    var form = el("form", { class: "rem-form", novalidate: true }, [
      el("label", { class: "rem-field" }, [el("span", { text: tr("addText") }), text]),
      el("label", { class: "rem-field" }, [el("span", { text: tr("addWhen") }), when]),
      el("label", { class: "rem-field rem-field-repeat" }, [el("span", { text: tr("repeat") }), repeat]),
      btn,
      msg,
    ]);
    form.addEventListener("submit", function (e) {
      e.preventDefault();
      var v = R.validateAdd(text.value, when.value, repeat.value);
      if (!v.ok) {
        msg.hidden = false;
        msg.textContent = tr(v.key, v.vars);
        (v.key === "errText" || v.key === "errTextLong" ? text : when).focus();
        return;
      }
      msg.hidden = true;
      btn.disabled = true;
      fetchJSON("/api/reminders", { method: "POST", body: R.addBody(v) })
        .then(function (r) {
          text.value = "";
          when.value = "";
          repeat.value = "none";
          var dueText = R.cleanStr(r && r.dueText, 80) || R.cleanStr(r && r.due, 40);
          var rule = R.ruleText(r, lang());
          toast(rule ? tr("addedRepeat", { rule: rule, when: dueText }) : tr("added", { when: dueText }), { type: "success", key: "rem-add" });
          loadList();
        })
        .catch(function (err) {
          msg.hidden = false;
          msg.textContent = errText(err);
          if (R.errorKey(err) === "errWhen") when.focus();
        })
        .then(function () {
          btn.disabled = false;
        });
    });
    return form;
  }

  function render() {
    if (!view) return;
    var root = view.root;
    // keep the add form (and what the user is typing) across list refreshes
    if (!view.form || view.formLang !== lang()) {
      view.form = addForm();
      view.formLang = lang();
    }
    clear(root);
    root.appendChild(view.form);
    if (view.error) {
      root.appendChild(
        el("div", { class: "rem-note warn", role: "alert" }, [
          el("span", { text: view.error }),
          el("button", { type: "button", class: "small ghost", text: tr("reload"), onclick: loadList }),
        ])
      );
    }
    if (!view.loaded) {
      root.appendChild(el("p", { class: "rem-empty", text: tr("loading") }));
      return;
    }
    var groups = R.visibleReminders(view.list);
    if (!groups.fired.length && !groups.pending.length) {
      if (!view.error) root.appendChild(el("p", { class: "rem-empty", text: tr("empty") }));
      return;
    }
    if (groups.fired.length) {
      root.appendChild(el("h3", { class: "rem-group", text: tr("due") }));
      root.appendChild(
        el(
          "ul",
          { class: "rem-list" },
          groups.fired.map(function (r) {
            return rowFor(r, true);
          })
        )
      );
    }
    if (groups.pending.length) {
      root.appendChild(el("h3", { class: "rem-group", text: tr("upcoming") }));
      root.appendChild(
        el(
          "ul",
          { class: "rem-list" },
          groups.pending.map(function (r) {
            return rowFor(r, false);
          })
        )
      );
    }
  }

  function mountTab(root) {
    root.classList.add("rem");
    view = { root: root, visible: false, list: [], loaded: false, error: "", busy: false, seq: 0, form: null, formLang: "" };
    render();
  }

  // ---------------------------------------------------------------- settings section

  var section = null;

  function mountSection(root) {
    root.classList.add("rem-settings");
    section = { root: root };
    renderSection();
  }

  function leaderText() {
    var ev = H.events || {};
    if (ev.leader) return tr("leaderYes");
    if (ev.active) return tr("leaderOther");
    return tr("leaderNone");
  }

  function renderSection() {
    if (!section) return;
    var root = section.root;
    clear(root);
    var on = speakHere();
    var box = el("input", { type: "checkbox", id: "rem_speak_here", checked: on });
    box.addEventListener("change", function () {
      setSpeakHere(box.checked);
      renderSection();
    });
    root.appendChild(el("label", { class: "check", for: "rem_speak_here" }, [box, " " + tr("speakHere")]));
    root.appendChild(el("p", { class: "hint", text: tr("speakHereHint") }));
    root.appendChild(el("p", { class: "rem-status", role: "status", text: on ? leaderText() : tr("speakOffNote") }));

    // system notifications
    var notifyRow = el("div", { class: "rem-notify" }, [el("span", { class: "rem-label", text: tr("notifications") })]);
    if (!isSecure() || typeof window.Notification !== "function") {
      notifyRow.appendChild(el("span", { class: "hint", text: tr("notifyUnavailable") }));
    } else if (window.Notification.permission === "granted") {
      notifyRow.appendChild(el("span", { class: "rem-ok", text: tr("notifyGranted") }));
    } else if (window.Notification.permission === "denied") {
      notifyRow.appendChild(el("span", { class: "hint", text: tr("notifyDenied") }));
    } else {
      notifyRow.appendChild(
        el("button", {
          type: "button",
          class: "small",
          text: tr("notifyAsk"),
          onclick: function () {
            try {
              var p = window.Notification.requestPermission(function () {
                renderSection();
              });
              if (p && p.then) p.then(renderSection, renderSection);
            } catch (e) {
              renderSection();
            }
          },
        })
      );
    }
    root.appendChild(notifyRow);
    root.appendChild(el("p", { class: "hint", text: tr("notifyHint") }));

    // quiet hours (edited in the Check-ins section; shown here for context)
    var s = settings();
    var qs = R.parseHHMM(s.quietStart) != null ? s.quietStart : "22:00";
    var qe = R.parseHHMM(s.quietEnd) != null ? s.quietEnd : "07:00";
    if (qs !== qe) root.appendChild(el("p", { class: "hint rem-quiet", text: tr("quiet", { from: qs, to: qe }) }));

    // Chrome Memory Saver may discard the speaking tab
    // docs/reminders-and-lists.md ("Who announces them") repeats this hint (the docs are not served, so no link)
    var chrome = /Chrome\//.test(navigator.userAgent || "") && !/Edg\//.test(navigator.userAgent || "");
    if (on && chrome) root.appendChild(el("p", { class: "rem-note warn", text: tr("memorySaver") }));

    var buttons = [
      el("button", {
        type: "button",
        class: "small ghost",
        text: tr("test"),
        disabled: !on,
        onclick: function () {
          var a = api();
          var c = typeof a.chime === "function" ? a.chime() : null;
          Promise.resolve(c)
            .catch(function () {})
            .then(function () {
              if (typeof a.say === "function") return a.say(tr("testLine"), { force: true });
              return null;
            })
            .catch(function () {});
        },
      }),
    ];
    if (typeof api().openTab === "function") {
      buttons.push(
        el("button", {
          type: "button",
          class: "small",
          text: tr("openTab"),
          onclick: function () {
            api().openTab("reminders");
          },
        })
      );
    }
    root.appendChild(el("div", { class: "rem-settings-actions" }, buttons));
  }

  // ---------------------------------------------------------------- wiring

  if (typeof H.registerTab === "function") {
    H.registerTab({
      id: "reminders",
      order: 10,
      label: { en: R.STRINGS.en.tab, de: R.STRINGS.de.tab },
      icon: ICON_TAB,
      mount: mountTab,
      show: function () {
        if (!view) return;
        view.visible = true;
        loadList();
      },
      hide: function () {
        if (view) view.visible = false;
      },
    });
  }
  if (typeof H.registerSettingsSection === "function") {
    H.registerSettingsSection({ id: "reminders", order: 70, title: { en: R.STRINGS.en.section, de: R.STRINGS.de.section }, mount: mountSection });
  }

  H.on("hub:leader", renderSection);
  H.on("hub:open", renderSection);
  H.on("settings", renderSection);
  H.on("lang", function () {
    renderSection();
    if (view) {
      if (view.visible) loadList(); // dueText is rendered by the server in the UI language
      else render();
    }
  });
  H.on("visible", function () {
    if (section) renderSection();
    if (view && view.visible) loadList();
  });
})();
