package hermes

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// config.go: hermes.conf, a hand-editable config file for the settings.
//
// Format (UTF-8, one setting per line):
//
//	# comment                 full-line comment
//	[Stimme / Voice]          section headers are cosmetic and ignored
//	speak = true              bools: true/false (also yes/no, on/off, 1/0)
//	temperature = 0.7         numbers
//	homeCity = Berlin  # x    "#" after whitespace starts a comment
//	quietStart = "22:00"      strings may be "double quoted"; needed only
//	                          for leading/trailing spaces or a "#". Inside
//	                          quotes \" \\ \n \t \r are escapes.
//	systemPrompt = Line 1\nLine 2   (unquoted \n works for systemPrompt)
//
// Keys are the JSON names of Settings (case-insensitive). Every value goes
// through Settings.normalize: a value normalize would change (out of range,
// unknown voice, bad clock time, …) is reported and ignored. Unknown keys,
// syntax errors and secrets are reported, never fatal. Secrets (tokens,
// passwords, Google/HA credentials, the PIN) and personalLock (needs the PIN
// flow) are refused: they belong in .env or the Settings window (Smart Home,
// Calendar, Privacy).
//
// Precedence: a key set in hermes.conf is "managed" and wins over
// settings.json. Saving from the UI keeps the file's value for managed keys
// (settings.json gets the file's value too, so removing the line later
// leaves the setting where the file had it). The file is never written by
// HERMES except by POST /api/config/write, which only creates it.
//
// Path: HERMES_CONFIG, else hermes.conf next to the settings file
// (data/hermes.conf by default; /app/data/hermes.conf in Docker, which is
// the mounted ./data folder).

const (
	configMaxBytes    = 256 << 10
	configMaxErrors   = 100
	configDefaultName = "hermes.conf"
)

// configPollInterval is how often the background worker checks the file.
var configPollInterval = 3 * time.Second

// configPath is where hermes.conf lives.
func configPath() string {
	if p := strings.TrimSpace(os.Getenv("HERMES_CONFIG")); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(settingsPath()), configDefaultName)
}

// ConfigError is one problem in hermes.conf. Code is machine-readable for
// the UI: syntax, unknown_key, invalid_value, secret, not_allowed,
// duplicate, too_large, read_error.
type ConfigError struct {
	Line int    `json:"line"`
	Key  string `json:"key"`
	Msg  string `json:"msg"`
	Code string `json:"code"`
}

// configField describes one setting for the config file and its export.
type configField struct {
	Key     string
	Section string
	DE, EN  string
}

// configSections are the export's sections, in order.
var configSections = []string{
	"Oberfläche / Interface",
	"Sprachausgabe / Voice output",
	"Spracheingabe / Voice input",
	"KI-Modell / AI model",
	"Ort & Zeit / Locale",
	"Privatsphäre / Privacy",
	"Stimmerkennung / Voice ID",
	"Gedächtnis / Memory",
	"Briefing",
	"Kalender / Calendar",
	"Smart Home",
	"Check-ins",
}

