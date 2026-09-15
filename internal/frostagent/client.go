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
	ErrInvalidRequest   = errors.New("invalid frostagent request")
	ErrDeliveryFailed   = errors.New("frostagent delivery failed")
	ErrUnauthenticated  = errors.New("frostagent authentication failed")
	ErrEndpointNotFound = errors.New("frostagent send message endpoint not found (404); verify FROSTAGENT_SEND_ENDPOINT or instance routing /instances/{id}/api/v1/messages/send")
)

// AttachmentType represents the canonical attachment classification matching FrostAgent.
type AttachmentType string

const (
	AttachmentTypeImage AttachmentType = "image"
	AttachmentTypeFile  AttachmentType = "file"
	AttachmentTypeAudio AttachmentType = "audio"
	AttachmentTypeVideo AttachmentType = "video"
)

// Attachment represents media or binary attachments in FrostAgent messages.
type Attachment struct {
	Type      AttachmentType `json:"type"`
	SubType   int            `json:"sub_type,omitempty"`
	MessageID string         `json:"message_id,omitempty"`
	Content   []byte         `json:"content,omitempty"`
	MimeType  string         `json:"mime_type,omitempty"`
	URL       string         `json:"url,omitempty"`
	Name      string         `json:"name,omitempty"`
}

// OutgoingMessage represents a platform-neutral message matching FrostAgent's core contract.
type OutgoingMessage struct {
	TargetID    string         `json:"target_id,omitempty"`    // target group_id or user_id
	MessageType string         `json:"message_type,omitempty"` // "group" | "private"
	Platform    string         `json:"platform,omitempty"`     // "qq", "telegram", "discord", "astrbot"
	Content     string         `json:"content,omitempty"`      // message text content
	Attachments []Attachment   `json:"attachments,omitempty"`  // attached media/files
	Metadata    map[string]any `json:"metadata,omitempty"`

	// Element compatibility fields
	Type          string `json:"type,omitempty"`                      // "plain", "image", "record", "video", "file", "mention_user", "quote"
	Text          string `json:"text,omitempty"`                      // text content when type is "plain"
	MentionUserID string `json:"mention_user_id,omitempty"`          // platform user ID when type is "mention_user"
	MessageID     string `json:"message_id,omitempty"`               // message ID to reply to when type is "quote"
	URL           string `json:"url,omitempty"`                      // web link for media/file
	Path          string `json:"path,omitempty"`                     // local path for media
	IsSticker     bool   `json:"is_sticker,omitempty"`
}

// MessageItem is maintained as an alias for OutgoingMessage for backward compatibility.
type MessageItem = OutgoingMessage

type SendMessageRequest struct {
	Session     string            `json:"session,omitempty"`      // format: "platform_id:message_type:session_id"
	Platform    string            `json:"platform,omitempty"`     // e.g. "qq", "telegram", "discord"
	MessageType string            `json:"message_type,omitempty"` // "private" or "group"
	TargetID    string            `json:"target_id,omitempty"`    // target user ID or group ID
	Content     string            `json:"content,omitempty"`      // direct message text
	Attachments []Attachment      `json:"attachments,omitempty"`  // direct message attachments
	Messages    []OutgoingMessage `json:"messages,omitempty"`     // segmented message list
	InstanceID  string            `json:"instance_id,omitempty"`  // optional FrostAgent multi-instance routing ID
	Metadata    map[string]any    `json:"metadata,omitempty"`

	// Element compatibility fields at top-level
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

type Client interface {
	SendMessage(ctx context.Context, req SendMessageRequest) error
}

type HTTPClient struct {
	baseURL      string
	sendEndpoint string
	instanceID   string
	apiKey       string
	httpClient   *http.Client
}

type Config struct {
	BaseURL      string
	SendEndpoint string // e.g. "/api/v1/messages/send" or custom endpoint
	InstanceID   string // e.g. "inst_123" for instance routing (/instances/{id}/api/v1/messages/send)
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
		instanceID:   strings.TrimSpace(cfg.InstanceID),
		apiKey:       cfg.APIKey,
		httpClient:   httpClient,
	}
}

