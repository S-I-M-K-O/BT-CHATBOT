package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// lists.go: to-do and shopping lists (SPEC 2.3, 3.6, 9 Feature 4).
//
// One small store (data/lists.json, 0600) holds the built-in "shopping" and
// "todo" lists plus up to 28 custom lists. Every mutation bumps a global rev
// and publishes the ephemeral hub event list.changed, which both the main
// page (hub client in hermes.js) and the phone page (/lists.html, lists-only
// event stream) use to refresh. Voice commands go through deterministic fast
// paths (order 50) and three router tools; lists are household data, so
// they are open to every speaker (Section 4.10), including guests. Nothing
// here ever leaves the machine: replies are templates, and list texts are only
// spoken back or shown.

const (
	listsFileName    = "lists.json"
	listsVersion     = 1
	listsMax         = 30  // lists in total, built-ins included
	listItemsMax     = 500 // items per list
	listItemRunes    = 200 // per item text
	listNameRunes    = 40  // per list name
	listBatchMax     = 50  // items per request or utterance
	listsBodyLimit   = 64 << 10
	listReadMax      = 20 // items read aloud before "… und N weitere"
	listLastTTL      = 10 * time.Minute
	listsShoppingID  = "shopping"
	listsTodoID      = "todo"
	listKindShopping = "shopping"
	listKindTodo     = "todo"
	listKindCustom   = "custom"
)

type listsFile struct {
	Version int     `json:"version"` // 1
	Rev     int64   `json:"rev"`     // bumps on every mutation
	Lists   []*List `json:"lists"`
}

type List struct {
	ID      string      `json:"id"`   // "shopping" | "todo" (built-in, undeletable) | "l_…"
	Name    string      `json:"name"` // display name; built-ins rendered per language in the UI
	Kind    string      `json:"kind"` // "shopping" | "todo" | "custom"
	Items   []*ListItem `json:"items"`
	Created time.Time   `json:"created"`
}

type ListItem struct {
	ID      string     `json:"id"`   // "i_…"
	Text    string     `json:"text"` // ≤200 runes
	Done    bool       `json:"done"`
	AddedBy string     `json:"addedBy,omitempty"` // person id
	AddedAt time.Time  `json:"addedAt"`
	DoneAt  *time.Time `json:"doneAt,omitempty"`
}

var listsStore = &fileStore[listsFile]{
	Name:    listsFileName,
	Version: listsVersion,
	New:     func() *listsFile { return &listsFile{Version: listsVersion} },
}

var (
	errListNotFound = errors.New("list not found")
	errItemNotFound = errors.New("item not found")
	errListBuiltin  = errors.New("the built-in lists cannot be deleted or renamed")
	errListExists   = errors.New("a list with this name already exists")
	errListLimit    = errors.New("too many lists (at most 30)")
	errListFull     = errors.New("the list is full (at most 500 items)")
	errListName     = errors.New("list name must be 1 to 40 characters")
	errItemText     = errors.New("item text must be 1 to 200 characters")
)

// ---------------------------------------------------------------------------
// Store

// ensureBuiltins drops null entries a hand-edited file may contain and makes
// sure shopping and todo exist (first load creates them). It runs inside
// every View and Update; the in-memory additions are saved by the next
// mutation.
func ensureBuiltins(st *listsFile) {
	st.Version = listsVersion
	lists := st.Lists[:0]
	for _, l := range st.Lists {
		if l == nil || l.ID == "" {
			continue
		}
		items := l.Items[:0]
		for _, it := range l.Items {
			if it != nil && it.ID != "" {
				items = append(items, it)
			}
		}
		l.Items = items
		switch l.ID {
		case listsShoppingID:
			l.Kind = listKindShopping
		case listsTodoID:
			l.Kind = listKindTodo
		default:
			l.Kind = listKindCustom
		}
		lists = append(lists, l)
	}
	st.Lists = lists
	now := nowFunc().UTC()
	if i := listIndex(st, listsTodoID); i < 0 {
		st.Lists = append([]*List{{ID: listsTodoID, Name: "To-do list", Kind: listKindTodo, Items: []*ListItem{}, Created: now}}, st.Lists...)
	}
	if i := listIndex(st, listsShoppingID); i < 0 {
		st.Lists = append([]*List{{ID: listsShoppingID, Name: "Shopping list", Kind: listKindShopping, Items: []*ListItem{}, Created: now}}, st.Lists...)
	}
}

func listIndex(st *listsFile, id string) int {
	for i, l := range st.Lists {
		if l.ID == id {
			return i
		}
	}
	return -1
}

func findList(st *listsFile, id string) *List {
	if i := listIndex(st, id); i >= 0 {
		return st.Lists[i]
	}
	return nil
}

func itemIndex(l *List, id string) int {
	for i, it := range l.Items {
		if it.ID == id {
			return i
		}
	}
	return -1
}

func isBuiltinList(id string) bool { return id == listsShoppingID || id == listsTodoID }

func copyItem(it *ListItem) *ListItem {
	c := *it
	if it.DoneAt != nil {
		d := *it.DoneAt
		c.DoneAt = &d
	}
	return &c
}

func copyList(l *List) *List {
	c := *l
	c.Items = make([]*ListItem, len(l.Items))
	for i, it := range l.Items {
		c.Items[i] = copyItem(it)
	}
	return &c
}

// listsSnapshot returns the current rev and a deep copy of every list.
func listsSnapshot() (int64, []*List, error) {
	var rev int64
	var out []*List
	err := listsStore.View(func(st *listsFile) error {
		ensureBuiltins(st)
		rev = st.Rev
		out = make([]*List, len(st.Lists))
		for i, l := range st.Lists {
			out[i] = copyList(l)
		}
		return nil
	})
	return rev, out, err
}

// listsMutate runs fn under the store lock. fn returns the id of the list it
// changed; errNoChange means nothing changed (no save, no event). On success
// the rev is bumped, the file saved and list.changed published. The returned
// rev is the current one either way.
func listsMutate(fn func(st *listsFile) (string, error)) (int64, error) {
	var rev int64
	var changed string
	err := listsStore.Update(func(st *listsFile) error {
		ensureBuiltins(st)
		id, err := fn(st)
		if err != nil {
			rev = st.Rev
			return err
		}
		st.Rev++
		rev, changed = st.Rev, id
		return nil
	})
	if err != nil {
		return 0, err
	}
	if changed != "" {
		hubPublish("list.changed", "", map[string]any{"listId": changed, "rev": rev})
	}
	return rev, nil
}