// configFields lists every setting the config file may hold, grouped for
// the export. A test makes sure it covers every Settings field except the
// ones in configForbiddenFields.
var configFields = []configField{
	{"uiLang", "Oberfläche / Interface", "Sprache der Oberfläche: auto (wie die Stimme), de, en", "Interface language: auto (follow the voice), de, en"},
	{"eyeDesign", "Oberfläche / Interface", "Augen-Design: classic, cyclops, iris, cortana", "Eye design: classic, cyclops, iris, cortana"},
	{"autoScroll", "Oberfläche / Interface", "Chat automatisch nach unten scrollen", "Scroll the chat to the newest message"},

	{"speak", "Sprachausgabe / Voice output", "Antworten vorlesen", "Speak replies aloud"},
	{"call", "Sprachausgabe / Voice output", "Gesprächsmodus (nach der Antwort wieder zuhören)", "Call mode (listen again after a reply)"},
	{"voiceMode", "Sprachausgabe / Voice output", "Sprachsynthese: piper (lokal) oder browser", "Speech engine: piper (local) or browser"},
	{"piperVoice", "Sprachausgabe / Voice output", "Piper-Stimme (Name aus voices/voices.json)", "Piper voice (name from voices/voices.json)"},
	{"voiceSpeed", "Sprachausgabe / Voice output", "Sprechtempo der Piper-Stimme (0.5 bis 2)", "Piper speaking speed (0.5 to 2)"},
	{"voiceFx", "Sprachausgabe / Voice output", "Stimmeffekt: none, titan, radio", "Voice effect: none, titan, radio"},
	{"streamSpeech", "Sprachausgabe / Voice output", "Schon während der Antwort sprechen", "Start speaking while the reply streams"},
	{"fillerSpeech", "Sprachausgabe / Voice output", "\"Moment.\" sagen, wenn eine Antwort länger dauert", "Say \"one moment\" when a reply takes long"},
	{"voice", "Sprachausgabe / Voice output", "Browser-Stimme (leer = Standard)", "Browser voice (empty = default)"},
	{"pitch", "Sprachausgabe / Voice output", "Tonhöhe der Browser-Stimme (0 bis 2)", "Browser voice pitch (0 to 2)"},
	{"rate", "Sprachausgabe / Voice output", "Tempo der Browser-Stimme (0.1 bis 3)", "Browser voice rate (0.1 to 3)"},
	{"volume", "Sprachausgabe / Voice output", "Lautstärke (0 bis 1)", "Volume (0 to 1)"},

	{"cloudSpeech", "Spracheingabe / Voice input", "Spracherkennung über Google erlauben (Chrome)", "Allow Google cloud speech recognition (Chrome)"},

	{"systemPrompt", "KI-Modell / AI model", "Systemanweisung (\\n für Zeilenumbruch)", "System prompt (\\n for a line break)"},
	{"temperature", "KI-Modell / AI model", "Kreativität des Modells (0 bis 2)", "Model temperature (0 to 2)"},
	{"maxTokens", "KI-Modell / AI model", "Maximale Antwortlänge in Tokens (16 bis 8192)", "Maximum reply length in tokens (16 to 8192)"},
	{"localModel", "KI-Modell / AI model", "Lokales Ollama-Modell", "Local Ollama model"},
	{"numCtx", "KI-Modell / AI model", "Kontextgröße für Ollama (2048 bis 32768)", "Ollama context size (2048 to 32768)"},
	{"routerEnabled", "KI-Modell / AI model", "Anfragen zu Werkzeugen weiterleiten (Router)", "Route requests to tools (router)"},
	{"routerModel", "KI-Modell / AI model", "Modell für den Router (leer = lokales Modell)", "Router model (empty = local model)"},
	{"extractModel", "KI-Modell / AI model", "Modell für das Herausziehen von Fakten (leer = Router-Modell)", "Fact extraction model (empty = router model)"},
	{"embedModel", "KI-Modell / AI model", "Embedding-Modell (leer = nur Stichwortsuche)", "Embedding model (empty = keyword search only)"},
	{"keepAlive", "KI-Modell / AI model", "Wie lange Ollama Modelle geladen hält (z. B. 30m, -1 = immer)", "How long Ollama keeps models loaded (e.g. 30m, -1 = forever)"},

	{"timezone", "Ort & Zeit / Locale", "Zeitzone (IANA, z. B. Europe/Berlin; leer = automatisch)", "Time zone (IANA, e.g. Europe/Berlin; empty = automatic)"},
	{"homeCity", "Ort & Zeit / Locale", "Wohnort für das Wetter", "Home city for the weather"},

	{"keepHistoryOnSpeakerChange", "Privatsphäre / Privacy", "Verlauf behalten, wenn jemand anderes spricht", "Keep the chat history when the speaker changes"},
	{"guestSeesHousehold", "Privatsphäre / Privacy", "Gäste sehen Haushalts-Erinnerungen", "Guests see household memories"},

	{"voiceIdEnabled", "Stimmerkennung / Voice ID", "Sprecher an der Stimme erkennen", "Recognise speakers by voice"},
	{"voiceIdThreshold", "Stimmerkennung / Voice ID", "Erkennungsschwelle (0.45 bis 0.85)", "Match threshold (0.45 to 0.85)"},
	{"voiceIdMargin", "Stimmerkennung / Voice ID", "Mindestabstand zum Zweitbesten (0 bis 0.3)", "Minimum margin to the runner-up (0 to 0.3)"},
	{"voiceIdMinSec", "Stimmerkennung / Voice ID", "Mindestlänge der Aufnahme in Sekunden (0.5 bis 4)", "Minimum speech length in seconds (0.5 to 4)"},
	{"voiceIdAdapt", "Stimmerkennung / Voice ID", "Stimmprofile mit der Zeit anpassen", "Adapt voice profiles over time"},

	{"memoryEnabled", "Gedächtnis / Memory", "Gedächtnis benutzen", "Use memory"},
	{"memoryAuto", "Gedächtnis / Memory", "Fakten automatisch merken", "Remember facts automatically"},
	{"memoryAutoHealth", "Gedächtnis / Memory", "Auch Gesundheitsdaten automatisch merken", "Also remember health facts automatically"},

	{"briefingOnGreeting", "Briefing", "Morgen-Briefing beim ersten Gruß des Tages", "Morning briefing on the first greeting of the day"},
	{"briefingNews", "Briefing", "Nachrichten im Briefing", "News in the briefing"},

	{"calendarOthers", "Kalender / Calendar", "Termine anderer Personen: third (neutral) oder refuse", "Other people's events: third (neutral) or refuse"},

	{"haLanguage", "Smart Home", "Sprache für Home Assistant (leer = Antwortsprache)", "Home Assistant language (empty = reply language)"},
	{"homeControl", "Smart Home", "Wer Geräte steuern darf: known (Standard), everyone, off", "Who may control devices: known (default), everyone, off"},
	{"homeQueries", "Smart Home", "Wer Gerätezustände abfragen darf: known (Standard), everyone", "Who may query device states: known (default), everyone"},

	{"checkinMeetings", "Check-ins", "Vor Terminen erinnern", "Remind before meetings"},
	{"checkinMeetingLead", "Check-ins", "Minuten vor dem Termin (1 bis 60)", "Minutes before a meeting (1 to 60)"},
	{"checkinSitting", "Check-ins", "An Pausen vom Sitzen erinnern", "Remind to take a break from sitting"},
	{"checkinSittingMin", "Check-ins", "Minuten bis zur Sitz-Erinnerung (20 bis 240)", "Minutes until the sitting reminder (20 to 240)"},
	{"quietStart", "Check-ins", "Ruhezeit Beginn (HH:MM)", "Quiet hours start (HH:MM)"},
	{"quietEnd", "Check-ins", "Ruhezeit Ende (HH:MM)", "Quiet hours end (HH:MM)"},
}

