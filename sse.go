package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"unicode"
)

// sseStream writes the /api/chat server-sent-event protocol:
//
//	data: {"intent":"image"} | {"delta":"..."} | {"image":"/gen/..."} |
//	      {"error":"..."} | {"done":true}
//
// It sets the streaming headers exactly once and never panics when the
// underlying writer cannot flush.
type sseStream struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func newSSE(w http.ResponseWriter) *sseStream {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	f, _ := w.(http.Flusher)
	return &sseStream{w: w, flusher: f}
}

func (s *sseStream) flush() {
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// event writes one JSON event and flushes it.
func (s *sseStream) event(obj any) {
	b, err := json.Marshal(obj)
	if err != nil {
		return
	}
	fmt.Fprintf(s.w, "data: %s\n\n", b)
	s.flush()
}

// text streams text as word-sized deltas: natural typing feel in the UI with
// far fewer events than one per character.
func (s *sseStream) text(text string) {
	for _, c := range sseChunks(text) {
		s.event(map[string]any{"delta": c})
	}
}

func (s *sseStream) done() { s.event(map[string]any{"done": true}) }

// reply streams a complete answer and closes the stream.
func (s *sseStream) reply(text string) {
	s.text(text)
	s.done()
}

// fail sends an error event (so the UI can style the bubble) followed by the
// human readable text and closes the stream.
func (s *sseStream) fail(text string) {
	s.event(map[string]any{"error": text})
	s.reply(text)
}

// sseChunks splits text into word chunks, each carrying its trailing
// whitespace. Concatenating the chunks yields the input unchanged.
func sseChunks(text string) []string {
	var out []string
	start := 0
	inSpace := false
	seenWord := false
	for i, r := range text {
		space := unicode.IsSpace(r)
		if !space && inSpace && seenWord {
			out = append(out, text[start:i])
			start = i
		}
		if !space {
			seenWord = true
		}
		inSpace = space
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}
