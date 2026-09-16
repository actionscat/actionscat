package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const (
	// MaxArtifactTotalBytes defines the maximum total size of an artifact bundle (64 MB).
	MaxArtifactTotalBytes = 64 * 1024 * 1024
)

var (
	ErrArtifactTooLarge = errors.New("artifact bundle exceeds maximum size (64MB)")
)

// FileStore manages persistent source code files and immutable built artifact bundles.
type FileStore struct {
	dataDir string
}

func NewFileStore(dataDir string) *FileStore {
	return &FileStore{dataDir: dataDir}
}

func (f *FileStore) DataDir() string {
	return f.dataDir
}

// StageSourceBundle writes source files into an isolated staging directory, calculates the SHA256 digest,
// and returns the digest and a temporary stageToken.
func (f *FileStore) StageSourceBundle(actionID string, files map[string][]byte) (string, string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("failed to generate staging token: %w", err)
	}
	stageToken := hex.EncodeToString(b)

	stageDir := filepath.Join(f.dataDir, "actions", actionID, "staging", stageToken, "source")
	if err := os.MkdirAll(stageDir, 0750); err != nil {
		return "", "", fmt.Errorf("failed to create staging source dir: %w", err)
	}

	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	bundleHasher := sha256.New()
	for _, name := range keys {
		cleanName, err := sanitizeStatePath(name)
		if err != nil {
			_ = os.RemoveAll(filepath.Join(f.dataDir, "actions", actionID, "staging", stageToken))
			return "", "", fmt.Errorf("invalid source filename %q: %w", name, err)
		}

		filePath := filepath.Join(stageDir, filepath.FromSlash(cleanName))
		if err := os.MkdirAll(filepath.Dir(filePath), 0750); err != nil {
			_ = os.RemoveAll(filepath.Join(f.dataDir, "actions", actionID, "staging", stageToken))
			return "", "", fmt.Errorf("failed to create staging sub dir: %w", err)
		}

		data := files[name]
		if err := os.WriteFile(filePath, data, 0640); err != nil {
			_ = os.RemoveAll(filepath.Join(f.dataDir, "actions", actionID, "staging", stageToken))
			return "", "", fmt.Errorf("failed to write staging source file %q: %w", name, err)
		}

		bundleHasher.Write([]byte(cleanName))
		bundleHasher.Write([]byte{0})
		bundleHasher.Write(data)
		bundleHasher.Write([]byte{0})
	}

	digest := hex.EncodeToString(bundleHasher.Sum(nil))
	return digest, stageToken, nil
}

// CommitSourceBundle atomically moves a staged source bundle to its final version directory.
func (f *FileStore) CommitSourceBundle(actionID, stageToken, versionID string) (string, error) {
	stagedVersionDir := filepath.Join(f.dataDir, "actions", actionID, "staging", stageToken)
	destVersionDir := filepath.Join(f.dataDir, "actions", actionID, "versions", versionID)
	relDir := filepath.Join("actions", actionID, "versions", versionID, "source")

	if err := os.MkdirAll(filepath.Dir(destVersionDir), 0750); err != nil {
		return "", fmt.Errorf("failed to ensure versions dir: %w", err)
	}

	// Remove existing destination directory (if any exists from an uncommitted crash) to ensure rename succeeds
	_ = os.RemoveAll(destVersionDir)

	if err := os.Rename(stagedVersionDir, destVersionDir); err != nil {
		return "", fmt.Errorf("failed to atomically commit source bundle: %w", err)
	}

	// Clean up empty staging parent if possible
	_ = os.Remove(filepath.Join(f.dataDir, "actions", actionID, "staging"))

	return relDir, nil
}

// DeleteSourceBundle removes a committed source bundle if subsequent database operations fail or for cleanup.
func (f *FileStore) DeleteSourceBundle(actionID, versionID string) error {
	destVersionDir := filepath.Join(f.dataDir, "actions", actionID, "versions", versionID)
	return os.RemoveAll(destVersionDir)
}

