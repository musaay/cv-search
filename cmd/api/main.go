package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "cv-search/docs" // Swagger docs
	"cv-search/internal/api"
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

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("set DATABASE_URL environment variable (e.g. postgres://user:pass@host:5432/dbname?sslmode=disable)")
	}

	log.Println("Connecting to database...")
	log.Println("DSN:", dsn)

	db, err := storage.NewDB(dsn)
	if err != nil {
		log.Fatal("db open:", err)
	}
	defer db.Close()

	log.Println("Database connected successfully!")

	apiSrv := api.NewAPI(db)
	router := api.NewRouter(apiSrv)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  30 * time.Second, // File upload için
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
