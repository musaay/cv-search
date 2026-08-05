package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	_ "cv-search/docs" // Swagger docs
	"cv-search/internal/api"
	"cv-search/internal/config"
	"cv-search/internal/llm"
	"cv-search/internal/reprocess"
	"cv-search/internal/storage"

	"github.com/joho/godotenv"
)

// @title CV Search & GraphRAG API
// @version 2.0
// @description AI-powered CV search system with GraphRAG and Hybrid Search capabilities
// @termsOfService http://swagger.io/terms/

// @contact.name API Support
// @contact.url http://www.swagger.io/support
// @contact.email support@swagger.io

// @license.name MIT
// @license.url https://opensource.org/licenses/MIT

// @host cv-search-production.up.railway.app
// @BasePath /api
// @schemes https

func main() {
	// Initialize structured logging (slog)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Load .env file
	if err := godotenv.Load(); err != nil {
		slog.Warn("Warning: .env file not found, using environment variables")
	}

	cfg := config.LoadConfig()

	if cfg.DatabaseURL == "" {
		slog.Error(fmt.Sprint("set DATABASE_URL environment variable (e.g. postgres://user:pass@host:5432/dbname?sslmode=disable)"))
		os.Exit(1)
	}

	slog.Info("Connecting to database...")

	db, err := storage.NewDB(cfg.DatabaseURL)
	if err != nil {
		slog.Error(fmt.Sprint("db open:", err))
		os.Exit(1)
	}
	defer db.Close()

	slog.Info("Database connected successfully!")

	apiSrv := api.NewAPI(db, cfg)
	router := api.NewRouter(apiSrv)

	// Optional one-off backlog job, gated entirely by env vars so it can be
	// toggled on/off per deploy without a code change:
	//   RUN_REPROCESS_JOB=true       — enables the job on this startup
	//   REPROCESS_DRY_RUN=true|false — defaults to true (report only, no writes)
	// Runs in the background so it never blocks the HTTP server from starting
	// (Railway health checks still pass immediately). Turn RUN_REPROCESS_JOB
	// off again once the logs show "Completed" so it doesn't re-run on the
	// next restart/deploy.
	if os.Getenv("RUN_REPROCESS_JOB") == "true" {
		go runReprocessJob(apiSrv, cfg)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  2 * time.Minute,  // File upload için (bulk upload desteği)
		WriteTimeout: 15 * time.Minute, // LLM processing + response için (Ollama 10 dakika + buffer)
		IdleTimeout:  120 * time.Second,
	}

	idleConnsClosed := make(chan struct{})
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			slog.Info("server shutdown:", err)
		}
		close(idleConnsClosed)
	}()

	slog.Info(fmt.Sprintf("API server listening on :%s\n", port))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error(fmt.Sprint(err))
		os.Exit(1)
	}

	<-idleConnsClosed
}

// runReprocessJob runs the shared CV backlog reprocessing pass in-process.
// By default it reuses the API's already-constructed llmService (and
// therefore its shared rate limiter) instead of spinning up a second,
// uncoordinated one. Set REPROCESS_LLM_PROVIDER (e.g. "openai") to run the
// job against a different provider entirely -- useful when the main
// provider's account has run out of daily capacity, since it doesn't share
// that provider's quota. Gated by RUN_REPROCESS_JOB / REPROCESS_DRY_RUN env
// vars (see main()). Any error here is logged, not fatal -- a failed job run
// must never take down the running API server.
func runReprocessJob(apiSrv *api.API, cfg *config.Config) {
	dryRun := true // safe default: never mutate data unless explicitly opted in
	if v := os.Getenv("REPROCESS_DRY_RUN"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			dryRun = b
		}
	}

	slog.Info(fmt.Sprintf("[ReprocessJob] Starting (dry_run=%v)...", dryRun))

	llmProvider := cfg.LLMProvider
	var llmSvcOverride *llm.Service
	if override := os.Getenv("REPROCESS_LLM_PROVIDER"); override != "" && override != cfg.LLMProvider {
		llmProvider = override
		model := os.Getenv("REPROCESS_LLM_MODEL")
		if model == "" {
			model = "gpt-4o-mini"
		}
		apiKey := cfg.OpenAIAPIKey
		if override == "groq" {
			apiKey = cfg.LLMAPIKey
		}
		if apiKey == "" {
			slog.Info(fmt.Sprintf("[ReprocessJob] REPROCESS_LLM_PROVIDER=%s set but no matching API key configured, aborting", override))
			return
		}
		slog.Info(fmt.Sprintf("[ReprocessJob] Using override provider=%s model=%s for this run", override, model))
		llmSvcOverride = llm.NewService(override, apiKey, model)
	}

	opts := reprocess.Options{
		DryRun:          dryRun,
		LLMProvider:     llmProvider,
		DisableBatchAPI: os.Getenv("GROQ_BATCH_DISABLED") == "true",
		ForceAll:        os.Getenv("REPROCESS_FORCE_ALL") == "true",
	}
	if v := os.Getenv("REPROCESS_BATCH_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.BatchThreshold = n
		}
	}

	if err := apiSrv.RunReprocessJob(context.Background(), llmSvcOverride, opts); err != nil {
		slog.Error(fmt.Sprintf("[ReprocessJob] run failed: %v", err))
		return
	}

	slog.Info(fmt.Sprintf("[ReprocessJob] Finished. Set RUN_REPROCESS_JOB=false and redeploy so this doesn't run again on the next restart."))
}
