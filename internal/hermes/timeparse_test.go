package hermes

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var berlin = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		panic(err)
	}
	return loc
}()

// tpNow is the reference clock of the research cases: Friday, 6 November
// 2026, 14:30 in Berlin.
var tpNow = time.Date(2026, 11, 6, 14, 30, 0, 0, berlin)

const tpLayout = "2006-01-02T15:04"

func checkWhen(t *testing.T, now time.Time, in, want string) WhenResult {
	t.Helper()
	r, ok := parseWhen(in, now, berlin)
	got := ""
	if ok {
		got = r.At.Format(tpLayout)
	}
	if got != want {
		t.Errorf("%q at %s: got %q want %q", in, now.Format("Mon 15:04"), got, want)
	}
	return r
}

// The 20 phrases of design/local-llm/time_cases.json and the 15 held-out
// ones (time_cases_heldout.json), now = Fri 2026-11-06 14:30.
func TestParseWhenResearchCases(t *testing.T) {
	cases := [][2]string{
		{"in 20 Minuten", "2026-11-06T14:50"},
		{"in 20 minutes", "2026-11-06T14:50"},
		{"in einer halben Stunde", "2026-11-06T15:00"},
		{"in anderthalb Stunden", "2026-11-06T16:00"},
		{"in 2 hours", "2026-11-06T16:30"},
		{"morgen um 8", "2026-11-07T08:00"},
		{"tomorrow at 8am", "2026-11-07T08:00"},
		{"tomorrow at 7pm", "2026-11-07T19:00"},
		{"heute Abend um 8", "2026-11-06T20:00"},
		{"um 18 Uhr", "2026-11-06T18:00"},
		{"übermorgen um halb neun", "2026-11-08T08:30"},
		{"nächsten Montag um 9", "2026-11-09T09:00"},
		{"next Tuesday at 3pm", "2026-11-10T15:00"},
		{"am Dienstag um Viertel nach drei", "2026-11-10T15:15"},
		{"am 20. November um 10 Uhr", "2026-11-20T10:00"},
		{"on December 1st at noon", "2026-12-01T12:00"},
		{"um Viertel vor sieben morgens", "2026-11-07T06:45"},
		{"in zehn Minuten", "2026-11-06T14:40"},
		{"Samstag früh um 7", "2026-11-07T07:00"},
		{"at 9", "2026-11-06T21:00"},
		// held out
		{"in 45 Minuten", "2026-11-06T15:15"},
		{"in drei Stunden", "2026-11-06T17:30"},
		{"morgen Nachmittag um 4", "2026-11-07T16:00"},
		{"Sonntag um 10", "2026-11-08T10:00"},
		{"in 2 Tagen um 9 Uhr", "2026-11-08T09:00"}, // known prototype bug: the clock time was ignored
		{"um 7 Uhr abends", "2026-11-06T19:00"},
		{"morgen früh um sechs", "2026-11-07T06:00"},
		{"this evening at 8", "2026-11-06T20:00"},
		{"Monday morning at 9:15", "2026-11-09T09:15"},
		{"in einer Woche", "2026-11-13T14:30"}, // known prototype bug: unsupported
		{"at half past six", "2026-11-06T18:30"},
		{"am Mittwoch um dreiviertel acht", "2026-11-11T07:45"},
		{"um Mitternacht", "2026-11-07T00:00"},
		{"in 1 Stunde und 15 Minuten", "2026-11-06T15:45"},
		{"kommenden Freitag um 12", "2026-11-13T12:00"},
	}
	for _, c := range cases {
		checkWhen(t, tpNow, c[0], c[1])
	}
}

// The whenparse prototype's table (now = Thu 2026-11-05 14:30), with the
// expectations that assumed "bare past hour -> tomorrow" updated to the
// earliest-future rule.
func TestParseWhenPrototypeTable(t *testing.T) {
	now := time.Date(2026, 11, 5, 14, 30, 0, 0, berlin)
	cases := [][2]string{
		{"in 20 Minuten", "2026-11-05T14:50"},
		{"in einer halben Stunde", "2026-11-05T15:00"},
		{"in anderthalb Stunden", "2026-11-05T16:00"},
		{"morgen um 8", "2026-11-06T08:00"},
		{"tomorrow at 8am", "2026-11-06T08:00"},
		{"übermorgen um halb sieben abends", "2026-11-07T18:30"},
		{"the day after tomorrow at 7:15 pm", "2026-11-07T19:15"},
		{"heute Abend um 9", "2026-11-05T21:00"},
		{"next Monday at 10", "2026-11-09T10:00"},
		{"am Freitag um viertel nach drei", "2026-11-06T15:15"},
		{"in 2 hours", "2026-11-05T16:30"},
		{"um 17 Uhr", "2026-11-05T17:00"},
		{"Sunday at noon", "2026-11-08T12:00"},
		{"in drei Tagen um 9", "2026-11-08T09:00"},
		{"am 24. Dezember um 18 Uhr", "2026-12-24T18:00"},
		{"um viertel vor acht", "2026-11-05T19:45"},
		{"in 90 seconds", "2026-11-05T14:31"},
		{"Hermes, remind me in 20 minutes to take the pizza out", "2026-11-05T14:50"},
		{"Erinnere mich in 20 Minuten daran, die Pizza rauszuholen", "2026-11-05T14:50"},
		{"Stell einen Timer auf 5 Minuten", "2026-11-05T14:35"},
		{"Hermes, Timer 10 Minuten", "2026-11-05T14:40"},
		{"set a 10 minute timer", "2026-11-05T14:40"},
		{"Erinnere mich morgen um 8 an den Zahnarzt", "2026-11-06T08:00"},
		{"remind me the day after tomorrow at 7:15 pm to water the plants", "2026-11-07T19:15"},
		{"Erinnere mich in einer halben Stunde, die Wäsche aufzuhängen", "2026-11-05T15:00"},
		{"Trag für Freitag um 15 Uhr einen Termin beim Friseur ein", "2026-11-06T15:00"},
		{"Add a meeting with Tom next Monday at 10", "2026-11-09T10:00"},
		{"erinnere mich um 8 an die Tabletten", "2026-11-05T20:00"},
		{"morgen früh um 7 joggen", "2026-11-06T07:00"},
		{"morgen abend um 7", "2026-11-06T19:00"},
		{"am Freitag um 3", "2026-11-06T15:00"},
		{"remind me at 9 to call Tom", "2026-11-05T21:00"},
		{"in einer Stunde", "2026-11-05T15:30"},
		{"in an hour", "2026-11-05T15:30"},
		{"in 1 Stunde und 30 Minuten", "2026-11-05T16:00"},
		{"am 3.12. um 10 Uhr", "2026-12-03T10:00"},
		{"on December 24th at 6pm", "2026-12-24T18:00"},
		{"um 8 Uhr 30", "2026-11-05T20:30"},
		{"morgen", "2026-11-06T09:00"},
		{"am Donnerstag um 10", "2026-11-12T10:00"},
		{"um Mitternacht", "2026-11-06T00:00"},
		{"um 14:45", "2026-11-05T14:45"},
		{"um 14:15", "2026-11-06T14:15"},
		{"um dreiviertel acht", "2026-11-05T19:45"},
		{"at quarter past five", "2026-11-05T17:15"},
		{"tonight at 10", "2026-11-05T22:00"},
		{"tomorrow morning at 6:30", "2026-11-06T06:30"},
		{"in zwei Wochen", "2026-11-19T14:30"},
	}
	for _, c := range cases {
		checkWhen(t, now, c[0], c[1])
	}
}

