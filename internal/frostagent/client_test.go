package frostagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPClient_SendMessage_DefaultAndCustomEndpoint(t *testing.T) {
	ctx := context.Background()

	var receivedPath string
	var receivedAuth string
	var receivedBody SendMessageRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// 1. Default endpoint
	client := NewHTTPClient(Config{
		BaseURL: server.URL,
		APIKey:  "secret-key",
		Timeout: 2 * time.Second,
	})

	req := SendMessageRequest{
		Session:     "qq:private:user_123",
		Platform:    "qq",
		MessageType: "private",
		TargetID:    "user_123",
		Messages: []OutgoingMessage{
			{Type: "plain", Text: "hello"},
		},
		Metadata: map[string]any{"source": "test"},
	}

	if err := client.SendMessage(ctx, req); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	if receivedPath != "/api/v1/messages/send" {
		t.Fatalf("expected path /api/v1/messages/send, got %s", receivedPath)
	}
	if receivedAuth != "Bearer secret-key" {
		t.Fatalf("expected Bearer secret-key, got %s", receivedAuth)
	}
	if receivedBody.Platform != "qq" || len(receivedBody.Messages) != 1 || receivedBody.Messages[0].Text != "hello" {
		t.Fatalf("unexpected received body: %+v", receivedBody)
	}

	// 2. Custom relative endpoint
	clientCustom := NewHTTPClient(Config{
		BaseURL:      server.URL,
		SendEndpoint: "/api/v2/custom/send",
		APIKey:       "secret-key-2",
	})
	if err := clientCustom.SendMessage(ctx, req); err != nil {
		t.Fatalf("SendMessage custom failed: %v", err)
	}
	if receivedPath != "/api/v2/custom/send" {
		t.Fatalf("expected path /api/v2/custom/send, got %s", receivedPath)
	}

	// 3. Instance routing endpoint
	clientInstance := NewHTTPClient(Config{
		BaseURL:    server.URL,
		InstanceID: "inst_demo123",
		APIKey:     "secret-key-3",
	})
	if err := clientInstance.SendMessage(ctx, req); err != nil {
		t.Fatalf("SendMessage instance failed: %v", err)
	}
	if receivedPath != "/instances/inst_demo123/api/v1/messages/send" {
		t.Fatalf("expected path /instances/inst_demo123/api/v1/messages/send, got %s", receivedPath)
	}

	// 4. Direct Content and Attachments normalization
	contentReq := SendMessageRequest{
		Session:  "qq:private:user_456",
		Platform: "qq",
		Content:  "direct content string",
		Attachments: []Attachment{
			{
				Type:     AttachmentTypeImage,
				URL:      "https://example.com/cat.png",
				MimeType: "image/png",
			},
		},
	}
	if err := client.SendMessage(ctx, contentReq); err != nil {
		t.Fatalf("SendMessage with direct content failed: %v", err)
	}
	if len(receivedBody.Messages) != 1 || receivedBody.Messages[0].Content != "direct content string" {
		t.Fatalf("expected normalized messages, got %+v", receivedBody.Messages)
	}
	if len(receivedBody.Messages[0].Attachments) != 1 || receivedBody.Messages[0].Attachments[0].Type != AttachmentTypeImage {
		t.Fatalf("expected attachments propagated into normalized messages, got %+v", receivedBody.Messages[0].Attachments)
	}

	// 5. Validation: empty messages, content, and attachments should fail
	emptyReq := SendMessageRequest{
		Session:     "qq:private:user_123",
		Messages:    nil,
		Content:     "",
		Attachments: nil,
	}
	if err := client.SendMessage(ctx, emptyReq); err == nil {
		t.Fatal("expected error for empty messages, got nil")
	}
}

