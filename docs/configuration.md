# Configuration

[← README](../README.md)

- [Where settings live](#where-settings-live)
- [The Settings window](#the-settings-window)
- [Interface language](#interface-language)
- [hermes.conf](#hermesconf)
- [.env](#env)

## Where settings live

There are three places, each for its own kind of thing:

| Where | What belongs there | Who wins |
| --- | --- | --- |
| **`.env`** (or real environment variables) | Server things and secrets: port/address, tokens, Ollama URL, data paths, Google client, Home Assistant, debug switches. See `.env.example`. Needs a restart. | Credentials from `.env` win over the ones saved in the window (shown there as "from .env"). |
| **`data/hermes.conf`** | Any normal setting (voice, language, models, quiet hours, …) as plain text. No secrets. Picked up within ~3 seconds, no restart. | A key set here wins over the Settings window and is **locked** there (🔒 *in hermes.conf*). |
| **Settings window** (stored in `data/settings.json`) | Everything else, clicked together in the UI. Credentials and the PIN go to `data/secrets.json` (0600). | Applies to every key the file doesn't set. |

## The Settings window

Click the gear (or `Ctrl/⌘+,`). On a desktop it's a large window with categories on the left — **General, Voice & speech, People & voice ID, Memory, Reminders & check-ins, Calendar, Smart Home, Documents, Location & time, Privacy, Models, Appearance, Config file** and **More** — and a search box at the top that matches any word, ignores case and accents (`ß` finds `ss`) and shows the number of hits per category. On a phone it's full screen: the category list first, a back arrow to return.

Changes preview live (eye, voice, language). **Save** (or `Ctrl/⌘+Enter`) keeps them; **Cancel**, `Esc` or the ✕ rolls them back, with an Undo toast in case that was a mistake. **Reset to defaults** keeps whatever `hermes.conf` sets.

**Location & time** holds the home city (`homeCity`), which weather questions and the briefing use when you name no place, and the household time zone.

## Interface language

Settings → General → **Interface language**: *Automatic* (follow the voice, the default), *Deutsch* or *English*.

- With *Deutsch* or *English* the screen texts switch **and the voice lists only offer voices of that language**. When you pick the language in the Settings window and the current voice is of the other language, HERMES switches to the first matching one (e.g. English → BT-7274) and says so.
- What he listens for and how he answers follow the voice, so picking a language there effectively sets his language.
- A `uiLang` set in `hermes.conf` or through the API does **not** change the voice: a voice of the other language stays selected (and listed), so the screen is in one language and the replies in the other until you pick a matching voice.
- With *Automatic* every voice is listed and the interface follows the voice.

In `hermes.conf` it's `uiLang = auto | de | en`.

## hermes.conf

A plain text file next to `settings.json`: `data/hermes.conf` by default, `/app/data/hermes.conf` in Docker (that's your mounted `./data`), or wherever `HERMES_CONFIG` points. The easiest start is Settings → **Config file**:

- **Export hermes.conf** downloads a complete file with all your current settings, grouped in sections, with a German and an English comment per key. (`task config:export` does the same.)
- **Create hermes.conf (template)** (only on this machine, only while no file exists) writes the same file into `data/` with every value commented out. Nothing is locked until you remove the `#` in front of a line.

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

- One `key = value` per line. Keys are the setting names from the export (`speak`, `uiLang`, `piperVoice`, `quietStart`, …); case doesn't matter.
- `#` starts a comment (at the start of a line or after a space); lines starting with `;` are comments too. Blank lines are ignored, and `[section]` headers are only for order.
- Values: `true`/`false` (also `yes/no`, `on/off`, `ja/nein`, `an/aus`, `1/0`), numbers (`0.5` or `0,5`), text — in `"double quotes"` only when it needs leading/trailing spaces or a `#`. `\n` in `systemPrompt` is a line break.
- A wrong line never stops HERMES: unknown keys, invalid values (out of range, unknown voice, bad time) and duplicates are listed under Settings → Config file and ignored (for duplicates the last one wins).
- **No secrets.** Tokens, passwords, Google/Home Assistant credentials and the PIN are refused; they belong in `.env` or the window. `personalLock` can only be switched in the window (it needs the PIN).
- The server checks the file every 3 seconds. When it changes, open pages reload their settings and show "Settings updated from hermes.conf." Delete a line and the setting is editable in the window again (it keeps its last value).
- Saving from the window never changes a key the file sets; that key keeps the file's value.

## .env

`.env.example` lists every variable with a comment. The ones you are most likely to set:

| Variable | For |
| --- | --- |
| `HERMES_TOKEN`, `HERMES_LISTS_TOKEN`, `ALLOWED_HOSTS` | Access from other devices ([Setup](setup.md#reaching-hermes-from-other-devices)) |
| `OLLAMA_BASE` / `DOCKER_OLLAMA_BASE` | Where Ollama is, for `go run` / inside the container |
| `ADDR`, `PORT` | Bind address (default `127.0.0.1`) and port (default `8080`) |
| `HERMES_UID`, `HERMES_GID` | Docker: the host user the container runs as |
| `DATA_DIR`, `SETTINGS_FILE`, `HERMES_CONFIG`, `GEN_DIR` | Where state, settings, `hermes.conf` and image-search pictures live |
| `PIPER_BIN`, `VOICE_DIR`, `STATIC_DIR` | Piper binary, voice models, web UI files |
| `HERMES_TZ` | Household time zone fallback (Settings → Location & time wins) |
| `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | Google Calendar ([setup](briefing-calendar-checkins.md#setting-up-google-calendar)) |
| `HA_URL`, `HA_TOKEN` | Home Assistant ([setup](smart-home.md#setting-up-home-assistant)) |
| `HERMES_DOCS_DIR` | A watched notes folder ([Documents](documents-and-links.md#a-watched-folder)) |
| `HERMES_BLOCK_CIDRS` | Docker: your LAN prefixes, kept away from read-a-link |
| `HERMES_BEHIND_LOOPBACK`, `HERMES_TRUSTED_PEER`, `HERMES_INSECURE_NO_TOKEN` | Docker / proxy edge cases ([Setup](setup.md#reaching-hermes-from-other-devices)) |
| `HERMES_DEBUG_TIMING`, `HERMES_DEBUG_VOICEID` | Debug logging: per-turn latency, voice-ID scores |

Weather, time and news need no key at all.
