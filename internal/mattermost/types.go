package mattermost

import "time"

// CommandResponse is the JSON reply to a slash command.
type CommandResponse struct {
	ResponseType string       `json:"response_type"` // "ephemeral" or "in_channel"
	Text         string       `json:"text"`
	Attachments  []Attachment `json:"attachments,omitempty"`
}

// Attachment is a message attachment; we only use it to carry buttons.
type Attachment struct {
	Text    string   `json:"text,omitempty"`
	Actions []Action `json:"actions,omitempty"`
}

// Action is an interactive button.
type Action struct {
	ID          string      `json:"id"` // alphanumeric only
	Name        string      `json:"name"`
	Style       string      `json:"style,omitempty"` // default, primary, danger, ...
	Integration Integration `json:"integration"`
}

// Integration tells Mattermost where to POST a button click.
type Integration struct {
	URL     string            `json:"url"`
	Context map[string]string `json:"context"`
}

// ActionRequest is what Mattermost POSTs to /mm/action on a click.
type ActionRequest struct {
	UserID    string            `json:"user_id"`
	UserName  string            `json:"user_name"`
	ChannelID string            `json:"channel_id"`
	TeamID    string            `json:"team_id"`
	PostID    string            `json:"post_id"`
	Context   map[string]string `json:"context"`
}

// ActionResponse updates the clicked post and/or shows an ephemeral note.
type ActionResponse struct {
	Update        *PostUpdate `json:"update,omitempty"`
	EphemeralText string      `json:"ephemeral_text,omitempty"`
}

// PostUpdate replaces the message and attachments of the clicked post.
type PostUpdate struct {
	Message string         `json:"message"`
	Props   map[string]any `json:"props"`
}

// CustomStatus is a user's Mattermost custom status.
type CustomStatus struct {
	Emoji     string    `json:"emoji"`
	Text      string    `json:"text"`
	Duration  string    `json:"duration,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// IsEmpty reports whether no status is set.
func (c *CustomStatus) IsEmpty() bool {
	return c == nil || (c.Emoji == "" && c.Text == "")
}

// Expired reports whether a status with an expiry time has passed it.
func (c *CustomStatus) Expired(now time.Time) bool {
	return c != nil && !c.ExpiresAt.IsZero() && c.ExpiresAt.Year() > 1 && !c.ExpiresAt.After(now)
}
