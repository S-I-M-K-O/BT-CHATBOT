"use strict";
// Run: node --test web/tests/
// Pure helpers of web/static/links.js (HermesLinks).
const test = require("node:test");
const assert = require("node:assert/strict");
const K = require("../static/links.js");

test("extractUrl: first URL, trailing punctuation, instruction", () => {
  const cases = [
    ["https://example.com/a", "https://example.com/a", ""],
    ["Fasse das kurz zusammen: https://www.heise.de/news/x-123.html.", "https://www.heise.de/news/x-123.html", "Fasse das kurz zusammen"],
    ["summarise this link https://example.com/post?id=7&x=1, please", "https://example.com/post?id=7&x=1", "summarise this link, please"],
    ["(siehe https://de.wikipedia.org/wiki/Foo_(Begriff))", "https://de.wikipedia.org/wiki/Foo_(Begriff)", "(siehe )"],
    ["„https://example.org/zitat“ lies vor", "https://example.org/zitat", "lies vor"],
    ["read me this article https://example.com/a#section", "https://example.com/a", "read me this article"],
    ["two links https://a.example/1 and https://b.example/2", "https://a.example/1", "two links and https://b.example/2"],
    ["HTTP://EXAMPLE.COM/Up", "http://example.com/Up", ""],
  ];
  for (const [input, url, instr] of cases) {
    const got = K.extractUrl(input);
    assert.ok(got, input);
    assert.equal(got.url, url, input);
    assert.equal(got.instruction, instr, input);
  }
});

test("extractUrl: refuses non-http, credentials, oversize and plain text", () => {
  for (const input of [
    "",
    null,
    "kein Link hier",
    "ftp://example.com/file",
    "javascript:alert(1)",
    "file:///etc/passwd",
    "https://user:pw@example.com/",
    "https://example.com/" + "a".repeat(2100),
    "www.example.com ohne Schema",
  ]) {
    assert.equal(K.extractUrl(input), null, String(input));
  }
});

test("isLinkFollowUp: DE and EN follow-ups, not ordinary questions", () => {
  for (const yes of [
    "Lies den Link vor",
    "lies mir den Artikel vor",
    "Fass den Artikel zusammen",
    "kannst du den Link zusammenfassen",
    "summarise this link",
    "summarize the link",
    "read me this article",
    "Read the article again",
    "give me the gist of the article",
  ]) {
    assert.equal(K.isLinkFollowUp(yes), true, yes);
  }
  for (const no of [
    "",
    "Was ist ein Link?",
    "Ich habe den Artikel vor drei Tagen gelesen",
    "I already shared the link",
    "Wie wird das Wetter morgen?",
    "read me a poem",
    "lies https://example.com/artikel vor", // has a URL: the URL path handles it
  ]) {
    assert.equal(K.isLinkFollowUp(no), false, no);
  }
});

test("lastLinkUrl: last user link message, attUrl or url", () => {
  const conv = [
    { role: "user", content: "[Link: a.de]", att: true, attUrl: "https://a.de/1" },
    { role: "assistant", content: "Zusammenfassung" },
    { role: "user", content: "hallo" },
    { role: "user", content: "[Link: b.de]", att: true, url: "https://b.de/2#x" },
    { role: "assistant", content: "…" },
    { role: "assistant", content: "[Link: evil]", att: true, attUrl: "https://evil.example/" },
  ];
  assert.equal(K.lastLinkUrl(conv), "https://b.de/2");
  assert.equal(K.lastLinkUrl(conv.slice(0, 3)), "https://a.de/1");
  assert.equal(K.lastLinkUrl([{ role: "user", att: true, attUrl: "javascript:alert(1)" }]), "");
  assert.equal(K.lastLinkUrl(null), "");
  assert.equal(K.lastLinkUrl([]), "");
});

