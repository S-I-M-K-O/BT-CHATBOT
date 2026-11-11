"use strict";
// Run: node --test web/tests/
// web/static/cards.js (HermesCards): pure helpers, view models for every card
// kind in DE and EN, and the DOM renderer against a tiny fake document
// (which throws on innerHTML, so server text can only arrive as text).
const test = require("node:test");
const assert = require("node:assert/strict");
const path = require("node:path");
const C = require("../static/cards.js");

// 2026-11-10 08:00 in Berlin (+01:00)
const NOW = Date.parse("2026-11-10T07:00:00Z");

test("cleanText: strings only, controls and bidi stripped, capped by code points", () => {
  assert.equal(C.cleanText("  Milch \n\t Eier  "), "Milch Eier");
  assert.equal(C.cleanText("a‮b\u0000c"), "a b c");
  assert.equal(C.cleanText(null), "");
  assert.equal(C.cleanText({ toString: () => "x" }), "");
  assert.equal(C.cleanText(7), "7");
  assert.equal(C.cleanText("😀".repeat(10), 5), "😀😀😀😀…");
  assert.ok(Array.from(C.cleanText("x".repeat(100000), 300)).length <= 300);
});

test("wallClock: date and time as written, offsets ignored", () => {
  assert.deepEqual(C.wallClock("2026-11-10T09:30:00+01:00"), { date: "2026-11-10", time: "09:30" });
  assert.deepEqual(C.wallClock("2026-11-10T23:15:00.123456789-05:00"), { date: "2026-11-10", time: "23:15" });
  assert.deepEqual(C.wallClock("2026-11-10T00:00:00Z"), { date: "2026-11-10", time: "00:00" });
  assert.deepEqual(C.wallClock("2026-11-12"), { date: "2026-11-12", time: "" });
  for (const bad of ["", "tomorrow", "2026-13-01T00:00:00Z", "2026-11-10T25:00:00Z", 12, null, "x".repeat(50)]) {
    assert.equal(C.wallClock(bad), null, String(bad));
  }
});

test("todayIn: the card's zone wins over the browser's", () => {
  // 23:30 UTC on Nov 10 is already Nov 11 in Berlin, still Nov 10 in New York
  const late = Date.parse("2026-11-10T23:30:00Z");
  assert.equal(C.todayIn(late, "2026-11-11T00:00:00+01:00"), "2026-11-11");
  assert.equal(C.todayIn(late, "2026-11-10T00:00:00-05:00"), "2026-11-10");
  assert.equal(C.todayIn(late, "2026-11-10T00:00:00Z"), "2026-11-10");
  assert.match(C.todayIn(late, ""), /^\d{4}-\d{2}-\d{2}$/);
});

test("dayLabel: today/tomorrow/yesterday, else weekday + date per language", () => {
  assert.equal(C.dayLabel("2026-11-10", "2026-11-10", "de"), "Heute");
  assert.equal(C.dayLabel("2026-11-11", "2026-11-10", "de"), "Morgen");
  assert.equal(C.dayLabel("2026-11-09", "2026-11-10", "en"), "Yesterday");
  assert.equal(C.dayLabel("2026-11-11", "2026-11-10", "en"), "Tomorrow");
  assert.equal(C.dayLabel("2026-11-12", "2026-11-10", "de"), "Do. 12.11.");
  assert.equal(C.dayLabel("2026-11-12", "2026-11-10", "en"), "Thu, Nov 12");
  assert.equal(C.dayLabel("2027-01-01", "2026-12-31", "en"), "Tomorrow"); // year change
  assert.equal(C.dayLabel("garbage", "2026-11-10", "en"), "");
});

test("countdown: m:ss, h:mm:ss, never negative", () => {
  assert.equal(C.countdown(NOW + 5 * 60000, NOW), "5:00");
  assert.equal(C.countdown(NOW + 61500, NOW), "1:02");
  assert.equal(C.countdown(NOW + 3723000, NOW), "1:02:03");
  assert.equal(C.countdown(NOW - 1000, NOW), "0:00");
  assert.equal(C.countdown(NaN, NOW), "");
});

test("snoozeAllowed: only once due", () => {
  assert.equal(C.snoozeAllowed(NOW - 1, NOW), true);
  assert.equal(C.snoozeAllowed(NOW, NOW), true);
  assert.equal(C.snoozeAllowed(NOW + 60000, NOW), false);
  assert.equal(C.snoozeAllowed(null, NOW), false);
  assert.equal(C.snoozeAllowed(NaN, NOW), false);
});

