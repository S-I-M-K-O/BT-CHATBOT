/*
 * HERMES voice ID worker (dedicated, classic): onnxruntime-web 1.30.0 (WASM,
 * one thread, self-hosted under /vendor) + the CAM++ speaker model.
 *
 * Messages in:
 *   {id, cmd: "init"}                          load the session (idempotent)
 *   {id, cmd: "embed", samples: Float32Array}  16 kHz mono, transferred
 * Messages out:
 *   {id, ok: true, ready: true}                after init
 *   {id, emb: Float32Array(192), sec}          L2-normalised; sec = audio fed to the model
 *   {id, error: "..."}                         on any failure
 *
 * Pipeline per embed: trimSilence -> fbank -> CMN -> run -> L2.
 * Raw audio never leaves the browser; this worker makes no network calls
 * except fetching the self-hosted runtime and model (immutable cache).
 */
/* global ort, VoiceIDCore, importScripts */
"use strict";

var CORE_URL = "/voiceid-core.js";
var ORT_URL = "/vendor/ort-1.30.0/ort.wasm.min.js";
var MIN_SEC = 0.3; // below this the model output is meaningless

var loadError = null;
try {
  importScripts(ORT_URL, CORE_URL);
} catch (e) {
  loadError = "runtime not found (" + ((e && e.message) || e) + ")";
}

var session = null;
var sessionPromise = null;

function initSession() {
  if (loadError) return Promise.reject(new Error(loadError));
  if (sessionPromise) return sessionPromise;
  var C = VoiceIDCore;
  ort.env.wasm.numThreads = 1; // no SharedArrayBuffer / cross-origin isolation needed
  ort.env.wasm.proxy = false;
  ort.env.wasm.wasmPaths = C.ORT_DIR; // self-hosted, never a CDN
  sessionPromise = fetch(C.MODEL_URL, { credentials: "same-origin" })
    .then(function (r) {
      if (!r.ok) throw new Error("model download failed: HTTP " + r.status);
      return r.arrayBuffer();
    })
    .then(function (buf) {
      return ort.InferenceSession.create(new Uint8Array(buf), {
        executionProviders: ["wasm"],
        graphOptimizationLevel: "all",
      });
    })
    .then(function (s) {
      session = s;
      return s;
    })
    .catch(function (err) {
      sessionPromise = null; // allow a retry on the next request
      throw err;
    });
  return sessionPromise;
}

function embed(samples) {
  var C = VoiceIDCore;
  return initSession().then(function (s) {
    if (!(samples instanceof Float32Array)) samples = Float32Array.from(samples || []);
    var trimmed = C.trimSilence(samples);
    var sec = trimmed.length / C.SR;
    if (sec < MIN_SEC) throw new Error("too short");
    var f = C.speakerFeatures(trimmed);
    var feeds = {};
    feeds[s.inputNames[0]] = new ort.Tensor("float32", f.data, [1, f.frames, f.dim]);
    return s.run(feeds).then(function (out) {
      var t = out[s.outputNames[0]];
      var raw = t.data;
      var emb = C.l2normalize(raw);
      if (typeof t.dispose === "function") t.dispose();
      if (!C.validEmbedding(emb, C.EMB_DIM)) throw new Error("bad embedding (" + raw.length + " values)");
      return { emb: emb, sec: sec };
    });
  });
}

// Requests run strictly one after another (one WASM thread anyway).
var queue = Promise.resolve();

self.onmessage = function (e) {
  var m = e.data || {};
  var id = m.id;
  queue = queue.then(function () {
    var job;
    if (m.cmd === "init") {
      job = initSession().then(function () {
        self.postMessage({ id: id, ok: true, ready: true });
      });
    } else if (m.cmd === "embed") {
      job = embed(m.samples).then(function (r) {
        self.postMessage({ id: id, ok: true, emb: r.emb, sec: r.sec }, [r.emb.buffer]);
      });
    } else {
      job = Promise.reject(new Error("unknown command"));
    }
    return job.catch(function (err) {
      self.postMessage({ id: id, ok: false, error: String((err && err.message) || err) });
    });
  });
};
