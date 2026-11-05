package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata" // the slim Docker image may not ship a zoneinfo database
)

// External data sources. Variables so tests can point them at fakes.
var (
	openMeteoForecastURL = "https://api.open-meteo.com/v1/forecast"
	openMeteoGeocodeURL  = "https://geocoding-api.open-meteo.com/v1/search"
	newsFeedURL          = "https://news.google.com/rss"
)

// mistralToolRounds bounds the tool-calling loop.
const mistralToolRounds = 5

// handleMistralTools answers a real-time question with Mistral function
// calling. clientTZ is the browser's IANA time zone (may be empty).
func handleMistralTools(ctx context.Context, w http.ResponseWriter, s *Settings, messages []chatMessage, clientTZ string) {
	sse := newSSE(w)
	p := phrasesFor(s.PiperVoice)
	msgs := make([]any, 0, len(messages))
	for _, m := range messages {
		msgs = append(msgs, map[string]any{"role": m.Role, "content": m.Content})
	}
	tools := mistralTools()
	final := ""
	for round := 0; round < mistralToolRounds; round++ {
		var resp struct {
			Choices []struct {
				Message struct {
					Role      string  `json:"role"`
					Content   *string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := mistralPost(ctx, "/v1/chat/completions", map[string]any{
			"model":               mistralModel(),
			"messages":            msgs,
			"tools":               tools,
			"tool_choice":         "auto",
			"temperature":         s.Temperature,
			"max_tokens":          s.MaxTokens,
			"parallel_tool_calls": true,
		}, &resp); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("mistral chat error: %v", err)
			sse.fail(satelliteFailure(p, err))
			return
		}
		if len(resp.Choices) == 0 {
			break
		}
		msg := resp.Choices[0].Message
		msgs = append(msgs, map[string]any{
			"role":       "assistant",
			"content":    msg.Content,
			"tool_calls": msg.ToolCalls,
		})
		if len(msg.ToolCalls) == 0 {
			if msg.Content != nil {
				final = *msg.Content
			}
			break
		}
		for _, tc := range msg.ToolCalls {
			result := executeMistralTool(ctx, tc.Function.Name, tc.Function.Arguments, voiceLang(s.PiperVoice), clientTZ)
			if ctx.Err() != nil {
				return
			}
			msgs = append(msgs, map[string]any{
				"role":         "tool",
				"tool_call_id": tc.ID,
				"name":         tc.Function.Name,
				"content":      result,
			})
		}
	}
	if strings.TrimSpace(final) == "" {
		sse.fail(p.noData)
		return
	}
	sse.reply(final)
}

func mistralTools() []any {
	return []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "get_time",
				"description": "Get the current date and time with its UTC offset. Without arguments it returns the user's local time; pass an IANA time zone to get the time somewhere else.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"timezone": map[string]any{"type": "string", "description": "Optional IANA time zone for another place, e.g. Asia/Tokyo or America/New_York. Omit for the user's own time."},
					},
				},
			},
		},
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "get_weather",
				"description": "Get the current weather for a city: temperature, feels-like, humidity, wind speed and conditions.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"city": map[string]any{"type": "string", "description": "City name, e.g. London or Tokyo."},
					},
					"required": []string{"city"},
				},
			},
		},
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "get_news",
				"description": "Get the latest top news headlines. Optionally pass a topic to filter the news.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"topic": map[string]any{"type": "string", "description": "Optional news topic, e.g. technology or sports."},
					},
				},
			},
		},
	}
}

func executeMistralTool(ctx context.Context, name, args, lang, clientTZ string) string {
	var p struct {
		City     string `json:"city"`
		Topic    string `json:"topic"`
		Timezone string `json:"timezone"`
	}
	_ = json.Unmarshal([]byte(args), &p)
	switch name {
	case "get_time":
		return getTimeTool(time.Now(), p.Timezone, clientTZ)
	case "get_weather":
		return getWeatherTool(ctx, p.City, lang)
	case "get_news":
		return getNewsTool(ctx, p.Topic, lang)
	}
	return `{"error":"unknown tool"}`
}

