package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEncodeDecodeF32(t *testing.T) {
	v := []float32{0, 1, -1.5, 3.25e-7, float32(math.Pi)}
	got, err := decodeF32(encodeF32(v))
	if err != nil || len(got) != len(v) {
		t.Fatalf("roundtrip %v %v", got, err)
	}
	for i := range v {
		if got[i] != v[i] {
			t.Errorf("[%d] %v != %v", i, got[i], v[i])
		}
	}
	// Little-endian float32: 1.0 is 00 00 80 3f.
	if enc := encodeF32([]float32{1}); enc != "AACAPw==" {
		t.Errorf("encoding %q", enc)
	}
	for _, bad := range []string{"!!", "AAA="} {
		if _, err := decodeF32(bad); err == nil {
			t.Errorf("decodeF32(%q) accepted", bad)
		}
	}
}

func TestCosineAndNormalize(t *testing.T) {
	if c := cosine32([]float32{1, 0}, []float32{1, 0}); math.Abs(c-1) > 1e-9 {
		t.Errorf("same %v", c)
	}
	if c := cosine32([]float32{1, 0}, []float32{0, 2}); c != 0 {
		t.Errorf("orthogonal %v", c)
	}
	if c := cosine32([]float32{1, 1}, []float32{-1, -1}); math.Abs(c+1) > 1e-6 {
		t.Errorf("opposite %v", c)
	}
	if cosine32([]float32{1}, []float32{1, 0}) != 0 || cosine32(nil, nil) != 0 || cosine32([]float32{0, 0}, []float32{1, 0}) != 0 {
		t.Error("degenerate inputs must score 0")
	}
	n := normalizeF32([]float32{3, 4})
	if math.Abs(float64(n[0])-0.6) > 1e-6 || math.Abs(float64(n[1])-0.8) > 1e-6 {
		t.Errorf("normalize %v", n)
	}
	if normalizeF32([]float32{0, 0}) != nil || normalizeF32([]float32{float32(math.NaN())}) != nil {
		t.Error("zero or NaN vectors must normalise to nil")
	}
}

func TestEmbedPrefix(t *testing.T) {
	cases := []struct{ model, role, want string }{
		{"embeddinggemma", embedQuery, "task: search result | query: "},
		{"embeddinggemma:300m", embedDoc, "title: none | text: "},
		{"library/EmbeddingGemma:latest", embedDoc, "title: none | text: "},
		{"nomic-embed-text", embedQuery, "search_query: "},
		{"nomic-embed-text:v1.5", embedDoc, "search_document: "},
		{"bge-m3", embedQuery, ""},
	}
	for _, c := range cases {
		if got := embedPrefix(c.model, c.role); got != c.want {
			t.Errorf("embedPrefix(%q, %q) = %q", c.model, c.role, got)
		}
	}
}

// fakeEmbed serves /api/embed; status != 200 answers an error.
func fakeEmbed(t *testing.T, status *int) *[]map[string]any {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any
	fakeOllamaServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		if *status != http.StatusOK {
			http.Error(w, `{"error":"model \"embeddinggemma\" not found, try pulling it first"}`, *status)
			return
		}
		in := body["input"].([]any)
		vecs := make([][]float32, len(in))
		for i := range in {
			vecs[i] = []float32{float32(i + 1), 0, 0, 2}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	})
	return &bodies
}

func TestEmbedTextsPrefixesBatchesAndNormalises(t *testing.T) {
	useTempSettings(t)
	resetEmbedState()
	t.Cleanup(resetEmbedState)
	status := http.StatusOK
	bodies := fakeEmbed(t, &status)
	s := defaultSettings()
	s.EmbedModel, s.KeepAlive = "embeddinggemma", "30m"

	inputs := make([]string, embedMaxBatch+3)
	for i := range inputs {
		inputs[i] = "Anna mag Jazz."
	}
	vecs, model, err := embedTexts(context.Background(), s, embedDoc, inputs)
	if err != nil || model != "embeddinggemma" || len(vecs) != len(inputs) {
		t.Fatalf("embedTexts: %d vecs, %q, %v", len(vecs), model, err)
	}
	if len(*bodies) != 2 {
		t.Errorf("%d requests, want 2 batches", len(*bodies))
	}
	first := (*bodies)[0]
	if first["model"] != "embeddinggemma" || first["keep_alive"] != "30m" ||
		first["input"].([]any)[0] != "title: none | text: Anna mag Jazz." {
		t.Errorf("request %v", first)
	}
	var norm float64
	for _, x := range vecs[0] {
		norm += float64(x) * float64(x)
	}
	if math.Abs(norm-1) > 1e-5 {
		t.Errorf("vector not normalised: |v|²=%v", norm)
	}
	if !embedOK(s) {
		t.Error("embedOK after a success")
	}
	q, _, err := embedOne(context.Background(), s, embedQuery, "Jazz")
	if err != nil || q == nil {
		t.Fatal(err)
	}
	if in := (*bodies)[2]["input"].([]any)[0]; in != "task: search result | query: Jazz" {
		t.Errorf("query input %q", in)
	}
}

