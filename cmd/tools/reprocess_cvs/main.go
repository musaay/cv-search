package main

import (
	"context"
	"flag"
	"log"
	"os"
	"strconv"

	"cv-search/internal/graphrag"
	"cv-search/internal/llm"
	"cv-search/internal/reprocess"
	"cv-search/internal/storage"
)

func main() {
	var dryRun bool
	var onlyCandidateID int
	flag.BoolVar(&dryRun, "dry-run", true, "only report")
	flag.IntVar(&onlyCandidateID, "candidate-id", 0, "specific candidate")
	flag.Parse()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL required")
	}
	openaiKey := os.Getenv("OPENAI_API_KEY")
	if openaiKey == "" {
		log.Fatal("OPENAI_API_KEY required")
	}
	llmProvider := os.Getenv("LLM_PROVIDER")
	if llmProvider == "" {
		llmProvider = "openai"
	}
	llmModel := os.Getenv("LLM_MODEL")
	if llmModel == "" {
		llmModel = "gpt-4o-mini"
	}
	var llmAPIKey string
	if llmProvider == "groq" {
		llmAPIKey = os.Getenv("GROQ_API_KEY")
	} else {
		llmAPIKey = os.Getenv("OPENAI_API_KEY")
	}
	if llmAPIKey == "" {
		log.Fatalf("API key not set for provider %q (set GROQ_API_KEY or OPENAI_API_KEY)", llmProvider)
	}

	db, err := storage.NewDB(dbURL)
	if err != nil {
		log.Fatalf("DB: %v", err)
	}
	defer db.Close()

	llmSvc := llm.NewService(llmProvider, llmAPIKey, llmModel)
	graphBuilder := graphrag.NewGraphBuilder(db.GetConnection())
	embeddingSvc := graphrag.NewEmbeddingService(openaiKey, db.GetConnection())

	ctx := context.Background()

	opts := reprocess.Options{
		DryRun:          dryRun,
		OnlyCandidateID: onlyCandidateID,
		LLMProvider:     llmProvider,
	}
	if v := os.Getenv("REPROCESS_BATCH_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.BatchThreshold = n
		}
	}

	if err := reprocess.Run(ctx, db, llmSvc, graphBuilder, embeddingSvc, opts); err != nil {
		log.Fatalf("reprocess run failed: %v", err)
	}
}
