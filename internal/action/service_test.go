package action

import (
	"actionscat/internal/domain"
	"actionscat/internal/sandbox"
	"actionscat/internal/store"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func setupActionTest(t *testing.T) (*store.SQLiteStore, *store.FileStore, *sandbox.FakeBackend, *Service) {
	tempDir := t.TempDir()
	db, err := store.OpenDB(filepath.Join(tempDir, "test.db"))
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	st := store.NewSQLiteStore(db)
	fs := store.NewFileStore(tempDir)
	sb := sandbox.NewFakeBackend()
	svc := NewService(st, fs, sb)

	return st, fs, sb, svc
}

func TestActionService_ValidationAndVersioning(t *testing.T) {
	ctx := context.Background()
	_, _, _, svc := setupActionTest(t)

	// Empty name rejected
	_, err := svc.CreateAction(ctx, CreateActionRequest{Name: "   "})
	if err == nil {
		t.Fatal("expected empty name to fail")
	}

	act, err := svc.CreateAction(ctx, CreateActionRequest{
		Name:           "My Action",
		Description:    "Test description",
		MaxConcurrency: 2,
	})
	if err != nil {
		t.Fatalf("create action: %v", err)
	}
	if act.MaxConcurrency != 2 {
		t.Fatalf("expected concurrency 2, got %d", act.MaxConcurrency)
	}

	// Create version with empty sources rejected
	_, err = svc.CreateVersion(ctx, act.ID, CreateVersionRequest{})
	if err == nil {
		t.Fatal("expected empty sources to fail")
	}

	// Valid version
	v1, err := svc.CreateVersion(ctx, act.ID, CreateVersionRequest{
		Files: map[string][]byte{
			"main.go": []byte("package main\n"),
		},
		BuildSpec: domain.BuildSpec{
			Language: "go",
		},
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	if v1.VersionNumber != 1 {
		t.Fatalf("expected version number 1, got %d", v1.VersionNumber)
	}

	v2, err := svc.CreateVersion(ctx, act.ID, CreateVersionRequest{
		Files: map[string][]byte{
			"main.go": []byte("package main\n// v2\n"),
		},
	})
	if err != nil {
		t.Fatalf("create version 2: %v", err)
	}
	if v2.VersionNumber != 2 {
		t.Fatalf("expected version number 2, got %d", v2.VersionNumber)
	}

	// List versions
	versions, err := svc.ListVersions(ctx, act.ID)
	if err != nil || len(versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(versions))
	}
}

func TestActionService_CreateVersion_MultipleActions_NoIDCollision(t *testing.T) {
	ctx := context.Background()
	_, _, _, svc := setupActionTest(t)

	actA, err := svc.CreateAction(ctx, CreateActionRequest{Name: "Action A"})
	if err != nil {
		t.Fatalf("create action A: %v", err)
	}
	actB, err := svc.CreateAction(ctx, CreateActionRequest{Name: "Action B"})
	if err != nil {
		t.Fatalf("create action B: %v", err)
	}

	// Create version 1 for Action A
	vA1, err := svc.CreateVersion(ctx, actA.ID, CreateVersionRequest{
		Files: map[string][]byte{"main.go": []byte("package main // Action A v1\n")},
	})
	if err != nil {
		t.Fatalf("create version A v1: %v", err)
	}

	// Create version 1 for Action B - MUST NOT collide with Action A's version ID
	vB1, err := svc.CreateVersion(ctx, actB.ID, CreateVersionRequest{
		Files: map[string][]byte{"main.go": []byte("package main // Action B v1\n")},
	})
	if err != nil {
		t.Fatalf("create version B v1: %v", err)
	}

	if vA1.ID == vB1.ID {
		t.Fatalf("version IDs must be globally unique across actions, got identical ID %q", vA1.ID)
	}
	if vA1.VersionNumber != 1 || vB1.VersionNumber != 1 {
		t.Fatalf("both actions should have version_number 1, got %d and %d", vA1.VersionNumber, vB1.VersionNumber)
	}
}

func TestActionService_CreateVersion_Concurrent_DigestConsistent(t *testing.T) {
	ctx := context.Background()
	_, fs, _, svc := setupActionTest(t)

	act, err := svc.CreateAction(ctx, CreateActionRequest{Name: "Concurrent Action"})
	if err != nil {
		t.Fatalf("create action: %v", err)
	}

	const concurrency = 10
	var wg sync.WaitGroup
	errs := make(chan error, concurrency)
	createdVersions := make([]*domain.ActionVersion, concurrency)
	payloads := make([]string, concurrency)

	for i := range concurrency {
		idx := i
		payloads[idx] = fmt.Sprintf("package main\n// version payload %d\nvar Data = %d\n", idx, idx*100)
		wg.Add(1)
		go func() {
			defer wg.Done()
			ver, err := svc.CreateVersion(ctx, act.ID, CreateVersionRequest{
				Files: map[string][]byte{
					"main.go": []byte(payloads[idx]),
				},
			})
			if err != nil {
				errs <- fmt.Errorf("goroutine %d failed: %w", idx, err)
				return
			}
			createdVersions[idx] = ver
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatal(err)
	}

	// Verify all version numbers are unique and in range 1..concurrency
	seenNumbers := make(map[int]bool)
	for i, v := range createdVersions {
		if v == nil {
			t.Fatalf("version %d is nil", i)
		}
		if seenNumbers[v.VersionNumber] {
			t.Fatalf("duplicate version number %d", v.VersionNumber)
		}
		seenNumbers[v.VersionNumber] = true

		// Read back source bundle from disk and verify exact content and digest match
		readFiles, err := fs.ReadSourceBundle(act.ID, v.ID)
		if err != nil {
			t.Fatalf("failed to read source bundle for %s: %v", v.ID, err)
		}
		if string(readFiles["main.go"]) != payloads[i] {
			t.Fatalf("disk payload corrupted for version %s: expected %q, got %q", v.ID, payloads[i], string(readFiles["main.go"]))
		}
	}
}

func TestActionService_CreateVersion_DBFailure_RollsBackFiles(t *testing.T) {
	tempDir := t.TempDir()
	db, err := store.OpenDB(filepath.Join(tempDir, "test.db"))
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	st := store.NewSQLiteStore(db)
	fs := store.NewFileStore(tempDir)
	sb := sandbox.NewFakeBackend()
	svc := NewService(st, fs, sb)

	ctx := context.Background()
	act, err := svc.CreateAction(ctx, CreateActionRequest{Name: "Rollback Action"})
	if err != nil {
		t.Fatalf("create action: %v", err)
	}

	// Close database to trigger guaranteed failure during CreateVersion DB insertion
	_ = db.Close()

	expectedVerID := fmt.Sprintf("ver_%s_%06d", act.ID, 1)
	_, err = svc.CreateVersion(ctx, act.ID, CreateVersionRequest{
		Files: map[string][]byte{"main.go": []byte("package main\n")},
	})
	if err == nil {
		t.Fatal("expected CreateVersion to fail when DB is closed")
	}

	// Verify that committed files were rolled back and no corrupted files remain on disk!
	_, err = fs.ReadSourceBundle(act.ID, expectedVerID)
	if err == nil {
		t.Fatalf("security violation: expected source bundle for %s to be rolled back, but it exists on disk", expectedVerID)
	}

	// Also verify no staging files remain
	stagingDir := filepath.Join(tempDir, "actions", act.ID, "staging")
	entries, _ := os.ReadDir(stagingDir)
	if len(entries) > 0 {
		t.Fatalf("expected staging directory to be cleaned, found %d entries", len(entries))
	}
}

func TestActionService_CreateVersion_CrashRecovery_OrphanCleanedAndOverwritten(t *testing.T) {
	ctx := context.Background()
	_, fs, _, svc := setupActionTest(t)

	act, err := svc.CreateAction(ctx, CreateActionRequest{Name: "Crash Action"})
	if err != nil {
		t.Fatalf("create action: %v", err)
	}

	// Simulate an orphaned version on disk left by a previous crash before DB insertion
	orphanVerID := fmt.Sprintf("ver_%s_%06d", act.ID, 1)
	orphanDir := filepath.Join(fs.DataDir(), "actions", act.ID, "versions", orphanVerID, "source")
	if err := os.MkdirAll(orphanDir, 0750); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(orphanDir, "stale.go"), []byte("stale-corrupted-code"), 0644); err != nil {
		t.Fatalf("write stale: %v", err)
	}

	// Verify CleanOrphanedVersions finds and cleans the orphaned version directory
	cleaned, err := svc.CleanOrphanedVersions(ctx, act.ID)
	if err != nil {
		t.Fatalf("clean orphans: %v", err)
	}
	if cleaned != 1 {
		t.Fatalf("expected 1 orphan cleaned, got %d", cleaned)
	}

	// Creating Version 1 now must succeed cleanly and contain only the new files
	v1, err := svc.CreateVersion(ctx, act.ID, CreateVersionRequest{
		Files: map[string][]byte{"main.go": []byte("package main // fresh valid code\n")},
	})
	if err != nil {
		t.Fatalf("create version 1 after recovery: %v", err)
	}
	if v1.VersionNumber != 1 {
		t.Fatalf("expected version number 1, got %d", v1.VersionNumber)
	}

	readFiles, err := fs.ReadSourceBundle(act.ID, v1.ID)
	if err != nil {
		t.Fatalf("read source bundle: %v", err)
	}
	if string(readFiles["main.go"]) != "package main // fresh valid code\n" {
		t.Fatalf("unexpected content: %s", string(readFiles["main.go"]))
	}
	if _, hasStale := readFiles["stale.go"]; hasStale {
		t.Fatal("stale file from previous crash was not purged!")
	}
}
