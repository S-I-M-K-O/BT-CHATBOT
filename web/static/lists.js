/*
 * HERMES lists UI (W2-J, SPEC 5.5 "lists.js", 9 Feature 4).
 *
 * Part 1 (UMD, no DOM): pure helpers for the browser (window.HermesLists)
 * and the node tests (require("../../static/lists.js")):
 *
 *   splitItems, foldText, listName, sortItems, openCount, listAsText,
 *   diffLists, makeSnapshot, parseSnapshot, snapshotView, formatStamp,
 *   pickLang, listsUrl, swipeDecision, format, STRINGS
 *
 * Part 2 (browser only): one list view used twice:
 *   - the hub tab "Listen" / "Lists" on the main page (window.Hermes tab
 *     contract, live updates from hub:list.changed);
 *   - the standalone phone page /lists.html (body[data-page="lists"]): its
 *     own EventSource on /api/events?scope=lists (works with the lists-only
 *     token cookie), a 30 s poll with ?since=rev, and an offline snapshot in
 *     localStorage that is shown read-only when the server is unreachable.
 *
 * List and item texts are user data: they are only ever set with
 * textContent. The only markup string is the static tab icon.
 */
(function (root, factory) {
  var api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root && typeof root === "object") root.HermesLists = api;
})(typeof window !== "undefined" ? window : typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  var ITEM_MAX = 200; // runes per item (SPEC 2.3)
  var NAME_MAX = 40; // runes per list name
  var BATCH_MAX = 50; // items per request
  var SNAPSHOT_KEY = "hermes-lists-snapshot";

  var STRINGS = {
    en: {
      tab: "Lists",
      pageTitle: "HERMES · Lists",
      heading: "Lists",
      shopping: "Shopping list",
      todo: "To-do list",
      addPlaceholder: "Add item …",
      addPlaceholderMany: "Add items, separated by commas",
      add: "Add",
      newList: "New list",
      newListName: "Name of the new list",
      rename: "Rename",
      renameTitle: "New name",
      create: "Create",
      save: "Save",
      cancel: "Cancel",
      deleteList: "Delete list",
      deleteListConfirm: "Delete the list “{name}” with all its items?",
      clearDone: "Remove checked",
      clearAll: "Clear list",
      clearAllConfirm: "Delete all {n} items from the {name}?",
      share: "Share",
      shareReady: "{name} is ready to share.",
      copied: "Copied – paste it into Notes or a messenger.",
      copyFailed: "Could not copy the list.",
      done: "Checked ({n})",
      empty: "Nothing here yet.",
      emptyHint: "Type above, or say “Hermes, put milk on the shopping list”.",
      deleteItem: "Delete {text}",
      editItem: "Edit {text}",
      markDone: "Check off {text}",
      deleted: "Deleted: {text}",
      removedDone: "Removed {n} checked items.",
      undo: "Undo",
      offline: "As of {time} (offline)",
      offlineNoData: "The server cannot be reached and there is no saved copy yet.",
      readOnly: "Offline: changes are not possible right now.",
      noAccess: "No access. Open the link with the lists token once: /lists.html?token=…",
      loading: "Loading …",
      errGeneric: "That did not work: {msg}",
      errNetwork: "The server cannot be reached.",
      errReadOnly: "The lists cannot be changed (data file from a newer HERMES).",
      errLimit: "Limit reached: at most 30 lists and 500 items per list.",
      errExists: "A list with this name already exists.",
      errName: "Please enter a name (at most 40 characters).",
      errText: "Please enter a text (at most 200 characters).",
      phoneHint: "On your phone: open {url} on the home network. The first time, add ?token= and the lists token (HERMES_LISTS_TOKEN).",
      phoneHintNoToken: "On your phone: {url} needs HERMES_LISTS_TOKEN (or the master token) on the server.",
      items: "{n} open",
      tabsLabel: "Lists",
      live: "live",
    },
    de: {
      tab: "Listen",
      pageTitle: "HERMES · Listen",
      heading: "Listen",
      shopping: "Einkaufsliste",
      todo: "To-do-Liste",
      addPlaceholder: "Eintrag hinzufügen …",
      addPlaceholderMany: "Einträge hinzufügen, mit Komma getrennt",
      add: "Hinzufügen",
      newList: "Neue Liste",
      newListName: "Name der neuen Liste",
      rename: "Umbenennen",
      renameTitle: "Neuer Name",
      create: "Anlegen",
      save: "Speichern",
      cancel: "Abbrechen",
      deleteList: "Liste löschen",
      deleteListConfirm: "Die Liste „{name}“ mit allen Einträgen löschen?",
      clearDone: "Erledigte entfernen",
      clearAll: "Liste leeren",
      clearAllConfirm: "Alle {n} Einträge der {name} löschen?",
      share: "Teilen",
      shareReady: "{name} ist bereit zum Teilen.",
      copied: "Kopiert – in Notizen oder Messenger einfügen",
      copyFailed: "Die Liste konnte nicht kopiert werden.",
      done: "Erledigt ({n})",
      empty: "Hier steht noch nichts.",
      emptyHint: "Tippe oben, oder sag „Hermes, setz Milch auf die Einkaufsliste“.",
      deleteItem: "{text} löschen",
      editItem: "{text} bearbeiten",
      markDone: "{text} abhaken",
      deleted: "Gelöscht: {text}",
      removedDone: "{n} erledigte Einträge entfernt.",
      undo: "Rückgängig",
      offline: "Stand: {time} (offline)",
      offlineNoData: "Der Server ist nicht erreichbar, und es gibt noch keine gespeicherte Kopie.",
      readOnly: "Offline: Änderungen sind gerade nicht möglich.",
      noAccess: "Kein Zugang. Öffne den Link einmal mit dem Listen-Token: /lists.html?token=…",
      loading: "Lädt …",
      errGeneric: "Das hat nicht geklappt: {msg}",
      errNetwork: "Der Server ist nicht erreichbar.",
      errReadOnly: "Die Listen können nicht geändert werden (Datendatei einer neueren HERMES-Version).",
      errLimit: "Grenze erreicht: höchstens 30 Listen und 500 Einträge pro Liste.",
      errExists: "Eine Liste mit diesem Namen gibt es schon.",
      errName: "Bitte gib einen Namen ein (höchstens 40 Zeichen).",
      errText: "Bitte gib einen Text ein (höchstens 200 Zeichen).",
      phoneHint: "Auf dem Handy: {url} im Heimnetz öffnen. Beim ersten Mal ?token= und das Listen-Token (HERMES_LISTS_TOKEN) anhängen.",
      phoneHintNoToken: "Auf dem Handy: {url} braucht HERMES_LISTS_TOKEN (oder das Haupt-Token) auf dem Server.",
      items: "{n} offen",
      tabsLabel: "Listen",
      live: "live",
    },
  };

  // ------------------------------------------------------------ text helpers

  function runes(s) {
    return Array.from(String(s == null ? "" : s));
  }

  function truncate(s, n) {
    var r = runes(s);
    return r.length > n ? r.slice(0, n).join("") : String(s);
  }

  /* Drop control characters (keeps \n and \t out too: items are one line). */
  function cleanLine(s) {
    return String(s == null ? "" : s)
      .replace(/[\u0000-\u001f\u007f-\u009f\u2028\u2029]/g, " ")
      .replace(/\s+/g, " ")
      .trim();
  }

  /* The duplicate key: case-folded, collapsed white space. */
  function foldText(s) {
    return cleanLine(s).toLowerCase();
  }

  function format(str, vars) {
    str = String(str == null ? "" : str);
    if (!vars) return str;
    return str.replace(/\{(\w+)\}/g, function (all, k) {
      return Object.prototype.hasOwnProperty.call(vars, k) && vars[k] != null ? String(vars[k]) : all;
    });
  }

  var SPLIT_WORDS = /\s*(?:,|;|&|\n|\s+(?:und|and|sowie)\s+)\s*/i;
  var SPLIT_TYPED = /\s*[,;\n]\s*/;
  var LEAD_WORDS = {
    und: 1, and: 1, sowie: 1, die: 1, der: 1, das: 1, den: 1, dem: 1, des: 1, ein: 1, eine: 1, einen: 1,
    einem: 1, einer: 1, etwas: 1, noch: 1, bitte: 1, auch: 1, mal: 1, some: 1, the: 1, a: 1, an: 1,
    any: 1, more: 1, also: 1, please: 1, too: 1,
  };
  var TAIL_WORDS = { bitte: 1, please: 1, noch: 1, auch: 1, too: 1, also: 1, danke: 1, thanks: 1, drauf: 1 };
  var TRIM_CHARS = /^[\s.,;:!?"'„“”‚‘’«»()]+|[\s.,;:!?"'„“”‚‘’«»()]+$/g;

  function upperFirst(s) {
    var r = runes(s);
    if (!r.length) return "";
    var up = r[0].toUpperCase();
    return up.length === 1 || up === r[0] ? up + r.slice(1).join("") : s;
  }

  /*
   * Split an item phrase into items (SPEC 9.4), mirroring lists.go.
   * opts.typed: text typed into the add box is split on , ; and new lines
   * only and kept as written. Otherwise (spoken phrasing) it is also split on
   * "und"/"and"/"sowie"/"&", articles and fillers are dropped, and German
   * items (opts.lang "de") are capitalised. At most 50 distinct items.
   */
  function splitItems(text, opts) {
    opts = opts || {};
    var parts = String(text == null ? "" : text).split(opts.typed ? SPLIT_TYPED : SPLIT_WORDS);
    var out = [];
    var seen = {};
    for (var i = 0; i < parts.length && out.length < BATCH_MAX; i++) {
      var s = cleanLine(parts[i]).replace(TRIM_CHARS, "");
      if (!opts.typed) {
        var words = s.split(" ").filter(Boolean);
        while (words.length > 1 && LEAD_WORDS[words[0].toLowerCase()]) words.shift();
        while (words.length > 1 && TAIL_WORDS[words[words.length - 1].toLowerCase()]) words.pop();
        s = words.length === 1 && LEAD_WORDS[words[0].toLowerCase()] ? "" : words.join(" ");
        if (opts.lang === "de") s = upperFirst(s);
      }
      s = truncate(s, ITEM_MAX).trim();
      var key = foldText(s);
      if (!key || seen[key]) continue;
      seen[key] = true;
      out.push(s);
    }
    return out;
  }

  // ------------------------------------------------------------ lists

  function isBuiltin(list) {
    return !!list && (list.id === "shopping" || list.id === "todo");
  }

  /* Display name: built-ins per language, custom lists as named. */
  function listName(list, lang) {
    if (!list) return "";
    var t = STRINGS[lang === "de" ? "de" : "en"];
    if (list.id === "shopping") return t.shopping;
    if (list.id === "todo") return t.todo;
    return cleanLine(list.name) || "?";
  }

  function itemsOf(list) {
    return list && Array.isArray(list.items) ? list.items.filter(function (it) {
      return it && typeof it.id === "string" && typeof it.text === "string";
    }) : [];
  }

  /* Open items in list order, then done items (most recently done first). */
  function sortItems(items) {
    var open = [];
    var done = [];
    (Array.isArray(items) ? items : []).forEach(function (it, i) {
      if (!it) return;
      (it.done ? done : open).push({ it: it, i: i });
    });
    done.sort(function (a, b) {
      var ta = Date.parse(a.it.doneAt || "") || 0;
      var tb = Date.parse(b.it.doneAt || "") || 0;
      return tb - ta || a.i - b.i;
    });
    return open.concat(done).map(function (x) {
      return x.it;
    });
  }

  function openCount(list) {
    return itemsOf(list).filter(function (it) {
      return !it.done;
    }).length;
  }

  /* Plain-text share format: "Einkaufsliste:\n- Milch\n- Eier" (open items). */
  function listAsText(list, lang) {
    var lines = [listName(list, lang) + ":"];
    itemsOf(list).forEach(function (it) {
      if (!it.done) lines.push("- " + cleanLine(it.text));
    });
    return lines.join("\n");
  }

  /*
   * What changed between two GET /api/lists results: item ids that are new,
   * changed (text or done) or gone, and list ids that appeared or vanished.
   * Used to highlight items another device just added.
   */
  function diffLists(prev, next) {
    var out = { addedItems: [], changedItems: [], removedItems: [], addedLists: [], removedLists: [] };
    var before = {};
    var beforeLists = {};
    (Array.isArray(prev) ? prev : []).forEach(function (l) {
      if (!l || typeof l.id !== "string") return;
      beforeLists[l.id] = true;
      itemsOf(l).forEach(function (it) {
        before[it.id] = it;
      });
    });
    var seenLists = {};
    var seen = {};
    (Array.isArray(next) ? next : []).forEach(function (l) {
      if (!l || typeof l.id !== "string") return;
      seenLists[l.id] = true;
      if (!beforeLists[l.id]) out.addedLists.push(l.id);
      itemsOf(l).forEach(function (it) {
        seen[it.id] = true;
        var old = before[it.id];
        if (!old) out.addedItems.push(it.id);
        else if (old.text !== it.text || !!old.done !== !!it.done) out.changedItems.push(it.id);
      });
    });
    Object.keys(before).forEach(function (id) {
      if (!seen[id]) out.removedItems.push(id);
    });
    Object.keys(beforeLists).forEach(function (id) {
      if (!seenLists[id]) out.removedLists.push(id);
    });
    return out;
  }

  // ------------------------------------------------------------ snapshot

  /* What the phone page stores after every successful load. */
  function makeSnapshot(data, now) {
    return { rev: Number(data && data.rev) || 0, lists: (data && data.lists) || [], at: Number(now) || Date.now() };
  }

  /* Validate a stored snapshot (string or object); null when unusable. */
  function parseSnapshot(raw) {
    var s = raw;
    if (typeof raw === "string") {
      try {
        s = JSON.parse(raw);
      } catch (e) {
        return null;
      }
    }
    if (!s || typeof s !== "object" || !Array.isArray(s.lists) || typeof s.at !== "number" || !isFinite(s.at)) return null;
    var lists = s.lists
      .filter(function (l) {
        return l && typeof l === "object" && typeof l.id === "string" && l.id;
      })
      .map(function (l) {
        return {
          id: l.id,
          name: typeof l.name === "string" ? truncate(l.name, NAME_MAX) : "",
          kind: typeof l.kind === "string" ? l.kind : "custom",
          items: itemsOf(l).map(function (it) {
            return { id: it.id, text: truncate(it.text, ITEM_MAX), done: !!it.done, doneAt: typeof it.doneAt === "string" ? it.doneAt : undefined };
          }),
        };
      });
    return { rev: Number(s.rev) || 0, lists: lists, at: s.at };
  }

  function pad2(n) {
    return (n < 10 ? "0" : "") + n;
  }

  /*
   * "Stand: 08:12 (offline)" / "As of 08:12 (offline)"; an older copy also
   * names the day: "Stand: 6.11., 08:12 (offline)" / "As of 11/6, 08:12".
   */
  function formatStamp(at, lang, now) {
    var d = new Date(at);
    if (isNaN(d.getTime())) return "";
    var n = new Date(now == null ? Date.now() : now);
    var time = pad2(d.getHours()) + ":" + pad2(d.getMinutes());
    var sameDay = d.getFullYear() === n.getFullYear() && d.getMonth() === n.getMonth() && d.getDate() === n.getDate();
    if (!sameDay) {
      time = (lang === "de" ? d.getDate() + "." + (d.getMonth() + 1) + "." : d.getMonth() + 1 + "/" + d.getDate()) + ", " + time;
    }
    return format(STRINGS[lang === "de" ? "de" : "en"].offline, { time: time });
  }

  /* The read-only offline rendering of a snapshot. */
  function snapshotView(snap, lang, now) {
    var s = parseSnapshot(snap);
    if (!s) return null;
    return { rev: s.rev, lists: s.lists, stamp: formatStamp(s.at, lang, now), readOnly: true };
  }

  /* Standalone language: ?lang=de|en wins, else the browser language. */
  function pickLang(search, navLang) {
    var m = /[?&]lang=([a-zA-Z-]+)/.exec(String(search || ""));
    var l = m ? m[1] : String(navLang || "");
    return /^de\b/i.test(l) ? "de" : "en";
  }

  /* GET URL: ?since=<rev> once data is loaded (204 when nothing changed). */
  function listsUrl(since) {
    return typeof since === "number" && isFinite(since) && since >= 0 ? "/api/lists?since=" + Math.floor(since) : "/api/lists";
  }

  /* A horizontal swipe far enough to the left deletes; vertical scrolls win. */
  function swipeDecision(dx, dy, width) {
    var threshold = Math.max(80, Math.min(160, (Number(width) || 320) * 0.35));
    if (Math.abs(dy) > Math.abs(dx)) return null;
    return dx <= -threshold ? "delete" : null;
  }

  return {
    ITEM_MAX: ITEM_MAX,
    NAME_MAX: NAME_MAX,
    BATCH_MAX: BATCH_MAX,
    SNAPSHOT_KEY: SNAPSHOT_KEY,
    STRINGS: STRINGS,
    format: format,
    foldText: foldText,
    cleanLine: cleanLine,
    truncate: truncate,
    splitItems: splitItems,
    isBuiltin: isBuiltin,
    listName: listName,
    itemsOf: itemsOf,
    sortItems: sortItems,
    openCount: openCount,
    listAsText: listAsText,
    diffLists: diffLists,
    makeSnapshot: makeSnapshot,
    parseSnapshot: parseSnapshot,
    formatStamp: formatStamp,
    snapshotView: snapshotView,
    pickLang: pickLang,
    listsUrl: listsUrl,
    swipeDecision: swipeDecision,
  };
});

