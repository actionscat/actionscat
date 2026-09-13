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
		MaxWorkers:      maxWorkers,
		RuntimeEndpoint: "http://127.0.0.1:7999/api/v1/runtime",
		PollInterval:    20 * time.Millisecond,
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
	sb.CustomExec = func(req sandbox.ExecRequest, _ *sandbox.FakeSession) (sandbox.ExecResult, error) {
		// Verify environment variables were passed to sandbox
		if req.Env["ACTIONSCAT_ACTION_ID"] != act.ID {
			t.Errorf("missing ACTIONSCAT_ACTION_ID")
		}
		if req.Env["ACTIONSCAT_RUNTIME_TOKEN"] == "" {
			t.Errorf("missing ACTIONSCAT_RUNTIME_TOKEN")
		}
		return sandbox.ExecResult{
			ExitCode: &zero,
			Stdout:   "action executed successfully\n",
			Stderr:   "",
		}, nil
	}

	// Start runner workers
	r.Start(ctx)
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

	blockChan := make(chan struct{})
	releaseChan := make(chan struct{})

	zero := 0
	sb.CustomExec = func(_ sandbox.ExecRequest, _ *sandbox.FakeSession) (sandbox.ExecResult, error) {
		select {
		case blockChan <- struct{}{}:
		default:
		}
		<-releaseChan
		return sandbox.ExecResult{ExitCode: &zero, Stdout: "done"}, nil
	}

	r.Start(ctx)
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
	<-blockChan

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
	<-blockChan
	r2RecAfter, _ := st.GetRun(ctx, run2.ID)
	if r2RecAfter.Status != domain.RunStatusRunning {
		t.Fatalf("run2 should now be running after run1 finished, got %s", r2RecAfter.Status)
	}

	// Release Run 2
	releaseChan <- struct{}{}
}
