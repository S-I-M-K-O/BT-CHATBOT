package hermes

import (
	"strings"
	"time"
)

// briefingGuestsMax caps the guest (client id) entries in BriefingSeen.
const briefingGuestsMax = 32

// state.json holds the few small server flags that must survive a restart
// but are not settings the user edits.
type stateFile struct {
	Version             int               `json:"version"`             // 1
	BriefingSeen        map[string]string `json:"briefingSeen"`        // person or client id -> "YYYY-MM-DD"
	CityHintShown       bool              `json:"cityHintShown"`       // briefing told once how to set the home city
	CheckinsPausedUntil time.Time         `json:"checkinsPausedUntil"` // zero = not paused
}

var stateStore = &fileStore[stateFile]{
	Name:    "state.json",
	Version: 1,
	New: func() *stateFile {
		return &stateFile{Version: 1, BriefingSeen: map[string]string{}}
	},
}

// briefingSeen returns the last day ("YYYY-MM-DD") the morning briefing ran
// for key (a person id, or a client id for guests), "" if never.
func briefingSeen(key string) string {
	var day string
	_ = stateStore.View(func(st *stateFile) error {
		day = st.BriefingSeen[key]
		return nil
	})
	return day
}

func markBriefingSeen(key, day string) error {
	return stateStore.Update(func(st *stateFile) error {
		if st.BriefingSeen == nil {
			st.BriefingSeen = map[string]string{}
		}
		st.BriefingSeen[key] = day
		// Old entries are useless after the day passed; keep the map small.
		for k, d := range st.BriefingSeen {
			if d < day && len(st.BriefingSeen) > 64 {
				delete(st.BriefingSeen, k)
			}
		}
		// Guests are keyed by a client id the requester picks: cap them, so
		// a stream of fresh ids cannot grow state.json (at worst a guest
		// hears the briefing twice on one day).
		guests := 0
		for k := range st.BriefingSeen {
			if !strings.HasPrefix(k, "p_") {
				guests++
			}
		}
		for k := range st.BriefingSeen {
			if guests <= briefingGuestsMax {
				break
			}
			if k != key && !strings.HasPrefix(k, "p_") {
				delete(st.BriefingSeen, k)
				guests--
			}
		}
		return nil
	})
}

func cityHintShown() bool {
	var shown bool
	_ = stateStore.View(func(st *stateFile) error {
		shown = st.CityHintShown
		return nil
	})
	return shown
}

func setCityHintShown() error {
	return stateStore.Update(func(st *stateFile) error {
		st.CityHintShown = true
		return nil
	})
}

// checkinsPausedUntil returns the end of the current check-in pause, or the
// zero time when check-ins are not paused.
func checkinsPausedUntil() time.Time {
	var until time.Time
	_ = stateStore.View(func(st *stateFile) error {
		until = st.CheckinsPausedUntil
		return nil
	})
	if !until.IsZero() && !until.After(nowFunc()) {
		return time.Time{}
	}
	return until
}

// setCheckinsPausedUntil pauses check-ins until t; the zero time resumes them.
func setCheckinsPausedUntil(t time.Time) error {
	return stateStore.Update(func(st *stateFile) error {
		st.CheckinsPausedUntil = t.UTC()
		return nil
	})
}
