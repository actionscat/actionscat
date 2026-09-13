package actionscat

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestSDK_GetContextAndEnv(t *testing.T) {
	t.Setenv("ACTIONSCAT_ACTION_ID", "act_test_1")
	t.Setenv("ACTIONSCAT_RUN_ID", "run_test_1")
	t.Setenv("ACTIONSCAT_EVENT_TEXT", "hello test")
	t.Setenv("PARAM_CUSTOM", "extracted_value")

	ctx := GetContext()
	if ctx.ActionID != "act_test_1" {
		t.Errorf("expected act_test_1, got %s", ctx.ActionID)
	}
	if ctx.RunID != "run_test_1" {
		t.Errorf("expected run_test_1, got %s", ctx.RunID)
	}
	if ctx.Text != "hello test" {
		t.Errorf("expected text 'hello test', got %s", ctx.Text)
	}
	if GetEnv("PARAM_CUSTOM") != "extracted_value" {
		t.Errorf("expected PARAM_CUSTOM=extracted_value, got %s", GetEnv("PARAM_CUSTOM"))
	}
}

func TestSDK_WriteStateAndSendMessage(t *testing.T) {
	var receivedStateReq map[string]any
	var receivedSendReq map[string]any
	var receivedToken string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedToken = r.Header.Get("Authorization")
		bodyBytes, _ := io.ReadAll(r.Body)

		switch r.URL.Path {
		case "/state":
			_ = json.Unmarshal(bodyBytes, &receivedStateReq)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/frostagent/send":
			_ = json.Unmarshal(bodyBytes, &receivedSendReq)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("ACTIONSCAT_RUNTIME_ENDPOINT", srv.URL)
	t.Setenv("ACTIONSCAT_RUNTIME_TOKEN", "mock-token-xyz")
	t.Setenv("ACTIONSCAT_EVENT_SESSION_ID", "session_999")

	ctx := t.Context()

	// 1. WriteState test
	err := WriteState(ctx, "data.json", []byte(`{"value":123}`))
	if err != nil {
		t.Fatalf("write state failed: %v", err)
	}
	if receivedToken != "Bearer mock-token-xyz" {
		t.Errorf("token header mismatch: %s", receivedToken)
	}
	if receivedStateReq["path"] != "data.json" || receivedStateReq["data"] != `{"value":123}` {
		t.Errorf("state payload mismatch: %+v", receivedStateReq)
	}

	// 2. Reply test
	err = Reply(ctx, "pong response")
	if err != nil {
		t.Fatalf("reply failed: %v", err)
	}
	if receivedSendReq["session"] != "session_999" {
		t.Errorf("session mismatch: %+v", receivedSendReq)
	}
}

func TestSDK_ErrorsWhenNotConfigured(t *testing.T) {
	_ = os.Unsetenv("ACTIONSCAT_RUNTIME_ENDPOINT")
	_ = os.Unsetenv("ACTIONSCAT_RUNTIME_TOKEN")

	ctx := t.Context()
	err := WriteState(ctx, "foo.json", []byte("bar"))
	if err != ErrNotConfigured {
		t.Errorf("expected ErrNotConfigured, got %v", err)
	}

	err = SendMessage(ctx, "session", nil)
	if err != ErrNotConfigured {
		t.Errorf("expected ErrNotConfigured, got %v", err)
	}
}
