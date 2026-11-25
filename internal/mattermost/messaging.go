package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// TeamMember is the subset of a team membership used for the admin check.
type TeamMember struct {
	TeamID      string `json:"team_id"`
	UserID      string `json:"user_id"`
	Roles       string `json:"roles"`
	SchemeAdmin bool   `json:"scheme_admin"`
	DeleteAt    int64  `json:"delete_at"`
}

// IsTeamAdmin reports team-admin rights via scheme_admin or the legacy role.
func (m *TeamMember) IsTeamAdmin() bool {
	return m != nil && m.DeleteAt == 0 && (m.SchemeAdmin || hasRole(m.Roles, "team_admin"))
}

// IsSystemAdmin reports whether the user has the system_admin role.
func (u *User) IsSystemAdmin() bool { return u != nil && hasRole(u.Roles, "system_admin") }

func hasRole(roles, want string) bool {
	for _, r := range strings.Fields(roles) {
		if r == want {
			return true
		}
	}
	return false
}

// IsNotFound reports a 404 from Mattermost.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// GetTeamMember returns the user's membership of a team, or nil if they
// aren't a member.
func (c *Client) GetTeamMember(ctx context.Context, teamID, userID string) (*TeamMember, error) {
	var m TeamMember
	err := c.do(ctx, http.MethodGet, "/api/v4/teams/"+url.PathEscape(teamID)+"/members/"+url.PathEscape(userID), nil, &m)
	if IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if m.DeleteAt != 0 {
		return nil, nil // left the team
	}
	return &m, nil
}

// GetUserByUsername returns the user, or nil if no such username exists.
func (c *Client) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	var u User
	err := c.do(ctx, http.MethodGet, "/api/v4/users/username/"+url.PathEscape(username), nil, &u)
	if IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// TeamUsers lists every user in a team (including bots and deactivated
// users; callers filter).
func (c *Client) TeamUsers(ctx context.Context, teamID string) ([]User, error) {
	var all []User
	for page := 0; page < 50; page++ {
		var batch []User
		path := fmt.Sprintf("/api/v4/users?in_team=%s&page=%d&per_page=200", url.QueryEscape(teamID), page)
		if err := c.do(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 200 {
			return all, nil
		}
	}
	return all, nil
}

type post struct {
	ChannelID string `json:"channel_id"`
	Message   string `json:"message"`
}

// CreatePost posts a message to a channel as the bot.
func (c *Client) CreatePost(ctx context.Context, channelID, message string) error {
	body, err := json.Marshal(post{ChannelID: channelID, Message: message})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/api/v4/posts", bytes.NewReader(body), nil)
}

// dmCache remembers the bot's own id and its DM channel per user.
type dmCache struct {
	mu       sync.Mutex
	botID    string
	channels map[string]string
}

// DirectMessage sends a DM from the bot to userID.
func (c *Client) DirectMessage(ctx context.Context, userID, message string) error {
	ch, err := c.directChannel(ctx, userID)
	if err != nil {
		return err
	}
	return c.CreatePost(ctx, ch, message)
}

func (c *Client) directChannel(ctx context.Context, userID string) (string, error) {
	c.dm.mu.Lock()
	defer c.dm.mu.Unlock()
	if ch, ok := c.dm.channels[userID]; ok {
		return ch, nil
	}
	if c.dm.botID == "" {
		var me User
		if err := c.do(ctx, http.MethodGet, "/api/v4/users/me", nil, &me); err != nil {
			return "", err
		}
		c.dm.botID = me.ID
	}
	body, _ := json.Marshal([]string{c.dm.botID, userID})
	var ch struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v4/channels/direct", bytes.NewReader(body), &ch); err != nil {
		return "", err
	}
	if c.dm.channels == nil {
		c.dm.channels = map[string]string{}
	}
	c.dm.channels[userID] = ch.ID
	return ch.ID, nil
}
