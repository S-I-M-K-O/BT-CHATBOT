# HERMES — Voice Assistant

> **HERMES** = *Holistic Emotive Reasoning & Multifunction Execution System* — a Vanguard-class assistant.

This is my voice assistant, HERMES. A Go backend in a Docker container, an animated mechanical eye watching you from the middle of the page, and a Piper voice that lives entirely on your machine. He answers fast, runs offline, and never phones home. No API keys required for any of the basics.

## What he does

You can type to him or just talk. The mic button (the little HERMES visor icon, bottom-left) is on by default, and it's the *always-on* kind — he ignores you completely until you say **"Hermes"**. Then it's over to you. Say the thing right away ("Hermes, what time is it?" works, and so does "What time is it, Hermes?") or say just his name and wait for the eye to flip to *"YES?"* — a little countdown ring shows how long he'll wait (8 seconds) before he goes back to ignoring you (the window stays open while you're still talking). Don't feel like saying his name? **Hold Space** (or long-press the mic button on a phone) and just talk — push-to-talk skips the wake word. And Chrome mishears "Hermes" a lot, so "Hermès", "her mess", "hermis" and friends all count too.

While you talk, a little chip under the eye shows what he actually heard, so you know whether it was you or the speech recogniser that got it wrong. While he talks, the line he's currently saying is shown as a subtitle — handy when the chat is hidden. Sending a message (typed or a suggestion) doesn't pop the chat open; it stays the way you left it.

The eye is basically his mood ring, and it's modelled on BT-7274's optic from Titanfall 2: armored olive-gunmetal housing, a notched bezel that slowly turns, a glowing cyan lens. Everything moves on springs now instead of keyframes, so state changes glide: a slow idle drift (with the occasional blink) when he's got nothing to do, following your mouse while he listens, scanning while he thinks, pulsing *with his actual voice* while he talks, and a nervous red jitter whenever something breaks.

