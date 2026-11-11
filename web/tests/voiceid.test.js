"use strict";
// Run: node --test web/tests/
// Voice ID: pure core (fbank, CMN, vectors, VAD ring, base64), the capture
// worklet's resampler/chunker, and voiceid.js against fake browser APIs.
// No ONNX here: the worker's model run is replaced by a fake.
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const C = require("../static/voiceid-core.js");
const W = require("../static/capture-worklet.js");

const STATIC = path.join(__dirname, "..", "static");
const FIX = path.join(__dirname, "fixtures", "voiceid");

// ------------------------------------------------------------------ helpers

function sine(freq, sec, amp, rate) {
  rate = rate || 16000;
  const n = Math.round(sec * rate);
  const x = new Float32Array(n);
  for (let i = 0; i < n; i++) x[i] = amp * Math.sin((2 * Math.PI * freq * i) / rate);
  return x;
}

function chunkOf(amp, seed) {
  // 20 ms of noise-like "speech" at roughly 20*log10(amp/sqrt(3)) dBFS
  const p = new Float32Array(320);
  let x = seed >>> 0 || 1;
  for (let i = 0; i < 320; i++) {
    x = (Math.imul(x, 1664525) + 1013904223) >>> 0;
    p[i] = amp * (x / 2147483648 - 1);
  }
  return p;
}

// Push sec seconds of chunks starting at t0 (ms); returns the end time.
function fill(ring, t0, sec, amp, muted) {
  const n = Math.round(sec * 50);
  for (let k = 0; k < n; k++) {
    const p = chunkOf(amp, k + 7);
    C.pushChunk(ring, p, C.levelDb(p), t0 + k * 20, !!muted);
  }
  return t0 + n * 20;
}

// ------------------------------------------------------------------ constants

test("model id, ORT version and vendor paths match the spec", () => {
  assert.equal(C.MODEL_ID, "campplus-zh-en-adv-16k@aa3cfc16");
  assert.equal(C.ORT_VERSION, "1.30.0");
  assert.equal(C.ORT_DIR, "/vendor/ort-1.30.0/");
  assert.equal(C.ORT_SCRIPT, "/vendor/ort-1.30.0/ort.wasm.min.js");
  assert.equal(C.MODEL_URL, "/vendor/models/campplus-zh-en-adv-16k.onnx");
  assert.equal(C.EMB_DIM, 192);
  // the worker and voiceid.js hard-code the same values
  const worker = fs.readFileSync(path.join(STATIC, "voiceid-worker.js"), "utf8");
  assert.match(worker, /\/vendor\/ort-1\.30\.0\/ort\.wasm\.min\.js/);
  assert.match(worker, /\/voiceid-core\.js/);
  const vid = fs.readFileSync(path.join(STATIC, "voiceid.js"), "utf8");
  assert.ok(vid.includes('"' + C.MODEL_ID + '"'), "voiceid.js model id");
  assert.ok(vid.includes('"' + C.ORT_VERSION + '"'), "voiceid.js ORT version");
});

// ------------------------------------------------------------------ fbank

test("fbank frame count is floor((N + 80) / 160) (snip_edges=false)", () => {
  assert.equal(C.numFrames(16000), 100);
  assert.equal(C.numFrames(16079), 100);
  assert.equal(C.numFrames(16080), 101);
  assert.equal(C.numFrames(80), 1);
  assert.equal(C.numFrames(79), 0);
  assert.equal(C.numFrames(0), 0);
  for (const n of [400, 7382, 23104, 44643]) {
    const f = C.fbank(new Float32Array(n));
    assert.equal(f.frames, Math.floor((n + 80) / 160), "N=" + n);
    assert.equal(f.dim, 80);
    assert.equal(f.data.length, f.frames * 80);
  }
});

test("Povey window: (0.5 - 0.5 cos(2 pi i / 399))^0.85, symmetric, zero at the edges", () => {
  const w = C.POVEY;
  assert.equal(w.length, 400);
  assert.equal(w[0], 0);
  for (const i of [1, 37, 199, 200, 333]) {
    const want = Math.pow(0.5 - 0.5 * Math.cos((2 * Math.PI * i) / 399), 0.85);
    assert.ok(Math.abs(w[i] - want) < 1e-12, "i=" + i);
    assert.ok(Math.abs(w[i] - w[399 - i]) < 1e-12, "symmetric at " + i);
  }
  assert.ok(Math.abs(w[199] - 1) < 1e-4 && Math.abs(w[200] - 1) < 1e-4);
});

