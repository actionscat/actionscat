package api_test

import (
	"actionscat/internal/api"
	"actionscat/internal/domain"
	"actionscat/internal/frostagent"
	"actionscat/internal/runner"
	"actionscat/internal/sandbox"
	"actionscat/internal/store"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

const testMgmtToken = "mgmt_secret_key_123"

func setupTestServer(t *testing.T) (*api.Server, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	db, err := store.OpenDB(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("OpenDB failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	st := store.NewSQLiteStore(db)
	fs := store.NewFileStore(tmpDir)
	state := store.NewStateStore(tmpDir)
	sb := sandbox.NewFakeBackend()

	srv := api.NewServer(api.ServerConfig{
		Store:           st,
		FileStore:       fs,
		StateStore:      state,
		Sandbox:         sb,
		FrostAgent:      frostagent.NewHTTPClient(frostagent.Config{BaseURL: "http://127.0.0.1:8000"}),
		RuntimeEndpoint: "http://127.0.0.1:7999/api/v1/runtime",
		RunnerWorkers:   2,
		ManagementToken: testMgmtToken,
	})

	router := srv.SetupRouter()
	return srv, router
}

func authReq(req *http.Request) *http.Request {
	req.Header.Set("Authorization", "Bearer "+testMgmtToken)
	return req
}

func TestManagementAPI_Authentication(t *testing.T) {
	srv, router := setupTestServer(t)
	ctx := context.Background()

	// 1. Missing Token -> 401 Unauthorized
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/actions", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for missing token, got %d", w.Code)
	}

	// 2. Forged Management Token -> 401 Unauthorized
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions", nil)
	req.Header.Set("Authorization", "Bearer invalid-forged-token")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for forged token, got %d", w.Code)
	}

	// 3. Dual-Domain Privilege Escalation: Run Capability Token presented to Management API -> 403 Forbidden!
	now := time.Now().UTC()
	testAct := &domain.Action{
		ID:             "act_auth_test",
		Name:           "auth-test",
		MaxConcurrency: 1,
		Enabled:        true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := srv.Store.CreateAction(ctx, testAct); err != nil {
		t.Fatalf("create action: %v", err)
	}
	testVer := &domain.ActionVersion{
		ID:            "ver_auth_test",
		ActionID:      testAct.ID,
		VersionNumber: 1,
		CreatedAt:     now,
	}
	if err := srv.Store.CreateVersion(ctx, testVer); err != nil {
		t.Fatalf("create version: %v", err)
	}
	testBld := &domain.ArtifactBuild{
		ID:          "bld_auth_test",
		ActionID:    testAct.ID,
		VersionID:   testVer.ID,
		BuildNumber: 1,
		Status:      domain.BuildStatusSucceeded,
		CreatedAt:   now,
	}
	if err := srv.Store.CreateBuild(ctx, testBld); err != nil {
		t.Fatalf("create build: %v", err)
	}
	testRun := &domain.Run{
		ID:              "run_auth_test",
		ActionID:        testAct.ID,
		ActionVersionID: testVer.ID,
		ArtifactBuildID: testBld.ID,
		Status:          domain.RunStatusRunning,
		TriggerType:     domain.TriggerTypeManual,
		CreatedAt:       now,
	}
	if err := srv.Store.CreateRun(ctx, testRun); err != nil {
		t.Fatalf("create run: %v", err)
	}

	rawRunToken, hash, err := domain.GenerateRawToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	runTok := &domain.RunToken{
		ID:        "tok_test_run",
		TokenHash: hash,
		RunID:     testRun.ID,
		ActionID:  testAct.ID,
		Scopes:    []string{domain.ScopeStateWrite},
		ExpiresAt: time.Now().Add(1 * time.Hour),
		CreatedAt: time.Now(),
	}
	if err := srv.Store.CreateRunToken(ctx, runTok); err != nil {
		t.Fatalf("create run token: %v", err)
	}

	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions", nil)
	req.Header.Set("Authorization", "Bearer "+rawRunToken)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for capability token on management API, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Valid Management Token -> 200 OK
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions", nil)
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for valid management token, got %d: %s", w.Code, w.Body.String())
	}

	// 5. Alternate header X-Actionscat-Management-Token -> 200 OK
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions", nil)
	req.Header.Set("X-Actionscat-Management-Token", testMgmtToken)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK with X-Actionscat-Management-Token, got %d", w.Code)
	}
}

