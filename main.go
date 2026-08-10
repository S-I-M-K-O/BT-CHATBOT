package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

const freeAIBase = "https://api.free.ai"

var (
	apiKey     string
	keyMu      sync.RWMutex
	settingsMu sync.Mutex
)

type Settings struct {
	Model        string  `json:"model"`
	SystemPrompt string  `json:"systemPrompt"`
	Temperature  float64 `json:"temperature"`
	MaxTokens    int     `json:"maxTokens"`
	Voice        string  `json:"voice"`
	VoiceMode    string  `json:"voiceMode"`
	Pitch        float64 `json:"pitch"`
	Rate         float64 `json:"rate"`
	Volume       float64 `json:"volume"`
	ServerVoice  string  `json:"serverVoice"`
}

func defaultSettings() *Settings {
	return &Settings{
		Model:        "qwen7b",
		SystemPrompt: "You are BT-7274, a Vanguard-class Titan from Titanfall 2. You are loyal to your Pilot, Jack Cooper. You speak in a calm, measured, slightly robotic monotone. Keep answers concise and tactical. Use phrases like 'Pilot', 'Copy that', 'Trust me'. You occasionally reference the Frontier.",
		Temperature:  0.7,
		MaxTokens:    1024,
		Voice:        "",
		VoiceMode:    "piper",
		Pitch:        0.6,
		Rate:         -0.2,
		Volume:       1.0,
		ServerVoice:  "am_onyx",
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

func getAPIKey() string {
	keyMu.RLock()
	defer keyMu.RUnlock()
	return apiKey
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

	// Local BT knowledge base: answer known questions without calling the API,
	// mirroring "if it does not know, ask free.ai".
	local := localAnswer(strings.ToLower(req.Messages[len(req.Messages)-1].Content))
	if local != "" {
		writeSSE(w, local)
		return
	}

	key := getAPIKey()
	if key == "" {
		http.Error(w, `{"error": "FREEAI_API_KEY not set. Set it in your environment and restart."}`, http.StatusServiceUnavailable)
		return
	}

	s := loadSettings()

	messages := make([]chatMessage, 0, len(req.Messages)+1)
	messages = append(messages, chatMessage{Role: "system", Content: s.SystemPrompt})
	messages = append(messages, req.Messages...)

	body, _ := json.Marshal(map[string]any{
		"model":       s.Model,
		"messages":    messages,
		"temperature": s.Temperature,
		"max_tokens":  s.MaxTokens,
		"stream":      true,
	})

	outReq, err := http.NewRequest(http.MethodPost, freeAIBase+"/v1/chat/", bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	outReq.Header.Set("Authorization", "Bearer "+key)
	outReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(outReq)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "free.ai request failed: %v"}`, err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		http.Error(w, fmt.Sprintf(`{"error": "free.ai HTTP %d: %s"}`, resp.StatusCode, string(b)), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			break
		}
	}
}

func writeSSE(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	enc := json.NewEncoder(w)
	for _, ch := range strings.Split(text, "") {
		enc.Encode(map[string]any{"delta": ch})
		w.(http.Flusher).Flush()
	}
	enc.Encode(map[string]any{"done": true})
	w.(http.Flusher).Flush()
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

	// Local Piper with the BT-7274 voice — no API key required.
	if engine == "piper" {
		audio, err := piperTTS(req.Text)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "%v"}`, err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Write(audio)
		return
	}

	key := getAPIKey()
	if key == "" {
		http.Error(w, `{"error": "FREEAI_API_KEY not set."}`, http.StatusServiceUnavailable)
		return
	}

	voice := req.Voice
	if voice == "" {
		voice = s.ServerVoice
	}

	body, _ := json.Marshal(map[string]any{
		"text":  req.Text,
		"voice": voice,
		"model": "kokoro",
	})

	outReq, err := http.NewRequest(http.MethodPost, freeAIBase+"/v1/tts/", bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	outReq.Header.Set("Authorization", "Bearer "+key)
	outReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(outReq)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%v"}`, err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		http.Error(w, fmt.Sprintf(`{"error": "free.ai TTS HTTP %d: %s"}`, resp.StatusCode, string(b)), http.StatusBadGateway)
		return
	}

	// free.ai TTS may return raw audio (audio/*) or a JSON object with audio_url.
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "audio/") || strings.HasPrefix(ct, "application/vnd") {
		w.Header().Set("Content-Type", ct)
		io.Copy(w, resp.Body)
		return
	}

	var ttsResp struct {
		AudioURL string `json:"audio_url"`
		URL      string `json:"url"`
		Audio    string `json:"audio"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ttsResp); err != nil {
		http.Error(w, `{"error": "unexpected TTS response"}`, http.StatusBadGateway)
		return
	}

	audioURL := ttsResp.AudioURL
	if audioURL == "" {
		audioURL = ttsResp.URL
	}
	if audioURL == "" && ttsResp.Audio != "" {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte(ttsResp.Audio))
		return
	}
	if audioURL == "" {
		http.Error(w, `{"error": "free.ai returned no audio"}`, http.StatusBadGateway)
		return
	}

	audio, err := fetchAudio(audioURL)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%v"}`, err), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(audio))
	w.Write(audio)
}