test("parseWeather: temperature and icon from the DE and EN sentences", () => {
  let w = C.parseWeather("In Berlin sind es minus 2 Grad, Schnee. Heute bis 1 Grad.");
  assert.equal(w.temp, -2);
  assert.equal(w.icon, "❄️");
  assert.equal(w.condition, "Schnee");
  w = C.parseWeather("In Munich it is 6 degrees, overcast. Today up to 9 degrees, 40 percent chance of rain.");
  assert.equal(w.temp, 6);
  assert.equal(w.icon, "☁️"); // the rain chance does not pick the icon
  w = C.parseWeather("In London it is 1 degree, partly cloudy.");
  assert.equal(w.temp, 1);
  assert.equal(w.icon, "⛅");
  // the city name is not the condition
  w = C.parseWeather("In Regensburg sind es 12 Grad, klar.");
  assert.equal(w.icon, "☀️");
  w = C.parseWeather("In Regensburg sind es 0 Grad.");
  assert.equal(w.temp, 0);
  assert.equal(w.icon, "🌡️");
  assert.equal(C.parseWeather("minus 0 Grad").temp, 0);
  w = C.parseWeather("Für das Wetter trage in den Einstellungen deinen Wohnort ein.");
  assert.equal(w.temp, null);
  assert.equal(C.parseWeather(undefined).temp, null);
});

test("safeHref: only plain http(s) without credentials", () => {
  assert.equal(C.safeHref("https://www.heise.de/news/x.html"), "https://www.heise.de/news/x.html");
  for (const bad of [
    "javascript:alert(1)",
    "JaVaScRiPt:alert(1)",
    "data:text/html,<b>x</b>",
    "https://user:pw@example.com/",
    "//example.com/x",
    "https://exa mple.com/",
    "https://example.com/\u0000",
    "https://" + "a".repeat(2100) + ".com",
    "",
    42,
  ]) {
    assert.equal(C.safeHref(bad), "", String(bad));
  }
});

test("findOpenItem: first open item with the text, case-insensitive, in that list", () => {
  const resp = {
    rev: 3,
    lists: [
      { id: "todo", items: [{ id: "i_aaaa", text: "Milch", done: false }] },
      {
        id: "shopping",
        items: [
          { id: "i_done01", text: "Milch", done: true },
          { id: "i_open02", text: " milch ", done: false },
          { id: "bad id", text: "Eier", done: false },
        ],
      },
    ],
  };
  assert.equal(C.findOpenItem(resp, "shopping", "Milch"), "i_open02");
  assert.equal(C.findOpenItem(resp, "shopping", "Eier"), ""); // invalid id is never used in a URL
  assert.equal(C.findOpenItem(resp, "shopping", "Brot"), "");
  assert.equal(C.findOpenItem(null, "shopping", "Milch"), "");
  assert.equal(C.findOpenItem(resp, "shopping", ""), "");
});

test("model: unknown kinds and bad data render nothing", () => {
  for (const [k, d] of [
    ["person", { name: "Anna" }],
    ["undo", { label: "x" }],
    ["nope", {}],
    [null, {}],
    ["calendar", null],
    ["calendar", []],
    ["calendar", "x"],
    ["calendar", {}],
    ["briefing", {}],
    ["briefing", { sections: [{ name: "news", text: "" }] }],
    ["home", {}],
    ["reminder", {}],
    ["reminder", { reminders: [] }],
    ["list", {}],
    ["docs", { hits: [] }],
    ["docs", { hits: [{ n: 1 }] }],
    ["link", { url: "javascript:alert(1)" }],
    ["memory", { id: "m_1" }],
  ]) {
    assert.equal(C.model(k, d, { lang: "de", now: NOW }), null, k + " " + JSON.stringify(d));
  }
});

test("model briefing: greeting, weather with temp, collapsed news", () => {
  const data = {
    sections: [
      { name: "greeting", text: "Guten Morgen, Anna. Heute ist Dienstag, der 10. November." },
      { name: "weather", text: "In Berlin sind es 4 Grad, Regen. Heute bis 7 Grad." },
      { name: "calendar", text: "Heute: 9:30 Uhr Zahnarzt." },
      { name: "reminders", text: "Um 12 Uhr: Paket abholen." },
      { name: "lists", text: "Auf der Einkaufsliste: Milch, Eier." },
      { name: "news", text: "Schlagzeilen: A. B. Mehr Schlagzeilen?" },
      { name: "custom", text: "Something else" },
      { name: "weird", text: 42 },
      "junk",
    ],
  };
  const m = C.model("briefing", data, { lang: "de", now: NOW });
  assert.equal(m.title, "Dein Tag");
  assert.match(m.greeting, /^Guten Morgen, Anna/);
  assert.deepEqual(
    m.rows.map((r) => r.name),
    ["weather", "calendar", "reminders", "lists", "news", "custom", "weird"]
  );
  assert.equal(m.rows[0].temp, 4);
  assert.equal(m.rows[0].icon, "🌧️");
  assert.equal(m.rows[1].label, "Kalender");
  assert.equal(m.rows[4].collapsed, true);
  assert.equal(m.rows[4].label, "Schlagzeilen");
  assert.equal(m.rows[1].collapsed, false);
  const en = C.model("briefing", { sections: [{ name: "news", text: "Headlines: X." }] }, { lang: "en", now: NOW });
  assert.equal(en.title, "Your day");
  assert.equal(en.rows[0].label, "Headlines");
});

