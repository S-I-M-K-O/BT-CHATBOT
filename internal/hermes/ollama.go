package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Shared helpers for the non-streaming Ollama calls the companion features
// make (tool router, memory extraction, link/doc answers, embeddings). They
// all send keep_alive and the same num_ctx, because Ollama reloads a model
// whenever num_ctx changes between calls (SPEC 4.5, 4.11).

// ollamaOptions returns the "options" block for a call to a chat model.
// numPredict <= 0 leaves Ollama's default.
func ollamaOptions(s *Settings, temperature float64, numPredict int) map[string]any {
	o := map[string]any{"temperature": temperature}
	if s.NumCtx > 0 {
		o["num_ctx"] = s.NumCtx
	}
	if numPredict > 0 {
		o["num_predict"] = numPredict
	}
	return o
}

// ollamaToolCall is one tool call in an Ollama chat response. Arguments is
// a JSON object (Ollama sends an object, not a string).
type ollamaToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// ollamaChatRequest is a non-streaming /api/chat call. Format is either
// nil, "json" or a JSON schema; Tools is the Ollama/OpenAI tools array.
type ollamaChatRequest struct {
	Model       string
	Messages    []chatMessage
	Tools       []any
	Format      any
	Temperature float64
	NumPredict  int
}

// ollamaChatResult is the assistant message of a non-streaming call.
type ollamaChatResult struct {
	Content   string
	ToolCalls []ollamaToolCall
}

var errOllamaStatus = errors.New("ollama returned an error status")

// ollamaChatOnce sends one non-streaming chat request with think:false,
// keep_alive and num_ctx from the settings. The caller bounds the time via
// ctx (e.g. the 4 s router timeout).
func ollamaChatOnce(ctx context.Context, s *Settings, req ollamaChatRequest) (ollamaChatResult, error) {
	body := map[string]any{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   false,
		"think":    false,
		"options":  ollamaOptions(s, req.Temperature, req.NumPredict),
	}
	if s.KeepAlive != "" {
		body["keep_alive"] = s.KeepAlive
	}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
	}
	if req.Format != nil {
		body["format"] = req.Format
	}
	var out struct {
		Message struct {
			Content   string           `json:"content"`
			ToolCalls []ollamaToolCall `json:"tool_calls"`
		} `json:"message"`
	}
	if err := ollamaPostJSON(ctx, "/api/chat", body, &out); err != nil {
		return ollamaChatResult{}, err
	}
	return ollamaChatResult{Content: out.Message.Content, ToolCalls: out.Message.ToolCalls}, nil
}

// ollamaEmbed returns one embedding per input via /api/embed. Embedding
// models are separate from the chat model, so num_ctx is not sent.
func ollamaEmbed(ctx context.Context, s *Settings, model string, inputs []string) ([][]float32, error) {
	if model == "" {
		return nil, errors.New("no embedding model configured")
	}
	body := map[string]any{"model": model, "input": inputs}
	if s.KeepAlive != "" {
		body["keep_alive"] = s.KeepAlive
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := ollamaPostJSON(ctx, "/api/embed", body, &out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) != len(inputs) {
		return nil, fmt.Errorf("ollama returned %d embeddings for %d inputs", len(out.Embeddings), len(inputs))
	}
	return out.Embeddings, nil
}

// ollamaPostJSON posts body to the Ollama endpoint and decodes the JSON
// answer into out (responses are capped at 8 MB).
func ollamaPostJSON(ctx context.Context, path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ollamaBase()+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ollamaClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("%w: %s %d: %s", errOllamaStatus, path, resp.StatusCode, msg)
	}
	return json.Unmarshal(data, out)
}

// ---------------------------------------------------------------------------
// Warm models (Section 4.11)

// psCacheTTL is how long the /api/ps answer (loaded models) is trusted.
const psCacheTTL = 30 * time.Second

var psCache struct {
	sync.Mutex
	base   string
	at     time.Time
	models map[string]bool
}

// ollamaModelName normalises "qwen3.5" to "qwen3.5:latest" for comparison.
func ollamaModelName(m string) string {
	m = strings.ToLower(strings.TrimSpace(m))
	if m != "" && !strings.Contains(m, ":") {
		m += ":latest"
	}
	return m
}

