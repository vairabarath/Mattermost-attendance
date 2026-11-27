// Package pluginbridge connects the bot to the Attendance Mattermost plugin:
// the shared secret both sides use, and live-update notifications from the
// bot to the plugin (which pushes them to the user's panel).
package pluginbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// PluginID is the Attendance plugin's id (plugin/plugin.json).
const PluginID = "com.bittrix.attendance"

// SecretHeader carries the shared secret in both directions; UserHeader
// carries the Mattermost user id the plugin verified.
const (
	SecretHeader = "X-Attendance-Secret"
	UserHeader   = "X-Attendance-User"
)

// MinSecretLen is the shortest secret either side accepts.
const MinSecretLen = 32

type event struct {
	UserID string `json:"user_id"`
	Event  string `json:"event"`
	TeamID string `json:"team_id,omitempty"`
}

// Notifier sends live-update events to the plugin in the background, so
// callers never wait. Events are dropped when the queue is full or the
// plugin can't be reached (the panel also refreshes on its own).
type Notifier struct {
	url    string
	secret string
	http   *http.Client
	log    *slog.Logger
	q      chan event
}

// NewNotifier posts to <mmURL>/plugins/com.bittrix.attendance/internal/notify.
func NewNotifier(mmURL, secret string, log *slog.Logger) *Notifier {
	return &Notifier{
		url:    strings.TrimRight(mmURL, "/") + "/plugins/" + PluginID + "/internal/notify",
		secret: secret,
		http:   &http.Client{Timeout: 5 * time.Second},
		log:    log,
		q:      make(chan event, 256),
	}
}

// Start runs the sender until ctx ends.
func (n *Notifier) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-n.q:
				if err := n.send(ctx, e); err != nil {
					n.log.Debug("plugin notify failed", "event", e.Event, "err", err)
				}
			}
		}
	}()
}

// Notify queues an event for userID's panel; it never blocks.
func (n *Notifier) Notify(userID, ev, teamID string) {
	select {
	case n.q <- event{UserID: userID, Event: ev, TeamID: teamID}:
	default:
		n.log.Warn("plugin notify queue full; event dropped", "event", ev)
	}
}

func (n *Notifier) send(ctx context.Context, e event) error {
	body, _ := json.Marshal(e)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(SecretHeader, n.secret)
	resp, err := n.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("plugin answered %d", resp.StatusCode)
	}
	return nil
}
