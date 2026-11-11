package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"
)

// homeassistant.go: smart home through Home Assistant (SPEC 3.8, 8.7, 9
// Feature 5).
//
//   - A separate PINNED client: it only ever dials the address the configured
//     HA URL resolved to (re-resolved at most every 10 minutes), follows no
//     redirects, uses no proxy and may only call a fixed list of paths. The
//     URL is validated with normalizeHAURL on every use and the token is read
//     with haToken() per request; onHAConfigChange drops client and caches.
//   - An entity cache (/api/states plus one fixed, read-only area template,
//     60 s) classifies every entity: controllable (light, switch, climate,
//     scene, media_player, cover except garage/gate/door), readable (sensor,
//     binary_sensor), blocked (lock, alarm_control_panel, script, automation,
//     siren, garage/gate/door covers and door/lock sensors) or other.
//   - Pre-classification: locks, alarms, scripts, automations, doors, gates
//     and garages are refused BEFORE anything is sent to HA. Then HA Assist
//     (POST /api/conversation/process, language = haLanguage or the voice
//     language) for the user's own words, with a fallback to whitelisted
//     services (/api/services/<domain>/<service>) for structured arguments.
//   - Fast paths 80 (home_control), 81 (home_query), 82 (home_setup: setup
//     or "switched off" hint) and the router tools home_control (side effect:
//     imperative runs, otherwise a "Soll ich …?" offer) and home_query.
//   - Access: control is Open (homeControl=everyone) or Write (known), off
//     disables it; queries are Read (homeQueries=known) or Open (everyone).
//   - Routes /api/home/status, /api/home/test (admin), /api/home/entities;
//     status provider "home". The token never appears in a response or log.
//   - HA states and names never leave the machine or go into persona
//     context: replies are tagged fast:/tool: and nothing here registers a
//     context section.

// ---------------------------------------------------------------------------
// Limits and timings

var (
	haTimeout     = 5 * time.Second // per HA request (SPEC: timeout 5 s)
	haDialTimeout = 3 * time.Second
)

const (
	haClientTTL       = 10 * time.Minute // re-resolve the configured host at most this often
	haCacheTTL        = 60 * time.Second // entity cache and status
	haQueryMaxAge     = 15 * time.Second // state questions want fresh states
	haStaleMatch      = 30 * time.Minute // fast-path name matching may use an older cache
	haStatesMax       = 8 << 20          // /api/states body
	haBodyMax         = 256 << 10        // every other body
	haMaxEntities     = 5000
	haMaxAreas        = 200
	haMaxTargets      = 10
	haMaxEnum         = 150 // router enum entries
	haUIEntitiesMax   = 1000
	haQueryRunes      = 300 // text sent to Assist
	haTargetRunes     = 100
	haSpeechRunes     = 300
	haNameRunes       = 100
	haAreaRunes       = 60
	haMinTemp         = 5.0
	haMaxTemp         = 30.0
	haProbeTimeoutMul = 3 // status probe: config + states + template
)

// ---------------------------------------------------------------------------
// Errors (generic codes only; upstream bodies are never kept)

type haError struct{ Code string }

func (e *haError) Error() string { return "home assistant: " + e.Code }

var (
	errHANotConfigured = &haError{"not_configured"}
	errHAUnreachable   = &haError{"unreachable"}
	errHAAuth          = &haError{"auth"}
	errHATimeout       = &haError{"timeout"}
	errHABadResponse   = &haError{"bad_response"}
	errHANotAllowed    = &haError{"not_allowed"}
	errHAPinned        = errors.New("refusing to dial: not the configured Home Assistant address")
)

func haCode(err error) string {
	var he *haError
	if errors.As(err, &he) {
		return he.Code
	}
	return "bad_response"
}

// ---------------------------------------------------------------------------
// Configuration

// haConfig returns the normalised URL and whether URL and token are usable.
func haConfig() (base string, ok bool) {
	raw := haURL()
	if raw == "" {
		return "", false
	}
	n, err := normalizeHAURL(raw)
	if err != nil {
		return "", false
	}
	return n, validCredential(haToken(), haTokenMaxLen)
}

func haConfigured() bool {
	_, ok := haConfig()
	return ok
}

// ---------------------------------------------------------------------------
// Pinned client

type haClient struct {
	base     string   // scheme://host[:port], normalised
	hostport string   // host:port the transport asks to dial
	pins     []string // resolved ip:port pairs; the only addresses ever dialled
	http     *http.Client
	tr       *http.Transport
	built    time.Time
}

