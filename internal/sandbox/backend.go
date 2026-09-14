package sandbox

import (
	"actionscat/internal/domain"
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrSessionNotFound     = errors.New("sandbox session not found")
	ErrSandboxUnavailable  = errors.New("sandbox infrastructure unavailable")
	ErrExecutionFailed     = errors.New("sandbox execution failed")
	ErrInvalidRequest      = errors.New("invalid sandbox request")
	ErrProfileNotSupported = errors.New("sandbox backend does not support requested profile/session contract")
)

const (
	ProfileGoBuilder = "go-builder"
	ProfileRuntime   = "action-runtime"
	ProfileMinimal   = "minimal"
)

// SessionRequest defines parameters to provision or configure an isolated sandbox session.
type SessionRequest struct {
	SessionID          string
	Profile            string               // "go-builder", "action-runtime", "minimal"
	Network            domain.NetworkPolicy // "none", "public", "allowlist", "isolated"
	MemoryLimitMB      int
	CPULimit           float64
	Env                map[string]string
	RuntimeCallbackURL string // Host-accessible runtime callback URL for sandboxed containers
}

// ValidateSessionRequest validates profile and network requirements before provisioning.
func ValidateSessionRequest(req SessionRequest) error {
	if req.SessionID == "" {
		return ErrInvalidRequest
	}
	switch req.Profile {
	case ProfileGoBuilder, ProfileRuntime, ProfileMinimal, "":
		// valid profile
	default:
		return ErrProfileNotSupported
	}
	switch req.Network.Mode {
	case domain.NetworkModeNone, domain.NetworkModePublic, domain.NetworkModeIsolated, "":
		// valid network mode
	case domain.NetworkModeAllowlist:
		if len(req.Network.Allow) == 0 {
			return fmt.Errorf("%w: allowlist network mode requires at least one allow rule", ErrInvalidRequest)
		}
	default:
		return ErrInvalidRequest
	}
	return nil
}

// SessionHandle identifies an active sandbox session.
type SessionHandle struct {
	SessionID string
	UserUUID  string
	Profile   string
}

// ExecRequest specifies the execution parameters for a command inside the sandbox.
type ExecRequest struct {
	SessionID string
	Command   string
	Cwd       string // e.g. "/sandbox" or relative path
	Timeout   time.Duration
	Env       map[string]string
}

// ExecResult contains the structured result of a command execution inside the sandbox.
type ExecResult struct {
	Stdout          string
	Stderr          string
	ExitCode        *int
	TimedOut        bool
	StdoutTruncated bool
	StderrTruncated bool
	Duration        time.Duration
}

// ToolchainInfo describes the toolchain version baseline for a language/profile.
type ToolchainInfo struct {
	Language        string `json:"language"`
	BaselineVersion string `json:"baseline_version"` // e.g. "go1.27.3"
	Profile         string `json:"profile"`
}

// Backend defines the execution boundary for isolated sandboxes.
// Core code-execution MUST ONLY interact through this interface. Host execution fallback is strictly prohibited.
type Backend interface {
	// CreateSession prepares a session with the requested profile and network policies.
	CreateSession(ctx context.Context, req SessionRequest) (*SessionHandle, error)

	// UploadFiles uploads files into the sandbox session's filesystem (/sandbox/...).
	UploadFiles(ctx context.Context, sessionID string, files map[string][]byte) error

	// Exec executes a command inside the isolated sandbox for the given session.
	// Non-zero exit codes and timeouts return a valid ExecResult with nil error;
	// non-nil error indicates transport, authentication, or infrastructure failures.
	Exec(ctx context.Context, req ExecRequest) (ExecResult, error)

	// ExportFiles extracts files from the sandbox session's filesystem (/sandbox/...).
	ExportFiles(ctx context.Context, sessionID string, paths []string) (map[string][]byte, error)

	// Release terminates and cleans up the sandbox instance associated with the session.
	Release(ctx context.Context, sessionID string) error

	// Health checks whether the sandbox runtime infrastructure is reachable.
	Health(ctx context.Context) error

	// ToolchainBaseline returns the current baseline toolchain version for the profile.
	ToolchainBaseline(ctx context.Context, profile string) (ToolchainInfo, error)
}
