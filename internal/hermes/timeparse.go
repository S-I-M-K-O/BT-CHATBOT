package hermes

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// timeparse.go reads spoken German and English time expressions in Go
// (SPEC Section 9 "Shared helper"): the router copies the user's words into
// "when_text" and never computes dates itself, because LLM-computed times
// were wrong in plausible-looking ways.
//
// The text is normalised into an ASCII-friendly copy (lower case, umlauts
// transliterated, punctuation to spaces) with a byte map back into the
// original, so "\b" works on "übermorgen" and the matched spans can be cut
// out of the original text to leave the reminder text (Rest).
//
// Rules (Section 9):
//   - "in 20 Minuten", "in einer halben Stunde", "in 2 hours", "in einer
//     Woche": relative to now (Duration set);
//   - an hour 1–12 without a qualifier (morgens, abends, Uhr in 24 h form,
//     AM/PM, "in the evening", a leading zero) is the earliest future of h
//     and h+12 today ("um 7" at 14:30 is 19:00, at 06:00 it is 07:00);
//   - on a later day an unqualified 1–5 means the afternoon ("am Freitag um
//     3" is 15:00); when the sentence is about waking up ("weck mich",
//     "Wecker") an unqualified hour is always the morning one;
//   - a day without a clock is 09:00 (DefaultedClock), a day part without a
//     clock its usual hour ("übermorgen früh" 08:00, "heute Abend" 19:00);
//   - results lie after now and less than 366 days ahead.

type WhenResult struct {
	At             time.Time     // absolute, in loc
	HasClock       bool          // the text named a clock time
	HasDay         bool          // the text named a day
	DefaultedClock bool          // day without clock: 09:00 (or the day part's hour) was assumed
	Duration       time.Duration // for "in 20 Minuten"/timers (0 if absolute)
	Rest           string        // input with the time expression cut out, tidied
}

// whenMaxAhead bounds every parsed time (Section 9: < now + 366 days).
const whenMaxAhead = 366 * 24 * time.Hour

// whenMaxInput caps the text the parser looks at; utterances are short.
const whenMaxInput = 1000

// ---------------------------------------------------------------------------
// Normalisation with a map back into the original text

type whenSpan struct{ a, b int }

type whenScan struct {
	orig string
	s    []byte // normalised text; matched parts are blanked with spaces
	idx  []int  // idx[i] = byte offset in orig of normalised byte i; len(s)+1 entries
	cut  []whenSpan
}