test("model calendar: grouped by day, times, all-day, invitation badge, caps", () => {
  const data = {
    from: "2026-11-10T00:00:00+01:00",
    to: "2026-11-12T00:00:00+01:00",
    events: [
      { id: "1", title: "Zahnarzt", location: "Praxis Dr. Weiß", start: "2026-11-10T09:30:00+01:00", end: "2026-11-10T10:00:00+01:00", allDay: false },
      { id: "2", title: "Team-Sync", start: "2026-11-10T14:00:00+01:00", end: "2026-11-10T14:00:00+01:00", needsAction: true },
      { id: "3", title: "", start: "2026-11-11T00:00:00+01:00", end: "2026-11-12T00:00:00+01:00", allDay: true },
      { id: "4", title: "Late", start: "2026-11-11T23:00:00+01:00", end: "2026-11-12T01:00:00+01:00" },
      { id: "bad", title: "no start" },
      "junk",
    ],
  };
  const de = C.model("calendar", data, { lang: "de", now: NOW });
  assert.equal(de.title, "Kalender");
  assert.equal(de.groups.length, 2);
  assert.equal(de.groups[0].label, "Heute");
  assert.equal(de.groups[1].label, "Morgen");
  assert.deepEqual(
    de.groups[0].items.map((i) => [i.time, i.title, i.location, i.invite]),
    [
      ["09:30–10:00", "Zahnarzt", "Praxis Dr. Weiß", false],
      ["14:00", "Team-Sync", "", true],
    ]
  );
  assert.equal(de.groups[1].items[0].time, "ganztägig");
  assert.equal(de.groups[1].items[0].title, "(ohne Titel)");
  assert.equal(de.groups[1].items[1].time, "23:00"); // ends the next day: start only
  assert.equal(de.more, 0);
  assert.equal(C.tr("de", "invite"), "(Einladung)");

  const en = C.model("calendar", Object.assign({}, data, { added: true }), { lang: "en", now: NOW });
  assert.equal(en.title, "Added to the calendar");
  assert.equal(en.groups[0].label, "Today");
  assert.equal(en.groups[1].items[0].time, "all day");
  assert.equal(C.tr("en", "invite"), "(Invitation)");

  const many = { from: data.from, events: [] };
  for (let i = 0; i < 25; i++) many.events.push({ title: "E" + i, start: "2026-11-10T08:00:00+01:00", end: "2026-11-10T09:00:00+01:00" });
  const capped = C.model("calendar", many, { lang: "en", now: NOW });
  assert.equal(capped.groups[0].items.length, 20);
  assert.equal(capped.more, 5);

  const empty = C.model("calendar", { from: data.from, events: [] }, { lang: "de", now: NOW });
  assert.equal(empty.groups.length, 0);
  assert.equal(empty.empty, "Keine Termine.");
});

test("model home: targets, state, via; refused is a warning without via", () => {
  const de = C.model("home", { action: "off", targets: ["Küche", "Flur", 7, ""], via: "service" }, { lang: "de", now: NOW });
  assert.deepEqual([de.targets, de.state, de.via, de.refused], ["Küche, Flur, 7", "Aus", "über Dienst", false]);
  const en = C.model("home", { action: "set", targets: ["Thermostat"], via: "assist" }, { lang: "en", now: NOW });
  assert.deepEqual([en.state, en.via], ["Set", "via Assist"]);
  const q = C.model("home", { action: "query", targets: ["Fenster"], via: "states" }, { lang: "en", now: NOW });
  assert.equal(q.query, true);
  const odd = C.model("home", { action: "<b>explode</b>", targets: ["Lampe"], via: "evil" }, { lang: "en", now: NOW });
  assert.deepEqual([odd.state, odd.via], ["", ""]);
  const r = C.model("home", { refused: true, targets: ["Haustür"], via: "service", action: "open" }, { lang: "de", now: NOW });
  assert.deepEqual([r.refused, r.state, r.via], [true, "Nicht erlaubt", ""]);
});

