package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DefaultEnvContent is the template written to .env when none exists.
const DefaultEnvContent = `# ==============================================================================
# ActionsCat Core Configuration
# ==============================================================================

# Server HTTP Listen Address
ACTIONSCAT_ADDR=:7999

# Directory for persistent data (database, artifacts, state)
ACTIONSCAT_DATA_DIR=./data

# Path to the SQLite database file (defaults to {ACTIONSCAT_DATA_DIR}/actionscat.db)
ACTIONSCAT_DB_PATH=./data/actionscat.db

# Number of concurrent Action runner workers
ACTIONSCAT_RUNNER_WORKERS=8

# Management API Bearer Token (optional; protects /api/v1/actions, /builds, /runs, /schedules, /matchers)
# ACTIONSCAT_MANAGEMENT_TOKEN=

# Event Dispatch Ingress Token (optional; protects /api/v1/dispatch)
# ACTIONSCAT_DISPATCH_TOKEN=

# ==============================================================================
# Runtime & Callback Configuration
# ==============================================================================

# Internal callback endpoint for ActionsCat runtime state writes and proxy requests
ACTIONSCAT_RUNTIME_ENDPOINT=http://127.0.0.1:7999/api/v1/runtime

# Advertised callback endpoint reachable from inside the sandbox (e.g. Docker container)
# When running sandboxes in Docker, set this to http://host.docker.internal:7999/api/v1/runtime
# ACTIONSCAT_RUNTIME_ADVERTISED_ENDPOINT=http://host.docker.internal:7999/api/v1/runtime

# ==============================================================================
# Sandbox Backend (Gateway / Code Interpreter)
# ==============================================================================

# FrostAgent Sandbox Gateway / Code Interpreter HTTP endpoint
FA_SANDBOX_ENDPOINT=http://127.0.0.1:3874

# API Key / Auth Token for authenticating with the sandbox gateway (optional)
# FA_SANDBOX_API_KEY=

# ==============================================================================
# FrostAgent Integration (Trusted LLM & Messaging Gateway)
# ==============================================================================

# FrostAgent HTTP endpoint
FROSTAGENT_ENDPOINT=http://127.0.0.1:8000

# Custom send endpoint on FrostAgent (optional; defaults to /api/v1/frostagent/send)
# FROSTAGENT_SEND_ENDPOINT=

# FrostAgent instance identifier (optional)
# FROSTAGENT_INSTANCE_ID=

# FrostAgent API Key for authentication (optional)
# FROSTAGENT_API_KEY=
`

// SetupResult contains the outcome of SetupEnv.
type SetupResult struct {
	Path       string
	Created    bool
	Loaded     bool
	LoadedVars int
}

// FindRootEnvPath resolves the target path for the .env file.
// Priority:
// 1. ACTIONSCAT_ENV_FILE environment variable (if non-empty)
// 2. Upward directory traversal from cwd looking for go.mod to identify project root
// 3. Fallback to "./.env"
func FindRootEnvPath() string {
	if custom := os.Getenv("ACTIONSCAT_ENV_FILE"); custom != "" {
		return custom
	}

	cwd, err := os.Getwd()
	if err != nil {
		return ".env"
	}

	dir := cwd
	for range 8 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, ".env")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return filepath.Join(cwd, ".env")
}

// EnsureEnvFile checks if an env file exists at path. If not, it generates DefaultEnvContent.
// Returns true if a new file was created, false if it already existed.
func EnsureEnvFile(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("check env file %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return false, fmt.Errorf("create directory for env file %s: %w", dir, err)
		}
	}

	if err := os.WriteFile(path, []byte(DefaultEnvContent), 0600); err != nil {
		return false, fmt.Errorf("write default env file %s: %w", path, err)
	}

	return true, nil
}

// ParseEnv parses a raw string containing dotenv formatted key-value pairs.
func ParseEnv(content string) (map[string]string, error) {
	return ParseEnvReader(strings.NewReader(content))
}

