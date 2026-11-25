package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
)

// UploadFile uploads data to a channel and returns the file id.
func (c *Client) UploadFile(ctx context.Context, channelID, filename string, data []byte) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("channel_id", channelID); err != nil {
		return "", err
	}
	fw, err := mw.CreateFormFile("files", filename)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(data); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	var resp struct {
		FileInfos []struct {
			ID string `json:"id"`
		} `json:"file_infos"`
	}
	if err := c.doCT(ctx, http.MethodPost, "/api/v4/files", mw.FormDataContentType(), &body, &resp); err != nil {
		return "", err
	}
	if len(resp.FileInfos) == 0 {
		return "", errors.New("upload returned no file")
	}
	return resp.FileInfos[0].ID, nil
}

// DirectFile DMs a message with an attached file from the bot to userID.
func (c *Client) DirectFile(ctx context.Context, userID, message, filename string, data []byte) error {
	ch, err := c.directChannel(ctx, userID)
	if err != nil {
		return err
	}
	id, err := c.UploadFile(ctx, ch, filename, data)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"channel_id": ch, "message": message, "file_ids": []string{id}})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/api/v4/posts", bytes.NewReader(body), nil)
}
