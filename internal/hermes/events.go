package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The event hub pushes server-side happenings (a reminder is due, a list
// changed) to open browser tabs. Durable events are kept in events.json until
// a tab acknowledges them, so a reminder that fires while no tab is open is
// replayed on the next connect. The SSE handler (GET /api/events) builds on
// hubSubscribe and hubReplay.

type eventsFile struct {
	Version int         `json:"version"` // 1
	NextID  int64       `json:"nextId"`
	Pending []*HubEvent `json:"pending"` // durable, not yet acked
}

type HubEvent struct {
	ID     int64           `json:"id,omitempty"` // > 0 only for durable events
	Type   string          `json:"type"`
	At     time.Time       `json:"at"`
	Person string          `json:"person,omitempty"`
	Data   json.RawMessage `json:"data"`
	Missed bool            `json:"missed,omitempty"` // set on replay when At < now-2min
}

const (
	hubMaxPending     = 200
	hubPendingMaxAge  = 7 * 24 * time.Hour
	hubMaxSubscribers = 32
	// hubMaxListsSubs caps lists-token subscribers, so a phone with only
	// the lists token cannot take every slot and starve the main UI.
	hubMaxListsSubs = hubMaxSubscribers / 2
	hubSubBuffer    = 32
	hubMissedAfter  = 2 * time.Minute
)

var errHubFull = errors.New("too many event subscribers")

var eventsStore = &fileStore[eventsFile]{
	Name:    "events.json",
	Version: 1,
	New:     func() *eventsFile { return &eventsFile{Version: 1, NextID: 1} },
}

// hubSub is one connected event-stream client.
type hubSub struct {
	ch        chan *HubEvent
	done      chan struct{} // closed when the hub drops the subscriber
	lists     bool          // authorised by the lists token only
	speakHere bool          // leader tab with "speak here" on
	once      sync.Once
}

func (s *hubSub) close() { s.once.Do(func() { close(s.done) }) }

// Done is closed when the hub drops this subscriber (slow, or shutdown).
func (s *hubSub) Done() <-chan struct{} { return s.done }

// Events delivers the subscriber's events.
func (s *hubSub) Events() <-chan *HubEvent { return s.ch }

var hub = struct {
	mu   sync.Mutex
	subs map[*hubSub]struct{}
}{subs: map[*hubSub]struct{}{}}

// listsEventTypes is all a lists-token subscriber may see.
var listsEventTypes = wordSet("hello", "list.changed")

// hubSubscribe registers a subscriber; lists limits it to list events.
func hubSubscribe(lists, speakHere bool) (*hubSub, error) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.subs) >= hubMaxSubscribers {
		return nil, errHubFull
	}
	if lists {
		n := 0
		for s := range hub.subs {
			if s.lists {
				n++
			}
		}
		if n >= hubMaxListsSubs {
			return nil, errHubFull
		}
	}
	s := &hubSub{ch: make(chan *HubEvent, hubSubBuffer), done: make(chan struct{}), lists: lists, speakHere: speakHere}
	hub.subs[s] = struct{}{}
	return s, nil
}

func hubUnsubscribe(s *hubSub) {
	hub.mu.Lock()
	delete(hub.subs, s)
	hub.mu.Unlock()
	s.close()
}

// deliver fans ev out. A subscriber whose buffer is full is dropped: its
// client reconnects and replays durable events instead of stalling the hub.
func deliver(ev *HubEvent) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for s := range hub.subs {
		if s.lists && !listsEventTypes[ev.Type] {
			continue
		}
		select {
		case s.ch <- ev:
		default:
			delete(hub.subs, s)
			s.close()
		}
	}
}

func newHubEvent(typ, person string, data any) *HubEvent {
	raw, err := json.Marshal(data)
	if err != nil || data == nil {
		raw = json.RawMessage("{}")
	}
	return &HubEvent{Type: typ, At: nowFunc().UTC(), Person: person, Data: raw}
}

// hubPublish sends an ephemeral event to every connected subscriber.
func hubPublish(typ, person string, data any) {
	deliver(newHubEvent(typ, person, data))
}

// hubPublishDurable persists the event until acked and delivers it; returns
// its id, or 0 when it could not be stored (it is still delivered live).
func hubPublishDurable(typ, person string, data any) int64 {
	ev := newHubEvent(typ, person, data)
	err := eventsStore.Update(func(st *eventsFile) error {
		if st.NextID < 1 {
			st.NextID = 1
		}
		ev.ID = st.NextID
		st.NextID++
		cutoff := nowFunc().Add(-hubPendingMaxAge)
		kept := st.Pending[:0]
		for _, p := range st.Pending {
			if p.At.After(cutoff) {
				kept = append(kept, p)
			}
		}
		kept = append(kept, ev)
		if len(kept) > hubMaxPending {
			kept = kept[len(kept)-hubMaxPending:]
		}
		st.Pending = kept
		return nil
	})
	if err != nil {
		log.Printf("events: could not persist %s: %v", typ, err)
		ev.ID = 0
	}
	deliver(ev)
	return ev.ID
}

// hubReplay returns pending durable events with id > since, flagging the
// ones older than two minutes as missed. Events older than seven days are
// left out (they are pruned on the next durable publish).
func hubReplay(since int64) []*HubEvent {
	var out []*HubEvent
	now := nowFunc()
	cutoff := now.Add(-hubPendingMaxAge)
	_ = eventsStore.View(func(st *eventsFile) error {
		for _, p := range st.Pending {
			if p.ID > since && p.At.After(cutoff) {
				c := *p
				c.Missed = now.Sub(p.At) > hubMissedAfter
				out = append(out, &c)
			}
		}
		return nil
	})
	return out
}

