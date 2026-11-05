"use strict";
// Run: node --test tests/js/
const test = require("node:test");
const assert = require("node:assert/strict");
const L = require("../../static/lib.js");

// ------------------------------------------------------------------ numbers

test("parseNum keeps 0 and rejects junk", () => {
  assert.equal(L.parseNum("0", 1), 0);
  assert.equal(L.parseNum(0, 1), 0);
  assert.equal(L.parseNum("0.0", 1), 0);
  assert.equal(L.parseNum("", 1), 1);
  assert.equal(L.parseNum("abc", 0.7), 0.7);
  assert.equal(L.parseNum(undefined, 5), 5);
  assert.equal(L.parseNum(null, 5), 5);
  assert.equal(L.parseNum(NaN, 5), 5);
  assert.equal(L.parseNum(Infinity, 5), 5);
  assert.equal(L.parseNum("1,5", 0), 1.5, "German decimal comma");
  assert.equal(L.parseNum("9", 1, 0, 2), 2, "clamped high");
  assert.equal(L.parseNum("-3", 1, 0, 2), 0, "clamped low");
});

test("clamp", () => {
  assert.equal(L.clamp(5, 0, 1), 1);
  assert.equal(L.clamp(-5, 0, 1), 0);
  assert.equal(L.clamp(0.5, 0, 1), 0.5);
});

test("normalizeSettings migrates and clamps like the server", () => {
  const s = L.normalizeSettings({ rate: -0.2, pitch: 0, volume: 0, voiceSpeed: 0, temperature: 0, maxTokens: 1 });
  assert.equal(s.rate, 1.0, "legacy negative rate -> 1.0");
  assert.equal(s.pitch, 0, "pitch 0 preserved");
  assert.equal(s.volume, 0, "volume 0 preserved");
  assert.equal(s.temperature, 0, "temperature 0 preserved");
  assert.equal(s.voiceSpeed, 1.0, "speed 0 -> default");
  assert.equal(s.maxTokens, 16);
  assert.equal(L.normalizeSettings({ voiceSpeed: 9 }).voiceSpeed, 2);
  assert.equal(L.normalizeSettings({ voiceSpeed: 0.1 }).voiceSpeed, 0.5);
  assert.equal(L.normalizeSettings({ voiceFx: "chorus" }).voiceFx, "titan");
  assert.equal(L.normalizeSettings({ voiceFx: "radio" }).voiceFx, "radio");
  assert.equal(L.normalizeSettings({ voiceMode: "x" }).voiceMode, "piper");
  assert.equal(L.normalizeSettings({ eyeDesign: "nope" }).eyeDesign, "classic");
  for (const gone of ["scorch", "radar", "mantis"]) assert.equal(L.normalizeSettings({ eyeDesign: gone }).eyeDesign, "classic");
  assert.equal(L.normalizeSettings({ eyeDesign: "cortana" }).eyeDesign, "cortana");
  assert.equal(L.normalizeSettings({ streamSpeech: false }).streamSpeech, false);
  assert.equal(L.normalizeSettings({}).streamSpeech, true);
  assert.equal(L.normalizeSettings({}).autoScroll, true);
  assert.equal(L.normalizeSettings(null).localModel, "qwen3.5:latest");
  assert.equal(L.normalizeSettings({ localModel: "  " }).localModel, "qwen3.5:latest");
  assert.equal(L.normalizeSettings({ rate: 5 }).rate, 3);
});

// ------------------------------------------------------------------ sentences

test("splitSentences: basic punctuation", () => {
  assert.deepEqual(L.splitSentences("Hello there, pilot. How are you today? I am fine!"), [
    "Hello there, pilot.",
    "How are you today?",
    "I am fine!",
  ]);
});

test("splitSentences: abbreviations, decimals and initials do not split", () => {
  const out = L.splitSentences("It is 3.5 degrees, e.g. quite cold. Dr. Smith agrees with Mr. Jones. J. R. R. Tolkien wrote it.");
  assert.deepEqual(out, ["It is 3.5 degrees, e.g. quite cold.", "Dr. Smith agrees with Mr. Jones.", "J. R. R. Tolkien wrote it."]);
});

