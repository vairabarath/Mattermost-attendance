package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthNeedsLogin(t *testing.T) {
	p := &Plugin{}
	rec := httptest.NewRecorder()
	p.ServeHTTP(nil, rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous request: %d", rec.Code)
	}
}

func TestHealthReportsSetup(t *testing.T) {
	p := &Plugin{}
	get := func() map[string]any {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		r.Header.Set("Mattermost-User-Id", "u1")
		rec := httptest.NewRecorder()
		p.ServeHTTP(nil, rec, r)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	if out := get(); out["ok"] != false || !strings.Contains(out["error"].(string), "Bot URL") {
		t.Errorf("unconfigured: %v", out)
	}
	p.configuration = &configuration{BotURL: "http://attendance:8080", Secret: "short"}
	if out := get(); out["ok"] != false || !strings.Contains(out["error"].(string), "secret") {
		t.Errorf("short secret: %v", out)
	}
	p.configuration.Secret = strings.Repeat("x", 64)
	if out := get(); out["ok"] != true || out["version"] != manifest.Version {
		t.Errorf("configured: %v", out)
	}
}

func TestUnknownPath(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/nope", nil)
	r.Header.Set("Mattermost-User-Id", "u1")
	rec := httptest.NewRecorder()
	(&Plugin{}).ServeHTTP(nil, rec, r)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}