// userLocation resolves the user's time zone: the browser's zone sent with the
// request, then HERMES_TZ / TZ, then the server's local zone.
func userLocation(clientTZ string) *time.Location {
	for _, name := range []string{clientTZ, os.Getenv("HERMES_TZ"), os.Getenv("TZ")} {
		name = strings.TrimSpace(name)
		if name == "" || name == "Local" {
			continue
		}
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.Local
}

// getTimeTool reports now in the requested zone (or the user's zone) with an
// explicit UTC offset, so the model never has to guess what "Local" means.
func getTimeTool(now time.Time, requestedTZ, clientTZ string) string {
	out := map[string]string{}
	loc := userLocation(clientTZ)
	if tz := strings.TrimSpace(requestedTZ); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		} else {
			out["error"] = "unknown time zone " + tz + "; showing the user's local time instead"
		}
	}
	t := now.In(loc)
	abbr, _ := t.Zone()
	name := loc.String()
	if name == "Local" || name == "" {
		name = abbr
	}
	out["date"] = t.Format("Monday, January 2, 2006")
	out["time"] = t.Format("15:04")
	out["time_12h"] = t.Format("3:04 PM")
	out["timezone"] = name
	out["zone_abbreviation"] = abbr
	out["utc_offset"] = t.Format("-07:00")
	out["iso8601"] = t.Format(time.RFC3339)
	b, _ := json.Marshal(out)
	return string(b)
}

func toolError(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}

func getWeatherTool(ctx context.Context, city, lang string) string {
	if city == "" {
		return `{"error":"no city provided"}`
	}
	lat, lon, name, err := geocodeCity(ctx, city, lang)
	if err != nil {
		return toolError(err.Error())
	}
	u := fmt.Sprintf("%s?latitude=%.4f&longitude=%.4f&current=temperature_2m,relative_humidity_2m,apparent_temperature,weather_code,wind_speed_10m&timezone=auto", openMeteoForecastURL, lat, lon)
	var cur struct {
		Current struct {
			Temperature2m    float64 `json:"temperature_2m"`
			RelativeHumidity float64 `json:"relative_humidity_2m"`
			ApparentTemp     float64 `json:"apparent_temperature"`
			WeatherCode      int     `json:"weather_code"`
			WindSpeed10m     float64 `json:"wind_speed_10m"`
		} `json:"current"`
	}
	if err := httpGetJSON(ctx, u, &cur); err != nil {
		return toolError("weather lookup failed: " + err.Error())
	}
	b, _ := json.Marshal(map[string]any{
		"city":             name,
		"temperature_c":    cur.Current.Temperature2m,
		"feels_like_c":     cur.Current.ApparentTemp,
		"humidity_percent": cur.Current.RelativeHumidity,
		"wind_kmh":         cur.Current.WindSpeed10m,
		"condition":        wmoCondition(cur.Current.WeatherCode),
	})
	return string(b)
}

func geocodeCity(ctx context.Context, city, lang string) (float64, float64, string, error) {
	l := "en"
	if lang == "de" {
		l = "de"
	}
	u := openMeteoGeocodeURL + "?count=1&language=" + l + "&format=json&name=" + url.QueryEscape(city)
	var res struct {
		Results []struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
			Name      string  `json:"name"`
			Country   string  `json:"country"`
		} `json:"results"`
	}
	if err := httpGetJSON(ctx, u, &res); err != nil {
		return 0, 0, "", err
	}
	if len(res.Results) == 0 {
		return 0, 0, "", fmt.Errorf("city not found: %s", city)
	}
	r := res.Results[0]
	name := r.Name
	if r.Country != "" {
		name += ", " + r.Country
	}
	return r.Latitude, r.Longitude, name, nil
}

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

