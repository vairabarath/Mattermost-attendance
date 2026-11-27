package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
)

const testSecret = "0123456789abcdef0123456789abcdef-secret"

type fakePanel struct {
	gotUser, gotTeam, gotAction, gotText string
	err                                  error
}

func (f *fakePanel) Me(_ context.Context, userID, teamID string) (attendance.MeState, error) {
	f.gotUser, f.gotTeam = userID, teamID
	return attendance.MeState{UserID: userID, Team: attendance.TeamRef{ID: teamID, Name: "InkYank"}}, f.err
}
func (f *fakePanel) Act(_ context.Context, userID, teamID, action, text string) (string, error) {
	f.gotUser, f.gotTeam, f.gotAction, f.gotText = userID, teamID, action, text
	return "✅ Checked in", f.err
}

func apiCall(h http.Handler, method, path, secret, userID, body string) (*httptest.ResponseRecorder, map[string]any) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if secret != "" {
		r.Header.Set("X-Attendance-Secret", secret)
	}
	if userID != "" {
		r.Header.Set("X-Attendance-User", userID)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestAPIOffWithoutSecret(t *testing.T) {
	h, _ := newHandler()
	h.Panel = &fakePanel{}
	if rec, _ := apiCall(h.Routes(), "GET", "/api/v1/me?team="+teamA, testSecret, user, ""); rec.Code != http.StatusNotFound {
		t.Errorf("no secret configured: %d", rec.Code)
	}
	h.PluginSecret = "too-short"
	if rec, _ := apiCall(h.Routes(), "GET", "/api/v1/me?team="+teamA, "too-short", user, ""); rec.Code != http.StatusNotFound {
		t.Errorf("short secret configured: %d", rec.Code)
	}
}

func TestAPIAuth(t *testing.T) {
	h, _ := newHandler()
	fp := &fakePanel{}
	h.Panel, h.PluginSecret = fp, testSecret
	routes := h.Routes()
	for name, c := range map[string]struct {
		secret, user, team string
		want               int
	}{
		"no secret":    {"", user, teamA, http.StatusUnauthorized},
		"wrong secret": {testSecret + "x", user, teamA, http.StatusUnauthorized},
		"no user":      {testSecret, "", teamA, http.StatusUnauthorized},
		"bad user id":  {testSecret, "../../etc", teamA, http.StatusUnauthorized},
		"unknown team": {testSecret, user, "zzzzzzzzzzzzzzzzzzzzzzzzzz", http.StatusBadRequest},
		"ok":           {testSecret, user, teamA, http.StatusOK},
	} {
		fp.gotUser = ""
		rec, _ := apiCall(routes, "GET", "/api/v1/me?team="+c.team, c.secret, c.user, "")
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d", name, rec.Code, c.want)
		}
		switch {
		case c.want != http.StatusOK && fp.gotUser != "":
			t.Errorf("%s: reached the service", name)
		case c.want == http.StatusOK && (fp.gotUser != user || fp.gotTeam != teamA):
			t.Errorf("%s: service got %q %q", name, fp.gotUser, fp.gotTeam)
		}
	}
}

func TestAPISession(t *testing.T) {
	h, _ := newHandler()
	fp := &fakePanel{}
	h.Panel, h.PluginSecret = fp, testSecret
	routes := h.Routes()

	rec, out := apiCall(routes, "POST", "/api/v1/session/in?team="+teamA, testSecret, user, `{"text":"  morning  "}`)
	me, _ := out["me"].(map[string]any)
	if rec.Code != 200 || out["message"] != "✅ Checked in" || me["user_id"] != user || fp.gotAction != "in" || fp.gotText != "morning" {
		t.Fatalf("in: %d %v %+v", rec.Code, out, fp)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("missing no-store")
	}
	// No body is fine for break/back/out.
	if rec, _ := apiCall(routes, "POST", "/api/v1/session/break?team="+teamA, testSecret, user, ""); rec.Code != 200 || fp.gotAction != "break" {
		t.Errorf("break: %d", rec.Code)
	}
	if rec, _ := apiCall(routes, "POST", "/api/v1/session/delete?team="+teamA, testSecret, user, ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown action: %d", rec.Code)
	}
	if rec, _ := apiCall(routes, "GET", "/api/v1/session/in?team="+teamA, testSecret, user, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET on an action: %d", rec.Code)
	}
	if rec, _ := apiCall(routes, "POST", "/api/v1/session/in?team="+teamA, testSecret, user, `{bad`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json: %d", rec.Code)
	}
	if rec, _ := apiCall(routes, "POST", "/api/v1/session/in?team="+teamA, testSecret, user, `{"text":"`+strings.Repeat("x", 20<<10)+`"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("oversized body: %d", rec.Code)
	}

	fp.err = attendance.ErrNotTeamMember
	if rec, out := apiCall(routes, "POST", "/api/v1/session/out?team="+teamA, testSecret, user, ""); rec.Code != http.StatusForbidden || !strings.Contains(out["error"].(string), "not a member") {
		t.Errorf("non-member: %d %v", rec.Code, out)
	}
	fp.err = errors.New("db down: password=hunter2")
	if rec, out := apiCall(routes, "GET", "/api/v1/me?team="+teamA, testSecret, user, ""); rec.Code != 500 || strings.Contains(out["error"].(string), "hunter2") {
		t.Errorf("internal error leaked: %d %v", rec.Code, out)
	}
}