test("fbank of silence is log(FLT_EPSILON); a 1 kHz tone peaks in the right mel bin", () => {
  const z = C.fbank(new Float32Array(1600));
  const floor = Math.log(1.1920928955078125e-7);
  for (const v of z.data) assert.ok(Math.abs(v - floor) < 1e-5);
  const f = C.fbank(sine(1000, 0.5, 0.5));
  const row = f.data.subarray(20 * 80, 21 * 80);
  let best = 0;
  for (let b = 1; b < 80; b++) if (row[b] > row[best]) best = b;
  // mel(1000) = 1127 ln(1 + 1000/700); bins span mel(20)..mel(7600) in 81 steps
  const mel = (h) => 1127 * Math.log(1 + h / 700);
  const expected = (mel(1000) - mel(20)) / ((mel(7600) - mel(20)) / 81) - 1;
  assert.ok(Math.abs(best - expected) <= 1, "peak bin " + best + " vs " + expected.toFixed(1));
});

test("CMN gives zero mean per dimension", () => {
  const x = new Float32Array(16000 * 1.3);
  for (let i = 0; i < x.length; i++) x[i] = 0.2 * Math.sin(i / 7) + 0.05 * Math.sin(i / 2.3) * Math.cos(i / 900);
  const f = C.speakerFeatures(x);
  for (let d = 0; d < 80; d++) {
    let m = 0;
    for (let t = 0; t < f.frames; t++) m += f.data[t * 80 + d];
    assert.ok(Math.abs(m / f.frames) < 1e-4, "dim " + d + " mean " + m / f.frames);
  }
  // empty input does not divide by zero
  const e = C.cmn({ data: new Float32Array(0), frames: 0, dim: 80 });
  assert.equal(e.frames, 0);
});

// Parity with the Python kaldi-native-fbank reference (design/speaker/refdump_v2).
test("fbank + CMN match the kaldi-native-fbank reference fixture", () => {
  const man = JSON.parse(fs.readFileSync(path.join(FIX, "manifest.json"), "utf8"));
  const raw16 = fs.readFileSync(path.join(FIX, man.audio));
  const x = new Float32Array(raw16.length / 2);
  for (let i = 0; i < x.length; i++) x[i] = raw16.readInt16LE(2 * i) / 32768;
  assert.equal(x.length, man.samples);
  const readF32 = (name) => {
    const b = fs.readFileSync(path.join(FIX, name));
    const out = new Float32Array(b.length / 4);
    for (let i = 0; i < out.length; i++) out[i] = b.readFloatLE(4 * i);
    return out;
  };
  const f = C.fbank(x);
  assert.equal(f.frames, man.frames);
  const ref = readF32(man.rawfbank);
  assert.equal(ref.length, man.frames * man.dim);
  let m = 0;
  for (let i = 0; i < ref.length; i++) m = Math.max(m, Math.abs(ref[i] - f.data[i]));
  assert.ok(m < man.tolerance, "raw fbank max abs diff " + m);
  C.cmn(f);
  const feats = readF32(man.feats);
  m = 0;
  for (let i = 0; i < feats.length; i++) m = Math.max(m, Math.abs(feats[i] - f.data[i]));
  assert.ok(m < man.tolerance, "CMN features max abs diff " + m);
});

// ------------------------------------------------------------------ vectors

test("cosine, l2normalize and centroid", () => {
  const a = C.l2normalize(Float32Array.from([3, 0, 4]));
  assert.ok(Math.abs(a[0] - 0.6) < 1e-7 && Math.abs(a[2] - 0.8) < 1e-7);
  assert.ok(Math.abs(C.cosine(a, a) - 1) < 1e-6);
  const x = Float32Array.from([1, 0, 0]);
  const y = Float32Array.from([0, 2, 0]);
  assert.equal(C.cosine(x, y), 0);
  assert.ok(Math.abs(C.cosine(x, Float32Array.from([-5, 0, 0])) + 1) < 1e-7);
  // centroid normalises each embedding first, so scale does not matter
  const c = C.centroid([x, y]);
  assert.ok(Math.abs(C.cosine(c, x) - Math.SQRT1_2) < 1e-6);
  assert.ok(Math.abs(C.cosine(c, y) - Math.SQRT1_2) < 1e-6);
  let n = 0;
  for (const v of c) n += v * v;
  assert.ok(Math.abs(n - 1) < 1e-6);
  assert.equal(C.centroid([]), null);
  assert.equal(C.cosine(new Float32Array(3), x), 0, "zero vector does not give NaN");
  // validEmbedding
  const ok = C.l2normalize(Float32Array.from({ length: 192 }, (_, i) => Math.sin(i)));
  assert.ok(C.validEmbedding(ok));
  assert.ok(!C.validEmbedding(new Float32Array(192)));
  assert.ok(!C.validEmbedding(new Float32Array(191).fill(1)));
  const bad = ok.slice();
  bad[5] = NaN;
  assert.ok(!C.validEmbedding(bad));
});

// ------------------------------------------------------------------ silence