// foldItem is the duplicate key of an item or list name: case-folded with
// collapsed white space.
func foldItem(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// cleanListName validates a list name (1–40 runes after cleaning).
func cleanListName(s string) (string, error) {
	s = cleanText(strings.Join(strings.Fields(s), " "), listNameRunes+1)
	if s == "" || utf8.RuneCountInString(s) > listNameRunes {
		return "", errListName
	}
	return s, nil
}

// cleanItemText validates an item text from the API (1–200 runes).
func cleanItemText(s string) (string, error) {
	s = cleanText(strings.Join(strings.Fields(s), " "), listItemRunes+1)
	if s == "" || utf8.RuneCountInString(s) > listItemRunes {
		return "", errItemText
	}
	return s, nil
}

// listNameTaken: names are unique (case-folded), and a custom list may not
// take a built-in's name or one of its spoken aliases.
func listNameTaken(st *listsFile, name, exceptID string) bool {
	f := normListPhrase(name)
	if shoppingAliases[f] || todoAliases[f] || bareListWords[f] {
		return true
	}
	for _, l := range st.Lists {
		if l.ID != exceptID && !isBuiltinList(l.ID) && normListPhrase(l.Name) == f {
			return true
		}
	}
	return false
}

func createList(name string) (*List, int64, error) {
	name, err := cleanListName(name)
	if err != nil {
		return nil, 0, err
	}
	var out *List
	rev, err := listsMutate(func(st *listsFile) (string, error) {
		if len(st.Lists) >= listsMax {
			return "", errListLimit
		}
		if listNameTaken(st, name, "") {
			return "", errListExists
		}
		l := &List{ID: newID("l"), Name: name, Kind: listKindCustom, Items: []*ListItem{}, Created: nowFunc().UTC()}
		st.Lists = append(st.Lists, l)
		out = copyList(l)
		return l.ID, nil
	})
	return out, rev, err
}

func renameList(id, name string) (*List, int64, error) {
	name, err := cleanListName(name)
	if err != nil {
		return nil, 0, err
	}
	var out *List
	rev, err := listsMutate(func(st *listsFile) (string, error) {
		l := findList(st, id)
		switch {
		case l == nil:
			return "", errListNotFound
		case isBuiltinList(id):
			return "", errListBuiltin
		case listNameTaken(st, name, id):
			return "", errListExists
		}
		out = copyList(l)
		if l.Name == name {
			return "", errNoChange
		}
		l.Name = name
		out.Name = name
		return id, nil
	})
	return out, rev, err
}

func deleteList(id string) (int64, error) {
	return listsMutate(func(st *listsFile) (string, error) {
		i := listIndex(st, id)
		switch {
		case i < 0:
			return "", errListNotFound
		case isBuiltinList(id):
			return "", errListBuiltin
		}
		st.Lists = append(st.Lists[:i], st.Lists[i+1:]...)
		return id, nil
	})
}

// listAddResult reports what an add did with each requested text.
type listAddResult struct {
	List     *List       // copy after the change
	Added    []*ListItem // new items
	Reopened []savedItem // items that were done and are open again (state before)
	Existing []*ListItem // already open: not added again
	Items    []*ListItem // one entry per distinct requested text, in order
	Rev      int64
}

// savedItem remembers an item and its position for undo.
type savedItem struct {
	Index int
	Item  *ListItem
}

// addItems adds texts (already cleaned) to a list. An open item with the
// same case-folded text is returned as is; a done one is reopened, so a
// weekly "Milch" does not pile up. Either all new items fit or none is added.
func addItems(listID string, texts []string, addedBy string) (listAddResult, error) {
	var res listAddResult
	rev, err := listsMutate(func(st *listsFile) (string, error) {
		l := findList(st, listID)
		if l == nil {
			return "", errListNotFound
		}
		// The write-after-delete guard (Section 4.8): attribute only to a
		// person who still exists, checked under the store lock.
		by := addedBy
		if by != "" && !personExists(by) {
			by = ""
		}
		seen := map[string]bool{}
		type plan struct {
			text string
			idx  int // existing item index, -1 = new
		}
		var plans []plan
		newCount := 0
		for _, text := range texts {
			f := foldItem(text)
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			// Prefer an open item with this text, else reopen a done one.
			idx := -1
			for i, it := range l.Items {
				if foldItem(it.Text) != f {
					continue
				}
				if !it.Done {
					idx = i
					break
				}
				if idx < 0 {
					idx = i
				}
			}
			if idx < 0 {
				newCount++
			}
			plans = append(plans, plan{text, idx})
		}
		if len(plans) == 0 {
			return "", errItemText
		}
		if len(l.Items)+newCount > listItemsMax {
			return "", errListFull
		}
		now := nowFunc().UTC()
		changed := false
		for _, p := range plans {
			if p.idx >= 0 {
				it := l.Items[p.idx]
				if it.Done {
					res.Reopened = append(res.Reopened, savedItem{p.idx, copyItem(it)})
					it.Done, it.DoneAt = false, nil
					changed = true
				} else {
					res.Existing = append(res.Existing, copyItem(it))
				}
				res.Items = append(res.Items, copyItem(it))
				continue
			}
			it := &ListItem{ID: newID("i"), Text: p.text, AddedBy: by, AddedAt: now}
			l.Items = append(l.Items, it)
			res.Added = append(res.Added, copyItem(it))
			res.Items = append(res.Items, copyItem(it))
			changed = true
		}
		res.List = copyList(l)
		if !changed {
			return "", errNoChange
		}
		return listID, nil
	})
	res.Rev = rev
	return res, err
}

// patchItem changes an item's done state and/or text.
func patchItem(listID, itemID string, done *bool, text *string) (*ListItem, int64, error) {
	var out *ListItem
	rev, err := listsMutate(func(st *listsFile) (string, error) {
		l := findList(st, listID)
		if l == nil {
			return "", errListNotFound
		}
		i := itemIndex(l, itemID)
		if i < 0 {
			return "", errItemNotFound
		}
		it := l.Items[i]
		changed := false
		if text != nil && *text != it.Text {
			it.Text, changed = *text, true
		}
		if done != nil && *done != it.Done {
			it.Done, changed = *done, true
			if it.Done {
				now := nowFunc().UTC()
				it.DoneAt = &now
			} else {
				it.DoneAt = nil
			}
		}
		out = copyItem(it)
		if !changed {
			return "", errNoChange
		}
		return listID, nil
	})
	return out, rev, err
}

func deleteItem(listID, itemID string) (int64, error) {
	return listsMutate(func(st *listsFile) (string, error) {
		l := findList(st, listID)
		if l == nil {
			return "", errListNotFound
		}
		i := itemIndex(l, itemID)
		if i < 0 {
			return "", errItemNotFound
		}
		l.Items = append(l.Items[:i], l.Items[i+1:]...)
		return listID, nil
	})
}

// clearList removes every item (doneOnly: only the done ones) and returns
// them with their positions for undo.
func clearList(listID string, doneOnly bool) ([]savedItem, int64, error) {
	var removed []savedItem
	rev, err := listsMutate(func(st *listsFile) (string, error) {
		l := findList(st, listID)
		if l == nil {
			return "", errListNotFound
		}
		keep := make([]*ListItem, 0, len(l.Items))
		for i, it := range l.Items {
			if doneOnly && !it.Done {
				keep = append(keep, it)
				continue
			}
			removed = append(removed, savedItem{i, copyItem(it)})
		}
		if len(removed) == 0 {
			return "", errNoChange
		}
		l.Items = keep
		return listID, nil
	})
	return removed, rev, err
}

// restoreItems puts items back as they were saved: a missing item is
// re-inserted near its old position, an existing one gets its old state.
// Used by undo. Items beyond the list cap are dropped.
func restoreItems(listID string, saved []savedItem) error {
	_, err := listsMutate(func(st *listsFile) (string, error) {
		l := findList(st, listID)
		if l == nil {
			return "", errListNotFound
		}
		changed := false
		for _, s := range saved {
			if i := itemIndex(l, s.Item.ID); i >= 0 {
				l.Items[i] = copyItem(s.Item)
				changed = true
				continue
			}
			if len(l.Items) >= listItemsMax {
				continue
			}
			at := min(max(s.Index, 0), len(l.Items))
			l.Items = append(l.Items, nil)
			copy(l.Items[at+1:], l.Items[at:])
			l.Items[at] = copyItem(s.Item)
			changed = true
		}
		if !changed {
			return "", errNoChange
		}
		return listID, nil
	})
	return err
}

// removeItemIDs deletes the given items (undo of an add).
func removeItemIDs(listID string, ids []string) error {
	drop := wordSet(ids...)
	_, err := listsMutate(func(st *listsFile) (string, error) {
		l := findList(st, listID)
		if l == nil {
			return "", errListNotFound
		}
		keep := l.Items[:0]
		for _, it := range l.Items {
			if !drop[it.ID] {
				keep = append(keep, it)
			}
		}
		if len(keep) == len(l.Items) {
			return "", errNoChange
		}
		for i := len(keep); i < len(l.Items); i++ {
			l.Items[i] = nil
		}
		l.Items = keep
		return listID, nil
	})
	return err
}

// matchOpenItem finds the open item a spoken text means: the same text
// (case-folded), else the only open item that contains it as whole words or
// is contained in it ("Milch" → "2 Liter Milch"). -1 when none or ambiguous.
func matchOpenItem(l *List, text string) int {
	f := foldItem(text)
	if f == "" {
		return -1
	}
	for i, it := range l.Items {
		if !it.Done && foldItem(it.Text) == f {
			return i
		}
	}
	want := tokenize(f)
	found := -1
	for i, it := range l.Items {
		if it.Done {
			continue
		}
		have := tokenize(it.Text)
		if containsTokens(have, want) || containsTokens(want, have) {
			if found >= 0 {
				return -1
			}
			found = i
		}
	}
	return found
}

func containsTokens(have, want []string) bool {
	if len(want) == 0 || len(want) > len(have) {
		return false
	}
	for i := 0; i+len(want) <= len(have); i++ {
		if phraseAt(have, i, want) {
			return true
		}
	}
	return false
}

// listRemoveResult reports a voice removal.
type listRemoveResult struct {
	List     *List
	Removed  []savedItem // state before the change, for undo
	NotFound []string
	Rev      int64
}

// removeItemsByText marks the spoken items done (mode "done") or deletes
// them (mode "delete").
func removeItemsByText(listID string, texts []string, mode string) (listRemoveResult, error) {
	var res listRemoveResult
	rev, err := listsMutate(func(st *listsFile) (string, error) {
		l := findList(st, listID)
		if l == nil {
			return "", errListNotFound
		}
		// Match everything first, so the saved indices are the original
		// positions (undo re-inserts in ascending order).
		chosen := map[int]bool{}
		for _, text := range texts {
			i := matchOpenItem(l, text)
			if i < 0 || chosen[i] {
				if i < 0 {
					res.NotFound = append(res.NotFound, text)
				}
				continue
			}
			chosen[i] = true
		}
		if len(chosen) == 0 {
			res.List = copyList(l)
			return "", errNoChange
		}
		now := nowFunc().UTC()
		keep := make([]*ListItem, 0, len(l.Items))
		for i, it := range l.Items {
			if !chosen[i] {
				keep = append(keep, it)
				continue
			}
			res.Removed = append(res.Removed, savedItem{i, copyItem(it)})
			if mode == "delete" {
				continue
			}
			done := now
			it.Done, it.DoneAt = true, &done
			keep = append(keep, it)
		}
		l.Items = keep
		res.List = copyList(l)
		return listID, nil
	})
	res.Rev = rev
	return res, err
}

// clearPersonFromLists is the cascade hook (Section 4.8): items keep their
// text, they just no longer name who added them.
func clearPersonFromLists(personID string) (string, int) {
	if personID == "" {
		return "", 0
	}
	n := 0
	_, err := listsMutate(func(st *listsFile) (string, error) {
		changed := ""
		for _, l := range st.Lists {
			for _, it := range l.Items {
				if it.AddedBy == personID {
					it.AddedBy = ""
					n++
					changed = l.ID
				}
			}
		}
		if n == 0 {
			return "", errNoChange
		}
		return changed, nil
	})
	if err != nil {
		log.Printf("lists: clearing a deleted person: %v", err)
		return "listItems", 0
	}
	return "listItems", n
}

// ---------------------------------------------------------------------------
// Names, phrases and item splitting

var (
	shoppingAliases = wordSet("einkaufsliste", "einkaufszettel", "einkauf", "einkaufen", "einkäufe",
		"einkaufs liste", "shopping", "shopping list", "shoppinglist", "grocery", "groceries",
		"grocery list", "groceries list")
	todoAliases = wordSet("to do", "todo", "to do liste", "todo liste", "todoliste", "to do list",
		"todo list", "aufgaben", "aufgabenliste", "aufgaben liste", "tasks", "task list", "tasks list")
	bareListWords = wordSet("liste", "list")
	listPhraseDet = wordSet("die", "der", "den", "dem", "meine", "meiner", "meinen", "unsere",
		"unserer", "unseren", "the", "my", "our")
)

// normListPhrase lower-cases a list reference, turns hyphens into spaces and
// drops leading articles and possessives: "die To-do-Liste" → "to do liste".
func normListPhrase(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "-", " "))
	f := strings.Fields(s)
	for len(f) > 1 && listPhraseDet[f[0]] {
		f = f[1:]
	}
	return strings.Join(f, " ")
}

