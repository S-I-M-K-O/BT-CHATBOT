"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const path = require("node:path");

const Eye = require(path.join(__dirname, "..", "static", "eye.js"));

const crit = (k) => 2 * Math.sqrt(k);

// deterministic PRNG for engine tests
function lcg(seed) {
  let s = seed >>> 0;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 4294967296;
  };
}

test("springStep: critically damped spring converges without overshoot", () => {
  const k = 200;
  let pos = 0;
  let vel = 0;
  const out = { pos: 0, vel: 0 };
  let prev = pos;
  for (let i = 0; i < 240; i++) {
    Eye.springStep(pos, vel, 10, k, crit(k), 1 / 60, out);
    pos = out.pos;
    vel = out.vel;
    assert.ok(pos <= 10 + 1e-9, `overshoot at step ${i}: ${pos}`);
    assert.ok(pos >= prev - 1e-9, `not monotonic at step ${i}`);
    prev = pos;
  }
  assert.ok(Math.abs(pos - 10) < 1e-3, `did not converge: ${pos}`);
  assert.ok(Math.abs(vel) < 1e-2);
});

test("springStep: result is independent of frame rate (closed form)", () => {
  const k = 120;
  const a = { pos: 0, vel: 0 };
  let p = 0, v = 0;
  for (let i = 0; i < 60; i++) {
    Eye.springStep(p, v, 1, k, crit(k), 1 / 60, a);
    p = a.pos; v = a.vel;
  }
  const b = Eye.springStep(0, 0, 1, k, crit(k), 1);
  assert.ok(Math.abs(p - b.pos) < 1e-9);
  assert.ok(Math.abs(v - b.vel) < 1e-9);
});

test("springStep: stable for huge dt and for under/over-damped springs", () => {
  for (const [k, c] of [[300, crit(300)], [300, 5], [300, 200]]) {
    let p = -50, v = 400;
    for (let i = 0; i < 20; i++) {
      const r = Eye.springStep(p, v, 3, k, c, 5);
      p = r.pos; v = r.vel;
      assert.ok(Number.isFinite(p) && Number.isFinite(v));
    }
    assert.ok(Math.abs(p - 3) < 1e-6, `k=${k} c=${c} ended at ${p}`);
  }
});

test("springStep: underdamped spring overshoots (sanity of formula)", () => {
  let p = 0, v = 0, max = 0;
  for (let i = 0; i < 200; i++) {
    const r = Eye.springStep(p, v, 1, 400, 4, 1 / 120);
    p = r.pos; v = r.vel;
    max = Math.max(max, p);
  }
  assert.ok(max > 1.2);
});

test("springStep: zero / invalid dt leaves state untouched", () => {
  const r = Eye.springStep(2, 1, 5, 100, 20, 0);
  assert.equal(r.pos, 2);
  assert.equal(r.vel, 1);
  const n = Eye.springStep(2, 1, 5, 100, 20, NaN);
  assert.equal(n.pos, 2);
});

test("smoothLevel: fast attack, slower release, frame-rate independent", () => {
  let up = 0;
  for (let i = 0; i < 6; i++) up = Eye.smoothLevel(up, 1, 1 / 60, 0.035, 0.16);
  assert.ok(up > 0.9, `attack too slow: ${up}`);
  let down = 1;
  for (let i = 0; i < 6; i++) down = Eye.smoothLevel(down, 0, 1 / 60, 0.035, 0.16);
  assert.ok(down > 0.4 && down < 0.6, `release ${down}`);
  // one 100ms step == six ~16.7ms steps
  const one = Eye.smoothLevel(0, 1, 0.1, 0.035, 0.16);
  assert.ok(Math.abs(one - up) < 1e-9);
  assert.equal(Eye.smoothLevel(0.5, 1, 0, 0.03, 0.1), 0.5);
  assert.equal(Eye.smoothLevel(0.5, 1, 0.01, 0, 0.1), 1);
});

test("blinkCurve: open at ends, nearly closed in the middle", () => {
  assert.equal(Eye.blinkCurve(0), 1);
  assert.equal(Eye.blinkCurve(1), 1);
  assert.ok(Eye.blinkCurve(0.5) < 0.1);
  assert.ok(Eye.blinkCurve(0.25) > Eye.blinkCurve(0.5));
});

