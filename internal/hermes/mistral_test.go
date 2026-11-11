package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeOpenverseResults(t *testing.T, n int) func(string, any) error {
	t.Helper()
	return func(u string, out any) error {
		var items []map[string]string
		for i := 0; i < n; i++ {
			items = append(items, map[string]string{
				"url":       "http://img.test/pic.jpg",
				"thumbnail": "http://img.test/thumb/pic.jpg",
				"title":     "pic",
			})
		}
		b, err := json.Marshal(map[string]any{"results": items})
		if err != nil {
			return err
		}
		return json.Unmarshal(b, out)
	}
}

func fakeImageBytes(u string, d time.Duration) ([]byte, error) {
	return []byte{0xFF, 0xD8, 0xFF, 0xE0}, nil // JPEG magic bytes
}

// TestImageSearchStream runs the full picture-search SSE flow with a fake
// Openverse API and image downloader, guarding the frontend contract:
// an "intent":"image" event followed by one "image" event per result.
func TestImageSearchStream(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GEN_DIR", dir)

	rec := httptest.NewRecorder()
	handleImageSearchWith(rec, defaultSettings(), "show me a picture of a lion",
		fakeOpenverseResults(t, 3), fakeImageBytes)

	body := rec.Body.String()
	if !strings.Contains(body, `"intent":"image"`) {
		t.Error("expected intent:image event in SSE stream")
	}
	if got := strings.Count(body, `"image":"/gen/`); got != 3 {
		t.Errorf("expected 3 image events, got %d", got)
	}
	if !strings.Contains(body, `"done":true`) {
		t.Error("expected done event in SSE stream")
	}
	files, err := filepath.Glob(filepath.Join(dir, "search_*"))
	if err != nil || len(files) != 3 {
		t.Errorf("expected 3 downloaded images on disk, got %d (err %v)", len(files), err)
	}
}

// TestImageSearchThumbnailFallback ensures that when the source host fails
// (e.g. Flickr's 502s) but Openverse's own thumbnail CDN works, the picture
// still gets served instead of being silently dropped.
func TestImageSearchThumbnailFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GEN_DIR", dir)

	sourceFails := func(u string, d time.Duration) ([]byte, error) {
		if strings.Contains(u, "thumb") {
			return fakeImageBytes(u, d)
		}
		return nil, os.ErrPermission
	}
	rec := httptest.NewRecorder()
	handleImageSearchWith(rec, defaultSettings(), "find a picture of a cat",
		fakeOpenverseResults(t, 3), sourceFails)

	body := rec.Body.String()
	if got := strings.Count(body, `"image":"/gen/`); got != 3 {
		t.Errorf("expected 3 images via thumbnail fallback, got %d", got)
	}
}

// TestImageSearchDownloadFailure ensures a download failure yields a readable
// message and no image events (so the UI shows its "no pictures" fallback
// instead of silently dropping the request).
func TestImageSearchDownloadFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GEN_DIR", dir)

	failingBytes := func(u string, d time.Duration) ([]byte, error) {
		return nil, os.ErrPermission
	}
	rec := httptest.NewRecorder()
	handleImageSearchWith(rec, defaultSettings(), "find a picture of a cat",
		fakeOpenverseResults(t, 1), failingBytes)

	body := rec.Body.String()
	if strings.Contains(body, `"image":"/gen/`) {
		t.Error("did not expect any image events when downloads fail")
	}
	if !strings.Contains(body, `"done":true`) {
		t.Error("expected the stream to close with a done event")
	}
}

// TestImageSearchEmptySubject guards the no-subject path (e.g. "zeig mir ein"
// with nothing after the prefix).
func TestImageSearchEmptySubject(t *testing.T) {
	rec := httptest.NewRecorder()
	handleImageSearchWith(rec, defaultSettings(), "zeig mir ein",
		fakeOpenverseResults(t, 1), fakeImageBytes)

	body := rec.Body.String()
	if strings.Contains(body, `"image":"/gen/`) {
		t.Error("did not expect image events for an empty subject")
	}
}

// TestHTTPGetUserAgent pins the exact User-Agent sent to external hosts. Some
// CDNs (e.g. Flickr) answer HTTP 502 to bot-like agent strings, which silently
// breaks image downloads. If the UA changes, this test forces a review.
func TestHTTPGetUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if _, err := httpGetWithTimeout(context.Background(), srv.URL, 5*time.Second); err != nil {
		t.Fatalf("httpGetWithTimeout: %v", err)
	}
	if gotUA != userAgent {
		t.Errorf("expected User-Agent %q, got %q", userAgent, gotUA)
	}
}

// TestHTTPGetSizeLimit ensures oversized responses are rejected with an error
// instead of being silently truncated (which corrupted image files before).
func TestHTTPGetSizeLimit(t *testing.T) {
	big := bytes.Repeat([]byte("x"), maxDownloadBytes+10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(big)
	}))
	defer srv.Close()

	if _, err := httpGetWithTimeout(context.Background(), srv.URL, 5*time.Second); err == nil {
		t.Error("expected error for oversized response, got nil")
	}
}