// newWhenScan lower-cases text, transliterates umlauts (ä->ae keeps the
// UTF-8 byte length, but the map does not rely on that), turns punctuation
// that never belongs to a time into spaces and collapses whitespace.
func newWhenScan(text string) *whenScan {
	text = truncateRunes(strings.ToValidUTF8(text, ""), whenMaxInput)
	w := &whenScan{orig: text}
	emit := func(out string, o int) {
		for i := 0; i < len(out); i++ {
			if out[i] == ' ' && (len(w.s) == 0 || w.s[len(w.s)-1] == ' ') {
				continue
			}
			w.s = append(w.s, out[i])
			w.idx = append(w.idx, o)
		}
	}
	for o, r := range text {
		var out string
		if r == ',' && o > 0 && o+1 < len(text) && isASCIIDigit(text[o-1]) && isASCIIDigit(text[o+1]) {
			emit(",", o) // "1,5 Stunden"
			continue
		}
		switch lr := unicode.ToLower(r); lr {
		case 'ä':
			out = "ae"
		case 'ö':
			out = "oe"
		case 'ü':
			out = "ue"
		case 'ß':
			out = "ss"
		case ',', ';', '!', '?', '"', '„', '“', '”', '«', '»', '‹', '›', '(', ')', '[', ']', '{', '}', '‚', '-', '–', '—', '/', '…':
			out = " "
		case '’', '‘', '`', '´':
			out = "'"
		default:
			if unicode.IsSpace(lr) || unicode.IsControl(lr) {
				out = " "
			} else {
				out = string(lr)
			}
		}
		emit(out, o)
	}
	w.idx = append(w.idx, len(text))
	return w
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

// take records [a,b) as part of the time expression and blanks it so later
// patterns cannot match it again.
func (w *whenScan) take(a, b int) {
	if a < 0 || b <= a {
		return
	}
	w.cut = append(w.cut, whenSpan{a, b})
	for i := a; i < b; i++ {
		w.s[i] = ' '
	}
}

// blank hides [a,b) from later patterns without cutting it from Rest
// (greetings such as "guten Morgen").
func (w *whenScan) blank(a, b int) {
	for i := a; i < b && i < len(w.s); i++ {
		w.s[i] = ' '
	}
}

// group returns submatch i of m in the current text ("" when absent).
func (w *whenScan) group(m []int, i int) string {
	if 2*i+1 >= len(m) || m[2*i] < 0 {
		return ""
	}
	return string(w.s[m[2*i]:m[2*i+1]])
}

// first returns the first match of re that ok accepts (nil ok accepts all).
func (w *whenScan) first(re *regexp.Regexp, ok func(m []int) bool) []int {
	for _, m := range re.FindAllSubmatchIndex(w.s, -1) {
		if ok == nil || ok(m) {
			return m
		}
	}
	return nil
}

// wordBefore and wordAfter return the neighbouring normalised word.
func (w *whenScan) wordBefore(pos int) string {
	i := pos
	for i > 0 && w.s[i-1] == ' ' {
		i--
	}
	j := i
	for j > 0 && w.s[j-1] != ' ' {
		j--
	}
	return string(w.s[j:i])
}

func (w *whenScan) wordAfter(pos int) string {
	i := pos
	for i < len(w.s) && w.s[i] == ' ' {
		i++
	}
	j := i
	for j < len(w.s) && w.s[j] != ' ' {
		j++
	}
	return strings.TrimRight(string(w.s[i:j]), ".:")
}

// rest returns the original text without the cut spans, tidied: single
// spaces, no dangling punctuation at either end.
func (w *whenScan) rest() string {
	if len(w.cut) == 0 {
		return tidyRest(w.orig)
	}
	spans := append([]whenSpan(nil), w.cut...)
	sort.Slice(spans, func(i, j int) bool { return spans[i].a < spans[j].a })
	var b strings.Builder
	pos := 0
	for _, sp := range spans {
		a, e := w.idx[sp.a], w.idx[sp.b]
		if a > pos {
			b.WriteString(w.orig[pos:a])
		}
		b.WriteString(" ")
		if e > pos {
			pos = e
		}
	}
	if pos < len(w.orig) {
		b.WriteString(w.orig[pos:])
	}
	return tidyRest(b.String())
}

var (
	reRestSpaces     = regexp.MustCompile(`\s+`)
	reRestSpacePunct = regexp.MustCompile(`\s+([,.;:!?])`)
	reRestDupComma   = regexp.MustCompile(`([,;])(\s*[,;])+`)
)

// tidyLabel keeps a matched phrase as said ("am 24.12."), on one line.
func tidyLabel(s string) string {
	return strings.Trim(reRestSpaces.ReplaceAllString(s, " "), " ,;:")
}

func tidyRest(s string) string {
	s = reRestSpaces.ReplaceAllString(s, " ")
	s = reRestSpacePunct.ReplaceAllString(s, "$1")
	s = reRestDupComma.ReplaceAllString(s, "$1")
	return strings.Trim(s, " ,;:.!?-–—")
}

// ---------------------------------------------------------------------------
// Number words

var (
	deOnes  = []string{"", "ein", "zwei", "drei", "vier", "fuenf", "sechs", "sieben", "acht", "neun"}
	deTeens = map[string]int{"zehn": 10, "elf": 11, "zwoelf": 12, "dreizehn": 13, "vierzehn": 14, "fuenfzehn": 15,
		"sechzehn": 16, "siebzehn": 17, "achtzehn": 18, "neunzehn": 19}
	deTens = map[string]int{"zwanzig": 20, "dreissig": 30, "vierzig": 40, "fuenfzig": 50, "sechzig": 60,
		"siebzig": 70, "achtzig": 80, "neunzig": 90}
	enOnes  = []string{"", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}
	enTeens = map[string]int{"ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15,
		"sixteen": 16, "seventeen": 17, "eighteen": 18, "nineteen": 19}
	enTens = map[string]int{"twenty": 20, "thirty": 30, "forty": 40, "fifty": 50, "sixty": 60, "seventy": 70,
		"eighty": 80, "ninety": 90}
)

// whenNumbers maps number words 0..99 (DE+EN, transliterated, "twenty one"
// spaced because hyphens became spaces) to their values. Articles count as
// one only in durations (whenArticles).
var whenNumbers = buildWhenNumbers()

var whenArticles = map[string]float64{"a": 1, "an": 1, "ein": 1, "eine": 1, "einer": 1, "einem": 1, "einen": 1, "ner": 1, "nem": 1}

// whenFractions are "anderthalb" and friends (durations only).
var whenFractions = map[string]float64{"anderthalb": 1.5, "eineinhalb": 1.5, "einundeinhalb": 1.5, "zweieinhalb": 2.5,
	"dreieinhalb": 3.5, "viereinhalb": 4.5, "fuenfeinhalb": 5.5, "one and a half": 1.5, "two and a half": 2.5,
	"three and a half": 3.5}

func buildWhenNumbers() map[string]float64 {
	m := map[string]float64{"null": 0, "zero": 0, "eins": 1, "zwo": 2}
	for i, w := range deOnes {
		if i > 0 {
			m[w] = float64(i)
		}
	}
	for i, w := range enOnes {
		if i > 0 {
			m[w] = float64(i)
		}
	}
	for w, v := range deTeens {
		m[w] = float64(v)
	}
	for w, v := range enTeens {
		m[w] = float64(v)
	}
	for w, v := range deTens {
		m[w] = float64(v)
		for i, o := range deOnes {
			if i > 0 {
				m[o+"und"+w] = float64(v + i)
			}
		}
	}
	for w, v := range enTens {
		m[w] = float64(v)
		for i, o := range enOnes {
			if i > 0 {
				m[w+" "+o] = float64(v + i)
			}
		}
	}
	return m
}

// altOf builds a regexp alternation, longest first, with spaces as \s+.
func altOf(sets ...map[string]float64) string {
	var words []string
	for _, set := range sets {
		for w := range set {
			words = append(words, w)
		}
	}
	sort.Slice(words, func(i, j int) bool {
		if len(words[i]) != len(words[j]) {
			return len(words[i]) > len(words[j])
		}
		return words[i] < words[j]
	})
	for i, w := range words {
		words[i] = strings.ReplaceAll(regexp.QuoteMeta(w), " ", `\s+`)
	}
	return strings.Join(words, "|")
}

// hourWords are number words usable as a clock hour (0..24, no articles).
var hourWords = func() map[string]float64 {
	m := map[string]float64{}
	for w, v := range whenNumbers {
		if v <= 24 {
			m[w] = v
		}
	}
	return m
}()

// minuteWords are number words usable as minutes (no articles).
var minuteWords = func() map[string]float64 {
	m := map[string]float64{}
	for w, v := range whenNumbers {
		if v <= 59 {
			m[w] = v
		}
	}
	return m
}()

// whenNumber reads a digit string ("1,5" too) or a number word.
func whenNumber(s string) (float64, bool) {
	s = strings.Join(strings.Fields(s), " ")
	if v, ok := whenFractions[s]; ok {
		return v, true
	}
	if v, ok := whenNumbers[s]; ok {
		return v, true
	}
	if v, ok := whenArticles[s]; ok {
		return v, true
	}
	f, err := strconv.ParseFloat(strings.Replace(s, ",", ".", 1), 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return f, true
}

// ---------------------------------------------------------------------------
// Patterns (on the normalised text)

const (
	whenUnitAlt  = `sekunden|sekunde|seconds|second|secs|sec|sek|minuten|minutes|minute|mins|min|stunden|stunde|hours|hour|hrs|hr|std|tagen|tage|tag|days|day|wochen|woche|weeks|week|monaten|monate|monat|months|month`
	whenMonthAlt = `januar|jaenner|january|februar|february|maerz|march|april|mai|may|juni|june|juli|july|august|september|oktober|october|november|dezember|december|jan|feb|mrz|mar|apr|jun|jul|aug|sept|sep|okt|oct|nov|dez|dec`
	whenWdayAlt  = `montag|dienstag|mittwoch|donnerstag|freitag|samstag|sonnabend|sonntag|monday|tuesday|wednesday|thursday|friday|saturday|sunday`
	whenPartAlt  = `fruehmorgens|morgens|vormittags|vormittag|nachmittags|nachmittag|mittags|mittag|abends|abend|nachts|nacht|frueh|morning|afternoon|evening|night|noon|midday|tonight|tonite`
)

var (
	durNumAlt = `\d+(?:[.,]\d+)?|` + altOf(whenFractions, whenNumbers, whenArticles)
	hourAlt   = `\d{1,2}|` + altOf(hourWords)
	minuteAlt = `\d{1,2}|` + altOf(minuteWords)

	// One duration part: "20 Minuten", "einer halben Stunde", "half an hour".
	durPart = `(?:(?:` + durNumAlt + `)\s*(?:` + whenUnitAlt + `)\b` +
		`|(?:(?:einer|eine|ner|1)\s+)?halben?\s*stunde\b` +
		`|(?:(?:einer|eine|ner|1)\s+)?(?:drei)?viertel\s*stunde\b` +
		`|(?:a\s+)?half\s+(?:an\s+)?hour\b` +
		`|(?:a\s+)?quarter\s+(?:of\s+an\s+)?hour\b)`
	durExpr = durPart + `(?:\s*(?:und|and)?\s*` + durPart + `)*(?:\s+(?:and\s+a\s+half|und\s+(?:eine\s+)?halbe?)\b)?`

	reDurPart = regexp.MustCompile(`(?:(` + durNumAlt + `)\s*(` + whenUnitAlt + `)\b` +
		`|((?:(?:einer|eine|ner|1)\s+)?halben?\s*stunde)\b` +
		`|((?:(?:einer|eine|ner|1)\s+)?(?:drei)?viertel\s*stunde)\b` +
		`|((?:a\s+)?half\s+(?:an\s+)?hour)\b` +
		`|((?:a\s+)?quarter\s+(?:of\s+an\s+)?hour)\b` +
		`|(and\s+a\s+half|und\s+(?:eine\s+)?halbe?)\b)`)

	// "in 20 Minuten", "nach einer Stunde", "in the next 10 minutes".
	reDurRel = regexp.MustCompile(`\b(?:in|nach|after|within|innerhalb\s+von|binnen)\s+(?:(?:den\s+)?naechsten\s+|the\s+next\s+)?(?:` + durExpr + `)`)
	// "für 10 Minuten", "Timer auf 5 Minuten", "10 Minuten" (timers, slot answers).
	reDurBare = regexp.MustCompile(`\b(?:(?:fuer|for|auf|von|ueber|of)\s+)?(?:` + durExpr + `)`)

	reTimerWord = regexp.MustCompile(`\b(?:timer|countdown|stoppuhr|wecker|alarm|kurzzeitwecker|eieruhr)\b`)
	reWakeWord  = regexp.MustCompile(`\b(?:weck|wecke|wecken|wecker|weckt|wake|alarm|aufwachen|aufstehen|wakeup)\b`)

	// Greetings are not times ("Guten Morgen", "gute Nacht").
	reGreeting = regexp.MustCompile(`\b(?:guten\s+morgen|gute\s+nacht|guten\s+abend|guten\s+tag|good\s+morning|good\s+night|good\s+evening|good\s+afternoon|good\s+day)\b`)

	reDateNum = regexp.MustCompile(`\b(?:(?:am|den|zum|bis\s+zum|ab\s+dem|on|the)\s+)?(\d{1,2})\.(\d{1,2})\.(?:\s?(\d{4})\b|(\d{2})\b)?`)
	reDateAm  = regexp.MustCompile(`\b(?:am|den|zum)\s+(\d{1,2})\.(\d{1,2})\b`)
	// A day of the month alone: "am 24.", "on the 24th", "on the 1st".
	reDateDay = regexp.MustCompile(`\b(?:am|zum|on\s+the|on)\s+(\d{1,2})(?:\.|st|nd|rd|th)(?:\s|$)`)
	reDateDE  = regexp.MustCompile(`\b(?:(?:am|den|zum|bis\s+zum|ab\s+dem|on|the)\s+)*(\d{1,2})(?:st|nd|rd|th|\.)?\s*(?:of\s+)?(` + whenMonthAlt + `)\b\.?(?:\s*(\d{4})\b)?`)
	reDateEN  = regexp.MustCompile(`\b(?:(?:on|the)\s+)*(` + whenMonthAlt + `)\.?\s+(?:the\s+)?(\d{1,2})(?:st|nd|rd|th)?\b(?:\s*(\d{4})\b)?`)

	reWeekday  = regexp.MustCompile(`\b(?:(?:am|on|fuer|for|bis|ab|until|by)\s+)?(?:(naechsten|naechster|naechste|naechstes|kommenden|kommender|kommende|uebernaechsten|uebernaechste|next|this|coming|diesen|dieser|diese)\s+)?(?:(?:am|on)\s+)?(` + whenWdayAlt + `)(morgen|frueh|vormittag|mittag|nachmittag|abend|nacht)?\b(?:\s+(naechster\s+woche|next\s+week|in\s+einer\s+woche))?`)
	reNextWeek = regexp.MustCompile(`\b(?:(?:in\s+der|in\s+the)\s+)?(?:(uebernaechste[nr]?|naechste[nr]?|kommende[nr]?|next|diese[r]?|this)\s+(?:woche|week)|week\s+after\s+next)\b`)
	reWeekend  = regexp.MustCompile(`\b(?:(?:am|an\s+diesem|an\s+dem|dieses|this|on\s+the|naechstes|kommendes|next|uebers|ueber\s+das|over\s+the)\s+)*(?:wochenende|weekend)\b`)

	reDayWord = regexp.MustCompile(`\b(?:(?:am|on|fuer|for|bis|ab|until|by|zum)\s+)?(uebermorgen|(?:the\s+)?day\s+after\s+tomorrow|morgen|tomorrow|heute|today|tonight|tonite)\b`)
	reDayPart = regexp.MustCompile(`\b(?:(am|in\s+the|at|this|zur|des)\s+)?(` + whenPartAlt + `)\b`)
	reAmMorn  = regexp.MustCompile(`\b(?:am|jeden|den)\s+morgen\b`)

	reClockMid  = regexp.MustCompile(`\b(?:(?:um|at|gegen|ab|bis|by|around|until|till)\s+)?(mitternacht|midnight|noon|midday)\b`)
	reClockRel  = regexp.MustCompile(`\b(?:(?:um|gegen|ab|bis)\s+)?(viertel|dreiviertel|zehn|fuenf|zwanzig|fuenfundzwanzig|5|10|20|25)\s+(nach|vor)\s+(halb\s+)?(` + hourAlt + `)\b`)
	reClockHalb = regexp.MustCompile(`\b(?:(?:um|gegen|ab|bis)\s+)?halb\s+(` + hourAlt + `)\b`)
	reClockQtr  = regexp.MustCompile(`\b(?:(?:um|gegen|ab|bis)\s+)?(dreiviertel|viertel)\s+(` + hourAlt + `)\b`)
	reClockEN   = regexp.MustCompile(`\b(?:(?:at|by|around|until|till)\s+)?(?:a\s+)?(quarter|half|ten|five|twenty|twenty\s+five|5|10|20|25)\s+(past|after|to|before|til|till)\s+(` + hourAlt + `)\b`)
	reClockHHMM = regexp.MustCompile(`\b(?:(?:um|at|gegen|ab|bis|by|around|until|till|ca\.?|circa|etwa|about)\s+)*(\d{1,2})[:.](\d{2})\b`)
	reClockUhrM = regexp.MustCompile(`\b(?:(?:um|gegen|ab|bis|ca\.?|circa|etwa)\s+)*(` + hourAlt + `)\s+uhr\s+(` + minuteAlt + `)\b`)
	reClockPrep = regexp.MustCompile(`\b(?:um|at|gegen|ab|bis|by|around|until|till)\s+(?:(?:ca\.?|circa|etwa|about)\s+)?(` + hourAlt + `)`)
	reClockBare = regexp.MustCompile(`\b(` + hourAlt + `)`)

	reMorgenWord = regexp.MustCompile(`\bmorgen\b`)
	reYesterday  = regexp.MustCompile(`\b(?:(?:am|on|fuer|for)\s+)?(gestern|yesterday|vorgestern)\b`)
	reInDays     = regexp.MustCompile(`\bin\s+(` + durNumAlt + `)\s+(?:tagen|days)\b`)

	// reRecurring: repeat words, for the "rings once" note when
	// parseRecurring cannot read them ("jede Stunde", "immer wenn …").
	reRecurring = regexp.MustCompile(`\b(?:(?:jeden|jede|jedes|jedem|alle|every|each)\s+(?:\S+\s+)?(?:` + whenWdayAlt +
		`|tag|tage|woche|wochen|monat|monate|jahr|jahre|stunde|stunden|minute|minuten|day|days|week|weeks|month|months|year|years|hour|hours|minutes)` +
		`|immer\s+wenn|jedes\s+mal\s+wenn|whenever|every\s+time|stuendlich|hourly|taeglich|woechentlich|monatlich|jaehrlich|werktags)\b`)
)

var whenMonths = map[string]time.Month{
	"januar": 1, "jaenner": 1, "january": 1, "jan": 1, "februar": 2, "february": 2, "feb": 2,
	"maerz": 3, "march": 3, "mrz": 3, "mar": 3, "april": 4, "apr": 4, "mai": 5, "may": 5,
	"juni": 6, "june": 6, "jun": 6, "juli": 7, "july": 7, "jul": 7, "august": 8, "aug": 8,
	"september": 9, "sept": 9, "sep": 9, "oktober": 10, "october": 10, "okt": 10, "oct": 10,
	"november": 11, "nov": 11, "dezember": 12, "december": 12, "dez": 12, "dec": 12,
}

var whenWeekdays = map[string]time.Weekday{
	"sonntag": time.Sunday, "sunday": time.Sunday, "montag": time.Monday, "monday": time.Monday,
	"dienstag": time.Tuesday, "tuesday": time.Tuesday, "mittwoch": time.Wednesday, "wednesday": time.Wednesday,
	"donnerstag": time.Thursday, "thursday": time.Thursday, "freitag": time.Friday, "friday": time.Friday,
	"samstag": time.Saturday, "sonnabend": time.Saturday, "saturday": time.Saturday,
}

// Day parts and their default hours when no clock was named.
const (
	partMorning   = "morning"
	partForenoon  = "forenoon"
	partNoon      = "noon"
	partAfternoon = "afternoon"
	partEvening   = "evening"
	partNight     = "night"
)

var partOf = map[string]string{
	"fruehmorgens": partMorning, "morgens": partMorning, "frueh": partMorning, "morning": partMorning, "morgen": partMorning,
	"vormittags": partForenoon, "vormittag": partForenoon,
	"mittags": partNoon, "mittag": partNoon, "noon": partNoon, "midday": partNoon,
	"nachmittags": partAfternoon, "nachmittag": partAfternoon, "afternoon": partAfternoon,
	"abends": partEvening, "abend": partEvening, "evening": partEvening, "tonight": partEvening, "tonite": partEvening,
	"nachts": partNight, "nacht": partNight, "night": partNight,
}

var partHour = map[string]int{partMorning: 8, partForenoon: 10, partNoon: 12, partAfternoon: 15, partEvening: 19, partNight: 22}

// adverbParts may stand alone ("Erinnere mich abends …"); the nouns ("der
// Abend", "zu früh") only count next to a day or a clock time.
var adverbParts = wordSet("fruehmorgens", "morgens", "vormittags", "mittags", "nachmittags", "abends", "nachts",
	"tonight", "tonite", "noon", "midday")

// amNotMeridiem: after these words "am" is German ("um 8 am Freitag").
var amNotMeridiem = func() map[string]bool {
	m := wordSet("morgen", "abend", "nachmittag", "vormittag", "mittag", "wochenende", "ende", "anfang", "naechsten",
		"kommenden", "ersten", "letzten", "tag", "montagmorgen", "liebsten", "besten", "meisten")
	for w := range whenWeekdays {
		m[w] = true
	}
	for w := range whenMonths {
		m[w] = true
	}
	return m
}()

// ---------------------------------------------------------------------------
// parseWhen

// parseWhen finds a future point in time in text relative to now in loc.
func parseWhen(text string, now time.Time, loc *time.Location) (WhenResult, bool) {
	if loc == nil {
		loc = now.Location()
	}
	now = now.In(loc)
	w := newWhenScan(text)
	res, ok := w.parse(now, loc)
	if !ok {
		return WhenResult{Rest: tidyRest(w.orig)}, false
	}
	if !res.At.After(now) || res.At.Sub(now) >= whenMaxAhead {
		return WhenResult{Rest: tidyRest(w.orig)}, false
	}
	res.Rest = w.rest()
	return res, true
}

func midnightOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// relDuration reads a duration match: sub-day parts as a Duration, days,
// weeks and months as calendar steps (DST-safe AddDate).
func relDuration(s string) (sub time.Duration, days, months int, ok bool) {
	var frac float64 // pending "and a half" of the previous unit
	var last time.Duration
	for _, m := range reDurPart.FindAllStringSubmatch(s, -1) {
		switch {
		case m[1] != "":
			n, good := whenNumber(m[1])
			if !good {
				return 0, 0, 0, false
			}
			u := m[2]
			switch {
			case strings.HasPrefix(u, "sek") || strings.HasPrefix(u, "sec"):
				last = time.Second
				sub += time.Duration(n * float64(time.Second))
			case strings.HasPrefix(u, "min"):
				last = time.Minute
				sub += time.Duration(n * float64(time.Minute))
			case strings.HasPrefix(u, "st") || strings.HasPrefix(u, "h"):
				last = time.Hour
				sub += time.Duration(n * float64(time.Hour))
			case strings.HasPrefix(u, "ta") || strings.HasPrefix(u, "day"):
				last = 0
				if n != float64(int(n)) {
					sub += time.Duration(n * 24 * float64(time.Hour))
				} else {
					days += int(n)
				}
			case strings.HasPrefix(u, "wo") || strings.HasPrefix(u, "week"):
				last = 0
				if n != float64(int(n)) {
					sub += time.Duration(n * 7 * 24 * float64(time.Hour))
				} else {
					days += 7 * int(n)
				}
			case strings.HasPrefix(u, "mon"):
				last = 0
				months += int(n)
			}
			ok = true
		case m[3] != "":
			sub += 30 * time.Minute
			ok = true
		case m[4] != "":
			if strings.Contains(m[4], "drei") {
				sub += 45 * time.Minute
			} else {
				sub += 15 * time.Minute
			}
			ok = true
		case m[5] != "":
			sub += 30 * time.Minute
			ok = true
		case m[6] != "":
			sub += 15 * time.Minute
			ok = true
		case m[7] != "":
			if last > 0 {
				frac += float64(last) / 2
			}
		}
	}
	sub += time.Duration(frac)
	if days > 3660 || months > 120 || sub > 100*365*24*time.Hour {
		return 0, 0, 0, false
	}
	return sub, days, months, ok && (sub > 0 || days > 0 || months > 0)
}

type whenDay struct {
	set   bool
	day   time.Time // midnight in loc
	today bool      // the day is today
	part  string    // a day part that came with the day ("Freitagabend", "tonight")
}

func (w *whenScan) parse(now time.Time, loc *time.Location) (WhenResult, bool) {
	today := midnightOf(now)
	wake := reWakeWord.Match(w.s)

	// Greetings never carry a time.
	for _, m := range reGreeting.FindAllIndex(w.s, -1) {
		w.blank(m[0], m[1])
	}

	// 1) Relative durations win ("in 20 Minuten"). Pure day steps ("in 2
	// Tagen") may still get a clock time ("in 2 Tagen um 9 Uhr").
	var d whenDay
	if m := w.first(reDurRel, nil); m != nil {
		sub, days, months, ok := relDuration(string(w.s[m[0]:m[1]]))
		if ok {
			w.take(m[0], m[1])
			if sub > 0 || !w.hasClockAhead() {
				at := now.AddDate(0, months, days).Add(sub)
				return WhenResult{At: at, HasDay: days > 0 || months > 0, Duration: at.Sub(now)}, true
			}
			d = whenDay{set: true, day: today.AddDate(0, months, days)}
		}
	}

	// 2) The day.
	part, partAdverb := "", false
	if !d.set {
		d = w.parseDay(now, today)
	}

	// 3) Day parts ("abends", "heute Abend", "am Morgen").
	if m := w.first(reAmMorn, nil); m != nil {
		part, partAdverb = partMorning, true
		w.take(m[0], m[1])
	}
	if part == "" {
		if m := w.first(reDayPart, nil); m != nil {
			word := w.group(m, 2)
			part = partOf[word]
			prefix := strings.Join(strings.Fields(w.group(m, 1)), " ")
			partAdverb = adverbParts[word] || prefix == "in the" || prefix == "am" || prefix == "this" || (prefix == "at" && word == "night")
			if word == "tonight" || word == "tonite" {
				if !d.set {
					d = whenDay{set: true, day: today, today: true}
				}
			}
			w.take(m[0], m[1])
		}
	}
	if d.part != "" && part == "" {
		part, partAdverb = d.part, true
	}

	// 4) The clock.
	c, hasClock := w.parseClock()

	if !d.set && !hasClock && (part == "" || !partAdverb) {
		// Nothing absolute: a bare duration counts for timers and short
		// answers ("10 Minuten" to "Wann soll ich dich erinnern?").
		return w.bareDuration(now)
	}
	return resolveWhen(now, today, d, part, c, hasClock, wake)
}

// hasClockAhead reports whether a clock time is still in the text (used to
// decide whether "in 2 Tagen" is a duration or a day).
func (w *whenScan) hasClockAhead() bool {
	s := w.s
	for _, re := range []*regexp.Regexp{reClockMid, reClockRel, reClockHalb, reClockEN, reClockHHMM, reClockUhrM, reClockPrep} {
		if re.Match(s) {
			return true
		}
	}
	for _, m := range reClockBare.FindAllSubmatchIndex(s, -1) {
		if suf, _ := w.clockSuffix(m[3]); suf != "" {
			return true
		}
	}
	return reDayPart.Match(s) || reAmMorn.Match(s)
}

func (w *whenScan) bareDuration(now time.Time) (WhenResult, bool) {
	m := w.first(reDurBare, nil)
	if m == nil {
		return WhenResult{}, false
	}
	allowed := reTimerWord.Match(w.s)
	if !allowed {
		// Only a duration and a few fillers: an answer such as "10 Minuten".
		others := 0
		for _, tok := range strings.Fields(string(w.s[:m[0]]) + " " + string(w.s[m[1]:])) {
			if !bareFillers[strings.Trim(tok, ".:")] {
				others++
			}
		}
		allowed = others <= 1
	}
	if !allowed {
		return WhenResult{}, false
	}
	sub, days, months, ok := relDuration(string(w.s[m[0]:m[1]]))
	if !ok {
		return WhenResult{}, false
	}
	w.take(m[0], m[1])
	at := now.AddDate(0, months, days).Add(sub)
	return WhenResult{At: at, HasDay: days > 0 || months > 0, Duration: at.Sub(now)}, true
}

var bareFillers = wordSet("", "bitte", "please", "hermes", "ok", "okay", "ja", "yes", "also", "so", "noch", "mal",
	"ungefaehr", "etwa", "about", "around", "dann", "then", "lang", "long", "lange", "fuer", "for", "in")

func (w *whenScan) parseDay(now, today time.Time) whenDay {
	// Dates first: "am 24.12.", "am 20. November", "on December 1st".
	if m := w.first(reDateNum, func(m []int) bool {
		mm, _ := strconv.Atoi(w.group(m, 2))
		return mm >= 1 && mm <= 12
	}); m != nil {
		dd, _ := strconv.Atoi(w.group(m, 1))
		mm, _ := strconv.Atoi(w.group(m, 2))
		year := 0
		if y := w.group(m, 3); y != "" {
			year, _ = strconv.Atoi(y)
		} else if y := w.group(m, 4); y != "" {
			year, _ = strconv.Atoi(y)
			year += 2000
		}
		if day, ok := calendarDate(now, year, time.Month(mm), dd); ok {
			w.take(m[0], m[1])
			return whenDay{set: true, day: day, today: day.Equal(today)}
		}
	}
	if m := w.first(reDateAm, func(m []int) bool {
		mm, _ := strconv.Atoi(w.group(m, 2))
		return mm >= 1 && mm <= 12
	}); m != nil {
		dd, _ := strconv.Atoi(w.group(m, 1))
		mm, _ := strconv.Atoi(w.group(m, 2))
		if day, ok := calendarDate(now, 0, time.Month(mm), dd); ok {
			w.take(m[0], m[1])
			return whenDay{set: true, day: day, today: day.Equal(today)}
		}
	}
	for _, re := range []*regexp.Regexp{reDateDE, reDateEN} {
		if m := w.first(re, nil); m != nil {
			var dd, year int
			var mon time.Month
			if re == reDateDE {
				dd, _ = strconv.Atoi(w.group(m, 1))
				mon = whenMonths[w.group(m, 2)]
			} else {
				mon = whenMonths[w.group(m, 1)]
				dd, _ = strconv.Atoi(w.group(m, 2))
			}
			if y := w.group(m, 3); y != "" {
				year, _ = strconv.Atoi(y)
			}
			if day, ok := calendarDate(now, year, mon, dd); ok {
				w.take(m[0], m[1])
				return whenDay{set: true, day: day, today: day.Equal(today)}
			}
		}
	}

	// A day of the month without one: the next such day (today counts).
	if m := w.first(reDateDay, nil); m != nil {
		dd, _ := strconv.Atoi(w.group(m, 1))
		if day, ok := nextMonthDay(today, dd); ok {
			w.take(m[0], m[1])
			return whenDay{set: true, day: day, today: day.Equal(today)}
		}
	}

	// Weekdays: "am Freitag", "nächsten Montag", "Freitagabend", "Montag nächster Woche".
	if m := w.first(reWeekday, nil); m != nil {
		wd := whenWeekdays[w.group(m, 2)]
		prefix := w.group(m, 1)
		delta := (int(wd) - int(now.Weekday()) + 7) % 7
		var day time.Time
		switch {
		case w.group(m, 4) != "" || w.nextWeekBefore(m[0]):
			day = mondayOf(today).AddDate(0, 0, 7+(int(wd)+6)%7)
		default:
			if delta == 0 && !(prefix == "this" || strings.HasPrefix(prefix, "dies")) {
				delta = 7
			}
			if strings.HasPrefix(prefix, "ueber") {
				delta += 7
			}
			day = today.AddDate(0, 0, delta)
		}
		part := partOf[w.group(m, 3)]
		w.take(m[0], m[1])
		return whenDay{set: true, day: day, today: day.Equal(today), part: part}
	}

	// Day words: übermorgen, morgen, heute, tomorrow, today, tonight.
	if m := w.first(reDayWord, func(m []int) bool {
		if w.group(m, 1) != "morgen" {
			return true
		}
		prev := w.wordBefore(m[2])
		// "heute Morgen" is this morning, "am/jeden Morgen" a day part.
		return prev != "am" && prev != "jeden" && prev != "den" && prev != "heute" && prev != "diesen" && prev != "einen"
	}); m != nil {
		word := w.group(m, 1)
		day := today
		part := ""
		switch {
		case word == "uebermorgen" || strings.HasSuffix(word, "day after tomorrow"):
			day = today.AddDate(0, 0, 2)
		case word == "morgen" || word == "tomorrow":
			day = today.AddDate(0, 0, 1)
		case word == "tonight" || word == "tonite":
			part = partEvening
		}
		w.take(m[0], m[1])
		// "heute Morgen": the following "morgen" is the morning.
		if (word == "heute" || word == "today") && w.wordAfter(m[1]) == "morgen" {
			if mm := w.first(reMorgenWord, func(x []int) bool { return x[0] >= m[1] }); mm != nil {
				w.take(mm[0], mm[1])
				part = partMorning
			}
		}
		return whenDay{set: true, day: day, today: day.Equal(today), part: part}
	}
	// "nächste Woche" (Monday next week), "am Wochenende" (Saturday).
	if m := w.first(reNextWeek, nil); m != nil {
		which := w.group(m, 1)
		var day time.Time
		switch {
		case which == "" || strings.HasPrefix(which, "ueber"):
			day = mondayOf(today).AddDate(0, 0, 14)
		case strings.HasPrefix(which, "dies") || which == "this":
			day = today
		default:
			day = mondayOf(today).AddDate(0, 0, 7)
		}
		w.take(m[0], m[1])
		return whenDay{set: true, day: day, today: day.Equal(today)}
	}
	if m := w.first(reWeekend, nil); m != nil {
		sat := today.AddDate(0, 0, (int(time.Saturday)-int(now.Weekday())+7)%7)
		if now.Weekday() == time.Sunday {
			sat = today
		}
		phrase := string(w.s[m[0]:m[1]])
		if strings.Contains(phrase, "naechstes") || strings.Contains(phrase, "next") {
			if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
				sat = mondayOf(today).AddDate(0, 0, 12)
			}
		}
		w.take(m[0], m[1])
		return whenDay{set: true, day: sat, today: sat.Equal(today)}
	}
	return whenDay{}
}

// nextWeekBefore reports "nächste Woche Montag": a next-week phrase right
// before the weekday, which it then consumes.
func (w *whenScan) nextWeekBefore(pos int) bool {
	head := string(w.s[:pos])
	loc := reNextWeek.FindAllStringSubmatchIndex(head, -1)
	if len(loc) == 0 {
		return false
	}
	m := loc[len(loc)-1]
	if strings.TrimSpace(head[m[1]:]) != "" {
		return false
	}
	which := head[m[2]:m[3]]
	if which == "" || strings.HasPrefix(which, "dies") || which == "this" || strings.HasPrefix(which, "ueber") {
		return false
	}
	w.take(m[0], m[1])
	return true
}

func mondayOf(day time.Time) time.Time {
	return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
}

// calendarDate builds a calendar day; without a year the next occurrence
// (today counts). Impossible dates ("31.2.") fail.
func calendarDate(now time.Time, year int, mon time.Month, dd int) (time.Time, bool) {
	if mon < 1 || mon > 12 || dd < 1 || dd > 31 {
		return time.Time{}, false
	}
	y := year
	if y == 0 {
		y = now.Year()
	}
	day := time.Date(y, mon, dd, 0, 0, 0, 0, now.Location())
	if day.Day() != dd {
		return time.Time{}, false
	}
	if year == 0 && day.Before(midnightOf(now)) {
		day = time.Date(y+1, mon, dd, 0, 0, 0, 0, now.Location())
		if day.Day() != dd {
			return time.Time{}, false
		}
	}
	return day, true
}

// nextMonthDay is the next day dd of a month from today on, skipping months
// that have no such day ("am 31." in November is December 31).
func nextMonthDay(today time.Time, dd int) (time.Time, bool) {
	if dd < 1 || dd > 31 {
		return time.Time{}, false
	}
	for i := 0; i < 13; i++ {
		first := time.Date(today.Year(), today.Month()+time.Month(i), 1, 0, 0, 0, 0, today.Location())
		day := time.Date(first.Year(), first.Month(), dd, 0, 0, 0, 0, today.Location())
		if day.Month() == first.Month() && !day.Before(today) {
			return day, true
		}
	}
	return time.Time{}, false
}

type whenClock struct {
	h, m     int
	explicit bool // unambiguous 24 h reading (13–23, 0, leading zero, AM/PM)
	midnight bool // "Mitternacht": the end of the day
	noon     bool
}

// clockSuffix reads "Uhr", "o'clock", "am"/"pm" right after pos. "am" is
// German when a day or date follows ("um 8 am Freitag").
func (w *whenScan) clockSuffix(pos int) (kind string, end int) {
	i := pos
	for i < len(w.s) && w.s[i] == ' ' {
		i++
	}
	rest := string(w.s[i:])
	boundary := func(n int) bool {
		return n >= len(rest) || !(rest[n] >= 'a' && rest[n] <= 'z' || rest[n] >= '0' && rest[n] <= '9')
	}
	for _, s := range []struct{ p, k string }{
		{"uhr", "uhr"}, {"o'clock", "oclock"}, {"oclock", "oclock"}, {"o clock", "oclock"},
		{"a.m.", "am"}, {"a.m", "am"}, {"p.m.", "pm"}, {"p.m", "pm"}, {"pm", "pm"}, {"am", "am"},
	} {
		if strings.HasPrefix(rest, s.p) && boundary(len(s.p)) {
			if s.p == "am" {
				next := w.wordAfter(i + 2)
				if amNotMeridiem[next] || (next != "" && next[0] >= '0' && next[0] <= '9') {
					return "", pos
				}
			}
			return s.k, i + len(s.p)
		}
	}
	return "", pos
}

func hourValue(s string) (int, bool) {
	v, ok := whenNumber(s)
	if !ok || v != float64(int(v)) || v < 0 || v > 24 {
		return 0, false
	}
	return int(v), true
}

func (w *whenScan) parseClock() (whenClock, bool) {
	apply := func(c whenClock, m []int, end int) (whenClock, bool) {
		if kind, e := w.clockSuffix(end); kind != "" {
			switch kind {
			case "am":
				if c.h == 12 {
					c.h = 0
				}
				c.explicit = true
			case "pm":
				if c.h < 12 {
					c.h += 12
				}
				c.explicit = true
			}
			end = e
		}
		w.take(m[0], end)
		return c, true
	}

	if m := w.first(reClockMid, nil); m != nil {
		word := w.group(m, 1)
		c := whenClock{h: 12, explicit: true, noon: true}
		if word == "mitternacht" || word == "midnight" {
			c = whenClock{h: 0, explicit: true, midnight: true}
		}
		w.take(m[0], m[1])
		return c, true
	}
	if m := w.first(reClockRel, func(m []int) bool { _, ok := hourValue(w.group(m, 4)); return ok }); m != nil {
		h, _ := hourValue(w.group(m, 4))
		off := map[string]int{"viertel": 15, "dreiviertel": 45, "zehn": 10, "fuenf": 5, "zwanzig": 20,
			"fuenfundzwanzig": 25, "5": 5, "10": 10, "20": 20, "25": 25}[w.group(m, 1)]
		total := h * 60
		if w.group(m, 3) != "" { // "fünf vor halb neun" = 8:25
			total -= 30
		}
		if w.group(m, 2) == "nach" {
			total += off
		} else {
			total -= off
		}
		total = (total + 24*60) % (24 * 60)
		return apply(whenClock{h: total / 60, m: total % 60}, m, m[1])
	}
	if m := w.first(reClockHalb, func(m []int) bool { h, ok := hourValue(w.group(m, 1)); return ok && h >= 1 }); m != nil {
		h, _ := hourValue(w.group(m, 1))
		return apply(whenClock{h: (h + 23) % 24, m: 30}, m, m[1])
	}
	if m := w.first(reClockQtr, func(m []int) bool { h, ok := hourValue(w.group(m, 2)); return ok && h >= 1 }); m != nil {
		h, _ := hourValue(w.group(m, 2))
		mins := 15 // "viertel acht" (southern) = 7:15
		if w.group(m, 1) == "dreiviertel" {
			mins = 45
		}
		return apply(whenClock{h: (h + 23) % 24, m: mins}, m, m[1])
	}
	if m := w.first(reClockEN, func(m []int) bool { _, ok := hourValue(w.group(m, 3)); return ok }); m != nil {
		h, _ := hourValue(w.group(m, 3))
		word := strings.Join(strings.Fields(w.group(m, 1)), " ")
		off := map[string]int{"quarter": 15, "half": 30, "ten": 10, "five": 5, "twenty": 20, "twenty five": 25,
			"5": 5, "10": 10, "20": 20, "25": 25}[word]
		total := h * 60
		switch w.group(m, 2) {
		case "past", "after":
			total += off
		default:
			total -= off
		}
		total = (total + 24*60) % (24 * 60)
		return apply(whenClock{h: total / 60, m: total % 60}, m, m[1])
	}
	if m := w.first(reClockHHMM, func(m []int) bool {
		h, _ := strconv.Atoi(w.group(m, 1))
		mm, _ := strconv.Atoi(w.group(m, 2))
		return h <= 24 && mm <= 59
	}); m != nil {
		hs := w.group(m, 1)
		h, _ := strconv.Atoi(hs)
		mm, _ := strconv.Atoi(w.group(m, 2))
		c := whenClock{h: h, m: mm, explicit: h == 0 || h > 12 || (len(hs) == 2 && hs[0] == '0')}
		return apply(c, m, m[1])
	}
	if m := w.first(reClockUhrM, func(m []int) bool {
		_, okH := hourValue(w.group(m, 1))
		v, okM := whenNumber(w.group(m, 2))
		return okH && okM && v <= 59
	}); m != nil {
		h, _ := hourValue(w.group(m, 1))
		v, _ := whenNumber(w.group(m, 2))
		w.take(m[0], m[1])
		return whenClock{h: h, m: int(v), explicit: h == 0 || h > 12}, true
	}
	// "um 8", "at 9", "um acht Uhr": a preposition and an hour. "ein"/"one"
	// need "Uhr"/"o'clock" ("um ein bisschen", "at one point").
	if m := w.first(reClockPrep, func(m []int) bool {
		g := w.group(m, 1)
		if _, ok := hourValue(g); !ok || w.badHourEnd(m[3]) {
			return false
		}
		if g == "ein" || g == "one" {
			suf, _ := w.clockSuffix(m[3])
			return suf != ""
		}
		return true
	}); m != nil {
		hs := w.group(m, 1)
		h, _ := hourValue(hs)
		return apply(whenClock{h: h, explicit: h == 0 || h > 12 || (len(hs) == 2 && hs[0] == '0')}, m, m[1])
	}
	// "18 Uhr", "7pm", "acht Uhr" without a preposition.
	if m := w.first(reClockBare, func(m []int) bool {
		if _, ok := hourValue(w.group(m, 1)); !ok || w.badHourEnd(m[3]) {
			return false
		}
		suf, _ := w.clockSuffix(m[3])
		return suf != ""
	}); m != nil {
		hs := w.group(m, 1)
		h, _ := hourValue(hs)
		return apply(whenClock{h: h, explicit: h == 0 || h > 12 || (len(hs) == 2 && hs[0] == '0')}, m, m[1])
	}
	return whenClock{}, false
}

// badHourEnd rejects an hour match that runs into more digits or letters
// ("123", "8er").
func (w *whenScan) badHourEnd(end int) bool {
	if end >= len(w.s) {
		return false
	}
	c := w.s[end]
	if c >= '0' && c <= '9' {
		return true
	}
	if c == ':' || c == '.' {
		return end+1 < len(w.s) && w.s[end+1] >= '0' && w.s[end+1] <= '9'
	}
	if c >= 'a' && c <= 'z' {
		// "7pm"/"7am" are fine; any other letter glued on is not an hour.
		rest := string(w.s[end:])
		return !(strings.HasPrefix(rest, "pm") || strings.HasPrefix(rest, "am") || strings.HasPrefix(rest, "a.m") ||
			strings.HasPrefix(rest, "p.m") || strings.HasPrefix(rest, "uhr"))
	}
	return false
}

// applyPart reads an unqualified hour in the light of a day part.
func applyPart(h int, part string) (int, bool) {
	switch part {
	case partMorning, partForenoon:
		return h, true
	case partNoon:
		if h >= 1 && h <= 5 {
			return h + 12, true
		}
		return h, true
	case partAfternoon, partEvening:
		if h < 12 {
			return h + 12, true
		}
		return h, true
	case partNight:
		switch {
		case h >= 6 && h < 12:
			return h + 12, true
		case h == 12:
			return 0, true
		}
		return h, true
	}
	return h, false
}

func resolveWhen(now, today time.Time, d whenDay, part string, c whenClock, hasClock, wake bool) (WhenResult, bool) {
	loc := now.Location()
	at := func(day time.Time, h, m int) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, loc)
	}
	res := WhenResult{HasDay: d.set, HasClock: hasClock}
	if !hasClock {
		if part == "" {
			if !d.set {
				return WhenResult{}, false
			}
			res.At, res.DefaultedClock = at(d.day, 9, 0), true
			return res, true
		}
		h := partHour[part]
		res.DefaultedClock = part != partNoon
		res.HasClock = part == partNoon
		if !d.set {
			t := at(today, h, 0)
			if !t.After(now) {
				t = at(today.AddDate(0, 0, 1), h, 0)
			}
			res.At = t
			return res, true
		}
		res.At = at(d.day, h, 0)
		return res, true
	}

	h, m := c.h, c.m
	explicit := c.explicit
	dayAdd := 0
	if c.midnight || h == 24 {
		h, explicit = 0, true
		if d.set {
			dayAdd = 1 // "Freitag um Mitternacht": the end of Friday
		}
	}
	if !explicit && part != "" {
		var nightEarly bool
		if part == partNight && h == 12 {
			dayAdd, nightEarly = 1, true
		}
		h, explicit = applyPart(h, part)
		if part == partNight && h < 6 && d.set && !nightEarly {
			dayAdd = 1 // "heute Nacht um 2", "Samstagnacht um 2": the night after that day
		}
	} else if explicit && part == partNight && h < 6 && d.set && !c.midnight && h != 0 {
		dayAdd = 1
	}

	if !d.set || d.today {
		base := today
		if d.set {
			base = d.day
		}
		cands := []time.Time{at(base.AddDate(0, 0, dayAdd), h, m)}
		if !explicit && h >= 1 && h <= 12 && !wake { // an alarm "auf 7 Uhr" is 07:00
			if h == 12 {
				cands = append(cands, at(base.AddDate(0, 0, 1), 0, m))
			} else {
				cands = append(cands, at(base, h+12, m))
			}
		}
		for _, t := range cands {
			if t.After(now) {
				res.At = t
				return res, true
			}
		}
		if d.set { // "heute um 8" when both 8:00 and 20:00 are over
			return WhenResult{}, false
		}
		res.At = at(base.AddDate(0, 0, 1+dayAdd), h, m) // "um 7" at 23:00: tomorrow 07:00
		return res, true
	}
	if !explicit && h >= 1 && h <= 5 && !wake {
		h += 12 // "am Freitag um 3" means the afternoon
	}
	res.At = at(d.day.AddDate(0, 0, dayAdd), h, m)
	return res, true
}

