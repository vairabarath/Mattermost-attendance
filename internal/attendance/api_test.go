package attendance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func (f *eodStore) StartBreak(_ context.Context, _ string, at time.Time) (*Session, error) {
	n := len(f.sessions)
	if n == 0 || f.sessions[n-1].CheckOut != nil {
		return nil, ErrNotCheckedIn
	}
	s := &f.sessions[n-1]
	if onBreak(s) {
		return nil, ErrAlreadyOnBreak
	}
	s.Breaks = append(s.Breaks, Break{Start: at})
	c := *s
	return &c, nil
}
func (f *eodStore) EndBreak(_ context.Context, _ string, at time.Time) (*Session, error) {
	n := len(f.sessions)
	if n == 0 || f.sessions[n-1].CheckOut != nil {
		return nil, ErrNotCheckedIn
	}
	s := &f.sessions[n-1]
	if !onBreak(s) {
		return nil, ErrNotOnBreak
	}
	s.Breaks[len(s.Breaks)-1].End = &at
	c := *s
	return &c, nil
}

type notes []string

func (n *notes) record(userID, event, teamID string) { *n = append(*n, userID+":"+event+":"+teamID) }

func TestPanelMeAndActions(t *testing.T) {
	now := istAt(2026, 11, 27, 9, 0)
	svc, st, _, _ := newAdminEODService(now)
	svc.Now = func() time.Time { return now }
	st.sessions = nil
	var got notes
	svc.Notify = got.record
	ctx := context.Background()

	me, err := svc.Me(ctx, "u", "A")
	if err != nil || me.Session != nil || me.Team.Name != "InkYank" || !me.GitHubLinked || me.IsAdmin {
		t.Fatalf("before check-in: %+v %v", me, err)
	}

	// Same messages as the slash commands.
	msg, err := svc.Act(ctx, "u", "A", ActionIn, "standup at 10")
	if err != nil || !strings.Contains(msg, "Checked in to **InkYank** at 9:00 AM") {
		t.Fatalf("in: %q %v", msg, err)
	}
	now = istAt(2026, 11, 27, 11, 0)
	if msg, _ := svc.Act(ctx, "u", "A", ActionBreak, ""); !strings.Contains(msg, "On break since 11:00 AM") {
		t.Errorf("break: %q", msg)
	}
	now = istAt(2026, 11, 27, 11, 30)
	me, _ = svc.Me(ctx, "u", "A")
	if me.Session == nil || !me.Session.OnBreak || me.Session.BreakSince == nil || me.Session.NetMinutes != 120 || me.Session.NetSeconds != 7200 || me.TodayAllSeconds != 7200 ||
		me.Session.Mode != "office" || me.Session.Team.ID != "A" || me.TodayTeamMinutes != 120 || me.TodayAllMinutes != 120 {
		t.Fatalf("on break: %+v %+v", me, me.Session)
	}
	if msg, _ := svc.Act(ctx, "u", "A", ActionBack, ""); !strings.Contains(msg, "Back at 11:30 AM") || !strings.Contains(msg, "break 0h 30m") {
		t.Errorf("back: %q", msg)
	}
	now = istAt(2026, 11, 27, 18, 0)
	if msg, _ := svc.Act(ctx, "u", "A", ActionOut, "done"); !strings.Contains(msg, "Checked out of **InkYank**") {
		t.Errorf("out: %q", msg)
	}
	if me, _ := svc.Me(ctx, "u", "A"); me.Session != nil || me.TodayTeamMinutes != 8*60+30 {
		t.Errorf("after check-out: %+v", me)
	}
	// Every change refreshes the user's panel.
	want := "u:session_changed:A,u:session_changed:A,u:session_changed:A,u:session_changed:A"
	if strings.Join(got, ",") != want {
		t.Errorf("notifications: %v", got)
	}

	if _, err := svc.Act(ctx, "u", "A", "delete", ""); err == nil {
		t.Error("unknown action accepted")
	}
}

func TestPanelOnlyForTeamMembers(t *testing.T) {
	svc, _, _, _ := newAdminEODService(istAt(2026, 11, 27, 9, 0))
	ctx := context.Background()
	// "x" isn't a member of A; B isn't configured; nobody is a member without the API.
	for _, c := range [][2]string{{"x", "A"}, {"u", "B"}} {
		if _, err := svc.Me(ctx, c[0], c[1]); !errors.Is(err, ErrNotTeamMember) {
			t.Errorf("Me %v: %v", c, err)
		}
		if _, err := svc.Act(ctx, c[0], c[1], ActionIn, ""); !errors.Is(err, ErrNotTeamMember) {
			t.Errorf("Act %v: %v", c, err)
		}
	}
	svc.People = nil
	if _, err := svc.Me(ctx, "u", "A"); !errors.Is(err, ErrNotTeamMember) {
		t.Errorf("no people api: %v", err)
	}
}
