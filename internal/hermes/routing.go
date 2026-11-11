package hermes

import (
	"strconv"
	"strings"
	"unicode"
)

// This file decides where a user message is routed: the local real-time
// answers (weather / time / news, realtime.go), internet image search, the
// "I cannot draw" notice for image generation requests, or the local model.
// Everything works on whole words so that "brain" is not "rain", "update" is
// not "date" and "photosynthesis" is not a photo.

// tokenize lowercases s and splits it into words made of letters and digits
// (Unicode aware, so umlauts and ß stay inside their words).
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// phraseAt reports whether the space-separated phrase occurs in tokens
// starting at index i.
func phraseAt(tokens []string, i int, phrase []string) bool {
	if i < 0 || i+len(phrase) > len(tokens) {
		return false
	}
	for j, p := range phrase {
		if tokens[i+j] != p {
			return false
		}
	}
	return true
}

// hasPhrase reports whether phrase (one or more words) occurs as consecutive
// whole tokens.
func hasPhrase(tokens []string, phrase string) bool {
	p := strings.Fields(phrase)
	if len(p) == 0 {
		return false
	}
	for i := range tokens {
		if phraseAt(tokens, i, p) {
			return true
		}
	}
	return false
}

func hasAnyPhrase(tokens []string, phrases []string) bool {
	for _, p := range phrases {
		if hasPhrase(tokens, p) {
			return true
		}
	}
	return false
}