Interrupt him whenever: **Esc**, the **Stop** button (the Send button turns into it while he's busy), or just say **"Hermes, stop"**.

He's local-first. Regular chat runs through your own Ollama instance (default model `qwen3.5:latest`) — none of that goes anywhere. The voice is Piper, which runs inside the container; the models are bundled in `voices/`. For the small talk — greetings and the like — he doesn't even bother the model, just answers from a short built-in list so he's never slow to say hi.

Optional extras, in case you want them:

- **Live data.** Drop a `MISTRAL_API_KEY` in a local `.env` and he stops guessing about the weather (Open-Meteo), the current time, or the news (Google News RSS).
- **Pictures.** "Find a picture of…" pulls up to three real photos from Openverse (Creative Commons) — no key needed for that. With a Mistral key, "draw…" or "generate an image of…" will actually make one.
- **Voice choice.** The default is HERMES's own Piper voice, but you can switch to any browser/OS voice and fiddle with pitch, rate and volume. Everything else — model (picked from what your Ollama actually has installed), system prompt, temperature, max tokens — is editable in the ⚙ Settings panel. Changes preview live (eye, voice); hit **Save** to keep them, or close the panel and they're rolled back (with an Undo, in case that was a mistake).

If you skip the Mistral key, live questions and image generation just fall back to the local Ollama model. (Image search keeps working regardless.)

## Getting it running

You'll need Docker (with Compose) and a local [Ollama](https://ollama.com) with a model pulled:

```bash
ollama pull qwen3.5:latest
```

Then:

```bash
docker compose up -d --build
```

and open http://localhost:8080.

The container reaches your host Ollama via `host.docker.internal` (that's wired up in `docker-compose.yml`). If you'd rather run the Go server directly on the host, skip Docker:

```bash
go run .
# open http://localhost:8080
```

One note: the server binds `127.0.0.1` by default, so it's local-only out of the box. If you want to expose it to your network (the Docker Compose setup does this so the mapped port works), set `ADDR=0.0.0.0` — just be aware it's holding your Mistral key.

## The wake-word flow

Speech recognition works in Chrome/Edge; the mic button hides itself in other browsers (typing always works). **Brave** exposes the API but has no speech service behind it, so HERMES tells you to switch to Chrome instead of retrying forever. Click it, say **"Hermes"**, and the eye answers *"YES?"* — the "Yes?" audio is cached per voice, so the answer is instant. Then ask away — he'll talk back. While he speaks he only listens for "Hermes, stop", so he never answers himself; about 300 ms after he finishes he's back to normal listening.

Chrome normally sends audio to Google's speech service. If that's unreachable (offline, VPN, firewall, ad blocker), HERMES checks for Chrome's **on-device recognition** (recent Chrome), downloads the language pack once and switches over — voice input then works offline. If the download needs a click, press `M`.

If the mic gets blocked, unplugged or no speech service is reachable at all, you get a toast telling you what happened instead of a silently dead button, and he backs off instead of hammering the recogniser. A missing mic or a dropped connection is treated as temporary: he keeps retrying every 15 seconds and picks up as soon as it's back, and your mic setting is never switched off for you. Only a blocked permission turns the mic off.

Browsers only allow speech recognition after you've interacted with the page, so on a fresh load the mic button shows a dashed *standby* outline: any click or key press (including the mic button or `M`) starts listening.

## The rest of the UI

- **Status pills** in the top bar: **Brain** (Ollama + is your model actually pulled?), **Voice** (Piper installed?) and **Satellite** (Mistral key set?). Hover or click one for what's wrong and how to fix it. They refresh every 30 seconds and whenever you come back to the tab.
- **Chat** renders Markdown (safely — bold, lists, code blocks, links), streams with a caret, and only auto-scrolls if you're already at the bottom; otherwise a "↓ New messages" button shows up. Every reply has **Copy**, **Speak again** and, when something failed, **Retry**. Hover for timestamps.
- The conversation survives a reload (last 100 messages, stored in your browser). **New chat** clears it with an Undo toast instead of a nag dialog. The download icon **exports the chat as Markdown**.
- **Suggestions** on an empty chat to get going — weather, a joke, the time somewhere (in German when JARVIS is the voice).
- **Pictures** open in an overlay; click one for the full-size lightbox with a download link.
- Works on a phone: compact top bar, eye scales down, full-screen settings, safe-area aware. Respects *reduce motion*.
- **Eye designs** (Settings → Appearance): **BT-7274** (the default), **Cyclops** (amber single-lens sentinel), **Iris** (violet mechanical aperture) and **Cortana** (holographic blue-violet AI). Saved settings that point at a removed design fall back to BT-7274.
- **Eye mode**: the frame icon in the top bar (or `E`) hides everything except the eye and goes fullscreen — handy on a second screen. Wake word, push-to-talk and speech keep working. `Esc`, `E` or the ✕ bring the page back; double-click toggles fullscreen. Open `http://localhost:8080/?view=eye` to start in eye mode (windowed). Don't keep a second HERMES tab open next to it, or both will answer.
- Drag the bar between eye and chat to resize the split; the "Show/Hide chat" button in the middle collapses the chat and gives the eye the whole screen. The input box stays put either way.

## Keyboard shortcuts

| Key | Does |
| --- | --- |
| hold `Space` | Push-to-talk (no wake word) |
| `M` | Toggle the always-on mic (starts it when it's still on standby) |
| `/` | Focus the message box |
| `Enter` / `Shift+Enter` | Send / new line |
| `↑` (empty box) | Previous message |
| `Esc` | Stop talking / generating, close panels |
| `Ctrl/⌘+K` | New chat |
| `Ctrl/⌘+,` | Settings (`Ctrl/⌘+Enter` saves) |
| `E` | Eye mode (only the eye, fullscreen) |
| `?` | Shortcut cheat sheet |

## How a message flows

`main.go` checks the built-in knowledge base first (the greetings and such). No match? Then it passes the question to your local Ollama with the saved system prompt and streams the reply. If Mistral is enabled and the question smells like weather/time/news or image generation, it's routed over there instead.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/api/chat` | Chat, streaming SSE. Local KB → Mistral (real-time/images) → Ollama. |
| POST | `/api/tts` | Text-to-speech via local Piper (`{text, voice?, speed?}` → WAV). |
| GET/POST | `/api/settings` | Read and persist settings (JSON). |
| GET | `/api/status` | Ollama / Piper / Mistral health + installed models and voices (feeds the pills). |
| GET | `/api/health` | Liveness probe (what the Docker healthcheck hits). |
| GET | `/` | The web UI. |
| GET | `/gen/…` | Serves generated images from `data/gen/`. |

## Voice settings, in one breath

- **HERMES (Piper)** — the default, fully local. The container ships the Piper binary and the voice models in `voices/`. The UI lists every model folder in `voices/voices.json` (BT7274, PDA, ScorchAI, JarvisDE). JarvisDE is the German voice — when it's selected, HERMES greets, answers the wake word ("Ja?"), reads the weather/news, and searches/generates images in German.
- **Browser** — pick any OS voice (or leave it on *Automatic*). For the classic robotic monotone, drop pitch to ~0.5 and rate to ~0.8. If Piper isn't installed, HERMES falls back to this automatically and tells you so.
- **Voice speed** — 0.5× to 2× for Piper.
- **Voice effect** — applied in the browser (Web Audio) to the Piper voice, so there's nothing to install:
  - **Titan** (default): the BT-7274 cockpit sound — trimmed lows and highs, a bit of presence around 2.5 kHz, compression and a very short metallic slap like it's coming from inside a hull. Still perfectly intelligible.
  - **Radio**: comms channel — 300–3400 Hz, a touch of grit, squashed hard.
  - **None**: dry.
- **Speak while streaming** (on by default) — he starts talking after the first sentence instead of waiting for the whole answer. Sentences are synthesised one ahead while the current one plays, so there's no gap. Code blocks are skipped; nobody wants to hear a for-loop read aloud.

## Eye states

- **READY** — idle, slow drift, the odd blink
- **AWAITING CALL** — listening for "Hermes" (follows your mouse)
- **YES?** — heard his name, waiting for your command (countdown ring)
- **GO AHEAD** — push-to-talk is held
- **PROCESSING** — rapid scanning while the answer streams
- **SPEAKING** — the light pulses with his voice
- **UNCERTAIN / ERROR** — red tint and a jitter when a request fails, dimmed when the mic is blocked

## Repo layout

```
main.go          server: chat proxy, local KB, Piper TTS, settings, .env loader
main_test.go     unit tests for the intent router, subject parsing, and local KB
mistral.go       Mistral: real-time tools (weather/time/news) + image generation
static/          the web UI: index.html, style.css, app.js (UI, voice, mic),
                 lib.js (pure helpers: sentences, SSE, markdown, wake word),
                 eye.js + eye.css (the spring-animated optic)
tests/js/       node tests for lib.js and the eye engine
voices/          one folder per Piper voice (BT7274/, PDA/, ScorchAI/, JarvisDE/)
voices/voices.json  the voice catalog the UI lists
data/            runtime state (settings.json, generated images) — not committed
Dockerfile       multi-stage Go build + Debian runtime with bundled Piper
docker-compose.yml
.github/workflows/  CI (go vet + go test + build) on push/PR
```

## Testing & housekeeping

```bash
go test ./...          # backend
node --test tests/js/  # frontend logic (Node 18+, no npm install needed)
```

There are unit tests for the parts that can't be eyeballed: the knowledge base, the search-vs-generate image routing, subject parsing, and the WMO weather-code mapping on the Go side; sentence splitting (abbreviations, decimals, German), the SSE parser, the Markdown renderer (including a pile of XSS payloads), wake-word / stop-command matching (including the push-to-talk and call-mode decisions and the recogniser back-off), speech normalisation for the browser voice, chat export and the eye's spring physics on the JS side. A guard test also makes sure no built-in line calls you "Pilot". Run `go vet ./...` before committing.

A few things I tidied up along the way: images in `data/gen/` self-clean after 24 hours, settings are written atomically (temp file + rename, so a crash can't corrupt `settings.json`), external downloads are capped at 25 MB each, and static assets get conditional caching so the browser does 304s instead of re-downloading everything.