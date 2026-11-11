package hermes

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// gcal.go is the Google Calendar client (SPEC 3.7, 8.8, 9 Feature 3), stdlib
// only: OAuth 2.0 for installed apps (a "Desktop app" client) with PKCE S256
// and a loopback redirect, the refresh token in data/google_token.json
// (0600), and list / insert / delete on the primary calendar. calendar.go
// builds the voice features on top of it.
//
// Privacy: calendar data only ever travels between this server and Google's
// fixed hosts. Tokens and Google error bodies are never logged or returned.

// Endpoints are package variables so tests can point them at a fake server.
var (
	googleAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL    = "https://oauth2.googleapis.com/token"
	googleRevokeURL   = "https://oauth2.googleapis.com/revoke"
	googleCalendarAPI = "https://www.googleapis.com/calendar/v3"
)

// googleCalendarScope: read and write events on the user's calendars. HERMES
// only uses "primary".
const googleCalendarScope = "https://www.googleapis.com/auth/calendar.events"

const (
	gcalTokenFile      = "google_token.json"
	gcalStateTTL       = 10 * time.Minute
	gcalMaxStates      = 5
	gcalMaxRange       = 62 * 24 * time.Hour
	gcalPageSize       = 100
	gcalMaxPages       = 5
	gcalExpirySkew     = time.Minute // refresh this long before the access token expires
	gcalTokenBodyLimit = 1 << 20
	gcalAPIBodyLimit   = 4 << 20
	gcalTitleMax       = 300
)

var (
	errCalendarRelink   = errors.New("calendar connection expired; connect it again")
	errCalendarUpstream = errors.New("calendar service did not answer")
	errCalendarRange    = errors.New("calendar range must be positive and at most 62 days")
	errCalendarNoClient = errors.New("google client id or secret missing")
)

// gcalHTTP talks to Google's fixed hosts: timeouts, no redirects followed.
var gcalHTTP = &http.Client{
	Timeout: 20 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// ---------------------------------------------------------------------------
// Token file (data/google_token.json, 0600)

type googleToken struct {
	Version      int       `json:"version"` // 1
	RefreshToken string    `json:"refreshToken"`
	AccessToken  string    `json:"accessToken"`
	Expiry       time.Time `json:"expiry"`
	Scope        string    `json:"scope"`
	Email        string    `json:"email,omitempty"` // not fetched in v1
	// ClientID is the OAuth client the refresh token was issued to; a
	// different client cannot use it (onGoogleClientChange).
	ClientID string `json:"clientId,omitempty"`
	// NeedsRelink is set when Google refused the refresh token
	// (invalid_grant): the user must connect again.
	NeedsRelink bool `json:"needsRelink,omitempty"`
}

const gcalTokenVersion = 1

// gcalTok caches the token file. It reloads when DATA_DIR changes (tests).
// refreshMu serialises refreshes so parallel requests refresh only once.
var gcalTok = struct {
	sync.Mutex
	path      string
	loaded    bool
	tok       *googleToken // nil: not connected
	readOnly  bool
	refreshMu sync.Mutex
}{}

// gcalLoadLocked reads the token file; gcalTok must be held.
func gcalLoadLocked() {
	path := dataPath(gcalTokenFile)
	if gcalTok.loaded && gcalTok.path == path {
		return
	}
	gcalTok.path, gcalTok.loaded, gcalTok.tok, gcalTok.readOnly = path, true, nil, false
	var head struct {
		Version int `json:"version"`
	}
	b, err := os.ReadFile(path)
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return
	}
	if json.Unmarshal(b, &head) == nil && head.Version > gcalTokenVersion {
		log.Printf("calendar: %s has version %d (newer HERMES data format; refusing to overwrite)", gcalTokenFile, head.Version)
		gcalTok.readOnly = true
	}
	var t googleToken
	found, err := loadJSONFile(path, &t)
	if err != nil || !found || t.RefreshToken == "" {
		return
	}
	gcalTok.tok = &t
}

// gcalToken returns a copy of the stored token, ok=false when not connected.
func gcalToken() (googleToken, bool) {
	gcalTok.Lock()
	defer gcalTok.Unlock()
	gcalLoadLocked()
	if gcalTok.tok == nil {
		return googleToken{}, false
	}
	return *gcalTok.tok, true
}

