package hermes

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

// Every file HERMES keeps in data/ goes through this file: one atomic write
// path, one corrupt-file policy and one "newer format" guard, so a crash, a
// hand-edited file or a downgrade never silently loses state.

var errStoreReadOnly = errors.New("data file was written by a newer HERMES; refusing to overwrite")

// errNoChange, returned from a fileStore.Update callback, skips the write
// (Update then returns nil).
var errNoChange = errors.New("no change")

func dataDir() string { return envOr("DATA_DIR", "data") }

func dataPath(name string) string { return filepath.Join(dataDir(), name) }

// writeFileAtomic writes data so readers see either the old or the new file,
// never a torn one: temp file in the same dir, fsync, chmod, rename.
func writeFileAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// loadJSONFile decodes path into v. A missing file is (false, nil). An
// unparsable one is moved aside as <name>.corrupt-<unix> (only the newest
// copy is kept) and reported as (false, nil) so the caller starts empty
// instead of refusing to run.
func loadJSONFile(path string, v any) (found bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		quarantineCorrupt(path, err)
		return false, nil
	}
	return true, nil
}

func quarantineCorrupt(path string, cause error) {
	removeCorruptCopies(path)
	dst := fmt.Sprintf("%s.corrupt-%d", path, nowFunc().Unix())
	if err := os.Rename(path, dst); err != nil {
		log.Printf("store: %s is corrupt (%v) and could not be moved aside: %v", filepath.Base(path), cause, err)
		return
	}
	_ = os.Chmod(dst, 0o600)
	log.Printf("store: %s was corrupt (%v); kept a copy as %s and started empty", filepath.Base(path), cause, filepath.Base(dst))
}

// removeCorruptCopies deletes every <path>.corrupt-* copy. Person deletion
// uses it too: an old copy may still hold the deleted voiceprint.
func removeCorruptCopies(path string) int {
	matches, _ := filepath.Glob(path + ".corrupt-*")
	n := 0
	for _, m := range matches {
		if os.Remove(m) == nil {
			n++
		}
	}
	return n
}

// saveJSONFile writes v compactly with 0600: every data file may hold
// personal data.
func saveJSONFile(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b, 0o600)
}

// newID returns prefix_ plus 12 random hex chars ("p_3f9a0b1c2d3e").
func newID(prefix string) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; a time-based id
		// is still unique enough for a household.
		return fmt.Sprintf("%s_%012x", prefix, nowFunc().UnixNano()&0xffffffffffff)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

// cleanText is the one sanitiser for user-supplied strings: valid UTF-8, no
// control characters except newline and tab, trimmed, capped in runes.
func cleanText(s string, maxRunes int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return truncateRunes(strings.TrimSpace(s), maxRunes)
}

// cleanLine is cleanText for a single line: newlines and tabs become spaces,
// so untrusted text can never start a fake line in the log.
func cleanLine(s string, maxRunes int) string {
	return strings.Join(strings.Fields(cleanText(s, maxRunes)), " ")
}

// fileStore caches one versioned JSON file in memory. It reloads whenever
// dataPath(Name) changes (tests switch DATA_DIR) and refuses to write a file
// whose version is newer than it knows. New must return the empty state with
// its Version field set; the file is decoded on top of it.
type fileStore[T any] struct {
	Name    string
	Version int
	New     func() *T

	mu       sync.Mutex
	path     string
	state    *T
	readOnly bool
}

// load must be called with mu held.
func (s *fileStore[T]) load() error {
	path := dataPath(s.Name)
	if s.state != nil && s.path == path {
		return nil
	}
	st := s.New()
	readOnly := false
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case len(bytes.TrimSpace(b)) == 0:
	default:
		var head struct {
			Version int `json:"version"`
		}
		if err := json.Unmarshal(b, &head); err != nil {
			quarantineCorrupt(path, err)
			break
		}
		if head.Version > s.Version {
			// A newer HERMES wrote this file: show what can be read, never
			// write it, and never move it aside (its shape may simply have
			// changed).
			log.Printf("store: %s has version %d (newer HERMES data format; refusing to overwrite)", s.Name, head.Version)
			readOnly = true
			if json.Unmarshal(b, st) != nil {
				st = s.New()
			}
			break
		}
		if err := json.Unmarshal(b, st); err != nil { // valid JSON, wrong shape
			quarantineCorrupt(path, err)
			st = s.New()
		}
	}
	s.state, s.path, s.readOnly = st, path, readOnly
	return nil
}

// View runs fn with the current state under the store lock. fn must not keep
// references to the state after it returns.
func (s *fileStore[T]) View(fn func(st *T) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	return fn(s.state)
}

// Update runs fn under the store lock and saves the state when fn returns
// nil (errNoChange: nothing to save). fn must leave the state unchanged when
// it returns an error. On a save error the in-memory state is reloaded from disk so memory
// and file never disagree.
func (s *fileStore[T]) Update(fn func(st *T) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	if s.readOnly {
		return errStoreReadOnly
	}
	if err := fn(s.state); err != nil {
		if errors.Is(err, errNoChange) {
			return nil
		}
		return err
	}
	if err := saveJSONFile(s.path, s.state); err != nil {
		s.state = nil
		return err
	}
	return nil
}

// ReadOnly reports whether the file on disk is from a newer HERMES.
func (s *fileStore[T]) ReadOnly() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return false
	}
	return s.readOnly
}
