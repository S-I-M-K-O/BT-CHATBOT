package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"
)

// The local LLM tool router (Section 4.5). A neutral prompt, temperature 0,
// no persona and no history beyond the user's own words: the router decides
// whether the current utterance asks for one of the registered tools. Router
// text is always discarded, because it may falsely claim an action.

// Router timeouts: warm when /api/ps lists the model, else a cold load.
var (
	routerWarmTimeout = 4 * time.Second
	routerColdTimeout = 10 * time.Second
)

const routerPromptTemplate = `You are the command router of HERMES, a home voice assistant. Current time: %s.
Decide whether the user's LAST message asks HERMES to perform one of the available actions.
Call a tool ONLY when the user clearly asks for that action. For greetings, questions, chit-chat, opinions or general knowledge, call no tool and answer with the single word NONE.
Copy time expressions verbatim from the user's message into "when_text"; never compute dates yourself.
Copy item names, titles and reminder texts in the user's language without translating them.`

// routerModel: RouterModel, else the persona model.
func routerModel(s *Settings) string {
	if s.RouterModel != "" {
		return s.RouterModel
	}
	return s.LocalModel
}

// routerTimeLine: "Friday, 6 November 2026, 14:30 (Europe/Berlin, UTC+01:00)".
func routerTimeLine(t *Turn) string {
	loc := t.Loc
	if loc == nil {
		loc = settingsLocation(t.Settings)
	}
	now := t.Now
	if now.IsZero() {
		now = nowFunc()
	}
	now = now.In(loc)
	return now.Format("Monday, 2 January 2006, 15:04") + " (" + loc.String() + ", UTC" + now.Format("-07:00") + ")"
}

// cueParts splits a gate or imperative cue on "…" / "..." into tokenised
// parts that must appear in this order ("trag … ein").
func cueParts(cue string) [][]string {
	cue = strings.ReplaceAll(cue, "...", "…")
	var parts [][]string
	for _, p := range strings.Split(cue, "…") {
		if tok := tokenize(p); len(tok) > 0 {
			parts = append(parts, tok)
		}
	}
	return parts
}

// hasCue reports whether the tokenised cue occurs in tokens, its "…" parts
// in order with anything in between. Cues are tokenised like the query, so
// "don't forget" and "Füg … hinzu" work.
func hasCue(tokens []string, cue string) bool {
	parts := cueParts(cue)
	if len(parts) == 0 {
		return false
	}
	from := 0
	for _, p := range parts {
		found := -1
		for i := from; i+len(p) <= len(tokens); i++ {
			if phraseAt(tokens, i, p) {
				found = i
				break
			}
		}
		if found < 0 {
			return false
		}
		from = found + len(p)
	}
	return true
}

func hasAnyCue(tokens []string, cues []string) bool {
	for _, c := range cues {
		if hasCue(tokens, c) {
			return true
		}
	}
	return false
}

// gateMatches reports whether any enabled router tool's phrase gate (or
// GateFunc) fires on the query. Chit-chat never pays for a router call.
func gateMatches(t *Turn) bool {
	for _, tool := range registeredTools() {
		if tool.Params == nil || !tool.enabled(t) {
			continue
		}
		if hasAnyCue(t.Tokens, tool.Gate) || (tool.GateFunc != nil && tool.GateFunc(t)) {
			return true
		}
	}
	return false
}

// routerTools lists every enabled tool with a schema. Access is checked
// after the call, so an uncertain speaker can still be asked who they are.
func routerTools(t *Turn) (tools []any, byName map[string]*Tool) {
	byName = map[string]*Tool{}
	for _, tool := range registeredTools() {
		if tool.Params == nil || !tool.enabled(t) {
			continue
		}
		params := tool.Params(t)
		if len(params) == 0 || !json.Valid(params) {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  params,
			},
		})
		byName[tool.Name] = tool
	}
	return tools, byName
}

// routerMessages: the neutral prompt, the previous exchange only when it was
// a tool turn of this speaker on this client (the user's own words plus a Go
// line naming the tool), and the current query. Never assistant model text,
// link markers, attachments or documents.
func routerMessages(t *Turn) []chatMessage {
	msgs := []chatMessage{{Role: "system", Content: strings.Replace(routerPromptTemplate, "%s", routerTimeLine(t), 1)}}
	if n := len(t.Tagged); n >= 3 {
		u, a := t.Tagged[n-3], t.Tagged[n-2]
		toolTurn := strings.HasPrefix(a.Src, "tool:") || strings.HasPrefix(a.Src, "fast:")
		if u.Role == "user" && a.Role == "assistant" && toolTurn && !u.Att && !a.Att &&
			u.Sp == t.Speaker.ID && a.Sp == t.Speaker.ID {
			if name, ok := lastToolFor(t.ClientID); ok {
				msgs = append(msgs,
					chatMessage{Role: "user", Content: u.Content},
					chatMessage{Role: "assistant", Content: "(done: " + name + ")"})
			}
		}
	}
	return append(msgs, chatMessage{Role: "user", Content: t.Query})
}

