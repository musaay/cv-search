package api

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// GraphRAGSearchRequest represents a natural language search request
type GraphRAGSearchRequest struct {
	Query string `json:"query"`
}

// GraphRAGSearchHandler handles natural language candidate search with GraphRAG
// @Summary GraphRAG Search (Vector + Community + LLM)
// @Description Search candidates using natural language with Vector embeddings, Community detection, and LLM reasoning (Microsoft GraphRAG style)
// @Tags graphrag
// @Accept json
// @Produce json
// @Param request body GraphRAGSearchRequest true "Natural language search query"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /graphrag/search [post]
func (a *API) GraphRAGSearchHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if any search engine is available
	if a.enhancedSearchEngine == nil && a.llmSearchEngine == nil {
		http.Error(w, "GraphRAG search not available (LLM not configured)", http.StatusServiceUnavailable)
		return
	}

	var req GraphRAGSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Query == "" {
		http.Error(w, "Query cannot be empty", http.StatusBadRequest)
		return
	}

	startTime := time.Now()

	// Use EnhancedSearchEngine if available (Vector + Community + LLM)
	// Otherwise fall back to LLM-only search
	if a.enhancedSearchEngine != nil {
		log.Printf("[Enhanced Search API] Using Vector + Community + LLM search for: %s", req.Query)

		enhancedResult, err := a.enhancedSearchEngine.Search(r.Context(), req.Query)
		if err != nil {
			log.Printf("[Enhanced Search API] Search failed: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		processingTime := time.Since(startTime)
		log.Printf("[Enhanced Search API] Search completed in %v: %d candidates found", processingTime, len(enhancedResult.Candidates))

		response := map[string]interface{}{
			"query":                req.Query,
			"candidates":           enhancedResult.Candidates,
			"total_found":          len(enhancedResult.Candidates),
			"search_method":        enhancedResult.SearchMethod,
			"relevant_communities": enhancedResult.RelevantCommunities,
			"reasoning":            enhancedResult.Reasoning,
			"processing_time":      processingTime.String(),
			"vector_search_used":   enhancedResult.SearchMethod == "vector+community+llm" || enhancedResult.SearchMethod == "vector+llm",
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}

	// Fallback to LLM-only search
	log.Printf("[LLM Search API] Using LLM-only search for: %s", req.Query)

	result, err := a.llmSearchEngine.Search(r.Context(), req.Query)
	if err != nil {
		log.Printf("[LLM Search API] Search failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	processingTime := time.Since(startTime)
	log.Printf("[LLM Search API] Search completed in %v: %d candidates found", processingTime, result.TotalFound)

	response := map[string]interface{}{
		"query":              req.Query,
		"candidates":         result.Candidates,
		"summary":            result.Summary,
		"total_found":        result.TotalFound,
		"reasoning":          result.Reasoning,
		"processing_time":    processingTime.String(),
		"search_method":      "llm-only",
		"vector_search_used": false,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
