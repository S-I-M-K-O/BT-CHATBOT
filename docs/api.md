# API

[← README](../README.md)

- [Endpoints](#endpoints)
- [Chat events](#chat-events)
- [Errors and access](#errors-and-access)

All `/api/*` routes sit behind the same origin/host checks and token rules (see [Setup](setup.md#reaching-hermes-from-other-devices)), and request bodies are size-capped.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/` | The web UI (`?view=eye` starts in eye mode). |
| POST | `/api/chat` | Chat, streaming SSE (see [Chat events](#chat-events)). |
| POST | `/api/tts` | Text-to-speech via local Piper (`{text, voice?, speed?}` → WAV). |
| GET/POST | `/api/settings` | Read / save settings. Both answer with the settings plus `"managed": [keys set by hermes.conf]`; a save never changes managed keys. |
| GET | `/api/config` | `{path, exists, managed, errors:[{line,key,msg,code}], mtime}` for `hermes.conf`. |
| GET | `/api/config/export` | Download a complete, commented `hermes.conf` of the current settings. |
| POST | `/api/config/write` | Admin only: create `hermes.conf` as a commented-out template (409 `exists` if there is one, 403 `local_only` from elsewhere). |
| GET | `/api/status` | Ollama / Piper health, installed models and voices, calendar/briefing/check-in state, `features.home` (cached HA status), `features.docs` `{count, embedOk, folder}`, `features.link` `{readability, pdfjs}` (feeds the pills). |
| GET | `/api/health` | Liveness probe (the Docker healthcheck). |
| GET | `/gen/…` | Image-search pictures from `data/gen/` (pruned after 24 h). |
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

## Chat events

`POST /api/chat` takes `messages` (the history, each with `role`, `content` and the `src` / `sp` tags the server sent), `clientId` (format `c_` plus 12 hex digits, e.g. `c_0123456789ab`; any other value is ignored, so pending answers like "ja", undo and the device-scoped features do nothing), `timeZone`, `spoken`, `voice` (the voice-ID sample), `voiceExpected`, `speaker` and a link `attachment` (`{kind:"link", url, finalUrl, title, site, text, paywall, lang, instruction}`, built by `links.js`).

It answers with server-sent events, one JSON object per `data:` line:

| Event | Meaning |
| --- | --- |
| `{"delta":"…"}` | The next piece of the reply text |
| `{"speaker":{…}}` | Who HERMES thinks is speaking |
| `{"card":{"kind","data"}}` | A chat card (at most one per reply; see below) |
| `{"tool":{"name","ok"}}` | A tool ran (e.g. a reminder was set) |
| `{"pending":{"kind","expiresIn"}}` | HERMES asked something; the next short answer on this device resolves it |
| `{"busy":true}` | A slow step (a model load, a slow web service) is under way |
| `{"error":"…"}` | Something failed; the same text follows as deltas so it is spoken |
| `{"intent":"image"}`, `{"image":"/gen/…"}` | Image-search results for the pictures overlay |
| `{"meta":{"src","sp","private"}}` | How the reply was produced, for whom, and whether it may hold private data |
| `{"done":true}` | End of the reply |

The browser stores `meta` with both messages and sends it back as history; the server uses these tags to decide what history may go where (untagged history counts as private). `src` values include `kb` (built-in replies), `fast:<feature>`, `tool:<name>`, `local:docs`, `local:link`, `realtime:weather`, `realtime:time`, `realtime:news` and `persona` (the local model).

**Card kinds:** `briefing`, `calendar`, `home`, `reminder`, `timer`, `list`, `docs`, `link`, `memory`, `weather` and `news`. The two live-info cards look like this:

```json
{"kind":"weather","data":{"city":"Berlin","rain":false,
  "now":{"temp":6,"feelsLike":3,"code":3,"condition":"bedeckt","wind":12},
  "days":[{"label":"Heute","date":"2026-11-11","max":11,"min":2,"rain":40,"code":61,"condition":"Regen"}]}}

{"kind":"news","data":{"source":"tagesschau","topic":"",
  "items":[{"title":"…","topline":"…"}]}}
```

`now` is only present for questions about current conditions; `days` has 1 to 4 entries; `condition` is in the reply language and `code` is the WMO weather code. `source` is `tagesschau` or `googlenews`, and `topline` is only set for Tagesschau items. Time answers have no card.

## Errors and access

Errors of the newer endpoints look like `{"error":"…","code":"…"}`. With the PIN lock on, the memory endpoints, people changes, `identify`, `/api/docs*`, `/api/calendar/events` and a person's reminders need the unlock cookie (403 `code:"locked"` otherwise), and so does a settings save that loosens privacy. Reminder errors add the codes `rule` (400, a bad repeat rule or a timer with one) and `not_recurring` (409). `data/reminders.json` is version 2 since repeating reminders; an older HERMES opens it read-only. All `/api/*` routes sit behind the same origin/host checks and token rules, and request bodies are size-capped.