func TestManagementAPI_TokenLeakageProtection(t *testing.T) {
	srv, router := setupTestServer(t)
	ctx := context.Background()

	// Create action and version
	createBody, _ := json.Marshal(map[string]any{
		"name": "leak-test-action",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/actions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	router.ServeHTTP(w, req)
	var act domain.Action
	_ = json.Unmarshal(w.Body.Bytes(), &act)

	versionBody, _ := json.Marshal(map[string]any{
		"files": map[string]string{
			"main.go": "package main\nfunc main() {}\n",
		},
	})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/actions/"+act.ID+"/versions", bytes.NewReader(versionBody))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	router.ServeHTTP(w, req)
	var ver domain.ActionVersion
	_ = json.Unmarshal(w.Body.Bytes(), &ver)

	// Build and activate
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/actions/"+act.ID+"/versions/"+ver.ID+"/builds", nil)
	authReq(req)
	router.ServeHTTP(w, req)
	var bld domain.ArtifactBuild
	_ = json.Unmarshal(w.Body.Bytes(), &bld)

	activateBody, _ := json.Marshal(map[string]any{
		"version_id": ver.ID,
		"build_id":   bld.ID,
	})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/actions/"+act.ID+"/active-build", bytes.NewReader(activateBody))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	router.ServeHTTP(w, req)

	// Prepare Run and verify planned environment
	run, err := srv.Runner.PrepareRun(ctx, runner.CreateRunRequest{
		ActionID:    act.ID,
		TriggerType: domain.TriggerTypeManual,
	})
	if err != nil {
		t.Fatalf("prepare run failed: %v", err)
	}

	// CRITICAL SECURITY INVARIANT:
	// Verify that Management Token or credentials NEVER leak into PlannedEnv or sandbox execution!
	for k, v := range run.PlannedEnv {
		if v == testMgmtToken || k == "ACTIONSCAT_MANAGEMENT_TOKEN" || k == "ACTIONSCAT_API_KEY" {
			t.Fatalf("CRITICAL SECURITY VULNERABILITY: Management credential %s leaked into Run PlannedEnv: %s=%s", k, k, v)
		}
	}
}

func TestManagementAPI_SourceEncodingProtocol(t *testing.T) {
	srv, router := setupTestServer(t)

	// Create action
	createBody, _ := json.Marshal(map[string]any{
		"name": "encoding-test-action",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/actions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	router.ServeHTTP(w, req)
	var act domain.Action
	_ = json.Unmarshal(w.Body.Bytes(), &act)

	// Case 1: Valid UTF-8 string that resembles Base64 (e.g. "YWJj" which is base64 for "abc")
	// In the new protocol, without explicit encoding declaration, it MUST NOT be sniffed or decoded!
	versionBody, _ := json.Marshal(map[string]any{
		"files": map[string]string{
			"resemble_base64.txt": "YWJj",
			"normal.go":           "package main\n",
		},
	})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/actions/"+act.ID+"/versions", bytes.NewReader(versionBody))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", w.Code, w.Body.String())
	}
	var ver domain.ActionVersion
	_ = json.Unmarshal(w.Body.Bytes(), &ver)

	// Read files from fileStore and assert content was NOT decoded
	savedFiles, err := srv.FileStore.ReadSourceBundle(act.ID, ver.ID)
	if err != nil {
		t.Fatalf("read source bundle: %v", err)
	}
	if string(savedFiles["resemble_base64.txt"]) != "YWJj" {
		t.Fatalf("content-guessing bug detected: expected literal 'YWJj', got %q", string(savedFiles["resemble_base64.txt"]))
	}

	// Case 2: Explicit encoding "base64" for binary data
	binaryData := []byte{0x00, 0xFF, 0xFE, 0x12, 0x34}
	b64Data := base64.StdEncoding.EncodeToString(binaryData)
	versionBody2, _ := json.Marshal(map[string]any{
		"files": map[string]string{
			"data.bin": b64Data,
		},
		"encodings": map[string]string{
			"data.bin": "base64",
		},
	})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/actions/"+act.ID+"/versions", bytes.NewReader(versionBody2))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for explicit base64, got %d: %s", w.Code, w.Body.String())
	}
	var ver2 domain.ActionVersion
	_ = json.Unmarshal(w.Body.Bytes(), &ver2)

	savedFiles2, err := srv.FileStore.ReadSourceBundle(act.ID, ver2.ID)
	if err != nil {
		t.Fatalf("read source bundle 2: %v", err)
	}
	if !bytes.Equal(savedFiles2["data.bin"], binaryData) {
		t.Fatalf("expected binary bytes %v, got %v", binaryData, savedFiles2["data.bin"])
	}

	// Case 3: Invalid base64 with declared base64 encoding returns 400 Bad Request
	versionBody3, _ := json.Marshal(map[string]any{
		"files": map[string]string{
			"invalid.bin": "!!!not_valid_base64!!!",
		},
		"encodings": map[string]string{
			"invalid.bin": "base64",
		},
	})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/actions/"+act.ID+"/versions", bytes.NewReader(versionBody3))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for invalid base64, got %d", w.Code)
	}
}