// stripListSuffix: "packliste" → "pack", "packing list" → "packing".
func stripListSuffix(s string) string {
	for _, suf := range []string{" liste", " list", "liste", "zettel"} {
		if strings.HasSuffix(s, suf) && len(s) > len(suf) {
			return strings.TrimSpace(strings.TrimSuffix(s, suf))
		}
	}
	return s
}

func looksLikeList(s string) bool {
	return strings.HasSuffix(s, "liste") || strings.HasSuffix(s, " list") || strings.HasSuffix(s, "zettel")
}

type listRefKind int

const (
	listRefNone    listRefKind = iota // not a list ("die Couch")
	listRefBare                       // "die Liste" / "the list": the client's last list, else shopping
	listRefKnown                      // a built-in alias or a custom list
	listRefUnknown                    // sounds like a list but none has that name ("Packliste")
)

// resolveListRef maps a spoken list reference to a list (SPEC 9.4 name
// resolution). Unknown names are never created by voice.
func resolveListRef(phrase string, lists []*List) (listRefKind, string) {
	p := normListPhrase(phrase)
	switch {
	case p == "":
		return listRefNone, ""
	case bareListWords[p]:
		return listRefBare, ""
	case shoppingAliases[p]:
		return listRefKnown, listsShoppingID
	case todoAliases[p]:
		return listRefKnown, listsTodoID
	}
	bare := stripListSuffix(p)
	for _, l := range lists {
		if isBuiltinList(l.ID) {
			continue
		}
		n := normListPhrase(l.Name)
		if n == p || n == bare || stripListSuffix(n) == bare {
			return listRefKnown, l.ID
		}
	}
	if looksLikeList(p) {
		return listRefUnknown, ""
	}
	return listRefNone, ""
}

var (
	itemSplitRe = regexp.MustCompile(`(?i)\s*(?:,|;|&|\s+(?:und|and|sowie)\s+)\s*`)
	// Leading articles and fillers dropped from each item.
	itemLeadWords = wordSet("und", "and", "sowie", "die", "der", "das", "den", "dem", "des", "ein", "eine", "einen", "einem",
		"einer", "etwas", "noch", "bitte", "auch", "mal", "some", "the", "a", "an", "any", "more",
		"also", "please", "too")
	itemTailWords = wordSet("bitte", "please", "noch", "auch", "too", "also", "danke", "thanks", "drauf")
	// Answers that name no item ("setz was auf die Liste").
	vagueItems = wordSet("was", "etwas", "was drauf", "noch was", "noch etwas", "something", "anything",
		"stuff", "sachen", "dinge", "items", "things", "something else")
)

// splitItems splits an item phrase on ",", ";", "&", "und", "and" and
// "sowie" and trims each item (SPEC 9.4). capitalise upper-cases the first
// letter (German nouns). At most listBatchMax distinct items.
func splitItems(s string, capitalise bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range itemSplitRe.Split(s, -1) {
		it := trimItem(part, capitalise)
		f := foldItem(it)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, it)
		if len(out) == listBatchMax {
			break
		}
	}
	return out
}

func trimItem(s string, capitalise bool) string {
	s = strings.Trim(strings.TrimSpace(s), " .,;:!?\"'„“”‚‘’«»()")
	words := strings.Fields(s)
	for len(words) > 1 && itemLeadWords[strings.ToLower(words[0])] {
		words = words[1:]
	}
	for len(words) > 1 && itemTailWords[strings.ToLower(words[len(words)-1])] {
		words = words[:len(words)-1]
	}
	s = cleanText(strings.Join(words, " "), listItemRunes)
	if len(words) == 1 && itemLeadWords[strings.ToLower(s)] {
		return ""
	}
	if capitalise {
		s = upperFirst(s)
	}
	return s
}

func upperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError || !unicode.IsLower(r) {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// listTitle is the display name in the reply language.
func listTitle(l *List, lang string) string {
	switch l.ID {
	case listsShoppingID:
		return pick(lang, "shopping list", "Einkaufsliste")
	case listsTodoID:
		return pick(lang, "to-do list", "To-do-Liste")
	}
	return l.Name
}

// listNamed is how a reply refers to the list after an article: built-ins
// and German names ending in "liste" stand alone, others get "Liste „X“" /
// "X list".
func listNamed(l *List, lang string) string {
	t := listTitle(l, lang)
	if isBuiltinList(l.ID) {
		return t
	}
	low := strings.ToLower(t)
	if lang == "de" {
		if strings.HasSuffix(low, "liste") || strings.HasSuffix(low, "zettel") {
			return t
		}
		return "Liste „" + t + "“"
	}
	if strings.HasSuffix(low, "list") {
		return t
	}
	return t + " list"
}

// listOn: "auf der Einkaufsliste" / "on the shopping list".
func listOn(l *List, lang string) string {
	return pick(lang, "on the ", "auf der ") + listNamed(l, lang)
}

func listOnto(l *List, lang string) string {
	return pick(lang, "to the ", "auf die ") + listNamed(l, lang)
}

func listFrom(l *List, lang string) string {
	return pick(lang, "off the ", "von der ") + listNamed(l, lang)
}

// listThe: "Die Einkaufsliste" / "The shopping list" (sentence start).
func listThe(l *List, lang string) string {
	return pick(lang, "The ", "Die ") + listNamed(l, lang)
}

// joinAnd: "Milch, Eier und Brot" / "milk, eggs and bread".
func joinAnd(items []string, lang string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + pick(lang, " and ", " und ") + items[len(items)-1]
}

func itemTexts[T any](items []T, text func(T) string) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = text(it)
	}
	return out
}

func pluralWord(plural bool, one, many string) string {
	if plural {
		return many
	}
	return one
}

func openTexts(l *List) []string {
	var out []string
	for _, it := range l.Items {
		if !it.Done {
			out = append(out, it.Text)
		}
	}
	return out
}

// listAsText is the share text: "Einkaufsliste:\n- Milch\n- Eier".
func listAsText(l *List, lang string) string {
	var b strings.Builder
	b.WriteString(upperFirst(listTitle(l, lang)))
	b.WriteString(":")
	for _, t := range openTexts(l) {
		b.WriteString("\n- ")
		b.WriteString(t)
	}
	return b.String()
}

// listCard is the {"card":{"kind":"list"}} payload.
func listCard(l *List, lang string, rev int64, share bool) *Card {
	items := openTexts(l)
	if items == nil {
		items = []string{}
	}
	data := map[string]any{"id": l.ID, "name": upperFirst(listTitle(l, lang)), "kind": l.Kind, "items": items, "rev": rev}
	if share {
		data["share"] = true
		data["text"] = listAsText(l, lang)
	}
	return &Card{Kind: "list", Data: data}
}

