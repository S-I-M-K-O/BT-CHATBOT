/*
 * HERMES memory UI (W1-F, SPEC 5.4 "memory.js").
 *
 * Part 1 (UMD, no DOM): pure helpers for the browser (window.HermesMemory)
 * and the node tests (require("../../static/memory.js")):
 *
 *   scopeOf, scopeMatches, filterMemories, sortMemories, foldText,
 *   validateText, validateExpires, isExpired, normalizeKind, savedEventIds,
 *   isOwnEvent, savedToastText, deleteAllQuery, formatDate, KINDS, STRINGS
 *
 * Part 2 (browser only, needs window.Hermes from hermes.js): the hub tab
 * "Gedächtnis" / "Memory", the Settings → Memory section, and the
 * "Gemerkt: …" toast with Undo for `hub:memory.saved` events and `memory`
 * cards of this device.
 *
 * Terminology (SPEC 4.7): stored facts are "Gedächtnis" / "Eintrag" /
 * "gemerkt", never "Erinnerung" (that word is reserved for reminders).
 * Memory texts are user data: they are only ever set with textContent.
 */
(function (root, factory) {
  var api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root && typeof root === "object") root.HermesMemory = api;
})(typeof window !== "undefined" ? window : typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  var TEXT_MAX = 300; // SPEC 2.3: ≤300 runes
  var OWNER = "_owner"; // single-user scope before people existed (SPEC 9.B)
  var KINDS = ["identity", "preference", "relationship", "date", "plan", "health", "home", "other"];

  var STRINGS = {
    en: {
      tab: "Memory",
      section: "Memory",
      enabled: "Use memory (remember and use facts about people)",
      enabledHint: "When off, HERMES neither stores nor uses memory entries. Existing entries stay.",
      auto: "Remember things from normal conversation",
      autoHint: "After a reply, a local model picks out lasting facts. Nothing leaves this computer for this.",
      autoHealth: "Also remember health details automatically",
      autoHealthHint: "Off: health facts are only stored when you say “remember …”.",
      guestHousehold: "Guests may use household entries",
      guestHouseholdHint: "Household entries are shared facts such as the Wi-Fi name. Personal entries are never used for guests.",
      openTab: "Open memory",
      export: "Export …",
      exportTitle: "Export memory entries?",
      exportBody:
        "The file contains all memory entries in plain text. Once downloaded, it is outside HERMES' control: deleting a person later does not delete it.",
      exportGo: "Download",
      exported: "Export downloaded.",
      filterLabel: "Show",
      filterAll: "All",
      filterHousehold: "Household",
      filterOwner: "Before people recognition",
      search: "Search",
      searchPlaceholder: "Search entries …",
      newEntry: "New entry",
      deleteAll: "Delete all ({scope})",
      deleteAllTitle: "Delete all entries of {scope}?",
      deleteAllBody: "{n} entries of {scope} will be deleted. This cannot be undone.",
      deleteAllBody1: "1 entry of {scope} will be deleted. This cannot be undone.",
      deleteAllGo: "Delete all",
      deletedAll: "{n} entries deleted.",
      empty: "Nothing remembered yet. Say “Hermes, remember that …”.",
      emptyFilter: "No entries match.",
      count: "{n} entries",
      count1: "1 entry",
      household: "Household",
      owner: "Before people recognition",
      ownerHint: "Remembered before HERMES knew any people. Assign these entries to someone.",
      assign: "Assign",
      unknownPerson: "Unknown person",
      kind: "Kind",
      scope: "Belongs to",
      pinned: "Pinned (always used)",
      pin: "Pin",
      unpin: "Unpin",
      edit: "Edit",
      del: "Delete",
      save: "Save",
      cancel: "Cancel",
      add: "Save entry",
      text: "Text",
      textPlaceholder: "e.g. Anna does not like coriander.",
      expires: "Forget after (optional)",
      expiresOn: "until {date}",
      src_explicit: "said",
      src_auto: "noticed",
      src_manual: "typed",
      created: "{date}",
      uses: "used {n}×",
      deleted: "Entry deleted.",
      undo: "Undo",
      saved: "Saved.",
      savedToast: "Remembered: {text}",
      savedToastMore: "Remembered: {text} (+{n} more)",
      savedNoText: "New memory entry saved.",
      undone: "Undone.",
      locked: "Memory is locked on this device.",
      unlock: "Unlock",
      disabledNote: "Memory is turned off (Settings → Memory).",
      err_empty: "Please enter a text.",
      err_long: "At most 300 characters.",
      err_date: "Please enter a date in the future.",
      err_locked: "Locked. Please enter the PIN.",
      err_readonly: "The data file was written by a newer HERMES. Changes are blocked.",
      err_notfound: "This entry no longer exists.",
      err_network: "The server cannot be reached.",
      err_generic: "That did not work: {msg}",
      kind_identity: "Identity",
      kind_preference: "Preference",
      kind_relationship: "Relationship",
      kind_date: "Date",
      kind_plan: "Plan",
      kind_health: "Health",
      kind_home: "Home",
      kind_other: "Other",
    },
    de: {
      tab: "Gedächtnis",
      section: "Gedächtnis",
      enabled: "Gedächtnis verwenden (Dinge über Personen merken und nutzen)",
      enabledHint: "Aus: HERMES speichert und nutzt keine Gedächtniseinträge. Bestehende Einträge bleiben.",
      auto: "Dinge aus normalen Gesprächen merken",
      autoHint: "Nach einer Antwort sucht ein lokales Modell dauerhafte Fakten heraus. Dafür verlässt nichts diesen Computer.",
      autoHealth: "Auch Gesundheitsangaben automatisch merken",
      autoHealthHint: "Aus: Gesundheitsangaben werden nur gespeichert, wenn du „merk dir …“ sagst.",
      guestHousehold: "Gäste dürfen Haushaltseinträge nutzen",
      guestHouseholdHint: "Haushaltseinträge sind gemeinsame Fakten, etwa der WLAN-Name. Persönliche Einträge gibt es für Gäste nie.",
      openTab: "Gedächtnis öffnen",
      export: "Exportieren …",
      exportTitle: "Gedächtniseinträge exportieren?",
      exportBody:
        "Die Datei enthält alle Gedächtniseinträge im Klartext. Nach dem Herunterladen liegt sie außerhalb von HERMES: Wird eine Person später gelöscht, bleibt die Datei bestehen.",
      exportGo: "Herunterladen",
      exported: "Export heruntergeladen.",
      filterLabel: "Anzeigen",
      filterAll: "Alle",
      filterHousehold: "Haushalt",
      filterOwner: "Vor der Personenerkennung",
      search: "Suche",
      searchPlaceholder: "Einträge durchsuchen …",
      newEntry: "Neuer Eintrag",
      deleteAll: "Alle löschen ({scope})",
      deleteAllTitle: "Alle Einträge von {scope} löschen?",
      deleteAllBody: "{n} Einträge von {scope} werden gelöscht. Das kann nicht rückgängig gemacht werden.",
      deleteAllBody1: "1 Eintrag von {scope} wird gelöscht. Das kann nicht rückgängig gemacht werden.",
      deleteAllGo: "Alle löschen",
      deletedAll: "{n} Einträge gelöscht.",
      empty: "Noch nichts gemerkt. Sag „Hermes, merk dir, dass …“.",
      emptyFilter: "Keine passenden Einträge.",
      count: "{n} Einträge",
      count1: "1 Eintrag",
      household: "Haushalt",
      owner: "Vor der Personenerkennung",
      ownerHint: "Gemerkt, bevor HERMES Personen kannte. Ordne diese Einträge jemandem zu.",
      assign: "Zuordnen",
      unknownPerson: "Unbekannte Person",
      kind: "Art",
      scope: "Gehört zu",
      pinned: "Angeheftet (immer verwenden)",
      pin: "Anheften",
      unpin: "Lösen",
      edit: "Bearbeiten",
      del: "Löschen",
      save: "Speichern",
      cancel: "Abbrechen",
      add: "Eintrag speichern",
      text: "Text",
      textPlaceholder: "z. B. Anna mag keinen Koriander.",
      expires: "Vergessen nach (optional)",
      expiresOn: "bis {date}",
      src_explicit: "gesagt",
      src_auto: "mitbekommen",
      src_manual: "eingetippt",
      created: "{date}",
      uses: "{n}× verwendet",
      deleted: "Eintrag gelöscht.",
      undo: "Rückgängig",
      saved: "Gespeichert.",
      savedToast: "Gemerkt: {text}",
      savedToastMore: "Gemerkt: {text} (+{n} weitere)",
      savedNoText: "Neuer Eintrag gespeichert.",
      undone: "Rückgängig gemacht.",
      locked: "Das Gedächtnis ist auf diesem Gerät gesperrt.",
      unlock: "Entsperren",
      disabledNote: "Das Gedächtnis ist ausgeschaltet (Einstellungen → Gedächtnis).",
      err_empty: "Bitte gib einen Text ein.",
      err_long: "Höchstens 300 Zeichen.",
      err_date: "Bitte gib ein Datum in der Zukunft an.",
      err_locked: "Gesperrt. Bitte gib die PIN ein.",
      err_readonly: "Die Datendatei stammt von einer neueren HERMES-Version. Änderungen sind gesperrt.",
      err_notfound: "Diesen Eintrag gibt es nicht mehr.",
      err_network: "Der Server ist nicht erreichbar.",
      err_generic: "Das hat nicht geklappt: {msg}",
      kind_identity: "Identität",
      kind_preference: "Vorliebe",
      kind_relationship: "Beziehung",
      kind_date: "Datum",
      kind_plan: "Vorhaben",
      kind_health: "Gesundheit",
      kind_home: "Zuhause",
      kind_other: "Sonstiges",
    },
  };

  function format(str, vars) {
    if (typeof str !== "string") return "";
    if (!vars) return str;
    return str.replace(/\{(\w+)\}/g, function (m, k) {
      return Object.prototype.hasOwnProperty.call(vars, k) && vars[k] != null ? String(vars[k]) : m;
    });
  }

  function makeTr(dict, langFn) {
    return function (key, vars) {
      var lang = (langFn && langFn()) === "de" ? "de" : "en";
      var s = (dict[lang] && dict[lang][key]) || (dict.en && dict.en[key]) || key;
      return format(s, vars);
    };
  }

  function runeLen(s) {
    var n = 0;
    for (var i = 0; i < s.length; i++) {
      var c = s.charCodeAt(i);
      if (c < 0xdc00 || c > 0xdfff) n++;
    }
    return n;
  }

  // ------------------------------------------------------------ scopes

  /* "household" for "", the person id otherwise ("_owner" stays as is). */
  function scopeOf(m) {
    var p = m && typeof m.person === "string" ? m.person : "";
    return p === "" ? "household" : p;
  }

  /* filter: "all" | "household" | "_owner" | "p_…" */
  function scopeMatches(m, filter) {
    if (!filter || filter === "all") return true;
    return scopeOf(m) === filter;
  }

  // ------------------------------------------------------------ search

  /* Lower-case, strip diacritics, ß → ss: "Größe" and "grosse" match. */
  function foldText(s) {
    s = String(s == null ? "" : s).toLowerCase();
    if (s.normalize) s = s.normalize("NFD").replace(/[̀-ͯ]/g, "");
    return s.replace(/ß/g, "ss").replace(/\s+/g, " ").trim();
  }

  /*
   * filterMemories(list, {scope, query, kindLabel}) → new array.
   * Every whitespace-separated query token must occur in the text (or in the
   * kind label, when kindLabel(kind) is given). Expired entries are kept: the
   * server purges them, and the user should still see what is stored.
   */
  function filterMemories(list, opts) {
    opts = opts || {};
    var tokens = foldText(opts.query).split(" ").filter(Boolean);
    return (list || []).filter(function (m) {
      if (!m || typeof m !== "object") return false;
      if (!scopeMatches(m, opts.scope)) return false;
      if (!tokens.length) return true;
      var hay = foldText(m.text) + " " + (opts.kindLabel ? foldText(opts.kindLabel(m.kind)) : "");
      for (var i = 0; i < tokens.length; i++) if (hay.indexOf(tokens[i]) < 0) return false;
      return true;
    });
  }

  function timeOf(v) {
    var t = typeof v === "number" ? v : Date.parse(v || "");
    return isFinite(t) ? t : 0;
  }

  /* Pinned first, then updatedAt (or createdAt) newest first, then id: the server order (SPEC 3.4). */
  function sortMemories(list) {
    return (list || []).slice().sort(function (a, b) {
      var pa = a && a.pinned ? 1 : 0;
      var pb = b && b.pinned ? 1 : 0;
      if (pa !== pb) return pb - pa;
      var ta = timeOf(a && (a.updatedAt || a.createdAt));
      var tb = timeOf(b && (b.updatedAt || b.createdAt));
      if (ta !== tb) return tb - ta;
      var ia = String((a && a.id) || "");
      var ib = String((b && b.id) || "");
      return ia < ib ? -1 : ia > ib ? 1 : 0;
    });
  }

  // ------------------------------------------------------------ validation

  /* validateText(raw) → {ok, text, error ∈ "empty"|"long"} */
  function validateText(raw) {
    var s = String(raw == null ? "" : raw);
    if (s.normalize) s = s.normalize("NFC");
    s = s.replace(/[\u0000-\u001f\u007f]+/g, " ").replace(/\s+/g, " ").trim();
    if (!s) return { ok: false, text: "", error: "empty" };
    if (runeLen(s) > TEXT_MAX) return { ok: false, text: s, error: "long" };
    return { ok: true, text: s, error: "" };
  }

  function pad(n) {
    return (n < 10 ? "0" : "") + n;
  }

  function localDay(now) {
    var d = new Date(now);
    return d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate());
  }

  /*
   * validateExpires(v, now) → {ok, value}. "" is valid (no expiry). A date
   * must be a real "YYYY-MM-DD" after today (local date).
   */
  function validateExpires(v, now) {
    var s = String(v == null ? "" : v).trim();
    if (!s) return { ok: true, value: "" };
    var m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s);
    if (!m) return { ok: false, value: s };
    var y = +m[1];
    var mo = +m[2];
    var d = +m[3];
    var dt = new Date(Date.UTC(y, mo - 1, d));
    if (dt.getUTCFullYear() !== y || dt.getUTCMonth() !== mo - 1 || dt.getUTCDate() !== d) return { ok: false, value: s };
    if (s <= localDay(now == null ? Date.now() : now)) return { ok: false, value: s };
    return { ok: true, value: s };
  }

  /* An entry is expired once its expiry day has passed (the expiry day itself still counts). */
  function isExpired(m, now) {
    var e = m && m.expires;
    if (!e || !/^\d{4}-\d{2}-\d{2}$/.test(e)) return false;
    return e < localDay(now == null ? Date.now() : now);
  }

  function normalizeKind(k) {
    return KINDS.indexOf(k) >= 0 ? k : "other";
  }

  // ------------------------------------------------------------ events

  var ID_RE = /^m_[0-9a-z]+$/i;

  /* Ids of a memory.saved hub event (accepts the HubEvent or its data). */
  function savedEventIds(ev) {
    var d = ev && ev.data && typeof ev.data === "object" ? ev.data : ev;
    var ids = d && Array.isArray(d.ids) ? d.ids : [];
    var out = [];
    for (var i = 0; i < ids.length && out.length < 10; i++) {
      if (typeof ids[i] === "string" && ID_RE.test(ids[i]) && out.indexOf(ids[i]) < 0) out.push(ids[i]);
    }
    return out;
  }

  /* Toasts appear only on the device that made the request (SPEC 5.4). */
  function isOwnEvent(ev, clientId) {
    var d = ev && ev.data && typeof ev.data === "object" ? ev.data : ev;
    return !!(d && clientId && typeof d.clientId === "string" && d.clientId === clientId);
  }

  function clip(s, n) {
    s = String(s || "");
    if (runeLen(s) <= n) return s;
    var arr = Array.from ? Array.from(s) : s.split("");
    return arr.slice(0, n - 1).join("") + "…";
  }

  /* "Gemerkt: {text}" for the newest saved entry, "(+n weitere)" for the rest. */
  function savedToastText(memories, tr) {
    var list = (memories || []).filter(function (m) {
      return m && typeof m.text === "string" && m.text;
    });
    if (!list.length) return tr("savedNoText");
    var first = clip(list[0].text, 140);
    return list.length === 1 ? tr("savedToast", { text: first }) : tr("savedToastMore", { text: first, n: list.length - 1 });
  }

  /* Query string for "Alle löschen" (only a person or the household, SPEC 3.4). */
  function deleteAllQuery(scope) {
    if (scope === "household") return "?person=household&confirm=1";
    if (typeof scope === "string" && /^p_[0-9a-z]+$/i.test(scope)) return "?person=" + encodeURIComponent(scope) + "&confirm=1";
    return "";
  }

  var MONTHS = {
    en: ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"],
    de: ["Jan.", "Feb.", "März", "Apr.", "Mai", "Juni", "Juli", "Aug.", "Sep.", "Okt.", "Nov.", "Dez."],
  };

  /* "3. März 2027" / "3 Mar 2027" for an ISO time or a YYYY-MM-DD date; "" when unknown. */
  function formatDate(v, lang) {
    if (!v) return "";
    var d;
    var m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(String(v));
    if (m) d = new Date(+m[1], +m[2] - 1, +m[3]);
    else {
      var t = Date.parse(v);
      if (!isFinite(t)) return "";
      d = new Date(t);
    }
    if (d.getFullYear() < 2000) return ""; // Go zero time
    var mon = MONTHS[lang === "de" ? "de" : "en"][d.getMonth()];
    return lang === "de" ? d.getDate() + ". " + mon + " " + d.getFullYear() : d.getDate() + " " + mon + " " + d.getFullYear();
  }

  return {
    TEXT_MAX: TEXT_MAX,
    OWNER: OWNER,
    KINDS: KINDS,
    STRINGS: STRINGS,
    format: format,
    makeTr: makeTr,
    scopeOf: scopeOf,
    scopeMatches: scopeMatches,
    foldText: foldText,
    filterMemories: filterMemories,
    sortMemories: sortMemories,
    validateText: validateText,
    validateExpires: validateExpires,
    isExpired: isExpired,
    normalizeKind: normalizeKind,
    savedEventIds: savedEventIds,
    isOwnEvent: isOwnEvent,
    savedToastText: savedToastText,
    deleteAllQuery: deleteAllQuery,
    formatDate: formatDate,
  };
});

