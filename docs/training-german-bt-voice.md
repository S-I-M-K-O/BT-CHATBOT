# Training a German BT-7274 (Titanfall 2) Piper Voice

Step-by-step guide to create your own Piper `.onnx` model of BT-7274 with the
German voice (actor: **Fahri Yardim**) and plug it into this HERMES project.

**Warning:** Game audio is copyrighted by Respawn / EA. This is for your own
personal, non-commercial use.

---

## Reality check (read first)

Piper uses the VITS neural TTS architecture. You are **not** converting game
clips — you are **fine-tuning a model on clips**. This assumes:

- An **NVIDIA GPU** with 8–24 GB VRAM (Linux, or Windows with WSL2). CPU
  training is possible but impractically slow.
- **~30–60+ minutes of clean, single-voice audio** for a decent fine-tune.
  BT's German campaign dialogue is a few hundred clips — workable, but quality
  scales with data.
- The **game recordings are compressed/processed** (FX, reverb, background).
  The more you clean them, the better the model sounds.
- Fine-tuning from an existing German Piper checkpoint (`de_DE`) dramatically
  helps.

---

## Step 1 — Extract the German BT audio from the game

1. **Own/install Titanfall 2** (Steam or EA App). You need the game files; there
   is no public `german.mbnk` download.
2. Download an extractor:
   - **RSX** (newest): https://github.com/r-ex/rsx
   - **Legion+** (older, still works, Windows): https://github.com/r-ex/LegionPlus
3. In the tool, set **Audio Language = german** (in Legion+ it is under
   Settings; on the CLI use `--audiolanguage german`).
4. **Load** `Titanfall2/r2/sound/general.mbnk`.
5. **Filter for BT's lines** — event names typically start with `bt_`,
   `vanguard`, or live under `diag_...`. Preview before exporting (`P` opens
   preview in Legion).
6. **Export selected as 16-bit WAV** into `dataset/wav/`. Export All of BT's
   events to start; you will curate them next. (Export All of the whole bank is
   ~73 GB — not what you want.)

Alternative quick start if you do not own the game: search YouTube for German BT
voiceline compilations ("BT-7274 Deutsche Voicelines") and rip a few dozen clean
lines as a *starting* corpus.

---

## Step 2 — Curate & clean the audio

Piper is picky about audio. Bad clips actively hurt the model.

Use [Audacity](https://www.audacityteam.org):

1. **Listen to every clip.** Keep only lines that are:
   - pure BT, no music/beeps/gunfire bleeding in,
   - complete sentences, no hard cuts mid-word,
   - roughly 0.5–10 s long.
2. **Trim leading/trailing silence** (Effect → Truncate Silence, or manual).
3. **Light denoise** (Effect → Noise Reduction) if there is hiss — do not overdo it.
4. **Normalize** (Effect → Normalize) to about −3 dB.
5. **Export**: 16-bit, **22050 Hz, mono, WAV** — the Piper target. Use simple
   unique filenames, e.g. `0001.wav`, `0002.wav`, ...

Concatenate/split clips so each WAV contains exactly one spoken sentence.

> Optional quality booster: BT's German set is small and gamey. Many people
> first clone the voice with a **zero-shot cloner** (e.g. `jamiepine/voicebox`
> / Qwen-TTS, or `resemble-ai/chatterbox`) using a few clean German BT lines,
> then synthesize 300–1500 clean German sentences to bulk up the fine-tune
> dataset. This gives Piper much cleaner training data than the raw game audio.

---

## Step 3 — Transcribe every clip

Create `dataset/metadata.csv` (pipe-delimited, **no header**):

```
id|text
0001|Copy that, Pilot.
0002|Hostile detected at your position.
```

Rules for the text column:

- Write **normal German orthography**; espeak-ng handles phonemization.
- **Spell out numbers** ("drei" not "3"), **expand abbreviations**
  ("z. B." → "zum Beispiel").
- Keep the text to exactly what is spoken in the clip.
- Transcribe by ear, or use a Whisper model (`whisper de`) for a first pass and
  then hand-fix.

Final structure:

```
dataset/
├── metadata.csv
└── wav/
    ├── 0001.wav
    ├── 0002.wav
    └── ...
```

---

## Step 4 — Set up the training environment

Use the maintained Piper repo: **OHF-Voice/piper1-gpl**.

```bash
git clone https://github.com/OHF-Voice/piper1-gpl.git
cd piper1-gpl

# system packages (Debian/Ubuntu)
sudo apt-get install build-essential cmake ninja-build espeak-ng

python3 -m venv .venv
source .venv/bin/activate
python3 -m pip install -e '.[train]'

# build the Cython monotonic-align + in-place extensions
./build_monotonic_align.sh
python3 setup.py build_ext --inplace
```

If you are on Windows, run all of this inside **WSL2** (or use Docker — below).

### Docker route (recommended)

```dockerfile
FROM nvcr.io/nvidia/pytorch:23.06-py3
RUN pip3 install 'pytorch-lightning'
ENV NUMBA_CACHE_DIR=.numba_cache
```

```bash
docker build -t pipertrain .
docker run --rm --gpus all pipertrain nvidia-smi   # verify GPU works
```

---

## Step 5 — Download a base checkpoint

Fine-tune (not from scratch). Get a **German** checkpoint:

- https://huggingface.co/datasets/rhasspy/piper-checkpoints
  → `de/de_DE/thorsten/...`

Download the `.ckpt` file (prefer `medium`; if only `high` is offered, match it
with `--quality high` later). This is your warm-start weights.

---

## Step 6 — Train

```bash
python3 -m piper.train fit \
  --data.voice_name "BT7274_DE" \
  --data.csv_path /path/to/dataset/metadata.csv \
  --data.audio_dir  /path/to/dataset/wav/ \
  --model.sample_rate 22050 \
  --data.espeak_voice de \
  --data.cache_dir /path/to/cache/ \
  --data.config_path /path/to/config.json \
  --data.batch_size 16 \
  --ckpt_path /path/to/de_DE-thorsten-medium.ckpt
```

Tuning knobs:

- `--data.batch_size`: 32 needs ~24 GB VRAM. Use 16 for 16 GB, 8 for 8 GB.
- Long sentences blow up VRAM — see `python3 -m piper.train fit --help` for
  dropping long utterances (max phoneme ids).
- Monitor with TensorBoard:
  ```bash
  tensorboard --logdir /path/to/cache/lightning_logs
  ```
  Train until `loss_disc_all` levels off (a few hundred epochs on a fine-tune).

---

## Step 7 — Export to ONNX

```bash
python3 -m piper.train.export_onnx \
  --checkpoint /path/to/cache/lightning_logs/version_0/checkpoints/<best>.ckpt \
  --output-file BT7274_DE.onnx

# config was written during training
cp /path/to/config.json BT7274_DE.onnx.json
```

Optional: BT talks slowly and calmly. For the plain `piper` CLI you can raise
`inference.length_scale` in `BT7274_DE.onnx.json` (e.g. 1.1–1.3) and lower
`inference.noise_scale` slightly for a less "breathy" robotic feel. **HERMES
ignores that block**, though: it always passes `--length_scale`,
`--noise_scale` and `--noise_w` on the command line. In HERMES the tuning lives
on the voice's `builtinVoices` line in `internal/hermes/tts.go` (`lengthScale`, `noiseScale`,
`noiseW`); the base length scale is multiplied with the user's speed setting
(`length_scale = lengthScale / speed`).

