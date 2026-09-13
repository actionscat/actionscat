package action

import (
	"actionscat/internal/domain"
	"actionscat/internal/sandbox"
	"actionscat/internal/store"
	"context"
	"path/filepath"
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
