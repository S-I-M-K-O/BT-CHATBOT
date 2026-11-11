"use strict";
// Run: node --test web/tests/
// Pure helpers of web/static/people.js (HermesPeople) and web/static/memory.js (HermesMemory).
const test = require("node:test");
const assert = require("node:assert/strict");
const P = require("../static/people.js");
const M = require("../static/memory.js");

const NOW = Date.UTC(2026, 10, 6, 14, 30, 0);

// ------------------------------------------------------------------ sticky speaker

test("shouldStick: only a voice match of a person within 120 s", () => {
  const ev = (o) => Object.assign({ id: "p_ab12cd34ef56", source: "voice", at: NOW - 5000 }, o);
  assert.equal(P.shouldStick(ev(), NOW), true);
  assert.equal(P.shouldStick(ev({ at: NOW - 120000 }), NOW), true, "exactly 120 s still sticks");
  assert.equal(P.shouldStick(ev({ at: NOW - 120001 }), NOW), false, "older than 120 s");
  assert.equal(P.shouldStick(ev({ at: NOW + 1000 }), NOW), false, "event from the future");
  for (const source of ["manual", "sticky", "claim", "guest", "uncertain", "single"]) {
    assert.equal(P.shouldStick(ev({ source }), NOW), false, source + " never sticks");
  }
  assert.equal(P.shouldStick(ev({ id: "" }), NOW), false, "guest voice result");
  assert.equal(P.shouldStick(ev({ id: "_single" }), NOW), false, "single-user mode");
  assert.equal(P.shouldStick(ev({ at: "junk" }), NOW), false);
  assert.equal(P.shouldStick(null, NOW), false);
  assert.equal(P.shouldStick(ev(), NaN), false);
});

test("payloadSpeaker: manual beats sticky, guest and auto send nothing stale", () => {
  const last = { id: "p_anna0001", source: "voice", at: NOW - 1000 };
  const known = ["p_anna0001", "p_ben00002"];
  assert.deepEqual(P.payloadSpeaker("p_ben00002", last, NOW, known), { id: "p_ben00002", source: "manual" });
  assert.deepEqual(P.payloadSpeaker("", last, NOW, known), { id: "p_anna0001", source: "sticky" });
  assert.equal(P.payloadSpeaker("", Object.assign({}, last, { at: NOW - 200000 }), NOW, known), null);
  assert.equal(P.payloadSpeaker("_guest", last, NOW, known), null, "explicit guest sends no hint");
  assert.equal(P.payloadSpeaker("p_gone0000", last, NOW, known), null, "deleted manual person");
  assert.equal(P.payloadSpeaker("", Object.assign({}, last, { id: "p_gone0000" }), NOW, known), null, "deleted sticky person");
  assert.deepEqual(P.payloadSpeaker("p_x1", null, NOW, null), { id: "p_x1", source: "manual" }, "list unknown: trust id");
  assert.equal(P.payloadSpeaker("evil<script>", last, NOW, null).source, "sticky", "junk manual value ignored");
});

test("normalizeManual drops choices for people that no longer exist", () => {
  const people = [{ id: "p_anna0001", name: "Anna" }];
  assert.equal(P.normalizeManual("p_anna0001", people), "p_anna0001");
  assert.equal(P.normalizeManual("p_ben00002", people), "");
  assert.equal(P.normalizeManual("_guest", people), "_guest");
  assert.equal(P.normalizeManual("garbage", people), "");
  assert.equal(P.normalizeManual(null, people), "");
});

// ------------------------------------------------------------------ names

