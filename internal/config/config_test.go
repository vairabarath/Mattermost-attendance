package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func base() map[string]string {
	return map[string]string{
		"MM_URL":                       "http://mattermost:8065/",
		"MM_BOT_TOKEN":                 "bot",
		"DATABASE_URL":                 "postgres://x",
		"HMAC_KEY":                     "0123456789abcdef0123456789abcdef",
		"TEAMS":                        "inkyank, seechanlabs",
		"TEAM_INKYANK_ID":              "teamink",
		"TEAM_INKYANK_NAME":            "InkYank",
		"TEAM_INKYANK_SLASH_TOKEN":     "tok-ink",
		"TEAM_SEECHANLABS_ID":          "teamsee",
		"TEAM_SEECHANLABS_NAME":        "Seechanlabs",
		"TEAM_SEECHANLABS_SLASH_TOKEN": "tok-see",
	}
}

func TestLoadOK(t *testing.T) {
	cfg, err := load(env(base()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MMURL != "http://mattermost:8065" {
		t.Errorf("trailing slash not trimmed: %q", cfg.MMURL)
	}
	if cfg.ListenAddr != ":8080" || cfg.Location.String() != "Asia/Kolkata" {
		t.Errorf("defaults wrong: %+v", cfg)
	}
	if len(cfg.Teams) != 2 || cfg.Teams["teamink"].SlashToken != "tok-ink" {
		t.Errorf("teams wrong: %+v", cfg.Teams)
	}
	if cfg.TeamName("teamsee") != "Seechanlabs" || cfg.TeamName("nope") != "another team" {
		t.Error("TeamName wrong")
	}
}

func TestLoadMissing(t *testing.T) {
	m := base()
	delete(m, "MM_BOT_TOKEN")
	delete(m, "TEAM_INKYANK_SLASH_TOKEN")
	_, err := load(env(m))
	if err == nil || !strings.Contains(err.Error(), "MM_BOT_TOKEN") || !strings.Contains(err.Error(), "TEAM_INKYANK_SLASH_TOKEN") {
		t.Fatalf("expected both missing vars reported, got %v", err)
	}
}

func TestLoadDuplicateTeamID(t *testing.T) {
	m := base()
	m["TEAM_SEECHANLABS_ID"] = "teamink"
	if _, err := load(env(m)); err == nil {
		t.Fatal("expected duplicate team id error")
	}
}

func TestWorkdaysAndMaxSession(t *testing.T) {
	cfg, err := load(env(base()))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Workdays) != 5 || cfg.Workdays[time.Saturday] || !cfg.Workdays[time.Monday] || cfg.MaxSessionHours != 16 {
		t.Errorf("defaults: %v %d", cfg.Workdays, cfg.MaxSessionHours)
	}
	if got := cfg.TeamList(); len(got) != 2 || got[0].Name != "InkYank" {
		t.Errorf("TeamList order: %+v", got)
	}
	m := base()
	m["WORKDAYS"] = "mon,tue,wed,thu,fri,sat"
	m["MAX_SESSION_HOURS"] = "12"
	cfg, err = load(env(m))
	if err != nil || !cfg.Workdays[time.Saturday] || cfg.MaxSessionHours != 12 {
		t.Fatalf("custom: %v %+v", err, cfg)
	}
	m["WORKDAYS"] = "mon,funday"
	if _, err := load(env(m)); err == nil || !strings.Contains(err.Error(), "funday") {
		t.Errorf("bad weekday accepted: %v", err)
	}
}

func TestGitHubConfig(t *testing.T) {
	cfg, err := load(env(base()))
	if err != nil || cfg.GitHubEnabled() {
		t.Fatalf("GitHub should be off by default: %v", err)
	}

	m := base()
	m["GITHUB_APP_ID"] = "12345"
	m["GITHUB_APP_PRIVATE_KEY_PATH"] = "/run/secrets/github-app.pem"
	m["TEAM_INKYANK_GITHUB_ORG"] = "inkyank-org"
	m["TEAM_INKYANK_GITHUB_PROJECT"] = "3"
	cfg, err = load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.GitHubEnabled() || cfg.GitHubAPIURL != "https://api.github.com" {
		t.Errorf("github settings: %+v", cfg)
	}
	if ink := cfg.Teams["teamink"]; ink.GitHubOrg != "inkyank-org" || ink.GitHubProject != 3 {
		t.Errorf("team github: %+v", ink)
	}
	if cfg.Teams["teamsee"].GitHubOrg != "" {
		t.Error("team without GITHUB_ORG should have none")
	}
	if cfg.PluginSecret != "" {
		t.Error("plugin secret should be empty by default")
	}
	m["PLUGIN_SECRET"] = "0123456789abcdef0123456789abcdef"
	if c, err := load(env(m)); err != nil || c.PluginSecret != m["PLUGIN_SECRET"] {
		t.Errorf("plugin secret: %v", err)
	}
	delete(m, "PLUGIN_SECRET")
	if !cfg.GitHubAddAssigned || cfg.GitHubSweepMinutes != 2 {
		t.Errorf("board defaults: add=%v sweep=%d", cfg.GitHubAddAssigned, cfg.GitHubSweepMinutes)
	}
	m["GITHUB_ADD_ASSIGNED_TO_BOARD"], m["GITHUB_BOARD_SWEEP_MINUTES"] = "false", "0"
	if cfg, err := load(env(m)); err != nil || cfg.GitHubAddAssigned || cfg.GitHubSweepMinutes != 0 {
		t.Errorf("board off: %v %+v", err, cfg)
	}

	bad := []map[string]string{
		{"TEAM_INKYANK_GITHUB_ORG": "x"},                                                            // org but no app
		{"GITHUB_APP_ID": "12345"},                                                                  // no key path
		{"GITHUB_APP_ID": "abc", "GITHUB_APP_PRIVATE_KEY_PATH": "/k"},                               // bad id
		{"GITHUB_APP_ID": "1", "GITHUB_APP_PRIVATE_KEY_PATH": "/k", "TEAM_INKYANK_GITHUB_ORG": "x"}, // no project
		{"GITHUB_BOARD_SWEEP_MINUTES": "90"},
		{"PLUGIN_SECRET": "too-short"},
		{"GITHUB_ADD_ASSIGNED_TO_BOARD": "maybe"},
	}
	for _, extra := range bad {
		m := base()
		for k, v := range extra {
			m[k] = v
		}
		if _, err := load(env(m)); err == nil {
			t.Errorf("accepted %v", extra)
		}
	}
}
