package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

const testSecret = "0123456789abcdef0123456789abcdef-secret"

type fakePanel struct {
	gotUser, gotTeam, gotAction, gotText string
	err                                  error
	gotFields                            map[string]string
	gotSub                               map[string]any
	submitRes                            attendance.DialogResult
	gotHalf                              bool
	gotID                                int64
}

func (f *fakePanel) PanelTasks(_ context.Context, userID, teamID string) (attendance.TasksState, error) {
	f.gotUser, f.gotTeam = userID, teamID
	return attendance.TasksState{Configured: true, Linked: true, Tasks: []attendance.TaskItem{{Number: 7, Title: "Test pick B"}}}, f.err
}
func (f *fakePanel) PanelEOD(_ context.Context, userID, teamID, day string) (attendance.EODFormState, error) {
	f.gotUser, f.gotTeam, f.gotText = userID, teamID, day
	return attendance.EODFormState{Available: true, Day: "2026-11-27"}, f.err
}
func (f *fakePanel) PanelSubmitEOD(_ context.Context, userID, teamID string, fields map[string]string, sub map[string]any) (attendance.DialogResult, error) {
	f.gotUser, f.gotTeam, f.gotFields, f.gotSub = userID, teamID, fields, sub
	return f.submitRes, f.err
}
func (f *fakePanel) PanelHours(_ context.Context, userID, teamID, rng string) (attendance.HoursState, error) {
	f.gotUser, f.gotTeam, f.gotText = userID, teamID, rng
	if rng != "today" && rng != "week" && rng != "month" {
		return attendance.HoursState{}, attendance.ErrBadRange
	}
	return attendance.HoursState{Range: rng}, f.err
}
func (f *fakePanel) PanelWFH(_ context.Context, userID, teamID string) (attendance.WFHState, error) {
	return attendance.WFHState{Today: "2026-11-27"}, f.err
}
func (f *fakePanel) PanelRequestWFH(_ context.Context, userID, teamID, start, end string, half bool, reason string) (attendance.DialogResult, error) {
	f.gotText = start + "|" + end + "|" + reason
	f.gotHalf = half
	return f.submitRes, f.err
}
func (f *fakePanel) PanelCancelWFH(_ context.Context, userID, teamID string, id int64) (string, error) {
	f.gotID = id
	return "🚫 cancelled", f.err
}
func (f *fakePanel) PanelSettings(_ context.Context, userID, teamID string) (attendance.SettingsState, error) {
	return attendance.SettingsState{GitHubLogin: "vairabarath"}, f.err
}
func (f *fakePanel) PanelPick(_ context.Context, userID, teamID, url string) (string, error) {
	f.gotUser, f.gotTeam, f.gotAction, f.gotText = userID, teamID, "pick", url
	return "▶️ picked", f.err
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
	if rec, _ := apiCall(routes, "POST", "/api/v1/session/in?team="+teamA, testSecret, user, `{"text":"`+strings.Repeat("x", 50<<10)+`"}`); rec.Code != http.StatusBadRequest {
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

func TestAPITasks(t *testing.T) {
	h, _ := newHandler()
	fp := &fakePanel{}
	h.Panel, h.PluginSecret = fp, testSecret
	routes := h.Routes()

	rec, out := apiCall(routes, "GET", "/api/v1/tasks?team="+teamA, testSecret, user, "")
	if rec.Code != 200 || out["linked"] != true || len(out["tasks"].([]any)) != 1 {
		t.Fatalf("tasks: %d %v", rec.Code, out)
	}
	url := "https://github.com/o/r/issues/7"
	rec, out = apiCall(routes, "POST", "/api/v1/tasks/pick?team="+teamA, testSecret, user, `{"issue_url":"`+url+`"}`)
	if rec.Code != 200 || out["message"] != "▶️ picked" || fp.gotAction != "pick" || fp.gotText != url || out["tasks"] == nil {
		t.Fatalf("pick: %d %v %+v", rec.Code, out, fp)
	}
	for name, body := range map[string]string{
		"no url":   `{}`,
		"bad json": `{`,
		"too long": `{"issue_url":"` + strings.Repeat("x", 400) + `"}`,
	} {
		if rec, _ := apiCall(routes, "POST", "/api/v1/tasks/pick?team="+teamA, testSecret, user, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if rec, _ := apiCall(routes, "GET", "/api/v1/tasks?team="+teamA, "", user, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no secret: %d", rec.Code)
	}
}

func TestAPIEOD(t *testing.T) {
	h, _ := newHandler()
	fp := &fakePanel{}
	h.Panel, h.PluginSecret = fp, testSecret
	routes := h.Routes()

	if rec, out := apiCall(routes, "GET", "/api/v1/eod?team="+teamA+"&day=2026-11-27", testSecret, user, ""); rec.Code != 200 || out["available"] != true || fp.gotText != "2026-11-27" {
		t.Fatalf("get: %d %v", rec.Code, out)
	}
	if rec, _ := apiCall(routes, "GET", "/api/v1/eod?team="+teamA+"&day=yesterday", testSecret, user, ""); rec.Code != 400 {
		t.Errorf("bad day: %d", rec.Code)
	}

	// A state signed by the bot (as in GET /eod) is accepted and passed on verified.
	signed := mattermost.NewSigner("test-key", time.Hour).Sign(map[string]string{"k": "eod", "u": user, "t": teamA, "d": "2026-11-27", "items": "[]"})
	state, _ := json.Marshal(signed)
	body, _ := json.Marshal(map[string]any{"state": string(state), "choices": map[string]string{"s0": "Done"},
		"others": []string{"1"}, "did": "Finished the work", "blockers": "", "tomorrow": "More"})
	rec, out := apiCall(routes, "POST", "/api/v1/eod?team="+teamA, testSecret, user, string(body))
	if rec.Code != 200 || out["eod"] == nil {
		t.Fatalf("submit: %d %v", rec.Code, out)
	}
	if fp.gotFields["u"] != user || fp.gotFields["d"] != "2026-11-27" || fp.gotFields["sig"] != "" {
		t.Errorf("fields: %v", fp.gotFields)
	}
	if fp.gotSub["s0"] != "Done" || fp.gotSub["did"] != "Finished the work" || len(fp.gotSub["others"].([]any)) != 1 {
		t.Errorf("submission: %v", fp.gotSub)
	}

	// Tampered or forged state: refused before the service.
	fp.gotFields = nil
	signed["d"] = "2026-11-01"
	tampered, _ := json.Marshal(signed)
	for name, st := range map[string]string{"tampered": string(tampered), "not json": "{", "unsigned": `{"k":"eod","u":"` + user + `"}`} {
		b, _ := json.Marshal(map[string]any{"state": st, "did": "x"})
		if rec, _ := apiCall(routes, "POST", "/api/v1/eod?team="+teamA, testSecret, user, string(b)); rec.Code != 400 || fp.gotFields != nil {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	signed = mattermost.NewSigner("test-key", time.Hour).Sign(map[string]string{"k": "eod", "u": user, "t": teamA, "d": "2026-11-27"})
	state, _ = json.Marshal(signed)
	b, _ := json.Marshal(map[string]any{"state": string(state), "choices": map[string]string{"did": "x"}})
	if rec, _ := apiCall(routes, "POST", "/api/v1/eod?team="+teamA, testSecret, user, string(b)); rec.Code != 400 {
		t.Errorf("choice key that isn't sN: %d", rec.Code)
	}

	// Validation errors come back per field.
	fp.submitRes = attendance.DialogResult{Errors: map[string]string{"did": "Write at least 10 characters."}}
	b, _ = json.Marshal(map[string]any{"state": string(state), "did": "short"})
	rec, out = apiCall(routes, "POST", "/api/v1/eod?team="+teamA, testSecret, user, string(b))
	if rec.Code != http.StatusUnprocessableEntity || out["errors"].(map[string]any)["did"] == nil {
		t.Errorf("validation: %d %v", rec.Code, out)
	}
}

func TestAPIPages(t *testing.T) {
	h, _ := newHandler()
	fp := &fakePanel{}
	h.Panel, h.PluginSecret = fp, testSecret
	routes := h.Routes()
	q := "?team=" + teamA

	if rec, out := apiCall(routes, "GET", "/api/v1/hours"+q+"&range=week", testSecret, user, ""); rec.Code != 200 || out["range"] != "week" {
		t.Errorf("hours: %d %v", rec.Code, out)
	}
	if rec, _ := apiCall(routes, "GET", "/api/v1/hours"+q, testSecret, user, ""); rec.Code != 200 || fp.gotText != "today" {
		t.Errorf("hours default: %d %q", rec.Code, fp.gotText)
	}
	if rec, _ := apiCall(routes, "GET", "/api/v1/hours"+q+"&range=year", testSecret, user, ""); rec.Code != 400 {
		t.Errorf("bad range: %d", rec.Code)
	}
	if rec, out := apiCall(routes, "GET", "/api/v1/wfh"+q, testSecret, user, ""); rec.Code != 200 || out["today"] != "2026-11-27" {
		t.Errorf("wfh: %d %v", rec.Code, out)
	}
	rec, out := apiCall(routes, "POST", "/api/v1/wfh"+q, testSecret, user, `{"start_date":"2026-12-01","end_date":"","half_day":true,"reason":"Plumber"}`)
	if rec.Code != 200 || out["wfh"] == nil || fp.gotText != "2026-12-01||Plumber" || !fp.gotHalf {
		t.Errorf("request: %d %v %q", rec.Code, out, fp.gotText)
	}
	fp.submitRes = attendance.DialogResult{Errors: map[string]string{"start_date": "Already requested."}}
	if rec, out := apiCall(routes, "POST", "/api/v1/wfh"+q, testSecret, user, `{"start_date":"2026-12-01"}`); rec.Code != 422 || out["errors"] == nil {
		t.Errorf("request errors: %d %v", rec.Code, out)
	}
	if rec, out := apiCall(routes, "POST", "/api/v1/wfh/12/cancel"+q, testSecret, user, ""); rec.Code != 200 || fp.gotID != 12 || out["message"] != "🚫 cancelled" {
		t.Errorf("cancel: %d %v", rec.Code, out)
	}
	for _, bad := range []string{"0", "-3", "x", "99999999999999999999"} {
		if rec, _ := apiCall(routes, "POST", "/api/v1/wfh/"+bad+"/cancel"+q, testSecret, user, ""); rec.Code != 400 {
			t.Errorf("cancel %q: %d", bad, rec.Code)
		}
	}
	if rec, out := apiCall(routes, "GET", "/api/v1/settings"+q, testSecret, user, ""); rec.Code != 200 || out["github_login"] != "vairabarath" {
		t.Errorf("settings: %d %v", rec.Code, out)
	}
	if rec, _ := apiCall(routes, "GET", "/api/v1/settings"+q, "", user, ""); rec.Code != 401 {
		t.Errorf("settings without secret: %d", rec.Code)
	}
}
