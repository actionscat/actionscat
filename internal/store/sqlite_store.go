package store

import (
	"actionscat/internal/domain"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrConflict      = errors.New("conflict: duplicate or constraint violation")
	ErrInvalidStatus = errors.New("invalid status transition")
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db}
}

// ---------------------------------------------------------
// Action operations
// ---------------------------------------------------------

func (s *SQLiteStore) CreateAction(ctx context.Context, act *domain.Action) error {
	query := `
	INSERT INTO actions (id, name, description, active_version_id, active_build_id, max_concurrency, enabled, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);`
	_, err := s.db.ExecContext(ctx, query,
		act.ID, act.Name, act.Description, act.ActiveVersionID, act.ActiveBuildID,
		act.MaxConcurrency, act.Enabled, act.CreatedAt, act.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create action: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetAction(ctx context.Context, id string) (*domain.Action, error) {
	query := `
	SELECT id, name, description, active_version_id, active_build_id, max_concurrency, enabled, created_at, updated_at
	FROM actions WHERE id = ?;`
	row := s.db.QueryRowContext(ctx, query, id)

	var act domain.Action
	var activeVer, activeBuild sql.NullString
	err := row.Scan(
		&act.ID, &act.Name, &act.Description, &activeVer, &activeBuild,
		&act.MaxConcurrency, &act.Enabled, &act.CreatedAt, &act.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query action: %w", err)
	}
	if activeVer.Valid {
		act.ActiveVersionID = activeVer.String
	}
	if activeBuild.Valid {
		act.ActiveBuildID = activeBuild.String
	}
	return &act, nil
}

func (s *SQLiteStore) UpdateAction(ctx context.Context, act *domain.Action) error {
	query := `
	UPDATE actions
	SET name = ?, description = ?, active_version_id = ?, active_build_id = ?, max_concurrency = ?, enabled = ?, updated_at = ?
	WHERE id = ?;`
	res, err := s.db.ExecContext(ctx, query,
		act.Name, act.Description, act.ActiveVersionID, act.ActiveBuildID,
		act.MaxConcurrency, act.Enabled, act.UpdatedAt, act.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update action: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) SetActiveBuild(ctx context.Context, actionID, versionID, buildID string) error {
	query := `
	UPDATE actions
	SET active_version_id = ?, active_build_id = ?, updated_at = ?
	WHERE id = ?;`
	res, err := s.db.ExecContext(ctx, query, versionID, buildID, time.Now().UTC(), actionID)
	if err != nil {
		return fmt.Errorf("failed to set active build: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) ListActions(ctx context.Context) ([]*domain.Action, error) {
	query := `
	SELECT id, name, description, active_version_id, active_build_id, max_concurrency, enabled, created_at, updated_at
	FROM actions ORDER BY created_at DESC;`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list actions: %w", err)
	}
	defer rows.Close()

	var list []*domain.Action
	for rows.Next() {
		var act domain.Action
		var activeVer, activeBuild sql.NullString
		if err := rows.Scan(
			&act.ID, &act.Name, &act.Description, &activeVer, &activeBuild,
			&act.MaxConcurrency, &act.Enabled, &act.CreatedAt, &act.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan action: %w", err)
		}
		if activeVer.Valid {
			act.ActiveVersionID = activeVer.String
		}
		if activeBuild.Valid {
			act.ActiveBuildID = activeBuild.String
		}
		list = append(list, &act)
	}
	return list, nil
}

func (s *SQLiteStore) DeleteAction(ctx context.Context, id string) error {
	query := `DELETE FROM actions WHERE id = ?;`
	res, err := s.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete action: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------
// ActionVersion operations (immutable once created)
// ---------------------------------------------------------

func (s *SQLiteStore) CreateVersion(ctx context.Context, v *domain.ActionVersion) error {
	buildSpecJSON, err := json.Marshal(v.BuildSpec)
	if err != nil {
		return fmt.Errorf("marshal build_spec: %w", err)
	}
	runtimeSpecJSON, err := json.Marshal(v.RuntimeSpec)
	if err != nil {
		return fmt.Errorf("marshal runtime_spec: %w", err)
	}
	stateInjectionsJSON, err := json.Marshal(v.StateInjections)
	if err != nil {
		return fmt.Errorf("marshal state_injections: %w", err)
	}
	runtimeCapabilitiesJSON, err := json.Marshal(v.RuntimeCapabilities)
	if err != nil {
		return fmt.Errorf("marshal runtime_capabilities: %w", err)
	}

	query := `
	INSERT INTO action_versions (
		id, action_id, version_number, source_digest, source_path,
		build_spec_json, runtime_spec_json, state_injections_json, runtime_capabilities_json, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`

	_, err = s.db.ExecContext(ctx, query,
		v.ID, v.ActionID, v.VersionNumber, v.SourceDigest, v.SourcePath,
		string(buildSpecJSON), string(runtimeSpecJSON), string(stateInjectionsJSON), string(runtimeCapabilitiesJSON), v.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create action version: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetVersion(ctx context.Context, id string) (*domain.ActionVersion, error) {
	query := `
	SELECT id, action_id, version_number, source_digest, source_path,
	       build_spec_json, runtime_spec_json, state_injections_json, runtime_capabilities_json, created_at
	FROM action_versions WHERE id = ?;`
	row := s.db.QueryRowContext(ctx, query, id)
	return scanVersion(row)
}

func (s *SQLiteStore) GetVersionByNumber(ctx context.Context, actionID string, number int) (*domain.ActionVersion, error) {
	query := `
	SELECT id, action_id, version_number, source_digest, source_path,
	       build_spec_json, runtime_spec_json, state_injections_json, runtime_capabilities_json, created_at
	FROM action_versions WHERE action_id = ? AND version_number = ?;`
	row := s.db.QueryRowContext(ctx, query, actionID, number)
	return scanVersion(row)
}

func (s *SQLiteStore) GetNextVersionNumber(ctx context.Context, actionID string) (int, error) {
	query := `SELECT COALESCE(MAX(version_number), 0) + 1 FROM action_versions WHERE action_id = ?;`
	var nextNum int
	err := s.db.QueryRowContext(ctx, query, actionID).Scan(&nextNum)
	if err != nil {
		return 0, fmt.Errorf("failed to get next version number: %w", err)
	}
	return nextNum, nil
}

func (s *SQLiteStore) ListVersions(ctx context.Context, actionID string) ([]*domain.ActionVersion, error) {
	query := `
	SELECT id, action_id, version_number, source_digest, source_path,
	       build_spec_json, runtime_spec_json, state_injections_json, runtime_capabilities_json, created_at
	FROM action_versions WHERE action_id = ? ORDER BY version_number DESC;`
	rows, err := s.db.QueryContext(ctx, query, actionID)
	if err != nil {
		return nil, fmt.Errorf("failed to list versions: %w", err)
	}
	defer rows.Close()

	var list []*domain.ActionVersion
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanVersion(s rowScanner) (*domain.ActionVersion, error) {
	var v domain.ActionVersion
	var buildSpecJSON, runtimeSpecJSON, stateInjectionsJSON, capabilitiesJSON string

	err := s.Scan(
		&v.ID, &v.ActionID, &v.VersionNumber, &v.SourceDigest, &v.SourcePath,
		&buildSpecJSON, &runtimeSpecJSON, &stateInjectionsJSON, &capabilitiesJSON, &v.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to scan version: %w", err)
	}

	if err := json.Unmarshal([]byte(buildSpecJSON), &v.BuildSpec); err != nil {
		return nil, fmt.Errorf("unmarshal build_spec: %w", err)
	}
	if err := json.Unmarshal([]byte(runtimeSpecJSON), &v.RuntimeSpec); err != nil {
		return nil, fmt.Errorf("unmarshal runtime_spec: %w", err)
	}
	if err := json.Unmarshal([]byte(stateInjectionsJSON), &v.StateInjections); err != nil {
		return nil, fmt.Errorf("unmarshal state_injections: %w", err)
	}
	if err := json.Unmarshal([]byte(capabilitiesJSON), &v.RuntimeCapabilities); err != nil {
		return nil, fmt.Errorf("unmarshal runtime_capabilities: %w", err)
	}

	return &v, nil
}

// ---------------------------------------------------------
// ArtifactBuild operations
// ---------------------------------------------------------

func (s *SQLiteStore) CreateBuild(ctx context.Context, b *domain.ArtifactBuild) error {
	query := `
	INSERT INTO artifact_builds (
		id, action_id, version_id, build_number, status, builder_profile,
		toolchain_version, build_command, stdout, stderr, exit_code,
		artifact_digest, artifact_path, artifact_size, started_at, completed_at, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`

	_, err := s.db.ExecContext(ctx, query,
		b.ID, b.ActionID, b.VersionID, b.BuildNumber, string(b.Status), b.BuilderProfile,
		b.ToolchainVersion, b.BuildCommand, b.Stdout, b.Stderr, b.ExitCode,
		b.ArtifactDigest, b.ArtifactPath, b.ArtifactSize, b.StartedAt, b.CompletedAt, b.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create build: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetBuild(ctx context.Context, id string) (*domain.ArtifactBuild, error) {
	query := `
	SELECT id, action_id, version_id, build_number, status, builder_profile,
	       toolchain_version, build_command, stdout, stderr, exit_code,
	       artifact_digest, artifact_path, artifact_size, started_at, completed_at, created_at
	FROM artifact_builds WHERE id = ?;`
	row := s.db.QueryRowContext(ctx, query, id)
	return scanBuild(row)
}

func (s *SQLiteStore) GetNextBuildNumber(ctx context.Context, versionID string) (int, error) {
	query := `SELECT COALESCE(MAX(build_number), 0) + 1 FROM artifact_builds WHERE version_id = ?;`
	var nextNum int
	err := s.db.QueryRowContext(ctx, query, versionID).Scan(&nextNum)
	if err != nil {
		return 0, fmt.Errorf("failed to get next build number: %w", err)
	}
	return nextNum, nil
}

func (s *SQLiteStore) UpdateBuildResult(
	ctx context.Context,
	buildID string,
	status domain.BuildStatus,
	toolchainVersion string,
	stdout string,
	stderr string,
	exitCode *int,
	digest string,
	path string,
	size int64,
	completedAt time.Time,
) error {
	query := `
	UPDATE artifact_builds
	SET status = ?, toolchain_version = ?, stdout = ?, stderr = ?, exit_code = ?,
	    artifact_digest = ?, artifact_path = ?, artifact_size = ?, completed_at = ?
	WHERE id = ?;`

	res, err := s.db.ExecContext(ctx, query,
		string(status), toolchainVersion, stdout, stderr, exitCode,
		digest, path, size, completedAt, buildID,
	)
	if err != nil {
		return fmt.Errorf("failed to update build result: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) ListBuilds(ctx context.Context, versionID string) ([]*domain.ArtifactBuild, error) {
	query := `
	SELECT id, action_id, version_id, build_number, status, builder_profile,
	       toolchain_version, build_command, stdout, stderr, exit_code,
	       artifact_digest, artifact_path, artifact_size, started_at, completed_at, created_at
	FROM artifact_builds WHERE version_id = ? ORDER BY build_number DESC;`
	rows, err := s.db.QueryContext(ctx, query, versionID)
	if err != nil {
		return nil, fmt.Errorf("failed to list builds: %w", err)
	}
	defer rows.Close()

	var list []*domain.ArtifactBuild
	for rows.Next() {
		b, err := scanBuild(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, b)
	}
	return list, nil
}

func (s *SQLiteStore) ListBuildsForAction(ctx context.Context, actionID string) ([]*domain.ArtifactBuild, error) {
	query := `
	SELECT id, action_id, version_id, build_number, status, builder_profile,
	       toolchain_version, build_command, stdout, stderr, exit_code,
	       artifact_digest, artifact_path, artifact_size, started_at, completed_at, created_at
	FROM artifact_builds WHERE action_id = ? ORDER BY created_at DESC;`
	rows, err := s.db.QueryContext(ctx, query, actionID)
	if err != nil {
		return nil, fmt.Errorf("failed to list builds for action: %w", err)
	}
	defer rows.Close()

	var list []*domain.ArtifactBuild
	for rows.Next() {
		b, err := scanBuild(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, b)
	}
	return list, nil
}

func scanBuild(s rowScanner) (*domain.ArtifactBuild, error) {
	var b domain.ArtifactBuild
	var status string
	var exitCode sql.NullInt32
	var startedAt, completedAt sql.NullTime

	err := s.Scan(
		&b.ID, &b.ActionID, &b.VersionID, &b.BuildNumber, &status, &b.BuilderProfile,
		&b.ToolchainVersion, &b.BuildCommand, &b.Stdout, &b.Stderr, &exitCode,
		&b.ArtifactDigest, &b.ArtifactPath, &b.ArtifactSize, &startedAt, &completedAt, &b.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to scan build: %w", err)
	}
	b.Status = domain.BuildStatus(status)
	if exitCode.Valid {
		ec := int(exitCode.Int32)
		b.ExitCode = &ec
	}
	if startedAt.Valid {
		t := startedAt.Time
		b.StartedAt = &t
	}
	if completedAt.Valid {
		t := completedAt.Time
		b.CompletedAt = &t
	}
	return &b, nil
}

// ---------------------------------------------------------
// Schedule operations
// ---------------------------------------------------------

func (s *SQLiteStore) CreateSchedule(ctx context.Context, sched *domain.Schedule) error {
	query := `
	INSERT INTO schedules (id, action_id, cron_expr, timezone, next_run_at, last_run_at, enabled, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);`
	_, err := s.db.ExecContext(ctx, query,
		sched.ID, sched.ActionID, sched.CronExpr, sched.Timezone,
		sched.NextRunAt, sched.LastRunAt, sched.Enabled, sched.CreatedAt, sched.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create schedule: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetSchedule(ctx context.Context, id string) (*domain.Schedule, error) {
	query := `
	SELECT id, action_id, cron_expr, timezone, next_run_at, last_run_at, enabled, created_at, updated_at
	FROM schedules WHERE id = ?;`
	row := s.db.QueryRowContext(ctx, query, id)

	var sched domain.Schedule
	var lastRun sql.NullTime
	err := row.Scan(
		&sched.ID, &sched.ActionID, &sched.CronExpr, &sched.Timezone,
		&sched.NextRunAt, &lastRun, &sched.Enabled, &sched.CreatedAt, &sched.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to scan schedule: %w", err)
	}
	if lastRun.Valid {
		t := lastRun.Time
		sched.LastRunAt = &t
	}
	return &sched, nil
}

func (s *SQLiteStore) UpdateSchedule(ctx context.Context, sched *domain.Schedule) error {
	query := `
	UPDATE schedules
	SET cron_expr = ?, timezone = ?, next_run_at = ?, last_run_at = ?, enabled = ?, updated_at = ?
	WHERE id = ?;`
	res, err := s.db.ExecContext(ctx, query,
		sched.CronExpr, sched.Timezone, sched.NextRunAt, sched.LastRunAt, sched.Enabled, sched.UpdatedAt, sched.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update schedule: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) AdvanceScheduleNextRun(ctx context.Context, id string, nextRunAt time.Time) error {
	query := `UPDATE schedules SET next_run_at = ?, updated_at = ? WHERE id = ?;`
	res, err := s.db.ExecContext(ctx, query, nextRunAt, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("failed to advance schedule next_run_at: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) ListDueSchedules(ctx context.Context, now time.Time, limit int) ([]*domain.Schedule, error) {
	query := `
	SELECT id, action_id, cron_expr, timezone, next_run_at, last_run_at, enabled, created_at, updated_at
	FROM schedules
	WHERE enabled = 1 AND next_run_at <= ?
	ORDER BY next_run_at ASC
	LIMIT ?;`
	rows, err := s.db.QueryContext(ctx, query, now, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query due schedules: %w", err)
	}
	defer rows.Close()

	var list []*domain.Schedule
	for rows.Next() {
		var sched domain.Schedule
		var lastRun sql.NullTime
		if err := rows.Scan(
			&sched.ID, &sched.ActionID, &sched.CronExpr, &sched.Timezone,
			&sched.NextRunAt, &lastRun, &sched.Enabled, &sched.CreatedAt, &sched.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan due schedule: %w", err)
		}
		if lastRun.Valid {
			t := lastRun.Time
			sched.LastRunAt = &t
		}
		list = append(list, &sched)
	}
	return list, nil
}

// RecordScheduleOccurrence atomically records an occurrence for deduplication,
// advances the schedule's next_run_at and last_run_at, and creates the Run in one transaction.
func (s *SQLiteStore) RecordScheduleOccurrence(
	ctx context.Context,
	scheduleID string,
	scheduledFor time.Time,
	nextRunAt time.Time,
	run *domain.Run,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 1. Insert occurrence (fails if duplicate (schedule_id, scheduled_for))
	occQuery := `
	INSERT INTO schedule_occurrences (schedule_id, scheduled_for, run_id, created_at)
	VALUES (?, ?, ?, ?);`
	if _, err := tx.ExecContext(ctx, occQuery, scheduleID, scheduledFor, run.ID, time.Now().UTC()); err != nil {
		return fmt.Errorf("failed to record occurrence (duplicate trigger prevented): %w", err)
	}

	// 2. Insert Run
	metadataJSON, _ := json.Marshal(run.TriggerMetadata)
	plannedEnvJSON, _ := json.Marshal(run.PlannedEnv)
	runQuery := `
	INSERT INTO runs (
		id, action_id, action_version_id, artifact_build_id, trigger_type,
		trigger_metadata_json, planned_env_json, status, exit_code, stdout, stderr,
		duration_ms, error_message, started_at, completed_at, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`
	if _, err := tx.ExecContext(ctx, runQuery,
		run.ID, run.ActionID, run.ActionVersionID, run.ArtifactBuildID, string(run.TriggerType),
		string(metadataJSON), string(plannedEnvJSON), string(run.Status), run.ExitCode,
		run.Stdout, run.Stderr, run.DurationMs, run.ErrorMessage, run.StartedAt, run.CompletedAt, run.CreatedAt,
	); err != nil {
		return fmt.Errorf("failed to insert run: %w", err)
	}

	// 3. Advance schedule
	now := time.Now().UTC()
	schedQuery := `
	UPDATE schedules
	SET next_run_at = ?, last_run_at = ?, updated_at = ?
	WHERE id = ?;`
	if _, err := tx.ExecContext(ctx, schedQuery, nextRunAt, scheduledFor, now, scheduleID); err != nil {
		return fmt.Errorf("failed to update schedule next_run_at: %w", err)
	}

	return tx.Commit()
}

func (s *SQLiteStore) ListSchedules(ctx context.Context, actionID string) ([]*domain.Schedule, error) {
	query := `
	SELECT id, action_id, cron_expr, timezone, next_run_at, last_run_at, enabled, created_at, updated_at
	FROM schedules WHERE action_id = ? ORDER BY created_at DESC;`
	rows, err := s.db.QueryContext(ctx, query, actionID)
	if err != nil {
		return nil, fmt.Errorf("failed to list schedules: %w", err)
	}
	defer rows.Close()

	var list []*domain.Schedule
	for rows.Next() {
		var sched domain.Schedule
		var lastRun sql.NullTime
		if err := rows.Scan(
			&sched.ID, &sched.ActionID, &sched.CronExpr, &sched.Timezone,
			&sched.NextRunAt, &lastRun, &sched.Enabled, &sched.CreatedAt, &sched.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan schedule: %w", err)
		}
		if lastRun.Valid {
			t := lastRun.Time
			sched.LastRunAt = &t
		}
		list = append(list, &sched)
	}
	return list, nil
}

func (s *SQLiteStore) DeleteSchedule(ctx context.Context, id string) error {
	query := `DELETE FROM schedules WHERE id = ?;`
	res, err := s.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete schedule: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------
// Matcher operations
// ---------------------------------------------------------

func (s *SQLiteStore) CreateMatcher(ctx context.Context, m *domain.Matcher) error {
	captureJSON, err := json.Marshal(m.CaptureEnvMap)
	if err != nil {
		return fmt.Errorf("marshal capture_env_map: %w", err)
	}
	query := `
	INSERT INTO matchers (
		id, action_id, name, match_type, pattern, target_field, capture_env_map_json, priority, continue_matching, enabled, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`
	_, err = s.db.ExecContext(ctx, query,
		m.ID, m.ActionID, m.Name, string(m.MatchType), m.Pattern, m.TargetField,
		string(captureJSON), m.Priority, m.ContinueMatching, m.Enabled, m.CreatedAt, m.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create matcher: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetMatcher(ctx context.Context, id string) (*domain.Matcher, error) {
	query := `
	SELECT id, action_id, name, match_type, pattern, target_field, capture_env_map_json, priority, continue_matching, enabled, created_at, updated_at
	FROM matchers WHERE id = ?;`
	row := s.db.QueryRowContext(ctx, query, id)
	return scanMatcher(row)
}

func (s *SQLiteStore) ListEnabledMatchers(ctx context.Context) ([]*domain.Matcher, error) {
	query := `
	SELECT id, action_id, name, match_type, pattern, target_field, capture_env_map_json, priority, continue_matching, enabled, created_at, updated_at
	FROM matchers
	WHERE enabled = 1
	ORDER BY priority DESC, created_at ASC;`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query enabled matchers: %w", err)
	}
	defer rows.Close()

	var list []*domain.Matcher
	for rows.Next() {
		m, err := scanMatcher(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, nil
}

func (s *SQLiteStore) ListMatchers(ctx context.Context, actionID string) ([]*domain.Matcher, error) {
	query := `
	SELECT id, action_id, name, match_type, pattern, target_field, capture_env_map_json, priority, continue_matching, enabled, created_at, updated_at
	FROM matchers WHERE action_id = ? ORDER BY priority DESC, created_at ASC;`
	rows, err := s.db.QueryContext(ctx, query, actionID)
	if err != nil {
		return nil, fmt.Errorf("failed to list matchers: %w", err)
	}
	defer rows.Close()

	var list []*domain.Matcher
	for rows.Next() {
		m, err := scanMatcher(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, nil
}

func (s *SQLiteStore) DeleteMatcher(ctx context.Context, id string) error {
	query := `DELETE FROM matchers WHERE id = ?;`
	res, err := s.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete matcher: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanMatcher(s rowScanner) (*domain.Matcher, error) {
	var m domain.Matcher
	var matchType string
	var captureJSON string
	err := s.Scan(
		&m.ID, &m.ActionID, &m.Name, &matchType, &m.Pattern, &m.TargetField,
		&captureJSON, &m.Priority, &m.ContinueMatching, &m.Enabled, &m.CreatedAt, &m.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to scan matcher: %w", err)
	}
	m.MatchType = domain.MatchType(matchType)
	if err := json.Unmarshal([]byte(captureJSON), &m.CaptureEnvMap); err != nil {
		return nil, fmt.Errorf("unmarshal capture_env_map: %w", err)
	}
	return &m, nil
}

// ---------------------------------------------------------
// Run operations
// ---------------------------------------------------------

func (s *SQLiteStore) CreateRun(ctx context.Context, run *domain.Run) error {
	metadataJSON, err := json.Marshal(run.TriggerMetadata)
	if err != nil {
		return fmt.Errorf("marshal trigger_metadata: %w", err)
	}
	plannedEnvJSON, err := json.Marshal(run.PlannedEnv)
	if err != nil {
		return fmt.Errorf("marshal planned_env: %w", err)
	}

	query := `
	INSERT INTO runs (
		id, action_id, action_version_id, artifact_build_id, trigger_type,
		trigger_metadata_json, planned_env_json, status, exit_code, stdout, stderr,
		duration_ms, error_message, started_at, completed_at, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`

	_, err = s.db.ExecContext(ctx, query,
		run.ID, run.ActionID, run.ActionVersionID, run.ArtifactBuildID, string(run.TriggerType),
		string(metadataJSON), string(plannedEnvJSON), string(run.Status), run.ExitCode,
		run.Stdout, run.Stderr, run.DurationMs, run.ErrorMessage, run.StartedAt, run.CompletedAt, run.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to insert run: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetRun(ctx context.Context, id string) (*domain.Run, error) {
	query := `
	SELECT id, action_id, action_version_id, artifact_build_id, trigger_type,
	       trigger_metadata_json, planned_env_json, status, exit_code, stdout, stderr,
	       duration_ms, error_message, started_at, completed_at, created_at
	FROM runs WHERE id = ?;`
	row := s.db.QueryRowContext(ctx, query, id)
	return scanRun(row)
}

func (s *SQLiteStore) UpdateRunStatus(
	ctx context.Context,
	runID string,
	status domain.RunStatus,
	exitCode *int,
	stdout string,
	stderr string,
	durationMs int64,
	errorMessage string,
	completedAt *time.Time,
) error {
	query := `
	UPDATE runs
	SET status = ?, exit_code = ?, stdout = ?, stderr = ?, duration_ms = ?, error_message = ?, completed_at = ?
	WHERE id = ?;`
	res, err := s.db.ExecContext(ctx, query,
		string(status), exitCode, stdout, stderr, durationMs, errorMessage, completedAt, runID,
	)
	if err != nil {
		return fmt.Errorf("failed to update run status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) MarkRunRunning(ctx context.Context, runID string, startedAt time.Time) error {
	query := `UPDATE runs SET status = ?, started_at = ? WHERE id = ? AND status = ?;`
	res, err := s.db.ExecContext(ctx, query, string(domain.RunStatusRunning), startedAt, runID, string(domain.RunStatusQueued))
	if err != nil {
		return fmt.Errorf("failed to mark run running: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrInvalidStatus
	}
	return nil
}

// ClaimNextQueuedRun checks for queued runs whose Action has not exceeded its concurrency limit.
func (s *SQLiteStore) ClaimNextQueuedRun(ctx context.Context) (*domain.Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Find the earliest queued run whose action's currently running runs < action's max_concurrency
	query := `
	SELECT r.id, r.action_id, r.action_version_id, r.artifact_build_id, r.trigger_type,
	       r.trigger_metadata_json, r.planned_env_json, r.status, r.exit_code, r.stdout, r.stderr,
	       r.duration_ms, r.error_message, r.started_at, r.completed_at, r.created_at
	FROM runs r
	JOIN actions a ON r.action_id = a.id
	WHERE r.status = 'queued'
	  AND (
	      SELECT COUNT(*)
	      FROM runs r2
	      WHERE r2.action_id = r.action_id AND r2.status = 'running'
	  ) < a.max_concurrency
	ORDER BY r.created_at ASC
	LIMIT 1;`

	row := tx.QueryRowContext(ctx, query)
	run, err := scanRun(row)
	if errors.Is(err, ErrNotFound) {
		return nil, nil // No runnable run right now
	}
	if err != nil {
		return nil, err
	}

	// Mark as running inside tx
	now := time.Now().UTC()
	updateQuery := `UPDATE runs SET status = 'running', started_at = ? WHERE id = ?;`
	if _, err := tx.ExecContext(ctx, updateQuery, now, run.ID); err != nil {
		return nil, fmt.Errorf("failed to claim run: %w", err)
	}

	run.Status = domain.RunStatusRunning
	run.StartedAt = &now

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *SQLiteStore) ListRuns(ctx context.Context, actionID string, limit, offset int) ([]*domain.Run, error) {
	var query string
	var args []any
	if actionID != "" {
		query = `
		SELECT id, action_id, action_version_id, artifact_build_id, trigger_type,
		       trigger_metadata_json, planned_env_json, status, exit_code, stdout, stderr,
		       duration_ms, error_message, started_at, completed_at, created_at
		FROM runs WHERE action_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?;`
		args = []any{actionID, limit, offset}
	} else {
		query = `
		SELECT id, action_id, action_version_id, artifact_build_id, trigger_type,
		       trigger_metadata_json, planned_env_json, status, exit_code, stdout, stderr,
		       duration_ms, error_message, started_at, completed_at, created_at
		FROM runs ORDER BY created_at DESC LIMIT ? OFFSET ?;`
		args = []any{limit, offset}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query runs: %w", err)
	}
	defer rows.Close()

	var list []*domain.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, r)
	}
	return list, nil
}

func scanRun(s rowScanner) (*domain.Run, error) {
	var r domain.Run
	var triggerType, status string
	var metadataJSON, plannedEnvJSON string
	var exitCode sql.NullInt32
	var startedAt, completedAt sql.NullTime

	err := s.Scan(
		&r.ID, &r.ActionID, &r.ActionVersionID, &r.ArtifactBuildID, &triggerType,
		&metadataJSON, &plannedEnvJSON, &status, &exitCode, &r.Stdout, &r.Stderr,
		&r.DurationMs, &r.ErrorMessage, &startedAt, &completedAt, &r.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to scan run: %w", err)
	}

	r.TriggerType = domain.TriggerType(triggerType)
	r.Status = domain.RunStatus(status)
	if exitCode.Valid {
		ec := int(exitCode.Int32)
		r.ExitCode = &ec
	}
	if startedAt.Valid {
		t := startedAt.Time
		r.StartedAt = &t
	}
	if completedAt.Valid {
		t := completedAt.Time
		r.CompletedAt = &t
	}

	if err := json.Unmarshal([]byte(metadataJSON), &r.TriggerMetadata); err != nil {
		return nil, fmt.Errorf("unmarshal trigger_metadata: %w", err)
	}
	if err := json.Unmarshal([]byte(plannedEnvJSON), &r.PlannedEnv); err != nil {
		return nil, fmt.Errorf("unmarshal planned_env: %w", err)
	}

	return &r, nil
}

// ---------------------------------------------------------
// RunToken operations
// ---------------------------------------------------------

func (s *SQLiteStore) CreateRunToken(ctx context.Context, token *domain.RunToken) error {
	scopesJSON, err := json.Marshal(token.Scopes)
	if err != nil {
		return fmt.Errorf("marshal scopes: %w", err)
	}
	query := `
	INSERT INTO run_tokens (id, token_hash, run_id, action_id, scopes_json, expires_at, revoked_at, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?);`

	_, err = s.db.ExecContext(ctx, query,
		token.ID, token.TokenHash, token.RunID, token.ActionID,
		string(scopesJSON), token.ExpiresAt, token.RevokedAt, token.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to insert run token: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetRunTokenByHash(ctx context.Context, tokenHash string) (*domain.RunToken, error) {
	query := `
	SELECT id, token_hash, run_id, action_id, scopes_json, expires_at, revoked_at, created_at
	FROM run_tokens WHERE token_hash = ?;`
	row := s.db.QueryRowContext(ctx, query, tokenHash)

	var token domain.RunToken
	var scopesJSON string
	var revokedAt sql.NullTime

	err := row.Scan(
		&token.ID, &token.TokenHash, &token.RunID, &token.ActionID,
		&scopesJSON, &token.ExpiresAt, &revokedAt, &token.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query run token: %w", err)
	}
	if revokedAt.Valid {
		t := revokedAt.Time
		token.RevokedAt = &t
	}
	if err := json.Unmarshal([]byte(scopesJSON), &token.Scopes); err != nil {
		return nil, fmt.Errorf("unmarshal scopes: %w", err)
	}

	return &token, nil
}

func (s *SQLiteStore) RevokeRunToken(ctx context.Context, tokenHash string, revokedAt time.Time) error {
	query := `UPDATE run_tokens SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL;`
	_, err := s.db.ExecContext(ctx, query, revokedAt, tokenHash)
	return err
}

func (s *SQLiteStore) RevokeTokensForRun(ctx context.Context, runID string, revokedAt time.Time) error {
	query := `UPDATE run_tokens SET revoked_at = ? WHERE run_id = ? AND revoked_at IS NULL;`
	_, err := s.db.ExecContext(ctx, query, revokedAt, runID)
	return err
}

func (s *SQLiteStore) ListTokensForRun(ctx context.Context, runID string) ([]*domain.RunToken, error) {
	query := `
	SELECT id, token_hash, run_id, action_id, scopes_json, expires_at, revoked_at, created_at
	FROM run_tokens WHERE run_id = ? ORDER BY created_at ASC;`
	rows, err := s.db.QueryContext(ctx, query, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to query run tokens: %w", err)
	}
	defer rows.Close()

	var list []*domain.RunToken
	for rows.Next() {
		var token domain.RunToken
		var scopesJSON string
		var revokedAt sql.NullTime
		if err := rows.Scan(
			&token.ID, &token.TokenHash, &token.RunID, &token.ActionID,
			&scopesJSON, &token.ExpiresAt, &revokedAt, &token.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan run token: %w", err)
		}
		if revokedAt.Valid {
			t := revokedAt.Time
			token.RevokedAt = &t
		}
		if err := json.Unmarshal([]byte(scopesJSON), &token.Scopes); err != nil {
			return nil, fmt.Errorf("unmarshal scopes: %w", err)
		}
		list = append(list, &token)
	}
	return list, nil
}
