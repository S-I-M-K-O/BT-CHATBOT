/*
 * HERMES documents page (/documents): send files to HERMES from a phone or
 * another PC, and see or delete the documents in the hub.
 *
 * The text is extracted here in the browser with window.HermesDocs (docs.js:
 * pdf.js for PDF, File.text() for .txt/.md); only the text goes to
 * POST /api/docs. Names come from users: only ever set with textContent.
 */
(function () {
  "use strict";
  var D = window.HermesDocs;
  var root = document.getElementById("docsApp");
  if (!D || !root) return;

  var de = /^de\b/i.test(navigator.language || "") || /[?&]lang=de\b/.test(location.search);
  document.documentElement.lang = de ? "de" : "en";
  var T = de
    ? {
        title: "Dokumente an HERMES senden",
        hint: "PDF, Markdown oder Text. Der Text wird auf diesem Gerät ausgelesen; nur der Text geht an HERMES.",
        pick: "Dateien auswählen",
        drop: "oder hier ablegen",
        inHub: "Im Hub",
        none: "Noch keine Dokumente.",
        reading: "Lese {name} …",
        added: "{name} hinzugefügt.",
        failed: "{name}: {msg}",
        del: "Löschen",
        confirmDel: "„{name}“ löschen?",
        noAccess: "Kein Zugang. Öffne die Seite einmal mit ?token=<HERMES_TOKEN>.",
        folder: "aus Ordner",
        chars: "{n} Zeichen",
        st_indexing: "wird verarbeitet",
        st_ready: "bereit",
        st_lexical: "bereit (ohne Vektoren)",
        st_error: "Fehler",
        e_empty: "kein Text gefunden (gescannte PDF?)",
        e_too_large: "zu groß",
        e_password: "passwortgeschützt",
        e_type: "Dateityp nicht unterstützt",
        e_other: "Fehler",
      }
    : {
        title: "Send documents to HERMES",
        hint: "PDF, Markdown or text. The text is read on this device; only the text goes to HERMES.",
        pick: "Choose files",
        drop: "or drop them here",
        inHub: "In the hub",
        none: "No documents yet.",
        reading: "Reading {name} …",
        added: "{name} added.",
        failed: "{name}: {msg}",
        del: "Delete",
        confirmDel: "Delete “{name}”?",
        noAccess: "No access. Open this page once with ?token=<HERMES_TOKEN>.",
        folder: "from folder",
        chars: "{n} characters",
        st_indexing: "processing",
        st_ready: "ready",
        st_lexical: "ready (no vectors)",
        st_error: "error",
        e_empty: "no text found (scanned PDF?)",
        e_too_large: "too large",
        e_password: "password protected",
        e_type: "file type not supported",
        e_other: "error",
      };
  function tr(k, vars) {
    return String(T[k] || k).replace(/\{(\w+)\}/g, function (all, v) {
      return vars && vars[v] != null ? String(vars[v]) : all;
    });
  }

  function el(tag, attrs, kids) {
    var e = document.createElement(tag);
    Object.keys(attrs || {}).forEach(function (k) {
      var v = attrs[k];
      if (v == null || v === false) return;
      if (k === "text") e.textContent = v;
      else if (k === "class") e.className = v;
      else if (k.indexOf("on") === 0) e.addEventListener(k.slice(2), v);
      else e.setAttribute(k, v === true ? "" : v);
    });
    (kids || []).forEach(function (c) {
      if (c) e.appendChild(c);
    });
    return e;
  }

  function api(url, o) {
    o = o || {};
    var init = { method: o.method || "GET", credentials: "same-origin", headers: {} };
    if (o.body !== undefined) {
      init.headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(o.body);
    }
    return fetch(url, init).then(function (res) {
      return res.text().then(function (txt) {
        var data = null;
        try {
          data = txt ? JSON.parse(txt) : null;
        } catch (e) {
          data = null;
        }
        if (!res.ok) {
          var err = new Error((data && data.error) || res.statusText || "HTTP " + res.status);
          err.status = res.status;
          err.code = data && data.code;
          throw err;
        }
        return data;
      });
    });
  }

  // ------------------------------------------------------------ layout

  var input = el("input", { type: "file", multiple: true, accept: ".pdf,.md,.markdown,.txt,application/pdf,text/plain,text/markdown", class: "dp-file" });
  var log = el("ul", { class: "dp-log", "aria-live": "polite" });
  var listEl = el("ul", { class: "dp-list" });
  var drop = el("label", { class: "dp-drop" }, [
    input,
    el("span", { class: "dp-btn", text: tr("pick") }),
    el("span", { class: "dp-drop-hint", text: tr("drop") }),
  ]);
  root.appendChild(el("h1", { class: "dp-title", text: tr("title") }));
  root.appendChild(el("p", { class: "dp-hint", text: tr("hint") }));
  root.appendChild(drop);
  root.appendChild(log);
  root.appendChild(el("h2", { class: "dp-sub", text: tr("inHub") }));
  root.appendChild(listEl);

  function note(text, kind) {
    var li = el("li", { class: "dp-log-item " + (kind || ""), text: text });
    log.insertBefore(li, log.firstChild);
    while (log.children.length > 8) log.removeChild(log.lastChild);
    return li;
  }

  // ------------------------------------------------------------ upload

  function readText(file, kind) {
    if (kind === "pdf") {
      return file.arrayBuffer().then(function (buf) {
        return D.pdfText(new Uint8Array(buf));
      });
    }
    return file.text().then(D.cleanExtractedText);
  }

  function errMsg(err) {
    var c = err && err.code;
    if (c === "empty" || c === "too_large" || c === "password") return tr("e_" + c);
    if (err && err.status === 401) return tr("noAccess");
    return (err && err.message) || tr("e_other");
  }

  function uploadOne(file) {
    var name = String(file.name || "Dokument").slice(0, 200);
    var check = D.checkFile(file, { pdfjs: typeof D.pdfText === "function" });
    if (!check.ok) {
      note(tr("failed", { name: name, msg: tr("e_type") }), "warn");
      return Promise.resolve();
    }
    var line = note(tr("reading", { name: name }));
    return readText(file, check.kind)
      .then(function (text) {
        if (!text || !text.replace(/\s+/g, "")) throw Object.assign(new Error("empty"), { code: "empty" });
        if (D.utf8Length(text) > D.LIMITS.textBytes) throw Object.assign(new Error("too large"), { code: "too_large" });
        return api("/api/docs", { method: "POST", body: { name: name, mime: D.mimeFor(check.kind, name), text: text, owner: "" } });
      })
      .then(
        function () {
          line.textContent = tr("added", { name: name });
          line.className = "dp-log-item ok";
          load();
        },
        function (err) {
          line.textContent = tr("failed", { name: name, msg: errMsg(err) });
          line.className = "dp-log-item warn";
        }
      );
  }

  function uploadAll(files) {
    var list = D.filesFrom(files).slice(0, D.LIMITS.files);
    return list.reduce(function (p, f) {
      return p.then(function () {
        return uploadOne(f);
      });
    }, Promise.resolve());
  }

  input.addEventListener("change", function () {
    var files = input.files;
    uploadAll(files).then(function () {
      input.value = "";
    });
  });
  ["dragenter", "dragover"].forEach(function (t) {
    drop.addEventListener(t, function (e) {
      e.preventDefault();
      drop.classList.add("over");
    });
  });
  ["dragleave", "drop"].forEach(function (t) {
    drop.addEventListener(t, function () {
      drop.classList.remove("over");
    });
  });
  drop.addEventListener("drop", function (e) {
    e.preventDefault();
    if (e.dataTransfer && e.dataTransfer.files) uploadAll(e.dataTransfer.files);
  });

  // ------------------------------------------------------------ list

  function render(docs) {
    listEl.textContent = "";
    if (!docs.length) {
      listEl.appendChild(el("li", { class: "dp-empty", text: tr("none") }));
      return;
    }
    docs.forEach(function (d) {
      var meta = [tr("st_" + (d.status || "ready")), tr("chars", { n: (d.chars || 0).toLocaleString() })];
      if (d.source === "folder") meta.push(tr("folder"));
      var del =
        d.source === "folder"
          ? null
          : el("button", {
              type: "button",
              class: "dp-del",
              text: tr("del"),
              onclick: function () {
                if (!window.confirm(tr("confirmDel", { name: d.name }))) return;
                api("/api/docs/" + encodeURIComponent(d.id), { method: "DELETE" }).then(load, function (err) {
                  note(tr("failed", { name: d.name, msg: errMsg(err) }), "warn");
                });
              },
            });
      listEl.appendChild(
        el("li", { class: "dp-item" }, [
          el("div", { class: "dp-item-main" }, [el("span", { class: "dp-name", text: d.name }), el("span", { class: "dp-meta", text: meta.join(" · ") })]),
          del,
        ])
      );
    });
  }

  var timer = 0;
  function load() {
    clearTimeout(timer);
    return api("/api/docs").then(
      function (res) {
        var docs = (res && Array.isArray(res.docs) ? res.docs : []).slice();
        docs.sort(function (a, b) {
          return String(b.createdAt || "").localeCompare(String(a.createdAt || ""));
        });
        render(docs);
        // refresh while something is still being indexed
        if (docs.some(function (d) { return d.status === "indexing"; })) timer = setTimeout(load, 3000);
      },
      function (err) {
        listEl.textContent = "";
        listEl.appendChild(el("li", { class: "dp-empty warn", text: err && (err.status === 401 || err.status === 403) ? tr("noAccess") : errMsg(err) }));
      }
    );
  }
  load();
})();
