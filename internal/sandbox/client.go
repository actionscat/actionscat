package sandbox

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
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
	UserUUID      string            `json:"user_uuid"`
	Profile       string            `json:"profile,omitempty"`
	Network       string            `json:"network,omitempty"`
	MemoryLimitMB int               `json:"memory_limit_mb,omitempty"`
	CPULimit      float64           `json:"cpu_limit,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
}

func (c *Client) CreateSession(ctx context.Context, req SessionRequest) (*SessionHandle, error) {
	if req.SessionID == "" {
		return nil, ErrInvalidRequest
	}
	userUUID := c.SessionIDToUUID(req.SessionID)

	// Verify reachability of the gateway
	if err := c.Health(ctx); err != nil {
		return nil, err
	}

	// Store session policy and settings locally
	c.mu.Lock()
	c.sessions[req.SessionID] = req
	c.mu.Unlock()

	// Proactively notify the gateway if it supports explicit session initialization
	initURL := c.baseURL + "/api/v1/sessions"
	initBody, err := json.Marshal(gatewaySessionInitRequest{
		UserUUID:      userUUID,
		Profile:       req.Profile,
		Network:       req.Network.Mode,
		MemoryLimitMB: req.MemoryLimitMB,
		CPULimit:      req.CPULimit,
		Env:           req.Env,
	})
	if err == nil {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, initURL, bytes.NewReader(initBody))
		if err == nil {
			httpReq.Header.Set("Content-Type", "application/json")
			httpReq.Header.Set("X-Auth-Token", c.authToken)
			if resp, err := c.httpClient.Do(httpReq); err == nil {
				_ = resp.Body.Close()
				// If 200/201/204 or 404 (endpoint not supported by gateway), proceed safely.
				// If 401/403/500+, report failure
				if resp.StatusCode != http.StatusOK &&
					resp.StatusCode != http.StatusCreated &&
					resp.StatusCode != http.StatusNoContent &&
					resp.StatusCode != http.StatusNotFound {
					return nil, fmt.Errorf("%w: gateway returned HTTP %d on session init", ErrSandboxUnavailable, resp.StatusCode)
				}
			}
		}
	}

	return &SessionHandle{
		SessionID: req.SessionID,
		UserUUID:  userUUID,
		Profile:   req.Profile,
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

	// Propagate profile, network, and resource policy if configured for this session
	c.mu.RLock()
	sessReq, hasSess := c.sessions[req.SessionID]
	c.mu.RUnlock()

	mergedEnv := make(map[string]string)
	if hasSess {
		if sessReq.Profile != "" {
			q.Set("profile", sessReq.Profile)
		}
		if sessReq.Network.Mode != "" {
			q.Set("network", sessReq.Network.Mode)
		}
		maps.Copy(mergedEnv, sessReq.Env)
	}
	maps.Copy(mergedEnv, req.Env)

	execURL.RawQuery = q.Encode()

	timeoutSec := req.Timeout.Seconds()
	if timeoutSec <= 0 {
		timeoutSec = 60.0
	}

	cwd := req.Cwd
	if cwd == "" {
		cwd = "/sandbox"
	}

	// If environment variables are provided, prepend them securely to command execution
	fullCmd := req.Command
	if len(mergedEnv) > 0 {
		var envBuilder strings.Builder
		for k, v := range mergedEnv {
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

// UploadFiles streams files into the sandbox worker filesystem using base64 shell unpacking.
func (c *Client) UploadFiles(ctx context.Context, sessionID string, files map[string][]byte) error {
	if len(files) == 0 {
		return nil
	}

	// Batch file unpacking using shell exec to ensure reliable file transmission without external S3
	var script strings.Builder
	for relPath, data := range files {
		encoded := base64.StdEncoding.EncodeToString(data)
		cleanRel := strings.TrimPrefix(relPath, "/")
		cleanRel = strings.TrimPrefix(cleanRel, "sandbox/")
		targetPath := "/sandbox/" + cleanRel
		targetDir := targetPath[:strings.LastIndex(targetPath, "/")]

		fmt.Fprintf(&script, "mkdir -p %q && base64 -d <<'EOF' > %q\n%s\nEOF\n", targetDir, targetPath, encoded)
	}

	res, err := c.Exec(ctx, ExecRequest{
		SessionID: sessionID,
		Command:   script.String(),
		Cwd:       "/sandbox",
		Timeout:   30 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("upload files failed: %w", err)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		return fmt.Errorf("upload files failed with exit code %v: %s", res.ExitCode, res.Stderr)
	}
	return nil
}

// ExportFiles reads files from the sandbox worker filesystem using base64 output.
func (c *Client) ExportFiles(ctx context.Context, sessionID string, paths []string) (map[string][]byte, error) {
	out := make(map[string][]byte)
	for _, p := range paths {
		cleanRel := strings.TrimPrefix(p, "/")
		cleanRel = strings.TrimPrefix(cleanRel, "sandbox/")

		// Check /sandbox/<path> as well as root /<path>
		cmd := fmt.Sprintf("base64 -w 0 %q 2>/dev/null || base64 -w 0 %q 2>/dev/null || true",
			"/sandbox/"+cleanRel, "/"+cleanRel)

		res, err := c.Exec(ctx, ExecRequest{
			SessionID: sessionID,
			Command:   cmd,
			Cwd:       "/sandbox",
			Timeout:   30 * time.Second,
		})
		if err != nil {
			return nil, fmt.Errorf("export file %q failed: %w", p, err)
		}
		rawB64 := strings.TrimSpace(res.Stdout)
		if rawB64 == "" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(rawB64)
		if err != nil {
			return nil, fmt.Errorf("decode exported file %q: %w", p, err)
		}
		out[p] = data
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
