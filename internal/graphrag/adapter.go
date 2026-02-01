package graphrag

import "cv-search/internal/llm"

// LLMAdapter adapts llm.Service to GraphRAG's LLMClient interface
type LLMAdapter struct {
	service *llm.Service
}

func NewLLMAdapter(service *llm.Service) *LLMAdapter {
	return &LLMAdapter{service: service}
}

func (a *LLMAdapter) Generate(prompt string) (string, error) {
	return a.service.Generate(prompt)
}

func (a *LLMAdapter) ExtractEntities(text string) ([]Entity, error) {
	// Not used in GraphRAG search, but required by interface
	return nil, nil
}

func (a *LLMAdapter) GenerateEmbedding(text string) ([]float64, error) {
	// Not used in GraphRAG search, but required by interface
	return nil, nil
}