test("isPaywall: short text, subscription words at start or end, title", () => {
  const long = "Lorem ipsum dolor sit amet. ".repeat(60);
  assert.equal(K.isPaywall("Nur ein Anriss.", "Titel"), true);
  assert.equal(K.isPaywall(long, "Ganz normaler Artikel"), false);
  assert.equal(K.isPaywall(long + " Jetzt weiterlesen mit SZ Plus – Abo abschließen.", "x"), true);
  assert.equal(K.isPaywall(long, "Plus-Artikel: Die Lage"), true);
  assert.equal(K.isPaywall(long + " Subscribe to continue reading.", "x"), true);
  // "about" is not "Abo"; a subscription word in the middle of a long text is not a paywall
  assert.equal(K.isPaywall(long + " This is about the weather. " + long, "All about rain"), false);
  const middle = long + " Das Abo-Modell der Verlage steht in der Kritik. " + long + long;
  assert.equal(K.isPaywall(middle, "Medien"), false);
});

test("normalizeArticleText: whitespace, paragraphs, controls, rune cap", () => {
  assert.equal(K.normalizeArticleText("  a\t\tb \r\n\r\n\r\n\r\nc\u0000d  "), "a b\n\nc d");
  assert.equal(K.normalizeArticleText("x  y"), "x y");
  const emoji = "😀".repeat(20);
  assert.equal(K.normalizeArticleText(emoji, 5), "😀".repeat(5));
  assert.equal([...K.normalizeArticleText("ä".repeat(13000))].length, K.TEXT_MAX);
  assert.equal(K.normalizeArticleText(null), "");
});

test("truncateRunes and oneLine count code points", () => {
  assert.equal(K.truncateRunes("abc", 5), "abc");
  assert.equal(K.truncateRunes("😀😀😀", 2), "😀😀");
  assert.equal(K.oneLine("  Titel\nmit\tUmbruch  ", 300), "Titel mit Umbruch");
});

test("siteFromUrl and articleLang", () => {
  assert.equal(K.siteFromUrl("https://www.Heise.de/news/1"), "heise.de");
  assert.equal(K.siteFromUrl("not a url"), "");
  assert.equal(K.articleLang("de-DE"), "de");
  assert.equal(K.articleLang("EN"), "en");
  assert.equal(K.articleLang("fr"), "");
  assert.equal(K.articleLang(""), "");
});

test("errorKey and upstreamStatus map the server's codes", () => {
  assert.equal(K.errorKey({ status: 403, code: "blocked" }), "err_blocked");
  assert.equal(K.errorKey({ status: 502, code: "status", message: "the site answered HTTP 403" }), "err_status");
  assert.equal(K.upstreamStatus({ message: "the site answered HTTP 403" }), 403);
  assert.equal(K.upstreamStatus({ message: "nope" }), 0);
  assert.equal(K.errorKey({ status: 429, code: "busy" }), "err_busy");
  assert.equal(K.errorKey({ status: 429 }), "err_busy");
  assert.equal(K.errorKey({ status: 504, code: "timeout" }), "err_timeout");
  assert.equal(K.errorKey({ name: "AbortError" }), "err_timeout");
  assert.equal(K.errorKey({ status: 0 }), "err_network");
  assert.equal(K.errorKey({ status: 500, code: "internal" }), "err_generic");
  assert.equal(K.errorKey(null), "err_generic");
  for (const key of ["err_url", "err_type", "err_too_large", "err_charset", "err_redirects", "err_pdf"]) {
    assert.ok(K.STRINGS.en[key] && K.STRINGS.de[key], key);
  }
});

test("STRINGS: DE and EN have the same keys", () => {
  assert.deepEqual(Object.keys(K.STRINGS.de).sort(), Object.keys(K.STRINGS.en).sort());
  const tr = K.makeTr("de");
  assert.equal(tr("err_status", { status: 403 }), "Die Website hat die Anfrage abgelehnt (HTTP 403). Manche Seiten sperren automatische Leser.");
  assert.equal(K.makeTr("en")("readingSay"), "Reading the article.");
  assert.equal(K.makeTr("xx")("readingSay"), "Reading the article.");
});

test("base64ToBytes decodes the server's PDF field", () => {
  const bytes = K.base64ToBytes(Buffer.from("%PDF-1.4").toString("base64"));
  assert.equal(Buffer.from(bytes).toString(), "%PDF-1.4");
});
