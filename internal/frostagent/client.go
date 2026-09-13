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

type MessageItem struct {
	Type          string `json:"type"`                      // "plain", "image", "record", "video", "file", "mention_user", "quote"
	Text          string `json:"text,omitempty"`            // text content when type is "plain"
	MentionUserID string `json:"mention_user_id,omitempty"` // platform user ID when type is "mention_user"
	MessageID     string `json:"message_id,omitempty"`      // message ID to reply to when type is "quote"
	URL           string `json:"url,omitempty"`             // web link for media/file
	Path          string `json:"path,omitempty"`            // path for media
	IsSticker     bool   `json:"is_sticker,omitempty"`
}

type SendMessageRequest struct {
	Session  string        `json:"session,omitempty"` // format: "platform_id:message_type:session_id"
	Messages []MessageItem `json:"messages"`
}

type Client interface {
	SendMessage(ctx context.Context, req SendMessageRequest) error
}

type HTTPClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

type Config struct {
	BaseURL    string
	APIKey     string
	Timeout    time.Duration
	HTTPClient *http.Client
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
	return &HTTPClient{
		baseURL:    baseURL,
		apiKey:     cfg.APIKey,
		httpClient: httpClient,
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

	endpoint := c.baseURL + "/api/v1/messages/send"
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
