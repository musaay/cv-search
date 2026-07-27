package api_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"cv-search/internal/api"
)

func TestMergeCandidatesHandler_Validation(t *testing.T) {
	apiHandler := &api.API{}

	tests := []struct {
		name       string
		payload    string
		wantStatus int
	}{
		{
			name:       "invalid json",
			payload:    `{invalid json}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing master_candidate_id",
			payload:    `{"duplicate_candidate_ids": [10, 20]}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing duplicate_candidate_ids",
			payload:    `{"master_candidate_id": 10}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty duplicate_candidate_ids array",
			payload:    `{"master_candidate_id": 10, "duplicate_candidate_ids": []}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/candidates/merge", bytes.NewBufferString(tc.payload))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			apiHandler.MergeCandidatesHandler(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("expected status %d, got %d. Body: %s", tc.wantStatus, rec.Code, rec.Body.String())
			}
		})
	}
}
