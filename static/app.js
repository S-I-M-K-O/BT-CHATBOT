const messagesEl = document.getElementById("messages");
const input = document.getElementById("msgInput");
const sendBtn = document.getElementById("sendBtn");
const statusDot = document.getElementById("statusDot");
const settingsPanel = document.getElementById("settingsPanel");
const settingsBtn = document.getElementById("settingsBtn");
const closeSettingsBtn = document.getElementById("closeSettingsBtn");
const eye = document.getElementById("eye");
const eyeLabel = document.getElementById("eyeLabel");
const eyeBanner = document.getElementById("eyeBanner");
const eyeZone = document.getElementById("eyezone");

const EYE_LABELS = {
  idle: "STANDBY",
  listening: "AWAITING INPUT",
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
  const label = EYE_LABELS[state] || "STANDBY";
  if (banner) {
    eyeLabel.textContent = banner.toUpperCase();
    eyeBanner.textContent = banner.toUpperCase();
  } else {
    eyeLabel.textContent = label;
    eyeBanner.textContent = label;
  }
}

const history = [];

let settings = {
  model: "qwen7b",
  systemPrompt: "",
  temperature: 0.7,
  maxTokens: 1024,
  voice: "",
  voiceMode: "piper",
  pitch: 0.6,
  rate: -0.2,
  volume: 1.0,
  serverVoice: "am_onyx",
  speak: true,
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
  document.getElementById("s_model").value = settings.model;
  document.getElementById("s_prompt").value = settings.systemPrompt;
  document.getElementById("s_temp").value = settings.temperature;
  document.getElementById("s_max").value = settings.maxTokens;
  document.getElementById("s_engine").value = settings.voiceMode;
  document.getElementById("s_pitch").value = settings.pitch;
  document.getElementById("s_rate").value = settings.rate;
  document.getElementById("s_volume").value = settings.volume;
  document.getElementById("s_servervoice").value = settings.serverVoice;
  document.getElementById("s_say").checked = settings.speak !== false;
  updateRangeLabels();
  toggleEngine();
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
  document.getElementById("serverVoiceGroup").classList.toggle("hidden", engine !== "server");
}

function testAllBtn() {
  readForm();
  if (settings.voiceMode === "server") speakServer("[System diagnostic: voice check. Pilot, systems online.]");
  else if (settings.voiceMode === "piper") speakServerPiper("[System diagnostic: voice check. Pilot, systems online.]");
  else speak("[System diagnostic: voice check. Pilot, systems online.]", true);
}

function readForm() {
  settings.model = document.getElementById("s_model").value.trim() || "qwen7b";
  settings.systemPrompt = document.getElementById("s_prompt").value;
  settings.temperature = parseFloat(document.getElementById("s_temp").value) || 0.7;
  settings.maxTokens = parseInt(document.getElementById("s_max").value, 10) || 1024;
  settings.voiceMode = document.getElementById("s_engine").value;
  settings.voice = document.getElementById("s_voice").value;
  settings.pitch = parseFloat(document.getElementById("s_pitch").value) || 1;
  settings.rate = parseFloat(document.getElementById("s_rate").value) || 1;
  settings.volume = parseFloat(document.getElementById("s_volume").value) || 1;
  settings.serverVoice = document.getElementById("s_servervoice").value.trim();
  settings.speak = document.getElementById("s_say").checked;
}

document.getElementById("settingsBtn").addEventListener("click", () => settingsPanel.classList.remove("hidden"));
document.getElementById("closeSettingsBtn").addEventListener("click", () => settingsPanel.classList.add("hidden"));
document.getElementById("s_engine").addEventListener("change", toggleEngine);

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

async function send() {
  const text = input.value.trim();
  if (!text) return;
  input.value = "";
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
    if (settings.voiceMode === "server") await speakServer(full);
    else if (settings.voiceMode === "piper") await speakServerPiper(full);
    else speak(full, true);
  } else {
    setEye("idle");
  }
}

async function speak(text, markEye) {
  if (!synth) {
    if (markEye) setEye("idle");
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
    setEye("speaking");
    u.onend = () => setEye("idle");
    u.onerror = () => setEye("idle");
  }
  synth.speak(u);
}

async function speakServer(text) {
  return speakTTS(text, "server", settings.serverVoice);
}

async function speakServerPiper(text) {
  return speakTTS(text, "piper");
}

async function speakTTS(text, engine, voice) {
  try {
    setEye("speaking", "SPEAKING");
    const res = await fetch("/api/tts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text, voice, engine }),
    });
    if (!res.ok) {
      setEye("confused", res.status === 503 ? "VOICE OFFLINE" : "VOICE FAILED");
      return;
    }
    const blob = await res.blob();
    const url = URL.createObjectURL(blob);
    const audio = new Audio(url);
    audio.play();
    audio.onended = () => {
      URL.revokeObjectURL(url);
      setEye("idle");
    };
    audio.onerror = () => setEye("idle");
  } catch (e) {
    console.warn("tts failed", e);
    setEye("idle");
  }
}

sendBtn.addEventListener("click", send);
input.addEventListener("keydown", (e) => {
  setEye("listening");
  if (e.key === "Enter") send();
});
input.addEventListener("focus", () => setEye("listening"));
input.addEventListener("blur", () => {
  if (eyeState === "listening") setEye("idle");
});

fetchSettings().then(() => {
  statusDot.classList.add("online");
});