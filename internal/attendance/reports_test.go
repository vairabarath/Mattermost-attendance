package attendance

import (
	"context"
	"strings"
	"testing"
	"time"
)

var monFri = map[time.Weekday]bool{time.Monday: true, time.Tuesday: true, time.Wednesday: true, time.Thursday: true, time.Friday: true}

func week(y int, m time.Month, d int) Period { // d = a Monday
	first := time.Date(y, m, d, 0, 0, 0, 0, ist)
	return Period{Name: "week", FirstDay: first, LastDay: first.AddDate(0, 0, 6)}
}

func TestExpectedHoursMinusHolidays(t *testing.T) {
	p := week(2026, 11, 16)
	if got := ExpectedHours(8, p, "A", monFri, nil); got != 40*time.Hour {
		t.Fatalf("plain week: %v", got)
	}
	hol := []Holiday{{Day: "2026-11-18", TeamID: "A"}, {Day: "2026-11-20"}} // team A Wed + all-org Fri
	if got := ExpectedHours(8, p, "A", monFri, hol); got != 24*time.Hour {
		t.Errorf("team A: %v, want 24h", got)
	}
	if got := ExpectedHours(7.5, p, "B", monFri, hol); got != 30*time.Hour {
		t.Errorf("team B: %v, want 30h (only the all-org holiday)", got)
	}
}

func TestBuildTeamReport(t *testing.T) {
	p := week(2026, 11, 16)
	now := istAt(2026, 11, 23, 9, 0)
	wfh := closed("A", istAt(2026, 11, 17, 9, 0), istAt(2026, 11, 17, 13, 0))
	wfh.WorkMode = "wfh"
	auto := closed("A", istAt(2026, 11, 18, 9, 0), istAt(2026, 11, 18, 17, 0))
	auto.AutoClosed = true
	in := TeamReportInput{
		TeamID: "A", Period: p, Now: now, Loc: ist, Workdays: monFri,
		Roster: []RosterMember{
			{UserID: "priya", Username: "priya", Tracked: true},
			{UserID: "guest", Username: "guest", Tracked: false},
			{UserID: "idle", Username: "idle", Tracked: true},
		},
		Usernames: map[string]string{"visitor": "visitor"},
		Settings:  map[string]MemberSettings{"priya": {ExpectedHours: 4}},
		Sessions: map[string][]Session{
			"priya": {
				closed("A", istAt(2026, 11, 16, 9, 0), istAt(2026, 11, 16, 17, 0)), // 8h
				wfh,  // 4h wfh
				auto, // 8h auto-closed
				closed("B", istAt(2026, 11, 19, 9, 0), istAt(2026, 11, 19, 11, 0)), // other org 2h
				closed("A", istAt(2026, 11, 9, 9, 0), istAt(2026, 11, 9, 17, 0)),   // previous week: ignored
			},
			"guest":   {closed("B", istAt(2026, 11, 16, 9, 0), istAt(2026, 11, 16, 10, 0))},
			"visitor": {closed("A", istAt(2026, 11, 20, 9, 0), istAt(2026, 11, 20, 10, 0))},
		},
	}
	rows := BuildTeamReport(in)
	by := map[string]TeamReportRow{}
	for _, r := range rows {
		by[r.Username] = r
	}
	p1 := by["priya"]
	if p1.Team != 20*time.Hour || p1.AllOrgs != 22*time.Hour || p1.Days != 3 || p1.WFHDays != 1 || p1.AutoClosed != 1 {
		t.Errorf("priya: %+v", p1)
	}
	if p1.Expected != 20*time.Hour || p1.Short() { // 4h × 5 days
		t.Errorf("priya expected: %v short=%v", p1.Expected, p1.Short())
	}
	if _, ok := by["guest"]; ok {
		t.Error("untracked member with no time in this team should be left out")
	}
	if v := by["visitor"]; v.Team != time.Hour || v.HasExpected {
		t.Errorf("non-member with time here: %+v", v)
	}
	if i := by["idle"]; !i.Short() || i.Expected != 40*time.Hour {
		t.Errorf("idle should be short of 40h: %+v", i)
	}
	out := RenderTeamReport("#### test", rows)
	if !strings.Contains(out, "@idle ⚠️") || !strings.Contains(out, "| @visitor | 1 | 1h 0m | 1h 0m | — |") {
		t.Errorf("render:\n%s", out)
	}
}