// routerArgs normalises tool-call arguments to a JSON object. Ollama sends
// an object; some models send a JSON string holding one.
func routerArgs(raw json.RawMessage) json.RawMessage {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`)
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil && json.Valid([]byte(s)) && strings.HasPrefix(strings.TrimSpace(s), "{") {
			return json.RawMessage(s)
		}
		return json.RawMessage(`{}`)
	}
	if raw[0] != '{' || !json.Valid(raw) {
		return json.RawMessage(`{}`)
	}
	return raw
}

// runRouter asks the router model for a tool call and runs it, or turns it
// into a spoken offer. It returns false to let the pipeline continue: no
// tool, an unknown tool, an error or a timeout.
func runRouter(t *Turn) bool {
	tools, byName := routerTools(t)
	if len(tools) == 0 {
		return false
	}
	s := t.Settings
	model := routerModel(s)
	timeout := routerColdTimeout
	if ollamaModelWarm(t.Ctx, model) {
		timeout = routerWarmTimeout
	}
	ctx, cancel := context.WithTimeout(t.Ctx, timeout)
	defer cancel()
	res, err := ollamaChatOnce(ctx, s, ollamaChatRequest{
		Model:       model,
		Messages:    routerMessages(t),
		Tools:       tools,
		Temperature: 0,
		NumPredict:  256,
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			log.Printf("router: no answer within %s", timeout)
		} else {
			log.Printf("router: %v", err)
		}
		return false
	}
	var tool *Tool
	var args json.RawMessage
	for _, c := range res.ToolCalls {
		if tl := byName[c.Function.Name]; tl != nil {
			tool, args = tl, routerArgs(c.Function.Arguments)
			break
		}
	}
	if tool == nil {
		return false // text (even one claiming success), NONE or an unknown tool
	}
	t.toolName = tool.Name
	t.handledBy = "tool:" + tool.Name
	if !t.checkAccess(tool, args) {
		writeReply(t, refuse(t, tool.Name, args))
		return true
	}
	if tool.Name == "home_control" {
		if r, refused := haRefuseSentence(t); refused {
			writeReply(t, r)
			return true
		}
	}
	if tool.SideEffect && !hasAnyCue(t.Tokens, tool.Imperative) {
		// A statement ("Mir ist kalt") is not a command: ask first.
		t.handledBy = "offer"
		writeReply(t, t.Ask(Pending{Kind: "confirm", Tool: tool.Name, Args: args}, routerOffer(t, tool, args)))
		return true
	}
	writeReply(t, tool.Run(t, args))
	return true
}

// routerOffer is the tool's own question, or a neutral one.
func routerOffer(t *Turn, tool *Tool, args json.RawMessage) string {
	if tool.Offer != nil {
		if q := strings.TrimSpace(tool.Offer(t, args)); q != "" {
			return q
		}
	}
	return pick(t.Lang, "Should I do that for you?", "Soll ich das für dich erledigen?")
}

// ---------------------------------------------------------------------------
// Previous tool per client (Section 4.5): the router context line and the
// snooze fast path read it.

const lastToolTTL = 2 * time.Minute

type lastToolEntry struct {
	name string
	at   time.Time
}

var lastTools = struct {
	sync.Mutex
	m map[string]lastToolEntry
}{m: map[string]lastToolEntry{}}

// noteLastTool remembers the tool a client's turn just ran.
func noteLastTool(clientID, name string) {
	if clientID == "" || name == "" {
		return
	}
	now := nowFunc()
	lastTools.Lock()
	defer lastTools.Unlock()
	for k, e := range lastTools.m {
		if now.Sub(e.at) > lastToolTTL {
			delete(lastTools.m, k)
		}
	}
	lastTools.m[clientID] = lastToolEntry{name: name, at: now}
}

// lastToolFor returns the tool the client ran within the last two minutes.
func lastToolFor(clientID string) (string, bool) {
	lastTools.Lock()
	defer lastTools.Unlock()
	e, ok := lastTools.m[clientID]
	if !ok || nowFunc().Sub(e.at) > lastToolTTL {
		return "", false
	}
	return e.name, true
}

func init() {
	registerStatus("router", func(context.Context) any {
		s := loadSettings()
		return map[string]any{"enabled": s.RouterEnabled, "model": routerModel(s)}
	})
}