/* ====================================================================== */
/* Browser UI                                                              */
/* ====================================================================== */
(function () {
  "use strict";
  if (typeof window === "undefined" || typeof document === "undefined") return;
  var H = window.Hermes;
  var M = window.HermesMemory;
  if (!H || !M) return; // hermes.js missing: the page works without the memory tab

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
  var tr = typeof H.strings === "function" ? H.strings(M.STRINGS) : M.makeTr(M.STRINGS, lang);

  function peopleUI() {
    var P = window.HermesPeople;
    return (P && P.ui) || null;
  }
  function settings() {
    try {
      return (api().settings && api().settings()) || {};
    } catch (e) {
      return {};
    }
  }
  function people() {
    var ui = peopleUI();
    return ui && ui.people ? ui.people() : state.people;
  }
  function personName(id) {
    var list = people();
    for (var i = 0; i < list.length; i++) if (list[i].id === id) return list[i].name;
    return "";
  }
  function scopeLabel(scope) {
    if (scope === "household" || scope === "") return tr("household");
    if (scope === M.OWNER) return tr("owner");
    return personName(scope) || tr("unknownPerson");
  }
  function kindLabel(k) {
    return tr("kind_" + M.normalizeKind(k));
  }

  var state = {
    list: [],
    loaded: false,
    locked: false,
    error: "",
    filter: "all",
    query: "",
    editing: "",
    adding: false,
    people: [],
    visible: false,
    loadSeq: 0,
  };
  var view = null; // {root, render}
  var toasted = {}; // memory id → time, dedupes card + hub event

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
        else if (k === "checked") e.checked = !!v;
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
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 4a3 3 0 0 0-3 3v.2A3 3 0 0 0 4 10a3 3 0 0 0 1 2.2A3 3 0 0 0 6 17a3 3 0 0 0 3 3h0a3 3 0 0 0 3-3V7a3 3 0 0 0-3-3z"/><path d="M15 4a3 3 0 0 1 3 3v.2A3 3 0 0 1 20 10a3 3 0 0 1-1 2.2A3 3 0 0 1 18 17a3 3 0 0 1-3 3h0a3 3 0 0 1-3-3"/></svg>';

  function toast(msg, opts) {
    opts = opts || {};
    if (opts.ms == null && opts.timeout != null) opts.ms = opts.timeout; // SPEC 5.2 name
    if (api().toast) return api().toast(msg, opts);
    return { close: function () {} };
  }

  function errKey(err) {
    if (!err) return "err_generic";
    var code = err.code || "";
    if (code === "locked") return "err_locked";
    if (err.status === 503 && /read-?only|newer/i.test(String(err.message || "") + code)) return "err_readonly";
    if (code === "read_only" || code === "readonly") return "err_readonly";
    if (err.status === 404) return "err_notfound";
    if (!err.status) return "err_network";
    return "err_generic";
  }
  function errText(err) {
    return tr(errKey(err), { msg: (err && err.message) || "?" });
  }

  /* fetchJSON with the shared PIN prompt + one retry on 403 locked (people.js). */
  function call(url, opts) {
    var ui = peopleUI();
    if (ui && ui.call) return ui.call(url, opts || {});
    return api().fetchJSON(url, opts || {});
  }

  function confirmDialog(title, body, okLabel) {
    var ui = peopleUI();
    if (ui && ui.dialog) {
      return ui
        .dialog({
          cls: "pp-confirm mem-confirm",
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

  // ---------------------------------------------------------------- data

  function load() {
    if (!api().fetchJSON) return Promise.resolve();
    var seq = ++state.loadSeq;
    // A plain fetch: an open tab must not pop a PIN prompt on every refresh.
    return api()
      .fetchJSON("/api/memories?person=all")
      .then(
        function (res) {
          if (seq !== state.loadSeq) return;
          state.list = res && Array.isArray(res.memories) ? res.memories : [];
          state.loaded = true;
          state.locked = false;
          state.error = "";
          render();
        },
        function (err) {
          if (seq !== state.loadSeq) return;
          state.loaded = true;
          state.locked = !!(err && err.code === "locked");
          state.error = state.locked ? "" : errText(err);
          if (state.locked) state.list = [];
          render();
        }
      );
  }

  function unlockAndLoad() {
    var ui = peopleUI();
    if (!ui || !ui.requestUnlock) return;
    ui.requestUnlock().then(function (ok) {
      if (ok) load();
    });
  }

  function patch(m, body) {
    return call("/api/memories/" + encodeURIComponent(m.id), { method: "PATCH", body: body }).then(
      function (res) {
        replaceLocal(res && res.id ? res : null, m.id, body);
        render();
        return res;
      },
      function (err) {
        toast(errText(err), { type: "error", key: "mem-err" });
        if (err && err.status === 404) load();
        throw err;
      }
    );
  }

  function replaceLocal(updated, id, body) {
    for (var i = 0; i < state.list.length; i++) {
      if (state.list[i].id !== id) continue;
      state.list[i] = updated || Object.assign({}, state.list[i], body);
      return;
    }
  }

  function removeLocal(id) {
    state.list = state.list.filter(function (m) {
      return m.id !== id;
    });
  }

  /* Undo for a deleted entry: create it again with the same content (new id). */
  function recreate(m) {
    return call("/api/memories", {
      method: "POST",
      body: { text: m.text, person: m.person || "", kind: M.normalizeKind(m.kind), pinned: !!m.pinned, expires: m.expires || "" },
    }).then(
      function () {
        toast(tr("undone"), { type: "success", key: "mem-undo" });
        load();
      },
      function (err) {
        toast(errText(err), { type: "error", key: "mem-err" });
      }
    );
  }

  function del(m) {
    return call("/api/memories/" + encodeURIComponent(m.id), { method: "DELETE" }).then(
      function () {
        removeLocal(m.id);
        render();
        toast(tr("deleted"), {
          key: "mem-del",
          ms: 8000,
          action: {
            label: tr("undo"),
            fn: function () {
              recreate(m);
            },
          },
        });
      },
      function (err) {
        toast(errText(err), { type: "error", key: "mem-err" });
        if (err && err.status === 404) load();
      }
    );
  }

  function deleteAll(scope) {
    var q = M.deleteAllQuery(scope);
    if (!q) return;
    var n = state.list.filter(function (m) {
      return M.scopeOf(m) === scope;
    }).length;
    var label = scopeLabel(scope);
    confirmDialog(
      tr("deleteAllTitle", { scope: label }),
      n === 1 ? tr("deleteAllBody1", { scope: label }) : tr("deleteAllBody", { n: n, scope: label }),
      tr("deleteAllGo")
    ).then(function (ok) {
      if (!ok) return;
      call("/api/memories" + q, { method: "DELETE" }).then(
        function (res) {
          toast(tr("deletedAll", { n: (res && res.deleted) || 0 }), { type: "success", key: "mem-del" });
          load();
        },
        function (err) {
          toast(errText(err), { type: "error", key: "mem-err" });
        }
      );
    });
  }

  function exportAll() {
    confirmDialog(tr("exportTitle"), tr("exportBody"), tr("exportGo")).then(function (ok) {
      if (!ok) return;
      call("/api/memories/export").then(
        function (res) {
          var blob = new Blob([JSON.stringify(res || {}, null, 2)], { type: "application/json" });
          var url = URL.createObjectURL(blob);
          var a = el("a", { href: url, download: "hermes-memories-" + new Date().toISOString().slice(0, 10) + ".json", hidden: true });
          document.body.appendChild(a);
          a.click();
          setTimeout(function () {
            URL.revokeObjectURL(url);
            if (a.parentNode) a.parentNode.removeChild(a);
          }, 1000);
          toast(tr("exported"), { type: "success", key: "mem-export" });
        },
        function (err) {
          toast(errText(err), { type: "error", key: "mem-err" });
        }
      );
    });
  }

  // ---------------------------------------------------------------- saved toasts

  function seen(id) {
    var now = Date.now();
    for (var k in toasted) if (now - toasted[k] > 600000) delete toasted[k];
    if (toasted[id]) return true;
    toasted[id] = now;
    return false;
  }

  function clientId() {
    try {
      return (api().clientId && api().clientId()) || "";
    } catch (e) {
      return "";
    }
  }

  function savedToast(mems, ids) {
    var undoIds = ids.slice();
    toast(M.savedToastText(mems, tr), {
      type: "success",
      key: "mem-saved",
      ms: 8000,
      action: {
        label: tr("undo"),
        fn: function () {
          Promise.all(
            undoIds.map(function (id) {
              return call("/api/memories/" + encodeURIComponent(id), { method: "DELETE" }).catch(function (err) {
                if (err && err.status === 404) return null; // already gone
                throw err;
              });
            })
          ).then(
            function () {
              undoIds.forEach(removeLocal);
              render();
              toast(tr("undone"), { type: "success", key: "mem-undo" });
            },
            function (err) {
              toast(errText(err), { type: "error", key: "mem-err" });
            }
          );
        },
      },
    });
  }

  function onSaved(ev) {
    if (view) load(); // keep an open tab in sync on every device
    if (!M.isOwnEvent(ev, clientId())) return;
    var ids = M.savedEventIds(ev).filter(function (id) {
      return !seen(id);
    });
    if (!ids.length) return;
    // The event carries ids only (no text on the wire, SPEC 3.2); fetch the texts.
    api()
      .fetchJSON("/api/memories?ids=" + ids.map(encodeURIComponent).join(","))
      .then(
        function (res) {
          var mems = res && Array.isArray(res.memories) ? res.memories : [];
          var order = {};
          ids.forEach(function (id, i) {
            order[id] = i;
          });
          mems.sort(function (a, b) {
            return (order[a.id] || 0) - (order[b.id] || 0);
          });
          savedToast(mems, ids);
        },
        function (err) {
          if (err && err.code === "locked") savedToast([], ids); // no text while locked
        }
      );
  }

  /* `memory` card of an explicit "merk dir" in this device's reply: {id, text, person}. */
  function onCard(c) {
    if (!c || c.kind !== "memory" || !c.data) return;
    var d = c.data;
    if (typeof d.id !== "string" || !/^m_[0-9a-z]+$/i.test(d.id) || seen(d.id)) return;
    savedToast([{ id: d.id, text: typeof d.text === "string" ? d.text : "" }], [d.id]);
    if (view) load();
  }

  // ---------------------------------------------------------------- tab rendering

  function kindSelect(value, onChange, label) {
    var s = el("select", { class: "mem-kind", "aria-label": label || tr("kind") });
    M.KINDS.forEach(function (k) {
      s.appendChild(el("option", { value: k, text: kindLabel(k), selected: k === M.normalizeKind(value) }));
    });
    if (onChange)
      s.addEventListener("change", function () {
        onChange(s.value);
      });
    return s;
  }

  /* Scope select: household plus every person (and "_owner" only when the entry is still there). */
  function scopeSelect(current, onChange, includeOwner) {
    var s = el("select", { class: "mem-scope", "aria-label": tr("scope") });
    s.appendChild(el("option", { value: "", text: tr("household"), selected: current === "" || current === "household" }));
    people().forEach(function (p) {
      s.appendChild(el("option", { value: p.id, text: p.name, selected: current === p.id }));
    });
    if (includeOwner) s.appendChild(el("option", { value: M.OWNER, text: tr("owner"), selected: current === M.OWNER, disabled: true }));
    if (onChange)
      s.addEventListener("change", function () {
        onChange(s.value);
      });
    return s;
  }

  function editor(m) {
    var ta = el("textarea", { class: "mem-edit", rows: "2", maxlength: String(M.TEXT_MAX + 20), "aria-label": tr("text") });
    ta.value = m.text || "";
    var err = el("p", { class: "mem-error", role: "alert" });
    function cancel() {
      state.editing = "";
      render();
    }
    function save() {
      var v = M.validateText(ta.value);
      if (!v.ok) {
        err.textContent = tr("err_" + v.error);
        return;
      }
      if (v.text === m.text) return cancel();
      patch(m, { text: v.text }).then(function () {
        state.editing = "";
        render();
      }, function () {});
    }
    ta.addEventListener("keydown", function (e) {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        cancel();
      } else if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
        e.preventDefault();
        save();
      }
    });
    setTimeout(function () {
      ta.focus();
      ta.setSelectionRange(ta.value.length, ta.value.length);
    }, 0);
    return el("div", { class: "mem-editor" }, [
      ta,
      err,
      el("div", { class: "mem-row-actions" }, [
        el("button", { type: "button", class: "small ghost", text: tr("cancel"), onclick: cancel }),
        el("button", { type: "button", class: "small primary", text: tr("save"), onclick: save }),
      ]),
    ]);
  }

  function item(m, now, L) {
    var expired = M.isExpired(m, now);
    var meta = [];
    var src = m.source === "explicit" || m.source === "auto" || m.source === "manual" ? tr("src_" + m.source) : "";
    var date = M.formatDate(m.updatedAt || m.createdAt, L);
    if (src || date) meta.push([src, date].filter(Boolean).join(" · "));
    if (m.expires) meta.push(tr("expiresOn", { date: M.formatDate(m.expires, L) || m.expires }));
    if (m.uses > 0) meta.push(tr("uses", { n: m.uses }));

    var body;
    if (state.editing === m.id) body = editor(m);
    else
      body = el("button", {
        type: "button",
        class: "mem-text",
        title: tr("edit"),
        text: m.text,
        onclick: function () {
          state.editing = m.id;
          render();
        },
      });

    var pinBtn = el("button", {
      type: "button",
      class: "small ghost mem-pin" + (m.pinned ? " is-on" : ""),
      "aria-pressed": m.pinned ? "true" : "false",
      title: tr("pinned"),
      text: m.pinned ? tr("unpin") : tr("pin"),
      onclick: function () {
        patch(m, { pinned: !m.pinned }).catch(function () {});
      },
    });
    var scope = M.scopeOf(m);
    return el("li", { class: "mem-item" + (m.pinned ? " is-pinned" : "") + (expired ? " is-expired" : ""), "data-kind": M.normalizeKind(m.kind) }, [
      body,
      el("div", { class: "mem-controls" }, [
        kindSelect(m.kind, function (k) {
          patch(m, { kind: k }).catch(render);
        }),
        scopeSelect(scope === "household" ? "" : scope, function (p) {
          patch(m, { person: p }).catch(render);
        }, scope === M.OWNER),
        pinBtn,
        el("button", {
          type: "button",
          class: "small ghost mem-del",
          text: tr("del"),
          onclick: function () {
            del(m);
          },
        }),
      ]),
      meta.length ? el("p", { class: "mem-meta", text: meta.join(" · ") }) : null,
    ]);
  }

  function newEntryForm() {
    var ta = el("textarea", { class: "mem-new-text", rows: "2", maxlength: String(M.TEXT_MAX + 20), placeholder: tr("textPlaceholder"), "aria-label": tr("text") });
    var f = state.filter;
    var initialScope = f === "household" || f === "all" || f === M.OWNER ? "" : f;
    var scope = scopeSelect(initialScope, null, false);
    var kind = kindSelect("other", null);
    var pinned = el("input", { type: "checkbox" });
    var exp = el("input", { type: "date", class: "mem-expires", "aria-label": tr("expires") });
    var err = el("p", { class: "mem-error", role: "alert" });
    var saveBtn = el("button", { type: "submit", class: "small primary", text: tr("add") });
    var form = el("form", { class: "mem-new" }, [
      el("label", { class: "mem-field mem-field-wide" }, [tr("text"), ta]),
      el("div", { class: "mem-new-row" }, [
        el("label", { class: "mem-field" }, [tr("scope"), scope]),
        el("label", { class: "mem-field" }, [tr("kind"), kind]),
        el("label", { class: "mem-field" }, [tr("expires"), exp]),
      ]),
      el("label", { class: "mem-check" }, [pinned, " " + tr("pinned")]),
      err,
      el("div", { class: "mem-row-actions" }, [
        el("button", {
          type: "button",
          class: "small ghost",
          text: tr("cancel"),
          onclick: function () {
            state.adding = false;
            render();
          },
        }),
        saveBtn,
      ]),
    ]);
    form.addEventListener("submit", function (e) {
      e.preventDefault();
      var v = M.validateText(ta.value);
      if (!v.ok) {
        err.textContent = tr("err_" + v.error);
        return;
      }
      var ex = M.validateExpires(exp.value, Date.now());
      if (!ex.ok) {
        err.textContent = tr("err_date");
        return;
      }
      saveBtn.disabled = true;
      call("/api/memories", {
        method: "POST",
        body: { text: v.text, person: scope.value, kind: kind.value, pinned: !!pinned.checked, expires: ex.value },
      }).then(
        function () {
          state.adding = false;
          toast(tr("saved"), { type: "success", key: "mem-new" });
          load();
        },
        function (e2) {
          saveBtn.disabled = false;
          err.textContent = errText(e2);
        }
      );
    });
    setTimeout(function () {
      ta.focus();
    }, 0);
    return form;
  }

  function toolbar(L) {
    var filter = el("select", { class: "mem-filter", "aria-label": tr("filterLabel") });
    var opts = [
      ["all", tr("filterAll")],
      ["household", tr("filterHousehold")],
    ];
    people().forEach(function (p) {
      opts.push([p.id, p.name]);
    });
    var hasOwner = state.list.some(function (m) {
      return M.scopeOf(m) === M.OWNER;
    });
    if (hasOwner || state.filter === M.OWNER) opts.push([M.OWNER, tr("filterOwner")]);
    var known = opts.some(function (o) {
      return o[0] === state.filter;
    });
    if (!known) state.filter = "all";
    opts.forEach(function (o) {
      filter.appendChild(el("option", { value: o[0], text: o[1], selected: o[0] === state.filter }));
    });
    filter.addEventListener("change", function () {
      state.filter = filter.value;
      state.editing = "";
      render();
    });

    var search = el("input", {
      type: "search",
      class: "mem-search",
      placeholder: tr("searchPlaceholder"),
      "aria-label": tr("search"),
      autocomplete: "off",
      spellcheck: "false",
      maxlength: "100",
      value: state.query,
    });
    search.addEventListener("input", function () {
      state.query = search.value;
      renderList();
    });

    var right = [
      el("button", {
        type: "button",
        class: "small primary",
        text: tr("newEntry"),
        disabled: state.adding,
        onclick: function () {
          state.adding = true;
          render();
        },
      }),
    ];
    if (M.deleteAllQuery(state.filter)) {
      right.push(
        el("button", {
          type: "button",
          class: "small ghost pp-danger-text mem-del-all",
          text: tr("deleteAll", { scope: scopeLabel(state.filter) }),
          onclick: function () {
            deleteAll(state.filter);
          },
        })
      );
    }
    return el("div", { class: "mem-toolbar" }, [
      el("div", { class: "mem-toolbar-row" }, [filter, search]),
      el("div", { class: "mem-toolbar-row mem-toolbar-actions" }, right),
    ]);
  }

  var listHost = null;

  function renderList() {
    if (!listHost) return;
    clear(listHost);
    var L = lang();
    var now = Date.now();
    var shown = M.sortMemories(M.filterMemories(state.list, { scope: state.filter, query: state.query, kindLabel: kindLabel }));
    if (!state.list.length) {
      listHost.appendChild(el("p", { class: "mem-empty", text: tr("empty") }));
      return;
    }
    if (!shown.length) {
      listHost.appendChild(el("p", { class: "mem-empty", text: tr("emptyFilter") }));
      return;
    }
    listHost.appendChild(el("p", { class: "mem-count", text: shown.length === 1 ? tr("count1") : tr("count", { n: shown.length }) }));
    // group by scope when showing all; owner group first (it needs action)
    var groups = {};
    var order = [];
    shown.forEach(function (m) {
      var s = M.scopeOf(m);
      if (!groups[s]) {
        groups[s] = [];
        order.push(s);
      }
      groups[s].push(m);
    });
    order.sort(function (a, b) {
      var ra = a === M.OWNER ? 0 : a === "household" ? 2 : 1;
      var rb = b === M.OWNER ? 0 : b === "household" ? 2 : 1;
      if (ra !== rb) return ra - rb;
      return scopeLabel(a).localeCompare(scopeLabel(b), L);
    });
    order.forEach(function (s) {
      var head = [el("h4", { class: "mem-group-title", text: scopeLabel(s) })];
      if (s === M.OWNER) {
        var ui = peopleUI();
        head.push(el("p", { class: "mem-owner-hint", text: tr("ownerHint") }));
        if (ui && ui.openMigration && people().length) {
          head.push(
            el("button", {
              type: "button",
              class: "small",
              text: tr("assign"),
              onclick: function () {
                Promise.resolve(ui.openMigration()).then(load, function () {});
              },
            })
          );
        }
      }
      var ul = el("ul", { class: "mem-list" });
      groups[s].forEach(function (m) {
        ul.appendChild(item(m, now, L));
      });
      listHost.appendChild(el("section", { class: "mem-group" + (s === M.OWNER ? " is-owner" : "") }, [el("div", { class: "mem-group-head" }, head), ul]));
    });
  }

  function render() {
    if (!view) return;
    var root = view.root;
    clear(root);
    listHost = null;
    var L = lang();
    if (settings().memoryEnabled === false) root.appendChild(el("p", { class: "mem-note warn", text: tr("disabledNote") }));
    if (state.locked) {
      root.appendChild(
        el("div", { class: "mem-locked" }, [
          el("p", { text: tr("locked") }),
          peopleUI() && peopleUI().requestUnlock ? el("button", { type: "button", class: "small primary", text: tr("unlock"), onclick: unlockAndLoad }) : null,
        ])
      );
      return;
    }
    if (!state.loaded) {
      root.appendChild(el("p", { class: "mem-empty", text: "…" }));
      return;
    }
    root.appendChild(toolbar(L));
    if (state.error) root.appendChild(el("p", { class: "mem-error", role: "alert", text: state.error }));
    if (state.adding) root.appendChild(newEntryForm());
    listHost = el("div", { class: "mem-body" });
    root.appendChild(listHost);
    renderList();
  }

  function mountTab(root) {
    root.classList.add("mem");
    view = { root: root };
    render();
  }

  // ---------------------------------------------------------------- settings section

  function mountSection(root) {
    root.classList.add("mem-settings");
    var boxes = {};
    function toggle(key, label, hint) {
      var input = el("input", { type: "checkbox", "data-setting": key, id: "s_" + key });
      boxes[key] = input;
      return [el("label", { class: "check" }, [input, " " + tr(label)]), hint ? el("p", { class: "hint", text: tr(hint) }) : null];
    }
    function fill() {
      var s = settings();
      var defs = { memoryEnabled: true, memoryAuto: true, memoryAutoHealth: false, guestSeesHousehold: false };
      for (var k in boxes) boxes[k].checked = s[k] == null ? defs[k] : !!s[k];
      sync();
    }
    function sync() {
      var on = boxes.memoryEnabled && boxes.memoryEnabled.checked;
      if (boxes.memoryAuto) boxes.memoryAuto.disabled = !on;
      if (boxes.memoryAutoHealth) boxes.memoryAutoHealth.disabled = !on || !boxes.memoryAuto.checked;
    }
    function renderSection() {
      clear(root);
      boxes = {};
      var parts = []
        .concat(toggle("memoryEnabled", "enabled", "enabledHint"))
        .concat(toggle("memoryAuto", "auto", "autoHint"))
        .concat(toggle("memoryAutoHealth", "autoHealth", "autoHealthHint"))
        .concat(toggle("guestSeesHousehold", "guestHousehold", "guestHouseholdHint"));
      parts.forEach(function (p) {
        if (p) root.appendChild(p);
      });
      var buttons = [];
      if (typeof api().openTab === "function") {
        buttons.push(
          el("button", {
            type: "button",
            class: "small",
            text: tr("openTab"),
            onclick: function () {
              api().openTab("memory");
            },
          })
        );
      }
      buttons.push(el("button", { type: "button", class: "small ghost", text: tr("export"), onclick: exportAll }));
      root.appendChild(el("div", { class: "mem-settings-actions" }, buttons));
      fill();
    }
    root.addEventListener("change", sync);
    sectionView = { render: renderSection, fill: fill };
    renderSection();
  }
  var sectionView = null;

  // ---------------------------------------------------------------- wiring

  if (typeof H.registerTab === "function") {
    H.registerTab({
      id: "memory",
      order: 20,
      label: { en: M.STRINGS.en.tab, de: M.STRINGS.de.tab },
      icon: ICON_TAB,
      mount: mountTab,
      show: function () {
        state.visible = true;
        load();
      },
      hide: function () {
        state.visible = false;
        state.editing = "";
      },
    });
  }
  if (typeof H.registerSettingsSection === "function") {
    H.registerSettingsSection({ id: "memory", order: 20, title: { en: M.STRINGS.en.section, de: M.STRINGS.de.section }, mount: mountSection });
  }

  H.on("hub:memory.saved", onSaved);
  H.on("card", onCard);
  function refreshIfVisible() {
    if (view && state.visible) load();
    else render();
  }
  H.on("hub:people.changed", refreshIfVisible);
  H.on("people:list", render);
  H.on("people:migrated", refreshIfVisible);
  H.on("people:unlocked", refreshIfVisible);
  H.on("people:locked", function () {
    if (settings().personalLock) {
      state.list = [];
      state.locked = true;
      render();
    }
  });
  H.on("settings", function () {
    if (sectionView) sectionView.fill();
    render();
  });
  H.on("lang", function () {
    if (sectionView) sectionView.render();
    render();
  });
})();
