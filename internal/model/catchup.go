package model

import (
	"math"
	"regexp"
	"time"
)

// DateWindow selects sending timestamps in [Since, Until), at second precision.
// Values remain transient; cursors bind them but never grant access.
type DateWindow struct {
	Since int64 `json:"since"`
	Until int64 `json:"until"`
}

var secondTimestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(Z|[+-](0[0-9]|1[0-9]|2[0-3]):[0-5][0-9])$`)

func ParseDateWindow(since, until string) (DateWindow, error) {
	start, err := ParseSearchDate(since)
	if err != nil {
		return DateWindow{}, err
	}
	end, err := ParseSearchDate(until)
	if err != nil {
		return DateWindow{}, err
	}
	window := DateWindow{Since: start, Until: end}
	return window, window.Validate()
}

func (w DateWindow) Validate() error {
	if w.Since <= 0 || w.Until <= w.Since || w.Until > math.MaxInt32 {
		return TextError(ErrorInvalidInput, nil)
	}
	return nil
}

func (w DateWindow) Contains(date string) bool {
	t, err := time.Parse(time.RFC3339Nano, date)
	return err == nil && t.Nanosecond() == 0 && t.Unix() >= w.Since && t.Unix() < w.Until
}

// Counts describe this response only. State describes traversal across the cursor
// chain, not completeness of excluded or unsupported message content.
type CatchUpPeer struct {
	Peer     PeerID `json:"peer"`
	State    string `json:"state"`
	Fetched  int    `json:"fetched"`
	Returned int    `json:"returned"`
}

type CatchUpCoverage struct {
	Since string        `json:"since"`
	Until string        `json:"until"`
	Peers []CatchUpPeer `json:"peers"`
}

// ParseSearchDate accepts explicit whole-second timestamps in Telegram's range.
func ParseSearchDate(value string) (int64, error) {
	if !secondTimestamp.MatchString(value) {
		return 0, TextError(ErrorInvalidInput, nil)
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil || t.Nanosecond() != 0 || t.Unix() <= 0 || t.Unix() > math.MaxInt32 {
		return 0, TextError(ErrorInvalidInput, nil)
	}
	return t.Unix(), nil
}