// configForbiddenFields are Settings fields the file may not set.
var configForbiddenFields = map[string]string{
	"personalLock": "personalLock is switched in Settings with the PIN, not in hermes.conf",
}

// configSecretKeys are credentials that live in .env or secrets.json. Keys
// that name a setting (maxTokens) are never treated as secrets.
var configSecretKeys = wordSet("haurl", "hatoken", "googleclientid", "googleclientsecret",
	"lockpin", "lockpinhash", "pin", "calendarowner", "mistralapikey", "hermestoken", "liststoken")

func isConfigSecretKey(key string) bool {
	k := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(key))
	if configSecretKeys[k] {
		return true
	}
	for _, w := range []string{"token", "secret", "password", "passwd", "apikey", "credential"} {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

// settingsFieldIndex maps a JSON key to its Settings field index.
var settingsFieldIndex = func() map[string]int {
	m := map[string]int{}
	t := reflect.TypeOf(Settings{})
	for i := 0; i < t.NumField(); i++ {
		tag := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			m[tag] = i
		}
	}
	return m
}()

// configKeyByLower resolves keys case-insensitively.
var configKeyByLower = func() map[string]string {
	m := map[string]string{}
	for k := range settingsFieldIndex {
		m[strings.ToLower(k)] = k
	}
	return m
}()

