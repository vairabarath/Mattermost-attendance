package attendance

import "testing"

func TestPeriodFor(t *testing.T) {
	cases := []struct {
		name, now, first, last string
	}{
		{"today", "2026-11-25", "2026-11-25", "2026-11-25"},
		{"week", "2026-11-25", "2026-11-23", "2026-11-25"}, // Wed → Mon
		{"week", "2026-11-23", "2026-11-23", "2026-11-23"}, // Monday itself
		{"week", "2026-11-29", "2026-11-23", "2026-11-29"}, // Sunday belongs to the week before
		{"week", "2026-12-02", "2026-11-30", "2026-12-02"}, // across month end
		{"month", "2026-11-25", "2026-11-01", "2026-11-25"},
		{"month", "2026-12-01", "2026-12-01", "2026-12-01"},
	}
	for _, c := range cases {
		// 00:10 IST is still the previous day in UTC: checks IST is used.
		p, ok := PeriodFor(c.name, at(c.now, 0, 10), ist)
		if !ok {
			t.Fatalf("%s: not ok", c.name)
		}
		if got := p.FirstDay.Format(DayFormat); got != c.first {
			t.Errorf("%s @%s first: got %s want %s", c.name, c.now, got, c.first)
		}
		if got := p.LastDay.Format(DayFormat); got != c.last {
			t.Errorf("%s @%s last: got %s want %s", c.name, c.now, got, c.last)
		}
	}
	if _, ok := PeriodFor("year", at("2026-11-25", 9, 0), ist); ok {
		t.Error("unknown period accepted")
	}
}

func TestPeriodDays(t *testing.T) {
	p, _ := PeriodFor("week", at("2026-11-25", 12, 0), ist)
	days := p.Days()
	if len(days) != 3 || days[0] != "2026-11-23" || days[2] != "2026-11-25" {
		t.Errorf("days: %v", days)
	}
}
