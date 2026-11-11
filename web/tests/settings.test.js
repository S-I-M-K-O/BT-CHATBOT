"use strict";
// Run: node --test web/tests/
// Pure helpers behind the settings window (web/static/lib.js).
const test = require("node:test");
const assert = require("node:assert/strict");
const L = require("../static/lib.js");

test("uiLang defaults to auto and only accepts auto/de/en", () => {
  assert.equal(L.DEFAULT_SETTINGS.uiLang, "auto");
  assert.equal(L.normalizeSettings({}).uiLang, "auto");
  assert.equal(L.normalizeSettings({ uiLang: "de" }).uiLang, "de");
  assert.equal(L.normalizeSettings({ uiLang: "en" }).uiLang, "en");
  assert.equal(L.normalizeSettings({ uiLang: "fr" }).uiLang, "auto");
  assert.equal(L.normalizeSettings({ uiLang: 1 }).uiLang, "auto");
  assert.equal(L.normalizeSettings({ uiLang: "DE" }).uiLang, "auto");
});

test("resolveUiLang: explicit choice wins, auto follows the voice", () => {
  assert.equal(L.resolveUiLang("de", "en"), "de");
  assert.equal(L.resolveUiLang("en", "de"), "en");
  assert.equal(L.resolveUiLang("auto", "de"), "de");
  assert.equal(L.resolveUiLang("auto", "en"), "en");
  assert.equal(L.resolveUiLang(undefined, "xx"), "en");
});

test("settings categories: order, titles and section mapping", () => {
  const ids = L.SETTINGS_CATEGORIES.map((c) => c.id);
  assert.deepEqual(ids, ["general", "voice", "people", "memory", "reminders", "calendar", "home", "docs", "location", "privacy", "models", "appearance", "config", "more"]);
  for (const c of L.SETTINGS_CATEGORIES) {
    assert.ok(c.title.de && c.title.en, c.id);
  }
  const want = {
    people: "people",
    voiceid: "people",
    memory: "memory",
    reminders: "reminders",
    checkins: "reminders",
    calendar: "calendar",
    home: "home",
    docs: "docs",
    links: "docs",
    privacy: "privacy",
    location: "location",
    models: "models",
  };
  for (const [section, cat] of Object.entries(want)) assert.equal(L.settingsCategoryFor(section), cat, section);
  assert.equal(L.settingsCategoryFor("weather"), "more");
  assert.equal(L.settingsCategoryFor(""), "more");
  assert.equal(L.settingsCategoryFor("__proto__"), "more");
});

test("search: case, accents, ß and multiple words", () => {
  assert.equal(L.normalizeSearchText("  Größe   ÄNDERN "), "grosse andern");
  assert.ok(L.settingsSearchMatch("", "anything"));
  assert.ok(L.settingsSearchMatch("   ", "anything"));
  assert.ok(L.settingsSearchMatch("stimme", "Piper-Stimme"));
  assert.ok(L.settingsSearchMatch("grosse", "Kontextgröße"));
  assert.ok(L.settingsSearchMatch("kontextgröße", "Kontextgrösse"));
  assert.ok(L.settingsSearchMatch("voice speed", "Voice speed"));
  assert.ok(L.settingsSearchMatch("speed voice", "Voice speed"), "word order does not matter");
  assert.ok(!L.settingsSearchMatch("voice pitch", "Voice speed"));
  assert.ok(!L.settingsSearchMatch("x", null));
});