// NormalizeSendMessageRequest canonicalizes an incoming SendMessageRequest:
// 1. Parses session strings ("platform:message_type:target_id") into fallback routing fields.
// 2. Ensures MessageType defaults to "group" if not otherwise specified.
// 3. Normalizes Content/Text element compatibility fields across Messages.
func NormalizeSendMessageRequest(req *SendMessageRequest) error {
	if req.Session != "" {
		parts := strings.Split(req.Session, ":")
		if len(parts) >= 3 {
			if req.Platform == "" {
				req.Platform = strings.TrimSpace(parts[0])
			}
			if req.MessageType == "" {
				req.MessageType = strings.TrimSpace(parts[1])
			}
			if req.TargetID == "" {
				req.TargetID = strings.TrimSpace(strings.Join(parts[2:], ":"))
			}
		}
	}
	if req.MessageType == "" {
		req.MessageType = "group"
	}

	if len(req.Messages) == 0 {
		content := req.Content
		if content == "" && req.Text != "" {
			content = req.Text
		}

		if strings.TrimSpace(content) == "" && len(req.Attachments) == 0 {
			return fmt.Errorf("%w: messages, content, or attachments cannot be empty", ErrInvalidRequest)
		}

		req.Messages = []OutgoingMessage{
			{
				TargetID:    req.TargetID,
				MessageType: req.MessageType,
				Platform:    req.Platform,
				Content:     content,
				Attachments: req.Attachments,
				Metadata:    req.Metadata,
				Type:        "plain",
				Text:        content,
			},
		}
		return nil
	}

	for i := range req.Messages {
		if req.Messages[i].Platform == "" {
			req.Messages[i].Platform = req.Platform
		}
		if req.Messages[i].MessageType == "" {
			req.Messages[i].MessageType = req.MessageType
		}
		if req.Messages[i].MessageType == "" {
			req.Messages[i].MessageType = "group"
		}
		if req.Messages[i].TargetID == "" {
			req.Messages[i].TargetID = req.TargetID
		}
		if req.Messages[i].Content == "" && req.Messages[i].Text != "" {
			req.Messages[i].Content = req.Messages[i].Text
		}
		if req.Messages[i].Type == "image" || req.Messages[i].Type == "file" || req.Messages[i].Type == "video" || req.Messages[i].Type == "audio" {
			if (req.Messages[i].URL != "" || req.Messages[i].Path != "") && len(req.Messages[i].Attachments) == 0 {
				req.Messages[i].Attachments = append(req.Messages[i].Attachments, Attachment{
					Type: AttachmentType(req.Messages[i].Type),
					URL:  req.Messages[i].URL,
					Name: req.Messages[i].Path,
				})
			}
		}
	}

	return nil
}

func (c *HTTPClient) SendMessage(ctx context.Context, req SendMessageRequest) error {
	if err := NormalizeSendMessageRequest(&req); err != nil {
		return err
	}

	payloadBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal send message request: %w", err)
	}

	// Determine endpoint with instance routing support
	var endpoint string
	reqInstanceID := strings.TrimSpace(req.InstanceID)
	if reqInstanceID == "" {
		reqInstanceID = c.instanceID
	}

	if reqInstanceID != "" && (c.sendEndpoint == "/api/v1/messages/send" || c.sendEndpoint == "") {
		endpoint = fmt.Sprintf("%s/instances/%s/api/v1/messages/send", c.baseURL, reqInstanceID)
	} else if strings.HasPrefix(c.sendEndpoint, "http://") || strings.HasPrefix(c.sendEndpoint, "https://") {
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
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w (URL %s): %s", ErrEndpointNotFound, endpoint, string(body))
	}
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
