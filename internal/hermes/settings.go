package hermes

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var settingsMu sync.Mutex

type Settings struct {
	SystemPrompt string  `json:"systemPrompt"`
	Temperature  float64 `json:"temperature"`
	MaxTokens    int     `json:"maxTokens"`
	Voice        string  `json:"voice"`
	VoiceMode    string  `json:"voiceMode"`
	Pitch        float64 `json:"pitch"`
	Rate         float64 `json:"rate"`
	Volume       float64 `json:"volume"`
	Speak        bool    `json:"speak"`
	Call         bool    `json:"call"`
	LocalModel   string  `json:"localModel"`
	EyeDesign    string  `json:"eyeDesign"`
	PiperVoice   string  `json:"piperVoice"`
	AutoScroll   bool    `json:"autoScroll"`
	VoiceSpeed   float64 `json:"voiceSpeed"`
	VoiceFx      string  `json:"voiceFx"`
	StreamSpeech bool    `json:"streamSpeech"`
	// CloudSpeech lets Chrome send microphone audio to Google for speech
	// recognition. Off = on-device recognition only (or no voice input).
	CloudSpeech bool `json:"cloudSpeech"`
	// UILang is the interface language only (labels, hints, toasts):
	// "auto" follows the voice language, "de" or "en" pin it. Speech
	// recognition, replies and spoken text keep following the voice.
	UILang string `json:"uiLang"`

	// Locale / time
	Timezone string `json:"timezone"` // IANA; "" => seeded by the browser once; fallback HERMES_TZ, TZ, Europe/Berlin
	HomeCity string `json:"homeCity"` // weather for the briefing
	// Local models
	NumCtx        int    `json:"numCtx"`       // the same for EVERY Ollama call to a model, or Ollama reloads it
	RouterModel   string `json:"routerModel"`  // "" => LocalModel
	ExtractModel  string `json:"extractModel"` // "" => RouterModel or LocalModel
	EmbedModel    string `json:"embedModel"`   // "" => lexical only
	RouterEnabled bool   `json:"routerEnabled"`
	KeepAlive     string `json:"keepAlive"`    // Ollama keep_alive on every call; "" => Ollama default
	FillerSpeech  bool   `json:"fillerSpeech"` // say "Moment." when a turn takes > 1.5 s
	// Privacy
	PersonalLock               bool `json:"personalLock"` // needs a PIN in secrets.json
	KeepHistoryOnSpeakerChange bool `json:"keepHistoryOnSpeakerChange"`
	// Voice ID
	VoiceIDEnabled   bool    `json:"voiceIdEnabled"`
	VoiceIDThreshold float64 `json:"voiceIdThreshold"`
	VoiceIDMargin    float64 `json:"voiceIdMargin"`
	VoiceIDMinSec    float64 `json:"voiceIdMinSec"`
	VoiceIDAdapt     bool    `json:"voiceIdAdapt"`
	// Memory
	MemoryEnabled      bool `json:"memoryEnabled"`
	MemoryAuto         bool `json:"memoryAuto"`
	MemoryAutoHealth   bool `json:"memoryAutoHealth"` // health facts only on explicit "merk dir" unless set
	GuestSeesHousehold bool `json:"guestSeesHousehold"`
	// Briefing
	BriefingOnGreeting bool `json:"briefingOnGreeting"`
	BriefingNews       bool `json:"briefingNews"`
	// Calendar
	CalendarOthers string `json:"calendarOthers"` // "third" | "refuse"
	// Smart home (URL and token live in secrets.json)
	HALanguage  string `json:"haLanguage"`  // "" => reply language
	HomeControl string `json:"homeControl"` // "known" (default) | "everyone" | "off"
	HomeQueries string `json:"homeQueries"` // "known" | "everyone"
	// Check-ins
	CheckinMeetings    bool   `json:"checkinMeetings"`
	CheckinMeetingLead int    `json:"checkinMeetingLead"` // minutes
	CheckinSitting     bool   `json:"checkinSitting"`
	CheckinSittingMin  int    `json:"checkinSittingMin"`
	QuietStart         string `json:"quietStart"` // "HH:MM"
	QuietEnd           string `json:"quietEnd"`   // "HH:MM"
}

const (
	defaultLocalModel  = "qwen3.5:latest"
	defaultPiperVoice  = "BT7274"
	maxSystemPromptLen = 8000
	maxSettingStrLen   = 200
)

const (
	defaultNumCtx     = 8192
	defaultEmbedModel = "embeddinggemma"
	defaultKeepAlive  = "30m"
)

