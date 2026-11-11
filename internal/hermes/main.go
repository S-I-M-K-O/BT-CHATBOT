package hermes

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
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

const (
	germanReplyInstruction  = "Antworte immer auf Deutsch."
	englishReplyInstruction = "Always reply in English."
)

// effectiveSystemPrompt picks the persona for the selected voice. The reply
// language always follows the voice: a German voice reading English (or an
// English voice reading German) sounds like a heavy accent. So
//   - an unedited built-in persona (English, German or a legacy default) is
//     swapped for the default of the voice's language, and
//   - an edited prompt is kept, with the reply instruction of the voice's
//     language appended (and the other language's instruction, which an
//     earlier save may have stored with it, removed).
func effectiveSystemPrompt(s *Settings) string {
	prompt := strings.TrimSpace(s.SystemPrompt)
	german := isGermanVoice(s.PiperVoice)
	want, other := englishReplyInstruction, germanReplyInstruction
	if german {
		want, other = germanReplyInstruction, englishReplyInstruction
	}
	if prompt == "" || isBuiltinEnglishPrompt(prompt) || prompt == germanDefaultPrompt {
		// The personas say nothing about the language, and a small model
		// answers in the language of the question unless told otherwise.
		if german {
			return germanDefaultPrompt + "\n\n" + want
		}
		return englishDefaultPrompt + "\n\n" + want
	}
	prompt = strings.TrimSpace(strings.ReplaceAll(prompt, other, ""))
	prompt = strings.TrimSpace(strings.ReplaceAll(prompt, want, ""))
	if prompt == "" || prompt == germanDefaultPrompt || isBuiltinEnglishPrompt(prompt) {
		return effectiveSystemPrompt(&Settings{PiperVoice: s.PiperVoice})
	}
	return prompt + "\n\n" + want
}

// replyLanguageReminder is sent as the last message of a persona request to
// the local model. The instruction in the system prompt alone is not enough:
// qwen answers a German question in German under an English voice (and the
// other way round) unless the reminder comes after the question.
func replyLanguageReminder(s *Settings) string {
	if isGermanVoice(s.PiperVoice) {
		return "Antworte auf Deutsch, egal in welcher Sprache der Nutzer geschrieben hat."
	}
	return "Reply in English, whatever language the user wrote in."
}

// phrases holds the spoken/displayed HERMES voicelines in one language.
type phrases struct {
	neuralOffline   string // %s ollama base, %s model
	localModelErr   string // %d status, %s model
	modelMissing    string // %s model
	streamBroken    string
	searchNoSubject string
	searchEmpty     string
	searchStore     string
	searchDownload  string
	searchFound     string // %d pictures, %s subject
}

var phrasesEN = phrases{
	neuralOffline:   "My local neural core is offline. Check that Ollama is running at %s and that the model %s is pulled.",
	localModelErr:   "Local model error (%d). Try: ollama pull %s",
	modelMissing:    "The model %s is not installed. Run: ollama pull %s",
	streamBroken:    "My transmission was interrupted. Please try again.",
	searchNoSubject: "What should I search for?",
	searchEmpty:     "No pictures found on the network. Try a different subject.",
	searchStore:     "Could not store the pictures.",
	searchDownload:  "Found pictures but could not download them.",
	searchFound:     "Found %d pictures of %s.",
}

var phrasesDE = phrases{
	neuralOffline:   "Meine lokale Recheneinheit ist offline. Prüfe, ob Ollama unter %s läuft und das Modell %s geladen ist.",
	localModelErr:   "Fehler des lokalen Modells (%d). Versuche: ollama pull %s",
	modelMissing:    "Das Modell %s ist nicht installiert. Ausführen: ollama pull %s",
	streamBroken:    "Meine Übertragung wurde unterbrochen. Bitte versuch es erneut.",
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
	// TrimSpace already ate the whitespace before it, so a leading "#" is a
	// comment after an empty value ("KEY= # note"), never the value itself.
	if strings.HasPrefix(v, "#") {
		return ""
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

// ensureGenDir creates the generated-images folder 0700 like the rest of
// data/ (it was 0755), and tightens a folder an older version created.
func ensureGenDir() error {
	dir := genDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// listenAddr is ADDR:PORT. Local-only by default; docker-compose overrides
// ADDR to 0.0.0.0 so the mapped port stays reachable. JoinHostPort brackets
// an IPv6 ADDR ("::1" -> "[::1]:8080"), which plain concatenation broke.
func listenAddr() string {
	return net.JoinHostPort(strings.Trim(envOr("ADDR", "127.0.0.1"), "[]"), envOr("PORT", "8080"))
}

// ---------------------------------------------------------------------------
// main

// Run starts HERMES: loads .env, starts the background workers and serves
// HTTP until the process is stopped. cmd/hermes calls it.
func Run() {
	loadDotEnv()
	if err := startupProblem(); err != nil {
		log.Fatal(err)
	}
	addr := listenAddr()

	if err := ensureGenDir(); err != nil {
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
	startBackground(ctx)
	srv.RegisterOnShutdown(hubCloseAll)

	if msg := lanAccessProblem(); msg != "" {
		log.Print(msg)
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("HERMES online on http://%s (Ollama: %s, Piper: %v)",
			addr, ollamaBase(), piperAvailable())
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
