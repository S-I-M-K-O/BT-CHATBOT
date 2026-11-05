package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestSSEChunks(t *testing.T) {
	cases := map[string][]string{
		"":                  nil,
		"Hello":             {"Hello"},
		"Hello world":       {"Hello ", "world"},
		"  lead and trail ": {"  lead ", "and ", "trail "},
		"a\n\nb":            {"a\n\n", "b"},
		"Grüße, Pilot! 🤖":   {"Grüße, ", "Pilot! ", "🤖"},
	}
	for in, want := range cases {
		got := sseChunks(in)
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("sseChunks(%q) = %q, want %q", in, got, want)
		}
		if strings.Join(got, "") != in {
			t.Errorf("sseChunks(%q) does not round-trip", in)
		}
	}
}

// sseEvent is one decoded "data:" line of the chat stream.
type sseEvent struct {
	Intent string `json:"intent"`
	Delta  string `json:"delta"`
	Image  string `json:"image"`
	Error  string `json:"error"`
	Done   bool   `json:"done"`
}

func parseSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var out []sseEvent
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev sseEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("bad SSE payload %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

func joinDeltas(evs []sseEvent) string {
	var b strings.Builder
	for _, e := range evs {
		b.WriteString(e.Delta)
	}
	return b.String()
}

func TestSSEReplyIsWordChunked(t *testing.T) {
	rec := httptest.NewRecorder()
	text := "Copy that, Pilot. Standing by for orders."
	newSSE(rec).reply(text)
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	evs := parseSSE(t, rec.Body.String())
	if got := joinDeltas(evs); got != text {
		t.Errorf("deltas = %q, want %q", got, text)
	}
	if n := len(evs); n != len(strings.Fields(text))+1 {
		t.Errorf("expected one event per word plus done, got %d", n)
	}
	if !evs[len(evs)-1].Done {
		t.Error("last event must be done")
	}
}

func TestSSEFailSendsErrorFirst(t *testing.T) {
	rec := httptest.NewRecorder()
	newSSE(rec).fail("Core offline.")
	evs := parseSSE(t, rec.Body.String())
	if len(evs) < 3 || evs[0].Error != "Core offline." {
		t.Fatalf("expected an error event first, got %+v", evs)
	}
	if joinDeltas(evs) != "Core offline." || !evs[len(evs)-1].Done {
		t.Errorf("unexpected events %+v", evs)
	}
}

// nonFlusher is a ResponseWriter that does not implement http.Flusher.
type nonFlusher struct {
	h    http.Header
	body strings.Builder
}

func (n *nonFlusher) Header() http.Header         { return n.h }
func (n *nonFlusher) Write(b []byte) (int, error) { return n.body.Write(b) }
func (n *nonFlusher) WriteHeader(int)             {}

func TestSSEWithoutFlusherDoesNotPanic(t *testing.T) {
	w := &nonFlusher{h: http.Header{}}
	newSSE(w).reply("still works")
	if !strings.Contains(w.body.String(), `"done":true`) {
		t.Error("expected done event")
	}
}
