package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeForSpeechEnglish(t *testing.T) {
	cases := map[string]string{
		"**Copy that**, Pilot.":                      "Copy that, Pilot.",
		"# Status\nAll systems _nominal_":            "Status. All systems nominal",
		"See https://example.com/a?b=c for details.": "See link for details.",
		"Visit www.titanfall.com now":                "Visit link now",
		"Read [the docs](https://go.dev/doc) first.": "Read the docs first.",
		"It is 21°C in Berlin.":                      "It is 21 degrees Celsius in Berlin.",
		"It is 70 °F outside":                        "It is 70 degrees Fahrenheit outside",
		"Turn 90° left":                              "Turn 90 degrees left",
		"Humidity at 45%.":                           "Humidity at 45 percent.",
		"Salt & pepper":                              "Salt and pepper",
		"Tom &amp; Jerry":                            "Tom and Jerry",
		"Wind 12 km/h":                               "Wind 12 kilometers per hour",
		"Ready 🤖🚀 for action ✅":                      "Ready for action",
		"Flag 🇩🇪 test 👍🏽!":                           "Flag test!",
		"- one\n- two\n- three":                      "one. two. three",
		"1. alpha\n2. beta":                          "1. alpha. 2. beta",
		"> quoted wisdom":                            "quoted wisdom",
		"Use `go test` now":                          "Use go test now",
		"snake_case_name":                            "snake case name",
		"Code:\n```go\nfmt.Println(1)\n```\nDone.":   "Code: See the code on screen. Done.",
		"a   lot\t\tof\n\n\nspace":                   "a lot of. space",
		"<think>hidden</think>Answer":                "hidden Answer",
		"| a | b |\n|---|---|\n| 1 | 2 |":            "a b. 1 2",
		"":                                           "",
		"🤖":                                          "",
	}
	for in, want := range cases {
		if got := normalizeForSpeech(in, "en"); got != want {
			t.Errorf("normalizeForSpeech(%q, en)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestNormalizeForSpeechGerman(t *testing.T) {
	cases := map[string]string{
		"Es sind 21°C in Berlin.":     "Es sind 21 Grad Celsius in Berlin.",
		"Luftfeuchtigkeit 60%":        "Luftfeuchtigkeit 60 Prozent",
		"Katz & Maus":                 "Katz und Maus",
		"Siehe https://wetter.de":     "Siehe Link",
		"Wind mit 20 km/h aus Westen": "Wind mit 20 Kilometer pro Stunde aus Westen",
		"Drehe um 45°":                "Drehe um 45 Grad",
	}
	for in, want := range cases {
		if got := normalizeForSpeech(in, "de"); got != want {
			t.Errorf("normalizeForSpeech(%q, de)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestNormalizeForSpeechTruncates(t *testing.T) {
	long := strings.Repeat("Protocol one: link to Pilot. ", 200)
	got := normalizeForSpeech(long, "en")
	if n := len([]rune(got)); n > maxSpeechRunes || n < maxSpeechRunes/2 {
		t.Errorf("length %d", n)
	}
	if !strings.HasSuffix(got, ".") {
		t.Errorf("expected cut at a sentence end, got ...%q", got[len(got)-20:])
	}
}

func TestTTSCacheLRU(t *testing.T) {
	c := newTTSCache(2, 1000)
	c.Add("a", []byte("1"))
	c.Add("b", []byte("2"))
	c.Get("a") // a becomes most recent
	c.Add("c", []byte("3"))
	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted (least recently used)")
	}
	if _, ok := c.Get("a"); !ok {
		t.Error("a should be cached")
	}
	if _, ok := c.Get("c"); !ok {
		t.Error("c should be cached")
	}
	if c.Len() != 2 || c.Bytes() != 2 {
		t.Errorf("len %d bytes %d", c.Len(), c.Bytes())
	}
}

func TestTTSCacheByteBound(t *testing.T) {
	c := newTTSCache(100, 10)
	c.Add("a", make([]byte, 4))
	c.Add("b", make([]byte, 4))
	c.Add("c", make([]byte, 4)) // 12 > 10: evict a
	if _, ok := c.Get("a"); ok {
		t.Error("a should be evicted by the byte bound")
	}
	if c.Bytes() != 8 {
		t.Errorf("bytes = %d", c.Bytes())
	}
	c.Add("huge", make([]byte, 11)) // larger than the whole cache: ignored
	if _, ok := c.Get("huge"); ok || c.Len() != 2 {
		t.Error("oversized entry must not be cached")
	}
	c.Add("b", make([]byte, 1)) // replace updates the byte count
	if c.Bytes() != 5 {
		t.Errorf("bytes after replace = %d", c.Bytes())
	}
}

func TestTTSCacheConcurrent(t *testing.T) {
	c := newTTSCache(16, 1<<20)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				k := fmt.Sprint((i + j) % 40)
				c.Add(k, []byte(k))
				c.Get(k)
			}
		}(i)
	}
	wg.Wait()
	if c.Len() > 16 {
		t.Errorf("len %d exceeds bound", c.Len())
	}
}

func TestFlightGroupDedup(t *testing.T) {
	var g flightGroup
	var calls int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]string, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err, _ := g.Do(context.Background(), "k", func(context.Context) ([]byte, error) {
				atomic.AddInt32(&calls, 1)
				<-release
				return []byte("v"), nil
			})
			if err == nil {
				results[i] = string(v)
			}
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls != 1 {
		t.Errorf("fn ran %d times, want 1", calls)
	}
	for i, r := range results {
		if r != "v" {
			t.Errorf("result %d = %q", i, r)
		}
	}
	// Caller cancellation does not hang.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err, _ := g.Do(ctx, "slow", func(context.Context) ([]byte, error) { time.Sleep(100 * time.Millisecond); return nil, nil }); err == nil {
		t.Error("expected context error")
	}
}