// configResult is a parsed and validated hermes.conf.
type configResult struct {
	values  Settings       // only the managed fields are meaningful
	managed map[string]int // key -> line
	errors  []ConfigError
}

func (c *configResult) managedKeys() []string {
	keys := make([]string, 0, len(c.managed))
	for k := range c.managed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// overlay copies the managed fields onto s.
func (c *configResult) overlay(s *Settings) {
	if c == nil || len(c.managed) == 0 {
		return
	}
	dst := reflect.ValueOf(s).Elem()
	src := reflect.ValueOf(&c.values).Elem()
	for k := range c.managed {
		i := settingsFieldIndex[k]
		dst.Field(i).Set(src.Field(i))
	}
}

// addError records a problem. Key and message are cut short: they may echo
// a whole over-long line (a 9000-character systemPrompt), and they end up in
// GET /api/config and the log.
func (c *configResult) addError(line int, key, code, format string, args ...any) {
	if len(c.errors) >= configMaxErrors {
		return
	}
	c.errors = append(c.errors, ConfigError{Line: line, Key: shortenConfigText(key, 100), Code: code, Msg: shortenConfigText(fmt.Sprintf(format, args...), 300)})
}

func shortenConfigText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return truncateRunes(s, n-1) + "…"
}

// parseConfig parses and validates the text of a hermes.conf.
func parseConfig(text string) *configResult {
	c := &configResult{values: *defaultSettings(), managed: map[string]int{}}
	text = strings.TrimPrefix(text, "\ufeff")
	lines := strings.Split(text, "\n")
	for n, raw := range lines {
		lineNo := n + 1
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(stripConfigComment(line), "]") {
				c.addError(lineNo, "", "syntax", "section header needs a closing ]")
			}
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			c.addError(lineNo, "", "syntax", "expected key = value")
			continue
		}
		rawKey := strings.TrimSpace(line[:eq])
		if rawKey == "" {
			c.addError(lineNo, "", "syntax", "missing key before =")
			continue
		}
		key, ok := configKeyByLower[strings.ToLower(rawKey)]
		if !ok && isConfigSecretKey(rawKey) {
			c.addError(lineNo, rawKey, "secret", "secrets are not allowed in hermes.conf; put them in .env or in Settings (Smart Home, Calendar, Privacy)")
			continue
		}
		if !ok {
			c.addError(lineNo, rawKey, "unknown_key", "unknown setting %q", rawKey)
			continue
		}
		if msg, bad := configForbiddenFields[key]; bad {
			c.addError(lineNo, key, "not_allowed", "%s", msg)
			continue
		}
		val, err := parseConfigValue(strings.TrimSpace(line[eq+1:]), key == "systemPrompt")
		if err != nil {
			c.addError(lineNo, key, "syntax", "%v", err)
			continue
		}
		if err := c.set(key, val); err != nil {
			c.addError(lineNo, key, "invalid_value", "%v", err)
			continue
		}
		if prev, dup := c.managed[key]; dup {
			c.addError(lineNo, key, "duplicate", "%s is also set on line %d; the last one wins", key, prev)
		}
		c.managed[key] = lineNo
	}
	return c
}