// gcalSaveToken stores t atomically with 0600.
func gcalSaveToken(t googleToken) error {
	gcalTok.Lock()
	defer gcalTok.Unlock()
	gcalLoadLocked()
	if gcalTok.readOnly {
		return errStoreReadOnly
	}
	t.Version = gcalTokenVersion
	if err := saveJSONFile(gcalTok.path, &t); err != nil {
		return err
	}
	gcalTok.tok = &t
	return nil
}

// errGcalTokenReplaced: the token a refresh started from was disconnected or
// replaced meanwhile (gcalSaveRefreshed).
var errGcalTokenReplaced = errors.New("calendar token was replaced or removed meanwhile")

// gcalSaveRefreshed stores t only while the stored token is still the one the
// refresh started from (same refresh token). A disconnect, an owner deletion
// or a new consent that happened during the refresh request wins: the old
// token is never written back.
func gcalSaveRefreshed(prevRefresh string, t googleToken) error {
	gcalTok.Lock()
	defer gcalTok.Unlock()
	gcalLoadLocked()
	if gcalTok.tok == nil || gcalTok.tok.RefreshToken != prevRefresh {
		return errGcalTokenReplaced
	}
	if gcalTok.readOnly {
		return errStoreReadOnly
	}
	t.Version = gcalTokenVersion
	if err := saveJSONFile(gcalTok.path, &t); err != nil {
		return err
	}
	gcalTok.tok = &t
	return nil
}

// gcalDeleteToken removes the token file; it returns the token it removed.
func gcalDeleteToken() (googleToken, bool, error) {
	gcalTok.Lock()
	defer gcalTok.Unlock()
	gcalLoadLocked()
	if gcalTok.readOnly {
		return googleToken{}, false, errStoreReadOnly
	}
	var old googleToken
	had := gcalTok.tok != nil
	if had {
		old = *gcalTok.tok
	}
	if err := os.Remove(gcalTok.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return old, had, err
	}
	removeCorruptCopies(gcalTok.path)
	gcalTok.tok = nil
	return old, had, nil
}

// gcalConfigured: a Google client id and secret are set (secrets.json or env).
func gcalConfigured() bool {
	id, secret := googleClient()
	return id != "" && secret != ""
}

// gcalState is the connection as the status API reports it.
type gcalState struct {
	Configured  bool
	Connected   bool // token present and usable
	NeedsRelink bool
	Scope       string
	ReadOnly    bool
}

func gcalCurrentState() gcalState {
	st := gcalState{Configured: gcalConfigured()}
	gcalTok.Lock()
	gcalLoadLocked()
	if t := gcalTok.tok; t != nil {
		st.NeedsRelink = t.NeedsRelink
		st.Connected = !t.NeedsRelink
		st.Scope = t.Scope
	}
	st.ReadOnly = gcalTok.readOnly
	gcalTok.Unlock()
	return st
}

// gcalPublishChanged tells every tab that the connection changed.
func gcalPublishChanged() {
	st := gcalCurrentState()
	calendarCacheClear()
	hubPublish("calendar.changed", "", map[string]any{"connected": st.Connected, "needsRelink": st.NeedsRelink})
	checkinWake() // re-plan meeting check-ins at once (SPEC 9.1 "on calendar.changed")
}

// ---------------------------------------------------------------------------
// OAuth: PKCE, pending states, token endpoint

type gcalPending struct {
	verifier string
	redirect string
	owner    string
	created  time.Time
}

var gcalStates = struct {
	sync.Mutex
	m map[string]gcalPending
}{m: map[string]gcalPending{}}

func randURLToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// pkceChallenge is the S256 code challenge of verifier (RFC 7636 4.2).
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// gcalAuthStart creates a single-use state with a PKCE verifier and returns
// Google's consent URL. At most gcalMaxStates are pending; the oldest goes.
func gcalAuthStart(redirect, owner string) (string, error) {
	id, secret := googleClient()
	if id == "" || secret == "" {
		return "", errCalendarNoClient
	}
	verifier, err := randURLToken(32) // 43 chars, RFC 7636: 43..128
	if err != nil {
		return "", err
	}
	state, err := randURLToken(32)
	if err != nil {
		return "", err
	}
	now := nowFunc()
	gcalStates.Lock()
	for k, p := range gcalStates.m {
		if now.Sub(p.created) > gcalStateTTL {
			delete(gcalStates.m, k)
		}
	}
	for len(gcalStates.m) >= gcalMaxStates {
		oldest, at := "", time.Time{}
		for k, p := range gcalStates.m {
			if oldest == "" || p.created.Before(at) {
				oldest, at = k, p.created
			}
		}
		delete(gcalStates.m, oldest)
	}
	gcalStates.m[state] = gcalPending{verifier: verifier, redirect: redirect, owner: owner, created: now}
	gcalStates.Unlock()
	q := url.Values{
		"client_id":             {id},
		"redirect_uri":          {redirect},
		"response_type":         {"code"},
		"scope":                 {googleCalendarScope},
		"state":                 {state},
		"code_challenge":        {pkceChallenge(verifier)},
		"code_challenge_method": {"S256"},
		"access_type":           {"offline"}, // ask for a refresh token
		"prompt":                {"consent"}, // and get one on every re-link
	}
	return googleAuthURL + "?" + q.Encode(), nil
}

// gcalTakeState removes and returns a pending state; ok=false when it is
// unknown or older than 10 minutes. A state is usable exactly once.
func gcalTakeState(state string) (gcalPending, bool) {
	if state == "" {
		return gcalPending{}, false
	}
	gcalStates.Lock()
	defer gcalStates.Unlock()
	p, ok := gcalStates.m[state]
	delete(gcalStates.m, state)
	if !ok || nowFunc().Sub(p.created) > gcalStateTTL {
		return gcalPending{}, false
	}
	return p, true
}

// gcalOAuthError is an error answer of Google's token endpoint. Its
// description is never shown to users or logged in full.
type gcalOAuthError struct {
	Status int
	Code   string
}

func (e *gcalOAuthError) Error() string {
	return fmt.Sprintf("google oauth %d %s", e.Status, e.Code)
}

// gcalNeedsRelink: Google refused the grant (revoked, expired after 7 days
// in "testing" mode, 6 months unused): the user must connect again.
func gcalNeedsRelink(err error) bool {
	var oe *gcalOAuthError
	return errors.As(err, &oe) && (oe.Code == "invalid_grant" || oe.Code == "unauthorized_client")
}

type gcalTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

