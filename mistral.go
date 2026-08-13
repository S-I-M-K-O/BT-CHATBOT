package main

import (
	"bytes"
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
	"sync"
	"time"
)

const (
	mistralDefaultBase  = "https://api.mistral.ai"
	mistralDefaultModel = "mistral-medium-latest"
)

var (
	imageAgentMu sync.Mutex
	imageAgentID string
)

func mistralKey() string  { return os.Getenv("MISTRAL_API_KEY") }
func mistralBase() string { return envOr("MISTRAL_BASE", mistralDefaultBase) }
func mistralModel() string {
	return envOr("MISTRAL_MODEL", mistralDefaultModel)
}
func mistralEnabled() bool { return mistralKey() != "" }

func genDir() string { return envOr("GEN_DIR", filepath.Join("data", "gen")) }

var realtimeWords = []string{
	"weather", "forecast", "temperature", "temp", "raining", "rain", "snow",
	"wind", "humidity", "sunny", "cloudy", "celsius", "degrees",
	"time", "clock", "date", "today",
	"news", "headlines", "headline", "breaking",
}

var imageGenWords = []string{
	"draw", "generate", "render", "create an image", "create a picture",
	"make an image", "make a picture", "paint", "illustration", "concept art",
}

var imageSearchWords = []string{
	"find", "search", "look up", "photo of", "photos of", "picture of",
	"pictures of", "image of", "images of", "show me a", "show me pictures",
	"show me some", "get a picture", "find a picture", "find a photo",
}

var imagePlainWords = []string{"image", "picture", "photo", "pic", "drawing"}

func wantsRealtime(q string) bool {
	q = strings.ToLower(q)
	for _, kw := range realtimeWords {
		if strings.Contains(q, kw) {
			return true
		}
	}
	return false
}

func classifyImageRequest(q string) string {
	q = strings.ToLower(q)
	for _, kw := range imageGenWords {
		if strings.Contains(q, kw) {
			return "generate"
		}
	}
	for _, kw := range imageSearchWords {
		if strings.Contains(q, kw) {
			return "search"
		}
	}
	for _, kw := range imagePlainWords {
		if strings.Contains(q, kw) {
			return "search"
		}
	}
	return ""
}

func mistralClient() *http.Client { return &http.Client{Timeout: 120 * time.Second} }

func mistralPost(path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, mistralBase()+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+mistralKey())
	resp, err := mistralClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mistral %s: %d: %s", path, resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	if out != nil {
		return json.Unmarshal(rb, out)
	}
	return nil
}

func mistralGetBytes(path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, mistralBase()+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+mistralKey())
	resp, err := mistralClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mistral %s: %d: %s", path, resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	return rb, nil
}

func handleMistralTools(w http.ResponseWriter, s *Settings, messages []chatMessage) {
	msgs := make([]any, 0, len(messages))
	for _, m := range messages {
		msgs = append(msgs, map[string]any{"role": m.Role, "content": m.Content})
	}
	tools := mistralTools()
	final := ""
	for round := 0; round < 5; round++ {
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
		if err := mistralPost("/v1/chat/completions", map[string]any{
			"model":               mistralModel(),
			"messages":            msgs,
			"tools":               tools,
			"tool_choice":         "auto",
			"temperature":         s.Temperature,
			"max_tokens":          s.MaxTokens,
			"parallel_tool_calls": true,
		}, &resp); err != nil {
			log.Printf("mistral chat error: %v", err)
			writeSSE(w, "My satellite link is down, Pilot. Mistral could not be reached — check that MISTRAL_API_KEY is set.")
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
			result := executeMistralTool(tc.Function.Name, tc.Function.Arguments)
			msgs = append(msgs, map[string]any{
				"role":         "tool",
				"tool_call_id": tc.ID,
				"name":         tc.Function.Name,
				"content":      result,
			})
		}
	}
	if final == "" {
		final = "No data recovered from the satellite, Pilot. Try again."
	}
	writeSSE(w, final)
}

