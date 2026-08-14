# HERMES — Voice Assistant

> **HERMES** = *Holistic Emotive Reasoning & Multifunction Execution System* — a Vanguard-class assistant.

This is my voice assistant, HERMES. He's a Pilot's kit through and through: Go backend in a Docker container, an animated mechanical eye watching you from the middle of the page, and a Piper voice that lives entirely on your machine. He answers fast, runs offline, and never phones home. No API keys required for any of the basics.

## What he does

You can type to him or just talk. The mic button (the little HERMES visor icon, bottom-left of the chat) is on by default, and it's the *always-on* kind — he ignores you completely until you say **"Hermes"**. Then it's over to you. Say the thing right away ("Hermes what time is it") or say just his name and wait for the eye to flip to *"YES?"* before you speak.

The eye is basically his mood ring: a slow idle drift when he's got nothing to do, scanning while he thinks, pulsing while he talks, and a nervous jitter whenever something breaks.

He's local-first. Regular chat runs through your own Ollama instance (default model `qwen3.5:latest`) — none of that goes anywhere. The voice is Piper, which runs inside the container; the models are bundled in `voices/`. For the small talk — greetings and the like — he doesn't even bother the model, just answers from a short built-in list so he's never slow to say hi.

Optional extras, in case you want them:

- **Live data.** Drop a `MISTRAL_API_KEY` in a local `.env` and he stops guessing about the weather (Open-Meteo), the current time, or the news (Google News RSS).
- **Pictures.** "Find a picture of…" pulls up to three real photos from Openverse (Creative Commons) — no key needed for that. With a Mistral key, "draw…" or "generate an image of…" will actually make one.
- **Voice choice.** The default is HERMES's own Piper voice, but you can switch to any browser/OS voice and fiddle with pitch, rate and volume. Everything else — model, system prompt, temperature, max tokens — is editable in the ⚙ Settings panel, saved to disk, applied on the fly.

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

One note: the server binds `0.0.0.0` by default, which means anything on your network can reach it — and it's holding your Mistral key. To keep it local-only, set `ADDR=127.0.0.1` (in your environment or `.env`).

## The wake-word flow

Speech recognition works in Chrome/Edge; the mic button hides itself in other browsers. Click it, say **"Hermes"**, and the eye answers *"YES?"*. Then ask away — he'll talk back. He pauses his own listening while he speaks so he doesn't hear himself, then picks right back up.

## How a message flows

`main.go` checks the built-in knowledge base first (the greetings and such). No match? Then it passes the question to your local Ollama with the saved system prompt and streams the reply. If Mistral is enabled and the question smells like weather/time/news or image generation, it's routed over there instead.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/api/chat` | Chat, streaming SSE. Local KB → Mistral (real-time/images) → Ollama. |
| POST | `/api/tts` | Text-to-speech via local Piper. |
| GET/POST | `/api/settings` | Read and persist settings (JSON). |
| GET | `/api/health` | Liveness probe (what the Docker healthcheck hits). |
| GET | `/` | The web UI. |
| GET | `/gen/…` | Serves generated images from `data/gen/`. |

## Voice settings, in one breath

- **HERMES (Piper)** — the default, fully local. The container ships the Piper binary and `voices/BT7274.onnx`.
- **Browser** — pick any OS voice. For the classic robotic monotone, drop pitch to ~0.5 and rate to ~-0.3.

## Eye states

- **READY** — idle, slow drift
- **AWAITING CALL** — listening for "Hermes"
- **PROCESSING** — rapid scanning while the answer streams
- **SPEAKING** — pulsing while the voice plays
- **UNCERTAIN / ERROR** — jittering when a request fails

## Repo layout

```
main.go          server: chat proxy, local KB, Piper TTS, settings, .env loader
main_test.go     unit tests for the intent router, subject parsing, and local KB
mistral.go       Mistral: real-time tools (weather/time/news) + image generation
static/          the web UI (HTML/CSS/JS), eye, and wake-word mic
voices/          Piper voice models (BT7274.onnx + config)
Dockerfile       multi-stage Go build + Debian runtime with bundled Piper
docker-compose.yml
```

## Testing & housekeeping

```bash
go test ./...
```

There are unit tests for the parts that can't be eyeballed: the knowledge base, the search-vs-generate image routing, subject parsing, and the WMO weather-code mapping. Run `go vet ./...` before committing.

A few things I tidied up along the way: images in `data/gen/` self-clean after 24 hours, settings are written atomically (temp file + rename, so a crash can't corrupt `settings.json`), external downloads are capped at 25 MB each, and static assets get conditional caching so the browser does 304s instead of re-downloading everything.