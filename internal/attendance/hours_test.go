package attendance

import (
	"testing"
	"time"
)

var ist = mustLoad("Asia/Kolkata")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// at builds an IST timestamp on the given date.
func at(date string, hh, mm int) time.Time {
	d, err := time.ParseInLocation(DayFormat, date, ist)
	if err != nil {
		panic(err)
	}
	return d.Add(time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute)
}

func ptr(t time.Time) *time.Time { return &t }

func closed(team string, in, out time.Time, breaks ...Break) Session {
	return Session{TeamID: team, CheckIn: in, CheckOut: ptr(out), Breaks: breaks}
}

func brk(start, end time.Time) Break { return Break{Start: start, End: ptr(end)} }

func assertDur(t *testing.T, got map[DayKey]time.Duration, day, team string, want time.Duration) {
	t.Helper()
	if g := got[DayKey{Day: day, TeamID: team}]; g != want {
		t.Errorf("%s/%s: got %v, want %v", day, team, g, want)
	}
}

const d1 = "2026-11-24"
const d2 = "2026-11-25"

func TestSingleSession(t *testing.T) {
	got := NetByDay([]Session{closed("ink", at(d1, 9, 30), at(d1, 17, 45))}, at(d2, 12, 0), ist)
	assertDur(t, got, d1, "ink", 8*time.Hour+15*time.Minute)
	if len(got) != 1 {
		t.Errorf("expected 1 key, got %v", got)
	}
}

func TestMultipleSessionsSameDay(t *testing.T) {
	got := NetByDay([]Session{
		closed("ink", at(d1, 9, 0), at(d1, 13, 0)),
		closed("ink", at(d1, 14, 0), at(d1, 18, 30)),
	}, at(d2, 12, 0), ist)
	assertDur(t, got, d1, "ink", 8*time.Hour+30*time.Minute)
}

func TestSessionCrossingISTMidnightIsSplit(t *testing.T) {
	got := NetByDay([]Session{closed("ink", at(d1, 22, 0), at(d2, 2, 30))}, at(d2, 12, 0), ist)
	assertDur(t, got, d1, "ink", 2*time.Hour)
	assertDur(t, got, d2, "ink", 2*time.Hour+30*time.Minute)
}

func TestMidnightIsISTNotUTC(t *testing.T) {
	// 04:00–07:00 IST is 22:30–01:30 UTC: crosses UTC midnight, not IST.
	got := NetByDay([]Session{closed("ink", at(d2, 4, 0), at(d2, 7, 0))}, at(d2, 12, 0), ist)
	assertDur(t, got, d2, "ink", 3*time.Hour)
	if len(got) != 1 {
		t.Errorf("expected a single IST day, got %v", got)
	}
}

func TestBreaksDeducted(t *testing.T) {
	got := NetByDay([]Session{closed("ink", at(d1, 9, 0), at(d1, 18, 0),
		brk(at(d1, 13, 0), at(d1, 13, 45)),
		brk(at(d1, 16, 0), at(d1, 16, 15)),
	)}, at(d2, 12, 0), ist)
	assertDur(t, got, d1, "ink", 8*time.Hour)
}

func TestBreakCrossingMidnight(t *testing.T) {
	got := NetByDay([]Session{closed("ink", at(d1, 22, 0), at(d2, 2, 0),
		brk(at(d1, 23, 30), at(d2, 0, 30)),
	)}, at(d2, 12, 0), ist)
	assertDur(t, got, d1, "ink", 90*time.Minute)
	assertDur(t, got, d2, "ink", 90*time.Minute)
}

func TestOpenBreakEndsAtCheckOut(t *testing.T) {
	// Break never closed: it is treated as running until check-out.
	got := NetByDay([]Session{closed("ink", at(d1, 9, 0), at(d1, 17, 0),
		Break{Start: at(d1, 16, 0)},
	)}, at(d2, 12, 0), ist)
	assertDur(t, got, d1, "ink", 7*time.Hour)
}

func TestOverlappingBreaksNotDoubleCounted(t *testing.T) {
	got := NetByDay([]Session{closed("ink", at(d1, 9, 0), at(d1, 17, 0),
		brk(at(d1, 12, 0), at(d1, 13, 0)),
		brk(at(d1, 12, 30), at(d1, 13, 30)),
	)}, at(d2, 12, 0), ist)
	assertDur(t, got, d1, "ink", 6*time.Hour+30*time.Minute)
}

func TestOpenSessionCountsUntilNow(t *testing.T) {
	s := Session{TeamID: "ink", CheckIn: at(d2, 9, 0)}
	got := NetByDay([]Session{s}, at(d2, 11, 20), ist)
	assertDur(t, got, d2, "ink", 2*time.Hour+20*time.Minute)
}

func TestTwoTeamsSameDay(t *testing.T) {
	got := NetByDay([]Session{
		closed("ink", at(d1, 9, 0), at(d1, 13, 0)),
		closed("seechan", at(d1, 14, 0), at(d1, 17, 30)),
	}, at(d2, 12, 0), ist)
	assertDur(t, got, d1, "ink", 4*time.Hour)
	assertDur(t, got, d1, "seechan", 3*time.Hour+30*time.Minute)

	per := TeamTotalsForDay(got, d1)
	var total time.Duration
	for _, v := range per {
		total += v
	}
	if total != 7*time.Hour+30*time.Minute {
		t.Errorf("total: got %v", total)
	}
}

func TestZeroOrNegativeSpanIgnored(t *testing.T) {
	got := NetByDay([]Session{closed("ink", at(d1, 9, 0), at(d1, 9, 0))}, at(d2, 12, 0), ist)
	if len(got) != 0 {
		t.Errorf("expected nothing, got %v", got)
	}
}

func TestDayBounds(t *testing.T) {
	start, end := DayBounds(at(d2, 0, 10), ist)
	if !start.Equal(at(d2, 0, 0)) || !end.Equal(at("2026-11-26", 0, 0)) {
		t.Errorf("got %v – %v", start, end)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]string{
		7*time.Hour + 45*time.Minute + 59*time.Second: "7h 45m",
		0:                "0h 0m",
		45 * time.Minute: "0h 45m",
		-time.Minute:     "0h 0m",
		25 * time.Hour:   "25h 0m",
	}
	for in, want := range cases {
		if got := FormatDuration(in); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", in, got, want)
		}
	}
}
