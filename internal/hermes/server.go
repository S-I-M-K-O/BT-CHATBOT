package hermes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"strconv"
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
	// Companion core (Sections 3.2, 4.10, 4.11).
	mux.HandleFunc("GET /api/events", handleEvents)
	mux.HandleFunc("POST /api/events/ack", handleEventsAck)
	mux.HandleFunc("POST /api/warm", handleWarm)
	mux.HandleFunc("GET /api/unlock", handleUnlockStatus)
	mux.HandleFunc("POST /api/unlock", handleUnlock)
	mux.HandleFunc("DELETE /api/unlock", handleLock)
	// Feature files register their endpoints from init().
	for _, rt := range registeredRoutes() {
		mux.HandleFunc(rt.pattern, rt.h)
	}
	mux.Handle("/", staticHandler(envOr("STATIC_DIR", "web/static")))
	return recoverMiddleware(logMiddleware(securityHeaders(originGuard(tokenGuard(mux)))))
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
		// The escaped path: a decoded one could carry "\n" and forge log lines.
		log.Printf("%s %s %d %s", r.Method, r.URL.EscapedPath(), rec.status, time.Since(start).Round(time.Millisecond))
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
				log.Printf("panic serving %s %s: %v\n%s", r.Method, r.URL.EscapedPath(), p, debug.Stack())
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
// Anything but loopback still needs HERMES_TOKEN (tokenGuard).
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

// ---------------------------------------------------------------------------
// Access token
//
// The API can rewrite the system prompt and reads personal data, so anything
// that does not come from this machine needs HERMES_TOKEN. The origin checks
// above only stop browsers; curl on the LAN sends no Origin at all.

const tokenCookieName = "hermes_token"

func envBool(key string) bool {
	b, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv(key)))
	return b
}

// isLoopbackHost reports whether a Host header (or ADDR host) names this
// machine only: localhost, *.localhost or a loopback IP. An empty host is not
// loopback; HTTP/1.0 clients without Host get no free pass.
func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isLocalRequest: a loopback Host alone can be spoofed by any LAN client, so
// the connection itself must come from loopback too. In Docker it arrives
// from the bridge gateway instead; HERMES_BEHIND_LOOPBACK=1 (set by
// docker-compose, whose port is published on 127.0.0.1 only) says that is
// still this machine, but only for a connection from that gateway (or from
// HERMES_TRUSTED_PEER): another container on the network, or a LAN host that
// routes to the bridge, is not this machine.
func isLocalRequest(r *http.Request) bool {
	if !isLoopbackHost(r.Host) {
		return false
	}
	if isLoopbackRemote(r.RemoteAddr) {
		return true
	}
	return envBool("HERMES_BEHIND_LOOPBACK") && trustedPeer(r.RemoteAddr)
}

// routeFile is the kernel routing table the default gateway is read from
// (tests point it elsewhere).
var routeFile = "/proc/net/route"

var gatewayCache struct {
	sync.Mutex
	ip    net.IP
	until time.Time
}

// defaultGateway is the container's IPv4 default gateway: where Docker's
// port proxy connects from (checked with Docker Desktop and the Linux
// userland proxy, default bridge and compose networks). nil when unknown.
func defaultGateway() net.IP {
	gatewayCache.Lock()
	defer gatewayCache.Unlock()
	if time.Now().Before(gatewayCache.until) {
		return gatewayCache.ip
	}
	gatewayCache.ip, gatewayCache.until = readDefaultGateway(routeFile), time.Now().Add(time.Minute)
	return gatewayCache.ip
}

func readDefaultGateway(path string) net.IP {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 4 || f[1] != "00000000" {
			continue
		}
		flags, err1 := strconv.ParseUint(f[3], 16, 32)
		gw, err2 := strconv.ParseUint(f[2], 16, 32)
		if err1 != nil || err2 != nil || flags&0x2 == 0 || gw == 0 {
			continue
		}
		// little-endian hex: 010011AC is 172.17.0.1
		return net.IPv4(byte(gw), byte(gw>>8), byte(gw>>16), byte(gw>>24))
	}
	return nil
}

