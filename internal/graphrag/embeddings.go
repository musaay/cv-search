package graphrag

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// EmbeddingService generates vector embeddings for semantic search
type EmbeddingService struct {
	apiKey     string
	httpClient *http.Client
	db         *sql.DB
}

func NewEmbeddingService(apiKey string, db *sql.DB) *EmbeddingService {
	return &EmbeddingService{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		db: db,
	}
}

// GenerateEmbedding creates a vector embedding for text using Groq
func (s *EmbeddingService) GenerateEmbedding(ctx context.Context, text string) ([]float32, error) {
	// Note: Groq doesn't have embeddings API yet, so we'll use a workaround
	// or integrate with OpenAI for embeddings specifically

	// For now, use OpenAI embeddings (you can switch to other providers)
	url := "https://api.openai.com/v1/embeddings"

	requestBody := map[string]interface{}{
		"input": text,
		"model": "text-embedding-3-small", // 1536 dimensions, cheaper than ada-002
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embedding API error: %d - %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}

	return result.Data[0].Embedding, nil
}

// EmbedNode generates and stores embedding for a graph node
func (s *EmbeddingService) EmbedNode(ctx context.Context, nodeID string) error {
	// Get node info
	var nodeType string
	var properties []byte

	err := s.db.QueryRowContext(ctx, `
		SELECT node_type, properties 
		FROM graph_nodes 
		WHERE node_id = $1
	`, nodeID).Scan(&nodeType, &properties)

	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}

	// Parse properties
	var props map[string]interface{}
	if err := json.Unmarshal(properties, &props); err != nil {
		return err
	}

	// Create text representation
	text := s.nodeToText(nodeType, props)

	// Generate embedding
	embedding, err := s.GenerateEmbedding(ctx, text)
	if err != nil {
		return fmt.Errorf("failed to generate embedding: %w", err)
	}

	// Store embedding
	embeddingJSON, _ := json.Marshal(embedding)

	_, err = s.db.ExecContext(ctx, `
		UPDATE graph_nodes 
		SET embedding = $1,
		    embedding_model = 'text-embedding-3-small',
		    embedding_created_at = NOW()
		WHERE node_id = $2
	`, embeddingJSON, nodeID)

	return err
}

// nodeToText converts node properties to text for embedding
func (s *EmbeddingService) nodeToText(nodeType string, props map[string]interface{}) string {
	switch nodeType {
	case "person":
		name := getString(props, "name")
		position := getString(props, "current_position")
		seniority := getString(props, "seniority")
		exp := getString(props, "total_experience_years")
		return fmt.Sprintf("%s: %s with %s years experience. Seniority: %s",
			name, position, exp, seniority)

	case "skill":
		name := getString(props, "name")
		proficiency := getString(props, "proficiency")
		return fmt.Sprintf("%s skill (proficiency: %s)", name, proficiency)

	case "company":
		name := getString(props, "name")
		industry := getString(props, "industry")
		return fmt.Sprintf("%s company in %s industry", name, industry)

	case "education":
		institution := getString(props, "institution")
		degree := getString(props, "degree")
		field := getString(props, "field")
		return fmt.Sprintf("%s degree in %s from %s", degree, field, institution)

	default:
		return fmt.Sprintf("%v", props)
	}
}

// BatchEmbedAllNodes generates embeddings for all nodes without embeddings
func (s *EmbeddingService) BatchEmbedAllNodes(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT node_id 
		FROM graph_nodes 
		WHERE embedding IS NULL
		ORDER BY created_at DESC
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var nodeIDs []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			continue
		}
		nodeIDs = append(nodeIDs, nodeID)
	}

	log.Printf("[Embeddings] Starting batch embedding for %d nodes", len(nodeIDs))

	for i, nodeID := range nodeIDs {
		if err := s.EmbedNode(ctx, nodeID); err != nil {
			log.Printf("[Embeddings] Failed to embed node %s: %v", nodeID, err)
			continue
		}

		if (i+1)%10 == 0 {
			log.Printf("[Embeddings] Progress: %d/%d nodes embedded", i+1, len(nodeIDs))
		}

		// Rate limiting to avoid OpenAI 429 errors
		// Tier 1 (free): 3 req/min → 20 seconds
		// Tier 2 ($5+): 500 req/min → safe at 5 req/sec (300 req/min)
		if i < len(nodeIDs)-1 {
			time.Sleep(200 * time.Millisecond) // 0.2 seconds = 5x faster!
		}
	}

	log.Printf("[Embeddings] Completed: %d nodes embedded", len(nodeIDs))
	return nil
}

// SimilaritySearch finds similar nodes using vector similarity
func (s *EmbeddingService) SimilaritySearch(ctx context.Context, queryText string, topK int) ([]string, []float64, error) {
	// Generate query embedding
	queryEmbedding, err := s.GenerateEmbedding(ctx, queryText)
	if err != nil {
		return nil, nil, err
	}

	embeddingJSON, _ := json.Marshal(queryEmbedding)

	// Vector similarity search (only person nodes for hybrid search)
	query := `
		SELECT 
			node_id,
			1 - (embedding <=> $1::vector) as similarity
		FROM graph_nodes
		WHERE embedding IS NOT NULL 
		  AND node_type = 'person'
		ORDER BY similarity ASC
		LIMIT $2
	`

	rows, err := s.db.QueryContext(ctx, query, embeddingJSON, topK)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var nodeIDs []string
	var similarities []float64

	for rows.Next() {
		var nodeID string
		var similarity float64

		if err := rows.Scan(&nodeID, &similarity); err != nil {
			continue
		}

		nodeIDs = append(nodeIDs, nodeID)
		similarities = append(similarities, similarity)
	}

	return nodeIDs, similarities, nil
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprintf("%v", v)
	}
	return ""
}
