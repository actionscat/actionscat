package build

import (
	"actionscat/internal/action"
	"actionscat/internal/domain"
	"actionscat/internal/sandbox"
	"actionscat/internal/store"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func setupTestEnvironment(t *testing.T) (*store.SQLiteStore, *store.FileStore, *sandbox.FakeBackend, *action.Service, *Builder) {
	tempDir := t.TempDir()
	db, err := store.OpenDB(filepath.Join(tempDir, "test.db"))
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	st := store.NewSQLiteStore(db)
	fs := store.NewFileStore(tempDir)
	sb := sandbox.NewFakeBackend()
	actSvc := action.NewService(st, fs, sb)
	builder := NewBuilder(st, fs, sb)

	return st, fs, sb, actSvc, builder
}

func TestBuildLifecycle_SuccessAndActivation(t *testing.T) {
	ctx := context.Background()
	_, _, _, actSvc, builder := setupTestEnvironment(t)

	// 1. Create Action
	act, err := actSvc.CreateAction(ctx, action.CreateActionRequest{
		Name: "Test Action",
	})
	if err != nil {
		t.Fatalf("create action: %v", err)
	}

	// 2. Create Version 1
	srcFiles := map[string][]byte{
		"main.go": []byte("package main\nfunc main() {}\n"),
		"go.mod":  []byte("module example.com/test\ngo 1.25.3\n"),
	}
	ver, err := actSvc.CreateVersion(ctx, act.ID, action.CreateVersionRequest{
		Files: srcFiles,
		BuildSpec: domain.BuildSpec{
			Language:             "go",
			ToolchainRequirement: ">= 1.25",
			Command:              "go build -o /out/entrypoint .",
		},
		RuntimeSpec: domain.RuntimeSpec{
			Entrypoint: "entrypoint",
		},
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}

	// 3. Build Version 1
	bld, err := builder.BuildVersion(ctx, act.ID, ver.ID)
	if err != nil {
		t.Fatalf("build version: %v", err)
	}
	if bld.Status != domain.BuildStatusSucceeded {
		t.Fatalf("expected build success, got %s: stderr=%s", bld.Status, bld.Stderr)
	}
	if bld.ToolchainVersion != "go1.27.3" {
		t.Fatalf("expected toolchain go1.27.3, got %s", bld.ToolchainVersion)
	}
	if bld.ArtifactDigest == "" || bld.ArtifactSize == 0 {
		t.Fatalf("expected artifact digest and size, got digest=%s size=%d", bld.ArtifactDigest, bld.ArtifactSize)
	}

	// 4. Activate Build
	if err := actSvc.ActivateBuild(ctx, act.ID, ver.ID, bld.ID); err != nil {
		t.Fatalf("activate build: %v", err)
	}

	updatedAct, err := actSvc.GetAction(ctx, act.ID)
	if err != nil {
		t.Fatalf("get action: %v", err)
	}
	if updatedAct.ActiveVersionID != ver.ID || updatedAct.ActiveBuildID != bld.ID {
		t.Fatalf("mismatched active IDs: ver=%s, bld=%s", updatedAct.ActiveVersionID, updatedAct.ActiveBuildID)
	}

	// 5. Verify build status
	if bld.Status != domain.BuildStatusSucceeded {
		t.Fatalf("expected build to succeed")
	}
}

func TestBuildLifecycle_FailureIsolation(t *testing.T) {
	ctx := context.Background()
	_, _, sb, actSvc, builder := setupTestEnvironment(t)

	// 1. Create Action and initial good build
	act, _ := actSvc.CreateAction(ctx, action.CreateActionRequest{Name: "Stable Action"})
	ver1, _ := actSvc.CreateVersion(ctx, act.ID, action.CreateVersionRequest{
		Files: map[string][]byte{"main.go": []byte("good code")},
	})
	bld1, _ := builder.BuildVersion(ctx, act.ID, ver1.ID)
	_ = actSvc.ActivateBuild(ctx, act.ID, ver1.ID, bld1.ID)

	// 2. Create Version 2 with failing build command
	ver2, _ := actSvc.CreateVersion(ctx, act.ID, action.CreateVersionRequest{
		Files: map[string][]byte{"main.go": []byte("faulty code")},
		BuildSpec: domain.BuildSpec{
			Command: "go build -faulty-flag",
		},
	})

	// Make fake backend simulate build failure for faulty command
	exitOne := 1
	sb.CustomExec = func(req sandbox.ExecRequest, session *sandbox.FakeSession) (sandbox.ExecResult, error) {
		if strings.Contains(req.Command, "-faulty-flag") {
			return sandbox.ExecResult{
				Stderr:   "flag provided but not defined: -faulty-flag\n",
				ExitCode: &exitOne,
			}, nil
		}
		zero := 0
		return sandbox.ExecResult{Stdout: "ok", ExitCode: &zero}, nil
	}

	bld2, err := builder.BuildVersion(ctx, act.ID, ver2.ID)
	if err != nil {
		t.Fatalf("unexpected builder error: %v", err)
	}
	if bld2.Status != domain.BuildStatusFailed {
		t.Fatalf("expected build2 to fail, got %s", bld2.Status)
	}

	// 3. CRITICAL INVARIANT: Active Build and Active Version MUST remain bld1 & ver1!
	actAfter, _ := actSvc.GetAction(ctx, act.ID)
	if actAfter.ActiveVersionID != ver1.ID || actAfter.ActiveBuildID != bld1.ID {
		t.Fatalf("failure corrupted active build! got ver=%s, bld=%s", actAfter.ActiveVersionID, actAfter.ActiveBuildID)
	}

	// 4. Activating a failed build MUST be rejected
	err = actSvc.ActivateBuild(ctx, act.ID, ver2.ID, bld2.ID)
	if err == nil {
		t.Fatal("expected activating failed build to error, got nil")
	}
}

func TestBuildLifecycle_ToolchainRebuildAndWarning(t *testing.T) {
	ctx := context.Background()
	_, _, sb, actSvc, builder := setupTestEnvironment(t)

	// Baseline starts at go1.25.6
	sb.SetBaseline(sandbox.ProfileGoBuilder, "go1.25.6")

	act, _ := actSvc.CreateAction(ctx, action.CreateActionRequest{Name: "Rebuild Action"})
	ver, _ := actSvc.CreateVersion(ctx, act.ID, action.CreateVersionRequest{
		Files: map[string][]byte{"main.go": []byte("code")},
		BuildSpec: domain.BuildSpec{
			ToolchainRequirement: ">= 1.25",
		},
	})

	bld1, _ := builder.BuildVersion(ctx, act.ID, ver.ID)
	if bld1.ToolchainVersion != "go1.25.6" {
		t.Fatalf("expected go1.25.6, got %s", bld1.ToolchainVersion)
	}
	_ = actSvc.ActivateBuild(ctx, act.ID, ver.ID, bld1.ID)

	// Toolchain check when baseline == built_with -> No warning
	tcCheck1, err := actSvc.CheckToolchainOutdated(ctx, act.ID)
	if err != nil {
		t.Fatalf("check toolchain: %v", err)
	}
	if tcCheck1.Outdated {
		t.Fatal("expected not outdated when toolchains match")
	}

	// Baseline is upgraded to go1.27.3
	sb.SetBaseline(sandbox.ProfileGoBuilder, "go1.27.3")

	// Now toolchain check MUST flag as outdated and recommend rebuild
	tcCheck2, err := actSvc.CheckToolchainOutdated(ctx, act.ID)
	if err != nil {
		t.Fatalf("check toolchain: %v", err)
	}
	if !tcCheck2.Outdated || !tcCheck2.RebuildRecommended {
		t.Fatal("expected outdated and rebuild recommended")
	}
	if tcCheck2.ActiveBuiltWith != "go1.25.6" || tcCheck2.CurrentBaseline != "go1.27.3" {
		t.Fatalf("unexpected versions: built_with=%s, baseline=%s", tcCheck2.ActiveBuiltWith, tcCheck2.CurrentBaseline)
	}
	if !strings.Contains(tcCheck2.WarningMessage, "go1.27.3") {
		t.Fatalf("expected warning to mention go1.27.3, got %s", tcCheck2.WarningMessage)
	}

	// Rebuild the SAME Version under new toolchain
	bld2, err := builder.BuildVersion(ctx, act.ID, ver.ID)
	if err != nil {
		t.Fatalf("rebuild version: %v", err)
	}
	if bld2.BuildNumber != 2 {
		t.Fatalf("expected build number 2, got %d", bld2.BuildNumber)
	}
	if bld2.ToolchainVersion != "go1.27.3" {
		t.Fatalf("expected new build toolchain go1.27.3, got %s", bld2.ToolchainVersion)
	}

	// Activate new build
	_ = actSvc.ActivateBuild(ctx, act.ID, ver.ID, bld2.ID)

	// Warning should now be cleared
	tcCheck3, _ := actSvc.CheckToolchainOutdated(ctx, act.ID)
	if tcCheck3.Outdated {
		t.Fatal("expected warning to be cleared after rebuilding and activating")
	}
}

func TestInjectSDK(t *testing.T) {
	t.Run("auto injects sdk when no go.mod provided", func(t *testing.T) {
		src := map[string][]byte{
			"main.go": []byte("package main\nimport \"actionscat/pkg/actionscat\"\nfunc main() {}\n"),
		}
		injected := InjectSDK(src)

		if _, ok := injected["_sdk/go.mod"]; !ok {
			t.Fatal("expected _sdk/go.mod to be injected")
		}
		if _, ok := injected["_sdk/pkg/actionscat/sdk.go"]; !ok {
			t.Fatal("expected _sdk/pkg/actionscat/sdk.go to be injected")
		}
		if modBytes, ok := injected["go.mod"]; !ok || !strings.Contains(string(modBytes), "replace actionscat => ./_sdk") {
			t.Fatalf("expected go.mod with replace directive, got: %s", string(modBytes))
		}
	})

	t.Run("appends replace directive to existing go.mod", func(t *testing.T) {
		src := map[string][]byte{
			"main.go": []byte("package main\nfunc main() {}\n"),
			"go.mod":  []byte("module my_action\n\ngo 1.25.3\n"),
		}
		injected := InjectSDK(src)

		modContent := string(injected["go.mod"])
		if !strings.Contains(modContent, "module my_action") {
			t.Fatal("expected user module to be preserved")
		}
		if !strings.Contains(modContent, "replace actionscat => ./_sdk") {
			t.Fatalf("expected replace directive appended, got: %s", modContent)
		}
	})
}

