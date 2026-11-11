package hermes

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the slim Docker image may not ship a zoneinfo database
	"unicode"
)

// realtime.go answers weather, time and news questions locally: the data is
// fetched directly from free public services that need no key and the reply
// is a short fixed sentence in the voice's language. No language model sees
// the question or the data, and only the city (Open-Meteo) or the news topic
// (Google News) leaves the machine — never the history, names or memories.
//
//   - weather: Open-Meteo geocoding + forecast (current, today, tomorrow,
//     a weekday, the weekend, the next days, rain / umbrella questions);
//     the default city is the homeCity setting.
//   - time: the server clock in the household / browser zone, or in a
//     city's IANA zone (from Open-Meteo geocoding): "Wie spät ist es in
//     Tokio?".
//   - news: German top headlines from the Tagesschau API, English top
//     headlines from Google News, a topic from Google News search in the
//     reply language.
//
// Routing (wantsRealtime in routing.go) is unchanged: it decides whether a
// message is a live-data question at all; parseRealtime here decides which
// kind. chat.go runs it after the router, where the cloud step used to be.

// External data sources. Variables so tests can point them at fakes.
var (
	openMeteoForecastURL = "https://api.open-meteo.com/v1/forecast"
	openMeteoGeocodeURL  = "https://geocoding-api.open-meteo.com/v1/search"
	newsFeedURL          = "https://news.google.com/rss"
	tagesschauNewsURL    = "https://www.tagesschau.de/api2u/news"
)

// realtimeTimeout bounds every outgoing request of this file.
var realtimeTimeout = 5 * time.Second

const (
	weatherCacheTTL = 10 * time.Minute
	newsCacheTTL    = 15 * time.Minute

	geocodeMaxBytes    = 256 << 10
	forecastMaxBytes   = 256 << 10
	rssMaxBytes        = 2 << 20
	tagesschauMaxBytes = 6 << 20 // the full feed is about 1 MB

	realtimeSpokenHeadlines = 3
	realtimeCardHeadlines   = 5
)

// ---------------------------------------------------------------------------
// Response cache (raw bodies by URL; only successes are stored)

type realtimeCacheEntry struct {
	body []byte
	at   time.Time
}

const realtimeCacheMax = 48

var realtimeCache = struct {
	sync.Mutex
	m map[string]realtimeCacheEntry
}{m: map[string]realtimeCacheEntry{}}

// cachedGet returns a cached body younger than ttl or fetches u.
func cachedGet(ctx context.Context, u string, ttl time.Duration, limit int64) ([]byte, error) {
	realtimeCache.Lock()
	e, ok := realtimeCache.m[u]
	realtimeCache.Unlock()
	if ok && time.Since(e.at) < ttl {
		return e.body, nil
	}
	body, err := httpGetLimited(ctx, u, realtimeTimeout, limit)
	if err != nil {
		return nil, err
	}
	realtimeCache.Lock()
	defer realtimeCache.Unlock()
	if len(realtimeCache.m) >= realtimeCacheMax {
		for k, old := range realtimeCache.m {
			if time.Since(old.at) >= newsCacheTTL {
				delete(realtimeCache.m, k)
			}
		}
		if len(realtimeCache.m) >= realtimeCacheMax {
			realtimeCache.m = map[string]realtimeCacheEntry{}
		}
	}
	realtimeCache.m[u] = realtimeCacheEntry{body: body, at: time.Now()}
	return body, nil
}

// resetRealtimeCaches empties the response and geocode caches (tests).
func resetRealtimeCaches() {
	realtimeCache.Lock()
	realtimeCache.m = map[string]realtimeCacheEntry{}
	realtimeCache.Unlock()
	geoCache.Lock()
	geoCache.m = map[string]geoPlace{}
	geoCache.Unlock()
}

// ---------------------------------------------------------------------------
// Geocoding (Open-Meteo)

// geocodeResult is one Open-Meteo geocoding match.
type geocodeResult struct {
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Name        string  `json:"name"`
	Country     string  `json:"country"`
	CountryCode string  `json:"country_code"`
	Admin1      string  `json:"admin1"`
	Timezone    string  `json:"timezone"`
}

// errPlaceNotFound: the geocoder answered, but knows no such place.
var errPlaceNotFound = errors.New("place not found")

// geocodeCity resolves a city name. Open-Meteo only matches bare place names,
// so a qualified "Paris, France" or "Frankfurt (Oder), Germany" is retried as
// its base name, and the qualifier picks among the matches. Only the name and
// the language are sent.
func geocodeCity(ctx context.Context, city, lang string) (geocodeResult, error) {
	l := "en"
	if lang == "de" {
		l = "de"
	}
	lookup := func(name string) ([]geocodeResult, error) {
		u := openMeteoGeocodeURL + "?count=5&language=" + l + "&format=json&name=" + url.QueryEscape(name)
		b, err := httpGetLimited(ctx, u, realtimeTimeout, geocodeMaxBytes)
		if err != nil {
			return nil, err
		}
		var res struct {
			Results []geocodeResult `json:"results"`
		}
		if err := json.Unmarshal(b, &res); err != nil {
			return nil, err
		}
		return res.Results, nil
	}
	city = strings.TrimSpace(city)
	results, err := lookup(city)
	if err != nil {
		return geocodeResult{}, err
	}
	var qualifiers []string
	if i := strings.IndexAny(city, ",("); i > 0 {
		qualifiers = strings.FieldsFunc(city[i:], func(r rune) bool { return r == ',' || r == '(' || r == ')' })
		if base := strings.TrimSpace(city[:i]); len(results) == 0 && base != "" {
			if results, err = lookup(base); err != nil {
				return geocodeResult{}, err
			}
		}
	}
	if len(results) == 0 {
		return geocodeResult{}, fmt.Errorf("%w: %s", errPlaceNotFound, city)
	}
	return bestGeocodeMatch(results, qualifiers), nil
}

