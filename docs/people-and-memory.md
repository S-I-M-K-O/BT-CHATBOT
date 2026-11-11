# People & memory

[← README](../README.md)

- [People and voice ID](#people-and-voice-id)
- [Memory](#memory)
- [Privacy PIN](#privacy-pin)

HERMES can tell who is speaking and remember things per person. Both are optional: with nobody set up he behaves like a single-user assistant, and everything he remembers belongs to you.

## People and voice ID

- **People & enrolment** (Settings → People & voice ID). Add the people in your household and let each record three short sentences. The voice is turned into a voiceprint **in the browser** (onnxruntime-web and the CAM++ speaker model from `fetch-assets.sh`); the audio never leaves the browser, and the server only compares voiceprints. Voiceprints are stored in `data/people.json` (0600) and never sent back by any API.
- **Introducing yourself by voice.** "Hermes, ich bin Anna" creates a provisional person that disappears after 7 days unless you keep it or record the three sentences.
- **Thresholds** (match, margin, minimum length, adapt over time) live in the same category.
- **Who is speaking.** A recognised voice, or the speaker chip. An unknown voice is a **guest**: guests can use timers, lists and the like, but HERMES won't read out or change anyone's personal data for them. When an utterance was too short to tell, he asks "Wer spricht gerade?" before anything personal.
- **Voice ID is not a lock.** Anyone in the room can pretend to be someone else. For real privacy set the household PIN (see [Privacy PIN](#privacy-pin)).
- **Deleting a person** deletes their voiceprints, their memories, their pending notifications, their last live-info question and any backup copies of those files.

## Memory

- **Saving.** Say "Merk dir, dass ich keinen Koriander mag" / "remember that …", or let HERMES pick up facts from conversation (Settings → Memory; a separate extraction model can be chosen).
- **Storage.** Memories are stored per person or for the household in `data/memories.json`.
- **Search.** They are searched with the embedding model (`embeddinggemma` by default, vectors in `data/memory_vectors.json`), or with keyword search when the model isn't pulled.
- **Only local.** Memories are only ever given to the **local** model, inside a block marked as data.
- **Forgetting.** "Vergiss das" forgets the last one; the Memory tab lists, edits, exports and deletes everything.

All state lives in `DATA_DIR` (default `data/`), written atomically with mode 0600. A file written by a newer HERMES is never overwritten: the affected feature goes read-only and the API says so.

## Privacy PIN

Voice ID is not a lock, so HERMES has an optional household PIN (Settings → Privacy):

1. **Set a PIN** (4–12 digits). Changing or removing it needs the current PIN and works only from the host itself. It's stored only as a salted PBKDF2 hash in `data/secrets.json`.
2. **Turn on "Personal lock"** (`personalLock`, window only, not in `hermes.conf`). The browser asks for the PIN once.
3. **From then on, personal things ask for the PIN:**
   - switching the speaker chip to a person;
   - the Memory, People and Documents panels;
   - memory and chat exports (when the chat holds folded turns of other speakers);
   - `/api/calendar/events`;
   - opening or changing a person's reminders (the list shows them without text, meeting check-ins without their title);
   - any setting that loosens privacy, e.g. turning Google speech recognition back on, editing the system prompt, lowering the voice-ID thresholds, `keepHistoryOnSpeakerChange`, or a wider `homeControl`.

An unlock lasts 10 minutes of activity; *Lock now* ends it at once. Five wrong PINs lock the device out for a minute.

In chat the lock applies to speakers picked on the chip or claimed by name. A speaker **recognised by their voiceprint** is trusted as before and can still use their own memories ("merk dir …", "was weißt du über mich?"), reminders and calendar by voice. Set the voice-ID thresholds strictly if that matters.

Changing or removing the PIN locks every device again; removing it also turns the personal lock off. Forgot it? Stop HERMES, delete the `lockPinHash` entry from `data/secrets.json`, and start again.
