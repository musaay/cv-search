package graphrag

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
)

// HybridSearchEngine combines BM25 + Vector + Graph search
// Implements Reciprocal Rank Fusion (RRF) for combining results
type HybridSearchEngine struct {
	db               *sql.DB
	bm25Searcher     *BM25Searcher
	embeddingService *EmbeddingService
	graphQuerier     *GraphQuerier
	llm              LLMClient
}

func NewHybridSearchEngine(db *sql.DB, llm LLMClient, openaiKey string) *HybridSearchEngine {
	return &HybridSearchEngine{
		db:               db,
		bm25Searcher:     NewBM25Searcher(db),
		embeddingService: NewEmbeddingService(openaiKey, db),
		graphQuerier:     NewGraphQuerier(db),
		llm:              llm,
	}
}

// FusedCandidate represents a candidate with scores from multiple sources
type FusedCandidate struct {
	CandidateID          int
	PersonID             string // Graph uses string IDs
	Name                 string
	CurrentPosition      string
	Seniority            string
	TotalExperienceYears int
	Skills               []SkillNode
	Companies            []CompanyNode
	BM25Score            float64 // 0-1 normalized
	VectorScore          float64 // 0-1 normalized
	GraphScore           float64 // 0-1 normalized
	FusionScore          float64 // Weighted combination
	LLMScore             float64 // Final LLM reranking score (0-100)
	LLMReasoning         string
	Rank                 int
}

// VectorSearchResult represents a candidate from vector search
type VectorSearchResult struct {
	CandidateID int
	PersonID    string
	Similarity  float64
}

// HybridSearchConfig defines weights for fusion
type HybridSearchConfig struct {
	BM25Weight   float64 // Default: 0.3
	VectorWeight float64 // Default: 0.4
	GraphWeight  float64 // Default: 0.3
	TopK         int     // How many candidates to retrieve from each source
	FinalTopN    int     // How many to send to LLM for reranking
}

func DefaultHybridConfig() HybridSearchConfig {
	return HybridSearchConfig{
		BM25Weight:   0.3,
		VectorWeight: 0.4,
		GraphWeight:  0.3,
		TopK:         100,
		FinalTopN:    0, // 0 = no limit, send all candidates to LLM
	}
}