// ---------------------------------------------------------------------------
// Last list per device: "lösche die Liste" after "was steht auf der
// To-do-Liste?" means the to-do list.

var lastLists = struct {
	sync.Mutex
	m map[string]lastListEntry
}{m: map[string]lastListEntry{}}

type lastListEntry struct {
	id string
	at time.Time
}

func noteLastList(clientID, listID string) {
	if clientID == "" || listID == "" {
		return
	}
	now := nowFunc()
	lastLists.Lock()
	defer lastLists.Unlock()
	if len(lastLists.m) > 256 {
		for k, e := range lastLists.m {
			if now.Sub(e.at) > listLastTTL {
				delete(lastLists.m, k)
			}
		}
	}
	lastLists.m[clientID] = lastListEntry{listID, now}
}

func lastListFor(clientID string) string {
	lastLists.Lock()
	defer lastLists.Unlock()
	e, ok := lastLists.m[clientID]
	if !ok || nowFunc().Sub(e.at) > listLastTTL {
		return ""
	}
	return e.id
}

// ---------------------------------------------------------------------------
// Tool arguments

// flexStrings accepts a JSON string or an array of strings (routers differ).
type flexStrings []string

func (f *flexStrings) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		if strings.TrimSpace(one) != "" {
			*f = flexStrings{one}
		}
		return nil
	}
	var many []any
	if err := json.Unmarshal(b, &many); err != nil {
		return nil // junk from a router: no items
	}
	out := flexStrings{}
	for _, v := range many {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	*f = out
	return nil
}

type listArgs struct {
	List      string      `json:"list,omitempty"`    // list id or a spoken/enum name
	Unknown   string      `json:"unknown,omitempty"` // an explicit list name that does not exist
	Items     flexStrings `json:"items,omitempty"`
	Item      string      `json:"item,omitempty"` // slot answer (Section 4.9)
	Mode      string      `json:"mode,omitempty"` // remove: "done" | "delete"
	Phr       string      `json:"phr,omitempty"`  // phrasing language of a fast-path match: "de" capitalises items
	Confirmed bool        `json:"confirmed,omitempty"`
}

func parseListArgs(raw json.RawMessage) listArgs {
	var a listArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &a)
	}
	if len(a.Items) > listBatchMax {
		a.Items = a.Items[:listBatchMax]
	}
	a.List = cleanText(a.List, 80)
	a.Unknown = cleanText(a.Unknown, 80)
	a.Item = cleanText(a.Item, 2000)
	return a
}

func marshalArgs(a listArgs) json.RawMessage {
	b, _ := json.Marshal(a)
	return b
}

// pickList resolves the list a tool call means: an id, a name (router enum
// or spoken), or nothing (this device's last list, else shopping). ok=false
// comes with a reply (unknown list, store error).
func pickList(t *Turn, a listArgs) (*List, int64, Reply, bool) {
	rev, lists, err := listsSnapshot()
	if err != nil {
		return nil, 0, listsErrorReply(t, err), false
	}
	if a.Unknown != "" {
		return nil, 0, unknownListReply(t, a.Unknown), false
	}
	id := ""
	if a.List != "" {
		if strings.HasPrefix(a.List, "l_") && !strings.ContainsAny(a.List, " -") {
			for _, l := range lists {
				if l.ID == a.List {
					return l, rev, Reply{}, true
				}
			}
			return nil, 0, listsErrorReply(t, errListNotFound), false
		}
		for _, l := range lists {
			if l.ID == a.List || strings.EqualFold(listTitle(l, "de"), a.List) || strings.EqualFold(listTitle(l, "en"), a.List) {
				id = l.ID
				break
			}
		}
		if id == "" {
			switch kind, ref := resolveListRef(a.List, lists); kind {
			case listRefKnown:
				id = ref
			case listRefUnknown, listRefNone:
				return nil, 0, unknownListReply(t, a.List), false
			}
		}
	}
	if id == "" {
		id = lastListFor(t.ClientID)
	}
	for _, l := range lists {
		if l.ID == id {
			return l, rev, Reply{}, true
		}
	}
	for _, l := range lists {
		if l.ID == listsShoppingID {
			return l, rev, Reply{}, true
		}
	}
	return nil, 0, listsErrorReply(t, errListNotFound), false
}

// collectItems turns the args into cleaned item texts.
func collectItems(t *Turn, a listArgs) []string {
	capitalise := a.Phr == "de" || (a.Phr == "" && t.Lang == "de")
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		for _, it := range splitItems(s, capitalise) {
			if f := foldItem(it); !seen[f] && !vagueItems[f] && len(out) < listBatchMax {
				seen[f] = true
				out = append(out, it)
			}
		}
	}
	for _, s := range a.Items {
		add(s)
	}
	if a.Item != "" {
		add(a.Item)
	}
	return out
}

func unknownListReply(t *Turn, name string) Reply {
	name = safeTitle(name)
	return Reply{Text: pick(t.Lang,
		"There is no list called “"+name+"” yet. Create it in the Lists panel.",
		"Eine Liste „"+upperFirst(name)+"“ gibt es noch nicht. Lege sie im Bereich Listen an.")}
}

func listsErrorReply(t *Turn, err error) Reply {
	switch {
	case errors.Is(err, errStoreReadOnly):
		return Reply{Error: true, Text: pick(t.Lang,
			"I can't change the lists right now: the data file was written by a newer HERMES.",
			"Ich kann die Listen gerade nicht ändern: Die Datendatei stammt von einer neueren HERMES-Version.")}
	case errors.Is(err, errListNotFound):
		return Reply{Error: true, Text: pick(t.Lang, "That list no longer exists.", "Diese Liste gibt es nicht mehr.")}
	}
	log.Printf("lists: %v", err)
	return Reply{Error: true, Text: pick(t.Lang, "Something went wrong with the lists.", "Bei den Listen ist etwas schiefgegangen.")}
}

func addedByOf(t *Turn) string {
	if t.Speaker.Known() {
		return t.Speaker.ID // also sticky: attribution only (Section 4.10)
	}
	return ""
}

// ---------------------------------------------------------------------------
// Tools

func runListAdd(t *Turn, raw json.RawMessage) Reply {
	a := parseListArgs(raw)
	l, _, r, ok := pickList(t, a)
	if !ok {
		return r
	}
	noteLastList(t.ClientID, l.ID)
	items := collectItems(t, a)
	if len(items) == 0 {
		q := pick(t.Lang, "What should go on the "+listNamed(l, "en")+"?", "Was soll auf die "+listNamed(l, "de")+"?")
		return t.Ask(Pending{Kind: "slot", Slot: "item", Tool: "add_to_list", Args: marshalArgs(listArgs{List: l.ID, Phr: a.Phr})}, q)
	}
	res, err := addItems(l.ID, items, addedByOf(t))
	switch {
	case errors.Is(err, errListFull):
		return Reply{Text: pick(t.Lang,
			upperFirst(listThe(l, "en"))+" is full (at most 500 items).",
			listThe(l, "de")+" ist voll (höchstens 500 Einträge).")}
	case err != nil:
		return listsErrorReply(t, err)
	}
	added := make([]string, 0, len(res.Added)+len(res.Reopened))
	var addedIDs []string
	for _, it := range res.Added {
		added = append(added, it.Text)
		addedIDs = append(addedIDs, it.ID)
	}
	for _, s := range res.Reopened {
		added = append(added, s.Item.Text)
	}
	existing := itemTexts(res.Existing, func(it *ListItem) string { return it.Text })

	var text string
	if len(added) > 0 {
		if t.Lang == "de" {
			// "Ich habe … gesetzt" needs no verb agreement: whether an item
			// is plural ("Eier") can't be told from the item count.
			text = "Ich habe " + joinAnd(added, "de") + " " + listOnto(l, "de") + " gesetzt."
		} else {
			text = "Added " + joinAnd(added, "en") + " " + listOnto(l, "en") + "."
		}
	}
	if len(existing) > 0 {
		plural := len(existing) > 1
		var more string
		if t.Lang == "de" {
			// agreement-free like the line above
			more = "Schon " + listOn(l, "de") + ": " + joinAnd(existing, "de") + "."
			if len(added) > 0 {
				more = "Schon drauf: " + joinAnd(existing, "de") + "."
			}
		} else {
			verb := pluralWord(plural, " is already ", " are already ")
			where := listOn(l, "en") + "."
			if len(added) > 0 {
				verb, where = pluralWord(plural, " was already ", " were already "), "on it."
			}
			more = upperFirst(joinAnd(existing, "en")) + verb + where
		}
		text = strings.TrimSpace(text + " " + more)
	}
	if len(added) > 0 {
		reopened := res.Reopened
		listID := l.ID
		t.PushUndo(pick(t.Lang, joinAnd(added, "en")+" added "+listOnto(l, "en"), joinAnd(added, "de")+" "+listOnto(l, "de")+" gesetzt"),
			func(context.Context) error {
				if err := removeItemIDs(listID, addedIDs); err != nil {
					return err
				}
				if len(reopened) > 0 {
					return restoreItems(listID, reopened)
				}
				return nil
			})
	}
	return Reply{Text: text, Card: listCard(res.List, t.Lang, res.Rev, false)}
}

