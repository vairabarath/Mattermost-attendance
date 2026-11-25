// Package mattermost is a minimal Mattermost REST API v4 client plus the
// payload types the service exchanges with Mattermost.
package mattermost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client calls the Mattermost API as the bot user.
type Client struct {
	BaseURL string
	token   string
	http    *http.Client
}

// NewClient builds a client. The token is never logged.
func NewClient(baseURL, botToken string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		token:   botToken,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// User is the subset of the Mattermost user object we use.
type User struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Nickname  string `json:"nickname"`
	IsBot     bool   `json:"is_bot"`
	DeleteAt  int64  `json:"delete_at"`
	Roles     string `json:"roles"`
}

// DisplayName prefers nickname, then full name, then username.
func (u User) DisplayName() string {
	if u.Nickname != "" {
		return u.Nickname
	}
	if full := strings.TrimSpace(u.FirstName + " " + u.LastName); full != "" {
		return full
	}
	return u.Username
}

// GetUser fetches GET /api/v4/users/{id}.
func (c *Client) GetUser(ctx context.Context, userID string) (*User, error) {
	var u User
	if err := c.do(ctx, http.MethodGet, "/api/v4/users/"+url.PathEscape(userID), nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// UserProfile satisfies attendance.Directory.
func (c *Client) UserProfile(ctx context.Context, userID string) (string, string, error) {
	u, err := c.GetUser(ctx, userID)
	if err != nil {
		return "", "", err
	}
	return u.Username, u.DisplayName(), nil
}

// APIError is a non-2xx response from Mattermost.
type APIError struct {
	Status  int
	Path    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("mattermost %s: HTTP %d: %s", e.Path, e.Status, e.Message)
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
		return &APIError{Status: resp.StatusCode, Path: path, Message: e.Message}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

// CommandResponse is the JSON reply to a slash command.
type CommandResponse struct {
	ResponseType string `json:"response_type"` // "ephemeral" or "in_channel"
	Text         string `json:"text"`
}
