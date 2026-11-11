"use strict";
// Run: node --test web/tests/
// Pure helpers of web/static/docs.js (HermesDocs).
const test = require("node:test");
const assert = require("node:assert/strict");
const D = require("../static/docs.js");

const trEn = D.makeTr("en");
const trDe = D.makeTr("de");

test("fileKind and mimeFor", () => {
  assert.equal(D.fileKind("Vertrag.PDF", ""), "pdf");
  assert.equal(D.fileKind("x", "application/pdf"), "pdf");
  assert.equal(D.fileKind("notes.md", ""), "text");
  assert.equal(D.fileKind("a.markdown", ""), "text");
  assert.equal(D.fileKind("a.txt", ""), "text");
  assert.equal(D.fileKind("README", "text/plain"), "text");
  assert.equal(D.fileKind("bild.png", "image/png"), "");
  assert.equal(D.fileKind("brief.docx", ""), "");
  assert.equal(D.fileKind("", ""), "");
  assert.equal(D.mimeFor("pdf", "a.pdf"), "application/pdf");
  assert.equal(D.mimeFor("text", "a.md"), "text/markdown");
  assert.equal(D.mimeFor("text", "a.txt"), "text/plain");
});

test("checkFile: type, size caps, missing pdf.js", () => {
  assert.deepEqual(D.checkFile({ name: "a.txt", size: 10 }), { ok: true, kind: "text", error: "" });
  assert.deepEqual(D.checkFile({ name: "a.pdf", size: 10 }), { ok: true, kind: "pdf", error: "" });
  assert.equal(D.checkFile({ name: "a.exe", size: 10 }).error, "type");
  assert.equal(D.checkFile(null).error, "type");
  assert.equal(D.checkFile({ name: "a.txt", size: D.LIMITS.textFile + 1 }).error, "size");
  assert.equal(D.checkFile({ name: "a.pdf", size: D.LIMITS.pdfFile + 1 }).error, "size");
  assert.equal(D.checkFile({ name: "a.pdf", size: 10 }, { pdfjs: false }).error, "pdf_missing");
  assert.equal(D.checkFile({ name: "a.md", size: 10 }, { pdfjs: false }).ok, true);
});

test("utf8Length counts bytes like the server", () => {
  assert.equal(D.utf8Length("abc"), 3);
  assert.equal(D.utf8Length("äöü"), 6);
  assert.equal(D.utf8Length("€"), 3);
  assert.equal(D.utf8Length("😀"), 4);
  assert.equal(D.utf8Length(""), 0);
  assert.equal(D.utf8Length(Buffer.alloc(10).toString() + "ß"), Buffer.byteLength(Buffer.alloc(10).toString() + "ß"));
});

test("cleanExtractedText: BOM, CRLF, NUL", () => {
  assert.equal(D.cleanExtractedText("﻿a\r\nb\rc\u0000d"), "a\nb\ncd");
  assert.equal(D.cleanExtractedText(null), "");
});

test("pageText joins pdf.js items with line breaks", () => {
  const items = [
    { str: "§ 9 Kündigung", transform: [1, 0, 0, 1, 50, 700], hasEOL: true },
    { str: "Die Frist beträgt ", transform: [1, 0, 0, 1, 50, 680] },
    { str: "drei Monate.", transform: [1, 0, 0, 1, 140, 680] },
    { str: "Neue Zeile", transform: [1, 0, 0, 1, 50, 660] },
    { notText: true },
  ];
  assert.equal(D.pageText(items), "§ 9 Kündigung\nDie Frist beträgt drei Monate.\nNeue Zeile");
  assert.equal(D.pageText([]), "");
  assert.equal(D.pageText(null), "");
});

test("joinPages: blank line between pages, empty pages dropped", () => {
  assert.equal(D.joinPages(["Seite 1 ", "", "  ", "Seite 3"]), "Seite 1\n\nSeite 3");
  assert.equal(D.joinPages([]), "");
});

