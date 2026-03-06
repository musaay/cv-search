package api

import (
	"net/http"
	"os"
	"strings"

	httpSwagger "github.com/swaggo/http-swagger"
)

// corsMiddleware adds CORS headers to every response.
// Allowed origins are read from the CORS_ORIGINS env variable as a comma-separated list.
// Examples:
//   CORS_ORIGINS=*                                          → allow all (dev default)
//   CORS_ORIGINS=https://app.example.com                   → single origin
//   CORS_ORIGINS=https://app.example.com,https://admin.example.com → multiple origins
func corsMiddleware(next http.Handler) http.Handler {
	raw := os.Getenv("CORS_ORIGINS")
	if raw == "" {
		raw = "*"
	}

	// Parse once at startup into a set for O(1) lookup.
	wildcard := false
	allowedSet := make(map[string]struct{})
	for _, o := range strings.Split(raw, ",") {
		o = strings.TrimSpace(o)
		if o == "*" {
			wildcard = true
			break
		}
		allowedSet[strings.ToLower(o)] = struct{}{}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if wildcard {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if origin != "" {
			if _, ok := allowedSet[strings.ToLower(origin)]; ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}
		}

		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
		w.Header().Set("Access-Control-Max-Age", "86400")

		// Handle preflight
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func NewRouter(a *API) http.Handler {
	mux := http.NewServeMux()

	// Swagger documentation - must be registered first
	mux.Handle("/swagger/", httpSwagger.Handler(
		httpSwagger.URL("/swagger/doc.json"),
	))

	// Health check (for Railway, k8s, etc.)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"healthy"}`))
	})

	// API endpoints
	mux.HandleFunc("/api/search", a.SearchHandler)

	// CV & Graph endpoints
	mux.HandleFunc("/api/cv/upload", a.CVUploadHandler)
	mux.HandleFunc("/api/cv/bulk-upload", a.BulkCVUploadHandler) // Bulk upload (up to 10 files)
	mux.HandleFunc("/api/cv/batch/", a.GetBatchStatusHandler)    // Batch status
	mux.HandleFunc("/api/cv/job/", a.GetJobStatusHandler)        // Job status endpoint
	mux.HandleFunc("/api/graph/stats", a.GetGraphStatsHandler)
	mux.HandleFunc("/api/graph/skills/popular", a.GetPopularSkillsHandler)

	// GraphRAG endpoints
	mux.HandleFunc("/api/graphrag/search", a.GraphRAGSearchHandler)
	mux.HandleFunc("/api/graphrag/embeddings/generate", a.GenerateEmbeddingsHandler)
	mux.HandleFunc("/api/graphrag/communities/detect", a.DetectCommunitiesHandler)

	// Hybrid Search endpoint (BM25 + Vector + Graph + LLM)
	mux.HandleFunc("/api/search/hybrid", a.HybridSearchHandler)

	// Candidate management + interview tracking
	mux.HandleFunc("GET /api/candidates", a.ListCandidatesHandler)
	mux.HandleFunc("GET /api/candidates/{id}", a.GetCandidateHandler)
	mux.HandleFunc("POST /api/candidates/{id}/interviews", a.CreateInterviewHandler)
	mux.HandleFunc("PUT /api/candidates/{id}/interviews/{iid}", a.UpdateInterviewHandler)
	mux.HandleFunc("DELETE /api/candidates/{id}/interviews/{iid}", a.DeleteInterviewHandler)

	return corsMiddleware(mux)
}
