package handlers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
	"github.com/vairabarath/Mattermost-attendance/internal/pluginbridge"
)

// PanelAPI is what the Attendance plugin's panel can do (see ui-plan.md).
type PanelAPI interface {
	Me(ctx context.Context, userID, teamID string) (attendance.MeState, error)
	Act(ctx context.Context, userID, teamID, action, text string) (string, error)
}

// apiRoutes registers /api/v1/* when the plugin secret is set. Only the
// Attendance plugin calls these, over the internal Docker network: every
// request must carry the shared secret and the user id the plugin got from
// Mattermost's session.
func (h *Handler) apiRoutes(mux *http.ServeMux) {
	if h.Panel == nil || len(h.PluginSecret) < pluginbridge.MinSecretLen {
		return
	}
	mux.HandleFunc("GET /api/v1/me", h.api(h.apiMe))
	mux.HandleFunc("POST /api/v1/session/{action}", h.api(h.apiSession))
}

type apiRequest struct {
	userID, teamID string
}

// api checks the secret, the user id and the team, then calls fn.
func (h *Handler) api(fn func(http.ResponseWriter, *http.Request, apiRequest)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get(pluginbridge.SecretHeader)
		if subtle.ConstantTimeCompare([]byte(got), []byte(h.PluginSecret)) != 1 {
			h.Log.Warn("api rejected: bad secret", "path", r.URL.Path)
			apiError(w, http.StatusUnauthorized, "Not allowed.")
			return
		}
		userID := r.Header.Get(pluginbridge.UserHeader)
		if !idRe.MatchString(userID) {
			apiError(w, http.StatusUnauthorized, "Not logged in.")
			return
		}
		teamID := r.URL.Query().Get("team")
		if _, ok := h.Teams[teamID]; !ok {
			apiError(w, http.StatusBadRequest, "Attendance isn't used in this team.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		fn(w, r, apiRequest{userID: userID, teamID: teamID})
	}
}

func (h *Handler) apiMe(w http.ResponseWriter, r *http.Request, a apiRequest) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	me, err := h.Panel.Me(ctx, a.userID, a.teamID)
	if h.apiFailed(w, err, "me", a) {
		return
	}
	apiJSON(w, http.StatusOK, me)
}

func (h *Handler) apiSession(w http.ResponseWriter, r *http.Request, a apiRequest) {
	action := r.PathValue("action")
	switch action {
	case attendance.ActionIn, attendance.ActionBreak, attendance.ActionBack, attendance.ActionOut:
	default:
		apiError(w, http.StatusNotFound, "Not found.")
		return
	}
	var body struct {
		Text string `json:"text"` // check-in note or check-out summary
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			apiError(w, http.StatusBadRequest, "Bad request.")
			return
		}
	}
	if !utf8.ValidString(body.Text) {
		apiError(w, http.StatusBadRequest, "Bad request.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	msg, err := h.Panel.Act(ctx, a.userID, a.teamID, action, strings.TrimSpace(body.Text))
	if h.apiFailed(w, err, "session_"+action, a) {
		return
	}
	me, err := h.Panel.Me(ctx, a.userID, a.teamID)
	if h.apiFailed(w, err, "me", a) {
		return
	}
	h.Log.Info("api ok", "user_id", a.userID, "team_id", a.teamID, "action", action)
	apiJSON(w, http.StatusOK, map[string]any{"message": msg, "me": me})
}

// apiFailed writes the error reply for err (if any) and reports whether it did.
func (h *Handler) apiFailed(w http.ResponseWriter, err error, what string, a apiRequest) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, attendance.ErrNotTeamMember):
		apiError(w, http.StatusForbidden, "You're not a member of this team.")
	default:
		h.Log.Error("api failed", "what", what, "user_id", a.userID, "team_id", a.teamID, "err", err)
		apiError(w, http.StatusInternalServerError, "Something went wrong. Please try again.")
	}
	return true
}

func apiJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	apiJSON(w, status, map[string]string{"error": msg})
}
