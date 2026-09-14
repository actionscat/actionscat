package sandbox

import (
	"actionscat/internal/domain"
	"context"
	"encoding/json"
	"errors"
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

func TestClient_CreateSession_ContractFailClosed(t *testing.T) {
	ctx := context.Background()

	var receivedSessionReq gatewaySessionInitRequest
	var returnStatus int = http.StatusCreated

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/status":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/sessions":
			if returnStatus != http.StatusNotFound {
				_ = json.NewDecoder(r.Body).Decode(&receivedSessionReq)
			}
			w.WriteHeader(returnStatus)
			if returnStatus >= 400 {
				_, _ = w.Write([]byte(`{"error":"profile contract not supported"}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:          server.URL,
		AuthToken:        "tok",
		SessionNamespace: "test",
		ClientTimeout:    2 * time.Second,
	})

	// 1. Success with profile and advertised callback URL
	returnStatus = http.StatusCreated
	handle, err := client.CreateSession(ctx, SessionRequest{
		SessionID:          "sess_1",
		Profile:            ProfileRuntime,
		Network:            domain.NetworkPolicy{Mode: domain.NetworkModeIsolated},
		RuntimeCallbackURL: "http://host.docker.internal:7999/api/v1/runtime",
	})
	if err != nil {
		t.Fatalf("expected successful CreateSession, got error: %v", err)
	}
	if handle == nil || handle.SessionID != "sess_1" {
		t.Fatalf("unexpected handle: %+v", handle)
	}
	if receivedSessionReq.Profile != ProfileRuntime ||
		receivedSessionReq.Network != domain.NetworkModeIsolated ||
		receivedSessionReq.RuntimeCallbackURL != "http://host.docker.internal:7999/api/v1/runtime" {
		t.Fatalf("gateway did not receive expected contract parameters: %+v", receivedSessionReq)
	}

	// 2. Gateway returns 404 -> MUST FAIL CLOSED with ErrProfileNotSupported!
	returnStatus = http.StatusNotFound
	_, err = client.CreateSession(ctx, SessionRequest{
		SessionID: "sess_2",
		Profile:   ProfileGoBuilder,
	})
	if err == nil {
		t.Fatal("expected error on 404 response, got nil")
	}
	if !errors.Is(err, ErrProfileNotSupported) {
		t.Fatalf("expected ErrProfileNotSupported on 404, got: %v", err)
	}

	// 3. Gateway returns 400/422 -> MUST FAIL CLOSED with ErrProfileNotSupported!
	returnStatus = http.StatusBadRequest
	_, err = client.CreateSession(ctx, SessionRequest{
		SessionID: "sess_3",
		Profile:   ProfileMinimal,
	})
	if err == nil {
		t.Fatal("expected error on 400 response, got nil")
	}
	if !errors.Is(err, ErrProfileNotSupported) {
		t.Fatalf("expected ErrProfileNotSupported on 400, got: %v", err)
	}

	// 4. Invalid profile locally -> MUST FAIL CLOSED with ErrProfileNotSupported!
	_, err = client.CreateSession(ctx, SessionRequest{
		SessionID: "sess_4",
		Profile:   "unsupported-arbitrary-profile",
	})
	if err == nil {
		t.Fatal("expected error for invalid profile, got nil")
	}
	if !errors.Is(err, ErrProfileNotSupported) {
		t.Fatalf("expected ErrProfileNotSupported for invalid profile, got: %v", err)
	}

	// 5. NetworkPolicy.Allow propagation across client boundary
	returnStatus = http.StatusCreated
	_, err = client.CreateSession(ctx, SessionRequest{
		SessionID: "sess_allowlist",
		Profile:   ProfileRuntime,
		Network: domain.NetworkPolicy{
			Mode: domain.NetworkModeAllowlist,
			Allow: []domain.NetworkAllowRule{
				{Host: "api.example.com", Port: 443},
				{Host: "10.0.0.8", Port: 22},
				{Host: "plain-host"},
			},
		},
	})
	if err != nil {
		t.Fatalf("expected successful allowlist CreateSession, got error: %v", err)
	}
	if receivedSessionReq.Network != domain.NetworkModeAllowlist {
		t.Fatalf("expected network mode allowlist, got %s", receivedSessionReq.Network)
	}
	expectedHosts := []string{"api.example.com:443", "10.0.0.8:22", "plain-host"}
	if len(receivedSessionReq.AllowedHosts) != len(expectedHosts) {
		t.Fatalf("expected %d allowed hosts, got %d: %+v", len(expectedHosts), len(receivedSessionReq.AllowedHosts), receivedSessionReq.AllowedHosts)
	}
	for i, h := range expectedHosts {
		if receivedSessionReq.AllowedHosts[i] != h {
			t.Fatalf("expected allowed host %s at %d, got %s", h, i, receivedSessionReq.AllowedHosts[i])
		}
	}
	if len(receivedSessionReq.NetworkRules) != 3 {
		t.Fatalf("expected 3 structured network rules, got %d", len(receivedSessionReq.NetworkRules))
	}

	// 6. NetworkModeAllowlist without allow rules MUST FAIL CLOSED
	_, err = client.CreateSession(ctx, SessionRequest{
		SessionID: "sess_empty_allowlist",
		Profile:   ProfileRuntime,
		Network: domain.NetworkPolicy{
			Mode: domain.NetworkModeAllowlist,
		},
	})
	if err == nil {
		t.Fatal("expected empty allowlist to fail closed, got nil")
	}
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest for empty allowlist, got: %v", err)
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(s) > len(sub) && (s[:len(sub)] == sub || containsStr(s[1:], sub))))
}
