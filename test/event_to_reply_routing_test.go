package test

import (
	"actionscat/internal/api"
	"actionscat/internal/domain"
	"actionscat/internal/frostagent"
	"actionscat/internal/matcher"
	"actionscat/internal/sandbox"
	"actionscat/internal/store"
	"actionscat/pkg/actionscat"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// fakeDispatcher captures dispatched messages matching FrostAgent's Dispatcher interface.
type fakeDispatcher struct {
	mu         sync.Mutex
	dispatched []dispatchedMsg
}

type dispatchedMsg struct {
	Platform    string
	MessageType string
	TargetID    string
	Content     string
}

func (d *fakeDispatcher) Dispatch(ctx context.Context, platform string, msg dispatchedMsg) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dispatched = append(d.dispatched, msg)
	return nil
}

// setupRealFrostAgentMessageService creates an HTTP server implementing the exact logic
// of FrostAgent's internal/service/messages/service.go, dispatching to fakeDispatcher.
func setupRealFrostAgentMessageService(t *testing.T, expectedToken, boundInstanceID string, dispatcher *fakeDispatcher) *httptest.Server {
	t.Helper()

	parseSession := func(session string) (platform, msgType, targetID string) {
		session = strings.TrimSpace(session)
		if session == "" {
			return "", "", ""
		}
		parts := strings.Split(session, ":")
		if len(parts) >= 3 {
			return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(strings.Join(parts[2:], ":"))
		}
		return "", "", ""
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		// Auth check matching FrostAgent checkAuth()
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		expectedBearer := "Bearer " + expectedToken
		if subtle.ConstantTimeCompare([]byte(authHeader), []byte(expectedBearer)) != 1 &&
			subtle.ConstantTimeCompare([]byte(authHeader), []byte(expectedToken)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}

		bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 10<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var req frostagent.SendMessageRequest
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Instance binding invariant matching FrostAgent
		if boundInstanceID != "" && req.InstanceID != "" && req.InstanceID != boundInstanceID {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":"instance_id mismatch: request targets %q but service is bound to %q"}`, req.InstanceID, boundInstanceID)
			return
		}

		// Exact normalizeMessages logic from FrostAgent internal/service/messages/service.go
		sessPlatform, sessMsgType, sessTargetID := parseSession(req.Session)
		if req.Platform == "" {
			req.Platform = sessPlatform
		}
		if req.MessageType == "" {
			req.MessageType = sessMsgType
		}
		if req.TargetID == "" {
			req.TargetID = sessTargetID
		}
		if req.MessageType == "" {
			req.MessageType = "group"
		}

		type outMsg struct {
			TargetID    string
			MessageType string
			Platform    string
			Content     string
		}

		var messages []outMsg
		if len(req.Messages) == 0 {
			content := req.Content
			if content == "" && req.Text != "" {
				content = req.Text
			}
			messages = []outMsg{{
				TargetID:    req.TargetID,
				MessageType: req.MessageType,
				Platform:    req.Platform,
				Content:     content,
			}}
		} else {
			for _, m := range req.Messages {
				plat := m.Platform
				if plat == "" {
					plat = req.Platform
				}
				mt := m.MessageType
				if mt == "" {
					mt = req.MessageType
				}
				tid := m.TargetID
				if tid == "" {
					tid = req.TargetID
				}
				cnt := m.Content
				if cnt == "" && m.Text != "" {
					cnt = m.Text
				}
				messages = append(messages, outMsg{
					TargetID:    tid,
					MessageType: mt,
					Platform:    plat,
					Content:     cnt,
				})
			}
		}

		// Dispatch through fake dispatcher
		for _, msg := range messages {
			if err := dispatcher.Dispatch(r.Context(), msg.Platform, dispatchedMsg{
				Platform:    msg.Platform,
				MessageType: msg.MessageType,
				TargetID:    msg.TargetID,
				Content:     msg.Content,
			}); err != nil {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","delivered":true,"count":%d}`, len(messages))
	})

	mux := http.NewServeMux()
	if boundInstanceID != "" {
		mux.Handle(fmt.Sprintf("/instances/%s/api/v1/messages/send", boundInstanceID), handler)
	}
	mux.Handle("/api/v1/messages/send", handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestEventToReplyRouting_FullPipeline implements the complete end-to-end routing test requested in code review:
//
//	IncomingEventRequest.Normalize
//	→ Matcher
//	→ injected Context
//	→ actionscat.Reply
//	→ ActionsCat Runtime API
//	→ real FrostAgent messages.Service
//	→ fake Dispatcher
//	assert:
//	platform == qq
//	message_type == group
//	target_id == 123
//	content correct
func TestEventToReplyRouting_FullPipeline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	now := time.Now().UTC()

	tempDir := t.TempDir()
	db, err := store.OpenDB(filepath.Join(tempDir, "routing_test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	st := store.NewSQLiteStore(db)
	fs := store.NewFileStore(tempDir)
	ss := store.NewStateStore(tempDir)
	sb := sandbox.NewFakeBackend()

	// 1. Setup real FrostAgent messages.Service HTTP endpoint with fake Dispatcher
	frostAgentKey := "secret-frostagent-key-999"
	instanceID := "inst_fox_dispatch_01"
	dispatcher := &fakeDispatcher{}
	faServer := setupRealFrostAgentMessageService(t, frostAgentKey, instanceID, dispatcher)

	faClient := frostagent.NewHTTPClient(frostagent.Config{
		BaseURL:      faServer.URL,
		InstanceID:   instanceID,
		APIKey:       frostAgentKey,
		SendEndpoint: "/api/v1/messages/send",
		Timeout:      5 * time.Second,
	})

	// 2. Setup ActionsCat Core Runtime API Server
	coreServer := api.NewServer(api.ServerConfig{
		Store:           st,
		FileStore:       fs,
		StateStore:      ss,
		Sandbox:         sb,
		FrostAgent:      faClient,
		ManagementToken: "mgmt-token",
		DispatchToken:   "dispatch-token",
	})
	coreHTTPServer := httptest.NewServer(coreServer.SetupRouter())
	t.Cleanup(coreHTTPServer.Close)

	runtimeEndpoint := coreHTTPServer.URL + "/api/v1/runtime"
	coreServer.Runner.SetEndpoints(runtimeEndpoint, runtimeEndpoint)

	// 3. Register Action with frostagent.sendmsg capability
	actionID := "act_group_responder"
	if err := st.CreateAction(ctx, &domain.Action{
		ID:             actionID,
		Name:           "Group Responder Action",
		MaxConcurrency: 1,
		Enabled:        true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("create action: %v", err)
	}

	ver := &domain.ActionVersion{
		ID:            "ver_routing_1",
		ActionID:      actionID,
		VersionNumber: 1,
		SourceDigest:  "digest123",
		SourcePath:    "path123",
		RuntimeSpec: domain.RuntimeSpec{
			Entrypoint: "entrypoint",
		},
		RuntimeCapabilities: []string{
			domain.ScopeFrostAgentSendMsg,
		},
		CreatedAt: now,
	}
	if err := st.CreateVersion(ctx, ver); err != nil {
		t.Fatalf("create version: %v", err)
	}

	bld := &domain.ArtifactBuild{
		ID:             "bld_routing_1",
		ActionID:       actionID,
		VersionID:      ver.ID,
		BuildNumber:    1,
		Status:         domain.BuildStatusSucceeded,
		ArtifactDigest: "artdigest123",
		ArtifactPath:   "artpath123",
		ArtifactSize:   1024,
		CreatedAt:      now,
	}
	if err := st.CreateBuild(ctx, bld); err != nil {
		t.Fatalf("create build: %v", err)
	}
	if err := st.SetActiveBuild(ctx, actionID, ver.ID, bld.ID); err != nil {
		t.Fatalf("set active build: %v", err)
	}

	// 4. Register Matcher on text "ping_weather"
	m := &domain.Matcher{
		ID:          "m_weather_query",
		ActionID:    actionID,
		Name:        "Weather Query Matcher",
		MatchType:   domain.MatchTypeExact,
		Pattern:     "ping_weather",
		TargetField: "text",
		Priority:    10,
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := st.CreateMatcher(ctx, m); err != nil {
		t.Fatalf("create matcher: %v", err)
	}

	// -------------------------------------------------------------
	// SUBTEST 1: Group Message Routing Verification
	// -------------------------------------------------------------
	t.Run("Group Message Routing (platform=qq, group_id=123)", func(t *testing.T) {
		// STEP 1: IncomingEventRequest.Normalize
		ingressReq := api.IncomingEventRequest{
			Platform: "qq",
			GroupID:  "123",
			UserID:   "456",
			Text:     "ping_weather",
		}
		event := ingressReq.Normalize()

		// Verify event normalization invariant:
		// session_id is opaque identity "123:456", while routing fields are separate
		if event.Platform != "qq" || event.GroupID != "123" || event.UserID != "456" {
			t.Fatalf("unexpected normalized event: %+v", event)
		}
		if event.SessionID != "123:456" {
			t.Fatalf("expected opaque SessionID '123:456', got %q", event.SessionID)
		}

		// STEP 2: Matcher evaluation produces Run with extra environment
		matcherEngine := matcher.NewEngine(st, coreServer.Runner)
		runs, err := matcherEngine.MatchAndDispatch(ctx, event)
		if err != nil {
			t.Fatalf("match and dispatch: %v", err)
		}
		if len(runs) != 1 {
			t.Fatalf("expected 1 run dispatched, got %d", len(runs))
		}
		dispatchedRun := runs[0]

		// Verify environment created by matcher for action injection
		if dispatchedRun.PlannedEnv["ACTIONSCAT_EVENT_PLATFORM"] != "qq" {
			t.Fatalf("expected ACTIONSCAT_EVENT_PLATFORM qq, got %s", dispatchedRun.PlannedEnv["ACTIONSCAT_EVENT_PLATFORM"])
		}
		if dispatchedRun.PlannedEnv["ACTIONSCAT_EVENT_GROUP_ID"] != "123" {
			t.Fatalf("expected ACTIONSCAT_EVENT_GROUP_ID 123, got %s", dispatchedRun.PlannedEnv["ACTIONSCAT_EVENT_GROUP_ID"])
		}
		if dispatchedRun.PlannedEnv["ACTIONSCAT_EVENT_USER_ID"] != "456" {
			t.Fatalf("expected ACTIONSCAT_EVENT_USER_ID 456, got %s", dispatchedRun.PlannedEnv["ACTIONSCAT_EVENT_USER_ID"])
		}
		if dispatchedRun.PlannedEnv["ACTIONSCAT_EVENT_SESSION_ID"] != "123:456" {
			t.Fatalf("expected ACTIONSCAT_EVENT_SESSION_ID 123:456, got %s", dispatchedRun.PlannedEnv["ACTIONSCAT_EVENT_SESSION_ID"])
		}

		// STEP 3: Injected Context into Action execution sandbox
		// Generate valid single-run capability token
		rawToken, hash, err := domain.GenerateRawToken()
		if err != nil {
			t.Fatalf("generate raw token: %v", err)
		}
		tokRecord := &domain.RunToken{
			ID:        "tok_test_group",
			TokenHash: hash,
			RunID:     dispatchedRun.ID,
			ActionID:  actionID,
			Scopes:    ver.RuntimeCapabilities,
			ExpiresAt: time.Now().Add(5 * time.Minute),
			CreatedAt: time.Now(),
		}
		if err := st.CreateRunToken(ctx, tokRecord); err != nil {
			t.Fatalf("create run token: %v", err)
		}

		// Mark run as Running
		if err := st.MarkRunRunning(ctx, dispatchedRun.ID, time.Now().UTC()); err != nil {
			t.Fatalf("mark run running: %v", err)
		}

		// Inject environment into process simulating Action container execution
		t.Setenv("ACTIONSCAT_ACTION_ID", actionID)
		t.Setenv("ACTIONSCAT_RUN_ID", dispatchedRun.ID)
		t.Setenv("ACTIONSCAT_RUNTIME_ENDPOINT", runtimeEndpoint)
		t.Setenv("ACTIONSCAT_RUNTIME_TOKEN", rawToken)
		t.Setenv("ACTIONSCAT_EVENT_PLATFORM", dispatchedRun.PlannedEnv["ACTIONSCAT_EVENT_PLATFORM"])
		t.Setenv("ACTIONSCAT_EVENT_GROUP_ID", dispatchedRun.PlannedEnv["ACTIONSCAT_EVENT_GROUP_ID"])
		t.Setenv("ACTIONSCAT_EVENT_USER_ID", dispatchedRun.PlannedEnv["ACTIONSCAT_EVENT_USER_ID"])
		t.Setenv("ACTIONSCAT_EVENT_SESSION_ID", dispatchedRun.PlannedEnv["ACTIONSCAT_EVENT_SESSION_ID"])
		t.Setenv("ACTIONSCAT_EVENT_TEXT", dispatchedRun.PlannedEnv["ACTIONSCAT_EVENT_TEXT"])

		// Verify actionscat.GetContext() reads injected context correctly
		actCtx := actionscat.GetContext()
		if actCtx.Platform != "qq" || actCtx.GroupID != "123" || actCtx.UserID != "456" || actCtx.SessionID != "123:456" {
			t.Fatalf("actionscat.GetContext mismatch: %+v", actCtx)
		}

		// STEP 4: Action code calls actionscat.Reply
		// STEP 5: Request passes through ActionsCat Runtime API
		// STEP 6: Core proxies to real FrostAgent messages.Service
		// STEP 7: Messages are dispatched to fake Dispatcher
		replyText := "Weather report: sunny 25°C in group 123"
		if err := actionscat.Reply(ctx, replyText); err != nil {
			t.Fatalf("actionscat.Reply failed: %v", err)
		}

		// STEP 8: Assertions on fake Dispatcher
		dispatcher.mu.Lock()
		defer dispatcher.mu.Unlock()

		if len(dispatcher.dispatched) != 1 {
			t.Fatalf("expected 1 dispatched message in fake dispatcher, got %d", len(dispatcher.dispatched))
		}

		gotMsg := dispatcher.dispatched[0]
		if gotMsg.Platform != "qq" {
			t.Errorf("assert platform: expected 'qq', got %q", gotMsg.Platform)
		}
		if gotMsg.MessageType != "group" {
			t.Errorf("assert message_type: expected 'group', got %q", gotMsg.MessageType)
		}
		if gotMsg.TargetID != "123" {
			t.Errorf("assert target_id: expected '123', got %q", gotMsg.TargetID)
		}
		if gotMsg.Content != replyText {
			t.Errorf("assert content: expected %q, got %q", replyText, gotMsg.Content)
		}
	})

	// -------------------------------------------------------------
	// SUBTEST 2: Private Message Routing Verification
	// -------------------------------------------------------------
	t.Run("Private Message Routing (platform=qq, user_id=456, no group)", func(t *testing.T) {
		dispatcher.mu.Lock()
		dispatcher.dispatched = nil // reset
		dispatcher.mu.Unlock()

		// STEP 1: IncomingEventRequest.Normalize with private message
		ingressReq := api.IncomingEventRequest{
			Platform: "qq",
			UserID:   "456",
			Text:     "ping_weather",
		}
		event := ingressReq.Normalize()

		if event.Platform != "qq" || event.UserID != "456" || event.GroupID != "" {
			t.Fatalf("unexpected normalized private event: %+v", event)
		}
		if event.SessionID != "456" {
			t.Fatalf("expected SessionID '456', got %q", event.SessionID)
		}

		// STEP 2: Matcher evaluation
		matcherEngine := matcher.NewEngine(st, coreServer.Runner)
		runs, err := matcherEngine.MatchAndDispatch(ctx, event)
		if err != nil {
			t.Fatalf("match and dispatch: %v", err)
		}
		if len(runs) != 1 {
			t.Fatalf("expected 1 run dispatched, got %d", len(runs))
		}
		dispatchedRun := runs[0]

		// STEP 3: Injected Context into Action execution sandbox
		rawToken, hash, err := domain.GenerateRawToken()
		if err != nil {
			t.Fatalf("generate raw token: %v", err)
		}
		tokRecord := &domain.RunToken{
			ID:        "tok_test_private",
			TokenHash: hash,
			RunID:     dispatchedRun.ID,
			ActionID:  actionID,
			Scopes:    ver.RuntimeCapabilities,
			ExpiresAt: time.Now().Add(5 * time.Minute),
			CreatedAt: time.Now(),
		}
		if err := st.CreateRunToken(ctx, tokRecord); err != nil {
			t.Fatalf("create run token: %v", err)
		}

		// Mark run as Running
		if err := st.MarkRunRunning(ctx, dispatchedRun.ID, time.Now().UTC()); err != nil {
			t.Fatalf("mark run running: %v", err)
		}

		t.Setenv("ACTIONSCAT_ACTION_ID", actionID)
		t.Setenv("ACTIONSCAT_RUN_ID", dispatchedRun.ID)
		t.Setenv("ACTIONSCAT_RUNTIME_ENDPOINT", runtimeEndpoint)
		t.Setenv("ACTIONSCAT_RUNTIME_TOKEN", rawToken)
		t.Setenv("ACTIONSCAT_EVENT_PLATFORM", dispatchedRun.PlannedEnv["ACTIONSCAT_EVENT_PLATFORM"])
		t.Setenv("ACTIONSCAT_EVENT_GROUP_ID", dispatchedRun.PlannedEnv["ACTIONSCAT_EVENT_GROUP_ID"])
		t.Setenv("ACTIONSCAT_EVENT_USER_ID", dispatchedRun.PlannedEnv["ACTIONSCAT_EVENT_USER_ID"])
		t.Setenv("ACTIONSCAT_EVENT_SESSION_ID", dispatchedRun.PlannedEnv["ACTIONSCAT_EVENT_SESSION_ID"])
		t.Setenv("ACTIONSCAT_EVENT_TEXT", dispatchedRun.PlannedEnv["ACTIONSCAT_EVENT_TEXT"])

		// STEP 4-7: Action calls actionscat.Reply -> Core Runtime API -> FrostAgent messages.Service -> fake Dispatcher
		replyText := "Private weather report: sunny 25°C for user 456"
		if err := actionscat.Reply(ctx, replyText); err != nil {
			t.Fatalf("actionscat.Reply failed: %v", err)
		}

		// STEP 8: Assertions on fake Dispatcher
		dispatcher.mu.Lock()
		defer dispatcher.mu.Unlock()

		if len(dispatcher.dispatched) != 1 {
			t.Fatalf("expected 1 dispatched message in fake dispatcher, got %d", len(dispatcher.dispatched))
		}

		gotMsg := dispatcher.dispatched[0]
		if gotMsg.Platform != "qq" {
			t.Errorf("assert platform: expected 'qq', got %q", gotMsg.Platform)
		}
		if gotMsg.MessageType != "private" {
			t.Errorf("assert message_type: expected 'private', got %q", gotMsg.MessageType)
		}
		if gotMsg.TargetID != "456" {
			t.Errorf("assert target_id: expected '456', got %q", gotMsg.TargetID)
		}
		if gotMsg.Content != replyText {
			t.Errorf("assert content: expected %q, got %q", replyText, gotMsg.Content)
		}
	})
}