test("splitSentences: German abbreviations and ordinals", () => {
  const out = L.splitSentences("Am 3. Oktober ist Feiertag. Das ist z.B. gut, d.h. wir feiern. Bzw. später.");
  assert.equal(out[0], "Am 3. Oktober ist Feiertag.");
  assert.equal(out[1], "Das ist z.B. gut, d.h. wir feiern.");
});

test("splitSentences: ellipsis only splits before a capital", () => {
  assert.deepEqual(L.splitSentences("Well... maybe later we talk. Wait… Then it happened."), [
    "Well... maybe later we talk.",
    "Wait… Then it happened.",
  ]);
});

test("splitSentences: short fragments are merged", () => {
  const out = L.splitSentences("Yes. Ok. The titan is ready for deployment.");
  assert.equal(out.length, 1);
  assert.equal(out[0], "Yes. Ok. The titan is ready for deployment.");
  const tail = L.splitSentences("The titan is ready for deployment. Go.");
  assert.deepEqual(tail, ["The titan is ready for deployment. Go."]);
});

test("splitSentences: closing quotes stay with their sentence", () => {
  assert.deepEqual(L.splitSentences('He said "trust me." Then he jumped off.'), ['He said "trust me."', "Then he jumped off."]);
});

test("splitSentences: empty input", () => {
  assert.deepEqual(L.splitSentences(""), []);
  assert.deepEqual(L.splitSentences("   "), []);
});

test("extractCompleteSentences keeps the unfinished tail", () => {
  const r = L.extractCompleteSentences("Protocol one: link to pilot. Protocol two: uphold the");
  assert.deepEqual(r.sentences, ["Protocol one: link to pilot."]);
  assert.equal(r.rest.trim(), "Protocol two: uphold the");
  const r2 = L.extractCompleteSentences("The value is 3.");
  assert.deepEqual(r2.sentences, [], "terminator at the end is not yet confirmed (could be 3.5)");
  const r3 = L.extractCompleteSentences("A line without a stop that is long enough\nnext");
  assert.deepEqual(r3.sentences, ["A line without a stop that is long enough"]);
  assert.equal(r3.rest, "next");
});

test("splitSentences: closed sentences and lines also respect maxChars", () => {
  const long = Array(20).fill("this clause goes on, and on").join(" ") + ". Short one here.";
  const parts = L.splitSentences(long);
  assert.ok(parts.length > 2, parts.join(" | "));
  for (const p of parts) assert.ok(p.length <= 220, p.length + ": " + p);
  assert.equal(parts.join(" ").replace(/\s+/g, " "), long);
  for (const p of L.splitSentences("word ".repeat(80))) assert.ok(p.length <= 220);
  for (const p of L.splitSentences(long, { maxChars: 150 })) assert.ok(p.length <= 150);
  // a whole reply in one streamed delta is cut the same way
  const st = L.createSentenceStream();
  const out = st.push(long + " ").concat(st.flush());
  for (const p of out) assert.ok(p.length <= 220, p.length + ": " + p);
});

test("splitSentences: units, Plan B, 'no.' and bold sentence ends split", () => {
  assert.deepEqual(L.splitSentences("Temperature is 20 °C. Wind is 10 km/h. Done here now."), ["Temperature is 20 °C.", "Wind is 10 km/h.", "Done here now."]);
  assert.deepEqual(L.splitSentences("The answer is no. We should leave now."), ["The answer is no.", "We should leave now."]);
  assert.deepEqual(L.splitSentences("I like Plan B. It works well for us."), ["I like Plan B.", "It works well for us."]);
  assert.equal(L.splitSentences("See No. 5 for the details please.").length, 1);
  assert.equal(L.splitSentences("J. R. R. Tolkien wrote many books.").length, 1);
  const st = L.createSentenceStream();
  const got = [];
  for (const c of "**Short answer: yes.** The reason is simple. ") got.push(...st.push(c));
  // the bold sentence is emitted as soon as the next word starts
  assert.deepEqual(got, ["Short answer: yes.", "The reason is simple."]);
  assert.deepEqual(st.flush(), []);
});

