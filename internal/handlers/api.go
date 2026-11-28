package handlers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
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
	PanelTasks(ctx context.Context, userID, teamID string) (attendance.TasksState, error)
	PanelPick(ctx context.Context, userID, teamID, issueURL string) (string, error)
	PanelEOD(ctx context.Context, userID, teamID, day string) (attendance.EODFormState, error)
	PanelSubmitEOD(ctx context.Context, userID, teamID string, fields map[string]string, submission map[string]any) (attendance.DialogResult, error)
	PanelHours(ctx context.Context, userID, teamID, rng string) (attendance.HoursState, error)
	PanelWFH(ctx context.Context, userID, teamID string) (attendance.WFHState, error)
	PanelRequestWFH(ctx context.Context, userID, teamID, start, end string, halfDay bool, reason string) (attendance.DialogResult, error)
	PanelCancelWFH(ctx context.Context, userID, teamID string, id int64) (string, error)
	PanelSettings(ctx context.Context, userID, teamID string) (attendance.SettingsState, error)
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
	mux.HandleFunc("GET /api/v1/tasks", h.api(h.apiTasks))
	mux.HandleFunc("POST /api/v1/tasks/pick", h.api(h.apiPick))
	mux.HandleFunc("GET /api/v1/eod", h.api(h.apiEOD))
	mux.HandleFunc("POST /api/v1/eod", h.api(h.apiSubmitEOD))
	mux.HandleFunc("GET /api/v1/hours", h.api(h.apiHours))
	mux.HandleFunc("GET /api/v1/wfh", h.api(h.apiWFH))
	mux.HandleFunc("POST /api/v1/wfh", h.api(h.apiRequestWFH))
	mux.HandleFunc("POST /api/v1/wfh/{id}/cancel", h.api(h.apiCancelWFH))
	mux.HandleFunc("GET /api/v1/settings", h.api(h.apiSettings))
}

var dayRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

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
		r.Body = http.MaxBytesReader(w, r.Body, 48<<10) // the EOD form: 3 × 2000 chars + its state
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

func (h *Handler) apiTasks(w http.ResponseWriter, r *http.Request, a apiRequest) {
	// GitHub can take a while; the bot's own GitHub calls stop at 8 s.
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	st, err := h.Panel.PanelTasks(ctx, a.userID, a.teamID)
	if h.apiFailed(w, err, "tasks", a) {
		return
	}
	apiJSON(w, http.StatusOK, st)
}

