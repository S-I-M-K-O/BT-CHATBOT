package main

import (
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

// piperVoices maps a voice key to its bundled Piper model inside VOICE_DIR.
// Only these keys are accepted (no path traversal).
var piperVoices = map[string]string{
	"BT7274":   "BT7274/BT7274.onnx",
	"ScorchAI": "ScorchAI/ScorchAI.onnx",
	"PDA":      "PDA/PDA.onnx",
	"JarvisDE": "JarvisDE/JarvisDE.onnx",
}

type voiceInfo struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Lang string `json:"lang"`
}

// builtinVoices is the display order and friendly naming of piperVoices.
var builtinVoices = []voiceInfo{
	{Key: "BT7274", Name: "BT-7274", Lang: "en"},
	{Key: "ScorchAI", Name: "Scorch AI", Lang: "en"},
	{Key: "PDA", Name: "Subnautica PDA", Lang: "en"},
	{Key: "JarvisDE", Name: "JARVIS (German)", Lang: "de"},
}

func isGermanVoice(voice string) bool { return voice == "JarvisDE" }

func voiceLang(voice string) string {
	if isGermanVoice(voice) {
		return "de"
	}
	return "en"
}

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
// multiplier (length_scale = 1/speed).
func piperTTS(ctx context.Context, text, voice string, speed float64) ([]byte, error) {
	bin, err := exec.LookPath(piperBin())
	if err != nil {
		return nil, errPiperMissing
	}
	modelFile, ok := piperVoices[voice]
	if !ok {
		modelFile = piperVoices[defaultPiperVoice]
	}
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

	cmd := exec.CommandContext(ctx, bin,
		"--model", model,
		"--config", model+".json",
		"--output_file", outFile,
		"--length_scale", strconv.FormatFloat(1/speed, 'f', 3, 64),
		"--noise_scale", "0.6",
		"--noise_w", "0.8",
		"--sentence_silence", "0.25",
	)
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
	return audio, nil
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

	key := voice + "|" + formatSpeed(speed) + "|" + text
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
		writeJSONError(w, http.StatusInternalServerError, "speech synthesis failed: "+err.Error())
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
