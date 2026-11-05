package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const englishDefaultPrompt = "You are HERMES, a voice assistant with a calm, measured, slightly robotic monotone. Keep your answers concise and tactical. Never call the user 'Pilot' and do not give them any title or nickname; just talk to them directly. Use phrases like 'Copy that' and 'Understood' sparingly. Be precise, helpful, and a little dry. You never mention being an AI language model."

const germanDefaultPrompt = "Du bist HERMES, ein Sprachassistent mit ruhiger, gemessener, leicht roboterhafter Monotonie. Halte deine Antworten kurz und taktisch. Nenne den Nutzer niemals 'Pilot' und gib ihm keinen Titel oder Spitznamen; sprich ihn einfach direkt an. Verwende Ausdrücke wie 'Verstanden' und 'Kopiert' sparsam. Sei präzise, hilfreich und ein wenig trocken. Erwähne niemals, dass du ein KI-Sprachmodell bist."

// legacyEnglishDefaultPrompts are earlier shipped defaults (English and
// German) that may still be saved in users' settings files; they are treated
// as unedited and replaced by the current default.
var legacyEnglishDefaultPrompts = []string{
	"You are HERMES, a voice assistant with a calm, measured, slightly robotic monotone. Keep your answers concise and tactical. Use phrases like 'Copy that' and 'Understood' sparingly. Be precise, helpful, and a little dry. You never mention being an AI language model.",
	"Du bist HERMES, ein Sprachassistent mit ruhiger, gemessener, leicht roboterhafter Monotonie. Halte deine Antworten kurz und taktisch. Verwende Ausdrücke wie 'Verstanden' und 'Kopiert' sparsam. Sei präzise, hilfreich und ein wenig trocken. Erwähne niemals, dass du ein KI-Sprachmodell bist.",
	"You are HERMES, a voice assistant with a calm, measured, slightly robotic monotone. Keep your answers concise and tactical. Address the user as 'Pilot'. Use phrases like 'Copy that' and 'Understood' sparingly. Be precise, helpful, and a little dry. You never mention being an AI language model.",
}

// isBuiltinEnglishPrompt reports whether p is exactly (ignoring surrounding
// whitespace) a shipped English default, i.e. one the user has not edited.
func isBuiltinEnglishPrompt(p string) bool {
	p = strings.TrimSpace(p)
	if p == englishDefaultPrompt {
		return true
	}
	for _, d := range legacyEnglishDefaultPrompts {
		if p == d {
			return true
		}
	}
	return false
}

const germanReplyInstruction = "Antworte immer auf Deutsch."

// effectiveSystemPrompt picks the language-appropriate HERMES persona. The
// unedited built-in English persona is swapped for the German one when a
// German voice is selected; an edited or hand-written prompt is always kept,
// with a reply-in-German instruction appended for a German voice.
func effectiveSystemPrompt(s *Settings) string {
	prompt := strings.TrimSpace(s.SystemPrompt)
	german := isGermanVoice(s.PiperVoice)
	switch {
	case prompt == "" || isBuiltinEnglishPrompt(prompt):
		if german {
			return germanDefaultPrompt
		}
		return englishDefaultPrompt
	case german && prompt != germanDefaultPrompt && !strings.Contains(prompt, germanReplyInstruction):
		return prompt + "\n\n" + germanReplyInstruction
	}
	return prompt
}

// phrases holds the spoken/displayed HERMES voicelines in one language.
type phrases struct {
	neuralOffline        string // %s ollama base, %s model
	localModelErr        string // %d status, %s model
	modelMissing         string // %s model
	streamBroken         string
	satelliteDown        string
	satelliteKeyRejected string
	noData               string
	imageFabricator      string
	imageSynthesis       string
	imageEmpty           string
	imageRetrieve        string
	imageStore           string
	imageCaption         string
	searchNoSubject      string
	searchEmpty          string
	searchStore          string
	searchDownload       string
	searchFound          string // %d pictures, %s subject
}

var phrasesEN = phrases{
	neuralOffline:        "My local neural core is offline. Check that Ollama is running at %s and that the model %s is pulled.",
	localModelErr:        "Local model error (%d). Try: ollama pull %s",
	modelMissing:         "The model %s is not installed. Run: ollama pull %s",
	streamBroken:         "My transmission was interrupted. Please try again.",
	satelliteDown:        "My satellite link is down. Mistral could not be reached — check that MISTRAL_API_KEY is set.",
	satelliteKeyRejected: "Mistral rejected my satellite credentials. Check MISTRAL_API_KEY.",
	noData:               "No data recovered from the satellite. Try again.",
	imageFabricator:      "My image fabricator is offline. Check the Mistral API key.",
	imageSynthesis:       "Image synthesis failed. Try again in a moment.",
	imageEmpty:           "The image came back empty. Try a more specific request.",
	imageRetrieve:        "Could not retrieve the generated image.",
	imageStore:           "Could not store the generated image.",
	imageCaption:         "Image fabricated.",
	searchNoSubject:      "What should I search for?",
	searchEmpty:          "No pictures found on the network. Try a different subject.",
	searchStore:          "Could not store the pictures.",
	searchDownload:       "Found pictures but could not download them.",
	searchFound:          "Found %d pictures of %s.",
}

