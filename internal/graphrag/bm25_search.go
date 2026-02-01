package graphrag

import (
	"context"
	"database/sql"
	"fmt"
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
	Name        string
	Rank        float64 // PostgreSQL ts_rank score
	Headline    string  // First 100 chars of experience
}

// Search performs BM25-style full-text search
// Returns top N candidates sorted by relevance
func (b *BM25Searcher) Search(ctx context.Context, query string, limit int) ([]BM25Result, error) {
	// Convert query to tsquery format
	// "Go developer" -> "Go & developer"
	tsQuery := prepareTSQuery(query)

	sqlQuery := `
		SELECT 
			id,
			name,
			ts_rank(search_vector, to_tsquery('english', $1)) as rank,
			LEFT(experience, 100) as headline
		FROM candidates
		WHERE search_vector @@ to_tsquery('english', $1)
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
		if err := rows.Scan(&r.CandidateID, &r.Name, &r.Rank, &r.Headline); err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		results = append(results, r)
	}

	return results, rows.Err()
}

// prepareTSQuery converts natural language query to PostgreSQL tsquery
// Example: "senior golang developer" -> "senior & golang & developer"
func prepareTSQuery(query string) string {
	// Remove special characters
	query = strings.ToLower(query)

	// Split by spaces and join with &
	words := strings.Fields(query)

	// Filter out common stop words (already handled by to_tsvector, but extra safety)
	filtered := make([]string, 0, len(words))
	for _, word := range words {
		if len(word) > 2 { // Skip very short words
			filtered = append(filtered, word)
		}
	}

	if len(filtered) == 0 {
		return "default" // Fallback
	}

	return strings.Join(filtered, " & ")
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