func mistralTools() []any {
	return []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "get_time",
				"description": "Get the current local time and date.",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
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

func executeMistralTool(name, args string) string {
	var p struct {
		City  string `json:"city"`
		Topic string `json:"topic"`
	}
	_ = json.Unmarshal([]byte(args), &p)
	switch name {
	case "get_time":
		return getTimeTool()
	case "get_weather":
		return getWeatherTool(p.City)
	case "get_news":
		return getNewsTool(p.Topic)
	}
	return `{"error":"unknown tool"}`
}

func getTimeTool() string {
	now := time.Now()
	b, _ := json.Marshal(map[string]string{
		"date":     now.Format("Monday, January 2, 2006"),
		"time":     now.Format("3:04 PM"),
		"timezone": now.Location().String(),
	})
	return string(b)
}

func getWeatherTool(city string) string {
	if city == "" {
		return `{"error":"no city provided"}`
	}
	lat, lon, name, err := geocodeCity(city)
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(b)
	}
	u := fmt.Sprintf("https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f&current=temperature_2m,relative_humidity_2m,apparent_temperature,weather_code,wind_speed_10m&timezone=auto", lat, lon)
	var cur struct {
		Current struct {
			Temperature2m    float64 `json:"temperature_2m"`
			RelativeHumidity float64 `json:"relative_humidity_2m"`
			ApparentTemp     float64 `json:"apparent_temperature"`
			WeatherCode      int     `json:"weather_code"`
			WindSpeed10m     float64 `json:"wind_speed_10m"`
		} `json:"current"`
	}
	if err := httpGetJSON(u, &cur); err != nil {
		b, _ := json.Marshal(map[string]string{"error": "weather lookup failed: " + err.Error()})
		return string(b)
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

func geocodeCity(city string) (float64, float64, string, error) {
	u := "https://geocoding-api.open-meteo.com/v1/search?count=1&language=en&format=json&name=" + url.QueryEscape(city)
	var res struct {
		Results []struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
			Name      string  `json:"name"`
			Country   string  `json:"country"`
		} `json:"results"`
	}
	if err := httpGetJSON(u, &res); err != nil {
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

func getNewsTool(topic string) string {
	feedURL := "https://news.google.com/rss?hl=en-US&gl=US&ceid=US:en"
	if topic != "" {
		feedURL = "https://news.google.com/rss/search?q=" + url.QueryEscape(topic) + "&hl=en-US&gl=US&ceid=US:en"
	}
	body, err := httpGet(feedURL)
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": "news lookup failed: " + err.Error()})
		return string(b)
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
	for i, it := range feed.Channel.Item {
		if i >= 7 {
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

func httpGetWithTimeout(u string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func httpGet(u string) ([]byte, error) {
	return httpGetWithTimeout(u, 30*time.Second)
}

func httpGetJSON(u string, out any) error {
	b, err := httpGet(u)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func handleMistralImage(w http.ResponseWriter, q string) {
	sseBegin(w)
	writeSSEJSON(w, map[string]any{"intent": "image"})
	w.(http.Flusher).Flush()

	agentID, err := ensureImageAgent()
	if err != nil {
		log.Printf("mistral image agent error: %v", err)
		writeSSE(w, "My image fabricator is offline, Pilot. Check the Mistral API key.")
		return
	}
	var conv struct {
		ConversationID string `json:"conversation_id"`
		Outputs        []struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		} `json:"outputs"`
	}
	if err := mistralPost("/v1/conversations", map[string]any{
		"agent_id": agentID,
		"inputs":   q,
		"store":    false,
	}, &conv); err != nil {
		log.Printf("mistral image conversation error: %v", err)
		writeSSE(w, "Image synthesis failed, Pilot. "+err.Error())
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
		writeSSE(w, "The image came back empty, Pilot. Try a more specific request.")
		return
	}
	data, err := mistralGetBytes("/v1/files/" + fileID + "/content")
	if err != nil {
		log.Printf("mistral image download error: %v", err)
		writeSSE(w, "Could not retrieve the generated image, Pilot.")
		return
	}
	dir := genDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeSSE(w, "Could not store the generated image, Pilot.")
		return
	}
	name := fmt.Sprintf("hermes_%d.png", time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		writeSSE(w, "Could not store the generated image, Pilot.")
		return
	}

	sseBegin(w)
	streamSSE(w, text)
	writeSSEJSON(w, map[string]any{"image": "/gen/" + name})
	writeSSEJSON(w, map[string]any{"done": true})
	w.(http.Flusher).Flush()
}

func sseBegin(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}

func streamSSE(w http.ResponseWriter, text string) {
	for _, ch := range strings.Split(text, "") {
		writeSSEJSON(w, map[string]any{"delta": ch})
		w.(http.Flusher).Flush()
	}
}

func handleImageSearch(w http.ResponseWriter, q string) {
	sseBegin(w)
	writeSSEJSON(w, map[string]any{"intent": "image"})
	w.(http.Flusher).Flush()

	subject := extractImageSubject(q)
	if subject == "" {
		writeSSE(w, "What should I search for, Pilot?")
		return
	}
	u := "https://api.openverse.org/v1/images/?page_size=3&q=" + url.QueryEscape(subject)
	var res struct {
		Results []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"results"`
	}
	if err := httpGetJSON(u, &res); err != nil || len(res.Results) == 0 {
		writeSSE(w, "No pictures found on the network, Pilot. Try a different subject.")
		return
	}
	dir := genDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeSSE(w, "Could not store the pictures, Pilot.")
		return
	}
	var urls []string
	for i, r := range res.Results {
		if i >= 3 {
			break
		}
		data, err := httpGetWithTimeout(r.URL, 15*time.Second)
		if err != nil {
			log.Printf("image search download failed for %s: %v", r.URL, err)
			continue
		}
		ext := ".jpg"
		switch http.DetectContentType(data) {
		case "image/png":
			ext = ".png"
		case "image/gif":
			ext = ".gif"
		case "image/webp":
			ext = ".webp"
		}
		name := fmt.Sprintf("search_%d_%d%s", time.Now().UnixNano(), i, ext)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			continue
		}
		urls = append(urls, "/gen/"+name)
	}
	if len(urls) == 0 {
		writeSSE(w, "Found pictures but could not download them, Pilot.")
		return
	}
	streamSSE(w, fmt.Sprintf("Found %d pictures of %s, Pilot.", len(urls), subject))
	for _, u := range urls {
		writeSSEJSON(w, map[string]any{"image": u})
	}
	writeSSEJSON(w, map[string]any{"done": true})
	w.(http.Flusher).Flush()
}

func extractImageSubject(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	for _, p := range []string{"show me", "find me", "look up", "search for", "get me", "can you show", "show"} {
		if strings.HasPrefix(q, p) {
			q = strings.TrimSpace(q[len(p):])
			break
		}
	}
	drop := map[string]bool{
		"a": true, "an": true, "the": true, "of": true, "for": true, "me": true,
		"picture": true, "pictures": true, "photo": true, "photos": true,
		"image": true, "images": true, "some": true, "any": true, "please": true,
	}
	var kept []string
	for _, w := range strings.Fields(q) {
		if !drop[w] {
			kept = append(kept, w)
		}
	}
	if len(kept) == 0 {
		return strings.TrimSpace(q)
	}
	return strings.Join(kept, " ")
}

func ensureImageAgent() (string, error) {
	imageAgentMu.Lock()
	defer imageAgentMu.Unlock()
	if imageAgentID != "" {
		return imageAgentID, nil
	}
	var agent struct {
		ID string `json:"id"`
	}
	err := mistralPost("/v1/agents", map[string]any{
		"model":        mistralModel(),
		"name":         "HERMES Image Generator",
		"description":  "Generates images for the HERMES voice assistant.",
		"instructions": "You generate images on demand. Use the image_generation tool whenever the user asks to see an image or picture. Keep any spoken reply to one short sentence.",
		"tools":        []any{map[string]any{"type": "image_generation"}},
	}, &agent)
	if err != nil {
		return "", err
	}
	if agent.ID == "" {
		return "", fmt.Errorf("empty agent id from Mistral")
	}
	imageAgentID = agent.ID
	log.Printf("created Mistral image agent %s", agent.ID)
	return agent.ID, nil
}
