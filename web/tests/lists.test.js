"use strict";
// Run: node --test web/tests/
// Pure helpers of web/static/lists.js (HermesLists).
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const L = require("../static/lists.js");

const NOW = new Date(2026, 10, 6, 14, 30, 0).getTime(); // local time, like the page

// ------------------------------------------------------------------ splitting

test("splitItems: spoken phrasing mirrors lists.go", () => {
  const cases = [
    ["Milch und Eier", "de", ["Milch", "Eier"]],
    ["die Milch, ein Brot sowie etwas Käse", "de", ["Milch", "Brot", "Käse"]],
    ["milch & eier; butter", "de", ["Milch", "Eier", "Butter"]],
    ["some milk and the eggs", "en", ["milk", "eggs"]],
    ["2 Liter Milch und noch Eier bitte", "de", ["2 Liter Milch", "Eier"]],
    ["Milch, milch, MILCH", "de", ["Milch"]],
    ["„Äpfel“", "de", ["Äpfel"]],
    ["die", "de", []],
    [" , und ", "de", []],
    ["", "de", []],
  ];
  for (const [input, lang, want] of cases) {
    assert.deepEqual(L.splitItems(input, { lang }), want, input);
  }
});

test("splitItems: typed text splits on commas and lines only and stays as written", () => {
  assert.deepEqual(L.splitItems("Salz und Pfeffer, die Ärzte CD\n  hafermilch ", { typed: true, lang: "de" }), [
    "Salz und Pfeffer",
    "die Ärzte CD",
    "hafermilch",
  ]);
  assert.deepEqual(L.splitItems("a;; ;b,A", { typed: true }), ["a", "b"]);
  assert.deepEqual(L.splitItems(null, { typed: true }), []);
});

test("splitItems: caps (50 items, 200 runes) and control characters", () => {
  const many = Array.from({ length: 80 }, (_, i) => "item" + i).join(", ");
  assert.equal(L.splitItems(many, { typed: true }).length, L.BATCH_MAX);
  const long = L.splitItems("ä".repeat(300), { typed: true });
  assert.equal(Array.from(long[0]).length, L.ITEM_MAX);
  assert.deepEqual(L.splitItems("Mil\u0007ch Brot", { typed: true }), ["Mil ch Brot"]);
});

// ------------------------------------------------------------------ lists

const shopping = {
  id: "shopping",
  name: "Shopping list",
  kind: "shopping",
  items: [
    { id: "i_1", text: "Milch", done: false },
    { id: "i_2", text: "Eier", done: true, doneAt: "2026-11-06T10:00:00Z" },
    { id: "i_3", text: "Brot", done: false },
    { id: "i_4", text: "Tee", done: true, doneAt: "2026-11-06T12:00:00Z" },
  ],
};

test("listName: built-ins per language, custom lists as named", () => {
  assert.equal(L.listName(shopping, "de"), "Einkaufsliste");
  assert.equal(L.listName(shopping, "en"), "Shopping list");
  assert.equal(L.listName({ id: "todo" }, "de"), "To-do-Liste");
  assert.equal(L.listName({ id: "l_1", name: " Pack\nliste " }, "de"), "Pack liste");
  assert.equal(L.listName({ id: "l_1", name: "" }, "de"), "?");
  assert.equal(L.listName(null, "de"), "");
  assert.equal(L.isBuiltin({ id: "todo" }), true);
  assert.equal(L.isBuiltin({ id: "l_x" }), false);
});

test("sortItems / openCount: open first in order, then the newest done", () => {
  assert.deepEqual(
    L.sortItems(shopping.items).map((i) => i.id),
    ["i_1", "i_3", "i_4", "i_2"]
  );
  assert.equal(L.openCount(shopping), 2);
  assert.equal(L.openCount({ items: [null, { id: "x" }] }), 0, "malformed items are ignored");
  assert.deepEqual(L.sortItems(null), []);
});

