package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const schemaDDL = `
CREATE TABLE IF NOT EXISTS actions (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	active_version_id TEXT,
	active_build_id TEXT,
	max_concurrency INTEGER NOT NULL DEFAULT 1,
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS action_versions (
	id TEXT PRIMARY KEY,
	action_id TEXT NOT NULL REFERENCES actions(id) ON DELETE CASCADE,
	version_number INTEGER NOT NULL,
	source_digest TEXT NOT NULL,
	source_path TEXT NOT NULL,
	build_spec_json TEXT NOT NULL,
	runtime_spec_json TEXT NOT NULL,
	state_injections_json TEXT NOT NULL,
	runtime_capabilities_json TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	UNIQUE(action_id, version_number)
);

CREATE TABLE IF NOT EXISTS artifact_builds (
	id TEXT PRIMARY KEY,
	action_id TEXT NOT NULL REFERENCES actions(id) ON DELETE CASCADE,
	version_id TEXT NOT NULL REFERENCES action_versions(id) ON DELETE CASCADE,
	build_number INTEGER NOT NULL,
	status TEXT NOT NULL,
	builder_profile TEXT NOT NULL,
	toolchain_version TEXT NOT NULL,
	build_command TEXT NOT NULL,
	stdout TEXT NOT NULL DEFAULT '',
	stderr TEXT NOT NULL DEFAULT '',
	exit_code INTEGER,
	artifact_digest TEXT NOT NULL DEFAULT '',
	artifact_path TEXT NOT NULL DEFAULT '',
	artifact_size INTEGER NOT NULL DEFAULT 0,
	started_at DATETIME,
	completed_at DATETIME,
	created_at DATETIME NOT NULL,
	UNIQUE(version_id, build_number)
);

CREATE TABLE IF NOT EXISTS schedules (
	id TEXT PRIMARY KEY,
	action_id TEXT NOT NULL REFERENCES actions(id) ON DELETE CASCADE,
	cron_expr TEXT NOT NULL,
	timezone TEXT NOT NULL DEFAULT 'UTC',
	next_run_at DATETIME NOT NULL,
	last_run_at DATETIME,
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS schedule_occurrences (
	schedule_id TEXT NOT NULL REFERENCES schedules(id) ON DELETE CASCADE,
	scheduled_for DATETIME NOT NULL,
	run_id TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	PRIMARY KEY (schedule_id, scheduled_for)
);

CREATE TABLE IF NOT EXISTS matchers (
	id TEXT PRIMARY KEY,
	action_id TEXT NOT NULL REFERENCES actions(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	match_type TEXT NOT NULL,
	pattern TEXT NOT NULL,
	target_field TEXT NOT NULL DEFAULT 'text',
	capture_env_map_json TEXT NOT NULL DEFAULT '{}',
	priority INTEGER NOT NULL DEFAULT 0,
	continue_matching INTEGER NOT NULL DEFAULT 0,
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS runs (
	id TEXT PRIMARY KEY,
	action_id TEXT NOT NULL REFERENCES actions(id),
	action_version_id TEXT NOT NULL REFERENCES action_versions(id),
	artifact_build_id TEXT NOT NULL REFERENCES artifact_builds(id),
	trigger_type TEXT NOT NULL,
	trigger_metadata_json TEXT NOT NULL DEFAULT '{}',
	planned_env_json TEXT NOT NULL DEFAULT '{}',
	status TEXT NOT NULL,
	exit_code INTEGER,
	stdout TEXT NOT NULL DEFAULT '',
	stderr TEXT NOT NULL DEFAULT '',
	duration_ms INTEGER NOT NULL DEFAULT 0,
	error_message TEXT NOT NULL DEFAULT '',
	started_at DATETIME,
	completed_at DATETIME,
	created_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS run_tokens (
	id TEXT PRIMARY KEY,
	token_hash TEXT NOT NULL UNIQUE,
	run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
	action_id TEXT NOT NULL REFERENCES actions(id) ON DELETE CASCADE,
	scopes_json TEXT NOT NULL,
	expires_at DATETIME NOT NULL,
	revoked_at DATETIME,
	created_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_runs_status ON runs(status);
CREATE INDEX IF NOT EXISTS idx_runs_action ON runs(action_id);
CREATE INDEX IF NOT EXISTS idx_schedules_next_run ON schedules(enabled, next_run_at);
CREATE INDEX IF NOT EXISTS idx_run_tokens_hash ON run_tokens(token_hash);
`

// OpenDB opens a SQLite database at dbPath and configures pragmas.
func OpenDB(dbPath string) (*sql.DB, error) {
	if dbPath != ":memory:" {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0750); err != nil {
			return nil, fmt.Errorf("failed to create database dir: %w", err)
		}
	}

	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// SQLite WAL mode supports multiple readers with single writer; serialize pool connections to avoid 'database is locked'
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA busy_timeout=10000;
		PRAGMA foreign_keys=ON;
		PRAGMA synchronous=NORMAL;
	`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to configure sqlite pragmas: %w", err)
	}

	if _, err := db.Exec(schemaDDL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to run database migration: %w", err)
	}

	return db, nil
}
