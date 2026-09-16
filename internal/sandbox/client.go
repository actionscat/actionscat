package sandbox

import (
	"actionscat/internal/domain"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var defaultProjectNamespace = [16]byte{
	0x9b, 0x98, 0x6a, 0x76, 0x6c, 0x17, 0x48, 0xf8,
	0xb3, 0xd4, 0x72, 0x2a, 0x46, 0x69, 0xf9, 0x39,
}

// Config holds connection parameters for the code-interpreter gateway.
type Config struct {
	BaseURL          string
	AuthToken        string
	SessionNamespace string
	ClientTimeout    time.Duration
}

// Client implements Backend by communicating with code-interpreter Gateway over HTTP.
type Client struct {
	baseURL          string
	authToken        string
	sessionNamespace string
	httpClient       *http.Client
	projectNamespace [16]byte
	mu               sync.RWMutex
	sessions         map[string]SessionRequest
}

func NewClient(cfg Config) *Client {
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	timeout := cfg.ClientTimeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	return &Client{
		baseURL:          baseURL,
		authToken:        cfg.AuthToken,
		sessionNamespace: cfg.SessionNamespace,
		httpClient:       &http.Client{Timeout: timeout},
		projectNamespace: defaultProjectNamespace,
		sessions:         make(map[string]SessionRequest),
	}
}

// SessionIDToUUID derives a deterministic RFC4122 v5 UUID from the session ID.
func (c *Client) SessionIDToUUID(sessionID string) string {
	h := sha1.New()
	h.Write(c.projectNamespace[:])
	h.Write([]byte(c.sessionNamespace))
	h.Write([]byte{0x00})
	h.Write([]byte(sessionID))
	sum := h.Sum(nil)

	sum[6] = (sum[6] & 0x0f) | 0x50 // version 5
	sum[8] = (sum[8] & 0x3f) | 0x80 // variant RFC 4122

	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16],
	)
}

func (c *Client) Health(ctx context.Context) error {
	reqURL := c.baseURL + "/api/v1/status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("build health request: %w", err)
	}
	req.Header.Set("X-Auth-Token", c.authToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSandboxUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: status returned HTTP %d", ErrSandboxUnavailable, resp.StatusCode)
	}
	return nil
}

type gatewaySessionInitRequest struct {
	UserUUID           string                    `json:"user_uuid"`
	Profile            string                    `json:"profile,omitempty"`
	Network            string                    `json:"network,omitempty"`
	AllowedHosts       []string                  `json:"allowed_hosts,omitempty"`
	NetworkRules       []domain.NetworkAllowRule `json:"network_rules,omitempty"`
	MemoryLimitMB      int                       `json:"memory_limit_mb,omitempty"`
	CPULimit           float64                   `json:"cpu_limit,omitempty"`
	Env                map[string]string         `json:"env,omitempty"`
	RuntimeCallbackURL string                    `json:"runtime_callback_url,omitempty"`
}