// stripConfigComment drops a "#" comment that follows whitespace.
func stripConfigComment(s string) string {
	for i := 1; i < len(s); i++ {
		if s[i] == '#' && (s[i-1] == ' ' || s[i-1] == '\t') {
			return strings.TrimSpace(s[:i])
		}
	}
	if strings.HasPrefix(s, "#") {
		return ""
	}
	return s
}

// parseConfigValue returns the string value of the right-hand side.
func parseConfigValue(s string, unescapeNewlines bool) (string, error) {
	if !strings.HasPrefix(s, `"`) {
		v := stripConfigComment(s)
		if unescapeNewlines {
			v = unescapeConfig(v)
		}
		return v, nil
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\\' && i+1 < len(s):
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '"', '\\':
				b.WriteByte(s[i])
			default:
				b.WriteByte('\\')
				b.WriteByte(s[i])
			}
		case ch == '"':
			rest := strings.TrimSpace(s[i+1:])
			if rest != "" && !strings.HasPrefix(rest, "#") {
				return "", fmt.Errorf("unexpected text after the closing quote")
			}
			return b.String(), nil
		default:
			b.WriteByte(ch)
		}
	}
	return "", fmt.Errorf("missing closing quote")
}

// unescapeConfig turns \n (and \\) in an unquoted systemPrompt into a newline.
func unescapeConfig(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// set parses val for key and stores it when normalize keeps it unchanged.
func (c *configResult) set(key, val string) error {
	i := settingsFieldIndex[key]
	probe := defaultSettings()
	f := reflect.ValueOf(probe).Elem().Field(i)
	switch f.Kind() {
	case reflect.Bool:
		b, ok := parseConfigBool(val)
		if !ok {
			return fmt.Errorf("expected true or false, got %q", val)
		}
		f.SetBool(b)
	case reflect.Int:
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("expected a whole number, got %q", val)
		}
		f.SetInt(int64(n))
	case reflect.Float64:
		x, err := strconv.ParseFloat(strings.Replace(val, ",", ".", 1), 64)
		if err != nil {
			return fmt.Errorf("expected a number, got %q", val)
		}
		f.SetFloat(x)
	case reflect.String:
		f.SetString(val)
	default:
		return fmt.Errorf("cannot be set in hermes.conf")
	}
	want := reflect.ValueOf(f.Interface())
	probe.normalize()
	got := reflect.ValueOf(probe).Elem().Field(i)
	if !reflect.DeepEqual(got.Interface(), want.Interface()) {
		return fmt.Errorf("invalid value %q (HERMES would use %s instead)", val, formatConfigValue(got))
	}
	reflect.ValueOf(&c.values).Elem().Field(i).Set(got)
	return nil
}

func parseConfigBool(s string) (bool, bool) {
	switch strings.ToLower(s) {
	case "true", "yes", "on", "1", "ja", "an":
		return true, true
	case "false", "no", "off", "0", "nein", "aus":
		return false, true
	}
	return false, false
}

// formatConfigValue writes a value so parseConfigValue reads it back.
func formatConfigValue(v reflect.Value) string {
	switch v.Kind() {
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Int:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'f', -1, 64)
	case reflect.String:
		return quoteConfigString(v.String())
	}
	return fmt.Sprint(v.Interface())
}

func quoteConfigString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

// ---------------------------------------------------------------------------
// Loading with a stat-keyed cache: loadSettings calls this on every request.

type configStamp struct {
	path    string
	exists  bool
	mtime   int64 // UnixNano
	size    int64
	readErr bool
}

var configCache = struct {
	sync.Mutex
	stamp  configStamp
	valid  bool
	result *configResult
}{}

func statConfig(path string) (configStamp, error) {
	st := configStamp{path: path}
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return st, nil
		}
		st.readErr = true
		return st, err
	}
	st.exists, st.mtime, st.size = true, fi.ModTime().UnixNano(), fi.Size()
	return st, nil
}