func TestBuildUserReportBreaksAndMidnight(t *testing.T) {
	p := week(2026, 11, 16)
	bs, be := istAt(2026, 11, 16, 13, 0), istAt(2026, 11, 16, 13, 45)
	sessions := []Session{
		closed("A", istAt(2026, 11, 16, 9, 0), istAt(2026, 11, 16, 18, 0), Break{bs, &be}),
		closed("A", istAt(2026, 11, 17, 22, 0), istAt(2026, 11, 18, 1, 0)), // crosses midnight
		closed("B", istAt(2026, 11, 19, 9, 0), istAt(2026, 11, 19, 12, 0)), // other team
	}
	rows := BuildUserReport(sessions, "A", p, istAt(2026, 11, 23, 9, 0), ist)
	if len(rows) != 3 {
		t.Fatalf("rows: %+v", rows)
	}
	if rows[0].Net != 8*time.Hour+15*time.Minute || rows[0].Breaks != 45*time.Minute {
		t.Errorf("mon: %+v", rows[0])
	}
	if rows[1].Day != "2026-11-17" || rows[1].Net != 2*time.Hour || rows[2].Net != time.Hour {
		t.Errorf("midnight split: %+v %+v", rows[1], rows[2])
	}
}

func TestParseFix(t *testing.T) {
	now := istAt(2026, 11, 25, 12, 0)
	fa, msg := ParseFix("@priya 2026-11-24 IN 9:30 out 18:00 forgot to check in", now, ist)
	if msg != "" || fa.Username != "priya" || !fa.In.Equal(istAt(2026, 11, 24, 9, 30)) || !fa.Out.Equal(istAt(2026, 11, 24, 18, 0)) || fa.Reason != "forgot to check in" {
		t.Fatalf("valid: %+v %q", fa, msg)
	}
	bad := map[string]string{
		"future date":    "priya 2026-11-26 in 09:00 out 10:00 reason",
		"out before in":  "priya 2026-11-24 in 18:00 out 09:00 reason",
		"out in future":  "priya 2026-11-25 in 09:00 out 13:00 reason",
		"bad time":       "priya 2026-11-24 in 25:00 out 26:00 reason",
		"no reason":      "priya 2026-11-24 in 09:00 out 10:00",
		"too short":      "priya 2026-11-24 in 09:00 out 10:00 x",
		"bad date":       "priya 2026-02-30 in 09:00 out 10:00 reason",
		"missing in/out": "priya 2026-11-24 09:00 10:00 reason",
	}
	for name, args := range bad {
		if _, msg := ParseFix(args, now, ist); msg == "" {
			t.Errorf("%s accepted", name)
		}
	}
	if _, msg := ParseFix("priya 2026-11-25 in 09:00 out 11:30 fixed today", now, ist); msg != "" {
		t.Errorf("today before now rejected: %s", msg)
	}
}

func TestExportCSV(t *testing.T) {
	bs, be := istAt(2026, 11, 2, 13, 0), istAt(2026, 11, 2, 13, 30)
	s := closed("A", istAt(2026, 11, 2, 9, 0), istAt(2026, 11, 2, 17, 0), Break{bs, &be})
	s.WorkMode, s.InNote, s.OutSummary = "office", "=HYPERLINK(\"x\")", "done, shipped"
	data, err := ExportCSV([]ExportRow{{Username: "priya", DisplayName: "Priya", Session: s}}, istAt(2026, 11, 25, 9, 0), ist)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "username,display_name,date") {
		t.Fatalf("csv: %s", data)
	}
	want := `priya,Priya,2026-11-02,2026-11-02 09:00,2026-11-02 17:00,30,450,7h 30m,office,false,"'=HYPERLINK(""x"")","done, shipped"`
	if lines[1] != want {
		t.Errorf("row:\n got %s\nwant %s", lines[1], want)
	}
}

func TestWeeklyReportPostsLastWeekOnce(t *testing.T) {
	st := newFakeJobStore()
	st.sessions["u"] = []Session{closed("A", istAt(2026, 11, 17, 9, 0), istAt(2026, 11, 17, 17, 0))}
	roster := fakeRoster{"A": {{ID: "u", Username: "priya"}}, "B": {{ID: "u", Username: "priya"}}}
	j, msg := newJobs(st, roster)
	now := istAt(2026, 11, 23, 9, 0) // Monday
	j.WeeklyReports(context.Background(), now)
	j.WeeklyReports(context.Background(), now)
	if len(msg.posts["admA"]) != 1 || len(msg.posts["admB"]) != 1 {
		t.Fatalf("posts: %v", msg.posts)
	}
	a := msg.posts["admA"][0]
	if !strings.Contains(a, "Mon 16 Nov – Sun 22 Nov") || !strings.Contains(a, "| @priya ⚠️ | 1 | 8h 0m | 8h 0m | 40h 0m |") {
		t.Errorf("weekly A:\n%s", a)
	}
	if !strings.Contains(msg.posts["admB"][0], "| @priya ⚠️ | 0 | 0h 0m | 8h 0m | 40h 0m |") {
		t.Errorf("weekly B:\n%s", msg.posts["admB"][0])
	}
}
