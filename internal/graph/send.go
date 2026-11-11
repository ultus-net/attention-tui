package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// SendChatMessage posts one plain-text reply to a Teams chat. It is the only
// write attention-tui makes, and it never touches read state.
func (c *Client) SendChatMessage(ctx context.Context, chatID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("graph: empty chat message")
	}
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return errors.New("graph: empty chat id")
	}

	var payload struct {
		Body struct {
			ContentType string `json:"contentType"`
			Content     string `json:"content"`
		} `json:"body"`
	}
	payload.Body.ContentType = "text"
	payload.Body.Content = text
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("graph: encode chat message: %w", err)
	}

	token, err := c.tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("graph: acquire token: %w", err)
	}

	endpoint := c.baseURL + "/me/chats/" + url.PathEscape(chatID) + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("graph: build chat message request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("graph: send chat message: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil {
		return fmt.Errorf("graph: read send error: %w", err)
	}
	return newGraphError(resp.StatusCode, body)
}
