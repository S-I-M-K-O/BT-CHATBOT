package hermes

import (
	"bytes"
	"container/list"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Voices

type voiceInfo struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Lang string `json:"lang"` // two-letter family, e.g. "en" or "de"

	model string // model path inside VOICE_DIR

	// Optional per-voice synthesis tuning. Piper's command-line flags override
	// the inference block of the model's .onnx.json, so these are what count.
	// Zero means the shared default (see piperTuning).
	lengthScale float64 // base length scale, multiplied by 1/speed (>1 = slower)
	noiseScale  float64
	noiseW      float64

	// pitch lowers (or raises) the voice by playing the audio back at a
	// different rate: Piper synthesises with lengthScale*pitch and the WAV
	// header's sample rate is rewritten to rate*pitch, so pitch drops while
	// the pace stays as tuned. 0 or 1 means off; useful range ~0.7-1.0.
	pitch float64
	// speaker selects a speaker of a multi-speaker model (Piper --speaker N).
	// nil or negative means none (single-speaker models).
	speaker *int
}

// speakerID is a helper for builtinVoices literals: speaker: speakerID(3).
func speakerID(n int) *int { return &n }

// Shared Piper synthesis defaults for voices without their own tuning.
const (
	defaultLengthScale = 1.0
	defaultNoiseScale  = 0.6
	defaultNoiseW      = 0.8
)

// builtinVoices is the single list of bundled Piper voices, in display order.
// To add a voice, drop its folder into voices/ and add one line here (plus an
// entry in voices/voices.json). Lang is the fallback when voices.json does not
// list the voice; it drives the reply language, persona, small talk and speech
// normalisation, so a German model must say "de".
var builtinVoices = []voiceInfo{
	{Key: "BT7274", Name: "BT-7274", Lang: "en", model: "BT7274/BT7274.onnx"},
	{Key: "ScorchAI", Name: "Scorch AI", Lang: "en", model: "ScorchAI/ScorchAI.onnx"},
	{Key: "PDA", Name: "Subnautica PDA", Lang: "en", model: "PDA/PDA.onnx"},
	{Key: "JarvisDE", Name: "JARVIS", Lang: "de", model: "JarvisDE/JarvisDE.onnx"},
}

// piperVoices maps a voice key to its bundled Piper model inside VOICE_DIR.
// Only these keys are accepted (no path traversal).
var piperVoices = func() map[string]string {
	m := make(map[string]string, len(builtinVoices))
	for _, v := range builtinVoices {
		m[v.Key] = v.model
	}
	return m
}()

// voiceLang is the two-letter language of a Piper voice ("en" for unknown
// keys), taken from voices.json with the built-in table as fallback.
func voiceLang(voice string) string {
	for _, v := range listVoices() {
		if v.Key == voice {
			return v.Lang
		}
	}
	return "en"
}

// Accepted range for voiceInfo.pitch; values outside it are ignored (off).
const (
	minVoicePitch = 0.5
	maxVoicePitch = 1.5
)

// piperParams is everything about a voice that shapes Piper's output, beyond
// the text and the model file.
type piperParams struct {
	lengthScale float64 // already divided by speed and multiplied by pitch
	noiseScale  float64
	noiseW      float64
	pitch       float64 // 1 = off
	speaker     int     // -1 = none
}

// cacheKey renders the parameters with the precision passed to Piper, so
// two requests share a cache entry exactly when they produce the same audio.
func (p piperParams) cacheKey() string {
	return fmt.Sprintf("l%.3f|n%.3f|w%.3f|p%.4f|s%d", p.lengthScale, p.noiseScale, p.noiseW, p.pitch, p.speaker)
}

