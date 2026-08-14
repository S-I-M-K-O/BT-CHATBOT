const messagesEl = document.getElementById("messages");
const input = document.getElementById("msgInput");
const sendBtn = document.getElementById("sendBtn");
const micBtn = document.getElementById("micBtn");
const statusDot = document.getElementById("statusDot");
const settingsPanel = document.getElementById("settingsPanel");
const settingsBtn = document.getElementById("settingsBtn");
const closeSettingsBtn = document.getElementById("closeSettingsBtn");
const eye = document.getElementById("eye");
const eyeBanner = document.getElementById("eyeBanner");
const eyeZone = document.getElementById("eyezone");
eye.setAttribute("data-design", "classic");

const EYE_LABELS = {
  idle: "READY",
  listening: "AWAITING CALL",
  thinking: "PROCESSING",
  speaking: "SPEAKING",
  confused: "UNCERTAIN",
  error: "ERROR",
};

const EYE_LABELS_DE = {
  idle: "BEREIT",
  listening: "HÖRE ZU",
  thinking: "DENKE NACH",
  speaking: "SPRICHT",
  confused: "UNSICHER",
  error: "FEHLER",
};

function isGermanVoice() {
  return settings.piperVoice === "JarvisDE";
}

let eyeState = "idle";

function setEye(state, banner) {
  eyeState = state;
  eye.setAttribute("data-state", state);
  eyeZone.setAttribute("data-state", state);
  const labels = isGermanVoice() ? EYE_LABELS_DE : EYE_LABELS;
  const label = labels[state] || labels.idle;
  eyeBanner.textContent = (banner || label).toUpperCase();
}

const history = [];

let settings = {
  localModel: "qwen3.5:latest",
  systemPrompt: "",
  temperature: 0.7,
  maxTokens: 1024,
  voice: "",
  voiceMode: "piper",
  piperVoice: "BT7274",
  pitch: 0.6,
  rate: -0.2,
  volume: 1.0,
  speak: true,
  call: true,
  eyeDesign: "classic",
};

let synth = window.speechSynthesis;
let voices = [];

function loadVoices() {
  voices = synth.getVoices();
  const sel = document.getElementById("s_voice");
  sel.innerHTML = "";
  voices.forEach((v) => {
    const opt = document.createElement("option");
    opt.value = v.name;
    opt.textContent = `${v.name} (${v.lang})`;
    sel.appendChild(opt);
  });
  if (settings.voice) sel.value = settings.voice;
}

if (synth) {
  loadVoices();
  synth.onvoiceschanged = loadVoices;
}

async function fetchSettings() {
  try {
    const res = await fetch("/api/settings");
    if (!res.ok) throw new Error();
    settings = await res.json();
    applySettingsToForm();
  } catch (e) {
    console.warn("could not load settings", e);
  }
}

function applySettingsToForm() {
  const id = (el) => (document.getElementById(el).value = settings[el.replace("s_", "")]);
  document.getElementById("s_localmodel").value = settings.localModel || "qwen3.5:latest";
  document.getElementById("s_prompt").value = settings.systemPrompt;
  document.getElementById("s_temp").value = settings.temperature;
  document.getElementById("s_max").value = settings.maxTokens;
  document.getElementById("s_engine").value = settings.voiceMode;
  if (settings.piperVoice) {
    const pv = document.getElementById("s_pipervoice");
    if ([...pv.options].some((o) => o.value === settings.piperVoice)) pv.value = settings.piperVoice;
  }
  document.getElementById("s_pitch").value = settings.pitch;
  document.getElementById("s_rate").value = settings.rate;
  document.getElementById("s_volume").value = settings.volume;
  document.getElementById("s_eye").value = settings.eyeDesign || "classic";
  document.getElementById("s_say").checked = settings.speak !== false;
  document.getElementById("s_call").checked = settings.call !== false;
  updateRangeLabels();
  toggleEngine();
  applyEyeDesign();
  if (settings.voice && synth) {
    const sel = document.getElementById("s_voice");
    if ([...sel.options].some((o) => o.value === settings.voice)) sel.value = settings.voice;
  }
}

function updateRangeLabels() {
  document.getElementById("s_pitch_val").textContent = document.getElementById("s_pitch").value;
  document.getElementById("s_rate_val").textContent = document.getElementById("s_rate").value;
  document.getElementById("s_vol_val").textContent = document.getElementById("s_volume").value;
}