func wordSet(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// A real-time match sends the city or topic to a public data service
// (Open-Meteo, the news feeds) and answers from a template, so the router
// only fires on questions about live data. Bare topic words ("rain", "news", "Nebel") are not enough: "Explain
// how rain forms" or "Ich habe Nachrichten von meiner Mutter" stay local.

// The realtime phrase groups are live-data questions that are answered by
// realtime.go on their own (unless a task veto applies). Single words match
// whole tokens; multi-word entries match consecutive tokens. The group a
// question matches also decides what kind of answer it gets.
var realtimeWeatherPhrases = []string{
	// English
	"weather", "degrees outside", "how hot", "how cold", "how warm",
	"freezing outside", "how windy", "how humid", "how sunny", "how cloudy",
	"is it raining", "is it snowing", "is it sunny", "is it windy",
	"is it cloudy", "is it humid", "is it foggy", "is it stormy",
	"is it hailing", "is it freezing",
	// Deutsch
	"wetter", "wetterbericht", "wie viel grad", "wieviel grad", "grad draußen",
	"grad draussen", "grad hat", "wie warm", "wie kalt", "wie windig",
	"regnet es", "schneit es", "hagelt es", "stürmt es", "stuermt es",
	"ist es windig", "ist es sonnig", "ist es bewölkt", "ist es bewoelkt",
	"ist es neblig", "ist es stürmisch", "ist es stuermisch",
	// Clothing questions are about today's weather.
	"need an umbrella", "need a jacket", "need a coat", "bring an umbrella",
	"bring a jacket", "brauche ich einen regenschirm", "brauch ich einen regenschirm",
	"brauche ich einen schirm", "brauche ich eine jacke",
}

var realtimeTimePhrases = []string{
	"what time", "time is it", "current time", "local time", "time zone",
	"timezone", "tell me the time", "s the time", "is the time", "the time now",
	"wie viel uhr", "wieviel uhr", "wie spät", "wie spaet", "uhrzeit",
}

var realtimeDatePhrases = []string{
	"what date", "date today", "today s date", "current date", "s the date",
	"is the date", "the date today",
	"what day", "which day", "day is it", "day of the week",
	"what year is it", "which year is it", "year is it", "current year",
	"welches datum", "das datum", "heutige datum", "heutiges datum",
	"datum heute", "datum von heute", "wochentag", "welcher tag",
	"heute für ein tag", "heute fuer ein tag", "welches jahr", "jahr haben wir",
}

var realtimeNewsPhrases = []string{
	"breaking news", "current events", "any news", "latest news",
	"latest headlines", "top news", "top headlines", "today s news",
	"today s headlines", "what s in the news", "what is in the news",
	"anything in the news",
	"was gibt s neues", "was gibts neues", "was gibt es neues",
	"aktuelle nachrichten", "aktuellen nachrichten", "neueste nachrichten",
	"neuesten nachrichten", "heutigen nachrichten",
}

// realtimePhrases is every group: any of them makes a live-data question.
var realtimePhrases = concatPhrases(realtimeWeatherPhrases, realtimeTimePhrases, realtimeDatePhrases, realtimeNewsPhrases)

func concatPhrases(groups ...[]string) []string {
	var out []string
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// realtimeTopicWords name weather topics. They count only together with a
// time or place anchor ("snow tomorrow", "temperature in Paris") or right
// after a question opener ("what's the forecast"). Without one, "Jon Snow",
// "Rain Man" or "Nebel des Krieges" would get a weather report.
var realtimeTopicWords = []string{
	"forecast", "rain", "raining", "rainy", "snow", "snowing", "snowy", "wind",
	"windy", "humidity", "humid", "sunny", "cloudy", "celsius", "fahrenheit",
	"umbrella", "thunderstorm", "thunderstorms", "storm", "stormy", "hail",
	"hailing", "fog", "foggy", "frost", "hot", "cold", "jacket", "coat",
	"temperature", "temperatures",
	"wettervorhersage", "vorhersage", "regen", "regnet", "regnen", "schnee",
	"schneit", "schneien", "windig", "luftfeuchtigkeit", "sonnig", "bewoelkt",
	"bewölkt", "nebel", "neblig", "gewitter", "hagel", "hagelt", "glatteis",
	"sturm", "stürmisch", "stuermisch", "regenschirm", "heiss", "heiß",
	"kalt", "jacke", "temperatur", "temperaturen",
}

// realtimeNewsWords count only with a time anchor ("news today") or right
// after a question opener ("gibt es Nachrichten aus Bayern"). A place is not
// enough ("what's new in Go 1.23"), and German "Nachrichten" also means
// personal messages, so first-person statements are excluded.
var realtimeNewsWords = []string{
	"news", "headline", "headlines", "nachrichten", "schlagzeilen",
	"neuigkeiten", "what s new", "anything new",
}

// realtimeQuestionOpeners turn a directly following topic or news word into a
// live-data question: "what's the humidity", "wie ist die Luftfeuchtigkeit",
// "gibt es Gewitter", "what are the headlines".
var realtimeQuestionOpeners = []string{
	"what s the", "what is the", "how s the", "how is the", "what are the",
	"how are the", "will there be", "is there", "are there", "any",
	"wie ist die", "wie ist der", "wie ist das", "wie wird die", "wie wird der",
	"wie wird das", "was sagt die", "was sagt der", "wie sind die",
	"was sind die", "gibt es", "gibt s", "gibts", "kommt",
	"tell me the", "give me the", "read me the", "read the", "sag mir die",
	"gib mir die", "lies mir die", "erzähl mir die", "erzaehl mir die",
}

// newsNouns asked for on their own or with a topic ("news", "Nachrichten aus
// Bayern", "news about the election") are a request for the news.
var newsNouns = wordSet("news", "headlines", "nachrichten", "schlagzeilen", "neuigkeiten")

// newsTopicLinkers may follow a leading news noun: "news about ...".
var newsTopicLinkers = wordSet("about", "on", "from", "regarding", "über", "ueber", "aus", "zu", "zum", "zur")

// realtimeAnchors mark "now" questions.
var realtimeAnchors = []string{
	"now", "today", "tonight", "tomorrow", "outside", "currently", "this week",
	"this weekend", "will it", "is it going to", "heute", "heut", "morgen",
	"übermorgen", "uebermorgen", "jetzt", "gerade", "draußen", "draussen",
	"aktuell", "momentan", "wird es",
}

// notAPlace lists words after "in" / "for" that do not name a location:
// articles, languages, seasons and programming tools ("what's new in Go").
var notAPlace = wordSet(
	"the", "a", "an", "my", "your", "this", "that", "general", "detail",
	"details", "physics", "english", "german", "deutsch", "math", "chemistry",
	"der", "die", "das", "dem", "den", "einem", "einer", "meinem", "meiner",
	"grad", "celsius", "fahrenheit", "kelvin", "degrees", "short", "kurz",
	"simple", "words", "it", "me", "you", "us", "mich", "dich", "uns",
	"winter", "summer", "spring", "autumn", "fall", "sommer", "herbst",
	"frühling", "fruehling", "theory", "theorie", "practice", "fiction",
	"movies", "films", "film", "books", "games", "school", "class",
	"go", "golang", "python", "java", "javascript", "typescript", "rust", "c",
	"react", "node", "linux", "windows", "excel", "sql", "html", "css",
	"code", "version", "v",
)

// realtimeCompoundStop follows a topic word that is part of a compound noun
// rather than the weather: "wind turbine", "cold brew", "storm drain".
var realtimeCompoundStop = wordSet(
	"turbine", "turbines", "energy", "power", "farm", "farms", "mill", "mills",
	"park", "parks", "instrument", "instruments", "chime", "chimes", "tunnel",
	"brew", "war", "case", "cases", "storage", "shoulder", "feet", "sore",
	"drain", "drains", "chaser", "chasers", "trooper", "troopers", "man",
	"forest", "forests", "dance", "water", "gauge", "boots", "jacket",
	"rad", "kraft", "energie", "wald", "sport",
)

// realtimeHardVeto words turn the message into a task on given text or a
// creative request; such messages stay local even with a time anchor
// ("translate 'what time is it'", "a poem about the weather today").
var realtimeHardVeto = wordSet(
	"summarize", "summarise", "summary", "translate", "translation", "rewrite",
	"paraphrase", "proofread", "poem", "poems", "joke", "jokes", "story",
	"stories", "song", "lyrics", "recipe", "recipes", "movie", "movies",
	"film", "define", "definition", "essay",
	"zusammenfassen", "zusammenfassung", "fasse", "übersetze", "übersetzen",
	"uebersetze", "uebersetzen", "übersetzung", "gedicht", "gedichte", "witz",
	"witze", "geschichte", "lied", "rezept", "rezepte", "definiere", "aufsatz",
)

// realtimeSoftVeto marks explanations and definitions ("Explain how weather
// forms", "Was ist eine Wettervorhersage?"); they stay local unless a time
// anchor makes them about now ("why is it so cold today").
var realtimeSoftVeto = []string{
	"explain", "explains", "explained", "describe", "write", "why", "how does",
	"how do", "what is a", "what is an", "what s a", "what s an",
	"erklär", "erkläre", "erklärt", "erklaer", "erklaere", "erklaert",
	"beschreibe", "schreib", "schreibe", "warum", "wieso", "weshalb",
	"wie funktioniert", "wie entsteht", "wie entstehen",
	"was ist ein", "was ist eine", "was ist einer",
}

// personalWords mark a first-person statement ("I got news from my mother").
var personalWords = wordSet(
	"i", "my", "we", "our", "ich", "mein", "meine", "meiner", "meinem",
	"meinen", "wir", "unser", "unsere",
)

// dateTimeStop follows "the date" / "the time" when they refer to history:
// "the date of the French revolution", "at the time of Napoleon", "what date
// did WW2 end". It also lists technical compounds ("time complexity", "date
// format") that are not questions about the current time at all.
var dateTimeStop = wordSet(
	"of", "der", "des", "von", "when", "did", "was", "were",
	"complexity", "komplexität", "komplexitaet", "limit", "limits", "travel",
	"traveler", "traveller", "frame", "frames", "machine", "series", "signature",
	"stamp", "stamps", "slot", "slots", "lapse", "loop", "span", "management",
	"constant", "constants", "domain", "format", "formats", "formatting",
	"type", "types", "picker", "field", "fields", "function", "functions",
	"variable", "object", "objects", "class", "string", "strings", "parse",
	"parsing", "library", "module", "package", "api", "column", "columns",
	"value", "values", "range", "ranges", "difference", "dilation", "crystal",
	"keeper", "capsule", "bomb", "out", "signatures", "budget", "sheet",
	"tracking", "tracker", "unit", "units", "step", "steps", "period",
	"periods", "arithmetic", "conversion", "handling", "zones",
)

func endsInDateWord(p []string) bool {
	switch p[len(p)-1] {
	case "date", "time", "day", "year", "datum", "tag", "jahr":
		return true
	}
	return false
}

// matchRealtimePhrase is hasPhrase with the history guard for date/time words.
func matchRealtimePhrase(tokens []string, phrase string) bool {
	p := strings.Fields(phrase)
	for i := range tokens {
		if !phraseAt(tokens, i, p) {
			continue
		}
		next := i + len(p)
		if endsInDateWord(p) && next < len(tokens) && dateTimeStop[tokens[next]] {
			continue
		}
		return true
	}
	return false
}

// matchTopic reports whether one of words occurs, skipping compound nouns
// ("wind turbine"). opened reports that a match directly follows a question
// opener ("what's the forecast").
func matchTopic(tokens []string, words []string) (found, opened bool) {
	for _, w := range words {
		p := strings.Fields(w)
		for i := range tokens {
			if !phraseAt(tokens, i, p) {
				continue
			}
			next := i + len(p)
			if next < len(tokens) && realtimeCompoundStop[tokens[next]] {
				continue
			}
			found = true
			for _, o := range realtimeQuestionOpeners {
				op := strings.Fields(o)
				if phraseAt(tokens, i-len(op), op) {
					return true, true
				}
			}
		}
	}
	return found, false
}

func hasTimeAnchor(tokens []string) bool { return hasAnyPhrase(tokens, realtimeAnchors) }

// newsRequest reports a message that opens with a news noun and has nothing
// after it but a topic: "news", "the headlines", "Nachrichten aus Bayern".
func newsRequest(tokens []string) bool {
	i := firstContentIndex(tokens)
	for i < len(tokens) && (tokens[i] == "the" || tokens[i] == "die" || tokens[i] == "latest") {
		i++
	}
	if i >= len(tokens) || !newsNouns[tokens[i]] {
		return false
	}
	return i+1 > lastContentIndex(tokens) || newsTopicLinkers[tokens[i+1]]
}

// hasPlaceAnchor reports an "in <place>" / "for <place>" phrase.
func hasPlaceAnchor(tokens []string) bool {
	for i, t := range tokens {
		if (t == "in" || t == "for" || t == "für" || t == "fuer") && i+1 < len(tokens) && !notAPlace[tokens[i+1]] {
			if _, err := strconv.Atoi(tokens[i+1]); err != nil {
				return true
			}
		}
	}
	return false
}

// wantsRealtime reports whether q asks for live data (weather, time, news).
func wantsRealtime(q string) bool {
	tokens := tokenize(q)
	if hasAnyWord(tokens, realtimeHardVeto) {
		return false
	}
	timed := hasTimeAnchor(tokens)
	if !timed && hasAnyPhrase(tokens, realtimeSoftVeto) {
		return false
	}
	for _, p := range realtimePhrases {
		if matchRealtimePhrase(tokens, p) {
			return true
		}
	}
	if found, opened := matchTopic(tokens, realtimeTopicWords); opened || (found && (timed || hasPlaceAnchor(tokens))) {
		return true
	}
	found, opened := matchTopic(tokens, realtimeNewsWords)
	if opened {
		return true
	}
	return found && (timed || newsRequest(tokens)) && !hasAnyWord(tokens, personalWords)
}

// Image intent vocabulary.
var (
	// Nouns that make "create / generate / make" an image request. "art" is
	// not one on its own ("art museums", "art rock").
	imageGenNouns = wordSet(
		"image", "images", "picture", "pictures", "pic", "pics", "photo", "photos",
		"illustration", "illustrations", "drawing", "drawings", "painting",
		"paintings", "artwork", "artworks", "wallpaper", "wallpapers",
		"logo", "logos", "portrait", "portraits", "sketch", "poster", "posters",
		"icon", "icons",
		"bild", "bilder", "foto", "fotos", "grafik", "grafiken", "zeichnung",
		"zeichnungen", "gemälde", "gemaelde", "porträt", "portraet", "kunstwerk",
		"hintergrundbild", "skizze",
	)
	// Nouns that make a search request (a subset: no gerunds like "drawing").
	imageSearchNouns = wordSet(
		"image", "images", "picture", "pictures", "pic", "pics", "photo", "photos",
		"photograph", "photographs", "wallpaper", "wallpapers",
		"bild", "bilder", "bildern", "foto", "fotos", "fotografie", "fotografien",
		"abbildung", "abbildungen",
	)
	// Generic verbs that only mean image generation when they lead the request
	// and take an image noun as their direct object.
	imageGenGenericVerbs = wordSet(
		"generate", "create", "make", "render", "design", "produce", "craft",
		"generiere", "generier", "erzeuge", "erzeug", "erstelle", "erstell",
		"mach", "mache", "kreiere", "entwirf", "entwerfe",
	)
	// German infinitives closing a "kannst du ... ein Bild erstellen" request.
	imageGenInfinitiveDE = wordSet(
		"generieren", "erzeugen", "erstellen", "machen", "kreieren", "entwerfen",
		"rendern",
	)
	// English art verbs: draw / paint / sketch.
	imageArtVerbsEN = wordSet("draw", "paint", "sketch")
	// German imperatives that are unambiguous on their own.
	imageArtImperativeDE = wordSet("zeichne", "skizziere")
	// German infinitives ("kannst du einen Hund malen").
	imageArtInfinitiveDE = wordSet("malen", "zeichnen", "skizzieren")

	// After a leading "male" (paint!) one of these must follow; "mal" alone is
	// not accepted ("Mal eine Frage: ...").
	germanObjectStarters = wordSet(
		"mir", "uns", "ein", "eine", "einen", "einem", "einer", "das", "den",
		"die", "bitte", "mal", "etwas", "irgendwas", "meinen", "meine", "mein",
	)
	// Words that may precede a leading imperative ("Hermes, please draw ...").
	leadingFillers = wordSet(
		"hermes", "hey", "hi", "hallo", "ok", "okay", "please", "bitte", "bt",
		"pilot", "now", "jetzt", "so", "also", "and", "und", "doch", "noch",
	)
	// Words before an English art verb that make it a request to the assistant.
	artVerbRequestPrev = wordSet("you", "u", "please", "to", "and", "then", "now", "pls")
	// Objects that turn draw/paint/sketch into an idiom.
	artVerbIdiomObjects = wordSet(
		"conclusion", "conclusions", "attention", "line", "lines", "near", "blood",
		"lots", "card", "cards", "breath", "water", "money", "inspiration",
		"comparison", "comparisons", "parallel", "parallels", "distinction",
		"closer", "back", "from", "on", "up", "out", "upon", "in", "into",
		"plan", "outline", "fire", "crowd", "crowds", "salary", "pension",
		"interest", "strength", "power", "energy", "current",
	)
	articles = wordSet("a", "an", "the", "some", "my", "your", "our", "any", "this", "that")
	// Question words: a message that asks how/what/where is not an order to
	// produce or fetch a picture ("how do I make a photo smaller").
	questionWords = wordSet(
		"how", "what", "why", "where", "who", "whom", "whose", "which", "when",
		"wie", "was", "warum", "wieso", "weshalb", "wo", "woher", "wohin", "wer",
		"wen", "wem", "welche", "welcher", "welches", "welchen", "wann",
	)
	// Words that mark a technical / coding request ("create an image upload
	// form in HTML", "Erstelle eine Grafik mit matplotlib").
	imageTechWords = wordSet(
		"code", "python", "html", "css", "javascript", "typescript", "matplotlib",
		"opengl", "numpy", "pandas", "opencv", "pillow", "latex", "sql", "excel",
		"script", "function", "funktion", "class", "klasse", "programm", "program",
		"algorithm", "algorithmus", "tutorial", "plotly", "seaborn", "canvas",
		"ffmpeg", "imagemagick", "gimp", "photoshop", "convert", "resize",
		"compress",
	)
	// Words after an image noun that make it a technical compound ("image
	// classification model") or the idiom "mach dir ein Bild davon".
	imageTechModifiers = wordSet(
		"classification", "classifier", "recognition", "upload", "uploader",
		"processing", "processor", "form", "model", "player", "viewer", "editor",
		"gallery", "slider", "carousel", "component", "file", "files", "format",
		"size", "compression", "loader", "parser", "tag", "element", "button",
		"url", "link", "dataset", "filter", "generator", "sensor", "api",
		"davon", "darüber", "darueber",
		// Physical objects named after pictures ("make a picture frame").
		"frame", "frames", "rahmen", "bilderrahmen", "hook", "hooks", "haken",
		"book", "books", "album", "albums", "booth", "wall", "rail", "light",
		"hanger", "puzzle", "puzzles", "mount", "stand", "paper", "printer",
		"studio", "shoot", "shooting", "session", "kamera", "camera",
	)
	// Reflexive objects that turn "mach ... ein Bild" into the idiom
	// "sich ein Bild machen" (to form an opinion).
	reflexiveDE = wordSet("dir", "sich", "euch", "selbst", "selber")
	// Verbs that turn an image noun into a search request when they lead it.
	// Bare "see" / "get" are too common to count.
	imageSearchVerbs = wordSet(
		"find", "search", "show", "look", "fetch", "google",
		"zeig", "zeige", "such", "suche", "finde", "hol", "hole", "gib",
	)
	// German infinitives closing a "kannst du mir Bilder von X zeigen" request.
	imageSearchInfinitiveDE = wordSet("zeigen", "suchen", "finden", "raussuchen", "heraussuchen")
	// Words that may sit between a search verb and its image noun.
	searchVerbParticles = wordSet(
		"for", "up", "at", "me", "us", "nach", "mir", "uns", "mal", "bitte", "raus",
		"heraus", "a", "an", "the", "some", "any", "few", "couple", "more", "nice",
		"ein", "eine", "einen", "einige", "paar", "mehr", "schöne", "schoene",
	)
	// Words after an image noun that make it a search ("pictures of cats").
	imageNounLinkers = wordSet("of", "von", "vom", "from", "about", "zu", "zum", "zur", "aus")
	// Words after an image noun that make it an adjective or an edit of an
	// existing picture: "a picture-perfect itinerary", "an image smaller".
	imageNounEdits = wordSet(
		"perfect", "smaller", "bigger", "larger", "wider", "narrower", "taller",
		"shorter", "brighter", "darker", "sharper", "clearer", "lighter",
		"sizes", "resolution", "quality", "transparent", "black", "grayscale",
		"kleiner", "größer", "groesser", "breiter", "schärfer", "schaerfer",
		"heller", "dunkler", "transparenter",
	)
	// Nouns after a sentence-initial draw/paint/sketch that make it a noun
	// compound, not an order: "Paint thinner", "Sketch comedy".
	artVerbNounCompounds = wordSet(
		"thinner", "comedy", "comedies", "distance", "colors", "colours",
		"color", "colour", "job", "jobs", "brush", "brushes", "shop", "dries",
		"peels", "chips", "can", "cans", "bucket", "show", "shows", "artist",
		"artists", "pad", "book", "tool", "tools", "call", "calls", "string",
	)
	// Number words and indefinites that open a draw/paint object.
	artObjectOpeners = wordSet(
		"me", "us", "him", "her", "them", "something", "anything", "one", "two",
		"three", "four", "five", "six", "seven", "eight", "nine", "ten", "few",
		"several", "many",
	)
	// Words that may follow a bare draw/paint object: "draw cats in space".
	artBareObjectFollowers = wordSet(
		"in", "on", "with", "at", "under", "over", "and", "playing", "eating",
		"sitting", "flying", "wearing", "riding", "running",
	)
	// "or" makes a '?' a choice question ("Draw a card or pass?").
	choiceWords = wordSet("or", "oder")
	// Subject pronouns after "zeichne" make it a statement or question
	// ("Zeichne ich besser mit Bleistift?"), not an imperative.
	subjectPronounsDE = wordSet("ich", "du", "er", "sie", "es", "wir", "ihr", "man")
)

// Request openers after which the actual verb follows.
var (
	modalOpenersEN = [][]string{
		{"can", "you"}, {"could", "you"}, {"would", "you"}, {"will", "you"},
		{"can", "u"}, {"could", "u"}, {"i", "want", "you", "to"},
		{"i", "d", "like", "you", "to"}, {"i", "would", "like", "you", "to"},
		{"i", "need", "you", "to"}, {"go", "ahead", "and"},
	}
	modalOpenersDE = [][]string{
		{"kannst", "du"}, {"könntest", "du"}, {"koenntest", "du"},
		{"würdest", "du"}, {"wuerdest", "du"}, {"magst", "du"},
	}
	// Openers that already contain the search verb ("I want to see ...").
	searchOpeners = [][]string{
		{"i", "want", "to", "see"}, {"i", "d", "like", "to", "see"},
		{"i", "would", "like", "to", "see"}, {"let", "me", "see"},
		{"can", "i", "see"}, {"could", "i", "see"},
	}
)

// firstContentIndex skips leading filler words such as "Hermes" or "please".
func firstContentIndex(tokens []string) int {
	return skipFillers(tokens, 0)
}

func skipFillers(tokens []string, i int) int {
	for i < len(tokens) && leadingFillers[tokens[i]] {
		i++
	}
	return i
}

// requestStart returns the index of the word that should be the request's
// verb: after fillers and a "can you" / "kannst du" opener. german reports a
// German modal opener (whose verb comes last: "kannst du ... malen").
func requestStart(tokens []string) (start int, german bool) {
	i := firstContentIndex(tokens)
	for _, m := range modalOpenersEN {
		if phraseAt(tokens, i, m) {
			return skipFillers(tokens, i+len(m)), false
		}
	}
	for _, m := range modalOpenersDE {
		if phraseAt(tokens, i, m) {
			j := i + len(m)
			for j < len(tokens) && (leadingFillers[tokens[j]] || tokens[j] == "mal") {
				j++
			}
			return j, true
		}
	}
	return i, false
}

// lastContentIndex skips trailing fillers ("..., please").
func lastContentIndex(tokens []string) int {
	i := len(tokens) - 1
	for i >= 0 && leadingFillers[tokens[i]] {
		i--
	}
	return i
}

func hasAnyWord(tokens []string, set map[string]bool) bool {
	for _, t := range tokens {
		if set[t] {
			return true
		}
	}
	return false
}

func hasQuestionWord(tokens []string) bool { return hasAnyWord(tokens, questionWords) }

// englishArtVerbAt reports whether tokens[i] (draw/paint/sketch) is a request
// for the assistant to make a picture. question reports a trailing '?'.
func englishArtVerbAt(tokens []string, i, first int, question bool) bool {
	if hasQuestionWord(tokens[:i]) {
		return false
	}
	requested := i > 0 && artVerbRequestPrev[tokens[i-1]]
	if i != first && !requested {
		return false
	}
	// A sentence-initial draw/paint/sketch is also a noun ("Paint dries
	// faster", "Sketch comedy is underrated", "Draw distance in games"). With
	// no request word before it, the object must start with an article,
	// pronoun or number ("draw a cat", "paint me ...", "draw two cats") or be
	// a bare noun ending the order or followed by a preposition ("draw cats",
	// "draw dragons please", "draw cats in space").
	if !requested && i+1 < len(tokens) {
		next := tokens[i+1]
		if artVerbNounCompounds[next] || (question && hasAnyWord(tokens, choiceWords)) {
			return false
		}
		_, err := strconv.Atoi(next)
		opens := err == nil || articles[next] || artObjectOpeners[next]
		bare := i+1 == lastContentIndex(tokens) || (i+2 < len(tokens) && artBareObjectFollowers[tokens[i+2]])
		if !opens && (question || !bare) {
			return false
		}
	}
	// Look at the object, skipping articles and "me"/"us".
	for j := i + 1; j < len(tokens); j++ {
		t := tokens[j]
		if articles[t] || t == "me" || t == "us" {
			continue
		}
		return !artVerbIdiomObjects[t]
	}
	return i == first // a bare "draw!" still counts
}

// genericVerbTakesImage reports whether the generic verb at tokens[i] has an
// image noun as its direct object within a few words ("make me a cool
// picture of ..."), excluding technical compounds and "sich ein Bild machen".
func genericVerbTakesImage(tokens []string, i int) bool {
	for j := i + 1; j < len(tokens) && j <= i+4; j++ {
		t := tokens[j]
		if reflexiveDE[t] {
			return false
		}
		if imageGenNouns[t] {
			// Not a technical compound, "a picture-perfect trip" or an edit
			// such as "make an image smaller".
			next := j + 1
			return next > lastContentIndex(tokens) || (!imageTechModifiers[tokens[next]] && !imageNounEdits[tokens[next]])
		}
	}
	return false
}

// wantsImageGeneration reports a picture creation request. question reports
// that the original text ends with '?'.
func wantsImageGeneration(tokens []string, question bool) bool {
	if hasAnyPhrase(tokens, []string{"concept art", "konzeptkunst", "konzept art", "pixel art", "digital art"}) {
		return true
	}
	if hasAnyWord(tokens, imageTechWords) {
		return false
	}
	first := firstContentIndex(tokens)
	start, german := requestStart(tokens)
	if start >= len(tokens) {
		return false
	}

	// Leading imperative: "make an image of ...", "male einen Hund".
	switch v := tokens[start]; {
	case imageGenGenericVerbs[v]:
		if genericVerbTakesImage(tokens, start) {
			return true
		}
	case v == "male":
		if start+1 < len(tokens) && germanObjectStarters[tokens[start+1]] {
			return true
		}
	}

	// German modal with the verb last: "Kannst du einen Hund malen?",
	// "Kannst du mir ein Bild von einer Katze erstellen?". Not "Kannst du mir
	// Tipps zum Malen geben?" (the verb is "geben").
	if german {
		last := lastContentIndex(tokens)
		if last > start && !hasQuestionWord(tokens[start:last]) && !hasAnyWord(tokens[start:last], reflexiveDE) {
			prev := tokens[last-1]
			if imageArtInfinitiveDE[tokens[last]] && prev != "zum" && prev != "beim" && prev != "im" {
				return true
			}
			if imageGenInfinitiveDE[tokens[last]] && hasAnyWord(tokens[start:last], imageGenNouns) {
				return true
			}
		}
	}

	// draw / paint / sketch and zeichne / skizziere inside a request.
	for i, t := range tokens {
		switch {
		case imageArtVerbsEN[t]:
			if englishArtVerbAt(tokens, i, first, question) {
				return true
			}
		case imageArtImperativeDE[t]:
			// "Zeichne einen Drachen", not "ich zeichne gern" or "Zeichne
			// ich besser mit Bleistift oder Kohle?".
			if (i > 0 && tokens[i-1] == "ich") || hasQuestionWord(tokens[:i]) {
				continue
			}
			if i+1 < len(tokens) && subjectPronounsDE[tokens[i+1]] {
				continue
			}
			if question && hasAnyWord(tokens, choiceWords) {
				continue
			}
			return true
		}
	}
	return false
}

// shortSearchStop excludes sentences from the "cat pictures" shortcut.
var shortSearchStop = wordSet("i", "ich", "my", "mein", "meine", "is", "ist", "a", "an", "ein", "eine", "the")

func wantsImageSearch(tokens []string) bool {
	first := firstContentIndex(tokens)
	if first >= len(tokens) {
		return false
	}
	content := tokens[first:]

	// "pictures of cats", "Bilder von Katzen" (the subject may contain anything).
	if imageSearchNouns[content[0]] && len(content) > 1 && imageNounLinkers[content[1]] {
		return true
	}
	// A question is never a search order: "What is a picture?", "Where can I
	// find my photos?", "Who wrote The Picture of Dorian Gray?".
	if hasQuestionWord(content) {
		return false
	}
	// Two-to-four word "cat pictures", "Katzen Bilder".
	if n := len(content); n >= 2 && n <= 4 && imageSearchNouns[content[n-1]] &&
		!hasAnyWord(content[:n-1], imageSearchNouns) && !hasAnyWord(content, shortSearchStop) &&
		!imageSearchVerbs[content[0]] {
		return true
	}

	start, german := requestStart(tokens)
	verbEnd := -1 // index just past the search verb
	for _, o := range searchOpeners {
		if phraseAt(tokens, first, o) {
			verbEnd = first + len(o)
			break
		}
	}
	if verbEnd < 0 && start < len(tokens) {
		if imageSearchVerbs[tokens[start]] {
			verbEnd = start + 1
		} else if tokens[start] == "mir" && start+1 < len(tokens) && imageSearchVerbs[tokens[start+1]] {
			verbEnd = start + 2
		}
	}
	if verbEnd >= 0 {
		j := verbEnd
		for j < len(tokens) && searchVerbParticles[tokens[j]] {
			j++
		}
		if j < len(tokens) && imageSearchNouns[tokens[j]] {
			// "show me a photo of a red car", "zeig mir Fotos" — but not
			// "find a picture-perfect vacation spot".
			return j+1 >= len(tokens) || imageNounLinkers[tokens[j+1]]
		}
		// "find cat pictures", "show me some cute dog photos".
		last := lastContentIndex(tokens)
		return last >= j && imageSearchNouns[tokens[last]] && last-j <= 3
	}

	// "Kannst du mir Bilder von Katzen zeigen?"
	if german {
		last := lastContentIndex(tokens)
		if last > start && imageSearchInfinitiveDE[tokens[last]] {
			for j := start; j < last; j++ {
				if imageSearchNouns[tokens[j]] {
					return j+1 == last || imageNounLinkers[tokens[j+1]]
				}
				if !searchVerbParticles[tokens[j]] {
					return false
				}
			}
		}
	}
	return false
}

// classifyImageRequest returns "generate" for picture creation requests,
// "search" for internet picture lookups and "" for everything else.
func classifyImageRequest(q string) string {
	tokens := tokenize(q)
	if wantsImageGeneration(tokens, strings.HasSuffix(strings.TrimSpace(q), "?")) {
		return "generate"
	}
	if wantsImageSearch(tokens) {
		return "search"
	}
	return ""
}

// imageSubjectDrop are words that never belong in a picture search query:
// articles, image nouns, request verbs and modals.
var imageSubjectDrop = wordSet(
	"a", "an", "the", "of", "for", "me", "us", "picture", "pictures", "photo",
	"photos", "image", "images", "pic", "pics", "some", "any", "please",
	"photograph", "photographs", "wallpaper", "wallpapers", "few", "couple",
	"ein", "eine", "einen", "einem", "einer", "eines", "einige", "paar", "der",
	"die", "das", "den", "dem", "des", "mir", "uns", "bitte", "mal", "etwas",
	"von", "vom", "bild", "bilder", "bildern", "foto", "fotos", "fotografie",
	"fotografien", "abbildung", "abbildungen", "hermes",
	"can", "could", "would", "will", "you", "u", "kannst", "könntest",
	"koenntest", "würdest", "wuerdest", "magst", "du",
	"find", "search", "show", "look", "fetch", "google", "up", "see", "get",
	"zeig", "zeige", "zeigen", "such", "suche", "suchen", "finde", "finden",
	"hol", "hole", "gib", "nach", "raus", "heraus", "raussuchen", "heraussuchen",
)

// subjectFunctionWords alone do not make a searchable subject.
var subjectFunctionWords = wordSet(
	"i", "ich", "my", "mein", "meine", "meinen", "your", "dein", "is", "ist",
	"it", "es", "and", "und", "or", "oder", "in", "on", "with", "mit", "to",
	"zu", "at", "an", "am", "im", "this", "that", "dies", "these", "those",
	"be", "sein", "have", "habe", "hast", "do", "does", "want", "like", "let",
	"d", "need",
)

// extractImageSubject turns "show me a photo of a red car" into "red car"
// and "Kannst du mir Bilder von Katzen zeigen?" into "katzen". It returns ""
// when nothing searchable is left, so the caller can ask for a subject.
func extractImageSubject(q string) string {
	tokens := tokenize(q)
	var rest []string
	found := false
	for i, t := range tokens {
		if imageSearchNouns[t] && i+1 < len(tokens) && imageNounLinkers[tokens[i+1]] {
			rest, found = tokens[i+2:], true
			break
		}
	}
	if !found {
		start, _ := requestStart(tokens)
		rest = tokens[start:]
		for _, o := range searchOpeners {
			if phraseAt(rest, 0, o) {
				rest = rest[len(o):]
				break
			}
		}
	}
	var kept []string
	for _, t := range rest {
		if imageSubjectDrop[t] || questionWords[t] || leadingFillers[t] {
			continue
		}
		kept = append(kept, t)
	}
	for _, t := range kept {
		if !subjectFunctionWords[t] {
			return strings.Join(kept, " ")
		}
	}
	return ""
}
