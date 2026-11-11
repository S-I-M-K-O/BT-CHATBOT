package hermes

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// secrets.json keeps credentials out of settings.json, which the UI reads
// back verbatim. Nothing in here is ever returned by an API or logged.
type secretsFile struct {
	Version            int    `json:"version"`         // 1
	HAURL              string `json:"haUrl,omitempty"` // bound to HAToken: changing one without the other clears the token
	HAToken            string `json:"haToken,omitempty"`
	GoogleClientID     string `json:"googleClientId,omitempty"`
	GoogleClientSecret string `json:"googleClientSecret,omitempty"`
	LockPINHash        string `json:"lockPinHash,omitempty"`   // "pbkdf2-sha256$<iter>$<salt b64>$<hash b64>"
	CalendarOwner      string `json:"calendarOwner,omitempty"` // person id ("" = household / single user)
}

var secretsStore = &fileStore[secretsFile]{
	Name:    "secrets.json",
	Version: 1,
	New:     func() *secretsFile { return &secretsFile{Version: 1} },
}

// loadSecrets returns a copy of the stored secrets (without env overrides).
func loadSecrets() secretsFile {
	var out secretsFile
	_ = secretsStore.View(func(st *secretsFile) error {
		out = *st
		return nil
	})
	return out
}

// updateSecrets changes the stored secrets atomically (0600).
func updateSecrets(fn func(s *secretsFile) error) error {
	return secretsStore.Update(fn)
}

// Env overrides win over secrets.json, field by field, so Docker users can
// keep everything in .env:
//
//   - HA_URL beats the stored URL, HA_TOKEN beats the stored token.
//   - The stored token is bound to the stored URL: with HA_URL set and no
//     HA_TOKEN, the stored token is used only when it was saved for that same
//     URL (POST /api/integrations/ha binds it), so a token typed in for one
//     host is never sent to another.
//   - GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET each beat the stored value.
//   - The PIN has no env override.

func haURL() string {
	if v := strings.TrimSpace(os.Getenv("HA_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return strings.TrimRight(loadSecrets().HAURL, "/")
}

func haToken() string {
	if v := strings.TrimSpace(os.Getenv("HA_TOKEN")); v != "" {
		return v
	}
	s := loadSecrets()
	if env := strings.TrimSpace(os.Getenv("HA_URL")); env != "" && !sameHAURL(env, s.HAURL) {
		return ""
	}
	return s.HAToken
}

func googleClient() (id, secret string) {
	s := loadSecrets()
	id, secret = s.GoogleClientID, s.GoogleClientSecret
	if v := strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_ID")); v != "" {
		id = v
	}
	if v := strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_SECRET")); v != "" {
		secret = v
	}
	return id, secret
}

func lockPINSet() bool { return loadSecrets().LockPINHash != "" }

// checkLockPIN verifies pin against the stored hash in constant time. No PIN
// set means nothing can unlock (the lock cannot be on without one).
func checkLockPIN(pin string) bool {
	h := loadSecrets().LockPINHash
	if h == "" || pin == "" {
		return false
	}
	return verifyPINHash(h, pin)
}

// PIN hashing. crypto/pbkdf2 arrived in Go 1.24 and this module stays on
// 1.22, so PBKDF2-HMAC-SHA256 is spelled out here (RFC 8018, one block: the
// derived key is exactly one SHA-256 output).

const (
	pinHashIterations = 200_000
	pinHashMaxIter    = 5_000_000 // a tampered file must not stall the server
	pinHashScheme     = "pbkdf2-sha256"
)

// pinHashIter is a variable so tests can hash quickly.
var pinHashIter = pinHashIterations

func pbkdf2SHA256(password, salt []byte, iter int) []byte {
	mac := hmac.New(sha256.New, password)
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], 1)
	mac.Write(salt)
	mac.Write(idx[:])
	u := mac.Sum(nil)
	out := append([]byte(nil), u...)
	for i := 1; i < iter; i++ {
		mac.Reset()
		mac.Write(u)
		u = mac.Sum(u[:0])
		for j := range out {
			out[j] ^= u[j]
		}
	}
	return out
}

