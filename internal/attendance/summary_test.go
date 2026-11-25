package attendance

import (
	"strings"
	"testing"

	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

func TestFilterRosterDropsBotsDeactivatedAndMarksUntracked(t *testing.T) {
	users := []mattermost.User{
		{ID: "a", Username: "a"}, {ID: "bot", Username: "bot", IsBot: true},
		{ID: "gone", Username: "gone", DeleteAt: 123}, {ID: "u", Username: "u"},
	}
	r := FilterRoster(users, map[string]MemberSettings{"u": {Tracked: false}})
	if len(r) != 2 || r[0].UserID != "a" || !r[0].Tracked || r[1].UserID != "u" || r[1].Tracked {
		t.Fatalf("roster: %+v", r)
	}
}

func TestDailySummary(t *testing.T) {
	now := istAt(2026, 11, 25, 20, 0)
	roster := []RosterMember{
		{UserID: "priya", Username: "priya", Tracked: true},
		{UserID: "raj", Username: "raj", Tracked: true},
		{UserID: "other", Username: "other", Tracked: true},
		{UserID: "absent", Username: "absent", Tracked: true},
		{UserID: "untracked", Username: "untracked", Tracked: false},
	}
	sessions := map[string][]Session{
		"priya": {closed("A", istAt(2026, 11, 25, 9, 0), istAt(2026, 11, 25, 17, 0))},
		"raj":   {{TeamID: "A", CheckIn: istAt(2026, 11, 25, 10, 0), WorkMode: "wfh"}},
		"other": {closed("B", istAt(2026, 11, 25, 9, 0), istAt(2026, 11, 25, 12, 0))},
	}
	sessions["priya"][0].WorkMode = "office"

	got := BuildDailySummary(SummaryInput{
		TeamID: "A", TeamName: "InkYank", Teams: teamNames{"A": "InkYank", "B": "Seechanlabs"},
		Day: now, Now: now, Loc: ist, Roster: roster, Sessions: sessions,
	})
	for _, want := range []string{
		"Worked today: 2** · 🏠 WFH: 1",
		"| @priya | 8h 0m | 🏢 |",
		"| @raj | 10h 0m | 🏠 |",
		"**Still checked in:** @raj (since 10:00 AM)",
		"**Worked for another org today:** @other (Seechanlabs)",
		"**Not checked in:** @absent",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
	// Worked for another org → not absent; untracked → never listed absent.
	notIn := got[strings.Index(got, "**Not checked in:**"):]
	if strings.Contains(notIn, "@other") || strings.Contains(notIn, "untracked") {
		t.Errorf("wrong absentees: %s", notIn)
	}
}

func TestDailySummaryOnLeaveHook(t *testing.T) {
	now := istAt(2026, 11, 25, 20, 0)
	got := BuildDailySummary(SummaryInput{
		TeamID: "A", TeamName: "InkYank", Teams: teamNames{}, Day: now, Now: now, Loc: ist,
		Roster:  []RosterMember{{UserID: "x", Username: "x", Tracked: true}},
		OnLeave: func(string) bool { return true },
	})
	if strings.Contains(got, "Not checked in") || !strings.Contains(got, "Nobody checked in") {
		t.Errorf("on-leave member listed: %s", got)
	}
}
