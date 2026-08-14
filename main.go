package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	settingsMu sync.Mutex
)

type Settings struct {
	SystemPrompt string  `json:"systemPrompt"`
	Temperature  float64 `json:"temperature"`
	MaxTokens    int     `json:"maxTokens"`
	Voice        string  `json:"voice"`
	VoiceMode    string  `json:"voiceMode"`
	Pitch        float64 `json:"pitch"`
	Rate         float64 `json:"rate"`
	Volume       float64 `json:"volume"`
	Speak        bool    `json:"speak"`
	Call         bool    `json:"call"`
	LocalModel   string  `json:"localModel"`
	EyeDesign    string  `json:"eyeDesign"`
	PiperVoice   string  `json:"piperVoice"`
}

func isGermanVoice(voice string) bool { return voice == "JarvisDE" }

const englishDefaultPrompt = "You are HERMES, a voice assistant with a calm, measured, slightly robotic monotone. Keep your answers concise and tactical. Use phrases like 'Copy that' and 'Understood' sparingly. Be precise, helpful, and a little dry. You never mention being an AI language model."

const germanDefaultPrompt = "Du bist HERMES, ein Sprachassistent mit ruhiger, gemessener, leicht roboterhafter Monotonie. Halte deine Antworten kurz und taktisch. Verwende Ausdrücke wie 'Verstanden' und 'Kopiert' sparsam. Sei präzise, hilfreich und ein wenig trocken. Erwähne niemals, dass du ein KI-Sprachmodell bist."

// effectiveSystemPrompt picks the language-appropriate HERMES persona. A
// hand-written custom prompt is always respected; the built-in English persona
// is swapped for the German one when a German voice is selected.
func effectiveSystemPrompt(s *Settings) string {
	if isGermanVoice(s.PiperVoice) && (s.SystemPrompt == "" || strings.Contains(s.SystemPrompt, "You are HERMES")) {
		return germanDefaultPrompt
	}
	if s.SystemPrompt == "" {
		return englishDefaultPrompt
	}
	return s.SystemPrompt
}

// phrases holds the spoken/displayed HERMES voicelines in one language.
type phrases struct {
	neuralOffline   string
	localModelErr   string // %d status
	satelliteDown   string
	noData          string
	imageFabricator string
	imageSynthesis  string
	imageEmpty      string
	imageRetrieve   string
	imageStore      string
	searchNoSubject string
	searchEmpty     string
	searchStore     string
	searchDownload  string
	searchFound     string // %d pictures, %s subject
}

var phrasesEN = phrases{
	neuralOffline:   "My local neural core is offline. Check that Ollama is running and pull qwen3.5:latest.",
	localModelErr:   "Local model error (%d). Run: ollama pull qwen3.5:latest",
	satelliteDown:   "My satellite link is down. Mistral could not be reached — check that MISTRAL_API_KEY is set.",
	noData:          "No data recovered from the satellite. Try again.",
	imageFabricator: "My image fabricator is offline. Check the Mistral API key.",
	imageSynthesis:  "Image synthesis failed. ",
	imageEmpty:      "The image came back empty. Try a more specific request.",
	imageRetrieve:   "Could not retrieve the generated image.",
	imageStore:      "Could not store the generated image.",
	searchNoSubject: "What should I search for?",
	searchEmpty:     "No pictures found on the network. Try a different subject.",
	searchStore:     "Could not store the pictures.",
	searchDownload:  "Found pictures but could not download them.",
	searchFound:     "Found %d pictures of %s.",
}

var phrasesDE = phrases{
	neuralOffline:   "Meine lokale Recheneinheit ist offline. Prüfe, ob Ollama läuft, und ziehe qwen3.5:latest.",
	localModelErr:   "Lokales Modell-Fehler (%d). Ausführen: ollama pull qwen3.5:latest",
	satelliteDown:   "Meine Satellitenverbindung ist unterbrochen. Mistral konnte nicht erreicht werden — prüfe, ob MISTRAL_API_KEY gesetzt ist.",
	noData:          "Keine Daten vom Satelliten erhalten. Versuch es erneut.",
	imageFabricator: "Meine Bild-Fabrik ist offline. Prüfe den Mistral-API-Schlüssel.",
	imageSynthesis:  "Die Bildsynthese ist fehlgeschlagen. ",
	imageEmpty:      "Das Bild kam leer zurück. Versuch eine genauere Anfrage.",
	imageRetrieve:   "Das erzeugte Bild konnte nicht abgerufen werden.",
	imageStore:      "Das erzeugte Bild konnte nicht gespeichert werden.",
	searchNoSubject: "Wonach soll ich suchen?",
	searchEmpty:     "Keine Bilder im Netzwerk gefunden. Versuch ein anderes Motiv.",
	searchStore:     "Die Bilder konnten nicht gespeichert werden.",
	searchDownload:  "Bilder gefunden, aber nicht herunterladbar.",
	searchFound:     "Gefunden: %d Bilder von %s.",
}

