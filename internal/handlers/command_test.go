package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
	"github.com/vairabarath/Mattermost-attendance/internal/config"
)

type fakeCommander struct{ got *attendance.Command }

func (f *fakeCommander) Handle(_ context.Context, c attendance.Command) string {
	f.got = &c
	return "handled"
}

const (
	teamA = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	teamB = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
	hq    = "hhhhhhhhhhhhhhhhhhhhhhhhhh"
	user  = "uuuuuuuuuuuuuuuuuuuuuuuuuu"
)

func newHandler() (*Handler, *fakeCommander) {
	fc := &fakeCommander{}
	return &Handler{
		Teams: map[string]config.Team{
			teamA: {ID: teamA, Name: "InkYank", SlashToken: "token-a"},
			teamB: {ID: teamB, Name: "Seechanlabs", SlashToken: "token-b"},
		},
		Commander: fc,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, fc
}

func post(h http.Handler, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/mm/command", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func form(token, team string) url.Values {
	return url.Values{"token": {token}, "team_id": {team}, "user_id": {user}, "user_name": {"priya"}, "text": {"in hello"}}
}

func TestValidTokenRoutesCommand(t *testing.T) {
	h, fc := newHandler()
	rec := post(h.Routes(), form("token-a", teamA))
	if rec.Code != http.StatusOK || fc.got == nil {
		t.Fatalf("status %d, handled=%v", rec.Code, fc.got != nil)
	}
	if fc.got.TeamID != teamA || fc.got.Text != "in hello" || fc.got.UserID != user {
		t.Errorf("wrong command: %+v", fc.got)
	}
	var resp struct{ ResponseType, Text string }
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Text != "handled" {
		t.Errorf("reply: %+v", resp)
	}
}

func TestTokenFromOtherTeamRejected(t *testing.T) {
	h, fc := newHandler()
	rec := post(h.Routes(), form("token-a", teamB))
	if rec.Code != http.StatusUnauthorized || fc.got != nil {
		t.Fatalf("expected 401 and no command, got %d handled=%v", rec.Code, fc.got != nil)
	}
}

func TestWrongTokenRejected(t *testing.T) {
	h, fc := newHandler()
	rec := post(h.Routes(), form("nope", teamA))
	if rec.Code != http.StatusUnauthorized || fc.got != nil {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestUnknownTeamGetsFriendlyMessage(t *testing.T) {
	h, fc := newHandler()
	rec := post(h.Routes(), form("token-a", hq))
	if rec.Code != http.StatusOK || fc.got != nil || !strings.Contains(rec.Body.String(), "isn't used in this team") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestUnknownTeamUnknownTokenRejected(t *testing.T) {
	h, _ := newHandler()
	if rec := post(h.Routes(), form("nope", hq)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestBadUserIDRejected(t *testing.T) {
	h, fc := newHandler()
	f := form("token-a", teamA)
	f.Set("user_id", "../../etc")
	if rec := post(h.Routes(), f); rec.Code != http.StatusBadRequest || fc.got != nil {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
