/*
 * HERMES own documents (W2-L, SPEC 3.9, 5.5 "docs.js", 9 Feature 6).
 *
 * Part 1 (UMD, no DOM): pure helpers for the browser (window.HermesDocs)
 * and the node tests (require("../../static/docs.js")):
 *
 *   fileKind, checkFile, utf8Length, cleanExtractedText, pageText,
 *   joinPages, ownerLabel, ownerOptions, statusKey, errorKey, sortDocs,
 *   filesFrom, formatCount, STRINGS, LIMITS
 *
 * Part 2 (browser only, needs window.Hermes): the hub tab "Dokumente" /
 * "Documents" (upload by file picker or drag and drop, owner choice, list
 * with status, delete, search, folder rescan), the Settings → Documents
 * section (id "docs"), uploads of files dropped on the chat area (the
 * "files:drop" bus event app.js emits), and pdfText() for links.js.
 *
 * The text is extracted HERE, in the browser: .txt/.md with File.text(),
 * PDF with pdf.js 4.10.38 (self-hosted under /vendor, loaded lazily with a
 * dynamic import, isEvalSupported:false). Only the text goes to the server.
 * Document names and texts are user data: only ever set with textContent.
 */
(function (root, factory) {
  var api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root && typeof root === "object") root.HermesDocs = api;
})(typeof window !== "undefined" ? window : typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  var LIMITS = {
    textBytes: 2 * 1024 * 1024, // server: ≤2 MB text per document
    textFile: 2 * 1024 * 1024, // a .txt/.md file is its text
    pdfFile: 40 * 1024 * 1024, // the PDF itself (text is usually far smaller)
    pdfPages: 2000,
    files: 20, // per drop / pick
  };

  var STRINGS = {
    en: {
      tab: "Documents",
      section: "Documents",
      intro:
        "Add your own notes, contracts or manuals. HERMES answers questions about them with the local model only – e.g. “what does my lease say about pets?”. Nothing is sent to Mistral.",
      upload: "Add files …",
      dropHere: "Drop .pdf, .md or .txt files here",
      dropChat: "Files dropped on the chat are added to the household documents.",
      owner: "Belongs to",
      household: "Household (everyone known)",
      unknownPerson: "Unknown person",
      search: "Search",
      searchPlaceholder: "Search in documents …",
      searchNone: "Nothing found.",
      empty: "No documents yet.",
      loading: "…",
      chars: "{n} characters",
      chunks: "{n} parts",
      folderDoc: "from folder",
      delete: "Delete",
      deleteTitle: "Delete “{name}”?",
      deleteBody: "The document and its search index are deleted from HERMES. The original file is not touched.",
      deleteFolderBody: "It comes back with the next folder scan unless you remove it from the folder.",
      cancel: "Cancel",
      deleted: "Deleted: {name}",
      added: "Added: {name}",
      reading: "Reading {name} …",
      st_indexing: "indexing …",
      st_ready: "ready",
      st_lexical: "keyword search",
      st_error: "error",
      lexicalHint:
        "No embedding model: documents are searched by keywords only. For better results run “ollama pull embeddinggemma”.",
      pdfMissing: "PDF support is not installed. Run “sh scripts/fetch-assets.sh”, then reload. .md and .txt files still work.",
      folder: "Watched folder: {name}",
      rescan: "Scan folder now",
      rescanned: "Folder scanned: {added} new, {updated} updated, {removed} removed.",
      locked: "Documents are locked on this device. New files can still be added to the household.",
      unlock: "Unlock",
      readOnly: "The documents index was written by a newer HERMES. Changes are blocked.",
      openTab: "Open documents",
      sectionCount: "{n} documents stored.",
      sectionCount1: "1 document stored.",
      sectionNone: "No documents stored yet.",
      embedOk: "Search: semantic (embedding model {model}).",
      embedOff: "Search: keywords only.",
      askHint: "Ask, for example: “Was steht in meinem Mietvertrag über Kündigung?” or “search my notes for the wifi password”.",
      err_type: "{name}: only .pdf, .md and .txt files are supported.",
      err_size: "{name} is too large.",
      err_empty: "{name}: no text layer found (scanned PDF?) – OCR is not supported.",
      err_too_large: "{name}: the text is larger than 2 MB.",
      err_too_many_chunks: "{name} is too long to index.",
      err_limit: "The document limit is reached. Delete documents first.",
      err_locked: "Locked. Please enter the PIN.",
      err_read_only: "The documents index was written by a newer HERMES. Changes are blocked.",
      err_owner: "That person no longer exists.",
      err_pdf: "{name}: the PDF could not be read.",
      err_password: "{name} is password-protected.",
      err_pdf_missing: "{name}: PDF support is not installed (sh scripts/fetch-assets.sh).",
      err_network: "The server could not be reached.",
      err_not_found: "That document no longer exists.",
      err_no_folder: "No folder is configured (HERMES_DOCS_DIR).",
      err_generic: "Something went wrong: {msg}",
    },
    de: {
      tab: "Dokumente",
      section: "Dokumente",
      intro:
        "Füge eigene Notizen, Verträge oder Anleitungen hinzu. HERMES beantwortet Fragen dazu nur mit dem lokalen Modell – z. B. „Was steht in meinem Mietvertrag über Kündigung?“. Nichts geht an Mistral.",
      upload: "Dateien hinzufügen …",
      dropHere: ".pdf-, .md- oder .txt-Dateien hier ablegen",
      dropChat: "Auf den Chat gezogene Dateien kommen zu den Haushaltsdokumenten.",
      owner: "Gehört zu",
      household: "Haushalt (alle Bekannten)",
      unknownPerson: "Unbekannte Person",
      search: "Suchen",
      searchPlaceholder: "In Dokumenten suchen …",
      searchNone: "Nichts gefunden.",
      empty: "Noch keine Dokumente.",
      loading: "…",
      chars: "{n} Zeichen",
      chunks: "{n} Abschnitte",
      folderDoc: "aus Ordner",
      delete: "Löschen",
      deleteTitle: "„{name}“ löschen?",
      deleteBody: "Das Dokument und sein Suchindex werden aus HERMES gelöscht. Die Originaldatei bleibt unberührt.",
      deleteFolderBody: "Beim nächsten Ordner-Scan kommt es wieder, außer du entfernst es aus dem Ordner.",
      cancel: "Abbrechen",
      deleted: "Gelöscht: {name}",
      added: "Hinzugefügt: {name}",
      reading: "Lese {name} …",
      st_indexing: "wird indexiert …",
      st_ready: "bereit",
      st_lexical: "Stichwortsuche",
      st_error: "Fehler",
      lexicalHint:
        "Kein Embedding-Modell: Dokumente werden nur nach Stichwörtern durchsucht. Bessere Ergebnisse mit „ollama pull embeddinggemma“.",
      pdfMissing: "PDF-Unterstützung fehlt. Führe „sh scripts/fetch-assets.sh“ aus und lade neu. .md und .txt funktionieren trotzdem.",
      folder: "Überwachter Ordner: {name}",
      rescan: "Ordner jetzt scannen",
      rescanned: "Ordner gescannt: {added} neu, {updated} aktualisiert, {removed} entfernt.",
      locked: "Dokumente sind auf diesem Gerät gesperrt. Neue Dateien können trotzdem zum Haushalt hinzugefügt werden.",
      unlock: "Entsperren",
      readOnly: "Der Dokumentindex stammt von einem neueren HERMES. Änderungen sind gesperrt.",
      openTab: "Dokumente öffnen",
      sectionCount: "{n} Dokumente gespeichert.",
      sectionCount1: "1 Dokument gespeichert.",
      sectionNone: "Noch keine Dokumente gespeichert.",
      embedOk: "Suche: nach Bedeutung (Embedding-Modell {model}).",
      embedOff: "Suche: nur Stichwörter.",
      askHint: "Frag zum Beispiel: „Was steht in meinem Mietvertrag über Kündigung?“ oder „search my notes for the wifi password“.",
      err_type: "{name}: Nur .pdf-, .md- und .txt-Dateien werden unterstützt.",
      err_size: "{name} ist zu groß.",
      err_empty: "{name}: Keine Textebene gefunden (gescannte PDF?) – OCR wird nicht unterstützt.",
      err_too_large: "{name}: Der Text ist größer als 2 MB.",
      err_too_many_chunks: "{name} ist zu lang zum Indexieren.",
      err_limit: "Die Höchstzahl an Dokumenten ist erreicht. Lösche zuerst Dokumente.",
      err_locked: "Gesperrt. Bitte gib die PIN ein.",
      err_read_only: "Der Dokumentindex stammt von einem neueren HERMES. Änderungen sind gesperrt.",
      err_owner: "Diese Person gibt es nicht mehr.",
      err_pdf: "{name}: Das PDF konnte nicht gelesen werden.",
      err_password: "{name} ist passwortgeschützt.",
      err_pdf_missing: "{name}: PDF-Unterstützung fehlt (sh scripts/fetch-assets.sh).",
      err_network: "Der Server ist nicht erreichbar.",
      err_not_found: "Dieses Dokument gibt es nicht mehr.",
      err_no_folder: "Es ist kein Ordner eingerichtet (HERMES_DOCS_DIR).",
      err_generic: "Etwas ist schiefgegangen: {msg}",
    },
  };

  var TEXT_EXT = /\.(md|markdown|txt|text)$/i;
  var PDF_EXT = /\.pdf$/i;

  /* "pdf" | "text" | "" for a file name and MIME type. */
  function fileKind(name, type) {
    name = String(name || "");
    type = String(type || "").toLowerCase();
    if (PDF_EXT.test(name) || type === "application/pdf") return "pdf";
    if (TEXT_EXT.test(name) || type === "text/plain" || type === "text/markdown" || type === "text/x-markdown") return "text";
    return "";
  }

  /* The MIME type the server stores. */
  function mimeFor(kind, name) {
    if (kind === "pdf") return "application/pdf";
    return /\.(md|markdown)$/i.test(String(name || "")) ? "text/markdown" : "text/plain";
  }

  /*
   * Can this file be added? {ok, kind, error} with error "type", "size" or
   * "pdf_missing" (pdf.js not installed).
   */
  function checkFile(file, opts) {
    opts = opts || {};
    if (!file) return { ok: false, kind: "", error: "type" };
    var kind = fileKind(file.name, file.type);
    if (!kind) return { ok: false, kind: "", error: "type" };
    var size = Number(file.size) || 0;
    if (kind === "pdf") {
      if (opts.pdfjs === false) return { ok: false, kind: kind, error: "pdf_missing" };
      if (size > LIMITS.pdfFile) return { ok: false, kind: kind, error: "size" };
    } else if (size > LIMITS.textFile) {
      return { ok: false, kind: kind, error: "size" };
    }
    return { ok: true, kind: kind, error: "" };
  }

  /* UTF-8 byte length of a string (the server's 2 MB cap is in bytes). */
  function utf8Length(s) {
    s = String(s == null ? "" : s);
    var n = 0;
    for (var i = 0; i < s.length; i++) {
      var c = s.charCodeAt(i);
      if (c < 0x80) n += 1;
      else if (c < 0x800) n += 2;
      else if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) {
        n += 4;
        i++;
      } else n += 3;
    }
    return n;
  }

  /* Drop a BOM and NUL bytes, unify line breaks. */
  function cleanExtractedText(s) {
    return String(s == null ? "" : s)
      .replace(/^﻿/, "")
      .replace(/\r\n?/g, "\n")
      .replace(/\u0000/g, "");
  }

  /*
   * The text of one PDF page from pdf.js getTextContent() items: items are
   * joined, hasEOL ends a line, a vertical jump starts a new line.
   */
  function pageText(items) {
    var out = "";
    var lastY = null;
    (items || []).forEach(function (it) {
      if (!it || typeof it.str !== "string") return;
      var y = it.transform && it.transform.length > 5 ? it.transform[5] : null;
      if (lastY !== null && y !== null && Math.abs(y - lastY) > 2 && out && !/\n$/.test(out)) out += "\n";
      out += it.str;
      if (it.hasEOL) out += "\n";
      if (y !== null) lastY = y;
    });
    return out.replace(/[ \t]+\n/g, "\n").trim();
  }

  /* Pages joined with a blank line (SPEC 5.5). */
  function joinPages(pages) {
    return (pages || [])
      .map(function (p) {
        return String(p || "").trim();
      })
      .filter(Boolean)
      .join("\n\n");
  }

  function ownerLabel(owner, people, tr) {
    if (!owner) return tr("household");
    var list = people || [];
    for (var i = 0; i < list.length; i++) if (list[i] && list[i].id === owner) return list[i].name || tr("unknownPerson");
    return tr("unknownPerson");
  }

  /* [{value, label}] for the owner select: household first, then people. */
  function ownerOptions(people, tr) {
    var out = [{ value: "", label: tr("household") }];
    (people || []).forEach(function (p) {
      if (p && typeof p.id === "string" && /^p_/.test(p.id) && p.name && p.confirmed !== false) out.push({ value: p.id, label: p.name });
    });
    return out;
  }

  function statusKey(status) {
    return { indexing: "st_indexing", ready: "st_ready", lexical: "st_lexical", error: "st_error" }[status] || "st_indexing";
  }

  /* The STRINGS key for an API error {status, code}. */
  function errorKey(err) {
    if (!err) return "err_generic";
    var code = err.code || "";
    var known = {
      empty: 1, too_large: 1, too_many_chunks: 1, limit: 1, locked: 1, read_only: 1, owner: 1, pdf: 1,
      password: 1, pdf_missing: 1, type: 1, size: 1, no_folder: 1, not_found: 1,
    };
    if (known[code]) return "err_" + code;
    if (err.status === 413) return "err_too_large";
    if (err.status === 404) return "err_not_found";
    if (err.status === 503 && /read-?only|newer/i.test(String(err.message || ""))) return "err_read_only";
    if (!err.status) return "err_network";
    return "err_generic";
  }

  /* Newest first, then by name. */
  function sortDocs(docs) {
    return (docs || []).slice().sort(function (a, b) {
      var ta = Date.parse(a.createdAt || "") || 0;
      var tb = Date.parse(b.createdAt || "") || 0;
      if (ta !== tb) return tb - ta;
      return String(a.name || "").localeCompare(String(b.name || ""));
    });
  }

  /* An array of File-like objects from a FileList, an array or one file. */
  function filesFrom(x) {
    if (!x) return [];
    if (Array.isArray(x)) return x.filter(Boolean);
    if (typeof x.length === "number") {
      var out = [];
      for (var i = 0; i < x.length; i++) if (x[i]) out.push(x[i]);
      return out;
    }
    if (x.files) return filesFrom(x.files);
    return typeof x.name === "string" ? [x] : [];
  }

  function formatCount(n, lang) {
    n = Number(n) || 0;
    try {
      return n.toLocaleString(lang === "de" ? "de-DE" : "en-US");
    } catch (e) {
      return String(n);
    }
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
    LIMITS: LIMITS,
    STRINGS: STRINGS,
    fileKind: fileKind,
    mimeFor: mimeFor,
    checkFile: checkFile,
    utf8Length: utf8Length,
    cleanExtractedText: cleanExtractedText,
    pageText: pageText,
    joinPages: joinPages,
    ownerLabel: ownerLabel,
    ownerOptions: ownerOptions,
    statusKey: statusKey,
    errorKey: errorKey,
    sortDocs: sortDocs,
    filesFrom: filesFrom,
    formatCount: formatCount,
    makeTr: makeTr,
  };
});