func gcalTokenRequest(ctx context.Context, v url.Values) (gcalTokenResponse, error) {
	id, secret := googleClient()
	if id == "" || secret == "" {
		return gcalTokenResponse{}, errCalendarNoClient
	}
	v.Set("client_id", id)
	v.Set("client_secret", secret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL, strings.NewReader(v.Encode()))
	if err != nil {
		return gcalTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := gcalHTTP.Do(req)
	if err != nil {
		return gcalTokenResponse{}, fmt.Errorf("%w: %v", errCalendarUpstream, redactURLError(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, gcalTokenBodyLimit))
	var r gcalTokenResponse
	_ = json.Unmarshal(body, &r)
	if resp.StatusCode != http.StatusOK {
		return r, &gcalOAuthError{Status: resp.StatusCode, Code: cleanLine(r.Error, 64)}
	}
	if r.AccessToken == "" {
		return r, &gcalOAuthError{Status: resp.StatusCode, Code: "no_access_token"}
	}
	return r, nil
}

// redactURLError drops the URL from a transport error (it never holds a
// token, but keeps logs short and generic).
func redactURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func tokenExpiry(now time.Time, expiresIn int) time.Time {
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return now.Add(time.Duration(expiresIn) * time.Second)
}

// scopeHas reports whether a space-separated scope list grants want.
func scopeHas(scopes, want string) bool {
	for _, s := range strings.Fields(scopes) {
		if s == want || s == "https://www.googleapis.com/auth/calendar" {
			return true
		}
	}
	return false
}

// Callback failure reasons (the UI maps them to DE/EN text).
const (
	gcalReasonDenied   = "denied"
	gcalReasonState    = "state"
	gcalReasonExchange = "exchange"
	gcalReasonScope    = "scope"
	gcalReasonRefresh  = "refresh"
	gcalReasonStore    = "store"
	gcalReasonOwner    = "owner"
)

// gcalCallback finishes the consent flow: it exchanges the code with the
// stored PKCE verifier, checks the granted scope and stores the token and
// the calendar owner. It returns a reason code on failure.
func gcalCallback(ctx context.Context, q url.Values) (reason string, err error) {
	p, ok := gcalTakeState(q.Get("state"))
	if !ok {
		return gcalReasonState, errors.New("unknown or expired state")
	}
	if e := q.Get("error"); e != "" {
		return gcalReasonDenied, fmt.Errorf("google: %s", cleanLine(e, 64))
	}
	code := q.Get("code")
	if code == "" || len(code) > 2048 {
		return gcalReasonExchange, errors.New("missing code")
	}
	now := nowFunc()
	r, err := gcalTokenRequest(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {p.verifier},
		"redirect_uri":  {p.redirect},
	})
	if err != nil {
		return gcalReasonExchange, err
	}
	if !scopeHas(r.Scope, googleCalendarScope) {
		return gcalReasonScope, fmt.Errorf("granted scope lacks calendar.events")
	}
	if r.RefreshToken == "" {
		return gcalReasonRefresh, errors.New("google returned no refresh token")
	}
	if !personExists(p.owner) {
		return gcalReasonOwner, errors.New("calendar owner was deleted meanwhile")
	}
	clientID, _ := googleClient()
	tok := googleToken{
		RefreshToken: r.RefreshToken, AccessToken: r.AccessToken,
		Expiry: tokenExpiry(now, r.ExpiresIn), Scope: r.Scope, ClientID: clientID,
	}
	if err := gcalSaveToken(tok); err != nil {
		return gcalReasonStore, err
	}
	if err := updateSecrets(func(s *secretsFile) error {
		if s.CalendarOwner == p.owner {
			return errNoChange
		}
		s.CalendarOwner = p.owner
		return nil
	}); err != nil {
		return gcalReasonStore, err
	}
	return "", nil
}

// gcalAccessToken returns a valid access token, refreshing it when needed.
// force refreshes even an unexpired token (after a 401 from the API).
func gcalAccessToken(ctx context.Context, force bool) (string, error) {
	t, ok := gcalToken()
	if !ok {
		return "", errCalendarNotConnected
	}
	if t.NeedsRelink {
		return "", errCalendarRelink
	}
	if gcalClientMismatch(t) {
		gcalClientChanged()
		return "", errCalendarRelink
	}
	if !force && t.AccessToken != "" && nowFunc().Add(gcalExpirySkew).Before(t.Expiry) {
		return t.AccessToken, nil
	}
	gcalTok.refreshMu.Lock()
	defer gcalTok.refreshMu.Unlock()
	// Another request may have refreshed while this one waited.
	if t2, ok := gcalToken(); !ok {
		return "", errCalendarNotConnected
	} else if t2.AccessToken != t.AccessToken && !t2.NeedsRelink && nowFunc().Add(gcalExpirySkew).Before(t2.Expiry) {
		return t2.AccessToken, nil
	} else {
		t = t2
	}
	now := nowFunc()
	prevRefresh := t.RefreshToken
	r, err := gcalTokenRequest(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken}})
	if err != nil {
		if gcalNeedsRelink(err) {
			t.NeedsRelink = true
			t.AccessToken = ""
			if serr := gcalSaveRefreshed(prevRefresh, t); errors.Is(serr, errGcalTokenReplaced) {
				return "", errCalendarNotConnected
			} else if serr != nil {
				log.Printf("calendar: could not mark the token for relink: %v", serr)
			}
			log.Printf("calendar: Google refused the refresh token; the calendar must be connected again")
			gcalPublishChanged()
			return "", errCalendarRelink
		}
		log.Printf("calendar: token refresh failed: %v", err)
		if errors.Is(err, errCalendarNoClient) {
			return "", err
		}
		return "", errCalendarUpstream
	}
	t.AccessToken = r.AccessToken
	t.Expiry = tokenExpiry(now, r.ExpiresIn)
	if r.RefreshToken != "" { // Google rarely rotates it; keep the old one otherwise
		t.RefreshToken = r.RefreshToken
	}
	if r.Scope != "" {
		t.Scope = r.Scope
	}
	if err := gcalSaveRefreshed(prevRefresh, t); errors.Is(err, errGcalTokenReplaced) {
		// Disconnected (or re-linked) while Google answered: this token is
		// no longer ours to use.
		return "", errCalendarNotConnected
	} else if err != nil {
		log.Printf("calendar: could not store the refreshed token: %v", err)
	}
	return t.AccessToken, nil
}

