package api

import (
	"actionscat/internal/action"
	"actionscat/internal/build"
	"actionscat/internal/domain"
	"actionscat/internal/runner"
	"actionscat/internal/scheduler"
	"actionscat/internal/store"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type ManagementAPI struct {
	store     *store.SQLiteStore
	actionSvc *action.Service
	builder   *build.Builder
	runner    *runner.Runner
	scheduler *scheduler.Scheduler
}

func NewManagementAPI(
	store *store.SQLiteStore,
	actionSvc *action.Service,
	builder *build.Builder,
	runner *runner.Runner,
	scheduler *scheduler.Scheduler,
) *ManagementAPI {
	return &ManagementAPI{
		store:     store,
		actionSvc: actionSvc,
		builder:   builder,
		runner:    runner,
		scheduler: scheduler,
	}
}

// RegisterRoutes registers all management endpoints on a Gin router group.
func (m *ManagementAPI) RegisterRoutes(rg *gin.RouterGroup) {
	// Actions
	rg.POST("/actions", m.CreateAction)
	rg.GET("/actions", m.ListActions)
	rg.GET("/actions/:id", m.GetAction)
	rg.PUT("/actions/:id", m.UpdateAction)
	rg.DELETE("/actions/:id", m.DeleteAction)

	// Versions
	rg.POST("/actions/:id/versions", m.CreateVersion)
	rg.GET("/actions/:id/versions", m.ListVersions)
	rg.GET("/actions/:id/versions/:vid", m.GetVersion)

	// Builds
	rg.POST("/actions/:id/versions/:vid/builds", m.CreateBuild)
	rg.GET("/actions/:id/builds", m.ListBuilds)
	rg.GET("/actions/:id/builds/:bid", m.GetBuild)
	rg.GET("/actions/:id/builds/:bid/logs", m.GetBuildLogs)
	rg.POST("/actions/:id/active-build", m.SetActiveBuild)
	rg.GET("/actions/:id/toolchain-check", m.ToolchainCheck)

	// Runs
	rg.POST("/actions/:id/runs", m.CreateRun)
	rg.GET("/actions/:id/runs", m.ListRuns)
	rg.GET("/actions/:id/runs/:rid", m.GetRun)
	rg.GET("/actions/:id/runs/:rid/logs", m.GetRunLogs)

	// Triggers: Schedules
	rg.POST("/actions/:id/schedules", m.CreateSchedule)
	rg.GET("/actions/:id/schedules", m.ListSchedules)
	rg.DELETE("/schedules/:id", m.DeleteSchedule)

	// Triggers: Matchers
	rg.POST("/actions/:id/matchers", m.CreateMatcher)
	rg.GET("/actions/:id/matchers", m.ListMatchers)
	rg.DELETE("/matchers/:id", m.DeleteMatcher)
}

// ---------------------------------------------------------
// Actions Handlers
// ---------------------------------------------------------

type CreateActionReq struct {
	Name           string `json:"name" binding:"required"`
	Description    string `json:"description"`
	MaxConcurrency int    `json:"max_concurrency"`
}

func (m *ManagementAPI) CreateAction(c *gin.Context) {
	var req CreateActionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	act, err := m.actionSvc.CreateAction(c.Request.Context(), action.CreateActionRequest{
		Name:           req.Name,
		Description:    req.Description,
		MaxConcurrency: req.MaxConcurrency,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, act)
}

func (m *ManagementAPI) ListActions(c *gin.Context) {
	acts, err := m.actionSvc.ListActions(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, acts)
}

func (m *ManagementAPI) GetAction(c *gin.Context) {
	id := c.Param("id")
	act, err := m.actionSvc.GetAction(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "action not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, act)
}

type UpdateActionReq struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	MaxConcurrency int    `json:"max_concurrency"`
	Enabled        bool   `json:"enabled"`
}

func (m *ManagementAPI) UpdateAction(c *gin.Context) {
	id := c.Param("id")
	var req UpdateActionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	act, err := m.actionSvc.UpdateAction(c.Request.Context(), id, action.UpdateActionRequest{
		Name:           req.Name,
		Description:    req.Description,
		MaxConcurrency: req.MaxConcurrency,
		Enabled:        req.Enabled,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "action not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, act)
}

func (m *ManagementAPI) DeleteAction(c *gin.Context) {
	id := c.Param("id")
	err := m.actionSvc.DeleteAction(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "action not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---------------------------------------------------------
// Versions Handlers
// ---------------------------------------------------------

type CreateVersionReq struct {
	Files               map[string]string       `json:"files" binding:"required"`
	Encodings           map[string]string       `json:"encodings"`      // optional: filename -> "utf8"|"base64"
	FileEncodings       map[string]string       `json:"file_encodings"` // alias for encodings
	BuildSpec           domain.BuildSpec        `json:"build_spec"`
	RuntimeSpec         domain.RuntimeSpec      `json:"runtime_spec"`
	StateInjections     []domain.StateInjection `json:"state_injections"`
	RuntimeCapabilities []string                `json:"runtime_capabilities"`
}

func (m *ManagementAPI) CreateVersion(c *gin.Context) {
	actionID := c.Param("id")
	var req CreateVersionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	encodings := req.Encodings
	if encodings == nil {
		encodings = req.FileEncodings
	}

	fileMap := make(map[string][]byte, len(req.Files))
	for name, content := range req.Files {
		enc := "utf8"
		if encodings != nil {
			if specified, ok := encodings[name]; ok && specified != "" {
				enc = strings.ToLower(strings.TrimSpace(specified))
			}
		}

		switch enc {
		case "utf8", "utf-8", "text", "plain":
			fileMap[name] = []byte(content)
		case "base64", "b64":
			decoded, err := base64.StdEncoding.DecodeString(content)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid base64 content for file %q: %v", name, err)})
				return
			}
			fileMap[name] = decoded
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unsupported encoding %q for file %q (must be 'utf8' or 'base64')", enc, name)})
			return
		}
	}

	ver, err := m.actionSvc.CreateVersion(c.Request.Context(), actionID, action.CreateVersionRequest{
		Files:               fileMap,
		BuildSpec:           req.BuildSpec,
		RuntimeSpec:         req.RuntimeSpec,
		StateInjections:     req.StateInjections,
		RuntimeCapabilities: req.RuntimeCapabilities,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, ver)
}

func (m *ManagementAPI) ListVersions(c *gin.Context) {
	actionID := c.Param("id")
	vers, err := m.actionSvc.ListVersions(c.Request.Context(), actionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, vers)
}

func (m *ManagementAPI) GetVersion(c *gin.Context) {
	vid := c.Param("vid")
	ver, err := m.actionSvc.GetVersion(c.Request.Context(), vid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "version not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, ver)
}

// ---------------------------------------------------------
// Builds Handlers
// ---------------------------------------------------------

func (m *ManagementAPI) CreateBuild(c *gin.Context) {
	actionID := c.Param("id")
	vid := c.Param("vid")
	bld, err := m.builder.BuildVersion(c.Request.Context(), actionID, vid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, bld)
}

func (m *ManagementAPI) ListBuilds(c *gin.Context) {
	actionID := c.Param("id")
	builds, err := m.store.ListBuildsForAction(c.Request.Context(), actionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, builds)
}

func (m *ManagementAPI) GetBuild(c *gin.Context) {
	bid := c.Param("bid")
	bld, err := m.store.GetBuild(c.Request.Context(), bid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "build not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, bld)
}

func (m *ManagementAPI) GetBuildLogs(c *gin.Context) {
	bid := c.Param("bid")
	bld, err := m.store.GetBuild(c.Request.Context(), bid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "build not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"stdout": bld.Stdout,
		"stderr": bld.Stderr,
	})
}

type SetActiveBuildReq struct {
	VersionID string `json:"version_id" binding:"required"`
	BuildID   string `json:"build_id" binding:"required"`
}

func (m *ManagementAPI) SetActiveBuild(c *gin.Context) {
	actionID := c.Param("id")
	var req SetActiveBuildReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	err := m.actionSvc.ActivateBuild(c.Request.Context(), actionID, req.VersionID, req.BuildID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (m *ManagementAPI) ToolchainCheck(c *gin.Context) {
	actionID := c.Param("id")
	res, err := m.actionSvc.CheckToolchainOutdated(c.Request.Context(), actionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

// ---------------------------------------------------------
// Runs Handlers
// ---------------------------------------------------------

type ManualRunReq struct {
	ExtraEnv        map[string]string `json:"extra_env"`
	TriggerMetadata map[string]string `json:"trigger_metadata"`
}

func (m *ManagementAPI) CreateRun(c *gin.Context) {
	actionID := c.Param("id")
	var req ManualRunReq
	_ = c.ShouldBindJSON(&req)

	run, err := m.runner.CreateRun(c.Request.Context(), runner.CreateRunRequest{
		ActionID:        actionID,
		TriggerType:     domain.TriggerTypeManual,
		TriggerMetadata: req.TriggerMetadata,
		ExtraEnv:        req.ExtraEnv,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, run)
}

func (m *ManagementAPI) ListRuns(c *gin.Context) {
	actionID := c.Param("id")
	limit := 50
	offset := 0
	if l := c.Query("limit"); l != "" {
		if val, err := strconv.Atoi(l); err == nil && val > 0 {
			limit = val
		}
	}
	if o := c.Query("offset"); o != "" {
		if val, err := strconv.Atoi(o); err == nil && val >= 0 {
			offset = val
		}
	}

	runs, err := m.store.ListRuns(c.Request.Context(), actionID, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, runs)
}

func (m *ManagementAPI) GetRun(c *gin.Context) {
	rid := c.Param("rid")
	run, err := m.store.GetRun(c.Request.Context(), rid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, run)
}

func (m *ManagementAPI) GetRunLogs(c *gin.Context) {
	rid := c.Param("rid")
	run, err := m.store.GetRun(c.Request.Context(), rid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"stdout": run.Stdout,
		"stderr": run.Stderr,
	})
}

// ---------------------------------------------------------
// Schedules Handlers
// ---------------------------------------------------------

type CreateScheduleReq struct {
	CronExpr string `json:"cron_expr" binding:"required"`
	Timezone string `json:"timezone"`
	Enabled  bool   `json:"enabled"`
}

func (m *ManagementAPI) CreateSchedule(c *gin.Context) {
	actionID := c.Param("id")
	var req CreateScheduleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	now := time.Now().UTC()
	nextRun, err := m.scheduler.CalculateNextRun(req.CronExpr, req.Timezone, now)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cron or timezone: " + err.Error()})
		return
	}

	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schedID := fmt.Sprintf("sched_%s", hex.EncodeToString(b))

	sched := &domain.Schedule{
		ID:        schedID,
		ActionID:  actionID,
		CronExpr:  req.CronExpr,
		Timezone:  req.Timezone,
		NextRunAt: nextRun,
		Enabled:   req.Enabled,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := m.store.CreateSchedule(c.Request.Context(), sched); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, sched)
}

func (m *ManagementAPI) ListSchedules(c *gin.Context) {
	actionID := c.Param("id")
	list, err := m.store.ListSchedules(c.Request.Context(), actionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, list)
}

func (m *ManagementAPI) DeleteSchedule(c *gin.Context) {
	id := c.Param("id")
	err := m.store.DeleteSchedule(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "schedule not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---------------------------------------------------------
// Matchers Handlers
// ---------------------------------------------------------

type CreateMatcherReq struct {
	Name             string            `json:"name" binding:"required"`
	MatchType        string            `json:"match_type" binding:"required"`
	Pattern          string            `json:"pattern" binding:"required"`
	TargetField      string            `json:"target_field"`
	CaptureEnvMap    map[string]string `json:"capture_env_map"`
	Priority         int               `json:"priority"`
	ContinueMatching bool              `json:"continue_matching"`
	Enabled          bool              `json:"enabled"`
}

func (m *ManagementAPI) CreateMatcher(c *gin.Context) {
	actionID := c.Param("id")
	var req CreateMatcherReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	target := req.TargetField
	if target == "" {
		target = "text"
	}

	b := make([]byte, 6)
	_, _ = rand.Read(b)
	matcherID := fmt.Sprintf("m_%s", hex.EncodeToString(b))
	now := time.Now().UTC()

	matcher := &domain.Matcher{
		ID:               matcherID,
		ActionID:         actionID,
		Name:             req.Name,
		MatchType:        domain.MatchType(strings.ToLower(req.MatchType)),
		Pattern:          req.Pattern,
		TargetField:      target,
		CaptureEnvMap:    req.CaptureEnvMap,
		Priority:         req.Priority,
		ContinueMatching: req.ContinueMatching,
		Enabled:          req.Enabled,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := m.store.CreateMatcher(c.Request.Context(), matcher); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, matcher)
}

func (m *ManagementAPI) ListMatchers(c *gin.Context) {
	actionID := c.Param("id")
	list, err := m.store.ListMatchers(c.Request.Context(), actionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, list)
}

func (m *ManagementAPI) DeleteMatcher(c *gin.Context) {
	id := c.Param("id")
	err := m.store.DeleteMatcher(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "matcher not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