func TestParseWhenTwelveHourRule(t *testing.T) {
	early := time.Date(2026, 11, 6, 6, 0, 0, 0, berlin)
	late := time.Date(2026, 11, 6, 23, 0, 0, 0, berlin)
	cases := []struct {
		now       time.Time
		in, want  string
		clock     bool
		defaulted bool
	}{
		{tpNow, "um 7", "2026-11-06T19:00", true, false},
		{early, "um 7", "2026-11-06T07:00", true, false},
		{late, "um 7", "2026-11-07T07:00", true, false},
		{tpNow, "um halb acht", "2026-11-06T19:30", true, false},
		{early, "um halb acht", "2026-11-06T07:30", true, false},
		{tpNow, "um Viertel vor sieben", "2026-11-06T18:45", true, false},
		{tpNow, "um 7:00", "2026-11-06T19:00", true, false},
		{tpNow, "um 07:00", "2026-11-07T07:00", true, false}, // a leading zero is 24 h
		{tpNow, "um 19 Uhr", "2026-11-06T19:00", true, false},
		{tpNow, "um 12", "2026-11-07T00:00", true, false},
		{early, "um 12", "2026-11-06T12:00", true, false},
		{tpNow, "um 0 Uhr", "2026-11-07T00:00", true, false},
		{tpNow, "um 24 Uhr", "2026-11-07T00:00", true, false},
		{tpNow, "heute um 8", "2026-11-06T20:00", true, false},
		{late, "heute um 8", "", false, false}, // both readings are over
		{tpNow, "um 7 Uhr morgens", "2026-11-07T07:00", true, false},
		{tpNow, "um 1 mittags", "2026-11-07T13:00", true, false},
		{tpNow, "um 10 nachts", "2026-11-06T22:00", true, false},
		{tpNow, "heute Nacht um 2", "2026-11-07T02:00", true, false},
		{tpNow, "Samstagnacht um 2", "2026-11-08T02:00", true, false},
		{tpNow, "7pm", "2026-11-06T19:00", true, false},
		{tpNow, "8 Uhr", "2026-11-06T20:00", true, false},
		{tpNow, "um acht Uhr dreißig", "2026-11-06T20:30", true, false},
		{tpNow, "fünf vor halb neun", "2026-11-06T20:25", true, false},
		{tpNow, "zehn nach acht", "2026-11-06T20:10", true, false},
		{tpNow, "quarter to seven", "2026-11-06T18:45", true, false},
		{tpNow, "am Freitag um 3", "2026-11-13T15:00", true, false}, // afternoon on a later day
		{tpNow, "weck mich morgen um 5", "2026-11-07T05:00", true, false},
		{tpNow, "morgen um 7", "2026-11-07T07:00", true, false},
		{tpNow, "um 8 am Freitag", "2026-11-13T08:00", true, false}, // German "am", not a.m.
		{tpNow, "at 8 a.m. on Monday", "2026-11-09T08:00", true, false},
		// a day without a clock: 09:00, said explicitly
		{tpNow, "morgen", "2026-11-07T09:00", false, true},
		{tpNow, "am Freitag", "2026-11-13T09:00", false, true},
		{tpNow, "next Monday", "2026-11-09T09:00", false, true},
		{tpNow, "am 24.12.", "2026-12-24T09:00", false, true},
		{tpNow, "nächste Woche", "2026-11-09T09:00", false, true},
		{tpNow, "am Wochenende", "2026-11-07T09:00", false, true},
		// day parts without a clock
		{tpNow, "übermorgen früh", "2026-11-08T08:00", false, true},
		{tpNow, "heute Abend", "2026-11-06T19:00", false, true},
		{tpNow, "morgen Nachmittag", "2026-11-07T15:00", false, true},
		{tpNow, "Freitagabend", "2026-11-13T19:00", false, true},
		{tpNow, "tonight", "2026-11-06T19:00", false, true},
		{tpNow, "abends", "2026-11-06T19:00", false, true},
		{tpNow, "am Morgen", "2026-11-07T08:00", false, true},
		{tpNow, "mittags", "2026-11-07T12:00", true, false},
	}
	for _, c := range cases {
		r := checkWhen(t, c.now, c.in, c.want)
		if c.want != "" && (r.HasClock != c.clock || r.DefaultedClock != c.defaulted) {
			t.Errorf("%q: HasClock=%v DefaultedClock=%v, want %v/%v", c.in, r.HasClock, r.DefaultedClock, c.clock, c.defaulted)
		}
	}
}

