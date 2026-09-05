package model

import "testing"

func TestDateWindowRequiresExplicitWholeSecondBounds(t *testing.T) {
	w, err := ParseDateWindow("2026-09-05T15:00:00+03:00", "2026-09-05T12:00:02Z")
	if err != nil {
		t.Fatal(err)
	}
	for date, want := range map[string]bool{
		"2026-09-05T11:59:59Z": false, "2026-09-05T12:00:00Z": true,
		"2026-09-05T12:00:01Z": true, "2026-09-05T12:00:02Z": false,
		"2026-09-05T12:00:00.5Z": false, "invalid": false,
	} {
		if w.Contains(date) != want {
			t.Fatalf("wrong inclusion for %s", date)
		}
	}
	for _, since := range []string{"", "2026-09-05", "2026-09-05T12:00:00", "2026-09-05T12:00:00.000Z", "2026-09-05T12:00:00+24:00", "2026-09-05T12:00:00+00:60", "1970-01-01T00:00:00Z", "2026-09-05T12:00:02Z", "2026-09-05T12:00:03Z"} {
		if _, err := ParseDateWindow(since, "2026-09-05T12:00:02Z"); TextErrorCategory(err) != ErrorInvalidInput {
			t.Fatalf("accepted %s", since)
		}
	}
	if _, err := ParseDateWindow("2038-01-19T03:14:06Z", "2038-01-19T03:14:08Z"); err == nil {
		t.Fatal("accepted timestamp overflow")
	}
}
