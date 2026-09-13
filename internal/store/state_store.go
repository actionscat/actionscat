package store

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var (
	ErrInvalidPath     = errors.New("invalid state path")
	ErrPathTraversal   = errors.New("path traversal detected")
	ErrInvalidActionID = errors.New("invalid action id")
)

// StateStore manages isolated persistent state for Actions.
type StateStore struct {
	dataDir string
}

// NewStateStore creates a new StateStore rooted at dataDir.
func NewStateStore(dataDir string) *StateStore {
	return &StateStore{dataDir: dataDir}
}

func validateActionID(actionID string) error {
	trimmed := strings.TrimSpace(actionID)
	if trimmed == "" || strings.ContainsAny(trimmed, "/\\..\x00") {
		return ErrInvalidActionID
	}
	return nil
}

func sanitizeStatePath(statePath string) (string, error) {
	if strings.Contains(statePath, "\x00") {
		return "", ErrInvalidPath
	}
	if filepath.VolumeName(statePath) != "" {
		return "", ErrPathTraversal
	}

	// Normalize separators to forward slash
	slashPath := filepath.ToSlash(statePath)
	if strings.HasPrefix(slashPath, "/") || strings.HasPrefix(slashPath, "\\") {
		return "", ErrPathTraversal
	}

	cleaned := path.Clean(slashPath)
	if cleaned == "." || cleaned == ".." || cleaned == "" {
		return "", ErrInvalidPath
	}
	if strings.HasPrefix(cleaned, "../") || cleaned == ".." || strings.Contains(cleaned, "/../") {
		return "", ErrPathTraversal
	}

	return cleaned, nil
}

func (s *StateStore) resolveStatePath(actionID, statePath string) (string, string, error) {
	if err := validateActionID(actionID); err != nil {
		return "", "", err
	}

	cleanRel, err := sanitizeStatePath(statePath)
	if err != nil {
		return "", "", err
	}

	stateDir := filepath.Join(s.dataDir, "actions", actionID, "state")
	absStateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve state dir: %w", err)
	}

	fullTarget := filepath.Join(absStateDir, filepath.FromSlash(cleanRel))
	absTarget, err := filepath.Abs(fullTarget)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve target path: %w", err)
	}

	rel, err := filepath.Rel(absStateDir, absTarget)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return "", "", ErrPathTraversal
	}

	return absStateDir, absTarget, nil
}

// WriteState atomically writes data to an action's isolated state directory.
func (s *StateStore) WriteState(actionID, statePath string, data []byte) error {
	_, targetFile, err := s.resolveStatePath(actionID, statePath)
	if err != nil {
		return err
	}

	parentDir := filepath.Dir(targetFile)
	if err := os.MkdirAll(parentDir, 0750); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	// Write to a temporary file in the same directory to allow atomic rename
	tmpFile, err := os.CreateTemp(parentDir, ".tmp-state-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpName := tmpFile.Name()

	cleanup := func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
	}

	if _, err := tmpFile.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("failed to write data: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("failed to sync data: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpName, targetFile); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to atomically rename state file: %w", err)
	}

	return nil
}

// ReadState reads a file from an action's isolated state directory.
func (s *StateStore) ReadState(actionID, statePath string) ([]byte, error) {
	_, targetFile, err := s.resolveStatePath(actionID, statePath)
	if err != nil {
		return nil, err
	}

	return os.ReadFile(targetFile)
}
