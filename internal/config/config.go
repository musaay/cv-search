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

	maxBulkFileCount := 20 // default 20 files
	if val := os.Getenv("MAX_BULK_FILE_COUNT"); val != "" {
		if i, err := strconv.Atoi(val); err == nil && i > 0 {
			maxBulkFileCount = i
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
		MaxFileSizeMB:   maxFileSizeMB,
		MaxBulkFileCount: maxBulkFileCount,
	}
}
