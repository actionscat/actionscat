package test

import (
	"actionscat/internal/api"
	"actionscat/internal/domain"
	"actionscat/internal/frostagent"
	"actionscat/internal/sandbox"
	"actionscat/internal/store"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// gatewaySessionInitRequest represents the code-interpreter /api/v1/sessions contract.
type gatewaySessionInitRequest struct {
	SessionID          string            `json:"session_id"`
	UserUUID           string            `json:"user_uuid"`
	Profile            string            `json:"profile"`
	Network            string            `json:"network"`
	AllowedHosts       []string          `json:"allowed_hosts,omitempty"`
	RuntimeCallbackURL string            `json:"runtime_callback_url,omitempty"`
	CPULimit           float64           `json:"cpu_limit,omitempty"`
	MemoryLimitMB      int64             `json:"memory_limit_mb,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
}

type gatewayExecRequest struct {
	Command string            `json:"command"`
	Cwd     string            `json:"cwd"`
	Timeout float64           `json:"timeout"`
	Env     map[string]string `json:"env"`
}

type gatewayExecResponse struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   *int   `json:"exit_code"`
	TimedOut   bool   `json:"timed_out"`
	DurationMs int    `json:"duration_ms"`
}

// setupMockCodeInterpreterServer creates a real HTTP server implementing code-interpreter's
// gateway API: /api/v1/status, /api/v1/sessions, /api/v1/shell/exec, /api/v1/release.
func setupMockCodeInterpreterServer(t *testing.T, expectedAuthToken string) (*httptest.Server, *mockSandboxState) {
	t.Helper()
	state := &mockSandboxState{
		sessions: make(map[string]gatewaySessionInitRequest),
	}

	mux := http.NewServeMux()

	// 1. Status
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// 2. Sessions (POST /api/v1/sessions)
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Auth-Token") != expectedAuthToken {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		var req gatewaySessionInitRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}

		// Contract Validation: Fail closed on invalid profile or network
		validProfiles := map[string]bool{
			sandbox.ProfileGoBuilder: true,
			sandbox.ProfileRuntime:   true,
			sandbox.ProfileMinimal:   true,
		}
		if !validProfiles[req.Profile] {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"unsupported profile contract: ` + req.Profile + `"}`))
			return
		}

		state.mu.Lock()
		state.sessions[req.UserUUID] = req
		state.lastProfile = req.Profile
		state.lastNetwork = req.Network
		state.lastCallbackURL = req.RuntimeCallbackURL
		state.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"session_id":%q,"user_uuid":%q,"status":"created"}`, req.UserUUID, req.UserUUID)
	})

	// 3. Shell Exec (POST /api/v1/shell/exec)
	mux.HandleFunc("/api/v1/shell/exec", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Auth-Token") != expectedAuthToken {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		var req gatewayExecRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}

		zeroExit := 0
		resp := gatewayExecResponse{
			Stdout:   "mock execution success\n",
			ExitCode: &zeroExit,
		}

		// Check if this execution is a runtime Action execution
		// When running the action runtime entrypoint, the sandboxed worker executes the action code
		// which performs runtime callbacks to ActionsCat Core:
		// 1. state.write callback
		// 2. frostagent.sendmsg callback
		userUUID := r.URL.Query().Get("user_uuid")
		state.mu.Lock()
		sessReq := state.sessions[userUUID]
		state.mu.Unlock()

		runtimeEndpoint := sessReq.Env["ACTIONSCAT_RUNTIME_ENDPOINT"]
		runtimeToken := sessReq.Env["ACTIONSCAT_RUNTIME_TOKEN"]

		if runtimeEndpoint != "" && runtimeToken != "" && strings.Contains(req.Command, "chmod +x /sandbox/") {
			client := &http.Client{Timeout: 5 * time.Second}

			// 1. Security Invariant check: Sandbox attempting to specify instance_id MUST be rejected with HTTP 400
			evilFaBody := `{
				"instance_id": "malicious_instance_override",
				"platform": "qq",
				"message_type": "group",
				"target_id": "mock_target_group_999",
				"messages": [
					{"type": "plain", "text": "exploit attempt"}
				]
			}`
			evilReq, _ := http.NewRequest(http.MethodPost, runtimeEndpoint+"/frostagent/send", strings.NewReader(evilFaBody))
			evilReq.Header.Set("Content-Type", "application/json")
			evilReq.Header.Set("Authorization", "Bearer "+runtimeToken)
			evilResp, err := client.Do(evilReq)
			if err != nil || evilResp.StatusCode != http.StatusBadRequest {
				errExit := 1
				resp.ExitCode = &errExit
				resp.Stderr = fmt.Sprintf("expected HTTP 400 on instance_id override attempt, got code=%v, err=%v", evilResp.StatusCode, err)
			} else {
				// 2. Simulate Action executing: Action calls Core Runtime API to write persistent state
				stateBody := `{"path":"summary.json","data":"{\"status\":\"processed\",\"count\":100}"}`
				stateReq, _ := http.NewRequest(http.MethodPost, runtimeEndpoint+"/state", strings.NewReader(stateBody))
				stateReq.Header.Set("Content-Type", "application/json")
				stateReq.Header.Set("Authorization", "Bearer "+runtimeToken)
				stateResp, err := client.Do(stateReq)
				if err != nil || stateResp.StatusCode != http.StatusOK {
					errExitState := 1
					resp.ExitCode = &errExitState
					resp.Stderr = fmt.Sprintf("failed to write state: err=%v, code=%v", err, stateResp.StatusCode)
				} else {
					// 3. Simulate Action executing: Action calls Core Runtime API to send message via FrostAgent.
					// CRITICAL CONTRACT CHECK: Test canonical ActionsCat SDK payload format:
					// {"session": "qq:group:mock_target_group_999", "messages": [{"type": "plain", "text": "..."}]}
					faBody := `{
						"session": "qq:group:mock_target_group_999",
						"messages": [
							{"type": "plain", "text": "E2E: Action executed successfully and state updated."}
						]
					}`
					faReq, _ := http.NewRequest(http.MethodPost, runtimeEndpoint+"/frostagent/send", strings.NewReader(faBody))
					faReq.Header.Set("Content-Type", "application/json")
					faReq.Header.Set("Authorization", "Bearer "+runtimeToken)
					faResp, err := client.Do(faReq)
					if err != nil || faResp.StatusCode != http.StatusOK {
						errExitFA := 1
						resp.ExitCode = &errExitFA
						resp.Stderr = fmt.Sprintf("failed to send frostagent message: err=%v, code=%v", err, faResp.StatusCode)
					}
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// 4. Release (POST /api/v1/release)
	mux.HandleFunc("/api/v1/release", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, state
}

type mockSandboxState struct {
	mu              sync.Mutex
	sessions        map[string]gatewaySessionInitRequest
	lastProfile     string
	lastNetwork     string
	lastCallbackURL string
}

// setupMockFrostAgentServer creates an authenticated FrostAgent server implementing
// /instances/{id}/api/v1/messages/send and tracking delivered messages.
func setupMockFrostAgentServer(t *testing.T, expectedAPIKey, expectedInstanceID string) (*httptest.Server, *mockFrostAgentState) {
	t.Helper()
	state := &mockFrostAgentState{
		deliveredMessages: make([]frostagent.SendMessageRequest, 0),
	}

	mux := http.NewServeMux()
	expectedPath := fmt.Sprintf("/instances/%s/api/v1/messages/send", expectedInstanceID)

	mux.HandleFunc(expectedPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Verify API Key
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+expectedAPIKey {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		var req frostagent.SendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}

		state.mu.Lock()
		state.deliveredMessages = append(state.deliveredMessages, req)
		state.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","delivered":true}`))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, state
}