// hubLastID is the newest durable event id handed out (for "hello").
func hubLastID() int64 {
	var id int64
	_ = eventsStore.View(func(st *eventsFile) error {
		id = st.NextID - 1
		return nil
	})
	if id < 0 {
		id = 0
	}
	return id
}

// hubAck removes acknowledged durable events; unknown ids are ignored.
func hubAck(ids []int64) int {
	if len(ids) == 0 {
		return 0
	}
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	return removePending(func(ev *HubEvent) bool { return want[ev.ID] })
}

// hubPurgePerson drops pending durable events of a deleted person.
func hubPurgePerson(personID string) int {
	if personID == "" {
		return 0
	}
	return removePending(func(ev *HubEvent) bool { return ev.Person == personID })
}

func removePending(match func(*HubEvent) bool) int {
	n := 0
	err := eventsStore.Update(func(st *eventsFile) error {
		kept := st.Pending[:0]
		for _, p := range st.Pending {
			if match(p) {
				n++
				continue
			}
			kept = append(kept, p)
		}
		st.Pending = kept
		if n == 0 {
			return errNoChange
		}
		return nil
	})
	if err != nil {
		log.Printf("events: could not update pending events: %v", err)
		return 0
	}
	return n
}

// hubCloseAll disconnects every subscriber (server shutdown).
func hubCloseAll() {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for s := range hub.subs {
		delete(hub.subs, s)
		s.close()
	}
}

// hubSubscribers counts full and lists-only subscribers, and the full ones
// that will speak reminders aloud.
func hubSubscribers() (full, lists, speakHere int) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for s := range hub.subs {
		if s.lists {
			lists++
			continue
		}
		full++
		if s.speakHere {
			speakHere++
		}
	}
	return full, lists, speakHere
}

// ---------------------------------------------------------------------------
// HTTP: GET /api/events (SSE) and POST /api/events/ack (Section 3.2)

// hubPingEvery keeps proxies and NAT state alive and finds dead peers.
var hubPingEvery = 25 * time.Second

// listsScopeKey marks a request that only the scoped lists token let in.
type listsScopeKey struct{}

// withListsScope tags r as authorised by the lists token only.
func withListsScope(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), listsScopeKey{}, true))
}

// listsScopeOnly reports whether r came in on the lists token alone.
func listsScopeOnly(r *http.Request) bool {
	v, _ := r.Context().Value(listsScopeKey{}).(bool)
	return v
}

// writeHubEvent writes one SSE frame. Durable events carry an id line, so
// Last-Event-ID tracks only them.
func writeHubEvent(w http.ResponseWriter, ev *HubEvent) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return nil // skip an unencodable event rather than drop the stream
	}
	var frame strings.Builder
	if ev.ID > 0 {
		fmt.Fprintf(&frame, "id: %d\n", ev.ID)
	}
	fmt.Fprintf(&frame, "event: %s\ndata: %s\n\n", sanitizeEventType(ev.Type), b)
	_, err = w.Write([]byte(frame.String()))
	return err
}

// sanitizeEventType keeps an event name on one line.
func sanitizeEventType(t string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, t)
}

// eventsSince reads ?since= or the Last-Event-ID header (0 = everything).
func eventsSince(r *http.Request) int64 {
	raw := r.URL.Query().Get("since")
	if raw == "" {
		raw = r.Header.Get("Last-Event-ID")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func handleEvents(w http.ResponseWriter, r *http.Request) {
	lists := listsScopeOnly(r) || r.URL.Query().Get("scope") == "lists"
	speakHere := !lists && r.URL.Query().Get("speak") == "1"
	sub, err := hubSubscribe(lists, speakHere)
	if err != nil {
		writeJSONErrorCode(w, http.StatusServiceUnavailable, "busy", "too many event subscribers")
		return
	}
	defer hubUnsubscribe(sub)

	// The stream outlives the server's WriteTimeout; statusRecorder.Unwrap
	// lets the controller reach the real connection.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flush := func() error { return rc.Flush() }
	hello, _ := json.Marshal(map[string]any{"serverTime": nowFunc().In(settingsLocation(loadSettings())).Format(time.RFC3339), "lastId": hubLastID()})
	if _, err := fmt.Fprintf(w, "retry: 3000\n\nevent: hello\ndata: %s\n\n", hello); err != nil {
		return
	}
	// Subscribed before the replay, so nothing published in between is
	// lost; durable events already replayed are skipped when they arrive
	// live as well.
	var replayed int64
	if !lists {
		for _, ev := range hubReplay(eventsSince(r)) {
			if err := writeHubEvent(w, ev); err != nil {
				return
			}
			replayed = max(replayed, ev.ID)
		}
	}
	if flush() != nil {
		return
	}

	ping := time.NewTicker(hubPingEvery)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.Done():
			return // dropped (slow) or server shutdown; the client reconnects
		case ev := <-sub.Events():
			if ev.ID > 0 && ev.ID <= replayed {
				continue
			}
			if writeHubEvent(w, ev) != nil || flush() != nil {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || flush() != nil {
				return
			}
		}
	}
}

// handleEventsAck removes acknowledged durable events: {"ids":[42,43]}.
func handleEventsAck(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) > 500 {
		writeJSONErrorCode(w, http.StatusBadRequest, "bad_request", "bad request")
		return
	}
	if eventsStore.ReadOnly() {
		writeJSONErrorCode(w, http.StatusServiceUnavailable, "read_only", errStoreReadOnly.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"acked": hubAck(req.IDs)})
}

func init() {
	registerStatus("events", func(context.Context) any {
		full, lists, speakHere := hubSubscribers()
		return map[string]any{"subscribers": full + lists, "full": full, "lists": lists, "speakHere": speakHere}
	})
}
