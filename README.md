# BT-7274 Chatbot

A local AI chatbot for your web browser. Runs in a Docker container, built in Go, with a customizable voice and an animated mechanical eye.

## Features

- **Chat with BT-7274** (Vanguard-class Titan from Titanfall 2) via a web frontend.
- **Animated visor eye** in the center of the page that reacts: standby, listening (while you type), thinking (while streaming an answer), speaking (while BT talks), and confused/error (when something fails).
- **Real BT-7274 voice** — the officially-styled Piper voice model from [DJMalachite/PiperVoiceModels](https://github.com/DJMalachite/PiperVoiceModels/tree/main/Titanfall2/BT7274) runs fully locally inside the container (no API key needed for speech). Files are vendored in `voices/`.
- **Local knowledge base**: greetings and Titanfall trivia answered instantly, offline.
- **Fallback to free.ai**: when the bot doesn't know the answer, it asks `https://api.free.ai/v1/chat/` (OpenAI-compatible) and streams the reply.
- **Adjustable voice** (in the ⚙ Settings panel):
  - *BT-7274 (local Piper)*: the real Titanfall 2 voice, runs on-device.
  - *Browser voice (OS)*: any installed voice with pitch / rate / volume sliders.
  - *free.ai TTS (Kokoro)*: cloud voices such as `am_onyx`, `af_heart`.
- **Customizable settings** (model, system prompt, temperature, max tokens, voice) saved to disk and applied on the fly.

> The BT-7274 Piper model is fan-created (not official Respawn audio), distributed under the original repo's terms. If you prefer, the `voices/` dir can be swapped for any other Piper `.onnx` voice.

## Prerequisites

- Docker + Docker Compose
- A free API key from [free.ai](https://free.ai/signup/) — sign up, then generate a key (`sk-free-...`) under Account → API. Free tier gives 30K tokens/day.

## Run

```bash
cp .env.example .env    # then edit .env and put in your FREEAI_API_KEY
docker compose up -d --build
```

Open http://localhost:8080

If you don't use an `.env` file, pass the key inline:

```bash
FREEAI_API_KEY=sk-free-xxx docker compose up -d --build
```

## Local development (no Docker)

```bash
FREEAI_API_KEY=sk-free-xxx go run .
# open http://localhost:8080
```

## API key config file vs .env

There's no config file — the key must be present at container start. Get one at:

1. https://free.ai/signup/ → confirm email
2. https://free.ai/account/?tab=api → Generate key
3. Put it in `.env`

## How the "doesn't know" fallback works

The Go backend (`main.go`) first checks a small built-in BT knowledge base (ordered phrases, protocol answers, Titanfall lore). Only if there's no local match does it call free.ai's chat API with your saved system prompt and stream the response.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/api/chat` | Chat; streams SSE. Local KB answer or free.ai fallback. |
| POST | `/api/tts` | Text-to-speech. `engine` = `piper` (local BT-7274) or `server` (free.ai Kokoro). |
| GET/POST | `/api/settings` | Read / persist settings (JSON). |
| GET | `/` | Web UI (chat + mechanical eye). |

## Voice settings quick guide

- **BT-7274 (Piper)** — default. Runs fully locally; the container bundles the Piper binary + `voices/BT7274.onnx`. No API key needed to hear BT speak.
- **Browser engine** — pick a voice, drop pitch to ~0.5 and rate to ~-0.3 for a closer "BT" monotone.
- **Server engine (Kokoro)** — set a server voice id like `am_onyx` (deep male) or `af_heart`. Requires `FREEAI_API_KEY`.

## Eye states

The visor in the center of the page reflects BT's state:
- **STANDBY** — idle, slow drift
- **AWAITING INPUT** — eye tracks left/right while you type
- **PROCESSING** — rapid scanning while the answer streams
- **SPEAKING** — pulsing while the voice plays
- **UNCERTAIN / ERROR** — jittering when a request fails

## Repo layout

```
main.go          Go server: chat proxy, local KB, Piper + Kokoro TTS, settings
static/          Web UI (HTML/CSS/JS) with the animated eye
voices/          BT-7274 Piper voice model (BT7274.onnx + config)
Dockerfile       Multi-stage: Go build + Debian runtime with bundled Piper
docker-compose.yml
```