func TestHTTPClient_Errors(t *testing.T) {
	ctx := context.Background()

	// Server returning 401 Unauthorized
	server401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`unauthorized`))
	}))
	defer server401.Close()

	client := NewHTTPClient(Config{BaseURL: server401.URL})
	err := client.SendMessage(ctx, SendMessageRequest{
		Messages: []OutgoingMessage{{Type: "plain", Text: "test"}},
	})
	if err == nil {
		t.Fatal("expected error on 401 response")
	}

	// Server returning 404 Endpoint Not Found
	server404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`404 page not found`))
	}))
	defer server404.Close()

	client404 := NewHTTPClient(Config{BaseURL: server404.URL})
	err = client404.SendMessage(ctx, SendMessageRequest{
		Messages: []OutgoingMessage{{Type: "plain", Text: "test"}},
	})
	if err == nil {
		t.Fatal("expected error on 404 response")
	}
	if !errors.Is(err, ErrEndpointNotFound) {
		t.Fatalf("expected ErrEndpointNotFound, got %v", err)
	}

	// Server returning 500
	server500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`internal server error`))
	}))
	defer server500.Close()

	client500 := NewHTTPClient(Config{BaseURL: server500.URL})
	err = client500.SendMessage(ctx, SendMessageRequest{
		Messages: []OutgoingMessage{{Type: "plain", Text: "test"}},
	})
	if err == nil {
		t.Fatal("expected error on 500 response")
	}
}

func TestNormalizeSendMessageRequest(t *testing.T) {
	t.Run("session parsing with top level content", func(t *testing.T) {
		req := SendMessageRequest{
			Session: "qq:group:grp_101",
			Content: "hello world",
		}
		if err := NormalizeSendMessageRequest(&req); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req.Platform != "qq" || req.MessageType != "group" || req.TargetID != "grp_101" {
			t.Fatalf("unexpected parsed session fields: %+v", req)
		}
		if len(req.Messages) != 1 || req.Messages[0].Content != "hello world" {
			t.Fatalf("unexpected normalized messages: %+v", req.Messages)
		}
	})

	t.Run("actionscat sdk format session and messages with text", func(t *testing.T) {
		// Matches actionscat.Reply and actionscat.SendMessage payload
		req := SendMessageRequest{
			Session: "onebot:private:user_202",
			Messages: []OutgoingMessage{
				{Type: "plain", Text: "text from sdk"},
			},
		}
		if err := NormalizeSendMessageRequest(&req); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req.Platform != "onebot" || req.MessageType != "private" || req.TargetID != "user_202" {
			t.Fatalf("unexpected top-level fields: %+v", req)
		}
		if len(req.Messages) != 1 {
			t.Fatalf("expected 1 message, got %d", len(req.Messages))
		}
		msg := req.Messages[0]
		if msg.Platform != "onebot" || msg.MessageType != "private" || msg.TargetID != "user_202" {
			t.Fatalf("expected msg to inherit session routing, got %+v", msg)
		}
		if msg.Content != "text from sdk" {
			t.Fatalf("expected Content to be populated from Text, got %q", msg.Content)
		}
	})

	t.Run("media message attachments normalization", func(t *testing.T) {
		req := SendMessageRequest{
			Session: "telegram:channel:chan_303",
			Messages: []OutgoingMessage{
				{Type: "image", URL: "https://example.com/pic.png", Text: "caption text"},
			},
		}
		if err := NormalizeSendMessageRequest(&req); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(req.Messages) != 1 {
			t.Fatalf("expected 1 message, got %d", len(req.Messages))
		}
		msg := req.Messages[0]
		if msg.Content != "caption text" {
			t.Fatalf("expected caption content, got %q", msg.Content)
		}
		if len(msg.Attachments) != 1 || msg.Attachments[0].Type != AttachmentTypeImage || msg.Attachments[0].URL != "https://example.com/pic.png" {
			t.Fatalf("expected image attachment, got %+v", msg.Attachments)
		}
	})

	t.Run("validation empty request", func(t *testing.T) {
		req := SendMessageRequest{
			Session: "qq:group:grp_404",
		}
		if err := NormalizeSendMessageRequest(&req); err == nil {
			t.Fatal("expected error for empty message and content, got nil")
		}
	})
}
