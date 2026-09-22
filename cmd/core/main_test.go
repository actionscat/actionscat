package main

import (
	"actionscat/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestCoreEnvInitialization(t *testing.T) {
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, ".env")
	t.Setenv("ACTIONSCAT_ENV_FILE", envPath)

	// 1. Initial run: file should not exist, SetupEnv should create it
	res, err := config.SetupEnv()
	if err != nil {
		t.Fatalf("SetupEnv failed: %v", err)
	}
	if !res.Created {
		t.Errorf("expected res.Created=true, got false")
	}
	if !res.Loaded {
		t.Errorf("expected res.Loaded=true, got false")
	}

	// Verify file actually exists on disk
	if _, err := os.Stat(envPath); err != nil {
		t.Fatalf("expected env file to exist at %s: %v", envPath, err)
	}

	// Verify core variables are populated
	addr := os.Getenv("ACTIONSCAT_ADDR")
	if addr != ":7999" {
		t.Errorf("expected ACTIONSCAT_ADDR=:7999, got %q", addr)
	}

	// 2. Second run: file already exists, should not recreate
	res2, err := config.SetupEnv()
	if err != nil {
		t.Fatalf("SetupEnv second run failed: %v", err)
	}
	if res2.Created {
		t.Errorf("expected res2.Created=false on existing file")
	}
}
