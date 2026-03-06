package graphrag

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// HybridSearchEngine combines BM25 + Vector + Graph search
// Implements Reciprocal Rank Fusion (RRF) for combining results
type HybridSearchEngine struct {
	db               *sql.DB
	bm25Searcher     *BM25Searcher
	embeddingService *EmbeddingService
	graphQuerier     *GraphQuerier
	llm              LLMClient
	scorer           *LLMScorer     // persistent across requests so its LLM cache survives between searches
	semanticCache    *SemanticCache // skip full pipeline for semantically identical queries
}

func NewHybridSearchEngine(db *sql.DB, llm LLMClient, openaiKey string) *HybridSearchEngine {
	return &HybridSearchEngine{
		db:               db,
		bm25Searcher:     NewBM25Searcher(db),
		embeddingService: NewEmbeddingService(openaiKey, db),
		graphQuerier:     NewGraphQuerier(db),
		llm:              llm,
		scorer:           NewLLMScorer(llm),
		semanticCache:    NewSemanticCache(30*time.Minute, 0.95),
	}
}

// ReEmbedPersonNode regenerates the vector embedding for a person node, enriching it with
// current interview notes. Call this after any interview write to keep search signals fresh.
// notes should be all interview notes for the candidate (fetched via DB).
func (h *HybridSearchEngine) ReEmbedPersonNode(ctx context.Context, graphNodeID int, notes []string) error {
	if h.embeddingService == nil {
		return fmt.Errorf("embedding service not available")
	}
	return h.embeddingService.ReEmbedPersonNodeByID(ctx, graphNodeID, notes)
}

// InterviewContext holds lightweight interview data attached to a search candidate.
// Used for outcome-based score adjustments and LLM prompt enrichment.
type InterviewContext struct {
	ID              int
	InterviewDate   time.Time
	Team            string
	InterviewerName string
	InterviewType   string
	Outcome         string // passed, failed, pending
	Notes           string // available for re-embedding; excluded from API response
}

// FusedCandidate represents a candidate with scores from multiple sources
type FusedCandidate struct {
	CandidateID              int
	GraphNodeIntID           int    // graph_nodes.id (integer PK), needed for interview lookup
	PersonID                 string // Graph uses string IDs
	Name                     string
	CurrentPosition          string
	Seniority                string
	TotalExperienceYears     int
	Skills                   []SkillNode
	Companies                []CompanyNode
	Interviews               []InterviewContext // loaded during enrichment
	Community                string             // Primary community
	Communities              []string           // All matching communities (Microsoft GraphRAG style)
	CommunityScores          map[string]float64 // Normalized scores for each community
	ComputedCommunityID      string             // ID of the graph-computed community this person belongs to
	ComputedCommunitySummary string             // LLM summary of that community
	BM25Score                float64            // 0-1 normalized
	VectorScore              float64            // 0-1 normalized
	GraphScore               float64            // 0-1 normalized
	FusionScore              float64            // Weighted combination
	LLMScore                 float64            // Final LLM reranking score (0-100)
	LLMReasoning             string
	Rank                     int
}

// VectorSearchResult represents a candidate from vector search
type VectorSearchResult struct {
	CandidateID int
	PersonID    string
	Similarity  float64
}

// HybridSearchConfig defines weights for fusion
type HybridSearchConfig struct {
	BM25Weight         float64 // Default: 0.3
	VectorWeight       float64 // Default: 0.4
	GraphWeight        float64 // Default: 0.3
	TopK               int     // How many candidates to retrieve from each source
	FinalTopN          int     // How many to send to LLM for reranking
	UseCommunityFilter bool    // Enable community-based filtering (default: false, enabled at 50+ candidates)
	CommunityThreshold int     // Auto-enable community filter at this candidate count (default: 50)
}

func DefaultHybridConfig() HybridSearchConfig {
	return HybridSearchConfig{
		BM25Weight:         0.2, // Full-text search on candidates.search_vector (name + skills + experience)
		VectorWeight:       0.5,
		GraphWeight:        0.3,
		TopK:               100,
		FinalTopN:          8, // Skill-based searches bypass this (see Step 2.55). Used only for skill-less queries as an LLM cost guard.
		UseCommunityFilter: false,
		CommunityThreshold: 10,
	}
}