test("model reminder/timer: single card, private, list form", () => {
  const due = "2026-11-10T07:05:00Z";
  const t = C.model("timer", { id: "r_abc123", text: "", due: due, dueText: "in 5 Minuten", kind: "timer", private: false }, { lang: "de", now: NOW });
  assert.equal(t.kind, "timer");
  assert.equal(t.title, "Timer");
  assert.equal(t.items[0].text, "Timer");
  assert.equal(t.items[0].due, Date.parse(due));
  assert.equal(t.items[0].id, "r_abc123");

  const r = C.model("reminder", { id: "r_x1", text: "Müll rausbringen", due: due, dueText: "today at 8:05", kind: "reminder" }, { lang: "en", now: NOW });
  assert.equal(r.kind, "reminder");
  assert.equal(r.items[0].text, "Müll rausbringen");
  assert.equal(r.items[0].timer, false);

  const p = C.model("reminder", { id: "r_x2", text: "secret", private: true, dueText: "morgen" }, { lang: "de", now: NOW });
  assert.equal(p.items[0].text, "Private Erinnerung"); // never the text
  assert.equal(p.items[0].private, true);

  const badId = C.model("reminder", { id: "../people", text: "x", dueText: "y" }, { lang: "en", now: NOW });
  assert.equal(badId.items[0].id, ""); // no buttons, no URL built from it

  const list = C.model(
    "reminder",
    {
      reminders: [
        { id: "r_1", kind: "timer", text: "", due: due, dueText: "in 5 min", personName: "Anna" },
        { id: "r_2", kind: "reminder", text: "Call mum", due: due, dueText: "at 9", private: true },
        null,
      ].concat(Array.from({ length: 12 }, (_, i) => ({ id: "r_m" + i, text: "R" + i, dueText: "later" }))),
    },
    { lang: "en", now: NOW }
  );
  assert.equal(list.list, true);
  assert.equal(list.title, "Reminders");
  assert.equal(list.items.length, 9); // 10 taken, the null dropped
  assert.equal(list.more, 5);
  assert.equal(list.items[0].timer, true);
  assert.equal(list.items[0].person, "Anna");
  assert.equal(list.items[1].text, "Private reminder");
});

test("model list: items capped, id checked, todo icon", () => {
  const items = Array.from({ length: 35 }, (_, i) => "Item " + i);
  const m = C.model("list", { id: "shopping", name: "Einkaufsliste", kind: "shopping", items: items, rev: 3 }, { lang: "de", now: NOW });
  assert.equal(m.id, "shopping");
  assert.equal(m.items.length, 30);
  assert.equal(m.more, 5);
  assert.equal(m.icon, "🛒");
  const todo = C.model("list", { id: "todo", name: "To-do", kind: "todo", items: [] }, { lang: "en", now: NOW });
  assert.equal(todo.icon, "✅");
  assert.equal(todo.items.length, 0);
  assert.equal(todo.empty, "The list is empty.");
  assert.equal(C.model("list", { id: "../x", name: "X", items: ["a"] }, { lang: "en" }).id, "");
  assert.equal(C.model("list", { id: "l_abc123", items: ["a"] }, { lang: "de" }).title, "Liste");
});

test("model docs/link/memory", () => {
  const d = C.model(
    "docs",
    { hits: [{ name: "Mietvertrag.pdf", n: 1, doc: "d_1" }, { name: "Mietvertrag.pdf", n: 2, doc: "d_1" }, { name: "Garantie", n: 1, doc: "d_2" }] },
    { lang: "de", now: NOW }
  );
  assert.deepEqual(d.names, ["Mietvertrag.pdf", "Garantie"]);
  assert.equal(d.title, "Quellen");

  const l = C.model("link", { url: "https://www.heise.de/a", title: "Neue CPUs", site: "heise online", paywall: true }, { lang: "de" });
  assert.deepEqual([l.href, l.title, l.site, l.paywall], ["https://www.heise.de/a", "Neue CPUs", "heise online", true]);
  const l2 = C.model("link", { url: "https://www.example.com/a", title: "" }, { lang: "en" });
  assert.deepEqual([l2.title, l2.site], ["example.com", ""]);
  const l3 = C.model("link", { url: "javascript:alert(1)", title: "Click" }, { lang: "en" });
  assert.equal(l3.href, ""); // shown, never as a link

  const m = C.model("memory", { id: "m_1", text: "Anna mag keinen Koriander", person: "p_1" }, { lang: "de" });
  assert.deepEqual([m.title, m.text], ["Gemerkt", "Anna mag keinen Koriander"]);
  const del = C.model("memory", { id: "m_1", deleted: true }, { lang: "en" });
  assert.equal(del.title, "Removed from memory");
});