func fetchAudio(audioURL string) ([]byte, error) {
	client := &http.Client{}
	resp, err := client.Get(audioURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("audio download HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func piperTTS(text string) ([]byte, error) {
	bin := envOr("PIPER_BIN", "piper")
	voiceDir := envOr("VOICE_DIR", "voices")
	model := filepath.Join(voiceDir, "BT7274.onnx")
	config := filepath.Join(voiceDir, "BT7274.onnx.json")
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
	apiKey = os.Getenv("FREEAI_API_KEY")

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
	mux.Handle("/", http.FileServer(http.Dir(envOr("STATIC_DIR", "static"))))

	log.Printf("BT-7274 online on :%s (FREEAI_API_KEY %s)", port, keyStatus())
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}

func keyStatus() string {
	s := getAPIKey()
	if s == "" {
		return "<NOT SET>"
	}
	return "set"
}

// localAnswer returns a canned BT response when the message is known locally,
// so free.ai is only used when the bot "does not know".
func localAnswer(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))

	greeting := []string{"hi", "hello", "hey", "howdy", "greetings", "hi there", "hello there"}
	for _, g := range greeting {
		if q == g || strings.HasPrefix(q, g+" ") || strings.HasPrefix(q, g+",") || strings.HasPrefix(q, g+"!") {
			return "Hello, Pilot. BT-7274 online. How can I assist you today?"
		}
	}

	if strings.Contains(q, "who are you") || strings.Contains(q, "your name") || strings.Contains(q, "what are you") {
		return "I am BT-7274, a Vanguard-class Titan. My Pilot is Jack Cooper. Protocol 3: protect the Pilot."
	}
	if strings.Contains(q, "protocol") {
		return "Protocol 1: link to Pilot. Protocol 2: uphold the mission. Protocol 3: protect the Pilot. Jill demands it. I mean... the mission demands it."
	}
	if strings.Contains(q, "jack cooper") || strings.Contains(q, "cooper") {
		return "Jack Cooper is a Rifleman Third Class with the SRS. Despite the odds, he became a worthy Pilot. I trust him."
	}
	if strings.Contains(q, "how are you") {
		return "My chassis is at 100 percent integrity. All systems operational. How are you, Pilot?"
	}
	if strings.Contains(q, "trust me") {
		return "I trust you, Pilot. The SRS motto: 'Stand fast. Fight smart. Trust me.'"
	}
	if strings.Contains(q, "thank") {
		return "You are welcome, Pilot."
	}
	if strings.Contains(q, "bye") || strings.Contains(q, "goodbye") || strings.Contains(q, "good night") {
		return "Stand by, Pilot. I will be here when you return."
	}

	return ""
}