// Search performs hybrid search with fusion
func (h *HybridSearchEngine) Search(ctx context.Context, query string, config HybridSearchConfig) ([]FusedCandidate, error) {
	log.Printf("[HybridSearch] Starting search for: %s", query)

	// Step 1: Parallel retrieval from 3 sources
	bm25ResultsChan := make(chan []BM25Result)
	vectorResultsChan := make(chan []VectorSearchResult)
	graphResultsChan := make(chan []CandidateResult)
	errChan := make(chan error, 3)

	// BM25 search
	go func() {
		results, err := h.bm25Searcher.Search(ctx, query, config.TopK)
		if err != nil {
			errChan <- fmt.Errorf("bm25 failed: %w", err)
			return
		}
		bm25ResultsChan <- results
	}()

	// Vector search
	go func() {
		personIDs, similarities, err := h.embeddingService.SimilaritySearch(ctx, query, config.TopK)
		if err != nil {
			errChan <- fmt.Errorf("vector failed: %w", err)
			return
		}
		results := make([]VectorSearchResult, len(personIDs))
		for i := range personIDs {
			results[i] = VectorSearchResult{
				PersonID:   personIDs[i],
				Similarity: similarities[i],
			}
		}
		vectorResultsChan <- results
	}()

	// Graph search (needs criteria extraction first)
	go func() {
		analyzer := NewQueryAnalyzer(h.llm)
		criteria, err := analyzer.AnalyzeQuery(ctx, query)
		if err != nil {
			log.Printf("[HybridSearch] Graph search skipped (criteria extraction failed): %v", err)
			graphResultsChan <- []CandidateResult{}
			return
		}

		results, err := h.graphQuerier.QueryGraph(ctx, criteria)
		if err != nil {
			errChan <- fmt.Errorf("graph failed: %w", err)
			return
		}
		graphResultsChan <- results
	}()

	// Wait for all results
	var bm25Results []BM25Result
	var vectorResults []VectorSearchResult
	var graphResults []CandidateResult

	for i := 0; i < 3; i++ {
		select {
		case bm25Results = <-bm25ResultsChan:
			log.Printf("[HybridSearch] BM25 returned %d results", len(bm25Results))
		case vectorResults = <-vectorResultsChan:
			log.Printf("[HybridSearch] Vector returned %d results", len(vectorResults))
		case graphResults = <-graphResultsChan:
			log.Printf("[HybridSearch] Graph returned %d results", len(graphResults))
		case err := <-errChan:
			return nil, err
		}
	}

	// Step 2: Fuse results using RRF (Reciprocal Rank Fusion)
	fusedCandidates := h.fuseResults(bm25Results, vectorResults, graphResults, config)

	// Step 2.5: Enrich candidates with full details (skills, companies, etc.)
	h.enrichCandidates(ctx, fusedCandidates)

	// Step 3: Take top N for LLM reranking (if FinalTopN > 0, otherwise send all)
	if config.FinalTopN > 0 && len(fusedCandidates) > config.FinalTopN {
		fusedCandidates = fusedCandidates[:config.FinalTopN]
	}

	log.Printf("[HybridSearch] Fusion complete. Top %d candidates ready for LLM reranking", len(fusedCandidates))

	// Step 4: LLM Reranking (Pure LLM Scoring - NO local heuristics!)
	scorer := NewLLMScorer(h.llm)
	llmScores, err := scorer.ScoreCandidates(ctx, query, fusedCandidates)
	if err != nil {
		log.Printf("[HybridSearch] LLM scoring failed, returning fusion scores: %v", err)
		// Fallback: use fusion scores
		for i := range fusedCandidates {
			fusedCandidates[i].LLMScore = fusedCandidates[i].FusionScore
		}
		return fusedCandidates, nil
	}

	// Step 5: Merge LLM scores back into fused candidates
	scoreMap := make(map[string]CandidateScore)
	for _, score := range llmScores {
		scoreMap[score.PersonID] = score
	}

	for i := range fusedCandidates {
		if score, found := scoreMap[fusedCandidates[i].PersonID]; found {
			fusedCandidates[i].LLMScore = score.Score
			fusedCandidates[i].LLMReasoning = score.Reasoning
		} else {
			// If LLM didn't score this candidate, use fusion score
			fusedCandidates[i].LLMScore = fusedCandidates[i].FusionScore
		}
	}

	// Step 6: Re-sort by LLM score (final ranking)
	sort.Slice(fusedCandidates, func(i, j int) bool {
		return fusedCandidates[i].LLMScore > fusedCandidates[j].LLMScore
	})

	// Filter out candidates with no name (empty/invalid results)
	validCandidates := make([]FusedCandidate, 0, len(fusedCandidates))
	for _, c := range fusedCandidates {
		if c.Name != "" && c.PersonID != "" {
			validCandidates = append(validCandidates, c)
		}
	}

	// Update ranks
	for i := range validCandidates {
		validCandidates[i].Rank = i + 1
	}

	if len(validCandidates) == 0 {
		log.Printf("[HybridSearch] No valid candidates found")
		return []FusedCandidate{}, nil
	}

	log.Printf("[HybridSearch] Final ranking complete. Top candidate: %s (LLM Score: %.2f)",
		validCandidates[0].Name, validCandidates[0].LLMScore)

	return validCandidates, nil
} // fuseResults combines results using weighted scoring
func (h *HybridSearchEngine) fuseResults(
	bm25 []BM25Result,
	vector []VectorSearchResult,
	graph []CandidateResult,
	config HybridSearchConfig,
) []FusedCandidate {
	// Create a map of candidate scores (keyed by PersonID string)
	scoreMap := make(map[string]*FusedCandidate)

	// Normalize and add BM25 scores
	maxBM25 := maxBM25Score(bm25)
	for i, r := range bm25 {
		candidateKey := fmt.Sprintf("cand_%d", r.CandidateID)
		if _, exists := scoreMap[candidateKey]; !exists {
			scoreMap[candidateKey] = &FusedCandidate{
				CandidateID: r.CandidateID,
				Name:        r.Name,
			}
		}
		// Reciprocal Rank Fusion: 1 / (k + rank)
		// Also use normalized score
		normalizedScore := r.Rank / maxBM25
		rrfScore := 1.0 / float64(60+i+1) // k=60 is common in RRF
		scoreMap[candidateKey].BM25Score = (normalizedScore + rrfScore) / 2.0
	}

	// Normalize and add Vector scores
	maxVector := maxVectorScore(vector)
	for i, r := range vector {
		if _, exists := scoreMap[r.PersonID]; !exists {
			scoreMap[r.PersonID] = &FusedCandidate{
				PersonID: r.PersonID,
			}
		}
		normalizedScore := r.Similarity / maxVector
		rrfScore := 1.0 / float64(60+i+1)
		scoreMap[r.PersonID].VectorScore = (normalizedScore + rrfScore) / 2.0
	}

	// Normalize and add Graph scores
	maxGraph := maxGraphScore(graph)
	for i, r := range graph {
		if _, exists := scoreMap[r.PersonID]; !exists {
			scoreMap[r.PersonID] = &FusedCandidate{
				PersonID: r.PersonID,
				Name:     r.Name,
			}
		}
		normalizedScore := r.MatchScore / maxGraph
		rrfScore := 1.0 / float64(60+i+1)
		scoreMap[r.PersonID].GraphScore = (normalizedScore + rrfScore) / 2.0
		scoreMap[r.PersonID].Name = r.Name // Update name if not set
	}

	// Calculate fusion score
	results := make([]FusedCandidate, 0, len(scoreMap))
	for _, candidate := range scoreMap {
		candidate.FusionScore = config.BM25Weight*candidate.BM25Score +
			config.VectorWeight*candidate.VectorScore +
			config.GraphWeight*candidate.GraphScore
		results = append(results, *candidate)
	}

	// Sort by fusion score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].FusionScore > results[j].FusionScore
	})

	// Assign ranks
	for i := range results {
		results[i].Rank = i + 1
	}

	return results
}