test("normalizeConfigInfo keeps only well-formed fields", () => {
  assert.deepEqual(L.normalizeConfigInfo(null), { available: false, path: "", exists: false, managed: [], errors: [], mtime: "" });
  assert.equal(L.normalizeConfigInfo([]).available, false);
  const c = L.normalizeConfigInfo({
    path: "data/hermes.conf",
    exists: true,
    managed: ["speak", "uiLang", "speak", "bad key", 3, "__proto__x", ""],
    errors: [{ line: 4, key: "foo", msg: "unknown key", code: "unknown_key" }, null, { line: "x", key: 2, msg: "bad", code: "<b>" }],
    mtime: "2026-11-09T10:00:00Z",
  });
  assert.equal(c.available, true);
  assert.equal(c.path, "data/hermes.conf");
  assert.equal(c.exists, true);
  assert.deepEqual(c.managed, ["speak", "uiLang"]);
  assert.deepEqual(c.errors, [
    { line: 4, key: "foo", msg: "unknown key", code: "unknown_key" },
    { line: 0, key: "", msg: "bad", code: "" },
  ]);
  assert.equal(c.mtime, "2026-11-09T10:00:00Z");
  assert.equal(L.normalizeConfigInfo({ exists: "yes" }).exists, false);
});

test("managed keys: set, lookup and keepManaged", () => {
  const set = L.managedSet(["speak", "uiLang", 5, "no spaces"]);
  assert.ok(L.isManaged(set, "speak"));
  assert.ok(L.isManaged(set, "uiLang"));
  assert.ok(!L.isManaged(set, "volume"));
  assert.ok(!L.isManaged(set, "toString"), "no prototype keys");
  assert.ok(!L.isManaged(null, "speak"));

  const base = { speak: false, uiLang: "de", volume: 0.5 };
  const draft = { speak: true, uiLang: "en", volume: 0.9, extra: 1 };
  const out = L.keepManaged(draft, base, ["speak", "uiLang"]);
  assert.deepEqual(out, { speak: false, uiLang: "de", volume: 0.9, extra: 1 });
  assert.equal(draft.speak, true, "input not mutated");
  // a set works as well as a list; keys missing from base stay as drafted
  assert.deepEqual(L.keepManaged(draft, base, L.managedSet(["volume", "extra"])), { speak: true, uiLang: "en", volume: 0.5, extra: 1 });
  assert.deepEqual(L.keepManaged(draft, base, []), draft);
});

test("reset to defaults keeps managed values", () => {
  const saved = L.normalizeSettings({ speak: false, uiLang: "en", volume: 0.3 });
  const defaults = L.normalizeSettings(L.DEFAULT_SETTINGS);
  const reset = L.normalizeSettings(L.keepManaged(defaults, saved, ["uiLang"]));
  assert.equal(reset.uiLang, "en");
  assert.equal(reset.speak, true);
  assert.equal(reset.volume, 1);
});

test("voice lists follow the interface language", () => {
  const piper = [
    { key: "BT7274", name: "BT-7274", lang: "en" },
    { key: "ScorchAI", name: "Scorch AI", lang: "en" },
    { key: "JarvisDE", name: "JARVIS", lang: "de" },
  ];
  assert.deepEqual(L.voicesForUiLang(piper, "en").map((v) => v.key), ["BT7274", "ScorchAI"]);
  assert.deepEqual(L.voicesForUiLang(piper, "de").map((v) => v.key), ["JarvisDE"]);
  assert.equal(L.voicesForUiLang(piper, "auto").length, 3, "auto lists every voice");
  // browser voices carry BCP-47 tags
  const browser = [{ name: "Anna", lang: "de-DE" }, { name: "Samantha", lang: "en-US" }, { name: "Daniel", lang: "en_GB" }];
  assert.deepEqual(L.voicesForUiLang(browser, "en").map((v) => v.name), ["Samantha", "Daniel"]);
  // switching language: keep a matching voice, else take the first of that language
  assert.equal(L.pickVoiceForUiLang(piper, "ScorchAI", "en"), "ScorchAI");
  assert.equal(L.pickVoiceForUiLang(piper, "JarvisDE", "en"), "BT7274");
  assert.equal(L.pickVoiceForUiLang(piper, "BT7274", "de"), "JarvisDE");
  assert.equal(L.pickVoiceForUiLang([], "BT7274", "de"), "");
  assert.equal(L.pickVoiceForUiLang(browser, "Anna", "en", "name"), "Samantha");
});
