package hermes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
	"unicode"
)

// sseStream writes the /api/chat server-sent-event protocol:
//
//	data: {"intent":"image"} | {"delta":"..."} | {"image":"/gen/..."} |
//	      {"error":"..."} | {"speaker":…} | {"card":…} | {"tool":…} |
//	      {"pending":…} | {"busy":true} | {"meta":…} | {"done":true}
//
// It sets the streaming headers exactly once and never panics when the
// underlying writer cannot flush. Writes are serialised, because the busy
// notice comes from a timer goroutine while the turn streams. Nothing is
// written after "done".
type sseStream struct {
	w       http.ResponseWriter
	flusher http.Flusher

	mu         sync.Mutex
	finished   bool      // "done" went out
	content    bool      // a delta or error went out (no busy notice needed)
	firstDelta time.Time // when the first delta went out (timing log)
	// beforeDone runs once, right before "done", while the stream is still
	// open; the chat turn uses it for the always-sent {"meta"} event.
	beforeDone func() any
}

// turnWriter is the ResponseWriter handed to the older chat handlers
// (Mistral, image search) that build their own stream with newSSE(w): they
// get the turn's stream instead, so the headers go out once and the meta
// event still precedes their "done".
type turnWriter struct {
	http.ResponseWriter
	t *Turn
}

func (w *turnWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *turnWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func newSSE(w http.ResponseWriter) *sseStream {
	if tw, ok := w.(*turnWriter); ok {
		return tw.t.SSE()
	}
	return newSSEStream(w)
}

func newSSEStream(w http.ResponseWriter) *sseStream {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	f, _ := w.(http.Flusher)
	return &sseStream{w: w, flusher: f}
}

// event writes one JSON event and flushes it.
func (s *sseStream) event(obj any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeLocked(obj)
}

func (s *sseStream) writeLocked(obj any) {
	if s.finished {
		return
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return
	}
	if m, ok := obj.(map[string]any); ok {
		if _, d := m["delta"]; d {
			if s.firstDelta.IsZero() {
				s.firstDelta = time.Now()
			}
			s.content = true
		}
		if _, e := m["error"]; e {
			s.content = true
		}
	}
	fmt.Fprintf(s.w, "data: %s\n\n", b)
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// text streams text as word-sized deltas: natural typing feel in the UI with
// far fewer events than one per character.
func (s *sseStream) text(text string) {
	for _, c := range sseChunks(text) {
		s.event(map[string]any{"delta": c})
	}
}

// done closes the stream, preceded by the beforeDone event (meta).
func (s *sseStream) done() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return
	}
	if fn := s.beforeDone; fn != nil {
		s.beforeDone = nil
		if ev := fn(); ev != nil {
			s.writeLocked(ev)
		}
	}
	s.writeLocked(map[string]any{"done": true})
	s.finished = true
}

// busy sends {"busy":true} unless an answer is already streaming or done.
func (s *sseStream) busy() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.content && !s.finished {
		s.writeLocked(map[string]any{"busy": true})
	}
}

// isDone reports whether "done" went out.
func (s *sseStream) isDone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finished
}

// firstDeltaAt is when the first delta went out (zero if none).
func (s *sseStream) firstDeltaAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstDelta
}

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

// busyAfter is how long a turn may stay silent before the UI is told it is
// still working (Section 4.11).
var busyAfter = 1500 * time.Millisecond

// watchBusy sends {"busy":true} if no delta went out within busyAfter. The
// returned stop func must be called before the handler returns; it waits for
// the watcher, so nothing is written to the response afterwards.
func (s *sseStream) watchBusy() (stop func()) {
	quit := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		timer := time.NewTimer(busyAfter)
		defer timer.Stop()
		select {
		case <-timer.C:
			s.busy()
		case <-quit:
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(quit)
			<-finished
		})
	}
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
