package frostagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var (
	ErrInvalidRequest  = errors.New("invalid frostagent request")
	ErrDeliveryFailed  = errors.New("frostagent delivery failed")
	ErrUnauthenticated = errors.New("frostagent authentication failed")
)

// OutgoingMessage represents a canonical platform-neutral message element.
type OutgoingMessage struct {
	Type          string `json:"type"`                      // "plain", "image", "record", "video", "file", "mention_user", "quote"
	Text          string `json:"text,omitempty"`            // text content when type is "plain"
	MentionUserID string `json:"mention_user_id,omitempty"` // platform user ID when type is "mention_user"
	MessageID     string `json:"message_id,omitempty"`      // message ID to reply to when type is "quote"
	URL           string `json:"url,omitempty"`             // web link for media/file
	Path          string `json:"path,omitempty"`            // path for media
	IsSticker     bool   `json:"is_sticker,omitempty"`
}

// MessageItem is maintained as an alias for OutgoingMessage for compatibility.
type MessageItem = OutgoingMessage

type SendMessageRequest struct {
	Session     string            `json:"session,omitempty"`      // format: "platform_id:message_type:session_id"
	Platform    string            `json:"platform,omitempty"`     // e.g. "qq", "telegram", "discord"
	MessageType string            `json:"message_type,omitempty"` // "private" or "group"
	TargetID    string            `json:"target_id,omitempty"`    // target user ID or group ID
	Messages    []OutgoingMessage `json:"messages"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type Client interface {
	SendMessage(ctx context.Context, req SendMessageRequest) error
}

type HTTPClient struct {
	baseURL      string
	sendEndpoint string
	apiKey       string
	httpClient   *http.Client
}

type Config struct {
	BaseURL      string
	SendEndpoint string // e.g. "/api/v1/messages/send" or custom endpoint
	APIKey       string
	Timeout      time.Duration
	HTTPClient   *http.Client
}

func NewHTTPClient(cfg Config) *HTTPClient {
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	sendEndpoint := strings.TrimSpace(cfg.SendEndpoint)
	if sendEndpoint == "" {
		sendEndpoint = "/api/v1/messages/send"
	}
	return &HTTPClient{
		baseURL:      baseURL,
		sendEndpoint: sendEndpoint,
		apiKey:       cfg.APIKey,
		httpClient:   httpClient,
	}
}

func (c *HTTPClient) SendMessage(ctx context.Context, req SendMessageRequest) error {
	if len(req.Messages) == 0 {
		return fmt.Errorf("%w: messages cannot be empty", ErrInvalidRequest)
	}

	payloadBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal send message request: %w", err)
	}

	var endpoint string
	if strings.HasPrefix(c.sendEndpoint, "http://") || strings.HasPrefix(c.sendEndpoint, "https://") {
		endpoint = c.sendEndpoint
	} else if strings.HasPrefix(c.sendEndpoint, "/") {
		endpoint = c.baseURL + c.sendEndpoint
	} else {
		endpoint = c.baseURL + "/" + c.sendEndpoint
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
		httpReq.Header.Set("X-FrostAgent-Key", c.apiKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDeliveryFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusAccepted {
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%w: HTTP %d: %s", ErrUnauthenticated, resp.StatusCode, string(body))
	}

	return fmt.Errorf("%w: HTTP %d: %s", ErrDeliveryFailed, resp.StatusCode, string(body))
}

// MockClient is useful for testing without network requests.
type MockClient struct {
	SentMessages []SendMessageRequest
	Err          error
}

func (m *MockClient) SendMessage(ctx context.Context, req SendMessageRequest) error {
	if m.Err != nil {
		return m.Err
	}
	m.SentMessages = append(m.SentMessages, req)
	return nil
}
