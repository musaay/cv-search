package graphrag

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// BM25Searcher performs full-text search using PostgreSQL tsvector
type BM25Searcher struct {
	db *sql.DB
}

func NewBM25Searcher(db *sql.DB) *BM25Searcher {
	return &BM25Searcher{db: db}
}

// BM25Result represents a candidate with BM25 relevance score
type BM25Result struct {
	CandidateID int
	NodeID      string  // graph_nodes.node_id — the shared key for merging with vector/graph results
	Name        string
	Rank        float64 // PostgreSQL ts_rank score
	Headline    string  // First 100 chars of experience
}

// Search performs BM25-style full-text search
// Returns top N candidates sorted by relevance
func (b *BM25Searcher) Search(ctx context.Context, query string, limit int) ([]BM25Result, error) {
	// Clear any cached prepared statements to avoid binding errors
	if _, err := b.db.ExecContext(ctx, "DEALLOCATE ALL"); err != nil {
		log.Printf("[BM25Search] Warning: DEALLOCATE ALL failed: %v", err)
	}

	// Convert query to tsquery format
	// "Go developer" -> "Go & developer"
	tsQuery := prepareTSQuery(query)

	// Join graph_nodes to get node_id — the same key used by vector and graph searchers.
	// Without this, BM25 results would never merge with the other two sources.
	sqlQuery := `
		SELECT
			c.id,
			COALESCE(gn.node_id, ''),
			c.name,
			ts_rank(c.search_vector, to_tsquery('english', $1)) as rank,
			LEFT(c.experience, 100) as headline
		FROM candidates c
		LEFT JOIN graph_nodes gn ON gn.id = c.graph_node_id
		WHERE c.search_vector @@ to_tsquery('english', $1)
		ORDER BY rank DESC
		LIMIT $2
	`

	rows, err := b.db.QueryContext(ctx, sqlQuery, tsQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("bm25 search failed: %w", err)
	}
	defer rows.Close()

	var results []BM25Result
	for rows.Next() {
		var r BM25Result
		if err := rows.Scan(&r.CandidateID, &r.NodeID, &r.Name, &r.Rank, &r.Headline); err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		results = append(results, r)
	}

	return results, rows.Err()
}

// prepareTSQuery converts natural language query to PostgreSQL tsquery.
// Uses OR logic so any term match counts; ts_rank handles relevance ordering.
// Example: "senior golang developer" -> "senior | golang | develop"
func prepareTSQuery(query string) string {
	query = strings.ToLower(query)
	words := strings.Fields(query)

	filtered := make([]string, 0, len(words))
	for _, word := range words {
		if len(word) > 2 { // Skip very short words
			filtered = append(filtered, word)
		}
	}

	if len(filtered) == 0 {
		return "default"
	}

	// OR logic for maximum recall — ts_rank will sort by how many terms match.
	// AND would require ALL terms in one row, which is too strict for skill lists.
	return strings.Join(filtered, " | ")
}

// SearchWithWeights allows custom weight for different fields
// Useful for boosting skills vs experience
func (b *BM25Searcher) SearchWithWeights(ctx context.Context, query string, limit int, weights []float64) ([]BM25Result, error) {
	if len(weights) != 4 {
		weights = []float64{0.3, 0.4, 0.2, 0.1} // Default: name, skills, experience, location
	}

	tsQuery := prepareTSQuery(query)

	sqlQuery := fmt.Sprintf(`
		SELECT 
			id,
			name,
			ts_rank(
				array[%f, %f, %f, %f],
				search_vector, 
				to_tsquery('english', $1)
			) as rank,
			LEFT(experience, 100) as headline
		FROM candidates
		WHERE search_vector @@ to_tsquery('english', $1)
		ORDER BY rank DESC
		LIMIT $2
	`, weights[0], weights[1], weights[2], weights[3])

	rows, err := b.db.QueryContext(ctx, sqlQuery, tsQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("weighted bm25 search failed: %w", err)
	}
	defer rows.Close()

	var results []BM25Result
	for rows.Next() {
		var r BM25Result
		if err := rows.Scan(&r.CandidateID, &r.Name, &r.Rank, &r.Headline); err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		results = append(results, r)
	}

	return results, rows.Err()
}
