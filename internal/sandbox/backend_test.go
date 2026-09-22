package sandbox

import (
	"actionscat/internal/domain"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	var customRespBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/status":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/sessions":
			if returnStatus != http.StatusNotFound {
				receivedSessionReq = gatewaySessionInitRequest{}
				_ = json.NewDecoder(r.Body).Decode(&receivedSessionReq)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(returnStatus)
			if customRespBody != nil {
				_, _ = w.Write(customRespBody)
				return
			}
			if returnStatus >= 400 {
				_, _ = w.Write([]byte(`{"error":"profile contract not supported"}`))
			} else if returnStatus == http.StatusOK || returnStatus == http.StatusCreated {
				resp := map[string]any{
					"user_uuid":            receivedSessionReq.UserUUID,
					"profile":              receivedSessionReq.Profile,
					"network":              receivedSessionReq.Network,
					"allowed_hosts":        receivedSessionReq.AllowedHosts,
					"status":               "ready",
					"runtime_callback_url": receivedSessionReq.RuntimeCallbackURL,
				}
				_ = json.NewEncoder(w).Encode(resp)
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
	customRespBody = nil
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

	// 7. Effective Runtime Callback URL from Gateway response is recorded in SessionHandle
	returnStatus = http.StatusCreated
	customRespBody = nil
	handleProxy, err := client.CreateSession(ctx, SessionRequest{
		SessionID:          "sess_proxy_effective",
		Profile:            ProfileRuntime,
		Network:            domain.NetworkPolicy{Mode: domain.NetworkModeIsolated},
		RuntimeCallbackURL: "http://host.docker.internal:7999/api/v1/runtime",
		Env: map[string]string{
			"ACTIONSCAT_RUNTIME_ENDPOINT": "http://host.docker.internal:7999/api/v1/runtime",
		},
	})
	if err != nil {
		t.Fatalf("expected successful CreateSession with proxy, got: %v", err)
	}
	if handleProxy.RuntimeCallbackURL != "http://host.docker.internal:7999/api/v1/runtime" {
		t.Fatalf("expected callback URL in handle, got %s", handleProxy.RuntimeCallbackURL)
	}

	// 8. Gateway returns 204 No Content -> MUST FAIL CLOSED with ErrSandboxUnavailable!
	returnStatus = http.StatusNoContent
	customRespBody = nil
	_, err = client.CreateSession(ctx, SessionRequest{
		SessionID: "sess_204",
		Profile:   ProfileRuntime,
	})
	if err == nil || !errors.Is(err, ErrSandboxUnavailable) {
		t.Fatalf("expected ErrSandboxUnavailable on 204 No Content, got: %v", err)
	}

	// 9. Gateway returns corrupted/invalid JSON -> MUST FAIL CLOSED with ErrSandboxUnavailable!
	returnStatus = http.StatusOK
	customRespBody = []byte("{invalid-json")
	_, err = client.CreateSession(ctx, SessionRequest{
		SessionID: "sess_bad_json",
		Profile:   ProfileRuntime,
	})
	if err == nil || !errors.Is(err, ErrSandboxUnavailable) {
		t.Fatalf("expected ErrSandboxUnavailable on bad JSON, got: %v", err)
	}

	// 10. Gateway returns user_uuid mismatch -> MUST FAIL CLOSED with ErrProfileNotSupported!
	returnStatus = http.StatusOK
	customRespBody = []byte(`{"user_uuid":"attacker-uuid","profile":"action-runtime","status":"ready"}`)
	_, err = client.CreateSession(ctx, SessionRequest{
		SessionID: "sess_uuid_mismatch",
		Profile:   ProfileRuntime,
	})
	if err == nil || !errors.Is(err, ErrProfileNotSupported) {
		t.Fatalf("expected ErrProfileNotSupported on user_uuid mismatch, got: %v", err)
	}

	// 11. Gateway returns profile mismatch -> MUST FAIL CLOSED with ErrProfileNotSupported!
	returnStatus = http.StatusOK
	uuidMismatch := client.SessionIDToUUID("sess_prof_mismatch")
	customRespBody = fmt.Appendf(nil, `{"user_uuid":%q,"profile":"minimal","status":"ready"}`, uuidMismatch)
	_, err = client.CreateSession(ctx, SessionRequest{
		SessionID: "sess_prof_mismatch",
		Profile:   ProfileRuntime,
	})
	if err == nil || !errors.Is(err, ErrProfileNotSupported) {
		t.Fatalf("expected ErrProfileNotSupported on profile mismatch, got: %v", err)
	}

	// 12. Gateway returns missing allowed host -> MUST FAIL CLOSED with ErrProfileNotSupported!
	returnStatus = http.StatusOK
	uuidAllowMismatch := client.SessionIDToUUID("sess_allow_mismatch")
	customRespBody = fmt.Appendf(nil, `{"user_uuid":%q,"profile":"action-runtime","network":"allowlist","allowed_hosts":["other.com"],"status":"ready"}`, uuidAllowMismatch)
	_, err = client.CreateSession(ctx, SessionRequest{
		SessionID: "sess_allow_mismatch",
		Profile:   ProfileRuntime,
		Network: domain.NetworkPolicy{
			Mode: domain.NetworkModeAllowlist,
			Allow: []domain.NetworkAllowRule{
				{Host: "required.com"},
			},
		},
	})
	if err == nil || !errors.Is(err, ErrProfileNotSupported) {
		t.Fatalf("expected ErrProfileNotSupported on allowlist mismatch, got: %v", err)
	}

	// 13. Gateway returns status != ready/created -> MUST FAIL CLOSED with ErrSandboxUnavailable!
	returnStatus = http.StatusOK
	uuidPending := client.SessionIDToUUID("sess_pending")
	customRespBody = fmt.Appendf(nil, `{"user_uuid":%q,"profile":"action-runtime","status":"pending"}`, uuidPending)
	_, err = client.CreateSession(ctx, SessionRequest{
		SessionID: "sess_pending",
		Profile:   ProfileRuntime,
	})
	if err == nil || !errors.Is(err, ErrSandboxUnavailable) {
		t.Fatalf("expected ErrSandboxUnavailable on status pending, got: %v", err)
	}

	// 14. Gateway returns unexpected extra allowed host -> MUST FAIL CLOSED with ErrProfileNotSupported!
	returnStatus = http.StatusOK
	uuidAllowExtra := client.SessionIDToUUID("sess_allow_extra")
	customRespBody = fmt.Appendf(nil, `{"user_uuid":%q,"profile":"action-runtime","network":"allowlist","allowed_hosts":["required.com:443","attacker.example:443"],"status":"ready"}`, uuidAllowExtra)
	_, err = client.CreateSession(ctx, SessionRequest{
		SessionID: "sess_allow_extra",
		Profile:   ProfileRuntime,
		Network: domain.NetworkPolicy{
			Mode: domain.NetworkModeAllowlist,
			Allow: []domain.NetworkAllowRule{
				{Host: "required.com", Port: 443},
			},
		},
	})
	if err == nil || !errors.Is(err, ErrProfileNotSupported) {
		t.Fatalf("expected ErrProfileNotSupported when gateway returns extra unrequested host, got: %v", err)
	}
	if !strings.Contains(err.Error(), "unexpected extra allowed host") {
		t.Fatalf("expected error message to mention unexpected extra allowed host, got: %v", err)
	}

	// 15. Gateway returns allowed hosts when mode is none -> MUST FAIL CLOSED with ErrProfileNotSupported!
	returnStatus = http.StatusOK
	uuidNoneWithHosts := client.SessionIDToUUID("sess_none_extra")
	customRespBody = fmt.Appendf(nil, `{"user_uuid":%q,"profile":"action-runtime","network":"none","allowed_hosts":["evil.com:443"],"status":"ready"}`, uuidNoneWithHosts)
	_, err = client.CreateSession(ctx, SessionRequest{
		SessionID: "sess_none_extra",
		Profile:   ProfileRuntime,
		Network: domain.NetworkPolicy{
			Mode: domain.NetworkModeNone,
		},
	})
	if err == nil || !errors.Is(err, ErrProfileNotSupported) {
		t.Fatalf("expected ErrProfileNotSupported when gateway returns allowed hosts for network:none, got: %v", err)
	}
}

func TestClient_ExportFiles_LimitEnforcement(t *testing.T) {
	ctx := context.Background()

	// Create a mock server that simulates returning chunks exceeding 64MB
	chunkCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/shell/exec":
			var req gatewayExecRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.Header().Set("Content-Type", "application/json")
			exitCode := 0

			if containsStr(req.Command, "if [ -f") {
				resp := gatewayExecResponse{
					Stdout:   "/sandbox/huge-file.bin\n",
					ExitCode: &exitCode,
				}
				_ = json.NewEncoder(w).Encode(resp)
				return
			}

			// Simulating chunk return of 512KB base64
			chunkCount++
			// 512KB of 'A' encoded in base64
			b64Chunk := make([]byte, 699052)
			for i := range b64Chunk {
				b64Chunk[i] = 'A'
			}
			resp := gatewayExecResponse{
				Stdout:   string(b64Chunk) + "\n",
				ExitCode: &exitCode,
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:          server.URL,
		AuthToken:        "tok",
		SessionNamespace: "test",
	})

	_, err := client.ExportFiles(ctx, "sess_large", []string{"huge-file.bin"})
	if err == nil {
		t.Fatal("expected ExportFiles to fail when exceeding 64MB, got nil error")
	}
	if !errors.Is(err, ErrArtifactTooLarge) {
		t.Fatalf("expected ErrArtifactTooLarge, got: %v", err)
	}
}

func TestClient_ExecEnvIsolation_DoesNotClobberContainerEnv(t *testing.T) {
	ctx := context.Background()
	var executedCmd string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/status":
			w.WriteHeader(http.StatusOK)
		case "/api/v1/sessions":
			var req gatewaySessionInitRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			resp := map[string]any{
				"user_uuid":            req.UserUUID,
				"profile":              req.Profile,
				"network":              req.Network,
				"status":               "ready",
				"runtime_callback_url": "http://172.28.0.2:3874/api/v1/sessions/" + req.UserUUID + "/callback",
			}
			_ = json.NewEncoder(w).Encode(resp)
		case "/api/v1/shell/exec":
			var req gatewayExecRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			executedCmd = req.Command
			exitCode := 0
			resp := gatewayExecResponse{ExitCode: &exitCode}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:          server.URL,
		AuthToken:        "tok",
		SessionNamespace: "test",
	})

	// 1. Create session with session-level environment (e.g. initial advertised endpoint)
	_, err := client.CreateSession(ctx, SessionRequest{
		SessionID: "sess_exec_isolation",
		Profile:   ProfileRuntime,
		Network:   domain.NetworkPolicy{Mode: domain.NetworkModeIsolated},
		Env: map[string]string{
			"ACTIONSCAT_RUNTIME_ENDPOINT": "http://stale-host-advertised:7999/api/v1/runtime",
			"SESSION_VAR":                 "initial_val",
		},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// 2. Exec without per-exec overrides: MUST NOT re-inject session env into command
	_, err = client.Exec(ctx, ExecRequest{
		SessionID: "sess_exec_isolation",
		Command:   "/sandbox/entrypoint",
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if containsStr(executedCmd, "ACTIONSCAT_RUNTIME_ENDPOINT") || containsStr(executedCmd, "SESSION_VAR") {
		t.Fatalf("Exec must not re-inject session env into command, got: %s", executedCmd)
	}
	if executedCmd != "/sandbox/entrypoint" {
		t.Fatalf("expected plain command without prepended exports, got: %s", executedCmd)
	}

	// 3. Exec with explicit per-exec overrides: ONLY per-exec overrides are prepended
	_, err = client.Exec(ctx, ExecRequest{
		SessionID: "sess_exec_isolation",
		Command:   "/sandbox/entrypoint",
		Env: map[string]string{
			"PER_EXEC_FLAG": "1",
		},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !containsStr(executedCmd, "export PER_EXEC_FLAG='1';") {
		t.Fatalf("expected per-exec override exported in command, got: %s", executedCmd)
	}
	if containsStr(executedCmd, "SESSION_VAR") {
		t.Fatalf("session env must still not be injected, got: %s", executedCmd)
	}
}

func TestClient_ExportFiles_CanonicalDeduplication_32to64MB(t *testing.T) {
	ctx := context.Background()

	const fileSize = 40 * 1024 * 1024 // 40 MiB canonical artifact (between 32 and 64 MiB)
	const chunkSize = 524288          // 512 KiB per chunk
	totalChunks := fileSize / chunkSize

	chunkRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/shell/exec":
			var req gatewayExecRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.Header().Set("Content-Type", "application/json")
			exitCode := 0

			if containsStr(req.Command, "if [ -f") {
				// Both /sandbox/out/entrypoint and out/entrypoint resolve to canonical /sandbox/out/entrypoint
				resp := gatewayExecResponse{
					Stdout:   "/sandbox/out/entrypoint\n",
					ExitCode: &exitCode,
				}
				_ = json.NewEncoder(w).Encode(resp)
				return
			}

			// Chunk extraction
			if chunkRequests < totalChunks {
				chunkRequests++
				// 512KB base64 chunk
				rawChunk := make([]byte, chunkSize)
				b64Str := base64.StdEncoding.EncodeToString(rawChunk)
				resp := gatewayExecResponse{
					Stdout:   b64Str + "\n",
					ExitCode: &exitCode,
				}
				_ = json.NewEncoder(w).Encode(resp)
			} else {
				// EOF
				resp := gatewayExecResponse{
					Stdout:   "\n",
					ExitCode: &exitCode,
				}
				_ = json.NewEncoder(w).Encode(resp)
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
	})

	// Requesting both canonical path and fallback path for a 40MB file:
	// Without deduplication, 40MB + 40MB = 80MB would incorrectly exceed the 64MB limit!
	exported, err := client.ExportFiles(ctx, "sess_dedup_test", []string{
		"/sandbox/out/entrypoint",
		"out/entrypoint",
	})
	if err != nil {
		t.Fatalf("expected 40MB export to succeed with deduplication, got: %v", err)
	}

	if len(exported["/sandbox/out/entrypoint"]) != fileSize {
		t.Fatalf("expected %d bytes for /sandbox/out/entrypoint, got %d", fileSize, len(exported["/sandbox/out/entrypoint"]))
	}
	if len(exported["out/entrypoint"]) != fileSize {
		t.Fatalf("expected %d bytes for out/entrypoint, got %d", fileSize, len(exported["out/entrypoint"]))
	}

	// Ensure file was only read once across container chunks
	if chunkRequests != totalChunks {
		t.Fatalf("expected exactly %d chunk reads, got %d", totalChunks, chunkRequests)
	}
}

func TestClient_Release_StrictParity(t *testing.T) {
	ctx := context.Background()

	var releaseStatus int
	var releaseBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/status":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/sessions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user_uuid": req["user_uuid"],
				"profile":   req["profile"],
				"network":   req["network"],
				"status":    "ready",
			})
		case "/api/v1/release":
			w.WriteHeader(releaseStatus)
			if releaseBody != "" {
				_, _ = w.Write([]byte(releaseBody))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:          server.URL,
		AuthToken:        "tok",
		SessionNamespace: "release-test",
	})

	initSession := func(sessionID string) {
		_, err := client.CreateSession(ctx, SessionRequest{
			SessionID: sessionID,
			Profile:   ProfileRuntime,
		})
		if err != nil {
			t.Fatalf("setup CreateSession failed: %v", err)
		}
		if !client.HasSession(sessionID) {
			t.Fatalf("expected client to have session metadata for %s", sessionID)
		}
	}

	// 1. HTTP 200 OK -> release succeeds and deletes local metadata
	initSession("sess_200")
	releaseStatus = http.StatusOK
	releaseBody = `{"status":"released"}`
	if err := client.Release(ctx, "sess_200"); err != nil {
		t.Fatalf("expected Release to succeed on 200 OK, got: %v", err)
	}
	if client.HasSession("sess_200") {
		t.Fatal("expected local metadata to be deleted after 200 OK release")
	}

	// 2. HTTP 204 No Content -> release succeeds and deletes local metadata
	initSession("sess_204")
	releaseStatus = http.StatusNoContent
	releaseBody = ""
	if err := client.Release(ctx, "sess_204"); err != nil {
		t.Fatalf("expected Release to succeed on 204 No Content, got: %v", err)
	}
	if client.HasSession("sess_204") {
		t.Fatal("expected local metadata to be deleted after 204 No Content release")
	}

	// 3. HTTP 404 with machine-readable session_not_found -> release succeeds (idempotent) and deletes local metadata
	initSession("sess_404_found")
	releaseStatus = http.StatusNotFound
	releaseBody = `{"code":"session_not_found","message":"Session not found on worker"}`
	if err := client.Release(ctx, "sess_404_found"); err != nil {
		t.Fatalf("expected Release to succeed on 404 with session_not_found body, got: %v", err)
	}
	if client.HasSession("sess_404_found") {
		t.Fatal("expected local metadata to be deleted after machine-readable 404 release")
	}

	// 4. HTTP 404 with generic router 404 -> MUST FAIL and PRESERVE local metadata!
	initSession("sess_404_generic")
	releaseStatus = http.StatusNotFound
	releaseBody = `404 page not found`
	if err := client.Release(ctx, "sess_404_generic"); err == nil {
		t.Fatal("expected Release to fail on generic 404, got nil")
	}
	if !client.HasSession("sess_404_generic") {
		t.Fatal("expected local metadata to be PRESERVED on generic 404 release failure")
	}

	// 5. HTTP 500 Server Error -> MUST FAIL and PRESERVE local metadata!
	initSession("sess_500")
	releaseStatus = http.StatusInternalServerError
	releaseBody = `{"error":"internal server error"}`
	if err := client.Release(ctx, "sess_500"); err == nil {
		t.Fatal("expected Release to fail on HTTP 500, got nil")
	}
	if !client.HasSession("sess_500") {
		t.Fatal("expected local metadata to be PRESERVED on HTTP 500 release failure")
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(s) > len(sub) && (s[:len(sub)] == sub || containsStr(s[1:], sub))))
}
