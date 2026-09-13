package action

import (
	"actionscat/internal/domain"
	"actionscat/internal/sandbox"
	"actionscat/internal/store"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrEmptyName       = errors.New("action name cannot be empty")
	ErrEmptySource     = errors.New("source bundle cannot be empty")
	ErrSourceTooLarge  = errors.New("source bundle exceeds maximum size (20MB)")
	ErrVersionNotFound = errors.New("action version not found")
	ErrBuildNotFound   = errors.New("artifact build not found")
	ErrBuildNotSuccess = errors.New("cannot activate build that did not succeed")
)

const maxSourceBundleBytes = 20 * 1024 * 1024 // 20 MB

type CreateActionRequest struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	MaxConcurrency int    `json:"max_concurrency"` // default 1
}

type CreateVersionRequest struct {
	Files               map[string][]byte       `json:"-"` // source bundle
	BuildSpec           domain.BuildSpec        `json:"build_spec"`
	RuntimeSpec         domain.RuntimeSpec      `json:"runtime_spec"`
	StateInjections     []domain.StateInjection `json:"state_injections"`
	RuntimeCapabilities []string                `json:"runtime_capabilities"`
}

type ToolchainCheckResult struct {
	ActionID           string `json:"action_id"`
	ActionName         string `json:"action_name"`
	ActiveVersionID    string `json:"active_version_id"`
	ActiveBuildID      string `json:"active_build_id"`
	LanguageReq        string `json:"language_requirement"`
	ActiveBuiltWith    string `json:"active_built_with"`
	CurrentBaseline    string `json:"current_baseline"`
	Outdated           bool   `json:"outdated"`
	RebuildRecommended bool   `json:"rebuild_recommended"`
	WarningMessage     string `json:"warning_message,omitempty"`
	LLMPrompt          string `json:"llm_prompt,omitempty"`
}

type Service struct {
	store     *store.SQLiteStore
	fileStore *store.FileStore
	sandbox   sandbox.Backend
}

func NewService(store *store.SQLiteStore, fileStore *store.FileStore, sandbox sandbox.Backend) *Service {
	return &Service{
		store:     store,
		fileStore: fileStore,
		sandbox:   sandbox,
	}
}

func randomID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b))
}

func (s *Service) CreateAction(ctx context.Context, req CreateActionRequest) (*domain.Action, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, ErrEmptyName
	}
	concurrency := req.MaxConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}

	now := time.Now().UTC()
	act := &domain.Action{
		ID:             randomID("act"),
		Name:           name,
		Description:    req.Description,
		MaxConcurrency: concurrency,
		Enabled:        true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := s.store.CreateAction(ctx, act); err != nil {
		return nil, err
	}
	return act, nil
}

func (s *Service) GetAction(ctx context.Context, actionID string) (*domain.Action, error) {
	return s.store.GetAction(ctx, actionID)
}

func (s *Service) ListActions(ctx context.Context) ([]*domain.Action, error) {
	return s.store.ListActions(ctx)
}