// Search performs hybrid search with fusion
func (h *HybridSearchEngine) Search(ctx context.Context, query string, config HybridSearchConfig) ([]FusedCandidate, error) {
	log.Printf("[HybridSearch] Starting search for: %s", query)

	// Semantic cache: if a semantically identical query ran recently, return immediately (<5ms)
	var queryEmbedding []float32
	var embErr error
	queryEmbedding, embErr = h.embeddingService.GenerateEmbedding(ctx, query)
	if embErr == nil {
		if cached, cachedQuery, found := h.semanticCache.Get(queryEmbedding); found {
			log.Printf("[HybridSearch] Semantic cache HIT (similar to: %q) → %d cached results", cachedQuery, len(cached))
			return cached, nil
		}
	} else {
		log.Printf("[HybridSearch] Semantic cache embedding failed: %v", embErr)
	}

	// Clear ALL prepared statements once before parallel retrieval to prevent cache collisions
	h.db.Exec("DEALLOCATE ALL")

	// Step 1: Parallel retrieval from 3 sources
	bm25ResultsChan := make(chan []BM25Result)
	vectorResultsChan := make(chan []VectorSearchResult)

	type graphSearchResult struct {
		criteria *SearchCriteria
		results  []CandidateResult
	}
	graphResultsChan := make(chan graphSearchResult)
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

	// Vector search — reuse the embedding already generated for semantic cache (saves ~2s API call)
	go func() {
		var personIDs []string
		var similarities []float64
		var err error
		if queryEmbedding != nil {
			personIDs, similarities, err = h.embeddingService.SimilaritySearchByEmbedding(ctx, queryEmbedding, config.TopK)
		} else {
			personIDs, similarities, err = h.embeddingService.SimilaritySearch(ctx, query, config.TopK)
		}
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

	// Graph search (needs criteria extraction first; sends criteria alongside results for post-fusion filtering)
	go func() {
		analyzer := NewQueryAnalyzer(h.llm)
		criteria, err := analyzer.AnalyzeQuery(ctx, query)
		if err != nil {
			log.Printf("[HybridSearch] Graph search skipped (criteria extraction failed): %v", err)
			graphResultsChan <- graphSearchResult{criteria: &SearchCriteria{}, results: []CandidateResult{}}
			return
		}

		results, err := h.graphQuerier.QueryGraph(ctx, criteria)
		if err != nil {
			errChan <- fmt.Errorf("graph failed: %w", err)
			return
		}
		graphResultsChan <- graphSearchResult{criteria: criteria, results: results}
	}()

	// Wait for all results
	var bm25Results []BM25Result
	var vectorResults []VectorSearchResult
	var graphResults []CandidateResult
	var searchCriteria *SearchCriteria

	for i := 0; i < 3; i++ {
		select {
		case bm25Results = <-bm25ResultsChan:
			log.Printf("[HybridSearch] BM25 returned %d results", len(bm25Results))
		case vectorResults = <-vectorResultsChan:
			log.Printf("[HybridSearch] Vector returned %d results", len(vectorResults))
		case gr := <-graphResultsChan:
			graphResults = gr.results
			searchCriteria = gr.criteria
			log.Printf("[HybridSearch] Graph returned %d results", len(graphResults))
		case err := <-errChan:
			return nil, err
		}
	}

	// Step 2: Fuse results using RRF (Reciprocal Rank Fusion)
	fusedCandidates := h.fuseResults(bm25Results, vectorResults, graphResults, config)

	// Step 2.5: Enrich candidates with full details (skills, companies, computed communities)
	h.enrichCandidates(ctx, fusedCandidates)

	// Step 2.55: Post-fusion skill filter.
	// Vector search returns semantically similar CVs regardless of tech stack.
	// If the query specifies skills, remove candidates who have none of them —
	// they are noise picked up by vector similarity alone.
	//
	// skillFilterActive = true when the filter actually eliminated at least one candidate.
	// This signal is used downstream to bypass the community filter and FinalTopN cut:
	// if skills are confirmed, domain is already guaranteed — further filtering only causes harm.
	skillFilterActive := false
	if searchCriteria != nil && len(searchCriteria.Skills) > 0 {
		requiredSkills := make(map[string]bool, len(searchCriteria.Skills))
		for _, s := range searchCriteria.Skills {
			requiredSkills[strings.ToLower(s)] = true
		}
		skillFiltered := make([]FusedCandidate, 0, len(fusedCandidates))
		for _, c := range fusedCandidates {
			for _, sk := range c.Skills {
				if requiredSkills[strings.ToLower(sk.Name)] {
					skillFiltered = append(skillFiltered, c)
					break
				}
			}
		}
		if len(skillFiltered) > 0 {
			skillFilterActive = len(skillFiltered) < len(fusedCandidates)
			log.Printf("[HybridSearch] Skill post-filter: %d → %d candidates (kept skill-relevant only)",
				len(fusedCandidates), len(skillFiltered))
			fusedCandidates = skillFiltered
		} else {
			log.Printf("[HybridSearch] Skill post-filter matched 0 candidates, skipping filter")
		}
	}

	// Step 2.6: Fetch global community context for this query (for LLM scoring context)
	var queryCommunityContext []string
	if queryEmbedding != nil {
		queryCommunityContext = h.fetchQueryCommunities(ctx, queryEmbedding)
		if len(queryCommunityContext) > 0 {
			log.Printf("[HybridSearch] Found %d relevant graph communities for query context", len(queryCommunityContext))
		}
	}

	// Step 2.7: Community-based filtering (if enabled) - Microsoft GraphRAG style
	// Skipped when skillFilterActive=true: skill match already guarantees domain relevance.
	// Used as the primary narrowing mechanism when no skills were extracted (e.g. "Senior Backend Developer").
	shouldUseCommunityFilter := !skillFilterActive && (config.UseCommunityFilter || len(fusedCandidates) >= config.CommunityThreshold)
	if shouldUseCommunityFilter && len(fusedCandidates) > 0 {
		// Infer relevant communities from query
		queryCommunities := FindCommunitiesByQuery(query)
		log.Printf("[HybridSearch] Community filter enabled. Query matches communities: %v", queryCommunities)

		// Filter candidates by overlapping communities (Microsoft GraphRAG approach)
		filteredCandidates := make([]FusedCandidate, 0, len(fusedCandidates))
		for _, candidate := range fusedCandidates {
			// Check if candidate has ANY matching community
			matched := false
			for _, qc := range queryCommunities {
				for _, candidateCommunity := range candidate.Communities {
					if candidateCommunity == qc {
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}

			if matched {
				filteredCandidates = append(filteredCandidates, candidate)
			}
		}

		if len(filteredCandidates) > 0 {
			log.Printf("[HybridSearch] Community filter reduced candidates from %d to %d",
				len(fusedCandidates), len(filteredCandidates))
			fusedCandidates = filteredCandidates
		} else {
			log.Printf("[HybridSearch] Community filter matched 0 candidates, keeping all")
		}
	}

	// Step 2.8: Interview outcome modifier.
	// Adjusts FusionScore based on the most recent interview result.
	// Applied before top-N cut so outcome-boosted candidates rank higher going into LLM.
	for i := range fusedCandidates {
		if len(fusedCandidates[i].Interviews) == 0 {
			continue
		}
		// Interviews are ordered DESC by date; first entry is the most recent
		switch fusedCandidates[i].Interviews[0].Outcome {
		case "passed":
			fusedCandidates[i].FusionScore = min(1.0, fusedCandidates[i].FusionScore+0.05)
		case "failed":
			fusedCandidates[i].FusionScore = max(0.0, fusedCandidates[i].FusionScore-0.10)
		}
	}

	// Step 3: Take top N for LLM reranking.
	// Skipped when skillFilterActive=true: all skill-matched candidates go to LLM regardless of count.
	// This ensures graph-only candidates (e.g. Architects with the right skill) aren't cut by RRF vector bias.
	// For skill-less queries, FinalTopN acts as an LLM cost guard.
	if !skillFilterActive && config.FinalTopN > 0 && len(fusedCandidates) > config.FinalTopN {
		fusedCandidates = fusedCandidates[:config.FinalTopN]
	}

	log.Printf("[HybridSearch] Fusion complete. Top %d candidates ready for LLM reranking", len(fusedCandidates))

	// Step 4: LLM Reranking — persistent scorer keeps its cache alive across requests
	llmScores, err := h.scorer.ScoreCandidates(ctx, query, fusedCandidates, queryCommunityContext)
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

	// Store results in semantic cache for future similar queries
	if embErr == nil {
		h.semanticCache.Set(queryEmbedding, query, validCandidates)
		log.Printf("[HybridSearch] Results stored in semantic cache (30m TTL)")
	}

	return validCandidates, nil
}

// fetchQueryCommunities finds the most relevant graph-computed communities for a query
// using vector similarity. Returns community summaries to use as global LLM context.
func (h *HybridSearchEngine) fetchQueryCommunities(ctx context.Context, embedding []float32) []string {
	if len(embedding) == 0 {
		return nil
	}

	embeddingJSON, err := json.Marshal(embedding)
	if err != nil {
		return nil
	}

	rows, err := h.db.QueryContext(ctx, `
		SELECT summary
		FROM graph_communities
		WHERE embedding IS NOT NULL AND level = 0
		ORDER BY embedding <=> $1::vector
		LIMIT 3
	`, string(embeddingJSON))
	if err != nil {
		log.Printf("[HybridSearch] fetchQueryCommunities failed (non-fatal): %v", err)
		return nil
	}
	defer rows.Close()

	var summaries []string
	for rows.Next() {
		var summary sql.NullString
		if err := rows.Scan(&summary); err != nil {
			continue
		}
		if summary.Valid && summary.String != "" {
			summaries = append(summaries, summary.String)
		}
	}
	return summaries
}

// fuseResults combines results using weighted scoring
func (h *HybridSearchEngine) fuseResults(
	bm25 []BM25Result,
	vector []VectorSearchResult,
	graph []CandidateResult,
	config HybridSearchConfig,
) []FusedCandidate {
	// Create a map of candidate scores (keyed by PersonID string)
	scoreMap := make(map[string]*FusedCandidate)

	// Normalize and add BM25 scores.
	// Use graph node_id as key so results merge with vector/graph sources.
	// Fall back to cand_X if node_id is empty (shouldn't happen in practice).
	maxBM25 := maxBM25Score(bm25)
	for i, r := range bm25 {
		candidateKey := r.NodeID
		if candidateKey == "" {
			candidateKey = fmt.Sprintf("cand_%d", r.CandidateID)
		}
		if _, exists := scoreMap[candidateKey]; !exists {
			scoreMap[candidateKey] = &FusedCandidate{
				PersonID:    candidateKey,
				CandidateID: r.CandidateID,
				Name:        r.Name,
			}
		}
		normalizedScore := r.Rank / maxBM25
		rrfScore := 1.0 / float64(60+i+1) // k=60 is standard RRF constant
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
// OPTIMIZED: Batch loading to avoid N+1 queries
func (h *HybridSearchEngine) enrichCandidates(ctx context.Context, candidates []FusedCandidate) {
	if len(candidates) == 0 {
		return
	}

	// Build person ID list for batch queries
	personIDs := make([]interface{}, 0, len(candidates))
	personIDToIndex := make(map[string]int)
	for i := range candidates {
		if candidates[i].PersonID != "" {
			personIDs = append(personIDs, candidates[i].PersonID)
			personIDToIndex[candidates[i].PersonID] = i
		}
	}

	if len(personIDs) == 0 {
		return
	}

	// Build placeholders for IN clause ($1, $2, $3, ...)
	placeholders := make([]string, len(personIDs))
	for i := range personIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	inClause := "(" + strings.Join(placeholders, ",") + ")"

	// BATCH 1: Load all person details in one query
	// Also fetch integer id so we can correlate with interviews.candidate_id via candidates.graph_node_id
	personQuery := fmt.Sprintf(`
		SELECT id, node_id, properties
		FROM graph_nodes
		WHERE node_id IN %s AND node_type = 'person'
	`, inClause)

	personRows, err := h.db.QueryContext(ctx, personQuery, personIDs...)
	if err != nil {
		log.Printf("[HybridSearch] Failed to batch load persons: %v", err)
		return
	}
	defer personRows.Close()

	// Track integer IDs for interview batch lookup
	nodeIntIDIndex := make(map[int]int) // graphNodeIntID → candidate slice index

	for personRows.Next() {
		var nodeIntID int
		var personID string
		var propsJSON []byte
		if err := personRows.Scan(&nodeIntID, &personID, &propsJSON); err != nil {
			continue
		}

		idx, ok := personIDToIndex[personID]
		if !ok {
			continue
		}
		candidates[idx].GraphNodeIntID = nodeIntID
		nodeIntIDIndex[nodeIntID] = idx

		var props map[string]interface{}
		if err := json.Unmarshal(propsJSON, &props); err != nil {
			continue
		}

		// Update candidate fields
		if name, ok := props["name"].(string); ok {
			candidates[idx].Name = name
		}
		if pos, ok := props["current_position"].(string); ok {
			candidates[idx].CurrentPosition = pos
		}
		if sen, ok := props["seniority"].(string); ok {
			candidates[idx].Seniority = sen
		}
		if exp, ok := props["total_experience_years"].(float64); ok {
			candidates[idx].TotalExperienceYears = int(exp)
		}
		if comm, ok := props["community"].(string); ok {
			candidates[idx].Community = comm
		}
		if communities, ok := props["communities"].([]interface{}); ok {
			commList := make([]string, 0, len(communities))
			for _, c := range communities {
				if commStr, ok := c.(string); ok {
					commList = append(commList, commStr)
				}
			}
			candidates[idx].Communities = commList
		}
	}

	// BATCH 2: Load all skills in one query
	skillQuery := fmt.Sprintf(`
		SELECT p.node_id, s.properties, e.properties
		FROM graph_nodes p
		JOIN graph_edges e ON p.id = e.source_node_id
		JOIN graph_nodes s ON e.target_node_id = s.id
		WHERE p.node_id IN %s
		  AND e.edge_type = 'HAS_SKILL'
		  AND s.node_type = 'skill'
	`, inClause)

	skillRows, err := h.db.QueryContext(ctx, skillQuery, personIDs...)
	if err != nil {
		log.Printf("[HybridSearch] Failed to batch load skills: %v", err)
	} else {
		defer skillRows.Close()
		for skillRows.Next() {
			var personID string
			var skillPropsJSON, edgePropsJSON []byte
			if err := skillRows.Scan(&personID, &skillPropsJSON, &edgePropsJSON); err != nil {
				continue
			}

			idx, ok := personIDToIndex[personID]
			if !ok {
				continue
			}

			var skillProps, edgeProps map[string]interface{}
			if err := json.Unmarshal(skillPropsJSON, &skillProps); err != nil {
				continue
			}

			name, ok := skillProps["name"].(string)
			if !ok {
				continue
			}

			skill := SkillNode{Name: name}
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

			candidates[idx].Skills = append(candidates[idx].Skills, skill)
		}
	}

	// BATCH 3: Load all companies in one query
	companyQuery := fmt.Sprintf(`
		SELECT p.node_id, c.properties, e.properties
		FROM graph_nodes p
		JOIN graph_edges e ON p.id = e.source_node_id
		JOIN graph_nodes c ON e.target_node_id = c.id
		WHERE p.node_id IN %s
		  AND e.edge_type = 'WORKED_AT'
		  AND c.node_type = 'company'
	`, inClause)

	companyRows, err := h.db.QueryContext(ctx, companyQuery, personIDs...)
	if err != nil {
		log.Printf("[HybridSearch] Failed to batch load companies: %v", err)
	} else {
		defer companyRows.Close()
		for companyRows.Next() {
			var personID string
			var companyPropsJSON, edgePropsJSON []byte
			if err := companyRows.Scan(&personID, &companyPropsJSON, &edgePropsJSON); err != nil {
				continue
			}

			idx, ok := personIDToIndex[personID]
			if !ok {
				continue
			}

			var companyProps, edgeProps map[string]interface{}
			if err := json.Unmarshal(companyPropsJSON, &companyProps); err != nil {
				continue
			}

			name, ok := companyProps["name"].(string)
			if !ok {
				continue
			}

			company := CompanyNode{Name: name}
			if err := json.Unmarshal(edgePropsJSON, &edgeProps); err == nil {
				if pos, ok := edgeProps["position"].(string); ok {
					company.Position = pos
				}
				if isCurr, ok := edgeProps["is_current"].(bool); ok {
					company.IsCurrent = isCurr
				}
			}

			candidates[idx].Companies = append(candidates[idx].Companies, company)
		}
	}

	// BATCH 4: Load computed community from graph_communities (the real Leiden-computed communities)
	if len(personIDs) > 0 {
		communityMemberQuery := fmt.Sprintf(`
			SELECT p.node_id, gc.community_id, gc.summary
			FROM graph_nodes p
			JOIN community_members cm ON p.id = cm.node_id
			JOIN graph_communities gc ON cm.community_id = gc.id
			WHERE p.node_id IN %s
			  AND p.node_type = 'person'
			  AND gc.summary IS NOT NULL
			ORDER BY gc.node_count DESC
		`, inClause)

		communityRows, err := h.db.QueryContext(ctx, communityMemberQuery, personIDs...)
		if err != nil {
			log.Printf("[HybridSearch] Failed to batch load computed communities (non-fatal): %v", err)
		} else {
			defer communityRows.Close()
			for communityRows.Next() {
				var personID, communityID string
				var summary sql.NullString
				if err := communityRows.Scan(&personID, &communityID, &summary); err != nil {
					continue
				}
				idx, ok := personIDToIndex[personID]
				if !ok {
					continue
				}
				// Only set if not already set (keep the highest node_count community due to ORDER BY)
				if candidates[idx].ComputedCommunityID == "" {
					candidates[idx].ComputedCommunityID = communityID
					if summary.Valid {
						candidates[idx].ComputedCommunitySummary = summary.String
					}
				}
			}
		}
	}

	// Assign keyword-based communities from freshly-loaded skills (used for community filter).
	// Always recompute from skills — stored `community` prop may be stale or missing Communities list.
	for i := range candidates {
		if len(candidates[i].Skills) > 0 {
			primary, communities, scores := FindCommunities(candidates[i].Skills, 0.3)
			candidates[i].Community = primary
			candidates[i].Communities = communities
			candidates[i].CommunityScores = scores
		}
	}

	// BATCH 5: Load interviews for all candidates via candidates.graph_node_id
	if len(nodeIntIDIndex) > 0 {
		nodeIntIDs := make([]interface{}, 0, len(nodeIntIDIndex))
		for nodeIntID := range nodeIntIDIndex {
			nodeIntIDs = append(nodeIntIDs, nodeIntID)
		}
		interviewPlaceholders := make([]string, len(nodeIntIDs))
		for i := range nodeIntIDs {
			interviewPlaceholders[i] = fmt.Sprintf("$%d", i+1)
		}
		interviewQuery := fmt.Sprintf(`
			SELECT c.graph_node_id, i.id, i.interview_date,
			       COALESCE(i.team,''), COALESCE(i.interviewer_name,''),
			       COALESCE(i.interview_type,''), COALESCE(i.outcome,''), COALESCE(i.notes,'')
			FROM interviews i
			JOIN candidates c ON c.id = i.candidate_id
			WHERE c.graph_node_id IN (%s)
			ORDER BY i.interview_date DESC
		`, strings.Join(interviewPlaceholders, ","))

		ivRows, ivErr := h.db.QueryContext(ctx, interviewQuery, nodeIntIDs...)
		if ivErr != nil {
			log.Printf("[HybridSearch] Failed to batch load interviews (non-fatal): %v", ivErr)
		} else {
			defer ivRows.Close()
			for ivRows.Next() {
				var nodeIntID, ivID int
				var iv InterviewContext
				if err := ivRows.Scan(
					&nodeIntID, &ivID, &iv.InterviewDate,
					&iv.Team, &iv.InterviewerName, &iv.InterviewType, &iv.Outcome, &iv.Notes,
				); err != nil {
					continue
				}
				iv.ID = ivID
				if idx, ok := nodeIntIDIndex[nodeIntID]; ok {
					candidates[idx].Interviews = append(candidates[idx].Interviews, iv)
				}
			}
		}
	}
}
