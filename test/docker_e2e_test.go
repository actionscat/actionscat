package test

import (
	"actionscat/internal/build"
	"actionscat/internal/domain"
	"actionscat/internal/frostagent"
	"actionscat/internal/runner"
	"actionscat/internal/runtime"
	"actionscat/internal/sandbox"
	"actionscat/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestRealDockerE2EFullChain runs the complete, unmocked end-to-end integration test:
// ActionsCat Core
// → real go-builder Worker (Go 1.25.3 container)
// → actual go build (hermetic SDK replace + entrypoint compilation)
// → real action-runtime Worker container
// → SDK WriteState & Reply under network: none
// → Gateway callback proxy
// → ActionsCat Core Runtime API
// → FrostAgent messages.Service
func TestRealDockerE2EFullChain(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	gatewayURL := os.Getenv("FA_SANDBOX_ENDPOINT")
	if gatewayURL == "" {
		gatewayURL = "http://127.0.0.1:3874"
	}
	authToken := os.Getenv("FA_SANDBOX_AUTH_TOKEN")
	if authToken == "" {
		authToken = "e2e-secret-token"
	}

	sbClient := sandbox.NewClient(sandbox.Config{
		BaseURL:          gatewayURL,
		AuthToken:        authToken,
		SessionNamespace: "docker-e2e",
		ClientTimeout:    120 * time.Second,
	})

	// 1. Check if real Docker Gateway is reachable
	if err := sbClient.Health(ctx); err != nil {
		t.Skipf("Skipping real Docker E2E test: Gateway at %s unavailable: %v", gatewayURL, err)
	}

	tempDir := t.TempDir()
	db, err := store.OpenDB(filepath.Join(tempDir, "e2e_core.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	st := store.NewSQLiteStore(db)
	fs := store.NewFileStore(tempDir)
	ss := store.NewStateStore(tempDir)

	// 2. Mock FrostAgent messages.Service
	type receivedMsg struct {
		Req frostagent.SendMessageRequest
	}
	var (
		faMu       sync.Mutex
		faMessages []receivedMsg
	)

	faListener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen fa: %v", err)
	}
	defer faListener.Close()
	faPort := faListener.Addr().(*net.TCPAddr).Port

	faServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var req frostagent.SendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			faMu.Lock()
			faMessages = append(faMessages, receivedMsg{Req: req})
			faMu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok","delivered":true}`))
		}),
	}
	go func() {
		_ = faServer.Serve(faListener)
	}()
	defer faServer.Close()

	faClient := frostagent.NewHTTPClient(frostagent.Config{
		BaseURL:      fmt.Sprintf("http://127.0.0.1:%d", faPort),
		SendEndpoint: "/api/v1/messages/send",
	})

	// 3. Start ActionsCat Core Runtime API
	rtAPI := runtime.NewAPI(st, ss, faClient)
	ginEngine := gin.New()
	ginEngine.Use(gin.Recovery())
	rtAPI.RegisterRoutes(ginEngine.Group("/api/v1/runtime"))

	rtListener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen runtime API: %v", err)
	}
	defer rtListener.Close()
	rtPort := rtListener.Addr().(*net.TCPAddr).Port

	rtServer := &http.Server{
		Handler: ginEngine,
	}
	go func() {
		_ = rtServer.Serve(rtListener)
	}()
	defer rtServer.Close()

	advertisedRuntimeURL := fmt.Sprintf("http://host.docker.internal:%d/api/v1/runtime", rtPort)
	t.Logf("Core Runtime API listening on port %d, advertising %s to containers", rtPort, advertisedRuntimeURL)

	// 4. Create Action & Version with network: none and runtime capabilities
	actionID := "act_docker_e2e"
	now := time.Now().UTC()
	actRecord := &domain.Action{
		ID:             actionID,
		Name:           "Docker E2E Action",
		Description:    "Real unmocked container compilation and execution test",
		Enabled:        true,
		MaxConcurrency: 2,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := st.CreateAction(ctx, actRecord); err != nil {
		t.Fatalf("create action: %v", err)
	}

	versionID := "ver_docker_e2e"
	// Store source bundle containing real Go Action code with embedded SDK
	// Note: user go.mod deliberately includes replace actionscat => ../../../
	// to verify hermetic SDK replace override!
	goModContent := `module dockere2e

go 1.25.3

require actionscat v0.0.0

replace actionscat => ../../../
`

	mainGoContent := `package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"actionscat/pkg/actionscat"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Invariant 1: network:none must strictly block public internet access
	netClient := &http.Client{Timeout: 2 * time.Second}
	resp, err := netClient.Get("https://1.1.1.1")
	if err == nil {
		resp.Body.Close()
		panic("CRITICAL FAILURE: public internet access succeeded under network:none")
	}
	fmt.Println("E2E_INVARIANT: public internet blocked successfully")

	// Invariant 2: state.write via Gateway callback proxy -> Core Runtime API -> StateStore
	stateContent := []byte(` + "`" + `{"docker_e2e_verified":true,"ts":"2026-09-15"}` + "`" + `)
	if err := actionscat.WriteState(ctx, "docker_e2e_summary.json", stateContent); err != nil {
		panic(fmt.Sprintf("WriteState failed: %v", err))
	}
	fmt.Println("E2E_INVARIANT: state.write succeeded via callback proxy")

	// Invariant 3: Reply via Gateway callback proxy -> Core Runtime API -> FrostAgent
	if err := actionscat.Reply(ctx, "E2E verification message delivered from isolated container!"); err != nil {
		panic(fmt.Sprintf("Reply failed: %v", err))
	}
	fmt.Println("E2E_INVARIANT: reply delivered via callback proxy")
}
`

	sourceFiles := map[string][]byte{
		"go.mod":  []byte(goModContent),
		"main.go": []byte(mainGoContent),
	}
	digest, path, err := fs.SaveSourceBundle(actionID, versionID, sourceFiles)
	if err != nil {
		t.Fatalf("save source bundle: %v", err)
	}

	verRecord := &domain.ActionVersion{
		ID:            versionID,
		ActionID:      actionID,
		VersionNumber: 1,
		SourceDigest:  digest,
		SourcePath:    path,
		BuildSpec: domain.BuildSpec{
			Command: "go build -o /sandbox/out/entrypoint .",
			Network: false,
		},
		RuntimeSpec: domain.RuntimeSpec{
			Entrypoint:     "entrypoint",
			TimeoutSeconds: 60,
			Network:        domain.NetworkPolicy{Mode: domain.NetworkModeNone},
		},
		RuntimeCapabilities: []string{
			domain.ScopeStateWrite,
			domain.ScopeFrostAgentSendMsg,
		},
		CreatedAt: now,
	}
	if err := st.CreateVersion(ctx, verRecord); err != nil {
		t.Fatalf("create version: %v", err)
	}

	// 5. Real Compilation: go-builder Worker
	t.Log("===> Step 1: Real Go Builder compilation in Docker Worker container...")
	builder := build.NewBuilder(st, fs, sbClient)
	buildResult, err := builder.BuildVersion(ctx, actionID, versionID)
	if err != nil {
		t.Fatalf("build version failed: %v\nStdout:\n%s\nStderr:\n%s", err, buildResult.Stdout, buildResult.Stderr)
	}
	if buildResult.Status != domain.BuildStatusSucceeded {
		t.Fatalf("build failed with status %s\nStdout:\n%s\nStderr:\n%s", buildResult.Status, buildResult.Stdout, buildResult.Stderr)
	}
	t.Logf("Build succeeded! Toolchain version: %s", buildResult.ToolchainVersion)

	// Verify compiled artifact exists in FileStore
	artifacts, err := fs.ReadArtifactBundle(actionID, versionID, buildResult.ID)
	if err != nil || len(artifacts["entrypoint"]) == 0 {
		t.Fatalf("artifact entrypoint missing in filestore: %v", err)
	}
	t.Logf("Artifact bundle verified (entrypoint size: %d bytes)", len(artifacts["entrypoint"]))

	// Activate version and build on action so runner can schedule it
	actRecord.ActiveVersionID = versionID
	actRecord.ActiveBuildID = buildResult.ID
	if err := st.UpdateAction(ctx, actRecord); err != nil {
		t.Fatalf("update action: %v", err)
	}

	// 6. Real Execution: action-runtime Worker
	t.Log("===> Step 2: Real Action execution in action-runtime Docker Worker container under network: none...")
	r := runner.NewRunner(st, fs, ss, sbClient, runner.Config{
		PollInterval:              50 * time.Millisecond,
		RuntimeEndpoint:           fmt.Sprintf("http://127.0.0.1:%d/api/v1/runtime", rtPort),
		AdvertisedRuntimeEndpoint: advertisedRuntimeURL,
	})
	r.Start(ctx)
	defer r.Stop()

	plannedEnv := map[string]string{
		"ACTIONSCAT_EVENT_PLATFORM":   "qq",
		"ACTIONSCAT_EVENT_GROUP_ID":   "889900",
		"ACTIONSCAT_EVENT_USER_ID":     "556677",
		"ACTIONSCAT_EVENT_SESSION_ID": "sess_qq_group_889900",
	}

	runRecord, err := r.CreateRun(ctx, runner.CreateRunRequest{
		ActionID:    actionID,
		TriggerType: domain.TriggerTypeMatcher,
		ExtraEnv:    plannedEnv,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	t.Logf("Created run %s", runRecord.ID)

	// Wait for run completion
	var completedRun *domain.Run
	for range 60 {
		time.Sleep(500 * time.Millisecond)
		checkRun, err := st.GetRun(ctx, runRecord.ID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		if checkRun.Status == domain.RunStatusSucceeded || checkRun.Status == domain.RunStatusFailed || checkRun.Status == domain.RunStatusTimedOut {
			completedRun = checkRun
			break
		}
	}

	if completedRun == nil {
		t.Fatalf("timed out waiting for run %s to complete", runRecord.ID)
	}

	t.Logf("Run %s completed with status: %s, exit code: %v", completedRun.ID, completedRun.Status, completedRun.ExitCode)
	t.Logf("Run stdout:\n%s", completedRun.Stdout)
	if completedRun.Stderr != "" {
		t.Logf("Run stderr:\n%s", completedRun.Stderr)
	}

	// 7. Validate All Core Invariants
	if completedRun.Status != domain.RunStatusSucceeded {
		t.Fatalf("expected run status succeeded, got %s: stderr=%s", completedRun.Status, completedRun.Stderr)
	}
	if completedRun.ExitCode == nil || *completedRun.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %v", completedRun.ExitCode)
	}

	// Verify all stdout invariant markers were reached
	if !strings.Contains(completedRun.Stdout, "E2E_INVARIANT: public internet blocked successfully") {
		t.Errorf("missing public internet blocked confirmation in stdout")
	}
	if !strings.Contains(completedRun.Stdout, "E2E_INVARIANT: state.write succeeded via callback proxy") {
		t.Errorf("missing state.write confirmation in stdout")
	}
	if !strings.Contains(completedRun.Stdout, "E2E_INVARIANT: reply delivered via callback proxy") {
		t.Errorf("missing reply delivered confirmation in stdout")
	}

	// Verify StateStore has the persisted state file
	savedState, err := ss.ReadState(actionID, "docker_e2e_summary.json")
	if err != nil {
		t.Fatalf("failed to read persisted state from StateStore: %v", err)
	}
	if !strings.Contains(string(savedState), "docker_e2e_verified") {
		t.Fatalf("state content mismatch, got: %s", string(savedState))
	}
	t.Logf("StateStore content verified successfully: %s", string(savedState))

	// Verify FrostAgent received the Reply message
	faMu.Lock()
	msgCount := len(faMessages)
	var lastMsg frostagent.SendMessageRequest
	if msgCount > 0 {
		lastMsg = faMessages[msgCount-1].Req
	}
	faMu.Unlock()

	if msgCount == 0 {
		t.Fatalf("expected at least 1 message delivered to FrostAgent, got 0")
	}
	if lastMsg.Platform != "qq" {
		t.Errorf("expected platform qq, got %s", lastMsg.Platform)
	}
	if lastMsg.TargetID != "889900" {
		t.Errorf("expected target_id 889900, got %s", lastMsg.TargetID)
	}
	if lastMsg.MessageType != "group" {
		t.Errorf("expected message_type group, got %s", lastMsg.MessageType)
	}
	hasExpectedContent := strings.Contains(lastMsg.Content, "E2E verification message delivered from isolated container!")
	for _, m := range lastMsg.Messages {
		if strings.Contains(m.Text, "E2E verification message delivered from isolated container!") ||
			strings.Contains(m.Content, "E2E verification message delivered from isolated container!") {
			hasExpectedContent = true
		}
	}
	if !hasExpectedContent {
		t.Errorf("unexpected message content: Content=%q, Messages=%+v", lastMsg.Content, lastMsg.Messages)
	}
	t.Logf("FrostAgent received message verified: TargetID=%s, Platform=%s, Messages=%+v", lastMsg.TargetID, lastMsg.Platform, lastMsg.Messages)

	// Verify Capability Token Revocation
	tokens, err := st.ListTokensForRun(ctx, completedRun.ID)
	if err != nil {
		t.Fatalf("get tokens for run: %v", err)
	}
	if len(tokens) == 0 {
		t.Fatalf("expected at least 1 capability token to have been generated, found 0")
	}
	for _, tok := range tokens {
		if tok.RevokedAt == nil {
			t.Errorf("token %s was not revoked upon run completion", tok.ID)
		}
	}
	t.Logf("Capability token revocation verified: %d tokens verified revoked", len(tokens))
}
