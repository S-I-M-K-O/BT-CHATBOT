package hermes

import (
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// docs_manage.go: voice access to the hub's documents beyond questions:
// "Welche Dokumente hast du?" (docs_list), "Lösch das Dokument Mietvertrag"
// (docs_delete, asks first), and helpers that let a question about one named
// document read that document (docs_ask uses docsTarget).

var (
	docsPluralRe = `(?:dokumente|dateien|unterlagen|documents|files|docs)`
	docsListRe   = regexp.MustCompile(`(?i)^(?:` +
		`(?:welche|was\s+für)\s+` + docsPluralRe + `(?:\s+\S+){0,6}` +
		`|(?:liste|list|zeig|zeige|nenn|nenne|show|tell)\s+(?:(?:mir|me|uns|us|mal|bitte|auf)\s+)*(?:alle\s+|all\s+)?(?:(?:meine|unsere|die|my|the|our)\s+)?` + docsPluralRe + `(?:\s+\S+){0,4}` +
		`|(?:what|which)\s+` + docsPluralRe + `(?:\s+\S+){0,6}` +
		`)$`)
	// a list request never asks about content
	docsListDeny = wordSet("über", "uber", "about", "steht", "stehen", "sagt", "sagen", "say", "says", "mention",
		"mentions", "erwähnt", "erwähnen", "erwahnt", "zum", "zur", "regarding", "lösch", "lösche", "losch", "delete", "remove", "entferne")

	docsDeleteVerb = `(?:lösch|lösche|losch|losche|entfern|entferne|delete|remove)`
	docsDeleteRes  = []*regexp.Regexp{
		// "lösch das Dokument Mietvertrag", "delete the file notes"
		regexp.MustCompile(`(?i)^` + docsDeleteVerb + `\s+(?:(?:bitte|mal|please)\s+)*(?:(?:das|die|den|the|my|mein|meine|meinen)\s+)?(?:dokument|datei|document|file|pdf|doc)(?:\s+(.*))?$`),
		// "lösch Mietvertrag aus dem Hub / aus meinen Dokumenten"
		regexp.MustCompile(`(?i)^` + docsDeleteVerb + `\s+(?:(?:bitte|mal|please)\s+)*(.+?)\s+(?:aus|from)\s+(?:dem\s+hub|der\s+zentrale|den\s+dokumenten|meinen\s+dokumenten|unseren\s+dokumenten|the\s+hub|my\s+documents|our\s+documents|documents)$`),
	}
	docsDeleteAllRe = regexp.MustCompile(`(?i)^` + docsDeleteVerb + `\s+(?:(?:bitte|mal|please)\s+)*(?:alle\s+(?:meine\s+|unsere\s+)?(?:dokumente|dateien)|all\s+(?:my\s+|our\s+|the\s+)?(?:documents|files))(?:\s+(?:aus\s+dem\s+hub|im\s+hub|in\s+the\s+hub|from\s+the\s+hub))?$`)
)

// docsQuery is the utterance lower-cased and without end punctuation.
func docsQuery(t *Turn) string {
	return strings.TrimSpace(strings.TrimRight(strings.TrimSpace(strings.ToLower(t.Query)), " .!?"))
}

// docTitle: the name without its extension ("Mietvertrag Wohnung").
func docTitle(name string) string {
	if t := strings.TrimSpace(strings.TrimSuffix(name, path.Ext(name))); t != "" {
		return t
	}
	return name
}

// visibleDocs are the documents t may read, oldest first.
func visibleDocs(t *Turn) []DocMeta {
	allow := docsScope(t)
	var out []DocMeta
	for _, d := range docsList() {
		if allow(d.Owner) && d.Status != docError {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// docsByName ranks docs by how many words of phrase match their name;
// only the best-scoring docs (score > 0) are returned.
func docsByName(docs []DocMeta, phrase string) []DocMeta {
	qTerms := lexTokens(phrase, docsLexExclude)
	best := 0
	var out []DocMeta
	for _, d := range docs {
		score := 0
		for _, nt := range nameTerms(d.Name) {
			for _, qt := range qTerms {
				if termsMatch(nt, qt) {
					score++
					break
				}
			}
		}
		switch {
		case score == 0 || score < best:
		case score > best:
			best, out = score, []DocMeta{d}
		default:
			out = append(out, d)
		}
	}
	return out
}

// docsTarget is the single document a question is about: one whose name the
// question uses, or the only document there is. ok=false otherwise.
// lead=true when the whole document is wanted: the question names it or
// asks only about "the document" ("Was steht in dem Dokument?").
func docsTarget(t *Turn, question string) (d DocMeta, lead, ok bool) {
	docs := visibleDocs(t)
	if len(docs) == 0 {
		return DocMeta{}, false, false
	}
	if m := docsByName(docs, question); len(m) == 1 {
		return m[0], true, true
	}
	if len(docs) == 1 {
		return docs[0], docsGeneric(question), true
	}
	return DocMeta{}, false, false
}

// docsGeneric: the question has no topic of its own, only document words.
func docsGeneric(question string) bool {
	for _, w := range lexTokens(question, docsLexExclude) {
		if !docNounLike(w) && !docsGenericWords[lexFold(w)] {
			return false
		}
	}
	return true
}

var docsGenericWords = wordSet("steht", "stehen", "drin", "drinnen", "inhalt", "fasse", "fass", "zusammen",
	"zusammenfassen", "zusammenfassung", "worum", "geht", "erklare", "erklar", "lies", "vor", "say", "says",
	"summarize", "summarise", "summary", "content", "contents", "explain", "read", "inside", "about")

// docLeadHits: the first k chunks of a document, for "what is in it?".
func docLeadHits(d DocMeta, k int) []docHit {
	docsMem.Lock()
	defer docsMem.Unlock()
	docsMem.ensureLocked()
	var out []docHit
	for _, ch := range docsMem.byDoc[d.ID] {
		if len(out) >= k {
			break
		}
		out = append(out, docHit{Doc: d.ID, Name: d.Name, N: ch.n, Text: ch.text})
	}
	return out
}

func matchDocsList(t *Turn) (json.RawMessage, bool) {
	q := docsQuery(t)
	if q == "" || len(q) > 200 || !docsListRe.MatchString(q) {
		return nil, false
	}
	for _, w := range t.Tokens {
		if docsListDeny[w] {
			return nil, false
		}
	}
	return json.RawMessage(`{}`), true
}

func runDocsList(t *Turn, _ json.RawMessage) Reply {
	docs := visibleDocs(t)
	if len(docs) == 0 {
		return Reply{Text: pick(t.Lang,
			"There are no documents in the hub yet. You can upload them under Documents or at /documents.",
			"In der Zentrale liegen noch keine Dokumente. Du kannst sie unter Dokumente oder auf /documents hochladen.")}
	}
	names := make([]string, len(docs))
	for i, d := range docs {
		names[i] = docTitle(d.Name)
	}
	if len(docs) == 1 {
		return Reply{Text: pick(t.Lang, "There is one document: ", "Es gibt ein Dokument: ") + names[0] + "."}
	}
	n := strconv.Itoa(len(docs))
	return Reply{Text: pick(t.Lang, "There are "+n+" documents: ", "Es gibt "+n+" Dokumente: ") + joinAnd(names, t.Lang) + "."}
}

type docsDeleteArgs struct {
	Name      string   `json:"name,omitempty"`
	All       bool     `json:"all,omitempty"`
	IDs       []string `json:"ids,omitempty"` // set once confirmed
	Confirmed bool     `json:"confirmed,omitempty"`
}

func matchDocsDelete(t *Turn) (json.RawMessage, bool) {
	q := docsQuery(t)
	if q == "" || len(q) > 300 {
		return nil, false
	}
	if docsDeleteAllRe.MatchString(q) {
		b, _ := json.Marshal(docsDeleteArgs{All: true})
		return b, true
	}
	for _, re := range docsDeleteRes {
		if m := re.FindStringSubmatch(q); m != nil {
			name := strings.Trim(strings.TrimSpace(m[1]), "„“\"'")
			b, _ := json.Marshal(docsDeleteArgs{Name: name})
			return b, true
		}
	}
	return nil, false
}

func runDocsDelete(t *Turn, raw json.RawMessage) Reply {
	var a docsDeleteArgs
	_ = json.Unmarshal(raw, &a)
	a.Name = cleanText(a.Name, docsNameMax)
	docs := visibleDocs(t)
	if a.Confirmed {
		var gone []string
		for _, id := range a.IDs {
			d, ok := docGet(id)
			if !ok || !docsScope(t)(d.Owner) || d.Source == "folder" {
				continue
			}
			if err := docsDelete(id); err == nil {
				gone = append(gone, docTitle(d.Name))
			}
		}
		if len(gone) == 0 {
			return Reply{Text: pick(t.Lang, "That document is already gone.", "Das Dokument ist schon weg.")}
		}
		return Reply{Text: pick(t.Lang, "Deleted: ", "Gelöscht: ") + joinAnd(gone, t.Lang) + "."}
	}
	// uploads only: a watched folder brings its files back on the next scan
	var deletable []DocMeta
	for _, d := range docs {
		if d.Source != "folder" {
			deletable = append(deletable, d)
		}
	}
	if len(deletable) == 0 {
		return Reply{Text: pick(t.Lang, "There are no uploaded documents to delete.", "Es gibt keine hochgeladenen Dokumente zum Löschen.")}
	}
	var targets []DocMeta
	switch {
	case a.All:
		targets = deletable
	case a.Name != "":
		targets = docsByName(deletable, a.Name)
		if len(targets) == 0 {
			return Reply{Text: pick(t.Lang, "I have no document called “"+a.Name+"”.", "Ich habe kein Dokument namens „"+a.Name+"“.")}
		}
	case len(deletable) == 1:
		targets = deletable
	}
	if len(targets) == 0 || (!a.All && len(targets) > 1) {
		pool := deletable
		if len(targets) > 1 {
			pool = targets
		}
		names := make([]string, len(pool))
		for i, d := range pool {
			names[i] = docTitle(d.Name)
		}
		return Reply{Text: pick(t.Lang, "Which document? ", "Welches Dokument? ") + joinAnd(names, t.Lang) + pick(t.Lang, " – say “delete the document” and its name.", " – sag „lösch das Dokument“ und den Namen.")}
	}
	if t.ClientID == "" {
		return Reply{Text: pick(t.Lang, "Please delete documents in the hub under Documents.", "Lösche Dokumente bitte in der Zentrale unter Dokumente.")}
	}
	ids := make([]string, len(targets))
	names := make([]string, len(targets))
	for i, d := range targets {
		ids[i], names[i] = d.ID, docTitle(d.Name)
	}
	var q string
	if len(targets) == 1 {
		q = pick(t.Lang, "Should I delete the document “"+names[0]+"”?", "Soll ich das Dokument „"+names[0]+"“ löschen?")
	} else {
		n := strconv.Itoa(len(targets))
		q = pick(t.Lang, "Should I delete all "+n+" documents?", "Soll ich alle "+n+" Dokumente löschen?")
	}
	b, _ := json.Marshal(docsDeleteArgs{IDs: ids, Confirmed: true})
	return t.Ask(Pending{Kind: "confirm", Tool: "docs_delete", Args: b}, q)
}

func init() {
	registerTool(Tool{
		Name:        "documents_list",
		Description: "List the documents in the hub (uploaded files and notes) by name.",
		Params:      func(*Turn) json.RawMessage { return json.RawMessage(`{"type":"object","properties":{}}`) },
		Gate:        []string{"dokumente", "dateien", "unterlagen", "documents", "files", "docs"},
		Access:      docsReadAccess,
		Run:         runDocsList,
	})
	registerTool(Tool{Name: "docs_list", Access: docsReadAccess, Run: runDocsList})
	registerTool(Tool{Name: "docs_delete", SideEffect: true,
		Access: func(*Turn, json.RawMessage) Access { return AccessWrite }, Run: runDocsDelete})
	// before docs_ask (90): "zeig mir meine Dokumente" lists, it does not search
	registerFastPath(FastPath{Name: "docs_list", Order: 88, Tool: "docs_list", Match: matchDocsList})
	registerFastPath(FastPath{Name: "docs_delete", Order: 88, Tool: "docs_delete", Match: matchDocsDelete})
}
