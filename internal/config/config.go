// Package config loads service settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Team is one organization team that runs attendance.
type Team struct {
	Key                 string // short key from TEAMS, e.g. "inkyank"
	ID                  string // Mattermost team id
	Name                string // display name, e.g. "InkYank"
	SlashToken          string // verification token of this team's /att command
	AttendanceChannelID string // #attendance (used from phase 3)
	AdminChannelID      string // #attendance-admin (used from phase 3)

	// GitHub (tasks + EOD). Empty GitHubOrg: no task features for this team.
	GitHubOrg     string // TEAM_K_GITHUB_ORG
	GitHubProject int    // TEAM_K_GITHUB_PROJECT: org Project (v2) number
}

// Config is the full service configuration.
type Config struct {
	ListenAddr  string
	MMURL       string // Mattermost base URL reachable from this container
	BotToken    string
	DatabaseURL string
	HMACKey     string // signs button/dialog payloads
	ServiceURL  string // this service's URL as Mattermost sees it
	Location    *time.Location
	Teams       map[string]Team // keyed by Mattermost team id
	TeamOrder   []string        // team ids in TEAMS order

	// Scheduled jobs (phase 3).
	Workdays        map[time.Weekday]bool // WORKDAYS, default mon..fri
	MaxSessionHours int                   // MAX_SESSION_HOURS, default 16

	// GitHub App (tasks + EOD). GitHubAppID 0: GitHub features off.
	GitHubAppID          int64
	GitHubPrivateKeyPath string
	GitHubAPIURL         string // default https://api.github.com
	// GitHubAddAssigned: open issues in a team's org assigned to a linked
	// member are added to the team's Project (default on).
	GitHubAddAssigned bool
	// GitHubSweepMinutes: how often the board sweep runs (0 = off; the
	// commands still add issues when members look).
	GitHubSweepMinutes int

	// PluginSecret is shared with the Attendance Mattermost plugin
	// (PLUGIN_SECRET, 32+ characters). Empty: the panel API is off.
	PluginSecret string
}

// GitHubEnabled reports whether the GitHub App is configured.
func (c *Config) GitHubEnabled() bool { return c.GitHubAppID != 0 }

var weekdayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// TeamList returns the configured teams in TEAMS order.
func (c *Config) TeamList() []Team {
	out := make([]Team, 0, len(c.TeamOrder))
	for _, id := range c.TeamOrder {
		out = append(out, c.Teams[id])
	}
	return out
}

var keyRe = regexp.MustCompile(`^[a-z0-9_]+$`)

