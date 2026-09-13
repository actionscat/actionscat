package api_test

import (
	"actionscat/internal/api"
	"actionscat/internal/domain"
	"actionscat/internal/frostagent"
	"actionscat/internal/sandbox"
	"actionscat/internal/store"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

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
	})

	router := srv.SetupRouter()
	return srv, router
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
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}

	// 3. List Actions
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions", nil)
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
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	// 5. Delete Action
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodDelete, "/api/v1/actions/"+createdAction.ID, nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}

	// Verify deleted
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+createdAction.ID, nil)
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
			"command":               "go build -o /out/entrypoint .",
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
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for version, got %d: %s", w.Code, w.Body.String())
	}
	var ver domain.ActionVersion
	_ = json.Unmarshal(w.Body.Bytes(), &ver)

	// Build Version
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/actions/"+act.ID+"/versions/"+ver.ID+"/builds", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for build, got %d: %s", w.Code, w.Body.String())
	}
	var bld domain.ArtifactBuild
	_ = json.Unmarshal(w.Body.Bytes(), &bld)

	// Get Build and Build Logs
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+act.ID+"/builds/"+bld.ID, nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for get build, got %d", w.Code)
	}

	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+act.ID+"/builds/"+bld.ID+"/logs", nil)
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
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for activate build, got %d: %s", w.Code, w.Body.String())
	}

	// Toolchain Check
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+act.ID+"/toolchain-check", nil)
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
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for run, got %d: %s", w.Code, w.Body.String())
	}
	var run domain.Run
	_ = json.Unmarshal(w.Body.Bytes(), &run)

	// List Runs
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+act.ID+"/runs", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for list runs, got %d", w.Code)
	}

	// Get Run
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+act.ID+"/runs/"+run.ID, nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for get run, got %d", w.Code)
	}

	// Get Run Logs
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+act.ID+"/runs/"+run.ID+"/logs", nil)
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
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for schedule, got %d: %s", w.Code, w.Body.String())
	}
	var sched domain.Schedule
	_ = json.Unmarshal(w.Body.Bytes(), &sched)

	// List Schedules
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+act.ID+"/schedules", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for list schedules, got %d", w.Code)
	}

	// Delete Schedule
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodDelete, "/api/v1/schedules/"+sched.ID, nil)
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
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for matcher, got %d: %s", w.Code, w.Body.String())
	}
	var matcher domain.Matcher
	_ = json.Unmarshal(w.Body.Bytes(), &matcher)

	// List Matchers
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/actions/"+act.ID+"/matchers", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for list matchers, got %d", w.Code)
	}

	// Delete Matcher
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodDelete, "/api/v1/matchers/"+matcher.ID, nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for delete matcher, got %d", w.Code)
	}
}
