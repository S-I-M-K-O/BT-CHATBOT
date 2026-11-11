/*
 * HERMES voice ID capture: AudioWorkletProcessor "hermes-capture".
 *
 * Mono input -> 20 ms chunks of 320 samples at 16 kHz, each posted to the
 * main thread as {pcm: Float32Array (transferred), level_dB, t} where t is
 * the AudioContext time of the chunk's first sample. The node is created
 * with numberOfOutputs: 0, so nothing is ever played back.
 *
 * The capture context normally runs at 16 kHz (Chrome resamples the mic
 * track into it). If the browser refused that rate, the context runs at its
 * native rate and this processor resamples: a windowed-sinc low-pass (cut
 * 7.2 kHz) evaluated only at the output positions, i.e. a plain /3 FIR
 * decimation for 48 kHz, plus linear interpolation for non-integer ratios.
 *
 * Messages in: {on: boolean} pauses/resumes posting.
 *
 * The pure parts (HermesChunker, HermesResampler) are exported for the node
 * tests via module.exports; the processor registers only inside a worklet.
 */
/* global sampleRate, currentTime, registerProcessor, AudioWorkletProcessor */

var OUT_RATE = 16000;
var CHUNK = 320;

/* Collects samples into fixed-size chunks; calls emit(chunk, startIndex). */
function HermesChunker(size, emit) {
  this.size = size || CHUNK;
  this.emit = emit;
  this.buf = new Float32Array(this.size);
  this.n = 0;
  this.count = 0; // samples ever pushed
}
HermesChunker.prototype.push = function (v) {
  this.buf[this.n++] = v;
  this.count++;
  if (this.n === this.size) {
    var full = this.buf;
    this.buf = new Float32Array(this.size); // the full one is transferred away
    this.n = 0;
    this.emit(full, this.count - this.size);
  }
};

/*
 * Streaming resampler inRate -> 16 kHz. emit(sample) per output sample.
 * inRate === 16000 is a pass-through.
 */
function HermesResampler(inRate, emit, taps) {
  this.inRate = inRate;
  this.emit = emit;
  this.step = inRate / OUT_RATE;
  this.pass = Math.abs(inRate - OUT_RATE) < 1;
  if (this.pass) return;
  var n = taps || 127;
  if (n % 2 === 0) n++;
  this.taps = n;
  var fc = 7200 / inRate; // cycles per input sample
  var h = new Float64Array(n);
  var sum = 0;
  var mid = (n - 1) / 2;
  for (var i = 0; i < n; i++) {
    var x = i - mid;
    var sinc = x === 0 ? 2 * fc : Math.sin(2 * Math.PI * fc * x) / (Math.PI * x);
    var w = 0.42 - 0.5 * Math.cos((2 * Math.PI * i) / (n - 1)) + 0.08 * Math.cos((4 * Math.PI * i) / (n - 1)); // Blackman
    h[i] = sinc * w;
    sum += h[i];
  }
  for (var j = 0; j < n; j++) h[j] /= sum; // unity DC gain
  this.h = h;
  this.hist = new Float32Array(2 * n); // doubled history, no modulo in the FIR loop
  this.pos = 0; // write index into hist (0..n-1)
  this.got = 0; // input samples received
  this.next = n - 1; // next output position, in input-sample units (first full window)
  this.lastIdx = -1;
  this.lastVal = 0;
}
/* Input-sample position (fractional, global index) that output sample j represents. */
HermesResampler.prototype.inputPosOf = function (j) {
  if (this.pass) return j;
  return (this.taps - 1) / 2 + j * this.step; // FIR group delay + output grid
};
HermesResampler.prototype._fir = function () {
  // FIR output at the newest input sample
  var h = this.h;
  var n = this.taps;
  var base = this.pos; // hist[base .. base+n-1] = oldest .. newest
  var acc = 0;
  for (var i = 0; i < n; i++) acc += h[i] * this.hist[base + i];
  return acc;
};
HermesResampler.prototype.pushSample = function (v) {
  if (this.pass) {
    this.emit(v);
    return;
  }
  var n = this.taps;
  this.hist[this.pos] = v;
  this.hist[this.pos + n] = v;
  this.pos = (this.pos + 1) % n;
  var idx = this.got++; // index of the sample just written
  if (idx < n - 1) return;
  var y = this._fir();
  // emit every output position p with floor(p) + 1 === idx (interpolate between idx-1 and idx)
  while (this.next <= idx) {
    var p = this.next;
    var k = Math.floor(p);
    var frac = p - k;
    var out;
    if (k === idx) out = y;
    else if (k === idx - 1 && this.lastIdx === idx - 1) out = this.lastVal + frac * (y - this.lastVal);
    else out = y;
    this.emit(out);
    this.next += this.step;
  }
  this.lastIdx = idx;
  this.lastVal = y;
};
HermesResampler.prototype.push = function (arr) {
  for (var i = 0; i < arr.length; i++) this.pushSample(arr[i]);
};

function chunkLevelDb(pcm) {
  var e = 0;
  for (var i = 0; i < pcm.length; i++) e += pcm[i] * pcm[i];
  return Math.max(-120, 10 * Math.log10(e / pcm.length + 1e-12));
}

if (typeof AudioWorkletProcessor !== "undefined" && typeof registerProcessor === "function") {
  class HermesCapture extends AudioWorkletProcessor {
    constructor() {
      super();
      var self = this;
      this.on = true;
      // time base of the current render quantum: context time + global input index
      this.qTime = 0;
      this.qBase = 0;
      this.chunker = new HermesChunker(CHUNK, function (pcm, start) {
        if (!self.on) return;
        // context time of the chunk's first sample, anchored to this quantum
        // (robust against dropped quanta, unlike counting samples from t0)
        var t = self.qTime + (self.resampler.inputPosOf(start) - self.qBase) / sampleRate;
        self.port.postMessage({ pcm: pcm, level_dB: chunkLevelDb(pcm), t: t }, [pcm.buffer]);
      });
      this.resampler = new HermesResampler(sampleRate, function (v) {
        self.chunker.push(v);
      });
      this.port.onmessage = function (e) {
        if (e.data && typeof e.data.on === "boolean") self.on = e.data.on;
      };
    }
    process(inputs) {
      var ch = inputs[0] && inputs[0][0];
      this.qTime = currentTime;
      this.qBase = this.resampler.pass ? this.chunker.count : this.resampler.got;
      if (!ch) {
        // no input connected yet: keep the timeline continuous with silence
        ch = new Float32Array(128);
      }
      this.resampler.push(ch);
      return true;
    }
  }
  registerProcessor("hermes-capture", HermesCapture);
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { HermesChunker: HermesChunker, HermesResampler: HermesResampler, chunkLevelDb: chunkLevelDb, CHUNK: CHUNK, OUT_RATE: OUT_RATE };
}
