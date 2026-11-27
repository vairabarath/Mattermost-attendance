package attendance

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Phase 3: after a report is saved, its status changes are written to
// GitHub and the member's GitHub activity for the day is attached. This
// runs in the background (never blocking the form), is retried by a job,
// and never loses the report: a failure only marks sync_status 'failed'.

const (
	maxSyncAttempts = 6               // the submit-time try + 5 retries (15 min apart)
	syncLease       = 5 * time.Minute // one sync of a report at a time
	syncWindow      = 72 * time.Hour  // older unsynced reports are left alone
	commentDidLen   = 300
	notAssignedNote = " skipped: not assigned to you"
)

// ErrNotAssigned refuses a GitHub write for an issue that isn't assigned
// to the member (spec §11).
var ErrNotAssigned = errors.New("issue not assigned to member")

// Activity is a member's GitHub activity on one local day (gh_activity).
type Activity struct {
	Commits      int            `json:"commits"` // on default branches
	CommitRepos  map[string]int `json:"commit_repos,omitempty"`
	Branches     []BranchWork   `json:"branches,omitempty"`
	IssuesClosed int            `json:"issues_closed"`
	PRsOpened    int            `json:"prs_opened"`
	PRsMerged    int            `json:"prs_merged"`
	Reviews      int            `json:"reviews"`
	// ClosedIssues are the URLs of the issues counted in IssuesClosed, as
	// far as GitHub's search lists them.
	ClosedIssues []string `json:"closed_issues,omitempty"`
}

// BranchWork is a `<issue>-…` branch the member committed to that day.
type BranchWork struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Issue  int    `json:"issue"`
}

// TaskWriter writes EOD results to GitHub (implemented by internal/github).
type TaskWriter interface {
	// ApplyEODStatus checks the issue is assigned to login (else
	// ErrNotAssigned), sets its Project Status (closing the issue for
	// Done), adds or removes the "blocked" label, and posts comment.
	// closed reports that this call closed the issue (also on a later error).
	ApplyEODStatus(ctx context.Context, teamID, login, issueURL, status, comment string) (closed bool, err error)
	// Activity is login's activity in teamID's org on the local day
	// starting at dayStart.
	Activity(ctx context.Context, teamID, login string, dayStart time.Time) (*Activity, error)
}

// EODSyncStore is the persistence for syncing.
type EODSyncStore interface {
	// ClaimEODSync takes a lease on an unsynced report and counts an
	// attempt; nil when it's synced or another sync holds it.
	ClaimEODSync(ctx context.Context, id int64, lease time.Duration) (*EODReport, error)
	MarkEODItemSynced(ctx context.Context, reportID int64, issueURL string) error
	// FinishEODSync records the outcome and releases the lease; a nil
	// activity keeps the stored one.
	FinishEODSync(ctx context.Context, id int64, status, syncErr string, act *Activity) error
	// PendingEODSyncs lists unsynced, unclaimed reports updated since,
	// with fewer than maxAttempts attempts.
	PendingEODSyncs(ctx context.Context, maxAttempts int, since time.Time) ([]int64, error)
	GetEODReportByID(ctx context.Context, id int64) (*EODReport, error)
	// EnsureEODBlockerPost is EnsureEODReply for the admin alert post.
	EnsureEODBlockerPost(ctx context.Context, reportID int64, create func() (string, error)) (string, bool, error)
}

var branchRe = regexp.MustCompile(`^(\d+)-`)