test("trimSilence cuts quiet edges and keeps a padding", () => {
  const x = new Float32Array(48000);
  for (let i = 16000; i < 32000; i++) x[i] = Math.sin(i / 5) * 0.3;
  const y = C.trimSilence(x, { padMs: 0 });
  assert.ok(Math.abs(y.length - 16000) <= 640, "len " + y.length);
  const z = C.trimSilence(x); // default 100 ms pad on both sides
  assert.ok(Math.abs(z.length - 19200) <= 640, "padded len " + z.length);
  assert.equal(C.trimSilence(new Float32Array(16000)).length, 0, "all silence -> empty");
  const tiny = new Float32Array(100).fill(0.1);
  assert.equal(C.trimSilence(tiny).length, 100, "shorter than one window -> unchanged");
});

test("voicedSeconds measures the span between the first and last voiced window", () => {
  const x = new Float32Array(16000 * 4);
  for (let i = 16000; i < 16000 * 2.5; i++) x[i] = Math.sin(i / 3) * 0.2;
  assert.ok(Math.abs(C.voicedSeconds(x) - 1.5) <= 0.04, String(C.voicedSeconds(x)));
  assert.equal(C.voicedSeconds(new Float32Array(32000)), 0);
});

test("levelDb and lowNoise", () => {
  assert.equal(C.levelDb(new Float32Array(320)), -120);
  const s = sine(440, 0.02, 1);
  assert.ok(Math.abs(C.levelDb(s) - -3.01) < 0.2);
  const n1 = C.lowNoise(1.5);
  const n2 = C.lowNoise(1.5);
  assert.equal(n1.length, 24000);
  assert.deepEqual(n1, n2, "deterministic");
  assert.ok(C.levelDb(n1) < -50 && C.levelDb(n1) > -70);
  assert.equal(C.trimSilence(n1).length, n1.length, "warm-up noise survives trimming");
});

// ------------------------------------------------------------------ VAD ring

test("VAD excludes muted frames and frames outside the window", () => {
  const r = C.createRing(15);
  let t = 0;
  t = fill(r, t, 3, 0.0005, false); // room noise 0-3 s
  t = fill(r, t, 2, 0.3, true); // HERMES speaking (muted) 3-5 s
  const u0 = t;
  t = fill(r, t, 0.5, 0.0005, false);
  t = fill(r, t, 2, 0.2, false); // the user speaks 2 s
  t = fill(r, t, 1, 0.0005, false);
  const seg = C.cut(r, u0, t);
  assert.ok(seg.sec >= 2 && seg.sec <= 2.21, "voiced " + seg.sec); // 2 s + 200 ms hangover
  assert.equal(seg.pcm.length, seg.frames * 320);
  const all = C.cut(r, 0, t);
  assert.ok(all.totalSec < 2.25, "muted TTS excluded: " + all.totalSec);
  assert.equal(C.cut(r, t + 1, t + 1000).sec, 0, "empty window");
  assert.equal(C.voicedSecondsIn(r, 0, t), all.totalSec);
});

test("VAD keeps the newest 6 s and reports the full voiced total", () => {
  const r = C.createRing(15);
  let t = fill(r, 0, 1, 0.0005, false);
  const first = chunkOf(0.2, 999);
  C.pushChunk(r, first, C.levelDb(first), t, false); // marker chunk at the start of the speech
  t = fill(r, t + 20, 9, 0.2, false);
  const seg = C.cut(r, 0, t, { maxSec: 6 });
  assert.equal(seg.sec, 6);
  assert.ok(seg.totalSec > 9, "total " + seg.totalSec);
  // the oldest voiced chunk (marker) is not in the kept audio; the newest is
  const lastChunk = chunkOf(0.2, 9 * 50 - 1 + 7);
  assert.deepEqual(seg.pcm.subarray(seg.pcm.length - 320), lastChunk);
  assert.notDeepEqual(seg.pcm.subarray(0, 320), first);
});

test("ring wraps after its capacity", () => {
  const r = C.createRing(2);
  assert.equal(r.cap, 100);
  const t = fill(r, 0, 5, 0.2, false);
  assert.equal(r.head, 250);
  const seg = C.cut(r, 0, t);
  assert.equal(seg.sec, 2);
  // the kept audio is the newest 2 s, in order
  assert.deepEqual(seg.pcm.subarray(0, 320), chunkOf(0.2, 150 + 7));
  assert.deepEqual(seg.pcm.subarray(seg.pcm.length - 320), chunkOf(0.2, 249 + 7));
});

