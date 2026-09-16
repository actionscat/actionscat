package build

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

type Builder struct {
	store     *store.SQLiteStore
	fileStore *store.FileStore
	sandbox   sandbox.Backend
}

func NewBuilder(store *store.SQLiteStore, fileStore *store.FileStore, sandbox sandbox.Backend) *Builder {
	return &Builder{
		store:     store,
		fileStore: fileStore,
		sandbox:   sandbox,
	}
}

func randomBuildID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("bld_%s", hex.EncodeToString(b))
}

func (b *Builder) BuildVersion(ctx context.Context, actionID, versionID string) (*domain.ArtifactBuild, error) {
	// 1. Fetch Version
	ver, err := b.store.GetVersion(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch version %s: %w", versionID, err)
	}
	if ver.ActionID != actionID {
		return nil, fmt.Errorf("version %s does not belong to action %s", versionID, actionID)
	}

	// 2. Read source bundle
	sourceFiles, err := b.fileStore.ReadSourceBundle(actionID, versionID)
	if err != nil {
		return nil, fmt.Errorf("failed to read source files: %w", err)
	}
	if len(sourceFiles) == 0 {
		return nil, fmt.Errorf("source bundle for version %s is empty", versionID)
	}

	buildNum, err := b.store.GetNextBuildNumber(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get next build number: %w", err)
	}

	buildID := randomBuildID()
	now := time.Now().UTC()

	// Initial build record
	buildRecord := &domain.ArtifactBuild{
		ID:             buildID,
		ActionID:       actionID,
		VersionID:      versionID,
		BuildNumber:    buildNum,
		Status:         domain.BuildStatusBuilding,
		BuilderProfile: sandbox.ProfileGoBuilder,
		BuildCommand:   ver.BuildSpec.Command,
		StartedAt:      &now,
		CreatedAt:      now,
	}
	if err := b.store.CreateBuild(ctx, buildRecord); err != nil {
		return nil, fmt.Errorf("failed to create build record: %w", err)
	}

	// 3. Prepare Builder Sandbox Session
	sessionID := fmt.Sprintf("build-%s-%s-%d", actionID, versionID, buildNum)
	defer func() {
		_ = b.sandbox.Release(context.Background(), sessionID)
	}()

	netMode := "none"
	if ver.BuildSpec.Network {
		netMode = "public"
	}

	_, err = b.sandbox.CreateSession(ctx, sandbox.SessionRequest{
		SessionID: sessionID,
		Profile:   sandbox.ProfileGoBuilder,
		Network:   domain.NetworkPolicy{Mode: netMode},
		// Builder environment receives ZERO runtime capability tokens, NO action state, NO frostagent credentials!
		Env: nil,
	})
	if err != nil {
		completedAt := time.Now().UTC()
		_ = b.store.UpdateBuildResult(ctx, buildID, domain.BuildStatusFailed, "", "", err.Error(), nil, "", "", 0, completedAt)
		buildRecord.Status = domain.BuildStatusFailed
		buildRecord.Stderr = err.Error()
		return buildRecord, fmt.Errorf("failed to create builder sandbox: %w", err)
	}

	// 4. Inject SDK and upload source files into sandbox
	sourceFiles = InjectSDK(sourceFiles)
	if err := b.sandbox.UploadFiles(ctx, sessionID, sourceFiles); err != nil {
		completedAt := time.Now().UTC()
		_ = b.store.UpdateBuildResult(ctx, buildID, domain.BuildStatusFailed, "", "", err.Error(), nil, "", "", 0, completedAt)
		buildRecord.Status = domain.BuildStatusFailed
		buildRecord.Stderr = err.Error()
		return buildRecord, fmt.Errorf("failed to upload sources: %w", err)
	}

	// 5. Query actual Go toolchain version in builder sandbox
	verRes, _ := b.sandbox.Exec(ctx, sandbox.ExecRequest{
		SessionID: sessionID,
		Command:   "go version",
		Timeout:   10 * time.Second,
	})
	toolchainVersion := extractGoVersion(verRes.Stdout)
	buildRecord.ToolchainVersion = toolchainVersion

	// 6. Execute Build Spec Command
	// Ensure /sandbox/out exists; canonical artifact target is /sandbox/out/entrypoint
	cmd := fmt.Sprintf("mkdir -p /sandbox/out && cd /sandbox && %s", ver.BuildSpec.Command)
	buildTimeout := 120 * time.Second

	execRes, err := b.sandbox.Exec(ctx, sandbox.ExecRequest{
		SessionID: sessionID,
		Command:   cmd,
		Cwd:       "/sandbox",
		Timeout:   buildTimeout,
	})

	completedAt := time.Now().UTC()
	buildRecord.Stdout = execRes.Stdout
	buildRecord.Stderr = execRes.Stderr
	if err != nil && buildRecord.Stderr == "" {
		buildRecord.Stderr = err.Error()
	}
	buildRecord.ExitCode = execRes.ExitCode
	buildRecord.CompletedAt = &completedAt

	if err != nil || execRes.TimedOut || (execRes.ExitCode != nil && *execRes.ExitCode != 0) {
		status := domain.BuildStatusFailed
		if execRes.TimedOut {
			status = domain.BuildStatusTimedOut
		}
		buildRecord.Status = status
		_ = b.store.UpdateBuildResult(
			ctx, buildID, status, toolchainVersion,
			execRes.Stdout, execRes.Stderr, execRes.ExitCode,
			"", "", 0, completedAt,
		)
		return buildRecord, nil
	}

	// 7. Export built artifact files from /sandbox/out or /sandbox using ordered candidate fallback
	candidateFallbacks := []string{
		"/sandbox/out/entrypoint",
		"out/entrypoint",
		"entrypoint",
	}
	var exported map[string][]byte
	var lastExportErr error
	for _, candidate := range candidateFallbacks {
		exp, err := b.sandbox.ExportFiles(ctx, sessionID, []string{candidate})
		if err != nil {
			if errors.Is(err, sandbox.ErrArtifactTooLarge) {
				lastExportErr = err
				break
			}
			lastExportErr = err
			continue
		}
		if len(exp) > 0 {
			exported = exp
			lastExportErr = nil
			break
		}
	}

	if lastExportErr != nil {
		status := domain.BuildStatusFailed
		buildRecord.Status = status
		buildRecord.Stderr += fmt.Sprintf("\nfailed to export artifact: %v", lastExportErr)
		_ = b.store.UpdateBuildResult(
			ctx, buildID, status, toolchainVersion,
			execRes.Stdout, buildRecord.Stderr, execRes.ExitCode,
			"", "", 0, completedAt,
		)
		return buildRecord, fmt.Errorf("failed to export artifact: %w", lastExportErr)
	}
	if len(exported) == 0 {
		status := domain.BuildStatusFailed
		buildRecord.Status = status
		errMsg := "build finished but no entrypoint artifact was generated"
		buildRecord.Stderr += "\n" + errMsg
		_ = b.store.UpdateBuildResult(
			ctx, buildID, status, toolchainVersion,
			execRes.Stdout, buildRecord.Stderr, execRes.ExitCode,
			"", "", 0, completedAt,
		)
		return buildRecord, nil
	}

	// Normalize artifact file mapping so entrypoint is executable
	artifactFiles := make(map[string][]byte)
	for path, data := range exported {
		cleanName := path[strings.LastIndex(path, "/")+1:]
		artifactFiles[cleanName] = data
	}

	// 8. Save artifact bundle as immutable in FileStore
	digest, relPath, size, err := b.fileStore.SaveArtifactBundle(actionID, versionID, buildID, artifactFiles)
	if err != nil {
		status := domain.BuildStatusFailed
		buildRecord.Status = status
		buildRecord.Stderr += "\nfailed to save artifact: " + err.Error()
		_ = b.store.UpdateBuildResult(
			ctx, buildID, status, toolchainVersion,
			execRes.Stdout, buildRecord.Stderr, execRes.ExitCode,
			"", "", 0, completedAt,
		)
		return buildRecord, fmt.Errorf("failed to save artifact: %w", err)
	}

	// 9. Mark Build Succeeded
	buildRecord.Status = domain.BuildStatusSucceeded
	buildRecord.ArtifactDigest = digest
	buildRecord.ArtifactPath = relPath
	buildRecord.ArtifactSize = size

	if err := b.store.UpdateBuildResult(
		ctx, buildID, domain.BuildStatusSucceeded, toolchainVersion,
		execRes.Stdout, execRes.Stderr, execRes.ExitCode,
		digest, relPath, size, completedAt,
	); err != nil {
		return nil, fmt.Errorf("failed to update build result: %w", err)
	}

	return buildRecord, nil
}

func extractGoVersion(output string) string {
	fields := strings.Fields(strings.TrimSpace(output))
	if len(fields) >= 3 && fields[0] == "go" && fields[1] == "version" {
		return fields[2]
	}
	return strings.TrimSpace(output)
}
