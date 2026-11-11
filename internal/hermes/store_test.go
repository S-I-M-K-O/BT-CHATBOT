package hermes

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// useTempData points DATA_DIR at a fresh directory for one test.
func useTempData(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	return dir
}

type testFile struct {
	Version int      `json:"version"`
	Items   []string `json:"items"`
}

func newTestStore() *fileStore[testFile] {
	return &fileStore[testFile]{Name: "test.json", Version: 1, New: func() *testFile { return &testFile{Version: 1} }}
}

func TestWriteFileAtomicPermsAndNoLeftovers(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "x.json")
	if err := writeFileAtomic(p, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte(`{"a":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != `{"a":2}` {
		t.Errorf("content %q", b)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm %v, want 0600", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestLoadJSONFileMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "people.json")
	var v testFile
	if found, err := loadJSONFile(p, &v); found || err != nil {
		t.Fatalf("missing file: found=%v err=%v", found, err)
	}

	// Two corrupt generations: only the newest copy is kept, with 0600.
	now := time.Unix(1_800_000_000, 0)
	old := nowFunc
	t.Cleanup(func() { nowFunc = old })
	for i := 0; i < 2; i++ {
		nowFunc = func() time.Time { return now.Add(time.Duration(i) * time.Second) }
		os.WriteFile(p, []byte("{broken"), 0o644)
		if found, err := loadJSONFile(p, &v); found || err != nil {
			t.Fatalf("corrupt file: found=%v err=%v", found, err)
		}
	}
	copies, _ := filepath.Glob(p + ".corrupt-*")
	if len(copies) != 1 || !strings.HasSuffix(copies[0], ".corrupt-1800000001") {
		t.Fatalf("corrupt copies = %v", copies)
	}
	if info, _ := os.Stat(copies[0]); info.Mode().Perm() != 0o600 {
		t.Errorf("corrupt copy perm %v", info.Mode().Perm())
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("corrupt file must be moved away")
	}
	if n := removeCorruptCopies(p); n != 1 {
		t.Errorf("removeCorruptCopies = %d", n)
	}
}

func TestFileStoreRoundTripAndReloadOnDataDirChange(t *testing.T) {
	dir1 := useTempData(t)
	s := newTestStore()
	if err := s.Update(func(st *testFile) error { st.Items = append(st.Items, "a"); return nil }); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dir1, "test.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file not written with 0600: %v %v", info, err)
	}

	// A new DATA_DIR is a different store: the cache must not leak across.
	useTempData(t)
	s.View(func(st *testFile) error {
		if len(st.Items) != 0 {
			t.Errorf("stale cache after DATA_DIR change: %v", st.Items)
		}
		return nil
	})

	// errNoChange skips the write entirely.
	s.Update(func(*testFile) error { return errNoChange })
	if _, err := os.Stat(dataPath("test.json")); !os.IsNotExist(err) {
		t.Error("errNoChange must not create the file")
	}

	t.Setenv("DATA_DIR", dir1)
	s.View(func(st *testFile) error {
		if len(st.Items) != 1 || st.Items[0] != "a" {
			t.Errorf("reload lost data: %v", st.Items)
		}
		return nil
	})
}

func TestFileStoreReadOnlyOnNewerVersion(t *testing.T) {
	dir := useTempData(t)
	p := filepath.Join(dir, "test.json")
	os.WriteFile(p, []byte(`{"version":2,"items":["future"]}`), 0o600)
	s := newTestStore()
	if !s.ReadOnly() {
		t.Fatal("newer version must make the store read-only")
	}
	err := s.Update(func(st *testFile) error { st.Items = nil; return nil })
	if !errors.Is(err, errStoreReadOnly) {
		t.Fatalf("Update err = %v", err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "future") {
		t.Error("read-only store overwrote the file")
	}

	// A newer format whose shape changed is still never moved aside.
	useTempData(t)
	p = dataPath("test.json")
	os.WriteFile(p, []byte(`{"version":3,"items":{"new":"shape"}}`), 0o600)
	if !s.ReadOnly() {
		t.Fatal("newer version with a new shape must be read-only")
	}
	if copies, _ := filepath.Glob(p + ".corrupt-*"); len(copies) != 0 {
		t.Errorf("newer file was quarantined: %v", copies)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("newer file must stay in place: %v", err)
	}
}

func TestFileStoreWrongShapeStartsEmpty(t *testing.T) {
	dir := useTempData(t)
	os.WriteFile(filepath.Join(dir, "test.json"), []byte(`{"version":1,"items":"nope"}`), 0o600)
	s := newTestStore()
	s.View(func(st *testFile) error {
		if st.Version != 1 || len(st.Items) != 0 {
			t.Errorf("want empty state, got %+v", st)
		}
		return nil
	})
}

func TestNewIDAndCleanText(t *testing.T) {
	re := regexp.MustCompile(`^p_[0-9a-f]{12}$`)
	a, b := newID("p"), newID("p")
	if !re.MatchString(a) || a == b {
		t.Errorf("ids %q %q", a, b)
	}
	if got := cleanText("  hi\x00\x07 there\n\tok \xff ", 100); got != "hi there\n\tok" {
		t.Errorf("cleanText = %q", got)
	}
	if got := cleanText("äöüßxyz", 4); got != "äöüß" {
		t.Errorf("cleanText truncation = %q", got)
	}
	if got := cleanLine("access_denied\n2026/11/11 calendar: connected\r\tx", 64); got != "access_denied 2026/11/11 calendar: connected x" {
		t.Errorf("cleanLine = %q", got)
	}
}

func TestStateAccessors(t *testing.T) {
	useTempData(t)
	if briefingSeen("p_1") != "" || cityHintShown() || !checkinsPausedUntil().IsZero() {
		t.Fatal("fresh state must be empty")
	}
	if err := markBriefingSeen("p_1", "2026-11-06"); err != nil {
		t.Fatal(err)
	}
	setCityHintShown()
	until := time.Now().Add(time.Hour).Truncate(time.Second)
	setCheckinsPausedUntil(until)
	stateStore.mu.Lock()
	stateStore.state = nil // force a reload from disk
	stateStore.mu.Unlock()
	if briefingSeen("p_1") != "2026-11-06" || !cityHintShown() || !checkinsPausedUntil().Equal(until) {
		t.Error("state did not persist")
	}
	setCheckinsPausedUntil(time.Now().Add(-time.Minute))
	if !checkinsPausedUntil().IsZero() {
		t.Error("an expired pause must read as not paused")
	}
}

func TestSecretsAccessorsAndPIN(t *testing.T) {
	dir := useTempData(t)
	for _, k := range []string{"HA_URL", "HA_TOKEN", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET"} {
		t.Setenv(k, "")
	}
	old := pinHashIter
	pinHashIter = 1000
	t.Cleanup(func() { pinHashIter = old })

	if lockPINSet() || checkLockPIN("1234") || haURL() != "" {
		t.Fatal("empty secrets must report nothing")
	}
	hash, err := hashLockPIN("1234")
	if err != nil {
		t.Fatal(err)
	}
	err = updateSecrets(func(s *secretsFile) error {
		s.HAURL, s.HAToken = "http://ha.local:8123/", "tok"
		s.GoogleClientID, s.GoogleClientSecret = "id", "sec"
		s.LockPINHash = hash
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(dir, "secrets.json")); info.Mode().Perm() != 0o600 {
		t.Errorf("secrets perm %v", info.Mode().Perm())
	}
	if haURL() != "http://ha.local:8123" || haToken() != "tok" || !lockPINSet() {
		t.Error("stored secrets not returned")
	}
	if !checkLockPIN("1234") || checkLockPIN("1235") || checkLockPIN("") {
		t.Error("PIN check wrong")
	}
	t.Setenv("HA_TOKEN", "envtok")
	t.Setenv("GOOGLE_CLIENT_ID", "envid")
	if id, sec := googleClient(); id != "envid" || sec != "sec" || haToken() != "envtok" {
		t.Errorf("env must override: %q %q %q", id, sec, haToken())
	}
	// Tampered hashes never verify (and never run unbounded iterations).
	for _, bad := range []string{"plain", "pbkdf2-sha256$999999999$AA$AA", "md5$1$AA$AA"} {
		if verifyPINHash(bad, "1234") {
			t.Errorf("%q verified", bad)
		}
	}
}

// RFC 7914 section 11 test vector for PBKDF2-HMAC-SHA256 (first 32 bytes).
func TestPBKDF2Vector(t *testing.T) {
	got := pbkdf2SHA256([]byte("passwd"), []byte("salt"), 1)
	want := "55ac046e56e3089fec1691c22544b605f94185216dde0465e68b9d57c20dacbc"
	if hex.EncodeToString(got) != want {
		t.Errorf("pbkdf2 = %x", got)
	}
}

// Guests are keyed by a client id the requester picks: same-day entries are
// capped, people's entries and the current key are always kept.
func TestBriefingSeenCapsGuestEntries(t *testing.T) {
	useTempData(t)
	day := "2026-11-06"
	if err := markBriefingSeen("p_anna", day); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if err := markBriefingSeen(fmt.Sprintf("c_%012x", i), day); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	_ = stateStore.View(func(st *stateFile) error { n = len(st.BriefingSeen); return nil })
	if n > briefingGuestsMax+1 {
		t.Errorf("BriefingSeen has %d entries, want at most %d", n, briefingGuestsMax+1)
	}
	if briefingSeen("p_anna") != day || briefingSeen(fmt.Sprintf("c_%012x", 199)) != day {
		t.Error("the person's entry or the newest guest entry was dropped")
	}
}