test("listAsText: the share format with open items only", () => {
  assert.equal(L.listAsText(shopping, "de"), "Einkaufsliste:\n- Milch\n- Brot");
  assert.equal(L.listAsText(shopping, "en"), "Shopping list:\n- Milch\n- Brot");
  assert.equal(L.listAsText({ id: "todo", items: [] }, "de"), "To-do-Liste:");
  assert.equal(L.listAsText({ id: "l_1", name: "Pack", items: [{ id: "a", text: "Zelt\nund Schlafsack" }] }, "de"), "Pack:\n- Zelt und Schlafsack");
});

test("diffLists: added, changed and removed items and lists", () => {
  const before = [shopping, { id: "l_old", items: [{ id: "i_9", text: "x" }] }];
  const after = [
    {
      id: "shopping",
      items: [
        { id: "i_1", text: "Milch", done: true },
        { id: "i_2", text: "Eier", done: true },
        { id: "i_3", text: "Vollkornbrot", done: false },
        { id: "i_5", text: "Käse", done: false },
      ],
    },
    { id: "l_new", items: [] },
  ];
  const d = L.diffLists(before, after);
  assert.deepEqual(d.addedItems, ["i_5"]);
  assert.deepEqual(d.changedItems.sort(), ["i_1", "i_3"]);
  assert.deepEqual(d.removedItems.sort(), ["i_4", "i_9"]);
  assert.deepEqual(d.addedLists, ["l_new"]);
  assert.deepEqual(d.removedLists, ["l_old"]);
  const same = L.diffLists(after, after);
  assert.deepEqual(same, { addedItems: [], changedItems: [], removedItems: [], addedLists: [], removedLists: [] });
  assert.deepEqual(L.diffLists(null, undefined).addedItems, []);
});

// ------------------------------------------------------------------ snapshot

test("snapshot: round trip, validation and read-only view", () => {
  const snap = L.makeSnapshot({ rev: 17, lists: [shopping] }, NOW);
  const raw = JSON.stringify(snap);
  const back = L.parseSnapshot(raw);
  assert.equal(back.rev, 17);
  assert.equal(back.at, NOW);
  assert.equal(back.lists[0].items.length, 4);
  for (const junk of ["", "{", "null", "[]", '{"lists":{}}', '{"lists":[],"at":"x"}', 42]) {
    assert.equal(L.parseSnapshot(junk), null, String(junk));
  }
  const dirty = L.parseSnapshot({ at: NOW, lists: [null, { id: "" }, { id: "shopping", items: [{ id: 3, text: "x" }, { id: "i", text: "y".repeat(500) }] }] });
  assert.equal(dirty.lists.length, 1);
  assert.equal(dirty.lists[0].items.length, 1);
  assert.equal(dirty.lists[0].items[0].text.length, L.ITEM_MAX);

  const view = L.snapshotView(raw, "de", NOW + 60000);
  assert.equal(view.readOnly, true);
  assert.equal(view.stamp, "Stand: 14:30 (offline)");
  assert.equal(L.snapshotView(raw, "en", NOW).stamp, "As of 14:30 (offline)");
  assert.equal(L.snapshotView("junk", "de", NOW), null);
});

test("formatStamp: older copies name the day", () => {
  const yesterday = new Date(2026, 10, 5, 8, 12).getTime();
  assert.equal(L.formatStamp(yesterday, "de", NOW), "Stand: 5.11., 08:12 (offline)");
  assert.equal(L.formatStamp(yesterday, "en", NOW), "As of 11/5, 08:12 (offline)");
  assert.equal(L.formatStamp("nope", "de", NOW), "");
});

// ------------------------------------------------------------------ misc

test("pickLang, listsUrl and swipeDecision", () => {
  assert.equal(L.pickLang("?lang=de", "en-US"), "de");
  assert.equal(L.pickLang("?x=1&lang=en", "de-DE"), "en");
  assert.equal(L.pickLang("", "de-AT"), "de");
  assert.equal(L.pickLang("", "fr-FR"), "en");
  assert.equal(L.pickLang(null, undefined), "en");

  assert.equal(L.listsUrl(null), "/api/lists");
  assert.equal(L.listsUrl(17), "/api/lists?since=17");
  assert.equal(L.listsUrl(0), "/api/lists?since=0");
  assert.equal(L.listsUrl(NaN), "/api/lists");
  assert.equal(L.listsUrl(-1), "/api/lists");

  assert.equal(L.swipeDecision(-200, 10, 360), "delete");
  assert.equal(L.swipeDecision(-60, 0, 360), null, "too short");
  assert.equal(L.swipeDecision(200, 0, 360), null, "right swipe");
  assert.equal(L.swipeDecision(-150, 300, 360), null, "vertical scroll");
});

