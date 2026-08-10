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

let eyeState = "idle";

function setEye(state, banner) {
  eyeState = state;
  eye.setAttribute("data-state", state);
  eyeZone.setAttribute("data-state", state);
  const label = EYE_LABELS[state] || "READY";
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
  if (settings.voiceMode === "piper") speakServerPiper("[System diagnostic: voice check. Pilot, systems online.]");
  else speak("[System diagnostic: voice check. Pilot, systems online.]", true);
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

async function send(spokenText) {
  const text = (spokenText || input.value).trim();
  if (!text) return;
  if (!spokenText) input.value = "";
  history.push({ role: "user", content: text });
  addMsg("user", text);

  const botDiv = addMsg("bot", "…");

  let res;
  try {
    res = await fetch("/api/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ messages: history }),
    });
  } catch (e) {
    botDiv.textContent = "Link lost, Pilot.";
    setEye("confused", "LINK LOST");
    return;
  }

  if (!res.ok) {
    botDiv.textContent = "Error: " + res.status;
    setEye("confused", "ANALYSIS FAILED");
    return;
  }

  setEye("thinking", "PROCESSING");

  const ctype = res.headers.get("content-type") || "";
  let full = "";

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
          if (obj.delta) full += obj.delta;
          if (obj.done) break;
          if (obj.choices) {
            const d = obj.choices[0] && obj.choices[0].delta;
            if (d && d.content) full += d.content;
          }
        } catch (e) {}
      }
      botDiv.textContent = full + "▊";
      scrollBottom();
    }
  } else {
    const data = await res.json();
    full = data.reply || "";
  }

  botDiv.textContent = full;
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
    setEye("speaking", "SPEAKING");
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
      setEye("error", "MIC BLOCKED");
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
      setEye("listening", "YES?");
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

async function speakAck() {
  try {
    const res = await fetch("/api/tts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text: "Yes?", engine: settings.voiceMode, voice: settings.piperVoice }),
    });
    if (!res.ok) return;
    const blob = await res.blob();
    const url = URL.createObjectURL(blob);
    const audio = new Audio(url);
    audio.volume = parseFloat(settings.volume) || 1;
    audio.play().catch(() => {});
    audio.onended = () => URL.revokeObjectURL(url);
  } catch (e) {}
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
});
chatToggle.textContent = document.body.classList.contains("chat-hidden") ? "▴ Show chat" : "▾ Hide chat";

fetchSettings().then(() => {
  statusDot.classList.add("online");
  if (micSupported()) {
    micBtn.title = "Click to start always-on listening (say \u201CHermes\u201D first)";
  } else {
    micBtn.style.display = "none";
    micBtn.disabled = true;
  }
});