test("validateName mirrors the server rule", () => {
  assert.deepEqual(P.validateName("  Anna  "), { ok: true, name: "Anna", error: "" });
  assert.equal(P.validateName("Anna  Lena").name, "Anna Lena");
  assert.equal(P.validateName("Anne-Marie").ok, true);
  assert.equal(P.validateName("O’Brien").name, "O'Brien", "typographic apostrophe");
  assert.equal(P.validateName("Jürgen").ok, true);
  assert.equal(P.validateName("Zoë").ok, true);
  assert.equal(P.validateName("Ζωή").ok, true, "non-Latin letters");
  assert.equal(P.validateName("Jürgen").name, "Jürgen", "NFC");
  assert.equal(P.validateName("").error, "empty");
  assert.equal(P.validateName("   ").error, "empty");
  assert.equal(P.validateName(null).error, "empty");
  assert.equal(P.validateName("A".repeat(40)).ok, true);
  assert.equal(P.validateName("A".repeat(41)).error, "long");
  for (const bad of ["Anna1", "R2D2", "<b>", "Anna!", "-Anna", "Anna-", "Anna--Lena", "a@b", "Anna 😀"]) {
    assert.equal(P.validateName(bad).ok, false, bad);
  }
});

test("nameTaken is case-insensitive and skips the renamed person", () => {
  const people = [
    { id: "p_1", name: "Anna" },
    { id: "p_2", name: "Ben" },
  ];
  assert.equal(P.nameTaken("anna", people), true);
  assert.equal(P.nameTaken(" ANNA ", people), true);
  assert.equal(P.nameTaken("Anna", people, "p_1"), false, "renaming Anna to Anna");
  assert.equal(P.nameTaken("Clara", people), false);
  assert.equal(P.nameTaken("", people), false);
});

// ------------------------------------------------------------------ people view

test("personBadges and deleteSummary", () => {
  assert.deepEqual(P.personBadges({ origin: "voice", confirmed: false, provisional: true, samples: 1, voiceModelOk: true }), [
    "unconfirmed",
    "provisional",
  ]);
  assert.deepEqual(P.personBadges({ origin: "ui", confirmed: true, provisional: false, samples: 3, voiceModelOk: false }), ["oldModel"]);
  assert.deepEqual(P.personBadges({ origin: "ui", confirmed: true, provisional: false, samples: 0, voiceModelOk: false }), []);

  const trDe = P.makeTr(P.STRINGS, () => "de");
  const s = P.deleteSummary({ name: "Anna", samples: 3, memories: 12 }, trDe);
  assert.equal(s.summary, "Anna, das Stimmprofil und 12 Gedächtniseinträge werden gelöscht.");
  assert.equal(s.title, "Anna löschen?");
  assert.ok(s.items.some((i) => i.includes("3 Aufnahmen")));
  assert.ok(s.items.some((i) => i.includes("Sicherungskopien")), "corrupt copies are named");
  assert.equal(P.deleteSummary({ name: "Ben", samples: 0, memories: 1 }, trDe).summary, "Ben und 1 Gedächtniseintrag werden gelöscht.");
  const trEn = P.makeTr(P.STRINGS, () => "en");
  assert.equal(P.deleteSummary({ name: "Anna", samples: 2, memories: 0 }, trEn).summary, "Anna, the voice profile and 0 memory entries will be deleted.");
});

test("time formatting treats the Go zero time as never", () => {
  assert.equal(P.formatLastSeen("0001-01-01T00:00:00Z", NOW, "de"), "");
  assert.equal(P.formatLastSeen("", NOW, "en"), "");
  assert.equal(P.formatLastSeen(new Date(NOW - 30000).toISOString(), NOW, "de"), "gerade eben");
  assert.equal(P.formatLastSeen(new Date(NOW - 5 * 60000).toISOString(), NOW, "en"), "5 min ago");
  assert.equal(P.formatLastSeen(new Date(NOW - 3 * 3600000).toISOString(), NOW, "de"), "vor 3 Std.");
  assert.equal(P.formatLastSeen(new Date(NOW - 30 * 3600000).toISOString(), NOW, "en"), "yesterday");
  assert.equal(P.formatLastSeen(new Date(NOW - 3 * 86400000).toISOString(), NOW, "de"), "vor 3 Tagen");
  assert.match(P.formatLastSeen(new Date(NOW - 30 * 86400000).toISOString(), NOW, "de"), /^am \d+\. /);
  assert.equal(P.formatExpiry(new Date(NOW + 3 * 86400000).toISOString(), NOW, "de"), "in 3 Tagen");
  assert.equal(P.formatExpiry(new Date(NOW + 2 * 3600000).toISOString(), NOW, "en"), "today");
  assert.equal(P.formatExpiry(null, NOW, "en"), "");
  assert.equal(P.formatScore(0.7412, "de"), "0,74");
  assert.equal(P.formatScore(undefined, "en"), "–");
});

