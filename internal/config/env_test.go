package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnv_Basic(t *testing.T) {
	input := `
# Server configuration
ACTIONSCAT_ADDR=:7999
ACTIONSCAT_DATA_DIR = ./data
ACTIONSCAT_RUNNER_WORKERS = 16

# Empty lines and empty values
EMPTY_VAL=
SPACED_EMPTY=
`
	vars, err := ParseEnv(input)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	if vars["ACTIONSCAT_ADDR"] != ":7999" {
		t.Errorf("expected :7999, got %q", vars["ACTIONSCAT_ADDR"])
	}
	if vars["ACTIONSCAT_DATA_DIR"] != "./data" {
		t.Errorf("expected ./data, got %q", vars["ACTIONSCAT_DATA_DIR"])
	}
	if vars["ACTIONSCAT_RUNNER_WORKERS"] != "16" {
		t.Errorf("expected 16, got %q", vars["ACTIONSCAT_RUNNER_WORKERS"])
	}
	if vars["EMPTY_VAL"] != "" {
		t.Errorf("expected empty string, got %q", vars["EMPTY_VAL"])
	}
	if vars["SPACED_EMPTY"] != "" {
		t.Errorf("expected empty string, got %q", vars["SPACED_EMPTY"])
	}
}

func TestParseEnv_Quotes(t *testing.T) {
	input := `
DOUBLE_QUOTED="hello world"
DOUBLE_QUOTED_ESCAPES="line1\nline2\ttab\"quote\\"
SINGLE_QUOTED='raw $not_expanded \n'
QUOTED_EMPTY=""
SINGLE_EMPTY=''
WITH_INLINE_COMMENT="quoted value" # this is a comment
`
	vars, err := ParseEnv(input)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	if vars["DOUBLE_QUOTED"] != "hello world" {
		t.Errorf("expected 'hello world', got %q", vars["DOUBLE_QUOTED"])
	}
	expectedEscapes := "line1\nline2\ttab\"quote\\"
	if vars["DOUBLE_QUOTED_ESCAPES"] != expectedEscapes {
		t.Errorf("expected %q, got %q", expectedEscapes, vars["DOUBLE_QUOTED_ESCAPES"])
	}
	if vars["SINGLE_QUOTED"] != `raw $not_expanded \n` {
		t.Errorf("expected raw string, got %q", vars["SINGLE_QUOTED"])
	}
	if vars["QUOTED_EMPTY"] != "" {
		t.Errorf("expected empty string, got %q", vars["QUOTED_EMPTY"])
	}
	if vars["SINGLE_EMPTY"] != "" {
		t.Errorf("expected empty string, got %q", vars["SINGLE_EMPTY"])
	}
	if vars["WITH_INLINE_COMMENT"] != "quoted value" {
		t.Errorf("expected 'quoted value', got %q", vars["WITH_INLINE_COMMENT"])
	}
}

func TestParseEnv_CommentsAndExports(t *testing.T) {
	input := `
# full line comment
export EXPORTED_VAR=123
UNQUOTED_COMMENT=my_value # inline comment here
URL_WITH_HASH=http://127.0.0.1:8000/path#anchor
`
	vars, err := ParseEnv(input)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	if vars["EXPORTED_VAR"] != "123" {
		t.Errorf("expected '123', got %q", vars["EXPORTED_VAR"])
	}
	if vars["UNQUOTED_COMMENT"] != "my_value" {
		t.Errorf("expected 'my_value', got %q", vars["UNQUOTED_COMMENT"])
	}
	if vars["URL_WITH_HASH"] != "http://127.0.0.1:8000/path#anchor" {
		t.Errorf("expected url with hash preserved, got %q", vars["URL_WITH_HASH"])
	}
}

func TestParseEnv_Errors(t *testing.T) {
	_, err := ParseEnv(`BAD_DOUBLE="unclosed quote`)
	if err == nil {
		t.Error("expected error for unclosed double quote")
	}

	_, err = ParseEnv(`BAD_SINGLE='unclosed quote`)
	if err == nil {
		t.Error("expected error for unclosed single quote")
	}

	_, err = ParseEnv(`BAD_TRAILING="ok" trailing_invalid`)
	if err == nil {
		t.Error("expected error for unexpected trailing characters after quote")
	}
}

