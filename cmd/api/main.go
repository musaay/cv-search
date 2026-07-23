package main

import (
	"context"
	"log"
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
	// Load .env file
	if err := godotenv.Load(); err != nil {
		log.Println("Warning: .env file not found, using environment variables")
	}

	cfg := config.LoadConfig()

	if cfg.DatabaseURL == "" {
		log.Fatal("set DATABASE_URL environment variable (e.g. postgres://user:pass@host:5432/dbname?sslmode=disable)")
	}

	log.Println("Connecting to database...")

	db, err := storage.NewDB(cfg.DatabaseURL)
	if err != nil {
		log.Fatal("db open:", err)
	}
	defer db.Close()

	log.Println("Database connected successfully!")

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
			log.Println("server shutdown:", err)
		}
		close(idleConnsClosed)
	}()

	log.Printf("API server listening on :%s\n", port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
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

	log.Printf("[ReprocessJob] Starting (dry_run=%v)...", dryRun)

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
			log.Printf("[ReprocessJob] REPROCESS_LLM_PROVIDER=%s set but no matching API key configured, aborting", override)
			return
		}
		log.Printf("[ReprocessJob] Using override provider=%s model=%s for this run", override, model)
		llmSvcOverride = llm.NewService(override, apiKey, model)
	}

	opts := reprocess.Options{
		DryRun:          dryRun,
		LLMProvider:     llmProvider,
		DisableBatchAPI: os.Getenv("GROQ_BATCH_DISABLED") == "true",
	}
	if v := os.Getenv("REPROCESS_BATCH_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.BatchThreshold = n
		}
	}

	if err := apiSrv.RunReprocessJob(context.Background(), llmSvcOverride, opts); err != nil {
		log.Printf("[ReprocessJob] run failed: %v", err)
		return
	}

	log.Printf("[ReprocessJob] Finished. Set RUN_REPROCESS_JOB=false and redeploy so this doesn't run again on the next restart.")
}
