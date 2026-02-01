package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"time"

	"cv-search/internal/llm"
)

// CVUploadHandler handles CV file uploads and extraction
// @Summary Upload and parse CV
// @Description Upload a CV file (PDF/DOCX) and extract entities using LLM
// @Tags cv
// @Accept multipart/form-data
// @Produce json
// @Param file formData file true "CV file (PDF or DOCX)"
// @Param candidate_id formData int false "Candidate ID (optional)"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /cv/upload [post]
func (a *API) CVUploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	startTime := time.Now()

	// Parse multipart form (max 10MB)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "file too large or invalid (max 10MB)", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "no file uploaded", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Validate file type
	ext := filepath.Ext(header.Filename)
	if ext != ".pdf" && ext != ".docx" && ext != ".doc" && ext != ".txt" {
		http.Error(w, "invalid file type (supported: PDF, DOCX, TXT)", http.StatusBadRequest)
		return
	}

	// Parse CV file (extract text)
	parsedCV, err := a.cvParser.ParseFile(header.Filename, file)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse CV: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("CV parsed: %s (%d bytes text)", parsedCV.Filename, len(parsedCV.FullText))

	// Save CV file to database
	cvID, err := a.db.SaveCVFile(r.Context(), nil, parsedCV.Filename,
		parsedCV.Filename, parsedCV.FileType, parsedCV.FullText, parsedCV.FileSize)
	if err != nil {
		log.Printf("Failed to save CV: %v", err)
		http.Error(w, "failed to save CV", http.StatusInternalServerError)
		return
	}

	log.Printf("CV saved to database with ID: %d", cvID)

	// Extract entities using LLM (if enabled)
	var extraction *llm.CVExtraction
	extractionMethod := "none"

	if a.llmService != nil {
		log.Println("Extracting entities using LLM...")
		extractionMethod = "llm"

		extraction, err = a.llmService.ExtractEntities(parsedCV.FullText)
		if err != nil {
			log.Printf("LLM extraction failed: %v", err)
			// Don't fail the request, just log and continue
			extractionMethod = "failed"
		} else {
			log.Printf("LLM extracted: %d skills, %d companies, %d education entries",
				len(extraction.Skills), len(extraction.Companies), len(extraction.Education))

			// Save extracted entities to cv_entities table
			for _, skill := range extraction.Skills {
				_ = a.db.SaveCVEntity(r.Context(), cvID, "skill", skill.Name, skill.Confidence)
			}
			for _, company := range extraction.Companies {
				_ = a.db.SaveCVEntity(r.Context(), cvID, "company", company.Name, company.Confidence)
			}
			for _, edu := range extraction.Education {
				_ = a.db.SaveCVEntity(r.Context(), cvID, "education", edu.Institution, 0.9)
			}
			for _, loc := range extraction.Locations {
				_ = a.db.SaveCVEntity(r.Context(), cvID, "location", loc, 0.85)
			}

			// Build graph from extraction
			if a.graphBuilder != nil {
				log.Println("Building knowledge graph...")

				// Convert extraction to map for graph builder
				extractionMap := map[string]interface{}{
					"candidate": map[string]interface{}{
						"name":                   extraction.Candidate.Name,
						"current_position":       extraction.Candidate.CurrentPosition,
						"seniority":              extraction.Candidate.Seniority,
						"total_experience_years": extraction.Candidate.TotalExperienceYears,
					},
					"skills":    extraction.Skills,
					"companies": extraction.Companies,
					"education": extraction.Education,
				}

				err = a.graphBuilder.BuildFromLLMExtraction(r.Context(), cvID, extractionMap)
				if err != nil {
					log.Printf("Graph building failed: %v", err)
				} else {
					log.Printf("Graph built successfully: %d skills, %d companies, %d education nodes",
						len(extraction.Skills), len(extraction.Companies), len(extraction.Education))

					// Queue background embedding job for newly created nodes
					newNodeIDs := a.collectNewNodeIDs(r.Context(), int64(cvID))
					if len(newNodeIDs) > 0 {
						a.QueueEmbeddingJob(int64(cvID), newNodeIDs)
						log.Printf("Queued %d nodes for background embedding", len(newNodeIDs))
					}
				}
			}
		}
	}

	processingTime := time.Since(startTime).Milliseconds()

	// Prepare response
	response := map[string]interface{}{
		"cv_id":              cvID,
		"filename":           parsedCV.Filename,
		"file_type":          parsedCV.FileType,
		"file_size":          parsedCV.FileSize,
		"text_length":        len(parsedCV.FullText),
		"extraction_method":  extractionMethod,
		"processing_time_ms": processingTime,
	}

	if extraction != nil {
		response["entities"] = map[string]interface{}{
			"candidate": extraction.Candidate,
			"skills":    extraction.Skills,
			"companies": extraction.Companies,
			"education": extraction.Education,
			"locations": extraction.Locations,
			"languages": extraction.Languages,
		}
		response["summary"] = map[string]int{
			"skills_count":    len(extraction.Skills),
			"companies_count": len(extraction.Companies),
			"education_count": len(extraction.Education),
		}
	}

	log.Printf("Sending response for CV %d (processing time: %dms)", cvID, processingTime)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("ERROR: Failed to encode JSON response: %v", err)
	} else {
		log.Printf("Response sent successfully for CV %d", cvID)
	}
}

