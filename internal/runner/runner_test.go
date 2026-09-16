package runner

import (
	"actionscat/internal/domain"
	"actionscat/internal/sandbox"
	"actionscat/internal/store"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupTestRunner(t *testing.T, maxWorkers int) (
	*store.SQLiteStore,
	*store.FileStore,
	*store.StateStore,
	*sandbox.FakeBackend,
	*Runner,
) {
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

	r := NewRunner(st, fs, ss, sb, Config{
		MaxWorkers:                maxWorkers,
		RuntimeEndpoint:           "http://127.0.0.1:7999/api/v1/runtime",
		AdvertisedRuntimeEndpoint: "http://10.0.0.1:7999/api/v1/runtime",
		PollInterval:              20 * time.Millisecond,
	})

	return st, fs, ss, sb, r
}

func createRunnableAction(
	t *testing.T,
	st *store.SQLiteStore,
	fs *store.FileStore,
	actionID string,
	maxConcurrency int,
	injections []domain.StateInjection,
) (*domain.Action, *domain.ActionVersion, *domain.ArtifactBuild) {
	ctx := context.Background()
	now := time.Now().UTC()

	act := &domain.Action{
		ID:             actionID,
		Name:           "Test " + actionID,
		MaxConcurrency: maxConcurrency,
		Enabled:        true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := st.CreateAction(ctx, act); err != nil {
		t.Fatalf("create action: %v", err)
	}

	ver := &domain.ActionVersion{
		ID:              "ver_" + actionID,
		ActionID:        actionID,
		VersionNumber:   1,
		SourceDigest:    "src_digest",
		SourcePath:      "src_path",
		StateInjections: injections,
		RuntimeSpec: domain.RuntimeSpec{
			Entrypoint:     "entrypoint",
			TimeoutSeconds: 5,
		},
		RuntimeCapabilities: []string{domain.ScopeStateWrite},
		CreatedAt:           now,
	}
	if err := st.CreateVersion(ctx, ver); err != nil {
		t.Fatalf("create version: %v", err)
	}

	// Write mock artifact binary into FileStore
	digest, path, size, err := fs.SaveArtifactBundle(actionID, ver.ID, "bld_"+actionID, map[string][]byte{
		"entrypoint": []byte("#!/bin/sh\necho 'hello from test'\n"),
	})
	if err != nil {
		t.Fatalf("save artifact bundle: %v", err)
	}

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
	if err := st.CreateBuild(ctx, bld); err != nil {
		t.Fatalf("create build: %v", err)
	}

	if err := st.SetActiveBuild(ctx, actionID, ver.ID, bld.ID); err != nil {
		t.Fatalf("set active build: %v", err)
	}

	return act, ver, bld
}

func TestRunner_CreateRun_Validation(t *testing.T) {
	st, fs, ss, _, r := setupTestRunner(t, 2)
	ctx := context.Background()

	// 1. Non-existent action
	_, err := r.CreateRun(ctx, CreateRunRequest{ActionID: "nonexistent"})
	if err == nil {
		t.Fatal("expected error for nonexistent action")
	}

	// 2. Disabled action
	act, _, _ := createRunnableAction(t, st, fs, "act_disabled", 1, nil)
	act.Enabled = false
	_ = st.UpdateAction(ctx, act)
	_, err = r.CreateRun(ctx, CreateRunRequest{ActionID: act.ID})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected action disabled error, got %v", err)
	}

	// 3. State injection missing non-optional state
	injections := []domain.StateInjection{
		{StatePath: "required.json", EnvVar: "REQ_STATE", Optional: false},
	}
	actWithState, _, _ := createRunnableAction(t, st, fs, "act_state", 1, injections)
	_, err = r.CreateRun(ctx, CreateRunRequest{ActionID: actWithState.ID})
	if err == nil || !strings.Contains(err.Error(), "required state") {
		t.Fatalf("expected required state error, got %v", err)
	}

	// 4. State injection succeeds when file exists
	if err := ss.WriteState(actWithState.ID, "required.json", []byte(`{"key":"val"}`)); err != nil {
		t.Fatalf("write state: %v", err)
	}
	run, err := r.CreateRun(ctx, CreateRunRequest{ActionID: actWithState.ID})
	if err != nil {
		t.Fatalf("create run failed: %v", err)
	}
	if run.PlannedEnv["REQ_STATE"] != `{"key":"val"}` {
		t.Fatalf("expected injected state, got %q", run.PlannedEnv["REQ_STATE"])
	}
}

func TestRunner_ExecuteRun_EndToEndAndTokenRevocation(t *testing.T) {
	st, fs, _, sb, r := setupTestRunner(t, 2)
	ctx := t.Context()

	act, _, _ := createRunnableAction(t, st, fs, "act_e2e", 2, nil)

	// Mock sandbox exec behavior
	zero := 0
	sb.CustomExec = func(req sandbox.ExecRequest, sess *sandbox.FakeSession) (sandbox.ExecResult, error) {
		// Verify environment variables were provisioned to sandbox session
		if sess.Req.Env["ACTIONSCAT_ACTION_ID"] != act.ID {
			t.Errorf("missing ACTIONSCAT_ACTION_ID")
		}
		if sess.Req.Env["ACTIONSCAT_RUNTIME_TOKEN"] == "" {
			t.Errorf("missing ACTIONSCAT_RUNTIME_TOKEN")
		}
		return sandbox.ExecResult{
			ExitCode: &zero,
			Stdout:   "action executed successfully\n",
			Stderr:   "",
		}, nil
	}

	// Start runner workers
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start runner: %v", err)
	}
	defer r.Stop()

	run, err := r.CreateRun(ctx, CreateRunRequest{
		ActionID:    act.ID,
		TriggerType: domain.TriggerTypeManual,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Poll until run completes
	var finishedRun *domain.Run
	for range 50 {
		rRecord, err := st.GetRun(ctx, run.ID)
		if err == nil && rRecord.Status == domain.RunStatusSucceeded {
			finishedRun = rRecord
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if finishedRun == nil {
		t.Fatal("run did not finish in time")
	}

	if finishedRun.Stdout != "action executed successfully\n" {
		t.Fatalf("unexpected stdout: %s", finishedRun.Stdout)
	}

	// CRITICAL SECURITY INVARIANT CHECK:
	// Verify that any tokens generated for this run are REVOKED after completion!
	tokens, err := st.ListTokensForRun(ctx, finishedRun.ID)
	if err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	if len(tokens) == 0 {
		t.Fatal("expected at least one token to have been recorded")
	}
	for _, tok := range tokens {
		if tok.RevokedAt == nil {
			t.Fatalf("security violation: token %s was not revoked upon run termination!", tok.ID)
		}
	}
}

func TestRunner_ConcurrencyLimit(t *testing.T) {
	st, fs, _, sb, r := setupTestRunner(t, 4)
	ctx := t.Context()

	// Action with max_concurrency = 1
	act, _, _ := createRunnableAction(t, st, fs, "act_concurrency", 1, nil)

	blockChan := make(chan struct{}, 10)
	releaseChan := make(chan struct{}, 10)

	zero := 0
	sb.CustomExec = func(_ sandbox.ExecRequest, _ *sandbox.FakeSession) (sandbox.ExecResult, error) {
		blockChan <- struct{}{}
		<-releaseChan
		return sandbox.ExecResult{ExitCode: &zero, Stdout: "done"}, nil
	}

	if err := r.Start(ctx); err != nil {
		t.Fatalf("start runner: %v", err)
	}
	defer r.Stop()

	// Enqueue Run 1 and Run 2
	run1, err := r.CreateRun(ctx, CreateRunRequest{ActionID: act.ID, TriggerType: domain.TriggerTypeManual})
	if err != nil {
		t.Fatalf("create run 1: %v", err)
	}
	run2, err := r.CreateRun(ctx, CreateRunRequest{ActionID: act.ID, TriggerType: domain.TriggerTypeManual})
	if err != nil {
		t.Fatalf("create run 2: %v", err)
	}

	// Wait for Run 1 to start executing
	select {
	case <-blockChan:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run 1 to start")
	}

	// Verify Run 1 is running and Run 2 is still queued (blocked by max_concurrency=1)
	r1Rec, _ := st.GetRun(ctx, run1.ID)
	r2Rec, _ := st.GetRun(ctx, run2.ID)

	if r1Rec.Status != domain.RunStatusRunning {
		t.Fatalf("run1 should be running, got %s", r1Rec.Status)
	}
	if r2Rec.Status != domain.RunStatusQueued {
		t.Fatalf("run2 should still be queued due to max_concurrency=1, got %s", r2Rec.Status)
	}

	// Release Run 1
	releaseChan <- struct{}{}

	// Wait for Run 2 to start executing
	select {
	case <-blockChan:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run 2 to start")
	}

	r2RecAfter, _ := st.GetRun(ctx, run2.ID)
	if r2RecAfter.Status != domain.RunStatusRunning {
		t.Fatalf("run2 should now be running after run1 finished, got %s", r2RecAfter.Status)
	}

	// Release Run 2
	releaseChan <- struct{}{}
}

func TestRunner_CrashRecoveryAndTokenRevocation(t *testing.T) {
	st, fs, _, _, r := setupTestRunner(t, 2)
	ctx := t.Context()

	act, _, _ := createRunnableAction(t, st, fs, "act_crash_recover", 1, nil)

	// Create run directly and claim it as running (simulating crash before completion)
	run1, err := r.CreateRun(ctx, CreateRunRequest{ActionID: act.ID, TriggerType: domain.TriggerTypeManual})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	claimed, err := st.ClaimNextQueuedRun(ctx)
	if err != nil || claimed == nil || claimed.ID != run1.ID {
		t.Fatalf("claim failed: %v", err)
	}

	// Create an unrevoked token for this run (simulating unrevoked token in DB)
	_, hash, err := domain.GenerateRawToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	tok := &domain.RunToken{
		ID:        "tok_crash_test",
		TokenHash: hash,
		RunID:     claimed.ID,
		ActionID:  act.ID,
		Scopes:    []string{"write_state"},
		ExpiresAt: time.Now().Add(1 * time.Hour),
		CreatedAt: time.Now(),
	}
	if err := st.CreateRunToken(ctx, tok); err != nil {
		t.Fatalf("create token: %v", err)
	}

	// Concurrency slot is now occupied: another run for this action should NOT be claimable
	run2, err := r.CreateRun(ctx, CreateRunRequest{ActionID: act.ID, TriggerType: domain.TriggerTypeManual})
	if err != nil {
		t.Fatalf("create run2: %v", err)
	}
	blockedClaim, err := st.ClaimNextQueuedRun(ctx)
	if err != nil {
		t.Fatalf("claim error: %v", err)
	}
	if blockedClaim != nil {
		t.Fatalf("expected claim to be blocked by max_concurrency=1, got %v", blockedClaim.ID)
	}

	// Simulate Server Restart: Start runner, which invokes RecoverOrphans
	recovered, err := r.RecoverOrphans(ctx)
	if err != nil {
		t.Fatalf("recover orphans: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("expected 1 recovered run, got %d", recovered)
	}

	// 1. Verify orphan run is now marked interrupted
	recoveredRun, err := st.GetRun(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if recoveredRun.Status != domain.RunStatusInterrupted {
		t.Fatalf("expected run status %s, got %s", domain.RunStatusInterrupted, recoveredRun.Status)
	}

	// 2. Verify token is now revoked
	tokens, err := st.ListTokensForRun(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	if len(tokens) == 0 || tokens[0].RevokedAt == nil {
		t.Fatal("expected token to be revoked upon orphan recovery")
	}

	// 3. Verify concurrency slot is now freed: run2 can now be claimed!
	claimedAfter, err := st.ClaimNextQueuedRun(ctx)
	if err != nil {
		t.Fatalf("claim after recovery: %v", err)
	}
	if claimedAfter == nil || claimedAfter.ID != run2.ID {
		t.Fatalf("expected run2 to be claimed now, got %v", claimedAfter)
	}
}

func TestRunner_FailFast_LoopbackRuntimeCapabilities(t *testing.T) {
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

	// Configure runner with loopback runtime endpoint and NO advertised endpoint
	r := NewRunner(st, fs, ss, sb, Config{
		MaxWorkers:      2,
		RuntimeEndpoint: "http://127.0.0.1:7999/api/v1/runtime",
		PollInterval:    20 * time.Millisecond,
	})

	ctx := t.Context()
	// Action has runtime capabilities [state.write]
	act, _, _ := createRunnableAction(t, st, fs, "act_failfast", 1, nil)

	if err := r.Start(ctx); err != nil {
		t.Fatalf("start runner: %v", err)
	}
	defer r.Stop()

	run, err := r.CreateRun(ctx, CreateRunRequest{
		ActionID:    act.ID,
		TriggerType: domain.TriggerTypeManual,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Poll until run completes (it should fail-fast without executing in sandbox)
	var finishedRun *domain.Run
	for range 50 {
		rRecord, err := st.GetRun(ctx, run.ID)
		if err == nil && (rRecord.Status == domain.RunStatusFailed || rRecord.Status == domain.RunStatusSucceeded) {
			finishedRun = rRecord
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if finishedRun == nil {
		t.Fatal("run did not finish in time")
	}
	if finishedRun.Status != domain.RunStatusFailed {
		t.Fatalf("expected run status %s, got %s", domain.RunStatusFailed, finishedRun.Status)
	}
	if !strings.Contains(finishedRun.ErrorMessage, "loopback") || !strings.Contains(finishedRun.ErrorMessage, "ACTIONSCAT_RUNTIME_ADVERTISED_ENDPOINT") {
		t.Fatalf("expected fail-fast loopback error message, got %q", finishedRun.ErrorMessage)
	}
}

func TestRunner_Start_OrphanRecoveryFailure_Gating(t *testing.T) {
	tempDir := t.TempDir()
	db, err := store.OpenDB(filepath.Join(tempDir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	st := store.NewSQLiteStore(db)
	fs := store.NewFileStore(tempDir)
	ss := store.NewStateStore(tempDir)
	sb := sandbox.NewFakeBackend()

	r := NewRunner(st, fs, ss, sb, Config{
		MaxWorkers: 2,
	})

	// Intentionally close database before Start to force recovery failure
	_ = db.Close()

	ctx := context.Background()
	err = r.Start(ctx)
	if err == nil {
		t.Fatal("expected r.Start() to fail when database is unavailable, got nil")
	}
	if !strings.Contains(err.Error(), "failed to recover orphan runs") {
		t.Fatalf("expected error mentioning orphan recovery, got: %v", err)
	}

	// Verify that workers were not launched
	r.mu.Lock()
	cancelFn := r.cancel
	r.mu.Unlock()
	if cancelFn != nil {
		t.Fatal("worker context should not be initialized if startup recovery failed")
	}
}
