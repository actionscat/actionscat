package domain

import (
	"time"
)

// BuildSpec describes how to build the source code into an artifact bundle.
type BuildSpec struct {
	Language             string `json:"language"`              // e.g. "go"
	ToolchainRequirement string `json:"toolchain_requirement"` // e.g. ">= 1.25"
	Command              string `json:"command"`               // e.g. "go build -o /out/entrypoint ."
	Network              bool   `json:"network"`               // whether build requires public network access (e.g. go mod download)
}

// NetworkMode constants define sandbox network confinement modes.
const (
	NetworkModeNone      = "none"
	NetworkModePublic    = "public"
	NetworkModeAllowlist = "allowlist"
	NetworkModeIsolated  = "isolated"
)

// NetworkAllowRule specifies a host and port allowlist entry.
type NetworkAllowRule struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// NetworkPolicy describes network access permissions for a Run.
type NetworkPolicy struct {
	Mode  string             `json:"mode"`            // "none", "public", "allowlist"
	Allow []NetworkAllowRule `json:"allow,omitempty"` // populated when mode is "allowlist"
}

// RuntimeSpec describes execution constraints and entrypoint for a Run.
type RuntimeSpec struct {
	Entrypoint     string        `json:"entrypoint"`      // path to entrypoint executable within artifact
	Network        NetworkPolicy `json:"network"`         // network access policy
	TimeoutSeconds int           `json:"timeout_seconds"` // default execution timeout
	MemoryLimitMB  int           `json:"memory_limit_mb"` // worker RAM limit
	CPULimit       float64       `json:"cpu_limit"`       // worker CPU cores limit
}

// StateInjection declares a persistent state file to read and inject as an env var before Run.
type StateInjection struct {
	StatePath string `json:"state_path"` // relative path inside action's state namespace, e.g. "html.json"
	EnvVar    string `json:"env_var"`    // target env var name, e.g. "PREVIOUS_HTML"
	Optional  bool   `json:"optional"`   // if true, empty string if missing; if false, fail run if missing
}

// ActionVersion is an immutable snapshot of source code and runtime specifications.
type ActionVersion struct {
	ID                  string           `json:"id"`
	ActionID            string           `json:"action_id"`
	VersionNumber       int              `json:"version_number"`
	SourceDigest        string           `json:"source_digest"` // SHA256 of source bundle
	SourcePath          string           `json:"source_path"`   // relative path to data dir
	BuildSpec           BuildSpec        `json:"build_spec"`
	RuntimeSpec         RuntimeSpec      `json:"runtime_spec"`
	StateInjections     []StateInjection `json:"state_injections"`
	RuntimeCapabilities []string         `json:"runtime_capabilities"` // e.g. "state.write", "frostagent.sendmsg"
	CreatedAt           time.Time        `json:"created_at"`
}
