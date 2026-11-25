package config

import (
	"strings"
	"testing"
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