func (s *Service) CreateVersion(ctx context.Context, actionID string, req CreateVersionRequest) (*domain.ActionVersion, error) {
	if _, err := s.store.GetAction(ctx, actionID); err != nil {
		return nil, err
	}
	if len(req.Files) == 0 {
		return nil, ErrEmptySource
	}

	var totalSize int64
	for _, data := range req.Files {
		totalSize += int64(len(data))
	}
	if totalSize > maxSourceBundleBytes {
		return nil, ErrSourceTooLarge
	}

	verNum, err := s.store.GetNextVersionNumber(ctx, actionID)
	if err != nil {
		return nil, err
	}

	verID := fmt.Sprintf("ver_%06d", verNum)

	// Save source files into filesystem store
	digest, relPath, err := s.fileStore.SaveSourceBundle(actionID, verID, req.Files)
	if err != nil {
		return nil, fmt.Errorf("failed to save source bundle: %w", err)
	}

	// Set defaults
	if req.BuildSpec.Command == "" {
		req.BuildSpec.Command = "go build -o /out/entrypoint ."
	}
	if req.BuildSpec.Language == "" {
		req.BuildSpec.Language = "go"
	}
	if req.RuntimeSpec.Entrypoint == "" {
		req.RuntimeSpec.Entrypoint = "entrypoint"
	}
	if req.RuntimeSpec.TimeoutSeconds <= 0 {
		req.RuntimeSpec.TimeoutSeconds = 60
	}

	now := time.Now().UTC()
	v := &domain.ActionVersion{
		ID:                  verID,
		ActionID:            actionID,
		VersionNumber:       verNum,
		SourceDigest:        digest,
		SourcePath:          relPath,
		BuildSpec:           req.BuildSpec,
		RuntimeSpec:         req.RuntimeSpec,
		StateInjections:     req.StateInjections,
		RuntimeCapabilities: req.RuntimeCapabilities,
		CreatedAt:           now,
	}

	if err := s.store.CreateVersion(ctx, v); err != nil {
		return nil, err
	}
	return v, nil
}

func (s *Service) GetVersion(ctx context.Context, versionID string) (*domain.ActionVersion, error) {
	return s.store.GetVersion(ctx, versionID)
}

func (s *Service) ListVersions(ctx context.Context, actionID string) ([]*domain.ActionVersion, error) {
	return s.store.ListVersions(ctx, actionID)
}

func (s *Service) ActivateBuild(ctx context.Context, actionID, versionID, buildID string) error {
	build, err := s.store.GetBuild(ctx, buildID)
	if err != nil {
		return err
	}
	if build.ActionID != actionID || build.VersionID != versionID {
		return ErrBuildNotFound
	}
	if build.Status != domain.BuildStatusSucceeded {
		return ErrBuildNotSuccess
	}

	return s.store.SetActiveBuild(ctx, actionID, versionID, buildID)
}

func (s *Service) CheckToolchainOutdated(ctx context.Context, actionID string) (ToolchainCheckResult, error) {
	act, err := s.store.GetAction(ctx, actionID)
	if err != nil {
		return ToolchainCheckResult{}, err
	}

	result := ToolchainCheckResult{
		ActionID:   act.ID,
		ActionName: act.Name,
	}

	if act.ActiveBuildID == "" || act.ActiveVersionID == "" {
		return result, nil
	}

	result.ActiveVersionID = act.ActiveVersionID
	result.ActiveBuildID = act.ActiveBuildID

	ver, err := s.store.GetVersion(ctx, act.ActiveVersionID)
	if err == nil {
		result.LanguageReq = ver.BuildSpec.ToolchainRequirement
	}

	bld, err := s.store.GetBuild(ctx, act.ActiveBuildID)
	if err != nil {
		return result, nil
	}
	result.ActiveBuiltWith = bld.ToolchainVersion

	baselineInfo, err := s.sandbox.ToolchainBaseline(ctx, sandbox.ProfileGoBuilder)
	if err != nil {
		return result, nil
	}
	result.CurrentBaseline = baselineInfo.BaselineVersion

	// Compare active built_with against current baseline
	if result.ActiveBuiltWith != "" && result.CurrentBaseline != "" && result.ActiveBuiltWith != result.CurrentBaseline {
		result.Outdated = true
		result.RebuildRecommended = true
		result.WarningMessage = fmt.Sprintf(
			"检测到目前 code-interpreter 的 Go Toolchain 基线为 %s，当前 Artifact 使用 %s 构建，请考虑重建。",
			result.CurrentBaseline, result.ActiveBuiltWith,
		)
		result.LLMPrompt = fmt.Sprintf(
			"Action %q 当前 Artifact 构建于 %s，而当前系统基线已升级至 %s。请检查新工具链兼容性并重新构建，不要无意义升级 go.mod 最低版本要求。",
			act.Name, result.ActiveBuiltWith, result.CurrentBaseline,
		)
	}

	return result, nil
}
