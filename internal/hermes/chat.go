package hermes

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
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// chatMessage is what reaches Ollama: role and content only, so client-side
// tags can never leak into a model request.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// reqMessage is a history message as the client sends it, with the tags the
// server handed out in its "meta" event. Untagged messages (history from
// before v2) count as src "persona" and speaker "?".
type reqMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Src     string `json:"src,omitempty"`
	Sp      string `json:"sp"`
	Att     bool   `json:"att,omitempty"`
}

// untaggedSpeaker marks a message without an "sp" tag. It matches no
// speaker, so the speaker filter drops it when people exist.
const untaggedSpeaker = "?"

func (m *reqMessage) UnmarshalJSON(b []byte) error {
	var raw struct {
		Role    string  `json:"role"`
		Content string  `json:"content"`
		Src     *string `json:"src"`
		Sp      *string `json:"sp"`
		Att     bool    `json:"att"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*m = reqMessage{Role: raw.Role, Content: raw.Content, Src: "persona", Sp: untaggedSpeaker, Att: raw.Att}
	if raw.Src != nil && *raw.Src != "" {
		m.Src = truncateRunes(*raw.Src, 64)
	}
	if raw.Sp != nil {
		m.Sp = truncateRunes(*raw.Sp, 64)
	}
	return nil
}

type voicePayload struct {
	Model string  `json:"model"`
	ORT   string  `json:"ort"`
	Emb   string  `json:"emb"` // base64 little-endian float32
	Sec   float64 `json:"sec"`
	Short bool    `json:"short"` // captured, but too little audio
}

type speakerClaim struct {
	ID     string `json:"id"`
	Source string `json:"source"` // "manual" | "sticky" | "claim"
}

type attachmentPayload struct {
	Kind     string `json:"kind"`
	URL      string `json:"url"`
	FinalURL string `json:"finalUrl"`
	Title    string `json:"title"`
	Site     string `json:"site"`
	Text     string `json:"text"`
	Paywall  bool   `json:"paywall"`
	Lang     string `json:"lang"`
	// Instruction is what the user typed next to the URL ("fasse kurz
	// zusammen"); the stored message content is only the link marker.
	Instruction string `json:"instruction"`
}

type chatRequest struct {
	Messages []reqMessage `json:"messages"`
	// TimeZone is the browser's IANA zone (Intl.DateTimeFormat().
	// resolvedOptions().timeZone). Optional; the turn's clock uses it when
	// the household set no time zone.
	TimeZone      string             `json:"timeZone"`
	ClientID      string             `json:"clientId"`
	Spoken        bool               `json:"spoken"`
	Voice         *voicePayload      `json:"voice"`
	VoiceExpected bool               `json:"voiceExpected"`
	Speaker       *speakerClaim      `json:"speaker"`
	Attachment    *attachmentPayload `json:"attachment"`
}

const (
	maxHistoryMessages = 30
	maxMessageRunes    = 8000
)

// chatInFlight counts running chat turns; background model work (memory
// extraction) waits for it to drop to zero so it never slows a reply.
var chatInFlight atomic.Int64

// sanitizeTagged validates the client-supplied conversation: only user and
// assistant turns, empty turns dropped, content capped, history trimmed. The
// last message must be a non-empty user message.
func sanitizeTagged(in []reqMessage) ([]reqMessage, error) {
	if len(in) == 0 {
		return nil, errors.New("no messages")
	}
	out := make([]reqMessage, 0, len(in))
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
	return trimLeadingAssistant(out), nil
}

// trimLeadingAssistant: some chat templates reject a conversation that opens
// with the assistant.
func trimLeadingAssistant(out []reqMessage) []reqMessage {
	for len(out) > 1 && out[0].Role == "assistant" {
		out = out[1:]
	}
	return out
}

// sanitizeHistory is sanitizeTagged for untagged messages.
func sanitizeHistory(in []chatMessage) ([]chatMessage, error) {
	tagged := make([]reqMessage, len(in))
	for i, m := range in {
		tagged[i] = reqMessage{Role: m.Role, Content: m.Content}
	}
	out, err := sanitizeTagged(tagged)
	if err != nil {
		return nil, err
	}
	return plainMessages(out), nil
}

func plainMessages(in []reqMessage) []chatMessage {
	out := make([]chatMessage, len(in))
	for i, m := range in {
		out[i] = chatMessage{Role: m.Role, Content: m.Content}
	}
	return out
}

// filterHistory drops every earlier message that belongs to another speaker
// (or carries no speaker tag) once people exist, so one person's private
// conversation never reaches a model while someone else talks. The current
// query is always kept. Single-user mode keeps everything.
func filterHistory(tagged []reqMessage, speakerID string, people int) []reqMessage {
	if people == 0 || len(tagged) == 0 {
		return tagged
	}
	last := len(tagged) - 1
	out := make([]reqMessage, 0, len(tagged))
	for _, m := range tagged[:last] {
		if m.Sp == speakerID {
			out = append(out, m)
		}
	}
	return trimLeadingAssistant(append(out, tagged[last]))
}

var clientIDPattern = regexp.MustCompile(`^c_[0-9a-f]{12}$`)

// voiceModelWarned remembers mismatching model ids already logged, so a stale
// browser cache does not flood the log. The ids come from the client, so at
// most voiceModelWarnMax are remembered (and logged).
var (
	voiceModelWarned  sync.Map
	voiceModelWarnedN atomic.Int64
)

const voiceModelWarnMax = 16

// validateVoice checks a client voice embedding (Section 3.1): the expected
// model, exactly dim little-endian float32 values, all finite, a norm in
// [0.5, 2] (then renormalised). Too little voiced audio makes it short. A
// voice that fails a check is ignored (nil, not short).
func validateVoice(s *Settings, v *voicePayload) (in *VoiceInput, short bool, sec float64) {
	if v == nil {
		return nil, false, 0
	}
	sec = finiteOr(v.Sec, 0)
	if sec < 0 {
		sec = 0
	}
	if v.Short {
		return nil, true, sec
	}
	if v.Model != voiceModelID {
		model := truncateRunes(v.Model, 80)
		if _, seen := voiceModelWarned.Load(model); !seen && voiceModelWarnedN.Add(1) <= voiceModelWarnMax {
			voiceModelWarned.Store(model, true)
			log.Printf("voiceid: ignoring embedding of model %q (want %q)", model, voiceModelID)
		}
		return nil, false, sec
	}
	// The format checks are shared with the People API (people.go).
	emb, _, err := decodeVoiceEmb(v.Model, v.Emb)
	if err != nil {
		return nil, false, sec
	}
	if sec < s.VoiceIDMinSec {
		return nil, true, sec
	}
	return &VoiceInput{Model: v.Model, ORT: truncateRunes(v.ORT, 32), Emb: emb, Sec: sec}, false, sec
}

const (
	maxAttachmentText  = 12000
	maxAttachmentTitle = 300
	maxAttachmentURL   = 2000
)

// validAttachmentURL accepts absolute http(s) URLs of bounded length.
func validAttachmentURL(raw string) bool {
	if raw == "" || len(raw) > maxAttachmentURL {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// validateAttachment cleans a link attachment; anything else is dropped.
// The text and title are attacker-controlled: they are only ever summarised
// by the local model without tools, never stored in history.
func validateAttachment(a *attachmentPayload) *Attachment {
	if a == nil || a.Kind != "link" || !validAttachmentURL(a.URL) {
		return nil
	}
	final := a.FinalURL
	if !validAttachmentURL(final) {
		final = a.URL
	}
	lang := a.Lang
	if lang != "de" && lang != "en" {
		lang = ""
	}
	return &Attachment{
		Kind:        "link",
		URL:         a.URL,
		FinalURL:    final,
		Title:       cleanText(strings.ReplaceAll(a.Title, "\n", " "), maxAttachmentTitle),
		Site:        cleanText(strings.ReplaceAll(a.Site, "\n", " "), 200),
		Text:        cleanText(a.Text, maxAttachmentText),
		Lang:        lang,
		Paywall:     a.Paywall,
		Instruction: cleanText(strings.Join(strings.Fields(a.Instruction), " "), 500),
	}
}

// turnLocation: the household setting, then the browser's zone, then the
// env / Europe/Berlin fallback of settingsLocation.
func turnLocation(s *Settings, clientTZ string) *time.Location {
	if s.Timezone == "" {
		if tz := strings.TrimSpace(clientTZ); tz != "" && tz != "Local" {
			if loc, err := time.LoadLocation(tz); err == nil {
				return loc
			}
		}
	}
	return settingsLocation(s)
}

// newTurn builds the per-request Turn: language, clock, voice and attachment
// validation, speaker resolution, then the speaker filter on the history.
func newTurn(r *http.Request, w http.ResponseWriter, s *Settings, req *chatRequest, tagged []reqMessage) *Turn {
	t := &Turn{
		Ctx:           r.Context(),
		Req:           r,
		Settings:      s,
		Lang:          voiceLang(s.PiperVoice),
		Loc:           turnLocation(s, req.TimeZone),
		Spoken:        req.Spoken,
		VoiceExpected: req.VoiceExpected,
		w:             w,
	}
	t.Now = nowFunc().In(t.Loc)
	if clientIDPattern.MatchString(req.ClientID) {
		t.ClientID = req.ClientID
	}
	t.Unlocked = unlockedRequest(r)
	if s.VoiceIDEnabled {
		t.Voice, t.VoiceShort, t.VoiceSec = validateVoice(s, req.Voice)
	} else {
		t.VoiceExpected = false // voice ID is off: never "uncertain"
	}
	t.Attachment = validateAttachment(req.Attachment)
	var claimID, claimSource string
	if req.Speaker != nil {
		claimID, claimSource = truncateRunes(req.Speaker.ID, 64), req.Speaker.Source
	}
	t.Speaker = resolveSpeaker(t, t.Voice, claimID, claimSource)
	if t.Speaker.Known() && t.Speaker.Name == "" {
		t.Speaker.Name, _ = personName(t.Speaker.ID)
	}
	t.Tagged = filterHistory(tagged, t.Speaker.ID, peopleCount())
	t.History = plainMessages(t.Tagged)
	t.Query = t.History[len(t.History)-1].Content
	t.Tokens = tokenize(t.Query)
	return t
}

// carriesAttachment: the turn reads a link. Such turns never reach the
// router or the realtime services, whatever the fast paths did with them.
func (t *Turn) carriesAttachment() bool {
	if t.Attachment != nil {
		return true
	}
	n := len(t.Tagged)
	return n > 0 && t.Tagged[n-1].Att
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
	tagged, err := sanitizeTagged(req.Messages)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	start := time.Now()
	s := loadSettings()
	t := newTurn(r, w, s, &req, tagged)
	chatInFlight.Add(1)
	defer chatInFlight.Add(-1)

	// From here on the answer is a stream. {"meta"} always goes out right
	// before "done" (also after errors), evaluated when the turn is over.
	sse := t.SSE()
	sse.beforeDone = func() any { return map[string]any{"meta": t.Meta()} }
	defer logTurnTiming(t, start)
	// Every path ends with meta + done, also an older handler that returned
	// early (busy image job, client gone).
	defer func() {
		if !sse.isDone() {
			sse.done()
		}
	}()

	if peopleCount() > 0 || t.Voice != nil || t.VoiceShort {
		sse.event(map[string]any{"speaker": t.Speaker})
	}

	// 0+1: a pending answer, then deterministic fast paths (access checked).
	if runFastPaths(t) {
		return
	}

	// 2: local knowledge base: answer small talk without calling a model.
	if local := localAnswer(t.Query, t.Lang); local != "" {
		t.handledBy = "kb"
		sse.reply(local)
		runAfterTurn(t, local)
		return
	}

	// 3: the tool router, only for utterances that pass a phrase gate. It
	// never sees a link turn.
	if s.RouterEnabled && !t.carriesAttachment() && gateMatches(t) {
		stop := sse.watchBusy()
		handled := runRouter(t)
		stop()
		if handled {
			return
		}
	}

	// The image search handler builds its stream with newSSE(w); tw hands
	// it this turn's stream.
	tw := &turnWriter{ResponseWriter: w, t: t}
	kind := classifyImageRequest(t.Query)
	if !t.carriesAttachment() {
		// 4: there is no image generator: say so (and what works instead)
		// rather than letting the local model pretend to draw.
		if kind == "generate" {
			t.handledBy = "kb"
			text := pick(t.Lang,
				"I can't draw pictures, but I can search the web for some. Say, for example: \"Show me pictures of lighthouses.\"",
				"Bilder malen kann ich nicht, aber ich kann im Internet nach Bildern suchen. Sag zum Beispiel: „Zeig mir Bilder von Leuchttürmen.“")
			sse.reply(text)
			runAfterTurn(t, text)
			return
		}
		// 5: weather, time and news, answered locally from Open-Meteo, the
		// clock and the news feeds (realtime.go). Only the city or the
		// topic leaves the machine, never the history.
		if req, ok := realtimeIntent(t); ok {
			runRealtime(t, req)
			return
		}
	}
	// 6: internet image search needs no API key.
	if kind == "search" && !t.carriesAttachment() {
		t.handledBy = "image"
		handleImageSearch(r.Context(), tw, s, t.Query)
		return
	}
	// 7: persona chat, no tools, with the personal context. After a reply
	// built from a page, a document or the calendar only the speaker and
	// honesty lines go in: untrusted text in the history must not share a
	// prompt with personal data (SPEC 8.3).
	t.handledBy = "persona"
	stop := sse.watchBusy()
	t.untrustedHistory = recentUntrustedReply(t)
	reply, ok := streamLocal(t, LocalJob{Persona: true, Personal: true, LangReminder: true, Messages: personaHistory(t)})
	stop()
	if ok {
		runAfterTurn(t, reply)
	}
}

// personaHistory is the history the persona model sees. When the prompt
// carries personal data (no untrusted reply among the last two assistant
// turns), older replies built from a page, a document, the calendar or the
// briefing are replaced by a short marker, so their text never sits next to
// the household's memories (SPEC 8.3).
func personaHistory(t *Turn) []chatMessage {
	if t.untrustedHistory || len(t.Tagged) != len(t.History) {
		return t.History
	}
	out := t.History
	copied := false
	for i := len(t.Tagged) - 2; i >= 0; i-- {
		if t.Tagged[i].Role != "assistant" || !extractionSkipSource(t.Tagged[i].Src) {
			continue
		}
		if !copied {
			out = append([]chatMessage(nil), t.History...)
			copied = true
		}
		out[i].Content = pick(t.Lang,
			"(An earlier answer from a web page, a document or the calendar is left out here.)",
			"(Eine frühere Antwort aus einer Webseite, einem Dokument oder dem Kalender ist hier ausgelassen.)")
	}
	return out
}

// logTurnTiming logs per-turn latency with HERMES_DEBUG_TIMING=1: request
// start to first delta, and to the end of the stream.
func logTurnTiming(t *Turn, start time.Time) {
	if !envBool("HERMES_DEBUG_TIMING") || t.out == nil {
		return
	}
	first := "-"
	if at := t.out.firstDeltaAt(); !at.IsZero() {
		first = at.Sub(start).Round(time.Millisecond).String()
	}
	src, _ := t.Meta()["src"].(string)
	log.Printf("timing: src=%s first_delta=%s total=%s", src, first, time.Since(start).Round(time.Millisecond))
}

type ollamaChunk struct {
	Message chatMessage `json:"message"`
	Done    bool        `json:"done"`
	Error   string      `json:"error"`
}

// streamLocal streams a local-model answer for a turn and returns the full
// text, ok=false when the model failed or the client went away. It never
// sends tools. The personal context (Section 4.7) is only ever placed here,
// in a request to ollamaBase().
func streamLocal(t *Turn, job LocalJob) (string, bool) {
	s := t.Settings
	var system []string
	if job.Persona {
		system = append(system, effectiveSystemPrompt(s))
	}
	if strings.TrimSpace(job.System) != "" {
		system = append(system, job.System)
	}
	if job.Personal {
		if text, personal := personalContext(t); text != "" {
			system = append(system, text)
			if personal {
				t.personalUsed = true
			}
		}
	}
	if len(t.replySuffix) > 0 && t.out != nil {
		sse, suffix := t.out, t.replySuffix
		t.replySuffix = nil
		prev := sse.beforeDone
		// done() calls this with the stream lock held.
		sse.beforeDone = func() any {
			ev := map[string]any{}
			if prev != nil {
				if m, ok := prev().(map[string]any); ok {
					for k, v := range m {
						ev[k] = v
					}
				}
			}
			if !sse.firstDelta.IsZero() && t.Ctx.Err() == nil {
				for _, fn := range suffix {
					fn(ev)
				}
			}
			return ev
		}
	}
	messages := make([]chatMessage, 0, len(job.Messages)+1)
	if len(system) > 0 {
		messages = append(messages, chatMessage{Role: "system", Content: strings.Join(system, "\n\n")})
	}
	messages = append(messages, job.Messages...)
	if job.LangReminder && len(job.Messages) > 0 {
		messages = append(messages, chatMessage{Role: "system", Content: replyLanguageReminder(s)})
	}
	maxTokens := job.MaxTokens
	if maxTokens <= 0 {
		maxTokens = s.MaxTokens
	}
	return ollamaStream(t.Ctx, s, messages, maxTokens, t.SSE)
}

// handleLocalChat streams a local-model answer for ready-made messages
// (system prompt included).
func handleLocalChat(ctx context.Context, w http.ResponseWriter, s *Settings, messages []chatMessage) {
	var sse *sseStream
	ollamaStream(ctx, s, messages, s.MaxTokens, func() *sseStream {
		if sse == nil {
			sse = newSSE(w)
		}
		return sse
	})
}

// ollamaStream posts messages to Ollama and relays the reply as deltas.
// stream is called only once there is something to send, so the SSE headers
// go out as late as before. Like every local call it sends keep_alive and
// the shared num_ctx, so Ollama never reloads the model between calls.
func ollamaStream(ctx context.Context, s *Settings, messages []chatMessage, maxTokens int, stream func() *sseStream) (string, bool) {
	p := phrasesFor(s.PiperVoice)
	base := ollamaBase()
	payload := map[string]any{
		"model":    s.LocalModel,
		"messages": messages,
		"stream":   true,
		"think":    false,
		"options":  ollamaOptions(s, s.Temperature, maxTokens),
	}
	if s.KeepAlive != "" {
		payload["keep_alive"] = s.KeepAlive
	}
	body, err := json.Marshal(payload)
	if err != nil {
		stream().fail(p.streamBroken)
		return "", false
	}
	outReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/chat", bytes.NewReader(body))
	if err != nil {
		stream().fail(fmt.Sprintf(p.neuralOffline, base, s.LocalModel))
		return "", false
	}
	outReq.Header.Set("Content-Type", "application/json")

	resp, err := ollamaClient().Do(outReq)
	if err != nil {
		if ctx.Err() != nil {
			return "", false // client went away
		}
		log.Printf("ollama request error: %v", err)
		stream().fail(fmt.Sprintf(p.neuralOffline, base, s.LocalModel))
		return "", false
	}
	defer resp.Body.Close()

	sse := stream()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		log.Printf("ollama HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		if resp.StatusCode == http.StatusNotFound {
			sse.fail(fmt.Sprintf(p.modelMissing, s.LocalModel, s.LocalModel))
			return "", false
		}
		sse.fail(fmt.Sprintf(p.localModelErr, resp.StatusCode, s.LocalModel))
		return "", false
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var text strings.Builder
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
			if text.Len() > 0 {
				msg = " " + msg
			}
			sse.fail(msg)
			return text.String(), false
		}
		if chunk.Message.Content != "" {
			text.WriteString(chunk.Message.Content)
			sse.event(map[string]any{"delta": chunk.Message.Content})
		}
		if chunk.Done {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return text.String(), false // client aborted; Ollama was cancelled via the context
		}
		log.Printf("ollama stream read error: %v", err)
		msg := p.streamBroken
		if text.Len() > 0 {
			msg = " " + msg
		}
		sse.fail(msg)
		return text.String(), false
	}
	sse.done()
	return text.String(), true
}