// ===================================================================== browser part
(function () {
  "use strict";
  if (typeof window === "undefined" || typeof document === "undefined") return;
  var H = window.Hermes;
  var D = window.HermesDocs;
  if (!D) return;

  var PDFJS_DIR = "/vendor/pdfjs-4.10.38/";

  // ---------------------------------------------------------------- pdf.js

  var pdfjsPromise = null;
  /* Load pdf.js once (dynamic import works from classic scripts). */
  function loadPdfjs() {
    if (!pdfjsPromise) {
      pdfjsPromise = import(PDFJS_DIR + "pdf.min.mjs").then(
        function (lib) {
          lib.GlobalWorkerOptions.workerSrc = PDFJS_DIR + "pdf.worker.min.mjs";
          return lib;
        },
        function (err) {
          pdfjsPromise = null; // assets may be fetched later
          var e = new Error("pdf.js missing");
          e.code = "pdf_missing";
          e.cause = err;
          throw e;
        }
      );
    }
    return pdfjsPromise;
  }

  /*
   * The text of a PDF (bytes: Uint8Array), pages joined with a blank line.
   * Stops after 2,000 pages or 2 MB of text. Rejects with .code "password"
   * or "pdf".
   */
  function pdfText(bytes) {
    return loadPdfjs().then(function (lib) {
      var task = lib.getDocument({ data: bytes, isEvalSupported: false, disableFontFace: true, stopAtErrors: false });
      return task.promise.then(
        function (pdf) {
          var pages = [];
          var total = 0;
          var n = Math.min(pdf.numPages || 0, D.LIMITS.pdfPages);
          function next(i) {
            if (i > n || total > D.LIMITS.textBytes) {
              return Promise.resolve(D.joinPages(pages));
            }
            return pdf
              .getPage(i)
              .then(function (page) {
                return page.getTextContent().then(function (content) {
                  var t = D.pageText(content && content.items);
                  pages.push(t);
                  total += t.length;
                  if (page.cleanup) page.cleanup();
                });
              })
              .then(function () {
                return next(i + 1);
              });
          }
          return next(1).then(
            function (text) {
              if (pdf.destroy) pdf.destroy();
              return D.cleanExtractedText(text);
            },
            function (err) {
              if (pdf.destroy) pdf.destroy();
              throw err;
            }
          );
        },
        function (err) {
          var e = new Error("pdf");
          e.code = err && err.name === "PasswordException" ? "password" : "pdf";
          throw e;
        }
      );
    });
  }
  D.pdfText = pdfText;

  if (!H || typeof H.registerTab !== "function") return;

  function api() {
    return H.api || {};
  }
  function lang() {
    try {
      return api().lang && api().lang() === "de" ? "de" : "en";
    } catch (e) {
      return "en";
    }
  }
  var tr = typeof H.strings === "function" ? H.strings(D.STRINGS) : D.makeTr(lang);

  function settings() {
    try {
      return (api().settings && api().settings()) || {};
    } catch (e) {
      return {};
    }
  }
  function peopleUI() {
    var P = window.HermesPeople;
    return (P && P.ui) || null;
  }
  function people() {
    var ui = peopleUI();
    try {
      return ui && ui.people ? ui.people() : [];
    } catch (e) {
      return [];
    }
  }
  function pdfjsAvailable() {
    if (state.pdfjs !== null) return state.pdfjs;
    try {
      var st = api().status && api().status();
      if (st && st.features && st.features.link && typeof st.features.link.pdfjs === "boolean") return st.features.link.pdfjs;
    } catch (e) {
      /* unknown: try */
    }
    return true;
  }

  // ---------------------------------------------------------------- DOM helpers

  function el(tag, attrs, kids) {
    var e = document.createElement(tag);
    if (attrs) {
      for (var k in attrs) {
        if (!Object.prototype.hasOwnProperty.call(attrs, k)) continue;
        var v = attrs[k];
        if (v == null || v === false) continue;
        if (k === "class") e.className = v;
        else if (k === "text") e.textContent = v;
        else if (k.slice(0, 2) === "on" && typeof v === "function") e.addEventListener(k.slice(2), v);
        else if (k === "hidden") e.hidden = !!v;
        else if (k === "disabled") e.disabled = !!v;
        else if (k === "value") e.value = v;
        else if (k === "selected") e.selected = !!v;
        else e.setAttribute(k, v === true ? "" : v);
      }
    }
    if (kids != null) {
      if (!Array.isArray(kids)) kids = [kids];
      for (var i = 0; i < kids.length; i++) {
        var c = kids[i];
        if (c == null || c === false) continue;
        e.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
      }
    }
    return e;
  }
  function clear(node) {
    while (node && node.firstChild) node.removeChild(node.firstChild);
  }

  // Static, trusted icon markup (never user data).
  var ICON_TAB =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/><path d="M9 13h6M9 17h6"/></svg>';

  function toast(msg, opts) {
    if (api().toast) return api().toast(msg, opts || {});
  }
  function errText(err, name) {
    return tr(D.errorKey(err), { name: name || "", msg: (err && err.message) || "?" });
  }

  // ---------------------------------------------------------------- state

  var state = {
    docs: [],
    loaded: false,
    locked: false,
    readOnly: false,
    error: "",
    embedOk: true,
    embedModel: "",
    folder: "",
    pdfjs: null,
    owner: "",
    query: "",
    hits: null,
    busy: 0,
    visible: false,
    loadSeq: 0,
    searchSeq: 0,
  };
  var view = null;
  var sectionView = null;

  function load() {
    if (!api().fetchJSON) return Promise.resolve();
    var seq = ++state.loadSeq;
    // a plain fetch: an open tab must not pop a PIN prompt on every refresh
    return api()
      .fetchJSON("/api/docs")
      .then(
        function (res) {
          if (seq !== state.loadSeq) return;
          res = res || {};
          state.docs = D.sortDocs(Array.isArray(res.docs) ? res.docs : []);
          state.embedOk = res.embedOk !== false;
          state.embedModel = typeof res.embedModel === "string" ? res.embedModel : "";
          state.folder = typeof res.folder === "string" ? res.folder : "";
          if (typeof res.pdfjs === "boolean") state.pdfjs = res.pdfjs;
          state.readOnly = !!res.readOnly;
          state.loaded = true;
          state.locked = false;
          state.error = "";
          render();
          if (sectionView) sectionView.render();
        },
        function (err) {
          if (seq !== state.loadSeq) return;
          state.loaded = true;
          state.locked = !!(err && err.code === "locked");
          state.error = state.locked ? "" : errText(err);
          if (state.locked) state.docs = [];
          render();
          if (sectionView) sectionView.render();
        }
      );
  }

  var reloadTimer = null;
  function scheduleReload() {
    if (reloadTimer) clearTimeout(reloadTimer);
    reloadTimer = setTimeout(function () {
      reloadTimer = null;
      if (state.visible || sectionView) load();
    }, 300);
  }

  /* fetchJSON with the shared PIN prompt + one retry on 403 locked (people.js). */
  function call(url, opts) {
    var ui = peopleUI();
    if (ui && ui.call) return ui.call(url, opts || {});
    return api().fetchJSON(url, opts || {});
  }

  function unlockAndLoad() {
    var ui = peopleUI();
    if (!ui || !ui.requestUnlock) return;
    ui.requestUnlock().then(function (ok) {
      if (ok) load();
    });
  }

  function confirmDialog(title, body, okLabel) {
    var ui = peopleUI();
    if (ui && ui.dialog) {
      return ui
        .dialog({
          cls: "pp-confirm docs-confirm",
          title: title,
          body: [el("p", { class: "pp-dlg-text", text: body })],
          buttons: [
            { label: tr("cancel"), value: false, kind: "ghost", focus: true },
            { label: okLabel, value: true, kind: "danger" },
          ],
        })
        .then(function (v) {
          return !!v;
        });
    }
    return Promise.resolve(window.confirm(title + "\n\n" + body));
  }

  // ---------------------------------------------------------------- uploads

  function readText(file, kind) {
    if (kind === "pdf") {
      return file.arrayBuffer().then(function (buf) {
        return pdfText(new Uint8Array(buf));
      });
    }
    if (typeof file.text === "function") return file.text().then(D.cleanExtractedText);
    return new Promise(function (resolve, reject) {
      var r = new FileReader();
      r.onload = function () {
        resolve(D.cleanExtractedText(r.result));
      };
      r.onerror = function () {
        reject(r.error || new Error("read"));
      };
      r.readAsText(file);
    });
  }

  /* Extract and upload one file; resolves true on success. */
  function uploadOne(file, owner) {
    var name = String(file.name || "Dokument").slice(0, 200);
    var check = D.checkFile(file, { pdfjs: pdfjsAvailable() });
    if (!check.ok) {
      toast(tr("err_" + check.error, { name: name }), { type: "warn" });
      return Promise.resolve(false);
    }
    var pending = toast(tr("reading", { name: name }), { key: "docs-reading", ms: 60000 });
    var done = function () {
      if (pending && pending.close) pending.close();
    };
    return readText(file, check.kind)
      .then(function (text) {
        if (!text || !text.replace(/\s+/g, "")) {
          var e = new Error("empty");
          e.code = "empty";
          throw e;
        }
        if (D.utf8Length(text) > D.LIMITS.textBytes) {
          var big = new Error("too large");
          big.code = "too_large";
          throw big;
        }
        // POST is allowed while locked (the server then files it under the
        // household), so no PIN prompt here.
        return api().fetchJSON("/api/docs", {
          method: "POST",
          body: { name: name, mime: D.mimeFor(check.kind, name), text: text, owner: owner || "" },
        });
      })
      .then(
        function () {
          done();
          toast(tr("added", { name: name }), { type: "ok", key: "docs-added" });
          return true;
        },
        function (err) {
          done();
          if (err && err.code === "pdf_missing") state.pdfjs = false;
          toast(errText(err, name), { type: "error" });
          return false;
        }
      );
  }

  /* Upload several files one after another (pdf.js is memory-hungry). */
  function uploadFiles(list, owner) {
    var files = D.filesFrom(list).slice(0, D.LIMITS.files);
    if (!files.length) return Promise.resolve(0);
    state.busy++;
    render();
    var ok = 0;
    return files
      .reduce(function (p, f) {
        return p.then(function () {
          return uploadOne(f, owner).then(function (r) {
            if (r) ok++;
          });
        });
      }, Promise.resolve())
      .then(function () {
        state.busy--;
        load();
        return ok;
      });
  }

  function deleteDoc(d) {
    var body = tr("deleteBody") + (d.source === "folder" ? " " + tr("deleteFolderBody") : "");
    return confirmDialog(tr("deleteTitle", { name: d.name }), body, tr("delete")).then(function (yes) {
      if (!yes) return;
      return call("/api/docs/" + encodeURIComponent(d.id), { method: "DELETE" }).then(
        function () {
          state.docs = state.docs.filter(function (x) {
            return x.id !== d.id;
          });
          state.hits = null;
          toast(tr("deleted", { name: d.name }), { type: "ok" });
          render();
          load();
        },
        function (err) {
          toast(errText(err, d.name), { type: "error" });
          if (err && err.status === 404) load();
        }
      );
    });
  }

  function search(q) {
    q = String(q || "").trim();
    state.query = q;
    var seq = ++state.searchSeq;
    if (!q) {
      state.hits = null;
      render();
      return Promise.resolve();
    }
    return call("/api/docs/search", { method: "POST", body: { q: q.slice(0, 500), k: 8 } }).then(
      function (res) {
        if (seq !== state.searchSeq) return;
        state.hits = res && Array.isArray(res.hits) ? res.hits : [];
        render();
      },
      function (err) {
        if (seq !== state.searchSeq) return;
        state.hits = [];
        toast(errText(err), { type: "error" });
        render();
      }
    );
  }

  function rescan() {
    return call("/api/docs/rescan", { method: "POST", body: {} }).then(
      function (res) {
        res = res || {};
        toast(tr("rescanned", { added: res.added || 0, updated: res.updated || 0, removed: res.removed || 0 }), { type: "ok" });
        load();
      },
      function (err) {
        toast(errText(err), { type: "error" });
      }
    );
  }

  // ---------------------------------------------------------------- tab view

  function picker(ownerGetter) {
    var input = el("input", {
      type: "file",
      multiple: true,
      accept: pdfjsAvailable() ? ".pdf,.md,.markdown,.txt,application/pdf,text/plain,text/markdown" : ".md,.markdown,.txt,text/plain,text/markdown",
      class: "docs-file",
      hidden: true,
      onchange: function () {
        var files = D.filesFrom(input.files);
        input.value = "";
        uploadFiles(files, ownerGetter());
      },
    });
    var btn = el("button", {
      type: "button",
      class: "small primary",
      text: tr("upload"),
      disabled: state.readOnly || state.busy > 0,
      onclick: function () {
        input.click();
      },
    });
    return [btn, input];
  }

  function dropZone(ownerGetter) {
    var zone = el("div", { class: "docs-drop", tabindex: "-1" }, [el("span", { text: tr("dropHere") })]);
    var depth = 0;
    zone.addEventListener("dragenter", function (e) {
      if (!hasFiles(e)) return;
      e.preventDefault();
      depth++;
      zone.classList.add("is-over");
    });
    zone.addEventListener("dragover", function (e) {
      if (!hasFiles(e)) return;
      e.preventDefault();
      if (e.dataTransfer) e.dataTransfer.dropEffect = "copy";
    });
    zone.addEventListener("dragleave", function () {
      depth = Math.max(0, depth - 1);
      if (!depth) zone.classList.remove("is-over");
    });
    zone.addEventListener("drop", function (e) {
      if (!hasFiles(e)) return;
      e.preventDefault();
      e.stopPropagation(); // not also the chat-area drop
      depth = 0;
      zone.classList.remove("is-over");
      uploadFiles(e.dataTransfer.files, ownerGetter());
    });
    return zone;
  }

  function hasFiles(e) {
    var dt = e && e.dataTransfer;
    if (!dt || !dt.types) return false;
    for (var i = 0; i < dt.types.length; i++) if (dt.types[i] === "Files") return true;
    return false;
  }

  function ownerSelect() {
    var opts = D.ownerOptions(people(), tr);
    var found = opts.some(function (o) {
      return o.value === state.owner;
    });
    if (!found) state.owner = "";
    var sel = el(
      "select",
      {
        class: "docs-owner",
        "aria-label": tr("owner"),
        disabled: state.locked,
        onchange: function () {
          state.owner = sel.value;
        },
      },
      opts.map(function (o) {
        return el("option", { value: o.value, text: o.label, selected: o.value === state.owner });
      })
    );
    return el("label", { class: "docs-owner-row" }, [el("span", { text: tr("owner") }), sel]);
  }

  function docItem(d, L) {
    var meta = [D.ownerLabel(d.owner, people(), tr), tr("chars", { n: D.formatCount(d.chars, L) })];
    if (d.source === "folder") meta.push(tr("folderDoc"));
    var status = el("span", { class: "docs-status is-" + (d.status || "indexing"), text: tr(D.statusKey(d.status)) });
    return el("li", { class: "docs-item" }, [
      el("div", { class: "docs-item-main" }, [
        el("div", { class: "docs-name", title: d.path || d.name, text: d.name || "?" }),
        el("div", { class: "docs-meta" }, [status, " · " + meta.join(" · ")]),
      ]),
      el("button", {
        type: "button",
        class: "small ghost docs-del",
        text: tr("delete"),
        "aria-label": tr("delete") + ": " + (d.name || ""),
        disabled: state.readOnly,
        onclick: function () {
          deleteDoc(d);
        },
      }),
    ]);
  }

  function hitsView() {
    if (!state.hits) return null;
    if (!state.hits.length) return el("p", { class: "docs-empty", text: tr("searchNone") });
    return el(
      "ul",
      { class: "docs-hits" },
      state.hits.map(function (h) {
        var text = String(h.text || "");
        if (text.length > 400) text = text.slice(0, 400) + " …";
        return el("li", { class: "docs-hit" }, [el("div", { class: "docs-hit-name", text: h.name || "?" }), el("p", { class: "docs-hit-text", text: text })]);
      })
    );
  }

  function render() {
    if (!view) return;
    var root = view.root;
    var L = lang();
    // keep the search box (and its focus) when only the list changes
    var hadFocus = document.activeElement && document.activeElement.classList && document.activeElement.classList.contains("docs-search");
    clear(root);
    root.appendChild(el("p", { class: "docs-intro", text: tr("intro") }));

    var owner = function () {
      return state.locked ? "" : state.owner;
    };
    var up = el("div", { class: "docs-upload" }, [el("div", { class: "docs-upload-row" }, picker(owner).concat([state.locked ? null : ownerSelect()])), dropZone(owner)]);
    root.appendChild(up);
    if (state.pdfjs === false || pdfjsAvailable() === false) root.appendChild(el("p", { class: "docs-note warn", text: tr("pdfMissing") }));
    if (state.readOnly) root.appendChild(el("p", { class: "docs-note warn", text: tr("readOnly") }));

    if (state.locked) {
      root.appendChild(
        el("div", { class: "docs-locked" }, [
          el("p", { text: tr("locked") }),
          peopleUI() && peopleUI().requestUnlock ? el("button", { type: "button", class: "small primary", text: tr("unlock"), onclick: unlockAndLoad }) : null,
        ])
      );
      return;
    }
    if (!state.loaded) {
      root.appendChild(el("p", { class: "docs-empty", text: tr("loading") }));
      return;
    }
    if (state.error) root.appendChild(el("p", { class: "docs-error", role: "alert", text: state.error }));
    if (!state.embedOk && state.docs.length) root.appendChild(el("p", { class: "docs-note", text: tr("lexicalHint") }));
    if (state.folder) {
      root.appendChild(
        el("div", { class: "docs-folder" }, [
          el("span", { text: tr("folder", { name: state.folder }) }),
          el("button", { type: "button", class: "small ghost", text: tr("rescan"), disabled: state.readOnly, onclick: rescan }),
        ])
      );
    }

    if (state.docs.length) {
      var box = el("input", {
        type: "search",
        class: "docs-search",
        placeholder: tr("searchPlaceholder"),
        "aria-label": tr("search"),
        value: state.query,
        maxlength: "500",
      });
      var timer = null;
      box.addEventListener("input", function () {
        if (timer) clearTimeout(timer);
        timer = setTimeout(function () {
          search(box.value);
        }, 350);
      });
      box.addEventListener("keydown", function (e) {
        if (e.key === "Enter") {
          e.preventDefault();
          if (timer) clearTimeout(timer);
          search(box.value);
        }
      });
      root.appendChild(box);
      if (hadFocus) {
        box.focus();
        try {
          box.setSelectionRange(box.value.length, box.value.length);
        } catch (e) {
          /* not all inputs support it */
        }
      }
      var hv = hitsView();
      if (hv) root.appendChild(hv);
    }

    if (!state.docs.length) {
      root.appendChild(el("p", { class: "docs-empty", text: tr("empty") }));
    } else {
      root.appendChild(
        el(
          "ul",
          { class: "docs-list" },
          state.docs.map(function (d) {
            return docItem(d, L);
          })
        )
      );
    }
    root.appendChild(el("p", { class: "docs-hint", text: tr("askHint") }));
  }

  function mountTab(root) {
    root.classList.add("docs");
    view = { root: root };
    render();
  }

  // ---------------------------------------------------------------- settings section

  function mountSection(root) {
    root.classList.add("docs-settings");
    function renderSection() {
      clear(root);
      root.appendChild(el("p", { class: "hint", text: tr("intro") }));
      var n = state.docs.length;
      if (state.locked) root.appendChild(el("p", { class: "hint", text: tr("locked") }));
      else if (state.loaded) root.appendChild(el("p", { text: n === 0 ? tr("sectionNone") : n === 1 ? tr("sectionCount1") : tr("sectionCount", { n: n }) }));
      if (state.loaded && !state.locked) {
        root.appendChild(el("p", { class: "hint", text: state.embedOk && state.embedModel ? tr("embedOk", { model: state.embedModel }) : tr("embedOff") }));
        if (!state.embedOk) root.appendChild(el("p", { class: "hint", text: tr("lexicalHint") }));
      }
      if (pdfjsAvailable() === false) root.appendChild(el("p", { class: "hint warn", text: tr("pdfMissing") }));
      if (state.folder) root.appendChild(el("p", { class: "hint", text: tr("folder", { name: state.folder }) }));
      root.appendChild(el("p", { class: "hint", text: tr("dropChat") }));
      var buttons = [];
      if (typeof api().openTab === "function") {
        buttons.push(
          el("button", {
            type: "button",
            class: "small",
            text: tr("openTab"),
            onclick: function () {
              api().openTab("docs");
            },
          })
        );
      }
      if (state.folder && !state.locked) buttons.push(el("button", { type: "button", class: "small ghost", text: tr("rescan"), onclick: rescan }));
      if (buttons.length) root.appendChild(el("div", { class: "docs-settings-actions" }, buttons));
    }
    sectionView = { render: renderSection };
    renderSection();
    if (!state.loaded) load();
  }

  // ---------------------------------------------------------------- wiring

  H.registerTab({
    id: "docs",
    order: 40,
    label: { en: D.STRINGS.en.tab, de: D.STRINGS.de.tab },
    icon: ICON_TAB,
    mount: mountTab,
    show: function () {
      state.visible = true;
      load();
    },
    hide: function () {
      state.visible = false;
    },
  });
  if (typeof H.registerSettingsSection === "function") {
    H.registerSettingsSection({ id: "docs", order: 55, title: { en: D.STRINGS.en.section, de: D.STRINGS.de.section }, mount: mountSection });
  }

  // Files dropped on the chat area (app.js emits "files:drop" with a
  // FileList or an array of File). They go to the household: the drop zone
  // has no owner choice, and with the personal lock on the server forces
  // the household anyway (SPEC 4.10).
  H.on("files:drop", function (files) {
    var list = D.filesFrom(files);
    if (!list.length) return;
    uploadFiles(list, "");
  });

  H.on("hub:docs.changed", scheduleReload);
  H.on("hub:people.changed", function () {
    render();
  });
  H.on("people:list", render);
  H.on("people:unlocked", function () {
    if (state.visible) load();
  });
  H.on("people:locked", function () {
    if (settings().personalLock) {
      state.docs = [];
      state.hits = null;
      state.locked = true;
      render();
      if (sectionView) sectionView.render();
    }
  });
  // re-render only when PDF support appeared or vanished (status polls are
  // frequent and a re-render would disturb typing in the search box)
  var lastPdfjs = null;
  H.on("status", function () {
    var now = pdfjsAvailable();
    if (now === lastPdfjs) return;
    lastPdfjs = now;
    render();
    if (sectionView) sectionView.render();
  });
  H.on("lang", function () {
    render();
    if (sectionView) sectionView.render();
  });
})();
