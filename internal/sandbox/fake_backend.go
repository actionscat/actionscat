package sandbox

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"
)

type FakeSession struct {
	Req     SessionRequest
	Files   map[string][]byte
	ExecLog []ExecRequest
}

// FakeBackend provides a deterministic in-memory sandbox backend for testing.
type FakeBackend struct {
	mu                sync.RWMutex
	sessions          map[string]*FakeSession
	toolchainBaseline map[string]string
	Unavailable       bool // when true, Health() and Exec() fail (simulating outage)

	// CustomExec can be overridden by specific tests to simulate custom build/run behaviors.
	CustomExec func(req ExecRequest, session *FakeSession) (ExecResult, error)
}

func NewFakeBackend() *FakeBackend {
	return &FakeBackend{
		sessions: make(map[string]*FakeSession),
		toolchainBaseline: map[string]string{
			ProfileGoBuilder: "go1.27.3",
			ProfileRuntime:   "action-runtime-v1",
		},
	}
}

func (f *FakeBackend) SetBaseline(profile, version string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.toolchainBaseline[profile] = version
}

func (f *FakeBackend) CreateSession(ctx context.Context, req SessionRequest) (*SessionHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.Unavailable {
		return nil, ErrSandboxUnavailable
	}
	if req.SessionID == "" {
		return nil, ErrInvalidRequest
	}

	f.sessions[req.SessionID] = &FakeSession{
		Req:     req,
		Files:   make(map[string][]byte),
		ExecLog: nil,
	}

	return &SessionHandle{
		SessionID: req.SessionID,
		UserUUID:  "fake-uuid-" + req.SessionID,
		Profile:   req.Profile,
	}, nil
}

func (f *FakeBackend) UploadFiles(ctx context.Context, sessionID string, files map[string][]byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.Unavailable {
		return ErrSandboxUnavailable
	}
	sess, exists := f.sessions[sessionID]
	if !exists {
		return ErrSessionNotFound
	}

	maps.Copy(sess.Files, files)
	return nil
}

func (f *FakeBackend) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	f.mu.Lock()
	if f.Unavailable {
		f.mu.Unlock()
		return ExecResult{}, ErrSandboxUnavailable
	}
	sess, exists := f.sessions[req.SessionID]
	if !exists {
		f.mu.Unlock()
		return ExecResult{}, ErrSessionNotFound
	}
	sess.ExecLog = append(sess.ExecLog, req)
	custom := f.CustomExec
	f.mu.Unlock()

	if custom != nil {
		f.mu.Lock()
		res, err := custom(req, sess)
		f.mu.Unlock()
		return res, err
	}

	// Default smart mock behavior for Go builder & runtime
	zero := 0
	cmd := req.Command

	// 1. Toolchain version inspection
	if strings.Contains(cmd, "go version") {
		f.mu.RLock()
		baseVer := f.toolchainBaseline[ProfileGoBuilder]
		f.mu.RUnlock()
		return ExecResult{
			Stdout:   fmt.Sprintf("go version %s linux/amd64\n", baseVer),
			ExitCode: &zero,
			Duration: 5 * time.Millisecond,
		}, nil
	}

	// 2. Go build simulation
	if strings.Contains(cmd, "go build") {
		f.mu.Lock()
		// Mock producing an entrypoint executable
		sess.Files["entrypoint"] = []byte("\x7fELFfake-compiled-binary")
		f.mu.Unlock()
		return ExecResult{
			Stdout:   "Build succeeded\n",
			ExitCode: &zero,
			Duration: 50 * time.Millisecond,
		}, nil
	}

	// 3. Runtime entrypoint execution
	return ExecResult{
		Stdout:   "Action executed successfully\n",
		ExitCode: &zero,
		Duration: 20 * time.Millisecond,
	}, nil
}

func (f *FakeBackend) ExportFiles(ctx context.Context, sessionID string, paths []string) (map[string][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.Unavailable {
		return nil, ErrSandboxUnavailable
	}
	sess, exists := f.sessions[sessionID]
	if !exists {
		return nil, ErrSessionNotFound
	}

	out := make(map[string][]byte)
	for _, p := range paths {
		if data, ok := sess.Files[p]; ok {
			out[p] = data
		} else if data, ok := sess.Files[p[strings.LastIndex(p, "/")+1:]]; ok {
			out[p] = data
		}
	}
	// If paths requested was empty, export all files
	if len(paths) == 0 {
		maps.Copy(out, sess.Files)
	}

	// Calculate total unique size so duplicate path aliases do not inflate byte count
	seenFiles := make(map[string]bool)
	var totalSize int64
	for path, data := range out {
		cleanName := path[strings.LastIndex(path, "/")+1:]
		if !seenFiles[cleanName] {
			seenFiles[cleanName] = true
			totalSize += int64(len(data))
		}
	}
	if totalSize > MaxArtifactTotalBytes {
		return nil, fmt.Errorf("%w: total artifact size %d exceeds limit of %d bytes", ErrArtifactTooLarge, totalSize, MaxArtifactTotalBytes)
	}
	return out, nil
}

func (f *FakeBackend) Release(ctx context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.sessions, sessionID)
	return nil
}

func (f *FakeBackend) Health(ctx context.Context) error {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if f.Unavailable {
		return ErrSandboxUnavailable
	}
	return nil
}

func (f *FakeBackend) ToolchainBaseline(ctx context.Context, profile string) (ToolchainInfo, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if f.Unavailable {
		return ToolchainInfo{}, ErrSandboxUnavailable
	}
	ver, ok := f.toolchainBaseline[profile]
	if !ok {
		ver = "unknown"
	}
	return ToolchainInfo{
		Language:        "go",
		BaselineVersion: ver,
		Profile:         profile,
	}, nil
}
