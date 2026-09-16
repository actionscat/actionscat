package runtime

import (
	"actionscat/internal/domain"
	"actionscat/internal/frostagent"
	"actionscat/internal/store"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func setupRuntimeTest(t *testing.T) (*store.SQLiteStore, *store.StateStore, *frostagent.MockClient, *gin.Engine) {
	tempDir := t.TempDir()
	db, err := store.OpenDB(filepath.Join(tempDir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	st := store.NewSQLiteStore(db)
	ss := store.NewStateStore(tempDir)
	fa := &frostagent.MockClient{}

	api := NewAPI(st, ss, fa)
	r := gin.New()
	rg := r.Group("/api/v1/runtime")
	api.RegisterRoutes(rg)

	return st, ss, fa, r
}

func createTestRunAndToken(
	t *testing.T,
	st *store.SQLiteStore,
	actionID string,
	scopes []string,
) (rawToken string, runID string) {
	ctx := context.Background()

	act := &domain.Action{
		ID:        actionID,
		Name:      "Action " + actionID,
		Enabled:   true,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	_ = st.CreateAction(ctx, act)

	v := &domain.ActionVersion{
		ID:            "ver_" + actionID,
		ActionID:      actionID,
		VersionNumber: 1,
		SourceDigest:  "digest",
		SourcePath:    "path",
		CreatedAt:     time.Now().UTC(),
	}
	_ = st.CreateVersion(ctx, v)

	bld := &domain.ArtifactBuild{
		ID:          "bld_" + actionID,
		ActionID:    actionID,
		VersionID:   "ver_" + actionID,
		BuildNumber: 1,
		Status:      domain.BuildStatusSucceeded,
		CreatedAt:   time.Now().UTC(),
	}
	_ = st.CreateBuild(ctx, bld)

	runID = "run_" + actionID
	now := time.Now().UTC()
	run := &domain.Run{
		ID:              runID,
		ActionID:        actionID,
		ActionVersionID: "ver_" + actionID,
		ArtifactBuildID: "bld_" + actionID,
		TriggerType:     domain.TriggerTypeManual,
		Status:          domain.RunStatusRunning,
		StartedAt:       &now,
		CreatedAt:       now,
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	raw, hash, err := domain.GenerateRawToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	token := &domain.RunToken{
		ID:        "tok_" + actionID,
		TokenHash: hash,
		RunID:     runID,
		ActionID:  actionID,
		Scopes:    scopes,
		ExpiresAt: now.Add(15 * time.Minute),
		CreatedAt: now,
	}
	if err := st.CreateRunToken(ctx, token); err != nil {
		t.Fatalf("create run token: %v", err)
	}

	return raw, runID
}

func TestRuntimeAPI_StateWrite_SuccessAndForgeryProof(t *testing.T) {
	st, ss, _, r := setupRuntimeTest(t)

	rawTokenA, _ := createTestRunAndToken(t, st, "act_alpha", []string{domain.ScopeStateWrite})

	// Client sends a write request with a forged action_id: "act_victim"
	body := WriteStateRequest{
		ActionID: "act_victim", // Trying to forge Action ID!
		Path:     "state.json",
		Data:     `{"message":"hello from alpha"}`,
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/state", bytes.NewReader(bodyBytes))
	req.Header.Set("Authorization", "Bearer "+rawTokenA)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", w.Code, w.Body.String())
	}

	// 1. Verify that act_alpha's state got written
	dataAlpha, err := ss.ReadState("act_alpha", "state.json")
	if err != nil || string(dataAlpha) != `{"message":"hello from alpha"}` {
		t.Fatalf("failed to read written state in act_alpha: %v", err)
	}

	// 2. CRITICAL: Verify that act_victim's namespace was NOT written
	_, err = ss.ReadState("act_victim", "state.json")
	if err == nil {
		t.Fatal("security violation: forged action_id was honored!")
	}
}

func TestRuntimeAPI_PathTraversal_Rejected(t *testing.T) {
	st, _, _, r := setupRuntimeTest(t)
	rawToken, _ := createTestRunAndToken(t, st, "act_trav", []string{domain.ScopeStateWrite})

	badPaths := []string{
		"../escape.json",
		"/etc/passwd",
		"nested/../../secret.json",
		"null\x00byte.json",
	}

	for _, badPath := range badPaths {
		body := WriteStateRequest{
			Path: badPath,
			Data: "payload",
		}
		bodyBytes, _ := json.Marshal(body)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/state", bytes.NewReader(bodyBytes))
		req.Header.Set("Authorization", "Bearer "+rawToken)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected path %q to return HTTP 400, got %d: %s", badPath, w.Code, w.Body.String())
		}
	}
}

func TestRuntimeAPI_TokenRevocationAndRunTermination(t *testing.T) {
	st, _, _, r := setupRuntimeTest(t)
	rawToken, runID := createTestRunAndToken(t, st, "act_rev", []string{domain.ScopeStateWrite})

	body := WriteStateRequest{
		Path: "test.json",
		Data: "data",
	}
	bodyBytes, _ := json.Marshal(body)

	// 1. Initial write succeeds
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/state", bytes.NewReader(bodyBytes))
	req.Header.Set("Authorization", "Bearer "+rawToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("initial write failed: %d", w.Code)
	}

	// 2. Complete the Run (status -> succeeded)
	completedAt := time.Now().UTC()
	_ = st.UpdateRunStatus(context.Background(), runID, domain.RunStatusSucceeded, nil, "", "", 10, "", &completedAt)

	// Attempt write again -> MUST return 401 because Run has completed!
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/state", bytes.NewReader(bodyBytes))
	req2.Header.Set("Authorization", "Bearer "+rawToken)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 after run completion, got %d", w2.Code)
	}

	// 3. Explicit revocation also returns 401
	hash := domain.HashToken(rawToken)
	_ = st.RevokeRunToken(context.Background(), hash, time.Now().UTC())

	req3 := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/state", bytes.NewReader(bodyBytes))
	req3.Header.Set("Authorization", "Bearer "+rawToken)
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	if w3.Code != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 for revoked token, got %d", w3.Code)
	}
}

func TestRuntimeAPI_ScopeEnforcement(t *testing.T) {
	st, _, fa, r := setupRuntimeTest(t)

	// Token with ONLY state.write
	tokenStateOnly, _ := createTestRunAndToken(t, st, "act_scope_1", []string{domain.ScopeStateWrite})
	// Token with ONLY frostagent.sendmsg
	tokenMsgOnly, _ := createTestRunAndToken(t, st, "act_scope_2", []string{domain.ScopeFrostAgentSendMsg})

	// 1. Token with only state.write trying to call frostagent/send -> 403
	msgReq := frostagent.SendMessageRequest{
		Session:  "group:123",
		Messages: []frostagent.MessageItem{{Type: "plain", Text: "hi"}},
	}
	msgBytes, _ := json.Marshal(msgReq)

	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/frostagent/send", bytes.NewReader(msgBytes))
	req1.Header.Set("Authorization", "Bearer "+tokenStateOnly)
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden without sendmsg scope, got %d", w1.Code)
	}

	// 2. Token with only frostagent.sendmsg trying to call state -> 403
	stateReq := WriteStateRequest{Path: "test.json", Data: "val"}
	stateBytes, _ := json.Marshal(stateReq)

	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/state", bytes.NewReader(stateBytes))
	req2.Header.Set("Authorization", "Bearer "+tokenMsgOnly)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden without state.write scope, got %d", w2.Code)
	}

	// 3. Token with frostagent.sendmsg calling frostagent/send -> 200 and message proxied
	req3 := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/frostagent/send", bytes.NewReader(msgBytes))
	req3.Header.Set("Authorization", "Bearer "+tokenMsgOnly)
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for frostagent send, got %d", w3.Code)
	}
	if len(fa.SentMessages) != 1 || fa.SentMessages[0].Session != "group:123" {
		t.Fatalf("message was not proxied to FrostAgent: %v", fa.SentMessages)
	}

	// 4. Sandbox attempting to specify instance_id MUST be rejected with 400
	evilMsgReq := frostagent.SendMessageRequest{
		Session:    "group:123",
		InstanceID: "attacker_controlled_instance",
		Messages:   []frostagent.MessageItem{{Type: "plain", Text: "escalate"}},
	}
	evilBytes, _ := json.Marshal(evilMsgReq)
	req4 := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/frostagent/send", bytes.NewReader(evilBytes))
	req4.Header.Set("Authorization", "Bearer "+tokenMsgOnly)
	w4 := httptest.NewRecorder()
	r.ServeHTTP(w4, req4)
	if w4.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when sandbox specifies instance_id, got %d", w4.Code)
	}
}

