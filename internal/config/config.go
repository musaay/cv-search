package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	DatabaseURL string

	// LLM Configuration
	LLMProvider string // "openai", "groq", or "none"
	LLMModel    string // "gpt-4o-mini", "gpt-4o", "llama-3.3-70b-versatile"
	LLMAPIKey   string // OpenAI or Groq API key
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

	return &Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		LLMProvider: llmProvider,
		LLMModel:    llmModel,
		LLMAPIKey:   llmAPIKey,
	}
}