// whenDayPart reports the day part named in text ("abends" -> "evening"),
// for corrections such as "nein, abends".
func whenDayPart(text string) (string, bool) {
	w := newWhenScan(text)
	for _, m := range reGreeting.FindAllIndex(w.s, -1) {
		w.blank(m[0], m[1])
	}
	if reAmMorn.Match(w.s) {
		return partMorning, true
	}
	if m := w.first(reDayPart, nil); m != nil {
		if p := partOf[w.group(m, 2)]; p != "" {
			return p, true
		}
	}
	return "", false
}

// whenRecurring reports words that ask for a repeating reminder ("jeden
// Montag", "every day", "immer wenn"). parseRecurring reads the ones it
// understands; the rest ("immer wenn ich heimkomme") ring once and the reply
// says so.
func whenRecurring(text string) bool {
	return reRecurring.Match(newWhenScan(text).s)
}

// ---------------------------------------------------------------------------
// parseDayRange

// parseDayRange reads a day or week span ("heute", "morgen", "übermorgen",
// "am Freitag", "diese Woche", "nächste Woche" (Mon–Sun of next week), "am
// Wochenende", "gestern", "in 3 Tagen", dates). from is the first midnight,
// to the exclusive end; label is the matched phrase from the original text.
func parseDayRange(text string, now time.Time, loc *time.Location) (from, to time.Time, label string, ok bool) {
	if loc == nil {
		loc = now.Location()
	}
	now = now.In(loc)
	today := midnightOf(now)
	w := newWhenScan(text)
	for _, m := range reGreeting.FindAllIndex(w.s, -1) {
		w.blank(m[0], m[1])
	}
	labelOf := func(m []int) string {
		return tidyLabel(w.orig[w.idx[m[0]]:w.idx[m[1]]])
	}
	oneDay := func(day time.Time, m []int) (time.Time, time.Time, string, bool) {
		return day, day.AddDate(0, 0, 1), labelOf(m), true
	}

	if m := w.first(reWeekend, nil); m != nil {
		sat := today.AddDate(0, 0, (int(time.Saturday)-int(now.Weekday())+7)%7)
		if now.Weekday() == time.Sunday {
			sat = today.AddDate(0, 0, -1)
		}
		phrase := string(w.s[m[0]:m[1]])
		if strings.Contains(phrase, "naechstes") || strings.Contains(phrase, "next") {
			sat = mondayOf(today).AddDate(0, 0, 12)
		}
		from := sat
		if from.Before(today) {
			from = today
		}
		return from, sat.AddDate(0, 0, 2), labelOf(m), true
	}
	if m := w.first(reNextWeek, nil); m != nil && !reWeekday.Match(w.s) {
		which := w.group(m, 1)
		mon := mondayOf(today)
		switch {
		case strings.HasPrefix(which, "dies") || which == "this":
			return today, mon.AddDate(0, 0, 7), labelOf(m), true
		case which == "" || strings.HasPrefix(which, "ueber"):
			return mon.AddDate(0, 0, 14), mon.AddDate(0, 0, 21), labelOf(m), true
		}
		return mon.AddDate(0, 0, 7), mon.AddDate(0, 0, 14), labelOf(m), true
	}
	if m := w.first(reYesterday, nil); m != nil {
		back := 1
		if w.group(m, 1) == "vorgestern" {
			back = 2
		}
		return oneDay(today.AddDate(0, 0, -back), m)
	}
	if m := w.first(reInDays, nil); m != nil {
		if n, good := whenNumber(w.group(m, 1)); good && n == float64(int(n)) && n <= 366 {
			return oneDay(today.AddDate(0, 0, int(n)), m)
		}
	}
	// A single day: reuse the parser's day logic on a fresh scan, but a
	// weekday that is today means today ("Was habe ich am Freitag?").
	if m := w.first(reWeekday, nil); m != nil && w.group(m, 4) == "" && !w.nextWeekBeforeNoTake(m[0]) {
		wd := whenWeekdays[w.group(m, 2)]
		prefix := w.group(m, 1)
		delta := (int(wd) - int(now.Weekday()) + 7) % 7
		if delta == 0 && (strings.HasPrefix(prefix, "naechst") || strings.HasPrefix(prefix, "kommend") || prefix == "next") {
			delta = 7
		}
		if strings.HasPrefix(prefix, "ueber") {
			delta += 7
		}
		return oneDay(today.AddDate(0, 0, delta), m)
	}
	probe := newWhenScan(text)
	for _, g := range reGreeting.FindAllIndex(probe.s, -1) {
		probe.blank(g[0], g[1])
	}
	d := probe.parseDay(now, today)
	if !d.set || len(probe.cut) == 0 {
		return time.Time{}, time.Time{}, "", false
	}
	first := probe.cut[0]
	last := probe.cut[len(probe.cut)-1]
	a, b := first.a, last.b
	if last.a < a {
		a = last.a
	}
	if first.b > b {
		b = first.b
	}
	return d.day, d.day.AddDate(0, 0, 1), tidyLabel(probe.orig[probe.idx[a]:probe.idx[b]]), true
}

