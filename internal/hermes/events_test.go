package hermes

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func resetHub(t *testing.T) {
	t.Helper()
	hubCloseAll()
	t.Cleanup(hubCloseAll)
}

func TestHubDurableReplayAckAndPurge(t *testing.T) {
	useTempData(t)
	resetHub(t)
	start := time.Now()
	old := nowFunc
	nowFunc = func() time.Time { return start }
	t.Cleanup(func() { nowFunc = old })

	id1 := hubPublishDurable("reminder.due", "p_anna", map[string]any{"text": "Pizza"})
	id2 := hubPublishDurable("reminder.due", "p_ben", nil)
	if id1 != 1 || id2 != 2 || hubLastID() != 2 {
		t.Fatalf("ids %d %d last %d", id1, id2, hubLastID())
	}
	nowFunc = func() time.Time { return start.Add(3 * time.Minute) }
	evs := hubReplay(1)
	if len(evs) != 1 || evs[0].ID != 2 || !evs[0].Missed || string(evs[0].Data) != "{}" {
		t.Fatalf("replay = %+v", evs)
	}
	if n := hubPurgePerson("p_anna"); n != 1 {
		t.Errorf("purge = %d", n)
	}
	if n := hubAck([]int64{2, 99}); n != 1 {
		t.Errorf("ack = %d", n)
	}
	if len(hubReplay(0)) != 0 {
		t.Error("nothing should be pending")
	}
}

func TestHubSubscribersScopesAndSlowDrop(t *testing.T) {
	useTempData(t)
	resetHub(t)
	full, _ := hubSubscribe(false, true)
	lists, _ := hubSubscribe(true, false)
	if f, l, sh := hubSubscribers(); f != 1 || l != 1 || sh != 1 {
		t.Fatalf("counts %d %d %d", f, l, sh)
	}
	hubPublish("memory.saved", "", map[string]any{"count": 1})
	hubPublish("list.changed", "", map[string]any{"listId": "shopping", "rev": 3})
	if ev := <-full.Events(); ev.Type != "memory.saved" {
		t.Errorf("full got %s", ev.Type)
	}
	if ev := <-lists.Events(); ev.Type != "list.changed" {
		t.Errorf("lists subscriber must only see list events, got %s", ev.Type)
	}

	// A subscriber that never reads is dropped instead of blocking the hub.
	for i := 0; i < hubSubBuffer+1; i++ {
		hubPublish("people.changed", "", nil)
	}
	select {
	case <-full.Done():
	default:
		t.Error("slow subscriber not dropped")
	}
	hubUnsubscribe(lists)
	if f, l, _ := hubSubscribers(); f != 0 || l != 0 {
		t.Errorf("counts after drop %d %d", f, l)
	}
}

func TestHubSubscriberCap(t *testing.T) {
	resetHub(t)
	for i := 0; i < hubMaxSubscribers; i++ {
		if _, err := hubSubscribe(false, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := hubSubscribe(false, false); err != errHubFull {
		t.Errorf("33rd subscriber: %v", err)
	}
}

// Phones holding only the lists token must not be able to take every slot
// and lock the main UI out of reminders.
func TestHubListsSubscribersCannotFillTheHub(t *testing.T) {
	resetHub(t)
	n := 0
	for i := 0; i < hubMaxSubscribers; i++ {
		if _, err := hubSubscribe(true, false); err == nil {
			n++
		}
	}
	if n >= hubMaxSubscribers {
		t.Fatalf("lists subscribers took all %d slots", n)
	}
	for i := n; i < hubMaxSubscribers; i++ {
		if _, err := hubSubscribe(false, false); err != nil {
			t.Fatalf("full subscriber %d refused: %v", i, err)
		}
	}
	if _, err := hubSubscribe(false, false); err != errHubFull {
		t.Errorf("33rd subscriber: %v", err)
	}
}

// sseClient reads an event stream frame by frame.
type sseClient struct {
	t      *testing.T
	resp   *http.Response
	frames chan string
}

// openEvents connects to path on srv and collects frames ("id: …\nevent:
// …\ndata: …" or ": ping") in the background.
func openEvents(t *testing.T, srv *httptest.Server, path string, header http.Header) *sseClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	c := &sseClient{t: t, resp: resp, frames: make(chan string, 64)}
	t.Cleanup(func() { cancel(); resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		return c
	}
	go func() {
		defer close(c.frames)
		br := bufio.NewReader(resp.Body)
		var frame []string
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\n")
			if line == "" {
				if len(frame) > 0 {
					c.frames <- strings.Join(frame, "\n")
					frame = nil
				}
				continue
			}
			frame = append(frame, line)
		}
	}()
	return c
}

// next returns the next frame that is not a ping (unless pings are wanted).
func (c *sseClient) next(wantPing bool) string {
	c.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f, ok := <-c.frames:
			if !ok {
				c.t.Fatal("stream closed")
			}
			if strings.HasPrefix(f, "retry:") || (strings.HasPrefix(f, ":") && !wantPing) {
				continue
			}
			return f
		case <-deadline:
			c.t.Fatal("no frame within 5 s")
		}
	}
}

// frameData decodes a frame's data line.
func frameData(t *testing.T, frame string) map[string]any {
	t.Helper()
	for _, l := range strings.Split(frame, "\n") {
		if strings.HasPrefix(l, "data: ") {
			var m map[string]any
			if err := json.Unmarshal([]byte(l[6:]), &m); err != nil {
				t.Fatalf("bad data in %q: %v", frame, err)
			}
			return m
		}
	}
	t.Fatalf("no data line in %q", frame)
	return nil
}

