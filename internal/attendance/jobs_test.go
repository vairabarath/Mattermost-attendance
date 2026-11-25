package attendance

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/config"
	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

// --- auto-close math ---

func TestAutoCloseNetEqualsExpectedWithBreaks(t *testing.T) {
	in := istAt(2026, 11, 24, 9, 0)
	b1s, b1e := istAt(2026, 11, 24, 13, 0), istAt(2026, 11, 24, 14, 0)
	b2s, b2e := istAt(2026, 11, 24, 16, 0), istAt(2026, 11, 24, 16, 30)
	s := Session{TeamID: "A", CheckIn: in, Breaks: []Break{{b1s, &b1e}, {b2s, &b2e}}}
	now := istAt(2026, 11, 25, 2, 0) // 17h later

	out := AutoCloseTime(s, 8*time.Hour, now)
	if want := istAt(2026, 11, 24, 18, 30); !out.Equal(want) { // 9:00 + 8h + 1h30 breaks
		t.Fatalf("out = %v, want %v", out.In(ist), want.In(ist))
	}
	s.CheckOut = &out
	if net := sumAll(NetByDay([]Session{s}, now, ist)); net != 8*time.Hour {
		t.Fatalf("net = %v, want 8h", net)
	}
}

func TestAutoCloseOpenBreakAndCapAtNow(t *testing.T) {
	in := istAt(2026, 11, 24, 9, 0)
	// Went on break at 11:00 and never came back: only 2h of work exists.
	s := Session{TeamID: "A", CheckIn: in, Breaks: []Break{{Start: istAt(2026, 11, 24, 11, 0)}}}
	now := istAt(2026, 11, 25, 2, 0)
	if out := AutoCloseTime(s, 8*time.Hour, now); !out.Equal(now) {
		t.Fatalf("expected cap at now, got %v", out.In(ist))
	}
	// Plenty of work but closing time would be in the future: never after now.
	s2 := Session{TeamID: "A", CheckIn: in}
	early := istAt(2026, 11, 24, 12, 0)
	if out := AutoCloseTime(s2, 8*time.Hour, early); out.After(early) {
		t.Fatalf("out %v is after now", out)
	}
}

func sumAll(m map[DayKey]time.Duration) (t time.Duration) {
	for _, v := range m {
		t += v
	}
	return t
}

// --- calendar ---

func TestIsWorkday(t *testing.T) {
	wd := map[time.Weekday]bool{time.Monday: true, time.Tuesday: true, time.Wednesday: true, time.Thursday: true, time.Friday: true}
	wed, sat := istAt(2026, 11, 25, 10, 0), istAt(2026, 11, 28, 10, 0)
	if !IsWorkday(wed, "A", wd, nil) || IsWorkday(sat, "A", wd, nil) {
		t.Fatal("weekday handling wrong")
	}
	teamHol := []Holiday{{Day: "2026-11-25", TeamID: "A"}}
	if IsWorkday(wed, "A", wd, teamHol) || !IsWorkday(wed, "B", wd, teamHol) {
		t.Fatal("team holiday should only affect team A")
	}
	if IsWorkday(wed, "B", wd, []Holiday{{Day: "2026-11-25"}}) {
		t.Fatal("global holiday should affect every team")
	}
}

// --- jobs with fakes ---

type fakeJobStore struct {
	runs     map[string]bool
	members  map[string]MemberSettings
	sessions map[string][]Session
	open     []OpenSessionInfo
	closed   map[int64]time.Time
	audits   []AuditEntry
	holidays []Holiday
}

func newFakeJobStore() *fakeJobStore {
	return &fakeJobStore{runs: map[string]bool{}, members: map[string]MemberSettings{}, sessions: map[string][]Session{}, closed: map[int64]time.Time{}}
}

func (f *fakeJobStore) ClaimJobRun(_ context.Context, job, key string) (bool, error) {
	if f.runs[job+"|"+key] {
		return false, nil
	}
	f.runs[job+"|"+key] = true
	return true, nil
}
func (f *fakeJobStore) SyncMembers(_ context.Context, users []mattermost.User) error {
	for _, u := range users {
		if _, ok := f.members[u.ID]; !ok {
			f.members[u.ID] = MemberSettings{UserID: u.ID, Username: u.Username, Tracked: true, RemindersOn: true, ExpectedHours: 8, Active: true}
		}
	}
	return nil
}
func (f *fakeJobStore) AllMembers(context.Context) (map[string]MemberSettings, error) {
	return f.members, nil
}
func (f *fakeJobStore) AllSessionsOverlapping(context.Context, time.Time, time.Time) (map[string][]Session, error) {
	return f.sessions, nil
}
func (f *fakeJobStore) OpenSessions(_ context.Context, before time.Time) ([]OpenSessionInfo, error) {
	var out []OpenSessionInfo
	for _, o := range f.open {
		if _, done := f.closed[o.Session.ID]; !done && (before.IsZero() || o.Session.CheckIn.Before(before)) {
			out = append(out, o)
		}
	}
	return out, nil
}
func (f *fakeJobStore) AutoCloseSession(_ context.Context, id int64, out time.Time) (time.Time, bool, error) {
	if _, done := f.closed[id]; done {
		return time.Time{}, false, nil
	}
	f.closed[id] = out
	return out, true, nil
}
func (f *fakeJobStore) HolidaysOn(context.Context, string) ([]Holiday, error) { return f.holidays, nil }
func (f *fakeJobStore) InsertAudit(_ context.Context, e AuditEntry) error {
	f.audits = append(f.audits, e)
	return nil
}

