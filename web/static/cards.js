/*
 * HERMES chat cards: compact visual cards under HERMES's reply bubble.
 *
 * The server sends at most one {"card":{kind,data}} event per reply
 * (skills.go Card); app.js re-emits it as Hermes.emit("card", {kind, data, msgId}).
 * Kinds rendered here (data shapes from the Go code that emits them):
 *
 *   briefing  {sections:[{name,text}]}                         briefing.go
 *   calendar  {from,to,events:[CalendarEvent],added?}          calendar.go
 *   home      {action,targets:[name],via,refused?}             homeassistant.go
 *   reminder  {id,text,due,dueText,kind,private}               reminders.go
 *             or {reminders:[ReminderView]} (the "what's due" list)
 *   timer     {id,text,due,dueText,kind:"timer",private}
 *   list      {id,name,kind,items:[text],rev,share?,text?}     lists.go
 *   docs      {hits:[{name,n,doc}]}                            docs.go
 *   link      {url,title,site,paywall}                         readlink.go
 *   memory    {id,text,person} or {id,deleted:true,person}     memory.go
 *   weather   {city,rain,now?:{temp,feelsLike,code,condition,wind},
 *              days:[{label,date,max,min,rain?,code,condition}]} realtime.go
 *   news      {source:"tagesschau"|"googlenews",topic,
 *              items:[{title,topline?}]}                       realtime.go
 *
 * Any other kind (person, undo, …) renders nothing here; other modules
 * handle those. Missing or malformed fields degrade to less detail.
 *
 * Part 1 (UMD, no DOM): pure helpers for the browser (window.HermesCards)
 * and the node tests (require("../../static/cards.js")): cleanText,
 * wallClock, dayLabel, todayIn, parseWeather, countdown, safeHref,
 * findOpenItem, model, STRINGS.
 *
 * Part 2 (browser only, needs window.Hermes): listens for "card" and builds
 * the card with createElement/textContent only. Server and model text is
 * never parsed as HTML. Buttons call the existing endpoints:
 * POST /api/reminders/{id}/done, POST /api/reminders/{id}/snooze,
 * GET /api/lists and PATCH /api/lists/{id}/items/{item}.
 *
 * Wiring: index.html loads /css/cards.css (after /css/settings.css) and
 *   this script after /links.js, before /app.js. app.js emits
 *   ("card", {kind, data, msgId}) with the reply's message id, keeps the
 *   cards with the message (lib.js keepCard, at most 4, private reminder
 *   text dropped) and redraws them through HermesCards.renderInto() when it
 *   rebuilds a message (reload, renderAll), without re-emitting the event.
 * homeassistant.go sends no card for a refused action (the spoken refusal
 *   is the answer); a {refused:true} home card still renders as a muted
 *   warning should one arrive.
 */
