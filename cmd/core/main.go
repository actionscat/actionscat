package main

import (
	"actionscat/internal/api"
	"actionscat/internal/frostagent"
	"actionscat/internal/sandbox"
	"actionscat/internal/store"
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func main() {
	addr := os.Getenv("ACTIONSCAT_ADDR")
	if addr == "" {
		addr = ":7999"
	}

	dataDir := os.Getenv("ACTIONSCAT_DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	if err := os.MkdirAll(dataDir, 0750); err != nil {
		log.Fatalf("failed to create data dir %s: %v", dataDir, err)
	}

	dbPath := os.Getenv("ACTIONSCAT_DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(dataDir, "actionscat.db")
	}

	db, err := store.OpenDB(dbPath)
	if err != nil {
		log.Fatalf("failed to open database at %s: %v", dbPath, err)
	}
	defer db.Close()

	sqliteStore := store.NewSQLiteStore(db)
	fileStore := store.NewFileStore(dataDir)
	stateStore := store.NewStateStore(dataDir)

	sandboxEndpoint := os.Getenv("FA_SANDBOX_ENDPOINT")
	if sandboxEndpoint == "" {
		sandboxEndpoint = "http://127.0.0.1:8080"
	}
	sandboxBackend := sandbox.NewClient(sandbox.Config{
		BaseURL:   sandboxEndpoint,
		AuthToken: os.Getenv("FA_SANDBOX_API_KEY"),
	})

	faEndpoint := os.Getenv("FROSTAGENT_ENDPOINT")
	if faEndpoint == "" {
		faEndpoint = "http://127.0.0.1:8000"
	}
	faClient := frostagent.NewHTTPClient(frostagent.Config{
		BaseURL:      faEndpoint,
		SendEndpoint: os.Getenv("FROSTAGENT_SEND_ENDPOINT"),
		APIKey:       os.Getenv("FROSTAGENT_API_KEY"),
	})

	runtimeEndpoint := os.Getenv("ACTIONSCAT_RUNTIME_ENDPOINT")
	if runtimeEndpoint == "" {
		runtimeEndpoint = "http://127.0.0.1:7999/api/v1/runtime"
	}

	workers := 8
	if wStr := os.Getenv("ACTIONSCAT_RUNNER_WORKERS"); wStr != "" {
		if w, err := strconv.Atoi(wStr); err == nil && w > 0 {
			workers = w
		}
	}

	mgmtToken := os.Getenv("ACTIONSCAT_MANAGEMENT_TOKEN")
	if mgmtToken == "" {
		mgmtToken = os.Getenv("ACTIONSCAT_API_KEY")
	}

	server := api.NewServer(api.ServerConfig{
		Store:           sqliteStore,
		FileStore:       fileStore,
		StateStore:      stateStore,
		Sandbox:         sandboxBackend,
		FrostAgent:      faClient,
		RuntimeEndpoint: runtimeEndpoint,
		RunnerWorkers:   workers,
		ManagementToken: mgmtToken,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start asynchronous persistent workers
	server.Runner.Start(ctx)
	server.Scheduler.Start(ctx)

	router := server.SetupRouter()
	httpServer := &http.Server{
		Addr:    addr,
		Handler: router,
	}

	// Graceful shutdown handling
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("ActionsCat Core server listening on %s (data: %s)", addr, dataDir)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-stopChan
	log.Println("Shutting down ActionsCat Core server gracefully...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown error: %v", err)
	}

	server.Scheduler.Stop()
	server.Runner.Stop()
	cancel()

	log.Println("ActionsCat Core server stopped.")
}