test("strings: DE/EN parity, placeholders, no 'Pilot'", () => {
  const en = L.STRINGS.en;
  const de = L.STRINGS.de;
  assert.deepEqual(Object.keys(de).sort(), Object.keys(en).sort());
  for (const k of Object.keys(en)) {
    const ph = (s) => (s.match(/\{\w+\}/g) || []).sort();
    assert.deepEqual(ph(de[k]), ph(en[k]), k);
    assert.ok(!/pilot/i.test(en[k] + de[k]), k);
  }
  assert.equal(L.format("{a} und {b}", { a: 1 }), "1 und {b}");
});

test("lists.html: standalone page loads only the allowed files, no inline script", () => {
  const html = fs.readFileSync(path.join(__dirname, "../static/lists.html"), "utf8");
  const scripts = [...html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/g)];
  assert.deepEqual(
    scripts.map((m) => (/src="([^"]+)"/.exec(m[1]) || [])[1]),
    ["/lib.js", "/hermes.js", "/lists.js"]
  );
  for (const m of scripts) assert.equal(m[2].trim(), "", "inline script body");
  assert.ok(!/\son[a-z]+=/i.test(html), "inline event handler");
  assert.ok(!/app\.js/.test(html), "app.js must not load");
  const links = [...html.matchAll(/<link\b[^>]*href="([^"]+)"/g)].map((m) => m[1]);
  for (const href of links) assert.ok(href === "/css/lists.css" || href.startsWith("/favicon"), href);
  assert.match(html, /<body data-page="lists">/);
});

test("lists.js: no innerHTML with data", () => {
  const src = fs.readFileSync(path.join(__dirname, "../static/lists.js"), "utf8");
  assert.ok(!/innerHTML|insertAdjacentHTML|outerHTML|document\.write/.test(src));
});

// ------------------------------------------------------------------ standalone page (fake DOM)

/* A tiny DOM, just enough for the standalone phone page. */
function makeListsDom() {
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
      this.value = "";
      this._text = "";
      this.className = "";
      this.isConnected = true;
      const self = this;
      this.classList = {
        add: (...c) => (self.className = (self.className + " " + c.join(" ")).trim()),
        remove: (...c) => (self.className = self.className.split(/\s+/).filter((x) => !c.includes(x)).join(" ")),
        toggle: () => {},
        contains: (c) => self.className.split(/\s+/).includes(c),
      };
      this.style = {};
      this.dataset = {};
    }
    get firstChild() {
      return this.childNodes[0] || null;
    }
    get textContent() {
      if (this.tagName === "#text") return this._text;
      return this._text + this.childNodes.map((c) => c.textContent).join("");
    }
    set textContent(t) {
      this.childNodes = [];
      this._text = String(t);
    }
    set innerHTML(v) {
      throw new Error("innerHTML must not be used");
    }
    appendChild(c) {
      if (c.parentNode) c.parentNode.removeChild(c);
      c.parentNode = this;
      this.childNodes.push(c);
      return c;
    }
    insertBefore(c, ref) {
      if (c.parentNode) c.parentNode.removeChild(c);
      c.parentNode = this;
      const i = this.childNodes.indexOf(ref);
      if (i < 0) this.childNodes.push(c);
      else this.childNodes.splice(i, 0, c);
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
    removeAttribute(k) {
      delete this.attrs[k];
    }
    addEventListener(t, fn) {
      (this.listeners[t] = this.listeners[t] || []).push(fn);
    }
    removeEventListener() {}
    focus() {}
    blur() {}
    select() {}
    scrollIntoView() {}
    closest() {
      return null;
    }
    all() {
      return this.childNodes.flatMap((c) => [c, ...c.all()]);
    }
  }
  const body = new Node("body");
  body.setAttribute("data-page", "lists");
  const main = new Node("main");
  main.setAttribute("id", "listsApp");
  body.appendChild(main);
  const document = {
    body,
    title: "",
    documentElement: { lang: "" },
    visibilityState: "visible",
    activeElement: null,
    createElement: (t) => new Node(t),
    createTextNode: (t) => {
      const n = new Node("");
      n._text = String(t);
      return n;
    },
    getElementById: (id) => (id === "listsApp" ? main : null),
    addEventListener: () => {},
    execCommand: () => false,
  };
  return { document, main };
}