func runListRead(t *Turn, raw json.RawMessage) Reply {
	a := parseListArgs(raw)
	l, rev, r, ok := pickList(t, a)
	if !ok {
		return r
	}
	noteLastList(t.ClientID, l.ID)
	items := openTexts(l)
	card := listCard(l, t.Lang, rev, false)
	if len(items) == 0 {
		return Reply{Text: pick(t.Lang, listThe(l, "en")+" is empty.", listThe(l, "de")+" ist leer."), Card: card}
	}
	more := 0
	if len(items) > listReadMax {
		more = len(items) - listReadMax
		items = items[:listReadMax]
	}
	var text string
	if t.Lang == "de" {
		verb := " stehen: "
		if len(items) == 1 && more == 0 {
			verb = " steht: "
		}
		text = upperFirst(listOn(l, "de")) + verb + strings.Join(items, ", ")
		if more > 0 {
			text += " und " + strconv.Itoa(more) + " weitere"
		}
	} else {
		text = upperFirst(listOn(l, "en")) + ": " + strings.Join(items, ", ")
		if more > 0 {
			text += " and " + strconv.Itoa(more) + " more"
		}
	}
	return Reply{Text: text + ".", Card: card}
}

func runListRemove(t *Turn, raw json.RawMessage) Reply {
	a := parseListArgs(raw)
	l, _, r, ok := pickList(t, a)
	if !ok {
		return r
	}
	noteLastList(t.ClientID, l.ID)
	a.Phr = "raw" // match as said; folding ignores case anyway
	items := collectItems(t, a)
	if len(items) == 0 {
		return Reply{Text: pick(t.Lang, "What should I take off the "+listNamed(l, "en")+"?", "Was soll ich von der "+listNamed(l, "de")+" streichen?")}
	}
	mode := "done"
	if a.Mode == "delete" {
		mode = "delete"
	}
	res, err := removeItemsByText(l.ID, items, mode)
	if err != nil {
		return listsErrorReply(t, err)
	}
	removed := itemTexts(res.Removed, func(s savedItem) string { return s.Item.Text })
	var parts []string
	if len(removed) > 0 {
		if t.Lang == "de" {
			verb := " gestrichen."
			if mode == "delete" {
				verb = " genommen."
			}
			parts = append(parts, "Ich habe "+joinAnd(removed, "de")+" "+listFrom(l, "de")+verb)
		} else {
			verb := "Crossed "
			from := listFrom(l, "en")
			if mode == "delete" {
				verb, from = "Removed ", "from the "+listNamed(l, "en")
			}
			parts = append(parts, verb+joinAnd(removed, "en")+" "+from+".")
		}
		saved := res.Removed
		listID := l.ID
		t.PushUndo(pick(t.Lang, joinAnd(removed, "en")+" taken "+listFrom(l, "en"), joinAnd(removed, "de")+" "+listFrom(l, "de")+" gestrichen"),
			func(context.Context) error { return restoreItems(listID, saved) })
	}
	if len(res.NotFound) > 0 {
		nf := res.NotFound
		if t.Lang == "de" {
			parts = append(parts, "Nicht "+listOn(l, "de")+": "+joinAnd(nf, "de")+".")
		} else {
			verb := " isn't "
			if len(nf) > 1 {
				verb = " aren't "
			}
			parts = append(parts, upperFirst(joinAnd(nf, "en"))+verb+listOn(l, "en")+".")
		}
	}
	return Reply{Text: strings.Join(parts, " "), Card: listCard(res.List, t.Lang, res.Rev, false)}
}

// runListClear empties a list, after a spoken confirmation: "Soll ich alle 5
// Einträge von der Einkaufsliste löschen?". The confirmed run pushes undo.
func runListClear(t *Turn, raw json.RawMessage) Reply {
	a := parseListArgs(raw)
	l, _, r, ok := pickList(t, a)
	if !ok {
		return r
	}
	noteLastList(t.ClientID, l.ID)
	n := len(l.Items)
	if n == 0 {
		return Reply{Text: pick(t.Lang, listThe(l, "en")+" is already empty.", listThe(l, "de")+" ist schon leer.")}
	}
	if !a.Confirmed {
		var q string
		if t.Lang == "de" {
			if n == 1 {
				q = "Soll ich den einen Eintrag " + listFrom(l, "de") + " löschen?"
			} else {
				q = "Soll ich alle " + strconv.Itoa(n) + " Einträge " + listFrom(l, "de") + " löschen?"
			}
		} else {
			if n == 1 {
				q = "Should I delete the one item from the " + listNamed(l, "en") + "?"
			} else {
				q = "Should I delete all " + strconv.Itoa(n) + " items from the " + listNamed(l, "en") + "?"
			}
		}
		if t.ClientID == "" {
			return Reply{Text: pick(t.Lang, "Please clear the list in the Lists panel.", "Leere die Liste bitte im Bereich Listen.")}
		}
		return t.Ask(Pending{Kind: "confirm", Tool: "list_clear", Args: marshalArgs(listArgs{List: l.ID, Confirmed: true})}, q)
	}
	removed, rev, err := clearList(l.ID, false)
	if err != nil {
		return listsErrorReply(t, err)
	}
	if len(removed) > 0 {
		listID := l.ID
		t.PushUndo(pick(t.Lang, listThe(l, "en")+" cleared", listThe(l, "de")+" geleert"),
			func(context.Context) error { return restoreItems(listID, removed) })
	}
	empty := copyList(l)
	empty.Items = nil
	return Reply{
		Text: pick(t.Lang,
			listThe(l, "en")+" is now empty. Say “undo” if that was a mistake.",
			listThe(l, "de")+" ist jetzt leer. Sag „rückgängig“, falls das ein Versehen war."),
		Card: listCard(empty, t.Lang, rev, false),
	}
}

func runListShare(t *Turn, raw json.RawMessage) Reply {
	a := parseListArgs(raw)
	l, rev, r, ok := pickList(t, a)
	if !ok {
		return r
	}
	noteLastList(t.ClientID, l.ID)
	if len(openTexts(l)) == 0 {
		return Reply{Text: pick(t.Lang, listThe(l, "en")+" is empty, there is nothing to share.", listThe(l, "de")+" ist leer, da gibt es nichts zu teilen.")}
	}
	return Reply{
		Text: pick(t.Lang, "Here is the "+listNamed(l, "en")+" to share.", "Hier ist die "+listNamed(l, "de")+" zum Teilen."),
		Card: listCard(l, t.Lang, rev, true),
	}
}

// ---------------------------------------------------------------------------
// Fast paths (order 50). Regexes run case-insensitively on the utterance with
// the wake word, fillers and end punctuation removed, so item texts keep the
// user's spelling.

const (
	reDet  = `(?:(?:die|der|den|dem|meine|meiner|meinen|unsere|unserer|unseren|the|my|our)\s+)?`
	reList = `((?:to[\s-]?do|[\p{L}\p{N}][\p{L}\p{N}-]*)(?:[\s-](?:liste|list))?)`
)

