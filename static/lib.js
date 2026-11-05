/*
 * HERMES pure-logic helpers (no DOM). Shared by the browser (window.HermesLib)
 * and the node tests (require("../../static/lib.js")).
 *
 *   clamp, parseNum, normalizeSettings, DEFAULT_SETTINGS
 *   splitSentences, extractCompleteSentences, createSentenceStream
 *   parseSSEChunk, createSSEParser
 *   escapeHtml, renderMarkdown, cleanForDisplay, stripMarkdown
 *   matchWakeWord, isStopCommand, normalizeSpeech, decideFinal,
 *   recognitionErrorPolicy, restartPlan, normalizeForSpeech
 *   formatChatAsMarkdown, buildRequestMessages, rms
 */
(function (root, factory) {
  var api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root && typeof root === "object") root.HermesLib = api;
})(typeof window !== "undefined" ? window : typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  // ------------------------------------------------------------ numbers

  function clamp(x, lo, hi) {
    return x < lo ? lo : x > hi ? hi : x;
  }

  /*
   * Parse a number from user input without the `|| default` trap: 0 stays 0.
   * Non-numeric / non-finite input returns `fallback`. Optional clamping.
   */
  function parseNum(value, fallback, lo, hi) {
    var n;
    if (typeof value === "number") n = value;
    else if (typeof value === "string" && value.trim() !== "") n = Number(value.trim().replace(",", "."));
    else n = NaN;
    if (!isFinite(n)) return fallback;
    if (typeof lo === "number" && typeof hi === "number") n = clamp(n, lo, hi);
    return n;
  }

  // ------------------------------------------------------------ settings

  var DEFAULT_SETTINGS = {
    systemPrompt: "",
    temperature: 0.7,
    maxTokens: 1024,
    voice: "",
    voiceMode: "piper",
    pitch: 0.6,
    rate: 1.0,
    volume: 1.0,
    speak: true,
    call: true,
    localModel: "qwen3.5:latest",
    eyeDesign: "classic",
    piperVoice: "BT7274",
    autoScroll: true,
    voiceSpeed: 1.0,
    voiceFx: "titan",
    streamSpeech: true,
  };

  var EYE_DESIGNS = ["classic", "cyclops", "iris", "cortana"];
  var VOICE_FX = ["none", "titan", "radio"];

  function bool(v, d) {
    return typeof v === "boolean" ? v : d;
  }

  /* Mirror of the server-side normalize(): clamps, defaults, migrations. */
  function normalizeSettings(s) {
    var d = DEFAULT_SETTINGS;
    s = s && typeof s === "object" ? s : {};
    var out = {};
    out.systemPrompt = typeof s.systemPrompt === "string" ? s.systemPrompt : d.systemPrompt;
    out.temperature = parseNum(s.temperature, d.temperature, 0, 2);
    out.maxTokens = Math.round(parseNum(s.maxTokens, d.maxTokens, 16, 8192));
    out.voice = typeof s.voice === "string" ? s.voice : "";
    out.voiceMode = s.voiceMode === "browser" ? "browser" : "piper";
    out.pitch = parseNum(s.pitch, d.pitch, 0, 2);
    var rate = parseNum(s.rate, d.rate);
    out.rate = rate <= 0 ? 1.0 : clamp(rate, 0.1, 3); // legacy negative rates
    out.volume = parseNum(s.volume, d.volume, 0, 1);
    out.speak = bool(s.speak, d.speak);
    out.call = bool(s.call, d.call);
    out.localModel = typeof s.localModel === "string" && s.localModel.trim() ? s.localModel.trim() : d.localModel;
    out.eyeDesign = EYE_DESIGNS.indexOf(s.eyeDesign) >= 0 ? s.eyeDesign : d.eyeDesign;
    out.piperVoice = typeof s.piperVoice === "string" && s.piperVoice ? s.piperVoice : d.piperVoice;
    out.autoScroll = bool(s.autoScroll, d.autoScroll);
    var speed = parseNum(s.voiceSpeed, d.voiceSpeed);
    out.voiceSpeed = speed <= 0 ? 1.0 : clamp(speed, 0.5, 2);
    out.voiceFx = VOICE_FX.indexOf(s.voiceFx) >= 0 ? s.voiceFx : d.voiceFx;
    out.streamSpeech = bool(s.streamSpeech, d.streamSpeech);
    return out;
  }

  // ------------------------------------------------------------ sentences

  // Lower-cased tokens (without the final ".") that do not end a sentence.
  var ABBREVIATIONS = {};
  [
    "e.g", "i.e", "vs", "mr", "mrs", "ms", "dr", "prof", "st", "jr", "sr", "approx", "ca",
    "fig", "dept", "est", "inc", "ltd", "co", "corp", "mt", "lt", "col", "gen", "sgt", "capt", "cmdr",
    "z.b", "d.h", "u.a", "bzw", "nr", "evtl", "ggf", "hr", "fr", "str", "inkl", "zzgl", "vgl", "bspw",
    "u.s", "u.k", "a.m", "p.m", "jan", "feb", "mar", "apr", "jun", "jul", "aug", "sep", "sept", "oct", "nov", "dec",
  ].forEach(function (a) {
    ABBREVIATIONS[a] = true;
  });
  // Abbreviations that commonly end a sentence too: a boundary only when the
  // next word starts with a capital letter.
  var SOFT_ABBREVIATIONS = { etc: true, usw: true, ff: true };

  // Number abbreviations ("No. 5", "Nr. 3"): only before a digit.
  var NUMBER_ABBREVIATIONS = { no: true, nr: true };

  // Words that start a new sentence after a single capital letter
  // ("Plan B. We go now.") but rarely follow an initial ("J. R. R. Tolkien").
  var SENTENCE_STARTERS = /^\s*["'“‘«(]*(?:the|it|we|i|this|that|he|she|they|you|there|then|so|but|and|now|es|das|die|der|wir|ich|sie|er|dann|aber|und|jetzt)\b/i;

  // Characters that may follow a terminator and still belong to the sentence:
  // quotes, brackets and Markdown emphasis markers ("**Yes.** Next").
  var CLOSERS = "\"')]}»”’«“*_";

  function isUpperStart(s) {
    var m = /^\s*["'“‘«(]*(\S)/.exec(s);
    if (!m) return false;
    var c = m[1];
    return c !== c.toLowerCase() && c === c.toUpperCase();
  }

  /*
   * Is the "." at text[i] a real sentence end? Called only when a whitespace
   * character follows it (so the sentence is known to be complete).
   */
  function periodEndsSentence(text, i) {
    // ellipsis "..." -> only before a capitalised word
    if (text[i - 1] === ".") return isUpperStart(text.slice(i + 1));
    var m = /([A-Za-zÀ-ÿ0-9.]+)$/.exec(text.slice(Math.max(0, i - 24), i));
    var tok = m ? m[1] : "";
    var low = tok.toLowerCase().replace(/^\.+/, "");
    if (!low) return true;
    var after = text.slice(i + 1);
    if (ABBREVIATIONS[low]) return false;
    if (NUMBER_ABBREVIATIONS[low]) return !/^\s*\d/.test(after); // "No. 5" vs "The answer is no."
    if (SOFT_ABBREVIATIONS[low]) return isUpperStart(after);
    if (/^\d{1,3}$/.test(low)) return isUpperStart(after) && !/^\s*(januar|februar|märz|april|mai|juni|juli|august|september|oktober|november|dezember)\b/i.test(after); // "3. Oktober", "1. item"
    if (/^[a-z]$/i.test(tok)) {
      // a unit after a number or symbol: "20 °C.", "10 km/h.", "5 m."
      var before = text.slice(Math.max(0, i - tok.length - 3), i - tok.length);
      if (/(?:[\d°\/]|\d\s)$/.test(before)) return true;
      // "Plan B. We go now." — a capital letter followed by a typical
      // sentence opener; real initials are followed by names.
      return SENTENCE_STARTERS.test(after);
    }
    if (/^([a-z]\.)+[a-z]$/i.test(tok)) return false; // "U.S.A"
    return true;
  }

  /*
   * Find complete sentences in a (possibly still growing) buffer.
   * A sentence is complete when its terminator is followed by whitespace, or
   * at a line break. Fragments shorter than opts.minChars are merged with the
   * following sentence. Over-long runs (> opts.maxChars) are cut at a comma.
   * Returns { sentences: [...trimmed], rest: "unfinished tail" }.
   */
  function extractCompleteSentences(buffer, opts) {
    opts = opts || {};
    var minChars = opts.minChars == null ? 10 : opts.minChars;
    var maxChars = opts.maxChars || 220;
    var text = String(buffer || "");
    var sentences = [];
    var start = 0;
    var i = 0;
    var n = text.length;

    function emit(end) {
      var s = text.slice(start, end).trim();
      if (s && s.length >= minChars) {
        pushCut(s);
        start = end;
        return true;
      }
      return false;
    }

    // a closed sentence/line can still be too long to speak in one go
    function pushCut(s) {
      var r = cutLong(s, maxChars, minChars);
      r.pieces.forEach(function (p) {
        sentences.push(p);
      });
      if (r.rest.trim()) sentences.push(r.rest.trim());
    }

    while (i < n) {
      var c = text[i];
      if (c === "\n") {
        // A line break ends a chunk (list items, paragraphs) if long enough;
        // short headings merge forward.
        var seg = text.slice(start, i).trim();
        if (seg && seg.length >= minChars) {
          pushCut(seg);
          start = i + 1;
        }
        i++;
        continue;
      }
      if (c === "." || c === "!" || c === "?" || c === "…") {
        var j = i + 1;
        while (j < n && (text[j] === "!" || text[j] === "?" || (c !== "." && text[j] === "."))) j++;
        while (j < n && CLOSERS.indexOf(text[j]) >= 0) j++;
        if (j >= n) break; // need the following character to be sure
        if (/\s/.test(text[j])) {
          var ends = true;
          if (c === "." && j === i + 1) ends = periodEndsSentence(text, i);
          else if (c === "." || c === "…") {
            // "..." / "…": only before a capitalised word
            ends = isUpperStart(text.slice(j));
          }
          if (ends) emit(j);
        }
        i = j;
        continue;
      }
      i++;
    }

    // keep very long unpunctuated runs flowing: cut at the last comma
    var tail = cutLong(text.slice(start), maxChars, minChars);
    tail.pieces.forEach(function (p) {
      sentences.push(p);
    });
    return { sentences: sentences, rest: tail.rest };
  }

  /*
   * Cut text longer than maxChars at the last clause break (", " "; " ": "
   * " – ") inside the window, else at the last space. Returns the cut pieces
   * and the remainder (<= maxChars unless it has no usable break).
   */
  function cutLong(rest, maxChars, minChars) {
    var pieces = [];
    var floor = Math.max(1, minChars || 0);
    while (rest.length > maxChars) {
      var window_ = rest.slice(0, maxChars);
      var cut = Math.max(window_.lastIndexOf(", "), window_.lastIndexOf("; "), window_.lastIndexOf(": "), window_.lastIndexOf(" – "), window_.lastIndexOf(" - "));
      if (cut < floor) cut = window_.lastIndexOf(" ");
      if (cut < floor) break;
      var piece = rest.slice(0, cut + 1).trim();
      if (piece) pieces.push(piece);
      rest = rest.slice(cut + 1).replace(/^\s+/, "");
    }
    return { pieces: pieces, rest: rest };
  }

  /* Split complete text into speakable sentences. */
  function splitSentences(text, opts) {
    var r = extractCompleteSentences(String(text || "") + "\n", Object.assign({}, opts, { minChars: 0 }));
    var minChars = !opts || opts.minChars == null ? 10 : opts.minChars;
    var raw = r.sentences.slice();
    if (r.rest.trim()) raw.push(r.rest.trim());
    // merge short fragments forward; a short tail joins the previous one
    var out = [];
    var pending = "";
    raw.forEach(function (s) {
      pending = pending ? pending + " " + s : s;
      if (pending.length >= minChars) {
        out.push(pending);
        pending = "";
      }
    });
    if (pending) {
      if (out.length) out[out.length - 1] += " " + pending;
      else out.push(pending);
    }
    return out;
  }

  /*
   * Streaming sentence splitter for speech. push(delta) returns sentences that
   * are complete; flush() returns whatever is left. Fenced code blocks are
   * skipped (nobody wants code read aloud), as are <think> blocks.
   */
  function createSentenceStream(opts) {
    var raw = ""; // unprocessed input (may hold a partial fence / tag)
    var prose = ""; // prose waiting for a sentence boundary
    var inCode = false;
    var inThink = false;

    function drain(final) {
      for (;;) {
        if (inCode) {
          var close = raw.indexOf("```");
          if (close < 0) {
            raw = final ? "" : raw.slice(-2);
            return;
          }
          raw = raw.slice(close + 3).replace(/^[^\n]*\n?/, "");
          inCode = false;
          prose += "\n";
          continue;
        }
        if (inThink) {
          var end = raw.indexOf("</think>");
          if (end < 0) {
            raw = final ? "" : raw.slice(-7);
            return;
          }
          raw = raw.slice(end + 8);
          inThink = false;
          continue;
        }
        var fence = raw.indexOf("```");
        var think = raw.indexOf("<think>");
        var first = fence < 0 ? think : think < 0 ? fence : Math.min(fence, think);
        if (first < 0) {
          // hold back a possible partial marker at the end
          var hold = 0;
          if (!final) {
            var m = /(`{1,2}|<t?h?i?n?k?)$/.exec(raw);
            if (m && (m[1][0] === "`" || "<think".indexOf(m[1]) === 0)) hold = m[1].length;
          }
          prose += raw.slice(0, raw.length - hold);
          raw = raw.slice(raw.length - hold);
          return;
        }
        prose += raw.slice(0, first);
        if (first === fence) {
          inCode = true;
          raw = raw.slice(fence + 3);
        } else {
          inThink = true;
          raw = raw.slice(think + 7);
        }
        prose += "\n";
      }
    }

    function clean(list) {
      var out = [];
      list.forEach(function (s) {
        var t = stripMarkdown(s);
        if (/[\p{L}\p{N}]/u.test(t)) out.push(t);
      });
      return out;
    }

    return {
      push: function (delta) {
        raw += String(delta || "");
        drain(false);
        var r = extractCompleteSentences(prose, opts);
        prose = r.rest;
        return clean(r.sentences);
      },
      flush: function () {
        drain(true);
        var r = extractCompleteSentences(prose + "\n", opts);
        var out = r.sentences;
        if (r.rest.trim()) out.push(r.rest.trim());
        prose = "";
        raw = "";
        inCode = false;
        inThink = false;
        return clean(out);
      },
    };
  }

  // ------------------------------------------------------------ SSE

  function parsePayload(data) {
    if (data === "[DONE]") return { done: true };
    try {
      return JSON.parse(data);
    } catch (e) {
      return { raw: data };
    }
  }

  /*
   * Parse a chunk of an SSE stream. `buffer` is the carry-over from the
   * previous call plus new text. Returns { events: [parsed objects], rest }.
   * Events end at a blank line; CRLF / CR line endings are accepted; comment
   * lines (":") and non-data fields are ignored. Multiple data: lines in one
   * event are joined with "\n" before JSON parsing.
   */
  function parseSSEChunk(buffer) {
    var text = String(buffer || "").replace(/\r\n?/g, "\n");
    var events = [];
    var cut = 0;
    var idx;
    while ((idx = text.indexOf("\n\n", cut)) >= 0) {
      var block = text.slice(cut, idx);
      cut = idx + 2;
      var data = [];
      block.split("\n").forEach(function (line) {
        if (line.indexOf("data:") === 0) data.push(line.slice(5).replace(/^ /, ""));
      });
      if (data.length) {
        var payload = data.join("\n");
        if (payload.trim()) events.push(parsePayload(payload.trim()));
      }
    }
    var rest = text.slice(cut);
    // A trailing lone "\r" may be the first half of a CRLF; keep it raw.
    if (/\r$/.test(buffer) && !/\r\n$/.test(buffer)) rest = rest.replace(/\n$/, "\r");
    return { events: events, rest: rest };
  }

  /* Stateful wrapper: push(text) -> events; end() -> trailing event (if any). */
  function createSSEParser() {
    var buf = "";
    return {
      push: function (chunk) {
        var r = parseSSEChunk(buf + chunk);
        buf = r.rest;
        return r.events;
      },
      end: function () {
        var r = parseSSEChunk(buf + "\n\n");
        buf = "";
        return r.events;
      },
    };
  }

  // ------------------------------------------------------------ text / markdown

  var HTML_ESCAPES = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };

  function escapeHtml(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return HTML_ESCAPES[c];
    });
  }

  /* Remove model "thinking" blocks (complete, and an unclosed trailing one). */
  function cleanForDisplay(text) {
    return String(text || "")
      .replace(/<think>[\s\S]*?<\/think>\s*/g, "")
      .replace(/<think>[\s\S]*$/, "")
      .replace(/\u0000/g, "")
      .replace(/^\s+/, "");
  }

  /*
   * Markdown -> plain text (for speech captions and clipboard-free uses).
   * Only markup is removed: "5*3" becomes "5 × 3" (not "53"), and "_" / "#"
   * inside words survive (MISTRAL_API_KEY, snake_case, C#).
   */
  function stripMarkdown(s) {
    return String(s || "")
      .replace(/```[\s\S]*?```/g, " ")
      .replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1")
      .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
      .replace(/`([^`]*)`/g, "$1")
      .replace(/(\d)[ \t]*\*[ \t]*(?=[-+]?\d)/g, "$1 × ") // multiplication, before emphasis rules
      .replace(/(\*\*|__)(.+?)\1/g, "$2")
      .replace(/(^|[\s(])[*_]([^*_\n]+)[*_](?=[\s).,!?:;]|$)/g, "$1$2")
      .replace(/~~(.+?)~~/g, "$1")
      .replace(/^\s{0,3}#{1,6}\s+/gm, "")
      .replace(/^\s{0,3}>\s?/gm, "")
      .replace(/^\s*(?:[-*+]|\d+[.)])\s+/gm, "")
      .replace(/[*~`]+/g, "")
      .replace(/(^|[^\p{L}\p{N}_])_+|_+(?![\p{L}\p{N}_])/gu, "$1") // stray emphasis "_", not snake_case
      .replace(/(^|[^\p{L}\p{N}#])#+/gu, "$1") // stray "#", not C#
      .replace(/[ \t]+/g, " ")
      .trim();
  }

  var SPEECH_WORDS = {
    en: { link: "link", celsius: " degrees Celsius", fahrenheit: " degrees Fahrenheit", degrees: " degrees", percent: " percent", and: " and ", kmh: " kilometers per hour", times: " times " },
    de: { link: "Link", celsius: " Grad Celsius", fahrenheit: " Grad Fahrenheit", degrees: " Grad", percent: " Prozent", and: " und ", kmh: " Kilometer pro Stunde", times: " mal " },
  };

  /*
   * Plain text -> what a TTS voice should read (client mirror of the server's
   * normalizeForSpeech, used for the browser speechSynthesis engine): URLs
   * become "link", emoji are dropped, table pipes removed, units, "&" and
   * multiplication spelled out. lang is "de" for German.
   */
  function normalizeForSpeech(text, lang) {
    var w = SPEECH_WORDS[lang === "de" ? "de" : "en"];
    var lines = String(text || "")
      .replace(/\r\n?/g, "\n")
      .replace(/\b(?:https?:\/\/|www\.)[^\s<>()\[\]]+/gi, w.link)
      .split("\n")
      .map(function (line) {
        return line.replace(/\|/g, " ").trim();
      })
      .filter(function (line) {
        return line && /[\p{L}\p{N}]/u.test(line);
      });
    // end unpunctuated lines so they are not read as one run-on sentence
    var t = lines
      .map(function (line, i) {
        return i < lines.length - 1 && !/[.!?:;,…]$/.test(line) ? line + "." : line;
      })
      .join(" ");
    return t
      .replace(/(\d)\s*[×*]\s*(?=[-+]?\d)/g, "$1" + w.times)
      .replace(/\s*°\s*C\b/g, w.celsius)
      .replace(/\s*°\s*F\b/g, w.fahrenheit)
      .replace(/\s*°/g, w.degrees)
      .replace(/\s*%/g, w.percent)
      .replace(/\s*&\s*/g, w.and)
      .replace(/\s*\bkm\/h\b/gi, w.kmh)
      .replace(/(\p{L})_+(?=[\p{L}\p{N}])/gu, "$1 ")
      .replace(/[\p{Extended_Pictographic}\u{1F1E6}-\u{1F1FF}\u{FE0E}\u{FE0F}\u{200D}\u{20E3}\u{E0020}-\u{E007F}]/gu, "")
      .replace(/\s+/g, " ")
      .replace(/\s+([.,!?;:])/g, "$1")
      .trim();
  }

  var PH_OPEN = "";
  var PH_CLOSE = "";

  function safeUrl(url) {
    // url is already HTML-escaped text; decode the entities we produced to test
    var plain = url.replace(/&amp;/g, "&").replace(/&#39;/g, "'").replace(/&quot;/g, '"');
    if (!/^https?:\/\/[^\s<>"']+$/i.test(plain)) return null;
    return escapeHtml(plain);
  }

  function renderInline(text) {
    var slots = [];
    function hold(html) {
      slots.push(html);
      return PH_OPEN + (slots.length - 1) + PH_CLOSE;
    }
    var s = String(text).replace(/[]/g, "");
    // code spans first: their content is never formatted
    s = s.replace(/`([^`\n]+)`/g, function (_, code) {
      return hold("<code>" + escapeHtml(code) + "</code>");
    });
    s = escapeHtml(s);
    // [label](url)
    s = s.replace(/\[([^\]\n]+)\]\(([^()\s]+)\)/g, function (all, label, url) {
      var href = safeUrl(url);
      if (!href) return label;
      return hold('<a href="' + href + '" target="_blank" rel="noopener noreferrer">' + label + "</a>");
    });
    // bare URLs
    s = s.replace(/(^|[\s(])(https?:\/\/[^\s<>()]+[^\s<>().,!?;:'"])/g, function (all, pre, url) {
      var href = safeUrl(url);
      if (!href) return all;
      return pre + hold('<a href="' + href + '" target="_blank" rel="noopener noreferrer">' + url + "</a>");
    });
    s = s.replace(/(\*\*|__)(?=\S)([\s\S]*?\S)\1/g, "<strong>$2</strong>");
    s = s.replace(/(^|[^*\w])\*(?=\S)([^*\n]*?\S)\*(?!\*)/g, "$1<em>$2</em>");
    s = s.replace(/(^|[^_\w])_(?=\S)([^_\n]*?\S)_(?![_\w])/g, "$1<em>$2</em>");
    s = s.replace(/~~(?=\S)([^~\n]*?\S)~~/g, "<del>$1</del>");
    s = s.replace(new RegExp(PH_OPEN + "(\\d+)" + PH_CLOSE, "g"), function (_, k) {
      return slots[+k];
    });
    return s;
  }

  /*
   * Minimal, XSS-safe Markdown -> HTML string. Every piece of input text is
   * escaped; the only tags emitted are our own. Links are http(s) only.
   * Supports paragraphs, line breaks, headings, **bold**, *italic*, ~~del~~,
   * `code`, fenced code (an unclosed fence renders as code while streaming),
   * - / 1. lists, > quotes, [links](https://...) and bare URLs.
   */
  function renderMarkdown(src) {
    var lines = String(src == null ? "" : src).replace(/\r\n?/g, "\n").split("\n");
    var html = [];
    var para = [];
    var list = null; // {type, items}
    var quote = [];

    function flushPara() {
      if (para.length) html.push("<p>" + para.map(renderInline).join("<br>") + "</p>");
      para = [];
    }
    function flushList() {
      if (list) {
        html.push("<" + list.type + ">" + list.items.map(function (it) {
          return "<li>" + renderInline(it) + "</li>";
        }).join("") + "</" + list.type + ">");
      }
      list = null;
    }
    function flushQuote() {
      if (quote.length) html.push("<blockquote>" + quote.map(renderInline).join("<br>") + "</blockquote>");
      quote = [];
    }
    function flushAll() {
      flushPara();
      flushList();
      flushQuote();
    }

    for (var i = 0; i < lines.length; i++) {
      var line = lines[i];
      var fence = /^\s{0,3}```\s*([\w+#.-]*)/.exec(line);
      if (fence) {
        flushAll();
        var lang = (fence[1] || "").toLowerCase().replace(/[^a-z0-9+#-]/g, "");
        var code = [];
        i++;
        while (i < lines.length && !/^\s{0,3}```\s*$/.test(lines[i])) code.push(lines[i++]);
        html.push("<pre><code" + (lang ? ' class="lang-' + lang + '"' : "") + ">" + escapeHtml(code.join("\n")) + "</code></pre>");
        continue;
      }
      if (!line.trim()) {
        flushAll();
        continue;
      }
      var h = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/.exec(line);
      if (h) {
        flushAll();
        var lvl = Math.min(6, h[1].length + 2);
        html.push("<h" + lvl + ">" + renderInline(h[2]) + "</h" + lvl + ">");
        continue;
      }
      var ul = /^\s*[-*+•]\s+(.*)$/.exec(line);
      var ol = /^\s*\d{1,3}[.)]\s+(.*)$/.exec(line);
      if (ul || ol) {
        flushPara();
        flushQuote();
        var type = ul ? "ul" : "ol";
        if (list && list.type !== type) flushList();
        if (!list) list = { type: type, items: [] };
        list.items.push((ul || ol)[1]);
        continue;
      }
      var q = /^\s{0,3}>\s?(.*)$/.exec(line);
      if (q) {
        flushPara();
        flushList();
        quote.push(q[1]);
        continue;
      }
      if (list && /^\s{2,}\S/.test(line)) {
        list.items[list.items.length - 1] += " " + line.trim();
        continue;
      }
      flushList();
      flushQuote();
      para.push(line);
    }
    flushAll();
    return html.join("");
  }

  // ------------------------------------------------------------ voice commands

  /* Lower-case, strip diacritics, keep string length (index-aligned). */
  function foldChars(text) {
    var out = "";
    for (var i = 0; i < text.length; i++) {
      var c = text[i];
      var f = c.normalize ? c.normalize("NFD").replace(/[̀-ͯ]/g, "") : c;
      f = (f || c).toLowerCase();
      out += f.length === 1 ? f : c.length === 1 ? f[0] || c : c;
    }
    return out;
  }

  // Common speech-recognition renderings of "Hermes" (EN + DE recognisers).
  var WAKE_RE = /(^|[^a-z0-9])(hermes|hermess|hermez|hermis|hermus|hermos|hermies|hermies|herms|hermas|hermesh|ermes|hermit's|her mess|her miss|her mes|her mass|hair mess|hermy|harmes|harmess|airmes|air mess)(?=$|[^a-z0-9])/;

  // Filler that may surround the wake word without being a request.
  var WAKE_FILLER = /^(?:[\s,.:;!?\-–—]*(?:hey there|hey|hi|hello|hallo|ok|okay|yo|oh|so|well|na|also|ja|please|bitte)(?=$|[^a-z0-9]))*[\s,.:;!?\-–—]*/;

  function trimCommand(s) {
    return s
      .replace(/^[\s,.:;!?\-–—]+/, "")
      .replace(/[\s.!?,;:…\-–—]+$/, "")
      .trim();
  }

  /*
   * Detect the wake word anywhere in a transcript.
   * Returns { woke, command, index, leading, trailing, bare }:
   *   command  - the text after the wake word ("Hermes, what time is it?");
   *              when nothing follows it, the text before it ("What time is
   *              it, Hermes?"), minus filler like "hey" / "ok" and punctuation
   *   index    - character offset of the wake word
   *   leading  - only filler precedes the wake word ("hey Hermes, ...")
   *   trailing - the command came from the text before the wake word
   *   bare     - the utterance is just the wake word (plus filler)
   */
  function matchWakeWord(text) {
    var original = String(text || "");
    var folded = foldChars(original);
    var m = WAKE_RE.exec(folded);
    if (!m) return { woke: false, command: "", index: -1, leading: false, trailing: false, bare: false };
    var start = m.index + m[1].length;
    var end = start + m[2].length;
    var afterRaw = original.slice(end).replace(/^['’]s\b/, "");
    var after = trimCommand(afterRaw);
    // filler after the wake word ("Hermes, please") is not a command either
    var afterFiller = WAKE_FILLER.exec(foldChars(afterRaw))[0].length;
    if (!trimCommand(afterRaw.slice(afterFiller))) after = "";
    var beforeFolded = folded.slice(0, start);
    var filler = WAKE_FILLER.exec(beforeFolded)[0].length;
    var before = trimCommand(original.slice(filler, start));
    var leading = !before;
    var command = after;
    var trailing = false;
    if (!after && before) {
      command = before;
      trailing = true;
    }
    return { woke: true, command: command, index: start, leading: leading, trailing: trailing, bare: !command };
  }

  var STOP_RE = /^(?:(?:hey|ok|okay|hallo)\s+)?(?:stop|stopp|halt|stop it|stop talking|be quiet|quiet|silence|shut up|enough|cancel|hör auf|hoer auf|sei still|ruhe|abbrechen|genug)(?:\s+(?:please|bitte|now|jetzt))?$/;

  /*
   * True for "stop", "Hermes, stop!", "Hermes halt", "stopp", "Hermes sei
   * still"… The whole utterance must be the command, so a request like
   * "stop the timer and tell me a joke" is not treated as barge-in.
   */
  function isStopCommand(text) {
    var t = String(text || "");
    var wake = matchWakeWord(t);
    if (wake.woke) t = wake.command;
    t = foldChars(t)
      .replace(/[.!?,;:…"'“”]+/g, " ")
      .replace(/\s+/g, " ")
      .trim();
    if (!t) return false;
    return STOP_RE.test(t);
  }

  // HERMES' own acknowledgement coming back through the microphone.
  var ACK_ECHOES = { yes: 1, ja: 1, yeah: 1, "yes yes": 1, jaja: 1 };

  /*
   * What to do with a final speech-recognition result. Pure: the caller
   * handles duplicates, timing and side effects.
   *   state: { ptt, call, armed }
   * Returns { action: "send" | "ack" | "ignore" | "echo", command }
   *   send   - send `command` as a spoken request
   *   ack    - only the wake word was heard: answer "Yes?" and arm
   *   echo   - armed, but the text is our own "Yes?"
   *   ignore - heard, but not addressed to HERMES (call mode)
   */
  function decideFinal(text, state) {
    state = state || {};
    var w = matchWakeWord(text);
    var whole = String(text || "").trim();
    if (state.ptt) {
      // push-to-talk is explicitly addressed; strip a leading / trailing
      // "Hermes" but keep mid-sentence mentions ("I told Hermes ...")
      if (w.woke && w.bare) return { action: "ack", command: "" };
      return { action: "send", command: w.woke && (w.leading || w.trailing) ? w.command : whole };
    }
    if (w.woke) return w.command ? { action: "send", command: w.command } : { action: "ack", command: "" };
    if (!state.call) return { action: "send", command: whole };
    if (state.armed) {
      if (ACK_ECHOES[normalizeSpeech(text)]) return { action: "echo", command: "" };
      return { action: "send", command: whole };
    }
    return { action: "ignore", command: "" };
  }

  /*
   * Recognition-error policy. Transient errors (no microphone plugged in,
   * recognition service unreachable) keep retrying on a capped back-off and
   * never turn the always-on mic off; fatal ones (permission denied) do.
   */
  function recognitionErrorPolicy(code) {
    switch (code) {
      case "no-speech":
      case "aborted":
        return { failures: 0, transient: false, fatal: false };
      case "not-allowed":
      case "service-not-allowed":
        return { failures: 0, transient: false, fatal: true };
      case "audio-capture":
        return { failures: 2, transient: true, fatal: false };
      case "network":
        return { failures: 1, transient: true, fatal: false };
      default:
        return { failures: 1, transient: false, fatal: false };
    }
  }

  var RESTART_MAX_MS = 15000;
  var RESTART_GIVE_UP = 8;

  /*
   * Restart plan after a recognition session ended: { delay, giveUp }.
   * failures = consecutive failed sessions; transient = the last failure was
   * a transient condition (never gives up, retries at most every 15 s).
   */
  function restartPlan(failures, transient) {
    failures = Math.max(0, failures | 0);
    if (failures > RESTART_GIVE_UP && !transient) return { delay: 0, giveUp: true };
    var delay = failures ? Math.min(RESTART_MAX_MS, 300 * Math.pow(2, Math.min(failures, 16))) : 150;
    return { delay: delay, giveUp: false };
  }

  /* Normalise a transcript for de-duplication. */
  function normalizeSpeech(text) {
    return foldChars(String(text || ""))
      .replace(/[^\p{L}\p{N}\s]/gu, "")
      .replace(/\s+/g, " ")
      .trim();
  }

  // ------------------------------------------------------------ conversation

  function pad(n) {
    return (n < 10 ? "0" : "") + n;
  }

  function fmtDate(ts) {
    var d = new Date(ts);
    if (isNaN(d.getTime())) return "";
    return d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()) + " " + pad(d.getHours()) + ":" + pad(d.getMinutes());
  }

  /* Conversation -> Markdown document for export. */
  function formatChatAsMarkdown(messages, opts) {
    opts = opts || {};
    var title = opts.title || "HERMES conversation";
    var out = ["# " + title, ""];
    if (opts.date != null) out.push("_Exported " + fmtDate(opts.date) + "_", "");
    (messages || []).forEach(function (m) {
      if (!m || (m.role !== "user" && m.role !== "assistant")) return;
      var who = m.role === "user" ? "**You**" : "**HERMES**";
      var when = m.ts ? " · " + fmtDate(m.ts) : "";
      out.push("### " + who + when + (m.error ? " (error)" : ""), "");
      out.push(String(m.content || "").trim() || "_(no text)_");
      (m.images || []).forEach(function (src, k) {
        out.push("", "![image " + (k + 1) + "](" + src + ")");
      });
      out.push("");
    });
    return out.join("\n").replace(/\n{3,}/g, "\n\n").trim() + "\n";
  }

  /*
   * The message list sent to /api/chat: only user/assistant turns, no failed
   * exchanges (an error reply and the user turn it answered are dropped,
   * unless that user turn is the final one being retried), capped at `max`.
   */
  function buildRequestMessages(conv, max) {
    max = max || 40;
    var out = [];
    var list = conv || [];
    for (var i = 0; i < list.length; i++) {
      var m = list[i];
      if (!m || (m.role !== "user" && m.role !== "assistant")) continue;
      if (m.role === "assistant" && (m.error || m.pending)) continue;
      var next = list[i + 1];
      if (m.role === "user" && next && next.role === "assistant" && next.error) continue;
      var content = String(m.content || "").trim();
      if (!content) continue;
      // collapse consecutive user turns (e.g. after a stopped reply)
      if (out.length && out[out.length - 1].role === m.role && m.role === "user") {
        out[out.length - 1] = { role: "user", content: content };
        continue;
      }
      out.push({ role: m.role, content: content });
    }
    if (out.length > max) out = out.slice(out.length - max);
    while (out.length && out[0].role !== "user") out.shift();
    return out;
  }

  /* Root-mean-square of a time-domain buffer (Float32 -1..1 or Uint8 0..255). */
  function rms(buf) {
    if (!buf || !buf.length) return 0;
    var sum = 0;
    var isByte = typeof Uint8Array !== "undefined" && buf instanceof Uint8Array;
    for (var i = 0; i < buf.length; i++) {
      var v = isByte ? (buf[i] - 128) / 128 : buf[i];
      sum += v * v;
    }
    return Math.sqrt(sum / buf.length);
  }

  return {
    clamp: clamp,
    parseNum: parseNum,
    DEFAULT_SETTINGS: DEFAULT_SETTINGS,
    normalizeSettings: normalizeSettings,
    splitSentences: splitSentences,
    extractCompleteSentences: extractCompleteSentences,
    createSentenceStream: createSentenceStream,
    parseSSEChunk: parseSSEChunk,
    createSSEParser: createSSEParser,
    escapeHtml: escapeHtml,
    renderMarkdown: renderMarkdown,
    cleanForDisplay: cleanForDisplay,
    stripMarkdown: stripMarkdown,
    normalizeForSpeech: normalizeForSpeech,
    matchWakeWord: matchWakeWord,
    isStopCommand: isStopCommand,
    decideFinal: decideFinal,
    recognitionErrorPolicy: recognitionErrorPolicy,
    restartPlan: restartPlan,
    normalizeSpeech: normalizeSpeech,
    formatChatAsMarkdown: formatChatAsMarkdown,
    buildRequestMessages: buildRequestMessages,
    rms: rms,
  };
});
