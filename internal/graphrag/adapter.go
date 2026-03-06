package graphrag

import "cv-search/internal/llm"

// LLMClient is the interface every LLM integration must satisfy within the graphrag package.
type LLMClient interface {
	ExtractEntities(text string) ([]Entity, error)
	GenerateEmbedding(text string) ([]float64, error)
	Generate(prompt string) (string, error)
}

// Entity represents a node extracted from text (skill, company, education, etc.)
type Entity struct {
	Type       string                 `json:"type"`
	Value      string                 `json:"value"`
	Confidence float64                `json:"confidence"`
	Properties map[string]interface{} `json:"properties,omitempty"`
}

// Relationship represents a directed edge between two entities.
type Relationship struct {
	SourceType string                 `json:"source_type"`
	SourceID   string                 `json:"source_id"`
	TargetType string                 `json:"target_type"`
	TargetID   string                 `json:"target_id"`
	EdgeType   string                 `json:"edge_type"`
	Properties map[string]interface{} `json:"properties,omitempty"`
}

// LLMAdapter adapts llm.Service to GraphRAG's LLMClient interface.
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
	// Not used in the search pipeline; required by LLMClient interface.
	return nil, nil
}

func (a *LLMAdapter) GenerateEmbedding(text string) ([]float64, error) {
	// Embeddings are handled by EmbeddingService (direct OpenAI HTTP client); not routed here.
	return nil, nil
}
