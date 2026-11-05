package main

import (
	"reflect"
	"testing"
)

func TestTokenize(t *testing.T) {
	cases := map[string][]string{
		"Hello, World!":           {"hello", "world"},
		"Wie ist das Wetter?":     {"wie", "ist", "das", "wetter"},
		"Grüß Gott, Löwe & Bär":   {"grüß", "gott", "löwe", "bär"},
		"what's the time":         {"what", "s", "the", "time"},
		"  ":                      nil,
		"T-Rex 3000":              {"t", "rex", "3000"},
		"STRAßE":                  {"straße"},
		"emoji 🤖 stripped":        {"emoji", "stripped"},
		"comma,separated;values.": {"comma", "separated", "values"},
	}
	for in, want := range cases {
		got := tokenize(in)
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("tokenize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHasPhrase(t *testing.T) {
	tok := tokenize("what time is it in Tokyo")
	for _, p := range []string{"time", "what time", "time is it", "in tokyo"} {
		if !hasPhrase(tok, p) {
			t.Errorf("expected %q to match", p)
		}
	}
	for _, p := range []string{"tim", "is time", "tokyo time", ""} {
		if hasPhrase(tok, p) {
			t.Errorf("did not expect %q to match", p)
		}
	}
}

func TestWantsRealtimeTable(t *testing.T) {
	positive := []string{
		"what is the weather in Tokyo",
		"Weather?",
		"what's the forecast for tomorrow",
		"is it going to rain today",
		"will it snow in Denver",
		"how windy is it in Chicago",
		"what time is it",
		"what's the time in London",
		"tell me the time",
		"what's today's date",
		"what day is it",
		"any news about technology",
		"latest headlines",
		"how hot is it in Dubai",
		"temperature in Paris",
		"Wie ist das Wetter in Berlin?",
		"Wie viel Uhr ist es?",
		"wieviel uhr haben wir",
		"Wie spät ist es?",
		"Regnet es morgen in Hamburg?",
		"Welches Datum haben wir heute?",
		"Gibt es Nachrichten aus Bayern?",
		"Wie warm ist es in München?",
		"wie viel grad hat es draußen",
		"Schneit es in den Alpen?",
	}
	for _, q := range positive {
		if !wantsRealtime(q) {
			t.Errorf("wantsRealtime(%q) = false, want true", q)
		}
	}
	negative := []string{
		"hello there",
		"what can you do today",
		"Wie geht es dir heute?",
		"explain how the brain works",
		"which train goes to Berlin",
		"I made an attempt to fix it",
		"please update the list",
		"sometimes I wonder",
		"open the window",
		"the drainage system is clogged",
		"what is a timestamp",
		"I need a strategy for chess",
		"write a newsletter intro",        // "newsletter" is not "news"
		"tell me about the Ukraine war",   // "rain" inside a word
		"what are your capabilities",      // no time word
		"ich bin gerade sehr beschäftigt", // "grad" only as phrase
		"meine Uhr ist kaputt",
		"es ist zu spät für Pizza",
		"Wer hat das Rad erfunden?",
	}
	for _, q := range negative {
		if wantsRealtime(q) {
			t.Errorf("wantsRealtime(%q) = true, want false", q)
		}
	}
}

func TestClassifyImageRequestTable(t *testing.T) {
	cases := map[string]string{
		// generation — English
		"draw a cat":                          "generate",
		"Draw me a dragon":                    "generate",
		"Hermes, draw a robot":                "generate",
		"please paint a sunset over the sea":  "generate",
		"can you draw a titan":                "generate",
		"I want you to sketch a castle":       "generate",
		"generate an image of a dog":          "generate",
		"create a picture of a mountain":      "generate",
		"make an image of a spaceship":        "generate",
		"render a picture of a city at night": "generate",
		"design a logo for my bakery":         "generate",
		"some concept art of a mech":          "generate",
		// generation — German
		"zeichne einen Drachen":               "generate",
		"generiere ein bild von einem Hund":   "generate",
		"male einen Sonnenuntergang":          "generate",
		"bitte male mir ein Schiff":           "generate",
		"Kannst du einen Hund malen?":         "generate",
		"erstelle eine Grafik von einem Mond": "generate",
		"mach ein Bild von einer Katze":       "generate",
		// search
		"find a picture of the eiffel tower": "search",
		"show me a photo of a red car":       "search",
		"look up images of the moon":         "search",
		"cat pictures":                       "search",
		"photos of the alps":                 "search",
		"zeig mir ein Bild von einem Löwen":  "search",
		"suche ein Foto von einem Auto":      "search",
		"bilder von Katzen":                  "search",
		"zeig mir Fotos vom Matterhorn":      "search",
		// neither
		"what is the weather in paris":         "",
		"what time is it":                      "",
		"generate a list of names":             "",
		"create a python script":               "",
		"make me a sandwich":                   "",
		"draw a conclusion from this data":     "",
		"what can we draw from this":           "",
		"that will draw attention":             "",
		"list the drawbacks of nuclear power":  "",
		"explain photosynthesis":               "",
		"pick a topic for my essay":            "",
		"I will never surrender":               "",
		"what does render mean in React":       "",
		"is the lion male or female":           "",
		"the female astronaut":                 "",
		"find the bug in my code":              "",
		"search for a good recipe":             "",
		"show me how to cook rice":             "",
		"how to draw a cat":                    "",
		"how do I make a photo smaller":        "",
		"what is an image sensor in a camera":  "",
		"Bildung ist wichtig":                  "",
		"wir bilden ein Team":                  "",
		"mal sehen, was passiert":              "",
		"zum ersten Male in Berlin":            "",
		"ich zeichne gern in meiner Freizeit":  "",
		"wie man malen lernt, erklär es mir":   "",
		"such dir etwas aus":                   "",
		"Ich mag malen":                        "",
		"what paint color fits a living room?": "",
	}
	for q, want := range cases {
		if got := classifyImageRequest(q); got != want {
			t.Errorf("classifyImageRequest(%q) = %q, want %q", q, got, want)
		}
	}
}

func TestExtractImageSubjectMore(t *testing.T) {
	cases := map[string]string{
		"find a picture of the eiffel tower": "eiffel tower",
		"Hermes, show me photos of a tiger!": "tiger",
		"pictures":                           "",
		"zeig mir ein":                       "",
		"cat pictures?":                      "cat",
		"findings pictures":                  "findings", // "find" prefix needs a word boundary
		"can you show me pictures of Mars":   "mars",
	}
	for in, want := range cases {
		if got := extractImageSubject(in); got != want {
			t.Errorf("extractImageSubject(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestImageRoutingDoesNotHijackQuestions covers ordinary questions and
// coding / advice requests that used to be routed to image search or
// generation because an image noun and a common verb appeared anywhere.
func TestImageRoutingDoesNotHijackQuestions(t *testing.T) {
	for _, q := range []string{
		// formerly "search"
		"Who wrote The Picture of Dorian Gray?",
		"I took a picture of my dog yesterday, what breed is it likely?",
		"Ich habe ein Foto von meinem Hund gemacht, was meinst du?",
		"Show me how to resize an image in Python",
		"How do I get my photos off my iPhone?",
		"Where can I find my photos on Windows?",
		"What do you see in this image?",
		"What is a picture?",
		"Hallo, kannst du mir zeigen wie ich Fotos sichere",
		"Find a picture-perfect vacation spot",
		"Can you get me a coffee recipe with photos?",
		// formerly "generate"
		"Make a list of art museums in Paris",
		"Create a playlist of art rock songs",
		"Make an image classification model",
		"Create an image upload form in HTML",
		"Write code to render an image with OpenGL",
		"Erstelle eine Grafik mit matplotlib",
		"What's the best way to create a logo?",
		"Which tool should I use to make a poster?",
		"Ich muss mir erst ein Bild von der Lage machen",
		"Mach dir selbst ein Bild davon",
		"Mal eine Frage: wie spaet ist es?",
		"Mal ein Beispiel, was ist Rekursion?",
		"Kannst du mir Tipps zum Malen geben?",
		"Hast du Tipps zum Zeichnen fuer Anfaenger?",
		"Wie kann ich in Photoshop ein Bild erstellen?",
		"Wie kann ich ein Logo fuer meine Firma erstellen?",
	} {
		if got := classifyImageRequest(q); got != "" {
			t.Errorf("classifyImageRequest(%q) = %q, want \"\"", q, got)
		}
	}
}

func TestImageRoutingPositivePhrasings(t *testing.T) {
	cases := map[string]string{
		"Can you find me pictures of dogs?":                 "search",
		"Could you search for photos of the Eiffel tower":   "search",
		"Kannst du mir Bilder von Katzen zeigen?":           "search",
		"I want to see pictures of cats":                    "search",
		"show me some cute dog photos":                      "search",
		"find cat pictures":                                 "search",
		"Kannst du mir ein Bild von einer Katze erstellen?": "generate",
		"Could you make me a cool picture of a dragon":      "generate",
		"create an icon for my app":                         "generate",
		"mach mir ein Bild von einem Roboter":               "generate",
	}
	for q, want := range cases {
		if got := classifyImageRequest(q); got != want {
			t.Errorf("classifyImageRequest(%q) = %q, want %q", q, got, want)
		}
	}
}

func TestExtractImageSubjectStripsRequestWords(t *testing.T) {
	cases := map[string]string{
		"Can you find me pictures of dogs?":               "dogs",
		"Could you search for photos of the Eiffel tower": "eiffel tower",
		"Kannst du mir Bilder von Katzen zeigen?":         "katzen",
		"I want to see pictures of cats":                  "cats",
		"show me some cute dog photos":                    "cute dog",
		"find cat pictures":                               "cat",
		"can you show me":                                 "",
		"show me pictures of it":                          "",
	}
	for in, want := range cases {
		if got := extractImageSubject(in); got != want {
			t.Errorf("extractImageSubject(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWantsRealtimeAnchorsAndHistory(t *testing.T) {
	for _, q := range []string{
		"Is it going to storm tomorrow?",
		"Gibt es heute Gewitter?",
		"Was ist heute fuer ein Tag?",
		"Was ist heute für ein Tag?",
		"What year is it?",
		"Was gibt's Neues?",
		"Is it hot outside?",
		"Will it be hot tomorrow in Berlin?",
		"Brauche ich heute eine Jacke?",
		"Temperatur in Berlin?",
	} {
		if !wantsRealtime(q) {
			t.Errorf("wantsRealtime(%q) = false, want true", q)
		}
	}
	for _, q := range []string{
		"What's the date of the French revolution?",
		"At the time of Napoleon, what was Europe like?",
		"How does a wind turbine work?",
		"Who is Jon Snow?",
		"My clock is broken",
		"What temperature does water boil at?",
		"Mein Datum ist morgen",
		"What year did the war end?",
	} {
		if wantsRealtime(q) {
			t.Errorf("wantsRealtime(%q) = true, want false", q)
		}
	}
}

// Technical "time"/"date" compounds and physical picture objects must not be
// mistaken for live-data or image requests.
func TestRoutingCompoundGuards(t *testing.T) {
	notRealtime := []string{
		"what is the time complexity of quicksort",
		"what's the time complexity of a hash map lookup",
		"what is the date format in ISO 8601",
		"how do I parse the date string in python",
		"what is the time limit for the exam",
		"is time travel possible",
		"Was ist die Zeitkomplexität von Mergesort",
	}
	for _, q := range notRealtime {
		if wantsRealtime(q) {
			t.Errorf("wantsRealtime(%q) = true, want false", q)
		}
	}
	stillRealtime := []string{
		"what time is it", "what time is it in tokyo", "what's the time now",
		"what is the date today", "what's the weather in Berlin",
		"Wie spät ist es?", "time zone in New York",
	}
	for _, q := range stillRealtime {
		if !wantsRealtime(q) {
			t.Errorf("wantsRealtime(%q) = false, want true", q)
		}
	}
	notImage := []string{
		"make a picture frame from wood",
		"how to make a photo album",
		"create a photo booth for the party",
		"mach einen Bilderrahmen aus Holz",
	}
	for _, q := range notImage {
		if got := classifyImageRequest(q); got != "" {
			t.Errorf("classifyImageRequest(%q) = %q, want \"\"", q, got)
		}
	}
	stillImage := map[string]string{
		"make a picture of a titan":       "generate",
		"create an image of a red dragon": "generate",
		"find a picture of a titan":       "search",
	}
	for q, want := range stillImage {
		if got := classifyImageRequest(q); got != want {
			t.Errorf("classifyImageRequest(%q) = %q, want %q", q, got, want)
		}
	}
}