// currentConfig returns the parsed config file (re-read when it changed).
// A missing file yields an empty result.
func currentConfig() (*configResult, configStamp) {
	path := configPath()
	stamp, statErr := statConfig(path)
	configCache.Lock()
	defer configCache.Unlock()
	if configCache.valid && configCache.stamp == stamp && !stamp.readErr {
		return configCache.result, stamp
	}
	res := readConfig(path, stamp, statErr)
	if len(res.errors) > 0 && (!configCache.valid || configCache.stamp != stamp) {
		log.Printf("hermes.conf: %d problem(s), first: line %d: %s", len(res.errors), res.errors[0].Line, res.errors[0].Msg)
	}
	configCache.stamp, configCache.valid, configCache.result = stamp, true, res
	return res, stamp
}

func readConfig(path string, stamp configStamp, statErr error) *configResult {
	empty := &configResult{values: *defaultSettings(), managed: map[string]int{}}
	if statErr != nil {
		empty.addError(0, "", "read_error", "cannot read %s: %v", path, statErr)
		return empty
	}
	if !stamp.exists {
		return empty
	}
	if stamp.size > configMaxBytes {
		empty.addError(0, "", "too_large", "hermes.conf is larger than %d KiB and was ignored", configMaxBytes>>10)
		return empty
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) { // deleted between stat and read
			return empty
		}
		empty.addError(0, "", "read_error", "cannot read %s: %v", path, err)
		return empty
	}
	if len(b) > configMaxBytes {
		empty.addError(0, "", "too_large", "hermes.conf is larger than %d KiB and was ignored", configMaxBytes>>10)
		return empty
	}
	return parseConfig(string(b))
}

// applyConfig overlays the managed keys of hermes.conf onto s.
func applyConfig(s *Settings) []string {
	c, _ := currentConfig()
	c.overlay(s)
	return c.managedKeys()
}

// ---------------------------------------------------------------------------
// Export

// configExport renders s as a complete, commented hermes.conf.
func configExport(s *Settings) string { return renderConfig(s, false) }

// configTemplate is configExport with every value line commented out, so a
// freshly created hermes.conf locks nothing until a line is uncommented.
func configTemplate(s *Settings) string { return renderConfig(s, true) }

func renderConfig(s *Settings, commented bool) string {
	var b strings.Builder
	b.WriteString("# HERMES – hermes.conf\n")
	b.WriteString("#\n")
	b.WriteString("# DE: Jede Zeile \"name = wert\". Was hier steht, gilt und ist in den Einstellungen gesperrt.\n")
	b.WriteString("#     Zeilen löschen oder mit # auskommentieren, um sie wieder in der Oberfläche zu ändern.\n")
	b.WriteString("#     Änderungen gelten nach wenigen Sekunden, ohne Neustart. Keine Passwörter oder Tokens hier\n")
	b.WriteString("#     (die gehören in .env oder in die Einstellungen: Smart Home, Kalender, Datenschutz).\n")
	b.WriteString("# EN: One \"name = value\" per line. Values set here win and are locked in Settings.\n")
	b.WriteString("#     Delete a line or comment it out with # to change it in the UI again.\n")
	b.WriteString("#     Changes apply within seconds, no restart. No passwords or tokens here\n")
	b.WriteString("#     (they belong in .env or in Settings: Smart Home, Calendar, Privacy).\n")
	b.WriteString("#\n")
	b.WriteString("# true/false, Zahlen/numbers, Text/text (\"Anführungszeichen\"/quotes optional; \\n = newline)\n")
	prefix := ""
	if commented {
		prefix = "# "
		b.WriteString("#\n")
		b.WriteString("# DE: Alle Werte sind noch auskommentiert (aktueller Stand). \"# \" vor einer Zeile entfernen,\n")
		b.WriteString("#     damit die Datei diesen Wert festlegt.\n")
		b.WriteString("# EN: Every value is still commented out (current values). Remove the \"# \" in front of a\n")
		b.WriteString("#     line to let this file set that value.\n")
	}
	v := reflect.ValueOf(s).Elem()
	for _, sec := range configSections {
		fmt.Fprintf(&b, "\n[%s]\n", sec)
		for _, f := range configFields {
			if f.Section != sec {
				continue
			}
			fmt.Fprintf(&b, "\n# %s\n# %s\n", f.DE, f.EN)
			fmt.Fprintf(&b, "%s%s = %s\n", prefix, f.Key, formatConfigValue(v.Field(settingsFieldIndex[f.Key])))
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// HTTP

type configView struct {
	Path    string        `json:"path"`
	Exists  bool          `json:"exists"`
	Managed []string      `json:"managed"`
	Errors  []ConfigError `json:"errors"`
	Mtime   string        `json:"mtime"`
}

func buildConfigView() configView {
	c, stamp := currentConfig()
	v := configView{Path: configPath(), Exists: stamp.exists, Managed: c.managedKeys(), Errors: c.errors}
	if v.Errors == nil {
		v.Errors = []ConfigError{}
	}
	if stamp.exists {
		v.Mtime = time.Unix(0, stamp.mtime).UTC().Format(time.RFC3339)
	}
	return v
}

func handleConfigGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, buildConfigView())
}

func handleConfigExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="hermes.conf"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(configExport(loadSettings())))
}