test("noise floor drops instantly, rises 1 dB/s, ignores muted and digital silence", () => {
  const r = C.createRing(15);
  const q = (db) => new Float32Array(320).fill(Math.pow(10, db / 20));
  C.pushChunk(r, q(-40), -40, 0, false);
  assert.equal(r.floor, -40);
  C.pushChunk(r, q(-60), -60, 20, false);
  assert.equal(r.floor, -60, "drops instantly");
  for (let k = 0; k < 100; k++) C.pushChunk(r, q(-20), -20, 40 + k * 20, false); // 2 s of louder audio
  assert.ok(Math.abs(r.floor - -58) < 0.05, "rises 1 dB/s: " + r.floor);
  const before = r.floor;
  for (let k = 0; k < 50; k++) C.pushChunk(r, q(-90), -90, 3000 + k * 20, true);
  assert.equal(r.floor, before, "muted frames never move the floor");
  C.pushChunk(r, new Float32Array(320), -120, 5000, false);
  assert.equal(r.floor, before, "digital silence is ignored");
  // the floor never rises above the current level
  const r2 = C.createRing(1);
  C.pushChunk(r2, q(-50), -50, 0, false);
  C.pushChunk(r2, q(-49.99), -49.99, 1000, false);
  assert.ok(r2.floor <= -49.99);
});

test("speech threshold: dB > min(floor+10, -30) and dB > -55", () => {
  assert.equal(C.speechThreshold(-70), -55);
  assert.equal(C.speechThreshold(-50), -40);
  assert.equal(C.speechThreshold(-35), -30);
  assert.equal(C.speechThreshold(null), -55);
  assert.ok(C.isSpeech(-39, -50));
  assert.ok(!C.isSpeech(-41, -50));
  assert.ok(!C.isSpeech(-56, -90));
  assert.ok(C.isSpeech(-29, -10), "loud room: capped at -30");
});

// ------------------------------------------------------------------ base64

test("base64 float32 round trip is little-endian", () => {
  assert.equal(C.encodeF32(Float32Array.from([1])), "AACAPw==");
  assert.equal(C.encodeF32(Float32Array.from([1])), Buffer.from(new Uint8Array([0, 0, 0x80, 0x3f])).toString("base64"));
  const v = Float32Array.from({ length: 192 }, (_, i) => Math.sin(i * 1.7) * (i % 3 ? 1 : -1e-3));
  const b64 = C.encodeF32(v);
  assert.equal(b64.length, Math.ceil((192 * 4) / 3) * 4);
  const buf = Buffer.from(b64, "base64");
  assert.equal(buf.length, 768);
  for (let i = 0; i < 192; i++) assert.equal(buf.readFloatLE(4 * i), v[i]);
  assert.deepEqual(C.decodeF32(b64), v);
  // odd byte counts and padding
  for (let n = 0; n < 8; n++) {
    const bytes = Uint8Array.from({ length: n }, (_, i) => (i * 37 + 11) & 255);
    const s = C.bytesToBase64(bytes);
    assert.equal(s, Buffer.from(bytes).toString("base64"));
    assert.deepEqual(C.base64ToBytes(s), bytes);
  }
  assert.throws(() => C.base64ToBytes("AA*A"));
  assert.throws(() => C.decodeF32("AAA="), /multiple of 4/);
  const vo = C.voiceObject(v, 2.314);
  assert.deepEqual(Object.keys(vo).sort(), ["emb", "model", "ort", "sec"]);
  assert.equal(vo.model, C.MODEL_ID);
  assert.equal(vo.sec, 2.31);
});

// ------------------------------------------------------------------ worklet

test("worklet chunker emits 320-sample chunks with their start index", () => {
  const got = [];
  const ch = new W.HermesChunker(320, (pcm, start) => got.push([pcm, start]));
  for (let i = 0; i < 1000; i++) ch.push(i);
  assert.equal(got.length, 3);
  assert.deepEqual(
    got.map((g) => g[1]),
    [0, 320, 640]
  );
  assert.equal(got[1][0][0], 320);
  assert.equal(got[2][0][319], 959);
  assert.notEqual(got[0][0].buffer, got[1][0].buffer, "each chunk owns its buffer (transferable)");
});

function resample(x, rate) {
  const out = [];
  const r = new W.HermesResampler(rate, (v) => out.push(v));
  // feed in render quanta of 128
  for (let i = 0; i < x.length; i += 128) r.push(x.subarray(i, Math.min(x.length, i + 128)));
  return { out: Float32Array.from(out), r };
}

function rmsOf(x, from) {
  let e = 0;
  let n = 0;
  for (let i = from; i < x.length; i++) {
    e += x[i] * x[i];
    n++;
  }
  return Math.sqrt(e / n);
}

test("worklet resampler: 16 kHz passes through", () => {
  const x = sine(440, 0.1, 0.5);
  const { out, r } = resample(x, 16000);
  assert.deepEqual(out, x);
  assert.equal(r.inputPosOf(320), 320);
});

