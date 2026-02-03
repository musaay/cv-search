package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"
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

	// Calculate content hash for duplicate detection
	hash := sha256.Sum256([]byte(parsedCV.FullText))
	contentHash := hex.EncodeToString(hash[:])
	log.Printf("[DUPLICATE CHECK] Content hash: %s (length: %d)", contentHash[:16], len(contentHash))

	// Check if CV already exists
	existingCV, err := a.db.FindCVByHash(r.Context(), contentHash)
	if err != nil {
		log.Printf("[DUPLICATE CHECK] Error checking for duplicate CV: %v", err)
		// Continue with upload even if duplicate check fails
	} else if existingCV != nil {
		// CV already exists - return existing info
		log.Printf("[DUPLICATE CHECK] Duplicate CV detected: %s (existing ID: %d)", parsedCV.Filename, existingCV.ID)

		response := map[string]interface{}{
			"cv_id":              existingCV.ID,
			"filename":           existingCV.Filename,
			"file_size":          existingCV.FileSize,
			"status":             "duplicate",
			"message":            "This CV has already been uploaded",
			"original_upload_at": existingCV.UploadedAt,
			"duplicate":          true,
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
		return
	}

	// Save CV file to database with hash
	log.Printf("[DUPLICATE CHECK] Saving CV with hash to database...")
	cvID, err := a.db.SaveCVFileWithHash(r.Context(), nil, parsedCV.Filename,
		parsedCV.Filename, parsedCV.FileType, parsedCV.FullText, parsedCV.FileSize, contentHash)
	if err != nil {
		log.Printf("Failed to save CV: %v", err)
		http.Error(w, "failed to save CV", http.StatusInternalServerError)
		return
	}

	log.Printf("CV saved to database with ID: %d (hash: %s...)", cvID, contentHash[:16])

	// Create async processing job
	jobID, err := a.db.CreateCVUploadJob(r.Context(), int64(cvID))
	if err != nil {
		log.Printf("Failed to create job: %v", err)
		http.Error(w, "failed to create processing job", http.StatusInternalServerError)
		return
	}

	log.Printf("Created job %d for CV %d", jobID, cvID)

	// Queue job for background processing
	a.queueCVProcessingJob(jobID, int64(cvID), parsedCV.FullText)

	processingTime := time.Since(startTime).Milliseconds()

	// Return immediately with job info (async response)
	response := map[string]interface{}{
		"cv_id":              cvID,
		"job_id":             jobID,
		"filename":           parsedCV.Filename,
		"file_type":          parsedCV.FileType,
		"file_size":          parsedCV.FileSize,
		"text_length":        len(parsedCV.FullText),
		"status":             "pending",
		"message":            "CV uploaded successfully. Processing in background.",
		"processing_time_ms": processingTime,
		"check_status_url":   fmt.Sprintf("/api/cv/job/%d", jobID),
	}

	log.Printf("CV upload complete - instant response in %dms (job %d queued for processing)", processingTime, jobID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted) // 202 Accepted (async processing)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("ERROR: Failed to encode JSON response: %v", err)
	} else {
		log.Printf("Response sent successfully for CV %d (job %d)", cvID, jobID)
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

// GetJobStatusHandler returns the status of a CV processing job
// @Summary Get CV processing job status
// @Description Get the current status of an async CV processing job
// @Tags cv
// @Produce json
// @Param job_id path int true "Job ID"
// @Success 200 {object} map[string]interface{}
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /cv/job/{job_id} [get]
func (a *API) GetJobStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract job_id from URL path
	// Expected format: /api/cv/job/123
	pathParts := strings.Split(r.URL.Path, "/")
	if len(pathParts) < 5 {
		http.Error(w, "invalid job ID", http.StatusBadRequest)
		return
	}

	var jobID int64
	_, err := fmt.Sscanf(pathParts[4], "%d", &jobID)
	if err != nil {
		http.Error(w, "invalid job ID format", http.StatusBadRequest)
		return
	}

	// Get job from database
	job, err := a.db.GetJobByID(r.Context(), jobID)
	if err != nil {
		log.Printf("Failed to get job %d: %v", jobID, err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	if job == nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	// Prepare response
	response := map[string]interface{}{
		"job_id":     job.ID,
		"cv_file_id": job.CVFileID,
		"status":     job.Status,
		"created_at": job.CreatedAt,
	}

	if job.StartedAt != nil {
		response["started_at"] = job.StartedAt
	}
	if job.CompletedAt != nil {
		response["completed_at"] = job.CompletedAt
	}
	if job.ErrorMessage != nil {
		response["error"] = *job.ErrorMessage
	}
	if job.Status == "completed" {
		response["message"] = "CV processing completed successfully"
	} else if job.Status == "processing" {
		response["message"] = "CV processing in progress"
	} else if job.Status == "pending" {
		response["message"] = "CV processing queued"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