// Load reads configuration from the environment.
//
// Teams are declared as TEAMS=inkyank,seechanlabs,... and then, for each key
// K (upper-cased), TEAM_K_ID, TEAM_K_NAME, TEAM_K_SLASH_TOKEN,
// TEAM_K_ATTENDANCE_CHANNEL_ID and TEAM_K_ADMIN_CHANNEL_ID, plus optional
// TEAM_K_GITHUB_ORG and TEAM_K_GITHUB_PROJECT.
func Load() (*Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (*Config, error) {
	var errs []error
	req := func(name string) string {
		v := strings.TrimSpace(getenv(name))
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
		return v
	}
	def := func(name, fallback string) string {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return v
		}
		return fallback
	}

	cfg := &Config{
		ListenAddr:  def("LISTEN_ADDR", ":8080"),
		MMURL:       strings.TrimRight(req("MM_URL"), "/"),
		BotToken:    req("MM_BOT_TOKEN"),
		DatabaseURL: req("DATABASE_URL"),
		HMACKey:     req("HMAC_KEY"),
		ServiceURL:  strings.TrimRight(def("SERVICE_URL", "http://attendance:8080"), "/"),
		Teams:       map[string]Team{},
	}

	if cfg.HMACKey != "" && len(cfg.HMACKey) < 32 {
		errs = append(errs, errors.New("HMAC_KEY must be at least 32 characters (openssl rand -hex 32)"))
	}

	tz := def("TIMEZONE", "Asia/Kolkata")
	loc, err := time.LoadLocation(tz)
	if err != nil {
		errs = append(errs, fmt.Errorf("TIMEZONE %q: %w", tz, err))
	}
	cfg.Location = loc

	keys := strings.Split(req("TEAMS"), ",")
	for _, k := range keys {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		if !keyRe.MatchString(k) {
			errs = append(errs, fmt.Errorf("TEAMS: invalid key %q (use a-z, 0-9, _)", k))
			continue
		}
		p := "TEAM_" + strings.ToUpper(k) + "_"
		t := Team{
			Key:                 k,
			ID:                  req(p + "ID"),
			Name:                req(p + "NAME"),
			SlashToken:          req(p + "SLASH_TOKEN"),
			AttendanceChannelID: def(p+"ATTENDANCE_CHANNEL_ID", ""),
			AdminChannelID:      def(p+"ADMIN_CHANNEL_ID", ""),
		}
		if org := def(p+"GITHUB_ORG", ""); org != "" {
			t.GitHubOrg = org
			pn := def(p+"GITHUB_PROJECT", "")
			if n, err := strconv.Atoi(pn); err != nil || n < 1 {
				errs = append(errs, fmt.Errorf("%sGITHUB_PROJECT %q: must be the Project number (from its URL) when %sGITHUB_ORG is set", p, pn, p))
			} else {
				t.GitHubProject = n
			}
		}
		if t.ID == "" {
			continue
		}
		if _, dup := cfg.Teams[t.ID]; dup {
			errs = append(errs, fmt.Errorf("%sID: team id %s configured twice", p, t.ID))
		}
		cfg.Teams[t.ID] = t
		cfg.TeamOrder = append(cfg.TeamOrder, t.ID)
	}

	cfg.Workdays = map[time.Weekday]bool{}
	for _, d := range strings.Split(def("WORKDAYS", "mon,tue,wed,thu,fri"), ",") {
		wd, ok := weekdayNames[strings.ToLower(strings.TrimSpace(d))]
		if !ok {
			errs = append(errs, fmt.Errorf("WORKDAYS: unknown day %q (use mon,tue,...)", d))
			continue
		}
		cfg.Workdays[wd] = true
	}

	maxH := def("MAX_SESSION_HOURS", "16")
	if n, err := strconv.Atoi(maxH); err != nil || n < 1 || n > 72 {
		errs = append(errs, fmt.Errorf("MAX_SESSION_HOURS %q: must be a whole number 1–72", maxH))
	} else {
		cfg.MaxSessionHours = n
	}

	cfg.GitHubAPIURL = strings.TrimRight(def("GITHUB_API_URL", "https://api.github.com"), "/")
	if id := def("GITHUB_APP_ID", ""); id != "" {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("GITHUB_APP_ID %q: must be the numeric App ID", id))
		}
		cfg.GitHubAppID = n
		cfg.GitHubPrivateKeyPath = req("GITHUB_APP_PRIVATE_KEY_PATH")
	}
	switch v := strings.ToLower(def("GITHUB_ADD_ASSIGNED_TO_BOARD", "true")); v {
	case "true", "1", "yes", "on":
		cfg.GitHubAddAssigned = true
	case "false", "0", "no", "off":
	default:
		errs = append(errs, fmt.Errorf("GITHUB_ADD_ASSIGNED_TO_BOARD %q: use true or false", v))
	}
	sweep := def("GITHUB_BOARD_SWEEP_MINUTES", "2")
	if n, err := strconv.Atoi(sweep); err != nil || n < 0 || n > 60 {
		errs = append(errs, fmt.Errorf("GITHUB_BOARD_SWEEP_MINUTES %q: must be a whole number 0–60 (0 = off)", sweep))
	} else {
		cfg.GitHubSweepMinutes = n
	}
	if s := def("PLUGIN_SECRET", ""); s != "" {
		if len(s) < 32 {
			errs = append(errs, errors.New("PLUGIN_SECRET: must be at least 32 characters (e.g. openssl rand -hex 32)"))
		}
		cfg.PluginSecret = s
	}
	if cfg.GitHubAppID == 0 {
		for _, id := range cfg.TeamOrder {
			if t := cfg.Teams[id]; t.GitHubOrg != "" {
				errs = append(errs, fmt.Errorf("TEAM_%s_GITHUB_ORG is set but GITHUB_APP_ID is not", strings.ToUpper(t.Key)))
			}
		}
	}

	if len(cfg.Teams) == 0 && len(errs) == 0 {
		errs = append(errs, errors.New("TEAMS: no teams configured"))
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return cfg, nil
}

// TeamName returns the display name for a team id, or a fallback.
func (c *Config) TeamName(teamID string) string {
	if t, ok := c.Teams[teamID]; ok {
		return t.Name
	}
	return "another team"
}