var phrasesDE = phrases{
	neuralOffline:        "Meine lokale Recheneinheit ist offline. Prüfe, ob Ollama unter %s läuft und das Modell %s geladen ist.",
	localModelErr:        "Fehler des lokalen Modells (%d). Versuche: ollama pull %s",
	modelMissing:         "Das Modell %s ist nicht installiert. Ausführen: ollama pull %s",
	streamBroken:         "Meine Übertragung wurde unterbrochen. Bitte versuch es erneut.",
	satelliteDown:        "Meine Satellitenverbindung ist unterbrochen. Mistral konnte nicht erreicht werden — prüfe, ob MISTRAL_API_KEY gesetzt ist.",
	satelliteKeyRejected: "Mistral hat meine Satelliten-Zugangsdaten abgelehnt. Prüfe MISTRAL_API_KEY.",
	noData:               "Keine Daten vom Satelliten erhalten. Versuch es erneut.",
	imageFabricator:      "Meine Bild-Fabrik ist offline. Prüfe den Mistral-API-Schlüssel.",
	imageSynthesis:       "Die Bildsynthese ist fehlgeschlagen. Versuch es gleich noch einmal.",
	imageEmpty:           "Das Bild kam leer zurück. Versuch eine genauere Anfrage.",
	imageRetrieve:        "Das erzeugte Bild konnte nicht abgerufen werden.",
	imageStore:           "Das erzeugte Bild konnte nicht gespeichert werden.",
	imageCaption:         "Bild erstellt.",
	searchNoSubject:      "Wonach soll ich suchen?",
	searchEmpty:          "Keine Bilder im Netzwerk gefunden. Versuch ein anderes Motiv.",
	searchStore:          "Die Bilder konnten nicht gespeichert werden.",
	searchDownload:       "Bilder gefunden, aber nicht herunterladbar.",
	searchFound:          "Gefunden: %d Bilder von %s.",
}

func phrasesFor(voice string) phrases {
	if isGermanVoice(voice) {
		return phrasesDE
	}
	return phrasesEN
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

// parseDotEnvValue interprets the right-hand side of a .env line the way
// docker compose does: a quoted value ends at its closing quote (anything
// after it, such as a comment, is ignored); an unquoted value ends at the
// first whitespace-preceded "#".
func parseDotEnvValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 0 && (v[0] == '"' || v[0] == '\'') {
		if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
			return v[1 : end+1]
		}
		return v // unterminated quote: keep as written
	}
	for i := 1; i < len(v); i++ {
		if v[i] == '#' && (v[i-1] == ' ' || v[i-1] == '\t') {
			return strings.TrimSpace(v[:i])
		}
	}
	return v
}

// loadDotEnv reads KEY=VALUE pairs from .env without overriding variables
// that are already set in the environment.
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
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, parseDotEnvValue(v))
		}
	}
}

// ---------------------------------------------------------------------------
// Chat

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Messages []chatMessage `json:"messages"`
	// TimeZone is the browser's IANA zone (Intl.DateTimeFormat().
	// resolvedOptions().timeZone). Optional; used by the get_time tool.
	TimeZone string `json:"timeZone"`
}

const (
	maxHistoryMessages = 30
	maxMessageRunes    = 8000
)