// voiceParams returns the Piper parameters for a voice at the given speed
// multiplier: the voice's base length scale divided by speed (and multiplied
// by its pitch, so lowering the pitch keeps the pace), its noise settings,
// pitch and speaker (shared defaults for unset fields and unknown voices).
func voiceParams(voice string, speed float64) piperParams {
	p := piperParams{
		lengthScale: defaultLengthScale, noiseScale: defaultNoiseScale, noiseW: defaultNoiseW,
		pitch: 1, speaker: -1,
	}
	for _, v := range builtinVoices {
		if v.Key != voice {
			continue
		}
		if v.lengthScale > 0 {
			p.lengthScale = v.lengthScale
		}
		if v.noiseScale > 0 {
			p.noiseScale = v.noiseScale
		}
		if v.noiseW > 0 {
			p.noiseW = v.noiseW
		}
		if v.pitch >= minVoicePitch && v.pitch <= maxVoicePitch {
			p.pitch = v.pitch
		}
		if v.speaker != nil && *v.speaker >= 0 {
			p.speaker = *v.speaker
		}
		break
	}
	p.lengthScale = p.lengthScale / speed * p.pitch
	return p
}

// piperTuning returns the length scale, noise scale and noise width passed to
// Piper for a voice at the given speed multiplier (see voiceParams).
func piperTuning(voice string, speed float64) (lengthScale, noiseScale, noiseW float64) {
	p := voiceParams(voice, speed)
	return p.lengthScale, p.noiseScale, p.noiseW
}

func isGermanVoice(voice string) bool { return voiceLang(voice) == "de" }

