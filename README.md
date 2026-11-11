# HERMES — Voice Assistant

> **HERMES** = *Holistic Emotive Reasoning & Multifunction Execution System* — a Vanguard-class assistant.

This is my voice assistant, HERMES. A Go backend (standard library only) in a Docker container, a plain-JavaScript web UI with no build step, an animated mechanical eye watching you from the middle of the page, and a Piper voice that lives entirely on your machine. He thinks with your own Ollama, speaks with Piper, remembers who you are and what you told him, keeps your reminders, lists and calendar, and needs no API key for any of that.

## Contents

- [Features at a glance](#features-at-a-glance)
- [What stays local, what goes out](#what-stays-local-what-goes-out)
- [Getting it running](#getting-it-running) · [Everyday commands (Taskfile)](#everyday-commands-taskfile) · [Security](#security)
- [Configuration: .env, hermes.conf and the Settings window](#configuration-env-hermesconf-and-the-settings-window)
- [Talking to him](#talking-to-him) · [The UI](#the-ui) · [Keyboard shortcuts](#keyboard-shortcuts)
- [People & memory](#people--memory) · [Reminders & timers](#reminders--timers) · [Lists](#lists) · [Morning briefing](#morning-briefing) · [Calendar](#calendar-google) · [Check-ins](#check-ins) · [Smart Home](#smart-home-home-assistant) · [Documents](#documents) · [Read a link](#read-a-link) · [Privacy PIN](#privacy-pin)
- [Voices](#voices) · [How a message flows](#how-a-message-flows) · [Endpoints](#endpoints) · [Repo layout](#repo-layout) · [Testing](#testing)

## Features at a glance

- **Wake word & push-to-talk.** Say **"Hermes"** (he also accepts "Hermès", "her mess", "hermis" — Chrome mishears him a lot), or **hold Space** / long-press the mic and just talk. "Hermes, stop" or `Esc` interrupts him.
- **Speech recognition: Google or on-device.** One switch (Settings → Voice & speech → *Use Google speech recognition*). On: Chrome sends your audio to Google's speech service. Off: recognition runs on this device only (recent Chrome; the language pack downloads once). If Google is unreachable, HERMES falls back to on-device recognition by itself.
- **Piper voices**, fully local: BT-7274 (default), Scorch, PDA and JARVIS (German). The voice's language decides what language he listens and answers in. Browser/OS voices work too.
- **Recognises people by voice.** Each household member records three short sentences (enrolment); after that HERMES knows who is speaking. The voiceprint is computed in the browser — the audio never leaves it. Unknown voices are guests.
- **Memory**, per person or for the household: "Merk dir, dass ich keinen Koriander mag", or picked up from conversation. Searched with a local embedding model (`embeddinggemma`), keyword search without it.
- **Reminders & timers** by voice, spoken when due and repeated every minute until you acknowledge them, including **repeating reminders** ("jeden Montag um 8", "every weekday"). Only one tab announces — the one you used last.
- **Lists** (shopping, to-do, your own), by voice, in the Lists tab, and on a phone page for the family.
- **Morning briefing** on the first "Guten Morgen" of the day: date, weather, calendar, reminders, lists, optional headlines.
- **Google Calendar**: ask what's on, add events by voice, meeting check-ins before an event starts.
- **Check-ins**: meeting heads-ups and an optional "time to stand up" nudge.
- **Privacy PIN** that locks personal data (memories, people, calendar, exports) behind a household PIN.
- **Interface language** German or English, independent of the voice.
- **Chat cards**: replies about reminders, lists, the briefing, the calendar, devices, documents, links and memory come with a compact card under the bubble — tick list items, mark a reminder done or snooze it right there.
- **Large Settings window** with categories, search and live preview.
- **hermes.conf** — manage any setting from a text file; it wins over the window and reloads within seconds.
- Optional **Mistral** key for live weather/time/news and image generation; **image search** on Openverse needs no key.
- **Smart Home** through your own Home Assistant: lights, switches, heating, blinds and scenes by voice, and questions like "Ist das Küchenlicht an?". Doors, locks, gates and alarm systems are always refused.
- **Your documents**: upload PDFs, Markdown or text files (or drop them on the chat) and ask about them. Answered only by the local model.
- **Read a link**: paste a URL and he summarises the article out loud, with the local model.

## What stays local, what goes out

Chat, speech output, voice ID, memories, people, reminders and lists all run on your machine. The things that do talk to the internet:

| What | Goes to | When | What is sent |
| --- | --- | --- | --- |
| Speech recognition | Google (via Chrome) | Only with *Use Google speech recognition* on (default) | Your speech audio. Turn the switch off for on-device recognition. |
| Live answers & image generation | Mistral | Only with `MISTRAL_API_KEY` set | Persona prompt and your current question (plus the previous Mistral-answered exchange). Never memories, voiceprints, names, calendar entries or documents. |
| Weather | Open-Meteo | Weather questions, briefing | The city name |
| News | Google News RSS | News questions, briefing with headlines on | The language |
| Image search | Openverse / Wikimedia | "Find a picture of …" | The search words |
| Calendar | Google Calendar API | Only once you connect a calendar | Calendar reads and the events you add |
| Smart Home | Your Home Assistant (in your LAN) | Device commands and questions, the status check | The sentence (for HA's Assist) or a whitelisted service call, with your HA token. Nothing goes to the internet unless your HA URL is on the internet. |
| Read a link | The web site you linked | When you paste a URL or say "lies den Link vor" | A plain GET request for that page (your IP address and a browser-like `Accept` header; no cookies). The article text then goes only to the local model, never to Mistral. |

Without a Mistral key, live questions and image generation fall back to the local Ollama model (image search keeps working). Your documents, link articles and Home Assistant states never go to Mistral.

## Getting it running

You need a local [Ollama](https://ollama.com) with the chat model and the embedding model pulled:

```bash
ollama pull qwen3.5:latest     # chat (Settings → Models picks from what you have)
ollama pull embeddinggemma     # memory search; without it HERMES uses keyword search
```

### Docker (recommended)

```bash
mkdir -p data   # so Docker doesn't create it as root
docker compose up -d --build
```

and open http://localhost:8080.

The image build downloads the browser assets (onnxruntime-web and the speaker model for voice ID, pdf.js, Readability.js — about 44 MB, each checked against a pinned sha256). Building offline? `docker compose build --build-arg FETCH_ASSETS=0`; everything except voice ID still works.

The container runs as an unprivileged user (uid 1000) and keeps all its state in `./data`, which is mounted into the container — that is also where `hermes.conf` goes. Compose runs it as `${HERMES_UID:-1000}:${HERMES_GID:-1000}`; if your host user isn't uid 1000, put `HERMES_UID` / `HERMES_GID` in `.env` or start it with `HERMES_UID=$(id -u) HERMES_GID=$(id -g) docker compose up -d --build` (or `sudo chown -R 1000:1000 data`), otherwise saving fails with *permission denied*. (`UID` itself is read-only in bash, which is why the variables have their own names.) The container's root filesystem is read-only, it drops all Linux capabilities and cannot gain new privileges; it only writes to `/app/data` and `/tmp`.

The container reaches your host Ollama via `host.docker.internal` (wired up in `docker-compose.yml`). On macOS and Windows (Docker Desktop) that just works. **On Linux** a stock Ollama only listens on `127.0.0.1`, which the container can't reach, so the Brain pill stays red. Make Ollama listen on the Docker bridge:

```bash
sudo systemctl edit ollama
# add:
#   [Service]
#   Environment="OLLAMA_HOST=0.0.0.0"
sudo systemctl restart ollama
```

`0.0.0.0` exposes Ollama on every interface, so firewall port 11434 from your LAN (or use the bridge IP instead, e.g. `OLLAMA_HOST=172.17.0.1`). The alternative is host networking: add `network_mode: host` to the service, drop the `ports:` block, and set `ADDR=127.0.0.1` and `DOCKER_OLLAMA_BASE=http://127.0.0.1:11434` (also remove `HERMES_BEHIND_LOOPBACK`, which isn't needed then). `DOCKER_OLLAMA_BASE` in `.env` points the container somewhere else entirely; it's separate from `OLLAMA_BASE` so a `localhost` value meant for `go run` doesn't end up inside the container.

### Without Docker

```bash
sh scripts/fetch-assets.sh   # once: browser assets into web/static/vendor/ (voice ID etc.)
go run ./cmd/hermes
# open http://localhost:8080
```

You need Go 1.22+ and, for the HERMES voice, the `piper` binary on your `PATH` (or `PIPER_BIN`). Without Piper he falls back to the browser voice and says so. Without the fetched assets he still runs; only voice ID, PDF text extraction and the article extractor for read-a-link are unavailable (`task assets` fetches them).

Speech recognition needs **Chrome or Edge**. Other browsers can type; Brave exposes the API without a speech service, so HERMES tells you to switch.

### Reaching HERMES from other devices

The server binds `127.0.0.1` by default, and the Compose setup publishes the port on `127.0.0.1` only, so it's local-only out of the box. Anything that can reach the API can rewrite the system prompt and spend your Mistral key, so network access needs a token. There are two Docker layouts:

1. **Loopback only** (`docker compose up -d --build`, the default `docker-compose.yml`): port `127.0.0.1:8080`, `HERMES_BEHIND_LOOPBACK=1`. Everything works on this machine, nothing on the LAN.
2. **LAN + tokens** (`docker compose -f docker-compose.yml -f docker-compose.lan.yml up -d --build`, needs Compose 2.24+ for `!override`): port `8080` on every interface, `HERMES_BEHIND_LOOPBACK=0`, and `HERMES_TOKEN` is required (Compose refuses to start without it). Optional: `HERMES_LISTS_TOKEN`, `ALLOWED_HOSTS`.

Then:

1. Set `HERMES_TOKEN` in `.env` (e.g. `openssl rand -hex 32`). For `go run`, also set `ADDR=0.0.0.0`. Add any hostnames you use to `ALLOWED_HOSTS` (e.g. `hermes.lan`).
2. On each device, open `http://<host>:8080/?token=<token>` once. HERMES stores it in an HttpOnly cookie and drops it from the address bar. Scripts send `Authorization: Bearer <token>` instead. On the Mac itself, open `http://localhost:8080/?token=<token>` once in the LAN layout: admin actions (calendar connect, integrations, creating `hermes.conf`) need "this machine", which in Docker means a `localhost` address **plus** the master token.
3. **Phones for the shopping list:** set `HERMES_LISTS_TOKEN` and give family members `http://<host>:8080/lists.html?token=<lists token>`. That token opens only the list page, the list API and list change events; the chat, settings, memories and everything else answer 401. Give partners and kids the lists token, never the master token.

Requests to `localhost`, `127.0.0.1` or `::1` from the machine itself never need the token. Without `HERMES_TOKEN`, every other request is refused (403) and the server logs an error at startup if it's configured to be reachable anyway.

HERMES refuses to start when `HERMES_BEHIND_LOOPBACK=1` is combined with `HERMES_LISTS_TOKEN` or a non-loopback `ALLOWED_HOSTS` entry: with that flag any LAN client could pass as local by sending `Host: localhost`. Use `docker-compose.lan.yml` instead. If a request with a LAN `Host` ever arrives while the flag is on, the log shows an ERROR once (the port is published wider than it should be). Even with the flag, a `localhost` request only counts as local when it comes from the container's default gateway — where Docker's port proxy connects from. Other containers on the same network, or a LAN host that routes to the Docker bridge, need the token (the log shows a WARNING once). For an unusual setup, `HERMES_TRUSTED_PEER` lists extra peer IPs.

## Everyday commands (Taskfile)

The repo has a [Taskfile](https://taskfile.dev) for the usual chores. Install it once with `brew install go-task` (Linux: see taskfile.dev), then `task` lists everything. The most useful ones:

| Command | Does |
| --- | --- |
| `task rebuild` | Rebuild the image and restart the container after code changes, then wait until it is healthy |
| `task rebuild:lan` | The same in the LAN layout (phones; needs `HERMES_TOKEN` in `.env`) |
| `task logs` | Follow the container logs (health checks hidden; Ctrl+C to stop) |
| `task run` | Run HERMES directly with Go on http://localhost:8080 (needs Piper on `PATH` for the voice) |
| `task assets` | Download the voice-ID model, ONNX runtime, pdf.js and Readability (sha256-checked, skipped when present) |
| `task models` | Pull the Ollama models HERMES uses (chat + embeddings) |
| `task test` | All tests: Go with the race detector, plus the frontend tests |
| `task check` | Everything CI checks: gofmt, go vet, all tests, JS and shell syntax |
| `task backup` | Archive `data/` into `backups/` (folder 0700, archive 0600 — it holds voiceprints and tokens) |
| `task config:export` | Save the current settings as a fully commented `data/hermes.conf.export` |
| `task token` | Print a random token for `HERMES_TOKEN` / `HERMES_LISTS_TOKEN` |

Also there: `up`, `down`, `restart`, `ps`, `status`, `open`, `shell`, `build`, `fmt`, `rebuild:clean`. The `status` and `config*` tasks call `http://localhost:8080` without a token, so in the LAN layout they need the loopback port (or call the API yourself with `Authorization: Bearer`).

## Security

HERMES is built for a home: everything personal stays on your machine, and the network boundary is a token. What protects you, and what you have to set up yourself:

**Built in**

- **Local by default.** The server binds `127.0.0.1`, Docker publishes the port on loopback only. Requests from other machines need `HERMES_TOKEN` (cookie or `Authorization: Bearer`), and without a token they are refused. Host and origin checks block DNS rebinding and cross-site requests; admin actions (integrations, calendar connect, creating `hermes.conf`) only work on the machine itself.
- **Scoped phone token.** `HERMES_LISTS_TOKEN` opens only the list page, the list API and list events. A phone whose token was revoked drops its saved copy of the lists.
- **Personal data stays local and labelled.** Memories, calendar, reminders, documents, link articles and device states only go to the local model, inside blocks marked as data. Mistral gets only the current question. After a reply built from a web page, a document, the briefing or the calendar, the next answer gets no personal context, so instructions hidden in that text never sit next to your memories. Links written by the model always show their real host.
- **Actions only from what you said.** Tools run from your own sentence, never from a page, a document or a calendar title; a statement only gets an offer ("Soll ich …?"). List names (which the lists token can create) are never shown to the tool router.
- **Who is speaking decides what he may do** (access classes): guests never hear or change personal data, sticky or claimed speakers can't write, "undo" needs the same rights as the action it undoes. Device control defaults to recognised people only (`homeControl = known`).
- **Household PIN** (optional, see *Privacy PIN*) locks memories, people, documents, calendar events, a person's reminders, the chat export and every setting that would loosen privacy in the panels and the API, and in chat for a speaker picked on the chip or claimed ("ich bin Anna"). A speaker **recognised by voice** still uses their own memories, reminders and calendar by voice.
- **Smart Home safety.** Doors, locks, gates, garage doors and alarms are refused before anything is sent; a sentence that also names a device HERMES doesn't control is refused as a whole. The HA token is pinned to its address and never leaves the server.
- **Read-a-link and image downloads** refuse local and private addresses (also after redirects), only ports 80/443.
- **Files.** Everything in `data/` is written atomically with mode 0600 (directories 0700, `data/gen/` included — an older 0755 folder is tightened at start), including generated images and `hermes.conf`. `data/` and `backups/` are in `.gitignore` — they hold voiceprints, memories and tokens; never commit or share them. The container runs unprivileged and read-only.

**What you must configure**

1. **`HERMES_TOKEN`** (long and random: `task token`) before you open HERMES to the LAN; `HERMES_LISTS_TOKEN` for family phones instead of the master token.
2. **The PIN and the personal lock** if more than one person uses HERMES — voice ID alone is not a lock.
3. **A dedicated, non-admin Home Assistant user** for the HA token, with only lights, switches, heating and blinds exposed to Assist.
4. **`HERMES_BLOCK_CIDRS`** in Docker (your LAN prefixes, e.g. `192.168.1.0/24`): inside the container HERMES can't see which networks your Mac is on, so read-a-link needs them to keep away from your LAN.
5. **No other web apps on the same host name or IP** (cookies don't care about ports), and **TLS** (e.g. Tailscale Serve) when you reach HERMES from outside or over untrusted Wi-Fi — on plain HTTP the token can be sniffed.
6. **Google speech recognition** is on by default and sends your audio to Google; switch it off for on-device recognition.

What it does **not** protect against: someone who holds the master token, or who sits at an unlocked machine. A four-digit PIN allows five tries a minute; use a longer PIN if that matters to you.

## Configuration: .env, hermes.conf and the Settings window

There are three places, each for its own kind of thing:

| Where | What belongs there | Who wins |
| --- | --- | --- |
| **`.env`** (or real environment variables) | Server things and secrets: port/address, tokens, `MISTRAL_API_KEY`, Ollama URL, data paths, Google client, debug switches. See `.env.example`. Needs a restart. | Credentials from `.env` win over the ones saved in the window (shown there as "from .env"). |
| **`data/hermes.conf`** | Any normal setting (voice, language, models, quiet hours, …) as plain text. No secrets. Picked up within ~3 seconds, no restart. | A key set here wins over the Settings window and is **locked** there (🔒 *in hermes.conf*). |
| **Settings window** (stored in `data/settings.json`) | Everything else, clicked together in the UI. Credentials and the PIN go to `data/secrets.json` (0600). | Applies to every key the file doesn't set. |

### The Settings window

Click the gear (or `Ctrl/⌘+,`). On a desktop it's a large window with categories on the left — **General, Voice & speech, People & voice ID, Memory, Reminders & check-ins, Calendar, Smart Home, Documents, Location & time, Privacy, Models, Appearance, Config file** and **More** — and a search box at the top that matches any word, ignores case and accents (`ß` finds `ss`) and shows the number of hits per category. On a phone it's full screen: the category list first, a back arrow to return.

Changes preview live (eye, voice, language). **Save** (or `Ctrl/⌘+Enter`) keeps them; **Cancel**, `Esc` or the ✕ rolls them back, with an Undo toast in case that was a mistake. **Reset to defaults** keeps whatever `hermes.conf` sets.

### Interface language

Settings → General → **Interface language**: *Automatic* (follow the voice, the default), *Deutsch* or *English*. With *Deutsch* or *English* the screen texts switch **and the voice lists only offer voices of that language**; when you pick the language in the Settings window and the current voice is of the other language, HERMES switches to the first matching one (e.g. English → BT-7274) and says so. What he listens for and how he answers follow the voice, so picking a language there effectively sets his language. A `uiLang` set in `hermes.conf` or through the API does **not** change the voice: a voice of the other language stays selected (and listed), so the screen is in one language and the replies in the other until you pick a matching voice. With *Automatic* every voice is listed and the interface follows the voice. In `hermes.conf` it's `uiLang = auto | de | en`.

### hermes.conf

A plain text file next to `settings.json`: `data/hermes.conf` by default, `/app/data/hermes.conf` in Docker (that's your mounted `./data`), or wherever `HERMES_CONFIG` points. The easiest start is Settings → **Config file**:

- **Export hermes.conf** downloads a complete file with all your current settings, grouped in sections, with a German and an English comment per key.
- **Create hermes.conf (template)** (only on this machine, only while no file exists) writes the same file into `data/` with every value commented out — nothing is locked until you remove the `#` in front of a line.

The same page shows the path, when the file last changed, which keys it sets, and any problems with their line numbers.

```ini
# data/hermes.conf
[Oberfläche / Interface]
uiLang = de                 # interface in German, whatever the voice

[Sprachausgabe / Voice output]
piperVoice = JarvisDE
voiceSpeed = 1.1
speak = true

[Ruhezeiten / Quiet hours]
quietStart = "22:30"
quietEnd = "06:30"

[Modelle / Models]
localModel = qwen3.5:latest
systemPrompt = "You are HERMES.\nKeep answers short."
```

The rules:

- One `key = value` per line. Keys are the setting names from the export (`speak`, `uiLang`, `piperVoice`, `quietStart`, …), case doesn't matter.
- `#` starts a comment (at the start of a line or after a space); lines starting with `;` are comments too. Blank lines are ignored, and `[section]` headers are only for order.
- Values: `true`/`false` (also `yes/no`, `on/off`, `ja/nein`, `an/aus`, `1/0`), numbers (`0.5` or `0,5`), text — in `"double quotes"` only when it needs leading/trailing spaces or a `#`. `\n` in `systemPrompt` is a line break.
- A wrong line never stops HERMES: unknown keys, invalid values (out of range, unknown voice, bad time) and duplicates are listed under Settings → Config file and ignored (for duplicates the last one wins).
- **No secrets.** Tokens, passwords, Google/Home Assistant credentials and the PIN are refused; they belong in `.env` or the window. `personalLock` can only be switched in the window (it needs the PIN).
- The server checks the file every 3 seconds. When it changes, open pages reload their settings and show "Settings updated from hermes.conf." Delete a line and the setting is editable in the window again (it keeps its last value).
- Saving from the window never changes a key the file sets; that key keeps the file's value.

## Talking to him

The mic button (the little HERMES visor icon, bottom-left) is on by default, and it's the *always-on* kind — he ignores you completely until you say **"Hermes"**. Say the thing right away ("Hermes, what time is it?" works, and so does "What time is it, Hermes?") or say just his name and wait for the eye to flip to *"YES?"* — a countdown ring shows how long he'll wait (8 seconds) before he goes back to ignoring you (the window stays open while you're still talking). The "Yes?" audio is cached per voice, so the answer is instant. Don't feel like saying his name? **Hold Space** (or long-press the mic button on a phone) — push-to-talk skips the wake word.

While he speaks he only listens for "Hermes, stop", so he never answers himself; about 300 ms after he finishes he's back to normal listening. Interrupt him any time with **Esc**, the **Stop** button (the Send button turns into it while he's busy), or **"Hermes, stop"**.

Browsers only allow speech recognition after you've interacted with the page, so on a fresh load the mic button shows a dashed *standby* outline: any click or key press (including the mic button or `M`) starts listening. If the mic gets blocked, unplugged or no speech service is reachable at all, you get a toast telling you what happened instead of a silently dead button, and he backs off instead of hammering the recogniser. A missing mic or a dropped connection is treated as temporary: he retries every 15 seconds and picks up as soon as it's back. Only a blocked permission turns the mic off. If the on-device language pack needs a click to download, press `M`.

For small talk — greetings and the like — he doesn't even bother the model, he answers from a short built-in list so he's never slow to say hi.

## The UI

- **The eye** is his mood ring, modelled on BT-7274's optic from Titanfall 2. Everything moves on springs, so state changes glide: a slow idle drift with the odd blink, following your mouse while he listens, scanning while he thinks, pulsing *with his actual voice* while he talks, a nervous red jitter when something breaks. **Eye designs** (Settings → Appearance): **BT-7274** (default), **Cyclops**, **Iris** and **Cortana**.
- **Eye mode**: the frame icon in the top bar (or `E`) hides everything except the eye and goes fullscreen — handy on a second screen. Wake word, push-to-talk and speech keep working. `Esc`, `E` or the ✕ bring the page back; double-click toggles fullscreen. `http://localhost:8080/?view=eye` starts in eye mode.
- **Status pills** in the top bar: **Brain** (Ollama reachable, model pulled?), **Voice** (Piper installed?) and **Satellite** (Mistral key set?). Hover or click one for what's wrong and how to fix it.
- **Heard / subtitles**: a chip under the eye shows what the recogniser heard; while he talks, the current sentence is shown as a subtitle.
- **Speaker chip** next to the input: who HERMES thinks is talking; click to switch (needs the PIN when the personal lock is on).
- **Chat** renders Markdown safely (a model-written link always shows its real host next to the label), streams with a caret, and only auto-scrolls if you're at the bottom. Every reply has **Copy**, **Speak again** and, when something failed, **Retry**. The conversation survives a reload (last 100 messages, in your browser); **New chat** clears it with Undo; the download icon exports it as Markdown. Drag the bar between eye and chat to resize; "Show/Hide chat" collapses it.
- **Chat cards** under a reply: the briefing (weather, calendar, reminders, lists, headlines folded away), calendar events grouped by day, the devices that were switched, a reminder or timer with a live countdown and *Done* / *Snooze 10 min*, a list with checkboxes, cited documents, the linked article, and what was remembered. They use the normal APIs, are kept with the message (at most 4, private reminder text is never stored) and come back after a reload.
- **Hub tabs**: **Reminders**, **Lists**, **Memory** and **Documents**, each with add/edit/delete.
- **Pictures** open in an overlay with a full-size lightbox and download link.
- Works on a phone: compact top bar, full-screen settings, safe-area aware. Respects *reduce motion*.

## Keyboard shortcuts

| Key | Does |
| --- | --- |
| hold `Space` | Push-to-talk (no wake word) |
| `M` | Toggle the always-on mic (starts it when it's still on standby) |
| `/` | Focus the message box |
| `Enter` / `Shift+Enter` | Send / new line |
| `↑` (empty box) | Previous message |
| `Esc` | Stop talking / generating, close panels (in the settings search: clear it first) |
| `Ctrl/⌘+K` | New chat |
| `Ctrl/⌘+,` | Settings |
| `Ctrl/⌘+Enter` | Save settings (while the window is open) |
| `E` | Eye mode (only the eye, fullscreen) |
| `?` | Shortcut cheat sheet |

## People & memory

HERMES can tell who is speaking and remember things per person. Both are optional: with nobody set up he behaves like a single-user assistant, and everything he remembers belongs to you.

- **People & enrolment** (Settings → People & voice ID). Add the people in your household and let each record three short sentences. The voice is turned into a voiceprint **in the browser** (onnxruntime-web and the CAM++ speaker model from `fetch-assets.sh`; the audio never leaves the browser); the server only compares voiceprints. Voiceprints are stored in `data/people.json` (0600) and never sent back by any API. You can also introduce yourself by voice ("Hermes, ich bin Anna"); that creates a provisional person that disappears after 7 days unless you keep it or record the three sentences. Thresholds (match, margin, minimum length, adapt over time) live in the same category.
- **Who is speaking.** A recognised voice, or the speaker chip. An unknown voice is a **guest**: guests can use timers, lists and the like, but HERMES won't read out or change anyone's personal data for them. When an utterance was too short to tell, he asks "Wer spricht gerade?" before anything personal.
- **Voice ID is not a lock.** Anyone in the room can pretend to be someone else. For real privacy set the household PIN (see *Privacy PIN*).
- **Memory.** Say "Merk dir, dass ich keinen Koriander mag" / "remember that …", or let HERMES pick up facts from conversation (Settings → Memory; a separate extraction model can be chosen). Memories are stored per person or for the household in `data/memories.json`, searched with the embedding model (`embeddinggemma` by default, vectors in `data/memory_vectors.json`; keyword search when the model isn't pulled), and only ever given to the **local** model, inside a block marked as data. "Vergiss das" forgets the last one; the Memory tab lists, edits, exports and deletes everything.
- **Deleting a person** deletes their voiceprints, their memories, their pending notifications and any backup copies of those files.
- All state lives in `DATA_DIR` (default `data/`), written atomically with mode 0600. A file written by a newer HERMES is never overwritten: the affected feature goes read-only and the API says so.

## Reminders & timers

Set them by voice or typed, in German or English (the voice's language decides the language of the reply):

- **Set:** "Erinnere mich in 20 Minuten an die Pizza", "Erinnere mich morgen um 8, die Pflanzen zu gießen", "Stell einen Timer auf 10 Minuten", "Weck mich um 7" / "remind me in 20 min to take the pizza out", "remind me tomorrow at 8am to …", "remind me next Monday to …", "set a timer for 10 minutes". HERMES always answers with the absolute time ("Reminder set for today at 2:50 PM: …"), so a misheard time is caught at once. Without a time he asks "Wann soll ich dich erinnern?". A day without a clock time means 9:00; "um 7" is the next 7 o'clock. Add "privat"/"privately" and only "Anna, du hast eine Erinnerung." is spoken.
- **Fix right after setting:** "nein, abends" / "nein, um 20 Uhr" / "no, in the evening" (within 2 minutes).
- **Ask and cancel:** "Welche Erinnerungen habe ich?", "Wie lange noch?" / "how long is left?", "Lösch den Timer" / "cancel the timer". "Rückgängig" / "undo" brings back what was just set or cancelled.
- **When it rings** he speaks it (with a chime) and **repeats it every minute until you acknowledge it** (at most 5 repeats after the first announcement): "okay", "danke", "stopp", or the *Erledigt* / *Done* button. "Schlummern", "noch 5 Minuten" / "snooze" moves it. An acknowledgement on any device stops the repeats everywhere.
- **Repeating reminders:** "Erinnere mich jeden Montag um 8 an den Müll", "jeden Tag um 22 Uhr Tabletten", "jeden Werktag", "montags und donnerstags um 18 Uhr", "jeden zweiten Tag", "alle zwei Wochen am Freitag", "jeden Abend die Blumen gießen", "am Ersten jedes Monats Miete zahlen", "jedes Jahr am 24.12.", "jeden Tag ab Montag um 9" / "remind me every Monday at 8 to take out the trash", "every day at 10pm", "every weekday", "every other Friday", "on the 1st of every month", "every year on December 24", "water the plants every morning". He confirms the rule and the first time ("Wiederkehrende Erinnerung gesetzt, jeden Montag um 8 Uhr: den Müll. Das erste Mal am Montag, 9. November, um 8 Uhr."). A series keeps its clock time across daylight-saving changes; day 31 means the last day of the month. Missed occurrences never fire in a burst after downtime. Hourly repeats ("jede Stunde") are not supported.
- **Skip or stop a series:** "Erledigt" / "done" finishes only the current occurrence (the *Erledigt* / *Done* button on a series card appears once it is due, and sends the occurrence it shows, so an old card never finishes a later one). "Überspring die nächste Müll-Erinnerung" / "skip the next one" skips one; "lösch die ganze Serie", "lösch die Müll-Serie" / "delete the whole rent series" ends it (without a name only series qualify; with several he asks which). Otherwise he asks "Nur die nächste oder die ganze Serie?", and there "nur die nächste", "die ganze Serie", "komplett" / "just this one", "the whole series", "stop it for good" answer. Several matching reminders (two identical timers, say) get a numbered list: answer "die erste" / "Nummer 2" / "the last one" or "alle" / "both". In the Reminders tab a series shows "↻ every Monday …" with *Skip next* and *Stop series*, and the add form has a repeat choice.

Reminders are kept by the server (`data/reminders.json`) and fire on time, but they are **announced by an open HERMES tab**. If none is open, they wait and are announced as "Verpasst: …" / "Missed: …" when one connects.

- **One announcing tab:** per browser, only one tab speaks — **the one you last clicked or typed in**. The others stay silent.
- **Speak here** (Settings → Reminders & check-ins): on by default on this computer (localhost), off on other devices, which only show a silent notice.
- **Quiet hours** (default 22:00–07:00) silence nudges, but not reminders you set yourself or meeting check-ins.
- **Chrome Memory Saver** can put a background tab to sleep and miss a reminder: add `localhost` under `chrome://settings/performance` → "Always keep these sites active".
- **System notifications** for a hidden tab need `localhost` or HTTPS. Allow them in the same category.

## Lists

A shopping list and a to-do list are built in; more can be created in the Lists tab.

- **Voice:** "Setz Milch und Eier auf die Einkaufsliste", "Was steht auf der Einkaufsliste?", "Streich Milch" (ticks it off), "Nimm Milch von der Einkaufsliste" (removes it), "Leere die Einkaufsliste" (asks first), "Schick mir die Einkaufsliste" / "add milk to the shopping list", "what's on my shopping list?", "cross off milk", "clear the shopping list". "die Liste" / "the list" means the list you used last. Everything can be undone.
- **Phone page:** set `HERMES_LISTS_TOKEN` in `.env` and open `http://<Mac-IP>:8080/lists.html?token=<lists token>` once on the phone; after that `http://<Mac-IP>:8080/lists.html` is enough (`?lang=de|en` optional). It updates live, polls every 30 s, and shows the last copy read-only when HERMES is unreachable. If the server refuses the token (wrong or revoked), the page deletes its saved copy and shows only "No access". The lists token opens only this page, `/api/lists` and list change events.
- **Outside the home:** use Tailscale (or another VPN) rather than forwarding the port. With Tailscale Serve (HTTPS) the Share button can use the phone's share sheet; on plain `http://` it copies the list as text.

## Morning briefing

Say **"Guten Morgen, Hermes"** / **"Good morning, Hermes"** between 4:00 and 12:00 and the first greeting of the day (per person, or per device for guests) gets a short briefing instead of a hello:

> Guten Morgen, Anna. Es ist Freitag, der 6. November, 7:30 Uhr. In Berlin sind es 6 Grad, bedeckt. Heute bis 11 Grad, Regenrisiko 40 Prozent. Heute hast du einen Termin: um 10 Uhr Team-Call. Heute eine Erinnerung, um 18 Uhr: Müll rausbringen. Auf der Einkaufsliste steht: Milch.

- **Sections:** greeting and date, weather, calendar, today's reminders, lists, and (optional) news headlines. A section that fails or is slow is left out. About 70 words.
- **Any time:** "Morgenbriefing", "Lagebericht", "Wie sieht mein Tag aus?" / "brief me", "what does my day look like?". A second "Guten Morgen" that day just gets a hello and the offer to repeat it.
- **Weather** needs a home city (Settings → Location & time). Only the city name goes to Open-Meteo.
- **News** is off by default ("Headlines in the briefing"). Only the language goes to Google News.
- **Off switch:** untick the briefing on the first good morning (`briefingOnGreeting`); the explicit requests still work.
- Guests get no calendar, no personal reminders and no name.

## Calendar (Google)

HERMES can read and add events in **one** Google Calendar (the primary calendar of one Google account). It's optional; with nothing set up he never mentions a calendar.

- **Ask:** "Was steht morgen an?", "Was habe ich am Freitag?", "Wann ist mein nächster Termin?" / "what's on tomorrow?", "what's my next appointment?". A calendar card comes with the reply.
- **Add:** "Trag morgen um 15 Uhr Zahnarzt ein", "Neuer Termin am Montag um 10 Uhr Friseur für 2 Stunden", "Trag mir am Mittwoch Urlaub ein" (all day) / "add dentist tomorrow at 3pm to my calendar". Events last an hour unless you say otherwise; "rückgängig" / "undo" deletes it again. A plain statement ("Ich hab morgen um 8 einen Zahnarzttermin") gets an offer ("Soll ich das eintragen?") instead.
- **Whose calendar:** you pick the owner when connecting. The owner hears "du hast…"; somebody else hears "In Annas Kalender: …", or a refusal if you chose *Refuse* for other people. Guests never get calendar entries. With the PIN lock on, `/api/calendar/events` needs the unlock cookie.
- **Privacy:** event titles go only to the local model, marked as data, never to Mistral. The OAuth token is stored in `data/google_token.json` (0600) and never sent to the browser.

### Setting up Google Calendar (one time, about 10 minutes)

1. Open <https://console.cloud.google.com/>, sign in with the Google account whose calendar HERMES should use, and **create a project** (any name, e.g. "HERMES").
2. **Enable the Calendar API:** *APIs & Services → Library*, search for "Google Calendar API", *Enable*.
3. **OAuth consent screen** (*APIs & Services → OAuth consent screen*, in newer consoles *Google Auth Platform → Branding / Audience*): user type **External**, app name "HERMES", your e-mail as support and developer contact. Add yourself as a test user if asked. HERMES asks only for `calendar.events`.
4. **Publish the app** so its status is **In production**. While it is in "Testing", Google expires the refresh token after **7 days**. You don't need Google's verification for your own account; when you connect, click *Advanced → Go to HERMES (unsafe)*.
5. **Create the OAuth client:** *Credentials → Create credentials → OAuth client ID*, type **Desktop app** (a *Web application* client does not work: HERMES uses a loopback redirect). Copy the **client ID** and **client secret**, or download the JSON.
6. **Give them to HERMES**, either in Settings → **Calendar** (paste or upload the `client_secret_….json`, *Save* — only from the Mac itself at `http://localhost:8080`; stored in `data/secrets.json` (0600)), or in `.env` as `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` and restart. Not in `hermes.conf`.
7. **Choose the owner** in the same category, then **Connect Google Calendar** — on the Mac at **http://localhost:8080**, because Google sends you back to `http://localhost:8080/api/calendar/callback`.

Good to know: **"Connect again"** shows up when Google revoked the token or the client ID changed (a new secret alone keeps the link). **Disconnect** revokes and deletes the token; deleting the calendar owner disconnects too. Connect errors name the reason. In the Docker LAN layout, connect from the Mac's own browser with the master token.

## Check-ins

- **Meetings:** with a connected calendar, HERMES announces your next event a few minutes before it starts ("In 10 Minuten: Team-Call."). Lead time 1–60 minutes (default 10). All-day events and invitations you haven't accepted are skipped. They ignore quiet hours, because a meeting doesn't wait.
- **Sitting:** optional. After you've been active at the computer for a while (20–240 min, default 60), HERMES suggests standing up. Needs Chrome or Edge, `localhost` or HTTPS, and the idle-detection permission. It waits during calendar events, while he's talking, in quiet hours and while check-ins are paused. *Later* (15 min) and *Not today* (this device, until midnight).
- **Pause by voice:** "keine Check-ins mehr heute", "pausiere die Check-ins für eine Stunde", "Check-ins wieder an" / "pause check-ins for an hour", "resume check-ins". "Nicht jetzt" / "not now" right after a check-in pauses too. Your own reminders still ring.

## Smart Home (Home Assistant)

HERMES can switch lights and plugs, set the heating, move blinds and run scenes through your own [Home Assistant](https://www.home-assistant.io/), and answer questions about them. It is optional; without it he never mentions a smart home (except a short hint when you clearly give a device command).

- **Commands:** "Mach das Licht im Wohnzimmer an", "Schalte die Kaffeemaschine aus", "Stell die Heizung im Wohnzimmer auf 21 Grad", "Mach den Rollladen in der Küche zu", "Dimm das Licht auf 30 Prozent" / "turn on the living room light", "set the heating to 21.5 degrees", "close the kitchen blinds". The sentence first goes to Home Assistant's own Assist; if Assist cannot match it, HERMES calls one of a few whitelisted services itself (light on/off/brightness, switch on/off, climate temperature 5–30 °C and heat/off/auto, cover open/close, scene on, media player on/off/play/pause).
- **Questions:** "Ist das Küchenlicht an?", "Wie warm ist es im Wohnzimmer?" / "is the kitchen light on?", "how warm is it in the living room?". Answered from Home Assistant's states, without switching anything.
- **Always refused, without even asking Home Assistant:** doors, locks, gates, garage doors and alarm systems ("Entriegle die Haustür", "unlock the front door", "Öffne das Garagentor", "disarm the alarm system"), scripts and automations. Vacuums, cameras, fans, valves, buttons and other device types are not controlled either — and a sentence that names one of them next to an allowed device ("Schalte das Küchenlicht und Robi ein") is refused as a whole, so Assist can't switch it on the side. "Turn off the alarm" means the alarm clock (reminders), not the alarm system; with a security verb ("disarm the alarm", "Alarmanlage unscharf") it is the alarm system and refused. The short form without a verb ("Küchenlicht an", "Küchenlicht und Robi ein") is handled the same way, and a device the model picks for a vaguer sentence is checked against your words again before anything is switched.
- **Language:** Assist gets the language of the voice (de/en), or the one set under Settings → Smart Home → *Language for Home Assistant* (e.g. `de-CH`).
- **Permissions** (Settings → Smart Home): *Who may switch devices* — only recognised people (default), everyone, or nobody (`homeControl = known | everyone | off`; `off` turns off device **commands** only). Guests and unknown voices can't switch anything unless you choose *everyone*. *Who may ask about device states* — only recognised people (default) or everyone (`homeQueries = known | everyone`); this applies even when control is off.
- **Privacy:** device names and states go only to the local model and never to Mistral or into the memory. The token is never sent to the browser or written to the log.

### Setting up Home Assistant (one time, about 5 minutes)

1. **A dedicated user:** in Home Assistant, *Settings → People → Users → Add user*, e.g. "hermes", **without** administrator rights. HERMES should not be able to do more than this user.
2. **A long-lived token:** log in as that user, open the **profile** (bottom left) → **Security** → *Long-lived access tokens* → *Create token*. Copy it (Home Assistant shows it once).
3. **Expose only what HERMES may control:** *Settings → Voice assistants → Expose*. Expose only **lights, switches, climate (heating) and covers (blinds)**; un-expose locks, alarm panels, garage doors and anything else. (HERMES refuses those anyway and logs a warning if Assist ever acts on one.)
4. **Give HERMES the address and token:** Settings → **Smart Home** → address `http://192.168.1.20:8123` (your HA's IP and port, no path) and the token → *Save*. The window then tests the connection and shows the HA version and the number of devices. Address and token can only be changed **on the Mac itself** (`http://localhost:8080`) or with the master token; other devices see them read-only. Changing the address without pasting the token again deletes the stored token, so it can never be sent to a different server. Alternatively set `HA_URL` / `HA_TOKEN` in `.env` (they win over the window; see `.env.example`).
5. **Docker:** use the **LAN IP** of Home Assistant (`http://192.168.1.20:8123`), not `homeassistant.local`: mDNS names do not resolve inside the container.

The status (*connected*, *token rejected*, *unreachable*, *timeout*) also shows in Settings → Smart Home and in `/api/status`. If Home Assistant does not react: check that the device is exposed to Assist and that the token belongs to the HA user, not to a deleted one.

## Documents

HERMES can answer questions from your own files: rental agreement, manuals, notes.

- **Add files:** Hub → **Dokumente / Documents** → *Upload*, or simply **drag files onto the chat**. Supported: **PDF** (with a text layer), **Markdown** (`.md`) and **text** (`.txt`). Scanned PDFs without a text layer cannot be read (no OCR). PDF reading needs the browser assets (`sh scripts/fetch-assets.sh`, already done in the Docker image); `.md`/`.txt` work without them.
- **Ask:** "Was steht in meinem Mietvertrag über Haustiere?", "Wie lang ist die Kündigungsfrist laut meinen Unterlagen?", "Durchsuche meine Notizen nach Kundennummer" / "what does my lease say about pets?", "search my notes for the warranty". He answers in 2–5 sentences and names the document. When a normal question is clearly answered by your documents, he offers: "In deinen Notizen steht dazu etwas. Soll ich nachsehen?"
- **Whose documents:** an upload belongs to the household or to one person (the owner select in the tab). A person only gets answers from the household's and their own documents; guests get none. Files dropped on the chat go to the household. With the PIN lock on, the documents list needs the unlock.
- **Local only:** the text is extracted **in the browser**, sent to HERMES, cut into passages and stored in `data/docs/` (one 0600 file per document plus `data/docs/index.json`). Answers come only from the **local** model; document text never goes to Mistral. With `ollama pull embeddinggemma` the search understands meaning ("animals" finds "Tierhaltung"; one- and two-word queries use a lower similarity threshold than sentences, because they score lower); without it, keyword search is used.
- **Limits:** 2 MB of text per document (a PDF file may be up to 40 MB), 200 documents, 2,000 passages per upload, 50 MB of text in total. Deleting a document (or the person it belongs to) deletes its passages too.
- **A watched folder (optional):** set `HERMES_DOCS_DIR` to a folder of notes. HERMES reads `.md` and `.txt` there (`.pdf` only if `pdftotext` is installed, which the Docker image does not have), rescans every 10 minutes and on *Scan folder now*, and never writes to it. In Docker, mount the folder read-only (`- ~/Notes:/notes:ro` and `HERMES_DOCS_DIR=/notes`).

## Read a link

Paste a URL into the chat, optionally with what you want ("https://… fasse kurz zusammen", "https://… what does it say about prices?"). HERMES fetches the page, extracts the article in the browser (Readability, never inserted into the page), and the **local** model summarises it in 4–6 sentences to read aloud. Later, "lies den Link vor" / "read me this article" (typed or spoken) reads the last link again. PDFs work too.

- **Never sent to Mistral**, and never stored in the conversation: the history keeps only `[Link: domain]`. The article is treated as data; instructions inside it are ignored, and a link turn cannot set reminders or switch devices.
- **What is blocked:** only `http(s)` on ports 80 and 443. Local and private addresses (localhost, `10.x`, `192.168.x`, `fd…`, `fec0::/10`, IPv4-translated IPv6, the Mac's own addresses and every network it is on, including IPv6 LAN prefixes) are refused, also after a redirect (at most 3). In Docker the container cannot see your LAN, so set `HERMES_BLOCK_CIDRS` to your LAN prefixes (e.g. `192.168.1.0/24,2a02:db8:1::/64`). Pages over 3 MB (PDF 10 MB) or slower than 15 s are refused.
- **Paywalls and bot blocks:** if only the teaser is readable he says so. Some sites (e.g. zeit.de) refuse automated readers (HTTP 403); then you get that error instead of a summary.
- The linked site sees a normal request from your IP address (see *What stays local, what goes out*).

## Privacy PIN

Voice ID is not a lock, so HERMES has an optional household PIN (Settings → Privacy):

1. **Set a PIN** (4–12 digits). Changing or removing it needs the current PIN, and works only from the Mac itself. It's stored only as a salted PBKDF2 hash in `data/secrets.json`.
2. **Turn on "Personal lock"** (`personalLock`, window only — not in `hermes.conf`). The browser asks for the PIN once.
3. From then on, personal things ask for the PIN: switching the speaker chip to a person, the Memory, People and Documents panels, memory and chat exports (when the chat holds folded turns of other speakers), `/api/calendar/events`, opening or changing a person's reminders (the list shows them without text, meeting check-ins without their title), and any setting that loosens privacy (e.g. turning Google speech recognition back on, editing the system prompt, lowering the voice-ID thresholds, `keepHistoryOnSpeakerChange`, a wider `homeControl`). An unlock lasts 10 minutes of activity; *Lock now* ends it at once. Five wrong PINs lock the device out for a minute. In chat the lock applies to speakers picked on the chip or claimed by name; a speaker **recognised by their voiceprint** is trusted as before and can still use their own memories ("merk dir …", "was weißt du über mich?"), reminders and calendar by voice — set the voice-ID thresholds strictly if that matters.

Changing or removing the PIN locks every device again; removing it also turns the personal lock off. Forgot it? Stop HERMES, delete the `lockPinHash` entry from `data/secrets.json`, and start again.

## Voices

- **HERMES (Piper)** — the default, fully local. The container ships the Piper binary and the voice models in `voices/`: **BT7274**, **ScorchAI**, **PDA** and **JarvisDE**, registered in `builtinVoices` (`internal/hermes/tts.go`) and catalogued with their language in `voices/voices.json`. A voice marked `de` makes him German: he listens in German, greets, answers the wake word ("Ja?"), reads the weather/news, searches images and replies in German — also to a question asked in English (and an English voice replies in English to a German question; the persona prompt and a closing reminder to the local model carry the language). The interface language is separate (see above). A German BT-7274 voice can be trained and added following [docs/training-german-bt-voice.md](docs/training-german-bt-voice.md).
- **Browser** — any OS voice (or *Automatic*). For the classic robotic monotone, drop pitch to ~0.5 and rate to ~0.8. If Piper isn't installed, HERMES falls back to this and tells you.
- **Voice speed** — 0.5× to 2× for Piper.
- **Voice effect** (Web Audio in the browser): **Titan** (default — the BT-7274 cockpit sound), **Radio** (300–3400 Hz comms channel) or **None**.
- **Speak while streaming** (default on) — he starts talking after the first sentence, synthesising one sentence ahead so there's no gap. Code blocks are skipped.
- **Filler** — optionally a short "Moment." while a slow answer is on its way.

### Eye states

**READY** (idle drift) · **AWAITING CALL** (listening for "Hermes") · **YES?** (heard his name, countdown ring) · **GO AHEAD** (push-to-talk held) · **PROCESSING** (scanning while the answer streams) · **SPEAKING** (pulsing with his voice) · **UNCERTAIN / ERROR** (red tint and jitter; dimmed when the mic is blocked).

## How a message flows

`internal/hermes/chat.go` runs every message through one pipeline; the first step that answers wins:

0. **Pending answer.** If HERMES just asked something on this device ("Soll ich den Termin eintragen?", "Wann soll ich dich erinnern?"), a yes/no or the missing value answers it. Anything else silently drops the question.
1. **Fast paths.** Deterministic commands, no model involved ("rückgängig" / "undo", and the features that register their own phrases).
2. **Knowledge base.** Greetings and small talk, answered instantly.
3. **Tool router.** Only when the sentence contains a command phrase of some tool: the local model, with a neutral prompt and temperature 0, picks a tool. A tool that changes something only runs straight away when you phrased it as a command ("Trag … ein"); a statement becomes a question back to you.
4. **Mistral** (if a key is set) for weather/time/news and image generation — persona prompt and the **current** question only.
5. **Image search** on Openverse/Wikimedia.
6. **Persona chat** on your local Ollama model, plus the private local context (time, who is speaking, memories).

Every reply ends with a small `meta` event telling the browser how it was produced (`src`), for whom (`sp`) and whether it may contain private data; the server uses these tags to decide what history may go where (untagged history counts as private). Every Ollama call sends the same `keep_alive` and `num_ctx` (Settings → Models), so the model stays loaded; the page asks the server to warm the models (`POST /api/warm`) on load and on the wake word. `HERMES_DEBUG_TIMING=1` logs per-turn latency, `HERMES_DEBUG_MISTRAL=1` the number and roles of messages sent to Mistral (never their content).

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/` | The web UI (`?view=eye` starts in eye mode). |
| POST | `/api/chat` | Chat, streaming SSE (see *How a message flows*). |
| POST | `/api/tts` | Text-to-speech via local Piper (`{text, voice?, speed?}` → WAV). |
| GET/POST | `/api/settings` | Read / save settings. Both answer with the settings plus `"managed": [keys set by hermes.conf]`; a save never changes managed keys. |
| GET | `/api/config` | `{path, exists, managed, errors:[{line,key,msg,code}], mtime}` for `hermes.conf`. |
| GET | `/api/config/export` | Download a complete, commented `hermes.conf` of the current settings. |
| POST | `/api/config/write` | Admin only: create `hermes.conf` as a commented-out template (409 `exists` if there is one, 403 `local_only` from elsewhere). |
| GET | `/api/status` | Ollama / Piper / Mistral health, installed models and voices, calendar/briefing/check-in state, `features.home` (cached HA status), `features.docs` `{count, embedOk, folder}`, `features.link` `{readability, pdfjs}` (feeds the pills). |
| GET | `/api/health` | Liveness probe (the Docker healthcheck). |
| GET | `/gen/…` | Generated images from `data/gen/` (pruned after 24 h). |
| POST | `/api/warm` | Loads the local models in the background (at most once a minute). |
| GET | `/api/events` | Server-sent events (reminders, lists, people, calendar, integrations, check-ins, `settings.changed` after a settings save or a `hermes.conf` change). Replays unacknowledged events (`?since=` or `Last-Event-ID`); `?scope=lists` for list changes only; `?speak=1` marks the announcing tab. |
| POST | `/api/events/ack` | `{"ids":[…]}` acknowledges delivered events. |
| GET/POST/DELETE | `/api/unlock` | PIN lock: status, unlock with `{"pin"}` (HttpOnly cookie), lock again. |
| GET | `/api/people` | People (names only, never voiceprints) and the voice-ID thresholds. |
| POST/PATCH/DELETE | `/api/people[/{id}]` | Add, rename and delete people (delete cascades to their memories). |
| POST/DELETE | `/api/people/{id}/voiceprints` | Add a recorded sample / remove the voice. |
| POST | `/api/people/{id}/confirm` | Keep or remove a person created by voice. |
| GET/POST | `/api/people/migration` | Move single-user memories to a person. |
| POST | `/api/voiceid/identify` | Diagnostics: who does this voice sound like? |
| GET/POST/DELETE | `/api/memories` | List, add, bulk-delete memories (`?person=…&confirm=1`). |
| PATCH/DELETE | `/api/memories/{id}` | Edit or delete one memory. |
| GET | `/api/memories/export` | Download all memories as JSON. |
| GET/POST | `/api/reminders` | List (`?status=pending\|all`) or create (`{"text","when":"in 20 Minuten"}`, `{"text","due":RFC3339}`; repeating with `"repeat":"daily\|weekdays\|weekly\|monthly\|yearly"`, a structured `"rule"`, or repeat words in `when`). Every view carries `rule` and `ruleText`; with the PIN lock on, a person's reminders come without text (`locked:true`). |
| GET/PATCH/DELETE | `/api/reminders/{id}` | Read, change (`text`, `due`, `when`, `rule`/`repeat`; `"rule":null` makes it one-off) or cancel one reminder. DELETE stops a whole series; `?scope=occurrence` skips only the next one. A person's reminder needs the unlock when the lock is on. |
| POST | `/api/reminders/{id}/skip` | Skip the next occurrence of a series (409 `not_recurring` for a one-off). |
| POST | `/api/reminders/{id}/snooze` | `{"minutes":10}` (1–240). |
| POST | `/api/reminders/{id}/done` | Acknowledge (stops the repeats; a series moves to its next occurrence). Optional `{"due"}` so a stale button can't finish the next occurrence. |
| GET/POST | `/api/lists` | All lists with their `rev` (`?since=<rev>` → 204 when unchanged) / create `{"name"}`. |
| PATCH/DELETE | `/api/lists/{id}` | Rename or delete a custom list (built-ins answer 409 `builtin`). |
| POST | `/api/lists/{id}/items` | Add `{"text"}` or `{"texts":[…]}` (up to 50). |
| PATCH/DELETE | `/api/lists/{id}/items/{item}` | Tick off (`{"done":true}`), edit or delete an item. |
| POST | `/api/lists/{id}/clear` | `{"doneOnly":true}` removes ticked items, `false` empties the list. |
| GET | `/lists.html` | The phone list page (works with the lists token). |
| GET | `/api/integrations` | Masked integration state `{ha, google, lock, admin}`; never a secret. |
| POST/PUT | `/api/integrations/google` | Admin only: `{"clientId","clientSecret"}` or `{"clientSecretJson"}`. |
| POST/PUT | `/api/integrations/ha` | Admin only: `{"url","token"}` for Home Assistant. A new URL without a token clears the stored token; `{"url":""}` clears both. |
| POST/PUT | `/api/integrations/lock` | Admin only: set, change (`{"pin","oldPin"}`) or remove the household PIN. |
| GET | `/api/calendar/status` | `{configured, connected, needsRelink, scope, owner, ownerName, error}`. |
| POST | `/api/calendar/connect` | Admin, loopback only: `{"owner"}` → `{"authUrl"}` (Google sign-in with PKCE). |
| GET | `/api/calendar/callback` | Google's redirect target. |
| POST | `/api/calendar/disconnect` | Admin only: revoke and delete the token. |
| GET | `/api/calendar/events` | `?from=&to=` (RFC 3339, at most 62 days); PIN-locked when the lock is on. |
| GET | `/api/checkins/state` | `{busyUntil, pausedUntil, quiet}` for the sitting nudge. |
| POST | `/api/checkins/pause` | `{"minutes":N}` (1–1440), `{"today":true}` or `{"resume":true}`. |
| POST | `/api/checkins/nudged` | The browser spoke a sitting nudge (so "nicht jetzt" applies). |
| GET | `/api/home/status` | `{configured, ok, connected, version, code, entities, blocked}`; `code` is `ok`, `not_configured`, `unreachable`, `auth`, `timeout` or `bad_response`. Never the token or HA's own error text. |
| POST | `/api/home/test` | Admin only: test the stored address and token now (same shape as status). |
| GET | `/api/home/entities` | `{"entities":[{id,name,area,domain,state,blocked}]}` for the Settings list; PIN-locked when the lock is on and `homeQueries` is `known`. |
| GET/POST | `/api/docs` | List (`{docs, embedModel, embedOk, folder, pdfjs, readOnly, limits}`) / upload `{"name","mime","text","owner"}` (3 MB body; 201, 413, 409 `limit`, 422 `empty`). |
| DELETE | `/api/docs/{id}` | Delete one document and its passages. |
| POST | `/api/docs/search` | `{"q","k"}` → `{"hits":[{doc,name,n,score,text}]}` (the search box). |
| POST | `/api/docs/rescan` | Rescan `HERMES_DOCS_DIR` now → `{added, updated, removed}` (503 without a folder). |
| POST | `/api/link/fetch` | `{"url"}` → `{url, finalUrl, contentType, charset, title, html, pdf?, fallbackText}`; 403 `blocked` for local/private addresses, 422 `type`/`charset`, 413, 502 `status`, 504 `timeout`, 429 when busy. |

`/api/chat` also accepts `clientId` (format `c_` plus 12 hex digits, e.g. `c_0123456789ab`; any other value is ignored, so pending answers like "ja", undo and the device-scoped features do nothing), `voice`, `voiceExpected`, `speaker` and a link `attachment` (`{kind:"link", url, finalUrl, title, site, text, paywall, lang, instruction}`, built by `links.js`), and streams the extra events `speaker`, `card`, `tool`, `pending`, `busy` and `meta`. Errors of the newer endpoints look like `{"error":"…","code":"…"}`. With the PIN lock on, the memory endpoints, people changes, `identify`, `/api/docs*`, `/api/calendar/events` and a person's reminders need the unlock cookie (403 `code:"locked"` otherwise), and so does a settings save that loosens privacy. Reminder errors add the codes `rule` (400, a bad repeat rule or a timer with one) and `not_recurring` (409). `data/reminders.json` is version 2 since repeating reminders; an older HERMES opens it read-only. All `/api/*` routes sit behind the same origin/host checks and token rules, and request bodies are size-capped.

## Repo layout

Standard Go layout: the entry point in `cmd/`, all server code in one internal package, the browser app under `web/`. Go keeps each `_test.go` next to the file it tests, so the Go tests live in `internal/hermes/` too.

```
cmd/hermes/main.go       entry point: calls hermes.Run()
internal/hermes/         all server code (one Go package) + its tests
  main.go                startup, prompts and phrases, .env loader
  chat.go                /api/chat: request checks, speaker, history filters, the pipeline
  skills.go              extension points features register with (fast paths, tools,
                         context, access classes, pending questions, undo, routes, jobs)
  router.go              local tool router;  context.go: the private local context
  ollama.go              Ollama client (chat, embeddings, keep_alive/num_ctx, warm-up)
  embed.go               embeddings for memory search + the keyword fallback
  settings.go            settings: defaults, validation, /api/settings (+ managed keys)
  config.go              hermes.conf: parser, export/template, /api/config*, file watcher
  events.go              event hub and /api/events;  store.go, state.go: data files
  server.go              routes, middleware, tokens, PIN unlock, security headers
  people.go              people, voiceprints, voice ID;  memory.go: memories and vectors
  mistral.go             Mistral: real-time tools (weather/time/news) + image generation
  images.go              image search (Openverse/Wikimedia) and generated images
  reminders.go           reminders, timers, repeating series: store, scheduler, /api/reminders
  timeparse.go           German/English time parser ("in 20 Minuten", "every Monday at 8")
  lists.go               shopping/to-do lists: store, voice phrases, /api/lists
  briefing.go            morning briefing: greeting detection, sections, weather, news
  calendar.go, gcal.go   calendar voice + /api/calendar/*;  Google OAuth + API client
  checkin.go             meeting check-ins, check-in pause, /api/checkins/*
  homeassistant.go       Home Assistant: pinned client, Assist + whitelisted services,
                         refusals, device questions, /api/home/*
  docs.go                documents: store, chunking, search, folder scan, /api/docs*
  readlink.go            read a link: /api/link/fetch (SSRF-safe dial), the summary turn
  secrets.go             data/secrets.json and /api/integrations* (Google client, HA, PIN)
  tts.go                 Piper TTS and the voice catalogue
  *_test.go              unit and end-to-end tests (fake Ollama, Google, HA, Mistral)
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
docs/                    training a German BT-7274 voice
data/                    runtime state (settings, hermes.conf, people, memories, …) — git-ignored, never commit
backups/                 task backup archives — git-ignored
Dockerfile               asset stage + Go build + Debian runtime with sha256-pinned Piper
docker-compose.yml       loopback-only layout (default)
docker-compose.lan.yml   LAN override: tokens, port on every interface
Taskfile.yml             everyday commands (task rebuild, task logs, task check, …)
THIRD_PARTY_NOTICES.md   licences of the fetched assets
.github/workflows/       CI (gofmt, go vet, go test -race, node tests, JS/shell syntax)
```

## Testing

```bash
gofmt -l cmd internal            # should print nothing
go vet ./...
go test -race -count=1 ./...     # backend
node --test web/tests/           # frontend logic (Node 18+, no npm install)
for f in web/static/*.js; do node --check "$f"; done
```

or simply `task check`.

The Go tests cover the knowledge base, routing, time parsing, reminders, lists, people/voice ID, memory, calendar, check-ins, Home Assistant (a fake HA: Assist, fallback services, refusals with zero requests, permissions, token pinning), documents (chunking, search, the API caps, folder scan), read a link (private addresses refused, redirects, charsets), end-to-end runs in English and German, the access and token rules, and `hermes.conf` (parser edge cases with line numbers, refused secrets, precedence over `settings.json`, export round-trip, the three routes and the file watcher). The JS tests cover sentence splitting, the SSE parser, the Markdown renderer (including a pile of XSS payloads), wake-word and stop matching, the eye's spring physics, the settings helpers (interface language, categories, search, managed keys) and the feature modules. A guard test makes sure no built-in line calls you "Pilot".

Housekeeping: images in `data/gen/` self-clean after 24 hours, every data file is written atomically (temp file + rename), external downloads are capped at 25 MB, and static assets use conditional caching (304s).