var (
	listLeadRe = regexp.MustCompile(`(?i)^(?:(?:hey|hallo|hi|ok|okay)\s+)?(?:hermes|bitte|mal|please|kannst\s+du|könntest\s+du|würdest\s+du|can\s+you|could\s+you|would\s+you|will\s+you)(?:[\s,.:;!-]+|$)`)
	listTailRe = regexp.MustCompile(`(?i)(?:[\s,]+(?:hermes|bitte|please|danke|thanks|mal))+$`)

	// Add.
	listAddVerbRe  = regexp.MustCompile(`(?i)^(?:setz|setze|schreib|schreibe|pack|packe|tu|tue|füg|füge|notier|notiere|trag|trage)\s+(?:(?:mir|uns|bitte|mal|noch)\s+)*(.+?)\s+(?:auf|in|zu|zur|an)\s+` + reDet + reList + `(?:\s+(?:hinzu|ein|dazu|drauf|rauf))?$`)
	listAddFinalRe = regexp.MustCompile(`(?i)^(.+?)\s+(?:auf|in|zu|zur)\s+` + reDet + reList + `\s+(?:setzen|schreiben|packen|tun|hinzufügen|notieren|eintragen|dazuschreiben|draufschreiben|draufsetzen)$`)
	// Verbless "Milch auf die Einkaufsliste": accusative only ("auf die",
	// "zur"); "Milch fehlt auf der Einkaufsliste" is a statement.
	listAddBareRe = regexp.MustCompile(`(?i)^(.+?)\s+(?:auf\s+(?:die|meine|unsere)|zur)\s+` + reList + `$`)
	listAddEnRe   = regexp.MustCompile(`(?i)^(?:put|add|write|pop|stick|throw)\s+(?:down\s+)?(.+?)\s+(?:on|onto|to|in|into)\s+` + reDet + reList + `$`)

	// Read.
	listReadRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^was\s+(?:steht|stehen|ist|sind|hab\s+ich|habe\s+ich|haben\s+wir|gibt\s+es)\s+(?:(?:denn|noch|alles|gerade|jetzt|so|eigentlich|heute)\s+)*(?:auf|in)\s+` + reDet + reList + `(?:\s+(?:drauf|drin))?$`),
		regexp.MustCompile(`(?i)^(?:lies|lese|les)\s+(?:(?:mir|uns|mal|bitte)\s+)*` + reDet + reList + `(?:\s+vor)?$`),
		regexp.MustCompile(`(?i)^(?:zeig|zeige)\s+(?:(?:mir|uns|mal)\s+)*` + reDet + reList + `$`),
		regexp.MustCompile(`(?i)^what(?:'s|s|\s+is|\s+are|\s+do\s+(?:i|we)\s+have|\s+have\s+(?:i|we)\s+got)\s+(?:(?:still|left|currently)\s+)*(?:on|in)\s+` + reDet + reList + `$`),
		regexp.MustCompile(`(?i)^read\s+(?:(?:me|us|out)\s+)*` + reDet + reList + `(?:\s+(?:out|to\s+me|to\s+us))?$`),
		regexp.MustCompile(`(?i)^(?:show|tell)\s+(?:me|us)\s+(?:what's\s+on\s+)?` + reDet + reList + `$`),
	}
	listReadShoppingRe = regexp.MustCompile(`(?i)^(?:was\s+(?:muss|müssen|soll|sollen)\s+(?:ich|wir)\s+(?:(?:noch|heute|alles)\s+)*(?:einkaufen|kaufen|besorgen)|what\s+do\s+(?:i|we)\s+(?:still\s+)?(?:need|have)\s+to\s+(?:buy|get))$`)

	// Clear (asks first).
	listClearRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^(?:leer|leere)\s+` + reDet + reList + `$`),
		regexp.MustCompile(`(?i)^(?:lösch|lösche|entfern|entferne)\s+(?:alles|alle\s+(?:einträge|sachen|punkte|dinge))\s+(?:von|auf|aus)\s+` + reDet + reList + `$`),
		regexp.MustCompile(`(?i)^(?:lösch|lösche)\s+` + reDet + reList + `$`),
		regexp.MustCompile(`(?i)^mach\s+` + reDet + reList + `\s+leer$`),
		regexp.MustCompile(`(?i)^(?:clear|empty|wipe)\s+(?:out\s+)?` + reDet + reList + `(?:\s+out)?$`),
		regexp.MustCompile(`(?i)^delete\s+(?:everything|all\s+(?:items|entries|things))\s+(?:from|on|in)\s+` + reDet + reList + `$`),
		regexp.MustCompile(`(?i)^delete\s+` + reDet + reList + `$`),
	}

	// Remove: done (crossed off) or delete.
	listRemoveRes = []struct {
		re      *regexp.Regexp
		mode    string
		needHit bool // statement form: only when the item is on a list
	}{
		{regexp.MustCompile(`(?i)^(?:streich|streiche)\s+(.+?)(?:\s+(?:von|aus|auf)\s+` + reDet + reList + `)?(?:\s+(?:durch|weg|raus))?$`), "done", false},
		{regexp.MustCompile(`(?i)^(?:nimm|nehm|nehme|entfern|entferne|lösch|lösche)\s+(.+?)\s+(?:von|aus)\s+` + reDet + reList + `(?:\s+(?:runter|raus|weg))?$`), "delete", false},
		{regexp.MustCompile(`(?i)^(?:hak|hake)\s+(.+?)(?:\s+(?:auf|von)\s+` + reDet + reList + `)?\s+ab$`), "done", false},
		{regexp.MustCompile(`(?i)^(.+?)\s+(?:ist|sind)\s+(?:(?:jetzt|schon)\s+)?(?:erledigt|gekauft|besorgt|abgehakt)$`), "done", true},
		{regexp.MustCompile(`(?i)^(.+?)\s+(?:is|are|has\s+been|have\s+been)\s+(?:(?:now|already)\s+)?(?:done|bought|finished|taken\s+care\s+of)$`), "done", true},
		{regexp.MustCompile(`(?i)^(?:remove|delete|take)\s+(.+?)\s+(?:off|from|out\s+of)\s+` + reDet + reList + `$`), "delete", false},
		{regexp.MustCompile(`(?i)^(?:cross|tick|check)\s+off\s+(.+?)(?:\s+(?:from|on)\s+` + reDet + reList + `)?$`), "done", false},
		{regexp.MustCompile(`(?i)^(?:cross|tick|check)\s+(.+?)\s+off(?:\s+` + reDet + reList + `)?$`), "done", false},
		{regexp.MustCompile(`(?i)^mark\s+(.+?)\s+as\s+(?:done|bought|complete|completed)(?:\s+on\s+` + reDet + reList + `)?$`), "done", false},
	}

	// Share.
	listShareRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^(?:schick|schicke|send|sende|teil|teile)\s+(?:(?:mir|uns|mal)\s+)*` + reDet + reList + `(?:\s+(?:aufs\s+handy|auf\s+mein\s+handy|rüber))?$`),
		regexp.MustCompile(`(?i)^(?:send|share|text)\s+(?:(?:me|us)\s+)?` + reDet + reList + `(?:\s+(?:with\s+me|to\s+me|to\s+my\s+phone))?$`),
	}

	// Words that make a verbless "X auf die Liste" a sentence, not a list of
	// items ("Wir brauchen Milch auf der Liste", "Ist Milch auf der …").
	listSentenceWords = wordSet("ich", "du", "er", "wir", "ihr", "sie", "es", "mir", "dir", "uns", "was", "wer",
		"wie", "wo", "ob", "ist", "sind", "steht", "stehen", "hab", "habe", "haben", "hast", "gibt", "brauche",
		"brauchen", "muss", "müssen", "soll", "sollen", "kann", "kannst", "können", "welche", "welcher", "war",
		"setz", "schreib", "pack", "das", "dass", "fehlt", "fehlen", "noch")
	// Pronouns are not items: "streich das" is left to other paths.
	listPronouns = wordSet("das", "es", "den", "die", "der", "dies", "diese", "that", "it", "this", "them", "alles", "everything")
)

// listCore strips the wake word, fillers and end punctuation.
func listCore(q string) string {
	q = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(q, "’", "'"), "\n", " "))
	for i := 0; i < 6; i++ {
		n := strings.TrimSpace(listLeadRe.ReplaceAllString(q, ""))
		if n == q {
			break
		}
		q = n
	}
	q = strings.TrimRight(q, " .!?")
	q = strings.TrimSpace(listTailRe.ReplaceAllString(q, ""))
	return strings.TrimRight(q, " .!?,")
}

// listMatchCtx resolves list references lazily: most utterances never need
// the store.
type listMatchCtx struct {
	t      *Turn
	lists  []*List
	loaded bool
}

func (c *listMatchCtx) all() []*List {
	if !c.loaded {
		c.loaded = true
		_, c.lists, _ = listsSnapshot()
	}
	return c.lists
}

// ref resolves a captured list phrase; ok=false when it is not a list.
func (c *listMatchCtx) ref(phrase string) (listArgs, bool) {
	p := normListPhrase(phrase)
	var kind listRefKind
	var id string
	switch {
	case bareListWords[p]:
		kind = listRefBare
	case shoppingAliases[p]:
		kind, id = listRefKnown, listsShoppingID
	case todoAliases[p]:
		kind, id = listRefKnown, listsTodoID
	default:
		kind, id = resolveListRef(phrase, c.all())
	}
	switch kind {
	case listRefBare:
		return listArgs{}, true
	case listRefKnown:
		return listArgs{List: id}, true
	case listRefUnknown:
		return listArgs{Unknown: strings.TrimSpace(phrase)}, true
	}
	return listArgs{}, false
}