func TestRuntimeAPI_PayloadSizeLimitEnforcement(t *testing.T) {
	st, _, _, r := setupRuntimeTest(t)

	tokenState, _ := createTestRunAndToken(t, st, "act_limit_1", []string{domain.ScopeStateWrite})
	tokenSend, _ := createTestRunAndToken(t, st, "act_limit_2", []string{domain.ScopeFrostAgentSendMsg})

	// 1. Oversized body on /state (> 8 MB) MUST return 413 StatusRequestEntityTooLarge
	hugeStatePayload := make([]byte, 9*1024*1024)
	for i := range hugeStatePayload {
		hugeStatePayload[i] = 'A'
	}
	stateBody, _ := json.Marshal(WriteStateRequest{
		Path: "huge.bin",
		Data: string(hugeStatePayload),
	})
	reqState := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/state", bytes.NewReader(stateBody))
	reqState.Header.Set("Authorization", "Bearer "+tokenState)
	reqState.Header.Set("Content-Type", "application/json")
	wState := httptest.NewRecorder()
	r.ServeHTTP(wState, reqState)
	if wState.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized state body, got %d: %s", wState.Code, wState.Body.String())
	}

	// 2. Oversized body on /frostagent/send (> 2 MB) MUST return 413 StatusRequestEntityTooLarge
	hugeSendPayload := make([]byte, 3*1024*1024)
	for i := range hugeSendPayload {
		hugeSendPayload[i] = 'B'
	}
	sendBody, _ := json.Marshal(frostagent.SendMessageRequest{
		Session:  "group:123",
		Messages: []frostagent.MessageItem{{Type: "plain", Text: string(hugeSendPayload)}},
	})
	reqSend := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/frostagent/send", bytes.NewReader(sendBody))
	reqSend.Header.Set("Authorization", "Bearer "+tokenSend)
	reqSend.Header.Set("Content-Type", "application/json")
	wSend := httptest.NewRecorder()
	r.ServeHTTP(wSend, reqSend)
	if wSend.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized send body, got %d: %s", wSend.Code, wSend.Body.String())
	}
}