type fakeMsg struct {
	dms   map[string][]string
	posts map[string][]string
}

func (m *fakeMsg) CreatePost(_ context.Context, ch, msg string) error {
	m.posts[ch] = append(m.posts[ch], msg)
	return nil
}
func (m *fakeMsg) DirectMessage(_ context.Context, uid, msg string) error {
	m.dms[uid] = append(m.dms[uid], msg)
	return nil
}

type fakeRoster map[string][]mattermost.User

func (r fakeRoster) TeamUsers(_ context.Context, teamID string) ([]mattermost.User, error) {
	return r[teamID], nil
}

type teamNames map[string]string

func (t teamNames) TeamName(id string) string {
	if n, ok := t[id]; ok {
		return n
	}
	return "another team"
}

func newJobs(st *fakeJobStore, roster fakeRoster) (*Jobs, *fakeMsg) {
	msg := &fakeMsg{dms: map[string][]string{}, posts: map[string][]string{}}
	return &Jobs{
		Store: st, Roster: roster, Msg: msg,
		Teams: []config.Team{
			{Key: "a", ID: "A", Name: "InkYank", AttendanceChannelID: "chA", AdminChannelID: "admA"},
			{Key: "b", ID: "B", Name: "Seechanlabs", AttendanceChannelID: "chB", AdminChannelID: "admB"},
		},
		TeamNames:  teamNames{"A": "InkYank", "B": "Seechanlabs"},
		Workdays:   map[time.Weekday]bool{time.Monday: true, time.Tuesday: true, time.Wednesday: true, time.Thursday: true, time.Friday: true},
		MaxSession: 16 * time.Hour,
		Leave:      NoLeave{},
		Loc:        ist,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, msg
}

func TestAutoCloseJobRunsOncePerHour(t *testing.T) {
	st := newFakeJobStore()
	in := istAt(2026, 11, 24, 9, 0)
	st.open = []OpenSessionInfo{{UserID: "u1", Username: "priya", Expected: 8, Session: Session{ID: 7, TeamID: "A", CheckIn: in}}}
	j, msg := newJobs(st, nil)
	now := istAt(2026, 11, 25, 2, 5)

	j.AutoClose(context.Background(), now)
	j.AutoClose(context.Background(), now.Add(10*time.Minute)) // same hour: no-op
	if got := st.closed[7]; !got.Equal(istAt(2026, 11, 24, 17, 0)) {
		t.Fatalf("closed at %v, want 17:00", got.In(ist))
	}
	if len(msg.dms["u1"]) != 1 || len(msg.posts["admA"]) != 1 || len(st.audits) != 1 || st.audits[0].Action != "auto_close" {
		t.Fatalf("side effects: dms=%v posts=%v audits=%d", msg.dms, msg.posts, len(st.audits))
	}
	if !strings.Contains(msg.posts["admA"][0], "@priya") {
		t.Errorf("admin flag: %s", msg.posts["admA"][0])
	}
}

func TestAutoCloseSkipsYoungSessions(t *testing.T) {
	st := newFakeJobStore()
	st.open = []OpenSessionInfo{{UserID: "u1", Expected: 8, Session: Session{ID: 1, TeamID: "A", CheckIn: istAt(2026, 11, 25, 9, 0)}}}
	j, _ := newJobs(st, nil)
	j.AutoClose(context.Background(), istAt(2026, 11, 25, 20, 5)) // 11h open
	if len(st.closed) != 0 {
		t.Fatal("closed a session younger than MAX_SESSION_HOURS")
	}
}

func TestRemindersOneDMPerPersonAndIdempotent(t *testing.T) {
	st := newFakeJobStore()
	roster := fakeRoster{
		"A": {{ID: "both", Username: "both"}, {ID: "in", Username: "in"}, {ID: "bot", Username: "bot", IsBot: true}, {ID: "gone", Username: "gone", DeleteAt: 1}},
		"B": {{ID: "both", Username: "both"}, {ID: "untracked", Username: "untracked"}, {ID: "quiet", Username: "quiet"}},
	}
	st.members["untracked"] = MemberSettings{UserID: "untracked", Tracked: false, RemindersOn: true}
	st.members["quiet"] = MemberSettings{UserID: "quiet", Tracked: true, RemindersOn: false}
	st.sessions["in"] = []Session{{TeamID: "B", CheckIn: istAt(2026, 11, 25, 9, 0)}} // checked in elsewhere
	j, msg := newJobs(st, roster)
	now := istAt(2026, 11, 25, 10, 30)

	j.Reminders(context.Background(), now)
	j.Reminders(context.Background(), now) // restart: must not resend
	if len(msg.dms) != 1 || len(msg.dms["both"]) != 1 {
		t.Fatalf("want exactly one DM to 'both', got %v", msg.dms)
	}
}

func TestRemindersSkipWeekendAndGlobalHoliday(t *testing.T) {
	roster := fakeRoster{"A": {{ID: "u", Username: "u"}}}
	st := newFakeJobStore()
	j, msg := newJobs(st, roster)
	j.Reminders(context.Background(), istAt(2026, 11, 28, 10, 30)) // Saturday
	st2 := newFakeJobStore()
	st2.holidays = []Holiday{{Day: "2026-11-25", Name: "x"}}
	j2, msg2 := newJobs(st2, roster)
	j2.Reminders(context.Background(), istAt(2026, 11, 25, 10, 30))
	if len(msg.dms)+len(msg2.dms) != 0 {
		t.Fatalf("reminded on a day off: %v %v", msg.dms, msg2.dms)
	}
}

func istAt(y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, ist)
}