// ParseEnvReader reads and parses dotenv formatted lines from an io.Reader.
func ParseEnvReader(r io.Reader) (map[string]string, error) {
	vars := make(map[string]string)
	scanner := bufio.NewScanner(r)
	lineNum := 0
	utf8BOM := string([]byte{0xEF, 0xBB, 0xBF})

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if lineNum == 1 {
			line = strings.TrimPrefix(line, utf8BOM)
			line = strings.TrimSpace(line)
		}

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Support "export KEY=VAL" syntax
		if after, ok := strings.CutPrefix(line, "export "); ok {
			line = strings.TrimSpace(after)
		}

		rawKey, rawVal, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}

		key := strings.TrimSpace(rawKey)
		if key == "" {
			continue
		}

		val := strings.TrimSpace(rawVal)
		parsedVal, err := parseValue(val)
		if err != nil {
			return nil, fmt.Errorf("line %d (%s): %w", lineNum, key, err)
		}

		vars[key] = parsedVal
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan env lines: %w", err)
	}

	return vars, nil
}

func parseValue(raw string) (string, error) {
	val := strings.TrimSpace(raw)
	if val == "" {
		return "", nil
	}

	// Double quoted value
	if strings.HasPrefix(val, "\"") {
		var sb strings.Builder
		escaped := false
		closed := false
		i := 1
		for i < len(val) {
			ch := val[i]
			if escaped {
				switch ch {
				case 'n':
					sb.WriteByte('\n')
				case 'r':
					sb.WriteByte('\r')
				case 't':
					sb.WriteByte('\t')
				case '"':
					sb.WriteByte('"')
				case '\\':
					sb.WriteByte('\\')
				case '$':
					sb.WriteByte('$')
				default:
					sb.WriteByte('\\')
					sb.WriteByte(ch)
				}
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				closed = true
				i++
				break
			} else {
				sb.WriteByte(ch)
			}
			i++
		}
		if !closed {
			return "", fmt.Errorf("unclosed double quote in %q", val)
		}
		remainder := strings.TrimSpace(val[i:])
		if remainder != "" && !strings.HasPrefix(remainder, "#") {
			return "", fmt.Errorf("unexpected trailing characters after closing quote in %q", val)
		}
		return sb.String(), nil
	}

	// Single quoted value
	if strings.HasPrefix(val, "'") {
		endIdx := strings.Index(val[1:], "'")
		if endIdx == -1 {
			return "", fmt.Errorf("unclosed single quote in %q", val)
		}
		content := val[1 : 1+endIdx]
		remainder := strings.TrimSpace(val[1+endIdx+1:])
		if remainder != "" && !strings.HasPrefix(remainder, "#") {
			return "", fmt.Errorf("unexpected trailing characters after closing quote in %q", val)
		}
		return content, nil
	}

	// Unquoted value: strip trailing comment separated by whitespace
	for idx := 0; idx < len(val); idx++ {
		if val[idx] == '#' {
			if idx > 0 && (val[idx-1] == ' ' || val[idx-1] == '\t') {
				val = strings.TrimSpace(val[:idx])
				break
			}
		}
	}

	return strings.TrimSpace(val), nil
}

// LoadEnvFile reads and parses the .env file at path and applies variables to os.Setenv.
// If override is false, existing environment variables in os.Environ() are preserved.
func LoadEnvFile(path string, override bool) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open env file %s: %w", path, err)
	}
	defer f.Close()

	vars, err := ParseEnvReader(f)
	if err != nil {
		return 0, fmt.Errorf("parse env file %s: %w", path, err)
	}

	loadedCount := 0
	for k, v := range vars {
		if !override {
			if _, exists := os.LookupEnv(k); exists {
				continue
			}
		}
		if err := os.Setenv(k, v); err != nil {
			return loadedCount, fmt.Errorf("setenv %s: %w", k, err)
		}
		loadedCount++
	}

	return loadedCount, nil
}

// SetupEnv discovers the root .env file, creates it with default template if missing,
// and loads it into the process environment without overriding already set variables.
func SetupEnv() (*SetupResult, error) {
	path := FindRootEnvPath()
	return SetupEnvWithPath(path, false)
}

// SetupEnvWithPath ensures the env file at path exists (creating it if absent)
// and loads variables into the environment.
func SetupEnvWithPath(path string, override bool) (*SetupResult, error) {
	created, err := EnsureEnvFile(path)
	if err != nil {
		return nil, fmt.Errorf("ensure env file: %w", err)
	}

	loadedCount, err := LoadEnvFile(path, override)
	if err != nil {
		return nil, fmt.Errorf("load env file: %w", err)
	}

	return &SetupResult{
		Path:       path,
		Created:    created,
		Loaded:     true,
		LoadedVars: loadedCount,
	}, nil
}