// bestGeocodeMatch picks the result matching the most qualifiers ("Oder",
// "Germany", "DE") by name, state, country or country code; ties keep
// Open-Meteo's own ranking.
func bestGeocodeMatch(results []geocodeResult, qualifiers []string) geocodeResult {
	best, bestScore := 0, 0
	for i, r := range results {
		score := 0
		for _, q := range qualifiers {
			q = strings.ToLower(strings.TrimSpace(q))
			if q == "" {
				continue
			}
			for _, f := range []string{r.Name, r.Admin1, r.Country, r.CountryCode} {
				f = strings.ToLower(f)
				// Short codes ("DE", "IL") must start the field, so "de"
				// does not match "Sweden".
				if f != "" && (strings.HasPrefix(f, q) || (len(q) > 2 && strings.Contains(f, q))) {
					score++
					break
				}
			}
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	return results[best]
}

type geoPlace struct {
	lat, lon float64
	name     string // spoken name, without the country
	tz       string // IANA zone, "" if the geocoder gave none
}

var geoCache = struct {
	sync.Mutex
	m map[string]geoPlace
}{m: map[string]geoPlace{}}

// cachedGeocode resolves a city once per process (and endpoint, so tests
// with fake servers never share entries). Only found places are cached.
func cachedGeocode(ctx context.Context, city, lang string) (geoPlace, error) {
	key := openMeteoGeocodeURL + "|" + lang + "|" + strings.ToLower(strings.TrimSpace(city))
	geoCache.Lock()
	p, ok := geoCache.m[key]
	geoCache.Unlock()
	if ok {
		return p, nil
	}
	r, err := geocodeCity(ctx, city, lang)
	if err != nil {
		return geoPlace{}, err
	}
	p = geoPlace{lat: r.Latitude, lon: r.Longitude, name: safeTitle(r.Name), tz: r.Timezone}
	if p.name == "" {
		p.name = safeTitle(city)
	}
	geoCache.Lock()
	if len(geoCache.m) >= 64 {
		geoCache.m = map[string]geoPlace{}
	}
	geoCache.m[key] = p
	geoCache.Unlock()
	return p, nil
}

// ---------------------------------------------------------------------------
// Forecast (Open-Meteo)

type openMeteoForecast struct {
	Timezone string `json:"timezone"`
	Current  struct {
		Temperature   float64 `json:"temperature_2m"`
		Apparent      float64 `json:"apparent_temperature"`
		WeatherCode   int     `json:"weather_code"`
		Wind          float64 `json:"wind_speed_10m"`
		Precipitation float64 `json:"precipitation"`
	} `json:"current"`
	Daily struct {
		Time []string  `json:"time"`
		Max  []float64 `json:"temperature_2m_max"`
		Min  []float64 `json:"temperature_2m_min"`
		Rain []float64 `json:"precipitation_probability_max"`
		Code []int     `json:"weather_code"`
	} `json:"daily"`
}

// forecastDays is how far ahead a weekday question can reach.
const forecastDays = 7

// fetchForecast gets the current conditions and the daily forecast for a
// place (only its coordinates are sent), cached for weatherCacheTTL.
func fetchForecast(ctx context.Context, p geoPlace) (openMeteoForecast, error) {
	u := fmt.Sprintf("%s?latitude=%.4f&longitude=%.4f"+
		"&current=temperature_2m,weather_code,apparent_temperature,wind_speed_10m,precipitation"+
		"&daily=weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max"+
		"&timezone=auto&forecast_days=%d", openMeteoForecastURL, p.lat, p.lon, forecastDays)
	var f openMeteoForecast
	b, err := cachedGet(ctx, u, weatherCacheTTL, forecastMaxBytes)
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, err
	}
	return f, nil
}

// wmoCondition is the English wording of a WMO weather code.
func wmoCondition(code int) string {
	switch code {
	case 0:
		return "clear sky"
	case 1:
		return "mainly clear"
	case 2:
		return "partly cloudy"
	case 3:
		return "overcast"
	case 45, 48:
		return "fog"
	case 51, 53, 55:
		return "drizzle"
	case 56, 57:
		return "freezing drizzle"
	case 61, 63, 65:
		return "rain"
	case 66, 67:
		return "freezing rain"
	case 71, 73, 75:
		return "snow"
	case 77:
		return "snow grains"
	case 80, 81, 82:
		return "rain showers"
	case 85, 86:
		return "snow showers"
	case 95:
		return "thunderstorm"
	case 96, 99:
		return "thunderstorm with hail"
	}
	return "unknown conditions"
}

// conditionText is the condition in lang, "" when the code is unknown.
func conditionText(code int, lang string) string {
	if lang == "de" {
		return wmoConditionDE(code)
	}
	if c := wmoCondition(code); c != "unknown conditions" {
		return c
	}
	return ""
}

// wetCode: rain, drizzle, showers or a thunderstorm.
func wetCode(code int) bool {
	return (code >= 51 && code <= 67) || (code >= 80 && code <= 82) || code >= 95
}

// ---------------------------------------------------------------------------
// News

// headline is one news item, cleaned and safe to speak.
type headline struct {
	Title   string `json:"title"`
	Topline string `json:"topline,omitempty"`
}

// cleanSpeech makes third-party text safe to speak and to show: no URLs,
// controls or prompt delimiters, one line, at most 160 runes.
func cleanSpeech(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = cleanText(s, 400)
	s = urlPattern.ReplaceAllString(s, " ")
	for strings.Contains(s, "<<<") || strings.Contains(s, ">>>") {
		s = strings.ReplaceAll(strings.ReplaceAll(s, "<<<", ""), ">>>", "")
	}
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(truncateRunes(s, 160))
}

// cleanHeadline drops the " - Source" suffix Google News appends and makes
// the text safe to speak.
func cleanHeadline(s string) string {
	s = strings.ToValidUTF8(s, "")
	if i := strings.LastIndex(s, " - "); i > 0 {
		s = s[:i]
	}
	return cleanSpeech(s)
}

// googleNewsHeadlines reads the Google News RSS feed in lang: the top
// stories, or a search for topic. Only the topic and the language are sent.
func googleNewsHeadlines(ctx context.Context, topic, lang string) ([]headline, error) {
	l, gl, ceid := "en-US", "US", "US:en"
	if lang == "de" {
		l, gl, ceid = "de-DE", "DE", "DE:de"
	}
	params := "hl=" + l + "&gl=" + gl + "&ceid=" + ceid
	feedURL := newsFeedURL + "?" + params
	if topic != "" {
		feedURL = newsFeedURL + "/search?q=" + url.QueryEscape(topic) + "&" + params
	}
	body, err := cachedGet(ctx, feedURL, newsCacheTTL, rssMaxBytes)
	if err != nil {
		return nil, err
	}
	var feed struct {
		Channel struct {
			Item []struct {
				Title string `xml:"title"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("news feed: %w", err)
	}
	var out []headline
	seen := map[string]bool{}
	for _, it := range feed.Channel.Item {
		h := cleanHeadline(it.Title)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, headline{Title: h})
		if len(out) >= 10 {
			break
		}
	}
	return out, nil
}

// tagesschauHeadlines reads the Tagesschau API (GET /api2u/news, no
// parameters, nothing about the user is sent). It keeps the national
// stories ("type":"story", "regionId":0) in feed order, newest first;
// regional stories and videos are left out.
func tagesschauHeadlines(ctx context.Context) ([]headline, error) {
	body, err := cachedGet(ctx, tagesschauNewsURL, newsCacheTTL, tagesschauMaxBytes)
	if err != nil {
		return nil, err
	}
	var res struct {
		News []struct {
			Title    string `json:"title"`
			Topline  string `json:"topline"`
			Type     string `json:"type"`
			RegionID int    `json:"regionId"`
		} `json:"news"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("tagesschau: %w", err)
	}
	var out []headline
	seen := map[string]bool{}
	for _, it := range res.News {
		if it.Type != "story" || it.RegionID != 0 {
			continue
		}
		h := cleanSpeech(it.Title)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, headline{Title: h, Topline: cleanSpeech(it.Topline)})
		if len(out) >= 10 {
			break
		}
	}
	return out, nil
}

// fetchHeadlines returns headlines and their source ("tagesschau" or
// "googlenews"): German top news from the Tagesschau (Google News as the
// fallback), everything else from Google News.
func fetchHeadlines(ctx context.Context, topic, lang string) ([]headline, string, error) {
	if topic == "" && lang == "de" {
		hs, err := tagesschauHeadlines(ctx)
		if err == nil && len(hs) > 0 {
			return hs, "tagesschau", nil
		}
		if err != nil {
			log.Printf("realtime: tagesschau: %v (falling back to Google News)", err)
		}
	}
	hs, err := googleNewsHeadlines(ctx, topic, lang)
	return hs, "googlenews", err
}

// ---------------------------------------------------------------------------
// Understanding the question

// realtimeRequest is a parsed live-data question. It is also the args of the
// "realtime" tool (the city question resumes through it).
type realtimeRequest struct {
	Kind  string `json:"kind"`            // "weather" | "time" | "news"
	City  string `json:"city,omitempty"`  // weather / time place; "" = home
	Span  string `json:"span,omitempty"`  // weather day(s), see parseSpan
	Rain  bool   `json:"rain,omitempty"`  // rain / umbrella question
	Clock bool   `json:"clock,omitempty"` // time: the clock
	Date  bool   `json:"date,omitempty"`  // time: date / weekday / year
	Zone  bool   `json:"zone,omitempty"`  // time: the time zone was asked
	Topic string `json:"topic,omitempty"` // news topic
}

func matchesRealtimeGroup(tokens []string, group []string) bool {
	for _, p := range group {
		if matchRealtimePhrase(tokens, p) {
			return true
		}
	}
	return false
}

// parseRealtime classifies a question wantsRealtime accepted.
func parseRealtime(q string) realtimeRequest {
	tokens := tokenize(q)
	newsFound, newsOpened := matchTopic(tokens, realtimeNewsWords)
	weather := matchesRealtimeGroup(tokens, realtimeWeatherPhrases)
	if !weather {
		weather, _ = matchTopic(tokens, realtimeTopicWords)
	}
	switch {
	case matchesRealtimeGroup(tokens, realtimeNewsPhrases) || newsRequest(tokens) || newsOpened || (newsFound && !weather):
		return realtimeRequest{Kind: "news", Topic: extractNewsTopic(q)}
	case weather:
		return realtimeRequest{Kind: "weather", City: extractPlace(q), Span: parseSpan(tokens), Rain: hasAnyWord(tokens, rainWords)}
	}
	r := realtimeRequest{Kind: "time", City: extractPlace(q)}
	r.Clock = matchesRealtimeGroup(tokens, realtimeTimePhrases)
	r.Date = matchesRealtimeGroup(tokens, realtimeDatePhrases)
	r.Zone = hasAnyPhrase(tokens, []string{"time zone", "timezone", "zeitzone"})
	if !r.Clock && !r.Date {
		r.Clock = true
	}
	return r
}

// rainWords make a weather question about rain or an umbrella.
var rainWords = wordSet(
	"rain", "raining", "rainy", "umbrella", "wet", "shower", "showers", "precipitation", "drizzle",
	"regen", "regnet", "regnen", "regnets", "regenschirm", "schirm", "nass", "niederschlag",
	"schauer", "nieselregen",
)

var (
	weekdayWords = map[string]time.Weekday{
		"sonntag": time.Sunday, "montag": time.Monday, "dienstag": time.Tuesday, "mittwoch": time.Wednesday,
		"donnerstag": time.Thursday, "freitag": time.Friday, "samstag": time.Saturday, "sonnabend": time.Saturday,
		"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday,
		"thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday,
	}
	weekPhrases = []string{
		"nächsten tage", "naechsten tage", "kommenden tage", "nächste woche", "naechste woche",
		"diese woche", "die woche", "der woche", "ganze woche", "nächsten tagen", "naechsten tagen",
		"next days", "next few days", "coming days", "this week", "the week", "next week", "few days",
	}
)

// parseSpan finds the forecast day(s) a weather question asks about: ""
// (now), "today", "d1" (tomorrow), "d2" (the day after), "weekend", "week"
// (today and the next three days) or "wd0".."wd6" (a weekday, Sunday = 0).
func parseSpan(tokens []string) string {
	switch {
	case hasAnyWord(tokens, wordSet("übermorgen", "uebermorgen")) || hasPhrase(tokens, "day after tomorrow"):
		return "d2"
	case hasAnyWord(tokens, wordSet("wochenende", "weekend")):
		return "weekend"
	}
	for _, tok := range tokens {
		if wd, ok := weekdayWords[tok]; ok {
			return "wd" + strconv.Itoa(int(wd))
		}
	}
	morning := hasAnyPhrase(tokens, []string{"heute morgen", "heut morgen", "guten morgen"})
	switch {
	case hasPhrase(tokens, "tomorrow") || (hasPhrase(tokens, "morgen") && !morning):
		return "d1"
	case hasAnyPhrase(tokens, weekPhrases):
		return "week"
	case hasAnyWord(tokens, wordSet("heute", "heut", "today", "tonight", "heutige", "heutigen")):
		return "today"
	}
	return ""
}

// qToken is a word of the original text with its byte span, so a place or a
// topic keeps its spelling ("Frankfurt am Main", "Baden-Baden").
type qToken struct {
	orig, low  string
	start, end int
}

func queryTokens(q string) []qToken {
	var out []qToken
	start := -1
	for i, r := range q {
		word := unicode.IsLetter(r) || unicode.IsDigit(r)
		switch {
		case word && start < 0:
			start = i
		case !word && start >= 0:
			out = append(out, qToken{q[start:i], strings.ToLower(q[start:i]), start, i})
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, qToken{q[start:], strings.ToLower(q[start:]), start, len(q)})
	}
	return out
}

func capitalized(s string) bool {
	for _, r := range s {
		return unicode.IsUpper(r)
	}
	return false
}

// placePreps introduce a place: "in Berlin", "für Hamburg", "for Paris".
var placePreps = wordSet("in", "for", "für", "fuer")

// placeStop ends (or rules out) a place name: time words, fillers, pronouns,
// numbers and weather words.
var placeStop = wordSet(
	// time
	"now", "today", "tonight", "tomorrow", "outside", "currently", "right", "this", "next", "week",
	"weekend", "days", "day", "morning", "evening", "afternoon", "night", "time", "later", "soon",
	"heute", "heut", "morgen", "übermorgen", "uebermorgen", "jetzt", "gerade", "draußen", "draussen",
	"aktuell", "momentan", "zurzeit", "derzeit", "woche", "wochenende", "tag", "tage", "tagen", "früh",
	"frueh", "abend", "nachmittag", "vormittag", "mittag", "nacht", "uhr", "zeit", "später", "spaeter", "bald",
	"diese", "dieses", "diesem", "diesen", "nächste", "nächsten", "naechste", "naechsten", "kommende", "kommenden",
	// fillers and verbs
	"am", "um", "an", "im", "bis", "und", "oder", "bitte", "mal", "es", "ist", "sein", "wird", "werden",
	"so", "auch", "wie", "was", "wann", "denn", "eigentlich", "noch", "schon", "da", "dort", "hier",
	"on", "at", "and", "or", "please", "like", "be", "will", "going", "gonna", "is", "it", "there", "here",
	"what", "how", "when", "about", "right", "now", "the", "a", "an", "be", "s",
	// pronouns
	"du", "dir", "dich", "ich", "mir", "mich", "er", "sie", "wir", "ihr", "you", "me", "him", "her", "we", "they",
	// numbers
	"ein", "eine", "einer", "zwei", "drei", "vier", "fünf", "fuenf", "sechs", "sieben", "acht", "neun", "zehn",
	"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "few", "couple",
	// weather
	"wetter", "weather", "regen", "regnen", "regnet", "schnee", "schneien", "warm", "kalt", "sonnig",
	"rain", "raining", "snow", "sunny", "cold", "hot", "grad", "degrees",
)

// placeConnectors may sit inside a place name before a capitalised word:
// "Frankfurt am Main", "Rio de Janeiro", "Stratford upon Avon".
var placeConnectors = wordSet("am", "an", "der", "de", "del", "la", "le", "upon", "on", "sur", "im", "ob")

func init() {
	for w := range weekdayWords {
		placeStop[w] = true
	}
}

// placeWordOK: tok may be (part of) a place name.
func placeWordOK(tok qToken) bool {
	if placeStop[tok.low] || notAPlace[tok.low] {
		return false
	}
	_, err := strconv.Atoi(tok.low)
	return err != nil
}

// extractPlace finds the place a weather or time question names: after
// "in" / "für" / "for" ("Wie spät ist es in New York?"), or right after
// "Wetter" ("Wetter Berlin morgen"). "" when none is named.
func extractPlace(q string) string {
	toks := queryTokens(q)
	lowerOnly := q == strings.ToLower(q)
	for i := 0; i+1 < len(toks); i++ {
		if !placePreps[toks[i].low] || !placeWordOK(toks[i+1]) {
			continue
		}
		if p := placeFrom(q, toks, i+1, lowerOnly); p != "" {
			return p
		}
	}
	for i := 0; i+1 < len(toks); i++ {
		if (toks[i].low == "wetter" || toks[i].low == "weather") && capitalized(toks[i+1].orig) && placeWordOK(toks[i+1]) {
			return placeFrom(q, toks, i+1, false)
		}
	}
	return ""
}

// placeFrom reads a place of up to four words starting at toks[j]: later
// words must be capitalised (unless the whole text is lower case) or a
// connector before a capitalised word.
func placeFrom(q string, toks []qToken, j int, lowerOnly bool) string {
	end := j
	for k := j + 1; k < len(toks) && k <= j+3; k++ {
		w := toks[k]
		if placeConnectors[w.low] && !lowerOnly {
			if k+1 < len(toks) && capitalized(toks[k+1].orig) && placeWordOK(toks[k+1]) {
				continue
			}
			break
		}
		if !placeWordOK(w) || (!lowerOnly && !capitalized(w.orig)) {
			break
		}
		end = k
	}
	return cleanPlace(q[toks[j].start:toks[end].end])
}

func cleanPlace(s string) string {
	s = safeTitle(s)
	return strings.TrimSpace(truncateRunes(s, 60))
}

// topicArticles may open a news topic and are dropped.
var topicArticles = wordSet("the", "a", "an", "der", "die", "das", "den", "dem", "des")

// topicTrailStop may end a news request without belonging to the topic.
var topicTrailStop = wordSet("heute", "heut", "today", "bitte", "please", "now", "jetzt", "aktuell",
	"currently", "latest", "neueste", "neuesten", "gerade", "mal", "hermes")

// extractNewsTopic finds the topic of "Nachrichten über X", "Nachrichten
// aus Bayern", "news about X", "headlines on Y"; "" for the top news.
func extractNewsTopic(q string) string {
	toks := queryTokens(q)
	for i := 0; i+1 < len(toks); i++ {
		if !newsNouns[toks[i].low] && toks[i].low != "headline" && toks[i].low != "neues" {
			continue
		}
		if !newsTopicLinkers[toks[i+1].low] {
			continue
		}
		j := i + 2
		for j < len(toks) && topicArticles[toks[j].low] {
			j++
		}
		end := len(toks) - 1
		for end >= j && topicTrailStop[toks[end].low] {
			end--
		}
		if end < j {
			return ""
		}
		return strings.TrimSpace(truncateRunes(safeTitle(q[toks[j].start:toks[end].end]), 80))
	}
	return ""
}

// ---------------------------------------------------------------------------
// Follow-ups ("Und morgen?", "And in Paris?") after a realtime answer

const realtimeFollowTTL = 10 * time.Minute

type realtimeLastEntry struct {
	req     realtimeRequest
	at      time.Time
	speaker string
}

var realtimeLast = struct {
	sync.Mutex
	m map[string]realtimeLastEntry
}{m: map[string]realtimeLastEntry{}}

func rememberRealtime(t *Turn, req realtimeRequest) {
	if t.ClientID == "" {
		return
	}
	realtimeLast.Lock()
	defer realtimeLast.Unlock()
	now := time.Now()
	for k, e := range realtimeLast.m {
		if now.Sub(e.at) > realtimeFollowTTL {
			delete(realtimeLast.m, k)
		}
	}
	if len(realtimeLast.m) >= 256 {
		realtimeLast.m = map[string]realtimeLastEntry{}
	}
	realtimeLast.m[t.ClientID+"|"+t.Speaker.ID] = realtimeLastEntry{req: req, at: now, speaker: t.Speaker.ID}
}

func lastRealtime(t *Turn) (realtimeRequest, bool) {
	realtimeLast.Lock()
	defer realtimeLast.Unlock()
	e, ok := realtimeLast.m[t.ClientID+"|"+t.Speaker.ID]
	if !ok || time.Since(e.at) > realtimeFollowTTL {
		return realtimeRequest{}, false
	}
	return e.req, true
}

func purgeRealtimePerson(personID string) int {
	realtimeLast.Lock()
	defer realtimeLast.Unlock()
	n := 0
	for k, e := range realtimeLast.m {
		if e.speaker == personID {
			delete(realtimeLast.m, k)
			n++
		}
	}
	return n
}

// isRealtimeSrc: the reply came from this file.
func isRealtimeSrc(src string) bool {
	return strings.HasPrefix(src, "realtime:") || src == "tool:realtime"
}

var followUpOpeners = [][]string{
	{"und", "wie", "ist", "es"}, {"und", "wie", "sieht", "es"}, {"und", "was", "ist", "mit"},
	{"was", "ist", "mit"}, {"wie", "ist", "es"}, {"wie", "sieht", "s"}, {"und", "wie"},
	{"and", "what", "about"}, {"what", "about"}, {"how", "about"}, {"and", "how", "about"},
	{"und"}, {"and"},
}

// followUpFillers may sit between the opener and the new day or place.
var followUpFillers = wordSet("in", "for", "für", "fuer", "mit", "dort", "there", "aus", "dann", "then", "the",
	"am", "on", "der", "die", "das", "it", "es", "s", "like", "aus", "hermes", "bitte", "please")

// realtimeFollowUp turns "Und morgen?" / "And in Paris?" into a full
// request when the previous assistant message (same speaker) was a weather
// or time answer of this client.
func realtimeFollowUp(t *Turn) (realtimeRequest, bool) {
	n := len(t.Tagged)
	if t.ClientID == "" || n < 3 || len(t.Tokens) == 0 || len(t.Tokens) > 8 {
		return realtimeRequest{}, false
	}
	a := t.Tagged[n-2]
	if a.Role != "assistant" || !isRealtimeSrc(a.Src) || a.Sp != t.Speaker.ID {
		return realtimeRequest{}, false
	}
	base, ok := lastRealtime(t)
	if !ok || base.Kind == "news" {
		return realtimeRequest{}, false
	}
	toks := queryTokens(t.Query)
	i := 0
	for i < len(toks) && leadingFillers[toks[i].low] && toks[i].low != "und" && toks[i].low != "and" {
		i++
	}
	low := make([]string, len(toks))
	for k, tok := range toks {
		low[k] = tok.low
	}
	opened := false
	for _, o := range followUpOpeners {
		if phraseAt(low, i, o) {
			i += len(o)
			opened = true
			break
		}
	}
	if !opened {
		return realtimeRequest{}, false
	}
	rest := low[i:]
	span := parseSpan(rest)
	city := extractPlace(t.Query)
	if city == "" {
		j := i
		for j < len(toks) && followUpFillers[toks[j].low] {
			j++
		}
		last := len(toks) - 1
		for last >= j && topicTrailStop[toks[last].low] {
			last--
		}
		lowerOnly := t.Query == strings.ToLower(t.Query)
		if j <= last && last-j < 4 && span == "" && placeWordOK(toks[j]) && (lowerOnly || capitalized(toks[j].orig)) {
			ok := true
			for _, tok := range toks[j : last+1] {
				if !placeWordOK(tok) && !placeConnectors[tok.low] {
					ok = false
				}
			}
			if ok {
				city = cleanPlace(t.Query[toks[j].start:toks[last].end])
			}
		}
	}
	if base.Kind == "time" {
		if city == "" {
			return realtimeRequest{}, false
		}
		base.City = city
		return base, true
	}
	if city == "" && span == "" {
		return realtimeRequest{}, false
	}
	if city != "" {
		base.City = city
	}
	if span != "" {
		base.Span = span
	}
	if hasAnyWord(rest, rainWords) {
		base.Rain = true
	}
	return base, true
}

// realtimeIntent: the turn asks for live data (or follows up on such an
// answer), and what exactly.
func realtimeIntent(t *Turn) (realtimeRequest, bool) {
	if wantsRealtime(t.Query) {
		return parseRealtime(t.Query), true
	}
	return realtimeFollowUp(t)
}

// ---------------------------------------------------------------------------
// Answering

// runRealtime answers a live-data question on the turn's stream: busy
// notice while the services answer, an optional card, the text, meta, done.
func runRealtime(t *Turn, req realtimeRequest) {
	t.handledBy = "realtime:" + req.Kind
	sse := t.SSE()
	stop := sse.watchBusy()
	r := answerRealtime(t, req)
	stop()
	writeReply(t, r)
}

func turnCtx(t *Turn) context.Context {
	if t.Ctx != nil {
		return t.Ctx
	}
	return context.Background()
}

func answerRealtime(t *Turn, req realtimeRequest) Reply {
	var r Reply
	switch req.Kind {
	case "news":
		r = answerNews(t, req)
	case "time":
		r = answerTime(t, req)
	default:
		req.Kind = "weather"
		r = answerWeather(t, req)
	}
	if !r.Error {
		rememberRealtime(t, req)
	}
	return r
}

func realtimeDown(lang, what string) Reply {
	var text string
	switch what {
	case "weather":
		text = pick(lang, "I can't reach the weather service right now.", "Ich erreiche den Wetterdienst gerade nicht.")
	case "news":
		text = pick(lang, "I can't reach the news service right now.", "Ich erreiche den Nachrichtendienst gerade nicht.")
	default:
		text = pick(lang, "I can't reach the place lookup right now.", "Ich erreiche den Ortsdienst gerade nicht.")
	}
	return Reply{Text: text, Error: true}
}

func placeNotFound(lang, city string) Reply {
	return Reply{Text: fmt.Sprintf(pick(lang, "I can't find a place called %s.", "Ich finde keinen Ort namens %s."), safeTitle(city))}
}

// realtimeCityQuestion asks for the city when neither the question nor the
// settings name one; the answer resumes the weather request.
func realtimeCityQuestion(t *Turn, req realtimeRequest) Reply {
	args, _ := json.Marshal(req)
	q := pick(t.Lang,
		"For which city? Tip: set your home city in the settings and I won't need to ask.",
		"Für welchen Ort? Tipp: Trag in den Einstellungen deinen Wohnort ein, dann muss ich nicht fragen.")
	return t.Ask(Pending{Kind: "slot", Slot: "city", Tool: "realtime", Args: args, Resume: realtimeCityResume}, q)
}

// realtimeCityResume takes a short answer ("Hamburg", "in Köln") as the
// city; anything longer or a question is a new request.
func realtimeCityResume(t *Turn, p *Pending) (Reply, bool) {
	if len(t.Tokens) == 0 || len(t.Tokens) > 5 || isQuestion(t) || yesNo(t.Tokens) != 0 {
		return Reply{}, false
	}
	city := extractPlace(t.Query)
	if city == "" {
		toks := queryTokens(t.Query)
		j := 0
		for j < len(toks) && (placePreps[toks[j].low] || leadingFillers[toks[j].low]) {
			j++
		}
		last := len(toks) - 1
		for last >= j && (topicTrailStop[toks[last].low] || leadingFillers[toks[last].low]) {
			last--
		}
		if j > last || !placeWordOK(toks[j]) {
			return Reply{}, false
		}
		city = cleanPlace(t.Query[toks[j].start:toks[last].end])
	}
	if city == "" {
		return Reply{}, false
	}
	return runToolChecked(t, "realtime", mergeArg(p.Args, "city", city)), true
}

// homeCity is the household's weather city from the settings.
func homeCity(t *Turn) string {
	if t.Settings == nil {
		return ""
	}
	return strings.TrimSpace(t.Settings.HomeCity)
}

func answerWeather(t *Turn, req realtimeRequest) Reply {
	city := req.City
	if city == "" {
		city = homeCity(t)
	}
	if city == "" {
		return realtimeCityQuestion(t, req)
	}
	ctx := turnCtx(t)
	place, err := cachedGeocode(ctx, city, t.Lang)
	if errors.Is(err, errPlaceNotFound) {
		return placeNotFound(t.Lang, city)
	}
	if err != nil {
		log.Printf("realtime: geocode: %v", err)
		return realtimeDown(t.Lang, "weather")
	}
	f, err := fetchForecast(ctx, place)
	if err != nil {
		log.Printf("realtime: forecast: %v", err)
		return realtimeDown(t.Lang, "weather")
	}
	days, ok := resolveDays(req.Span, f, turnNow(t))
	if !ok {
		return Reply{Text: pick(t.Lang, "I have no forecast for that day yet.", "Für diesen Tag habe ich noch keine Vorhersage.")}
	}
	byWeekday := strings.HasPrefix(req.Span, "wd")
	var text string
	switch {
	case len(days) == 0 && req.Rain:
		text = rainNowText(t.Lang, place.name, f)
	case len(days) == 0:
		text = weatherSentence(t.Lang, place.name, f)
	case len(days) == 1 && req.Rain:
		text = rainDayText(t.Lang, place.name, f, days[0], byWeekday)
	case len(days) == 1:
		text = dayText(t.Lang, place.name, f, days[0], byWeekday)
	default:
		text = daysText(t.Lang, place.name, f, days)
	}
	return Reply{Text: text, Card: weatherCard(t.Lang, place.name, f, days, req.Rain)}
}

// forecastBase is the first forecast date at the place (today there).
func forecastBase(f openMeteoForecast, now time.Time) time.Time {
	if len(f.Daily.Time) > 0 {
		if d, err := time.Parse("2006-01-02", f.Daily.Time[0]); err == nil {
			return d
		}
	}
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func forecastLen(f openMeteoForecast) int {
	return min(len(f.Daily.Max), len(f.Daily.Min))
}

// resolveDays maps a span to forecast day indexes (nil = now and today);
// ok=false when the forecast does not reach that far.
func resolveDays(span string, f openMeteoForecast, now time.Time) ([]int, bool) {
	wd := int(forecastBase(f, now).Weekday())
	var days []int
	switch {
	case span == "":
		return nil, true
	case span == "today":
		days = []int{0}
	case span == "d1":
		days = []int{1}
	case span == "d2":
		days = []int{2}
	case span == "weekend":
		switch wd {
		case int(time.Sunday):
			days = []int{0}
		case int(time.Saturday):
			days = []int{0, 1}
		default:
			days = []int{6 - wd, 7 - wd}
		}
	case span == "week":
		days = []int{0, 1, 2, 3}
	case strings.HasPrefix(span, "wd"):
		n, err := strconv.Atoi(span[2:])
		if err != nil || n < 0 || n > 6 {
			return nil, true
		}
		days = []int{(n - wd + 7) % 7}
	default:
		return nil, true
	}
	var out []int
	for _, d := range days {
		if d < forecastLen(f) {
			out = append(out, d)
		}
	}
	return out, len(out) > 0
}

// weatherSentence is the "now and today" report (also the briefing's
// weather section): "In Berlin sind es 6 Grad, bedeckt. Heute bis 11 Grad,
// Regenrisiko 40 Prozent."
func weatherSentence(lang, city string, f openMeteoForecast) string {
	cond := conditionText(f.Current.WeatherCode, lang)
	var b strings.Builder
	if lang == "de" {
		fmt.Fprintf(&b, "In %s sind es %s", city, degrees(f.Current.Temperature, lang))
	} else {
		fmt.Fprintf(&b, "In %s it is %s", city, degrees(f.Current.Temperature, lang))
	}
	if cond != "" {
		b.WriteString(", " + cond)
	}
	b.WriteString(".")
	var day []string
	if len(f.Daily.Max) > 0 {
		day = append(day, pick(lang, "Today up to ", "Heute bis ")+degrees(f.Daily.Max[0], lang))
	}
	if p, ok := rainChance(f, 0); ok && p >= 10 {
		day = append(day, fmt.Sprintf(pick(lang, "%d percent chance of rain", "Regenrisiko %d Prozent"), p))
	}
	if len(day) > 0 {
		b.WriteString(" " + strings.Join(day, ", ") + ".")
	}
	return b.String()
}

// rainChance is the day's maximum precipitation probability in percent.
func rainChance(f openMeteoForecast, i int) (int, bool) {
	if i < 0 || i >= len(f.Daily.Rain) {
		return 0, false
	}
	return int(math.Round(f.Daily.Rain[i])), true
}

func dayCode(f openMeteoForecast, i int) int {
	if i < len(f.Daily.Code) {
		return f.Daily.Code[i]
	}
	return -1
}

// dayName names forecast day i: "heute", "morgen", "übermorgen",
// "Samstag"; EN "today", "tomorrow", "the day after tomorrow", "Saturday".
// byWeekday names day 2 by its weekday (the question named it: "Freitag").
func dayName(lang string, i int, f openMeteoForecast, now time.Time, byWeekday bool) string {
	switch {
	case i == 0:
		return pick(lang, "today", "heute")
	case i == 1:
		return pick(lang, "tomorrow", "morgen")
	case i == 2 && !byWeekday:
		return pick(lang, "the day after tomorrow", "übermorgen")
	}
	wd := forecastBase(f, now).AddDate(0, 0, i).Weekday()
	if lang == "de" {
		return weekdaysDE[wd]
	}
	return wd.String()
}

// dayStart opens a sentence about day i: "Morgen", "Am Samstag",
// "Tomorrow", "On Saturday".
func dayStart(lang string, i int, f openMeteoForecast, now time.Time, byWeekday bool) string {
	name := dayName(lang, i, f, now, byWeekday)
	if i > 2 || (i == 2 && byWeekday) {
		return pick(lang, "On ", "Am ") + name
	}
	return upperFirst(name)
}

// tempRange speaks "4 bis 9 Grad", "minus 2 to 3 degrees".
func tempRange(lang string, lo, hi float64) string {
	n := int(math.Round(lo))
	low := strconv.Itoa(n)
	if n < 0 {
		low = "minus " + strconv.Itoa(-n)
	}
	return low + pick(lang, " to ", " bis ") + degrees(hi, lang)
}

// dayFacts is "Regen, 4 bis 9 Grad, Regenrisiko 80 Prozent".
func dayFacts(lang string, f openMeteoForecast, i int) string {
	var parts []string
	if c := conditionText(dayCode(f, i), lang); c != "" {
		parts = append(parts, c)
	}
	parts = append(parts, tempRange(lang, f.Daily.Min[i], f.Daily.Max[i]))
	if p, ok := rainChance(f, i); ok && p >= 10 {
		parts = append(parts, fmt.Sprintf(pick(lang, "%d percent chance of rain", "Regenrisiko %d Prozent"), p))
	}
	return strings.Join(parts, ", ")
}

func dayText(lang, city string, f openMeteoForecast, i int, byWeekday bool) string {
	return fmt.Sprintf("%s in %s: %s.", dayStart(lang, i, f, nowFunc(), byWeekday), city, dayFacts(lang, f, i))
}

func daysText(lang, city string, f openMeteoForecast, days []int) string {
	now := nowFunc()
	parts := make([]string, 0, len(days))
	for _, i := range days {
		parts = append(parts, upperFirst(dayName(lang, i, f, now, false))+" "+dayFacts(lang, f, i)+".")
	}
	return pick(lang, "Weather in ", "Wetter in ") + city + ": " + strings.Join(parts, " ")
}

// umbrellaAdvice closes a rain answer.
func umbrellaAdvice(lang string, p int) string {
	switch {
	case p >= 50:
		return pick(lang, "Better take an umbrella.", "Nimm besser einen Schirm mit.")
	case p >= 20:
		return pick(lang, "An umbrella can't hurt.", "Ein Schirm kann nicht schaden.")
	}
	return pick(lang, "You probably won't need an umbrella.", "Einen Schirm brauchst du wohl nicht.")
}

func rainNowText(lang, city string, f openMeteoForecast) string {
	var b strings.Builder
	if wetCode(f.Current.WeatherCode) || f.Current.Precipitation > 0 {
		cond := conditionText(f.Current.WeatherCode, lang)
		if cond == "" || !wetCode(f.Current.WeatherCode) {
			cond = pick(lang, "rain", "Regen")
		}
		fmt.Fprintf(&b, pick(lang, "Yes, there is %s in %s right now.", "Ja, in %[2]s gibt es gerade %[1]s."), cond, city)
		b.WriteString(" " + umbrellaAdvice(lang, 100))
		return b.String()
	}
	fmt.Fprintf(&b, pick(lang, "It is not raining in %s right now.", "In %s regnet es gerade nicht."), city)
	if p, ok := rainChance(f, 0); ok {
		fmt.Fprintf(&b, pick(lang, " Today %d percent chance of rain.", " Heute Regenrisiko %d Prozent."), p)
		b.WriteString(" " + umbrellaAdvice(lang, p))
	}
	return b.String()
}

func rainDayText(lang, city string, f openMeteoForecast, i int, byWeekday bool) string {
	start := dayStart(lang, i, f, nowFunc(), byWeekday)
	p, ok := rainChance(f, i)
	if !ok {
		p = 0
		if wetCode(dayCode(f, i)) {
			p = 60
		}
		return fmt.Sprintf("%s in %s: %s.", start, city, dayFacts(lang, f, i)) + " " + umbrellaAdvice(lang, p)
	}
	return fmt.Sprintf(pick(lang, "%s in %s: %d percent chance of rain, %s.", "%s in %s: Regenrisiko %d Prozent, %s."),
		start, city, p, tempRange(lang, f.Daily.Min[i], f.Daily.Max[i])) + " " + umbrellaAdvice(lang, p)
}

// weatherCard is the {"card":{"kind":"weather"}} payload.
func weatherCard(lang, city string, f openMeteoForecast, days []int, rain bool) *Card {
	data := map[string]any{"city": city, "rain": rain}
	show := days
	if len(days) == 0 {
		data["now"] = map[string]any{
			"temp":      int(math.Round(f.Current.Temperature)),
			"feelsLike": int(math.Round(f.Current.Apparent)),
			"code":      f.Current.WeatherCode,
			"condition": conditionText(f.Current.WeatherCode, lang),
			"wind":      int(math.Round(f.Current.Wind)),
		}
		if forecastLen(f) > 0 {
			show = []int{0}
		}
	}
	now := nowFunc()
	list := make([]map[string]any, 0, len(show))
	for _, i := range show {
		d := map[string]any{
			"label":     upperFirst(dayName(lang, i, f, now, false)),
			"max":       int(math.Round(f.Daily.Max[i])),
			"min":       int(math.Round(f.Daily.Min[i])),
			"code":      dayCode(f, i),
			"condition": conditionText(dayCode(f, i), lang),
		}
		if i < len(f.Daily.Time) {
			d["date"] = f.Daily.Time[i]
		}
		if p, ok := rainChance(f, i); ok {
			d["rain"] = p
		}
		list = append(list, d)
	}
	data["days"] = list
	return &Card{Kind: "weather", Data: data}
}

// utcOffset speaks a zone offset: "UTC+9", "UTC-3:30", "UTC".
func utcOffset(t time.Time) string {
	_, off := t.Zone()
	if off == 0 {
		return "UTC"
	}
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	h, m := off/3600, (off%3600)/60
	if m != 0 {
		return fmt.Sprintf("UTC%s%d:%02d", sign, h, m)
	}
	return fmt.Sprintf("UTC%s%d", sign, h)
}

// longDate speaks "Mittwoch, der 11. November 2026" / "Wednesday, November
// 11, 2026".
func longDate(t time.Time, lang string) string {
	if lang == "de" {
		return fmt.Sprintf("%s, der %d. %s %d", weekdaysDE[t.Weekday()], t.Day(), monthsDE[t.Month()-1], t.Year())
	}
	return fmt.Sprintf("%s, %s %d, %d", t.Weekday(), t.Month(), t.Day(), t.Year())
}

func answerTime(t *Turn, req realtimeRequest) Reply {
	local := turnNow(t)
	lang := t.Lang
	if req.City == "" {
		var parts []string
		if req.Clock {
			parts = append(parts, fmt.Sprintf(pick(lang, "It is %s.", "Es ist %s."), formatClock(local, lang)))
		}
		if req.Date {
			parts = append(parts, fmt.Sprintf(pick(lang, "Today is %s.", "Heute ist %s."), longDate(local, lang)))
		}
		if req.Zone {
			parts = append(parts, fmt.Sprintf(pick(lang, "Time zone: %s (%s).", "Zeitzone: %s (%s)."), local.Location().String(), utcOffset(local)))
		}
		return Reply{Text: strings.Join(parts, " ")}
	}
	place, err := cachedGeocode(turnCtx(t), req.City, lang)
	if errors.Is(err, errPlaceNotFound) {
		return placeNotFound(lang, req.City)
	}
	if err != nil {
		log.Printf("realtime: geocode: %v", err)
		return realtimeDown(lang, "place")
	}
	loc, err := time.LoadLocation(place.tz)
	if place.tz == "" || err != nil {
		return Reply{Text: fmt.Sprintf(pick(lang, "I don't know the time zone of %s.", "Die Zeitzone von %s kenne ich nicht."), place.name)}
	}
	there := local.In(loc)
	var parts []string
	if req.Clock {
		parts = append(parts, fmt.Sprintf(pick(lang, "In %s it is %s.", "In %s ist es %s."), place.name, formatClock(there, lang)))
		if !req.Date {
			switch days := wallDaysBetween(local, there); {
			case days > 0:
				parts = append(parts, fmt.Sprintf(pick(lang, "It is already %s there.", "Dort ist schon %s."), weekdayName(there, lang)))
			case days < 0:
				parts = append(parts, fmt.Sprintf(pick(lang, "It is still %s there.", "Dort ist noch %s."), weekdayName(there, lang)))
			}
		}
	}
	if req.Date {
		parts = append(parts, fmt.Sprintf(pick(lang, "In %s it is %s.", "In %s ist heute %s."), place.name, longDate(there, lang)))
	}
	if req.Zone {
		parts = append(parts, fmt.Sprintf(pick(lang, "Time zone: %s (%s).", "Zeitzone: %s (%s)."), loc.String(), utcOffset(there)))
	}
	return Reply{Text: strings.Join(parts, " ")}
}

// wallDaysBetween compares the calendar dates of two wall clocks (each in
// its own zone): +1 when b's date is a day after a's.
func wallDaysBetween(a, b time.Time) int {
	ya, ma, da := a.Date()
	yb, mb, db := b.Date()
	ua := time.Date(ya, ma, da, 0, 0, 0, 0, time.UTC)
	ub := time.Date(yb, mb, db, 0, 0, 0, 0, time.UTC)
	return int(ub.Sub(ua).Hours() / 24)
}

func weekdayName(t time.Time, lang string) string {
	if lang == "de" {
		return weekdaysDE[t.Weekday()]
	}
	return t.Weekday().String()
}

func answerNews(t *Turn, req realtimeRequest) Reply {
	lang := t.Lang
	hs, source, err := fetchHeadlines(turnCtx(t), req.Topic, lang)
	if err != nil {
		log.Printf("realtime: news: %v", err)
		return realtimeDown(lang, "news")
	}
	if len(hs) == 0 {
		if req.Topic != "" {
			return Reply{Text: fmt.Sprintf(pick(lang, "I found no headlines about %s right now.", "Zu %s finde ich gerade keine Schlagzeilen."), req.Topic)}
		}
		return Reply{Text: pick(lang, "I found no headlines right now.", "Ich finde gerade keine Schlagzeilen.")}
	}
	var intro string
	switch {
	case req.Topic != "":
		intro = fmt.Sprintf(pick(lang, "Headlines about %s: ", "Schlagzeilen zu %s: "), req.Topic)
	case source == "tagesschau":
		intro = "Die Schlagzeilen der Tagesschau: "
	default:
		intro = pick(lang, "Top headlines: ", "Die aktuellen Schlagzeilen: ")
	}
	spoken := make([]string, 0, realtimeSpokenHeadlines)
	for _, h := range hs[:min(len(hs), realtimeSpokenHeadlines)] {
		spoken = append(spoken, sentence(h.Title))
	}
	items := hs[:min(len(hs), realtimeCardHeadlines)]
	return Reply{
		Text: intro + strings.Join(spoken, " "),
		Card: &Card{Kind: "news", Data: map[string]any{"source": source, "topic": req.Topic, "items": items}},
	}
}

func init() {
	registerTool(Tool{Name: "realtime", Run: func(t *Turn, raw json.RawMessage) Reply {
		var req realtimeRequest
		_ = json.Unmarshal(raw, &req)
		req.City = cleanPlace(req.City)
		req.Topic = cleanPlace(req.Topic)
		return answerRealtime(t, req)
	}})
	onPersonDeleted(func(id string) (string, int) { return "session", purgeRealtimePerson(id) })
}
