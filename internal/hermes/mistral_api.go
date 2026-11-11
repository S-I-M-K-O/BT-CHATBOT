package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// This file holds the low-level Mistral API client: configuration, typed
// errors and the "is the key accepted" state shown in /api/status.

const (
	mistralDefaultBase  = "https://api.mistral.ai"
	mistralDefaultModel = "mistral-medium-latest"
)

func mistralKey() string  { return os.Getenv("MISTRAL_API_KEY") }
func mistralBase() string { return strings.TrimRight(envOr("MISTRAL_BASE", mistralDefaultBase), "/") }
func mistralModel() string {
	return envOr("MISTRAL_MODEL", mistralDefaultModel)
}
func mistralEnabled() bool { return mistralKey() != "" }

func genDir() string { return envOr("GEN_DIR", filepath.Join("data", "gen")) }

var mistralHTTP = &http.Client{Timeout: 120 * time.Second}

// mistralMaxResponse caps JSON responses from the Mistral API.
const mistralMaxResponse = 8 << 20

// mistralAPIError is a non-200 answer from the Mistral API. Callers inspect
// Status to tell a rejected key (401/403), a missing resource (404) and a
// transient failure (429/5xx) apart.
type mistralAPIError struct {
	Path   string
	Status int
	Body   string
}

func (e *mistralAPIError) Error() string {
	return fmt.Sprintf("mistral %s: %d: %s", e.Path, e.Status, e.Body)
}

// isMistralAuthError reports a rejected API key.
func isMistralAuthError(err error) bool {
	var ae *mistralAPIError
	return errors.As(err, &ae) && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden)
}

// mistralAuth remembers the last authentication failure so /api/status can
// report a configured-but-rejected key instead of "online".
var mistralAuth struct {
	sync.Mutex
	key string // the key that was rejected
	err string
}

// noteMistralResult records the outcome of an API call: a 401/403 marks the
// current key as rejected, any successful call clears that mark.
func noteMistralResult(err error) {
	key := mistralKey()
	mistralAuth.Lock()
	defer mistralAuth.Unlock()
	switch {
	case err == nil:
		mistralAuth.key, mistralAuth.err = "", ""
	case isMistralAuthError(err):
		mistralAuth.key, mistralAuth.err = key, "Mistral rejected MISTRAL_API_KEY (HTTP 401/403)."
	}
}

// mistralAuthError returns a description of why the configured key was
// rejected, or "" when it was not (or the key has changed since).
func mistralAuthError() string {
	mistralAuth.Lock()
	defer mistralAuth.Unlock()
	if mistralAuth.err == "" || mistralAuth.key != mistralKey() {
		return ""
	}
	return mistralAuth.err
}

func mistralDo(req *http.Request, limit int64) ([]byte, error) {
	req.Header.Set("Authorization", "Bearer "+mistralKey())
	resp, err := mistralHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body := strings.TrimSpace(string(rb))
		if len(body) > 500 {
			body = body[:500]
		}
		apiErr := &mistralAPIError{Path: req.URL.Path, Status: resp.StatusCode, Body: body}
		noteMistralResult(apiErr)
		return nil, apiErr
	}
	if int64(len(rb)) > limit {
		return nil, fmt.Errorf("mistral %s: response exceeds %d bytes", req.URL.Path, limit)
	}
	noteMistralResult(nil)
	return rb, nil
}

func mistralPost(ctx context.Context, path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mistralBase()+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	rb, err := mistralDo(req, mistralMaxResponse)
	if err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(rb, out)
	}
	return nil
}

func mistralGet(ctx context.Context, path string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mistralBase()+path, nil)
	if err != nil {
		return nil, err
	}
	return mistralDo(req, limit)
}

func mistralGetBytes(ctx context.Context, path string) ([]byte, error) {
	return mistralGet(ctx, path, maxDownloadBytes)
}

// satelliteFailure picks the user-facing message for a failed Mistral call.
func satelliteFailure(p phrases, err error) string {
	if isMistralAuthError(err) {
		return p.satelliteKeyRejected
	}
	return p.satelliteDown
}
