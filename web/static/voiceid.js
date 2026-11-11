/*
 * HERMES voice ID, browser side (window.VoiceID).
 *
 * getUserMedia -> separate 16 kHz AudioContext -> /capture-worklet.js
 * (20 ms chunks) -> 15 s in-memory ring with a muted flag + energy VAD ->
 * /voiceid-worker.js (onnxruntime-web 1.30.0 + CAM++) -> 192-dim embedding.
 * Only the embedding is sent (with the chat request); matching happens on
 * the server. Raw audio never leaves the browser and is never stored.
 *
 *   VoiceID.available()   secure context + AudioWorklet + WASM + assets on the server
 *   VoiceID.wanted()      settings.voiceIdEnabled && available()
 *   VoiceID.enable()      capture + worker + warm-up; idempotent
 *   VoiceID.disable()     stop the mic tracks, drop the ring
 *   VoiceID.mark(ptt) / keep() / discard()   utterance anchor
 *   VoiceID.take()        -> Promise<voice | {short:true, sec} | null>
 *   VoiceID.record(ms, onLevel) -> Promise<{voice, sec, level}>   (enrolment)
 *   VoiceID.embedSamples(Float32Array) -> Promise<{emb, sec}>
 *   VoiceID.encode(Float32Array) -> base64 (LE float32)
 *   VoiceID.capturing()   true while audio is being captured (sets voiceExpected)
 *   VoiceID.state()       diagnostics
 *
 * It registers the `collectVoice` hook and follows the Hermes bus events
 * (5.2): gesture, mic:on/off, settings, status, utterance:anchor/keep/discard.
 */