test("state table: every state has sane targets", () => {
  const names = ["idle", "listening", "thinking", "speaking", "confused", "error"];
  assert.deepEqual(Object.keys(Eye.STATES).sort(), names.slice().sort());
  for (const n of names) {
    const s = Eye.STATES[n];
    for (const key of ["scale", "glow", "ring", "energy", "track", "breathe", "flicker", "scan", "posK"]) {
      assert.equal(typeof s[key], "number", `${n}.${key}`);
      assert.ok(Number.isFinite(s[key]));
    }
    assert.ok(s.scale > 0.7 && s.scale < 1.3, `${n}.scale`);
    assert.ok(s.glow >= 0 && s.glow <= 1, `${n}.glow`);
    assert.ok(s.posK > 0);
  }
  assert.ok(Eye.STATES.listening.scale > Eye.STATES.idle.scale, "listening dilates");
  assert.ok(Eye.STATES.thinking.ring > Eye.STATES.idle.ring, "thinking spins faster");
  assert.ok(Eye.STATES.error.glow < Eye.STATES.idle.glow, "error is dim");
  assert.ok(Eye.STATES.speaking.audio, "speaking follows audio");
  for (const d of ["classic", "cyclops", "iris", "cortana"]) {
    assert.ok(Eye.DESIGNS[d], `design ${d}`);
  }
});

test("API calls before init are safe no-ops", () => {
  assert.doesNotThrow(() => {
    Eye.setState("thinking");
    Eye.setLevel(0.5);
    Eye.lookAt(10, 20);
    Eye.lookAt(null);
    Eye.blink();
    Eye.setDesign("cortana");
    Eye.init(null);
  });
  assert.equal(Eye.getState(), null);
});

test("engine: state transitions are continuous (no snapping)", () => {
  const e = Eye.createEngine(lcg(1));
  for (let i = 0; i < 120; i++) e.update(1 / 60);
  const before = { ...e.update(1 / 60) };
  e.setState("thinking");
  const after = e.update(1 / 60);
  assert.ok(Math.abs(after.x - before.x) < 2, "pupil x jumped");
  assert.ok(Math.abs(after.scale - before.scale) < 0.02, "scale jumped");
  for (let i = 0; i < 180; i++) e.update(1 / 60);
  assert.ok(Math.abs(e.sc - Eye.STATES.thinking.scale) < 0.03);
});

test("engine: thinking scans left and right", () => {
  const e = Eye.createEngine(lcg(7));
  e.setState("thinking");
  let minX = 0, maxX = 0;
  for (let i = 0; i < 60 * 5; i++) {
    const o = e.update(1 / 60);
    minX = Math.min(minX, o.x);
    maxX = Math.max(maxX, o.x);
  }
  assert.ok(minX < -3 && maxX > 3, `range ${minX}..${maxX}`);
});

test("engine: speaking follows setLevel; setLevel(null) opts into a pulse", () => {
  const e = Eye.createEngine(lcg(3));
  e.setState("speaking");
  for (let i = 0; i < 30; i++) { e.setLevel(1); e.update(1 / 60); }
  const loud = e.level;
  for (let i = 0; i < 15; i++) { e.setLevel(0); e.update(1 / 60); }
  const quiet = e.level;
  assert.ok(loud > 0.9 && quiet < 0.3, `${loud} ${quiet}`);
  // no meter for this playback (browser speech): synthetic pulse on request
  e.setLevel(null);
  let max = 0;
  for (let i = 0; i < 120; i++) { e.update(1 / 60); max = Math.max(max, e.level); }
  assert.ok(max > 0.3, `fallback pulse missing (${max})`);
  // out-of-range input is clamped
  e.setLevel(5); assert.equal(e.levelIn, 1);
  e.setLevel(-2); assert.equal(e.levelIn, 0);
  e.setLevel("nope"); assert.equal(e.levelIn, 0);
});

test("engine: confused shake decays", () => {
  const e = Eye.createEngine(lcg(5));
  e.setState("confused");
  e.update(1 / 60);
  assert.ok(e.shake > 0.9);
  for (let i = 0; i < 180; i++) e.update(1 / 60);
  assert.ok(e.shake < 0.01);
});

test("engine: blink squashes then reopens", () => {
  const e = Eye.createEngine(lcg(9));
  e.update(1 / 60);
  e.blink();
  let minLid = 1;
  for (let i = 0; i < 20; i++) minLid = Math.min(minLid, e.update(1 / 120).lid);
  assert.ok(minLid < 0.2);
  for (let i = 0; i < 10; i++) e.update(1 / 60);
  assert.equal(e.update(1 / 60).lid, 1);
});