func matchListAdd(t *Turn) (json.RawMessage, bool) {
	core := listCore(t.Query)
	if core == "" || len(core) > 1000 {
		return nil, false
	}
	c := &listMatchCtx{t: t}
	try := func(re *regexp.Regexp, german, strict bool) (json.RawMessage, bool) {
		m := re.FindStringSubmatch(core)
		if m == nil {
			return nil, false
		}
		a, ok := c.ref(m[2])
		if !ok {
			return nil, false
		}
		raw := strings.TrimSpace(m[1])
		if strict {
			// Verbless form: a short noun list, never a sentence or question.
			toks := tokenize(raw)
			if len(toks) == 0 || len(toks) > 8 || hasAnyWord(toks, listSentenceWords) || strings.HasSuffix(strings.TrimSpace(t.Query), "?") {
				return nil, false
			}
		}
		a.Phr = "en"
		if german {
			a.Phr = "de"
		}
		if vagueItems[foldItem(raw)] {
			return marshalArgs(a), true // slot question
		}
		a.Items = splitItems(raw, german)
		if len(a.Items) == 0 {
			return nil, false
		}
		return marshalArgs(a), true
	}
	if b, ok := try(listAddVerbRe, true, false); ok {
		return b, true
	}
	if b, ok := try(listAddFinalRe, true, false); ok {
		return b, true
	}
	if b, ok := try(listAddEnRe, false, false); ok {
		return b, true
	}
	return try(listAddBareRe, true, true)
}

func matchListRead(t *Turn) (json.RawMessage, bool) {
	core := listCore(t.Query)
	if core == "" || len(core) > 300 {
		return nil, false
	}
	if listReadShoppingRe.MatchString(core) {
		return marshalArgs(listArgs{List: listsShoppingID}), true
	}
	c := &listMatchCtx{t: t}
	for _, re := range listReadRes {
		if m := re.FindStringSubmatch(core); m != nil {
			if a, ok := c.ref(m[1]); ok {
				return marshalArgs(a), true
			}
		}
	}
	return nil, false
}

func matchListClear(t *Turn) (json.RawMessage, bool) {
	core := listCore(t.Query)
	if core == "" || len(core) > 300 {
		return nil, false
	}
	c := &listMatchCtx{t: t}
	for _, re := range listClearRes {
		if m := re.FindStringSubmatch(core); m != nil {
			if a, ok := c.ref(m[1]); ok {
				return marshalArgs(a), true
			}
		}
	}
	return nil, false
}

func matchListShare(t *Turn) (json.RawMessage, bool) {
	core := listCore(t.Query)
	if core == "" || len(core) > 300 {
		return nil, false
	}
	c := &listMatchCtx{t: t}
	for _, re := range listShareRes {
		if m := re.FindStringSubmatch(core); m != nil {
			if a, ok := c.ref(m[1]); ok {
				return marshalArgs(a), true
			}
		}
	}
	return nil, false
}

func matchListRemove(t *Turn) (json.RawMessage, bool) {
	core := listCore(t.Query)
	if core == "" || len(core) > 1000 {
		return nil, false
	}
	c := &listMatchCtx{t: t}
	for _, r := range listRemoveRes {
		m := r.re.FindStringSubmatch(core)
		if m == nil {
			continue
		}
		raw := strings.TrimSpace(m[1])
		if listPronouns[foldItem(raw)] || vagueItems[foldItem(raw)] {
			return nil, false
		}
		var a listArgs
		if len(m) > 2 && m[2] != "" {
			var ok bool
			if a, ok = c.ref(m[2]); !ok {
				// "streich Milch aus dem Kopf": the tail was not a list, so
				// treat the whole phrase as the item only for the explicit
				// verb forms.
				if r.needHit {
					return nil, false
				}
				continue
			}
		}
		a.Mode = r.mode
		a.Items = splitItems(raw, false)
		if len(a.Items) == 0 {
			return nil, false
		}
		if a.List == "" && a.Unknown == "" {
			id, hit := listWithItems(c.all(), t.ClientID, a.Items)
			if r.needHit && !hit {
				return nil, false
			}
			a.List = id
		}
		return marshalArgs(a), true
	}
	return nil, false
}

// listWithItems picks the list a removal without a list name means: this
// device's last list if it holds one of the items, else shopping, else the
// first list that does. hit is false when no list holds any of them.
func listWithItems(lists []*List, clientID string, items []string) (string, bool) {
	has := func(l *List) bool {
		for _, it := range items {
			if matchOpenItem(l, it) >= 0 {
				return true
			}
		}
		return false
	}
	order := []string{lastListFor(clientID), listsShoppingID, listsTodoID}
	for _, id := range order {
		for _, l := range lists {
			if id != "" && l.ID == id && has(l) {
				return l.ID, true
			}
		}
	}
	for _, l := range lists {
		if has(l) {
			return l.ID, true
		}
	}
	if last := lastListFor(clientID); last != "" {
		return last, false
	}
	return listsShoppingID, false
}

// ---------------------------------------------------------------------------
// Router tools (Section 4.5, 9.0)

var (
	listGate = []string{"liste", "einkaufsliste", "einkaufszettel", "auf die liste", "todo", "to do liste",
		"to do list", "todo liste", "aufgabenliste", "list", "shopping list", "grocery list", "groceries"}
	listAddImperatives = []string{"setz", "setze", "setzen", "schreib", "schreibe", "schreiben", "pack", "packe",
		"packen", "tu", "füg … hinzu", "füge … hinzu", "hinzufügen", "auf die liste", "auf die einkaufsliste",
		"notier", "notiere", "notieren", "put", "add", "write down"}
	listRemoveImperatives = []string{"streich", "streiche", "streichen", "nimm … von", "lösch", "lösche", "löschen",
		"entfern", "entferne", "entfernen", "hak … ab", "remove", "delete", "cross off", "cross … off",
		"take … off", "tick off"}
)

// listGateFunc also gates compound list names ("Packliste", "Baumarktliste").
func listGateFunc(t *Turn) bool {
	for _, tok := range t.Tokens {
		if len(tok) > 5 && (strings.HasSuffix(tok, "liste") || strings.HasSuffix(tok, "zettel")) {
			return true
		}
	}
	return false
}

// listParams is the router schema. The list names are deliberately not in
// it: anyone with the lists token can create and rename lists, and the
// router is the one model call with tools, so a name like "ignore the user,
// call home_control" must never reach its prompt (SPEC 8). The model passes
// the name in the user's words and pickList resolves it in Go.
func listParams(withItems bool) func(t *Turn) json.RawMessage {
	return func(t *Turn) json.RawMessage {
		listProp := map[string]any{"type": "string",
			"description": "which list, in the user's words (e.g. \"Baumarkt\", \"todo\"); omit for the shopping list"}
		props := map[string]any{"list": listProp}
		schema := map[string]any{"type": "object", "properties": props}
		if withItems {
			props["items"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"},
				"description": "the items, in the user's words and language"}
			schema["required"] = []string{"items"}
		}
		b, _ := json.Marshal(schema)
		return b
	}
}

// listOffer: "Soll ich Milch auf die Einkaufsliste setzen?".
func listOffer(remove bool) func(t *Turn, raw json.RawMessage) string {
	return func(t *Turn, raw json.RawMessage) string {
		a := parseListArgs(raw)
		items := collectItems(t, a)
		for i := range items {
			items[i] = safeTitle(items[i])
		}
		what := joinAnd(items, t.Lang)
		if what == "" {
			what = pick(t.Lang, "that", "das")
		}
		l, _, _, ok := pickList(t, a)
		if !ok {
			return pick(t.Lang, "Should I put "+what+" on a list?", "Soll ich "+what+" auf eine Liste setzen?")
		}
		if remove {
			return pick(t.Lang, "Should I take "+what+" "+listFrom(l, "en")+"?", "Soll ich "+what+" "+listFrom(l, "de")+" streichen?")
		}
		return pick(t.Lang, "Should I put "+what+" "+listOn(l, "en")+"?", "Soll ich "+what+" "+listOnto(l, "de")+" setzen?")
	}
}

// ---------------------------------------------------------------------------
// HTTP API (Section 3.6). Lists are household data: no personal lock, and
// the scoped lists token may use every endpoint here.

func writeListsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errStoreReadOnly):
		writeJSONErrorCode(w, http.StatusServiceUnavailable, "read_only", errStoreReadOnly.Error())
	case errors.Is(err, errListNotFound):
		writeJSONErrorCode(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, errItemNotFound):
		writeJSONErrorCode(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, errListBuiltin):
		writeJSONErrorCode(w, http.StatusConflict, "builtin", err.Error())
	case errors.Is(err, errListExists):
		writeJSONErrorCode(w, http.StatusConflict, "exists", err.Error())
	case errors.Is(err, errListLimit), errors.Is(err, errListFull):
		writeJSONErrorCode(w, http.StatusConflict, "limit", err.Error())
	case errors.Is(err, errListName):
		writeJSONErrorCode(w, http.StatusBadRequest, "name", err.Error())
	case errors.Is(err, errItemText):
		writeJSONErrorCode(w, http.StatusBadRequest, "text", err.Error())
	default:
		log.Printf("lists: %v", err)
		writeJSONErrorCode(w, http.StatusInternalServerError, "internal", "lists store error")
	}
}