(function (root, factory) {
  var api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root && typeof root === "object") root.HermesCards = api;
})(typeof window !== "undefined" ? window : typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  var TEXT_MAX = 300; // runes per line of server text
  var SECTION_MAX = 800; // runes per briefing section
  var TITLE_MAX = 120;
  var URL_MAX = 2000;
  var EVENTS_MAX = 20;
  var ITEMS_MAX = 30;
  var REMINDERS_MAX = 10;
  var DOCS_MAX = 6;
  var SECTIONS_MAX = 12;
  var CARDS_PER_MESSAGE = 4;
  var SNOOZE_MINUTES = 10;

  var REMINDER_ID = /^r_[A-Za-z0-9]{1,40}$/;
  var LIST_ID = /^(shopping|todo|l_[A-Za-z0-9]{1,40})$/;
  var ITEM_ID = /^i_[A-Za-z0-9]{1,40}$/;

  var STRINGS = {
    en: {
      briefing: "Your day",
      weather: "Weather",
      calendarSec: "Calendar",
      remindersSec: "Reminders",
      listsSec: "Lists",
      news: "Headlines",
      calendar: "Calendar",
      calendarAdded: "Added to the calendar",
      noEvents: "No events.",
      allDay: "all day",
      invite: "(Invitation)",
      noTitle: "(no title)",
      today: "Today",
      tomorrow: "Tomorrow",
      yesterday: "Yesterday",
      more: "+{n} more",
      home: "Home",
      via_assist: "via Assist",
      via_service: "via service",
      via_states: "status",
      refused: "Not allowed",
      act_on: "On",
      act_off: "Off",
      act_set: "Set",
      act_dim: "Dimmed",
      act_open: "Open",
      act_close: "Closed",
      act_play: "Playing",
      act_pause: "Paused",
      act_stop: "Stopped",
      act_query: "Status",
      reminder: "Reminder",
      reminders: "Reminders",
      timer: "Timer",
      privateText: "Private reminder",
      done: "Done",
      doneAria: "Mark “{what}” as done",
      snooze: "Snooze {n} min",
      snoozeAria: "Snooze “{what}” for {n} minutes",
      markedDone: "Done ✓",
      snoozed: "Snoozed {n} min",
      expired: "elapsed",
      failed: "That did not work. Try again.",
      gone: "This reminder no longer exists.",
      list: "List",
      emptyList: "The list is empty.",
      itemAria: "Mark “{what}” as done",
      itemGone: "That item is no longer on the list.",
      docs: "Sources",
      link: "Article",
      paywall: "Paywall",
      memory: "Memory",
      memorySaved: "Noted",
      memoryDeleted: "Removed from memory",
      feelsLike: "feels like {n}°",
      wind: "wind {n} km/h",
      rainChance: "{n}% rain",
      newsTopic: "News: {topic}",
      src_tagesschau: "tagesschau.de",
      src_googlenews: "Google News",
    },
    de: {
      briefing: "Dein Tag",
      weather: "Wetter",
      calendarSec: "Kalender",
      remindersSec: "Erinnerungen",
      listsSec: "Listen",
      news: "Schlagzeilen",
      calendar: "Kalender",
      calendarAdded: "Im Kalender eingetragen",
      noEvents: "Keine Termine.",
      allDay: "ganztägig",
      invite: "(Einladung)",
      noTitle: "(ohne Titel)",
      today: "Heute",
      tomorrow: "Morgen",
      yesterday: "Gestern",
      more: "+{n} weitere",
      home: "Zuhause",
      via_assist: "über Assist",
      via_service: "über Dienst",
      via_states: "Status",
      refused: "Nicht erlaubt",
      act_on: "An",
      act_off: "Aus",
      act_set: "Eingestellt",
      act_dim: "Gedimmt",
      act_open: "Offen",
      act_close: "Geschlossen",
      act_play: "Läuft",
      act_pause: "Pausiert",
      act_stop: "Gestoppt",
      act_query: "Status",
      reminder: "Erinnerung",
      reminders: "Erinnerungen",
      timer: "Timer",
      privateText: "Private Erinnerung",
      done: "Erledigt",
      doneAria: "„{what}“ als erledigt markieren",
      snooze: "{n} Min. später",
      snoozeAria: "„{what}“ um {n} Minuten verschieben",
      markedDone: "Erledigt ✓",
      snoozed: "Verschoben um {n} Min.",
      expired: "abgelaufen",
      failed: "Das hat nicht geklappt. Versuch es noch einmal.",
      gone: "Diese Erinnerung gibt es nicht mehr.",
      list: "Liste",
      emptyList: "Die Liste ist leer.",
      itemAria: "„{what}“ abhaken",
      itemGone: "Der Eintrag steht nicht mehr auf der Liste.",
      docs: "Quellen",
      link: "Artikel",
      paywall: "Bezahlschranke",
      memory: "Gedächtnis",
      memorySaved: "Gemerkt",
      memoryDeleted: "Aus dem Gedächtnis gelöscht",
      feelsLike: "gefühlt {n}°",
      wind: "Wind {n} km/h",
      rainChance: "{n} % Regen",
      newsTopic: "Nachrichten: {topic}",
      src_tagesschau: "tagesschau.de",
      src_googlenews: "Google News",
    },
  };

  var WEEKDAYS = {
    en: ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"],
    de: ["So.", "Mo.", "Di.", "Mi.", "Do.", "Fr.", "Sa."],
  };
  var MONTHS_EN = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

  var ICONS = {
    briefing: "☀️",
    calendar: "📅",
    home: "🏠",
    reminder: "⏰",
    timer: "⏲️",
    list: "🛒",
    todo: "✅",
    docs: "📄",
    link: "🔗",
    memory: "🧠",
    weather: "🌤️",
    calendarSec: "📅",
    remindersSec: "⏰",
    listsSec: "🛒",
    news: "📰",
    greeting: "👋",
  };

  // ---------------------------------------------------------------- text

  function lang2(lang) {
    return lang === "de" ? "de" : "en";
  }

  function tr(lang, key, vars) {
    var table = STRINGS[lang2(lang)];
    var s = Object.prototype.hasOwnProperty.call(table, key) ? table[key] : STRINGS.en[key];
    if (s == null) s = key;
    if (!vars) return s;
    return s.replace(/\{(\w+)\}/g, function (all, k) {
      return Object.prototype.hasOwnProperty.call(vars, k) && vars[k] != null ? String(vars[k]) : all;
    });
  }

  /*
   * One display line from untrusted input: strings only (numbers are
   * stringified), no control or bidi-override characters, whitespace
   * collapsed, at most max code points (with "…").
   */
  function cleanText(s, max) {
    if (typeof s === "number" && isFinite(s)) s = String(s);
    if (typeof s !== "string") return "";
    max = max || TEXT_MAX;
    // cheap pre-cut so a huge string is not scanned in full
    if (s.length > max * 4 + 16) s = s.slice(0, max * 4 + 16);
    s = s
      .replace(/[\u0000-\u001f\u007f-\u009f​-‏‪-‮⁦-⁩﻿]/g, " ")
      .replace(/\s+/g, " ")
      .trim();
    var cps = Array.from(s);
    if (cps.length > max) s = cps.slice(0, max - 1).join("").replace(/\s+$/, "") + "…";
    return s;
  }

  function arr(v, max) {
    return Array.isArray(v) ? v.slice(0, max) : [];
  }

  // ---------------------------------------------------------------- time

  var ISO_RE = /^(\d{4})-(\d{2})-(\d{2})(?:[T ](\d{2}):(\d{2})(?::(\d{2})(?:\.\d+)?)?)?(Z|[+-]\d{2}:?\d{2})?$/;

  /*
   * The wall-clock date and time an RFC 3339 string names, read from the
   * string itself: the server formats calendar times in the user's
   * timezone, which may differ from the browser's. Null when unusable.
   */
  function wallClock(iso) {
    if (typeof iso !== "string" || iso.length > 40) return null;
    var m = ISO_RE.exec(iso.trim());
    if (!m) return null;
    var mo = +m[2],
      d = +m[3];
    if (mo < 1 || mo > 12 || d < 1 || d > 31) return null;
    var out = { date: m[1] + "-" + m[2] + "-" + m[3], time: "" };
    if (m[4] != null) {
      if (+m[4] > 23 || +m[5] > 59) return null;
      out.time = m[4] + ":" + m[5];
    }
    return out;
  }

  /* Offset of an RFC 3339 string in minutes (0 for "Z"; null when none). */
  function offsetMinutes(iso) {
    if (typeof iso !== "string") return null;
    var m = /(Z|([+-])(\d{2}):?(\d{2}))$/.exec(iso.trim());
    if (!m || !/T|\s/.test(iso)) return null;
    if (m[1] === "Z") return 0;
    var v = +m[3] * 60 + +m[4];
    if (v > 18 * 60) return null;
    return m[2] === "-" ? -v : v;
  }

  function pad2(n) {
    return (n < 10 ? "0" : "") + n;
  }

  function ymdUTC(d) {
    return d.getUTCFullYear() + "-" + pad2(d.getUTCMonth() + 1) + "-" + pad2(d.getUTCDate());
  }

  /*
   * Today's date ("YYYY-MM-DD") in the timezone of `refIso` (an RFC 3339
   * string from the same card), else in the browser's local zone.
   */
  function todayIn(nowMs, refIso) {
    var off = offsetMinutes(refIso);
    if (off === null) {
      var l = new Date(nowMs);
      return l.getFullYear() + "-" + pad2(l.getMonth() + 1) + "-" + pad2(l.getDate());
    }
    return ymdUTC(new Date(nowMs + off * 60000));
  }

  function dateParts(ymd) {
    var m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(ymd || "");
    if (!m) return null;
    return new Date(Date.UTC(+m[1], +m[2] - 1, +m[3]));
  }

  /* "Heute" / "Morgen" / "Mi. 12.11." or "Today" / "Tomorrow" / "Wed, Nov 12". */
  function dayLabel(ymd, today, lang) {
    lang = lang2(lang);
    var d = dateParts(ymd);
    if (!d) return "";
    var t = dateParts(today);
    if (t) {
      var diff = Math.round((d.getTime() - t.getTime()) / 86400000);
      if (diff === 0) return tr(lang, "today");
      if (diff === 1) return tr(lang, "tomorrow");
      if (diff === -1) return tr(lang, "yesterday");
    }
    var wd = WEEKDAYS[lang][d.getUTCDay()];
    if (lang === "de") return wd + " " + pad2(d.getUTCDate()) + "." + pad2(d.getUTCMonth() + 1) + ".";
    return wd + ", " + MONTHS_EN[d.getUTCMonth()] + " " + d.getUTCDate();
  }

  /* Snoozing is offered once a reminder is due (known due time, not ahead). */
  function snoozeAllowed(dueMs, nowMs) {
    return typeof dueMs === "number" && isFinite(dueMs) && typeof nowMs === "number" && dueMs <= nowMs;
  }

  /* Remaining time as "m:ss" or "h:mm:ss"; "0:00" when due. */
  function countdown(dueMs, nowMs) {
    if (typeof dueMs !== "number" || !isFinite(dueMs) || typeof nowMs !== "number") return "";
    var s = Math.max(0, Math.ceil((dueMs - nowMs) / 1000));
    var h = Math.floor(s / 3600);
    var m = Math.floor((s % 3600) / 60);
    var sec = s % 60;
    return h > 0 ? h + ":" + pad2(m) + ":" + pad2(sec) : m + ":" + pad2(sec);
  }

  function parseMs(iso) {
    if (typeof iso !== "string" || !wallClock(iso)) return NaN;
    var v = Date.parse(iso);
    return isFinite(v) ? v : NaN;
  }

  // ---------------------------------------------------------------- weather

  var WEATHER_ICONS = [
    [/gewitter|thunder/i, "⛈️"],
    [/schnee|snow|sleet/i, "❄️"],
    [/regen|niesel|schauer|rain|drizzle|shower/i, "🌧️"],
    [/nebel|neblig|fog|mist/i, "🌫️"],
    [/teils|partly|überwiegend bewölkt/i, "⛅"],
    [/bewölkt|bedeckt|wolkig|cloud|overcast/i, "☁️"],
    [/klar|sonnig|heiter|clear|sunny|fair/i, "☀️"],
  ];

  /* An icon for a WMO weather code (Open-Meteo weather_code). */
  function wmoIcon(code) {
    if (typeof code !== "number" || !isFinite(code)) return "🌡️";
    if (code >= 95) return "⛈️";
    if ((code >= 71 && code <= 77) || code === 85 || code === 86) return "❄️";
    if ((code >= 51 && code <= 67) || (code >= 80 && code <= 82)) return "🌧️";
    if (code === 45 || code === 48) return "🌫️";
    if (code === 3) return "☁️";
    if (code === 1 || code === 2) return "⛅";
    if (code === 0) return "☀️";
    return "🌡️";
  }

  /*
   * The current temperature and an icon from the weather section text
   * (briefing.go weatherSentence: "In Berlin sind es minus 2 Grad, Schnee.
   * Heute bis 1 Grad." / "In Berlin it is 6 degrees, overcast."). The
   * condition is read only from the words after the temperature, so a city
   * name ("Regensburg") or the rain chance never picks the icon.
   */
  function parseWeather(text) {
    var out = { temp: null, icon: "🌡️", condition: "" };
    if (typeof text !== "string") return out;
    var m = /(minus\s+|-|−)?(\d{1,3})\s*(?:grad\b|degrees?\b|°)/i.exec(text);
    if (!m) return out;
    var n = parseInt(m[2], 10);
    if (m[1] && n !== 0) n = -n;
    out.temp = n;
    var rest = text.slice(m.index + m[0].length);
    var cut = rest.search(/[.!?]/);
    var cond = (cut >= 0 ? rest.slice(0, cut) : rest).replace(/^\s*,\s*/, "").trim();
    out.condition = cleanText(cond, 40);
    for (var i = 0; i < WEATHER_ICONS.length; i++) {
      if (WEATHER_ICONS[i][0].test(cond)) {
        out.icon = WEATHER_ICONS[i][1];
        break;
      }
    }
    return out;
  }

  // ---------------------------------------------------------------- links

  /* The URL when it is a plain http(s) URL without credentials, else "". */
  function safeHref(url) {
    if (typeof url !== "string" || !url || url.length > URL_MAX) return "";
    if (!/^https?:\/\//i.test(url) || /[\s\u0000-\u001f\u007f]/.test(url)) return "";
    try {
      var u = new URL(url);
      if ((u.protocol !== "http:" && u.protocol !== "https:") || u.username || u.password || !u.hostname) return "";
      return u.href;
    } catch (e) {
      return "";
    }
  }

  function hostOf(href) {
    try {
      return new URL(href).hostname.replace(/^www\./, "");
    } catch (e) {
      return "";
    }
  }

  // ---------------------------------------------------------------- lists

  /*
   * The id of the first open item with this text in list `listId` of a
   * GET /api/lists response; "" when there is none. Matching ignores case
   * and surrounding space, like the server's duplicate check.
   */
  function findOpenItem(resp, listId, text) {
    var lists = resp && Array.isArray(resp.lists) ? resp.lists : [];
    var want = cleanText(text, 400).toLowerCase();
    if (!want) return "";
    for (var i = 0; i < lists.length; i++) {
      var l = lists[i];
      if (!l || l.id !== listId || !Array.isArray(l.items)) continue;
      for (var j = 0; j < l.items.length; j++) {
        var it = l.items[j];
        if (!it || it.done || typeof it.id !== "string" || !ITEM_ID.test(it.id)) continue;
        if (cleanText(it.text, 400).toLowerCase() === want) return it.id;
      }
    }
    return "";
  }

  // ---------------------------------------------------------------- models

  function isObj(v) {
    return !!v && typeof v === "object" && !Array.isArray(v);
  }

  var BRIEFING_KEYS = { calendar: "calendarSec", reminders: "remindersSec", lists: "listsSec", news: "news", weather: "weather" };

  function briefingModel(d, o) {
    var greeting = "";
    var rows = [];
    arr(d.sections, SECTIONS_MAX).forEach(function (s) {
      if (!isObj(s)) return;
      var name = typeof s.name === "string" ? s.name : "";
      var text = cleanText(s.text, SECTION_MAX);
      if (!text) return;
      if (name === "greeting") {
        if (!greeting) greeting = text;
        return;
      }
      var key = BRIEFING_KEYS[name] || "";
      var row = { name: name, label: key ? tr(o.lang, key) : "", icon: ICONS[key] || "•", text: text, collapsed: name === "news" };
      if (name === "weather") {
        var w = parseWeather(text);
        row.icon = w.icon;
        row.temp = w.temp;
        row.condition = w.condition;
      }
      rows.push(row);
    });
    if (!greeting && !rows.length) return null;
    return { kind: "briefing", title: tr(o.lang, "briefing"), greeting: greeting, rows: rows };
  }

  function calendarModel(d, o) {
    var all = Array.isArray(d.events) ? d.events : null;
    if (!all) return null;
    var today = todayIn(o.now, typeof d.from === "string" ? d.from : "");
    var groups = [];
    var byDate = {};
    var shown = 0;
    for (var i = 0; i < all.length && shown < EVENTS_MAX; i++) {
      var ev = all[i];
      if (!isObj(ev)) continue;
      var st = wallClock(ev.start);
      if (!st) continue;
      var en = wallClock(ev.end);
      var time;
      if (ev.allDay === true) time = tr(o.lang, "allDay");
      else {
        time = st.time;
        if (en && en.date === st.date && en.time && en.time > st.time) time += "–" + en.time;
      }
      var item = {
        time: time,
        allDay: ev.allDay === true,
        title: cleanText(ev.title, TITLE_MAX) || tr(o.lang, "noTitle"),
        location: cleanText(ev.location, TITLE_MAX),
        invite: ev.needsAction === true,
      };
      var g = byDate[st.date];
      if (!g) {
        g = byDate[st.date] = { date: st.date, label: dayLabel(st.date, today, o.lang), items: [] };
        groups.push(g);
      }
      g.items.push(item);
      shown++;
    }
    var total = 0;
    all.forEach(function (ev) {
      if (isObj(ev) && wallClock(ev.start)) total++;
    });
    return {
      kind: "calendar",
      title: tr(o.lang, d.added === true ? "calendarAdded" : "calendar"),
      added: d.added === true,
      groups: groups,
      more: Math.max(0, total - shown),
      empty: tr(o.lang, "noEvents"),
    };
  }

  var HOME_ACTIONS = { on: 1, off: 1, set: 1, dim: 1, open: 1, close: 1, play: 1, pause: 1, stop: 1, query: 1 };

  function homeModel(d, o) {
    var names = [];
    arr(d.targets, 6).forEach(function (n) {
      var c = cleanText(n, 80);
      if (c) names.push(c);
    });
    var action = typeof d.action === "string" && HOME_ACTIONS[d.action] ? d.action : "";
    // optional: a future server field for a refused action (no card today)
    var refused = d.refused === true;
    if (!names.length && !action && !refused) return null;
    var via = d.via === "assist" || d.via === "service" || d.via === "states" ? tr(o.lang, "via_" + d.via) : "";
    return {
      kind: "home",
      title: tr(o.lang, "home"),
      targets: names.join(", "),
      state: refused ? tr(o.lang, "refused") : action ? tr(o.lang, "act_" + action) : "",
      via: refused ? "" : via,
      refused: refused,
      query: action === "query",
    };
  }

  function reminderItem(r, o, forceTimer) {
    if (!isObj(r)) return null;
    var timer = forceTimer || r.kind === "timer";
    var priv = r.private === true;
    var text = priv ? "" : cleanText(r.text, TITLE_MAX);
    var id = typeof r.id === "string" && REMINDER_ID.test(r.id) ? r.id : "";
    var due = parseMs(r.due);
    var dueText = cleanText(r.dueText, 80);
    if (!text && !dueText && !id) return null;
    return {
      id: id,
      // the occurrence this card shows, sent with Done so a stale card of a
      // series never completes a later occurrence
      dueIso: isFinite(due) && typeof r.due === "string" ? r.due : "",
      recurring: isObj(r.rule) || (typeof r.ruleText === "string" && r.ruleText.trim() !== ""),
      ruleText: cleanText(r.ruleText, 80),
      timer: timer,
      private: priv,
      text: text || (priv ? tr(o.lang, "privateText") : tr(o.lang, timer ? "timer" : "reminder")),
      dueText: dueText,
      due: isFinite(due) ? due : null,
      person: cleanText(r.personName, 60),
    };
  }

  function reminderModel(kind, d, o) {
    var items = [];
    var more = 0;
    if (Array.isArray(d.reminders)) {
      d.reminders.slice(0, REMINDERS_MAX).forEach(function (r) {
        var it = reminderItem(r, o, false);
        if (it) items.push(it);
      });
      more = Math.max(0, d.reminders.length - REMINDERS_MAX);
      if (!items.length) return null;
      return { kind: "reminder", title: tr(o.lang, "reminders"), list: true, items: items, more: more };
    }
    var one = reminderItem(d, o, kind === "timer");
    if (!one) return null;
    return { kind: one.timer ? "timer" : "reminder", title: tr(o.lang, one.timer ? "timer" : "reminder"), list: false, items: [one], more: 0 };
  }

  function listModel(d, o) {
    var raw = Array.isArray(d.items) ? d.items : null;
    var name = cleanText(d.name, 60);
    if (!raw && !name) return null;
    var items = [];
    (raw || []).slice(0, ITEMS_MAX).forEach(function (s) {
      var c = cleanText(s, 200);
      if (c) items.push(c);
    });
    return {
      kind: "list",
      id: typeof d.id === "string" && LIST_ID.test(d.id) ? d.id : "",
      icon: d.kind === "todo" ? ICONS.todo : ICONS.list,
      title: name || tr(o.lang, "list"),
      items: items,
      more: raw ? Math.max(0, raw.length - ITEMS_MAX) : 0,
      empty: tr(o.lang, "emptyList"),
    };
  }

  function docsModel(d, o) {
    var seen = {};
    var names = [];
    arr(d.hits, 50).forEach(function (h) {
      if (!isObj(h) || names.length >= DOCS_MAX) return;
      var name = cleanText(h.name, TITLE_MAX);
      if (!name) return;
      var key = typeof h.doc === "string" && h.doc ? "d:" + h.doc : "n:" + name.toLowerCase();
      if (seen[key]) return;
      seen[key] = true;
      names.push(name);
    });
    if (!names.length) return null;
    return { kind: "docs", title: tr(o.lang, "docs"), names: names };
  }

  function linkModel(d, o) {
    var href = safeHref(d.url);
    var title = cleanText(d.title, 200);
    var site = cleanText(d.site, 80) || (href ? hostOf(href) : "");
    if (!title && !site) return null;
    return { kind: "link", title: title || site, site: title ? site : "", href: href, paywall: d.paywall === true, label: tr(o.lang, "link") };
  }

  function memoryModel(d, o) {
    if (d.deleted === true) return { kind: "memory", title: tr(o.lang, "memoryDeleted"), text: "", deleted: true };
    var text = cleanText(d.text, 240);
    if (!text) return null;
    return { kind: "memory", title: tr(o.lang, "memorySaved"), text: text, deleted: false };
  }

  function intOrNull(v) {
    return typeof v === "number" && isFinite(v) ? Math.round(v) : null;
  }

  function weatherModel(d, o) {
    var city = cleanText(d.city, 80);
    var now = null;
    if (isObj(d.now) && intOrNull(d.now.temp) !== null) {
      now = {
        temp: intOrNull(d.now.temp),
        feelsLike: intOrNull(d.now.feelsLike),
        wind: intOrNull(d.now.wind),
        condition: cleanText(d.now.condition, 40),
        icon: wmoIcon(d.now.code),
      };
    }
    var days = [];
    arr(d.days, 7).forEach(function (x) {
      if (!isObj(x)) return;
      var max = intOrNull(x.max);
      var min = intOrNull(x.min);
      var label = cleanText(x.label, 30) || (typeof x.date === "string" ? dayLabel(x.date, "", o.lang) : "");
      if (!label || (max === null && min === null)) return;
      var rain = intOrNull(x.rain);
      days.push({
        label: label,
        max: max,
        min: min,
        rain: rain !== null ? Math.max(0, Math.min(100, rain)) : null,
        condition: cleanText(x.condition, 40),
        icon: wmoIcon(x.code),
      });
    });
    if (!now && !days.length) return null;
    var title = city ? tr(o.lang, "weather") + " · " + city : tr(o.lang, "weather");
    return { kind: "weather", title: title, now: now, days: days, rainFocus: d.rain === true, lang: o.lang };
  }

  function newsModel(d, o) {
    var items = [];
    arr(d.items, 10).forEach(function (x) {
      if (!isObj(x) || items.length >= 5) return;
      var title = cleanText(x.title, 200);
      if (!title) return;
      items.push({ title: title, topline: cleanText(x.topline, 80) });
    });
    if (!items.length) return null;
    var topic = cleanText(d.topic, 60);
    var src = d.source === "tagesschau" || d.source === "googlenews" ? tr(o.lang, "src_" + d.source) : "";
    return { kind: "news", title: topic ? tr(o.lang, "newsTopic", { topic: topic }) : tr(o.lang, "news"), source: src, items: items };
  }

  /*
   * The view model of a card, or null when there is nothing to show (unknown
   * kind, malformed data). opts: {lang:"de"|"en", now: ms}.
   */
  function model(kind, data, opts) {
    opts = opts || {};
    var o = { lang: lang2(opts.lang), now: typeof opts.now === "number" ? opts.now : Date.now() };
    if (typeof kind !== "string" || !isObj(data)) return null;
    switch (kind) {
      case "briefing":
        return briefingModel(data, o);
      case "calendar":
        return calendarModel(data, o);
      case "home":
        return homeModel(data, o);
      case "reminder":
      case "timer":
        return reminderModel(kind, data, o);
      case "list":
        return listModel(data, o);
      case "docs":
        return docsModel(data, o);
      case "link":
        return linkModel(data, o);
      case "memory":
        return memoryModel(data, o);
      case "weather":
        return weatherModel(data, o);
      case "news":
        return newsModel(data, o);
    }
    return null;
  }

  var helpers = {
    STRINGS: STRINGS,
    ICONS: ICONS,
    CARDS_PER_MESSAGE: CARDS_PER_MESSAGE,
    SNOOZE_MINUTES: SNOOZE_MINUTES,
    REVEAL_MAX_MS: 6 * 3600 * 1000,
    snoozeAllowed: snoozeAllowed,
    REMINDER_ID: REMINDER_ID,
    LIST_ID: LIST_ID,
    ITEM_ID: ITEM_ID,
    tr: tr,
    cleanText: cleanText,
    wallClock: wallClock,
    offsetMinutes: offsetMinutes,
    todayIn: todayIn,
    dayLabel: dayLabel,
    countdown: countdown,
    parseWeather: parseWeather,
    wmoIcon: wmoIcon,
    safeHref: safeHref,
    findOpenItem: findOpenItem,
    model: model,
  };
  return helpers;
});