function toggleEngine() {
  const engine = document.getElementById("s_engine").value;
  document.getElementById("browserVoiceGroup").classList.toggle("hidden", engine !== "browser");
  document.getElementById("piperVoiceGroup").classList.toggle("hidden", engine !== "piper");
}

function testAllBtn() {
  readForm();
  const check = isGermanVoice()
    ? "[Systemdiagnose: Stimmtest. Systeme online.]"
    : "[System diagnostic: voice check. Systems online.]";
  if (settings.voiceMode === "piper") speakServerPiper(check);
  else speak(check, true);
}

function applyEyeDesign() {
  const design = settings.eyeDesign || "classic";
  eye.setAttribute("data-design", design);
}

function readForm() {
  settings.localModel = document.getElementById("s_localmodel").value.trim() || "qwen3.5:latest";
  settings.systemPrompt = document.getElementById("s_prompt").value;
  settings.temperature = parseFloat(document.getElementById("s_temp").value) || 0.7;
  settings.maxTokens = parseInt(document.getElementById("s_max").value, 10) || 1024;
  settings.voiceMode = document.getElementById("s_engine").value;
  settings.voice = document.getElementById("s_voice").value;
  settings.piperVoice = document.getElementById("s_pipervoice").value || "BT7274";
  settings.pitch = parseFloat(document.getElementById("s_pitch").value) || 1;
  settings.rate = parseFloat(document.getElementById("s_rate").value) || 1;
  settings.volume = parseFloat(document.getElementById("s_volume").value) || 1;
  settings.eyeDesign = document.getElementById("s_eye").value;
  settings.speak = document.getElementById("s_say").checked;
  settings.call = document.getElementById("s_call").checked;
}

document.getElementById("settingsBtn").addEventListener("click", () => settingsPanel.classList.remove("hidden"));
document.getElementById("closeSettingsBtn").addEventListener("click", () => settingsPanel.classList.add("hidden"));
document.getElementById("s_engine").addEventListener("change", toggleEngine);
document.getElementById("s_eye").addEventListener("change", () => {
  settings.eyeDesign = document.getElementById("s_eye").value;
  applyEyeDesign();
});

["s_pitch", "s_rate", "s_volume"].forEach((id) => {
  document.getElementById(id).addEventListener("input", updateRangeLabels);
});

document.getElementById("saveBtn").addEventListener("click", async () => {
  readForm();
  const res = await fetch("/api/settings", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(settings),
  });
  const el = document.getElementById("saveMsg");
  el.textContent = res.ok ? "Saved." : "Save failed.";
  if (res.ok) settings = await res.json();
});

document.getElementById("testTTSBtn").addEventListener("click", testAllBtn);

function addMsg(role, text) {
  const div = document.createElement("div");
  div.className = "msg " + role;
  div.textContent = text;
  messagesEl.appendChild(div);
  return div;
}

function scrollBottom() {
  const chat = document.getElementById("chat");
  chat.scrollTop = chat.scrollHeight;
}