func phrasesFor(voice string) phrases {
	if isGermanVoice(voice) {
		return phrasesDE
	}
	return phrasesEN
}

func defaultSettings() *Settings {
	return &Settings{
		SystemPrompt: englishDefaultPrompt,
		Temperature:  0.7,
		MaxTokens:    1024,
		Voice:        "",
		VoiceMode:    "piper",
		Pitch:        0.6,
		Rate:         -0.2,
		Volume:       1.0,
		Speak:        true,
		Call:         true,
		LocalModel:   "qwen3.5:latest",
		EyeDesign:    "classic",
		PiperVoice:   "BT7274",
	}
}

func loadSettings() *Settings {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	s := defaultSettings()
	path := envOr("SETTINGS_FILE", "settings.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	if err := json.Unmarshal(b, s); err != nil {
		log.Printf("settings parse error: %v", err)
	}
	return s
}

func saveSettings(s *Settings) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	path := envOr("SETTINGS_FILE", "settings.json")
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "settings_*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// cleanForTTS strips Markdown markers (emphasis, headings) so the Piper voice
// does not read them out loud ("asterisk", "hash", ...).
var (
	ttsEmphasis = regexp.MustCompile(`[*_~` + "`" + `]`)
	ttsHeading  = regexp.MustCompile(`(?m)^#+\s*`)
)

func cleanForTTS(s string) string {
	return strings.TrimSpace(ttsHeading.ReplaceAllString(ttsEmphasis.ReplaceAllString(s, ""), ""))
}

func loadDotEnv() {
	b, err := os.ReadFile(".env")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Messages []chatMessage `json:"messages"`
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if len(req.Messages) == 0 {
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error": "no messages"}`, http.StatusBadRequest)
		return
	}

	q := req.Messages[len(req.Messages)-1].Content

	// Local knowledge base: answer known questions without calling the model.
	if local := localAnswer(strings.ToLower(q)); local != "" {
		writeSSE(w, local)
		return
	}

	s := loadSettings()

	messages := make([]chatMessage, 0, len(req.Messages)+1)
	messages = append(messages, chatMessage{Role: "system", Content: effectiveSystemPrompt(s)})
	messages = append(messages, req.Messages...)

	// Mistral handles real-time queries (weather, time, news) and image
	// generation. Everything else stays on the local Ollama model.
	if mistralEnabled() {
		if wantsRealtime(q) {
			handleMistralTools(w, s, messages)
			return
		}
		if classifyImageRequest(q) == "generate" {
			handleMistralImage(w, s, q)
			return
		}
	}
	// Internet image search needs no API key.
	if classifyImageRequest(q) == "search" {
		handleImageSearch(w, s, q)
		return
	}

	handleLocalChat(w, s, messages)
}

func handleLocalChat(w http.ResponseWriter, s *Settings, messages []chatMessage) {
	base := envOr("OLLAMA_BASE", "http://localhost:11434")
	body, _ := json.Marshal(map[string]any{
		"model":    s.LocalModel,
		"messages": messages,
		"stream":   true,
		"think":    false,
	})

	outReq, err := http.NewRequest(http.MethodPost, base+"/api/chat", bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	outReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(outReq)
	if err != nil {
		writeSSE(w, phrasesFor(s.PiperVoice).neuralOffline)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		writeSSE(w, fmt.Sprintf(phrasesFor(s.PiperVoice).localModelErr, resp.StatusCode))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	flush := w.(http.Flusher)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var chunk struct {
			Message chatMessage `json:"message"`
			Done    bool        `json:"done"`
		}
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			continue
		}
		if chunk.Message.Content != "" {
			writeSSEJSON(w, map[string]any{"delta": chunk.Message.Content})
			flush.Flush()
		}
		if chunk.Done {
			break
		}
	}
	writeSSEJSON(w, map[string]any{"done": true})
	flush.Flush()
}

func writeSSE(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	for _, ch := range strings.Split(text, "") {
		writeSSEJSON(w, map[string]any{"delta": ch})
		w.(http.Flusher).Flush()
	}
	writeSSEJSON(w, map[string]any{"done": true})
	w.(http.Flusher).Flush()
}

func writeSSEJSON(w http.ResponseWriter, obj any) {
	b, _ := json.Marshal(obj)
	fmt.Fprintf(w, "data: %s\n\n", b)
}