func TestEnsureEnvFile(t *testing.T) {
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, "sub", ".env")

	// 1. Should create file when missing
	created, err := EnsureEnvFile(envPath)
	if err != nil {
		t.Fatalf("failed to ensure env file: %v", err)
	}
	if !created {
		t.Error("expected created=true for new file")
	}

	content, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("failed to read created env file: %v", err)
	}
	if !strings.Contains(string(content), "ACTIONSCAT_ADDR") {
		t.Errorf("expected default content to include ACTIONSCAT_ADDR, got:\n%s", string(content))
	}

	// 2. Modifying file and re-ensuring should not overwrite
	customContent := "ACTIONSCAT_ADDR=:9999\n"
	if err := os.WriteFile(envPath, []byte(customContent), 0600); err != nil {
		t.Fatalf("failed to overwrite custom file: %v", err)
	}

	createdAgain, err := EnsureEnvFile(envPath)
	if err != nil {
		t.Fatalf("unexpected error re-ensuring env file: %v", err)
	}
	if createdAgain {
		t.Error("expected created=false for existing file")
	}

	afterContent, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("failed to read env file: %v", err)
	}
	if string(afterContent) != customContent {
		t.Errorf("expected existing file to be preserved, got:\n%s", string(afterContent))
	}
}

func TestLoadEnvFile(t *testing.T) {
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, ".env")

	const testKey1 = "TEST_ACTIONSCAT_ENV_KEY1"
	const testKey2 = "TEST_ACTIONSCAT_ENV_KEY2"

	// Preset testKey1 in OS environment
	_ = os.Setenv(testKey1, "initial_value")
	_ = os.Unsetenv(testKey2)
	defer func() {
		_ = os.Unsetenv(testKey1)
		_ = os.Unsetenv(testKey2)
	}()

	content := testKey1 + "=from_file\n" + testKey2 + "=from_file_2\n"
	if err := os.WriteFile(envPath, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write test env file: %v", err)
	}

	// Without override: testKey1 should stay "initial_value"
	loaded, err := LoadEnvFile(envPath, false)
	if err != nil {
		t.Fatalf("unexpected error loading env: %v", err)
	}
	if loaded != 1 {
		t.Errorf("expected 1 variable loaded (testKey1 skipped), got %d", loaded)
	}
	if val := os.Getenv(testKey1); val != "initial_value" {
		t.Errorf("expected initial_value preserved, got %q", val)
	}
	if val := os.Getenv(testKey2); val != "from_file_2" {
		t.Errorf("expected from_file_2 set, got %q", val)
	}

	// With override: testKey1 should now be "from_file"
	loadedOverride, err := LoadEnvFile(envPath, true)
	if err != nil {
		t.Fatalf("unexpected error loading env with override: %v", err)
	}
	if loadedOverride != 2 {
		t.Errorf("expected 2 variables loaded, got %d", loadedOverride)
	}
	if val := os.Getenv(testKey1); val != "from_file" {
		t.Errorf("expected from_file overridden, got %q", val)
	}
}

func TestFindRootEnvPath(t *testing.T) {
	// Custom env var override
	customPath := filepath.Join(t.TempDir(), "custom.env")
	t.Setenv("ACTIONSCAT_ENV_FILE", customPath)
	if got := FindRootEnvPath(); got != customPath {
		t.Errorf("expected %s, got %s", customPath, got)
	}

	// Unset and test fallback
	_ = os.Unsetenv("ACTIONSCAT_ENV_FILE")
	defaultPath := FindRootEnvPath()
	if !strings.HasSuffix(defaultPath, ".env") {
		t.Errorf("expected path ending in .env, got %s", defaultPath)
	}
}

func TestSetupEnvWithPath(t *testing.T) {
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, ".env")

	testKey := "TEST_ACTIONSCAT_SETUP_KEY"
	_ = os.Unsetenv(testKey)
	defer os.Unsetenv(testKey)

	res, err := SetupEnvWithPath(envPath, false)
	if err != nil {
		t.Fatalf("SetupEnvWithPath failed: %v", err)
	}
	if !res.Created {
		t.Error("expected res.Created = true")
	}
	if !res.Loaded {
		t.Error("expected res.Loaded = true")
	}
	if res.Path != envPath {
		t.Errorf("expected path %s, got %s", envPath, res.Path)
	}

	// Check that default content has core variables
	vars, err := ParseEnv(DefaultEnvContent)
	if err != nil {
		t.Fatalf("failed to parse DefaultEnvContent: %v", err)
	}
	requiredKeys := []string{
		"ACTIONSCAT_ADDR",
		"ACTIONSCAT_DATA_DIR",
		"ACTIONSCAT_DB_PATH",
		"ACTIONSCAT_RUNNER_WORKERS",
		"ACTIONSCAT_RUNTIME_ENDPOINT",
		"FA_SANDBOX_ENDPOINT",
		"FROSTAGENT_ENDPOINT",
	}
	for _, key := range requiredKeys {
		if _, ok := vars[key]; !ok {
			t.Errorf("expected key %s in DefaultEnvContent", key)
		}
	}
}