/* ====================================================================== */
/* Part 2: browser UI                                                      */
/* ====================================================================== */
(function () {
  "use strict";
  if (typeof window === "undefined" || !window.document) return;
  var L = window.HermesLists;
  if (!L) return;
  var H = window.Hermes || null;
  var doc = window.document;
  var standalone = !!(doc.body && doc.body.getAttribute("data-page") === "lists");

  var POLL_MS = 30000;
  var suppressClickUntil = 0; // a swipe must not also open the editor
  var CURRENT_KEY = "hermes-lists-current";

  // ---------------------------------------------------------------- language

  var pageLang = standalone ? L.pickLang(window.location.search, navigator.language) : "";
  if (standalone) doc.documentElement.lang = pageLang;

  function lang() {
    if (standalone) return pageLang;
    try {
      if (H && H.api && typeof H.api.lang === "function") return H.api.lang() === "de" ? "de" : "en";
      if (H && typeof H.lang === "function") return H.lang();
    } catch (e) {
      /* fall through */
    }
    return "en";
  }
  function tr(key, vars) {
    var t = L.STRINGS[lang()] || L.STRINGS.en;
    return L.format(Object.prototype.hasOwnProperty.call(t, key) ? t[key] : L.STRINGS.en[key] || key, vars);
  }

  // ---------------------------------------------------------------- storage

  function storeGet(key) {
    try {
      return window.localStorage.getItem(key);
    } catch (e) {
      return null;
    }
  }
  function storeSet(key, value) {
    try {
      window.localStorage.setItem(key, value);
    } catch (e) {
      /* quota or blocked: the page still works */
    }
  }
  function storeRemove(key) {
    try {
      window.localStorage.removeItem(key);
    } catch (e) {
      /* blocked storage: nothing was stored either */
    }
  }

  // ---------------------------------------------------------------- network

  /* JSON in and out; resolves {status, data}. 204 resolves data null. */
  function request(url, o) {
    o = o || {};
    var init = {
      method: o.method || "GET",
      headers: { Accept: "application/json" },
      credentials: "same-origin",
      cache: "no-store",
    };
    if (o.body !== undefined) {
      init.headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(o.body);
    }
    return fetch(url, init).then(function (res) {
      if (res.status === 204) return { status: 204, data: null };
      return res.text().then(function (txt) {
        var data = null;
        try {
          data = txt ? JSON.parse(txt) : null;
        } catch (e) {
          data = null;
        }
        if (!res.ok) {
          var err = new Error(data && typeof data.error === "string" ? data.error : "HTTP " + res.status);
          err.status = res.status;
          err.code = data && typeof data.code === "string" ? data.code : "";
          throw err;
        }
        return { status: res.status, data: data };
      });
    });
  }

  function errText(err) {
    if (!err || !err.status) return tr("errNetwork");
    switch (err.code) {
      case "read_only":
        return tr("errReadOnly");
      case "limit":
        return tr("errLimit");
      case "exists":
        return tr("errExists");
      case "name":
        return tr("errName");
      case "text":
        return tr("errText");
    }
    return tr("errGeneric", { msg: err.message || "?" });
  }

  // ---------------------------------------------------------------- DOM helpers

  function el(tag, attrs, kids) {
    var e = doc.createElement(tag);
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
        else e.setAttribute(k, v === true ? "" : v);
      }
    }
    if (kids != null) {
      if (!Array.isArray(kids)) kids = [kids];
      for (var i = 0; i < kids.length; i++) {
        var c = kids[i];
        if (c == null || c === false) continue;
        e.appendChild(typeof c === "string" ? doc.createTextNode(c) : c);
      }
    }
    return e;
  }
  function clear(node) {
    while (node && node.firstChild) node.removeChild(node.firstChild);
  }

  // Static, trusted icon markup (never user data).
  var ICON_TAB =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 6h11M9 12h11M9 18h11"/><path d="M4 6l1 1 2-2M4 12l1 1 2-2"/><circle cx="5" cy="18" r="1"/></svg>';

  // ---------------------------------------------------------------- toasts

  var ownToasts = null;
  function toast(msg, o) {
    o = o || {};
    if (!standalone && H && H.api && typeof H.api.toast === "function") return H.api.toast(msg, o);
    if (!ownToasts) {
      ownToasts = el("div", { class: "lists-toasts", "aria-live": "polite" });
      doc.body.appendChild(ownToasts);
    }
    var box = el("div", { class: "lists-toast " + (o.type || "info"), role: o.type === "error" ? "alert" : "status" }, [el("span", { text: msg })]);
    var timer = 0;
    function close() {
      clearTimeout(timer);
      if (box.parentNode) box.parentNode.removeChild(box);
    }
    if (o.action && typeof o.action.fn === "function") {
      box.appendChild(
        el("button", {
          type: "button",
          class: "lists-toast-action",
          text: o.action.label,
          onclick: function () {
            close();
            o.action.fn();
          },
        })
      );
    }
    while (ownToasts.childNodes.length >= 3) ownToasts.removeChild(ownToasts.firstChild);
    ownToasts.appendChild(box);
    timer = setTimeout(close, o.ms || 5000);
    return { close: close };
  }

  // ---------------------------------------------------------------- share

  function copyText(text) {
    var ta = el("textarea", { class: "lists-copy", readonly: true, "aria-hidden": "true" });
    ta.value = text;
    doc.body.appendChild(ta);
    var ok = false;
    try {
      ta.select();
      ta.setSelectionRange(0, text.length);
      ok = doc.execCommand("copy");
    } catch (e) {
      ok = false;
    }
    doc.body.removeChild(ta);
    return ok;
  }

  /*
   * Web Share where the browser offers it (secure contexts only: localhost,
   * HTTPS, Tailscale Serve), else copy as plain text.
   */
  function shareText(title, text) {
    function fallback() {
      if (copyText(text)) toast(tr("copied"), { type: "success" });
      else toast(tr("copyFailed"), { type: "error" });
    }
    if (window.isSecureContext && navigator.share) {
      navigator.share({ title: title, text: text }).catch(function (e) {
        if (!e || e.name !== "AbortError") fallback();
      });
      return;
    }
    fallback();
  }

  // ---------------------------------------------------------------- the view

  function createView(root) {
    var state = {
      rev: null,
      lists: [],
      current: storeGet(CURRENT_KEY) || "shopping",
      loaded: false,
      offline: false,
      noAccess: false,
      stamp: "",
      naming: null, // {mode: "new"|"rename", id}
      editing: "",
      flash: {},
      at: 0, // time of the data shown (server load or snapshot)
      loadSeq: 0,
      busy: 0,
    };
    var addInput = el("input", {
      type: "text",
      class: "lists-add-input",
      maxlength: String(L.ITEM_MAX * 4),
      autocomplete: "off",
      autocapitalize: "sentences",
      enterkeyhint: "done",
    });
    var nameInput = el("input", { type: "text", class: "lists-name-input", maxlength: String(L.NAME_MAX), autocomplete: "off", enterkeyhint: "done" });

    function currentList() {
      for (var i = 0; i < state.lists.length; i++) if (state.lists[i].id === state.current) return state.lists[i];
      return state.lists[0] || null;
    }
    function readOnly() {
      return state.offline || state.noAccess;
    }

    // ------------------------------------------------------------ data

    function apply(data, fromServer) {
      var lists = (data && Array.isArray(data.lists) ? data.lists : []).filter(function (l) {
        return l && typeof l.id === "string";
      });
      if (state.loaded) {
        var d = L.diffLists(state.lists, lists);
        d.addedItems.concat(d.changedItems).forEach(function (id) {
          state.flash[id] = true;
        });
      }
      state.lists = lists;
      state.rev = typeof data.rev === "number" ? data.rev : 0;
      state.loaded = true;
      if (!currentList() || currentList().id !== state.current) {
        state.current = lists.length ? lists[0].id : "shopping";
      }
      if (fromServer) {
        state.at = Date.now();
        if (standalone) storeSet(L.SNAPSHOT_KEY, JSON.stringify(L.makeSnapshot(data, state.at)));
      }
    }

    /*
     * The server is unreachable: keep what is shown, or fall back to the
     * stored snapshot, read-only with its time. The server refuses this
     * device (401: no, wrong or revoked token): show nothing and drop the
     * stored copy, so revoking a lost phone's token also hides the lists
     * there (the snapshot is for an unreachable server only, SPEC 5.5).
     */
    function goOffline(noAccess) {
      state.offline = !noAccess;
      state.noAccess = !!noAccess;
      if (noAccess) {
        if (standalone) storeRemove(L.SNAPSHOT_KEY);
        state.lists = [];
        state.rev = null;
        state.loaded = false;
        state.at = 0;
        state.stamp = "";
        state.naming = null;
        state.editing = "";
        return;
      }
      if (!state.loaded && standalone) {
        var raw = storeGet(L.SNAPSHOT_KEY);
        var snap = L.snapshotView(raw, lang(), Date.now());
        if (snap) {
          state.lists = snap.lists;
          state.rev = snap.rev;
          state.at = L.parseSnapshot(raw).at;
          state.loaded = true;
          if (!state.lists.some(function (l) { return l.id === state.current; })) state.current = state.lists.length ? state.lists[0].id : "shopping";
        }
      }
      state.stamp = state.loaded && state.at ? L.formatStamp(state.at, lang(), Date.now()) : "";
    }

    function load(force) {
      var seq = ++state.loadSeq;
      var since = !force && state.loaded && !state.offline && typeof state.rev === "number" ? state.rev : null;
      return request(L.listsUrl(since))
        .then(function (res) {
          if (seq !== state.loadSeq) return;
          var wasOffline = state.offline || state.noAccess;
          state.offline = false;
          state.noAccess = false;
          state.stamp = "";
          if (res.status === 204) {
            state.at = Date.now();
            if (wasOffline) render();
            return;
          }
          if (res.data) apply(res.data, true);
          render();
          state.flash = {};
        })
        .catch(function (err) {
          if (seq !== state.loadSeq) return;
          goOffline(err && err.status === 401);
          if (!standalone && err && err.status && err.status !== 401) toast(errText(err), { type: "error", key: "lists-load" });
          render();
        });
    }

    /* Run a change: optimistic local edit, request, then reconcile. */
    function mutate(method, url, body, optimistic) {
      if (readOnly()) {
        toast(tr("readOnly"), { type: "warn" });
        return Promise.reject(new Error("offline"));
      }
      if (optimistic) {
        optimistic();
        render();
      }
      state.busy++;
      return request(url, { method: method, body: body })
        .then(function (res) {
          return res.data;
        })
        .catch(function (err) {
          toast(errText(err), { type: "error" });
          throw err;
        })
        .finally(function () {
          state.busy--;
          load(true);
        });
    }

    function base(list) {
      return "/api/lists/" + encodeURIComponent(list.id);
    }

    function addItems(list, raw) {
      var texts = L.splitItems(raw, { typed: true });
      if (!texts.length) return;
      addInput.value = "";
      var tmp = texts.map(function (t, i) {
        return { id: "tmp-" + Date.now() + "-" + i, text: t, done: false, pending: true };
      });
      mutate("POST", base(list) + "/items", { texts: texts }, function () {
        list.items = L.itemsOf(list).concat(
          tmp.filter(function (t) {
            return !L.itemsOf(list).some(function (it) {
              return !it.done && L.foldText(it.text) === L.foldText(t.text);
            });
          })
        );
      }).catch(function () {
        if (!addInput.value) addInput.value = raw; // keep what was typed
      });
    }

    function toggle(list, item) {
      var done = !item.done;
      mutate("PATCH", base(list) + "/items/" + encodeURIComponent(item.id), { done: done }, function () {
        item.done = done;
        item.doneAt = done ? new Date().toISOString() : undefined;
      }).catch(function () {});
    }

    function removeItem(list, item) {
      mutate("DELETE", base(list) + "/items/" + encodeURIComponent(item.id), undefined, function () {
        list.items = L.itemsOf(list).filter(function (it) {
          return it.id !== item.id;
        });
      })
        .then(function () {
          toast(tr("deleted", { text: L.truncate(item.text, 60) }), {
            ms: 6000,
            action: {
              label: tr("undo"),
              fn: function () {
                request(base(list) + "/items", { method: "POST", body: { text: item.text } })
                  .then(function (res) {
                    var back = res.data && res.data.items && res.data.items[0];
                    if (item.done && back) {
                      return request(base(list) + "/items/" + encodeURIComponent(back.id), { method: "PATCH", body: { done: true } });
                    }
                  })
                  .catch(function (err) {
                    toast(errText(err), { type: "error" });
                  })
                  .then(function () {
                    load(true);
                  });
              },
            },
          });
        })
        .catch(function () {});
    }

    function saveEdit(list, item, value) {
      state.editing = "";
      var text = L.cleanLine(value);
      if (!text || text === item.text) {
        render();
        return;
      }
      mutate("PATCH", base(list) + "/items/" + encodeURIComponent(item.id), { text: L.truncate(text, L.ITEM_MAX) }, function () {
        item.text = text;
      }).catch(function () {});
    }

    function clearDone(list) {
      var n = L.itemsOf(list).filter(function (it) {
        return it.done;
      }).length;
      mutate("POST", base(list) + "/clear", { doneOnly: true }, function () {
        list.items = L.itemsOf(list).filter(function (it) {
          return !it.done;
        });
      })
        .then(function () {
          toast(tr("removedDone", { n: n }), { type: "success" });
        })
        .catch(function () {});
    }

    function clearAll(list) {
      var n = L.itemsOf(list).length;
      if (!window.confirm(tr("clearAllConfirm", { n: n, name: L.listName(list, lang()) }))) return;
      mutate("POST", base(list) + "/clear", { doneOnly: false }, function () {
        list.items = [];
      }).catch(function () {});
    }

    function deleteList(list) {
      if (!window.confirm(tr("deleteListConfirm", { name: L.listName(list, lang()) }))) return;
      mutate("DELETE", base(list), undefined, function () {
        state.lists = state.lists.filter(function (l) {
          return l.id !== list.id;
        });
        state.current = "shopping";
      }).catch(function () {});
    }

    function submitName() {
      var name = L.cleanLine(nameInput.value);
      var naming = state.naming;
      if (!naming) return;
      if (!name || Array.from(name).length > L.NAME_MAX) {
        toast(tr("errName"), { type: "warn" });
        return;
      }
      var p =
        naming.mode === "new"
          ? mutate("POST", "/api/lists", { name: name })
          : mutate("PATCH", "/api/lists/" + encodeURIComponent(naming.id), { name: name });
      p.then(function (l) {
        state.naming = null;
        nameInput.value = "";
        if (l && l.id) select(l.id);
      }).catch(function () {});
    }

    function select(id) {
      state.current = id;
      state.editing = "";
      storeSet(CURRENT_KEY, id);
      render();
    }

    function share(list) {
      shareText(L.listName(list, lang()), L.listAsText(list, lang()));
    }

    // ------------------------------------------------------------ swipe

    function attachSwipe(row, onDelete) {
      var start = null;
      row.addEventListener("pointerdown", function (e) {
        if (readOnly() || (e.pointerType !== "touch" && e.pointerType !== "pen")) return;
        if (e.target && e.target.closest && e.target.closest("input, .lists-del")) return;
        start = { x: e.clientX, y: e.clientY, id: e.pointerId };
      });
      row.addEventListener("pointermove", function (e) {
        if (!start || e.pointerId !== start.id) return;
        var dx = e.clientX - start.x;
        var dy = e.clientY - start.y;
        if (Math.abs(dy) > Math.abs(dx) && Math.abs(dy) > 12) {
          start = null;
          row.style.transform = "";
          return;
        }
        if (Math.abs(dx) > 10) suppressClickUntil = Date.now() + 400;
        if (dx < 0) row.style.transform = "translateX(" + Math.max(dx, -row.offsetWidth) + "px)";
      });
      function end(e) {
        if (!start || e.pointerId !== start.id) return;
        var decision = L.swipeDecision(e.clientX - start.x, e.clientY - start.y, row.offsetWidth);
        start = null;
        row.style.transform = "";
        if (decision === "delete") onDelete();
      }
      row.addEventListener("pointerup", end);
      row.addEventListener("pointercancel", function () {
        start = null;
        row.style.transform = "";
      });
    }

    // ------------------------------------------------------------ render

    function renderTabs() {
      var bar = el("div", { class: "lists-tabs", role: "tablist", "aria-label": tr("tabsLabel") });
      state.lists.forEach(function (l) {
        var sel = l.id === state.current;
        var n = L.openCount(l);
        bar.appendChild(
          el(
            "button",
            {
              type: "button",
              class: "lists-tab" + (sel ? " active" : ""),
              role: "tab",
              "aria-selected": sel ? "true" : "false",
              onclick: function () {
                select(l.id);
              },
            },
            [el("span", { class: "lists-tab-name", text: L.listName(l, lang()) }), n ? el("span", { class: "lists-count", text: String(n), "aria-label": tr("items", { n: n }) }) : null]
          )
        );
      });
      if (!readOnly()) {
        bar.appendChild(
          el("button", {
            type: "button",
            class: "lists-tab lists-tab-new",
            "aria-label": tr("newList"),
            title: tr("newList"),
            text: "+",
            onclick: function () {
              state.naming = { mode: "new" };
              nameInput.value = "";
              render();
              nameInput.focus();
            },
          })
        );
      }
      return bar;
    }

    function renderNaming() {
      var n = state.naming;
      nameInput.placeholder = n.mode === "new" ? tr("newListName") : tr("renameTitle");
      nameInput.setAttribute("aria-label", nameInput.placeholder);
      return el(
        "form",
        {
          class: "lists-naming",
          onsubmit: function (e) {
            e.preventDefault();
            submitName();
          },
        },
        [
          nameInput,
          el("button", { type: "submit", class: "lists-btn primary", text: n.mode === "new" ? tr("create") : tr("save") }),
          el("button", {
            type: "button",
            class: "lists-btn ghost",
            text: tr("cancel"),
            onclick: function () {
              state.naming = null;
              render();
            },
          }),
        ]
      );
    }

    function renderItem(list, item) {
      var label = L.cleanLine(item.text);
      var row = el("li", { class: "lists-item" + (item.done ? " done" : "") + (state.flash[item.id] ? " flash" : "") + (item.pending ? " pending" : "") });
      var box = el("input", {
        type: "checkbox",
        class: "lists-check",
        checked: !!item.done,
        disabled: readOnly() || item.pending,
        "aria-label": tr("markDone", { text: label }),
        onchange: function () {
          toggle(list, item);
        },
      });
      row.appendChild(el("label", { class: "lists-check-wrap" }, [box]));
      if (state.editing === item.id && !readOnly()) {
        var input = el("input", { type: "text", class: "lists-edit-input", value: item.text, maxlength: String(L.ITEM_MAX), "aria-label": tr("editItem", { text: label }), enterkeyhint: "done" });
        var finished = false;
        var finish = function (save) {
          if (finished) return;
          finished = true;
          if (save) saveEdit(list, item, input.value);
          else {
            state.editing = "";
            render();
          }
        };
        input.addEventListener("keydown", function (e) {
          if (e.key === "Enter") {
            e.preventDefault();
            finish(true);
          } else if (e.key === "Escape") {
            e.preventDefault();
            e.stopPropagation();
            finish(false);
          }
        });
        input.addEventListener("blur", function () {
          finish(true);
        });
        row.appendChild(input);
        setTimeout(function () {
          input.focus();
        }, 0);
      } else {
        row.appendChild(
          el("button", {
            type: "button",
            class: "lists-text",
            text: label,
            title: readOnly() ? null : tr("editItem", { text: label }),
            disabled: readOnly() || item.pending,
            onclick: function () {
              if (Date.now() < suppressClickUntil) return;
              state.editing = item.id;
              render();
            },
          })
        );
      }
      if (!readOnly() && !item.pending) {
        row.appendChild(
          el("button", {
            type: "button",
            class: "lists-del",
            "aria-label": tr("deleteItem", { text: label }),
            title: tr("deleteItem", { text: label }),
            text: "×",
            onclick: function () {
              removeItem(list, item);
            },
          })
        );
        attachSwipe(row, function () {
          removeItem(list, item);
        });
      }
      return row;
    }

    function renderList(list) {
      var wrap = el("section", { class: "lists-panel", role: "tabpanel", "aria-label": L.listName(list, lang()) });
      if (!readOnly()) {
        addInput.placeholder = tr("addPlaceholder");
        addInput.setAttribute("aria-label", tr("addPlaceholderMany"));
        wrap.appendChild(
          el(
            "form",
            {
              class: "lists-add",
              onsubmit: function (e) {
                e.preventDefault();
                addItems(list, addInput.value);
              },
            },
            [addInput, el("button", { type: "submit", class: "lists-btn primary lists-add-btn", "aria-label": tr("add"), title: tr("add"), text: "+" })]
          )
        );
      }
      var items = L.sortItems(L.itemsOf(list));
      var open = items.filter(function (it) {
        return !it.done;
      });
      var done = items.filter(function (it) {
        return it.done;
      });
      if (!items.length) {
        wrap.appendChild(el("p", { class: "lists-empty" }, [tr("empty"), readOnly() ? null : el("br"), readOnly() ? null : el("span", { class: "lists-hint", text: tr("emptyHint") })]));
      }
      if (open.length) {
        var ul = el("ul", { class: "lists-items" });
        open.forEach(function (it) {
          ul.appendChild(renderItem(list, it));
        });
        wrap.appendChild(ul);
      }
      if (done.length) {
        wrap.appendChild(el("h3", { class: "lists-done-head", text: tr("done", { n: done.length }) }));
        var dl = el("ul", { class: "lists-items lists-items-done" });
        done.forEach(function (it) {
          dl.appendChild(renderItem(list, it));
        });
        wrap.appendChild(dl);
      }
      var actions = [];
      if (open.length) actions.push(el("button", { type: "button", class: "lists-btn", text: tr("share"), onclick: function () { share(list); } }));
      if (!readOnly()) {
        if (done.length) actions.push(el("button", { type: "button", class: "lists-btn", text: tr("clearDone"), onclick: function () { clearDone(list); } }));
        if (items.length) actions.push(el("button", { type: "button", class: "lists-btn ghost", text: tr("clearAll"), onclick: function () { clearAll(list); } }));
        if (!L.isBuiltin(list)) {
          actions.push(
            el("button", {
              type: "button",
              class: "lists-btn ghost",
              text: tr("rename"),
              onclick: function () {
                state.naming = { mode: "rename", id: list.id };
                nameInput.value = list.name || "";
                render();
                nameInput.focus();
              },
            })
          );
          actions.push(el("button", { type: "button", class: "lists-btn danger", text: tr("deleteList"), onclick: function () { deleteList(list); } }));
        }
      }
      if (actions.length) wrap.appendChild(el("div", { class: "lists-actions" }, actions));
      return wrap;
    }

    function phoneHint() {
      if (standalone) return null;
      // From the Mac's own browser "localhost" means nothing to a phone.
      var loc = window.location;
      var host = /^(localhost|127\.|\[::1\])/.test(loc.host) ? "<IP>" + (loc.port ? ":" + loc.port : "") : loc.host;
      var url = loc.protocol + "//" + host + "/lists.html";
      var tokenSet = false;
      try {
        var st = H && H.api && H.api.status ? H.api.status() : null;
        tokenSet = !!(st && st.features && st.features.lists && st.features.lists.phoneToken);
      } catch (e) {
        tokenSet = false;
      }
      return el("p", { class: "lists-phone", text: tr(tokenSet ? "phoneHint" : "phoneHintNoToken", { url: url }) });
    }

    function render() {
      var focused = doc.activeElement;
      var keepAdd = focused === addInput;
      var keepName = focused === nameInput;
      clear(root);
      if (standalone) {
        root.appendChild(
          el("header", { class: "lists-header" }, [
            el("h1", { class: "lists-title" }, [el("span", { class: "lists-brand", text: "HERMES" }), " " + tr("heading")]),
            el("span", { class: "lists-state" + (readOnly() ? " off" : " on"), text: readOnly() ? state.stamp || "offline" : tr("live") }),
          ])
        );
        doc.title = tr("pageTitle");
      }
      if (state.noAccess) root.appendChild(el("p", { class: "lists-note warn", role: "alert", text: tr("noAccess") }));
      else if (state.offline) {
        // The phone header already shows the time of the copy.
        var note = !state.stamp ? tr("offlineNoData") : standalone ? tr("readOnly") : state.stamp + " · " + tr("readOnly");
        root.appendChild(el("p", { class: "lists-note warn", role: "status", text: note }));
      }
      if (!state.loaded) {
        if (!state.offline && !state.noAccess) root.appendChild(el("p", { class: "lists-note", text: tr("loading") }));
        return;
      }
      root.appendChild(renderTabs());
      if (state.naming && !readOnly()) root.appendChild(renderNaming());
      var list = currentList();
      if (list) root.appendChild(renderList(list));
      var hint = phoneHint();
      if (hint) root.appendChild(hint);
      if (keepAdd && addInput.isConnected) addInput.focus();
      if (keepName && nameInput.isConnected) nameInput.focus();
    }

    return {
      load: load,
      render: render,
      rev: function () {
        return state.rev;
      },
      busy: function () {
        return state.busy > 0;
      },
      byId: function (id) {
        for (var i = 0; i < state.lists.length; i++) if (state.lists[i].id === id) return state.lists[i];
        return null;
      },
    };
  }

  // ---------------------------------------------------------------- live updates

  /* Reload when another device changed something (rev differs). */
  function onChanged(view, ev) {
    var rev = ev && ev.data && typeof ev.data.rev === "number" ? ev.data.rev : null;
    if (rev !== null && rev === view.rev()) return;
    view.load();
  }

  function startPolling(view, active) {
    setInterval(function () {
      if (doc.visibilityState === "hidden" || !active()) return;
      view.load();
    }, POLL_MS);
    doc.addEventListener("visibilitychange", function () {
      if (doc.visibilityState === "visible" && active()) view.load();
    });
    window.addEventListener("online", function () {
      if (active()) view.load();
    });
  }

  // ---------------------------------------------------------------- standalone page

  function startStandalone() {
    var root = doc.getElementById("listsApp") || doc.body.appendChild(el("main", { id: "listsApp" }));
    root.classList.add("lists", "lists-standalone");
    var view = createView(root);
    view.render();
    view.load();
    startPolling(view, function () {
      return true;
    });
    // Own lists-only event stream (the master-token page uses hermes.js).
    if (typeof window.EventSource === "function") {
      var es = null;
      var retry = 0;
      var connect = function () {
        clearTimeout(retry);
        try {
          es = new window.EventSource("/api/events?scope=lists");
        } catch (e) {
          return;
        }
        es.addEventListener("list.changed", function (e) {
          var ev = null;
          try {
            ev = JSON.parse(e.data);
          } catch (err) {
            ev = null;
          }
          onChanged(view, ev);
        });
        // hello arrives on every (re)connect: catch up on missed changes.
        es.addEventListener("hello", function () {
          view.load();
        });
        es.onerror = function () {
          if (es && es.readyState === 2) {
            es.close();
            retry = setTimeout(connect, POLL_MS);
          }
        };
      };
      connect();
    }
  }

  // ---------------------------------------------------------------- hub tab

  function startHubTab() {
    if (!H || typeof H.registerTab !== "function") return;
    var view = null;
    var visible = false;
    var stale = false;
    H.registerTab({
      id: "lists",
      order: 30,
      label: { en: L.STRINGS.en.tab, de: L.STRINGS.de.tab },
      icon: ICON_TAB,
      mount: function (el) {
        el.classList.add("lists", "lists-hub");
        view = createView(el);
        view.render();
      },
      show: function () {
        visible = true;
        if (view) view.load(stale);
        stale = false;
      },
      hide: function () {
        visible = false;
      },
    });
    H.on("hub:list.changed", function (ev) {
      if (view && visible) onChanged(view, ev);
      else stale = true;
    });
    H.on("card", function (card) {
      if (!card || card.kind !== "list" || !card.data || typeof card.data !== "object") return;
      var d = card.data;
      if (view && visible) view.load();
      else stale = true;
      if (d.share === true && typeof d.text === "string") {
        var name = L.truncate(L.cleanLine(d.name), 60);
        var text = L.truncate(String(d.text), 20000);
        toast(tr("shareReady", { name: name }), {
          ms: 20000,
          key: "lists-share",
          action: {
            label: tr("share"),
            fn: function () {
              shareText(name, text);
            },
          },
        });
      }
    });
    H.on("lang", function () {
      if (view) view.render();
    });
    H.on("status", function () {
      if (view && visible) view.render();
    });
    startPolling(
      { load: function () { if (view) view.load(); }, rev: function () { return view ? view.rev() : null; } },
      function () {
        return visible;
      }
    );
  }

  if (standalone) startStandalone();
  else startHubTab();
})();