// tinyWAV is a valid 44-byte PCM WAV header with no samples.
func tinyWAV() []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint32(22050))
	binary.Write(&b, binary.LittleEndian, uint32(44100))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(0))
	return b.Bytes()
}

type fakePiper struct {
	dir string
}

// installFakePiper writes a shell script that behaves like the Piper CLI:
// it records its arguments and stdin, then writes a WAV to --output_file.
func installFakePiper(t *testing.T) *fakePiper {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake piper is a shell script")
	}
	dir := t.TempDir()
	wav := filepath.Join(dir, "out.wav")
	if err := os.WriteFile(wav, tinyWAV(), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--output_file" ]; then out="$a"; fi
  prev="$a"
done
echo "$@" >> "` + dir + `/args.log"
cat > "` + dir + `/stdin.txt"
if [ -n "$FAKE_PIPER_SLEEP" ]; then sleep "$FAKE_PIPER_SLEEP"; fi
if [ -n "$FAKE_PIPER_FAIL" ]; then echo "synthetic failure" >&2; exit 3; fi
cat "` + wav + `" > "$out"
`
	bin := filepath.Join(dir, "piper")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	voices := filepath.Join(dir, "voices")
	for key, model := range piperVoices {
		_ = key
		p := filepath.Join(voices, model)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("onnx"), 0o644)
		os.WriteFile(p+".json", []byte("{}"), 0o644)
	}
	t.Setenv("PIPER_BIN", bin)
	t.Setenv("VOICE_DIR", voices)
	t.Setenv("FAKE_PIPER_SLEEP", "")
	t.Setenv("FAKE_PIPER_FAIL", "")
	useTempSettings(t)
	ttsAudioCache = newTTSCache(128, 48<<20)
	return &fakePiper{dir: dir}
}

func (f *fakePiper) calls(t *testing.T) []string {
	b, err := os.ReadFile(filepath.Join(f.dir, "args.log"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (f *fakePiper) stdin(t *testing.T) string {
	b, _ := os.ReadFile(filepath.Join(f.dir, "stdin.txt"))
	return string(b)
}

func postTTS(h http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/tts", strings.NewReader(body)))
	return rec
}

func TestTTSWithFakePiper(t *testing.T) {
	fp := installFakePiper(t)
	h := newMux()

	rec := postTTS(h, `{"text":"**Hello** Pilot, it is 20°C 🤖","voice":"BT7274","speed":1.25}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("Content-Type %q", ct)
	}
	if rec.Header().Get("X-TTS-Cache") != "miss" {
		t.Errorf("first call should be a cache miss")
	}
	if !bytes.Equal(rec.Body.Bytes(), tinyWAV()) {
		t.Error("body is not the synthesised WAV")
	}
	calls := fp.calls(t)
	if len(calls) != 1 {
		t.Fatalf("piper calls: %v", calls)
	}
	for _, want := range []string{"--length_scale 0.800", "--noise_scale 0.6", "--noise_w 0.8", "--sentence_silence 0.25", "BT7274.onnx", "--config"} {
		if !strings.Contains(calls[0], want) {
			t.Errorf("args %q missing %q", calls[0], want)
		}
	}
	if got := strings.TrimSpace(fp.stdin(t)); got != "Hello Pilot, it is 20 degrees Celsius" {
		t.Errorf("piper stdin = %q", got)
	}

	// Same request again: served from cache, piper not run again.
	rec = postTTS(h, `{"text":"**Hello** Pilot, it is 20°C 🤖","voice":"BT7274","speed":1.25}`)
	if rec.Code != http.StatusOK || rec.Header().Get("X-TTS-Cache") != "hit" {
		t.Errorf("second call: status %d cache %q", rec.Code, rec.Header().Get("X-TTS-Cache"))
	}
	if n := len(fp.calls(t)); n != 1 {
		t.Errorf("piper ran %d times, want 1", n)
	}

	// Different speed is a different cache entry.
	postTTS(h, `{"text":"**Hello** Pilot, it is 20°C 🤖","voice":"BT7274","speed":0.5}`)
	calls = fp.calls(t)
	if len(calls) != 2 || !strings.Contains(calls[1], "--length_scale 2.000") {
		t.Errorf("speed 0.5 calls: %v", calls)
	}
}