// ollamaModelWarm reports whether Ollama has model loaded right now
// (/api/ps, cached 30 s). Any error counts as cold.
func ollamaModelWarm(ctx context.Context, model string) bool {
	base := ollamaBase()
	want := ollamaModelName(model)
	psCache.Lock()
	if psCache.base == base && time.Since(psCache.at) < psCacheTTL && psCache.models != nil {
		ok := psCache.models[want]
		psCache.Unlock()
		return ok
	}
	psCache.Unlock()

	models := map[string]bool{}
	pctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if req, err := http.NewRequestWithContext(pctx, http.MethodGet, base+"/api/ps", nil); err == nil {
		if resp, err := ollamaClient().Do(req); err == nil {
			var ps struct {
				Models []struct {
					Name  string `json:"name"`
					Model string `json:"model"`
				} `json:"models"`
			}
			if resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ps) == nil {
				for _, m := range ps.Models {
					models[ollamaModelName(m.Name)] = true
					models[ollamaModelName(m.Model)] = true
				}
			}
			resp.Body.Close()
		}
	}
	psCache.Lock()
	psCache.base, psCache.at, psCache.models = base, time.Now(), models
	psCache.Unlock()
	return models[want]
}

// resetOllamaCaches forgets the /api/ps answer and the warm-up throttle
// (tests, and after settings change the models).
func resetOllamaCaches() {
	psCache.Lock()
	psCache.models, psCache.at = nil, time.Time{}
	psCache.Unlock()
	warmState.Lock()
	warmState.last = time.Time{}
	warmState.Unlock()
}

// warmEvery throttles POST /api/warm.
const warmEvery = 60 * time.Second

var warmState struct {
	sync.Mutex
	last time.Time
}

// warmTargets are the chat models (persona, router) and the embedding model
// to keep loaded, without duplicates.
func warmTargets(s *Settings) (chat []string, embed string) {
	seen := map[string]bool{}
	for _, m := range []string{s.LocalModel, s.RouterModel} {
		if n := ollamaModelName(m); n != "" && !seen[n] {
			seen[n] = true
			chat = append(chat, m)
		}
	}
	return chat, s.EmbedModel
}

// warmModels loads the models with the same keep_alive and num_ctx every
// later call uses, so the first real turn does not pay the load.
func warmModels(ctx context.Context, s *Settings) {
	chat, embed := warmTargets(s)
	for _, m := range chat {
		body := map[string]any{
			"model":    m,
			"messages": []chatMessage{},
			"stream":   false,
			"options":  ollamaOptions(s, s.Temperature, 0),
		}
		if s.KeepAlive != "" {
			body["keep_alive"] = s.KeepAlive
		}
		var discard json.RawMessage
		if err := ollamaPostJSON(ctx, "/api/chat", body, &discard); err != nil && ctx.Err() == nil {
			log.Printf("warm %s: %v", m, err)
		}
	}
	if embed != "" {
		if _, err := ollamaEmbed(ctx, s, embed, []string{"hermes"}); err != nil && ctx.Err() == nil {
			log.Printf("warm %s: %v", embed, err)
		}
	}
	psCache.Lock()
	psCache.models = nil // re-read which models are loaded now
	psCache.Unlock()
}

// warmDone is signalled after each background warm-up (tests wait on it).
var warmDone = func() {}

// handleWarm (POST /api/warm) starts loading the models in the background
// and answers at once; at most one warm-up per minute.
func handleWarm(w http.ResponseWriter, r *http.Request) {
	warmState.Lock()
	if !warmState.last.IsZero() && time.Since(warmState.last) < warmEvery {
		warmState.Unlock()
		writeJSON(w, http.StatusOK, map[string]bool{"warming": false})
		return
	}
	warmState.last = time.Now()
	warmState.Unlock()
	s := loadSettings()
	done := warmDone
	go func() {
		defer done()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		warmModels(ctx, s)
	}()
	writeJSON(w, http.StatusAccepted, map[string]bool{"warming": true})
}
