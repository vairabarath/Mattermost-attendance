package attendance

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Errors from TaskSource.
var (
	ErrGitHubUserNotFound = errors.New("github user not found")
	ErrNotOrgMember       = errors.New("not a member of the github org")
	ErrGitHubLoginTaken   = errors.New("github login linked to another member")
	ErrGitHubNotSetUp     = errors.New("github not set up for this team")
)

// Project Status values (the GitHub Project field must use these names).
const (
	StatusTodo       = "Todo"
	StatusInProgress = "In progress"
	StatusInReview   = "In review"
	StatusBlocked    = "Blocked"
	StatusDone       = "Done"
)

// Task is an issue on a team's GitHub Project, assigned to a member.
type Task struct {
	Repo     string // owner/name
	Number   int
	Title    string
	URL      string
	Status   string // "" when unset on the project
	Priority string // High, Medium, Low or ""
	Due      string // YYYY-MM-DD or ""
	Updated  time.Time
	// Assignees: GitHub logins (set by TeamTaskSource.ProjectTasks only).
	Assignees []string
}

// Overdue reports whether the task's due date is before today (YYYY-MM-DD).
func (t Task) Overdue(today string) bool { return t.Due != "" && t.Due < today }

// TaskSource reads GitHub for the task features (implemented by internal/github).
type TaskSource interface {
	// Configured reports whether teamID has a GitHub org and Project.
	Configured(teamID string) bool
	// OrgName is the GitHub org of teamID.
	OrgName(teamID string) string
	// VerifyLogin returns the canonical login if the user exists and is a
	// member of teamID's GitHub org (ErrGitHubUserNotFound, ErrNotOrgMember).
	VerifyLogin(ctx context.Context, teamID, login string) (string, error)
	// AssignedTasks returns open issues assigned to login on teamID's
	// Project, excluding Status Done. Order is not defined.
	AssignedTasks(ctx context.Context, teamID, login string) ([]Task, error)
	// EODTasks returns, in one call, login's open tasks on teamID's Project
	// (as AssignedTasks) and the issues assigned to them on that Project
	// that were closed during the local day starting at dayStart.
	EODTasks(ctx context.Context, teamID, login string, dayStart time.Time) (open, closedToday []Task, err error)
}

var priorityRank = map[string]int{"High": 0, "Medium": 1, "Low": 2}

// SortTasks orders tasks: overdue first, then priority High→Low (unset
// last), then due date ascending (unset last), then repo and number.
func SortTasks(tasks []Task, today string) {
	rank := func(p string) int {
		if r, ok := priorityRank[p]; ok {
			return r
		}
		return len(priorityRank)
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		a, b := tasks[i], tasks[j]
		if ao, bo := a.Overdue(today), b.Overdue(today); ao != bo {
			return ao
		}
		if ra, rb := rank(a.Priority), rank(b.Priority); ra != rb {
			return ra < rb
		}
		if a.Due != b.Due {
			if a.Due == "" || b.Due == "" {
				return b.Due == ""
			}
			return a.Due < b.Due
		}
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		return a.Number < b.Number
	})
}

// maxTaskLines is how many tasks a list shows before "and N more".
const maxTaskLines = 10

