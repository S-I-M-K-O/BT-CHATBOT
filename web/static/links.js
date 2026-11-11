/*
 * HERMES "read a link" (W2-L, SPEC 3.10, 5.5 "links.js", 9 Feature 7).
 *
 * Part 1 (UMD, no DOM): pure helpers for the browser (window.HermesLinks)
 * and the node tests (require("../../static/links.js")):
 *
 *   extractUrl, isLinkFollowUp, isPaywall, normalizeArticleText,
 *   truncateRunes, siteFromUrl, articleLang, lastLinkUrl, errorKey,
 *   upstreamStatus, base64ToBytes, STRINGS, TEXT_MAX
 *
 * Part 2 (browser only, needs window.Hermes): a `beforeSend` hook. A typed
 * message with an http(s) URL, or a typed or spoken follow-up such as "lies
 * den Link vor" / "read me this article" (re-reads the conversation's last
 * link), is fetched through POST /api/link/fetch (public addresses only,
 * checked by the server). The HTML is parsed into an INERT DOMParser
 * document (no scripts run, nothing loads) and Readability.js extracts the
 * article; PDFs go through pdf.js (via HermesDocs); without either the
 * server's fallbackText is used. The text travels to /api/chat as the
 * `attachment`, where the local model summarises it without tools. The page
 * HTML is never inserted into the live DOM, and the page title is never
 * stored as message content (app.js stores the "[Link: domain]" marker).
 */