test("model weather/news: realtime cards in DE and EN, caps and bad data", () => {
  const w = C.model(
    "weather",
    {
      city: "Berlin",
      rain: false,
      now: { temp: 6.4, feelsLike: 3, code: 3, condition: "bedeckt", wind: 12 },
      days: [{ label: "Heute", date: "2026-11-11", max: 11, min: 2, rain: 40, code: 61, condition: "Regen" }],
    },
    { lang: "de" }
  );
  assert.equal(w.title, "Wetter · Berlin");
  assert.deepEqual([w.now.temp, w.now.feelsLike, w.now.wind, w.now.icon], [6, 3, 12, "☁️"]);
  assert.deepEqual([w.days[0].label, w.days[0].max, w.days[0].min, w.days[0].rain, w.days[0].icon], ["Heute", 11, 2, 40, "🌧️"]);
  const we = C.model("weather", { city: "London", days: [{ date: "2026-11-12", max: 9, min: 4, code: 0, condition: "clear" }] }, { lang: "en" });
  assert.equal(we.title, "Weather · London");
  assert.equal(we.now, null);
  assert.equal(we.days[0].label, "Thu, Nov 12"); // no label: from the date
  assert.equal(we.days[0].rain, null);
  assert.equal(C.model("weather", { city: "X", days: [{ label: "Heute" }] }, { lang: "de" }), null);
  assert.equal(C.model("weather", { city: "X", now: { temp: "warm" } }, { lang: "de" }), null);
  assert.equal(C.wmoIcon(95), "⛈️");
  assert.equal(C.wmoIcon(73), "❄️");
  assert.equal(C.wmoIcon("x"), "🌡️");

  const n = C.model(
    "news",
    { source: "tagesschau", topic: "", items: [{ title: "A", topline: "Bundestag" }, { title: "" }, { title: "B" }, "x"] },
    { lang: "de" }
  );
  assert.equal(n.title, "Schlagzeilen");
  assert.equal(n.source, "tagesschau.de");
  assert.deepEqual(n.items, [{ title: "A", topline: "Bundestag" }, { title: "B", topline: "" }]);
  const nt = C.model("news", { source: "googlenews", topic: "football", items: [1, 2, 3, 4, 5, 6, 7].map((i) => ({ title: "H" + i })) }, { lang: "en" });
  assert.equal(nt.title, "News: football");
  assert.equal(nt.source, "Google News");
  assert.equal(nt.items.length, 5);
  assert.equal(C.model("news", { source: "evil", items: [{ title: "A" }] }, { lang: "en" }).source, "");
  assert.equal(C.model("news", { items: [] }, { lang: "en" }), null);
});

test("STRINGS: DE and EN have the same keys", () => {
  assert.deepEqual(Object.keys(C.STRINGS.de).sort(), Object.keys(C.STRINGS.en).sort());
});

// ---------------------------------------------------------------- DOM

/* A tiny DOM: enough for cards.js; innerHTML is a trap. */
function fakeDom() {
  class Node {
    constructor(tag) {
      this.tagName = String(tag).toUpperCase();
      this.children = [];
      this.parentNode = null;
      this.attrs = {};
      this.dataset = {};
      this.listeners = {};
      this._text = "";
      this.className = "";
      const self = this;
      this.classList = {
        add: (c) => {
          if (!self.classList.contains(c)) self.className = (self.className + " " + c).trim();
        },
        contains: (c) => self.className.split(/\s+/).includes(c),
        remove: (c) => {
          self.className = self.className.split(/\s+/).filter((x) => x && x !== c).join(" ");
        },
        toggle: (c, on) => {
          const has = self.classList.contains(c);
          if (on === undefined) on = !has;
          if (on && !has) self.classList.add(c);
          if (!on && has) self.className = self.className.split(/\s+/).filter((x) => x !== c).join(" ");
        },
      };
    }
    set innerHTML(v) {
      throw new Error("innerHTML used: " + v);
    }
    get innerHTML() {
      throw new Error("innerHTML read");
    }
    set textContent(v) {
      this.children = [];
      this._text = String(v);
    }
    get textContent() {
      return this._text + this.children.map((c) => c.textContent).join("");
    }
    get isConnected() {
      return true;
    }
    appendChild(c) {
      c.parentNode = this;
      this.children.push(c);
      return c;
    }
    insertBefore(c, ref) {
      c.parentNode = this;
      const i = this.children.indexOf(ref);
      if (i < 0) this.children.push(c);
      else this.children.splice(i, 0, c);
      return c;
    }
    setAttribute(k, v) {
      this.attrs[k] = String(v);
    }
    getAttribute(k) {
      return this.attrs[k];
    }
    addEventListener(n, fn) {
      (this.listeners[n] = this.listeners[n] || []).push(fn);
    }
    fire(n) {
      (this.listeners[n] || []).forEach((fn) => fn({ currentTarget: this }));
    }
    all() {
      return this.children.reduce((a, c) => a.concat([c], c.all()), []);
    }
    find(cls) {
      return this.all().filter((n) => n.classList.contains(cls));
    }
    querySelector(sel) {
      const m = /^:scope > \.([\w-]+)$/.exec(sel);
      if (!m) throw new Error("unsupported selector " + sel);
      return this.children.find((c) => c.classList.contains(m[1])) || null;
    }
    querySelectorAll(sel) {
      if (sel !== ".msg.bot") throw new Error("unsupported selector " + sel);
      return this.all().filter((n) => n.classList.contains("msg") && n.classList.contains("bot"));
    }
  }
  const messages = new Node("div");
  const chat = new Node("main");
  chat.scrollHeight = 100;
  chat.scrollTop = 0;
  chat.clientHeight = 100;
  const document = {
    createElement: (t) => new Node(t),
    getElementById: (id) => (id === "messages" ? messages : id === "chat" ? chat : null),
  };
  return { Node, document, messages };
}