/* Run lists.js as the standalone phone page with a fake fetch and storage. */
function loadStandalone(fetchImpl, stored) {
  const vm = require("node:vm");
  const { document, main } = makeListsDom();
  const store = Object.assign({}, stored || {});
  const intervals = [];
  const win = {
    console,
    setTimeout,
    clearTimeout,
    setInterval: (fn) => intervals.push(fn),
    Promise,
    Date,
    JSON,
    Math,
    Object,
    Array,
    String,
    Number,
    RegExp,
    Error,
    document,
    location: { search: "?lang=en", host: "192.168.1.5:8080", protocol: "http:" },
    navigator: { language: "en-US" },
    addEventListener: () => {},
    localStorage: {
      getItem: (k) => (Object.prototype.hasOwnProperty.call(store, k) ? store[k] : null),
      setItem: (k, v) => (store[k] = String(v)),
      removeItem: (k) => delete store[k],
    },
    fetch: fetchImpl,
  };
  win.window = win;
  vm.createContext(win);
  vm.runInContext(fs.readFileSync(path.join(__dirname, "../static/lists.js"), "utf8"), win, { filename: "lists.js" });
  return { main, store, intervals, text: () => main.textContent };
}

const settleLists = () => new Promise((r) => setTimeout(r, 20));
const jsonRes = (status, data) => Promise.resolve({ status, ok: status >= 200 && status < 300, text: () => Promise.resolve(JSON.stringify(data)) });
const SNAP = JSON.stringify(L.makeSnapshot({ rev: 4, lists: [{ id: "shopping", kind: "shopping", items: [{ id: "i1", text: "Secret pills" }] }] }, NOW));

test("standalone: an unreachable server shows the stored snapshot read-only", async () => {
  const p = loadStandalone(() => Promise.reject(new TypeError("Failed to fetch")), { [L.SNAPSHOT_KEY]: SNAP });
  await settleLists();
  assert.ok(p.text().includes("Secret pills"), p.text());
  assert.ok(p.text().includes(L.STRINGS.en.readOnly));
  assert.ok(p.store[L.SNAPSHOT_KEY], "the snapshot stays for the next time");
});

test("standalone: a refused token (401) shows no stored lists and drops the snapshot", async () => {
  const p = loadStandalone(() => jsonRes(401, { error: "unauthorized" }), { [L.SNAPSHOT_KEY]: SNAP });
  await settleLists();
  assert.ok(p.text().includes(L.STRINGS.en.noAccess), p.text());
  assert.ok(!p.text().includes("Secret pills"), "a revoked token must not keep showing the lists: " + p.text());
  assert.equal(p.store[L.SNAPSHOT_KEY], undefined, "the stored copy is removed");
});

test("standalone: a token revoked while the page is open hides the lists shown so far", async () => {
  let status = 200;
  const p = loadStandalone(() =>
    status === 200 ? jsonRes(200, { rev: 5, lists: [{ id: "shopping", kind: "shopping", items: [{ id: "i1", text: "Secret pills" }] }] }) : jsonRes(401, {})
  );
  await settleLists();
  assert.ok(p.text().includes("Secret pills"));
  assert.ok(p.store[L.SNAPSHOT_KEY]);
  status = 401;
  p.intervals.forEach((fn) => fn()); // the 30 s poll
  await settleLists();
  assert.ok(p.text().includes(L.STRINGS.en.noAccess), p.text());
  assert.ok(!p.text().includes("Secret pills"), p.text());
  assert.equal(p.store[L.SNAPSHOT_KEY], undefined);
});