test("levelToPercent accepts dBFS and linear levels", () => {
  assert.equal(P.levelToPercent(-60), 0);
  assert.equal(P.levelToPercent(-90), 0);
  assert.equal(P.levelToPercent(-30), 50);
  assert.equal(P.levelToPercent(0), 0);
  assert.equal(P.levelToPercent(0.5), 50);
  assert.equal(P.levelToPercent(3), 100);
  assert.equal(P.levelToPercent("x"), 0);
});

test("errorKey maps API codes", () => {
  assert.equal(P.errorKey({ status: 409, code: "exists" }), "err_exists");
  assert.equal(P.errorKey({ status: 409, code: "limit" }), "err_limit");
  assert.equal(P.errorKey({ status: 403, code: "locked" }), "err_locked");
  assert.equal(P.errorKey({ status: 422, code: "model" }), "err_model");
  assert.equal(P.errorKey({ status: 503, code: "", message: "store is read-only" }), "err_readonly");
  assert.equal(P.errorKey({ status: 404 }), "err_notfound");
  assert.equal(P.errorKey(new TypeError("Failed to fetch")), "err_network");
  assert.equal(P.errorKey({ status: 500, code: "x" }), "err_generic");
});

// ------------------------------------------------------------------ enrolment wizard

function apiErr(status, code) {
  const e = new Error(code);
  e.status = status;
  e.code = code;
  return e;
}

function fakeDeps(script) {
  const calls = [];
  const enrolling = [];
  let take = 0;
  const deps = {
    lang: "de",
    setEnrolling: (on) => enrolling.push(on),
    stopSpeech: () => calls.push(["stopSpeech"]),
    record: async () => {
      const r = script.takes[take++];
      return typeof r === "function" ? r() : r;
    },
    fetchJSON: async (url, opts) => {
      calls.push([opts && opts.method, url, opts && opts.body]);
      const h = script.respond && script.respond(url, opts, calls);
      if (h instanceof Error) throw h;
      return h || {};
    },
  };
  return { deps, calls, enrolling };
}

const goodVoice = (sec) => ({ voice: { model: "campplus-zh-en-adv-16k@aa3cfc16", ort: "1.30.0", emb: "AAAA", sec: sec || 2.6 }, sec: sec || 2.6 });

function autoUI(log) {
  return {
    awaitTake: async (info) => {
      log.push(info);
      return true;
    },
  };
}

test("runEnrolment: three takes, no chat request, enrolling held for the whole wizard", async () => {
  let n = 0;
  const { deps, calls, enrolling } = fakeDeps({
    takes: [goodVoice(), goodVoice(), goodVoice()],
    respond: (url, opts) => (opts.method === "POST" ? { samples: ++n, provisional: n < 3, consistency: 0.8 } : {}),
  });
  const log = [];
  const res = await P.runEnrolment(deps, { id: "p_anna0001", samples: 0, origin: "ui", confirmed: true }, autoUI(log));
  assert.equal(res.ok, true);
  assert.equal(res.samples, 3);
  assert.equal(res.provisional, false);
  assert.deepEqual(enrolling, [true, false]);
  assert.deepEqual(calls[0], ["stopSpeech"], "TTS is stopped before the first take");
  assert.equal(calls.filter((c) => /\/api\/chat/.test(c[1] || "")).length, 0, "no /api/chat while recording");
  const posts = calls.filter((c) => c[0] === "POST");
  assert.equal(posts.length, 3);
  assert.equal(posts[0][1], "/api/people/p_anna0001/voiceprints");
  assert.deepEqual(Object.keys(posts[0][2]).sort(), ["emb", "kind", "model", "ort", "sec"]);
  assert.equal(posts[0][2].kind, "anchor");
  assert.deepEqual(
    log.map((l) => l.sentence),
    P.SENTENCES.de
  );
  assert.equal(calls.filter((c) => c[0] === "DELETE").length, 0, "no old profile to delete");
});