for (const rate of [48000, 44100, 32000]) {
  test("worklet resampler: " + rate + " Hz -> 16 kHz keeps speech, rejects aliases", () => {
    const sec = 1;
    const { out, r } = resample(sine(1000, sec, 0.5, rate), rate);
    const expected = (sec * rate - (r.taps - 1)) / (rate / 16000);
    assert.ok(Math.abs(out.length - expected) <= 2, "length " + out.length + " vs " + expected);
    assert.ok(Math.abs(rmsOf(out, 200) - 0.5 / Math.SQRT2) < 0.01, "1 kHz passband rms " + rmsOf(out, 200));
    // the output is a 1 kHz tone at 16 kHz: 16 samples per period
    let cross = 0;
    for (let i = 201; i < out.length; i++) if (out[i - 1] < 0 && out[i] >= 0) cross++;
    const want = ((out.length - 201) / 16000) * 1000;
    assert.ok(Math.abs(cross - want) <= 2, "zero crossings " + cross + " vs " + want);
    // 12 kHz would alias to 4 kHz without the low-pass
    const hi = resample(sine(12000, sec, 0.5, rate), rate).out;
    assert.ok(rmsOf(hi, 200) < 0.5 * 0.01, "alias rejected by > 40 dB: rms " + rmsOf(hi, 200));
    assert.ok(r.inputPosOf(0) === (r.taps - 1) / 2);
  });
}

test("worklet level in dBFS", () => {
  assert.equal(W.chunkLevelDb(new Float32Array(320)), -120);
  assert.ok(Math.abs(W.chunkLevelDb(new Float32Array(320).fill(0.1)) - -20) < 1e-6);
});

// ------------------------------------------------------------------ voiceid.js with fakes

function makeEnv(opts) {
  opts = opts || {};
  const clock = { now: 100000 };
  const log = { gum: 0, embeds: [], inits: 0, toasts: [], stopped: 0, workers: 0, terminated: 0, closed: 0 };
  const listeners = {};
  const hooks = {};
  const state = {
    status: opts.status === undefined ? { features: { voiceId: { assets: true } } } : opts.status,
    settings: { voiceIdEnabled: opts.enabled !== false },
    echo: false,
    lang: opts.lang || "de",
  };
  let lastNode = null;

  class FakeTrack {
    stop() {
      log.stopped++;
    }
    addEventListener() {}
  }
  class FakeAudioContext {
    constructor(o) {
      this.sampleRate = (o && o.sampleRate) || 48000;
      this.state = "running";
      this.audioWorklet = { addModule: (url) => (url === "/capture-worklet.js" ? Promise.resolve() : Promise.reject(new Error("bad url"))) };
    }
    get currentTime() {
      return clock.now / 1000;
    }
    getOutputTimestamp() {
      return { contextTime: clock.now / 1000, performanceTime: clock.now };
    }
    createMediaStreamSource() {
      return { connect() {}, disconnect() {} };
    }
    resume() {
      this.state = "running";
      return Promise.resolve();
    }
    close() {
      this.state = "closed";
      log.closed++;
      return Promise.resolve();
    }
  }
  class FakeNode {
    constructor(ctx, name, o) {
      assert.equal(name, "hermes-capture");
      assert.equal(o.numberOfOutputs, 0);
      this.port = { onmessage: null };
      lastNode = this;
    }
    disconnect() {}
  }
  class FakeWorker {
    constructor(url) {
      assert.equal(url, "/voiceid-worker.js");
      log.workers++;
      this.onmessage = null;
    }
    postMessage(m) {
      setImmediate(() => {
        if (m.cmd === "init") {
          log.inits++;
          if (opts.initFails) return this.onmessage({ data: { id: m.id, ok: false, error: "model download failed: HTTP 404" } });
          return this.onmessage({ data: { id: m.id, ok: true, ready: true } });
        }
        log.embeds.push(m.samples.length);
        const emb = new Float32Array(192);
        emb[m.samples.length % 192] = 1;
        this.onmessage({ data: { id: m.id, ok: true, emb, sec: m.samples.length / 16000 } });
      });
    }
    terminate() {
      log.terminated++;
    }
  }

  const Hermes = {
    v: 1,
    ready: true,
    on(name, fn) {
      (listeners[name] = listeners[name] || []).push(fn);
    },
    emit(name, d) {
      (listeners[name] || []).forEach((fn) => fn(d));
    },
    hook(name, fn) {
      (hooks[name] = hooks[name] || []).push(fn);
    },
    hooks(name) {
      return hooks[name] || [];
    },
    api: {
      lang: () => state.lang,
      settings: () => Object.assign({}, state.settings),
      status: () => state.status,
      isEchoRisk: () => state.echo,
      toast: (msg, o) => log.toasts.push({ msg, key: o && o.key }),
    },
  };

  const win = {
    isSecureContext: opts.secure !== false,
    AudioWorkletNode: FakeNode,
    AudioContext: FakeAudioContext,
    WebAssembly: {},
    Worker: FakeWorker,
    performance: { now: () => clock.now },
    navigator: {
      mediaDevices: {
        getUserMedia: (c) => {
          log.gum++;
          log.constraints = c;
          if (opts.denied) {
            const e = new Error("denied");
            e.name = "NotAllowedError";
            return Promise.reject(e);
          }
          return Promise.resolve({ getTracks: () => [new FakeTrack()] });
        },
      },
    },
    setTimeout,
    clearTimeout,
    console: { warn() {}, log() {} },
    Hermes,
    VoiceIDCore: C,
    Float32Array,
    Uint8Array,
    DataView,
    Promise,
    Object,
    Math,
    Error,
    String,
    isFinite,
  };
  win.window = win;
  vm.createContext(win);
  vm.runInContext(fs.readFileSync(path.join(STATIC, "voiceid.js"), "utf8"), win, { filename: "voiceid.js" });

  // Deliver sec seconds of 20 ms chunks at the current clock.
  function speak(sec, amp) {
    const n = Math.round(sec * 50);
    for (let k = 0; k < n; k++) {
      const pcm = chunkOf(amp, k + 3);
      lastNode.port.onmessage({ data: { pcm, level_dB: C.levelDb(pcm), t: clock.now / 1000 } });
      clock.now += 20;
    }
  }
  const flush = () => new Promise((res) => setImmediate(() => setImmediate(res)));
  async function settle() {
    for (let i = 0; i < 6; i++) await flush();
  }
  return { win, VoiceID: win.VoiceID, Hermes, hooks, log, state, clock, speak, settle };
}