// RenderTaskList renders sorted tasks as markdown lines:
// `#42 title — Status — Priority — due 3 Dec` with ⚠️ overdue, 🚧 blocked.
func RenderTaskList(tasks []Task, today string, loc *time.Location) string {
	multiRepo := false
	for _, t := range tasks {
		if t.Repo != tasks[0].Repo {
			multiRepo = true
		}
	}
	var b strings.Builder
	for i, t := range tasks {
		if i == maxTaskLines {
			fmt.Fprintf(&b, "_…and %d more._\n", len(tasks)-maxTaskLines)
			break
		}
		ref := fmt.Sprintf("#%d", t.Number)
		if multiRepo {
			ref = t.Repo[strings.LastIndex(t.Repo, "/")+1:] + ref
		}
		parts := []string{fmt.Sprintf("[%s](%s) %s", ref, t.URL, mdEscape(t.Title))}
		status := t.Status
		if status == "" {
			status = "No status"
		}
		parts = append(parts, status)
		if t.Priority != "" {
			parts = append(parts, t.Priority)
		}
		if t.Due != "" {
			due := t.Due
			if d, err := time.ParseInLocation(DayFormat, t.Due, loc); err == nil {
				due = d.Format("Mon 2 Jan")
			}
			parts = append(parts, "due "+due)
		}
		prefix := "- "
		if t.Overdue(today) {
			prefix += "⚠️ "
		}
		if t.Status == StatusBlocked {
			prefix += "🚧 "
		}
		b.WriteString(prefix + strings.Join(parts, " — ") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// mdEscape keeps issue titles (written on GitHub, by anyone who can edit
// the issue) from breaking markdown links and tables, or notifying a whole
// channel with @channel, @all or @here.
var mdEscaper = strings.NewReplacer("[", "\\[", "]", "\\]", "|", "\\|", "\n", " ", "\r", " ")

func mdEscape(s string) string { return safeText(mdEscaper.Replace(s)) }

// GitHub logins: letters, digits and single hyphens, 1–39 chars.
var githubLoginRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9]|-[A-Za-z0-9]){0,38}$`)

// ValidGitHubLogin reports whether s looks like a GitHub username.
func ValidGitHubLogin(s string) bool { return len(s) <= 39 && githubLoginRe.MatchString(s) }

const githubTimeout = 8 * time.Second

// --- /att github ---

const githubUsage = "Usage: `/att github` — show your linked GitHub login.\n" +
	"Admins link accounts: `/att member @user github <username>`."

// askAdminToLink is shown to members: only admins link GitHub logins, so
// nobody can claim someone else's account.
const (
	sendLoginToAdmin = "Send your GitHub username to an admin of this team; they'll run `/att member @you github <username>`."
	askAdminToLink   = "🔒 Only admins can link GitHub accounts, so nobody can claim someone else's. " + sendLoginToAdmin
)

func (s *Service) github(ctx context.Context, c Command, args string) (Reply, error) {
	fields := strings.Fields(args)
	if len(fields) > 1 {
		return text(githubUsage), nil
	}
	if len(fields) == 0 {
		ms, err := s.Store.GetMember(ctx, c.UserID)
		if err != nil {
			return Reply{}, err
		}
		if ms == nil || ms.GitHubLogin == "" {
			return text("Your GitHub login isn't linked yet. " + sendLoginToAdmin), nil
		}
		return text(fmt.Sprintf("Your GitHub login: **[%s](https://github.com/%s)**. To change it, ask an admin.",
			ms.GitHubLogin, ms.GitHubLogin)), nil
	}

	// Self-linking is for admins only (members could otherwise claim any
	// org member's login); members are pointed to an admin.
	if !s.isAdmin(ctx, c.TeamID, c.UserID) {
		return text(askAdminToLink), nil
	}

	login, msg, err := s.verifyGitHubLogin(ctx, c.TeamID, strings.TrimPrefix(fields[0], "@"))
	if err != nil || msg != "" {
		return text(msg), err
	}
	before, after, err := s.Store.UpdateMember(ctx, c.UserID, MemberUpdate{GitHubLogin: &login})
	if errors.Is(err, ErrGitHubLoginTaken) {
		return text(fmt.Sprintf("GitHub login `%s` is already linked to another member. Ask an admin if that's wrong.", login)), nil
	}
	if err != nil {
		return Reply{}, err
	}
	if err := s.Store.InsertAudit(ctx, AuditEntry{
		ActorID: c.UserID, TargetID: c.UserID, TeamID: c.TeamID,
		Action: "github_link", Old: before, New: after,
	}); err != nil {
		return Reply{}, err
	}
	return text(fmt.Sprintf("✅ Linked to GitHub **%s**. `/att tasks` shows your open tasks.", login)), nil
}

// verifyGitHubLogin checks the login against teamID's GitHub org. It
// returns the canonical login, or a message for the user when it can't
// be linked.
func (s *Service) verifyGitHubLogin(ctx context.Context, teamID, login string) (string, string, error) {
	if !ValidGitHubLogin(login) {
		return "", fmt.Sprintf("`%s` isn't a valid GitHub username.", login), nil
	}
	if s.Tasks == nil || !s.Tasks.Configured(teamID) {
		return "", "GitHub isn't set up for **" + s.Teams.TeamName(teamID) + "** yet.", nil
	}
	gctx, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	canonical, err := s.Tasks.VerifyLogin(gctx, teamID, login)
	switch {
	case errors.Is(err, ErrGitHubUserNotFound):
		return "", fmt.Sprintf("There's no GitHub user `%s`.", login), nil
	case errors.Is(err, ErrNotOrgMember):
		return "", fmt.Sprintf("GitHub user `%s` isn't a member of the **%s** GitHub organization. Ask an admin to invite them first.",
			login, s.Tasks.OrgName(teamID)), nil
	case err != nil:
		s.Log.Warn("github verify login failed", "team_id", teamID, "err", err)
		return "", "⚠️ Couldn't reach GitHub right now. Please try again in a minute.", nil
	}
	return canonical, "", nil
}

// --- /att tasks ---

func (s *Service) tasks(ctx context.Context, c Command, args string) (Reply, error) {
	fields := strings.Fields(args)
	if len(fields) > 1 || len(fields) == 1 && !strings.HasPrefix(fields[0], "@") {
		return text("Usage: `/att tasks` · admins: `/att tasks @user`"), nil
	}
	if s.Tasks == nil || !s.Tasks.Configured(c.TeamID) {
		return text("GitHub isn't set up for **" + s.Teams.TeamName(c.TeamID) + "** yet."), nil
	}
	if len(fields) == 1 {
		return s.memberTasks(ctx, c, fields[0])
	}
	ms, err := s.Store.GetMember(ctx, c.UserID)
	if err != nil {
		return Reply{}, err
	}
	if ms == nil || ms.GitHubLogin == "" {
		return text("Your GitHub login isn't linked yet. " + sendLoginToAdmin), nil
	}
	gctx, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	tasks, err := s.Tasks.AssignedTasks(gctx, c.TeamID, ms.GitHubLogin)
	if errors.Is(err, ErrGitHubNotSetUp) {
		s.Log.Error("github project misconfigured", "team_id", c.TeamID, "err", err)
		return text("⚠️ The GitHub Project for **" + s.Teams.TeamName(c.TeamID) + "** isn't set up as the bot needs. Admins: the bot log says exactly what's missing."), nil
	}
	if err != nil {
		s.Log.Warn("github tasks failed", "team_id", c.TeamID, "user_id", c.UserID, "err", err)
		return text("⚠️ Couldn't load your tasks from GitHub right now. Please try again in a minute."), nil
	}
	msg := s.taskListText(tasks, "Your open tasks", c.TeamID)
	return Reply{Text: msg, Attachments: s.pickMenu(c.UserID, c.TeamID, tasks)}, nil
}

// taskListText is the heading plus the sorted list, or a "no tasks" line.
func (s *Service) taskListText(tasks []Task, heading, teamID string) string {
	team := s.Teams.TeamName(teamID)
	if len(tasks) == 0 {
		return fmt.Sprintf("No open tasks assigned to you on the **%s** GitHub Project. 🎉", team)
	}
	today := s.Now().In(s.Loc).Format(DayFormat)
	SortTasks(tasks, today)
	return fmt.Sprintf("#### %s — %s (%d)\n%s", heading, team, len(tasks), RenderTaskList(tasks, today, s.Loc))
}