function cleanMarkdown(s) {
  return s.replace(/[*_~`]/g, "").replace(/^#+\s*/gm, "").trim();
}

function initPictureDoc(win) {
  if (win.document.getElementById("grid")) return;
  win.document.open();
  win.document.write(
    '<!doctype html><html><head><meta charset="utf-8">' +
      '<title>HERMES - Pictures</title>' +
      "<style>body{background:#0d1117;color:#e6edf3;font-family:system-ui,sans-serif;margin:0;padding:24px}" +
      "h1{color:#2ea8e6;font-size:20px;margin:0 0 4px}" +
      "p#caption{color:#8b949e;margin:0 0 16px}" +
      ".grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(320px,1fr));gap:12px}" +
      "img{max-width:100%;height:auto;border-radius:10px;box-shadow:0 4px 16px rgba(0,0,0,.4)}" +
      ".placeholder{color:#8b949e;font-style:italic}</style></head>" +
      "<body><h1>HERMES - Pictures</h1><p id='caption'></p><div id='grid' class='grid'></div></body></html>"
  );
  win.document.close();
}

function pictureWindow() {
  if (window.pictureWin && !window.pictureWin.closed) {
    const win = window.pictureWin;
    const grid = win.document.getElementById("grid");
    if (grid) grid.innerHTML = "";
    const cap = win.document.getElementById("caption");
    if (cap) cap.textContent = "";
    initPictureDoc(win);
    try {
      win.resizeTo(960, 720);
      win.moveTo(120, 80);
    } catch (e) {}
    win.focus();
    return win;
  }
  const win = window.open("", "hermes_pictures", "width=960,height=720,left=120,top=80");
  if (!win) return null;
  initPictureDoc(win);
  window.pictureWin = win;
  return win;
}

// Popup blockers only allow window.open within a user gesture. Once the named
// picture window is closed, re-creating it after the async fetch gets blocked
// and pictures silently fall back into the chat. Pre-opening the window on the
// first gesture keeps it reusable and never blocked.
function prefetchPictureWindow() {
  document.removeEventListener("pointerdown", prefetchPictureWindow, true);
  document.removeEventListener("keydown", prefetchPictureWindow, true);
  if (window.pictureWin && !window.pictureWin.closed) return;
  try {
    const win = window.open("", "hermes_pictures", "width=10,height=10,left=-10000,top=-10000");
    if (win) window.pictureWin = win;
  } catch (e) {}
}
document.addEventListener("pointerdown", prefetchPictureWindow, true);
document.addEventListener("keydown", prefetchPictureWindow, true);

function addPicture(win, src) {
  const img = win.document.createElement("img");
  img.src = src;
  img.alt = "Picture";
  win.document.getElementById("grid").appendChild(img);
}

function finishPictures(win, text, sawImage) {
  const doc = win.document;
  const cap = doc.getElementById("caption");
  if (cap && text) cap.textContent = text;
  if (!sawImage) {
    const p = doc.createElement("p");
    p.className = "placeholder";
    p.textContent = isGermanVoice() ? "Keine Bilder gefunden." : "No pictures found.";
    doc.getElementById("grid").appendChild(p);
  }
}

// Fallback when window.open is blocked: a fullscreen overlay styled like a
// separate window that always appears in front of the page.
function openPictureOverlay() {
  let ov = document.getElementById("picturesOverlay");
  if (!ov) {
    ov = document.createElement("div");
    ov.id = "picturesOverlay";
    ov.innerHTML =
      '<div class="pictures-window">' +
      '<div class="pictures-head"><h1>HERMES - Pictures</h1><button id="picturesClose" title="Close">✕</button></div>' +
      '<p class="pictures-caption"></p>' +
      '<div class="pictures-grid"></div></div>';
    document.body.appendChild(ov);
    ov.addEventListener("click", (e) => {
      if (e.target === ov || e.target.id === "picturesClose") closePictureOverlay();
    });
  }
  const grid = ov.querySelector(".pictures-grid");
  grid.innerHTML = "";
  const cap = ov.querySelector(".pictures-caption");
  cap.textContent = "";
  ov.classList.add("visible");
  return {
    addPicture: (src) => {
      const img = document.createElement("img");
      img.className = "bot-img";
      img.src = src;
      img.alt = "Picture";
      grid.appendChild(img);
    },
    setCaption: (t) => {
      cap.textContent = t;
    },
    addPlaceholder: (t) => {
      const p = document.createElement("p");
      p.className = "placeholder";
      p.textContent = t;
      grid.appendChild(p);
    },
  };
}

function closePictureOverlay() {
  const ov = document.getElementById("picturesOverlay");
  if (ov) ov.classList.remove("visible");
}

async function send(spokenText) {
  const text = (spokenText || input.value).trim();
  if (!text) return;
  if (!spokenText) input.value = "";
  history.push({ role: "user", content: text });
  addMsg("user", text);

  const botDiv = addMsg("bot", "…");
  botDiv.textContent = "";
  const botText = document.createElement("span");
  botDiv.appendChild(botText);

  let res;
  try {
    res = await fetch("/api/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ messages: history }),
    });
  } catch (e) {
    botDiv.textContent = isGermanVoice() ? "Verbindung verloren." : "Link lost.";
    setEye("confused", isGermanVoice() ? "VERBINDUNG VERLOREN" : "LINK LOST");
    return;
  }

  if (!res.ok) {
    botDiv.textContent = "Error: " + res.status;
    setEye("confused", isGermanVoice() ? "ANALYSE FEHLGESCHLAGEN" : "ANALYSIS FAILED");
    return;
  }

  setEye("thinking");

  const ctype = res.headers.get("content-type") || "";
  let full = "";
  let imgWin = null;
  let overlayWin = null;
  let sawImage = false;

  if (ctype.includes("text/event-stream")) {
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buf = "";
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      const lines = buf.split("\n");
      buf = lines.pop();
      for (const line of lines) {
        if (!line.trim() || !line.startsWith("data:")) continue;
        const payload = line.replace(/^data:\s*/, "").trim();
        if (payload === "[DONE]") continue;
        try {
          const obj = JSON.parse(payload);
          if (obj.intent === "image") {
            imgWin = pictureWindow();
            if (!imgWin) overlayWin = openPictureOverlay();
            continue;
          }
          if (obj.delta) full += obj.delta;
          if (obj.image) {
            sawImage = true;
            if (imgWin && !imgWin.closed) addPicture(imgWin, obj.image);
            else if (overlayWin) overlayWin.addPicture(obj.image);
            else {
              const img = document.createElement("img");
              img.className = "bot-img";
              img.src = obj.image;
              img.alt = "Generated image";
              botDiv.appendChild(img);
            }
          }
          if (obj.done) break;
          if (obj.choices) {
            const d = obj.choices[0] && obj.choices[0].delta;
            if (d && d.content) full += d.content;
          }
        } catch (e) {}
      }
      botText.textContent = full + "▊";
      scrollBottom();
    }
  } else {
    const data = await res.json();
    full = data.reply || "";
  }

  full = cleanMarkdown(full);
  botText.textContent = full;
  if (imgWin && !imgWin.closed) finishPictures(imgWin, full, sawImage);
  else if (overlayWin) {
    overlayWin.setCaption(full);
    if (!sawImage) {
      overlayWin.addPlaceholder(isGermanVoice() ? "Keine Bilder gefunden." : "No pictures found.");
    }
  }
  history.push({ role: "assistant", content: full });
  scrollBottom();

  if (settings.speak !== false && full) {
    if (settings.voiceMode === "piper") await speakServerPiper(full);
    else speak(full, true);
  } else {
    setEye(micOn ? "listening" : "idle");
  }
}

async function speak(text, markEye) {
  if (!synth) {
    if (markEye) setEye(micOn ? "listening" : "idle");
    return;
  }
  synth.cancel();
  const u = new SpeechSynthesisUtterance(text);
  if (settings.voice) {
    const found = voices.find((v) => v.name === settings.voice);
    if (found) u.voice = found;
  }
  u.pitch = parseFloat(settings.pitch) || 1;
  u.rate = parseFloat(settings.rate) || 1;
  u.volume = parseFloat(settings.volume) || 1;
  if (markEye) {
    if (micOn) pauseMic();
    setEye("speaking");
    u.onend = () => {
      setEye(micOn ? "listening" : "idle");
      if (micWantOn && !micOn) startMic();
    };
    u.onerror = () => {
      setEye(micOn ? "listening" : "idle");
      if (micWantOn && !micOn) startMic();
    };
  }
  synth.speak(u);
}

async function speakServerPiper(text) {
  return speakTTS(text, "piper", settings.piperVoice);
}

async function speakTTS(text, engine, voice) {
  try {
    setEye("speaking");
    const wasListening = micOn;
    if (micOn) pauseMic();
    const res = await fetch("/api/tts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text, voice, engine }),
    });
    if (!res.ok) {
      setEye(wasListening ? "listening" : "confused", res.status === 503 ? "VOICE OFFLINE" : "VOICE FAILED");
      if (micWantOn && !micOn) startMic();
      return;
    }
    const blob = await res.blob();
    const url = URL.createObjectURL(blob);
    const audio = new Audio(url);
    audio.volume = parseFloat(settings.volume) || 1;
    audio.play().catch(() => {
      // autoplay blocked; retry once after a microtask / gesture warmup
      primeAudio();
      audio.play().catch(() => {});
    });
    const done = () => {
      URL.revokeObjectURL(url);
      setEye(wasListening ? "listening" : "idle");
      if (micWantOn && !micOn) startMic();
    };
    audio.onended = done;
    audio.onerror = done;
  } catch (e) {
    console.warn("tts failed", e);
    setEye(micOn ? "listening" : "idle");
    if (micWantOn && !micOn) startMic();
  }
}

sendBtn.addEventListener("click", () => send());
input.addEventListener("keydown", (e) => {
  if (e.key === "Enter") send();
});
input.addEventListener("focus", () => {
  if (!micOn) setEye("listening");
});
input.addEventListener("blur", () => {
  if (!micOn && eyeState === "listening") setEye("idle");
});

// ---------- wake-word microphone ----------

const SR = window.SpeechRecognition || window.webkitSpeechRecognition;

let micOn = false;
let micWantOn = false;
let micArmed = false;
let micBusy = false;
let recognition = null;
let audioUnlocked = false;

function micSupported() {
  return !!SR;
}

function primeAudio() {
  if (audioUnlocked) return;
  const ctx = new (window.AudioContext || window.webkitAudioContext)();
  ctx.resume();
  const src = ctx.createOscillator?.() || ctx.createGain();
  if (src.start) src.start();
  if (src.stop) src.stop(0.01);
  if (ctx.close) ctx.close().catch(() => {});
  audioUnlocked = true;
}

document.addEventListener("pointerdown", primeAudio, { capture: true });
document.addEventListener("keydown", primeAudio, { capture: true });

function autoStartMic(e) {
  document.removeEventListener("pointerdown", autoStartMic, true);
  document.removeEventListener("keydown", autoStartMic, true);
  if (e.target.closest("#micBtn")) return;
  if (micWantOn && !micOn) {
    startMic();
    setEye("listening");
  }
}
document.addEventListener("pointerdown", autoStartMic, true);
document.addEventListener("keydown", autoStartMic, true);

function startMic() {
  if (!SR || recognition) return;
  micOn = true;
  micBtn.classList.add("active");
  recognition = new SR();
  recognition.continuous = true;
  recognition.interimResults = true;
  recognition.lang = navigator.language || "en-US";

  recognition.onresult = (event) => {
    let text = "";
    let isFinal = false;
    for (let i = event.resultIndex; i < event.results.length; i++) {
      const res = event.results[i];
      if (res.isFinal) isFinal = true;
      text += res[0].transcript;
    }
    text = text.trim();
    if (!text) return;
    input.value = text;
    handleTranscript(text, isFinal);
  };

  recognition.onerror = (e) => {
    if (e.error === "not-allowed" || e.error === "service-not-allowed") {
      stopMic();
      setEye("error", isGermanVoice() ? "MIKROFON GESPERRT" : "MIC BLOCKED");
    } else if (e.error === "no-speech") {
      // ignore; Chrome throws this constantly in continuous mode
    } else {
      console.warn("SR error:", e.error);
    }
  };

  recognition.onend = () => {
    recognition = null;
    if (micOn) startMic();
  };

  try {
    recognition.start();
  } catch (e) {
    console.warn("SR start failed:", e);
    recognition = null;
    micOn = false;
    micBtn.classList.remove("active");
  }
}

function stopMic() {
  micOn = false;
  micArmed = false;
  micBtn.classList.remove("active");
  if (recognition) {
    recognition.onend = null;
    try {
      recognition.abort();
    } catch (e) {}
    recognition = null;
  }
}

function pauseMic() {
  if (recognition) {
    recognition.onend = null;
    try {
      recognition.abort();
    } catch (e) {}
    recognition = null;
  }
  micOn = false;
  micArmed = false;
}

function handleTranscript(text, isFinal) {
  input.value = text;
  if (isFinal) sendSpoken(text, true);
}

function routeSpoken(text) {
  const lower = text.toLowerCase();
  const wakeAt = lower.indexOf("hermes");

  if (!settings.call) {
    return true; // send as-is
  }

  if (wakeAt >= 0) {
    const after = text.slice(wakeAt + "hermes".length).trim();
    if (after) return after;
    micArmed = true;
    if (!micBusy) {
      micBusy = true;
      setEye("listening", isGermanVoice() ? "JA?" : "YES?");
      if (settings.speak !== false) speakAck();
      micBusy = false;
    }
    return undefined;
  }

  if (micArmed) {
    micArmed = false;
    return text;
  }
  return undefined;
}

function sendSpoken(text, checkWake) {
  let payload = text;
  if (checkWake) payload = routeSpoken(text);
  if (payload === undefined || payload === true) {
    if (payload === true) send(text.trim());
    return;
  }
  const clean = payload.replace(/[.!?,]+$/, "").trim();
  if (clean) {
    input.value = "";
    send(clean);
  }
}

// Resume listening after the ack so the follow-up command is still captured.
// A short delay lets any speaker tail fade so HERMES does not echo his own ack.
function resumeAck(wasListening, wasArmed) {
  if (wasListening && micWantOn && !micOn) {
    setTimeout(() => {
      startMic();
      if (wasArmed) micArmed = true;
    }, 250);
  } else if (wasArmed) {
    micArmed = true;
  }
}

async function speakAck() {
  const wasListening = micOn;
  const wasArmed = micArmed;
  try {
    if (micOn) pauseMic();
    const res = await fetch("/api/tts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text: isGermanVoice() ? "Ja?" : "Yes?", engine: settings.voiceMode, voice: settings.piperVoice }),
    });
    if (!res.ok) {
      resumeAck(wasListening, wasArmed);
      return;
    }
    const blob = await res.blob();
    const url = URL.createObjectURL(blob);
    const audio = new Audio(url);
    audio.volume = parseFloat(settings.volume) || 1;
    const done = () => {
      URL.revokeObjectURL(url);
      resumeAck(wasListening, wasArmed);
    };
    audio.onended = done;
    audio.onerror = done;
    audio.play().catch(done);
  } catch (e) {
    resumeAck(wasListening, wasArmed);
  }
}

function toggleMic() {
  if (micOn) {
    micWantOn = false;
    stopMic();
    setEye("idle");
  } else {
    micWantOn = true;
    primeAudio();
    startMic();
    setEye("listening");
  }
}

micBtn.addEventListener("click", toggleMic);

const chatToggle = document.getElementById("chatToggle");
chatToggle.addEventListener("click", () => {
  const hidden = document.body.classList.toggle("chat-hidden");
  chatToggle.textContent = hidden ? "▴ Show chat" : "▾ Hide chat";
  if (!hidden) scrollBottom();
});
chatToggle.textContent = document.body.classList.contains("chat-hidden") ? "▴ Show chat" : "▾ Hide chat";

const EYE_FRAC_KEY = "hermes-eye-frac";
const chatBar = document.getElementById("chatBar");
const eyeEl = document.getElementById("eye");

function eyeRegionH() {
  const top = document.querySelector(".topbar").offsetHeight || 0;
  const bar = document.querySelector(".chatbar").offsetHeight || 0;
  const foot = document.querySelector(".inputbar").offsetHeight || 0;
  return Math.max(120, window.innerHeight - top - bar - foot);
}

let eyeFrac = parseFloat(localStorage.getItem(EYE_FRAC_KEY)) || 0.6;

function applyEyeFrac(f) {
  eyeFrac = Math.max(0.1, Math.min(0.9, f));
  localStorage.setItem(EYE_FRAC_KEY, eyeFrac);
  renderEye();
}

function renderEye() {
  document.documentElement.style.setProperty(
    "--eye-h",
    Math.round(eyeFrac * eyeRegionH()) + "px"
  );
}

chatBar.addEventListener("pointerdown", (e) => {
  if (e.button !== 0 || e.target.closest("button")) return;
  e.preventDefault();
  const startY = e.clientY;
  const startFrac = eyeFrac;
  const region = eyeRegionH();
  chatBar.setPointerCapture(e.pointerId);
  const onMove = (ev) => applyEyeFrac(startFrac + (ev.clientY - startY) / region);
  const onUp = () => {
    chatBar.removeEventListener("pointermove", onMove);
    chatBar.removeEventListener("pointerup", onUp);
  };
  chatBar.addEventListener("pointermove", onMove);
  chatBar.addEventListener("pointerup", onUp);
});

window.addEventListener("resize", renderEye);
renderEye();

fetchSettings().then(() => {
  statusDot.classList.add("online");
  if (micSupported()) {
    micWantOn = true;
    micBtn.classList.add("active");
    micBtn.title = "Click to stop always-on listening (say \u201CHermes\u201D first)";
  } else {
    micBtn.style.display = "none";
    micBtn.disabled = true;
  }
});