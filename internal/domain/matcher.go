package domain

import (
	"time"
)

// MatchType defines how a Matcher compares input text.
type MatchType string

const (
	MatchTypeExact    MatchType = "exact"
	MatchTypeContains MatchType = "contains"
	MatchTypeRegex    MatchType = "regex"
)

// Matcher represents a deterministic message pattern rule that produces Runs upon match.
type Matcher struct {
	ID            string            `json:"id"`
	ActionID      string            `json:"action_id"`
	Name          string            `json:"name"`
	MatchType     MatchType         `json:"match_type"`
	Pattern       string            `json:"pattern"`
	TargetField   string            `json:"target_field"`    // "text", "user_id", "group_id", "platform", default "text"
	CaptureEnvMap map[string]string `json:"capture_env_map"` // maps capture name -> env var name
	Priority      int               `json:"priority"`        // higher executes first
	Enabled       bool              `json:"enabled"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

// MessageEvent represents a platform-neutral incoming message event for matching.
type MessageEvent struct {
	Text      string            `json:"text"`
	Platform  string            `json:"platform"`
	SessionID string            `json:"session_id"`
	UserID    string            `json:"user_id"`
	GroupID   string            `json:"group_id"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}
