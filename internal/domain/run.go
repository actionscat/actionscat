package domain

import (
	"time"
)

// TriggerType indicates what initiated the Run.
type TriggerType string

const (
	TriggerTypeManual   TriggerType = "manual"
	TriggerTypeSchedule TriggerType = "schedule"
	TriggerTypeMatcher  TriggerType = "matcher"
)

// RunStatus represents the execution state of a Run.
type RunStatus string

const (
	RunStatusQueued    RunStatus = "queued"
	RunStatusRunning   RunStatus = "running"
	RunStatusSucceeded RunStatus = "succeeded"
	RunStatusFailed    RunStatus = "failed"
	RunStatusTimedOut    RunStatus = "timed_out"
	RunStatusCancelled  RunStatus = "cancelled"
	RunStatusInterrupted RunStatus = "interrupted"
)

// IsTerminal returns true if the Run has reached a final state.
func (s RunStatus) IsTerminal() bool {
	switch s {
	case RunStatusSucceeded, RunStatusFailed, RunStatusTimedOut, RunStatusCancelled, RunStatusInterrupted:
		return true
	default:
		return false
	}
}

// Run represents an immutable execution of a specific ActionVersion and ArtifactBuild.
type Run struct {
	ID              string            `json:"id"`
	ActionID        string            `json:"action_id"`
	ActionVersionID string            `json:"action_version_id"`
	ArtifactBuildID string            `json:"artifact_build_id"`
	TriggerType     TriggerType       `json:"trigger_type"`
	TriggerMetadata map[string]string `json:"trigger_metadata,omitempty"`
	PlannedEnv      map[string]string `json:"planned_env,omitempty"`
	Status          RunStatus         `json:"status"`
	ExitCode        *int              `json:"exit_code,omitempty"`
	Stdout          string            `json:"stdout"`
	Stderr          string            `json:"stderr"`
	DurationMs      int64             `json:"duration_ms"`
	ErrorMessage    string            `json:"error_message,omitempty"`
	StartedAt       *time.Time        `json:"started_at,omitempty"`
	CompletedAt     *time.Time        `json:"completed_at,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
}