func TestEmbedModelMissingIsCached(t *testing.T) {
	useTempSettings(t)
	resetEmbedState()
	t.Cleanup(resetEmbedState)
	now := time.Now()
	old := nowFunc
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = old })
	status := http.StatusNotFound
	bodies := fakeEmbed(t, &status)
	s := defaultSettings()
	s.EmbedModel = "embeddinggemma"

	if _, _, err := embedOne(context.Background(), s, embedDoc, "x"); !errors.Is(err, errEmbedModelMissing) {
		t.Fatalf("404: %v", err)
	}
	if _, _, err := embedOne(context.Background(), s, embedDoc, "x"); !errors.Is(err, errEmbedModelMissing) {
		t.Fatalf("cached: %v", err)
	}
	if len(*bodies) != 1 {
		t.Errorf("missing model asked %d times within the TTL", len(*bodies))
	}
	if embedOK(s) || !embedMissing("embeddinggemma") {
		t.Error("status must report the missing model")
	}
	// After the TTL, Ollama is asked again (the model may have been pulled).
	status = http.StatusOK
	nowFunc = func() time.Time { return now.Add(embedMissingTTL) }
	if _, _, err := embedOne(context.Background(), s, embedDoc, "x"); err != nil {
		t.Fatalf("after TTL: %v", err)
	}
	if len(*bodies) != 2 || !embedOK(s) {
		t.Errorf("requests %d, ok %v", len(*bodies), embedOK(s))
	}

	// A server error is not "missing": no caching, but status is not ok.
	status = http.StatusInternalServerError
	if _, _, err := embedOne(context.Background(), s, embedDoc, "x"); err == nil || errors.Is(err, errEmbedModelMissing) {
		t.Errorf("500: %v", err)
	}
	if embedOK(s) || embedMissing("embeddinggemma") {
		t.Error("500 must not mark the model missing but must clear embedOk")
	}

	// No model configured: lexical only, no request.
	s.EmbedModel = ""
	n := len(*bodies)
	if _, _, err := embedTexts(context.Background(), s, embedDoc, []string{"x"}); !errors.Is(err, errEmbedDisabled) || len(*bodies) != n {
		t.Errorf("disabled: %v", err)
	}
	if embedOK(s) {
		t.Error("embedOk without a model")
	}
}

func TestLexicalScorer(t *testing.T) {
	if got := lexFold("Größe Übermäßig"); got != "grosse ubermassig" {
		t.Errorf("fold %q", got)
	}
	for in, want := range map[string]string{"spielen": "spiel", "walking": "walk", "katze": "katz", "hunde": "hund", "bus": "bus", "rom": "rom"} {
		if got := lexStem(in); got != want {
			t.Errorf("lexStem(%q) = %q", in, got)
		}
	}
	toks := lexTokens("Ich mag keinen Koriander, und 42 ist meine Zahl", nil)
	if strings.Join(toks, ",") != "mag,koriand,42,zahl" {
		t.Errorf("tokens %v", toks)
	}
	if toks := lexTokens("Anna mag Hunde", nameExclusions("Anna")); strings.Join(toks, ",") != "mag,hund" {
		t.Errorf("tokens without name %v", toks)
	}
	kor := lexScore("Koriander", "Anna mag keinen Koriander.")
	dog := lexScore("Koriander", "Annas Hund heißt Bello.")
	if kor < 0.7 || dog != 0 {
		t.Errorf("scores %v %v", kor, dog)
	}
	// Cross-form match: plural / inflection.
	if lexScore("Hunde", "Anna hat einen Hund") < 0.5 {
		t.Error("stemmed match")
	}
	if lexScore("", "x") != 0 || lexScore("der die das", "Hund") != 0 {
		t.Error("empty queries score 0")
	}
	if j := lexJaccard("Anna mag keinen Koriander.", "Anna mag Koriander.", nameExclusions("Anna")); j != 1 {
		t.Errorf("jaccard ignoring negation %v", j)
	}
	if j := lexJaccard("Anna wohnt in Berlin.", "Anna spielt Geige.", nameExclusions("Anna")); j != 0 {
		t.Errorf("unrelated jaccard %v", j)
	}
}