function loadBrowser(fetchImpl) {
  const dom = fakeDom();
  const listeners = {};
  const Hermes = {
    api: { fetchJSON: fetchImpl, toast: () => {} },
    lang: () => Hermes._lang,
    _lang: "de",
    on: (n, fn) => {
      (listeners[n] = listeners[n] || []).push(fn);
    },
  };
  const g = globalThis;
  const saved = { window: g.window, document: g.document };
  g.window = { Hermes: Hermes, document: dom.document, console: console };
  g.document = dom.document;
  const file = path.resolve(__dirname, "../static/cards.js");
  delete require.cache[file];
  let lib;
  try {
    require(file);
    lib = g.window.HermesCards;
  } finally {
    g.window = saved.window;
    g.document = saved.document;
    if (saved.window === undefined) delete g.window;
    if (saved.document === undefined) delete g.document;
    delete require.cache[file];
  }
  return { dom, Hermes, lib, emit: (n, d) => (listeners[n] || []).forEach((fn) => fn(d)) };
}

function botMessage(dom, id, cls) {
  const msg = dom.document.createElement("div");
  msg.className = "msg bot " + (cls || "");
  msg.dataset.id = id;
  const bubble = dom.document.createElement("div");
  bubble.className = "bubble";
  msg.appendChild(bubble);
  const meta = dom.document.createElement("div");
  meta.className = "msg-meta";
  msg.appendChild(meta);
  dom.messages.appendChild(msg);
  return msg;
}

test("DOM: every kind renders as text only, under the streaming reply, before the meta row", () => {
  const env = loadBrowser(() => Promise.resolve({}));
  assert.equal(typeof env.lib.build, "function");
  const done = botMessage(env.dom, "a1", "");
  const live = botMessage(env.dom, "a2", "streaming");
  const evil = '<img src=x onerror="alert(1)">';
  const cards = [
    { kind: "briefing", data: { sections: [{ name: "greeting", text: evil }, { name: "weather", text: "In X sind es 3 Grad, klar." }, { name: "news", text: evil }] } },
    { kind: "calendar", data: { from: "2026-11-10T00:00:00+01:00", events: [{ title: evil, start: "2026-11-10T09:00:00+01:00", end: "2026-11-10T10:00:00+01:00", needsAction: true }] } },
    { kind: "home", data: { action: "on", targets: [evil], via: "assist" } },
    { kind: "link", data: { url: "javascript:alert(1)", title: evil, site: "x" } },
  ];
  cards.forEach((c) => env.emit("card", c));
  assert.equal(done.find("hcards").length, 0);
  const box = live.children[1];
  assert.ok(box.classList.contains("hcards"));
  assert.ok(live.children[2].classList.contains("msg-meta"));
  assert.equal(box.children.length, 4);
  assert.ok(box.textContent.includes(evil)); // shown literally, as text
  assert.ok(box.textContent.includes("(Einladung)"));
  assert.ok(box.textContent.includes("3°"));
  assert.equal(box.all().filter((n) => n.tagName === "A").length, 0); // unsafe URL: no anchor
  assert.equal(box.all().filter((n) => n.tagName === "DETAILS").length, 1); // news collapsed

  // the per-message cap
  env.emit("card", { kind: "memory", data: { text: "x" } });
  assert.equal(box.children.length, 4);
  // unknown kinds and garbage: nothing, no throw
  env.emit("card", { kind: "person", data: { name: "A" } });
  env.emit("card", null);
  env.emit("card", { kind: "calendar", data: "x" });
});

