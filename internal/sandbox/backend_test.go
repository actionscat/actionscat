package sandbox

import (
	"actionscat/internal/domain"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFakeBackend_Lifecycle(t *testing.T) {
	ctx := context.Background()
	fb := NewFakeBackend()

	// 1. Health check
	if err := fb.Health(ctx); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	// 2. Create session
	sessHandle, err := fb.CreateSession(ctx, SessionRequest{
		SessionID: "sess_test_1",
		Profile:   ProfileGoBuilder,
		Network:   domain.NetworkPolicy{Mode: "none"},
	})
	if err != nil || sessHandle.SessionID != "sess_test_1" {
		t.Fatalf("create session failed: %v", err)
	}

	// 3. Upload files
	srcFiles := map[string][]byte{
		"main.go": []byte("package main\nfunc main() {}\n"),
	}
	if err := fb.UploadFiles(ctx, "sess_test_1", srcFiles); err != nil {
		t.Fatalf("upload files failed: %v", err)
	}

	// 4. Exec go version
	resVer, err := fb.Exec(ctx, ExecRequest{
		SessionID: "sess_test_1",
		Command:   "go version",
	})
	if err != nil || resVer.ExitCode == nil || *resVer.ExitCode != 0 {
		t.Fatalf("exec go version failed: %v, %v", err, resVer)
	}

	// 5. Exec go build
	resBuild, err := fb.Exec(ctx, ExecRequest{
		SessionID: "sess_test_1",
		Command:   "go build -o /out/entrypoint .",
	})
	if err != nil || resBuild.ExitCode == nil || *resBuild.ExitCode != 0 {
		t.Fatalf("exec go build failed: %v, %v", err, resBuild)
	}

	// 6. Export artifact
	exported, err := fb.ExportFiles(ctx, "sess_test_1", []string{"entrypoint"})
	if err != nil || len(exported["entrypoint"]) == 0 {
		t.Fatalf("export artifact failed: %v", err)
	}

	// 7. Release session
	if err := fb.Release(ctx, "sess_test_1"); err != nil {
		t.Fatalf("release session failed: %v", err)
	}

	// Exec on released session MUST fail closed
	_, err = fb.Exec(ctx, ExecRequest{
		SessionID: "sess_test_1",
		Command:   "ls",
	})
	if err == nil {
		t.Fatal("expected exec on released session to fail, but got nil")
	}

	// 8. Fail-closed on backend outage
	fb.Unavailable = true
	if err := fb.Health(ctx); err == nil {
		t.Fatal("expected Health to fail when backend unavailable")
	}
	_, err = fb.CreateSession(ctx, SessionRequest{SessionID: "sess_2"})
	if err == nil {
		t.Fatal("expected CreateSession to fail when backend unavailable")
	}
}

func TestClient_UUIDDerivationAndGatewayMock(t *testing.T) {
	ctx := context.Background()

	var receivedToken string
	var receivedUUID string
	var receivedCmd string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedToken = r.Header.Get("X-Auth-Token")
		switch r.URL.Path {
		case "/api/v1/status":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/shell/exec":
			receivedUUID = r.URL.Query().Get("user_uuid")
			var req gatewayExecRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			receivedCmd = req.Command

			exitCode := 0
			resp := gatewayExecResponse{
				Stdout:     "mock-gateway-output\n",
				ExitCode:   &exitCode,
				DurationMs: 15,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case "/api/v1/release":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := Config{
		BaseURL:          server.URL,
		AuthToken:        "secret-gateway-token",
		SessionNamespace: "test-ns",
		ClientTimeout:    5 * time.Second,
	}
	client := NewClient(cfg)

	// Test UUID derivation stability
	uuid1 := client.SessionIDToUUID("session_42")
	uuid2 := client.SessionIDToUUID("session_42")
	if uuid1 != uuid2 {
		t.Fatalf("expected deterministic UUIDs, got %s vs %s", uuid1, uuid2)
	}
	if len(uuid1) != 36 {
		t.Fatalf("invalid UUID format: %s", uuid1)
	}

	// Health check
	if err := client.Health(ctx); err != nil {
		t.Fatalf("Health() failed: %v", err)
	}
	if receivedToken != "secret-gateway-token" {
		t.Fatalf("expected token secret-gateway-token, got %s", receivedToken)
	}

	// Exec command
	res, err := client.Exec(ctx, ExecRequest{
		SessionID: "session_42",
		Command:   "echo hello",
		Env:       map[string]string{"FOO": "BAR"},
	})
	if err != nil {
		t.Fatalf("Exec() failed: %v", err)
	}
	if res.Stdout != "mock-gateway-output\n" || res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("unexpected exec result: %v", res)
	}
	if receivedUUID != uuid1 {
		t.Fatalf("expected user_uuid %s, got %s", uuid1, receivedUUID)
	}
	if !containsStr(receivedCmd, "export FOO='BAR'") {
		t.Fatalf("expected environment variable exported in command, got %s", receivedCmd)
	}

	// Release
	if err := client.Release(ctx, "session_42"); err != nil {
		t.Fatalf("Release() failed: %v", err)
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(s) > len(sub) && (s[:len(sub)] == sub || containsStr(s[1:], sub))))
}