// listVoices returns the selectable voices. Languages are taken from
// VOICE_DIR/voices.json when present (Piper's catalogue format), falling back
// to the built-in table.
func listVoices() []voiceInfo {
	out := make([]voiceInfo, len(builtinVoices))
	copy(out, builtinVoices)
	b, err := os.ReadFile(filepath.Join(envOr("VOICE_DIR", "voices"), "voices.json"))
	if err != nil {
		return out
	}
	var catalogue map[string]struct {
		Language struct {
			Family string `json:"family"`
			Code   string `json:"code"`
		} `json:"language"`
	}
	if err := json.Unmarshal(b, &catalogue); err != nil {
		log.Printf("voices.json parse error: %v", err)
		return out
	}
	for i, v := range out {
		c, ok := catalogue[v.Key]
		if !ok {
			continue
		}
		lang := c.Language.Family
		if lang == "" && len(c.Language.Code) >= 2 {
			lang = c.Language.Code[:2]
		}
		if lang != "" {
			out[i].Lang = strings.ToLower(lang)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Text normalisation for speech

const maxSpeechRunes = 2000

var (
	reCodeFence  = regexp.MustCompile("(?s)```.*?(```|$)")
	reMdImage    = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	reMdLink     = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	reURL        = regexp.MustCompile(`(?i)\b(?:https?://|www\.)[^\s<>()\[\]]+`)
	reHTMLTag    = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)
	reHeading    = regexp.MustCompile(`^\s{0,3}#{1,6}\s*`)
	reQuote      = regexp.MustCompile(`^\s*>+\s?`)
	reBullet     = regexp.MustCompile(`^\s*[-*+•]\s+`)
	reRule       = regexp.MustCompile(`^\s*([-*_=]\s*){3,}$`)
	reEmphasis   = regexp.MustCompile("[*~`]+")
	reUnderscore = regexp.MustCompile(`_+`)
	reCelsius    = regexp.MustCompile(`\s*°\s*C\b`)
	reFahrenheit = regexp.MustCompile(`\s*°\s*F\b`)
	reDegree     = regexp.MustCompile(`\s*°`)
	rePercent    = regexp.MustCompile(`\s*%`)
	reAmp        = regexp.MustCompile(`\s*&\s*`)
	reKmh        = regexp.MustCompile(`(?i)\s*km/h\b`)
	reSpaces     = regexp.MustCompile(`\s+`)
	reSpacePunct = regexp.MustCompile(`\s+([.,!?;:])`)
)

type speechWords struct {
	link, celsius, fahrenheit, degrees, percent, and, kmh, code string
}

var speechEN = speechWords{"link", " degrees Celsius", " degrees Fahrenheit", " degrees", " percent", " and ", " kilometers per hour", " See the code on screen. "}
var speechDE = speechWords{"Link", " Grad Celsius", " Grad Fahrenheit", " Grad", " Prozent", " und ", " Kilometer pro Stunde", " Siehe Code auf dem Bildschirm. "}

// isEmojiRune reports runes that Piper would either skip or read as noise.
func isEmojiRune(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FAFF: // emoticons, pictographs, flags, ...
		return true
	case r >= 0x2600 && r <= 0x27BF: // misc symbols, dingbats
		return true
	case r >= 0x2B00 && r <= 0x2BFF: // arrows, stars
		return true
	case r >= 0xE0020 && r <= 0xE007F: // tag sequences
		return true
	case r == 0xFE0F || r == 0xFE0E || r == 0x200D || r == 0x20E3:
		return true
	}
	return false
}

// normalizeForSpeech turns chat text (Markdown, URLs, emoji, units) into plain
// sentences a TTS voice can read naturally. lang is "de" for German.
func normalizeForSpeech(text, lang string) string {
	words := speechEN
	if lang == "de" {
		words = speechDE
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = reCodeFence.ReplaceAllString(text, "\n"+strings.TrimSpace(words.code)+"\n")
	text = reMdImage.ReplaceAllString(text, "$1")
	text = reMdLink.ReplaceAllString(text, "$1")
	text = reURL.ReplaceAllString(text, words.link)
	text = reHTMLTag.ReplaceAllString(text, " ")
	text = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ").Replace(text)

	// Per line: strip block markup and end unpunctuated lines (list items,
	// headings) with a full stop so they are not read as one run-on sentence.
	var parts []string
	for _, line := range strings.Split(text, "\n") {
		if reRule.MatchString(line) {
			continue
		}
		line = reHeading.ReplaceAllString(line, "")
		line = reQuote.ReplaceAllString(line, "")
		line = reBullet.ReplaceAllString(line, "")
		line = strings.ReplaceAll(line, "|", " ")
		line = strings.TrimSpace(line)
		if line == "" || strings.Trim(line, "-: \t") == "" { // blank or table separator
			continue
		}
		parts = append(parts, line)
	}
	for i, p := range parts {
		if i == len(parts)-1 {
			break
		}
		last, _ := utf8.DecodeLastRuneInString(strings.TrimRight(p, "*_~`)\"' "))
		if !strings.ContainsRune(".!?:;,…", last) {
			parts[i] = p + "."
		}
	}
	text = strings.Join(parts, " ")

	text = reEmphasis.ReplaceAllString(text, "")
	text = reUnderscore.ReplaceAllString(text, " ")
	text = reCelsius.ReplaceAllString(text, words.celsius)
	text = reFahrenheit.ReplaceAllString(text, words.fahrenheit)
	text = reDegree.ReplaceAllString(text, words.degrees)
	text = rePercent.ReplaceAllString(text, words.percent)
	text = reAmp.ReplaceAllString(text, words.and)
	text = reKmh.ReplaceAllString(text, words.kmh)
	text = strings.Map(func(r rune) rune {
		if isEmojiRune(r) {
			return -1
		}
		return r
	}, text)
	text = reSpaces.ReplaceAllString(text, " ")
	text = reSpacePunct.ReplaceAllString(text, "$1")
	text = strings.TrimSpace(text)
	return truncateSpeech(text, maxSpeechRunes)
}

// truncateSpeech cuts text to at most n runes, preferring a sentence end and
// otherwise a word boundary.
func truncateSpeech(text string, n int) string {
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	cut := string([]rune(text)[:n])
	if i := strings.LastIndexAny(cut, ".!?"); i > len(cut)/2 {
		return cut[:i+1]
	}
	if i := strings.LastIndex(cut, " "); i > 0 {
		return cut[:i]
	}
	return cut
}

// ---------------------------------------------------------------------------
// TTS cache with request de-duplication

// ttsCache is a concurrency-safe LRU bounded by entry count and total bytes.
type ttsCache struct {
	mu         sync.Mutex
	maxEntries int
	maxBytes   int
	bytes      int
	ll         *list.List
	items      map[string]*list.Element
}

type ttsCacheEntry struct {
	key  string
	data []byte
}

func newTTSCache(maxEntries, maxBytes int) *ttsCache {
	return &ttsCache{maxEntries: maxEntries, maxBytes: maxBytes, ll: list.New(), items: map[string]*list.Element{}}
}

func (c *ttsCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*ttsCacheEntry).data, true
	}
	return nil, false
}