test("DOM: weather and news cards render as text only", () => {
  const env = loadBrowser(() => Promise.resolve({}));
  const live = botMessage(env.dom, "w1", "streaming");
  const evil = '<img src=x onerror="alert(1)">';
  env.emit("card", {
    kind: "weather",
    data: { city: evil, now: { temp: 6, feelsLike: 3, code: 3, condition: "bedeckt", wind: 12 }, days: [{ label: "Heute", max: 11, min: 2, rain: 40, code: 61, condition: evil }] },
  });
  env.emit("card", { kind: "news", data: { source: "tagesschau", items: [{ title: evil, topline: "Inland" }, { title: "Zwei" }] } });
  const box = live.children[1];
  assert.equal(box.children.length, 2);
  assert.ok(box.textContent.includes(evil));
  assert.ok(box.textContent.includes("6°"));
  assert.ok(box.textContent.includes("11° / 2°"));
  assert.ok(box.textContent.includes("Inland"));
  assert.ok(box.textContent.includes("tagesschau.de"));
  assert.equal(box.all().filter((n) => n.tagName === "IMG" || n.tagName === "A").length, 0);
});

test("DOM: msgId targets that message; a safe link gets rel=noopener", () => {
  const env = loadBrowser(() => Promise.resolve({}));
  const a = botMessage(env.dom, "a1", "");
  botMessage(env.dom, "a2", "streaming");
  env.Hermes._lang = "en";
  env.emit("card", { kind: "link", msgId: "a1", data: { url: "https://example.com/x", title: "T", site: "Example", paywall: true } });
  const link = a.all().find((n) => n.tagName === "A");
  assert.ok(link);
  assert.equal(link.href, "https://example.com/x");
  assert.equal(link.rel, "noopener noreferrer");
  assert.equal(link.target, "_blank");
  assert.ok(a.textContent.includes("Paywall"));
  env.emit("card", { kind: "link", msgId: "nope", data: { url: "https://example.com/x", title: "T" } });
});

test("DOM: reminder Done posts to /api/reminders/{id}/done and settles the row", async () => {
  const calls = [];
  const env = loadBrowser((url, o) => {
    calls.push([url, o && o.method, o && o.body]);
    return Promise.resolve({});
  });
  const msg = botMessage(env.dom, "a1", "streaming");
  env.emit("card", { kind: "reminder", data: { id: "r_abc", text: "Müll", dueText: "um 8", due: "2020-01-01T07:00:00Z" } });
  const buttons = msg.all().filter((n) => n.tagName === "BUTTON");
  assert.equal(buttons.length, 2);
  assert.equal(buttons[0].textContent, "Erledigt");
  buttons[0].fire("click");
  await new Promise((r) => setImmediate(r));
  // the shown occurrence goes along, so a stale card cannot finish a later one
  assert.deepEqual(calls, [["/api/reminders/r_abc/done", "POST", { due: "2020-01-01T07:00:00Z" }]]);
  assert.ok(msg.textContent.includes("Erledigt ✓"));
  assert.equal(msg.all().filter((n) => n.tagName === "BUTTON").length, 0);
});

// E2E b-2d: Done on a weekly series card before it rang moved the series a
// week ahead (the next occurrence was lost). A recurring card shows its rule
// and offers Done only once due; a stale answer is not shown as done.
test("DOM: a recurring reminder card shows its rule and hides Done until due", async () => {
  const calls = [];
  const env = loadBrowser((url, o) => {
    calls.push([url, o && o.method, o && o.body]);
    return Promise.resolve({ ok: true, stale: true });
  });
  const msg = botMessage(env.dom, "a1", "streaming");
  env.emit("card", {
    kind: "reminder",
    data: { id: "r_abc", text: "den Müll", dueText: "am Montag", due: "2999-01-01T07:00:00Z", rule: { freq: "weekly" }, ruleText: "jeden Montag um 8 Uhr" },
  });
  assert.ok(msg.textContent.includes("↻ jeden Montag um 8 Uhr"));
  let buttons = msg.all().filter((n) => n.tagName === "BUTTON");
  assert.equal(buttons[0].hidden, true, "Done hidden before the series is due");
  assert.equal(buttons[1].hidden, true);

  const msg2 = botMessage(env.dom, "a2", "streaming");
  env.emit("card", {
    kind: "reminder",
    msgId: "a2",
    data: { id: "r_abc", text: "den Müll", dueText: "am Montag", due: "2020-01-01T07:00:00Z", ruleText: "jeden Montag um 8 Uhr" },
  });
  buttons = msg2.all().filter((n) => n.tagName === "BUTTON");
  assert.equal(buttons[0].hidden, false, "Done offered once due");
  buttons[0].fire("click");
  await new Promise((r) => setImmediate(r));
  assert.deepEqual(calls, [["/api/reminders/r_abc/done", "POST", { due: "2020-01-01T07:00:00Z" }]]);
  assert.ok(msg2.textContent.includes("Diese Erinnerung gibt es nicht mehr."));
});

