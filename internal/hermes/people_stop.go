package hermes

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// people_stop.go keeps "ich bin müde", "I'm a teacher" and friends from being
// taken as a self-introduction (Section 4.6). The enrol fast path only fires
// when the whole utterance is "ich bin <1–2 name tokens>"; these lists and
// the suffix rules below reject everything that is a state, a role, a
// profession, a demonym or a function word instead of a name.

// enrollStop holds folded (foldName) lower-case words that are never a name.
var enrollStop = func() map[string]bool {
	m := map[string]bool{}
	for _, group := range [][]string{
		enrollStopStates,
		enrollStopFunction,
		enrollStopRolesDE,
		enrollStopRolesEN,
	} {
		for _, w := range group {
			m[foldName(w)] = true
		}
	}
	return m
}()

// States, moods, places and time words ("ich bin müde / zurück / gleich da").
var enrollStopStates = []string{
	// German
	"müde", "hungrig", "durstig", "fertig", "zurück", "da", "hier", "dort", "wieder", "bereit",
	"sicher", "unsicher", "krank", "gesund", "wach", "satt", "sauer", "böse", "froh", "traurig",
	"glücklich", "allein", "alleine", "zuhause", "daheim", "unterwegs", "draußen", "drinnen",
	"drin", "raus", "weg", "los", "dran", "dabei", "gleich", "sofort", "gerade", "grad",
	"schon", "noch", "jetzt", "nicht", "kein", "keine", "kaputt", "pleite", "spät", "früh",
	"pünktlich", "neu", "neue", "neuer", "alt", "jung", "groß", "klein", "dick", "dünn", "nervös",
	"gestresst", "genervt", "verliebt", "verheiratet", "ledig", "single", "schwanger", "erkältet",
	"verletzt", "beschäftigt", "gespannt", "gut", "schlecht", "okay", "ok", "super", "toll",
	"cool", "oben", "unten", "drüben", "links", "rechts", "kalt", "warm", "heiß", "nass",
	"online", "offline", "wütend", "aufgeregt", "entspannt", "gelangweilt", "einsam", "stolz",
	"dankbar", "zufrieden", "unzufrieden", "dagegen", "dafür", "schuld", "raus", "dabei",
	"bald", "heute", "morgen", "gestern", "nachher", "später", "endlich", "fit", "platt",
	"erschöpft", "betrunken", "nüchtern", "verwirrt", "überrascht", "enttäuscht", "begeistert",
	"bereits", "eingeschlafen", "angekommen", "fast", "kurz", "lange", "gleichzeitig",
	"vegan", "vegetarisch", "fertig", "zufällig", "ehrlich", "ernst", "dumm", "blöd", "doof",
	"schlau", "klug", "faul", "fleißig", "reich", "arm", "frei", "besetzt", "wichtig", "schuld",
	// English
	"tired", "hungry", "thirsty", "fine", "good", "great", "well", "sick", "ill", "back", "home",
	"done", "ready", "sure", "unsure", "sorry", "here", "there", "busy", "free", "late", "early",
	"cold", "hot", "happy", "sad", "angry", "mad", "bored", "scared", "afraid", "worried",
	"nervous", "stressed", "excited", "glad", "alive", "dead", "awake", "asleep", "alone",
	"married", "pregnant", "new", "old", "young", "lost", "confused", "serious", "kidding",
	"joking", "curious", "right", "wrong", "fun", "funny", "weird", "nice", "awesome",
	"amazing", "terrible", "bad", "perfect", "true", "false", "crazy", "cool", "fine", "ok",
	"okay", "full", "broke", "rich", "poor", "drunk", "sober", "sleepy", "exhausted",
	"proud", "grateful", "thankful", "lonely", "safe", "fair", "honest", "stupid", "smart",
	"lazy", "away", "outside", "inside", "upstairs", "downstairs", "today", "tomorrow",
	"tonight", "now", "soon", "later", "finished", "going", "coming", "leaving", "listening",
	"gonna", "almost", "nearly", "still", "already", "important", "excellent",
}