func (c *Client) CreateSession(ctx context.Context, req SessionRequest) (*SessionHandle, error) {
	if err := ValidateSessionRequest(req); err != nil {
		return nil, err
	}
	userUUID := c.SessionIDToUUID(req.SessionID)

	// Verify reachability of the gateway
	if err := c.Health(ctx); err != nil {
		return nil, err
	}

	// Strictly enforce the code-interpreter contract fail-closed:
	// ActionsCat must not silently degrade into unconstrained sandbox workers if
	// the gateway does not implement or rejects the requested profile/session contract.
	initURL := c.baseURL + "/api/v1/sessions"

	var allowedHosts []string
	for _, rule := range req.Network.Allow {
		if rule.Port > 0 {
			allowedHosts = append(allowedHosts, fmt.Sprintf("%s:%d", rule.Host, rule.Port))
		} else if rule.Host != "" {
			allowedHosts = append(allowedHosts, rule.Host)
		}
	}

	initBody, err := json.Marshal(gatewaySessionInitRequest{
		UserUUID:           userUUID,
		Profile:            req.Profile,
		Network:            req.Network.Mode,
		AllowedHosts:       allowedHosts,
		NetworkRules:       req.Network.Allow,
		MemoryLimitMB:      req.MemoryLimitMB,
		CPULimit:           req.CPULimit,
		Env:                req.Env,
		RuntimeCallbackURL: req.RuntimeCallbackURL,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal session request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, initURL, bytes.NewReader(initBody))
	if err != nil {
		return nil, fmt.Errorf("build session init request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Auth-Token", c.authToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSandboxUnavailable, err)
	}
	defer resp.Body.Close()

	type gatewaySessionInitResponse struct {
		UserUUID           string   `json:"user_uuid"`
		Profile            string   `json:"profile"`
		Network            string   `json:"network"`
		Status             string   `json:"status"`
		AllowedHosts       []string `json:"allowed_hosts"`
		RuntimeCallbackURL string   `json:"runtime_callback_url"`
	}

	var initResp gatewaySessionInitResponse
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		// Gateway successfully provisioned and bound the session contract
		if err := json.NewDecoder(resp.Body).Decode(&initResp); err != nil {
			return nil, fmt.Errorf("%w: failed to decode gateway session init response: %v", ErrSandboxUnavailable, err)
		}
	case http.StatusNotFound:
		// Gateway does not support the explicit session/profile contract -> Fail Closed!
		return nil, fmt.Errorf("%w: gateway returned 404 on /api/v1/sessions", ErrProfileNotSupported)
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("%w: gateway rejected profile/network policy: %s", ErrProfileNotSupported, string(body))
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("%w: gateway returned HTTP %d on session init: %s", ErrSandboxUnavailable, resp.StatusCode, string(body))
	}

	// Contract conformance validation (Fail Closed)
	if initResp.UserUUID == "" || initResp.UserUUID != userUUID {
		return nil, fmt.Errorf("%w: gateway user_uuid mismatch (expected %q, got %q)", ErrProfileNotSupported, userUUID, initResp.UserUUID)
	}
	if req.Profile != "" && initResp.Profile != req.Profile {
		return nil, fmt.Errorf("%w: gateway profile mismatch (expected %q, got %q)", ErrProfileNotSupported, req.Profile, initResp.Profile)
	}
	if req.Network.Mode != "" && initResp.Network != string(req.Network.Mode) {
		return nil, fmt.Errorf("%w: gateway network mismatch (expected %q, got %q)", ErrProfileNotSupported, req.Network.Mode, initResp.Network)
	}
	if req.Network.Mode == domain.NetworkModeAllowlist {
		returnedHosts := make(map[string]bool, len(initResp.AllowedHosts))
		for _, h := range initResp.AllowedHosts {
			returnedHosts[h] = true
		}
		for _, h := range allowedHosts {
			if !returnedHosts[h] {
				return nil, fmt.Errorf("%w: gateway missing requested allowed host %q", ErrProfileNotSupported, h)
			}
		}
	}
	if initResp.Status != "ready" && initResp.Status != "created" {
		return nil, fmt.Errorf("%w: gateway session status not ready (%q)", ErrSandboxUnavailable, initResp.Status)
	}

	effectiveCallbackURL := req.RuntimeCallbackURL
	if initResp.RuntimeCallbackURL != "" {
		effectiveCallbackURL = initResp.RuntimeCallbackURL
	}

	storedReq := req
	storedReq.RuntimeCallbackURL = effectiveCallbackURL
	if storedReq.Env != nil && effectiveCallbackURL != "" {
		storedReq.Env["ACTIONSCAT_RUNTIME_ENDPOINT"] = effectiveCallbackURL
	}

	// Store verified session policy and settings locally
	c.mu.Lock()
	c.sessions[req.SessionID] = storedReq
	c.mu.Unlock()

	return &SessionHandle{
		SessionID:          req.SessionID,
		UserUUID:           userUUID,
		Profile:            req.Profile,
		RuntimeCallbackURL: effectiveCallbackURL,
	}, nil
}

