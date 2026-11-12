package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// lists_overview.go: "Welche Listen gibt es?" (list_overview) and
// "Lösch alle Listen" (list_clear_all, asks first, undo restores every list).

var (
	listOverviewRe = regexp.MustCompile(`(?i)^(?:` +
		`(?:welche|was\s+für)\s+listen\s+(?:gibt\s+es|habe\s+ich|hab\s+ich|haben\s+wir|hast\s+du|existieren|sind\s+da|kennst\s+du)(?:\s+(?:denn|alles|gerade|so|eigentlich|überhaupt))*` +
		`|(?:zeig|zeige|nenn|nenne|lies|sag)\s+(?:(?:mir|uns|mal|bitte)\s+)*(?:alle|meine|unsere|die)\s+listen(?:\s+(?:vor|auf))?` +
		`|(?:what|which)\s+lists\s+(?:do\s+(?:i|we|you)\s+have|are\s+there|exist|have\s+(?:i|we)\s+got)` +
		`|(?:show|list|tell)\s+(?:me\s+|us\s+)?(?:all\s+)?(?:my|our|the|all)\s+lists` +
		`)$`)
	listClearAllRe = regexp.MustCompile(`(?i)^(?:` +
		`(?:lösch|lösche|leer|leere|entfern|entferne)\s+(?:(?:mal|bitte)\s+)*alle\s+(?:meine\s+|unsere\s+)?listen` +
		`|(?:lösch|lösche|entfern|entferne)\s+(?:(?:mal|bitte)\s+)*(?:alles|alle\s+(?:einträge|sachen|punkte|dinge))\s+(?:von|auf|aus)\s+(?:allen|den|meinen|unseren)\s+listen` +
		`|mach\s+alle\s+listen\s+leer` +
		`|(?:clear|empty|wipe)\s+(?:out\s+)?(?:all\s+(?:the\s+|my\s+|our\s+)?|every\s+)lists?` +
		`|delete\s+(?:everything|all\s+(?:items|entries|things))\s+(?:from|on|in)\s+(?:all\s+(?:the\s+|my\s+|our\s+)?|every\s+)lists?` +
		`)$`)
)

func matchListOverview(t *Turn) (json.RawMessage, bool) {
	if listOverviewRe.MatchString(listCore(t.Query)) {
		return json.RawMessage(`{}`), true
	}
	return nil, false
}

func matchListClearAll(t *Turn) (json.RawMessage, bool) {
	if listClearAllRe.MatchString(listCore(t.Query)) {
		return json.RawMessage(`{}`), true
	}
	return nil, false
}

// listCountPhrase: "Einkaufsliste (2 Einträge)" / "shopping list (empty)".
func listCountPhrase(l *List, lang string) string {
	n := len(openTexts(l))
	var c string
	switch {
	case n == 0:
		c = pick(lang, "empty", "leer")
	case n == 1:
		c = pick(lang, "1 item", "1 Eintrag")
	default:
		c = strconv.Itoa(n) + pick(lang, " items", " Einträge")
	}
	return listNamed(l, lang) + " (" + c + ")"
}

func runListOverview(t *Turn, _ json.RawMessage) Reply {
	_, lists, err := listsSnapshot()
	if err != nil {
		return listsErrorReply(t, err)
	}
	if len(lists) == 0 {
		return Reply{Text: pick(t.Lang, "There are no lists yet.", "Es gibt noch keine Listen.")}
	}
	parts := make([]string, len(lists))
	for i, l := range lists {
		parts[i] = listCountPhrase(l, t.Lang)
	}
	if len(lists) == 1 {
		return Reply{Text: pick(t.Lang, "There is one list: ", "Es gibt eine Liste: ") + parts[0] + "."}
	}
	n := strconv.Itoa(len(lists))
	return Reply{Text: pick(t.Lang, "There are "+n+" lists: ", "Es gibt "+n+" Listen: ") + joinAnd(parts, t.Lang) + "."}
}

type clearAllArgs struct {
	Confirmed bool `json:"confirmed,omitempty"`
}

// runListClearAll empties every list after a spoken confirmation. One undo
// puts every removed item back on its list.
func runListClearAll(t *Turn, raw json.RawMessage) Reply {
	var a clearAllArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &a)
	}
	_, lists, err := listsSnapshot()
	if err != nil {
		return listsErrorReply(t, err)
	}
	total := 0
	for _, l := range lists {
		total += len(l.Items)
	}
	if total == 0 {
		return Reply{Text: pick(t.Lang, "All lists are already empty.", "Alle Listen sind schon leer.")}
	}
	if !a.Confirmed {
		if t.ClientID == "" {
			return Reply{Text: pick(t.Lang, "Please clear the lists in the Lists panel.", "Leere die Listen bitte im Bereich Listen.")}
		}
		n := strconv.Itoa(total)
		q := pick(t.Lang, "Should I delete all "+n+" items from all lists?", "Soll ich alle "+n+" Einträge von allen Listen löschen?")
		if total == 1 {
			q = pick(t.Lang, "Should I delete the one item on your lists?", "Soll ich den einen Eintrag auf deinen Listen löschen?")
		}
		return t.Ask(Pending{Kind: "confirm", Tool: "list_clear_all", Args: json.RawMessage(`{"confirmed":true}`)}, q)
	}
	saved := map[string][]savedItem{}
	removed := 0
	for _, l := range lists {
		items, _, err := clearList(l.ID, false)
		if errors.Is(err, errNoChange) || errors.Is(err, errListNotFound) {
			continue
		}
		if err != nil {
			return listsErrorReply(t, err)
		}
		saved[l.ID] = items
		removed += len(items)
	}
	if removed == 0 {
		return Reply{Text: pick(t.Lang, "All lists are already empty.", "Alle Listen sind schon leer.")}
	}
	t.PushUndo(pick(t.Lang, "all lists cleared", "alle Listen geleert"), func(context.Context) error {
		var errs []string
		for id, items := range saved {
			if err := restoreItems(id, items); err != nil {
				errs = append(errs, err.Error())
			}
		}
		if len(errs) > 0 {
			return errors.New(strings.Join(errs, "; "))
		}
		return nil
	})
	return Reply{Text: pick(t.Lang,
		"Done, all lists are empty. Say “undo” if that was a mistake.",
		"Erledigt, alle Listen sind leer. Sag „rückgängig“, falls das ein Versehen war.")}
}

func init() {
	registerTool(Tool{
		Name:        "list_overview",
		Description: "Say which lists exist (shopping, to-do and any others) and how many items each has.",
		Params:      func(*Turn) json.RawMessage { return json.RawMessage(`{"type":"object","properties":{}}`) },
		Gate:        []string{"liste", "listen", "list", "lists"},
		Run:         runListOverview,
	})
	registerTool(Tool{Name: "list_clear_all", SideEffect: true, Run: runListClearAll})
	// before the single-list fast paths (Order 50): "lösch alles von allen Listen"
	registerFastPath(FastPath{Name: "list_overview", Order: 49, Tool: "list_overview", Match: matchListOverview})
	registerFastPath(FastPath{Name: "list_clear_all", Order: 49, Tool: "list_clear_all", Match: matchListClearAll})
}
