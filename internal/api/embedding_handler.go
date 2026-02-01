package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// GenerateEmbeddingsHandler generates embeddings for all nodes in the graph
// @Summary Generate Vector Embeddings
// @Description Generate OpenAI embeddings for all nodes in the knowledge graph
// @Tags graphrag
// @Accept json
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 500 {object} map[string]string
// @Router /graphrag/embeddings/generate [post]
func (a *API) GenerateEmbeddingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if enhanced search engine is available
	if a.enhancedSearchEngine == nil {
		http.Error(w, "Vector embeddings not available (OpenAI API key not configured)", http.StatusServiceUnavailable)
		return
	}

	log.Printf("[Embeddings API] Queueing background embedding generation...")

	// Get all nodes without embeddings
	rows, err := a.db.GetConnection().QueryContext(r.Context(), `
		SELECT node_id 
		FROM graph_nodes 
		WHERE embedding IS NULL
		ORDER BY created_at DESC
	`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
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

	if len(nodeIDs) == 0 {
		response := map[string]interface{}{
			"success": true,
			"message": "All nodes already have embeddings",
			"stats": map[string]int{
				"pending_nodes": 0,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}

	// Queue job for background processing
	a.QueueEmbeddingJob(0, nodeIDs) // CV ID = 0 for batch jobs

	estimatedTime := time.Duration(len(nodeIDs)*200) * time.Millisecond // 0.2 seconds per node

	response := map[string]interface{}{
		"success":         true,
		"message":         fmt.Sprintf("Background embedding generation started for %d nodes", len(nodeIDs)),
		"pending_nodes":   len(nodeIDs),
		"estimated_time":  estimatedTime.String(),
		"rate_limit_info": "0.2 seconds between requests (5 req/sec - Tier 2 safe)",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// DetectCommunitiesHandler runs community detection using Leiden algorithm
// @Summary Detect Communities
// @Description Run Leiden algorithm to detect communities in the knowledge graph
// @Tags graphrag
// @Accept json
// @Produce json
// @Param level query int false "Hierarchy level (default: 0)"
// @Success 200 {object} map[string]interface{}
// @Failure 500 {object} map[string]string
// @Router /graphrag/communities/detect [post]
func (a *API) DetectCommunitiesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if enhanced search engine is available
	if a.enhancedSearchEngine == nil {
		http.Error(w, "Community detection not available (LLM not configured)", http.StatusServiceUnavailable)
		return
	}

	// Get level from query params (default: 0)
	level := 0
	if levelStr := r.URL.Query().Get("level"); levelStr != "" {
		_, err := fmt.Sscanf(levelStr, "%d", &level)
		if err != nil {
			http.Error(w, "Invalid level parameter", http.StatusBadRequest)
			return
		}
	}

	log.Printf("[Communities API] Starting community detection (level=%d)...", level)
	startTime := time.Now()

	// Run community detection
	err := a.enhancedSearchEngine.GetCommunityDetector().DetectCommunities(r.Context(), level)
	if err != nil {
		log.Printf("[Communities API] Failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	processingTime := time.Since(startTime)
	log.Printf("[Communities API] Completed in %v", processingTime)

	// Get stats
	var stats struct {
		TotalCommunities int `json:"total_communities"`
		TotalMembers     int `json:"total_members"`
	}

	err = a.db.GetConnection().QueryRowContext(r.Context(), `
		SELECT 
			COUNT(DISTINCT community_id) as communities,
			COUNT(*) as members
		FROM community_members
		WHERE level = $1
	`, level).Scan(&stats.TotalCommunities, &stats.TotalMembers)

	if err != nil {
		log.Printf("[Communities API] Stats query failed: %v", err)
	}

	response := map[string]interface{}{
		"success":         true,
		"processing_time": processingTime.String(),
		"level":           level,
		"stats":           stats,
		"message":         "Community detection completed successfully",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
