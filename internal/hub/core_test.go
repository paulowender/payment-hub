package hub

import (
	"testing"
	"time"
)

func TestAnchoredCalendar(t *testing.T) {
	a := time.Date(2024, 1, 31, 12, 0, 0, 0, time.UTC)
	if got := cycleAt(a, "month", 1, 1); got.Day() != 29 || got.Month() != 2 {
		t.Fatal(got)
	}
	if got := cycleAt(a, "month", 1, 2); got.Day() != 31 || got.Month() != 3 {
		t.Fatal(got)
	}
	if got := cycleAt(a, "day", 7, 2); !got.Equal(a.AddDate(0, 0, 14)) {
		t.Fatal(got)
	}
}
func TestIntervals(t *testing.T) {
	for _, v := range []struct {
		u  string
		n  int
		ok bool
	}{{"day", 1, true}, {"month", 12, true}, {"day", 0, false}, {"week", 1, false}, {"month", 121, false}} {
		if validInterval(v.u, v.n) != v.ok {
			t.Fatal(v)
		}
	}
}