// ====================================================================== DOM

(function () {
  "use strict";
  if (typeof window === "undefined" || typeof document === "undefined" || !window.Hermes || !window.HermesCards) return;
  var win = window;
  var doc = document;
  var H = win.Hermes;
  var C = win.HermesCards;

  function lang() {
    return H.lang();
  }
  function t(key, vars) {
    return C.tr(lang(), key, vars);
  }
  function api() {
    return H.api || {};
  }
  function fetchJSON(url, o) {
    if (typeof api().fetchJSON === "function") return api().fetchJSON(url, o);
    return Promise.reject(Object.assign(new Error("not ready"), { status: 0 }));
  }
  function toast(msg, o) {
    try {
      if (typeof api().toast === "function") api().toast(msg, o || {});
    } catch (e) {
      /* ignore */
    }
  }

  /* createElement with text-only content; never parses markup. */
  function el(tag, cls, text) {
    var e = doc.createElement(tag);
    if (cls) e.className = cls;
    if (text != null && text !== "") e.textContent = String(text);
    return e;
  }
  function icon(ch) {
    var s = el("span", "hc-icon", ch);
    s.setAttribute("aria-hidden", "true");
    return s;
  }

  function shell(m, iconChar, extraClass) {
    var card = el("div", "hcard hcard-" + m.kind + (extraClass ? " " + extraClass : ""));
    card.setAttribute("role", "group");
    card.setAttribute("aria-label", m.title);
    var head = el("div", "hc-head");
    head.appendChild(icon(iconChar));
    head.appendChild(el("span", "hc-title", m.title));
    card.appendChild(head);
    return card;
  }

  function moreRow(n) {
    return n > 0 ? el("div", "hc-more", t("more", { n: n })) : null;
  }

  // ---------------------------------------------------------------- briefing

  function renderBriefing(m) {
    var card = shell(m, C.ICONS.briefing);
    if (m.greeting) card.appendChild(el("div", "hc-greeting", m.greeting));
    m.rows.forEach(function (r) {
      if (r.collapsed) {
        var det = el("details", "hc-sec hc-collapse");
        var sum = el("summary", "hc-sec-label");
        sum.appendChild(icon(r.icon));
        sum.appendChild(el("span", "", r.label || r.name));
        det.appendChild(sum);
        det.appendChild(el("p", "hc-sec-text", r.text));
        card.appendChild(det);
        return;
      }
      if (r.name === "weather") {
        var w = el("div", "hc-weather");
        var big = el("span", "hc-wx-icon", r.icon);
        big.setAttribute("aria-hidden", "true");
        w.appendChild(big);
        if (r.temp !== null && r.temp !== undefined) w.appendChild(el("span", "hc-wx-temp", r.temp + "°"));
        w.appendChild(el("span", "hc-wx-text", r.text));
        card.appendChild(w);
        return;
      }
      var row = el("div", "hc-sec");
      var label = el("div", "hc-sec-label");
      label.appendChild(icon(r.icon));
      if (r.label) label.appendChild(el("span", "", r.label));
      row.appendChild(label);
      row.appendChild(el("p", "hc-sec-text", r.text));
      card.appendChild(row);
    });
    return card;
  }

  // ---------------------------------------------------------------- calendar

  function renderCalendar(m) {
    var card = shell(m, C.ICONS.calendar, m.added ? "hc-ok" : "");
    if (!m.groups.length) {
      card.appendChild(el("div", "hc-empty", m.empty));
      return card;
    }
    m.groups.forEach(function (g) {
      card.appendChild(el("div", "hc-day", g.label));
      var ul = el("ul", "hc-events");
      g.items.forEach(function (it) {
        var li = el("li", "hc-event");
        li.appendChild(el("span", "hc-time" + (it.allDay ? " hc-allday" : ""), it.time));
        var body = el("span", "hc-ev-body");
        body.appendChild(el("span", "hc-ev-title", it.title));
        if (it.invite) body.appendChild(el("span", "hc-badge", t("invite")));
        if (it.location) body.appendChild(el("span", "hc-ev-loc", it.location));
        li.appendChild(body);
        ul.appendChild(li);
      });
      card.appendChild(ul);
    });
    var more = moreRow(m.more);
    if (more) card.appendChild(more);
    return card;
  }

  // ---------------------------------------------------------------- home

  function renderHome(m) {
    var card = shell(m, C.ICONS.home, m.refused ? "hc-refused" : "");
    var row = el("div", "hc-home-row");
    if (m.targets) row.appendChild(el("span", "hc-home-target", m.targets));
    if (m.state) row.appendChild(el("span", "hc-state" + (m.refused ? " hc-state-warn" : ""), m.state));
    card.appendChild(row);
    if (m.via) card.appendChild(el("div", "hc-via", m.via));
    return card;
  }

  // ---------------------------------------------------------------- reminders

  // one ticker for every visible countdown; it stops when none is left
  var ticking = [];
  var tickTimer = 0;
  function tick() {
    var now = Date.now();
    ticking = ticking.filter(function (c) {
      if (!c.node.isConnected || c.stopped) return false;
      c.node.textContent = c.due - now > 0 ? C.countdown(c.due, now) : t("expired");
      return c.due - now > 0;
    });
    if (!ticking.length && tickTimer) {
      clearInterval(tickTimer);
      tickTimer = 0;
    }
  }
  function startCountdown(node, due) {
    var c = { node: node, due: due, stopped: false };
    ticking.push(c);
    if (!tickTimer) tickTimer = setInterval(tick, 1000);
    // first paint right away (the node is attached by then: next frame)
    setTimeout(tick, 0);
    return c;
  }

  function reminderRow(it, single) {
    var row = el(single ? "div" : "li", "hc-rem" + (it.private ? " hc-private" : ""));
    var body = el("div", "hc-rem-body");
    body.appendChild(el("span", "hc-rem-text", it.text));
    var when = el("span", "hc-rem-when");
    if (it.dueText) when.appendChild(el("span", "", it.dueText));
    if (it.ruleText) when.appendChild(el("span", "hc-rem-rule", "↻ " + it.ruleText));
    var cd = null;
    if (it.timer && it.due !== null) {
      var count = el("span", "hc-countdown", C.countdown(it.due, Date.now()));
      count.setAttribute("role", "timer");
      when.appendChild(count);
      cd = startCountdown(count, it.due);
    }
    if (it.person) when.appendChild(el("span", "hc-rem-person", it.person));
    body.appendChild(when);
    row.appendChild(body);
    if (!it.id) return row;

    var acts = el("div", "hc-actions");
    var status = el("span", "hc-status");
    status.setAttribute("role", "status");
    var what = it.text;
    var done = el("button", "small", t("done"));
    done.type = "button";
    done.setAttribute("aria-label", t("doneAria", { what: what }));
    var snz = el("button", "small ghost", t("snooze", { n: C.SNOOZE_MINUTES }));
    snz.type = "button";
    snz.setAttribute("aria-label", t("snoozeAria", { what: what, n: C.SNOOZE_MINUTES }));

    function busy(on) {
      done.disabled = on;
      snz.disabled = on;
    }
    function finish(msg) {
      if (cd) cd.stopped = true;
      if (revealTimer) clearTimeout(revealTimer);
      status.classList.remove("hc-warn");
      acts.textContent = "";
      status.textContent = msg;
      acts.appendChild(status);
      row.classList.add("hc-settled");
    }
    function fail(err) {
      busy(false);
      if (err && err.status === 404) return finish(t("gone"));
      status.textContent = t("failed");
      status.classList.add("hc-warn");
    }
    var base = "/api/reminders/" + encodeURIComponent(it.id);
    done.addEventListener("click", function () {
      busy(true);
      status.textContent = "";
      fetchJSON(base + "/done", { method: "POST", body: it.dueIso ? { due: it.dueIso } : {} }).then(function (res) {
        finish(res && res.stale ? t("gone") : t("markedDone"));
      }, fail);
    });
    snz.addEventListener("click", function () {
      busy(true);
      status.textContent = "";
      fetchJSON(base + "/snooze", { method: "POST", body: { minutes: C.SNOOZE_MINUTES } }).then(function () {
        finish(t("snoozed", { n: C.SNOOZE_MINUTES }));
      }, fail);
    });
    // snoozing moves the due time to now + 10 min, so it is only offered
    // once the reminder is due (a card for "tomorrow at 8" must not be
    // pulled forward); a pending one reveals the button when it comes due
    // Done on a series before it rings would complete (skip) the next
    // occurrence, so a recurring card offers it only once due, like snooze
    var revealTimer = 0;
    var now = Date.now();
    snz.hidden = !C.snoozeAllowed(it.due, now);
    if (it.recurring) done.hidden = snz.hidden;
    if (snz.hidden && it.due !== null && it.due - now < C.REVEAL_MAX_MS) {
      revealTimer = setTimeout(function () {
        revealTimer = 0;
        if (row.isConnected && !row.classList.contains("hc-settled")) {
          snz.hidden = false;
          if (it.recurring) done.hidden = false;
        }
      }, it.due - now + 250);
    }
    acts.appendChild(done);
    acts.appendChild(snz);
    acts.appendChild(status);
    row.appendChild(acts);
    return row;
  }

  function renderReminder(m) {
    var card = shell(m, m.kind === "timer" ? C.ICONS.timer : C.ICONS.reminder);
    if (!m.list) {
      card.appendChild(reminderRow(m.items[0], true));
      return card;
    }
    var ul = el("ul", "hc-rems");
    m.items.forEach(function (it) {
      ul.appendChild(reminderRow(it, false));
    });
    card.appendChild(ul);
    var more = moreRow(m.more);
    if (more) card.appendChild(more);
    return card;
  }

  // ---------------------------------------------------------------- list

  var itemSeq = 0;

  function renderList(m) {
    var card = shell(m, m.icon);
    if (!m.items.length) {
      card.appendChild(el("div", "hc-empty", m.empty));
      return card;
    }
    var ul = el("ul", "hc-items");
    m.items.forEach(function (text) {
      var li = el("li", "hc-item");
      if (!m.id) {
        li.appendChild(el("span", "hc-item-text", text));
        ul.appendChild(li);
        return;
      }
      var id = "hc-item-" + ++itemSeq;
      var box = el("input", "hc-check");
      box.type = "checkbox";
      box.id = id;
      box.setAttribute("aria-label", t("itemAria", { what: text }));
      var label = el("label", "hc-item-text", text);
      label.htmlFor = id;
      var itemId = "";
      box.addEventListener("change", function () {
        var want = box.checked;
        box.disabled = true;
        var find = itemId
          ? Promise.resolve(itemId)
          : fetchJSON("/api/lists").then(function (resp) {
              return C.findOpenItem(resp, m.id, text);
            });
        find
          .then(function (iid) {
            if (!iid) {
              var e = new Error("gone");
              e.status = 404;
              throw e;
            }
            itemId = iid;
            return fetchJSON("/api/lists/" + encodeURIComponent(m.id) + "/items/" + encodeURIComponent(iid), {
              method: "PATCH",
              body: { done: want },
            });
          })
          .then(
            function () {
              box.disabled = false;
              li.classList.toggle("hc-item-done", want);
            },
            function (err) {
              box.disabled = false;
              box.checked = !want;
              if (err && err.status === 404) {
                li.classList.add("hc-item-gone");
                box.disabled = true;
                toast(t("itemGone"), { type: "warn", key: "cards-list" });
              } else {
                toast(t("failed"), { type: "warn", key: "cards-list" });
              }
            }
          );
      });
      li.appendChild(box);
      li.appendChild(label);
      ul.appendChild(li);
    });
    card.appendChild(ul);
    var more = moreRow(m.more);
    if (more) card.appendChild(more);
    return card;
  }

  // ---------------------------------------------------------------- docs, link, memory

  function renderDocs(m) {
    var card = shell(m, C.ICONS.docs);
    var ul = el("ul", "hc-docs");
    m.names.forEach(function (n) {
      ul.appendChild(el("li", "hc-doc", n));
    });
    card.appendChild(ul);
    return card;
  }

  function renderLink(m) {
    var card = el("div", "hcard hcard-link");
    card.setAttribute("role", "group");
    card.setAttribute("aria-label", m.label);
    card.appendChild(icon(C.ICONS.link));
    var body = el(m.href ? "a" : "div", "hc-link-body");
    if (m.href) {
      body.href = m.href;
      body.target = "_blank";
      body.rel = "noopener noreferrer";
      body.referrerPolicy = "no-referrer";
    }
    if (m.site) body.appendChild(el("span", "hc-link-site", m.site));
    body.appendChild(el("span", "hc-link-title", m.title));
    card.appendChild(body);
    if (m.paywall) card.appendChild(el("span", "hc-badge hc-badge-warn", t("paywall")));
    return card;
  }

  function renderMemory(m) {
    var card = shell(m, C.ICONS.memory, m.deleted ? "hc-muted" : "");
    if (m.text) card.appendChild(el("p", "hc-sec-text", m.text));
    return card;
  }

  // ---------------------------------------------------------------- weather, news

  function renderWeather(m) {
    var card = shell(m, C.ICONS.weather);
    if (m.now) {
      var w = el("div", "hc-weather");
      var big = el("span", "hc-wx-icon", m.now.icon);
      big.setAttribute("aria-hidden", "true");
      w.appendChild(big);
      w.appendChild(el("span", "hc-wx-temp", m.now.temp + "°"));
      var parts = [];
      if (m.now.condition) parts.push(m.now.condition);
      if (m.now.feelsLike !== null) parts.push(t("feelsLike", { n: m.now.feelsLike }));
      if (m.now.wind !== null) parts.push(t("wind", { n: m.now.wind }));
      w.appendChild(el("span", "hc-wx-text", parts.join(" · ")));
      card.appendChild(w);
    }
    if (m.days.length) {
      var ul = el("ul", "hc-wx-days");
      m.days.forEach(function (d) {
        var li = el("li", "hc-wx-day");
        li.appendChild(el("span", "hc-wx-label", d.label));
        var ic = el("span", "hc-wx-dicon", d.icon);
        ic.setAttribute("aria-hidden", "true");
        li.appendChild(ic);
        var temps = (d.max !== null ? d.max + "°" : "–") + " / " + (d.min !== null ? d.min + "°" : "–");
        li.appendChild(el("span", "hc-wx-range", temps));
        var rest = [];
        if (d.condition) rest.push(d.condition);
        if (d.rain !== null) rest.push(t("rainChance", { n: d.rain }));
        li.appendChild(el("span", "hc-wx-cond" + (m.rainFocus && d.rain !== null && d.rain >= 50 ? " hc-warn" : ""), rest.join(" · ")));
        ul.appendChild(li);
      });
      card.appendChild(ul);
    }
    return card;
  }

  function renderNews(m) {
    var card = shell(m, C.ICONS.news);
    var ul = el("ul", "hc-news");
    m.items.forEach(function (it) {
      var li = el("li", "hc-news-item");
      if (it.topline) li.appendChild(el("span", "hc-news-top", it.topline));
      li.appendChild(el("span", "hc-news-title", it.title));
      ul.appendChild(li);
    });
    card.appendChild(ul);
    if (m.source) card.appendChild(el("div", "hc-via", m.source));
    return card;
  }

  var RENDER = {
    weather: renderWeather,
    news: renderNews,
    briefing: renderBriefing,
    calendar: renderCalendar,
    home: renderHome,
    reminder: renderReminder,
    timer: renderReminder,
    list: renderList,
    docs: renderDocs,
    link: renderLink,
    memory: renderMemory,
  };

  /* A card element for {kind, data}, or null. */
  function build(card) {
    if (!card || typeof card !== "object") return null;
    var m = C.model(card.kind, card.data, { lang: lang(), now: Date.now() });
    if (!m || !RENDER[m.kind]) return null;
    return RENDER[m.kind](m);
  }

  /* The HERMES message a card belongs to. */
  function targetMessage(msgId) {
    var box = doc.getElementById("messages");
    if (!box) return null;
    var list = box.querySelectorAll(".msg.bot");
    if (typeof msgId === "string" && msgId) {
      for (var i = list.length - 1; i >= 0; i--) if (list[i].dataset.id === msgId) return list[i];
      return null;
    }
    for (var j = list.length - 1; j >= 0; j--) if (list[j].classList.contains("streaming")) return list[j];
    for (var k = list.length - 1; k >= 0; k--) if (!list[k].classList.contains("notice")) return list[k];
    return null;
  }

  /* Insert a card under the bubble of message element `msg` (before its meta row). */
  function renderInto(msg, card) {
    if (!msg) return null;
    var box = msg.querySelector(":scope > .hcards");
    if (box && box.children.length >= C.CARDS_PER_MESSAGE) return null;
    var node = build(card);
    if (!node) return null;
    if (!box) {
      box = el("div", "hcards");
      var meta = msg.querySelector(":scope > .msg-meta");
      if (meta) msg.insertBefore(box, meta);
      else msg.appendChild(box);
    }
    box.appendChild(node);
    return node;
  }

  H.on("card", function (c) {
    if (!c || typeof c !== "object") return;
    try {
      var msg = targetMessage(c.msgId);
      if (!msg) return;
      var chat = doc.getElementById("chat");
      // keep the view pinned to the bottom when it was there already
      var atBottom = chat ? chat.scrollHeight - chat.scrollTop - chat.clientHeight < 48 : false;
      if (renderInto(msg, c) && atBottom && chat) chat.scrollTop = chat.scrollHeight;
    } catch (e) {
      if (win.console) win.console.error("[cards]", e);
    }
  });

  C.build = build;
  C.renderInto = renderInto;
})();