// ListStoredVersionIDs returns all version directory names currently on disk for an action.
func (f *FileStore) ListStoredVersionIDs(actionID string) ([]string, error) {
	versionsDir := filepath.Join(f.dataDir, "actions", actionID, "versions")
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() {
			ids = append(ids, entry.Name())
		}
	}
	return ids, nil
}

// DiscardSourceBundle removes a staged source bundle if version creation fails.
func (f *FileStore) DiscardSourceBundle(actionID, stageToken string) error {
	stagedVersionDir := filepath.Join(f.dataDir, "actions", actionID, "staging", stageToken)
	return os.RemoveAll(stagedVersionDir)
}

// SaveSourceBundle saves source files for an ActionVersion and returns the SHA256 digest and relative path.
func (f *FileStore) SaveSourceBundle(actionID, versionID string, files map[string][]byte) (string, string, error) {
	digest, token, err := f.StageSourceBundle(actionID, files)
	if err != nil {
		return "", "", err
	}
	relDir, err := f.CommitSourceBundle(actionID, token, versionID)
	if err != nil {
		_ = f.DiscardSourceBundle(actionID, token)
		return "", "", err
	}
	return digest, relDir, nil
}

// ReadSourceBundle reads all source files for a given ActionVersion.
func (f *FileStore) ReadSourceBundle(actionID, versionID string) (map[string][]byte, error) {
	absDir := filepath.Join(f.dataDir, "actions", actionID, "versions", versionID, "source")
	files := make(map[string][]byte)

	err := filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(absDir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read source bundle: %w", err)
	}
	return files, nil
}

// SaveArtifactBundle stores built artifact files and marks them read-only.
func (f *FileStore) SaveArtifactBundle(actionID, versionID, buildID string, files map[string][]byte) (string, string, int64, error) {
	var expectedTotal int64
	for _, data := range files {
		expectedTotal += int64(len(data))
	}
	if expectedTotal > MaxArtifactTotalBytes {
		return "", "", 0, fmt.Errorf("%w: total artifact size %d exceeds limit of %d bytes", ErrArtifactTooLarge, expectedTotal, MaxArtifactTotalBytes)
	}

	relDir := filepath.Join("actions", actionID, "versions", versionID, "builds", buildID, "artifact")
	absDir := filepath.Join(f.dataDir, relDir)

	if err := os.MkdirAll(absDir, 0750); err != nil {
		return "", "", 0, fmt.Errorf("failed to create artifact dir: %w", err)
	}

	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	bundleHasher := sha256.New()
	var totalSize int64

	for _, name := range keys {
		cleanName, err := sanitizeStatePath(name)
		if err != nil {
			return "", "", 0, fmt.Errorf("invalid artifact filename %q: %w", name, err)
		}

		filePath := filepath.Join(absDir, filepath.FromSlash(cleanName))
		if err := os.MkdirAll(filepath.Dir(filePath), 0750); err != nil {
			return "", "", 0, fmt.Errorf("failed to create sub dir: %w", err)
		}

		data := files[name]
		// Write with read-only/immutable permission intent (0550 / 0440)
		if err := os.WriteFile(filePath, data, 0550); err != nil {
			return "", "", 0, fmt.Errorf("failed to write artifact file %q: %w", name, err)
		}

		totalSize += int64(len(data))
		bundleHasher.Write([]byte(cleanName))
		bundleHasher.Write([]byte{0})
		bundleHasher.Write(data)
		bundleHasher.Write([]byte{0})
	}

	digest := hex.EncodeToString(bundleHasher.Sum(nil))
	return digest, relDir, totalSize, nil
}

// ReadArtifactBundle reads all files in an artifact bundle.
func (f *FileStore) ReadArtifactBundle(actionID, versionID, buildID string) (map[string][]byte, error) {
	absDir := filepath.Join(f.dataDir, "actions", actionID, "versions", versionID, "builds", buildID, "artifact")
	files := make(map[string][]byte)

	err := filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(absDir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read artifact bundle: %w", err)
	}
	return files, nil
}
