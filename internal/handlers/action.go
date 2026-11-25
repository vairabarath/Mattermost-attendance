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
// signature is valid. Personal buttons (context "u") only work for that
// user; team buttons (context "t", e.g. WFH approve) carry no user and the
// service checks the clicker's admin rights for that team.
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
	personal := fields["u"] != ""
	if !idRe.MatchString(req.UserID) || (personal && fields["u"] != req.UserID) || (!personal && fields["t"] == "") {
		h.Log.Warn("button rejected: user mismatch", "user_id", req.UserID)
		writeJSON(w, mattermost.ActionResponse{EphemeralText: "This button isn't yours."})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	reply := h.Commander.HandleButton(ctx, attendance.Click{
		Action: fields["a"], UserID: req.UserID, Fields: fields, TriggerID: req.TriggerID,
	})
	if reply.EphemeralOnly {
		writeJSON(w, mattermost.ActionResponse{EphemeralText: reply.Text})
		return
	}
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

// dialog handles interactive dialog submissions. The dialog state is a
// signed JSON object created when the dialog was opened, bound to the user.
func (h *Handler) dialog(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var sub mattermost.DialogSubmission
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if sub.Cancelled {
		writeJSON(w, struct{}{})
		return
	}
	expired := mattermost.DialogResponse{Error: "This form has expired. Please start again."}
	var state map[string]string
	if err := json.Unmarshal([]byte(sub.State), &state); err != nil {
		writeJSON(w, expired)
		return
	}
	fields, err := h.Verifier.Verify(state)
	if err != nil {
		h.Log.Warn("dialog rejected: bad signature", "user_id", sub.UserID)
		writeJSON(w, expired)
		return
	}
	if !idRe.MatchString(sub.UserID) || fields["u"] != sub.UserID {
		h.Log.Warn("dialog rejected: user mismatch", "user_id", sub.UserID)
		writeJSON(w, mattermost.DialogResponse{Error: "This form isn't yours."})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	res := h.Commander.HandleDialog(ctx, attendance.DialogSubmit{
		Fields: fields, UserID: sub.UserID, ChannelID: sub.ChannelID, Submission: sub.Submission,
	})
	if res.Error == "" && len(res.Errors) == 0 {
		writeJSON(w, struct{}{})
		return
	}
	writeJSON(w, mattermost.DialogResponse{Error: res.Error, Errors: res.Errors})
}
