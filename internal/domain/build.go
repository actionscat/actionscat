package domain

import (
	"time"
)

// BuildStatus represents the state of an artifact build.
type BuildStatus string

const (
	BuildStatusPending   BuildStatus = "pending"
	BuildStatusBuilding  BuildStatus = "building"
	BuildStatusSucceeded BuildStatus = "succeeded"
	BuildStatusFailed    BuildStatus = "failed"
	BuildStatusTimedOut  BuildStatus = "timed_out"
)

// ArtifactBuild represents the output of building a specific ActionVersion under a toolchain.
type ArtifactBuild struct {
	ID               string      `json:"id"`
	ActionID         string      `json:"action_id"`
	VersionID        string      `json:"version_id"`
	BuildNumber      int         `json:"build_number"`
	Status           BuildStatus `json:"status"`
	BuilderProfile   string      `json:"builder_profile"`
	ToolchainVersion string      `json:"toolchain_version"` // e.g. "go1.25.6"
	BuildCommand     string      `json:"build_command"`
	Stdout           string      `json:"stdout"`
	Stderr           string      `json:"stderr"`
	ExitCode         *int        `json:"exit_code,omitempty"`
	ArtifactDigest   string      `json:"artifact_digest"` // SHA256 of artifact bundle
	ArtifactPath     string      `json:"artifact_path"`   // relative path to data dir
	ArtifactSize     int64       `json:"artifact_size"`
	StartedAt        *time.Time  `json:"started_at,omitempty"`
	CompletedAt      *time.Time  `json:"completed_at,omitempty"`
	CreatedAt        time.Time   `json:"created_at"`
}