// gcalClientMismatch: the token was issued to another OAuth client than the
// configured one (changed in the settings or in .env).
func gcalClientMismatch(t googleToken) bool {
	id, _ := googleClient()
	return t.ClientID != "" && id != "" && id != t.ClientID
}

// gcalClientChanged runs when the Google client id or secret changed. Open
// consent flows were started with the old client, so they are dropped. A
// refresh token belongs to the client that obtained it: when the client id
// differs (or is unknown), the calendar must be connected again. The token
// is kept so a disconnect can still revoke it.
func gcalClientChanged() {
	gcalStates.Lock()
	gcalStates.m = map[string]gcalPending{}
	gcalStates.Unlock()

	gcalTok.refreshMu.Lock() // no refresh with the old client may overwrite this
	id, _ := googleClient()
	changed := false
	gcalTok.Lock()
	gcalLoadLocked()
	if t := gcalTok.tok; t != nil && !t.NeedsRelink && !gcalTok.readOnly && (t.ClientID == "" || t.ClientID != id) {
		c := *t
		c.NeedsRelink, c.AccessToken, c.Version = true, "", gcalTokenVersion
		if err := saveJSONFile(gcalTok.path, &c); err != nil {
			log.Printf("calendar: could not mark the token for relink: %v", err)
		} else {
			gcalTok.tok = &c
			changed = true
		}
	}
	gcalTok.Unlock()
	gcalTok.refreshMu.Unlock()
	if changed {
		log.Printf("calendar: the Google client changed; the calendar must be connected again")
		gcalPublishChanged()
	}
}

// gcalAPIError is a non-2xx answer of the Calendar API.
type gcalAPIError struct{ Status int }

func (e *gcalAPIError) Error() string { return fmt.Sprintf("calendar api %d", e.Status) }

// gcalDo calls the Calendar API with a bearer token; on 401 it refreshes
// once and retries.
func gcalDo(ctx context.Context, method, u string, in, out any) error {
	var body []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = b
	}
	for attempt := 0; attempt < 2; attempt++ {
		at, err := gcalAccessToken(ctx, attempt > 0)
		if err != nil {
			return err
		}
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+at)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := gcalHTTP.Do(req)
		if err != nil {
			log.Printf("calendar: %s failed: %v", method, redactURLError(err))
			return errCalendarUpstream
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, gcalAPIBodyLimit))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		if resp.StatusCode/100 != 2 {
			return &gcalAPIError{Status: resp.StatusCode}
		}
		if out == nil || len(bytes.TrimSpace(b)) == 0 {
			return nil
		}
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("%w: bad JSON", errCalendarUpstream)
		}
		return nil
	}
	return &gcalAPIError{Status: http.StatusUnauthorized}
}

// ---------------------------------------------------------------------------
// Calendar API: list, insert, delete

