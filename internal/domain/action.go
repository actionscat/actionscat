package domain

import (
	"time"
)

// Action represents a managed long-lived action identity.
type Action struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Description     string    `json:"description"`
	ActiveVersionID string    `json:"active_version_id,omitempty"`
	ActiveBuildID   string    `json:"active_build_id,omitempty"`
	MaxConcurrency  int       `json:"max_concurrency"` // default 1
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
