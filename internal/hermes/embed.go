package hermes

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"sync"
	"time"
	"unicode"
)

// embed.go: text embeddings for memories (and later documents) through the
// local Ollama embedding model, plus the lexical fallback used whenever no
// embedding model is configured or pulled (SPEC 9.B "embed.go").
//
// The HTTP call itself is the shared ollamaEmbed helper (ollama.go); this
// file adds the model-specific prefixes, the "model missing" cache that keeps
// a missing model from costing a request per turn, vector encoding and the
// lexical scorer.

// errEmbedModelMissing: the configured embedding model is not pulled in
// Ollama (HTTP 404). Cached for embedMissingTTL so callers fall back to the
// lexical scorer without asking Ollama again on every turn.
var errEmbedModelMissing = errors.New("embedding model is not available in Ollama")

// errEmbedDisabled: no embedding model is configured (lexical only).
var errEmbedDisabled = errors.New("no embedding model configured")

const (
	embedMissingTTL = 5 * time.Minute
	embedTimeout    = 30 * time.Second
	embedMaxBatch   = 64
)

// Embedding roles: a search query, or a stored text (memory, document chunk).
const (
	embedQuery = "query"
	embedDoc   = "doc"
)

var embedState = struct {
	sync.Mutex
	missing map[string]time.Time // model -> when it was found missing
	lastErr map[string]error     // model -> last error (nil after a success)
}{missing: map[string]time.Time{}, lastErr: map[string]error{}}

// resetEmbedState forgets cached model failures (tests, settings changes).
func resetEmbedState() {
	embedState.Lock()
	embedState.missing = map[string]time.Time{}
	embedState.lastErr = map[string]error{}
	embedState.Unlock()
}

// embedModelFor is the embedding model of the settings, "" = lexical only.
func embedModelFor(s *Settings) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.EmbedModel)
}

// embedMissing reports whether model was found missing within the TTL.
func embedMissing(model string) bool {
	embedState.Lock()
	defer embedState.Unlock()
	at, ok := embedState.missing[model]
	if !ok {
		return false
	}
	if nowFunc().Sub(at) >= embedMissingTTL {
		delete(embedState.missing, model)
		return false
	}
	return true
}

// embedOK is the status-pill view: a model is configured, it was not found
// missing recently and the last call (if any) worked. It never calls Ollama.
func embedOK(s *Settings) bool {
	model := embedModelFor(s)
	if model == "" || embedMissing(model) {
		return false
	}
	embedState.Lock()
	defer embedState.Unlock()
	return embedState.lastErr[model] == nil
}

// embedPrefix returns the task prefix a model was trained with. Unknown
// models get none.
func embedPrefix(model, role string) string {
	base := strings.ToLower(model)
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.Index(base, ":"); i >= 0 {
		base = base[:i]
	}
	switch {
	case strings.HasPrefix(base, "embeddinggemma"):
		if role == embedQuery {
			return "task: search result | query: "
		}
		return "title: none | text: "
	case strings.HasPrefix(base, "nomic-embed-text"):
		if role == embedQuery {
			return "search_query: "
		}
		return "search_document: "
	}
	return ""
}

// isEmbedNotFound recognises Ollama's "model not found" answer to /api/embed.
func isEmbedNotFound(err error) bool {
	if !errors.Is(err, errOllamaStatus) {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "/api/embed 404") ||
		(strings.Contains(msg, "/api/embed 400") && strings.Contains(strings.ToLower(msg), "not found"))
}

// embedTexts embeds inputs with the configured model and role prefix. It
// returns the model used so callers can store it next to the vectors.
// errEmbedDisabled / errEmbedModelMissing mean "use the lexical fallback".
func embedTexts(ctx context.Context, s *Settings, role string, inputs []string) ([][]float32, string, error) {
	model := embedModelFor(s)
	if model == "" {
		return nil, "", errEmbedDisabled
	}
	if embedMissing(model) {
		return nil, model, errEmbedModelMissing
	}
	if len(inputs) == 0 {
		return nil, model, nil
	}
	ctx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()
	prefix := embedPrefix(model, role)
	out := make([][]float32, 0, len(inputs))
	for start := 0; start < len(inputs); start += embedMaxBatch {
		end := min(start+embedMaxBatch, len(inputs))
		batch := make([]string, 0, end-start)
		for _, in := range inputs[start:end] {
			batch = append(batch, prefix+in)
		}
		vecs, err := ollamaEmbed(ctx, s, model, batch)
		if err != nil {
			embedState.Lock()
			if isEmbedNotFound(err) {
				embedState.missing[model] = nowFunc()
				err = errEmbedModelMissing
			}
			embedState.lastErr[model] = err
			embedState.Unlock()
			return nil, model, err
		}
		for _, v := range vecs {
			out = append(out, normalizeF32(v))
		}
	}
	embedState.Lock()
	embedState.lastErr[model] = nil
	embedState.Unlock()
	return out, model, nil
}

