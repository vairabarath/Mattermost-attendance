package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// Dialog is an interactive dialog (form).
type Dialog struct {
	CallbackID       string          `json:"callback_id"`
	Title            string          `json:"title"`
	IntroductionText string          `json:"introduction_text,omitempty"`
	Elements         []DialogElement `json:"elements"`
	SubmitLabel      string          `json:"submit_label,omitempty"`
	State            string          `json:"state"`
}

// DialogElement is one form field: type "text", "textarea" or "bool".
type DialogElement struct {
	DisplayName string `json:"display_name"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Optional    bool   `json:"optional"`
	Placeholder string `json:"placeholder,omitempty"`
	HelpText    string `json:"help_text,omitempty"`
	Default     string `json:"default,omitempty"`
	MaxLength   int    `json:"max_length,omitempty"`
}

// DialogSubmission is what Mattermost POSTs to the dialog URL.
type DialogSubmission struct {
	Type       string         `json:"type"`
	CallbackID string         `json:"callback_id"`
	State      string         `json:"state"`
	UserID     string         `json:"user_id"`
	ChannelID  string         `json:"channel_id"`
	TeamID     string         `json:"team_id"`
	Submission map[string]any `json:"submission"`
	Cancelled  bool           `json:"cancelled"`
}

// DialogResponse returns field errors (keeps the dialog open) or a general error.
type DialogResponse struct {
	Error  string            `json:"error,omitempty"`
	Errors map[string]string `json:"errors,omitempty"`
}

// OpenDialog opens a dialog for the user who triggered triggerID.
func (c *Client) OpenDialog(ctx context.Context, triggerID, submitURL string, d Dialog) error {
	body, err := json.Marshal(map[string]any{"trigger_id": triggerID, "url": submitURL, "dialog": d})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/api/v4/actions/dialogs/open", bytes.NewReader(body), nil)
}

// CreatePostWithButtons posts a message with attachments and returns its id.
func (c *Client) CreatePostWithButtons(ctx context.Context, channelID, message string, atts []Attachment) (string, error) {
	body, err := json.Marshal(map[string]any{
		"channel_id": channelID, "message": message,
		"props": map[string]any{"attachments": atts},
	})
	if err != nil {
		return "", err
	}
	var p struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v4/posts", bytes.NewReader(body), &p); err != nil {
		return "", err
	}
	return p.ID, nil
}

// UpdatePost replaces a bot post's message and attachments (nil removes buttons).
func (c *Client) UpdatePost(ctx context.Context, postID, message string, atts []Attachment) error {
	if atts == nil {
		atts = []Attachment{}
	}
	body, err := json.Marshal(map[string]any{"message": message, "props": map[string]any{"attachments": atts}})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPut, "/api/v4/posts/"+url.PathEscape(postID)+"/patch", bytes.NewReader(body), nil)
}

// EphemeralPost shows a message only userID can see in a channel.
func (c *Client) EphemeralPost(ctx context.Context, userID, channelID, message string) error {
	body, err := json.Marshal(map[string]any{"user_id": userID, "post": map[string]any{"channel_id": channelID, "message": message}})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/api/v4/posts/ephemeral", bytes.NewReader(body), nil)
}
