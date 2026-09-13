package store

import (
	"actionscat/internal/domain"
	"context"
	"path/filepath"
	"testing"
	"time"
)

func setupTestStore(t *testing.T) (*SQLiteStore, *FileStore) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := NewSQLiteStore(db)
	fileStore := NewFileStore(tempDir)
	return store, fileStore
}

func TestSQLiteStore_ActionAndVersionLifecycle(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestStore(t)

	act := &domain.Action{
		ID:             "act_1",
		Name:           "Bili Resolver",
		Description:    "Resolves Bilibili links",
		MaxConcurrency: 1,
		Enabled:        true,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := store.CreateAction(ctx, act); err != nil {
		t.Fatalf("failed to create action: %v", err)
	}

	gotAct, err := store.GetAction(ctx, "act_1")
	if err != nil || gotAct.Name != "Bili Resolver" {
		t.Fatalf("failed to get action: %v", err)
	}

	// Create Version 1
	v1 := &domain.ActionVersion{
		ID:            "ver_1",
		ActionID:      "act_1",
		VersionNumber: 1,
		SourceDigest:  "sha256:abc1",
		SourcePath:    "actions/act_1/versions/ver_1/source",
		BuildSpec: domain.BuildSpec{
			Language:             "go",
			ToolchainRequirement: ">= 1.25",
			Command:              "go build -o /out/entrypoint .",
		},
		RuntimeSpec: domain.RuntimeSpec{
			Entrypoint:     "/entrypoint",
			TimeoutSeconds: 30,
		},
		RuntimeCapabilities: []string{domain.ScopeStateWrite},
		CreatedAt:           time.Now().UTC(),
	}
	if err := store.CreateVersion(ctx, v1); err != nil {
		t.Fatalf("failed to create version 1: %v", err)
	}

	// Duplicate version number for same action MUST fail (immutability)
	if err := store.CreateVersion(ctx, v1); err == nil {
		t.Fatal("expected duplicate version creation to fail")
	}

	// Create Version 2
	nextVer, err := store.GetNextVersionNumber(ctx, "act_1")
	if err != nil || nextVer != 2 {
		t.Fatalf("expected next version 2, got %d, err: %v", nextVer, err)
	}

	// Create multiple builds for Version 1 (e.g. initial build + toolchain rebuild)
	b1 := &domain.ArtifactBuild{
		ID:               "bld_1",
		ActionID:         "act_1",
		VersionID:        "ver_1",
		BuildNumber:      1,
		Status:           domain.BuildStatusSucceeded,
		BuilderProfile:   "go-1.25",
		ToolchainVersion: "go1.25.6",
		BuildCommand:     "go build -o /out/entrypoint .",
		ArtifactDigest:   "sha256:art1",
		ArtifactPath:     "actions/act_1/versions/ver_1/builds/bld_1/artifact",
		ArtifactSize:     1024,
		CreatedAt:        time.Now().UTC(),
	}
	if err := store.CreateBuild(ctx, b1); err != nil {
		t.Fatalf("failed to create build 1: %v", err)
	}

	nextBuild, err := store.GetNextBuildNumber(ctx, "ver_1")
	if err != nil || nextBuild != 2 {
		t.Fatalf("expected next build 2, got %d, err: %v", nextBuild, err)
	}

	b2 := &domain.ArtifactBuild{
		ID:               "bld_2",
		ActionID:         "act_1",
		VersionID:        "ver_1",
		BuildNumber:      2,
		Status:           domain.BuildStatusSucceeded,
		BuilderProfile:   "go-1.27",
		ToolchainVersion: "go1.27.3",
		BuildCommand:     "go build -o /out/entrypoint .",
		ArtifactDigest:   "sha256:art2",
		ArtifactPath:     "actions/act_1/versions/ver_1/builds/bld_2/artifact",
		ArtifactSize:     1030,
		CreatedAt:        time.Now().UTC(),
	}
	if err := store.CreateBuild(ctx, b2); err != nil {
		t.Fatalf("failed to create build 2: %v", err)
	}

	// Activate build 2
	if err := store.SetActiveBuild(ctx, "act_1", "ver_1", "bld_2"); err != nil {
		t.Fatalf("failed to set active build: %v", err)
	}

	updatedAct, err := store.GetAction(ctx, "act_1")
	if err != nil {
		t.Fatalf("get action error: %v", err)
	}
	if updatedAct.ActiveVersionID != "ver_1" || updatedAct.ActiveBuildID != "bld_2" {
		t.Fatalf("unexpected active version/build: %v, %v", updatedAct.ActiveVersionID, updatedAct.ActiveBuildID)
	}
}

func TestSQLiteStore_ScheduleDeduplication(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestStore(t)

	act := &domain.Action{
		ID:        "act_sched",
		Name:      "Scheduled Action",
		Enabled:   true,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	_ = store.CreateAction(ctx, act)

	v := &domain.ActionVersion{
		ID:            "ver_sched",
		ActionID:      "act_sched",
		VersionNumber: 1,
		SourceDigest:  "digest",
		SourcePath:    "path",
		CreatedAt:     time.Now().UTC(),
	}
	_ = store.CreateVersion(ctx, v)

	bld := &domain.ArtifactBuild{
		ID:          "bld_sched",
		ActionID:    "act_sched",
		VersionID:   "ver_sched",
		BuildNumber: 1,
		Status:      domain.BuildStatusSucceeded,
		CreatedAt:   time.Now().UTC(),
	}
	_ = store.CreateBuild(ctx, bld)
	_ = store.SetActiveBuild(ctx, "act_sched", "ver_sched", "bld_sched")

	schedTime := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	sched := &domain.Schedule{
		ID:        "sched_1",
		ActionID:  "act_sched",
		CronExpr:  "0 * * * *",
		Timezone:  "UTC",
		NextRunAt: schedTime,
		Enabled:   true,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := store.CreateSchedule(ctx, sched); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	// 1. Record occurrence and create run
	run1 := &domain.Run{
		ID:              "run_sched_1",
		ActionID:        "act_sched",
		ActionVersionID: "ver_sched",
		ArtifactBuildID: "bld_sched",
		TriggerType:     domain.TriggerTypeSchedule,
		Status:          domain.RunStatusQueued,
		CreatedAt:       time.Now().UTC(),
	}
	nextRunAt := schedTime.Add(1 * time.Hour)
	err := store.RecordScheduleOccurrence(ctx, "sched_1", schedTime, nextRunAt, run1)
	if err != nil {
		t.Fatalf("failed to record occurrence: %v", err)
	}

	// 2. Duplicate occurrence at the same scheduled_for MUST fail (ensuring deduplication)
	run2 := &domain.Run{
		ID:              "run_sched_2",
		ActionID:        "act_sched",
		ActionVersionID: "ver_sched",
		ArtifactBuildID: "bld_sched",
		TriggerType:     domain.TriggerTypeSchedule,
		Status:          domain.RunStatusQueued,
		CreatedAt:       time.Now().UTC(),
	}
	err = store.RecordScheduleOccurrence(ctx, "sched_1", schedTime, nextRunAt, run2)
	if err == nil {
		t.Fatal("expected duplicate schedule occurrence to be rejected, but it succeeded")
	}

	// Verify schedule was advanced
	updatedSched, err := store.GetSchedule(ctx, "sched_1")
	if err != nil {
		t.Fatalf("get schedule: %v", err)
	}
	if !updatedSched.NextRunAt.Equal(nextRunAt) {
		t.Fatalf("expected next_run_at %v, got %v", nextRunAt, updatedSched.NextRunAt)
	}
}

func TestSQLiteStore_ConcurrencyAndClaim(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestStore(t)

	act := &domain.Action{
		ID:             "act_conc",
		Name:           "Single Concurrency Action",
		MaxConcurrency: 1, // only 1 run can run at a time!
		Enabled:        true,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	_ = store.CreateAction(ctx, act)

	v := &domain.ActionVersion{
		ID:            "ver_conc",
		ActionID:      "act_conc",
		VersionNumber: 1,
		SourceDigest:  "digest",
		SourcePath:    "path",
		CreatedAt:     time.Now().UTC(),
	}
	_ = store.CreateVersion(ctx, v)

	bld := &domain.ArtifactBuild{
		ID:          "bld_conc",
		ActionID:    "act_conc",
		VersionID:   "ver_conc",
		BuildNumber: 1,
		Status:      domain.BuildStatusSucceeded,
		CreatedAt:   time.Now().UTC(),
	}
	_ = store.CreateBuild(ctx, bld)

	run1 := &domain.Run{
		ID:              "run_c1",
		ActionID:        "act_conc",
		ActionVersionID: "ver_conc",
		ArtifactBuildID: "bld_conc",
		TriggerType:     domain.TriggerTypeManual,
		Status:          domain.RunStatusQueued,
		CreatedAt:       time.Now().UTC(),
	}
	run2 := &domain.Run{
		ID:              "run_c2",
		ActionID:        "act_conc",
		ActionVersionID: "ver_conc",
		ArtifactBuildID: "bld_conc",
		TriggerType:     domain.TriggerTypeManual,
		Status:          domain.RunStatusQueued,
		CreatedAt:       time.Now().UTC().Add(1 * time.Second),
	}
	_ = store.CreateRun(ctx, run1)
	_ = store.CreateRun(ctx, run2)

	// Claim first run
	claimed1, err := store.ClaimNextQueuedRun(ctx)
	if err != nil || claimed1 == nil || claimed1.ID != "run_c1" {
		t.Fatalf("expected to claim run_c1, got %v, err: %v", claimed1, err)
	}
	if claimed1.Status != domain.RunStatusRunning {
		t.Fatalf("expected claimed run status to be running, got %v", claimed1.Status)
	}

	// Claim again -> should return nil because act_conc has max_concurrency=1 and 1 is running!
	claimed2, err := store.ClaimNextQueuedRun(ctx)
	if err != nil {
		t.Fatalf("unexpected claim error: %v", err)
	}
	if claimed2 != nil {
		t.Fatalf("expected nil claim due to concurrency limit, but got %v", claimed2.ID)
	}

	// Complete run 1
	completedAt := time.Now().UTC()
	err = store.UpdateRunStatus(ctx, "run_c1", domain.RunStatusSucceeded, nil, "out", "", 100, "", &completedAt)
	if err != nil {
		t.Fatalf("failed to update run status: %v", err)
	}

	// Claim again -> now run2 should be claimed!
	claimed3, err := store.ClaimNextQueuedRun(ctx)
	if err != nil || claimed3 == nil || claimed3.ID != "run_c2" {
		t.Fatalf("expected to claim run_c2, got %v, err: %v", claimed3, err)
	}
}

func TestSQLiteStore_RunTokenHashingAndRevocation(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestStore(t)

	act := &domain.Action{
		ID:        "act_tok",
		Name:      "Tok Action",
		Enabled:   true,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	_ = store.CreateAction(ctx, act)

	v := &domain.ActionVersion{
		ID:            "ver_tok",
		ActionID:      "act_tok",
		VersionNumber: 1,
		SourceDigest:  "digest",
		SourcePath:    "path",
		CreatedAt:     time.Now().UTC(),
	}
	_ = store.CreateVersion(ctx, v)

	bld := &domain.ArtifactBuild{
		ID:          "bld_tok",
		ActionID:    "act_tok",
		VersionID:   "ver_tok",
		BuildNumber: 1,
		Status:      domain.BuildStatusSucceeded,
		CreatedAt:   time.Now().UTC(),
	}
	_ = store.CreateBuild(ctx, bld)

	run := &domain.Run{
		ID:              "run_tok",
		ActionID:        "act_tok",
		ActionVersionID: "ver_tok",
		ArtifactBuildID: "bld_tok",
		TriggerType:     domain.TriggerTypeManual,
		Status:          domain.RunStatusRunning,
		CreatedAt:       time.Now().UTC(),
	}
	_ = store.CreateRun(ctx, run)

	rawToken, hash, err := domain.GenerateRawToken()
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	token := &domain.RunToken{
		ID:        "tok_record_1",
		TokenHash: hash,
		RunID:     "run_tok",
		ActionID:  "act_tok",
		Scopes:    []string{domain.ScopeStateWrite, domain.ScopeFrostAgentSendMsg},
		ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
		CreatedAt: time.Now().UTC(),
	}
	if err := store.CreateRunToken(ctx, token); err != nil {
		t.Fatalf("failed to store run token: %v", err)
	}

	// Verify query by raw token's hash
	computedHash := domain.HashToken(rawToken)
	queried, err := store.GetRunTokenByHash(ctx, computedHash)
	if err != nil || queried.ActionID != "act_tok" {
		t.Fatalf("failed to query token by hash: %v", err)
	}
	if !queried.IsValid(time.Now().UTC()) {
		t.Fatal("expected token to be valid")
	}

	// Revoke tokens for run
	revokedAt := time.Now().UTC()
	if err := store.RevokeTokensForRun(ctx, "run_tok", revokedAt); err != nil {
		t.Fatalf("failed to revoke tokens: %v", err)
	}

	queriedAfter, err := store.GetRunTokenByHash(ctx, computedHash)
	if err != nil {
		t.Fatalf("failed to get token after revocation: %v", err)
	}
	if queriedAfter.IsValid(time.Now().UTC()) {
		t.Fatal("expected token to be invalid after revocation")
	}
}

func TestFileStore_SourceAndArtifactBundles(t *testing.T) {
	_, fileStore := setupTestStore(t)

	sourceFiles := map[string][]byte{
		"main.go": []byte("package main\nfunc main() {}\n"),
		"go.mod":  []byte("module example.com/test\ngo 1.25.3\n"),
	}

	digest1, relPath1, err := fileStore.SaveSourceBundle("act_fs", "ver_1", sourceFiles)
	if err != nil {
		t.Fatalf("save source bundle: %v", err)
	}
	if digest1 == "" || relPath1 == "" {
		t.Fatalf("empty digest or relPath")
	}

	readSources, err := fileStore.ReadSourceBundle("act_fs", "ver_1")
	if err != nil {
		t.Fatalf("read source bundle: %v", err)
	}
	if len(readSources) != 2 || string(readSources["main.go"]) != string(sourceFiles["main.go"]) {
		t.Fatalf("mismatched source contents")
	}

	// Artifact bundle
	artFiles := map[string][]byte{
		"entrypoint": []byte("\x7fELFfake-binary-content"),
	}
	artDigest, artRel, artSize, err := fileStore.SaveArtifactBundle("act_fs", "ver_1", "bld_1", artFiles)
	if err != nil {
		t.Fatalf("save artifact bundle: %v", err)
	}
	if artSize != int64(len(artFiles["entrypoint"])) || artDigest == "" || artRel == "" {
		t.Fatalf("invalid artifact metadata: size=%d, digest=%s, rel=%s", artSize, artDigest, artRel)
	}

	readArtifacts, err := fileStore.ReadArtifactBundle("act_fs", "ver_1", "bld_1")
	if err != nil {
		t.Fatalf("read artifact bundle: %v", err)
	}
	if string(readArtifacts["entrypoint"]) != string(artFiles["entrypoint"]) {
		t.Fatalf("artifact content mismatch")
	}
}