Sanity test with the same Piper binary the project bundles:

```bash
echo "Verstanden, Pilot." | piper -m BT7274_DE.onnx --config BT7274_DE.onnx.json --output_file test.wav
```

---

## Step 8 — Drop it into this HERMES project

> The WAV-to-PIPER-script kit's `install_to_hermes.sh` does steps 1–4 for you. The manual steps are below.

1. Put the two files in their own folder (folder name = file name = voice key):
   ```bash
   mkdir -p voices/BT7274_DE
   cp BT7274_DE.onnx BT7274_DE.onnx.json voices/BT7274_DE/
   ```
2. Register the voice: add one line to `builtinVoices` in `internal/hermes/tts.go`. This is the
   only voice table in the code; the allow-list (`piperVoices`) is built from it.
   ```go
   {Key: "BT7274_DE", Name: "BT-7274", Lang: "de", model: "BT7274_DE/BT7274_DE.onnx"},
   ```
   Optional tuning on the same line: `lengthScale` (>1 is slower), `noiseScale`,
   `noiseW`, and `pitch` (e.g. 0.9 is lower). A trained BT voice should
   already sound like BT, so start without any and only add what it needs.
   `Lang: "de"` is what makes HERMES treat it as German: German persona and
   replies, German small talk, wake word answer "Ja?", German speech
   normalisation (units, numbers) and German speech recognition in the browser.
3. Add it to `voices/voices.json` with `"language": {"code": "de_DE", "family": "de", ...}`
   and the two file names (`BT7274_DE/BT7274_DE.onnx`, `BT7274_DE/BT7274_DE.onnx.json`)
   with their sizes. Copy the `JarvisDE` entry as a template.
   `go test ./...` (`TestBundledVoiceCatalogue`) fails if the entry is missing,
   the language disagrees with `builtinVoices` or a size is wrong.
4. Optional: add a fallback option to the `s_pipervoice` select in
   `web/static/index.html`. The settings panel replaces this list with the one from
   `/api/status` as soon as it loads, so this only matters for the first moment
   after a page load:
   ```html
   <option value="BT7274_DE" data-lang="de">BT-7274 (DE)</option>
   ```
5. Verify the container's bundled Piper has **German espeak-ng data** so the
   `de` phonemes in the config exist:
   ```bash
   docker exec bt-chatbot ls /opt/piper/espeak-ng-data/de_dict
   ```
   The stock Piper release ships all espeak-ng languages, so it should already
   be there.
6. Rebuild:
   ```bash
   docker compose up -d --build
   ```
7. Pick the voice: ⚙ Settings → HERMES (local Piper) → **BT-7274 (DE)** →
   *Test current voice*, then **Save**.

---

## Expected time

| Step        | Time                          |
| ----------- | ----------------------------- |
| Extract     | 1–2 h (mostly curation)       |
| Clean+trim  | 2–4 h                         |
| Transcribe  | 1–2 h (faster with Whisper)   |
| Training    | 2–8 h on GPU (fine-tune)      |

Total: roughly one weekend. The dominant cost is the audio curation, not the
GPU training.
