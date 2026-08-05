package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

// GetDuplicateCandidatesHandler returns candidate groups with matching normalized names for deduplication review.
// @Summary Get duplicate candidate groups
// @Description Returns groups of candidates sharing the same normalized name along with enriched profile data and a suggested master candidate ID for deduplication review.
// @Tags candidates
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /candidates/duplicates [get]
func (a *API) GetDuplicateCandidatesHandler(w http.ResponseWriter, r *http.Request) {
	groups, err := a.db.FindDuplicateCandidateGroups(r.Context())
	if err != nil {
		slog.Error(fmt.Sprintf("[DuplicateHandler] FindDuplicateCandidateGroups failed: %v", err))
		http.Error(w, "failed to find duplicate candidate groups", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"groups":       groups,
		"total_groups": len(groups),
	})
}

// mergeCandidatesRequest represents the payload for merging duplicate candidates.
type mergeCandidatesRequest struct {
	MasterCandidateID     int   `json:"master_candidate_id"`
	DuplicateCandidateIDs []int `json:"duplicate_candidate_ids"`
}

// MergeCandidatesHandler merges selected duplicate candidates into a master candidate.
// @Summary Merge duplicate candidates
// @Description Atomically merges duplicate candidates and their graph relationships (skills, companies, education, CV files, interviews, scores) into a master candidate, then deletes the duplicate records.
// @Tags candidates
// @Accept json
// @Produce json
// @Param request body mergeCandidatesRequest true "Merge request containing master_candidate_id and duplicate_candidate_ids"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "Invalid request payload"
// @Failure 500 {string} string "Failed to merge candidates"
// @Router /candidates/merge [post]
func (a *API) MergeCandidatesHandler(w http.ResponseWriter, r *http.Request) {
	var req mergeCandidatesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON payload", http.StatusBadRequest)
		return
	}

	if req.MasterCandidateID <= 0 || len(req.DuplicateCandidateIDs) == 0 {
		http.Error(w, "master_candidate_id and duplicate_candidate_ids are required", http.StatusBadRequest)
		return
	}

	if err := a.db.MergeCandidateNodes(r.Context(), req.MasterCandidateID, req.DuplicateCandidateIDs); err != nil {
		slog.Error(fmt.Sprintf("[DuplicateHandler] MergeCandidateNodes failed (master=%d, dups=%v): %v", req.MasterCandidateID, req.DuplicateCandidateIDs, err))
		http.Error(w, "failed to merge candidates: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Fetch updated master profile
	masterDetail, err := a.db.GetCandidateDetail(r.Context(), req.MasterCandidateID)
	if err != nil {
		slog.Error(fmt.Sprintf("[DuplicateHandler] GetCandidateDetail after merge failed for candidate %d: %v", req.MasterCandidateID, err))
		// Still return success even if fetch fails
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "candidates merged successfully",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":          true,
		"message":          "candidates merged successfully",
		"master_candidate": masterDetail,
	})
}