// embedOne embeds a single text.
func embedOne(ctx context.Context, s *Settings, role, text string) ([]float32, string, error) {
	vecs, model, err := embedTexts(ctx, s, role, []string{text})
	if err != nil {
		return nil, model, err
	}
	if len(vecs) != 1 {
		return nil, model, errors.New("embedding count mismatch")
	}
	return vecs[0], model, nil
}

// normalizeF32 returns v scaled to unit length (zero or non-finite vectors
// come back as nil so they never score).
func normalizeF32(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil
		}
		sum += f * f
	}
	if sum == 0 {
		return nil
	}
	n := math.Sqrt(sum)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / n)
	}
	return out
}

// cosine32 is the cosine similarity of two vectors; 0 when the lengths
// differ or either vector is empty.
func cosine32(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// encodeF32 stores a vector as base64 of little-endian float32s.
func encodeF32(v []float32) string {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return base64.StdEncoding.EncodeToString(b)
}

// decodeF32 reverses encodeF32; malformed input is an error.
func decodeF32(s string) ([]float32, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b)%4 != 0 {
		return nil, errors.New("vector length is not a multiple of 4 bytes")
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// Lexical fallback

// lexStopwords are DE+EN function words, pronouns, auxiliaries and fillers,
// stored umlaut-folded (für -> fur). Negations are stopwords too: polarity is
// judged separately (negationParity), so "mag keinen Koriander" and "mag
// Koriander" still count as the same topic.
var lexStopwords = wordSet(
	// German
	"der", "die", "das", "den", "dem", "des", "ein", "eine", "einen", "einem", "einer", "eines",
	"und", "oder", "aber", "doch", "denn", "sondern", "dass", "weil", "wenn", "als", "wie", "ob",
	"ich", "mich", "mir", "mein", "meine", "meinen", "meinem", "meiner", "meines",
	"du", "dich", "dir", "dein", "deine", "deinen", "deinem", "deiner", "deines",
	"er", "sie", "es", "ihn", "ihm", "ihr", "ihre", "ihren", "ihrem", "ihrer", "ihres",
	"sein", "seine", "seinen", "seinem", "seiner", "seines", "sich", "man",
	"wir", "uns", "unser", "unsere", "unseren", "unserem", "unserer", "euch", "euer", "eure",
	"ist", "bin", "bist", "sind", "seid", "war", "waren", "warst", "gewesen", "ware", "waren",
	"hat", "habe", "hab", "hast", "haben", "habt", "hatte", "hatten", "gehabt",
	"wird", "werde", "wirst", "werden", "wurde", "wurden", "worden",
	"kann", "kannst", "konnen", "konnte", "muss", "musst", "mussen", "soll", "sollst", "sollen",
	"will", "willst", "wollen", "darf", "darfst", "durfen", "mochte", "mochten",
	"nicht", "kein", "keine", "keinen", "keinem", "keiner", "keines", "nie", "niemals", "nichts",
	"auch", "noch", "schon", "nur", "sehr", "mal", "bitte", "ganz", "immer", "gerade", "grad",
	"eigentlich", "ubrigens", "halt", "eben", "wohl", "ja", "nein", "jetzt", "dann", "hier", "dort", "da",
	"mit", "von", "vom", "bei", "beim", "aus", "nach", "seit", "zu", "zum", "zur", "fur", "uber",
	"unter", "vor", "hinter", "neben", "zwischen", "durch", "gegen", "ohne", "um", "an", "am", "auf",
	"im", "in", "ins", "bis", "was", "wer", "wen", "wem", "wo", "wann", "warum", "wieso", "welche",
	"welcher", "welches", "dies", "diese", "dieser", "dieses", "diesen", "jene", "alle", "alles",
	"etwas", "viel", "viele", "mehr", "weniger", "so", "sowie", "zwar", "also", "hermes",
	"merk", "merke", "merken", "vergiss", "vergessen", "gemerkt", "nutzer", "nutzerin", "sprecher",
	// English
	"the", "a", "an", "and", "or", "but", "that", "this", "these", "those", "than", "then",
	"i", "me", "my", "mine", "myself", "you", "your", "yours", "he", "him", "his", "she", "her",
	"hers", "it", "its", "we", "us", "our", "ours", "they", "them", "their", "theirs",
	"is", "am", "are", "was", "were", "be", "been", "being", "has", "have", "had", "having",
	"do", "does", "did", "doing", "done", "will", "would", "shall", "should", "can", "could",
	"may", "might", "must", "not", "no", "never", "nothing", "don", "doesn", "didn", "isn",
	"aren", "wasn", "weren", "won", "can't", "also", "just", "very", "really", "please", "too",
	"of", "to", "in", "on", "at", "by", "for", "with", "from", "about", "into", "over", "under",
	"as", "if", "so", "what", "who", "whom", "which", "when", "where", "why", "how", "all", "any",
	"some", "more", "most", "much", "many", "now", "here", "there", "remember", "forget", "user", "speaker",
)

// lexFold lower-cases and folds umlauts and ß so "Müller" and "Mueller"-free
// spellings meet: ä->a, ö->o, ü->u, ß->ss.
func lexFold(s string) string {
	s = strings.ToLower(s)
	if !strings.ContainsAny(s, "äöüß") {
		return s
	}
	return strings.NewReplacer("ä", "a", "ö", "o", "ü", "u", "ß", "ss").Replace(s)
}

// lexStem strips one common DE/EN inflection suffix, keeping at least three
// runes of stem: -ing, -en, -er, -es, -e, -s.
func lexStem(w string) string {
	for _, suf := range []string{"ing", "en", "er", "es", "e", "s"} {
		if strings.HasSuffix(w, suf) && len([]rune(w))-len([]rune(suf)) >= 3 {
			return strings.TrimSuffix(w, suf)
		}
	}
	return w
}

func isNumberToken(w string) bool {
	if w == "" {
		return false
	}
	for _, r := range w {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// lexTokens returns the distinct content stems of s for lexical matching:
// words of at least three runes (or numbers) that are not stopwords, folded
// and stemmed. exclude holds folded words to drop (e.g. the speaker's name,
// which every memory of that person contains).
func lexTokens(s string, exclude map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range tokenize(s) {
		f := lexFold(w)
		if lexStopwords[f] || exclude[f] {
			continue
		}
		if !isNumberToken(f) && len([]rune(f)) < 3 {
			continue
		}
		st := lexStem(f)
		if !seen[st] {
			seen[st] = true
			out = append(out, st)
		}
	}
	return out
}

// lexScore rates how well text matches query on content stems, 0..1:
// mostly the share of the query found in the text, plus a little for how
// much of the text the query covers (prefers the more specific entry).
func lexScore(query, text string) float64 {
	return lexScoreExcl(query, text, nil)
}

func lexScoreExcl(query, text string, exclude map[string]bool) float64 {
	q := lexTokens(query, exclude)
	d := lexTokens(text, exclude)
	if len(q) == 0 || len(d) == 0 {
		return 0
	}
	in := map[string]bool{}
	for _, w := range d {
		in[w] = true
	}
	hit := 0
	for _, w := range q {
		if in[w] {
			hit++
		}
	}
	return 0.7*float64(hit)/float64(len(q)) + 0.3*float64(hit)/float64(len(d))
}

// lexJaccard is the Jaccard similarity of the content stems of a and b.
func lexJaccard(a, b string, exclude map[string]bool) float64 {
	x := lexTokens(a, exclude)
	y := lexTokens(b, exclude)
	if len(x) == 0 || len(y) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, w := range x {
		set[w] = true
	}
	inter := 0
	union := len(set)
	for _, w := range y {
		if set[w] {
			inter++
		} else {
			union++
		}
	}
	return float64(inter) / float64(union)
}
