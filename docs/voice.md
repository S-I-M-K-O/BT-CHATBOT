# Voice & speech

[← README](../README.md)

- [Talking to him](#talking-to-him)
- [Speech recognition](#speech-recognition)
- [Voices](#voices)

## Talking to him

The mic button (the little HERMES visor icon, bottom-left) is on by default, and it's the *always-on* kind: he ignores you completely until you say **"Hermes"**. He also accepts "Hermès", "her mess" and "hermis", because Chrome mishears him a lot.

- **Wake word.** Say the thing right away ("Hermes, what time is it?" works, and so does "What time is it, Hermes?"), or say just his name and wait for the eye to flip to *"YES?"*. A countdown ring shows how long he'll wait (8 seconds) before he goes back to ignoring you; the window stays open while you're still talking. The "Yes?" audio is cached per voice, so the answer is instant.
- **Push-to-talk.** Don't feel like saying his name? **Hold Space** (or long-press the mic button on a phone). Push-to-talk skips the wake word.
- **Interrupting.** While he speaks he only listens for "Hermes, stop", so he never answers himself; about 300 ms after he finishes he's back to normal listening. Interrupt him any time with **Esc**, the **Stop** button (the Send button turns into it while he's busy), or **"Hermes, stop"**.
- **Standby.** Browsers only allow speech recognition after you've interacted with the page, so on a fresh load the mic button shows a dashed *standby* outline. Any click or key press (including the mic button or `M`) starts listening.
- **When the mic fails.** If the mic gets blocked or unplugged, or no speech service is reachable at all, a toast tells you what happened instead of leaving a silently dead button, and he backs off instead of hammering the recogniser. A missing mic or a dropped connection is treated as temporary: he retries every 15 seconds and picks up as soon as it's back. Only a blocked permission turns the mic off. If the on-device language pack needs a click to download, press `M`.
- **Small talk.** For greetings and the like he doesn't even bother the model; he answers from a short built-in list so he's never slow to say hi.

## Speech recognition

One switch: Settings → Voice & speech → *Use Google speech recognition*.

- **On** (default): Chrome sends your audio to Google's speech service.
- **Off**: recognition runs on this device only (recent Chrome; the language pack downloads once).
- If Google is unreachable, HERMES falls back to on-device recognition by itself.

Speech recognition needs **Chrome or Edge**. Other browsers can type; Brave exposes the API without a speech service, so HERMES tells you to switch.

## Voices

- **HERMES (Piper)** — the default, fully local. The container ships the Piper binary and the voice models in `voices/`: **BT7274**, **ScorchAI**, **PDA** and **JarvisDE**. They are registered in `builtinVoices` (`internal/hermes/tts.go`) and catalogued with their language in `voices/voices.json`.
- **The voice decides the language.** A voice marked `de` makes him German: he listens in German, greets, answers the wake word ("Ja?"), answers weather, time and news questions, searches images and replies in German, also to a question asked in English. An English voice replies in English to a German question. The persona prompt and a closing reminder to the local model carry the language. The interface language is separate (see [Configuration](configuration.md#interface-language)).
- **Your own German BT-7274.** A German BT-7274 voice can be trained and added following [Training a German BT-7274 voice](training-german-bt-voice.md).
- **Browser** — any OS voice (or *Automatic*). For the classic robotic monotone, drop pitch to ~0.5 and rate to ~0.8. If Piper isn't installed, HERMES falls back to this and tells you.
- **Voice speed** — 0.5× to 2× for Piper.
- **Voice effect** (Web Audio in the browser): **Titan** (default, the BT-7274 cockpit sound), **Radio** (300–3400 Hz comms channel) or **None**.
- **Speak while streaming** (default on) — he starts talking after the first sentence, synthesising one sentence ahead so there's no gap. Code blocks are skipped.
- **Filler** — optionally a short "Moment." while a slow answer is on its way.