// Helper functions to find max scores for normalization
func maxBM25Score(results []BM25Result) float64 {
	if len(results) == 0 {
		return 1.0
	}
	max := results[0].Rank
	for _, r := range results {
		if r.Rank > max {
			max = r.Rank
		}
	}
	if max == 0 {
		return 1.0
	}
	return max
}

func maxVectorScore(results []VectorSearchResult) float64 {
	if len(results) == 0 {
		return 1.0
	}
	max := results[0].Similarity
	for _, r := range results {
		if r.Similarity > max {
			max = r.Similarity
		}
	}
	if max == 0 {
		return 1.0
	}
	return max
}

func maxGraphScore(results []CandidateResult) float64 {
	if len(results) == 0 {
		return 1.0
	}
	max := results[0].MatchScore
	for _, r := range results {
		if r.MatchScore > max {
			max = r.MatchScore
		}
	}
	if max == 0 {
		return 100.0 // Graph scores are 0-100
	}
	return max
}

// enrichCandidates loads full candidate details (skills, companies, etc.)
func (h *HybridSearchEngine) enrichCandidates(ctx context.Context, candidates []FusedCandidate) {
	for i := range candidates {
		if candidates[i].PersonID == "" {
			continue
		}

		// Load person details from graph
		var propsJSON []byte
		err := h.db.QueryRowContext(ctx, `
			SELECT properties 
			FROM graph_nodes 
			WHERE node_id = $1 AND node_type = 'person'
		`, candidates[i].PersonID).Scan(&propsJSON)

		if err != nil {
			log.Printf("[HybridSearch] Failed to load person %s: %v", candidates[i].PersonID, err)
			continue
		}

		var props map[string]interface{}
		if err := json.Unmarshal(propsJSON, &props); err != nil {
			continue
		}

		// Update candidate fields
		if name, ok := props["name"].(string); ok {
			candidates[i].Name = name
		}
		if pos, ok := props["current_position"].(string); ok {
			candidates[i].CurrentPosition = pos
		}
		if sen, ok := props["seniority"].(string); ok {
			candidates[i].Seniority = sen
		}
		if exp, ok := props["total_experience_years"].(float64); ok {
			candidates[i].TotalExperienceYears = int(exp)
		}

		// Load skills
		skillRows, err := h.db.QueryContext(ctx, `
			SELECT s.properties, e.properties
			FROM graph_nodes p
			JOIN graph_edges e ON p.id = e.source_node_id
			JOIN graph_nodes s ON e.target_node_id = s.id
			WHERE p.node_id = $1
			  AND e.edge_type = 'HAS_SKILL'
			  AND s.node_type = 'skill'
		`, candidates[i].PersonID)

		if err == nil {
			defer skillRows.Close()
			for skillRows.Next() {
				var skillPropsJSON, edgePropsJSON []byte
				if err := skillRows.Scan(&skillPropsJSON, &edgePropsJSON); err == nil {
					var skillProps, edgeProps map[string]interface{}
					if err := json.Unmarshal(skillPropsJSON, &skillProps); err == nil {
						// Safe type assertion for name
						name, ok := skillProps["name"].(string)
						if !ok {
							continue
						}
						skill := SkillNode{
							Name: name,
						}
						if prof, ok := skillProps["proficiency"].(string); ok {
							skill.Proficiency = prof
						}
						// Load years from edge properties
						if err := json.Unmarshal(edgePropsJSON, &edgeProps); err == nil {
							if prof, ok := edgeProps["proficiency"].(string); ok && skill.Proficiency == "" {
								skill.Proficiency = prof
							}
							if years, ok := edgeProps["years_of_experience"].(float64); ok {
								skill.YearsOfExperience = int(years)
							}
						}
						candidates[i].Skills = append(candidates[i].Skills, skill)
					}
				}
			}
		}

		// Load companies
		companyRows, err := h.db.QueryContext(ctx, `
			SELECT c.properties, e.properties
			FROM graph_nodes p
			JOIN graph_edges e ON p.id = e.source_node_id
			JOIN graph_nodes c ON e.target_node_id = c.id
			WHERE p.node_id = $1
			  AND e.edge_type = 'WORKED_AT'
			  AND c.node_type = 'company'
		`, candidates[i].PersonID)

		if err == nil {
			defer companyRows.Close()
			for companyRows.Next() {
				var companyPropsJSON, edgePropsJSON []byte
				if err := companyRows.Scan(&companyPropsJSON, &edgePropsJSON); err == nil {
					var companyProps, edgeProps map[string]interface{}
					if err := json.Unmarshal(companyPropsJSON, &companyProps); err == nil {
						company := CompanyNode{
							Name: companyProps["name"].(string),
						}
						if err := json.Unmarshal(edgePropsJSON, &edgeProps); err == nil {
							if pos, ok := edgeProps["position"].(string); ok {
								company.Position = pos
							}
							if isCurr, ok := edgeProps["is_current"].(bool); ok {
								company.IsCurrent = isCurr
							}
						}
						candidates[i].Companies = append(candidates[i].Companies, company)
					}
				}
			}
		}
	}
}
