package domain

import (
	"time"
)

// Schedule represents a deterministic cron schedule that produces Runs.
type Schedule struct {
	ID        string     `json:"id"`
	ActionID  string     `json:"action_id"`
	CronExpr  string     `json:"cron_expr"`
	Timezone  string     `json:"timezone"` // IANA timezone, e.g. "UTC", "Asia/Shanghai"
	NextRunAt time.Time  `json:"next_run_at"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	Enabled   bool       `json:"enabled"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ScheduleOccurrence records a processed schedule run trigger to ensure idempotency.
type ScheduleOccurrence struct {
	ScheduleID   string    `json:"schedule_id"`
	ScheduledFor time.Time `json:"scheduled_for"`
	RunID        string    `json:"run_id"`
	CreatedAt    time.Time `json:"created_at"`
}