test("engine: reduced motion = no saccades, shake, blink or spin", () => {
  const e = Eye.createEngine(lcg(11));
  e.reduced = true;
  e.setState("confused");
  e.blink();
  for (let i = 0; i < 300; i++) {
    const o = e.update(1 / 60);
    assert.equal(o.lid, 1);
    assert.equal(o.coreX, 0);
  }
  e.setState("thinking");
  for (let i = 0; i < 300; i++) e.update(1 / 60);
  assert.ok(Math.abs(e.px) < 0.01, `pupil moved ${e.px}`);
  assert.ok(Math.abs(e.rs) < 0.5, `ring spinning ${e.rs}`);
});

test("engine: lookAt pulls the pupil while listening, null releases", () => {
  const e = Eye.createEngine(lcg(13));
  e.setState("listening");
  e.lookAt(1, 0);
  for (let i = 0; i < 120; i++) e.update(1 / 60);
  assert.ok(e.px > 6, `px ${e.px}`);
  e.lookAt(null);
  for (let i = 0; i < 180; i++) e.update(1 / 60);
  assert.ok(Math.abs(e.px) < 1.5, `px ${e.px}`);
});

test("engine: huge dt (hidden tab) is clamped and stays finite", () => {
  const e = Eye.createEngine(lcg(17));
  const t0 = e.t;
  const o = e.update(1e6);
  assert.ok(e.t - t0 <= 0.1 + 1e-9);
  for (const v of Object.values(o)) assert.ok(Number.isFinite(v));
  e.update(-5);
  e.update(NaN);
  for (const v of Object.values(e.out)) assert.ok(Number.isFinite(v));
});

test("engine: unknown state falls back to idle, unknown design ignored", () => {
  const e = Eye.createEngine(lcg(19));
  e.setState("thinking");
  e.setState("bogus");
  assert.equal(e.state, "idle");
  e.setDesign("cortana");
  e.setDesign("nope");
  assert.equal(e.design, "cortana");
});

// ------------------------------------------------------------ regressions

test("engine: reduced motion freezes glow breathing and scan pulse, then settles", () => {
  for (const state of ["idle", "listening", "confused", "error"]) {
    const e = Eye.createEngine(lcg(23));
    e.reduced = true;
    e.setState(state);
    for (let i = 0; i < 600; i++) e.update(1 / 60); // let springs rest
    let gMin = Infinity, gMax = -Infinity, sMin = Infinity, sMax = -Infinity;
    for (let i = 0; i < 600; i++) {
      const o = e.update(1 / 60);
      gMin = Math.min(gMin, o.glow); gMax = Math.max(gMax, o.glow);
      sMin = Math.min(sMin, o.scanOpacity); sMax = Math.max(sMax, o.scanOpacity);
    }
    assert.ok(gMax - gMin < 0.005, `${state}: glow still breathing ${gMin}..${gMax}`);
    assert.ok(sMax - sMin < 0.005, `${state}: scan still pulsing ${sMin}..${sMax}`);
    assert.equal(e.settled(), true, `${state}: never settled`);
  }
  // without reduced motion the eye stays alive and never parks
  const live = Eye.createEngine(lcg(23));
  for (let i = 0; i < 600; i++) live.update(1 / 60);
  assert.equal(live.settled(), false);
});

test("engine: speaking is quiet while a meter is silent; pulse only on setLevel(null)", () => {
  const e = Eye.createEngine(lcg(29));
  e.setState("speaking");
  // waiting for the first Piper chunk: nothing has called setLevel yet
  let max = 0;
  for (let i = 0; i < 90; i++) { e.update(1 / 60); max = Math.max(max, e.level); }
  assert.ok(max < 0.01, `fake talking before audio (${max})`);
  // meter plays, then stops (gap between sentences): stays quiet
  for (let i = 0; i < 20; i++) { e.setLevel(0.8); e.update(1 / 60); }
  e.setLevel(0);
  for (let i = 0; i < 30; i++) e.update(1 / 60);
  // only the release tail may remain: the level must never rise again
  let prevL = e.level;
  for (let i = 0; i < 120; i++) {
    e.update(1 / 60);
    assert.ok(e.level <= prevL + 1e-12, `fake talking in a gap (${prevL} -> ${e.level})`);
    prevL = e.level;
  }
  assert.ok(e.level < 0.01, `level did not fall silent (${e.level})`);
  // a numeric level that goes stale (meter died without a final 0) decays
  e.setLevel(0.9);
  for (let i = 0; i < 120; i++) e.update(1 / 60);
  assert.ok(e.level < 0.05, `stale meter level held (${e.level})`);
  // leaving speaking resets the source
  e.setLevel(null);
  e.setState("idle");
  e.setState("speaking");
  max = 0;
  for (let i = 0; i < 60; i++) { e.update(1 / 60); max = Math.max(max, e.level); }
  assert.ok(max < 0.01, `synthetic mode leaked into next speaking (${max})`);
});

