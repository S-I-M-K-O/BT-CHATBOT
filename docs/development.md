# Development

[← README](../README.md)

- [Repo layout](#repo-layout)
- [How a message flows](#how-a-message-flows)
- [Testing](#testing)
- [CI](#ci)

## Repo layout

Standard Go layout: the entry point in `cmd/`, all server code in one internal package, the browser app under `web/`. Go keeps each `_test.go` next to the file it tests, so the Go tests live in `internal/hermes/` too.

```
cmd/hermes/main.go       entry point: calls hermes.Run()
internal/hermes/         all server code (one Go package) + its tests
  main.go                startup, prompts and phrases, .env loader
  chat.go                /api/chat: request checks, speaker, history filters, the pipeline
  sse.go                 the /api/chat event stream
  skills.go              extension points features register with (fast paths, tools,
                         context, access classes, pending questions, undo, routes, jobs)
  local.go               knowledge base: greetings and small talk
  routing.go             where a message goes: live info, image search, the local model
  router.go              local tool router;  context.go: the private local context
  ollama.go              Ollama client (chat, embeddings, keep_alive/num_ctx, warm-up)
  embed.go               embeddings for memory search + the keyword fallback
  settings.go            settings: defaults, validation, /api/settings (+ managed keys)
  config.go              hermes.conf: parser, export/template, /api/config*, file watcher
  events.go              event hub and /api/events;  store.go, state.go: data files
  server.go              routes, middleware, tokens, PIN unlock, security headers
  people.go              people, voiceprints, voice ID;  people_stop.go: "ich bin müde"
                         is not an introduction;  memory.go: memories and vectors
  realtime.go            weather / time / news from Open-Meteo, Tagesschau, Google News
  fetch.go               plain HTTP helpers with timeouts and size caps
  images.go              image search (Openverse), SSRF-safe downloads, data/gen/ budget
  reminders.go           reminders, timers, repeating series: store, scheduler, /api/reminders
  timeparse.go           German/English time parser ("in 20 Minuten", "every Monday at 8")
  lists.go               shopping/to-do lists: store, voice phrases, /api/lists
  briefing.go            morning briefing: greeting detection, sections
  calendar.go, gcal.go   calendar voice + /api/calendar/*;  Google OAuth + API client
  checkin.go             meeting check-ins, check-in pause, /api/checkins/*
  homeassistant.go       Home Assistant: pinned client, Assist + whitelisted services,
                         refusals, device questions, /api/home/*
  docs.go                documents: store, chunking, search, folder scan, /api/docs*
  readlink.go            read a link: /api/link/fetch (SSRF-safe dial), the summary turn
  secrets.go             data/secrets.json and /api/integrations* (Google client, HA, PIN)
  tts.go                 Piper TTS and the voice catalogue
  *_test.go              unit and end-to-end tests (fake Ollama, Google, HA, Open-Meteo,
                         Tagesschau, Google News, Openverse)
web/static/              the web UI, no build step:
  index.html, style.css, app.js     page, chat, mic/wake word, Settings window, i18n
  lib.js                            pure helpers (sentences, SSE, markdown, wake word,
                                    language detection, settings helpers) — unit-tested
  hermes.js                         module glue: event hub client, tabs, language
  cards.js                          chat cards under the replies
  eye.js + eye.css                  the spring-animated optic
  people.js, voiceid*.js, capture-worklet.js   people, enrolment, in-browser voice ID
  memory.js, reminders.js, lists.js (+ lists.html), checkins.js, integrations.js
  docs.js, links.js                 Documents tab/settings, drag-and-drop upload; link reading
  css/                              one stylesheet per feature (settings, hub, cards, …)
  vendor/                           fetched assets (scripts/fetch-assets.sh), not committed
web/tests/               node tests for the web UI (+ fixtures/)
scripts/fetch-assets.sh  downloads and sha256-checks the browser assets
voices/                  one folder per Piper voice + voices.json (language per voice)
docs/                    this documentation, and training a German BT-7274 voice
data/                    runtime state (settings, hermes.conf, people, memories, …) — git-ignored, never commit
backups/                 task backup archives — git-ignored
Dockerfile               asset stage + Go build + Debian runtime with sha256-pinned Piper
docker-compose.yml       loopback-only layout (default)
docker-compose.lan.yml   LAN override: tokens, port on every interface
Taskfile.yml             everyday commands (task rebuild, task logs, task check, …)
THIRD_PARTY_NOTICES.md   licences of the fetched assets and the online data services
.github/workflows/       CI (gofmt, go vet, go test -race, node tests, JS/shell syntax)
```

## How a message flows

`internal/hermes/chat.go` runs every message through one pipeline; the first step that answers wins:

0. **Pending answer.** If HERMES just asked something on this device ("Soll ich den Termin eintragen?", "Wann soll ich dich erinnern?", "Für welchen Ort?"), a yes/no or the missing value answers it. Anything else silently drops the question.
1. **Fast paths.** Deterministic commands, no model involved ("rückgängig" / "undo", and the features that register their own phrases).
2. **Knowledge base.** Greetings and small talk, answered instantly.
3. **Tool router.** Only when the sentence contains a command phrase of some tool: the local model, with a neutral prompt and temperature 0, picks a tool. A tool that changes something only runs straight away when you phrased it as a command ("Trag … ein"); a statement becomes a question back to you.
4. **"Draw me a picture".** There is no image generator, so a fixed reply points to image search instead of letting the model pretend.
5. **Live info.** Weather, time and news, answered locally from Open-Meteo, the clock, the Tagesschau and Google News with fixed sentences (`realtime.go`). Only the city or the topic leaves the machine.
6. **Image search** on Openverse.
7. **Persona chat** on your local Ollama model, plus the private local context (time, who is speaking, memories).

Every reply ends with a small `meta` event telling the browser how it was produced (`src`), for whom (`sp`) and whether it may contain private data; the server uses these tags to decide what history may go where (untagged history counts as private). See [API → Chat events](api.md#chat-events).

Every Ollama call sends the same `keep_alive` and `num_ctx` (Settings → Models), so the model stays loaded; the page asks the server to warm the models (`POST /api/warm`) on load and on the wake word. `HERMES_DEBUG_TIMING=1` logs per-turn latency.

Housekeeping: images in `data/gen/` self-clean after 24 hours, every data file is written atomically (temp file + rename), external downloads are capped at 25 MB, and static assets use conditional caching (304s).

## Testing

```bash
gofmt -l cmd internal            # should print nothing
go vet ./...
go test -race -count=1 ./...     # backend
node --test web/tests/           # frontend logic (Node 18+, no npm install)
for f in web/static/*.js; do node --check "$f"; done
sh -n scripts/fetch-assets.sh
```

or simply `task check`.

**Go tests** cover the knowledge base, routing (including the many false-positive guards for live-info questions), time parsing, reminders, lists, people/voice ID, memory, calendar, check-ins, and:

- **Live info:** weather, time and news in German and English against fake Open-Meteo, Tagesschau and Google News servers — cards, failures, caching, the city question, follow-ups, date-line cases and the size/timeout caps. A test-only `init()` points every service URL at a closed local port, so a test that forgets a fake fails instead of reaching the network.
- **Privacy:** only the city or topic reaches the fake services, no private-history marker appears, and no model is called on a live-info turn.
- **Home Assistant** (a fake HA): Assist, fallback services, refusals with zero requests, permissions, token pinning.
- **Documents** (chunking, search, the API caps, folder scan) and **read a link** (private addresses refused, redirects, charsets).
- **End-to-end runs** in English and German, the access and token rules, and **`hermes.conf`** (parser edge cases with line numbers, refused secrets, precedence over `settings.json`, export round-trip, the three routes and the file watcher).

**JS tests** cover sentence splitting, the SSE parser, the Markdown renderer (including a pile of XSS payloads), wake-word and stop matching, the eye's spring physics, the settings helpers (interface language, categories, search, managed keys), the chat cards (every kind, text-only rendering) and the feature modules. A guard test makes sure no built-in line calls you "Pilot".

## CI

`.github/workflows/` runs on pushes to `main` and on pull requests: `gofmt`, `go vet`, `go test -race`, `go build`, the node tests, `node --check` on every `web/static/*.js` and `sh -n scripts/fetch-assets.sh`. `task check` runs the same checks except the build.