func TestManagementAPI_ActionsLifecycle(t *testing.T) {
	_, router := setupTestServer(t)

	// 1. Create Action
	createBody, _ := json.Marshal(map[string]any{
		"name":            "test-action",
		"description":     "a demo action",
		"max_concurrency": 2,
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/actions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", w.Code, w.Body.String())
	}

	var createdAction domain.Action
	if err := json.Unmarshal(w.Body.Bytes(), &createdAction); err != nil {
		t.Fatalf("unmarshal created action failed: %v", err)
	}
	if createdAction.ID == "" || createdAction.Name != "test-action" {
		t.Fatalf("unexpected action: %+v", createdAction)
	}

	// 2. Get Action
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+createdAction.ID, nil)
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}

	// 3. List Actions
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions", nil)
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
	var actionList []*domain.Action
	if err := json.Unmarshal(w.Body.Bytes(), &actionList); err != nil {
		t.Fatalf("unmarshal list failed: %v", err)
	}
	if len(actionList) != 1 {
		t.Fatalf("expected 1 action in list, got %d", len(actionList))
	}

	// 4. Update Action
	updateBody, _ := json.Marshal(map[string]any{
		"name":            "updated-action",
		"description":     "updated description",
		"max_concurrency": 3,
		"enabled":         true,
	})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPut, "/api/v1/actions/"+createdAction.ID, bytes.NewReader(updateBody))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	// 5. Delete Action
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodDelete, "/api/v1/actions/"+createdAction.ID, nil)
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}

	// Verify deleted
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+createdAction.ID, nil)
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found after delete, got %d", w.Code)
	}
}

