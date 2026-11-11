/*
 * HERMES voice ID core: pure, dependency-free helpers (no DOM, no ONNX).
 * Shared by the speaker worker (importScripts -> self.VoiceIDCore), the main
 * page (window.VoiceIDCore, used by voiceid.js) and the node tests
 * (require("../../static/voiceid-core.js")).
 *
 * Front end for the CAM++ speaker model, matching kaldi-native-fbank as
 * sherpa-onnx configures it for 3D-Speaker models (verified against the
 * Python reference to < 2e-3, embedding cosine 1.0):
 *   16 kHz mono floats in [-1, 1] (no x32768 scaling), frame 25 ms (400) /
 *   shift 10 ms (160), snip_edges=false (reflected edges), dither 0, DC
 *   removal per frame, pre-emphasis 0.97, Povey window, FFT 512, power
 *   spectrum, 80 triangular mel bins 20 .. 7600 Hz (Kaldi mel scale),
 *   log(max(e, FLT_EPSILON)), no energy term; then per-utterance CMN.
 *   Model input [1, T, 80]; the output embedding is L2-normalised.
 *
 * Also: cosine / centroid, trimSilence / voicedSeconds, the 15 s capture
 * ring with its energy VAD, and LE float32 base64 encoding.
 */
(function (root, factory) {
  var api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root && typeof root === "object") root.VoiceIDCore = api;
})(
  typeof self !== "undefined" ? self : typeof window !== "undefined" ? window : typeof globalThis !== "undefined" ? globalThis : this,
  function () {
    "use strict";

    // ------------------------------------------------------------ constants

    var SR = 16000;
    var FRAME_LEN = 400; // 25 ms
    var FRAME_SHIFT = 160; // 10 ms
    var NFFT = 512;
    var NUM_BINS = 80;
    var LOW_FREQ = 20;
    var HIGH_FREQ = -400; // Kaldi: <= 0 means nyquist + value -> 7600 Hz
    var PREEMPH = 0.97;
    var FLT_EPSILON = 1.1920928955078125e-7;
    var EMB_DIM = 192;
    var CHUNK = 320; // capture chunk: 20 ms at 16 kHz

    // Identifies the model + front end; the server rejects other ids (3.1).
    var MODEL_ID = "campplus-zh-en-adv-16k@aa3cfc16";
    var ORT_VERSION = "1.30.0";
    var ORT_DIR = "/vendor/ort-" + ORT_VERSION + "/";
    var ORT_SCRIPT = ORT_DIR + "ort.wasm.min.js";
    var MODEL_URL = "/vendor/models/campplus-zh-en-adv-16k.onnx";

    var f32 = Math.fround;

    // ------------------------------------------------------------ tables

    var POVEY = new Float64Array(FRAME_LEN);
    for (var pi = 0; pi < FRAME_LEN; pi++) {
      POVEY[pi] = Math.pow(0.5 - 0.5 * Math.cos((2 * Math.PI * pi) / (FRAME_LEN - 1)), 0.85);
    }

    function melScale(f) {
      return f32(1127 * Math.log(1 + f / 700));
    }

    // Kaldi MelBanks: weights for FFT bins 0 .. NFFT/2-1 (Nyquist excluded), sparse.
    var MEL = (function () {
      var numFftBins = NFFT / 2;
      var binWidth = f32(SR / NFFT);
      var nyquist = SR / 2;
      var high = HIGH_FREQ <= 0 ? nyquist + HIGH_FREQ : HIGH_FREQ;
      var melLow = melScale(LOW_FREQ);
      var melHigh = melScale(high);
      var delta = f32((melHigh - melLow) / (NUM_BINS + 1));
      var banks = [];
      for (var b = 0; b < NUM_BINS; b++) {
        var left = f32(melLow + b * delta);
        var center = f32(melLow + (b + 1) * delta);
        var right = f32(melLow + (b + 2) * delta);
        var first = -1;
        var w = [];
        for (var i = 0; i < numFftBins; i++) {
          var mel = melScale(f32(binWidth * i));
          if (mel > left && mel < right) {
            var weight = mel <= center ? f32((mel - left) / (center - left)) : f32((right - mel) / (right - center));
            if (first < 0) first = i;
            w.push(weight);
          }
        }
        banks.push({ first: first, w: Float64Array.from(w) });
      }
      return banks;
    })();

    var BITREV = (function () {
      var r = new Uint16Array(NFFT);
      var bits = Math.round(Math.log(NFFT) / Math.LN2);
      for (var i = 0; i < NFFT; i++) {
        var x = i;
        var y = 0;
        for (var b = 0; b < bits; b++) {
          y = (y << 1) | (x & 1);
          x >>= 1;
        }
        r[i] = y;
      }
      return r;
    })();
    var TW_RE = new Float64Array(NFFT / 2);
    var TW_IM = new Float64Array(NFFT / 2);
    for (var ti = 0; ti < NFFT / 2; ti++) {
      TW_RE[ti] = Math.cos((-2 * Math.PI * ti) / NFFT);
      TW_IM[ti] = Math.sin((-2 * Math.PI * ti) / NFFT);
    }

    // In-place radix-2 FFT of a complex vector of length NFFT.
    function fftInPlace(re, im) {
      for (var i = 0; i < NFFT; i++) {
        var j = BITREV[i];
        if (j > i) {
          var t = re[i];
          re[i] = re[j];
          re[j] = t;
          t = im[i];
          im[i] = im[j];
          im[j] = t;
        }
      }
      for (var size = 2; size <= NFFT; size <<= 1) {
        var half = size >> 1;
        var step = NFFT / size;
        for (var start = 0; start < NFFT; start += size) {
          for (var k = 0; k < half; k++) {
            var wr = TW_RE[k * step];
            var wi = TW_IM[k * step];
            var a = start + k;
            var bb = a + half;
            var xr = re[bb] * wr - im[bb] * wi;
            var xi = re[bb] * wi + im[bb] * wr;
            re[bb] = re[a] - xr;
            im[bb] = im[a] - xi;
            re[a] += xr;
            im[a] += xi;
          }
        }
      }
    }

    // ------------------------------------------------------------ front end

    /* Frames Kaldi produces with snip_edges=false: floor((N + 80) / 160). */
    function numFrames(numSamples) {
      if (!(numSamples > 0)) return 0;
      return Math.floor((numSamples + FRAME_SHIFT / 2) / FRAME_SHIFT);
    }

    /*
     * Kaldi-compatible log-mel fbank of 16 kHz mono samples.
     * Returns {data: Float32Array (row-major [frames][80]), frames, dim}.
     */
    function fbank(samples, scale) {
      scale = scale == null ? 1 : scale;
      var N = samples.length;
      var T = numFrames(N);
      var out = new Float32Array(T * NUM_BINS);
      var frame = new Float64Array(FRAME_LEN);
      var re = new Float64Array(NFFT);
      var im = new Float64Array(NFFT);
      var power = new Float64Array(NFFT / 2 + 1);
      for (var t = 0; t < T; t++) {
        // snip_edges=false: frame centred on t*shift + shift/2, edges reflected
        var begin = t * FRAME_SHIFT + FRAME_SHIFT / 2 - FRAME_LEN / 2;
        var mean = 0;
        var i;
        for (i = 0; i < FRAME_LEN; i++) {
          var s = begin + i;
          while (s < 0 || s >= N) s = s < 0 ? -s - 1 : 2 * N - 1 - s;
          var v = f32(samples[s] * scale);
          frame[i] = v;
          mean += v;
        }
        mean /= FRAME_LEN;
        for (i = 0; i < FRAME_LEN; i++) frame[i] -= mean;
        for (i = FRAME_LEN - 1; i > 0; i--) frame[i] -= PREEMPH * frame[i - 1];
        frame[0] -= PREEMPH * frame[0];
        re.fill(0);
        im.fill(0);
        for (i = 0; i < FRAME_LEN; i++) re[i] = frame[i] * POVEY[i];
        fftInPlace(re, im);
        for (var k = 0; k <= NFFT / 2; k++) power[k] = re[k] * re[k] + im[k] * im[k];
        var row = t * NUM_BINS;
        for (var b = 0; b < NUM_BINS; b++) {
          var bank = MEL[b];
          var w = bank.w;
          var e = 0;
          for (var j = 0; j < w.length; j++) e += w[j] * power[bank.first + j];
          out[row + b] = Math.log(Math.max(e, FLT_EPSILON));
        }
      }
      return { data: out, frames: T, dim: NUM_BINS };
    }

    /* Cepstral mean normalisation in place (per-dimension mean over frames). */
    function cmn(feats) {
      var data = feats.data;
      var frames = feats.frames;
      var dim = feats.dim;
      if (!frames) return feats;
      var mean = new Float64Array(dim);
      var t;
      var d;
      for (t = 0; t < frames; t++) for (d = 0; d < dim; d++) mean[d] += data[t * dim + d];
      for (d = 0; d < dim; d++) mean[d] /= frames;
      for (t = 0; t < frames; t++) for (d = 0; d < dim; d++) data[t * dim + d] -= mean[d];
      return feats;
    }

    /* fbank + CMN: the model input for CAM++ / 3D-Speaker models. */
    function speakerFeatures(samples) {
      return cmn(fbank(samples, 1));
    }

    // ------------------------------------------------------------ vectors

    function l2normalize(v) {
      var n = 0;
      var i;
      for (i = 0; i < v.length; i++) n += v[i] * v[i];
      n = Math.sqrt(n) || 1;
      var o = new Float32Array(v.length);
      for (i = 0; i < v.length; i++) o[i] = v[i] / n;
      return o;
    }

    function cosine(a, b) {
      var d = 0;
      var na = 0;
      var nb = 0;
      var n = Math.min(a.length, b.length);
      for (var i = 0; i < n; i++) {
        d += a[i] * b[i];
        na += a[i] * a[i];
        nb += b[i] * b[i];
      }
      return d / (Math.sqrt(na * nb) || 1);
    }

    /* Voiceprint: L2-normalised mean of L2-normalised embeddings. */
    function centroid(embs) {
      if (!embs || !embs.length) return null;
      var o = new Float32Array(embs[0].length);
      for (var k = 0; k < embs.length; k++) {
        var u = l2normalize(embs[k]);
        for (var i = 0; i < o.length; i++) o[i] += u[i];
      }
      return l2normalize(o);
    }

    /* True for a usable embedding: right length, finite, sensible norm. */
    function validEmbedding(v, dim) {
      dim = dim || EMB_DIM;
      if (!v || v.length !== dim) return false;
      var n = 0;
      for (var i = 0; i < v.length; i++) {
        if (!isFinite(v[i])) return false;
        n += v[i] * v[i];
      }
      return n > 1e-12;
    }

    // ------------------------------------------------------------ silence

    /*
     * Trim leading/trailing low-energy audio: 20 ms energy gate relative to
     * the loudest window. opts: {dbBelowPeak=40, padMs=100}. Returns a
     * subarray view (no copy).
     */
    function trimSilence(samples, opts) {
      opts = opts || {};
      var dbBelowPeak = opts.dbBelowPeak == null ? 40 : opts.dbBelowPeak;
      var padMs = opts.padMs == null ? 100 : opts.padMs;
      var win = CHUNK;
      var n = Math.floor(samples.length / win);
      if (n === 0) return samples;
      var e = new Float64Array(n);
      var peak = 0;
      for (var i = 0; i < n; i++) {
        var s = 0;
        for (var j = 0; j < win; j++) {
          var v = samples[i * win + j];
          s += v * v;
        }
        e[i] = s / win;
        if (e[i] > peak) peak = e[i];
      }
      if (peak === 0) return samples.subarray(0, 0);
      var thr = peak * Math.pow(10, -dbBelowPeak / 10);
      var a = 0;
      var b = n - 1;
      while (a < n && e[a] < thr) a++;
      while (b > a && e[b] < thr) b--;
      var pad = Math.round((padMs / 1000) * SR);
      return samples.subarray(Math.max(0, a * win - pad), Math.min(samples.length, (b + 1) * win + pad));
    }

    /* Seconds of audio between the first and last window above the gate. */
    function voicedSeconds(samples) {
      return trimSilence(samples, { padMs: 0 }).length / SR;
    }

    /* RMS level of a chunk in dBFS (floored at -120). */
    function levelDb(pcm) {
      var e = 0;
      for (var i = 0; i < pcm.length; i++) e += pcm[i] * pcm[i];
      if (!pcm.length) return -120;
      return Math.max(-120, 10 * Math.log10(e / pcm.length + 1e-12));
    }

    /* Deterministic low noise (warm-up input; LCG, no Math.random). */
    function lowNoise(seconds, amp, seed) {
      var n = Math.round((seconds || 1.5) * SR);
      var a = amp == null ? 0.003 : amp;
      var x = (seed >>> 0) || 12345;
      var out = new Float32Array(n);
      for (var i = 0; i < n; i++) {
        x = (Math.imul(x, 1664525) + 1013904223) >>> 0;
        out[i] = a * (x / 2147483648 - 1);
      }
      return out;
    }

    // ------------------------------------------------------------ ring + VAD

    /*
     * Capture ring of 20 ms chunks (16 kHz) with a parallel level, timestamp
     * (performance.now() ms at chunk start) and muted flag per chunk.
     */
    function createRing(seconds) {
      var cap = Math.ceil(((seconds || 15) * SR) / CHUNK);
      return {
        cap: cap,
        pcm: new Float32Array(cap * CHUNK),
        db: new Float32Array(cap),
        at: new Float64Array(cap),
        muted: new Uint8Array(cap),
        head: 0, // total chunks ever written
        floor: null, // running noise floor (dB); null until the first unmuted chunk
        lastAt: -Infinity,
      };
    }

    var FLOOR_RISE_DB_PER_SEC = 1;

    /*
     * Append one 20 ms chunk. Muted chunks (HERMES may be audible) are stored
     * flagged and never move the noise floor. The floor drops instantly to a
     * quieter level and rises 1 dB/s otherwise. Digital silence (< -100 dB,
     * e.g. a muted track) is ignored for the floor.
     */
    function pushChunk(ring, pcm, db, atMs, muted) {
      var i = ring.head % ring.cap;
      var n = Math.min(pcm.length, CHUNK);
      var off = i * CHUNK;
      ring.pcm.set(n === pcm.length ? pcm : pcm.subarray(0, n), off);
      if (n < CHUNK) ring.pcm.fill(0, off + n, off + CHUNK);
      if (db == null || !isFinite(db)) db = levelDb(pcm);
      ring.db[i] = db;
      ring.at[i] = atMs;
      ring.muted[i] = muted ? 1 : 0;
      ring.head++;
      if (!muted && db > -100) {
        if (ring.floor === null || db < ring.floor) ring.floor = db;
        else {
          var dt = isFinite(ring.lastAt) ? Math.max(0, Math.min(1000, atMs - ring.lastAt)) : CHUNK / 16;
          ring.floor = Math.min(db, ring.floor + (FLOOR_RISE_DB_PER_SEC * dt) / 1000);
        }
      }
      ring.lastAt = atMs;
    }

    /* Speech threshold for the current floor: dB > min(floor+10, -30), and > -55. */
    function speechThreshold(floor, opts) {
      opts = opts || {};
      var margin = opts.marginDb == null ? 10 : opts.marginDb;
      var ceil = opts.maxThrDb == null ? -30 : opts.maxThrDb;
      var abs = opts.minDb == null ? -55 : opts.minDb;
      if (floor === null || floor === undefined || !isFinite(floor)) return abs; // no floor yet: absolute gate only
      return Math.max(Math.min(floor + margin, ceil), abs);
    }

    function isSpeech(db, floor, opts) {
      return db > speechThreshold(floor, opts);
    }

    /* Ring indices (oldest first) of the voiced, unmuted chunks in [fromMs, toMs]. */
    function voicedIndices(ring, fromMs, toMs, opts) {
      opts = opts || {};
      var thr = speechThreshold(ring.floor, opts);
      var hang = opts.hangFrames == null ? 10 : opts.hangFrames; // 200 ms
      var n = Math.min(ring.head, ring.cap);
      var keep = [];
      var h = 0;
      for (var f = ring.head - n; f < ring.head; f++) {
        var i = f % ring.cap;
        if (ring.at[i] < fromMs || ring.at[i] > toMs || ring.muted[i]) {
          h = 0;
          continue;
        }
        if (ring.db[i] > thr) h = hang;
        else if (h > 0) h--;
        else continue;
        keep.push(i);
      }
      return { idx: keep, thr: thr };
    }

    /*
     * Voiced audio between fromMs and toMs. opts: {maxSec=6, marginDb,
     * minDb, maxThrDb, hangFrames}. The newest voiced audio wins when it is
     * longer than maxSec. Returns {pcm, sec (kept), totalSec (all voiced),
     * frames, thresholdDb}.
     */
    function cut(ring, fromMs, toMs, opts) {
      opts = opts || {};
      var v = voicedIndices(ring, fromMs, toMs, opts);
      var keep = v.idx;
      var total = keep.length;
      var maxFrames = Math.round(((opts.maxSec || 6) * SR) / CHUNK);
      if (keep.length > maxFrames) keep = keep.slice(keep.length - maxFrames);
      var out = new Float32Array(keep.length * CHUNK);
      for (var k = 0; k < keep.length; k++) {
        out.set(ring.pcm.subarray(keep[k] * CHUNK, keep[k] * CHUNK + CHUNK), k * CHUNK);
      }
      return { pcm: out, sec: out.length / SR, totalSec: (total * CHUNK) / SR, frames: keep.length, thresholdDb: v.thr };
    }

    /* Seconds of voiced audio in [fromMs, toMs] without copying samples. */
    function voicedSecondsIn(ring, fromMs, toMs, opts) {
      return (voicedIndices(ring, fromMs, toMs, opts).idx.length * CHUNK) / SR;
    }

    // ------------------------------------------------------------ base64 (LE float32)

    var B64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    var B64_REV = (function () {
      var r = new Int16Array(256).fill(-1);
      for (var i = 0; i < B64.length; i++) r[B64.charCodeAt(i)] = i;
      return r;
    })();

    function bytesToBase64(bytes) {
      var out = "";
      var i = 0;
      for (; i + 2 < bytes.length; i += 3) {
        var n = (bytes[i] << 16) | (bytes[i + 1] << 8) | bytes[i + 2];
        out += B64[(n >> 18) & 63] + B64[(n >> 12) & 63] + B64[(n >> 6) & 63] + B64[n & 63];
      }
      var rest = bytes.length - i;
      if (rest === 1) {
        var a = bytes[i] << 16;
        out += B64[(a >> 18) & 63] + B64[(a >> 12) & 63] + "==";
      } else if (rest === 2) {
        var b = (bytes[i] << 16) | (bytes[i + 1] << 8);
        out += B64[(b >> 18) & 63] + B64[(b >> 12) & 63] + B64[(b >> 6) & 63] + "=";
      }
      return out;
    }

    function base64ToBytes(s) {
      s = String(s || "").replace(/[\s=]+/g, "");
      var out = new Uint8Array(Math.floor((s.length * 3) / 4));
      var o = 0;
      var acc = 0;
      var bits = 0;
      for (var i = 0; i < s.length; i++) {
        var v = B64_REV[s.charCodeAt(i) & 255];
        if (v < 0 || s.charCodeAt(i) > 255) throw new Error("invalid base64");
        acc = (acc << 6) | v;
        bits += 6;
        if (bits >= 8) {
          bits -= 8;
          out[o++] = (acc >> bits) & 255;
        }
      }
      return out.subarray(0, o);
    }

    /* Float32 values -> base64 of their little-endian bytes (any platform). */
    function encodeF32(v) {
      var bytes = new Uint8Array(v.length * 4);
      var dv = new DataView(bytes.buffer);
      for (var i = 0; i < v.length; i++) dv.setFloat32(i * 4, v[i], true);
      return bytesToBase64(bytes);
    }

    function decodeF32(s) {
      var bytes = base64ToBytes(s);
      if (bytes.length % 4) throw new Error("length is not a multiple of 4");
      var dv = new DataView(bytes.buffer, bytes.byteOffset, bytes.length);
      var out = new Float32Array(bytes.length / 4);
      for (var i = 0; i < out.length; i++) out[i] = dv.getFloat32(i * 4, true);
      return out;
    }

    /* The voice object sent with /api/chat and voiceprint POSTs (3.1, 3.3). */
    function voiceObject(emb, sec) {
      return { model: MODEL_ID, ort: ORT_VERSION, emb: encodeF32(emb), sec: Math.round(sec * 100) / 100 };
    }

    return {
      SR: SR,
      CHUNK: CHUNK,
      FRAME_LEN: FRAME_LEN,
      FRAME_SHIFT: FRAME_SHIFT,
      NUM_BINS: NUM_BINS,
      EMB_DIM: EMB_DIM,
      MODEL_ID: MODEL_ID,
      ORT_VERSION: ORT_VERSION,
      ORT_DIR: ORT_DIR,
      ORT_SCRIPT: ORT_SCRIPT,
      MODEL_URL: MODEL_URL,
      POVEY: POVEY,
      numFrames: numFrames,
      fbank: fbank,
      cmn: cmn,
      speakerFeatures: speakerFeatures,
      l2normalize: l2normalize,
      cosine: cosine,
      centroid: centroid,
      validEmbedding: validEmbedding,
      trimSilence: trimSilence,
      voicedSeconds: voicedSeconds,
      levelDb: levelDb,
      lowNoise: lowNoise,
      createRing: createRing,
      pushChunk: pushChunk,
      speechThreshold: speechThreshold,
      isSpeech: isSpeech,
      cut: cut,
      voicedSecondsIn: voicedSecondsIn,
      bytesToBase64: bytesToBase64,
      base64ToBytes: base64ToBytes,
      encodeF32: encodeF32,
      decodeF32: decodeF32,
      voiceObject: voiceObject,
    };
  }
);