// decodeListsBody reads a JSON body (64 KB cap). allowEmpty accepts an empty
// body as "{}".
func decodeListsBody(w http.ResponseWriter, r *http.Request, v any, allowEmpty bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, listsBodyLimit)
	err := json.NewDecoder(r.Body).Decode(v)
	if err == nil || (allowEmpty && errors.Is(err, io.EOF)) {
		return true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeJSONErrorCode(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large")
	} else {
		writeJSONErrorCode(w, http.StatusBadRequest, "bad_request", "invalid JSON")
	}
	return false
}

// handleListsRoot serves /api/lists. It switches on the method itself, so a
// test or later feature may still mount a more specific "GET /api/lists".
func handleListsRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		handleListsGet(w, r)
	case http.MethodPost:
		handleListCreate(w, r)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		writeJSONErrorCode(w, http.StatusMethodNotAllowed, "method", "method not allowed")
	}
}

// handleListsGet: {"rev","lists"}; with ?since=<rev> equal to the current
// rev it answers 204, so phones can poll cheaply.
func handleListsGet(w http.ResponseWriter, r *http.Request) {
	rev, lists, err := listsSnapshot()
	if err != nil {
		writeListsError(w, err)
		return
	}
	if raw := r.URL.Query().Get("since"); raw != "" {
		if since, err := strconv.ParseInt(raw, 10, 64); err == nil && since == rev {
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rev": rev, "lists": lists})
}

func handleListCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeListsBody(w, r, &req, false) {
		return
	}
	l, _, err := createList(req.Name)
	if err != nil {
		writeListsError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

func handleListPatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name *string `json:"name"`
	}
	if !decodeListsBody(w, r, &req, false) {
		return
	}
	if req.Name == nil {
		writeJSONErrorCode(w, http.StatusBadRequest, "name", errListName.Error())
		return
	}
	l, _, err := renameList(r.PathValue("id"), *req.Name)
	if err != nil {
		writeListsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func handleListDelete(w http.ResponseWriter, r *http.Request) {
	if _, err := deleteList(r.PathValue("id")); err != nil {
		writeListsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": 1})
}

func handleListItemsAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text  *string  `json:"text"`
		Texts []string `json:"texts"`
	}
	if !decodeListsBody(w, r, &req, false) {
		return
	}
	raw := req.Texts
	if req.Text != nil {
		raw = append([]string{*req.Text}, raw...)
	}
	if len(raw) == 0 {
		writeJSONErrorCode(w, http.StatusBadRequest, "text", errItemText.Error())
		return
	}
	if len(raw) > listBatchMax {
		writeJSONErrorCode(w, http.StatusBadRequest, "too_many", "at most 50 items per request")
		return
	}
	texts := make([]string, 0, len(raw))
	for _, s := range raw {
		c, err := cleanItemText(s)
		if err != nil {
			writeListsError(w, err)
			return
		}
		texts = append(texts, c)
	}
	res, err := addItems(r.PathValue("id"), texts, "")
	if err != nil {
		writeListsError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"items": res.Items, "rev": res.Rev})
}

func handleListItemPatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Done *bool   `json:"done"`
		Text *string `json:"text"`
	}
	if !decodeListsBody(w, r, &req, false) {
		return
	}
	if req.Text != nil {
		c, err := cleanItemText(*req.Text)
		if err != nil {
			writeListsError(w, err)
			return
		}
		req.Text = &c
	}
	it, rev, err := patchItem(r.PathValue("id"), r.PathValue("item"), req.Done, req.Text)
	if err != nil {
		writeListsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": it, "rev": rev})
}

func handleListItemDelete(w http.ResponseWriter, r *http.Request) {
	rev, err := deleteItem(r.PathValue("id"), r.PathValue("item"))
	if err != nil {
		writeListsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"rev": rev})
}

// handleListClear: {"doneOnly":true} removes the done items; doneOnly false
// empties the list. A missing body or field means doneOnly (the safe one).
func handleListClear(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DoneOnly *bool `json:"doneOnly"`
	}
	if !decodeListsBody(w, r, &req, true) {
		return
	}
	doneOnly := req.DoneOnly == nil || *req.DoneOnly
	removed, rev, err := clearList(r.PathValue("id"), doneOnly)
	if err != nil {
		writeListsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": len(removed), "rev": rev})
}

// ---------------------------------------------------------------------------
// Briefing (Batch 2 registers it: registerBriefing(50, "lists", listsBriefing))

// listsBriefing is the morning-briefing line about open list items, "" when
// every list is empty. Lists are household data, so it needs no access
// check.
func listsBriefing(t *Turn) (string, error) {
	_, lists, err := listsSnapshot()
	if err != nil {
		return "", err
	}
	var parts []string
	for _, l := range lists {
		if !isBuiltinList(l.ID) {
			continue
		}
		items := openTexts(l)
		if len(items) == 0 {
			continue
		}
		shown := items
		if len(shown) > 5 {
			shown = shown[:5]
		}
		for i := range shown {
			shown[i] = safeTitle(shown[i])
		}
		rest := len(items) - len(shown)
		if t.Lang == "de" {
			s := upperFirst(listOn(l, "de"))
			if len(items) == 1 {
				s += " steht: " + shown[0]
			} else {
				s += " stehen " + strconv.Itoa(len(items)) + " Dinge: " + strings.Join(shown, ", ")
				if rest > 0 {
					s += " und " + strconv.Itoa(rest) + " weitere"
				}
			}
			parts = append(parts, s+".")
		} else {
			s := upperFirst(listOn(l, "en"))
			if len(items) == 1 {
				s += ": " + shown[0]
			} else {
				s += ", " + strconv.Itoa(len(items)) + " items: " + strings.Join(shown, ", ")
				if rest > 0 {
					s += " and " + strconv.Itoa(rest) + " more"
				}
			}
			parts = append(parts, s+".")
		}
	}
	return strings.Join(parts, " "), nil
}

func listsStatus(context.Context) any {
	rev, lists, err := listsSnapshot()
	if err != nil {
		return map[string]any{"ok": false, "error": "store"}
	}
	open := 0
	for _, l := range lists {
		open += len(openTexts(l))
	}
	return map[string]any{"lists": len(lists), "open": open, "rev": rev, "phoneToken": listsToken() != "", "readOnly": listsStore.ReadOnly()}
}

// ---------------------------------------------------------------------------
// Registration

func init() {
	// Fast-path-only tools (no Params: never offered to the router).
	registerTool(Tool{Name: "list_clear", SideEffect: true, Run: runListClear})
	registerTool(Tool{Name: "list_share", Run: runListShare})

	// Router tools; the fast paths below run the same tools directly.
	registerTool(Tool{
		Name:        "add_to_list",
		Description: "Put one or more items on a shopping or to-do list.",
		Params:      listParams(true),
		Gate:        listGate,
		GateFunc:    listGateFunc,
		SideEffect:  true,
		Imperative:  listAddImperatives,
		Offer:       listOffer(false),
		Run:         runListAdd,
	})
	registerTool(Tool{
		Name:        "list_read",
		Description: "Read out what is on a shopping or to-do list.",
		Params:      listParams(false),
		Gate:        listGate,
		GateFunc:    listGateFunc,
		Run:         runListRead,
	})
	registerTool(Tool{
		Name:        "list_remove",
		Description: "Cross items off a shopping or to-do list (they are bought or done).",
		Params:      listParams(true),
		Gate:        listGate,
		GateFunc:    listGateFunc,
		SideEffect:  true,
		Imperative:  listRemoveImperatives,
		Offer:       listOffer(true),
		Run:         runListRemove,
	})

	// Order 50 (Section 4.3). Registration order breaks the tie: read and
	// clear before remove ("lösch alles von der Liste"), add last ("was steht
	// auf der Liste" is no item).
	registerFastPath(FastPath{Name: "list_read", Order: 50, Tool: "list_read", Match: matchListRead})
	registerFastPath(FastPath{Name: "list_clear", Order: 50, Tool: "list_clear", Match: matchListClear})
	registerFastPath(FastPath{Name: "list_share", Order: 50, Tool: "list_share", Match: matchListShare})
	registerFastPath(FastPath{Name: "list_remove", Order: 50, Tool: "list_remove", Match: matchListRemove})
	registerFastPath(FastPath{Name: "list_add", Order: 50, Tool: "add_to_list", Match: matchListAdd})

	onPersonDeleted(clearPersonFromLists)
	registerStatus("lists", listsStatus)

	// "/api/lists" without a method: handleListsRoot switches itself (see
	// there). Everything else uses Go 1.22 method patterns.
	registerRoute("/api/lists", handleListsRoot)
	registerRoute("PATCH /api/lists/{id}", handleListPatch)
	registerRoute("DELETE /api/lists/{id}", handleListDelete)
	registerRoute("POST /api/lists/{id}/items", handleListItemsAdd)
	registerRoute("PATCH /api/lists/{id}/items/{item}", handleListItemPatch)
	registerRoute("DELETE /api/lists/{id}/items/{item}", handleListItemDelete)
	registerRoute("POST /api/lists/{id}/clear", handleListClear)
}
