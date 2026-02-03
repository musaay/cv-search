package api

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"cv-search/internal/graphrag"
)

// HybridSearchRequest represents a hybrid search request
type HybridSearchRequest struct {
	Query        string  `json:"query"`
	BM25Weight   float64 `json:"bm25_weight,omitempty"`   // Default: 0.3
	VectorWeight float64 `json:"vector_weight,omitempty"` // Default: 0.4
	GraphWeight  float64 `json:"graph_weight,omitempty"`  // Default: 0.3
	TopK         int     `json:"top_k,omitempty"`         // Per-source retrieval limit (default: 100)
	FinalTopN    int     `json:"final_top_n,omitempty"`   // Max candidates to send to LLM (default: 0 = all)
}

// HybridSearchResponse represents the response
type HybridSearchResponse struct {
	Query          string                      `json:"query"`
	Candidates     []FusedCandidateResponse    `json:"candidates"`
	TotalFound     int                         `json:"total_found"`
	ProcessingTime string                      `json:"processing_time"`
	Method         string                      `json:"method"`
	Config         graphrag.HybridSearchConfig `json:"config"`
}

// FusedCandidateResponse represents a candidate with all scores
type FusedCandidateResponse struct {
	PersonID             string                 `json:"person_id"`
	Name                 string                 `json:"name"`
	CurrentPosition      string                 `json:"current_position,omitempty"`
	Seniority            string                 `json:"seniority,omitempty"`
	TotalExperienceYears int                    `json:"total_experience_years,omitempty"`
	Skills               []graphrag.SkillNode   `json:"skills,omitempty"`
	Companies            []graphrag.CompanyNode `json:"companies,omitempty"`
	Community            string                 `json:"community,omitempty"`        // Primary community
	Communities          []string               `json:"communities,omitempty"`      // All matching communities
	CommunityScores      map[string]float64     `json:"community_scores,omitempty"` // Score for each community
	BM25Score            float64                `json:"bm25_score"`
	VectorScore          float64                `json:"vector_score"`
	GraphScore           float64                `json:"graph_score"`
	FusionScore          float64                `json:"fusion_score"`
	LLMScore             float64                `json:"llm_score"`
	LLMReasoning         string                 `json:"llm_reasoning,omitempty"`
	Rank                 int                    `json:"rank"`
}

// HybridSearchHandler handles hybrid search requests
// Combines BM25 + Vector + Graph + LLM scoring
// @Summary Hybrid Search (BM25 + Vector + Graph + LLM)
// @Description Search candidates using multi-source retrieval with fusion and pure LLM reranking
// @Tags search
// @Accept json
// @Produce json
// @Param request body HybridSearchRequest true "Hybrid search request"
// @Success 200 {object} HybridSearchResponse
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /search/hybrid [post]
func (a *API) HybridSearchHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if a.hybridSearchEngine == nil {
		http.Error(w, "Hybrid search not available (OpenAI API key required)", http.StatusServiceUnavailable)
		return
	}

	var req HybridSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Query == "" {
		http.Error(w, "Query cannot be empty", http.StatusBadRequest)
		return
	}

	// Build config with defaults
	config := graphrag.DefaultHybridConfig()
	if req.BM25Weight > 0 {
		config.BM25Weight = req.BM25Weight
	}
	if req.VectorWeight > 0 {
		config.VectorWeight = req.VectorWeight
	}
	if req.GraphWeight > 0 {
		config.GraphWeight = req.GraphWeight
	}
	if req.TopK > 0 {
		config.TopK = req.TopK
	}
	if req.FinalTopN > 0 {
		config.FinalTopN = req.FinalTopN
	}

	// Validate weights sum to ~1.0
	totalWeight := config.BM25Weight + config.VectorWeight + config.GraphWeight
	if totalWeight < 0.9 || totalWeight > 1.1 {
		http.Error(w, "Weights must sum to 1.0", http.StatusBadRequest)
		return
	}

	startTime := time.Now()

	log.Printf("[API] Hybrid search: %s (BM25=%.2f, Vector=%.2f, Graph=%.2f)",
		req.Query, config.BM25Weight, config.VectorWeight, config.GraphWeight)

	// Perform hybrid search
	results, err := a.hybridSearchEngine.Search(r.Context(), req.Query, config)
	if err != nil {
		log.Printf("[API] Hybrid search failed: %v", err)
		http.Error(w, "Search failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	processingTime := time.Since(startTime)

	// Convert to response format
	var candidates []FusedCandidateResponse
	for _, c := range results {
		candidates = append(candidates, FusedCandidateResponse{
			PersonID:             c.PersonID,
			Name:                 c.Name,
			CurrentPosition:      c.CurrentPosition,
			Seniority:            c.Seniority,
			TotalExperienceYears: c.TotalExperienceYears,
			Skills:               c.Skills,
			Companies:            c.Companies,
			Community:            c.Community,
			Communities:          c.Communities,
			CommunityScores:      c.CommunityScores,
			BM25Score:            c.BM25Score,
			VectorScore:          c.VectorScore,
			GraphScore:           c.GraphScore,
			FusionScore:          c.FusionScore,
			LLMScore:             c.LLMScore,
			LLMReasoning:         c.LLMReasoning,
			Rank:                 c.Rank,
		})
	}

	response := HybridSearchResponse{
		Query:          req.Query,
		Candidates:     candidates,
		TotalFound:     len(candidates),
		ProcessingTime: processingTime.String(),
		Method:         "hybrid_fusion_llm",
		Config:         config,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)

	log.Printf("[API] Hybrid search completed in %s, found %d candidates", processingTime, len(candidates))
}