func getNewsTool(ctx context.Context, topic, lang string) string {
	l, gl, ceid := "en-US", "US", "US:en"
	if lang == "de" {
		l, gl, ceid = "de-DE", "DE", "DE:de"
	}
	params := "hl=" + l + "&gl=" + gl + "&ceid=" + ceid
	feedURL := newsFeedURL + "?" + params
	if topic != "" {
		feedURL = newsFeedURL + "/search?q=" + url.QueryEscape(topic) + "&" + params
	}
	body, err := httpGet(ctx, feedURL)
	if err != nil {
		return toolError("news lookup failed: " + err.Error())
	}
	var feed struct {
		Channel struct {
			Item []struct {
				Title   string `xml:"title"`
				PubDate string `xml:"pubDate"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return `{"error":"could not parse news feed"}`
	}
	type newsItem struct {
		Title     string `json:"title"`
		Published string `json:"published"`
	}
	items := make([]newsItem, 0, 7)
	for _, it := range feed.Channel.Item {
		if len(items) >= 7 {
			break
		}
		if strings.TrimSpace(it.Title) == "" {
			continue
		}
		items = append(items, newsItem{Title: it.Title, Published: it.PubDate})
	}
	if len(items) == 0 {
		return `{"error":"no headlines found"}`
	}
	b, _ := json.Marshal(items)
	return string(b)
}

// maxDownloadBytes caps bodies fetched from the network (weather, news,
// generated images) so a huge response cannot blow up memory.
const maxDownloadBytes = 25 << 20 // 25 MB

// userAgent is sent on all outgoing HTTP requests. Some CDNs (notably Flickr)
// return HTTP 502 for bot-like agent strings, so keep it neutral.
const userAgent = "HERMES/1.0 (+https://github.com/S-I-M-K-O/BT-CHATBOT)"

// httpGetLimited fetches a URL with a User-Agent (some APIs like Openverse
// reject requests without one), honours ctx (client disconnects cancel the
// download) and refuses bodies larger than limit.
func httpGetLimited(ctx context.Context, u string, timeout time.Duration, limit int64) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}

func httpGetWithTimeout(ctx context.Context, u string, timeout time.Duration) ([]byte, error) {
	return httpGetLimited(ctx, u, timeout, maxDownloadBytes)
}

func httpGet(ctx context.Context, u string) ([]byte, error) {
	return httpGetWithTimeout(ctx, u, 30*time.Second)
}

func httpGetJSON(ctx context.Context, u string, out any) error {
	b, err := httpGet(ctx, u)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// imageExt maps a sniffed content type to a file extension, or "" when the
// data is not an image format we serve from /gen/.
func imageExt(data []byte) string {
	switch http.DetectContentType(data) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	return ""
}

// imageJobs limits concurrent picture searches / generations (network,
// memory and disk heavy). Further requests wait until a slot frees or their
// client goes away.
var imageJobs = make(chan struct{}, 2)

func acquireImageJob(ctx context.Context) bool {
	select {
	case imageJobs <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func releaseImageJob() { <-imageJobs }

// shouldResetImageAgent reports whether a /v1/conversations failure means the
// cached agent is gone. Rate limits, 5xx and timeouts keep the agent: creating
// a new one each time would pile up stale agents in the user's account.
func shouldResetImageAgent(err error) bool {
	ae, ok := err.(*mistralAPIError)
	if !ok {
		return false
	}
	switch ae.Status {
	case http.StatusNotFound:
		return true
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return strings.Contains(strings.ToLower(ae.Body), "agent")
	}
	return false
}

func handleMistralImage(ctx context.Context, w http.ResponseWriter, s *Settings, q string) {
	sse := newSSE(w)
	sse.event(map[string]any{"intent": "image"})

	p := phrasesFor(s.PiperVoice)
	if !acquireImageJob(ctx) {
		return
	}
	defer releaseImageJob()

	agentID, err := ensureImageAgent(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("mistral image agent error: %v", err)
		if isMistralAuthError(err) {
			sse.fail(p.satelliteKeyRejected)
			return
		}
		sse.fail(p.imageFabricator)
		return
	}
	var conv struct {
		ConversationID string `json:"conversation_id"`
		Outputs        []struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		} `json:"outputs"`
	}
	if err := mistralPost(ctx, "/v1/conversations", map[string]any{
		"agent_id": agentID,
		"inputs":   q,
		"store":    false,
	}, &conv); err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("mistral image conversation error: %v", err)
		if shouldResetImageAgent(err) {
			forgetImageAgent(agentID)
		}
		if isMistralAuthError(err) {
			sse.fail(p.satelliteKeyRejected)
			return
		}
		sse.fail(p.imageSynthesis)
		return
	}

	var text, fileID string
	for _, out := range conv.Outputs {
		if out.Type != "message.output" {
			continue
		}
		var plain string
		if json.Unmarshal(out.Content, &plain) == nil {
			text = plain
			continue
		}
		var chunks []struct {
			Type   string `json:"type"`
			Text   string `json:"text"`
			FileID string `json:"file_id"`
		}
		if json.Unmarshal(out.Content, &chunks) != nil {
			continue
		}
		for _, c := range chunks {
			switch {
			case c.Type == "tool_file" && c.FileID != "" && fileID == "":
				fileID = c.FileID
			case c.Type == "text" && c.Text != "":
				text += c.Text
			}
		}
	}
	if fileID == "" {
		sse.reply(p.imageEmpty)
		return
	}
	data, err := mistralGetBytes(ctx, "/v1/files/"+url.PathEscape(fileID)+"/content")
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("mistral image download error: %v", err)
		sse.fail(p.imageRetrieve)
		return
	}
	ext := imageExt(data)
	if ext == "" {
		log.Printf("mistral image: unexpected content type %s", http.DetectContentType(data))
		sse.fail(p.imageRetrieve)
		return
	}
	dir := genDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		sse.fail(p.imageStore)
		return
	}
	name := fmt.Sprintf("hermes_%d%s", time.Now().UnixNano(), ext)
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		log.Printf("mistral image store error: %v", err)
		sse.fail(p.imageStore)
		return
	}
	enforceGenBudget()
	if strings.TrimSpace(text) == "" {
		text = p.imageCaption
	}
	sse.text(text)
	sse.event(map[string]any{"image": "/gen/" + name})
	sse.done()
}
