package attendance

import (
	"context"
	"fmt"
	"time"
)

// The end-of-day form in the panel. It shows exactly what the dialog shows
// (eodFormData) and submits through the dialog's code (submitEOD), with the
// task lists carried in the same signed state, so the panel can't change
// which issues a report covers.

// panelGitHubBudget: the panel has no trigger-id deadline, so GitHub gets
// longer than in the dialog.
const panelGitHubBudget = 6 * time.Second

// EODTaskRow is a task with a status row.
type EODTaskRow struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Repo   string `json:"repo"`
	Status string `json:"status"` // on the board now
	Choice string `json:"choice"` // pre-selected: "No change" or the status saved last time
}

// EODTaskRef is a task in the "already done" or "other tasks" lists.
type EODTaskRef struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Repo    string `json:"repo"`
	Status  string `json:"status,omitempty"`
	Checked bool   `json:"checked,omitempty"` // others: reported Done last time
}

// EODFormState is the panel's end-of-day form.
type EODFormState struct {
	// Available is false when there's no report to write (not worked in
	// this team today / yesterday before 11:00); Message says why.
	Available bool   `json:"available"`
	Message   string `json:"message,omitempty"`

	Team     TeamRef      `json:"team"`
	Day      string       `json:"day"`
	DayLabel string       `json:"day_label"`
	Note     string       `json:"note,omitempty"` // e.g. GitHub not linked / unreachable
	Omitted  int          `json:"omitted,omitempty"`
	Statuses []string     `json:"statuses"` // choices besides "No change"
	NoChange string       `json:"no_change"`
	Tasks    []EODTaskRow `json:"tasks"`
	Done     []EODTaskRef `json:"done"`
	Others   []EODTaskRef `json:"others"`

	Did      string `json:"did"`
	Blockers string `json:"blockers"`
	Tomorrow string `json:"tomorrow"`
	MinDid   int    `json:"min_did"`
	MaxText  int    `json:"max_text"`

	// Submitted reports: when, and how the GitHub sync is going.
	Submitted   bool       `json:"submitted"`
	SubmittedAt *time.Time `json:"submitted_at,omitempty"`
	SyncLine    string     `json:"sync_line,omitempty"`

	// State is the signed form state to send back with the submission.
	State string `json:"state"`
}

// PanelEOD is the end-of-day form for userID in teamID: for day (YYYY-MM-DD)
// if given, else the day /att eod submit would use.
func (s *Service) PanelEOD(ctx context.Context, userID, teamID, day string) (EODFormState, error) {
	if err := s.checkMember(ctx, userID, teamID); err != nil {
		return EODFormState{}, err
	}
	team := TeamRef{ID: teamID, Name: s.Teams.TeamName(teamID)}
	if day == "" {
		d, ok, err := s.eodDay(ctx, userID, teamID)
		if err != nil {
			return EODFormState{}, err
		}
		if !ok {
			return EODFormState{Team: team, Message: "You haven't worked in " + team.Name +
				" today, so there's nothing to report yet. The form opens when you check out."}, nil
		}
		day = d
	}
	if !dayRe.MatchString(day) || !s.eodDayOpen(day) {
		return EODFormState{Team: team, Message: s.eodClosedMsg(day)}, nil
	}

	f, err := s.eodFormData(ctx, userID, teamID, day, panelGitHubBudget)
	if err != nil {
		return EODFormState{}, err
	}
	st := EODFormState{
		Available: true, Team: team, Day: day, DayLabel: s.dayStart(day).Format("Mon 2 Jan"),
		Note: plainNote(f.note), Omitted: f.omitted,
		Statuses: eodStatuses, NoChange: noChange, MinDid: minDidLen, MaxText: maxEODText,
		Tasks: []EODTaskRow{}, Done: []EODTaskRef{}, Others: []EODTaskRef{},
	}
	prevStatus := map[string]string{}
	if f.prev != nil {
		for _, it := range f.prev.Items {
			prevStatus[it.IssueURL] = it.NewStatus
		}
		st.Did, st.Blockers, st.Tomorrow = f.prev.DidToday, f.prev.Blockers, f.prev.Tomorrow
		st.Submitted, st.SyncLine = true, syncLine(*f.prev)
		at := f.prev.UpdatedAt
		st.SubmittedAt = &at
	}
	for _, t := range f.selects {
		choice := noChange // same default as the dialog
		if ps, ok := prevStatus[t.URL]; ok && ps != t.Status {
			choice = ps
		}
		st.Tasks = append(st.Tasks, EODTaskRow{Number: t.Number, Title: t.Title, URL: t.URL, Repo: t.Repo, Status: t.Status, Choice: choice})
	}
	for _, t := range f.done {
		st.Done = append(st.Done, EODTaskRef{Number: t.Number, Title: t.Title, URL: t.URL, Repo: t.Repo})
	}
	for _, t := range f.others {
		st.Others = append(st.Others, EODTaskRef{Number: t.Number, Title: t.Title, URL: t.URL, Repo: t.Repo,
			Status: t.Status, Checked: prevStatus[t.URL] == StatusDone})
	}
	st.State = s.eodState(userID, teamID, day, f)
	return st, nil
}

// plainNote drops the dialog's markdown underscores.
func plainNote(n string) string {
	if len(n) > 2 && n[0] == '_' && n[len(n)-1] == '_' {
		return n[1 : len(n)-1]
	}
	return n
}

// PanelSubmitEOD submits the panel's form. fields is the verified (signed)
// form state; it must be an end-of-day form for this user and team.
func (s *Service) PanelSubmitEOD(ctx context.Context, userID, teamID string, fields map[string]string, submission map[string]any) (DialogResult, error) {
	if err := s.checkMember(ctx, userID, teamID); err != nil {
		return DialogResult{}, err
	}
	if fields["k"] != dlgEOD || fields["u"] != userID || fields["t"] != teamID {
		return DialogResult{Error: "This form isn't yours or has expired. Please reopen it."}, nil
	}
	res, err := s.submitEOD(ctx, DialogSubmit{Fields: fields, UserID: userID, Submission: submission})
	if err != nil {
		return DialogResult{}, fmt.Errorf("submit eod: %w", err)
	}
	return res, nil
}