test("voiceid.js registers collectVoice and starts capture on mic:on", async () => {
  const env = makeEnv();
  assert.equal(env.hooks.collectVoice.length, 1);
  assert.equal(env.VoiceID.available(), true);
  assert.equal(env.VoiceID.wanted(), true);
  assert.equal(env.log.gum, 0, "no capture before the mic is on");
  env.Hermes.emit("mic:on");
  await env.settle();
  assert.equal(env.log.gum, 1);
  assert.deepEqual(JSON.parse(JSON.stringify(env.log.constraints)), {
    audio: { echoCancellation: true, noiseSuppression: false, autoGainControl: false, channelCount: 1 },
  });
  assert.equal(env.log.inits, 1);
  assert.deepEqual(env.log.embeds, [24000], "one 1.5 s warm-up embed");
  assert.equal(env.VoiceID.capturing(), true);
  // idempotent
  await env.VoiceID.enable();
  env.Hermes.emit("mic:on");
  await env.settle();
  assert.equal(env.log.gum, 1);
});

test("take(): voice object for >= 1 s voiced audio, short for less, null when off", async () => {
  const env = makeEnv();
  assert.equal(await env.hooks.collectVoice[0](), null, "not capturing -> null");
  env.Hermes.emit("mic:on");
  await env.settle();
  env.speak(1, 0.0005); // room noise
  env.Hermes.emit("utterance:anchor", { ptt: false, at: env.clock.now });
  env.speak(0.5, 0.2);
  env.speak(0.3, 0.0005);
  const short = await env.VoiceID.take();
  assert.equal(short.short, true);
  assert.ok(short.sec > 0.4 && short.sec < 1, "sec " + short.sec);
  assert.equal(short.emb, undefined);

  env.Hermes.emit("utterance:anchor", { ptt: true, at: env.clock.now });
  env.speak(0.8, 0.2); // below the incremental step
  env.speak(0.3, 0.2);
  const v = await env.hooks.collectVoice[0]();
  assert.equal(v.model, "campplus-zh-en-adv-16k@aa3cfc16");
  assert.equal(v.ort, "1.30.0");
  assert.equal(C.decodeF32(v.emb).length, 192);
  assert.ok(v.sec >= 1 && v.sec <= 1.35, "sec " + v.sec + " (the pre-roll never reaches into the previous utterance)");

  // settings.voiceIdMinSec moves the short limit
  env.state.settings.voiceIdMinSec = 2;
  env.Hermes.emit("utterance:anchor", { ptt: true, at: env.clock.now });
  env.speak(1.5, 0.2);
  const s2 = await env.VoiceID.take();
  assert.equal(s2.short, true, "1.5 s < voiceIdMinSec 2");
});