func TestParseWhenDurationsAndDays(t *testing.T) {
	cases := []struct {
		in, want string
		dur      time.Duration
		hasDay   bool
	}{
		{"in 20 Minuten", "2026-11-06T14:50", 20 * time.Minute, false},
		{"in ner halben stunde", "2026-11-06T15:00", 30 * time.Minute, false},
		{"in einer Viertelstunde", "2026-11-06T14:45", 15 * time.Minute, false},
		{"in a quarter of an hour", "2026-11-06T14:45", 15 * time.Minute, false},
		{"in half an hour", "2026-11-06T15:00", 30 * time.Minute, false},
		{"in an hour and a half", "2026-11-06T16:00", 90 * time.Minute, false},
		{"in 1,5 Stunden", "2026-11-06T16:00", 90 * time.Minute, false},
		{"in zweieinhalb Stunden", "2026-11-06T17:00", 150 * time.Minute, false},
		{"in 2 Stunden 30 Minuten", "2026-11-06T17:00", 150 * time.Minute, false},
		{"in fünfundvierzig Minuten", "2026-11-06T15:15", 45 * time.Minute, false},
		{"in twenty five minutes", "2026-11-06T14:55", 25 * time.Minute, false},
		{"in 90 Sekunden", "2026-11-06T14:31", 90 * time.Second, false},
		{"in a minute", "2026-11-06T14:31", time.Minute, false},
		{"nach einer Stunde", "2026-11-06T15:30", time.Hour, false},
		{"in 3 Tagen", "2026-11-09T14:30", 72 * time.Hour, true},
		{"in einer Woche", "2026-11-13T14:30", 7 * 24 * time.Hour, true},
		{"in einem Monat", "2026-12-06T14:30", 30 * 24 * time.Hour, true},
		{"in 2 Tagen abends", "2026-11-08T19:00", 0, true},
		// bare durations: timers and short answers only
		{"Timer 10 Minuten", "2026-11-06T14:40", 10 * time.Minute, false},
		{"zehn Minuten Timer", "2026-11-06T14:40", 10 * time.Minute, false},
		{"set a timer for 5 minutes", "2026-11-06T14:35", 5 * time.Minute, false},
		{"10 Minuten", "2026-11-06T14:40", 10 * time.Minute, false},
		{"eine halbe Stunde bitte", "2026-11-06T15:00", 30 * time.Minute, false},
		{"Ich habe 3 Tage gewartet und war traurig", "", 0, false},
	}
	for _, c := range cases {
		r := checkWhen(t, tpNow, c.in, c.want)
		if c.want == "" {
			continue
		}
		if r.Duration != c.dur || r.HasDay != c.hasDay {
			t.Errorf("%q: Duration=%v HasDay=%v, want %v/%v", c.in, r.Duration, r.HasDay, c.dur, c.hasDay)
		}
	}
}

func TestParseWhenRest(t *testing.T) {
	cases := [][2]string{
		{"Hermes, erinnere mich in 20 Minuten daran, die Pizza rauszunehmen", "Hermes, erinnere mich daran, die Pizza rauszunehmen"},
		{"Remind me in 20 minutes to take the pizza out", "Remind me to take the pizza out"},
		{"Erinnere mich morgen um 8 an den Zahnarzt", "Erinnere mich an den Zahnarzt"},
		{"Erinner mich an die Pizza in 20 Minuten", "Erinner mich an die Pizza"},
		{"hermes erinner mich in ner halben stunde an die wäsche", "hermes erinner mich an die wäsche"},
		{"am 24.12. um 18 Uhr Bescherung", "Bescherung"},
		{"Add a dentist appointment on November 20th at 10am", "Add a dentist appointment"},
		{"Übermorgen früh Müll rausstellen", "Müll rausstellen"},
		{"remind me tomorrow at 7pm to call mum", "remind me to call mum"},
	}
	for _, c := range cases {
		r, ok := parseWhen(c[0], tpNow, berlin)
		if !ok || r.Rest != c[1] {
			t.Errorf("%q: rest %q (ok=%v), want %q", c[0], r.Rest, ok, c[1])
		}
	}
	if r, ok := parseWhen("Wie geht es dir?", tpNow, berlin); ok || r.Rest != "Wie geht es dir" {
		t.Errorf("no time: ok=%v rest=%q", ok, r.Rest)
	}
}

func TestParseWhenNoTime(t *testing.T) {
	for _, s := range []string{
		"Guten Morgen Hermes", "Good morning, Hermes", "Gute Nacht", "Guten Abend", "Wie geht es dir?",
		"Setz Milch auf die Einkaufsliste", "Ich habe 3 Katzen", "Tell me a joke", "Mach das Licht im Wohnzimmer an",
		"um ein bisschen Ruhe", "at one point I was there", "Achtung, um achtung", "Der Abend war schön",
		"Es ist zu früh", "", "   ", "Erinner mich an die Pizza",
	} {
		if r, ok := parseWhen(s, tpNow, berlin); ok {
			t.Errorf("%q: unexpected time %v", s, r.At)
		}
	}
}

func TestParseWhenBounds(t *testing.T) {
	checkWhen(t, tpNow, "am 1. November", "2027-11-01T09:00") // next occurrence, < 366 days
	checkWhen(t, tpNow, "am 6.11.2025 um 10 Uhr", "")         // past
	checkWhen(t, tpNow, "in 400 Tagen", "")                   // too far
	checkWhen(t, tpNow, "am 31.2.", "")                       // no such day
	checkWhen(t, tpNow, "am 24.12.2026 um 18 Uhr", "2026-12-24T18:00")
	checkWhen(t, tpNow, "am 24.12.26", "2026-12-24T09:00")
	checkWhen(t, tpNow, "heute um 14:30", "") // not after now
	checkWhen(t, tpNow, "in 0 Minuten", "")
	long := strings.Repeat("bla ", 2000) + "in 20 Minuten"
	if _, ok := parseWhen(long, tpNow, berlin); ok {
		t.Error("input beyond the cap must not be read")
	}
	if _, ok := parseWhen("in 20 Minuten", tpNow, nil); !ok {
		t.Error("nil location falls back to now's location")
	}
}