test("runEnrolment retries short and inconsistent takes, restarts after two mismatches", async () => {
  let posts = 0;
  const { deps, calls } = fakeDeps({
    takes: [
      goodVoice(1.2), // client-side short: no POST
      goodVoice(), // take 1 ok
      goodVoice(), // server says short
      goodVoice(), // inconsistent #1
      goodVoice(), // inconsistent #2 → restart from take 1
      goodVoice(),
      goodVoice(),
      goodVoice(),
    ],
    respond: (url, opts) => {
      if (opts.method !== "POST") return {};
      posts++;
      if (posts === 2) return apiErr(422, "short");
      if (posts === 3 || posts === 4) return apiErr(422, "inconsistent");
      return { samples: 1, provisional: true };
    },
  });
  const log = [];
  const res = await P.runEnrolment(deps, { id: "p_x1", samples: 0, origin: "ui", confirmed: true }, autoUI(log));
  assert.equal(res.ok, true);
  assert.deepEqual(
    log.map((l) => [l.index, l.error]),
    [
      [0, ""],
      [0, "short"],
      [1, ""],
      [1, "short"],
      [1, "inconsistent"],
      [0, "restarted"],
      [1, ""],
      [2, ""],
    ]
  );
  assert.equal(calls.filter((c) => c[0] === "DELETE").length, 1, "restart clears the anchors");
});

test("runEnrolment: re-record deletes the old profile once; voice person is confirmed", async () => {
  const { deps, calls } = fakeDeps({
    takes: [goodVoice(), goodVoice(), goodVoice()],
    respond: (url, opts) => (opts.method === "POST" && /voiceprints/.test(url) ? { samples: 1, provisional: false } : {}),
  });
  const res = await P.runEnrolment(deps, { id: "p_clara001", samples: 2, origin: "voice", confirmed: false }, autoUI([]));
  assert.equal(res.ok, true);
  const seq = calls.filter((c) => c[1]).map((c) => c[0] + " " + c[1]);
  assert.deepEqual(seq, [
    "DELETE /api/people/p_clara001/voiceprints",
    "POST /api/people/p_clara001/voiceprints",
    "POST /api/people/p_clara001/voiceprints",
    "POST /api/people/p_clara001/voiceprints",
    "POST /api/people/p_clara001/confirm",
  ]);
  assert.deepEqual(calls[calls.length - 1][2], { keep: true });
});

test("runEnrolment: cancel and fatal errors still release setEnrolling", async () => {
  const a = fakeDeps({ takes: [] });
  const res = await P.runEnrolment(a.deps, { id: "p_x1", samples: 0 }, { awaitTake: async () => false });
  assert.equal(res.cancelled, true);
  assert.deepEqual(a.enrolling, [true, false]);

  const b = fakeDeps({ takes: [goodVoice()], respond: () => apiErr(422, "model") });
  await assert.rejects(P.runEnrolment(b.deps, { id: "p_x1", samples: 0 }, autoUI([])), (e) => e.code === "model");
  assert.deepEqual(b.enrolling, [true, false]);

  const c = fakeDeps({ takes: [() => Promise.reject(Object.assign(new Error("denied"), { name: "NotAllowedError" }))] });
  await assert.rejects(P.runEnrolment(c.deps, { id: "p_x1", samples: 0 }, autoUI([])));
  assert.deepEqual(c.enrolling, [true, false]);
});

