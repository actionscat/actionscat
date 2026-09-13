package runner

import (
	"actionscat/internal/domain"
	"actionscat/internal/sandbox"
	"actionscat/internal/store"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"maps"
	"sync"
	"time"
)

const (
	maxSingleEnvVarBytes = 1024 * 1024     // 1 MB limit per env var
	maxTotalEnvBytes     = 2 * 1024 * 1024 // 2 MB limit total env
	maxOutputBytes       = 1024 * 1024     // 1 MB limit for stdout/stderr
)

var (
	ErrNoActiveBuild   = errors.New("action has no active build")
	ErrActionDisabled  = errors.New("action is disabled")
	ErrEnvTooLarge     = errors.New("environment variable exceeds size limit")
	ErrMissingState    = errors.New("required state injection file does not exist")
)

type Config struct {
	MaxWorkers      int
	RuntimeEndpoint string // e.g. "http://127.0.0.1:7999/api/v1/runtime"
	PollInterval    time.Duration
}

type Runner struct {
	store           *store.SQLiteStore
	fileStore       *store.FileStore
	stateStore      *store.StateStore
	sandbox         sandbox.Backend
	runtimeEndpoint string
	maxWorkers      int
	pollInterval    time.Duration

	wakeup chan struct{}
	mu     sync.Mutex
	wg     sync.WaitGroup
	cancel context.CancelFunc
}

func NewRunner(
	store *store.SQLiteStore,
	fileStore *store.FileStore,
	stateStore *store.StateStore,
	sandbox sandbox.Backend,
	cfg Config,
) *Runner {
	workers := cfg.MaxWorkers
	if workers <= 0 {
		workers = 8
	}
	poll := cfg.PollInterval
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}

	return &Runner{
		store:           store,
		fileStore:       fileStore,
		stateStore:      stateStore,
		sandbox:         sandbox,
		runtimeEndpoint: cfg.RuntimeEndpoint,
		maxWorkers:      workers,
		pollInterval:    poll,
		wakeup:          make(chan struct{}, 1),
	}
}

func randomRunID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("run_%s", hex.EncodeToString(b))
}

type CreateRunRequest struct {
	ActionID        string
	TriggerType     domain.TriggerType
	TriggerMetadata map[string]string
	ExtraEnv        map[string]string // e.g. matcher regex captures
}

// PrepareRun validates the action and constructs an immutable domain.Run in queued status.
func (r *Runner) PrepareRun(ctx context.Context, req CreateRunRequest) (*domain.Run, error) {
	// 1. Fetch and validate Action
	act, err := r.store.GetAction(ctx, req.ActionID)
	if err != nil {
		return nil, fmt.Errorf("fetch action: %w", err)
	}
	if !act.Enabled {
		return nil, ErrActionDisabled
	}
	if act.ActiveVersionID == "" || act.ActiveBuildID == "" {
		return nil, ErrNoActiveBuild
	}

	// 2. Fetch Version & Build
	ver, err := r.store.GetVersion(ctx, act.ActiveVersionID)
	if err != nil {
		return nil, fmt.Errorf("fetch active version: %w", err)
	}
	bld, err := r.store.GetBuild(ctx, act.ActiveBuildID)
	if err != nil {
		return nil, fmt.Errorf("fetch active build: %w", err)
	}
	if bld.Status != domain.BuildStatusSucceeded {
		return nil, fmt.Errorf("active build %s is not in succeeded status", bld.ID)
	}

	// 3. Construct Planned Environment
	plannedEnv := make(map[string]string)
	plannedEnv["ACTIONSCAT_ACTION_ID"] = act.ID
	plannedEnv["ACTIONSCAT_ACTION_VERSION"] = ver.ID
	plannedEnv["ACTIONSCAT_BUILD_ID"] = bld.ID
	plannedEnv["ACTIONSCAT_TRIGGER_TYPE"] = string(req.TriggerType)

	// Extra env (e.g. Matcher captures)
	for k, v := range req.ExtraEnv {
		if len(v) > maxSingleEnvVarBytes {
			return nil, fmt.Errorf("%w: variable %q exceeds 1MB limit", ErrEnvTooLarge, k)
		}
		plannedEnv[k] = v
	}

	// State Injections (read from Core's StateStore)
	for _, inj := range ver.StateInjections {
		stateData, err := r.stateStore.ReadState(act.ID, inj.StatePath)
		if err != nil {
			if errors.Is(err, store.ErrPathTraversal) || errors.Is(err, store.ErrInvalidPath) {
				return nil, fmt.Errorf("invalid state injection path: %w", err)
			}
			if !inj.Optional {
				return nil, fmt.Errorf("%w: %s (%v)", ErrMissingState, inj.StatePath, err)
			}
			plannedEnv[inj.EnvVar] = ""
		} else {
			if len(stateData) > maxSingleEnvVarBytes {
				return nil, fmt.Errorf("%w: state injection %q exceeds 1MB limit", ErrEnvTooLarge, inj.StatePath)
			}
			plannedEnv[inj.EnvVar] = string(stateData)
		}
	}

	// Total environment size validation
	var totalEnvBytes int
	for k, v := range plannedEnv {
		totalEnvBytes += len(k) + len(v)
	}
	if totalEnvBytes > maxTotalEnvBytes {
		return nil, fmt.Errorf("%w: total env size exceeds 2MB limit", ErrEnvTooLarge)
	}

	// 4. Construct Run Record
	runID := randomRunID()
	now := time.Now().UTC()
	run := &domain.Run{
		ID:              runID,
		ActionID:        act.ID,
		ActionVersionID: ver.ID,
		ArtifactBuildID: bld.ID,
		TriggerType:     req.TriggerType,
		TriggerMetadata: req.TriggerMetadata,
		PlannedEnv:      plannedEnv,
		Status:          domain.RunStatusQueued,
		CreatedAt:       now,
	}

	return run, nil
}

