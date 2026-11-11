package hermes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// fetch.go holds the plain HTTP GET helpers for fixed public hosts
// (Open-Meteo, the news feeds, Openverse). Search results from third-party
// hosts use the SSRF-safe imageHTTP client in images.go instead.

// maxDownloadBytes caps bodies fetched with httpGet so a huge response
// cannot blow up memory.
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