// nextWeekBeforeNoTake is nextWeekBefore without consuming the phrase.
func (w *whenScan) nextWeekBeforeNoTake(pos int) bool {
	head := string(w.s[:pos])
	loc := reNextWeek.FindAllStringSubmatchIndex(head, -1)
	if len(loc) == 0 {
		return false
	}
	m := loc[len(loc)-1]
	return strings.TrimSpace(head[m[1]:]) == ""
}

// ---------------------------------------------------------------------------
// Speaking times

var (
	weekdaysDE = [...]string{"Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"}
	monthsDE   = [...]string{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"}
)

// calendarDaysBetween counts calendar days from a to b in a's location
// (DST-safe: compares dates, not 24 h blocks).
func calendarDaysBetween(a, b time.Time) int {
	b = b.In(a.Location())
	ya, ma, da := a.Date()
	yb, mb, db := b.Date()
	ua := time.Date(ya, ma, da, 0, 0, 0, 0, time.UTC)
	ub := time.Date(yb, mb, db, 0, 0, 0, 0, time.UTC)
	return int(ub.Sub(ua).Hours() / 24)
}

// formatClock speaks a clock time: "14:50 Uhr", "8 Uhr"; EN "2:50 PM".
func formatClock(t time.Time, lang string) string {
	if lang == "de" {
		if t.Minute() == 0 {
			return fmt.Sprintf("%d Uhr", t.Hour())
		}
		return fmt.Sprintf("%d:%02d Uhr", t.Hour(), t.Minute())
	}
	return t.Format("3:04 PM")
}

// formatWhen speaks t relative to now ("heute um 14:50 Uhr", "tomorrow
// (Saturday) at 8:30 AM"); t is rendered in now's location.
func formatWhen(t, now time.Time, lang string) string {
	t = t.In(now.Location())
	days := calendarDaysBetween(now, t)
	day, clock := formatDay(t, now, lang), formatClock(t, lang)
	if days == 0 || days == 1 {
		return day + pick(lang, " at ", " um ") + clock
	}
	return day + pick(lang, ", at ", ", um ") + clock
}

// formatDay speaks the day of t relative to now ("heute", "morgen
// (Samstag)", "am Montag, 9. November"; "on Monday, November 9").
func formatDay(t, now time.Time, lang string) string {
	t = t.In(now.Location())
	days := calendarDaysBetween(now, t)
	if lang == "de" {
		switch days {
		case 0:
			return "heute"
		case 1:
			return "morgen (" + weekdaysDE[t.Weekday()] + ")"
		}
		s := fmt.Sprintf("am %s, %d. %s", weekdaysDE[t.Weekday()], t.Day(), monthsDE[t.Month()-1])
		if t.Year() != now.Year() && (days > 180 || days < 0) {
			s += fmt.Sprintf(" %d", t.Year())
		}
		return s
	}
	switch days {
	case 0:
		return "today"
	case 1:
		return "tomorrow (" + t.Weekday().String() + ")"
	}
	s := fmt.Sprintf("on %s, %s %d", t.Weekday(), t.Month(), t.Day())
	if t.Year() != now.Year() && (days > 180 || days < 0) {
		s += fmt.Sprintf(", %d", t.Year())
	}
	return s
}

// formatDuration speaks d in days, hours, minutes and seconds ("1 Stunde 30
// Minuten", "20 minutes").
func formatDuration(d time.Duration, lang string) string {
	if d < 0 {
		d = -d
	}
	d = d.Round(time.Second)
	days := int(d / (24 * time.Hour))
	h, m, s := int(d%(24*time.Hour)/time.Hour), int(d%time.Hour/time.Minute), int(d%time.Minute/time.Second)
	unit := func(n int, enOne, enMany, deOne, deMany string) string {
		if n == 1 {
			return "1 " + pick(lang, enOne, deOne)
		}
		return fmt.Sprintf("%d %s", n, pick(lang, enMany, deMany))
	}
	var parts []string
	if days > 0 {
		parts = append(parts, unit(days, "day", "days", "Tag", "Tage"))
	}
	if h > 0 {
		parts = append(parts, unit(h, "hour", "hours", "Stunde", "Stunden"))
	}
	if m > 0 {
		parts = append(parts, unit(m, "minute", "minutes", "Minute", "Minuten"))
	}
	if s > 0 || len(parts) == 0 {
		parts = append(parts, unit(s, "second", "seconds", "Sekunde", "Sekunden"))
	}
	return strings.Join(parts, " ")
}

// ---------------------------------------------------------------------------
// Reminder text

var (
	reTidyLead = regexp.MustCompile(`(?i)^(?:daran|dran|an|dass|daß|to|about|that|of|bitte|please|mal|doch|noch|nochmal|again)(?:(?:\s|[,.:;!-])+|$)`)
	reTidySep  = regexp.MustCompile(`^(?:\s|[,.:;!?–—-])+`)
	reTidyEnd  = regexp.MustCompile(`(?:\s|[,.:;!?–—-])+$`)
	reIchMuss  = regexp.MustCompile(`(?i)^(?:ich|wir)\s+(.+?)\s+(?:muss|müssen|muß|soll|sollte|sollen|will|wollte|möchte|wollen|darf)$`)
	reINeedTo  = regexp.MustCompile(`(?i)^(?:i|we)\s+(?:need\s+to|have\s+to|must|should|want\s+to|ought\s+to|got\s+to|gotta|'ve\s+got\s+to|have\s+got\s+to)\s+`)
	// Separable prefix + "zu" + verb: "rauszunehmen" -> "rausnehmen".
	reZuInf    = regexp.MustCompile(`(?i)^(herunter|herüber|hinüber|hinunter|zusammen|vorüber|zurecht|weiter|wieder|zurück|vorbei|voraus|heraus|herein|herauf|herbei|hervor|hinaus|hinein|hinauf|kennen|fertig|sauber|kaputt|nieder|runter|rüber|empor|heran|herab|herum|voran|fest|fern|fort|frei|heim|hoch|raus|rein|rauf|statt|teil|weg|los|mit|nach|ab|an|auf|aus|bei|ein|her|hin|um|vor|zu)zu(\p{L}{2,}(?:en|ern|eln))$`)
	reEndsVerb = regexp.MustCompile(`(?i)\p{L}{2,}(?:en|ern|eln)$`)
)

// tidyReminderText turns the rest of a reminder sentence into the text that
// is read back ("an die Pizza rauszunehmen" -> "die Pizza rausnehmen"):
// leading "daran", "an", "dass", "to", "about" go, "ich … muss" and "I need
// to …" become the plain task, a zu-infinitive becomes the infinitive.
func tidyReminderText(rest, lang string) string {
	s := strings.TrimSpace(rest)
	for i := 0; i < 6; i++ {
		before := s
		s = reTidySep.ReplaceAllString(s, "")
		s = reTidyLead.ReplaceAllString(s, "")
		if s == before {
			break
		}
	}
	s = reTidyEnd.ReplaceAllString(s, "")
	if m := reIchMuss.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	s = reINeedTo.ReplaceAllString(s, "")
	words := strings.Fields(s)
	if n := len(words); n > 0 {
		if m := reZuInf.FindStringSubmatch(words[n-1]); m != nil {
			words[n-1] = m[1] + m[2]
		} else if n >= 2 && strings.EqualFold(words[n-2], "zu") && reEndsVerb.MatchString(words[n-1]) {
			words = append(words[:n-2], words[n-1])
		}
		s = strings.Join(words, " ")
	}
	return strings.TrimSpace(s)
}

// ---------------------------------------------------------------------------
// Recurring rules ("jeden Montag um 8", "every other Friday", "jedes Jahr am
// 24.12.")
//
// A RecurRule is a small calendar rule: a frequency with an interval, the
// weekdays (weekly), the day of the month (monthly, yearly; past the month's
// end it means the last day, so "am 31." is February 28/29), the month
// (yearly) and a wall-clock time in a time zone. Occurrences are computed on
// calendar dates and then placed at the wall-clock time, so they stay at
// 8:00 across DST changes. Start is the date of the first occurrence; it
// anchors intervals ("alle zwei Wochen" counts from there) and no occurrence
// lies before it.

const (
	recurDaily   = "daily"
	recurWeekly  = "weekly"
	recurMonthly = "monthly"
	recurYearly  = "yearly"
)

// recurMaxInterval bounds the interval per frequency.
var recurMaxInterval = map[string]int{recurDaily: 366, recurWeekly: 52, recurMonthly: 24, recurYearly: 10}

type RecurRule struct {
	Freq     string `json:"freq"`               // "daily" | "weekly" | "monthly" | "yearly"
	Interval int    `json:"interval"`           // every n days/weeks/months/years (1 = every)
	Weekdays []int  `json:"weekdays,omitempty"` // weekly: 0 = Sunday … 6 = Saturday, sorted
	MonthDay int    `json:"monthDay,omitempty"` // monthly, yearly: 1..31 (31 = the month's last day)
	Month    int    `json:"month,omitempty"`    // yearly: 1..12
	Hour     int    `json:"hour"`               // wall clock in TZ
	Minute   int    `json:"minute"`             //
	TZ       string `json:"tz,omitempty"`       // IANA zone; "" = the household zone
	Start    string `json:"start,omitempty"`    // "2006-01-02": date of the first occurrence
}

var errRecurRule = errors.New("invalid repeat rule")

var reTZName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-]*(?:/[A-Za-z0-9_+\-]+){0,2}$`)

// validate checks a rule from any source (voice, API, the file).
func (rr RecurRule) validate() error {
	max, ok := recurMaxInterval[rr.Freq]
	if !ok {
		return fmt.Errorf("%w: freq must be daily, weekly, monthly or yearly", errRecurRule)
	}
	if rr.Interval < 0 || rr.Interval > max {
		return fmt.Errorf("%w: interval must be 1..%d for %s", errRecurRule, max, rr.Freq)
	}
	if rr.Hour < 0 || rr.Hour > 23 || rr.Minute < 0 || rr.Minute > 59 {
		return fmt.Errorf("%w: hour must be 0..23 and minute 0..59", errRecurRule)
	}
	switch rr.Freq {
	case recurWeekly:
		if len(rr.Weekdays) == 0 || len(rr.Weekdays) > 7 {
			return fmt.Errorf("%w: weekly needs 1..7 weekdays", errRecurRule)
		}
		for _, d := range rr.Weekdays {
			if d < 0 || d > 6 {
				return fmt.Errorf("%w: weekdays are 0 (Sunday) .. 6 (Saturday)", errRecurRule)
			}
		}
	case recurMonthly:
		if rr.MonthDay < 1 || rr.MonthDay > 31 {
			return fmt.Errorf("%w: monthDay must be 1..31", errRecurRule)
		}
	case recurYearly:
		if rr.Month < 1 || rr.Month > 12 || rr.MonthDay < 1 || rr.MonthDay > 31 {
			return fmt.Errorf("%w: yearly needs month 1..12 and monthDay 1..31", errRecurRule)
		}
	}
	if rr.TZ != "" {
		if len(rr.TZ) > 64 || !reTZName.MatchString(rr.TZ) || recurLocation(rr.TZ) == nil {
			return fmt.Errorf("%w: unknown time zone", errRecurRule)
		}
	}
	if rr.Start != "" {
		if _, err := time.Parse(time.DateOnly, rr.Start); err != nil {
			return fmt.Errorf("%w: start must be YYYY-MM-DD", errRecurRule)
		}
	}
	return nil
}

// normalize fills the default interval, sorts the weekdays and drops the
// fields the frequency does not use.
func (rr *RecurRule) normalize() {
	if rr.Interval <= 0 {
		rr.Interval = 1
	}
	if rr.Freq != recurWeekly {
		rr.Weekdays = nil
	} else {
		var seen [7]bool
		for _, d := range rr.Weekdays {
			if d >= 0 && d <= 6 {
				seen[d] = true
			}
		}
		wd := make([]int, 0, 7)
		for d := 0; d < 7; d++ {
			if seen[d] {
				wd = append(wd, d)
			}
		}
		rr.Weekdays = wd
	}
	if rr.Freq != recurMonthly && rr.Freq != recurYearly {
		rr.MonthDay = 0
	}
	if rr.Freq != recurYearly {
		rr.Month = 0
	}
}

// clone deep-copies the rule (Weekdays is a slice).
func (rr *RecurRule) clone() *RecurRule {
	if rr == nil {
		return nil
	}
	c := *rr
	c.Weekdays = append([]int(nil), rr.Weekdays...)
	return &c
}

var recurLocs sync.Map // zone name -> *time.Location (LoadLocation reads tzdata)

func recurLocation(name string) *time.Location {
	if v, ok := recurLocs.Load(name); ok {
		return v.(*time.Location)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil
	}
	recurLocs.Store(name, loc)
	return loc
}

// location is the rule's zone, else fallback (the household zone).
func (rr RecurRule) location(fallback *time.Location) *time.Location {
	if rr.TZ != "" && reTZName.MatchString(rr.TZ) {
		if loc := recurLocation(rr.TZ); loc != nil {
			return loc
		}
	}
	if fallback != nil {
		return fallback
	}
	return time.UTC
}

// civilDate is t's calendar date as UTC midnight: day arithmetic on it is
// exact (no DST).
func civilDate(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func civilDaysBetween(a, b time.Time) int { return int(b.Sub(a).Hours() / 24) }

func daysInMonth(y int, m time.Month) int { return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day() }

// Next returns the first occurrence strictly after after, never before
// Start. ok is false for an invalid rule.
func (rr RecurRule) Next(after time.Time, fallback *time.Location) (time.Time, bool) {
	if rr.validate() != nil {
		return time.Time{}, false
	}
	rr.normalize()
	loc := rr.location(fallback)
	from := civilDate(after.In(loc))
	anchor := from
	if rr.Start != "" {
		if s, err := time.Parse(time.DateOnly, rr.Start); err == nil {
			anchor = s
			if s.After(from) {
				from = s
			}
		}
	}
	at := func(d time.Time) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), rr.Hour, rr.Minute, 0, 0, loc)
	}
	n := rr.Interval
	switch rr.Freq {
	case recurDaily, recurWeekly:
		var days [7]bool
		for _, d := range rr.Weekdays {
			days[d] = true
		}
		for i := 0; i <= 7*n+7; i++ {
			d := from.AddDate(0, 0, i)
			if rr.Freq == recurDaily {
				if civilDaysBetween(anchor, d)%n != 0 {
					continue
				}
			} else if !days[d.Weekday()] || (civilDaysBetween(mondayOf(anchor), mondayOf(d))/7)%n != 0 {
				continue
			}
			if t := at(d); t.After(after) {
				return t, true
			}
		}
	case recurMonthly:
		for i := 0; i <= 12*n+2; i++ {
			first := time.Date(from.Year(), from.Month()+time.Month(i), 1, 0, 0, 0, 0, time.UTC)
			months := (first.Year()-anchor.Year())*12 + int(first.Month()) - int(anchor.Month())
			if months%n != 0 {
				continue
			}
			d := time.Date(first.Year(), first.Month(), min(rr.MonthDay, daysInMonth(first.Year(), first.Month())), 0, 0, 0, 0, time.UTC)
			if d.Before(from) {
				continue
			}
			if t := at(d); t.After(after) {
				return t, true
			}
		}
	case recurYearly:
		m := time.Month(rr.Month)
		for i := 0; i <= 2*n+2; i++ {
			y := from.Year() + i
			if (y-anchor.Year())%n != 0 {
				continue
			}
			d := time.Date(y, m, min(rr.MonthDay, daysInMonth(y, m)), 0, 0, 0, 0, time.UTC)
			if d.Before(from) {
				continue
			}
			if t := at(d); t.After(after) {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// begin anchors a new rule: without a Start, its first occurrence after now
// (and not before notBefore, when set) becomes Start, so "alle zwei Wochen am
// Freitag" starts this Friday and counts from there.
func (rr *RecurRule) begin(now, notBefore time.Time, fallback *time.Location) (time.Time, bool) {
	if rr.validate() != nil {
		return time.Time{}, false
	}
	rr.normalize()
	after := now
	if !notBefore.IsZero() && notBefore.Add(-time.Nanosecond).After(after) {
		after = notBefore.Add(-time.Nanosecond)
	}
	if rr.Start != "" {
		return rr.Next(after, fallback)
	}
	tmp := *rr
	tmp.Interval = 1
	first, ok := tmp.Next(after, fallback)
	if !ok {
		return time.Time{}, false
	}
	rr.Start = first.In(rr.location(fallback)).Format(time.DateOnly)
	return first, true
}

// Describe speaks the rule: "jeden Montag um 8 Uhr", "every other Friday at
// 6:00 PM", "on the 1st of every month at 9:00 AM".
func (rr RecurRule) Describe(lang string) string {
	rr.normalize()
	de := lang == "de"
	clock := formatClock(time.Date(2000, 1, 1, rr.Hour, rr.Minute, 0, 0, time.UTC), lang)
	n := rr.Interval
	var s string
	switch rr.Freq {
	case recurDaily:
		switch {
		case n == 1:
			s = pick(lang, "every day", "jeden Tag")
		case n == 2:
			s = pick(lang, "every other day", "jeden zweiten Tag")
		default:
			s = fmt.Sprintf(pick(lang, "every %d days", "alle %d Tage"), n)
		}
	case recurWeekly:
		set := fmt.Sprint(rr.Weekdays)
		var names []string
		for _, d := range rr.Weekdays {
			if de {
				names = append(names, weekdaysDE[d])
			} else {
				names = append(names, time.Weekday(d).String())
			}
		}
		// Monday first in speech ("Montag und Sonntag", not "Sonntag und Montag").
		if len(rr.Weekdays) > 1 && rr.Weekdays[0] == 0 {
			names = append(names[1:], names[0])
		}
		days := joinAnd(names, lang)
		switch {
		case set == "[0 1 2 3 4 5 6]" && n == 1:
			s = pick(lang, "every day", "jeden Tag")
		case set == "[1 2 3 4 5]" && n == 1:
			s = pick(lang, "every weekday", "jeden Werktag")
		case set == "[1 2 3 4 5]":
			s = fmt.Sprintf(pick(lang, "every %d weeks on weekdays", "alle %d Wochen werktags"), n)
		case set == "[0 6]" && n == 1:
			s = pick(lang, "every Saturday and Sunday", "jeden Samstag und Sonntag")
		case n == 1:
			s = pick(lang, "every ", "jeden ") + days
		case n == 2 && len(names) == 1:
			s = pick(lang, "every other ", "jeden zweiten ") + days
		default:
			s = fmt.Sprintf(pick(lang, "every %d weeks on ", "alle %d Wochen am "), n) + days
		}
	case recurMonthly:
		day := monthDayText(rr.MonthDay, lang)
		switch {
		case de && n == 1:
			s = "jeden Monat " + day
		case de:
			s = fmt.Sprintf("alle %d Monate ", n) + day
		case n == 1:
			s = day + " of every month"
		case n == 2:
			s = day + " of every other month"
		default:
			s = fmt.Sprintf("every %d months ", n) + day
		}
	case recurYearly:
		var date string
		if de {
			date = fmt.Sprintf("am %d. %s", rr.MonthDay, monthsDE[rr.Month-1])
		} else {
			date = fmt.Sprintf("on %s %d", time.Month(rr.Month), rr.MonthDay)
		}
		switch {
		case n == 1:
			s = pick(lang, "every year ", "jedes Jahr ") + date
		default:
			s = fmt.Sprintf(pick(lang, "every %d years ", "alle %d Jahre "), n) + date
		}
	default:
		return ""
	}
	return s + pick(lang, " at ", " um ") + clock
}

// monthDayText: "am 1." / "on the 1st"; 31 is the month's last day.
func monthDayText(d int, lang string) string {
	if d >= 31 {
		return pick(lang, "on the last day", "am Monatsletzten")
	}
	if lang == "de" {
		return fmt.Sprintf("am %d.", d)
	}
	return "on the " + ordinalEN(d)
}

func ordinalEN(n int) string {
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return strconv.Itoa(n) + suffix
}

// RecurResult is a parsed repeating time.
type RecurResult struct {
	Rule           RecurRule
	First          time.Time // the first occurrence, in loc
	DefaultedClock bool      // no clock named: 09:00 (or the day part's hour) was assumed
	Rest           string    // input with the repeat and time cut out, tidied
}

// recurOrdinals are "every other" / "jeden zweiten" interval words.
var recurOrdinals = map[string]int{"zweiten": 2, "dritten": 3, "vierten": 4, "fuenften": 5, "sechsten": 6,
	"other": 2, "second": 2, "third": 3, "fourth": 4, "fifth": 5, "sixth": 6}

// recurDayWords are spoken days of the month ("am Ersten", "the fifteenth";
// "letzten"/"last" is the month's last day).
var recurDayWords = func() map[string]float64 {
	m := map[string]float64{"letzten": 31, "last": 31}
	de := []string{"", "ersten", "zweiten", "dritten", "vierten", "fuenften", "sechsten", "siebten", "achten", "neunten",
		"zehnten", "elften", "zwoelften"}
	for i, w := range de {
		if i > 0 {
			m[w] = float64(i)
		}
	}
	for w, v := range deTeens {
		if v >= 13 {
			m[w+"ten"] = float64(v)
		}
	}
	m["zwanzigsten"], m["dreissigsten"], m["einunddreissigsten"] = 20, 30, 31
	for i := 1; i <= 9; i++ {
		m[deOnes[i]+"undzwanzigsten"] = float64(20 + i)
	}
	en := []string{"", "first", "second", "third", "fourth", "fifth", "sixth", "seventh", "eighth", "ninth", "tenth",
		"eleventh", "twelfth", "thirteenth", "fourteenth", "fifteenth", "sixteenth", "seventeenth", "eighteenth",
		"nineteenth", "twentieth"}
	for i, w := range en {
		if i > 0 {
			m[w] = float64(i)
		}
	}
	for i := 1; i <= 9; i++ {
		m["twenty "+en[i]] = float64(20 + i)
	}
	m["thirtieth"], m["thirty first"] = 30, 31
	return m
}()

var (
	recurNumAlt   = `\d{1,3}|` + altOf(hourWords)
	recurOrdAlt   = `zweiten|dritten|vierten|fuenften|sechsten|other|second|third|fourth|fifth|sixth|\d{1,2}(?:\.|st|nd|rd|th)`
	recurDayAlt   = `\d{1,2}(?:\.|st|nd|rd|th)?|` + altOf(recurDayWords)
	recurWdayS    = `(?:` + whenWdayAlt + `)s?`
	recurWdayList = recurWdayS + `(?:\s+(?:und|and|or|oder|sowie|&)?\s*` + recurWdayS + `)*`
	recurEvery    = `(?:jeden|jede|jedes|jedem|jeder|an\s+jedem|immer|every|each|on\s+every)`

	// "alle zwei Wochen", "every 3 days"
	reRecurUnitN = regexp.MustCompile(`\b(?:alle|every|each)\s+(` + recurNumAlt + `)\s+(tage|tagen|days|wochen|weeks|monate|monaten|months|jahre|jahren|years)\b`)
	// "jeden zweiten Freitag", "every other Friday"
	reRecurOrdWday = regexp.MustCompile(`\b(?:jeden|an\s+jedem|every|each|on\s+every)\s+(` + recurOrdAlt + `)\s+(` + whenWdayAlt + `)\b`)
	// "jeden zweiten Tag", "every other week"
	reRecurOrdUnit = regexp.MustCompile(`\b(?:jeden|jede|jedes|every|each)\s+(` + recurOrdAlt + `)\s+(tag|woche|monat|jahr|day|week|month|year)\b`)
	// "jeden Werktag", "werktags", "every weekday", "Montag bis Freitag"
	reRecurWorkdays = regexp.MustCompile(`\b(?:(?:jeden|an\s+jedem|jedem|every|each|on)\s+(?:werktag|arbeitstag|wochentag|weekday|workday|work\s+day|business\s+day)s?|werktags|wochentags|arbeitstags|weekdays|workdays|(?:von\s+)?montags?\s+bis\s+freitags?|(?:from\s+)?monday\s+(?:to|through|thru|till|until)\s+friday)\b`)
	// "jedes Wochenende", "every weekend", "on weekends"
	reRecurWeekend = regexp.MustCompile(`\b(?:(?:jedes|an\s+jedem|jedem|every|each)\s+wochenende|(?:every|each)\s+weekend|wochenends|(?:on\s+)?weekends)\b`)
	// "am 1. jedes Monats", "on the 1st of every month", "jeden Ersten des Monats"
	// ("am Ersten des Monats" alone is the next first: one-off.)
	reRecurMonthDay = regexp.MustCompile(`\b(?:(?:jeden|every|each)\s+(` + recurDayAlt + `)\s*(?:tag\s+|day\s+)?(?:des\s+monats|im\s+monat|of\s+(?:the|each|every|a)\s+month)` +
		`|(?:am|on\s+the|on|the)\s+(` + recurDayAlt + `)\s*(?:tag\s+|day\s+)?(?:eines\s+jeden\s+monats|jedes\s+monats|of\s+(?:each|every)\s+month))\b`)
	// "jeden Montag (und Donnerstag)", "every Monday", "immer montags"
	reRecurWdays = regexp.MustCompile(`\b` + recurEvery + `\s+(?:(?:am|on)\s+)?(` + recurWdayList + `)\b`)
	// "montags", "on Mondays and Thursdays"
	reRecurWdayPlural = regexp.MustCompile(`\b(?:on\s+)?((?:` + whenWdayAlt + `)s(?:\s+(?:und|and|or|oder|sowie|&)?\s*(?:` + whenWdayAlt + `)s)*)\b`)
	// "jeden Abend", "every morning", "nightly"
	reRecurDayPart = regexp.MustCompile(`\b(?:(?:jeden|jede|every|each)\s+(morgen|vormittag|mittag|nachmittag|abend|nacht|morning|afternoon|evening|night)|(nightly))\b`)
	reRecurDaily   = regexp.MustCompile(`\b(?:jeden\s+(?:einzelnen\s+)?tag|tagtaeglich|taeglich|(?:every|each)\s+(?:single\s+)?day|daily|everyday)\b`)
	reRecurWeekly  = regexp.MustCompile(`\b(?:jede\s+woche|woechentlich|(?:every|each)\s+week|weekly|einmal\s+(?:in\s+der|pro|die)\s+woche|once\s+a\s+week|(biweekly|fortnightly|zweiwoechentlich))\b`)
	reRecurMonthly = regexp.MustCompile(`\b(?:jeden\s+monat|monatlich|(?:every|each)\s+month|monthly|einmal\s+(?:im|pro|den)\s+monat|once\s+a\s+month)\b`)
	reRecurYearly  = regexp.MustCompile(`\b(?:jedes\s+jahr|jaehrlich|(?:every|each)\s+year|yearly|annually|einmal\s+(?:im|pro|das)\s+jahr|once\s+a\s+year)\b`)
	// "jeden 24.12.", "every December 24th", "every 24th of December"
	reRecurEveryWord = regexp.MustCompile(`\b(?:jeden|every|each)\s+`)

	// Details after the cue.
	reRecurWdayAny  = regexp.MustCompile(`\b(?:(?:am|on|an|jeden|every)\s+)?(` + recurWdayList + `)\b`)
	reRecurMDayAny  = regexp.MustCompile(`\b(?:am|on\s+the|on|the|zum|den)\s+(` + recurDayAlt + `)(?:\s+(?:tag|day))?(?:\s|$)`)
	reRecurMDayBare = regexp.MustCompile(`\b(\d{1,2})(?:st|nd|rd|th)\b`)
	reRecurWdayWord = regexp.MustCompile(whenWdayAlt)
)

// recurBareAdj are English cue words that are also adjectives ("the daily
// standup", "the monthly report"): they only count before a time word or at
// the end ("remind me daily at 10pm", "take my pills daily").
var recurBareAdj = wordSet("daily", "everyday", "weekly", "biweekly", "fortnightly", "monthly", "yearly", "annually", "nightly")

var recurAfterBare = wordSet("", "at", "on", "in", "from", "starting", "beginning", "until", "till", "to", "about",
	"that", "and", "um", "ab", "am", "an", "von", "bis", "dass", "zu", "please", "bitte")

// bareCueOK accepts a cue match unless it is an adjective before a noun.
func (w *whenScan) bareCueOK(m []int) bool {
	cue := strings.TrimSpace(string(w.s[m[0]:m[1]]))
	if !recurBareAdj[cue] {
		return true
	}
	return recurAfterBare[strings.TrimRight(w.wordAfter(m[1]), ".:")]
}

// recurOrdinal reads "zweiten", "other", "2.", "3rd".
func recurOrdinal(s string) int {
	if v, ok := recurOrdinals[s]; ok {
		return v
	}
	s = strings.TrimRight(s, ".stndrh")
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// recurDay reads a day of the month: "1.", "15th", "Ersten", "last".
func recurDay(s string) int {
	s = strings.Join(strings.Fields(s), " ")
	if v, ok := recurDayWords[s]; ok {
		return int(v)
	}
	n, err := strconv.Atoi(strings.TrimRight(s, ".stndrh"))
	if err != nil || n < 1 || n > 31 {
		return 0
	}
	return n
}

// recurWeekdays lists the weekdays named in s, in order.
func recurWeekdays(s string) []int {
	var out []int
	for _, w := range reRecurWdayWord.FindAllString(s, -1) {
		out = append(out, int(whenWeekdays[w]))
	}
	return out
}

func recurFreqOfUnit(u string) string {
	switch {
	case strings.HasPrefix(u, "tag") || strings.HasPrefix(u, "day"):
		return recurDaily
	case strings.HasPrefix(u, "woch") || strings.HasPrefix(u, "week"):
		return recurWeekly
	case strings.HasPrefix(u, "monat") || strings.HasPrefix(u, "month"):
		return recurMonthly
	}
	return recurYearly
}

// everyDate finds "jeden 24.12." / "every December 24th": an "every" word
// right before a date. It returns the date's month and day.
func (w *whenScan) everyDate() (time.Month, int, bool) {
	for _, m := range reRecurEveryWord.FindAllIndex(w.s, -1) {
		tail := w.s[m[1]:]
		for _, re := range []*regexp.Regexp{reDateNum, reDateDE, reDateEN} {
			d := re.FindSubmatchIndex(tail)
			if d == nil || d[0] != 0 {
				continue
			}
			g := func(i int) string {
				if d[2*i] < 0 {
					return ""
				}
				return string(tail[d[2*i]:d[2*i+1]])
			}
			var mon time.Month
			var dd int
			switch re {
			case reDateNum:
				dd, _ = strconv.Atoi(g(1))
				mm, _ := strconv.Atoi(g(2))
				mon = time.Month(mm)
			case reDateDE:
				dd, _ = strconv.Atoi(g(1))
				mon = whenMonths[g(2)]
			default:
				mon = whenMonths[g(1)]
				dd, _ = strconv.Atoi(g(2))
			}
			if mon < 1 || mon > 12 || dd < 1 || dd > daysInMonth(2028, mon) {
				continue
			}
			w.take(m[0], m[1]+d[1])
			return mon, dd, true
		}
	}
	return 0, 0, false
}

// recurDate finds a calendar date ("am 24.12.", "on December 24") for a
// yearly rule.
func (w *whenScan) recurDate() (time.Month, int, bool) {
	for _, re := range []*regexp.Regexp{reDateNum, reDateAm, reDateDE, reDateEN} {
		m := w.first(re, nil)
		if m == nil {
			continue
		}
		var mon time.Month
		var dd int
		switch re {
		case reDateNum, reDateAm:
			dd, _ = strconv.Atoi(w.group(m, 1))
			mm, _ := strconv.Atoi(w.group(m, 2))
			mon = time.Month(mm)
		case reDateDE:
			dd, _ = strconv.Atoi(w.group(m, 1))
			mon = whenMonths[w.group(m, 2)]
		default:
			mon = whenMonths[w.group(m, 1)]
			dd, _ = strconv.Atoi(w.group(m, 2))
		}
		if mon < 1 || mon > 12 || dd < 1 || dd > daysInMonth(2028, mon) {
			continue
		}
		w.take(m[0], m[1])
		return mon, dd, true
	}
	return 0, 0, false
}

// recurClock turns the parsed clock into the rule's wall-clock time. An
// unqualified 1–5 is the afternoon ("jeden Freitag um 3" is 15:00) unless the
// sentence is about waking up; without a clock the day part's hour or 09:00.
func recurClock(c whenClock, hasClock bool, part string, wake bool) (h, m int, defaulted bool) {
	if !hasClock {
		if part != "" {
			return partHour[part], 0, part != partNoon
		}
		return 9, 0, true
	}
	h, m = c.h, c.m
	if c.midnight || h == 24 {
		return 0, m, false
	}
	switch {
	case c.explicit:
	case part != "":
		h, _ = applyPart(h, part)
	case h >= 1 && h <= 5 && !wake:
		h += 12
	}
	return h % 24, m, false
}

// parseRecurring reads a repeating time from text: "jeden Montag um 8",
// "täglich um 22 Uhr", "alle zwei Wochen am Freitag", "jeden Monat am 1.",
// "jedes Jahr am 24.12.", "every weekday at 7", "every other Friday", "on
// the 1st of every month", "every year on December 24". ok is false when
// text names no repeat this parser understands.
func parseRecurring(text string, now time.Time, loc *time.Location) (RecurResult, bool) {
	if loc == nil {
		loc = now.Location()
	}
	now = now.In(loc)
	today := midnightOf(now)
	w := newWhenScan(text)
	for _, m := range reGreeting.FindAllIndex(w.s, -1) {
		w.blank(m[0], m[1])
	}
	wake := reWakeWord.Match(w.s)
	rr := RecurRule{Interval: 1}
	part := ""
	var mon time.Month
	mday := 0

	cue := func(re *regexp.Regexp) []int {
		m := w.first(re, w.bareCueOK)
		if m != nil {
			w.take(m[0], m[1])
		}
		return m
	}
	var m []int
	switch {
	case func() bool { m = w.first(reRecurUnitN, nil); return m != nil }():
		n, ok := whenNumber(w.group(m, 1))
		if !ok || n < 1 || n != float64(int(n)) {
			return RecurResult{}, false
		}
		rr.Freq, rr.Interval = recurFreqOfUnit(w.group(m, 2)), int(n)
		w.take(m[0], m[1])
	case func() bool { m = w.first(reRecurOrdWday, nil); return m != nil }():
		rr.Freq, rr.Interval = recurWeekly, recurOrdinal(w.group(m, 1))
		rr.Weekdays = []int{int(whenWeekdays[w.group(m, 2)])}
		w.take(m[0], m[1])
	case func() bool { m = w.first(reRecurOrdUnit, nil); return m != nil }():
		rr.Freq, rr.Interval = recurFreqOfUnit(w.group(m, 2)), recurOrdinal(w.group(m, 1))
		w.take(m[0], m[1])
	case cue(reRecurWorkdays) != nil:
		rr.Freq, rr.Weekdays = recurWeekly, []int{1, 2, 3, 4, 5}
	case cue(reRecurWeekend) != nil:
		rr.Freq, rr.Weekdays = recurWeekly, []int{int(time.Saturday)}
	case func() bool { m = w.first(reRecurMonthDay, nil); return m != nil }():
		rr.Freq, mday = recurMonthly, recurDay(w.group(m, 1)+w.group(m, 2))
		w.take(m[0], m[1])
	case func() bool { var ok bool; mon, mday, ok = w.everyDate(); return ok }():
		rr.Freq = recurYearly
	case func() bool { m = w.first(reRecurWdays, nil); return m != nil }(),
		func() bool { m = w.first(reRecurWdayPlural, nil); return m != nil }():
		rr.Freq, rr.Weekdays = recurWeekly, recurWeekdays(w.group(m, 1))
		w.take(m[0], m[1])
	case func() bool { m = w.first(reRecurDayPart, w.bareCueOK); return m != nil }():
		word := w.group(m, 1)
		if word == "" {
			word = "night"
		}
		rr.Freq, part = recurDaily, partOf[word]
		if word == "morgen" {
			part = partMorning
		}
		w.take(m[0], m[1])
	case cue(reRecurDaily) != nil:
		rr.Freq = recurDaily
	case func() bool { m = w.first(reRecurWeekly, w.bareCueOK); return m != nil }():
		rr.Freq = recurWeekly
		if w.group(m, 1) != "" {
			rr.Interval = 2
		}
		w.take(m[0], m[1])
	case cue(reRecurMonthly) != nil:
		rr.Freq = recurMonthly
	case cue(reRecurYearly) != nil:
		rr.Freq = recurYearly
	default:
		return RecurResult{}, false
	}
	if rr.Interval < 1 || rr.Interval > recurMaxInterval[rr.Freq] {
		return RecurResult{}, false
	}

	// Details the cue left open.
	switch rr.Freq {
	case recurWeekly:
		if len(rr.Weekdays) == 0 {
			if m := w.first(reRecurWdayAny, nil); m != nil {
				rr.Weekdays = recurWeekdays(w.group(m, 1))
				w.take(m[0], m[1])
			}
		}
	case recurMonthly:
		if mday == 0 {
			if m := w.first(reRecurMDayAny, func(m []int) bool { return recurDay(w.group(m, 1)) > 0 }); m != nil {
				mday = recurDay(w.group(m, 1))
				w.take(m[0], m[1])
			} else if m := w.first(reRecurMDayBare, func(m []int) bool { return recurDay(w.group(m, 1)) > 0 }); m != nil {
				mday = recurDay(w.group(m, 1))
				w.take(m[0], m[1])
			}
		}
	case recurYearly:
		if mon == 0 {
			mon, mday, _ = w.recurDate()
		}
	}

	// Day part and clock, as in parseWhen.
	if part == "" {
		if m := w.first(reAmMorn, nil); m != nil {
			part = partMorning
			w.take(m[0], m[1])
		} else if m := w.first(reDayPart, nil); m != nil {
			if p := partOf[w.group(m, 2)]; p != "" && w.group(m, 2) != "tonight" && w.group(m, 2) != "tonite" {
				part = p
				w.take(m[0], m[1])
			}
		}
	}
	c, hasClock := w.parseClock()

	// A start day ("ab Montag", "starting tomorrow") bounds the first one.
	var notBefore time.Time
	if d := w.parseDay(now, today); d.set {
		notBefore = d.day
	}
	ref := now
	if !notBefore.IsZero() {
		ref = notBefore
	}
	switch rr.Freq {
	case recurWeekly:
		if len(rr.Weekdays) == 0 {
			rr.Weekdays = []int{int(ref.Weekday())}
		}
	case recurMonthly:
		if mday == 0 {
			mday = ref.Day()
		}
		rr.MonthDay = mday
	case recurYearly:
		if mon == 0 {
			mon, mday = ref.Month(), ref.Day()
		}
		rr.Month, rr.MonthDay = int(mon), mday
	}
	var defaulted bool
	rr.Hour, rr.Minute, defaulted = recurClock(c, hasClock, part, wake)
	if name := loc.String(); name != "Local" && name != "" {
		rr.TZ = name
	}
	if rr.validate() != nil {
		return RecurResult{}, false
	}
	first, ok := rr.begin(now, notBefore, loc)
	if !ok || first.Sub(now) >= whenMaxAhead {
		return RecurResult{}, false
	}
	return RecurResult{Rule: rr, First: first, DefaultedClock: defaulted, Rest: w.rest()}, true
}
