package api

import (
	"actionscat/internal/domain"
	"actionscat/internal/matcher"
	"actionscat/internal/runner"
	"actionscat/internal/sandbox"
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

func setupDispatchTest(t *testing.T) (*store.SQLiteStore, *store.FileStore, *gin.Engine) {
	gin.SetMode(gin.TestMode)
	tempDir := t.TempDir()
	db, err := store.OpenDB(filepath.Join(tempDir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	st := store.NewSQLiteStore(db)
	fs := store.NewFileStore(tempDir)
	ss := store.NewStateStore(tempDir)
	sb := sandbox.NewFakeBackend()

	r := runner.NewRunner(st, fs, ss, sb, runner.Config{
		MaxWorkers:      2,
		RuntimeEndpoint: "http://127.0.0.1:7999/api/v1/runtime",
		PollInterval:    20 * time.Millisecond,
	})

	eng := matcher.NewEngine(st, r)
	handler := NewDispatchHandler(eng)

	router := gin.New()
	router.POST("/v1/dispatch", handler.HandleDispatch)

	return st, fs, router
}

func createDispatchAction(t *testing.T, st *store.SQLiteStore, fs *store.FileStore, actionID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	act := &domain.Action{
		ID:             actionID,
		Name:           actionID,
		MaxConcurrency: 2,
		Enabled:        true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	_ = st.CreateAction(ctx, act)

	ver := &domain.ActionVersion{
		ID:            "ver_" + actionID,
		ActionID:      actionID,
		VersionNumber: 1,
		SourceDigest:  "digest",
		SourcePath:    "path",
		RuntimeSpec: domain.RuntimeSpec{
			Entrypoint: "entrypoint",
		},
		CreatedAt: now,
	}
	_ = st.CreateVersion(ctx, ver)

	digest, path, size, _ := fs.SaveArtifactBundle(actionID, ver.ID, "bld_"+actionID, map[string][]byte{
		"entrypoint": []byte("#!/bin/sh\necho ok\n"),
	})

	bld := &domain.ArtifactBuild{
		ID:             "bld_" + actionID,
		ActionID:       actionID,
		VersionID:      ver.ID,
		BuildNumber:    1,
		Status:         domain.BuildStatusSucceeded,
		ArtifactDigest: digest,
		ArtifactPath:   path,
		ArtifactSize:   size,
		CreatedAt:      now,
	}
	_ = st.CreateBuild(ctx, bld)
	_ = st.SetActiveBuild(ctx, actionID, ver.ID, bld.ID)
}

func TestDispatchHandler_LegacyAndModernEvents(t *testing.T) {
	st, fs, router := setupDispatchTest(t)
	ctx := t.Context()
	now := time.Now().UTC()

	createDispatchAction(t, st, fs, "act_dispatch")

	// Matcher on text "hello"
	m := &domain.Matcher{
		ID:          "m_hello",
		ActionID:    "act_dispatch",
		Name:        "Hello Rule",
		MatchType:   domain.MatchTypeContains,
		Pattern:     "hello",
		TargetField: "text",
		Priority:    10,
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	_ = st.CreateMatcher(ctx, m)

	// 1. Legacy Adapter Request
	legacyBody := map[string]string{
		"sender_qq":     "123456",
		"current_group": "654321",
		"raw_msg":       "say hello to world",
	}
	b1, _ := json.Marshal(legacyBody)
	req1 := httptest.NewRequest(http.MethodPost, "/v1/dispatch", bytes.NewReader(b1))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", w1.Code, w1.Body.String())
	}

	var resp1 DispatchResponse
	_ = json.Unmarshal(w1.Body.Bytes(), &resp1)
	if !resp1.OK || !resp1.Matched || len(resp1.RunIDs) != 1 {
		t.Fatalf("expected matched legacy event with 1 run, got %+v", resp1)
	}

	// 2. Modern Platform-Neutral Request
	modernBody := domain.MessageEvent{
		Platform:  "discord",
		SessionID: "channel_123",
		UserID:    "user_abc",
		Text:      "just saying hello!",
	}
	b2, _ := json.Marshal(modernBody)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/dispatch", bytes.NewReader(b2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", w2.Code, w2.Body.String())
	}

	var resp2 DispatchResponse
	_ = json.Unmarshal(w2.Body.Bytes(), &resp2)
	if !resp2.OK || !resp2.Matched || len(resp2.RunIDs) != 1 {
		t.Fatalf("expected matched modern event with 1 run, got %+v", resp2)
	}

	// 3. Non-matching Request
	unmatchedBody := domain.MessageEvent{
		Text: "no match here",
	}
	b3, _ := json.Marshal(unmatchedBody)
	req3 := httptest.NewRequest(http.MethodPost, "/v1/dispatch", bytes.NewReader(b3))
	req3.Header.Set("Content-Type", "application/json")
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, req3)

	var resp3 DispatchResponse
	_ = json.Unmarshal(w3.Body.Bytes(), &resp3)
	if !resp3.OK || resp3.Matched || len(resp3.RunIDs) != 0 {
		t.Fatalf("expected unmatched event with 0 runs, got %+v", resp3)
	}
}