// eventsServer runs the full handler stack with a 1 s WriteTimeout, like a
// stricter production server: the stream must clear the deadline.
func eventsServer(t *testing.T) *httptest.Server {
	t.Helper()
	oldPing := hubPingEvery
	hubPingEvery = 300 * time.Millisecond
	srv := httptest.NewUnstartedServer(newMux())
	srv.Config.WriteTimeout = time.Second
	srv.Start()
	t.Cleanup(func() {
		hubCloseAll()
		srv.Close()
		hubPingEvery = oldPing
	})
	return srv
}

func TestEventsStreamReplayLivePingAndWriteDeadline(t *testing.T) {
	useTempData(t)
	useTempSettings(t)
	resetHub(t)
	start := time.Now()
	old := nowFunc
	nowFunc = func() time.Time { return start.Add(-3 * time.Minute) }
	id1 := hubPublishDurable("reminder.due", "p_anna", map[string]any{"reminderId": "r_1"})
	nowFunc = old
	id2 := hubPublishDurable("reminder.due", "", map[string]any{"reminderId": "r_2"})

	srv := eventsServer(t)
	c := openEvents(t, srv, "/api/events?since=0", nil)
	if c.resp.StatusCode != http.StatusOK || !strings.HasPrefix(c.resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d %s", c.resp.StatusCode, c.resp.Header.Get("Content-Type"))
	}
	hello := c.next(false)
	if !strings.Contains(hello, "event: hello") || frameData(t, hello)["lastId"] != float64(id2) {
		t.Fatalf("hello = %q", hello)
	}
	f1 := c.next(false)
	d1 := frameData(t, f1)
	if !strings.HasPrefix(f1, "id: 1\n") || d1["id"] != float64(id1) || d1["missed"] != true {
		t.Errorf("replayed old event = %q", f1)
	}
	f2 := c.next(false)
	if d2 := frameData(t, f2); d2["id"] != float64(id2) || d2["missed"] != nil {
		t.Errorf("replayed fresh event = %q", f2)
	}

	// Past the server's WriteTimeout the stream still works.
	time.Sleep(1200 * time.Millisecond)
	if p := c.next(true); p != ": ping" {
		t.Errorf("want a ping, got %q", p)
	}
	hubPublish("people.changed", "", nil)
	live := c.next(false)
	if strings.HasPrefix(live, "id:") || !strings.Contains(live, "event: people.changed") {
		t.Errorf("ephemeral event = %q", live)
	}
	id3 := hubPublishDurable("reminder.due", "", nil)
	if f := c.next(false); !strings.HasPrefix(f, "id: 3\n") || frameData(t, f)["id"] != float64(id3) {
		t.Errorf("live durable event = %q", f)
	}

	// Last-Event-ID replays only what came after it.
	c2 := openEvents(t, srv, "/api/events", http.Header{"Last-Event-ID": {"2"}})
	c2.next(false) // hello
	if f := c2.next(false); frameData(t, f)["id"] != float64(id3) {
		t.Errorf("Last-Event-ID replay = %q", f)
	}

	// Ack removes events from later replays.
	rec := httptest.NewRecorder()
	newMux().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/events/ack", strings.NewReader(`{"ids":[1,2,3,77]}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"acked":3`) {
		t.Errorf("ack: %d %s", rec.Code, rec.Body)
	}
	if n := len(hubReplay(0)); n != 0 {
		t.Errorf("%d events left after ack", n)
	}
}

func TestEventsListsScopeGetsOnlyListEvents(t *testing.T) {
	useTempData(t)
	useTempSettings(t)
	resetHub(t)
	hubPublishDurable("reminder.due", "p_anna", map[string]any{"text": "Zahnarzt"})
	srv := eventsServer(t)
	c := openEvents(t, srv, "/api/events?scope=lists", nil)
	if f := c.next(false); !strings.Contains(f, "event: hello") {
		t.Fatalf("hello = %q", f)
	}
	hubPublishDurable("reminder.due", "p_anna", map[string]any{"text": "Zahnarzt"})
	hubPublish("memory.saved", "", map[string]any{"count": 1})
	hubPublish("list.changed", "", map[string]any{"listId": "shopping", "rev": 4})
	if f := c.next(false); !strings.Contains(f, "event: list.changed") || strings.Contains(f, "Zahnarzt") {
		t.Errorf("lists subscriber got %q", f)
	}
	if _, l, _ := hubSubscribers(); l != 1 {
		t.Errorf("lists subscribers = %d", l)
	}
}

func TestEventsSubscriberCapAnswers503(t *testing.T) {
	useTempData(t)
	useTempSettings(t)
	resetHub(t)
	for i := 0; i < hubMaxSubscribers; i++ {
		if _, err := hubSubscribe(false, false); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	newMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("33rd stream: %d", rec.Code)
	}
}

func TestHubReplaySkipsEventsOlderThanSevenDays(t *testing.T) {
	useTempData(t)
	resetHub(t)
	start := time.Now()
	old := nowFunc
	t.Cleanup(func() { nowFunc = old })
	nowFunc = func() time.Time { return start }
	hubPublishDurable("reminder.due", "", nil)
	nowFunc = func() time.Time { return start.Add(8 * 24 * time.Hour) }
	if evs := hubReplay(0); len(evs) != 0 {
		t.Errorf("week-old event replayed: %+v", evs[0])
	}
}