// trustedPeer: the connection comes from the default gateway or from an IP
// listed in HERMES_TRUSTED_PEER (comma-separated).
func trustedPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, p := range strings.Split(os.Getenv("HERMES_TRUSTED_PEER"), ",") {
		if t := net.ParseIP(strings.TrimSpace(p)); t != nil && t.Equal(ip) {
			return true
		}
	}
	if gw := defaultGateway(); gw != nil && gw.Equal(ip) {
		return true
	}
	untrustedPeerLogged.Do(func() {
		log.Printf("WARNING: request with a loopback Host from %s, which is not the Docker gateway: treated as a LAN client (token needed). Set HERMES_TRUSTED_PEER if this is this machine.", truncateRunes(host, 64))
	})
	return false
}

var untrustedPeerLogged sync.Once

// tokenMatches compares in constant time; hashing first also hides the
// token's length.
func tokenMatches(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	g, w := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(g[:], w[:]) == 1
}

// cookieToken is what the login cookie holds instead of the raw token: hex is
// always a valid cookie value (net/http silently drops bytes like ';', '"' or
// 'ä', which would make the cookie never match), and the secret itself does
// not sit in the browser's cookie store.
func cookieToken(token string) string {
	sum := sha256.Sum256([]byte("hermes-cookie\x00" + token))
	return hex.EncodeToString(sum[:])
}

// hasValidToken accepts "Authorization: Bearer <token>" (scripts) or the login
// cookie (the browser UI, whose fetches send it automatically).
func hasValidToken(r *http.Request, want string) bool {
	if auth := r.Header.Get("Authorization"); len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return tokenMatches(strings.TrimSpace(auth[7:]), want)
	}
	if c, err := r.Cookie(tokenCookieName); err == nil {
		return tokenMatches(c.Value, cookieToken(want))
	}
	return false
}