type gcalEventTime struct {
	DateTime string `json:"dateTime,omitempty"`
	Date     string `json:"date,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
}

type gcalAttendee struct {
	Self           bool   `json:"self,omitempty"`
	ResponseStatus string `json:"responseStatus,omitempty"`
}

type gcalEvent struct {
	ID        string         `json:"id,omitempty"`
	Summary   string         `json:"summary"`
	Location  string         `json:"location,omitempty"`
	Start     gcalEventTime  `json:"start"`
	End       gcalEventTime  `json:"end"`
	Status    string         `json:"status,omitempty"`
	Attendees []gcalAttendee `json:"attendees,omitempty"`
}

// toCalendarEvent converts an API event; ok=false for cancelled or
// unreadable ones. All-day ends are exclusive (Google's convention).
func (e gcalEvent) toCalendarEvent(loc *time.Location) (CalendarEvent, bool) {
	if e.Status == "cancelled" {
		return CalendarEvent{}, false
	}
	ev := CalendarEvent{
		ID:       truncateRunes(e.ID, 1024),
		Title:    cleanText(strings.Join(strings.Fields(e.Summary), " "), gcalTitleMax),
		Location: cleanText(strings.Join(strings.Fields(e.Location), " "), gcalTitleMax),
	}
	var err error
	switch {
	case e.Start.Date != "":
		ev.AllDay = true
		if ev.Start, err = time.ParseInLocation("2006-01-02", e.Start.Date, loc); err != nil {
			return CalendarEvent{}, false
		}
		ev.End, err = time.ParseInLocation("2006-01-02", e.End.Date, loc)
		if err != nil || !ev.End.After(ev.Start) {
			ev.End = ev.Start.AddDate(0, 0, 1)
		}
	case e.Start.DateTime != "":
		if ev.Start, err = time.Parse(time.RFC3339, e.Start.DateTime); err != nil {
			return CalendarEvent{}, false
		}
		ev.Start = ev.Start.In(loc)
		if e.End.DateTime != "" {
			if end, err := time.Parse(time.RFC3339, e.End.DateTime); err == nil {
				ev.End = end.In(loc)
			}
		}
		if !ev.End.After(ev.Start) {
			ev.End = ev.Start
		}
	default:
		return CalendarEvent{}, false
	}
	for _, a := range e.Attendees {
		if a.Self && a.ResponseStatus == "needsAction" {
			ev.NeedsAction = true
		}
	}
	return ev, true
}

func gcalEventsURL() string {
	return strings.TrimRight(googleCalendarAPI, "/") + "/calendars/primary/events"
}

// gcalList returns the events overlapping [from, to) of the primary
// calendar, recurring events expanded, sorted by start, times in loc.
func gcalList(ctx context.Context, from, to time.Time, loc *time.Location) ([]CalendarEvent, error) {
	if !to.After(from) || to.Sub(from) > gcalMaxRange+time.Hour { // +1h: DST
		return nil, errCalendarRange
	}
	if loc == nil {
		loc = settingsLocation(loadSettings())
	}
	q := url.Values{
		"timeMin":      {from.Format(time.RFC3339)},
		"timeMax":      {to.Format(time.RFC3339)},
		"singleEvents": {"true"},
		"orderBy":      {"startTime"},
		"maxResults":   {fmt.Sprint(gcalPageSize)},
		"timeZone":     {loc.String()},
	}
	var out []CalendarEvent
	for page := 0; page < gcalMaxPages; page++ {
		var r struct {
			Items         []gcalEvent `json:"items"`
			NextPageToken string      `json:"nextPageToken"`
		}
		if err := gcalDo(ctx, http.MethodGet, gcalEventsURL()+"?"+q.Encode(), nil, &r); err != nil {
			return nil, err
		}
		for _, e := range r.Items {
			if ev, ok := e.toCalendarEvent(loc); ok {
				out = append(out, ev)
			}
		}
		if r.NextPageToken == "" || len(r.NextPageToken) > 2048 {
			break
		}
		q.Set("pageToken", r.NextPageToken)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// gcalInsert creates a timed event (dur > 0) or an all-day event (allDay)
// on start's day, in loc.
func gcalInsert(ctx context.Context, title string, start time.Time, dur time.Duration, allDay bool, loc *time.Location) (CalendarEvent, error) {
	if loc == nil {
		loc = settingsLocation(loadSettings())
	}
	ev := gcalEvent{Summary: title}
	if allDay {
		d := midnightOf(start.In(loc))
		ev.Start = gcalEventTime{Date: d.Format("2006-01-02")}
		ev.End = gcalEventTime{Date: d.AddDate(0, 0, 1).Format("2006-01-02")}
	} else {
		if dur <= 0 {
			dur = time.Hour
		}
		ev.Start = gcalEventTime{DateTime: start.In(loc).Format(time.RFC3339), TimeZone: loc.String()}
		ev.End = gcalEventTime{DateTime: start.Add(dur).In(loc).Format(time.RFC3339), TimeZone: loc.String()}
	}
	var created gcalEvent
	if err := gcalDo(ctx, http.MethodPost, gcalEventsURL(), ev, &created); err != nil {
		return CalendarEvent{}, err
	}
	if created.ID == "" {
		return CalendarEvent{}, fmt.Errorf("%w: no event id", errCalendarUpstream)
	}
	out, ok := created.toCalendarEvent(loc)
	if !ok { // a sparse answer: describe what was sent
		out, _ = ev.toCalendarEvent(loc)
		out.ID = created.ID
	}
	gcalPublishChanged()
	return out, nil
}

// gcalDelete removes an event (undo of an insert). An event that is already
// gone counts as deleted.
func gcalDelete(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("no event id")
	}
	err := gcalDo(ctx, http.MethodDelete, gcalEventsURL()+"/"+url.PathEscape(id), nil, nil)
	var ae *gcalAPIError
	if errors.As(err, &ae) && (ae.Status == http.StatusGone || ae.Status == http.StatusNotFound) {
		err = nil
	}
	if err == nil {
		gcalPublishChanged()
	}
	return err
}

// gcalRevoke asks Google to revoke a token (best effort).
func gcalRevoke(ctx context.Context, token string) {
	if token == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleRevokeURL, strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := gcalHTTP.Do(req)
	if err != nil {
		log.Printf("calendar: revoke failed: %v", redactURLError(err))
		return
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("calendar: revoke answered %d", resp.StatusCode)
	}
}

// gcalDisconnect revokes the token (best effort) and deletes the token file.
func gcalDisconnect(ctx context.Context) error {
	t, had, err := gcalDeleteToken()
	if err != nil {
		return err
	}
	if had {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		gcalRevoke(rctx, t.RefreshToken)
		cancel()
	}
	gcalPublishChanged()
	return nil
}

// ---------------------------------------------------------------------------
// HTTP: status, connect, callback, disconnect (SPEC 3.7)

// gcalStatus is GET /api/calendar/status and the "calendar" status entry.
func gcalStatus() map[string]any {
	st := gcalCurrentState()
	owner := calendarOwner()
	name := ""
	if owner != "" {
		name, _ = personName(owner)
	}
	errText := ""
	switch {
	case st.ReadOnly:
		errText = "read_only"
	case st.NeedsRelink:
		errText = "relink"
	}
	return map[string]any{
		"configured": st.Configured, "connected": st.Connected, "needsRelink": st.NeedsRelink,
		"scope": st.Scope, "owner": owner, "ownerName": name, "error": errText,
	}
}

func handleCalendarStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, gcalStatus())
}

// fetchSiteOK: only same-origin requests (or clients that send no
// Sec-Fetch-Site, like curl) may change the connection.
func fetchSiteOK(r *http.Request) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	return site == "" || site == "same-origin"
}

// loopbackRedirect builds the OAuth redirect URI from a loopback Host
// ("localhost:8080", "127.0.0.1:8080", "[::1]:8080"); ok=false otherwise.
func loopbackRedirect(host string) (string, bool) {
	if !isLoopbackHost(host) {
		return "", false
	}
	h, port, err := net.SplitHostPort(host)
	if err != nil {
		h, port = host, ""
	}
	h = strings.ToLower(strings.Trim(h, "[]"))
	if h != "localhost" {
		ip := net.ParseIP(h)
		if ip == nil || !ip.IsLoopback() {
			return "", false // "*.localhost" names are not accepted by Google
		}
		if ip.To4() == nil {
			h = "[" + h + "]"
		}
	}
	if port != "" {
		h += ":" + port
	}
	return "http://" + h + "/api/calendar/callback", true
}

func handleCalendarConnect(w http.ResponseWriter, r *http.Request) {
	if !isAdminRequest(r) {
		writeJSONErrorCode(w, http.StatusForbidden, "local_only", "only directly on this machine (http://localhost:8080)")
		return
	}
	if !fetchSiteOK(r) {
		writeJSONErrorCode(w, http.StatusForbidden, "cross_site", "cross-site request blocked")
		return
	}
	var body struct {
		Owner string `json:"owner"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeJSONErrorCode(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	owner := strings.TrimSpace(body.Owner)
	if owner == "household" {
		owner = ""
	}
	if owner != "" && (!strings.HasPrefix(owner, "p_") || len(owner) > 64 || !personExists(owner)) {
		writeJSONErrorCode(w, http.StatusBadRequest, "owner", "unknown person")
		return
	}
	redirect, ok := loopbackRedirect(r.Host)
	if !ok {
		writeJSONErrorCode(w, http.StatusForbidden, "local_only", "open HERMES as http://localhost:8080 to connect the calendar")
		return
	}
	authURL, err := gcalAuthStart(redirect, owner)
	if errors.Is(err, errCalendarNoClient) {
		writeJSONErrorCode(w, http.StatusServiceUnavailable, "no_client", "Google client id or secret missing")
		return
	}
	if err != nil {
		log.Printf("calendar: connect: %v", err)
		writeJSONErrorCode(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"authUrl": authURL})
}

func handleCalendarCallback(w http.ResponseWriter, r *http.Request) {
	// Google's redirect is a top-level navigation on this machine: it must
	// arrive with a loopback Host (an admin request always has one) and a
	// valid pending state, which gcalCallback checks.
	if !isLoopbackHost(r.Host) {
		writeJSONErrorCode(w, http.StatusForbidden, "local_only", "only directly on this machine (http://localhost:8080)")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	reason, err := gcalCallback(ctx, r.URL.Query())
	if err != nil {
		log.Printf("calendar: connect failed (%s): %v", reason, err)
		http.Redirect(w, r, "/?calendar=error&reason="+url.QueryEscape(reason), http.StatusSeeOther)
		return
	}
	log.Printf("calendar: connected")
	gcalPublishChanged()
	http.Redirect(w, r, "/?calendar=connected", http.StatusSeeOther)
}

func handleCalendarDisconnect(w http.ResponseWriter, r *http.Request) {
	if !isAdminRequest(r) {
		writeJSONErrorCode(w, http.StatusForbidden, "local_only", "only directly on this machine (http://localhost:8080)")
		return
	}
	if !fetchSiteOK(r) {
		writeJSONErrorCode(w, http.StatusForbidden, "cross_site", "cross-site request blocked")
		return
	}
	if err := gcalDisconnect(r.Context()); err != nil {
		if errors.Is(err, errStoreReadOnly) {
			writeJSONErrorCode(w, http.StatusServiceUnavailable, "read_only", err.Error())
			return
		}
		log.Printf("calendar: disconnect: %v", err)
		writeJSONErrorCode(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	log.Printf("calendar: disconnected")
	writeJSON(w, http.StatusOK, map[string]bool{"connected": false})
}

// calendarOwnerDeleted: the owner's calendar credential is their personal
// data, so deleting the owner disconnects the calendar (revoke + delete).
func calendarOwnerDeleted(personID string) (string, int) {
	if personID == "" || loadSecrets().CalendarOwner != personID {
		return "", 0
	}
	_ = updateSecrets(func(s *secretsFile) error {
		if s.CalendarOwner != personID {
			return errNoChange
		}
		s.CalendarOwner = ""
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := gcalDisconnect(ctx); err != nil {
		log.Printf("calendar: disconnect after deleting the owner: %v", err)
		return "calendar", 0
	}
	return "calendar", 1
}

func init() {
	calendarEvents = func(ctx context.Context, from, to time.Time) ([]CalendarEvent, error) {
		return gcalList(ctx, from, to, settingsLocation(loadSettings()))
	}
	calendarOwnerProvider = func() string { return loadSecrets().CalendarOwner }
	calendarConnectedProvider = func() bool { return gcalCurrentState().Connected }
	registerStatus("calendar", func(context.Context) any { return gcalStatus() })
	onPersonDeleted(calendarOwnerDeleted)
	onGoogleClientChange(gcalClientChanged)

	registerRoute("GET /api/calendar/status", handleCalendarStatus)
	registerRoute("POST /api/calendar/connect", handleCalendarConnect)
	registerRoute("GET /api/calendar/callback", handleCalendarCallback)
	registerRoute("POST /api/calendar/disconnect", handleCalendarDisconnect)
}
