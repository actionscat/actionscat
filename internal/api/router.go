package api

import (
	"actionscat/internal/action"
	"actionscat/internal/build"
	"actionscat/internal/frostagent"
	"actionscat/internal/matcher"
	"actionscat/internal/runner"
	"actionscat/internal/runtime"
	"actionscat/internal/sandbox"
	"actionscat/internal/scheduler"
	"actionscat/internal/store"
	"net/http"

	"github.com/gin-gonic/gin"
)

type Server struct {
	Store         *store.SQLiteStore
	FileStore     *store.FileStore
	StateStore    *store.StateStore
	ActionService *action.Service
	Builder       *build.Builder
	Runner        *runner.Runner
	Scheduler     *scheduler.Scheduler
	MatcherEngine *matcher.Engine
	RuntimeAPI    *runtime.API
	ManagementAPI *ManagementAPI
	Dispatch      *DispatchHandler
}

type ServerConfig struct {
	Store           *store.SQLiteStore
	FileStore       *store.FileStore
	StateStore      *store.StateStore
	Sandbox         sandbox.Backend
	FrostAgent      frostagent.Client
	RuntimeEndpoint string
	RunnerWorkers   int
}

func NewServer(cfg ServerConfig) *Server {
	actSvc := action.NewService(cfg.Store, cfg.FileStore, cfg.Sandbox)
	builder := build.NewBuilder(cfg.Store, cfg.FileStore, cfg.Sandbox)

	r := runner.NewRunner(cfg.Store, cfg.FileStore, cfg.StateStore, cfg.Sandbox, runner.Config{
		MaxWorkers:      cfg.RunnerWorkers,
		RuntimeEndpoint: cfg.RuntimeEndpoint,
	})

	sched := scheduler.NewScheduler(cfg.Store, r, scheduler.Config{})
	eng := matcher.NewEngine(cfg.Store, r)
	runtimeAPI := runtime.NewAPI(cfg.Store, cfg.StateStore, cfg.FrostAgent)
	mgmtAPI := NewManagementAPI(cfg.Store, actSvc, builder, r, sched)
	dispatchHandler := NewDispatchHandler(eng)

	return &Server{
		Store:         cfg.Store,
		FileStore:     cfg.FileStore,
		StateStore:    cfg.StateStore,
		ActionService: actSvc,
		Builder:       builder,
		Runner:        r,
		Scheduler:     sched,
		MatcherEngine: eng,
		RuntimeAPI:    runtimeAPI,
		ManagementAPI: mgmtAPI,
		Dispatch:      dispatchHandler,
	}
}

func (s *Server) SetupRouter() *gin.Engine {
	r := gin.Default()

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Legacy & Modern event dispatch endpoint
	r.POST("/v1/dispatch", s.Dispatch.HandleDispatch)
	r.POST("/api/v1/dispatch", s.Dispatch.HandleDispatch)

	// Runtime Capability API (called from inside Run Sandboxes)
	runtimeGroup := r.Group("/api/v1/runtime")
	s.RuntimeAPI.RegisterRoutes(runtimeGroup)

	// Management API (called by operators, CLI, and FrostAgent management plane)
	mgmtGroup := r.Group("/api/v1")
	s.ManagementAPI.RegisterRoutes(mgmtGroup)

	return r
}
