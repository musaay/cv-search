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
	InterviewDate   string `json:"interview_date"`    // "2026-03-06"
	Team            string `json:"team"`
	InterviewerName string `json:"interviewer_name"`
	InterviewType   string `json:"interview_type"`    // technical, hr, case_study, other
	Notes           string `json:"notes"`
	Outcome         string `json:"outcome"`           // passed, failed, pending
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
	validOutcomes := map[string]bool{"passed": true, "failed": true, "pending": true, "": true}

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
		return storage.Interview{}, errors.New("outcome must be one of: passed, failed, pending")
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