// --- minimal fake DOM to drive the real init()/setDesign()/render() path
function fakeDom() {
  function el(tag, attrs, classes, children) {
    const a = Object.assign({}, attrs);
    const node = {
      tagName: tag, children: children || [], parent: null, style: {},
      classList: {
        set: new Set(classes || []),
        add(c) { this.set.add(c); }, contains(c) { return this.set.has(c); },
      },
      getAttribute: (n) => (n in a ? a[n] : null),
      setAttribute: (n, v) => { a[n] = String(v); },
      hasAttribute: (n) => n in a,
      attrs: a,
      matches(sel) {
        const m = /^\.([\w-]+)(?:\[data-el="(\w+)"\])?$/.exec(sel);
        if (!m) return false;
        return node.classList.contains(m[1]) && (!m[2] || a["data-el"] === m[2]);
      },
      querySelectorAll(sel) {
        const out = [];
        (function walk(n) { for (const c of n.children) { if (c.matches(sel)) out.push(c); walk(c); } })(node);
        return out;
      },
      querySelector(sel) { return node.querySelectorAll(sel)[0] || null; },
      closest(sel) { let p = node.parent; while (p) { if (p.matches(sel)) return p; p = p.parent; } return null; },
      insertBefore(c) { c.parent = node; node.children.unshift(c); },
    };
    for (const c of node.children) c.parent = node;
    return node;
  }
  const design = (name, glowOps) => el("g", { "data-el": name }, ["design"], [
    el("g", {}, ["pupil"]),
    el("g", {}, ["shutters"]),
    el("g", {}, ["counter"]),
    ...glowOps.map((op) => el("circle", { opacity: op }, ["glow"])),
    el("rect", { opacity: "0.5" }, ["scan"]),
    el("circle", { opacity: "0.8" }, ["bloom"]),
    el("circle", {}, ["glow"]), // no authored opacity -> base 1
  ]);
  const svg = el("svg", { id: "eye", "data-design": "classic", "data-state": "idle" }, ["eye"], [
    design("classic", ["0.45", "0.68"]),
    design("iris", ["0.6"]),
    el("g", {}, ["eye-core"]),
  ]);
  const zone = el("div", {}, ["eyezone"], [svg]);
  const rafQ = [];
  const win = {
    document: {
      hidden: false,
      getElementById: (id) => (id === "eye" ? svg : null),
      createElement: (t) => el(t, {}, []),
      addEventListener() {},
    },
    addEventListener() {},
    requestAnimationFrame: (fn) => { rafQ.push(fn); return rafQ.length; },
    cancelAnimationFrame() {},
    matchMedia: () => ({ matches: false, addEventListener() {} }),
  };
  win.window = win;
  let now = 0;
  win.tick = (n = 1) => { for (let i = 0; i < n; i++) { const q = rafQ.splice(0); now += 16.7; q.forEach((f) => f(now)); } };
  win.pending = () => rafQ.length;
  return { win, svg, zone };
}

function loadInto(win) {
  const fs = require("node:fs");
  const vm = require("node:vm");
  const src = fs.readFileSync(path.join(__dirname, "..", "static", "eye.js"), "utf8");
  vm.runInNewContext(src, win);
  return win.HermesEye;
}

test("DOM: repeated setDesign / design switches never dim the eye", () => {
  const { win, svg } = fakeDom();
  const E = loadInto(win);
  assert.ok(E && svg.classList.contains("eye-live"), "auto-init did not run");
  win.tick(60);
  const glows = () => svg.querySelectorAll(".design[data-el=\"classic\"]")[0].querySelectorAll(".glow").map((g) => +g.getAttribute("opacity"));
  const authored = [0.45, 0.68, 1];
  for (let i = 0; i < 20; i++) { E.setDesign("classic"); win.tick(2); }
  for (let i = 0; i < 5; i++) { E.setDesign("iris"); win.tick(2); E.setDesign("classic"); win.tick(2); }
  win.tick(60);
  // Idle breathing/blinks make the absolute value time-dependent, so compare
  // each glow to its AUTHORED opacity. The old bug re-read the animated value
  // as the base on every setDesign, compounding (~0.7^30 -> 0): the eye went
  // dark. Healthy: every glow carries the same intensity factor, well above 0.
  const ratios = glows().map((v, i) => v / authored[i]);
  ratios.forEach((r, i) => assert.ok(r > 0.25 && r <= 1.0001, `glow ${i} dimmed to ${r.toFixed(3)} of its authored opacity`));
  ratios.forEach((r, i) => assert.ok(Math.abs(r - ratios[0]) < 0.02, `glow ${i} intensity ${r.toFixed(3)} differs from glow 0 ${ratios[0].toFixed(3)}`));
  // authored opacity is remembered, never overwritten
  const g0 = svg.querySelector(".glow");
  assert.equal(g0.getAttribute("data-base-opacity"), "0.45");
  assert.equal(svg.querySelectorAll(".glow")[2].getAttribute("data-base-opacity"), "1");
});