func TestTTSGermanAndSettingsSpeed(t *testing.T) {
	fp := installFakePiper(t)
	saveSettings(&Settings{PiperVoice: "JarvisDE", VoiceSpeed: 2, VoiceMode: "piper"})
	rec := postTTS(newMux(), `{"text":"Es sind 5°C & 80%"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	calls := fp.calls(t)
	if len(calls) != 1 || !strings.Contains(calls[0], "JarvisDE.onnx") || !strings.Contains(calls[0], "--length_scale 0.500") {
		t.Errorf("calls %v", calls)
	}
	if got := strings.TrimSpace(fp.stdin(t)); got != "Es sind 5 Grad Celsius und 80 Prozent" {
		t.Errorf("stdin %q", got)
	}
}

func TestTTSDedupConcurrent(t *testing.T) {
	fp := installFakePiper(t)
	t.Setenv("FAKE_PIPER_SLEEP", "0.3")
	h := newMux()
	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = postTTS(h, `{"text":"Trust me.","voice":"PDA"}`).Code
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("request %d: status %d", i, c)
		}
	}
	if n := len(fp.calls(t)); n != 1 {
		t.Errorf("piper ran %d times for identical concurrent requests, want 1", n)
	}
}

func TestTTSErrors(t *testing.T) {
	installFakePiper(t)
	h := newMux()
	cases := []struct {
		body string
		want int
	}{
		{`{bad`, http.StatusBadRequest},
		{`{"text":""}`, http.StatusBadRequest},
		{`{"text":"   "}`, http.StatusBadRequest},
		{`{"text":"🤖🤖"}`, http.StatusBadRequest},
		{`{"text":"hi","engine":"browser"}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		if rec := postTTS(h, c.body); rec.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.body, rec.Code, c.want)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tts", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status %d", rec.Code)
	}

	t.Setenv("FAKE_PIPER_FAIL", "1")
	rec = postTTS(h, `{"text":"this will fail"}`)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("synth failure: %d %s", rec.Code, rec.Body)
	}
}

func TestTTSPiperMissing(t *testing.T) {
	installFakePiper(t)
	t.Setenv("PIPER_BIN", filepath.Join(t.TempDir(), "does-not-exist"))
	rec := postTTS(newMux(), `{"text":"hello"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") || !strings.Contains(rec.Body.String(), "Piper") {
		t.Errorf("expected JSON error, got %q", rec.Body)
	}
}

// TestFlightCancelsWhenAllWaitersLeave: an aborted TTS request must kill its
// Piper run (freeing a piper slot) once no other client is waiting on it, but
// keep it alive while a second client still is.
func TestFlightCancelsWhenAllWaitersLeave(t *testing.T) {
	var g flightGroup
	started := make(chan struct{})
	cancelled := make(chan struct{})
	work := func(ctx context.Context) ([]byte, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	errs := make(chan error, 2)
	go func() { _, err, _ := g.Do(ctx1, "k", work); errs <- err }()
	<-started
	go func() {
		_, err, _ := g.Do(ctx2, "k", func(context.Context) ([]byte, error) { t.Error("second caller must share"); return nil, nil })
		errs <- err
	}()
	time.Sleep(20 * time.Millisecond) // let caller 2 join

	cancel1()
	<-errs
	select {
	case <-cancelled:
		t.Fatal("run cancelled while another caller was still waiting")
	case <-time.After(50 * time.Millisecond):
	}
	cancel2()
	<-errs
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("run not cancelled after the last waiter left")
	}
}