test("incremental embedding is reused by take() without a final embed", async () => {
  const env = makeEnv();
  env.Hermes.emit("mic:on");
  await env.settle();
  env.speak(0.5, 0.0005);
  env.Hermes.emit("utterance:anchor", { ptt: false, at: env.clock.now });
  // 2.1 s of speech: incremental jobs at 1 s and 2 s of voiced audio
  for (let i = 0; i < 21; i++) {
    env.speak(0.1, 0.2);
    await env.settle(); // let the fake worker answer between chunks
  }
  const jobs = env.log.embeds.length - 1; // minus warm-up
  assert.equal(jobs, 2, "incremental jobs " + jobs);
  env.speak(0.2, 0.0005); // trailing silence (VAD hangover), within the 0.4 s freshness
  await env.settle();
  const before = env.log.embeds.length;
  const v = await env.VoiceID.take();
  assert.ok(v && v.emb, "voice");
  assert.equal(env.log.embeds.length, before, "no extra embed");

  // a stale incremental result (> 0.4 s of newer voiced audio) -> one final embed
  env.Hermes.emit("utterance:anchor", { ptt: false, at: env.clock.now });
  for (let i = 0; i < 18; i++) {
    env.speak(0.1, 0.2);
    await env.settle();
  }
  env.speak(0.3, 0.0005);
  await env.settle();
  const before2 = env.log.embeds.length;
  const v2 = await env.VoiceID.take();
  assert.ok(v2 && v2.emb, "voice 2");
  assert.equal(env.log.embeds.length, before2 + 1, "exactly one final embed");
  assert.equal(env.log.embeds[env.log.embeds.length - 1], Math.round((1.8 + 0.2) * 50) * 320, "final embed covers all voiced audio");
  // after take the anchor is cleared: a new take without anchor and without speech is short
  const s = await env.VoiceID.take();
  assert.ok(s.short === true || s.emb, "fallback window works");
});

test("frames captured while HERMES may be audible are never used", async () => {
  const env = makeEnv();
  env.Hermes.emit("mic:on");
  await env.settle();
  env.Hermes.emit("utterance:anchor", { at: env.clock.now });
  env.state.echo = true;
  env.speak(3, 0.3);
  env.state.echo = false;
  env.speak(0.5, 0.0005);
  const v = await env.VoiceID.take();
  assert.equal(v.short, true);
  assert.equal(v.sec, 0);
});

test("keep() extends the anchor across the armed window; discard() clears it", async () => {
  const env = makeEnv();
  env.Hermes.emit("mic:on");
  await env.settle();
  env.speak(0.5, 0.0005);
  env.Hermes.emit("utterance:anchor", { at: env.clock.now });
  env.speak(0.6, 0.2); // "Hermes"
  env.Hermes.emit("utterance:keep");
  env.speak(3, 0.0005); // > 1.5 s pre-roll later
  env.Hermes.emit("utterance:anchor", { at: env.clock.now });
  env.speak(0.6, 0.2); // the command
  const v = await env.VoiceID.take();
  assert.ok(v.emb, "both parts count: " + JSON.stringify(v));

  env.Hermes.emit("utterance:anchor", { at: env.clock.now });
  env.speak(0.6, 0.2);
  env.Hermes.emit("utterance:keep");
  env.clock.now += 9000; // armed window over
  env.Hermes.emit("utterance:anchor", { at: env.clock.now });
  env.speak(0.6, 0.2);
  const s = await env.VoiceID.take();
  assert.equal(s.short, true, "an expired keep starts a new anchor");

  env.Hermes.emit("utterance:anchor", { at: env.clock.now });
  env.speak(2, 0.2);
  env.Hermes.emit("utterance:discard");
  env.speak(3, 0.0005);
  env.Hermes.emit("utterance:anchor", { at: env.clock.now });
  env.speak(0.4, 0.2);
  assert.equal((await env.VoiceID.take()).short, true, "discarded speech is not reused");
});

test("mic:off stops the tracks and drops the audio", async () => {
  const env = makeEnv();
  env.Hermes.emit("mic:on");
  await env.settle();
  env.speak(2, 0.2);
  env.Hermes.emit("mic:off");
  assert.equal(env.log.stopped, 1);
  assert.equal(env.log.closed, 1);
  assert.equal(env.VoiceID.capturing(), false);
  assert.equal(env.VoiceID.state().noiseFloorDb, null);
  assert.equal(await env.VoiceID.take(), null);
  assert.equal(env.log.terminated, 0, "worker stays warm while voice ID is enabled");
  env.state.settings.voiceIdEnabled = false;
  env.Hermes.emit("settings", env.state.settings);
  assert.equal(env.log.terminated, 1, "disabled in settings -> worker gone");
  assert.equal(env.VoiceID.wanted(), false);
});