// Function words: articles, pronouns, particles, prepositions, numbers in
// words ("ich bin die …" is never a name; neither is "I'm in").
var enrollStopFunction = []string{
	// German
	"ich", "du", "er", "sie", "es", "wir", "ihr", "man", "mich", "dich", "sich", "uns", "euch",
	"mir", "dir", "ihm", "ihnen", "mein", "meine", "dein", "deine", "sein", "seine", "unser",
	"das", "dies", "dieser", "diese", "dieses", "dass", "der", "die", "den", "dem", "des",
	"ein", "eine", "einer", "eines", "einem", "einen", "wer", "was", "wie", "wo", "wann",
	"warum", "wieso", "weil", "aber", "oder", "und", "an", "aus", "auf", "zu", "bei", "in",
	"im", "am", "mit", "ohne", "für", "gegen", "von", "vom", "zum", "zur", "nach", "vor",
	"über", "unter", "so", "sehr", "ganz", "echt", "wirklich", "total", "voll", "einfach",
	"auch", "nur", "ja", "nein", "doch", "mal", "halt", "eben", "wohl", "eh", "etwas", "was",
	"nichts", "alles", "immer", "nie", "niemals", "oft", "selbst", "selber", "eins", "zwei",
	"drei", "vier", "fünf", "sechs", "sieben", "acht", "neun", "zehn", "elf", "zwölf",
	"hundert", "jahre", "jahr", "bitte", "danke", "hallo", "hermes",
	// English
	"i", "me", "you", "he", "him", "she", "her", "it", "we", "us", "they", "them", "my",
	"your", "his", "its", "our", "their", "mine", "yours", "this", "that", "these", "those",
	"the", "a", "an", "what", "who", "how", "where", "when", "why", "because", "but", "or",
	"and", "in", "out", "on", "off", "up", "down", "at", "to", "for", "from", "with",
	"without", "of", "by", "about", "into", "so", "very", "really", "just", "also", "only",
	"not", "no", "yes", "all", "one", "two", "three", "four", "five", "six", "seven",
	"eight", "nine", "ten", "years", "year", "please", "thanks", "hello", "hi", "hey",
	"here", "there", "something", "nothing", "everything", "always", "never", "myself",
	"yourself", "too",
}

// Professions, family roles, demonyms and other predicate nouns (German).
var enrollStopRolesDE = []string{
	"lehrer", "lehrerin", "koch", "köchin", "arzt", "ärztin", "student", "studentin",
	"schüler", "schülerin", "rentner", "rentnerin", "ingenieur", "ingenieurin",
	"programmierer", "programmiererin", "entwickler", "entwicklerin", "informatiker",
	"informatikerin", "anwalt", "anwältin", "polizist", "polizistin", "pfleger", "pflegerin",
	"krankenschwester", "verkäufer", "verkäuferin", "bäcker", "bäckerin", "handwerker",
	"elektriker", "mechaniker", "maler", "malerin", "musiker", "musikerin", "künstler",
	"künstlerin", "sänger", "sängerin", "fahrer", "fahrerin", "pilot", "pilotin", "soldat",
	"soldatin", "beamter", "beamtin", "chef", "chefin", "boss", "mitarbeiter", "mitarbeiterin",
	"kollege", "kollegin", "praktikant", "praktikantin", "azubi", "lehrling", "hausfrau",
	"hausmann", "mutter", "vater", "mama", "papa", "mami", "papi", "oma", "opa", "sohn",
	"tochter", "bruder", "schwester", "onkel", "tante", "cousin", "cousine", "neffe", "nichte",
	"enkel", "enkelin", "freund", "freundin", "nachbar", "nachbarin", "mann", "frau", "kind",
	"junge", "mädchen", "baby", "gast", "besuch", "besucher", "besucherin", "kunde", "kundin",
	"nutzer", "nutzerin", "benutzer", "benutzerin", "mensch", "person", "niemand", "jemand",
	"berliner", "berlinerin", "hamburger", "hamburgerin", "münchner", "münchnerin", "kölner",
	"kölnerin", "deutscher", "deutsche", "österreicher", "österreicherin", "schweizer",
	"schweizerin", "amerikaner", "amerikanerin", "engländer", "engländerin", "franzose",
	"französin", "italiener", "italienerin", "spanier", "spanierin", "türke", "türkin", "pole",
	"polin", "russe", "russin", "ausländer", "ausländerin", "vegetarier", "vegetarierin",
	"veganer", "veganerin", "fan", "profi", "anfänger", "anfängerin", "experte", "expertin",
	"meister", "meisterin", "doktor", "professor", "professorin", "nerd", "idiot", "depp",
	"millionär", "selbstständig", "selbständig", "arbeitslos", "hausmeister", "erzieher",
	"erzieherin", "friseur", "friseurin", "kellner", "kellnerin", "tischler", "schreiner",
	"gärtner", "gärtnerin", "bauer", "bäuerin", "landwirt", "zahnarzt", "zahnärztin",
	"tierarzt", "tierärztin", "apotheker", "apothekerin", "journalist", "journalistin",
	"autor", "autorin", "schriftsteller", "schriftstellerin", "designer", "designerin",
	"architekt", "architektin", "berater", "beraterin", "manager", "managerin", "lehrkraft",
	"rentner", "pensionär", "papst", "könig", "königin", "prinz", "prinzessin", "held",
	"heldin", "chefkoch", "trainer", "trainerin", "spieler", "spielerin", "sportler",
	"sportlerin", "zuhörer", "zuhörerin", "anrufer", "anruferin",
}