(function (root, factory) {
  var api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root && typeof root === "object") root.HermesLinks = api;
})(typeof window !== "undefined" ? window : typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  var TEXT_MAX = 12000; // runes, the /api/chat attachment cap
  var TITLE_MAX = 300;
  var URL_MAX = 2000;
  var INSTRUCTION_MAX = 500;
  var PAYWALL_MIN_CHARS = 700;
  var PAYWALL_RE = /(abo\b|abonn|subscribe|subscription|paywall|plus-artikel|anmelden um weiterzulesen|jetzt weiterlesen mit|registrieren um weiterzulesen|sign in to continue|log in to continue)/i;

  var STRINGS = {
    en: {
      reading: "🔗 Reading the article …",
      readingSay: "Reading the article.",
      noLastLink: "There is no link in this conversation yet. Paste one first.",
      err_url: "That does not look like a valid web address.",
      err_blocked: "I don't open addresses in the local network or on this computer.",
      err_type: "That link is neither a web page nor a PDF.",
      err_too_large: "That page is too large to read.",
      err_status: "The website refused the request (HTTP {status}). Some sites block automated readers.",
      err_status_plain: "The website refused the request.",
      err_timeout: "The website took too long to answer.",
      err_charset: "That page uses a character set I can't read.",
      err_busy: "I'm already reading two links. Try again in a moment.",
      err_redirects: "That link redirects too often.",
      err_network: "The server could not be reached.",
      err_pdf: "I could not read that PDF.",
      err_generic: "I could not load that page.",
    },
    de: {
      reading: "🔗 Ich lese den Artikel …",
      readingSay: "Ich lese den Artikel.",
      noLastLink: "In diesem Gespräch gibt es noch keinen Link. Füge zuerst einen ein.",
      err_url: "Das sieht nicht nach einer gültigen Webadresse aus.",
      err_blocked: "Adressen im lokalen Netz oder auf diesem Rechner öffne ich nicht.",
      err_type: "Der Link ist weder eine Webseite noch ein PDF.",
      err_too_large: "Die Seite ist zu groß zum Lesen.",
      err_status: "Die Website hat die Anfrage abgelehnt (HTTP {status}). Manche Seiten sperren automatische Leser.",
      err_status_plain: "Die Website hat die Anfrage abgelehnt.",
      err_timeout: "Die Website hat zu lange gebraucht.",
      err_charset: "Die Seite verwendet einen Zeichensatz, den ich nicht lesen kann.",
      err_busy: "Ich lese gerade schon zwei Links. Versuch es gleich noch einmal.",
      err_redirects: "Der Link leitet zu oft weiter.",
      err_network: "Der Server ist nicht erreichbar.",
      err_pdf: "Das PDF konnte ich nicht lesen.",
      err_generic: "Die Seite konnte ich nicht laden.",
    },
  };

  // ---------------------------------------------------------------- text

  /* Cut s to at most n code points (not UTF-16 units). */
  function truncateRunes(s, n) {
    s = String(s == null ? "" : s);
    if (s.length <= n) return s;
    var out = "";
    var count = 0;
    for (var ch of s) {
      if (count >= n) break;
      out += ch;
      count++;
    }
    return out;
  }

  /*
   * Normalise article text: unify line breaks, collapse runs of spaces,
   * keep paragraph breaks (at most one blank line), drop control
   * characters, cap at TEXT_MAX code points.
   */
  function normalizeArticleText(s, max) {
    s = String(s == null ? "" : s)
      .replace(/\r\n?/g, "\n")
      .replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F]/g, " ")
      .replace(/[ \t  -​  　]+/g, " ");
    var lines = s.split("\n").map(function (l) {
      return l.trim();
    });
    s = lines.join("\n").replace(/\n{3,}/g, "\n\n").trim();
    return truncateRunes(s, max || TEXT_MAX);
  }

  function oneLine(s, max) {
    return truncateRunes(
      String(s == null ? "" : s)
        .replace(/[\u0000-\u001F\u007F]/g, " ")
        .replace(/\s+/g, " ")
        .trim(),
      max
    );
  }

  // ---------------------------------------------------------------- URLs

  var URL_RE = /\bhttps?:\/\/[^\s<>"'`]+/i;
  var TRAILING_RE = /[.,;:!?…»«"'”“’)\]}>]+$/;

  /*
   * The first http(s) URL in a typed message and the rest of the text as
   * an optional instruction ("fasse kurz zusammen"). Trailing punctuation
   * is not part of the URL, except a closing bracket that has its opening
   * one inside the URL (Wikipedia). Returns null when there is none, it is
   * longer than 2000 characters or it does not parse.
   */
  function extractUrl(text) {
    text = String(text == null ? "" : text);
    var m = URL_RE.exec(text);
    if (!m) return null;
    var raw = m[0];
    var url = raw;
    for (;;) {
      var t = TRAILING_RE.exec(url);
      if (!t) break;
      var strip = t[0];
      // keep a ")" that closes a "(" inside the URL
      var keep = "";
      while (strip.length && /[)\]]/.test(strip[0])) {
        var open = strip[0] === ")" ? "(" : "[";
        var close = strip[0];
        var body = url.slice(0, url.length - strip.length) + keep;
        if (count(body, open) > count(body, close)) {
          keep += strip[0];
          strip = strip.slice(1);
        } else break;
      }
      url = url.slice(0, url.length - t[0].length) + keep;
      if (!strip.length) break;
    }
    if (!url || url.length > URL_MAX) return null;
    var parsed = parseUrl(url);
    if (!parsed) return null;
    var rest = (text.slice(0, m.index) + " " + text.slice(m.index + url.length)).replace(/\s+/g, " ").trim();
    // tidy what the URL left behind: "Lies das: <url>." → "Lies das"
    rest = rest
      .replace(/\s+([,.;:!?])/g, "$1")
      .replace(/^[\s:;,.–—\-„“”"'»«]+|[\s:;,.–—\-„“”"'»«]+$/g, "")
      .trim();
    return { url: parsed, instruction: truncateRunes(rest, INSTRUCTION_MAX) };
  }

  function count(s, ch) {
    var n = 0;
    for (var i = 0; i < s.length; i++) if (s[i] === ch) n++;
    return n;
  }

  /* An absolute http(s) URL with a host and no credentials, or "". */
  function parseUrl(s) {
    try {
      var u = new URL(s);
      if ((u.protocol !== "http:" && u.protocol !== "https:") || !u.hostname || u.username || u.password) return "";
      u.hash = "";
      var out = u.toString();
      return out.length <= URL_MAX ? out : "";
    } catch (e) {
      return "";
    }
  }

  /*
   * A follow-up about the last link without a URL: "lies den Link vor",
   * "fass den Artikel zusammen", "summarise the link", "read me this
   * article" (SPEC 9.7). Typed and spoken.
   */
  function isLinkFollowUp(text) {
    var s = String(text == null ? "" : text).toLowerCase();
    if (!s || s.length > 200 || URL_RE.test(s)) return false;
    return /\b(link|links|artikel|article|artikels)\b/.test(s) && /(zusammen|vorlesen|vorles|\blies\b|\blese\b|summar|\bread\b|recap|\bgist\b)/.test(s);
  }

  /*
   * The URL of the last link message of a conversation (app.js keeps it as
   * m.attUrl on the user message; SPEC called it m.url).
   */
  function lastLinkUrl(conv) {
    if (!Array.isArray(conv)) return "";
    for (var i = conv.length - 1; i >= 0; i--) {
      var m = conv[i];
      if (!m || m.role !== "user" || !m.att) continue;
      var u = parseUrl(m.attUrl || m.url || "");
      if (u) return u;
    }
    return "";
  }

  /* The host name without "www." for the card ("heise.de"). */
  function siteFromUrl(url) {
    try {
      return new URL(url).hostname.replace(/^www\./i, "").toLowerCase();
    } catch (e) {
      return "";
    }
  }

  /* "de" / "en" from a document language such as "de-DE", else "". */
  function articleLang(l) {
    var m = /^\s*(de|en)\b/i.exec(String(l || ""));
    return m ? m[1].toLowerCase() : "";
  }

  // ---------------------------------------------------------------- heuristics

  /*
   * Paywall heuristic (SPEC 5.5): little text, or text/title mentioning a
   * subscription. Only marks the summary ("probably just the teaser").
   */
  function isPaywall(text, title) {
    text = String(text || "");
    if (text.replace(/\s+/g, " ").trim().length < PAYWALL_MIN_CHARS) return true;
    // only the start and end of a long article: a word in the middle of an
    // article about subscriptions is not a paywall
    var probe = text.length > 1600 ? text.slice(0, 600) + " " + text.slice(-800) : text;
    return PAYWALL_RE.test(probe) || PAYWALL_RE.test(String(title || ""));
  }

  // ---------------------------------------------------------------- errors

  /* The STRINGS key for a fetch error {status, code, message}. */
  function errorKey(err) {
    if (!err) return "err_generic";
    var code = err.code || "";
    var known = { url: 1, blocked: 1, type: 1, too_large: 1, status: 1, timeout: 1, charset: 1, busy: 1, redirects: 1, pdf: 1 };
    if (known[code]) return "err_" + code;
    if (err.status === 429) return "err_busy";
    if (err.status === 413) return "err_too_large";
    if (err.status === 504) return "err_timeout";
    if (err.name === "AbortError") return "err_timeout";
    if (!err.status) return "err_network";
    return "err_generic";
  }

  /* The upstream HTTP status in "the site answered HTTP 403", or 0. */
  function upstreamStatus(err) {
    if (!err) return 0;
    if (typeof err.upstream === "number") return err.upstream;
    var m = /HTTP (\d{3})/.exec(String(err.message || ""));
    return m ? Number(m[1]) : 0;
  }

  /* Decode base64 (the server's "pdf" field) to bytes. */
  function base64ToBytes(b64) {
    b64 = String(b64 || "");
    if (typeof atob === "function") {
      var bin = atob(b64);
      var out = new Uint8Array(bin.length);
      for (var i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
      return out;
    }
    /* global Buffer */
    return new Uint8Array(Buffer.from(b64, "base64"));
  }

  function makeTr(lang) {
    return function (key, vars) {
      var l = typeof lang === "function" ? lang() : lang;
      var table = STRINGS[l] || STRINGS.en;
      var s = Object.prototype.hasOwnProperty.call(table, key) ? table[key] : STRINGS.en[key] || key;
      return String(s).replace(/\{(\w+)\}/g, function (all, k) {
        return vars && vars[k] != null ? String(vars[k]) : all;
      });
    };
  }

  return {
    TEXT_MAX: TEXT_MAX,
    TITLE_MAX: TITLE_MAX,
    URL_MAX: URL_MAX,
    STRINGS: STRINGS,
    truncateRunes: truncateRunes,
    normalizeArticleText: normalizeArticleText,
    oneLine: oneLine,
    extractUrl: extractUrl,
    parseUrl: parseUrl,
    isLinkFollowUp: isLinkFollowUp,
    lastLinkUrl: lastLinkUrl,
    siteFromUrl: siteFromUrl,
    articleLang: articleLang,
    isPaywall: isPaywall,
    errorKey: errorKey,
    upstreamStatus: upstreamStatus,
    base64ToBytes: base64ToBytes,
    makeTr: makeTr,
  };
});

// ===================================================================== browser part
(function () {
  "use strict";
  if (typeof window === "undefined" || typeof document === "undefined") return;
  var H = window.Hermes;
  var K = window.HermesLinks;
  if (!H || !K || typeof H.hook !== "function") return;

  var READABILITY_SRC = "/vendor/readability-0.6.0/Readability.js";
  var CONV_KEY = "hermes-conversation"; // app.js storage (read only)
  // The beforeSend hook has 20 s in app.js; we finish (or give up) before,
  // so a slow site never lets the raw message through as normal chat.
  var BUDGET_MS = 18000;
  var FETCH_MS = 16000;

  function api() {
    return H.api || {};
  }
  var tr = typeof H.strings === "function" ? H.strings(K.STRINGS) : K.makeTr(function () {
    return H.lang ? H.lang() : "en";
  });

  function settings() {
    try {
      return (api().settings && api().settings()) || {};
    } catch (e) {
      return {};
    }
  }
  function linkFeature() {
    try {
      var st = api().status && api().status();
      return (st && st.features && st.features.link) || null;
    } catch (e) {
      return null;
    }
  }

  // ---------------------------------------------------------------- extractors

  var readabilityPromise = null;
  /* Load Readability.js once, lazily; resolves the constructor or null. */
  function loadReadability() {
    if (typeof window.Readability === "function") return Promise.resolve(window.Readability);
    var f = linkFeature();
    if (f && f.readability === false) return Promise.resolve(null);
    if (!readabilityPromise) {
      readabilityPromise = new Promise(function (resolve) {
        var s = document.createElement("script");
        s.src = READABILITY_SRC;
        s.async = true;
        s.onload = function () {
          resolve(typeof window.Readability === "function" ? window.Readability : null);
        };
        s.onerror = function () {
          readabilityPromise = null; // e.g. assets fetched later: try again next time
          if (s.parentNode) s.parentNode.removeChild(s);
          resolve(null);
        };
        document.head.appendChild(s);
      });
    }
    return readabilityPromise;
  }

  /*
   * Readability on an inert document: DOMParser documents have no browsing
   * context, so scripts never run and images, styles and frames never load.
   * Nothing from it is ever attached to the page.
   */
  function readArticle(Readability, html, finalUrl) {
    var doc = new DOMParser().parseFromString(String(html || ""), "text/html");
    // No <base href> for the article's address: only the text is used, and
    // the page's CSP (base-uri 'self') logged an error for it on every read.
    var docLang = (doc.documentElement && doc.documentElement.getAttribute("lang")) || "";
    var article = new Readability(doc, { charThreshold: 300 }).parse();
    if (!article) return null;
    return {
      title: article.title || "",
      text: article.textContent || "",
      site: article.siteName || "",
      lang: article.lang || docLang,
    };
  }

  function withTimeout(p, ms) {
    return new Promise(function (resolve, reject) {
      var timer = setTimeout(function () {
        var e = new Error("timeout");
        e.code = "timeout";
        reject(e);
      }, ms);
      p.then(
        function (v) {
          clearTimeout(timer);
          resolve(v);
        },
        function (e) {
          clearTimeout(timer);
          reject(e);
        }
      );
    });
  }

  /*
   * Fetch and extract one link. Resolves the attachment for /api/chat or
   * rejects with an Error carrying .code / .status like fetchJSON.
   */
  function readLink(url, instruction) {
    var started = Date.now();
    var ctrl = typeof AbortController === "function" ? new AbortController() : null;
    var timer = ctrl
      ? setTimeout(function () {
          ctrl.abort();
        }, FETCH_MS)
      : null;
    return api()
      .fetchJSON("/api/link/fetch", { method: "POST", body: { url: url }, signal: ctrl ? ctrl.signal : undefined })
      .then(
        function (res) {
          if (timer) clearTimeout(timer);
          return res || {};
        },
        function (err) {
          if (timer) clearTimeout(timer);
          throw err;
        }
      )
      .then(function (res) {
        var finalUrl = K.parseUrl(res.finalUrl) || url;
        var left = Math.max(1000, BUDGET_MS - (Date.now() - started));
        var extracted;
        if (res.pdf) {
          var D = window.HermesDocs;
          if (!D || typeof D.pdfText !== "function") {
            var e = new Error("pdf.js missing");
            e.code = "pdf";
            return Promise.reject(e);
          }
          extracted = withTimeout(D.pdfText(K.base64ToBytes(res.pdf)), left).then(
            function (text) {
              return { title: res.title || "", text: text, site: "", lang: "" };
            },
            function (err) {
              if (err && err.code === "timeout") throw err;
              var e = new Error("pdf");
              e.code = "pdf";
              throw e;
            }
          );
        } else {
          extracted = loadReadability().then(function (Readability) {
            var art = null;
            if (Readability) {
              try {
                art = readArticle(Readability, res.html, finalUrl);
              } catch (e) {
                art = null; // a page that trips Readability: use the fallback
              }
            }
            var text = art ? K.normalizeArticleText(art.text) : "";
            if (text.length < 200 && res.fallbackText && String(res.fallbackText).length > text.length) {
              return { title: (art && art.title) || res.title || "", text: res.fallbackText, site: (art && art.site) || "", lang: (art && art.lang) || "" };
            }
            return {
              title: (art && art.title) || res.title || "",
              text: text,
              site: (art && art.site) || "",
              lang: (art && art.lang) || "",
            };
          });
        }
        return extracted.then(function (a) {
          var text = K.normalizeArticleText(a.text);
          var title = K.oneLine(a.title || res.title || K.siteFromUrl(finalUrl), K.TITLE_MAX);
          var att = {
            kind: "link",
            url: url,
            finalUrl: finalUrl,
            title: title,
            site: K.oneLine(a.site || K.siteFromUrl(finalUrl), 200),
            text: text,
            paywall: K.isPaywall(text, title),
            lang: K.articleLang(a.lang),
          };
          // the instruction reaches runReadLink as attachment.instruction
          if (instruction) att.instruction = instruction;
          return att;
        });
      });
  }

  function errorMessage(err) {
    var key = K.errorKey(err);
    if (key === "err_status") {
      var st = K.upstreamStatus(err);
      return st ? tr("err_status", { status: st }) : tr("err_status_plain");
    }
    return tr(key);
  }

  // ---------------------------------------------------------------- hook

  function conversation() {
    try {
      var raw = window.localStorage.getItem(CONV_KEY);
      return raw ? JSON.parse(raw) : [];
    } catch (e) {
      return null;
    }
  }
  var lastUrlInTab = ""; // only used when localStorage is unavailable

  function announce() {
    if (api().toast) api().toast(tr("reading"), { key: "link-reading", ms: 4000 });
    if (settings().speak && api().say) {
      try {
        var p = api().say(tr("readingSay"));
        if (p && p.catch) p.catch(function () {});
      } catch (e) {
        /* speaking is a courtesy only */
      }
    }
  }

  function beforeSend(text, opts) {
    opts = opts || {};
    var url = "";
    var instruction = "";
    // URLs only from typed text (a recogniser never produces a safe URL)
    var found = opts.spoken ? null : K.extractUrl(text);
    if (found) {
      url = found.url;
      instruction = found.instruction;
    } else if (K.isLinkFollowUp(text)) {
      var conv = conversation();
      url = conv ? K.lastLinkUrl(conv) : lastUrlInTab;
      if (!url) return null; // no link yet: an ordinary question
      instruction = K.oneLine(text, 500);
    } else {
      return null;
    }
    if (!api().fetchJSON) return null;
    announce();
    return withTimeout(readLink(url, instruction), BUDGET_MS).then(
      function (att) {
        lastUrlInTab = att.finalUrl || url;
        return { attachment: att, display: "🔗 " + (att.title || K.siteFromUrl(att.finalUrl) || url) };
      },
      function (err) {
        return { cancel: true, message: errorMessage(err) };
      }
    );
  }

  H.hook("beforeSend", beforeSend);

  // for the docs module and tests in the browser console
  K.readLink = readLink;
})();
