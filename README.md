# HERMES — Voice Assistant

> **HERMES** = *Holistic Emotive Reasoning & Multifunction Execution System* — a Vanguard-class assistant.

HERMES is a voice assistant that runs on your own machine: a Go backend (standard library only), a plain-JavaScript web UI with no build step and an animated mechanical eye in the middle of the page. He thinks with your local [Ollama](https://ollama.com), speaks with a local Piper voice, and remembers people, reminders, lists and your calendar. No cloud language model and no API key are needed.

## Features

- **Wake word & push-to-talk.** Say "Hermes" or hold Space. Speech recognition runs through Google or on the device. → [Voice & speech](docs/voice.md)
- **Piper voices**, fully local: BT-7274, Scorch, PDA and JARVIS (German). The voice decides the language he listens and answers in. → [Voice & speech](docs/voice.md)
- **Weather, time and news** are answered locally from Open-Meteo, the Tagesschau and Google News, with short fixed sentences in German or English. → [Live info](docs/live-info.md)
- **Image search** on Openverse (Wikimedia Commons and other open collections). → [Live info](docs/live-info.md#image-search)
- **Recognises people by voice.** The voiceprint is computed in the browser. → [People & memory](docs/people-and-memory.md)
- **Memory** per person or for the household, searched with a local embedding model. → [People & memory](docs/people-and-memory.md#memory)
- **Reminders, timers and repeating reminders** by voice. They repeat until you acknowledge them. → [Reminders & lists](docs/reminders-and-lists.md)
- **Lists** (shopping, to-do, your own), with a phone page for the family. → [Reminders & lists](docs/reminders-and-lists.md#lists)
- **Morning briefing, Google Calendar and check-ins.** → [Briefing, calendar & check-ins](docs/briefing-calendar-checkins.md)
- **Smart Home** through your own Home Assistant. Doors, locks and alarms are always refused. → [Smart Home](docs/smart-home.md)
- **Your documents and links.** Ask about PDFs and notes, or have an article summarised. Only the local model sees them. → [Documents & links](docs/documents-and-links.md)
- **Privacy PIN** that locks personal data behind a household PIN. → [People & memory](docs/people-and-memory.md#privacy-pin)
- **Chat cards, eye mode, keyboard shortcuts**, and a UI that works on phones. → [The interface](docs/interface.md)
- **Settings window, hermes.conf and .env**, with German or English for the interface. → [Configuration](docs/configuration.md)

## Quick start

You need a local Ollama with the chat model and the embedding model:

```bash
ollama pull qwen3.5:latest     # chat (Settings → Models picks from what you have)
ollama pull embeddinggemma     # memory search; without it HERMES uses keyword search
```

**Docker (recommended):**

```bash
mkdir -p data                  # so Docker doesn't create it as root
docker compose up -d --build   # later, after code changes: task rebuild
```

Then open http://localhost:8080. On Linux, Ollama has to listen beyond `127.0.0.1` first, and the container's uid has to match your user. [Setup](docs/setup.md#docker) covers both.

**Without Docker** (Go 1.22+, `piper` on your `PATH` for the voice):

```bash
sh scripts/fetch-assets.sh     # once: browser assets for voice ID, PDFs, read-a-link
go run ./cmd/hermes            # open http://localhost:8080
```

Speech recognition needs **Chrome or Edge**. To use HERMES from other devices you need a token; see [Reaching HERMES from other devices](docs/setup.md#reaching-hermes-from-other-devices).

## Privacy at a glance

Chat, speech output, voice ID, memories, people, reminders, lists and documents all stay on your machine. Only the local Ollama model answers questions, and it never sends anything out.

| Goes to | When | What is sent |
| --- | --- | --- |
| Google speech service (via Chrome) | Only with *Use Google speech recognition* on (default) | Your speech audio. Turn it off for on-device recognition. |
| Open-Meteo | Weather and time-zone questions, the briefing | Only the city name |
| Tagesschau | German news, the briefing's headlines | No parameters, just the request |
| Google News (RSS) | English news, "news about X" | The language and, for a topic, the topic words |
| Openverse (image search) | "Find a picture of …" | The search words |
| Google Calendar | Only once you connect a calendar | Calendar reads and the events you add |
| The linked web site | When you paste a URL or say "read the link" | A plain GET for that page (your IP, no cookies) |
| Your Home Assistant (in your LAN) | Device commands and questions | The sentence or a whitelisted service call, with your HA token |

History, names, memories, voiceprints, documents and calendar entries are never sent to these services. Details are in [Security & privacy](docs/security.md).

## How a question is answered

Each message goes through one pipeline, and the first step that can answer wins:

1. a pending question ("Für welchen Ort?") or a fixed command ("undo"),
2. small talk from a built-in list,
3. the local tool router (reminders, lists, calendar, smart home, …),
4. weather, time and news, fetched directly and answered with fixed sentences,
5. image search,
6. the local Ollama model with your private context.

No step sends your history, names or memories to the internet. More in [Development](docs/development.md#how-a-message-flows).

## Documentation

| Page | Contents |
| --- | --- |
| [Setup](docs/setup.md) | Docker layouts, Linux Ollama, LAN access and tokens, `go run`, assets, models, Taskfile |
| [Configuration](docs/configuration.md) | Settings window, interface language, `hermes.conf`, `.env`, what wins over what |
| [Voice & speech](docs/voice.md) | Wake word, push-to-talk, speech recognition, voices, voice effects |
| [The interface](docs/interface.md) | Eye, eye mode, status pills, chat, chat cards, keyboard shortcuts |
| [Live info](docs/live-info.md) | Weather, time, news and image search |
| [People & memory](docs/people-and-memory.md) | Voice ID, memory, privacy PIN |
| [Reminders & lists](docs/reminders-and-lists.md) | Reminders, timers, repeating reminders, lists, phone page |
| [Briefing, calendar & check-ins](docs/briefing-calendar-checkins.md) | Morning briefing, Google Calendar setup, check-ins |
| [Smart Home](docs/smart-home.md) | Home Assistant commands, refusals, permissions, setup |
| [Documents & links](docs/documents-and-links.md) | Your documents, the watched folder, reading a link |
| [Security & privacy](docs/security.md) | What is built in, what you must configure, what leaves the machine |
| [API](docs/api.md) | Endpoints, chat events, error codes |
| [Development](docs/development.md) | Repo layout, message pipeline, tests, CI |
| [Training a German BT-7274 voice](docs/training-german-bt-voice.md) | Fine-tuning your own Piper voice |

## Testing

```bash
task check    # gofmt, go vet, go test -race, node tests, JS and shell syntax (same as CI)
```

The individual commands are listed in [Development](docs/development.md#testing).

## License & notices

The browser assets that HERMES downloads (ONNX Runtime Web, the CAM++ speaker model, PDF.js, Readability.js) and the online data services it uses are listed with their licenses in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). Game audio used to train voices is copyrighted by Respawn / EA; the [voice-training guide](docs/training-german-bt-voice.md) is for personal, non-commercial use.