test("extractCompleteSentences cuts very long runs at a comma", () => {
  const long = "word ".repeat(30) + ", " + "more ".repeat(30);
  const r = L.extractCompleteSentences(long, { maxChars: 200 });
  assert.ok(r.sentences.length >= 1);
  assert.ok(r.rest.length <= 200);
});

test("createSentenceStream: emits sentences as text streams in", () => {
  const s = L.createSentenceStream();
  const text = "Affirmative, pilot. The weather in Berlin is 21 degrees. Expect light rain later today.";
  const got = [];
  for (let i = 0; i < text.length; i += 3) got.push(...s.push(text.slice(i, i + 3)));
  assert.deepEqual(got, ["Affirmative, pilot.", "The weather in Berlin is 21 degrees."]);
  assert.deepEqual(s.flush(), ["Expect light rain later today."]);
  assert.deepEqual(s.flush(), [], "flush resets");
});

test("createSentenceStream: skips code blocks, think blocks and markdown", () => {
  const s = L.createSentenceStream();
  const text = "<think>internal. reasoning.</think>Here is **the** code:\n```js\nconsole.log('hi. there.');\n```\nThat prints a greeting. Done here now.";
  const got = [];
  for (const ch of text) got.push(...s.push(ch));
  got.push(...s.flush());
  assert.deepEqual(got, ["Here is the code:", "That prints a greeting.", "Done here now."]);
  assert.ok(!got.join(" ").includes("console"));
  assert.ok(!got.join(" ").includes("internal"));
});

test("createSentenceStream: emoji-only fragments are dropped", () => {
  const s = L.createSentenceStream();
  assert.deepEqual([...s.push("🙂🙂🙂🙂🙂🙂🙂🙂🙂🙂🙂. "), ...s.flush()], []);
});

// ------------------------------------------------------------------ SSE

test("parseSSEChunk: multiple events, CRLF and partial tail", () => {
  const r = L.parseSSEChunk('data: {"delta":"a"}\r\n\r\ndata: {"delta":"b"}\n\ndata: {"del');
  assert.deepEqual(r.events, [{ delta: "a" }, { delta: "b" }]);
  assert.equal(r.rest, 'data: {"del');
});

test("parseSSEChunk: comments, other fields, [DONE] and junk", () => {
  const r = L.parseSSEChunk(': ping\n\nevent: message\ndata: {"done":true}\n\ndata: [DONE]\n\ndata: not json\n\n');
  assert.deepEqual(r.events, [{ done: true }, { done: true }, { raw: "not json" }]);
  assert.equal(r.rest, "");
});

test("parseSSEChunk: multi-line data is joined", () => {
  const r = L.parseSSEChunk('data: {"delta":\ndata: "x"}\n\n');
  assert.deepEqual(r.events, [{ delta: "x" }]);
});

test("createSSEParser: events split across arbitrary chunk boundaries", () => {
  const stream = 'data: {"intent":"image"}\n\ndata: {"delta":"Hi "}\r\n\r\ndata: {"image":"/gen/a.png"}\n\ndata: {"done":true}\n\n';
  for (const size of [1, 2, 5, 7, 1000]) {
    const p = L.createSSEParser();
    const events = [];
    for (let i = 0; i < stream.length; i += size) events.push(...p.push(stream.slice(i, i + size)));
    events.push(...p.end());
    assert.deepEqual(events, [{ intent: "image" }, { delta: "Hi " }, { image: "/gen/a.png" }, { done: true }], "chunk size " + size);
  }
});

test("createSSEParser: end() flushes an unterminated final event", () => {
  const p = L.createSSEParser();
  assert.deepEqual(p.push('data: {"delta":"x"}'), []);
  assert.deepEqual(p.end(), [{ delta: "x" }]);
});

// ------------------------------------------------------------------ markdown

