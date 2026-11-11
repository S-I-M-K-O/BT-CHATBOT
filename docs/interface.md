# The interface

[← README](../README.md)

- [The eye](#the-eye)
- [Top bar and chat](#top-bar-and-chat)
- [Chat cards](#chat-cards)
- [Keyboard shortcuts](#keyboard-shortcuts)

## The eye

The eye is his mood ring, modelled on BT-7274's optic from Titanfall 2. Everything moves on springs, so state changes glide: a slow idle drift with the odd blink, following your mouse while he listens, scanning while he thinks, pulsing *with his actual voice* while he talks, a nervous red jitter when something breaks.

- **Eye designs** (Settings → Appearance): **BT-7274** (default), **Cyclops**, **Iris** and **Cortana**.
- **Eye mode**: the frame icon in the top bar (or `E`) hides everything except the eye and goes fullscreen, which is handy on a second screen. Wake word, push-to-talk and speech keep working. `Esc`, `E` or the ✕ bring the page back; double-click toggles fullscreen. `http://localhost:8080/?view=eye` starts in eye mode.

**Eye states:** **READY** (idle drift) · **AWAITING CALL** (listening for "Hermes") · **YES?** (heard his name, countdown ring) · **GO AHEAD** (push-to-talk held) · **PROCESSING** (scanning while the answer streams) · **SPEAKING** (pulsing with his voice) · **UNCERTAIN / ERROR** (red tint and jitter; dimmed when the mic is blocked).

## Top bar and chat

- **Status pills** in the top bar: **Brain** (Ollama reachable, model pulled?) and **Voice** (Piper installed?). Hover or click one for what's wrong and how to fix it.
- **Heard / subtitles**: a chip under the eye shows what the recogniser heard; while he talks, the current sentence is shown as a subtitle.
- **Speaker chip** next to the input: who HERMES thinks is talking. Click to switch (needs the PIN when the personal lock is on).
- **Chat** renders Markdown safely (a model-written link always shows its real host next to the label), streams with a caret, and only auto-scrolls if you're at the bottom. Every reply has **Copy**, **Speak again** and, when something failed, **Retry**. The conversation survives a reload (last 100 messages, in your browser); **New chat** clears it with Undo; the download icon exports it as Markdown. Drag the bar between eye and chat to resize; "Show/Hide chat" collapses it.
- **Hub tabs**: **Reminders**, **Lists**, **Memory** and **Documents**, each with add/edit/delete.
- **Pictures** from image search open in an overlay with a full-size lightbox and download link.
- **Phones**: compact top bar, full-screen settings, safe-area aware. The UI respects *reduce motion*.

## Chat cards

Replies about reminders, lists, the briefing, the calendar, devices, documents, links, memory, the weather and the news come with a compact card under the bubble:

| Card | Shows |
| --- | --- |
| Briefing | Weather, calendar, reminders, lists; headlines folded away |
| Calendar | Events grouped by day |
| Smart Home | The devices that were switched |
| Reminder / timer | The reminder with a live countdown and *Done* / *Snooze 10 min* |
| List | The list with checkboxes to tick items off |
| Documents | The cited documents |
| Link | The linked article |
| Memory | What was remembered or forgotten |
| Weather | The place, current conditions (temperature, feels-like, wind) and up to four days with high/low, rain chance and an icon |
| News | Up to five headlines (with the Tagesschau topline) and the source |

Cards use the normal APIs. They are kept with the message (at most 4; private reminder text is never stored) and come back after a reload.

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