test("runIdentify posts only the voice object and never adapts", async () => {
  const { deps, calls, enrolling } = fakeDeps({
    takes: [goodVoice()],
    respond: () => ({ speaker: { id: "p_a", name: "Anna", source: "voice", score: 0.71 }, scores: [] }),
  });
  const res = await P.runIdentify(deps);
  assert.equal(res.speaker.name, "Anna");
  const post = calls.find((c) => c[0] === "POST");
  assert.equal(post[1], "/api/voiceid/identify");
  assert.deepEqual(Object.keys(post[2]), ["voice"]);
  assert.deepEqual(enrolling, [true, false]);

  const s = fakeDeps({ takes: [{ voice: { short: true, sec: 0.6 }, sec: 0.6 }] });
  assert.deepEqual(await P.runIdentify(s.deps), { short: true, sec: 0.6 });
  assert.equal(s.calls.filter((c) => c[1]).length, 0);
});

// ------------------------------------------------------------------ strings

function checkStrings(S, label) {
  const en = Object.keys(S.en).sort();
  const de = Object.keys(S.de).sort();
  assert.deepEqual(de, en, label + ": DE and EN have the same keys");
  for (const lang of ["en", "de"]) {
    for (const [k, v] of Object.entries(S[lang])) {
      assert.equal(typeof v, "string", label + " " + lang + "." + k);
      assert.ok(v.length > 0, label + " " + lang + "." + k + " empty");
      assert.doesNotMatch(v, /\bPilot\b/i, "never address the user as Pilot");
      const vars = (s) => (s.match(/\{\w+\}/g) || []).sort().join(",");
      assert.equal(vars(v), vars(S.en[k]), label + " " + lang + "." + k + " placeholders");
    }
  }
}

test("people strings: DE/EN parity, spec texts", () => {
  checkStrings(P.STRINGS, "people");
  assert.equal(
    P.STRINGS.de.disclaimer,
    "Stimmerkennung ist kein Schutz. Wer im Raum ist oder Zugriff auf dieses Gerät hat, kann sich als jemand anderes ausgeben. Für mehr Privatsphäre aktiviere die PIN-Sperre."
  );
  assert.equal(P.STRINGS.de.wizRoom, "Nimm in dem Raum und mit dem Mikrofon auf, die du sonst mit HERMES nutzt.");
  assert.equal(P.STRINGS.de.insecure, "Stimmerkennung nur auf diesem Gerät über http://localhost.");
  assert.equal(P.STRINGS.de.recordVoice, "Stimme aufnehmen");
  assert.equal(P.STRINGS.de.testRecognition, "Erkennung testen");
  assert.equal(P.STRINGS.de.chipAuto, "Automatisch (Stimme)");
  assert.equal(P.STRINGS.de.chipGuest, "Gast");
  assert.equal(P.SENTENCES.de.length, 3);
  assert.equal(P.SENTENCES.en.length, 3);
  for (const s of P.SENTENCES.de.concat(P.SENTENCES.en)) assert.doesNotMatch(s, /hermes/i, "no wake word in sentences");
});

test("memory strings: DE/EN parity, never 'Erinnerung' for memories", () => {
  checkStrings(M.STRINGS, "memory");
  for (const v of Object.values(M.STRINGS.de)) assert.doesNotMatch(v, /Erinnerung/, v);
  assert.equal(M.STRINGS.de.tab, "Gedächtnis");
  assert.equal(M.STRINGS.de.newEntry, "Neuer Eintrag");
  assert.equal(M.STRINGS.de.owner, "Vor der Personenerkennung");
  assert.equal(M.STRINGS.de.assign, "Zuordnen");
  assert.equal(M.STRINGS.de.savedNoText, "Neuer Eintrag gespeichert.");
  for (const k of M.KINDS) assert.ok(M.STRINGS.de["kind_" + k] && M.STRINGS.en["kind_" + k], k);
});

