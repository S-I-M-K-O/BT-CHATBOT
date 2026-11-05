package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// newMux wires every route. Kept separate from main() so tests can exercise
// the full handler stack.
func newMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat", handleChat)
	mux.HandleFunc("/api/tts", handleTTS)
	mux.HandleFunc("/api/settings", handleSettings)
	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/gen/", handleGen)
	mux.Handle("/", staticHandler(envOr("STATIC_DIR", "static")))
	return recoverMiddleware(logMiddleware(securityHeaders(originGuard(mux))))
}

// ---------------------------------------------------------------------------
// Middleware

// statusRecorder captures the status code while staying a Flusher so the SSE
// stream keeps working behind the logging middleware.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/api/health" {
			return // docker healthcheck noise
		}
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}

// recoverMiddleware turns a handler panic into a logged 500 instead of a
// dropped connection.
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				log.Printf("panic serving %s %s: %v\n%s", r.Method, r.URL.Path, p, debug.Stack())
				writeJSONError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// hostAllowed reports whether the Host header names this server rather than
// an attacker's domain (DNS rebinding). IP literals and localhost are always
// accepted so LAN access by address keeps working; extra hostnames can be
// listed in ALLOWED_HOSTS (comma-separated, e.g. "hermes.lan,pi.local").
func hostAllowed(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || net.ParseIP(host) != nil {
		return true
	}
	for _, h := range strings.Split(os.Getenv("ALLOWED_HOSTS"), ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" && h == host {
			return true
		}
	}
	return false
}

// originGuard protects the unauthenticated API from other websites: it
// rejects unknown Host headers (DNS rebinding) and cross-origin state-changing
// requests (CSRF — e.g. a page silently POSTing new settings or chat/TTS
// requests to localhost).
func originGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host) {
			writeJSONError(w, http.StatusForbidden, "host not allowed; add it to ALLOWED_HOSTS")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
				writeJSONError(w, http.StatusForbidden, "cross-site request blocked")
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
				u, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(u.Host, r.Host) {
					writeJSONError(w, http.StatusForbidden, "cross-origin request blocked")
					return
				}
			} else if origin == "null" {
				writeJSONError(w, http.StatusForbidden, "cross-origin request blocked")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src 'self' data: https://fonts.gstatic.com; " +
	"img-src 'self' data: blob:; " +
	"media-src 'self' blob: data:; " +
	"connect-src 'self'; " +
	"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("Permissions-Policy", "microphone=(self), camera=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

// staticHandler serves the frontend. HTML/JS/CSS are revalidated on every load
// (no-cache still allows 304s) so updates show up without a hard refresh.
func staticHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.ToLower(path.Ext(r.URL.Path)) {
		case "", ".html", ".js", ".css", ".mjs", ".json", ".webmanifest":
			w.Header().Set("Cache-Control", "no-cache")
		default:
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		fs.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Generated images

var genNamePattern = regexp.MustCompile(`^(?:hermes_\d+|search_\d+_\d+)\.(png|jpg|gif|webp)$`)

var genContentTypes = map[string]string{
	"png": "image/png", "jpg": "image/jpeg", "gif": "image/gif", "webp": "image/webp",
}

func handleGen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/gen/")
	m := genNamePattern.FindStringSubmatch(name)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	fh, err := os.Open(filepath.Join(genDir(), name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer fh.Close()
	info, err := fh.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", genContentTypes[m[1]])
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	http.ServeContent(w, r, name, info.ModTime(), fh)
}

// genMaxAge is how long generated / searched images are kept.
const genMaxAge = 24 * time.Hour

func cleanupGenDir() {
	dir := genDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	now := time.Now()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > genMaxAge {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				log.Printf("gen cleanup: %v", err)
			}
		}
	}
}

// runGenCleanup prunes the gen dir now and then every interval until ctx ends.
func runGenCleanup(ctx context.Context, interval time.Duration) {
	cleanupGenDir()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cleanupGenDir()
		}
	}
}

// ---------------------------------------------------------------------------
// Status

type ollamaStatus struct {
	OK     bool     `json:"ok"`
	Models []string `json:"models"`
	Error  string   `json:"error,omitempty"`
}

type statusResponse struct {
	Ollama ollamaStatus `json:"ollama"`
	Piper  struct {
		OK bool `json:"ok"`
	} `json:"piper"`
	Mistral struct {
		Enabled bool `json:"enabled"`
	} `json:"mistral"`
	Voices []voiceInfo `json:"voices"`
}

var statusClient = &http.Client{Timeout: 2 * time.Second}

func probeOllama(ctx context.Context) ollamaStatus {
	st := ollamaStatus{Models: []string{}}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ollamaBase()+"/api/tags", nil)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	resp, err := statusClient.Do(req)
	if err != nil {
		st.Error = "Ollama is not reachable at " + ollamaBase()
		return st
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		st.Error = "Ollama answered HTTP " + resp.Status
		return st
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&tags); err != nil {
		st.Error = "could not parse Ollama model list"
		return st
	}
	for _, m := range tags.Models {
		if m.Name != "" {
			st.Models = append(st.Models, m.Name)
		}
	}
	st.OK = true
	return st
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET")
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var resp statusResponse
	resp.Ollama = probeOllama(r.Context())
	resp.Piper.OK = piperAvailable()
	resp.Mistral.Enabled = mistralEnabled()
	resp.Voices = listVoices()
	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// HTTP clients

var (
	ollamaClientOnce sync.Once
	ollamaHTTP       *http.Client
)

// ollamaClient has no total timeout (replies stream for as long as the model
// talks) but bounded dial and response-header waits. Cancellation comes from
// the request context.
func ollamaClient() *http.Client {
	ollamaClientOnce.Do(func() {
		ollamaHTTP = &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ResponseHeaderTimeout: 3 * time.Minute, // cold model loads can be slow
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConns:          10,
		}}
	})
	return ollamaHTTP
}

func ollamaBase() string {
	return strings.TrimRight(envOr("OLLAMA_BASE", "http://localhost:11434"), "/")
}