test("ownerOptions and ownerLabel", () => {
  const people = [
    { id: "p_aaaaaaaaaaaa", name: "Anna", confirmed: true },
    { id: "p_bbbbbbbbbbbb", name: "Ben" },
    { id: "p_cccccccccccc", name: "Unbestätigt", confirmed: false },
    { id: "_owner", name: "x" },
    null,
  ];
  assert.deepEqual(D.ownerOptions(people, trDe), [
    { value: "", label: "Haushalt (alle Bekannten)" },
    { value: "p_aaaaaaaaaaaa", label: "Anna" },
    { value: "p_bbbbbbbbbbbb", label: "Ben" },
  ]);
  assert.equal(D.ownerOptions(null, trEn)[0].label, "Household (everyone known)");
  assert.equal(D.ownerLabel("", people, trEn), "Household (everyone known)");
  assert.equal(D.ownerLabel("p_aaaaaaaaaaaa", people, trEn), "Anna");
  assert.equal(D.ownerLabel("p_gone00000000", people, trDe), "Unbekannte Person");
});

test("statusKey and errorKey", () => {
  assert.equal(D.statusKey("ready"), "st_ready");
  assert.equal(D.statusKey("lexical"), "st_lexical");
  assert.equal(D.statusKey("weird"), "st_indexing");
  assert.equal(D.errorKey({ status: 422, code: "empty" }), "err_empty");
  assert.equal(D.errorKey({ status: 413, code: "too_many_chunks" }), "err_too_many_chunks");
  assert.equal(D.errorKey({ status: 413 }), "err_too_large");
  assert.equal(D.errorKey({ status: 409, code: "limit" }), "err_limit");
  assert.equal(D.errorKey({ status: 403, code: "locked" }), "err_locked");
  assert.equal(D.errorKey({ status: 503, code: "read_only" }), "err_read_only");
  assert.equal(D.errorKey({ status: 503, message: "data file was written by a newer HERMES" }), "err_read_only");
  assert.equal(D.errorKey({ status: 404 }), "err_not_found");
  assert.equal(D.errorKey({ code: "password" }), "err_password");
  assert.equal(D.errorKey({}), "err_network");
  assert.equal(D.errorKey({ status: 500 }), "err_generic");
  assert.equal(trDe("err_empty", { name: "scan.pdf" }), "scan.pdf: Keine Textebene gefunden (gescannte PDF?) – OCR wird nicht unterstützt.");
});

test("sortDocs: newest first, then name", () => {
  const docs = [
    { name: "b", createdAt: "2026-11-01T10:00:00Z" },
    { name: "a", createdAt: "2026-11-01T10:00:00Z" },
    { name: "c", createdAt: "2026-11-05T10:00:00Z" },
    { name: "d" },
  ];
  assert.deepEqual(
    D.sortDocs(docs).map((d) => d.name),
    ["c", "a", "b", "d"]
  );
  assert.equal(docs[0].name, "b", "input untouched");
});

test("filesFrom accepts FileList-like, arrays, one file, dataTransfer", () => {
  const f1 = { name: "a.txt" };
  const f2 = { name: "b.pdf" };
  assert.deepEqual(D.filesFrom({ length: 2, 0: f1, 1: f2 }), [f1, f2]);
  assert.deepEqual(D.filesFrom([f1, null, f2]), [f1, f2]);
  assert.deepEqual(D.filesFrom(f1), [f1]);
  assert.deepEqual(D.filesFrom({ files: [f2] }), [f2]);
  assert.deepEqual(D.filesFrom(null), []);
  assert.deepEqual(D.filesFrom({}), []);
});

test("STRINGS: DE and EN have the same keys; formatCount", () => {
  assert.deepEqual(Object.keys(D.STRINGS.de).sort(), Object.keys(D.STRINGS.en).sort());
  assert.equal(trEn("chars", { n: 3 }), "3 characters");
  assert.equal(D.formatCount(12345, "de"), "12.345");
  assert.equal(D.formatCount(12345, "en"), "12,345");
});