// ------------------------------------------------------------------ memory: filter and sort

const mems = [
  { id: "m_1", person: "p_anna0001", text: "Anna mag keinen Koriander.", kind: "preference", pinned: false, updatedAt: "2026-11-01T10:00:00Z" },
  { id: "m_2", person: "", text: "Das WLAN heißt Fuchsbau.", kind: "home", pinned: false, updatedAt: "2026-11-05T10:00:00Z" },
  { id: "m_3", person: "p_ben00002", text: "Ben hat einen Hund namens Bello.", kind: "relationship", pinned: true, updatedAt: "2026-10-01T10:00:00Z" },
  { id: "m_4", person: "_owner", text: "Der Nutzer trinkt Kaffee schwarz.", kind: "preference", pinned: false, updatedAt: "2026-11-06T09:00:00Z" },
  { id: "m_5", person: "p_anna0001", text: "Anna läuft gern im Grüngürtel.", kind: "other", pinned: true, updatedAt: "2026-09-01T10:00:00Z" },
];

test("filterMemories by scope", () => {
  const ids = (l) => l.map((m) => m.id).sort();
  assert.deepEqual(ids(M.filterMemories(mems, { scope: "all" })), ["m_1", "m_2", "m_3", "m_4", "m_5"]);
  assert.deepEqual(ids(M.filterMemories(mems, {})), ["m_1", "m_2", "m_3", "m_4", "m_5"]);
  assert.deepEqual(ids(M.filterMemories(mems, { scope: "household" })), ["m_2"]);
  assert.deepEqual(ids(M.filterMemories(mems, { scope: "p_anna0001" })), ["m_1", "m_5"]);
  assert.deepEqual(ids(M.filterMemories(mems, { scope: "_owner" })), ["m_4"]);
  assert.deepEqual(ids(M.filterMemories(mems, { scope: "p_nobody00" })), []);
  assert.deepEqual(M.filterMemories([null, 3, "x"], {}), []);
  assert.equal(M.scopeOf({ person: "" }), "household");
  assert.equal(M.scopeOf({}), "household");
});

test("filterMemories search: tokens, case and diacritics", () => {
  const ids = (q, extra) => M.filterMemories(mems, Object.assign({ query: q }, extra)).map((m) => m.id).sort();
  assert.deepEqual(ids("koriander"), ["m_1"]);
  assert.deepEqual(ids("KORIANDER anna"), ["m_1"]);
  assert.deepEqual(ids("anna hund"), []);
  assert.deepEqual(ids("heisst"), ["m_2"], "ß folds to ss");
  assert.deepEqual(ids("grungurtel"), ["m_5"], "umlauts fold");
  assert.deepEqual(ids("  "), ["m_1", "m_2", "m_3", "m_4", "m_5"]);
  assert.deepEqual(ids("anna", { scope: "household" }), []);
  assert.deepEqual(ids("zuhause", { kindLabel: (k) => (k === "home" ? "Zuhause" : "") }), ["m_2"], "kind label is searchable");
});

test("sortMemories: pinned first, then newest", () => {
  assert.deepEqual(
    M.sortMemories(mems).map((m) => m.id),
    ["m_3", "m_5", "m_4", "m_2", "m_1"]
  );
  const input = mems.slice();
  M.sortMemories(input);
  assert.deepEqual(input, mems, "does not mutate");
  assert.deepEqual(
    M.sortMemories([{ id: "m_b" }, { id: "m_a" }]).map((m) => m.id),
    ["m_a", "m_b"],
    "stable by id without dates"
  );
});

