package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"cv-search/internal/storage"
)

// ─── Request/Response types ───────────────────────────────────────────────────

type listCandidatesResponse struct {
	Candidates []storage.CandidateListItem `json:"candidates"`
	Total      int                         `json:"total"`
	Limit      int                         `json:"limit"`
	Offset     int                         `json:"offset"`
}

type interviewRequest struct {
	InterviewDate   string `json:"interview_date"` // "2026-03-06"
	Team            string `json:"team"`
	InterviewerName string `json:"interviewer_name"`
	InterviewType   string `json:"interview_type"` // technical, hr, case_study, other
	Notes           string `json:"notes"`
	Outcome         string `json:"outcome"` // pre_interview, interview, decision_pending, hired, rejected_*, withdrawn, pending, reserved_*
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func parseCandidateID(r *http.Request) (int, error) {
	raw := r.PathValue("id")
	if raw == "" {
		return 0, errors.New("missing candidate id")
	}
	return strconv.Atoi(raw)
}

func parseInterviewID(r *http.Request) (int, error) {
	raw := r.PathValue("iid")
	if raw == "" {
		return 0, errors.New("missing interview id")
	}
	return strconv.Atoi(raw)
}

func (req *interviewRequest) toInterview() (storage.Interview, error) {
	validTypes := map[string]bool{"technical": true, "hr": true, "case_study": true, "other": true, "": true}
	validOutcomes := map[string]bool{
		"pre_interview":                true,
		"interview":                    true,
		"decision_pending":             true,
		"hired":                        true,
		"rejected_pre_interview":       true,
		"rejected_interview":           true,
		"withdrawn":                    true,
		"pending":                      true,
		"rejected_other_team_possible": true,
		"reserved":                     true,
		"different_account":            true,
		"contact_for_slot":             true,
		"reserved_future_hire":         true,
		"":                             true,
	}

	if req.InterviewDate == "" {
		return storage.Interview{}, errors.New("interview_date is required")
	}
	date, err := time.Parse("2006-01-02", req.InterviewDate)
	if err != nil {
		return storage.Interview{}, errors.New("interview_date must be in YYYY-MM-DD format")
	}
	if !validTypes[req.InterviewType] {
		return storage.Interview{}, errors.New("interview_type must be one of: technical, hr, case_study, other")
	}
	if !validOutcomes[req.Outcome] {
		return storage.Interview{}, errors.New("outcome must be one of: pre_interview, interview, decision_pending, hired, rejected_pre_interview, rejected_interview, withdrawn, pending, rejected_other_team_possible, reserved, different_account, contact_for_slot, reserved_future_hire")
	}

	return storage.Interview{
		InterviewDate:   date,
		Team:            req.Team,
		InterviewerName: req.InterviewerName,
		InterviewType:   req.InterviewType,
		Notes:           req.Notes,
		Outcome:         req.Outcome,
	}, nil
}

// reEmbed fetches current interview notes and re-embeds the person node.
// Runs in the background — does not block the HTTP response.
// Non-fatal: all errors are logged, never propagated.
func (a *API) reEmbed(candidateID int) {
	if a.hybridSearchEngine == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	graphNodeID, err := a.db.GetGraphNodeIDForCandidate(ctx, candidateID)
	if err != nil {
		log.Printf("[CandidateHandler] reEmbed: could not get graph_node_id for candidate %d: %v", candidateID, err)
		return
	}
	if graphNodeID == 0 {
		// Candidate not yet linked to a graph node (rare race); skip silently
		return
	}

	notes, err := a.db.GetInterviewNotesByGraphNodeID(ctx, graphNodeID)
	if err != nil {
		log.Printf("[CandidateHandler] reEmbed: could not fetch interview notes for node %d: %v", graphNodeID, err)
		return
	}

	if err := a.hybridSearchEngine.ReEmbedPersonNode(ctx, graphNodeID, notes); err != nil {
		log.Printf("[CandidateHandler] reEmbed: failed for node %d: %v", graphNodeID, err)
	}
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

// ListCandidatesHandler returns a paginated list of candidates with basic enrichment.
func (a *API) ListCandidatesHandler(w http.ResponseWriter, r *http.Request) {
	limit := 50
	offset := 0

	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	candidates, err := a.db.ListCandidates(r.Context(), limit, offset)
	if err != nil {
		log.Printf("[CandidateHandler] ListCandidates failed: %v", err)
		http.Error(w, "failed to list candidates", http.StatusInternalServerError)
		return
	}

	if candidates == nil {
		candidates = []storage.CandidateListItem{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(listCandidatesResponse{
		Candidates: candidates,
		Total:      len(candidates),
		Limit:      limit,
		Offset:     offset,
	})
}

// GetCandidateHandler returns the full candidate profile including all interviews.
func (a *API) GetCandidateHandler(w http.ResponseWriter, r *http.Request) {
	id, err := parseCandidateID(r)
	if err != nil {
		http.Error(w, "invalid candidate id", http.StatusBadRequest)
		return
	}

	candidate, err := a.db.GetCandidateDetail(r.Context(), id)
	if err != nil {
		log.Printf("[CandidateHandler] GetCandidateDetail(%d) failed: %v", id, err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	if candidate == nil {
		http.Error(w, "candidate not found", http.StatusNotFound)
		return
	}

	if candidate.Interviews == nil {
		candidate.Interviews = []storage.Interview{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(candidate)
}

// CreateInterviewHandler adds a new interview record for a candidate.
func (a *API) CreateInterviewHandler(w http.ResponseWriter, r *http.Request) {
	candidateID, err := parseCandidateID(r)
	if err != nil {
		http.Error(w, "invalid candidate id", http.StatusBadRequest)
		return
	}

	var req interviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	iv, err := req.toInterview()
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	newID, err := a.db.CreateInterview(r.Context(), candidateID, iv)
	if err != nil {
		log.Printf("[CandidateHandler] CreateInterview(candidate=%d) failed: %v", candidateID, err)
		http.Error(w, "failed to create interview", http.StatusInternalServerError)
		return
	}

	// Re-embed in background — don't block the HTTP response
	go a.reEmbed(candidateID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":           newID,
		"candidate_id": candidateID,
		"message":      "interview created; embedding update queued",
	})
}

// UpdateInterviewHandler updates an existing interview record.
func (a *API) UpdateInterviewHandler(w http.ResponseWriter, r *http.Request) {
	candidateID, err := parseCandidateID(r)
	if err != nil {
		http.Error(w, "invalid candidate id", http.StatusBadRequest)
		return
	}
	interviewID, err := parseInterviewID(r)
	if err != nil {
		http.Error(w, "invalid interview id", http.StatusBadRequest)
		return
	}

	var req interviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	iv, err := req.toInterview()
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	if err := a.db.UpdateInterview(r.Context(), interviewID, candidateID, iv); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "interview not found", http.StatusNotFound)
			return
		}
		log.Printf("[CandidateHandler] UpdateInterview(%d, candidate=%d) failed: %v", interviewID, candidateID, err)
		http.Error(w, "failed to update interview", http.StatusInternalServerError)
		return
	}

	go a.reEmbed(candidateID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"message": "interview updated; embedding update queued"})
}

// DeleteInterviewHandler removes an interview record.
// DELETE /api/candidates/{id}/interviews/{iid}
func (a *API) DeleteInterviewHandler(w http.ResponseWriter, r *http.Request) {
	candidateID, err := parseCandidateID(r)
	if err != nil {
		http.Error(w, "invalid candidate id", http.StatusBadRequest)
		return
	}
	interviewID, err := parseInterviewID(r)
	if err != nil {
		http.Error(w, "invalid interview id", http.StatusBadRequest)
		return
	}

	if err := a.db.DeleteInterview(r.Context(), interviewID, candidateID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "interview not found", http.StatusNotFound)
			return
		}
		log.Printf("[CandidateHandler] DeleteInterview(%d, candidate=%d) failed: %v", interviewID, candidateID, err)
		http.Error(w, "failed to delete interview", http.StatusInternalServerError)
		return
	}

	go a.reEmbed(candidateID)

	w.WriteHeader(http.StatusNoContent)
}

// ─── Similar Candidates ───────────────────────────────────────────────────────

type similarCandidatesResponse struct {
	SourceCandidateID int                        `json:"source_candidate_id"`
	TopK              int                        `json:"top_k"`
	Similar           []storage.SimilarCandidate `json:"similar"`
}

// SimilarCandidatesHandler returns candidates whose embedding vector is closest
// to the requested candidate's vector.
//
//	GET /api/candidates/{id}/similar?top_k=5
//
// Requires the candidate to have been embedded (via a CV upload). Returns
// at most top_k results (default 5, max 20). Candidates without an embedding
// will receive an empty list.
func (a *API) SimilarCandidatesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	candidateID, err := parseCandidateID(r)
	if err != nil {
		http.Error(w, "invalid candidate id", http.StatusBadRequest)
		return
	}

	topK := 5
	if v := r.URL.Query().Get("top_k"); v != "" {
		n, _ := strconv.Atoi(v)
		if n >= 1 && n <= 20 {
			topK = n
		}
	}

	ctx := r.Context()

	// Step 1: get integer graph_node_id
	graphNodeID, err := a.db.GetGraphNodeIDForCandidate(ctx, candidateID)
	if err != nil {
		log.Printf("[Similar] GetGraphNodeIDForCandidate(%d): %v", candidateID, err)
		http.Error(w, "candidate lookup failed", http.StatusInternalServerError)
		return
	}
	if graphNodeID == 0 {
		// Candidate exists but graph hasn't been built yet
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(similarCandidatesResponse{
			SourceCandidateID: candidateID,
			TopK:              topK,
			Similar:           []storage.SimilarCandidate{},
		})
		return
	}

	// Step 2: get string node_id ("person_2") — needed to exclude source from results
	sourceNodeID, err := a.db.GetPersonNodeIDString(ctx, graphNodeID)
	if err != nil {
		log.Printf("[Similar] GetPersonNodeIDString(graphNodeID=%d): %v", graphNodeID, err)
		http.Error(w, "graph node lookup failed", http.StatusInternalServerError)
		return
	}
	if sourceNodeID == "" {
		// Person node doesn't exist yet — graph not built
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(similarCandidatesResponse{
			SourceCandidateID: candidateID,
			TopK:              topK,
			Similar:           []storage.SimilarCandidate{},
		})
		return
	}

	// Step 3: load embedding vector
	embedding, err := a.db.GetPersonEmbedding(ctx, graphNodeID)
	if err != nil {
		log.Printf("[Similar] GetPersonEmbedding(graphNodeID=%d): %v", graphNodeID, err)
		http.Error(w, "embedding lookup failed", http.StatusInternalServerError)
		return
	}
	if len(embedding) == 0 {
		// No embedding yet — return empty result set, not an error
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(similarCandidatesResponse{
			SourceCandidateID: candidateID,
			TopK:              topK,
			Similar:           []storage.SimilarCandidate{},
		})
		return
	}

	// Step 4: nearest-neighbour search via pgvector
	embSvc := a.hybridSearchEngine.GetEmbeddingService()
	if embSvc == nil {
		log.Printf("[Similar] embedding service unavailable")
		http.Error(w, "embedding service unavailable", http.StatusServiceUnavailable)
		return
	}

	// Fetch topK+1 so we can exclude the source candidate itself from results
	nodeIDs, sims, err := embSvc.SimilaritySearchByEmbedding(ctx, embedding, topK+1)
	if err != nil {
		log.Printf("[Similar] SimilaritySearchByEmbedding: %v", err)
		http.Error(w, "similarity search failed", http.StatusInternalServerError)
		return
	}

	// Step 5: build similarity map and fetch full candidate info
	simMap := make(map[string]float64, len(nodeIDs))
	for i, nid := range nodeIDs {
		simMap[nid] = sims[i]
	}

	similar, err := a.db.GetCandidatesByPersonNodeIDs(ctx, nodeIDs, sourceNodeID, simMap)
	if err != nil {
		log.Printf("[Similar] GetCandidatesByPersonNodeIDs: %v", err)
		http.Error(w, "enrichment query failed", http.StatusInternalServerError)
		return
	}

	// Cap to topK (excludeNodeID may have already trimmed one result)
	if len(similar) > topK {
		similar = similar[:topK]
	}
	if similar == nil {
		similar = []storage.SimilarCandidate{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(similarCandidatesResponse{
		SourceCandidateID: candidateID,
		TopK:              topK,
		Similar:           similar,
	})
}
