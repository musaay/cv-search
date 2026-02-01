package graphrag

import (
	"context"
	"database/sql"
	"fmt"
)

type GraphRAGEngine struct {
	db           *sql.DB
	llmClient    LLMClient // OpenAI, Gemini, etc.
	graphBuilder *GraphBuilder
	matcher      *CriteriaMatcher
}

type LLMClient interface {
	ExtractEntities(text string) ([]Entity, error)
	GenerateEmbedding(text string) ([]float64, error)
	Generate(prompt string) (string, error) // For GraphRAG query analysis
}

type Entity struct {
	Type       string                 `json:"type"`
	Value      string                 `json:"value"`
	Confidence float64                `json:"confidence"`
	Properties map[string]interface{} `json:"properties,omitempty"`
}

type Relationship struct {
	SourceType string                 `json:"source_type"`
	SourceID   string                 `json:"source_id"`
	TargetType string                 `json:"target_type"`
	TargetID   string                 `json:"target_id"`
	EdgeType   string                 `json:"edge_type"`
	Properties map[string]interface{} `json:"properties,omitempty"`
}

func NewGraphRAGEngine(db *sql.DB, llmClient LLMClient) *GraphRAGEngine {
	return &GraphRAGEngine{
		db:           db,
		llmClient:    llmClient,
		graphBuilder: NewGraphBuilder(db),
		matcher:      NewCriteriaMatcher(db, llmClient),
	}
}

// BuildCandidateGraph creates knowledge graph from candidate data
func (e *GraphRAGEngine) BuildCandidateGraph(ctx context.Context, candidateID int) error {
	// 1. Fetch candidate from DB
	candidate, err := e.fetchCandidate(ctx, candidateID)
	if err != nil {
		return fmt.Errorf("failed to fetch candidate: %w", err)
	}

	// 2. Extract entities from candidate data
	entities, relationships := e.extractCandidateEntities(candidate)

	// 3. Fetch and parse CV if exists
	cvText, err := e.fetchCVText(ctx, candidateID)
	if err == nil && cvText != "" {
		cvEntities, cvRelations := e.extractCVEntities(cvText, candidateID)
		entities = append(entities, cvEntities...)
		relationships = append(relationships, cvRelations...)
	}

	// 4. Build graph nodes and edges
	if err := e.graphBuilder.CreateNodes(ctx, entities); err != nil {
		return fmt.Errorf("failed to create nodes: %w", err)
	}

	if err := e.graphBuilder.CreateEdges(ctx, relationships); err != nil {
		return fmt.Errorf("failed to create edges: %w", err)
	}

	return nil
}

type Candidate struct {
	ID         int
	Name       string
	Email      string
	Experience string
	Skills     string
	Location   string
}

func (e *GraphRAGEngine) fetchCandidate(ctx context.Context, candidateID int) (*Candidate, error) {
	var c Candidate
	err := e.db.QueryRowContext(ctx,
		`SELECT id, name, email, experience, skills, location 
		 FROM candidates WHERE id = $1`,
		candidateID,
	).Scan(&c.ID, &c.Name, &c.Email, &c.Experience, &c.Skills, &c.Location)

	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (e *GraphRAGEngine) fetchCVText(ctx context.Context, candidateID int) (string, error) {
	var text string
	err := e.db.QueryRowContext(ctx,
		`SELECT parsed_text FROM cv_files 
		 WHERE candidate_id = $1 AND parsed_text IS NOT NULL 
		 ORDER BY uploaded_at DESC LIMIT 1`,
		candidateID,
	).Scan(&text)

	if err == sql.ErrNoRows {
		return "", nil
	}
	return text, err
}

func (e *GraphRAGEngine) extractCandidateEntities(c *Candidate) ([]Entity, []Relationship) {
	entities := []Entity{
		{
			Type:  "person",
			Value: fmt.Sprintf("person_%d", c.ID),
			Properties: map[string]interface{}{
				"name":       c.Name,
				"email":      c.Email,
				"experience": c.Experience,
			},
		},
	}

	relationships := []Relationship{}

	// Extract location
	if c.Location != "" {
		entities = append(entities, Entity{
			Type:  "location",
			Value: c.Location,
		})
		relationships = append(relationships, Relationship{
			SourceType: "person",
			SourceID:   fmt.Sprintf("person_%d", c.ID),
			TargetType: "location",
			TargetID:   c.Location,
			EdgeType:   "located_in",
		})
	}

	// Extract skills (comma-separated in DB)
	if c.Skills != "" {
		// Simple parsing - should be improved with LLM
		skills := parseSkills(c.Skills)
		for _, skill := range skills {
			entities = append(entities, Entity{
				Type:  "skill",
				Value: skill,
			})
			relationships = append(relationships, Relationship{
				SourceType: "person",
				SourceID:   fmt.Sprintf("person_%d", c.ID),
				TargetType: "skill",
				TargetID:   skill,
				EdgeType:   "has_skill",
			})
		}
	}

	return entities, relationships
}

func (e *GraphRAGEngine) extractCVEntities(cvText string, candidateID int) ([]Entity, []Relationship) {
	// Use LLM to extract entities from CV
	entities, err := e.llmClient.ExtractEntities(cvText)
	if err != nil {
		// Fallback to basic extraction
		return []Entity{}, []Relationship{}
	}

	relationships := []Relationship{}
	personID := fmt.Sprintf("person_%d", candidateID)

	// Create relationships between person and extracted entities
	for _, entity := range entities {
		var edgeType string
		switch entity.Type {
		case "skill":
			edgeType = "has_skill"
		case "company":
			edgeType = "worked_at"
		case "education":
			edgeType = "studied_at"
		case "certification":
			edgeType = "certified_in"
		default:
			continue
		}

		relationships = append(relationships, Relationship{
			SourceType: "person",
			SourceID:   personID,
			TargetType: entity.Type,
			TargetID:   entity.Value,
			EdgeType:   edgeType,
			Properties: entity.Properties,
		})
	}

	return entities, relationships
}

func parseSkills(skills string) []string {
	// Simple comma-separated parsing
	// TODO: Use LLM for better parsing
	var result []string
	for _, skill := range splitAndTrim(skills, ",") {
		if skill != "" {
			result = append(result, skill)
		}
	}
	return result
}

func splitAndTrim(s, sep string) []string {
	var result []string
	for _, part := range splitString(s, sep) {
		trimmed := trimSpace(part)
		result = append(result, trimmed)
	}
	return result
}

func splitString(s, sep string) []string {
	// Simplified split
	return []string{s} // TODO: implement proper split
}

func trimSpace(s string) string {
	return s // TODO: implement proper trim
}