func (c *ttsCache) Add(key string, data []byte) {
	if len(data) > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		e := el.Value.(*ttsCacheEntry)
		c.bytes += len(data) - len(e.data)
		e.data = data
		c.ll.MoveToFront(el)
	} else {
		c.items[key] = c.ll.PushFront(&ttsCacheEntry{key: key, data: data})
		c.bytes += len(data)
	}
	for c.ll.Len() > c.maxEntries || c.bytes > c.maxBytes {
		el := c.ll.Back()
		if el == nil {
			break
		}
		e := el.Value.(*ttsCacheEntry)
		c.ll.Remove(el)
		delete(c.items, e.key)
		c.bytes -= len(e.data)
	}
}

func (c *ttsCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

func (c *ttsCache) Bytes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

// flightGroup de-duplicates concurrent calls with the same key (a tiny
// stdlib-only singleflight).
type flightGroup struct {
	mu    sync.Mutex
	calls map[string]*flightCall
}

type flightCall struct {
	done    chan struct{}
	val     []byte
	err     error
	waiters int                // callers still interested in the result
	cancel  context.CancelFunc // cancels the run once nobody is waiting
}

// Do runs fn once per key at a time. Callers that arrive while a call is in
// flight wait for its result, or give up when ctx is done. When the LAST
// interested caller gives up, the run's context is cancelled so an abandoned
// Piper process is killed instead of holding a piper slot. shared reports
// whether the result came from another caller's run.
func (g *flightGroup) Do(ctx context.Context, key string, fn func(context.Context) ([]byte, error)) (val []byte, err error, shared bool) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = map[string]*flightCall{}
	}
	c, ok := g.calls[key]
	if ok {
		c.waiters++
	} else {
		runCtx, cancel := context.WithCancel(context.Background())
		c = &flightCall{done: make(chan struct{}), waiters: 1, cancel: cancel}
		g.calls[key] = c
		go func() {
			defer func() {
				if p := recover(); p != nil {
					c.err = fmt.Errorf("tts panic: %v", p)
				}
				g.mu.Lock()
				if g.calls[key] == c {
					delete(g.calls, key)
				}
				g.mu.Unlock()
				cancel()
				close(c.done)
			}()
			c.val, c.err = fn(runCtx)
		}()
	}
	g.mu.Unlock()

	select {
	case <-c.done:
		return c.val, c.err, ok
	case <-ctx.Done():
		g.mu.Lock()
		c.waiters--
		if c.waiters == 0 {
			// Nobody wants this result any more: stop the work and let a
			// later identical request start fresh.
			if g.calls[key] == c {
				delete(g.calls, key)
			}
			c.cancel()
		}
		g.mu.Unlock()
		return nil, ctx.Err(), ok
	}
}

var (
	ttsAudioCache = newTTSCache(128, 48<<20)
	ttsFlights    flightGroup
	// piperSlots limits concurrent Piper processes; streamed speech sends one
	// request per sentence and must not fork a process per sentence at once.
	piperSlots = make(chan struct{}, 2)
)

const piperTimeout = 60 * time.Second

// errPiperMissing means the Piper binary is not installed / not on PATH.
var errPiperMissing = errors.New("piper binary not found")

func piperBin() string { return envOr("PIPER_BIN", "piper") }

func piperAvailable() bool {
	_, err := exec.LookPath(piperBin())
	return err == nil
}

