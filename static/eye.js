/*
 * HERMES eye animation engine.
 *
 * Drives the SVG optic (#eye) with a requestAnimationFrame loop of
 * critically-damped springs, so state changes glide instead of snapping
 * between CSS keyframe poses.
 *
 * Public API (window.HermesEye, also module.exports for node tests):
 *   init(svgEl, opts?)    attach to the <svg>; opts.trackPointer (default true)
 *   setState(state)       idle | listening | thinking | speaking | confused | error
 *   setLevel(x)           audio amplitude 0..1 from a real meter while speaking.
 *                         A meter that stops sending levels means silence
 *                         (the eye goes quiet, it does not fake talking).
 *   setLevel(null)        no meter for this playback (browser speech, plain
 *                         <audio>): animate a synthetic talking pulse until
 *                         the next numeric setLevel() or the end of speaking.
 *   lookAt(clientX, clientY) / lookAt(null)
 *   blink()
 *   setDesign(name)       classic | cyclops | iris | cortana
 * Every call is a safe no-op before init(). The engine also watches the
 * svg's data-state / data-design attributes, so code that only sets those
 * attributes still animates correctly.
 */
(function (root, factory) {
  var api = factory(root);
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root && root.document) root.HermesEye = api;
})(typeof window !== "undefined" ? window : this, function (root) {
  "use strict";

  var CX = 210;
  var CY = 128;
  var MAX_DT = 0.1; // seconds; clamps hitches (tab switches, GC pauses)

  // Per-design geometry: how far the pupil may travel and the base spin
  // (deg/s) of the .shutters ring (.counter rings turn the other way at
  // COUNTER_RATIO of that speed).
  var DESIGNS = {
    classic: { travelX: 10, travelY: 8, ring: 2.5, scanTravel: 0 },
    cyclops: { travelX: 10, travelY: 10, ring: 6, scanTravel: 40 },
    iris: { travelX: 7, travelY: 7, ring: 14, scanTravel: 0 },
    cortana: { travelX: 12, travelY: 6, ring: 9, scanTravel: 34 },
  };

  // Behaviour targets per state. scale = pupil dilation, glow = light
  // intensity, ring = multiplier on the design's ring spin, energy = overall
  // brightness (bloom + ambient), gaze = saccade mode, track = how much the
  // pupil follows lookAt(), breathe = glow breathing depth, flicker = glow
  // noise depth, scan = scan-line sweep amount, posK = gaze spring stiffness.
  var STATES = {
    idle: { scale: 1.0, glow: 0.85, ring: 1, energy: 0.75, gaze: "idle", track: 0.45, breathe: 0.06, flicker: 0, scan: 0, posK: 260, blinks: true },
    listening: { scale: 1.08, glow: 1.0, ring: 1.6, energy: 1.0, gaze: "center", track: 1, breathe: 0.14, flicker: 0, scan: 0.25, posK: 120, blinks: true },
    thinking: { scale: 0.95, glow: 0.9, ring: 4, energy: 0.9, gaze: "scan", track: 0, breathe: 0.03, flicker: 0.08, scan: 1, posK: 320, blinks: false },
    speaking: { scale: 1.02, glow: 0.78, ring: 2, energy: 1.0, gaze: "center", track: 0.25, breathe: 0, flicker: 0, scan: 0.6, posK: 90, blinks: false, audio: true },
    confused: { scale: 0.93, glow: 0.6, ring: -1.5, energy: 0.8, gaze: "wander", track: 0, breathe: 0.04, flicker: 0.05, scan: 0.3, posK: 140, blinks: false, shake: true },
    error: { scale: 0.88, glow: 0.3, ring: 0.15, energy: 0.3, gaze: "droop", track: 0, breathe: 0.02, flicker: 0.04, scan: 0, posK: 40, blinks: false },
  };

  var BLINK_MS = 150;
  var COUNTER_RATIO = -0.6;

  // ---------------------------------------------------------------- math

  var scratch = { pos: 0, vel: 0 };

  /*
   * Exact (closed-form) damped spring step. Unconditionally stable for any
   * dt; with damping = 2*sqrt(stiffness) it is critically damped and never
   * overshoots a target it starts at rest from. Writes into `out` (reused
   * object, no per-frame allocation) and returns it.
   */
  function springStep(pos, vel, target, stiffness, damping, dt, out) {
    out = out || { pos: 0, vel: 0 };
    if (!(dt > 0) || !(stiffness > 0)) {
      out.pos = pos;
      out.vel = vel;
      return out;
    }
    var x0 = pos - target;
    var w = Math.sqrt(stiffness);
    var zeta = damping / (2 * w);
    var x, v, e;
    if (Math.abs(zeta - 1) < 1e-4) {
      e = Math.exp(-w * dt);
      var b = vel + w * x0;
      x = (x0 + b * dt) * e;
      v = (vel - w * b * dt) * e;
    } else if (zeta < 1) {
      var wd = w * Math.sqrt(1 - zeta * zeta);
      e = Math.exp(-zeta * w * dt);
      var c = Math.cos(wd * dt);
      var s = Math.sin(wd * dt);
      var a = zeta * w;
      var B = (vel + a * x0) / wd;
      x = e * (x0 * c + B * s);
      v = e * (vel * c - (a * B + x0 * wd) * s);
    } else {
      var sq = Math.sqrt(zeta * zeta - 1);
      var r1 = -w * (zeta - sq);
      var r2 = -w * (zeta + sq);
      var c1 = (vel - r2 * x0) / (r1 - r2);
      var c2 = x0 - c1;
      var e1 = Math.exp(r1 * dt);
      var e2 = Math.exp(r2 * dt);
      x = c1 * e1 + c2 * e2;
      v = c1 * r1 * e1 + c2 * r2 * e2;
    }
    out.pos = target + x;
    out.vel = v;
    return out;
  }

  function critical(k) {
    return 2 * Math.sqrt(k);
  }

  /*
   * Envelope follower for audio amplitude: rises with the `attack` time
   * constant, falls with `release` (seconds). Frame-rate independent.
   */
  function smoothLevel(current, input, dt, attack, release) {
    if (!(dt > 0)) return current;
    var tau = input > current ? attack : release;
    if (!(tau > 0)) return input;
    return current + (input - current) * (1 - Math.exp(-dt / tau));
  }

  function clamp(x, lo, hi) {
    return x < lo ? lo : x > hi ? hi : x;
  }

  // Blink lid curve: 1 = open, ~0.08 = closed. p in [0,1]; smooth in/out.
  function blinkCurve(p) {
    if (!(p > 0) || p >= 1) return 1;
    var s = Math.sin(Math.PI * p);
    return 1 - 0.92 * s * s;
  }

  // ---------------------------------------------------------------- engine

  /*
   * Pure-ish animation model, independent of the DOM so it can be tested.
   * rand: () => [0,1)
   */
  function createEngine(rand) {
    rand = rand || Math.random;
    var m = {
      state: "idle",
      design: "classic",
      reduced: false,
      t: 0,
      // springs
      px: 0, pvx: 0, py: 0, pvy: 0,
      sc: 1, svc: 0,
      gl: 0.85, gvl: 0,
      rs: 0, rsv: 0, // ring speed deg/s
      en: 0.75, env: 0,
      sa: 0, sav: 0, // scan amount
      // integrators
      ring: 0,
      scanPhase: 0,
      // gaze
      gx: 0, gy: 0, nextGaze: 0.5, gazeDir: 1,
      lookX: 0, lookY: 0, looking: false,
      // audio
      // levelSrc: "none" (no audio yet -> quiet), "meter" (numeric
      // setLevel; stale = silence), "synthetic" (setLevel(null) opt-in)
      level: 0, levelIn: 0, lastLevelT: -1, levelSrc: "none",
      // blink
      blinkStart: -1, nextBlink: 2 + rand() * 3,
      // shake
      shake: 0,
      // outputs
      out: { x: 0, y: 0, scale: 1, lid: 1, glow: 0.85, ring: 0, energy: 0.75, scanY: 0, scanOpacity: 0.4, coreX: 0, coreY: 0, coreScale: 1 },
    };

    function setState(s) {
      if (!STATES[s]) s = "idle";
      if (s === m.state) return;
      m.state = s;
      m.nextGaze = m.t + 0.15;
      if (STATES[s].shake && !m.reduced) m.shake = 1;
      if (s !== "speaking") {
        m.lastLevelT = -1;
        m.levelSrc = "none";
      }
    }

    function setDesign(d) {
      if (DESIGNS[d]) m.design = d;
    }

    function setLevel(x) {
      if (x === null || x === undefined) {
        m.levelSrc = "synthetic";
        return;
      }
      m.levelSrc = "meter";
      x = +x;
      if (!(x >= 0)) x = 0;
      m.levelIn = x > 1 ? 1 : x;
      m.lastLevelT = m.t;
    }

    function lookAt(nx, ny) {
      if (nx === null || nx === undefined) {
        m.looking = false;
        return;
      }
      m.looking = true;
      m.lookX = clamp(+nx || 0, -1, 1);
      m.lookY = clamp(+ny || 0, -1, 1);
    }

    function blink() {
      if (m.reduced) return;
      if (m.blinkStart < 0) m.blinkStart = m.t;
    }

    function pickGaze(cfg) {
      var t = m.t;
      if (m.reduced) {
        m.gx = 0;
        m.gy = cfg.gaze === "droop" ? 0.3 : 0;
        return;
      }
      switch (cfg.gaze) {
        case "idle":
          if (t >= m.nextGaze) {
            // mostly small glances, sometimes a bigger look, often recenter
            var r = rand();
            var amp = r < 0.25 ? 0.15 : r < 0.8 ? 0.45 : 0.8;
            var a = rand() * Math.PI * 2;
            m.gx = Math.cos(a) * amp;
            m.gy = Math.sin(a) * amp * 0.7;
            m.nextGaze = t + 2 + rand() * 3;
          }
          break;
        case "scan":
          if (t >= m.nextGaze) {
            m.gazeDir = -m.gazeDir;
            m.gx = m.gazeDir * (0.55 + rand() * 0.35);
            m.gy = (rand() - 0.5) * 0.35;
            m.nextGaze = t + 0.55 + rand() * 0.6;
          }
          break;
        case "wander":
          if (t >= m.nextGaze) {
            m.gx = (rand() - 0.5) * 0.9;
            m.gy = (rand() - 0.5) * 0.6;
            m.nextGaze = t + 0.7 + rand() * 0.8;
          }
          break;
        case "droop":
          m.gx = 0;
          m.gy = 0.35;
          break;
        default:
          m.gx = 0;
          m.gy = 0;
      }
    }

    function update(dt) {
      if (!(dt > 0)) dt = 0;
      if (dt > MAX_DT) dt = MAX_DT;
      m.t += dt;
      var t = m.t;
      var cfg = STATES[m.state];
      var geo = DESIGNS[m.design];
      var reduced = m.reduced;
      var o = m.out;

      pickGaze(cfg);

      // --- gaze target
      var tx = m.gx, ty = m.gy;
      if (!reduced) {
        // slow organic drift on top of saccades
        tx += Math.sin(t * 0.37) * 0.06 + Math.sin(t * 0.91 + 1.3) * 0.03;
        ty += Math.sin(t * 0.29 + 2.1) * 0.05;
        if (m.looking && cfg.track > 0) {
          tx = tx * (1 - cfg.track) + m.lookX * cfg.track;
          ty = ty * (1 - cfg.track) + m.lookY * cfg.track;
        }
      }
      var k = cfg.posK;
      springStep(m.px, m.pvx, tx * geo.travelX, k, critical(k), dt, scratch);
      m.px = scratch.pos; m.pvx = scratch.vel;
      springStep(m.py, m.pvy, ty * geo.travelY, k, critical(k), dt, scratch);
      m.py = scratch.pos; m.pvy = scratch.vel;

      // --- audio envelope
      var audio = 0;
      if (cfg.audio) {
        // Only fake talking when the app said there is no meter. A meter
        // that went silent (fetching the next sentence, gaps) reads as 0.
        var input;
        if (m.levelSrc === "synthetic") {
          input = reduced ? 0.4 : 0.35 + 0.25 * Math.sin(t * Math.PI * 2 * 1.4) + 0.1 * Math.sin(t * 7.3);
        } else if (m.levelSrc === "meter" && m.lastLevelT >= 0 && t - m.lastLevelT < 0.3) {
          input = m.levelIn;
        } else {
          input = 0;
        }
        m.level = smoothLevel(m.level, input, dt, 0.035, 0.16);
        audio = m.level;
      } else {
        m.level = smoothLevel(m.level, 0, dt, 0.035, 0.16);
      }

      // --- scale / glow / ring / energy / scan targets
      var scaleT = cfg.scale + (cfg.audio ? audio * 0.09 : 0);
      if (!reduced && cfg.breathe) scaleT += Math.sin(t * Math.PI * 2 / 4.2) * cfg.breathe * 0.12;
      springStep(m.sc, m.svc, scaleT, 140, critical(140), dt, scratch);
      m.sc = scratch.pos; m.svc = scratch.vel;

      var glowT = cfg.glow;
      if (cfg.audio) glowT = 0.55 + 0.45 * audio;
      if (!reduced && cfg.breathe) glowT += Math.sin(t * Math.PI * 2 / 2.8) * cfg.breathe;
      if (cfg.flicker && !reduced) glowT += (Math.sin(t * 23.1) * Math.sin(t * 7.7 + 0.5)) * cfg.flicker;
      var gk = cfg.audio ? 220 : 30;
      springStep(m.gl, m.gvl, clamp(glowT, 0, 1.2), gk, critical(gk), dt, scratch);
      m.gl = scratch.pos; m.gvl = scratch.vel;

      springStep(m.rs, m.rsv, reduced ? 0 : geo.ring * cfg.ring, 3, critical(3), dt, scratch);
      m.rs = scratch.pos; m.rsv = scratch.vel;
      m.ring = (m.ring + m.rs * dt) % 360;

      springStep(m.en, m.env, cfg.energy + (cfg.audio ? audio * 0.2 : 0), 12, critical(12), dt, scratch);
      m.en = scratch.pos; m.env = scratch.vel;

      springStep(m.sa, m.sav, reduced ? 0 : cfg.scan, 6, critical(6), dt, scratch);
      m.sa = scratch.pos; m.sav = scratch.vel;
      m.scanPhase += dt * (1.2 + 2.2 * m.sa);

      // --- blink
      var lid = 1;
      if (!reduced && cfg.blinks && t >= m.nextBlink && m.blinkStart < 0) {
        m.blinkStart = t;
      }
      if (m.blinkStart >= 0) {
        var p = (t - m.blinkStart) / (BLINK_MS / 1000);
        if (p >= 1) {
          m.blinkStart = -1;
          // occasional double blink
          m.nextBlink = t + (rand() < 0.15 ? 0.22 : 2.5 + rand() * 4.5);
        } else {
          lid = blinkCurve(p);
        }
      }

      // --- shake (confused), decays
      var sx = 0, sy = 0;
      if (m.shake > 0.001 && !reduced) {
        m.shake *= Math.exp(-dt * 2.6);
        sx = (Math.sin(t * 41) + 0.5 * Math.sin(t * 67 + 1)) * 2.6 * m.shake;
        sy = (Math.sin(t * 37 + 2) + 0.5 * Math.sin(t * 59)) * 1.6 * m.shake;
      } else {
        m.shake = 0;
      }

      o.x = m.px + sx * 1.5;
      o.y = m.py + sy * 1.5;
      o.scale = m.sc;
      o.lid = lid;
      o.glow = clamp(m.gl * (0.55 + 0.45 * lid), 0, 1);
      o.ring = m.ring;
      o.energy = clamp(m.en, 0, 1.3);
      o.scanY = Math.sin(m.scanPhase * Math.PI) * geo.scanTravel * m.sa;
      o.scanOpacity = clamp(0.3 + (reduced ? 0 : 0.25 * Math.sin(t * 1.8) * (1 - m.sa)) + 0.5 * m.sa, 0, 1);
      o.coreX = sx;
      o.coreY = sy;
      o.coreScale = reduced ? 1 : 1 + Math.sin(t * Math.PI * 2 / 5.5) * 0.006;

      // settle tracking: how many consecutive frames produced no visible
      // change. Lets the DOM loop park itself under reduced motion.
      var still = true;
      for (var key in prev) {
        if (!(Math.abs(o[key] - prev[key]) < 1e-3)) still = false; // NaN-safe
        prev[key] = o[key];
      }
      m.stillFrames = still ? m.stillFrames + 1 : 0;
      return o;
    }

    var prev = {};
    for (var pk in m.out) prev[pk] = NaN;
    m.stillFrames = 0;

    /*
     * True when nothing on screen will change by itself: only possible under
     * reduced motion (otherwise drift / breathing never stop) once every
     * spring has come to rest and no blink is running.
     */
    function settled() {
      return m.reduced && m.blinkStart < 0 && m.stillFrames >= 8;
    }

    m.setState = setState;
    m.setDesign = setDesign;
    m.setLevel = setLevel;
    m.lookAt = lookAt;
    m.blink = blink;
    m.update = update;
    m.settled = settled;
    return m;
  }

  // ---------------------------------------------------------------- DOM

  var svg = null;
  var zone = null;
  var ambient = null;
  var engine = null;
  var raf = 0;
  var lastNow = 0;
  var observer = null;
  var els = { pupils: [], glows: [], glowBase: [], shutters: [], counters: [], scans: [], scanBase: [], blooms: [], bloomBase: [], core: null };
  var cache = Object.create(null); // last written attribute strings
  var pointer = { dirty: false, x: 0, y: 0, active: false };
  var syncing = false;
  var collectedDesign = null;

  function r2(x) {
    return Math.round(x * 100) / 100;
  }

  function hasDom() {
    return !!(root && root.document);
  }

  /*
   * The authored opacity of an element, read once and remembered in
   * data-base-opacity. render() overwrites the live `opacity` attribute with
   * base * factor, so reading it again (every collect()) would compound the
   * scaling and the eye would dim a little more on each setDesign().
   */
  function baseOpacity(el) {
    var stored = el.getAttribute("data-base-opacity");
    if (stored === null) {
      stored = el.getAttribute("opacity");
      if (stored === null || stored === "") stored = "1";
      el.setAttribute("data-base-opacity", stored);
    }
    var v = parseFloat(stored);
    return isNaN(v) ? 1 : v;
  }

  function collect() {
    if (!svg) return;
    var design = engine.design;
    var g = svg.querySelector('.design[data-el="' + design + '"]');
    var scope = g || svg;
    els.pupils = Array.prototype.slice.call(scope.querySelectorAll(".pupil"));
    els.glows = Array.prototype.slice.call(scope.querySelectorAll(".glow"));
    els.glowBase = els.glows.map(baseOpacity);
    els.shutters = Array.prototype.slice.call(scope.querySelectorAll(".shutters"));
    els.counters = Array.prototype.slice.call(scope.querySelectorAll(".counter"));
    els.scans = Array.prototype.slice.call(scope.querySelectorAll(".scan"));
    els.scanBase = els.scans.map(baseOpacity);
    els.blooms = Array.prototype.slice.call(scope.querySelectorAll(".bloom"));
    els.bloomBase = els.blooms.map(baseOpacity);
    els.core = svg.querySelector(".eye-core");
    cache = Object.create(null);
    collectedDesign = design;
  }

  function setAttr(el, idx, name, value) {
    var key = name + idx;
    if (cache[key] === value) return;
    cache[key] = value;
    el.setAttribute(name, value);
  }

  function render(o) {
    var i;
    var pt = "translate(" + r2(CX + o.x) + " " + r2(CY + o.y) + ") scale(" + r2(o.scale * 1000) / 1000 + " " + r2(o.scale * o.lid * 1000) / 1000 + ") translate(-210 -128)";
    for (i = 0; i < els.pupils.length; i++) setAttr(els.pupils[i], "p" + i, "transform", pt);
    for (i = 0; i < els.glows.length; i++) setAttr(els.glows[i], "g" + i, "opacity", String(r2(clamp(els.glowBase[i] * o.glow, 0, 1))));
    var rt = "rotate(" + r2(o.ring) + " 210 128)";
    for (i = 0; i < els.shutters.length; i++) setAttr(els.shutters[i], "s" + i, "transform", rt);
    var ct = "rotate(" + r2(o.ring * COUNTER_RATIO) + " 210 128)";
    for (i = 0; i < els.counters.length; i++) setAttr(els.counters[i], "r" + i, "transform", ct);
    var st = "translate(0 " + r2(o.scanY) + ")";
    for (i = 0; i < els.scans.length; i++) {
      setAttr(els.scans[i], "c" + i, "transform", st);
      setAttr(els.scans[i], "co" + i, "opacity", String(r2(els.scanBase[i] * o.scanOpacity)));
    }
    var bl = clamp(o.energy * (0.4 + 0.6 * o.glow), 0, 1);
    for (i = 0; i < els.blooms.length; i++) setAttr(els.blooms[i], "b" + i, "opacity", String(r2(clamp(els.bloomBase[i] * bl, 0, 1))));
    if (els.core) {
      setAttr(els.core, "k", "transform", "translate(" + r2(CX + o.coreX) + " " + r2(CY + o.coreY) + ") scale(" + r2(o.coreScale * 1000) / 1000 + ") translate(-210 -128)");
    }
    if (ambient) {
      var a = String(r2(clamp(0.25 + 0.6 * bl, 0, 1)));
      if (cache.amb !== a) {
        cache.amb = a;
        ambient.style.opacity = a;
      }
    }
  }

  function readPointer() {
    if (!pointer.dirty || !svg) return;
    pointer.dirty = false;
    if (!pointer.active) {
      engine.lookAt(null);
      return;
    }
    var rect = svg.getBoundingClientRect();
    if (!rect.width) return;
    var dx = (pointer.x - (rect.left + rect.width / 2)) / (rect.width * 0.55);
    var dy = (pointer.y - (rect.top + rect.height * (CY / 260))) / (rect.width * 0.55);
    var len = Math.sqrt(dx * dx + dy * dy);
    if (len > 1e-6) {
      var f = Math.tanh(len) / len;
      dx *= f;
      dy *= f;
    }
    engine.lookAt(dx, dy);
  }

  function frame(now) {
    raf = 0;
    if (!svg) return;
    var dt = lastNow ? (now - lastNow) / 1000 : 1 / 60;
    lastNow = now;
    readPointer();
    render(engine.update(dt));
    // Reduced motion: once everything has come to rest, stop the loop.
    // wake() restarts it on setState / setDesign / setLevel / mq change.
    if (engine.settled()) {
      lastNow = 0;
      return;
    }
    schedule();
  }

  function schedule() {
    if (raf || !svg || !root.requestAnimationFrame) return;
    if (root.document.hidden) return;
    raf = root.requestAnimationFrame(frame);
  }

  function wake() {
    if (engine) engine.stillFrames = 0;
    schedule();
  }

  function onVisibility() {
    if (root.document.hidden) {
      if (raf && root.cancelAnimationFrame) root.cancelAnimationFrame(raf);
      raf = 0;
    } else {
      lastNow = 0;
      schedule();
    }
  }

  function syncAttr(el, name, value) {
    if (el && el.getAttribute(name) !== value) el.setAttribute(name, value);
  }

  function onMutation() {
    if (syncing || !svg) return;
    var s = svg.getAttribute("data-state");
    var d = svg.getAttribute("data-design");
    if (s && s !== engine.state) api.setState(s);
    if (d && d !== engine.design) api.setDesign(d);
  }

  var api = {
    springStep: springStep,
    smoothLevel: smoothLevel,
    blinkCurve: blinkCurve,
    createEngine: createEngine,
    STATES: STATES,
    DESIGNS: DESIGNS,

    init: function (svgEl, opts) {
      if (!svgEl || !hasDom() || svg === svgEl) return api;
      opts = opts || {};
      svg = svgEl;
      engine = createEngine();
      zone = svg.closest ? svg.closest(".eyezone") : null;
      if (zone && !zone.querySelector(".eye-ambient")) {
        ambient = root.document.createElement("div");
        ambient.className = "eye-ambient";
        ambient.setAttribute("aria-hidden", "true");
        zone.insertBefore(ambient, zone.firstChild);
      } else if (zone) {
        ambient = zone.querySelector(".eye-ambient");
      }

      var mq = root.matchMedia ? root.matchMedia("(prefers-reduced-motion: reduce)") : null;
      if (mq) {
        engine.reduced = !!mq.matches;
        var onMq = function () {
          engine.reduced = !!mq.matches;
          wake();
        };
        if (mq.addEventListener) mq.addEventListener("change", onMq);
        else if (mq.addListener) mq.addListener(onMq);
      }

      svg.classList.add("eye-live"); // tells eye.css that JS drives motion
      engine.setDesign(svg.getAttribute("data-design") || "classic");
      engine.setState(svg.getAttribute("data-state") || "idle");
      syncAttr(svg, "data-design", engine.design);
      syncAttr(zone, "data-design", engine.design);
      collect();

      if (root.MutationObserver) {
        observer = new root.MutationObserver(onMutation);
        observer.observe(svg, { attributes: true, attributeFilter: ["data-state", "data-design"] });
      }
      root.document.addEventListener("visibilitychange", onVisibility);

      if (opts.trackPointer !== false) {
        root.addEventListener("pointermove", function (e) {
          pointer.x = e.clientX;
          pointer.y = e.clientY;
          pointer.active = true;
          pointer.dirty = true;
        }, { passive: true });
        root.document.addEventListener("pointerleave", function () {
          pointer.active = false;
          pointer.dirty = true;
        });
        root.addEventListener("blur", function () {
          pointer.active = false;
          pointer.dirty = true;
        });
      }
      schedule();
      return api;
    },

    setState: function (state) {
      if (!engine) return;
      if (!STATES[state]) state = "idle";
      engine.setState(state);
      syncing = true;
      syncAttr(svg, "data-state", state);
      syncAttr(zone, "data-state", state);
      syncing = false;
      wake();
    },

    setLevel: function (x) {
      if (!engine) return;
      engine.setLevel(x);
      wake();
    },

    lookAt: function (clientX, clientY) {
      if (!engine) return;
      if (clientX === null || clientX === undefined) {
        pointer.active = false;
        pointer.dirty = true;
        return;
      }
      pointer.x = +clientX || 0;
      pointer.y = +clientY || 0;
      pointer.active = true;
      pointer.dirty = true;
    },

    blink: function () {
      if (!engine) return;
      engine.blink();
      wake();
    },

    setDesign: function (name) {
      if (!engine || !DESIGNS[name]) return;
      if (name === engine.design && collectedDesign === name) return;
      engine.setDesign(name);
      syncing = true;
      syncAttr(svg, "data-design", name);
      syncAttr(zone, "data-design", name);
      syncing = false;
      collect();
      wake();
    },

    getState: function () {
      return engine ? engine.state : null;
    },
  };

  // Auto-init on the page's #eye (the script tag sits after the svg).
  if (hasDom()) {
    var auto = function () {
      var el = root.document.getElementById("eye");
      if (el) api.init(el);
    };
    if (root.document.getElementById("eye")) auto();
    else root.document.addEventListener("DOMContentLoaded", auto);
  }

  return api;
});
