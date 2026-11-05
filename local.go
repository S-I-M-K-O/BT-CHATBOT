package main

import "sort"

// Small-talk categories, most specific first: when a message mixes several
// ("hi, how are you?") the first one found in this order answers.
const (
	smallTalkIdentity = iota
	smallTalkHowAreYou
	smallTalkThanks
	smallTalkBye
	smallTalkGreeting
	smallTalkCount
)

var smallTalkPhrases = [smallTalkCount][]string{
	smallTalkIdentity: {
		"who are you", "what are you", "what is your name", "what s your name",
		"whats your name", "tell me your name", "your name", "introduce yourself",
		"wer bist du", "wie heißt du", "wie heisst du", "was ist dein name",
		"dein name", "was bist du", "stell dich vor", "stell dich mal vor",
	},
	smallTalkHowAreYou: {
		"how are you", "how are you doing", "how s it going", "how is it going",
		"how do you do", "wie geht es dir", "wie geht s", "wie gehts",
		"wie geht es ihnen", "alles gut bei dir", "alles klar bei dir",
	},
	smallTalkThanks: {
		"thanks", "thank you", "thx", "cheers", "danke", "dankeschön",
		"vielen dank", "danke schön", "danke schoen", "merci",
	},
	smallTalkBye: {
		"bye", "goodbye", "good bye", "good night", "see you", "see you later",
		"see you soon", "see ya", "farewell", "tschüss", "tschüs", "tschuess",
		"ciao", "auf wiedersehen", "bis später", "bis spaeter", "bis bald",
		"gute nacht", "bis dann",
	},
	smallTalkGreeting: {
		"hi", "hello", "hey", "howdy", "greetings", "yo", "hiya",
		"good morning", "good afternoon", "good evening",
		"hallo", "servus", "moin", "moin moin", "grüß gott", "gruess gott",
		"grüezi", "guten morgen", "guten tag", "guten abend",
	},
}

// smallTalkFillers may surround a courtesy phrase without turning the message
// into a real request ("thank you so much, Hermes").
var smallTalkFillers = wordSet(
	"hermes", "pilot", "bt", "so", "much", "very", "a", "lot", "again", "too",
	"there", "buddy", "mate", "friend", "all", "everyone", "and", "oh", "well",
	"ok", "okay", "then", "later", "today", "sehr", "vielmals", "auch", "und",
	"dann", "nochmal", "lieber", "liebe", "mein", "freund", "schön", "schoen",
	"ja", "yes", "yeah", "hey",
)

type smallTalkPhrase struct {
	words []string
	cat   int
}

// smallTalkIndex lists every phrase longest first so "see you later" wins
// over "see you".
var smallTalkIndex = func() []smallTalkPhrase {
	var out []smallTalkPhrase
	for cat, list := range smallTalkPhrases {
		for _, p := range list {
			out = append(out, smallTalkPhrase{tokenize(p), cat})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].words) > len(out[j].words) })
	return out
}()

// smallTalkCategory returns the category of a message made only of courtesy
// phrases and fillers, or -1 when anything else is in it ("Thanks, what time
// is it?", "hi weather", "I can see you", "How are you so smart?").
func smallTalkCategory(tokens []string) int {
	var found [smallTalkCount]bool
	hit := false
	for i := 0; i < len(tokens); {
		matched := false
		for _, p := range smallTalkIndex {
			if phraseAt(tokens, i, p.words) {
				found[p.cat] = true
				hit = true
				i += len(p.words)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if !smallTalkFillers[tokens[i]] {
			return -1
		}
		i++
	}
	if !hit {
		return -1
	}
	for cat, ok := range found {
		if ok {
			return cat
		}
	}
	return -1
}

// localAnswer returns a canned HERMES reply when the whole message is small
// talk the assistant knows without asking a model, or "" when the model should
// answer. lang selects the reply language ("de" for German, everything else
// English). A courtesy phrase followed by a real request ("thanks, and the
// news?") always reaches the model.
func localAnswer(q, lang string) string {
	tokens := tokenize(q)
	if len(tokens) == 0 || wantsRealtime(q) || classifyImageRequest(q) != "" {
		return ""
	}
	pick := func(en, ger string) string {
		if lang == "de" {
			return ger
		}
		return en
	}
	switch smallTalkCategory(tokens) {
	case smallTalkIdentity:
		return pick("I am HERMES, your voice assistant. Protocol three: protect the user.",
			"Ich bin HERMES, dein Sprachassistent. Protokoll drei: den Nutzer beschützen.")
	case smallTalkHowAreYou:
		return pick("All systems nominal. How are you?",
			"Alle Systeme nominal. Wie geht es dir?")
	case smallTalkThanks:
		return pick("You are welcome.", "Gern geschehen.")
	case smallTalkBye:
		return pick("Goodbye. I will be here when you return.",
			"Auf Wiedersehen. Ich bin hier, wenn du zurückkehrst.")
	case smallTalkGreeting:
		return pick("Hello. HERMES online. How can I assist?",
			"Hallo. HERMES ist online. Wie kann ich helfen?")
	}
	return ""
}