type mockFrostAgentState struct {
	mu                sync.Mutex
	deliveredMessages []frostagent.SendMessageRequest
}

// TestActionsCat_HTTPContractWiring verifies ActionsCat internal cross-component
// HTTP contract wiring, capability token generation/revocation lifecycle, and proxy routing:
//
//	Level 1 CI Fast Contract Test:
//	1. Session creation & NetworkPolicy.Allow propagation across client boundary
//	2. Profile fail-closed handling on builder and runtime worker provisioning
//	3. Single-run capability token injection, scope verification, and immediate post-run revocation
//	4. Runtime callback handling (StateStore write & server-side bound FrostAgent proxy delivery)
//	5. Privilege escalation defenses (rejection of sandbox-specified instance_id, token scoping)
func TestActionsCat_HTTPContractWiring(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	now := time.Now().UTC()

	tempDir := t.TempDir()
	db, err := store.OpenDB(filepath.Join(tempDir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	st := store.NewSQLiteStore(db)
	fs := store.NewFileStore(tempDir)
	ss := store.NewStateStore(tempDir)

	// 1. Setup Mock Code-Interpreter Gateway Server
	sandboxToken := "secret-sandbox-token-xyz"
	sandboxServer, sbState := setupMockCodeInterpreterServer(t, sandboxToken)

	sbClient := sandbox.NewClient(sandbox.Config{
		BaseURL:          sandboxServer.URL,
		AuthToken:        sandboxToken,
		SessionNamespace: "e2e-ns",
		ClientTimeout:    5 * time.Second,
	})

	// 2. Setup Mock FrostAgent Server with Instance Routing
	frostAgentKey := "secret-frostagent-key-abc"
	instanceID := "inst_prod_fox01"
	faServer, faState := setupMockFrostAgentServer(t, frostAgentKey, instanceID)

	faClient := frostagent.NewHTTPClient(frostagent.Config{
		BaseURL:      faServer.URL,
		InstanceID:   instanceID,
		APIKey:       frostAgentKey,
		SendEndpoint: "/api/v1/messages/send",
		Timeout:      5 * time.Second,
	})

	// 3. Setup ActionsCat Core Server
	mgmtToken := "test-management-token-123"
	dispatchToken := "test-dispatch-token-456"

	coreServer := api.NewServer(api.ServerConfig{
		Store:                     st,
		FileStore:                 fs,
		StateStore:                ss,
		Sandbox:                   sbClient,
		FrostAgent:                faClient,
		RuntimeEndpoint:           "http://will-be-updated/api/v1/runtime",
		AdvertisedRuntimeEndpoint: "http://will-be-updated/api/v1/runtime",
		RunnerWorkers:             2,
		ManagementToken:           mgmtToken,
		DispatchToken:             dispatchToken,
	})

	coreRouter := coreServer.SetupRouter()
	coreHTTPServer := httptest.NewServer(coreRouter)
	t.Cleanup(coreHTTPServer.Close)

	// Update real runtime endpoints to match httptest URL
	realRuntimeEndpoint := coreHTTPServer.URL + "/api/v1/runtime"
	coreServer.Runner.SetEndpoints(realRuntimeEndpoint, realRuntimeEndpoint)

	// Start asynchronous background worker pool
	coreServer.Runner.Start(ctx)
	t.Cleanup(func() { coreServer.Runner.Stop() })

	// -------------------------------------------------------------
	// STEP A: Create Action & Immutable Version with Source Code
	// -------------------------------------------------------------
	actionID := "act_weather_reporter"
	act := &domain.Action{
		ID:             actionID,
		Name:           "Weather Reporter Action",
		MaxConcurrency: 2,
		Enabled:        true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := st.CreateAction(ctx, act); err != nil {
		t.Fatalf("create action: %v", err)
	}

	srcFiles := map[string][]byte{
		"main.go": []byte(`package main
import "fmt"
func main() { fmt.Println("Action compiled and executed.") }
`),
		"go.mod": []byte("module weather\ngo 1.25\n"),
	}
	digest, path, err := fs.SaveSourceBundle(actionID, "ver_1", srcFiles)
	if err != nil {
		t.Fatalf("save source bundle: %v", err)
	}

	ver := &domain.ActionVersion{
		ID:            "ver_1",
		ActionID:      actionID,
		VersionNumber: 1,
		SourceDigest:  digest,
		SourcePath:    path,
		RuntimeSpec: domain.RuntimeSpec{
			Entrypoint:     "entrypoint",
			TimeoutSeconds: 10,
			Network:        domain.NetworkPolicy{Mode: domain.NetworkModeIsolated},
		},
		RuntimeCapabilities: []string{
			domain.ScopeStateWrite,
			domain.ScopeFrostAgentSendMsg,
		},
		CreatedAt: now,
	}
	if err := st.CreateVersion(ctx, ver); err != nil {
		t.Fatalf("create version: %v", err)
	}

	// -------------------------------------------------------------
	// STEP B: Build Version using Code-Interpreter 'go-builder' profile
	// -------------------------------------------------------------
	buildHandle, err := sbClient.CreateSession(ctx, sandbox.SessionRequest{
		SessionID: "build-ver_1",
		Profile:   sandbox.ProfileGoBuilder,
		Network:   domain.NetworkPolicy{Mode: domain.NetworkModeNone},
	})
	if err != nil {
		t.Fatalf("create builder session failed: %v", err)
	}
	if buildHandle == nil || sbState.lastProfile != sandbox.ProfileGoBuilder {
		t.Fatalf("expected profile go-builder on build session, got %s", sbState.lastProfile)
	}

	// Store compiled artifact bundle with a non-recursive entrypoint
	artDigest, artPath, artSize, err := fs.SaveArtifactBundle(actionID, ver.ID, "bld_1", map[string][]byte{
		"entrypoint": []byte("#!/bin/sh\necho 'Action entrypoint starting...'\nexit 0\n"),
	})
	if err != nil {
		t.Fatalf("save artifact bundle: %v", err)
	}

	bld := &domain.ArtifactBuild{
		ID:             "bld_1",
		ActionID:       actionID,
		VersionID:      ver.ID,
		BuildNumber:    1,
		Status:         domain.BuildStatusSucceeded,
		ArtifactDigest: artDigest,
		ArtifactPath:   artPath,
		ArtifactSize:   artSize,
		CreatedAt:      now,
	}
	if err := st.CreateBuild(ctx, bld); err != nil {
		t.Fatalf("create build: %v", err)
	}
	if err := st.SetActiveBuild(ctx, actionID, ver.ID, bld.ID); err != nil {
		t.Fatalf("set active build: %v", err)
	}

	// -------------------------------------------------------------
	// STEP C: Register Matcher & Dispatch Ingress Event
	// -------------------------------------------------------------
	m := &domain.Matcher{
		ID:          "m_weather",
		ActionID:    actionID,
		Name:        "Weather Matcher",
		MatchType:   domain.MatchTypeContains,
		Pattern:     "weather",
		TargetField: "text",
		Priority:    10,
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := st.CreateMatcher(ctx, m); err != nil {
		t.Fatalf("create matcher: %v", err)
	}

	// Dispatch event through POST /v1/dispatch with dispatch token authentication
	dispatchPayload := domain.MessageEvent{
		Platform:  "qq",
		SessionID: "group:mock_target_group_999",
		UserID:    "mock_user_123",
		Text:      "Please check the weather today",
	}
	dispatchBytes, _ := json.Marshal(dispatchPayload)

	req, _ := http.NewRequest(http.MethodPost, coreHTTPServer.URL+"/v1/dispatch", bytes.NewReader(dispatchBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+dispatchToken)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("dispatch request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 OK on dispatch, got %d: %s", resp.StatusCode, string(b))
	}

	var dispatchResp api.DispatchResponse
	_ = json.NewDecoder(resp.Body).Decode(&dispatchResp)
	if !dispatchResp.OK || !dispatchResp.Matched || len(dispatchResp.RunIDs) != 1 {
		t.Fatalf("unexpected dispatch response: %+v", dispatchResp)
	}

	runID := dispatchResp.RunIDs[0]

	// -------------------------------------------------------------
	// STEP D: Wait for Runner to Claim & Complete the Run
	// -------------------------------------------------------------
	var finalRun *domain.Run
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, err := st.GetRun(ctx, runID)
		if err == nil && (r.Status == domain.RunStatusSucceeded || r.Status == domain.RunStatusFailed) {
			finalRun = r
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if finalRun == nil {
		t.Fatalf("run %s did not reach terminal status before timeout", runID)
	}
	if finalRun.Status != domain.RunStatusSucceeded {
		t.Fatalf("run failed with status %s: error=%s", finalRun.Status, finalRun.ErrorMessage)
	}

	// -------------------------------------------------------------
	// STEP E: Verify Chain 1 (code-interpreter action-runtime -> Runtime Callback -> state.write)
	// -------------------------------------------------------------
	sbState.mu.Lock()
	lastProfile := sbState.lastProfile
	lastNetwork := sbState.lastNetwork
	lastCallbackURL := sbState.lastCallbackURL
	sbState.mu.Unlock()

	if lastProfile != sandbox.ProfileRuntime {
		t.Fatalf("expected runtime session profile %s, got %s", sandbox.ProfileRuntime, lastProfile)
	}
	if lastNetwork != domain.NetworkModeIsolated {
		t.Fatalf("expected runtime session network mode %s, got %s", domain.NetworkModeIsolated, lastNetwork)
	}
	if lastCallbackURL != realRuntimeEndpoint {
		t.Fatalf("expected runtime callback URL %s, got %s", realRuntimeEndpoint, lastCallbackURL)
	}

	// Verify State Persistence: state file 'summary.json' must be written in StateStore
	stateBytes, err := ss.ReadState(actionID, "summary.json")
	if err != nil {
		t.Fatalf("expected state file summary.json to be persisted, got error: %v", err)
	}
	var stateData map[string]any
	if err := json.Unmarshal(stateBytes, &stateData); err != nil {
		t.Fatalf("failed to unmarshal persisted state: %v", err)
	}
	if stateData["status"] != "processed" || stateData["count"] != float64(100) {
		t.Fatalf("persisted state content mismatch: %+v", stateData)
	}

	// -------------------------------------------------------------
	// STEP F: Verify Chain 2 (Action runtime -> Trusted Proxy -> FrostAgent -> Dispatcher -> Adapter)
	// -------------------------------------------------------------
	faState.mu.Lock()
	deliveredCount := len(faState.deliveredMessages)
	var deliveredMsg frostagent.SendMessageRequest
	if deliveredCount > 0 {
		deliveredMsg = faState.deliveredMessages[0]
	}
	faState.mu.Unlock()

	if deliveredCount != 1 {
		t.Fatalf("expected 1 delivered message to FrostAgent, got %d", deliveredCount)
	}
	if deliveredMsg.Platform != "qq" || deliveredMsg.TargetID != "mock_target_group_999" {
		t.Fatalf("unexpected delivered message metadata: %+v", deliveredMsg)
	}
	if len(deliveredMsg.Messages) != 1 || deliveredMsg.Messages[0].Text != "E2E: Action executed successfully and state updated." {
		t.Fatalf("unexpected message content: %+v", deliveredMsg)
	}

	// -------------------------------------------------------------
	// STEP G: Verify Cryptographic Capability Token Revocation
	// -------------------------------------------------------------
	// Capability tokens must be revoked immediately upon run termination
	tokens, err := st.ListTokensForRun(ctx, runID)
	if err != nil {
		t.Fatalf("list tokens for run: %v", err)
	}
	if len(tokens) == 0 {
		t.Fatalf("expected token for run to exist in database, found 0")
	}
	for _, tok := range tokens {
		if tok.IsValid(time.Now().UTC()) {
			t.Fatalf("expected token %s to be revoked after run completed, but it is still valid", tok.ID)
		}
		if tok.RevokedAt == nil {
			t.Fatalf("expected token %s RevokedAt to be set, but it was nil", tok.ID)
		}
	}

	// -------------------------------------------------------------
	// STEP H: Verify NetworkPolicy.Allow propagation to Sandbox Gateway
	// -------------------------------------------------------------
	allowlistHandle, err := sbClient.CreateSession(ctx, sandbox.SessionRequest{
		SessionID: "sess_verify_allowlist",
		Profile:   sandbox.ProfileRuntime,
		Network: domain.NetworkPolicy{
			Mode: domain.NetworkModeAllowlist,
			Allow: []domain.NetworkAllowRule{
				{Host: "api.github.com", Port: 443},
				{Host: "192.168.1.100", Port: 8080},
			},
		},
	})
	if err != nil {
		t.Fatalf("create allowlist session failed: %v", err)
	}
	if allowlistHandle == nil {
		t.Fatal("expected non-nil handle for allowlist session")
	}

	userUUID := sbClient.SessionIDToUUID("sess_verify_allowlist")
	sbState.mu.Lock()
	allowSess := sbState.sessions[userUUID]
	sbState.mu.Unlock()

	if allowSess.Network != domain.NetworkModeAllowlist {
		t.Fatalf("expected network mode allowlist, got %s", allowSess.Network)
	}
	if len(allowSess.AllowedHosts) != 2 || allowSess.AllowedHosts[0] != "api.github.com:443" || allowSess.AllowedHosts[1] != "192.168.1.100:8080" {
		t.Fatalf("unexpected allowed hosts passed to gateway: %+v", allowSess.AllowedHosts)
	}
}