func TestParseWhenDST(t *testing.T) {
	// Autumn: Sunday 25 October 2026, 03:00 CEST -> 02:00 CET.
	autumn := time.Date(2026, 10, 24, 14, 30, 0, 0, berlin)
	r := checkWhen(t, autumn, "morgen um 8", "2026-10-25T08:00")
	if _, off := r.At.Zone(); off != 3600 {
		t.Errorf("after the switch the offset is +01:00, got %d", off)
	}
	checkWhen(t, autumn, "in 24 Stunden", "2026-10-25T13:30") // absolute hours
	checkWhen(t, autumn, "in einem Tag", "2026-10-25T14:30")  // calendar day
	checkWhen(t, autumn, "am Montag um 9", "2026-10-26T09:00")
	// Spring: Sunday 28 March 2027, 02:00 CET -> 03:00 CEST.
	spring := time.Date(2027, 3, 27, 14, 30, 0, 0, berlin)
	r = checkWhen(t, spring, "morgen um 8", "2027-03-28T08:00")
	if _, off := r.At.Zone(); off != 7200 {
		t.Errorf("after the switch the offset is +02:00, got %d", off)
	}
	checkWhen(t, spring, "in 24 Stunden", "2027-03-28T15:30")
	if r, ok := parseWhen("morgen um 2:30", spring, berlin); !ok || !r.At.After(spring) {
		t.Errorf("a non-existent local time still resolves: %v %v", r.At, ok)
	}
	if got := formatWhen(time.Date(2026, 10, 25, 23, 30, 0, 0, berlin), autumn, "de"); got != "morgen (Sonntag) um 23:30 Uhr" {
		t.Errorf("formatWhen across DST: %q", got)
	}
}

func TestParseWhenOtherZone(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	now := time.Date(2026, 11, 6, 14, 30, 0, 0, ny)
	r, ok := parseWhen("tomorrow at 8am", now.UTC(), ny)
	if !ok || r.At.Format(tpLayout) != "2026-11-07T08:00" || r.At.Location() != ny {
		t.Errorf("New York: %v %v", r.At, ok)
	}
}

func TestParseDayRange(t *testing.T) {
	cases := []struct{ in, from, to, label string }{
		{"heute", "2026-11-06", "2026-11-07", "heute"},
		{"Was habe ich morgen?", "2026-11-07", "2026-11-08", "morgen"},
		{"übermorgen", "2026-11-08", "2026-11-09", "übermorgen"},
		{"am Freitag", "2026-11-06", "2026-11-07", "am Freitag"}, // today is Friday
		{"nächsten Freitag", "2026-11-13", "2026-11-14", "nächsten Freitag"},
		{"am Montag", "2026-11-09", "2026-11-10", "am Montag"},
		{"diese Woche", "2026-11-06", "2026-11-09", "diese Woche"},
		{"nächste Woche", "2026-11-09", "2026-11-16", "nächste Woche"},
		{"next week", "2026-11-09", "2026-11-16", "next week"},
		{"am Wochenende", "2026-11-07", "2026-11-09", "am Wochenende"},
		{"this weekend", "2026-11-07", "2026-11-09", "this weekend"},
		{"gestern", "2026-11-05", "2026-11-06", "gestern"},
		{"in 3 Tagen", "2026-11-09", "2026-11-10", "in 3 Tagen"},
		{"am 24.12.", "2026-12-24", "2026-12-25", "am 24.12."},
		{"tomorrow", "2026-11-07", "2026-11-08", "tomorrow"},
		{"heute Morgen", "2026-11-06", "2026-11-07", "heute Morgen"},
	}
	for _, c := range cases {
		from, to, label, ok := parseDayRange(c.in, tpNow, berlin)
		if !ok || from.Format("2006-01-02") != c.from || to.Format("2006-01-02") != c.to || label != c.label {
			t.Errorf("%q: %s..%s %q ok=%v; want %s..%s %q", c.in, from.Format("2006-01-02"), to.Format("2006-01-02"), label, ok, c.from, c.to, c.label)
		}
		if ok && (from.Hour() != 0 || from.Location() != berlin) {
			t.Errorf("%q: from must be local midnight, got %v", c.in, from)
		}
	}
	for _, s := range []string{"Guten Morgen", "Wie geht es dir?", ""} {
		if _, _, _, ok := parseDayRange(s, tpNow, berlin); ok {
			t.Errorf("%q: unexpected range", s)
		}
	}
	// On a Sunday "this weekend" starts today.
	sun := time.Date(2026, 11, 8, 10, 0, 0, 0, berlin)
	if from, to, _, ok := parseDayRange("am Wochenende", sun, berlin); !ok || from.Day() != 8 || to.Day() != 9 {
		t.Errorf("Sunday weekend: %v..%v", from, to)
	}
}

func TestFormatWhenAndDuration(t *testing.T) {
	cases := []struct {
		at         time.Time
		lang, want string
	}{
		{time.Date(2026, 11, 6, 14, 50, 0, 0, berlin), "de", "heute um 14:50 Uhr"},
		{time.Date(2026, 11, 7, 8, 30, 0, 0, berlin), "de", "morgen (Samstag) um 8:30 Uhr"},
		{time.Date(2026, 11, 9, 7, 0, 0, 0, berlin), "de", "am Montag, 9. November, um 7 Uhr"},
		{time.Date(2026, 11, 6, 14, 50, 0, 0, berlin), "en", "today at 2:50 PM"},
		{time.Date(2026, 11, 7, 8, 30, 0, 0, berlin), "en", "tomorrow (Saturday) at 8:30 AM"},
		{time.Date(2026, 11, 9, 7, 0, 0, 0, berlin), "en", "on Monday, November 9, at 7:00 AM"},
		{time.Date(2027, 10, 1, 9, 0, 0, 0, berlin), "de", "am Freitag, 1. Oktober 2027, um 9 Uhr"},
		{time.Date(2026, 11, 6, 13, 50, 0, 0, time.UTC), "de", "heute um 14:50 Uhr"}, // rendered in now's zone
	}
	for _, c := range cases {
		if got := formatWhen(c.at, tpNow, c.lang); got != c.want {
			t.Errorf("formatWhen(%v, %s) = %q, want %q", c.at, c.lang, got, c.want)
		}
	}
	durs := []struct {
		d          time.Duration
		lang, want string
	}{
		{20 * time.Minute, "de", "20 Minuten"},
		{90 * time.Minute, "de", "1 Stunde 30 Minuten"},
		{20 * time.Minute, "en", "20 minutes"},
		{time.Minute, "en", "1 minute"},
		{0, "de", "0 Sekunden"},
		{26 * time.Hour, "de", "1 Tag 2 Stunden"},
		{-5 * time.Second, "en", "5 seconds"},
	}
	for _, c := range durs {
		if got := formatDuration(c.d, c.lang); got != c.want {
			t.Errorf("formatDuration(%v, %s) = %q, want %q", c.d, c.lang, got, c.want)
		}
	}
}