func TestManagementAPI_VersionBuildAndRun(t *testing.T) {
	_, router := setupTestServer(t)

	// Create action
	createBody, _ := json.Marshal(map[string]any{
		"name":            "runner-action",
		"max_concurrency": 1,
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/actions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	router.ServeHTTP(w, req)
	var act domain.Action
	_ = json.Unmarshal(w.Body.Bytes(), &act)

	// Create Version
	versionBody, _ := json.Marshal(map[string]any{
		"files": map[string]string{
			"main.go": "package main\nfunc main() {}\n",
			"go.mod":  "module runner_action\ngo 1.22\n",
		},
		"build_spec": map[string]any{
			"command":               "go build -o /sandbox/out/entrypoint .",
			"toolchain_requirement": "1.22",
		},
		"runtime_spec": map[string]any{
			"entrypoint":      "entrypoint",
			"timeout_seconds": 30,
		},
		"state_injections": []map[string]any{
			{"state_path": "data.json", "env_var": "DATA_JSON", "optional": true},
		},
		"runtime_capabilities": []string{"write_state"},
	})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/actions/"+act.ID+"/versions", bytes.NewReader(versionBody))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for version, got %d: %s", w.Code, w.Body.String())
	}
	var ver domain.ActionVersion
	_ = json.Unmarshal(w.Body.Bytes(), &ver)

	// Build Version
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/actions/"+act.ID+"/versions/"+ver.ID+"/builds", nil)
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for build, got %d: %s", w.Code, w.Body.String())
	}
	var bld domain.ArtifactBuild
	_ = json.Unmarshal(w.Body.Bytes(), &bld)

	// Get Build and Build Logs
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+act.ID+"/builds/"+bld.ID, nil)
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for get build, got %d", w.Code)
	}

	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+act.ID+"/builds/"+bld.ID+"/logs", nil)
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for get build logs, got %d", w.Code)
	}

	// Activate Build
	activateBody, _ := json.Marshal(map[string]any{
		"version_id": ver.ID,
		"build_id":   bld.ID,
	})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/actions/"+act.ID+"/active-build", bytes.NewReader(activateBody))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for activate build, got %d: %s", w.Code, w.Body.String())
	}

	// Toolchain Check
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+act.ID+"/toolchain-check", nil)
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for toolchain check, got %d: %s", w.Code, w.Body.String())
	}

	// Create Manual Run
	runBody, _ := json.Marshal(map[string]any{
		"extra_env": map[string]string{"TRIGGER_SOURCE": "admin_ui"},
	})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/actions/"+act.ID+"/runs", bytes.NewReader(runBody))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for run, got %d: %s", w.Code, w.Body.String())
	}
	var run domain.Run
	_ = json.Unmarshal(w.Body.Bytes(), &run)

	// List Runs
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+act.ID+"/runs", nil)
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for list runs, got %d", w.Code)
	}

	// Get Run
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+act.ID+"/runs/"+run.ID, nil)
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for get run, got %d", w.Code)
	}

	// Get Run Logs
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+act.ID+"/runs/"+run.ID+"/logs", nil)
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for get run logs, got %d", w.Code)
	}
}

func TestManagementAPI_SchedulesAndMatchers(t *testing.T) {
	_, router := setupTestServer(t)

	// Create action
	createBody, _ := json.Marshal(map[string]any{
		"name": "trigger-action",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/actions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	router.ServeHTTP(w, req)
	var act domain.Action
	_ = json.Unmarshal(w.Body.Bytes(), &act)

	// 1. Create Schedule
	schedBody, _ := json.Marshal(map[string]any{
		"cron_expr": "0 12 * * *",
		"timezone":  "Asia/Shanghai",
		"enabled":   true,
	})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/actions/"+act.ID+"/schedules", bytes.NewReader(schedBody))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for schedule, got %d: %s", w.Code, w.Body.String())
	}
	var sched domain.Schedule
	_ = json.Unmarshal(w.Body.Bytes(), &sched)

	// List Schedules
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+act.ID+"/schedules", nil)
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for list schedules, got %d", w.Code)
	}

	// Delete Schedule
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodDelete, "/api/v1/schedules/"+sched.ID, nil)
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for delete schedule, got %d", w.Code)
	}

	// 2. Create Matcher
	matchBody, _ := json.Marshal(map[string]any{
		"name":       "demo_matcher",
		"match_type": "exact",
		"pattern":    "/ping",
		"priority":   100,
		"enabled":    true,
	})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/actions/"+act.ID+"/matchers", bytes.NewReader(matchBody))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for matcher, got %d: %s", w.Code, w.Body.String())
	}
	var matcher domain.Matcher
	_ = json.Unmarshal(w.Body.Bytes(), &matcher)

	// List Matchers
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+act.ID+"/matchers", nil)
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for list matchers, got %d", w.Code)
	}

	// Delete Matcher
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodDelete, "/api/v1/matchers/"+matcher.ID, nil)
	authReq(req)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for delete matcher, got %d", w.Code)
	}
}
