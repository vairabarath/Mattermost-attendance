package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

// The bridge between the panel and the bot (see ../../ui-plan.md):
//   - /api/v1/* from the panel is forwarded to the bot with the user id
//     Mattermost verified and the shared secret;
//   - /internal/notify from the bot (shared secret) becomes a websocket
//     event for that one user, so their panel refreshes.

const (
	secretHeader = "X-Attendance-Secret"
	userHeader   = "X-Attendance-User"

	maxRequestBody  = 64 << 10
	maxResponseBody = 2 << 20
)

// liveEvents are the events the bot may send; anything else is refused.
var liveEvents = map[string]bool{
	"session_changed": true,
	"tasks_changed":   true,
	"eod_changed":     true,
	"wfh_changed":     true,
	"team_changed":    true,
}

var idRe = regexp.MustCompile(`^[a-z0-9]{26}$`)

// botClient talks to the bot; it never follows redirects (overridden in tests).
var botClient = &http.Client{
	Timeout:       15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// logWarn logs through Mattermost when the plugin is running.
func (p *Plugin) logWarn(msg string, kv ...any) {
	if p.API != nil {
		p.API.LogWarn(msg, kv...)
	}
}

// proxy forwards a panel request to the bot.
func (p *Plugin) proxy(w http.ResponseWriter, r *http.Request, userID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Not allowed."})
		return
	}
	// Only /api/v1/... reaches the bot, after resolving any "..".
	clean := path.Clean(r.URL.Path)
	if !strings.HasPrefix(clean, "/api/v1/") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not found."})
		return
	}
	cfg := p.getConfiguration()
	if err := cfg.validate(); err != nil {
		p.logWarn("Attendance panel request while the plugin isn't set up", "reason", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "The Attendance plugin isn't connected yet. Ask a system admin."})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "Request too large."})
		return
	}
	target := strings.TrimRight(cfg.BotURL, "/") + clean
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Bad request."})
		return
	}
	// Only these headers reach the bot: never anything the browser sent.
	req.Header.Set(secretHeader, cfg.Secret)
	req.Header.Set(userHeader, userID)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := botClient.Do(req)
	if err != nil {
		p.logWarn("Attendance bot unreachable", "err", err.Error())
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Couldn't reach the attendance service. Please try again."})
		return
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Couldn't read the attendance service's answer."})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(out)
}

// notify turns a bot event into a websocket event for one user.
func (p *Plugin) notify(w http.ResponseWriter, r *http.Request) {
	cfg := p.getConfiguration()
	got := r.Header.Get(secretHeader)
	if cfg.Secret == "" || subtle.ConstantTimeCompare([]byte(got), []byte(cfg.Secret)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Not allowed."})
		return
	}
	var e struct {
		UserID string `json:"user_id"`
		Event  string `json:"event"`
		TeamID string `json:"team_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&e); err != nil ||
		!idRe.MatchString(e.UserID) || !liveEvents[e.Event] || (e.TeamID != "" && !idRe.MatchString(e.TeamID)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Bad event."})
		return
	}
	p.publish(e.Event, map[string]any{"team_id": e.TeamID}, e.UserID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
