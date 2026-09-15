package actionscat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var (
	ErrNotConfigured = errors.New("actionscat runtime endpoint or token not configured")
	ErrNoSession     = errors.New("no active session to reply to")
)

type Context struct {
	ActionID        string
	ActionVersion   string
	BuildID         string
	RunID           string
	TriggerType     string
	RuntimeEndpoint string
	RuntimeToken    string

	// Event variables
	Platform  string
	SessionID string
	UserID    string
	GroupID   string
	Text      string
}

// GetContext inspects environment variables injected by ActionsCat Core.
func GetContext() *Context {
	return &Context{
		ActionID:        os.Getenv("ACTIONSCAT_ACTION_ID"),
		ActionVersion:   os.Getenv("ACTIONSCAT_ACTION_VERSION"),
		BuildID:         os.Getenv("ACTIONSCAT_BUILD_ID"),
		RunID:           os.Getenv("ACTIONSCAT_RUN_ID"),
		TriggerType:     os.Getenv("ACTIONSCAT_TRIGGER_TYPE"),
		RuntimeEndpoint: strings.TrimRight(os.Getenv("ACTIONSCAT_RUNTIME_ENDPOINT"), "/"),
		RuntimeToken:    os.Getenv("ACTIONSCAT_RUNTIME_TOKEN"),

		Platform:  os.Getenv("ACTIONSCAT_EVENT_PLATFORM"),
		SessionID: os.Getenv("ACTIONSCAT_EVENT_SESSION_ID"),
		UserID:    os.Getenv("ACTIONSCAT_EVENT_USER_ID"),
		GroupID:   os.Getenv("ACTIONSCAT_EVENT_GROUP_ID"),
		Text:      os.Getenv("ACTIONSCAT_EVENT_TEXT"),
	}
}

// GetEnv is a helper to retrieve arbitrary injected variables (such as matcher regex captures or state injections).
func GetEnv(key string) string {
	return os.Getenv(key)
}

// WriteState writes data back to ActionsCat Core's isolated state storage.
func WriteState(ctx context.Context, path string, data []byte) error {
	c := GetContext()
	if c.RuntimeEndpoint == "" || c.RuntimeToken == "" {
		return ErrNotConfigured
	}

	payload := map[string]any{
		"path": path,
		"data": string(data),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal state payload: %w", err)
	}

	endpoint := c.RuntimeEndpoint + "/state"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.RuntimeToken)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("runtime state request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("runtime state write error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

type MessageItem struct {
	Type          string `json:"type"` // "plain", "image", "record", "video", "file", "mention_user", "quote"
	Text          string `json:"text,omitempty"`
	MentionUserID string `json:"mention_user_id,omitempty"`
	MessageID     string `json:"message_id,omitempty"`
	URL           string `json:"url,omitempty"`
	Path          string `json:"path,omitempty"`
}

// SendMessageRequest carries canonical routing and message content for FrostAgent delivery.
type SendMessageRequest struct {
	Platform    string        `json:"platform,omitempty"`
	MessageType string        `json:"message_type,omitempty"`
	TargetID    string        `json:"target_id,omitempty"`
	Session     string        `json:"session,omitempty"`
	Messages    []MessageItem `json:"messages,omitempty"`
	Content     string        `json:"content,omitempty"`
}

// Send sends a structured message request through Core's trusted FrostAgent proxy.
func Send(ctx context.Context, req SendMessageRequest) error {
	c := GetContext()
	if c.RuntimeEndpoint == "" || c.RuntimeToken == "" {
		return ErrNotConfigured
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal message payload: %w", err)
	}

	endpoint := c.RuntimeEndpoint + "/frostagent/send"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.RuntimeToken)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("runtime send message request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("runtime send message error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// SendMessage sends structured messages to the specified session through Core's trusted FrostAgent proxy.
// Destination routing is strictly governed by the session parameter and does not inherit ambient event targets.
func SendMessage(ctx context.Context, session string, messages []MessageItem) error {
	var platform, msgType, targetID string
	session = strings.TrimSpace(session)
	if session != "" {
		parts := strings.Split(session, ":")
		if len(parts) >= 3 {
			platform = strings.TrimSpace(parts[0])
			msgType = strings.TrimSpace(parts[1])
			targetID = strings.TrimSpace(strings.Join(parts[2:], ":"))
		}
	}

	return Send(ctx, SendMessageRequest{
		Platform:    platform,
		MessageType: msgType,
		TargetID:    targetID,
		Session:     session,
		Messages:    messages,
	})
}

// Reply sends a plain text response back to the current session using canonical transport routing.
func Reply(ctx context.Context, text string) error {
	c := GetContext()
	platform := c.Platform
	var msgType, targetID string
	if c.GroupID != "" {
		msgType = "group"
		targetID = c.GroupID
	} else if c.UserID != "" {
		msgType = "private"
		targetID = c.UserID
	} else if c.SessionID != "" {
		targetID = c.SessionID
	} else {
		return ErrNoSession
	}

	return Send(ctx, SendMessageRequest{
		Platform:    platform,
		MessageType: msgType,
		TargetID:    targetID,
		Session:     c.SessionID,
		Messages: []MessageItem{
			{Type: "plain", Text: text},
		},
	})
}