type gatewayExecRequest struct {
	Command string  `json:"command"`
	Cwd     string  `json:"cwd"`
	Timeout float64 `json:"timeout"`
}

type gatewayExecResponse struct {
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	ExitCode        *int   `json:"exit_code"`
	TimedOut        bool   `json:"timed_out"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
	DurationMs      int64  `json:"duration_ms"`
}

func (c *Client) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.Command) == "" {
		return ExecResult{}, ErrInvalidRequest
	}
	userUUID := c.SessionIDToUUID(req.SessionID)

	execURL, err := url.Parse(c.baseURL + "/api/v1/shell/exec")
	if err != nil {
		return ExecResult{}, fmt.Errorf("invalid exec URL: %w", err)
	}
	q := execURL.Query()
	q.Set("user_uuid", userUUID)

	// Propagate profile and network policy if configured for this session
	c.mu.RLock()
	sessReq, hasSess := c.sessions[req.SessionID]
	c.mu.RUnlock()

	if hasSess {
		if sessReq.Profile != "" {
			q.Set("profile", sessReq.Profile)
		}
		if sessReq.Network.Mode != "" {
			q.Set("network", sessReq.Network.Mode)
		}
	}

	execURL.RawQuery = q.Encode()

	timeoutSec := req.Timeout.Seconds()
	if timeoutSec <= 0 {
		timeoutSec = 60.0
	} else if timeoutSec > 120.0 {
		timeoutSec = 120.0
	}

	cwd := req.Cwd
	if cwd == "" {
		cwd = "/sandbox"
	}

	// The session provisioning effective environment is the single source of truth.
	// Container environment was already configured during session initialization.
	// Avoid re-injecting stale session environment (which could clobber the container's
	// proxy URL with host advertised endpoints). Only apply explicit per-exec overrides if provided.
	fullCmd := req.Command
	if len(req.Env) > 0 {
		var envBuilder strings.Builder
		for k, v := range req.Env {
			// export key='escaped_val'
			escapedVal := strings.ReplaceAll(v, "'", `'\''`)
			fmt.Fprintf(&envBuilder, "export %s='%s'; ", k, escapedVal)
		}
		fullCmd = envBuilder.String() + fullCmd
	}

	bodyBytes, err := json.Marshal(gatewayExecRequest{
		Command: fullCmd,
		Cwd:     cwd,
		Timeout: timeoutSec,
	})
	if err != nil {
		return ExecResult{}, fmt.Errorf("marshal exec request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, execURL.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		return ExecResult{}, fmt.Errorf("build exec request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Auth-Token", c.authToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return ExecResult{}, fmt.Errorf("%w: exec transport error: %v", ErrSandboxUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return ExecResult{}, fmt.Errorf("%w: gateway returned HTTP %d: %s", ErrExecutionFailed, resp.StatusCode, string(errBody))
	}

	var execResp gatewayExecResponse
	if err := json.NewDecoder(resp.Body).Decode(&execResp); err != nil {
		return ExecResult{}, fmt.Errorf("decode exec response: %w", err)
	}

	return ExecResult{
		Stdout:          execResp.Stdout,
		Stderr:          execResp.Stderr,
		ExitCode:        execResp.ExitCode,
		TimedOut:        execResp.TimedOut,
		StdoutTruncated: execResp.StdoutTruncated,
		StderrTruncated: execResp.StderrTruncated,
		Duration:        time.Duration(execResp.DurationMs) * time.Millisecond,
	}, nil
}

// UploadFiles streams files into the sandbox worker filesystem using base64 shell unpacking with chunking.
func (c *Client) UploadFiles(ctx context.Context, sessionID string, files map[string][]byte) error {
	if len(files) == 0 {
		return nil
	}

	const chunkSize = 30000 // 30KB is a multiple of 3, producing ~40KB base64 (< 64KB command limit)

	for relPath, data := range files {
		cleanRel := strings.TrimPrefix(relPath, "/")
		cleanRel = strings.TrimPrefix(cleanRel, "sandbox/")
		targetPath := "/sandbox/" + cleanRel
		lastSlash := strings.LastIndex(targetPath, "/")
		targetDir := "/sandbox"
		if lastSlash > 0 {
			targetDir = targetPath[:lastSlash]
		}

		// Ensure target directory exists
		mkdirRes, err := c.Exec(ctx, ExecRequest{
			SessionID: sessionID,
			Command:   fmt.Sprintf("mkdir -p %q", targetDir),
			Cwd:       "/sandbox",
			Timeout:   15 * time.Second,
		})
		if err != nil {
			return fmt.Errorf("mkdir for %q failed: %w", targetDir, err)
		}
		if mkdirRes.ExitCode == nil || *mkdirRes.ExitCode != 0 {
			return fmt.Errorf("mkdir for %q failed: %s", targetDir, mkdirRes.Stderr)
		}

		if len(data) == 0 {
			_, err := c.Exec(ctx, ExecRequest{
				SessionID: sessionID,
				Command:   fmt.Sprintf(": > %q", targetPath),
				Cwd:       "/sandbox",
				Timeout:   15 * time.Second,
			})
			if err != nil {
				return fmt.Errorf("touch empty file %q failed: %w", targetPath, err)
			}
			continue
		}

		for offset := 0; offset < len(data); offset += chunkSize {
			end := min(offset+chunkSize, len(data))
			chunk := data[offset:end]
			encoded := base64.StdEncoding.EncodeToString(chunk)

			op := ">"
			if offset > 0 {
				op = ">>"
			}
			cmd := fmt.Sprintf("base64 -d <<'EOF' %s %q\n%s\nEOF\n", op, targetPath, encoded)

			res, err := c.Exec(ctx, ExecRequest{
				SessionID: sessionID,
				Command:   cmd,
				Cwd:       "/sandbox",
				Timeout:   30 * time.Second,
			})
			if err != nil {
				return fmt.Errorf("upload chunk for %q failed: %w", targetPath, err)
			}
			if res.ExitCode == nil || *res.ExitCode != 0 {
				return fmt.Errorf("upload chunk for %q failed with exit code %v: %s", targetPath, res.ExitCode, res.Stderr)
			}
		}
	}
	return nil
}

// ExportFiles reads files from the sandbox worker filesystem using base64 output with chunking.
func (c *Client) ExportFiles(ctx context.Context, sessionID string, paths []string) (map[string][]byte, error) {
	out := make(map[string][]byte)
	const exportChunkBytes = 524288 // 512 KB per chunk -> ~683 KB base64, safely within 1MB stream drain limit
	var totalExportedBytes int64

	for _, p := range paths {
		cleanRel := strings.TrimPrefix(p, "/")
		cleanRel = strings.TrimPrefix(cleanRel, "sandbox/")

		// Check /sandbox/<path> as well as root /<path>
		findCmd := fmt.Sprintf(
			"if [ -f \"/sandbox/%s\" ]; then echo \"/sandbox/%s\"; elif [ -f \"/%s\" ]; then echo \"/%s\"; fi",
			cleanRel, cleanRel, cleanRel, cleanRel,
		)
		findRes, err := c.Exec(ctx, ExecRequest{
			SessionID: sessionID,
			Command:   findCmd,
			Cwd:       "/sandbox",
			Timeout:   15 * time.Second,
		})
		if err != nil {
			return nil, fmt.Errorf("locate file %q failed: %w", p, err)
		}
		targetFile := strings.TrimSpace(findRes.Stdout)
		if targetFile == "" {
			continue
		}

		// Read in 512KB chunks using dd to prevent stdout truncation at 1MiB
		var fileBuf bytes.Buffer
		skip := 0
		for {
			cmd := fmt.Sprintf("dd if=%q bs=%d skip=%d count=1 2>/dev/null | base64 -w 0",
				targetFile, exportChunkBytes, skip)

			res, err := c.Exec(ctx, ExecRequest{
				SessionID: sessionID,
				Command:   cmd,
				Cwd:       "/sandbox",
				Timeout:   30 * time.Second,
			})
			if err != nil {
				return nil, fmt.Errorf("export chunk for %q failed: %w", p, err)
			}
			rawB64 := strings.TrimSpace(res.Stdout)
			if rawB64 == "" {
				break
			}
			chunk, err := base64.StdEncoding.DecodeString(rawB64)
			if err != nil {
				return nil, fmt.Errorf("decode chunk for %q: %w", p, err)
			}
			if len(chunk) == 0 {
				break
			}
			totalExportedBytes += int64(len(chunk))
			if totalExportedBytes > MaxArtifactTotalBytes {
				return nil, fmt.Errorf("%w: total artifact size exceeded %d bytes while exporting %q", ErrArtifactTooLarge, MaxArtifactTotalBytes, p)
			}
			fileBuf.Write(chunk)
			if len(chunk) < exportChunkBytes {
				break
			}
			skip++
		}
		out[p] = fileBuf.Bytes()
	}
	return out, nil
}

func (c *Client) Release(ctx context.Context, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return nil
	}
	userUUID := c.SessionIDToUUID(sessionID)

	c.mu.Lock()
	delete(c.sessions, sessionID)
	c.mu.Unlock()

	releaseURL, err := url.Parse(c.baseURL + "/api/v1/release")
	if err != nil {
		return fmt.Errorf("invalid release URL: %w", err)
	}
	q := releaseURL.Query()
	q.Set("user_uuid", userUUID)
	releaseURL.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, releaseURL.String(), nil)
	if err != nil {
		return fmt.Errorf("build release request: %w", err)
	}
	httpReq.Header.Set("X-Auth-Token", c.authToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("release request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil // idempotent release
	}

	errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return fmt.Errorf("release returned HTTP %d: %s", resp.StatusCode, string(errBody))
}

func (c *Client) ToolchainBaseline(ctx context.Context, profile string) (ToolchainInfo, error) {
	if profile == "" {
		profile = ProfileGoBuilder
	}

	// Execute "go version" in a temporary builder session using the specified profile
	tempSession := fmt.Sprintf("detect-toolchain-%d", time.Now().UnixNano())
	defer func() { _ = c.Release(context.Background(), tempSession) }()

	// Explicitly target the requested profile (e.g. go-builder)
	if _, err := c.CreateSession(ctx, SessionRequest{
		SessionID: tempSession,
		Profile:   profile,
	}); err != nil {
		return ToolchainInfo{}, fmt.Errorf("failed to create detection session for profile %q: %w", profile, err)
	}

	res, err := c.Exec(ctx, ExecRequest{
		SessionID: tempSession,
		Command:   "go version",
		Cwd:       "/sandbox",
		Timeout:   10 * time.Second,
	})
	if err != nil {
		return ToolchainInfo{}, fmt.Errorf("failed to query toolchain baseline: %w", err)
	}

	// Parse "go version go1.27.3 linux/amd64" -> "go1.27.3"
	output := strings.TrimSpace(res.Stdout)
	fields := strings.Fields(output)
	var ver string
	if len(fields) >= 3 && fields[0] == "go" && fields[1] == "version" {
		ver = fields[2]
	} else {
		ver = output
	}

	return ToolchainInfo{
		Language:        "go",
		BaselineVersion: ver,
		Profile:         profile,
	}, nil
}

var _ Backend = (*Client)(nil)
var _ Backend = (*FakeBackend)(nil)