var (
	calendarOthersModes = wordSet("third", "refuse")
	homeControlModes    = wordSet("everyone", "known", "off")
	homeQueriesModes    = wordSet("known", "everyone")
	haLanguagePattern   = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z]{2,4})?$`)
	clockPattern        = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
)

var eyeDesigns = wordSet("classic", "cyclops", "iris", "cortana")
var voiceFxModes = wordSet("none", "titan", "radio")
var uiLangs = wordSet("auto", "de", "en")

func defaultSettings() *Settings {
	return &Settings{
		SystemPrompt: englishDefaultPrompt,
		Temperature:  0.7,
		MaxTokens:    1024,
		Voice:        "",
		VoiceMode:    "piper",
		Pitch:        0.6,
		Rate:         1.0,
		Volume:       1.0,
		Speak:        true,
		Call:         true,
		LocalModel:   defaultLocalModel,
		EyeDesign:    "classic",
		PiperVoice:   defaultPiperVoice,
		AutoScroll:   true,
		VoiceSpeed:   1.0,
		VoiceFx:      "titan",
		StreamSpeech: true,
		CloudSpeech:  true,
		UILang:       "auto",

		NumCtx:             defaultNumCtx,
		EmbedModel:         defaultEmbedModel,
		RouterEnabled:      true,
		KeepAlive:          defaultKeepAlive,
		VoiceIDEnabled:     true,
		VoiceIDThreshold:   0.60,
		VoiceIDMargin:      0.05,
		VoiceIDMinSec:      1.0,
		VoiceIDAdapt:       true,
		MemoryEnabled:      true,
		MemoryAuto:         true,
		BriefingOnGreeting: true,
		CalendarOthers:     "third",
		HomeControl:        "known", // guests may not switch devices (Section 4.10)
		HomeQueries:        "known",
		CheckinMeetings:    true,
		CheckinMeetingLead: 10,
		CheckinSittingMin:  60,
		QuietStart:         "22:00",
		QuietEnd:           "07:00",
	}
}

func finiteOr(v, fallback float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return fallback
	}
	return v
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// normalize clamps and defaults every field so the rest of the server (and
// the frontend) can trust the values. It also migrates legacy values.
func (s *Settings) normalize() {
	d := defaultSettings()
	s.Temperature = clamp(finiteOr(s.Temperature, d.Temperature), 0, 2)
	s.MaxTokens = clampInt(s.MaxTokens, 16, 8192)
	s.Pitch = clamp(finiteOr(s.Pitch, d.Pitch), 0, 2)
	// Older versions stored a negative "rate" (-0.2); the browser speech rate
	// is a multiplier, so anything <= 0 becomes the neutral 1.0.
	if s.Rate <= 0 || math.IsNaN(s.Rate) {
		s.Rate = 1.0
	}
	s.Rate = clamp(finiteOr(s.Rate, 1.0), 0.1, 3)
	s.Volume = clamp(finiteOr(s.Volume, d.Volume), 0, 1)
	if s.VoiceSpeed == 0 || math.IsNaN(s.VoiceSpeed) {
		s.VoiceSpeed = 1.0
	}
	s.VoiceSpeed = clamp(finiteOr(s.VoiceSpeed, 1.0), 0.5, 2)
	if s.VoiceMode != "piper" && s.VoiceMode != "browser" {
		s.VoiceMode = d.VoiceMode
	}
	if !voiceFxModes[s.VoiceFx] {
		s.VoiceFx = d.VoiceFx
	}
	if !uiLangs[s.UILang] {
		s.UILang = d.UILang
	}
	if !eyeDesigns[s.EyeDesign] {
		s.EyeDesign = d.EyeDesign
	}
	if _, ok := piperVoices[s.PiperVoice]; !ok {
		s.PiperVoice = d.PiperVoice
	}
	s.LocalModel = strings.TrimSpace(truncateRunes(s.LocalModel, maxSettingStrLen))
	if s.LocalModel == "" {
		s.LocalModel = d.LocalModel
	}
	s.Voice = truncateRunes(s.Voice, maxSettingStrLen)
	s.SystemPrompt = truncateRunes(s.SystemPrompt, maxSystemPromptLen)
	s.normalizeCompanion(d)
}

// normalizeCompanion handles the voice ID, memory and companion-feature
// fields. A zero number means "not set" and takes the default; strings that
// may legitimately be empty ("" = feature off / fall back) stay empty.
func (s *Settings) normalizeCompanion(d *Settings) {
	s.Timezone = cleanText(s.Timezone, 64)
	if s.Timezone != "" {
		if _, err := time.LoadLocation(s.Timezone); err != nil || s.Timezone == "Local" {
			s.Timezone = ""
		}
	}
	s.HomeCity = cleanText(s.HomeCity, 100)

	if s.NumCtx == 0 {
		s.NumCtx = d.NumCtx
	}
	s.NumCtx = clampInt(s.NumCtx, 2048, 32768)
	s.RouterModel = cleanText(s.RouterModel, maxSettingStrLen)
	s.ExtractModel = cleanText(s.ExtractModel, maxSettingStrLen)
	s.EmbedModel = cleanText(s.EmbedModel, maxSettingStrLen)
	s.KeepAlive = strings.TrimSpace(s.KeepAlive)
	if s.KeepAlive != "" && s.KeepAlive != "-1" {
		if _, err := time.ParseDuration(s.KeepAlive); err != nil || len(s.KeepAlive) > 16 {
			s.KeepAlive = d.KeepAlive
		}
	}

	s.VoiceIDThreshold = clampOrDefault(s.VoiceIDThreshold, d.VoiceIDThreshold, 0.45, 0.85)
	s.VoiceIDMargin = clamp(finiteOr(s.VoiceIDMargin, d.VoiceIDMargin), 0, 0.3) // 0 is a valid margin
	s.VoiceIDMinSec = clampOrDefault(s.VoiceIDMinSec, d.VoiceIDMinSec, 0.5, 4)

	if !calendarOthersModes[s.CalendarOthers] {
		s.CalendarOthers = d.CalendarOthers
	}
	if s.HALanguage = strings.TrimSpace(s.HALanguage); s.HALanguage != "" && !haLanguagePattern.MatchString(s.HALanguage) {
		s.HALanguage = ""
	}
	if !homeControlModes[s.HomeControl] {
		s.HomeControl = d.HomeControl
	}
	if !homeQueriesModes[s.HomeQueries] {
		s.HomeQueries = d.HomeQueries
	}

	if s.CheckinMeetingLead == 0 {
		s.CheckinMeetingLead = d.CheckinMeetingLead
	}
	s.CheckinMeetingLead = clampInt(s.CheckinMeetingLead, 1, 60)
	if s.CheckinSittingMin == 0 {
		s.CheckinSittingMin = d.CheckinSittingMin
	}
	s.CheckinSittingMin = clampInt(s.CheckinSittingMin, 20, 240)
	if !clockPattern.MatchString(s.QuietStart) {
		s.QuietStart = d.QuietStart
	}
	if !clockPattern.MatchString(s.QuietEnd) {
		s.QuietEnd = d.QuietEnd
	}
}

// clampOrDefault treats 0 and non-finite values as "not set".
func clampOrDefault(v, def, lo, hi float64) float64 {
	v = finiteOr(v, def)
	if v == 0 {
		v = def
	}
	return clamp(v, lo, hi)
}

// settingsPath is where settings are written. SETTINGS_FILE wins; the default
// lives in the data dir next to generated images.
func settingsPath() string {
	return envOr("SETTINGS_FILE", defaultSettingsPath)
}

var defaultSettingsPath = filepath.Join("data", "settings.json")

// legacySettingsPath is the pre-data-dir location, read (never written) when
// SETTINGS_FILE is unset and the new file does not exist yet.
var legacySettingsPath = "settings.json"

// loadSettings returns the settings: defaults, then data/settings.json, then
// the keys set in hermes.conf (config.go), which always win.
func loadSettings() *Settings {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	s := loadSettingsFile()
	applyConfig(s)
	return s
}

// loadSettingsFile reads settings.json alone (no hermes.conf overlay).
func loadSettingsFile() *Settings {
	s := defaultSettings()
	path := settingsPath()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && os.Getenv("SETTINGS_FILE") == "" {
		b, err = os.ReadFile(legacySettingsPath)
	}
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("settings read error: %v", err)
		}
		return s
	}
	if err := json.Unmarshal(b, s); err != nil {
		log.Printf("settings parse error: %v", err)
		s = defaultSettings()
	}
	s.normalize()
	return s
}

// saveSettings writes s to settings.json. Keys managed by hermes.conf keep
// the file's value: they are overlaid onto s first (so s also reflects what
// is in effect), and settings.json stores the file's value for them, so a
// UI save can never write a value that contradicts hermes.conf.
func saveSettings(s *Settings) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	applyConfig(s)
	path := settingsPath()
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	b, err = mergeUnknownSettings(b, path)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b, 0o600)
}

// droppedSettingsKeys are never carried over from an old file: credentials
// moved to secrets.json and must not linger in the readable settings.
var droppedSettingsKeys = wordSet("haUrl", "haToken")

// mergeUnknownSettings keeps keys of the file on disk that this build does
// not know (written by a newer HERMES, or by a feature that is switched off),
// so saving from an older client never deletes them.
func mergeUnknownSettings(known []byte, path string) ([]byte, error) {
	var out map[string]json.RawMessage
	if err := json.Unmarshal(known, &out); err != nil {
		return nil, err
	}
	if old, err := os.ReadFile(path); err == nil {
		var prev map[string]json.RawMessage
		if json.Unmarshal(old, &prev) == nil {
			for k, v := range prev {
				if _, ok := out[k]; !ok && !droppedSettingsKeys[k] {
					out[k] = v
				}
			}
		}
	}
	return json.MarshalIndent(out, "", "  ")
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		writeJSON(w, http.StatusOK, settingsWithManaged(loadSettings()))
	case http.MethodPost, http.MethodPut:
		handleSaveSettings(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, PUT")
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	// Decode on top of the current settings: older clients post only the
	// fields they know, and everything else must survive the save. (Reset to
	// defaults still works because the UI posts the whole default object.)
	prev := loadSettings()
	s := *prev
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad request")
		return
	}
	s.normalize()
	// Keys managed by hermes.conf keep the file's value whatever was posted
	// (before the lock checks, so a managed key never trips them).
	applyConfig(&s)
	// The lock cannot protect anything without a PIN to unlock it.
	if s.PersonalLock && !prev.PersonalLock && !lockPINSet() {
		s.PersonalLock = false
	}
	// Switching the lock is an admin action, and with a PIN set it also
	// needs this device to be unlocked (Section 4.10).
	if s.PersonalLock != prev.PersonalLock {
		if !isAdminRequest(r) {
			writeJSONErrorCode(w, http.StatusForbidden, "local_only", "only from this machine (http://localhost:8080)")
			return
		}
		if lockPINSet() && !unlockedRequest(r) {
			writeJSONErrorCode(w, http.StatusForbidden, "locked", "unlock with the PIN first")
			return
		}
	}
	// While the lock is on, a locked device may not loosen what guests and
	// unrecognised voices get: that would undo the lock without the PIN.
	if prev.PersonalLock && lockPINSet() && loosensPrivacy(prev, &s) && !unlockedRequest(r) {
		writeJSONErrorCode(w, http.StatusForbidden, "locked", "unlock with the PIN to loosen privacy settings")
		return
	}
	if err := saveSettings(&s); err != nil {
		log.Printf("settings save error: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "could not save settings")
		return
	}
	// Other open tabs pick up the new voice / interface language without a
	// reload (the same event a hermes.conf change sends).
	hubPublish("settings.changed", "", map[string]string{"source": "ui"})
	writeJSON(w, http.StatusOK, settingsWithManaged(&s))
}

// homeControlRank orders homeControl from strict to open.
var homeControlRank = map[string]int{"off": 0, "known": 1, "everyone": 2}

// loosensPrivacy reports whether next gives guests or unrecognised voices
// more than prev: household memories, device control or state queries,
// other people's calendar entries as "third", easier voice matching (lower
// threshold, margin or minimum length, voice ID off, adaptation on), other
// speakers' turns shown without the PIN-gated collapse
// (keepHistoryOnSpeakerChange), microphone audio sent to Google
// (cloudSpeech), health facts saved automatically, or a new system prompt
// (it shares the persona prompt with personal context, so it could ask the
// model to repeat that context to whoever is in the room).
func loosensPrivacy(prev, next *Settings) bool {
	return (next.GuestSeesHousehold && !prev.GuestSeesHousehold) ||
		(next.CloudSpeech && !prev.CloudSpeech) ||
		(next.MemoryAutoHealth && !prev.MemoryAutoHealth) ||
		(next.VoiceIDAdapt && !prev.VoiceIDAdapt) ||
		(!next.VoiceIDEnabled && prev.VoiceIDEnabled) ||
		next.SystemPrompt != prev.SystemPrompt ||
		(next.KeepHistoryOnSpeakerChange && !prev.KeepHistoryOnSpeakerChange) ||
		homeControlRank[next.HomeControl] > homeControlRank[prev.HomeControl] ||
		(next.HomeQueries == "everyone" && prev.HomeQueries != "everyone") ||
		(next.CalendarOthers == "third" && prev.CalendarOthers != "third") ||
		next.VoiceIDThreshold < prev.VoiceIDThreshold ||
		next.VoiceIDMargin < prev.VoiceIDMargin ||
		next.VoiceIDMinSec < prev.VoiceIDMinSec
}

// uiLanguage is the language of interface text ("de" or "en"): a pinned
// uiLang, else the language of the selected voice.
func (s *Settings) uiLanguage() string {
	if s.UILang == "de" || s.UILang == "en" {
		return s.UILang
	}
	return voiceLang(s.PiperVoice)
}

// settingsView is the GET/POST /api/settings body: the settings plus the
// keys hermes.conf manages (shown read-only in the UI).
type settingsView struct {
	*Settings
	Managed []string `json:"managed"`
}

func settingsWithManaged(s *Settings) settingsView {
	c, _ := currentConfig()
	return settingsView{Settings: s, Managed: c.managedKeys()}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeJSONErrorCode is the error body of the companion APIs: English text
// plus a machine code the UI maps to DE/EN ("locked", "local_only", …).
func writeJSONErrorCode(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}
