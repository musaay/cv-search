package config

import (
	"log"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	DatabaseURL string

	// LLM Configuration
	LLMProvider string // "openai", "groq", or "none"
	LLMModel    string // "gpt-4o-mini", "gpt-4o", "llama-3.3-70b-versatile"
	LLMAPIKey   string // OpenAI or Groq API key (for LLM text generation)

	// OpenAI embeddings key — always needed for vector search, even when using Groq for LLM.
	OpenAIAPIKey string

	// File storage
	UploadsDir string

	// Set to true in local/dev to bypass LLM cache and always hit the LLM.
	// In prod leave it unset (defaults to false) so cache is active.
	DisableLLMCache bool

	// File Upload Constraints
	MaxFileSizeMB    int
	MaxBulkFileCount int

	// Above this many files in a single bulk upload, CV extraction is routed
	// through the Groq Batch API instead of the real-time queue.
	MaxRealtimeCVCount int
}

func LoadConfig() *Config {
	err := godotenv.Load()
	if err != nil {
		log.Printf("Warning: Error loading .env file: %v", err)
		log.Println("Attempting to load from parent directory...")
		err = godotenv.Load("../../.env")
		if err != nil {
			log.Println("Warning: Could not load .env file, using environment variables")
		}
	}

	// LLM configuration
	llmProvider := os.Getenv("LLM_PROVIDER")
	if llmProvider == "" {
		llmProvider = "openai" // default
	}

	llmModel := os.Getenv("LLM_MODEL")
	if llmModel == "" {
		llmModel = "gpt-4o-mini" // default model
	}

	// Get API key based on provider
	llmAPIKey := ""
	if llmProvider == "openai" {
		llmAPIKey = os.Getenv("OPENAI_API_KEY")
	} else if llmProvider == "groq" {
		llmAPIKey = os.Getenv("GROQ_API_KEY")
	}

	maxFileSizeMB := 5 // default 5 MB
	if val := os.Getenv("MAX_FILE_SIZE_MB"); val != "" {
		if i, err := strconv.Atoi(val); err == nil && i > 0 {
			maxFileSizeMB = i
		}
	}

	maxBulkFileCount := 100 // default 100 files; large batches route through Groq Batch API (see MaxRealtimeCVCount)
	if val := os.Getenv("MAX_BULK_FILE_COUNT"); val != "" {
		if i, err := strconv.Atoi(val); err == nil && i > 0 {
			maxBulkFileCount = i
		}
	}

	// Above this many files in one bulk upload, extraction is submitted as a
	// single Groq Batch API job instead of the real-time queue (results take
	// minutes-to-hours instead of seconds, but never hit the standard rate limit).
	maxRealtimeCVCount := 20
	if val := os.Getenv("MAX_REALTIME_CV_COUNT"); val != "" {
		if i, err := strconv.Atoi(val); err == nil && i > 0 {
			maxRealtimeCVCount = i
		}
	}

	return &Config{
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		LLMProvider:     llmProvider,
		LLMModel:        llmModel,
		LLMAPIKey:       llmAPIKey,
		OpenAIAPIKey:    os.Getenv("OPENAI_API_KEY"),
		UploadsDir:      os.Getenv("UPLOADS_DIR"),
		DisableLLMCache: os.Getenv("LLM_CACHE_DISABLED") == "true",
		MaxFileSizeMB:      maxFileSizeMB,
		MaxBulkFileCount:   maxBulkFileCount,
		MaxRealtimeCVCount: maxRealtimeCVCount,
	}
}