func TestTidyReminderText(t *testing.T) {
	cases := [][2]string{
		{"an die Pizza", "die Pizza"},
		{"daran, die Pizza rauszunehmen", "die Pizza rausnehmen"},
		{"dran, Mama anzurufen", "Mama anrufen"},
		{"das Paket abzuholen", "das Paket abholen"},
		{"einzukaufen", "einkaufen"},
		{"daran, das Zimmer aufzuräumen", "das Zimmer aufräumen"},
		{"den Schirm mitzunehmen", "den Schirm mitnehmen"},
		{"den Müll rauszubringen", "den Müll rausbringen"},
		{"das Licht auszuschalten", "das Licht ausschalten"},
		{"Tom zurückzurufen", "Tom zurückrufen"},
		{"das Essen vorzubereiten", "das Essen vorbereiten"},
		{"die Blumen zu gießen", "die Blumen gießen"},
		{"die Rechnung zu bezahlen", "die Rechnung bezahlen"},
		{"das Fenster zuzumachen", "das Fenster zumachen"},
		{"dass ich Milch kaufen muss", "Milch kaufen"},
		{"to call mum", "call mum"},
		{"about the meeting", "the meeting"},
		{"that I need to water the plants", "water the plants"},
		{"  , an den Zahnarzt.", "den Zahnarzt"},
		{"", ""},
	}
	for _, c := range cases {
		if got := tidyReminderText(c[0], "de"); got != c[1] {
			t.Errorf("tidyReminderText(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestWhenDayPartAndRecurring(t *testing.T) {
	for in, want := range map[string]string{
		"nein, abends": partEvening, "no, in the evening": partEvening, "nein, morgens": partMorning,
		"lieber nachmittags": partAfternoon, "nein, am Morgen": partMorning, "nachts": partNight,
	} {
		if got, ok := whenDayPart(in); !ok || got != want {
			t.Errorf("whenDayPart(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	if _, ok := whenDayPart("Guten Abend"); ok {
		t.Error("a greeting is no day part")
	}
	for _, s := range []string{"jeden Montag um 9", "every day at 8", "jede Stunde", "immer wenn ich heimkomme", "every 2 hours", "stündlich"} {
		if !whenRecurring(s) {
			t.Errorf("whenRecurring(%q)", s)
		}
	}
	for _, s := range []string{"am Montag um 9", "dass jeder Gast etwas mitbringt", "the daily standup", "remind me to call everyone"} {
		if whenRecurring(s) {
			t.Errorf("whenRecurring(%q) is no repeat", s)
		}
	}
}

func BenchmarkParseWhen(b *testing.B) {
	for i := 0; i < b.N; i++ {
		parseWhen("Hermes, erinnere mich übermorgen um halb neun an den Müll", tpNow, berlin)
	}
}

// ---------------------------------------------------------------------------
// Recurring rules

func TestParseRecurringTable(t *testing.T) {
	cases := []struct {
		in        string
		freq      string
		interval  int
		weekdays  string // fmt of Weekdays
		monthDay  int
		month     int
		clock     string // HH:MM
		first     string // tpLayout
		rest      string
		de, en    string // Describe
		defaulted bool
	}{
		// German
		{"erinnere mich jeden Montag um 8 an den Müll", "weekly", 1, "[1]", 0, 0, "08:00", "2026-11-09T08:00", "erinnere mich an den Müll",
			"jeden Montag um 8 Uhr", "every Monday at 8:00 AM", false},
		{"jeden Tag um 7 Uhr", "daily", 1, "[]", 0, 0, "07:00", "2026-11-07T07:00", "", "jeden Tag um 7 Uhr", "every day at 7:00 AM", false},
		{"täglich um 22 Uhr Tabletten", "daily", 1, "[]", 0, 0, "22:00", "2026-11-06T22:00", "Tabletten", "jeden Tag um 22 Uhr", "every day at 10:00 PM", false},
		{"jeden Werktag", "weekly", 1, "[1 2 3 4 5]", 0, 0, "09:00", "2026-11-09T09:00", "", "jeden Werktag um 9 Uhr", "every weekday at 9:00 AM", true},
		{"jedes Wochenende", "weekly", 1, "[6]", 0, 0, "09:00", "2026-11-07T09:00", "", "jeden Samstag um 9 Uhr", "every Saturday at 9:00 AM", true},
		{"alle zwei Wochen am Freitag", "weekly", 2, "[5]", 0, 0, "09:00", "2026-11-13T09:00", "", "jeden zweiten Freitag um 9 Uhr", "every other Friday at 9:00 AM", true},
		{"jeden Monat am 1.", "monthly", 1, "[]", 1, 0, "09:00", "2026-12-01T09:00", "", "jeden Monat am 1. um 9 Uhr", "on the 1st of every month at 9:00 AM", true},
		{"jedes Jahr am 24.12.", "yearly", 1, "[]", 24, 12, "09:00", "2026-12-24T09:00", "", "jedes Jahr am 24. Dezember um 9 Uhr", "every year on December 24 at 9:00 AM", true},
		{"montags und donnerstags um 18 Uhr Sport", "weekly", 1, "[1 4]", 0, 0, "18:00", "2026-11-09T18:00", "Sport",
			"jeden Montag und Donnerstag um 18 Uhr", "every Monday and Thursday at 6:00 PM", false},
		{"jeden Abend die Blumen gießen", "daily", 1, "[]", 0, 0, "19:00", "2026-11-06T19:00", "die Blumen gießen", "jeden Tag um 19 Uhr", "every day at 7:00 PM", true},
		{"jeden zweiten Tag um halb acht", "daily", 2, "[]", 0, 0, "07:30", "2026-11-07T07:30", "", "jeden zweiten Tag um 7:30 Uhr", "every other day at 7:30 AM", false},
		{"jeden Freitag um 3", "weekly", 1, "[5]", 0, 0, "15:00", "2026-11-06T15:00", "", "jeden Freitag um 15 Uhr", "every Friday at 3:00 PM", false},
		{"weck mich jeden Tag um 6", "daily", 1, "[]", 0, 0, "06:00", "2026-11-07T06:00", "weck mich", "jeden Tag um 6 Uhr", "every day at 6:00 AM", false},
		{"jeden Monat am 31.", "monthly", 1, "[]", 31, 0, "09:00", "2026-11-30T09:00", "", "jeden Monat am Monatsletzten um 9 Uhr", "on the last day of every month at 9:00 AM", true},
		{"am Ersten jedes Monats Miete zahlen", "monthly", 1, "[]", 1, 0, "09:00", "2026-12-01T09:00", "Miete zahlen", "jeden Monat am 1. um 9 Uhr", "on the 1st of every month at 9:00 AM", true},
		{"jeden 24.12. um 18 Uhr", "yearly", 1, "[]", 24, 12, "18:00", "2026-12-24T18:00", "", "jedes Jahr am 24. Dezember um 18 Uhr", "every year on December 24 at 6:00 PM", false},
		{"jeden Tag ab Montag um 9", "daily", 1, "[]", 0, 0, "09:00", "2026-11-09T09:00", "", "jeden Tag um 9 Uhr", "every day at 9:00 AM", false},
		{"jeden Montag, Mittwoch und Freitag um 7:30", "weekly", 1, "[1 3 5]", 0, 0, "07:30", "2026-11-09T07:30", "",
			"jeden Montag, Mittwoch und Freitag um 7:30 Uhr", "every Monday, Wednesday and Friday at 7:30 AM", false},
		{"alle 3 Monate am 15.", "monthly", 3, "[]", 15, 0, "09:00", "2026-11-15T09:00", "", "alle 3 Monate am 15. um 9 Uhr", "every 3 months on the 15th at 9:00 AM", true},
		// English
		{"remind me every Monday at 8am to take out the trash", "weekly", 1, "[1]", 0, 0, "08:00", "2026-11-09T08:00", "remind me to take out the trash",
			"jeden Montag um 8 Uhr", "every Monday at 8:00 AM", false},
		{"every day at 7", "daily", 1, "[]", 0, 0, "07:00", "2026-11-07T07:00", "", "jeden Tag um 7 Uhr", "every day at 7:00 AM", false},
		{"take my pills daily", "daily", 1, "[]", 0, 0, "09:00", "2026-11-07T09:00", "take my pills", "jeden Tag um 9 Uhr", "every day at 9:00 AM", true},
		{"daily at 10pm", "daily", 1, "[]", 0, 0, "22:00", "2026-11-06T22:00", "", "jeden Tag um 22 Uhr", "every day at 10:00 PM", false},
		{"every weekday", "weekly", 1, "[1 2 3 4 5]", 0, 0, "09:00", "2026-11-09T09:00", "", "jeden Werktag um 9 Uhr", "every weekday at 9:00 AM", true},
		{"every other Friday", "weekly", 2, "[5]", 0, 0, "09:00", "2026-11-13T09:00", "", "jeden zweiten Freitag um 9 Uhr", "every other Friday at 9:00 AM", true},
		{"on the 1st of every month", "monthly", 1, "[]", 1, 0, "09:00", "2026-12-01T09:00", "", "jeden Monat am 1. um 9 Uhr", "on the 1st of every month at 9:00 AM", true},
		{"every year on December 24", "yearly", 1, "[]", 24, 12, "09:00", "2026-12-24T09:00", "", "jedes Jahr am 24. Dezember um 9 Uhr", "every year on December 24 at 9:00 AM", true},
		{"water the plants every morning", "daily", 1, "[]", 0, 0, "08:00", "2026-11-07T08:00", "water the plants", "jeden Tag um 8 Uhr", "every day at 8:00 AM", true},
		{"on Mondays and Thursdays at 6pm gym", "weekly", 1, "[1 4]", 0, 0, "18:00", "2026-11-09T18:00", "gym",
			"jeden Montag und Donnerstag um 18 Uhr", "every Monday and Thursday at 6:00 PM", false},
		{"every 3 days", "daily", 3, "[]", 0, 0, "09:00", "2026-11-07T09:00", "", "alle 3 Tage um 9 Uhr", "every 3 days at 9:00 AM", true},
		{"biweekly on Friday at 5pm", "weekly", 2, "[5]", 0, 0, "17:00", "2026-11-06T17:00", "", "jeden zweiten Freitag um 17 Uhr", "every other Friday at 5:00 PM", false},
		{"every December 24th at noon", "yearly", 1, "[]", 24, 12, "12:00", "2026-12-24T12:00", "", "jedes Jahr am 24. Dezember um 12 Uhr", "every year on December 24 at 12:00 PM", false},
		{"every month on the 15th", "monthly", 1, "[]", 15, 0, "09:00", "2026-11-15T09:00", "", "jeden Monat am 15. um 9 Uhr", "on the 15th of every month at 9:00 AM", true},
		{"weekly on Tuesday at 9:15", "weekly", 1, "[2]", 0, 0, "09:15", "2026-11-10T09:15", "", "jeden Dienstag um 9:15 Uhr", "every Tuesday at 9:15 AM", false},
	}
	for _, c := range cases {
		r, ok := parseRecurring(c.in, tpNow, berlin)
		if !ok {
			t.Errorf("%q: not recognised", c.in)
			continue
		}
		rr := r.Rule
		got := fmt.Sprintf("%s/%d/%v/%d/%d/%02d:%02d", rr.Freq, rr.Interval, rr.Weekdays, rr.MonthDay, rr.Month, rr.Hour, rr.Minute)
		want := fmt.Sprintf("%s/%d/%s/%d/%d/%s", c.freq, c.interval, c.weekdays, c.monthDay, c.month, c.clock)
		if got != want {
			t.Errorf("%q: rule %s, want %s", c.in, got, want)
		}
		if f := r.First.In(berlin).Format(tpLayout); f != c.first {
			t.Errorf("%q: first %s, want %s", c.in, f, c.first)
		}
		if r.Rest != c.rest || r.DefaultedClock != c.defaulted {
			t.Errorf("%q: rest %q defaulted %v, want %q %v", c.in, r.Rest, r.DefaultedClock, c.rest, c.defaulted)
		}
		if d := rr.Describe("de"); d != c.de {
			t.Errorf("%q: DE %q, want %q", c.in, d, c.de)
		}
		if d := rr.Describe("en"); d != c.en {
			t.Errorf("%q: EN %q, want %q", c.in, d, c.en)
		}
		if rr.TZ != "Europe/Berlin" || rr.Start != r.First.In(berlin).Format(time.DateOnly) {
			t.Errorf("%q: tz %q start %q", c.in, rr.TZ, rr.Start)
		}
	}
}

func TestParseRecurringRejectsOneOffs(t *testing.T) {
	for _, s := range []string{
		"erinnere mich morgen um 8", "am Montag um 9", "Erinnere mich morgen, dass jeder Gast etwas mitbringt",
		"on the weekend", "am Wochenende", "next Monday", "remind me to call everyone at 5", "am Ersten des Monats",
		"in 20 Minuten", "nächste Woche", "jeden", "every", "Guten Morgen", "",
		"remind me at 9 about the daily standup", "erinnere mich morgen an das Monthly Meeting", "the weekly report at 5",
	} {
		if r, ok := parseRecurring(s, tpNow, berlin); ok {
			t.Errorf("%q read as recurring: %+v", s, r.Rule)
		}
	}
}

func TestRecurNextDST(t *testing.T) {
	daily := RecurRule{Freq: recurDaily, Interval: 1, Hour: 8, TZ: "Europe/Berlin"}
	// Autumn 2026 (25 Oct) and spring 2027 (28 Mar): always 8:00 local.
	for _, from := range []time.Time{time.Date(2026, 10, 23, 9, 0, 0, 0, berlin), time.Date(2027, 3, 26, 9, 0, 0, 0, berlin)} {
		at := from
		for i := 0; i < 4; i++ {
			next, ok := daily.Next(at, nil)
			if !ok || next.In(berlin).Hour() != 8 || next.In(berlin).Minute() != 0 || calendarDaysBetween(at.In(berlin), next) != 1 {
				t.Fatalf("daily after %s: %s", at, next.In(berlin))
			}
			at = next
		}
	}
	// 25 hours between two 8:00 occurrences across the autumn change.
	a, _ := daily.Next(time.Date(2026, 10, 24, 7, 0, 0, 0, berlin), nil)
	if b, _ := daily.Next(a, nil); b.Sub(a) != 25*time.Hour {
		t.Errorf("autumn gap %v", b.Sub(a))
	}
	// A clock time that does not exist (2:30 on 28 March 2027) still fires
	// once that day, and the next day is back at 2:30.
	night := RecurRule{Freq: recurDaily, Interval: 1, Hour: 2, Minute: 30, TZ: "Europe/Berlin"}
	n1, _ := night.Next(time.Date(2027, 3, 27, 12, 0, 0, 0, berlin), nil)
	n2, _ := night.Next(n1, nil)
	if n1.In(berlin).Day() != 28 || n2.In(berlin).Format("02 15:04") != "29 02:30" {
		t.Errorf("spring gap: %s then %s", n1.In(berlin), n2.In(berlin))
	}
	// A time that exists twice (2:30 on 25 October 2026) fires once.
	n3, _ := night.Next(time.Date(2026, 10, 24, 12, 0, 0, 0, berlin), nil)
	n4, _ := night.Next(n3, nil)
	if n3.In(berlin).Day() != 25 || n4.In(berlin).Format("02 15:04") != "26 02:30" {
		t.Errorf("autumn double hour: %s then %s", n3.In(berlin), n4.In(berlin))
	}
	// The rule's own zone wins over the household zone.
	ny := RecurRule{Freq: recurDaily, Interval: 1, Hour: 7, TZ: "America/New_York"}
	n5, _ := ny.Next(tpNow, berlin)
	if n5.In(time.UTC).Format("15:04") != "12:00" {
		t.Errorf("rule zone: %s", n5.UTC())
	}
}

func TestRecurNextMonthEndsAndIntervals(t *testing.T) {
	seq := func(rr RecurRule, from time.Time, n int) []string {
		var out []string
		at := from
		for i := 0; i < n; i++ {
			next, ok := rr.Next(at, berlin)
			if !ok {
				out = append(out, "!")
				break
			}
			out = append(out, next.In(berlin).Format("2006-01-02"))
			at = next
		}
		return out
	}
	check := func(name string, got []string, want ...string) {
		t.Helper()
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s: %v\nwant %v", name, got, want)
		}
	}
	check("monthly 31", seq(RecurRule{Freq: recurMonthly, MonthDay: 31, Hour: 9}, tpNow, 5),
		"2026-11-30", "2026-12-31", "2027-01-31", "2027-02-28", "2027-03-31")
	check("monthly 30 in a leap year", seq(RecurRule{Freq: recurMonthly, MonthDay: 30, Hour: 9}, time.Date(2028, 1, 31, 0, 0, 0, 0, berlin), 2),
		"2028-02-29", "2028-03-30")
	check("yearly Feb 29", seq(RecurRule{Freq: recurYearly, Month: 2, MonthDay: 29, Hour: 9}, tpNow, 3),
		"2027-02-28", "2028-02-29", "2029-02-28")
	check("yearly Feb 30 is the last day", seq(RecurRule{Freq: recurYearly, Month: 2, MonthDay: 30, Hour: 9}, tpNow, 1), "2027-02-28")
	check("every other month from the start", seq(RecurRule{Freq: recurMonthly, Interval: 2, MonthDay: 15, Hour: 9, Start: "2026-12-15"}, tpNow, 3),
		"2026-12-15", "2027-02-15", "2027-04-15")
	check("every other week, two days", seq(RecurRule{Freq: recurWeekly, Interval: 2, Weekdays: []int{1, 4}, Hour: 9, Start: "2026-11-09"}, tpNow, 5),
		"2026-11-09", "2026-11-12", "2026-11-23", "2026-11-26", "2026-12-07")
	check("every 3 days", seq(RecurRule{Freq: recurDaily, Interval: 3, Hour: 9, Start: "2026-11-07"}, tpNow, 3),
		"2026-11-07", "2026-11-10", "2026-11-13")
	check("never before start", seq(RecurRule{Freq: recurDaily, Hour: 9, Start: "2027-01-01"}, tpNow, 2), "2027-01-01", "2027-01-02")
	check("every 2 years", seq(RecurRule{Freq: recurYearly, Interval: 2, Month: 12, MonthDay: 24, Hour: 9, Start: "2026-12-24"}, tpNow, 2),
		"2026-12-24", "2028-12-24")
	// Same day, later clock: today; earlier clock: next time.
	if n, _ := (RecurRule{Freq: recurWeekly, Weekdays: []int{5}, Hour: 15}).Next(tpNow, berlin); n.In(berlin).Format(tpLayout) != "2026-11-06T15:00" {
		t.Errorf("today later: %s", n.In(berlin))
	}
	if n, _ := (RecurRule{Freq: recurWeekly, Weekdays: []int{5}, Hour: 14, Minute: 30}).Next(tpNow, berlin); n.In(berlin).Format(tpLayout) != "2026-11-13T14:30" {
		t.Errorf("strictly after: %s", n.In(berlin))
	}
}

func TestRecurRuleValidateAndBegin(t *testing.T) {
	bad := []RecurRule{
		{Freq: "hourly", Hour: 9},
		{Freq: recurDaily, Interval: 400},
		{Freq: recurDaily, Interval: -1},
		{Freq: recurDaily, Hour: 24},
		{Freq: recurDaily, Minute: 60},
		{Freq: recurWeekly},
		{Freq: recurWeekly, Weekdays: []int{7}},
		{Freq: recurWeekly, Weekdays: []int{1, 2, 3, 4, 5, 6, 0, 1}},
		{Freq: recurMonthly},
		{Freq: recurMonthly, MonthDay: 32},
		{Freq: recurYearly, MonthDay: 1},
		{Freq: recurYearly, Month: 13, MonthDay: 1},
		{Freq: recurDaily, TZ: "Mars/Olympus"},
		{Freq: recurDaily, TZ: "../../etc/passwd"},
		{Freq: recurDaily, TZ: strings.Repeat("A", 70)},
		{Freq: recurDaily, Start: "tomorrow"},
	}
	for _, rr := range bad {
		if rr.validate() == nil {
			t.Errorf("accepted %+v", rr)
		}
		if _, ok := rr.Next(tpNow, berlin); ok {
			t.Errorf("Next on invalid %+v", rr)
		}
	}
	rr := RecurRule{Freq: recurWeekly, Interval: 2, Weekdays: []int{5, 5, 1}, MonthDay: 3, Month: 4, Hour: 9}
	first, ok := rr.begin(tpNow, time.Time{}, berlin)
	if !ok || first.In(berlin).Format(tpLayout) != "2026-11-09T09:00" || rr.Start != "2026-11-09" {
		t.Fatalf("begin: %s %+v", first, rr)
	}
	if fmt.Sprint(rr.Weekdays) != "[1 5]" || rr.MonthDay != 0 || rr.Month != 0 {
		t.Errorf("normalize: %+v", rr)
	}
	// notBefore bounds the first occurrence.
	rr2 := RecurRule{Freq: recurDaily, Hour: 9}
	if f, _ := rr2.begin(tpNow, time.Date(2026, 12, 1, 0, 0, 0, 0, berlin), berlin); f.In(berlin).Format(tpLayout) != "2026-12-01T09:00" {
		t.Errorf("notBefore: %s", f)
	}
	// clone is deep.
	c := rr.clone()
	c.Weekdays[0] = 3
	if rr.Weekdays[0] != 1 {
		t.Error("clone shares Weekdays")
	}
	if (*RecurRule)(nil).clone() != nil {
		t.Error("nil clone")
	}
}

func TestFormatDay(t *testing.T) {
	for _, c := range []struct {
		at     time.Time
		de, en string
	}{
		{tpNow.Add(time.Hour), "heute", "today"},
		{tpNow.Add(24 * time.Hour), "morgen (Samstag)", "tomorrow (Saturday)"},
		{time.Date(2026, 11, 9, 8, 0, 0, 0, berlin), "am Montag, 9. November", "on Monday, November 9"},
		{time.Date(2027, 6, 1, 8, 0, 0, 0, berlin), "am Dienstag, 1. Juni 2027", "on Tuesday, June 1, 2027"},
	} {
		if got := formatDay(c.at, tpNow, "de"); got != c.de {
			t.Errorf("DE %q, want %q", got, c.de)
		}
		if got := formatDay(c.at, tpNow, "en"); got != c.en {
			t.Errorf("EN %q, want %q", got, c.en)
		}
	}
}

// A day of the month alone is the next such day (today counts); a month
// without it is skipped. Audit regression: "am 24." used to mean today.
func TestParseWhenDayOfMonth(t *testing.T) {
	for _, c := range [][2]string{
		{"am 24. um 18 Uhr", "2026-11-24T18:00"},
		{"on the 24th at 6 pm", "2026-11-24T18:00"},
		{"on the 1st at 9", "2026-12-01T09:00"},
		{"am 6. um 20 Uhr", "2026-11-06T20:00"},
		{"am 31. um 8", "2026-12-31T08:00"},
		{"am 24. Dezember um 18 Uhr", "2026-12-24T18:00"},
		{"am 24.12. um 18 Uhr", "2026-12-24T18:00"},
	} {
		checkWhen(t, tpNow, c[0], c[1])
	}
	if _, ok := nextMonthDay(midnightOf(tpNow), 32); ok {
		t.Fatal("day 32")
	}
}

// FuzzParseWhen: the parsers never panic and stay in range on any input
// (they read every utterance and every API "when").
func FuzzParseWhen(f *testing.F) {
	for _, s := range []string{"in 20 Minuten", "morgen um 7", "jeden Montag um 8", "am 24.12. um 18:30",
		"every other friday at 6 pm", "halb acht", "um 25 uhr", "31.2.", "in 0 sekunden", "nein, abends",
		"alle 3 Wochen am Dienstag und Donnerstag", "on the 31st of every month", "bis 15 Uhr", "12:60", "9999 jahre"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 400 {
			return
		}
		if w, ok := parseWhen(s, tpNow, berlin); ok && (w.At.Before(tpNow) || w.At.Sub(tpNow) > whenMaxAhead) {
			t.Fatalf("parseWhen(%q) out of range: %v", s, w.At)
		}
		if r, ok := parseRecurring(s, tpNow, berlin); ok {
			if r.Rule.validate() != nil {
				t.Fatalf("parseRecurring(%q) invalid rule %+v", s, r.Rule)
			}
			if !r.First.After(tpNow) {
				t.Fatalf("parseRecurring(%q) first %v not after now", s, r.First)
			}
		}
		whenDayPart(s)
		whenRecurring(s)
		parseDayRange(s, tpNow, berlin)
		tidyReminderText(s, "de")
		reminderTextFrom(s, "en", true, true)
		cancelQuery(s)
	})
}