// CreateRun prepares and persists an immutable Run in queued status.
func (r *Runner) CreateRun(ctx context.Context, req CreateRunRequest) (*domain.Run, error) {
	run, err := r.PrepareRun(ctx, req)
	if err != nil {
		return nil, err
	}

	if err := r.store.CreateRun(ctx, run); err != nil {
		return nil, fmt.Errorf("create run in store: %w", err)
	}

	// Wake up workers
	r.TriggerWakeup()
	return run, nil
}

func (r *Runner) TriggerWakeup() {
	select {
	case r.wakeup <- struct{}{}:
	default:
	}
}

// Start launches the bounded worker pool and queue processor.
func (r *Runner) Start(ctx context.Context) {
	r.mu.Lock()
	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.mu.Unlock()

	// Launch worker pool
	for i := 0; i < r.maxWorkers; i++ {
		r.wg.Add(1)
		go r.workerLoop(runCtx, i)
	}
}

func (r *Runner) Stop() {
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *Runner) workerLoop(ctx context.Context, _ int) {
	defer r.wg.Done()

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wakeup:
			r.drainQueue(ctx)
		case <-ticker.C:
			r.drainQueue(ctx)
		}
	}
}

func (r *Runner) drainQueue(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		claimed, err := r.store.ClaimNextQueuedRun(ctx)
		if err != nil {
			log.Printf("[runner] claim error: %v", err)
			return
		}
		if claimed == nil {
			return // No eligible queued runs at the moment
		}

		r.executeRun(ctx, claimed)
	}
}