// hashLockPIN returns the storable form of pin with a fresh random salt.
func hashLockPIN(pin string) (string, error) {
	if pin == "" {
		return "", errors.New("empty pin")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := pbkdf2SHA256([]byte(pin), salt, pinHashIter)
	enc := base64.RawStdEncoding
	return pinHashScheme + "$" + strconv.Itoa(pinHashIter) + "$" + enc.EncodeToString(salt) + "$" + enc.EncodeToString(sum), nil
}

func verifyPINHash(stored, pin string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != pinHashScheme {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 || iter > pinHashMaxIter {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(want) != sha256.Size {
		return false
	}
	got := pbkdf2SHA256([]byte(pin), salt, iter)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ---------------------------------------------------------------------------
// Integrations config API (Section 3.11)
//
//	GET          /api/integrations         masked view (booleans and the HA URL only)
//	POST|PUT     /api/integrations/ha      {"url","token"}  admin only, ≤4 KB
//	POST|PUT     /api/integrations/google  {"clientId","clientSecret"} or {"clientSecretJson"}  admin only, ≤16 KB
//	POST|PUT     /api/integrations/lock    {"pin","oldPin"}  admin only, ≤4 KB
//
// No response or error ever contains a token, a client secret, a client id
// or a PIN; errors carry a fixed English text, a machine code and a hint in
// the interface language (uiLang, else the selected voice's language).

const (
	integrationsBodyLimit = 4 << 10
	googleBodyLimit       = 16 << 10 // a client_secret_*.json is ~400 bytes
	haURLMaxLen           = 512
	haTokenMaxLen         = 2048 // HA long-lived tokens are ~180 chars
	googleFieldMaxLen     = 512
	lockPINMinLen         = 4
	lockPINMaxLen         = 12
	lockOldPINMaxLen      = 64
)

func init() {
	registerRoute("GET /api/integrations", handleIntegrationsGet)
	for _, m := range []string{"POST", "PUT"} {
		registerRoute(m+" /api/integrations/ha", handleIntegrationsHA)
		registerRoute(m+" /api/integrations/google", handleIntegrationsGoogle)
		registerRoute(m+" /api/integrations/lock", handleIntegrationsLock)
	}
}

// Change hooks let the feature files drop cached clients or tokens when the
// admin changes a credential (homeassistant.go: pinned client and status
// cache; gcal.go: the OAuth token belongs to the old client id). They run
// after the change is saved, outside every lock, and never see secrets.
var (
	secretHooksMu     sync.Mutex
	haChangeHooks     []func()
	googleChangeHooks []func()
)

// onHAConfigChange registers fn to run after the HA URL or token changed.
func onHAConfigChange(fn func()) {
	secretHooksMu.Lock()
	haChangeHooks = append(haChangeHooks, fn)
	secretHooksMu.Unlock()
}

// onGoogleClientChange registers fn to run after the Google client id or
// secret changed.
func onGoogleClientChange(fn func()) {
	secretHooksMu.Lock()
	googleChangeHooks = append(googleChangeHooks, fn)
	secretHooksMu.Unlock()
}

func runSecretHooks(hooks *[]func()) {
	secretHooksMu.Lock()
	fns := append([]func(){}, (*hooks)...)
	secretHooksMu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// ---------------------------------------------------------------------------
// Validation

// normalizeHAURL accepts http(s)://host[:port] with an optional trailing
// slash and returns it as scheme://host[:port] in lower case. No user info,
// path, query or fragment: the HA client builds its own paths.
func normalizeHAURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > haURLMaxLen {
		return "", errors.New("bad url")
	}
	for _, r := range raw {
		if r <= ' ' || r == 0x7f {
			return "", errors.New("bad url")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("bad url")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("bad url")
	}
	if u.Path != "" && u.Path != "/" {
		return "", errors.New("bad url")
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, "%/\\@") {
		return "", errors.New("bad url")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
			return "", errors.New("bad url")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", errors.New("bad url")
	}
	return scheme + "://" + strings.ToLower(u.Host), nil
}

// sameHAURL compares two configured HA URLs after normalising; anything
// invalid matches nothing.
func sameHAURL(a, b string) bool {
	na, err1 := normalizeHAURL(a)
	nb, err2 := normalizeHAURL(b)
	return err1 == nil && err2 == nil && na == nb
}

// displayHAURL is the URL as GET may show it: normalised when valid, else
// stripped of everything after the host (an env value with user info or a
// query never leaks its credentials).
func displayHAURL(raw string) (shown string, valid bool) {
	if raw == "" {
		return "", true
	}
	if n, err := normalizeHAURL(raw); err == nil {
		return n, true
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	return u.Scheme + "://" + u.Host, false
}

// validCredential: 1..max visible ASCII characters (it ends up in an HTTP
// header or a form body, so no spaces or control characters).
func validCredential(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] >= 0x7f {
			return false
		}
	}
	return true
}

// validLockPIN: 4–12 ASCII digits.
func validLockPIN(pin string) bool {
	if len(pin) < lockPINMinLen || len(pin) > lockPINMaxLen {
		return false
	}
	for i := 0; i < len(pin); i++ {
		if pin[i] < '0' || pin[i] > '9' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Masked view

type integrationsHAView struct {
	URL          string `json:"url"`
	URLValid     bool   `json:"urlValid"`
	TokenSet     bool   `json:"tokenSet"`
	FromEnv      bool   `json:"fromEnv"` // either field comes from the environment
	URLFromEnv   bool   `json:"urlFromEnv"`
	TokenFromEnv bool   `json:"tokenFromEnv"`
}

type integrationsGoogleView struct {
	ClientIDSet         bool `json:"clientIdSet"`
	ClientSecretSet     bool `json:"clientSecretSet"`
	FromEnv             bool `json:"fromEnv"`
	ClientIDFromEnv     bool `json:"clientIdFromEnv"`
	ClientSecretFromEnv bool `json:"clientSecretFromEnv"`
}

type integrationsLockView struct {
	PINSet  bool `json:"pinSet"`
	Enabled bool `json:"enabled"` // settings.personalLock
}

type integrationsView struct {
	HA     integrationsHAView     `json:"ha"`
	Google integrationsGoogleView `json:"google"`
	Lock   integrationsLockView   `json:"lock"`
	Admin  bool                   `json:"admin"` // this request may change them
}

func envSet(key string) bool { return strings.TrimSpace(os.Getenv(key)) != "" }

func buildIntegrationsView(r *http.Request) integrationsView {
	var v integrationsView
	v.HA.URL, v.HA.URLValid = displayHAURL(haURL())
	v.HA.TokenSet = haToken() != ""
	v.HA.URLFromEnv, v.HA.TokenFromEnv = envSet("HA_URL"), envSet("HA_TOKEN")
	v.HA.FromEnv = v.HA.URLFromEnv || v.HA.TokenFromEnv
	id, secret := googleClient()
	v.Google.ClientIDSet, v.Google.ClientSecretSet = id != "", secret != ""
	v.Google.ClientIDFromEnv, v.Google.ClientSecretFromEnv = envSet("GOOGLE_CLIENT_ID"), envSet("GOOGLE_CLIENT_SECRET")
	v.Google.FromEnv = v.Google.ClientIDFromEnv || v.Google.ClientSecretFromEnv
	v.Lock.PINSet = lockPINSet()
	v.Lock.Enabled = loadSettings().PersonalLock
	v.Admin = isAdminRequest(r)
	return v
}

func handleIntegrationsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, buildIntegrationsView(r))
}

// ---------------------------------------------------------------------------
// Errors

// integrationsHints are the DE/EN texts for the error codes, picked by the
// interface language (Settings.uiLanguage). None of them repeats user input.
var integrationsHints = map[string][2]string{ // code → {de, en}
	"local_only":       {"Nur direkt am Mac (http://localhost:8080).", "Only directly on the Mac (http://localhost:8080)."},
	"bad_request":      {"Die Anfrage war ungültig.", "The request was invalid."},
	"too_large":        {"Die Anfrage ist zu groß.", "The request is too large."},
	"bad_url":          {"Die Adresse muss so aussehen: http://192.168.1.20:8123 (ohne Pfad).", "The address must look like http://192.168.1.20:8123 (no path)."},
	"bad_token":        {"Der Token ist ungültig.", "The token is invalid."},
	"url_required":     {"Gib zuerst die Home-Assistant-Adresse an.", "Enter the Home Assistant address first."},
	"env_override":     {"Die Adresse ist über HA_URL in der Umgebung festgelegt.", "The address is set by HA_URL in the environment."},
	"bad_client":       {"Client-ID oder Client-Secret ist ungültig.", "The client ID or client secret is invalid."},
	"bad_json":         {"Das ist keine gültige client_secret-JSON-Datei.", "That is not a valid client_secret JSON file."},
	"not_desktop":      {"Lege in der Google Cloud Console einen OAuth-Client vom Typ „Desktop-App“ an.", "Create an OAuth client of type \"Desktop app\" in the Google Cloud Console."},
	"bad_pin":          {"Die PIN muss aus 4 bis 12 Ziffern bestehen.", "The PIN must be 4 to 12 digits."},
	"old_pin_required": {"Gib die aktuelle PIN ein.", "Enter the current PIN."},
	"wrong_pin":        {"Die aktuelle PIN ist falsch.", "The current PIN is wrong."},
	"lockout":          {"Zu viele falsche PINs. Versuch es in einer Minute noch einmal.", "Too many wrong PINs. Try again in a minute."},
	"conflict":         {"Die Einstellung wurde gerade woanders geändert. Lade die Seite neu.", "The setting was just changed elsewhere. Reload the page."},
	"read_only":        {"Die Datei stammt von einer neueren HERMES-Version und wird nicht überschrieben.", "The file is from a newer HERMES version and is not overwritten."},
	"internal":         {"Speichern fehlgeschlagen.", "Saving failed."},
}

// integrationsError is a refusal decided inside a store update.
type integrationsError struct {
	status    int
	code, msg string
}

func (e *integrationsError) Error() string { return e.msg }

func writeIntegrationsError(w http.ResponseWriter, status int, code, msg string) {
	body := map[string]string{"error": msg, "code": code}
	if h, ok := integrationsHints[code]; ok {
		if loadSettings().uiLanguage() == "de" {
			body["hint"] = h[0]
		} else {
			body["hint"] = h[1]
		}
	}
	writeJSON(w, status, body)
}

// writeIntegrationsFailure maps an update error to a response; the store's
// own errors are logged without content (they never hold secrets, but the
// response stays generic anyway).
func writeIntegrationsFailure(w http.ResponseWriter, what string, err error) {
	var ie *integrationsError
	switch {
	case errors.As(err, &ie):
		writeIntegrationsError(w, ie.status, ie.code, ie.msg)
	case errors.Is(err, errStoreReadOnly):
		writeIntegrationsError(w, http.StatusConflict, "read_only", "secrets.json is from a newer HERMES")
	default:
		log.Printf("integrations: saving %s failed: %v", what, err)
		writeIntegrationsError(w, http.StatusInternalServerError, "internal", "could not save")
	}
}

// integrationsAdmin refuses writes from anywhere but this machine (or a
// loopback Host plus the master token, Section 8.12).
func integrationsAdmin(w http.ResponseWriter, r *http.Request) bool {
	if isAdminRequest(r) {
		return true
	}
	writeIntegrationsError(w, http.StatusForbidden, "local_only", "only from this machine (http://localhost:8080)")
	return false
}

// decodeIntegrationsBody reads one JSON object of at most limit bytes.
func decodeIntegrationsBody(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	err := dec.Decode(v)
	if err == nil && dec.More() {
		err = errors.New("trailing data")
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeIntegrationsError(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large")
		} else {
			writeIntegrationsError(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		}
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// POST /api/integrations/ha

// handleIntegrationsHA stores the HA URL and token as a pair. Absent fields
// keep their value, but a new URL without a token in the same request clears
// the stored token, so a token is never sent to a host it was not entered
// for. {"url":""} clears both. With HA_URL set in the environment the URL
// is fixed: a token is then bound to exactly that URL.
func handleIntegrationsHA(w http.ResponseWriter, r *http.Request) {
	if !integrationsAdmin(w, r) {
		return
	}
	var req struct {
		URL   *string `json:"url"`
		Token *string `json:"token"`
	}
	if !decodeIntegrationsBody(w, r, integrationsBodyLimit, &req) {
		return
	}
	if req.URL == nil && req.Token == nil {
		writeIntegrationsError(w, http.StatusBadRequest, "bad_request", "url or token required")
		return
	}
	envURL := strings.TrimSpace(os.Getenv("HA_URL"))
	var newURL string
	if req.URL != nil && strings.TrimSpace(*req.URL) != "" {
		n, err := normalizeHAURL(*req.URL)
		if err != nil {
			writeIntegrationsError(w, http.StatusBadRequest, "bad_url", "url must be http(s)://host[:port] without a path")
			return
		}
		if envURL != "" && !sameHAURL(n, envURL) {
			writeIntegrationsError(w, http.StatusConflict, "env_override", "HA_URL is set in the environment")
			return
		}
		newURL = n
	}
	var token string
	if req.Token != nil {
		token = strings.TrimSpace(*req.Token)
		if token != "" && !validCredential(token, haTokenMaxLen) {
			writeIntegrationsError(w, http.StatusBadRequest, "bad_token", "invalid token")
			return
		}
	}
	changed := false
	err := updateSecrets(func(s *secretsFile) error {
		oldURL, oldToken := s.HAURL, s.HAToken
		switch {
		case req.URL != nil && newURL == "": // clear both
			s.HAURL, s.HAToken = "", ""
		default:
			target := newURL
			if req.URL == nil { // token only: bind it to the URL in effect
				target = s.HAURL
				if n, err := normalizeHAURL(envURL); err == nil {
					target = n
				}
				if target == "" && token != "" {
					return &integrationsError{http.StatusBadRequest, "url_required", "set the URL first"}
				}
			}
			urlChanged := !sameHAURL(target, s.HAURL) && !(target == "" && s.HAURL == "")
			s.HAURL = target
			if req.Token != nil {
				s.HAToken = token
			} else if urlChanged {
				s.HAToken = ""
			}
		}
		if s.HAURL == oldURL && s.HAToken == oldToken {
			return errNoChange
		}
		changed = true
		return nil
	})
	if err != nil {
		writeIntegrationsFailure(w, "ha", err)
		return
	}
	if changed {
		runSecretHooks(&haChangeHooks)
		hubPublish("integrations.changed", "", map[string]string{"what": "ha"})
	}
	writeJSON(w, http.StatusOK, buildIntegrationsView(r))
}

// ---------------------------------------------------------------------------
// POST /api/integrations/google

// googleClientJSON is the client_secret_*.json download of a Desktop client.
type googleClientJSON struct {
	Installed *struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	} `json:"installed"`
	Web json.RawMessage `json:"web"`
}

// handleIntegrationsGoogle stores the OAuth client. Absent fields keep their
// value, "" clears; a new client id without a secret in the same request
// clears the stored secret (it belonged to the old client).
func handleIntegrationsGoogle(w http.ResponseWriter, r *http.Request) {
	if !integrationsAdmin(w, r) {
		return
	}
	var req struct {
		ClientID         *string `json:"clientId"`
		ClientSecret     *string `json:"clientSecret"`
		ClientSecretJSON *string `json:"clientSecretJson"`
	}
	if !decodeIntegrationsBody(w, r, googleBodyLimit, &req) {
		return
	}
	if req.ClientSecretJSON != nil {
		if req.ClientID != nil || req.ClientSecret != nil {
			writeIntegrationsError(w, http.StatusBadRequest, "bad_request", "send clientSecretJson alone")
			return
		}
		id, secret := "", ""
		if raw := strings.TrimSpace(*req.ClientSecretJSON); raw != "" {
			var f googleClientJSON
			if err := json.Unmarshal([]byte(raw), &f); err != nil {
				writeIntegrationsError(w, http.StatusBadRequest, "bad_json", "not a client_secret JSON file")
				return
			}
			if f.Installed == nil {
				if len(f.Web) > 0 {
					writeIntegrationsError(w, http.StatusBadRequest, "not_desktop", "use an OAuth client of type Desktop app")
				} else {
					writeIntegrationsError(w, http.StatusBadRequest, "bad_json", "not a client_secret JSON file")
				}
				return
			}
			id, secret = strings.TrimSpace(f.Installed.ClientID), strings.TrimSpace(f.Installed.ClientSecret)
			if !validCredential(id, googleFieldMaxLen) || !validCredential(secret, googleFieldMaxLen) {
				writeIntegrationsError(w, http.StatusBadRequest, "bad_client", "invalid client id or secret")
				return
			}
		}
		req.ClientID, req.ClientSecret = &id, &secret
	}
	if req.ClientID == nil && req.ClientSecret == nil {
		writeIntegrationsError(w, http.StatusBadRequest, "bad_request", "clientId, clientSecret or clientSecretJson required")
		return
	}
	for _, p := range []*string{req.ClientID, req.ClientSecret} {
		if p == nil {
			continue
		}
		*p = strings.TrimSpace(*p)
		if *p != "" && !validCredential(*p, googleFieldMaxLen) {
			writeIntegrationsError(w, http.StatusBadRequest, "bad_client", "invalid client id or secret")
			return
		}
	}
	changed := false
	err := updateSecrets(func(s *secretsFile) error {
		oldID, oldSecret := s.GoogleClientID, s.GoogleClientSecret
		if req.ClientID != nil {
			if *req.ClientID != s.GoogleClientID && req.ClientSecret == nil {
				s.GoogleClientSecret = ""
			}
			s.GoogleClientID = *req.ClientID
		}
		if req.ClientSecret != nil {
			s.GoogleClientSecret = *req.ClientSecret
		}
		if s.GoogleClientID == oldID && s.GoogleClientSecret == oldSecret {
			return errNoChange
		}
		changed = true
		return nil
	})
	if err != nil {
		writeIntegrationsFailure(w, "google", err)
		return
	}
	if changed {
		runSecretHooks(&googleChangeHooks)
		hubPublish("integrations.changed", "", map[string]string{"what": "google"})
	}
	writeJSON(w, http.StatusOK, buildIntegrationsView(r))
}

// ---------------------------------------------------------------------------
// POST /api/integrations/lock

// pinAttemptBegin starts a rate-limited PIN check for /api/unlock and the
// PIN change here; both share the per-address counter, so the admin
// endpoint is no side door for guessing. Checks already running count
// against the limit, so parallel guesses gain nothing. It returns nil and
// the Retry-After seconds when locked out; else call pinAttemptEnd.
func pinAttemptBegin(r *http.Request) (*unlockFailures, int) {
	client := unlockClientKey(r)
	now := time.Now()
	unlockSessions.Lock()
	defer unlockSessions.Unlock()
	f := unlockSessions.fails[client]
	if f == nil {
		if len(unlockSessions.fails) > 1000 { // forget idle addresses
			for k, v := range unlockSessions.fails {
				if v.inflight == 0 && now.After(v.until) {
					delete(unlockSessions.fails, k)
				}
			}
		}
		f = &unlockFailures{}
		unlockSessions.fails[client] = f
	}
	if now.Before(f.until) || f.n+f.inflight >= unlockMaxFails {
		retry := int(time.Until(f.until).Seconds()) + 1
		if retry < 1 {
			retry = int(unlockLockout.Seconds())
		}
		return nil, retry
	}
	f.inflight++
	return f, 0
}

func pinAttemptEnd(f *unlockFailures, ok bool) {
	unlockSessions.Lock()
	defer unlockSessions.Unlock()
	f.inflight--
	if ok {
		f.n = 0
		return
	}
	f.n++
	if f.n >= unlockMaxFails {
		f.n, f.until = 0, time.Now().Add(unlockLockout)
	}
}

// handleIntegrationsLock sets, changes or removes the household PIN. With a
// PIN set, the current one must come along as oldPin. Every change ends all
// unlock sessions, so every device locks again; removing the PIN also turns
// personalLock off (a lock nobody can open protects nothing).
func handleIntegrationsLock(w http.ResponseWriter, r *http.Request) {
	if !integrationsAdmin(w, r) {
		return
	}
	var req struct {
		PIN    *string `json:"pin"`
		OldPIN string  `json:"oldPin"`
	}
	if !decodeIntegrationsBody(w, r, integrationsBodyLimit, &req) {
		return
	}
	if req.PIN == nil || len(req.OldPIN) > lockOldPINMaxLen {
		writeIntegrationsError(w, http.StatusBadRequest, "bad_request", "pin required")
		return
	}
	pin := *req.PIN
	if pin != "" && !validLockPIN(pin) {
		writeIntegrationsError(w, http.StatusBadRequest, "bad_pin", "the PIN must be 4 to 12 digits")
		return
	}

	// Verify the current PIN (rate limited, outside the store lock: the hash
	// takes a while) and remember which hash it matched, so a change made
	// meanwhile from elsewhere is not overwritten.
	current := loadSecrets().LockPINHash
	if current != "" {
		if req.OldPIN == "" {
			writeIntegrationsError(w, http.StatusBadRequest, "old_pin_required", "the current PIN is required")
			return
		}
		f, retry := pinAttemptBegin(r)
		if f == nil {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeIntegrationsError(w, http.StatusTooManyRequests, "lockout", "too many wrong PINs; try again in a minute")
			return
		}
		ok := verifyPINHash(current, req.OldPIN)
		pinAttemptEnd(f, ok)
		if !ok {
			writeIntegrationsError(w, http.StatusForbidden, "wrong_pin", "wrong PIN")
			return
		}
	}

	var newHash string
	if pin != "" {
		h, err := hashLockPIN(pin)
		if err != nil {
			writeIntegrationsFailure(w, "lock", err)
			return
		}
		newHash = h
	}
	changed := false
	err := updateSecrets(func(s *secretsFile) error {
		if s.LockPINHash != current {
			return &integrationsError{http.StatusConflict, "conflict", "the PIN was changed meanwhile"}
		}
		if s.LockPINHash == "" && newHash == "" {
			return errNoChange
		}
		s.LockPINHash = newHash
		changed = true
		return nil
	})
	// Lock every device even when nothing was stored: an unlock session must
	// never outlive a PIN request that may have half-applied.
	clearUnlockSessions()
	if err != nil {
		writeIntegrationsFailure(w, "lock", err)
		return
	}
	// Without a PIN the lock goes off. This runs after the PIN is gone: if
	// it fails, the lock stays on and closed (fail closed) until an admin
	// switches it off in the settings, which needs no unlock without a PIN.
	if pin == "" {
		if s := loadSettings(); s.PersonalLock {
			s.PersonalLock = false
			if err := saveSettings(s); err != nil {
				writeIntegrationsFailure(w, "lock", err)
				return
			}
			changed = true
		}
	}
	if !changed {
		writeJSON(w, http.StatusOK, buildIntegrationsView(r))
		return
	}
	hubPublish("integrations.changed", "", map[string]string{"what": "lock"})
	writeJSON(w, http.StatusOK, buildIntegrationsView(r))
}
