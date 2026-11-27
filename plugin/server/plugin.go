package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
)

// Plugin is the server part of the Attendance plugin: a thin, authenticated
// bridge between the panel in the Mattermost web/desktop app and the
// attendance bot. All attendance rules live in the bot.
type Plugin struct {
	plugin.MattermostPlugin

	configurationLock sync.RWMutex
	configuration     *configuration

	// publish sends a websocket event to one user (set in OnActivate;
	// replaced in tests).
	publish func(event string, payload map[string]any, userID string)
}

// OnActivate wires the websocket publisher.
func (p *Plugin) OnActivate() error {
	p.publish = func(event string, payload map[string]any, userID string) {
		p.API.PublishWebSocketEvent(event, payload, &model.WebsocketBroadcast{UserId: userID})
	}
	return nil
}

// ServeHTTP handles /plugins/com.bittrix.attendance/*. Mattermost sets the
// Mattermost-User-Id header only for logged-in users (and strips any value
// a client sends), so it's the only source of the user's identity.
func (p *Plugin) ServeHTTP(_ *plugin.Context, w http.ResponseWriter, r *http.Request) {
	// From the bot, over the internal network: authenticated by the secret.
	if r.URL.Path == "/internal/notify" && r.Method == http.MethodPost {
		p.notify(w, r)
		return
	}
	userID := r.Header.Get("Mattermost-User-Id")
	if userID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Not logged in."})
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/health":
		resp := map[string]any{"ok": true, "version": manifest.Version}
		if err := p.getConfiguration().validate(); err != nil {
			resp["ok"], resp["error"] = false, err.Error()
		}
		writeJSON(w, http.StatusOK, resp)
	case strings.HasPrefix(r.URL.Path, "/api/v1/"):
		p.proxy(w, r, userID)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not found."})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