(function (root) {
  "use strict";

  var CORE_URL = "/voiceid-core.js";
  var WORKER_URL = "/voiceid-worker.js";
  var WORKLET_URL = "/capture-worklet.js";
  var MODEL_ID = "campplus-zh-en-adv-16k@aa3cfc16";
  var ORT_VERSION = "1.30.0";

  var RING_SEC = 15;
  var PRE_ROLL_MS = 1500; // Chrome's first interim lags speech onset
  var FALLBACK_WINDOW_MS = 8000; // no anchor: look at the last 8 s
  var MAX_SEC = 6; // newest voiced audio per utterance
  var MIN_SEC = 1.0; // default settings.voiceIdMinSec: less voiced audio -> {short:true}
  var INC_STEP_SEC = 1.0; // incremental embed every 1 s of new voiced audio
  var INC_CHECK_MS = 200;
  var FRESH_SEC = 0.4; // an incremental result may miss at most this much
  var ARM_MS = 8000; // app.js ARM_MS: a kept anchor survives the armed window
  var RECORD_MS = 6000;
  var RECORD_TRAIL_MS = 1200; // enrolment stops after this much trailing silence
  var RECORD_MIN_VOICED_MS = 400;
  var RECORD_MAX_SEC = 8;
  var RECORD_IDLE_STOP_MS = 30000; // mic off: keep capture this long between enrolment takes
  var JOB_TIMEOUT_MS = 20000;
  var INIT_TIMEOUT_MS = 60000;

  var STRINGS = {
    en: {
      unavailable: "Voice ID unavailable: {reason}",
      assets: "model files are missing (run sh scripts/fetch-assets.sh)",
      insecure: "only over http://localhost or HTTPS",
      browser: "this browser does not support it",
      nomic: "no microphone found",
      denied: "microphone access was denied",
      load: "the speaker model failed to load",
      failed: "the audio capture failed",
    },
    de: {
      unavailable: "Stimmerkennung nicht verfügbar: {reason}",
      assets: "Modelldateien fehlen (sh scripts/fetch-assets.sh ausführen)",
      insecure: "nur über http://localhost oder HTTPS",
      browser: "dieser Browser unterstützt sie nicht",
      nomic: "kein Mikrofon gefunden",
      denied: "Mikrofonzugriff wurde verweigert",
      load: "das Sprechermodell konnte nicht geladen werden",
      failed: "die Audioaufnahme ist fehlgeschlagen",
    },
  };

  var C = root.VoiceIDCore || null;
  var corePromise = null;

  var st = {
    gen: 0, // bumps on disable(): stale async steps tear themselves down
    capture: null, // {ctx, stream, src, node}
    ring: null,
    enabling: null,
    worker: null,
    workerInit: null,
    jobs: {},
    seq: 0,
    anchor: 0,
    anchorSeq: 0,
    consumedAt: 0, // audio up to here belongs to an earlier (taken or discarded) utterance
    keptAt: 0,
    inc: null, // newest incremental result {emb, sec, totalSec}
    incJob: null, // in-flight incremental job {promise, totalSec, seq}
    incPostedTotal: 0,
    incCheckAt: 0,
    recorder: null,
    recording: false, // a record() call is in progress (set synchronously)
    idleTimer: 0,
    micOn: false,
    failed: "", // last enable() failure reason (retried on the next mic:on / gesture)
    toasted: false,
  };

  function noop() {}

  function nowMs() {
    return root.performance && performance.now ? performance.now() : Date.now();
  }

  // ------------------------------------------------------------ Hermes access

  function H() {
    return root.Hermes || null;
  }

  function api() {
    var h = H();
    return h && h.api ? h.api : null;
  }

  function callApi(name, fallback) {
    var a = api();
    if (!a || typeof a[name] !== "function") return fallback;
    try {
      return a[name]();
    } catch (e) {
      return fallback;
    }
  }

  function lang() {
    var l = callApi("lang", null);
    if (l === "de" || l === "en") return l;
    var d = root.document && document.documentElement && document.documentElement.lang;
    return d && /^de/i.test(d) ? "de" : "en";
  }

  var trFn = null;
  function tr(key, vars) {
    var h = H();
    if (!trFn && h && typeof h.strings === "function") {
      try {
        trFn = h.strings(STRINGS);
      } catch (e) {
        trFn = null;
      }
    }
    if (trFn) {
      try {
        return trFn(key, vars);
      } catch (e) {
        /* fall through */
      }
    }
    var s = (STRINGS[lang()] || STRINGS.en)[key] || STRINGS.en[key] || key;
    return s.replace(/\{(\w+)\}/g, function (m, k) {
      return vars && vars[k] != null ? String(vars[k]) : m;
    });
  }

  function echoRisk() {
    return !!callApi("isEchoRisk", false);
  }

  // ------------------------------------------------------------ core loader

  function loadCore() {
    if (C) return Promise.resolve(C);
    if (root.VoiceIDCore) {
      C = root.VoiceIDCore;
      return Promise.resolve(C);
    }
    if (corePromise) return corePromise;
    corePromise = new Promise(function (resolve, reject) {
      if (!root.document) {
        reject(new Error("no document"));
        return;
      }
      var s = document.createElement("script");
      s.src = CORE_URL;
      s.async = true;
      s.onload = function () {
        if (root.VoiceIDCore) {
          C = root.VoiceIDCore;
          resolve(C);
        } else reject(new Error("voiceid-core.js did not load"));
      };
      s.onerror = function () {
        reject(new Error("voiceid-core.js did not load"));
      };
      (document.head || document.documentElement).appendChild(s);
    }).catch(function (err) {
      corePromise = null;
      throw err;
    });
    return corePromise;
  }

  // ------------------------------------------------------------ availability

  function status() {
    return callApi("status", null);
  }

  function settings() {
    return callApi("settings", null);
  }

  /*
   * "" when voice ID can run here. Otherwise one of: insecure, browser,
   * nomic, assets, nostatus (status not fetched yet: not an error).
   */
  function unavailableReason() {
    if (!root.isSecureContext) return "insecure";
    if (typeof root.AudioWorkletNode === "undefined" || typeof root.WebAssembly !== "object" || typeof root.Worker === "undefined") return "browser";
    if (!root.navigator || !navigator.mediaDevices || typeof navigator.mediaDevices.getUserMedia !== "function") return "nomic";
    var s = status();
    if (!s) return "nostatus";
    var f = s.features && s.features.voiceId;
    if (!f || !f.assets) return "assets";
    return "";
  }

  function available() {
    return unavailableReason() === "";
  }

  function settingOn() {
    var s = settings();
    return !!(s && s.voiceIdEnabled);
  }

  /* settings.voiceIdMinSec (server clamp 0.5 .. 4), default 1.0 s. */
  function minSec() {
    var s = settings();
    var v = s && typeof s.voiceIdMinSec === "number" && isFinite(s.voiceIdMinSec) ? s.voiceIdMinSec : MIN_SEC;
    return v < 0.5 ? 0.5 : v > 4 ? 4 : v;
  }

  function wanted() {
    return settingOn() && available();
  }

  function capturing() {
    return !!(st.capture && st.ring && st.capture.ctx && st.capture.ctx.state === "running");
  }

  function notifyUnavailable(reason) {
    if (st.toasted || !reason || reason === "nostatus") return;
    st.toasted = true;
    var msg = tr("unavailable", { reason: tr(reason) });
    var a = api();
    if (a && typeof a.toast === "function") {
      try {
        a.toast(msg, { type: "warn", key: "voiceid" });
        return;
      } catch (e) {
        /* fall through */
      }
    }
    if (root.console) console.warn("[voiceid] " + msg);
  }

  function errorReason(err) {
    var name = err && err.name;
    if (name === "NotAllowedError" || name === "SecurityError") return "denied";
    if (name === "NotFoundError" || name === "OverconstrainedError" || name === "NotReadableError") return "nomic";
    if (err && err.voiceidReason) return err.voiceidReason;
    return "failed";
  }

  function tagged(err, reason) {
    var e = err instanceof Error ? err : new Error(String(err));
    if (!e.voiceidReason) e.voiceidReason = reason;
    return e;
  }

  // ------------------------------------------------------------ worker

  function rejectAllJobs(msg) {
    var jobs = st.jobs;
    st.jobs = {};
    Object.keys(jobs).forEach(function (id) {
      clearTimeout(jobs[id].timer);
      jobs[id].reject(new Error(msg));
    });
  }

  function terminateWorker() {
    if (st.worker) {
      try {
        st.worker.terminate();
      } catch (e) {
        /* ignore */
      }
    }
    st.worker = null;
    st.workerInit = null;
    rejectAllJobs("voice ID stopped");
  }

  function post(msg, transfer, timeoutMs) {
    return new Promise(function (resolve, reject) {
      if (!st.worker) {
        reject(new Error("voice ID worker not running"));
        return;
      }
      var id = ++st.seq;
      msg.id = id;
      var job = { resolve: resolve, reject: reject, timer: 0 };
      job.timer = setTimeout(function () {
        if (st.jobs[id]) {
          delete st.jobs[id];
          reject(new Error("voice ID worker timed out"));
        }
      }, timeoutMs || JOB_TIMEOUT_MS);
      st.jobs[id] = job;
      try {
        st.worker.postMessage(msg, transfer || []);
      } catch (e) {
        clearTimeout(job.timer);
        delete st.jobs[id];
        reject(e);
      }
    });
  }

  function onWorkerMessage(e) {
    var d = e.data || {};
    var job = st.jobs[d.id];
    if (!job) return;
    delete st.jobs[d.id];
    clearTimeout(job.timer);
    if (d.error || d.ok === false) job.reject(new Error(d.error || "voice ID worker failed"));
    else job.resolve(d);
  }

  function ensureWorker() {
    if (st.workerInit) return st.workerInit;
    try {
      st.worker = new Worker(WORKER_URL);
    } catch (err) {
      st.worker = null;
      return Promise.reject(tagged(err, "load"));
    }
    st.worker.onmessage = onWorkerMessage;
    st.worker.onerror = function (ev) {
      if (ev && typeof ev.preventDefault === "function") ev.preventDefault();
      var msg = (ev && ev.message) || "voice ID worker crashed";
      terminateWorker();
      if (root.console) console.warn("[voiceid] " + msg);
    };
    var p = post({ cmd: "init" }, null, INIT_TIMEOUT_MS).catch(function (err) {
      // only tear down our own worker: a stopped one may already be replaced
      if (st.workerInit === p) terminateWorker();
      throw tagged(err, "load");
    });
    st.workerInit = p;
    return p;
  }

  /* Embed 16 kHz samples in the worker. The buffer is transferred. */
  function embedSamples(samples) {
    if (!(samples instanceof Float32Array)) samples = Float32Array.from(samples || []);
    return ensureWorker().then(function () {
      return post({ cmd: "embed", samples: samples }, [samples.buffer]);
    }).then(function (r) {
      var emb = r.emb instanceof Float32Array ? r.emb : Float32Array.from(r.emb || []);
      return { emb: emb, sec: r.sec };
    });
  }

  // ------------------------------------------------------------ encoding

  function encode(v) {
    if (C) return C.encodeF32(v);
    var bytes = new Uint8Array(v.length * 4);
    var dv = new DataView(bytes.buffer);
    for (var i = 0; i < v.length; i++) dv.setFloat32(i * 4, v[i], true);
    var s = "";
    for (var j = 0; j < bytes.length; j++) s += String.fromCharCode(bytes[j]);
    return root.btoa(s);
  }

  function voiceObject(emb, sec) {
    return { model: MODEL_ID, ort: ORT_VERSION, emb: encode(emb), sec: Math.round(sec * 100) / 100 };
  }

  // ------------------------------------------------------------ capture

  function mapTime(cap, t) {
    var ctx = cap.ctx;
    var now = nowMs();
    var at = NaN;
    try {
      var ts = ctx.getOutputTimestamp ? ctx.getOutputTimestamp() : null;
      if (ts && ts.performanceTime > 0 && isFinite(ts.contextTime)) at = ts.performanceTime + (t - ts.contextTime) * 1000;
    } catch (e) {
      /* ignore */
    }
    if (!isFinite(at)) at = now - (ctx.currentTime - t) * 1000;
    if (!isFinite(at) || at > now) at = now - 20;
    return at;
  }

  function onChunk(cap, e) {
    var d = e.data;
    if (!d || !d.pcm || cap !== st.capture || !st.ring) return;
    var at = mapTime(cap, d.t);
    var muted = echoRisk();
    C.pushChunk(st.ring, d.pcm, d.level_dB, at, muted);
    if (st.recorder) st.recorder.onChunk(d.level_dB, at, muted);
    maybeIncremental(at);
  }

  function openContext(stream, rate) {
    var ctx;
    try {
      ctx = rate ? new AudioContext({ sampleRate: rate, latencyHint: "interactive" }) : new AudioContext({ latencyHint: "interactive" });
    } catch (err) {
      return Promise.reject(err);
    }
    return ctx.audioWorklet
      .addModule(WORKLET_URL)
      .then(function () {
        var src = ctx.createMediaStreamSource(stream); // throws on a rate mismatch in some browsers
        var node = new AudioWorkletNode(ctx, "hermes-capture", {
          numberOfInputs: 1,
          numberOfOutputs: 0,
          channelCount: 1,
          channelCountMode: "explicit",
          channelInterpretation: "speakers",
        });
        src.connect(node);
        return { ctx: ctx, stream: stream, src: src, node: node };
      })
      .catch(function (err) {
        ctx.close().catch(noop);
        throw err;
      });
  }

  function stopCapture(cap) {
    if (!cap) return;
    try {
      if (cap.node) {
        cap.node.port.onmessage = null;
        cap.node.disconnect();
      }
      if (cap.src) cap.src.disconnect();
    } catch (e) {
      /* ignore */
    }
    if (cap.stream) {
      cap.stream.getTracks().forEach(function (t) {
        try {
          t.stop();
        } catch (e) {
          /* ignore */
        }
      });
    }
    if (cap.ctx && cap.ctx.state !== "closed") cap.ctx.close().catch(noop);
  }

  function startCapture(gen) {
    return navigator.mediaDevices
      .getUserMedia({
        // AEC on (HERMES' own TTS is the far end); NS/AGC off: they colour the
        // voice and the model does its own normalisation.
        audio: { echoCancellation: true, noiseSuppression: false, autoGainControl: false, channelCount: 1 },
      })
      .then(function (stream) {
        if (gen !== st.gen) {
          stopCapture({ stream: stream });
          throw new Error("voice ID disabled");
        }
        // A separate 16 kHz context (the playback context keeps its rate);
        // a browser that cannot feed the mic into it gets the native rate and
        // the worklet resamples.
        return openContext(stream, 16000)
          .catch(function () {
            return openContext(stream, 0);
          })
          .catch(function (err) {
            stopCapture({ stream: stream });
            throw err;
          });
      })
      .then(function (cap) {
        if (gen !== st.gen) {
          stopCapture(cap);
          throw new Error("voice ID disabled");
        }
        st.ring = C.createRing(RING_SEC);
        st.capture = cap;
        cap.node.port.onmessage = function (e) {
          onChunk(cap, e);
        };
        cap.stream.getTracks().forEach(function (t) {
          t.addEventListener("ended", function () {
            if (st.capture === cap) disable();
          });
        });
        if (cap.ctx.state !== "running") cap.ctx.resume().catch(noop);
        return cap;
      });
  }

  // ------------------------------------------------------------ enable / disable

  function enable() {
    if (st.capture && st.workerInit) return Promise.resolve();
    if (st.enabling) return st.enabling;
    var reason = unavailableReason();
    if (reason) return Promise.reject(tagged(new Error("voice ID unavailable: " + reason), reason));
    var gen = st.gen;
    var p = loadCore()
      .catch(function (err) {
        throw tagged(err, "load");
      })
      .then(function () {
        return Promise.all([ensureWorker(), st.capture ? st.capture : startCapture(gen)]);
      })
      .then(function () {
        if (gen !== st.gen) throw new Error("voice ID disabled");
        // Warm-up: one dummy embed of 1.5 s low noise, so the first real
        // utterance does not pay the session start-up.
        return embedSamples(C.lowNoise(1.5)).catch(function (err) {
          throw tagged(err, "load");
        });
      })
      .then(function () {
        st.failed = "";
      })
      .catch(function (err) {
        if (gen === st.gen) {
          var r = errorReason(err);
          st.failed = r;
          disable();
          if (r === "load") terminateWorker();
          notifyUnavailable(r);
          if (root.console) console.warn("[voiceid] unavailable:", err && err.message ? err.message : err);
        }
        throw err;
      });
    st.enabling = p;
    var clear = function () {
      if (st.enabling === p) st.enabling = null;
    };
    p.then(clear, clear);
    return p;
  }

  /* Stops capture (the mic indicator goes off) and drops the audio ring. */
  function disable() {
    st.gen++;
    var cap = st.capture;
    st.capture = null;
    st.ring = null;
    st.enabling = null;
    stopCapture(cap);
    clearAnchor();
    if (st.recorder) st.recorder.abort(new Error("voice ID stopped"));
    if (!settingOn()) terminateWorker();
  }

  // ------------------------------------------------------------ anchors

  function clearAnchor() {
    st.anchor = 0;
    st.anchorSeq++;
    st.keptAt = 0;
    st.inc = null;
    st.incJob = null;
    st.incPostedTotal = 0;
    st.incCheckAt = 0;
  }

  /* Utterance start (first interim after idle, or a PTT press). */
  function mark(ptt, at) {
    var t = typeof at === "number" && isFinite(at) ? at : nowMs();
    if (st.anchor && st.keptAt && t - st.keptAt <= ARM_MS) {
      // a bare "Hermes" was acknowledged: the armed window extends this anchor
      st.keptAt = 0;
      return;
    }
    clearAnchor();
    st.anchor = t;
    st.ptt = !!ptt;
  }

  function keep() {
    if (st.anchor) st.keptAt = nowMs();
  }

  function discard() {
    st.consumedAt = nowMs();
    clearAnchor();
  }

  /* Window start for an anchor: 1.5 s pre-roll, never into an earlier utterance. */
  function windowStart(anchor) {
    return Math.max(anchor - PRE_ROLL_MS, st.consumedAt);
  }

  function maybeIncremental(now) {
    if (!st.anchor || !st.ring || !st.worker || st.incJob) return;
    if (now - st.incCheckAt < INC_CHECK_MS) return;
    st.incCheckAt = now;
    var from = windowStart(st.anchor);
    var total = C.voicedSecondsIn(st.ring, from, now);
    if (total < Math.min(minSec(), INC_STEP_SEC) || total - st.incPostedTotal < INC_STEP_SEC) return;
    var seg = C.cut(st.ring, from, now, { maxSec: MAX_SEC });
    var seq = st.anchorSeq;
    st.incPostedTotal = seg.totalSec;
    var job = { totalSec: seg.totalSec, seq: seq, promise: null };
    job.promise = embedSamples(seg.pcm).then(function (r) {
      if (st.anchorSeq === seq) st.inc = { emb: r.emb, sec: r.sec, totalSec: seg.totalSec };
      return r;
    });
    job.promise.catch(noop).then(function () {
      if (st.incJob === job) st.incJob = null;
    });
    st.incJob = job;
  }

  /*
   * The voice of the utterance that just became final: [anchor - 1.5 s, now],
   * VAD, newest <= 6 s. Uses the incremental embedding when it covers all
   * but <= 0.4 s of the voiced audio; otherwise one final embed. Too little
   * voiced audio -> {short: true, sec}. Not capturing -> null. Clears the anchor.
   */
  function take() {
    var anchor = st.anchor;
    var inc = st.inc;
    var incJob = st.incJob;
    var now = nowMs();
    var from = windowStart(anchor || now - FALLBACK_WINDOW_MS);
    st.consumedAt = now;
    clearAnchor();
    if (!capturing() || !C) return Promise.resolve(null);
    var seg = C.cut(st.ring, from, now, { maxSec: MAX_SEC });
    if (seg.totalSec < minSec()) return Promise.resolve({ short: true, sec: Math.round(seg.totalSec * 100) / 100 });
    if (inc && seg.totalSec - inc.totalSec <= FRESH_SEC) return Promise.resolve(voiceObject(inc.emb, inc.sec));
    var finalEmbed = function () {
      return embedSamples(seg.pcm).then(function (r) {
        return voiceObject(r.emb, r.sec);
      });
    };
    var p;
    if (incJob && seg.totalSec - incJob.totalSec <= FRESH_SEC) {
      p = incJob.promise.then(
        function (r) {
          return voiceObject(r.emb, r.sec);
        },
        function () {
          return finalEmbed();
        }
      );
    } else p = finalEmbed();
    return p.catch(function (err) {
      if (root.console) console.warn("[voiceid] embed failed:", err && err.message ? err.message : err);
      return null;
    });
  }

  // ------------------------------------------------------------ enrolment recording

  function levelFromDb(db) {
    var x = (db + 60) / 45;
    return x < 0 ? 0 : x > 1 ? 1 : x;
  }

  /*
   * Enrolment take: records up to ms (default 6000), stops early after
   * 1.2 s of trailing silence once speech was heard. onLevel(0..1, dB) per
   * 20 ms chunk. Resolves {voice, sec, level}: voice is null when there was
   * too little speech to embed; level is the mean voiced level (0..1).
   */
  function record(ms, onLevel) {
    ms = ms > 0 ? Math.min(ms, 30000) : RECORD_MS;
    if (st.recording || st.recorder) return Promise.reject(new Error("already recording"));
    st.recording = true;
    clearTimeout(st.idleTimer);
    var p = enable().then(function () {
      return new Promise(function (resolve, reject) {
        var start = nowMs();
        var voicedMs = 0;
        var lastVoiceAt = 0;
        var dbSum = 0;
        var dbN = 0;
        var done = false;
        var timer = 0;
        var rec = {};
        function finish(err) {
          if (done) return;
          done = true;
          clearTimeout(timer);
          if (st.recorder === rec) st.recorder = null;
          if (err) {
            reject(err);
            return;
          }
          if (!st.ring) {
            reject(new Error("voice ID stopped"));
            return;
          }
          var seg = C.cut(st.ring, start - 100, nowMs(), { maxSec: RECORD_MAX_SEC });
          var level = dbN ? levelFromDb(dbSum / dbN) : 0;
          if (seg.sec < 0.5) {
            resolve({ voice: null, sec: Math.round(seg.sec * 100) / 100, level: level });
            return;
          }
          embedSamples(seg.pcm).then(function (r) {
            resolve({ voice: voiceObject(r.emb, r.sec), sec: Math.round(r.sec * 100) / 100, level: level });
          }, reject);
        }
        rec.onChunk = function (db, at, muted) {
          if (onLevel) {
            try {
              onLevel(levelFromDb(db), db);
            } catch (e) {
              /* the UI callback must not break the capture */
            }
          }
          if (!muted && st.ring && C.isSpeech(db, st.ring.floor)) {
            voicedMs += 20;
            lastVoiceAt = at;
            dbSum += db;
            dbN++;
          }
          if (voicedMs >= RECORD_MIN_VOICED_MS && at - lastVoiceAt >= RECORD_TRAIL_MS) finish();
        };
        rec.abort = function (err) {
          finish(err);
        };
        st.recorder = rec;
        timer = setTimeout(function () {
          finish();
        }, ms);
      });
    });
    var done = function () {
      st.recording = false;
      scheduleIdleStop();
    };
    p.then(done, done);
    return p;
  }

  /* Enrolment may start capture while the HERMES mic is off: stop it again soon. */
  function scheduleIdleStop() {
    clearTimeout(st.idleTimer);
    if (st.micOn) return;
    st.idleTimer = setTimeout(function () {
      if (!st.micOn && !st.recording && !st.recorder && (st.capture || st.enabling)) disable();
    }, RECORD_IDLE_STOP_MS);
  }

  // ------------------------------------------------------------ lifecycle

  /*
   * Bring capture in line with settings, status and the mic. Capture runs
   * only while the HERMES mic is on (mic:on .. mic:off) and voice ID is
   * wanted, including with zero people (voice enrolment needs audio).
   */
  function sync(trigger) {
    if (!settingOn()) {
      if (st.capture || st.enabling) disable();
      else terminateWorker();
      return;
    }
    if (!st.micOn) return;
    var reason = unavailableReason();
    if (reason) {
      if (reason !== "nostatus") notifyUnavailable(reason);
      return;
    }
    if (st.capture || st.enabling) return;
    // after a failure only a fresh user action retries (no retry loops)
    if (st.failed && trigger !== "mic:on" && trigger !== "gesture") return;
    enable().catch(noop);
  }

  function resumeContext() {
    var cap = st.capture;
    if (cap && cap.ctx && cap.ctx.state === "suspended") cap.ctx.resume().catch(noop);
  }

  function collectVoice() {
    return take();
  }

  var bound = false;
  function bind() {
    var h = H();
    if (bound || !h || typeof h.on !== "function") return bound;
    bound = true;
    if (typeof h.hook === "function") h.hook("collectVoice", collectVoice);
    h.on("utterance:anchor", function (d) {
      mark(!!(d && d.ptt), d && d.at);
    });
    h.on("utterance:keep", keep);
    h.on("utterance:discard", discard);
    h.on("gesture", function () {
      resumeContext();
      sync("gesture");
    });
    h.on("mic:on", function () {
      st.micOn = true;
      clearTimeout(st.idleTimer);
      sync("mic:on");
    });
    h.on("mic:off", function () {
      st.micOn = false;
      if (st.recording || st.recorder) scheduleIdleStop(); // an enrolment take finishes first
      else disable();
    });
    h.on("ready", function () {
      sync("ready");
    });
    h.on("settings", function () {
      sync("settings");
    });
    h.on("status", function () {
      sync("status");
    });
    h.on("visible", resumeContext);
    return true;
  }

  var VoiceID = {
    MODEL_ID: MODEL_ID,
    ORT_VERSION: ORT_VERSION,
    available: available,
    wanted: wanted,
    enable: enable,
    disable: disable,
    mark: mark,
    keep: keep,
    discard: discard,
    take: take,
    record: record,
    embedSamples: embedSamples,
    encode: encode,
    capturing: capturing,
    state: function () {
      return {
        available: available(),
        reason: unavailableReason(),
        wanted: wanted(),
        capturing: capturing(),
        workerReady: !!st.workerInit && !!st.worker,
        failed: st.failed,
        anchor: !!st.anchor,
        noiseFloorDb: st.ring ? st.ring.floor : null,
        sampleRate: st.capture && st.capture.ctx ? st.capture.ctx.sampleRate : null,
      };
    },
  };

  root.VoiceID = VoiceID;

  // Start fetching the core early; voiceid.js works without it until enable().
  if (!C && root.document) loadCore().catch(noop);

  // hermes.js loads before this file (5.1); bind now, or once the DOM is parsed.
  if (!bind() && root.document) {
    document.addEventListener("DOMContentLoaded", function () {
      // index.html loads /hermes.js before /voiceid.js, so this is only a fallback.
      if (!bind() && root.console) console.warn("[voiceid] window.Hermes missing; voice ID stays off");
    });
  }
})(typeof window !== "undefined" ? window : this);
