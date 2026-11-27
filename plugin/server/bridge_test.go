package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testUser   = "uuuuuuuuuuuuuuuuuuuuuuuuuu"
	testTeam   = "tttttttttttttttttttttttttt"
	testSecret = "0123456789abcdef0123456789abcdef-secret"
)

type botSeen struct {
	method, path, query, secret, user, body, cookie string
}

func newBridge(t *testing.T, status int, reply string) (*Plugin, *botSeen) {
	t.Helper()
	seen := &botSeen{}
	bot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*seen = botSeen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get(secretHeader), r.Header.Get(userHeader), string(b), r.Header.Get("Cookie")}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(bot.Close)
	p := &Plugin{configuration: &configuration{BotURL: bot.URL + "/", Secret: testSecret}}
	return p, seen
}

func call(p *Plugin, method, path, userID, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if userID != "" {
		r.Header.Set("Mattermost-User-Id", userID)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(nil, rec, r)
	return rec
}

func TestProxyForwardsWithIdentity(t *testing.T) {
	p, seen := newBridge(t, 200, `{"message":"ok"}`)
	rec := call(p, "POST", "/api/v1/session/in?team="+testTeam, testUser, `{"text":"hi"}`, map[string]string{
		// A browser can't choose who it is or forge the secret.
		userHeader: "aaaaaaaaaaaaaaaaaaaaaaaaaa", secretHeader: "forged", "Cookie": "MMAUTHTOKEN=session",
	})
	if rec.Code != 200 || rec.Body.String() != `{"message":"ok"}` || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("reply: %d %q", rec.Code, rec.Body.String())
	}
	want := botSeen{"POST", "/api/v1/session/in", "team=" + testTeam, testSecret, testUser, `{"text":"hi"}`, ""}
	if *seen != want {
		t.Errorf("bot saw %+v\nwant     %+v", *seen, want)
	}
}

func TestProxyPassesBotErrors(t *testing.T) {
	p, _ := newBridge(t, 403, `{"error":"You're not a member of this team."}`)
	if rec := call(p, "GET", "/api/v1/me?team="+testTeam, testUser, "", nil); rec.Code != 403 || !strings.Contains(rec.Body.String(), "not a member") {
		t.Errorf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestProxyRefusals(t *testing.T) {
	p, seen := newBridge(t, 200, `{}`)
	for name, c := range map[string]struct {
		method, path, user string
		want               int
	}{
		"not logged in":  {"GET", "/api/v1/me", "", 401},
		"dot-dot escape": {"GET", "/api/v1/../../mm/command", testUser, 404},
		"other method":   {"DELETE", "/api/v1/me", testUser, 405},
		"unknown path":   {"GET", "/admin", testUser, 404},
	} {
		seen.path = ""
		if rec := call(p, c.method, c.path, c.user, "", nil); rec.Code != c.want || seen.path != "" {
			t.Errorf("%s: %d (want %d), bot saw %q", name, rec.Code, c.want, seen.path)
		}
	}
	if rec := call(p, "POST", "/api/v1/session/in", testUser, strings.Repeat("x", maxRequestBody+1), nil); rec.Code != http.StatusRequestEntityTooLarge || seen.path != "" {
		t.Errorf("oversized: %d", rec.Code)
	}
	// Not set up: a clear answer, nothing sent.
	p.configuration = &configuration{}
	if rec := call(p, "GET", "/api/v1/me", testUser, "", nil); rec.Code != http.StatusServiceUnavailable || seen.path != "" {
		t.Errorf("unconfigured: %d", rec.Code)
	}
	// Bot down.
	p.configuration = &configuration{BotURL: "http://127.0.0.1:1", Secret: testSecret}
	if rec := call(p, "GET", "/api/v1/me", testUser, "", nil); rec.Code != http.StatusBadGateway {
		t.Errorf("bot down: %d", rec.Code)
	}
}

type published struct{ event, user, team string }

func TestNotify(t *testing.T) {
	p, _ := newBridge(t, 200, `{}`)
	var got []published
	p.publish = func(event string, payload map[string]any, userID string) {
		got = append(got, published{event, userID, payload["team_id"].(string)})
	}
	ok := `{"user_id":"` + testUser + `","event":"session_changed","team_id":"` + testTeam + `"}`
	if rec := call(p, "POST", "/internal/notify", "", ok, map[string]string{secretHeader: testSecret}); rec.Code != 200 {
		t.Fatalf("notify: %d %s", rec.Code, rec.Body.String())
	}
	if len(got) != 1 || got[0] != (published{"session_changed", testUser, testTeam}) {
		t.Fatalf("published %+v", got)
	}
	for name, c := range map[string]struct{ secret, body string }{
		"no secret":     {"", ok},
		"wrong secret":  {testSecret + "x", ok},
		"unknown event": {testSecret, `{"user_id":"` + testUser + `","event":"delete_all"}`},
		"bad user":      {testSecret, `{"user_id":"all","event":"session_changed"}`},
		"bad json":      {testSecret, `{`},
	} {
		h := map[string]string{}
		if c.secret != "" {
			h[secretHeader] = c.secret
		}
		if rec := call(p, "POST", "/internal/notify", "", c.body, h); rec.Code == 200 {
			t.Errorf("%s accepted", name)
		}
	}
	// Unconfigured plugin: an empty secret never matches.
	p.configuration = &configuration{}
	if rec := call(p, "POST", "/internal/notify", "", ok, map[string]string{secretHeader: ""}); rec.Code == 200 {
		t.Error("empty secret accepted")
	}
	if len(got) != 1 {
		t.Errorf("refused events published: %+v", got)
	}
	var e map[string]any
	_ = json.Unmarshal([]byte(ok), &e)
}