// sanitizeHistory validates the client-supplied conversation: only user and
// assistant turns, empty turns dropped, content capped, history trimmed. The
// last message must be a non-empty user message.
func sanitizeHistory(in []chatMessage) ([]chatMessage, error) {
	if len(in) == 0 {
		return nil, errors.New("no messages")
	}
	out := make([]chatMessage, 0, len(in))
	for _, m := range in {
		if m.Role != "user" && m.Role != "assistant" {
			return nil, fmt.Errorf("invalid role %q", m.Role)
		}
		if !utf8.ValidString(m.Content) {
			m.Content = strings.ToValidUTF8(m.Content, "")
		}
		m.Content = truncateRunes(strings.TrimSpace(m.Content), maxMessageRunes)
		if m.Content == "" {
			continue
		}
		out = append(out, m)
	}
	if len(out) == 0 || out[len(out)-1].Role != "user" {
		return nil, errors.New("last message must be a non-empty user message")
	}
	if len(out) > maxHistoryMessages {
		out = out[len(out)-maxHistoryMessages:]
	}
	// Some chat templates reject a conversation that opens with the assistant.
	for len(out) > 1 && out[0].Role == "assistant" {
		out = out[1:]
	}
	return out, nil
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad request")
		return
	}
	history, err := sanitizeHistory(req.Messages)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	q := history[len(history)-1].Content
	s := loadSettings()

	// Local knowledge base: answer small talk without calling a model.
	if local := localAnswer(q, voiceLang(s.PiperVoice)); local != "" {
		newSSE(w).reply(local)
		return
	}

	messages := make([]chatMessage, 0, len(history)+1)
	messages = append(messages, chatMessage{Role: "system", Content: effectiveSystemPrompt(s)})
	messages = append(messages, history...)

	kind := classifyImageRequest(q)
	// Mistral handles image generation and real-time queries (weather, time,
	// news). Everything else stays on the local Ollama model.
	if mistralEnabled() {
		if kind == "generate" {
			handleMistralImage(r.Context(), w, s, q)
			return
		}
		if wantsRealtime(q) {
			handleMistralTools(r.Context(), w, s, messages, req.TimeZone)
			return
		}
	}
	// Internet image search needs no API key.
	if kind == "search" {
		handleImageSearch(r.Context(), w, s, q)
		return
	}
	handleLocalChat(r.Context(), w, s, messages)
}

type ollamaChunk struct {
	Message chatMessage `json:"message"`
	Done    bool        `json:"done"`
	Error   string      `json:"error"`
}

func handleLocalChat(ctx context.Context, w http.ResponseWriter, s *Settings, messages []chatMessage) {
	p := phrasesFor(s.PiperVoice)
	base := ollamaBase()
	body, err := json.Marshal(map[string]any{
		"model":    s.LocalModel,
		"messages": messages,
		"stream":   true,
		"think":    false,
		"options": map[string]any{
			"temperature": s.Temperature,
			"num_predict": s.MaxTokens,
		},
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not encode request")
		return
	}
	outReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/chat", bytes.NewReader(body))
	if err != nil {
		newSSE(w).fail(fmt.Sprintf(p.neuralOffline, base, s.LocalModel))
		return
	}
	outReq.Header.Set("Content-Type", "application/json")

	resp, err := ollamaClient().Do(outReq)
	if err != nil {
		if ctx.Err() != nil {
			return // client went away
		}
		log.Printf("ollama request error: %v", err)
		newSSE(w).fail(fmt.Sprintf(p.neuralOffline, base, s.LocalModel))
		return
	}
	defer resp.Body.Close()

	sse := newSSE(w)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		log.Printf("ollama HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		if resp.StatusCode == http.StatusNotFound {
			sse.fail(fmt.Sprintf(p.modelMissing, s.LocalModel, s.LocalModel))
			return
		}
		sse.fail(fmt.Sprintf(p.localModelErr, resp.StatusCode, s.LocalModel))
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	gotText := false
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var chunk ollamaChunk
		if err := json.Unmarshal(line, &chunk); err != nil {
			continue
		}
		if chunk.Error != "" {
			log.Printf("ollama stream error: %s", chunk.Error)
			msg := p.streamBroken
			if gotText {
				msg = " " + msg
			}
			sse.fail(msg)
			return
		}
		if chunk.Message.Content != "" {
			gotText = true
			sse.event(map[string]any{"delta": chunk.Message.Content})
		}
		if chunk.Done {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return // client aborted; Ollama was cancelled via the context
		}
		log.Printf("ollama stream read error: %v", err)
		msg := p.streamBroken
		if gotText {
			msg = " " + msg
		}
		sse.fail(msg)
		return
	}
	sse.done()
}

// ---------------------------------------------------------------------------
// main

func main() {
	loadDotEnv()
	// Local-only by default; docker-compose overrides to 0.0.0.0 so the mapped
	// port stays reachable.
	addr := envOr("ADDR", "127.0.0.1") + ":" + envOr("PORT", "8080")

	if err := os.MkdirAll(genDir(), 0o755); err != nil {
		log.Printf("could not create gen dir: %v", err)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           newMux(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      10 * time.Minute, // long streamed answers
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go runGenCleanup(ctx, time.Hour)

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("HERMES online on http://%s (Ollama: %s, Piper: %v, Mistral: %v)",
			addr, ollamaBase(), piperAvailable(), mistralEnabled())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		log.Fatalf("server error: %v", err)
	}
	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
