package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

func defaultSettings() *Settings {
	return &Settings{
		SystemPrompt: "You are HERMES, a voice assistant with a calm, measured, slightly robotic monotone. Keep your answers concise and tactical. Address the user as 'Pilot'. Use phrases like 'Copy that' and 'Understood' sparingly. Be precise, helpful, and a little dry. You never mention being an AI language model.",
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
	return os.WriteFile(path, b, 0o644)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if len(req.Messages) == 0 {
		http.Error(w, `{"error": "no messages"}`, http.StatusBadRequest)
		return
	}

	// Local knowledge base: answer known questions without calling the model.
	local := localAnswer(strings.ToLower(req.Messages[len(req.Messages)-1].Content))
	if local != "" {
		writeSSE(w, local)
		return
	}

	s := loadSettings()

	messages := make([]chatMessage, 0, len(req.Messages)+1)
	messages = append(messages, chatMessage{Role: "system", Content: s.SystemPrompt})
	messages = append(messages, req.Messages...)

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
		writeSSE(w, "My local neural core is offline, Pilot. Check that Ollama is running and pull qwen3.5.")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		writeSSE(w, fmt.Sprintf("Local model error (%d), Pilot. Run: ollama pull qwen3.5", resp.StatusCode))
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
		http.Error(w, fmt.Sprintf(`{"error": "%v"}`, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Write(audio)
}

// piperVoices maps a friendly voice key to the bundled Piper model files
// inside VOICE_DIR. Only these names are accepted (no path traversal).
var piperVoices = map[string]string{
	"BT7274":   "BT7274.onnx",
	"ScorchAI": "ScorchAI.onnx",
	"PDA":      "PDA.onnx",
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
	outFile := filepath.Join(os.TempDir(), "bt_voice.wav")

	_ = os.Remove(outFile)
	cmd := exec.Command(bin,
		"--model", model,
		"--config", config,
		"--output_file", outFile,
	)
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
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
	var s Settings
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := saveSettings(&s); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

func main() {
	port := envOr("PORT", "8080")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat", handleChat)
	mux.HandleFunc("/api/tts", handleTTS)
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
	fs := http.StripPrefix("/", http.FileServer(http.Dir(envOr("STATIC_DIR", "static"))))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		fs.ServeHTTP(w, r)
	}))

	log.Printf("HERMES online on :%s (local Ollama engine)", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}

// localAnswer returns a canned HERMES response when the message is known locally,
// so the model is only used when the bot "does not know".
func localAnswer(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))

	greeting := []string{"hi", "hello", "hey", "howdy", "greetings", "hi there", "hello there"}
	for _, g := range greeting {
		if q == g || strings.HasPrefix(q, g+" ") || strings.HasPrefix(q, g+",") || strings.HasPrefix(q, g+"!") {
			return "Hello, Pilot. HERMES online. How can I assist you today?"
		}
	}

	if strings.Contains(q, "who are you") || strings.Contains(q, "your name") || strings.Contains(q, "what are you") {
		return "I am HERMES, your voice assistant."
	}
	if strings.Contains(q, "how are you") {
		return "My systems are nominal. How are you, Pilot?"
	}
	if strings.Contains(q, "thank") {
		return "You are welcome, Pilot."
	}
	if strings.Contains(q, "bye") || strings.Contains(q, "goodbye") || strings.Contains(q, "good night") {
		return "Goodbye, Pilot. I will be here when you return."
	}

	return ""
}