func handleTTS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req struct {
		Text   string `json:"text"`
		Voice  string `json:"voice"`
		Engine string `json:"engine"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	s := loadSettings()
	engine := req.Engine
	if engine == "" {
		engine = s.VoiceMode
	}
	voice := req.Voice
	if voice == "" {
		voice = s.PiperVoice
	}

	// Local Piper voice — no API key required.
	if engine == "browser" {
		// browser engine is handled client-side; nothing to serve here
		http.Error(w, `{"error": "browser engine is handled client-side"}`, http.StatusBadRequest)
		return
	}

	audio, err := piperTTS(req.Text, voice)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Write(audio)
}

// piperVoices maps a friendly voice key to the bundled Piper model files
// inside VOICE_DIR. Each voice lives in its own subdirectory. Only these
// names are accepted (no path traversal).
var piperVoices = map[string]string{
	"BT7274":   "BT7274/BT7274.onnx",
	"ScorchAI": "ScorchAI/ScorchAI.onnx",
	"PDA":      "PDA/PDA.onnx",
	"JarvisDE": "JarvisDE/JarvisDE.onnx",
}

func piperTTS(text, voice string) ([]byte, error) {
	bin := envOr("PIPER_BIN", "piper")
	voiceDir := envOr("VOICE_DIR", "voices")
	modelFile, ok := piperVoices[voice]
	if !ok {
		modelFile = piperVoices["BT7274"]
	}
	model := filepath.Join(voiceDir, modelFile)
	config := model + ".json"
	text = cleanForTTS(text)

	tmp, err := os.CreateTemp(os.TempDir(), "bt_voice_*.wav")
	if err != nil {
		return nil, fmt.Errorf("create temp output: %v", err)
	}
	outFile := tmp.Name()
	tmp.Close()

	cmd := exec.Command(bin,
		"--model", model,
		"--config", config,
		"--output_file", outFile,
	)
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		_ = os.Remove(outFile)
		return nil, fmt.Errorf("piper failed: %v: %s", err, stderr.String())
	}

	audio, err := os.ReadFile(outFile)
	_ = os.Remove(outFile)
	if err != nil {
		return nil, fmt.Errorf("read piper output: %v", err)
	}
	return audio, nil
}

func handleGetSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(loadSettings())
}

func handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var s Settings
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.Temperature = clamp(s.Temperature, 0, 2)
	s.MaxTokens = clampInt(s.MaxTokens, 16, 8192)
	s.Pitch = clamp(s.Pitch, 0, 2)
	s.Rate = clamp(s.Rate, 0.1, 3)
	s.Volume = clamp(s.Volume, 0, 1)
	if s.LocalModel == "" {
		s.LocalModel = "qwen3.5:latest"
	}
	if s.VoiceMode != "piper" && s.VoiceMode != "browser" {
		s.VoiceMode = "piper"
	}
	if err := saveSettings(&s); err != nil {
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error": "could not save settings"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

func main() {
	loadDotEnv()
	// Local-only by default; docker-compose overrides to 0.0.0.0 so the mapped
	// port stays reachable.
	addr := envOr("ADDR", "127.0.0.1") + ":" + envOr("PORT", "8080")

	if err := os.MkdirAll(genDir(), 0o755); err != nil {
		log.Printf("could not create gen dir: %v", err)
	}
	cleanupGenDir()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat", handleChat)
	mux.HandleFunc("/api/tts", handleTTS)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleGetSettings(w, r)
		case http.MethodPost, http.MethodPut:
			handleSaveSettings(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/gen/", func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		f := filepath.Join(genDir(), name)
		fh, err := os.Open(f)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer fh.Close()
		head := make([]byte, 512)
		n, _ := fh.Read(head)
		w.Header().Set("Content-Type", http.DetectContentType(head[:n]))
		http.ServeContent(w, r, name, time.Time{}, fh)
	})
	// http.FileServer sends Last-Modified and honours If-Modified-Since, so
	// browsers get conditional 304s instead of a full re-download every visit.
	mux.Handle("/", http.FileServer(http.Dir(envOr("STATIC_DIR", "static"))))

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("HERMES online on %s (local Ollama engine)", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

// cleanupGenDir removes generated / searched images older than genMaxAge so the
// data dir does not grow forever.
const genMaxAge = 24 * time.Hour

func cleanupGenDir() {
	entries, err := os.ReadDir(genDir())
	if err != nil {
		return
	}
	now := time.Now()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > genMaxAge {
			_ = os.Remove(filepath.Join(genDir(), e.Name()))
		}
	}
}

// localAnswer returns a canned HERMES response when the message is known locally,
// so the model is only used when the bot "does not know".
func localAnswer(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))

	greeting := []string{"hi", "hello", "hey", "howdy", "greetings", "hi there", "hello there"}
	for _, g := range greeting {
		if q == g || strings.HasPrefix(q, g+" ") || strings.HasPrefix(q, g+",") || strings.HasPrefix(q, g+"!") {
			return "Hello. HERMES online. How can I assist you today?"
		}
	}

	if strings.Contains(q, "who are you") || strings.Contains(q, "your name") || strings.Contains(q, "what are you") {
		return "I am HERMES, your voice assistant."
	}
	if strings.Contains(q, "how are you") {
		return "My systems are nominal. How are you?"
	}
	if strings.Contains(q, "thank") {
		return "You are welcome."
	}
	if strings.Contains(q, "bye") || strings.Contains(q, "goodbye") || strings.Contains(q, "good night") {
		return "Goodbye. I will be here when you return."
	}

	return ""
}
