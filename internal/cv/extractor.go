package cv

import (
	"cv-search/internal/llm"
	"log"
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
		log.Println("LLM disabled, returning empty extraction")
		return &llm.CVExtraction{
			Skills:    []llm.Skill{},
			Companies: []llm.Company{},
		}, nil
	}

	log.Println("Extracting entities using LLM...")
	extraction, err := e.llmService.ExtractEntities(cvText)
	if err != nil {
		log.Printf("LLM extraction failed: %v", err)
		return nil, err
	}

	log.Printf("LLM extracted: %d skills, %d companies",
		len(extraction.Skills), len(extraction.Companies))

	return extraction, nil
}
