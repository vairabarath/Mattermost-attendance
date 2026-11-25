package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
	"github.com/vairabarath/Mattermost-attendance/internal/mattermost"
)

// action handles interactive button clicks. The button context is HMAC
// signed when the button is created; the click is only honoured if the
// signature is valid and the clicker is the user the button was made for.
func (h *Handler) action(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req mattermost.ActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	fields, err := h.Verifier.Verify(req.Context)
	if err != nil {
		h.Log.Warn("button rejected: bad signature", "user_id", req.UserID)
		writeJSON(w, mattermost.ActionResponse{EphemeralText: "This button has expired. Run the command again."})
		return
	}
	if !idRe.MatchString(req.UserID) || fields["u"] != req.UserID {
		h.Log.Warn("button rejected: user mismatch", "user_id", req.UserID)
		writeJSON(w, mattermost.ActionResponse{EphemeralText: "This button isn't yours."})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	reply := h.Commander.HandleButton(ctx, fields["a"], req.UserID)
	writeJSON(w, updateFor(reply))
}

// updateFor replaces the clicked post with the reply (and its buttons, if
// any — an empty attachments list removes the old buttons).
func updateFor(r attendance.Reply) mattermost.ActionResponse {
	atts := r.Attachments
	if atts == nil {
		atts = []mattermost.Attachment{}
	}
	return mattermost.ActionResponse{Update: &mattermost.PostUpdate{
		Message: r.Text,
		Props:   map[string]any{"attachments": atts},
	}}
}
