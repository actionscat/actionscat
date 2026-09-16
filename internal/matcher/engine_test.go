package matcher

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

func setupTestMatcherEngine(t *testing.T) (
	*store.SQLiteStore,
	*store.FileStore,
	*runner.Runner,
	*Engine,
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

	eng := NewEngine(st, r)
	return st, fs, r, eng
}

func createActionWithArtifact(
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
		Name:           "Action " + actionID,
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

func TestEngine_ExactAndContainsMatch(t *testing.T) {
	st, fs, _, eng := setupTestMatcherEngine(t)
	ctx := t.Context()
	now := time.Now().UTC()

	actExact, _, _ := createActionWithArtifact(t, st, fs, "act_exact")
	actContains, _, _ := createActionWithArtifact(t, st, fs, "act_contains")

	// Exact matcher
	mExact := &domain.Matcher{
		ID:          "m_exact",
		ActionID:    actExact.ID,
		Name:        "Ping Rule",
		MatchType:   domain.MatchTypeExact,
		Pattern:     "/ping",
		TargetField: "text",
		Priority:    10,
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	_ = st.CreateMatcher(ctx, mExact)

	// Contains matcher
	mContains := &domain.Matcher{
		ID:          "m_contains",
		ActionID:    actContains.ID,
		Name:        "Help Rule",
		MatchType:   domain.MatchTypeContains,
		Pattern:     "help",
		TargetField: "text",
		Priority:    5,
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	_ = st.CreateMatcher(ctx, mContains)

	// Test exact match
	event1 := domain.MessageEvent{
		Platform:  "qq",
		SessionID: "group:user",
		UserID:    "user_1",
		GroupID:   "group_1",
		Text:      "/ping",
	}
	runs1, err := eng.MatchAndDispatch(ctx, event1)
	if err != nil {
		t.Fatalf("match exact: %v", err)
	}
	if len(runs1) != 1 || runs1[0].ActionID != actExact.ID {
		t.Fatalf("expected exact match on act_exact, got %d runs", len(runs1))
	}
	if runs1[0].TriggerType != domain.TriggerTypeMatcher {
		t.Fatalf("expected trigger_type matcher, got %s", runs1[0].TriggerType)
	}
	if runs1[0].PlannedEnv["ACTIONSCAT_EVENT_TEXT"] != "/ping" {
		t.Fatalf("expected event text injected, got %s", runs1[0].PlannedEnv["ACTIONSCAT_EVENT_TEXT"])
	}

	// Test contains match
	event2 := domain.MessageEvent{
		Platform: "telegram",
		Text:     "please send help immediately",
	}
	runs2, err := eng.MatchAndDispatch(ctx, event2)
	if err != nil {
		t.Fatalf("match contains: %v", err)
	}
	if len(runs2) != 1 || runs2[0].ActionID != actContains.ID {
		t.Fatalf("expected contains match on act_contains, got %d runs", len(runs2))
	}
}

func TestEngine_RegexWithNamedCaptures(t *testing.T) {
	st, fs, _, eng := setupTestMatcherEngine(t)
	ctx := t.Context()
	now := time.Now().UTC()

	actBili, _, _ := createActionWithArtifact(t, st, fs, "act_bili")

	mRegex := &domain.Matcher{
		ID:          "m_bili",
		ActionID:    actBili.ID,
		Name:        "Bilibili BVID Matcher",
		MatchType:   domain.MatchTypeRegex,
		Pattern:     `(?P<bvid>BV[0-9A-Za-z]{10})`,
		TargetField: "text",
		CaptureEnvMap: map[string]string{
			"bvid": "PARAM_BVID",
		},
		Priority:  50,
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	_ = st.CreateMatcher(ctx, mRegex)

	event := domain.MessageEvent{
		Platform: "qq",
		Text:     "Check out this video: https://www.bilibili.com/video/BV1xx411c7mD and enjoy!",
	}
	runs, err := eng.MatchAndDispatch(ctx, event)
	if err != nil {
		t.Fatalf("match regex: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}

	run := runs[0]
	if run.PlannedEnv["PARAM_BVID"] != "BV1xx411c7mD" {
		t.Fatalf("expected named capture PARAM_BVID=BV1xx411c7mD, got %q", run.PlannedEnv["PARAM_BVID"])
	}
}

func TestEngine_PriorityAndShortCircuit(t *testing.T) {
	st, fs, _, eng := setupTestMatcherEngine(t)
	ctx := t.Context()
	now := time.Now().UTC()

	actHigh, _, _ := createActionWithArtifact(t, st, fs, "act_high")
	actLow, _, _ := createActionWithArtifact(t, st, fs, "act_low")

	// High priority matcher (priority=100, continue_matching=false)
	mHigh := &domain.Matcher{
		ID:               "m_high",
		ActionID:         actHigh.ID,
		Name:             "High Priority",
		MatchType:        domain.MatchTypeContains,
		Pattern:          "trigger",
		Priority:         100,
		ContinueMatching: false, // Short-circuit!
		Enabled:          true,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	_ = st.CreateMatcher(ctx, mHigh)

	// Low priority matcher (priority=10)
	mLow := &domain.Matcher{
		ID:               "m_low",
		ActionID:         actLow.ID,
		Name:             "Low Priority",
		MatchType:        domain.MatchTypeContains,
		Pattern:          "trigger",
		Priority:         10,
		ContinueMatching: false,
		Enabled:          true,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	_ = st.CreateMatcher(ctx, mLow)

	event := domain.MessageEvent{
		Text: "test trigger message",
	}

	runs, err := eng.MatchAndDispatch(ctx, event)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	// Must short-circuit on high priority
	if len(runs) != 1 {
		t.Fatalf("expected exactly 1 run due to short-circuit, got %d", len(runs))
	}
	if runs[0].ActionID != actHigh.ID {
		t.Fatalf("expected high priority action, got %s", runs[0].ActionID)
	}
}

func TestEngine_TargetFieldRouting(t *testing.T) {
	st, fs, _, eng := setupTestMatcherEngine(t)
	ctx := t.Context()
	now := time.Now().UTC()

	actGroup, _, _ := createActionWithArtifact(t, st, fs, "act_group")

	// Matcher on group_id
	mGroup := &domain.Matcher{
		ID:          "m_group",
		ActionID:    actGroup.ID,
		Name:        "Special Group Rule",
		MatchType:   domain.MatchTypeExact,
		Pattern:     "target_group_999",
		TargetField: "group_id",
		Priority:    10,
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	_ = st.CreateMatcher(ctx, mGroup)

	// Event with matching group_id
	matchingEvent := domain.MessageEvent{
		GroupID: "target_group_999",
		Text:    "random text",
	}
	runs, err := eng.MatchAndDispatch(ctx, matchingEvent)
	if err != nil || len(runs) != 1 {
		t.Fatalf("expected match on target_field=group_id, got runs=%d err=%v", len(runs), err)
	}

	// Event with different group_id
	otherEvent := domain.MessageEvent{
		GroupID: "other_group",
		Text:    "target_group_999 in text",
	}
	runsOther, _ := eng.MatchAndDispatch(ctx, otherEvent)
	if len(runsOther) != 0 {
		t.Fatalf("expected no match when group_id differs, got %d", len(runsOther))
	}
}
