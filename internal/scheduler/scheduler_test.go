package scheduler

import (
	"actionscat/internal/domain"
	"actionscat/internal/runner"
	"actionscat/internal/sandbox"
	"actionscat/internal/store"
	"context"
	"path/filepath"
	"testing"
	"time"
)

func setupTestScheduler(t *testing.T) (
	*store.SQLiteStore,
	*store.FileStore,
	*store.StateStore,
	*runner.Runner,
	*Scheduler,
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

	r := runner.NewRunner(st, fs, ss, sb, runner.Config{
		MaxWorkers:      2,
		RuntimeEndpoint: "http://127.0.0.1:7999/api/v1/runtime",
		PollInterval:    20 * time.Millisecond,
	})

	s := NewScheduler(st, r, Config{
		TickInterval: 50 * time.Millisecond,
		BatchSize:    10,
	})

	return st, fs, ss, r, s
}

func createActionWithBuild(
	t *testing.T,
	st *store.SQLiteStore,
	fs *store.FileStore,
	actionID string,
) (*domain.Action, *domain.ActionVersion, *domain.ArtifactBuild) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	act := &domain.Action{
		ID:             actionID,
		Name:           "Sched " + actionID,
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

	return act, ver, bld
}

func TestScheduler_Tick_ProducesRunAndDeduplicates(t *testing.T) {
	st, fs, _, _, s := setupTestScheduler(t)
	ctx := context.Background()

	act, _, _ := createActionWithBuild(t, st, fs, "act_sched_1")

	// Schedule that is due right now
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	sched := &domain.Schedule{
		ID:        "sched_1",
		ActionID:  act.ID,
		CronExpr:  "*/5 * * * *", // every 5 minutes
		Timezone:  "UTC",
		NextRunAt: now.Add(-10 * time.Second), // Due!
		Enabled:   true,
		CreatedAt: now.Add(-1 * time.Hour),
		UpdatedAt: now.Add(-1 * time.Hour),
	}
	if err := st.CreateSchedule(ctx, sched); err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	// First tick: produces a Run
	s.Tick(ctx, now)

	// Check that a run was queued in SQLite
	runs, err := st.ListRuns(ctx, act.ID, 10, 0)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected exactly 1 queued run, got %d", len(runs))
	}
	run := runs[0]
	if run.TriggerType != domain.TriggerTypeSchedule {
		t.Fatalf("expected trigger_type schedule, got %s", run.TriggerType)
	}
	if run.Status != domain.RunStatusQueued {
		t.Fatalf("expected queued status, got %s", run.Status)
	}

	// Check that schedule's next_run_at was advanced into the future
	updatedSched, err := st.GetSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatalf("get schedule: %v", err)
	}
	if !updatedSched.NextRunAt.After(now) {
		t.Fatalf("expected next_run_at to be in the future, got %v", updatedSched.NextRunAt)
	}

	// Second tick at the exact same point in time:
	// Deduplication invariant: MUST NOT create another run!
	s.Tick(ctx, now)

	runsAfter, err := st.ListRuns(ctx, act.ID, 10, 0)
	if err != nil {
		t.Fatalf("list runs after: %v", err)
	}
	if len(runsAfter) != 1 {
		t.Fatalf("deduplication failed: expected still 1 run, got %d", len(runsAfter))
	}
}

func TestScheduler_NonRunnableActionAdvances(t *testing.T) {
	st, _, _, _, s := setupTestScheduler(t)
	ctx := context.Background()

	// Action with NO version/build
	act := &domain.Action{
		ID:             "act_nobuild",
		Name:           "No Build",
		MaxConcurrency: 1,
		Enabled:        true,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	_ = st.CreateAction(ctx, act)

	now := time.Now().UTC()
	sched := &domain.Schedule{
		ID:        "sched_nobuild",
		ActionID:  act.ID,
		CronExpr:  "*/10 * * * *",
		NextRunAt: now.Add(-1 * time.Minute),
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	_ = st.CreateSchedule(ctx, sched)

	// Tick should skip creating run, but advance next_run_at so it doesn't loop
	s.Tick(ctx, now)

	runs, _ := st.ListRuns(ctx, act.ID, 10, 0)
	if len(runs) != 0 {
		t.Fatalf("expected 0 runs for non-runnable action, got %d", len(runs))
	}

	updatedSched, _ := st.GetSchedule(ctx, sched.ID)
	if !updatedSched.NextRunAt.After(now) {
		t.Fatalf("expected next_run_at to advance even when action is not runnable, got %v", updatedSched.NextRunAt)
	}
}

func TestScheduler_CalculateNextRun(t *testing.T) {
	_, _, _, _, s := setupTestScheduler(t)

	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	// 1. Every 15 minutes in UTC
	next, err := s.CalculateNextRun("*/15 * * * *", "UTC", base)
	if err != nil {
		t.Fatalf("calc next run: %v", err)
	}
	expected := time.Date(2026, 9, 13, 12, 15, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Fatalf("expected %v, got %v", expected, next)
	}

	// 2. Valid timezone: Asia/Shanghai (UTC+8)
	// Cron "0 8 * * *" -> 8:00 AM Shanghai time = 0:00 UTC
	nextSH, err := s.CalculateNextRun("0 8 * * *", "Asia/Shanghai", base)
	if err != nil {
		t.Fatalf("calc next run Asia/Shanghai: %v", err)
	}
	expectedSH := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	if !nextSH.Equal(expectedSH) {
		t.Fatalf("expected %v, got %v", expectedSH, nextSH)
	}

	// 3. Fail-closed on invalid timezone identifier (must NOT fall back silently to UTC)
	_, err = s.CalculateNextRun("0 8 * * *", "Asia/Shangahi", base)
	if err == nil {
		t.Fatal("expected error for invalid timezone 'Asia/Shangahi', but got nil")
	}

	// 4. Invalid cron expression
	_, err = s.CalculateNextRun("invalid cron", "UTC", base)
	if err == nil {
		t.Fatal("expected error for invalid cron expression, but got nil")
	}

	// 5. Daylight Saving Time (DST) timezone: America/New_York
	// In September, EDT is UTC-4. "0 12 * * *" (noon EDT) = 16:00 UTC.
	nextNY, err := s.CalculateNextRun("0 12 * * *", "America/New_York", base)
	if err != nil {
		t.Fatalf("calc next run America/New_York: %v", err)
	}
	expectedNY := time.Date(2026, 9, 13, 16, 0, 0, 0, time.UTC)
	if !nextNY.Equal(expectedNY) {
		t.Fatalf("expected %v, got %v", expectedNY, nextNY)
	}
}
