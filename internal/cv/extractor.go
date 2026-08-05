package cv

import (
	"cv-search/internal/llm"
	"fmt"
	"log/slog"
)

type Extractor struct {
	llmService *llm.Service
	useLLM     bool
}

func NewExtractor(llmService *llm.Service, useLLM bool) *Extractor {
	return &Extractor{
		llmService: llmService,
		useLLM:     useLLM,
	}
}

// Extract entities from CV text using LLM
func (e *Extractor) Extract(cvText string) (*llm.CVExtraction, error) {
	if !e.useLLM || e.llmService == nil {
		slog.Info("LLM disabled, returning empty extraction")
		return &llm.CVExtraction{
			Skills:    []llm.Skill{},
			Companies: []llm.Company{},
		}, nil
	}

	slog.Info("Extracting entities using LLM...")
	extraction, err := e.llmService.ExtractEntities(cvText)
	if err != nil {
		slog.Error(fmt.Sprintf("LLM extraction failed: %v", err))
		return nil, err
	}

	slog.Info(fmt.Sprintf("LLM extracted: %d skills, %d companies",
		len(extraction.Skills), len(extraction.Companies)))

	return extraction, nil
}
