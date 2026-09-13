package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStateStore_PathValidation(t *testing.T) {
	tempDir := t.TempDir()
	ss := NewStateStore(tempDir)

	actionID := "action_test_123"

	// Valid writes
	if err := ss.WriteState(actionID, "html.json", []byte(`{"title":"hello"}`)); err != nil {
		t.Fatalf("expected valid write to succeed, got %v", err)
	}
	if err := ss.WriteState(actionID, "nested/sub/data.txt", []byte("sample")); err != nil {
		t.Fatalf("expected valid nested write to succeed, got %v", err)
	}

	data, err := ss.ReadState(actionID, "html.json")
	if err != nil || !bytes.Equal(data, []byte(`{"title":"hello"}`)) {
		t.Fatalf("read failed or content mismatch: %v, %s", err, string(data))
	}

	// Security tests: Path traversal attempts MUST be rejected
	traversalPaths := []string{
		"../escape.json",
		"../../escape.json",
		"/etc/passwd",
		"nested/../../secret.json",
		"..\\escape.json",
		"C:\\Windows\\System32\\calc.exe",
		"null\x00byte.json",
		"",
		".",
		"..",
	}

	for _, badPath := range traversalPaths {
		t.Run("bad_path_"+badPath, func(t *testing.T) {
			err := ss.WriteState(actionID, badPath, []byte("evil"))
			if err == nil {
				t.Errorf("expected WriteState to reject dangerous path %q, but got nil", badPath)
			}
			_, err = ss.ReadState(actionID, badPath)
			if err == nil {
				t.Errorf("expected ReadState to reject dangerous path %q, but got nil", badPath)
			}
		})
	}
}

func TestStateStore_NamespaceIsolation(t *testing.T) {
	tempDir := t.TempDir()
	ss := NewStateStore(tempDir)

	actA := "action_alpha"
	actB := "action_beta"

	if err := ss.WriteState(actA, "secret.json", []byte("alpha-data")); err != nil {
		t.Fatalf("failed to write actA state: %v", err)
	}

	// ActB should not find secret.json in its namespace
	_, err := ss.ReadState(actB, "secret.json")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist for actB, got: %v", err)
	}

	// Verify on filesystem that actA is strictly in its directory
	expectedFile := filepath.Join(tempDir, "actions", actA, "state", "secret.json")
	if _, err := os.Stat(expectedFile); err != nil {
		t.Fatalf("expected file to exist at %s: %v", expectedFile, err)
	}
}

func TestStateStore_AtomicWrite(t *testing.T) {
	tempDir := t.TempDir()
	ss := NewStateStore(tempDir)

	actionID := "action_atomic"
	initialData := []byte("v1 data")
	if err := ss.WriteState(actionID, "state.bin", initialData); err != nil {
		t.Fatalf("initial write failed: %v", err)
	}

	// Overwrite with v2
	newData := []byte("v2 updated data")
	if err := ss.WriteState(actionID, "state.bin", newData); err != nil {
		t.Fatalf("overwrite failed: %v", err)
	}

	readBack, err := ss.ReadState(actionID, "state.bin")
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if !bytes.Equal(readBack, newData) {
		t.Fatalf("expected %q, got %q", string(newData), string(readBack))
	}
}