test("renderMarkdown: formatting", () => {
  assert.equal(L.renderMarkdown("**bold** and *it* and `code`"), "<p><strong>bold</strong> and <em>it</em> and <code>code</code></p>");
  assert.equal(L.renderMarkdown("a\nb"), "<p>a<br>b</p>");
  assert.equal(L.renderMarkdown("p1\n\np2"), "<p>p1</p><p>p2</p>");
  assert.equal(L.renderMarkdown("- one\n- two"), "<ul><li>one</li><li>two</li></ul>");
  assert.equal(L.renderMarkdown("1. one\n2. two"), "<ol><li>one</li><li>two</li></ol>");
  assert.equal(L.renderMarkdown("# Title"), "<h3>Title</h3>");
  assert.equal(L.renderMarkdown("> quoted"), "<blockquote>quoted</blockquote>");
  assert.equal(L.renderMarkdown("~~old~~"), "<p><del>old</del></p>");
  assert.equal(L.renderMarkdown("snake_case_name stays"), "<p>snake_case_name stays</p>");
  assert.equal(L.renderMarkdown("2 * 3 * 4"), "<p>2 * 3 * 4</p>");
});

test("renderMarkdown: fenced code (closed and still streaming)", () => {
  assert.equal(L.renderMarkdown("```js\nlet a = 1 < 2;\n```"), '<pre><code class="lang-js">let a = 1 &lt; 2;</code></pre>');
  assert.equal(L.renderMarkdown("```\nopen"), "<pre><code>open</code></pre>");
  assert.equal(L.renderMarkdown("```\n**not bold**\n```"), "<pre><code>**not bold**</code></pre>");
});

test("renderMarkdown: links are http(s) only and open safely", () => {
  assert.equal(
    L.renderMarkdown("[site](https://example.com/a?b=1&c=2)"),
    '<p><a href="https://example.com/a?b=1&amp;c=2" target="_blank" rel="noopener noreferrer">site</a></p>'
  );
  assert.equal(
    L.renderMarkdown("see https://example.com."),
    '<p>see <a href="https://example.com" target="_blank" rel="noopener noreferrer">https://example.com</a>.</p>'
  );
  assert.ok(!L.renderMarkdown("[x](javascript:alert(1))").includes("<a"));
  assert.ok(!L.renderMarkdown("[x](javascript:alert)").includes("<a"));
  assert.ok(!L.renderMarkdown("[x](data:text/html,hi)").includes("href"));
  assert.ok(!L.renderMarkdown("[x](JaVaScRiPt:alert)").includes("href"));
});

