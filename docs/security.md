# Security & privacy

[← README](../README.md)

- [What stays local, what goes out](#what-stays-local-what-goes-out)
- [Built in](#built-in)
- [What you must configure](#what-you-must-configure)
- [What it does not protect against](#what-it-does-not-protect-against)

HERMES is built for a home: everything personal stays on your machine, and the network boundary is a token.

## What stays local, what goes out

Chat, speech output, voice ID, memories, people, reminders, lists and documents all run on your machine. Every question that needs a language model goes to your local Ollama. These are the things that talk to the internet (or your LAN):

| What | Goes to | When | What is sent |
| --- | --- | --- | --- |
| Speech recognition | Google (via Chrome) | Only with *Use Google speech recognition* on (default) | Your speech audio. Turn the switch off for on-device recognition. |
| Weather, time zones | Open-Meteo | Weather questions, "time in Tokyo", the briefing | The city name (and the coordinates it returned) |
| German news | Tagesschau | German news questions, the briefing's headline | A plain request without parameters |
| English news, news topics | Google News RSS | English news, "news about X", the briefing's headline | The language and, for a topic, the topic words |
| Image search | Openverse | "Find a picture of …" | The search words |
| Calendar | Google Calendar API | Only once you connect a calendar | Calendar reads and the events you add |
| Smart Home | Your Home Assistant (in your LAN) | Device commands and questions, the status check | The sentence (for HA's Assist) or a whitelisted service call, with your HA token. Nothing goes to the internet unless your HA URL is on the internet. |
| Read a link | The web site you linked | When you paste a URL or say "lies den Link vor" | A plain GET request for that page (your IP address and a browser-like `Accept` header; no cookies). The article text then goes only to the local model. |

The conversation history, names, memories, voiceprints, calendar entries and documents are never sent to the weather, news or image services. `internal/hermes/privacy_test.go` checks this against fake services.

## Built in

- **Local by default.** The server binds `127.0.0.1`, and Docker publishes the port on loopback only. Requests from other machines need `HERMES_TOKEN` (cookie or `Authorization: Bearer`); without a token they are refused. Host and origin checks block DNS rebinding and cross-site requests. Admin actions (integrations, calendar connect, creating `hermes.conf`) only work on the machine itself.
- **Scoped phone token.** `HERMES_LISTS_TOKEN` opens only the list page, the list API and list events. A phone whose token was revoked drops its saved copy of the lists.
- **Personal data stays local and labelled.** Memories, calendar, reminders, documents, link articles and device states only go to the local model, inside blocks marked as data. Weather, time and news questions send only the city or the topic. After a reply built from a web page, a document, the briefing or the calendar, the next answer gets no personal context, so instructions hidden in that text never sit next to your memories. Links written by the model always show their real host.
- **Actions only from what you said.** Tools run from your own sentence, never from a page, a document or a calendar title; a statement only gets an offer ("Soll ich …?"). List names (which the lists token can create) are never shown to the tool router.
- **Who is speaking decides what he may do** (access classes): guests never hear or change personal data, sticky or claimed speakers can't write, and "undo" needs the same rights as the action it undoes. Device control defaults to recognised people only (`homeControl = known`).
- **Household PIN** (optional, see [Privacy PIN](people-and-memory.md#privacy-pin)) locks memories, people, documents, calendar events, a person's reminders, the chat export and every setting that would loosen privacy, in the panels and the API, and in chat for a speaker picked on the chip or claimed ("ich bin Anna"). A speaker **recognised by voice** still uses their own memories, reminders and calendar by voice.
- **Smart Home safety.** Doors, locks, gates, garage doors and alarms are refused before anything is sent; a sentence that also names a device HERMES doesn't control is refused as a whole. The HA token is pinned to its address and never leaves the server.
- **Read-a-link and image downloads** refuse local and private addresses (also after redirects) and only use ports 80/443.
- **Bounded downloads.** Weather, time and news requests have a 5-second timeout and a size cap; other external downloads are capped at 25 MB.
- **Files.** Everything in `data/` is written atomically with mode 0600 (directories 0700, `data/gen/` included; an older 0755 folder is tightened at start), including image-search pictures and `hermes.conf`. `data/` and `backups/` are in `.gitignore`: they hold voiceprints, memories and tokens, so never commit or share them. The container runs unprivileged and read-only.

## What you must configure

1. **`HERMES_TOKEN`** (long and random: `task token`) before you open HERMES to the LAN; `HERMES_LISTS_TOKEN` for family phones instead of the master token.
2. **The PIN and the personal lock** if more than one person uses HERMES. Voice ID alone is not a lock.
3. **A dedicated, non-admin Home Assistant user** for the HA token, with only lights, switches, heating and blinds exposed to Assist.
4. **`HERMES_BLOCK_CIDRS`** in Docker (your LAN prefixes, e.g. `192.168.1.0/24`): inside the container HERMES can't see which networks your host is on, so read-a-link needs them to keep away from your LAN.
5. **No other web apps on the same host name or IP** (cookies don't care about ports), and **TLS** (e.g. Tailscale Serve) when you reach HERMES from outside or over untrusted Wi-Fi. On plain HTTP the token can be sniffed.
6. **Google speech recognition** is on by default and sends your audio to Google; switch it off for on-device recognition.

## What it does not protect against

Someone who holds the master token, or who sits at an unlocked machine. A four-digit PIN allows five tries a minute; use a longer PIN if that matters to you.
