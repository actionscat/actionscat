package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// FileStore manages persistent source code files and immutable built artifact bundles.
type FileStore struct {
	dataDir string
}

func NewFileStore(dataDir string) *FileStore {
	return &FileStore{dataDir: dataDir}
}

// SaveSourceBundle saves source files for an ActionVersion and returns the SHA256 digest and relative path.
func (f *FileStore) SaveSourceBundle(actionID, versionID string, files map[string][]byte) (string, string, error) {
	relDir := filepath.Join("actions", actionID, "versions", versionID, "source")
	absDir := filepath.Join(f.dataDir, relDir)

	if err := os.MkdirAll(absDir, 0750); err != nil {
		return "", "", fmt.Errorf("failed to create source dir: %w", err)
	}

	// Sort file paths for deterministic bundle hashing
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	bundleHasher := sha256.New()
	for _, name := range keys {
		cleanName, err := sanitizeStatePath(name)
		if err != nil {
			return "", "", fmt.Errorf("invalid source filename %q: %w", name, err)
		}

		filePath := filepath.Join(absDir, filepath.FromSlash(cleanName))
		if err := os.MkdirAll(filepath.Dir(filePath), 0750); err != nil {
			return "", "", fmt.Errorf("failed to create sub dir: %w", err)
		}

		data := files[name]
		if err := os.WriteFile(filePath, data, 0640); err != nil {
			return "", "", fmt.Errorf("failed to write source file %q: %w", name, err)
		}

		// Feed into deterministic bundle hash
		bundleHasher.Write([]byte(cleanName))
		bundleHasher.Write([]byte{0})
		bundleHasher.Write(data)
		bundleHasher.Write([]byte{0})
	}

	digest := hex.EncodeToString(bundleHasher.Sum(nil))
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
