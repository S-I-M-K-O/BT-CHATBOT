/*
 * HERMES pure-logic helpers (no DOM). Shared by the browser (window.HermesLib)
 * and the node tests (require("../../static/lib.js")).
 *
 *   clamp, parseNum, normalizeSettings, DEFAULT_SETTINGS, settingsEqual,
 *   coerceSettingValue, isGoDuration, isValidTimeZone
 *   splitSentences, extractCompleteSentences, createSentenceStream
 *   parseSSEChunk, createSSEParser
 *   escapeHtml, renderMarkdown, cleanForDisplay, stripMarkdown
 *   matchWakeWord, isStopCommand, normalizeSpeech, decideFinal,
 *   recognitionErrorPolicy, restartPlan, normalizeForSpeech
 *   formatChatAsMarkdown, buildRequestMessages, rms
 *   registrableDomain, linkMarker, previousSpeaker, collapsePlan
 *   resolveUiLang, SETTINGS_CATEGORIES, settingsCategoryFor,
 *   normalizeSearchText, settingsSearchMatch, normalizeConfigInfo,
 *   managedSet, isManaged, keepManaged
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
    cloudSpeech: true, // false: on-device speech recognition only
    // companion features (mirror of settings.go, SPEC 2.4)
    timezone: "",
    homeCity: "",
    numCtx: 8192,
    routerModel: "",
    extractModel: "",
    embedModel: "embeddinggemma",
    routerEnabled: true,
    keepAlive: "30m",
    fillerSpeech: false,
    personalLock: false,
    keepHistoryOnSpeakerChange: false,
    voiceIdEnabled: true,
    voiceIdThreshold: 0.6,
    voiceIdMargin: 0.05,
    voiceIdMinSec: 1.0,
    voiceIdAdapt: true,
    memoryEnabled: true,
    memoryAuto: true,
    memoryAutoHealth: false,
    guestSeesHousehold: false,
    briefingOnGreeting: true,
    briefingNews: false,
    calendarOthers: "third",
    haLanguage: "",
    homeControl: "known", // guests may not switch devices (mirror of settings.go)
    homeQueries: "known",
    checkinMeetings: true,
    checkinMeetingLead: 10,
    checkinSitting: false,
    checkinSittingMin: 60,
    quietStart: "22:00",
    quietEnd: "07:00",
    // interface language: "auto" follows the voice (speech keeps following it)
    uiLang: "auto",
  };

  var EYE_DESIGNS = ["classic", "cyclops", "iris", "cortana"];
  var VOICE_FX = ["none", "titan", "radio"];
  var CALENDAR_OTHERS = ["third", "refuse"];
  var HOME_CONTROL = ["everyone", "known", "off"];
  var HOME_QUERIES = ["known", "everyone"];
  var UI_LANGS = ["auto", "de", "en"];
  // keys the server drops (credentials moved to secrets.json)
  var DROPPED_KEYS = { haUrl: true, haToken: true };
  var MAX_SETTING_STR = 200;

  function bool(v, d) {
    return typeof v === "boolean" ? v : d;
  }

  /* Mirror of the server's cleanText: valid text, no control chars, trimmed, capped. */
  function cleanText(v, maxRunes) {
    if (typeof v !== "string") return "";
    var s = v.replace(/[\u0000-\u0008\u000b-\u001f\u007f]/g, "").trim();
    var runes = Array.from(s);
    if (runes.length > maxRunes) s = runes.slice(0, maxRunes).join("");
    return s;
  }

  /* Integer setting: 0 / missing => default, then clamp. */
  function intSetting(v, d, lo, hi) {
    var n = Math.round(parseNum(v, d));
    if (n === 0) n = d;
    return clamp(n, lo, hi);
  }

  /* Float setting where 0 and junk mean "not set". */
  function floatOrDefault(v, d, lo, hi) {
    var n = parseNum(v, d);
    if (n === 0) n = d;
    return clamp(n, lo, hi);
  }

  function oneOf(v, list, d) {
    return list.indexOf(v) >= 0 ? v : d;
  }

  /* Go time.ParseDuration syntax ("30m", "1h30m", "-1.5h"); "-1" is Ollama's "forever". */
  var GO_DURATION = /^[-+]?(?:(?:\d+(?:\.\d*)?|\.\d+)(?:ns|us|\u00b5s|\u03bcs|ms|s|m|h))+$/;
  function isGoDuration(s) {
    return typeof s === "string" && (s === "0" || GO_DURATION.test(s));
  }

  /* An IANA time zone this runtime knows ("Local" is not a zone). */
  function isValidTimeZone(tz) {
    if (typeof tz !== "string" || !tz || tz === "Local" || tz.length > 64) return false;
    try {
      new Intl.DateTimeFormat("en-US", { timeZone: tz });
      return true;
    } catch (e) {
      return false;
    }
  }

  var CLOCK = /^([01]\d|2[0-3]):[0-5]\d$/;
  var HA_LANGUAGE = /^[a-z]{2,3}(-[A-Za-z]{2,4})?$/;

  /*
   * Mirror of the server-side normalize(): clamps, defaults, migrations.
   * Keys this build does not know are passed through unchanged, so saving
   * from this page never drops settings of newer modules.
   */
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
    out.cloudSpeech = bool(s.cloudSpeech, d.cloudSpeech);

    // locale / time
    var tz = cleanText(s.timezone, 64);
    out.timezone = isValidTimeZone(tz) ? tz : "";
    out.homeCity = cleanText(s.homeCity, 100);
    // local models
    out.numCtx = intSetting(s.numCtx, d.numCtx, 2048, 32768);
    out.routerModel = cleanText(s.routerModel, MAX_SETTING_STR);
    out.extractModel = cleanText(s.extractModel, MAX_SETTING_STR);
    // "" is a valid embed model (lexical search only); missing means default
    out.embedModel = typeof s.embedModel === "string" ? cleanText(s.embedModel, MAX_SETTING_STR) : d.embedModel;
    out.routerEnabled = bool(s.routerEnabled, d.routerEnabled);
    var ka = typeof s.keepAlive === "string" ? s.keepAlive.trim() : d.keepAlive;
    out.keepAlive = ka === "" || ka === "-1" || (ka.length <= 16 && isGoDuration(ka)) ? ka : d.keepAlive;
    out.fillerSpeech = bool(s.fillerSpeech, d.fillerSpeech);
    // privacy
    out.personalLock = bool(s.personalLock, d.personalLock);
    out.keepHistoryOnSpeakerChange = bool(s.keepHistoryOnSpeakerChange, d.keepHistoryOnSpeakerChange);
    // voice ID
    out.voiceIdEnabled = bool(s.voiceIdEnabled, d.voiceIdEnabled);
    out.voiceIdThreshold = floatOrDefault(s.voiceIdThreshold, d.voiceIdThreshold, 0.45, 0.85);
    out.voiceIdMargin = parseNum(s.voiceIdMargin, d.voiceIdMargin, 0, 0.3); // 0 is valid
    out.voiceIdMinSec = floatOrDefault(s.voiceIdMinSec, d.voiceIdMinSec, 0.5, 4);
    out.voiceIdAdapt = bool(s.voiceIdAdapt, d.voiceIdAdapt);
    // memory
    out.memoryEnabled = bool(s.memoryEnabled, d.memoryEnabled);
    out.memoryAuto = bool(s.memoryAuto, d.memoryAuto);
    out.memoryAutoHealth = bool(s.memoryAutoHealth, d.memoryAutoHealth);
    out.guestSeesHousehold = bool(s.guestSeesHousehold, d.guestSeesHousehold);
    // briefing, calendar, smart home
    out.briefingOnGreeting = bool(s.briefingOnGreeting, d.briefingOnGreeting);
    out.briefingNews = bool(s.briefingNews, d.briefingNews);
    out.calendarOthers = oneOf(s.calendarOthers, CALENDAR_OTHERS, d.calendarOthers);
    var hl = typeof s.haLanguage === "string" ? s.haLanguage.trim() : "";
    out.haLanguage = HA_LANGUAGE.test(hl) ? hl : "";
    out.homeControl = oneOf(s.homeControl, HOME_CONTROL, d.homeControl);
    out.homeQueries = oneOf(s.homeQueries, HOME_QUERIES, d.homeQueries);
    // check-ins
    out.checkinMeetings = bool(s.checkinMeetings, d.checkinMeetings);
    out.checkinMeetingLead = intSetting(s.checkinMeetingLead, d.checkinMeetingLead, 1, 60);
    out.checkinSitting = bool(s.checkinSitting, d.checkinSitting);
    out.checkinSittingMin = intSetting(s.checkinSittingMin, d.checkinSittingMin, 20, 240);
    out.quietStart = typeof s.quietStart === "string" && CLOCK.test(s.quietStart) ? s.quietStart : d.quietStart;
    out.quietEnd = typeof s.quietEnd === "string" && CLOCK.test(s.quietEnd) ? s.quietEnd : d.quietEnd;
    out.uiLang = oneOf(s.uiLang, UI_LANGS, d.uiLang);

    // unknown keys (newer modules) pass through untouched
    Object.keys(s).forEach(function (k) {
      if (Object.prototype.hasOwnProperty.call(d, k) || DROPPED_KEYS[k]) return;
      if (k === "__proto__" || k === "constructor" || k === "prototype") return;
      out[k] = s[k];
    });
    return out;
  }

  /* Deep-enough equality for settings objects; key order does not matter. */
  function settingsEqual(a, b) {
    a = a || {};
    b = b || {};
    var ka = Object.keys(a).sort();
    var kb = Object.keys(b).sort();
    if (ka.length !== kb.length) return false;
    for (var i = 0; i < ka.length; i++) {
      if (ka[i] !== kb[i]) return false;
      var x = a[ka[i]];
      var y = b[kb[i]];
      if (x === y) continue;
      if (x && y && typeof x === "object" && typeof y === "object") {
        if (JSON.stringify(x) !== JSON.stringify(y)) return false;
        continue;
      }
      return false;
    }
    return true;
  }

  /*
   * Value of one data-setting form control. kind: "checkbox" | "number" |
   * "range" | anything else (string). `current` is the fallback for junk.
   */
  function coerceSettingValue(kind, value, checked, current) {
    if (kind === "checkbox") return !!checked;
    if (kind === "number" || kind === "range") return parseNum(value, current);
    return value == null ? "" : String(value);
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
   *
   * Like renderMarkdown, ``` only opens / closes a fence at the start of a
   * line; a mid-line ```x``` is an inline code span whose text is kept. The
   * output must not depend on how the reply was chunked, so whenever the
   * buffer cannot decide yet (partial marker, open inline span, closer line
   * without its newline) it is held back until more input arrives.
   */
  function createSentenceStream(opts) {
    var raw = ""; // unprocessed input (may hold a partial fence / tag)
    var prose = ""; // prose waiting for a sentence boundary
    var inCode = false;
    var inThink = false;
    var bol = true; // the prose emitted so far ends at the start of a line
    var codeBol = false; // inside a fence: raw[0] is at the start of a line

    function emit(text) {
      if (!text) return;
      prose += text;
      var nl = text.lastIndexOf("\n");
      bol = nl >= 0 ? /^[ \t]*$/.test(text.slice(nl + 1)) : bol && /^[ \t]*$/.test(text);
    }

    // Index of the first ``` that starts a line in s (only spaces / tabs
    // before it since the last "\n", or since s[0] when atBol), else -1.
    function lineFence(s, atBol) {
      var re = /(^|\n)[ \t]*```/g;
      var m;
      while ((m = re.exec(s))) {
        if (m[1] || atBol) return m.index + m[0].length - 3;
        re.lastIndex = m.index + 1;
      }
      return -1;
    }

    function drain(final) {
      for (;;) {
        if (inCode) {
          var close = lineFence(raw, codeBol);
          if (close < 0) {
            if (final) {
              raw = "";
              return;
            }
            // drop finished code lines; keep a partial line only while it
            // could still become the closer
            var nl = raw.lastIndexOf("\n");
            if (nl >= 0) {
              raw = raw.slice(nl + 1);
              codeBol = true;
            }
            if (!codeBol || !/^[ \t]*`{0,2}$/.test(raw)) {
              raw = "";
              codeBol = false;
            }
            return;
          }
          var eol = raw.indexOf("\n", close + 3);
          if (eol < 0 && !final) {
            // the rest of the closer line (e.g. "```  ") is not here yet
            raw = raw.slice(close);
            codeBol = true;
            return;
          }
          var closerRest = eol < 0 ? raw.slice(close + 3) : raw.slice(close + 3, eol);
          raw = eol < 0 ? "" : raw.slice(eol + 1);
          if (/\S/.test(closerRest)) {
            // "``` more" is a code line, not a closer (as in renderMarkdown)
            codeBol = true;
            continue;
          }
          inCode = false;
          emit("\n");
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
          emit(raw.slice(0, raw.length - hold));
          raw = raw.slice(raw.length - hold);
          return;
        }
        emit(raw.slice(0, first));
        raw = raw.slice(first);
        if (first === think) {
          inThink = true;
          raw = raw.slice(7);
          emit("\n");
          continue;
        }
        if (bol) {
          // a real fence: the rest of this line is its info string
          inCode = true;
          codeBol = false;
          raw = raw.slice(3);
          emit("\n");
          continue;
        }
        // inline ```code``` span: closes on the same line or is literal text
        var shut = raw.indexOf("```", 3);
        var br = raw.indexOf("\n", 3);
        if (shut >= 0 && (br < 0 || shut < br)) {
          emit(raw.slice(3, shut));
          raw = raw.slice(shut + 3);
          continue;
        }
        if (br < 0 && !final) return; // the span may still close
        emit("```");
        raw = raw.slice(3);
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
        bol = true;
        codeBol = false;
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
   * inside words survive (OLLAMA_BASE, snake_case, C#).
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
    // url is already HTML-escaped text: fully undo escapeHtml (&amp; last) so
    // the check sees the real URL; one with < > " ' is rejected, never
    // linked to a double-escaped target
    var plain = url
      .replace(/&lt;/g, "<")
      .replace(/&gt;/g, ">")
      .replace(/&quot;/g, '"')
      .replace(/&#39;/g, "'")
      .replace(/&amp;/g, "&");
    if (!/^https?:\/\/[^\s<>"']+$/i.test(plain)) return null;
    return escapeHtml(plain);
  }

  // linkHost is the host of an already-escaped http(s) URL, without
  // credentials, "www." or the port.
  function linkHost(href) {
    var m = /^https?:\/\/([^\/?#]+)/i.exec(href);
    if (!m) return "";
    return m[1].replace(/^.*@/, "").replace(/:\d+$/, "").replace(/^www\./i, "");
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
    // a held slot can itself contain placeholders (a `code` span inside a
    // link label), so the label is expanded before the link is held
    var phRe = new RegExp(PH_OPEN + "(\\d+)" + PH_CLOSE, "g");
    function expand(str) {
      return str.replace(phRe, function (_, k) {
        return slots[+k];
      });
    }
    s = escapeHtml(s);
    // [label](url)
    // (a held `code` placeholder ends the target: it must never reach href)
    s = s.replace(/\[([^\]\n]+)\]\(([^()\s\uE000]+)\)/g, function (all, label, url) {
      var href = safeUrl(url);
      if (!href) return label;
      // The model picks the label, so the target host is always shown next
      // to it: a link planted by injected text ("[mehr](https://evil/?d=…)")
      // must not pass as harmless (SPEC 8).
      var host = linkHost(href);
      var shown = host && label.toLowerCase().indexOf(host.toLowerCase()) < 0 ? ' <span class="link-host">(' + host + ")</span>" : "";
      return hold('<a href="' + href + '" target="_blank" rel="noopener noreferrer" title="' + href + '">' + expand(label) + "</a>" + shown);
    });
    // bare URLs; they end before an escaped < > " ' (never split its entity)
    s = s.replace(/(^|[\s(])(https?:\/\/(?:(?!&(?:lt|gt|quot|#39);)[^\s<>()])*(?!&(?:lt|gt|quot|#39);)[^\s<>().,!?;:'"])/g, function (all, pre, url) {
      var href = safeUrl(url);
      if (!href) return all;
      return pre + hold('<a href="' + href + '" target="_blank" rel="noopener noreferrer">' + url + "</a>");
    });
    s = s.replace(/(\*\*|__)(?=\S)([\s\S]*?\S)\1/g, "<strong>$2</strong>");
    s = s.replace(/(^|[^*\w])\*(?=\S)([^*\n]*?\S)\*(?!\*)/g, "$1<em>$2</em>");
    s = s.replace(/(^|[^_\w])_(?=\S)([^_\n]*?\S)_(?![_\w])/g, "$1<em>$2</em>");
    s = s.replace(/~~(?=\S)([^~\n]*?\S)~~/g, "<del>$1</del>");
    return expand(s);
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
    var list = null; // {type, items, start}
    var quote = [];

    function flushPara() {
      if (para.length) html.push("<p>" + para.map(renderInline).join("<br>") + "</p>");
      para = [];
    }
    function flushList() {
      if (list) {
        var open = list.type === "ol" && list.start !== 1 ? '<ol start="' + list.start + '">' : "<" + list.type + ">";
        html.push(open + list.items.map(function (it) {
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
      // a closing # run needs a space before it ("## Use C#" keeps its #)
      var h = /^\s{0,3}(#{1,6})\s+(.*?)(?:\s+#+)?\s*$/.exec(line);
      if (h) {
        flushAll();
        var lvl = Math.min(6, h[1].length + 2);
        html.push("<h" + lvl + ">" + renderInline(h[2]) + "</h" + lvl + ">");
        continue;
      }
      var ul = /^\s*[-*+•]\s+(.*)$/.exec(line);
      var ol = /^\s*(\d{1,3})[.)]\s+(.*)$/.exec(line);
      if (ul || ol) {
        flushPara();
        flushQuote();
        var type = ul ? "ul" : "ol";
        if (list && list.type !== type) flushList();
        // keep the item's own number: a blank line or code block between
        // steps splits the list, and "2." must not restart at 1
        if (!list) list = { type: type, items: [], start: ol ? parseInt(ol[1], 10) : 1 };
        list.items.push(ul ? ul[1] : ol[2]);
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

  // Common speech-recognition renderings of "Hermes" (EN + DE recognisers)
  // that are not ordinary words: they count anywhere in an utterance.
  var WAKE_WORDS = "hermes|hermess|hermez|hermis|hermus|hermos|hermies|herms|hermas|hermesh|ermes|hermy|harmes|harmess|airmes";
  var WAKE_RE = new RegExp("(^|[^a-z0-9])(" + WAKE_WORDS + ")(?=$|[^a-z0-9])");

  // Filler that may surround the wake word without being a request.
  var WAKE_FILLER = /^(?:[\s,.:;!?\-–—]*(?:hey there|hey|hi|hello|hallo|ok|okay|yo|oh|so|well|na|also|ja|please|bitte)(?=$|[^a-z0-9]))*[\s,.:;!?\-–—]*/;

  /*
   * Mis-hearings that are also everyday English ("clean up her mess", "the
   * hermit's shell"). In always-on call mode a match anywhere would send
   * background talk to the model, so these only count as an address: at the
   * very start of the utterance (after filler like "hey"), and not when the
   * next word makes them a plain noun phrase ("her mess is everywhere"). A
   * comma or other break after them ("her mess, is it raining?") still
   * counts. "her mass" is not a plausible rendering and is not listed.
   */
  var WAKE_LOOSE_RE = new RegExp(WAKE_FILLER.source + "(hermit['’]s|her mess|her miss|her mes|hair mess|air mess)(?=$|[^a-z0-9])");
  var WAKE_LOOSE_NOUN = /^\s+(?:is|isn't|was|wasn't|are|were|has|had|and|or|of|in|on|at|to|with|from|that|which|ist|war|und)(?=$|[^a-z0-9])/;
  var WAKE_LEAD_RE = new RegExp(WAKE_FILLER.source + "(?:" + WAKE_WORDS + ")(?=$|[^a-z0-9])");

  /* Length of a wake word (plus filler) at the start of folded text, else 0. */
  function leadingWake(folded) {
    var l = WAKE_LOOSE_RE.exec(folded);
    if (l && !WAKE_LOOSE_NOUN.test(folded.slice(l[0].length))) return l[0].length;
    var s = WAKE_LEAD_RE.exec(folded);
    return s ? s[0].length : 0;
  }

  /* The wake word in folded text: { start, end } or null. */
  function findWake(folded) {
    var l = WAKE_LOOSE_RE.exec(folded);
    if (l) {
      var end = l[0].length;
      if (!WAKE_LOOSE_NOUN.test(folded.slice(end))) return { start: end - l[1].length, end: end };
    }
    var m = WAKE_RE.exec(folded);
    if (!m) return null;
    var start = m.index + m[1].length;
    return { start: start, end: start + m[2].length };
  }

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
    var m = findWake(folded);
    if (!m) return { woke: false, command: "", index: -1, leading: false, trailing: false, bare: false };
    var start = m.start;
    var afterRaw = original.slice(m.end).replace(/^['’]s\b/, "");
    // a repeated wake word ("Hermes. Hermes.") is not the command
    for (var r; (r = leadingWake(foldChars(afterRaw))); ) afterRaw = afterRaw.slice(r).replace(/^['’]s\b/, "");
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
    // only a leading or trailing wake word addresses HERMES; a mid-sentence
    // mention ("I told Hermes about it") is talk about it, not a request
    if (w.woke && (w.leading || w.trailing)) return w.command ? { action: "send", command: w.command } : { action: "ack", command: "" };
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
      // private reminder text is never persisted, and unfinished turns are not
      // part of the conversation yet
      if (m.ephemeral || m.pending || m.preparing) return;
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

  // second-level labels under a two-letter country TLD that act as public
  // suffixes ("bbc.co.uk", "abc.net.au", "example.or.at")
  var SECOND_LEVEL = { co: true, com: true, net: true, org: true, gov: true, ac: true, edu: true, or: true, ne: true, go: true, gv: true, ltd: true, plc: true };

  /* Registrable domain of a URL or host ("www.heise.de" -> "heise.de"). */
  function registrableDomain(urlOrHost) {
    var host = String(urlOrHost || "").trim();
    var m = /^[a-z][a-z0-9+.-]*:\/\/([^/?#]*)/i.exec(host);
    if (m) host = m[1];
    host = host.replace(/^[^@]*@/, ""); // userinfo
    if (/^\[/.test(host)) return host.replace(/\]:\d+$/, "]").toLowerCase(); // IPv6 literal
    host = host.replace(/:\d+$/, "").replace(/\.$/, "").toLowerCase();
    if (!host) return "";
    if (/^\d{1,3}(\.\d{1,3}){3}$/.test(host)) return host;
    var labels = host.split(".").filter(Boolean);
    if (labels.length <= 2) return labels.join(".");
    var tld = labels[labels.length - 1];
    var sld = labels[labels.length - 2];
    var take = tld.length === 2 && SECOND_LEVEL[sld] ? 3 : 2;
    return labels.slice(-take).join(".");
  }

  /* The fixed history marker for a link message; never contains the title. */
  function linkMarker(url) {
    var d = registrableDomain(url);
    return d ? "[Link: " + d + "]" : "[Link]";
  }

  var LINK_MARKER_RE = /^\[Link(?:: [^\]\s]+)?\]$/;

  /*
   * The message list sent to /api/chat: only user/assistant turns, no
   * notices (local announcements), no failed exchanges (an error reply and
   * the user turn it answered are dropped, unless that user turn is the final
   * one being retried), capped at `max`. Each message keeps the tags the
   * server handed out (src, sp, att); a link message is always sent as its
   * fixed "[Link: domain]" marker.
   */
  function buildRequestMessages(conv, max) {
    max = max || 40;
    var out = [];
    var list = (conv || []).filter(function (m) {
      return m && !m.notice && !m.preparing;
    });
    for (var i = 0; i < list.length; i++) {
      var m = list[i];
      if (m.role !== "user" && m.role !== "assistant") continue;
      if (m.role === "assistant" && (m.error || m.pending)) continue;
      var next = list[i + 1];
      if (m.role === "user" && next && next.role === "assistant" && next.error) continue;
      var content = String(m.content || "").trim();
      if (m.role === "user" && m.att) {
        if (!LINK_MARKER_RE.test(content)) content = linkMarker(m.attUrl || "");
      }
      if (!content) continue;
      var msg = { role: m.role, content: content };
      if (typeof m.src === "string" && m.src) msg.src = m.src;
      if (typeof m.sp === "string") msg.sp = m.sp;
      if (m.role === "user" && m.att) msg.att = true;
      // collapse consecutive user turns (e.g. after a stopped reply)
      if (out.length && out[out.length - 1].role === m.role && m.role === "user") {
        out[out.length - 1] = msg;
        continue;
      }
      out.push(msg);
    }
    if (out.length > max) out = out.slice(out.length - max);
    while (out.length && out[0].role !== "user") out.shift();
    return out;
  }

  /*
   * Speaker id of the latest tagged user message before index `before`
   * (all of conv when omitted); undefined when there is none.
   */
  function previousSpeaker(conv, before) {
    var list = conv || [];
    var i = before == null ? list.length - 1 : before - 1;
    for (; i >= 0; i--) {
      var m = list[i];
      if (m && m.role === "user" && typeof m.sp === "string") return m.sp;
    }
    return undefined;
  }

  /*
   * How to show the conversation after speaker changes (SPEC 4.10, "history
   * per speaker"). Messages tagged with another speaker than the current one
   * (the latest tagged user message) are folded into "N earlier messages"
   * rows, one per consecutive run. Untagged messages (older history, local
   * notices) always stay visible. opts.keep: show everything.
   * Returns [{kind:"msg", index}] and [{kind:"collapsed", key, indices}] items.
   */
  /*
   * Chat cards kept with a HERMES message (app.js stores them so cards.js can
   * redraw them after a reload). keepCard returns a plain {kind, data} copy,
   * or null for an unknown kind, non-object data or oversize data. The text
   * of private reminders is never stored (the card hides it anyway).
   */
  var CARD_KINDS = ["briefing", "calendar", "home", "reminder", "timer", "list", "docs", "link", "memory", "weather", "news"];
  var CARDS_PER_MESSAGE = 4;
  var CARD_DATA_MAX = 16000; // bytes of JSON per card

  function keepCard(card) {
    if (!card || typeof card !== "object" || CARD_KINDS.indexOf(card.kind) < 0) return null;
    var d = card.data;
    if (!d || typeof d !== "object" || Array.isArray(d)) return null;
    var json;
    try {
      json = JSON.stringify(d);
    } catch (e) {
      return null;
    }
    if (!json || json.length > CARD_DATA_MAX) return null;
    var data = JSON.parse(json);
    var hide = function (r) {
      if (r && typeof r === "object" && r.private === true) {
        delete r.text;
        delete r.ruleText;
      }
    };
    hide(data);
    if (Array.isArray(data.reminders)) data.reminders.forEach(hide);
    return { kind: card.kind, data: data };
  }

  function collapsePlan(conv, opts) {
    opts = opts || {};
    var list = conv || [];
    var current = previousSpeaker(list);
    var plan = [];
    var run = null;
    for (var i = 0; i < list.length; i++) {
      var m = list[i];
      var foreign = !opts.keep && current !== undefined && m && typeof m.sp === "string" && m.sp !== current && !m.pending;
      if (foreign) {
        if (!run) {
          run = { kind: "collapsed", key: String(m.id || i), indices: [] };
          plan.push(run);
        }
        run.indices.push(i);
      } else {
        run = null;
        plan.push({ kind: "msg", index: i });
      }
    }
    return plan;
  }

  /*
   * Does exporting the chat need the PIN first? With the personal lock on,
   * turns of other speakers folded away (and not opened after an unlock)
   * must not leave through an export either (SPEC 4.10).
   * opts: {lock, keep, expanded: {runKey: true}}
   */
  function exportNeedsUnlock(conv, opts) {
    opts = opts || {};
    if (!opts.lock) return false;
    var expanded = opts.expanded || {};
    return collapsePlan(conv, { keep: !!opts.keep }).some(function (item) {
      return item.kind === "collapsed" && !Object.prototype.hasOwnProperty.call(expanded, item.key);
    });
  }

  // ------------------------------------------------------------ settings window

  /* The interface language: "de" / "en" pick it, anything else follows the voice. */
  /*
   * Voices offered for an interface language: with uiLang "de"/"en" only the
   * voices of that language (Piper "de", browser "de-DE"...); "auto" lists all.
   */
  function voiceLangMatches(lang, uiLang) {
    if (uiLang !== "de" && uiLang !== "en") return true;
    return String(lang || "").toLowerCase().split(/[-_]/)[0] === uiLang;
  }
  function voicesForUiLang(voices, uiLang) {
    return (Array.isArray(voices) ? voices : []).filter(function (v) {
      return v && voiceLangMatches(v.lang, uiLang);
    });
  }
  /*
   * The voice to use after the interface language changed: the current one
   * when it matches, else the first voice of that language, else "" (none).
   */
  function pickVoiceForUiLang(voices, current, uiLang, keyField) {
    var k = keyField || "key";
    var list = voicesForUiLang(voices, uiLang);
    for (var i = 0; i < list.length; i++) if (list[i][k] === current) return current;
    return list.length ? list[0][k] : "";
  }

  /*
   * Rough language of one sentence: "de", "en", or "" when unsure. Used to
   * keep a voice from reading the other language with a heavy accent (a
   * German sentence that still turns up with an English voice, e.g. a quoted
   * memory). Umlauts/ß count double; otherwise frequent function words.
   */
  var DE_WORDS = wordList("der die das und ist nicht ich du er sie es wir ihr ein eine einen einem einer mit für auf zu den dem des im ins vom zum zur von bei nach aus wie was wer wo noch auch schon heute morgen gestern uhr bitte ja nein gibt habe hast hat bin bist sind seid wird werden kann kannst muss soll will dein deine mein meine kein keine aber oder wenn dann dass weil sehr gern gerne jetzt hier dort");
  var EN_WORDS = wordList("the and is not i you he she it we they a an with for on to of in at by from how what who where still also already today tomorrow yesterday please yes no there have has am are was were will would can could must should your my his her their this that these those but or if then because very now here");
  function wordList(s) {
    var o = {};
    s.split(" ").forEach(function (w) {
      o[w] = true;
    });
    return o;
  }
  function detectLang(text) {
    var t = String(text == null ? "" : text).toLowerCase();
    var de = (t.match(/[äöüß]/g) || []).length * 2;
    var en = 0;
    var words = t.match(/[a-zäöüß']+/g) || [];
    words.forEach(function (w) {
      if (DE_WORDS[w] === true) de++;
      if (EN_WORDS[w] === true) en++;
    });
    if (de >= 2 && de >= en + 2) return "de";
    if (en >= 2 && en >= de + 2) return "en";
    return "";
  }

  function resolveUiLang(uiLang, voiceLang) {
    if (uiLang === "de" || uiLang === "en") return uiLang;
    return voiceLang === "de" ? "de" : "en";
  }

  /*
   * Categories of the settings window, in sidebar order. `sections` are the
   * ids of sections registered through Hermes.registerSettingsSection that
   * belong in that category; "more" collects every unknown id.
   */
  var SETTINGS_CATEGORIES = [
    { id: "general", title: { de: "Allgemein", en: "General" }, sections: [] },
    { id: "voice", title: { de: "Stimme & Sprache", en: "Voice & speech" }, sections: [] },
    { id: "people", title: { de: "Personen & Stimmerkennung", en: "People & voice ID" }, sections: ["people", "voiceid"] },
    { id: "memory", title: { de: "Gedächtnis", en: "Memory" }, sections: ["memory"] },
    { id: "reminders", title: { de: "Erinnerungen & Check-ins", en: "Reminders & check-ins" }, sections: ["reminders", "checkins"] },
    { id: "calendar", title: { de: "Kalender", en: "Calendar" }, sections: ["calendar"] },
    { id: "home", title: { de: "Smart Home", en: "Smart Home" }, sections: ["home"] },
    { id: "docs", title: { de: "Dokumente", en: "Documents" }, sections: ["docs", "links"] },
    { id: "location", title: { de: "Ort & Zeit", en: "Location & time" }, sections: ["location"] },
    { id: "privacy", title: { de: "Datenschutz", en: "Privacy" }, sections: ["privacy"] },
    { id: "models", title: { de: "Modelle", en: "Models" }, sections: ["models"] },
    { id: "appearance", title: { de: "Darstellung", en: "Appearance" }, sections: [] },
    { id: "config", title: { de: "Konfigurationsdatei", en: "Config file" }, sections: [] },
    { id: "more", title: { de: "Weitere", en: "More" }, sections: [] },
  ];

  /* Category id for a registered settings section id ("more" when unknown). */
  function settingsCategoryFor(sectionId) {
    for (var i = 0; i < SETTINGS_CATEGORIES.length; i++) {
      if (SETTINGS_CATEGORIES[i].sections.indexOf(sectionId) >= 0) return SETTINGS_CATEGORIES[i].id;
    }
    return "more";
  }

  /* Lower case, no accents, ß -> ss, single spaces: "Größe" ~ "grosse". */
  function normalizeSearchText(s) {
    s = String(s == null ? "" : s).toLowerCase();
    if (typeof s.normalize === "function") s = s.normalize("NFD").replace(/[̀-ͯ]/g, "");
    return s.replace(/ß/g, "ss").replace(/\s+/g, " ").trim();
  }

  /* Every word of `query` occurs in `text` (an empty query matches everything). */
  function settingsSearchMatch(query, text) {
    var q = normalizeSearchText(query);
    if (!q) return true;
    var hay = normalizeSearchText(text);
    var words = q.split(" ");
    for (var i = 0; i < words.length; i++) if (hay.indexOf(words[i]) < 0) return false;
    return true;
  }

  var SETTING_KEY = /^[A-Za-z][A-Za-z0-9_]{0,63}$/;

  /*
   * GET /api/config, made safe for the page: {path, exists, managed[],
   * errors[{line,key,msg}], mtime}. Junk and missing fields become defaults.
   */
  function normalizeConfigInfo(j) {
    var ok = !!(j && typeof j === "object" && !Array.isArray(j));
    var out = { available: ok, path: "", exists: false, managed: [], errors: [], mtime: "" };
    if (!ok) return out;
    out.path = typeof j.path === "string" ? j.path.slice(0, 512) : "";
    out.exists = j.exists === true;
    out.mtime = typeof j.mtime === "string" ? j.mtime.slice(0, 64) : "";
    if (Array.isArray(j.managed)) {
      j.managed.forEach(function (k) {
        if (typeof k === "string" && SETTING_KEY.test(k) && out.managed.indexOf(k) < 0) out.managed.push(k);
      });
    }
    if (Array.isArray(j.errors)) {
      j.errors.slice(0, 50).forEach(function (e) {
        if (!e || typeof e !== "object") return;
        var line = Math.round(parseNum(e.line, 0));
        out.errors.push({
          line: line > 0 ? line : 0,
          key: typeof e.key === "string" ? e.key.slice(0, 64) : "",
          msg: typeof e.msg === "string" ? e.msg.slice(0, 300) : "",
          code: typeof e.code === "string" && /^[a-z_]{1,32}$/.test(e.code) ? e.code : "",
        });
      });
    }
    return out;
  }

  /* ["a","b"] -> {a:true,b:true} (own keys only, no prototype tricks). */
  function managedSet(list) {
    var out = Object.create(null);
    (Array.isArray(list) ? list : []).forEach(function (k) {
      if (typeof k === "string" && SETTING_KEY.test(k)) out[k] = true;
    });
    return out;
  }

  function isManaged(set, key) {
    return !!(set && typeof key === "string" && set[key] === true);
  }

  /*
   * A copy of `draft` where every managed key keeps the value from `base`
   * (the server's settings, i.e. the value hermes.conf set): Reset and the
   * form never change what the config file decides.
   */
  function keepManaged(draft, base, managed) {
    var out = Object.assign({}, draft || {});
    var set = Array.isArray(managed) ? managedSet(managed) : managed || {};
    Object.keys(set).forEach(function (k) {
      if (set[k] === true && base && Object.prototype.hasOwnProperty.call(base, k)) out[k] = base[k];
    });
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
    registrableDomain: registrableDomain,
    linkMarker: linkMarker,
    previousSpeaker: previousSpeaker,
    collapsePlan: collapsePlan,
    exportNeedsUnlock: exportNeedsUnlock,
    keepCard: keepCard,
    CARDS_PER_MESSAGE: CARDS_PER_MESSAGE,
    settingsEqual: settingsEqual,
    coerceSettingValue: coerceSettingValue,
    isGoDuration: isGoDuration,
    isValidTimeZone: isValidTimeZone,
    rms: rms,
    resolveUiLang: resolveUiLang,
    detectLang: detectLang,
    voiceLangMatches: voiceLangMatches,
    voicesForUiLang: voicesForUiLang,
    pickVoiceForUiLang: pickVoiceForUiLang,
    SETTINGS_CATEGORIES: SETTINGS_CATEGORIES,
    settingsCategoryFor: settingsCategoryFor,
    normalizeSearchText: normalizeSearchText,
    settingsSearchMatch: settingsSearchMatch,
    normalizeConfigInfo: normalizeConfigInfo,
    managedSet: managedSet,
    isManaged: isManaged,
    keepManaged: keepManaged,
  };
});
