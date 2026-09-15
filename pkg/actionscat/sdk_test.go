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
	t.Setenv("ACTIONSCAT_EVENT_PLATFORM", "qq")
	t.Setenv("ACTIONSCAT_EVENT_GROUP_ID", "grp_101")
	t.Setenv("ACTIONSCAT_EVENT_USER_ID", "usr_202")
	t.Setenv("ACTIONSCAT_EVENT_SESSION_ID", "grp_101:usr_202")

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

	// 2. Reply test (canonical transport routing)
	err = Reply(ctx, "pong response")
	if err != nil {
		t.Fatalf("reply failed: %v", err)
	}
	if receivedSendReq["session"] != "grp_101:usr_202" {
		t.Errorf("session mismatch: %+v", receivedSendReq)
	}
	if receivedSendReq["platform"] != "qq" {
		t.Errorf("expected platform 'qq', got %+v", receivedSendReq["platform"])
	}
	if receivedSendReq["message_type"] != "group" {
		t.Errorf("expected message_type 'group', got %+v", receivedSendReq["message_type"])
	}
	if receivedSendReq["target_id"] != "grp_101" {
		t.Errorf("expected target_id 'grp_101', got %+v", receivedSendReq["target_id"])
	}

	// 3. Reply private message routing test
	t.Setenv("ACTIONSCAT_EVENT_GROUP_ID", "")
	err = Reply(ctx, "private reply")
	if err != nil {
		t.Fatalf("reply failed: %v", err)
	}
	if receivedSendReq["message_type"] != "private" || receivedSendReq["target_id"] != "usr_202" {
		t.Errorf("expected private target 'usr_202', got %+v", receivedSendReq)
	}

	// 4. SendMessage destination routing must NOT be contaminated by ambient event context!
	// Re-arm ambient group event context
	t.Setenv("ACTIONSCAT_EVENT_GROUP_ID", "ambient_grp_999")
	t.Setenv("ACTIONSCAT_EVENT_USER_ID", "ambient_usr_888")
	t.Setenv("ACTIONSCAT_EVENT_SESSION_ID", "ambient_grp_999:ambient_usr_888")

	err = SendMessage(ctx, "qq:private:target_user_123", []MessageItem{
		{Type: "plain", Text: "strictly private destination message"},
	})
	if err != nil {
		t.Fatalf("send message failed: %v", err)
	}
	if receivedSendReq["session"] != "qq:private:target_user_123" {
		t.Errorf("session mismatch: %+v", receivedSendReq["session"])
	}
	if receivedSendReq["platform"] != "qq" {
		t.Errorf("expected platform 'qq', got %+v", receivedSendReq["platform"])
	}
	if receivedSendReq["message_type"] != "private" {
		t.Errorf("expected message_type 'private' (not contaminated by ambient group), got %+v", receivedSendReq["message_type"])
	}
	if receivedSendReq["target_id"] != "target_user_123" {
		t.Errorf("expected target_id 'target_user_123' (not contaminated by ambient group), got %+v", receivedSendReq["target_id"])
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
