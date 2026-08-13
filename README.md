# HERMES — Voice Assistant

A fully local, voice-activated AI chatbot for your web browser. Runs in a Docker container, built in Go, with a customizable voice and an animated mechanical eye. No API keys required anywhere.

## Features

- **Chat with HERMES** via a web frontend.
- **Always-on microphone**: click 🎤 to enable continuous listening. HERMES ignores everything until you say **"Hermes"** — then it waits for your question or statement (or you can say "Hermes <question>" in one go). Toggle off in Settings if you prefer.
- **Animated visor eye** in the center of the page that reacts: idle, awaiting call, thinking (while streaming an answer), speaking (while HERMES talks), and confused/error (when something fails).
- **Local LLM (Ollama)** — chat runs through a local Ollama instance (default model `qwen3.5:latest`). Nothing leaves your machine.
- **Local Piper voice** — a Piper TTS model runs fully locally inside the container. Files are vendored in `voices/`.
- **Real-time data via Mistral (optional)** — set `MISTRAL_API_KEY` and questions about the **weather** (Open-Meteo), **time/date**, and the **news** (Google News feeds) are answered live via Mistral function calling.
- **Images — search or generate**:
  - *Search the internet* for real photos (free, no key — Openverse/CC): "find a picture of …", "show me a photo of …".
  - *Generate* images with Mistral (needs `MISTRAL_API_KEY`): "draw …", "generate an image of …".
- **Local knowledge base**: greetings and common phrases answered instantly, offline.
- **Adjustable voice** (in the ⚙ Settings panel):
  - *HERMES (local Piper)*: the default model voice, runs on-device.
  - *Browser voice (OS)*: any installed voice with pitch / rate / volume sliders.
- **Customizable settings** (model, system prompt, temperature, max tokens, voice) saved to disk and applied on the fly.

## Prerequisites

- Docker + Docker Compose
- [Ollama](https://ollama.com) running on the host, with a model pulled, e.g.:
  ```bash
  ollama pull qwen3.5:latest
  ```

## Run

```bash
docker compose up -d --build
```

Open http://localhost:8080

The container reaches the host Ollama via `host.docker.internal` (set in `docker-compose.yml`). To run the Go server directly on the host instead:

```bash
go run .
# open http://localhost:8080
```

## Real-time data & images (Mistral)

Set `MISTRAL_API_KEY` (in a local `.env`, which is gitignored) to enable:

- **Weather / time / news** — HERMES detects these intents and uses Mistral's
  function calling with live tools (Open-Meteo for weather, the server clock
  for time, Google News RSS for headlines).
- **Image generation** — "draw …", "generate an image of …" produces an image
  via Mistral's image tool and shows it inline in the chat.

**Image search needs no key.** Ask to *find a picture / photo of …* and HERMES
pulls real photos from Openverse (Creative Commons) and shows up to three inline.

Without a key, real-time questions and generation fall back to the local Ollama
model as before; image search still works. The Mistral model is configurable via
`MISTRAL_MODEL` (default `mistral-medium-latest`).

## Wake-word voice input

- Click the 🎤 button (Chrome/Edge support speech recognition; the button is hidden otherwise).
- Say **"Hermes"** — the eye will show "YES, PILOT?" and await your question.
- Speak your question or statement; HERMES answers aloud.
- You can also say "Hermes what time is it" as one phrase.
- HERMES pauses listening while speaking so it doesn't hear itself, then resumes.

## How the local lookup works

The Go backend (`main.go`) first checks a small built-in knowledge base (greetings, and so on). Only if there's no local match does it call the local Ollama model with your saved system prompt and stream the response.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/api/chat` | Chat; streams SSE. Local KB, Mistral (real-time/images), or Ollama fallback. |
| POST | `/api/tts` | Text-to-speech (local Piper). |
| GET/POST | `/api/settings` | Read / persist settings (JSON). |
| GET | `/` | Web UI (chat + mechanical eye). |
| GET | `/gen/…` | Serves generated images (written to `data/gen/`). |

## Voice settings quick guide

- **HERMES (Piper)** — default. Runs fully locally; the container bundles the Piper binary + `voices/BT7274.onnx`.
- **Browser engine** — pick a voice, drop pitch to ~0.5 and rate to ~-0.3 for a more robotic monotone.

## Eye states

The visor in the center of the page reflects HERMES' state:
- **READY** — idle, slow drift
- **AWAITING CALL** — listening for "Hermes"
- **PROCESSING** — rapid scanning while the answer streams
- **SPEAKING** — pulsing while the voice plays
- **UNCERTAIN / ERROR** — jittering when a request fails

## Repo layout

```
main.go          Go server: chat proxy, local KB, Piper TTS, settings, .env loader
mistral.go       Mistral integration: real-time tools (weather/time/news) + image generation
static/          Web UI (HTML/CSS/JS) with the animated eye + wake-word mic
voices/          Piper voice model (BT7274.onnx + config)
Dockerfile       Multi-stage: Go build + Debian runtime with bundled Piper
docker-compose.yml
```