test("DOM: reduced motion parks the rAF loop and API calls wake it", () => {
  const { win } = fakeDom();
  win.matchMedia = () => ({ matches: true, addEventListener() {} });
  const E = loadInto(win);
  win.tick(120);
  assert.equal(win.pending(), 0, "loop still running while settled");
  E.setState("listening");
  assert.equal(win.pending(), 1, "setState did not wake the loop");
  win.tick(600);
  assert.equal(win.pending(), 0);
  E.setLevel(0.5);
  assert.equal(win.pending(), 1, "setLevel did not wake the loop");
});

// --- eye.css cascade rules (the browser cannot be run here; check the text)
const css = require("node:fs").readFileSync(path.join(__dirname, "..", "static", "eye.css"), "utf8")
  .replace(/\/\*[\s\S]*?\*\//g, "");
function ruleBody(selector) {
  const re = new RegExp("(^|})\\s*" + selector.replace(/[[\]"().#=*]/g, "\\$&") + "\\s*\\{([^}]*)\\}");
  const m = re.exec(css);
  assert.ok(m, `rule ${selector} missing`);
  return m[2];
}

test("eye.css: #eye keeps the height transition and drops it while resizing", () => {
  const t = /transition:\s*([^;]+);/.exec(ruleBody("#eye"))[1];
  assert.match(t, /\bfilter\b/);
  assert.match(t, /\bheight\b/, "#eye transition overrides .eye height transition");
  const r = /transition:\s*([^;]+);/.exec(ruleBody("body.resizing #eye"))[1];
  assert.doesNotMatch(r, /height/);
});

test("eye.css: confused and error filters share one function list (interpolable)", () => {
  const fns = (sel) => /filter:\s*([^;]+);/.exec(ruleBody(sel))[1].match(/[a-z-]+(?=\()/g).filter((f) => f !== "rgba");
  assert.deepEqual(fns('#eye[data-state="confused"]'), fns('#eye[data-state="error"]'));
});

test("DOM: .counter rings turn against the .shutters ring", () => {
  const { win, svg } = fakeDom();
  loadInto(win);
  win.tick(240);
  const g = svg.querySelectorAll(".design[data-el=\"classic\"]")[0];
  const angle = (cls) => +/rotate\((-?[\d.]+)/.exec(g.querySelector("." + cls).getAttribute("transform"))[1];
  const s = angle("shutters"), c = angle("counter");
  assert.ok(s > 0.5, `shutters did not turn: ${s}`);
  assert.ok(c < 0, `counter ring must turn the other way: ${c}`);
  assert.ok(Math.abs(c / s + 0.6) < 0.05, `counter ratio ${c / s}`);
});

test("index.html: every design in DESIGNS has markup and a settings option", () => {
  const html = require("node:fs").readFileSync(path.join(__dirname, "..", "static", "index.html"), "utf8");
  const marked = [...html.matchAll(/class="design" data-el="([a-z]+)"/g)].map((m) => m[1]).sort();
  assert.deepEqual(marked, Object.keys(Eye.DESIGNS).sort());
  const sel = /<select id="s_eye">([\s\S]*?)<\/select>/.exec(html)[1];
  const opts = [...sel.matchAll(/value="([a-z]+)"/g)].map((m) => m[1]).sort();
  assert.deepEqual(opts, Object.keys(Eye.DESIGNS).sort());
  assert.doesNotMatch(html, />BT-7274<\/text>/, "the BT-7274 stencil was removed from the eye");
  for (const d of Object.keys(Eye.DESIGNS)) assert.match(css, new RegExp(`\\.design\\[data-el="${d}"\\]`), `eye.css shows ${d}`);
});