func (r *Runner) executeRun(ctx context.Context, run *domain.Run) {
	startTime := time.Now().UTC()

	// 1. Fetch Action, Version and Build for this claimed run
	ver, err := r.store.GetVersion(ctx, run.ActionVersionID)
	if err != nil {
		r.finishRun(ctx, run.ID, domain.RunStatusFailed, nil, "", "failed to load action version: "+err.Error(), startTime)
		return
	}

	// 2. Generate 256-bit cryptographically secure Run Capability Token
	rawToken, hash, err := domain.GenerateRawToken()
	if err != nil {
		r.finishRun(ctx, run.ID, domain.RunStatusFailed, nil, "", "token generation failed: "+err.Error(), startTime)
		return
	}

	timeoutSec := ver.RuntimeSpec.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	runTimeout := time.Duration(timeoutSec) * time.Second

	// Run token record (expires after runTimeout + buffer)
	tokenRecord := &domain.RunToken{
		ID:        fmt.Sprintf("tok_%s", run.ID),
		TokenHash: hash,
		RunID:     run.ID,
		ActionID:  run.ActionID,
		Scopes:    ver.RuntimeCapabilities,
		ExpiresAt: startTime.Add(runTimeout + 5*time.Minute),
		CreatedAt: startTime,
	}
	if err := r.store.CreateRunToken(ctx, tokenRecord); err != nil {
		r.finishRun(ctx, run.ID, domain.RunStatusFailed, nil, "", "token storage failed: "+err.Error(), startTime)
		return
	}

	// TOKEN INVARIANT: Revoke token immediately upon run completion
	defer func() {
		_ = r.store.RevokeTokensForRun(context.Background(), run.ID, time.Now().UTC())
	}()

	// 3. Prepare Runtime Sandbox Session
	sessionID := fmt.Sprintf("run-%s", run.ID)
	defer func() {
		_ = r.sandbox.Release(context.Background(), sessionID)
	}()

	// Assemble complete sandbox environment
	sandboxEnv := make(map[string]string, len(run.PlannedEnv)+3)
	maps.Copy(sandboxEnv, run.PlannedEnv)
	sandboxEnv["ACTIONSCAT_RUN_ID"] = run.ID
	sandboxEnv["ACTIONSCAT_RUNTIME_ENDPOINT"] = r.runtimeEndpoint
	sandboxEnv["ACTIONSCAT_RUNTIME_TOKEN"] = rawToken

	_, err = r.sandbox.CreateSession(ctx, sandbox.SessionRequest{
		SessionID:     sessionID,
		Profile:       sandbox.ProfileRuntime,
		Network:       ver.RuntimeSpec.Network,
		MemoryLimitMB: ver.RuntimeSpec.MemoryLimitMB,
		CPULimit:      ver.RuntimeSpec.CPULimit,
		Env:           sandboxEnv,
	})
	if err != nil {
		r.finishRun(ctx, run.ID, domain.RunStatusFailed, nil, "", "sandbox provision failed: "+err.Error(), startTime)
		return
	}

	// 4. Read Artifact Bundle from FileStore and upload into Sandbox
	artifactFiles, err := r.fileStore.ReadArtifactBundle(run.ActionID, run.ActionVersionID, run.ArtifactBuildID)
	if err != nil || len(artifactFiles) == 0 {
		r.finishRun(ctx, run.ID, domain.RunStatusFailed, nil, "", "failed to read artifact bundle: "+err.Error(), startTime)
		return
	}

	if err := r.sandbox.UploadFiles(ctx, sessionID, artifactFiles); err != nil {
		r.finishRun(ctx, run.ID, domain.RunStatusFailed, nil, "", "failed to upload artifact to sandbox: "+err.Error(), startTime)
		return
	}

	// 5. Execute Entrypoint inside Sandbox
	entrypoint := ver.RuntimeSpec.Entrypoint
	if entrypoint == "" {
		entrypoint = "entrypoint"
	}
	cmd := fmt.Sprintf("chmod +x /sandbox/%s 2>/dev/null; /sandbox/%s", entrypoint, entrypoint)

	execRes, err := r.sandbox.Exec(ctx, sandbox.ExecRequest{
		SessionID: sessionID,
		Command:   cmd,
		Cwd:       "/sandbox",
		Timeout:   runTimeout,
		Env:       sandboxEnv,
	})

	if err != nil {
		r.finishRun(ctx, run.ID, domain.RunStatusFailed, nil, "", "sandbox execution error: "+err.Error(), startTime)
		return
	}

	// 6. Determine final status and record execution results
	status := domain.RunStatusSucceeded
	var errMsg string
	if execRes.TimedOut {
		status = domain.RunStatusTimedOut
		errMsg = "execution timed out"
	} else if execRes.ExitCode != nil && *execRes.ExitCode != 0 {
		status = domain.RunStatusFailed
		errMsg = fmt.Sprintf("process exited with status %d", *execRes.ExitCode)
	}

	stdout := truncateOutput(execRes.Stdout, maxOutputBytes)
	stderr := truncateOutput(execRes.Stderr, maxOutputBytes)

	r.finishRun(ctx, run.ID, status, execRes.ExitCode, stdout, stderr+errMsg, startTime)
}

func (r *Runner) finishRun(
	ctx context.Context,
	runID string,
	status domain.RunStatus,
	exitCode *int,
	stdout string,
	stderr string,
	startTime time.Time,
) {
	completedAt := time.Now().UTC()
	durationMs := completedAt.Sub(startTime).Milliseconds()

	_ = r.store.UpdateRunStatus(
		ctx, runID, status, exitCode, stdout, stderr, durationMs, stderr, &completedAt,
	)
}

func truncateOutput(s string, limit int) string {
	if len(s) > limit {
		return s[:limit] + "\n[OUTPUT TRUNCATED]"
	}
	return s
}