test("DOM: reminder Snooze failure keeps the buttons and shows a warning", async () => {
  const env = loadBrowser(() => Promise.reject(Object.assign(new Error("x"), { status: 500 })));
  env.Hermes._lang = "en";
  const msg = botMessage(env.dom, "a1", "streaming");
  env.emit("card", { kind: "reminder", data: { id: "r_abc", text: "Call", dueText: "at 9", due: "2020-01-01T09:00:00Z" } });
  const snooze = msg.all().filter((n) => n.tagName === "BUTTON")[1];
  assert.equal(snooze.textContent, "Snooze 10 min");
  assert.equal(snooze.hidden, false);
  snooze.fire("click");
  await new Promise((r) => setImmediate(r));
  assert.equal(snooze.disabled, false);
  assert.ok(msg.textContent.includes("That did not work"));
});

test("DOM: a reminder that is not due yet offers Done but no Snooze", () => {
  const env = loadBrowser(() => Promise.resolve({}));
  const msg = botMessage(env.dom, "a1", "streaming");
  // far ahead: no reveal timer is scheduled either
  env.emit("card", { kind: "reminder", data: { id: "r_abc", text: "Zahnarzt", dueText: "2099", due: "2099-01-01T09:00:00Z" } });
  const [done, snooze] = msg.all().filter((n) => n.tagName === "BUTTON");
  assert.equal(done.hidden, undefined);
  assert.equal(snooze.hidden, true);
  // no due time at all: no Snooze
  const msg2 = botMessage(env.dom, "a2", "streaming");
  env.emit("card", { kind: "reminder", msgId: "a2", data: { id: "r_abd", text: "X", dueText: "?" } });
  assert.equal(msg2.all().filter((n) => n.tagName === "BUTTON")[1].hidden, true);
});

test("DOM: checking a list item looks up its id and PATCHes it done", async () => {
  const calls = [];
  const env = loadBrowser((url, o) => {
    calls.push([url, o && o.method, o && o.body]);
    if (url === "/api/lists") return Promise.resolve({ rev: 2, lists: [{ id: "shopping", items: [{ id: "i_milk01", text: "Milch", done: false }] }] });
    return Promise.resolve({ item: {}, rev: 3 });
  });
  const msg = botMessage(env.dom, "a1", "streaming");
  env.emit("card", { kind: "list", data: { id: "shopping", name: "Einkaufsliste", kind: "shopping", items: ["Milch"] } });
  const box = msg.all().find((n) => n.tagName === "INPUT");
  assert.equal(box.type, "checkbox");
  box.checked = true;
  box.fire("change");
  await new Promise((r) => setImmediate(r));
  await new Promise((r) => setImmediate(r));
  assert.deepEqual(calls, [
    ["/api/lists", undefined, undefined],
    ["/api/lists/shopping/items/i_milk01", "PATCH", { done: true }],
  ]);
  assert.ok(msg.find("hc-item-done").length === 1);
  // unchecking reuses the id
  box.checked = false;
  box.fire("change");
  await new Promise((r) => setImmediate(r));
  assert.deepEqual(calls[2], ["/api/lists/shopping/items/i_milk01", "PATCH", { done: false }]);
});

test("DOM: a list item that is gone reverts the checkbox", async () => {
  const env = loadBrowser((url) => Promise.resolve(url === "/api/lists" ? { rev: 1, lists: [] } : {}));
  const msg = botMessage(env.dom, "a1", "streaming");
  env.emit("card", { kind: "list", data: { id: "shopping", name: "Einkaufsliste", items: ["Brot"] } });
  const box = msg.all().find((n) => n.tagName === "INPUT");
  box.checked = true;
  box.fire("change");
  await new Promise((r) => setImmediate(r));
  await new Promise((r) => setImmediate(r));
  assert.equal(box.checked, false);
  assert.equal(box.disabled, true);
  assert.equal(msg.find("hc-item-gone").length, 1);
});
