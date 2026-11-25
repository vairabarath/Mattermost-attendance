// Package handlers holds the HTTP endpoints Mattermost calls.
package handlers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
	"github.com/vairabarath/Mattermost-attendance/internal/config"
	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

// Commander executes a verified slash command and returns reply text.
type Commander interface {
	Handle(ctx context.Context, c attendance.Command) string
}

// Pinger checks a dependency for /healthz.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Handler serves the Mattermost-facing endpoints.
type Handler struct {
	Teams     map[string]config.Team
	Commander Commander
	DB        Pinger
	Log       *slog.Logger
}

// Mattermost ids are 26 lowercase alphanumerics.
var idRe = regexp.MustCompile(`^[a-z0-9]{26}$`)

func (h *Handler) notAttendanceTeam() string {
	names := make([]string, 0, len(h.Teams))
	for _, t := range h.Teams {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return "Attendance isn't used in this team. Run `/att` from your organization's team (" +
		strings.Join(names, ", ") + ")."
}

// Routes registers the endpoints on a new mux.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mm/command", h.command)
	mux.HandleFunc("GET /healthz", h.healthz)
	return h.logRequests(mux)
}

func (h *Handler) command(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	token := r.PostForm.Get("token")
	teamID := r.PostForm.Get("team_id")
	userID := r.PostForm.Get("user_id")

	team, known := h.Teams[teamID]
	if !known {
		// Only answer if the token belongs to one of our commands, so random
		// callers learn nothing.
		if !h.anyTokenMatches(token) {
			h.Log.Warn("slash command rejected: unknown team and token", "team_id", teamID)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeReply(w, h.notAttendanceTeam())
		return
	}
	// The token must belong to THIS team: team A's token with team B's id
	// is rejected.
	if !tokenEqual(token, team.SlashToken) {
		h.Log.Warn("slash command rejected: token does not match team", "team_id", teamID)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !idRe.MatchString(userID) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	reply := h.Commander.Handle(ctx, attendance.Command{
		TeamID:   teamID,
		UserID:   userID,
		UserName: r.PostForm.Get("user_name"),
		Text:     strings.TrimSpace(r.PostForm.Get("text")),
	})
	writeReply(w, reply)
}

func (h *Handler) anyTokenMatches(token string) bool {
	match := false
	for _, t := range h.Teams {
		if tokenEqual(token, t.SlashToken) {
			match = true
		}
	}
	return match
}

// tokenEqual compares in constant time; empty never matches.
func tokenEqual(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func writeReply(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(mattermost.CommandResponse{ResponseType: "ephemeral", Text: text})
}

func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.DB.Ping(ctx); err != nil {
		h.Log.Error("healthz: db ping failed", "err", err)
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ok"))
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// logRequests logs method, path, status and duration — never bodies, which
// carry tokens and personal notes.
func (h *Handler) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" && rec.status == http.StatusOK {
			return
		}
		h.Log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"ms", time.Since(start).Milliseconds())
	})
}