// BranchIssue is the issue number a branch is named after (`42-fix` → 42),
// or 0 when the name doesn't start with a number and a hyphen.
func BranchIssue(name string) int {
	m := branchRe.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n == 0 {
		return 0
	}
	return n
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// RenderActivity is the report's "GitHub:" line, or "" when there was no
// activity. PR and review counts appear only when non-zero.
func RenderActivity(a *Activity) string {
	if a == nil {
		return ""
	}
	var parts []string
	if a.Commits > 0 {
		parts = append(parts, plural(a.Commits, "commit", "commits")+" to default branches")
	}
	if a.IssuesClosed > 0 {
		parts = append(parts, plural(a.IssuesClosed, "issue", "issues")+" closed")
	}
	seen := map[int]bool{}
	var nums []int
	for _, b := range a.Branches {
		if b.Issue > 0 && !seen[b.Issue] {
			seen[b.Issue] = true
			nums = append(nums, b.Issue)
		}
	}
	sort.Ints(nums)
	if len(nums) > 0 {
		var s []string
		for _, n := range nums {
			s = append(s, strconv.Itoa(n))
		}
		label := "branches: "
		if len(nums) == 1 {
			label = "branch: "
		}
		parts = append(parts, label+strings.Join(s, ", "))
	}
	if a.PRsOpened > 0 {
		parts = append(parts, plural(a.PRsOpened, "PR", "PRs")+" opened")
	}
	if a.PRsMerged > 0 {
		parts = append(parts, plural(a.PRsMerged, "PR", "PRs")+" merged")
	}
	if a.Reviews > 0 {
		parts = append(parts, plural(a.Reviews, "review", "reviews"))
	}
	if len(parts) == 0 {
		return ""
	}
	return "GitHub: " + strings.Join(parts, " · ")
}

// githubMention keeps text posted to GitHub from @-mentioning GitHub users.
func githubSafe(s string) string { return strings.ReplaceAll(s, "@", "@​") }

// eodComment is the issue comment for a status change (spec §7.4.ii).
func eodComment(day, mmUsername, status, did string) string {
	return fmt.Sprintf("📝 EOD %s (`@%s`): **%s** — %s", day, mmUsername, status, githubSafe(clip(did, commentDidLen)))
}

// --- running syncs ---

// StartEODSync starts the background worker that syncs reports right
// after they're submitted.
func (s *Service) StartEODSync(ctx context.Context) {
	s.eodQueue = make(chan int64, 100)
	go func() {
		for {
			select {
			case id := <-s.eodQueue:
				s.SyncEOD(ctx, id)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (s *Service) enqueueEODSync(id int64) {
	if s.eodQueue == nil {
		return
	}
	select {
	case s.eodQueue <- id:
	default: // full: the retry job will pick it up
		s.Log.Warn("eod: sync queue full; left for the retry job", "report_id", id)
	}
}

// RetryEODSyncs is the 15-minute job: it syncs reports still pending or
// failed.
func (s *Service) RetryEODSyncs(ctx context.Context) {
	ids, err := s.Store.PendingEODSyncs(ctx, maxSyncAttempts, s.Now().Add(-syncWindow))
	if err != nil {
		s.Log.Error("eod: list pending syncs", "err", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		s.SyncEOD(ctx, id)
	}
}

// SyncEOD writes one report's changes to GitHub and attaches the day's
// activity, then refreshes the report's thread reply.
func (s *Service) SyncEOD(ctx context.Context, id int64) {
	log := s.Log.With("report_id", id)
	r, err := s.Store.ClaimEODSync(ctx, id, syncLease)
	if err != nil {
		log.Error("eod sync: claim", "err", err)
		return
	}
	if r == nil {
		return // synced already, or another sync is running
	}
	log = log.With("team_id", r.TeamID, "user_id", r.UserID, "attempt", r.SyncAttempts)

	ms, err := s.Store.GetMember(ctx, r.UserID)
	if err != nil || ms == nil {
		s.finishSync(ctx, log, r, "failed", fmt.Sprintf("member lookup: %v", err), nil)
		return
	}
	if s.Writer == nil || s.Tasks == nil || !s.Tasks.Configured(r.TeamID) || ms.GitHubLogin == "" {
		s.finishSync(ctx, log, r, "synced", "", nil) // nothing to write
		return
	}

	var errs, notes, closedNow []string
	// Keep notes about refused issues from earlier attempts: those items
	// are settled and won't be tried again.
	for _, part := range strings.Split(r.SyncError, "; ") {
		if strings.HasSuffix(part, notAssignedNote) {
			notes = append(notes, part)
		}
	}
	for _, it := range r.Items {
		if it.Synced {
			continue
		}
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		closed, err := s.Writer.ApplyEODStatus(wctx, r.TeamID, ms.GitHubLogin, it.IssueURL, it.NewStatus,
			eodComment(r.Day, ms.Username, it.NewStatus, r.DidToday))
		cancel()
		if closed {
			closedNow = append(closedNow, it.IssueURL)
		}
		switch {
		case errors.Is(err, ErrNotAssigned):
			log.Warn("eod sync: refused, issue not assigned to member", "issue", it.IssueURL)
			notes = append(notes, issueRef(it.IssueURL)+notAssignedNote)
		case err != nil:
			log.Warn("eod sync: item failed", "issue", it.IssueURL, "err", err)
			errs = append(errs, issueRef(it.IssueURL)+": "+err.Error())
			continue
		}
		if err := s.Store.MarkEODItemSynced(ctx, r.ID, it.IssueURL); err != nil {
			errs = append(errs, err.Error())
		}
	}

	actx, cancel := context.WithTimeout(ctx, 60*time.Second)
	act, err := s.Writer.Activity(actx, r.TeamID, ms.GitHubLogin, s.dayStart(r.Day))
	cancel()
	if err != nil {
		log.Warn("eod sync: activity failed", "err", err)
		errs = append(errs, "activity: "+err.Error())
		act = nil
	}
	addJustClosed(act, closedNow)

	status := "synced"
	if len(errs) > 0 {
		status = "failed"
	}
	s.finishSync(ctx, log, r, status, strings.Join(append(errs, notes...), "; "), act)
}

func (s *Service) finishSync(ctx context.Context, log interface {
	Info(string, ...any)
	Error(string, ...any)
}, r *EODReport, status, syncErr string, act *Activity) {
	if err := s.Store.FinishEODSync(ctx, r.ID, status, clip(syncErr, 1000), act); err != nil {
		log.Error("eod sync: save result", "err", err)
		return
	}
	log.Info("eod sync done", "status", status)
	if status == "failed" && r.SyncAttempts >= maxSyncAttempts {
		s.alertSyncGaveUp(ctx, r, syncErr)
	}
	// Refresh the thread reply with the GitHub line.
	fresh, err := s.Store.GetEODReportByID(ctx, r.ID)
	if err != nil || fresh == nil || fresh.ReplyPostID == "" || s.Posts == nil {
		return
	}
	msg, err := s.renderEODForPost(ctx, *fresh)
	if err == nil {
		err = s.Posts.UpdatePost(ctx, fresh.ReplyPostID, msg, nil)
	}
	if err != nil {
		log.Error("eod sync: refresh reply", "err", err)
	}
}

// alertSyncGaveUp tells admins once, after the last attempt failed.
func (s *Service) alertSyncGaveUp(ctx context.Context, r *EODReport, syncErr string) {
	ch := s.TeamCfg[r.TeamID].AdminChannelID
	if ch == "" || s.Posts == nil {
		return
	}
	name := r.UserID
	if ms, err := s.Store.GetMember(ctx, r.UserID); err == nil && ms != nil {
		name = ms.Username
	}
	msg := fmt.Sprintf("⚠️ Couldn't sync @%s's end-of-day report for %s to GitHub after %d tries. The report is saved; "+
		"statuses on the Project may need updating by hand.\n> %s",
		name, s.dayStart(r.Day).Format("Mon 2 Jan"), maxSyncAttempts, clip(syncErr, 300))
	if _, err := s.Posts.CreatePostWithButtons(ctx, ch, msg, nil); err != nil {
		s.Log.Error("eod: sync alert failed", "report_id", r.ID, "err", err)
	}
}

// --- blocker alerts (spec §7.4.v) ---

// alertBlockers posts (or edits) an alert in #attendance-admin when the
// report has blockers or a Blocked task, with a link to the report.
func (s *Service) alertBlockers(ctx context.Context, r EODReport, username string) error {
	ch := s.TeamCfg[r.TeamID].AdminChannelID
	if ch == "" || s.Posts == nil {
		return nil
	}
	var blocked []string
	for _, it := range r.Items {
		if it.NewStatus == StatusBlocked {
			blocked = append(blocked, fmt.Sprintf("[%s](%s) %s", issueRef(it.IssueURL), it.IssueURL, mdEscape(it.Title)))
		}
	}
	if strings.TrimSpace(r.Blockers) == "" && len(blocked) == 0 {
		if r.BlockerPostID != "" { // blockers were removed on resubmission
			return s.Posts.UpdatePost(ctx, r.BlockerPostID, fmt.Sprintf("✅ @%s's end-of-day report for %s no longer lists blockers.",
				username, s.dayStart(r.Day).Format("Mon 2 Jan")), nil)
		}
		return nil
	}
	link := ""
	if r.ReplyPostID != "" {
		if pl, err := s.Posts.Permalink(ctx, r.TeamID, r.ReplyPostID); err == nil {
			link = " — [view report](" + pl + ")"
		}
	}
	msg := fmt.Sprintf("🚧 **@%s** reported a blocker (%s)%s", username, s.dayStart(r.Day).Format("Mon 2 Jan"), link)
	if b := safeText(r.Blockers); b != "" {
		msg += "\n> " + strings.ReplaceAll(clip(b, 500), "\n", "\n> ")
	}
	if len(blocked) > 0 {
		msg += "\nBlocked: " + strings.Join(blocked, ", ")
	}
	postID, created, err := s.Store.EnsureEODBlockerPost(ctx, r.ID, func() (string, error) {
		return s.Posts.CreatePostWithButtons(ctx, ch, msg, nil)
	})
	if err != nil || created {
		return err
	}
	return s.Posts.UpdatePost(ctx, postID, msg, nil)
}

// syncLine describes the report's GitHub sync for `/att eod`.
func syncLine(r EODReport) string {
	switch r.SyncStatus {
	case "synced":
		if r.SyncError != "" {
			return "GitHub: ✅ synced (" + r.SyncError + ")"
		}
		return "GitHub: ✅ synced"
	case "failed":
		if r.SyncAttempts >= maxSyncAttempts {
			return fmt.Sprintf("GitHub: ⚠️ sync failed after %d tries (admins were told): %s", r.SyncAttempts, clip(r.SyncError, 200))
		}
		return fmt.Sprintf("GitHub: ⚠️ sync failed (try %d of %d, retrying every 15 min): %s", r.SyncAttempts, maxSyncAttempts, clip(r.SyncError, 200))
	}
	return "GitHub: ⏳ syncing…"
}

// addJustClosed counts issues this sync closed that GitHub's search doesn't
// list yet: its index lags a few seconds behind a close. Only when the search
// listed every closed issue, so a missing URL can't be one past the page.
func addJustClosed(act *Activity, urls []string) {
	if act == nil || len(act.ClosedIssues) != act.IssuesClosed {
		return
	}
	for _, u := range urls {
		if !slices.ContainsFunc(act.ClosedIssues, func(c string) bool { return strings.EqualFold(c, u) }) {
			act.ClosedIssues = append(act.ClosedIssues, u)
			act.IssuesClosed++
		}
	}
}
