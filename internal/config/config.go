// Package config loads service settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
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
}

// Config is the full service configuration.
type Config struct {
	ListenAddr  string
	MMURL       string // Mattermost base URL reachable from this container
	BotToken    string
	DatabaseURL string
	HMACKey     string // signs button/dialog payloads (used from phase 2)
	Location    *time.Location
	Teams       map[string]Team // keyed by Mattermost team id
}

var keyRe = regexp.MustCompile(`^[a-z0-9_]+$`)

// Load reads configuration from the environment.
//
// Teams are declared as TEAMS=inkyank,seechanlabs,... and then, for each key
// K (upper-cased), TEAM_K_ID, TEAM_K_NAME, TEAM_K_SLASH_TOKEN,
// TEAM_K_ATTENDANCE_CHANNEL_ID and TEAM_K_ADMIN_CHANNEL_ID.
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
		HMACKey:     def("HMAC_KEY", ""),
		Teams:       map[string]Team{},
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
		if t.ID == "" {
			continue
		}
		if _, dup := cfg.Teams[t.ID]; dup {
			errs = append(errs, fmt.Errorf("%sID: team id %s configured twice", p, t.ID))
		}
		cfg.Teams[t.ID] = t
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
