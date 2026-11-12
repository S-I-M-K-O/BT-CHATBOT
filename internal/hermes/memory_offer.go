package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
)

// memory_offer.go: automatic memory asks first. The background extractor
// (processExtraction) no longer saves what it finds; it offers it:
// "Soll ich mir merken: Du wohnst in der Hauptstraße 12 in Berlin?". The
// offer is a confirm pending for the client that said it, announced with a
// "memory.offer" hub event that carries no text (the tab fetches the
// question from GET /api/memory/offer). "Ja" saves it, "nein" drops it.

// memoryAskFirst: offer extracted facts instead of saving them (tests of the
// saving itself turn it off).
var memoryAskFirst = true

const memoryOfferTTL = 2 * time.Minute

type memoryOffer struct {
	Text     string `json:"text"`
	Scope    string `json:"scope"`
	Kind     string `json:"kind"`
	Lang     string `json:"lang"`
	Expires  string `json:"expires,omitempty"`
	Replaces string `json:"replaces,omitempty"`
}

type memoryOfferArgs struct {
	Facts []memoryOffer `json:"facts"`
}

// memoryOfferQuestion: "Soll ich mir merken: Du magst Lasagne?"
func memoryOfferQuestion(offers []memoryOffer, lang string) string {
	parts := make([]string, 0, len(offers))
	for _, o := range offers {
		s := toSecondPerson(o.Text, scopeLabel(o.Scope, o.Lang), o.Lang)
		parts = append(parts, strings.TrimRight(strings.TrimSpace(s), ".!"))
	}
	body := strings.Join(parts, "; ")
	return pick(lang, "Should I remember that? "+body+".", "Soll ich mir das merken? "+body+".")
}

// offerMemories stores the confirm pending (unless the client is already in
// the middle of another question) and tells the client's tabs.
func offerMemories(job extractJob, offers []memoryOffer) {
	if len(offers) == 0 || job.ClientID == "" {
		return
	}
	args, err := json.Marshal(memoryOfferArgs{Facts: offers})
	if err != nil {
		return
	}
	q := memoryOfferQuestion(offers, job.Lang)
	p := &Pending{Kind: "confirm", Tool: "memory_offer_save", Args: args, Prompt: q,
		Speaker: job.Speaker, Expires: nowFunc().Add(memoryOfferTTL)}
	pendingMu.Lock()
	if old, busy := pendings[job.ClientID]; busy && nowFunc().Before(old.Expires) {
		pendingMu.Unlock()
		log.Printf("memory: offer dropped, the client is answering another question")
		return
	}
	pendings[job.ClientID] = p
	pendingMu.Unlock()
	hubPublish("memory.offer", job.Scope, map[string]any{
		"clientId": job.ClientID, "expiresIn": int(memoryOfferTTL / time.Second),
	})
}

// runMemoryOfferSave saves the confirmed facts.
func runMemoryOfferSave(t *Turn, raw json.RawMessage) Reply {
	var a memoryOfferArgs
	_ = json.Unmarshal(raw, &a)
	s := t.Settings
	if s == nil || !s.MemoryEnabled {
		return memoryDisabledReply(t)
	}
	ctx := t.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var saved []Memory
	skip := map[string]bool{}
	for i, o := range a.Facts {
		if i >= 3 {
			break
		}
		text := cleanText(o.Text, memoryMaxRunes)
		if text == "" || !personExists(o.Scope) {
			continue
		}
		kind := o.Kind
		if !memoryKinds[kind] {
			kind = "other"
		}
		if o.Replaces != "" {
			_, after, err := memoryModify(o.Replaces, func(x *Memory) error {
				if x.Person != o.Scope {
					return errMemoryNotFound
				}
				x.Text, x.Lang, x.Kind, x.Expires, x.UpdatedAt = text, o.Lang, kind, o.Expires, nowFunc().UTC()
				return nil
			})
			if err == nil {
				embedMemoryTexts(ctx, s, []Memory{after})
				saved = append(saved, after)
				skip[after.ID] = true
				continue
			}
		}
		m, _, err := saveFact(ctx, s, Memory{Person: o.Scope, Text: text, Kind: kind, Source: "auto",
			ClientID: t.ClientID, Lang: o.Lang, Expires: o.Expires}, skip)
		if err != nil {
			if errors.Is(err, errMemoryLimit) {
				return Reply{Text: pick(t.Lang, "My memory is full. Please delete something in the Memory tab.",
					"Mein Gedächtnis ist voll. Lösch bitte etwas im Gedächtnis-Tab.")}
			}
			log.Printf("memory: saving an offered fact: %v", err)
			continue
		}
		saved = append(saved, m)
		skip[m.ID] = true
	}
	if len(saved) == 0 {
		return Reply{Text: pick(t.Lang, "That didn't work, nothing was saved.", "Das hat nicht geklappt, ich habe nichts gespeichert.")}
	}
	byScope := map[string][]string{}
	for _, m := range saved {
		byScope[m.Person] = append(byScope[m.Person], m.ID)
	}
	for sc, ids := range byScope {
		publishMemorySaved(sc, t.ClientID, ids...)
	}
	return Reply{Text: pick(t.Lang, "Okay, I'll remember that.", "Okay, ich merke es mir.")}
}

// handleMemoryOffer returns the open offer question of a client.
func handleMemoryOffer(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("clientId")
	if !clientIDPattern.MatchString(id) {
		writeJSONErrorCode(w, http.StatusBadRequest, "client", "bad client id")
		return
	}
	pendingMu.Lock()
	p := pendings[id]
	var q string
	var left time.Duration
	if p != nil && p.Tool == "memory_offer_save" && nowFunc().Before(p.Expires) {
		q, left = p.Prompt, p.Expires.Sub(nowFunc())
	}
	pendingMu.Unlock()
	if q == "" {
		writeJSONErrorCode(w, http.StatusNotFound, "not_found", "no open offer")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"question": q, "expiresIn": int(left / time.Second)})
}

func init() {
	registerTool(Tool{Name: "memory_offer_save", SideEffect: true, Access: memoryWriteAccess, Run: runMemoryOfferSave})
	registerRoute("GET /api/memory/offer", requireUnlock(handleMemoryOffer))
}