// Professions, family roles, demonyms and other predicate nouns (English).
var enrollStopRolesEN = []string{
	"teacher", "student", "doctor", "nurse", "cook", "chef", "engineer", "developer",
	"programmer", "lawyer", "police", "officer", "driver", "pilot", "soldier", "farmer",
	"writer", "author", "artist", "musician", "singer", "manager", "boss", "employee",
	"worker", "intern", "retiree", "retired", "mom", "mum", "mother", "dad", "father",
	"mommy", "mummy", "daddy", "grandma", "grandpa", "granny", "son", "daughter", "brother",
	"sister", "uncle", "aunt", "auntie", "cousin", "nephew", "niece", "grandson",
	"granddaughter", "friend", "neighbor", "neighbour", "husband", "wife", "boyfriend",
	"girlfriend", "partner", "man", "woman", "kid", "child", "boy", "girl", "baby", "guest",
	"visitor", "customer", "user", "human", "person", "nobody", "somebody", "someone",
	"anyone", "american", "british", "english", "german", "french", "italian", "spanish",
	"canadian", "australian", "european", "irish", "scottish", "vegetarian", "vegan", "fan",
	"beginner", "expert", "professor", "nerd", "idiot", "genius", "robot", "hermes",
	"unemployed", "freelancer", "consultant", "designer", "architect", "journalist",
	"dentist", "vet", "pharmacist", "plumber", "electrician", "mechanic", "carpenter",
	"gardener", "waiter", "waitress", "barista", "cashier", "accountant", "scientist",
	"researcher", "coach", "player", "athlete", "king", "queen", "prince", "princess",
	"hero", "caller", "listener", "owner", "parent", "teen", "teenager", "adult", "grownup",
}

// enrollArticles are the articles that make "ich bin die Neue" a predicate.
var enrollArticles = wordSet("der", "die", "das", "den", "dem", "ein", "eine", "einer", "einen", "the", "a", "an")

// foldName lower-cases and folds umlauts, so typed "muede" and spoken "müde"
// hit the same stop-list entry.
func foldName(s string) string {
	s = strings.ToLower(s)
	return strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss", "’", "'").Replace(s)
}

// adjectiveLike reports a lower-case word that looks like an adjective or a
// participle ("traurig", "glücklich", "gestresst", "verliebt", "boring",
// "excited"). Speech recognition capitalises names, so the rule only applies
// to words that arrived in lower case; capitalised "Ludwig" stays a name.
func adjectiveLike(word string) bool {
	w := foldName(word)
	if utf8.RuneCountInString(w) < 5 {
		return false
	}
	for _, suf := range []string{"ig", "lich", "isch", "bar", "los", "sam", "haft",
		"ing", "ed", "ful", "less", "ous", "ive", "able", "ible", "ish"} {
		if strings.HasSuffix(w, suf) {
			return true
		}
	}
	for _, pre := range []string{"ge", "be", "ver", "er", "ent", "zer", "ueber"} {
		if strings.HasPrefix(w, pre) && (strings.HasSuffix(w, "t") || strings.HasSuffix(w, "en")) {
			return true
		}
	}
	return false
}

// nameStopWord reports whether word (as typed or recognised, any case) can
// never be (part of) a name.
func nameStopWord(word string) bool {
	if enrollStop[foldName(word)] || enrollArticles[strings.ToLower(word)] {
		return true
	}
	return isLowerWord(word) && adjectiveLike(word)
}

func isLowerWord(word string) bool {
	for _, r := range word {
		if unicode.IsUpper(r) {
			return false
		}
	}
	return true
}