func formatSpeed(speed float64) string { return strconv.FormatFloat(speed, 'f', 2, 64) }

// piperTTS synthesises already-normalised text with the given voice and speed
// multiplier (length_scale = the voice's base length scale / speed).
func piperTTS(ctx context.Context, text, voice string, speed float64) ([]byte, error) {
	bin, err := exec.LookPath(piperBin())
	if err != nil {
		return nil, errPiperMissing
	}
	modelFile, ok := piperVoices[voice]
	if !ok {
		voice = defaultPiperVoice
		modelFile = piperVoices[voice]
	}
	params := voiceParams(voice, speed)
	model := filepath.Join(envOr("VOICE_DIR", "voices"), modelFile)
	if _, err := os.Stat(model); err != nil {
		return nil, fmt.Errorf("voice model missing: %s", model)
	}

	select {
	case piperSlots <- struct{}{}:
		defer func() { <-piperSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	tmp, err := os.CreateTemp("", "bt_voice_*.wav")
	if err != nil {
		return nil, fmt.Errorf("create temp output: %w", err)
	}
	outFile := tmp.Name()
	tmp.Close()
	defer os.Remove(outFile)

	args := []string{
		"--model", model,
		"--config", model + ".json",
		"--output_file", outFile,
		"--length_scale", strconv.FormatFloat(params.lengthScale, 'f', 3, 64),
		"--noise_scale", strconv.FormatFloat(params.noiseScale, 'f', 3, 64),
		"--noise_w", strconv.FormatFloat(params.noiseW, 'f', 3, 64),
		// Scaled by pitch like the length scale, so the pauses also keep their
		// length once the sample rate is relabelled ("0.25" when pitch is off).
		"--sentence_silence", strconv.FormatFloat(math.Round(250*params.pitch)/1000, 'f', -1, 64),
	}
	if params.speaker >= 0 {
		args = append(args, "--speaker", strconv.Itoa(params.speaker))
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = strings.NewReader(text + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[len(msg)-500:]
		}
		return nil, fmt.Errorf("piper failed: %v: %s", err, msg)
	}
	audio, err := os.ReadFile(outFile)
	if err != nil {
		return nil, fmt.Errorf("read piper output: %w", err)
	}
	if len(audio) < 44 || string(audio[:4]) != "RIFF" {
		return nil, errors.New("piper produced no valid audio")
	}
	if params.pitch != 1 {
		audio = applyWAVPitch(audio, params.pitch)
	}
	return audio, nil
}

var wavPitchWarn sync.Once

// applyWAVPitch shifts the pitch of a WAV by relabelling its sample rate as
// round(rate*pitch) (the samples are untouched, so playback is slower/lower
// for pitch < 1). If the header cannot be parsed the audio is returned as is.
func applyWAVPitch(audio []byte, pitch float64) []byte {
	out, err := rewriteWAVSampleRate(audio, func(rate uint32) uint32 {
		return uint32(math.Round(float64(rate) * pitch))
	})
	if err != nil {
		wavPitchWarn.Do(func() { log.Printf("tts: pitch shift skipped: %v", err) })
		return audio
	}
	return out
}

// rewriteWAVSampleRate returns a copy of a RIFF/WAVE file whose 'fmt ' chunk
// has its sample rate replaced by newRate(old) and its byte rate recomputed
// (rate * channels * bits/8). Chunks are walked properly, so extra chunks
// before 'fmt ' (LIST, JUNK, ...) and extended fmt chunks are handled.
func rewriteWAVSampleRate(wav []byte, newRate func(uint32) uint32) ([]byte, error) {
	if len(wav) < 12 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF/WAVE file")
	}
	for off := 12; off+8 <= len(wav); {
		id := string(wav[off : off+4])
		size := int(binary.LittleEndian.Uint32(wav[off+4 : off+8]))
		body := off + 8
		if size < 0 || size > len(wav)-body {
			return nil, fmt.Errorf("chunk %q overruns the file", id)
		}
		if id != "fmt " {
			off = body + size + size%2 // chunks are padded to even sizes
			continue
		}
		if size < 16 {
			return nil, fmt.Errorf("fmt chunk too short (%d bytes)", size)
		}
		channels := uint32(binary.LittleEndian.Uint16(wav[body+2:]))
		rate := binary.LittleEndian.Uint32(wav[body+4:])
		bits := uint32(binary.LittleEndian.Uint16(wav[body+14:]))
		if channels == 0 || bits == 0 || rate == 0 {
			return nil, errors.New("fmt chunk has zero channels, rate or bit depth")
		}
		r := newRate(rate)
		if r == 0 {
			return nil, errors.New("new sample rate is zero")
		}
		out := append([]byte(nil), wav...)
		binary.LittleEndian.PutUint32(out[body+4:], r)
		binary.LittleEndian.PutUint32(out[body+8:], r*channels*((bits+7)/8))
		return out, nil
	}
	return nil, errors.New("no fmt chunk")
}

type ttsRequest struct {
	Text   string   `json:"text"`
	Voice  string   `json:"voice"`
	Engine string   `json:"engine"`
	Speed  *float64 `json:"speed"`
}

func handleTTS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req ttsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		writeJSONError(w, http.StatusBadRequest, "bad request: text is required")
		return
	}

	s := loadSettings()
	engine := req.Engine
	if engine == "" {
		engine = s.VoiceMode
	}
	if engine == "browser" {
		writeJSONError(w, http.StatusBadRequest, "browser engine is handled client-side")
		return
	}
	voice := req.Voice
	if _, ok := piperVoices[voice]; !ok {
		voice = s.PiperVoice
	}
	speed := s.VoiceSpeed
	if req.Speed != nil && *req.Speed > 0 {
		speed = *req.Speed
	}
	speed = clamp(finiteOr(speed, 1), 0.5, 2)
	speed, _ = strconv.ParseFloat(formatSpeed(speed), 64) // same precision as the cache key

	text := normalizeForSpeech(req.Text, voiceLang(voice))
	if text == "" {
		writeJSONError(w, http.StatusBadRequest, "nothing to speak")
		return
	}
	if !piperAvailable() {
		writeJSONError(w, http.StatusServiceUnavailable, "Piper is not installed. Set PIPER_BIN or switch the voice engine to browser.")
		return
	}

	// The key covers everything that shapes the audio: voice (model), speed
	// and the voice's tuning (length/noise, pitch, speaker), then the text.
	key := voice + "|" + formatSpeed(speed) + "|" + voiceParams(voice, speed).cacheKey() + "|" + text
	if audio, ok := ttsAudioCache.Get(key); ok {
		writeWAV(w, audio, "hit")
		return
	}
	audio, err, _ := ttsFlights.Do(r.Context(), key, func(runCtx context.Context) ([]byte, error) {
		// runCtx outlives any single request (others may share the run) but is
		// cancelled once every waiting client has gone away.
		ctx, cancel := context.WithTimeout(runCtx, piperTimeout)
		defer cancel()
		a, err := piperTTS(ctx, text, voice, speed)
		if err == nil {
			ttsAudioCache.Add(key, a)
		}
		return a, err
	})
	if err != nil {
		if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
			return // client went away
		}
		if errors.Is(err, errPiperMissing) {
			writeJSONError(w, http.StatusServiceUnavailable, "Piper is not installed. Set PIPER_BIN or switch the voice engine to browser.")
			return
		}
		log.Printf("tts error: %v", err)
		// Piper's stderr and the model paths stay in the server log.
		writeJSONError(w, http.StatusInternalServerError, "speech synthesis failed")
		return
	}
	writeWAV(w, audio, "miss")
}

func writeWAV(w http.ResponseWriter, audio []byte, cache string) {
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Length", strconv.Itoa(len(audio)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-TTS-Cache", cache)
	_, _ = w.Write(audio)
}
