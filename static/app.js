/*
 * HERMES web client.
 *
 * Sections:
 *   1. helpers (DOM, storage, i18n)        7. chat (send, stream, render)
 *   2. state + settings                    8. pictures overlay + lightbox
 *   3. eye                                 9. settings panel
 *   4. toasts                             10. status pills
 *   5. audio engine + voice FX            11. layout (eye / chat split)
 *   6. speech queue + microphone          12. keyboard + boot
 *
 * Classic script, no build step. Pure logic lives in lib.js (HermesLib).
 */
(function () {
  "use strict";

  var L = window.HermesLib;
  if (!L) {
    // lib.js failed to load; nothing sensible can run.
    return;
  }

  // =================================================================== 1. helpers

  function $(id) {
    return document.getElementById(id);
  }
  function on(el, type, fn, opts) {
    if (el) el.addEventListener(type, fn, opts);
  }
  function now() {
    return Date.now();
  }

  var store = {
    get: function (key, fallback) {
      try {
        var raw = window.localStorage.getItem(key);
        return raw === null ? fallback : JSON.parse(raw);
      } catch (e) {
        return fallback;
      }
    },
    set: function (key, value) {
      try {
        window.localStorage.setItem(key, JSON.stringify(value));
      } catch (e) {
        /* private mode / quota: non-fatal */
      }
    },
  };

  var KEYS = {
    conv: "hermes-conversation",
    piper: "hermes-piper-voice-v2",
    design: "hermes-eye-design",
    eyeFrac: "hermes-eye-frac-v2",
    chat: "hermes-chat-visible",
    onboard: "hermes-onboarded",
    mic: "hermes-mic-wanted",
    inputHistory: "hermes-input-history",
  };

  var MAX_INPUT = 4000;
  var MAX_STORED = 100;
  var ARM_MS = 8000;

  var dom = {
    body: document.body,
    statusDot: $("statusDot"),
    eye: $("eye"),
    eyeZone: $("eyezone"),
    banner: $("eyeBanner"),
    caption: $("caption"),
    transcript: $("transcript"),
    transcriptText: $("transcriptText"),
    chat: $("chat"),
    messages: $("messages"),
    emptyState: $("emptyState"),
    jumpBtn: $("jumpBtn"),
    input: $("msgInput"),
    composer: $("composer"),
    sendBtn: $("sendBtn"),
    micBtn: $("micBtn"),
    newChatBtn: $("newChatBtn"),
    charCount: $("charCount"),
    suggestions: $("suggestions"),
    chatToggle: $("chatToggle"),
    chatBar: $("chatBar"),
    topbar: document.querySelector(".topbar"),
    inputbar: document.querySelector(".inputbar"),
    eyehud: document.querySelector(".eyehud"),
    toasts: $("toasts"),
  };

  var STRINGS = {
    en: {
      idle: "READY",
      listening: "AWAITING CALL",
      typing: "AWAITING INPUT",
      thinking: "PROCESSING",
      speaking: "SPEAKING",
      confused: "UNCERTAIN",
      error: "ERROR",
      armed: "YES?",
      ptt: "GO AHEAD",
      stopped: "STANDING DOWN",
      linkLost: "LINK LOST",
      micBlocked: "MIC BLOCKED",
      ack: "Yes?",
      linkLostText: "Link lost — I can't reach the HERMES server. Is it still running?",
      emptyReply: "I came up empty on that one. Try asking again, or rephrase it.",
      testLine: "This is HERMES. Voice systems online. Protocol three: protect the user.",
      noPictures: "No pictures found.",
    },
    de: {
      idle: "BEREIT",
      listening: "HÖRE ZU",
      typing: "WARTE AUF EINGABE",
      thinking: "DENKE NACH",
      speaking: "SPRICHT",
      confused: "UNSICHER",
      error: "FEHLER",
      armed: "JA?",
      ptt: "SPRICH",
      stopped: "ABGEBROCHEN",
      linkLost: "VERBINDUNG VERLOREN",
      micBlocked: "MIKROFON GESPERRT",
      ack: "Ja?",
      linkLostText: "Verbindung verloren — der HERMES-Server ist nicht erreichbar. Läuft er noch?",
      emptyReply: "Dazu habe ich keine Antwort bekommen. Frag bitte noch einmal oder formuliere es anders.",
      testLine: "Hier spricht HERMES. Sprachsysteme online. Protokoll drei: den Nutzer schützen.",
      noPictures: "Keine Bilder gefunden.",
    },
  };

  var SUGGESTIONS = {
    en: ["What's the weather in Berlin?", "Tell me a joke", "What time is it in Tokyo?"],
    de: ["Wie ist das Wetter in Berlin?", "Erzähl mir einen Witz", "Wie spät ist es in Tokio?"],
  };

  // =================================================================== 2. state

  var settings = L.normalizeSettings(L.DEFAULT_SETTINGS);
  var savedPiper = store.get(KEYS.piper, null);
  if (typeof savedPiper === "string" && savedPiper) settings.piperVoice = savedPiper;
  // last known eye design, so the right optic shows before /api/settings
  // answers (the server value still wins when it arrives)
  settings.eyeDesign = L.normalizeSettings({ eyeDesign: store.get(KEYS.design, settings.eyeDesign) }).eyeDesign;
  if (dom.eye) dom.eye.setAttribute("data-design", settings.eyeDesign);
  var savedSettings = Object.assign({}, settings);

  function isGerman() {
    // browser engine: the language of the voice that will actually speak
    // (also in "Automatic" mode and before the voice list has loaded)
    if (settings.voiceMode === "browser") return /^de\b/i.test(browserSpeechLang());
    return settings.piperVoice === "JarvisDE" || voiceLangByKey[settings.piperVoice] === "de";
  }
  function t(key) {
    var dict = isGerman() ? STRINGS.de : STRINGS.en;
    return dict[key] || STRINGS.en[key] || key;
  }

  var voiceLangByKey = { JarvisDE: "de" };
  var statusInfo = null;
  var piperDown = false; // set when /api/tts says Piper is missing

  var inputFocused = false;

  var gen = { active: false, controller: null, seq: 0 };

  function loadConversation() {
    var raw = store.get(KEYS.conv, []);
    if (!Array.isArray(raw)) return [];
    return raw
      .filter(function (m) {
        return m && (m.role === "user" || m.role === "assistant") && typeof m.content === "string";
      })
      .map(function (m) {
        return {
          id: String(m.id || Math.random().toString(36).slice(2)),
          role: m.role,
          content: m.content,
          ts: typeof m.ts === "number" ? m.ts : now(),
          error: !!m.error,
          stopped: !!m.stopped,
          images: Array.isArray(m.images) ? m.images.filter(function (s) {
            return typeof s === "string" && /^\/gen\//.test(s);
          }) : [],
        };
      })
      .slice(-MAX_STORED);
  }
  var conv = loadConversation();

  function saveConversation() {
    store.set(
      KEYS.conv,
      conv
        .filter(function (m) {
          return !m.pending;
        })
        .slice(-MAX_STORED)
    );
  }

  // =================================================================== 3. eye

  var Eye = window.HermesEye || null;
  if (Eye && dom.eye) {
    try {
      Eye.init(dom.eye);
    } catch (e) {
      Eye = null;
    }
  }

  var eyeView = { override: null, overrideBanner: null, timer: 0, banner: "", state: "" };

  function computeEyeState() {
    if (eyeView.override) return eyeView.override;
    // "speaking" only while audio actually plays; waiting for the next
    // sentence (LLM still generating, TTS fetch) is "thinking"
    if (speech.isAudible()) return "speaking";
    if (gen.active || speech.isActive()) return "thinking";
    // push-to-talk grace: key released, final result still on its way
    if (mic.armed || mic.ptt || mic.pttGrace) return "listening";
    if (mic.running && mic.wanted) return "listening";
    if (inputFocused) return "listening";
    return "idle";
  }

  function computeBanner(state) {
    if (eyeView.override && eyeView.overrideBanner) return eyeView.overrideBanner;
    if (state === "listening") {
      if (mic.ptt || mic.pttGrace) return t("ptt");
      if (mic.armed) return t("armed");
      if (!(mic.running && mic.wanted)) return t("typing");
    }
    return t(state);
  }

  function setBanner(text) {
    var el = dom.banner;
    if (!el || eyeView.banner === text) return;
    eyeView.banner = text;
    el.textContent = text;
    el.classList.remove("swap");
    void el.offsetWidth; // restart the swap animation
    el.classList.add("swap");
  }

  function updateEye() {
    var state = computeEyeState();
    if (state !== eyeView.state) {
      eyeView.state = state;
      if (Eye) Eye.setState(state);
      else if (dom.eye) {
        dom.eye.setAttribute("data-state", state);
        if (dom.eyeZone) dom.eyeZone.setAttribute("data-state", state);
      }
    }
    setBanner(computeBanner(state));
    if (dom.statusDot) dom.statusDot.classList.toggle("listening", state === "listening");
    dom.body.classList.toggle("armed", !!mic.armed);
    updateSendButton();
  }

  /* Show a transient state (confused / error / a banner) for `ms`. */
  function flashEye(state, banner, ms) {
    clearTimeout(eyeView.timer);
    eyeView.override = state;
    eyeView.overrideBanner = banner || null;
    updateEye();
    eyeView.timer = setTimeout(function () {
      eyeView.override = null;
      eyeView.overrideBanner = null;
      updateEye();
    }, ms || 3500);
  }

  function applyEyeDesign(design) {
    if (Eye) Eye.setDesign(design);
    else if (dom.eye) dom.eye.setAttribute("data-design", design);
  }

  // =================================================================== 4. toasts

  var toastKeys = {};

  /*
   * toast(message, {type: info|success|warn|error, timeout ms (0 = sticky),
   *                 action: {label, fn}, key: dedupe id})
   */
  function toast(message, opts) {
    opts = opts || {};
    if (!dom.toasts) return { close: function () {} };
    if (opts.key && toastKeys[opts.key]) toastKeys[opts.key].close(true);
    var el = document.createElement("div");
    el.className = "toast " + (opts.type || "info");
    el.setAttribute("role", opts.type === "error" ? "alert" : "status");
    var msg = document.createElement("span");
    msg.className = "toast-msg";
    msg.textContent = message;
    el.appendChild(msg);
    var closed = false;
    var timer = 0;
    var handle = {
      close: function (instant) {
        if (closed) return;
        closed = true;
        clearTimeout(timer);
        if (opts.key && toastKeys[opts.key] === handle) delete toastKeys[opts.key];
        if (instant) {
          el.remove();
          return;
        }
        el.classList.add("leaving");
        setTimeout(function () {
          el.remove();
        }, 180);
      },
    };
    if (opts.action) {
      var act = document.createElement("button");
      act.type = "button";
      act.className = "toast-action";
      act.textContent = opts.action.label;
      act.addEventListener("click", function () {
        handle.close();
        try {
          opts.action.fn();
        } catch (e) {
          /* ignore */
        }
      });
      el.appendChild(act);
    }
    var x = document.createElement("button");
    x.type = "button";
    x.className = "toast-close";
    x.setAttribute("aria-label", "Dismiss");
    x.textContent = "✕";
    x.addEventListener("click", function () {
      handle.close();
    });
    el.appendChild(x);
    dom.toasts.appendChild(el);
    while (dom.toasts.children.length > 4) dom.toasts.firstChild.remove();
    var timeout = opts.timeout === undefined ? (opts.type === "error" ? 7000 : 4000) : opts.timeout;
    if (timeout > 0) {
      timer = setTimeout(handle.close, timeout);
      el.addEventListener("mouseenter", function () {
        clearTimeout(timer);
      });
      el.addEventListener("mouseleave", function () {
        if (!closed) timer = setTimeout(handle.close, 2000);
      });
    }
    if (opts.key) toastKeys[opts.key] = handle;
    return handle;
  }

  // =================================================================== 5. audio

  var AC = window.AudioContext || window.webkitAudioContext || null;
  var gestureSeen = false;

  function userActivated() {
    if (navigator.userActivation) return navigator.userActivation.hasBeenActive || gestureSeen;
    return gestureSeen;
  }

  var audio = {
    ctx: null,
    master: null,
    analyser: null,
    chains: {},
    meterBuf: null,
    playing: 0,
    meterRaf: 0,

    /* The shared AudioContext; only created after a user gesture so the
       browser never logs an autoplay warning. Returns null if unavailable. */
    get: function () {
      if (audio.ctx) return audio.ctx;
      if (!AC || !userActivated()) return null;
      try {
        var ctx = new AC();
        audio.master = ctx.createGain();
        audio.master.gain.value = settings.volume;
        audio.analyser = ctx.createAnalyser();
        audio.analyser.fftSize = 1024;
        audio.analyser.smoothingTimeConstant = 0.2;
        audio.analyser.connect(audio.master);
        audio.master.connect(ctx.destination);
        audio.meterBuf = new Float32Array(audio.analyser.fftSize);
        audio.ctx = ctx;
      } catch (e) {
        audio.ctx = null;
      }
      return audio.ctx;
    },

    resume: function () {
      var ctx = audio.get();
      if (ctx && ctx.state === "suspended" && ctx.resume) ctx.resume().catch(function () {});
      return ctx;
    },

    setVolume: function (v) {
      if (audio.master && audio.ctx) {
        audio.master.gain.setTargetAtTime(v, audio.ctx.currentTime, 0.03);
      }
    },

    chain: function (mode) {
      var ctx = audio.ctx;
      if (!audio.chains[mode]) {
        audio.chains[mode] = buildFxChain(ctx, mode);
        audio.chains[mode].output.connect(audio.analyser);
      }
      return audio.chains[mode];
    },

    decode: function (arrayBuffer) {
      var ctx = audio.get();
      if (!ctx) return Promise.reject(new Error("no audio context"));
      var copy = arrayBuffer.slice(0); // decodeAudioData detaches its input
      return new Promise(function (resolve, reject) {
        var p = ctx.decodeAudioData(copy, resolve, reject);
        if (p && p.then) p.then(resolve, reject);
      });
    },

    /* Play an AudioBuffer through the FX chain. Returns {done: Promise, stop}. */
    playBuffer: function (buffer) {
      var ctx = audio.resume();
      var src = ctx.createBufferSource();
      src.buffer = buffer;
      src.connect(audio.chain(settings.voiceFx).input);
      var finished = false;
      var resolveDone;
      var done = new Promise(function (r) {
        resolveDone = r;
      });
      function finish() {
        if (finished) return;
        finished = true;
        audio.playing = Math.max(0, audio.playing - 1);
        try {
          src.disconnect();
        } catch (e) {
          /* already disconnected */
        }
        resolveDone();
      }
      src.onended = finish;
      audio.playing++;
      audio.startMeter();
      src.start();
      // safety net in case onended never fires (suspended context etc.)
      setTimeout(finish, buffer.duration * 1000 + 1500);
      return {
        done: done,
        stop: function () {
          try {
            src.stop();
          } catch (e) {
            /* not started */
          }
          finish();
        },
      };
    },

    /* Fallback when no AudioContext is usable: plain <audio>. */
    playElement: function (arrayBuffer) {
      var url = URL.createObjectURL(new Blob([arrayBuffer], { type: "audio/wav" }));
      var el = new Audio(url);
      el.volume = settings.volume;
      var finished = false;
      var resolveDone;
      var done = new Promise(function (r) {
        resolveDone = r;
      });
      function finish() {
        if (finished) return;
        finished = true;
        URL.revokeObjectURL(url);
        resolveDone();
      }
      el.onended = finish;
      el.onerror = finish;
      // No level meter on this path: let the eye run its synthetic pulse.
      el.onplaying = function () {
        if (Eye) Eye.setLevel(null);
      };
      var p = el.play();
      if (p && p.catch) {
        p.catch(function () {
          toast("Click anywhere on the page so HERMES is allowed to speak.", { type: "warn", key: "autoplay" });
          finish();
        });
      }
      return {
        done: done,
        stop: function () {
          try {
            el.pause();
          } catch (e) {
            /* ignore */
          }
          finish();
        },
      };
    },

    startMeter: function () {
      if (audio.meterRaf || !audio.analyser) return;
      var tick = function () {
        if (audio.playing <= 0) {
          audio.meterRaf = 0;
          if (Eye) Eye.setLevel(0);
          return;
        }
        audio.analyser.getFloatTimeDomainData(audio.meterBuf);
        var level = Math.min(1, L.rms(audio.meterBuf) * 3);
        if (Eye) Eye.setLevel(level);
        audio.meterRaf = requestAnimationFrame(tick);
      };
      audio.meterRaf = requestAnimationFrame(tick);
    },
  };

  /*
   * Voice effect chains. Each returns {input, output} GainNodes.
   *   titan: BT-7274 cockpit voice — trimmed lows/highs, presence lift,
   *          compression and a very short metallic slap (helmet resonance).
   *   radio: comms channel — 300-3400 Hz band, light saturation, squashed.
   *   none:  dry.
   */
  function buildFxChain(ctx, mode) {
    var input = ctx.createGain();
    var output = ctx.createGain();
    function biquad(type, freq, q, gain) {
      var f = ctx.createBiquadFilter();
      f.type = type;
      f.frequency.value = freq;
      if (q != null) f.Q.value = q;
      if (gain != null) f.gain.value = gain;
      return f;
    }
    function compressor(threshold, ratio, attack, release) {
      var c = ctx.createDynamicsCompressor();
      c.threshold.value = threshold;
      c.knee.value = 8;
      c.ratio.value = ratio;
      c.attack.value = attack;
      c.release.value = release;
      return c;
    }

    if (mode === "titan") {
      var hp = biquad("highpass", 120, 0.7);
      var body = biquad("lowshelf", 220, null, 2);
      var presence = biquad("peaking", 2500, 0.9, 3);
      var lp = biquad("lowpass", 7500, 0.7);
      var comp = compressor(-22, 3, 0.004, 0.18);
      input.connect(hp);
      hp.connect(body);
      body.connect(presence);
      presence.connect(lp);
      lp.connect(comp);
      var dry = ctx.createGain();
      dry.gain.value = 0.9;
      comp.connect(dry);
      dry.connect(output);
      // short comb: 12 ms delay with low feedback, mixed ~12%
      var delay = ctx.createDelay(0.05);
      delay.delayTime.value = 0.012;
      var fb = ctx.createGain();
      fb.gain.value = 0.22;
      var wet = ctx.createGain();
      wet.gain.value = 0.12;
      comp.connect(delay);
      delay.connect(fb);
      fb.connect(delay);
      delay.connect(wet);
      wet.connect(output);
      output.gain.value = 1.05;
    } else if (mode === "radio") {
      var rhp = biquad("highpass", 300, 0.8);
      var rlp = biquad("lowpass", 3400, 0.8);
      var mid = biquad("peaking", 1800, 1.2, 4);
      var shaper = ctx.createWaveShaper();
      var n = 1024;
      var curve = new Float32Array(n);
      var k = 6;
      for (var i = 0; i < n; i++) {
        var x = (i * 2) / n - 1;
        curve[i] = ((1 + k) * x) / (1 + k * Math.abs(x));
      }
      shaper.curve = curve;
      shaper.oversample = "2x";
      var rcomp = compressor(-28, 6, 0.003, 0.12);
      input.connect(rhp);
      rhp.connect(rlp);
      rlp.connect(mid);
      mid.connect(shaper);
      shaper.connect(rcomp);
      rcomp.connect(output);
      output.gain.value = 0.85;
    } else {
      input.connect(output);
    }
    return { input: input, output: output };
  }

  // --- TTS fetching + client cache

  var ttsCache = new Map(); // key -> ArrayBuffer (LRU)
  var TTS_CACHE_MAX = 40;

  function ttsKey(text, voice, speed) {
    return voice + "|" + speed.toFixed(2) + "|" + text;
  }

  function fetchTTS(text, signal) {
    var voice = settings.piperVoice;
    var speed = settings.voiceSpeed;
    var key = ttsKey(text, voice, speed);
    if (ttsCache.has(key)) {
      var hit = ttsCache.get(key);
      ttsCache.delete(key);
      ttsCache.set(key, hit);
      return Promise.resolve(hit);
    }
    return fetch("/api/tts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text: text, voice: voice, engine: "piper", speed: speed }),
      signal: signal,
    }).then(function (res) {
      if (!res.ok) {
        return res
          .json()
          .catch(function () {
            return {};
          })
          .then(function (j) {
            var err = new Error(j && j.error ? j.error : "TTS failed (" + res.status + ")");
            err.status = res.status;
            throw err;
          });
      }
      return res.arrayBuffer().then(function (ab) {
        ttsCache.set(key, ab);
        while (ttsCache.size > TTS_CACHE_MAX) ttsCache.delete(ttsCache.keys().next().value);
        return ab;
      });
    });
  }

  // --- browser speechSynthesis

  var synth = window.speechSynthesis || null;
  var browserVoices = [];

  function voiceLangFor(name) {
    for (var i = 0; i < browserVoices.length; i++) if (browserVoices[i].name === name) return browserVoices[i].lang || "";
    return "";
  }

  function pickBrowserVoice() {
    if (!browserVoices.length) return null;
    var found = null;
    if (settings.voice) {
      browserVoices.forEach(function (v) {
        if (v.name === settings.voice) found = v;
      });
    }
    if (found) return found;
    var want = fallbackSpeechLang().slice(0, 2).toLowerCase();
    browserVoices.forEach(function (v) {
      if (!found && v.lang && v.lang.toLowerCase().indexOf(want) === 0) found = v;
    });
    return found;
  }

  /* Language to speak in when no (matching) browser voice is known yet. */
  function fallbackSpeechLang() {
    if (settings.piperVoice === "JarvisDE" || voiceLangByKey[settings.piperVoice] === "de") return "de-DE";
    return navigator.language || "en-US";
  }

  /* Language of the browser voice that will actually speak. */
  function browserSpeechLang() {
    var v = pickBrowserVoice();
    if (v && v.lang) return v.lang;
    return fallbackSpeechLang();
  }

  // Chrome stops (network) utterances after ~15 s without firing onend;
  // shorter chunks plus a pause/resume keep-alive avoid that.
  var BROWSER_CHUNK_CHARS = 150;
  var KEEPALIVE_MS = 10000;

  function speakBrowser(text) {
    var resolveDone;
    var done = new Promise(function (r) {
      resolveDone = r;
    });
    if (!synth || typeof SpeechSynthesisUtterance === "undefined") {
      resolveDone();
      return { done: done, stop: function () {} };
    }
    var v = pickBrowserVoice();
    var lang = v && v.lang ? v.lang : fallbackSpeechLang();
    var spoken = L.normalizeForSpeech(text, /^de\b/i.test(lang) ? "de" : "en");
    var pieces = L.splitSentences(spoken, { maxChars: BROWSER_CHUNK_CHARS, minChars: 0 });
    var finished = false;
    var watchdog = 0;
    var keepAlive = 0;
    var idx = 0;

    function cancelSynth() {
      try {
        if (synth.speaking || synth.pending) synth.cancel();
      } catch (e) {
        /* ignore */
      }
    }
    function clearTimers() {
      clearTimeout(watchdog);
      clearInterval(keepAlive);
      watchdog = 0;
      keepAlive = 0;
    }
    function finish() {
      if (finished) return;
      finished = true;
      clearTimers();
      resolveDone();
    }
    function speakNext() {
      clearTimers();
      if (finished) return;
      if (idx >= pieces.length) {
        finish();
        return;
      }
      var piece = pieces[idx++];
      var settled = false;
      function next() {
        if (settled) return;
        settled = true;
        speakNext();
      }
      var u = new SpeechSynthesisUtterance(piece);
      if (v) u.voice = v;
      u.lang = lang;
      u.pitch = settings.pitch;
      u.rate = settings.rate;
      u.volume = settings.volume;
      u.onend = next;
      u.onerror = next;
      // Browser speech has no level meter: use the eye's synthetic pulse.
      u.onstart = function () {
        if (Eye) Eye.setLevel(null);
      };
      try {
        if (synth.paused) synth.resume();
        synth.speak(u);
      } catch (e) {
        next();
        return;
      }
      if (!v || !v.localService) {
        keepAlive = setInterval(function () {
          try {
            if (synth.speaking && !synth.paused) {
              synth.pause();
              synth.resume();
            }
          } catch (e) {
            /* ignore */
          }
        }, KEEPALIVE_MS);
      }
      // Some engines never fire onend; never let the queue hang. A stuck
      // utterance is cancelled, or every later one would queue behind it.
      watchdog = setTimeout(function () {
        if (settled) return;
        cancelSynth();
        next();
      }, 6000 + (piece.length * 110) / Math.max(0.1, settings.rate));
    }
    speakNext();
    return {
      done: done,
      stop: function () {
        idx = pieces.length;
        try {
          synth.cancel();
        } catch (e) {
          /* ignore */
        }
        finish();
      },
    };
  }

  function loadBrowserVoices() {
    if (!synth) return;
    try {
      browserVoices = synth.getVoices() || [];
    } catch (e) {
      browserVoices = [];
    }
    fillBrowserVoiceSelect(settingsOpen ? null : settings.voice);
    refreshLanguage(); // "Automatic" may now resolve to another language
  }

  /*
   * Rebuild the browser-voice select. `want` is the voice to select (null:
   * keep the current selection). A voice that is not available on this device
   * (settings from another browser, or voices not loaded yet) stays selected
   * as a placeholder, so saving the form never silently erases it.
   */
  function fillBrowserVoiceSelect(want) {
    var sel = $("s_voice");
    if (!sel) return;
    var current = want == null ? sel.value : want;
    sel.innerHTML = "";
    var auto = document.createElement("option");
    auto.value = "";
    auto.textContent = "Automatic (match language)";
    sel.appendChild(auto);
    browserVoices.forEach(function (v) {
      var opt = document.createElement("option");
      opt.value = v.name;
      opt.textContent = v.name + " (" + v.lang + ")";
      sel.appendChild(opt);
    });
    if (current && !voiceLangFor(current)) {
      var missing = document.createElement("option");
      missing.value = current;
      missing.textContent = current + (browserVoices.length ? " (not available here)" : " (loading…)");
      sel.appendChild(missing);
    }
    sel.value = current || "";
  }

  // =================================================================== 6a. speech queue

  /*
   * Ordered speech playback. Sentences are enqueued as they stream in; the
   * next sentences' audio is fetched while the current one plays (max 2 in
   * flight); playback is strictly in order. stop() cancels everything.
   */
  var speech = (function () {
    var queue = []; // {text, promise, controller}
    var playing = false;
    var sessionOpen = false;
    var epoch = 0;
    var session = 0; // id of the current begin() session (see begin/enqueue/end)
    var current = null;
    var waiters = [];
    var notified = {};

    function engine() {
      if (settings.voiceMode === "piper" && !piperDown) return "piper";
      return synth ? "browser" : "none";
    }

    function load(item) {
      if (item.promise) return item.promise;
      if (item.engine === "piper") {
        item.controller = typeof AbortController !== "undefined" ? new AbortController() : null;
        item.promise = fetchTTS(L.normalizeForSpeech(item.text, isGerman() ? "de" : "en"), item.controller && item.controller.signal).then(function (ab) {
          if (audio.get()) {
            return audio.decode(ab).then(
              function (buf) {
                return { buffer: buf };
              },
              function () {
                return { raw: ab };
              }
            );
          }
          return { raw: ab };
        });
        item.promise.catch(function () {}); // handled when played
      } else {
        item.promise = Promise.resolve({ browser: true });
      }
      return item.promise;
    }

    function prefetch() {
      var inFlight = 0;
      for (var i = 0; i < queue.length && i < 3; i++) {
        var it = queue[i];
        if (it.promise && !it.settled) inFlight++;
      }
      for (var j = 0; j < queue.length && j < 3 && inFlight < 2; j++) {
        if (!queue[j].promise) {
          load(queue[j]);
          queue[j].watched = true;
          watchSettle(queue[j]);
          inFlight++;
        }
      }
    }

    function watchSettle(item) {
      item.promise.then(
        function () {
          item.settled = true;
          prefetch();
        },
        function () {
          item.settled = true;
          prefetch();
        }
      );
    }

    function handleFailure(err, item) {
      if (err && err.name === "AbortError") return;
      if (err && err.status === 503) {
        piperDown = true;
        setPill("pillVoice", "warn", "Voice: Piper not installed — using the browser voice. Install Piper or set PIPER_BIN to get HERMES' own voice.");
        if (!notified.piper) {
          notified.piper = true;
          toast(synth ? "Piper voice is offline — falling back to the browser voice." : "Piper voice is offline and this browser has no speech engine.", { type: "warn", key: "piper" });
        }
        // re-queue this sentence with the browser voice
        if (synth) {
          item.engine = "browser";
          item.promise = Promise.resolve({ browser: true });
          return true;
        }
      } else if (err && err.status === 400) {
        return false; // nothing speakable (emoji only etc.)
      } else if (!notified.fail) {
        notified.fail = true;
        toast("HERMES lost his voice for a moment: " + ((err && err.message) || "speech failed"), { type: "error", key: "tts" });
      }
      return false;
    }

    function play(result, item) {
      if (result.browser) return speakBrowser(item.text);
      if (result.buffer && audio.ctx) return audio.playBuffer(result.buffer);
      if (result.raw) return audio.playElement(result.raw);
      return { done: Promise.resolve(), stop: function () {} };
    }

    function run() {
      if (playing) return;
      playing = true;
      var my = epoch;
      onActiveChange(true);
      (function next() {
        if (my !== epoch) return;
        if (!queue.length) {
          playing = false;
          current = null;
          setCaption("");
          checkDrained();
          // a streamed reply may still be generating: show "thinking", not a
          // silent "speaking" eye
          updateEye();
          return;
        }
        var item = queue[0];
        load(item);
        if (!item.watched) {
          item.watched = true;
          watchSettle(item);
        }
        prefetch();
        item.promise.then(
          function (result) {
            if (my !== epoch) return;
            queue.shift();
            prefetch();
            setCaption(item.text);
            var handle = (current = play(result, item));
            updateEye(); // audible now
            handle.done.then(function () {
              if (current === handle) current = null;
              if (my !== epoch) return;
              updateEye();
              next();
            });
          },
          function (err) {
            if (my !== epoch) return;
            if (handleFailure(err, item)) {
              next(); // retried with another engine
              return;
            }
            queue.shift();
            next();
          }
        );
      })();
    }

    function checkDrained() {
      if (playing || queue.length || sessionOpen) return;
      var list = waiters;
      waiters = [];
      list.forEach(function (fn) {
        fn();
      });
      onActiveChange(false);
    }

    function stop() {
      epoch++;
      session++; // a stopped session's late enqueue()/end() calls are void
      queue.forEach(function (it) {
        if (it.controller) {
          try {
            it.controller.abort();
          } catch (e) {
            /* ignore */
          }
        }
      });
      queue = [];
      if (current) current.stop();
      current = null;
      if (synth) {
        try {
          synth.cancel();
        } catch (e) {
          /* ignore */
        }
      }
      var wasActive = playing || sessionOpen;
      playing = false;
      sessionOpen = false;
      setCaption("");
      if (Eye) Eye.setLevel(0);
      var list = waiters;
      waiters = [];
      list.forEach(function (fn) {
        fn();
      });
      if (wasActive) onActiveChange(false);
    }

    return {
      isActive: function () {
        return playing || queue.length > 0;
      },
      /* audio is actually playing right now (not fetching / waiting) */
      isAudible: function () {
        return !!current;
      },
      isBusy: function () {
        return playing || queue.length > 0 || sessionOpen;
      },
      /*
       * Start a new utterance session (stops whatever is playing). Returns
       * the session id: enqueue(text, id) / end(id) with an id that is no
       * longer current are ignored, so a reply that is still streaming can't
       * leak sentences into a later say() ("Speak again", "Test voice").
       */
      begin: function () {
        stop();
        notified = {};
        sessionOpen = true;
        return ++session;
      },
      enqueue: function (text, id) {
        if (id != null && id !== session) return;
        text = String(text || "").trim();
        if (!text || engine() === "none") return;
        queue.push({ text: text, engine: engine() });
        prefetch();
        run();
      },
      /* no more sentences will come for this session */
      end: function (id) {
        if (id != null && id !== session) return;
        sessionOpen = false;
        checkDrained();
      },
      say: function (text) {
        var id = this.begin();
        var self = this;
        L.splitSentences(L.stripMarkdown(L.cleanForDisplay(text))).forEach(function (s) {
          self.enqueue(s, id);
        });
        return this.whenIdle(true);
      },
      session: function () {
        return session;
      },
      whenIdle: function (end) {
        var p = new Promise(function (resolve) {
          waiters.push(resolve);
        });
        if (end) this.end();
        else checkDrained();
        return p;
      },
      stop: stop,
    };
  })();

  var captionTimer = 0;
  function setCaption(text) {
    var el = dom.caption;
    if (!el) return;
    clearTimeout(captionTimer);
    if (text) {
      el.textContent = text;
      el.classList.add("show");
      if (dom.transcript) dom.transcript.classList.remove("show");
    } else {
      captionTimer = setTimeout(function () {
        el.classList.remove("show");
      }, 400);
    }
  }

  function onActiveChange(active) {
    if (active) mic.onSpeechStart();
    else mic.onSpeechEnd();
    updateEye();
  }

  // --- wake acknowledgement ("Yes?") cached per voice as a decoded buffer

  // voiceKey -> {raw: Promise<ArrayBuffer>, buffer: AudioBuffer, controller}
  // A small LRU: trying voices / speeds must not keep every "Yes?" around.
  var ackCache = new Map();
  var ACK_CACHE_MAX = 3;
  var ackTimer = 0;

  /* preloadAck after the settings stop changing (slider drags fire per step). */
  function schedulePreloadAck() {
    clearTimeout(ackTimer);
    ackTimer = setTimeout(preloadAck, 600);
  }

  function ackEntry(key) {
    var entry = ackCache.get(key);
    if (entry) {
      ackCache.delete(key);
      ackCache.set(key, entry);
    }
    return entry;
  }

  function ackKey() {
    return settings.piperVoice + "|" + settings.voiceSpeed.toFixed(2);
  }

  function preloadAck() {
    if (settings.voiceMode !== "piper" || piperDown || !settings.speak) return;
    // only once the server confirmed Piper exists (avoids 503 noise)
    if (!statusInfo || !statusInfo.piper || !statusInfo.piper.ok) return;
    clearTimeout(ackTimer);
    var key = ackKey();
    var entry = ackEntry(key);
    if (!entry) {
      entry = { controller: typeof AbortController !== "undefined" ? new AbortController() : null };
      ackCache.set(key, entry);
      entry.raw = fetchTTS(t("ack"), entry.controller && entry.controller.signal);
      entry.raw.catch(function () {
        if (ackCache.get(key) === entry) ackCache.delete(key);
      });
      while (ackCache.size > ACK_CACHE_MAX) {
        var oldKey = ackCache.keys().next().value;
        var old = ackCache.get(oldKey);
        ackCache.delete(oldKey);
        // a superseded preload still waiting for Piper is not needed any more
        if (old && !old.buffer && old.controller) {
          try {
            old.controller.abort();
          } catch (e) {
            /* ignore */
          }
        }
      }
    }
    if (!entry.buffer && audio.get()) {
      entry.raw
        .then(function (ab) {
          return audio.decode(ab);
        })
        .then(function (buf) {
          entry.buffer = buf;
        })
        .catch(function () {});
    }
  }

  /* Plays the acknowledgement; resolves with its duration in ms. */
  function playAck() {
    if (!settings.speak) return Promise.resolve(0);
    if (settings.voiceMode !== "piper" || piperDown) {
      if (!synth) return Promise.resolve(0);
      var h = speakBrowser(t("ack"));
      return h.done.then(function () {
        return 0;
      });
    }
    var entry = ackEntry(ackKey());
    if (entry && entry.buffer && audio.ctx) {
      var p = audio.playBuffer(entry.buffer);
      return p.done.then(function () {
        return 0;
      });
    }
    preloadAck();
    entry = ackEntry(ackKey());
    if (!entry) return Promise.resolve(0);
    return entry.raw
      .then(function (ab) {
        var handle = audio.ctx
          ? audio.decode(ab).then(function (buf) {
              entry.buffer = buf;
              return audio.playBuffer(buf);
            })
          : Promise.resolve(audio.playElement(ab));
        return handle.then(function (hd) {
          return hd.done;
        });
      })
      .then(function () {
        return 0;
      })
      .catch(function () {
        return 0;
      });
  }

  // =================================================================== 6b. microphone

  var SR = window.SpeechRecognition || window.webkitSpeechRecognition || null;

  var mic = {
    supported: !!SR,
    wanted: false,
    running: false,
    rec: null,
    starting: false,
    armed: false,
    armTimer: 0,
    ptt: false,
    pttGrace: false,
    pttTimer: 0,
    guard: false, // HERMES is speaking: only stop commands count
    ignoreUntil: 0,
    failures: 0,
    restartTimer: 0,
    startedAt: 0,
    gotResult: false,
    lastFinal: { text: "", at: 0 },
    pendingWakeAt: 0,
    wakeTimer: 0,
    lastAckAt: 0,
    blocked: false,
    lang: "",
    notified: {},
    transient: false, // last failure was a transient condition
    // On-device recognition (newer Chrome): used when the cloud speech
    // service is unreachable. "unknown" -> not tried yet, "trying", "on",
    // "unavailable".
    local: "unknown",

    shouldRun: function () {
      return mic.supported && !mic.blocked && (mic.wanted || mic.ptt || mic.pttGrace);
    },

    lang_: function () {
      if (isGerman()) return "de-DE";
      return navigator.language || "en-US";
    },

    ensure: function () {
      if (mic.shouldRun()) {
        if (!mic.rec && !mic.restartTimer) mic.start();
      } else if (mic.rec) {
        mic.stop();
      }
      updateMicButton();
      updateEye();
    },

    start: function () {
      clearTimeout(mic.restartTimer);
      mic.restartTimer = 0;
      if (!SR || mic.rec || !mic.shouldRun()) return;
      var rec;
      try {
        rec = new SR();
      } catch (e) {
        mic.supported = false;
        updateMicButton();
        return;
      }
      rec.continuous = true;
      rec.interimResults = true;
      rec.maxAlternatives = 1;
      mic.lang = mic.lang_();
      rec.lang = mic.lang;
      if (mic.local === "on" && "processLocally" in rec) rec.processLocally = true;
      rec.onstart = function () {
        mic.running = true;
        updateMicButton();
        updateEye();
      };
      rec.onresult = function (e) {
        if (rec !== mic.rec) return;
        onRecognitionResult(e);
      };
      rec.onerror = function (e) {
        if (rec !== mic.rec) return;
        onRecognitionError(e && e.error);
      };
      rec.onend = function () {
        if (rec !== mic.rec) return;
        onRecognitionEnd();
      };
      mic.rec = rec;
      mic.startedAt = now();
      mic.gotResult = false;
      try {
        rec.start();
      } catch (e) {
        mic.rec = null;
        mic.running = false;
        mic.scheduleRestart(true);
      }
    },

    stop: function () {
      clearTimeout(mic.restartTimer);
      mic.restartTimer = 0;
      var rec = mic.rec;
      mic.rec = null;
      mic.running = false;
      if (rec) {
        try {
          rec.abort();
        } catch (e) {
          /* ignore */
        }
      }
    },

    /* Abort and start a fresh session (drops buffered audio, e.g. echo). */
    restart: function (delay) {
      mic.stop();
      if (!mic.shouldRun()) return;
      mic.restartTimer = setTimeout(function () {
        mic.restartTimer = 0;
        mic.start();
      }, delay || 0);
    },

    scheduleRestart: function (failed) {
      if (!mic.shouldRun()) {
        updateEye();
        return;
      }
      if (failed) mic.failures++;
      // transient conditions (no mic plugged in, offline) keep retrying on a
      // capped back-off and never switch the always-on mic off
      var plan = L.restartPlan(mic.failures, mic.transient);
      if (plan.giveUp) {
        // off for this page only: the saved preference stays as it was
        mic.wanted = false;
        toast("Microphone stopped after repeated errors. Press M or the mic button to try again.", { type: "error", key: "mic" });
        mic.failures = 0;
        mic.ensure();
        return;
      }
      var delay = plan.delay;
      clearTimeout(mic.restartTimer);
      mic.restartTimer = setTimeout(function () {
        mic.restartTimer = 0;
        mic.start();
      }, delay);
    },

    setWanted: function (want) {
      mic.wanted = !!want && mic.supported;
      if (want) {
        mic.blocked = false;
        mic.failures = 0;
        mic.transient = false;
        mic.notified = {};
        // a user gesture: retry the on-device download if it failed before
        if (mic.local === "unavailable") mic.local = "unknown";
      }
      store.set(KEYS.mic, mic.wanted);
      if (!mic.wanted) disarm();
      mic.ensure();
    },

    onSpeechStart: function () {
      mic.guard = true;
    },

    onSpeechEnd: function () {
      if (!mic.guard) return;
      mic.guard = false;
      // fresh session ~300 ms after HERMES stops talking, so his own words
      // still buffered in the recogniser are never taken as a command
      mic.ignoreUntil = now() + 300;
      if (mic.rec) mic.restart(300);
      else mic.ensure();
    },
  };

  function updateMicButton() {
    var b = dom.micBtn;
    if (!b) return;
    if (!mic.supported) {
      b.hidden = true;
      return;
    }
    // wanted but not started yet (waiting for a gesture): standby, not "on"
    var standby = mic.wanted && !mic.rec && !mic.running && !mic.restartTimer;
    b.setAttribute("aria-pressed", mic.wanted && !standby ? "true" : "false");
    b.classList.toggle("standby", standby);
    b.title = standby
      ? "Microphone ready — click (or M) to start listening for “Hermes” (hold to talk)"
      : mic.wanted
      ? "Microphone on — say “Hermes” first (M to turn off, hold to talk)"
      : "Microphone off (M to turn on, hold to talk)";
    b.classList.toggle("ptt", !!mic.ptt);
  }

  var transcriptTimer = 0;
  function showTranscript(text, kind) {
    var el = dom.transcript;
    if (!el || !dom.transcriptText) return;
    clearTimeout(transcriptTimer);
    if (!text) {
      el.classList.remove("show");
      return;
    }
    if (speech.isActive()) return; // caption has the slot while speaking
    dom.transcriptText.textContent = text.length > 140 ? "…" + text.slice(-140) : text;
    el.classList.toggle("interim", kind === "interim");
    el.classList.toggle("sent", kind === "sent");
    el.classList.add("show");
    transcriptTimer = setTimeout(
      function () {
        el.classList.remove("show");
      },
      kind === "interim" ? 2500 : 1800
    );
  }

  function arm() {
    clearTimeout(mic.armTimer);
    mic.armed = true;
    dom.body.style.setProperty("--armed-ms", ARM_MS + "ms");
    // restart the countdown ring animation
    dom.body.classList.remove("armed");
    void dom.body.offsetWidth;
    mic.armTimer = setTimeout(function () {
      mic.armed = false;
      updateEye();
    }, ARM_MS);
    updateEye();
  }

  /* Restart the armed window (and its countdown ring) while the user talks. */
  var lastExtendAt = 0;
  function extendArm() {
    if (now() - lastExtendAt < 500) return;
    lastExtendAt = now();
    arm();
  }

  function disarm() {
    clearTimeout(mic.armTimer);
    if (mic.armed) {
      mic.armed = false;
      updateEye();
    }
  }

  function ackWake() {
    clearTimeout(mic.wakeTimer);
    mic.pendingWakeAt = 0;
    if (now() - mic.lastAckAt < 2000) return; // re-emitted result
    mic.lastAckAt = now();
    if (speech.isBusy() || gen.active) {
      // a new request interrupts the current one
      stopAll(true);
    }
    arm();
    if (Eye) Eye.blink();
    mic.ignoreUntil = now() + 1500;
    playAck().then(function () {
      mic.ignoreUntil = now() + 350;
    });
  }

  function onRecognitionResult(e) {
    mic.gotResult = true;
    mic.failures = 0;
    mic.transient = false;
    mic.notified = {}; // a later recurrence gets its hint again
    var interim = "";
    var finals = [];
    for (var i = e.resultIndex; i < e.results.length; i++) {
      var r = e.results[i];
      var txt = r[0] && r[0].transcript ? r[0].transcript : "";
      if (r.isFinal) finals.push(txt);
      else interim += txt;
    }
    interim = interim.trim();
    var finalText = finals.join(" ").replace(/\s+/g, " ").trim();

    // Barge-in: react to "Hermes stop" without waiting for a final result.
    var probe = finalText || interim;
    if (probe && (speech.isBusy() || gen.active) && L.isStopCommand(probe)) {
      var w = L.matchWakeWord(probe);
      if (w.woke || speech.isActive()) {
        stopAll();
        showTranscript(probe, "sent");
        return;
      }
    }
    if (mic.guard) return; // HERMES is talking: ignore everything else
    if (now() < mic.ignoreUntil) return;

    if (interim && !finalText) {
      showTranscript(interim, "interim");
      // the user is still talking: keep the armed window open until the
      // final result arrives
      if (mic.armed) extendArm();
      var wi = L.matchWakeWord(interim);
      if (settings.call && wi.woke && wi.bare && !mic.armed && !mic.ptt) {
        // bare wake word in an interim result: answer quickly if nothing
        // else follows, instead of waiting for Chrome's slow final result
        mic.pendingWakeAt = now();
        clearTimeout(mic.wakeTimer);
        mic.wakeTimer = setTimeout(ackWake, 900);
      } else if (!wi.bare) {
        clearTimeout(mic.wakeTimer);
        mic.pendingWakeAt = 0;
      }
      return;
    }
    if (finalText) handleFinal(finalText);
  }

  function handleFinal(text) {
    var norm = L.normalizeSpeech(text);
    if (!norm) return;
    // Chrome re-emits the previous final after a restart: skip duplicates
    if (norm === mic.lastFinal.text && now() - mic.lastFinal.at < 4000) return;
    mic.lastFinal = { text: norm, at: now() };
    clearTimeout(mic.wakeTimer);
    mic.pendingWakeAt = 0;

    var ptt = mic.ptt || mic.pttGrace;
    var d = L.decideFinal(text, { ptt: ptt, call: settings.call, armed: mic.armed });
    if (ptt) endPTTSession();
    if (d.action === "send") {
      disarm();
      sendSpoken(d.command);
    } else if (d.action === "ack") {
      ackWake();
    } else if (d.action === "ignore") {
      showTranscript(text, "interim"); // heard, but not addressed to HERMES
    }
    // "echo": HERMES' own "Yes?" coming back while armed
  }

  function sendSpoken(text) {
    var clean = String(text || "")
      .replace(/^[\s,.;:!?-]+/, "")
      .replace(/[\s.!?,;:]+$/, "")
      .trim();
    if (!clean) {
      ackWake();
      return;
    }
    if (L.isStopCommand(clean)) {
      stopAll();
      return;
    }
    disarm();
    showTranscript(clean, "sent");
    send(clean, { spoken: true });
  }

  var isBrave = !!(navigator.brave && typeof navigator.brave.isBrave === "function");

  /*
   * The cloud speech service is unreachable ("network"). Newer Chrome can
   * recognise speech on the device instead: check, download the language
   * pack if needed, and switch over. Resolves true when on-device is in use.
   */
  function tryOnDeviceRecognition() {
    if (mic.local === "trying" && mic.localPromise) return mic.localPromise;
    if (mic.local !== "unknown") return Promise.resolve(mic.local === "on");
    if (!SR || typeof SR.available !== "function") {
      mic.local = "unavailable";
      return Promise.resolve(false);
    }
    mic.local = "trying";
    var opts = { langs: [mic.lang || mic.lang_()], processLocally: true };
    mic.localPromise = Promise.resolve()
      .then(function () {
        return SR.available(opts);
      })
      .then(function (status) {
        if (status === "available") return true;
        if ((status === "downloadable" || status === "downloading") && typeof SR.install === "function") {
          toast("Downloading Chrome's offline speech model — voice input will work without the internet in a moment.", { type: "info", key: "mic" });
          return SR.install(opts).then(function (ok) {
            return ok !== false;
          });
        }
        return false;
      })
      .then(function (ok) {
        mic.local = ok ? "on" : "unavailable";
        if (ok) {
          mic.failures = 0;
          mic.transient = false;
          toast("Voice input switched to on-device recognition (works offline).", { type: "info", key: "mic" });
          if (mic.shouldRun()) mic.restart(100);
        }
        return ok;
      })
      .catch(function () {
        mic.local = "unavailable";
        return false;
      });
    return mic.localPromise;
  }

  function explainNetworkError() {
    if (mic.notified.network) return;
    mic.notified.network = true;
    if (isBrave) {
      // Brave ships the API without Google's speech backend: it never works
      mic.wanted = false;
      mic.ensure();
      toast("Brave doesn't support voice input (it has no speech service behind it). Open HERMES in Google Chrome or Edge — typing works here.", { type: "error", key: "mic", timeout: 12000 });
      return;
    }
    toast("Voice input can't reach Google's speech service, which Chrome uses for speech recognition. Check that a VPN, firewall or ad blocker isn't blocking Google — HERMES keeps retrying. Typing always works.", { type: "warn", key: "mic", timeout: 12000 });
  }

  function onRecognitionError(code) {
    // on-device mode failed (pack removed, language unsupported): go back
    // to the cloud service instead of treating it as "mic blocked"
    if (mic.local === "on" && (code === "service-not-allowed" || code === "language-not-supported")) {
      mic.local = "unavailable";
      mic.failures++;
      return;
    }
    var policy = L.recognitionErrorPolicy(code);
    if (policy.fatal) {
      mic.blocked = true;
      mic.wanted = false;
      store.set(KEYS.mic, false);
      mic.stop();
      flashEye("error", t("micBlocked"), 4000);
      toast("Microphone access is blocked. Allow it via the icon in the address bar, then press M.", { type: "error", key: "mic", timeout: 9000 });
      updateMicButton();
      return;
    }
    mic.failures += policy.failures;
    if (policy.failures) mic.transient = policy.transient;
    if (code === "audio-capture" && !mic.notified.capture) {
      mic.notified.capture = true;
      toast("No microphone found. Plug one in — HERMES keeps listening for it.", { type: "warn", key: "mic" });
    } else if (code === "network" && !mic.notified.network) {
      tryOnDeviceRecognition().then(function (ok) {
        if (!ok) explainNetworkError();
      });
    } else if (code === "language-not-supported" && !mic.notified.lang) {
      mic.notified.lang = true;
      toast("Speech recognition doesn't support " + mic.lang + ".", { type: "warn", key: "mic" });
    }
  }

  function onRecognitionEnd() {
    mic.rec = null;
    mic.running = false;
    if (mic.pendingWakeAt && now() - mic.pendingWakeAt < 1500) ackWake();
    var quick = !mic.gotResult && now() - mic.startedAt < 1000;
    if (mic.pttGrace && !mic.ptt) {
      endPTTSession();
    }
    mic.scheduleRestart(quick);
    updateEye();
  }

  // --- push-to-talk (hold Space / long-press the mic button)

  function startPTT() {
    if (!mic.supported) {
      toast("Voice input needs Chrome or Edge. You can always type instead.", { type: "warn", key: "ptt" });
      return;
    }
    if (mic.ptt) return;
    unlockAudio();
    if (speech.isBusy() || gen.active) stopAll(true);
    clearTimeout(mic.pttTimer);
    mic.blocked = false;
    mic.ptt = true;
    mic.pttGrace = false;
    mic.guard = false;
    mic.ignoreUntil = 0;
    disarm();
    if (Eye) Eye.blink();
    if (!mic.rec) mic.start();
    updateMicButton();
    updateEye();
  }

  function endPTT() {
    if (!mic.ptt) return;
    mic.ptt = false;
    mic.pttGrace = true; // wait for the final result
    clearTimeout(mic.pttTimer);
    mic.pttTimer = setTimeout(endPTTSession, 2500);
    updateMicButton();
    updateEye();
  }

  function endPTTSession() {
    clearTimeout(mic.pttTimer);
    mic.ptt = false;
    mic.pttGrace = false;
    mic.ensure();
  }

  // =================================================================== 7. chat

  var renderRaf = 0;
  var msgEls = {}; // id -> element

  function newId() {
    return now().toString(36) + Math.random().toString(36).slice(2, 7);
  }

  function nearBottom() {
    var c = dom.chat;
    if (!c) return true;
    return c.scrollHeight - c.scrollTop - c.clientHeight < 90;
  }

  function scrollToBottom(smooth) {
    var c = dom.chat;
    if (!c) return;
    var reduce = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    if (smooth && !reduce && c.scrollTo) c.scrollTo({ top: c.scrollHeight, behavior: "smooth" });
    else c.scrollTop = c.scrollHeight;
    showJump(false);
  }

  function showJump(show) {
    if (!dom.jumpBtn) return;
    dom.jumpBtn.classList.toggle("show", !!show);
    dom.jumpBtn.setAttribute("aria-hidden", show ? "false" : "true");
    dom.jumpBtn.tabIndex = show ? 0 : -1;
  }

  /* Run a DOM update, then keep the view pinned to the bottom if it was. */
  function withScroll(fn, force) {
    var wasNear = nearBottom();
    fn();
    if (force || (wasNear && settings.autoScroll)) scrollToBottom(false);
    else if (!wasNear && !dom.body.classList.contains("chat-hidden")) showJump(true);
  }

  function formatTime(ts) {
    try {
      return new Date(ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
    } catch (e) {
      return "";
    }
  }

  function makeAction(label, title, cls, fn) {
    var b = document.createElement("button");
    b.type = "button";
    b.className = "msg-action " + (cls || "");
    b.textContent = label;
    b.title = title;
    b.setAttribute("aria-label", title);
    b.addEventListener("click", fn);
    return b;
  }

  function buildMessageEl(m) {
    var el = document.createElement("div");
    el.className = "msg " + (m.role === "user" ? "user" : "bot");
    el.dataset.id = m.id;
    var bubble = document.createElement("div");
    bubble.className = "bubble";
    el.appendChild(bubble);
    var meta = document.createElement("div");
    meta.className = "msg-meta";
    el.appendChild(meta);
    msgEls[m.id] = el;
    fillMessageEl(m, el);
    return el;
  }

  function fillMessageEl(m, el) {
    el = el || msgEls[m.id];
    if (!el) return;
    var bubble = el.querySelector(".bubble");
    var meta = el.querySelector(".msg-meta");
    el.classList.toggle("error", !!m.error);
    el.classList.toggle("stopped", !!m.stopped);
    el.classList.toggle("streaming", !!m.pending);
    if (m.role === "user") {
      bubble.textContent = m.content;
    } else {
      var md = bubble.querySelector(".md");
      if (!md) {
        md = document.createElement("div");
        md.className = "md";
        bubble.appendChild(md);
      }
      var text = L.cleanForDisplay(m.content);
      if (m.pending && !text) {
        if (!md.querySelector(".typing")) {
          md.innerHTML = '<span class="typing" aria-label="HERMES is thinking"><span></span><span></span><span></span></span>';
        }
      } else if (!text && m.stopped) {
        md.innerHTML = '<p class="msg-flag">Stopped.</p>';
      } else {
        md.innerHTML = L.renderMarkdown(text); // escaped + sanitised by lib.js
      }
      var imgs = m.images || [];
      var box = bubble.querySelector(".msg-images");
      if (imgs.length) {
        if (!box) {
          box = document.createElement("div");
          box.className = "msg-images";
          bubble.appendChild(box);
        }
        if (box.children.length !== imgs.length) {
          box.innerHTML = "";
          imgs.forEach(function (src) {
            var b = document.createElement("button");
            b.type = "button";
            b.title = "Open picture";
            var img = document.createElement("img");
            img.src = src;
            img.alt = "Picture from HERMES";
            img.loading = "lazy";
            img.addEventListener("error", function () {
              b.remove();
            });
            b.appendChild(img);
            b.addEventListener("click", function () {
              openLightbox(src);
            });
            box.appendChild(b);
          });
        }
      } else if (box) {
        box.remove();
      }
    }

    meta.innerHTML = "";
    var time = document.createElement("time");
    time.className = "msg-time";
    time.textContent = formatTime(m.ts);
    try {
      time.dateTime = new Date(m.ts).toISOString();
      time.title = new Date(m.ts).toLocaleString();
    } catch (e) {
      /* ignore */
    }
    meta.appendChild(time);
    if (m.role === "assistant" && !m.pending) {
      if (m.stopped) {
        var f = document.createElement("span");
        f.className = "msg-flag";
        f.textContent = "· stopped";
        meta.appendChild(f);
      }
      if (m.content) {
        meta.appendChild(
          makeAction("Copy", "Copy message", "copy", function (ev) {
            copyText(L.cleanForDisplay(m.content), ev.currentTarget);
          })
        );
        meta.appendChild(
          makeAction("▶ Speak", "Speak again", "speak", function () {
            unlockAudio();
            speech.say(m.content);
          })
        );
      }
      if (m.error || m.stopped) {
        meta.appendChild(
          makeAction("↻ Retry", "Retry this request", "retry", function () {
            retryMessage(m.id);
          })
        );
      }
    }
  }

  function renderAll() {
    if (!dom.messages) return;
    dom.messages.innerHTML = "";
    msgEls = {};
    var frag = document.createDocumentFragment();
    conv.forEach(function (m) {
      var el = buildMessageEl(m);
      el.style.animation = "none"; // no entrance animation on restore
      frag.appendChild(el);
    });
    dom.messages.appendChild(frag);
    updateEmpty();
    scrollToBottom(false);
  }

  function updateEmpty() {
    var empty = conv.length === 0;
    if (dom.emptyState) dom.emptyState.hidden = !empty;
    renderSuggestions(empty);
    if (dom.newChatBtn) dom.newChatBtn.disabled = empty && !gen.active;
  }

  function addMessage(m) {
    m.id = m.id || newId();
    m.ts = m.ts || now();
    conv.push(m);
    if (conv.length > MAX_STORED) {
      var removed = conv.splice(0, conv.length - MAX_STORED);
      removed.forEach(function (r) {
        if (msgEls[r.id]) msgEls[r.id].remove();
        delete msgEls[r.id];
      });
    }
    withScroll(function () {
      dom.messages.appendChild(buildMessageEl(m));
    }, m.role === "user");
    updateEmpty();
    return m;
  }

  function removeMessage(id) {
    conv = conv.filter(function (m) {
      return m.id !== id;
    });
    if (msgEls[id]) msgEls[id].remove();
    delete msgEls[id];
    saveConversation();
    updateEmpty();
  }

  function copyText(text, btn) {
    var done = function () {
      if (!btn) return;
      var old = btn.textContent;
      btn.textContent = "✓ Copied";
      btn.classList.add("done");
      setTimeout(function () {
        btn.textContent = old;
        btn.classList.remove("done");
      }, 1400);
    };
    var fallback = function () {
      var ta = document.createElement("textarea");
      ta.value = text;
      ta.setAttribute("readonly", "");
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      var ok = false;
      try {
        ok = document.execCommand("copy");
      } catch (e) {
        ok = false;
      }
      ta.remove();
      if (ok) done();
      else toast("Couldn't copy — your browser blocked clipboard access.", { type: "warn" });
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, fallback);
    } else fallback();
  }

  function retryMessage(id) {
    var idx = -1;
    for (var i = 0; i < conv.length; i++) if (conv[i].id === id) idx = i;
    if (idx < 0) return;
    var prevUser = null;
    for (var j = idx - 1; j >= 0; j--) {
      if (conv[j].role === "user") {
        prevUser = conv[j];
        break;
      }
    }
    if (!prevUser) return;
    if (idx === conv.length - 1) {
      stopAll(true);
      removeMessage(id);
      runReply();
    } else {
      send(prevUser.content);
    }
  }

  function stopAll(quiet) {
    var wasBusy = gen.active || speech.isBusy();
    // speech.stop() restarts the recogniser (mic.running goes false for a
    // moment), so decide what to show from what the mic is meant to do
    var listening = mic.shouldRun();
    if (gen.active && gen.controller) {
      try {
        gen.controller.abort();
      } catch (e) {
        /* ignore */
      }
    }
    speech.stop();
    disarm();
    if (wasBusy && !quiet) flashEye(listening ? "listening" : "idle", t("stopped"), 1400);
    updateEye();
  }

  function send(text, opts) {
    opts = opts || {};
    text = String(text || "").trim();
    if (!text) return;
    if (text.length > MAX_INPUT) {
      toast("That message is too long (" + text.length + " / " + MAX_INPUT + " characters).", { type: "warn" });
      return;
    }
    unlockAudio();
    stopAll(true); // a new request replaces the current one
    // the first exchange must not sit under the tips card
    if (onboard && !onboard.hidden) dismissOnboarding();
    if (!opts.spoken) pushInputHistory(text);
    addMessage({ role: "user", content: text });
    saveConversation();
    runReply();
  }

  function readErrorMessage(res) {
    return res
      .json()
      .then(function (j) {
        return j && j.error ? j.error : "HTTP " + res.status;
      })
      .catch(function () {
        return "HTTP " + res.status;
      });
  }

  function runReply() {
    var controller = typeof AbortController !== "undefined" ? new AbortController() : null;
    var my = ++gen.seq;
    gen.active = true;
    gen.controller = controller;
    var bot = addMessage({ role: "assistant", content: "", pending: true, images: [] });
    var payload = { messages: L.buildRequestMessages(conv) };
    var speak = settings.speak && (settings.voiceMode === "browser" ? !!synth : true);
    var streamSpeak = speak && settings.streamSpeech;
    var sentences = streamSpeak ? L.createSentenceStream() : null;
    // own speech session: a later say() ("Speak again", "Test voice") makes
    // this id stale, so the rest of this reply is no longer queued into it
    var speechId = streamSpeak ? speech.begin() : 0;
    function speakSentence(s) {
      speech.enqueue(s, speechId);
    }
    var full = "";
    var pictures = null;
    var serverError = false;
    var stopped = false;
    var failure = null;
    var emptyReply = false;
    updateEye();
    updateEmpty();

    function scheduleRender() {
      if (renderRaf) return;
      renderRaf = requestAnimationFrame(function () {
        renderRaf = 0;
        bot.content = full;
        withScroll(function () {
          fillMessageEl(bot);
        });
      });
    }

    function handleEvent(ev) {
      if (!ev || typeof ev !== "object") return false;
      if (ev.intent === "image") {
        pictures = openPictures();
      }
      if (typeof ev.error === "string") {
        serverError = true;
        flashEye("confused", null, 4000);
      }
      var delta = typeof ev.delta === "string" ? ev.delta : "";
      if (!delta && ev.choices && ev.choices[0] && ev.choices[0].delta && typeof ev.choices[0].delta.content === "string") {
        delta = ev.choices[0].delta.content;
      }
      if (delta) {
        full += delta;
        scheduleRender();
        if (sentences) sentences.push(delta).forEach(speakSentence);
      }
      if (typeof ev.image === "string" && /^\/gen\//.test(ev.image)) {
        bot.images.push(ev.image);
        if (pictures) pictures.add(ev.image);
        scheduleRender();
      }
      return ev.done === true;
    }

    var request = fetch("/api/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
      signal: controller ? controller.signal : undefined,
    }).then(function (res) {
      if (!res.ok) {
        return readErrorMessage(res).then(function (msg) {
          var err = new Error(msg);
          err.http = res.status;
          throw err;
        });
      }
      var ctype = res.headers.get("content-type") || "";
      if (!res.body || !res.body.getReader || ctype.indexOf("text/event-stream") < 0) {
        return res.json().then(function (data) {
          handleEvent({ delta: (data && (data.reply || data.delta)) || "" });
        });
      }
      var reader = res.body.getReader();
      var decoder = new TextDecoder();
      var parser = L.createSSEParser();
      function pump() {
        return reader.read().then(function (r) {
          var finished = false;
          if (r.value) {
            parser.push(decoder.decode(r.value, { stream: true })).forEach(function (ev) {
              if (handleEvent(ev)) finished = true;
            });
          }
          if (r.done) {
            parser.push(decoder.decode());
            parser.end().forEach(handleEvent);
            return;
          }
          if (finished) {
            try {
              reader.cancel();
            } catch (e) {
              /* ignore */
            }
            return;
          }
          return pump();
        });
      }
      return pump();
    });

    return request
      .catch(function (err) {
        if (err && err.name === "AbortError") stopped = true;
        else failure = err || new Error("request failed");
      })
      .then(function () {
        if (renderRaf) {
          cancelAnimationFrame(renderRaf);
          renderRaf = 0;
        }
        var text = L.cleanForDisplay(full).trim();
        if (failure && !text) {
          text = failure.http ? "Request failed: " + failure.message : t("linkLostText");
          flashEye("confused", failure.http ? null : t("linkLost"), 4000);
        } else if (!text && !stopped && !failure && !bot.images.length) {
          // the model finished without a word (empty / thinking-only reply)
          text = t("emptyReply");
          emptyReply = true;
          serverError = true;
          flashEye("confused", null, 4000);
        }
        bot.content = text;
        bot.error = serverError || !!failure;
        bot.stopped = stopped;
        delete bot.pending;
        withScroll(function () {
          fillMessageEl(bot);
        });
        saveConversation();

        if (pictures) {
          pictures.setCaption(L.stripMarkdown(text));
          if (!bot.images.length) pictures.empty(t("noPictures"));
        }

        if (my === gen.seq) {
          gen.active = false;
          gen.controller = null;
        }
        if (streamSpeak) {
          if (!stopped && my === gen.seq) {
            sentences.flush().forEach(speakSentence);
            if (emptyReply) speakSentence(text);
            speech.end(speechId);
          }
        } else if (speak && text && !stopped && !(failure && !serverError) && my === gen.seq) {
          speech.say(text);
        }
        updateEye();
        updateEmpty();
      });
  }

  // --- input box

  var inputHistory = store.get(KEYS.inputHistory, []);
  if (!Array.isArray(inputHistory)) inputHistory = [];
  var historyPos = -1;

  function pushInputHistory(text) {
    if (inputHistory[inputHistory.length - 1] !== text) inputHistory.push(text);
    if (inputHistory.length > 50) inputHistory = inputHistory.slice(-50);
    historyPos = -1;
    store.set(KEYS.inputHistory, inputHistory);
  }

  function autoGrow() {
    var el = dom.input;
    if (!el) return;
    if (!el.value) {
      el.style.height = ""; // CSS height; ignore the placeholder's wrap
    } else {
      el.style.height = "auto";
      el.style.height = Math.max(42, Math.min(el.scrollHeight + 2, 180)) + "px";
    }
    var n = el.value.length;
    if (dom.charCount) {
      dom.charCount.textContent = n > MAX_INPUT * 0.85 ? n + " / " + MAX_INPUT : "";
      dom.charCount.classList.toggle("over", n >= MAX_INPUT);
    }
    scheduleLayout();
  }

  function updateSendButton() {
    var b = dom.sendBtn;
    if (!b) return;
    var busy = gen.active || speech.isBusy();
    var empty = !dom.input || !dom.input.value.trim();
    var stopMode = busy && empty;
    b.classList.toggle("stop", stopMode);
    var label = b.querySelector(".send-label");
    if (label) label.textContent = stopMode ? "Stop" : "Send";
    b.setAttribute("aria-label", stopMode ? "Stop (Esc)" : "Send message");
    b.title = stopMode ? "Stop talking / generating (Esc)" : "Send (Enter)";
  }

  function submitInput() {
    var text = dom.input ? dom.input.value : "";
    if (!text.trim()) {
      if (gen.active || speech.isBusy()) stopAll();
      else if (dom.input) dom.input.focus();
      return;
    }
    dom.input.value = "";
    autoGrow();
    updateSendButton();
    send(text); // the chat stays as it is: the reply is spoken and captioned
  }

  on(dom.composer, "submit", function (e) {
    e.preventDefault();
    submitInput();
  });

  on(dom.input, "keydown", function (e) {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing && e.keyCode !== 229) {
      e.preventDefault();
      submitInput();
      return;
    }
    if ((e.key === "ArrowUp" || e.key === "ArrowDown") && !e.shiftKey && !e.altKey && !e.metaKey && !e.ctrlKey) {
      var el = dom.input;
      var browsing = historyPos !== -1;
      if (!browsing && el.value) return;
      if (!inputHistory.length) return;
      e.preventDefault();
      if (e.key === "ArrowUp") {
        historyPos = historyPos === -1 ? inputHistory.length - 1 : Math.max(0, historyPos - 1);
      } else {
        if (historyPos === -1) return;
        historyPos++;
        if (historyPos >= inputHistory.length) {
          historyPos = -1;
          el.value = "";
          autoGrow();
          updateSendButton();
          return;
        }
      }
      el.value = inputHistory[historyPos];
      autoGrow();
      updateSendButton();
      try {
        el.setSelectionRange(el.value.length, el.value.length);
      } catch (err) {
        /* ignore */
      }
    }
  });

  on(dom.input, "input", function () {
    historyPos = -1;
    autoGrow();
    updateSendButton();
  });
  on(dom.input, "focus", function () {
    inputFocused = true;
    updateEye();
  });
  on(dom.input, "blur", function () {
    inputFocused = false;
    updateEye();
  });

  // --- suggestions

  function renderSuggestions(show) {
    var box = dom.suggestions;
    if (!box) return;
    var want = show ? SUGGESTIONS[isGerman() ? "de" : "en"] : [];
    if (box.dataset.lang === (show ? (isGerman() ? "de" : "en") : "none")) return;
    box.dataset.lang = show ? (isGerman() ? "de" : "en") : "none";
    box.innerHTML = "";
    want.forEach(function (s) {
      var b = document.createElement("button");
      b.type = "button";
      b.className = "chip";
      b.textContent = s;
      b.addEventListener("click", function () {
        send(s);
      });
      box.appendChild(b);
    });
    scheduleLayout();
  }

  // --- new chat / export

  function newChat() {
    if (!conv.length) {
      if (dom.input) dom.input.focus();
      return;
    }
    stopAll(true);
    closePictures();
    var backup = conv.slice();
    conv = [];
    saveConversation();
    renderAll();
    toast("Chat cleared.", {
      timeout: 7000,
      key: "clear",
      action: {
        label: "Undo",
        fn: function () {
          if (conv.length) backup = backup.concat(conv);
          conv = backup.slice(-MAX_STORED);
          saveConversation();
          renderAll();
        },
      },
    });
    if (dom.input) dom.input.focus();
  }

  function exportChat() {
    if (!conv.length) {
      toast("Nothing to export yet — say something first.", { type: "info" });
      return;
    }
    var md = L.formatChatAsMarkdown(conv, { date: now(), title: "HERMES conversation" });
    var d = new Date();
    var pad = function (n) {
      return (n < 10 ? "0" : "") + n;
    };
    var name = "hermes-chat-" + d.getFullYear() + pad(d.getMonth() + 1) + pad(d.getDate()) + "-" + pad(d.getHours()) + pad(d.getMinutes()) + ".md";
    try {
      var url = URL.createObjectURL(new Blob([md], { type: "text/markdown;charset=utf-8" }));
      var a = document.createElement("a");
      a.href = url;
      a.download = name;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(function () {
        URL.revokeObjectURL(url);
      }, 2000);
      toast("Chat exported as " + name, { type: "success" });
    } catch (e) {
      toast("Export failed: " + e.message, { type: "error" });
    }
  }

  on(dom.newChatBtn, "click", newChat);
  on($("exportBtn"), "click", exportChat);
  on(dom.jumpBtn, "click", function () {
    scrollToBottom(true);
  });
  on(dom.chat, "scroll", function () {
    if (nearBottom()) showJump(false);
  }, { passive: true });

  // =================================================================== 8. pictures + lightbox

  var overlay = $("picturesOverlay");
  var lastFocus = null;

  /* Animated show/hide for elements toggled with [hidden] + .open */
  function reveal(el) {
    if (!el) return;
    clearTimeout(el._hideTimer);
    el.hidden = false;
    void el.offsetWidth;
    el.classList.add("open");
  }
  function conceal(el, after) {
    if (!el || el.hidden) {
      if (after) after();
      return;
    }
    el.classList.remove("open");
    clearTimeout(el._hideTimer);
    el._hideTimer = setTimeout(function () {
      el.hidden = true;
      if (after) after();
    }, 330);
  }

  function openPictures() {
    var grid = $("picturesGrid");
    var cap = $("picturesCaption");
    if (!overlay || !grid) {
      return { add: function () {}, setCaption: function () {}, empty: function () {} };
    }
    grid.innerHTML = "";
    if (cap) cap.textContent = "";
    var loading = document.createElement("div");
    loading.className = "pic pic-loading";
    grid.appendChild(loading);
    if (overlay.hidden) lastFocus = document.activeElement;
    reveal(overlay);
    var closeBtn = $("picturesClose");
    if (closeBtn) closeBtn.focus({ preventScroll: true });
    return {
      add: function (src) {
        if (loading.parentNode) loading.remove();
        var fig = document.createElement("figure");
        fig.className = "pic";
        var b = document.createElement("button");
        b.type = "button";
        b.title = "View full size";
        var img = document.createElement("img");
        img.alt = "Picture from HERMES";
        img.addEventListener("load", function () {
          img.classList.add("loaded");
        });
        img.addEventListener("error", function () {
          fig.remove();
        });
        img.src = src;
        b.appendChild(img);
        b.addEventListener("click", function () {
          openLightbox(src);
        });
        var fc = document.createElement("figcaption");
        var dl = document.createElement("a");
        dl.className = "small-link";
        dl.href = src;
        dl.download = src.split("/").pop();
        dl.textContent = "Download";
        fc.appendChild(dl);
        fig.appendChild(b);
        fig.appendChild(fc);
        grid.appendChild(fig);
      },
      setCaption: function (text) {
        if (cap) cap.textContent = text;
      },
      empty: function (text) {
        if (loading.parentNode) loading.remove();
        var p = document.createElement("p");
        p.className = "placeholder";
        p.textContent = text;
        grid.appendChild(p);
      },
    };
  }

  function closePictures() {
    if (!overlay || overlay.hidden) return;
    conceal(overlay);
    if (lastFocus && lastFocus.focus) lastFocus.focus({ preventScroll: true });
  }

  on(overlay, "click", function (e) {
    if (e.target === overlay) closePictures();
  });
  on($("picturesClose"), "click", closePictures);

  var lightbox = $("lightbox");
  function openLightbox(src) {
    if (!lightbox || typeof lightbox.showModal !== "function") {
      window.open(src, "_blank", "noopener");
      return;
    }
    var img = $("lightboxImg");
    if (img) img.src = src;
    var dl = $("lightboxDownload");
    if (dl) {
      dl.href = src;
      dl.download = src.split("/").pop();
    }
    var op = $("lightboxOpen");
    if (op) op.href = src;
    if (!lightbox.open) lightbox.showModal();
  }
  on($("lightboxClose"), "click", function () {
    if (lightbox && lightbox.open) lightbox.close();
  });
  on(lightbox, "click", function (e) {
    if (e.target === lightbox || e.target.id === "lightboxImg") lightbox.close();
  });

  var shortcuts = $("shortcutsDialog");
  function toggleShortcuts() {
    if (!shortcuts || typeof shortcuts.showModal !== "function") return;
    if (shortcuts.open) shortcuts.close();
    else shortcuts.showModal();
  }
  on($("helpBtn"), "click", toggleShortcuts);
  on($("shortcutsClose"), "click", function () {
    if (shortcuts && shortcuts.open) shortcuts.close();
  });
  on(shortcuts, "click", function (e) {
    if (e.target === shortcuts) shortcuts.close();
  });

  // =================================================================== 9. settings panel

  var panel = $("settingsPanel");
  var backdrop = $("settingsBackdrop");
  var settingsOpen = false;
  var settingsReturnFocus = null;

  function field(id) {
    return $(id);
  }
  function setVal(id, v) {
    var el = field(id);
    if (el) el.value = v;
  }
  function setChecked(id, v) {
    var el = field(id);
    if (el) el.checked = !!v;
  }
  function getVal(id, fallback) {
    var el = field(id);
    return el ? el.value : fallback;
  }
  function getChecked(id, fallback) {
    var el = field(id);
    return el ? !!el.checked : fallback;
  }

  /*
   * Rebuild the model select. `keepSelection`: a status refresh while the
   * panel is open only updates the options; the user's current choice
   * (including "Custom…" with its text field) is kept.
   */
  function populateModels(current, keepSelection) {
    var sel = field("s_modelselect");
    if (!sel) return;
    if (keepSelection && sel.value === "__custom__") {
      current = null; // rebuild the list but stay on "Custom…"
    } else if (keepSelection && sel.value) {
      current = sel.value;
    }
    var stayCustom = keepSelection && current === null;
    var models = statusInfo && statusInfo.ollama && Array.isArray(statusInfo.ollama.models) ? statusInfo.ollama.models.slice() : [];
    sel.innerHTML = "";
    var inList = models.indexOf(current) >= 0;
    models.forEach(function (name) {
      var o = document.createElement("option");
      o.value = name;
      o.textContent = name;
      sel.appendChild(o);
    });
    if (current && !inList) {
      var cur = document.createElement("option");
      cur.value = current;
      cur.textContent = current + (statusInfo && statusInfo.ollama && statusInfo.ollama.ok ? " (not installed)" : "");
      sel.insertBefore(cur, sel.firstChild);
    }
    var custom = document.createElement("option");
    custom.value = "__custom__";
    custom.textContent = "Custom…";
    sel.appendChild(custom);
    sel.value = current || "__custom__";
    var wrap = $("customModelWrap");
    if (stayCustom) {
      if (wrap) wrap.hidden = false;
      updateModelHint(getVal("s_localmodel", "").trim());
      return;
    }
    if (wrap) wrap.hidden = true;
    setVal("s_localmodel", current || "");
    updateModelHint(current);
  }

  function updateModelHint(model) {
    var hint = $("modelHint");
    if (!hint) return;
    var o = statusInfo && statusInfo.ollama;
    hint.classList.remove("warn");
    if (!o) {
      hint.textContent = "";
    } else if (!o.ok) {
      hint.textContent = "Ollama is offline — start it with “ollama serve” to list installed models.";
      hint.classList.add("warn");
    } else if (model && (o.models || []).indexOf(model) < 0 && (o.models || []).indexOf(model + ":latest") < 0) {
      hint.textContent = "Not installed yet — run “ollama pull " + model + "”.";
      hint.classList.add("warn");
    } else {
      hint.textContent = (o.models || []).length + " model(s) installed in Ollama.";
    }
  }

  function populatePiperVoices() {
    var sel = field("s_pipervoice");
    var voices = statusInfo && Array.isArray(statusInfo.voices) ? statusInfo.voices : [];
    voices.forEach(function (v) {
      if (v && v.key && v.lang) voiceLangByKey[v.key] = v.lang;
    });
    if (!sel || !voices.length) return;
    var cur = settingsOpen ? sel.value : settings.piperVoice;
    sel.innerHTML = "";
    voices.forEach(function (v) {
      if (!v || !v.key) return;
      var o = document.createElement("option");
      o.value = v.key;
      o.textContent = (v.name || v.key) + (v.lang && v.lang !== "en" ? " (" + v.lang.toUpperCase() + ")" : "");
      sel.appendChild(o);
    });
    sel.value = cur;
    if (sel.value !== cur && sel.options.length) sel.value = sel.options[0].value;
  }

  function fillForm(s) {
    populateModels(s.localModel);
    setVal("s_prompt", s.systemPrompt);
    setVal("s_temp", s.temperature);
    setVal("s_max", s.maxTokens);
    setVal("s_engine", s.voiceMode);
    var pv = field("s_pipervoice");
    if (pv) {
      var exists = false;
      for (var i = 0; i < pv.options.length; i++) if (pv.options[i].value === s.piperVoice) exists = true;
      if (exists) pv.value = s.piperVoice;
    }
    setVal("s_speed", s.voiceSpeed);
    setVal("s_fx", s.voiceFx);
    fillBrowserVoiceSelect(s.voice || "");
    setVal("s_pitch", s.pitch);
    setVal("s_rate", s.rate);
    setVal("s_volume", s.volume);
    setVal("s_eye", s.eyeDesign);
    setChecked("s_stream", s.streamSpeech);
    setChecked("s_say", s.speak);
    setChecked("s_call", s.call);
    setChecked("s_auto", s.autoScroll);
    refreshFormUi();
  }

  function readForm() {
    var sel = getVal("s_modelselect", "__custom__");
    var model = sel === "__custom__" ? getVal("s_localmodel", "").trim() : sel;
    return L.normalizeSettings({
      localModel: model || settings.localModel,
      systemPrompt: getVal("s_prompt", settings.systemPrompt),
      temperature: L.parseNum(getVal("s_temp"), settings.temperature),
      maxTokens: L.parseNum(getVal("s_max"), settings.maxTokens),
      voiceMode: getVal("s_engine", settings.voiceMode),
      piperVoice: getVal("s_pipervoice", settings.piperVoice) || settings.piperVoice,
      voiceSpeed: L.parseNum(getVal("s_speed"), settings.voiceSpeed),
      voiceFx: getVal("s_fx", settings.voiceFx),
      voice: getVal("s_voice", settings.voice),
      pitch: L.parseNum(getVal("s_pitch"), settings.pitch),
      rate: L.parseNum(getVal("s_rate"), settings.rate),
      volume: L.parseNum(getVal("s_volume"), settings.volume),
      eyeDesign: getVal("s_eye", settings.eyeDesign),
      streamSpeech: getChecked("s_stream", settings.streamSpeech),
      speak: getChecked("s_say", settings.speak),
      call: getChecked("s_call", settings.call),
      autoScroll: getChecked("s_auto", settings.autoScroll),
    });
  }

  function refreshFormUi() {
    var fmt = function (id, out, digits, suffix) {
      var el = field(id);
      var o = field(out);
      if (el && o) o.textContent = Number(el.value).toFixed(digits) + (suffix || "");
    };
    fmt("s_speed", "s_speed_val", 2, "×");
    fmt("s_pitch", "s_pitch_val", 1);
    fmt("s_rate", "s_rate_val", 1, "×");
    var vol = field("s_volume");
    var vo = field("s_vol_val");
    if (vol && vo) vo.textContent = Math.round(Number(vol.value) * 100) + "%";
    var engine = getVal("s_engine", "piper");
    var bg = $("browserVoiceGroup");
    var pg = $("piperVoiceGroup");
    if (bg) bg.hidden = engine !== "browser";
    if (pg) pg.hidden = engine !== "piper";
    var custom = getVal("s_modelselect") === "__custom__";
    var wrap = $("customModelWrap");
    if (wrap) wrap.hidden = !custom;
  }

  function settingsEqual(a, b) {
    return JSON.stringify(a) === JSON.stringify(b);
  }

  function setDirty(dirty) {
    var badge = $("unsavedBadge");
    if (badge) badge.hidden = !dirty;
    dom.body.classList.toggle("settings-dirty", !!dirty);
  }

  /* Apply settings that have an immediate visible / audible effect. */
  function applyLive(prev) {
    applyEyeDesign(settings.eyeDesign);
    audio.setVolume(settings.volume);
    if (!prev || prev.piperVoice !== settings.piperVoice || prev.voiceSpeed !== settings.voiceSpeed || prev.voiceMode !== settings.voiceMode) {
      schedulePreloadAck();
    }
    // any setting can change the language (Piper voice, engine, or the
    // browser voice itself): banner, suggestions and recognition follow it
    refreshLanguage(!prev);
  }

  var shownLang = "";

  /* Re-render language-dependent UI when isGerman() changed (or `force`). */
  function refreshLanguage(force) {
    var lang = isGerman() ? "de" : "en";
    if (lang !== shownLang || force) {
      shownLang = lang;
      eyeView.banner = "";
      updateEye();
      renderSuggestionsForce();
    }
    if (mic.rec && mic.lang !== mic.lang_()) mic.restart(100);
  }

  function renderSuggestionsForce() {
    if (dom.suggestions) dom.suggestions.dataset.lang = "";
    updateEmpty();
  }

  function onFormChange() {
    refreshFormUi();
    var prev = settings;
    settings = readForm();
    applyLive(prev);
    setDirty(!settingsEqual(settings, savedSettings));
    if (getVal("s_modelselect") !== "__custom__") updateModelHint(settings.localModel);
  }

  function focusables() {
    if (!panel) return [];
    return Array.prototype.filter.call(
      panel.querySelectorAll("button, select, input, textarea, [href], [tabindex]:not([tabindex='-1'])"),
      function (el) {
        return !el.disabled && el.offsetParent !== null;
      }
    );
  }

  function openSettings() {
    if (!panel || settingsOpen) return;
    settingsOpen = true;
    settingsReturnFocus = document.activeElement;
    fillForm(settings);
    setDirty(!settingsEqual(settings, savedSettings));
    reveal(backdrop);
    reveal(panel);
    var btn = $("settingsBtn");
    if (btn) btn.setAttribute("aria-expanded", "true");
    setTimeout(function () {
      var f = focusables();
      if (f.length > 1) f[1].focus({ preventScroll: true });
    }, 60);
    refreshStatus();
  }

  var savePending = null; // Promise while a POST /api/settings is in flight

  function closeSettings() {
    if (!panel || !settingsOpen) return;
    if (savePending) {
      // the draft is being saved: close without discarding it; the save's
      // own success / error toast reports the outcome
      settingsOpen = false;
      var sb = $("settingsBtn");
      if (sb) sb.setAttribute("aria-expanded", "false");
      conceal(backdrop);
      conceal(panel);
      savePending.then(function (ok) {
        if (ok || settingsOpen || settingsEqual(settings, savedSettings)) return;
        // the save failed: fall back to what is stored, as a discard would
        var prevS = settings;
        settings = Object.assign({}, savedSettings);
        applyLive(prevS);
        setDirty(false);
      });
      if (settingsReturnFocus && settingsReturnFocus.focus) settingsReturnFocus.focus({ preventScroll: true });
      return;
    }
    settingsOpen = false;
    var btn = $("settingsBtn");
    if (btn) btn.setAttribute("aria-expanded", "false");
    conceal(backdrop);
    conceal(panel);
    if (!settingsEqual(settings, savedSettings)) {
      var draft = settings;
      var prev = settings;
      settings = Object.assign({}, savedSettings);
      applyLive(prev);
      setDirty(false);
      toast("Unsaved settings discarded.", {
        type: "warn",
        timeout: 6000,
        key: "discard",
        action: {
          label: "Undo",
          fn: function () {
            openSettings();
            fillForm(draft);
            onFormChange();
          },
        },
      });
    }
    if (settingsReturnFocus && settingsReturnFocus.focus) settingsReturnFocus.focus({ preventScroll: true });
  }

  function saveSettingsNow() {
    if (savePending) return savePending;
    var draft = readForm();
    var btn = $("saveBtn");
    if (btn) btn.disabled = true;
    savePending = fetch("/api/settings", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(draft),
    })
      .then(function (res) {
        if (!res.ok) return readErrorMessage(res).then(function (m) {
          throw new Error(m);
        });
        return res.json();
      })
      .then(function (saved) {
        var prev = settings;
        settings = L.normalizeSettings(saved);
        savedSettings = Object.assign({}, settings);
        store.set(KEYS.piper, settings.piperVoice);
        store.set(KEYS.design, settings.eyeDesign);
        if (settingsOpen) fillForm(settings);
        setDirty(false);
        applyLive(prev);
        refreshStatus();
        toast("Settings saved.", { type: "success", key: "save" });
        return true;
      })
      .catch(function (err) {
        toast("Couldn't save settings: " + (err && err.message ? err.message : "server unreachable"), { type: "error", key: "save" });
        return false;
      })
      .then(function (ok) {
        savePending = null;
        if (btn) btn.disabled = false;
        return ok;
      });
    return savePending;
  }

  function resetSettings() {
    var defaults = L.normalizeSettings(L.DEFAULT_SETTINGS);
    fillForm(defaults);
    onFormChange();
    toast("Defaults loaded — press Save to keep them.", { key: "reset" });
  }

  function testVoice() {
    unlockAudio();
    if (!settings.speak) {
      toast("“Speak replies out loud” is off — testing anyway.", { key: "test" });
    }
    speech.say(t("testLine"));
  }

  on($("settingsBtn"), "click", function () {
    if (settingsOpen) closeSettings();
    else openSettings();
  });
  on($("closeSettingsBtn"), "click", closeSettings);
  on(backdrop, "click", closeSettings);
  on($("saveBtn"), "click", saveSettingsNow);
  on($("resetBtn"), "click", resetSettings);
  on($("testTTSBtn"), "click", testVoice);
  on(panel, "input", onFormChange);
  on(panel, "change", onFormChange);
  on(panel, "keydown", function (e) {
    if (e.key === "Tab") {
      var f = focusables();
      if (!f.length) return;
      var first = f[0];
      var last = f[f.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    } else if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      saveSettingsNow();
    }
  });

  function fetchSettings() {
    return fetch("/api/settings", { cache: "no-store" })
      .then(function (res) {
        if (!res.ok) throw new Error("HTTP " + res.status);
        return res.json();
      })
      .then(function (s) {
        var prev = settings;
        settings = L.normalizeSettings(s);
        savedSettings = Object.assign({}, settings);
        store.set(KEYS.piper, settings.piperVoice);
        store.set(KEYS.design, settings.eyeDesign);
        applyLive(prev);
        if (dom.statusDot) dom.statusDot.classList.add("online");
      })
      .catch(function () {
        applyLive(null);
        toast("Couldn't load settings — using defaults. Is the HERMES server running?", { type: "warn", key: "settings-load" });
      });
  }

  // =================================================================== 10. status pills

  var lastStatusAt = 0;

  function setPill(id, state, tip) {
    var el = $(id);
    if (!el) return;
    el.dataset.state = state;
    el.title = tip;
    el.setAttribute("aria-label", tip);
    el.dataset.tip = tip;
  }

  function refreshStatus() {
    lastStatusAt = now();
    var controller = typeof AbortController !== "undefined" ? new AbortController() : null;
    var timer = setTimeout(function () {
      if (controller) controller.abort();
    }, 6000);
    return fetch("/api/status", { cache: "no-store", signal: controller ? controller.signal : undefined })
      .then(function (res) {
        if (!res.ok) throw new Error("HTTP " + res.status);
        return res.json();
      })
      .then(function (st) {
        statusInfo = st || {};
        if (dom.statusDot) dom.statusDot.classList.add("online");
        var o = statusInfo.ollama || {};
        var models = Array.isArray(o.models) ? o.models : [];
        var model = settings.localModel;
        var hasModel = models.indexOf(model) >= 0 || models.indexOf(model + ":latest") >= 0;
        if (!o.ok) {
          setPill("pillOllama", "bad", "Brain offline: " + (o.error || "Ollama is not reachable") + ". Start Ollama (“ollama serve”) and check OLLAMA_BASE.");
        } else if (!hasModel) {
          setPill("pillOllama", "warn", "Brain online, but the model “" + model + "” isn't installed. Run “ollama pull " + model + "” or pick another model in Settings.");
        } else {
          setPill("pillOllama", "ok", "Brain online — Ollama is running " + model + ".");
        }
        var piperOk = !!(statusInfo.piper && statusInfo.piper.ok);
        piperDown = !piperOk;
        if (settings.voiceMode === "browser") {
          setPill("pillVoice", synth ? "ok" : "bad", synth ? "Voice: using the browser's speech engine." : "Voice: this browser has no speech engine.");
        } else if (piperOk) {
          setPill("pillVoice", "ok", "Voice online — local Piper is ready.");
        } else {
          setPill("pillVoice", "warn", "Voice: Piper isn't installed, so HERMES uses the browser voice. Install Piper or set PIPER_BIN (the Docker image bundles it).");
        }
        if (statusInfo.mistral && statusInfo.mistral.enabled) {
          setPill("pillSat", "ok", "Satellite uplink online — live weather, time, news and image generation via Mistral.");
        } else {
          setPill("pillSat", "off", "Satellite offline (optional): add MISTRAL_API_KEY to .env for live weather, time, news and image generation.");
        }
        populatePiperVoices();
        if (settingsOpen) populateModels(settings.localModel, true);
        preloadAck();
      })
      .catch(function () {
        if (dom.statusDot) dom.statusDot.classList.remove("online");
        var tip = "HERMES server unreachable — is “go run .” or the Docker container running?";
        setPill("pillOllama", "bad", tip);
        setPill("pillVoice", "bad", tip);
        setPill("pillSat", "bad", tip);
      })
      .then(function () {
        clearTimeout(timer);
      });
  }

  ["pillOllama", "pillVoice", "pillSat"].forEach(function (id) {
    on($(id), "click", function (e) {
      var tip = e.currentTarget.dataset.tip;
      var state = e.currentTarget.dataset.state;
      if (tip) toast(tip, { type: state === "ok" ? "success" : state === "bad" ? "error" : state === "warn" ? "warn" : "info", key: "pill" });
    });
  });

  setInterval(function () {
    if (document.visibilityState !== "hidden") refreshStatus();
  }, 30000);
  on(window, "focus", function () {
    if (now() - lastStatusAt > 5000) refreshStatus();
  });

  // =================================================================== 11. layout

  var eyeFrac = L.parseNum(store.get(KEYS.eyeFrac, 0.55), 0.55, 0.15, 0.85);
  var layoutRaf = 0;

  function eyeRegion() {
    var vh = window.innerHeight || 600;
    var used = 0;
    [dom.topbar, dom.chatBar, dom.inputbar, dom.eyehud].forEach(function (el) {
      if (el) used += el.offsetHeight;
    });
    return Math.max(80, vh - used - 24);
  }

  function renderEye() {
    var region = eyeRegion();
    var hidden = dom.body.classList.contains("chat-hidden");
    var h = hidden ? region : Math.max(70, Math.round(eyeFrac * region));
    document.documentElement.style.setProperty("--eye-h", Math.round(h) + "px");
  }

  function scheduleLayout() {
    if (layoutRaf) return;
    layoutRaf = requestAnimationFrame(function () {
      layoutRaf = 0;
      renderEye();
      updateDockOffsets();
    });
  }

  /*
   * Floating layers (toasts, onboarding card) sit above the composer, whose
   * height changes (multi-line input, suggestion chips); toasts also stack
   * above a visible onboarding card instead of covering its button.
   */
  function updateDockOffsets() {
    var root = document.documentElement;
    var vh = window.innerHeight || 0;
    var dock = 0;
    if (dom.inputbar && vh) dock = Math.max(0, Math.round(vh - dom.inputbar.getBoundingClientRect().top));
    root.style.setProperty("--dock-h", dock + "px");
  }

  function setChatVisible(visible) {
    dom.body.classList.toggle("chat-hidden", !visible);
    store.set(KEYS.chat, !!visible);
    if (dom.chatToggle) {
      dom.chatToggle.setAttribute("aria-expanded", visible ? "true" : "false");
      var label = dom.chatToggle.querySelector(".chat-toggle-label");
      if (label) label.textContent = visible ? "Hide chat" : "Show chat";
    }
    renderEye();
    if (visible) setTimeout(function () {
      scrollToBottom(false);
    }, 30);
  }

  // ------------------------------------------------------------ eye-only mode
  // Nothing but the eye, e.g. on a second screen. Wake word, push-to-talk
  // and speech keep running; Esc / E (or the ✕) bring the page back.
  var eyeMode = { on: false, fs: false, idleTimer: 0 };
  var eyeModeBtn = $("eyeModeBtn");
  var eyeModeExit = $("eyeModeExit");

  function eyeModeWake() {
    dom.body.classList.remove("eye-idle");
    clearTimeout(eyeMode.idleTimer);
    if (eyeMode.on) {
      eyeMode.idleTimer = setTimeout(function () {
        dom.body.classList.add("eye-idle"); // hides the cursor and the ✕
      }, 2500);
    }
  }

  function enterEyeMode(fullscreen) {
    if (eyeMode.on) return;
    eyeMode.on = true;
    if (settingsOpen) closeSettings();
    if (onboard && !onboard.hidden) dismissOnboarding();
    dom.body.classList.add("eye-only");
    if (eyeModeBtn) eyeModeBtn.setAttribute("aria-pressed", "true");
    if (eyeModeExit) eyeModeExit.hidden = false;
    // a focused button would swallow Space (push-to-talk) as a click
    if (document.activeElement && document.activeElement.blur) document.activeElement.blur();
    if (fullscreen) requestEyeFullscreen();
    eyeModeWake();
    toast("Eye mode — press Esc or E to return.", { key: "eyemode" });
  }

  function exitEyeMode() {
    if (!eyeMode.on) return;
    eyeMode.on = false;
    clearTimeout(eyeMode.idleTimer);
    dom.body.classList.remove("eye-only", "eye-idle");
    if (eyeModeBtn) eyeModeBtn.setAttribute("aria-pressed", "false");
    if (eyeModeExit) eyeModeExit.hidden = true;
    if (eyeMode.fs && document.fullscreenElement && document.exitFullscreen) {
      var p = document.exitFullscreen();
      if (p && p.catch) p.catch(function () {});
    }
    eyeMode.fs = false;
    scheduleLayout();
    if (eyeModeBtn && eyeModeBtn.focus) eyeModeBtn.focus({ preventScroll: true });
  }

  function requestEyeFullscreen() {
    var el = document.documentElement;
    if (!el.requestFullscreen || document.fullscreenElement) return;
    try {
      eyeMode.fs = true;
      var p = el.requestFullscreen({ navigationUI: "hide" });
      if (p && p.catch) p.catch(function () {
        eyeMode.fs = false; // denied / unsupported: stay in windowed eye mode
      });
    } catch (e) {
      eyeMode.fs = false;
    }
  }

  // The browser's own Esc leaves fullscreen before our keydown sees it, so
  // leaving a fullscreen we started also leaves eye mode.
  on(document, "fullscreenchange", function () {
    if (!document.fullscreenElement && eyeMode.fs) {
      eyeMode.fs = false;
      exitEyeMode();
    }
  });
  on(eyeModeBtn, "click", function () {
    if (eyeMode.on) exitEyeMode();
    else enterEyeMode(true);
  });
  on(eyeModeExit, "click", exitEyeMode);
  on(document, "pointermove", function () {
    if (eyeMode.on) eyeModeWake();
  }, { passive: true });
  on(dom.eyeZone, "dblclick", function () {
    if (!eyeMode.on) return;
    if (document.fullscreenElement) {
      if (document.exitFullscreen) document.exitFullscreen().catch(function () {});
      eyeMode.fs = false;
    } else requestEyeFullscreen();
  });

  on(dom.chatToggle, "click", function () {
    setChatVisible(dom.body.classList.contains("chat-hidden"));
  });

  on(dom.chatBar, "pointerdown", function (e) {
    if (e.button !== 0 || (e.target.closest && e.target.closest("button"))) return;
    e.preventDefault();
    if (dom.body.classList.contains("chat-hidden")) {
      eyeFrac = 0.85;
      setChatVisible(true);
    }
    var startY = e.clientY;
    var startFrac = eyeFrac;
    var region = eyeRegion();
    var pending = null;
    var raf = 0;
    dom.body.classList.add("resizing");
    try {
      dom.chatBar.setPointerCapture(e.pointerId);
    } catch (err) {
      /* ignore */
    }
    var onMove = function (ev) {
      pending = ev.clientY;
      if (raf) return;
      raf = requestAnimationFrame(function () {
        raf = 0;
        eyeFrac = L.clamp(startFrac + (pending - startY) / region, 0.15, 0.9);
        renderEye();
      });
    };
    var onUp = function () {
      dom.chatBar.removeEventListener("pointermove", onMove);
      dom.chatBar.removeEventListener("pointerup", onUp);
      dom.chatBar.removeEventListener("pointercancel", onUp);
      dom.body.classList.remove("resizing");
      store.set(KEYS.eyeFrac, eyeFrac);
    };
    dom.chatBar.addEventListener("pointermove", onMove);
    dom.chatBar.addEventListener("pointerup", onUp);
    dom.chatBar.addEventListener("pointercancel", onUp);
  });

  on(window, "resize", scheduleLayout);
  if (window.ResizeObserver) {
    try {
      var ro = new ResizeObserver(scheduleLayout);
      [dom.topbar, dom.inputbar, dom.eyehud, $("onboarding")].forEach(function (el) {
        if (el) ro.observe(el);
      });
    } catch (e) {
      /* ignore */
    }
  }

  // =================================================================== 12. keyboard, gestures, boot

  function unlockAudio() {
    gestureSeen = true;
    var hadCtx = !!audio.ctx;
    audio.resume();
    if (!hadCtx && audio.ctx) preloadAck();
  }

  var firstGesture = function (e) {
    unlockAudio();
    // the always-on mic auto-starts on the first interaction (permission
    // prompts need a gesture), unless that interaction was the mic button
    var ownKey = e.type === "keydown" && (e.key === "m" || e.key === "M" || e.key === " ");
    var micClick = e.target && e.target.closest && e.target.closest("#micBtn");
    if (mic.wanted && !mic.rec && !ownKey && !micClick) mic.ensure();
    document.removeEventListener("pointerdown", firstGesture, true);
    document.removeEventListener("keydown", firstGesture, true);
  };
  document.addEventListener("pointerdown", firstGesture, true);
  document.addEventListener("keydown", firstGesture, true);
  on(document, "pointerdown", unlockAudio, { capture: true, passive: true });

  // mic button: click toggles, long-press = push-to-talk
  (function () {
    var b = dom.micBtn;
    if (!b) return;
    var holdTimer = 0;
    var held = false;
    b.addEventListener("pointerdown", function (e) {
      if (e.button !== 0) return;
      held = false;
      clearTimeout(holdTimer);
      holdTimer = setTimeout(function () {
        held = true;
        startPTT();
      }, 380);
    });
    var release = function () {
      clearTimeout(holdTimer);
      if (held) endPTT();
    };
    b.addEventListener("pointerup", release);
    b.addEventListener("pointerleave", release);
    b.addEventListener("pointercancel", release);
    b.addEventListener("click", function (e) {
      if (held) {
        held = false;
        e.preventDefault();
        return;
      }
      toggleMic();
    });
    b.addEventListener("contextmenu", function (e) {
      e.preventDefault();
    });
  })();

  function toggleMic() {
    if (!mic.supported) {
      toast("Voice input needs Chrome or Edge. You can always type instead.", { type: "warn", key: "ptt" });
      return;
    }
    unlockAudio();
    if (mic.wanted && !mic.rec && !mic.running && !mic.restartTimer && !mic.blocked) {
      // "standby": wanted, but recognition never started (it needs a user
      // gesture). The button looks idle, so pressing it starts listening.
      mic.ensure();
      toast("Listening for “Hermes”.", { key: "mic", timeout: 1800 });
      return;
    }
    mic.setWanted(!mic.wanted);
    toast(mic.wanted ? "Listening for “Hermes”." : "Microphone off.", { key: "mic", timeout: 1800 });
  }

  function isTypingTarget(el) {
    if (!el || el === document.body) return false;
    if (el.isContentEditable) return true;
    var tag = el.tagName;
    return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT";
  }

  function isInteractive(el) {
    return !!(el && el.closest && el.closest("button, a, input, textarea, select, summary, [role='button'], dialog, .settings"));
  }

  function anyDialogOpen() {
    return (lightbox && lightbox.open) || (shortcuts && shortcuts.open);
  }

  on(document, "keydown", function (e) {
    var mod = e.ctrlKey || e.metaKey;
    var typing = isTypingTarget(e.target);

    if (e.key === "Escape") {
      if (anyDialogOpen()) return; // native <dialog> closes itself
      if (overlay && !overlay.hidden) {
        closePictures();
      } else if (settingsOpen) {
        closeSettings();
      } else if (eyeMode.on) {
        exitEyeMode();
      } else if (gen.active || speech.isBusy()) {
        stopAll(); // the documented "stop" shortcut wins over the tips card
      } else if (onboard && !onboard.hidden) {
        dismissOnboarding();
      } else if (mic.armed) {
        disarm();
      } else if (typing && e.target.blur) {
        e.target.blur();
      } else return;
      e.preventDefault();
      return;
    }

    // page shortcuts never act behind a modal (settings, pictures, dialogs)
    var modal = settingsOpen || anyDialogOpen() || (overlay && !overlay.hidden) || (onboard && !onboard.hidden);
    if (mod && !e.altKey && (e.key === "k" || e.key === "K")) {
      if (modal) return;
      e.preventDefault();
      newChat();
      return;
    }
    if (mod && e.key === ",") {
      e.preventDefault();
      if (settingsOpen) closeSettings();
      else openSettings();
      return;
    }
    if (mod || e.altKey || typing || modal) return;

    if ((e.key === "e" || e.key === "E") && !e.repeat) {
      e.preventDefault();
      if (eyeMode.on) exitEyeMode();
      else enterEyeMode(true);
    } else if (e.key === "/") {
      e.preventDefault();
      if (eyeMode.on) exitEyeMode();
      if (dom.input) dom.input.focus();
    } else if (e.key === "?") {
      e.preventDefault();
      toggleShortcuts();
    } else if ((e.key === "m" || e.key === "M") && !e.repeat) {
      e.preventDefault();
      toggleMic();
    } else if ((e.key === " " || e.code === "Space") && !isInteractive(e.target)) {
      e.preventDefault();
      if (!e.repeat) startPTT();
    }
  });

  on(document, "keyup", function (e) {
    if (e.key === " " || e.code === "Space") endPTT();
  });
  on(window, "blur", function () {
    endPTT();
  });

  // --- onboarding

  var onboard = $("onboarding");
  var onboardBackdrop = $("onboardBackdrop");
  var onboardReturnFocus = null;
  function dismissOnboarding() {
    store.set(KEYS.onboard, true);
    conceal(onboardBackdrop);
    conceal(onboard, function () {
      // Hand focus back to where the user was (usually the composer).
      var back = onboardReturnFocus && document.contains(onboardReturnFocus) ? onboardReturnFocus : dom.input;
      onboardReturnFocus = null;
      if (back && back.focus) {
        try {
          back.focus({ preventScroll: true });
        } catch (e) {
          /* ignore */
        }
      }
    });
  }
  on($("onboardClose"), "click", dismissOnboarding);
  on($("onboardOk"), "click", dismissOnboarding);
  on(onboardBackdrop, "click", dismissOnboarding);
  // Keep Tab inside the dialog while it is open.
  on(onboard, "keydown", function (e) {
    if (e.key !== "Tab") return;
    var items = onboard.querySelectorAll("button, [href], input, [tabindex]:not([tabindex='-1'])");
    if (!items.length) return;
    var first = items[0];
    var last = items[items.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  });

  function maybeOnboard() {
    if (!onboard || store.get(KEYS.onboard, false) || eyeMode.on) return;
    var note = $("onboardNote");
    if (note && !mic.supported) {
      note.textContent = "Voice input isn't available in this browser — use Chrome or Edge for the wake word. Typing works everywhere.";
      note.classList.add("warn");
    }
    setTimeout(function () {
      if (store.get(KEYS.onboard, false) || eyeMode.on) return; // dismissed meanwhile
      onboardReturnFocus = document.activeElement;
      reveal(onboardBackdrop);
      reveal(onboard);
      var ok = $("onboardOk");
      if (ok && ok.focus) {
        try {
          ok.focus({ preventScroll: true });
        } catch (e) {
          /* ignore */
        }
      }
    }, 700);
  }

  // --- boot

  function boot() {
    if (synth) {
      loadBrowserVoices();
      try {
        synth.addEventListener ? synth.addEventListener("voiceschanged", loadBrowserVoices) : (synth.onvoiceschanged = loadBrowserVoices);
      } catch (e) {
        /* ignore */
      }
    }
    var touch = !!(window.matchMedia && (window.matchMedia("(max-width: 640px)").matches || window.matchMedia("(hover: none)").matches));
    if (dom.input && touch) {
      dom.input.placeholder = mic.supported ? "Say “Hermes” or type…" : "Message HERMES…";
    } else if (dom.input && !mic.supported) {
      dom.input.placeholder = "Message HERMES…";
    }
    // phones have no Space key: point at the mic button instead
    var emptySub = document.querySelector(".empty-sub");
    if (emptySub) {
      if (!mic.supported) emptySub.textContent = "Type below to talk to HERMES.";
      else if (touch) emptySub.innerHTML = "Say <kbd>Hermes</kbd>, long-press the mic button to talk, or type below.";
    }
    var obPtt = $("obPtt");
    if (obPtt && touch) {
      obPtt.innerHTML = '<span class="ob-key">Mic</span><span>Long-press the mic button to talk directly — no wake word needed.</span>';
    }
    setChatVisible(store.get(KEYS.chat, false) === true);
    renderAll();
    autoGrow();
    updateDockOffsets();
    applyEyeDesign(settings.eyeDesign);
    mic.wanted = mic.supported && store.get(KEYS.mic, true) !== false;
    updateMicButton();
    updateEye();
    // ?view=eye (or #eye) opens straight into eye mode, e.g. for a kiosk
    // screen. Fullscreen needs a click, so this starts windowed.
    if (/(?:^|[?&])view=eye(?:&|$)/.test(location.search.slice(1)) || location.hash === "#eye") enterEyeMode(false);
    maybeOnboard();
    fetchSettings().then(function () {
      refreshStatus();
      updateEye();
    });
    on(document, "visibilitychange", function () {
      if (document.visibilityState === "visible") {
        if (now() - lastStatusAt > 5000) refreshStatus();
        if (mic.shouldRun() && !mic.rec && gestureSeen) mic.ensure();
      }
    });
  }

  boot();
})();