test("validateText and validateExpires", () => {
  assert.deepEqual(M.validateText("  Anna  mag\nTee. "), { ok: true, text: "Anna mag Tee.", error: "" });
  assert.equal(M.validateText("").error, "empty");
  assert.equal(M.validateText("x".repeat(300)).ok, true);
  assert.equal(M.validateText("x".repeat(301)).error, "long");
  assert.equal(M.validateText("😀".repeat(300)).ok, true, "counts runes, not UTF-16 units");

  const now = new Date(2026, 10, 6, 12, 0, 0).getTime();
  assert.deepEqual(M.validateExpires("", now), { ok: true, value: "" });
  assert.equal(M.validateExpires("2026-11-07", now).ok, true);
  assert.equal(M.validateExpires("2026-11-06", now).ok, false, "today is not in the future");
  assert.equal(M.validateExpires("2025-01-01", now).ok, false);
  assert.equal(M.validateExpires("2027-02-30", now).ok, false, "invalid day");
  assert.equal(M.validateExpires("07.11.2026", now).ok, false);
  assert.equal(M.isExpired({ expires: "2026-11-05" }, now), true);
  assert.equal(M.isExpired({ expires: "2026-11-06" }, now), false);
  assert.equal(M.isExpired({ expires: "" }, now), false);
  assert.equal(M.normalizeKind("health"), "health");
  assert.equal(M.normalizeKind("<x>"), "other");
});

test("memory.saved events: own device only, ids validated", () => {
  const ev = { type: "memory.saved", data: { person: "p_anna0001", count: 2, ids: ["m_abc", "m_def", "../x", "m_abc"], clientId: "c_111" } };
  assert.deepEqual(M.savedEventIds(ev), ["m_abc", "m_def"]);
  assert.deepEqual(M.savedEventIds(ev.data), ["m_abc", "m_def"]);
  assert.deepEqual(M.savedEventIds({}), []);
  assert.equal(M.isOwnEvent(ev, "c_111"), true);
  assert.equal(M.isOwnEvent(ev, "c_222"), false);
  assert.equal(M.isOwnEvent(ev, ""), false);
  assert.equal(M.isOwnEvent({ data: { ids: ["m_a"] } }, "c_111"), false);

  const tr = M.makeTr(M.STRINGS, () => "de");
  assert.equal(M.savedToastText([{ text: "Anna hat einen Hund namens Bello." }], tr), "Gemerkt: Anna hat einen Hund namens Bello.");
  assert.equal(M.savedToastText([{ text: "A." }, { text: "B." }], tr), "Gemerkt: A. (+1 weitere)");
  assert.equal(M.savedToastText([], tr), "Neuer Eintrag gespeichert.");
  assert.ok(M.savedToastText([{ text: "x".repeat(300) }], tr).length < 160, "long texts are clipped");
});

test("deleteAllQuery only for a person or the household", () => {
  assert.equal(M.deleteAllQuery("household"), "?person=household&confirm=1");
  assert.equal(M.deleteAllQuery("p_anna0001"), "?person=p_anna0001&confirm=1");
  assert.equal(M.deleteAllQuery("all"), "");
  assert.equal(M.deleteAllQuery("_owner"), "");
  assert.equal(M.deleteAllQuery("p_x&confirm=1"), "");
});

test("formatDate", () => {
  assert.equal(M.formatDate("2027-03-03", "de"), "3. März 2027");
  assert.equal(M.formatDate("2027-03-03", "en"), "3 Mar 2027");
  assert.equal(M.formatDate("0001-01-01T00:00:00Z", "de"), "");
  assert.equal(M.formatDate("", "de"), "");
  assert.equal(M.formatDate("junk", "en"), "");
});

test("modules load without a DOM and expose no browser UI", () => {
  assert.equal(P.ui, undefined);
  assert.equal(typeof P.runEnrolment, "function");
  assert.equal(typeof M.filterMemories, "function");
});