func denyAccess(w http.ResponseWriter, r *http.Request, code int, msg string) {
	if code == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="HERMES"`)
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSONError(w, code, msg)
		return
	}
	http.Error(w, msg, code)
}

// tokenGuard requires HERMES_TOKEN for every non-local request and fails
// closed when no token is configured. Opening any page with ?token=<token>
// stores it in an HttpOnly, SameSite=Strict cookie and redirects to the same
// page without it, so the token does not linger in the address bar or history.
//
// The scoped HERMES_LISTS_TOKEN (Section 8.9) works the same way but only for
// the shopping-list page and its API: /lists.html?token=<lists token> sets the
// hermes_lists cookie, and that cookie (or a Bearer lists token) opens nothing
// else. Such requests are tagged with withListsScope, so the event stream
// sends them list changes only.
func tokenGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		noteLANPublishedPort(r)
		want := os.Getenv("HERMES_TOKEN")
		lists := listsToken()
		if (want != "" || lists != "") && (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.URL.Query().Has("token") {
			q := r.URL.Query()
			got := q.Get("token")
			switch {
			case want != "" && tokenMatches(got, want):
				setAuthCookie(w, r, tokenCookieName, cookieToken(want))
			case lists != "" && tokenMatches(got, lists) && listsPathAllowed(r):
				setAuthCookie(w, r, listsCookieName, listsCookieToken(lists))
			default:
				denyAccess(w, r, http.StatusUnauthorized, "invalid access token")
				return
			}
			q.Del("token")
			// The escaped path keeps "%5C" and "%3F" encoded: decoded, "/\x"
			// reads as the off-site "//x" in browsers and "?" would start
			// the query. Clean also folds a leading "//" so this cannot
			// become a protocol-relative redirect.
			escaped := r.URL.EscapedPath()
			target := path.Clean("/" + escaped)
			if strings.HasSuffix(escaped, "/") && target != "/" {
				target += "/"
			}
			if enc := q.Encode(); enc != "" {
				target += "?" + enc
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		if isLocalRequest(r) || envBool("HERMES_INSECURE_NO_TOKEN") {
			next.ServeHTTP(w, r)
			return
		}
		if want != "" && hasValidToken(r, want) {
			next.ServeHTTP(w, r)
			return
		}
		if lists != "" && hasListsToken(r, lists) && listsPathAllowed(r) {
			next.ServeHTTP(w, withListsScope(r))
			return
		}
		if want == "" && lists == "" {
			denyAccess(w, r, http.StatusForbidden, "network access is disabled; set HERMES_TOKEN on the server")
			return
		}
		denyAccess(w, r, http.StatusUnauthorized, "access token required: open /?token=<HERMES_TOKEN> once, or send Authorization: Bearer <token>")
	})
}

func setAuthCookie(w http.ResponseWriter, r *http.Request, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   365 * 24 * 60 * 60,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
}

// ---------------------------------------------------------------------------
// Scoped lists token (Section 8.9)

const listsCookieName = "hermes_lists"

// listsToken is HERMES_LISTS_TOKEN; it is ignored when it equals the master
// token (that would silently turn the master token into a lists-only one).
func listsToken() string {
	t := strings.TrimSpace(os.Getenv("HERMES_LISTS_TOKEN"))
	if t != "" && t == os.Getenv("HERMES_TOKEN") {
		return ""
	}
	return t
}

// listsCookieToken hashes like cookieToken with its own domain separation,
// so a lists cookie can never pass as the master cookie or vice versa.
func listsCookieToken(token string) string {
	sum := sha256.Sum256([]byte("hermes-lists-cookie\x00" + token))
	return hex.EncodeToString(sum[:])
}

// hasListsToken accepts "Authorization: Bearer <lists token>" or the
// hermes_lists cookie.
func hasListsToken(r *http.Request, want string) bool {
	if auth := r.Header.Get("Authorization"); len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return tokenMatches(strings.TrimSpace(auth[7:]), want)
	}
	if c, err := r.Cookie(listsCookieName); err == nil {
		return tokenMatches(c.Value, listsCookieToken(want))
	}
	return false
}

// listsStaticPaths are the files the phone list page needs.
var listsStaticPaths = map[string]bool{
	"/lists.html": true, "/lib.js": true, "/hermes.js": true, "/lists.js": true, "/css/lists.css": true,
}

// listsPathAllowed is everything the lists token may open: the list page and
// its scripts (GET), the list API (all methods) and the list-only event
// stream. Paths must already be clean, so "/api/lists/../memories" is no way
// out.
func listsPathAllowed(r *http.Request) bool {
	p := r.URL.Path
	if p == "" || path.Clean(p) != p {
		return false
	}
	get := r.Method == http.MethodGet || r.Method == http.MethodHead
	switch {
	case p == "/api/lists" || strings.HasPrefix(p, "/api/lists/"):
		return true
	case p == "/api/events":
		return get && r.URL.Query().Get("scope") == "lists"
	case listsStaticPaths[p], strings.HasPrefix(p, "/favicon"):
		return get
	}
	return false
}

// lanPortLogged makes the "published on the LAN" error appear once.
var lanPortLogged sync.Once

// noteLANPublishedPort: with HERMES_BEHIND_LOOPBACK=1 the port must only be
// published on 127.0.0.1. A request naming a non-loopback Host means it is
// reachable from the LAN, where Host: localhost would pass as local.
func noteLANPublishedPort(r *http.Request) {
	if envBool("HERMES_BEHIND_LOOPBACK") && r.Host != "" && !isLoopbackHost(r.Host) {
		lanPortLogged.Do(func() {
			log.Printf("ERROR: request for Host %q while HERMES_BEHIND_LOOPBACK=1: the port seems to be published beyond loopback. Use docker-compose.lan.yml instead.", truncateRunes(r.Host, 100))
		})
	}
}

// startupProblem is a configuration HERMES refuses to start with (Section 7):
// HERMES_BEHIND_LOOPBACK=1 trusts every request that says Host: localhost, so
// it must never be combined with settings that only make sense on the LAN.
func startupProblem() error {
	if !envBool("HERMES_BEHIND_LOOPBACK") {
		return nil
	}
	lan := strings.TrimSpace(os.Getenv("HERMES_LISTS_TOKEN")) != ""
	for _, h := range strings.Split(os.Getenv("ALLOWED_HOSTS"), ",") {
		if h = strings.TrimSpace(h); h != "" && !isLoopbackHost(h) {
			lan = true
		}
	}
	if lan {
		return errors.New("HERMES_BEHIND_LOOPBACK=1 together with LAN settings would let any LAN client in by sending Host: localhost. Use docker-compose.lan.yml.")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Admin requests and the personal lock (Sections 4.10, 8.12). Feature files
// gate their handlers with these; the signatures are final.

// isAdminRequest: from this machine, or a loopback Host plus the master
// token (the Docker LAN layout seen from the Mac's own browser).
// HERMES_INSECURE_NO_TOKEN never makes a request an admin request.
func isAdminRequest(r *http.Request) bool {
	if isLocalRequest(r) {
		return true
	}
	want := os.Getenv("HERMES_TOKEN")
	return want != "" && isLoopbackHost(r.Host) && hasValidToken(r, want)
}

// ---------------------------------------------------------------------------
// Personal lock: POST /api/unlock sets hermes_unlock (Section 3.11)

const (
	unlockCookieName = "hermes_unlock"
	unlockTTL        = 10 * time.Minute // sliding
	unlockMaxFails   = 5
	unlockLockout    = 60 * time.Second
	unlockMaxSess    = 64
)

// unlockSessions maps the sha256 of a cookie's random session id to its
// expiry. Sessions live in memory only: a restart locks every device.
var unlockSessions = struct {
	sync.Mutex
	m     map[string]time.Time
	fails map[string]*unlockFailures
}{m: map[string]time.Time{}, fails: map[string]*unlockFailures{}}

type unlockFailures struct {
	n        int       // wrong PINs since the last success or lockout
	inflight int       // checks running right now (count against the limit)
	until    time.Time // locked out until
}

func unlockSessionKey(id string) string {
	sum := sha256.Sum256([]byte("hermes-unlock\x00" + id))
	return hex.EncodeToString(sum[:])
}

// unlockedRequest reports whether r carries a valid hermes_unlock cookie,
// and slides its expiry. Without a PIN nothing is ever unlocked.
func unlockedRequest(r *http.Request) bool {
	c, err := r.Cookie(unlockCookieName)
	if err != nil || len(c.Value) != 64 || !lockPINSet() {
		return false
	}
	key := unlockSessionKey(c.Value)
	now := time.Now()
	unlockSessions.Lock()
	defer unlockSessions.Unlock()
	exp, ok := unlockSessions.m[key]
	if !ok || now.After(exp) {
		delete(unlockSessions.m, key)
		return false
	}
	unlockSessions.m[key] = now.Add(unlockTTL)
	return true
}

// clearUnlockSessions locks every device (PIN changed or removed).
func clearUnlockSessions() {
	unlockSessions.Lock()
	clear(unlockSessions.m)
	unlockSessions.Unlock()
}

// unlockClientKey identifies the requester for the lockout: the remote IP
// (in Docker all requests share the bridge address, so they share it too).
func unlockClientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return host
}

func handleUnlockStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"pinSet": lockPINSet(), "unlocked": unlockedRequest(r), "locked": loadSettings().PersonalLock})
}

// handleUnlock checks {"pin"} and sets the unlock cookie. Five wrong PINs
// lock the remote address out for a minute; checks already running count
// against the limit, so parallel guesses gain nothing.
func handleUnlock(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.PIN) > 64 {
		writeJSONErrorCode(w, http.StatusBadRequest, "bad_request", "bad request")
		return
	}
	if !lockPINSet() {
		writeJSONErrorCode(w, http.StatusConflict, "no_pin", "no PIN is set")
		return
	}
	f, retry := pinAttemptBegin(r)
	if f == nil {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeJSONErrorCode(w, http.StatusTooManyRequests, "lockout", "too many wrong PINs; try again in a minute")
		return
	}
	ok := checkLockPIN(req.PIN)
	pinAttemptEnd(f, ok)
	if !ok {
		writeJSONErrorCode(w, http.StatusForbidden, "wrong_pin", "wrong PIN")
		return
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		writeJSONErrorCode(w, http.StatusInternalServerError, "internal", "could not create a session")
		return
	}
	id := hex.EncodeToString(raw[:])
	now := time.Now()
	unlockSessions.Lock()
	for k, exp := range unlockSessions.m {
		if now.After(exp) {
			delete(unlockSessions.m, k)
		}
	}
	if len(unlockSessions.m) >= unlockMaxSess { // drop the session closest to expiry
		var oldK string
		var oldT time.Time
		for k, exp := range unlockSessions.m {
			if oldK == "" || exp.Before(oldT) {
				oldK, oldT = k, exp
			}
		}
		delete(unlockSessions.m, oldK)
	}
	unlockSessions.m[unlockSessionKey(id)] = now.Add(unlockTTL)
	unlockSessions.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     unlockCookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"unlocked": true, "expiresIn": int(unlockTTL.Seconds())})
}

// handleLock (DELETE /api/unlock) ends this device's session.
func handleLock(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(unlockCookieName); err == nil {
		unlockSessions.Lock()
		delete(unlockSessions.m, unlockSessionKey(c.Value))
		unlockSessions.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: unlockCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"unlocked": false})
}

// requireUnlock wraps a handler that exposes or changes personal data: with
// personalLock on it needs the unlock cookie, else 403 code "locked".
func requireUnlock(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if loadSettings().PersonalLock && !unlockedRequest(r) {
			writeJSONErrorCode(w, http.StatusForbidden, "locked", "personal data is locked; unlock with the PIN")
			return
		}
		h(w, r)
	}
}

// lanAccessProblem explains at startup why network clients will be refused
// or are unprotected; "" when the setup is consistent.
func lanAccessProblem() string {
	if envBool("HERMES_INSECURE_NO_TOKEN") {
		return "WARNING: HERMES_INSECURE_NO_TOKEN is set; anyone who can reach this server can change the system prompt and read personal data"
	}
	if os.Getenv("HERMES_TOKEN") != "" {
		return ""
	}
	if os.Getenv("HERMES_LISTS_TOKEN") != "" {
		return "WARNING: only HERMES_LISTS_TOKEN is set; LAN devices can open the shopping lists but nothing else until you set HERMES_TOKEN"
	}
	var exposed []string
	for _, h := range strings.Split(os.Getenv("ALLOWED_HOSTS"), ",") {
		if h = strings.TrimSpace(h); h != "" && !isLoopbackHost(h) {
			exposed = append(exposed, h)
		}
	}
	// In Docker ADDR is 0.0.0.0 but the published port is loopback-only.
	if addr := envOr("ADDR", "127.0.0.1"); !isLoopbackHost(addr) && !envBool("HERMES_BEHIND_LOOPBACK") {
		exposed = append(exposed, "ADDR="+addr)
	}
	if len(exposed) == 0 {
		return ""
	}
	return "ERROR: HERMES is exposed beyond loopback (" + strings.Join(exposed, ", ") +
		") but HERMES_TOKEN is empty; every non-local request will be refused until you set HERMES_TOKEN"
}

// contentSecurityPolicy: 'wasm-unsafe-eval' lets the voice-ID worker compile
// the ONNX runtime's WebAssembly; it does not allow eval of JavaScript.
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self' 'wasm-unsafe-eval'; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src 'self' data: https://fonts.gstatic.com; " +
	"img-src 'self' data: blob:; " +
	"media-src 'self' blob: data:; " +
	"connect-src 'self'; " +
	"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

const permissionsPolicy = "microphone=(self), camera=(), geolocation=(), idle-detection=(self)"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("Permissions-Policy", permissionsPolicy)
		next.ServeHTTP(w, r)
	})
}

// staticHandler serves the frontend. HTML/JS/CSS are revalidated on every load
// (no-cache still allows 304s) so updates show up without a hard refresh.
// /vendor/ holds versioned third-party assets (ONNX runtime, speaker model,
// pdf.js, Readability), cached for a year.
func staticHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// no directory listings (/vendor/, /css/): only the root has an
		// index page
		if r.URL.Path != "/" && strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/vendor/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			fs.ServeHTTP(w, r)
			return
		}
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
// Pictures under /gen/: image-search results (hermes_* files are left over
// from older versions and still served until the 24-hour pruning)

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

// genMaxAge is how long image-search pictures (and leftover hermes_* files
// from older versions) are kept.
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
	Voices []voiceInfo `json:"voices"`
	// Features holds the registerStatus providers; omitted while none exist.
	Features map[string]any `json:"features,omitempty"`
}

var statusClient = &http.Client{Timeout: 2 * time.Second}

// The last /api/tags probe, so status providers (memory's embedOk) can ask
// "is Ollama up and is this model pulled?" without an extra request.
var ollamaProbeCache struct {
	sync.Mutex
	st   ollamaStatus
	at   time.Time
	base string
}

// ollamaProbeFresh is how long a cached probe answers ollamaModelAvailable.
const ollamaProbeFresh = 30 * time.Second

// ollamaModelAvailable reports whether Ollama answered /api/tags and lists
// model (an untagged name matches any tag of it). It reuses a probe younger
// than ollamaProbeFresh (handleStatus probes right before the providers run)
// and probes otherwise.
func ollamaModelAvailable(ctx context.Context, model string) bool {
	if model == "" {
		return false
	}
	ollamaProbeCache.Lock()
	st, at, base := ollamaProbeCache.st, ollamaProbeCache.at, ollamaProbeCache.base
	ollamaProbeCache.Unlock()
	if at.IsZero() || time.Since(at) > ollamaProbeFresh || base != ollamaBase() {
		st = probeOllama(ctx)
	}
	if !st.OK {
		return false
	}
	return modelListed(st.Models, model)
}

func modelListed(models []string, model string) bool {
	untagged := !strings.Contains(model, ":")
	for _, m := range models {
		if m == model || (untagged && strings.HasPrefix(m, model+":")) || (!untagged && strings.TrimSuffix(model, ":latest") == m) {
			return true
		}
	}
	return false
}

func probeOllama(ctx context.Context) (st ollamaStatus) {
	st = ollamaStatus{Models: []string{}}
	base := ollamaBase()
	defer func() {
		ollamaProbeCache.Lock()
		ollamaProbeCache.st, ollamaProbeCache.at, ollamaProbeCache.base = st, time.Now(), base
		ollamaProbeCache.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/tags", nil)
	if err != nil {
		st.Error = "invalid OLLAMA_BASE"
		return st
	}
	resp, err := statusClient.Do(req)
	if err != nil {
		// Redacted: OLLAMA_BASE may hold proxy credentials (user:pass@).
		st.Error = "Ollama is not reachable at " + req.URL.Scheme + "://" + req.URL.Host
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
	resp.Voices = listVoices()
	resp.Features = collectStatus(r.Context())
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