// newHAClient resolves the configured host once and pins the result.
func newHAClient(ctx context.Context, base string) (*haClient, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errHANotConfigured
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else {
		rctx, cancel := context.WithTimeout(ctx, haDialTimeout)
		ips, err = net.DefaultResolver.LookupNetIP(rctx, "ip", host)
		cancel()
		if err != nil || len(ips) == 0 {
			// In Docker, .local names do not resolve: docs/smart-home.md says to use the IP.
			log.Printf("home: cannot resolve the Home Assistant host")
			return nil, errHAUnreachable
		}
	}
	c := &haClient{base: base, hostport: strings.ToLower(net.JoinHostPort(host, port)), built: time.Now()}
	seen := map[string]bool{}
	for _, ip := range ips {
		p := net.JoinHostPort(ip.Unmap().WithZone("").String(), port)
		if !seen[p] {
			seen[p] = true
			c.pins = append(c.pins, p)
		}
	}
	c.tr = &http.Transport{
		Proxy:                 nil, // never through a proxy
		DialContext:           c.dial,
		TLSHandshakeTimeout:   haDialTimeout,
		ResponseHeaderTimeout: haTimeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       90 * time.Second,
	}
	c.http = &http.Client{
		Transport: c.tr,
		// A redirect could point anywhere: never follow one.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return c, nil
}

// dial connects only to the pinned addresses of the configured host. The
// transport passes the URL's host:port; anything else is refused, and the
// Control hook re-checks the address actually dialled.
func (c *haClient) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if !strings.EqualFold(addr, c.hostport) {
		return nil, errHAPinned
	}
	d := &net.Dialer{Timeout: haDialTimeout, Control: c.control}
	var lastErr error = errHAPinned
	for _, p := range c.pins {
		conn, err := d.DialContext(ctx, network, p)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (c *haClient) control(_, address string, _ syscall.RawConn) error {
	h, p, err := net.SplitHostPort(address)
	if err != nil {
		return errHAPinned
	}
	ip, err := netip.ParseAddr(h)
	if err != nil {
		return errHAPinned
	}
	want := net.JoinHostPort(ip.Unmap().WithZone("").String(), p)
	for _, pin := range c.pins {
		if pin == want {
			return nil
		}
	}
	return errHAPinned
}

func (c *haClient) close() {
	if c != nil && c.tr != nil {
		c.tr.CloseIdleConnections()
	}
}

// haServicePattern is /api/services/<domain>/<service>.
var haServicePattern = regexp.MustCompile(`^/api/services/([a-z_]+)/([a-z_]+)$`)

// haPathAllowed is the fixed list of paths this client may call.
func haPathAllowed(method, path string) bool {
	switch {
	case method == http.MethodGet && (path == "/api/config" || path == "/api/states"):
		return true
	case method == http.MethodPost && (path == "/api/conversation/process" || path == "/api/template"):
		return true
	case method == http.MethodPost:
		m := haServicePattern.FindStringSubmatch(path)
		return m != nil && haServices[m[1]+"/"+m[2]] != nil
	}
	return false
}

// do sends one request. out may be nil, a *[]byte (raw body) or a pointer
// for encoding/json. Errors are generic codes; bodies are never logged.
func (c *haClient) do(ctx context.Context, method, path string, in, out any, max int64) error {
	if !haPathAllowed(method, path) {
		return errHANotAllowed
	}
	token := haToken()
	if !validCredential(token, haTokenMaxLen) {
		return errHANotConfigured
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return errHANotAllowed
		}
		body = bytes.NewReader(b)
	}
	rctx, cancel := context.WithTimeout(ctx, haTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, method, c.base+path, body)
	if err != nil {
		return errHANotConfigured
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ne net.Error
		switch {
		case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
			return errHATimeout
		case ctx.Err() != nil:
			return errHATimeout
		}
		return errHAUnreachable
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return errHAAuth
	case resp.StatusCode/100 != 2: // incl. 3xx: redirects are not followed
		return errHABadResponse
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		if rctx.Err() != nil {
			return errHATimeout
		}
		return errHABadResponse
	}
	if int64(len(b)) > max {
		return errHABadResponse
	}
	switch o := out.(type) {
	case nil:
		return nil
	case *[]byte:
		*o = b
		return nil
	default:
		if json.Unmarshal(b, out) != nil {
			return errHABadResponse
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Service whitelist. Each validator checks every key of the call data.

type haServiceCheck func(data map[string]any) error

func haEntityList(domain string, v any) error {
	ids, ok := v.([]string)
	if !ok || len(ids) == 0 || len(ids) > haMaxTargets {
		return errHANotAllowed
	}
	for _, id := range ids {
		if !haEntityIDPattern.MatchString(id) || !strings.HasPrefix(id, domain+".") {
			return errHANotAllowed
		}
	}
	return nil
}

// haCheck builds a validator: entity_id is required, extra keys must pass
// their own check.
func haCheck(domain string, extra map[string]func(any) bool) haServiceCheck {
	return func(data map[string]any) error {
		if err := haEntityList(domain, data["entity_id"]); err != nil {
			return err
		}
		for k, v := range data {
			if k == "entity_id" {
				continue
			}
			ok := extra[k]
			if ok == nil || !ok(v) {
				return errHANotAllowed
			}
		}
		for k := range extra {
			if k == "temperature" || k == "hvac_mode" || k == "volume_level" { // required
				if _, has := data[k]; !has {
					return errHANotAllowed
				}
			}
		}
		return nil
	}
}

func haNumberIn(lo, hi float64) func(any) bool {
	return func(v any) bool {
		f, ok := v.(float64)
		if !ok {
			if i, isInt := v.(int); isInt {
				f, ok = float64(i), true
			}
		}
		return ok && !math.IsNaN(f) && f >= lo && f <= hi
	}
}

var haServices = map[string]haServiceCheck{
	"light/turn_on":           haCheck("light", map[string]func(any) bool{"brightness_pct": haNumberIn(1, 100)}),
	"light/turn_off":          haCheck("light", nil),
	"switch/turn_on":          haCheck("switch", nil),
	"switch/turn_off":         haCheck("switch", nil),
	"climate/set_temperature": haCheck("climate", map[string]func(any) bool{"temperature": haNumberIn(haMinTemp, haMaxTemp)}),
	"climate/set_hvac_mode": haCheck("climate", map[string]func(any) bool{"hvac_mode": func(v any) bool {
		s, _ := v.(string)
		return s == "off" || s == "heat" || s == "auto"
	}}),
	"scene/turn_on":            haCheck("scene", nil),
	"cover/open_cover":         haCheck("cover", nil),
	"cover/close_cover":        haCheck("cover", nil),
	"media_player/turn_on":     haCheck("media_player", nil),
	"media_player/turn_off":    haCheck("media_player", nil),
	"media_player/media_play":  haCheck("media_player", nil),
	"media_player/media_pause": haCheck("media_player", nil),
	"media_player/media_stop":  haCheck("media_player", nil),
	"media_player/volume_set":  haCheck("media_player", map[string]func(any) bool{"volume_level": haNumberIn(0, 1)}),
}

// callService runs a whitelisted service with validated data.
func (c *haClient) callService(ctx context.Context, domain, service string, data map[string]any) error {
	check := haServices[domain+"/"+service]
	if check == nil || check(data) != nil {
		return errHANotAllowed
	}
	return c.do(ctx, http.MethodPost, "/api/services/"+domain+"/"+service, data, nil, haBodyMax)
}

// haAssistResult is the part of an Assist answer HERMES uses.
type haAssistResult struct {
	Speech  string
	Type    string // action_done | query_answer | error
	Code    string // on error: no_intent_match | no_valid_targets | …
	Targets []string
}

// process sends the user's own words to HA Assist (no conversation id, so
// nothing carries over between turns).
func (c *haClient) process(ctx context.Context, text, lang string) (haAssistResult, error) {
	var r struct {
		Response struct {
			ResponseType string `json:"response_type"`
			Speech       struct {
				Plain struct {
					Speech string `json:"speech"`
				} `json:"plain"`
			} `json:"speech"`
			Data struct {
				Code    string `json:"code"`
				Success []struct {
					ID   string `json:"id"`
					Type string `json:"type"`
				} `json:"success"`
			} `json:"data"`
		} `json:"response"`
	}
	in := map[string]string{"text": text, "language": lang}
	if err := c.do(ctx, http.MethodPost, "/api/conversation/process", in, &r, haBodyMax); err != nil {
		return haAssistResult{}, err
	}
	res := haAssistResult{
		Speech: haCleanSpeech(r.Response.Speech.Plain.Speech),
		Type:   r.Response.ResponseType,
		Code:   r.Response.Data.Code,
	}
	for _, s := range r.Response.Data.Success {
		if s.Type == "" || s.Type == "entity" {
			res.Targets = append(res.Targets, s.ID)
		}
	}
	return res, nil
}

func haCleanSpeech(s string) string {
	s = cleanText(strings.Join(strings.Fields(s), " "), haSpeechRunes)
	for strings.Contains(s, "<<<") || strings.Contains(s, ">>>") {
		s = strings.ReplaceAll(strings.ReplaceAll(s, "<<<", ""), ">>>", "")
	}
	return s
}

// ---------------------------------------------------------------------------
// Entities and their classification

type haClass int

const (
	haOther   haClass = iota // not listed, never touched
	haControl                // may be switched / set
	haRead                   // may be asked about
	haBlocked                // refused on every path
)

type haEntity struct {
	ID, Name, Area, Domain, DeviceClass, State, Unit string
	Class                                            haClass
	CurTemp, SetTemp                                 float64 // climate (NaN when absent)
	nameToks                                         []string
}

type haArea struct {
	Name  string
	toks  []string
	group string // room synonym group ("" if none)
}

type haCache struct {
	base    string
	gen     uint64
	at      time.Time
	ents    []*haEntity
	areas   []*haArea
	version string
	stale   bool // states changed by a command: state reads refetch, name matching does not
}

var haEntityIDPattern = regexp.MustCompile(`^[a-z0-9_]+\.[a-z0-9_]+$`)

var haBlockedDeviceClasses = wordSet("garage", "gate", "door")
var haBlockedSensorClasses = wordSet("door", "garage_door", "lock", "opening", "tamper", "safety")

func haClassify(domain, deviceClass string) haClass {
	switch domain {
	case "lock", "alarm_control_panel", "script", "automation", "siren":
		return haBlocked
	case "cover":
		if haBlockedDeviceClasses[deviceClass] {
			return haBlocked
		}
		return haControl
	case "light", "switch", "climate", "scene", "media_player":
		return haControl
	case "sensor":
		return haRead
	case "binary_sensor":
		if haBlockedSensorClasses[deviceClass] {
			return haBlocked
		}
		return haRead
	}
	return haOther
}

// haOtherKept are device domains HERMES does not control but keeps in the
// cache by name, so "Schalte Robi ein" (a vacuum) gets an honest refusal
// instead of reaching Assist. They are never listed, counted or targeted.
var haOtherKept = wordSet("vacuum", "fan", "humidifier", "water_heater", "valve", "lawn_mower",
	"button", "input_boolean", "input_button", "remote", "camera", "number", "select")

// haAreaTemplate maps entity ids to area names in one read-only call. It is
// a constant: no user text ever reaches /api/template.
const haAreaTemplate = `{% set ns = namespace(o=[]) %}{% for s in states %}{% set a = area_name(s.entity_id) %}{% if a %}{% set ns.o = ns.o + [[s.entity_id, a]] %}{% endif %}{% endfor %}{{ ns.o | tojson }}`

func haAttrString(attrs map[string]json.RawMessage, key string, max int) string {
	var s string
	if raw, ok := attrs[key]; ok && json.Unmarshal(raw, &s) == nil {
		return cleanText(strings.Join(strings.Fields(s), " "), max)
	}
	return ""
}

func haAttrNumber(attrs map[string]json.RawMessage, key string) float64 {
	var f float64
	if raw, ok := attrs[key]; ok && json.Unmarshal(raw, &f) == nil && !math.IsInf(f, 0) {
		return f
	}
	return math.NaN()
}

// fetchCache loads /api/states (and, best effort, the areas).
func (c *haClient) fetchCache(ctx context.Context) (*haCache, error) {
	var states []struct {
		EntityID   string                     `json:"entity_id"`
		State      string                     `json:"state"`
		Attributes map[string]json.RawMessage `json:"attributes"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/states", nil, &states, haStatesMax); err != nil {
		return nil, err
	}
	areaOf := map[string]string{}
	var raw []byte
	// Areas are best effort: a restricted (non-admin) HA user may not render
	// templates; matching then works on entity names only.
	if err := c.do(ctx, http.MethodPost, "/api/template", map[string]string{"template": haAreaTemplate}, &raw, haStatesMax); err == nil {
		var pairs [][]string
		if json.Unmarshal(bytes.TrimSpace(raw), &pairs) == nil {
			for _, p := range pairs {
				if len(p) == 2 {
					areaOf[p[0]] = cleanText(strings.Join(strings.Fields(p[1]), " "), haAreaRunes)
				}
			}
		}
	}
	out := &haCache{base: c.base, at: time.Now()}
	areas := map[string]*haArea{}
	for _, s := range states {
		if len(out.ents) >= haMaxEntities {
			break
		}
		if len(s.EntityID) > 255 || !haEntityIDPattern.MatchString(s.EntityID) {
			continue
		}
		domain, _, _ := strings.Cut(s.EntityID, ".")
		e := &haEntity{
			ID:          s.EntityID,
			Domain:      domain,
			State:       cleanText(s.State, 64),
			Name:        haAttrString(s.Attributes, "friendly_name", haNameRunes),
			DeviceClass: haAttrString(s.Attributes, "device_class", 40),
			Unit:        haAttrString(s.Attributes, "unit_of_measurement", 16),
			CurTemp:     haAttrNumber(s.Attributes, "current_temperature"),
			SetTemp:     haAttrNumber(s.Attributes, "temperature"),
			Area:        areaOf[s.EntityID],
		}
		e.Class = haClassify(domain, e.DeviceClass)
		if e.Class == haOther && !haOtherKept[domain] {
			continue
		}
		if e.Name == "" {
			_, obj, _ := strings.Cut(s.EntityID, ".")
			e.Name = strings.ReplaceAll(obj, "_", " ")
		}
		e.nameToks = haFold(e.Name)
		out.ents = append(out.ents, e)
		if e.Area != "" && areas[strings.ToLower(e.Area)] == nil && len(areas) < haMaxAreas {
			a := &haArea{Name: e.Area, toks: haFold(e.Area)}
			a.group = haRoomGroup[strings.Join(a.toks, " ")]
			areas[strings.ToLower(e.Area)] = a
		}
	}
	for _, a := range areas {
		out.areas = append(out.areas, a)
	}
	sort.Slice(out.areas, func(i, j int) bool { return out.areas[i].Name < out.areas[j].Name })
	return out, nil
}

// ---------------------------------------------------------------------------
// Shared state: client, entity cache and status cache. A config change
// bumps gen, so a fetch that started before it never stores old-host data.

type haStatusView struct {
	Configured bool   `json:"configured"`
	OK         bool   `json:"ok"`
	Connected  bool   `json:"connected"` // = OK (the settings UI reads either)
	Version    string `json:"version"`
	Code       string `json:"code"`
	Entities   int    `json:"entities"`
	Blocked    int    `json:"blocked"`
}

var (
	haMu       sync.Mutex
	haGen      uint64
	haCl       *haClient
	haCached   *haCache
	haStatus   haStatusView
	haStatusAt time.Time
	haStatusG  uint64
	haStatusB  string

	haFetchMu sync.Mutex     // one /api/states refresh at a time
	haProbeMu sync.Mutex     // one status probe at a time
	haKicking atomic.Bool    // a background refresh is running
	haBG      sync.WaitGroup // background refreshes (tests wait for them)
)

// haReset drops the client and every cache (onHAConfigChange).
func haReset() {
	haMu.Lock()
	haGen++
	old := haCl
	haCl, haCached = nil, nil
	haStatus, haStatusAt, haStatusB = haStatusView{}, time.Time{}, ""
	haMu.Unlock()
	old.close()
}

// haClientFor returns the pinned client for the current config.
func haClientFor(ctx context.Context) (*haClient, uint64, error) {
	base, ok := haConfig()
	if !ok {
		return nil, 0, errHANotConfigured
	}
	haMu.Lock()
	gen, c := haGen, haCl
	haMu.Unlock()
	if c != nil && c.base == base && time.Since(c.built) < haClientTTL {
		return c, gen, nil
	}
	c, err := newHAClient(ctx, base)
	if err != nil {
		return nil, gen, err
	}
	haMu.Lock()
	var old *haClient
	if haGen == gen {
		old, haCl = haCl, c
	}
	haMu.Unlock()
	if old != c {
		old.close()
	}
	return c, gen, nil
}

// haCacheNow returns the cached entities for the current config without any
// network call, when not older than maxAge.
func haCacheNow(maxAge time.Duration) *haCache {
	base, ok := haConfig()
	if !ok {
		return nil
	}
	haMu.Lock()
	c := haCached
	haMu.Unlock()
	if c == nil || c.base != base || time.Since(c.at) > maxAge || (c.stale && maxAge <= haCacheTTL) {
		return nil
	}
	return c
}

// haEntities returns entities at most maxAge old, fetching when needed.
func haEntities(ctx context.Context, maxAge time.Duration) (*haCache, error) {
	if c := haCacheNow(maxAge); c != nil {
		return c, nil
	}
	haFetchMu.Lock()
	defer haFetchMu.Unlock()
	if c := haCacheNow(maxAge); c != nil { // someone else just fetched
		return c, nil
	}
	cl, gen, err := haClientFor(ctx)
	if err != nil {
		return nil, err
	}
	c, err := cl.fetchCache(ctx)
	if err != nil {
		haLogFailure("states", err)
		return nil, err
	}
	c.gen = gen
	haMu.Lock()
	if haGen == gen {
		haCached = c
	}
	haMu.Unlock()
	return c, nil
}

// haMatchCache is the cache the fast paths and gates match names against:
// no network call; a stale cache is refreshed in the background.
func haMatchCache() *haCache {
	if !haConfigured() {
		return nil
	}
	c := haCacheNow(haStaleMatch)
	if c == nil || time.Since(c.at) > haCacheTTL {
		haKick()
	}
	return c
}

// haKick refreshes the entity cache in the background (once at a time).
func haKick() {
	if !haKicking.CompareAndSwap(false, true) {
		return
	}
	haBG.Add(1)
	go func() {
		defer haBG.Done()
		defer haKicking.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), haTimeout*haProbeTimeoutMul)
		defer cancel()
		_, _ = haEntities(ctx, haCacheTTL)
	}()
}

// haLogFailure logs a generic code, never a body, URL or token.
func haLogFailure(what string, err error) {
	log.Printf("home: %s failed: %s", what, haCode(err))
}

// haProbe checks the connection: /api/config (version and token), then the
// entity cache. The result is cached for 60 s unless force is set.
func haProbe(ctx context.Context, force bool) haStatusView {
	base, ok := haConfig()
	if !ok {
		return haStatusView{Code: "not_configured"}
	}
	cached := func() (haStatusView, bool) {
		haMu.Lock()
		defer haMu.Unlock()
		if !force && haStatusB == base && haStatusG == haGen && time.Since(haStatusAt) < haCacheTTL {
			return haStatus, true
		}
		return haStatusView{}, false
	}
	if st, ok := cached(); ok {
		return st
	}
	// One probe at a time: polls that arrive meanwhile get its result
	// instead of each sending their own requests to HA.
	haProbeMu.Lock()
	defer haProbeMu.Unlock()
	if st, ok := cached(); ok {
		return st
	}
	st := haStatusView{Configured: true}
	cl, gen, err := haClientFor(ctx)
	if err == nil {
		var cfg struct {
			Version string `json:"version"`
		}
		if err = cl.do(ctx, http.MethodGet, "/api/config", nil, &cfg, haBodyMax); err == nil {
			st.Version = haCleanVersion(cfg.Version)
			maxAge := haCacheTTL
			if force {
				maxAge = 0
			}
			var c *haCache
			if c, err = haEntities(ctx, maxAge); err == nil {
				for _, e := range c.ents {
					if e.Class == haOther {
						continue
					}
					st.Entities++
					if e.Class == haBlocked {
						st.Blocked++
					}
				}
			}
		}
	}
	if err != nil {
		st.Code = haCode(err)
		haLogFailure("status", err)
	} else {
		st.OK, st.Connected, st.Code = true, true, "ok"
	}
	haMu.Lock()
	if haGen == gen {
		haStatus, haStatusAt, haStatusG, haStatusB = st, time.Now(), gen, base
	}
	haMu.Unlock()
	return st
}

var haVersionPattern = regexp.MustCompile(`^[0-9A-Za-z.+\-]{1,32}$`)

func haCleanVersion(v string) string {
	if haVersionPattern.MatchString(v) {
		return v
	}
	return ""
}

// haStatusDetached probes with its own deadline, so a status poll that gives
// up early still leaves a fresh result in the cache for the next poll.
func haStatusDetached(ctx context.Context, force bool) haStatusView {
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), haTimeout*haProbeTimeoutMul)
	defer cancel()
	return haProbe(pctx, force)
}

// ---------------------------------------------------------------------------
// Words. Everything is matched on folded tokens (lower case, ä→ae, ß→ss),
// so typed "oeffne" and spoken "öffne" are the same, in German and English.

func haFold(s string) []string { return tokenize(foldName(s)) }

func haFoldCues(cues []string) []string {
	out := make([]string, len(cues))
	for i, c := range cues {
		out[i] = foldName(c)
	}
	return out
}

// haVerbCues are the HA command verbs (9.0 imperative cues), DE + EN.
var haVerbCues = []string{
	"schalte", "schalt", "schalten", "einschalten", "ausschalten", "anschalten", "abschalten",
	"anmachen", "ausmachen", "aufmachen", "zumachen",
	"mach … an", "mach … aus", "mach … auf", "mach … zu", "mache … an", "mache … aus", "mache … auf", "mache … zu",
	"dreh … auf", "dreh … runter", "dreh … hoch", "dreh … ab", "drehe … auf", "drehe … runter", "drehe … hoch",
	"stell … auf", "stelle … auf", "stell … an", "stell … aus", "stelle … an", "stelle … aus",
	"dimm", "dimme", "dimmen", "öffne", "öffnen", "schließ", "schließe", "schließen",
	"fahr … hoch", "fahr … runter", "fahre … hoch", "fahre … runter", "aktiviere", "aktivier",
	"turn on", "turn off", "turn … on", "turn … off", "turn … up", "turn … down", "turn up", "turn down",
	"switch on", "switch off", "switch … on", "switch … off", "set … to", "dim", "open", "close",
	"raise", "lower", "activate",
}

var haVerbCuesFolded = haFoldCues(haVerbCues)

// haNounDomains maps device nouns to the domain they name.
var haNounDomains = map[string]string{
	"licht": "light", "lichter": "light", "lampe": "light", "lampen": "light", "leuchte": "light",
	"leuchten": "light", "beleuchtung": "light", "light": "light", "lights": "light", "lamp": "light",
	"lamps": "light", "lighting": "light",
	"heizung": "climate", "heizungen": "climate", "thermostat": "climate", "heizkoerper": "climate",
	"klima": "climate", "klimaanlage": "climate", "heating": "climate", "heater": "climate",
	"radiator": "climate", "thermostats": "climate",
	"rollladen": "cover", "rolladen": "cover", "rolllaeden": "cover", "rollaeden": "cover", "rollo": "cover",
	"rollos": "cover", "jalousie": "cover", "jalousien": "cover", "markise": "cover", "blinds": "cover",
	"blind": "cover", "shades": "cover", "shutters": "cover", "shutter": "cover", "awning": "cover",
	"szene": "scene", "scene": "scene",
	"steckdose": "switch", "steckdosen": "switch", "plug": "switch", "socket": "switch", "outlet": "switch",
	"fernseher": "media_player", "tv": "media_player", "musik": "media_player", "lautsprecher": "media_player",
	"radio": "media_player", "television": "media_player", "speaker": "media_player", "music": "media_player",
}

// haNounSuffixes catch German compounds ("Deckenlicht", "Fußbodenheizung").
var haNounSuffixes = []struct{ suf, domain string }{
	{"licht", "light"}, {"lampe", "light"}, {"lampen", "light"}, {"leuchte", "light"},
	{"heizung", "climate"}, {"rollladen", "cover"}, {"rolladen", "cover"}, {"rollo", "cover"},
	{"jalousie", "cover"}, {"steckdose", "switch"}, {"fernseher", "media_player"},
}

// haGateNouns are the router gate phrases (spelled like t.Tokens).
var haGateNouns = []string{
	"licht", "lichter", "lampe", "lampen", "leuchte", "heizung", "thermostat", "rollladen", "rollläden",
	"rollo", "rollos", "jalousie", "jalousien", "szene", "steckdose", "light", "lights", "lamp", "lamps",
	"heating", "blinds", "shades", "scene", "plug",
}

func haNounDomain(tok string) string {
	if d := haNounDomains[tok]; d != "" {
		return d
	}
	for _, s := range haNounSuffixes {
		if len(tok) > len(s.suf)+2 && strings.HasSuffix(tok, s.suf) {
			return s.domain
		}
	}
	return ""
}

func haNouns(toks []string) map[string]bool {
	out := map[string]bool{}
	for _, tok := range toks {
		if d := haNounDomain(tok); d != "" {
			out[d] = true
		}
	}
	return out
}

// haBlockedWords: doors, locks, gates, garages, alarms, scripts and
// automations are refused before anything is sent (SPEC 9.5 step 0).
var haBlockedWords = wordSet(
	"schloss", "schloesser", "tuerschloss", "tuer", "tueren", "haustuer", "haustuere", "garage", "garagen",
	"garagentor", "tor", "tore", "hoftor", "gartentor", "einfahrtstor", "rolltor", "alarm", "alarmanlage",
	"alarmanlagen", "skript", "skripte", "script", "scripts", "automation", "automationen", "automations",
	"automatisierung", "automatisierungen", "lock", "locks", "unlock", "door", "doors", "gate", "gates",
	"garage", "alarms", "verriegeln", "verriegle", "verriegel", "entriegeln", "entriegle", "entriegel",
	"aufschliessen", "abschliessen", "zuschliessen", "aufsperren", "zusperren", "absperren", "sirene",
	"siren", "disarm", "abgeschlossen", "verriegelt", "unlocked", "locked",
)

// haBlockedVerbs are lock/alarm verbs: with a blocked word they are a
// command HERMES refuses (they never trigger anything on their own).
var haBlockedVerbs = wordSet(
	"unlock", "lock", "disarm", "entriegle", "entriegel", "entriegeln", "verriegle", "verriegel",
	"verriegeln", "aufschliessen", "abschliessen", "zuschliessen", "aufsperren", "zusperren", "absperren",
	"sperr", "sperre", "schliess", "schliesse", "scharfschalten", "unscharf",
)

func haBlockedWord(toks []string) bool {
	for _, tok := range toks {
		if haBlockedWords[tok] {
			return true
		}
		for _, suf := range []string{"tuer", "tuere", "tueren", "schloss"} {
			if strings.HasSuffix(tok, suf) && len(tok) > len(suf)+2 {
				return true
			}
		}
		if strings.HasPrefix(tok, "alarm") || strings.HasPrefix(tok, "garage") {
			return true
		}
	}
	return false
}

var (
	haTempWords  = wordSet("warm", "kalt", "temperatur", "heiss", "grad", "cold", "hot", "temperature", "degrees", "warmer", "kaelter")
	haStateWords = wordSet("an", "aus", "ein", "offen", "zu", "geschlossen", "auf", "on", "off", "open", "closed",
		"shut", "running", "laeuft", "brennt", "locked", "unlocked", "abgeschlossen", "verriegelt", "armed",
		"scharf", "eingeschaltet", "ausgeschaltet", "playing")
	haQuestionStarts = wordSet("ist", "sind", "wie", "was", "welche", "welcher", "welches", "brennt", "laeuft",
		"is", "are", "how", "what", "which", "does", "do", "wieviel")
	haPoliteStarts = wordSet("kannst", "koenntest", "wuerdest", "can", "could", "would", "will")
	haOffWords     = wordSet("aus", "ausschalten", "ausmachen", "abschalten", "abdrehen", "off")
	haOnWords      = wordSet("an", "ein", "einschalten", "anschalten", "anmachen", "on", "aktiviere", "aktivier", "activate")
	haOpenWords    = wordSet("oeffne", "oeffnen", "auf", "aufmachen", "hoch", "rauf", "open", "raise", "up")
	haCloseWords   = wordSet("schliess", "schliesse", "schliessen", "zu", "zumachen", "runter", "close", "lower", "down")
	haDimWords     = wordSet("dimm", "dimme", "dimmen", "dim")
	haFillerWords  = wordSet("hermes", "hey", "hallo", "ok", "okay", "bitte", "mal", "please", "doch", "jetzt", "now")
)

// haRoomGroup maps folded room names (DE and EN) to a shared group, so an
// English "living room" finds the HA area "Wohnzimmer" and vice versa.
var haRoomGroup = func() map[string]string {
	groups := [][]string{
		{"wohnzimmer", "living room", "lounge", "livingroom"},
		{"kueche", "kitchen"},
		{"schlafzimmer", "bedroom"},
		{"bad", "badezimmer", "bathroom"},
		{"flur", "diele", "gang", "hallway", "hall"},
		{"buero", "arbeitszimmer", "office", "study"},
		{"kinderzimmer", "kids room", "nursery", "childrens room"},
		{"esszimmer", "dining room"},
		{"garten", "garden", "yard"},
		{"keller", "basement", "cellar"},
		{"gaestezimmer", "guest room"},
		{"balkon", "balcony"},
		{"terrasse", "terrace", "patio"},
		{"dachboden", "attic"},
		{"waschkueche", "laundry", "laundry room"},
	}
	m := map[string]string{}
	for _, g := range groups {
		for _, n := range g {
			m[n] = g[0]
		}
	}
	return m
}()

// haCoreTokens drops fillers and wake words.
func haCoreTokens(toks []string) []string {
	out := make([]string, 0, len(toks))
	for _, tok := range toks {
		if !haFillerWords[tok] {
			out = append(out, tok)
		}
	}
	return out
}

func haHasVerb(toks []string) bool { return hasAnyCue(toks, haVerbCuesFolded) }

// haQuestionForm: starts with a question word or ends with "?". A polite
// "Kannst du …?" / "Can you …?" is a request, not a question.
func haQuestionForm(query string, core []string) bool {
	if len(core) > 0 && haPoliteStarts[core[0]] {
		return false
	}
	if len(core) > 0 && haQuestionStarts[core[0]] {
		return true
	}
	return strings.HasSuffix(strings.TrimSpace(query), "?")
}

func haStartsWithQuestionWord(core []string) bool {
	return len(core) > 0 && haQuestionStarts[core[0]] && !haPoliteStarts[core[0]]
}

func seqIn(toks, seq []string) bool {
	if len(seq) == 0 {
		return false
	}
	for i := 0; i+len(seq) <= len(toks); i++ {
		if phraseAt(toks, i, seq) {
			return true
		}
	}
	return false
}

// nameIn: the folded name occurs in the folded query; a single-word name of
// at least 4 letters may also start a compound ("Küchenlicht").
func nameIn(toks, name []string) bool {
	if seqIn(toks, name) {
		return true
	}
	if len(name) == 1 && utf8.RuneCountInString(name[0]) >= 4 {
		for _, tok := range toks {
			if len(tok) > len(name[0]) && strings.HasPrefix(tok, name[0]) {
				return true
			}
		}
	}
	return false
}

func haLetters(toks []string) int {
	n := 0
	for _, t := range toks {
		n += utf8.RuneCountInString(t)
	}
	return n
}

// ---------------------------------------------------------------------------
// Mentions and target resolution (no network)

type haMention struct {
	strong  []*haEntity // named by a specific name ("Stehlampe", "Küche Licht")
	generic []*haEntity // named by a bare device noun ("Licht")
	others  []*haEntity // named devices of a domain HERMES does not control (haOtherKept)
	areas   []*haArea
	nouns   map[string]bool
	blocked bool // a blocked word, or a blocked entity was named
}

func (m haMention) named() bool { return len(m.strong) > 0 || len(m.areas) > 0 }

func haGenericName(toks []string) bool {
	return len(toks) == 1 && haNounDomain(toks[0]) != ""
}

// mentions finds everything the folded tokens name. c may be nil.
func (c *haCache) mentions(toks []string) haMention {
	m := haMention{nouns: haNouns(toks), blocked: haBlockedWord(toks)}
	if c == nil {
		return m
	}
	groups := map[string]bool{}
	for phrase, g := range haRoomGroup {
		if hasPhrase(toks, phrase) {
			groups[g] = true
		}
	}
	for _, a := range c.areas {
		if haLetters(a.toks) >= 2 && nameIn(toks, a.toks) || (a.group != "" && groups[a.group]) {
			m.areas = append(m.areas, a)
		}
	}
	for _, e := range c.ents {
		if haLetters(e.nameToks) < 3 || !nameIn(toks, e.nameToks) {
			continue
		}
		if e.Class == haBlocked {
			m.blocked = true
			continue
		}
		if e.Class == haOther {
			m.others = append(m.others, e)
			continue
		}
		if haGenericName(e.nameToks) {
			m.generic = append(m.generic, e)
		} else {
			m.strong = append(m.strong, e)
		}
	}
	return m
}

func haInAreas(e *haEntity, areas []*haArea) bool {
	for _, a := range areas {
		if strings.EqualFold(e.Area, a.Name) {
			return true
		}
	}
	return false
}

// haAreaDomains: which domains an area command means when no device noun
// names one: the domain hint, else what the action implies.
func haAreaDomains(nouns map[string]bool, action string, value float64, tempHint bool) map[string]bool {
	if len(nouns) > 0 {
		return nouns
	}
	switch {
	case action == "set" && !math.IsNaN(value) && value >= haMinTemp && value <= haMaxTemp, tempHint:
		return map[string]bool{"climate": true}
	case action == "dim":
		return map[string]bool{"light": true}
	case action == "open" || action == "close":
		return map[string]bool{"cover": true}
	case action == "play" || action == "pause" || action == "stop":
		return map[string]bool{"media_player": true}
	}
	return map[string]bool{"light": true}
}

func haWantClass(e *haEntity, read bool) bool {
	if read {
		return e.Class == haControl || e.Class == haRead
	}
	return e.Class == haControl
}

type haPlan struct {
	targets   []*haEntity
	areas     []*haArea
	blocked   bool
	other     bool // an entity HERMES may not act on was named
	ambiguous bool
	fromQuery bool // the user's own words name the target (Assist may get them)
	// otherNamed: the user's words also name a device HERMES does not
	// control (haOtherKept). Assist would get the whole sentence and act on
	// it too, so the command is refused instead.
	otherNamed bool
}

// haResolve resolves the query tokens first, then the args. read selects
// readable instead of controllable entities.
func haResolve(c *haCache, toks []string, a homeArgs, read bool) haPlan {
	var p haPlan
	m := c.mentions(toks)
	tm := c.mentions(haFold(a.Target))
	if m.blocked || tm.blocked || haBlockedWord(haFold(a.Device)) {
		p.blocked = true
		return p
	}
	value := a.value()
	tempHint := false
	for _, tok := range toks {
		if haTempWords[tok] {
			tempHint = true
		}
	}
	choose := func(m haMention, hint map[string]bool, tempHint bool) []*haEntity {
		var out []*haEntity
		add := func(e *haEntity) {
			if len(out) < haMaxTargets {
				out = append(out, e)
			}
		}
		strong := m.strong
		if len(hint) > 0 && len(strong) > 0 {
			// "Licht im Wohnzimmer" must not hit a speaker named "Wohnzimmer".
			var fit []*haEntity
			for _, e := range strong {
				if hint[e.Domain] {
					fit = append(fit, e)
				}
			}
			if len(fit) > 0 || len(m.areas) > 0 {
				strong = fit
			}
		}
		if len(strong) > 0 {
			for _, e := range strong {
				if haWantClass(e, read) {
					add(e)
				} else if !read {
					p.other = true
				}
			}
			return out
		}
		if len(m.areas) > 0 {
			doms := hint
			if len(doms) == 0 {
				if read && tempHint {
					doms = map[string]bool{"sensor": true, "climate": true}
				} else if read {
					doms = map[string]bool{"light": true, "switch": true, "cover": true, "climate": true, "media_player": true}
				} else {
					doms = haAreaDomains(nil, a.Action, value, tempHint)
				}
			}
			for _, e := range c.ents {
				if haWantClass(e, read) && doms[e.Domain] && haInAreas(e, m.areas) {
					add(e)
				}
			}
			return out
		}
		if len(m.generic) > 0 {
			for _, e := range m.generic {
				if haWantClass(e, read) {
					add(e)
				}
			}
			return out
		}
		if len(hint) > 0 {
			var cands []*haEntity
			for _, e := range c.ents {
				if haWantClass(e, read) && hint[e.Domain] {
					cands = append(cands, e)
				}
			}
			if len(cands) == 1 {
				return cands
			}
			if len(cands) > 1 {
				p.ambiguous = true
			}
		}
		return out
	}
	if c == nil {
		return p
	}
	if m.named() || len(m.generic) > 0 || len(m.nouns) > 0 {
		p.targets = choose(m, m.nouns, tempHint)
		p.areas = m.areas
		if len(p.targets) > 0 {
			p.fromQuery = true
			p.ambiguous = false
			p.otherNamed = !read && len(m.others) > 0
			return p
		}
	}
	if !read && len(m.others) > 0 {
		p.other = true
	}
	if p.other {
		return p
	}
	if a.Target != "" || a.Device != "" {
		hint := haNouns(haFold(a.Device))
		if d := haDeviceDomain(a.Device); d != "" {
			hint = map[string]bool{d: true}
		}
		for k := range tm.nouns {
			hint[k] = true
		}
		// The args alone decide here (a confirmed offer: the query is "ja").
		p.ambiguous = false
		p.targets = choose(tm, hint, false)
		p.areas = tm.areas
		if !read && len(p.targets) == 0 && len(tm.others) > 0 {
			p.other = true
		}
	}
	return p
}

// ---------------------------------------------------------------------------
// Arguments

type homeArgs struct {
	Target string          `json:"target,omitempty"`
	Action string          `json:"action,omitempty"` // on|off|set|dim|open|close|play|pause|stop|query
	Value  json.RawMessage `json:"value,omitempty"`
	Device string          `json:"device,omitempty"` // light|heating|blinds|switch|scene|media
	What   string          `json:"what,omitempty"`   // query: state|temperature
}

var haActions = wordSet("on", "off", "set", "dim", "open", "close", "play", "pause", "stop", "query")

func parseHomeArgs(raw json.RawMessage) homeArgs {
	var a homeArgs
	_ = json.Unmarshal(raw, &a)
	a.Target = cleanText(strings.Join(strings.Fields(a.Target), " "), haTargetRunes)
	a.Action = strings.ToLower(strings.TrimSpace(a.Action))
	if !haActions[a.Action] {
		a.Action = ""
	}
	a.Device = strings.ToLower(cleanText(a.Device, 20))
	if a.What != "temperature" {
		a.What = "state"
	}
	return a
}

// value is the numeric value (number or numeric string), NaN when absent.
func (a homeArgs) value() float64 {
	raw := bytes.TrimSpace(a.Value)
	if len(raw) == 0 || string(raw) == "null" {
		return math.NaN()
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil && !math.IsInf(f, 0) {
		return f
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if f, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(s), ",", "."), 64); err == nil && !math.IsInf(f, 0) {
			return f
		}
	}
	return math.NaN()
}

var haDeviceDomains = map[string]string{
	"light": "light", "heating": "climate", "climate": "climate", "blinds": "cover", "cover": "cover",
	"switch": "switch", "scene": "scene", "media": "media_player", "media_player": "media_player",
}

func haDeviceDomain(d string) string { return haDeviceDomains[d] }

var haDomainDevice = map[string]string{
	"light": "light", "climate": "heating", "cover": "blinds", "switch": "switch", "scene": "scene", "media_player": "media",
}

var (
	haTempPattern    = regexp.MustCompile(`(\d{1,2}(?:[.,]\d)?)\s*(?:°|grad\b|degrees?\b|celsius\b)`)
	haPercentPattern = regexp.MustCompile(`(\d{1,3})\s*(?:%|prozent\b|percent\b)`)
	haToNumPattern   = regexp.MustCompile(`\b(?:auf|to)\s+(\d{1,3}(?:[.,]\d)?)\b`)
)

func haParseNum(s string) float64 {
	f, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
	if err != nil {
		return math.NaN()
	}
	return f
}

// haActionOf derives action and value from the user's words.
func haActionOf(toks []string, query string) (string, float64) {
	q := strings.ToLower(query)
	if m := haTempPattern.FindStringSubmatch(q); m != nil {
		return "set", haParseNum(m[1])
	}
	if m := haPercentPattern.FindStringSubmatch(q); m != nil {
		return "dim", haParseNum(m[1])
	}
	dim := hasAnyWord(toks, haDimWords)
	if m := haToNumPattern.FindStringSubmatch(q); m != nil {
		if dim {
			return "dim", haParseNum(m[1])
		}
		return "set", haParseNum(m[1])
	}
	switch {
	case dim:
		return "dim", math.NaN()
	case hasAnyWord(toks, haOffWords):
		return "off", math.NaN()
	case hasAnyWord(toks, haOnWords):
		return "on", math.NaN()
	case hasAnyWord(toks, haCloseWords):
		return "close", math.NaN()
	case hasAnyWord(toks, haOpenWords):
		return "open", math.NaN()
	}
	return "", math.NaN()
}

func haArgsJSON(a homeArgs, value float64) json.RawMessage {
	if !math.IsNaN(value) {
		a.Value = json.RawMessage(strconv.FormatFloat(value, 'f', -1, 64))
	}
	b, _ := json.Marshal(a)
	return b
}

// haLabel names the plan's primary target for args and replies.
func haLabel(m haMention) string {
	switch {
	case len(m.strong) > 0:
		return m.strong[0].Name
	case len(m.areas) > 0:
		return m.areas[0].Name
	case len(m.generic) > 0:
		return m.generic[0].Name
	}
	return ""
}

func haDeviceOf(nouns map[string]bool) string {
	if len(nouns) == 1 {
		for d := range nouns {
			return haDomainDevice[d]
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Settings and access

func homeControlMode(t *Turn) string {
	if t.Settings == nil {
		return "known"
	}
	return t.Settings.HomeControl
}

func homeQueryAccess(t *Turn, _ json.RawMessage) Access {
	if t.Settings != nil && t.Settings.HomeQueries == "everyone" {
		return AccessOpen
	}
	return AccessRead
}

func homeControlAccess(t *Turn, raw json.RawMessage) Access {
	if parseHomeArgs(raw).Action == "query" {
		return homeQueryAccess(t, raw)
	}
	if homeControlMode(t) == "everyone" {
		return AccessOpen
	}
	return AccessWrite
}

func homeControlEnabled(t *Turn) bool { return homeControlMode(t) != "off" && haConfigured() }
func homeQueryEnabled(*Turn) bool     { return haConfigured() }

// ---------------------------------------------------------------------------
// Fast paths

// matchHomeControl (order 80): an HA verb plus a cached entity or area
// name, an unambiguous device noun, or a blocked word (refused in Run
// without any request). Questions are left to home_query.
func matchHomeControl(t *Turn) (json.RawMessage, bool) {
	if homeControlMode(t) == "off" || !haConfigured() {
		return nil, false
	}
	toks := haFold(t.Query)
	core := haCoreTokens(toks)
	if len(core) == 0 || len(core) > 30 || haStartsWithQuestionWord(core) {
		return nil, false
	}
	if !haHasVerb(toks) {
		// "unlock the front door", "Entriegle die Haustür", "disarm the alarm
		// system": no switching verb, but a lock/alarm verb on a blocked
		// target is refused here rather than left to the router or chat.
		if hasAnyWord(toks, haBlockedVerbs) && haBlockedWord(toks) {
			return json.RawMessage(`{}`), true
		}
		// The short form "Küchenlicht und Robi ein" / "kitchen light on": a
		// trailing on/off and a named device. Left to the router or chat, it
		// was "switched" by the persona or reduced to its allowed half.
		if args, ok := haParticleCommand(t, core); ok {
			return args, true
		}
		return nil, false
	}
	action, value := haActionOf(toks, t.Query)
	if haBlockedWord(toks) { // refused in Run; not even the entity list is fetched
		return haArgsJSON(homeArgs{Action: action}, value), true
	}
	c := haMatchCache()
	if c == nil {
		return nil, false
	}
	m := c.mentions(toks)
	if m.blocked {
		return haArgsJSON(homeArgs{Action: action}, value), true
	}
	a := homeArgs{Target: haLabel(m), Action: action, Device: haDeviceOf(m.nouns)}
	if m.named() || len(m.generic) > 0 || len(m.others) > 0 {
		return haArgsJSON(a, value), true
	}
	if p := haResolve(c, toks, homeArgs{Action: action}, false); len(p.targets) == 1 {
		a.Target = p.targets[0].Name
		return haArgsJSON(a, value), true
	}
	return nil, false
}

// haParticleCommand reads a verbless command that ends in a switching
// particle ("Küchenlicht an", "Robi aus", "kitchen light on") and names a
// known device or room. Blocked or uncontrolled devices are taken too, so
// home_control refuses the whole sentence.
func haParticleCommand(t *Turn, core []string) (json.RawMessage, bool) {
	if !haParticleForm(core) {
		return nil, false
	}
	action := "on"
	if haOffWords[core[len(core)-1]] {
		action = "off"
	}
	c := haMatchCache()
	if c == nil {
		return nil, false
	}
	m := c.mentions(core)
	if m.blocked || len(m.others) > 0 {
		return haArgsJSON(homeArgs{Action: action}, math.NaN()), true
	}
	if !m.named() {
		return nil, false
	}
	return haArgsJSON(homeArgs{Target: haLabel(m), Action: action, Device: haDeviceOf(m.nouns)}, math.NaN()), true
}

// haParticleForm: a short verbless command ending in on/off ("Licht aus").
func haParticleForm(core []string) bool {
	if len(core) < 2 || len(core) > 8 {
		return false
	}
	last := core[len(core)-1]
	return haOnWords[last] || haOffWords[last]
}

// haRefuseSentence refuses a home_control call from the router when the
// user's own sentence names a blocked or uncontrolled device. The router
// only passes one target, and after a confirming "ja" the sentence is gone,
// so "Küchenlicht und Robi ein" would switch the light and drop the robot.
func haRefuseSentence(t *Turn) (Reply, bool) {
	toks := haFold(t.Query)
	if haBlockedWord(toks) {
		return haRefuseBlocked(t, false), true
	}
	c := haMatchCache()
	if c == nil {
		return Reply{}, false
	}
	m := c.mentions(toks)
	switch {
	case m.blocked:
		return haRefuseBlocked(t, false), true
	case len(m.others) > 0:
		return haRefuseOther(t), true
	}
	return Reply{}, false
}

// matchHomeQuery (order 81): a question about a named device or room
// ("Ist das Licht in der Küche an?", "How warm is it in the living room?").
func matchHomeQuery(t *Turn) (json.RawMessage, bool) {
	if !haConfigured() {
		return nil, false
	}
	toks := haFold(t.Query)
	core := haCoreTokens(toks)
	if len(core) == 0 || len(core) > 30 || !haQuestionForm(t.Query, core) || haHasVerb(toks) {
		return nil, false
	}
	temp := hasAnyWord(toks, haTempWords)
	state := hasAnyWord(toks, haStateWords)
	if haBlockedWord(toks) { // "Ist die Haustür zu?": refused in Run, no request
		return json.RawMessage(`{"action":"query"}`), state
	}
	c := haMatchCache()
	if c == nil {
		return nil, false
	}
	m := c.mentions(toks)
	if m.blocked {
		return json.RawMessage(`{"action":"query"}`), state
	}
	a := homeArgs{Target: haLabel(m), Device: haDeviceOf(m.nouns), What: "state"}
	if temp {
		a.What = "temperature"
	}
	switch {
	case m.named() && (temp || state || len(m.nouns) > 0 || len(m.strong) > 0):
	case len(m.generic) > 0 && state:
	case len(m.nouns) > 0 && state:
		if p := haResolve(c, toks, a, true); len(p.targets) != 1 {
			return nil, false
		}
	default:
		return nil, false
	}
	b, _ := json.Marshal(a)
	return b, true
}

// matchHomeSetup (order 82): HA verb plus device noun while smart home is
// not set up, or switched off in the settings: say so instead of chatting.
func matchHomeSetup(t *Turn) (json.RawMessage, bool) {
	toks := haFold(t.Query)
	core := haCoreTokens(toks)
	if len(core) == 0 || len(core) > 12 || haStartsWithQuestionWord(core) || (!haHasVerb(toks) && !haParticleForm(core)) {
		return nil, false
	}
	configured := haConfigured()
	if configured && homeControlMode(t) != "off" {
		return nil, false
	}
	if len(haNouns(toks)) > 0 {
		return nil, true
	}
	if configured {
		if c := haMatchCache(); c != nil && c.mentions(toks).named() {
			return nil, true
		}
	}
	return nil, false
}

// haGateFunc opens the router for device nouns (compounds included) and
// cached entity or area names.
func haGateFunc(t *Turn) bool {
	if !haConfigured() {
		return false
	}
	toks := haFold(t.Query)
	if len(haNouns(toks)) > 0 {
		return true
	}
	c := haMatchCache()
	if c == nil {
		return false
	}
	m := c.mentions(toks)
	return m.named() || len(m.generic) > 0
}

// ---------------------------------------------------------------------------
// Replies (DE + EN)

func haFailReply(t *Turn, err error) Reply {
	var en, de string
	switch haCode(err) {
	case "not_configured":
		return Reply{Text: haSetupText(t.Lang)}
	case "auth":
		en, de = "The Home Assistant access is invalid. Please renew the token in the settings.",
			"Der Home-Assistant-Zugang ist ungültig. Bitte das Token in den Einstellungen erneuern."
	case "timeout":
		en, de = "Home Assistant took too long. Try again in a moment.",
			"Home Assistant hat zu lange gebraucht. Versuch es gleich noch einmal."
	case "unreachable":
		en, de = "Home Assistant is not responding right now.", "Home Assistant antwortet gerade nicht."
	case "not_allowed":
		en, de = "I'm not allowed to do that in Home Assistant.", "Das darf ich in Home Assistant nicht."
	default:
		en, de = "Home Assistant sent an unexpected answer.", "Home Assistant hat unerwartet geantwortet."
	}
	return Reply{Text: pick(t.Lang, en, de), Error: true}
}

func haSetupText(lang string) string {
	return pick(lang,
		"Smart home is not set up. Enter the Home Assistant address and a token under Settings → Smart Home.",
		"Smart Home ist nicht eingerichtet. Trage unter Einstellungen → Smart Home die Home-Assistant-Adresse und ein Token ein.")
}

func haNotMatched(t *Turn) Reply {
	return Reply{Text: pick(t.Lang,
		"Home Assistant could not match that. Check that the device is exposed to voice assistants in Home Assistant.",
		"Das konnte Home Assistant nicht zuordnen. Prüfe, ob das Gerät in Home Assistant für Sprachassistenten freigegeben ist.")}
}

func haAmbiguous(t *Turn) Reply {
	return Reply{Text: pick(t.Lang,
		"Which device do you mean? Please name the room or the device.",
		"Welches Gerät meinst du? Nenn bitte den Raum oder den Namen.")}
}

// haRefuseBlocked is the safety refusal; it counts as a refusal (no
// "tool ok" flash) and nothing was sent to HA.
func haRefuseBlocked(t *Turn, query bool) Reply {
	t.refused = true
	if query {
		return Reply{Text: pick(t.Lang,
			"For safety reasons I don't give information about doors, locks, gates or alarm systems.",
			"Über Türen, Schlösser, Tore und Alarmanlagen gebe ich aus Sicherheitsgründen keine Auskunft.")}
	}
	return Reply{Text: pick(t.Lang,
		"For safety reasons I don't control doors, locks, gates or alarm systems.",
		"Türen, Schlösser, Tore und Alarmanlagen steuere ich aus Sicherheitsgründen nicht.")}
}

func haRefuseOther(t *Turn) Reply {
	t.refused = true
	return Reply{Text: pick(t.Lang,
		"I don't control that device. I can switch lights, switches, scenes, heating, blinds and media players.",
		"Dieses Gerät steuere ich nicht. Ich kann Licht, Schalter, Szenen, Heizung, Rollläden und Mediaplayer steuern.")}
}

// haNum formats a number for speech: one decimal at most, German comma.
func haNum(v float64, lang string) string {
	s := strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64)
	if lang == "de" {
		s = strings.ReplaceAll(s, ".", ",")
	}
	return s
}

func haNames(ents []*haEntity, max int) []string {
	var out []string
	for _, e := range ents {
		if len(out) == max {
			out = append(out, "…")
			break
		}
		if n := safeTitle(e.Name); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// haDoneText is the template reply after a service call.
func haDoneText(names, action string, value float64, domain, lang string) string {
	v := haNum(value, lang)
	if (action == "set" || action == "dim") && math.IsNaN(value) {
		action = "on" // no value: the service only switched it on (haServiceFor)
	}
	switch action {
	case "on":
		return pick(lang, "Turned on "+names+".", names+" eingeschaltet.")
	case "off":
		return pick(lang, "Turned off "+names+".", names+" ausgeschaltet.")
	case "set":
		switch domain {
		case "climate":
			return pick(lang, "Set "+names+" to "+v+" degrees.", names+" auf "+v+" Grad gestellt.")
		case "media_player":
			return pick(lang, "Set the volume of "+names+" to "+v+" percent.", "Lautstärke von "+names+" auf "+v+" Prozent gestellt.")
		}
		return pick(lang, "Set "+names+" to "+v+" percent.", names+" auf "+v+" Prozent gestellt.")
	case "dim":
		return pick(lang, "Dimmed "+names+" to "+v+" percent.", names+" auf "+v+" Prozent gedimmt.")
	case "open":
		return pick(lang, "Opened "+names+".", names+" geöffnet.")
	case "close":
		return pick(lang, "Closed "+names+".", names+" geschlossen.")
	case "play":
		return pick(lang, "Playing on "+names+".", names+" spielt.")
	case "pause":
		return pick(lang, "Paused "+names+".", names+" pausiert.")
	case "stop":
		return pick(lang, "Stopped "+names+".", names+" gestoppt.")
	}
	return pick(lang, "Done.", "Erledigt.")
}

// ---------------------------------------------------------------------------
// Service fallback

type haCall struct {
	domain, service string
	data            map[string]any
}

// haServiceFor maps an action on one domain to a whitelisted call, or
// returns ok=false when it does not apply.
func haServiceFor(domain, action string, value float64) (service string, extra map[string]any, ok bool) {
	has := !math.IsNaN(value)
	switch domain {
	case "light":
		switch action {
		case "on", "open":
			return "turn_on", nil, true
		case "off", "close":
			return "turn_off", nil, true
		case "set", "dim":
			if !has {
				return "turn_on", nil, true
			}
			if value < 1 || value > 100 {
				return "", nil, false
			}
			return "turn_on", map[string]any{"brightness_pct": math.Round(value)}, true
		}
	case "switch":
		switch action {
		case "on", "open":
			return "turn_on", nil, true
		case "off", "close":
			return "turn_off", nil, true
		}
	case "climate":
		switch action {
		case "set", "dim":
			if !has || value < haMinTemp || value > haMaxTemp {
				return "", nil, false
			}
			return "set_temperature", map[string]any{"temperature": math.Round(value*2) / 2}, true
		case "on", "open":
			return "set_hvac_mode", map[string]any{"hvac_mode": "heat"}, true
		case "off", "close":
			return "set_hvac_mode", map[string]any{"hvac_mode": "off"}, true
		}
	case "cover":
		switch action {
		case "open", "on":
			return "open_cover", nil, true
		case "close", "off":
			return "close_cover", nil, true
		}
	case "scene":
		if action == "on" || action == "set" || action == "open" {
			return "turn_on", nil, true
		}
	case "media_player":
		switch action {
		case "on":
			return "turn_on", nil, true
		case "off":
			return "turn_off", nil, true
		case "play":
			return "media_play", nil, true
		case "pause":
			return "media_pause", nil, true
		case "stop":
			return "media_stop", nil, true
		case "set", "dim":
			if !has || value < 0 || value > 100 {
				return "", nil, false
			}
			return "volume_set", map[string]any{"volume_level": value / 100}, true
		}
	}
	return "", nil, false
}

// haPlanCalls groups the targets per domain into whitelisted calls.
func haPlanCalls(targets []*haEntity, action string, value float64) (calls []haCall, used []*haEntity, badValue bool) {
	byDomain := map[string][]*haEntity{}
	var order []string
	for _, e := range targets {
		if e.Class != haControl {
			continue
		}
		if byDomain[e.Domain] == nil {
			order = append(order, e.Domain)
		}
		byDomain[e.Domain] = append(byDomain[e.Domain], e)
	}
	for _, d := range order {
		svc, extra, ok := haServiceFor(d, action, value)
		if !ok {
			if action == "set" || action == "dim" {
				badValue = true
			}
			continue
		}
		data := map[string]any{}
		for k, v := range extra {
			data[k] = v
		}
		var ids []string
		for _, e := range byDomain[d] {
			ids = append(ids, e.ID)
			used = append(used, e)
		}
		data["entity_id"] = ids
		calls = append(calls, haCall{d, svc, data})
	}
	return calls, used, badValue
}

func haCtx(t *Turn) context.Context {
	if t.Ctx != nil {
		return t.Ctx
	}
	return context.Background()
}

// haAssistLang: the haLanguage setting, else the voice language.
func haAssistLang(t *Turn) string {
	if t.Settings != nil && t.Settings.HALanguage != "" {
		return t.Settings.HALanguage
	}
	if t.Lang == "de" {
		return "de"
	}
	return "en"
}

func haSameLang(assist, lang string) bool {
	base, _, _ := strings.Cut(assist, "-")
	return strings.EqualFold(base, lang)
}

// haStripWake removes the wake word and polite fillers that make Assist's
// sentence templates miss ("Hermes, mach bitte mal das Licht an").
func haStripWake(s string) string {
	words := strings.Fields(s)
	core := func(w string) string { return foldName(strings.Trim(w, ",.!?:;\"'„“”")) }
	i := 0
	if len(words) > 1 && (core(words[0]) == "hey" || core(words[0]) == "hallo" || core(words[0]) == "ok" ||
		core(words[0]) == "okay" || core(words[0]) == "hi") && core(words[1]) == "hermes" {
		i = 2
	} else if len(words) > 0 && core(words[0]) == "hermes" {
		i = 1
	}
	words = words[i:]
	if len(words) > 1 {
		w0, w1 := core(words[0]), core(words[1])
		if (w0 == "kannst" || w0 == "koenntest" || w0 == "wuerdest") && w1 == "du" ||
			(w0 == "can" || w0 == "could" || w0 == "would") && w1 == "you" {
			words = words[2:]
		}
	}
	out := make([]string, 0, len(words))
	for j, w := range words {
		c := core(w)
		if c == "bitte" || c == "mal" || c == "please" || (c == "hermes" && j == len(words)-1) {
			continue
		}
		out = append(out, w)
	}
	res := strings.TrimLeft(strings.Join(out, " "), " ,.!:;")
	res = strings.TrimRight(res, " ,?!.;:")
	if r, size := utf8.DecodeRuneInString(res); size > 0 && r < utf8.RuneSelf && r >= 'a' && r <= 'z' {
		res = strings.ToUpper(res[:1]) + res[1:]
	}
	return res
}

// ---------------------------------------------------------------------------
// home_control

func runHomeControl(t *Turn, raw json.RawMessage) Reply {
	a := parseHomeArgs(raw)
	if a.Action == "query" {
		return runHomeQuery(t, raw)
	}
	toks := haFold(t.Query)
	// Step 0a: blocked words need no entity list and no request at all.
	if haBlockedWord(toks) || haBlockedWord(haFold(a.Target)) {
		return haRefuseBlocked(t, false)
	}
	ctx := haCtx(t)
	c, err := haEntities(ctx, haCacheTTL)
	if err != nil {
		return haFailReply(t, err)
	}
	// Step 0b: pre-classify against the full entity list.
	p := haResolve(c, toks, a, false)
	switch {
	case p.blocked:
		return haRefuseBlocked(t, false)
	case p.otherNamed, p.other && len(p.targets) == 0:
		return haRefuseOther(t)
	case len(p.targets) == 0 && p.ambiguous:
		return haAmbiguous(t)
	case len(p.targets) == 0:
		return haNotMatched(t)
	}
	action, value := a.Action, a.value()
	if p.fromQuery {
		if qa, qv := haActionOf(toks, t.Query); qa != "" {
			action, value = qa, qv
		}
	}
	cl, _, err := haClientFor(ctx)
	if err != nil {
		return haFailReply(t, err)
	}
	// Step 1: Assist, only with the user's own words for an allowed target.
	if p.fromQuery {
		text := cleanText(haStripWake(t.Query), haQueryRunes)
		lang := haAssistLang(t)
		res, err := cl.process(ctx, text, lang)
		if err != nil {
			haLogFailure("assist", err)
			return haFailReply(t, err)
		}
		haCheckAssistTargets(c, res.Targets)
		switch res.Type {
		case "action_done":
			speech := res.Speech
			if speech == "" || !haSameLang(lang, t.Lang) {
				speech = haDoneText(joinAnd(haNames(p.targets, 5), t.Lang), action, value, p.targets[0].Domain, t.Lang)
			}
			return Reply{Text: speech, Card: haCard(action, p.targets, "assist")}
		case "query_answer":
			if !t.Can(homeQueryAccess(t, nil)) {
				return refuse(t, "home_query", haArgsJSON(homeArgs{Target: a.Target, Action: "query"}, math.NaN()))
			}
			if res.Speech != "" && haSameLang(lang, t.Lang) {
				return Reply{Text: res.Speech, Card: haCard("query", p.targets, "assist")}
			}
			return haAnswer(t, p, "state", false)
		}
		// error (no_intent_match, no_valid_targets, …): try the services.
	}
	// Step 2: whitelisted services for structured args.
	if action == "" {
		return haNotMatched(t)
	}
	calls, used, badValue := haPlanCalls(p.targets, action, value)
	if len(calls) == 0 {
		if badValue && p.targets[0].Domain == "climate" {
			return Reply{Text: pick(t.Lang, "The temperature must be between 5 and 30 degrees.",
				"Die Temperatur muss zwischen 5 und 30 Grad liegen.")}
		}
		if badValue {
			return Reply{Text: pick(t.Lang, "That value is out of range.", "Dieser Wert liegt außerhalb des erlaubten Bereichs.")}
		}
		return haNotMatched(t)
	}
	for _, call := range calls {
		if err := cl.callService(ctx, call.domain, call.service, call.data); err != nil {
			haLogFailure("service "+call.domain+"."+call.service, err)
			return haFailReply(t, err)
		}
	}
	haInvalidateStates()
	return Reply{
		Text: haDoneText(joinAnd(haNames(used, 5), t.Lang), action, value, used[0].Domain, t.Lang),
		Card: haCard(action, used, "service"),
	}
}

// haInvalidateStates marks the states as changed after a command, so the
// next question or command reads fresh states; name matching keeps using the
// list without a background refresh.
func haInvalidateStates() {
	haMu.Lock()
	if haCached != nil {
		cp := *haCached
		cp.stale = true
		haCached = &cp
	}
	haMu.Unlock()
}

// haCheckAssistTargets warns when Assist acted on an entity HERMES blocks:
// it is exposed to Assist in HA, which docs/smart-home.md says not to do.
func haCheckAssistTargets(c *haCache, ids []string) {
	for _, id := range ids {
		for _, e := range c.ents {
			if e.ID == id && e.Class == haBlocked {
				log.Printf("home: Assist acted on a blocked entity (%s); expose only lights, switches, climate and blinds to Assist", e.Domain)
			}
		}
	}
}

func haCard(action string, ents []*haEntity, via string) *Card {
	return &Card{Kind: "home", Data: map[string]any{"action": action, "targets": haNames(ents, 5), "via": via}}
}

// homeOffer: "Soll ich die Heizung im Wohnzimmer auf 22 Grad stellen?"
func homeOffer(t *Turn, raw json.RawMessage) string {
	a := parseHomeArgs(raw)
	v := a.value()
	c := haCacheNow(haStaleMatch)
	target := safeTitle(a.Target)
	what := target
	if c != nil && target != "" {
		tm := c.mentions(haFold(target))
		if len(tm.strong) == 0 && len(tm.areas) > 0 {
			doms := haAreaDomains(haNounsOrDevice(a.Device), a.Action, v, false)
			what = haAreaPhrase(tm.areas[0].Name, doms, t.Lang)
		}
	}
	if what == "" {
		what = pick(t.Lang, "the device", "das Gerät")
	}
	n := haNum(v, t.Lang)
	has := !math.IsNaN(v)
	switch a.Action {
	case "set":
		if has && v >= haMinTemp && v <= haMaxTemp && a.Device != "light" && a.Device != "media" {
			return pick(t.Lang, "Should I set "+what+" to "+n+" degrees?", "Soll ich "+what+" auf "+n+" Grad stellen?")
		}
		if has {
			return pick(t.Lang, "Should I set "+what+" to "+n+" percent?", "Soll ich "+what+" auf "+n+" Prozent stellen?")
		}
		return pick(t.Lang, "Should I turn on "+what+"?", "Soll ich "+what+" einschalten?")
	case "dim":
		if has {
			return pick(t.Lang, "Should I dim "+what+" to "+n+" percent?", "Soll ich "+what+" auf "+n+" Prozent dimmen?")
		}
		return pick(t.Lang, "Should I dim "+what+"?", "Soll ich "+what+" dimmen?")
	case "off":
		return pick(t.Lang, "Should I turn off "+what+"?", "Soll ich "+what+" ausschalten?")
	case "open":
		return pick(t.Lang, "Should I open "+what+"?", "Soll ich "+what+" öffnen?")
	case "close":
		return pick(t.Lang, "Should I close "+what+"?", "Soll ich "+what+" schließen?")
	case "play", "pause", "stop":
		verbs := map[string][2]string{"play": {"start playback on", "abspielen"}, "pause": {"pause", "pausieren"}, "stop": {"stop", "stoppen"}}[a.Action]
		return pick(t.Lang, "Should I "+verbs[0]+" "+what+"?", "Soll ich "+what+" "+verbs[1]+"?")
	}
	return pick(t.Lang, "Should I turn on "+what+"?", "Soll ich "+what+" einschalten?")
}

func haNounsOrDevice(device string) map[string]bool {
	if d := haDeviceDomain(device); d != "" {
		return map[string]bool{d: true}
	}
	return nil
}

// haAreaPhrase: "die Heizung im Wohnzimmer", "das Licht in der Küche",
// "the heating in Wohnzimmer".
func haAreaPhrase(area string, doms map[string]bool, lang string) string {
	area = safeTitle(area)
	de, en := "die Geräte", "the devices"
	switch {
	case doms["climate"]:
		de, en = "die Heizung", "the heating"
	case doms["light"]:
		de, en = "das Licht", "the lights"
	case doms["cover"]:
		de, en = "die Rollläden", "the blinds"
	case doms["media_player"]:
		de, en = "die Wiedergabe", "the media player"
	}
	if lang != "de" {
		return en + " in " + area
	}
	f := foldName(area)
	if strings.HasSuffix(f, "e") || strings.HasSuffix(f, "ung") || strings.HasSuffix(f, "ei") {
		return de + " in der " + area
	}
	return de + " im " + area
}

// ---------------------------------------------------------------------------
// home_query: answered from the entity states (no Assist needed)

func runHomeQuery(t *Turn, raw json.RawMessage) Reply {
	a := parseHomeArgs(raw)
	toks := haFold(t.Query)
	if haBlockedWord(toks) || haBlockedWord(haFold(a.Target)) {
		return haRefuseBlocked(t, true)
	}
	c, err := haEntities(haCtx(t), haQueryMaxAge)
	if err != nil {
		return haFailReply(t, err)
	}
	q := a
	q.Action = ""
	p := haResolve(c, toks, q, true)
	switch {
	case p.blocked:
		return haRefuseBlocked(t, true)
	case len(p.targets) == 0 && p.ambiguous:
		return haAmbiguous(t)
	case len(p.targets) == 0:
		return haNotMatched(t)
	}
	what := a.What
	if hasAnyWord(toks, haTempWords) {
		what = "temperature"
	}
	return haAnswer(t, p, what, true)
}

// haAnswer builds the spoken answer about the plan's targets.
func haAnswer(t *Turn, p haPlan, what string, card bool) Reply {
	lang := t.Lang
	var text string
	area := ""
	if len(p.areas) > 0 {
		area = safeTitle(p.areas[0].Name)
	}
	if what == "temperature" {
		if v, ok := haTemperature(p.targets); ok {
			n := haNum(v, lang)
			if area != "" {
				text = pick(lang, "It's "+n+" degrees in "+area+".", haInArea(area)+" sind es "+n+" Grad.")
			} else {
				name := safeTitle(p.targets[0].Name)
				text = pick(lang, name+": "+n+" degrees.", name+": "+n+" Grad.")
			}
		}
	}
	if text == "" {
		text = haStateText(p, area, lang)
	}
	r := Reply{Text: text}
	if card {
		r.Card = haCard("query", p.targets, "states")
	}
	return r
}

func haInArea(area string) string {
	f := foldName(area)
	if strings.HasSuffix(f, "e") || strings.HasSuffix(f, "ung") || strings.HasSuffix(f, "ei") {
		return "In der " + area
	}
	return "Im " + area
}

// haTemperature: a temperature sensor first, else a thermostat's reading.
func haTemperature(ents []*haEntity) (float64, bool) {
	for _, e := range ents {
		if e.Domain == "sensor" && (e.DeviceClass == "temperature" || e.Unit == "°C" || e.Unit == "°F") {
			if f, err := strconv.ParseFloat(e.State, 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
				return f, true
			}
		}
	}
	for _, e := range ents {
		if e.Domain == "climate" && !math.IsNaN(e.CurTemp) {
			return e.CurTemp, true
		}
	}
	return 0, false
}

func haOnLike(e *haEntity) bool {
	switch e.State {
	case "on", "open", "opening", "playing", "heat", "cool", "auto", "heat_cool", "dry", "fan_only", "idle", "paused":
		return e.Domain != "media_player" || e.State != "idle"
	}
	return false
}

// haStateWord is one entity's state in words.
func haStateWord(e *haEntity, lang string) string {
	s := e.State
	if s == "unavailable" || s == "unknown" || s == "" {
		return pick(lang, "unavailable right now", "gerade nicht erreichbar")
	}
	switch e.Domain {
	case "cover":
		m := map[string][2]string{"open": {"open", "offen"}, "closed": {"closed", "geschlossen"},
			"opening": {"opening", "öffnet gerade"}, "closing": {"closing", "schließt gerade"}}
		if w, ok := m[s]; ok {
			return pick(lang, w[0], w[1])
		}
	case "climate":
		if s == "off" {
			return pick(lang, "off", "aus")
		}
		w := pick(lang, "on", "an")
		if !math.IsNaN(e.SetTemp) {
			w += pick(lang, ", set to "+haNum(e.SetTemp, lang)+" degrees", ", eingestellt auf "+haNum(e.SetTemp, lang)+" Grad")
		}
		return w
	case "media_player":
		m := map[string][2]string{"playing": {"playing", "spielt gerade"}, "paused": {"paused", "pausiert"},
			"off": {"off", "aus"}, "on": {"on", "an"}, "idle": {"on", "an"}, "standby": {"off", "aus"}}
		if w, ok := m[s]; ok {
			return pick(lang, w[0], w[1])
		}
	case "binary_sensor":
		if e.DeviceClass == "window" {
			return pick(lang, map[bool]string{true: "open", false: "closed"}[s == "on"], map[bool]string{true: "offen", false: "geschlossen"}[s == "on"])
		}
	case "sensor":
		v := safeTitle(s)
		if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			v = haNum(f, lang)
		}
		if e.Unit != "" {
			v += " " + safeTitle(e.Unit)
		}
		return v
	}
	switch s {
	case "on":
		return pick(lang, "on", "an")
	case "off":
		return pick(lang, "off", "aus")
	}
	return safeTitle(s)
}

func haStateText(p haPlan, area, lang string) string {
	ents := p.targets
	if len(ents) == 1 {
		e := ents[0]
		name := safeTitle(e.Name)
		if e.Domain == "sensor" {
			return name + ": " + haStateWord(e, lang) + "."
		}
		return pick(lang, name+" is "+haStateWord(e, lang)+".", name+" ist "+haStateWord(e, lang)+".")
	}
	// Several devices (a room): name the ones that are on.
	var on []*haEntity
	allLights := true
	for _, e := range ents {
		if e.Domain != "light" {
			allLights = false
		}
		if haOnLike(e) {
			on = append(on, e)
		}
	}
	where := ""
	if area != "" {
		where = area
	}
	if len(on) == 0 {
		switch {
		case allLights && where != "":
			return pick(lang, "No lights are on in "+where+".", haInArea(where)+" ist kein Licht an.")
		case allLights:
			return pick(lang, "No lights are on.", "Es ist kein Licht an.")
		case where != "":
			return pick(lang, "Nothing is on in "+where+".", haInArea(where)+" ist nichts an.")
		}
		return pick(lang, "None of them is on.", "Davon ist nichts an.")
	}
	names := joinAnd(haNames(on, 5), lang)
	if where != "" {
		return pick(lang, "On in "+where+": "+names+".", haInArea(where)+" ist an: "+names+".")
	}
	return pick(lang, "On: "+names+".", "An ist: "+names+".")
}

// ---------------------------------------------------------------------------
// home_setup: honest hints instead of persona chat pretending to act

func runHomeSetup(t *Turn, _ json.RawMessage) Reply {
	t.refused = true // nothing was done
	if haConfigured() {
		return Reply{Text: pick(t.Lang, "Device control is turned off in the settings.",
			"Die Gerätesteuerung ist in den Einstellungen ausgeschaltet.")}
	}
	return Reply{Text: haSetupText(t.Lang)}
}

// ---------------------------------------------------------------------------
// Router schemas

// haTargetNames lists allowed entity and area names for the router enum
// (never blocked ones), from the cache without a network call.
func haTargetNames(read bool) []string {
	c := haMatchCache()
	if c == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = safeTitle(s)
		k := strings.ToLower(s)
		if s == "" || seen[k] || len(out) >= haMaxEnum {
			return
		}
		seen[k] = true
		out = append(out, s)
	}
	for _, a := range c.areas {
		add(a.Name)
	}
	for _, e := range c.ents {
		if haWantClass(e, read) {
			add(e.Name)
		}
	}
	return out
}

func haTargetSchema(read bool) map[string]any {
	s := map[string]any{"type": "string", "description": "the device or room exactly as listed"}
	if names := haTargetNames(read); len(names) > 0 {
		s["enum"] = names
	}
	return s
}

func homeControlParams(*Turn) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"target": haTargetSchema(false),
			"action": map[string]any{"type": "string", "enum": []string{"on", "off", "set", "dim", "open", "close", "play", "pause", "stop"}},
			"device": map[string]any{"type": "string", "enum": []string{"light", "heating", "blinds", "switch", "scene", "media"},
				"description": "the kind of device when the target is a room"},
			"value": map[string]any{"type": "number", "description": "temperature in °C for heating, or percent for brightness or volume"},
		},
		"required": []string{"target", "action"},
	})
	return b
}

func homeQueryParams(*Turn) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"target": haTargetSchema(true),
			"what":   map[string]any{"type": "string", "enum": []string{"state", "temperature"}},
			"device": map[string]any{"type": "string", "enum": []string{"light", "heating", "blinds", "switch", "scene", "media"}},
		},
		"required": []string{"target"},
	})
	return b
}

// ---------------------------------------------------------------------------
// HTTP: /api/home/*

func handleHomeStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, haStatusDetached(r.Context(), false))
}

func handleHomeTest(w http.ResponseWriter, r *http.Request) {
	if !isAdminRequest(r) {
		writeJSONErrorCode(w, http.StatusForbidden, "local_only", "only from this machine (http://localhost:8080)")
		return
	}
	writeJSON(w, http.StatusOK, haStatusDetached(r.Context(), true))
}

type haEntityView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Area    string `json:"area"`
	Domain  string `json:"domain"`
	State   string `json:"state"`
	Blocked bool   `json:"blocked"`
}

func handleHomeEntities(w http.ResponseWriter, r *http.Request) {
	s := loadSettings()
	if s.PersonalLock && s.HomeQueries != "everyone" && !unlockedRequest(r) {
		writeJSONErrorCode(w, http.StatusForbidden, "locked", "personal data is locked; unlock with the PIN")
		return
	}
	if !haConfigured() {
		writeJSONErrorCode(w, http.StatusServiceUnavailable, "not_configured", "smart home is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), haTimeout*haProbeTimeoutMul)
	defer cancel()
	c, err := haEntities(ctx, haCacheTTL)
	if err != nil {
		writeJSONErrorCode(w, http.StatusBadGateway, haCode(err), "Home Assistant did not answer as expected")
		return
	}
	out := make([]haEntityView, 0, len(c.ents))
	for _, e := range c.ents {
		if len(out) >= haUIEntitiesMax {
			break
		}
		if e.Class == haOther {
			continue
		}
		v := haEntityView{ID: e.ID, Name: e.Name, Area: e.Area, Domain: e.Domain, State: e.State, Blocked: e.Class == haBlocked}
		if v.Blocked {
			v.State = "" // no lock or alarm states, even for the UI
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Area != out[j].Area {
			return out[i].Area < out[j].Area
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, http.StatusOK, map[string]any{"entities": out})
}

// ---------------------------------------------------------------------------
// Registration

var haImperatives = []string{
	"schalte", "schalt", "einschalten", "ausschalten", "anschalten", "anmachen", "ausmachen",
	"mach … an", "mach … aus", "mach … auf", "mach … zu", "mache … an", "mache … aus",
	"dreh … auf", "dreh … runter", "drehe … auf", "drehe … runter", "stell … auf", "stelle … auf",
	"dimm", "dimme", "öffne", "oeffne", "schließ", "schließe", "schliess", "fahr … hoch", "fahr … runter",
	"turn on", "turn off", "turn … on", "turn … off", "switch", "set … to", "dim", "open", "close",
}

func init() {
	registerTool(Tool{
		Name: "home_control",
		Description: "Control a smart-home device in the user's home: switch lights, switches, scenes or media players on or off, " +
			"dim lights, set the heating to a temperature, open or close blinds. Only when the user asks to change a device.",
		Params:     homeControlParams,
		Gate:       haGateNouns,
		GateFunc:   haGateFunc,
		Enabled:    homeControlEnabled,
		Access:     homeControlAccess,
		SideEffect: true,
		Imperative: haImperatives,
		Offer:      homeOffer,
		Run:        runHomeControl,
	})
	registerTool(Tool{
		Name: "home_query",
		Description: "Answer a question about the current state of a smart-home device or room, " +
			"e.g. whether a light is on or how warm it is in a room.",
		Params:   homeQueryParams,
		GateFunc: haGateFunc,
		Enabled:  homeQueryEnabled,
		Access:   homeQueryAccess,
		Run:      runHomeQuery,
	})
	registerTool(Tool{Name: "home_setup", Run: runHomeSetup}) // no Params: never offered to the router

	registerFastPath(FastPath{Name: "home_control", Order: 80, Tool: "home_control", Match: matchHomeControl})
	registerFastPath(FastPath{Name: "home_query", Order: 81, Tool: "home_query", Match: matchHomeQuery})
	registerFastPath(FastPath{Name: "home_setup", Order: 82, Tool: "home_setup", Match: matchHomeSetup})

	registerStatus("home", func(ctx context.Context) any { return haStatusDetached(ctx, false) })
	registerRoute("GET /api/home/status", handleHomeStatus)
	registerRoute("POST /api/home/test", handleHomeTest)
	registerRoute("GET /api/home/entities", handleHomeEntities)

	onHAConfigChange(haReset)
}

// String never includes the token.
func (c *haClient) String() string { return fmt.Sprintf("haClient(%s, %d pins)", c.base, len(c.pins)) }