func handleConfigWrite(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	if !isAdminRequest(r) {
		writeJSONErrorCode(w, http.StatusForbidden, "local_only", "only from this machine (http://localhost:8080)")
		return
	}
	err := writeConfigNew(configPath(), []byte(configTemplate(loadSettings())))
	if errors.Is(err, fs.ErrExist) {
		writeJSONErrorCode(w, http.StatusConflict, "exists", "hermes.conf already exists")
		return
	}
	if err != nil {
		log.Printf("hermes.conf write error: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "could not write hermes.conf")
		return
	}
	writeJSON(w, http.StatusOK, buildConfigView())
}

// configWriteMu serialises creations so two requests cannot both win.
var configWriteMu sync.Mutex

// writeConfigNew creates path atomically (temp file + hard link, which fails
// when the file exists) with mode 0600, like settings.json (it holds the
// home city and the system prompt); fs.ErrExist when it already exists.
func writeConfigNew(path string, data []byte) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	if _, err := os.Lstat(path); err == nil {
		return fs.ErrExist
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	if err := os.Link(name, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fs.ErrExist
		}
		// Some file systems (bind mounts) have no hard links: fall back to
		// rename, re-checking existence right before.
		if _, serr := os.Lstat(path); serr == nil {
			return fs.ErrExist
		}
		return os.Rename(name, path)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Watcher

// configWatcher polls the file every configPollInterval.
func configWatcher(ctx context.Context) {
	t := time.NewTicker(configPollInterval)
	defer t.Stop()
	runConfigWatcher(ctx, t.C)
}

// runConfigWatcher re-applies hermes.conf whenever its stat changes and
// publishes a non-durable "settings.changed" {"source":"config"}, once per
// observed change. A file being deleted, created or rewritten in place all
// count; a half-written file is simply re-read on the next tick.
func runConfigWatcher(ctx context.Context, tick <-chan time.Time) {
	last, _ := statConfig(configPath())
	currentConfig() // prime the cache and log problems once at start
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
		}
		now, err := statConfig(configPath())
		if err != nil || now == last {
			continue
		}
		last = now
		c, _ := currentConfig()
		log.Printf("hermes.conf changed: %d managed setting(s), %d problem(s)", len(c.managed), len(c.errors))
		hubPublish("settings.changed", "", map[string]string{"source": "config"})
	}
}

func init() {
	registerRoute("GET /api/config", handleConfigGet)
	registerRoute("GET /api/config/export", handleConfigExport)
	registerRoute("POST /api/config/write", handleConfigWrite)
	registerBackground("config", configWatcher)
}