func (h *Handler) apiPick(w http.ResponseWriter, r *http.Request, a apiRequest) {
	var body struct {
		IssueURL string `json:"issue_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.IssueURL == "" || len(body.IssueURL) > 300 {
		apiError(w, http.StatusBadRequest, "Bad request.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	msg, err := h.Panel.PanelPick(ctx, a.userID, a.teamID, strings.TrimSpace(body.IssueURL))
	if h.apiFailed(w, err, "pick", a) {
		return
	}
	st, err := h.Panel.PanelTasks(ctx, a.userID, a.teamID)
	if h.apiFailed(w, err, "tasks", a) {
		return
	}
	h.Log.Info("api ok", "user_id", a.userID, "team_id", a.teamID, "action", "pick")
	apiJSON(w, http.StatusOK, map[string]any{"message": msg, "tasks": st})
}

func (h *Handler) apiEOD(w http.ResponseWriter, r *http.Request, a apiRequest) {
	day := r.URL.Query().Get("day")
	if day != "" && !dayRe.MatchString(day) {
		apiError(w, http.StatusBadRequest, "Bad day.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	st, err := h.Panel.PanelEOD(ctx, a.userID, a.teamID, day)
	if h.apiFailed(w, err, "eod", a) {
		return
	}
	apiJSON(w, http.StatusOK, st)
}

// eodSubmission is the panel's form: the signed state it was given, and the
// member's answers. Choices are keyed s0, s1, ... like the dialog's fields;
// Others are indexes into the state's "others" list.
type eodSubmission struct {
	State    string            `json:"state"`
	Choices  map[string]string `json:"choices"`
	Others   []string          `json:"others"`
	Did      string            `json:"did"`
	Blockers string            `json:"blockers"`
	Tomorrow string            `json:"tomorrow"`
}

func (h *Handler) apiSubmitEOD(w http.ResponseWriter, r *http.Request, a apiRequest) {
	var body eodSubmission
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, "Bad request.")
		return
	}
	// The task lists come only from the signed state, as in the dialog.
	var state map[string]string
	if err := json.Unmarshal([]byte(body.State), &state); err != nil {
		apiError(w, http.StatusBadRequest, "This form has expired. Please reopen it.")
		return
	}
	fields, err := h.Verifier.Verify(state)
	if err != nil {
		h.Log.Warn("api eod rejected: bad signature", "user_id", a.userID)
		apiError(w, http.StatusBadRequest, "This form has expired. Please reopen it.")
		return
	}
	sub := map[string]any{"did": body.Did, "blockers": body.Blockers, "tomorrow": body.Tomorrow}
	for k, v := range body.Choices {
		if len(k) < 2 || len(k) > 4 || k[0] != 's' {
			apiError(w, http.StatusBadRequest, "Bad request.")
			return
		}
		sub[k] = v
	}
	others := make([]any, 0, len(body.Others))
	for _, o := range body.Others {
		others = append(others, o)
	}
	sub["others"] = others

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	res, err := h.Panel.PanelSubmitEOD(ctx, a.userID, a.teamID, fields, sub)
	if h.apiFailed(w, err, "eod_submit", a) {
		return
	}
	if res.Error != "" || len(res.Errors) > 0 {
		apiJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": res.Error, "errors": res.Errors})
		return
	}
	st, err := h.Panel.PanelEOD(ctx, a.userID, a.teamID, fields["d"])
	if h.apiFailed(w, err, "eod", a) {
		return
	}
	h.Log.Info("api ok", "user_id", a.userID, "team_id", a.teamID, "action", "eod_submit")
	apiJSON(w, http.StatusOK, map[string]any{"eod": st})
}

func (h *Handler) apiHours(w http.ResponseWriter, r *http.Request, a apiRequest) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "today"
	}
	st, err := h.Panel.PanelHours(r.Context(), a.userID, a.teamID, rng)
	if errors.Is(err, attendance.ErrBadRange) {
		apiError(w, http.StatusBadRequest, "Use range=today, week or month.")
		return
	}
	if h.apiFailed(w, err, "hours", a) {
		return
	}
	apiJSON(w, http.StatusOK, st)
}

func (h *Handler) apiWFH(w http.ResponseWriter, r *http.Request, a apiRequest) {
	st, err := h.Panel.PanelWFH(r.Context(), a.userID, a.teamID)
	if h.apiFailed(w, err, "wfh", a) {
		return
	}
	apiJSON(w, http.StatusOK, st)
}

func (h *Handler) apiRequestWFH(w http.ResponseWriter, r *http.Request, a apiRequest) {
	var body struct {
		Start   string `json:"start_date"`
		End     string `json:"end_date"`
		HalfDay bool   `json:"half_day"`
		Reason  string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, "Bad request.")
		return
	}
	res, err := h.Panel.PanelRequestWFH(r.Context(), a.userID, a.teamID, body.Start, body.End, body.HalfDay, body.Reason)
	if h.apiFailed(w, err, "wfh_request", a) {
		return
	}
	if res.Error != "" || len(res.Errors) > 0 {
		apiJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": res.Error, "errors": res.Errors})
		return
	}
	st, err := h.Panel.PanelWFH(r.Context(), a.userID, a.teamID)
	if h.apiFailed(w, err, "wfh", a) {
		return
	}
	h.Log.Info("api ok", "user_id", a.userID, "team_id", a.teamID, "action", "wfh_request")
	apiJSON(w, http.StatusOK, map[string]any{"wfh": st})
}

func (h *Handler) apiCancelWFH(w http.ResponseWriter, r *http.Request, a apiRequest) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		apiError(w, http.StatusBadRequest, "Bad request.")
		return
	}
	msg, err := h.Panel.PanelCancelWFH(r.Context(), a.userID, a.teamID, id)
	if h.apiFailed(w, err, "wfh_cancel", a) {
		return
	}
	st, err := h.Panel.PanelWFH(r.Context(), a.userID, a.teamID)
	if h.apiFailed(w, err, "wfh", a) {
		return
	}
	h.Log.Info("api ok", "user_id", a.userID, "team_id", a.teamID, "action", "wfh_cancel")
	apiJSON(w, http.StatusOK, map[string]any{"message": msg, "wfh": st})
}

func (h *Handler) apiSettings(w http.ResponseWriter, r *http.Request, a apiRequest) {
	st, err := h.Panel.PanelSettings(r.Context(), a.userID, a.teamID)
	if h.apiFailed(w, err, "settings", a) {
		return
	}
	apiJSON(w, http.StatusOK, st)
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
