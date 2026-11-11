/*
 * HERMES web client.
 *
 * Sections:
 *   1. helpers (DOM, storage, i18n)        7. chat (send, stream, render)
 *   2. state + settings                    8. pictures overlay + lightbox
 *   3. eye                                 9. settings panel
 *   4. toasts                             10. status pills
 *   5. audio engine + voice FX            11. layout (eye / chat split)
 *   6. speech queue + microphone          12. hub panel + settings sections
 *                                         13. keyboard + boot, Hermes.api
 *
 * Classic script, no build step. Pure logic lives in lib.js (HermesLib); the
 * module bus is hermes.js (window.Hermes, SPEC 5.2): app.js fills
 * Hermes.api, emits the lifecycle events and runs the feature hooks.
 */
(function () {
  "use strict";

  var L = window.HermesLib;
  if (!L) {
    // lib.js failed to load; nothing sensible can run.
    return;
  }

  // The module bus. Without hermes.js the page still works, just without
  // feature modules: a tiny stand-in keeps every call site simple.
  var H =
    window.Hermes ||
    (function () {
      var ls = {};
      var hs = {};
      var stub = {
        v: 1,
        ready: false,
        api: {},
        on: function (n, f) {
          (ls[n] = ls[n] || []).push(f);
          return stub;
        },
        off: function () {
          return stub;
        },
        emit: function (n, d) {
          if (n === "ready") stub.ready = true;
          (ls[n] || []).slice().forEach(function (f) {
            try {
              f(d);
            } catch (e) {
              /* ignore */
            }
          });
          return stub;
        },
        hook: function (n, f) {
          (hs[n] = hs[n] || []).push(f);
          return function () {};
        },
        hooks: function (n) {
          return (hs[n] || []).slice();
        },
        registerTab: function () {
          return stub;
        },
        registerSettingsSection: function () {
          return stub;
        },
        tabs: function () {
          return [];
        },
        sections: function () {
          return [];
        },
        events: { start: function () {}, leader: false, active: false },
      };
      return stub;
    })();

  /* Emit on the bus without ever letting a module break the caller. */
  function emit(name, detail) {
    try {
      H.emit(name, detail);
    } catch (e) {
      /* a listener threw; hermes.js already logged it */
    }
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
    clientId: "hermes-client-id", // raw string, not JSON (read by other modules)
    hubTab: "hermes-hub-tab",
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
    hubBtn: $("hubBtn"),
    settingsSections: $("settingsSections"),
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
      confirmed: "CONFIRMED",
      filler: "One moment.",
      guest: "Guest",
      unknownSpeaker: "Unknown",
      speakingNow: "{name} is speaking now",
      earlier: "{n} earlier messages from someone else",
      earlierOne: "1 earlier message from someone else",
      hide: "Hide",
      pinTitle: "Enter PIN",
      pinText: "The personal lock is on. Enter the household PIN to show these messages.",
      pinWrong: "Wrong PIN.",
      pinLocked: "Too many attempts — try again in a minute.",
      pinFailed: "Unlock failed: {msg}",
      unlock: "Unlock",
      cancel: "Cancel",
      hubTitle: "Hub",
      hubEmpty: "No modules loaded yet.",
      notice: "Notice",
      hubBtnTitle: "Hub (G)",
      hubClose: "Close (Esc)",
      tzLabel: "Time zone",
      tzHint: "Used for reminders, appointments and the morning briefing.",
      tzInvalid: "Unknown time zone — use a name like Europe/Berlin.",
      tzUseBrowser: "Use this browser's zone ({tz})",
      cityLabel: "Home city",
      cityHint: "For the weather in the morning briefing.",
      briefingOnGreetingLabel: "Morning briefing on the first \u201cGood morning, Hermes\u201d (4:00\u201312:00)",
      briefingNewsLabel: "Headlines in the briefing (Google News)",
      cityPlaceholder: "e.g. Berlin",
      routerEnabledLabel: "Understand commands in natural language (tool router)",
      routerHint: "A small local model picks reminders, lists, smart home and more from what you say.",
      routerModelLabel: "Router model",
      extractModelLabel: "Memory model",
      extractHint: "Picks out facts worth remembering after a conversation.",
      embedModelLabel: "Embedding model",
      embedHint: "For finding memories and documents. Empty = keyword search only.",
      sameAsMain: "same as the main model",
      sameAsRouter: "same as the router model",
      numCtxLabel: "Context size",
      keepAliveLabel: "Keep models loaded",
      numCtxHint: "Tokens per request (2048–32768), the same for every call so Ollama never reloads a model. Keep-alive like 30m, 1h, or -1 for always.",
      fillerLabel: "Say “One moment.” when an answer takes longer",
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
      confirmed: "BESTÄTIGT",
      filler: "Moment.",
      guest: "Gast",
      unknownSpeaker: "Unbekannt",
      speakingNow: "Jetzt spricht {name}",
      earlier: "{n} frühere Nachrichten von jemand anderem",
      earlierOne: "1 frühere Nachricht von jemand anderem",
      hide: "Ausblenden",
      pinTitle: "PIN eingeben",
      pinText: "Die PIN-Sperre ist an. Gib die Haushalts-PIN ein, um diese Nachrichten anzuzeigen.",
      pinWrong: "Falsche PIN.",
      pinLocked: "Zu viele Versuche – bitte in einer Minute noch einmal.",
      pinFailed: "Entsperren fehlgeschlagen: {msg}",
      unlock: "Entsperren",
      cancel: "Abbrechen",
      hubTitle: "Zentrale",
      hubEmpty: "Noch keine Module geladen.",
      notice: "Hinweis",
      hubBtnTitle: "Zentrale (G)",
      hubClose: "Schließen (Esc)",
      tzLabel: "Zeitzone",
      tzHint: "Für Erinnerungen, Termine und das Morgenbriefing.",
      tzInvalid: "Unbekannte Zeitzone – nutze einen Namen wie Europe/Berlin.",
      tzUseBrowser: "Zeitzone dieses Browsers übernehmen ({tz})",
      cityLabel: "Heimatort",
      cityHint: "Für das Wetter im Morgenbriefing.",
      briefingOnGreetingLabel: "Morgenbriefing beim ersten \u201eGuten Morgen, Hermes\u201c (4\u201312 Uhr)",
      briefingNewsLabel: "Schlagzeilen im Briefing (Google News)",
      cityPlaceholder: "z. B. Berlin",
      routerEnabledLabel: "Befehle in natürlicher Sprache verstehen (Werkzeug-Router)",
      routerHint: "Ein kleines lokales Modell erkennt Erinnerungen, Listen, Smart Home und mehr in dem, was du sagst.",
      routerModelLabel: "Router-Modell",
      extractModelLabel: "Gedächtnis-Modell",
      extractHint: "Sucht nach einem Gespräch Fakten heraus, die sich zu merken lohnen.",
      embedModelLabel: "Embedding-Modell",
      embedHint: "Zum Finden von Gedächtnis-Einträgen und Dokumenten. Leer = nur Stichwortsuche.",
      sameAsMain: "wie das Hauptmodell",
      sameAsRouter: "wie das Router-Modell",
      numCtxLabel: "Kontextgröße",
      keepAliveLabel: "Modelle geladen halten",
      numCtxHint: "Tokens pro Anfrage (2048–32768), bei jedem Aufruf gleich, damit Ollama kein Modell neu lädt. Keep-alive z. B. 30m, 1h oder -1 für immer.",
      fillerLabel: "„Moment.“ sagen, wenn eine Antwort länger dauert",
    },
  };

  /*
   * Interface strings (UI language, Settings → General). STRINGS above holds
   * the eye banner and spoken lines; these are merged into it. data-i18n*
   * attributes in index.html name these keys; "-html" keys are trusted
   * constant markup.
   */
  var UI_STRINGS = {
    en: {
      skipLink: "Skip to message input",
      brandTag: "Vanguard Class · Assistant",
      pillsAria: "System status",
      pillBrain: "Brain",
      pillVoice: "Voice",
      pillSat: "Satellite",
      exportBtnTitle: "Export chat as Markdown",
      eyeModeBtnTitle: "Eye mode — show only the eye (E)",
      helpBtnTitle: "Keyboard shortcuts (?)",
      settingsBtnTitle: "Settings (Ctrl+,)",
      eyeModeExitTitle: "Leave eye mode (Esc)",
      heard: "HEARD",
      chatBarTitle: "Drag to resize the eye / chat split",
      chatAria: "Conversation",
      showChat: "Show chat",
      hideChat: "Hide chat",
      emptyTitle: "Standing by.",
      emptySub: "Say <kbd>Hermes</kbd>, hold <kbd>Space</kbd> to talk, or type below.",
      emptySubTouch: "Say <kbd>Hermes</kbd>, long-press the mic button to talk, or type below.",
      emptySubNoMic: "Type below to talk to HERMES.",
      jumpNew: "↓ New messages",
      suggestionsAria: "Suggestions",
      msgInputLabel: "Message HERMES",
      inputPh: "Say “Hermes”, hold Space, or type…",
      inputPhTouch: "Say “Hermes” or type…",
      inputPhPlain: "Message HERMES…",
      newChatTitle: "New chat (Ctrl+K)",
      send: "Send",
      stop: "Stop",
      sendAria: "Send message",
      stopAria: "Stop (Esc)",
      sendTitle: "Send (Enter)",
      stopTitle: "Stop talking / generating (Esc)",
      micStandby: "Microphone ready — click (or M) to start listening for “Hermes” (hold to talk)",
      micOnTitle: "Microphone on — say “Hermes” first (M to turn off, hold to talk)",
      micOffTitle: "Microphone off (M to turn on, hold to talk)",
      copy: "Copy",
      copied: "✓ Copied",
      copyTitle: "Copy message",
      speakAgain: "▶ Speak",
      speakAgainTitle: "Speak again",
      retry: "↻ Retry",
      retryTitle: "Retry this request",
      stoppedFlag: "· stopped",
      stoppedMsg: "Stopped.",
      thinkingAria: "HERMES is thinking",
      openPicture: "Open picture",
      pictureAlt: "Picture from HERMES",
      viewFull: "View full size",
      requestFailed: "Request failed: {msg}",
      exportTitle: "HERMES conversation",
      obTitle: "Protocol 1: Link established",
      dismiss: "Dismiss",
      obWake: "Say his name, wait for <b>YES?</b>, then talk — or say it all in one go: “Hermes, what time is it?”",
      obWakeKey: "\u201cHermes\u201d",
      obPttKey: "<kbd>Space</kbd>",
      obPtt: "Hold to talk directly — no wake word needed. On a phone, long-press the mic button.",
      obPttKeyTouch: "Mic",
      obPttTouch: "Long-press the mic button to talk directly — no wake word needed.",
      obType: "Or just type. <kbd>?</kbd> shows every shortcut.",
      obNote: "Voice input needs Chrome or Edge and microphone access.",
      obNoteNoMic: "Voice input isn't available in this browser — use Chrome or Edge for the wake word. Typing works everywhere.",
      obOk: "Understood",
      picturesTitle: "HERMES — Pictures",
      closeEsc: "Close (Esc)",
      close: "Close",
      picture: "Picture",
      download: "Download",
      openFull: "Open full size",
      scTitle: "Shortcuts",
      scSpaceKey: "<kbd>Space</kbd> (hold)",
      scSpace: "Push-to-talk — no wake word needed",
      scMic: "Toggle always-on microphone",
      scFocus: "Focus the message box",
      scEnter: "Send · <kbd>Shift</kbd>+<kbd>Enter</kbd> new line",
      scUp: "Previous message (empty box)",
      scEsc: "Stop talking / generating, close panels",
      scNew: "New chat",
      scSettings: "Settings",
      scHub: "Hub — memory and other modules",
      scEye: "Eye mode — only the eye, fullscreen (<kbd>Esc</kbd> to leave)",
      scHelp: "This help",
      scStopKey: "“Hermes, stop”",
      scStop: "Interrupt him by voice",
      // pills
      pillBrainBad: "Brain offline: {err}. Start Ollama (“ollama serve”) and check OLLAMA_BASE.",
      pillBrainNoErr: "Ollama is not reachable",
      pillBrainWarn: "Brain online, but the model “{model}” isn't installed. Run “ollama pull {model}” or pick another model in Settings.",
      pillBrainOk: "Brain online — Ollama is running {model}.",
      pillVoiceBrowserOk: "Voice: using the browser's speech engine.",
      pillVoiceBrowserBad: "Voice: this browser has no speech engine.",
      pillVoiceOk: "Voice online — local Piper is ready.",
      pillVoiceWarn: "Voice: Piper isn't installed, so HERMES uses the browser voice. Install Piper or set PIPER_BIN (the Docker image bundles it).",
      pillSatOk: "Satellite uplink online — live weather, time, news and image generation via Mistral.",
      pillSatOff: "Satellite offline (optional): add MISTRAL_API_KEY to .env for live weather, time, news and image generation.",
      serverUnreachable: "HERMES server unreachable — is “go run .” or the Docker container running?",
      // toasts
      undo: "Undo",
      autoplayToast: "Click anywhere on the page so HERMES is allowed to speak.",
      piperOffline: "Piper voice is offline — falling back to the browser voice.",
      piperOfflineNoSynth: "Piper voice is offline and this browser has no speech engine.",
      ttsFailed: "HERMES lost his voice for a moment: {msg}",
      ttsFailedDefault: "speech failed",
      micStoppedErrors: "Microphone stopped after repeated errors. Press M or the mic button to try again.",
      micDownloading: "Downloading Chrome's offline speech model — voice input will work without the internet in a moment.",
      micOnDevice: "Voice input switched to on-device recognition (works offline).",
      micLocalOnly: "Google speech recognition is off, but this Chrome can't recognise speech on the device. Voice input stays off — typing always works. (Settings → Voice & speech → Use Google speech recognition)",
      micBrave: "Brave doesn't support voice input (it has no speech service behind it). Open HERMES in Google Chrome or Edge — typing works here.",
      micNetwork: "Voice input can't reach Google's speech service, which Chrome uses for speech recognition. Check that a VPN, firewall or ad blocker isn't blocking Google — HERMES keeps retrying. Typing always works.",
      micBlockedToast: "Microphone access is blocked. Allow it via the icon in the address bar, then press M.",
      micNone: "No microphone found. Plug one in — HERMES keeps listening for it.",
      micLangUnsupported: "Speech recognition doesn't support {lang}.",
      needsChrome: "Voice input needs Chrome or Edge. You can always type instead.",
      copyFailed: "Couldn't copy — your browser blocked clipboard access.",
      tooLong: "That message is too long ({n} / {max} characters).",
      chatCleared: "Chat cleared.",
      nothingToExport: "Nothing to export yet — say something first.",
      exported: "Chat exported as {name}",
      exportFailed: "Export failed: {msg}",
      discarded: "Unsaved settings discarded.",
      saved: "Settings saved.",
      saveFailed: "Couldn't save settings: {msg}",
      unreachable: "server unreachable",
      defaultsLoaded: "Defaults loaded — press Save to keep them.",
      defaultsManaged: "Defaults loaded — values from hermes.conf stay. Press Save to keep them.",
      speakOffTesting: "“Speak replies out loud” is off — testing anyway.",
      loadFailed: "Couldn't load settings — using defaults. Is the HERMES server running?",
      eyeModeToast: "Eye mode — press Esc or E to return.",
      listeningToast: "Listening for “Hermes”.",
      micOffToast: "Microphone off.",
      // settings window
      setTitle: "Settings",
      setUnsaved: "unsaved",
      setBack: "Back to the categories",
      setClose: "Close (Esc)",
      setSearchPh: "Search settings",
      setCategories: "Categories",
      setNoResults: "No setting matches your search.",
      setReset: "Reset to defaults",
      setCancel: "Cancel",
      setSave: "Save settings",
      setNote: "Everything runs locally — Piper speaks, Ollama thinks. Changes preview live; press <b>Save</b> (or <kbd>Ctrl</kbd>+<kbd>Enter</kbd>) to keep them.",
      setEmptyCat: "Nothing to set here on this server.",
      uiLangLabel: "Interface language",
      uiLangAuto: "Automatic (like the voice)",
      uiLangHint: "Automatic: everything follows the voice. English/Deutsch: the screen texts change and only voices of that language are offered — HERMES then listens and answers in it.",
      voiceFollowsLang: "Voice switched to {voice} to match the language.",
      grpBehaviour: "Behaviour",
      speakLabel: "Speak replies out loud",
      callLabel: "Only answer when called by name (“Hermes”)",
      autoScrollLabel: "Auto-scroll chat",
      engineLabel: "Engine",
      enginePiper: "HERMES (local Piper)",
      engineBrowser: "Browser voice (OS)",
      piperVoiceLabel: "Piper voice",
      speedLabel: "Voice speed",
      fxLabel: "Voice effect",
      fxTitan: "Titan — BT-style cockpit voice",
      fxRadio: "Radio — comms channel",
      fxNone: "None — dry",
      browserVoiceLabel: "Browser voice",
      autoVoice: "Automatic (match language)",
      notAvailableHere: " (not available here)",
      loadingSuffix: " (loading…)",
      pitchLabel: "Pitch",
      rateLabel: "Rate",
      volumeLabel: "Volume",
      streamLabel: "Speak while the reply is still streaming",
      cloudSpeechLabel: "Use Google speech recognition",
      cloudSpeechHint: "On: Chrome sends what you say to Google to turn it into text. Off: speech is recognised on this device only (needs a recent Chrome; the language pack downloads once). If on-device recognition isn't available, voice input stays off and typing still works.",
      testVoice: "▶ Test voice",
      grpBrain: "Brain",
      localModelLabel: "Local model",
      customModelLabel: "Custom model name",
      customModelPh: "e.g. llama3.2:latest",
      customModel: "Custom…",
      notInstalledSuffix: " (not installed)",
      ollamaOffline: "Ollama is offline — start it with “ollama serve” to list installed models.",
      notInstalledHint: "Not installed yet — run “ollama pull {model}”.",
      modelsInstalled: "{n} model(s) installed in Ollama.",
      promptLabel: "System prompt",
      promptPh: "Leave empty for HERMES' default personality",
      tempLabel: "Temperature",
      maxTokensLabel: "Max tokens",
      eyeLabel: "Eye design",
      // config file
      managedBadge: "🔒 in hermes.conf",
      managedTip: "Set in hermes.conf — change it there (or remove the line) to edit it here.",
      cfgIntro: "Settings can also live in a text file. A key set there wins over this window and shows up locked here (🔒) — change it in the file. The server picks up changes within a few seconds.",
      cfgPath: "File",
      cfgStatus: "Status",
      cfgManaged: "Set by the file",
      cfgExists: "Present",
      cfgExistsAt: "Present — last changed {time}",
      cfgMissing: "Not created yet — every setting comes from this window.",
      cfgUnavailable: "This server doesn't offer a config file (yet).",
      cfgNone: "none",
      cfgErrorsTitle: "Problems in the file",
      cfgLine: "Line {n}",
      cfgExport: "Export hermes.conf",
      cfgWrite: "Create hermes.conf (template)",
      cfgWriteDone: "hermes.conf created with all values commented out. Remove the # in front of a line to set it from the file.",
      cfgWriteExists: "hermes.conf already exists.",
      cfgWriteFailed: "Couldn't create hermes.conf: {msg}",
      cfgAdminOnly: "Creating the file works only directly on the server machine (http://localhost). Export it here and copy it into the data folder instead.",
      cfgReloaded: "Settings updated from hermes.conf.",
      cfgFormatTitle: "Format",
      cfgFormatHint: "One “key = value” per line, keys as in the export. # starts a comment, [sections] only group. true/false, numbers, text (\"quotes\" only when needed). Tokens, passwords and the PIN do not belong here — they stay in .env or in this window.",
    },
    de: {
      skipLink: "Zur Nachrichteneingabe springen",
      brandTag: "Vanguard-Klasse · Assistent",
      pillsAria: "Systemstatus",
      pillBrain: "Gehirn",
      pillVoice: "Stimme",
      pillSat: "Satellit",
      exportBtnTitle: "Chat als Markdown exportieren",
      eyeModeBtnTitle: "Augenmodus – nur das Auge zeigen (E)",
      helpBtnTitle: "Tastenkürzel (?)",
      settingsBtnTitle: "Einstellungen (Strg+,)",
      eyeModeExitTitle: "Augenmodus verlassen (Esc)",
      heard: "GEHÖRT",
      chatBarTitle: "Ziehen, um Auge und Chat aufzuteilen",
      chatAria: "Unterhaltung",
      showChat: "Chat anzeigen",
      hideChat: "Chat ausblenden",
      emptyTitle: "Bereit.",
      emptySub: "Sag <kbd>Hermes</kbd>, halte <kbd>Leertaste</kbd> zum Sprechen oder tippe unten.",
      emptySubTouch: "Sag <kbd>Hermes</kbd>, halte den Mikrofon-Knopf gedrückt oder tippe unten.",
      emptySubNoMic: "Tippe unten, um mit HERMES zu sprechen.",
      jumpNew: "↓ Neue Nachrichten",
      suggestionsAria: "Vorschläge",
      msgInputLabel: "Nachricht an HERMES",
      inputPh: "Sag „Hermes“, halte die Leertaste oder tippe…",
      inputPhTouch: "Sag „Hermes“ oder tippe…",
      inputPhPlain: "Nachricht an HERMES…",
      newChatTitle: "Neuer Chat (Strg+K)",
      send: "Senden",
      stop: "Stopp",
      sendAria: "Nachricht senden",
      stopAria: "Stopp (Esc)",
      sendTitle: "Senden (Enter)",
      stopTitle: "Sprechen / Antworten stoppen (Esc)",
      micStandby: "Mikrofon bereit – klicken (oder M), um auf „Hermes“ zu hören (gedrückt halten zum Sprechen)",
      micOnTitle: "Mikrofon an – sag zuerst „Hermes“ (M zum Ausschalten, gedrückt halten zum Sprechen)",
      micOffTitle: "Mikrofon aus (M zum Einschalten, gedrückt halten zum Sprechen)",
      copy: "Kopieren",
      copied: "✓ Kopiert",
      copyTitle: "Nachricht kopieren",
      speakAgain: "▶ Vorlesen",
      speakAgainTitle: "Noch einmal vorlesen",
      retry: "↻ Wiederholen",
      retryTitle: "Anfrage wiederholen",
      stoppedFlag: "· abgebrochen",
      stoppedMsg: "Abgebrochen.",
      thinkingAria: "HERMES denkt nach",
      openPicture: "Bild öffnen",
      pictureAlt: "Bild von HERMES",
      viewFull: "In voller Größe ansehen",
      requestFailed: "Anfrage fehlgeschlagen: {msg}",
      exportTitle: "HERMES-Unterhaltung",
      obTitle: "Protokoll 1: Verbindung hergestellt",
      dismiss: "Schließen",
      obWake: "Sag seinen Namen, warte auf <b>JA?</b> und sprich – oder alles in einem Satz: „Hermes, wie spät ist es?“",
      obWakeKey: "\u201eHermes\u201c",
      obPttKey: "<kbd>Leertaste</kbd>",
      obPtt: "Gedrückt halten und direkt sprechen – ohne Aktivierungswort. Am Handy den Mikrofon-Knopf lange drücken.",
      obPttKeyTouch: "Mikro",
      obPttTouch: "Den Mikrofon-Knopf lange drücken und direkt sprechen – ohne Aktivierungswort.",
      obType: "Oder einfach tippen. <kbd>?</kbd> zeigt alle Tastenkürzel.",
      obNote: "Spracheingabe braucht Chrome oder Edge und Zugriff aufs Mikrofon.",
      obNoteNoMic: "Spracheingabe gibt es in diesem Browser nicht – für das Aktivierungswort Chrome oder Edge nutzen. Tippen geht überall.",
      obOk: "Verstanden",
      picturesTitle: "HERMES – Bilder",
      closeEsc: "Schließen (Esc)",
      close: "Schließen",
      picture: "Bild",
      download: "Herunterladen",
      openFull: "In voller Größe öffnen",
      scTitle: "Tastenkürzel",
      scSpaceKey: "<kbd>Leertaste</kbd> (halten)",
      scSpace: "Push-to-Talk – ohne Aktivierungswort",
      scMic: "Dauer-Mikrofon an/aus",
      scFocus: "Zum Eingabefeld",
      scEnter: "Senden · <kbd>Umschalt</kbd>+<kbd>Enter</kbd> neue Zeile",
      scUp: "Vorige Nachricht (leeres Feld)",
      scEsc: "Sprechen / Antworten stoppen, Fenster schließen",
      scNew: "Neuer Chat",
      scSettings: "Einstellungen",
      scHub: "Zentrale – Gedächtnis und andere Module",
      scEye: "Augenmodus – nur das Auge, Vollbild (<kbd>Esc</kbd> zum Verlassen)",
      scHelp: "Diese Hilfe",
      scStopKey: "„Hermes, stopp“",
      scStop: "Per Stimme unterbrechen",
      pillBrainBad: "Gehirn offline: {err}. Starte Ollama („ollama serve“) und prüfe OLLAMA_BASE.",
      pillBrainNoErr: "Ollama ist nicht erreichbar",
      pillBrainWarn: "Gehirn online, aber das Modell „{model}“ ist nicht installiert. Führe „ollama pull {model}“ aus oder wähle in den Einstellungen ein anderes Modell.",
      pillBrainOk: "Gehirn online – Ollama läuft mit {model}.",
      pillVoiceBrowserOk: "Stimme: Sprachausgabe des Browsers.",
      pillVoiceBrowserBad: "Stimme: Dieser Browser hat keine Sprachausgabe.",
      pillVoiceOk: "Stimme online – lokales Piper ist bereit.",
      pillVoiceWarn: "Stimme: Piper ist nicht installiert, HERMES nutzt die Browserstimme. Installiere Piper oder setze PIPER_BIN (das Docker-Image enthält es).",
      pillSatOk: "Satellitenverbindung online – Live-Wetter, Uhrzeit, Nachrichten und Bilder über Mistral.",
      pillSatOff: "Satellit offline (optional): MISTRAL_API_KEY in .env eintragen für Live-Wetter, Uhrzeit, Nachrichten und Bilder.",
      serverUnreachable: "HERMES-Server nicht erreichbar – läuft „go run .“ oder der Docker-Container?",
      undo: "Rückgängig",
      autoplayToast: "Klicke irgendwo auf die Seite, damit HERMES sprechen darf.",
      piperOffline: "Piper-Stimme offline – nutze die Browserstimme.",
      piperOfflineNoSynth: "Piper-Stimme offline und dieser Browser hat keine Sprachausgabe.",
      ttsFailed: "HERMES hat kurz die Stimme verloren: {msg}",
      ttsFailedDefault: "Sprachausgabe fehlgeschlagen",
      micStoppedErrors: "Mikrofon nach wiederholten Fehlern gestoppt. Drück M oder den Mikrofon-Knopf, um es erneut zu versuchen.",
      micDownloading: "Chrome lädt das Offline-Sprachmodell – gleich klappt die Spracheingabe auch ohne Internet.",
      micOnDevice: "Spracheingabe auf Erkennung im Gerät umgestellt (funktioniert offline).",
      micLocalOnly: "Google-Spracherkennung ist aus, aber Chrome kann hier nicht offline erkennen. Spracheingabe bleibt aus – tippen geht immer. (Einstellungen → Stimme & Sprache → Google-Spracherkennung)",
      micBrave: "Brave unterstützt keine Spracheingabe (dahinter steckt kein Sprachdienst). Öffne HERMES in Google Chrome oder Edge – tippen geht hier.",
      micNetwork: "Die Spracheingabe erreicht Googles Sprachdienst nicht, den Chrome zur Erkennung nutzt. Prüfe, ob ein VPN, eine Firewall oder ein Werbeblocker Google sperrt – HERMES versucht es weiter. Tippen geht immer.",
      micBlockedToast: "Mikrofonzugriff gesperrt. Erlaube ihn über das Symbol in der Adressleiste und drück dann M.",
      micNone: "Kein Mikrofon gefunden. Schließ eins an – HERMES wartet darauf.",
      micLangUnsupported: "Die Spracherkennung unterstützt {lang} nicht.",
      needsChrome: "Spracheingabe braucht Chrome oder Edge. Tippen geht immer.",
      copyFailed: "Kopieren nicht möglich – der Browser hat den Zugriff auf die Zwischenablage blockiert.",
      tooLong: "Die Nachricht ist zu lang ({n} / {max} Zeichen).",
      chatCleared: "Chat geleert.",
      nothingToExport: "Noch nichts zu exportieren – sag zuerst etwas.",
      exported: "Chat exportiert als {name}",
      exportFailed: "Export fehlgeschlagen: {msg}",
      discarded: "Ungespeicherte Einstellungen verworfen.",
      saved: "Einstellungen gespeichert.",
      saveFailed: "Einstellungen nicht gespeichert: {msg}",
      unreachable: "Server nicht erreichbar",
      defaultsLoaded: "Standardwerte geladen – zum Behalten auf Speichern drücken.",
      defaultsManaged: "Standardwerte geladen – Werte aus hermes.conf bleiben. Zum Behalten auf Speichern drücken.",
      speakOffTesting: "„Antworten laut vorlesen“ ist aus – teste trotzdem.",
      loadFailed: "Einstellungen nicht geladen – nutze Standardwerte. Läuft der HERMES-Server?",
      eyeModeToast: "Augenmodus – Esc oder E zum Zurückkehren.",
      listeningToast: "Höre auf „Hermes“.",
      micOffToast: "Mikrofon aus.",
      setTitle: "Einstellungen",
      setUnsaved: "ungespeichert",
      setBack: "Zurück zu den Bereichen",
      setClose: "Schließen (Esc)",
      setSearchPh: "Einstellungen durchsuchen",
      setCategories: "Bereiche",
      setNoResults: "Keine Einstellung passt zur Suche.",
      setReset: "Standardwerte",
      setCancel: "Abbrechen",
      setSave: "Speichern",
      setNote: "Alles läuft lokal – Piper spricht, Ollama denkt. Änderungen wirken sofort als Vorschau; mit <b>Speichern</b> (oder <kbd>Strg</kbd>+<kbd>Enter</kbd>) bleiben sie.",
      setEmptyCat: "Auf diesem Server gibt es hier nichts einzustellen.",
      uiLangLabel: "Sprache der Oberfläche",
      uiLangAuto: "Automatisch (wie die Stimme)",
      uiLangHint: "Automatisch: alles folgt der Stimme. Deutsch/English: die Texte wechseln und es werden nur Stimmen dieser Sprache angeboten – HERMES hört und antwortet dann in dieser Sprache.",
      voiceFollowsLang: "Stimme auf {voice} umgestellt, passend zur Sprache.",
      grpBehaviour: "Verhalten",
      speakLabel: "Antworten laut vorlesen",
      callLabel: "Nur antworten, wenn er beim Namen gerufen wird („Hermes“)",
      autoScrollLabel: "Chat automatisch scrollen",
      engineLabel: "Sprachausgabe",
      enginePiper: "HERMES (lokales Piper)",
      engineBrowser: "Browserstimme (Betriebssystem)",
      piperVoiceLabel: "Piper-Stimme",
      speedLabel: "Sprechtempo",
      fxLabel: "Stimmeffekt",
      fxTitan: "Titan – Cockpit-Stimme wie BT",
      fxRadio: "Funk – Funkkanal",
      fxNone: "Keiner – trocken",
      browserVoiceLabel: "Browserstimme",
      autoVoice: "Automatisch (passend zur Sprache)",
      notAvailableHere: " (hier nicht verfügbar)",
      loadingSuffix: " (lädt…)",
      pitchLabel: "Tonhöhe",
      rateLabel: "Tempo",
      volumeLabel: "Lautstärke",
      streamLabel: "Schon sprechen, während die Antwort noch entsteht",
      cloudSpeechLabel: "Google-Spracherkennung nutzen",
      cloudSpeechHint: "An: Chrome schickt das Gesprochene zur Umwandlung in Text an Google. Aus: Erkennung nur auf diesem Gerät (braucht ein aktuelles Chrome; das Sprachpaket lädt einmalig). Geht das im Gerät nicht, bleibt die Spracheingabe aus – tippen geht weiter.",
      testVoice: "▶ Stimme testen",
      grpBrain: "Gehirn",
      localModelLabel: "Lokales Modell",
      customModelLabel: "Eigener Modellname",
      customModelPh: "z. B. llama3.2:latest",
      customModel: "Eigenes…",
      notInstalledSuffix: " (nicht installiert)",
      ollamaOffline: "Ollama ist offline – starte es mit „ollama serve“, um die installierten Modelle zu sehen.",
      notInstalledHint: "Noch nicht installiert – führe „ollama pull {model}“ aus.",
      modelsInstalled: "{n} Modell(e) in Ollama installiert.",
      promptLabel: "Systemprompt",
      promptPh: "Leer lassen für HERMES' Standard-Persönlichkeit",
      tempLabel: "Temperatur",
      maxTokensLabel: "Max. Tokens",
      eyeLabel: "Augen-Design",
      managedBadge: "🔒 in hermes.conf",
      managedTip: "In hermes.conf festgelegt – dort ändern (oder die Zeile entfernen), um es hier einzustellen.",
      cfgIntro: "Einstellungen können auch in einer Textdatei stehen. Ein dort gesetzter Wert hat Vorrang vor diesem Fenster und ist hier gesperrt (🔒) – ändere ihn in der Datei. Der Server übernimmt Änderungen nach wenigen Sekunden.",
      cfgPath: "Datei",
      cfgStatus: "Status",
      cfgManaged: "Von der Datei gesetzt",
      cfgExists: "Vorhanden",
      cfgExistsAt: "Vorhanden – zuletzt geändert {time}",
      cfgMissing: "Noch nicht angelegt – alle Einstellungen kommen aus diesem Fenster.",
      cfgUnavailable: "Dieser Server bietet (noch) keine Konfigurationsdatei.",
      cfgNone: "keine",
      cfgErrorsTitle: "Probleme in der Datei",
      cfgLine: "Zeile {n}",
      cfgExport: "hermes.conf exportieren",
      cfgWrite: "hermes.conf als Vorlage anlegen",
      cfgWriteDone: "hermes.conf angelegt, alle Werte noch auskommentiert. # vor einer Zeile entfernen, damit die Datei sie festlegt.",
      cfgWriteExists: "hermes.conf gibt es schon.",
      cfgWriteFailed: "hermes.conf nicht angelegt: {msg}",
      cfgErr_syntax: "Formatfehler – erwartet wird „name = wert“",
      cfgErr_unknown_key: "unbekannte Einstellung",
      cfgErr_invalid_value: "ungültiger Wert{value} – die Zeile wird ignoriert",
      cfgErr_secret: "Tokens, Passwörter und Zugangsdaten gehören nicht in hermes.conf, sondern in .env oder Einstellungen › Integrationen",
      cfgErr_not_allowed: "kann nicht über hermes.conf gesetzt werden (nur hier im Fenster, mit PIN)",
      cfgErr_duplicate: "steht mehrfach in der Datei – der letzte Eintrag gilt",
      cfgErr_too_large: "Datei ist zu groß (über 256 KiB) und wird ignoriert",
      cfgErr_read_error: "Datei kann nicht gelesen werden",
      cfgAdminOnly: "Anlegen geht nur direkt am Server-Rechner (http://localhost). Exportiere die Datei hier und kopiere sie stattdessen in den data-Ordner.",
      cfgReloaded: "Einstellungen aus hermes.conf übernommen.",
      cfgFormatTitle: "Format",
      cfgFormatHint: "Pro Zeile „Schlüssel = Wert“, Schlüssel wie im Export. # beginnt einen Kommentar, [Abschnitte] gliedern nur. true/false, Zahlen, Text (\"Anführungszeichen\" nur wenn nötig). Tokens, Passwörter und die PIN gehören nicht hinein – die bleiben in .env oder in diesem Fenster.",
    },
  };
  L.SETTINGS_CATEGORIES.forEach(function (c) {
    UI_STRINGS.en["cat_" + c.id] = c.title.en;
    UI_STRINGS.de["cat_" + c.id] = c.title.de;
  });
  Object.keys(UI_STRINGS).forEach(function (l) {
    Object.keys(UI_STRINGS[l]).forEach(function (k) {
      if (!Object.prototype.hasOwnProperty.call(STRINGS[l], k)) STRINGS[l][k] = UI_STRINGS[l][k];
    });
  });

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

  /*
   * Two languages:
   *   voice language (isGerman / voiceLang): what HERMES hears, says and
   *     answers in; follows the voice that will actually speak.
   *   UI language (uiGerman / lang): every text on screen; settings.uiLang
   *     "de" / "en" pins it, "auto" follows the voice language.
   */
  function isGerman() {
    // browser engine: the language of the voice that will actually speak
    // (also in "Automatic" mode and before the voice list has loaded)
    if (settings.voiceMode === "browser") return /^de\b/i.test(browserSpeechLang());
    return piperVoiceLang() === "de";
  }
  function voiceLang() {
    return isGerman() ? "de" : "en";
  }
  function uiGerman() {
    return L.resolveUiLang(settings.uiLang, voiceLang()) === "de";
  }
  function lookup(dict, key) {
    return Object.prototype.hasOwnProperty.call(dict, key) ? dict[key] : Object.prototype.hasOwnProperty.call(STRINGS.en, key) ? STRINGS.en[key] : key;
  }
  /* Interface text in the UI language. */
  function t(key) {
    return lookup(uiGerman() ? STRINGS.de : STRINGS.en, key);
  }
  /* Spoken / reply text in the voice language. */
  function tv(key) {
    return lookup(isGerman() ? STRINGS.de : STRINGS.en, key);
  }
  function fill(str, vars) {
    return String(str).replace(/\{(\w+)\}/g, function (all, k) {
      return vars && vars[k] != null ? String(vars[k]) : all;
    });
  }
  /* t() with "{name}" placeholders. */
  function tf(key, vars) {
    return fill(t(key), vars);
  }
  /* The UI language ("de" / "en"); Hermes.api.lang() returns this. */
  function lang() {
    return uiGerman() ? "de" : "en";
  }

  // Piper voice key -> two-letter language. Seeded from the data-lang
  // attributes of the static options in index.html so the right language is
  // used before /api/status answers; populatePiperVoices() then fills it from
  // the server's voice list.
  var voiceLangByKey = {};
  Array.prototype.forEach.call(document.querySelectorAll("#s_pipervoice option[data-lang]"), function (o) {
    voiceLangByKey[o.value] = o.getAttribute("data-lang");
  });
  function piperVoiceLang() {
    return voiceLangByKey[settings.piperVoice] || "en";
  }
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
        var out = {
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
        // message tags from the server's "meta" event (SPEC 3.1) and the
        // display-only fields that go with them
        if (typeof m.src === "string" && m.src) out.src = m.src.slice(0, 64);
        if (typeof m.sp === "string") out.sp = m.sp.slice(0, 64);
        if (m.private === true) out.private = true;
        if (m.att === true && m.role === "user") {
          out.att = true;
          if (typeof m.attUrl === "string") out.attUrl = m.attUrl.slice(0, 2000);
        }
        if (typeof m.display === "string" && m.display) out.display = m.display.slice(0, 500);
        if (typeof m.retryText === "string" && m.retryText && out.att) out.retryText = m.retryText.slice(0, MAX_INPUT);
        if (typeof m.speaker === "string" && m.speaker) out.speaker = m.speaker.slice(0, 64);
        if (typeof m.spChange === "string" && m.spChange) out.spChange = m.spChange.slice(0, 64);
        if (typeof m.notice === "string" && m.notice && m.role === "assistant") out.notice = m.notice.slice(0, 32);
        if (m.role === "assistant" && Array.isArray(m.cards)) {
          var cards = m.cards.map(L.keepCard).filter(Boolean).slice(0, L.CARDS_PER_MESSAGE);
          if (cards.length) out.cards = cards;
        }
        return out;
      })
      .slice(-MAX_STORED);
  }
  var conv = loadConversation();

  function saveConversation() {
    store.set(
      KEYS.conv,
      conv
        .filter(function (m) {
          return !m.pending && !m.preparing && !m.ephemeral;
        })
        .slice(-MAX_STORED)
        .map(function (m) {
          // live-only fields (callbacks of notices) are not stored
          if (!m.actions) return m;
          var copy = Object.assign({}, m);
          delete copy.actions;
          return copy;
        })
    );
  }

  /* The persisted per-browser client id ("c_" + 12 hex), SPEC 3.1. */
  var clientIdValue = "";
  function clientId() {
    if (clientIdValue) return clientIdValue;
    var id = "";
    try {
      id = window.localStorage.getItem(KEYS.clientId) || "";
    } catch (e) {
      id = "";
    }
    if (!/^c_[0-9a-f]{12}$/.test(id)) {
      var bytes = new Uint8Array(6);
      if (window.crypto && window.crypto.getRandomValues) window.crypto.getRandomValues(bytes);
      else for (var i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256);
      id = "c_" + Array.prototype.map.call(bytes, function (b) {
        return (b < 16 ? "0" : "") + b.toString(16);
      }).join("");
      try {
        window.localStorage.setItem(KEYS.clientId, id);
      } catch (e) {
        /* private mode: the id lives for this page only */
      }
    }
    clientIdValue = id;
    return id;
  }

  function browserTimeZone() {
    try {
      var tz = Intl.DateTimeFormat().resolvedOptions().timeZone || "";
      return L.isValidTimeZone(tz) ? tz : "";
    } catch (e) {
      return "";
    }
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

  var eyeView = { override: null, overrideBanner: null, timer: 0, banner: "", state: "", flashBanner: null, flashTimer: 0 };

  function computeEyeState() {
    if (eyeView.override) return eyeView.override;
    // "speaking" only while audio actually plays; waiting for the next
    // sentence (LLM still generating, TTS fetch) is "thinking"
    if (speech.isAudible()) return "speaking";
    if (gen.active || speech.isActive() || (prep && prep.active)) return "thinking";
    // push-to-talk grace: key released, final result still on its way
    if (mic.armed || mic.ptt || mic.pttGrace) return "listening";
    if (mic.running && mic.wanted) return "listening";
    if (inputFocused) return "listening";
    return "idle";
  }

  function computeBanner(state) {
    if (eyeView.override && eyeView.overrideBanner) return eyeView.overrideBanner;
    if (eyeView.flashBanner) return eyeView.flashBanner;
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

  /* Show `text` in the banner for `ms` without changing the eye's state. */
  function flashBanner(text, ms) {
    clearTimeout(eyeView.flashTimer);
    eyeView.flashBanner = text || null;
    updateEye();
    eyeView.flashTimer = setTimeout(function () {
      eyeView.flashBanner = null;
      updateEye();
    }, ms || 1500);
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
    x.setAttribute("aria-label", t("dismiss"));
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
          toast(t("autoplayToast"), { type: "warn", key: "autoplay" });
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

  /*
   * The Piper voice for one sentence: the selected voice, unless the sentence
   * is clearly in the other language and a voice of that language is
   * installed — then that one, so German is never read with an English
   * accent (and vice versa). Returns {voice, lang}.
   */
  function voiceForSentence(text) {
    var own = piperVoiceLang();
    var lang = L.detectLang(text) || own;
    if (lang === own) return { voice: settings.piperVoice, lang: own };
    var voices = statusInfo && Array.isArray(statusInfo.voices) ? statusInfo.voices : [];
    for (var i = 0; i < voices.length; i++) {
      if (voices[i] && voices[i].key && voices[i].lang === lang) return { voice: voices[i].key, lang: lang };
    }
    return { voice: settings.piperVoice, lang: own };
  }

  function fetchTTS(text, signal, voiceKey) {
    var voice = voiceKey || settings.piperVoice;
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
    if (piperVoiceLang() === "de") return "de-DE";
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
    auto.textContent = t("autoVoice");
    sel.appendChild(auto);
    L.voicesForUiLang(browserVoices, settings.uiLang).forEach(function (v) {
      var opt = document.createElement("option");
      opt.value = v.name;
      opt.textContent = v.name + " (" + v.lang + ")";
      sel.appendChild(opt);
    });
    if (current && !voiceLangFor(current)) {
      var missing = document.createElement("option");
      missing.value = current;
      missing.textContent = current + (browserVoices.length ? t("notAvailableHere") : t("loadingSuffix"));
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
        var pick = voiceForSentence(item.text);
        item.promise = fetchTTS(L.normalizeForSpeech(item.text, pick.lang), item.controller && item.controller.signal, pick.voice).then(function (ab) {
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
        setPill("pillVoice", "warn", "pillVoiceWarn");
        if (!notified.piper) {
          notified.piper = true;
          toast(synth ? t("piperOffline") : t("piperOfflineNoSynth"), { type: "warn", key: "piper" });
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
        toast(tf("ttsFailed", { msg: (err && err.message) || t("ttsFailedDefault") }), { type: "error", key: "tts" });
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

  var ttsActive = false;
  function onActiveChange(active) {
    if (active) mic.onSpeechStart();
    else mic.onSpeechEnd();
    updateEye();
    if (active !== ttsActive) {
      ttsActive = active;
      emit(active ? "tts:start" : "tts:end");
    }
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
      entry.raw = fetchTTS(tv("ack"), entry.controller && entry.controller.signal);
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
      var h = speakBrowser(tv("ack"));
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

  // How long a spoken message waits for VoiceID's embedding (SPEC 4.11).
  var VOICE_WAIT_MS = 250;
  // utterance anchor state for VoiceID ("utterance:anchor" once per utterance)
  var utter = { anchored: false };
  // true while the enrolment wizard records (Hermes.api.setEnrolling)
  var enrolling = false;

  function perfNow() {
    return window.performance && performance.now ? performance.now() : now();
  }

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
    pttBuf: "", // finals heard while push-to-talk is held, sent on release
    pttPending: false, // the recogniser still holds unfinished (interim) speech
    pttShort: false, // grace timer is the short one (nothing seemed pending)
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
      } else {
        announceMic(false);
      }
      updateMicButton();
      updateEye();
    },

    start: function () {
      clearTimeout(mic.restartTimer);
      mic.restartTimer = 0;
      if (!SR || mic.rec || !mic.shouldRun()) return;
      // Google speech switched off: only ever start in on-device mode. The
      // first start checks (and if needed downloads) Chrome's local model;
      // tryOnDeviceRecognition() restarts the mic once it is ready.
      if (!settings.cloudSpeech && mic.local !== "on") {
        tryOnDeviceRecognition().then(function (ok) {
          if (!ok) explainLocalOnlyUnavailable();
        });
        return;
      }
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
      if (!settings.cloudSpeech && rec.processLocally !== true) {
        // this Chrome cannot force local processing: never fall back to Google
        mic.local = "unavailable";
        explainLocalOnlyUnavailable();
        return;
      }
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
        return;
      }
      announceMic(true);
    },

    stop: function () {
      clearTimeout(mic.restartTimer);
      mic.restartTimer = 0;
      // a bare "Hermes" waiting to be acknowledged dies with the session:
      // nothing would hear the follow-up
      clearTimeout(mic.wakeTimer);
      mic.wakeTimer = 0;
      mic.pendingWakeAt = 0;
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
      announceMic(false);
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
        toast(t("micStoppedErrors"), { type: "error", key: "mic" });
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

  /*
   * "mic:on" / "mic:off" for the feature modules (VoiceID opens and closes
   * its own capture with them). The recogniser restarts all the time (echo
   * guard, Chrome's session limit), so "off" is only announced once the mic
   * has really stayed off for a moment; a push-to-talk release dispatches
   * its utterance first.
   */
  var micAnnounce = { on: false, offTimer: 0 };
  function announceMic(on) {
    if (on) {
      clearTimeout(micAnnounce.offTimer);
      micAnnounce.offTimer = 0;
      if (micAnnounce.on) return;
      micAnnounce.on = true;
      emit("mic:on");
      return;
    }
    if (!micAnnounce.on || micAnnounce.offTimer || mic.shouldRun()) return;
    micAnnounce.offTimer = setTimeout(function () {
      micAnnounce.offTimer = 0;
      if (!micAnnounce.on || mic.shouldRun() || mic.rec) return;
      micAnnounce.on = false;
      emit("mic:off");
    }, 2000);
  }

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
    b.title = standby ? t("micStandby") : mic.wanted ? t("micOnTitle") : t("micOffTitle");
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

  function arm(ms) {
    ms = ms > 0 ? ms : ARM_MS;
    clearTimeout(mic.armTimer);
    mic.armed = true;
    dom.body.style.setProperty("--armed-ms", ms + "ms");
    // restart the countdown ring animation
    dom.body.classList.remove("armed");
    void dom.body.offsetWidth;
    mic.armTimer = setTimeout(function () {
      mic.armed = false;
      updateEye();
    }, ms);
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
    if (enrolling) return; // the enrolment wizard records: HERMES stays silent
    if (!mic.shouldRun()) return; // mic switched off meanwhile
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

    mic.pttPending = !!interim;
    if (mic.pttGrace && interim && mic.pttShort) {
      // more speech surfaced after release: give its final the full wait
      mic.pttShort = false;
      clearTimeout(mic.pttTimer);
      mic.pttTimer = setTimeout(endPTTSession, 2000);
    }

    if (interim && !finalText) {
      // the first words of a new utterance: VoiceID marks where it starts
      if (!utter.anchored && !enrolling) {
        utter.anchored = true;
        emit("utterance:anchor", { ptt: !!(mic.ptt || mic.pttGrace), at: perfNow() });
      }
      showTranscript((mic.ptt || mic.pttGrace) && mic.pttBuf ? mic.pttBuf + " " + interim : interim, "interim");
      // the user is still talking: keep the armed window open until the
      // final result arrives
      if (mic.armed) extendArm();
      var wi = L.matchWakeWord(interim);
      if (settings.call && wi.woke && wi.bare && !mic.armed && !mic.ptt && !mic.pttGrace && !enrolling) {
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
    // the next interim starts a new utterance (push-to-talk keeps its press
    // anchor until the release is dispatched)
    if (!mic.ptt && !mic.pttGrace) utter.anchored = false;
    var norm = L.normalizeSpeech(text);
    if (!norm) return;
    // Chrome re-emits the previous final after a restart: skip duplicates
    if (norm === mic.lastFinal.text && now() - mic.lastFinal.at < 4000) return;
    mic.lastFinal = { text: norm, at: now() };
    clearTimeout(mic.wakeTimer);
    mic.pendingWakeAt = 0;

    if (mic.ptt || mic.pttGrace) {
      // Chrome emits a final at every pause in continuous mode: collect them
      // while the key is held so one utterance stays one request
      mic.pttBuf = mic.pttBuf ? mic.pttBuf + " " + text : text;
      if (mic.ptt || mic.pttPending) showTranscript(mic.pttBuf, "interim");
      else endPTTSession(); // released and nothing left unfinished
      return;
    }
    dispatchFinal(text, false);
  }

  function dispatchFinal(text, ptt) {
    utter.anchored = false;
    // the enrolment wizard is recording: nothing it hears is a command
    if (enrolling) return;
    var d = L.decideFinal(text, { ptt: ptt, call: settings.call, armed: mic.armed });
    if (d.action === "send") {
      disarm();
      // VoiceID starts cutting the utterance now; the reply waits for it
      // at most VOICE_WAIT_MS (runReply)
      var expected = voiceCapturing();
      var voice = collectVoice();
      sendSpoken(d.command, { voice: voice, voiceExpected: expected });
    } else if (d.action === "ack") {
      emit("utterance:keep");
      ackWake();
    } else {
      emit("utterance:discard");
      if (d.action === "ignore") showTranscript(text, "interim"); // heard, but not addressed to HERMES
      // "echo": HERMES' own "Yes?" coming back while armed
    }
  }

  function sendSpoken(text, opts) {
    opts = opts || {};
    var clean = String(text || "")
      .replace(/^[\s,.;:!?-]+/, "")
      .replace(/[\s.!?,;:]+$/, "")
      .trim();
    if (!clean) {
      emit("utterance:keep");
      ackWake();
      return;
    }
    if (L.isStopCommand(clean)) {
      emit("utterance:discard");
      stopAll();
      return;
    }
    disarm();
    showTranscript(clean, "sent");
    send(clean, { spoken: true, voice: opts.voice || null, voiceExpected: !!opts.voiceExpected });
  }

  /* Is VoiceID capturing audio for utterances on this device? */
  function voiceCapturing() {
    var V = window.VoiceID;
    if (!V || typeof V.wanted !== "function") return false;
    try {
      // wanted() includes settings.voiceIdEnabled; capturing() is false when
      // the mic or the model failed, so the server must not expect a voice
      var cap = typeof V.capturing === "function" ? !!V.capturing() : true;
      return !!V.wanted() && !!settings.voiceIdEnabled && cap;
    } catch (e) {
      return false;
    }
  }

  /* The first registered collectVoice hook; a promise of the voice or null. */
  function collectVoice() {
    var fn = H.hooks("collectVoice")[0];
    if (!fn) return null;
    try {
      var p = fn();
      return p && typeof p.then === "function"
        ? p.then(null, function () {
            return null;
          })
        : Promise.resolve(p || null);
    } catch (e) {
      return null;
    }
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
          toast(t("micDownloading"), { type: "info", key: "mic" });
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
          toast(t("micOnDevice"), { type: "info", key: "mic" });
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

  /* Google speech is off but Chrome can't recognise speech on the device. */
  function explainLocalOnlyUnavailable() {
    if (mic.notified.localOnly) return;
    mic.notified.localOnly = true;
    toast(t("micLocalOnly"), { type: "warn", key: "mic", timeout: 12000 });
  }

  function explainNetworkError() {
    if (mic.notified.network) return;
    mic.notified.network = true;
    if (isBrave) {
      // Brave ships the API without Google's speech backend: it never works
      mic.wanted = false;
      mic.ensure();
      toast(t("micBrave"), { type: "error", key: "mic", timeout: 12000 });
      return;
    }
    toast(t("micNetwork"), { type: "warn", key: "mic", timeout: 12000 });
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
      toast(t("micBlockedToast"), { type: "error", key: "mic", timeout: 9000 });
      updateMicButton();
      return;
    }
    mic.failures += policy.failures;
    if (policy.failures) mic.transient = policy.transient;
    if (code === "audio-capture" && !mic.notified.capture) {
      mic.notified.capture = true;
      toast(t("micNone"), { type: "warn", key: "mic" });
    } else if (code === "network" && !mic.notified.network) {
      tryOnDeviceRecognition().then(function (ok) {
        if (!ok) explainNetworkError();
      });
    } else if (code === "language-not-supported" && !mic.notified.lang) {
      mic.notified.lang = true;
      toast(tf("micLangUnsupported", { lang: mic.lang }), { type: "warn", key: "mic" });
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
      toast(t("needsChrome"), { type: "warn", key: "ptt" });
      return;
    }
    if (mic.ptt) return;
    unlockAudio();
    if (speech.isBusy() || gen.active) stopAll(true);
    clearTimeout(mic.pttTimer);
    mic.blocked = false;
    var resumed = mic.pttGrace; // pressed again before the last final came
    mic.ptt = true;
    mic.pttGrace = false;
    mic.guard = false;
    mic.ignoreUntil = 0;
    disarm();
    if (Eye) Eye.blink();
    if (resumed) {
      // the recogniser only holds speech from the previous press: keep it
      // and its buffered text as part of the same utterance
      if (!mic.rec) mic.start();
    } else {
      utter.anchored = true;
      if (!enrolling) emit("utterance:anchor", { ptt: true, at: perfNow() });
      mic.pttBuf = "";
      mic.pttPending = false;
      // a fresh session drops audio from before the press, which would
      // otherwise arrive as a final while ptt is set and be sent unaddressed
      mic.lastFinal = { text: "", at: 0 };
      if (mic.rec) mic.restart(0);
      else mic.start();
    }
    updateMicButton();
    updateEye();
  }

  function endPTT() {
    if (!mic.ptt) return;
    mic.ptt = false;
    mic.pttGrace = true; // wait for the final result
    clearTimeout(mic.pttTimer);
    // everything said while held is already final: only wait briefly for a
    // last word that has not surfaced yet, instead of the full grace period
    mic.pttShort = !!mic.pttBuf && !mic.pttPending;
    mic.pttTimer = setTimeout(endPTTSession, mic.pttShort ? 700 : 2500);
    updateMicButton();
    updateEye();
  }

  function endPTTSession() {
    clearTimeout(mic.pttTimer);
    var text = mic.pttBuf;
    mic.pttBuf = "";
    mic.pttPending = false;
    mic.pttShort = false;
    mic.ptt = false;
    mic.pttGrace = false;
    utter.anchored = false;
    mic.ensure();
    if (text) dispatchFinal(text, true);
    else emit("utterance:discard");
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
      // the interface language, not the browser locale ("01:59 PM" in a German UI)
      var nav = (navigator.language || "").toLowerCase();
      var loc = nav.slice(0, 2) === lang() ? navigator.language : lang() === "de" ? "de-DE" : "en-GB";
      return new Date(ts).toLocaleTimeString(loc, { hour: "2-digit", minute: "2-digit" });
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
    // stored chat cards (cards.js); drawn directly, not re-emitted, so the
    // modules that toast on "card" stay quiet
    if (m.cards && window.HermesCards && typeof window.HermesCards.renderInto === "function") {
      m.cards.forEach(function (c) {
        try {
          window.HermesCards.renderInto(el, c);
        } catch (e) {
          console.error("[cards]", e);
        }
      });
    }
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
    el.classList.toggle("preparing", !!m.preparing);
    el.classList.toggle("notice", !!m.notice);
    if (m.role === "user") {
      // who spoke (voice ID); text only, never markup
      var who = el.querySelector(".msg-speaker");
      if (m.speaker) {
        if (!who) {
          who = document.createElement("div");
          who.className = "msg-speaker";
          el.insertBefore(who, bubble);
        }
        who.textContent = m.speaker;
      } else if (who) {
        who.remove();
      }
      // a link message shows its display text ("🔗 title"); the stored
      // content is the fixed "[Link: domain]" marker sent as history
      bubble.textContent = m.display || m.content;
    } else if (m.notice) {
      // local announcements (reminders, …) may carry server text: plain text
      bubble.textContent = "";
      var icon = document.createElement("span");
      icon.className = "notice-icon";
      icon.setAttribute("aria-hidden", "true");
      icon.textContent = NOTICE_ICONS[m.notice] || NOTICE_ICONS.info;
      var body = document.createElement("span");
      body.className = "notice-text";
      body.textContent = m.content;
      bubble.appendChild(icon);
      bubble.appendChild(body);
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
          md.innerHTML = '<span class="typing" aria-label="' + L.escapeHtml(t("thinkingAria")) + '"><span></span><span></span><span></span></span>';
        }
      } else if (!text && m.stopped) {
        md.innerHTML = '<p class="msg-flag">' + L.escapeHtml(t("stoppedMsg")) + "</p>";
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
            b.title = t("openPicture");
            var img = document.createElement("img");
            img.src = src;
            img.alt = t("pictureAlt");
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
    if (m.notice) {
      (m.actions || []).forEach(function (a) {
        if (!a || typeof a.fn !== "function") return;
        meta.appendChild(
          makeAction(String(a.label || ""), String(a.title || a.label || ""), "notice-action", function () {
            try {
              a.fn(m);
            } catch (e) {
              /* the module's problem */
            }
          })
        );
      });
      return;
    }
    if (m.role === "assistant" && !m.pending) {
      if (m.stopped) {
        var f = document.createElement("span");
        f.className = "msg-flag";
        f.textContent = t("stoppedFlag");
        meta.appendChild(f);
      }
      if (m.content) {
        meta.appendChild(
          makeAction(t("copy"), t("copyTitle"), "copy", function (ev) {
            copyText(L.cleanForDisplay(m.content), ev.currentTarget);
          })
        );
        meta.appendChild(
          makeAction(t("speakAgain"), t("speakAgainTitle"), "speak", function () {
            unlockAudio();
            speech.say(m.content);
          })
        );
      }
      if (m.error || m.stopped) {
        meta.appendChild(
          makeAction(t("retry"), t("retryTitle"), "retry", function () {
            retryMessage(m.id);
          })
        );
      }
    }
  }

  // collapsed runs of another speaker's turns the user opened (by run key)
  var expandedRuns = {};

  /* "Jetzt spricht Ben" above the first message after a speaker change. */
  function buildDivider(name) {
    var d = document.createElement("div");
    d.className = "speaker-divider";
    d.setAttribute("role", "separator");
    var span = document.createElement("span");
    span.textContent = tf("speakingNow", { name: name });
    d.appendChild(span);
    return d;
  }

  /* The "N earlier messages" row for a folded run of another speaker. */
  function buildCollapsedRow(run) {
    var row = document.createElement("div");
    row.className = "collapsed-run";
    var b = document.createElement("button");
    b.type = "button";
    b.className = "collapsed-btn";
    b.textContent = run.indices.length === 1 ? t("earlierOne") : tf("earlier", { n: run.indices.length });
    b.addEventListener("click", function () {
      var open = function () {
        expandedRuns[run.key] = true;
        renderAll(true);
      };
      if (settings.personalLock) requestUnlock().then(function (ok) {
        if (ok) open();
      });
      else open();
    });
    row.appendChild(b);
    return row;
  }

  function renderAll(keepScroll) {
    if (!dom.messages) return;
    var top = dom.chat ? dom.chat.scrollTop : 0;
    dom.messages.innerHTML = "";
    msgEls = {};
    var frag = document.createDocumentFragment();
    var plan = L.collapsePlan(conv, { keep: !!settings.keepHistoryOnSpeakerChange });
    var addMsg = function (m) {
      if (m.role === "user" && m.spChange) frag.appendChild(buildDivider(m.spChange));
      var el = buildMessageEl(m);
      el.style.animation = "none"; // no entrance animation on restore
      frag.appendChild(el);
    };
    plan.forEach(function (item) {
      if (item.kind === "msg") {
        addMsg(conv[item.index]);
      } else if (expandedRuns[item.key]) {
        item.indices.forEach(function (i) {
          addMsg(conv[i]);
        });
      } else {
        frag.appendChild(buildCollapsedRow(item));
      }
    });
    dom.messages.appendChild(frag);
    updateEmpty();
    if (keepScroll && dom.chat) dom.chat.scrollTop = top;
    else scrollToBottom(false);
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
      // a second click within 1.4 s must not make "✓ Copied" the label
      if (!btn.dataset.label) btn.dataset.label = btn.textContent;
      clearTimeout(btn._copiedTimer);
      btn.textContent = t("copied");
      btn.classList.add("done");
      btn._copiedTimer = setTimeout(function () {
        btn.textContent = btn.dataset.label;
        delete btn.dataset.label;
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
      else toast(t("copyFailed"), { type: "warn" });
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
    if (prevUser.att) {
      // a link message: its attachment is gone, so fetch the link again
      var again = prevUser.retryText || prevUser.attUrl;
      if (!again) return;
      if (idx === conv.length - 1) {
        stopAll(true);
        removeMessage(id);
        removeMessage(prevUser.id);
      }
      send(again);
      return;
    }
    if (idx === conv.length - 1) {
      stopAll(true);
      removeMessage(id);
      runReply();
    } else {
      send(prevUser.content);
    }
  }

  function stopAll(quiet) {
    var wasBusy = gen.active || speech.isBusy() || prep.active;
    // a user stop (button, Esc, "stopp"): reminders.js acks a reminder that is
    // being spoken right now (SPEC 5.5); quiet stops are internal and do not ack
    if (!quiet) emit("stop", { spoken: true });
    // a message still waiting for its beforeSend hooks is not sent any more
    if (prep.active) {
      prep.seq++;
      prep.active = false;
    }
    if (followUp) followUp.seq++;
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

  // a message waiting for its beforeSend hooks (links.js fetches a page)
  var prep = { seq: 0, active: false };
  var BEFORE_SEND_MS = 20000;

  /*
   * Run the beforeSend hooks in order; the first non-null result wins.
   * Resolves null when no hook wants the message (or they took > 20 s).
   */
  function runBeforeSend(hooks, text, opts) {
    var chain = hooks.reduce(function (p, fn) {
      return p.then(function (res) {
        if (res) return res;
        return Promise.resolve()
          .then(function () {
            return fn(text, opts);
          })
          .then(
            function (r) {
              return r && typeof r === "object" ? r : null;
            },
            function () {
              return null;
            }
          );
      });
    }, Promise.resolve(null));
    return Promise.race([
      chain,
      new Promise(function (resolve) {
        setTimeout(function () {
          resolve(null);
        }, BEFORE_SEND_MS);
      }),
    ]);
  }

  function send(text, opts) {
    opts = Object.assign({}, opts || {});
    text = String(text || "").trim();
    if (!text) return;
    if (text.length > MAX_INPUT) {
      toast(tf("tooLong", { n: text.length, max: MAX_INPUT }), { type: "warn" });
      return;
    }
    unlockAudio();
    stopAll(true); // a new request replaces the current one
    // the first exchange must not sit under the tips card
    if (onboard && !onboard.hidden) dismissOnboarding();
    if (!opts.spoken) pushInputHistory(text);
    var hooks = H.hooks("beforeSend");
    if (!hooks.length) {
      addMessage({ role: "user", content: text });
      saveConversation();
      runReply(opts);
      return;
    }
    var my = ++prep.seq;
    prep.active = true;
    var user = addMessage({ role: "user", content: text, preparing: true });
    updateEye(); // "PROCESSING" while a hook works
    var hookOpts = { spoken: !!opts.spoken };
    runBeforeSend(hooks, text, hookOpts).then(function (res) {
      if (my !== prep.seq || conv.indexOf(user) < 0) {
        // stopped or replaced meanwhile: keep the bubble, send nothing
        if (conv.indexOf(user) >= 0) {
          delete user.preparing;
          fillMessageEl(user);
          saveConversation();
        }
        return;
      }
      prep.active = false;
      delete user.preparing;
      if (res && res.cancel) {
        fillMessageEl(user);
        addMessage({ role: "assistant", content: String(res.message || tv("emptyReply")).slice(0, 2000), error: true });
        saveConversation();
        updateEye();
        return;
      }
      if (res && res.attachment && typeof res.attachment === "object") {
        var url = String(res.attachment.finalUrl || res.attachment.url || "").slice(0, 2000);
        user.att = true;
        user.attUrl = url;
        user.content = L.linkMarker(url); // never the (untrusted) page title
        user.display = String(res.display || text).slice(0, 500);
        user.retryText = text;
        opts.attachment = res.attachment;
      }
      fillMessageEl(user);
      saveConversation();
      runReply(opts);
    });
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

  /* The user message a reply answers: the last user turn in conv. */
  function lastUserMessage() {
    for (var i = conv.length - 1; i >= 0; i--) if (conv[i].role === "user") return conv[i];
    return null;
  }

  function cleanTag(v) {
    return typeof v === "string" ? v.slice(0, 64) : "";
  }

  /*
   * The server said who is speaking (SPEC 3.1): label the user bubble and,
   * when the speaker changed since the previous exchange, show the divider
   * and fold the other speaker's turns.
   */
  function applySpeaker(userMsg, sp) {
    var id = cleanTag(sp.id);
    var name = cleanTag(sp.name);
    var source = cleanTag(sp.source);
    var detail = { id: id, name: name, source: source };
    if (typeof sp.score === "number" && isFinite(sp.score)) detail.score = sp.score;
    emit("speaker", detail);
    if (!userMsg) return;
    var label = name || (source === "guest" ? t("guest") : "");
    var idx = conv.indexOf(userMsg);
    var prev = idx >= 0 ? L.previousSpeaker(conv, idx) : undefined;
    var changed = prev !== undefined && prev !== id;
    userMsg.speaker = label;
    if (changed && label) userMsg.spChange = label;
    else delete userMsg.spChange;
    var before = userMsg.sp;
    userMsg.sp = id; // provisional until "meta" confirms it
    if (changed || before !== id) renderAll(!settings.autoScroll);
    else fillMessageEl(userMsg);
  }

  function abortError() {
    var e = new Error("aborted");
    e.name = "AbortError";
    return e;
  }

  function runReply(opts) {
    opts = opts || {};
    var controller = typeof AbortController !== "undefined" ? new AbortController() : null;
    var my = ++gen.seq;
    gen.active = true;
    gen.controller = controller;
    var userMsg = lastUserMessage();
    emit("reply:start", { text: userMsg ? userMsg.content : "" });
    var bot = addMessage({ role: "assistant", content: "", pending: true, images: [] });
    var payload = {
      messages: L.buildRequestMessages(conv),
      clientId: clientId(),
      spoken: !!opts.spoken,
      voiceExpected: !!opts.voiceExpected,
    };
    var tz = browserTimeZone();
    if (tz) payload.timeZone = tz;
    if (opts.attachment) payload.attachment = opts.attachment;
    var fillerSaid = false;
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
      var live = my === gen.seq;
      if (ev.speaker && typeof ev.speaker === "object") applySpeaker(userMsg, ev.speaker);
      if (ev.card && typeof ev.card === "object" && typeof ev.card.kind === "string") {
        // kept with the message (capped) so the card is redrawn after a
        // reload or renderAll(); cards.js draws it now via the bus
        var kept = L.keepCard(ev.card);
        if (bot && kept) {
          bot.cards = (bot.cards || []).concat([kept]).slice(0, L.CARDS_PER_MESSAGE);
        }
        emit("card", { kind: ev.card.kind, data: ev.card.data, msgId: bot ? bot.id : "" });
      }
      if (ev.tool && typeof ev.tool === "object" && ev.tool.ok && live) {
        if (Eye) Eye.blink();
        flashBanner(t("confirmed"), 1200);
      }
      if (ev.meta && typeof ev.meta === "object") {
        // message tags: stored with both messages, sent back as history
        var src = cleanTag(ev.meta.src);
        var sp = typeof ev.meta.sp === "string" ? cleanTag(ev.meta.sp) : null;
        var priv = ev.meta.private === true;
        [userMsg, bot].forEach(function (m) {
          if (!m) return;
          if (src) m.src = src;
          if (sp !== null) m.sp = sp;
          if (priv) m.private = true;
          else delete m.private;
        });
      }
      if (ev.pending && typeof ev.pending === "object" && live) armFollowUp(8000);
      if (ev.busy === true && live) {
        flashBanner(t("thinking"), 1500);
        if (settings.fillerSpeech && !fillerSaid && speak) {
          fillerSaid = true;
          if (streamSpeak) speakSentence(tv("filler"));
          else speech.say(tv("filler"));
        }
      }
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

    // A spoken message waits up to VOICE_WAIT_MS for VoiceID's embedding.
    var voiceWait = opts.voice
      ? Promise.race([
          Promise.resolve(opts.voice).then(null, function () {
            return null;
          }),
          new Promise(function (resolve) {
            setTimeout(function () {
              resolve(null);
            }, VOICE_WAIT_MS);
          }),
        ])
      : Promise.resolve(null);

    var request = voiceWait
      .then(function (voice) {
        if (my !== gen.seq || (controller && controller.signal.aborted)) throw abortError();
        if (voice && typeof voice === "object" && !Array.isArray(voice)) payload.voice = voice;
        H.hooks("payload").forEach(function (fn) {
          try {
            fn(payload, opts);
          } catch (e) {
            /* a module's payload hook must not break the request */
          }
        });
        return fetch("/api/chat", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload),
          signal: controller ? controller.signal : undefined,
        });
      })
      .then(function (res) {
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
          text = failure.http ? tf("requestFailed", { msg: failure.message }) : tv("linkLostText");
          flashEye("confused", failure.http ? null : t("linkLost"), 4000);
        } else if (!text && !stopped && !failure && !bot.images.length) {
          // the model finished without a word (empty / thinking-only reply)
          text = tv("emptyReply");
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
        emit("reply:done", { text: text, error: !!bot.error });
      });
  }

  // --- follow-up: keep listening for the answer to HERMES' question

  var followUp = { seq: 0 };

  /*
   * Keep the recogniser armed (like after a bare "Hermes") for `ms`, once
   * the reply has finished and HERMES has stopped talking, so the answer to
   * his question needs no wake word.
   */
  function armFollowUp(ms) {
    ms = L.clamp(L.parseNum(ms, 8000), 1000, 60000);
    var my = ++followUp.seq;
    var started = now();
    (function check() {
      if (my !== followUp.seq || enrolling) return;
      if (now() - started > 120000) return; // the reply never ended
      if (gen.active || speech.isBusy()) {
        setTimeout(check, 150);
        return;
      }
      if (mic.shouldRun()) arm(ms);
    })();
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
    if (label) label.textContent = stopMode ? t("stop") : t("send");
    b.setAttribute("aria-label", stopMode ? t("stopAria") : t("sendAria"));
    b.title = stopMode ? t("stopTitle") : t("sendTitle");
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
    var want = show ? SUGGESTIONS[lang()] : [];
    if (box.dataset.lang === (show ? lang() : "none")) return;
    box.dataset.lang = show ? lang() : "none";
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
    toast(t("chatCleared"), {
      timeout: 7000,
      key: "clear",
      action: {
        label: t("undo"),
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
      toast(t("nothingToExport"), { type: "info" });
      return;
    }
    // folded turns of other speakers stay behind the PIN (SPEC 4.10)
    if (L.exportNeedsUnlock(conv, { lock: settings.personalLock, keep: settings.keepHistoryOnSpeakerChange, expanded: expandedRuns })) {
      requestUnlock().then(function (ok) {
        if (ok) writeChatExport();
      });
      return;
    }
    writeChatExport();
  }

  function writeChatExport() {
    var md =L.formatChatAsMarkdown(conv, { date: now(), title: t("exportTitle") });
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
      toast(tf("exported", { name: name }), { type: "success" });
    } catch (e) {
      toast(tf("exportFailed", { msg: e.message }), { type: "error" });
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

  // Files dropped on the chat area go to docs.js ("files:drop"), which
  // uploads them to the household's documents. Only real file drags are
  // taken over; dragged text or links keep the browser's behaviour.
  function isFileDrag(e) {
    var types = e.dataTransfer && e.dataTransfer.types;
    if (!types) return false;
    for (var i = 0; i < types.length; i++) if (types[i] === "Files") return true;
    return false;
  }
  var dragDepth = 0;
  function dragOver(e) {
    if (!isFileDrag(e)) return;
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = "copy";
    if (e.type === "dragenter") dragDepth++;
    if (dom.chat) dom.chat.classList.add("is-file-over");
  }
  function dragEnd() {
    dragDepth = 0;
    if (dom.chat) dom.chat.classList.remove("is-file-over");
  }
  on(dom.chat, "dragenter", dragOver);
  on(dom.chat, "dragover", dragOver);
  on(dom.chat, "dragleave", function (e) {
    if (!isFileDrag(e)) return;
    dragDepth = Math.max(0, dragDepth - 1);
    if (!dragDepth) dragEnd();
  });
  on(dom.chat, "drop", function (e) {
    if (!isFileDrag(e)) return;
    e.preventDefault();
    dragEnd();
    var files = e.dataTransfer && e.dataTransfer.files;
    if (files && files.length) emit("files:drop", files);
  });

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
        b.title = t("viewFull");
        var img = document.createElement("img");
        img.alt = t("pictureAlt");
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
        dl.textContent = t("download");
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
      cur.textContent = current + (statusInfo && statusInfo.ollama && statusInfo.ollama.ok ? t("notInstalledSuffix") : "");
      sel.insertBefore(cur, sel.firstChild);
    }
    var custom = document.createElement("option");
    custom.value = "__custom__";
    custom.textContent = t("customModel");
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
      hint.textContent = t("ollamaOffline");
      hint.classList.add("warn");
    } else if (model && (o.models || []).indexOf(model) < 0 && (o.models || []).indexOf(model + ":latest") < 0) {
      hint.textContent = tf("notInstalledHint", { model: model });
      hint.classList.add("warn");
    } else {
      hint.textContent = tf("modelsInstalled", { n: (o.models || []).length });
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
    // English interface: English voices only, German: German only. A saved
    // voice of the other language (e.g. from hermes.conf) stays listed so
    // saving never changes it behind the user's back.
    var shown = L.voicesForUiLang(voices, settings.uiLang);
    voices.forEach(function (v) {
      if (v && v.key === cur && shown.indexOf(v) < 0) shown.push(v);
    });
    shown.forEach(function (v) {
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
    setChecked("s_cloudspeech", s.cloudSpeech);
    fillDataSettings(s);
    refreshFormUi();
  }

  /* Every control in the settings panel bound with data-setting="<key>". */
  function dataSettingEls() {
    return panel ? Array.prototype.slice.call(panel.querySelectorAll("[data-setting]")) : [];
  }

  function controlKind(el) {
    var type = (el.getAttribute("type") || "").toLowerCase();
    if (el.tagName === "INPUT" && (type === "checkbox" || type === "number" || type === "range")) return type;
    return "text";
  }

  function fillDataSettings(s) {
    dataSettingEls().forEach(function (el) {
      var key = el.getAttribute("data-setting");
      if (!key) return;
      var v = s[key];
      var kind = controlKind(el);
      if (kind === "checkbox") el.checked = !!v;
      else if (el.tagName === "SELECT") {
        var want = v == null ? "" : String(v);
        var has = false;
        for (var i = 0; i < el.options.length; i++) if (el.options[i].value === want) has = true;
        if (!has && want) {
          // a value this build's list does not offer (newer server): keep it
          var o = document.createElement("option");
          o.value = want;
          o.textContent = want;
          el.appendChild(o);
        }
        el.value = want;
      } else el.value = v == null ? "" : String(v);
    });
  }

  function readDataSettings(out) {
    dataSettingEls().forEach(function (el) {
      var key = el.getAttribute("data-setting");
      if (!key || key === "__proto__" || key === "constructor" || key === "prototype") return;
      out[key] = L.coerceSettingValue(controlKind(el), el.value, el.checked, out[key]);
    });
    return out;
  }

  function readForm() {
    var sel = getVal("s_modelselect", "__custom__");
    var model = sel === "__custom__" ? getVal("s_localmodel", "").trim() : sel;
    // start from the current settings: fields without a control here (and
    // keys of newer modules) survive the round trip
    var out = Object.assign({}, settings, {
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
      cloudSpeech: getChecked("s_cloudspeech", settings.cloudSpeech),
    });
    // keys hermes.conf sets keep the server's value, whatever the form says
    return L.normalizeSettings(L.keepManaged(readDataSettings(out), savedSettings, managed));
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
    return L.settingsEqual(a, b);
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
    // switching Google speech on/off needs a fresh recogniser in the new mode
    if (prev && prev.cloudSpeech !== settings.cloudSpeech && mic.rec) mic.restart(100);
  }

  var shownLang = "";

  /* Re-render language-dependent UI when the UI language changed (or `force`). */
  function refreshLanguage(force) {
    var lang = uiGerman() ? "de" : "en";
    if (lang !== shownLang || force) {
      var changed = lang !== shownLang;
      shownLang = lang;
      if (document.documentElement) document.documentElement.lang = lang;
      eyeView.banner = "";
      updateEye();
      renderSuggestionsForce();
      relabelUi();
      if (changed || force) emit("lang", lang);
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
    if (prev.uiLang !== settings.uiLang) followUiLangWithVoice();
    applyLive(prev);
    setDirty(!settingsEqual(settings, savedSettings));
    if (getVal("s_modelselect") !== "__custom__") updateModelHint(settings.localModel);
  }

  /*
   * The interface language changed to Deutsch/English: list only voices of
   * that language and switch away from a voice of the other language, so
   * HERMES listens and talks in the language the user picked.
   */
  function followUiLangWithVoice() {
    var ui = settings.uiLang;
    populatePiperVoices();
    fillBrowserVoiceSelect(null);
    if (ui !== "de" && ui !== "en") return;
    var voices = statusInfo && Array.isArray(statusInfo.voices) ? statusInfo.voices : [];
    var next = L.pickVoiceForUiLang(voices, settings.piperVoice, ui, "key");
    var changed = "";
    if (next && next !== settings.piperVoice) {
      setVal("s_pipervoice", next);
      voices.forEach(function (v) {
        if (v.key === next) changed = v.name || v.key;
      });
    }
    if (settings.voice && !L.voiceLangMatches(voiceLangFor(settings.voice), ui)) setVal("s_voice", "");
    var prev = settings;
    settings = readForm();
    // rebuild once more: the voice of the other language was kept listed only
    // while it was still selected
    populatePiperVoices();
    fillBrowserVoiceSelect(null);
    applyLive(prev);
    if (changed) toast(t("voiceFollowsLang").replace("{voice}", changed), { type: "info", key: "uilang" });
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

  function openSettings(catId) {
    if (!panel || settingsOpen) return;
    if (hub && hub.open) closeHub();
    settingsOpen = true;
    settingsReturnFocus = document.activeElement;
    fillForm(settings);
    setDirty(!settingsEqual(settings, savedSettings));
    if (settingsSearch && settingsSearch.value) {
      settingsSearch.value = "";
      applySearch();
    }
    renderSettingsNav();
    showCategory(catId || settingsCat, { content: !!catId || !isPhone(), noFocus: true });
    reveal(backdrop);
    reveal(panel);
    var btn = $("settingsBtn");
    if (btn) btn.setAttribute("aria-expanded", "true");
    setTimeout(function () {
      var cur = settingsNav && settingsNav.querySelector('[aria-current="true"]');
      var target = !catId && cur && cur.offsetParent !== null ? cur : focusables()[1];
      if (target) target.focus({ preventScroll: true });
    }, 60);
    refreshStatus();
    fetchConfig();
    fetchAdmin();
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
      toast(t("discarded"), {
        type: "warn",
        timeout: 6000,
        key: "discard",
        action: {
          label: t("undo"),
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
        savedSettings = L.normalizeSettings(saved);
        store.set(KEYS.piper, savedSettings.piperVoice);
        store.set(KEYS.design, savedSettings.eyeDesign);
        // edits made while the POST was in flight are newer than the reply:
        // keep them in the form and live, still marked unsaved
        var current = settingsOpen ? readForm() : null;
        if (current && !settingsEqual(current, draft)) {
          settings = current;
          setDirty(!settingsEqual(settings, savedSettings));
        } else {
          settings = Object.assign({}, savedSettings);
          if (settingsOpen) fillForm(settings);
          setDirty(false);
        }
        applyLive(prev);
        refreshStatus();
        emit("settings", Object.assign({}, settings));
        toast(t("saved"), { type: "success", key: "save" });
        return true;
      })
      .catch(function (err) {
        toast(tf("saveFailed", { msg: err && err.message ? err.message : t("unreachable") }), { type: "error", key: "save" });
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
    // the browser's zone stays: an empty one would only be seeded again
    if (settings.timezone) defaults.timezone = settings.timezone;
    // values hermes.conf sets are not "defaults" here: they stay
    defaults = L.normalizeSettings(L.keepManaged(defaults, savedSettings, managed));
    var prev = settings;
    settings = defaults; // readForm() starts from here: every field resets
    fillForm(defaults);
    applyLive(prev);
    onFormChange();
    toast(configInfo.managed.length ? t("defaultsManaged") : t("defaultsLoaded"), { key: "reset" });
  }

  function testVoice() {
    unlockAudio();
    if (!settings.speak) {
      toast(t("speakOffTesting"), { key: "test" });
    }
    speech.say(tv("testLine"));
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
  on(panel, "input", function (e) {
    if (e.target === settingsSearch) return; // the search box is not a setting
    onFormChange();
  });
  on(panel, "change", function (e) {
    if (e.target === settingsSearch) return;
    onFormChange();
  });
  on($("cancelSettingsBtn"), "click", closeSettings);
  on(panel, "keydown", function (e) {
    if (e.key === "Escape" && e.target === settingsSearch && settingsSearch.value) {
      // Esc in the search box clears it first
      e.preventDefault();
      e.stopPropagation();
      settingsSearch.value = "";
      applySearch();
      return;
    }
    if ((e.key === "ArrowDown" || e.key === "ArrowUp") && e.target && e.target.classList && e.target.classList.contains("nav-item")) {
      var items = Array.prototype.filter.call(settingsNav.querySelectorAll(".nav-item"), function (n) {
        return !n.hidden;
      });
      var i = items.indexOf(e.target) + (e.key === "ArrowDown" ? 1 : -1);
      if (i >= 0 && i < items.length) {
        e.preventDefault();
        items[i].focus();
        showCategory(items[i].getAttribute("data-cat"), { content: !isPhone(), noFocus: true });
      }
      return;
    }
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

  // --- settings window: categories, search, managed keys, config file

  var settingsNav = $("settingsNav");
  var settingsBody = $("settingsBody");
  var settingsSearch = $("settingsSearch");
  var SETTINGS_CAT_KEY = "hermes-settings-cat";
  var settingsCat = store.get(SETTINGS_CAT_KEY, "general");
  if (!L.SETTINGS_CATEGORIES.some(function (c) {
    return c.id === settingsCat;
  })) settingsCat = "general";
  var phoneQuery = window.matchMedia ? window.matchMedia("(max-width: 640px)") : null;

  function isPhone() {
    return !!(phoneQuery && phoneQuery.matches);
  }
  function catEl(id) {
    return $("cat-" + id);
  }

  // stroke icons for the sidebar (24×24, currentColor)
  var CAT_ICONS = {
    general: '<path d="M4 7h10M18 7h2M4 17h4M12 17h8"/><circle cx="16" cy="7" r="2"/><circle cx="10" cy="17" r="2"/>',
    voice: '<path d="M4 10v4M8 7v10M12 4v16M16 8v8M20 11v2"/>',
    people: '<circle cx="9" cy="8" r="3.2"/><path d="M3.5 19c.8-3.2 3-5 5.5-5s4.7 1.8 5.5 5"/><circle cx="17" cy="9" r="2.4"/><path d="M16 14.2c2.3.2 3.9 1.8 4.5 4.3"/>',
    memory: '<path d="M9 4.5a3 3 0 0 0-5 2.4A3.2 3.2 0 0 0 4.5 13 3.3 3.3 0 0 0 9 18.8V4.5zM15 4.5a3 3 0 0 1 5 2.4 3.2 3.2 0 0 1-.5 6.1 3.3 3.3 0 0 1-4.5 5.8V4.5z"/>',
    reminders: '<path d="M6 16V11a6 6 0 0 1 12 0v5l1.5 2h-15z"/><path d="M10 20.5h4"/>',
    calendar: '<rect x="3.5" y="5" width="17" height="15" rx="2"/><path d="M3.5 10h17M8 3v4M16 3v4"/>',
    home: '<path d="M3.5 11.5L12 4l8.5 7.5"/><path d="M5.5 10v10h13V10"/><path d="M10 20v-5.5h4V20"/>',
    docs: '<path d="M14 3H6.5A1.5 1.5 0 0 0 5 4.5v15A1.5 1.5 0 0 0 6.5 21h11a1.5 1.5 0 0 0 1.5-1.5V8z"/><path d="M14 3v5h5"/><path d="M10.5 15.5l3-3a1.8 1.8 0 0 0-2.5-2.5l-3.5 3.5a2.8 2.8 0 0 0 4 4l2-2"/>',
    location: '<path d="M12 21s-6.5-6-6.5-11a6.5 6.5 0 0 1 13 0c0 5-6.5 11-6.5 11z"/><circle cx="12" cy="10" r="2.3"/>',
    privacy: '<path d="M12 3l7 3v5c0 4.6-3 8.3-7 10-4-1.7-7-5.4-7-10V6z"/><path d="M9.5 12l1.8 1.8 3.4-3.6"/>',
    models: '<rect x="6" y="6" width="12" height="12" rx="2"/><path d="M9 2.5v3.5M15 2.5v3.5M9 18v3.5M15 18v3.5M2.5 9h3.5M2.5 15h3.5M18 9h3.5M18 15h3.5"/>',
    appearance: '<path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="3"/>',
    config: '<path d="M14 3H6.5A1.5 1.5 0 0 0 5 4.5v15A1.5 1.5 0 0 0 6.5 21h11a1.5 1.5 0 0 0 1.5-1.5V8z"/><path d="M14 3v5h5M8.5 13h7M8.5 16.5h5"/>',
    more: '<circle cx="5.5" cy="12" r="1.4"/><circle cx="12" cy="12" r="1.4"/><circle cx="18.5" cy="12" r="1.4"/>',
  };

  /* A category is shown when it has something to set (module sections may be missing). */
  function catHasContent(id) {
    var c = catEl(id);
    if (!c) return false;
    var slot = c.querySelector(".cat-slot");
    var own = Array.prototype.some.call(c.children, function (n) {
      return n !== slot && !n.classList.contains("cat-title");
    });
    return own || !!(slot && slot.children.length);
  }

  function renderSettingsNav() {
    if (!settingsNav) return;
    var counts = settingsNav._counts || null;
    if (!settingsNav.children.length) {
      L.SETTINGS_CATEGORIES.forEach(function (c) {
        var b = el("button", { type: "button", class: "nav-item", "data-cat": c.id });
        var ic = el("span", { class: "nav-icon", "aria-hidden": "true" });
        ic.innerHTML = '<svg viewBox="0 0 24 24">' + (CAT_ICONS[c.id] || CAT_ICONS.more) + "</svg>"; // constants above
        b.appendChild(ic);
        b.appendChild(el("span", { class: "nav-label" }));
        b.appendChild(el("span", { class: "nav-count", "aria-hidden": "true" }));
        b.appendChild(el("span", { class: "nav-chev", "aria-hidden": "true" }));
        b.addEventListener("click", function () {
          if (settingsSearch && settingsSearch.value) {
            settingsSearch.value = "";
            applySearch();
          }
          showCategory(c.id, { content: true });
        });
        settingsNav.appendChild(b);
      });
    }
    Array.prototype.forEach.call(settingsNav.children, function (b) {
      var id = b.getAttribute("data-cat");
      var label = b.querySelector(".nav-label");
      var text = t("cat_" + id);
      if (label && label.textContent !== text) label.textContent = text;
      var has = catHasContent(id);
      b.hidden = !has;
      var c = catEl(id);
      if (c) {
        c.classList.toggle("cat-empty", !has);
        var slot = c.querySelector(".cat-slot");
        // one module section alone in its category: the category title is enough
        var solo = !!slot && slot.children.length === 1 && c.querySelectorAll(".setting").length === 0;
        c.classList.toggle("solo", solo);
      }
      var n = counts && Object.prototype.hasOwnProperty.call(counts, id) ? counts[id] : null;
      var cnt = b.querySelector(".nav-count");
      if (cnt) cnt.textContent = n == null ? "" : String(n);
      b.classList.toggle("no-hits", n === 0);
      var cur = id === settingsCat && !(counts && settingsSearch && settingsSearch.value);
      if (cur) b.setAttribute("aria-current", "true");
      else b.removeAttribute("aria-current");
    });
  }

  /*
   * Show one category. opts.content: on a phone, switch from the category
   * list to the content screen (the back button returns).
   */
  function showCategory(id, opts) {
    opts = opts || {};
    if (!catEl(id) || !catHasContent(id)) id = "general";
    settingsCat = id;
    store.set(SETTINGS_CAT_KEY, id);
    var searching = !!(settingsSearch && settingsSearch.value.trim());
    Array.prototype.forEach.call(document.querySelectorAll("#settingsBody .settings-cat"), function (c) {
      if (!searching) c.hidden = c.getAttribute("data-cat") !== id;
    });
    if (panel) panel.classList.toggle("show-content", !!opts.content || (!isPhone() && true));
    renderSettingsNav();
    if (settingsBody && !opts.keepScroll) settingsBody.scrollTop = 0;
    if (id === "config") fetchConfig();
    if (!opts.noFocus && isPhone()) {
      var back = $("settingsBackBtn");
      if (back) back.focus({ preventScroll: true });
    }
  }

  on($("settingsBackBtn"), "click", function () {
    if (settingsSearch && settingsSearch.value) {
      settingsSearch.value = "";
      applySearch();
    }
    if (panel) panel.classList.remove("show-content");
    var cur = settingsNav && settingsNav.querySelector('[aria-current="true"]');
    if (cur) cur.focus({ preventScroll: true });
  });

  /* Visible label text of a settings unit (options, textareas and badges excluded). */
  function unitText(node) {
    var out = [];
    var walk = function (n) {
      if (n.nodeType === 3) {
        out.push(n.nodeValue);
        return;
      }
      if (n.nodeType !== 1) return;
      var tag = n.tagName;
      if (tag === "OPTION" || tag === "TEXTAREA" || tag === "SCRIPT" || tag === "STYLE" || tag === "DATALIST" || tag === "SELECT") return;
      if (n.classList && n.classList.contains("managed-badge")) return;
      for (var c = n.firstChild; c; c = c.nextSibling) walk(c);
    };
    walk(node);
    return out.join(" ");
  }

  /* Filter every category by the search box (all words must match). */
  function applySearch() {
    if (!settingsBody) return;
    var q = settingsSearch ? settingsSearch.value.trim() : "";
    var searching = q !== "";
    if (panel) panel.classList.toggle("searching", searching);
    var counts = {};
    var total = 0;
    Array.prototype.forEach.call(settingsBody.querySelectorAll(".settings-cat"), function (c) {
      var id = c.getAttribute("data-cat");
      var units = c.querySelectorAll(".setting, .feature-section");
      var title = c.querySelector(".cat-title");
      var catHit = searching && title && L.settingsSearchMatch(q, title.textContent);
      var n = 0;
      Array.prototype.forEach.call(units, function (u) {
        // a .setting inside a matching feature section counts with that section
        var hit = !searching || catHit || L.settingsSearchMatch(q, unitText(u));
        u.classList.toggle("search-miss", !hit);
        // count what the user can see: not inside a module section (counted
        // as one) and not in a group hidden by the form (e.g. browser voice)
        var inner = u.parentNode && u.parentNode.closest && u.parentNode.closest(".feature-section");
        var off = u.closest && u.closest("[hidden]:not(.settings-cat)");
        if (hit && !inner && !off) n++;
      });
      if (searching) {
        var visible = n > 0 && catHasContent(id);
        c.hidden = !visible;
        counts[id] = visible ? n : 0;
        total += visible ? n : 0;
      } else {
        c.hidden = id !== settingsCat;
      }
    });
    if (settingsNav) settingsNav._counts = searching ? counts : null;
    var none = $("settingsNoResults");
    if (none) none.hidden = !searching || total > 0;
    if (searching && panel) panel.classList.add("show-content");
    renderSettingsNav();
  }
  on(settingsSearch, "input", function () {
    applySearch();
    if (settingsBody) settingsBody.scrollTop = 0;
  });

  // --- managed keys (hermes.conf)

  var configInfo = L.normalizeConfigInfo(null);
  var managed = L.managedSet([]);
  var configAdmin = null; // null: unknown yet
  var configFetch = null;

  // static controls of index.html -> setting key (data-setting controls name their own)
  var STATIC_BINDINGS = {
    s_modelselect: "localModel",
    s_localmodel: "localModel",
    s_prompt: "systemPrompt",
    s_temp: "temperature",
    s_max: "maxTokens",
    s_engine: "voiceMode",
    s_pipervoice: "piperVoice",
    s_speed: "voiceSpeed",
    s_fx: "voiceFx",
    s_voice: "voice",
    s_pitch: "pitch",
    s_rate: "rate",
    s_volume: "volume",
    s_eye: "eyeDesign",
    s_stream: "streamSpeech",
    s_say: "speak",
    s_call: "call",
    s_auto: "autoScroll",
    s_cloudspeech: "cloudSpeech",
  };

  function boundControls() {
    var out = [];
    Object.keys(STATIC_BINDINGS).forEach(function (id) {
      var c = $(id);
      if (c) out.push({ el: c, key: STATIC_BINDINGS[id] });
    });
    dataSettingEls().forEach(function (c) {
      out.push({ el: c, key: c.getAttribute("data-setting") });
    });
    return out;
  }

  /* Where a control's lock badge goes: its label (or the label for it). */
  function badgeHost(c) {
    var lab = c.closest ? c.closest("label") : null;
    if (!lab && c.id && panel) lab = panel.querySelector('label[for="' + c.id + '"]');
    return lab;
  }

  var managedApplying = false;
  function applyManaged() {
    if (!panel) return;
    managedApplying = true;
    try {
      var tip = t("managedTip");
      var badgeText = t("managedBadge");
      var wanted = [];
      boundControls().forEach(function (b) {
        var c = b.el;
        var on = L.isManaged(managed, b.key);
        if (on) {
          if (!c.disabled) c.disabled = true;
          if (c.getAttribute("data-managed") !== b.key) c.setAttribute("data-managed", b.key);
          if (c.title !== tip) c.title = tip;
          var host = badgeHost(c);
          if (host) wanted.push(host);
          if (host && !host.querySelector(".managed-badge")) {
            var badge = el("span", { class: "managed-badge", title: tip, text: badgeText });
            if (host.classList.contains("check") || host.contains(c) === false) host.appendChild(badge);
            else host.insertBefore(badge, c);
          }
        } else if (c.hasAttribute("data-managed")) {
          c.disabled = false;
          c.removeAttribute("data-managed");
          if (c.title === tip) c.removeAttribute("title");
        }
      });
      Array.prototype.forEach.call(panel.querySelectorAll(".managed-badge"), function (badge) {
        var host = badge.parentNode;
        if (wanted.indexOf(host) < 0) {
          badge.remove();
          return;
        }
        if (badge.textContent !== badgeText) badge.textContent = badgeText;
        if (badge.title !== tip) badge.title = tip;
      });
    } finally {
      managedApplying = false;
    }
  }

  // modules re-render their sections at any time: lock their new controls too
  if (settingsBody && typeof MutationObserver === "function") {
    var managedTimer = 0;
    new MutationObserver(function () {
      if (managedApplying || managedTimer) return;
      managedTimer = setTimeout(function () {
        managedTimer = 0;
        if (configInfo.managed.length || (panel && panel.querySelector("[data-managed]"))) applyManaged();
        if (settingsSearch && settingsSearch.value) applySearch();
      }, 30);
    }).observe(settingsBody, { childList: true, subtree: true });
  }

  function fmtTime(iso) {
    var d = iso ? new Date(iso) : null;
    if (!d || isNaN(d.getTime())) return "";
    try {
      return d.toLocaleString(lang() === "de" ? "de-DE" : "en-GB", { dateStyle: "medium", timeStyle: "short" });
    } catch (e) {
      return d.toISOString();
    }
  }

  /*
   * The server's error messages are English; in German, show a translation
   * by error code (the English detail stays in the tooltip).
   */
  function configErrorText(e) {
    if (!uiGerman() || !e.code) return e.msg || "?";
    var key = "cfgErr_" + e.code;
    var text = t(key);
    if (text === key) return e.msg || "?";
    var m = e.code === "invalid_value" && /"([^"]{0,80})"/.exec(e.msg || "");
    return text.replace("{value}", m ? " „" + m[1] + "“" : "");
  }

  function renderConfigPane() {
    var pathEl = $("cfgPath");
    if (!pathEl) return;
    var c = configInfo;
    pathEl.textContent = c.path || "data/hermes.conf";
    var status = $("cfgStatus");
    if (status) {
      status.textContent = !c.available ? t("cfgUnavailable") : c.exists ? (c.mtime ? tf("cfgExistsAt", { time: fmtTime(c.mtime) }) : t("cfgExists")) : t("cfgMissing");
      status.className = c.available && c.exists ? "ok" : "";
    }
    var list = $("cfgManagedList");
    if (list) {
      list.innerHTML = "";
      if (!c.managed.length) list.textContent = t("cfgNone");
      c.managed.forEach(function (k) {
        list.appendChild(el("code", { class: "cfg-key", text: k }));
        list.appendChild(document.createTextNode(" "));
      });
    }
    var errs = $("cfgErrors");
    var errList = $("cfgErrorList");
    if (errs && errList) {
      errs.hidden = !c.errors.length;
      errList.innerHTML = "";
      c.errors.forEach(function (e) {
        var where = e.line ? tf("cfgLine", { n: e.line }) : "";
        var head = [where, e.key].filter(Boolean).join(" · ");
        errList.appendChild(el("li", { title: e.msg || "" }, [head ? el("b", { text: head + ": " }) : null, configErrorText(e)]));
      });
    }
    var exp = $("cfgExportBtn");
    if (exp) exp.hidden = !c.available;
    var write = $("cfgWriteBtn");
    var admin = configAdmin === null ? !!(H.api.isLocalhost && H.api.isLocalhost()) : configAdmin;
    if (write) write.hidden = !c.available || c.exists || !admin;
    var hint = $("cfgActionHint");
    if (hint && !hint.classList.contains("sticky")) {
      var showAdminHint = c.available && !c.exists && !admin;
      hint.textContent = showAdminHint ? t("cfgAdminOnly") : "";
      hint.hidden = !showAdminHint;
    }
  }

  /*
   * GET /api/config. New managed keys take the server's value at once (an
   * open draft keeps everything else).
   */
  function fetchConfig() {
    if (configFetch) return configFetch;
    configFetch = fetchJSON("/api/config")
      .then(function (j) {
        configInfo = L.normalizeConfigInfo(j);
      })
      .catch(function (err) {
        // 404: an older server without config file support
        configInfo = L.normalizeConfigInfo(null);
        if (err && err.status && err.status !== 404 && window.console) console.warn("[Hermes] /api/config:", err.message);
      })
      .then(function () {
        configFetch = null;
        managed = L.managedSet(configInfo.managed);
        var kept = L.normalizeSettings(L.keepManaged(settings, savedSettings, managed));
        if (!settingsEqual(kept, settings)) {
          var prev = settings;
          settings = kept;
          if (settingsOpen) fillForm(settings);
          applyLive(prev);
          setDirty(!settingsEqual(settings, savedSettings));
        }
        renderConfigPane();
        applyManaged();
        return configInfo;
      });
    return configFetch;
  }

  function fetchAdmin() {
    if (configAdmin !== null) return;
    fetchJSON("/api/integrations")
      .then(function (j) {
        configAdmin = !!(j && typeof j === "object" && j.admin !== false);
      })
      .catch(function () {
        configAdmin = null;
      })
      .then(renderConfigPane);
  }

  on($("cfgWriteBtn"), "click", function (e) {
    var btn = e.currentTarget;
    var hint = $("cfgActionHint");
    btn.disabled = true;
    fetchJSON("/api/config/write", { method: "POST", body: {} })
      .then(function (j) {
        configInfo = L.normalizeConfigInfo(j);
        managed = L.managedSet(configInfo.managed);
        toast(t("cfgWriteDone"), { type: "success", key: "config" });
      })
      .catch(function (err) {
        var msg = err && err.status === 409 ? t("cfgWriteExists") : err && err.status === 403 ? t("cfgAdminOnly") : tf("cfgWriteFailed", { msg: (err && err.message) || "?" });
        if (err && err.status === 403) configAdmin = false;
        if (hint) {
          hint.textContent = msg;
          hint.hidden = false;
          hint.classList.add("warn");
        }
        toast(msg, { type: err && err.status === 409 ? "warn" : "error", key: "config" });
      })
      .then(function () {
        btn.disabled = false;
        return fetchConfig();
      });
  });

  /*
   * hermes.conf changed on disk (hub event "settings.changed"): reload the
   * config info and the settings and apply them live. An open, edited
   * window keeps its draft except for the keys the file now sets.
   */
  function reloadSettingsFromServer(source) {
    return fetchConfig()
      .then(function () {
        return fetchJSON("/api/settings");
      })
      .then(function (s) {
        if (!s || typeof s !== "object") return;
        var prev = settings;
        var dirty = settingsOpen && !settingsEqual(settings, savedSettings);
        savedSettings = L.normalizeSettings(s);
        store.set(KEYS.piper, savedSettings.piperVoice);
        store.set(KEYS.design, savedSettings.eyeDesign);
        settings = dirty ? L.normalizeSettings(L.keepManaged(settings, savedSettings, managed)) : Object.assign({}, savedSettings);
        if (settingsOpen) fillForm(settings);
        setDirty(!settingsEqual(settings, savedSettings));
        applyLive(prev);
        applyManaged();
        emit("settings", Object.assign({}, settings));
        if (source === "config" && !settingsEqual(prev, settings)) toast(t("cfgReloaded"), { type: "info", key: "config" });
      })
      .catch(function () {
        /* offline: the next change or page load catches up */
      });
  }
  H.on("hub:settings.changed", function (ev) {
    var data = ev && (ev.data || ev);
    reloadSettingsFromServer(data && data.source === "config" ? "config" : "");
  });

  if (phoneQuery) {
    var onPhoneChange = function () {
      if (!settingsOpen) return;
      if (!isPhone() && panel) panel.classList.add("show-content");
    };
    if (typeof phoneQuery.addEventListener === "function") phoneQuery.addEventListener("change", onPhoneChange);
    else if (typeof phoneQuery.addListener === "function") phoneQuery.addListener(onPhoneChange);
  }

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
        emit("settings", Object.assign({}, settings));
        seedTimeZone();
      })
      .catch(function () {
        applyLive(null);
        toast(t("loadFailed"), { type: "warn", key: "settings-load" });
      });
  }

  /*
   * POST a partial settings object merged onto the saved settings (the
   * server merges too). Resolves to the saved settings; an open, edited
   * Settings panel keeps its draft (plus the new values).
   */
  function saveSettingsPartial(partial) {
    var body = Object.assign({}, savedSettings, partial || {});
    return fetch("/api/settings", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    })
      .then(function (res) {
        if (!res.ok)
          return res
            .json()
            .catch(function () {
              return null;
            })
            .then(function (j) {
              var err = new Error(j && typeof j.error === "string" ? j.error : "HTTP " + res.status);
              err.status = res.status;
              err.code = j && typeof j.code === "string" ? j.code : ""; // "locked" → PIN prompt
              err.hint = j && typeof j.hint === "string" ? j.hint : "";
              throw err;
            });
        return res.json();
      })
      .then(function (saved) {
        var prev = settings;
        var dirty = !settingsEqual(settings, savedSettings);
        savedSettings = L.normalizeSettings(saved);
        if (dirty) {
          settings = L.normalizeSettings(Object.assign({}, settings, partial || {}));
          if (settingsOpen) fillForm(settings);
          setDirty(!settingsEqual(settings, savedSettings));
        } else {
          settings = Object.assign({}, savedSettings);
          if (settingsOpen) fillForm(settings);
          setDirty(false);
        }
        applyLive(prev);
        emit("settings", Object.assign({}, settings));
        return Object.assign({}, savedSettings);
      });
  }

  /* The server has no time zone yet: give it the browser's, once. */
  var tzSeeded = false;
  function seedTimeZone() {
    if (tzSeeded || savedSettings.timezone) return;
    var tz = browserTimeZone();
    if (!tz) return;
    tzSeeded = true;
    saveSettingsPartial({ timezone: tz }).catch(function () {
      tzSeeded = false; // try again on the next settings load
    });
  }

  // =================================================================== 10. status pills

  var lastStatusAt = 0;

  // id -> {state, key, vars}: pills re-render in a new UI language
  var pillState = {};

  function setPill(id, state, key, vars) {
    var el = $(id);
    if (!el) return;
    pillState[id] = { state: state, key: key, vars: vars || null };
    var tip = tf(key, vars);
    el.dataset.state = state;
    el.title = tip;
    el.setAttribute("aria-label", tip);
    el.dataset.tip = tip;
  }

  function renderPills() {
    Object.keys(pillState).forEach(function (id) {
      var p = pillState[id];
      setPill(id, p.state, p.key, p.vars);
    });
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
          setPill("pillOllama", "bad", "pillBrainBad", { err: o.error || t("pillBrainNoErr") });
        } else if (!hasModel) {
          setPill("pillOllama", "warn", "pillBrainWarn", { model: model });
        } else {
          setPill("pillOllama", "ok", "pillBrainOk", { model: model });
        }
        var piperOk = !!(statusInfo.piper && statusInfo.piper.ok);
        piperDown = !piperOk;
        if (settings.voiceMode === "browser") {
          setPill("pillVoice", synth ? "ok" : "bad", synth ? "pillVoiceBrowserOk" : "pillVoiceBrowserBad");
        } else if (piperOk) {
          setPill("pillVoice", "ok", "pillVoiceOk");
        } else {
          setPill("pillVoice", "warn", "pillVoiceWarn");
        }
        if (statusInfo.mistral && statusInfo.mistral.enabled) {
          setPill("pillSat", "ok", "pillSatOk");
        } else {
          setPill("pillSat", "off", "pillSatOff");
        }
        populatePiperVoices();
        if (settingsOpen) populateModels(settings.localModel, true);
        fillModelLists();
        preloadAck();
        emit("status", statusInfo);
      })
      .catch(function () {
        if (dom.statusDot) dom.statusDot.classList.remove("online");
        setPill("pillOllama", "bad", "serverUnreachable");
        setPill("pillVoice", "bad", "serverUnreachable");
        setPill("pillSat", "bad", "serverUnreachable");
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

  function updateChatToggleLabel() {
    if (!dom.chatToggle) return;
    var visible = !dom.body.classList.contains("chat-hidden");
    dom.chatToggle.setAttribute("aria-expanded", visible ? "true" : "false");
    var label = dom.chatToggle.querySelector(".chat-toggle-label");
    if (label) label.textContent = visible ? t("hideChat") : t("showChat");
  }

  function setChatVisible(visible) {
    dom.body.classList.toggle("chat-hidden", !visible);
    store.set(KEYS.chat, !!visible);
    updateChatToggleLabel();
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
    toast(t("eyeModeToast"), { key: "eyemode" });
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

  // =================================================================== 12. hub panel + settings sections

  var NOTICE_ICONS = {
    info: "ℹ️",
    reminder: "⏰",
    timer: "⏱️",
    meeting: "📅",
    calendar: "📅",
    memory: "🧠",
    list: "📝",
    lock: "🔒",
    home: "🏠",
    docs: "📄",
    link: "🔗",
    checkin: "🧍",
  };

  /* Small DOM builder: el("div", {class: "x", text: "…"}, [children]). */
  function el(tag, attrs, children) {
    var node = document.createElement(tag);
    Object.keys(attrs || {}).forEach(function (k) {
      var v = attrs[k];
      if (v == null || v === false) return;
      if (k === "class") node.className = v;
      else if (k === "text") node.textContent = v;
      else node.setAttribute(k, v === true ? "" : String(v));
    });
    (children || []).forEach(function (c) {
      if (c) node.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
    });
    return node;
  }

  /*
   * Re-label everything built with data-i18n (text), data-i18n-html (trusted
   * constant markup), data-i18n-ph (placeholder), data-i18n-title (title +
   * aria-label), data-i18n-aria (aria-label) and data-i18n-tip (title only).
   * Only app.js strings use these attributes; modules translate themselves.
   */
  function relabelUi() {
    var root = document.body;
    if (!root) return;
    var each = function (attr, fn) {
      Array.prototype.forEach.call(root.querySelectorAll("[" + attr + "]"), function (n) {
        fn(n, t(n.getAttribute(attr)));
      });
    };
    each("data-i18n", function (n, v) {
      if (n.textContent !== v) n.textContent = v;
    });
    each("data-i18n-html", function (n, v) {
      if (n.innerHTML !== v) n.innerHTML = v; // STRINGS constants only
    });
    each("data-i18n-ph", function (n, v) {
      n.setAttribute("placeholder", v);
    });
    each("data-i18n-title", function (n, v) {
      n.setAttribute("title", v);
      n.setAttribute("aria-label", v);
    });
    each("data-i18n-aria", function (n, v) {
      n.setAttribute("aria-label", v);
    });
    each("data-i18n-tip", function (n, v) {
      n.setAttribute("title", v);
    });
    var l = lang();
    Object.keys(sectionEls).forEach(function (id) {
      var s = sectionEls[id];
      if (s.title) s.title.textContent = labelFor(s.def.title, id, l);
    });
    if (hub.open) renderHubTabs();
    var tzBtn = $("tzBrowserBtn");
    if (tzBtn) tzBtn.textContent = tf("tzUseBrowser", { tz: browserTimeZone() || "–" });
    if (typeof updateTzHint === "function") updateTzHint();
    relabelDynamic();
  }

  /* Texts app.js sets from code: redo them in the new language. */
  function relabelDynamic() {
    updateMicButton();
    updateSendButton();
    updateChatToggleLabel();
    applyTouchTexts();
    renderPills();
    if (statusInfo) {
      populateModels(settings.localModel, settingsOpen);
    }
    fillBrowserVoiceSelect(settingsOpen ? null : settings.voice);
    renderSettingsNav();
    renderConfigPane();
    applyManaged();
    if (settingsSearch && settingsSearch.value) applySearch();
    if (!gen.active && conv.length) renderAll(true);
  }

  function labelFor(dict, fallback, l) {
    if (dict && typeof dict === "object") return String(dict[l] || dict.en || dict.de || fallback);
    return String(dict || fallback);
  }

  // --- settings sections (Hermes.registerSettingsSection)

  var sectionEls = {}; // id -> {def, root, title}

  /* The slot a registered section goes into (its category, else "More"). */
  function sectionSlot(id) {
    var c = catEl(L.settingsCategoryFor(id)) || catEl("more");
    return (c && c.querySelector(".cat-slot")) || dom.settingsSections;
  }

  function mountSections() {
    if (!dom.settingsSections && !settingsBody) return;
    var added = false;
    var l = lang();
    H.sections().forEach(function (def) {
      var cur = sectionEls[def.id];
      if (cur && cur.def === def) {
        sectionSlot(def.id).appendChild(cur.root); // keeps the sorted order
        return;
      }
      if (cur) cur.root.remove();
      var title = el("h3", { class: "group", text: labelFor(def.title, def.id, l) });
      var body = el("div", { class: "section-body" });
      var root = el("section", { class: "feature-section", "data-section": def.id, id: "section-" + def.id }, [title, body]);
      sectionSlot(def.id).appendChild(root);
      sectionEls[def.id] = { def: def, root: root, title: title };
      added = true;
      try {
        if (typeof def.mount === "function") def.mount(body);
      } catch (e) {
        if (window.console) console.error("[Hermes] settings section " + def.id + ":", e);
      }
    });
    if (added && settingsOpen) fillDataSettings(settings);
    renderSettingsNav();
    if (added) applyManaged();
  }

  function reducedMotion() {
    return !!(window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches);
  }

  /* Open the category a section lives in and bring the section into view. */
  function scrollToSection(id) {
    var s = id && sectionEls[id];
    var catId = s ? L.settingsCategoryFor(id) : catEl(id) ? id : "";
    if (!catId) return;
    if (settingsSearch && settingsSearch.value) {
      settingsSearch.value = "";
      applySearch();
    }
    showCategory(catId, { content: true, noFocus: true });
    if (!s) return;
    setTimeout(function () {
      try {
        s.root.scrollIntoView({ block: "start", behavior: reducedMotion() ? "auto" : "smooth" });
      } catch (e) {
        s.root.scrollIntoView();
      }
    }, 80);
  }

  /* One labelled row: <label><span data-i18n>…</span> control</label> + hint. */
  function settingRow(labelKey, control, hintKey, hintId) {
    var label = el("label", null, [el("span", { "data-i18n": labelKey, text: t(labelKey) }), control]);
    var nodes = [label];
    if (hintKey) nodes.push(el("p", { class: "hint", id: hintId || null, "data-i18n": hintKey, text: t(hintKey) }));
    return nodes;
  }

  function checkRow(key, labelKey) {
    return el("label", { class: "check" }, [
      el("input", { type: "checkbox", "data-setting": key }),
      " ",
      el("span", { "data-i18n": labelKey, text: t(labelKey) }),
    ]);
  }

  function textInput(key, attrs) {
    return el("input", Object.assign({ type: "text", "data-setting": key, spellcheck: "false", autocomplete: "off" }, attrs || {}));
  }

  var updateTzHint = null;

  /* Settings → "Ort & Zeit" / "Location & time" (SPEC 5.2, order 5). */
  function mountLocationSection(body) {
    var zones = [];
    try {
      if (typeof Intl.supportedValuesOf === "function") zones = Intl.supportedValuesOf("timeZone");
    } catch (e) {
      zones = [];
    }
    var list = el("datalist", { id: "tzList" });
    zones.forEach(function (z) {
      list.appendChild(el("option", { value: z }));
    });
    var tzInput = textInput("timezone", { maxlength: 64, list: "tzList", placeholder: "Europe/Berlin", id: "s_timezone" });
    var useBtn = el("button", { type: "button", class: "small", id: "tzBrowserBtn", text: tf("tzUseBrowser", { tz: browserTimeZone() || "–" }) });
    useBtn.addEventListener("click", function () {
      var tz = browserTimeZone();
      if (!tz) return;
      tzInput.value = tz;
      onFormChange();
    });
    updateTzHint = function () {
      var hint = $("tzHint");
      if (!hint) return;
      var v = tzInput.value.trim();
      var bad = v !== "" && !L.isValidTimeZone(v);
      hint.classList.toggle("warn", bad);
      hint.textContent = bad ? t("tzInvalid") : t("tzHint");
    };
    tzInput.addEventListener("input", updateTzHint);
    settingRow("tzLabel", tzInput, "tzHint", "tzHint").forEach(function (n) {
      body.appendChild(n);
    });
    body.appendChild(list);
    body.appendChild(el("div", { class: "row-actions" }, [useBtn]));
    settingRow("cityLabel", textInput("homeCity", { maxlength: 100, "data-i18n-ph": "cityPlaceholder", placeholder: t("cityPlaceholder") }), "cityHint").forEach(function (n) {
      body.appendChild(n);
    });
    body.appendChild(checkRow("briefingOnGreeting", "briefingOnGreetingLabel"));
    body.appendChild(checkRow("briefingNews", "briefingNewsLabel"));
  }

  /* Settings → "Lokale Modelle" / "Local models" (SPEC 5.2, order 30). */
  function mountModelsSection(body) {
    body.appendChild(el("datalist", { id: "hermesModelList" }));
    body.appendChild(checkRow("routerEnabled", "routerEnabledLabel"));
    body.appendChild(el("p", { class: "hint", "data-i18n": "routerHint", text: t("routerHint") }));
    var rows = [
      ["routerModelLabel", textInput("routerModel", { maxlength: 200, list: "hermesModelList", "data-i18n-ph": "sameAsMain", placeholder: t("sameAsMain") }), null],
      ["extractModelLabel", textInput("extractModel", { maxlength: 200, list: "hermesModelList", "data-i18n-ph": "sameAsRouter", placeholder: t("sameAsRouter") }), "extractHint"],
      ["embedModelLabel", textInput("embedModel", { maxlength: 200, list: "hermesModelList", placeholder: "embeddinggemma" }), "embedHint"],
    ];
    rows.forEach(function (r) {
      settingRow(r[0], r[1], r[2]).forEach(function (n) {
        body.appendChild(n);
      });
    });
    var ctx = el("input", { type: "number", "data-setting": "numCtx", min: 2048, max: 32768, step: 1024, inputmode: "numeric" });
    var keep = textInput("keepAlive", { maxlength: 16, placeholder: "30m" });
    body.appendChild(
      el("div", { class: "row" }, [
        el("label", null, [el("span", { "data-i18n": "numCtxLabel", text: t("numCtxLabel") }), ctx]),
        el("label", null, [el("span", { "data-i18n": "keepAliveLabel", text: t("keepAliveLabel") }), keep]),
      ])
    );
    body.appendChild(el("p", { class: "hint", "data-i18n": "numCtxHint", text: t("numCtxHint") }));
    body.appendChild(checkRow("fillerSpeech", "fillerLabel"));
    fillModelLists();
  }

  /* Installed Ollama models as suggestions for the model fields. */
  function fillModelLists() {
    var list = $("hermesModelList");
    if (!list) return;
    var models = statusInfo && statusInfo.ollama && Array.isArray(statusInfo.ollama.models) ? statusInfo.ollama.models : [];
    list.innerHTML = "";
    models.slice(0, 200).forEach(function (m) {
      if (typeof m === "string") list.appendChild(el("option", { value: m }));
    });
  }

  H.registerSettingsSection({ id: "location", order: 5, title: { en: "Location & time", de: "Ort & Zeit" }, mount: mountLocationSection });
  H.registerSettingsSection({ id: "models", order: 30, title: { en: "Local models", de: "Lokale Modelle" }, mount: mountModelsSection });

  // --- hub panel (Hermes.registerTab)

  var hubPanel = $("hubPanel");
  var hubBackdrop = $("hubBackdrop");
  var hubTabsEl = $("hubTabs");
  var hubBody = $("hubBody");
  var hubEmpty = $("hubEmpty");
  var hub = { open: false, current: "", panes: {}, returnFocus: null };

  function tabDef(id) {
    var list = H.tabs();
    for (var i = 0; i < list.length; i++) if (list[i].id === id) return list[i];
    return null;
  }

  function renderHubTabs() {
    if (!hubTabsEl) return;
    var l = lang();
    var tabs = H.tabs();
    hubTabsEl.innerHTML = "";
    tabs.forEach(function (def) {
      var b = el("button", {
        type: "button",
        class: "hub-tab",
        role: "tab",
        id: "hubtab-" + def.id,
        "aria-selected": def.id === hub.current ? "true" : "false",
        "aria-controls": "hubpane-" + def.id,
        tabindex: def.id === hub.current ? "0" : "-1",
      });
      // icons are inline SVG constants shipped by the modules themselves
      if (typeof def.icon === "string" && /^\s*<svg[\s>]/i.test(def.icon)) {
        var ic = el("span", { class: "hub-tab-icon", "aria-hidden": "true" });
        ic.innerHTML = def.icon;
        b.appendChild(ic);
      }
      b.appendChild(el("span", { class: "hub-tab-label", text: labelFor(def.label, def.id, l) }));
      b.addEventListener("click", function () {
        showHubTab(def.id);
      });
      hubTabsEl.appendChild(b);
    });
    if (hubEmpty) hubEmpty.hidden = tabs.length > 0;
  }

  function showHubTab(id) {
    var def = tabDef(id);
    if (!def || !hubBody) return;
    if (hub.current && hub.current !== id) {
      var old = tabDef(hub.current);
      if (hub.panes[hub.current]) hub.panes[hub.current].hidden = true;
      if (old && typeof old.hide === "function") {
        try {
          old.hide();
        } catch (e) {
          /* ignore */
        }
      }
    }
    hub.current = id;
    var pane = hub.panes[id];
    if (!pane) {
      pane = el("div", { class: "hub-pane", role: "tabpanel", id: "hubpane-" + id, "aria-labelledby": "hubtab-" + id });
      hubBody.appendChild(pane);
      hub.panes[id] = pane;
      try {
        if (typeof def.mount === "function") def.mount(pane); // lazily, on first show
      } catch (e) {
        if (window.console) console.error("[Hermes] hub tab " + id + ":", e);
      }
    }
    pane.hidden = false;
    try {
      store.set(KEYS.hubTab, id);
    } catch (e) {
      /* ignore */
    }
    renderHubTabs();
    if (typeof def.show === "function") {
      try {
        def.show();
      } catch (e) {
        /* ignore */
      }
    }
  }

  function openHub(tabId) {
    if (!hubPanel) return;
    if (settingsOpen) closeSettings();
    if (eyeMode.on) exitEyeMode();
    if (!hub.open) {
      hub.open = true;
      hub.returnFocus = document.activeElement;
      reveal(hubBackdrop);
      reveal(hubPanel);
      if (dom.hubBtn) dom.hubBtn.setAttribute("aria-expanded", "true");
    }
    var want = tabId && tabDef(tabId) ? tabId : hub.current && tabDef(hub.current) ? hub.current : store.get(KEYS.hubTab, "");
    if (!tabDef(want)) want = H.tabs().length ? H.tabs()[0].id : "";
    renderHubTabs();
    if (want) showHubTab(want);
    setTimeout(function () {
      var sel = hubTabsEl && hubTabsEl.querySelector("[aria-selected='true']");
      var target = sel || $("closeHubBtn");
      if (target && target.focus) target.focus({ preventScroll: true });
    }, 60);
  }

  function closeHub() {
    if (!hub.open) return;
    hub.open = false;
    conceal(hubBackdrop);
    conceal(hubPanel);
    if (dom.hubBtn) dom.hubBtn.setAttribute("aria-expanded", "false");
    var def = tabDef(hub.current);
    if (def && typeof def.hide === "function") {
      try {
        def.hide();
      } catch (e) {
        /* ignore */
      }
    }
    if (hub.returnFocus && hub.returnFocus.focus) hub.returnFocus.focus({ preventScroll: true });
  }

  function hubFocusables() {
    if (!hubPanel) return [];
    return Array.prototype.filter.call(hubPanel.querySelectorAll("button, select, input, textarea, [href], [tabindex]:not([tabindex='-1'])"), function (n) {
      return !n.disabled && n.offsetParent !== null;
    });
  }

  on(dom.hubBtn, "click", function () {
    if (hub.open) closeHub();
    else openHub();
  });
  on($("closeHubBtn"), "click", closeHub);
  on(hubBackdrop, "click", closeHub);
  on(hubPanel, "keydown", function (e) {
    if (e.key === "Tab") {
      var f = hubFocusables();
      if (!f.length) return;
      if (e.shiftKey && document.activeElement === f[0]) {
        e.preventDefault();
        f[f.length - 1].focus();
      } else if (!e.shiftKey && document.activeElement === f[f.length - 1]) {
        e.preventDefault();
        f[0].focus();
      }
    } else if ((e.key === "ArrowRight" || e.key === "ArrowLeft") && e.target && e.target.getAttribute && e.target.getAttribute("role") === "tab") {
      var tabs = H.tabs();
      var i = -1;
      for (var k = 0; k < tabs.length; k++) if (tabs[k].id === hub.current) i = k;
      if (i < 0 || !tabs.length) return;
      e.preventDefault();
      var next = tabs[(i + (e.key === "ArrowRight" ? 1 : tabs.length - 1)) % tabs.length];
      showHubTab(next.id);
      var b = $("hubtab-" + next.id);
      if (b) b.focus();
    }
  });
  H.on("hermes:tabs", function () {
    if (hub.open) renderHubTabs();
  });
  H.on("hermes:sections", mountSections);

  // --- personal lock: PIN prompt (POST /api/unlock)

  var unlockPromise = null;

  /* Ask for the household PIN; resolves true once the server unlocked. */
  function requestUnlock() {
    if (unlockPromise) return unlockPromise;
    var dlg = el("dialog", { class: "pin-dialog", "aria-labelledby": "pinTitle" });
    if (typeof dlg.showModal !== "function") return Promise.resolve(false);
    var input = el("input", { type: "password", inputmode: "numeric", autocomplete: "off", maxlength: 32, id: "pinInput", "aria-describedby": "pinText" });
    var err = el("p", { class: "hint warn", role: "alert" });
    var okBtn = el("button", { type: "submit", class: "primary", text: t("unlock") });
    var cancelBtn = el("button", { type: "button", class: "ghost", text: t("cancel") });
    var form = el("form", { method: "dialog" }, [
      el("h2", { id: "pinTitle", text: t("pinTitle") }),
      el("p", { id: "pinText", text: t("pinText") }),
      input,
      err,
      el("div", { class: "pin-actions" }, [cancelBtn, okBtn]),
    ]);
    dlg.appendChild(form);
    document.body.appendChild(dlg);
    unlockPromise = new Promise(function (resolve) {
      var done = false;
      var finish = function (ok) {
        if (done) return;
        done = true;
        unlockPromise = null;
        if (dlg.open) dlg.close();
        dlg.remove();
        resolve(ok);
      };
      cancelBtn.addEventListener("click", function () {
        finish(false);
      });
      dlg.addEventListener("close", function () {
        finish(false);
      });
      form.addEventListener("submit", function (e) {
        e.preventDefault();
        var pin = input.value.trim();
        if (!pin) return;
        okBtn.disabled = true;
        err.textContent = "";
        fetchJSON("/api/unlock", { method: "POST", body: { pin: pin } })
          .then(function () {
            finish(true);
          })
          .catch(function (e2) {
            okBtn.disabled = false;
            input.value = "";
            input.focus();
            if (e2.status === 429 || e2.code === "lockout") err.textContent = t("pinLocked");
            else if (e2.status === 401 || e2.status === 403 || e2.code === "pin") err.textContent = t("pinWrong");
            else err.textContent = tf("pinFailed", { msg: e2.message || "?" });
          });
      });
    });
    dlg.showModal();
    input.focus();
    return unlockPromise;
  }

  // --- small services for the modules (Hermes.api)

  /* JSON in and out; errors carry .status, .code and the server's message. */
  function fetchJSON(url, o) {
    o = o || {};
    var init = {
      method: o.method || (o.body !== undefined ? "POST" : "GET"),
      headers: { Accept: "application/json" },
      credentials: "same-origin",
      cache: "no-store",
    };
    if (o.signal) init.signal = o.signal;
    if (o.body !== undefined) {
      init.headers["Content-Type"] = "application/json";
      init.body = typeof o.body === "string" ? o.body : JSON.stringify(o.body);
    }
    return fetch(url, init).then(function (res) {
      return res.text().then(function (txt) {
        var data = null;
        try {
          data = txt ? JSON.parse(txt) : null;
        } catch (e) {
          data = null;
        }
        if (!res.ok) {
          var err = new Error(data && typeof data.error === "string" ? data.error : "HTTP " + res.status);
          err.status = res.status;
          err.code = data && typeof data.code === "string" ? data.code : "";
          // secrets.go and friends add a DE/EN hint matching the voice
          err.hint = data && typeof data.hint === "string" ? data.hint : "";
          throw err;
        }
        return data;
      });
    });
  }

  /* A local announcement in the chat: never sent to the server. */
  function addNotice(text, o) {
    o = o || {};
    var kind = typeof o.kind === "string" && /^[a-z][a-z0-9_-]{0,31}$/.test(o.kind) ? o.kind : "info";
    var m = addMessage({ role: "assistant", content: String(text == null ? "" : text).slice(0, 2000), notice: kind });
    if (Array.isArray(o.actions) && o.actions.length) m.actions = o.actions.slice(0, 4);
    if (o.ephemeral) m.ephemeral = true; // e.g. private reminder text: never stored
    fillMessageEl(m);
    saveConversation();
    return {
      id: m.id,
      update: function (newText, actions) {
        if (conv.indexOf(m) < 0) return;
        if (newText != null) m.content = String(newText).slice(0, 2000);
        if (Array.isArray(actions)) m.actions = actions.slice(0, 4);
        fillMessageEl(m);
        saveConversation();
      },
      remove: function () {
        removeMessage(m.id);
      },
    };
  }

  /* The short built-in reminder chime (Web Audio, no asset). */
  function chime() {
    var ctx = audio.resume();
    if (!ctx || !audio.master) return Promise.resolve(false);
    try {
      var t0 = ctx.currentTime + 0.02;
      [
        [880, 0],
        [1318.5, 0.16],
        [1760, 0.32],
      ].forEach(function (n) {
        var osc = ctx.createOscillator();
        var g = ctx.createGain();
        osc.type = "sine";
        osc.frequency.value = n[0];
        g.gain.setValueAtTime(0.0001, t0 + n[1]);
        g.gain.exponentialRampToValueAtTime(0.35, t0 + n[1] + 0.015);
        g.gain.exponentialRampToValueAtTime(0.0001, t0 + n[1] + 0.45);
        osc.connect(g);
        g.connect(audio.master); // volume applies, voice FX do not
        osc.start(t0 + n[1]);
        osc.stop(t0 + n[1] + 0.5);
        osc.onended = function () {
          try {
            g.disconnect();
          } catch (e) {
            /* ignore */
          }
        };
      });
    } catch (e) {
      return Promise.resolve(false);
    }
    return new Promise(function (resolve) {
      setTimeout(function () {
        resolve(true);
      }, 900);
    });
  }

  /* Ask the server to load the local models (SPEC 4.11); fire and forget. */
  var warmState = { at: 0, off: false };
  function warmModels() {
    if (warmState.off || now() - warmState.at < 30000) return;
    warmState.at = now();
    fetch("/api/warm", { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" })
      .then(function (res) {
        if (res.status === 404 || res.status === 405) warmState.off = true; // older server
      })
      .catch(function () {});
  }

  function setEnrolling(on) {
    on = !!on;
    if (on === enrolling) return;
    enrolling = on;
    if (on) {
      stopAll(true); // nothing talks over the recording
      clearTimeout(mic.wakeTimer);
      mic.pendingWakeAt = 0;
      utter.anchored = false;
    }
    updateEye();
  }

  function isEchoRisk() {
    // the enrolment wizard stopped HERMES first: only real playback counts
    if (enrolling) return audio.playing > 0 || speech.isAudible();
    return mic.guard || now() < mic.ignoreUntil || audio.playing > 0 || speech.isAudible();
  }

  function fillHermesApi() {
    var api = H.api;
    api.lang = lang;
    api.settings = function () {
      return Object.assign({}, settings);
    };
    api.saveSettings = saveSettingsPartial;
    api.status = function () {
      return statusInfo || null;
    };
    api.refreshStatus = function () {
      return refreshStatus().then(function () {
        return statusInfo || null;
      });
    };
    api.toast = function (msg, o) {
      o = o || {};
      // SPEC 5.2 names the duration `ms`; `timeout` is accepted as an alias
      var ms = o.ms !== undefined ? o.ms : o.timeout;
      return toast(String(msg == null ? "" : msg), { type: o.type, key: o.key, timeout: ms, action: o.action });
    };
    api.say = function (text, o) {
      o = o || {};
      if (!o.force && !settings.speak) return Promise.resolve();
      return speech.say(String(text == null ? "" : text));
    };
    api.chime = chime;
    api.setEnrolling = setEnrolling;
    api.armFollowUp = armFollowUp;
    api.isSpeaking = function () {
      return speech.isBusy();
    };
    api.isEchoRisk = isEchoRisk;
    api.flashEye = flashEye;
    api.addNotice = addNotice;
    api.send = send;
    api.openTab = openHub;
    api.openSettings = function (sectionId) {
      if (hub.open) closeHub();
      var cat = sectionId && sectionEls[sectionId] ? L.settingsCategoryFor(sectionId) : sectionId && catEl(sectionId) ? sectionId : undefined;
      if (!settingsOpen) openSettings(cat);
      if (sectionId) scrollToSection(sectionId);
    };
    api.fetchJSON = fetchJSON;
    api.clientId = clientId;
    api.gestureSeen = function () {
      return !!gestureSeen;
    };
    api.isSecure = function () {
      return !!window.isSecureContext;
    };
    api.isLocalhost = function () {
      // same rule as isLocalhostHost in hermes.js: the whole host, not a prefix
      return /^(?:localhost|127(?:\.\d{1,3}){3}|\[::1\])(?::\d+)?$/i.test(location.host);
    };
    // extras beyond SPEC 5.2 (used by people.js / memory.js for the lock)
    api.requestUnlock = requestUnlock;
    api.stop = function () {
      stopAll(true);
    };
    // stops speech only, at once (the enrolment wizard calls it before
    // recording); an answer still streaming keeps going but stays silent
    // while setEnrolling(true) is on
    api.stopSpeaking = function () {
      speech.stop();
      updateEye();
    };
  }

  // =================================================================== 13. keyboard, gestures, boot

  var gestureAnnounced = false;
  function unlockAudio() {
    gestureSeen = true;
    if (!gestureAnnounced) {
      gestureAnnounced = true;
      // inside the gesture: a module may start a capture context now
      emit("gesture");
    }
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
      toast(t("needsChrome"), { type: "warn", key: "ptt" });
      return;
    }
    unlockAudio();
    if (mic.wanted && !mic.rec && !mic.running && !mic.restartTimer && !mic.blocked) {
      // "standby": wanted, but recognition never started (it needs a user
      // gesture). The button looks idle, so pressing it starts listening.
      mic.ensure();
      toast(t("listeningToast"), { key: "mic", timeout: 1800 });
      return;
    }
    mic.setWanted(!mic.wanted);
    toast(mic.wanted ? t("listeningToast") : t("micOffToast"), { key: "mic", timeout: 1800 });
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
    return !!((lightbox && lightbox.open) || (shortcuts && shortcuts.open) || document.querySelector("dialog[open]"));
  }

  on(document, "keydown", function (e) {
    var mod = e.ctrlKey || e.metaKey;
    var typing = isTypingTarget(e.target);

    if (e.key === "Escape") {
      if (anyDialogOpen()) return; // native <dialog> closes itself
      if (overlay && !overlay.hidden) {
        closePictures();
      } else if (hub.open) {
        closeHub();
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
    var modal = settingsOpen || hub.open || anyDialogOpen() || (overlay && !overlay.hidden) || (onboard && !onboard.hidden);
    // G toggles the hub (and closes it again from inside)
    if ((e.key === "g" || e.key === "G") && !mod && !e.altKey && !typing && !e.repeat) {
      if (hub.open) {
        e.preventDefault();
        closeHub();
        return;
      }
      if (!modal) {
        e.preventDefault();
        openHub();
        return;
      }
    }
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

  /*
   * Texts that depend on the device: phones have no Space key (point at the
   * mic button), browsers without speech input get typing-only hints. Sets
   * the data-i18n keys so a language switch keeps the right variant.
   */
  function applyTouchTexts() {
    var touch = !!(window.matchMedia && (window.matchMedia("(max-width: 640px)").matches || window.matchMedia("(hover: none)").matches));
    var setKey = function (node, attr, key) {
      if (!node) return;
      node.setAttribute(attr, key);
      var v = t(key);
      if (attr === "data-i18n-ph") node.setAttribute("placeholder", v);
      else if (attr === "data-i18n-html") node.innerHTML = v; // STRINGS constants only
      else node.textContent = v;
    };
    setKey(dom.input, "data-i18n-ph", !mic.supported ? "inputPhPlain" : touch ? "inputPhTouch" : "inputPh");
    var emptySub = document.querySelector(".empty-sub");
    if (emptySub) {
      if (!mic.supported) {
        emptySub.removeAttribute("data-i18n-html");
        setKey(emptySub, "data-i18n", "emptySubNoMic");
      } else setKey(emptySub, "data-i18n-html", touch ? "emptySubTouch" : "emptySub");
    }
    var obPtt = $("obPtt");
    if (obPtt && touch) {
      setKey(obPtt.querySelector(".ob-key"), "data-i18n-html", "obPttKeyTouch");
      setKey(obPtt.querySelector("span:not(.ob-key)"), "data-i18n-html", "obPttTouch");
    }
    setKey($("onboardNote"), "data-i18n", mic.supported ? "obNote" : "obNoteNoMic");
  }

  function maybeOnboard() {
    if (!onboard || store.get(KEYS.onboard, false) || eyeMode.on) return;
    var note = $("onboardNote");
    if (note && !mic.supported) note.classList.add("warn");
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
    applyTouchTexts();
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
    fillHermesApi();
    mountSections();
    H.on("utterance:anchor", warmModels);
    fetchSettings().then(function () {
      refreshStatus();
      updateEye();
      warmModels();
    });
    on(document, "visibilitychange", function () {
      if (document.visibilityState === "visible") {
        if (now() - lastStatusAt > 5000) refreshStatus();
        if (mic.shouldRun() && !mic.rec && gestureSeen) mic.ensure();
        emit("visible");
      }
    });
  }

  boot();
  // feature modules may use Hermes.api from now on
  emit("ready");
  try {
    if (H.events && typeof H.events.start === "function") H.events.start();
  } catch (e) {
    /* no hub client: reminders etc. simply do not arrive */
  }
})();
