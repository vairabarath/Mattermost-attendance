package attendance

import "testing"

func TestParseMemberUpdate(t *testing.T) {
	u, _, e := parseMemberUpdate("tracked", "off")
	if e != "" || u.Tracked == nil || *u.Tracked {
		t.Fatalf("tracked off: %+v %q", u, e)
	}
	u, _, e = parseMemberUpdate("Reminders", "ON")
	if e != "" || u.RemindersOn == nil || !*u.RemindersOn {
		t.Fatalf("reminders on: %+v %q", u, e)
	}
	u, desc, e := parseMemberUpdate("hours", "4.5")
	if e != "" || *u.ExpectedHours != 4.5 || desc != "expected hours 4h 30m/day" {
		t.Fatalf("hours: %+v %q %q", u, desc, e)
	}
	for _, bad := range [][2]string{{"hours", "25"}, {"hours", "-1"}, {"hours", "x"}, {"tracked", "maybe"}, {"colour", "red"}} {
		if _, _, e := parseMemberUpdate(bad[0], bad[1]); e == "" {
			t.Errorf("%v accepted", bad)
		}
	}
	if fmtHours(8) != "8h" {
		t.Errorf("fmtHours(8) = %q", fmtHours(8))
	}
}