test("renderMarkdown: XSS payloads are always inert", () => {
  const payloads = [
    "<script>alert(1)</script>",
    "<img src=x onerror=alert(1)>",
    '<a href="javascript:alert(1)">x</a>',
    "**<svg onload=alert(1)>**",
    "[<img src=x onerror=alert(1)>](https://ok.com)",
    '[x](https://ok.com" onmouseover="alert(1))',
    "`<script>`",
    "```\n</code></pre><script>alert(1)</script>\n```",
    "- <iframe src=javascript:alert(1)>",
    "> <style>body{}</style>",
    "https://evil.com/\"><script>alert(1)</script>",
    "0<b>",
  ];
  for (const p of payloads) {
    const html = L.renderMarkdown(p);
    assert.ok(!/<(script|img|svg|iframe|style|b)[\s>]/i.test(html), "no raw tags for: " + p + " -> " + html);
    assert.ok(!/\son\w+=/i.test(html.replace(/&[a-z#0-9]+;/gi, "")) || !/<[^>]+\son\w+=/i.test(html), "no event handler attrs: " + html);
    assert.ok(!/href="(?!https?:)/i.test(html), "only http(s) hrefs: " + html);
    // every tag we emit is from the allow-list
    const tags = html.match(/<\/?([a-z0-9]+)/gi) || [];
    for (const t of tags) {
      assert.ok(/^<\/?(p|br|strong|em|del|code|pre|a|ul|ol|li|h[3-6]|blockquote)$/i.test(t), "unexpected tag " + t + " in " + html);
    }
  }
});

test("escapeHtml", () => {
  assert.equal(L.escapeHtml(`<a href="x">'&'</a>`), "&lt;a href=&quot;x&quot;&gt;&#39;&amp;&#39;&lt;/a&gt;");
  assert.equal(L.escapeHtml(null), "");
});

test("cleanForDisplay strips think blocks (complete and unclosed)", () => {
  assert.equal(L.cleanForDisplay("<think>hmm</think>\nHello"), "Hello");
  assert.equal(L.cleanForDisplay("Hi <think>still thinking"), "Hi ");
  assert.equal(L.cleanForDisplay("  Plain"), "Plain");
  assert.equal(L.cleanForDisplay(undefined), "");
});

test("stripMarkdown keeps identifiers and multiplication intact", () => {
  assert.equal(L.stripMarkdown("check that MISTRAL_API_KEY is set"), "check that MISTRAL_API_KEY is set");
  assert.equal(L.stripMarkdown("use snake_case and C#"), "use snake_case and C#");
  assert.equal(L.stripMarkdown("Price 5*3=15"), "Price 5 × 3=15");
  assert.equal(L.stripMarkdown("5 * 3 = 15"), "5 × 3 = 15");
  assert.equal(L.stripMarkdown("**bold** and *it* and _em_"), "bold and it and em");
});

test("normalizeForSpeech: what the browser voice must not read literally", () => {
  const en = (s) => L.normalizeForSpeech(s, "en");
  assert.equal(en("See https://en.wikipedia.org/wiki/Titanfall_2 for details."), "See link for details.");
  assert.equal(en("🎉 Great stuff!"), "Great stuff!");
  assert.equal(en("|---|---|\n| Alpha | 10 |"), "Alpha 10");
  assert.equal(en("It is 20 °C"), "It is 20 degrees Celsius");
  assert.equal(en("Wind 10 km/h"), "Wind 10 kilometers per hour");
  assert.equal(en("5*3 is 15"), "5 times 3 is 15");
  assert.equal(en("rock & roll"), "rock and roll");
  assert.ok(!/MISTRAL_API/.test(en("set MISTRAL_API_KEY")));
  assert.match(L.normalizeForSpeech("5*3", "de"), /mal/);
});

test("stripMarkdown produces speakable text", () => {
  assert.equal(L.stripMarkdown("**Bold** _it_ `x` [link](https://a.b)"), "Bold it x link");
  assert.equal(L.stripMarkdown("## Heading\n- item"), "Heading\nitem");
  assert.equal(L.stripMarkdown("before ```code``` after"), "before after");
});

// ------------------------------------------------------------------ wake word

test("matchWakeWord: command extraction with punctuation", () => {
  assert.deepEqual(L.matchWakeWord("Hermes, what's the time?"), { woke: true, command: "what's the time", index: 0, leading: true, trailing: false, bare: false });
  assert.equal(L.matchWakeWord("Hermes what is the weather in Berlin").command, "what is the weather in Berlin");
  assert.equal(L.matchWakeWord("hey Hermes. Tell me a joke!").command, "Tell me a joke");
  assert.equal(L.matchWakeWord("Hermes").command, "");
  assert.equal(L.matchWakeWord("Hermes?").woke, true);
});

test("matchWakeWord: tolerant to common mis-hearings", () => {
  for (const heard of ["Hermès", "HERMES", "hermis", "her mess", "herms", "ermes", "Hermez", "Hermes's", "hermus"]) {
    assert.equal(L.matchWakeWord(heard + " what time is it").woke, true, heard);
  }
  assert.equal(L.matchWakeWord("her mess, open the pod bay doors").command, "open the pod bay doors");
  assert.equal(L.matchWakeWord("Hermès, wie spät ist es?").command, "wie spät ist es");
});

test("matchWakeWord: no false positives on similar words", () => {
  for (const heard of ["thermes", "hermetic seal", "permission", "the hermit", "", "chairmen"]) {
    assert.equal(L.matchWakeWord(heard).woke, false, heard);
  }
});

test("matchWakeWord: a trailing wake word keeps the request before it", () => {
  const w = L.matchWakeWord("What time is it, Hermes?");
  assert.equal(w.woke, true);
  assert.equal(w.command, "What time is it");
  assert.equal(w.trailing, true);
  assert.equal(w.bare, false);
  assert.equal(L.matchWakeWord("tell me a joke hermes").command, "tell me a joke");
  assert.equal(L.matchWakeWord("ok, tell me a joke, Hermes.").command, "tell me a joke");
  // just the wake word (plus filler) is bare: answer "Yes?"
  for (const s of ["Hermes", "hey Hermes", "ok hermes!", "Hermes, please", "hallo Hermes"]) {
    assert.equal(L.matchWakeWord(s).bare, true, s);
  }
  // text after the wake word still wins
  assert.equal(L.matchWakeWord("so Hermes, what is up").command, "what is up");
});

test("decideFinal: push-to-talk strips a leading or trailing wake word", () => {
  assert.deepEqual(L.decideFinal("hey hermes, what time is it", { ptt: true }), { action: "send", command: "what time is it" });
  assert.deepEqual(L.decideFinal("ok hermes what time", { ptt: true }), { action: "send", command: "what time" });
  assert.deepEqual(L.decideFinal("Hermes what time", { ptt: true }), { action: "send", command: "what time" });
  assert.deepEqual(L.decideFinal("what time is it, Hermes", { ptt: true }), { action: "send", command: "what time is it" });
  // a mid-sentence mention is part of the request
  assert.deepEqual(L.decideFinal("I told Hermes about it", { ptt: true }), { action: "send", command: "I told Hermes about it" });
  assert.deepEqual(L.decideFinal("hey Hermes", { ptt: true }).action, "ack");
  assert.deepEqual(L.decideFinal("tell me a joke", { ptt: true }), { action: "send", command: "tell me a joke" });
});

test("decideFinal: wake word, call mode, armed window and echoes", () => {
  // trailing wake word sends the request instead of only answering "Yes?"
  assert.deepEqual(L.decideFinal("What time is it, Hermes?", { call: true }), { action: "send", command: "What time is it" });
  assert.deepEqual(L.decideFinal("Hermes", { call: true }).action, "ack");
  assert.deepEqual(L.decideFinal("Hermes, tell me a joke", { call: true }), { action: "send", command: "tell me a joke" });
  // call mode, not addressed: ignored unless armed
  assert.equal(L.decideFinal("tell me a joke", { call: true }).action, "ignore");
  assert.deepEqual(L.decideFinal("tell me a joke", { call: true, armed: true }), { action: "send", command: "tell me a joke" });
  // HERMES' own "Yes?" coming back while armed
  assert.equal(L.decideFinal("Yes.", { call: true, armed: true }).action, "echo");
  assert.equal(L.decideFinal("ja", { call: true, armed: true }).action, "echo");
  // without call mode everything heard is a request
  assert.deepEqual(L.decideFinal("tell me a joke", {}), { action: "send", command: "tell me a joke" });
});

test("recognition errors: transient ones never give up, fatal ones do", () => {
  assert.equal(L.recognitionErrorPolicy("not-allowed").fatal, true);
  assert.equal(L.recognitionErrorPolicy("service-not-allowed").fatal, true);
  assert.equal(L.recognitionErrorPolicy("audio-capture").transient, true);
  assert.equal(L.recognitionErrorPolicy("network").transient, true);
  assert.equal(L.recognitionErrorPolicy("no-speech").failures, 0);
  assert.equal(L.recognitionErrorPolicy("aborted").failures, 0);
  assert.equal(L.recognitionErrorPolicy("bad-grammar").transient, false);

  // simulate the app loop: error adds the policy failures, the quick end adds 1
  function cycles(code, n) {
    let failures = 0;
    let plan;
    let elapsed = 0;
    for (let i = 0; i < n; i++) {
      const pol = L.recognitionErrorPolicy(code);
      failures += pol.failures + 1;
      plan = L.restartPlan(failures, pol.transient);
      if (plan.giveUp) return { gaveUp: true, at: i, elapsed };
      elapsed += plan.delay;
    }
    return { gaveUp: false, plan, elapsed };
  }
  const noMic = cycles("audio-capture", 50);
  assert.equal(noMic.gaveUp, false, "a missing microphone keeps retrying");
  assert.equal(noMic.plan.delay, 15000, "capped back-off");
  assert.equal(cycles("network", 50).gaveUp, false, "offline keeps retrying");
  assert.equal(cycles("bad-grammar", 50).gaveUp, true, "persistent unknown errors still give up");
});

test("restartPlan: back-off ladder", () => {
  assert.deepEqual(L.restartPlan(0, false), { delay: 150, giveUp: false });
  assert.deepEqual(L.restartPlan(1, false), { delay: 600, giveUp: false });
  assert.deepEqual(L.restartPlan(3, false), { delay: 2400, giveUp: false });
  assert.deepEqual(L.restartPlan(6, false), { delay: 15000, giveUp: false });
  assert.deepEqual(L.restartPlan(8, false), { delay: 15000, giveUp: false });
  assert.deepEqual(L.restartPlan(9, false), { delay: 0, giveUp: true });
  assert.deepEqual(L.restartPlan(40, true), { delay: 15000, giveUp: false });
});

test("isStopCommand", () => {
  for (const s of ["stop", "Stop!", "Hermes stop", "Hermes, stop.", "hermes halt", "stopp", "Hermes, sei still", "Hermes be quiet", "her mess stop", "okay stop", "stop please"]) {
    assert.equal(L.isStopCommand(s), true, s);
  }
  for (const s of ["stop the timer and tell me a joke", "Hermes, what time is it", "", "bus stop nearby", "Hermes"]) {
    assert.equal(L.isStopCommand(s), false, s);
  }
});

test("normalizeSpeech", () => {
  assert.equal(L.normalizeSpeech("  Hermès,  WHAT?! "), "hermes what");
});

// ------------------------------------------------------------------ conversation

test("formatChatAsMarkdown", () => {
  const ts = new Date(2026, 9, 31, 14, 5).getTime();
  const md = L.formatChatAsMarkdown(
    [
      { role: "user", content: "Hi", ts },
      { role: "assistant", content: "Hello **pilot**", ts, images: ["/gen/a.png"] },
      { role: "assistant", content: "Link lost", error: true },
      { role: "system", content: "hidden" },
    ],
    { date: ts }
  );
  assert.ok(md.startsWith("# HERMES conversation\n"));
  assert.ok(md.includes("_Exported 2026-10-31 14:05_"));
  assert.ok(md.includes("### **You** · 2026-10-31 14:05\n\nHi"));
  assert.ok(md.includes("### **HERMES** · 2026-10-31 14:05\n\nHello **pilot**"));
  assert.ok(md.includes("![image 1](/gen/a.png)"));
  assert.ok(md.includes("(error)"));
  assert.ok(!md.includes("hidden"));
  assert.ok(md.endsWith("\n") && !md.includes("\n\n\n"));
  assert.equal(L.formatChatAsMarkdown([]), "# HERMES conversation\n");
});

test("buildRequestMessages drops failed exchanges and pending replies", () => {
  const conv = [
    { role: "user", content: "a" },
    { role: "assistant", content: "A" },
    { role: "user", content: "b" },
    { role: "assistant", content: "Link lost", error: true },
    { role: "user", content: "c" },
    { role: "assistant", content: "", pending: true },
  ];
  assert.deepEqual(L.buildRequestMessages(conv), [
    { role: "user", content: "a" },
    { role: "assistant", content: "A" },
    { role: "user", content: "c" },
  ]);
});

test("buildRequestMessages: retry keeps the final user turn, collapses doubles, caps length", () => {
  assert.deepEqual(L.buildRequestMessages([{ role: "user", content: "x" }, { role: "user", content: "y" }]), [{ role: "user", content: "y" }]);
  const many = [];
  for (let i = 0; i < 100; i++) many.push({ role: i % 2 ? "assistant" : "user", content: "m" + i });
  const out = L.buildRequestMessages(many, 40);
  assert.ok(out.length <= 40);
  assert.equal(out[0].role, "user", "history starts with a user turn");
  assert.equal(out[out.length - 1].content, "m99");
});

test("rms", () => {
  assert.equal(L.rms(new Float32Array([0, 0, 0])), 0);
  assert.ok(Math.abs(L.rms(new Float32Array([1, -1, 1, -1])) - 1) < 1e-9);
  assert.ok(Math.abs(L.rms(new Uint8Array([128, 128])) - 0) < 1e-9);
  assert.equal(L.rms(null), 0);
});