// GetGraphStats returns graph statistics
// @Summary Get graph statistics
// @Description Get statistics about the knowledge graph (skill popularity, etc.)
// @Tags graph
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /graph/stats [get]
func (a *API) GetGraphStatsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Query graph statistics
	stats := map[string]interface{}{
		"total_nodes": 0,
		"total_edges": 0,
		"node_types":  map[string]int{},
	}

	// Count total nodes
	var totalNodes int
	err := a.db.GetConnection().QueryRow("SELECT COUNT(*) FROM graph_nodes").Scan(&totalNodes)
	if err == nil {
		stats["total_nodes"] = totalNodes
	}

	// Count total edges
	var totalEdges int
	err = a.db.GetConnection().QueryRow("SELECT COUNT(*) FROM graph_edges").Scan(&totalEdges)
	if err == nil {
		stats["total_edges"] = totalEdges
	}

	// Count nodes by type
	rows, err := a.db.GetConnection().Query(`
		SELECT node_type, COUNT(*) 
		FROM graph_nodes 
		GROUP BY node_type
	`)
	if err == nil {
		defer rows.Close()
		nodeTypes := make(map[string]int)
		for rows.Next() {
			var entityType string
			var count int
			rows.Scan(&entityType, &count)
			nodeTypes[entityType] = count
		}
		stats["node_types"] = nodeTypes
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// GetPopularSkills returns most popular skills from graph
// @Summary Get popular skills
// @Description Get most popular skills extracted from CVs
// @Tags graph
// @Produce json
// @Param limit query int false "Limit results" default(20)
// @Success 200 {object} map[string]interface{}
// @Router /graph/skills/popular [get]
func (a *API) GetPopularSkillsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Default limit
	limit := 20

	rows, err := a.db.GetConnection().Query(`
		SELECT 
			n.properties->>'name' as skill,
			COUNT(DISTINCT e.source_node_id) as candidate_count
		FROM graph_nodes n
		LEFT JOIN graph_edges e ON e.target_node_id = n.id AND e.edge_type = 'HAS_SKILL'
		WHERE n.node_type = 'skill'
		GROUP BY n.properties->>'name'
		HAVING COUNT(DISTINCT e.source_node_id) > 0
		ORDER BY candidate_count DESC
		LIMIT $1
	`, limit)

	if err != nil {
		log.Printf("Query error: %v", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type SkillStat struct {
		Skill string `json:"skill"`
		Count int    `json:"count"`
	}

	skills := []SkillStat{}
	for rows.Next() {
		var s SkillStat
		rows.Scan(&s.Skill, &s.Count)
		skills = append(skills, s)
	}

	response := map[string]interface{}{
		"total":  len(skills),
		"skills": skills,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// collectNewNodeIDs gets all nodes without embeddings (likely newly created from this CV)
func (a *API) collectNewNodeIDs(ctx context.Context, cvID int64) []string {
	rows, err := a.db.GetConnection().QueryContext(ctx, `
		SELECT node_id 
		FROM graph_nodes 
		WHERE embedding IS NULL
		ORDER BY created_at DESC
	`)

	if err != nil {
		log.Printf("Failed to query unembedded nodes: %v", err)
		return []string{}
	}
	defer rows.Close()

	nodeIDs := []string{}
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			continue
		}
		nodeIDs = append(nodeIDs, nodeID)
	}

	return nodeIDs
}