test("missing assets: one toast (key voiceid), no capture, collectVoice null", async () => {
  const env = makeEnv({ status: { features: { voiceId: { assets: false } } } });
  assert.equal(env.VoiceID.available(), false);
  env.Hermes.emit("mic:on");
  env.Hermes.emit("status", {});
  env.Hermes.emit("gesture");
  await env.settle();
  assert.equal(env.log.gum, 0);
  assert.equal(env.log.toasts.length, 1);
  assert.equal(env.log.toasts[0].key, "voiceid");
  assert.match(env.log.toasts[0].msg, /^Stimmerkennung nicht verfügbar: .*fetch-assets\.sh/);
  assert.equal(await env.hooks.collectVoice[0](), null);
  await assert.rejects(env.VoiceID.enable(), /assets/);
});

test("status not loaded yet: no toast, enable once it arrives", async () => {
  const env = makeEnv({ status: null, lang: "en" });
  env.Hermes.emit("mic:on");
  await env.settle();
  assert.equal(env.log.toasts.length, 0);
  assert.equal(env.log.gum, 0);
  env.state.status = { features: { voiceId: { assets: true } } };
  env.Hermes.emit("status", env.state.status);
  await env.settle();
  assert.equal(env.log.gum, 1);
});

test("denied microphone or a failing model: one toast, everything else keeps working", async () => {
  const env = makeEnv({ denied: true, lang: "en" });
  env.Hermes.emit("mic:on");
  await env.settle();
  env.Hermes.emit("settings", env.state.settings);
  await env.settle();
  assert.equal(env.log.gum, 1, "no retry loop on settings events");
  assert.equal(env.log.toasts.length, 1);
  assert.match(env.log.toasts[0].msg, /^Voice ID unavailable: microphone access was denied/);
  assert.equal(env.VoiceID.capturing(), false);
  assert.equal(await env.hooks.collectVoice[0](), null);

  const env2 = makeEnv({ initFails: true });
  env2.Hermes.emit("mic:on");
  await env2.settle();
  assert.equal(env2.log.toasts.length, 1);
  assert.match(env2.log.toasts[0].msg, /Sprechermodell/);
  assert.equal(env2.log.stopped, 1, "the mic is released again");
  assert.equal(env2.VoiceID.capturing(), false);
});

test("insecure context: not available, nothing runs", async () => {
  const env = makeEnv({ secure: false });
  assert.equal(env.VoiceID.available(), false);
  assert.equal(env.VoiceID.state().reason, "insecure");
  env.Hermes.emit("mic:on");
  await env.settle();
  assert.equal(env.log.gum, 0);
  assert.equal(env.log.workers, 0);
});

test("record(): enrolment take stops after trailing silence and returns a voice", async () => {
  const env = makeEnv();
  const levels = [];
  const p = env.VoiceID.record(7000, (l) => levels.push(l));
  await env.settle(); // enable() without mic:on (wizard button = gesture)
  assert.equal(env.log.gum, 1);
  env.speak(0.3, 0.0005);
  env.speak(2.5, 0.2);
  env.speak(1.3, 0.0005); // 1.2 s trailing silence ends the take
  await env.settle();
  const r = await p;
  assert.ok(r.voice && r.voice.emb, "voice");
  assert.ok(r.sec >= 2.4 && r.sec <= 2.8, "sec " + r.sec);
  assert.equal(r.voice.sec, r.sec);
  assert.ok(r.level > 0.3 && r.level <= 1, "level " + r.level);
  assert.ok(levels.length > 100 && levels.every((l) => l >= 0 && l <= 1));
  await assert.rejects(
    Promise.all([env.VoiceID.record(500), env.VoiceID.record(500)]),
    /already recording/
  );
});

test("record(): silence until the deadline gives no voice", async () => {
  const env = makeEnv();
  const p = env.VoiceID.record(300);
  await env.settle();
  env.speak(0.3, 0.0005);
  const r = await p;
  assert.equal(r.voice, null);
  assert.equal(r.sec, 0);
});

test("encode() and embedSamples() through the worker", async () => {
  const env = makeEnv();
  assert.equal(env.VoiceID.encode(Float32Array.from([1])), "AACAPw==");
  const r = await env.VoiceID.embedSamples(new Float32Array(16000 * 2));
  assert.equal(r.emb.length, 192);
  assert.equal(r.sec, 2);
});

test("a stale worker start-up failure never tears down the worker that replaced it", async () => {
  const env = makeEnv({ enabled: false });
  // start a worker, then stop it (settings off) while its init is still in flight
  const first = env.VoiceID.embedSamples(new Float32Array(16000)).catch((e) => e);
  env.Hermes.emit("settings");
  // a new request in the same turn starts a fresh worker
  const second = env.VoiceID.embedSamples(new Float32Array(16000 * 2));
  assert.ok((await first) instanceof Error, "the first request fails: its worker was stopped");
  const r = await second;
  assert.equal(r.sec, 2);
  assert.equal(env.log.workers, 2);
  assert.equal(env.log.terminated, 1, "only the stopped worker is terminated");
});
