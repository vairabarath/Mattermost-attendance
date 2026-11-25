package attendance

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

type fakeStatusAPI struct {
	cur *mattermost.CustomStatus
}

func (f *fakeStatusAPI) GetCustomStatus(context.Context, string) (*mattermost.CustomStatus, error) {
	if f.cur == nil {
		return nil, nil
	}
	c := *f.cur
	return &c, nil
}
func (f *fakeStatusAPI) SetCustomStatus(_ context.Context, _ string, cs mattermost.CustomStatus) error {
	f.cur = &cs
	return nil
}
func (f *fakeStatusAPI) ClearCustomStatus(context.Context, string) error { f.cur = nil; return nil }

type fakeStatusStore struct{ saved []byte }

func (f *fakeStatusStore) SaveCustomStatus(_ context.Context, _ string, raw []byte) error {
	f.saved = raw
	return nil
}
func (f *fakeStatusStore) TakeSavedCustomStatus(context.Context, string) ([]byte, error) {
	r := f.saved
	f.saved = nil
	return r, nil
}

func newUpdater(api *fakeStatusAPI, st *fakeStatusStore) *StatusUpdater {
	return NewStatusUpdater(api, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func step(t *testing.T, u *StatusUpdater, kind StatusKind, saveOwn bool) {
	t.Helper()
	if err := u.apply(context.Background(), statusJob{"u", kind, saveOwn}); err != nil {
		t.Fatal(err)
	}
}

func TestOwnStatusSavedAndRestored(t *testing.T) {
	own := &mattermost.CustomStatus{Emoji: "palm_tree", Text: "Vacation soon"}
	api, st := &fakeStatusAPI{cur: own}, &fakeStatusStore{}
	u := newUpdater(api, st)

	step(t, u, StatusWorking, true)
	if api.cur.Text != "Working" || st.saved == nil {
		t.Fatalf("after in: cur=%+v saved=%s", api.cur, st.saved)
	}
	step(t, u, StatusOnBreak, false)
	if api.cur.Text != "On break" {
		t.Fatalf("after break: %+v", api.cur)
	}
	step(t, u, StatusOff, false)
	if api.cur == nil || api.cur.Text != "Vacation soon" || st.saved != nil {
		t.Fatalf("after out: cur=%+v saved=%s", api.cur, st.saved)
	}
}

func TestNoOwnStatusClearedOnOut(t *testing.T) {
	api, st := &fakeStatusAPI{}, &fakeStatusStore{}
	u := newUpdater(api, st)
	step(t, u, StatusWorkingFromHome, true)
	if api.cur.Text != "Working from home" || st.saved != nil {
		t.Fatalf("cur=%+v saved=%s", api.cur, st.saved)
	}
	step(t, u, StatusOff, false)
	if api.cur != nil {
		t.Fatalf("expected cleared, got %+v", api.cur)
	}
}

func TestExpiredOwnStatusNotRestored(t *testing.T) {
	own := &mattermost.CustomStatus{Emoji: "x", Text: "In a meeting", ExpiresAt: time.Now().Add(time.Hour)}
	api, st := &fakeStatusAPI{cur: own}, &fakeStatusStore{}
	u := newUpdater(api, st)
	step(t, u, StatusWorking, true)
	u.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	step(t, u, StatusOff, false)
	if api.cur != nil {
		t.Fatalf("expired status restored: %+v", api.cur)
	}
}

func TestStatusChangedByMemberIsKept(t *testing.T) {
	api, st := &fakeStatusAPI{}, &fakeStatusStore{}
	u := newUpdater(api, st)
	step(t, u, StatusWorking, true)
	api.cur = &mattermost.CustomStatus{Emoji: "pizza", Text: "Lunch"} // member changed it
	step(t, u, StatusOff, false)
	if api.cur == nil || api.cur.Text != "Lunch" {
		t.Fatalf("member's status overwritten: %+v", api.cur)
	}
}

func TestBotStatusNotSavedAsOwn(t *testing.T) {
	// e.g. a crash left our status set; it must not be saved as the member's.
	api, st := &fakeStatusAPI{cur: &mattermost.CustomStatus{Emoji: "coffee", Text: "On break"}}, &fakeStatusStore{}
	u := newUpdater(api, st)
	step(t, u, StatusWorking, true)
	if st.saved != nil {
		t.Fatalf("bot status saved as own: %s", st.saved)
	}
}
